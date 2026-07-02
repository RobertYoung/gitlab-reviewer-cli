package tui

import (
	"strings"
	"testing"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx"
)

func treeDiffs() []gitlabx.FileDiff {
	return []gitlabx.FileDiff{
		{NewPath: "internal/tui/app.go"},
		{NewPath: "internal/tui/help.go", NewFile: true},
		{NewPath: "cmd/main.go"},
		{NewPath: "README.md", OldPath: "README.md"},
		{OldPath: "internal/old.go", NewPath: "internal/old.go", DeletedFile: true},
	}
}

// names flattens the visible rows into "name" for dirs-with-slash / files.
func names(t *fileTree) []string {
	out := make([]string, 0, len(t.rows))
	for _, n := range t.rows {
		name := n.name
		if n.isDir() {
			name += "/"
		}
		out = append(out, name)
	}
	return out
}

func TestFileTreeBuildOrdersDirsFirst(t *testing.T) {
	ft := newFileTree(treeDiffs())
	got := names(ft)
	want := []string{"cmd/", "main.go", "internal/", "tui/", "app.go", "help.go", "old.go", "README.md"}
	if len(got) != len(want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("rows = %v, want %v", got, want)
		}
	}
}

func TestFileTreeCollapseAndReveal(t *testing.T) {
	ft := newFileTree(treeDiffs())

	// fold internal/ — its subtree disappears
	ft.cursorTo(findDiff(ft.root, 0).parent.parent) // internal/
	ft.toggle()
	got := names(ft)
	want := []string{"cmd/", "main.go", "internal/", "README.md"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("after fold rows = %v, want %v", got, want)
		}
	}

	// reveal expands the folded ancestors and lands on the file
	ft.reveal(1) // internal/tui/help.go
	sel := ft.selected()
	if sel == nil || sel.diffIdx != 1 {
		t.Fatalf("selected after reveal = %+v", sel)
	}

	// h on a file jumps to its parent directory; another h folds it
	ft.collapseOrUp()
	if sel = ft.selected(); sel == nil || !sel.isDir() || sel.name != "tui" {
		t.Fatalf("selected after collapseOrUp = %+v", sel)
	}
	ft.collapseOrUp()
	if sel = ft.selected(); sel == nil || !sel.collapsed {
		t.Fatalf("tui/ not folded: %+v", sel)
	}
}

func TestFileTreeDiscussionCounts(t *testing.T) {
	diffs := treeDiffs()
	line := 3
	ft := newFileTree(diffs)
	ft.setDiscussions(diffs, []gitlabx.Discussion{
		{ID: "1", Notes: []gitlabx.Note{{Position: &gitlabx.Position{NewPath: "internal/tui/app.go", NewLine: &line}}}},
		{ID: "2", Notes: []gitlabx.Note{{Position: &gitlabx.Position{NewPath: "internal/tui/app.go", NewLine: &line}}}},
		{ID: "3", Notes: []gitlabx.Note{{Body: "general note, no anchor"}}},
	})
	if ft.counts[0] != 2 {
		t.Errorf("counts[0] = %d, want 2", ft.counts[0])
	}
	if len(ft.counts) != 1 {
		t.Errorf("counts = %v, want only app.go", ft.counts)
	}

	view := stripANSI(ft.view(diffs, 30, 20, true, 0))
	if !strings.Contains(view, "💬2") {
		t.Errorf("view missing discussion count:\n%s", view)
	}
}

func TestFileTreeViewGlyphsAndCursor(t *testing.T) {
	diffs := treeDiffs()
	ft := newFileTree(diffs)
	ft.reveal(1) // help.go, a new file

	view := stripANSI(ft.view(diffs, 30, 20, true, 1))
	if !strings.Contains(view, "> ") {
		t.Errorf("view missing cursor marker:\n%s", view)
	}
	if !strings.Contains(view, "A help.go") {
		t.Errorf("view missing added glyph:\n%s", view)
	}
	if !strings.Contains(view, "D old.go") {
		t.Errorf("view missing deleted glyph:\n%s", view)
	}
	if !strings.Contains(view, "M app.go") {
		t.Errorf("view missing modified glyph:\n%s", view)
	}

	// scrolls to keep the cursor visible in a short window
	ft.last()
	short := stripANSI(ft.view(diffs, 30, 3, true, 1))
	if !strings.Contains(short, "README.md") {
		t.Errorf("short view missing last row:\n%s", short)
	}
	if lines := strings.Split(short, "\n"); len(lines) != 3 {
		t.Errorf("short view has %d lines, want 3", len(lines))
	}
}
