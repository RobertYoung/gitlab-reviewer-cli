package webui

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review"
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
	if len(s.instances) == 1 {
		http.Redirect(w, r, instPath(s.instances[0], "/"), http.StatusSeeOther)
		return
	}
	type instanceItem struct {
		Name string
		URL  string
	}
	items := make([]instanceItem, 0, len(s.instances))
	for _, name := range s.instances {
		items = append(items, instanceItem{Name: name, URL: instPath(name, "/")})
	}
	s.render(w, http.StatusOK, "home", pageData{Title: "instances", Content: items})
}

type mrListContent struct {
	Instance   string
	MRs        []gitlabx.MRSummary
	State      string
	Search     string
	Author     string
	Target     string
	Projects   string // scope override, comma-separated
	Groups     string
	NeedsScope bool // no configured projects/groups: show the scope inputs
	Page       int
	PrevURL    string
	NextURL    string
	Err        string
	MRURL      func(gitlabx.MRSummary) string
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
	// show the scope form instead of an error.
	if content.NeedsScope && len(filter.Projects) == 0 && len(filter.Groups) == 0 {
		s.render(w, http.StatusOK, "mrs", pageData{Title: "merge requests", Instance: inst, Content: content})
		return
	}

	mrs, hasMore, err := d.Svc.ListOpenMergeRequests(r.Context(), filter,
		gitlabx.Page{Number: page, PerPage: d.Cfg.GitLab.PerPage})
	if err != nil {
		content.Err = err.Error()
	}
	content.MRs = mrs
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
	PublishURL  string // GET confirm page for pending comments
	FindingsURL string
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
		PublishURL: mrURL(inst, "/mr/publish", project, iid, url.Values{"source": {"comments"}}),
	}
}

type mrDetailContent struct {
	Nav           mrNav
	Detail        *gitlabx.MRDetail
	Commits       []gitlabx.Commit
	Pending       []review.Finding
	RebaseWarning string
	HasAccepted   bool
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
	commits, _ := d.Svc.ListCommits(r.Context(), detail.Project(), iid) // best-effort
	pending := s.comments.list(mrKey(inst, project, iid))

	content := mrDetailContent{
		Nav:     newMRNav(inst, project, iid),
		Detail:  detail,
		Commits: commits,
		Pending: pending,
	}
	for _, f := range pending {
		if f.State == review.StateAccepted {
			content.HasAccepted = true
		}
	}
	if detail.NeedsRebase() {
		content.RebaseWarning = runner.RebaseWarning(*detail)
	}
	s.render(w, http.StatusOK, "mrdetail", pageData{Title: detail.Ref(), Instance: inst, Content: content})
}

type diffContent struct {
	Nav         mrNav
	Detail      *gitlabx.MRDetail
	Files       []diffFile
	General     []review.Finding // pending MR-level comments
	HasAccepted bool
	BackURL     string // this page, for comment form redirects
}

// handleDiff shows the full MR diff with the file explorer sidebar, inline
// discussions, and manual comment forms.
func (s *Server) handleDiff(w http.ResponseWriter, r *http.Request, d *Deps) {
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
	discussions, _ := d.Svc.ListDiscussions(r.Context(), detail.Project(), iid) // decorative
	pending := s.comments.list(mrKey(inst, project, iid))

	content := diffContent{
		Nav:     newMRNav(inst, project, iid),
		Detail:  detail,
		Files:   buildDiffFiles(diffs, discussions, pending),
		BackURL: mrURL(inst, "/mr/diff", project, iid, nil),
	}
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
