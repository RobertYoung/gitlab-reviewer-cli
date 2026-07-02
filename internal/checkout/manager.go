// Package checkout gets the repository under review onto disk. Three modes
// (managed cache clone, user-supplied path, structured git root) all
// converge on one invariant: the review runs in a detached git worktree at
// the MR head SHA, never in a user's working tree.
package checkout

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/config"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx"
)

// AskpassTokenEnv carries the GitLab token to the inline git credential
// helper, scoped to git subprocesses only.
const AskpassTokenEnv = "GITLAB_REVIEWER_ASKPASS_TOKEN" //nolint:gosec // env var name, not a credential

// Manager prepares review worktrees.
type Manager struct {
	cfg     config.Checkout
	baseURL *url.URL
	token   string
}

// NewManager builds a Manager. baseURL is the GitLab instance URL; token may
// be empty when transport is ssh.
func NewManager(cfg config.Checkout, baseURL, token string) (*Manager, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("parsing GitLab base URL: %w", err)
	}
	return &Manager{cfg: cfg, baseURL: u, token: token}, nil
}

// Checkout is a ready review worktree. Close removes it (unless the user
// asked to keep worktrees).
type Checkout struct {
	// Path of the detached worktree at the MR head SHA.
	Path string

	repoDir string
	keep    bool
	git     func(ctx context.Context, dir string, args ...string) (string, error)
}

// Close removes the worktree.
func (c *Checkout) Close(ctx context.Context) error {
	if c.keep || c.Path == "" {
		return nil
	}
	_, err := c.git(ctx, c.repoDir, "worktree", "remove", "--force", c.Path)
	return err
}

// Ensure produces a worktree at the MR's head SHA, cloning or fetching as
// needed. progress receives human-readable status lines for the TUI.
func (m *Manager) Ensure(ctx context.Context, mr gitlabx.MRDetail, progress func(string)) (*Checkout, error) {
	if progress == nil {
		progress = func(string) {}
	}
	if mr.HeadSHA == "" {
		return nil, fmt.Errorf("MR !%d has no head SHA", mr.IID)
	}
	if mr.ProjectPath == "" {
		return nil, fmt.Errorf("MR !%d has no project path", mr.IID)
	}

	repoDir, err := m.repoDir(ctx, mr, progress)
	if err != nil {
		return nil, err
	}

	// Make sure the head commit is present, fetching the MR ref if not.
	if _, err := m.git(ctx, repoDir, "cat-file", "-e", mr.HeadSHA+"^{commit}"); err != nil {
		progress(fmt.Sprintf("fetching merge request !%d…", mr.IID))
		ref := fmt.Sprintf("refs/merge-requests/%d/head", mr.IID)
		if _, err := m.git(ctx, repoDir, "fetch", "origin", ref); err != nil {
			return nil, fmt.Errorf("fetching %s: %w", ref, err)
		}
		if _, err := m.git(ctx, repoDir, "cat-file", "-e", mr.HeadSHA+"^{commit}"); err != nil {
			return nil, fmt.Errorf("head commit %s not found after fetch (is the MR up to date?)", short(mr.HeadSHA))
		}
	}

	progress("creating review worktree…")
	wtDir := filepath.Join(m.cfg.CacheDir, "worktrees",
		strings.ReplaceAll(mr.ProjectPath, "/", "__"),
		fmt.Sprintf("mr-%d-%s", mr.IID, short(mr.HeadSHA)))
	if err := os.MkdirAll(filepath.Dir(wtDir), 0o700); err != nil {
		return nil, err
	}
	// A leftover worktree from an interrupted run is reused if intact.
	reused := false
	if _, statErr := os.Stat(wtDir); statErr == nil {
		if _, err := m.git(ctx, wtDir, "rev-parse", "--is-inside-work-tree"); err == nil {
			reused = true
		} else {
			_, _ = m.git(ctx, repoDir, "worktree", "remove", "--force", wtDir)
		}
	}
	if !reused {
		if _, err := m.git(ctx, repoDir, "worktree", "add", "--detach", wtDir, mr.HeadSHA); err != nil {
			return nil, fmt.Errorf("creating worktree: %w", err)
		}
	}

	if err := m.overlayLocalFiles(ctx, repoDir, wtDir, progress); err != nil {
		return nil, err
	}
	return &Checkout{Path: wtDir, repoDir: repoDir, keep: m.cfg.Keep, git: m.git}, nil
}

// overlayLocalFiles copies untracked files matching checkout.local_overlay
// from the user's local clone into the review worktree. This carries team
// conventions that are deliberately kept out of the repository (typically
// via .git/info/exclude) — CLAUDE.md, .claude/ agents and skills — so the
// reviewer follows them. Only applies to path/root modes, where a working
// tree exists to copy from; paths tracked at the MR head are never
// overridden, so the review always sees the committed state of real code.
func (m *Manager) overlayLocalFiles(ctx context.Context, srcDir, wtDir string, progress func(string)) error {
	if m.cfg.Mode == "clone" || len(m.cfg.LocalOverlay) == 0 {
		return nil
	}

	// All untracked files in the source clone, including ignored/excluded.
	out, err := m.git(ctx, srcDir, "ls-files", "--others", "-z")
	if err != nil {
		return fmt.Errorf("listing local overlay candidates: %w", err)
	}
	var candidates []string
	for _, path := range strings.Split(strings.TrimRight(out, "\x00"), "\x00") {
		if path != "" && matchesAny(path, m.cfg.LocalOverlay) {
			candidates = append(candidates, path)
		}
	}
	if len(candidates) == 0 {
		return nil
	}

	// Never shadow files that exist at the MR head commit.
	trackedOut, err := m.git(ctx, wtDir, "ls-files", "-z")
	if err != nil {
		return fmt.Errorf("listing worktree files: %w", err)
	}
	tracked := map[string]bool{}
	for _, path := range strings.Split(strings.TrimRight(trackedOut, "\x00"), "\x00") {
		tracked[path] = true
	}

	copied := 0
	for _, path := range candidates {
		if tracked[path] {
			continue
		}
		src := filepath.Join(srcDir, path)
		info, err := os.Lstat(src)
		if err != nil || !info.Mode().IsRegular() {
			continue // skip symlinks and anything unusual
		}
		// git paths are repo-relative, but keep the write provably inside
		// the worktree regardless.
		dst := filepath.Join(wtDir, path)
		if !strings.HasPrefix(dst, filepath.Clean(wtDir)+string(filepath.Separator)) {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return err
		}
		data, err := os.ReadFile(src) //nolint:gosec // path comes from git ls-files in the user's own clone
		if err != nil {
			return fmt.Errorf("reading overlay file %s: %w", path, err)
		}
		if err := os.WriteFile(dst, data, info.Mode().Perm()); err != nil { //nolint:gosec // dst is prefix-checked against the worktree above
			return fmt.Errorf("writing overlay file %s: %w", path, err)
		}
		copied++
	}
	if copied > 0 {
		progress(fmt.Sprintf("copied %d local convention file(s) into the worktree", copied))
	}
	return nil
}

func matchesAny(path string, globs []string) bool {
	for _, g := range globs {
		if ok, err := doublestar.Match(g, path); err == nil && ok {
			return true
		}
	}
	return false
}

// repoDir returns the repository (clone) the worktree hangs off, creating
// or validating it according to the checkout mode.
func (m *Manager) repoDir(ctx context.Context, mr gitlabx.MRDetail, progress func(string)) (string, error) {
	switch m.cfg.Mode {
	case "clone":
		dir := filepath.Join(m.cfg.CacheDir, "repos", m.baseURL.Hostname(), mr.ProjectPath+".git")
		if _, err := os.Stat(dir); err == nil {
			return dir, nil
		}
		progress(fmt.Sprintf("cloning %s…", mr.ProjectPath))
		if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
			return "", err
		}
		if _, err := m.git(ctx, "", "clone", "--bare", m.remoteURL(mr.ProjectPath), dir); err != nil {
			return "", fmt.Errorf("cloning %s: %w", mr.ProjectPath, err)
		}
		return dir, nil

	case "path":
		return m.validateLocalRepo(ctx, m.cfg.Path, mr.ProjectPath)

	case "root":
		dir := filepath.Join(expandHome(m.cfg.Root), m.baseURL.Hostname(), mr.ProjectPath)
		if _, err := os.Stat(dir); err != nil {
			if !m.cfg.CloneMissing {
				return "", fmt.Errorf("no clone at %s (set checkout.clone_missing to clone automatically)", dir)
			}
			progress(fmt.Sprintf("cloning %s into git root…", mr.ProjectPath))
			if err := os.MkdirAll(filepath.Dir(dir), 0o750); err != nil {
				return "", err
			}
			if _, err := m.git(ctx, "", "clone", m.remoteURL(mr.ProjectPath), dir); err != nil {
				return "", fmt.Errorf("cloning %s: %w", mr.ProjectPath, err)
			}
			return dir, nil
		}
		return m.validateLocalRepo(ctx, dir, mr.ProjectPath)

	default:
		return "", fmt.Errorf("unknown checkout mode %q", m.cfg.Mode)
	}
}

// validateLocalRepo checks that dir is a git repo whose origin matches the
// project, so a review can never run against the wrong repository.
func (m *Manager) validateLocalRepo(ctx context.Context, dir, projectPath string) (string, error) {
	dir = expandHome(dir)
	if _, err := os.Stat(dir); err != nil {
		return "", fmt.Errorf("checkout path %s does not exist", dir)
	}
	remote, err := m.git(ctx, dir, "remote", "get-url", "origin")
	if err != nil {
		return "", fmt.Errorf("%s is not a git repository with an origin remote: %w", dir, err)
	}
	remote = strings.TrimSpace(remote)
	if !strings.Contains(remote, projectPath) {
		return "", fmt.Errorf("%s origin (%s) does not match project %s", dir, remote, projectPath)
	}
	return dir, nil
}

func (m *Manager) remoteURL(projectPath string) string {
	if m.cfg.Transport == "ssh" {
		return fmt.Sprintf("git@%s:%s.git", m.baseURL.Hostname(), projectPath)
	}
	return m.baseURL.JoinPath(projectPath + ".git").String()
}

// credentialHelper answers git's credential prompts with the token from the
// environment. The ${...} stays unexpanded in argv — the shell resolves it
// inside the git subprocess — so the token never appears in process lists.
const credentialHelper = `!f() { test "$1" = get && printf 'username=oauth2\npassword=%s\n' "${` + AskpassTokenEnv + `}"; }; f`

// git runs one git command with interactive prompts disabled and, for HTTPS
// transport, an inline credential helper supplying the token via env.
func (m *Manager) git(ctx context.Context, dir string, args ...string) (string, error) {
	env := append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if m.token != "" && m.cfg.Transport != "ssh" {
		// Empty -c credential.helper= first disables any configured
		// helpers (keychain etc.) so ours is authoritative.
		args = append([]string{"-c", "credential.helper=", "-c", "credential.helper=" + credentialHelper}, args...)
		env = append(env, AskpassTokenEnv+"="+m.token)
	}
	cmd := exec.CommandContext(ctx, "git", args...) //nolint:gosec // args are built internally, never from model output
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}
