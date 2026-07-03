package webui

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/config"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review"
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
			SourceBranch: "feature",
			TargetBranch: "main",
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
	inline         []string
	notes          []string
	drafts         []string
	publishedAll   bool
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
	return &mr, nil
}

func (f *fakeService) ListDiffs(context.Context, any, int64) ([]gitlabx.FileDiff, error) {
	return sampleDiffs(), nil
}

func (f *fakeService) ListCommits(context.Context, any, int64) ([]gitlabx.Commit, error) {
	return []gitlabx.Commit{{ShortID: "abc1234", Title: "add import"}}, nil
}
func (f *fakeService) GetMergeRequestTemplate(context.Context, any) (string, error) { return "", nil }
func (f *fakeService) ListDiscussions(context.Context, any, int64) ([]gitlabx.Discussion, error) {
	return nil, nil
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
	return &gitlabx.Approvals{UserCanApprove: true}, nil
}
func (f *fakeService) Approve(context.Context, any, int64, string) error { return nil }
func (f *fakeService) Unapprove(context.Context, any, int64) error       { return nil }

type fakeReviewer struct{ result *review.Result }

func (r *fakeReviewer) Name() string                         { return "fake" }
func (r *fakeReviewer) CheckAvailable(context.Context) error { return nil }
func (r *fakeReviewer) Review(_ context.Context, _ review.Request, onEvent func(review.Event)) (*review.Result, error) {
	onEvent(review.Event{Kind: review.EventStatus, Text: "thinking…"})
	res := *r.result
	res.Findings = append([]review.Finding(nil), r.result.Findings...)
	return &res, nil
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
			return &Deps{
				Cfg:      cfg,
				Svc:      svc,
				Reviewer: rev,
				Logs:     runlog.NewStore(dir),
				Results:  resultstore.NewStore(dir),
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

	return &testEnv{t: t, srv: srv, ts: ts, client: client, svc: svc}
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

func TestReviewRunToFindingsToPublish(t *testing.T) {
	env := newTestEnv(t, &fakeReviewer{result: defaultResult()})

	// Kick off a review; the redirect lands on the run page.
	code, body := env.post("/i/default/mr/review", mrForm(nil))
	if code != http.StatusOK || !strings.Contains(body, "Reviewing group/app!5") {
		t.Fatalf("review start: %d\n%s", code, body)
	}

	run := waitRun(t, env.srv)
	_, _, out := run.snapshot()
	if out.Err != "" || out.RecName == "" {
		t.Fatalf("run outcome: %+v", out)
	}

	// The run page now links to the findings.
	code, body = env.get("/i/default/run/" + run.ID)
	if code != http.StatusOK || !strings.Contains(body, "Open findings") {
		t.Fatalf("run page after done: %d\n%s", code, body)
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
	env.post("/i/default/mr/review", mrForm(nil))
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
	env.post("/i/default/mr/review", mrForm(nil))
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
	lines := parseDiffLines(sampleDiffs()[0], nil)
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
	env.post("/i/default/mr/review", mrForm(nil))
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
