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

// FullSystemPrompt is the system prompt for one agent's pass: the shared
// reviewer contract above, then the agent's own persona/focus text.
func FullSystemPrompt(req Request) string {
	if strings.TrimSpace(req.AgentPrompt) == "" {
		return SystemPrompt
	}
	return SystemPrompt + "\n\n" + strings.TrimSpace(req.AgentPrompt)
}

// BuildUserPrompt renders the review request: MR metadata, agent scope,
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

	if req.AgentName != "" {
		fmt.Fprintf(&b, "\nYou are running as the %q review agent.", req.AgentName)
	}
	if len(req.Categories) > 0 {
		cats := make([]string, len(req.Categories))
		for i, c := range req.Categories {
			cats[i] = string(c)
		}
		fmt.Fprintf(&b, "\nLabel findings only with these categories: %s\n", strings.Join(cats, ", "))
	}

	if inst := strings.TrimSpace(req.Instructions); inst != "" {
		b.WriteString("\nAdditional review instructions from the team:\n")
		b.WriteString(inst)
		b.WriteString("\n")
	}

	if req.Incremental {
		sha := req.LastReviewedSHA
		if len(sha) > 8 {
			sha = sha[:8]
		}
		fmt.Fprintf(&b, "\nThis is an incremental re-review: an earlier review already covered this\nMR up to commit %s and its findings were carried forward. The diff below\nshows only the changes pushed since that commit; report findings only on\nthese changes. The checkout is at the new head, so the full context is on\ndisk as usual.\n", sha)
	}

	if len(req.Diffs) > 0 {
		b.WriteString("\nThe diff under review follows. Each hunk header shows old and new line\nnumbers; report line numbers consistent with these headers.\n")
		for _, d := range req.Diffs {
			fmt.Fprintf(&b, "\n--- a/%s\n+++ b/%s\n", d.OldPath, d.NewPath)
			b.WriteString(strings.TrimSuffix(d.Diff, "\n"))
			b.WriteString("\n")
		}
	}

	if len(req.DiffFiles) > 0 {
		b.WriteString("\nThe diffs for the following changed files were too large to include inline.\nEach full diff has been written to a file inside the checkout; Read the diff\nfile and review these changes with the same rules as the inline diff:\n")
		for _, f := range req.DiffFiles {
			fmt.Fprintf(&b, "- %s: diff at %s\n", f.Path, f.DiffPath)
		}
	}

	if len(req.Unavailable) > 0 {
		b.WriteString("\nThe following files also changed in this MR, but GitLab could not provide\ntheir diffs (too large). The checkout is at the MR head commit; Read them\ndirectly if relevant:\n")
		for _, f := range req.Unavailable {
			fmt.Fprintf(&b, "- %s\n", f)
		}
	}

	if len(req.Excluded) > 0 {
		b.WriteString("\nThe following changed files are excluded from review by configuration\n(lockfiles, vendored or generated code). Ignore them unless other changes\ndepend on them:\n")
		for _, f := range req.Excluded {
			fmt.Fprintf(&b, "- %s\n", f)
		}
	}
	return b.String()
}

// SkipReason says why a changed file was left out of the inline diff.
type SkipReason int

const (
	// SkipExcluded: matched a review.exclude glob or GitLab marked the file
	// as generated. Deliberate filtering, not information loss.
	SkipExcluded SkipReason = iota
	// SkipOverBudget: the file's diff alone exceeds the whole max_diff_kb
	// budget. The diff content is available and can be provided on disk.
	SkipOverBudget
	// SkipUnavailable: GitLab returned no diff content (too_large); only the
	// head state of the file is visible to the reviewer.
	SkipUnavailable
)

// SkippedDiff is one changed file left out of the inline diff.
type SkippedDiff struct {
	Path    string
	OldPath string
	Reason  SkipReason
	Diff    string // populated for SkipOverBudget so the diff can go on disk
}

// ChunkDiffs filters the diffs sent to the model and splits them into
// review passes: excluded and generated files are dropped, and the rest is
// packed (in original order) into chunks of at most maxKB each so oversized
// MRs become several passes instead of a truncated one. Files individually
// larger than the whole budget are returned as SkipOverBudget with their
// diff content, so the caller can supply them out of band.
func ChunkDiffs(diffs []gitlabx.FileDiff, exclude []string, maxKB int) (chunks [][]gitlabx.FileDiff, skipped []SkippedDiff) {
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
		switch {
		case d.GeneratedFile || excluded(d.NewPath, exclude):
			skipped = append(skipped, SkippedDiff{Path: d.NewPath, OldPath: d.OldPath, Reason: SkipExcluded})
			continue
		case d.TooLarge:
			skipped = append(skipped, SkippedDiff{Path: d.NewPath, OldPath: d.OldPath, Reason: SkipUnavailable})
			continue
		case len(d.Diff) > budget:
			skipped = append(skipped, SkippedDiff{Path: d.NewPath, OldPath: d.OldPath, Reason: SkipOverBudget, Diff: d.Diff})
			continue
		}
		size := len(d.Diff)
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
