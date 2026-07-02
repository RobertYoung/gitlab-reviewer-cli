package gitlabx

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestClient serves canned JSON per URL path and returns a Client
// pointed at it. handlers map path suffix → handler.
func newTestClient(t *testing.T, projects, groups []string, mux *http.ServeMux) *Client {
	t.Helper()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, "test-token", projects, groups)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Fatal(err)
	}
}

func TestListOpenMergeRequests(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/{project}/merge_requests", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("state"); got != "opened" {
			t.Errorf("state = %q", got)
		}
		if got := r.Header.Get("PRIVATE-TOKEN"); got != "test-token" {
			t.Errorf("token header = %q", got)
		}
		w.Header().Set("X-Next-Page", "2")
		writeJSON(t, w, []map[string]any{{
			"id": 1, "iid": 11, "project_id": 7,
			"title": "Fix the frobnicator", "state": "opened", "draft": true,
			"source_branch": "fix", "target_branch": "main", "sha": "abc123",
			"author":     map[string]any{"username": "alice"},
			"updated_at": "2026-07-01T10:00:00Z",
		}})
	})
	mux.HandleFunc("/api/v4/groups/{group}/merge_requests", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, []map[string]any{
			{
				"id": 2, "iid": 5, "project_id": 9,
				"title": "Group MR", "state": "opened",
				"source_branch": "feat", "target_branch": "main",
				"author":     map[string]any{"username": "bob"},
				"references": map[string]any{"full": "platform/svc!5"},
				"updated_at": "2026-07-02T09:00:00Z",
			},
			// duplicate of the project MR: must be deduped by global ID
			{
				"id": 1, "iid": 11, "project_id": 7,
				"title": "Fix the frobnicator", "state": "opened",
				"source_branch": "fix", "target_branch": "main",
				"updated_at": "2026-07-01T10:00:00Z",
			},
		})
	})

	c := newTestClient(t, []string{"group/app"}, []string{"platform"}, mux)
	mrs, hasMore, err := c.ListOpenMergeRequests(context.Background(), MRFilter{}, Page{Number: 1, PerPage: 20})
	if err != nil {
		t.Fatal(err)
	}
	if !hasMore {
		t.Error("hasMore should be true (project source has next page)")
	}
	if len(mrs) != 2 {
		t.Fatalf("got %d MRs, want 2 (deduped): %+v", len(mrs), mrs)
	}
	// newest updated first
	if mrs[0].Title != "Group MR" || mrs[1].Title != "Fix the frobnicator" {
		t.Errorf("wrong order: %q, %q", mrs[0].Title, mrs[1].Title)
	}
	if mrs[0].ProjectPath != "platform/svc" {
		t.Errorf("group MR project path from references = %q", mrs[0].ProjectPath)
	}
	if mrs[1].ProjectPath != "group/app" {
		t.Errorf("project MR path = %q", mrs[1].ProjectPath)
	}
	if !mrs[1].Draft || mrs[1].Author != "alice" || mrs[1].HeadSHA != "abc123" {
		t.Errorf("field mapping wrong: %+v", mrs[1])
	}
}

func TestListOpenMergeRequestsFilter(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/{project}/merge_requests", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("state") != "" {
			t.Errorf("state=all must omit the state param, got %q", q.Get("state"))
		}
		for param, want := range map[string]string{
			"author_username": "alice", "target_branch": "main", "search": "frob",
		} {
			if got := q.Get(param); got != want {
				t.Errorf("%s = %q, want %q", param, got, want)
			}
		}
		writeJSON(t, w, []map[string]any{})
	})
	c := newTestClient(t, []string{"group/app"}, nil, mux)
	_, _, err := c.ListOpenMergeRequests(context.Background(), MRFilter{
		State: "all", AuthorUsername: "alice", TargetBranch: "main", Search: "frob",
	}, Page{Number: 1, PerPage: 20})
	if err != nil {
		t.Fatal(err)
	}
}

func TestGetMergeRequest(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/{project}/merge_requests/11", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{
			"id": 1, "iid": 11, "project_id": 7,
			"title": "Fix", "state": "opened",
			"source_branch": "fix", "target_branch": "main", "sha": "head999",
			"has_conflicts": true,
			"author":        map[string]any{"username": "alice"},
			"diff_refs": map[string]any{
				"base_sha": "base1", "head_sha": "head999", "start_sha": "start1",
			},
		})
	})
	c := newTestClient(t, nil, nil, mux)
	mr, err := c.GetMergeRequest(context.Background(), "group/app", 11)
	if err != nil {
		t.Fatal(err)
	}
	if mr.DiffRefs != (DiffRefs{BaseSHA: "base1", HeadSHA: "head999", StartSHA: "start1"}) {
		t.Errorf("diff refs = %+v", mr.DiffRefs)
	}
	if !mr.HasConflicts || mr.ProjectPath != "group/app" {
		t.Errorf("detail mapping: %+v", mr)
	}
}

func TestListDiffsPaginates(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/{project}/merge_requests/11/diffs", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("page") {
		case "", "1":
			w.Header().Set("X-Next-Page", "2")
			writeJSON(t, w, []map[string]any{{
				"old_path": "a.go", "new_path": "b.go", "renamed_file": true,
				"diff": "@@ -1 +1 @@\n-x\n+y\n",
			}})
		case "2":
			writeJSON(t, w, []map[string]any{{
				"old_path": "c.go", "new_path": "c.go", "generated_file": true, "diff": "",
			}})
		default:
			t.Errorf("unexpected page %q", r.URL.Query().Get("page"))
		}
	})
	c := newTestClient(t, nil, nil, mux)
	diffs, err := c.ListDiffs(context.Background(), "group/app", 11)
	if err != nil {
		t.Fatal(err)
	}
	if len(diffs) != 2 {
		t.Fatalf("got %d diffs, want 2", len(diffs))
	}
	if !diffs[0].RenamedFile || diffs[0].Path() != "a.go → b.go" {
		t.Errorf("diff 0: %+v", diffs[0])
	}
	if !diffs[1].GeneratedFile {
		t.Errorf("diff 1: %+v", diffs[1])
	}
}

func TestGetMergeRequestTemplatePrefersDefault(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/{project}/templates/merge_requests", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, []map[string]any{
			{"key": "Bug", "name": "Bug"},
			{"key": "Default", "name": "Default"},
		})
	})
	mux.HandleFunc("/api/v4/projects/{project}/templates/merge_requests/Default", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{"key": "Default", "name": "Default", "content": "## What\n<!-- describe -->"})
	})
	c := newTestClient(t, nil, nil, mux)
	tmpl, err := c.GetMergeRequestTemplate(context.Background(), "group/app")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tmpl, "## What") {
		t.Errorf("template content = %q", tmpl)
	}
}

func TestGetMergeRequestTemplateNoneConfigured(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/{project}/templates/merge_requests", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, []map[string]any{})
	})
	c := newTestClient(t, nil, nil, mux)
	tmpl, err := c.GetMergeRequestTemplate(context.Background(), "group/app")
	if err != nil {
		t.Fatal(err)
	}
	if tmpl != "" {
		t.Errorf("expected empty template, got %q", tmpl)
	}
}

func TestListDiscussions(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/projects/{project}/merge_requests/11/discussions", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, []map[string]any{{
			"id":              "disc1",
			"individual_note": false,
			"notes": []map[string]any{{
				"id": 100, "body": "please rename", "system": false, "resolved": true,
				"author": map[string]any{"username": "carol"},
				"position": map[string]any{
					"base_sha": "b", "head_sha": "h", "start_sha": "s",
					"old_path": "a.go", "new_path": "a.go", "new_line": 12,
				},
			}},
		}})
	})
	c := newTestClient(t, nil, nil, mux)
	discussions, err := c.ListDiscussions(context.Background(), "group/app", 11)
	if err != nil {
		t.Fatal(err)
	}
	if len(discussions) != 1 || len(discussions[0].Notes) != 1 {
		t.Fatalf("got %+v", discussions)
	}
	note := discussions[0].Notes[0]
	if note.Author != "carol" || !note.Resolved {
		t.Errorf("note mapping: %+v", note)
	}
	if note.Position == nil || note.Position.NewLine == nil || *note.Position.NewLine != 12 || note.Position.OldLine != nil {
		t.Errorf("position mapping: %+v", note.Position)
	}
}

func TestErrorsCarryContext(t *testing.T) {
	mux := http.NewServeMux() // 404 for everything
	c := newTestClient(t, []string{"group/app"}, nil, mux)
	_, _, err := c.ListOpenMergeRequests(context.Background(), MRFilter{}, Page{Number: 1, PerPage: 20})
	if err == nil || !strings.Contains(err.Error(), "group/app") {
		t.Errorf("error should name the project: %v", err)
	}
}
