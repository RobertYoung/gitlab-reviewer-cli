package cli

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review"
)

func line(n int) review.LineRef { return review.LineRef{NewLine: &n} }

func TestReviewListAndShowJSON(t *testing.T) {
	g := newFakeGitLab(t)
	_, older := seedRecord(t, time.Unix(100, 0), review.Finding{ID: "f1", Body: "old", State: review.StateRejected})
	_, newer := seedRecord(t, time.Unix(200, 0),
		review.Finding{ID: "f1", File: "feature.go", Line: line(1), Severity: review.SeverityMajor, Title: "T", Body: "new", State: review.StatePending},
	)

	out, err := g.run(t, "review", "list", "group/app!7", "--output", "json")
	if err != nil {
		t.Fatal(err)
	}
	var list struct {
		Ref     string `json:"ref"`
		Reviews []struct {
			Record string `json:"record"`
			Path   string `json:"path"`
			Stage  string `json:"stage"`
			Counts struct {
				Findings int `json:"findings"`
				Rejected int `json:"rejected"`
			} `json:"counts"`
		} `json:"reviews"`
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		t.Fatalf("decoding: %v\n%s", err, out)
	}
	if list.Ref != "group/app!7" || len(list.Reviews) != 2 || list.Reviews[0].Record != newer || list.Reviews[1].Record != older {
		t.Fatalf("list = %+v", list)
	}
	if list.Reviews[0].Stage != "triage" || list.Reviews[1].Counts.Rejected != 1 || list.Reviews[0].Path == "" {
		t.Errorf("entries = %+v", list.Reviews)
	}

	for _, tc := range []struct{ name, record, wantBody string }{
		{"latest by default", "", "new"},
		{"named record", older, "old"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := []string{"review", "show", "group/app!7", "--output", "json"}
			if tc.record != "" {
				args = append(args, "--record", tc.record)
			}
			out, err := g.run(t, args...)
			if err != nil {
				t.Fatal(err)
			}
			var show struct {
				Record   string `json:"record"`
				Ref      string `json:"ref"`
				HeadSHA  string `json:"head_sha"`
				Stage    string `json:"stage"`
				Findings []struct {
					ID    string `json:"id"`
					Body  string `json:"body"`
					State string `json:"state"`
				} `json:"findings"`
			}
			if err := json.Unmarshal([]byte(out), &show); err != nil {
				t.Fatalf("decoding: %v\n%s", err, out)
			}
			if show.Ref != "group/app!7" || show.HeadSHA != fakeHead || len(show.Findings) != 1 || show.Findings[0].Body != tc.wantBody || show.Findings[0].ID != "f1" {
				t.Errorf("show = %+v", show)
			}
		})
	}
}

func TestReviewRecordReferenceRejected(t *testing.T) {
	g := newFakeGitLab(t)
	seedRecord(t, time.Unix(100, 0), review.Finding{ID: "f1", Body: "b", State: review.StatePending})

	for _, name := range []string{"../../config.json", "review-7-100.json/../../x.json", "/etc/passwd", "secrets.json"} {
		if _, err := g.run(t, "review", "show", "group/app!7", "--record", name); err == nil || !strings.Contains(err.Error(), "invalid file reference") {
			t.Errorf("--record %q: err = %v", name, err)
		}
	}
	if _, err := g.run(t, "review", "show", "group/app!7", "--record", "review-7-999.json"); err == nil || !strings.Contains(err.Error(), "no stored review") {
		t.Errorf("missing record: err = %v", err)
	}
	if _, err := g.run(t, "review", "show", "other/app!7", "--record", "review-7-100.json"); err == nil || !strings.Contains(err.Error(), "belongs to group/app!7") {
		t.Errorf("record of another MR: err = %v", err)
	}
	if _, err := g.run(t, "review", "show", "group/app!8"); err == nil || !strings.Contains(err.Error(), "no stored review for group/app!8") {
		t.Errorf("unreviewed MR: err = %v", err)
	}
}

func TestReviewTriage(t *testing.T) {
	g := newFakeGitLab(t)
	rec, _ := seedRecord(t, time.Unix(100, 0),
		review.Finding{ID: "a", Body: "one", State: review.StatePending},
		review.Finding{ID: "b", Body: "two", State: review.StatePending},
		review.Finding{ID: "c", Body: "three", State: review.StatePending},
	)
	states := func() string {
		var parts []string
		for _, f := range loadSeeded(t, rec).Findings {
			parts = append(parts, f.ID+"="+f.State.String())
		}
		return strings.Join(parts, ",")
	}

	if _, err := g.run(t, "review", "reject", "group/app!7", "b"); err != nil {
		t.Fatal(err)
	}
	if got := states(); got != "a=pending,b=rejected,c=pending" {
		t.Errorf("after reject: %s", got)
	}

	if _, err := g.run(t, "review", "accept", "group/app!7", "a", "nope"); err == nil || !strings.Contains(err.Error(), `"nope"`) {
		t.Errorf("unknown id: err = %v", err)
	}
	if got := states(); got != "a=pending,b=rejected,c=pending" {
		t.Errorf("a failed triage must not save: %s", got)
	}

	out, err := g.run(t, "review", "accept-all", "group/app!7", "--output", "json")
	if err != nil {
		t.Fatal(err)
	}
	if got := states(); got != "a=accepted,b=rejected,c=accepted" {
		t.Errorf("after accept-all: %s", got)
	}
	var resp struct {
		Counts struct {
			Accepted int `json:"accepted"`
			Rejected int `json:"rejected"`
		} `json:"counts"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil || resp.Counts.Accepted != 2 || resp.Counts.Rejected != 1 {
		t.Errorf("accept-all output: %+v, %v\n%s", resp, err, out)
	}

	if _, err := g.run(t, "review", "edit", "group/app!7", "c", "--body", "  rewritten  "); err != nil {
		t.Fatal(err)
	}
	if body := loadSeeded(t, rec).Findings[2].Body; body != "rewritten" {
		t.Errorf("edited body = %q", body)
	}
	if _, err := g.run(t, "review", "edit", "group/app!7", "c"); err == nil || !strings.Contains(err.Error(), "body") {
		t.Errorf("edit without --body: err = %v", err)
	}
}

func TestReviewPublishOnlyAccepted(t *testing.T) {
	g := newFakeGitLab(t)
	rec, name := seedRecord(t, time.Unix(100, 0),
		review.Finding{ID: "acc", File: "feature.go", Line: line(1), Severity: review.SeverityMajor, Title: "Accepted", Body: "post me", State: review.StateAccepted},
		review.Finding{ID: "pen", File: "feature.go", Line: line(2), Severity: review.SeverityMajor, Title: "Pending", Body: "not yet", State: review.StatePending},
		review.Finding{ID: "rej", File: "feature.go", Line: line(2), Severity: review.SeverityMajor, Title: "Rejected", Body: "never", State: review.StateRejected},
		review.Finding{ID: "pub", File: "feature.go", Line: line(1), Severity: review.SeverityMajor, Title: "Done", Body: "already", State: review.StatePublished},
	)

	out, err := g.run(t, "review", "publish", "group/app!7", "--mode", "immediate", "--output", "json")
	if err != nil {
		t.Fatal(err)
	}
	var got publishOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decoding: %v\n%s", err, out)
	}
	if got.Record != name || got.Mode != "immediate" || got.Inline != 1 || got.Failed != 0 || got.DraftPending {
		t.Errorf("output = %+v", got)
	}
	if len(got.Results) != 1 || got.Results[0].ID != "acc" || got.Results[0].State != review.StatePublished {
		t.Errorf("results = %+v", got.Results)
	}
	if w := g.recorded(); len(w) != 1 || !strings.HasSuffix(w[0], "/merge_requests/7/discussions") {
		t.Errorf("writes = %v; want one discussion", w)
	}
	if body, _ := g.bodies[0]["body"].(string); !strings.Contains(body, "post me") {
		t.Errorf("posted body = %q", body)
	}

	var parts []string
	for _, f := range loadSeeded(t, rec).Findings {
		parts = append(parts, f.ID+"="+f.State.String())
	}
	if s := strings.Join(parts, ","); s != "acc=published,pen=pending,rej=rejected,pub=published" {
		t.Errorf("stored states = %s", s)
	}
}

func TestReviewPublishDraftThenSubmit(t *testing.T) {
	g := newFakeGitLab(t)
	seedRecord(t, time.Unix(100, 0),
		review.Finding{ID: "acc", File: "feature.go", Line: line(1), Severity: review.SeverityMajor, Title: "Accepted", Body: "post me", State: review.StateAccepted},
	)

	out, err := g.run(t, "review", "publish", "group/app!7", "--publish-mode", "draft")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "review submit group/app!7") {
		t.Errorf("text output should point at submit:\n%s", out)
	}
	if _, err := g.run(t, "review", "submit", "group/app!7"); err != nil {
		t.Fatal(err)
	}
	w := g.recorded()
	if len(w) != 2 || !strings.HasSuffix(w[0], "/draft_notes") || !strings.HasSuffix(w[1], "/draft_notes/bulk_publish") {
		t.Errorf("writes = %v", w)
	}
}

func TestReviewPublishFailureExitsNonZero(t *testing.T) {
	g := newFakeGitLab(t)
	rec, _ := seedRecord(t, time.Unix(100, 0),
		review.Finding{ID: "acc", File: "feature.go", Line: line(1), Severity: review.SeverityMajor, Title: "Accepted", Body: "post me", State: review.StateAccepted},
	)
	g.failWrites = true
	out, err := g.run(t, "review", "publish", "group/app!7", "--mode", "immediate", "--fallback-to-note=false", "--output", "json")
	if err == nil || !strings.Contains(err.Error(), "1 finding(s) failed to publish") {
		t.Fatalf("err = %v", err)
	}
	var got publishOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil || got.Failed != 1 || len(got.Results) != 1 || got.Results[0].Error == "" {
		t.Errorf("output = %+v, %v\n%s", got, err, out)
	}
	if state := loadSeeded(t, rec).Findings[0].State; state != review.StatePending {
		t.Errorf("state = %v; want pending after a failed post, as the GUI records it", state)
	}
}
