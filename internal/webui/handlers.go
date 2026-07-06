package webui

import (
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review/agents"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review/resultstore"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review/runner"
)

// instPath builds an instance-scoped URL path.
func instPath(inst, rest string) string {
	return "/i/" + url.PathEscape(inst) + rest
}

// mrURL builds an MR-scoped URL: page is e.g. "/mr/diff"; extra params are
// appended after project and iid.
func mrURL(inst, page, project string, iid int64, extra url.Values) string {
	q := url.Values{"project": {project}, "iid": {fmt.Sprint(iid)}}
	for k, vs := range extra {
		q[k] = vs
	}
	return instPath(inst, page) + "?" + q.Encode()
}

// parseProject turns the project query value into the API identifier: a
// numeric ID when the path was unknown, otherwise the full path.
func parseProject(s string) any {
	if id, err := strconv.ParseInt(s, 10, 64); err == nil {
		return id
	}
	return s
}

// localRedirect follows a form's "back" target, restricted to local paths
// so the parameter cannot bounce the browser off the session.
func localRedirect(w http.ResponseWriter, r *http.Request, back, fallback string) {
	if back == "" || !strings.HasPrefix(back, "/") || strings.HasPrefix(back, "//") {
		back = fallback
	}
	http.Redirect(w, r, back, http.StatusSeeOther) //nolint:gosec // restricted to a local path above
}

// handleHome is the instance picker; with one instance it redirects
// straight to the MR list.
func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	instances := s.instanceList()
	if len(instances) == 1 {
		http.Redirect(w, r, instPath(instances[0], "/"), http.StatusSeeOther)
		return
	}
	type instanceItem struct {
		Name string
		URL  string
	}
	items := make([]instanceItem, 0, len(instances))
	for _, name := range instances {
		items = append(items, instanceItem{Name: name, URL: instPath(name, "/")})
	}
	s.render(w, http.StatusOK, "home", pageData{Title: "instances", Content: items})
}

type mrListContent struct {
	Instance   string
	MRs        []gitlabx.MRSummary
	Reviewed   []*mrReviewedInfo // parallel to MRs; nil entries mean no stored review
	State      string
	Search     string
	Author     string
	Target     string
	Projects   string // scope override, comma-separated
	Groups     string
	NeedsScope bool   // no configured projects/groups: show the scope inputs
	BrowseURL  string // the scope picker, offered alongside the inputs
	Page       int
	PrevURL    string
	NextURL    string
	Err        string
	MRURL      func(gitlabx.MRSummary) string
}

// mrReviewedInfo decorates a listed MR that has stored reviews: how the
// latest one went, linking to the past-reviews page.
type mrReviewedInfo struct {
	Findings int
	Accepted int
	When     time.Time
	URL      string // past reviews of this MR
}

// handleMRList lists merge requests with the same filters as the TUI list.
func (s *Server) handleMRList(w http.ResponseWriter, r *http.Request, d *Deps) {
	inst := r.PathValue("inst")
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	page = max(page, 1)

	filter := gitlabx.MRFilter{
		State:          q.Get("state"),
		Search:         q.Get("search"),
		AuthorUsername: q.Get("author"),
		TargetBranch:   q.Get("target"),
		Projects:       splitList(q.Get("projects")),
		Groups:         splitList(q.Get("groups")),
	}

	content := mrListContent{
		Instance:   inst,
		State:      q.Get("state"),
		Search:     q.Get("search"),
		Author:     q.Get("author"),
		Target:     q.Get("target"),
		Projects:   q.Get("projects"),
		Groups:     q.Get("groups"),
		NeedsScope: len(d.Cfg.GitLab.Projects) == 0 && len(d.Cfg.GitLab.Groups) == 0,
		BrowseURL:  instPath(inst, "/browse"),
		Page:       page,
		MRURL: func(m gitlabx.MRSummary) string {
			project := m.ProjectPath
			if project == "" {
				project = fmt.Sprint(m.ProjectID)
			}
			return mrURL(inst, "/mr", project, m.IID, nil)
		},
	}

	// An empty scope with nothing configured would query nothing useful;
	// send the user to the scope picker instead.
	if content.NeedsScope && len(filter.Projects) == 0 && len(filter.Groups) == 0 {
		http.Redirect(w, r, content.BrowseURL, http.StatusSeeOther) //nolint:gosec // local path with the instance segment escaped
		return
	}

	mrs, hasMore, err := d.Svc.ListOpenMergeRequests(r.Context(), filter,
		gitlabx.Page{Number: page, PerPage: d.Cfg.GitLab.PerPage})
	if err != nil {
		content.Err = err.Error()
	}
	content.MRs = mrs
	// Decorate MRs that already have a stored review: one scan of the local
	// result store, keyed by MR ref (entries come back newest first).
	latest := map[string]resultstore.Entry{}
	if entries, err := d.Results.List(""); err == nil {
		for _, e := range entries {
			if _, ok := latest[e.Ref]; !ok {
				latest[e.Ref] = e
			}
		}
	}
	content.Reviewed = make([]*mrReviewedInfo, len(mrs))
	for i, m := range mrs {
		e, ok := latest[m.Ref()]
		if !ok || m.ProjectPath == "" {
			continue
		}
		content.Reviewed[i] = &mrReviewedInfo{
			Findings: e.Findings,
			Accepted: e.Accepted,
			When:     e.Started,
			URL:      mrURL(inst, "/mr/history", m.ProjectPath, m.IID, nil),
		}
	}
	if page > 1 {
		content.PrevURL = listPageURL(inst, q, page-1)
	}
	if hasMore {
		content.NextURL = listPageURL(inst, q, page+1)
	}
	s.render(w, http.StatusOK, "mrs", pageData{Title: "merge requests", Instance: inst, Content: content})
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func listPageURL(inst string, q url.Values, page int) string {
	nq := url.Values{}
	for k, vs := range q {
		nq[k] = vs
	}
	nq.Set("page", fmt.Sprint(page))
	return instPath(inst, "/") + "?" + nq.Encode()
}

// fetchDetail loads the full MR for the ?project=&iid= pair on the request.
func fetchDetail(r *http.Request, d *Deps) (*gitlabx.MRDetail, error) {
	project, iid, err := mrQuery(r)
	if err != nil {
		return nil, err
	}
	return d.Svc.GetMergeRequest(r.Context(), parseProject(project), iid)
}

// mrNav is the URL set shared by every MR-scoped page.
type mrNav struct {
	Instance    string
	Project     string
	IID         int64
	DetailURL   string
	DiffURL     string
	HistoryURL  string
	ReviewURL   string // POST target
	CommentURL  string // POST target
	DeleteURL   string // POST target
	ApproveURL  string // POST target
	PublishURL  string // GET confirm page for pending comments
	FindingsURL string
	ChatURL     string // POST target: start a conversation about the MR/line
}

func newMRNav(inst, project string, iid int64) mrNav {
	return mrNav{
		Instance:   inst,
		Project:    project,
		IID:        iid,
		DetailURL:  mrURL(inst, "/mr", project, iid, nil),
		DiffURL:    mrURL(inst, "/mr/diff", project, iid, nil),
		HistoryURL: mrURL(inst, "/mr/history", project, iid, nil),
		ReviewURL:  instPath(inst, "/mr/review"),
		CommentURL: instPath(inst, "/mr/comment"),
		DeleteURL:  instPath(inst, "/mr/comment/delete"),
		ApproveURL: instPath(inst, "/mr/approve"),
		PublishURL: mrURL(inst, "/mr/publish", project, iid, url.Values{"source": {"comments"}}),
		ChatURL:    instPath(inst, "/mr/chat/start"),
	}
}

type mrDetailContent struct {
	Nav           mrNav
	Detail        *gitlabx.MRDetail
	Commits       []gitlabx.Commit
	Pending       []review.Finding
	Approvals     *gitlabx.Approvals // nil when the instance exposes none
	RebaseWarning string
	HasAccepted   bool
	AgentOptions  []agentOption
	AgentWarnings []string
	// GateBlocking is how many findings in the last stored review block the
	// severity gate (0 when the gate is off or satisfied); GateSeverity is
	// its threshold and GateBlocked whether approval is refused outright.
	GateBlocking int
	GateSeverity string
	GateBlocked  bool
	// PrevReviewHead is the head commit of the MR's newest stored review;
	// when set the review form offers the full-re-review override (the run
	// defaults to reviewing only the changes pushed since that commit).
	PrevReviewHead string
}

// agentOption is one review agent offered on the run-review form.
type agentOption struct {
	Name        string
	Description string
	Source      string // "" for builtins; "user"/"project" shown as a badge
	Checked     bool
	// Models is the per-agent model dropdown: a "(default)" entry (empty
	// value) followed by the configured model list, with the current pick
	// pre-selected. Mirrors the TUI picker's "m" chooser.
	Models []agentModelOption
}

// agentModelOption is one entry in an agent's model dropdown.
type agentModelOption struct {
	Value    string // model ID; "" is the "(default)" entry
	Label    string
	Selected bool
}

// agentOptions builds the review form's agent checkboxes and per-agent model
// dropdowns: the catalog (including any repo-fetched project agents) in
// display order, pre-checked and pre-selected from the remembered selection
// and model picks, or the configured default.
func agentOptions(d *Deps, cat *agents.Catalog, projectPath string) []agentOption {
	cfg := d.cfgFor(projectPath)
	selected := d.Selection.Load(projectPath)
	if len(selected) == 0 {
		selected = cfg.Review.Agents
	}
	checked := map[string]bool{}
	for _, name := range selected {
		checked[name] = true
	}
	models := d.Selection.LoadModels(projectPath)
	modelChoices := cfg.ModelOptions()

	all := cat.All()
	opts := make([]agentOption, 0, len(all))
	anyChecked := false
	for _, a := range all {
		opt := agentOption{Name: a.Name, Description: a.Description, Checked: checked[a.Name]}
		if a.Source != agents.SourceBuiltin {
			opt.Source = string(a.Source)
		}
		fallback := cfg.Review.AgentModels[a.Name]
		if fallback == "" {
			fallback = a.Model
		}
		opt.Models = modelMenu(modelChoices, models[a.Name], fallback)
		anyChecked = anyChecked || opt.Checked
		opts = append(opts, opt)
	}
	if !anyChecked {
		for i := range opts {
			opts[i].Checked = true
		}
	}
	return opts
}

// modelMenu builds one agent's model dropdown. The "(default)" entry falls
// back to the agent's effective default — its review.agent_models entry,
// then its frontmatter model (surfaced in the label when set), then
// review.model; pick is the remembered override, pre-selected and kept as
// an entry even when it is not in the configured list.
func modelMenu(choices []string, pick, fallback string) []agentModelOption {
	defaultLabel := "(default)"
	if fallback != "" {
		defaultLabel = "default (" + fallback + ")"
	}
	menu := []agentModelOption{{Value: "", Label: defaultLabel, Selected: pick == ""}}
	found := false
	for _, m := range choices {
		menu = append(menu, agentModelOption{Value: m, Label: m, Selected: m == pick})
		found = found || m == pick
	}
	if pick != "" && !found {
		menu = append(menu, agentModelOption{Value: pick, Label: pick, Selected: true})
	}
	return menu
}

// handleMRDetail shows one MR: metadata, description, commits, pending
// comments, and the actions (diff, review, history, publish, GitLab).
func (s *Server) handleMRDetail(w http.ResponseWriter, r *http.Request, d *Deps) {
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
	commits, _ := d.Svc.ListCommits(r.Context(), detail.Project(), iid)    // best-effort
	approvals, _ := d.Svc.GetApprovals(r.Context(), detail.Project(), iid) // decoration; page works without it
	pending := s.comments.list(mrKey(inst, project, iid))
	cat, fetchWarnings := d.projectCatalog(r.Context(), detail)

	content := mrDetailContent{
		Nav:           newMRNav(inst, project, iid),
		Detail:        detail,
		Commits:       commits,
		Pending:       pending,
		Approvals:     approvals,
		AgentOptions:  agentOptions(d, cat, detail.ProjectPath),
		AgentWarnings: append(cat.Warnings(), fetchWarnings...),
	}
	for _, f := range pending {
		if f.State == review.StateAccepted {
			content.HasAccepted = true
		}
	}
	if detail.NeedsRebase() {
		content.RebaseWarning = runner.RebaseWarning(*detail)
	}
	// Severity gate status for the approve action; best-effort, an
	// unreadable store must not break the page.
	if gate := d.cfgFor(detail.ProjectPath).Gate; gate.Enabled() && gate.Approvals != "off" {
		if n, err := d.Results.LatestBlocking(detail.Ref(), review.Severity(gate.MinSeverity)); err == nil && n > 0 {
			content.GateBlocking = n
			content.GateSeverity = gate.MinSeverity
			content.GateBlocked = gate.Approvals == "block"
		}
	}
	if prev, err := d.Results.Latest(detail.Ref()); err == nil && prev != nil && prev.HeadSHA != "" {
		content.PrevReviewHead = prev.HeadSHA
	}
	s.render(w, http.StatusOK, "mrdetail", pageData{Title: detail.Ref(), Instance: inst, Content: content})
}

type diffContent struct {
	Nav         mrNav
	Detail      *gitlabx.MRDetail
	Files       []diffFile
	Explorer    []*explorerNode  // Files grouped into a collapsible tree
	General     []review.Finding // pending MR-level comments
	HasAccepted bool
	BackURL     string // this page, for comment form redirects
	Split       bool   // side-by-side layout
	UnifiedURL  string // layout toggle targets
	SplitURL    string
	// Review-form state, same as the MR detail page.
	AgentOptions   []agentOption
	AgentWarnings  []string
	PrevReviewHead string // baseline for the full-re-review override
	// A stored review shown inline: its findings anchor on their diff
	// lines and can be triaged in place.
	RecordName      string
	RecordTime      time.Time
	StateURL        string           // POST target for accept/reject
	FindingsURL     string           // the findings page for the same record
	GeneralFindings []review.Finding // the record's MR-level findings
}

// handleDiff shows the full MR diff with the file explorer sidebar, inline
// discussions, and manual comment forms. ?view=unified|split selects the
// layout; the configured ui.diff_view is the default, as in the TUI.
func (s *Server) handleDiff(w http.ResponseWriter, r *http.Request, d *Deps) {
	inst := r.PathValue("inst")
	project, iid, err := mrQuery(r)
	if err != nil {
		s.renderError(w, http.StatusBadRequest, err)
		return
	}
	view := r.URL.Query().Get("view")
	if view == "" {
		view = d.Cfg.UI.DiffView
	}
	split := view == "split"
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
	discussions, _ := d.Svc.ListDiscussions(r.Context(), detail.Project(), iid) // decorative
	pending := s.comments.list(mrKey(inst, project, iid))
	cat, fetchWarnings := d.projectCatalog(r.Context(), detail)

	// The latest stored review rides along so its findings can be triaged
	// in diff context; ?record= pins a specific one (the findings page
	// links back here with it).
	recName := r.URL.Query().Get("record")
	if recName == "" {
		if entries, err := d.Results.List(detail.Ref()); err == nil && len(entries) > 0 {
			recName = filepath.Base(entries[0].Path) // newest first
		}
	}
	var recFindings []review.Finding
	var recTime time.Time
	if recName != "" {
		if path, err := s.safeStoreFile(recName, ".json"); err == nil {
			if rec, err := d.Results.Load(path); err == nil {
				recFindings = rec.Findings
				recTime = rec.Started
			} else {
				recName = ""
			}
		} else {
			recName = ""
		}
	}

	viewQuery := url.Values{"view": {view}}
	content := diffContent{
		Nav:           newMRNav(inst, project, iid),
		Detail:        detail,
		Files:         buildDiffFiles(diffs, discussions, pending, recFindings, split),
		BackURL:       mrURL(inst, "/mr/diff", project, iid, viewQuery),
		Split:         split,
		UnifiedURL:    mrURL(inst, "/mr/diff", project, iid, url.Values{"view": {"unified"}}),
		SplitURL:      mrURL(inst, "/mr/diff", project, iid, url.Values{"view": {"split"}}),
		AgentOptions:  agentOptions(d, cat, detail.ProjectPath),
		AgentWarnings: append(cat.Warnings(), fetchWarnings...),
		RecordName:    recName,
		RecordTime:    recTime,
		StateURL:      instPath(inst, "/mr/findings/state"),
	}
	if recName != "" {
		content.FindingsURL = mrURL(inst, "/mr/findings", project, iid, url.Values{"record": {recName}})
	}
	if prev, err := d.Results.Latest(detail.Ref()); err == nil && prev != nil && prev.HeadSHA != "" {
		content.PrevReviewHead = prev.HeadSHA
	}
	for _, f := range recFindings {
		if f.File == "" {
			content.GeneralFindings = append(content.GeneralFindings, f)
		}
	}
	content.Explorer = buildExplorer(content.Files)
	for _, f := range pending {
		if f.File == "" {
			content.General = append(content.General, f)
		}
		if f.State == review.StateAccepted {
			content.HasAccepted = true
		}
	}
	s.render(w, http.StatusOK, "diff", pageData{Title: "diff · " + detail.Ref(), Instance: inst, Content: content})
}

// handleCommentAdd stores a manual comment (line-anchored or MR-level)
// pending publication.
func (s *Server) handleCommentAdd(w http.ResponseWriter, r *http.Request, _ *Deps) {
	inst := r.PathValue("inst")
	project, iid, err := mrQuery(r)
	if err != nil {
		s.renderError(w, http.StatusBadRequest, err)
		return
	}
	body := strings.TrimSpace(r.FormValue("body"))
	fallback := mrURL(inst, "/mr", project, iid, nil)
	if body == "" {
		localRedirect(w, r, r.FormValue("back"), fallback)
		return
	}
	f := review.Finding{Body: body, File: r.FormValue("file")}
	if f.File != "" {
		if n, err := strconv.Atoi(r.FormValue("new")); err == nil && n > 0 {
			f.Line.NewLine = &n
		}
		if o, err := strconv.Atoi(r.FormValue("old")); err == nil && o > 0 {
			f.Line.OldLine = &o
		}
	}
	s.comments.add(mrKey(inst, project, iid), f)
	localRedirect(w, r, r.FormValue("back"), fallback)
}

// handleCommentDelete removes one pending manual comment.
func (s *Server) handleCommentDelete(w http.ResponseWriter, r *http.Request, _ *Deps) {
	inst := r.PathValue("inst")
	project, iid, err := mrQuery(r)
	if err != nil {
		s.renderError(w, http.StatusBadRequest, err)
		return
	}
	s.comments.remove(mrKey(inst, project, iid), r.FormValue("id"))
	localRedirect(w, r, r.FormValue("back"), mrURL(inst, "/mr", project, iid, nil))
}

// handleApprove approves the MR, or removes the user's approval when the
// form says so. The head SHA posted from the detail page rides along on
// approval, so an MR that gained commits since the page was rendered is
// rejected by GitLab rather than silently approved. A block-mode severity
// gate is enforced here too, so it cannot be sidestepped with a direct POST.
func (s *Server) handleApprove(w http.ResponseWriter, r *http.Request, d *Deps) {
	inst := r.PathValue("inst")
	project, iid, err := mrQuery(r)
	if err != nil {
		s.renderError(w, http.StatusBadRequest, err)
		return
	}
	if r.FormValue("action") == "unapprove" {
		err = d.Svc.Unapprove(r.Context(), parseProject(project), iid)
	} else {
		if gate := d.cfgFor(project).Gate; gate.Enabled() && gate.Approvals == "block" {
			ref := fmt.Sprintf("%s!%d", project, iid)
			if n, gerr := d.Results.LatestBlocking(ref, review.Severity(gate.MinSeverity)); gerr == nil && n > 0 {
				s.renderError(w, http.StatusConflict, fmt.Errorf(
					"approval blocked: %d finding(s) at or above %s in the last review (gate.approvals: block)", n, gate.MinSeverity))
				return
			}
		}
		err = d.Svc.Approve(r.Context(), parseProject(project), iid, r.FormValue("sha"))
	}
	if err != nil {
		s.renderError(w, http.StatusBadGateway, err)
		return
	}
	localRedirect(w, r, r.FormValue("back"), mrURL(inst, "/mr", project, iid, nil))
}
