package cli

import (
	"strings"
	"testing"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/config"
)

func TestResolveInstance(t *testing.T) {
	base := config.Default()
	base.GitLab.Token = "shared"

	t.Run("no instances passes through", func(t *testing.T) {
		cfg, err := resolveInstance(base)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.GitLab.BaseURL != base.GitLab.BaseURL {
			t.Errorf("base_url = %q", cfg.GitLab.BaseURL)
		}
	})

	t.Run("single instance selected without prompt", func(t *testing.T) {
		cfg := base
		cfg.GitLab.Instances = []config.Instance{
			{Name: "work", BaseURL: "https://gitlab.example.com", Token: "glpat-work"},
		}
		cfg, err := resolveInstance(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.GitLab.BaseURL != "https://gitlab.example.com" || cfg.GitLab.Token != "glpat-work" {
			t.Errorf("gitlab = %+v", cfg.GitLab)
		}
	})

	t.Run("default_instance skips prompt", func(t *testing.T) {
		cfg := base
		cfg.GitLab.Instances = []config.Instance{
			{Name: "work", BaseURL: "https://gitlab.example.com", Token: "glpat-work"},
			{Name: "personal", BaseURL: "https://gitlab.com", Token: "glpat-personal"},
		}
		cfg.GitLab.DefaultInstance = "personal"
		cfg, err := resolveInstance(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.GitLab.BaseURL != "https://gitlab.com" || cfg.GitLab.Token != "glpat-personal" {
			t.Errorf("gitlab = %+v", cfg.GitLab)
		}
	})

	t.Run("unknown default_instance errors", func(t *testing.T) {
		cfg := base
		cfg.GitLab.Instances = []config.Instance{
			{Name: "work", BaseURL: "https://gitlab.example.com", Token: "glpat-work"},
		}
		cfg.GitLab.DefaultInstance = "nope"
		if _, err := resolveInstance(cfg); err == nil {
			t.Error("expected error for unknown instance")
		}
	})

	t.Run("multiple without selection needs a terminal", func(t *testing.T) {
		// go test runs without a TTY on stdin, so the prompt path must
		// fail with guidance instead of hanging.
		cfg := base
		cfg.GitLab.Instances = []config.Instance{
			{Name: "work", BaseURL: "https://gitlab.example.com", Token: "glpat-work"},
			{Name: "personal", BaseURL: "https://gitlab.com", Token: "glpat-personal"},
		}
		_, err := resolveInstance(cfg)
		if err == nil || !strings.Contains(err.Error(), "--instance") {
			t.Errorf("err = %v", err)
		}
	})
}
