package webui

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/config"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review"
)

// chatMessage is one message of a conversation transcript.
type chatMessage struct {
	You  bool
	Text string
}

// chatEvent is one SSE payload for the chat page: a progress line while a
// reply is being written, or the signal that the page should re-render.
type chatEvent struct {
	Line string
	Done bool
}

// chatSession is one server-side conversation with Claude about an MR (or
// one diff line), the browser counterpart of the TUI chat screen. The MR's
// checkout lives for the whole conversation — the claude CLI session is
// resumed from it — and is released when the chat ends.
type chatSession struct {
	ID       string
	Instance string
	Project  string
	IID      int64
	Ref      string
	Title    string
	WebURL   string
	Focus    *review.ChatFocus // nil chats about the whole MR
	Started  time.Time

	deps   *Deps
	cfg    config.Config
	detail gitlabx.MRDetail
	diffs  []gitlabx.FileDiff

	// ctx spans the conversation; endSession cancels the checkout, any
	// in-flight turn, and the claude subprocess with it.
	ctx        context.Context
	endSession context.CancelFunc

	mu         sync.Mutex
	cancelTurn context.CancelFunc
	messages   []chatMessage
	status     []string // progress lines of the turn in flight
	busy       bool
	err        string // last turn's failure, shown on the page
	closed     bool
	repoPath   string
	cleanup    func(context.Context) error
	sessionID  string // backend session ID, set after the first turn
	subs       map[chan chatEvent]struct{}
}

// FocusLabel names what the conversation is about; empty for the whole MR.
func (c *chatSession) FocusLabel() string {
	if c.Focus == nil {
		return ""
	}
	return c.Focus.Label()
}

// appendStatus records one progress line and fans it out to subscribers.
func (c *chatSession) appendStatus(text string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.appendStatusLocked(text)
}

// appendStatusLocked is appendStatus for callers already holding c.mu.
func (c *chatSession) appendStatusLocked(text string) {
	c.status = append(c.status, text)
	for ch := range c.subs {
		select {
		case ch <- chatEvent{Line: text}:
		default: // slow subscriber; it replays on reconnect
		}
	}
}

// notifyDone tells subscribers to re-render the page. Callers hold c.mu.
func (c *chatSession) notifyDoneLocked() {
	for ch := range c.subs {
		select {
		case ch <- chatEvent{Done: true}:
		default:
		}
	}
}

// subscribe returns the progress lines so far and, while a reply is being
// written, a channel for what follows; done is true when there is nothing
// to wait for.
func (c *chatSession) subscribe() (replay []string, done bool, ch chan chatEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	replay = append([]string(nil), c.status...)
	if !c.busy {
		return replay, true, nil
	}
	ch = make(chan chatEvent, 256)
	c.subs[ch] = struct{}{}
	return replay, false, ch
}

func (c *chatSession) unsubscribe(ch chan chatEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.subs, ch)
}

// chatSnapshot is the render-ready state of a conversation.
type chatSnapshot struct {
	Messages []chatMessage
	Status   []string
	Busy     bool
	Err      string
	Closed   bool
}

func (c *chatSession) snapshot() chatSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return chatSnapshot{
		Messages: append([]chatMessage(nil), c.messages...),
		Status:   append([]string(nil), c.status...),
		Busy:     c.busy,
		Err:      c.err,
		Closed:   c.closed,
	}
}

// startTurn appends the user's message and answers it in a server
// goroutine, streaming progress to subscribers. It reports false when the
// session is already busy or closed.
func (c *chatSession) startTurn(message string) bool {
	c.mu.Lock()
	if c.busy || c.closed {
		c.mu.Unlock()
		return false
	}
	c.busy = true
	c.err = ""
	c.status = nil
	c.messages = append(c.messages, chatMessage{You: true, Text: message})
	turnCtx, cancel := context.WithCancel(c.ctx)
	c.cancelTurn = cancel
	c.mu.Unlock()

	go func() {
		defer cancel()
		reply, err := c.runTurn(turnCtx, message)

		c.mu.Lock()
		defer c.mu.Unlock()
		c.busy = false
		c.cancelTurn = nil
		switch {
		case err != nil && errors.Is(err, context.Canceled):
			// Cancelled by the user; the conversation continues.
		case err != nil:
			c.err = err.Error()
		case reply != nil:
			c.sessionID = reply.SessionID
			c.messages = append(c.messages, chatMessage{Text: reply.Text})
		}
		c.notifyDoneLocked()
	}()
	return true
}

// runTurn makes sure the checkout exists, then runs one chat turn in it.
func (c *chatSession) runTurn(ctx context.Context, message string) (*review.ChatReply, error) {
	repoPath, err := c.ensureRepo(ctx)
	if err != nil {
		return nil, fmt.Errorf("checkout failed: %w", err)
	}
	c.mu.Lock()
	sessionID := c.sessionID
	c.mu.Unlock()
	return c.deps.Chatter.Chat(ctx, review.ChatRequest{
		RepoPath:  repoPath,
		MR:        c.detail,
		Diffs:     c.diffs,
		Focus:     c.Focus,
		MaxDiffKB: c.cfg.Review.MaxDiffKB,
		Message:   message,
		SessionID: sessionID,
		Timeout:   c.cfg.Review.Timeout,
	}, func(e review.Event) {
		c.appendStatus(e.Text)
	})
}

// ensureRepo prepares the conversation's checkout on first use. Turns run
// one at a time (startTurn's busy gate), so no lock is held while the
// checkout runs.
func (c *chatSession) ensureRepo(ctx context.Context) (string, error) {
	c.mu.Lock()
	path := c.repoPath
	c.mu.Unlock()
	if path != "" {
		return path, nil
	}
	c.appendStatus("preparing repository…")
	path, cleanup, err := c.deps.Checkout(ctx, c.detail, c.appendStatus)
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	c.repoPath, c.cleanup = path, cleanup
	c.mu.Unlock()
	return path, nil
}

// close ends the conversation: any in-flight turn dies with the session
// context and the worktree is released.
func (c *chatSession) close(ctx context.Context) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	cleanup := c.cleanup
	c.cleanup = nil
	c.notifyDoneLocked()
	c.mu.Unlock()

	c.endSession()
	if cleanup != nil {
		_ = cleanup(ctx)
	}
}

// chatRegistry tracks conversations by ID for the chat page, its SSE
// stream, and the send/cancel/end actions.
type chatRegistry struct {
	mu    sync.Mutex
	seq   int
	chats map[string]*chatSession
}

func newChatRegistry() *chatRegistry {
	return &chatRegistry{chats: map[string]*chatSession{}}
}

func (g *chatRegistry) get(id string) *chatSession {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.chats[id]
}

// create registers a new conversation for one MR.
func (g *chatRegistry) create(d *Deps, instance string, detail gitlabx.MRDetail, diffs []gitlabx.FileDiff, focus *review.ChatFocus) *chatSession {
	ctx, cancel := context.WithCancel(context.Background())
	g.mu.Lock()
	defer g.mu.Unlock()
	g.seq++
	cs := &chatSession{
		ID:         fmt.Sprintf("c%d", g.seq),
		Instance:   instance,
		Project:    detail.ProjectPath,
		IID:        detail.IID,
		Ref:        detail.Ref(),
		Title:      detail.Title,
		WebURL:     detail.WebURL,
		Focus:      focus,
		Started:    time.Now(),
		deps:       d,
		cfg:        d.cfgFor(detail.ProjectPath),
		detail:     detail,
		diffs:      diffs,
		ctx:        ctx,
		endSession: cancel,
		subs:       map[chan chatEvent]struct{}{},
	}
	g.chats[cs.ID] = cs
	return cs
}

// closeAll ends every conversation; used on server shutdown so no
// worktrees are left behind.
func (g *chatRegistry) closeAll(ctx context.Context) {
	g.mu.Lock()
	sessions := make([]*chatSession, 0, len(g.chats))
	for _, cs := range g.chats {
		sessions = append(sessions, cs)
	}
	g.mu.Unlock()
	for _, cs := range sessions {
		cs.close(ctx)
	}
}
