package resultstore

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/config"
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

func TestLatestReturnsNewestForRef(t *testing.T) {
	store := NewStore(t.TempDir())
	older := record(1, "a/b!1", time.Unix(100, 0))
	newer := record(1, "a/b!1", time.Unix(200, 0))
	newer.BaseSHA, newer.HeadSHA = "base1", "head2"
	for _, r := range []Record{older, newer, record(2, "c/d!2", time.Unix(300, 0))} {
		if err := store.Save(r); err != nil {
			t.Fatal(err)
		}
	}

	got, err := store.Latest("a/b!1")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || !got.Started.Equal(newer.Started) {
		t.Fatalf("latest = %+v", got)
	}
	if got.BaseSHA != "base1" || got.HeadSHA != "head2" {
		t.Errorf("sha round-trip: %+v", got)
	}

	if none, err := store.Latest("x/y!9"); err != nil || none != nil {
		t.Errorf("latest for unknown ref: %v, err=%v", none, err)
	}
	var nilStore *Store
	if none, err := nilStore.Latest("a/b!1"); err != nil || none != nil {
		t.Errorf("nil store latest: %v, err=%v", none, err)
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

func TestCountsStage(t *testing.T) {
	for want, c := range map[string]Counts{
		"clean":     {},
		"triage":    {Findings: 3, Pending: 1, Accepted: 1},
		"publish":   {Findings: 2, Accepted: 2, Published: 1},
		"published": {Findings: 2, Accepted: 2, Published: 2},
		"triaged":   {Findings: 2},
	} {
		if got := c.Stage(); got != want {
			t.Errorf("%+v: stage %q, want %q", c, got, want)
		}
	}
}

func TestCountFindings(t *testing.T) {
	got := CountFindings([]review.Finding{
		{State: review.StateAccepted},
		{State: review.StatePublished},
		{State: review.StateFellBack},
		{State: review.StatePending},
		{State: review.StateRejected},
		{State: review.StateBelowThreshold},
	})
	want := Counts{Findings: 6, Accepted: 3, Rejected: 1, Pending: 1, Published: 2}
	if got != want {
		t.Errorf("counts = %+v, want %+v", got, want)
	}
}

func TestResolveRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	for _, name := range []string{
		"", "../review-1-1.json", "review-1-1.json/../../x.json", "/etc/review-1-1.json",
		"sub/review-1-1.json", "review-1-1.log", "notes.json", "..",
	} {
		if p, err := store.Resolve(name); err == nil {
			t.Errorf("Resolve(%q) = %q; want error", name, p)
		}
	}
	got, err := store.Resolve("review-7-100.json")
	if err != nil || got != filepath.Join(dir, "review-7-100.json") {
		t.Errorf("Resolve = %q, %v", got, err)
	}
}

func TestTriage(t *testing.T) {
	fresh := func() Record {
		return Record{Findings: []review.Finding{
			{ID: "a", Body: "one", State: review.StatePending},
			{ID: "b", Body: "two", State: review.StatePending},
			{ID: "c", Body: "three", State: review.StateRejected},
		}}
	}
	states := func(r Record) string {
		var parts []string
		for _, f := range r.Findings {
			parts = append(parts, f.ID+"="+f.State.String())
		}
		return strings.Join(parts, ",")
	}

	tests := []struct {
		action, id, body string
		want             string
		wantErr          bool
	}{
		{"accept", "a", "", "a=accepted,b=pending,c=rejected", false},
		{"reject", "b", "", "a=pending,b=rejected,c=rejected", false},
		{"accept", "c", "", "a=pending,b=pending,c=accepted", false},
		{"accept-all", "", "", "a=accepted,b=accepted,c=rejected", false},
		{"accept", "zzz", "", "a=pending,b=pending,c=rejected", true},
		{"edit", "a", "   ", "a=pending,b=pending,c=rejected", true},
		{"explode", "a", "", "a=pending,b=pending,c=rejected", true},
	}
	for _, tt := range tests {
		t.Run(tt.action+" "+tt.id, func(t *testing.T) {
			rec := fresh()
			err := rec.Triage(tt.action, tt.id, tt.body)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got := states(rec); got != tt.want {
				t.Errorf("states = %s, want %s", got, tt.want)
			}
		})
	}

	rec := fresh()
	if err := rec.Triage("edit", "b", "  new body \n"); err != nil {
		t.Fatal(err)
	}
	if rec.Findings[1].Body != "new body" || rec.Findings[1].State != review.StatePending {
		t.Errorf("edited finding = %+v", rec.Findings[1])
	}
}

func TestCheckApproval(t *testing.T) {
	store := NewStore(t.TempDir())
	rec := record(1, "a/b!1", time.Unix(100, 0))
	rec.Findings = []review.Finding{{ID: "f1", Severity: review.SeverityMajor, State: review.StatePending, Body: "b"}}
	if err := store.Save(rec); err != nil {
		t.Fatal(err)
	}

	err := store.CheckApproval(config.Gate{MinSeverity: "major", Approvals: "block"}, "a/b!1")
	var blocked *ApprovalBlockedError
	if !errors.As(err, &blocked) || blocked.Blocking != 1 || blocked.MinSeverity != "major" {
		t.Errorf("block mode: err = %v", err)
	}
	for _, gate := range []config.Gate{
		{MinSeverity: "major", Approvals: "warn"},
		{MinSeverity: "critical", Approvals: "block"},
		{Approvals: "block"},
	} {
		if err := store.CheckApproval(gate, "a/b!1"); err != nil {
			t.Errorf("gate %+v: err = %v; want nil", gate, err)
		}
	}
	if err := store.CheckApproval(config.Gate{MinSeverity: "major", Approvals: "block"}, "x/y!2"); err != nil {
		t.Errorf("unreviewed MR: err = %v", err)
	}
}
