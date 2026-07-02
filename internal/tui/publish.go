package tui

import (
	"context"
	"fmt"
	"strings"
	"text/template"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/config"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx/position"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review"
)

type publishProgressMsg struct {
	iid   int64
	index int
	state review.FindingState
	err   error
}

type publishDoneMsg struct {
	iid int64
	err error // draft-mode PublishAll failure
}

type publishPhase int

const (
	phaseConfirm publishPhase = iota
	phasePosting
	phaseDraftReady // draft mode: notes created, awaiting "publish review"
	phaseDone
)

// publishOpts control how the publish screen behaves.
type publishOpts struct {
	// auto skips the confirmation phase (publish.auto_comment).
	auto bool
	// popCount is how many screens to pop when leaving.
	popCount int
	// report receives finding state changes so the findings screen stays
	// in sync; called on the UI goroutine.
	report func(id string, state review.FindingState)
}

// publish posts the accepted findings to GitLab: immediately as live
// discussions, or as a draft review published in one action.
type publish struct {
	deps   Deps
	detail gitlabx.MRDetail
	items  []review.Finding
	cfg    config.Config
	opts   publishOpts
	mode   string // draft | immediate, per-run overridable
	tmpl   *template.Template
	index  []position.FileIndex

	ch             chan tea.Msg
	spin           spinner.Model
	phase          publishPhase
	current        int
	errs           []string
	draftPublished bool
	keptAsDrafts   bool
	width          int
	height         int
}

func newPublish(deps Deps, detail gitlabx.MRDetail, diffs []gitlabx.FileDiff, accepted []review.Finding, opts publishOpts) *publish {
	if opts.popCount == 0 {
		opts.popCount = 2
	}
	cfg := deps.cfgFor(detail.ProjectPath)
	s := &publish{
		deps:   deps,
		detail: detail,
		items:  accepted,
		cfg:    cfg,
		opts:   opts,
		mode:   cfg.Publish.Mode,
		index:  position.Index(diffs),
		spin:   spinner.New(spinner.WithSpinner(spinner.MiniDot)),
	}
	// A bad per-project template falls back to the built-in layout rather
	// than blocking the publish; the error is surfaced on screen.
	tmpl, err := review.ParseBodyTemplate(cfg.Publish.Template)
	if err != nil {
		s.errs = append(s.errs, err.Error()+" — using the built-in layout")
	}
	s.tmpl = tmpl
	return s
}

func (s *publish) Title() string {
	return fmt.Sprintf("publish · %s · %s mode", s.detail.Ref(), s.mode)
}

func (s *publish) Hints() string {
	switch s.phase {
	case phaseConfirm:
		return "enter publish · m toggle mode · esc back"
	case phaseDraftReady:
		return "P publish review · esc keep as drafts"
	case phaseDone:
		return "enter/esc done"
	default:
		return "posting…"
	}
}

func (s *publish) Init() tea.Cmd {
	s.ch = make(chan tea.Msg, 16)
	if s.opts.auto {
		return tea.Batch(s.spin.Tick, s.start())
	}
	return s.spin.Tick
}

func (s *publish) start() tea.Cmd {
	s.phase = phasePosting
	go s.run()
	return s.wait()
}

func (s *publish) wait() tea.Cmd {
	return func() tea.Msg { return <-s.ch }
}

// run posts each accepted finding in order. Sequential on purpose: GitLab
// rate limits are unkind to bursts, and progress is clearer.
func (s *publish) run() {
	iid := s.detail.IID
	for i, f := range s.items {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		state, err := s.publishOne(ctx, f)
		cancel()
		s.ch <- publishProgressMsg{iid: iid, index: i, state: state, err: err}
	}
	s.ch <- publishDoneMsg{iid: iid}
}

func (s *publish) publishOne(ctx context.Context, f review.Finding) (review.FindingState, error) {
	project := s.detail.Project()
	body := f.RenderBody(s.tmpl, s.cfg.Publish.Attribution)
	draft := s.mode == "draft"

	post := func(body string, pos *gitlabx.Position) error {
		if draft {
			return s.deps.Svc.CreateDraftNote(ctx, project, s.detail.IID, body, pos)
		}
		if pos != nil {
			return s.deps.Svc.CreateInlineDiscussion(ctx, project, s.detail.IID, body, pos)
		}
		return s.deps.Svc.CreateNote(ctx, project, s.detail.IID, body)
	}

	pos, resolveErr := position.Resolve(f.File, f.Line.OldLine, f.Line.NewLine, s.index, s.detail.DiffRefs)
	if resolveErr == nil {
		if err := post(body, pos); err == nil {
			return review.StatePublished, nil
		} else if !s.cfg.Publish.FallbackToNote {
			return review.StatePending, err
		}
	} else if !s.cfg.Publish.FallbackToNote {
		return review.StatePending, resolveErr
	}

	// Fallback: unpositioned comment with a permalink to the flagged line.
	fallback := f.RenderFallbackBody(s.tmpl, s.cfg.Publish.Attribution, s.blobURL(f))
	if err := post(fallback, nil); err != nil {
		return review.StatePending, err
	}
	return review.StateFellBack, nil
}

// publishReview publishes all pending draft notes in one action.
func (s *publish) publishReview() tea.Cmd {
	s.phase = phasePosting
	iid := s.detail.IID
	project := s.detail.Project()
	svc := s.deps.Svc
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		s.ch <- publishDoneMsg{iid: iid, err: svc.PublishAllDraftNotes(ctx, project, iid)}
	}()
	return s.wait()
}

// blobURL builds a permalink to the finding's line at the MR head commit.
func (s *publish) blobURL(f review.Finding) string {
	if s.detail.WebURL == "" || s.detail.DiffRefs.HeadSHA == "" {
		return ""
	}
	base, _, found := strings.Cut(s.detail.WebURL, "/-/")
	if !found {
		return ""
	}
	url := fmt.Sprintf("%s/-/blob/%s/%s", base, s.detail.DiffRefs.HeadSHA, f.File)
	if f.Line.NewLine != nil {
		url += fmt.Sprintf("#L%d", *f.Line.NewLine)
	}
	return url
}

func (s *publish) Update(msg tea.Msg) (Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		s.width, s.height = msg.Width, msg.Height
		return s, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		s.spin, cmd = s.spin.Update(msg)
		return s, cmd

	case publishProgressMsg:
		if msg.iid != s.detail.IID {
			return s, nil
		}
		s.items[msg.index].State = msg.state
		s.current = msg.index + 1
		if msg.err != nil {
			s.errs = append(s.errs, fmt.Sprintf("%s: %v", s.items[msg.index].Title, msg.err))
		}
		if s.opts.report != nil {
			s.opts.report(s.items[msg.index].ID, msg.state)
		}
		return s, s.wait()

	case publishDoneMsg:
		if msg.iid != s.detail.IID {
			return s, nil
		}
		if msg.err != nil {
			s.errs = append(s.errs, "publishing draft review: "+msg.err.Error())
			s.phase = phaseDone
			return s, nil
		}
		switch {
		case s.phase == phasePosting && s.mode == "draft" && s.postedCount() > 0 && !s.reviewPublished():
			s.phase = phaseDraftReady
		default:
			s.phase = phaseDone
		}
		return s, nil

	case tea.KeyPressMsg:
		return s.updateKeys(msg)
	}
	return s, nil
}

// reviewPublished reports whether PublishAllDraftNotes already ran: the
// second publishDoneMsg in draft mode.
func (s *publish) reviewPublished() bool { return s.draftPublished }

func (s *publish) postedCount() int {
	n := 0
	for _, f := range s.items {
		if f.State == review.StatePublished || f.State == review.StateFellBack {
			n++
		}
	}
	return n
}

func (s *publish) updateKeys(msg tea.KeyPressMsg) (Screen, tea.Cmd) {
	switch s.phase {
	case phaseConfirm:
		switch msg.String() {
		case "enter":
			return s, s.start()
		case "m":
			if s.mode == "draft" {
				s.mode = "immediate"
			} else {
				s.mode = "draft"
			}
		case "esc":
			return s, popScreen
		}
	case phaseDraftReady:
		switch msg.String() {
		case "P":
			s.draftPublished = true
			return s, s.publishReview()
		case "esc":
			// Leave the notes as a pending review; GitLab keeps them.
			s.phase = phaseDone
			s.keptAsDrafts = true
		}
	case phaseDone:
		switch msg.String() {
		case "enter", "esc":
			return s, popScreens(s.opts.popCount, nil)
		case "q":
			return s, tea.Quit
		}
	}
	return s, nil
}

func (s *publish) View() string {
	var b strings.Builder
	switch s.phase {
	case phaseConfirm:
		fmt.Fprintf(&b, "%s\n\n", headerStyle.Render(fmt.Sprintf("publish %d comment(s) to %s", len(s.items), s.detail.Ref())))
		if s.mode == "draft" {
			b.WriteString("mode: " + draftStyle.Render("draft review") + subtleStyle.Render("  — comments stay invisible until you publish the review in one action") + "\n\n")
		} else {
			b.WriteString("mode: " + addedStyle.Render("immediate") + subtleStyle.Render("  — each comment appears on the MR as it is posted") + "\n\n")
		}
		s.renderItems(&b)
		for _, e := range s.errs {
			b.WriteString("\n" + errorStyle.Render(truncate(e, max(s.width-2, 20))))
		}
		return b.String()

	case phasePosting:
		fmt.Fprintf(&b, "%s publishing %d/%d…\n\n", s.spin.View(), s.current, len(s.items))
		s.renderItems(&b)
		return b.String()

	case phaseDraftReady:
		fmt.Fprintf(&b, "%s\n\n", headerStyle.Render("draft review ready"))
		s.renderItems(&b)
		fmt.Fprintf(&b, "\n%d draft note(s) created. Press %s to publish the review, or esc to keep them as pending drafts in GitLab.\n",
			s.postedCount(), headerStyle.Render("P"))
		return b.String()

	default: // done
		fmt.Fprintf(&b, "%s\n\n", headerStyle.Render("publish complete"))
		s.renderItems(&b)
		published, fellBack := 0, 0
		for _, f := range s.items {
			switch f.State {
			case review.StatePublished:
				published++
			case review.StateFellBack:
				fellBack++
			}
		}
		fmt.Fprintf(&b, "\n%d inline · %d as notes · %d failed\n", published, fellBack, len(s.errs))
		if s.keptAsDrafts {
			b.WriteString(draftStyle.Render("left as a pending review — publish it from the GitLab UI") + "\n")
		}
		for _, e := range s.errs {
			b.WriteString(errorStyle.Render(truncate("  "+e, max(s.width-2, 20))) + "\n")
		}
		b.WriteString("\n" + subtleStyle.Render("enter to go back"))
		return b.String()
	}
}

func (s *publish) renderItems(b *strings.Builder) {
	for _, f := range s.items {
		var badge string
		switch f.State {
		case review.StatePublished:
			if s.mode == "draft" && s.phase != phaseDone {
				badge = draftStyle.Render("✓ drafted")
			} else {
				badge = addedStyle.Render("✓ inline")
			}
		case review.StateFellBack:
			badge = draftStyle.Render("✓ note (no inline position)")
		case review.StateAccepted:
			badge = subtleStyle.Render("… waiting")
		default:
			badge = errorStyle.Render("✗ failed")
		}
		line := fmt.Sprintf("  %s  %s", badge, truncate(fmt.Sprintf("%s — %s", f.File, f.Title), max(s.width-32, 20)))
		b.WriteString(line + "\n")
	}
}
