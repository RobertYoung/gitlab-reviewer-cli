package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// typer is implemented by screens that capture raw text input; the root
// model will not open help over an active input.
type typer interface{ Typing() bool }

var helpKeyStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12")).Width(14)

// helpScreen is a static keybinding reference, opened with ? anywhere.
type helpScreen struct {
	width  int
	height int
}

func (s *helpScreen) Title() string { return "help" }
func (s *helpScreen) Hints() string { return "esc/q back" }
func (s *helpScreen) Init() tea.Cmd { return nil }

func (s *helpScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		s.width, s.height = msg.Width, msg.Height
	case tea.KeyPressMsg:
		switch msg.String() {
		case "esc", "q", "?":
			return s, popScreen
		}
	}
	return s, nil
}

func (s *helpScreen) View() string {
	sections := []struct {
		title string
		keys  [][2]string
	}{
		{"everywhere", [][2]string{
			{"?", "this help"},
			{"esc", "go back / cancel"},
			{"ctrl+c", "quit"},
		}},
		{"group/project selector (no scope configured)", [][2]string{
			{"↑/↓ j/k", "move"},
			{"enter", "open group / browse project"},
			{"b", "browse all MRs in the group"},
			{"/", "search (server-side)"},
			{"esc", "back to groups / clear search"},
		}},
		{"merge request list", [][2]string{
			{"↑/↓ j/k", "move"},
			{"enter", "open merge request"},
			{"/", "search text"},
			{"a", "filter by author"},
			{"t", "filter by target branch"},
			{"s", "cycle state (opened/merged/closed/all)"},
			{"r", "reload"},
			{"q", "quit"},
		}},
		{"merge request detail", [][2]string{
			{"↑/↓", "scroll diff"},
			{"n/p", "next/previous file"},
			{"]/[", "next/previous hunk"},
			{"g/G", "top/bottom"},
			{"v", "toggle unified / side-by-side diff"},
			{"r", "review with Claude"},
			{"L", "browse stored review logs"},
		}},
		{"review", [][2]string{
			{"esc", "cancel the running review"},
			{"l", "view the run log after a failure"},
		}},
		{"findings", [][2]string{
			{"↑/↓ j/k", "move"},
			{"a / x", "accept / reject"},
			{"A", "accept all pending"},
			{"e", "edit comment body (ctrl+s saves)"},
			{"p", "publish accepted findings"},
			{"l", "view this review's run log"},
		}},
		{"publish", [][2]string{
			{"m", "toggle immediate/draft mode"},
			{"enter", "start publishing"},
			{"P", "publish draft review"},
			{"esc", "keep as pending drafts"},
		}},
	}

	var b strings.Builder
	for _, sec := range sections {
		b.WriteString(headerStyle.Render(sec.title) + "\n")
		for _, k := range sec.keys {
			b.WriteString("  " + helpKeyStyle.Render(k[0]) + k[1] + "\n")
		}
		b.WriteByte('\n')
	}
	return strings.TrimSuffix(b.String(), "\n")
}
