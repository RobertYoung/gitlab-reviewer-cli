package tui

import (
	"context"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx"
)

type selectorMode int

const (
	selGroups   selectorMode = iota // pick a group (or "your projects")
	selProjects                     // pick a project within the chosen group
)

type selRow struct {
	label string
	desc  string
	// group is the full group path this row browses ("" for project rows
	// and the member-projects pseudo-row).
	group string
	// project is the project path this row browses.
	project string
	// memberProjects marks the "your projects" pseudo-row.
	memberProjects bool
}

type (
	selGroupsLoadedMsg struct {
		reqID   int
		page    int
		groups  []gitlabx.GroupInfo
		hasMore bool
	}
	selProjectsLoadedMsg struct {
		reqID    int
		page     int
		projects []gitlabx.ProjectInfo
		hasMore  bool
	}
	selErrMsg struct {
		reqID int
		err   error
	}
)

// selector lets the user pick what to browse when no projects or groups
// are configured: their accessible groups, a group's projects, or projects
// they are a member of.
type selector struct {
	deps Deps

	mode  selectorMode
	group string // current group in selProjects mode; "" = member projects

	rows    []selRow
	cursor  int
	input   textinput.Model
	typing  bool
	search  string
	spin    spinner.Model
	loading bool
	page    int
	hasMore bool
	reqID   int
	err     error
	width   int
	height  int
}

func newSelector(deps Deps) *selector {
	in := textinput.New()
	in.Prompt = "search: "
	in.CharLimit = 100
	return &selector{
		deps:  deps,
		input: in,
		spin:  spinner.New(spinner.WithSpinner(spinner.MiniDot)),
	}
}

func (s *selector) Title() string {
	switch {
	case s.mode == selProjects && s.group != "":
		return "select a project · " + s.group
	case s.mode == selProjects:
		return "select a project · your projects"
	default:
		return "select a group or project"
	}
}

func (s *selector) Typing() bool { return s.typing }

func (s *selector) Hints() string {
	if s.typing {
		return "enter search · esc cancel"
	}
	if s.mode == selGroups {
		return "↑/↓ move · enter open · b browse whole group · / search · q quit"
	}
	return "↑/↓ move · enter browse project · b browse whole group · / search · esc back · q quit"
}

func (s *selector) Init() tea.Cmd {
	return tea.Batch(s.spin.Tick, s.reload())
}

func (s *selector) reload() tea.Cmd {
	s.rows = nil
	s.cursor = 0
	s.page = 0
	s.hasMore = false
	return s.loadPage(1)
}

func (s *selector) loadPage(page int) tea.Cmd {
	s.loading = true
	s.err = nil
	s.reqID++
	reqID := s.reqID
	mode, group, search := s.mode, s.group, s.search
	svc := s.deps.Svc
	perPage := s.deps.Cfg.GitLab.PerPage
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), listRequestTimeout)
		defer cancel()
		p := gitlabx.Page{Number: page, PerPage: perPage}
		if mode == selGroups {
			groups, hasMore, err := svc.ListGroups(ctx, search, p)
			if err != nil {
				return selErrMsg{reqID: reqID, err: err}
			}
			return selGroupsLoadedMsg{reqID: reqID, page: page, groups: groups, hasMore: hasMore}
		}
		var (
			projects []gitlabx.ProjectInfo
			hasMore  bool
			err      error
		)
		if group == "" {
			projects, hasMore, err = svc.ListMemberProjects(ctx, search, p)
		} else {
			projects, hasMore, err = svc.ListGroupProjects(ctx, group, search, p)
		}
		if err != nil {
			return selErrMsg{reqID: reqID, err: err}
		}
		return selProjectsLoadedMsg{reqID: reqID, page: page, projects: projects, hasMore: hasMore}
	}
}

func (s *selector) Update(msg tea.Msg) (Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		s.width, s.height = msg.Width, msg.Height
		s.input.SetWidth(max(s.width-12, 10))
		return s, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		s.spin, cmd = s.spin.Update(msg)
		return s, cmd

	case selGroupsLoadedMsg:
		if msg.reqID != s.reqID || s.mode != selGroups {
			return s, nil
		}
		s.loading = false
		s.page = msg.page
		s.hasMore = msg.hasMore
		if msg.page == 1 {
			s.rows = []selRow{{label: "your projects", desc: "projects you are a member of", memberProjects: true}}
		}
		for _, g := range msg.groups {
			s.rows = append(s.rows, selRow{label: g.FullPath, desc: firstLineOf(g.Description), group: g.FullPath})
		}
		return s, nil

	case selProjectsLoadedMsg:
		if msg.reqID != s.reqID || s.mode != selProjects {
			return s, nil
		}
		s.loading = false
		s.page = msg.page
		s.hasMore = msg.hasMore
		if msg.page == 1 {
			s.rows = nil
		}
		for _, p := range msg.projects {
			desc := firstLineOf(p.Description)
			if !p.LastActivity.IsZero() {
				if desc != "" {
					desc += " · "
				}
				desc += "active " + relTime(p.LastActivity)
			}
			s.rows = append(s.rows, selRow{label: p.PathWithNamespace, desc: desc, project: p.PathWithNamespace})
		}
		return s, nil

	case selErrMsg:
		if msg.reqID != s.reqID {
			return s, nil
		}
		s.loading = false
		s.err = msg.err
		return s, nil

	case tea.KeyPressMsg:
		if s.typing {
			return s.updateInput(msg)
		}
		return s.updateList(msg)
	}
	return s, nil
}

func (s *selector) updateInput(msg tea.KeyPressMsg) (Screen, tea.Cmd) {
	switch msg.String() {
	case "enter":
		s.search = s.input.Value()
		s.typing = false
		s.input.Blur()
		return s, s.reload()
	case "esc":
		s.typing = false
		s.input.Blur()
		return s, nil
	}
	var cmd tea.Cmd
	s.input, cmd = s.input.Update(msg)
	return s, cmd
}

func (s *selector) updateList(msg tea.KeyPressMsg) (Screen, tea.Cmd) {
	switch msg.String() {
	case "q":
		return s, tea.Quit
	case "up", "k":
		if s.cursor > 0 {
			s.cursor--
		}
	case "down", "j":
		if s.cursor < len(s.rows)-1 {
			s.cursor++
		}
		if s.hasMore && !s.loading && s.cursor >= len(s.rows)-5 {
			return s, s.loadPage(s.page + 1)
		}
	case "/":
		s.typing = true
		s.input.SetValue(s.search)
		return s, s.input.Focus()
	case "esc":
		switch {
		case s.search != "":
			s.search = ""
			return s, s.reload()
		case s.mode == selProjects:
			s.mode = selGroups
			s.group = ""
			return s, s.reload()
		}
	case "enter":
		row, ok := s.selected()
		if !ok {
			return s, nil
		}
		switch {
		case row.memberProjects:
			s.mode = selProjects
			s.group = ""
			s.search = ""
			return s, s.reload()
		case row.group != "":
			s.mode = selProjects
			s.group = row.group
			s.search = ""
			return s, s.reload()
		case row.project != "":
			return s, pushScreen(newMRListScoped(s.deps, []string{row.project}, nil))
		}
	case "b":
		// Browse the whole group: the selected group row, or the group
		// currently drilled into.
		if row, ok := s.selected(); ok && row.group != "" {
			return s, pushScreen(newMRListScoped(s.deps, nil, []string{row.group}))
		}
		if s.mode == selProjects && s.group != "" {
			return s, pushScreen(newMRListScoped(s.deps, nil, []string{s.group}))
		}
	}
	return s, nil
}

func (s *selector) selected() (selRow, bool) {
	if s.cursor < 0 || s.cursor >= len(s.rows) {
		return selRow{}, false
	}
	return s.rows[s.cursor], true
}

func (s *selector) View() string {
	var b strings.Builder

	if s.mode == selGroups {
		b.WriteString(subtleStyle.Render("No projects or groups are configured — pick what to browse.") + "\n\n")
	}

	visible := max(s.height-5, 3)
	start := 0
	if s.cursor >= visible {
		start = s.cursor - visible + 1
	}
	for i := start; i < min(start+visible, len(s.rows)); i++ {
		row := s.rows[i]
		prefix := "  "
		if i == s.cursor {
			prefix = "> "
		}
		label := row.label
		if row.memberProjects {
			label = headerStyle.Render(label)
		}
		line := prefix + label
		if row.desc != "" {
			line += "  " + subtleStyle.Render(truncate(row.desc, max(s.width-len(row.label)-6, 10)))
		}
		b.WriteString(truncate(line, s.width) + "\n")
	}

	b.WriteByte('\n')
	switch {
	case s.err != nil:
		b.WriteString(errorStyle.Render(truncate("error: "+s.err.Error(), max(s.width-2, 10))))
	case s.typing:
		b.WriteString(s.input.View())
	case s.loading:
		b.WriteString(s.spin.View() + " loading…")
	case len(s.rows) == 0:
		b.WriteString(subtleStyle.Render("nothing found"))
	default:
		count := fmt.Sprintf("%d entries", len(s.rows))
		if s.search != "" {
			count += " · search: “" + s.search + "”"
		}
		b.WriteString(subtleStyle.Render(count))
	}
	return b.String()
}

func firstLineOf(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}
