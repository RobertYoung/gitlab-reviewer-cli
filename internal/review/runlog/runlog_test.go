package runlog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStartAppendFinish(t *testing.T) {
	store := NewStore(t.TempDir())

	l := store.Start(42, "group/app!42", "Fix parser")
	if l == nil || l.Path() == "" {
		t.Fatal("log not created")
	}
	l.Append("cloning…")
	l.Append("Read a.go")
	l.Finish("completed with 1 finding(s)")
	l.Finish("must be idempotent") // second finish is a no-op

	data, err := os.ReadFile(l.Path())
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	for _, want := range []string{
		"review of group/app!42 — Fix parser",
		"started ",
		"cloning…",
		"Read a.go",
		"completed with 1 finding(s) after ",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("log missing %q:\n%s", want, content)
		}
	}
	if strings.Contains(content, "idempotent") {
		t.Error("write after Finish must be dropped")
	}
}

func TestListFiltersAndSorts(t *testing.T) {
	dir := t.TempDir()
	write := func(name, header string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(header+"\nstarted x\n\n  1s  hi\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("review-1-100.log", "review of a/b!1 — first run")
	write("review-1-200.log", "review of a/b!1 — second run")
	write("review-2-300.log", "review of c/d!2 — other MR")
	write("notes.txt", "review of a/b!1 — not a log file")

	store := NewStore(dir)
	entries, err := store.List("a/b!1")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %+v", entries)
	}
	if entries[0].Title != "second run" || entries[1].Title != "first run" {
		t.Errorf("not newest-first: %+v", entries)
	}
	if entries[0].Ref != "a/b!1" {
		t.Errorf("ref = %q", entries[0].Ref)
	}

	all, err := store.List("")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("all entries = %+v", all)
	}
}

func TestListMissingDir(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "does-not-exist"))
	entries, err := store.List("")
	if err != nil || entries != nil {
		t.Fatalf("entries=%v err=%v", entries, err)
	}
}

func TestNilStoreAndLogAreNoOps(t *testing.T) {
	var store *Store
	l := store.Start(1, "a/b!1", "t")
	if l != nil {
		t.Fatal("nil store must hand out a nil log")
	}
	l.Append("x") // must not panic
	l.Finish("y")
	if l.Path() != "" {
		t.Errorf("path = %q", l.Path())
	}
	if entries, err := store.List(""); entries != nil || err != nil {
		t.Errorf("entries=%v err=%v", entries, err)
	}
}
