package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/config"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review/resultstore"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/secret"
)

const (
	fakeBase = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	fakeHead = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// fakeGitLab serves MR group/app!7 for the headless MR and stored-review
// commands and records every write request it receives.
type fakeGitLab struct {
	url string

	mu         sync.Mutex
	writes     []string // "METHOD path" of every non-GET request
	bodies     []map[string]any
	failWrites bool
}

func (g *fakeGitLab) recorded() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.writes...)
}

func newFakeGitLab(t *testing.T) *fakeGitLab {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	g := &fakeGitLab{}

	mr := map[string]any{
		"id": 1, "iid": 7, "project_id": 3,
		"title": "Add feature", "description": "Adds the feature.", "state": "opened",
		"source_branch": "feature", "target_branch": "main", "sha": fakeHead,
		"references":             map[string]any{"full": "group/app!7"},
		"author":                 map[string]any{"username": "alice", "name": "Alice"},
		"web_url":                "https://gitlab.example.com/group/app/-/merge_requests/7",
		"diff_refs":              map[string]any{"base_sha": fakeBase, "head_sha": fakeHead, "start_sha": fakeBase},
		"diverged_commits_count": 2,
		"detailed_merge_status":  "need_rebase",
		"head_pipeline":          map[string]any{"status": "failed", "web_url": "https://gitlab.example.com/p/1"},
		"updated_at":             "2026-11-01T10:00:00Z",
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v4/projects/{p}/merge_requests", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, []map[string]any{mr})
	})
	mux.HandleFunc("GET /api/v4/projects/{p}/merge_requests/7", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, mr)
	})
	mux.HandleFunc("GET /api/v4/projects/{p}/merge_requests/7/commits", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, []map[string]any{{"short_id": "bbbbbbb", "title": "feat: add feature", "message": "feat: add feature"}})
	})
	mux.HandleFunc("GET /api/v4/projects/{p}/merge_requests/7/diffs", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, []map[string]any{{
			"old_path": "feature.go", "new_path": "feature.go", "new_file": true,
			"diff": "@@ -0,0 +1,2 @@\n+package main\n+// feature\n",
		}})
	})
	mux.HandleFunc("GET /api/v4/projects/{p}/merge_requests/7/discussions", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, []map[string]any{
			{"id": "d1", "notes": []map[string]any{{"id": 1, "body": "open", "resolvable": true, "resolved": false}}},
			{"id": "d2", "notes": []map[string]any{{"id": 2, "body": "done", "resolvable": true, "resolved": true}}},
			{"id": "d3", "notes": []map[string]any{{"id": 3, "body": "fyi"}}},
		})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		g.mu.Lock()
		g.writes = append(g.writes, r.Method+" "+r.URL.Path)
		g.bodies = append(g.bodies, body)
		fail := g.failWrites
		g.mu.Unlock()
		if fail {
			http.Error(w, `{"message":"boom"}`, http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
		if strings.HasSuffix(r.URL.Path, "/discussions") {
			writeJSON(t, w, map[string]any{"id": "d1", "notes": []any{}})
			return
		}
		writeJSON(t, w, map[string]any{"id": 1})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	g.url = srv.URL
	return g
}

// run executes the CLI against the fake GitLab and returns stdout.
func (g *fakeGitLab) run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	root := newRoot(&state{redactor: secret.NewRedactor()})
	root.SetArgs(append(args, "--gitlab-base-url", g.url, "--gitlab-token", "test-token"))
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	err := root.Execute()
	if strings.Contains(stdout.String()+stderr.String(), "test-token") {
		t.Errorf("token leaked into output:\n%s\n%s", stdout.String(), stderr.String())
	}
	return stdout.String(), err
}

func seedRecord(t *testing.T, started time.Time, findings ...review.Finding) (resultstore.Record, string) {
	t.Helper()
	store := resultstore.NewStore(filepath.Join(config.DefaultStateDir(), "reviews"))
	rec := resultstore.Record{IID: 7, Ref: "group/app!7", Title: "Add feature", Started: started, HeadSHA: fakeHead, Findings: findings}
	if err := store.Save(rec); err != nil {
		t.Fatal(err)
	}
	return rec, filepath.Base(store.Path(rec))
}

func loadSeeded(t *testing.T, rec resultstore.Record) resultstore.Record {
	t.Helper()
	store := resultstore.NewStore(filepath.Join(config.DefaultStateDir(), "reviews"))
	got, err := store.Load(store.Path(rec))
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestMRListJSON(t *testing.T) {
	g := newFakeGitLab(t)
	seedRecord(t, time.Unix(100, 0),
		review.Finding{ID: "f1", Body: "b", State: review.StateAccepted},
		review.Finding{ID: "f2", Body: "b", State: review.StatePending},
	)

	out, err := g.run(t, "mr", "list", "--project", "group/app", "--output", "json")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Page          int  `json:"page"`
		HasMore       bool `json:"has_more"`
		MergeRequests []struct {
			Ref          string `json:"ref"`
			IID          int64  `json:"iid"`
			Author       string `json:"author"`
			HeadSHA      string `json:"head_sha"`
			LatestReview *struct {
				Record string             `json:"record"`
				Stage  string             `json:"stage"`
				Counts resultstore.Counts `json:"counts"`
			} `json:"latest_review"`
		} `json:"merge_requests"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decoding: %v\n%s", err, out)
	}
	if got.Page != 1 || len(got.MergeRequests) != 1 {
		t.Fatalf("got %+v", got)
	}
	m := got.MergeRequests[0]
	if m.Ref != "group/app!7" || m.IID != 7 || m.Author != "alice" || m.HeadSHA != fakeHead {
		t.Errorf("mr = %+v", m)
	}
	if m.LatestReview == nil || m.LatestReview.Stage != "triage" || m.LatestReview.Counts.Accepted != 1 || m.LatestReview.Counts.Pending != 1 {
		t.Errorf("latest_review = %+v", m.LatestReview)
	}
}

func TestMRListRequiresScope(t *testing.T) {
	g := newFakeGitLab(t)
	if _, err := g.run(t, "mr", "list"); err == nil || !strings.Contains(err.Error(), "--project") {
		t.Errorf("err = %v", err)
	}
}

func TestMRShowJSON(t *testing.T) {
	g := newFakeGitLab(t)
	out, err := g.run(t, "mr", "show", "group/app!7", "--output", "json")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Ref               string `json:"ref"`
		Description       string `json:"description"`
		NeedsRebase       bool   `json:"needs_rebase"`
		RebaseWarning     string `json:"rebase_warning"`
		Pipeline          *struct{ Status string }
		Threads           *int `json:"threads"`
		UnresolvedThreads *int `json:"unresolved_threads"`
		Commits           []struct {
			ShortID string `json:"short_id"`
		} `json:"commits"`
		LatestReview *json.RawMessage `json:"latest_review"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decoding: %v\n%s", err, out)
	}
	if got.Ref != "group/app!7" || got.Description != "Adds the feature." {
		t.Errorf("got %+v", got)
	}
	if !got.NeedsRebase || !strings.Contains(got.RebaseWarning, "2 commit(s) behind main") {
		t.Errorf("rebase: %v %q", got.NeedsRebase, got.RebaseWarning)
	}
	if got.Pipeline == nil || got.Pipeline.Status != "failed" {
		t.Errorf("pipeline = %+v", got.Pipeline)
	}
	if got.Threads == nil || *got.Threads != 2 || *got.UnresolvedThreads != 1 {
		t.Errorf("threads = %v / %v", got.Threads, got.UnresolvedThreads)
	}
	if len(got.Commits) != 1 || got.Commits[0].ShortID != "bbbbbbb" {
		t.Errorf("commits = %+v", got.Commits)
	}
	if got.LatestReview != nil {
		t.Errorf("latest_review = %s; want null", *got.LatestReview)
	}
}

func TestMRApprove(t *testing.T) {
	blocking := review.Finding{ID: "f1", Severity: review.SeverityMajor, Body: "b", State: review.StatePending}
	gateFlags := []string{"--gate-min-severity", "major", "--gate-approvals", "block"}

	t.Run("blocked by the gate in block mode", func(t *testing.T) {
		g := newFakeGitLab(t)
		seedRecord(t, time.Unix(100, 0), blocking)
		_, err := g.run(t, append([]string{"mr", "approve", "group/app!7"}, gateFlags...)...)
		var ee *exitError
		if !errors.As(err, &ee) || ee.code != gateExitCode || !strings.Contains(err.Error(), "approval blocked: 1 finding(s)") {
			t.Fatalf("err = %v; want gate exit %d", err, gateExitCode)
		}
		if w := g.recorded(); len(w) != 0 {
			t.Errorf("writes = %v; want none", w)
		}
	})

	t.Run("warn mode does not block", func(t *testing.T) {
		g := newFakeGitLab(t)
		seedRecord(t, time.Unix(100, 0), blocking)
		out, err := g.run(t, "mr", "approve", "group/app!7", "--gate-min-severity", "major", "--gate-approvals", "warn", "--output", "json")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, `"sha": "`+fakeHead+`"`) {
			t.Errorf("output = %s", out)
		}
		if w := g.recorded(); len(w) != 1 || !strings.HasSuffix(w[0], "/merge_requests/7/approve") {
			t.Errorf("writes = %v", w)
		}
		if sha, _ := g.bodies[0]["sha"].(string); sha != fakeHead {
			t.Errorf("approve sha = %q; want the current head", sha)
		}
	})

	t.Run("rejected finding no longer blocks, explicit sha is passed", func(t *testing.T) {
		g := newFakeGitLab(t)
		rejected := blocking
		rejected.State = review.StateRejected
		seedRecord(t, time.Unix(100, 0), rejected)
		if _, err := g.run(t, append([]string{"mr", "approve", "group/app!7", "--sha", "cafe"}, gateFlags...)...); err != nil {
			t.Fatal(err)
		}
		if sha, _ := g.bodies[0]["sha"].(string); sha != "cafe" {
			t.Errorf("approve sha = %q; want cafe", sha)
		}
	})

	t.Run("unapprove", func(t *testing.T) {
		g := newFakeGitLab(t)
		if _, err := g.run(t, "mr", "unapprove", g.url+"/group/app/-/merge_requests/7"); err != nil {
			t.Fatal(err)
		}
		if w := g.recorded(); len(w) != 1 || !strings.HasSuffix(w[0], "/merge_requests/7/unapprove") {
			t.Errorf("writes = %v", w)
		}
	})
}
