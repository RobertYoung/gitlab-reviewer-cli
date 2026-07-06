package config

import (
	"os"
	"path/filepath"
)

const appName = "gitlab-reviewer"

// The XDG base-directory spec is followed on every platform, including
// macOS: CLI users expect ~/.config over ~/Library/Application Support.

func xdgDir(envVar, fallback string) string {
	if dir := os.Getenv(envVar); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fallback
	}
	return filepath.Join(home, fallback)
}

// DefaultFile is the default settings file location.
func DefaultFile() string {
	return filepath.Join(xdgDir("XDG_CONFIG_HOME", ".config"), appName, "config.yaml")
}

// DefaultAgentsDir is where user-level review agent definitions live.
func DefaultAgentsDir() string {
	return filepath.Join(xdgDir("XDG_CONFIG_HOME", ".config"), appName, "agents")
}

// DefaultClaudeAgentsDir is Claude Code's user-scope subagents directory,
// ~/.claude/agents — the user-level counterpart of a repo's .claude/agents.
// Empty when the home directory cannot be resolved.
func DefaultClaudeAgentsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "agents")
}

// UserAgentDirs are the user-scope agent directories in increasing
// precedence: a definition in DefaultAgentsDir shadows a same-named one in
// DefaultClaudeAgentsDir.
func UserAgentDirs() []string {
	return []string{DefaultClaudeAgentsDir(), DefaultAgentsDir()}
}

// DefaultClaudePluginsManifest is Claude Code's plugin install manifest,
// ~/.claude/plugins/installed_plugins.json — the record of which plugins
// are installed and where their files live in the plugin cache. Empty when
// the home directory cannot be resolved.
func DefaultClaudePluginsManifest() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "plugins", "installed_plugins.json")
}

// DefaultCacheDir is where repository clones and worktrees are cached.
func DefaultCacheDir() string {
	return filepath.Join(xdgDir("XDG_CACHE_HOME", ".cache"), appName)
}

// DefaultStateDir is where logs and raw review dumps are written.
func DefaultStateDir() string {
	return filepath.Join(xdgDir("XDG_STATE_HOME", filepath.Join(".local", "state")), appName)
}

// DefaultLogFile is the default log destination; the TUI owns stdout so logs
// always go to a file.
func DefaultLogFile() string {
	return filepath.Join(DefaultStateDir(), appName+".log")
}
