package resultstore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review"
)

func intp(n int) *int { return &n }

func record(iid int64, ref string, started time.Time) Record {
	return Record{
		IID:     iid,
		Ref:     ref,
		Title:   "Fix parser",
		Started: started,
		Summary: "One bug.",
		LogPath: "/state/reviews/review-42-100.log",
		CostUSD: 0.12,
		Findings: []review.Finding{
			{
				ID: "f001", File: "a.go", Line: review.LineRef{NewLine: intp(2)},
				Severity: review.SeverityMajor, Category: "bug",
				Title: "Bug", Body: "This is wrong.", State: review.StateAccepted,
			},
			{ID: "m001", Body: "manual note", State: review.StatePending, Manual: true},
		},
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	store := NewStore(t.TempDir())
	rec := record(42, "group/app!42", time.Unix(100, 0))
	if err := store.Save(rec); err != nil {
		t.Fatal(err)
	}

	entries, err := store.List("")
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries = %+v, err = %v", entries, err)
	}
	got, err := store.Load(entries[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Ref != rec.Ref || got.Title != rec.Title || got.Summary != rec.Summary ||
		got.LogPath != rec.LogPath || got.CostUSD != rec.CostUSD || !got.Started.Equal(rec.Started) {
		t.Errorf("metadata round-trip: %+v", got)
	}
	if len(got.Findings) != 2 {
		t.Fatalf("findings = %+v", got.Findings)
	}
	f := got.Findings[0]
	if f.State != review.StateAccepted || f.Severity != review.SeverityMajor ||
		f.Line.NewLine == nil || *f.Line.NewLine != 2 {
		t.Errorf("finding round-trip: %+v", f)
	}
	if !got.Findings[1].Manual || got.Findings[1].State != review.StatePending {
		t.Errorf("manual finding round-trip: %+v", got.Findings[1])
	}

	// states are stored as words, not iota values
	data, _ := os.ReadFile(entries[0].Path)
	if !strings.Contains(string(data), `"state": "accepted"`) {
		t.Errorf("state not stored as text:\n%s", data)
	}
}

func TestSaveOverwritesSameRun(t *testing.T) {
	store := NewStore(t.TempDir())
	rec := record(42, "group/app!42", time.Unix(100, 0))
	if err := store.Save(rec); err != nil {
		t.Fatal(err)
	}
	rec.Findings[1].State = review.StateAccepted
	if err := store.Save(rec); err != nil {
		t.Fatal(err)
	}

	entries, err := store.List("group/app!42")
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries = %+v, err = %v", entries, err)
	}
	if entries[0].Findings != 2 || entries[0].Accepted != 2 {
		t.Errorf("counts = %+v", entries[0])
	}
}

func TestListFiltersAndSorts(t *testing.T) {
	store := NewStore(t.TempDir())
	for _, r := range []Record{
		record(1, "a/b!1", time.Unix(100, 0)),
		record(1, "a/b!1", time.Unix(200, 0)),
		record(2, "c/d!2", time.Unix(300, 0)),
	} {
		if err := store.Save(r); err != nil {
			t.Fatal(err)
		}
	}
	// junk files are skipped
	if err := os.WriteFile(filepath.Join(store.dir, "review-9-9.json"), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.dir, "notes.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	entries, err := store.List("a/b!1")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %+v", entries)
	}
	if !entries[0].Started.After(entries[1].Started) {
		t.Errorf("not newest-first: %+v", entries)
	}

	all, err := store.List("")
	if err != nil || len(all) != 3 {
		t.Fatalf("all = %+v, err = %v", all, err)
	}
}

func TestListMissingDir(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "does-not-exist"))
	entries, err := store.List("")
	if err != nil || entries != nil {
		t.Fatalf("entries=%v err=%v", entries, err)
	}
}

func TestLatestBlocking(t *testing.T) {
	store := NewStore(t.TempDir())

	// Older review with a critical finding, newer one with a rejected major
	// and a pending minor: only the newest record counts, and rejected or
	// too-weak findings never block.
	older := record(1, "a/b!1", time.Unix(100, 0))
	older.Findings[0].Severity = review.SeverityCritical
	newer := record(1, "a/b!1", time.Unix(200, 0))
	newer.Findings = []review.Finding{
		{ID: "f1", Severity: review.SeverityMajor, State: review.StateRejected, Body: "b"},
		{ID: "f2", Severity: review.SeverityMinor, State: review.StatePending, Body: "b"},
		{ID: "m1", Manual: true, State: review.StatePending, Body: "manual"},
	}
	for _, r := range []Record{older, newer} {
		if err := store.Save(r); err != nil {
			t.Fatal(err)
		}
	}

	if n, err := store.LatestBlocking("a/b!1", review.SeverityMajor); n != 0 || err != nil {
		t.Errorf("major gate: n=%d err=%v; want 0 (rejected finding must not block)", n, err)
	}
	if n, err := store.LatestBlocking("a/b!1", review.SeverityMinor); n != 1 || err != nil {
		t.Errorf("minor gate: n=%d err=%v; want 1", n, err)
	}
	if n, err := store.LatestBlocking("x/y!9", review.SeverityInfo); n != 0 || err != nil {
		t.Errorf("unreviewed MR: n=%d err=%v; want 0", n, err)
	}
}

func TestNilStoreIsNoOp(t *testing.T) {
	var store *Store
	if err := store.Save(record(1, "a/b!1", time.Unix(100, 0))); err != nil {
		t.Fatal(err)
	}
	if entries, err := store.List(""); entries != nil || err != nil {
		t.Errorf("entries=%v err=%v", entries, err)
	}
}
