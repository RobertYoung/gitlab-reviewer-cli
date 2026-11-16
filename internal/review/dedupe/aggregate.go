package dedupe

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review"
)

// DefaultBatchSize bounds how many findings one aggregation call sees.
// ponytail: counted in findings, not tokens; findings are a few sentences
// each, so 150 stays well inside the model's context. Measure tokens if
// agents start emitting long bodies.
const DefaultBatchSize = 150

// AggregateOptions configures one aggregation phase.
type AggregateOptions struct {
	Backend review.Aggregator
	// BudgetUSD caps the spend of every call in the phase together; zero
	// means uncapped.
	BudgetUSD float64
	// BatchSize defaults to DefaultBatchSize.
	BatchSize int
	OnEvent   func(review.Event)
}

// AggregateOutcome is a completed aggregation phase.
type AggregateOutcome struct {
	Findings []review.Finding
	Warnings []string
	CostUSD  float64
}

// ErrAggregateBudget means the phase ran out of budget before every batch
// could be consolidated.
var ErrAggregateBudget = errors.New("not enough review budget left for aggregation")

// unit is one finding moving through aggregation rounds: the canonical
// finding shown for curation, and the original findings it stands for.
type unit struct {
	f       review.Finding
	sources []review.Finding
	curated bool
	order   int
}

// Aggregate asks the aggregation agent to group carried (already curated)
// and fresh findings by underlying defect, and merges each confident group
// into one finding that keeps every original in Sources. Curated findings
// are never rewritten: one absorbs fresh duplicates verbatim, and a group
// holding several is left unmerged with a warning for human resolution.
// Manual comments pass through untouched.
//
// More findings than BatchSize are consolidated in batches first, then the
// batches' canonical findings go through one reconciliation call so
// duplicates split across batches still meet; if they still exceed
// BatchSize the phase fails rather than leave duplicates unreconciled. Any
// failure
// (malformed or inconsistent output, budget, timeout, cancellation) returns
// an error and no findings: the caller keeps the source results and falls
// back to the deterministic path. The outcome's cost is valid either way.
func Aggregate(ctx context.Context, carried, fresh []review.Finding, opts AggregateOptions) (AggregateOutcome, error) {
	if opts.BatchSize < 1 {
		opts.BatchSize = DefaultBatchSize
	}
	if opts.OnEvent == nil {
		opts.OnEvent = func(review.Event) {}
	}
	var out AggregateOutcome
	var units, manual []unit
	for i, f := range append(append([]review.Finding(nil), carried...), fresh...) {
		u := unit{f: f, sources: expand(f), curated: i < len(carried), order: i}
		if f.Manual {
			manual = append(manual, u)
			continue
		}
		units = append(units, u)
	}
	// Neighbouring findings land in the same batch, where duplicates are
	// most likely.
	slices.SortStableFunc(units, func(a, b unit) int {
		return cmp.Or(strings.Compare(a.f.File, b.f.File), cmp.Compare(lineOf(a.f), lineOf(b.f)))
	})

	if len(units) > opts.BatchSize {
		batches := chunk(units, opts.BatchSize)
		var next []unit
		for i, batch := range batches {
			opts.OnEvent(review.Event{Kind: review.EventStatus, Text: fmt.Sprintf("batch %d/%d: %d finding(s)", i+1, len(batches), len(batch))})
			merged, err := aggregateBatch(ctx, batch, &out, opts)
			if err != nil {
				return AggregateOutcome{CostUSD: out.CostUSD}, err
			}
			next = append(next, merged...)
		}
		if len(next) > opts.BatchSize {
			return AggregateOutcome{CostUSD: out.CostUSD}, fmt.Errorf("%d findings remain after batching, more than the aggregation batch size of %d, so duplicates across batches cannot be reconciled", len(next), opts.BatchSize)
		}
		opts.OnEvent(review.Event{Kind: review.EventStatus, Text: fmt.Sprintf("reconciling %d finding(s) across batches", len(next))})
		units = next
	}
	units, err := aggregateBatch(ctx, units, &out, opts)
	if err != nil {
		return AggregateOutcome{CostUSD: out.CostUSD}, err
	}

	units = append(units, manual...)
	slices.SortFunc(units, func(a, b unit) int { return cmp.Compare(a.order, b.order) })
	for _, u := range units {
		out.Findings = append(out.Findings, u.f)
	}
	return out, nil
}

// aggregateItem is one finding as the aggregation agent sees it.
type aggregateItem struct {
	ID         string `json:"id"`
	Agent      string `json:"agent,omitempty"`
	File       string `json:"file"`
	OldFile    string `json:"old_file,omitempty"`
	NewLine    *int   `json:"new_line"`
	OldLine    *int   `json:"old_line"`
	Severity   string `json:"severity"`
	Category   string `json:"category"`
	Title      string `json:"title"`
	Body       string `json:"body"`
	Suggestion string `json:"suggestion,omitempty"`
}

type aggregateGroup struct {
	Members   []string `json:"members"`
	Anchor    string   `json:"anchor"`
	Certain   bool     `json:"certain"`
	Title     string   `json:"title"`
	Body      string   `json:"body"`
	Rationale string   `json:"rationale"`
}

// aggregateBatch runs one aggregation call over batch and returns the
// resulting units, adding the call's cost and warnings to out.
func aggregateBatch(ctx context.Context, batch []unit, out *AggregateOutcome, opts AggregateOptions) ([]unit, error) {
	if len(batch) < 2 {
		return batch, nil
	}
	req := review.AggregateRequest{}
	if opts.BudgetUSD > 0 {
		req.MaxBudgetUSD = opts.BudgetUSD - out.CostUSD
		if req.MaxBudgetUSD <= 0 {
			return nil, ErrAggregateBudget
		}
	}
	items := make([]aggregateItem, len(batch))
	for i, u := range batch {
		items[i] = aggregateItem{
			ID: inputID(i), Agent: u.f.Agent, File: u.f.File, OldFile: u.f.OldFile,
			NewLine: u.f.Line.NewLine, OldLine: u.f.Line.OldLine,
			Severity: string(u.f.Severity), Category: string(u.f.Category),
			Title: u.f.Title, Body: u.f.Body, Suggestion: u.f.Suggestion,
		}
	}
	var err error
	if req.Findings, err = json.Marshal(items); err != nil {
		return nil, err
	}
	reply, err := opts.Backend.Aggregate(ctx, req, opts.OnEvent)
	if err != nil {
		return nil, err
	}
	out.CostUSD += reply.CostUSD
	groups, err := parseGroups(reply.Output, len(batch))
	if err != nil {
		return nil, err
	}
	var units []unit
	for _, g := range groups {
		members := make([]unit, len(g.Members))
		for i, id := range g.Members {
			members[i] = batch[indexOf(id)]
		}
		slices.SortFunc(members, func(a, b unit) int { return cmp.Compare(a.order, b.order) })
		if len(members) == 1 || !g.Certain {
			units = append(units, members...)
			continue
		}
		var curated []unit
		for _, m := range members {
			if m.curated {
				curated = append(curated, m)
			}
		}
		if len(curated) > 1 {
			units = append(units, members...)
			if w := curatedConflict(curated); !slices.Contains(out.Warnings, w) {
				out.Warnings = append(out.Warnings, w)
			}
			continue
		}
		units = append(units, merge(members, batch[indexOf(g.Anchor)], g))
	}
	return units, nil
}

// parseGroups decodes the agent's reply and checks it covers the n input
// ids exactly: every id in one group, no unknown ids, anchors drawn from
// their own group.
func parseGroups(data []byte, n int) ([]aggregateGroup, error) {
	var reply struct {
		Groups []aggregateGroup `json:"groups"`
	}
	if err := json.Unmarshal(data, &reply); err != nil {
		return nil, fmt.Errorf("aggregation output is not valid JSON: %w", err)
	}
	seen := make([]bool, n)
	for _, g := range reply.Groups {
		if len(g.Members) == 0 {
			return nil, errors.New("aggregation output has an empty group")
		}
		for _, id := range g.Members {
			i := indexOf(id)
			if i < 0 || i >= n {
				return nil, fmt.Errorf("aggregation output references unknown finding %q", id)
			}
			if seen[i] {
				return nil, fmt.Errorf("aggregation output assigns finding %q to more than one group", id)
			}
			seen[i] = true
		}
		if !slices.Contains(g.Members, g.Anchor) {
			return nil, fmt.Errorf("aggregation anchor %q is not a member of its group", g.Anchor)
		}
	}
	for i, ok := range seen {
		if !ok {
			return nil, fmt.Errorf("aggregation output omits finding %q", inputID(i))
		}
	}
	return reply.Groups, nil
}

// merge folds a confident group into one unit. A single curated member is
// kept verbatim, absorbing the fresh duplicates as sources. Otherwise the
// canonical finding takes its location, category and suggestion from the
// anchor, its wording from the agent, and the highest severity any source
// reported.
func merge(members []unit, anchor unit, g aggregateGroup) unit {
	u := unit{order: members[0].order}
	var agents, suggestions []string
	for _, m := range members {
		u.sources = append(u.sources, m.sources...)
		u.order = min(u.order, m.order)
		if m.curated {
			u.curated = true
			anchor = m
		}
	}
	f := anchor.f
	for _, s := range u.sources {
		for a := range strings.SplitSeq(s.Agent, ", ") {
			if a != "" && !slices.Contains(agents, a) {
				agents = append(agents, a)
			}
		}
		if s.Suggestion != "" && !slices.Contains(suggestions, s.Suggestion) {
			suggestions = append(suggestions, s.Suggestion)
		}
		if !u.curated && severityRank[s.Severity] > severityRank[f.Severity] {
			f.Severity = s.Severity
		}
	}
	if !u.curated {
		if t, b := strings.TrimSpace(g.Title), strings.TrimSpace(g.Body); t != "" && b != "" {
			f.Title, f.Body = t, b
		}
		f.State = review.StatePending
	}
	f.Agent = strings.Join(agents, ", ")
	f.Sources = u.sources
	f.MergeNote = strings.TrimSpace(g.Rationale)
	if len(suggestions) > 1 {
		f.MergeNote = strings.TrimSpace(f.MergeNote + " The sources suggest different fixes, compare them before publishing.")
	}
	u.f = f
	return u
}

// expand returns the original findings f stands for: its sources when an
// earlier run already merged it, otherwise f itself.
func expand(f review.Finding) []review.Finding {
	if len(f.Sources) > 0 {
		return f.Sources
	}
	return []review.Finding{f}
}

func curatedConflict(curated []unit) string {
	parts := make([]string, len(curated))
	for i, c := range curated {
		parts[i] = fmt.Sprintf("%s (%s)", location(c.f), c.f.State)
	}
	return "possible duplicates kept separate because each was already curated, resolve them by hand: " + strings.Join(parts, ", ")
}

func location(f review.Finding) string {
	if n := lineOf(f); n > 0 {
		return fmt.Sprintf("%s:%d %q", f.File, n, f.Title)
	}
	return fmt.Sprintf("%s %q", f.File, f.Title)
}

func lineOf(f review.Finding) int {
	switch {
	case f.Line.NewLine != nil:
		return *f.Line.NewLine
	case f.Line.OldLine != nil:
		return *f.Line.OldLine
	}
	return 0
}

func chunk(units []unit, size int) [][]unit {
	var out [][]unit
	for len(units) > size {
		out = append(out, units[:size])
		units = units[size:]
	}
	return append(out, units)
}

func inputID(i int) string { return fmt.Sprintf("s%d", i+1) }

// indexOf is the inverse of inputID; -1 for anything it did not produce.
func indexOf(id string) int {
	var n int
	if _, err := fmt.Sscanf(id, "s%d", &n); err != nil || inputID(n-1) != id {
		return -1
	}
	return n - 1
}
