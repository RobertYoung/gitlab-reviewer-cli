package gitlabx

import "context"

// Service is what the rest of the application sees of GitLab. Write
// operations (discussions, draft notes) join the interface with the review
// publishing milestone.
type Service interface {
	// ListOpenMergeRequests returns one page of MRs merged across all
	// configured projects and groups, newest-updated first, and whether
	// any source has more pages.
	ListOpenMergeRequests(ctx context.Context, filter MRFilter, page Page) ([]MRSummary, bool, error)

	// GetMergeRequest fetches full MR details including diff refs.
	// project is a full path (group/app) or numeric ID.
	GetMergeRequest(ctx context.Context, project any, iid int64) (*MRDetail, error)

	// ListDiffs returns every file diff of the MR (paginating internally).
	ListDiffs(ctx context.Context, project any, iid int64) ([]FileDiff, error)

	// ListDiscussions returns every discussion thread on the MR
	// (paginating internally).
	ListDiscussions(ctx context.Context, project any, iid int64) ([]Discussion, error)

	// CreateInlineDiscussion posts a positioned comment on the MR diff.
	CreateInlineDiscussion(ctx context.Context, project any, iid int64, body string, pos *Position) error

	// CreateNote posts a general (unpositioned) comment on the MR — the
	// fallback when no diff position can be resolved.
	CreateNote(ctx context.Context, project any, iid int64, body string) error
}
