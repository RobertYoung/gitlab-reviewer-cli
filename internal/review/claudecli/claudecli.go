// Package claudecli runs reviews by shelling out to the Claude Code CLI in
// headless mode (claude -p, stream-json output, structured output schema).
// It is the only package that knows the claude binary exists.
package claudecli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/config"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review"
)

// MinVersion is the oldest claude CLI this backend is tested against.
var MinVersion = [2]int{2, 0}

const installHint = "install it from https://docs.anthropic.com/en/docs/claude-code (e.g. `npm install -g @anthropic-ai/claude-code` or `brew install claude-code`)"

// Backend implements review.Reviewer via the claude CLI.
type Backend struct {
	// ClaudePath is the binary to run ("claude" resolved on PATH by default).
	ClaudePath string
	Provider   string // anthropic | bedrock
	Model      string
	Bare       bool
	// UseAgents grants the Task tool so the reviewer can delegate to
	// Claude Code subagents; write/exec tools stay denied throughout.
	UseAgents bool
	Bedrock   config.Bedrock
	ExtraEnv  map[string]string
	// DumpDir receives raw stream transcripts for debugging; empty disables.
	DumpDir string
	// LookupEnv defaults to os.LookupEnv; injectable for tests.
	LookupEnv func(string) (string, bool)
}

// New builds a Backend from configuration.
func New(cfg config.Config, dumpDir string) *Backend {
	return &Backend{
		ClaudePath: cfg.Review.ClaudePath,
		Provider:   cfg.Review.Provider,
		Model:      cfg.Review.Model,
		Bare:       cfg.Review.Bare,
		UseAgents:  cfg.Review.UseAgents,
		Bedrock:    cfg.Bedrock,
		ExtraEnv:   cfg.Review.Env,
		DumpDir:    dumpDir,
	}
}

func (b *Backend) Name() string { return "claude-cli" }

// The backend serves both the review pipeline and MR conversations.
var (
	_ review.Reviewer = (*Backend)(nil)
	_ review.Chatter  = (*Backend)(nil)
)

var versionRe = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)

// CheckAvailable verifies the claude binary exists and is new enough.
func (b *Backend) CheckAvailable(ctx context.Context) error {
	path, err := exec.LookPath(b.ClaudePath)
	if err != nil {
		return fmt.Errorf("the claude CLI was not found (looked for %q): %s", b.ClaudePath, installHint)
	}
	out, err := exec.CommandContext(ctx, path, "--version").Output() //nolint:gosec // path comes from user config by design
	if err != nil {
		return fmt.Errorf("%s --version failed: %w", path, err)
	}
	m := versionRe.FindStringSubmatch(string(out))
	if m == nil {
		return fmt.Errorf("could not parse claude version from %q", strings.TrimSpace(string(out)))
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	if major < MinVersion[0] || (major == MinVersion[0] && minor < MinVersion[1]) {
		return fmt.Errorf("claude CLI %s is too old (need >= %d.%d): update it, e.g. `claude update`",
			strings.TrimSpace(string(out)), MinVersion[0], MinVersion[1])
	}
	return nil
}

// invocation is the outcome of one claude subprocess run: the terminal
// result event (nil when the stream ended without one), the captured
// stderr, the transcript dump location, and the exit/stream errors.
type invocation struct {
	final     *streamEvent
	stderr    string
	dumpPath  string
	waitErr   error
	streamErr error
}

// noResult builds the shared "stream ended without a result event" error.
func (inv *invocation) noResult() error {
	detail := strings.TrimSpace(inv.stderr)
	if inv.streamErr != nil && detail == "" {
		detail = inv.streamErr.Error()
	}
	return fmt.Errorf("claude produced no result event (%w): %s%s", errOrExit(inv.waitErr), detail, dumpSuffix(inv.dumpPath))
}

// invoke runs one claude subprocess in dir, feeding stdin and streaming
// progress through onEvent. The transcript is dumped under dumpName.
func (b *Backend) invoke(ctx context.Context, dir string, args []string, stdin string, onEvent func(review.Event), dumpName string) (*invocation, error) {
	cmd := exec.CommandContext(ctx, b.ClaudePath, args...) //nolint:gosec // running the user-configured claude binary is the point
	cmd.Dir = dir
	cmd.Env = b.subprocessEnv()
	cmd.Stdin = strings.NewReader(stdin)
	// Kill the whole process group on cancel so MCP/tool children die too.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGINT)
	}
	cmd.WaitDelay = 10 * time.Second

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr strings.Builder
	cmd.Stderr = &limitedWriter{w: &stderr, n: 8 * 1024}

	onEvent(review.Event{Kind: review.EventStatus, Text: "starting claude…"})
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting %s: %w: %s", b.ClaudePath, err, installHint)
	}

	final, transcript, streamErr := b.consumeStream(stdout, onEvent)
	waitErr := cmd.Wait()
	return &invocation{
		final:     final,
		stderr:    stderr.String(),
		dumpPath:  b.dump(transcript, dumpName),
		waitErr:   waitErr,
		streamErr: streamErr,
	}, nil
}

// Review runs one review in req.RepoPath and parses the structured output.
func (b *Backend) Review(ctx context.Context, req review.Request, onEvent func(review.Event)) (*review.Result, error) {
	if onEvent == nil {
		onEvent = func(review.Event) {}
	}
	if req.AgentName != "" {
		inner := onEvent
		onEvent = func(e review.Event) {
			e.Agent = req.AgentName
			inner(e)
		}
	}
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}

	dumpName := fmt.Sprintf("review-%d", req.MR.IID)
	if req.AgentName != "" {
		// Concurrent agent passes dump in the same second; the agent name
		// keeps their transcripts apart.
		dumpName = fmt.Sprintf("review-%d-%s", req.MR.IID, req.AgentName)
	}
	inv, err := b.invoke(ctx, req.RepoPath, b.buildArgs(req), review.BuildUserPrompt(req), onEvent, dumpName)
	if err != nil {
		return nil, err
	}

	if ctx.Err() != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("review timed out after %s", req.Timeout)
		}
		return nil, ctx.Err()
	}
	if inv.final == nil {
		return nil, inv.noResult()
	}
	if inv.final.IsError {
		return nil, fmt.Errorf("claude reported an error: %s%s", firstNonEmpty(inv.final.Result, inv.final.Error, inv.stderr), dumpSuffix(inv.dumpPath))
	}

	res, err := b.parseFinal(inv.final)
	if err != nil {
		return nil, fmt.Errorf("%w (session %s)%s", err, inv.final.SessionID, dumpSuffix(inv.dumpPath))
	}
	res.SessionID = inv.final.SessionID
	res.CostUSD = inv.final.TotalCostUSD
	return res, nil
}

// Chat runs one conversation turn in req.RepoPath. The first turn sends the
// full MR context; later turns resume the CLI session (which lives with the
// checkout directory, so RepoPath must not change mid-conversation).
func (b *Backend) Chat(ctx context.Context, req review.ChatRequest, onEvent func(review.Event)) (*review.ChatReply, error) {
	if onEvent == nil {
		onEvent = func(review.Event) {}
	}
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}

	stdin := review.BuildChatPrompt(req)
	if req.SessionID != "" {
		stdin = req.Message
	}
	inv, err := b.invoke(ctx, req.RepoPath, b.buildChatArgs(req), stdin, onEvent, fmt.Sprintf("chat-%d", req.MR.IID))
	if err != nil {
		return nil, err
	}

	if ctx.Err() != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("chat turn timed out after %s", req.Timeout)
		}
		return nil, ctx.Err()
	}
	if inv.final == nil {
		return nil, inv.noResult()
	}
	if inv.final.IsError {
		return nil, fmt.Errorf("claude reported an error: %s%s", firstNonEmpty(inv.final.Result, inv.final.Error, inv.stderr), dumpSuffix(inv.dumpPath))
	}
	text := strings.TrimSpace(inv.final.Result)
	if text == "" {
		return nil, fmt.Errorf("claude returned an empty reply (session %s)%s", inv.final.SessionID, dumpSuffix(inv.dumpPath))
	}
	return &review.ChatReply{
		Text:      text,
		SessionID: inv.final.SessionID,
		CostUSD:   inv.final.TotalCostUSD,
	}, nil
}

// toolArgs returns the read-only tool grant shared by every session:
// mutating and network tools are denied as permission rules, which cascade
// into subagents when UseAgents grants the Task tool.
func (b *Backend) toolArgs() (tools, disallowed string) {
	if b.UseAgents {
		return "Read,Grep,Glob,Task", "Bash,Edit,Write,NotebookEdit,WebFetch,WebSearch"
	}
	return "Read,Grep,Glob", "Bash,Edit,Write,NotebookEdit,WebFetch,WebSearch,Task,Agent"
}

// buildArgs assembles the headless review invocation.
func (b *Backend) buildArgs(req review.Request) []string {
	tools, disallowed := b.toolArgs()
	args := []string{
		"-p",
		"--output-format", "stream-json",
		"--verbose",
		"--json-schema", review.OutputSchema,
		"--tools", tools,
		"--permission-mode", "dontAsk",
		"--disallowedTools", disallowed,
		"--strict-mcp-config",
		"--append-system-prompt", review.FullSystemPrompt(req),
	}
	if b.Model != "" {
		args = append(args, "--model", b.Model)
	}
	if req.MaxBudgetUSD > 0 {
		args = append(args, "--max-budget-usd", strconv.FormatFloat(req.MaxBudgetUSD, 'f', -1, 64))
	}
	if b.Bare {
		args = append(args, "--bare")
	}
	return args
}

// buildChatArgs assembles the headless chat invocation: same read-only
// sandbox as reviews, conversational persona instead of the finding schema,
// and session resume on follow-up turns.
func (b *Backend) buildChatArgs(req review.ChatRequest) []string {
	tools, disallowed := b.toolArgs()
	args := []string{
		"-p",
		"--output-format", "stream-json",
		"--verbose",
		"--tools", tools,
		"--permission-mode", "dontAsk",
		"--disallowedTools", disallowed,
		"--strict-mcp-config",
		"--append-system-prompt", review.ChatSystemPrompt,
	}
	if req.SessionID != "" {
		args = append(args, "--resume", req.SessionID)
	}
	if b.Model != "" {
		args = append(args, "--model", b.Model)
	}
	if b.Bare {
		args = append(args, "--bare")
	}
	return args
}

// parseFinal extracts findings from the result event, degrading gracefully:
// structured_output first, then JSON (possibly fenced) in the result text.
func (b *Backend) parseFinal(final *streamEvent) (*review.Result, error) {
	if len(final.StructuredOutput) > 0 && string(final.StructuredOutput) != "null" {
		return review.ParseResult(final.StructuredOutput)
	}
	text := strings.TrimSpace(final.Result)
	if fenced := extractFencedJSON(text); fenced != "" {
		text = fenced
	}
	if strings.HasPrefix(text, "{") {
		res, err := review.ParseResult([]byte(text))
		if err == nil {
			res.Warnings = append(res.Warnings, "structured output missing; findings recovered from result text")
			return res, nil
		}
	}
	return nil, errors.New("claude returned no structured output and the result text is not parseable findings JSON")
}

var fenceRe = regexp.MustCompile("(?s)```(?:json)?\\s*(\\{.*?\\})\\s*```")

func extractFencedJSON(s string) string {
	if m := fenceRe.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return ""
}

// streamEvent is the subset of claude's stream-json events we act on.
type streamEvent struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	SessionID string `json:"session_id"`
	Model     string `json:"model"`
	Message   *struct {
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
	} `json:"message"`
	Result           string          `json:"result"`
	Error            string          `json:"error"`
	StructuredOutput json.RawMessage `json:"structured_output"`
	TotalCostUSD     float64         `json:"total_cost_usd"`
	IsError          bool            `json:"is_error"`
	NumTurns         int             `json:"num_turns"`
}

// consumeStream decodes NDJSON events, forwarding progress and returning
// the terminal result event plus the full transcript.
func (b *Backend) consumeStream(r io.Reader, onEvent func(review.Event)) (*streamEvent, []byte, error) {
	var (
		transcript strings.Builder
		final      *streamEvent
	)
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 32*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		transcript.Write(line)
		transcript.WriteByte('\n')

		var ev streamEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue // tolerate non-JSON noise on stdout
		}
		switch ev.Type {
		case "system":
			switch ev.Subtype {
			case "init":
				onEvent(review.Event{Kind: review.EventInit, Text: "session started (model " + ev.Model + ")"})
			case "api_retry":
				onEvent(review.Event{Kind: review.EventRetry, Text: "API error, retrying…"})
			}
		case "assistant":
			if ev.Message == nil {
				continue
			}
			for _, c := range ev.Message.Content {
				switch c.Type {
				case "tool_use":
					if c.Name == "StructuredOutput" {
						onEvent(review.Event{Kind: review.EventStatus, Text: "writing findings…"})
					} else {
						onEvent(review.Event{Kind: review.EventToolUse, Text: c.Name + " " + toolTarget(c.Input)})
					}
				case "text":
					if t := strings.TrimSpace(c.Text); t != "" {
						onEvent(review.Event{Kind: review.EventText, Text: firstLine(t)})
					}
				}
			}
		case "result":
			evCopy := ev
			final = &evCopy
		}
	}
	return final, []byte(transcript.String()), scanner.Err()
}

// toolTarget pulls the most useful argument out of a tool call for display.
func toolTarget(input json.RawMessage) string {
	var args struct {
		FilePath     string `json:"file_path"`
		Path         string `json:"path"`
		Pattern      string `json:"pattern"`
		SubagentType string `json:"subagent_type"`
		Description  string `json:"description"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return ""
	}
	return firstNonEmpty(args.FilePath, args.Pattern, args.Path, args.SubagentType, args.Description)
}

// subprocessEnv builds a minimal environment: shell basics, proxy settings,
// provider credentials — and never the GitLab token.
func (b *Backend) subprocessEnv() []string {
	lookup := b.LookupEnv
	if lookup == nil {
		lookup = os.LookupEnv
	}

	passthrough := []string{
		"HOME", "PATH", "TERM", "SHELL", "USER", "LANG", "LC_ALL", "TMPDIR",
		"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy",
		"NODE_EXTRA_CA_CERTS", "SSL_CERT_FILE",
	}
	var env []string
	add := func(k, v string) { env = append(env, k+"="+v) }
	for _, k := range passthrough {
		if v, ok := lookup(k); ok {
			add(k, v)
		}
	}

	switch b.Provider {
	case "bedrock":
		add("CLAUDE_CODE_USE_BEDROCK", "1")
		if b.Bedrock.Region != "" {
			add("AWS_REGION", b.Bedrock.Region)
		}
		if b.Bedrock.Profile != "" {
			add("AWS_PROFILE", b.Bedrock.Profile)
		}
		// Credentials and SSO caches resolve via the ambient AWS env.
		for _, k := range []string{
			"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN",
			"AWS_CONFIG_FILE", "AWS_SHARED_CREDENTIALS_FILE",
			"AWS_BEARER_TOKEN_BEDROCK", "AWS_DEFAULT_REGION",
		} {
			if v, ok := lookup(k); ok {
				add(k, v)
			}
		}
	default: // anthropic
		for _, k := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN"} {
			if v, ok := lookup(k); ok {
				add(k, v)
			}
		}
	}

	for k, v := range b.ExtraEnv {
		// The GitLab token must never reach the model subprocess.
		if strings.HasPrefix(k, "GITLAB") {
			continue
		}
		add(k, v)
	}
	return env
}

func (b *Backend) dump(transcript []byte, name string) string {
	if b.DumpDir == "" || len(transcript) == 0 {
		return ""
	}
	if err := os.MkdirAll(b.DumpDir, 0o700); err != nil {
		return ""
	}
	path := filepath.Join(b.DumpDir, fmt.Sprintf("%s-%d.jsonl", name, time.Now().Unix()))
	if err := os.WriteFile(path, transcript, 0o600); err != nil {
		return ""
	}
	return path
}

type limitedWriter struct {
	w io.Writer
	n int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.n <= 0 {
		return len(p), nil
	}
	if len(p) > l.n {
		p = p[:l.n]
	}
	n, err := l.w.Write(p)
	l.n -= n
	if err != nil {
		return n, err
	}
	return len(p), nil
}

func dumpSuffix(path string) string {
	if path == "" {
		return ""
	}
	return " — raw transcript: " + path
}

func errOrExit(err error) error {
	if err == nil {
		return errors.New("process exited cleanly")
	}
	return err
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	if len(line) > 120 {
		line = line[:120] + "…"
	}
	return line
}
