package tui

import (
	"context"
	"fmt"
	"strings"
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

type publishDoneMsg struct{ iid int64 }

// publish posts the accepted findings to GitLab as inline discussions,
// falling back to general notes for unresolvable positions.
type publish struct {
	deps    Deps
	detail  gitlabx.MRDetail
	items   []review.Finding
	cfg     config.Config
	index   []position.FileIndex
	ch      chan tea.Msg
	spin    spinner.Model
	started bool
	done    bool
	current int
	errs    []string
	width   int
	height  int
}

func newPublish(deps Deps, detail gitlabx.MRDetail, diffs []gitlabx.FileDiff, accepted []review.Finding) *publish {
	return &publish{
		deps:   deps,
		detail: detail,
		items:  accepted,
		cfg:    deps.cfgFor(detail.ProjectPath),
		index:  position.Index(diffs),
		spin:   spinner.New(spinner.WithSpinner(spinner.MiniDot)),
	}
}

func (s *publish) Title() string {
	return fmt.Sprintf("publishing · %s", s.detail.Ref())
}

func (s *publish) Hints() string {
	if s.done {
		return "enter/esc done"
	}
	return "publishing…"
}

func (s *publish) Init() tea.Cmd {
	s.ch = make(chan tea.Msg, 16)
	s.started = true
	go s.run()
	return tea.Batch(s.spin.Tick, s.wait())
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
	body := f.RenderBody(s.cfg.Publish.Attribution)

	pos, resolveErr := position.Resolve(f.File, f.Line.OldLine, f.Line.NewLine, s.index, s.detail.DiffRefs)
	if resolveErr == nil {
		err := s.deps.Svc.CreateInlineDiscussion(ctx, project, s.detail.IID, body, pos)
		if err == nil {
			return review.StatePublished, nil
		}
		if !s.cfg.Publish.FallbackToNote {
			return review.StatePending, err
		}
	} else if !s.cfg.Publish.FallbackToNote {
		return review.StatePending, resolveErr
	}

	// Fallback: general MR note with a permalink to the flagged line.
	fallback := f.RenderFallbackBody(s.cfg.Publish.Attribution, s.blobURL(f))
	if err := s.deps.Svc.CreateNote(ctx, project, s.detail.IID, fallback); err != nil {
		return review.StatePending, err
	}
	return review.StateFellBack, nil
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
		return s, s.wait()

	case publishDoneMsg:
		if msg.iid != s.detail.IID {
			return s, nil
		}
		s.done = true
		return s, nil

	case tea.KeyPressMsg:
		switch msg.String() {
		case "enter", "esc":
			if s.done {
				// Back to the MR detail screen (skip the findings screen).
				return s, popScreens(2, nil)
			}
		case "q":
			if s.done {
				return s, tea.Quit
			}
		}
	}
	return s, nil
}

func (s *publish) View() string {
	var b strings.Builder
	published, fellBack, failed := 0, 0, len(s.errs)
	for _, f := range s.items {
		switch f.State {
		case review.StatePublished:
			published++
		case review.StateFellBack:
			fellBack++
		}
	}

	if s.done {
		fmt.Fprintf(&b, "%s\n\n", headerStyle.Render("publish complete"))
	} else {
		fmt.Fprintf(&b, "%s publishing %d/%d…\n\n", s.spin.View(), s.current, len(s.items))
	}

	for _, f := range s.items {
		var badge string
		switch f.State {
		case review.StatePublished:
			badge = addedStyle.Render("✓ inline")
		case review.StateFellBack:
			badge = draftStyle.Render("✓ note (no inline position)")
		case review.StateAccepted:
			badge = subtleStyle.Render("… waiting")
		default:
			badge = errorStyle.Render("✗ failed")
		}
		line := fmt.Sprintf("  %s  %s", badge, truncate(fmt.Sprintf("%s — %s", f.File, f.Title), max(s.width-30, 20)))
		b.WriteString(line + "\n")
	}

	if s.done {
		fmt.Fprintf(&b, "\n%d inline · %d as notes · %d failed\n", published, fellBack, failed)
		for _, e := range s.errs {
			b.WriteString(errorStyle.Render(truncate("  "+e, max(s.width-2, 20))) + "\n")
		}
		b.WriteString("\n" + subtleStyle.Render("enter to return to the merge request"))
	}
	return b.String()
}
