package webui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/config"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review/agents"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review/publisher"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review/resultstore"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review/runner"
)

// reviewRun is one in-flight (or finished) review executed server-side.
// Progress lines accumulate for replay so a page opened mid-run still shows
// the full log; subscribers receive live lines over SSE.
type reviewRun struct {
	ID       string
	Instance string
	Project  string
	IID      int64
	Ref      string
	Title    string
	WebURL   string
	Started  time.Time
	cancel   context.CancelFunc

	mu         sync.Mutex
	lines      []string
	subs       map[chan runEvent]struct{}
	done       bool
	err        error
	recName    string // stored record file name; "" when the run stored none
	logName    string // stored progress log file name
	findings   int    // findings stored with the record
	published  int    // findings auto-publish posted live (immediate mode)
	draftReady bool   // auto-publish left a draft review pending
}

// runEvent is one SSE payload: a progress line, or the final done event.
type runEvent struct {
	Line string
	Done *runOutcome
}

// runOutcome summarises a finished run for the browser.
type runOutcome struct {
	Cancelled bool
	Err       string
	RecName   string
	LogName   string
	Findings  int
	// Published counts findings auto-publish (publish.auto_comment) posted
	// straight to GitLab in immediate mode, so the findings page can confirm
	// the outcome rather than landing on it silently.
	Published int
	// DraftReady reports that auto-publish (publish.auto_comment, draft
	// mode) created a pending draft review awaiting one-click publication.
	DraftReady bool
}

func (r *reviewRun) append(text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	line := fmt.Sprintf("%6s  %s", time.Since(r.Started).Round(time.Second), text)
	r.lines = append(r.lines, line)
	for ch := range r.subs {
		select {
		case ch <- runEvent{Line: line}:
		default: // slow subscriber; it replays on reconnect
		}
	}
}

func (r *reviewRun) finish(out runOutcome) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.done = true
	if out.Err != "" {
		r.err = errors.New(out.Err)
	}
	r.recName = out.RecName
	r.logName = out.LogName
	r.findings = out.Findings
	r.published = out.Published
	r.draftReady = out.DraftReady
	for ch := range r.subs {
		select {
		case ch <- runEvent{Done: &out}:
		default:
		}
	}
}

// subscribe returns the lines so far, the outcome if the run already
// finished, and a channel for what follows.
func (r *reviewRun) subscribe() (replay []string, done *runOutcome, ch chan runEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	replay = append([]string(nil), r.lines...)
	if r.done {
		done = r.outcomeLocked()
		return replay, done, nil
	}
	ch = make(chan runEvent, 256)
	r.subs[ch] = struct{}{}
	return replay, nil, ch
}

func (r *reviewRun) unsubscribe(ch chan runEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.subs, ch)
}

func (r *reviewRun) outcomeLocked() *runOutcome {
	out := &runOutcome{RecName: r.recName, LogName: r.logName, Findings: r.findings, Published: r.published, DraftReady: r.draftReady}
	if r.err != nil {
		if errors.Is(r.err, context.Canceled) {
			out.Cancelled = true
		}
		out.Err = r.err.Error()
	}
	return out
}

func (r *reviewRun) snapshot() (lines []string, done bool, out *runOutcome) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.done {
		out = r.outcomeLocked()
	}
	return append([]string(nil), r.lines...), r.done, out
}

// runRegistry tracks review runs by ID for the run page, its SSE stream,
// and cancellation.
type runRegistry struct {
	mu   sync.Mutex
	seq  int
	runs map[string]*reviewRun
}

func newRunRegistry() *runRegistry {
	return &runRegistry{runs: map[string]*reviewRun{}}
}

func (g *runRegistry) get(id string) *reviewRun {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.runs[id]
}

// reviewStartOptions carries the per-run choices from the review options
// form into startRun.
type reviewStartOptions struct {
	AgentNames  []string
	AgentModels map[string]string
	Incremental bool
	// Overrides are applied on top of the effective config for this run
	// only; nil or zero fields keep the configured defaults.
	Overrides *agents.RunOptions
}

// applyRunOverrides folds the form's per-run overrides into this run's
// config copy and returns a human-readable summary of what changed, or ""
// when everything is at its configured default. Extra instructions append
// to any configured ones rather than replacing them.
func applyRunOverrides(cfg *config.Config, o *agents.RunOptions) string {
	if o == nil {
		return ""
	}
	var parts []string
	if o.Concurrency > 0 && o.Concurrency != cfg.Review.AgentConcurrency {
		cfg.Review.AgentConcurrency = o.Concurrency
		parts = append(parts, fmt.Sprintf("concurrency %d", o.Concurrency))
	}
	if o.Model != "" && o.Model != cfg.Review.Model {
		cfg.Review.Model = o.Model
		parts = append(parts, "model "+o.Model)
	}
	if o.MaxBudgetUSD != nil && *o.MaxBudgetUSD != cfg.Review.MaxBudgetUSD {
		cfg.Review.MaxBudgetUSD = *o.MaxBudgetUSD
		parts = append(parts, fmt.Sprintf("budget $%g", *o.MaxBudgetUSD))
	}
	if o.Instructions != "" {
		if cfg.Review.Instructions != "" {
			cfg.Review.Instructions += "\n\n"
		}
		cfg.Review.Instructions += o.Instructions
		parts = append(parts, "extra instructions")
	}
	// A run's grant is always the intersection of what was picked with the
	// configured catalog — including an empty pick, which clears the grant
	// entirely — never the catalog itself: network/shell access must be
	// opted into per run, not silently inherited from config.
	cfg.Review.AllowedDomains = intersect(cfg.Review.AllowedDomains, o.Domains)
	if len(cfg.Review.AllowedDomains) > 0 {
		parts = append(parts, "domains: "+strings.Join(cfg.Review.AllowedDomains, ", "))
	}
	cfg.Review.AllowedCommands = intersect(cfg.Review.AllowedCommands, o.Commands)
	if len(cfg.Review.AllowedCommands) > 0 {
		parts = append(parts, "commands: "+strings.Join(cfg.Review.AllowedCommands, ", "))
	}
	return strings.Join(parts, ", ")
}

// intersect returns the elements of picked that also appear in catalog,
// in catalog order: a run can only narrow the admin-configured allowlist,
// never widen it, even if a posted form field is tampered with.
func intersect(catalog, picked []string) []string {
	want := make(map[string]bool, len(picked))
	for _, p := range picked {
		want[p] = true
	}
	var out []string
	for _, c := range catalog {
		if want[c] {
			out = append(out, c)
		}
	}
	return out
}

// start launches a review in a server goroutine, mirroring the TUI review
// screen: run through the shared runner, then fold the diff view's pending
// manual comments and the auto-accept threshold into the stored record so
// the findings page opens with the same curation state the TUI would show.
func (s *Server) startRun(d *Deps, instance string, detail gitlabx.MRDetail, diffs []gitlabx.FileDiff, commits []gitlabx.Commit, opts reviewStartOptions) *reviewRun {
	s.runs.mu.Lock()
	s.runs.seq++
	run := &reviewRun{
		ID:       fmt.Sprintf("r%d", s.runs.seq),
		Instance: instance,
		Project:  detail.ProjectPath,
		IID:      detail.IID,
		Ref:      detail.Ref(),
		Title:    detail.Title,
		WebURL:   detail.WebURL,
		Started:  time.Now(),
		subs:     map[chan runEvent]struct{}{},
	}
	s.runs.runs[run.ID] = run
	s.runs.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	run.cancel = cancel

	cfg := d.cfgFor(detail.ProjectPath)
	if summary := applyRunOverrides(&cfg, opts.Overrides); summary != "" {
		run.append("run overrides: " + summary)
	}
	key := mrKey(instance, detail.ProjectPath, detail.IID)

	go func() {
		defer cancel()
		r := runner.Runner{
			Cfg:         cfg,
			Svc:         d.Svc,
			Reviewer:    d.Reviewer,
			Checkout:    d.Checkout,
			Catalog:     d.Agents,
			AgentNames:  opts.AgentNames,
			AgentModels: opts.AgentModels,
			Logs:        d.Logs,
			Results:     d.Results,
			Incremental: opts.Incremental,
		}
		out := r.Run(ctx, detail, diffs, commits, run.append)

		outcome := runOutcome{}
		if out.Err != nil {
			if errors.Is(out.Err, context.Canceled) {
				outcome.Cancelled = true
			}
			outcome.Err = out.Err.Error()
		}
		if out.LogPath != "" {
			outcome.LogName = filepath.Base(out.LogPath)
		}
		if out.Err == nil && out.Rec != nil {
			rec := out.Rec
			// Same curation bootstrap as the TUI findings screen: findings
			// below the publish floor are marked (they never reach GitLab),
			// findings at or above the auto-comment threshold arrive
			// accepted, and pending manual comments join the record. Only
			// pending findings qualify either way: an incremental run
			// carries forward already-curated (published, rejected)
			// findings whose states must survive.
			floor := review.Severity(cfg.Publish.MinSeverity)
			for i := range rec.Findings {
				if rec.Findings[i].State == review.StatePending &&
					rec.Findings[i].Severity.Valid() && !rec.Findings[i].Severity.AtLeast(floor) {
					rec.Findings[i].State = review.StateBelowThreshold
				}
			}
			if cfg.Publish.AutoComment {
				for i := range rec.Findings {
					if rec.Findings[i].State == review.StatePending &&
						rec.Findings[i].Severity.AtLeast(review.Severity(cfg.Publish.AutoMinSeverity)) {
						rec.Findings[i].State = review.StateAccepted
					}
				}
			}
			rec.Findings = append(rec.Findings, s.comments.take(key)...)
			// TUI parity: auto_comment publishes the accepted findings
			// without further confirmation once the run completes.
			if cfg.Publish.AutoComment {
				outcome.Published, outcome.DraftReady = autoPublish(ctx, d, cfg, detail, diffs, rec, run.append)
			}
			if err := d.Results.Save(*rec); err != nil {
				run.append("warning: could not store the review result: " + err.Error())
			}
			if p := d.Results.Path(*rec); p != "" {
				outcome.RecName = filepath.Base(p)
			}
			outcome.Findings = len(rec.Findings)
		}
		run.finish(outcome)
	}()
	return run
}

// autoPublish posts a fresh record's accepted findings straight to GitLab,
// mirroring the TUI's publish.auto_comment behaviour, and updates their
// states in place (the caller saves the record). It reports how many
// findings were posted live (immediate mode) and whether a draft review was
// created and left pending publication.
func autoPublish(ctx context.Context, d *Deps, cfg config.Config, detail gitlabx.MRDetail, diffs []gitlabx.FileDiff, rec *resultstore.Record, emit func(string)) (published int, draftReady bool) {
	var accepted []int
	for i := range rec.Findings {
		if rec.Findings[i].State == review.StateAccepted {
			accepted = append(accepted, i)
		}
	}
	if len(accepted) == 0 {
		return 0, false
	}
	pub, tmplErr := publisher.New(d.Svc, detail, diffs, cfg.Publish)
	if tmplErr != nil {
		emit("warning: comment template ignored: " + tmplErr.Error())
	}
	pub.Draft = cfg.Publish.Mode == "draft"
	// Best-effort: skip findings that already restate a comment on the MR.
	// A fetch error just means duplicate detection is unavailable this run.
	if err := pub.LoadExisting(ctx); err != nil {
		emit("warning: could not check for already-posted comments: " + err.Error())
	}
	emit(fmt.Sprintf("auto-publishing %d accepted finding(s) in %s mode…", len(accepted), cfg.Publish.Mode))
	for _, i := range accepted {
		pctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		state, err := pub.PublishOne(pctx, rec.Findings[i])
		cancel()
		rec.Findings[i].State = state
		if err != nil {
			emit(fmt.Sprintf("warning: publishing %q failed: %v", findingTitle(rec.Findings[i]), err))
			continue
		}
		published++
	}
	if pub.Draft && published > 0 {
		emit(fmt.Sprintf("%d draft note(s) created — publish the review to make them visible", published))
		return 0, true
	}
	emit(fmt.Sprintf("auto-published %d comment(s)", published))
	return published, false
}
