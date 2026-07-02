package gitlabx

import "context"

// Service is what the rest of the application sees of GitLab. Write
// operations (discussions, draft notes) join the interface with the review
// publishing milestone.
type Service interface {
	// ListOpenMergeRequests returns one page of MRs merged across the
	// scope (filter override or the configured projects and groups),
	// newest-updated first, and whether any source has more pages.
	ListOpenMergeRequests(ctx context.Context, filter MRFilter, page Page) ([]MRSummary, bool, error)

	// ListGroups returns groups the user has access to, for in-TUI scope
	// selection.
	ListGroups(ctx context.Context, search string, page Page) ([]GroupInfo, bool, error)

	// ListGroupProjects returns a group's projects (including subgroups).
	ListGroupProjects(ctx context.Context, group string, search string, page Page) ([]ProjectInfo, bool, error)

	// ListMemberProjects returns projects the user is a member of,
	// covering personal and directly-shared projects outside any group.
	ListMemberProjects(ctx context.Context, search string, page Page) ([]ProjectInfo, bool, error)

	// GetMergeRequest fetches full MR details including diff refs.
	// project is a full path (group/app) or numeric ID.
	GetMergeRequest(ctx context.Context, project any, iid int64) (*MRDetail, error)

	// ListDiffs returns every file diff of the MR (paginating internally).
	ListDiffs(ctx context.Context, project any, iid int64) ([]FileDiff, error)

	// ListCommits returns every commit on the MR's source branch
	// (paginating internally), for commit-message hygiene checks.
	ListCommits(ctx context.Context, project any, iid int64) ([]Commit, error)

	// GetMergeRequestTemplate resolves the project's default MR description
	// template (including group-inherited ones), or "" if none is set.
	GetMergeRequestTemplate(ctx context.Context, project any) (string, error)

	// ListDiscussions returns every discussion thread on the MR
	// (paginating internally).
	ListDiscussions(ctx context.Context, project any, iid int64) ([]Discussion, error)

	// CreateInlineDiscussion posts a positioned comment on the MR diff.
	CreateInlineDiscussion(ctx context.Context, project any, iid int64, body string, pos *Position) error

	// CreateNote posts a general (unpositioned) comment on the MR — the
	// fallback when no diff position can be resolved.
	CreateNote(ctx context.Context, project any, iid int64, body string) error

	// CreateDraftNote adds a comment to the user's pending review; pos may
	// be nil for a general draft note. Nothing is visible to others until
	// PublishAllDraftNotes.
	CreateDraftNote(ctx context.Context, project any, iid int64, body string, pos *Position) error

	// PublishAllDraftNotes publishes the pending review in one action.
	PublishAllDraftNotes(ctx context.Context, project any, iid int64) error

	// GetApprovals returns the MR's approval state as seen by the
	// current user.
	GetApprovals(ctx context.Context, project any, iid int64) (*Approvals, error)

	// Approve approves the MR. A non-empty sha must match the MR's HEAD,
	// guarding against approving code pushed after it was reviewed.
	Approve(ctx context.Context, project any, iid int64, sha string) error

	// Unapprove removes the current user's approval from the MR.
	Unapprove(ctx context.Context, project any, iid int64) error
}
