package webui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review"
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

	mu      sync.Mutex
	lines   []string
	subs    map[chan runEvent]struct{}
	done    bool
	err     error
	recName string // stored record file name; "" when the run stored none
	logName string // stored progress log file name
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
	out := &runOutcome{RecName: r.recName, LogName: r.logName}
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

// start launches a review in a server goroutine, mirroring the TUI review
// screen: run through the shared runner, then fold the diff view's pending
// manual comments and the auto-accept threshold into the stored record so
// the findings page opens with the same curation state the TUI would show.
func (s *Server) startRun(d *Deps, instance string, detail gitlabx.MRDetail, diffs []gitlabx.FileDiff, commits []gitlabx.Commit) *reviewRun {
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
	key := mrKey(instance, detail.ProjectPath, detail.IID)

	go func() {
		defer cancel()
		r := runner.Runner{
			Cfg:      cfg,
			Svc:      d.Svc,
			Reviewer: d.Reviewer,
			Checkout: d.Checkout,
			Logs:     d.Logs,
			Results:  d.Results,
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
			// at or above the auto-comment threshold arrive accepted, and
			// pending manual comments join the record.
			if cfg.Publish.AutoComment {
				for i := range rec.Findings {
					if rec.Findings[i].Severity.AtLeast(review.Severity(cfg.Publish.AutoMinSeverity)) {
						rec.Findings[i].State = review.StateAccepted
					}
				}
			}
			rec.Findings = append(rec.Findings, s.comments.take(key)...)
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
