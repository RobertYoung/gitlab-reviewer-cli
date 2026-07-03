package claudecli

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review"
)

func testChatRequest(t *testing.T) review.ChatRequest {
	return review.ChatRequest{
		RepoPath: t.TempDir(),
		MR: gitlabx.MRDetail{
			MRSummary: gitlabx.MRSummary{IID: 7, Title: "Fix auth", SourceBranch: "fix", TargetBranch: "main"},
		},
		Diffs:   []gitlabx.FileDiff{{OldPath: "a.go", NewPath: "a.go", Diff: "@@ -1 +1 @@\n-x\n+y\n"}},
		Message: "Is the nil check right?",
		Timeout: 30 * time.Second,
	}
}

func TestChatHappyPath(t *testing.T) {
	b := backend(t, "chat.jsonl")
	b.DumpDir = t.TempDir()

	var events []review.Event
	reply, err := b.Chat(context.Background(), testChatRequest(t), func(e review.Event) { events = append(events, e) })
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(reply.Text, "The nil check is correct.") || !strings.Contains(reply.Text, "optional expiry claim") {
		t.Errorf("reply text = %q", reply.Text)
	}
	if reply.SessionID != "sess-chat" || reply.CostUSD != 0.03 {
		t.Errorf("meta: %+v", reply)
	}

	var texts []string
	for _, e := range events {
		texts = append(texts, e.Text)
	}
	joined := strings.Join(texts, "|")
	if !strings.Contains(joined, "Read internal/auth/token.go") {
		t.Errorf("missing tool event: %v", texts)
	}

	dumps, _ := filepath.Glob(filepath.Join(b.DumpDir, "chat-7-*.jsonl"))
	if len(dumps) != 1 {
		t.Errorf("expected one chat dump, got %v", dumps)
	}
}

func TestChatEmptyReply(t *testing.T) {
	_, err := backend(t, "chat-empty.jsonl").Chat(context.Background(), testChatRequest(t), nil)
	if err == nil || !strings.Contains(err.Error(), "empty reply") {
		t.Errorf("want empty-reply error, got %v", err)
	}
}

func TestChatErrorResult(t *testing.T) {
	_, err := backend(t, "error.jsonl").Chat(context.Background(), testChatRequest(t), nil)
	if err == nil || !strings.Contains(err.Error(), "Credit balance is too low") {
		t.Errorf("want claude error surfaced, got %v", err)
	}
}

func TestBuildChatArgs(t *testing.T) {
	find := func(args []string, flag string) string {
		for i, a := range args {
			if a == flag && i+1 < len(args) {
				return args[i+1]
			}
		}
		return ""
	}
	has := func(args []string, flag string) bool { return slices.Contains(args, flag) }

	t.Run("first turn is read-only without a schema", func(t *testing.T) {
		args := (&Backend{Model: "m"}).buildChatArgs(review.ChatRequest{})
		if has(args, "--json-schema") {
			t.Error("chat must not request the findings schema")
		}
		if has(args, "--resume") {
			t.Error("first turn must not resume")
		}
		if got := find(args, "--tools"); got != "Read,Grep,Glob" {
			t.Errorf("tools = %q", got)
		}
		disallowed := find(args, "--disallowedTools")
		for _, want := range []string{"Bash", "Edit", "Write", "Task"} {
			if !strings.Contains(disallowed, want) {
				t.Errorf("disallowed missing %s: %q", want, disallowed)
			}
		}
		if got := find(args, "--append-system-prompt"); !strings.Contains(got, "helping a human reviewer") {
			t.Errorf("system prompt = %q", got)
		}
		if got := find(args, "--model"); got != "m" {
			t.Errorf("model = %q", got)
		}
	})

	t.Run("later turns resume the session", func(t *testing.T) {
		args := (&Backend{}).buildChatArgs(review.ChatRequest{SessionID: "sess-1"})
		if got := find(args, "--resume"); got != "sess-1" {
			t.Errorf("resume = %q", got)
		}
	})
}
