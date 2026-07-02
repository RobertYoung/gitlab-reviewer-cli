package review

import (
	"fmt"
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

	if len(req.Commits) > 0 {
		b.WriteString("\nCommits on this MR (full message per commit, separated by ---):\n")
		for _, c := range req.Commits {
			msg := strings.TrimSpace(c.Message)
			if msg == "" {
				msg = strings.TrimSpace(c.Title)
			}
			fmt.Fprintf(&b, "\n%s:\n%s\n---\n", c.ShortID, msg)
		}
	}

	if tmpl := strings.TrimSpace(req.Template); tmpl != "" {
		b.WriteString("\nThe project's default MR description template follows. Only relevant if\nyour instructions ask you to check the description against it:\n")
		b.WriteString(tmpl)
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

// ChunkDiffs filters the diffs sent to the model and splits them into
// review passes: excluded and generated files are dropped entirely, and the
// rest is packed (in original order) into chunks of at most maxKB each so
// oversized MRs become several passes instead of a truncated one. Files
// individually larger than the whole budget are skipped.
func ChunkDiffs(diffs []gitlabx.FileDiff, exclude []string, maxKB int) (chunks [][]gitlabx.FileDiff, skipped []string) {
	budget := maxKB * 1024

	var current []gitlabx.FileDiff
	spent := 0
	flush := func() {
		if len(current) > 0 {
			chunks = append(chunks, current)
			current = nil
			spent = 0
		}
	}
	for _, d := range diffs {
		if d.GeneratedFile || d.TooLarge || excluded(d.NewPath, exclude) {
			skipped = append(skipped, d.NewPath)
			continue
		}
		size := len(d.Diff)
		if size > budget {
			skipped = append(skipped, d.NewPath)
			continue
		}
		if spent+size > budget {
			flush()
		}
		current = append(current, d)
		spent += size
	}
	flush()
	return chunks, skipped
}

// MergeResults combines the results of a multi-pass review into one, with
// finding IDs reassigned to stay unique.
func MergeResults(parts []*Result) *Result {
	merged := &Result{}
	var summaries []string
	for _, p := range parts {
		if p == nil {
			continue
		}
		if s := strings.TrimSpace(p.Summary); s != "" {
			summaries = append(summaries, s)
		}
		merged.Findings = append(merged.Findings, p.Findings...)
		merged.Warnings = append(merged.Warnings, p.Warnings...)
		merged.CostUSD += p.CostUSD
		merged.SessionID = p.SessionID
	}
	merged.Summary = strings.Join(summaries, "\n\n")
	for i := range merged.Findings {
		merged.Findings[i].ID = fmt.Sprintf("f%03d", i+1)
	}
	return merged
}

func excluded(path string, globs []string) bool {
	for _, g := range globs {
		if ok, err := doublestar.Match(g, path); err == nil && ok {
			return true
		}
	}
	return false
}
