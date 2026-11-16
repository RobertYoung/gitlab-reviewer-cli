package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review/resultstore"
)

// aggReviewer is a fake backend that also aggregates: by default it merges
// every finding it is sent into one group anchored on the first.
type aggReviewer struct {
	*fakeReviewer
	reply func(ctx context.Context, n int) (*review.AggregateReply, error)
	calls []review.AggregateRequest
}

func (a *aggReviewer) Aggregate(ctx context.Context, req review.AggregateRequest, onEvent func(review.Event)) (*review.AggregateReply, error) {
	a.calls = append(a.calls, req)
	var items []map[string]any
	if err := json.Unmarshal(req.Findings, &items); err != nil {
		return nil, err
	}
	onEvent(review.Event{Kind: review.EventStatus, Text: "grouping"})
	if a.reply != nil {
		return a.reply(ctx, len(items))
	}
	return mergeAll(len(items)), nil
}

func mergeAll(n int) *review.AggregateReply {
	var ids []string
	for i := range n {
		ids = append(ids, fmt.Sprintf("%q", fmt.Sprintf("s%d", i+1)))
	}
	out := `{"groups":[{"members":[` + strings.Join(ids, ",") + `],"anchor":"s1","certain":true,"title":"merged","body":"merged body","rationale":"same defect"}]}`
	return &review.AggregateReply{Output: []byte(out), CostUSD: 0.05}
}

func aggRunner(t *testing.T, agg *aggReviewer) Runner {
	t.Helper()
	r := fanOutRunner(t, agg.fakeReviewer, []string{"bug", "security"})
	r.Reviewer = agg
	return r
}

func hasWarning(res *review.Result, substr string) bool {
	for _, w := range res.Warnings {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}

func TestRunAggregatesAcrossAgents(t *testing.T) {
	agg := &aggReviewer{fakeReviewer: &fakeReviewer{}}
	r := aggRunner(t, agg)

	var lines []string
	out := r.Run(context.Background(), gitlabx.MRDetail{}, smallDiffs(), nil, func(s string) { lines = append(lines, s) })
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	fs := out.Result.Findings
	if len(fs) != 1 || fs[0].Title != "merged" || len(fs[0].Sources) != 2 || fs[0].ID != "f001" {
		t.Fatalf("findings = %+v", fs)
	}
	if fs[0].Agent != "bug, security" && fs[0].Agent != "security, bug" {
		t.Errorf("contributing agents = %q", fs[0].Agent)
	}
	if out.Result.CostUSD < 0.2499 || out.Result.CostUSD > 0.2501 {
		t.Errorf("cost = %v, want both passes plus aggregation", out.Result.CostUSD)
	}
	if !hasWarning(out.Result, "aggregation merged 1 duplicate") {
		t.Errorf("warnings = %v", out.Result.Warnings)
	}
	if out.Rec == nil || len(out.Rec.Findings[0].Sources) != 2 {
		t.Errorf("stored record lost the sources: %+v", out.Rec)
	}
	var progress bool
	for _, l := range lines {
		if strings.HasPrefix(l, "[aggregate] done") {
			progress = true
		}
	}
	if !progress {
		t.Errorf("no aggregation progress in %v", lines)
	}
}

func TestRunAggregationFallsBackOnMalformedOutput(t *testing.T) {
	agg := &aggReviewer{fakeReviewer: &fakeReviewer{}, reply: func(context.Context, int) (*review.AggregateReply, error) {
		return &review.AggregateReply{Output: []byte(`{"groups":[{"members":["s1"],"anchor":"s1","certain":true,"title":"t","body":"b","rationale":""}]}`), CostUSD: 0.01}, nil
	}}
	out := aggRunner(t, agg).Run(context.Background(), gitlabx.MRDetail{}, smallDiffs(), nil, nil)
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	if len(out.Result.Findings) != 2 {
		t.Errorf("source results lost: %+v", out.Result.Findings)
	}
	if !hasWarning(out.Result, "aggregation failed, used deterministic deduplication") || !hasWarning(out.Result, `omits finding "s2"`) {
		t.Errorf("warnings = %v", out.Result.Warnings)
	}
}

func TestRunAggregationTimeoutFallsBack(t *testing.T) {
	agg := &aggReviewer{fakeReviewer: &fakeReviewer{}, reply: func(ctx context.Context, _ int) (*review.AggregateReply, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	r := aggRunner(t, agg)
	r.Cfg.Review.Timeout = 20 * time.Millisecond
	out := r.Run(context.Background(), gitlabx.MRDetail{}, smallDiffs(), nil, nil)
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	if len(out.Result.Findings) != 2 || !hasWarning(out.Result, "timed out") {
		t.Errorf("findings = %d, warnings = %v", len(out.Result.Findings), out.Result.Warnings)
	}
}

func TestRunAggregationRespectsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	agg := &aggReviewer{fakeReviewer: &fakeReviewer{}, reply: func(ctx context.Context, _ int) (*review.AggregateReply, error) {
		cancel()
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	out := aggRunner(t, agg).Run(ctx, gitlabx.MRDetail{}, smallDiffs(), nil, nil)
	if !errors.Is(out.Err, context.Canceled) || out.Rec != nil {
		t.Errorf("err = %v, rec = %v; want a cancelled run, not a fallback", out.Err, out.Rec)
	}
}

func TestRunAggregationBudget(t *testing.T) {
	t.Run("reserves a slice of the total", func(t *testing.T) {
		agg := &aggReviewer{fakeReviewer: &fakeReviewer{}}
		r := aggRunner(t, agg)
		r.Cfg.Review.MaxBudgetUSD = 0.6
		out := r.Run(context.Background(), gitlabx.MRDetail{}, smallDiffs(), nil, nil)
		if out.Err != nil {
			t.Fatal(out.Err)
		}
		for _, req := range agg.reqs {
			if req.MaxBudgetUSD < 0.1999 || req.MaxBudgetUSD > 0.2001 {
				t.Errorf("agent %s budget = %v, want a third of the total", req.AgentName, req.MaxBudgetUSD)
			}
		}
		if len(agg.calls) != 1 || agg.calls[0].MaxBudgetUSD < 0.1999 || agg.calls[0].MaxBudgetUSD > 0.2001 {
			t.Errorf("aggregation calls = %+v, want one capped at its slice", agg.calls)
		}
	})
	t.Run("falls back when the passes spent it all", func(t *testing.T) {
		agg := &aggReviewer{fakeReviewer: &fakeReviewer{}}
		r := aggRunner(t, agg)
		r.Cfg.Review.MaxBudgetUSD = 0.15 // the fake passes cost 0.10 each
		out := r.Run(context.Background(), gitlabx.MRDetail{}, smallDiffs(), nil, nil)
		if out.Err != nil {
			t.Fatal(out.Err)
		}
		if len(agg.calls) != 0 {
			t.Errorf("aggregation ran without budget: %+v", agg.calls)
		}
		if len(out.Result.Findings) != 2 || !hasWarning(out.Result, "not enough review budget") {
			t.Errorf("findings = %d, warnings = %v", len(out.Result.Findings), out.Result.Warnings)
		}
	})
}

func TestRunAggregationDisabled(t *testing.T) {
	agg := &aggReviewer{fakeReviewer: &fakeReviewer{findings: []review.Finding{{File: "a.go", Title: "same text", Body: "same text"}}}}
	r := aggRunner(t, agg)
	r.Cfg.Review.Aggregate = false
	r.Cfg.Review.MaxBudgetUSD = 1.0
	out := r.Run(context.Background(), gitlabx.MRDetail{}, smallDiffs(), nil, nil)
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	if len(agg.calls) != 0 {
		t.Error("aggregation ran while disabled")
	}
	if agg.reqs[0].MaxBudgetUSD != 0.5 {
		t.Errorf("budget = %v, want no aggregation slice", agg.reqs[0].MaxBudgetUSD)
	}
	if len(out.Result.Findings) != 1 || !hasWarning(out.Result, "dropped 1 duplicate") {
		t.Errorf("heuristic dedupe not applied: %+v / %v", out.Result.Findings, out.Result.Warnings)
	}
}

func TestRunAggregationKeepsCuratedIncrementalFindings(t *testing.T) {
	agg := &aggReviewer{fakeReviewer: &fakeReviewer{}}
	r := aggRunner(t, agg)
	r.Cfg.Review.Agents = []string{"bug"}
	r.Results = resultstore.NewStore(t.TempDir())
	r.Incremental = true
	prev := previousRecord()
	prev.Findings[0].Body = "hand-edited before publishing"
	if err := r.Results.Save(prev); err != nil {
		t.Fatal(err)
	}
	r.Svc = fakeSvc{compareDiffs: []gitlabx.FileDiff{
		{OldPath: "touched.go", NewPath: "touched.go", Diff: "@@ -5 +5 @@\n-old\n+new\n"},
	}}

	out := r.Run(context.Background(), incrementalDetail("head2"), mrDiffsForIncremental(), nil, nil)
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	fs := out.Result.Findings
	if len(fs) != 1 {
		t.Fatalf("findings = %+v", fs)
	}
	f := fs[0]
	if f.Title != "carried" || f.Body != "hand-edited before publishing" || f.State != review.StatePublished {
		t.Errorf("curated finding rewritten: %+v", f)
	}
	if len(f.Sources) != 2 {
		t.Errorf("fresh duplicate not kept as a source: %+v", f.Sources)
	}
}
