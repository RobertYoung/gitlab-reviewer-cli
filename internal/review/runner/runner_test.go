package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/config"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx"
)

func testCfg() config.Config {
	cfg := config.Default()
	cfg.Review.MaxDiffKB = 1
	cfg.Review.Exclude = []string{"**/go.sum"}
	return cfg
}

func TestBuildRequestsSkipHandling(t *testing.T) {
	repo := t.TempDir()
	small := "@@ -1 +1 @@\n+ok\n"
	oversize := "@@ -1 +1 @@\n+" + strings.Repeat("x", 2*1024) + "\n"
	diffs := []gitlabx.FileDiff{
		{OldPath: "a.go", NewPath: "a.go", Diff: small},
		{OldPath: "go.sum", NewPath: "go.sum", Diff: small},
		{OldPath: "big.go", NewPath: "big.go", Diff: oversize},
		{OldPath: "huge.sql", NewPath: "huge.sql", TooLarge: true},
	}

	reqs, info, warnings, err := BuildRequests(testCfg(), gitlabx.MRDetail{}, diffs, nil, "", repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 1 {
		t.Fatalf("reqs = %d, want 1", len(reqs))
	}
	req := reqs[0]
	if len(req.Diffs) != 1 || req.Diffs[0].NewPath != "a.go" {
		t.Errorf("inline diffs: %+v", req.Diffs)
	}
	if len(req.Excluded) != 1 || req.Excluded[0] != "go.sum" {
		t.Errorf("excluded = %v", req.Excluded)
	}
	if len(req.Unavailable) != 1 || req.Unavailable[0] != "huge.sql" {
		t.Errorf("unavailable = %v", req.Unavailable)
	}
	if len(req.DiffFiles) != 1 || req.DiffFiles[0].Path != "big.go" {
		t.Fatalf("diff files = %v", req.DiffFiles)
	}

	// The oversized diff must be readable from inside the checkout.
	data, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(req.DiffFiles[0].DiffPath))) //nolint:gosec // test path inside t.TempDir()
	if err != nil {
		t.Fatalf("reading diff file: %v", err)
	}
	if !strings.Contains(string(data), "+++ b/big.go") || !strings.Contains(string(data), oversize) {
		t.Errorf("diff file content missing header or diff:\n%.120s", data)
	}

	// Config exclusions and on-disk diffs are informational; only the
	// GitLab-truncated file warrants a persisted warning.
	if len(info) != 2 {
		t.Errorf("info = %v, want excluded + oversized lines", info)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "GitLab returned no diff") {
		t.Errorf("warnings = %v", warnings)
	}
}

func TestBuildRequestsOnlyOversized(t *testing.T) {
	repo := t.TempDir()
	oversize := "@@ -1 +1 @@\n+" + strings.Repeat("x", 2*1024) + "\n"
	diffs := []gitlabx.FileDiff{{OldPath: "big.go", NewPath: "big.go", Diff: oversize}}

	reqs, _, _, err := BuildRequests(testCfg(), gitlabx.MRDetail{}, diffs, nil, "", repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 1 || len(reqs[0].Diffs) != 0 || len(reqs[0].DiffFiles) != 1 {
		t.Errorf("want one pass with only an on-disk diff, got %+v", reqs)
	}
}

func TestBuildRequestsNothingReviewable(t *testing.T) {
	diffs := []gitlabx.FileDiff{{OldPath: "go.sum", NewPath: "go.sum", Diff: "@@ -1 +1 @@\n+x\n"}}
	if _, _, _, err := BuildRequests(testCfg(), gitlabx.MRDetail{}, diffs, nil, "", t.TempDir()); err == nil {
		t.Error("want an error when every file is excluded")
	}
}

func TestBuildRequestsMultiPassDiffFilesOnce(t *testing.T) {
	repo := t.TempDir()
	half := "@@ -1 +1 @@\n+" + strings.Repeat("x", 600) + "\n"
	oversize := "@@ -1 +1 @@\n+" + strings.Repeat("x", 2*1024) + "\n"
	diffs := []gitlabx.FileDiff{
		{OldPath: "a.go", NewPath: "a.go", Diff: half},
		{OldPath: "b.go", NewPath: "b.go", Diff: half},
		{OldPath: "big.go", NewPath: "big.go", Diff: oversize},
	}

	reqs, _, warnings, err := BuildRequests(testCfg(), gitlabx.MRDetail{}, diffs, nil, "", repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 2 {
		t.Fatalf("reqs = %d, want 2 passes", len(reqs))
	}
	if len(reqs[0].DiffFiles) != 1 || len(reqs[1].DiffFiles) != 0 {
		t.Errorf("on-disk diffs must join the first pass only: %v / %v", reqs[0].DiffFiles, reqs[1].DiffFiles)
	}
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "2 passes") {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings = %v, want a multi-pass note", warnings)
	}
}
