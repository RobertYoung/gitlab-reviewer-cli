package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/config"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review/resultstore"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review/runner"
)

type (
	reviewEventMsg struct {
		iid  int64
		text string
	}
	reviewDoneMsg struct {
		iid     int64
		result  *review.Result
		err     error
		logPath string
		// rec is the stored result record (nil on failure or cancellation);
		// the findings screen keeps re-saving it as curation progresses.
		rec *resultstore.Record
	}
)

// reviewRun drives one review: checkout → claude → findings, streaming
// progress into a scrolling log. Esc cancels the underlying context.
type reviewRun struct {
	deps    Deps
	detail  gitlabx.MRDetail
	diffs   []gitlabx.FileDiff
	commits []gitlabx.Commit
	cfg     config.Config

	// manual comments composed in the diff view ride along so the findings
	// screen can publish them together with the review's findings;
	// manualReport keeps the diff view's copies in sync by ID.
	manual       []review.Finding
	manualReport func(id string, state review.FindingState)
	// agentNames is the agent selection from the picker; empty falls back
	// to the configured default inside the runner.
	agentNames []string
	// agentModels overrides the review model per agent (agent name → model
	// ID), from the picker; nil applies no overrides.
	agentModels map[string]string
	// incremental asks the runner for a delta review against the MR's last
	// stored record (the picker's default when one exists); the runner falls
	// back to a full review when the baseline is unusable.
	incremental bool

	ch      chan tea.Msg
	cancel  context.CancelFunc
	spin    spinner.Model
	log     []string
	logPath string
	started time.Time
	done    bool
	err     error
	width   int
	height  int
}

func newReviewRun(deps Deps, detail gitlabx.MRDetail, diffs []gitlabx.FileDiff, commits []gitlabx.Commit, manual []review.Finding, manualReport func(string, review.FindingState), agentNames []string, agentModels map[string]string, incremental bool) *reviewRun {
	return &reviewRun{
		deps:         deps,
		detail:       detail,
		diffs:        diffs,
		commits:      commits,
		manual:       manual,
		manualReport: manualReport,
		agentNames:   agentNames,
		agentModels:  agentModels,
		incremental:  incremental,
		cfg:          deps.cfgFor(detail.ProjectPath),
		spin:         spinner.New(spinner.WithSpinner(spinner.MiniDot)),
	}
}

func (s *reviewRun) Title() string {
	return fmt.Sprintf("reviewing %s", s.detail.Ref())
}

func (s *reviewRun) Hints() string {
	if s.done {
		if s.logPath != "" {
			return "l view log · o browser · esc back"
		}
		return "o browser · esc back"
	}
	return "o browser · esc cancel"
}

func (s *reviewRun) Init() tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.ch = make(chan tea.Msg, 64)
	s.started = time.Now()
	go s.run(ctx)
	return tea.Batch(s.spin.Tick, s.wait())
}

// wait pumps exactly one message from the review goroutine into the UI; it
// is re-issued from Update after each message until the done message.
func (s *reviewRun) wait() tea.Cmd {
	return func() tea.Msg { return <-s.ch }
}

// run executes the whole review off the UI goroutine through the shared
// runner, reporting through the channel. It must not touch the model. The
// done message is sent strictly last, after worktree cleanup.
func (s *reviewRun) run(ctx context.Context) {
	iid := s.detail.IID
	r := runner.Runner{
		Cfg:         s.cfg,
		Svc:         s.deps.Svc,
		Reviewer:    s.deps.Reviewer,
		Checkout:    s.deps.Checkout,
		Catalog:     s.deps.Agents,
		AgentNames:  s.agentNames,
		AgentModels: s.agentModels,
		Logs:        s.deps.Logs,
		Results:     s.deps.Results,
		Incremental: s.incremental,
	}
	out := r.Run(ctx, s.detail, s.diffs, s.commits, func(text string) {
		s.ch <- reviewEventMsg{iid: iid, text: text}
	})
	s.ch <- reviewDoneMsg{iid: iid, result: out.Result, err: out.Err, logPath: out.LogPath, rec: out.Rec}
}

func (s *reviewRun) Update(msg tea.Msg) (Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		s.width, s.height = msg.Width, msg.Height
		return s, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		s.spin, cmd = s.spin.Update(msg)
		return s, cmd

	case reviewEventMsg:
		if msg.iid != s.detail.IID {
			return s, nil
		}
		s.log = append(s.log, fmt.Sprintf("%6s  %s", time.Since(s.started).Round(time.Second), msg.text))
		return s, s.wait()

	case reviewDoneMsg:
		if msg.iid != s.detail.IID {
			return s, nil
		}
		s.done = true
		s.logPath = msg.logPath
		if msg.err != nil {
			if errors.Is(msg.err, context.Canceled) {
				return s, popScreen
			}
			s.err = msg.err
			return s, nil
		}
		// Swap this progress screen for the findings editor.
		return s, popScreens(1, newFindings(s.deps, s.detail, s.diffs, msg.result, msg.rec, s.manual, s.manualReport))

	case tea.KeyPressMsg:
		switch msg.String() {
		case "esc":
			if s.done {
				return s, popScreen
			}
			s.log = append(s.log, "cancelling…")
			s.cancel()
			return s, nil
		case "l":
			// After a failure the screen stays up; the stored run log has
			// the full story, including lines that scrolled away.
			if s.done && s.logPath != "" {
				return s, pushScreen(newLogView(s.deps, s.detail.Ref(), s.detail.WebURL, s.logPath))
			}
		case "o":
			return s, openURLCmd(s.deps, s.detail.WebURL)
		}
	}
	return s, nil
}

func (s *reviewRun) View() string {
	var b strings.Builder
	if s.done && s.err != nil {
		b.WriteString(errorStyle.Render("review failed") + "\n\n")
		b.WriteString(wrap(s.err.Error(), s.width))
		hint := "esc to go back"
		if s.logPath != "" {
			hint = "l to view the run log · " + hint
		}
		b.WriteString("\n\n" + subtleStyle.Render(hint))
		return b.String()
	}

	fmt.Fprintf(&b, "%s reviewing %s  %s\n\n", s.spin.View(), s.detail.Ref(),
		subtleStyle.Render(time.Since(s.started).Round(time.Second).String()))

	visible := max(s.height-4, 3)
	logLines := s.log
	if len(logLines) > visible {
		logLines = logLines[len(logLines)-visible:]
	}
	for _, l := range logLines {
		b.WriteString(subtleStyle.Render(truncate(l, max(s.width-2, 20))) + "\n")
	}
	return b.String()
}

// wrap does simple whitespace wrapping for error text.
func wrap(s string, width int) string {
	if width < 20 {
		width = 80
	}
	var b strings.Builder
	line := 0
	for _, word := range strings.Fields(s) {
		if line+len(word)+1 > width {
			b.WriteByte('\n')
			line = 0
		} else if line > 0 {
			b.WriteByte(' ')
			line++
		}
		b.WriteString(word)
		line += len(word)
	}
	return b.String()
}
