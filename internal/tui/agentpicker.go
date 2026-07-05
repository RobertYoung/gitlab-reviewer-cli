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

	// models holds the per-agent model overrides the user picked (agent name
	// → model ID); an agent absent from the map runs with its frontmatter
	// model, then the configured default. Seeded from the remembered
	// selection and carried to the run.
	models map[string]string
	// modelChoices is the menu the "m" chooser offers: "" (the default entry)
	// followed by cfg.ModelOptions(). choosing shows it for the cursor agent,
	// with modelCursor the highlighted row.
	modelChoices []string
	choosing     bool
	modelCursor  int

	// initial is the remembered/configured selection; allByDefault records
	// that it was empty (everything checked). Both are re-applied to repo
	// agents when the background fetch delivers them.
	initial      []string
	allByDefault bool
	fetching     bool
	// localRepo is the user's local clone (path/root checkout modes); when
	// set, repo agents are read from it instead of the API.
	localRepo string

	// prevHead is the head commit of the MR's newest stored review; when set
	// the run defaults to an incremental re-review of the changes since it,
	// carrying the previous findings (and their curation states) forward.
	// full is the user's override to scan the whole diff again.
	prevHead string
	full     bool
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

	// A stored review with a tracked head makes an incremental run possible;
	// the runner re-verifies (and falls back) when the run actually starts.
	if prev, err := deps.Results.Latest(detail.Ref()); err == nil && prev != nil && prev.HeadSHA != "" {
		p.prevHead = prev.HeadSHA
	}

	// Per-agent model overrides: the remembered picks for this project, plus
	// the "" default entry ahead of the configured model list for the chooser.
	p.models = map[string]string{}
	for name, m := range deps.Selection.LoadModels(detail.ProjectPath) {
		p.models[name] = m
	}
	p.modelChoices = append([]string{""}, cfg.ModelOptions()...)

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
	if p.choosing {
		return "↑/↓ move · enter choose model · esc cancel"
	}
	if p.prevHead != "" {
		return "space toggle · m model · a all · n none · f full/incremental · enter start review · esc back"
	}
	return "space toggle · m model · a all · n none · enter start review · esc back"
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
		if p.choosing {
			return p.updateChoosing(msg)
		}
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
		case "m":
			if len(p.agents) > 0 {
				p.openModelChooser()
			}
		case "a":
			for _, a := range p.agents {
				p.checked[a.Name] = true
			}
		case "n":
			p.checked = map[string]bool{}
		case "f":
			if p.prevHead != "" {
				p.full = !p.full
			}
		case "enter":
			selected := p.selected()
			if len(selected) == 0 {
				return p, nil
			}
			models := p.selectedModels(selected)
			p.deps.Selection.Save(p.detail.ProjectPath, selected)
			p.deps.Selection.SaveModels(p.detail.ProjectPath, models)
			incremental := p.prevHead != "" && !p.full
			return p, popScreens(1, newReviewRun(p.deps, p.detail, p.diffs, p.commits, p.manual, p.manualReport, selected, models, incremental))
		case "esc", "q":
			return p, popScreen
		}
	}
	return p, nil
}

// openModelChooser starts the per-agent model menu for the cursor agent,
// highlighting its current pick (or the default entry if none).
func (p *agentPicker) openModelChooser() {
	p.choosing = true
	p.modelCursor = 0
	current := p.models[p.agents[p.cursor].Name]
	for i, m := range p.modelChoices {
		if m == current {
			p.modelCursor = i
			break
		}
	}
}

// updateChoosing handles keys while the model menu is open: navigate, pick
// (the "" default entry clears the override), or cancel.
func (p *agentPicker) updateChoosing(msg tea.KeyPressMsg) (Screen, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		if p.modelCursor > 0 {
			p.modelCursor--
		}
	case "down", "j":
		if p.modelCursor < len(p.modelChoices)-1 {
			p.modelCursor++
		}
	case "enter", "space":
		name := p.agents[p.cursor].Name
		if choice := p.modelChoices[p.modelCursor]; choice == "" {
			delete(p.models, name)
		} else {
			p.models[name] = choice
		}
		p.choosing = false
	case "esc", "q":
		p.choosing = false
	}
	return p, nil
}

// selectedModels is the per-agent model overrides restricted to the checked
// agents, dropping empties — the map handed to the run and remembered.
func (p *agentPicker) selectedModels(selected []string) map[string]string {
	models := map[string]string{}
	for _, name := range selected {
		if m := p.models[name]; m != "" {
			models[name] = m
		}
	}
	return models
}

// displayModel is the model shown on an agent's row: the user's pick, else
// its frontmatter model, else the "(default)" placeholder.
func (p *agentPicker) displayModel(a agents.Agent) string {
	if m := p.models[a.Name]; m != "" {
		return m
	}
	if a.Model != "" {
		return a.Model
	}
	return "(default)"
}

func (p *agentPicker) View() string {
	if p.choosing {
		return p.viewChooser()
	}

	nameWidth, modelWidth := 0, 0
	for _, a := range p.agents {
		nameWidth = max(nameWidth, len(a.Name))
		modelWidth = max(modelWidth, len(p.displayModel(a)))
	}

	var b strings.Builder
	b.WriteString(headerStyle.Render("which agents should review this MR?") + "\n\n")

	// Window the list around the cursor so it never runs off the bottom:
	// reserve the header (2 lines) and the footer (the count, the optional
	// fetch note, and any warnings), then show only what fits. With no known
	// height (e.g. before the first WindowSizeMsg) show everything.
	start, end := 0, len(p.agents)
	if p.height > 0 {
		// Header (title + blank) and footer (blank + count) are four lines;
		// the fetch note and each warning add one more.
		reserved := 4 + len(p.warnings)
		if p.fetching {
			reserved++
		}
		visible := max(p.height-reserved, 3)
		if p.cursor >= visible {
			start = p.cursor - visible + 1
		}
		end = min(start+visible, len(p.agents))
	}
	for i := start; i < end; i++ {
		a := p.agents[i]
		prefix := "  "
		if i == p.cursor {
			prefix = "> "
		}
		box := "[ ]"
		if p.checked[a.Name] {
			box = "[x]"
		}
		model := fmt.Sprintf("%-*s", modelWidth, p.displayModel(a))
		// Dim the model column only when it is the bare "(default)" fallback;
		// an explicit pick or a frontmatter model reads as a real choice.
		if p.models[a.Name] == "" && a.Model == "" {
			model = subtleStyle.Render(model)
		}
		line := fmt.Sprintf("%s%s %-*s  %s  %s", prefix, box, nameWidth, a.Name, model, a.Description)
		if a.Source != agents.SourceBuiltin {
			line += subtleStyle.Render(" (" + string(a.Source) + ")")
		}
		b.WriteString(truncate(line, max(p.width-2, 20)) + "\n")
	}
	fmt.Fprintf(&b, "\n%s\n", subtleStyle.Render(fmt.Sprintf("%d of %d selected", len(p.selected()), len(p.agents))))
	if p.prevHead != "" {
		scope := fmt.Sprintf("incremental: reviewing the changes since %.8s, carrying previous findings forward (f to scan the full diff)", p.prevHead)
		if p.full {
			scope = "full re-review: the whole diff will be scanned (f for incremental)"
		}
		b.WriteString(subtleStyle.Render(truncate(scope, max(p.width-2, 20))) + "\n")
	}
	if p.fetching {
		b.WriteString(subtleStyle.Render("looking for agents shipped in the repo…") + "\n")
	}
	for _, w := range p.warnings {
		b.WriteString(errorStyle.Render(truncate(w, max(p.width-2, 20))) + "\n")
	}
	return b.String()
}

// viewChooser renders the per-agent model menu shown while choosing.
func (p *agentPicker) viewChooser() string {
	var b strings.Builder
	b.WriteString(headerStyle.Render("model for "+p.agents[p.cursor].Name) + "\n\n")

	start, end := 0, len(p.modelChoices)
	if p.height > 0 {
		visible := max(p.height-3, 3)
		if p.modelCursor >= visible {
			start = p.modelCursor - visible + 1
		}
		end = min(start+visible, len(p.modelChoices))
	}
	for i := start; i < end; i++ {
		prefix := "  "
		if i == p.modelCursor {
			prefix = "> "
		}
		label := p.modelChoices[i]
		if label == "" {
			label = subtleStyle.Render("(default)")
		}
		b.WriteString(truncate(prefix+label, max(p.width-2, 20)) + "\n")
	}
	return b.String()
}
