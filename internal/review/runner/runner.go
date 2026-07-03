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
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/checkout"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/config"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review/agents"
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
	// Catalog is the available agents (builtins + user agents); nil means
	// builtins only. Project agents shipped in the checkout are merged in
	// after checkout, so they can shadow or satisfy selected names.
	Catalog *agents.Catalog
	// AgentNames is the agent selection for this run (from the picker or
	// --agents); empty falls back to cfg.Review.Agents.
	AgentNames []string
	// AgentModels overrides the review model per agent (agent name → model
	// ID), from the picker. It takes precedence over an agent's frontmatter
	// model and cfg.Review.Model; agents absent from the map keep those
	// defaults. Nil applies no overrides.
	AgentModels map[string]string
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

	reqs, info, warnings, err := BuildRequests(r.Cfg, detail, diffs, commits, template, path)
	if err != nil {
		return nil, err
	}
	// Rebase status is a deterministic MR-level fact with no diff line to
	// anchor a finding on, so it surfaces as a review warning rather than a
	// model finding.
	if detail.NeedsRebase() {
		warnings = append(warnings, RebaseWarning(detail))
	}
	for _, line := range info {
		emit(line)
	}
	for _, w := range warnings {
		emit(w)
	}

	selected, err := r.resolveAgents(detail, path, emit)
	if err != nil {
		return nil, err
	}

	// One reviewer pass per selected agent per diff chunk, run concurrently
	// under the configured limit. Claude has the whole repo on disk in every
	// pass; only the focus diff and the agent prompt change.
	type task struct {
		agent agents.Agent
		req   review.Request
		pass  int // 1-based chunk index, for multi-chunk progress lines
	}
	tasks := make([]task, 0, len(selected)*len(reqs))
	for _, a := range selected {
		for i, req := range reqs {
			req.AgentName = a.Name
			req.AgentPrompt = agentPrompt(a)
			req.Categories = append([]review.Category(nil), a.Categories...)
			if m := r.modelFor(a); m != "" {
				req.Model = m
			}
			tasks = append(tasks, task{agent: a, req: req, pass: i + 1})
		}
	}
	// The budget is a total for the run: split it evenly across the planned
	// passes so worst-case spend stays at the configured amount. Unspent
	// slices are not redistributed.
	if total := r.Cfg.Review.MaxBudgetUSD; total > 0 {
		per := total / float64(len(tasks))
		for i := range tasks {
			tasks[i].req.MaxBudgetUSD = per
		}
	}

	emit(fmt.Sprintf("running %d agent(s) over %d review pass(es) with %s…", len(selected), len(reqs), r.Reviewer.Name()))

	concurrency := r.Cfg.Review.AgentConcurrency
	if concurrency < 1 {
		concurrency = 1
	}
	var (
		mu      sync.Mutex // guards emit: the run log writer is not concurrent-safe
		wg      sync.WaitGroup
		sem     = make(chan struct{}, concurrency)
		results = make([]*review.Result, len(tasks))
		errs    = make([]error, len(tasks))
	)
	log := func(agent, text string) {
		mu.Lock()
		defer mu.Unlock()
		emit("[" + agent + "] " + text)
	}
	for i, t := range tasks {
		wg.Add(1)
		go func(i int, t task) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if ctx.Err() != nil {
				errs[i] = ctx.Err()
				return
			}
			files := len(t.req.Diffs) + len(t.req.DiffFiles)
			if len(reqs) > 1 {
				log(t.agent.Name, fmt.Sprintf("pass %d/%d starting (%d file(s))…", t.pass, len(reqs), files))
			} else {
				log(t.agent.Name, fmt.Sprintf("reviewing %d file(s)…", files))
			}
			res, err := r.Reviewer.Review(ctx, t.req, func(e review.Event) {
				log(t.agent.Name, e.Text)
			})
			if err != nil {
				errs[i] = err
				log(t.agent.Name, "failed: "+err.Error())
				return
			}
			res.Agent = t.agent.Name
			for j := range res.Findings {
				res.Findings[j].Agent = t.agent.Name
			}
			results[i] = res
			log(t.agent.Name, fmt.Sprintf("done: %d finding(s), $%.2f", len(res.Findings), res.CostUSD))
		}(i, t)
	}
	wg.Wait()

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	var ok []*review.Result
	for i, res := range results {
		if res != nil {
			ok = append(ok, res)
			continue
		}
		// Keep what the other agents found rather than losing it all.
		warnings = append(warnings, fmt.Sprintf("agent %s (pass %d/%d) failed: %v", tasks[i].agent.Name, tasks[i].pass, len(reqs), errs[i]))
	}
	if len(ok) == 0 {
		return nil, errs[0]
	}
	var final *review.Result
	if len(ok) == 1 {
		final = ok[0]
	} else {
		final = review.MergeResults(ok)
	}
	// Surface pre-review warnings (chunking, rebase status, failed agents)
	// in the findings screen, not just the transient progress log.
	final.Warnings = append(warnings, final.Warnings...)
	return final, nil
}

// resolveAgents merges project-shipped agents into the catalog and resolves
// this run's selection against it. Selection precedence: the run's explicit
// AgentNames (picker, --agents), then cfg.Review.Agents.
func (r Runner) resolveAgents(detail gitlabx.MRDetail, repoPath string, emit func(string)) ([]agents.Agent, error) {
	catalog := r.Catalog
	if catalog == nil {
		catalog = agents.NewCatalog("")
	}
	// Path/root checkout modes may keep agent definitions untracked in the
	// user's local clone (like local_overlay files); merge them first so
	// definitions committed at the MR head shadow them — mirroring the
	// pickers, which offer local-clone agents in those modes.
	if dir, ok := checkout.LocalRepoDir(r.Cfg.Checkout, r.Cfg.GitLab.BaseURL, detail.ProjectPath); ok {
		catalog = catalog.WithProject(dir)
	}
	catalog = catalog.WithProject(repoPath)
	for _, w := range catalog.Warnings() {
		emit(w)
	}
	names := r.AgentNames
	if len(names) == 0 {
		names = r.Cfg.Review.Agents
	}
	if len(names) == 0 {
		// Config that skipped load-time finalisation (e.g. built from
		// config.Default() directly): apply the same categories fallback.
		names = r.Cfg.Review.Categories
	}
	selected, err := catalog.Resolve(names)
	if err != nil {
		return nil, err
	}
	// Point out repo-shipped agents the user has not enabled.
	inSelection := map[string]bool{}
	for _, a := range selected {
		inSelection[a.Name] = true
	}
	for _, a := range catalog.All() {
		if a.Source == agents.SourceProject && !inSelection[a.Name] {
			emit(fmt.Sprintf("note: this repo defines agent %q (%s); enable it with --agents or the picker", a.Name, a.Path))
		}
	}
	return selected, nil
}

// modelFor resolves the review model an agent runs with: the per-agent
// override from AgentModels (picker choice) first, then the agent's
// frontmatter model. Empty means neither applies, leaving req.Model at the
// cfg.Review.Model default BuildRequests set.
func (r Runner) modelFor(a agents.Agent) string {
	if m := r.AgentModels[a.Name]; m != "" {
		return m
	}
	return a.Model
}

// agentPrompt renders the agent's focus prompt, folding in the optional
// severity hint from its frontmatter.
func agentPrompt(a agents.Agent) string {
	p := a.Prompt
	if a.Severity != "" {
		p += fmt.Sprintf("\nUnless clearly otherwise, findings from this agent are typically %q severity.", a.Severity)
	}
	return p
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

// DiffFilesDir is where oversized diffs are written inside the checkout so
// the reviewer can Read them (the review session has no Bash to run git).
const DiffFilesDir = ".review-diffs"

// BuildRequests assembles the reviewer request(s) from project-resolved
// config: chunked diffs and combined custom instructions. The requests are
// agent-neutral; the runner specialises a copy per selected agent. Diffs too
// large to inline are written into the checkout under DiffFilesDir so the
// reviewer can still see them. It returns the requests, informational
// progress lines, and warnings worth persisting with the result (only
// genuine information loss qualifies).
func BuildRequests(cfg config.Config, detail gitlabx.MRDetail, diffs []gitlabx.FileDiff, commits []gitlabx.Commit, template, repoPath string) ([]review.Request, []string, []string, error) {
	var info, warnings []string

	chunks, skipped := review.ChunkDiffs(diffs, cfg.Review.Exclude, cfg.Review.MaxDiffKB)

	// Drop diff files from an earlier interrupted run of a reused worktree.
	_ = os.RemoveAll(filepath.Join(repoPath, DiffFilesDir))

	var (
		excluded    []string
		unavailable []string
		diffFiles   []review.DiffFile
	)
	for i, s := range skipped {
		switch s.Reason {
		case review.SkipExcluded:
			excluded = append(excluded, s.Path)
		case review.SkipUnavailable:
			unavailable = append(unavailable, s.Path)
		case review.SkipOverBudget:
			rel, err := writeDiffFile(repoPath, i, s)
			if err != nil {
				unavailable = append(unavailable, s.Path)
				warnings = append(warnings, fmt.Sprintf("could not write the oversized diff for %s: %v", s.Path, err))
				continue
			}
			diffFiles = append(diffFiles, review.DiffFile{Path: s.Path, DiffPath: rel})
		}
	}

	if len(chunks) == 0 && len(diffFiles) == 0 {
		return nil, nil, nil, errors.New("nothing to review: every changed file is excluded by configuration or has no retrievable diff")
	}
	if len(excluded) > 0 {
		info = append(info, fmt.Sprintf("%d file(s) excluded from review by configured filters (lockfiles/vendored/generated)", len(excluded)))
	}
	if len(diffFiles) > 0 {
		info = append(info, fmt.Sprintf("%d oversized diff(s) written into the checkout for the reviewer to read", len(diffFiles)))
	}
	if len(unavailable) > 0 {
		warnings = append(warnings, fmt.Sprintf("%d file(s) changed but GitLab returned no diff (too large); the reviewer only sees their head state", len(unavailable)))
	}
	if len(chunks) > 1 {
		warnings = append(warnings, fmt.Sprintf("large MR: splitting the review into %d passes", len(chunks)))
	}

	instructions := cfg.Review.Instructions
	if cfg.Review.InstructionsFile != "" {
		data, err := os.ReadFile(cfg.Review.InstructionsFile)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("reading review.instructions_file: %w", err)
		}
		if instructions != "" {
			instructions += "\n\n"
		}
		instructions += string(data)
	}

	if len(chunks) == 0 {
		// Everything inline-sized was filtered out but oversized diffs
		// remain reviewable from disk: run one pass with no inline diff.
		chunks = [][]gitlabx.FileDiff{nil}
	}
	reqs := make([]review.Request, 0, len(chunks))
	for i, chunk := range chunks {
		req := review.Request{
			RepoPath:     repoPath,
			MR:           detail,
			Diffs:        chunk,
			Commits:      commits,
			Template:     template,
			Excluded:     excluded,
			Unavailable:  unavailable,
			Instructions: instructions,
			Model:        cfg.Review.Model,
			Timeout:      cfg.Review.Timeout,
			MaxBudgetUSD: cfg.Review.MaxBudgetUSD,
		}
		// On-disk diffs join the first pass only, so multi-pass reviews
		// don't report the same oversized files twice.
		if i == 0 {
			req.DiffFiles = diffFiles
		}
		reqs = append(reqs, req)
	}
	return reqs, info, warnings, nil
}

// writeDiffFile stores one oversized diff inside the checkout and returns
// its repo-relative path. The worktree is always tool-managed and detached,
// never the user's working tree, so writing scratch files here is safe.
func writeDiffFile(repoPath string, n int, s review.SkippedDiff) (string, error) {
	dir := filepath.Join(repoPath, DiffFilesDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	name := fmt.Sprintf("%03d-%s.diff", n+1, strings.ReplaceAll(s.Path, "/", "__"))
	content := fmt.Sprintf("--- a/%s\n+++ b/%s\n%s", s.OldPath, s.Path, s.Diff)
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		return "", err
	}
	return DiffFilesDir + "/" + name, nil
}
