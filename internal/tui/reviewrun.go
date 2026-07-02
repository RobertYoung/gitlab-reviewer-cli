package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/config"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review"
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

func newReviewRun(deps Deps, detail gitlabx.MRDetail, diffs []gitlabx.FileDiff, commits []gitlabx.Commit, manual []review.Finding, manualReport func(string, review.FindingState)) *reviewRun {
	return &reviewRun{
		deps:         deps,
		detail:       detail,
		diffs:        diffs,
		commits:      commits,
		manual:       manual,
		manualReport: manualReport,
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

// run executes the whole review off the UI goroutine, reporting through the
// channel. It must not touch the model. The done message is sent strictly
// last, after worktree cleanup. Every progress line is also appended to the
// run log on disk so it can be read back after this screen is gone.
func (s *reviewRun) run(ctx context.Context) {
	iid := s.detail.IID
	rl := s.deps.Logs.Start(iid, s.detail.Ref(), s.detail.Title)
	emit := func(text string) {
		rl.Append(text)
		s.ch <- reviewEventMsg{iid: iid, text: text}
	}
	res, err := s.execute(ctx, emit)
	switch {
	case errors.Is(err, context.Canceled):
		rl.Finish("cancelled")
	case err != nil:
		rl.Finish("failed: " + err.Error())
	default:
		rl.Finish(fmt.Sprintf("completed with %d finding(s)", len(res.Findings)))
	}
	s.ch <- reviewDoneMsg{iid: iid, result: res, err: err, logPath: rl.Path()}
}

func (s *reviewRun) execute(ctx context.Context, emit func(string)) (*review.Result, error) {
	emit("preparing repository…")
	path, cleanup, err := s.deps.Checkout(ctx, s.detail, emit)
	if err != nil {
		return nil, fmt.Errorf("checkout failed: %w", err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := cleanup(cleanupCtx); err != nil {
			emit("warning: worktree cleanup failed: " + err.Error())
		}
	}()

	// Fetch the MR description template from GitLab (best-effort): it lets
	// the review run a description-vs-template hygiene check when the team's
	// instructions ask for one. Fetched here in the Go process, not the
	// read-only claude subprocess.
	template, err := s.deps.Svc.GetMergeRequestTemplate(ctx, s.detail.Project())
	if err != nil {
		emit("note: could not fetch MR template: " + err.Error())
	}

	reqs, warnings, err := buildRequests(s.cfg, s.detail, s.diffs, s.commits, template, path)
	if err != nil {
		return nil, err
	}
	// Rebase status is a deterministic MR-level fact with no diff line to
	// anchor a finding on, so it surfaces as a review warning rather than a
	// model finding.
	if s.detail.NeedsRebase() {
		warnings = append(warnings, rebaseWarning(s.detail))
	}
	for _, w := range warnings {
		emit(w)
	}

	// Oversized MRs run as several passes; Claude has the whole repo on
	// disk in every pass, only the focus diff changes.
	results := make([]*review.Result, 0, len(reqs))
	for i, req := range reqs {
		if len(reqs) > 1 {
			emit(fmt.Sprintf("review pass %d/%d (%d file(s)) with %s…", i+1, len(reqs), len(req.Diffs), s.deps.Reviewer.Name()))
		} else {
			emit(fmt.Sprintf("reviewing %d file(s) with %s…", len(req.Diffs), s.deps.Reviewer.Name()))
		}
		res, err := s.deps.Reviewer.Review(ctx, req, func(e review.Event) {
			emit(e.Text)
		})
		if err != nil {
			if len(results) > 0 {
				// Keep what earlier passes found rather than losing it all.
				merged := review.MergeResults(results)
				merged.Warnings = append(merged.Warnings, fmt.Sprintf("pass %d/%d failed: %v", i+1, len(reqs), err))
				return merged, nil
			}
			return nil, err
		}
		results = append(results, res)
	}
	var final *review.Result
	if len(results) == 1 {
		final = results[0]
	} else {
		final = review.MergeResults(results)
	}
	// Surface pre-review warnings (chunking, rebase status) in the findings
	// screen, not just the transient progress log.
	final.Warnings = append(warnings, final.Warnings...)
	return final, nil
}

// rebaseWarning describes why the MR branch is not up to date with its
// target, for the findings-screen warning banner.
func rebaseWarning(detail gitlabx.MRDetail) string {
	switch {
	case detail.HasConflicts:
		return fmt.Sprintf("MR branch has conflicts with %s — rebase before review", detail.TargetBranch)
	case detail.DivergedCommits > 0:
		return fmt.Sprintf("MR branch is %d commit(s) behind %s — a rebase is needed", detail.DivergedCommits, detail.TargetBranch)
	default:
		return "MR branch is not up to date with its target"
	}
}

// buildRequests assembles the reviewer request(s) from project-resolved
// config: chunked diffs, category list, and combined custom instructions.
func buildRequests(cfg config.Config, detail gitlabx.MRDetail, diffs []gitlabx.FileDiff, commits []gitlabx.Commit, template, repoPath string) ([]review.Request, []string, error) {
	var warnings []string

	chunks, skipped := review.ChunkDiffs(diffs, cfg.Review.Exclude, cfg.Review.MaxDiffKB)
	if len(chunks) == 0 {
		return nil, nil, errors.New("nothing to review: every changed file is excluded or over the diff budget")
	}
	if len(skipped) > 0 {
		warnings = append(warnings, fmt.Sprintf("%d file(s) excluded from the prompt (globs/size budget)", len(skipped)))
	}
	if len(chunks) > 1 {
		warnings = append(warnings, fmt.Sprintf("large MR: splitting the review into %d passes", len(chunks)))
	}

	instructions := cfg.Review.Instructions
	if cfg.Review.InstructionsFile != "" {
		data, err := os.ReadFile(cfg.Review.InstructionsFile)
		if err != nil {
			return nil, nil, fmt.Errorf("reading review.instructions_file: %w", err)
		}
		if instructions != "" {
			instructions += "\n\n"
		}
		instructions += string(data)
	}

	categories := make([]review.Category, 0, len(cfg.Review.Categories))
	for _, c := range cfg.Review.Categories {
		categories = append(categories, review.Category(c))
	}

	reqs := make([]review.Request, 0, len(chunks))
	for _, chunk := range chunks {
		reqs = append(reqs, review.Request{
			RepoPath:     repoPath,
			MR:           detail,
			Diffs:        chunk,
			Commits:      commits,
			Template:     template,
			Truncated:    skipped,
			Instructions: instructions,
			Categories:   categories,
			Model:        cfg.Review.Model,
			Timeout:      cfg.Review.Timeout,
			MaxBudgetUSD: cfg.Review.MaxBudgetUSD,
		})
	}
	return reqs, warnings, nil
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
		return s, popScreens(1, newFindings(s.deps, s.detail, s.diffs, msg.result, msg.logPath, s.manual, s.manualReport))

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
