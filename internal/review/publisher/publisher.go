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
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review/dedupe"
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

	// existing holds comments already on the MR, loaded by LoadExisting, so
	// PublishOne can skip findings that substantially restate one of them.
	existing []existingComment
}

// existingComment is a comment already on the MR, reduced to what
// PublishOne needs to check a finding against it.
type existingComment struct {
	file             string
	oldLine, newLine *int
	body             string
}

// LoadExisting fetches the MR's current discussions so PublishOne can skip
// findings that substantially match a comment already posted — from this
// tool or a human — rather than posting the same finding again. Call once
// before a batch of PublishOne calls; a fetch error is advisory, so callers
// may choose to proceed without duplicate detection rather than block
// publishing on it.
func (p *Publisher) LoadExisting(ctx context.Context) error {
	discussions, err := p.svc.ListDiscussions(ctx, p.detail.Project(), p.detail.IID)
	if err != nil {
		return err
	}
	existing := make([]existingComment, 0, len(discussions))
	for _, d := range discussions {
		for _, n := range d.Notes {
			if n.System || n.Body == "" {
				continue
			}
			ec := existingComment{body: n.Body}
			if n.Position != nil {
				ec.file = n.Position.NewPath
				if ec.file == "" {
					ec.file = n.Position.OldPath
				}
				ec.oldLine = n.Position.OldLine
				ec.newLine = n.Position.NewLine
			}
			existing = append(existing, ec)
		}
	}
	p.existing = existing
	return nil
}

// duplicatesExisting reports whether f substantially restates a comment
// already on the MR: same file and (when positioned) an overlapping line,
// with similar text.
func (p *Publisher) duplicatesExisting(f review.Finding) bool {
	for _, e := range p.existing {
		if !sameCommentPosition(f, e) {
			continue
		}
		if dedupe.SimilarText(e.body, f.Title+" "+f.Body) {
			return true
		}
	}
	return false
}

func sameCommentPosition(f review.Finding, e existingComment) bool {
	if f.File != e.file {
		return false
	}
	fLineless := f.Line.NewLine == nil && f.Line.OldLine == nil
	eLineless := e.newLine == nil && e.oldLine == nil
	if fLineless || eLineless {
		return fLineless == eLineless
	}
	return intPtrEq(f.Line.NewLine, e.newLine) || intPtrEq(f.Line.OldLine, e.oldLine)
}

func intPtrEq(a, b *int) bool {
	return a != nil && b != nil && *a == *b
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
	// A finding that substantially restates a comment already on the MR
	// (this tool's own from a previous run, or a human's) is already
	// visible there; posting it again would just be noise.
	if p.duplicatesExisting(f) {
		return review.StatePublished, nil
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
