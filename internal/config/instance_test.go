package config

import (
	"strings"
	"testing"

	"github.com/spf13/pflag"
)

const instancesYAML = `
gitlab:
  token: shared-token
  instances:
    - name: work
      base_url: https://gitlab.example.com
      token: glpat-work
    - name: personal
      base_url: https://gitlab.com
`

func TestInstancesFromFile(t *testing.T) {
	file := writeFile(t, instancesYAML)
	res, err := Load(Options{File: file, LookupEnv: envLookup(nil)})
	if err != nil {
		t.Fatal(err)
	}
	cfg := res.Config
	if len(cfg.GitLab.Instances) != 2 {
		t.Fatalf("instances = %+v", cfg.GitLab.Instances)
	}
	if cfg.GitLab.Instances[0].Name != "work" || cfg.GitLab.Instances[0].BaseURL != "https://gitlab.example.com" {
		t.Errorf("instance[0] = %+v", cfg.GitLab.Instances[0])
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("instances config must validate: %v", err)
	}
}

func TestDefaultInstancePrecedence(t *testing.T) {
	file := writeFile(t, instancesYAML+"  default_instance: work\n")
	env := map[string]string{"GITLAB_REVIEWER_GITLAB_DEFAULT_INSTANCE": "personal"}

	t.Run("file", func(t *testing.T) {
		res, err := Load(Options{File: file, LookupEnv: envLookup(nil)})
		if err != nil {
			t.Fatal(err)
		}
		if got := res.Config.GitLab.DefaultInstance; got != "work" {
			t.Errorf("default_instance = %q", got)
		}
	})

	t.Run("env over file", func(t *testing.T) {
		res, err := Load(Options{File: file, LookupEnv: envLookup(env)})
		if err != nil {
			t.Fatal(err)
		}
		if got := res.Config.GitLab.DefaultInstance; got != "personal" {
			t.Errorf("default_instance = %q", got)
		}
	})

	t.Run("flag over env", func(t *testing.T) {
		fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
		fs.String("instance", "", "")
		if err := fs.Parse([]string{"--instance", "work"}); err != nil {
			t.Fatal(err)
		}
		res, err := Load(Options{File: file, LookupEnv: envLookup(env), Flags: fs})
		if err != nil {
			t.Fatal(err)
		}
		if got := res.Config.GitLab.DefaultInstance; got != "work" {
			t.Errorf("default_instance = %q", got)
		}
	})
}

func TestWithInstance(t *testing.T) {
	cfg := Default()
	cfg.GitLab.Token = "shared-token"
	cfg.GitLab.Instances = []Instance{
		{Name: "work", BaseURL: "https://gitlab.example.com", Token: "glpat-work"},
		{Name: "personal", BaseURL: "https://gitlab.com"},
	}

	work, err := cfg.WithInstance("work")
	if err != nil {
		t.Fatal(err)
	}
	if work.GitLab.BaseURL != "https://gitlab.example.com" || work.GitLab.Token != "glpat-work" {
		t.Errorf("work instance not applied: %+v", work.GitLab)
	}

	// empty instance token keeps gitlab.token as the fallback
	personal, err := cfg.WithInstance("personal")
	if err != nil {
		t.Fatal(err)
	}
	if personal.GitLab.BaseURL != "https://gitlab.com" || personal.GitLab.Token != "shared-token" {
		t.Errorf("personal instance not applied: %+v", personal.GitLab)
	}

	if _, err := cfg.WithInstance("nope"); err == nil || !strings.Contains(err.Error(), "work, personal") {
		t.Errorf("unknown instance error = %v", err)
	}
}

func TestValidateInstances(t *testing.T) {
	cfg := Default()
	cfg.GitLab.Instances = []Instance{
		{Name: "", BaseURL: "https://a.example.com"},
		{Name: "dup", BaseURL: "https://b.example.com"},
		{Name: "dup", BaseURL: "not-a-url"},
	}
	cfg.GitLab.DefaultInstance = "missing"
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error")
	}
	for _, want := range []string{"name is required", "duplicate name", "base_url", "default_instance"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q: %v", want, err)
		}
	}
}

func TestValidateGitLabInstances(t *testing.T) {
	cfg := Default()
	cfg.GitLab.Instances = []Instance{
		{Name: "work", BaseURL: "https://gitlab.example.com", Token: "glpat-work"},
		{Name: "personal", BaseURL: "https://gitlab.com"},
	}

	if err := cfg.ValidateGitLab(); err == nil || !strings.Contains(err.Error(), "personal") {
		t.Errorf("tokenless instance without fallback must fail, got %v", err)
	}

	cfg.GitLab.Token = "shared-token"
	if err := cfg.ValidateGitLab(); err != nil {
		t.Errorf("shared fallback token must satisfy instances: %v", err)
	}
}

func TestRedactedHidesInstanceTokens(t *testing.T) {
	file := writeFile(t, instancesYAML)
	res, err := Load(Options{File: file, LookupEnv: envLookup(nil)})
	if err != nil {
		t.Fatal(err)
	}
	raw := res.Redacted()
	instances := raw["gitlab"].(map[string]any)["instances"].([]any)
	if tok := instances[0].(map[string]any)["token"]; tok != "[redacted]" {
		t.Errorf("instance token = %v", tok)
	}
	// the loaded config keeps the real token for API use
	if res.Config.GitLab.Instances[0].Token != "glpat-work" {
		t.Errorf("config instance token clobbered: %q", res.Config.GitLab.Instances[0].Token)
	}
}
