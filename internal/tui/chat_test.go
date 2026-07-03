package tui

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review"
)

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
	return &review.ChatReply{Text: c.reply, SessionID: "sess-1", CostUSD: 0.01}, nil
}

func (c *fakeChatter) requests() []review.ChatRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]review.ChatRequest(nil), c.reqs...)
}

// pumpChat drains the chat screen's async channel into Update until the
// predicate holds or the timeout hits.
func pumpChat(t *testing.T, s *chatScreen, until func() bool) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for !until() {
		select {
		case msg := <-s.ch:
			s.Update(msg)
		case <-deadline:
			t.Fatal("timed out waiting for chat state")
		}
	}
}

func chatTestDetail() gitlabx.MRDetail {
	return gitlabx.MRDetail{MRSummary: gitlabx.MRSummary{
		ProjectPath: "group/app", IID: 11, Title: "Fix parser", SourceBranch: "fix", TargetBranch: "main",
	}}
}

func TestChatScreenConversation(t *testing.T) {
	chatter := &fakeChatter{reply: "The nil check guards the expiry claim."}
	cleaned := make(chan struct{}, 1)
	deps := testDeps(&fakeService{})
	deps.Chatter = chatter
	deps.Checkout = func(context.Context, gitlabx.MRDetail, func(string)) (string, func(context.Context) error, error) {
		return t.TempDir(), func(context.Context) error { cleaned <- struct{}{}; return nil }, nil
	}

	line := 2
	focus := &review.ChatFocus{File: "a.go", Line: review.LineRef{NewLine: &line}}
	diffs := []gitlabx.FileDiff{{OldPath: "a.go", NewPath: "a.go", Diff: "@@ -1 +1 @@\n-x\n+y\n"}}
	s := newChatScreen(deps, chatTestDetail(), diffs, focus, "+new line")
	var screen Screen = s
	screen, _ = screen.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	screen.Init() // starts the checkout; the batch cmd is drained via pumpChat

	pumpChat(t, s, func() bool { return !s.preparing })
	if s.repoPath == "" || s.err != nil {
		t.Fatalf("checkout: path=%q err=%v", s.repoPath, s.err)
	}

	// type a message and send it with ctrl+s
	s.ta.SetValue("Is the nil check right?")
	screen.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if !s.busy || len(s.transcript) != 1 || !s.transcript[0].you {
		t.Fatalf("after send: busy=%v transcript=%+v", s.busy, s.transcript)
	}

	pumpChat(t, s, func() bool { return !s.busy })
	if len(s.transcript) != 2 || s.transcript[1].you {
		t.Fatalf("transcript after reply: %+v", s.transcript)
	}
	if !strings.Contains(s.transcript[1].text, "expiry claim") {
		t.Errorf("reply text: %q", s.transcript[1].text)
	}
	if s.claudeSession != "sess-1" {
		t.Errorf("session = %q", s.claudeSession)
	}

	reqs := chatter.requests()
	if len(reqs) != 1 {
		t.Fatalf("requests = %d", len(reqs))
	}
	req := reqs[0]
	if req.Focus == nil || req.Focus.File != "a.go" || req.Focus.Line.NewLine == nil || *req.Focus.Line.NewLine != 2 {
		t.Errorf("focus: %+v", req.Focus)
	}
	if req.SessionID != "" || req.Message != "Is the nil check right?" || req.RepoPath != s.repoPath {
		t.Errorf("request: %+v", req)
	}

	// a second message resumes the session
	s.ta.SetValue("thanks, and the old branch?")
	screen.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	pumpChat(t, s, func() bool { return !s.busy })
	if reqs = chatter.requests(); len(reqs) != 2 || reqs[1].SessionID != "sess-1" {
		t.Fatalf("second turn should resume: %+v", reqs)
	}

	view := stripANSI(screen.View())
	if !strings.Contains(view, "you") || !strings.Contains(view, "expiry claim") {
		t.Errorf("view missing transcript:\n%s", view)
	}

	// esc ends the chat: screen pops and the worktree is released
	_, cmd := screen.Update(key("esc"))
	msgs := runCmd(cmd)
	if len(msgs) != 1 {
		t.Fatalf("msgs = %v", msgs)
	}
	if _, ok := msgs[0].(popScreenMsg); !ok {
		t.Fatalf("expected pop, got %T", msgs[0])
	}
	select {
	case <-cleaned:
	case <-time.After(5 * time.Second):
		t.Fatal("worktree cleanup was not called")
	}
}

func TestMRDetailChatKeys(t *testing.T) {
	svc := &fakeService{
		detail: &gitlabx.MRDetail{
			MRSummary: gitlabx.MRSummary{ProjectPath: "group/app", IID: 11},
			DiffRefs:  gitlabx.DiffRefs{BaseSHA: "b", HeadSHA: "h", StartSHA: "s"},
		},
		diffs: []gitlabx.FileDiff{{OldPath: "a.go", NewPath: "a.go", Diff: "@@ -1,3 +1,3 @@\n ctx\n-old\n+new\n"}},
	}
	deps := testDeps(svc)
	deps.Chatter = &fakeChatter{}
	s := newMRDetail(deps, gitlabx.MRSummary{ProjectPath: "group/app", IID: 11, Title: "Fix"})
	var screen Screen = s
	screen, _ = screen.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	for _, msg := range runCmd(screen.Init()) {
		switch msg.(type) {
		case mrDetailLoadedMsg, mrDiffsLoadedMsg:
			screen, _ = screen.Update(msg)
		}
	}

	// t on the added line opens a chat focused on new-side line 2
	screen, _ = screen.Update(key("j"))
	screen, _ = screen.Update(key("j"))
	_, cmd := screen.Update(key("t"))
	msgs := runCmd(cmd)
	if len(msgs) != 1 {
		t.Fatalf("msgs = %v", msgs)
	}
	chat, ok := msgs[0].(pushScreenMsg).screen.(*chatScreen)
	if !ok {
		t.Fatalf("expected chat screen, got %T", msgs[0].(pushScreenMsg).screen)
	}
	if chat.focus == nil || chat.focus.File != "a.go" ||
		chat.focus.Line.NewLine == nil || *chat.focus.Line.NewLine != 2 || chat.focus.Line.OldLine != nil {
		t.Fatalf("focus: %+v", chat.focus)
	}

	// T opens a whole-MR chat
	_, cmd = screen.Update(key("T"))
	msgs = runCmd(cmd)
	if len(msgs) != 1 {
		t.Fatalf("msgs = %v", msgs)
	}
	chat, ok = msgs[0].(pushScreenMsg).screen.(*chatScreen)
	if !ok || chat.focus != nil {
		t.Fatalf("expected whole-MR chat, got %T focus=%v", msgs[0].(pushScreenMsg).screen, chat.focus)
	}

	// without a chatter the keys are inert
	deps.Chatter = nil
	s2 := newMRDetail(deps, gitlabx.MRSummary{ProjectPath: "group/app", IID: 11})
	var screen2 Screen = s2
	screen2, _ = screen2.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	for _, msg := range runCmd(screen2.Init()) {
		switch msg.(type) {
		case mrDetailLoadedMsg, mrDiffsLoadedMsg:
			screen2, _ = screen2.Update(msg)
		}
	}
	if _, cmd := screen2.Update(key("T")); cmd != nil {
		t.Error("T should be inert without a chatter")
	}
}
