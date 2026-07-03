package tui

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

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
