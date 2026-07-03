package review

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx"
)

// ChatFocus narrows a chat to one line of the MR diff.
type ChatFocus struct {
	File string // new path, repo-relative, as shown in the diff headers
	Line LineRef
}

// Label names the focus for titles and prompts: "path:42" or "path:42(old)".
func (f ChatFocus) Label() string {
	switch {
	case f.Line.NewLine != nil:
		return fmt.Sprintf("%s:%d", f.File, *f.Line.NewLine)
	case f.Line.OldLine != nil:
		return fmt.Sprintf("%s:%d(old)", f.File, *f.Line.OldLine)
	default:
		return f.File
	}
}

// ChatRequest is everything a backend needs to run one chat turn. The first
// turn of a conversation (empty SessionID) carries the MR context; later
// turns resume the backend session and carry only the new message.
type ChatRequest struct {
	// RepoPath is the checkout the chat runs in (the subprocess cwd). It must
	// stay the same across the turns of one conversation: the backend session
	// is resumed from there.
	RepoPath string
	// MR carries metadata shown to the model (title, description, branches).
	MR gitlabx.MRDetail
	// Diffs is the MR's diff set; the prompt builder bounds what is inlined.
	// Only consulted on the first turn.
	Diffs []gitlabx.FileDiff
	// Focus narrows the conversation to one diff line; nil chats about the
	// whole MR.
	Focus *ChatFocus
	// MaxDiffKB bounds the diff inlined into the first turn's prompt; <= 0
	// falls back to a conservative default.
	MaxDiffKB int
	// Message is the user's message for this turn.
	Message string
	// SessionID resumes an earlier conversation; empty starts a new one.
	SessionID string
	// Timeout bounds one turn, not the whole conversation.
	Timeout time.Duration
}

// ChatReply is one completed chat turn.
type ChatReply struct {
	// Text is the model's answer, GitLab-flavoured markdown.
	Text string
	// SessionID identifies the backend conversation; pass it back in the
	// next ChatRequest to continue with full context.
	SessionID string
	CostUSD   float64
}

// Chatter holds a conversation about an MR inside its checkout. onEvent
// receives progress (tool use, status) while a turn runs; the reply text
// arrives complete when the turn finishes.
type Chatter interface {
	Chat(ctx context.Context, req ChatRequest, onEvent func(Event)) (*ChatReply, error)
}

// ChatSystemPrompt is the persona for MR conversations: a knowledgeable
// pair of eyes, not the finding-reporting reviewer contract.
const ChatSystemPrompt = `You are an expert engineer helping a human reviewer understand a GitLab
merge request. You are running inside a checkout of the repository at the
merge request's head commit: use your Read/Grep/Glob tools to explore the
surrounding code, callers, and tests whenever a question needs more context
than the diff shows.

Guidelines:
- Answer the reviewer's questions directly, in GitLab-flavoured markdown.
  Be concise; this is a conversation, not a report.
- Ground your answers in the actual code — read the relevant files before
  speculating, and cite files and line numbers when it helps.
- When asked about a specific line, explain it in the context of the change
  around it and the merge request's intent.
- Be candid about problems you notice, and equally candid when the code is
  fine as it is.
- You are read-only: you cannot edit files, run commands, or post comments.
  When the reviewer wants a change, show the concrete code they could apply.`

// chatDiffBudgetKB bounds the inline diff on the first turn when the
// request does not say otherwise.
const chatDiffBudgetKB = 256

// BuildChatPrompt renders the opening turn of a conversation: MR metadata,
// the (bounded) diff under discussion, the focus line if any, then the
// user's first message. Later turns resume the backend session and send the
// message alone.
func BuildChatPrompt(req ChatRequest) string {
	var b strings.Builder

	fmt.Fprintf(&b, "The reviewer wants to discuss merge request !%d: %s\n", req.MR.IID, req.MR.Title)
	fmt.Fprintf(&b, "Branches: %s → %s\n", req.MR.SourceBranch, req.MR.TargetBranch)
	if desc := strings.TrimSpace(req.MR.Description); desc != "" {
		b.WriteString("\nMR description:\n")
		b.WriteString(desc)
		b.WriteString("\n")
	}

	if req.Focus != nil {
		writeChatFocus(&b, req)
	} else {
		writeChatDiffs(&b, req, req.Diffs)
	}

	b.WriteString("\nThe reviewer's first message follows:\n\n")
	b.WriteString(strings.TrimSpace(req.Message))
	b.WriteString("\n")
	return b.String()
}

// writeChatFocus inlines the focused file's diff and points at the line the
// conversation is about.
func writeChatFocus(b *strings.Builder, req ChatRequest) {
	focus := *req.Focus
	var focused []gitlabx.FileDiff
	for _, d := range req.Diffs {
		if d.NewPath == focus.File || d.OldPath == focus.File {
			focused = append(focused, d)
			break
		}
	}
	writeChatDiffs(b, req, focused)

	switch {
	case focus.Line.NewLine != nil:
		fmt.Fprintf(b, "\nThe conversation is about line %d of %s (new side of the diff).\n", *focus.Line.NewLine, focus.File)
	case focus.Line.OldLine != nil:
		fmt.Fprintf(b, "\nThe conversation is about line %d of the old version of %s (a removed line).\n", *focus.Line.OldLine, focus.File)
	default:
		fmt.Fprintf(b, "\nThe conversation is about the file %s.\n", focus.File)
	}
}

// writeChatDiffs inlines diffs in order until the budget is spent; whatever
// does not fit is listed for the model to read from the checkout instead.
func writeChatDiffs(b *strings.Builder, req ChatRequest, diffs []gitlabx.FileDiff) {
	budgetKB := req.MaxDiffKB
	if budgetKB <= 0 {
		budgetKB = chatDiffBudgetKB
	}
	budget := budgetKB * 1024

	var inline, omitted []gitlabx.FileDiff
	spent := 0
	for _, d := range diffs {
		if d.TooLarge || spent+len(d.Diff) > budget {
			omitted = append(omitted, d)
			continue
		}
		inline = append(inline, d)
		spent += len(d.Diff)
	}

	if len(inline) > 0 {
		b.WriteString("\nThe merge request's diff follows. Each hunk header shows old and new line\nnumbers; line references in the conversation match these headers.\n")
		for _, d := range inline {
			fmt.Fprintf(b, "\n--- a/%s\n+++ b/%s\n", d.OldPath, d.NewPath)
			b.WriteString(strings.TrimSuffix(d.Diff, "\n"))
			b.WriteString("\n")
		}
	}
	if len(omitted) > 0 {
		b.WriteString("\nThe following files also changed in this MR, but their diffs were too\nlarge to include here. The checkout is at the MR head commit; Read them\ndirectly when the conversation needs them:\n")
		for _, d := range omitted {
			fmt.Fprintf(b, "- %s\n", d.Path())
		}
	}
}
