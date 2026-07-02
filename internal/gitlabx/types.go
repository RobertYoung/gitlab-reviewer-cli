// Package gitlabx wraps the official GitLab client behind a small interface
// the TUI can depend on and tests can fake. It is the only package in the
// module that imports client-go.
package gitlabx

import (
	"fmt"
	"time"
)

// MRFilter narrows the MR list. Zero values mean "no filter" except State,
// which defaults to opened.
type MRFilter struct {
	State          string // opened | merged | closed | all
	AuthorUsername string
	TargetBranch   string
	Search         string

	// Projects/Groups override the configured scope for this query (used
	// by in-TUI selection). Both empty = use the configured scope.
	Projects []string
	Groups   []string
}

// GroupInfo is a group the user can browse, for in-TUI selection.
type GroupInfo struct {
	ID          int64
	FullPath    string
	Name        string
	Description string
}

// ProjectInfo is a project the user can browse, for in-TUI selection.
type ProjectInfo struct {
	ID                int64
	PathWithNamespace string
	Description       string
	LastActivity      time.Time
}

// Page is an offset pagination request applied to every configured source.
type Page struct {
	Number  int
	PerPage int
}

// MRSummary is the list-view projection of a merge request.
type MRSummary struct {
	ProjectID    int64
	ProjectPath  string // full path with namespace; may be empty if unknown
	IID          int64
	Title        string
	Description  string
	State        string
	Draft        bool
	Author       string
	SourceBranch string
	TargetBranch string
	HeadSHA      string
	WebURL       string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Project returns the best identifier for API calls: the full path when
// known, otherwise the numeric ID.
func (m MRSummary) Project() any {
	if m.ProjectPath != "" {
		return m.ProjectPath
	}
	return m.ProjectID
}

// Ref returns a human-readable reference like group/app!42.
func (m MRSummary) Ref() string {
	return fmt.Sprintf("%s!%d", m.ProjectPath, m.IID)
}

// DiffRefs are the three SHAs GitLab requires on every positioned comment.
type DiffRefs struct {
	BaseSHA  string
	HeadSHA  string
	StartSHA string
}

// MRDetail is the full view of one merge request.
type MRDetail struct {
	MRSummary
	DiffRefs DiffRefs
	// HasConflicts and DivergedCommits drive the rebase hygiene check:
	// DivergedCommits > 0 means the target branch moved ahead.
	HasConflicts        bool
	DivergedCommits     int64
	DetailedMergeStatus string
}

// NeedsRebase reports whether the source branch is behind its target or
// conflicts, i.e. the author should rebase before review.
func (m MRDetail) NeedsRebase() bool {
	return m.HasConflicts || m.DivergedCommits > 0
}

// Commit is one commit on an MR's source branch, for hygiene checks that
// compare commit messages against the diff.
type Commit struct {
	ShortID string
	Title   string
	Message string // full message (subject + body)
}

// FileDiff is one file's unified diff within an MR.
type FileDiff struct {
	OldPath       string
	NewPath       string
	Diff          string
	NewFile       bool
	RenamedFile   bool
	DeletedFile   bool
	GeneratedFile bool
	TooLarge      bool
}

// Path returns the display path: the new path, annotated when renamed.
func (f FileDiff) Path() string {
	if f.RenamedFile && f.OldPath != f.NewPath {
		return f.OldPath + " → " + f.NewPath
	}
	return f.NewPath
}

// Discussion is a thread of notes on an MR.
type Discussion struct {
	ID    string
	Notes []Note
}

// Anchor returns the diff position of the discussion's first positioned
// note, or nil for general (unanchored) threads.
func (d Discussion) Anchor() *Position {
	for _, n := range d.Notes {
		if n.Position != nil {
			return n.Position
		}
	}
	return nil
}

// Note is a single comment, optionally anchored to a diff position.
type Note struct {
	ID        int64
	Author    string
	Body      string
	System    bool
	Resolved  bool
	CreatedAt time.Time
	Position  *Position // nil for general (unpositioned) notes
}

// Position locates a comment on a diff, mapping 1:1 onto the GitLab API's
// text position type.
type Position struct {
	BaseSHA  string
	HeadSHA  string
	StartSHA string
	OldPath  string
	NewPath  string
	OldLine  *int
	NewLine  *int
}
