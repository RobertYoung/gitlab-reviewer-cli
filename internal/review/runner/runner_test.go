package runner

import (
	"context"
	"errors"
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

// fakeSvc satisfies gitlabx.Service for the one method the runner calls.
type fakeSvc struct{ gitlabx.Service }

func (fakeSvc) GetMergeRequestTemplate(context.Context, any) (string, error) { return "", nil }

// fakeReviewer records requests and can fail per agent, tracking the
// maximum number of concurrent Review calls.
type fakeReviewer struct {
	mu       sync.Mutex
	reqs     []review.Request
	inflight atomic.Int32
	maxIn    atomic.Int32
	fail     map[string]bool
	delay    time.Duration
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
	return &review.Result{
		Summary:  "summary from " + req.AgentName,
		Findings: []review.Finding{{ID: "f001", File: "a.go", Title: "t-" + req.AgentName, Body: "b"}},
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

func TestRunSeverityHintInPrompt(t *testing.T) {
	a := agents.Agent{Name: "x", Prompt: "P", Severity: review.SeverityMajor}
	if got := agentPrompt(a); !strings.Contains(got, "major") {
		t.Errorf("prompt missing severity hint: %q", got)
	}
	if got := agentPrompt(agents.Agent{Name: "x", Prompt: "P"}); got != "P" {
		t.Errorf("prompt = %q", got)
	}
}
