package checkout

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeRepo creates a directory shaped like a cached bare clone with
// sizeKB of data and the given FETCH_HEAD mtime.
func fakeRepo(t *testing.T, cacheDir, project string, sizeKB int, lastUse time.Time) {
	t.Helper()
	dir := filepath.Join(cacheDir, "repos", project+".git")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pack"), make([]byte, sizeKB*1024), 0o600); err != nil {
		t.Fatal(err)
	}
	fh := filepath.Join(dir, "FETCH_HEAD")
	if err := os.WriteFile(fh, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(fh, lastUse, lastUse); err != nil {
		t.Fatal(err)
	}
}

func TestListCache(t *testing.T) {
	cache := t.TempDir()
	now := time.Now()
	fakeRepo(t, cache, "gitlab.com/group/small", 10, now.Add(-time.Hour))
	fakeRepo(t, cache, "gitlab.com/group/big", 100, now.Add(-24*time.Hour))

	repos, err := ListCache(cache)
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 2 {
		t.Fatalf("repos = %d", len(repos))
	}
	// largest first
	if repos[0].Project != "gitlab.com/group/big" || repos[0].Size < repos[1].Size {
		t.Errorf("order: %+v", repos)
	}
	if repos[0].LastUse.IsZero() {
		t.Error("last use not read from FETCH_HEAD")
	}
}

func TestListCacheEmpty(t *testing.T) {
	repos, err := ListCache(t.TempDir())
	if err != nil || len(repos) != 0 {
		t.Errorf("repos=%v err=%v", repos, err)
	}
}

func TestCleanCacheEvictsLRU(t *testing.T) {
	cache := t.TempDir()
	now := time.Now()
	fakeRepo(t, cache, "gitlab.com/group/old", 600, now.Add(-48*time.Hour))
	fakeRepo(t, cache, "gitlab.com/group/fresh", 600, now)

	// worktrees always go
	wt := filepath.Join(cache, "worktrees", "x")
	if err := os.MkdirAll(wt, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "f"), make([]byte, 1024), 0o600); err != nil {
		t.Fatal(err)
	}

	// budget of 1 MiB forces one eviction; the older repo must go
	res, err := CleanCache(context.Background(), cache, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cache, "worktrees")); !os.IsNotExist(err) {
		t.Error("worktrees not removed")
	}
	if _, err := os.Stat(filepath.Join(cache, "repos", "gitlab.com/group/old.git")); !os.IsNotExist(err) {
		t.Error("LRU repo should be evicted")
	}
	if _, err := os.Stat(filepath.Join(cache, "repos", "gitlab.com/group/fresh.git")); err != nil {
		t.Error("fresh repo should survive")
	}
	if res.FreedBytes == 0 {
		t.Error("freed bytes not accounted")
	}
}

func TestCleanCacheAll(t *testing.T) {
	cache := t.TempDir()
	fakeRepo(t, cache, "gitlab.com/group/a", 10, time.Now())
	fakeRepo(t, cache, "gitlab.com/group/b", 10, time.Now())
	if _, err := CleanCache(context.Background(), cache, 0, true); err != nil {
		t.Fatal(err)
	}
	repos, _ := ListCache(cache)
	if len(repos) != 0 {
		t.Errorf("repos left: %+v", repos)
	}
}

func TestCleanCacheUnderBudgetNoop(t *testing.T) {
	cache := t.TempDir()
	fakeRepo(t, cache, "gitlab.com/group/a", 10, time.Now())
	res, err := CleanCache(context.Background(), cache, 100, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Removed) != 0 {
		t.Errorf("removed: %v", res.Removed)
	}
}
