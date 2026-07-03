package tui

import (
	"context"
	"errors"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/config"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review"
)

type (
	// chatReadyMsg reports the checkout the conversation runs in.
	chatReadyMsg struct {
		iid     int64
		path    string
		cleanup func(context.Context) error
		err     error
	}
	// chatEventMsg is one progress line: checkout output, or tool use and
	// status while Claude works on a reply.
	chatEventMsg struct {
		iid  int64
		text string
	}
	// chatReplyMsg carries one finished turn.
	chatReplyMsg struct {
		iid   int64
		reply *review.ChatReply
		err   error
	}
)

var (
	chatYouStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("10"))
	chatClaudeStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
)

// chatEntry is one message of the conversation transcript.
type chatEntry struct {
	you  bool
	text string
}

// chatScreen is a conversation with Claude about the MR — the whole change,
// or one diff line — running inside the MR's checkout so Claude can explore
// the code while answering. The checkout lives for the whole conversation
// (the CLI session resumes from it) and is cleaned up when the screen pops.
type chatScreen struct {
	deps    Deps
	detail  gitlabx.MRDetail
	diffs   []gitlabx.FileDiff
	focus   *review.ChatFocus // nil chats about the whole MR
	excerpt string            // rendered diff line shown under the header
	cfg     config.Config

	// sessionCtx spans the conversation; endSession cancels the checkout,
	// any in-flight turn, and the claude subprocess with it.
	sessionCtx context.Context
	endSession context.CancelFunc
	cancelTurn context.CancelFunc

	repoPath  string
	cleanup   func(context.Context) error
	preparing bool

	claudeSession string // backend session ID, set after the first turn
	transcript    []chatEntry
	busy          bool
	status        string // latest progress line while preparing or busy
	err           error

	ch   chan tea.Msg
	vp   viewport.Model
	ta   textarea.Model
	spin spinner.Model

	width  int
	height int
}

func newChatScreen(deps Deps, detail gitlabx.MRDetail, diffs []gitlabx.FileDiff, focus *review.ChatFocus, excerpt string) *chatScreen {
	ta := textarea.New()
	ta.ShowLineNumbers = false
	return &chatScreen{
		deps:    deps,
		detail:  detail,
		diffs:   diffs,
		focus:   focus,
		excerpt: excerpt,
		cfg:     deps.cfgFor(detail.ProjectPath),
		vp:      viewport.New(),
		ta:      ta,
		spin:    spinner.New(spinner.WithSpinner(spinner.MiniDot)),
	}
}

func (s *chatScreen) subject() string {
	if s.focus == nil {
		return s.detail.Ref()
	}
	return s.focus.Label()
}

func (s *chatScreen) Title() string { return "chat · " + s.subject() }

// Typing reports that the textarea captures keystrokes, so "?" stays literal.
func (s *chatScreen) Typing() bool { return true }

func (s *chatScreen) Hints() string {
	if s.busy {
		return "esc cancel reply · pgup/pgdn scroll"
	}
	return "ctrl+s send · pgup/pgdn scroll · esc end chat"
}

func (s *chatScreen) Init() tea.Cmd {
	s.sessionCtx, s.endSession = context.WithCancel(context.Background())
	s.ch = make(chan tea.Msg, 64)
	s.preparing = true
	s.status = "preparing repository…"
	go s.prepare()
	return tea.Batch(s.spin.Tick, s.ta.Focus(), s.wait())
}

// prepare runs the checkout off the UI goroutine; the conversation needs
// the repository on disk before the first message can be sent.
func (s *chatScreen) prepare() {
	iid := s.detail.IID
	path, cleanup, err := s.deps.Checkout(s.sessionCtx, s.detail, func(line string) {
		s.ch <- chatEventMsg{iid: iid, text: line}
	})
	s.ch <- chatReadyMsg{iid: iid, path: path, cleanup: cleanup, err: err}
}

// wait pumps exactly one message from the chat goroutines into the UI; it
// is re-issued from Update after each message, for the whole conversation.
func (s *chatScreen) wait() tea.Cmd {
	return func() tea.Msg { return <-s.ch }
}

// send starts one turn with the composer's content.
func (s *chatScreen) send() tea.Cmd {
	msg := strings.TrimSpace(s.ta.Value())
	if msg == "" || s.busy || s.preparing || s.repoPath == "" {
		return nil
	}
	s.transcript = append(s.transcript, chatEntry{you: true, text: msg})
	s.ta.Reset()
	s.busy = true
	s.err = nil
	s.status = "thinking…"

	turnCtx, cancel := context.WithCancel(s.sessionCtx)
	s.cancelTurn = cancel
	req := review.ChatRequest{
		RepoPath:  s.repoPath,
		MR:        s.detail,
		Diffs:     s.diffs,
		Focus:     s.focus,
		MaxDiffKB: s.cfg.Review.MaxDiffKB,
		Message:   msg,
		SessionID: s.claudeSession,
		Timeout:   s.cfg.Review.Timeout,
	}
	iid, chatter, ch := s.detail.IID, s.deps.Chatter, s.ch
	go func() {
		reply, err := chatter.Chat(turnCtx, req, func(e review.Event) {
			ch <- chatEventMsg{iid: iid, text: e.Text}
		})
		ch <- chatReplyMsg{iid: iid, reply: reply, err: err}
	}()
	s.refresh()
	return nil
}

// close ends the conversation: the session context dies with any in-flight
// subprocess, and the worktree is released in the background.
func (s *chatScreen) close() {
	s.endSession()
	if s.cleanup != nil {
		cleanup := s.cleanup
		s.cleanup = nil
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_ = cleanup(ctx)
		}()
	}
}

func (s *chatScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		s.width, s.height = msg.Width, msg.Height
		s.layout()
		s.refresh()
		return s, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		s.spin, cmd = s.spin.Update(msg)
		return s, cmd

	case chatEventMsg:
		if msg.iid != s.detail.IID {
			return s, nil
		}
		s.status = msg.text
		return s, s.wait()

	case chatReadyMsg:
		if msg.iid != s.detail.IID {
			return s, nil
		}
		s.preparing = false
		s.status = ""
		if msg.err != nil {
			s.err = msg.err
		} else {
			s.repoPath, s.cleanup = msg.path, msg.cleanup
		}
		s.refresh()
		return s, s.wait()

	case chatReplyMsg:
		if msg.iid != s.detail.IID {
			return s, nil
		}
		s.busy = false
		s.status = ""
		s.cancelTurn = nil
		switch {
		case msg.err != nil && errors.Is(msg.err, context.Canceled):
			// Turn cancelled by the user; the conversation continues.
		case msg.err != nil:
			s.err = msg.err
		case msg.reply != nil:
			s.claudeSession = msg.reply.SessionID
			s.transcript = append(s.transcript, chatEntry{text: msg.reply.Text})
		}
		s.refresh()
		return s, s.wait()

	case tea.KeyPressMsg:
		switch msg.String() {
		case "esc":
			if s.busy && s.cancelTurn != nil {
				s.status = "cancelling…"
				s.cancelTurn()
				return s, nil
			}
			s.close()
			return s, popScreen
		case "ctrl+s":
			return s, s.send()
		case "pgup", "pgdown", "ctrl+u", "ctrl+d":
			var cmd tea.Cmd
			s.vp, cmd = s.vp.Update(msg)
			return s, cmd
		}
	}

	var cmd tea.Cmd
	s.ta, cmd = s.ta.Update(msg)
	return s, cmd
}

// headerHeight is what the header renders above the transcript: title line,
// optional excerpt, and a blank separator.
func (s *chatScreen) headerHeight() int {
	h := 2
	if s.excerpt != "" {
		h++
	}
	return h
}

const chatComposerHeight = 4

func (s *chatScreen) layout() {
	if s.width == 0 {
		return
	}
	s.ta.SetWidth(max(s.width-2, 20))
	s.ta.SetHeight(chatComposerHeight - 1)
	// One line between transcript and composer carries status or a hint.
	s.vp.SetWidth(s.width)
	s.vp.SetHeight(max(s.height-s.headerHeight()-chatComposerHeight-1, 3))
}

// refresh rebuilds the transcript viewport and follows the newest message.
func (s *chatScreen) refresh() {
	width := max(s.vp.Width()-2, 30)
	body := lipgloss.NewStyle().Width(width)
	var b strings.Builder
	if s.focus == nil {
		b.WriteString(subtleStyle.Render("Ask Claude anything about this merge request — it reads the code from a checkout of the MR branch.") + "\n")
	} else {
		b.WriteString(subtleStyle.Render("Ask Claude about this line — it reads the surrounding code from a checkout of the MR branch.") + "\n")
	}
	for _, e := range s.transcript {
		b.WriteByte('\n')
		if e.you {
			b.WriteString(chatYouStyle.Render("you") + "\n")
		} else {
			b.WriteString(chatClaudeStyle.Render("claude") + "\n")
		}
		b.WriteString(body.Render(e.text) + "\n")
	}
	if s.err != nil {
		b.WriteByte('\n')
		b.WriteString(errorStyle.Render("error") + "\n")
		b.WriteString(body.Render(s.err.Error()) + "\n")
	}
	s.vp.SetContent(b.String())
	s.vp.GotoBottom()
}

func (s *chatScreen) View() string {
	var b strings.Builder
	b.WriteString(headerStyle.Render("💬 chat about "+s.subject()) + "  " + subtleStyle.Render(truncate(s.detail.Title, max(s.width-lipgloss.Width(s.subject())-16, 10))) + "\n")
	if s.excerpt != "" {
		b.WriteString(s.excerpt + "\n")
	}
	b.WriteByte('\n')
	b.WriteString(s.vp.View() + "\n")

	switch {
	case s.preparing || s.busy:
		b.WriteString(s.spin.View() + " " + subtleStyle.Render(truncate(s.status, max(s.width-4, 20))) + "\n")
	case s.repoPath == "" && s.err != nil:
		b.WriteString(errorStyle.Render("the checkout failed — press esc to go back") + "\n")
	default:
		b.WriteString(subtleStyle.Render("ctrl+s sends · markdown supported") + "\n")
	}
	b.WriteString(s.ta.View())
	return b.String()
}
