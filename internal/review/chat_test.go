package review

import (
	"strings"
	"testing"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx"
)

func chatMR() gitlabx.MRDetail {
	return gitlabx.MRDetail{MRSummary: gitlabx.MRSummary{
		IID: 42, Title: "Add cache", SourceBranch: "cache", TargetBranch: "main",
		Description: "Speeds up hot paths.",
	}}
}

func TestBuildChatPromptWholeMR(t *testing.T) {
	req := ChatRequest{
		MR: chatMR(),
		Diffs: []gitlabx.FileDiff{
			{OldPath: "a.go", NewPath: "a.go", Diff: "@@ -1 +1 @@\n-x\n+y\n"},
			{OldPath: "big.go", NewPath: "big.go", Diff: strings.Repeat("+pad\n", 2000)},
			{OldPath: "huge.go", NewPath: "huge.go", TooLarge: true},
		},
		MaxDiffKB: 1, // 1 KiB: a.go fits, big.go does not
		Message:   "What is the caching strategy here?",
	}
	p := BuildChatPrompt(req)

	for _, want := range []string{
		"merge request !42: Add cache",
		"Branches: cache → main",
		"Speeds up hot paths.",
		"+++ b/a.go",
		"-x\n+y",
		"- big.go",
		"- huge.go",
		"What is the caching strategy here?",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, "+pad") {
		t.Error("over-budget diff was inlined")
	}
}

func TestBuildChatPromptFocus(t *testing.T) {
	line := 7
	req := ChatRequest{
		MR: chatMR(),
		Diffs: []gitlabx.FileDiff{
			{OldPath: "a.go", NewPath: "a.go", Diff: "@@ -1 +1 @@\n-x\n+y\n"},
			{OldPath: "other.go", NewPath: "other.go", Diff: "@@ -1 +1 @@\n-o\n+p\n"},
		},
		Focus:   &ChatFocus{File: "a.go", Line: LineRef{NewLine: &line}},
		Message: "Why was this changed?",
	}
	p := BuildChatPrompt(req)

	if !strings.Contains(p, "+++ b/a.go") {
		t.Errorf("focused file diff missing:\n%s", p)
	}
	if strings.Contains(p, "other.go") {
		t.Error("unfocused file should not be inlined on a line chat")
	}
	if !strings.Contains(p, "about line 7 of a.go") {
		t.Errorf("focus line missing:\n%s", p)
	}

	old := 3
	req.Focus = &ChatFocus{File: "a.go", Line: LineRef{OldLine: &old}}
	if p := BuildChatPrompt(req); !strings.Contains(p, "line 3 of the old version of a.go") {
		t.Errorf("old-side focus missing:\n%s", p)
	}
}

func TestChatFocusLabel(t *testing.T) {
	n, o := 12, 4
	cases := []struct {
		focus ChatFocus
		want  string
	}{
		{ChatFocus{File: "a.go", Line: LineRef{NewLine: &n}}, "a.go:12"},
		{ChatFocus{File: "a.go", Line: LineRef{OldLine: &o}}, "a.go:4(old)"},
		{ChatFocus{File: "a.go"}, "a.go"},
	}
	for _, c := range cases {
		if got := c.focus.Label(); got != c.want {
			t.Errorf("Label() = %q, want %q", got, c.want)
		}
	}
}
