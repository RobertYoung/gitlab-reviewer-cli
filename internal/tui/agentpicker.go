package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review/agents"
)

// agentPicker is the multi-select screen shown before a review run: which
// review agents should this scan use. Enter swaps it for the run screen;
// the selection is remembered per project.
type agentPicker struct {
	deps    Deps
	detail  gitlabx.MRDetail
	diffs   []gitlabx.FileDiff
	commits []gitlabx.Commit

	// manual comments ride through to the run screen unchanged.
	manual       []review.Finding
	manualReport func(string, review.FindingState)

	agents   []agents.Agent
	checked  map[string]bool
	warnings []string
	cursor   int
	width    int
	height   int
}

func newAgentPicker(deps Deps, detail gitlabx.MRDetail, diffs []gitlabx.FileDiff, commits []gitlabx.Commit, manual []review.Finding, manualReport func(string, review.FindingState)) *agentPicker {
	catalog := deps.Agents
	if catalog == nil {
		catalog = agents.NewCatalog("")
	}
	p := &agentPicker{
		deps:         deps,
		detail:       detail,
		diffs:        diffs,
		commits:      commits,
		manual:       manual,
		manualReport: manualReport,
		agents:       catalog.All(),
		warnings:     catalog.Warnings(),
		checked:      map[string]bool{},
	}

	// Initial checks: last selection for this project, then the configured
	// default (which the categories alias already folds into).
	initial := deps.Selection.Load(detail.ProjectPath)
	if len(initial) == 0 {
		initial = deps.cfgFor(detail.ProjectPath).Review.Agents
	}
	known := map[string]bool{}
	for _, a := range p.agents {
		known[a.Name] = true
	}
	for _, name := range initial {
		if known[name] {
			p.checked[name] = true
		}
	}
	if len(p.checked) == 0 {
		for _, a := range p.agents {
			p.checked[a.Name] = true
		}
	}
	return p
}

func (p *agentPicker) Title() string {
	return fmt.Sprintf("select agents for %s", p.detail.Ref())
}

func (p *agentPicker) Hints() string {
	return "space toggle · a all · n none · enter start review · esc back"
}

func (p *agentPicker) Init() tea.Cmd { return nil }

func (p *agentPicker) selected() []string {
	var names []string
	for _, a := range p.agents {
		if p.checked[a.Name] {
			names = append(names, a.Name)
		}
	}
	return names
}

func (p *agentPicker) Update(msg tea.Msg) (Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		p.width, p.height = msg.Width, msg.Height
		return p, nil

	case tea.KeyPressMsg:
		switch msg.String() {
		case "up", "k":
			if p.cursor > 0 {
				p.cursor--
			}
		case "down", "j":
			if p.cursor < len(p.agents)-1 {
				p.cursor++
			}
		case "space":
			name := p.agents[p.cursor].Name
			p.checked[name] = !p.checked[name]
		case "a":
			for _, a := range p.agents {
				p.checked[a.Name] = true
			}
		case "n":
			p.checked = map[string]bool{}
		case "enter":
			selected := p.selected()
			if len(selected) == 0 {
				return p, nil
			}
			p.deps.Selection.Save(p.detail.ProjectPath, selected)
			return p, popScreens(1, newReviewRun(p.deps, p.detail, p.diffs, p.commits, p.manual, p.manualReport, selected))
		case "esc", "q":
			return p, popScreen
		}
	}
	return p, nil
}

func (p *agentPicker) View() string {
	nameWidth := 0
	for _, a := range p.agents {
		nameWidth = max(nameWidth, len(a.Name))
	}

	var b strings.Builder
	b.WriteString(headerStyle.Render("which agents should review this MR?") + "\n\n")
	for i, a := range p.agents {
		prefix := "  "
		if i == p.cursor {
			prefix = "> "
		}
		box := "[ ]"
		if p.checked[a.Name] {
			box = "[x]"
		}
		line := fmt.Sprintf("%s%s %-*s  %s", prefix, box, nameWidth, a.Name, a.Description)
		if a.Source != agents.SourceBuiltin {
			line += subtleStyle.Render(" (" + string(a.Source) + ")")
		}
		b.WriteString(truncate(line, max(p.width-2, 20)) + "\n")
	}
	fmt.Fprintf(&b, "\n%s\n", subtleStyle.Render(fmt.Sprintf("%d of %d selected", len(p.selected()), len(p.agents))))
	for _, w := range p.warnings {
		b.WriteString(errorStyle.Render(truncate(w, max(p.width-2, 20))) + "\n")
	}
	return b.String()
}
