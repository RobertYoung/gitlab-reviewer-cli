package position

import (
	"errors"
	"strconv"
	"testing"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx"
)

var refs = gitlabx.DiffRefs{BaseSHA: "base", HeadSHA: "head", StartSHA: "start"}

func ptr(n int) *int { return &n }

// modifiedDiff: file with one hunk — context at 10-11, removed old 12,
// added new 12-13, context at old 13 / new 14.
const modifiedDiff = `@@ -10,4 +10,5 @@ func main() {
 keep one
 keep two
-removed line
+added one
+added two
 keep three
`

// newFileDiff: brand-new file, three lines.
const newFileDiff = `@@ -0,0 +1,3 @@
+package x
+
+func New() {}
`

// deletedFileDiff: file removed entirely.
const deletedFileDiff = `@@ -1,2 +0,0 @@
-package gone
-func Old() {}
`

// renamedDiff: renamed file with a one-line change at line 5.
const renamedDiff = `@@ -3,5 +3,5 @@
 ctx a
 ctx b
-old five
+new five
 ctx c
 ctx d
`

func fixtureIndex() []FileIndex {
	return Index([]gitlabx.FileDiff{
		{OldPath: "main.go", NewPath: "main.go", Diff: modifiedDiff},
		{OldPath: "new.go", NewPath: "new.go", NewFile: true, Diff: newFileDiff},
		{OldPath: "gone.go", NewPath: "gone.go", DeletedFile: true, Diff: deletedFileDiff},
		{OldPath: "before.go", NewPath: "after.go", RenamedFile: true, Diff: renamedDiff},
	})
}

func TestResolve(t *testing.T) {
	index := fixtureIndex()

	tests := []struct {
		name     string
		file     string
		oldLine  *int
		newLine  *int
		wantOld  *int
		wantNew  *int
		wantPath [2]string // old, new
		wantErr  bool
	}{
		{
			name: "added line → new only",
			file: "main.go", newLine: ptr(12),
			wantNew: ptr(12), wantPath: [2]string{"main.go", "main.go"},
		},
		{
			name: "second added line → new only",
			file: "main.go", newLine: ptr(13),
			wantNew: ptr(13), wantPath: [2]string{"main.go", "main.go"},
		},
		{
			name: "removed line → old only",
			file: "main.go", oldLine: ptr(12),
			wantOld: ptr(12), wantPath: [2]string{"main.go", "main.go"},
		},
		{
			name: "context line via new → both sides",
			file: "main.go", newLine: ptr(10),
			wantOld: ptr(10), wantNew: ptr(10), wantPath: [2]string{"main.go", "main.go"},
		},
		{
			name: "context after change has shifted counterpart",
			file: "main.go", newLine: ptr(14),
			wantOld: ptr(13), wantNew: ptr(14), wantPath: [2]string{"main.go", "main.go"},
		},
		{
			name: "context line via old side",
			file: "main.go", oldLine: ptr(13),
			wantOld: ptr(13), wantNew: ptr(14), wantPath: [2]string{"main.go", "main.go"},
		},
		{
			name: "new file line",
			file: "new.go", newLine: ptr(1),
			wantNew: ptr(1), wantPath: [2]string{"new.go", "new.go"},
		},
		{
			name: "deleted file line",
			file: "gone.go", oldLine: ptr(2),
			wantOld: ptr(2), wantPath: [2]string{"gone.go", "gone.go"},
		},
		{
			name: "renamed file: paths come from the diff entry",
			file: "after.go", newLine: ptr(5),
			wantNew: ptr(5), wantPath: [2]string{"before.go", "after.go"},
		},
		{
			name: "renamed file addressed by old path still resolves",
			file: "before.go", newLine: ptr(5),
			wantNew: ptr(5), wantPath: [2]string{"before.go", "after.go"},
		},
		{
			name: "line outside any hunk",
			file: "main.go", newLine: ptr(500),
			wantErr: true,
		},
		{
			name: "unknown file",
			file: "nope.go", newLine: ptr(1),
			wantErr: true,
		},
		{
			name: "model reported line on wrong side falls through to old",
			file: "main.go", oldLine: ptr(12), newLine: ptr(999),
			wantOld: ptr(12), wantPath: [2]string{"main.go", "main.go"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pos, err := Resolve(tt.file, tt.oldLine, tt.newLine, index, refs)
			if tt.wantErr {
				if !errors.Is(err, ErrUnresolved) {
					t.Fatalf("want ErrUnresolved, got %v (pos %+v)", err, pos)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if pos.BaseSHA != "base" || pos.HeadSHA != "head" || pos.StartSHA != "start" {
				t.Errorf("SHAs not copied: %+v", pos)
			}
			if pos.OldPath != tt.wantPath[0] || pos.NewPath != tt.wantPath[1] {
				t.Errorf("paths = %s→%s, want %s→%s", pos.OldPath, pos.NewPath, tt.wantPath[0], tt.wantPath[1])
			}
			if !lineEq(pos.OldLine, tt.wantOld) || !lineEq(pos.NewLine, tt.wantNew) {
				t.Errorf("lines = old:%s new:%s, want old:%s new:%s",
					fmtLine(pos.OldLine), fmtLine(pos.NewLine), fmtLine(tt.wantOld), fmtLine(tt.wantNew))
			}
		})
	}
}

func lineEq(a, b *int) bool {
	if (a == nil) != (b == nil) {
		return false
	}
	return a == nil || *a == *b
}

func fmtLine(p *int) string {
	if p == nil {
		return "nil"
	}
	return strconv.Itoa(*p)
}

func TestParseHunkHeader(t *testing.T) {
	tests := []struct {
		line string
		old  int
		new  int
		ok   bool
	}{
		{"@@ -10,4 +10,5 @@ func main() {", 10, 10, true},
		{"@@ -0,0 +1,3 @@", 1, 1, true},
		{"@@ -1,2 +0,0 @@", 1, 1, true},
		{"@@ -7 +7 @@", 7, 7, true},
		{"not a header", 0, 0, false},
		{"@@ garbage @@", 0, 0, false},
	}
	for _, tt := range tests {
		o, n, ok := parseHunkHeader(tt.line)
		if ok != tt.ok || o != tt.old || n != tt.new {
			t.Errorf("parseHunkHeader(%q) = %d,%d,%v want %d,%d,%v", tt.line, o, n, ok, tt.old, tt.new, tt.ok)
		}
	}
}

func TestMultiHunkIndexing(t *testing.T) {
	diff := `@@ -5,3 +5,4 @@
 a
+inserted
 b
 c
@@ -50,3 +51,3 @@
 x
-old y
+new y
 z
`
	index := Index([]gitlabx.FileDiff{{OldPath: "m.go", NewPath: "m.go", Diff: diff}})

	// Second hunk: new line 52 is the added replacement.
	pos, err := Resolve("m.go", nil, ptr(52), index, refs)
	if err != nil {
		t.Fatal(err)
	}
	if pos.NewLine == nil || *pos.NewLine != 52 || pos.OldLine != nil {
		t.Errorf("added in 2nd hunk: %+v", pos)
	}

	// Second hunk removed line: old 51.
	pos, err = Resolve("m.go", ptr(51), nil, index, refs)
	if err != nil {
		t.Fatal(err)
	}
	if pos.OldLine == nil || *pos.OldLine != 51 || pos.NewLine != nil {
		t.Errorf("removed in 2nd hunk: %+v", pos)
	}

	// Gap between hunks is unresolvable.
	if _, err := Resolve("m.go", nil, ptr(20), index, refs); !errors.Is(err, ErrUnresolved) {
		t.Errorf("gap line: %v", err)
	}
}
