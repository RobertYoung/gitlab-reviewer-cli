package agents

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Claude Code records installed plugins in a manifest
// (~/.claude/plugins/installed_plugins.json); each install entry points at
// the plugin's files in a version-addressed cache, and a plugin's agents
// live in the agents/ directory of that root. pluginManifest mirrors the
// parts this tool reads.
type pluginManifest struct {
	Version int `json:"version"`
	// Plugins is keyed by "name@marketplace". A plugin can be installed
	// several times: at user scope and per Claude Code project.
	Plugins map[string][]pluginInstall `json:"plugins"`
}

type pluginInstall struct {
	Scope       string    `json:"scope"`
	InstallPath string    `json:"installPath"`
	LastUpdated time.Time `json:"lastUpdated"`
}

// pluginManifestVersion is the installed_plugins.json format this tool
// understands; other versions are skipped with a warning rather than
// guessed at.
const pluginManifestVersion = 2

// PluginAgentDirs resolves the accepted Claude Code plugins — entries from
// review.claude_plugins, each "name" or "name@marketplace" — to their
// agents directories via the install manifest at manifestPath (typically
// config.DefaultClaudePluginsManifest()). Dirs come back in allowlist
// order, ready to be NewCatalog's plugin layer. Acceptance is explicit:
// an empty allowlist resolves to nothing without touching the manifest,
// and entries that match no installed plugin — or ambiguously match
// several — become warnings, never guesses.
func PluginAgentDirs(manifestPath string, accepted []string) (dirs, warnings []string) {
	if len(accepted) == 0 {
		return nil, nil
	}
	if manifestPath == "" {
		return nil, []string{"plugins: cannot resolve review.claude_plugins: home directory unknown"}
	}
	raw, err := os.ReadFile(manifestPath) //nolint:gosec // fixed path under the user's home
	if err != nil {
		if os.IsNotExist(err) {
			return nil, []string{fmt.Sprintf("plugins: review.claude_plugins is set but %s does not exist (no Claude Code plugins installed?)", manifestPath)}
		}
		return nil, []string{fmt.Sprintf("plugins: cannot read %s: %v", manifestPath, err)}
	}
	var m pluginManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, []string{fmt.Sprintf("plugins: cannot parse %s: %v", manifestPath, err)}
	}
	if m.Version != pluginManifestVersion {
		return nil, []string{fmt.Sprintf("plugins: %s is manifest version %d, this tool reads version %d — skipping plugin agents", manifestPath, m.Version, pluginManifestVersion)}
	}

	keys := slices.Sorted(maps.Keys(m.Plugins))
	used := map[string]bool{} // resolved keys, so duplicate allowlist entries add one dir
	for _, want := range accepted {
		matches := matchPlugin(keys, want)
		switch {
		case len(matches) == 0:
			warnings = append(warnings, fmt.Sprintf("plugins: %q (review.claude_plugins) is not an installed Claude Code plugin", want))
			continue
		case len(matches) > 1:
			warnings = append(warnings, fmt.Sprintf("plugins: %q is ambiguous, qualify it with the marketplace (installed: %s)", want, strings.Join(matches, ", ")))
			continue
		}
		key := matches[0]
		if used[key] {
			continue
		}
		used[key] = true
		install, ok := pickPluginInstall(m.Plugins[key])
		if !ok {
			warnings = append(warnings, fmt.Sprintf("plugins: %s has no usable install path in %s", key, manifestPath))
			continue
		}
		dirs = append(dirs, filepath.Join(install.InstallPath, "agents"))
	}
	return dirs, warnings
}

// matchPlugin finds the manifest keys an allowlist entry names: the full
// "name@marketplace" form matches exactly, the bare name form matches that
// plugin in any marketplace.
func matchPlugin(keys []string, want string) []string {
	if strings.Contains(want, "@") {
		if slices.Contains(keys, want) {
			return []string{want}
		}
		return nil
	}
	var out []string
	for _, k := range keys {
		if name, _, _ := strings.Cut(k, "@"); name == want {
			out = append(out, k)
		}
	}
	return out
}

// pickPluginInstall chooses among a plugin's install records: the
// user-scope install wins (project-scope installs belong to other
// checkouts on this machine, but are still better than nothing), newest
// lastUpdated breaks ties.
func pickPluginInstall(installs []pluginInstall) (pluginInstall, bool) {
	var best pluginInstall
	var found bool
	for _, in := range installs {
		if in.InstallPath == "" {
			continue
		}
		if !found || betterInstall(in, best) {
			best, found = in, true
		}
	}
	return best, found
}

func betterInstall(a, b pluginInstall) bool {
	if (a.Scope == "user") != (b.Scope == "user") {
		return a.Scope == "user"
	}
	return a.LastUpdated.After(b.LastUpdated)
}
