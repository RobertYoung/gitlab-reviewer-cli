package publisher

import (
	"context"
	"errors"
	"testing"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/config"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review"
)

// fakeService is a minimal gitlabx.Service stub covering only what
// Publisher exercises; every other method is a stub returning zero values.
type fakeService struct {
	discussions []gitlabx.Discussion
	posted      int
}

func (f *fakeService) ListOpenMergeRequests(context.Context, gitlabx.MRFilter, gitlabx.Page) ([]gitlabx.MRSummary, bool, error) {
	return nil, false, nil
}

func (f *fakeService) ListGroups(context.Context, string, gitlabx.Page) ([]gitlabx.GroupInfo, bool, error) {
	return nil, false, nil
}

func (f *fakeService) ListGroupProjects(context.Context, string, string, gitlabx.Page) ([]gitlabx.ProjectInfo, bool, error) {
	return nil, false, nil
}

func (f *fakeService) ListMemberProjects(context.Context, string, gitlabx.Page) ([]gitlabx.ProjectInfo, bool, error) {
	return nil, false, nil
}

func (f *fakeService) GetMergeRequest(context.Context, any, int64) (*gitlabx.MRDetail, error) {
	return nil, nil
}

func (f *fakeService) ListDiffs(context.Context, any, int64) ([]gitlabx.FileDiff, error) {
	return nil, nil
}

func (f *fakeService) ListCommits(context.Context, any, int64) ([]gitlabx.Commit, error) {
	return nil, nil
}

func (f *fakeService) CompareRevisions(context.Context, any, string, string) ([]gitlabx.FileDiff, error) {
	return nil, nil
}

func (f *fakeService) GetMergeRequestTemplate(context.Context, any) (string, error) { return "", nil }

func (f *fakeService) ListDirectoryFiles(context.Context, any, string, string) ([]gitlabx.RepoFile, error) {
	return nil, nil
}

func (f *fakeService) GetRawFile(context.Context, any, string, string) ([]byte, error) {
	return nil, errors.New("not found")
}

func (f *fakeService) ListDiscussions(context.Context, any, int64) ([]gitlabx.Discussion, error) {
	return f.discussions, nil
}

func (f *fakeService) CreateInlineDiscussion(context.Context, any, int64, string, *gitlabx.Position) error {
	f.posted++
	return nil
}

func (f *fakeService) CreateNote(context.Context, any, int64, string) error {
	f.posted++
	return nil
}

func (f *fakeService) CreateDraftNote(context.Context, any, int64, string, *gitlabx.Position) error {
	f.posted++
	return nil
}
func (f *fakeService) PublishAllDraftNotes(context.Context, any, int64) error { return nil }
func (f *fakeService) GetApprovals(context.Context, any, int64) (*gitlabx.Approvals, error) {
	return nil, nil
}
func (f *fakeService) Approve(context.Context, any, int64, string) error { return nil }
func (f *fakeService) Unapprove(context.Context, any, int64) error       { return nil }

func intp(n int) *int { return &n }

func TestPublishOneSkipsDuplicateOfExistingComment(t *testing.T) {
	svc := &fakeService{
		discussions: []gitlabx.Discussion{{
			Notes: []gitlabx.Note{{
				Body: "**[major · bug] possible nil deref**\n\npossible nil pointer dereference: err could be nil when this line executes",
				Position: &gitlabx.Position{
					NewPath: "a.go",
					NewLine: intp(10),
				},
			}},
		}},
	}
	detail := gitlabx.MRDetail{MRSummary: gitlabx.MRSummary{IID: 1}}
	pub, err := New(svc, detail, nil, config.Publish{MinSeverity: "info", FallbackToNote: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := pub.LoadExisting(context.Background()); err != nil {
		t.Fatalf("LoadExisting: %v", err)
	}

	f := review.Finding{
		File:     "a.go",
		Line:     review.LineRef{NewLine: intp(10)},
		Severity: review.SeverityMajor,
		Title:    "nil pointer risk",
		Body:     "possible nil pointer dereference: err may be nil when this line executes",
	}
	state, err := pub.PublishOne(context.Background(), f)
	if err != nil {
		t.Fatalf("PublishOne: %v", err)
	}
	if state != review.StatePublished {
		t.Errorf("state = %v, want StatePublished (treated as already there)", state)
	}
	if svc.posted != 0 {
		t.Errorf("posted = %d, want 0 — duplicate should not repost", svc.posted)
	}
}

func TestPublishOnePostsWhenNoMatchingExistingComment(t *testing.T) {
	svc := &fakeService{
		discussions: []gitlabx.Discussion{{
			Notes: []gitlabx.Note{{
				Body: "unrelated comment about something else entirely",
				Position: &gitlabx.Position{
					NewPath: "a.go",
					NewLine: intp(10),
				},
			}},
		}},
	}
	detail := gitlabx.MRDetail{MRSummary: gitlabx.MRSummary{IID: 1}}
	pub, err := New(svc, detail, nil, config.Publish{MinSeverity: "info", FallbackToNote: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := pub.LoadExisting(context.Background()); err != nil {
		t.Fatalf("LoadExisting: %v", err)
	}

	f := review.Finding{
		File:     "a.go",
		Line:     review.LineRef{NewLine: intp(10)},
		Severity: review.SeverityMajor,
		Title:    "nil pointer risk",
		Body:     "possible nil pointer dereference: err may be nil when this line executes",
	}
	if _, err := pub.PublishOne(context.Background(), f); err != nil {
		t.Fatalf("PublishOne: %v", err)
	}
	if svc.posted != 1 {
		t.Errorf("posted = %d, want 1 — no matching existing comment", svc.posted)
	}
}
