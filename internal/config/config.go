// Package config loads and validates gitlab-reviewer settings with the
// precedence flags > environment variables > settings file > defaults.
package config

import (
	"fmt"
	"net/url"
	"slices"
	"strings"
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
	Log      Log      `koanf:"log"`
}

type GitLab struct {
	BaseURL  string   `koanf:"base_url"`
	Token    string   `koanf:"token"`
	Projects []string `koanf:"projects"`
	Groups   []string `koanf:"groups"`
	PerPage  int      `koanf:"per_page"`
}

type Review struct {
	Provider         string            `koanf:"provider"` // anthropic | bedrock
	Model            string            `koanf:"model"`
	ClaudePath       string            `koanf:"claude_path"`
	Timeout          time.Duration     `koanf:"timeout"`
	MaxBudgetUSD     float64           `koanf:"max_budget_usd"`
	Categories       []string          `koanf:"categories"`
	Instructions     string            `koanf:"instructions"`
	InstructionsFile string            `koanf:"instructions_file"`
	MaxDiffKB        int               `koanf:"max_diff_kb"`
	Exclude          []string          `koanf:"exclude"`
	Bare             bool              `koanf:"bare"`
	Env              map[string]string `koanf:"env"`
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
}

type Publish struct {
	Mode            string `koanf:"mode"` // draft | immediate
	AutoComment     bool   `koanf:"auto_comment"`
	AutoMinSeverity string `koanf:"auto_min_severity"`
	FallbackToNote  bool   `koanf:"fallback_to_note"`
	Attribution     bool   `koanf:"attribution"`
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
			Categories: slices.Clone(Categories),
			MaxDiffKB:  256,
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
		},
		Publish: Publish{
			Mode:            "draft",
			AutoMinSeverity: "major",
			FallbackToNote:  true,
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
	if c.Review.Provider == "bedrock" && c.Bedrock.Region == "" {
		errs = append(errs, fmt.Errorf("bedrock.region: required when review.provider is bedrock (or set AWS_REGION)"))
	}

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

// ValidateGitLab checks settings required to talk to GitLab at all.
func (c Config) ValidateGitLab() error {
	if c.GitLab.Token == "" {
		return fmt.Errorf("gitlab.token is required: set GITLAB_REVIEWER_GITLAB_TOKEN (or GITLAB_TOKEN), or add it to %s", DefaultFile())
	}
	if len(c.GitLab.Projects) == 0 && len(c.GitLab.Groups) == 0 {
		return fmt.Errorf("no projects or groups configured: set gitlab.projects/gitlab.groups, --project/--group, or the matching env vars")
	}
	return nil
}
