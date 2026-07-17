package webui

import (
	"testing"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/config"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review/agents"
)

func TestIntersect(t *testing.T) {
	catalog := []string{"a.example.com", "b.example.com", "c.example.com"}
	got := intersect(catalog, []string{"c.example.com", "a.example.com", "not-in-catalog.example.com"})
	want := []string{"a.example.com", "c.example.com"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("intersect = %v, want %v", got, want)
	}
}

func TestApplyRunOverridesDomainsAndCommands(t *testing.T) {
	t.Run("picks are narrowed to the configured catalog, never widened", func(t *testing.T) {
		cfg := config.Config{Review: config.Review{
			AllowedDomains:  []string{"docs.example.com", "api.example.com"},
			AllowedCommands: []string{"npm test:*"},
		}}
		summary := applyRunOverrides(&cfg, &agents.RunOptions{
			Domains:  []string{"docs.example.com", "not-configured.example.com"},
			Commands: []string{"rm -rf /"}, // not in the configured catalog
		})
		if got := cfg.Review.AllowedDomains; len(got) != 1 || got[0] != "docs.example.com" {
			t.Errorf("AllowedDomains = %v, want only the configured, picked domain", got)
		}
		if len(cfg.Review.AllowedCommands) != 0 {
			t.Errorf("AllowedCommands = %v, want none: the picked command isn't in the configured catalog", cfg.Review.AllowedCommands)
		}
		if summary == "" {
			t.Error("expected a non-empty override summary")
		}
	})

	t.Run("no picks clears the run's grant even though the catalog is configured", func(t *testing.T) {
		cfg := config.Config{Review: config.Review{AllowedDomains: []string{"docs.example.com"}}}
		applyRunOverrides(&cfg, &agents.RunOptions{})
		if len(cfg.Review.AllowedDomains) != 0 {
			t.Errorf("AllowedDomains = %v, want none: an empty pick must not fall back to granting the whole catalog", cfg.Review.AllowedDomains)
		}
	})
}
