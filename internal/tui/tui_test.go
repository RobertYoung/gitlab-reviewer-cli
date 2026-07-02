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
)

type fakeService struct {
	mrs         []gitlabx.MRSummary
	hasMore     bool
	listErr     error
	lastFilter  gitlabx.MRFilter
	lastPage    gitlabx.Page
	detail      *gitlabx.MRDetail
	diffs       []gitlabx.FileDiff
	discussions []gitlabx.Discussion
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
	s := newMRList(svc, 50)

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
	s := newMRList(svc, 50)
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
	s := newMRList(svc, 50)
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
	s := newMRList(svc, 50)
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
	s := newMRDetail(svc, mr)
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
	app := NewApp(config.Default(), svc)

	m, _ := app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	app = m.(*App)
	if len(app.stack) != 1 {
		t.Fatalf("stack = %d", len(app.stack))
	}

	detail := newMRDetail(svc, sampleMRs()[0])
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
