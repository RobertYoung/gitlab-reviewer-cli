package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/table"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx"
)

const listRequestTimeout = 30 * time.Second

type inputMode int

const (
	inputNone inputMode = iota
	inputSearch
	inputAuthor
	inputTarget
)

var stateCycle = []string{"opened", "merged", "closed", "all"}

type (
	mrPageLoadedMsg struct {
		reqID   int
		page    int
		mrs     []gitlabx.MRSummary
		hasMore bool
	}
	mrListErrMsg struct {
		reqID int
		err   error
	}
)

// mrList is the MR browser screen: a table of open MRs across all
// configured projects/groups with filtering.
type mrList struct {
	deps    Deps
	svc     gitlabx.Service
	perPage int

	table  table.Model
	input  textinput.Model
	mode   inputMode
	spin   spinner.Model
	filter gitlabx.MRFilter
	// scoped means the projects/groups came from the in-TUI selector, so
	// esc navigates back to it.
	scoped  bool
	mrs     []gitlabx.MRSummary
	page    int
	hasMore bool
	loading bool
	reqID   int
	err     error
	width   int
	height  int
}

func newMRList(deps Deps) *mrList {
	return newMRListScoped(deps, nil, nil)
}

// newMRListScoped browses a scope chosen in the TUI instead of the
// configured projects/groups; esc pops back to the selector.
func newMRListScoped(deps Deps, projects, groups []string) *mrList {
	in := textinput.New()
	in.Prompt = "/"
	in.CharLimit = 100

	scoped := len(projects) > 0 || len(groups) > 0
	return &mrList{
		deps:    deps,
		svc:     deps.Svc,
		perPage: deps.Cfg.GitLab.PerPage,
		scoped:  scoped,
		filter:  gitlabx.MRFilter{State: "opened", Projects: projects, Groups: groups},
		table: table.New(
			table.WithFocused(true),
			table.WithColumns([]table.Column{{Title: "!IID", Width: 6}}),
		),
		input: in,
		spin:  spinner.New(spinner.WithSpinner(spinner.MiniDot)),
	}
}

func (s *mrList) Title() string {
	t := "merge requests"
	if len(s.filter.Projects) > 0 {
		t += " · " + strings.Join(s.filter.Projects, ",")
	}
	if len(s.filter.Groups) > 0 {
		t += " · " + strings.Join(s.filter.Groups, ",")
	}
	t += " · " + s.filter.State
	if s.filter.AuthorUsername != "" {
		t += " · author:" + s.filter.AuthorUsername
	}
	if s.filter.TargetBranch != "" {
		t += " · target:" + s.filter.TargetBranch
	}
	if s.filter.Search != "" {
		t += " · “" + s.filter.Search + "”"
	}
	return t
}

// Typing reports whether a filter input currently captures keystrokes.
func (s *mrList) Typing() bool { return s.mode != inputNone }

func (s *mrList) Hints() string {
	if s.mode != inputNone {
		return "enter apply · esc cancel"
	}
	return "↑/↓ move · enter open · / search · a author · t target · s state · r reload · q quit"
}

func (s *mrList) Init() tea.Cmd {
	return tea.Batch(s.spin.Tick, s.reload())
}

// reload restarts from page 1 with the current filter.
func (s *mrList) reload() tea.Cmd {
	s.mrs = nil
	s.page = 0
	s.hasMore = false
	return s.loadPage(1)
}

func (s *mrList) loadPage(page int) tea.Cmd {
	s.loading = true
	s.err = nil
	s.reqID++
	reqID := s.reqID
	filter := s.filter
	svc, perPage := s.svc, s.perPage
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), listRequestTimeout)
		defer cancel()
		mrs, hasMore, err := svc.ListOpenMergeRequests(ctx, filter, gitlabx.Page{Number: page, PerPage: perPage})
		if err != nil {
			return mrListErrMsg{reqID: reqID, err: err}
		}
		return mrPageLoadedMsg{reqID: reqID, page: page, mrs: mrs, hasMore: hasMore}
	}
}

func (s *mrList) Update(msg tea.Msg) (Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		s.width, s.height = msg.Width, msg.Height
		s.layout()
		return s, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		s.spin, cmd = s.spin.Update(msg)
		return s, cmd

	case mrPageLoadedMsg:
		if msg.reqID != s.reqID {
			return s, nil // stale response after filter change
		}
		s.loading = false
		s.page = msg.page
		s.hasMore = msg.hasMore
		firstPage := msg.page == 1
		if firstPage {
			s.mrs = msg.mrs
		} else {
			s.mrs = append(s.mrs, msg.mrs...)
		}
		s.refreshRows()
		if firstPage {
			s.table.GotoTop()
		}
		return s, nil

	case mrListErrMsg:
		if msg.reqID != s.reqID {
			return s, nil
		}
		s.loading = false
		s.err = msg.err
		return s, nil

	case tea.KeyPressMsg:
		if s.mode != inputNone {
			return s.updateInput(msg)
		}
		return s.updateTable(msg)
	}

	var cmd tea.Cmd
	s.table, cmd = s.table.Update(msg)
	return s, cmd
}

func (s *mrList) updateInput(msg tea.KeyPressMsg) (Screen, tea.Cmd) {
	switch msg.String() {
	case "enter":
		val := s.input.Value()
		switch s.mode {
		case inputSearch:
			s.filter.Search = val
		case inputAuthor:
			s.filter.AuthorUsername = val
		case inputTarget:
			s.filter.TargetBranch = val
		}
		s.mode = inputNone
		s.input.Blur()
		return s, s.reload()
	case "esc":
		s.mode = inputNone
		s.input.Blur()
		return s, nil
	}
	var cmd tea.Cmd
	s.input, cmd = s.input.Update(msg)
	return s, cmd
}

func (s *mrList) updateTable(msg tea.KeyPressMsg) (Screen, tea.Cmd) {
	switch msg.String() {
	case "q":
		return s, tea.Quit
	case "enter":
		if mr, ok := s.selected(); ok {
			return s, pushScreen(newMRDetail(s.deps, mr))
		}
		return s, nil
	case "/", "a", "t":
		s.mode = map[string]inputMode{"/": inputSearch, "a": inputAuthor, "t": inputTarget}[msg.String()]
		s.input.SetValue(s.currentFilterValue())
		s.input.Prompt = map[inputMode]string{inputSearch: "search: ", inputAuthor: "author: ", inputTarget: "target: "}[s.mode]
		return s, s.input.Focus()
	case "s":
		for i, st := range stateCycle {
			if st == s.filter.State {
				s.filter.State = stateCycle[(i+1)%len(stateCycle)]
				break
			}
		}
		return s, s.reload()
	case "r":
		return s, s.reload()
	case "esc":
		if s.filter.State != "opened" || s.filter.AuthorUsername != "" || s.filter.TargetBranch != "" || s.filter.Search != "" {
			s.filter.State = "opened"
			s.filter.AuthorUsername, s.filter.TargetBranch, s.filter.Search = "", "", ""
			return s, s.reload()
		}
		if s.scoped {
			return s, popScreen // back to the selector
		}
		return s, nil
	}

	var cmd tea.Cmd
	s.table, cmd = s.table.Update(msg)

	// Infinite scroll: fetch the next page when the cursor nears the end.
	if s.hasMore && !s.loading && s.table.Cursor() >= len(s.mrs)-5 {
		return s, tea.Batch(cmd, s.loadPage(s.page+1))
	}
	return s, cmd
}

func (s *mrList) currentFilterValue() string {
	switch s.mode {
	case inputSearch:
		return s.filter.Search
	case inputAuthor:
		return s.filter.AuthorUsername
	case inputTarget:
		return s.filter.TargetBranch
	}
	return ""
}

func (s *mrList) selected() (gitlabx.MRSummary, bool) {
	i := s.table.Cursor()
	if i < 0 || i >= len(s.mrs) {
		return gitlabx.MRSummary{}, false
	}
	return s.mrs[i], true
}

func (s *mrList) layout() {
	if s.width == 0 {
		return
	}
	// Fixed-width columns; title takes the remainder.
	iid, project, author, target, updated := 7, 26, 14, 16, 10
	title := max(s.width-iid-project-author-target-updated-12, 20)
	s.table.SetColumns([]table.Column{
		{Title: "!IID", Width: iid},
		{Title: "Project", Width: project},
		{Title: "Title", Width: title},
		{Title: "Author", Width: author},
		{Title: "Target", Width: target},
		{Title: "Updated", Width: updated},
	})
	s.table.SetWidth(s.width)
	s.table.SetHeight(max(s.height-2, 3)) // one line for input/status, one spare
	s.refreshRows()
	s.input.SetWidth(max(s.width-10, 10))
}

func (s *mrList) refreshRows() {
	rows := make([]table.Row, len(s.mrs))
	for i, mr := range s.mrs {
		title := mr.Title
		if mr.Draft {
			title = draftStyle.Render("[draft] ") + title
		}
		rows[i] = table.Row{
			fmt.Sprintf("!%d", mr.IID),
			truncate(mr.ProjectPath, 26),
			title,
			mr.Author,
			mr.TargetBranch,
			relTime(mr.UpdatedAt),
		}
	}
	s.table.SetRows(rows)
}

func (s *mrList) View() string {
	var status string
	switch {
	case s.err != nil:
		status = errorStyle.Render(truncate("error: "+s.err.Error(), max(s.width-2, 10)))
	case s.mode != inputNone:
		status = s.input.View()
	case s.loading:
		status = s.spin.View() + " loading merge requests…"
	case len(s.mrs) == 0:
		status = subtleStyle.Render("no merge requests match")
	default:
		status = subtleStyle.Render(fmt.Sprintf("%d merge requests", len(s.mrs)))
		if s.hasMore {
			status += subtleStyle.Render(" (more available — scroll down)")
		}
	}
	return s.table.View() + "\n" + status
}
