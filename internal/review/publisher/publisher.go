// Package publisher posts curated findings back to a merge request — as
// live inline discussions, as a draft review published in one action, or as
// general notes when no diff position resolves — so every frontend (TUI,
// web GUI) publishes identically.
package publisher

import (
	"context"
	"fmt"
	"strings"
	"text/template"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/config"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx/position"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review"
)

// Publisher posts findings for one MR. Draft selects the publish mode:
// draft notes collected into a pending review, or immediate discussions.
type Publisher struct {
	svc    gitlabx.Service
	detail gitlabx.MRDetail
	cfg    config.Publish
	tmpl   *template.Template
	index  []position.FileIndex

	// Draft is the publish mode for subsequent PublishOne calls; frontends
	// may let the user toggle it before posting starts.
	Draft bool
}

// New builds a publisher for one MR. A bad body template falls back to the
// built-in layout rather than blocking the publish; the returned error is
// advisory and should be surfaced, not treated as fatal.
func New(svc gitlabx.Service, detail gitlabx.MRDetail, diffs []gitlabx.FileDiff, cfg config.Publish) (*Publisher, error) {
	tmpl, err := review.ParseBodyTemplate(cfg.Template)
	if err != nil {
		err = fmt.Errorf("%w — using the built-in layout", err)
	}
	return &Publisher{
		svc:    svc,
		detail: detail,
		cfg:    cfg,
		tmpl:   tmpl,
		index:  position.Index(diffs),
		Draft:  cfg.Mode == "draft",
	}, err
}

// PublishOne posts a single finding and returns its new state: Published
// for a positioned comment or deliberate MR-level note, FellBack when the
// position did not resolve and the body went out as a general note,
// BelowThreshold when the publish floor kept it off GitLab entirely, or
// Pending (with the error) when nothing was posted.
func (p *Publisher) PublishOne(ctx context.Context, f review.Finding) (review.FindingState, error) {
	// The publish floor is enforced here, at the shared choke point, so no
	// frontend can post a below-floor finding. Manual comments are the
	// reviewer's own words and always publish.
	if !f.Manual && f.Severity.Valid() && !f.Severity.AtLeast(review.Severity(p.cfg.MinSeverity)) {
		return review.StateBelowThreshold, nil
	}
	project := p.detail.Project()
	body := f.RenderBody(p.tmpl, p.cfg.Attribution)

	post := func(body string, pos *gitlabx.Position) error {
		if p.Draft {
			return p.svc.CreateDraftNote(ctx, project, p.detail.IID, body, pos)
		}
		if pos != nil {
			return p.svc.CreateInlineDiscussion(ctx, project, p.detail.IID, body, pos)
		}
		return p.svc.CreateNote(ctx, project, p.detail.IID, body)
	}

	// A finding with no file is a deliberate MR-level comment (manual
	// comments composed by the reviewer): post it as a general note, not as
	// a failed position resolution.
	if f.File == "" {
		if err := post(body, nil); err != nil {
			return review.StatePending, err
		}
		return review.StatePublished, nil
	}

	pos, resolveErr := position.Resolve(f.File, f.Line.OldLine, f.Line.NewLine, p.index, p.detail.DiffRefs)
	if resolveErr == nil {
		if err := post(body, pos); err == nil {
			return review.StatePublished, nil
		} else if !p.cfg.FallbackToNote {
			return review.StatePending, err
		}
	} else if !p.cfg.FallbackToNote {
		return review.StatePending, resolveErr
	}

	// Fallback: unpositioned comment with a permalink to the flagged line.
	fallback := f.RenderFallbackBody(p.tmpl, p.cfg.Attribution, p.blobURL(f))
	if err := post(fallback, nil); err != nil {
		return review.StatePending, err
	}
	return review.StateFellBack, nil
}

// PublishReview publishes all pending draft notes in one action.
func (p *Publisher) PublishReview(ctx context.Context) error {
	return p.svc.PublishAllDraftNotes(ctx, p.detail.Project(), p.detail.IID)
}

// blobURL builds a permalink to the finding's line at the MR head commit.
func (p *Publisher) blobURL(f review.Finding) string {
	if p.detail.WebURL == "" || p.detail.DiffRefs.HeadSHA == "" {
		return ""
	}
	base, _, found := strings.Cut(p.detail.WebURL, "/-/")
	if !found {
		return ""
	}
	url := fmt.Sprintf("%s/-/blob/%s/%s", base, p.detail.DiffRefs.HeadSHA, f.File)
	if f.Line.NewLine != nil {
		url += fmt.Sprintf("#L%d", *f.Line.NewLine)
	}
	return url
}
