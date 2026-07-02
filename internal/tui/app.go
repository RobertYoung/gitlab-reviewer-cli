// Package tui implements the terminal UI: a stack of screens over the
// gitlabx service, built on Bubble Tea.
package tui

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/config"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx"
)

// Screen is one screen on the navigation stack. Screens receive an adjusted
// WindowSizeMsg (content area only) and may emit pushScreenMsg/popScreenMsg
// commands to navigate.
type Screen interface {
	Init() tea.Cmd
	Update(msg tea.Msg) (Screen, tea.Cmd)
	View() string
	Title() string
	// Hints is the contextual key help shown in the status bar.
	Hints() string
}

// Navigation messages emitted by screens.
type (
	pushScreenMsg struct{ screen Screen }
	popScreenMsg  struct{}
)

func pushScreen(s Screen) tea.Cmd {
	return func() tea.Msg { return pushScreenMsg{screen: s} }
}

func popScreen() tea.Msg { return popScreenMsg{} }

// chrome is the number of lines the app reserves around screen content
// (title bar + status bar).
const chrome = 2

// App is the root Bubble Tea model: it owns the screen stack and window
// chrome and routes every other message to the top screen.
type App struct {
	cfg    config.Config
	stack  []Screen
	width  int
	height int
}

// NewApp builds the root model with the MR list as the bottom screen.
func NewApp(cfg config.Config, svc gitlabx.Service) *App {
	return &App{
		cfg:   cfg,
		stack: []Screen{newMRList(svc, cfg.GitLab.PerPage)},
	}
}

func (a *App) Init() tea.Cmd {
	return a.top().Init()
}

func (a *App) top() Screen { return a.stack[len(a.stack)-1] }

func (a *App) contentSize() tea.WindowSizeMsg {
	return tea.WindowSizeMsg{Width: a.width, Height: max(a.height-chrome, 1)}
}

func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
		// Every screen tracks its own size so pops need no re-measure.
		var cmds []tea.Cmd
		for i, s := range a.stack {
			next, cmd := s.Update(a.contentSize())
			a.stack[i] = next
			cmds = append(cmds, cmd)
		}
		return a, tea.Batch(cmds...)

	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			return a, tea.Quit
		}

	case pushScreenMsg:
		a.stack = append(a.stack, msg.screen)
		next, cmd := msg.screen.Update(a.contentSize())
		a.stack[len(a.stack)-1] = next
		return a, tea.Batch(msg.screen.Init(), cmd)

	case popScreenMsg:
		if len(a.stack) > 1 {
			a.stack = a.stack[:len(a.stack)-1]
		}
		return a, nil
	}

	next, cmd := a.top().Update(msg)
	a.stack[len(a.stack)-1] = next
	return a, cmd
}

func (a *App) View() tea.View {
	title := titleStyle.Render("gitlab-reviewer") + " " + headerStyle.Render(a.top().Title())
	status := statusStyle.Render(truncate(a.top().Hints(), max(a.width-1, 0)))

	content := a.top().View()
	body := lipgloss.NewStyle().
		Width(a.width).
		Height(max(a.height-chrome, 1)).
		MaxHeight(max(a.height-chrome, 1)).
		Render(content)

	v := tea.NewView(title + "\n" + body + "\n" + status)
	v.AltScreen = true
	return v
}
