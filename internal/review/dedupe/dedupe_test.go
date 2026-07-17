package dedupe

import (
	"testing"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review"
)

func intp(n int) *int { return &n }

func TestSimilarText(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want bool
	}{
		{"identical", "possible nil pointer dereference on err", "possible nil pointer dereference on err", true},
		{
			"reworded restatement", "possible nil pointer dereference: err may be nil when this line executes",
			"possible nil pointer dereference: err could be nil when this line executes", true,
		},
		{"unrelated", "possible nil pointer dereference", "missing test coverage for the new flag", false},
		{"empty a", "", "some text", false},
		{"empty both", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SimilarText(tt.a, tt.b); got != tt.want {
				t.Errorf("SimilarText(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestFindingsDropsSameFileLineDuplicates(t *testing.T) {
	findings := []review.Finding{
		{
			ID: "f001", File: "a.go", Line: review.LineRef{NewLine: intp(10)}, Severity: review.SeverityMinor,
			Agent: "bugs", Title: "nil pointer risk", Body: "possible nil pointer dereference: err may be nil when this line executes",
		},
		{
			ID: "f002", File: "a.go", Line: review.LineRef{NewLine: intp(10)}, Severity: review.SeverityMajor,
			Agent: "security", Title: "possible nil deref", Body: "possible nil pointer dereference: err could be nil when this line executes",
		},
		{
			ID: "f003", File: "a.go", Line: review.LineRef{NewLine: intp(42)}, Severity: review.SeverityMinor,
			Agent: "style", Title: "unrelated finding", Body: "missing test coverage for the new flag",
		},
	}
	kept, dropped := Findings(findings)

	if len(kept) != 2 {
		t.Fatalf("kept = %+v, want 2", kept)
	}
	if len(dropped) != 1 || dropped[0].ID != "f001" {
		t.Fatalf("dropped = %+v, want [f001]", dropped)
	}
	// The higher-severity duplicate (f002) survives.
	if kept[0].ID != "f002" {
		t.Errorf("kept[0] = %+v, want f002 (higher severity kept)", kept[0])
	}
}

func TestFindingsKeepsCuratedOverPending(t *testing.T) {
	findings := []review.Finding{
		{
			ID: "f001", File: "a.go", Line: review.LineRef{NewLine: intp(10)}, Severity: review.SeverityMajor,
			State: review.StateAccepted, Title: "nil pointer risk", Body: "possible nil pointer dereference: err may be nil when this line executes",
		},
		{
			ID: "f002", File: "a.go", Line: review.LineRef{NewLine: intp(10)}, Severity: review.SeverityCritical,
			State: review.StatePending, Title: "possible nil deref", Body: "possible nil pointer dereference: err could be nil when this line executes",
		},
	}
	kept, dropped := Findings(findings)

	if len(kept) != 1 || kept[0].ID != "f001" {
		t.Fatalf("kept = %+v, want [f001] (already curated, kept despite lower severity)", kept)
	}
	if len(dropped) != 1 || dropped[0].ID != "f002" {
		t.Fatalf("dropped = %+v, want [f002]", dropped)
	}
}

func TestFindingsDoesNotMergeDifferentFilesOrLines(t *testing.T) {
	findings := []review.Finding{
		{ID: "f001", File: "a.go", Line: review.LineRef{NewLine: intp(10)}, Title: "issue", Body: "possible nil pointer dereference"},
		{ID: "f002", File: "b.go", Line: review.LineRef{NewLine: intp(10)}, Title: "issue", Body: "possible nil pointer dereference"},
		{ID: "f003", File: "a.go", Line: review.LineRef{NewLine: intp(11)}, Title: "issue", Body: "possible nil pointer dereference"},
	}
	kept, dropped := Findings(findings)
	if len(kept) != 3 || len(dropped) != 0 {
		t.Fatalf("kept = %+v, dropped = %+v, want all 3 kept", kept, dropped)
	}
}

func TestFindingsFileLevelFindingsDedupeOnlyAgainstEachOther(t *testing.T) {
	findings := []review.Finding{
		{ID: "f001", File: "", Manual: true, Title: "", Body: "MR-level manual note"},
		{ID: "f002", File: "a.go", Title: "generic file comment", Body: "generic file-level comment about the whole file"},
		{ID: "f003", File: "a.go", Title: "generic file comment", Body: "generic file-level comment about the whole file"},
	}
	kept, dropped := Findings(findings)
	if len(kept) != 2 {
		t.Fatalf("kept = %+v, want 2 (manual note kept, one of the file-level pair kept)", kept)
	}
	if len(dropped) != 1 || dropped[0].ID != "f003" {
		t.Fatalf("dropped = %+v, want [f003]", dropped)
	}
}
