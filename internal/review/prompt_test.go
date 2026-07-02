package review

import (
	"strings"
	"testing"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx"
)

func diffOfSize(path string, kb int) gitlabx.FileDiff {
	return gitlabx.FileDiff{
		OldPath: path, NewPath: path,
		Diff: "@@ -1 +1 @@\n+" + strings.Repeat("x", kb*1024-8) + "\n",
	}
}

func TestChunkDiffs(t *testing.T) {
	diffs := []gitlabx.FileDiff{
		diffOfSize("a.go", 40),
		diffOfSize("b.go", 40),
		diffOfSize("c.go", 40),
		diffOfSize("vendor/dep.go", 1),                              // excluded by glob
		{NewPath: "gen.go", GeneratedFile: true},                    // generated
		{NewPath: "huge.bin", Diff: string(make([]byte, 200*1024))}, // over whole budget
	}

	chunks, skipped := ChunkDiffs(diffs, []string{"vendor/**"}, 100)
	if len(chunks) != 2 {
		t.Fatalf("chunks = %d, want 2", len(chunks))
	}
	if len(chunks[0]) != 2 || chunks[0][0].NewPath != "a.go" || chunks[0][1].NewPath != "b.go" {
		t.Errorf("chunk 0: %v", paths(chunks[0]))
	}
	if len(chunks[1]) != 1 || chunks[1][0].NewPath != "c.go" {
		t.Errorf("chunk 1: %v", paths(chunks[1]))
	}
	want := []string{"vendor/dep.go", "gen.go", "huge.bin"}
	if len(skipped) != len(want) {
		t.Errorf("skipped = %v, want %v", skipped, want)
	}
}

func TestChunkDiffsAllFit(t *testing.T) {
	chunks, skipped := ChunkDiffs([]gitlabx.FileDiff{diffOfSize("a.go", 1), diffOfSize("b.go", 1)}, nil, 256)
	if len(chunks) != 1 || len(chunks[0]) != 2 || len(skipped) != 0 {
		t.Errorf("chunks=%d skipped=%v", len(chunks), skipped)
	}
}

func TestChunkDiffsNothingReviewable(t *testing.T) {
	chunks, skipped := ChunkDiffs([]gitlabx.FileDiff{{NewPath: "gen.go", GeneratedFile: true}}, nil, 256)
	if len(chunks) != 0 || len(skipped) != 1 {
		t.Errorf("chunks=%d skipped=%v", len(chunks), skipped)
	}
}

func paths(diffs []gitlabx.FileDiff) []string {
	out := make([]string, len(diffs))
	for i, d := range diffs {
		out[i] = d.NewPath
	}
	return out
}

func TestMergeResults(t *testing.T) {
	line := 1
	merged := MergeResults([]*Result{
		{
			Summary:  "Pass one.",
			Findings: []Finding{{ID: "f001", File: "a.go", Line: LineRef{NewLine: &line}, Severity: SeverityMajor, Category: "bug", Title: "A", Body: "x"}},
			CostUSD:  0.5,
			Warnings: []string{"w1"},
		},
		nil,
		{
			Summary:   "Pass two.",
			Findings:  []Finding{{ID: "f001", File: "b.go", Line: LineRef{NewLine: &line}, Severity: SeverityInfo, Category: "style", Title: "B", Body: "y"}},
			CostUSD:   0.25,
			SessionID: "s2",
		},
	})
	if merged.Summary != "Pass one.\n\nPass two." {
		t.Errorf("summary = %q", merged.Summary)
	}
	if len(merged.Findings) != 2 || merged.Findings[0].ID == merged.Findings[1].ID {
		t.Errorf("findings/IDs: %+v", merged.Findings)
	}
	if merged.CostUSD != 0.75 || merged.SessionID != "s2" || len(merged.Warnings) != 1 {
		t.Errorf("meta: cost=%v session=%q warnings=%v", merged.CostUSD, merged.SessionID, merged.Warnings)
	}
}

func TestBuildUserPromptShape(t *testing.T) {
	line := "@@ -1 +1 @@\n-a\n+b\n"
	req := Request{
		MR: gitlabx.MRDetail{MRSummary: gitlabx.MRSummary{
			IID: 5, Title: "Add cache", Description: "Speeds things up.",
			SourceBranch: "cache", TargetBranch: "main",
		}},
		Diffs:        []gitlabx.FileDiff{{OldPath: "c.go", NewPath: "c.go", Diff: line}},
		Commits:      []gitlabx.Commit{{ShortID: "abc1234", Title: "feat: cache", Message: "feat: cache\n\nAvoids recompute."}},
		Template:     "## What\n<!-- describe the change -->",
		Truncated:    []string{"vendor/big.go"},
		Instructions: "Focus on concurrency.",
		Categories:   []Category{"bug", "security"},
	}
	p := BuildUserPrompt(req)
	for _, want := range []string{
		"merge request !5: Add cache",
		"cache → main",
		"Speeds things up.",
		"abc1234:", "Avoids recompute.",
		"## What", "describe the change",
		"- bug:", "- security:",
		"Focus on concurrency.",
		"--- a/c.go", "+++ b/c.go",
		"vendor/big.go",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
	if strings.Contains(p, "- docs:") {
		t.Error("disabled category leaked into prompt")
	}
}

func TestParseResultDegradation(t *testing.T) {
	if _, err := ParseResult([]byte("not json")); err == nil {
		t.Error("garbage must error")
	}
	res, err := ParseResult([]byte(`{"summary":"ok","findings":[]}`))
	if err != nil || len(res.Findings) != 0 {
		t.Errorf("empty findings: %v %v", res, err)
	}
}

func TestRenderBody(t *testing.T) {
	line := 4
	f := Finding{
		File: "a.go", Line: LineRef{NewLine: &line},
		Severity: SeverityMajor, Category: "bug",
		Title: "Nil deref", Body: "Pointer may be nil.", Suggestion: "if p != nil {",
	}
	body := f.RenderBody(nil, false)
	for _, want := range []string{"**[major · bug] Nil deref**", "Pointer may be nil.", "```suggestion:-0+0\nif p != nil {\n```"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "gitlab-reviewer") {
		t.Error("attribution must be off by default")
	}
	if !strings.Contains(f.RenderBody(nil, true), "gitlab-reviewer") {
		t.Error("attribution missing when enabled")
	}

	// suggestions only make sense on new-side anchors
	old := 9
	f.Line = LineRef{OldLine: &old}
	if strings.Contains(f.RenderBody(nil, false), "suggestion:") {
		t.Error("old-line finding must not carry a suggestion block")
	}

	fb := f.RenderFallbackBody(nil, false, "https://x/-/blob/h/a.go")
	if !strings.Contains(fb, "could not anchor") || !strings.Contains(fb, "a.go:9 (old)") {
		t.Errorf("fallback body: %q", fb)
	}
}
