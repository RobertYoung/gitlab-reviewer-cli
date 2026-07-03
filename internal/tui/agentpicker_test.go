package tui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review/agents"
)

func pickerDeps(t *testing.T) Deps {
	t.Helper()
	deps := testDeps(&fakeService{})
	deps.Agents = agents.NewCatalog("")
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
	return &fakeService{repoFiles: []gitlabx.RepoFile{{
		Name:    "sql.md",
		Content: []byte("---\nname: sql-migrations\ndescription: Lock hazards\n---\nLook for locks.\n"),
	}}}
}

func TestAgentPickerMergesRepoAgents(t *testing.T) {
	deps := testDeps(repoAgentService())
	deps.Agents = agents.NewCatalog("")
	deps.Selection = agents.NewSelectionStore(filepath.Join(t.TempDir(), "sel.json"))

	p, deliver := repoAgentPicker(t, deps)
	before := len(p.agents)
	deliver()

	if len(p.agents) != before+1 || p.agents[len(p.agents)-1].Name != "sql-migrations" {
		t.Fatalf("repo agent not merged: %v", p.selected())
	}
	// With no remembered selection everything is checked, repo agents too.
	if !p.checked["sql-migrations"] {
		t.Errorf("repo agent not checked by default: %v", p.selected())
	}
	p.width = 100
	if !strings.Contains(p.View(), "(project)") {
		t.Errorf("view missing the project badge:\n%s", p.View())
	}
}

func TestAgentPickerRepoAgentsRespectRememberedSelection(t *testing.T) {
	deps := testDeps(repoAgentService())
	deps.Agents = agents.NewCatalog("")
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
	dir := filepath.Join(root, "gitlab.example.com", "group", "app", ".gitlab-reviewer", "agents")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "local-only.md"), []byte("Untracked local agent.\n"), 0o600); err != nil {
		t.Fatal(err)
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
	if p.agents[len(p.agents)-1].Name != "local-only" {
		t.Fatalf("local agent not merged: %v", p.selected())
	}
	if !p.checked["local-only"] {
		t.Errorf("local agent not checked by default: %v", p.selected())
	}
}
