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
