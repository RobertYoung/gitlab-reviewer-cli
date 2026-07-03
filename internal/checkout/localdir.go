package checkout

import (
	"net/url"
	"os"
	"path/filepath"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/config"
)

// LocalRepoDir resolves the local clone the checkout config points at for
// projectPath — the user's own working tree in path and root modes. It
// reports false in clone mode or when the directory does not exist. Used to
// read repo-shipped review agents straight from disk, which covers
// definitions deliberately kept untracked (e.g. via .git/info/exclude);
// unlike Manager.Ensure it never clones, fetches, or validates remotes.
func LocalRepoDir(cfg config.Checkout, baseURL, projectPath string) (string, bool) {
	var dir string
	switch cfg.Mode {
	case "path":
		dir = expandHome(cfg.Path)
	case "root":
		u, err := url.Parse(baseURL)
		if err != nil {
			return "", false
		}
		dir = filepath.Join(expandHome(cfg.Root), u.Hostname(), projectPath)
	default:
		return "", false
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return "", false
	}
	return dir, true
}
