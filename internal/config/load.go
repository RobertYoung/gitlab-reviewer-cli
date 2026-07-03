package config

import (
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/providers/structs"
	"github.com/knadh/koanf/v2"
	"github.com/spf13/pflag"
)

const (
	delim     = "."
	envPrefix = "GITLAB_REVIEWER_"
)

// envToKey maps env var names (after the GITLAB_REVIEWER_ prefix) to config
// keys. Explicit table: key segments contain underscores (base_url), so an
// algorithmic underscore→dot mapping would be ambiguous.
var envToKey = map[string]string{
	"GITLAB_BASE_URL":           "gitlab.base_url",
	"GITLAB_TOKEN":              "gitlab.token",
	"GITLAB_DEFAULT_INSTANCE":   "gitlab.default_instance",
	"GITLAB_PROJECTS":           "gitlab.projects",
	"GITLAB_GROUPS":             "gitlab.groups",
	"GITLAB_PER_PAGE":           "gitlab.per_page",
	"REVIEW_PROVIDER":           "review.provider",
	"REVIEW_MODEL":              "review.model",
	"REVIEW_CLAUDE_PATH":        "review.claude_path",
	"REVIEW_TIMEOUT":            "review.timeout",
	"REVIEW_MAX_BUDGET_USD":     "review.max_budget_usd",
	"REVIEW_AGENTS":             "review.agents",
	"REVIEW_AGENT_CONCURRENCY":  "review.agent_concurrency",
	"REVIEW_CATEGORIES":         "review.categories",
	"REVIEW_INSTRUCTIONS":       "review.instructions",
	"REVIEW_INSTRUCTIONS_FILE":  "review.instructions_file",
	"REVIEW_MAX_DIFF_KB":        "review.max_diff_kb",
	"REVIEW_EXCLUDE":            "review.exclude",
	"REVIEW_BARE":               "review.bare",
	"REVIEW_USE_AGENTS":         "review.use_agents",
	"BEDROCK_REGION":            "bedrock.region",
	"BEDROCK_PROFILE":           "bedrock.profile",
	"CHECKOUT_MODE":             "checkout.mode",
	"CHECKOUT_PATH":             "checkout.path",
	"CHECKOUT_ROOT":             "checkout.root",
	"CHECKOUT_TRANSPORT":        "checkout.transport",
	"CHECKOUT_CACHE_DIR":        "checkout.cache_dir",
	"CHECKOUT_CACHE_MAX_MB":     "checkout.cache_max_mb",
	"CHECKOUT_KEEP":             "checkout.keep",
	"CHECKOUT_CLONE_MISSING":    "checkout.clone_missing",
	"CHECKOUT_LOCAL_OVERLAY":    "checkout.local_overlay",
	"PUBLISH_MODE":              "publish.mode",
	"PUBLISH_AUTO_COMMENT":      "publish.auto_comment",
	"PUBLISH_AUTO_MIN_SEVERITY": "publish.auto_min_severity",
	"PUBLISH_FALLBACK_TO_NOTE":  "publish.fallback_to_note",
	"PUBLISH_ATTRIBUTION":       "publish.attribution",
	"PUBLISH_TEMPLATE":          "publish.template",
	"UI_DIFF_VIEW":              "ui.diff_view",
	"UI_FILE_EXPLORER":          "ui.file_explorer",
	"LOG_LEVEL":                 "log.level",
	"LOG_FILE":                  "log.file",
}

// listKeys hold comma-separated values in env vars.
var listKeys = map[string]bool{
	"gitlab.projects":        true,
	"gitlab.groups":          true,
	"review.agents":          true,
	"review.categories":      true,
	"review.exclude":         true,
	"checkout.local_overlay": true,
}

// envFallbacks are conventional env vars honoured only when the prefixed
// variable did not set the key.
var envFallbacks = map[string]string{
	"GITLAB_TOKEN": "gitlab.token",
	"AWS_REGION":   "bedrock.region",
	"AWS_PROFILE":  "bedrock.profile",
}

// flagToKey maps pflag names to config keys; only changed flags are applied.
var flagToKey = map[string]string{
	"gitlab-base-url":   "gitlab.base_url",
	"gitlab-token":      "gitlab.token",
	"instance":          "gitlab.default_instance",
	"project":           "gitlab.projects",
	"group":             "gitlab.groups",
	"per-page":          "gitlab.per_page",
	"provider":          "review.provider",
	"model":             "review.model",
	"claude-path":       "review.claude_path",
	"review-timeout":    "review.timeout",
	"max-budget-usd":    "review.max_budget_usd",
	"agents":            "review.agents",
	"agent-concurrency": "review.agent_concurrency",
	"categories":        "review.categories",
	"instructions":      "review.instructions",
	"instructions-file": "review.instructions_file",
	"max-diff-kb":       "review.max_diff_kb",
	"exclude":           "review.exclude",
	"bare":              "review.bare",
	"use-agents":        "review.use_agents",
	"review-env":        "review.env",
	"aws-region":        "bedrock.region",
	"aws-profile":       "bedrock.profile",
	"checkout-mode":     "checkout.mode",
	"repo-path":         "checkout.path",
	"git-root":          "checkout.root",
	"clone-transport":   "checkout.transport",
	"cache-dir":         "checkout.cache_dir",
	"cache-max-mb":      "checkout.cache_max_mb",
	"keep-worktree":     "checkout.keep",
	"local-overlay":     "checkout.local_overlay",
	"publish-mode":      "publish.mode",
	"auto-comment":      "publish.auto_comment",
	"auto-min-severity": "publish.auto_min_severity",
	"fallback-to-note":  "publish.fallback_to_note",
	"attribution":       "publish.attribution",
	"publish-template":  "publish.template",
	"diff-view":         "ui.diff_view",
	"file-explorer":     "ui.file_explorer",
	"log-level":         "log.level",
	"log-file":          "log.file",
}

// Options control loading; zero value means real environment and default paths.
type Options struct {
	// File is an explicit settings file path (--config). Empty = default
	// XDG location, which is allowed to be absent.
	File string
	// Flags is the parsed flag set; nil skips the flag layer.
	Flags *pflag.FlagSet
	// LookupEnv overrides os.LookupEnv in tests.
	LookupEnv func(string) (string, bool)
}

// Result is a loaded configuration plus the merged koanf tree, kept so
// per-project overrides can be applied later.
type Result struct {
	Config Config
	// FilePath is the settings file that was read, "" if none existed.
	FilePath string

	k      *koanf.Koanf
	lookup func(string) (string, bool)
}

// Load builds the effective configuration: defaults → file → env → flags.
func Load(opts Options) (*Result, error) {
	lookup := opts.LookupEnv
	if lookup == nil {
		lookup = os.LookupEnv
	}

	k := koanf.New(delim)

	if err := k.Load(structs.Provider(Default(), "koanf"), nil); err != nil {
		return nil, fmt.Errorf("loading defaults: %w", err)
	}

	path := opts.File
	explicit := path != ""
	if !explicit {
		path = DefaultFile()
	}
	filePath := ""
	if _, err := os.Stat(path); err == nil {
		if err := k.Load(file.Provider(path), yaml.Parser()); err != nil {
			return nil, fmt.Errorf("reading config file %s: %w", path, err)
		}
		filePath = path
	} else if explicit {
		return nil, fmt.Errorf("config file %s: %w", path, err)
	}

	if err := k.Load(confmap.Provider(envLayer(lookup), delim), nil); err != nil {
		return nil, fmt.Errorf("loading environment: %w", err)
	}

	if opts.Flags != nil {
		if err := k.Load(confmap.Provider(flagLayer(opts.Flags), delim), nil); err != nil {
			return nil, fmt.Errorf("loading flags: %w", err)
		}
	}

	res := &Result{FilePath: filePath, k: k, lookup: lookup}
	if err := k.Unmarshal("", &res.Config); err != nil {
		return nil, fmt.Errorf("parsing configuration: %w", err)
	}
	resolveInstanceTokens(&res.Config, lookup)
	if len(res.Config.Review.Agents) == 0 && !slices.Equal(res.Config.Review.Categories, Default().Review.Categories) {
		slog.Warn("review.categories is deprecated; use review.agents (category names are builtin agent names)")
	}
	finalizeAgents(&res.Config)
	return res, nil
}

// finalizeAgents resolves the agent selection: review.agents when set,
// otherwise the deprecated review.categories key — whose defaults make the
// builtin agents the overall default. This runs after every unmarshal so
// per-project overrides of either key behave the same way.
func finalizeAgents(cfg *Config) {
	if len(cfg.Review.Agents) == 0 {
		cfg.Review.Agents = slices.Clone(cfg.Review.Categories)
	}
}

// resolveInstanceTokens fills each instance's Token from the environment
// variable named by its token_env. An explicit token wins; a variable that
// is unset or empty leaves Token empty, which WithInstance reports if that
// instance is selected.
func resolveInstanceTokens(cfg *Config, lookup func(string) (string, bool)) {
	for i, inst := range cfg.GitLab.Instances {
		if inst.Token != "" || inst.TokenEnv == "" {
			continue
		}
		if val, ok := lookup(inst.TokenEnv); ok && val != "" {
			cfg.GitLab.Instances[i].Token = val
		}
	}
}

func envLayer(lookup func(string) (string, bool)) map[string]any {
	layer := map[string]any{}
	set := map[string]bool{}
	for suffix, key := range envToKey {
		if val, ok := lookup(envPrefix + suffix); ok {
			layer[key] = envValue(key, val)
			set[key] = true
		}
	}
	for envVar, key := range envFallbacks {
		if set[key] {
			continue
		}
		if val, ok := lookup(envVar); ok {
			layer[key] = envValue(key, val)
		}
	}
	return layer
}

func envValue(key, val string) any {
	if !listKeys[key] {
		return val
	}
	parts := strings.Split(val, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func flagLayer(fs *pflag.FlagSet) map[string]any {
	layer := map[string]any{}
	fs.Visit(func(f *pflag.Flag) {
		key, ok := flagToKey[f.Name]
		if !ok {
			return
		}
		switch f.Value.Type() {
		case "stringArray", "stringSlice":
			vals, err := fs.GetStringArray(f.Name)
			if err != nil {
				vals, _ = fs.GetStringSlice(f.Name)
			}
			if key == "review.env" {
				m := map[string]string{}
				for _, kv := range vals {
					name, value, found := strings.Cut(kv, "=")
					if found {
						m[name] = value
					}
				}
				layer[key] = m
				return
			}
			layer[key] = vals
		default:
			// koanf's unmarshal hooks convert the string form to the
			// target type (int, bool, float, duration).
			layer[key] = f.Value.String()
		}
	})
	return layer
}

// ForProject returns the configuration with per-project overrides from the
// settings file's projects.<full/project/path> section merged over the
// review, checkout, and publish sections.
func (r *Result) ForProject(projectPath string) (Config, error) {
	sub := r.k.Cut("projects" + delim + projectPath)
	if len(sub.Keys()) == 0 {
		return r.Config, nil
	}
	merged := r.k.Copy()
	for _, section := range []string{"review", "checkout", "publish"} {
		s := sub.Cut(section)
		for _, key := range s.Keys() {
			if err := merged.Set(section+delim+key, s.Get(key)); err != nil {
				return r.Config, fmt.Errorf("applying override %s.%s for %s: %w", section, key, projectPath, err)
			}
		}
	}
	var cfg Config
	if err := merged.Unmarshal("", &cfg); err != nil {
		return r.Config, fmt.Errorf("parsing overrides for %s: %w", projectPath, err)
	}
	resolveInstanceTokens(&cfg, r.lookup)
	finalizeAgents(&cfg)
	return cfg, nil
}

// Redacted returns the effective configuration as a YAML-ready map with
// secret values masked, for `config show`.
func (r *Result) Redacted() map[string]any {
	raw := r.k.Raw()
	if g, ok := raw["gitlab"].(map[string]any); ok {
		if tok, ok := g["token"].(string); ok && tok != "" {
			g["token"] = "[redacted]"
		}
		if instances, ok := g["instances"].([]any); ok {
			for _, item := range instances {
				if m, ok := item.(map[string]any); ok {
					if tok, ok := m["token"].(string); ok && tok != "" {
						m["token"] = "[redacted]"
					}
				}
			}
		}
	}
	return raw
}
