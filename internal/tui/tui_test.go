package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/config"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review/resultstore"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review/runlog"
)

type published struct {
	body string
	pos  *gitlabx.Position // nil for general notes
}

type fakeService struct {
	mrs             []gitlabx.MRSummary
	hasMore         bool
	listErr         error
	lastFilter      gitlabx.MRFilter
	lastPage        gitlabx.Page
	detail          *gitlabx.MRDetail
	diffs           []gitlabx.FileDiff
	compareDiffs    []gitlabx.FileDiff
	compareErr      error
	commits         []gitlabx.Commit
	template        string
	repoFiles       []gitlabx.RepoFile
	repoFilesByDir  map[string][]gitlabx.RepoFile
	discussions     []gitlabx.Discussion
	posted          []published
	drafts          []published
	draftsPublished bool
	inlineErr       error

	groups           []gitlabx.GroupInfo
	projects         []gitlabx.ProjectInfo
	lastGroupSearch  string
	lastProjectGroup string
	memberListed     bool

	approvals    *gitlabx.Approvals
	approveErr   error
	approvedSHAs []string
	unapprovals  int
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

func (f *fakeService) ListCommits(context.Context, any, int64) ([]gitlabx.Commit, error) {
	return f.commits, nil
}

func (f *fakeService) CompareRevisions(context.Context, any, string, string) ([]gitlabx.FileDiff, error) {
	return f.compareDiffs, f.compareErr
}

func (f *fakeService) GetMergeRequestTemplate(context.Context, any) (string, error) {
	return f.template, nil
}

func (f *fakeService) ListDirectoryFiles(_ context.Context, _ any, dir, _ string) ([]gitlabx.RepoFile, error) {
	if f.repoFilesByDir != nil {
		return f.repoFilesByDir[dir], nil
	}
	return f.repoFiles, nil
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

func (f *fakeService) CreateDraftNote(_ context.Context, _ any, _ int64, body string, pos *gitlabx.Position) error {
	f.drafts = append(f.drafts, published{body: body, pos: pos})
	return nil
}

func (f *fakeService) PublishAllDraftNotes(context.Context, any, int64) error {
	f.draftsPublished = true
	return nil
}

func (f *fakeService) GetApprovals(context.Context, any, int64) (*gitlabx.Approvals, error) {
	if f.approvals == nil {
		return nil, errors.New("approvals unavailable")
	}
	return f.approvals, nil
}

func (f *fakeService) Approve(_ context.Context, _ any, _ int64, sha string) error {
	if f.approveErr != nil {
		return f.approveErr
	}
	f.approvedSHAs = append(f.approvedSHAs, sha)
	f.approvals = &gitlabx.Approvals{UserHasApproved: true, ApprovedBy: []string{"you"}}
	return nil
}

func (f *fakeService) Unapprove(context.Context, any, int64) error {
	f.unapprovals++
	f.approvals = &gitlabx.Approvals{UserCanApprove: true}
	return nil
}

func testDeps(svc *fakeService) Deps {
	return Deps{Cfg: config.Default(), Svc: svc}
}

func sampleMRs() []gitlabx.MRSummary {
	return []gitlabx.MRSummary{
		{ProjectPath: "group/app", IID: 11, Title: "Fix parser", Author: "alice", TargetBranch: "main", UpdatedAt: time.Now(), WebURL: "https://gitlab.com/group/app/-/merge_requests/11"},
		{ProjectPath: "group/app", IID: 12, Title: "Add cache", Author: "bob", TargetBranch: "main", UpdatedAt: time.Now(), WebURL: "https://gitlab.com/group/app/-/merge_requests/12"},
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
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
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

func TestMRDetailOverviewToggle(t *testing.T) {
	mr := sampleMRs()[0]
	mr.Description = "Fixes the parser crash on empty input."
	svc := &fakeService{
		detail:  &gitlabx.MRDetail{MRSummary: mr},
		diffs:   []gitlabx.FileDiff{{NewPath: "a.go", Diff: "@@ -1,1 +1,1 @@\n-old\n+new\n"}},
		commits: []gitlabx.Commit{{ShortID: "abc1234", Title: "fix parser"}},
	}
	s := newMRDetail(testDeps(svc), mr)
	var screen Screen = s
	screen, _ = screen.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	for _, msg := range runCmd(screen.Init()) {
		switch msg.(type) {
		case mrDetailLoadedMsg, mrDiffsLoadedMsg, mrCommitsLoadedMsg:
			screen, _ = screen.Update(msg)
		}
	}

	screen, _ = screen.Update(key("d"))
	view := screen.View()
	if !strings.Contains(view, "Fixes the parser crash") || !strings.Contains(view, "abc1234") {
		t.Errorf("overview missing description or commits:\n%s", view)
	}
	if !strings.Contains(s.Hints(), "back to diff") {
		t.Errorf("overview hints wrong: %s", s.Hints())
	}

	// d again returns to the diff.
	screen, _ = screen.Update(key("d"))
	if view := screen.View(); strings.Contains(view, "Fixes the parser crash") || !strings.Contains(view, "new") {
		t.Errorf("diff not restored after leaving the overview:\n%s", view)
	}

	// esc closes the overview without popping the screen.
	screen, _ = screen.Update(key("d"))
	_, cmd := screen.Update(key("esc"))
	if msgs := runCmd(cmd); len(msgs) != 0 {
		t.Errorf("esc in overview should not pop: %v", msgs)
	}
	if s.overview {
		t.Error("overview still open after esc")
	}
}

func TestMRDetailShowsRebaseWarning(t *testing.T) {
	mr := sampleMRs()[0]
	svc := &fakeService{
		detail: &gitlabx.MRDetail{MRSummary: mr, DivergedCommits: 3},
		diffs:  []gitlabx.FileDiff{{NewPath: "a.go", Diff: "@@ -1,1 +1,1 @@\n-old\n+new\n"}},
	}
	s := newMRDetail(testDeps(svc), mr)
	var screen Screen = s
	screen, _ = screen.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	for _, msg := range runCmd(screen.Init()) {
		switch msg.(type) {
		case mrDetailLoadedMsg, mrDiffsLoadedMsg:
			screen, _ = screen.Update(msg)
		}
	}
	if !strings.Contains(screen.View(), "needs rebase") {
		t.Errorf("header missing rebase warning:\n%s", screen.View())
	}
}

func TestMRListOpensBrowser(t *testing.T) {
	svc := &fakeService{mrs: sampleMRs()}
	deps := testDeps(svc)
	var opened []string
	deps.OpenURL = func(url string) error { opened = append(opened, url); return nil }

	var screen Screen = newMRList(deps)
	screen, _ = screen.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	for _, msg := range runCmd(screen.Init()) {
		if _, ok := msg.(mrPageLoadedMsg); ok {
			screen, _ = screen.Update(msg)
		}
	}

	_, cmd := screen.Update(key("o"))
	runCmd(cmd)
	if len(opened) != 1 || opened[0] != "https://gitlab.com/group/app/-/merge_requests/11" {
		t.Errorf("opened = %v", opened)
	}
}

func TestMRDetailShowsURLAndOpensBrowser(t *testing.T) {
	mr := sampleMRs()[0]
	svc := &fakeService{
		detail: &gitlabx.MRDetail{MRSummary: mr},
		diffs:  []gitlabx.FileDiff{{NewPath: "a.go", Diff: "@@ -1,1 +1,1 @@\n-old\n+new\n"}},
	}
	deps := testDeps(svc)
	var opened []string
	deps.OpenURL = func(url string) error { opened = append(opened, url); return nil }

	var screen Screen = newMRDetail(deps, mr)
	screen, _ = screen.Update(tea.WindowSizeMsg{Width: 120, Height: 20})
	for _, msg := range runCmd(screen.Init()) {
		switch msg.(type) {
		case mrDetailLoadedMsg, mrDiffsLoadedMsg:
			screen, _ = screen.Update(msg)
		}
	}

	if !strings.Contains(screen.View(), mr.WebURL) {
		t.Errorf("view missing MR web URL:\n%s", screen.View())
	}

	_, cmd := screen.Update(key("o"))
	runCmd(cmd)
	if len(opened) != 1 || opened[0] != mr.WebURL {
		t.Errorf("opened = %v", opened)
	}
}

func TestMRDetailApproveToggle(t *testing.T) {
	mr := sampleMRs()[0]
	mr.HeadSHA = "head999"
	svc := &fakeService{
		detail:    &gitlabx.MRDetail{MRSummary: mr},
		diffs:     []gitlabx.FileDiff{{NewPath: "a.go", Diff: "@@ -1,1 +1,1 @@\n-old\n+new\n"}},
		approvals: &gitlabx.Approvals{UserCanApprove: true, ApprovedBy: []string{"alice"}},
	}
	s := newMRDetail(testDeps(svc), mr)
	var screen Screen = s
	screen, _ = screen.Update(tea.WindowSizeMsg{Width: 120, Height: 20})
	for _, msg := range runCmd(screen.Init()) {
		switch msg.(type) {
		case mrDetailLoadedMsg, mrDiffsLoadedMsg, mrCommitsLoadedMsg, mrApprovalsMsg:
			screen, _ = screen.Update(msg)
		}
	}

	if !strings.Contains(screen.View(), "approved by alice") {
		t.Errorf("view missing existing approval:\n%s", screen.View())
	}
	if !strings.Contains(screen.Hints(), "a approve") {
		t.Errorf("hints should offer approve: %s", screen.Hints())
	}

	// approve: the action must carry the MR's head SHA and, once the
	// refreshed state lands, flip the view and the hint.
	screen, cmd := screen.Update(key("a"))
	if !s.approvalBusy {
		t.Error("approvalBusy should be set while the action runs")
	}
	for _, msg := range runCmd(cmd) {
		screen, _ = screen.Update(msg)
	}
	if len(svc.approvedSHAs) != 1 || svc.approvedSHAs[0] != "head999" {
		t.Errorf("approvedSHAs = %v", svc.approvedSHAs)
	}
	if !strings.Contains(screen.View(), "approved by you") {
		t.Errorf("view missing own approval:\n%s", screen.View())
	}
	if !strings.Contains(screen.Hints(), "a unapprove") {
		t.Errorf("hints should offer unapprove: %s", screen.Hints())
	}

	// press again: removes the approval
	screen, cmd = screen.Update(key("a"))
	for _, msg := range runCmd(cmd) {
		screen, _ = screen.Update(msg)
	}
	if svc.unapprovals != 1 {
		t.Errorf("unapprovals = %d", svc.unapprovals)
	}
	if strings.Contains(screen.View(), "approved by") {
		t.Errorf("view still shows an approval:\n%s", screen.View())
	}
}

func TestMRDetailApproveError(t *testing.T) {
	mr := sampleMRs()[0]
	svc := &fakeService{
		detail:     &gitlabx.MRDetail{MRSummary: mr},
		diffs:      []gitlabx.FileDiff{{NewPath: "a.go", Diff: "@@ -1,1 +1,1 @@\n-old\n+new\n"}},
		approvals:  &gitlabx.Approvals{UserCanApprove: true},
		approveErr: errors.New("401 not allowed"),
	}
	s := newMRDetail(testDeps(svc), mr)
	var screen Screen = s
	screen, _ = screen.Update(tea.WindowSizeMsg{Width: 120, Height: 20})
	for _, msg := range runCmd(screen.Init()) {
		switch msg.(type) {
		case mrDetailLoadedMsg, mrDiffsLoadedMsg, mrCommitsLoadedMsg, mrApprovalsMsg:
			screen, _ = screen.Update(msg)
		}
	}

	screen, cmd := screen.Update(key("a"))
	for _, msg := range runCmd(cmd) {
		screen, _ = screen.Update(msg)
	}
	if !strings.Contains(screen.View(), "approval failed") {
		t.Errorf("view missing approval failure:\n%s", screen.View())
	}
	if s.approvalBusy {
		t.Error("approvalBusy should clear after a failure")
	}
}

func TestMRDetailFileExplorer(t *testing.T) {
	diff := "@@ -1,2 +1,2 @@\n-old\n+new\n"
	line := 1
	svc := &fakeService{
		detail: &gitlabx.MRDetail{MRSummary: gitlabx.MRSummary{IID: 11}},
		diffs: []gitlabx.FileDiff{
			{NewPath: "pkg/a.go", OldPath: "pkg/a.go", Diff: diff},
			{NewPath: "pkg/b.go", OldPath: "pkg/b.go", Diff: diff, NewFile: true},
			{NewPath: "top.go", OldPath: "top.go", Diff: diff},
		},
		discussions: []gitlabx.Discussion{{
			ID: "d1",
			Notes: []gitlabx.Note{{
				Author: "carol", Body: "hm",
				Position: &gitlabx.Position{NewPath: "pkg/a.go", OldPath: "pkg/a.go", NewLine: &line},
			}},
		}},
	}
	mr := gitlabx.MRSummary{ProjectPath: "group/app", IID: 11, Title: "Fix"}
	s := newMRDetail(testDeps(svc), mr)
	var screen Screen = s
	screen, _ = screen.Update(tea.WindowSizeMsg{Width: 120, Height: 20})
	for _, msg := range runCmd(screen.Init()) {
		switch msg.(type) {
		case mrDetailLoadedMsg, mrDiffsLoadedMsg, mrDiscussionsLoadedMsg:
			screen, _ = screen.Update(msg)
		}
	}

	// closed by default; e opens the sidebar
	if s.treeWidth() != 0 {
		t.Fatalf("explorer open by default, width %d", s.treeWidth())
	}
	screen, _ = screen.Update(key("e"))
	if s.treeWidth() == 0 {
		t.Fatal("e did not open the explorer")
	}
	view := stripANSI(screen.View())
	if !strings.Contains(view, "pkg/") || !strings.Contains(view, "a.go") {
		t.Errorf("explorer entries missing:\n%s", view)
	}
	if !strings.Contains(view, "💬1") {
		t.Errorf("discussion count missing:\n%s", view)
	}

	// tab focuses the tree; the cursor starts on the current file (a.go)
	screen, _ = screen.Update(key("tab"))
	if !s.treeFocus {
		t.Fatal("tab did not focus the explorer")
	}
	if n := s.tree.selected(); n == nil || n.diffIdx != 0 {
		t.Fatalf("cursor not on current file: %+v", n)
	}

	// j + enter opens the next file in the diff pane
	screen, _ = screen.Update(key("j"))
	screen, _ = screen.Update(key("enter"))
	if s.fileIdx != 1 {
		t.Errorf("fileIdx after explorer select = %d, want 1", s.fileIdx)
	}

	// h jumps to the parent dir, enter folds it
	screen, _ = screen.Update(key("h"))
	screen, _ = screen.Update(key("enter"))
	if n := s.tree.selected(); n == nil || !n.isDir() || !n.collapsed {
		t.Fatalf("pkg/ not folded: %+v", n)
	}
	rows := len(s.tree.rows)
	if rows != 2 { // pkg/ + top.go
		t.Errorf("visible rows after fold = %d, want 2", rows)
	}

	// esc returns focus to the diff without popping the screen
	screen, _ = screen.Update(key("esc"))
	if s.treeFocus {
		t.Error("esc did not unfocus the explorer")
	}

	// n-navigation re-reveals the folded file in the tree
	screen, _ = screen.Update(key("n")) // b.go -> top.go
	screen, _ = screen.Update(key("n")) // top.go -> a.go
	if s.fileIdx != 0 {
		t.Fatalf("fileIdx after n n = %d", s.fileIdx)
	}
	if n := s.tree.selected(); n == nil || n.diffIdx != 0 {
		t.Errorf("tree did not follow diff navigation: %+v", n)
	}

	// e hides the sidebar again and the diff regains the full width
	screen.Update(key("e"))
	if s.treeWidth() != 0 || s.mainWidth() != 120 {
		t.Errorf("explorer still visible: tree=%d main=%d", s.treeWidth(), s.mainWidth())
	}
}

func TestMRDetailManualInlineComment(t *testing.T) {
	svc := &fakeService{
		detail: &gitlabx.MRDetail{
			MRSummary: gitlabx.MRSummary{ProjectPath: "group/app", IID: 11},
			DiffRefs:  gitlabx.DiffRefs{BaseSHA: "b", HeadSHA: "h", StartSHA: "s"},
		},
		diffs: []gitlabx.FileDiff{{OldPath: "a.go", NewPath: "a.go", Diff: "@@ -1,3 +1,3 @@\n ctx\n-old\n+new\n"}},
	}
	deps := testDeps(svc)
	deps.Cfg.Publish.Mode = "immediate"
	s := newMRDetail(deps, gitlabx.MRSummary{ProjectPath: "group/app", IID: 11, Title: "Fix"})
	var screen Screen = s
	screen, _ = screen.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	for _, msg := range runCmd(screen.Init()) {
		switch msg.(type) {
		case mrDetailLoadedMsg, mrDiffsLoadedMsg:
			screen, _ = screen.Update(msg)
		}
	}

	// rendered lines: file header, hunk header, " ctx", "-old", "+new";
	// the cursor starts on the first commentable line.
	if s.cursor != 2 {
		t.Fatalf("initial cursor = %d", s.cursor)
	}
	screen, _ = screen.Update(key("j"))
	screen, _ = screen.Update(key("j"))
	if s.cursor != 4 {
		t.Fatalf("cursor after j j = %d", s.cursor)
	}

	// c opens the composer anchored to the added line (new-side line 2)
	_, cmd := screen.Update(key("c"))
	msgs := runCmd(cmd)
	if len(msgs) != 1 {
		t.Fatalf("msgs = %v", msgs)
	}
	comp, ok := msgs[0].(pushScreenMsg).screen.(*commentComposer)
	if !ok {
		t.Fatalf("expected composer, got %T", msgs[0].(pushScreenMsg).screen)
	}
	if comp.anchor == nil || comp.anchor.file != "a.go" ||
		comp.anchor.line.NewLine == nil || *comp.anchor.line.NewLine != 2 || comp.anchor.line.OldLine != nil {
		t.Fatalf("anchor: %+v", comp.anchor)
	}

	// ctrl+s saves the comment into the diff view and pops the composer
	var cs Screen = comp
	cs, _ = cs.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	comp.ta.SetValue("please rename")
	_, cmd = cs.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	msgs = runCmd(cmd)
	if len(msgs) != 1 {
		t.Fatalf("msgs = %v", msgs)
	}
	if _, ok := msgs[0].(popScreenMsg); !ok {
		t.Fatalf("expected pop, got %T", msgs[0])
	}
	if len(s.comments) != 1 || !s.comments[0].Manual || s.comments[0].State != review.StateAccepted {
		t.Fatalf("comments: %+v", s.comments)
	}

	// the pending comment shows inline in the diff and in the header count
	view := stripANSI(screen.View())
	if !strings.Contains(view, "@you (pending)") || !strings.Contains(view, "please rename") {
		t.Errorf("pending comment not rendered inline:\n%s", view)
	}
	if !strings.Contains(view, "1 pending comment(s)") {
		t.Errorf("pending count missing from header:\n%s", view)
	}

	// P publishes through the standard publish pipeline
	_, cmd = screen.Update(key("P"))
	msgs = runCmd(cmd)
	pub, ok := msgs[0].(pushScreenMsg).screen.(*publish)
	if !ok || len(pub.items) != 1 {
		t.Fatalf("publish push: %v", msgs)
	}
	if pub.opts.popCount != 1 {
		t.Errorf("popCount = %d", pub.opts.popCount)
	}
	var ps Screen = pub
	ps.Init()
	ps, _ = ps.Update(key("enter"))
	for range 10 {
		msg := <-pub.ch
		ps, _ = ps.Update(msg)
		if _, ok := msg.(publishDoneMsg); ok {
			break
		}
	}
	if len(svc.posted) != 1 {
		t.Fatalf("posted = %+v", svc.posted)
	}
	if svc.posted[0].pos == nil || svc.posted[0].pos.NewLine == nil || *svc.posted[0].pos.NewLine != 2 {
		t.Errorf("inline position: %+v", svc.posted[0].pos)
	}
	// manual comments post verbatim: no template badge, no attribution
	if svc.posted[0].body != "please rename" {
		t.Errorf("body = %q", svc.posted[0].body)
	}

	// the publish outcome flows back into the diff view; P becomes a no-op
	if s.comments[0].State != review.StatePublished {
		t.Errorf("comment state = %v", s.comments[0].State)
	}
	if _, cmd := screen.Update(key("P")); cmd != nil {
		t.Error("P with nothing pending must be a no-op")
	}
}

func TestMRDetailMRLevelComment(t *testing.T) {
	svc := &fakeService{
		detail: &gitlabx.MRDetail{
			MRSummary: gitlabx.MRSummary{ProjectPath: "group/app", IID: 11},
			DiffRefs:  gitlabx.DiffRefs{BaseSHA: "b", HeadSHA: "h", StartSHA: "s"},
		},
		diffs: []gitlabx.FileDiff{{OldPath: "a.go", NewPath: "a.go", Diff: "@@ -1,1 +1,1 @@\n-old\n+new\n"}},
	}
	deps := testDeps(svc)
	deps.Cfg.Publish.Mode = "immediate"
	deps.Cfg.Publish.FallbackToNote = false // MR-level comments must not depend on the fallback
	s := newMRDetail(deps, gitlabx.MRSummary{ProjectPath: "group/app", IID: 11})
	var screen Screen = s
	screen, _ = screen.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	for _, msg := range runCmd(screen.Init()) {
		switch msg.(type) {
		case mrDetailLoadedMsg, mrDiffsLoadedMsg:
			screen, _ = screen.Update(msg)
		}
	}

	_, cmd := screen.Update(key("C"))
	comp := runCmd(cmd)[0].(pushScreenMsg).screen.(*commentComposer)
	if comp.anchor != nil {
		t.Fatalf("MR-level composer must have no anchor: %+v", comp.anchor)
	}
	comp.ta.SetValue("Overall this looks good, one question in a.go.")
	comp.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if len(s.comments) != 1 || s.comments[0].File != "" {
		t.Fatalf("comments: %+v", s.comments)
	}

	_, cmd = screen.Update(key("P"))
	pub := runCmd(cmd)[0].(pushScreenMsg).screen.(*publish)
	var ps Screen = pub
	ps.Init()
	ps, _ = ps.Update(key("enter"))
	for range 10 {
		msg := <-pub.ch
		ps, _ = ps.Update(msg)
		if _, ok := msg.(publishDoneMsg); ok {
			break
		}
	}
	if len(svc.posted) != 1 || svc.posted[0].pos != nil {
		t.Fatalf("expected one general note, got %+v", svc.posted)
	}
	if svc.posted[0].body != "Overall this looks good, one question in a.go." {
		t.Errorf("body = %q", svc.posted[0].body)
	}
	if s.comments[0].State != review.StatePublished {
		t.Errorf("state = %v", s.comments[0].State)
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
	deps := testDeps(&fakeService{template: "## What\n<!-- fill this in -->"})
	deps.Reviewer = rev
	deps.Logs = runlog.NewStore(t.TempDir())
	deps.Results = resultstore.NewStore(t.TempDir())
	cleanedUp := false
	deps.Checkout = func(_ context.Context, mr gitlabx.MRDetail, progress func(string)) (string, func(context.Context) error, error) {
		progress("cloning…")
		return "/tmp/worktree", func(context.Context) error { cleanedUp = true; return nil }, nil
	}

	s := newReviewRun(deps, *detail, diffs, nil, nil, nil, []string{"bug"}, nil, false)
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
	if !strings.Contains(rev.gotReq.Template, "fill this in") {
		t.Errorf("MR template not threaded into request: %q", rev.gotReq.Template)
	}
	if len(s.log) == 0 {
		t.Error("no progress lines recorded")
	}

	// the run log is stored on disk with the progress lines and outcome
	if s.logPath == "" {
		t.Fatal("no run log path recorded")
	}
	data, err := os.ReadFile(s.logPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"review of group/app!11 — Fix", "cloning…", "Read a.go", "completed with 2 finding(s)"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("run log missing %q:\n%s", want, data)
		}
	}
	entries, err := deps.Logs.List(detail.Ref())
	if err != nil || len(entries) != 1 {
		t.Fatalf("stored entries = %+v, err = %v", entries, err)
	}

	// the result is stored too, so the review can be reopened later
	records, err := deps.Results.List(detail.Ref())
	if err != nil || len(records) != 1 {
		t.Fatalf("stored records = %+v, err = %v", records, err)
	}
	rec, err := deps.Results.Load(records[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Summary != result.Summary || len(rec.Findings) != 2 || rec.LogPath != s.logPath {
		t.Errorf("stored record = %+v", rec)
	}
}

func TestReviewHistoryBrowseAndView(t *testing.T) {
	detail, diffs, _ := reviewFixture()
	dir := t.TempDir()
	deps := testDeps(&fakeService{})
	deps.Logs = runlog.NewStore(dir)
	deps.Results = resultstore.NewStore(dir)

	// one full review: a run log plus a stored result with curated states
	l := deps.Logs.Start(detail.IID, detail.Ref(), "Fix parser")
	l.Append("preparing repository…")
	l.Finish("completed with 1 finding(s)")
	rec := resultstore.Record{
		IID: detail.IID, Ref: detail.Ref(), Title: "Fix parser",
		Started: time.Unix(200, 0), Summary: "One bug.", LogPath: l.Path(),
		Findings: []review.Finding{
			{
				ID: "f001", File: "a.go", Line: review.LineRef{NewLine: intp(2)},
				Severity: review.SeverityMajor, Category: "bug",
				Title: "Bug on added line", Body: "This is wrong.", State: review.StateAccepted,
			},
		},
	}
	if err := deps.Results.Save(rec); err != nil {
		t.Fatal(err)
	}
	// and one older run that only left a log behind (written directly: two
	// Starts in the same second would collide on the timestamped filename)
	oldLog := filepath.Join(dir, "review-11-100.log")
	if err := os.WriteFile(oldLog, []byte("review of "+detail.Ref()+" — Fix parser\nstarted x\n\ncancelled after 1s\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	s := newReviewHistory(deps, *detail, diffs)
	var screen Screen = s
	screen, _ = screen.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	for _, msg := range runCmd(screen.Init()) {
		screen, _ = screen.Update(msg)
	}
	if len(s.entries) != 2 {
		t.Fatalf("entries = %+v (err %v)", s.entries, s.err)
	}
	view := screen.View()
	for _, want := range []string{"Fix parser", "1 finding(s), 1 accepted", "log only"} {
		if !strings.Contains(view, want) {
			t.Errorf("list view missing %q:\n%s", want, view)
		}
	}

	// enter on the stored result reopens the findings screen, states intact
	for i, e := range s.entries {
		if e.recordPath != "" {
			s.cursor = i
		}
	}
	_, cmd := screen.Update(key("enter"))
	var push pushScreenMsg
	for _, msg := range runCmd(cmd) { // load record → push findings
		var c tea.Cmd
		screen, c = screen.Update(msg)
		for _, m := range runCmd(c) {
			if p, ok := m.(pushScreenMsg); ok {
				push = p
			}
		}
	}
	fs, ok := push.screen.(*findings)
	if !ok {
		t.Fatalf("expected findings screen, got %T", push.screen)
	}
	if len(fs.items) != 1 || fs.items[0].State != review.StateAccepted {
		t.Fatalf("restored items = %+v", fs.items)
	}
	if fs.result.Summary != "One bug." {
		t.Errorf("summary = %q", fs.result.Summary)
	}

	// continuing curation on the reopened review re-saves the record
	var fScreen Screen = fs
	fScreen, _ = fScreen.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	_, _ = fScreen.Update(key("x"))
	entries, err := deps.Results.List(detail.Ref())
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries = %+v, err = %v", entries, err)
	}
	if entries[0].Accepted != 0 {
		t.Errorf("rejection not persisted: %+v", entries[0])
	}

	// l opens the run log
	_, cmd = screen.Update(key("l"))
	msgs := runCmd(cmd)
	if len(msgs) != 1 {
		t.Fatalf("msgs = %v", msgs)
	}
	logPush, ok := msgs[0].(pushScreenMsg)
	if !ok {
		t.Fatalf("expected pushScreenMsg, got %T", msgs[0])
	}
	lv := logPush.screen.(*logView)
	var vScreen Screen = lv
	vScreen, _ = vScreen.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	for _, msg := range runCmd(vScreen.Init()) {
		vScreen, _ = vScreen.Update(msg)
	}
	got := vScreen.View()
	for _, want := range []string{"preparing repository…", "completed with 1 finding(s)"} {
		if !strings.Contains(got, want) {
			t.Errorf("log view missing %q:\n%s", want, got)
		}
	}

	// esc pops back
	_, cmd = vScreen.Update(key("esc"))
	if msgs := runCmd(cmd); len(msgs) != 1 {
		t.Fatalf("msgs = %v", msgs)
	} else if _, ok := msgs[0].(popScreenMsg); !ok {
		t.Errorf("expected popScreenMsg, got %T", msgs[0])
	}
}

// Every MR-related screen must open the MR in the browser with o.
func TestOpenBrowserOnMRScreens(t *testing.T) {
	detail, diffs, result := reviewFixture()
	var opened []string
	deps := testDeps(&fakeService{})
	deps.OpenURL = func(url string) error { opened = append(opened, url); return nil }
	deps.Logs = runlog.NewStore(t.TempDir())

	screens := map[string]Screen{
		"reviewrun": newReviewRun(deps, *detail, diffs, nil, nil, nil, []string{"bug"}, nil, false),
		"findings":  newFindings(deps, *detail, diffs, result, nil, nil, nil),
		"publish":   newPublish(deps, *detail, diffs, result.Findings, publishOpts{}),
		"history":   newReviewHistory(deps, *detail, diffs),
		"logview":   newLogView(deps, detail.Ref(), detail.WebURL, "unused.log"),
	}
	for name, screen := range screens {
		opened = nil
		_, cmd := screen.Update(key("o"))
		runCmd(cmd)
		if len(opened) != 1 || opened[0] != detail.WebURL {
			t.Errorf("%s: opened = %v", name, opened)
		}
	}
}

func TestReviewHistoryEmpty(t *testing.T) {
	detail, diffs, _ := reviewFixture()
	deps := testDeps(&fakeService{})
	deps.Logs = runlog.NewStore(t.TempDir())
	deps.Results = resultstore.NewStore(t.TempDir())
	s := newReviewHistory(deps, *detail, diffs)
	var screen Screen = s
	screen, _ = screen.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	for _, msg := range runCmd(screen.Init()) {
		screen, _ = screen.Update(msg)
	}
	if !strings.Contains(screen.View(), "no stored reviews") {
		t.Errorf("empty state not rendered:\n%s", screen.View())
	}
	// enter with no entries must not push anything
	if _, cmd := screen.Update(key("enter")); cmd != nil {
		t.Error("enter on empty list must be a no-op")
	}
}

func TestFindingsCurationPersists(t *testing.T) {
	detail, diffs, result := reviewFixture()
	deps := testDeps(&fakeService{})
	deps.Results = resultstore.NewStore(t.TempDir())
	rec := &resultstore.Record{
		IID: detail.IID, Ref: detail.Ref(), Title: detail.Title,
		Started: time.Unix(300, 0), Summary: result.Summary, Findings: result.Findings,
	}

	s := newFindings(deps, *detail, diffs, result, rec, nil, nil)
	var screen Screen = s
	screen, _ = screen.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	screen, _ = screen.Update(key("a")) // accept the first finding
	_, _ = screen.Update(key("x"))      // reject the second

	entries, err := deps.Results.List(detail.Ref())
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries = %+v, err = %v", entries, err)
	}
	stored, err := deps.Results.Load(entries[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Findings) != 2 ||
		stored.Findings[0].State != review.StateAccepted ||
		stored.Findings[1].State != review.StateRejected {
		t.Errorf("stored findings = %+v", stored.Findings)
	}
}

func TestReviewRunRebaseWarning(t *testing.T) {
	detail, diffs, result := reviewFixture()
	detail.DivergedCommits = 3
	deps := testDeps(&fakeService{})
	deps.Reviewer = &fakeReviewer{result: result}
	deps.Checkout = func(context.Context, gitlabx.MRDetail, func(string)) (string, func(context.Context) error, error) {
		return "/tmp/worktree", func(context.Context) error { return nil }, nil
	}

	s := newReviewRun(deps, *detail, diffs, nil, nil, nil, []string{"bug"}, nil, false)
	var screen Screen = s
	screen, _ = screen.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	screen.Init()

	var got *review.Result
	for range 50 {
		msg := <-s.ch
		if pm, ok := msg.(reviewDoneMsg); ok {
			if pm.err != nil {
				t.Fatal(pm.err)
			}
			got = pm.result
			break
		}
	}
	if got == nil {
		t.Fatal("review never completed")
	}
	var found bool
	for _, w := range got.Warnings {
		if strings.Contains(w, "3 commit(s) behind") {
			found = true
		}
	}
	if !found {
		t.Errorf("rebase warning not surfaced in result: %v", got.Warnings)
	}
}

func TestReviewRunCheckoutFailure(t *testing.T) {
	detail, diffs, _ := reviewFixture()
	deps := testDeps(&fakeService{})
	deps.Reviewer = &fakeReviewer{}
	deps.Checkout = func(context.Context, gitlabx.MRDetail, func(string)) (string, func(context.Context) error, error) {
		return "", nil, errors.New("clone exploded")
	}
	s := newReviewRun(deps, *detail, diffs, nil, nil, nil, []string{"bug"}, nil, false)
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

func TestFindingsRendersWarningsWithFindings(t *testing.T) {
	detail, diffs, result := reviewFixture()
	// A review that produced findings AND carries a rebase warning: the
	// warning must still show, not only on the empty-review screen.
	result.Warnings = []string{"MR branch is 3 commit(s) behind main — a rebase is needed"}
	s := newFindings(testDeps(&fakeService{}), *detail, diffs, result, nil, nil, nil)
	var screen Screen = s
	screen, _ = screen.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if len(s.items) == 0 {
		t.Fatal("fixture should have findings")
	}
	if !strings.Contains(screen.View(), "rebase is needed") {
		t.Errorf("rebase warning not rendered on findings screen:\n%s", screen.View())
	}
}

func TestFindingsCuration(t *testing.T) {
	detail, diffs, result := reviewFixture()
	s := newFindings(testDeps(&fakeService{}), *detail, diffs, result, nil, nil, nil)
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
	deps.Cfg.Publish.Mode = "immediate"

	accepted := []review.Finding{result.Findings[0], result.Findings[1]}
	for i := range accepted {
		accepted[i].State = review.StateAccepted
	}
	s := newPublish(deps, *detail, diffs, accepted, publishOpts{auto: true})
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
	if s.phase != phaseDone {
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
	deps.Cfg.Publish.Mode = "immediate"

	accepted := []review.Finding{result.Findings[0]}
	s := newPublish(deps, *detail, diffs, accepted, publishOpts{auto: true})
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

func TestPublishDraftMode(t *testing.T) {
	detail, diffs, result := reviewFixture()
	svc := &fakeService{}
	deps := testDeps(svc) // default publish.mode = draft

	accepted := []review.Finding{result.Findings[0]}
	s := newPublish(deps, *detail, diffs, accepted, publishOpts{auto: true})
	var screen Screen = s
	screen.Init()
	for range 10 {
		msg := <-s.ch
		screen, _ = screen.Update(msg)
		if _, ok := msg.(publishDoneMsg); ok {
			break
		}
	}
	if s.phase != phaseDraftReady {
		t.Fatalf("phase = %v, want draft-ready", s.phase)
	}
	if len(svc.drafts) != 1 || len(svc.posted) != 0 {
		t.Fatalf("drafts=%d posted=%d", len(svc.drafts), len(svc.posted))
	}
	if svc.drafts[0].pos == nil {
		t.Error("draft note should carry the inline position")
	}
	if svc.draftsPublished {
		t.Fatal("review must not be published before P")
	}

	// P publishes the whole review in one action
	screen, cmd := screen.Update(key("P"))
	msg := <-s.ch
	screen, _ = screen.Update(msg)
	_ = cmd
	if !svc.draftsPublished {
		t.Error("PublishAllDraftNotes not called")
	}
	if s.phase != phaseDone {
		t.Errorf("phase = %v", s.phase)
	}
	_ = screen
}

func TestPublishDraftKeep(t *testing.T) {
	detail, diffs, result := reviewFixture()
	svc := &fakeService{}
	deps := testDeps(svc)

	s := newPublish(deps, *detail, diffs, []review.Finding{result.Findings[0]}, publishOpts{auto: true})
	var screen Screen = s
	screen.Init()
	for range 10 {
		msg := <-s.ch
		screen, _ = screen.Update(msg)
		if _, ok := msg.(publishDoneMsg); ok {
			break
		}
	}
	screen, _ = screen.Update(key("esc")) // keep as pending drafts
	if svc.draftsPublished {
		t.Error("esc must not publish the review")
	}
	if s.phase != phaseDone || !s.keptAsDrafts {
		t.Errorf("phase=%v kept=%v", s.phase, s.keptAsDrafts)
	}
	_ = screen
}

func TestPublishConfirmToggleMode(t *testing.T) {
	detail, diffs, result := reviewFixture()
	svc := &fakeService{}
	deps := testDeps(svc)

	s := newPublish(deps, *detail, diffs, []review.Finding{result.Findings[0]}, publishOpts{})
	var screen Screen = s
	screen.Init()
	if s.phase != phaseConfirm {
		t.Fatalf("phase = %v, want confirm", s.phase)
	}
	if s.mode != "draft" {
		t.Fatalf("mode = %q", s.mode)
	}
	screen, _ = screen.Update(key("m"))
	if s.mode != "immediate" {
		t.Fatalf("mode after toggle = %q", s.mode)
	}
	// enter starts posting in the chosen mode
	screen, _ = screen.Update(key("enter"))
	for range 10 {
		msg := <-s.ch
		screen, _ = screen.Update(msg)
		if _, ok := msg.(publishDoneMsg); ok {
			break
		}
	}
	if len(svc.posted) != 1 || len(svc.drafts) != 0 {
		t.Errorf("posted=%d drafts=%d", len(svc.posted), len(svc.drafts))
	}
	if s.phase != phaseDone {
		t.Errorf("phase = %v", s.phase)
	}
	_ = screen
}

func TestFindingsAutoComment(t *testing.T) {
	detail, diffs, result := reviewFixture()
	svc := &fakeService{}
	deps := testDeps(svc)
	deps.Cfg.Publish.AutoComment = true
	deps.Cfg.Publish.AutoMinSeverity = "major"

	s := newFindings(deps, *detail, diffs, result, nil, nil, nil)
	// major finding pre-accepted, info finding untouched
	if s.items[0].State != review.StateAccepted {
		t.Errorf("major finding should be pre-accepted: %v", s.items[0].State)
	}
	if s.items[1].State != review.StatePending {
		t.Errorf("info finding should stay pending: %v", s.items[1].State)
	}

	// Init pushes an auto publish screen for the accepted set only
	msgs := runCmd(s.Init())
	if len(msgs) != 1 {
		t.Fatalf("msgs = %v", msgs)
	}
	push, ok := msgs[0].(pushScreenMsg)
	if !ok {
		t.Fatalf("expected push, got %T", msgs[0])
	}
	pub := push.screen.(*publish)
	if !pub.opts.auto || pub.opts.popCount != 1 || len(pub.items) != 1 {
		t.Fatalf("publish opts: %+v items=%d", pub.opts, len(pub.items))
	}

	// state reports flow back into the findings screen by ID
	pub.opts.report(s.items[0].ID, review.StatePublished)
	if s.items[0].State != review.StatePublished {
		t.Errorf("report did not update findings: %v", s.items[0].State)
	}
}

func TestFindingsManualComment(t *testing.T) {
	detail, diffs, result := reviewFixture()
	s := newFindings(testDeps(&fakeService{}), *detail, diffs, result, nil, nil, nil)
	var screen Screen = s
	screen, _ = screen.Update(tea.WindowSizeMsg{Width: 100, Height: 30})

	// c opens the composer for an MR-level comment
	_, cmd := screen.Update(key("c"))
	comp := runCmd(cmd)[0].(pushScreenMsg).screen.(*commentComposer)
	comp.ta.SetValue("One general question about the approach.")
	comp.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})

	if len(s.items) != 3 {
		t.Fatalf("items = %d", len(s.items))
	}
	added := s.items[2]
	if !added.Manual || added.State != review.StateAccepted || added.File != "" {
		t.Fatalf("added item: %+v", added)
	}
	if !strings.Contains(stripANSI(screen.View()), "manual") {
		t.Errorf("manual badge missing:\n%s", screen.View())
	}

	// p publishes the accepted set — just the manual comment here
	_, cmd = screen.Update(key("p"))
	pub := runCmd(cmd)[0].(pushScreenMsg).screen.(*publish)
	if len(pub.items) != 1 || !pub.items[0].Manual {
		t.Fatalf("publish items: %+v", pub.items)
	}
}

func TestFindingsForwardsManualStateToDiffView(t *testing.T) {
	detail, diffs, result := reviewFixture()
	manual := []review.Finding{{
		ID: "m001", File: "a.go", Line: review.LineRef{NewLine: intp(2)},
		Body: "typed in the diff view", Manual: true, State: review.StateAccepted,
	}}
	var gotID string
	var gotState review.FindingState
	report := func(id string, state review.FindingState) { gotID, gotState = id, state }
	s := newFindings(testDeps(&fakeService{}), *detail, diffs, result, nil, manual, report)

	if len(s.items) != 3 || !s.items[2].Manual {
		t.Fatalf("manual comment not merged: %+v", s.items)
	}

	// publish outcomes for manual comments flow back to the diff view…
	s.setState("m001", review.StatePublished)
	if gotID != "m001" || gotState != review.StatePublished {
		t.Errorf("manual report: id=%q state=%v", gotID, gotState)
	}
	// …but the model's findings do not
	gotID = ""
	s.setState("f001", review.StatePublished)
	if gotID != "" {
		t.Errorf("model finding leaked into the manual report: %q", gotID)
	}
}

func TestRenderDiffLineRefs(t *testing.T) {
	fd := gitlabx.FileDiff{
		OldPath: "a.go", NewPath: "a.go",
		Diff: "@@ -1,3 +1,3 @@\n ctx\n-old\n+new\n",
	}

	// unified: header, hunk, context, removal, addition
	_, _, refs := renderDiff(fd, nil, 80, false)
	wantU := []diffLineRef{{}, {}, {old: 1, new: 1}, {old: 2}, {new: 2}}
	if len(refs) != len(wantU) {
		t.Fatalf("unified refs = %+v", refs)
	}
	for i, want := range wantU {
		if refs[i] != want {
			t.Errorf("unified ref[%d] = %+v, want %+v", i, refs[i], want)
		}
	}

	// split: header, hunk, context row, then the removal/addition pair on
	// one row carrying both sides.
	_, _, refs = renderDiff(fd, nil, 100, true)
	wantS := []diffLineRef{{}, {}, {old: 1, new: 1}, {old: 2, new: 2}}
	if len(refs) != len(wantS) {
		t.Fatalf("split refs = %+v", refs)
	}
	for i, want := range wantS {
		if refs[i] != want {
			t.Errorf("split ref[%d] = %+v, want %+v", i, refs[i], want)
		}
	}
}

func TestRenderDiffWithDiscussions(t *testing.T) {
	fd := gitlabx.FileDiff{
		OldPath: "a.go", NewPath: "a.go",
		Diff: "@@ -1,3 +1,3 @@\n ctx\n-old\n+new\n@@ -10,2 +10,2 @@\n more\n+tail\n",
	}
	discussions := []gitlabx.Discussion{
		{
			ID: "d1",
			Notes: []gitlabx.Note{{
				Author: "carol", Body: "please rename this\nsecond line",
				Position: &gitlabx.Position{NewPath: "a.go", OldPath: "a.go", NewLine: intp(2)},
			}},
		},
		{
			ID: "other-file",
			Notes: []gitlabx.Note{{
				Author: "dave", Body: "not here",
				Position: &gitlabx.Position{NewPath: "b.go", OldPath: "b.go", NewLine: intp(2)},
			}},
		},
	}

	content, hunks, _ := renderDiff(fd, discussions, 80, false)
	if !strings.Contains(content, "@carol") || !strings.Contains(content, "please rename this") {
		t.Errorf("discussion not rendered:\n%s", content)
	}
	if strings.Contains(content, "@dave") {
		t.Errorf("other file's discussion leaked in:\n%s", content)
	}
	if len(hunks) != 2 {
		t.Fatalf("hunks = %v", hunks)
	}
	// The second hunk offset must account for the multi-line thread block:
	// its recorded line must actually contain the second hunk header.
	lines := strings.Split(content, "\n")
	if !strings.Contains(lines[hunks[1]], "@@ -10,2") {
		t.Errorf("hunk offset drifted: line %d = %q", hunks[1], lines[hunks[1]])
	}
}

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

func TestRenderSplitDiff(t *testing.T) {
	fd := gitlabx.FileDiff{
		OldPath: "a.go", NewPath: "a.go",
		Diff: "@@ -1,3 +1,4 @@\n ctx\n-old\n+new\n+extra\n@@ -10,2 +11,2 @@\n more\n+tail\n",
	}
	discussions := []gitlabx.Discussion{{
		ID: "d1",
		Notes: []gitlabx.Note{{
			Author: "carol", Body: "please rename this",
			Position: &gitlabx.Position{NewPath: "a.go", OldPath: "a.go", NewLine: intp(2)},
		}},
	}}

	content, hunks, _ := renderDiff(fd, discussions, 100, true)
	lines := strings.Split(content, "\n")
	if len(hunks) != 2 {
		t.Fatalf("hunks = %v", hunks)
	}
	if !strings.Contains(lines[hunks[1]], "@@ -10,2") {
		t.Errorf("hunk offset drifted: line %d = %q", hunks[1], lines[hunks[1]])
	}

	rowOf := func(needle string) string {
		t.Helper()
		for _, l := range lines {
			if strings.Contains(l, needle) {
				return l
			}
		}
		t.Fatalf("no rendered line contains %q:\n%s", needle, content)
		return ""
	}
	// A replaced line renders as one row: removal left, addition right.
	if row := rowOf("old"); !strings.Contains(row, "new") {
		t.Errorf("removal not paired with addition: %q", row)
	}
	// A context line shows on both sides with its old and new numbers.
	if row := rowOf("ctx"); strings.Count(row, "ctx") != 2 || !strings.Contains(row, "1") {
		t.Errorf("context row: %q", row)
	}
	// The unpaired addition gets a blank left side (no old line number
	// before the separator on its row).
	if row := rowOf("extra"); strings.Contains(strings.Split(stripANSI(row), "│")[0], "extra") {
		t.Errorf("unpaired addition leaked into the old side: %q", row)
	}
	// Discussion threads still anchor to new-side lines.
	if !strings.Contains(content, "@carol") || !strings.Contains(content, "please rename this") {
		t.Errorf("discussion not rendered:\n%s", content)
	}
}

func (f *fakeService) ListGroups(_ context.Context, search string, _ gitlabx.Page) ([]gitlabx.GroupInfo, bool, error) {
	f.lastGroupSearch = search
	return f.groups, false, nil
}

func (f *fakeService) ListGroupProjects(_ context.Context, group string, _ string, _ gitlabx.Page) ([]gitlabx.ProjectInfo, bool, error) {
	f.lastProjectGroup = group
	return f.projects, false, nil
}

func (f *fakeService) ListMemberProjects(_ context.Context, _ string, _ gitlabx.Page) ([]gitlabx.ProjectInfo, bool, error) {
	f.memberListed = true
	return f.projects, false, nil
}

func TestSelectorFlow(t *testing.T) {
	svc := &fakeService{
		groups:   []gitlabx.GroupInfo{{FullPath: "platform", Name: "Platform", Description: "infra team"}},
		projects: []gitlabx.ProjectInfo{{PathWithNamespace: "platform/api", LastActivity: time.Now()}},
	}
	s := newSelector(testDeps(svc))
	var screen Screen = s
	screen, _ = screen.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	for _, msg := range runCmd(screen.Init()) {
		if _, ok := msg.(selGroupsLoadedMsg); ok {
			screen, _ = screen.Update(msg)
		}
	}

	// row 0 = "your projects", row 1 = the group
	if len(s.rows) != 2 || !s.rows[0].memberProjects || s.rows[1].group != "platform" {
		t.Fatalf("rows: %+v", s.rows)
	}

	// b on the group browses the whole group
	s.cursor = 1
	_, cmd := screen.Update(key("b"))
	msgs := runCmd(cmd)
	if len(msgs) != 1 {
		t.Fatalf("msgs = %v", msgs)
	}
	list := msgs[0].(pushScreenMsg).screen.(*mrList)
	if len(list.filter.Groups) != 1 || list.filter.Groups[0] != "platform" || !list.scoped {
		t.Fatalf("group scope: %+v", list.filter)
	}

	// enter drills into the group's projects
	_, cmd = screen.Update(key("enter"))
	for _, msg := range runCmd(cmd) {
		if _, ok := msg.(selProjectsLoadedMsg); ok {
			screen, _ = screen.Update(msg)
		}
	}
	if s.mode != selProjects || svc.lastProjectGroup != "platform" {
		t.Fatalf("mode=%v group=%q", s.mode, svc.lastProjectGroup)
	}
	if len(s.rows) != 1 || s.rows[0].project != "platform/api" {
		t.Fatalf("project rows: %+v", s.rows)
	}

	// enter on a project browses that project
	_, cmd = screen.Update(key("enter"))
	msgs = runCmd(cmd)
	list = msgs[0].(pushScreenMsg).screen.(*mrList)
	if len(list.filter.Projects) != 1 || list.filter.Projects[0] != "platform/api" {
		t.Fatalf("project scope: %+v", list.filter)
	}

	// esc returns to the groups view
	_, cmd = screen.Update(key("esc"))
	for _, msg := range runCmd(cmd) {
		if _, ok := msg.(selGroupsLoadedMsg); ok {
			screen, _ = screen.Update(msg)
		}
	}
	if s.mode != selGroups {
		t.Fatalf("mode after esc = %v", s.mode)
	}
}

func TestSelectorMemberProjects(t *testing.T) {
	svc := &fakeService{projects: []gitlabx.ProjectInfo{{PathWithNamespace: "rob/dotfiles"}}}
	s := newSelector(testDeps(svc))
	var screen Screen = s
	screen, _ = screen.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	for _, msg := range runCmd(screen.Init()) {
		if _, ok := msg.(selGroupsLoadedMsg); ok {
			screen, _ = screen.Update(msg)
		}
	}
	// enter on "your projects"
	_, cmd := screen.Update(key("enter"))
	for _, msg := range runCmd(cmd) {
		if _, ok := msg.(selProjectsLoadedMsg); ok {
			screen, _ = screen.Update(msg)
		}
	}
	if !svc.memberListed {
		t.Fatal("member projects not requested")
	}
	if len(s.rows) != 1 || s.rows[0].project != "rob/dotfiles" {
		t.Fatalf("rows: %+v", s.rows)
	}
}

func TestAppRootScreenChoice(t *testing.T) {
	svc := &fakeService{}

	deps := testDeps(svc) // no projects/groups configured
	if _, ok := NewApp(deps).top().(*selector); !ok {
		t.Error("empty scope must boot into the selector")
	}

	deps.Cfg.GitLab.Projects = []string{"group/app"}
	if _, ok := NewApp(deps).top().(*mrList); !ok {
		t.Error("configured scope must boot into the MR list")
	}
}
