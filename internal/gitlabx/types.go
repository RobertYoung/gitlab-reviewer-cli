// Package gitlabx wraps the official GitLab client behind a small interface
// the TUI can depend on and tests can fake. It is the only package in the
// module that imports client-go.
package gitlabx

import (
	"fmt"
	"net/url"
	"strings"
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
	Author       string // username
	AuthorName   string // full name; empty when GitLab omits it
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

// ProjectWebURL is the project's web URL, derived from the MR's WebURL
// (…/group/app/-/merge_requests/42 → …/group/app). Empty when unknown.
func (m MRSummary) ProjectWebURL() string {
	if i := strings.Index(m.WebURL, "/-/"); i >= 0 {
		return m.WebURL[:i]
	}
	return ""
}

// AuthorDisplay formats the author for display.
func (m MRSummary) AuthorDisplay() string {
	return userDisplay(m.AuthorName, m.Author)
}

// AuthorWebURL is the author's profile page on the MR's instance, or empty
// when either part is unknown.
func (m MRSummary) AuthorWebURL() string {
	u, err := url.Parse(m.WebURL)
	if err != nil || u.Host == "" || m.Author == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host + "/" + m.Author
}

// BranchWebURL is the tree view of a branch on the MR's project, or empty
// when the project URL is unknown. Branches on a fork are not resolvable
// from the summary, so source-branch links assume same-project MRs.
func (m MRSummary) BranchWebURL(branch string) string {
	p := m.ProjectWebURL()
	if p == "" || branch == "" {
		return ""
	}
	// escape per segment: slashes in branch names stay literal in tree URLs
	segs := strings.Split(branch, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return p + "/-/tree/" + strings.Join(segs, "/")
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
// conflicts, i.e. the author should rebase before review. It trusts
// GitLab's detailed_merge_status ("need_rebase") as well as the
// diverged-commit count, because the latter is often reported as 0 unless
// merge status has been recomputed server-side.
func (m MRDetail) NeedsRebase() bool {
	return m.HasConflicts ||
		m.DivergedCommits > 0 ||
		m.DetailedMergeStatus == "need_rebase"
}

// Approvals is one MR's approval state as seen by the current user.
type Approvals struct {
	Approved          bool // every required approval rule is satisfied
	ApprovalsRequired int64
	ApprovalsLeft     int64
	ApprovedBy        []string // display names, see userDisplay
	UserHasApproved   bool
	UserCanApprove    bool
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
	ID         int64
	Author     string // username
	AuthorName string // full name; empty when GitLab omits it
	Body       string
	System     bool
	Resolved   bool
	CreatedAt  time.Time
	Position   *Position // nil for general (unpositioned) notes
}

// AuthorDisplay formats the note's author for display.
func (n Note) AuthorDisplay() string {
	return userDisplay(n.AuthorName, n.Author)
}

// userDisplay renders a user as "Full Name (@username)", degrading to
// whichever part is known when the other is missing.
func userDisplay(name, username string) string {
	switch {
	case name != "" && username != "":
		return name + " (@" + username + ")"
	case username != "":
		return "@" + username
	default:
		return name
	}
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

// RepoFile is one file fetched from a repository tree, named by its base
// file name within the listed directory.
type RepoFile struct {
	Name    string
	Content []byte
}
