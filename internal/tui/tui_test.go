package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/config"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review"
)

type published struct {
	body string
	pos  *gitlabx.Position // nil for general notes
}

type fakeService struct {
	mrs         []gitlabx.MRSummary
	hasMore     bool
	listErr     error
	lastFilter  gitlabx.MRFilter
	lastPage    gitlabx.Page
	detail      *gitlabx.MRDetail
	diffs       []gitlabx.FileDiff
	discussions []gitlabx.Discussion
	posted      []published
	inlineErr   error
}

func (f *fakeService) ListOpenMergeRequests(_ context.Context, filter gitlabx.MRFilter, page gitlabx.Page) ([]gitlabx.MRSummary, bool, error) {
	f.lastFilter = filter
	f.lastPage = page
	return f.mrs, f.hasMore, f.listErr
}

func (f *fakeService) GetMergeRequest(context.Context, any, int64) (*gitlabx.MRDetail, error) {
	return f.detail, nil
}

func (f *fakeService) ListDiffs(context.Context, any, int64) ([]gitlabx.FileDiff, error) {
	return f.diffs, nil
}

func (f *fakeService) ListDiscussions(context.Context, any, int64) ([]gitlabx.Discussion, error) {
	return f.discussions, nil
}

func (f *fakeService) CreateInlineDiscussion(_ context.Context, _ any, _ int64, body string, pos *gitlabx.Position) error {
	if f.inlineErr != nil {
		return f.inlineErr
	}
	f.posted = append(f.posted, published{body: body, pos: pos})
	return nil
}

func (f *fakeService) CreateNote(_ context.Context, _ any, _ int64, body string) error {
	f.posted = append(f.posted, published{body: body})
	return nil
}

func testDeps(svc *fakeService) Deps {
	return Deps{Cfg: config.Default(), Svc: svc}
}

func sampleMRs() []gitlabx.MRSummary {
	return []gitlabx.MRSummary{
		{ProjectPath: "group/app", IID: 11, Title: "Fix parser", Author: "alice", TargetBranch: "main", UpdatedAt: time.Now()},
		{ProjectPath: "group/app", IID: 12, Title: "Add cache", Author: "bob", TargetBranch: "main", UpdatedAt: time.Now()},
	}
}

func key(k string) tea.Msg {
	// Construct key presses via the public parser-independent path: each
	// screen switches on msg.String(), so a Key with just runes suffices.
	switch k {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	default:
		return tea.KeyPressMsg{Code: rune(k[0]), Text: k}
	}
}

// drain runs a returned command synchronously and feeds resulting msgs back
// into the screen, ignoring nil and batch internals it cannot execute.
func runCmd(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	switch m := msg.(type) {
	case tea.BatchMsg:
		var out []tea.Msg
		for _, c := range m {
			out = append(out, runCmd(c)...)
		}
		return out
	default:
		return []tea.Msg{msg}
	}
}

func TestMRListLoadsAndSelects(t *testing.T) {
	svc := &fakeService{mrs: sampleMRs()}
	s := newMRList(testDeps(svc))

	var screen Screen = s
	screen, _ = screen.Update(tea.WindowSizeMsg{Width: 120, Height: 30})

	// run Init: spinner tick + page load
	for _, msg := range runCmd(screen.Init()) {
		if _, ok := msg.(mrPageLoadedMsg); ok {
			screen, _ = screen.Update(msg)
		}
	}

	list := screen.(*mrList)
	if list.loading {
		t.Error("loading should be done")
	}
	if len(list.mrs) != 2 {
		t.Fatalf("mrs = %d", len(list.mrs))
	}
	if got := list.table.Rows(); len(got) != 2 || !strings.Contains(got[0][2], "Fix parser") {
		t.Errorf("rows = %v", got)
	}
	if svc.lastPage.Number != 1 || svc.lastPage.PerPage != 50 {
		t.Errorf("page = %+v", svc.lastPage)
	}
	if svc.lastFilter.State != "opened" {
		t.Errorf("filter = %+v", svc.lastFilter)
	}

	// enter pushes the detail screen for the selected MR
	_, cmd := screen.Update(key("enter"))
	msgs := runCmd(cmd)
	if len(msgs) != 1 {
		t.Fatalf("expected one msg, got %v", msgs)
	}
	push, ok := msgs[0].(pushScreenMsg)
	if !ok {
		t.Fatalf("expected pushScreenMsg, got %T", msgs[0])
	}
	detail, ok := push.screen.(*mrDetail)
	if !ok || detail.mr.IID != 11 {
		t.Errorf("pushed screen: %+v", push.screen)
	}
}

func TestMRListStateCycleAndSearch(t *testing.T) {
	svc := &fakeService{mrs: sampleMRs()}
	s := newMRList(testDeps(svc))
	var screen Screen = s
	screen, _ = screen.Update(tea.WindowSizeMsg{Width: 120, Height: 30})

	// s cycles state and reloads
	_, cmd := screen.Update(key("s"))
	runCmd(cmd)
	if svc.lastFilter.State != "merged" {
		t.Errorf("state after cycle = %q", svc.lastFilter.State)
	}

	// / focuses search input; typing + enter applies the filter
	screen, _ = screen.Update(key("/"))
	if s.mode != inputSearch {
		t.Fatalf("mode = %v", s.mode)
	}
	screen, _ = screen.Update(key("x"))
	_, cmd = screen.Update(key("enter"))
	runCmd(cmd)
	if svc.lastFilter.Search != "x" {
		t.Errorf("search = %q", svc.lastFilter.Search)
	}
	if s.mode != inputNone {
		t.Error("input should be closed after enter")
	}

	// esc resets filters
	_, cmd = screen.Update(key("esc"))
	runCmd(cmd)
	if svc.lastFilter.State != "opened" || svc.lastFilter.Search != "" {
		t.Errorf("filter after esc = %+v", svc.lastFilter)
	}
}

func TestMRListStaleResponsesDropped(t *testing.T) {
	svc := &fakeService{mrs: sampleMRs()}
	s := newMRList(testDeps(svc))
	var screen Screen = s
	screen, _ = screen.Update(tea.WindowSizeMsg{Width: 120, Height: 30})

	s.loadPage(1) // reqID 1 (never delivered)
	s.loadPage(1) // reqID 2

	screen, _ = screen.Update(mrPageLoadedMsg{reqID: 1, page: 1, mrs: sampleMRs()})
	if len(s.mrs) != 0 {
		t.Error("stale page must be dropped")
	}
	screen, _ = screen.Update(mrPageLoadedMsg{reqID: 2, page: 1, mrs: sampleMRs()})
	if len(s.mrs) != 2 {
		t.Error("current page must be applied")
	}
	_ = screen
}

func TestMRListError(t *testing.T) {
	svc := &fakeService{listErr: errors.New("boom")}
	s := newMRList(testDeps(svc))
	var screen Screen = s
	screen, _ = screen.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	for _, msg := range runCmd(screen.Init()) {
		if _, ok := msg.(mrListErrMsg); ok {
			screen, _ = screen.Update(msg)
		}
	}
	if s.err == nil {
		t.Fatal("error should be recorded")
	}
	if !strings.Contains(screen.View(), "boom") {
		t.Error("error should be rendered")
	}
}

func TestMRDetailNavigation(t *testing.T) {
	// Two hunks separated by enough context that the second hunk sits
	// beyond the viewport, so jumping to it actually scrolls.
	diff := "@@ -1,2 +1,2 @@\n-old\n+new\n" + strings.Repeat(" context\n", 30) + "@@ -40,2 +40,2 @@\n context\n+added\n"
	svc := &fakeService{
		detail: &gitlabx.MRDetail{MRSummary: gitlabx.MRSummary{IID: 11}, DiffRefs: gitlabx.DiffRefs{BaseSHA: "b"}},
		diffs: []gitlabx.FileDiff{
			{NewPath: "a.go", Diff: diff},
			{NewPath: "b.go", Diff: diff},
		},
	}
	mr := gitlabx.MRSummary{ProjectPath: "group/app", IID: 11, Title: "Fix"}
	s := newMRDetail(testDeps(svc), mr)
	var screen Screen = s
	screen, _ = screen.Update(tea.WindowSizeMsg{Width: 100, Height: 10})
	for _, msg := range runCmd(screen.Init()) {
		switch msg.(type) {
		case mrDetailLoadedMsg, mrDiffsLoadedMsg:
			screen, _ = screen.Update(msg)
		}
	}

	if s.detail == nil || len(s.diffs) != 2 {
		t.Fatalf("load failed: detail=%v diffs=%d", s.detail, len(s.diffs))
	}
	if !strings.Contains(screen.View(), "file 1/2") {
		t.Errorf("view missing file indicator:\n%s", screen.View())
	}

	screen, _ = screen.Update(key("n"))
	if s.fileIdx != 1 {
		t.Errorf("fileIdx after n = %d", s.fileIdx)
	}
	screen, _ = screen.Update(key("n")) // wraps
	if s.fileIdx != 0 {
		t.Errorf("fileIdx after wrap = %d", s.fileIdx)
	}

	// hunk jump moves the viewport offset to the second hunk
	if len(s.hunkLines) != 2 {
		t.Fatalf("hunkLines = %v", s.hunkLines)
	}
	screen, _ = screen.Update(key("]"))
	if got := s.vp.YOffset(); got != s.hunkLines[0] {
		t.Errorf("yoffset after first ] = %d, hunks %v", got, s.hunkLines)
	}
	// The second jump may clamp at max scroll; the hunk must be visible.
	screen, _ = screen.Update(key("]"))
	if got := s.vp.YOffset(); got <= s.hunkLines[0] || got > s.hunkLines[1] {
		t.Errorf("yoffset after second ] = %d, hunks %v", got, s.hunkLines)
	}
	screen, _ = screen.Update(key("["))
	if got := s.vp.YOffset(); got != s.hunkLines[0] {
		t.Errorf("yoffset after [ = %d, hunks %v", got, s.hunkLines)
	}

	// esc pops
	_, cmd := screen.Update(key("esc"))
	msgs := runCmd(cmd)
	if len(msgs) != 1 {
		t.Fatalf("msgs = %v", msgs)
	}
	if _, ok := msgs[0].(popScreenMsg); !ok {
		t.Errorf("expected popScreenMsg, got %T", msgs[0])
	}
}

func TestAppStackRouting(t *testing.T) {
	svc := &fakeService{mrs: sampleMRs()}
	app := NewApp(testDeps(svc))

	m, _ := app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	app = m.(*App)
	if len(app.stack) != 1 {
		t.Fatalf("stack = %d", len(app.stack))
	}

	detail := newMRDetail(testDeps(svc), sampleMRs()[0])
	m, _ = app.Update(pushScreenMsg{screen: detail})
	app = m.(*App)
	if len(app.stack) != 2 || app.top() != Screen(detail) {
		t.Fatalf("push failed: %d", len(app.stack))
	}

	m, _ = app.Update(popScreenMsg{})
	app = m.(*App)
	if len(app.stack) != 1 {
		t.Fatalf("pop failed: %d", len(app.stack))
	}
	// popping the last screen is a no-op
	m, _ = app.Update(popScreenMsg{})
	app = m.(*App)
	if len(app.stack) != 1 {
		t.Fatal("bottom screen must not pop")
	}

	// the view renders the title chrome
	if !strings.Contains(app.View().Content, "gitlab-reviewer") {
		t.Error("view missing title bar")
	}
}

type fakeReviewer struct {
	result *review.Result
	err    error
	events []review.Event
	gotReq review.Request
}

func (f *fakeReviewer) Name() string                         { return "fake" }
func (f *fakeReviewer) CheckAvailable(context.Context) error { return nil }
func (f *fakeReviewer) Review(_ context.Context, req review.Request, onEvent func(review.Event)) (*review.Result, error) {
	f.gotReq = req
	for _, e := range f.events {
		onEvent(e)
	}
	return f.result, f.err
}

func intp(n int) *int { return &n }

func reviewFixture() (*gitlabx.MRDetail, []gitlabx.FileDiff, *review.Result) {
	detail := &gitlabx.MRDetail{
		MRSummary: gitlabx.MRSummary{ProjectPath: "group/app", IID: 11, Title: "Fix", WebURL: "https://gitlab.com/group/app/-/merge_requests/11"},
		DiffRefs:  gitlabx.DiffRefs{BaseSHA: "b", HeadSHA: "h", StartSHA: "s"},
	}
	diffs := []gitlabx.FileDiff{{
		OldPath: "a.go", NewPath: "a.go",
		Diff: "@@ -1,3 +1,3 @@\n ctx\n-old\n+new\n",
	}}
	result := &review.Result{
		Summary: "One bug.",
		Findings: []review.Finding{
			{ID: "f001", File: "a.go", Line: review.LineRef{NewLine: intp(2)}, Severity: review.SeverityMajor, Category: "bug", Title: "Bug on added line", Body: "This is wrong."},
			{ID: "f002", File: "a.go", Line: review.LineRef{NewLine: intp(999)}, Severity: review.SeverityInfo, Category: "style", Title: "Unanchorable", Body: "Cannot be placed."},
		},
	}
	return detail, diffs, result
}

func TestReviewRunHappyFlow(t *testing.T) {
	detail, diffs, result := reviewFixture()
	rev := &fakeReviewer{result: result, events: []review.Event{{Kind: review.EventToolUse, Text: "Read a.go"}}}
	deps := testDeps(&fakeService{})
	deps.Reviewer = rev
	cleanedUp := false
	deps.Checkout = func(_ context.Context, mr gitlabx.MRDetail, progress func(string)) (string, func(context.Context) error, error) {
		progress("cloning…")
		return "/tmp/worktree", func(context.Context) error { cleanedUp = true; return nil }, nil
	}

	s := newReviewRun(deps, *detail, diffs)
	var screen Screen = s
	screen, _ = screen.Update(tea.WindowSizeMsg{Width: 100, Height: 20})

	cmd := screen.Init()
	// pump messages from the channel until the review finishes
	var done bool
	for range 50 {
		msg := <-s.ch
		var c tea.Cmd
		screen, c = screen.Update(msg)
		_ = c
		if pm, ok := msg.(reviewDoneMsg); ok {
			if pm.err != nil {
				t.Fatal(pm.err)
			}
			done = true
			break
		}
	}
	_ = cmd
	if !done {
		t.Fatal("review never completed")
	}
	if !cleanedUp {
		t.Error("worktree cleanup not called")
	}
	if rev.gotReq.RepoPath != "/tmp/worktree" {
		t.Errorf("repo path = %q", rev.gotReq.RepoPath)
	}
	if len(rev.gotReq.Diffs) != 1 {
		t.Errorf("bounded diffs = %d", len(rev.gotReq.Diffs))
	}
	if len(s.log) == 0 {
		t.Error("no progress lines recorded")
	}
}

func TestReviewRunCheckoutFailure(t *testing.T) {
	detail, diffs, _ := reviewFixture()
	deps := testDeps(&fakeService{})
	deps.Reviewer = &fakeReviewer{}
	deps.Checkout = func(context.Context, gitlabx.MRDetail, func(string)) (string, func(context.Context) error, error) {
		return "", nil, errors.New("clone exploded")
	}
	s := newReviewRun(deps, *detail, diffs)
	var screen Screen = s
	screen.Init()
	for range 10 {
		msg := <-s.ch
		screen, _ = screen.Update(msg)
		if _, ok := msg.(reviewDoneMsg); ok {
			break
		}
	}
	if s.err == nil || !strings.Contains(s.err.Error(), "clone exploded") {
		t.Fatalf("err = %v", s.err)
	}
	if !strings.Contains(screen.View(), "review failed") {
		t.Error("failure not rendered")
	}
}

func TestFindingsCuration(t *testing.T) {
	detail, diffs, result := reviewFixture()
	s := newFindings(testDeps(&fakeService{}), *detail, diffs, result)
	var screen Screen = s
	screen, _ = screen.Update(tea.WindowSizeMsg{Width: 100, Height: 30})

	// accept first finding (cursor auto-advances), reject second
	screen, _ = screen.Update(key("a"))
	screen, _ = screen.Update(key("x"))
	if s.items[0].State != review.StateAccepted || s.items[1].State != review.StateRejected {
		t.Fatalf("states: %v %v", s.items[0].State, s.items[1].State)
	}

	// edit the accepted finding's body
	s.cursor = 0
	screen, _ = screen.Update(key("e"))
	if !s.editing {
		t.Fatal("editor should be open")
	}
	s.editor.SetValue("Edited body.")
	screen, _ = screen.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if s.editing || s.items[0].Body != "Edited body." {
		t.Fatalf("edit not applied: editing=%v body=%q", s.editing, s.items[0].Body)
	}

	// p pushes the publish screen with only accepted findings
	_, cmd := screen.Update(key("p"))
	msgs := runCmd(cmd)
	if len(msgs) != 1 {
		t.Fatalf("msgs = %v", msgs)
	}
	push, ok := msgs[0].(pushScreenMsg)
	if !ok {
		t.Fatalf("expected push, got %T", msgs[0])
	}
	pub := push.screen.(*publish)
	if len(pub.items) != 1 || pub.items[0].Body != "Edited body." {
		t.Fatalf("publish items: %+v", pub.items)
	}

	// view renders the hunk context marker
	if !strings.Contains(screen.View(), "diff context:") {
		t.Errorf("missing diff context in view")
	}
}

func TestPublishInlineAndFallback(t *testing.T) {
	detail, diffs, result := reviewFixture()
	svc := &fakeService{}
	deps := testDeps(svc)

	accepted := []review.Finding{result.Findings[0], result.Findings[1]}
	for i := range accepted {
		accepted[i].State = review.StateAccepted
	}
	s := newPublish(deps, *detail, diffs, accepted)
	var screen Screen = s
	screen, _ = screen.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	screen.Init()
	for range 10 {
		msg := <-s.ch
		screen, _ = screen.Update(msg)
		if _, ok := msg.(publishDoneMsg); ok {
			break
		}
	}
	if !s.done {
		t.Fatal("publish never completed")
	}
	if len(svc.posted) != 2 {
		t.Fatalf("posted = %d", len(svc.posted))
	}
	// first finding resolved inline at new line 2
	if svc.posted[0].pos == nil || *svc.posted[0].pos.NewLine != 2 {
		t.Errorf("inline position: %+v", svc.posted[0].pos)
	}
	if svc.posted[0].pos.BaseSHA != "b" || svc.posted[0].pos.HeadSHA != "h" {
		t.Errorf("SHAs: %+v", svc.posted[0].pos)
	}
	// second finding fell back to a note with a permalink
	if svc.posted[1].pos != nil {
		t.Errorf("expected note fallback, got position %+v", svc.posted[1].pos)
	}
	if !strings.Contains(svc.posted[1].body, "could not anchor") || !strings.Contains(svc.posted[1].body, "/-/blob/h/a.go") {
		t.Errorf("fallback body: %q", svc.posted[1].body)
	}
	if s.items[0].State != review.StatePublished || s.items[1].State != review.StateFellBack {
		t.Errorf("states: %v %v", s.items[0].State, s.items[1].State)
	}

	// enter pops back to the MR detail (2 screens)
	_, cmd := screen.Update(key("enter"))
	msgs := runCmd(cmd)
	if len(msgs) != 1 {
		t.Fatalf("msgs = %v", msgs)
	}
	pop, ok := msgs[0].(popScreenMsg)
	if !ok || pop.count != 2 {
		t.Errorf("expected pop 2, got %+v", msgs[0])
	}
}

func TestPublishInlineErrorFallsBack(t *testing.T) {
	detail, diffs, result := reviewFixture()
	svc := &fakeService{inlineErr: errors.New("400 line_code invalid")}
	deps := testDeps(svc)

	accepted := []review.Finding{result.Findings[0]}
	s := newPublish(deps, *detail, diffs, accepted)
	var screen Screen = s
	screen.Init()
	for range 10 {
		msg := <-s.ch
		screen, _ = screen.Update(msg)
		if _, ok := msg.(publishDoneMsg); ok {
			break
		}
	}
	if len(svc.posted) != 1 || svc.posted[0].pos != nil {
		t.Fatalf("expected note fallback after 400, got %+v", svc.posted)
	}
	if s.items[0].State != review.StateFellBack {
		t.Errorf("state = %v", s.items[0].State)
	}
}
