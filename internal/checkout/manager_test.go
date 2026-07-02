package checkout

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/config"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx"
)

// setupOrigin creates a "remote" repo at <base>/group/app.git with a main
// commit and an MR head commit reachable via refs/merge-requests/7/head.
// Returns the base directory (the fake GitLab host root) and the MR head SHA.
func setupOrigin(t *testing.T) (string, string) {
	t.Helper()
	base := t.TempDir()

	work := filepath.Join(base, "work")
	run(t, "", "git", "init", "-q", "-b", "main", work)
	run(t, work, "git", "config", "user.email", "test@example.com")
	run(t, work, "git", "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(work, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run(t, work, "git", "add", ".")
	run(t, work, "git", "commit", "-q", "-m", "initial")

	run(t, work, "git", "checkout", "-q", "-b", "feature")
	if err := os.WriteFile(filepath.Join(work, "feature.go"), []byte("package main // feature\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run(t, work, "git", "add", ".")
	run(t, work, "git", "commit", "-q", "-m", "feature work")
	headSHA := strings.TrimSpace(run(t, work, "git", "rev-parse", "HEAD"))

	origin := filepath.Join(base, "group", "app.git")
	if err := os.MkdirAll(filepath.Dir(origin), 0o750); err != nil {
		t.Fatal(err)
	}
	run(t, "", "git", "clone", "-q", "--bare", work, origin)
	// GitLab exposes MR heads under refs/merge-requests/<iid>/head.
	run(t, origin, "git", "update-ref", "refs/merge-requests/7/head", headSHA)
	// The bare clone already has the feature branch; delete it so the test
	// proves we fetch through the MR ref, not the branch.
	run(t, origin, "git", "branch", "-D", "feature")

	return base, headSHA
}

func run(t *testing.T, dir string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...) //nolint:gosec // test helper runs fixed git commands
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
	return string(out)
}

func mrDetail(head string) gitlabx.MRDetail {
	return gitlabx.MRDetail{
		MRSummary: gitlabx.MRSummary{ProjectPath: "group/app", IID: 7, HeadSHA: head},
	}
}

func newManager(t *testing.T, cfg config.Checkout, base string) *Manager {
	t.Helper()
	m, err := NewManager(cfg, "file://"+base, "")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestEnsureCloneMode(t *testing.T) {
	base, head := setupOrigin(t)
	cache := t.TempDir()
	m := newManager(t, config.Checkout{Mode: "clone", Transport: "https", CacheDir: cache}, base)

	var progress []string
	co, err := m.Ensure(context.Background(), mrDetail(head), func(s string) { progress = append(progress, s) })
	if err != nil {
		t.Fatal(err)
	}

	// worktree is at the MR head with the feature file present
	if _, err := os.Stat(filepath.Join(co.Path, "feature.go")); err != nil {
		t.Errorf("worktree missing feature file: %v", err)
	}
	got := strings.TrimSpace(run(t, co.Path, "git", "rev-parse", "HEAD"))
	if got != head {
		t.Errorf("worktree at %s, want %s", got, head)
	}
	if len(progress) == 0 {
		t.Error("no progress reported")
	}

	// second Ensure for the same MR reuses clone and worktree
	co2, err := m.Ensure(context.Background(), mrDetail(head), nil)
	if err != nil {
		t.Fatal(err)
	}
	if co2.Path != co.Path {
		t.Errorf("expected worktree reuse, got %s and %s", co.Path, co2.Path)
	}

	if err := co.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(co.Path); !os.IsNotExist(err) {
		t.Errorf("worktree should be removed, stat err = %v", err)
	}
}

func TestEnsureKeepWorktree(t *testing.T) {
	base, head := setupOrigin(t)
	m := newManager(t, config.Checkout{Mode: "clone", Transport: "https", CacheDir: t.TempDir(), Keep: true}, base)
	co, err := m.Ensure(context.Background(), mrDetail(head), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := co.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(co.Path); err != nil {
		t.Errorf("worktree should be kept: %v", err)
	}
}

func TestEnsurePathMode(t *testing.T) {
	base, head := setupOrigin(t)

	// user's own clone of the project
	local := filepath.Join(t.TempDir(), "app")
	run(t, "", "git", "clone", "-q", "file://"+filepath.Join(base, "group", "app.git"), local)

	m := newManager(t, config.Checkout{Mode: "path", Path: local, Transport: "https", CacheDir: t.TempDir()}, base)
	co, err := m.Ensure(context.Background(), mrDetail(head), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = co.Close(context.Background()) }()

	if co.Path == local {
		t.Error("review must run in a worktree, not the user's clone")
	}
	if _, err := os.Stat(filepath.Join(co.Path, "feature.go")); err != nil {
		t.Errorf("worktree missing feature file: %v", err)
	}

	// user's working tree untouched
	if _, err := os.Stat(filepath.Join(local, "feature.go")); !os.IsNotExist(err) {
		t.Error("user clone must not be modified")
	}
}

func TestEnsurePathModeWrongRemote(t *testing.T) {
	base, head := setupOrigin(t)
	other := filepath.Join(t.TempDir(), "other")
	run(t, "", "git", "init", "-q", other)
	run(t, other, "git", "remote", "add", "origin", "https://example.com/some/other.git")

	m := newManager(t, config.Checkout{Mode: "path", Path: other, Transport: "https", CacheDir: t.TempDir()}, base)
	if _, err := m.Ensure(context.Background(), mrDetail(head), nil); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Errorf("want remote mismatch error, got %v", err)
	}
}

func TestEnsureRootMode(t *testing.T) {
	base, head := setupOrigin(t)

	// structured root: <root>/<host>/<project_path>; host for file:// URLs
	// is empty, so the layout is <root>//group/app → filepath.Join drops it.
	root := t.TempDir()
	local := filepath.Join(root, "group", "app")
	run(t, "", "git", "clone", "-q", "file://"+filepath.Join(base, "group", "app.git"), local)

	m := newManager(t, config.Checkout{Mode: "root", Root: root, Transport: "https", CacheDir: t.TempDir()}, base)
	co, err := m.Ensure(context.Background(), mrDetail(head), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = co.Close(context.Background()) }()
	if _, err := os.Stat(filepath.Join(co.Path, "feature.go")); err != nil {
		t.Errorf("worktree missing feature file: %v", err)
	}
}

func TestEnsureRootModeMissingClone(t *testing.T) {
	base, head := setupOrigin(t)
	m := newManager(t, config.Checkout{Mode: "root", Root: t.TempDir(), Transport: "https", CacheDir: t.TempDir()}, base)
	_, err := m.Ensure(context.Background(), mrDetail(head), nil)
	if err == nil || !strings.Contains(err.Error(), "clone_missing") {
		t.Errorf("want missing-clone hint, got %v", err)
	}
}

func TestEnsureLocalOverlay(t *testing.T) {
	base, head := setupOrigin(t)

	// user's clone with local-only Claude convention files, deliberately
	// kept out of the repo via .git/info/exclude
	local := filepath.Join(t.TempDir(), "app")
	run(t, "", "git", "clone", "-q", "file://"+filepath.Join(base, "group", "app.git"), local)
	writeLocal := func(rel, content string) {
		t.Helper()
		path := filepath.Join(local, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeLocal("CLAUDE.md", "# repo conventions\n")
	writeLocal("CLAUDE.local.md", "# personal notes\n")
	writeLocal(".claude/skills/review/SKILL.md", "standards skill\n")
	writeLocal("pkg/CLAUDE.md", "# nested conventions\n")
	writeLocal(".env", "SECRET=1\n")   // excluded but must NOT be copied
	writeLocal("scratch.txt", "wip\n") // untracked, not matching globs
	writeLocal(".git/info/exclude", "CLAUDE.md\nCLAUDE.local.md\n.claude/\n.env\n")

	m := newManager(t, config.Checkout{
		Mode: "path", Path: local, Transport: "https", CacheDir: t.TempDir(),
		LocalOverlay: config.Default().Checkout.LocalOverlay,
	}, base)
	co, err := m.Ensure(context.Background(), mrDetail(head), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = co.Close(context.Background()) }()

	for _, want := range []string{"CLAUDE.md", "CLAUDE.local.md", ".claude/skills/review/SKILL.md", "pkg/CLAUDE.md"} {
		if _, err := os.Stat(filepath.Join(co.Path, want)); err != nil {
			t.Errorf("overlay file %s missing from worktree: %v", want, err)
		}
	}
	for _, banned := range []string{".env", "scratch.txt"} {
		if _, err := os.Stat(filepath.Join(co.Path, banned)); !os.IsNotExist(err) {
			t.Errorf("%s must not be copied into the worktree", banned)
		}
	}

	// overlaid files must not dirty the review checkout's git view of
	// tracked content
	out := run(t, co.Path, "git", "status", "--porcelain", "--untracked-files=no")
	if strings.TrimSpace(out) != "" {
		t.Errorf("worktree tracked files dirty:\n%s", out)
	}
}

func TestEnsureLocalOverlayNeverShadowsTracked(t *testing.T) {
	base, head := setupOrigin(t)

	local := filepath.Join(t.TempDir(), "app")
	run(t, "", "git", "clone", "-q", "file://"+filepath.Join(base, "group", "app.git"), local)
	// main.go is tracked at the MR head; a local untracked variant with
	// the same name must not replace it (contrived, but the guard matters)
	m := newManager(t, config.Checkout{
		Mode: "path", Path: local, Transport: "https", CacheDir: t.TempDir(),
		LocalOverlay: []string{"main.go", "CLAUDE.md"},
	}, base)

	// make main.go appear untracked in a scratch state: delete + recreate
	// with different content while keeping it out of the index is not
	// possible for a tracked file, so simulate via a new untracked file
	// that IS tracked at head: feature.go exists at head but not on main
	if err := os.WriteFile(filepath.Join(local, "feature.go"), []byte("LOCAL OVERRIDE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m.cfg.LocalOverlay = []string{"feature.go"}

	co, err := m.Ensure(context.Background(), mrDetail(head), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = co.Close(context.Background()) }()

	data, err := os.ReadFile(filepath.Join(co.Path, "feature.go"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "LOCAL OVERRIDE") {
		t.Error("tracked file at MR head was shadowed by a local overlay file")
	}
}

func TestEnsureCloneModeSkipsOverlay(t *testing.T) {
	base, head := setupOrigin(t)
	m := newManager(t, config.Checkout{
		Mode: "clone", Transport: "https", CacheDir: t.TempDir(),
		LocalOverlay: config.Default().Checkout.LocalOverlay,
	}, base)
	if _, err := m.Ensure(context.Background(), mrDetail(head), nil); err != nil {
		t.Fatalf("clone mode must not attempt overlay: %v", err)
	}
}
