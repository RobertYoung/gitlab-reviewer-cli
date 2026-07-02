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
	DiffRefs     DiffRefs
	HasConflicts bool
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
