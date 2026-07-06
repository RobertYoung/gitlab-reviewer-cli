package tui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review/agents"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review/resultstore"
)

func pickerDeps(t *testing.T) Deps {
	t.Helper()
	deps := testDeps(&fakeService{})
	deps.Agents = agents.NewCatalog(nil, nil)
	deps.Selection = agents.NewSelectionStore(filepath.Join(t.TempDir(), "sel.json"))
	return deps
}

func TestAgentPickerTogglesAndStarts(t *testing.T) {
	detail, diffs, _ := reviewFixture()
	deps := pickerDeps(t)
	deps.Cfg.Review.Agents = []string{"bug", "security"}

	p := newAgentPicker(deps, *detail, diffs, nil, nil, nil)
	if got := p.selected(); !slices.Equal(got, []string{"bug", "security"}) {
		t.Fatalf("initial selection from config: %v", got)
	}

	var screen Screen = p
	screen, _ = screen.Update(tea.WindowSizeMsg{Width: 100, Height: 20})

	// Cursor starts on "bug"; toggle it off.
	screen, _ = screen.Update(tea.KeyPressMsg{Code: tea.KeySpace})
	if got := p.selected(); !slices.Equal(got, []string{"security"}) {
		t.Fatalf("after toggle: %v", got)
	}

	// "a" selects everything, "n" clears.
	screen, _ = screen.Update(key("a"))
	if got := len(p.selected()); got != len(p.agents) {
		t.Fatalf("select all: %d of %d", got, len(p.agents))
	}
	screen, _ = screen.Update(key("n"))
	if got := p.selected(); got != nil {
		t.Fatalf("select none: %v", got)
	}

	// Enter with nothing selected must not start a run.
	_, cmd := screen.Update(key("enter"))
	if cmd != nil {
		t.Fatal("enter with empty selection must be a no-op")
	}

	// Pick one agent and start: the picker swaps itself for the run screen
	// and remembers the selection.
	screen, _ = screen.Update(tea.KeyPressMsg{Code: tea.KeySpace})
	_, cmd = screen.Update(key("enter"))
	if cmd == nil {
		t.Fatal("enter must produce a navigation command")
	}
	msg, ok := cmd().(popScreenMsg)
	if !ok {
		t.Fatalf("enter must swap screens, got %T", cmd())
	}
	run, ok := msg.replacement.(*reviewRun)
	if !ok {
		t.Fatalf("replacement is %T, want *reviewRun", msg.replacement)
	}
	if !slices.Equal(run.agentNames, []string{"bug"}) {
		t.Fatalf("run selection: %v", run.agentNames)
	}
	if got := deps.Selection.Load(detail.ProjectPath); !slices.Equal(got, []string{"bug"}) {
		t.Fatalf("selection not remembered: %v", got)
	}
}

func TestAgentPickerModelChooser(t *testing.T) {
	detail, diffs, _ := reviewFixture()
	deps := pickerDeps(t)
	deps.Cfg.Review.Agents = []string{"bug", "security"}
	// A short, known model list so we can navigate to a specific entry.
	deps.Cfg.Review.Models = []string{"opus", "sonnet"}

	var screen Screen = newAgentPicker(deps, *detail, diffs, nil, nil, nil)
	p := screen.(*agentPicker)
	screen, _ = screen.Update(tea.WindowSizeMsg{Width: 100, Height: 20})

	// Cursor is on "bug". Open the chooser: choices are "" then the models.
	screen, _ = screen.Update(key("m"))
	if !p.choosing {
		t.Fatal("m must open the model chooser")
	}
	if !strings.Contains(p.View(), "model for bug") {
		t.Errorf("chooser view missing header:\n%s", p.View())
	}
	// Step to "sonnet" (index 2: "", opus, sonnet) and choose it.
	screen, _ = screen.Update(key("j"))
	screen, _ = screen.Update(key("j"))
	screen, _ = screen.Update(key("enter"))
	if p.choosing {
		t.Fatal("enter must close the chooser")
	}
	if p.models["bug"] != "sonnet" {
		t.Fatalf("bug model = %q, want sonnet", p.models["bug"])
	}
	// The row now shows the chosen model.
	if !strings.Contains(p.View(), "sonnet") {
		t.Errorf("view missing chosen model:\n%s", p.View())
	}

	// Start the review: the choice reaches the run and is remembered.
	_, cmd := screen.Update(key("enter"))
	msg, ok := cmd().(popScreenMsg)
	if !ok {
		t.Fatalf("enter must swap screens, got %T", cmd())
	}
	run := msg.replacement.(*reviewRun)
	if run.agentModels["bug"] != "sonnet" {
		t.Fatalf("run models: %v", run.agentModels)
	}
	if got := deps.Selection.LoadModels(detail.ProjectPath); got["bug"] != "sonnet" {
		t.Fatalf("models not remembered: %v", got)
	}

	// Reopening on the same agent clears the pick via the "(default)" entry.
	p2 := newAgentPicker(deps, *detail, diffs, nil, nil, nil)
	if p2.models["bug"] != "sonnet" {
		t.Fatalf("remembered model not seeded: %v", p2.models)
	}
	var s2 Screen = p2
	s2, _ = s2.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	s2, _ = s2.Update(key("m")) // opens with cursor on the current pick
	s2, _ = s2.Update(key("k")) // up toward the "(default)" entry
	s2, _ = s2.Update(key("k"))
	_, _ = s2.Update(key("enter"))
	if _, ok := p2.models["bug"]; ok {
		t.Fatalf("default entry must clear the override: %v", p2.models)
	}
}

func TestAgentPickerPrefersRememberedSelection(t *testing.T) {
	detail, diffs, _ := reviewFixture()
	deps := pickerDeps(t)
	deps.Selection.Save(detail.ProjectPath, []string{"docs"})

	p := newAgentPicker(deps, *detail, diffs, nil, nil, nil)
	if got := p.selected(); !slices.Equal(got, []string{"docs"}) {
		t.Fatalf("selection: %v", got)
	}
}

func TestAgentPickerView(t *testing.T) {
	detail, diffs, _ := reviewFixture()
	p := newAgentPicker(pickerDeps(t), *detail, diffs, nil, nil, nil)
	p.width, p.height = 100, 20
	v := p.View()
	for _, want := range []string{"[x] bug", "[x] security", "selected"} {
		if !strings.Contains(v, want) {
			t.Errorf("view missing %q:\n%s", want, v)
		}
	}
}

// repoAgentFixture is a picker whose MR head ships one repo agent named
// "sql-migrations"; deliver() runs the Init fetch and applies its message.
func repoAgentPicker(t *testing.T, deps Deps) (*agentPicker, func()) {
	t.Helper()
	detail, diffs, _ := reviewFixture()
	detail.HeadSHA = "h"
	p := newAgentPicker(deps, *detail, diffs, nil, nil, nil)
	cmd := p.Init()
	if cmd == nil {
		t.Fatal("Init must fetch repo agents when the MR has a head SHA")
	}
	return p, func() {
		t.Helper()
		msg, ok := cmd().(projectAgentsMsg)
		if !ok {
			t.Fatalf("fetch must yield projectAgentsMsg")
		}
		if _, c := p.Update(msg); c != nil {
			t.Fatalf("merge must not emit commands")
		}
	}
}

func repoAgentService() *fakeService {
	return &fakeService{repoFilesByDir: map[string][]gitlabx.RepoFile{
		agents.ProjectAgentsDir: {{
			Name:    "sql.md",
			Content: []byte("---\nname: sql-migrations\ndescription: Lock hazards\n---\nLook for locks.\n"),
		}},
		agents.ClaudeAgentsDir: {{
			Name:    "conventions.md",
			Content: []byte("---\ndescription: Team conventions\n---\nCheck team conventions.\n"),
		}},
	}}
}

func TestAgentPickerMergesRepoAgents(t *testing.T) {
	deps := testDeps(repoAgentService())
	deps.Agents = agents.NewCatalog(nil, nil)
	deps.Selection = agents.NewSelectionStore(filepath.Join(t.TempDir(), "sel.json"))

	p, deliver := repoAgentPicker(t, deps)
	before := len(p.agents)
	deliver()

	// Both agent directories are fetched, .claude/agents first.
	if len(p.agents) != before+2 || p.agents[len(p.agents)-2].Name != "conventions" || p.agents[len(p.agents)-1].Name != "sql-migrations" {
		t.Fatalf("repo agents not merged: %v", p.selected())
	}
	// With no remembered selection everything is checked, repo agents too.
	if !p.checked["sql-migrations"] || !p.checked["conventions"] {
		t.Errorf("repo agents not checked by default: %v", p.selected())
	}
	p.width = 100
	if !strings.Contains(p.View(), "(project)") {
		t.Errorf("view missing the project badge:\n%s", p.View())
	}
}

func TestAgentPickerRepoAgentsRespectRememberedSelection(t *testing.T) {
	deps := testDeps(repoAgentService())
	deps.Agents = agents.NewCatalog(nil, nil)
	deps.Selection = agents.NewSelectionStore(filepath.Join(t.TempDir(), "sel.json"))
	deps.Selection.Save("group/app", []string{"docs"})

	p, deliver := repoAgentPicker(t, deps)
	deliver()

	if got := p.selected(); !slices.Equal(got, []string{"docs"}) {
		t.Fatalf("selection: %v", got)
	}

	// A remembered selection naming the repo agent checks it on arrival.
	deps.Selection.Save("group/app", []string{"docs", "sql-migrations"})
	p, deliver = repoAgentPicker(t, deps)
	deliver()
	if got := p.selected(); !slices.Equal(got, []string{"docs", "sql-migrations"}) {
		t.Fatalf("selection: %v", got)
	}
}

func TestAgentPickerSkipsFetchWithoutHeadSHA(t *testing.T) {
	detail, diffs, _ := reviewFixture()
	p := newAgentPicker(pickerDeps(t), *detail, diffs, nil, nil, nil)
	if cmd := p.Init(); cmd != nil {
		t.Fatal("no head SHA must skip the repo agents fetch")
	}
}

func TestAgentPickerReadsLocalClone(t *testing.T) {
	root := t.TempDir()
	clone := filepath.Join(root, "gitlab.example.com", "group", "app")
	for dir, name := range map[string]string{
		filepath.Join(clone, ".gitlab-reviewer", "agents"): "local-only.md",
		filepath.Join(clone, ".claude", "agents"):          "conventions.md",
	} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte("Untracked local agent.\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	deps := pickerDeps(t)
	deps.Cfg.Checkout.Mode = "root"
	deps.Cfg.Checkout.Root = root
	deps.Cfg.GitLab.BaseURL = "https://gitlab.example.com"

	// No head SHA needed: the local clone is read from disk.
	detail, diffs, _ := reviewFixture()
	p := newAgentPicker(deps, *detail, diffs, nil, nil, nil)
	cmd := p.Init()
	if cmd == nil {
		t.Fatal("Init must load agents from the local clone")
	}
	msg, ok := cmd().(projectAgentsMsg)
	if !ok || msg.err != nil {
		t.Fatalf("local load: %T %v", msg, msg.err)
	}
	if _, c := p.Update(msg); c != nil {
		t.Fatal("merge must not emit commands")
	}
	// Both agent directories in the clone are read, .claude/agents first.
	if p.agents[len(p.agents)-2].Name != "conventions" || p.agents[len(p.agents)-1].Name != "local-only" {
		t.Fatalf("local agents not merged: %v", p.selected())
	}
	if !p.checked["local-only"] || !p.checked["conventions"] {
		t.Errorf("local agents not checked by default: %v", p.selected())
	}
}

func TestAgentPickerIncrementalToggle(t *testing.T) {
	detail, diffs, _ := reviewFixture()
	deps := pickerDeps(t)
	deps.Cfg.Review.Agents = []string{"bug"}

	// Without a stored review there is no baseline: no toggle, full review.
	p := newAgentPicker(deps, *detail, diffs, nil, nil, nil)
	if p.prevHead != "" {
		t.Fatalf("prevHead = %q without any stored review", p.prevHead)
	}
	if strings.Contains(p.Hints(), "full/incremental") {
		t.Error("toggle hint shown without a baseline")
	}

	// With a stored review the picker defaults to incremental, and f toggles.
	deps.Results = resultstore.NewStore(t.TempDir())
	if err := deps.Results.Save(resultstore.Record{
		IID: detail.IID, Ref: detail.Ref(), Started: time.Unix(100, 0),
		BaseSHA: "b", HeadSHA: "prevhead1",
	}); err != nil {
		t.Fatal(err)
	}
	var screen Screen = newAgentPicker(deps, *detail, diffs, nil, nil, nil)
	p = screen.(*agentPicker)
	screen, _ = screen.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	if p.prevHead != "prevhead1" {
		t.Fatalf("prevHead = %q", p.prevHead)
	}
	if !strings.Contains(p.View(), "incremental: reviewing the changes since prevhead") {
		t.Errorf("view missing the incremental line:\n%s", p.View())
	}
	screen, _ = screen.Update(key("f"))
	if !p.full || !strings.Contains(p.View(), "full re-review") {
		t.Errorf("f must switch to a full re-review (full=%v):\n%s", p.full, p.View())
	}
	screen, _ = screen.Update(key("f"))
	if p.full {
		t.Error("f must toggle back to incremental")
	}

	// Starting the run carries the incremental choice.
	_, cmd := screen.Update(key("enter"))
	if cmd == nil {
		t.Fatal("enter must start the run")
	}
	msg, ok := cmd().(popScreenMsg)
	if !ok {
		t.Fatalf("enter must swap screens, got %T", cmd())
	}
	run := msg.replacement.(*reviewRun)
	if !run.incremental {
		t.Error("run must be incremental by default when a baseline exists")
	}
}
