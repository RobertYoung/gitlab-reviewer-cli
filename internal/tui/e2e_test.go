package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/checkout"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/config"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review/claudecli"
)

// TestEndToEndReviewFlow wires the real components together — gitlabx
// against an httptest GitLab, checkout against a local git origin, the
// claudecli backend against a scripted claude — and drives the actual
// screens from review trigger to a published inline discussion.
func TestEndToEndReviewFlow(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	// --- fixture git origin with an MR head commit ---
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
	baseSHA := git(work, "rev-parse", "HEAD")
	git(work, "checkout", "-q", "-b", "feature")
	if err := os.WriteFile(filepath.Join(work, "feature.go"), []byte("package main // feature\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(work, "add", ".")
	git(work, "commit", "-q", "-m", "feature")
	headSHA := git(work, "rev-parse", "HEAD")

	origin := filepath.Join(gitBase, "group", "app.git")
	if err := os.MkdirAll(filepath.Dir(origin), 0o750); err != nil {
		t.Fatal(err)
	}
	git("", "clone", "-q", "--bare", work, origin)
	git(origin, "update-ref", "refs/merge-requests/7/head", headSHA)

	// --- httptest GitLab ---
	featureDiff := "@@ -0,0 +1 @@\n+package main // feature\n"
	var postedBody struct {
		Body     string `json:"body"`
		Position *struct {
			BaseSHA string `json:"base_sha"`
			HeadSHA string `json:"head_sha"`
			NewPath string `json:"new_path"`
			NewLine int    `json:"new_line"`
		} `json:"position"`
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v4/projects/{p}/merge_requests/7", func(w http.ResponseWriter, r *http.Request) {
		writeJSONResp(t, w, map[string]any{
			"id": 1, "iid": 7, "project_id": 3,
			"title": "Add feature", "state": "opened",
			"source_branch": "feature", "target_branch": "main", "sha": headSHA,
			"references": map[string]any{"full": "group/app!7"},
			"author":     map[string]any{"username": "alice"},
			"web_url":    "https://gitlab.example.com/group/app/-/merge_requests/7",
			"diff_refs":  map[string]any{"base_sha": baseSHA, "head_sha": headSHA, "start_sha": baseSHA},
		})
	})
	mux.HandleFunc("GET /api/v4/projects/{p}/merge_requests/7/diffs", func(w http.ResponseWriter, r *http.Request) {
		writeJSONResp(t, w, []map[string]any{{
			"old_path": "feature.go", "new_path": "feature.go", "new_file": true,
			"diff": featureDiff,
		}})
	})
	mux.HandleFunc("GET /api/v4/projects/{p}/merge_requests/7/discussions", func(w http.ResponseWriter, r *http.Request) {
		writeJSONResp(t, w, []map[string]any{})
	})
	mux.HandleFunc("POST /api/v4/projects/{p}/merge_requests/7/discussions", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&postedBody); err != nil {
			t.Errorf("decoding posted discussion: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
		writeJSONResp(t, w, map[string]any{"id": "d1", "notes": []any{}})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// --- scripted claude returning one finding on feature.go:1 ---
	transcript := `{"type":"system","subtype":"init","session_id":"e2e","model":"m"}
{"type":"result","subtype":"success","is_error":false,"result":"{}","session_id":"e2e","total_cost_usd":0.01,"structured_output":{"summary":"ok","findings":[{"file":"feature.go","new_line":1,"severity":"major","category":"bug","title":"E2E finding","body":"Found in pass."}]}}
`
	tPath := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(tPath, []byte(transcript), 0o600); err != nil {
		t.Fatal(err)
	}
	claudeDir := t.TempDir()
	script := fmt.Sprintf("#!/bin/sh\nfor a in \"$@\"; do [ \"$a\" = --version ] && echo 2.1.0 && exit 0; done\ncat >/dev/null\ncat %q\n", tPath)
	if err := os.WriteFile(filepath.Join(claudeDir, "claude"), []byte(script), 0o700); err != nil { //nolint:gosec // test script
		t.Fatal(err)
	}

	// --- real deps, wired like cli/root.go ---
	cfg := config.Default()
	cfg.GitLab.BaseURL = srv.URL
	cfg.GitLab.Token = "e2e-token"
	cfg.GitLab.Projects = []string{"group/app"}
	cfg.Checkout.CacheDir = t.TempDir()
	cfg.Publish.Mode = "immediate"

	svc, err := gitlabx.New(cfg.GitLab.BaseURL, cfg.GitLab.Token, cfg.GitLab.Projects, nil)
	if err != nil {
		t.Fatal(err)
	}
	// checkout fetches from the local origin instead of the httptest URL
	manager, err := checkout.NewManager(cfg.Checkout, "file://"+gitBase, "")
	if err != nil {
		t.Fatal(err)
	}
	backend := &claudecli.Backend{
		ClaudePath: filepath.Join(claudeDir, "claude"),
		Provider:   "anthropic",
		LookupEnv:  func(string) (string, bool) { return "", false },
	}
	deps := Deps{
		Cfg:      cfg,
		Svc:      svc,
		Reviewer: backend,
		Checkout: func(ctx context.Context, mr gitlabx.MRDetail, progress func(string)) (string, func(context.Context) error, error) {
			co, err := manager.Ensure(ctx, mr, progress)
			if err != nil {
				return "", nil, err
			}
			return co.Path, co.Close, nil
		},
	}

	// --- fetch MR detail + diffs through the real service ---
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	detail, err := svc.GetMergeRequest(ctx, "group/app", 7)
	if err != nil {
		t.Fatal(err)
	}
	diffs, err := svc.ListDiffs(ctx, "group/app", 7)
	if err != nil {
		t.Fatal(err)
	}

	// --- run the review screen ---
	run := newReviewRun(deps, *detail, diffs, nil, nil, nil, []string{"bug"})
	var screen Screen = run
	screen, _ = screen.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	screen.Init()

	var result *review.Result
	deadline := time.After(60 * time.Second)
	for result == nil {
		select {
		case msg := <-run.ch:
			if done, ok := msg.(reviewDoneMsg); ok {
				if done.err != nil {
					t.Fatalf("review failed: %v", done.err)
				}
				result = done.result
			}
			screen, _ = screen.Update(msg)
		case <-deadline:
			t.Fatal("review timed out")
		}
	}
	if len(result.Findings) != 1 || result.Findings[0].File != "feature.go" {
		t.Fatalf("findings: %+v", result.Findings)
	}

	// --- curate and publish through the real screens ---
	fs := newFindings(deps, *detail, diffs, result, nil, nil, nil)
	screen = fs
	screen, _ = screen.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	_, _ = screen.Update(key("a")) // accept

	pub := newPublish(deps, *detail, diffs, fs.accepted(), publishOpts{auto: true})
	screen = pub
	screen.Init()
	for pub.phase != phaseDone {
		select {
		case msg := <-pub.ch:
			screen, _ = screen.Update(msg)
		case <-time.After(30 * time.Second):
			t.Fatal("publish timed out")
		}
	}

	// --- the inline discussion landed with the right position ---
	if postedBody.Position == nil {
		t.Fatalf("no position posted; body=%q", postedBody.Body)
	}
	if postedBody.Position.NewPath != "feature.go" || postedBody.Position.NewLine != 1 {
		t.Errorf("position: %+v", postedBody.Position)
	}
	if postedBody.Position.BaseSHA != baseSHA || postedBody.Position.HeadSHA != headSHA {
		t.Errorf("SHAs: %+v (want %s / %s)", postedBody.Position, baseSHA, headSHA)
	}
	if !strings.Contains(postedBody.Body, "E2E finding") {
		t.Errorf("body: %q", postedBody.Body)
	}
	if pub.items[0].State != review.StatePublished {
		t.Errorf("state: %v", pub.items[0].State)
	}
}

func writeJSONResp(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Error(err)
	}
}
