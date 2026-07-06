package agents

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// writeManifest writes an installed_plugins.json and returns its path.
func writeManifest(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "installed_plugins.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPluginAgentDirs(t *testing.T) {
	manifest := writeManifest(t, `{
	  "version": 2,
	  "plugins": {
	    "review-pack@official": [
	      {"scope": "project", "projectPath": "/elsewhere", "installPath": "/cache/official/review-pack/2.0.0", "lastUpdated": "2026-07-01T00:00:00Z"},
	      {"scope": "user", "installPath": "/cache/official/review-pack/1.0.0", "lastUpdated": "2026-06-01T00:00:00Z"}
	    ],
	    "linters@official": [
	      {"scope": "user", "installPath": "/cache/official/linters/1.0.0", "lastUpdated": "2026-05-01T00:00:00Z"},
	      {"scope": "user", "installPath": "/cache/official/linters/1.1.0", "lastUpdated": "2026-06-01T00:00:00Z"}
	    ],
	    "linters@community": [
	      {"scope": "user", "installPath": "/cache/community/linters/1.0.0", "lastUpdated": "2026-06-01T00:00:00Z"}
	    ]
	  }
	}`)

	// Bare name, unambiguous: the user-scope install wins over a newer
	// project-scope one.
	dirs, warns := PluginAgentDirs(manifest, []string{"review-pack"})
	if want := []string{filepath.FromSlash("/cache/official/review-pack/1.0.0/agents")}; !slices.Equal(dirs, want) {
		t.Errorf("dirs = %v, want %v", dirs, want)
	}
	if len(warns) != 0 {
		t.Errorf("warnings: %v", warns)
	}

	// Full name: picks the marketplace exactly; among same-scope installs
	// the newest lastUpdated wins. Dirs keep allowlist order.
	dirs, warns = PluginAgentDirs(manifest, []string{"linters@official", "review-pack@official"})
	want := []string{
		filepath.FromSlash("/cache/official/linters/1.1.0/agents"),
		filepath.FromSlash("/cache/official/review-pack/1.0.0/agents"),
	}
	if !slices.Equal(dirs, want) {
		t.Errorf("dirs = %v, want %v", dirs, want)
	}
	if len(warns) != 0 {
		t.Errorf("warnings: %v", warns)
	}

	// Ambiguous bare name: warned, not guessed.
	dirs, warns = PluginAgentDirs(manifest, []string{"linters"})
	if len(dirs) != 0 {
		t.Errorf("ambiguous name resolved to %v", dirs)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "ambiguous") || !strings.Contains(warns[0], "linters@community") {
		t.Errorf("warnings: %v", warns)
	}

	// Unknown plugin: warned. Duplicate entries add one dir.
	dirs, warns = PluginAgentDirs(manifest, []string{"nope", "review-pack", "review-pack@official"})
	if len(dirs) != 1 {
		t.Errorf("dirs = %v", dirs)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], `"nope"`) {
		t.Errorf("warnings: %v", warns)
	}
}

func TestPluginAgentDirsEmptyAllowlist(t *testing.T) {
	// Nothing is accepted, so the manifest (existent or not) is never read.
	dirs, warns := PluginAgentDirs(filepath.Join(t.TempDir(), "nope.json"), nil)
	if dirs != nil || warns != nil {
		t.Errorf("dirs=%v warns=%v", dirs, warns)
	}
}

func TestPluginAgentDirsManifestProblems(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.json")
	for name, tc := range map[string]struct {
		path string
		want string
	}{
		"missing":     {missing, "does not exist"},
		"unparseable": {writeManifest(t, "{"), "cannot parse"},
		"version":     {writeManifest(t, `{"version": 3, "plugins": {}}`), "version 3"},
	} {
		dirs, warns := PluginAgentDirs(tc.path, []string{"review-pack"})
		if len(dirs) != 0 {
			t.Errorf("%s: dirs = %v", name, dirs)
		}
		if len(warns) != 1 || !strings.Contains(warns[0], tc.want) {
			t.Errorf("%s: warnings = %v, want one containing %q", name, warns, tc.want)
		}
	}
}

func TestCatalogPluginAgents(t *testing.T) {
	pluginDir := t.TempDir()
	userDir := t.TempDir()
	// Plugin agents load recursively — plugin layouts are not ours to flatten.
	writeAgent(t, pluginDir, "security.md", "Plugin security prompt.\n")
	writeAgent(t, filepath.Join(pluginDir, "nested"), "sql.md", "Plugin sql prompt.\n")
	writeAgent(t, pluginDir, "shared.md", "Plugin shared prompt.\n")
	writeAgent(t, userDir, "shared.md", "User shared prompt.\n")

	c := NewCatalog([]string{pluginDir}, []string{userDir})
	byName := map[string]Agent{}
	for _, a := range c.All() {
		byName[a.Name] = a
	}
	// Plugin shadows builtin in place; nested definitions are found.
	if a := byName["security"]; a.Source != SourcePlugin || a.Prompt != "Plugin security prompt." {
		t.Errorf("security: %+v", a)
	}
	if a := byName["sql"]; a.Source != SourcePlugin {
		t.Errorf("nested plugin agent: %+v", a)
	}
	// User shadows plugin.
	if a := byName["shared"]; a.Source != SourceUser || a.Prompt != "User shared prompt." {
		t.Errorf("shared: %+v", a)
	}
	if n := len(c.All()); n != len(Builtins())+2 {
		t.Errorf("catalog size %d: %v", n, c.Names())
	}
	if len(c.Warnings()) != 0 {
		t.Errorf("warnings: %v", c.Warnings())
	}
}

func TestCatalogWithWarnings(t *testing.T) {
	base := NewCatalog(nil, nil)
	c := base.WithWarnings("plugins: something went wrong")
	if len(c.Warnings()) != 1 {
		t.Errorf("warnings: %v", c.Warnings())
	}
	if len(base.Warnings()) != 0 {
		t.Errorf("base catalog mutated: %v", base.Warnings())
	}
	if c.WithWarnings() != c {
		t.Error("WithWarnings() without messages should return the catalog unchanged")
	}
}

func TestLoadTreeDuplicateAndMissing(t *testing.T) {
	root := t.TempDir()
	writeAgent(t, root, "dup.md", "First prompt.\n")
	writeAgent(t, filepath.Join(root, "sub"), "dup.md", "Second prompt.\n")
	got, warns := loadTree(root, SourcePlugin)
	if len(got) != 1 || got[0].Prompt != "First prompt." {
		t.Errorf("agents: %+v", got)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "duplicate") {
		t.Errorf("warnings: %v", warns)
	}

	got, warns = loadTree(filepath.Join(root, "nope"), SourcePlugin)
	if len(got) != 0 || len(warns) != 0 {
		t.Errorf("missing root: agents=%v warns=%v", got, warns)
	}
}
