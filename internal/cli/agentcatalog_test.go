package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/config"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review/agents"
)

func TestNewAgentCatalogLoadsAcceptedPlugins(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	// A Claude Code plugin install: manifest entry plus the cached files.
	pluginRoot := filepath.Join(home, ".claude", "plugins", "cache", "official", "review-pack", "1.0.0")
	writeFile(t, filepath.Join(pluginRoot, "agents", "lock-hazards.md"), "Find migration lock hazards.\n")
	writeFile(t, filepath.Join(home, ".claude", "plugins", "installed_plugins.json"), `{
	  "version": 2,
	  "plugins": {
	    "review-pack@official": [
	      {"scope": "user", "installPath": `+jsonPath(pluginRoot)+`, "lastUpdated": "2026-07-01T00:00:00Z"}
	    ]
	  }
	}`)
	// A user-scope Claude Code agent still loads alongside.
	writeFile(t, filepath.Join(home, ".claude", "agents", "own.md"), "My own prompt.\n")

	cfg := config.Default()
	cfg.Review.ClaudePlugins = []string{"review-pack"}
	c := newAgentCatalog(cfg)

	byName := map[string]agents.Agent{}
	for _, a := range c.All() {
		byName[a.Name] = a
	}
	if a := byName["lock-hazards"]; a.Source != agents.SourcePlugin {
		t.Errorf("plugin agent: %+v", a)
	}
	if a := byName["own"]; a.Source != agents.SourceUser {
		t.Errorf("user agent: %+v", a)
	}
	if len(c.Warnings()) != 0 {
		t.Errorf("warnings: %v", c.Warnings())
	}

	// Not accepted → not loaded, and discovery problems become warnings.
	cfg.Review.ClaudePlugins = nil
	c = newAgentCatalog(cfg)
	if _, ok := agentByName(c, "lock-hazards"); ok {
		t.Error("plugin agent loaded without acceptance")
	}

	cfg.Review.ClaudePlugins = []string{"nope"}
	c = newAgentCatalog(cfg)
	if _, ok := agentByName(c, "lock-hazards"); ok {
		t.Error("unaccepted plugin agent loaded")
	}
	if w := c.Warnings(); len(w) != 1 || !strings.Contains(w[0], `"nope"`) {
		t.Errorf("warnings: %v", w)
	}
}

func agentByName(c *agents.Catalog, name string) (agents.Agent, bool) {
	for _, a := range c.All() {
		if a.Name == name {
			return a, true
		}
	}
	return agents.Agent{}, false
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// jsonPath quotes a filesystem path for embedding in JSON (Windows
// backslashes would otherwise be parsed as escapes).
func jsonPath(p string) string {
	return `"` + strings.ReplaceAll(p, `\`, `\\`) + `"`
}
