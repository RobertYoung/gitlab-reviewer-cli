package dedupe

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review"
)

// scripted is an aggregation backend that groups findings by a defect key
// looked up from their title. Untitled keys stand alone. Merged groups get
// the title "merged <key>", which also maps back to the key so later
// reconciliation rounds recognise them.
type scripted struct {
	defect    map[string]string // title -> defect key
	anchor    map[string]string // defect key -> title of the anchor member
	uncertain map[string]bool   // defect keys reported with certain=false
	raw       string            // when set, returned verbatim as the output
	err       error
	cost      float64
	calls     []review.AggregateRequest
	inputs    [][]aggregateItem
}

func (s *scripted) Aggregate(ctx context.Context, req review.AggregateRequest, _ func(review.Event)) (*review.AggregateReply, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.calls = append(s.calls, req)
	if s.err != nil {
		return nil, s.err
	}
	var items []aggregateItem
	if err := json.Unmarshal(req.Findings, &items); err != nil {
		return nil, err
	}
	s.inputs = append(s.inputs, items)
	if s.raw != "" {
		return &review.AggregateReply{Output: []byte(s.raw), CostUSD: s.cost}, nil
	}
	var groups []aggregateGroup
	byKey := map[string]int{}
	for _, it := range items {
		key := s.defect[it.Title]
		if key == "" {
			key = strings.TrimPrefix(it.Title, "merged ")
			if key == it.Title {
				groups = append(groups, aggregateGroup{Members: []string{it.ID}, Anchor: it.ID, Certain: true, Title: it.Title, Body: it.Body})
				continue
			}
		}
		i, ok := byKey[key]
		if !ok {
			i = len(groups)
			byKey[key] = i
			groups = append(groups, aggregateGroup{Anchor: it.ID, Certain: !s.uncertain[key], Title: "merged " + key, Body: "combined evidence for " + key, Rationale: "same " + key})
		}
		groups[i].Members = append(groups[i].Members, it.ID)
		if s.anchor[key] == it.Title {
			groups[i].Anchor = it.ID
		}
	}
	out, _ := json.Marshal(map[string]any{"groups": groups})
	return &review.AggregateReply{Output: out, CostUSD: s.cost}, nil
}

func finding(agent, file string, line int, sev review.Severity, title string) review.Finding {
	return review.Finding{Agent: agent, File: file, Line: review.LineRef{NewLine: &line}, Severity: sev, Category: "bug", Title: title, Body: title + " body"}
}

func run(t *testing.T, s *scripted, carried, fresh []review.Finding, opts AggregateOptions) AggregateOutcome {
	t.Helper()
	opts.Backend = s
	out, err := Aggregate(context.Background(), carried, fresh, opts)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func titles(fs []review.Finding) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Title)
	}
	return out
}

func TestAggregateParaphrasedDuplicatesFromDifferentAgents(t *testing.T) {
	s := &scripted{defect: map[string]string{
		"Token expiry never checked":      "expiry",
		"Expired tokens are still valid":  "expiry",
		"Unrelated naming nit in handler": "",
	}}
	fresh := []review.Finding{
		finding("bug", "auth.go", 42, review.SeverityMajor, "Token expiry never checked"),
		finding("security", "auth.go", 42, review.SeverityMajor, "Expired tokens are still valid"),
		finding("style", "auth.go", 10, review.SeverityInfo, "Unrelated naming nit in handler"),
	}
	out := run(t, s, nil, fresh, AggregateOptions{})

	if got := titles(out.Findings); !slices.Equal(got, []string{"merged expiry", "Unrelated naming nit in handler"}) {
		t.Fatalf("titles = %v", got)
	}
	m := out.Findings[0]
	if m.Agent != "bug, security" {
		t.Errorf("contributing agents = %q", m.Agent)
	}
	if len(m.Sources) != 2 || m.Sources[0].Title != "Token expiry never checked" || m.Sources[1].Body != "Expired tokens are still valid body" {
		t.Errorf("original evidence not retained: %+v", m.Sources)
	}
	if m.MergeNote != "same expiry" || m.Body != "combined evidence for expiry" {
		t.Errorf("merged wording: %q / %q", m.Body, m.MergeNote)
	}
	if m.State != review.StatePending {
		t.Errorf("state = %v", m.State)
	}

	var sent []aggregateItem
	_ = json.Unmarshal(s.calls[0].Findings, &sent)
	if len(sent) != 3 || sent[0].ID != "s1" || sent[0].Agent == "" || sent[0].NewLine == nil || sent[0].Severity == "" {
		t.Errorf("agent input lacks ids, attribution or location: %+v", sent)
	}
}

func TestAggregateDifferentAnchorsUseTheChosenSourceAnchor(t *testing.T) {
	s := &scripted{
		defect: map[string]string{"nil deref in caller": "nil", "missing nil check": "nil"},
		anchor: map[string]string{"nil": "missing nil check"},
	}
	fresh := []review.Finding{
		finding("bug", "api/handler.go", 12, review.SeverityMinor, "nil deref in caller"),
		finding("security", "api/parse.go", 30, review.SeverityMinor, "missing nil check"),
	}
	fresh[1].Category = "security"
	out := run(t, s, nil, fresh, AggregateOptions{})

	if len(out.Findings) != 1 {
		t.Fatalf("findings = %v", titles(out.Findings))
	}
	f := out.Findings[0]
	if f.File != "api/parse.go" || *f.Line.NewLine != 30 || f.Category != "security" {
		t.Errorf("anchor not taken from the chosen source: %s:%d %s", f.File, *f.Line.NewLine, f.Category)
	}
}

func TestAggregateComplementaryEvidenceKeepsEverySource(t *testing.T) {
	s := &scripted{defect: map[string]string{"race on cache map": "race", "cache map written without lock": "race", "concurrent writes panic": "race"}}
	fresh := []review.Finding{
		finding("bug", "cache.go", 5, review.SeverityMajor, "race on cache map"),
		finding("performance", "cache.go", 6, review.SeverityMinor, "cache map written without lock"),
		finding("security", "cache.go", 5, review.SeverityMajor, "concurrent writes panic"),
	}
	out := run(t, s, nil, fresh, AggregateOptions{})
	if len(out.Findings) != 1 || len(out.Findings[0].Sources) != 3 {
		t.Fatalf("want one finding with three sources, got %+v", out.Findings)
	}
	if out.Findings[0].Agent != "bug, performance, security" {
		t.Errorf("agents = %q", out.Findings[0].Agent)
	}
}

func TestAggregateUniqueFindingsStandAloneVerbatim(t *testing.T) {
	s := &scripted{}
	fresh := []review.Finding{
		finding("bug", "a.go", 1, review.SeverityMajor, "first"),
		finding("docs", "b.go", 2, review.SeverityInfo, "second"),
	}
	out := run(t, s, nil, fresh, AggregateOptions{})
	if len(out.Findings) != 2 {
		t.Fatalf("findings = %v", titles(out.Findings))
	}
	for i, f := range out.Findings {
		if f.Title != fresh[i].Title || f.Body != fresh[i].Body || f.Agent != fresh[i].Agent || f.Sources != nil || f.MergeNote != "" {
			t.Errorf("singleton %d changed: %+v", i, f)
		}
	}
}

func TestAggregateDistinctDefectsOnTheSameLineStaySeparate(t *testing.T) {
	// Similar enough wording that the deterministic heuristic would merge
	// them; the aggregation agent judges them distinct and nothing
	// downstream overrides that.
	a := finding("bug", "a.go", 7, review.SeverityMajor, "the loop index is off by one here")
	b := finding("performance", "a.go", 7, review.SeverityMajor, "the loop index is off by two here")
	a.Body, b.Body = a.Title, b.Title
	if kept, _ := Findings([]review.Finding{a, b}); len(kept) != 1 {
		t.Fatalf("precondition: heuristic should merge these, kept %d", len(kept))
	}
	out := run(t, &scripted{}, nil, []review.Finding{a, b}, AggregateOptions{})
	if len(out.Findings) != 2 {
		t.Errorf("distinct defects merged: %v", titles(out.Findings))
	}
}

func TestAggregateUncertainMatchesStaySeparate(t *testing.T) {
	s := &scripted{
		defect:    map[string]string{"maybe leak": "leak", "possible leak": "leak"},
		uncertain: map[string]bool{"leak": true},
	}
	fresh := []review.Finding{
		finding("bug", "a.go", 1, review.SeverityMinor, "maybe leak"),
		finding("performance", "a.go", 9, review.SeverityMinor, "possible leak"),
	}
	out := run(t, s, nil, fresh, AggregateOptions{})
	if got := titles(out.Findings); !slices.Equal(got, []string{"maybe leak", "possible leak"}) {
		t.Errorf("uncertain group was merged: %v", got)
	}
}

func TestAggregateRejectsInconsistentGroups(t *testing.T) {
	fresh := []review.Finding{
		finding("bug", "a.go", 1, review.SeverityMinor, "one"),
		finding("docs", "a.go", 2, review.SeverityMinor, "two"),
	}
	group := func(members, anchor string) string {
		return `{"members":` + members + `,"anchor":"` + anchor + `","certain":true,"title":"t","body":"b","rationale":"r"}`
	}
	cases := map[string]struct{ raw, want string }{
		"missing id":        {`{"groups":[` + group(`["s1"]`, "s1") + `]}`, `omits finding "s2"`},
		"invented id":       {`{"groups":[` + group(`["s1","s2","s3"]`, "s1") + `]}`, `unknown finding "s3"`},
		"malformed id":      {`{"groups":[` + group(`["s1","s02"]`, "s1") + `]}`, `unknown finding "s02"`},
		"multiply assigned": {`{"groups":[` + group(`["s1","s2"]`, "s1") + `,` + group(`["s2"]`, "s2") + `]}`, `more than one group`},
		"foreign anchor":    {`{"groups":[` + group(`["s1"]`, "s2") + `,` + group(`["s2"]`, "s2") + `]}`, `not a member`},
		"empty group":       {`{"groups":[` + group(`[]`, "s1") + `,` + group(`["s1","s2"]`, "s1") + `]}`, `empty group`},
		"not json":          {`I merged them for you`, `not valid JSON`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			out, err := Aggregate(context.Background(), nil, fresh, AggregateOptions{Backend: &scripted{raw: tc.raw, cost: 0.02}})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if out.Findings != nil {
				t.Errorf("partial result returned on failure: %+v", out.Findings)
			}
			if out.CostUSD != 0.02 {
				t.Errorf("cost of the failed call lost: %v", out.CostUSD)
			}
		})
	}
}

func TestAggregateConflictingSeveritiesAndSuggestions(t *testing.T) {
	s := &scripted{
		defect: map[string]string{"sql built by concat": "sqli", "query injection": "sqli"},
		anchor: map[string]string{"sqli": "sql built by concat"},
	}
	a := finding("bug", "db.go", 3, review.SeverityMinor, "sql built by concat")
	b := finding("security", "db.go", 3, review.SeverityCritical, "query injection")
	a.Suggestion, b.Suggestion = `db.Query(q, id)`, `db.QueryContext(ctx, q, id)`
	out := run(t, s, nil, []review.Finding{a, b}, AggregateOptions{})

	f := out.Findings[0]
	if f.Severity != review.SeverityCritical {
		t.Errorf("severity = %s, want the highest reported", f.Severity)
	}
	if f.Suggestion != a.Suggestion {
		t.Errorf("suggestion = %q, want the anchor's own", f.Suggestion)
	}
	if !strings.Contains(f.MergeNote, "different fixes") {
		t.Errorf("conflicting suggestions not surfaced: %q", f.MergeNote)
	}
}

func TestAggregateNeverMovesASuggestionToAnotherAnchor(t *testing.T) {
	s := &scripted{
		defect: map[string]string{"x": "d", "y": "d"},
		anchor: map[string]string{"d": "y"},
	}
	a := finding("bug", "a.go", 3, review.SeverityMinor, "x")
	a.Suggestion = "fixed line 3"
	b := finding("bug", "a.go", 8, review.SeverityMinor, "y")
	out := run(t, s, nil, []review.Finding{a, b}, AggregateOptions{})
	if out.Findings[0].Suggestion != "" {
		t.Errorf("suggestion for line 3 attached to line 8: %q", out.Findings[0].Suggestion)
	}
}

func TestAggregateCuratedFindingAbsorbsFreshDuplicates(t *testing.T) {
	s := &scripted{defect: map[string]string{"carried": "d", "fresh restatement": "d"}}
	carried := finding("bug", "a.go", 3, review.SeverityMinor, "carried")
	carried.Body = "my hand-edited wording"
	carried.State = review.StatePublished
	fresh := finding("security", "a.go", 4, review.SeverityCritical, "fresh restatement")
	out := run(t, s, []review.Finding{carried}, []review.Finding{fresh}, AggregateOptions{})

	if len(out.Findings) != 1 {
		t.Fatalf("findings = %v", titles(out.Findings))
	}
	f := out.Findings[0]
	if f.Title != "carried" || f.Body != "my hand-edited wording" || f.State != review.StatePublished || f.Severity != review.SeverityMinor || *f.Line.NewLine != 3 {
		t.Errorf("curated finding rewritten: %+v", f)
	}
	if len(f.Sources) != 2 || f.Agent != "bug, security" {
		t.Errorf("fresh duplicate not recorded as a source: %+v", f)
	}
}

func TestAggregateConflictingCuratedStatesStayVisible(t *testing.T) {
	s := &scripted{defect: map[string]string{"accepted one": "d", "rejected one": "d", "fresh one": "d"}}
	acc := finding("bug", "a.go", 3, review.SeverityMinor, "accepted one")
	acc.State = review.StateAccepted
	rej := finding("security", "a.go", 3, review.SeverityMinor, "rejected one")
	rej.State = review.StateRejected
	fresh := finding("style", "a.go", 3, review.SeverityMinor, "fresh one")
	out := run(t, s, []review.Finding{acc, rej}, []review.Finding{fresh}, AggregateOptions{})

	if got := titles(out.Findings); !slices.Equal(got, []string{"accepted one", "rejected one", "fresh one"}) {
		t.Fatalf("titles = %v", got)
	}
	if out.Findings[0].State != review.StateAccepted || out.Findings[1].State != review.StateRejected {
		t.Errorf("curated states overwritten: %v / %v", out.Findings[0].State, out.Findings[1].State)
	}
	if len(out.Warnings) != 1 || !strings.Contains(out.Warnings[0], "(accepted)") || !strings.Contains(out.Warnings[0], "(rejected)") {
		t.Errorf("conflict not surfaced: %v", out.Warnings)
	}
}

func TestAggregateManualCommentsPassThrough(t *testing.T) {
	s := &scripted{}
	manual := review.Finding{Body: "my own note", Manual: true, State: review.StatePublished}
	out := run(t, s, []review.Finding{manual}, []review.Finding{finding("bug", "a.go", 1, review.SeverityMinor, "x")}, AggregateOptions{})
	if len(out.Findings) != 2 || !out.Findings[0].Manual {
		t.Fatalf("findings = %+v", out.Findings)
	}
	if len(s.calls) != 0 {
		t.Errorf("a single model finding needs no aggregation call, got %d", len(s.calls))
	}
}

func TestAggregateReconcilesDuplicatesAcrossBatches(t *testing.T) {
	// Sorted by location, a.go lands in the first batch of four and z.go
	// in the second, so only the reconciliation call can see they match.
	s := &scripted{defect: map[string]string{"dup in a": "d", "same dup in a": "d", "dup in z": "d"}, cost: 0.01}
	fresh := []review.Finding{
		finding("bug", "a.go", 1, review.SeverityMinor, "dup in a"),
		finding("style", "a.go", 2, review.SeverityMinor, "same dup in a"),
		finding("bug", "b.go", 1, review.SeverityMinor, "unique b"),
		finding("bug", "c.go", 1, review.SeverityMinor, "unique c"),
		finding("security", "z.go", 1, review.SeverityMajor, "dup in z"),
	}
	out := run(t, s, nil, fresh, AggregateOptions{BatchSize: 4})

	if got := titles(out.Findings); !slices.Equal(got, []string{"merged d", "unique b", "unique c"}) {
		t.Fatalf("titles = %v", got)
	}
	// The second batch holds z.go alone and needs no call of its own.
	if len(s.calls) != 2 || len(s.inputs[1]) != 4 {
		t.Errorf("calls = %d, want the first batch plus a reconciliation pass over 4 findings", len(s.calls))
	}
	m := out.Findings[0]
	if m.Severity != review.SeverityMajor || len(m.Sources) != 3 || m.Agent != "bug, style, security" {
		t.Errorf("cross-batch merge: %+v", m)
	}
	if out.CostUSD != 0.02 {
		t.Errorf("cost = %v, want every call counted", out.CostUSD)
	}
}

func TestAggregateBatchesThatCannotShrinkFail(t *testing.T) {
	fresh := []review.Finding{
		finding("bug", "a.go", 1, review.SeverityMinor, "a"),
		finding("bug", "b.go", 1, review.SeverityMinor, "b"),
		finding("bug", "c.go", 1, review.SeverityMinor, "c"),
	}
	_, err := Aggregate(context.Background(), nil, fresh, AggregateOptions{Backend: &scripted{}, BatchSize: 2})
	if err == nil || !strings.Contains(err.Error(), "cannot be reconciled") {
		t.Errorf("err = %v", err)
	}
}

func TestAggregateBudget(t *testing.T) {
	fresh := []review.Finding{
		finding("bug", "a.go", 1, review.SeverityMinor, "a"),
		finding("bug", "b.go", 1, review.SeverityMinor, "b"),
		finding("bug", "c.go", 1, review.SeverityMinor, "c"),
		finding("bug", "d.go", 1, review.SeverityMinor, "d"),
	}
	s := &scripted{cost: 0.25}
	out, err := Aggregate(context.Background(), nil, fresh, AggregateOptions{Backend: s, BudgetUSD: 0.25, BatchSize: 2})
	if !errors.Is(err, ErrAggregateBudget) {
		t.Fatalf("err = %v, want the budget error once the first batch spent it all", err)
	}
	if len(s.calls) != 1 || s.calls[0].MaxBudgetUSD != 0.25 {
		t.Errorf("calls = %+v, want one call capped at the whole budget", s.calls)
	}
	if out.CostUSD != 0.25 {
		t.Errorf("cost = %v", out.CostUSD)
	}
}

func TestAggregateCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fresh := []review.Finding{finding("bug", "a.go", 1, review.SeverityMinor, "a"), finding("bug", "b.go", 1, review.SeverityMinor, "b")}
	if _, err := Aggregate(ctx, nil, fresh, AggregateOptions{Backend: &scripted{}}); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
}

func TestAggregateBackendFailure(t *testing.T) {
	fresh := []review.Finding{finding("bug", "a.go", 1, review.SeverityMinor, "a"), finding("bug", "b.go", 1, review.SeverityMinor, "b")}
	if _, err := Aggregate(context.Background(), nil, fresh, AggregateOptions{Backend: &scripted{err: errors.New("claude timed out")}}); err == nil {
		t.Error("want the backend error")
	}
}
