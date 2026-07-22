// Package dedupe collapses review findings that describe the same
// underlying issue: near-duplicates reported by more than one agent in a
// single run, and findings that substantially match a comment already
// posted to the MR. Matching is heuristic (same file/line plus token-overlap
// text similarity) rather than an LLM pass — cheap, deterministic, and good
// enough to catch reworded restatements of the same finding.
package dedupe

import (
	"strings"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review"
)

// textThreshold is the token-overlap ratio (Jaccard over lower-cased words)
// above which two texts are considered the same underlying issue.
const textThreshold = 0.6

// SimilarText reports whether a and b are similar enough to be considered
// duplicates.
func SimilarText(a, b string) bool {
	ta, tb := tokenSet(a), tokenSet(b)
	if len(ta) == 0 || len(tb) == 0 {
		return false
	}
	inter := 0
	for t := range ta {
		if tb[t] {
			inter++
		}
	}
	union := len(ta) + len(tb) - inter
	if union == 0 {
		return false
	}
	return float64(inter)/float64(union) >= textThreshold
}

func tokenSet(s string) map[string]bool {
	set := map[string]bool{}
	for f := range strings.FieldsSeq(strings.ToLower(s)) {
		f = strings.Trim(f, ".,;:!?()`'\"*_[]{}~·")
		if f != "" {
			set[f] = true
		}
	}
	return set
}

var severityRank = map[review.Severity]int{
	review.SeverityInfo:     0,
	review.SeverityMinor:    1,
	review.SeverityMajor:    2,
	review.SeverityCritical: 3,
}

// Findings drops near-duplicate findings reported by more than one agent in
// the same run: same file and line, with substantially similar title/body
// text. Within a duplicate cluster, a finding already carried through
// curation (state other than pending, e.g. carried forward from a previous
// incremental review) is kept over a fresh pending one so re-running agents
// can never silently discard curation work; ties prefer the higher severity,
// then the earliest occurrence.
func Findings(findings []review.Finding) (kept, dropped []review.Finding) {
	used := make([]bool, len(findings))
	for i := range findings {
		if used[i] {
			continue
		}
		used[i] = true
		cluster := []int{i}
		for j := i + 1; j < len(findings); j++ {
			if !used[j] && isDuplicate(findings[i], findings[j]) {
				used[j] = true
				cluster = append(cluster, j)
			}
		}
		best := cluster[0]
		for _, idx := range cluster[1:] {
			if isBetter(findings[idx], findings[best]) {
				best = idx
			}
		}
		kept = append(kept, findings[best])
		for _, idx := range cluster {
			if idx != best {
				dropped = append(dropped, findings[idx])
			}
		}
	}
	return kept, dropped
}

// isDuplicate reports whether a and b are two agents' reports of the same
// issue. File-level findings (no file, e.g. MR-level manual comments) never
// auto-dedupe against each other — they carry no anchor to compare.
func isDuplicate(a, b review.Finding) bool {
	if a.File == "" || b.File == "" || a.File != b.File {
		return false
	}
	if !samePosition(a.Line, b.Line) {
		return false
	}
	return SimilarText(a.Title+" "+a.Body, b.Title+" "+b.Body)
}

func samePosition(a, b review.LineRef) bool {
	if a.NewLine == nil && a.OldLine == nil && b.NewLine == nil && b.OldLine == nil {
		return true // both file-level findings on the same file
	}
	return intPtrEq(a.NewLine, b.NewLine) || intPtrEq(a.OldLine, b.OldLine)
}

func intPtrEq(a, b *int) bool {
	return a != nil && b != nil && *a == *b
}

func isBetter(a, b review.Finding) bool {
	ac, bc := a.State != review.StatePending, b.State != review.StatePending
	if ac != bc {
		return ac
	}
	if severityRank[a.Severity] != severityRank[b.Severity] {
		return severityRank[a.Severity] > severityRank[b.Severity]
	}
	return false
}
