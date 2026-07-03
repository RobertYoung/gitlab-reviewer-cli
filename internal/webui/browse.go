package webui

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx"
)

// browseRow is one selectable entry on the scope picker.
type browseRow struct {
	Label string
	Desc  string
	// OpenURL drills in: a group's project list, or the member-projects
	// list. Empty for project rows.
	OpenURL string
	// MRsURL lists merge requests scoped to this row.
	MRsURL string
}

type browseContent struct {
	Instance string
	Group    string // group drilled into ("" at the top level)
	Mine     bool   // member-projects mode
	Search   string
	Rows     []browseRow
	// GroupMRsURL browses the whole drilled-into group's MRs.
	GroupMRsURL string
	BackURL     string
	Err         string
	PrevURL     string
	NextURL     string
}

// handleBrowse is the scope picker shown when no projects or groups are
// configured: the web equivalent of the TUI selector. The top level lists
// the user's groups plus a "your projects" entry; drilling in lists a
// group's (or the member) projects, each linking to the MR list scoped via
// the ?projects=/?groups= override.
func (s *Server) handleBrowse(w http.ResponseWriter, r *http.Request, d *Deps) {
	inst := r.PathValue("inst")
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	page = max(page, 1)
	group := q.Get("group")
	mine := q.Get("mine") != ""
	search := q.Get("search")

	content := browseContent{
		Instance: inst,
		Group:    group,
		Mine:     mine,
		Search:   search,
	}
	title := "select a group or project"
	switch {
	case group != "":
		title = "select a project · " + group
		content.BackURL = instPath(inst, "/browse")
		content.GroupMRsURL = scopedMRsURL(inst, "groups", group)
	case mine:
		title = "select a project · your projects"
		content.BackURL = instPath(inst, "/browse")
	}

	p := gitlabx.Page{Number: page, PerPage: d.Cfg.GitLab.PerPage}
	var hasMore bool
	switch {
	case group != "":
		projects, more, err := d.Svc.ListGroupProjects(r.Context(), group, search, p)
		content.Rows, hasMore = projectRows(inst, projects), more
		content.Err = errText(err)
	case mine:
		projects, more, err := d.Svc.ListMemberProjects(r.Context(), search, p)
		content.Rows, hasMore = projectRows(inst, projects), more
		content.Err = errText(err)
	default:
		if page == 1 && search == "" {
			content.Rows = append(content.Rows, browseRow{
				Label:   "your projects",
				Desc:    "projects you are a member of",
				OpenURL: instPath(inst, "/browse") + "?mine=1",
			})
		}
		groups, more, err := d.Svc.ListGroups(r.Context(), search, p)
		hasMore = more
		content.Err = errText(err)
		for _, g := range groups {
			content.Rows = append(content.Rows, browseRow{
				Label:   g.FullPath,
				Desc:    firstLine(g.Description),
				OpenURL: instPath(inst, "/browse") + "?" + url.Values{"group": {g.FullPath}}.Encode(),
				MRsURL:  scopedMRsURL(inst, "groups", g.FullPath),
			})
		}
	}
	if page > 1 {
		content.PrevURL = browsePageURL(inst, q, page-1)
	}
	if hasMore {
		content.NextURL = browsePageURL(inst, q, page+1)
	}
	s.render(w, http.StatusOK, "browse", pageData{Title: title, Instance: inst, Content: content})
}

// scopedMRsURL links to the MR list with an ad-hoc scope override; param is
// "projects" or "groups".
func scopedMRsURL(inst, param, path string) string {
	return instPath(inst, "/") + "?" + url.Values{param: {path}}.Encode()
}

func browsePageURL(inst string, q url.Values, page int) string {
	nq := url.Values{}
	for k, vs := range q {
		nq[k] = vs
	}
	nq.Set("page", fmt.Sprint(page))
	return instPath(inst, "/browse") + "?" + nq.Encode()
}

func projectRows(inst string, projects []gitlabx.ProjectInfo) []browseRow {
	rows := make([]browseRow, 0, len(projects))
	for _, p := range projects {
		desc := firstLine(p.Description)
		if !p.LastActivity.IsZero() {
			if desc != "" {
				desc += " · "
			}
			desc += "active " + relTime(p.LastActivity)
		}
		rows = append(rows, browseRow{
			Label:  p.PathWithNamespace,
			Desc:   desc,
			MRsURL: scopedMRsURL(inst, "projects", p.PathWithNamespace),
		})
	}
	return rows
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}
