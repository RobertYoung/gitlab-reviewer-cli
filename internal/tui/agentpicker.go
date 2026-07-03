package tui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/checkout"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review/agents"
)

// agentPicker is the multi-select screen shown before a review run: which
// review agents should this scan use. Enter swaps it for the run screen;
// the selection is remembered per project. Agents shipped in the repo's
// .gitlab-reviewer/agents/ or .claude/agents/ are loaded in the background
// — from the local clone in path/root checkout modes (covering untracked
// definitions), otherwise fetched over the API — and merged in when they
// arrive.
type agentPicker struct {
	deps    Deps
	detail  gitlabx.MRDetail
	diffs   []gitlabx.FileDiff
	commits []gitlabx.Commit

	// manual comments ride through to the run screen unchanged.
	manual       []review.Finding
	manualReport func(string, review.FindingState)

	base     *agents.Catalog // builtins + user agents, before repo extras
	agents   []agents.Agent
	checked  map[string]bool
	warnings []string
	cursor   int
	width    int
	height   int

	// initial is the remembered/configured selection; allByDefault records
	// that it was empty (everything checked). Both are re-applied to repo
	// agents when the background fetch delivers them.
	initial      []string
	allByDefault bool
	fetching     bool
	// localRepo is the user's local clone (path/root checkout modes); when
	// set, repo agents are read from it instead of the API.
	localRepo string
}

// projectAgentsMsg delivers the catalog extended with repo-fetched agents,
// or the fetch error.
type projectAgentsMsg struct {
	catalog *agents.Catalog
	err     error
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
		base:         catalog,
		agents:       catalog.All(),
		warnings:     catalog.Warnings(),
		checked:      map[string]bool{},
	}

	cfg := deps.cfgFor(detail.ProjectPath)
	p.localRepo, _ = checkout.LocalRepoDir(cfg.Checkout, cfg.GitLab.BaseURL, detail.ProjectPath)

	// Initial checks: last selection for this project, then the configured
	// default (which the categories alias already folds into).
	p.initial = deps.Selection.Load(detail.ProjectPath)
	if len(p.initial) == 0 {
		p.initial = cfg.Review.Agents
	}
	known := map[string]bool{}
	for _, a := range p.agents {
		known[a.Name] = true
	}
	for _, name := range p.initial {
		if known[name] {
			p.checked[name] = true
		}
	}
	if len(p.checked) == 0 {
		p.allByDefault = true
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

func (p *agentPicker) Init() tea.Cmd {
	if p.localRepo == "" && p.detail.HeadSHA == "" {
		return nil
	}
	p.fetching = true
	return p.fetchProjectAgents
}

// fetchProjectAgents loads the repo's agent directories (.claude/agents/
// and .gitlab-reviewer/agents/) so repo-shipped agents are toggleable
// before any checkout exists: straight from the local clone in path/root
// checkout modes (which also covers definitions kept untracked), otherwise
// over the API at the MR head, cached per (project, sha) in
// deps.ProjectAgents.
func (p *agentPicker) fetchProjectAgents() tea.Msg {
	if p.localRepo != "" {
		return projectAgentsMsg{catalog: p.base.WithProject(p.localRepo)}
	}
	cat, err := p.deps.ProjectAgents.Extend(p.base, p.detail.ProjectPath, p.detail.HeadSHA, func() ([]agents.File, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		var files []agents.File
		for _, dir := range agents.ProjectAgentDirs {
			repoFiles, err := p.deps.Svc.ListDirectoryFiles(ctx, p.detail.Project(), dir, p.detail.HeadSHA)
			if err != nil {
				return nil, err
			}
			for _, f := range repoFiles {
				files = append(files, agents.File{Dir: dir, Name: f.Name, Content: f.Content})
			}
		}
		return files, nil
	})
	return projectAgentsMsg{catalog: cat, err: err}
}

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

	case projectAgentsMsg:
		p.fetching = false
		if msg.err != nil {
			p.warnings = append(p.warnings, "agents: could not fetch repo agents: "+msg.err.Error())
			return p, nil
		}
		known := map[string]bool{}
		for _, a := range p.agents {
			known[a.Name] = true
		}
		p.agents = msg.catalog.All()
		p.warnings = msg.catalog.Warnings()
		// Repo agents the user hasn't seen before follow the same rules as
		// the initial checks: remembered selections apply by name, and the
		// everything-checked default extends to them.
		for _, a := range p.agents {
			if known[a.Name] {
				continue
			}
			if p.allByDefault || slices.Contains(p.initial, a.Name) {
				p.checked[a.Name] = true
			}
		}
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
	if p.fetching {
		b.WriteString(subtleStyle.Render("looking for agents shipped in the repo…") + "\n")
	}
	for _, w := range p.warnings {
		b.WriteString(errorStyle.Render(truncate(w, max(p.width-2, 20))) + "\n")
	}
	return b.String()
}
