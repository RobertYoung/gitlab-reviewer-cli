package review

import (
	"fmt"
	"slices"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx"
)

// SystemPrompt is appended to the backend's system prompt: the reviewer
// persona and the line-reporting contract the position resolver depends on.
const SystemPrompt = `You are an expert code reviewer for GitLab merge requests. You are running
inside a checkout of the repository at the merge request's head commit: use
your Read/Grep/Glob tools to explore surrounding code, callers, and tests
before flagging anything, so your findings reflect the whole codebase and
not just the diff.

Reporting rules:
- Only report findings you are confident about. Prefer a few high-value
  findings over many speculative ones. Do not pad: an empty findings list is
  a valid review.
- Anchor every finding on a line that appears in the diff (a changed line
  where possible). Use repository-relative file paths exactly as they appear
  in the diff headers.
- For added or unchanged lines set "new_line" to the line number in the new
  file. For removed lines set "old_line" to the line number in the old file
  and leave "new_line" null.
- Only fill "suggestion" when you can propose an exact drop-in replacement
  for the single flagged line; it must be complete and correctly indented.
- Write bodies in GitLab-flavoured markdown, concise and specific. Explain
  why it matters, not just what to change.

Severity rubric:
- critical: will break production, lose data, or is an exploitable
  vulnerability
- major: a real bug or security/performance problem that should block merge
- minor: worth fixing but not blocking (edge cases, unclear code, missing
  tests)
- info: observations and polish (naming, docs, style)`

var categoryGuidance = map[Category]string{
	"bug":         "logic errors, race conditions, unhandled failure paths, off-by-one errors, broken edge cases",
	"security":    "injection, authn/authz gaps, secrets in code, unsafe deserialisation, SSRF, path traversal",
	"performance": "algorithmic complexity, N+1 queries, unbounded memory, missing caching where it clearly matters",
	"docs":        "missing or stale documentation and comments for non-obvious public behaviour",
	"style":       "readability, naming, idiomatic usage, dead code — only where it genuinely hurts maintainability",
	"design":      "API shape, layering violations, error-handling strategy, extensibility problems",
}

// BuildUserPrompt renders the review request: MR metadata, category scope,
// custom instructions, then the bounded diff with annotated line numbers.
func BuildUserPrompt(req Request) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Review merge request !%d: %s\n", req.MR.IID, req.MR.Title)
	fmt.Fprintf(&b, "Branches: %s → %s\n", req.MR.SourceBranch, req.MR.TargetBranch)
	if desc := strings.TrimSpace(req.MR.Description); desc != "" {
		b.WriteString("\nMR description:\n")
		b.WriteString(desc)
		b.WriteString("\n")
	}

	b.WriteString("\nReport findings only in these categories:\n")
	for _, c := range req.Categories {
		fmt.Fprintf(&b, "- %s: %s\n", c, categoryGuidance[c])
	}

	if inst := strings.TrimSpace(req.Instructions); inst != "" {
		b.WriteString("\nAdditional review instructions from the team:\n")
		b.WriteString(inst)
		b.WriteString("\n")
	}

	b.WriteString("\nThe diff under review follows. Each hunk header shows old and new line\nnumbers; report line numbers consistent with these headers.\n")
	for _, d := range req.Diffs {
		fmt.Fprintf(&b, "\n--- a/%s\n+++ b/%s\n", d.OldPath, d.NewPath)
		b.WriteString(strings.TrimSuffix(d.Diff, "\n"))
		b.WriteString("\n")
	}

	if len(req.Truncated) > 0 {
		b.WriteString("\nThe following changed files were NOT included in the diff above (excluded or over the size budget). Read them directly if relevant:\n")
		for _, f := range req.Truncated {
			fmt.Fprintf(&b, "- %s\n", f)
		}
	}
	return b.String()
}

// BoundDiffs filters and budgets the diffs sent to the model: excluded and
// generated files are dropped, then files are admitted smallest-first until
// maxKB is spent. Returns the admitted diffs (original order) and the paths
// left out.
func BoundDiffs(diffs []gitlabx.FileDiff, exclude []string, maxKB int) (kept []gitlabx.FileDiff, truncated []string) {
	budget := maxKB * 1024

	type cand struct {
		idx  int
		size int
	}
	var candidates []cand
	for i, d := range diffs {
		if d.GeneratedFile || d.TooLarge || excluded(d.NewPath, exclude) {
			truncated = append(truncated, d.NewPath)
			continue
		}
		candidates = append(candidates, cand{idx: i, size: len(d.Diff)})
	}

	// Admit smallest-first so one huge file cannot starve the rest.
	admitted := make(map[int]bool)
	slices.SortStableFunc(candidates, func(a, b cand) int { return a.size - b.size })
	spent := 0
	for _, c := range candidates {
		if spent+c.size > budget && spent > 0 {
			truncated = append(truncated, diffs[c.idx].NewPath)
			continue
		}
		if c.size > budget {
			truncated = append(truncated, diffs[c.idx].NewPath)
			continue
		}
		admitted[c.idx] = true
		spent += c.size
	}

	for i, d := range diffs {
		if admitted[i] {
			kept = append(kept, d)
		}
	}
	return kept, truncated
}

func excluded(path string, globs []string) bool {
	for _, g := range globs {
		if ok, err := doublestar.Match(g, path); err == nil && ok {
			return true
		}
	}
	return false
}
