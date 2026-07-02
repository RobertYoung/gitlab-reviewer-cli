package tui

import (
	tea "charm.land/bubbletea/v2"
)

// Run starts the TUI and blocks until the user quits.
func Run(deps Deps) error {
	_, err := tea.NewProgram(NewApp(deps)).Run()
	return err
}
