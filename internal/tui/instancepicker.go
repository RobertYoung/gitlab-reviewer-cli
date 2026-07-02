package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/config"
)

// SelectInstance runs a minimal inline picker over the configured GitLab
// instances and returns the chosen name. It runs before the main TUI starts
// because the GitLab client is built for the chosen instance.
func SelectInstance(instances []config.Instance) (string, error) {
	m, err := tea.NewProgram(&instancePicker{instances: instances}).Run()
	if err != nil {
		return "", err
	}
	picker := m.(*instancePicker)
	if picker.choice == "" {
		return "", fmt.Errorf("no GitLab instance selected")
	}
	return picker.choice, nil
}

type instancePicker struct {
	instances []config.Instance
	cursor    int
	choice    string
}

func (p *instancePicker) Init() tea.Cmd { return nil }

func (p *instancePicker) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "up", "k":
			if p.cursor > 0 {
				p.cursor--
			}
		case "down", "j":
			if p.cursor < len(p.instances)-1 {
				p.cursor++
			}
		case "enter":
			p.choice = p.instances[p.cursor].Name
			return p, tea.Quit
		case "q", "esc", "ctrl+c":
			return p, tea.Quit
		}
	}
	return p, nil
}

func (p *instancePicker) View() tea.View {
	nameWidth := 0
	for _, inst := range p.instances {
		nameWidth = max(nameWidth, len(inst.Name))
	}

	var b strings.Builder
	b.WriteString(headerStyle.Render("select a gitlab instance") + "\n")
	for i, inst := range p.instances {
		prefix := "  "
		if i == p.cursor {
			prefix = "> "
		}
		fmt.Fprintf(&b, "%s%-*s  %s\n", prefix, nameWidth, inst.Name, subtleStyle.Render(inst.BaseURL))
	}
	b.WriteString(statusStyle.Render("↑/↓ move · enter select · q quit"))
	return tea.NewView(b.String())
}
