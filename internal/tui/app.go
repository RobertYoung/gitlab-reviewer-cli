// Package tui implements the terminal UI: a stack of screens over the
// gitlabx service, built on Bubble Tea.
package tui

import (
	"context"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/config"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review/runlog"
)

// CheckoutFunc prepares a review worktree for an MR and returns its path
// plus a cleanup function. Progress lines go to the TUI's review log.
type CheckoutFunc func(ctx context.Context, mr gitlabx.MRDetail, progress func(string)) (path string, cleanup func(context.Context) error, err error)

// Deps bundles everything screens need. CfgFor resolves per-project
// overrides on top of the base configuration. OpenURL overrides the
// default browser launcher (tests inject a recorder).
type Deps struct {
	Cfg      config.Config
	Svc      gitlabx.Service
	Reviewer review.Reviewer
	Checkout CheckoutFunc
	CfgFor   func(projectPath string) config.Config
	// Logs stores each review run's progress log for later viewing; nil
	// disables both storing and the log screens' content.
	Logs    *runlog.Store
	OpenURL func(url string) error
}

func (d Deps) cfgFor(projectPath string) config.Config {
	if d.CfgFor == nil {
		return d.Cfg
	}
	return d.CfgFor(projectPath)
}

func (d Deps) openURL(url string) error {
	if d.OpenURL == nil {
		return openBrowser(url)
	}
	return d.OpenURL(url)
}

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
	// popScreenMsg pops count screens (0 means 1); replacement, when set,
	// is pushed afterwards — used to swap the review-progress screen for
	// the findings screen.
	popScreenMsg struct {
		count       int
		replacement Screen
	}
)

func pushScreen(s Screen) tea.Cmd {
	return func() tea.Msg { return pushScreenMsg{screen: s} }
}

func popScreen() tea.Msg { return popScreenMsg{} }

func popScreens(count int, replacement Screen) tea.Cmd {
	return func() tea.Msg { return popScreenMsg{count: count, replacement: replacement} }
}

// chrome is the number of lines the app reserves around screen content
// (title bar + status bar).
const chrome = 2

// App is the root Bubble Tea model: it owns the screen stack and window
// chrome and routes every other message to the top screen.
type App struct {
	deps   Deps
	stack  []Screen
	width  int
	height int
}

// NewApp builds the root model. With projects/groups configured the MR
// list is the bottom screen; otherwise a selector lists the user's
// available groups and projects to pick from.
func NewApp(deps Deps) *App {
	var root Screen = newMRList(deps)
	if len(deps.Cfg.GitLab.Projects) == 0 && len(deps.Cfg.GitLab.Groups) == 0 {
		root = newSelector(deps)
	}
	return &App{
		deps:  deps,
		stack: []Screen{root},
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
		switch msg.String() {
		case "ctrl+c":
			return a, tea.Quit
		case "?":
			if t, ok := a.top().(typer); ok && t.Typing() {
				break // "?" is a literal character while typing
			}
			if _, ok := a.top().(*helpScreen); !ok {
				return a, pushScreen(&helpScreen{})
			}
		}

	case pushScreenMsg:
		a.stack = append(a.stack, msg.screen)
		next, cmd := msg.screen.Update(a.contentSize())
		a.stack[len(a.stack)-1] = next
		return a, tea.Batch(msg.screen.Init(), cmd)

	case popScreenMsg:
		n := max(msg.count, 1)
		for range n {
			if len(a.stack) > 1 {
				a.stack = a.stack[:len(a.stack)-1]
			}
		}
		if msg.replacement != nil {
			return a, pushScreen(msg.replacement)
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
