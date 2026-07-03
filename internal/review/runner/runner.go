// Package runner orchestrates one review run end to end — checkout, prompt
// assembly, reviewer passes, result merging, and persistence — so every
// frontend (TUI, web GUI) drives the same pipeline and stores the same
// artifacts.
package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/config"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review/resultstore"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review/runlog"
)

// CheckoutFunc prepares a review worktree for an MR and returns its path
// plus a cleanup function. Progress lines go to the run's progress log.
type CheckoutFunc func(ctx context.Context, mr gitlabx.MRDetail, progress func(string)) (path string, cleanup func(context.Context) error, err error)

// Runner bundles everything one review run needs. Cfg must already be
// resolved for the MR's project (per-project overrides applied).
type Runner struct {
	Cfg      config.Config
	Svc      gitlabx.Service
	Reviewer review.Reviewer
	Checkout CheckoutFunc
	// Logs stores the run's progress log; nil disables storing.
	Logs *runlog.Store
	// Results stores the run's result record; nil disables storing.
	Results *resultstore.Store
}

// Outcome is everything a frontend needs after a run: the merged result,
// the stored record (nil on failure, cancellation, or when storage is
// disabled), and the progress log location.
type Outcome struct {
	Result  *review.Result
	Rec     *resultstore.Record
	LogPath string
	Err     error
}

// Run executes the whole review, reporting every progress line through emit
// (may be nil) and mirroring it into the stored run log. On success the
// result is saved as a record so the review can be reopened later; curation
// screens keep re-saving the same record as states change.
func (r Runner) Run(ctx context.Context, detail gitlabx.MRDetail, diffs []gitlabx.FileDiff, commits []gitlabx.Commit, emit func(string)) Outcome {
	if emit == nil {
		emit = func(string) {}
	}
	started := time.Now()
	rl := r.Logs.Start(detail.IID, detail.Ref(), detail.Title)
	log := func(text string) {
		rl.Append(text)
		emit(text)
	}
	res, err := r.execute(ctx, detail, diffs, commits, log)
	switch {
	case errors.Is(err, context.Canceled):
		rl.Finish("cancelled")
	case err != nil:
		rl.Finish("failed: " + err.Error())
	default:
		rl.Finish(fmt.Sprintf("completed with %d finding(s)", len(res.Findings)))
	}
	var rec *resultstore.Record
	if err == nil {
		rec = &resultstore.Record{
			IID:       detail.IID,
			Ref:       detail.Ref(),
			Title:     detail.Title,
			Started:   started,
			Summary:   res.Summary,
			Warnings:  res.Warnings,
			SessionID: res.SessionID,
			CostUSD:   res.CostUSD,
			LogPath:   rl.Path(),
			Findings:  res.Findings,
		}
		if saveErr := r.Results.Save(*rec); saveErr != nil {
			res.Warnings = append(res.Warnings, "could not store the review result: "+saveErr.Error())
		}
	}
	return Outcome{Result: res, Rec: rec, LogPath: rl.Path(), Err: err}
}

func (r Runner) execute(ctx context.Context, detail gitlabx.MRDetail, diffs []gitlabx.FileDiff, commits []gitlabx.Commit, emit func(string)) (*review.Result, error) {
	emit("preparing repository…")
	path, cleanup, err := r.Checkout(ctx, detail, emit)
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
	template, err := r.Svc.GetMergeRequestTemplate(ctx, detail.Project())
	if err != nil {
		emit("note: could not fetch MR template: " + err.Error())
	}

	reqs, warnings, err := BuildRequests(r.Cfg, detail, diffs, commits, template, path)
	if err != nil {
		return nil, err
	}
	// Rebase status is a deterministic MR-level fact with no diff line to
	// anchor a finding on, so it surfaces as a review warning rather than a
	// model finding.
	if detail.NeedsRebase() {
		warnings = append(warnings, RebaseWarning(detail))
	}
	for _, w := range warnings {
		emit(w)
	}

	// Oversized MRs run as several passes; Claude has the whole repo on
	// disk in every pass, only the focus diff changes.
	results := make([]*review.Result, 0, len(reqs))
	for i, req := range reqs {
		if len(reqs) > 1 {
			emit(fmt.Sprintf("review pass %d/%d (%d file(s)) with %s…", i+1, len(reqs), len(req.Diffs), r.Reviewer.Name()))
		} else {
			emit(fmt.Sprintf("reviewing %d file(s) with %s…", len(req.Diffs), r.Reviewer.Name()))
		}
		res, err := r.Reviewer.Review(ctx, req, func(e review.Event) {
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

// RebaseWarning describes why the MR branch is not up to date with its
// target, for the findings-screen warning banner.
func RebaseWarning(detail gitlabx.MRDetail) string {
	switch {
	case detail.HasConflicts:
		return fmt.Sprintf("MR branch has conflicts with %s — rebase before review", detail.TargetBranch)
	case detail.DivergedCommits > 0:
		return fmt.Sprintf("MR branch is %d commit(s) behind %s — a rebase is needed", detail.DivergedCommits, detail.TargetBranch)
	default:
		return "MR branch is not up to date with its target"
	}
}

// BuildRequests assembles the reviewer request(s) from project-resolved
// config: chunked diffs, category list, and combined custom instructions.
func BuildRequests(cfg config.Config, detail gitlabx.MRDetail, diffs []gitlabx.FileDiff, commits []gitlabx.Commit, template, repoPath string) ([]review.Request, []string, error) {
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
