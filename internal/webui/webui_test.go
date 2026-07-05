package webui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/config"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review/agents"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review/resultstore"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review/runlog"
)

const sampleDiff = "@@ -1,3 +1,4 @@\n package main\n+import \"fmt\"\n \n func main() {\n"

func sampleMR() gitlabx.MRDetail {
	return gitlabx.MRDetail{
		MRSummary: gitlabx.MRSummary{
			ProjectID:    7,
			ProjectPath:  "group/app",
			IID:          5,
			Title:        "Add fmt import",
			Description:  "Imports fmt.",
			State:        "opened",
			Author:       "alice",
			AuthorName:   "Alice Smith",
			SourceBranch: "feature",
			TargetBranch: "main",
			HeadSHA:      "head",
			WebURL:       "https://gitlab.example.com/group/app/-/merge_requests/5",
			UpdatedAt:    time.Now(),
		},
		DiffRefs: gitlabx.DiffRefs{BaseSHA: "base", HeadSHA: "head", StartSHA: "start"},
	}
}

func sampleDiffs() []gitlabx.FileDiff {
	return []gitlabx.FileDiff{{OldPath: "main.go", NewPath: "main.go", Diff: sampleDiff}}
}

type fakeService struct {
	mu             sync.Mutex
	mrs            []gitlabx.MRSummary
	groups         []gitlabx.GroupInfo
	groupProjects  map[string][]gitlabx.ProjectInfo
	memberProjects []gitlabx.ProjectInfo
	mr             *gitlabx.MRDetail  // nil serves sampleMR()
	diffs          []gitlabx.FileDiff // nil serves sampleDiffs()
	discussions    []gitlabx.Discussion
	repoFiles      []gitlabx.RepoFile
	repoFilesByDir map[string][]gitlabx.RepoFile
	repoFilesErr   error
	inline         []string
	notes          []string
	drafts         []string
	publishedAll   bool
	approved       bool
	approvedSHA    string
}

func (f *fakeService) ListOpenMergeRequests(context.Context, gitlabx.MRFilter, gitlabx.Page) ([]gitlabx.MRSummary, bool, error) {
	return f.mrs, false, nil
}

func (f *fakeService) ListGroups(context.Context, string, gitlabx.Page) ([]gitlabx.GroupInfo, bool, error) {
	return f.groups, false, nil
}

func (f *fakeService) ListGroupProjects(_ context.Context, group string, _ string, _ gitlabx.Page) ([]gitlabx.ProjectInfo, bool, error) {
	return f.groupProjects[group], false, nil
}

func (f *fakeService) ListMemberProjects(context.Context, string, gitlabx.Page) ([]gitlabx.ProjectInfo, bool, error) {
	return f.memberProjects, false, nil
}

func (f *fakeService) GetMergeRequest(context.Context, any, int64) (*gitlabx.MRDetail, error) {
	mr := sampleMR()
	if f.mr != nil {
		mr = *f.mr
	}
	return &mr, nil
}

func (f *fakeService) ListDiffs(context.Context, any, int64) ([]gitlabx.FileDiff, error) {
	if f.diffs != nil {
		return f.diffs, nil
	}
	return sampleDiffs(), nil
}

func (f *fakeService) ListCommits(context.Context, any, int64) ([]gitlabx.Commit, error) {
	return []gitlabx.Commit{{ShortID: "abc1234", Title: "add import"}}, nil
}

func (f *fakeService) CompareRevisions(context.Context, any, string, string) ([]gitlabx.FileDiff, error) {
	return nil, errors.New("no comparison available")
}

func (f *fakeService) GetMergeRequestTemplate(context.Context, any) (string, error) { return "", nil }

func (f *fakeService) ListDirectoryFiles(_ context.Context, _ any, dir, _ string) ([]gitlabx.RepoFile, error) {
	if f.repoFilesByDir != nil {
		return f.repoFilesByDir[dir], f.repoFilesErr
	}
	return f.repoFiles, f.repoFilesErr
}

func (f *fakeService) ListDiscussions(context.Context, any, int64) ([]gitlabx.Discussion, error) {
	return f.discussions, nil
}

func (f *fakeService) CreateInlineDiscussion(_ context.Context, _ any, _ int64, body string, _ *gitlabx.Position) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inline = append(f.inline, body)
	return nil
}

func (f *fakeService) CreateNote(_ context.Context, _ any, _ int64, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notes = append(f.notes, body)
	return nil
}

func (f *fakeService) CreateDraftNote(_ context.Context, _ any, _ int64, body string, _ *gitlabx.Position) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.drafts = append(f.drafts, body)
	return nil
}

func (f *fakeService) PublishAllDraftNotes(context.Context, any, int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.publishedAll = true
	return nil
}

func (f *fakeService) GetApprovals(context.Context, any, int64) (*gitlabx.Approvals, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := &gitlabx.Approvals{UserCanApprove: true, UserHasApproved: f.approved}
	if f.approved {
		a.ApprovedBy = []string{"you"}
	}
	return a, nil
}

func (f *fakeService) Approve(_ context.Context, _ any, _ int64, sha string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.approved, f.approvedSHA = true, sha
	return nil
}

func (f *fakeService) Unapprove(context.Context, any, int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.approved = false
	return nil
}

type fakeReviewer struct{ result *review.Result }

func (r *fakeReviewer) Name() string                         { return "fake" }
func (r *fakeReviewer) CheckAvailable(context.Context) error { return nil }
func (r *fakeReviewer) Review(_ context.Context, _ review.Request, onEvent func(review.Event)) (*review.Result, error) {
	onEvent(review.Event{Kind: review.EventStatus, Text: "thinking…"})
	res := *r.result
	res.Findings = append([]review.Finding(nil), r.result.Findings...)
	return &res, nil
}

// recordingReviewer behaves like fakeReviewer but captures every request,
// so tests can assert what the runner passed (e.g. the per-agent model).
type recordingReviewer struct {
	result *review.Result
	mu     sync.Mutex
	reqs   []review.Request
}

func (r *recordingReviewer) Name() string                         { return "fake" }
func (r *recordingReviewer) CheckAvailable(context.Context) error { return nil }
func (r *recordingReviewer) Review(_ context.Context, req review.Request, onEvent func(review.Event)) (*review.Result, error) {
	r.mu.Lock()
	r.reqs = append(r.reqs, req)
	r.mu.Unlock()
	onEvent(review.Event{Kind: review.EventStatus, Text: "thinking…"})
	res := *r.result
	res.Findings = append([]review.Finding(nil), r.result.Findings...)
	return &res, nil
}

func (r *recordingReviewer) requests() []review.Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]review.Request(nil), r.reqs...)
}

func intp(n int) *int { return &n }

func defaultResult() *review.Result {
	return &review.Result{
		Summary: "One issue found.",
		Findings: []review.Finding{{
			ID:       "f001",
			File:     "main.go",
			Line:     review.LineRef{NewLine: intp(2)},
			Severity: review.SeverityMajor,
			Category: review.Category("bug"),
			Title:    "Unused import",
			Body:     "fmt is imported but unused.",
		}},
	}
}

type testEnv struct {
	t      *testing.T
	srv    *Server
	ts     *httptest.Server
	client *http.Client
	svc    *fakeService
	dir    string // reviews dir: run logs and stored records
}

func newTestEnv(t *testing.T, rev review.Reviewer, cfgOpts ...func(*config.Config)) *testEnv {
	t.Helper()
	dir := t.TempDir()
	svc := &fakeService{mrs: []gitlabx.MRSummary{sampleMR().MRSummary}}

	cfg := config.Default()
	cfg.GitLab.Projects = []string{"group/app"}
	for _, opt := range cfgOpts {
		opt(&cfg)
	}

	srv, err := New(Options{
		ReviewsDir: dir,
		MakeDeps: func(string) (*Deps, error) {
			chatter, _ := rev.(review.Chatter)
			return &Deps{
				Cfg:       cfg,
				Svc:       svc,
				Reviewer:  rev,
				Chatter:   chatter,
				Selection: agents.NewSelectionStore(filepath.Join(dir, "selection.json")),
				Logs:      runlog.NewStore(dir),
				Results:   resultstore.NewStore(dir),
				Checkout: func(context.Context, gitlabx.MRDetail, func(string)) (string, func(context.Context) error, error) {
					return t.TempDir(), func(context.Context) error { return nil }, nil
				},
			}, nil
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	// Bootstrap the session cookie via the tokenised launch URL.
	resp, err := client.Get(ts.URL + "/?token=" + srv.Token())
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	_ = resp.Body.Close()

	return &testEnv{t: t, srv: srv, ts: ts, client: client, svc: svc, dir: dir}
}

func (e *testEnv) get(path string) (int, string) {
	e.t.Helper()
	resp, err := e.client.Get(e.ts.URL + path)
	if err != nil {
		e.t.Fatalf("GET %s: %v", path, err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode, string(body)
}

func (e *testEnv) post(path string, form url.Values) (int, string) {
	e.t.Helper()
	resp, err := e.client.PostForm(e.ts.URL+path, form)
	if err != nil {
		e.t.Fatalf("POST %s: %v", path, err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode, string(body)
}

func mrForm(extra url.Values) url.Values {
	form := url.Values{"project": {"group/app"}, "iid": {"5"}}
	for k, vs := range extra {
		form[k] = vs
	}
	return form
}

func TestAuthRequired(t *testing.T) {
	env := newTestEnv(t, &fakeReviewer{result: defaultResult()})

	// No cookie, no token → forbidden.
	plain := &http.Client{}
	resp, err := plain.Get(env.ts.URL + "/i/default/")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("unauthenticated request: got %d, want 403", resp.StatusCode)
	}

	// Wrong token → forbidden.
	resp, err = plain.Get(env.ts.URL + "/?token=wrong")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("wrong token: got %d, want 403", resp.StatusCode)
	}

	// The bootstrapped client works.
	if code, _ := env.get("/i/default/"); code != http.StatusOK {
		t.Fatalf("authenticated request: got %d, want 200", code)
	}
}

func TestCrossOriginPosts(t *testing.T) {
	env := newTestEnv(t, &fakeReviewer{result: defaultResult()})

	post := func(headers map[string]string) int {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, env.ts.URL+"/i/default/mr/comment",
			strings.NewReader(mrForm(url.Values{"body": {"hi"}}).Encode()))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := env.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}

	// Firefox under a strict referrer policy sends Origin: null on
	// same-origin form POSTs; Sec-Fetch-Site must win over it.
	if code := post(map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "null"}); code == http.StatusForbidden {
		t.Fatalf("same-origin POST with Origin: null refused")
	}

	if code := post(map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.example"}); code != http.StatusForbidden {
		t.Fatalf("cross-site POST: got %d, want 403", code)
	}

	// Older browsers without fetch metadata: the Origin fallback applies.
	if code := post(map[string]string{"Origin": "https://evil.example"}); code != http.StatusForbidden {
		t.Fatalf("cross-origin POST without Sec-Fetch-Site: got %d, want 403", code)
	}
}

func TestHomeRedirectsToSingleInstance(t *testing.T) {
	env := newTestEnv(t, &fakeReviewer{result: defaultResult()})
	code, body := env.get("/")
	if code != http.StatusOK || !strings.Contains(body, "Add fmt import") {
		t.Fatalf("home should land on the MR list: %d\n%s", code, body)
	}
}

func TestMRListAndDetail(t *testing.T) {
	env := newTestEnv(t, &fakeReviewer{result: defaultResult()})

	code, body := env.get("/i/default/")
	if code != http.StatusOK || !strings.Contains(body, "Add fmt import") || !strings.Contains(body, "group/app") {
		t.Fatalf("MR list: %d\n%s", code, body)
	}

	code, body = env.get("/i/default/mr?project=group%2Fapp&iid=5")
	if code != http.StatusOK || !strings.Contains(body, "Imports fmt.") || !strings.Contains(body, "Run AI review") {
		t.Fatalf("MR detail: %d\n%s", code, body)
	}
}

func TestMRHeaderLinksAndMarkdownDescription(t *testing.T) {
	env := newTestEnv(t, &fakeReviewer{result: defaultResult()})
	mr := sampleMR()
	mr.Description = "Imports **fmt** for printing.\n\n<script>alert(1)</script>"
	env.svc.mr = &mr

	// Both the overview and the diff page show the metadata line with each
	// part linked to GitLab, and the description rendered from markdown.
	for _, page := range []string{
		"/i/default/mr?project=group%2Fapp&iid=5",
		"/i/default/mr/diff?project=group%2Fapp&iid=5",
	} {
		code, body := env.get(page)
		if code != http.StatusOK {
			t.Fatalf("%s: %d", page, code)
		}
		for _, want := range []string{
			"<strong>fmt</strong>",
			`href="https://gitlab.example.com/group/app/-/merge_requests/5" target="_blank" rel="noopener">group/app!5</a>`,
			`href="https://gitlab.example.com/alice" target="_blank" rel="noopener">Alice Smith (@alice)</a>`,
			`href="https://gitlab.example.com/group/app/-/tree/feature" target="_blank" rel="noopener">feature</a>`,
			`href="https://gitlab.example.com/group/app/-/tree/main" target="_blank" rel="noopener">main</a>`,
		} {
			if !strings.Contains(body, want) {
				t.Fatalf("%s missing %q:\n%s", page, want, body)
			}
		}
		if strings.Contains(body, "<script>alert") {
			t.Fatalf("%s renders raw HTML from the description:\n%s", page, body)
		}
	}
}

func TestScopePickerWhenNothingConfigured(t *testing.T) {
	env := newTestEnv(t, &fakeReviewer{result: defaultResult()}, func(c *config.Config) {
		c.GitLab.Projects = nil
	})
	env.svc.groups = []gitlabx.GroupInfo{{FullPath: "acme", Description: "Acme group\nsecond line"}}
	env.svc.memberProjects = []gitlabx.ProjectInfo{{PathWithNamespace: "rob/tool"}}
	env.svc.groupProjects = map[string][]gitlabx.ProjectInfo{
		"acme": {{PathWithNamespace: "acme/app", Description: "The app", LastActivity: time.Now()}},
	}

	// With no scope configured the MR list redirects to the picker, which
	// lists the user's groups plus the member-projects entry.
	code, body := env.get("/i/default/")
	if code != http.StatusOK {
		t.Fatalf("scope picker: %d", code)
	}
	for _, want := range []string{"your projects", "mine=1", "acme", "Acme group", "groups=acme"} {
		if !strings.Contains(body, want) {
			t.Fatalf("picker missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "second line") {
		t.Fatalf("description not truncated to its first line:\n%s", body)
	}

	// Drilling into a group lists its projects, linked to the scoped MR list.
	code, body = env.get("/i/default/browse?group=acme")
	if code != http.StatusOK || !strings.Contains(body, "acme/app") || !strings.Contains(body, "projects=acme%2Fapp") {
		t.Fatalf("group projects: %d\n%s", code, body)
	}

	// The member-projects pseudo-entry works the same way.
	code, body = env.get("/i/default/browse?mine=1")
	if code != http.StatusOK || !strings.Contains(body, "rob/tool") {
		t.Fatalf("member projects: %d\n%s", code, body)
	}

	// An ad-hoc scope lists MRs without any configured projects/groups.
	code, body = env.get("/i/default/?groups=acme")
	if code != http.StatusOK || !strings.Contains(body, "Add fmt import") {
		t.Fatalf("scoped MR list: %d\n%s", code, body)
	}
}

func TestDiffPageRendersLinesAndComments(t *testing.T) {
	env := newTestEnv(t, &fakeReviewer{result: defaultResult()})

	code, body := env.get("/i/default/mr/diff?project=group%2Fapp&iid=5")
	if code != http.StatusOK {
		t.Fatalf("diff: %d", code)
	}
	for _, want := range []string{"main.go", `data-new="2"`, "@@ -1,3 +1,4 @@", "add-comment"} {
		if !strings.Contains(body, want) {
			t.Fatalf("diff page missing %q:\n%s", want, body)
		}
	}

	// Add a line comment, see it inline, then delete it.
	code, _ = env.post("/i/default/mr/comment", mrForm(url.Values{
		"file": {"main.go"}, "new": {"2"}, "body": {"why fmt?"},
	}))
	if code != http.StatusOK {
		t.Fatalf("comment add: %d", code)
	}
	_, body = env.get("/i/default/mr/diff?project=group%2Fapp&iid=5")
	if !strings.Contains(body, "why fmt?") {
		t.Fatalf("pending comment not rendered:\n%s", body)
	}
	pending := env.srv.comments.list(mrKey("default", "group/app", 5))
	if len(pending) != 1 || pending[0].Line.NewLine == nil || *pending[0].Line.NewLine != 2 {
		t.Fatalf("stored comment wrong: %+v", pending)
	}
	env.post("/i/default/mr/comment/delete", mrForm(url.Values{"id": {pending[0].ID}}))
	if left := env.srv.comments.list(mrKey("default", "group/app", 5)); len(left) != 0 {
		t.Fatalf("comment not deleted: %+v", left)
	}
}

func TestDiffPageGroupsDiscussionsIntoThreads(t *testing.T) {
	env := newTestEnv(t, &fakeReviewer{result: defaultResult()})
	env.svc.discussions = []gitlabx.Discussion{
		{ID: "d1", Notes: []gitlabx.Note{
			{Author: "alice", Body: "looks wrong", Position: &gitlabx.Position{NewPath: "main.go", NewLine: intp(2)}},
			{Author: "bob", Body: "agreed"},
		}},
		{ID: "d2", Notes: []gitlabx.Note{
			{Author: "carol", Body: "old nit", Resolved: true, Position: &gitlabx.Position{NewPath: "main.go", NewLine: intp(1)}},
		}},
	}

	code, body := env.get("/i/default/mr/diff?project=group%2Fapp&iid=5")
	if code != http.StatusOK {
		t.Fatalf("diff: %d", code)
	}
	// The unresolved discussion renders as one expanded thread holding both notes.
	if !strings.Contains(body, `class="thread" open`) {
		t.Fatalf("unresolved thread not open:\n%s", body)
	}
	for _, want := range []string{"looks wrong", "agreed", "2 comments"} {
		if !strings.Contains(body, want) {
			t.Fatalf("thread missing %q:\n%s", want, body)
		}
	}
	// The resolved discussion is present but collapsed.
	if !strings.Contains(body, `class="thread resolved"`) || strings.Contains(body, `class="thread resolved" open`) {
		t.Fatalf("resolved thread not collapsed:\n%s", body)
	}
	if !strings.Contains(body, "old nit") {
		t.Fatalf("resolved thread content missing:\n%s", body)
	}

	// The split layout renders the same threads.
	_, body = env.get("/i/default/mr/diff?project=group%2Fapp&iid=5&view=split")
	if !strings.Contains(body, `class="thread" open`) || strings.Contains(body, `class="thread resolved" open`) {
		t.Fatalf("split view threads wrong:\n%s", body)
	}
}

func TestApproveAndUnapprove(t *testing.T) {
	env := newTestEnv(t, &fakeReviewer{result: defaultResult()})

	// The detail page offers approval while the user has not approved.
	code, body := env.get("/i/default/mr?project=group%2Fapp&iid=5")
	if code != http.StatusOK || !strings.Contains(body, "> Approve</button>") {
		t.Fatalf("detail before approving: %d\n%s", code, body)
	}

	// Approving records the MR's head SHA and flips the page to unapprove.
	code, body = env.post("/i/default/mr/approve", mrForm(url.Values{"sha": {"head"}}))
	if code != http.StatusOK {
		t.Fatalf("approve: %d", code)
	}
	if !env.svc.approved || env.svc.approvedSHA != "head" {
		t.Fatalf("approval not recorded: approved=%v sha=%q", env.svc.approved, env.svc.approvedSHA)
	}
	if !strings.Contains(body, "Approved by you") || !strings.Contains(body, ">Unapprove<") {
		t.Fatalf("detail after approving:\n%s", body)
	}

	code, _ = env.post("/i/default/mr/approve", mrForm(url.Values{"action": {"unapprove"}}))
	if code != http.StatusOK || env.svc.approved {
		t.Fatalf("unapprove: %d approved=%v", code, env.svc.approved)
	}
}

// TestApproveGate covers gate.approvals: with a blocking finding in the MR's
// last stored review, warn relabels the approve button and block refuses the
// approval outright — including a direct POST around the disabled button.
func TestApproveGate(t *testing.T) {
	storeBlocking := func(env *testEnv) {
		rec := resultstore.Record{
			IID: 5, Ref: "group/app!5", Started: time.Now(),
			Findings: []review.Finding{{ID: "f1", Severity: review.SeverityMajor, Body: "b"}},
		}
		if err := resultstore.NewStore(env.dir).Save(rec); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("warn", func(t *testing.T) {
		env := newTestEnv(t, &fakeReviewer{result: defaultResult()}, func(c *config.Config) {
			c.Gate.MinSeverity = "major"
			c.Gate.Approvals = "warn"
		})
		storeBlocking(env)

		code, body := env.get("/i/default/mr?project=group%2Fapp&iid=5")
		if code != http.StatusOK || !strings.Contains(body, "1 finding(s) at or above major") {
			t.Fatalf("detail missing gate warning: %d\n%s", code, body)
		}
		if !strings.Contains(body, "Approve anyway") {
			t.Fatalf("approve button not relabelled:\n%s", body)
		}

		// Warn is advisory: the approval still goes through.
		code, _ = env.post("/i/default/mr/approve", mrForm(url.Values{"sha": {"head"}}))
		if code != http.StatusOK || !env.svc.approved {
			t.Fatalf("approve under warn: %d approved=%v", code, env.svc.approved)
		}
	})

	t.Run("block", func(t *testing.T) {
		env := newTestEnv(t, &fakeReviewer{result: defaultResult()}, func(c *config.Config) {
			c.Gate.MinSeverity = "major"
			c.Gate.Approvals = "block"
		})
		storeBlocking(env)

		code, body := env.get("/i/default/mr?project=group%2Fapp&iid=5")
		if code != http.StatusOK || !strings.Contains(body, "approval is blocked") {
			t.Fatalf("detail missing block notice: %d\n%s", code, body)
		}

		code, _ = env.post("/i/default/mr/approve", mrForm(url.Values{"sha": {"head"}}))
		if code != http.StatusConflict || env.svc.approved {
			t.Fatalf("approve under block: %d approved=%v; want %d and no approval", code, env.svc.approved, http.StatusConflict)
		}

		// Rejecting the finding lifts the gate.
		rec := resultstore.Record{
			IID: 5, Ref: "group/app!5", Started: time.Now(),
			Findings: []review.Finding{{ID: "f1", Severity: review.SeverityMajor, Body: "b", State: review.StateRejected}},
		}
		if err := resultstore.NewStore(env.dir).Save(rec); err != nil {
			t.Fatal(err)
		}
		code, _ = env.post("/i/default/mr/approve", mrForm(url.Values{"sha": {"head"}}))
		if code != http.StatusOK || !env.svc.approved {
			t.Fatalf("approve after rejecting: %d approved=%v", code, env.svc.approved)
		}
	})
}

func TestSplitDiffView(t *testing.T) {
	env := newTestEnv(t, &fakeReviewer{result: defaultResult()})

	code, body := env.get("/i/default/mr/diff?project=group%2Fapp&iid=5&view=split")
	if code != http.StatusOK || !strings.Contains(body, `class="diff split"`) {
		t.Fatalf("split diff: %d\n%s", code, body)
	}
	// The added line sits on the right with its own anchor button.
	if !strings.Contains(body, `data-new="2"`) || !strings.Contains(body, `class="code add"`) {
		t.Fatalf("split diff missing the added line's cell:\n%s", body)
	}
	// The unified page offers the split toggle and vice versa.
	if !strings.Contains(body, "view=unified") {
		t.Fatalf("split page missing unified toggle:\n%s", body)
	}
	_, body = env.get("/i/default/mr/diff?project=group%2Fapp&iid=5")
	if !strings.Contains(body, "view=split") || strings.Contains(body, `class="diff split"`) {
		t.Fatalf("unified page toggle wrong:\n%s", body)
	}
}

func TestSplitLinesPairing(t *testing.T) {
	diff := "@@ -1,3 +1,3 @@\n ctx1\n-old1\n-old2\n+new1\n ctx2\n"
	lines := parseDiffLines(gitlabx.FileDiff{OldPath: "f.txt", NewPath: "f.txt", Diff: diff}, nil, nil)
	rows := splitLines(lines)
	if len(rows) != 5 {
		t.Fatalf("got %d rows, want 5: %+v", len(rows), rows)
	}
	if rows[0].Hunk == "" {
		t.Fatalf("first row should be the hunk header: %+v", rows[0])
	}
	if rows[1].Left.Kind != "ctx" || rows[1].Left.Num != 1 || rows[1].Right.Num != 1 {
		t.Fatalf("context row wrong: %+v", rows[1])
	}
	if rows[2].Left.Kind != "del" || rows[2].Left.Num != 2 || rows[2].Right.Kind != "add" || rows[2].Right.Num != 2 {
		t.Fatalf("paired del/add row wrong: %+v", rows[2])
	}
	if rows[3].Left.Kind != "del" || rows[3].Left.Num != 3 || rows[3].Right.Kind != "" {
		t.Fatalf("unpaired deletion should face an empty cell: %+v", rows[3])
	}
	if rows[4].Left.Kind != "ctx" || rows[4].Right.Num != 3 {
		t.Fatalf("trailing context row wrong: %+v", rows[4])
	}
}

func TestBuildExplorerTree(t *testing.T) {
	files := buildDiffFiles([]gitlabx.FileDiff{
		{NewPath: "internal/tui/app.go", Diff: sampleDiff},
		{NewPath: "README.md", Diff: sampleDiff},
		{NewPath: "internal/config/load.go", Diff: sampleDiff},
	}, nil, nil, nil, false)
	tree := buildExplorer(files)

	// Directories sort before files, alphabetically within each level.
	if len(tree) != 2 || tree[0].Name != "internal" || tree[0].File != nil || tree[1].Name != "README.md" {
		t.Fatalf("root wrong: %+v", tree)
	}
	sub := tree[0].Children
	if len(sub) != 2 || sub[0].Name != "config" || sub[1].Name != "tui" {
		t.Fatalf("internal/ children wrong: %+v", sub)
	}
	leaf := sub[0].Children
	if len(leaf) != 1 || leaf[0].Name != "load.go" || leaf[0].File == nil || leaf[0].File.Index != 2 {
		t.Fatalf("config/ leaf wrong: %+v", leaf)
	}
}

func TestDiffExplorerRendersCollapsibleTree(t *testing.T) {
	env := newTestEnv(t, &fakeReviewer{result: defaultResult()})
	env.svc.diffs = []gitlabx.FileDiff{
		{OldPath: "internal/tui/app.go", NewPath: "internal/tui/app.go", Diff: sampleDiff},
		{OldPath: "README.md", NewPath: "README.md", Diff: sampleDiff},
	}

	code, body := env.get("/i/default/mr/diff?project=group%2Fapp&iid=5")
	if code != http.StatusOK {
		t.Fatalf("diff: %d", code)
	}
	// Nested directories become open, toggleable folders; files link by name.
	for _, want := range []string{"<details open", ">internal/", ">tui/", `class="fname">app.go<`, `class="fname">README.md<`} {
		if !strings.Contains(body, want) {
			t.Fatalf("explorer missing %q:\n%s", want, body)
		}
	}
}

// waitRun waits for the (only) run to finish and returns it.
func waitRun(t *testing.T, s *Server) *reviewRun {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		s.runs.mu.Lock()
		var run *reviewRun
		for _, r := range s.runs.runs {
			run = r
		}
		s.runs.mu.Unlock()
		if run != nil {
			if _, done, _ := run.snapshot(); done {
				return run
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("review run did not finish")
	return nil
}

func TestParseAgentModels(t *testing.T) {
	form := url.Values{
		"agents":         {"bug", "security"},
		"model:bug":      {"opus"},
		"model:security": {""},      // "(default)" — dropped
		"model:docs":     {"haiku"}, // not selected — ignored
		"project":        {"group/app"},
	}
	got := parseAgentModels(form, []string{"bug", "security"})
	if len(got) != 1 || got["bug"] != "opus" {
		t.Fatalf("parseAgentModels: %v", got)
	}
}

func TestReviewPerAgentModel(t *testing.T) {
	rev := &recordingReviewer{result: defaultResult()}
	env := newTestEnv(t, rev)

	// The detail page renders a per-agent model dropdown.
	_, body := env.get("/i/default/mr?project=group%2Fapp&iid=5")
	if !strings.Contains(body, `name="model:bug"`) {
		t.Fatalf("no per-agent model dropdown:\n%s", body)
	}

	// Start a review choosing opus for the bug agent (the model for the
	// unchecked security agent is ignored).
	code, _ := env.post("/i/default/mr/review", mrForm(url.Values{
		"agents": {"bug"}, "model:bug": {"opus"}, "model:security": {"sonnet"},
	}))
	if code != http.StatusOK {
		t.Fatalf("review start: %d", code)
	}
	waitRun(t, env.srv)

	// The choice reached the reviewer as the request model.
	reqs := rev.requests()
	if len(reqs) != 1 || reqs[0].Model != "opus" {
		t.Fatalf("agent model not applied: %+v", reqs)
	}

	// It is remembered: reopening the form pre-selects it.
	_, body = env.get("/i/default/mr?project=group%2Fapp&iid=5")
	if !strings.Contains(body, `<option value="opus" selected>opus</option>`) {
		t.Fatalf("remembered model not pre-selected:\n%s", body)
	}
}

func TestReviewRunToFindingsToPublish(t *testing.T) {
	env := newTestEnv(t, &fakeReviewer{result: defaultResult()})

	// Kick off a review; the redirect lands on the run page.
	code, body := env.post("/i/default/mr/review", mrForm(url.Values{"agents": {"bug"}}))
	if code != http.StatusOK || !strings.Contains(body, "Reviewing group/app!5") {
		t.Fatalf("review start: %d\n%s", code, body)
	}

	run := waitRun(t, env.srv)
	_, _, out := run.snapshot()
	if out.Err != "" || out.RecName == "" {
		t.Fatalf("run outcome: %+v", out)
	}

	// The run page now links to the findings and reports their count.
	code, body = env.get("/i/default/run/" + run.ID)
	if code != http.StatusOK || !strings.Contains(body, "Open findings") {
		t.Fatalf("run page after done: %d\n%s", code, body)
	}
	if !strings.Contains(body, "completed with 1 finding(s)") {
		t.Fatalf("run page lost the findings count:\n%s", body)
	}

	findingsPath := "/i/default/mr/findings?project=group%2Fapp&iid=5&record=" + out.RecName
	code, body = env.get(findingsPath)
	if code != http.StatusOK || !strings.Contains(body, "Unused import") || !strings.Contains(body, "One issue found.") {
		t.Fatalf("findings: %d\n%s", code, body)
	}

	// Accept the finding; the record on disk is updated.
	env.post("/i/default/mr/findings/state", mrForm(url.Values{
		"record": {out.RecName}, "id": {"f001"}, "action": {"accept"},
	}))
	rec := loadRecord(t, env, out.RecName)
	if rec.Findings[0].State != review.StateAccepted {
		t.Fatalf("finding not accepted: %v", rec.Findings[0].State)
	}

	// Publish immediately: the finding resolves to an inline discussion.
	code, body = env.post("/i/default/mr/publish", mrForm(url.Values{
		"record": {out.RecName}, "mode": {"immediate"},
	}))
	if code != http.StatusOK || !strings.Contains(body, "Publish complete") {
		t.Fatalf("publish: %d\n%s", code, body)
	}
	if len(env.svc.inline) != 1 || !strings.Contains(env.svc.inline[0], "fmt is imported but unused.") {
		t.Fatalf("inline discussions: %+v", env.svc.inline)
	}
	rec = loadRecord(t, env, out.RecName)
	if rec.Findings[0].State != review.StatePublished {
		t.Fatalf("published state not stored: %v", rec.Findings[0].State)
	}

	// History lists the stored review.
	code, body = env.get("/i/default/mr/history?project=group%2Fapp&iid=5")
	if code != http.StatusOK || !strings.Contains(body, "Open findings") {
		t.Fatalf("history: %d\n%s", code, body)
	}
}

func TestDraftPublishFlow(t *testing.T) {
	env := newTestEnv(t, &fakeReviewer{result: defaultResult()})
	env.post("/i/default/mr/review", mrForm(url.Values{"agents": {"bug"}}))
	run := waitRun(t, env.srv)
	_, _, out := run.snapshot()

	env.post("/i/default/mr/findings/state", mrForm(url.Values{
		"record": {out.RecName}, "action": {"accept-all"},
	}))
	code, body := env.post("/i/default/mr/publish", mrForm(url.Values{
		"record": {out.RecName}, "mode": {"draft"},
	}))
	if code != http.StatusOK || !strings.Contains(body, "Draft review ready") {
		t.Fatalf("draft publish: %d\n%s", code, body)
	}
	if len(env.svc.drafts) != 1 {
		t.Fatalf("draft notes: %+v", env.svc.drafts)
	}
	env.post("/i/default/mr/publish/review", mrForm(nil))
	if !env.svc.publishedAll {
		t.Fatal("PublishAllDraftNotes not called")
	}
}

func TestManualCommentsRideAlongWithRun(t *testing.T) {
	env := newTestEnv(t, &fakeReviewer{result: defaultResult()})
	env.post("/i/default/mr/comment", mrForm(url.Values{
		"file": {"main.go"}, "new": {"2"}, "body": {"manual note"},
	}))
	env.post("/i/default/mr/review", mrForm(url.Values{"agents": {"bug"}}))
	run := waitRun(t, env.srv)
	_, _, out := run.snapshot()

	rec := loadRecord(t, env, out.RecName)
	if len(rec.Findings) != 2 {
		t.Fatalf("manual comment not folded into the record: %+v", rec.Findings)
	}
	if left := env.srv.comments.list(mrKey("default", "group/app", 5)); len(left) != 0 {
		t.Fatalf("pending comment should have moved into the record: %+v", left)
	}
}

func TestPublishPendingCommentsOnly(t *testing.T) {
	env := newTestEnv(t, &fakeReviewer{result: defaultResult()})
	env.post("/i/default/mr/comment", mrForm(url.Values{"body": {"general remark"}}))

	code, body := env.post("/i/default/mr/publish", mrForm(url.Values{
		"source": {"comments"}, "mode": {"immediate"},
	}))
	if code != http.StatusOK || !strings.Contains(body, "Publish complete") {
		t.Fatalf("publish comments: %d\n%s", code, body)
	}
	if len(env.svc.notes) != 1 || env.svc.notes[0] != "general remark" {
		t.Fatalf("notes: %+v", env.svc.notes)
	}
	pending := env.srv.comments.list(mrKey("default", "group/app", 5))
	if len(pending) != 1 || pending[0].State != review.StatePublished {
		t.Fatalf("comment state after publish: %+v", pending)
	}
}

func loadRecord(t *testing.T, env *testEnv, name string) resultstore.Record {
	t.Helper()
	path, err := env.srv.safeStoreFile(name, ".json")
	if err != nil {
		t.Fatal(err)
	}
	d, _ := env.srv.instanceDeps("default")
	rec, err := d.Results.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestSafeStoreFile(t *testing.T) {
	s := &Server{opts: Options{ReviewsDir: "/state/reviews"}}
	if _, err := s.safeStoreFile("review-5-123.json", ".json"); err != nil {
		t.Fatalf("valid name rejected: %v", err)
	}
	for _, bad := range []string{"", "../secrets.json", "review-5-123.json/../../x.json", "other.json", "review-5-123.log"} {
		if _, err := s.safeStoreFile(bad, ".json"); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
}

func TestParseDiffLines(t *testing.T) {
	lines := parseDiffLines(sampleDiffs()[0], nil, nil)
	if len(lines) != 5 {
		t.Fatalf("got %d lines, want 5: %+v", len(lines), lines)
	}
	if lines[0].Kind != "hunk" {
		t.Fatalf("first line should be the hunk header: %+v", lines[0])
	}
	if lines[1].Kind != "ctx" || lines[1].Old != 1 || lines[1].New != 1 {
		t.Fatalf("context line numbering wrong: %+v", lines[1])
	}
	if lines[2].Kind != "add" || lines[2].New != 2 || lines[2].Old != 0 {
		t.Fatalf("added line numbering wrong: %+v", lines[2])
	}
	if !strings.Contains(string(lines[2].HTML), "fmt") {
		t.Fatalf("added line content missing: %s", lines[2].HTML)
	}
}

func TestRunEventsStream(t *testing.T) {
	env := newTestEnv(t, &fakeReviewer{result: defaultResult()})
	env.post("/i/default/mr/review", mrForm(url.Values{"agents": {"bug"}}))
	run := waitRun(t, env.srv)

	code, body := env.get(fmt.Sprintf("/i/default/run/%s/events", run.ID))
	if code != http.StatusOK {
		t.Fatalf("events: %d", code)
	}
	if !strings.Contains(body, "event: line") || !strings.Contains(body, "event: done") {
		t.Fatalf("SSE stream missing events:\n%s", body)
	}
	if !strings.Contains(body, "findingsUrl") {
		t.Fatalf("done event missing findings URL:\n%s", body)
	}
}

func TestAgentSelectionDrivesRunAndBadge(t *testing.T) {
	env := newTestEnv(t, &fakeReviewer{result: defaultResult()})

	// The MR page offers the agent checkboxes, all builtins pre-checked.
	code, body := env.get("/i/default/mr?project=group%2Fapp&iid=5")
	if code != http.StatusOK {
		t.Fatalf("mr page: %d", code)
	}
	for _, want := range []string{`name="agents" value="bug"`, `name="agents" value="security"`, "checked"} {
		if !strings.Contains(body, want) {
			t.Fatalf("mr page missing %q", want)
		}
	}

	// Starting a review with one agent selected attributes its findings.
	env.post("/i/default/mr/review", mrForm(url.Values{"agents": {"security"}}))
	run := waitRun(t, env.srv)
	_, _, out := run.snapshot()
	rec := loadRecord(t, env, out.RecName)
	if len(rec.Findings) != 1 || rec.Findings[0].Agent != "security" {
		t.Fatalf("findings: %+v", rec.Findings)
	}

	// The findings page badges the agent when it differs from the category.
	code, body = env.get("/i/default/mr/findings?project=group%2Fapp&iid=5&record=" + out.RecName)
	if code != http.StatusOK || !strings.Contains(body, "· security") {
		t.Fatalf("findings page missing agent badge: %d\n%s", code, body)
	}

	// A record without agent attribution (pre-agents era) renders plainly.
	rec.Findings[0].Agent = ""
	d, _ := env.srv.instanceDeps("default")
	if err := d.Results.Save(rec); err != nil {
		t.Fatal(err)
	}
	code, body = env.get("/i/default/mr/findings?project=group%2Fapp&iid=5&record=" + out.RecName)
	if code != http.StatusOK || strings.Contains(body, "· security") {
		t.Fatalf("legacy record must render without an agent badge: %d", code)
	}
}

func TestMRDetailOffersRepoAgents(t *testing.T) {
	env := newTestEnv(t, &fakeReviewer{result: defaultResult()})
	env.svc.repoFilesByDir = map[string][]gitlabx.RepoFile{
		agents.ProjectAgentsDir: {{
			Name:    "sql.md",
			Content: []byte("---\nname: sql-migrations\ndescription: Lock hazards\n---\nLook for locks.\n"),
		}},
		agents.ClaudeAgentsDir: {{
			Name:    "conventions.md",
			Content: []byte("---\ndescription: Team conventions\n---\nCheck team conventions.\n"),
		}},
	}

	code, body := env.get("/i/default/mr?project=group%2Fapp&iid=5")
	if code != http.StatusOK {
		t.Fatalf("mr page: %d", code)
	}
	// Agents from both repo directories are offered.
	if !strings.Contains(body, `name="agents" value="sql-migrations"`) || !strings.Contains(body, `name="agents" value="conventions"`) {
		t.Fatalf("form missing a repo agent:\n%s", body)
	}
	if !strings.Contains(body, `<span class="badge">project</span>`) {
		t.Fatalf("repo agent missing its project badge:\n%s", body)
	}
}

func TestMRDetailSurvivesRepoAgentFetchFailure(t *testing.T) {
	env := newTestEnv(t, &fakeReviewer{result: defaultResult()})
	env.svc.repoFilesErr = errors.New("boom")

	code, body := env.get("/i/default/mr?project=group%2Fapp&iid=5")
	if code != http.StatusOK {
		t.Fatalf("mr page: %d", code)
	}
	if !strings.Contains(body, `name="agents" value="bug"`) {
		t.Fatalf("builtin agents must still be offered:\n%s", body)
	}
	if !strings.Contains(body, "could not fetch repo agents") {
		t.Fatalf("fetch failure must surface as a warning:\n%s", body)
	}
}

func TestMRDetailOffersLocalCloneAgents(t *testing.T) {
	root := t.TempDir()
	clone := filepath.Join(root, "gitlab.example.com", "group", "app")
	for dir, name := range map[string]string{
		filepath.Join(clone, ".gitlab-reviewer", "agents"): "local-only.md",
		filepath.Join(clone, ".claude", "agents"):          "conventions.md",
	} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte("Untracked local agent.\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	env := newTestEnv(t, &fakeReviewer{result: defaultResult()}, func(c *config.Config) {
		c.Checkout.Mode = "root"
		c.Checkout.Root = root
		c.GitLab.BaseURL = "https://gitlab.example.com"
	})
	// The API must not be consulted in root mode.
	env.svc.repoFilesErr = errors.New("API must not be used")

	code, body := env.get("/i/default/mr?project=group%2Fapp&iid=5")
	if code != http.StatusOK {
		t.Fatalf("mr page: %d", code)
	}
	if !strings.Contains(body, `name="agents" value="local-only"`) || !strings.Contains(body, `name="agents" value="conventions"`) {
		t.Fatalf("form missing a local clone agent:\n%s", body)
	}
	if strings.Contains(body, "could not fetch repo agents") {
		t.Fatalf("root mode must not hit the API:\n%s", body)
	}
}

// --- chat ---

type fakeChatter struct {
	mu    sync.Mutex
	reqs  []review.ChatRequest
	reply string
}

func (c *fakeChatter) Chat(_ context.Context, req review.ChatRequest, onEvent func(review.Event)) (*review.ChatReply, error) {
	c.mu.Lock()
	c.reqs = append(c.reqs, req)
	c.mu.Unlock()
	onEvent(review.Event{Kind: review.EventStatus, Text: "reading code…"})
	return &review.ChatReply{Text: c.reply, SessionID: "chat-sess-1", CostUSD: 0.01}, nil
}

func (c *fakeChatter) requests() []review.ChatRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]review.ChatRequest(nil), c.reqs...)
}

// fakeChatReviewer serves both seams, like the real claudecli backend.
type fakeChatReviewer struct {
	fakeReviewer
	fakeChatter
}

// waitChat polls one chat session until no turn is in flight.
func waitChat(t *testing.T, s *Server, id string) *chatSession {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		cs := s.chats.get(id)
		if cs != nil && !cs.snapshot().Busy {
			return cs
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("chat session never went idle")
	return nil
}

func TestChatFlow(t *testing.T) {
	rev := &fakeChatReviewer{
		fakeReviewer{result: defaultResult()},
		fakeChatter{reply: "It guards the expiry claim."},
	}
	env := newTestEnv(t, rev)

	// Starting a line chat with a first message redirects to the chat page.
	code, body := env.post("/i/default/mr/chat/start", mrForm(url.Values{
		"file": {"main.go"}, "new": {"2"}, "body": {"why is this needed?"},
	}))
	if code != http.StatusOK || !strings.Contains(body, "main.go:2") {
		t.Fatalf("chat start: %d\n%s", code, body)
	}

	cs := waitChat(t, env.srv, "c1")
	code, body = env.get("/i/default/chat/" + cs.ID)
	if code != http.StatusOK {
		t.Fatalf("chat page: %d", code)
	}
	for _, want := range []string{"why is this needed?", "It guards the expiry claim."} {
		if !strings.Contains(body, want) {
			t.Errorf("chat page missing %q:\n%s", want, body)
		}
	}

	reqs := rev.requests()
	if len(reqs) != 1 {
		t.Fatalf("requests = %d", len(reqs))
	}
	req := reqs[0]
	if req.Focus == nil || req.Focus.File != "main.go" || req.Focus.Line.NewLine == nil || *req.Focus.Line.NewLine != 2 {
		t.Errorf("focus: %+v", req.Focus)
	}
	if req.SessionID != "" || req.Message != "why is this needed?" || req.RepoPath == "" {
		t.Errorf("request: %+v", req)
	}

	// A follow-up message resumes the backend session.
	env.post("/i/default/chat/"+cs.ID+"/send", url.Values{"message": {"and the old branch?"}})
	waitChat(t, env.srv, cs.ID)
	if reqs = rev.requests(); len(reqs) != 2 || reqs[1].SessionID != "chat-sess-1" {
		t.Fatalf("second turn should resume: %+v", reqs)
	}

	// The SSE endpoint reports done immediately once the turn finished.
	code, body = env.get("/i/default/chat/" + cs.ID + "/events")
	if code != http.StatusOK || !strings.Contains(body, "event: done") {
		t.Fatalf("events: %d\n%s", code, body)
	}

	// Ending the chat closes the session and disables the composer.
	env.post("/i/default/chat/"+cs.ID+"/end", url.Values{})
	code, body = env.get("/i/default/chat/" + cs.ID)
	if code != http.StatusOK || !strings.Contains(body, "This chat has ended.") {
		t.Fatalf("ended chat page: %d\n%s", code, body)
	}

	// A start without a file anchor opens a whole-MR chat.
	code, body = env.post("/i/default/mr/chat/start", mrForm(nil))
	if code != http.StatusOK || !strings.Contains(body, "Chat about group/app!5") {
		t.Fatalf("MR chat page: %d\n%s", code, body)
	}
}

func TestChatUnavailableWithoutChatter(t *testing.T) {
	env := newTestEnv(t, &fakeReviewer{result: defaultResult()}) // no Chatter seam
	code, body := env.post("/i/default/mr/chat/start", mrForm(nil))
	if code != http.StatusNotImplemented || !strings.Contains(body, "chat is not available") {
		t.Fatalf("want 501, got %d\n%s", code, body)
	}
}

func TestMRDetailOffersFullReReview(t *testing.T) {
	env := newTestEnv(t, &fakeReviewer{result: defaultResult()})

	// No stored review yet: nothing to be incremental against, no override.
	_, body := env.get("/i/default/mr?project=group%2Fapp&iid=5")
	if strings.Contains(body, `name="full"`) {
		t.Fatalf("full re-review offered without a stored review:\n%s", body)
	}

	env.post("/i/default/mr/review", mrForm(url.Values{"agents": {"bug"}}))
	waitRun(t, env.srv)

	_, body = env.get("/i/default/mr?project=group%2Fapp&iid=5")
	if !strings.Contains(body, `name="full"`) || !strings.Contains(body, "full re-review") {
		t.Fatalf("full re-review override missing once a review is stored:\n%s", body)
	}
}

func TestSyntaxCSSHasBothThemes(t *testing.T) {
	css, err := syntaxCSS()
	if err != nil {
		t.Fatal(err)
	}
	s := string(css)
	if strings.Contains(s, ".chroma") {
		t.Fatalf("chroma scope not rewritten:\n%.400s", s)
	}
	if !strings.Contains(s, "td.code .k ") && !strings.Contains(s, "td.code .k{") {
		t.Fatalf("dark token rules missing:\n%.400s", s)
	}
	if !strings.Contains(s, `:root[data-theme="light"] td.code`) {
		t.Fatalf("light token rules missing:\n%.400s", s)
	}
}

func TestWordLevelEmphasisMarksChangedSpan(t *testing.T) {
	diff := "@@ -1,2 +1,2 @@\n-left = compute(alpha, one)\n+left = compute(alpha, two)\n ctx\n"
	lines := parseDiffLines(gitlabx.FileDiff{OldPath: "f.txt", NewPath: "f.txt", Diff: diff}, nil, nil)
	if len(lines) != 4 {
		t.Fatalf("got %d lines, want 4: %+v", len(lines), lines)
	}
	if !strings.Contains(string(lines[1].HTML), `<span class="dchg">one</span>`) {
		t.Fatalf("deletion emphasis missing: %s", lines[1].HTML)
	}
	if !strings.Contains(string(lines[2].HTML), `<span class="dchg">two</span>`) {
		t.Fatalf("addition emphasis missing: %s", lines[2].HTML)
	}
	if strings.Contains(string(lines[3].HTML), "dchg") {
		t.Fatalf("context line should not be emphasised: %s", lines[3].HTML)
	}
}

func TestWordLevelEmphasisSkipsUnrelatedLines(t *testing.T) {
	diff := "@@ -1,1 +1,1 @@\n-aaaaaaaaaaaa\n+zzzzzzzzzzzz\n"
	lines := parseDiffLines(gitlabx.FileDiff{OldPath: "f.txt", NewPath: "f.txt", Diff: diff}, nil, nil)
	for _, l := range lines {
		if strings.Contains(string(l.HTML), "dchg") {
			t.Fatalf("unrelated replacement should not be emphasised: %s", l.HTML)
		}
	}
}

func TestWordLevelEmphasisSplitsHighlightTokens(t *testing.T) {
	diff := "@@ -1,1 +1,1 @@\n-x := foo(1)\n+x := bar(1)\n"
	lines := parseDiffLines(gitlabx.FileDiff{OldPath: "main.go", NewPath: "main.go", Diff: diff}, nil, nil)
	del, add := string(lines[1].HTML), string(lines[2].HTML)
	if !strings.Contains(del, `class="dchg"`) || !strings.Contains(del, "foo") {
		t.Fatalf("deletion emphasis missing from highlighted line: %s", del)
	}
	if !strings.Contains(add, `class="dchg"`) || !strings.Contains(add, "bar") {
		t.Fatalf("addition emphasis missing from highlighted line: %s", add)
	}
	// The shared prefix stays outside the marker.
	if strings.HasPrefix(del, `<span class="dchg">`) {
		t.Fatalf("emphasis should not swallow the shared prefix: %s", del)
	}
}

func TestChangedSpanBounds(t *testing.T) {
	aFrom, aTo, bFrom, bTo, ok := changedSpan("compute(alpha)", "compute(gamma)")
	if !ok || aFrom != 8 || aTo != 12 || bFrom != 8 || bTo != 12 { // "alph" vs "gamm": "a)" is shared suffix
		t.Fatalf("changedSpan: %d %d %d %d %v", aFrom, aTo, bFrom, bTo, ok)
	}
	if _, _, _, _, ok := changedSpan("same", "same"); ok {
		t.Fatal("equal lines must not be marked")
	}
	// Multi-byte runes must not be split.
	aFrom, aTo, _, _, ok = changedSpan("héllo wörld", "héllo wërld")
	if !ok {
		t.Fatal("similar unicode lines should be marked")
	}
	if aFrom > len("héllo wörld") || aTo > len("héllo wörld") || !utf8.RuneStart("héllo wörld"[aFrom]) {
		t.Fatalf("emphasis breaks rune boundary: %d %d", aFrom, aTo)
	}
}

func TestFindingStateJSONResponse(t *testing.T) {
	env := newTestEnv(t, &fakeReviewer{result: defaultResult()})
	env.post("/i/default/mr/review", mrForm(url.Values{"agents": {"bug"}}))
	run := waitRun(t, env.srv)
	_, _, out := run.snapshot()

	code, body := env.post("/i/default/mr/findings/state", mrForm(url.Values{
		"record": {out.RecName}, "id": {"f001"}, "action": {"accept"}, "format": {"json"},
	}))
	if code != http.StatusOK {
		t.Fatalf("state json: %d\n%s", code, body)
	}
	var res struct {
		States   map[string]string `json:"states"`
		Accepted int               `json:"accepted"`
		Rejected int               `json:"rejected"`
		Pending  int               `json:"pending"`
	}
	if err := json.Unmarshal([]byte(body), &res); err != nil {
		t.Fatalf("decoding %q: %v", body, err)
	}
	if res.States["f001"] != "accepted" || res.Accepted != 1 || res.Pending != 0 || res.Rejected != 0 {
		t.Fatalf("unexpected response: %+v", res)
	}
}

func TestDiffShowsStoredFindingsInline(t *testing.T) {
	env := newTestEnv(t, &fakeReviewer{result: defaultResult()})
	env.post("/i/default/mr/review", mrForm(url.Values{"agents": {"bug"}}))
	waitRun(t, env.srv)

	code, body := env.get("/i/default/mr/diff?project=group%2Fapp&iid=5")
	if code != http.StatusOK {
		t.Fatalf("diff: %d", code)
	}
	if !strings.Contains(body, `data-finding="f001"`) || !strings.Contains(body, "Unused import") {
		t.Fatalf("stored finding not shown inline:\n%s", body)
	}
	if !strings.Contains(body, "Findings from the review") {
		t.Fatalf("findings banner missing:\n%s", body)
	}
	if !strings.Contains(body, `class="inline-form f-state-form"`) {
		t.Fatalf("inline triage forms missing:\n%s", body)
	}
}

func TestMRListShowsReviewedBadge(t *testing.T) {
	env := newTestEnv(t, &fakeReviewer{result: defaultResult()})

	_, body := env.get("/i/default/?projects=group%2Fapp")
	if strings.Contains(body, `class="badge reviewed"`) {
		t.Fatalf("badge shown before any review:\n%s", body)
	}

	env.post("/i/default/mr/review", mrForm(url.Values{"agents": {"bug"}}))
	waitRun(t, env.srv)

	code, body := env.get("/i/default/?projects=group%2Fapp")
	if code != http.StatusOK || !strings.Contains(body, `class="badge reviewed"`) {
		t.Fatalf("reviewed badge missing: %d\n%s", code, body)
	}
}

func TestAutoPublishImmediateMode(t *testing.T) {
	env := newTestEnv(t, &fakeReviewer{result: defaultResult()}, func(c *config.Config) {
		c.Publish.AutoComment = true
		c.Publish.Mode = "immediate"
	})
	env.post("/i/default/mr/review", mrForm(url.Values{"agents": {"bug"}}))
	run := waitRun(t, env.srv)
	_, _, out := run.snapshot()
	if out.Err != "" || out.DraftReady {
		t.Fatalf("outcome: %+v", out)
	}
	if len(env.svc.inline) != 1 || !strings.Contains(env.svc.inline[0], "fmt is imported but unused.") {
		t.Fatalf("auto-publish did not post inline: %+v", env.svc.inline)
	}
	rec := loadRecord(t, env, out.RecName)
	if rec.Findings[0].State != review.StatePublished {
		t.Fatalf("published state not stored: %v", rec.Findings[0].State)
	}
}

func TestAutoPublishDraftMode(t *testing.T) {
	env := newTestEnv(t, &fakeReviewer{result: defaultResult()}, func(c *config.Config) {
		c.Publish.AutoComment = true
		c.Publish.Mode = "draft"
	})
	env.post("/i/default/mr/review", mrForm(url.Values{"agents": {"bug"}}))
	run := waitRun(t, env.srv)
	_, _, out := run.snapshot()
	if !out.DraftReady {
		t.Fatalf("draft mode should leave a pending review: %+v", out)
	}
	if len(env.svc.drafts) != 1 {
		t.Fatalf("draft notes: %+v", env.svc.drafts)
	}
	// The run page offers the one-click publish.
	code, body := env.get("/i/default/run/" + run.ID)
	if code != http.StatusOK || !strings.Contains(body, "Publish review now") {
		t.Fatalf("run page missing publish action: %d\n%s", code, body)
	}
	// And its SSE done event carries the flag for the streaming client.
	_, events := env.get("/i/default/run/" + run.ID + "/events")
	if !strings.Contains(events, `"draftReady":true`) {
		t.Fatalf("done event missing draftReady:\n%s", events)
	}
}
