package runner

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/config"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review/agents"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review/resultstore"
)

func testCfg() config.Config {
	cfg := config.Default()
	cfg.Review.MaxDiffKB = 1
	cfg.Review.Exclude = []string{"**/go.sum"}
	return cfg
}

func TestBuildRequestsSkipHandling(t *testing.T) {
	repo := t.TempDir()
	small := "@@ -1 +1 @@\n+ok\n"
	oversize := "@@ -1 +1 @@\n+" + strings.Repeat("x", 2*1024) + "\n"
	diffs := []gitlabx.FileDiff{
		{OldPath: "a.go", NewPath: "a.go", Diff: small},
		{OldPath: "go.sum", NewPath: "go.sum", Diff: small},
		{OldPath: "big.go", NewPath: "big.go", Diff: oversize},
		{OldPath: "huge.sql", NewPath: "huge.sql", TooLarge: true},
	}

	reqs, info, warnings, err := BuildRequests(testCfg(), gitlabx.MRDetail{}, diffs, nil, "", repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 1 {
		t.Fatalf("reqs = %d, want 1", len(reqs))
	}
	req := reqs[0]
	if len(req.Diffs) != 1 || req.Diffs[0].NewPath != "a.go" {
		t.Errorf("inline diffs: %+v", req.Diffs)
	}
	if len(req.Excluded) != 1 || req.Excluded[0] != "go.sum" {
		t.Errorf("excluded = %v", req.Excluded)
	}
	if len(req.Unavailable) != 1 || req.Unavailable[0] != "huge.sql" {
		t.Errorf("unavailable = %v", req.Unavailable)
	}
	if len(req.DiffFiles) != 1 || req.DiffFiles[0].Path != "big.go" {
		t.Fatalf("diff files = %v", req.DiffFiles)
	}

	// The oversized diff must be readable from inside the checkout.
	data, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(req.DiffFiles[0].DiffPath))) //nolint:gosec // test path inside t.TempDir()
	if err != nil {
		t.Fatalf("reading diff file: %v", err)
	}
	if !strings.Contains(string(data), "+++ b/big.go") || !strings.Contains(string(data), oversize) {
		t.Errorf("diff file content missing header or diff:\n%.120s", data)
	}

	// Config exclusions and on-disk diffs are informational; only the
	// GitLab-truncated file warrants a persisted warning.
	if len(info) != 2 {
		t.Errorf("info = %v, want excluded + oversized lines", info)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "GitLab returned no diff") {
		t.Errorf("warnings = %v", warnings)
	}
}

func TestBuildRequestsOnlyOversized(t *testing.T) {
	repo := t.TempDir()
	oversize := "@@ -1 +1 @@\n+" + strings.Repeat("x", 2*1024) + "\n"
	diffs := []gitlabx.FileDiff{{OldPath: "big.go", NewPath: "big.go", Diff: oversize}}

	reqs, _, _, err := BuildRequests(testCfg(), gitlabx.MRDetail{}, diffs, nil, "", repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 1 || len(reqs[0].Diffs) != 0 || len(reqs[0].DiffFiles) != 1 {
		t.Errorf("want one pass with only an on-disk diff, got %+v", reqs)
	}
}

func TestBuildRequestsNothingReviewable(t *testing.T) {
	diffs := []gitlabx.FileDiff{{OldPath: "go.sum", NewPath: "go.sum", Diff: "@@ -1 +1 @@\n+x\n"}}
	if _, _, _, err := BuildRequests(testCfg(), gitlabx.MRDetail{}, diffs, nil, "", t.TempDir()); err == nil {
		t.Error("want an error when every file is excluded")
	}
}

func TestBuildRequestsMultiPassDiffFilesOnce(t *testing.T) {
	repo := t.TempDir()
	half := "@@ -1 +1 @@\n+" + strings.Repeat("x", 600) + "\n"
	oversize := "@@ -1 +1 @@\n+" + strings.Repeat("x", 2*1024) + "\n"
	diffs := []gitlabx.FileDiff{
		{OldPath: "a.go", NewPath: "a.go", Diff: half},
		{OldPath: "b.go", NewPath: "b.go", Diff: half},
		{OldPath: "big.go", NewPath: "big.go", Diff: oversize},
	}

	reqs, _, warnings, err := BuildRequests(testCfg(), gitlabx.MRDetail{}, diffs, nil, "", repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 2 {
		t.Fatalf("reqs = %d, want 2 passes", len(reqs))
	}
	if len(reqs[0].DiffFiles) != 1 || len(reqs[1].DiffFiles) != 0 {
		t.Errorf("on-disk diffs must join the first pass only: %v / %v", reqs[0].DiffFiles, reqs[1].DiffFiles)
	}
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "2 passes") {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings = %v, want a multi-pass note", warnings)
	}
}

// fakeSvc satisfies gitlabx.Service for the methods the runner calls.
type fakeSvc struct {
	gitlabx.Service
	compareDiffs []gitlabx.FileDiff
	compareErr   error
}

func (fakeSvc) GetMergeRequestTemplate(context.Context, any) (string, error) { return "", nil }

func (f fakeSvc) CompareRevisions(context.Context, any, string, string) ([]gitlabx.FileDiff, error) {
	return f.compareDiffs, f.compareErr
}

// fakeReviewer records requests and can fail per agent, tracking the
// maximum number of concurrent Review calls.
type fakeReviewer struct {
	mu       sync.Mutex
	reqs     []review.Request
	inflight atomic.Int32
	maxIn    atomic.Int32
	fail     map[string]bool
	delay    time.Duration
	// findings overrides the single default finding each pass returns.
	findings []review.Finding
}

func (f *fakeReviewer) Name() string                         { return "fake" }
func (f *fakeReviewer) CheckAvailable(context.Context) error { return nil }
func (f *fakeReviewer) Review(_ context.Context, req review.Request, onEvent func(review.Event)) (*review.Result, error) {
	cur := f.inflight.Add(1)
	defer f.inflight.Add(-1)
	for {
		max := f.maxIn.Load()
		if cur <= max || f.maxIn.CompareAndSwap(max, cur) {
			break
		}
	}
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	f.mu.Unlock()
	if onEvent != nil {
		onEvent(review.Event{Kind: review.EventStatus, Text: "working"})
	}
	if f.fail[req.AgentName] {
		return nil, errors.New("boom")
	}
	findings := f.findings
	if findings == nil {
		findings = []review.Finding{{ID: "f001", File: "a.go", Title: "t-" + req.AgentName, Body: "b"}}
	}
	return &review.Result{
		Summary:  "summary from " + req.AgentName,
		Findings: append([]review.Finding(nil), findings...),
		CostUSD:  0.10,
	}, nil
}

func fanOutRunner(t *testing.T, rev *fakeReviewer, names []string) Runner {
	t.Helper()
	cfg := config.Default()
	cfg.Review.Agents = names
	return Runner{
		Cfg:      cfg,
		Svc:      fakeSvc{},
		Reviewer: rev,
		Checkout: func(_ context.Context, _ gitlabx.MRDetail, _ func(string)) (string, func(context.Context) error, error) {
			return t.TempDir(), func(context.Context) error { return nil }, nil
		},
	}
}

// TestRunPerAgentModel checks the per-agent model precedence the runner
// applies when specialising each agent's request: the AgentModels override
// wins, then the agent's frontmatter model, then cfg.Review.Model.
func TestRunPerAgentModel(t *testing.T) {
	rev := &fakeReviewer{}
	r := fanOutRunner(t, rev, []string{"bug", "security", "schema"})
	r.Cfg.Review.Model = "sonnet" // the run-wide default
	// A custom agent that declares its own model in frontmatter.
	r.Catalog = agents.NewCatalog("").WithProjectFiles([]agents.File{{
		Name:    "schema.md",
		Content: []byte("---\nname: schema\ndescription: Schema checks\nmodel: haiku\n---\nCheck the schema.\n"),
	}})
	// The picker's override for one agent takes precedence over everything.
	r.AgentModels = map[string]string{"bug": "opus"}

	out := r.Run(context.Background(), gitlabx.MRDetail{}, smallDiffs(), nil, nil)
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	got := map[string]string{}
	for _, req := range rev.reqs {
		got[req.AgentName] = req.Model
	}
	want := map[string]string{
		"bug":      "opus",   // AgentModels override
		"schema":   "haiku",  // frontmatter model
		"security": "sonnet", // cfg.Review.Model default
	}
	if !maps.Equal(got, want) {
		t.Fatalf("per-agent models: got %v, want %v", got, want)
	}
}

func smallDiffs() []gitlabx.FileDiff {
	return []gitlabx.FileDiff{{OldPath: "a.go", NewPath: "a.go", Diff: "@@ -1 +1 @@\n+ok\n"}}
}

func TestRunFanOutPerAgent(t *testing.T) {
	rev := &fakeReviewer{}
	r := fanOutRunner(t, rev, []string{"bug", "security"})
	r.Cfg.Review.MaxBudgetUSD = 1.0

	var lines []string
	out := r.Run(context.Background(), gitlabx.MRDetail{}, smallDiffs(), nil, func(s string) { lines = append(lines, s) })
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	if len(rev.reqs) != 2 {
		t.Fatalf("review calls = %d, want 2", len(rev.reqs))
	}
	seen := map[string]review.Request{}
	for _, req := range rev.reqs {
		seen[req.AgentName] = req
	}
	for _, name := range []string{"bug", "security"} {
		req, ok := seen[name]
		if !ok {
			t.Fatalf("no request for agent %s (got %v)", name, seen)
		}
		if req.AgentPrompt == "" {
			t.Errorf("agent %s: empty prompt", name)
		}
		if len(req.Categories) != 1 || string(req.Categories[0]) != name {
			t.Errorf("agent %s: categories %v", name, req.Categories)
		}
		if req.MaxBudgetUSD != 0.5 {
			t.Errorf("agent %s: budget %v, want the total split evenly", name, req.MaxBudgetUSD)
		}
	}
	if len(out.Result.Findings) != 2 {
		t.Fatalf("findings = %d, want 2", len(out.Result.Findings))
	}
	agentsSeen := map[string]bool{}
	for _, f := range out.Result.Findings {
		agentsSeen[f.Agent] = true
	}
	if !agentsSeen["bug"] || !agentsSeen["security"] {
		t.Errorf("finding attribution: %+v", out.Result.Findings)
	}
	var prefixed bool
	for _, l := range lines {
		if strings.HasPrefix(l, "[bug] ") || strings.HasPrefix(l, "[security] ") {
			prefixed = true
		}
	}
	if !prefixed {
		t.Errorf("no agent-prefixed progress lines in %v", lines)
	}
}

func TestRunConcurrencyCap(t *testing.T) {
	rev := &fakeReviewer{delay: 30 * time.Millisecond}
	r := fanOutRunner(t, rev, []string{"bug", "security", "performance", "docs", "style", "design"})
	r.Cfg.Review.AgentConcurrency = 2

	out := r.Run(context.Background(), gitlabx.MRDetail{}, smallDiffs(), nil, nil)
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	if got := rev.maxIn.Load(); got > 2 {
		t.Errorf("max in-flight reviews = %d, want <= 2", got)
	}
	if len(rev.reqs) != 6 {
		t.Errorf("review calls = %d, want 6", len(rev.reqs))
	}
}

func TestRunPartialAgentFailure(t *testing.T) {
	rev := &fakeReviewer{fail: map[string]bool{"security": true}}
	r := fanOutRunner(t, rev, []string{"bug", "security"})

	out := r.Run(context.Background(), gitlabx.MRDetail{}, smallDiffs(), nil, nil)
	if out.Err != nil {
		t.Fatalf("partial failure must salvage: %v", out.Err)
	}
	if len(out.Result.Findings) != 1 || out.Result.Findings[0].Agent != "bug" {
		t.Errorf("findings: %+v", out.Result.Findings)
	}
	var warned bool
	for _, w := range out.Result.Warnings {
		if strings.Contains(w, "agent security") && strings.Contains(w, "failed") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("warnings missing failed agent: %v", out.Result.Warnings)
	}
}

func TestRunAllAgentsFail(t *testing.T) {
	rev := &fakeReviewer{fail: map[string]bool{"bug": true, "security": true}}
	r := fanOutRunner(t, rev, []string{"bug", "security"})
	out := r.Run(context.Background(), gitlabx.MRDetail{}, smallDiffs(), nil, nil)
	if out.Err == nil {
		t.Fatal("expected error when every agent fails")
	}
}

func TestRunUnknownAgentFailsLoudly(t *testing.T) {
	rev := &fakeReviewer{}
	r := fanOutRunner(t, rev, []string{"bug", "nonsense"})
	out := r.Run(context.Background(), gitlabx.MRDetail{}, smallDiffs(), nil, nil)
	if out.Err == nil || !strings.Contains(out.Err.Error(), "nonsense") {
		t.Fatalf("err = %v, want unknown-agent error", out.Err)
	}
}

func TestRunProjectAgents(t *testing.T) {
	rev := &fakeReviewer{}
	repo := t.TempDir()
	dir := filepath.Join(repo, ".gitlab-reviewer", "agents")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "api-compat.md"), []byte("Flag breaking API changes.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "unused.md"), []byte("Never selected.\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	r := fanOutRunner(t, rev, nil)
	r.AgentNames = []string{"api-compat"}
	r.Checkout = func(context.Context, gitlabx.MRDetail, func(string)) (string, func(context.Context) error, error) {
		return repo, func(context.Context) error { return nil }, nil
	}

	var lines []string
	out := r.Run(context.Background(), gitlabx.MRDetail{}, smallDiffs(), nil, func(s string) { lines = append(lines, s) })
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	if len(rev.reqs) != 1 || rev.reqs[0].AgentName != "api-compat" {
		t.Fatalf("requests: %+v", rev.reqs)
	}
	if !strings.Contains(rev.reqs[0].AgentPrompt, "Flag breaking API changes.") {
		t.Errorf("project agent prompt not used: %q", rev.reqs[0].AgentPrompt)
	}
	var noted bool
	for _, l := range lines {
		if strings.Contains(l, `"unused"`) {
			noted = true
		}
	}
	if !noted {
		t.Errorf("expected a note about the unselected repo agent, got %v", lines)
	}
}

func intp(n int) *int { return &n }

// incrementalDetail is an MR whose diff refs distinguish the baseline SHAs.
func incrementalDetail(headSHA string) gitlabx.MRDetail {
	return gitlabx.MRDetail{
		MRSummary: gitlabx.MRSummary{ProjectPath: "group/app", IID: 7, Title: "T"},
		DiffRefs:  gitlabx.DiffRefs{BaseSHA: "base1", HeadSHA: headSHA, StartSHA: "start1"},
	}
}

// previousRecord is a stored review at head1 with one finding that survives
// the delta below (kept.go) and one whose line the delta changes (touched.go:5).
func previousRecord() resultstore.Record {
	return resultstore.Record{
		IID: 7, Ref: "group/app!7", Title: "T", Started: time.Unix(100, 0),
		BaseSHA: "base1", HeadSHA: "head1",
		Findings: []review.Finding{
			{ID: "f001", File: "kept.go", Line: review.LineRef{NewLine: intp(3)}, State: review.StatePublished, Title: "carried"},
			{ID: "f002", File: "touched.go", Line: review.LineRef{NewLine: intp(5)}, State: review.StateAccepted, Title: "stale"},
		},
	}
}

func mrDiffsForIncremental() []gitlabx.FileDiff {
	return []gitlabx.FileDiff{
		{OldPath: "kept.go", NewPath: "kept.go", Diff: "@@ -3 +3 @@\n-y\n+z\n"},
		{OldPath: "touched.go", NewPath: "touched.go", Diff: "@@ -5 +5 @@\n-old\n+new\n"},
	}
}

func TestRunIncrementalReviewsOnlyTheDelta(t *testing.T) {
	rev := &fakeReviewer{}
	r := fanOutRunner(t, rev, []string{"bug"})
	r.Results = resultstore.NewStore(t.TempDir())
	r.Incremental = true
	if err := r.Results.Save(previousRecord()); err != nil {
		t.Fatal(err)
	}
	r.Svc = fakeSvc{compareDiffs: []gitlabx.FileDiff{
		{OldPath: "touched.go", NewPath: "touched.go", Diff: "@@ -5 +5 @@\n-old\n+new\n"},
	}}

	out := r.Run(context.Background(), incrementalDetail("head2"), mrDiffsForIncremental(), nil, nil)
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	// The agent pass sees only the delta, flagged as incremental.
	if len(rev.reqs) != 1 {
		t.Fatalf("review calls = %d, want 1", len(rev.reqs))
	}
	req := rev.reqs[0]
	if len(req.Diffs) != 1 || req.Diffs[0].NewPath != "touched.go" {
		t.Errorf("reviewed diffs: %+v", req.Diffs)
	}
	if !req.Incremental || req.LastReviewedSHA != "head1" {
		t.Errorf("incremental request markers: %+v/%q", req.Incremental, req.LastReviewedSHA)
	}
	// Findings: the carried one first (state intact), then the agent's new
	// one; the stale one is dropped. IDs renumbered to stay unique.
	fs := out.Result.Findings
	if len(fs) != 2 {
		t.Fatalf("findings = %+v", fs)
	}
	if fs[0].Title != "carried" || fs[0].State != review.StatePublished || fs[0].File != "kept.go" {
		t.Errorf("carried finding: %+v", fs[0])
	}
	if fs[1].Title != "t-bug" || fs[1].State != review.StatePending {
		t.Errorf("new finding: %+v", fs[1])
	}
	if fs[0].ID == fs[1].ID {
		t.Errorf("finding IDs collide: %v / %v", fs[0].ID, fs[1].ID)
	}
	// The record is keyed to the new head, ready to baseline the next run.
	if out.Rec == nil || out.Rec.HeadSHA != "head2" || out.Rec.BaseSHA != "base1" {
		t.Errorf("record SHAs: %+v", out.Rec)
	}
	var noted bool
	for _, w := range out.Result.Warnings {
		if strings.Contains(w, "incremental review") && strings.Contains(w, "1 dropped") {
			noted = true
		}
	}
	if !noted {
		t.Errorf("warnings = %v, want an incremental note", out.Result.Warnings)
	}
}

// TestRunIncrementalDropsOldSideAnchors: an incremental pass reviews the
// delta diff, whose old side counts lines of the last reviewed head — not
// of the MR diff's base — so a removed-line anchor from such a pass cannot
// name a GitLab position. The runner strips it (the finding falls back to a
// file-level note) while leaving carried base-relative anchors alone.
func TestRunIncrementalDropsOldSideAnchors(t *testing.T) {
	rev := &fakeReviewer{findings: []review.Finding{
		{ID: "f001", File: "touched.go", Line: review.LineRef{OldLine: intp(5)}, Title: "on a removed line", Body: "b"},
		{ID: "f002", File: "touched.go", Line: review.LineRef{NewLine: intp(5)}, Title: "on a new line", Body: "b"},
	}}
	r := fanOutRunner(t, rev, []string{"bug"})
	r.Results = resultstore.NewStore(t.TempDir())
	r.Incremental = true
	prev := previousRecord()
	prev.Findings = append(prev.Findings, review.Finding{
		ID: "f003", File: "kept.go", Line: review.LineRef{OldLine: intp(9)}, State: review.StateRejected, Title: "carried removed-line",
	})
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
	byTitle := map[string]review.Finding{}
	for _, f := range out.Result.Findings {
		byTitle[f.Title] = f
	}
	if f := byTitle["on a removed line"]; f.Line.OldLine != nil || f.Line.NewLine != nil {
		t.Errorf("old-side anchor not stripped: %+v", f)
	}
	if f := byTitle["on a new line"]; f.Line.NewLine == nil || *f.Line.NewLine != 5 {
		t.Errorf("new-side anchor lost: %+v", f)
	}
	// The carried finding's old line references the MR base and stays.
	if f := byTitle["carried removed-line"]; f.Line.OldLine == nil || *f.Line.OldLine != 9 || f.State != review.StateRejected {
		t.Errorf("carried anchor: %+v", f)
	}
}

func TestRunIncrementalUnchangedHeadSkipsAgents(t *testing.T) {
	rev := &fakeReviewer{}
	r := fanOutRunner(t, rev, []string{"bug"})
	r.Results = resultstore.NewStore(t.TempDir())
	r.Incremental = true
	if err := r.Results.Save(previousRecord()); err != nil {
		t.Fatal(err)
	}

	out := r.Run(context.Background(), incrementalDetail("head1"), mrDiffsForIncremental(), nil, nil)
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	if len(rev.reqs) != 0 {
		t.Fatalf("review calls = %d, want none for an unchanged head", len(rev.reqs))
	}
	if len(out.Result.Findings) != 2 || out.Result.Findings[0].State != review.StatePublished {
		t.Errorf("carried findings: %+v", out.Result.Findings)
	}
	if !strings.Contains(out.Result.Summary, "No reviewable changes") {
		t.Errorf("summary = %q", out.Result.Summary)
	}
}

func TestRunIncrementalFallsBackToFull(t *testing.T) {
	rebasedPrev := previousRecord()
	rebasedPrev.BaseSHA = "otherbase"
	untrackedPrev := previousRecord()
	untrackedPrev.BaseSHA, untrackedPrev.HeadSHA = "", ""

	tests := []struct {
		name   string
		prev   *resultstore.Record
		svc    fakeSvc
		reason string
	}{
		{"no stored review", nil, fakeSvc{}, "no stored review"},
		{"record without SHAs", &untrackedPrev, fakeSvc{}, "predates commit tracking"},
		{"rebased since last review", &rebasedPrev, fakeSvc{}, "rebased"},
		{"stored head unreachable", ptr(previousRecord()), fakeSvc{compareErr: errors.New("404 not found")}, "cannot compare"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rev := &fakeReviewer{}
			r := fanOutRunner(t, rev, []string{"bug"})
			r.Results = resultstore.NewStore(t.TempDir())
			r.Incremental = true
			r.Svc = tt.svc
			if tt.prev != nil {
				if err := r.Results.Save(*tt.prev); err != nil {
					t.Fatal(err)
				}
			}
			var lines []string
			out := r.Run(context.Background(), incrementalDetail("head2"), mrDiffsForIncremental(), nil, func(s string) { lines = append(lines, s) })
			if out.Err != nil {
				t.Fatal(out.Err)
			}
			// Full review: the agent sees the whole MR diff, unmarked.
			if len(rev.reqs) != 1 || len(rev.reqs[0].Diffs) != 2 || rev.reqs[0].Incremental {
				t.Errorf("requests: %+v", rev.reqs)
			}
			var explained bool
			for _, l := range lines {
				if strings.Contains(l, tt.reason) && strings.Contains(l, "full review") {
					explained = true
				}
			}
			if !explained {
				t.Errorf("progress lines %v missing fallback reason %q", lines, tt.reason)
			}
		})
	}
}

func TestRunIncrementalDeltaAllExcluded(t *testing.T) {
	rev := &fakeReviewer{}
	r := fanOutRunner(t, rev, []string{"bug"})
	r.Cfg.Review.Exclude = []string{"**/go.sum"}
	r.Results = resultstore.NewStore(t.TempDir())
	r.Incremental = true
	if err := r.Results.Save(previousRecord()); err != nil {
		t.Fatal(err)
	}
	r.Svc = fakeSvc{compareDiffs: []gitlabx.FileDiff{
		{OldPath: "go.sum", NewPath: "go.sum", Diff: "@@ -1 +1 @@\n-a\n+b\n"},
	}}

	out := r.Run(context.Background(), incrementalDetail("head2"), mrDiffsForIncremental(), nil, nil)
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	if len(rev.reqs) != 0 {
		t.Fatalf("review calls = %d, want none when the delta is fully excluded", len(rev.reqs))
	}
	if len(out.Result.Findings) != 2 {
		t.Errorf("carried findings: %+v", out.Result.Findings)
	}
}

func ptr[T any](v T) *T { return &v }

func TestRunSeverityHintInPrompt(t *testing.T) {
	a := agents.Agent{Name: "x", Prompt: "P", Severity: review.SeverityMajor}
	if got := agentPrompt(a); !strings.Contains(got, "major") {
		t.Errorf("prompt missing severity hint: %q", got)
	}
	if got := agentPrompt(agents.Agent{Name: "x", Prompt: "P"}); got != "P" {
		t.Errorf("prompt = %q", got)
	}
}

func TestRunLocalCloneAgents(t *testing.T) {
	rev := &fakeReviewer{}
	root := t.TempDir()
	localDir := filepath.Join(root, "gitlab.example.com", "group", "app", ".gitlab-reviewer", "agents")
	if err := os.MkdirAll(localDir, 0o750); err != nil {
		t.Fatal(err)
	}
	// An untracked local-only agent, plus a local version of an agent that
	// is also committed at the MR head.
	if err := os.WriteFile(filepath.Join(localDir, "local-only.md"), []byte("Untracked local agent.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "shared.md"), []byte("Stale local prompt.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	worktree := t.TempDir()
	wtDir := filepath.Join(worktree, ".gitlab-reviewer", "agents")
	if err := os.MkdirAll(wtDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wtDir, "shared.md"), []byte("Committed prompt.\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	r := fanOutRunner(t, rev, nil)
	r.Cfg.Checkout.Mode = "root"
	r.Cfg.Checkout.Root = root
	r.Cfg.GitLab.BaseURL = "https://gitlab.example.com"
	r.AgentNames = []string{"local-only", "shared"}
	r.Checkout = func(context.Context, gitlabx.MRDetail, func(string)) (string, func(context.Context) error, error) {
		return worktree, func(context.Context) error { return nil }, nil
	}

	detail := gitlabx.MRDetail{MRSummary: gitlabx.MRSummary{ProjectPath: "group/app"}}
	out := r.Run(context.Background(), detail, smallDiffs(), nil, nil)
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	prompts := map[string]string{}
	for _, req := range rev.reqs {
		prompts[req.AgentName] = req.AgentPrompt
	}
	if !strings.Contains(prompts["local-only"], "Untracked local agent.") {
		t.Errorf("local-only prompt: %q", prompts["local-only"])
	}
	// The committed definition at the MR head shadows the local one.
	if !strings.Contains(prompts["shared"], "Committed prompt.") {
		t.Errorf("shared prompt: %q", prompts["shared"])
	}
}
