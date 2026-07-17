// Package config loads and validates gitlab-reviewer settings with the
// precedence flags > environment variables > settings file > defaults.
package config

import (
	"fmt"
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"text/template"
	"time"
)

// Severity levels a finding can carry, ordered weakest to strongest.
var Severities = []string{"info", "minor", "major", "critical"}

// Categories of findings the reviewer can be asked to produce.
var Categories = []string{"bug", "security", "performance", "docs", "style", "design"}

type Config struct {
	GitLab   GitLab   `koanf:"gitlab"`
	Review   Review   `koanf:"review"`
	Bedrock  Bedrock  `koanf:"bedrock"`
	Checkout Checkout `koanf:"checkout"`
	Publish  Publish  `koanf:"publish"`
	Gate     Gate     `koanf:"gate"`
	UI       UI       `koanf:"ui"`
	Log      Log      `koanf:"log"`
}

type GitLab struct {
	BaseURL  string   `koanf:"base_url"`
	Token    string   `koanf:"token"`
	Projects []string `koanf:"projects"`
	Groups   []string `koanf:"groups"`
	PerPage  int      `koanf:"per_page"`
	// Instances are named GitLab instances to choose between (settings file
	// only). When set, one instance is selected at startup — via --instance,
	// default_instance, or an interactive prompt — and its connection
	// settings replace gitlab.base_url and gitlab.token.
	Instances []Instance `koanf:"instances"`
	// DefaultInstance names the instance to use without prompting; the
	// --instance flag and GITLAB_REVIEWER_GITLAB_DEFAULT_INSTANCE override it.
	DefaultInstance string `koanf:"default_instance"`
}

// Instance is one named GitLab instance in gitlab.instances.
type Instance struct {
	Name    string `koanf:"name"`
	BaseURL string `koanf:"base_url"`
	// Token is the access token for this instance; empty falls back to
	// gitlab.token (useful when one env-provided token covers an instance).
	Token string `koanf:"token"`
	// TokenEnv names an environment variable holding the token for this
	// instance (e.g. WORK_GITLAB_TOKEN), keeping the secret out of the
	// settings file. Consulted only when token is empty; the variable must
	// be set whenever this instance is selected.
	TokenEnv string `koanf:"token_env"`
}

type Review struct {
	Provider string `koanf:"provider"` // anthropic | bedrock
	Model    string `koanf:"model"`
	// Models is the list offered by the `models` command and --model shell
	// completion. Empty falls back to a curated list of common Claude
	// models for the selected provider. Suggestions only: review.model
	// accepts any string the claude CLI understands.
	Models       []string      `koanf:"models"`
	ClaudePath   string        `koanf:"claude_path"`
	Timeout      time.Duration `koanf:"timeout"`
	MaxBudgetUSD float64       `koanf:"max_budget_usd"`
	// Agents is the default agent selection for a review: builtin agent
	// names (the category names) plus any custom agents by name. Empty
	// falls back to the deprecated categories key, which defaults to all
	// builtins.
	Agents []string `koanf:"agents"`
	// AgentModels pins the review model per agent (agent name → model ID),
	// e.g. security: opus. A picker choice remembered for the project still
	// wins; an agent's own frontmatter model and review.model are the
	// fallbacks. Names are resolved when the run's agents are, so an entry
	// for an unknown agent is simply inert.
	AgentModels map[string]string `koanf:"agent_models"`
	// AgentConcurrency caps how many agent passes run at once.
	AgentConcurrency int `koanf:"agent_concurrency"`
	// Categories is deprecated: builtin agents subsumed it. It is kept as
	// an alias for agents (each category name is a builtin agent).
	Categories       []string `koanf:"categories"`
	Instructions     string   `koanf:"instructions"`
	InstructionsFile string   `koanf:"instructions_file"`
	MaxDiffKB        int      `koanf:"max_diff_kb"`
	Exclude          []string `koanf:"exclude"`
	Bare             bool     `koanf:"bare"`
	// UseAgents lets the reviewer delegate to Claude Code subagents
	// (project .claude/agents plus user-level ones). Write/exec tools
	// stay denied for the whole session, subagents included.
	UseAgents bool `koanf:"use_agents"`
	// ClaudePlugins names installed Claude Code plugins ("name" or
	// "name@marketplace") whose agents join the catalog like ~/.claude/agents
	// definitions do. An explicit allowlist because a plugin agent's prompt
	// steers the reviewer: installing a plugin for Claude Code must never
	// silently add reviewers here. Empty loads none.
	ClaudePlugins []string          `koanf:"claude_plugins"`
	Env           map[string]string `koanf:"env"`
	// MCPServers grants the review session named MCP servers, keyed by
	// server name (settings file only — no flag or env form; per-project
	// sections may add servers for just that project). Off by default:
	// a server that reaches the network reopens the exfiltration channel
	// the review sandbox exists to close, so grant only servers whose
	// egress you trust and keep the grant per-project where possible.
	MCPServers map[string]MCPServer `koanf:"mcp_servers"`
	// AllowedDomains grants WebFetch scoped to these domains only, via
	// fine-grained permission rules (settings file only). WebFetch stays
	// fully denied when empty: an unscoped grant reopens the exfiltration
	// channel the review sandbox exists to close, so list only domains you
	// trust the reviewer to reach and keep the grant per-project where
	// possible. The GUI's per-run picker can narrow this list further but
	// never widen it.
	AllowedDomains []string `koanf:"allowed_domains"`
	// AllowedCommands grants Bash scoped to these command patterns only
	// (Claude Code prefix rules, e.g. "npm test:*", "git log:*"); settings
	// file only. Bash stays fully denied when empty, for the same
	// exfiltration/blast-radius reason as AllowedDomains — an arbitrary
	// shell is a much bigger grant than a scoped one, so list only the
	// specific commands a review genuinely needs.
	AllowedCommands []string `koanf:"allowed_commands"`
}

// MCPServer is one MCP server definition in review.mcp_servers, mirroring
// Claude Code's .mcp.json entries: a local stdio server (command/args/env)
// or a remote http/sse one (url/headers).
type MCPServer struct {
	// Type is stdio, http, or sse; empty infers stdio when command is set
	// and http when url is set.
	Type    string            `koanf:"type"`
	Command string            `koanf:"command"`
	Args    []string          `koanf:"args"`
	Env     map[string]string `koanf:"env"`
	URL     string            `koanf:"url"`
	// Headers are sent with every request to a remote server; values are
	// treated as secrets and redacted from `config show`.
	Headers map[string]string `koanf:"headers"`
	// Tools narrows the allowed tools of this server to the named ones;
	// empty allows all of the server's tools.
	Tools []string `koanf:"tools"`
}

type Bedrock struct {
	Region  string `koanf:"region"`
	Profile string `koanf:"profile"`
}

type Checkout struct {
	Mode         string `koanf:"mode"` // clone | path | root
	Path         string `koanf:"path"`
	Root         string `koanf:"root"`
	Transport    string `koanf:"transport"` // https | ssh
	CacheDir     string `koanf:"cache_dir"`
	CacheMaxMB   int    `koanf:"cache_max_mb"`
	Keep         bool   `koanf:"keep"`
	CloneMissing bool   `koanf:"clone_missing"`
	// LocalOverlay globs select untracked files in the local clone (path
	// and root modes) that are copied into the review worktree — team
	// standards kept out of the repo, e.g. via .git/info/exclude.
	LocalOverlay []string `koanf:"local_overlay"`
}

type Publish struct {
	Mode            string `koanf:"mode"` // draft | immediate
	AutoComment     bool   `koanf:"auto_comment"`
	AutoMinSeverity string `koanf:"auto_min_severity"`
	// MinSeverity is the publish floor: findings below it are never posted
	// to GitLab — they stay visible in triage, marked below-threshold. The
	// default (info) publishes everything.
	MinSeverity    string `koanf:"min_severity"`
	FallbackToNote bool   `koanf:"fallback_to_note"`
	Attribution    bool   `koanf:"attribution"`
	// Template is a Go text/template for the comment body with fields
	// severity, category, title, body, file. Empty means the built-in
	// "**[severity · category] title**" layout; set e.g. "{{.body}}" for
	// comments with no machine-looking header.
	Template string `koanf:"template"`
}

// Gate ties the review outcome to a severity policy: findings at or above
// min_severity are "blocking". The headless review command exits non-zero
// while blocking findings remain, and approvals controls how the TUI/GUI
// approve action behaves. Advisory only: GitLab itself is not restricted.
type Gate struct {
	// MinSeverity marks findings at or above it as blocking; empty disables
	// the gate entirely.
	MinSeverity string `koanf:"min_severity"`
	// Approvals is what approving from the tool does while blocking findings
	// remain: off (ignore the gate), warn (ask for confirmation), or block
	// (refuse). Only consulted when min_severity is set.
	Approvals string `koanf:"approvals"`
}

// Enabled reports whether a gate severity is configured.
func (g Gate) Enabled() bool { return g.MinSeverity != "" }

type UI struct {
	// DiffView is the diff layout in the MR detail screen: unified or
	// split (side-by-side). Toggleable per session with `v`.
	DiffView string `koanf:"diff_view"`
	// FileExplorer is the initial state of the changed-files tree in the
	// MR detail screen: open or closed. Toggleable per session with `e`.
	FileExplorer string `koanf:"file_explorer"`
}

type Log struct {
	Level string `koanf:"level"`
	File  string `koanf:"file"`
}

// Default returns the built-in defaults, the lowest-precedence layer.
func Default() Config {
	return Config{
		GitLab: GitLab{
			BaseURL: "https://gitlab.com",
			PerPage: 50,
		},
		Review: Review{
			Provider:   "anthropic",
			ClaudePath: "claude",
			Timeout:    10 * time.Minute,
			// Agents deliberately has no default: the load-time finalize
			// step falls back to categories, making the deprecated key a
			// working alias without tracking which layer set it.
			AgentConcurrency: 3,
			AgentModels:      map[string]string{},
			Categories:       slices.Clone(Categories),
			MaxDiffKB:        256,
			Exclude: []string{
				"**/go.sum", "**/package-lock.json", "**/yarn.lock", "**/pnpm-lock.yaml",
				"**/Cargo.lock", "**/poetry.lock", "**/uv.lock", "**/Gemfile.lock",
				"vendor/**", "node_modules/**", "**/*.pb.go", "**/*_generated.go",
				"**/*.min.js", "**/*.min.css", "**/*.svg", "dist/**",
			},
			Env: map[string]string{},
		},
		Checkout: Checkout{
			Mode:       "clone",
			Transport:  "https",
			CacheDir:   DefaultCacheDir(),
			CacheMaxMB: 2048,
			// The files Claude Code itself reads for repo conventions.
			LocalOverlay: []string{"**/CLAUDE.md", "**/CLAUDE.local.md", ".claude/**"},
		},
		Publish: Publish{
			Mode:            "draft",
			AutoMinSeverity: "major",
			MinSeverity:     "info",
			FallbackToNote:  true,
		},
		Gate: Gate{
			Approvals: "warn",
		},
		UI: UI{
			DiffView:     "unified",
			FileExplorer: "closed",
		},
		Log: Log{
			Level: "info",
			File:  DefaultLogFile(),
		},
	}
}

func oneOf(field, value string, allowed ...string) error {
	if slices.Contains(allowed, value) {
		return nil
	}
	return fmt.Errorf("%s: %q is not one of %s", field, value, strings.Join(allowed, "|"))
}

// Validate checks internal consistency. It does not require a GitLab token;
// commands that talk to GitLab must additionally call ValidateGitLab.
func (c Config) Validate() error {
	var errs []error

	u, err := url.Parse(c.GitLab.BaseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		errs = append(errs, fmt.Errorf("gitlab.base_url: %q is not a valid URL", c.GitLab.BaseURL))
	}
	if c.GitLab.PerPage < 1 || c.GitLab.PerPage > 100 {
		errs = append(errs, fmt.Errorf("gitlab.per_page: %d must be between 1 and 100", c.GitLab.PerPage))
	}

	seen := map[string]bool{}
	for i, inst := range c.GitLab.Instances {
		if inst.Name == "" {
			errs = append(errs, fmt.Errorf("gitlab.instances[%d]: name is required", i))
		} else if seen[inst.Name] {
			errs = append(errs, fmt.Errorf("gitlab.instances: duplicate name %q", inst.Name))
		}
		seen[inst.Name] = true
		u, err := url.Parse(inst.BaseURL)
		if err != nil || u.Scheme == "" || u.Host == "" {
			errs = append(errs, fmt.Errorf("gitlab.instances[%s].base_url: %q is not a valid URL", inst.Name, inst.BaseURL))
		}
	}
	if c.GitLab.DefaultInstance != "" && !seen[c.GitLab.DefaultInstance] {
		errs = append(errs, fmt.Errorf("gitlab.default_instance: %q is not a configured instance (have %s)",
			c.GitLab.DefaultInstance, strings.Join(c.InstanceNames(), ", ")))
	}

	if err := oneOf("review.provider", c.Review.Provider, "anthropic", "bedrock"); err != nil {
		errs = append(errs, err)
	}
	if c.Review.Timeout <= 0 {
		errs = append(errs, fmt.Errorf("review.timeout: must be positive, got %s", c.Review.Timeout))
	}
	if c.Review.MaxDiffKB < 1 {
		errs = append(errs, fmt.Errorf("review.max_diff_kb: must be at least 1, got %d", c.Review.MaxDiffKB))
	}
	for _, cat := range c.Review.Categories {
		if err := oneOf("review.categories", cat, Categories...); err != nil {
			errs = append(errs, err)
		}
	}
	// review.agents entries are not validated here: custom agents are only
	// discoverable once their definition files are loaded. Selection is
	// validated when the agent catalog resolves it.
	if c.Review.AgentConcurrency < 1 {
		errs = append(errs, fmt.Errorf("review.agent_concurrency: must be at least 1, got %d", c.Review.AgentConcurrency))
	}
	if c.Review.Provider == "bedrock" && c.Bedrock.Region == "" {
		errs = append(errs, fmt.Errorf("bedrock.region: required when review.provider is bedrock (or set AWS_REGION)"))
	}
	errs = append(errs, validateMCPServers(c.Review.MCPServers)...)
	errs = append(errs, validateAllowRules("review.allowed_domains", c.Review.AllowedDomains)...)
	errs = append(errs, validateAllowRules("review.allowed_commands", c.Review.AllowedCommands)...)

	if err := oneOf("checkout.mode", c.Checkout.Mode, "clone", "path", "root"); err != nil {
		errs = append(errs, err)
	}
	if err := oneOf("checkout.transport", c.Checkout.Transport, "https", "ssh"); err != nil {
		errs = append(errs, err)
	}
	if c.Checkout.Mode == "path" && c.Checkout.Path == "" {
		errs = append(errs, fmt.Errorf("checkout.path: required when checkout.mode is path"))
	}
	if c.Checkout.Mode == "root" && c.Checkout.Root == "" {
		errs = append(errs, fmt.Errorf("checkout.root: required when checkout.mode is root"))
	}

	if err := oneOf("publish.mode", c.Publish.Mode, "draft", "immediate"); err != nil {
		errs = append(errs, err)
	}
	if err := oneOf("publish.auto_min_severity", c.Publish.AutoMinSeverity, Severities...); err != nil {
		errs = append(errs, err)
	}
	if err := oneOf("publish.min_severity", c.Publish.MinSeverity, Severities...); err != nil {
		errs = append(errs, err)
	}
	if c.Gate.MinSeverity != "" {
		if err := oneOf("gate.min_severity", c.Gate.MinSeverity, Severities...); err != nil {
			errs = append(errs, err)
		}
	}
	if err := oneOf("gate.approvals", c.Gate.Approvals, "off", "warn", "block"); err != nil {
		errs = append(errs, err)
	}
	if c.Publish.Template != "" {
		if _, err := template.New("publish.template").Parse(c.Publish.Template); err != nil {
			errs = append(errs, fmt.Errorf("publish.template: %w", err))
		}
	}

	if err := oneOf("ui.diff_view", c.UI.DiffView, "unified", "split"); err != nil {
		errs = append(errs, err)
	}
	if err := oneOf("ui.file_explorer", c.UI.FileExplorer, "open", "closed"); err != nil {
		errs = append(errs, err)
	}

	if err := oneOf("log.level", c.Log.Level, "debug", "info", "warn", "error"); err != nil {
		errs = append(errs, err)
	}

	if len(errs) == 0 {
		return nil
	}
	msgs := make([]string, len(errs))
	for i, e := range errs {
		msgs[i] = "  - " + e.Error()
	}
	return fmt.Errorf("invalid configuration:\n%s", strings.Join(msgs, "\n"))
}

// validateAllowRules rejects entries that would break the fine-grained
// permission rule syntax they get wrapped in (e.g. "WebFetch(domain:x)",
// "Bash(x)"): empty, or containing a parenthesis.
func validateAllowRules(field string, entries []string) []error {
	var errs []error
	for _, e := range entries {
		if strings.TrimSpace(e) == "" {
			errs = append(errs, fmt.Errorf("%s: entries must not be empty", field))
			continue
		}
		if strings.ContainsAny(e, "()") {
			errs = append(errs, fmt.Errorf("%s: %q must not contain parentheses", field, e))
		}
	}
	return errs
}

// mcpNameRe restricts server names: they become tool-name prefixes
// (mcp__<name>__<tool>) and permission rules, so no separators or spaces.
var mcpNameRe = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

func validateMCPServers(servers map[string]MCPServer) []error {
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(servers)) {
		s := servers[name]
		field := "review.mcp_servers." + name
		if !mcpNameRe.MatchString(name) {
			errs = append(errs, fmt.Errorf("review.mcp_servers: name %q must contain only letters, digits, - and _", name))
		}
		hasCmd, hasURL := s.Command != "", s.URL != ""
		switch {
		case !hasCmd && !hasURL:
			errs = append(errs, fmt.Errorf("%s: needs command (stdio server) or url (remote server)", field))
		case hasCmd && hasURL:
			errs = append(errs, fmt.Errorf("%s: command and url are mutually exclusive", field))
		}
		switch s.Type {
		case "":
			// inferred from command/url
		case "stdio":
			if !hasCmd {
				errs = append(errs, fmt.Errorf("%s: type stdio requires command", field))
			}
		case "http", "sse":
			if !hasURL {
				errs = append(errs, fmt.Errorf("%s: type %s requires url", field, s.Type))
			}
		default:
			errs = append(errs, fmt.Errorf("%s.type: %q is not one of stdio|http|sse", field, s.Type))
		}
		if hasURL {
			if u, err := url.Parse(s.URL); err != nil || u.Scheme == "" || u.Host == "" {
				errs = append(errs, fmt.Errorf("%s.url: %q is not a valid URL", field, s.URL))
			}
		}
		for _, k := range slices.Sorted(maps.Keys(s.Env)) {
			// Same rule as review.env: GitLab credentials never reach the
			// model subprocess, and MCP servers run inside it.
			if strings.HasPrefix(k, "GITLAB") {
				errs = append(errs, fmt.Errorf("%s.env: %s is not allowed (GitLab credentials must not reach the review subprocess)", field, k))
			}
		}
	}
	return errs
}

// ValidateGitLab checks settings required to talk to GitLab at all. An
// empty project/group scope is fine: the TUI offers in-app selection.
func (c Config) ValidateGitLab() error {
	if len(c.GitLab.Instances) > 0 {
		for _, inst := range c.GitLab.Instances {
			// A configured token_env counts as a token source even when its
			// variable is unset here: it may only exist on the machine where
			// that instance is used. Selection (WithInstance) enforces it.
			if inst.Token == "" && inst.TokenEnv == "" && c.GitLab.Token == "" {
				return fmt.Errorf("gitlab.instances[%s].token is required: add token or token_env to the settings file, or set gitlab.token (GITLAB_REVIEWER_GITLAB_TOKEN) as a shared fallback", inst.Name)
			}
		}
		return nil
	}
	if c.GitLab.Token == "" {
		return fmt.Errorf("gitlab.token is required: set GITLAB_REVIEWER_GITLAB_TOKEN (or GITLAB_TOKEN), or add it to %s", DefaultFile())
	}
	return nil
}

// InstanceNames returns the configured instance names in file order.
func (c Config) InstanceNames() []string {
	names := make([]string, len(c.GitLab.Instances))
	for i, inst := range c.GitLab.Instances {
		names[i] = inst.Name
	}
	return names
}

// WithInstance returns a copy of the configuration narrowed to the named
// instance: its base URL and token replace the top-level gitlab settings.
// An instance with an empty token keeps gitlab.token as the fallback.
// Tokens named by token_env are resolved at load time; selecting an
// instance whose variable is unset is an error rather than a silent
// fallback to the shared token.
func (c Config) WithInstance(name string) (Config, error) {
	for _, inst := range c.GitLab.Instances {
		if inst.Name != name {
			continue
		}
		c.GitLab.BaseURL = inst.BaseURL
		if inst.Token != "" {
			c.GitLab.Token = inst.Token
		} else if inst.TokenEnv != "" {
			return c, fmt.Errorf("gitlab instance %q: environment variable %s (gitlab.instances[%s].token_env) is not set", name, inst.TokenEnv, name)
		}
		return c, nil
	}
	return c, fmt.Errorf("gitlab instance %q is not configured (have %s)", name, strings.Join(c.InstanceNames(), ", "))
}
