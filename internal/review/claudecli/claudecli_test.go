package claudecli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/config"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review"
)

// fakeClaude writes a shell script that mimics the claude CLI: it answers
// --version and otherwise streams the given transcript file to stdout.
func fakeClaude(t *testing.T, transcript string, version string, exitCode int) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "claude")
	script := fmt.Sprintf(`#!/bin/sh
for arg in "$@"; do
  if [ "$arg" = "--version" ]; then echo "%s (Claude Code)"; exit 0; fi
done
cat >/dev/null   # drain the prompt from stdin
cat %q
exit %d
`, version, transcript, exitCode)
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil { //nolint:gosec // test script must be executable
		t.Fatal(err)
	}
	return path
}

func testRequest(t *testing.T) review.Request {
	return review.Request{
		RepoPath: t.TempDir(),
		MR: gitlabx.MRDetail{
			MRSummary: gitlabx.MRSummary{IID: 7, Title: "Fix auth", SourceBranch: "fix", TargetBranch: "main"},
		},
		Diffs:      []gitlabx.FileDiff{{OldPath: "a.go", NewPath: "a.go", Diff: "@@ -1 +1 @@\n-x\n+y\n"}},
		Categories: review.AllCategories,
		Timeout:    30 * time.Second,
	}
}

func backend(t *testing.T, transcript string) *Backend {
	t.Helper()
	// The subprocess cwd is the repo path, so the transcript needs an
	// absolute path.
	abs, err := filepath.Abs(filepath.Join("testdata", transcript))
	if err != nil {
		t.Fatal(err)
	}
	return &Backend{
		ClaudePath: fakeClaude(t, abs, "2.1.198", 0),
		Provider:   "anthropic",
		LookupEnv:  func(string) (string, bool) { return "", false },
	}
}

func TestReviewHappyPath(t *testing.T) {
	b := backend(t, "happy.jsonl")
	b.DumpDir = t.TempDir()

	var events []review.Event
	res, err := b.Review(context.Background(), testRequest(t), func(e review.Event) { events = append(events, e) })
	if err != nil {
		t.Fatal(err)
	}

	if res.SessionID != "sess-happy" || res.CostUSD != 0.42 {
		t.Errorf("meta: %+v", res)
	}
	if res.Summary != "The change looks solid overall with one real bug." {
		t.Errorf("summary = %q", res.Summary)
	}
	if len(res.Findings) != 2 {
		t.Fatalf("findings = %d", len(res.Findings))
	}
	f := res.Findings[0]
	if f.File != "internal/auth/token.go" || f.Severity != review.SeverityMajor || f.Category != "bug" {
		t.Errorf("finding 0: %+v", f)
	}
	if f.Line.NewLine == nil || *f.Line.NewLine != 42 || f.Line.OldLine != nil {
		t.Errorf("finding 0 line: %+v", f.Line)
	}
	if !strings.Contains(f.Suggestion, "claims.Expiry == nil") {
		t.Errorf("suggestion: %q", f.Suggestion)
	}
	f2 := res.Findings[1]
	if f2.Line.OldLine == nil || *f2.Line.OldLine != 17 || f2.Line.NewLine != nil {
		t.Errorf("finding 1 line: %+v", f2.Line)
	}

	// progress: init, tool uses with targets, structured-output status
	var kinds []review.EventKind
	var texts []string
	for _, e := range events {
		kinds = append(kinds, e.Kind)
		texts = append(texts, e.Text)
	}
	joined := strings.Join(texts, "|")
	if !strings.Contains(joined, "session started (model claude-fable-5)") {
		t.Errorf("missing init event: %v", texts)
	}
	if !strings.Contains(joined, "Read internal/auth/token.go") || !strings.Contains(joined, "Grep ParseToken") {
		t.Errorf("missing tool events: %v", texts)
	}
	if !strings.Contains(joined, "writing findings…") {
		t.Errorf("missing structured output status: %v", texts)
	}
	_ = kinds

	// transcript dumped for drift debugging
	dumps, _ := filepath.Glob(filepath.Join(b.DumpDir, "review-7-*.jsonl"))
	if len(dumps) != 1 {
		t.Errorf("expected one dump, got %v", dumps)
	}
}

func TestReviewFencedFallback(t *testing.T) {
	res, err := backend(t, "fenced.jsonl").Review(context.Background(), testRequest(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Findings) != 1 || res.Findings[0].File != "a.go" {
		t.Fatalf("findings: %+v", res.Findings)
	}
	if len(res.Warnings) == 0 || !strings.Contains(res.Warnings[0], "structured output missing") {
		t.Errorf("warnings: %v", res.Warnings)
	}
}

func TestReviewMalformedFindingsDropped(t *testing.T) {
	res, err := backend(t, "malformed-findings.jsonl").Review(context.Background(), testRequest(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Findings) != 1 || res.Findings[0].File != "good.go" {
		t.Fatalf("findings: %+v", res.Findings)
	}
	if len(res.Warnings) != 3 {
		t.Errorf("want 3 dropped-finding warnings, got %v", res.Warnings)
	}
}

func TestReviewErrorResult(t *testing.T) {
	_, err := backend(t, "error.jsonl").Review(context.Background(), testRequest(t), nil)
	if err == nil || !strings.Contains(err.Error(), "Credit balance is too low") {
		t.Errorf("want claude error surfaced, got %v", err)
	}
}

func TestReviewNoResultEvent(t *testing.T) {
	_, err := backend(t, "no-result.jsonl").Review(context.Background(), testRequest(t), nil)
	if err == nil || !strings.Contains(err.Error(), "no result event") {
		t.Errorf("want no-result error, got %v", err)
	}
}

func TestReviewRetryEventSurfaced(t *testing.T) {
	var events []review.Event
	res, err := backend(t, "retry.jsonl").Review(context.Background(), testRequest(t), func(e review.Event) { events = append(events, e) })
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Findings) != 0 {
		t.Errorf("findings: %+v", res.Findings)
	}
	found := false
	for _, e := range events {
		if e.Kind == review.EventRetry {
			found = true
		}
	}
	if !found {
		t.Errorf("retry event not forwarded: %+v", events)
	}
}

func TestCheckAvailable(t *testing.T) {
	t.Run("ok", func(t *testing.T) {
		b := backend(t, "happy.jsonl")
		if err := b.CheckAvailable(context.Background()); err != nil {
			t.Errorf("want available, got %v", err)
		}
	})
	t.Run("too old", func(t *testing.T) {
		b := backend(t, "happy.jsonl")
		b.ClaudePath = fakeClaude(t, "testdata/happy.jsonl", "1.0.17", 0)
		err := b.CheckAvailable(context.Background())
		if err == nil || !strings.Contains(err.Error(), "too old") {
			t.Errorf("want too-old error, got %v", err)
		}
	})
	t.Run("missing binary", func(t *testing.T) {
		b := &Backend{ClaudePath: "definitely-not-a-real-binary-xyz"}
		err := b.CheckAvailable(context.Background())
		if err == nil || !strings.Contains(err.Error(), "not found") {
			t.Errorf("want not-found error, got %v", err)
		}
	})
}

func TestSubprocessEnvStripsGitLab(t *testing.T) {
	parent := map[string]string{
		"HOME": "/home/u", "PATH": "/bin", "GITLAB_TOKEN": "glpat-secret",
		"GITLAB_REVIEWER_GITLAB_TOKEN": "glpat-secret", "ANTHROPIC_API_KEY": "sk-ant-x",
		"AWS_ACCESS_KEY_ID": "AKIA",
	}
	lookup := func(k string) (string, bool) { v, ok := parent[k]; return v, ok }

	t.Run("anthropic", func(t *testing.T) {
		b := &Backend{Provider: "anthropic", LookupEnv: lookup, ExtraEnv: map[string]string{"GITLAB_SNEAKY": "x", "MY_PROXY_SETTING": "y"}}
		env := strings.Join(b.subprocessEnv(), "\n")
		if strings.Contains(env, "glpat-secret") {
			t.Error("GitLab token leaked into claude env")
		}
		if !strings.Contains(env, "ANTHROPIC_API_KEY=sk-ant-x") || !strings.Contains(env, "HOME=/home/u") {
			t.Errorf("expected passthrough missing:\n%s", env)
		}
		if strings.Contains(env, "AWS_ACCESS_KEY_ID") {
			t.Error("AWS creds should not pass through for anthropic provider")
		}
		if strings.Contains(env, "GITLAB_SNEAKY") {
			t.Error("GITLAB* extra env must be dropped")
		}
		if !strings.Contains(env, "MY_PROXY_SETTING=y") {
			t.Error("extra env missing")
		}
	})

	t.Run("bedrock", func(t *testing.T) {
		b := &Backend{Provider: "bedrock", LookupEnv: lookup, Bedrock: bedrockCfg("eu-west-2", "work")}
		env := strings.Join(b.subprocessEnv(), "\n")
		for _, want := range []string{"CLAUDE_CODE_USE_BEDROCK=1", "AWS_REGION=eu-west-2", "AWS_PROFILE=work", "AWS_ACCESS_KEY_ID=AKIA"} {
			if !strings.Contains(env, want) {
				t.Errorf("missing %s in:\n%s", want, env)
			}
		}
		if strings.Contains(env, "glpat-secret") {
			t.Error("GitLab token leaked into claude env")
		}
	})
}

func TestReviewTimeout(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "claude")
	// a claude that hangs forever after reading stdin
	script := "#!/bin/sh\nfor a in \"$@\"; do [ \"$a\" = --version ] && echo 2.1.0 && exit 0; done\ncat >/dev/null\nsleep 60\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil { //nolint:gosec // test script must be executable
		t.Fatal(err)
	}
	b := &Backend{ClaudePath: path, Provider: "anthropic", LookupEnv: func(string) (string, bool) { return "", false }}

	req := testRequest(t)
	req.Timeout = 300 * time.Millisecond
	start := time.Now()
	_, err := b.Review(context.Background(), req, nil)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("want timeout error, got %v", err)
	}
	if time.Since(start) > 15*time.Second {
		t.Errorf("timeout took too long: %s", time.Since(start))
	}
}

func bedrockCfg(region, profile string) config.Bedrock {
	return config.Bedrock{Region: region, Profile: profile}
}

func TestBuildArgsToolPolicy(t *testing.T) {
	req := review.Request{MaxBudgetUSD: 2.5}

	find := func(args []string, flag string) string {
		for i, a := range args {
			if a == flag && i+1 < len(args) {
				return args[i+1]
			}
		}
		return ""
	}

	t.Run("default is read-only without agents", func(t *testing.T) {
		args := (&Backend{Model: "m"}).buildArgs(req)
		if got := find(args, "--tools"); got != "Read,Grep,Glob" {
			t.Errorf("tools = %q", got)
		}
		disallowed := find(args, "--disallowedTools")
		for _, want := range []string{"Bash", "Edit", "Write", "Task"} {
			if !strings.Contains(disallowed, want) {
				t.Errorf("disallowed missing %s: %q", want, disallowed)
			}
		}
		if got := find(args, "--max-budget-usd"); got != "2.5" {
			t.Errorf("budget = %q", got)
		}
	})

	t.Run("use_agents grants Task but stays read-only", func(t *testing.T) {
		args := (&Backend{UseAgents: true}).buildArgs(req)
		if got := find(args, "--tools"); got != "Read,Grep,Glob,Task" {
			t.Errorf("tools = %q", got)
		}
		disallowed := find(args, "--disallowedTools")
		if strings.Contains(disallowed, "Task") {
			t.Errorf("Task must not be denied when agents are enabled: %q", disallowed)
		}
		// mutating/exec tools stay denied so subagents are read-only too
		for _, want := range []string{"Bash", "Edit", "Write", "WebFetch"} {
			if !strings.Contains(disallowed, want) {
				t.Errorf("disallowed missing %s: %q", want, disallowed)
			}
		}
	})
}
