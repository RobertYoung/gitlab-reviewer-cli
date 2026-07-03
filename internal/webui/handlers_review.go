package webui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review/publisher"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review/resultstore"
)

// handleReviewStart kicks off a review run and redirects to its progress
// page. The MR's accepted pending comments ride along into the run.
func (s *Server) handleReviewStart(w http.ResponseWriter, r *http.Request, d *Deps) {
	inst := r.PathValue("inst")
	detail, err := fetchDetail(r, d)
	if err != nil {
		s.renderError(w, http.StatusBadGateway, err)
		return
	}
	diffs, err := d.Svc.ListDiffs(r.Context(), detail.Project(), detail.IID)
	if err != nil {
		s.renderError(w, http.StatusBadGateway, err)
		return
	}
	commits, _ := d.Svc.ListCommits(r.Context(), detail.Project(), detail.IID) // best-effort
	_ = r.ParseForm()
	agentNames := r.Form["agents"]
	agentModels := parseAgentModels(r.Form, agentNames)
	if len(agentNames) > 0 {
		d.Selection.Save(detail.ProjectPath, agentNames)
		d.Selection.SaveModels(detail.ProjectPath, agentModels)
	}
	run := s.startRun(d, inst, *detail, diffs, commits, agentNames, agentModels)
	http.Redirect(w, r, instPath(inst, "/run/"+run.ID), http.StatusSeeOther) //nolint:gosec // server-built path: escaped instance + generated run ID
}

// parseAgentModels reads the per-agent model picks from the review form
// (fields named "model:<agent>"), restricted to the checked agents and
// dropping the empty "(default)" selections.
func parseAgentModels(form url.Values, agentNames []string) map[string]string {
	selected := map[string]bool{}
	for _, n := range agentNames {
		selected[n] = true
	}
	models := map[string]string{}
	for field, vals := range form {
		name, ok := strings.CutPrefix(field, "model:")
		if !ok || len(vals) == 0 || vals[0] == "" || !selected[name] {
			continue
		}
		models[name] = vals[0]
	}
	return models
}

type runContent struct {
	Nav       mrNav
	Run       *reviewRun
	Lines     []string
	Done      bool
	Outcome   *runOutcome
	EventsURL string
	CancelURL string
	// FindingsURL / LogURL are set once the outcome is known.
	FindingsURL string
	LogURL      string
	WebURL      string
}

func (s *Server) runURLs(inst string, run *reviewRun, out *runOutcome) (findings, log string) {
	if out == nil {
		return "", ""
	}
	if out.RecName != "" {
		findings = mrURL(inst, "/mr/findings", run.Project, run.IID, url.Values{"record": {out.RecName}})
	}
	if out.LogName != "" {
		log = mrURL(inst, "/mr/log", run.Project, run.IID, url.Values{"name": {out.LogName}})
	}
	return findings, log
}

// handleRunPage shows one review run: the streamed progress log while it
// runs (via SSE), then the outcome.
func (s *Server) handleRunPage(w http.ResponseWriter, r *http.Request, _ *Deps) {
	inst := r.PathValue("inst")
	run := s.runs.get(r.PathValue("run"))
	if run == nil || run.Instance != inst {
		s.renderError(w, http.StatusNotFound, errors.New("unknown review run"))
		return
	}
	lines, done, out := run.snapshot()
	content := runContent{
		Nav:       newMRNav(inst, run.Project, run.IID),
		Run:       run,
		Lines:     lines,
		Done:      done,
		Outcome:   out,
		EventsURL: instPath(inst, "/run/"+run.ID+"/events"),
		CancelURL: instPath(inst, "/run/"+run.ID+"/cancel"),
		WebURL:    run.WebURL,
	}
	content.FindingsURL, content.LogURL = s.runURLs(inst, run, out)
	s.render(w, http.StatusOK, "run", pageData{Title: "reviewing " + run.Ref, Instance: inst, Content: content})
}

// handleRunEvents streams a run's progress as server-sent events: replayed
// history first, live lines after, and a final "done" event with the
// redirect target.
func (s *Server) handleRunEvents(w http.ResponseWriter, r *http.Request, _ *Deps) {
	inst := r.PathValue("inst")
	run := s.runs.get(r.PathValue("run"))
	if run == nil || run.Instance != inst {
		http.NotFound(w, r)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")

	writeLine := func(line string) {
		data, _ := json.Marshal(line)
		_, _ = fmt.Fprintf(w, "event: line\ndata: %s\n\n", data)
	}
	writeDone := func(out *runOutcome) {
		findings, log := s.runURLs(inst, run, out)
		payload, _ := json.Marshal(map[string]any{
			"cancelled":   out.Cancelled,
			"error":       out.Err,
			"findingsUrl": findings,
			"logUrl":      log,
			"findings":    out.Findings,
		})
		_, _ = fmt.Fprintf(w, "event: done\ndata: %s\n\n", payload)
	}

	replay, done, ch := run.subscribe()
	for _, l := range replay {
		writeLine(l)
	}
	if done != nil {
		writeDone(done)
		flusher.Flush()
		return
	}
	defer run.unsubscribe(ch)
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case ev := <-ch:
			if ev.Done != nil {
				writeDone(ev.Done)
				flusher.Flush()
				return
			}
			writeLine(ev.Line)
			flusher.Flush()
		}
	}
}

// handleRunCancel cancels an in-flight run.
func (s *Server) handleRunCancel(w http.ResponseWriter, r *http.Request, _ *Deps) {
	inst := r.PathValue("inst")
	run := s.runs.get(r.PathValue("run"))
	if run == nil || run.Instance != inst {
		http.NotFound(w, r)
		return
	}
	if run.cancel != nil {
		run.append("cancelling…")
		run.cancel()
	}
	http.Redirect(w, r, instPath(inst, "/run/"+run.ID), http.StatusSeeOther) //nolint:gosec // server-built path: escaped instance + generated run ID
}

// loadRecord resolves and loads the ?record= reference on the request.
func (s *Server) loadRecord(r *http.Request, d *Deps) (rec resultstore.Record, name string, err error) {
	name = r.FormValue("record")
	path, err := s.safeStoreFile(name, ".json")
	if err != nil {
		return rec, name, err
	}
	rec, err = d.Results.Load(path)
	return rec, name, err
}

// findingItem is one finding prepared for the findings page.
type findingItem struct {
	F       review.Finding
	Excerpt []diffLine
}

type findingsContent struct {
	Nav        mrNav
	Detail     *gitlabx.MRDetail
	Rec        resultstore.Record
	RecordName string
	Items      []findingItem
	Accepted   int
	StateURL   string // POST target
	PublishURL string
	LogURL     string
}

// handleFindings shows a stored review for triage: accept, reject, edit,
// then publish. It serves fresh runs and reopened past reviews alike.
func (s *Server) handleFindings(w http.ResponseWriter, r *http.Request, d *Deps) {
	inst := r.PathValue("inst")
	project, iid, err := mrQuery(r)
	if err != nil {
		s.renderError(w, http.StatusBadRequest, err)
		return
	}
	rec, recName, err := s.loadRecord(r, d)
	if err != nil {
		s.renderError(w, http.StatusBadRequest, err)
		return
	}
	detail, err := d.Svc.GetMergeRequest(r.Context(), parseProject(project), iid)
	if err != nil {
		s.renderError(w, http.StatusBadGateway, err)
		return
	}
	diffs, _ := d.Svc.ListDiffs(r.Context(), detail.Project(), iid) // context excerpts only

	content := findingsContent{
		Nav:        newMRNav(inst, project, iid),
		Detail:     detail,
		Rec:        rec,
		RecordName: recName,
		StateURL:   instPath(inst, "/mr/findings/state"),
		PublishURL: mrURL(inst, "/mr/publish", project, iid, url.Values{"record": {recName}}),
	}
	if rec.LogPath != "" {
		content.LogURL = mrURL(inst, "/mr/log", project, iid, url.Values{"name": {filepath.Base(rec.LogPath)}})
	}
	for _, f := range rec.Findings {
		content.Items = append(content.Items, findingItem{F: f, Excerpt: hunkExcerptHTML(diffs, f, 4)})
		if f.State == review.StateAccepted {
			content.Accepted++
		}
	}
	s.render(w, http.StatusOK, "findings", pageData{Title: "findings · " + detail.Ref(), Instance: inst, Content: content})
}

// handleFindingState curates a stored review: accept, reject, accept-all,
// or edit a finding's body. Every change re-saves the record, same as the
// TUI findings screen.
func (s *Server) handleFindingState(w http.ResponseWriter, r *http.Request, d *Deps) {
	inst := r.PathValue("inst")
	project, iid, err := mrQuery(r)
	if err != nil {
		s.renderError(w, http.StatusBadRequest, err)
		return
	}
	rec, recName, err := s.loadRecord(r, d)
	if err != nil {
		s.renderError(w, http.StatusBadRequest, err)
		return
	}
	id, action := r.FormValue("id"), r.FormValue("action")
	for i := range rec.Findings {
		f := &rec.Findings[i]
		switch action {
		case "accept-all":
			if f.State == review.StatePending {
				f.State = review.StateAccepted
			}
		case "accept":
			if f.ID == id {
				f.State = review.StateAccepted
			}
		case "reject":
			if f.ID == id {
				f.State = review.StateRejected
			}
		case "edit":
			if f.ID == id {
				if body := strings.TrimSpace(r.FormValue("body")); body != "" {
					f.Body = body
				}
			}
		}
	}
	if err := d.Results.Save(rec); err != nil {
		s.renderError(w, http.StatusInternalServerError, err)
		return
	}
	back := mrURL(inst, "/mr/findings", project, iid, url.Values{"record": {recName}})
	if id != "" {
		back += "#f-" + url.QueryEscape(id)
	}
	http.Redirect(w, r, back, http.StatusSeeOther) //nolint:gosec // server-built path with escaped query values
}

// publishItems resolves what a publish request applies to: a stored
// record's accepted findings, or the MR's pending manual comments.
func (s *Server) publishItems(r *http.Request, d *Deps, inst, project string, iid int64) (items []review.Finding, rec *resultstore.Record, err error) {
	if r.FormValue("source") == "comments" {
		return s.comments.accepted(mrKey(inst, project, iid)), nil, nil
	}
	loaded, _, err := s.loadRecord(r, d)
	if err != nil {
		return nil, nil, err
	}
	for _, f := range loaded.Findings {
		if f.State == review.StateAccepted {
			items = append(items, f)
		}
	}
	return items, &loaded, nil
}

type publishResult struct {
	F     review.Finding
	State review.FindingState
	Err   string
}

type publishContent struct {
	Nav        mrNav
	Detail     *gitlabx.MRDetail
	Items      []review.Finding
	Mode       string
	Source     string
	RecordName string
	PostURL    string
	// Result phase:
	Posted     bool
	Results    []publishResult
	Inline     int
	Notes      int
	Failed     int
	DraftReady bool
	ReviewURL  string // POST publish/review
	TmplErr    string
}

// handlePublishForm is the confirmation page: what will be posted, in
// which mode.
func (s *Server) handlePublishForm(w http.ResponseWriter, r *http.Request, d *Deps) {
	inst := r.PathValue("inst")
	project, iid, err := mrQuery(r)
	if err != nil {
		s.renderError(w, http.StatusBadRequest, err)
		return
	}
	detail, err := d.Svc.GetMergeRequest(r.Context(), parseProject(project), iid)
	if err != nil {
		s.renderError(w, http.StatusBadGateway, err)
		return
	}
	items, _, err := s.publishItems(r, d, inst, project, iid)
	if err != nil {
		s.renderError(w, http.StatusBadRequest, err)
		return
	}
	cfg := d.cfgFor(detail.ProjectPath)
	s.render(w, http.StatusOK, "publish", pageData{Title: "publish · " + detail.Ref(), Instance: inst, Content: publishContent{
		Nav:        newMRNav(inst, project, iid),
		Detail:     detail,
		Items:      items,
		Mode:       cfg.Publish.Mode,
		Source:     r.FormValue("source"),
		RecordName: r.FormValue("record"),
		PostURL:    instPath(inst, "/mr/publish"),
	}})
}

// handlePublish posts the accepted findings/comments sequentially through
// the shared publisher, records each outcome, and renders the result —
// with the one-click "publish review" step when draft notes were created.
func (s *Server) handlePublish(w http.ResponseWriter, r *http.Request, d *Deps) {
	inst := r.PathValue("inst")
	project, iid, err := mrQuery(r)
	if err != nil {
		s.renderError(w, http.StatusBadRequest, err)
		return
	}
	detail, err := d.Svc.GetMergeRequest(r.Context(), parseProject(project), iid)
	if err != nil {
		s.renderError(w, http.StatusBadGateway, err)
		return
	}
	diffs, err := d.Svc.ListDiffs(r.Context(), detail.Project(), iid)
	if err != nil {
		s.renderError(w, http.StatusBadGateway, err)
		return
	}
	items, rec, err := s.publishItems(r, d, inst, project, iid)
	if err != nil {
		s.renderError(w, http.StatusBadRequest, err)
		return
	}

	cfg := d.cfgFor(detail.ProjectPath)
	pub, tmplErr := publisher.New(d.Svc, *detail, diffs, cfg.Publish)
	mode := r.FormValue("mode")
	if mode != "draft" && mode != "immediate" {
		mode = cfg.Publish.Mode
	}
	pub.Draft = mode == "draft"

	content := publishContent{
		Nav:        newMRNav(inst, project, iid),
		Detail:     detail,
		Mode:       mode,
		Source:     r.FormValue("source"),
		RecordName: r.FormValue("record"),
		Posted:     true,
		ReviewURL:  instPath(inst, "/mr/publish/review"),
	}
	if tmplErr != nil {
		content.TmplErr = tmplErr.Error()
	}

	key := mrKey(inst, project, iid)
	for _, f := range items {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		state, err := pub.PublishOne(ctx, f)
		cancel()
		res := publishResult{F: f, State: state}
		if err != nil {
			res.Err = err.Error()
			content.Failed++
		}
		switch {
		case state == review.StatePublished && f.File == "":
			content.Notes++
		case state == review.StatePublished:
			content.Inline++
		case state == review.StateFellBack:
			content.Notes++
		}
		content.Results = append(content.Results, res)
		// Report the outcome where the item lives: the stored record, or
		// the pending comment list.
		if rec != nil {
			for i := range rec.Findings {
				if rec.Findings[i].ID == f.ID {
					rec.Findings[i].State = state
				}
			}
		} else {
			s.comments.setState(key, f.ID, state)
		}
	}
	if rec != nil {
		if err := d.Results.Save(*rec); err != nil {
			content.Results = append(content.Results, publishResult{Err: "could not re-save the review record: " + err.Error()})
		}
	}
	content.DraftReady = pub.Draft && content.Inline+content.Notes > 0
	s.render(w, http.StatusOK, "publish", pageData{Title: "publish · " + detail.Ref(), Instance: inst, Content: content})
}

// handlePublishReview publishes all pending draft notes in one action.
func (s *Server) handlePublishReview(w http.ResponseWriter, r *http.Request, d *Deps) {
	inst := r.PathValue("inst")
	project, iid, err := mrQuery(r)
	if err != nil {
		s.renderError(w, http.StatusBadRequest, err)
		return
	}
	detail, err := d.Svc.GetMergeRequest(r.Context(), parseProject(project), iid)
	if err != nil {
		s.renderError(w, http.StatusBadGateway, err)
		return
	}
	pub, _ := publisher.New(d.Svc, *detail, nil, d.cfgFor(detail.ProjectPath).Publish)
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	if err := pub.PublishReview(ctx); err != nil {
		s.renderError(w, http.StatusBadGateway, fmt.Errorf("publishing draft review: %w", err))
		return
	}
	http.Redirect(w, r, mrURL(inst, "/mr", project, iid, nil), http.StatusSeeOther) //nolint:gosec // server-built path with escaped query values
}

type historyRow struct {
	Started     time.Time
	Title       string
	Findings    int
	Accepted    int
	RecordOnly  bool
	FindingsURL string
	LogURL      string
}

type historyContent struct {
	Nav     mrNav
	Detail  *gitlabx.MRDetail
	Entries []historyRow
}

// handleHistory lists the stored reviews of one MR, newest first; entries
// reopen in the findings page with their curation states intact.
func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request, d *Deps) {
	inst := r.PathValue("inst")
	project, iid, err := mrQuery(r)
	if err != nil {
		s.renderError(w, http.StatusBadRequest, err)
		return
	}
	detail, err := d.Svc.GetMergeRequest(r.Context(), parseProject(project), iid)
	if err != nil {
		s.renderError(w, http.StatusBadGateway, err)
		return
	}
	records, err := d.Results.List(detail.Ref())
	if err != nil {
		s.renderError(w, http.StatusInternalServerError, err)
		return
	}
	logs, err := d.Logs.List(detail.Ref())
	if err != nil {
		s.renderError(w, http.StatusInternalServerError, err)
		return
	}

	logURL := func(path string) string {
		if path == "" {
			return ""
		}
		return mrURL(inst, "/mr/log", project, iid, url.Values{"name": {filepath.Base(path)}})
	}
	// One entry per run: records lead; logs whose run has a record fold
	// into it, the rest (runs predating result storage, or failed before a
	// result) get log-only entries.
	covered := map[string]bool{}
	var entries []historyRow
	for _, rec := range records {
		covered[rec.LogPath] = true
		entries = append(entries, historyRow{
			Started:     rec.Started,
			Title:       rec.Title,
			Findings:    rec.Findings,
			Accepted:    rec.Accepted,
			FindingsURL: mrURL(inst, "/mr/findings", project, iid, url.Values{"record": {filepath.Base(rec.Path)}}),
			LogURL:      logURL(rec.LogPath),
		})
	}
	for _, l := range logs {
		if covered[l.Path] {
			continue
		}
		entries = append(entries, historyRow{Started: l.Started, Title: l.Title, LogURL: logURL(l.Path)})
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Started.After(entries[j].Started) })

	s.render(w, http.StatusOK, "history", pageData{Title: "past reviews · " + detail.Ref(), Instance: inst, Content: historyContent{
		Nav:     newMRNav(inst, project, iid),
		Detail:  detail,
		Entries: entries,
	}})
}

type logContent struct {
	Nav  mrNav
	Name string
	Text string
}

// handleLogView shows one stored run log.
func (s *Server) handleLogView(w http.ResponseWriter, r *http.Request, _ *Deps) {
	inst := r.PathValue("inst")
	project, iid, err := mrQuery(r)
	if err != nil {
		s.renderError(w, http.StatusBadRequest, err)
		return
	}
	name := r.FormValue("name")
	path, err := s.safeStoreFile(name, ".log")
	if err != nil {
		s.renderError(w, http.StatusBadRequest, err)
		return
	}
	data, err := os.ReadFile(path) //nolint:gosec // constrained to the reviews directory by safeStoreFile
	if err != nil {
		s.renderError(w, http.StatusNotFound, err)
		return
	}
	s.render(w, http.StatusOK, "log", pageData{Title: "review log", Instance: inst, Content: logContent{
		Nav:  newMRNav(inst, project, iid),
		Name: name,
		Text: string(data),
	}})
}
