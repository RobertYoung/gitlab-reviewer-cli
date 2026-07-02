package checkout

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// RepoInfo describes one cached clone.
type RepoInfo struct {
	// Project is host/group/app derived from the cache layout.
	Project string
	Path    string
	Size    int64
	LastUse time.Time
}

// ListCache returns the cached clones under cacheDir/repos, largest first.
func ListCache(cacheDir string) ([]RepoInfo, error) {
	reposDir := filepath.Join(cacheDir, "repos")
	var repos []RepoInfo
	err := filepath.WalkDir(reposDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() || !strings.HasSuffix(path, ".git") {
			return err
		}
		size, mtime := dirStats(path)
		rel, _ := filepath.Rel(reposDir, path)
		repos = append(repos, RepoInfo{
			Project: strings.TrimSuffix(rel, ".git"),
			Path:    path,
			Size:    size,
			LastUse: mtime,
		})
		return filepath.SkipDir
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	slices.SortFunc(repos, func(a, b RepoInfo) int {
		return int(b.Size - a.Size)
	})
	return repos, nil
}

// dirStats returns total size and the most useful "last used" signal: the
// mtime of FETCH_HEAD (updated on every fetch) falling back to the newest
// file in the repo.
func dirStats(dir string) (int64, time.Time) {
	var size int64
	var last time.Time
	if fi, err := os.Stat(filepath.Join(dir, "FETCH_HEAD")); err == nil {
		last = fi.ModTime()
	}
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // best-effort accounting
		}
		if fi, err := d.Info(); err == nil {
			size += fi.Size()
			if last.IsZero() && fi.ModTime().After(last) {
				last = fi.ModTime()
			}
		}
		return nil
	})
	return size, last
}

// CleanResult reports what CleanCache removed.
type CleanResult struct {
	Removed    []string
	FreedBytes int64
}

// CleanCache removes review worktrees and, when the clone cache exceeds
// maxMB (0 = no limit; all=true removes everything), evicts least-recently
// used clones until it fits.
func CleanCache(_ context.Context, cacheDir string, maxMB int, all bool) (*CleanResult, error) {
	res := &CleanResult{}

	// Worktrees are transient by design; remove them wholesale.
	wtDir := filepath.Join(cacheDir, "worktrees")
	if size, _ := dirStats(wtDir); size > 0 {
		if err := os.RemoveAll(wtDir); err != nil {
			return nil, fmt.Errorf("removing worktrees: %w", err)
		}
		res.Removed = append(res.Removed, wtDir)
		res.FreedBytes += size
	}

	repos, err := ListCache(cacheDir)
	if err != nil {
		return nil, err
	}
	var total int64
	for _, r := range repos {
		total += r.Size
	}

	budget := int64(maxMB) * 1024 * 1024
	if all {
		budget = 0
	} else if maxMB <= 0 || total <= budget {
		return res, nil
	}

	// Evict least-recently used first.
	slices.SortFunc(repos, func(a, b RepoInfo) int {
		return a.LastUse.Compare(b.LastUse)
	})
	for _, r := range repos {
		if total <= budget {
			break
		}
		// A removed repo orphans git's worktree bookkeeping; worktrees were
		// already deleted above, so the whole clone can go.
		if err := os.RemoveAll(r.Path); err != nil {
			return res, fmt.Errorf("removing %s: %w", r.Project, err)
		}
		res.Removed = append(res.Removed, r.Project)
		res.FreedBytes += r.Size
		total -= r.Size
	}
	return res, nil
}

// EvictIfNeeded enforces the configured cache budget in the background
// after a checkout; errors are reported to the caller for logging only.
func (m *Manager) EvictIfNeeded(ctx context.Context) (*CleanResult, error) {
	if m.cfg.Mode != "clone" || m.cfg.CacheMaxMB <= 0 {
		return &CleanResult{}, nil
	}
	repos, err := ListCache(m.cfg.CacheDir)
	if err != nil {
		return nil, err
	}
	var total int64
	for _, r := range repos {
		total += r.Size
	}
	if total <= int64(m.cfg.CacheMaxMB)*1024*1024 {
		return &CleanResult{}, nil
	}
	// Over budget: evict LRU clones but leave live worktrees alone — only
	// CleanCache (the explicit command) removes those.
	slices.SortFunc(repos, func(a, b RepoInfo) int {
		return a.LastUse.Compare(b.LastUse)
	})
	res := &CleanResult{}
	budget := int64(m.cfg.CacheMaxMB) * 1024 * 1024
	for _, r := range repos {
		if total <= budget {
			break
		}
		if m.repoHasWorktrees(ctx, r.Path) {
			continue
		}
		if err := os.RemoveAll(r.Path); err != nil {
			return res, err
		}
		res.Removed = append(res.Removed, r.Project)
		res.FreedBytes += r.Size
		total -= r.Size
	}
	return res, nil
}

func (m *Manager) repoHasWorktrees(ctx context.Context, repoDir string) bool {
	out, err := m.git(ctx, repoDir, "worktree", "list", "--porcelain")
	if err != nil {
		return true // be conservative
	}
	// The main (bare) entry is always listed; extra entries mean live trees.
	return strings.Count(out, "worktree ") > 1
}
