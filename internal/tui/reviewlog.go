package tui

import (
	"fmt"
	"os"
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review/runlog"
)

type (
	logEntriesMsg struct {
		ref     string
		entries []runlog.Entry
		err     error
	}
	logContentMsg struct {
		path    string
		content string
		err     error
	}
)

// logList browses the stored review run logs for one MR, newest first.
type logList struct {
	deps    Deps
	ref     string
	entries []runlog.Entry
	cursor  int
	loaded  bool
	err     error
	width   int
	height  int
}

func newLogList(deps Deps, ref string) *logList {
	return &logList{deps: deps, ref: ref}
}

func (s *logList) Title() string { return "review logs · " + s.ref }
func (s *logList) Hints() string { return "↑/↓ move · enter view · esc back" }

func (s *logList) Init() tea.Cmd {
	ref, logs := s.ref, s.deps.Logs
	return func() tea.Msg {
		entries, err := logs.List(ref)
		return logEntriesMsg{ref: ref, entries: entries, err: err}
	}
}

func (s *logList) Update(msg tea.Msg) (Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		s.width, s.height = msg.Width, msg.Height

	case logEntriesMsg:
		if msg.ref != s.ref {
			return s, nil
		}
		s.loaded = true
		s.entries, s.err = msg.entries, msg.err

	case tea.KeyPressMsg:
		switch msg.String() {
		case "esc":
			return s, popScreen
		case "q":
			return s, tea.Quit
		case "up", "k":
			if s.cursor > 0 {
				s.cursor--
			}
		case "down", "j":
			if s.cursor < len(s.entries)-1 {
				s.cursor++
			}
		case "enter":
			if s.cursor < len(s.entries) {
				e := s.entries[s.cursor]
				return s, pushScreen(newLogView(e.Ref, e.Path))
			}
		}
	}
	return s, nil
}

func (s *logList) View() string {
	switch {
	case s.err != nil:
		return errorStyle.Render(truncate("error: "+s.err.Error(), max(s.width*2, 20)))
	case !s.loaded:
		return subtleStyle.Render("loading…")
	case len(s.entries) == 0:
		return subtleStyle.Render("no stored review logs for this MR") + "\n\n" +
			subtleStyle.Render("run a review with r; its log is kept here for reference")
	}

	var b strings.Builder
	visible := max(s.height, 3)
	start := 0
	if s.cursor >= visible {
		start = s.cursor - visible + 1
	}
	for i := start; i < min(start+visible, len(s.entries)); i++ {
		e := s.entries[i]
		prefix := "  "
		if i == s.cursor {
			prefix = "> "
		}
		line := fmt.Sprintf("%s%s  %s", prefix,
			e.Started.Format("2006-01-02 15:04:05"),
			subtleStyle.Render(truncate(e.Title, max(s.width-24, 10))))
		b.WriteString(line + "\n")
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// logView displays one stored review run log in a scrollable viewport.
type logView struct {
	ref    string
	path   string
	vp     viewport.Model
	loaded bool
	err    error
	width  int
	height int
}

func newLogView(ref, path string) *logView {
	return &logView{ref: ref, path: path, vp: viewport.New()}
}

func (s *logView) Title() string { return "review log · " + s.ref }
func (s *logView) Hints() string { return "↑/↓ scroll · g/G top/bottom · esc back" }

func (s *logView) Init() tea.Cmd {
	path := s.path
	return func() tea.Msg {
		data, err := os.ReadFile(path) //nolint:gosec // paths come from the run-log store
		return logContentMsg{path: path, content: string(data), err: err}
	}
}

func (s *logView) Update(msg tea.Msg) (Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		s.width, s.height = msg.Width, msg.Height
		s.vp.SetWidth(s.width)
		s.vp.SetHeight(max(s.height, 1))
		return s, nil

	case logContentMsg:
		if msg.path != s.path {
			return s, nil
		}
		s.loaded = true
		s.err = msg.err
		s.vp.SetContent(msg.content)
		return s, nil

	case tea.KeyPressMsg:
		switch msg.String() {
		case "esc":
			return s, popScreen
		case "q":
			return s, tea.Quit
		case "g":
			s.vp.GotoTop()
			return s, nil
		case "G":
			s.vp.GotoBottom()
			return s, nil
		}
	}
	var cmd tea.Cmd
	s.vp, cmd = s.vp.Update(msg)
	return s, cmd
}

func (s *logView) View() string {
	switch {
	case s.err != nil:
		return errorStyle.Render(truncate("error: "+s.err.Error(), max(s.width*2, 20)))
	case !s.loaded:
		return subtleStyle.Render("loading…")
	}
	return s.vp.View()
}
