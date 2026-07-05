package webui

// Dev tool, skipped unless PREVIEW_DIR is set: renders the GUI pages with
// realistic fake data and dumps them (plus assets) as static HTML, so the
// docs/screenshots images can be regenerated with a headless browser after
// styling changes:
//
//	PREVIEW_DIR=/tmp/preview go test ./internal/webui -run TestDumpPreviewPages
//	chrome --headless --hide-scrollbars --force-device-scale-factor=2 \
//	  --window-size=1360,850 --screenshot=docs/screenshots/gui-diff.png \
//	  file:///tmp/preview/diff.html

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review"
)

const previewHandlerDiff = `@@ -10,9 +10,11 @@ func NewHandler(svc *Service) Handler {
 func (h Handler) Create(w http.ResponseWriter, r *http.Request) {
 	var req CreateRequest
-	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
+	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
 		http.Error(w, "bad request", http.StatusBadRequest)
+		return
 	}
-	intent, err := h.svc.CreateIntent(r.Context(), req)
+	intent, err := h.svc.CreateIntent(r.Context(), req.Normalize())
 	if err != nil {
 		http.Error(w, err.Error(), http.StatusInternalServerError)
 		return
`

const previewStoreDiff = `@@ -3,6 +3,7 @@ package payments
 import (
 	"context"
 	"sync"
+	"time"
 )

 type Store struct {
@@ -21,6 +22,9 @@ func (s *Store) FindByKey(ctx context.Context, key string) (*Intent, bool) {
 	s.mu.Lock()
 	defer s.mu.Unlock()
+	if key == "" {
+		return nil, false
+	}
 	intent, ok := s.byKey[key]
 	return intent, ok
 }
`

func TestDumpPreviewPages(t *testing.T) {
	dir := os.Getenv("PREVIEW_DIR")
	if dir == "" {
		t.Skip("PREVIEW_DIR not set")
	}

	res := &review.Result{
		Summary: "The error handling change looks risky: a decode failure no longer aborts the request, and the new empty-key guard is untested.",
		Findings: []review.Finding{
			{
				ID: "f001", File: "api/handler.go", Line: review.LineRef{NewLine: intp(12)},
				Severity: review.SeverityCritical, Category: review.Category("bug"),
				Title: "Decode errors are swallowed for EOF",
				Body:  "Treating io.EOF as success means an empty body creates a zero-valued intent. Reject empty bodies explicitly.",
			},
			{
				ID: "f002", File: "internal/payments/store.go", Line: review.LineRef{NewLine: intp(25)},
				Severity: review.SeverityMajor, Category: review.Category("bug"),
				Title:      "Empty-key guard hides caller bugs",
				Body:       "Returning (nil, false) for an empty key silently masks a missing Idempotency-Key header upstream.",
				Suggestion: "if key == \"\" {\n\treturn nil, false // consider logging\n}",
			},
			{
				ID: "f003", File: "api/handler.go", Line: review.LineRef{NewLine: intp(16)},
				Severity: review.SeverityInfo, Category: review.Category("style"),
				Title: "Normalize() call is undocumented",
				Body:  "A short comment on what Normalize changes would help reviewers.",
			},
			{
				ID: "f004", Severity: review.SeverityMinor, Category: review.Category("tests"),
				Title: "No test covers the EOF path",
				Body:  "Add a request with an empty body to the handler tests.",
			},
		},
	}

	env := newTestEnv(t, &fakeReviewer{result: res})
	mr := sampleMR()
	mr.Title = "Handle empty request bodies in the payments API"
	mr.Description = "Guards the intent store against empty idempotency keys and tolerates `io.EOF` when decoding.\n\n- new `Normalize()` on requests\n- empty-key guard in `FindByKey`"
	env.svc.mr = &mr
	env.svc.diffs = []gitlabx.FileDiff{
		{OldPath: "api/handler.go", NewPath: "api/handler.go", Diff: previewHandlerDiff},
		{OldPath: "internal/payments/store.go", NewPath: "internal/payments/store.go", Diff: previewStoreDiff},
	}
	when := time.Now().Add(-3 * time.Hour)
	env.svc.discussions = []gitlabx.Discussion{{
		ID: "d1",
		Notes: []gitlabx.Note{{
			ID: 1, Author: "marcus", AuthorName: "Marcus Vale", CreatedAt: when,
			Body:     "Should this be a `*string` so we can tell \"no key sent\" apart from an empty header?",
			Position: &gitlabx.Position{NewPath: "internal/payments/store.go", NewLine: intp(23)},
		}},
	}}

	env.post("/i/default/mr/review", mrForm(url.Values{"agents": {"bug"}}))
	run := waitRun(t, env.srv)
	_, _, out := run.snapshot()
	if out.RecName == "" {
		t.Fatalf("no record: %+v", out)
	}
	// Curate a little so states differ.
	env.post("/i/default/mr/findings/state", mrForm(url.Values{"record": {out.RecName}, "id": {"f001"}, "action": {"accept"}}))
	env.post("/i/default/mr/findings/state", mrForm(url.Values{"record": {out.RecName}, "id": {"f003"}, "action": {"reject"}}))

	if err := os.MkdirAll(filepath.Join(dir, "static"), 0o750); err != nil { //nolint:gosec // dev tool; dir comes from the developer's own env var
		t.Fatal(err)
	}
	pages := map[string]string{
		"mrs.html":        "/i/default/?projects=group%2Fapp",
		"mrdetail.html":   "/i/default/mr?project=group%2Fapp&iid=5",
		"diff.html":       "/i/default/mr/diff?project=group%2Fapp&iid=5",
		"diff-split.html": "/i/default/mr/diff?project=group%2Fapp&iid=5&view=split",
		"findings.html":   "/i/default/mr/findings?project=group%2Fapp&iid=5&record=" + out.RecName,
		"run.html":        "/i/default/run/" + run.ID,
	}
	for name, path := range pages {
		code, body := env.get(path)
		if code != 200 {
			t.Fatalf("%s: %d", path, code)
		}
		body = strings.ReplaceAll(body, `"/static/`, `"static/`)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil { //nolint:gosec // dev tool; dir comes from the developer's own env var
			t.Fatal(err)
		}
	}
	for _, asset := range []string{"app.css", "app.js"} {
		data, err := os.ReadFile(filepath.Join("static", asset)) //nolint:gosec // fixed asset names in the package dir
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "static", asset), data, 0o600); err != nil { //nolint:gosec // dev tool; dir comes from the developer's own env var
			t.Fatal(err)
		}
	}
	css, err := syntaxCSS()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "static", "chroma.css"), css, 0o600); err != nil { //nolint:gosec // dev tool; dir comes from the developer's own env var
		t.Fatal(err)
	}
}
