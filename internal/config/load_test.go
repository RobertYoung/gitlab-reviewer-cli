package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/spf13/pflag"
)

func writeFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func envLookup(vars map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		v, ok := vars[name]
		return v, ok
	}
}

func flagSet(t *testing.T, args ...string) *pflag.FlagSet {
	t.Helper()
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	fs.String("gitlab-base-url", "", "")
	fs.String("gitlab-token", "", "")
	fs.StringArray("project", nil, "")
	fs.Int("per-page", 0, "")
	fs.Duration("review-timeout", 0, "")
	fs.StringSlice("categories", nil, "")
	fs.StringSlice("agents", nil, "")
	fs.StringArray("review-env", nil, "")
	fs.String("publish-mode", "", "")
	if err := fs.Parse(args); err != nil {
		t.Fatal(err)
	}
	return fs
}

func TestDefaultsOnly(t *testing.T) {
	res, err := Load(Options{LookupEnv: envLookup(nil)})
	if err != nil {
		t.Fatal(err)
	}
	cfg := res.Config
	if cfg.GitLab.BaseURL != "https://gitlab.com" {
		t.Errorf("base_url = %q", cfg.GitLab.BaseURL)
	}
	if cfg.Review.Timeout != 10*time.Minute {
		t.Errorf("timeout = %s", cfg.Review.Timeout)
	}
	if cfg.Publish.Mode != "draft" {
		t.Errorf("publish.mode = %q", cfg.Publish.Mode)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("defaults must validate: %v", err)
	}
}

// TestPrecedence exercises the full flags > env > file > defaults chain on a
// single key, plus each pairwise layer.
func TestPrecedence(t *testing.T) {
	file := writeFile(t, "gitlab:\n  base_url: https://file.example.com\n  per_page: 30\n")
	env := map[string]string{
		"GITLAB_REVIEWER_GITLAB_BASE_URL": "https://env.example.com",
	}

	t.Run("file over defaults", func(t *testing.T) {
		res, err := Load(Options{File: file, LookupEnv: envLookup(nil)})
		if err != nil {
			t.Fatal(err)
		}
		if got := res.Config.GitLab.BaseURL; got != "https://file.example.com" {
			t.Errorf("base_url = %q", got)
		}
		if got := res.Config.GitLab.PerPage; got != 30 {
			t.Errorf("per_page = %d", got)
		}
	})

	t.Run("env over file", func(t *testing.T) {
		res, err := Load(Options{File: file, LookupEnv: envLookup(env)})
		if err != nil {
			t.Fatal(err)
		}
		if got := res.Config.GitLab.BaseURL; got != "https://env.example.com" {
			t.Errorf("base_url = %q", got)
		}
		// untouched by env: file wins over default
		if got := res.Config.GitLab.PerPage; got != 30 {
			t.Errorf("per_page = %d", got)
		}
	})

	t.Run("flag over env and file", func(t *testing.T) {
		fs := flagSet(t, "--gitlab-base-url", "https://flag.example.com", "--per-page", "77")
		res, err := Load(Options{File: file, LookupEnv: envLookup(env), Flags: fs})
		if err != nil {
			t.Fatal(err)
		}
		if got := res.Config.GitLab.BaseURL; got != "https://flag.example.com" {
			t.Errorf("base_url = %q", got)
		}
		if got := res.Config.GitLab.PerPage; got != 77 {
			t.Errorf("per_page = %d", got)
		}
	})

	t.Run("unchanged flag does not mask lower layers", func(t *testing.T) {
		fs := flagSet(t) // declared but not passed
		res, err := Load(Options{File: file, LookupEnv: envLookup(env), Flags: fs})
		if err != nil {
			t.Fatal(err)
		}
		if got := res.Config.GitLab.BaseURL; got != "https://env.example.com" {
			t.Errorf("base_url = %q", got)
		}
	})
}

// TestAgentSelection covers the review.agents key and its deprecated
// review.categories alias.
func TestAgentSelection(t *testing.T) {
	t.Run("defaults to all builtin agents", func(t *testing.T) {
		res, err := Load(Options{LookupEnv: envLookup(nil)})
		if err != nil {
			t.Fatal(err)
		}
		if got := res.Config.Review.Agents; !slices.Equal(got, Categories) {
			t.Errorf("agents = %v", got)
		}
	})

	t.Run("categories aliases agents when agents unset", func(t *testing.T) {
		file := writeFile(t, "review:\n  categories: [bug, security]\n")
		res, err := Load(Options{File: file, LookupEnv: envLookup(nil)})
		if err != nil {
			t.Fatal(err)
		}
		if got := res.Config.Review.Agents; !slices.Equal(got, []string{"bug", "security"}) {
			t.Errorf("agents = %v", got)
		}
	})

	t.Run("agents wins over categories", func(t *testing.T) {
		file := writeFile(t, "review:\n  categories: [bug]\n  agents: [docs, my-custom]\n")
		res, err := Load(Options{File: file, LookupEnv: envLookup(nil)})
		if err != nil {
			t.Fatal(err)
		}
		if got := res.Config.Review.Agents; !slices.Equal(got, []string{"docs", "my-custom"}) {
			t.Errorf("agents = %v", got)
		}
	})

	t.Run("agents flag and env", func(t *testing.T) {
		env := map[string]string{"GITLAB_REVIEWER_REVIEW_AGENTS": "style"}
		fs := flagSet(t, "--agents", "security,my-custom")
		res, err := Load(Options{LookupEnv: envLookup(env), Flags: fs})
		if err != nil {
			t.Fatal(err)
		}
		if got := res.Config.Review.Agents; !slices.Equal(got, []string{"security", "my-custom"}) {
			t.Errorf("agents = %v", got)
		}
	})

	t.Run("per-project override of agents", func(t *testing.T) {
		file := writeFile(t, `review:
  agents: [bug]
projects:
  group/app:
    review:
      agents: [security]
`)
		res, err := Load(Options{File: file, LookupEnv: envLookup(nil)})
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := res.ForProject("group/app")
		if err != nil {
			t.Fatal(err)
		}
		if got := cfg.Review.Agents; !slices.Equal(got, []string{"security"}) {
			t.Errorf("agents = %v", got)
		}
	})

	t.Run("per-project categories alias still works", func(t *testing.T) {
		file := writeFile(t, `projects:
  group/app:
    review:
      categories: [docs]
`)
		res, err := Load(Options{File: file, LookupEnv: envLookup(nil)})
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := res.ForProject("group/app")
		if err != nil {
			t.Fatal(err)
		}
		if got := cfg.Review.Agents; !slices.Equal(got, []string{"docs"}) {
			t.Errorf("agents = %v", got)
		}
	})
}

func TestEnvLists(t *testing.T) {
	env := map[string]string{
		"GITLAB_REVIEWER_GITLAB_PROJECTS":   "group/app, group/other ,",
		"GITLAB_REVIEWER_REVIEW_CATEGORIES": "bug,security",
	}
	res, err := Load(Options{LookupEnv: envLookup(env)})
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Config.GitLab.Projects; len(got) != 2 || got[0] != "group/app" || got[1] != "group/other" {
		t.Errorf("projects = %v", got)
	}
	if got := res.Config.Review.Categories; len(got) != 2 || got[0] != "bug" || got[1] != "security" {
		t.Errorf("categories = %v", got)
	}
}

func TestEnvFallbacks(t *testing.T) {
	t.Run("GITLAB_TOKEN honoured when prefixed var absent", func(t *testing.T) {
		res, err := Load(Options{LookupEnv: envLookup(map[string]string{"GITLAB_TOKEN": "fallback-token"})})
		if err != nil {
			t.Fatal(err)
		}
		if got := res.Config.GitLab.Token; got != "fallback-token" {
			t.Errorf("token = %q", got)
		}
	})
	t.Run("prefixed var wins over GITLAB_TOKEN", func(t *testing.T) {
		res, err := Load(Options{LookupEnv: envLookup(map[string]string{
			"GITLAB_TOKEN":                 "fallback-token",
			"GITLAB_REVIEWER_GITLAB_TOKEN": "primary-token",
		})})
		if err != nil {
			t.Fatal(err)
		}
		if got := res.Config.GitLab.Token; got != "primary-token" {
			t.Errorf("token = %q", got)
		}
	})
}

func TestDurationAndReviewEnvFromFlags(t *testing.T) {
	fs := flagSet(t, "--review-timeout", "3m", "--review-env", "FOO=bar", "--review-env", "BAZ=qux=1")
	res, err := Load(Options{LookupEnv: envLookup(nil), Flags: fs})
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Config.Review.Timeout; got != 3*time.Minute {
		t.Errorf("timeout = %s", got)
	}
	want := map[string]string{"FOO": "bar", "BAZ": "qux=1"}
	got := res.Config.Review.Env
	if len(got) != len(want) || got["FOO"] != want["FOO"] || got["BAZ"] != want["BAZ"] {
		t.Errorf("review.env = %v", got)
	}
}

func TestForProjectOverrides(t *testing.T) {
	file := writeFile(t, `
review:
  max_diff_kb: 100
publish:
  mode: immediate
projects:
  group/app:
    review:
      max_diff_kb: 512
      instructions: "focus on concurrency"
    publish:
      mode: draft
`)
	res, err := Load(Options{File: file, LookupEnv: envLookup(nil)})
	if err != nil {
		t.Fatal(err)
	}

	base := res.Config
	if base.Review.MaxDiffKB != 100 || base.Publish.Mode != "immediate" {
		t.Fatalf("base config wrong: %+v", base.Review.MaxDiffKB)
	}

	over, err := res.ForProject("group/app")
	if err != nil {
		t.Fatal(err)
	}
	if over.Review.MaxDiffKB != 512 {
		t.Errorf("override max_diff_kb = %d", over.Review.MaxDiffKB)
	}
	if over.Review.Instructions != "focus on concurrency" {
		t.Errorf("override instructions = %q", over.Review.Instructions)
	}
	if over.Publish.Mode != "draft" {
		t.Errorf("override publish.mode = %q", over.Publish.Mode)
	}
	// untouched settings keep their base values
	if over.GitLab.BaseURL != "https://gitlab.com" {
		t.Errorf("base_url leaked: %q", over.GitLab.BaseURL)
	}

	same, err := res.ForProject("group/unknown")
	if err != nil {
		t.Fatal(err)
	}
	if same.Review.MaxDiffKB != 100 {
		t.Errorf("unknown project must return base config, got %d", same.Review.MaxDiffKB)
	}
}

func TestRedactedHidesToken(t *testing.T) {
	res, err := Load(Options{LookupEnv: envLookup(map[string]string{"GITLAB_TOKEN": "glpat-secret"})})
	if err != nil {
		t.Fatal(err)
	}
	raw := res.Redacted()
	g := raw["gitlab"].(map[string]any)
	if g["token"] != "[redacted]" {
		t.Errorf("token = %v", g["token"])
	}
	// the loaded config itself must keep the real token for API use
	if res.Config.GitLab.Token != "glpat-secret" {
		t.Errorf("config token clobbered: %q", res.Config.GitLab.Token)
	}
}

func TestExplicitMissingFileErrors(t *testing.T) {
	if _, err := Load(Options{File: "/nonexistent/config.yaml", LookupEnv: envLookup(nil)}); err == nil {
		t.Error("expected error for explicit missing file")
	}
}

func TestValidateRejectsBadValues(t *testing.T) {
	cfg := Default()
	cfg.Publish.Mode = "yolo"
	cfg.Checkout.Mode = "teleport"
	cfg.GitLab.PerPage = 0
	cfg.Publish.MinSeverity = "harsh"
	cfg.Gate.MinSeverity = "fatal"
	cfg.Gate.Approvals = "maybe"
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error")
	}
	for _, want := range []string{"publish.mode", "checkout.mode", "per_page", "publish.min_severity", "gate.min_severity", "gate.approvals"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q: %v", want, err)
		}
	}
}

func TestValidateGateDisabledByDefault(t *testing.T) {
	cfg := Default()
	if cfg.Gate.Enabled() {
		t.Error("gate enabled by default")
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("defaults invalid: %v", err)
	}
	cfg.Gate.MinSeverity = "major"
	if !cfg.Gate.Enabled() {
		t.Error("gate not enabled with min_severity set")
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("enabled gate invalid: %v", err)
	}
}
