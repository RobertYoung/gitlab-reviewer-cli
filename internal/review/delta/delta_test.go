package delta

import (
	"testing"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review"
)

func intp(n int) *int { return &n }

// changedGo replaces old lines 5-6 with three new lines: lines 1-4 keep
// their numbers, old line 7+ shifts down by one.
var changedGo = gitlabx.FileDiff{
	OldPath: "changed.go",
	NewPath: "changed.go",
	Diff: "@@ -3,6 +3,7 @@ func f() {\n" +
		" three\n" +
		" four\n" +
		"-five\n" +
		"-six\n" +
		"+five'\n" +
		"+six'\n" +
		"+six-and-a-half\n" +
		" seven\n" +
		" eight\n",
}

// insertedGo inserts two lines after old line 4 (a pure insertion hunk).
var insertedGo = gitlabx.FileDiff{
	OldPath: "inserted.go",
	NewPath: "inserted.go",
	Diff:    "@@ -4,0 +5,2 @@ func g() {\n+new five\n+new six\n",
}

func TestMapLine(t *testing.T) {
	m := NewMapper([]gitlabx.FileDiff{
		changedGo,
		insertedGo,
		{OldPath: "gone.go", NewPath: "gone.go", DeletedFile: true, Diff: "@@ -1,2 +0,0 @@\n-a\n-b\n"},
		{OldPath: "old-name.go", NewPath: "new-name.go", RenamedFile: true, Diff: ""},
	})

	tests := []struct {
		name     string
		file     string
		line     int
		wantFile string
		wantLine int
		wantOK   bool
	}{
		{"untouched file maps to itself", "other.go", 42, "other.go", 42, true},
		{"context line before change", "changed.go", 4, "changed.go", 4, true},
		{"changed line is stale", "changed.go", 5, "", 0, false},
		{"second changed line is stale", "changed.go", 6, "", 0, false},
		{"context line after change", "changed.go", 7, "changed.go", 8, true},
		{"line after the hunk shifts", "changed.go", 20, "changed.go", 21, true},
		{"line before any hunk keeps its number", "changed.go", 1, "changed.go", 1, true},
		{"line at a pure insertion point stays", "inserted.go", 4, "inserted.go", 4, true},
		{"line after a pure insertion shifts", "inserted.go", 5, "inserted.go", 7, true},
		{"line before a pure insertion stays", "inserted.go", 2, "inserted.go", 2, true},
		{"deleted file is stale", "gone.go", 1, "", 0, false},
		{"renamed file follows the rename", "old-name.go", 3, "new-name.go", 3, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file, line, ok := m.MapLine(tt.file, tt.line)
			if ok != tt.wantOK || file != tt.wantFile || line != tt.wantLine {
				t.Errorf("MapLine(%s, %d) = (%q, %d, %v), want (%q, %d, %v)",
					tt.file, tt.line, file, line, ok, tt.wantFile, tt.wantLine, tt.wantOK)
			}
		})
	}
}

func TestCarryForward(t *testing.T) {
	prev := []review.Finding{
		{ID: "f001", File: "changed.go", Line: review.LineRef{NewLine: intp(4)}, State: review.StatePublished},
		{ID: "f002", File: "changed.go", Line: review.LineRef{NewLine: intp(5)}, State: review.StateAccepted},
		{ID: "f003", File: "changed.go", Line: review.LineRef{NewLine: intp(9)}, State: review.StateRejected},
		{ID: "f004", File: "untouched.go", Line: review.LineRef{NewLine: intp(2)}, State: review.StatePending},
		{ID: "m001", Body: "MR-level manual note", State: review.StatePublished, Manual: true},
	}
	kept, dropped := CarryForward(prev, []gitlabx.FileDiff{changedGo}, nil)

	if len(dropped) != 1 || dropped[0].ID != "f002" {
		t.Fatalf("dropped = %+v", dropped)
	}
	if len(kept) != 4 {
		t.Fatalf("kept = %+v", kept)
	}
	byID := map[string]review.Finding{}
	for _, f := range kept {
		byID[f.ID] = f
	}
	if f := byID["f001"]; *f.Line.NewLine != 4 || f.State != review.StatePublished {
		t.Errorf("f001 = %+v", f)
	}
	if f := byID["f003"]; *f.Line.NewLine != 10 || f.State != review.StateRejected {
		t.Errorf("f003 (after the hunk) = %+v", f)
	}
	if f := byID["f004"]; *f.Line.NewLine != 2 || f.State != review.StatePending {
		t.Errorf("f004 (untouched file) = %+v", f)
	}
	if _, ok := byID["m001"]; !ok {
		t.Error("MR-level manual note not carried")
	}
}

func TestCarryForwardRemovedLineFindings(t *testing.T) {
	// Findings anchored to removed lines (old side of the MR diff). The MR
	// diff says base line 3 of a.go is still removed but base line 8 is not.
	mrDiffs := []gitlabx.FileDiff{{
		OldPath: "a.go", NewPath: "a.go",
		Diff: "@@ -2,3 +2,2 @@\n two\n-three\n four\n",
	}}
	prev := []review.Finding{
		{ID: "f001", File: "a.go", Line: review.LineRef{OldLine: intp(3)}, State: review.StateAccepted},
		{ID: "f002", File: "a.go", Line: review.LineRef{OldLine: intp(8)}, State: review.StatePending},
		{ID: "f003", File: "b.go", Line: review.LineRef{OldLine: intp(1)}, State: review.StatePending},
	}
	// The delta touches a.go (any change) but not b.go.
	deltaDiffs := []gitlabx.FileDiff{{
		OldPath: "a.go", NewPath: "a.go",
		Diff: "@@ -10,1 +10,1 @@\n-x\n+y\n",
	}}

	kept, dropped := CarryForward(prev, deltaDiffs, mrDiffs)
	ids := func(fs []review.Finding) []string {
		var out []string
		for _, f := range fs {
			out = append(out, f.ID)
		}
		return out
	}
	if got := ids(kept); len(got) != 2 || got[0] != "f001" || got[1] != "f003" {
		t.Errorf("kept = %v", got)
	}
	if got := ids(dropped); len(got) != 1 || got[0] != "f002" {
		t.Errorf("dropped = %v", got)
	}
}

func TestCarryForwardRenameAndDelete(t *testing.T) {
	prev := []review.Finding{
		{ID: "f001", File: "old-name.go", Line: review.LineRef{NewLine: intp(3)}},
		{ID: "f002", File: "gone.go", Line: review.LineRef{NewLine: intp(1)}},
		{ID: "f003", File: "gone.go"}, // file-level, no line
	}
	deltaDiffs := []gitlabx.FileDiff{
		{OldPath: "old-name.go", NewPath: "new-name.go", RenamedFile: true, Diff: ""},
		{OldPath: "gone.go", NewPath: "gone.go", DeletedFile: true, Diff: "@@ -1,1 +0,0 @@\n-a\n"},
	}
	kept, dropped := CarryForward(prev, deltaDiffs, nil)
	if len(kept) != 1 || kept[0].File != "new-name.go" || *kept[0].Line.NewLine != 3 {
		t.Errorf("kept = %+v", kept)
	}
	if len(dropped) != 2 {
		t.Errorf("dropped = %+v", dropped)
	}
}
