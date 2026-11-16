package config

import (
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/knadh/koanf/providers/structs"
	"github.com/knadh/koanf/v2"
)

// Option is the hand-written documentation metadata for one settings key.
// Everything that can be read from the code instead (environment variable,
// flag, default, Go type) is derived in Reference, so only prose and the
// allowed values live here; TestSettings keeps both halves in step with the
// struct, the env and flag tables, and Validate.
type Option struct {
	Key string
	// Type overrides the type name derived from the Go field, for values
	// with more structure than their Go type shows (a URL, a template).
	Type string
	// Allowed lists the values Validate accepts for an enumerated key.
	Allowed []string
	// Constraint is a short range or rule shown next to the type.
	Constraint  string
	Description string
	// DefaultText describes a default the zero value cannot express, such
	// as "claude CLI default". Only valid when the real default is empty.
	DefaultText string
	Secret      bool
	Deprecated  bool
}

// Settings documents every settings key, in reference order.
var Settings = []Option{
	{Key: "gitlab.base_url", Type: "URL", Description: "Base URL of the GitLab instance. Replaced by the selected entry's `base_url` when `gitlab.instances` is set."},
	{Key: "gitlab.token", Secret: true, Description: "Personal, project or group access token with the `api` scope. Required unless every instance supplies its own token. Prefer the environment variable: flags are visible in `ps` and shell history."},
	{Key: "gitlab.projects", Description: "Full project paths to browse, for example `mygroup/myapp`. Empty, together with `gitlab.groups`, opens the TUI's group and project picker."},
	{Key: "gitlab.groups", Description: "Group paths whose merge requests to browse."},
	{Key: "gitlab.per_page", Constraint: "1 to 100", Description: "GitLab API page size."},
	{Key: "gitlab.instances", Description: "Named GitLab instances to choose between, each with `name`, `base_url`, and optionally `token` or `token_env`. One is selected at startup and replaces `gitlab.base_url` and `gitlab.token`."},
	{Key: "gitlab.default_instance", Description: "Instance to use without prompting. Must name a configured instance."},

	{Key: "review.provider", Allowed: []string{"anthropic", "bedrock"}, Description: "Where the claude CLI sends review requests. `bedrock` sets `CLAUDE_CODE_USE_BEDROCK=1` on the subprocess and requires `bedrock.region`."},
	{Key: "review.model", DefaultText: "claude CLI default", Description: "Model passed to the claude CLI verbatim. Any model ID or alias the claude CLI understands."},
	{Key: "review.models", DefaultText: "curated per-provider list", Description: "Models offered by `gitlab-reviewer models`, `--model` completion and the per-agent model pickers. Suggestions, not validation."},
	{Key: "review.claude_path", Type: "path", Description: "Path to the claude binary, looked up on `PATH` by default."},
	{Key: "review.timeout", Constraint: "per pass, > 0", Description: "Maximum duration of one review pass, as a Go duration such as `10m` or `1h30m`."},
	{Key: "review.max_budget_usd", Constraint: "total per run", DefaultText: "unset", Description: "Spending cap for the whole run, split evenly across the planned passes plus one slice for aggregation when `review.aggregate` is on."},
	{Key: "review.agents", DefaultText: "all built-ins", Description: "Review agents to run: built-in names plus custom agents by name. Unknown names fail the run instead of silently reviewing less."},
	{Key: "review.agent_models", Description: "Model per agent, for example `security: opus`. A picker choice remembered for the project wins; the agent's own frontmatter `model` and `review.model` are the fallbacks."},
	{Key: "review.agent_concurrency", Constraint: ">= 1", Description: "How many agent passes run at once."},
	{Key: "review.aggregate", Description: "Merge duplicate findings across agents and passes with the aggregation agent. Off, or when aggregation fails, a deterministic same-line heuristic is used."},
	{Key: "review.categories", Allowed: Categories, Deprecated: true, Description: "Alias for `review.agents` from before reviews were agent-based. Fills `review.agents` when that is unset. Use `review.agents`."},
	{Key: "review.instructions", Description: "Extra instructions appended to the review prompt, for team conventions."},
	{Key: "review.instructions_file", Type: "path", Description: "File whose contents are appended to the review prompt too."},
	{Key: "review.max_diff_kb", Constraint: ">= 1, KiB per pass", Description: "Diff budget per pass. Larger merge requests are split into several passes."},
	{Key: "review.exclude", Description: "Globs of files removed from the review entirely. Setting it replaces the default list, so re-include the defaults you still want."},
	{Key: "review.bare", Description: "Run claude with `--bare`, skipping user hooks and CLAUDE.md. Also skips OAuth and keychain auth, so leave it off with a Claude subscription login."},
	{Key: "review.use_agents", Description: "Let the reviewer delegate to Claude Code subagents (`.claude/agents` plus user-level ones). Unrelated to `review.agents`. Write and network tools stay denied for subagents too."},
	{Key: "review.claude_plugins", Description: "Installed Claude Code plugins (`name` or `name@marketplace`) whose agents join the review agent catalog. An explicit allowlist: installing a plugin never adds reviewers on its own."},
	{Key: "review.env", Description: "Extra environment for the claude subprocess. Keys starting with `GITLAB` are stripped so the token never reaches the model."},
	{Key: "review.mcp_servers", Description: "MCP servers granted to the review session, keyed by server name, in Claude Code's `.mcp.json` shape. Off by default: a server with network access reopens the exfiltration channel the sandbox closes."},
	{Key: "review.allowed_domains", Description: "Grants `WebFetch` scoped to these domains only. WebFetch stays fully denied when empty. The GUI's per-run picker can narrow the list, never widen it."},
	{Key: "review.allowed_commands", Description: "Grants `Bash` scoped to these command patterns only, as Claude Code prefix rules such as `npm test:*`. Bash stays fully denied when empty."},

	{Key: "bedrock.region", Description: "AWS region for Bedrock. Required when `review.provider` is `bedrock`."},
	{Key: "bedrock.profile", Description: "AWS profile for Bedrock."},

	{Key: "checkout.mode", Allowed: []string{"clone", "path", "root"}, Description: "Where the repository comes from: managed clones in the cache, one existing clone, or clones under a structured root. Reviews always run in a detached worktree at the MR head."},
	{Key: "checkout.path", Type: "path", Description: "Existing local clone. Required in `path` mode."},
	{Key: "checkout.root", Type: "path", Description: "Structured git root such as `~/git`, holding clones at `<root>/<host>/<group>/<project>`. Required in `root` mode."},
	{Key: "checkout.transport", Allowed: []string{"https", "ssh"}, Description: "How clones and fetches talk to GitLab. `https` hands the token to git through an in-memory credential helper; `ssh` uses your own agent and keys."},
	{Key: "checkout.cache_dir", Type: "path", Description: "Cache for managed clones and review worktrees. Honours `XDG_CACHE_HOME`."},
	{Key: "checkout.cache_max_mb", Constraint: "MiB", Description: "Clone cache budget. Least-recently-used clones are evicted above it."},
	{Key: "checkout.keep", Description: "Keep review worktrees after the review, for debugging. `cache clean` removes them later."},
	{Key: "checkout.clone_missing", Description: "In `root` mode, clone projects that are not cloned yet instead of failing."},
	{Key: "checkout.local_overlay", Description: "Globs of untracked files in your local clone copied into the review worktree (`path` and `root` modes). Files tracked at the MR head are never overridden."},

	{Key: "publish.mode", Allowed: []string{"draft", "immediate"}, Description: "`draft` collects comments into a GitLab draft review published in one action (GitLab 16 or later); `immediate` posts each as a live discussion."},
	{Key: "publish.auto_comment", Description: "Accept findings at or above `publish.auto_min_severity` automatically."},
	{Key: "publish.auto_min_severity", Allowed: Severities, Description: "Threshold for `publish.auto_comment`."},
	{Key: "publish.min_severity", Allowed: Severities, Description: "Publish floor: findings below it are never posted from any frontend, and stay visible in triage marked below-threshold."},
	{Key: "publish.fallback_to_note", Description: "Post a general MR note with a file and line reference when an inline position cannot be resolved."},
	{Key: "publish.attribution", Description: "Append a footer marking published comments as AI-suggested."},
	{Key: "publish.template", Type: "Go template", DefaultText: "built-in layout", Description: "Comment body layout with fields `{{.severity}}`, `{{.category}}`, `{{.agent}}`, `{{.title}}`, `{{.body}}`, `{{.file}}`. Set `{{.body}}` for comments without a header."},

	{Key: "gate.min_severity", Allowed: Severities, DefaultText: "unset (gate off)", Description: "Findings at or above this severity are blocking until rejected in triage. `review` exits 2 while any remain."},
	{Key: "gate.approvals", Allowed: []string{"off", "warn", "block"}, Description: "What approving from the TUI, GUI or `mr approve` does while blocking findings remain. Only consulted when the gate is on."},

	{Key: "ui.diff_view", Allowed: []string{"unified", "split"}, Description: "Diff layout in both frontends. `v` in the TUI switches it for the session."},
	{Key: "ui.file_explorer", Allowed: []string{"open", "closed"}, Description: "Initial state of the changed-files explorer. `e` in the TUI toggles it."},

	{Key: "log.level", Allowed: []string{"debug", "info", "warn", "error"}, Description: "Log level."},
	{Key: "log.file", Type: "path", Description: "Log file, created with owner-only permissions. Honours `XDG_STATE_HOME`."},
}

// OptionDoc is an Option resolved against the code: the reference entry the
// documentation generator renders.
type OptionDoc struct {
	Key         string   `json:"key"`
	Section     string   `json:"section"`
	Type        string   `json:"type"`
	Allowed     []string `json:"allowed,omitempty"`
	Constraint  string   `json:"constraint,omitempty"`
	Description string   `json:"description"`
	Secret      bool     `json:"secret,omitempty"`
	Deprecated  bool     `json:"deprecated,omitempty"`
	Default     string   `json:"default"`
	Env         string   `json:"env,omitempty"`
	EnvFallback string   `json:"envFallback,omitempty"`
	EnvList     bool     `json:"envList,omitempty"`
	Flag        string   `json:"flag,omitempty"`
	FileOnly    bool     `json:"fileOnly,omitempty"`
}

// Reference resolves Settings against the env and flag tables and the
// defaults. Path defaults expand the home directory as reported by the
// environment, so callers wanting stable output set HOME and unset the
// XDG variables first.
func Reference() []OptionDoc {
	defaults := koanf.New(delim)
	_ = defaults.Load(structs.Provider(Default(), "koanf"), nil)
	fields := LeafKeys()
	envFor := invert(envToKey)
	flagFor := invert(flagToKey)
	fallbackFor := invert(envFallbacks)

	docs := make([]OptionDoc, 0, len(Settings))
	for _, o := range Settings {
		d := OptionDoc{
			Key:         o.Key,
			Type:        o.Type,
			Allowed:     o.Allowed,
			Constraint:  o.Constraint,
			Description: o.Description,
			Secret:      o.Secret,
			Deprecated:  o.Deprecated,
			Section:     strings.SplitN(o.Key, delim, 2)[0],
			Default:     formatDefault(defaults.Get(o.Key)),
			EnvFallback: fallbackFor[o.Key],
			EnvList:     listKeys[o.Key],
			Flag:        flagFor[o.Key],
		}
		if suffix, ok := envFor[o.Key]; ok {
			d.Env = envPrefix + suffix
		}
		if d.Type == "" {
			isString := fields[o.Key].Kind() == reflect.String
			switch {
			case isString && slices.Equal(o.Allowed, Severities):
				d.Type = "severity"
			case isString && len(o.Allowed) > 0:
				d.Type = "enum"
			default:
				d.Type = typeName(fields[o.Key])
			}
		}
		if d.Default == "" {
			d.Default = o.DefaultText
		}
		d.FileOnly = d.Env == "" && d.Flag == ""
		docs = append(docs, d)
	}
	return docs
}

// LeafKeys maps every settings key to its Go type by walking the koanf
// tags of Config. Struct-valued slices and maps are leaves: the reference
// documents them as one key.
func LeafKeys() map[string]reflect.Type {
	out := map[string]reflect.Type{}
	var walk func(prefix string, t reflect.Type)
	walk = func(prefix string, t reflect.Type) {
		for i := range t.NumField() {
			f := t.Field(i)
			key := prefix + f.Tag.Get("koanf")
			if f.Type.Kind() == reflect.Struct && f.Type != reflect.TypeFor[time.Duration]() {
				walk(key+delim, f.Type)
				continue
			}
			out[key] = f.Type
		}
	}
	walk("", reflect.TypeFor[Config]())
	return out
}

func typeName(t reflect.Type) string {
	if t == reflect.TypeFor[time.Duration]() {
		return "duration"
	}
	switch t.Kind() {
	case reflect.String:
		return "string"
	case reflect.Int:
		return "integer"
	case reflect.Float64:
		return "number"
	case reflect.Bool:
		return "boolean"
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Struct {
			return "list of objects"
		}
		return "list"
	case reflect.Map:
		if t.Elem().Kind() == reflect.Struct {
			return "map of objects"
		}
		return "map"
	}
	return t.String()
}

func formatDefault(v any) string {
	switch v := v.(type) {
	case nil:
		return ""
	case string:
		return v
	case bool:
		return strconv.FormatBool(v)
	case int:
		if v == 0 {
			return ""
		}
		return strconv.Itoa(v)
	case float64:
		if v == 0 {
			return ""
		}
		return strconv.FormatFloat(v, 'f', -1, 64)
	case time.Duration:
		s := v.String()
		if strings.HasSuffix(s, "m0s") {
			s = strings.TrimSuffix(s, "0s")
		}
		if strings.HasSuffix(s, "h0m") {
			s = strings.TrimSuffix(s, "0m")
		}
		return s
	case []string:
		return strings.Join(v, ", ")
	case map[string]any:
		if len(v) == 0 {
			return ""
		}
	}
	if rv := reflect.ValueOf(v); (rv.Kind() == reflect.Slice || rv.Kind() == reflect.Map) && rv.Len() == 0 {
		return ""
	}
	return fmt.Sprint(v)
}

func invert(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for _, k := range slices.Sorted(maps.Keys(m)) {
		out[m[k]] = k
	}
	return out
}
