// Package review defines the Reviewer abstraction: what a review needs,
// what it produces, and how findings are modelled. Backends (the claude CLI
// today, SDKs later) live in subpackages.
package review

import (
	"context"
	"slices"
	"time"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx"
)

// Severity of a finding, weakest to strongest.
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityMinor    Severity = "minor"
	SeverityMajor    Severity = "major"
	SeverityCritical Severity = "critical"
)

var severityRank = map[Severity]int{
	SeverityInfo: 0, SeverityMinor: 1, SeverityMajor: 2, SeverityCritical: 3,
}

// Valid reports whether s is a known severity.
func (s Severity) Valid() bool { _, ok := severityRank[s]; return ok }

// AtLeast reports whether s is min or stronger.
func (s Severity) AtLeast(min Severity) bool { return severityRank[s] >= severityRank[min] }

// Category of a finding.
type Category string

// Categories the reviewer knows how to look for.
var AllCategories = []Category{"bug", "security", "performance", "docs", "style", "design"}

// Valid reports whether c is a known category.
func (c Category) Valid() bool { return slices.Contains(AllCategories, c) }

// FindingState tracks a finding through the curation flow.
type FindingState int

const (
	StatePending FindingState = iota
	StateAccepted
	StateRejected
	StatePublished
	StateFellBack // published, but as a general note because no position resolved
)

func (s FindingState) String() string {
	switch s {
	case StateAccepted:
		return "accepted"
	case StateRejected:
		return "rejected"
	case StatePublished:
		return "published"
	case StateFellBack:
		return "note"
	default:
		return "pending"
	}
}

// LineRef locates a finding in a diff: new-side line for added/context
// lines, old-side line for removed lines. Nil means not applicable.
type LineRef struct {
	OldLine *int
	NewLine *int
}

// Finding is one suggested review comment.
type Finding struct {
	ID         string
	File       string // new path, repo-relative
	OldFile    string // as reported by the model for renames; advisory only
	Line       LineRef
	Severity   Severity
	Category   Category
	Title      string
	Body       string // markdown, user-editable
	Suggestion string // optional replacement for the flagged line
	State      FindingState
}

// Request is everything a backend needs to run one review.
type Request struct {
	// RepoPath is the checkout the review runs in (the subprocess cwd).
	RepoPath string
	// MR carries metadata shown to the model (title, description, branches).
	MR gitlabx.MRDetail
	// Diffs is the bounded, pre-filtered set of file diffs to review.
	Diffs []gitlabx.FileDiff
	// Commits are the MR's commits, shown to the model as context (and for
	// commit-message hygiene checks driven via Instructions).
	Commits []gitlabx.Commit
	// Template is the project's default MR description template, shown to the
	// model so instructions can drive a description-vs-template hygiene
	// check. Empty when the project has no template.
	Template string
	// Truncated lists files that were excluded or cut by the diff budget.
	Truncated []string
	// Instructions is extra prompt text: global then per-project.
	Instructions string
	// Categories to report on.
	Categories []Category

	Model        string
	Timeout      time.Duration
	MaxBudgetUSD float64
}

// EventKind classifies progress events streamed during a review.
type EventKind int

const (
	EventInit EventKind = iota
	EventStatus
	EventToolUse
	EventText
	EventRetry
)

// Event is one progress update for the TUI's review log.
type Event struct {
	Kind EventKind
	Text string
}

// Result is a completed review.
type Result struct {
	Summary   string
	Findings  []Finding
	Warnings  []string // dropped findings, truncation notes
	SessionID string
	CostUSD   float64
	Raw       []byte // raw output for drift debugging; persisted by the caller
}

// Reviewer runs reviews. Implementations must be safe to reuse serially;
// onEvent is called from the reviewing goroutine.
type Reviewer interface {
	Name() string
	// CheckAvailable verifies the backend can run (binary present, version
	// supported) and returns a user-actionable error otherwise.
	CheckAvailable(ctx context.Context) error
	Review(ctx context.Context, req Request, onEvent func(Event)) (*Result, error)
}
