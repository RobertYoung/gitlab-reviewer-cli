package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/config"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review/resultstore"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/secret"
)

func TestParseMRRef(t *testing.T) {
	tests := []struct {
		ref     string
		project string
		iid     int64
		wantErr bool
	}{
		{ref: "group/app!7", project: "group/app", iid: 7},
		{ref: "group/sub/app!123", project: "group/sub/app", iid: 123},
		{ref: "group/app", wantErr: true},   // no !
		{ref: "!7", wantErr: true},          // no project
		{ref: "group/app!", wantErr: true},  // no iid
		{ref: "group/app!x", wantErr: true}, // non-numeric iid
		{ref: "group/app!0", wantErr: true}, // iid must be positive
		{ref: "group/app!-1", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			project, iid, err := parseMRRef(tt.ref)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseMRRef(%q) = %q, %d; want error", tt.ref, project, iid)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if project != tt.project || iid != tt.iid {
				t.Errorf("parseMRRef(%q) = %q, %d; want %q, %d", tt.ref, project, iid, tt.project, tt.iid)
			}
		})
	}
}

func TestParseMRTarget(t *testing.T) {
	tests := []struct {
		name    string
		ref     string
		project string
		iid     int64
		host    string
		wantErr bool
	}{
		{name: "ref form", ref: "group/app!7", project: "group/app", iid: 7},
		{name: "url", ref: "https://gitlab.example.com/group/app/-/merge_requests/123", project: "group/app", iid: 123, host: "gitlab.example.com"},
		{name: "url subgroup", ref: "https://gitlab.example.com/group/sub/app/-/merge_requests/9", project: "group/sub/app", iid: 9, host: "gitlab.example.com"},
		{name: "url with tab", ref: "https://gitlab.example.com/group/app/-/merge_requests/123/diffs", project: "group/app", iid: 123, host: "gitlab.example.com"},
		{name: "url with query and anchor", ref: "https://gitlab.example.com/group/app/-/merge_requests/123?tab=overview#note_1", project: "group/app", iid: 123, host: "gitlab.example.com"},
		{name: "url with port", ref: "http://127.0.0.1:8080/group/app/-/merge_requests/5", project: "group/app", iid: 5, host: "127.0.0.1:8080"},
		{name: "url mixed-case host", ref: "https://GitLab.Example.com/group/app/-/merge_requests/3", project: "group/app", iid: 3, host: "gitlab.example.com"},
		{name: "url not an MR page", ref: "https://gitlab.example.com/group/app/-/issues/1", wantErr: true},
		{name: "url without project", ref: "https://gitlab.example.com/-/merge_requests/1", wantErr: true},
		{name: "url non-numeric iid", ref: "https://gitlab.example.com/group/app/-/merge_requests/new", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			project, iid, host, err := parseMRTarget(tt.ref)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseMRTarget(%q) = %q, %d, %q; want error", tt.ref, project, iid, host)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if project != tt.project || iid != tt.iid || host != tt.host {
				t.Errorf("parseMRTarget(%q) = %q, %d, %q; want %q, %d, %q", tt.ref, project, iid, host, tt.project, tt.iid, tt.host)
			}
		})
	}
}

func TestResolveInstanceForHost(t *testing.T) {
	base := config.Default()
	base.GitLab.Token = "shared"
	work := config.Instance{Name: "work", BaseURL: "https://gitlab.example.com", Token: "glpat-work"}
	personal := config.Instance{Name: "personal", BaseURL: "https://gitlab.com", Token: "glpat-personal"}

	t.Run("no host applies explicit selection rules", func(t *testing.T) {
		cfg := base
		cfg.GitLab.Instances = []config.Instance{work, personal}
		if _, err := resolveInstanceForHost(cfg, ""); err == nil || !strings.Contains(err.Error(), "--instance") {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("no instances, matching base_url", func(t *testing.T) {
		cfg := base
		cfg.GitLab.BaseURL = "https://gitlab.example.com"
		cfg, err := resolveInstanceForHost(cfg, "gitlab.example.com")
		if err != nil {
			t.Fatal(err)
		}
		if cfg.GitLab.BaseURL != "https://gitlab.example.com" {
			t.Errorf("base_url = %q", cfg.GitLab.BaseURL)
		}
	})

	t.Run("no instances, mismatched base_url errors", func(t *testing.T) {
		cfg := base
		cfg.GitLab.BaseURL = "https://gitlab.example.com"
		if _, err := resolveInstanceForHost(cfg, "gitlab.other.com"); err == nil || !strings.Contains(err.Error(), "does not match") {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("host selects the matching instance", func(t *testing.T) {
		cfg := base
		cfg.GitLab.Instances = []config.Instance{work, personal}
		cfg, err := resolveInstanceForHost(cfg, "gitlab.com")
		if err != nil {
			t.Fatal(err)
		}
		if cfg.GitLab.Token != "glpat-personal" {
			t.Errorf("gitlab = %+v", cfg.GitLab)
		}
	})

	t.Run("host overrides a non-matching default_instance", func(t *testing.T) {
		cfg := base
		cfg.GitLab.Instances = []config.Instance{work, personal}
		cfg.GitLab.DefaultInstance = "work"
		cfg, err := resolveInstanceForHost(cfg, "gitlab.com")
		if err != nil {
			t.Fatal(err)
		}
		if cfg.GitLab.Token != "glpat-personal" {
			t.Errorf("gitlab = %+v", cfg.GitLab)
		}
	})

	t.Run("unmatched host errors", func(t *testing.T) {
		cfg := base
		cfg.GitLab.Instances = []config.Instance{work, personal}
		if _, err := resolveInstanceForHost(cfg, "gitlab.other.com"); err == nil || !strings.Contains(err.Error(), "no configured gitlab instance") {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("shared host without default errors", func(t *testing.T) {
		cfg := base
		twin := config.Instance{Name: "twin", BaseURL: "https://gitlab.example.com", Token: "glpat-twin"}
		cfg.GitLab.Instances = []config.Instance{work, twin}
		if _, err := resolveInstanceForHost(cfg, "gitlab.example.com"); err == nil || !strings.Contains(err.Error(), "--instance") {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("shared host with matching default selects it", func(t *testing.T) {
		cfg := base
		twin := config.Instance{Name: "twin", BaseURL: "https://gitlab.example.com", Token: "glpat-twin"}
		cfg.GitLab.Instances = []config.Instance{work, twin}
		cfg.GitLab.DefaultInstance = "twin"
		cfg, err := resolveInstanceForHost(cfg, "gitlab.example.com")
		if err != nil {
			t.Fatal(err)
		}
		if cfg.GitLab.Token != "glpat-twin" {
			t.Errorf("gitlab = %+v", cfg.GitLab)
		}
	})
}

func TestResolveInstanceHeadless(t *testing.T) {
	base := config.Default()
	base.GitLab.Token = "shared"

	t.Run("no instances passes through", func(t *testing.T) {
		cfg, err := resolveInstanceHeadless(base)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.GitLab.BaseURL != base.GitLab.BaseURL {
			t.Errorf("base_url = %q", cfg.GitLab.BaseURL)
		}
	})

	t.Run("single instance selected", func(t *testing.T) {
		cfg := base
		cfg.GitLab.Instances = []config.Instance{
			{Name: "work", BaseURL: "https://gitlab.example.com", Token: "glpat-work"},
		}
		cfg, err := resolveInstanceHeadless(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.GitLab.BaseURL != "https://gitlab.example.com" {
			t.Errorf("gitlab = %+v", cfg.GitLab)
		}
	})

	t.Run("multiple without selection errors instead of prompting", func(t *testing.T) {
		cfg := base
		cfg.GitLab.Instances = []config.Instance{
			{Name: "work", BaseURL: "https://gitlab.example.com", Token: "glpat-work"},
			{Name: "personal", BaseURL: "https://gitlab.com", Token: "glpat-personal"},
		}
		if _, err := resolveInstanceHeadless(cfg); err == nil || !strings.Contains(err.Error(), "--instance") {
			t.Errorf("err = %v", err)
		}
	})
}

func TestReviewCmdFlagValidation(t *testing.T) {
	tests := []struct {
		name   string
		target string
		args   []string
		want   string
	}{
		{"bad publish", "group/app!7", []string{"--publish", "sometimes"}, "--publish"},
		{"bad output", "group/app!7", []string{"--output", "yaml"}, "--output"},
		{"bad ref", "group/app", nil, "expected project!iid"},
		{
			"url host mismatch", "https://gitlab.other.com/group/app/-/merge_requests/7",
			[]string{"--gitlab-base-url", "https://gitlab.example.com"},
			"does not match",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := newRoot(&state{redactor: secret.NewRedactor()})
			args := append([]string{"review", tt.target, "--gitlab-token", "t", "--log-file", filepath.Join(t.TempDir(), "log")}, tt.args...)
			root.SetArgs(args)
			root.SetOut(&bytes.Buffer{})
			root.SetErr(&bytes.Buffer{})
			err := root.Execute()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v; want it to mention %q", err, tt.want)
			}
		})
	}
}

// headlessFixture is the shared harness for headless review command tests:
// an httptest GitLab serving MR group/app!7, a local git origin with the MR
// head, a clone for checkout-mode=path, and a scripted claude reporting one
// major finding on feature.go:1.
type headlessFixture struct {
	srvURL     string
	baseSHA    string
	headSHA    string
	localClone string
	claudePath string

	// posted captures the last discussion POSTed to the fake GitLab, and how
	// many arrived in total.
	posted struct {
		Count    int
		Body     string `json:"body"`
		Position *struct {
			BaseSHA string `json:"base_sha"`
			HeadSHA string `json:"head_sha"`
			NewPath string `json:"new_path"`
			NewLine int    `json:"new_line"`
		} `json:"position"`
	}
}

// args builds the command line for one run against the fixture.
func (f *headlessFixture) args(target string, extra ...string) []string {
	return append([]string{
		"review", target,
		"--gitlab-base-url", f.srvURL,
		"--gitlab-token", "e2e-token",
		"--checkout-mode", "path",
		"--repo-path", f.localClone,
		"--claude-path", f.claudePath,
		"--agents", "bug",
	}, extra...)
}

func newHeadlessFixture(t *testing.T) *headlessFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	// Keep state, config, and cache inside the test: the command derives its
	// reviews directory and default log file from XDG paths.
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	f := &headlessFixture{}

	// --- fixture git origin with an MR head commit, plus a local clone for
	// checkout-mode=path ---
	gitBase := t.TempDir()
	work := filepath.Join(gitBase, "work")
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...) //nolint:gosec // fixed test commands
		if dir != "" {
			cmd.Dir = dir
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("", "init", "-q", "-b", "main", work)
	git(work, "config", "user.email", "t@example.com")
	git(work, "config", "user.name", "T")
	if err := os.WriteFile(filepath.Join(work, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(work, "add", ".")
	git(work, "commit", "-q", "-m", "initial")
	f.baseSHA = git(work, "rev-parse", "HEAD")
	git(work, "checkout", "-q", "-b", "feature")
	if err := os.WriteFile(filepath.Join(work, "feature.go"), []byte("package main // feature\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(work, "add", ".")
	git(work, "commit", "-q", "-m", "feature")
	f.headSHA = git(work, "rev-parse", "HEAD")

	origin := filepath.Join(gitBase, "group", "app.git")
	if err := os.MkdirAll(filepath.Dir(origin), 0o750); err != nil {
		t.Fatal(err)
	}
	git("", "clone", "-q", "--bare", work, origin)
	git(origin, "update-ref", "refs/merge-requests/7/head", f.headSHA)
	f.localClone = filepath.Join(gitBase, "clone")
	git("", "clone", "-q", origin, f.localClone)

	// --- httptest GitLab ---
	featureDiff := "@@ -0,0 +1 @@\n+package main // feature\n"
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v4/projects/{p}/merge_requests/7", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{
			"id": 1, "iid": 7, "project_id": 3,
			"title": "Add feature", "state": "opened",
			"source_branch": "feature", "target_branch": "main", "sha": f.headSHA,
			"references": map[string]any{"full": "group/app!7"},
			"author":     map[string]any{"username": "alice"},
			"web_url":    "https://gitlab.example.com/group/app/-/merge_requests/7",
			"diff_refs":  map[string]any{"base_sha": f.baseSHA, "head_sha": f.headSHA, "start_sha": f.baseSHA},
		})
	})
	mux.HandleFunc("GET /api/v4/projects/{p}/merge_requests/7/diffs", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, []map[string]any{{
			"old_path": "feature.go", "new_path": "feature.go", "new_file": true,
			"diff": featureDiff,
		}})
	})
	mux.HandleFunc("POST /api/v4/projects/{p}/merge_requests/7/discussions", func(w http.ResponseWriter, r *http.Request) {
		f.posted.Count++
		if err := json.NewDecoder(r.Body).Decode(&f.posted); err != nil {
			t.Errorf("decoding posted discussion: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
		writeJSON(t, w, map[string]any{"id": "d1", "notes": []any{}})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	f.srvURL = srv.URL

	// --- scripted claude returning one finding on feature.go:1 ---
	transcript := `{"type":"system","subtype":"init","session_id":"e2e","model":"m"}
{"type":"result","subtype":"success","is_error":false,"result":"{}","session_id":"e2e","total_cost_usd":0.01,"structured_output":{"summary":"looks fine","findings":[{"file":"feature.go","new_line":1,"severity":"major","category":"bug","title":"E2E finding","body":"Found in pass."}]}}
`
	tPath := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(tPath, []byte(transcript), 0o600); err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf("#!/bin/sh\nfor a in \"$@\"; do [ \"$a\" = --version ] && echo 2.1.0 && exit 0; done\ncat >/dev/null\ncat %q\n", tPath)
	f.claudePath = filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(f.claudePath, []byte(script), 0o700); err != nil { //nolint:gosec // test script
		t.Fatal(err)
	}
	return f
}

// TestHeadlessReviewEndToEnd drives the real `review` command — gitlabx
// against an httptest GitLab, a path-mode checkout against a local git
// origin, and the claudecli backend against a scripted claude — and asserts
// the JSON outcome on stdout plus the discussion posted to GitLab.
func TestHeadlessReviewEndToEnd(t *testing.T) {
	f := newHeadlessFixture(t)

	// --- run the real command, once per target form; the MR URL's host
	// (the httptest server) matches --gitlab-base-url. --full keeps the later
	// runs from going incremental against the record the first one stored ---
	targets := []struct{ name, target string }{
		{"ref", "group/app!7"},
		{"url", f.srvURL + "/group/app/-/merge_requests/7"},
	}
	for _, tc := range targets {
		t.Run(tc.name, func(t *testing.T) {
			f.posted.Count, f.posted.Body, f.posted.Position = 0, "", nil

			var stdout, stderr bytes.Buffer
			root := newRoot(&state{redactor: secret.NewRedactor()})
			root.SetArgs(f.args(tc.target, "--publish", "immediate", "--output", "json", "--full"))
			root.SetOut(&stdout)
			root.SetErr(&stderr)
			if err := root.Execute(); err != nil {
				t.Fatalf("review command failed: %v\nstderr:\n%s", err, stderr.String())
			}

			// --- the JSON outcome on stdout ---
			var got struct {
				resultstore.Record
				RecordPath string `json:"record_path"`
			}
			if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
				t.Fatalf("decoding stdout: %v\n%s", err, stdout.String())
			}
			if got.Ref != "group/app!7" || got.Summary != "looks fine" {
				t.Errorf("record: ref=%q summary=%q", got.Ref, got.Summary)
			}
			if len(got.Findings) != 1 || got.Findings[0].File != "feature.go" {
				t.Fatalf("findings: %+v", got.Findings)
			}
			if got.Findings[0].State != review.StatePublished {
				t.Errorf("finding state = %v; want published", got.Findings[0].State)
			}
			if got.RecordPath == "" {
				t.Error("record_path missing")
			} else if _, err := os.Stat(got.RecordPath); err != nil {
				t.Errorf("stored record: %v", err)
			} else {
				// The stored record carries the published state for later
				// reopening.
				data, err := os.ReadFile(got.RecordPath)
				if err != nil {
					t.Fatal(err)
				}
				var rec resultstore.Record
				if err := json.Unmarshal(data, &rec); err != nil {
					t.Fatal(err)
				}
				if len(rec.Findings) != 1 || rec.Findings[0].State != review.StatePublished {
					t.Errorf("stored findings: %+v", rec.Findings)
				}
			}

			// --- the inline discussion landed with the right position ---
			if f.posted.Position == nil {
				t.Fatalf("no position posted; body=%q", f.posted.Body)
			}
			if f.posted.Position.NewPath != "feature.go" || f.posted.Position.NewLine != 1 {
				t.Errorf("position: %+v", f.posted.Position)
			}
			if f.posted.Position.BaseSHA != f.baseSHA || f.posted.Position.HeadSHA != f.headSHA {
				t.Errorf("SHAs: %+v (want %s / %s)", f.posted.Position, f.baseSHA, f.headSHA)
			}
			if !strings.Contains(f.posted.Body, "E2E finding") {
				t.Errorf("body: %q", f.posted.Body)
			}

			// Progress streamed to stderr, not stdout.
			if !strings.Contains(stderr.String(), "finding") {
				t.Errorf("expected progress on stderr, got:\n%s", stderr.String())
			}
		})
	}

	// --- incremental rerun: without --full, the unchanged head means no
	// review passes run, the published finding carries forward, and nothing
	// is posted to GitLab again ---
	t.Run("incremental rerun", func(t *testing.T) {
		f.posted.Count, f.posted.Body, f.posted.Position = 0, "", nil

		var stdout, stderr bytes.Buffer
		root := newRoot(&state{redactor: secret.NewRedactor()})
		root.SetArgs(f.args("group/app!7", "--publish", "immediate", "--output", "json"))
		root.SetOut(&stdout)
		root.SetErr(&stderr)
		if err := root.Execute(); err != nil {
			t.Fatalf("review command failed: %v\nstderr:\n%s", err, stderr.String())
		}

		var got struct {
			resultstore.Record
			RecordPath string `json:"record_path"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
			t.Fatalf("decoding stdout: %v\n%s", err, stdout.String())
		}
		if !strings.Contains(got.Summary, "No reviewable changes") {
			t.Errorf("summary = %q", got.Summary)
		}
		if len(got.Findings) != 1 || got.Findings[0].State != review.StatePublished {
			t.Fatalf("carried findings: %+v", got.Findings)
		}
		if f.posted.Count != 0 {
			t.Errorf("already-published finding re-posted: %+v", f.posted)
		}
		if !strings.Contains(stderr.String(), "unchanged since the last review") {
			t.Errorf("expected the incremental note on stderr, got:\n%s", stderr.String())
		}
	})
}

// TestHeadlessReviewGateExitCode covers gate.min_severity in headless mode:
// blocking findings turn into the distinct gate exit code, a satisfied gate
// exits clean, and both report the gate in the JSON output.
func TestHeadlessReviewGateExitCode(t *testing.T) {
	f := newHeadlessFixture(t)

	run := func(t *testing.T, extra ...string) (gate *gateReport, err error) {
		t.Helper()
		var stdout, stderr bytes.Buffer
		root := newRoot(&state{redactor: secret.NewRedactor()})
		root.SetArgs(f.args("group/app!7", append([]string{"--output", "json"}, extra...)...))
		root.SetOut(&stdout)
		root.SetErr(&stderr)
		err = root.Execute()
		var got struct {
			Gate *gateReport `json:"gate"`
		}
		if jerr := json.Unmarshal(stdout.Bytes(), &got); jerr != nil {
			t.Fatalf("decoding stdout: %v\n%s\nstderr:\n%s", jerr, stdout.String(), stderr.String())
		}
		return got.Gate, err
	}

	t.Run("blocking finding fails the gate", func(t *testing.T) {
		gate, err := run(t, "--gate-min-severity", "major")
		var ee *exitError
		if !errors.As(err, &ee) || ee.code != gateExitCode {
			t.Fatalf("err = %v; want exit code %d", err, gateExitCode)
		}
		if !strings.Contains(err.Error(), "gate failed") {
			t.Errorf("err = %v", err)
		}
		if gate == nil || gate.MinSeverity != "major" || gate.Blocking != 1 || gate.Passed {
			t.Errorf("gate = %+v", gate)
		}
	})

	t.Run("satisfied gate passes", func(t *testing.T) {
		gate, err := run(t, "--gate-min-severity", "critical")
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if gate == nil || gate.Blocking != 0 || !gate.Passed {
			t.Errorf("gate = %+v", gate)
		}
	})

	t.Run("no gate configured reports none", func(t *testing.T) {
		gate, err := run(t)
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if gate != nil {
			t.Errorf("gate = %+v; want omitted", gate)
		}
	})
}

// TestHeadlessReviewPublishFloor covers publish.min_severity: a finding
// below the floor is never posted to GitLab and comes back marked
// below-threshold.
func TestHeadlessReviewPublishFloor(t *testing.T) {
	f := newHeadlessFixture(t)

	var stdout, stderr bytes.Buffer
	root := newRoot(&state{redactor: secret.NewRedactor()})
	root.SetArgs(f.args("group/app!7",
		"--publish", "immediate", "--output", "json", "--publish-min-severity", "critical"))
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	if err := root.Execute(); err != nil {
		t.Fatalf("review command failed: %v\nstderr:\n%s", err, stderr.String())
	}

	if f.posted.Count != 0 {
		t.Errorf("posted %d discussion(s); the major finding is below the critical floor", f.posted.Count)
	}
	var got resultstore.Record
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decoding stdout: %v\n%s", err, stdout.String())
	}
	if len(got.Findings) != 1 || got.Findings[0].State != review.StateBelowThreshold {
		t.Errorf("findings: %+v; want one below-threshold", got.Findings)
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Error(err)
	}
}
