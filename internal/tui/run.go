package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/config"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx"
)

// Run starts the TUI and blocks until the user quits.
func Run(cfg config.Config, svc gitlabx.Service) error {
	_, err := tea.NewProgram(NewApp(cfg, svc)).Run()
	return err
}
