package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileValuesMissingIsEmpty(t *testing.T) {
	values, err := FileValues(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatalf("FileValues: %v", err)
	}
	if len(values) != 0 {
		t.Fatalf("expected empty map, got %v", values)
	}
}

func TestSetAndDeleteValue(t *testing.T) {
	m := map[string]any{}
	SetValue(m, "gitlab.base_url", "https://example.com")
	SetValue(m, "gitlab.per_page", 25)
	if got, _ := mapString(m, "gitlab", "base_url"); got != "https://example.com" {
		t.Fatalf("base_url = %q", got)
	}

	DeleteValue(m, "gitlab.base_url")
	if _, ok := m["gitlab"].(map[string]any)["base_url"]; ok {
		t.Fatalf("base_url should be deleted")
	}
	// per_page still there, so the gitlab map survives.
	if _, ok := m["gitlab"]; !ok {
		t.Fatalf("gitlab section pruned too early")
	}
	DeleteValue(m, "gitlab.per_page")
	if _, ok := m["gitlab"]; ok {
		t.Fatalf("emptied gitlab section should be pruned")
	}
}

func mapString(m map[string]any, path ...string) (string, bool) {
	var cur any = m
	for _, p := range path {
		node, ok := cur.(map[string]any)
		if !ok {
			return "", false
		}
		cur = node[p]
	}
	s, ok := cur.(string)
	return s, ok
}

func TestSaveFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config.yaml")
	values := map[string]any{}
	SetValue(values, "gitlab.base_url", "https://gitlab.example.com")
	SetValue(values, "review.timeout", "5m")
	SetValue(values, "review.exclude", []any{"vendor/**", "dist/**"})

	if err := SaveFile(path, values); err != nil {
		t.Fatalf("SaveFile: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
	}

	// Reading it back through the real loader must yield the values.
	res, err := Load(Options{File: path, LookupEnv: func(string) (string, bool) { return "", false }})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if res.Config.GitLab.BaseURL != "https://gitlab.example.com" {
		t.Fatalf("base_url = %q", res.Config.GitLab.BaseURL)
	}
	if res.Config.Review.Timeout.String() != "5m0s" {
		t.Fatalf("timeout = %s", res.Config.Review.Timeout)
	}
	if strings.Join(res.Config.Review.Exclude, ",") != "vendor/**,dist/**" {
		t.Fatalf("exclude = %v", res.Config.Review.Exclude)
	}
}

func TestSaveFilePreservesUnmanagedKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	seed := "" +
		"gitlab:\n" +
		"  instances:\n" +
		"    - name: work\n" +
		"      base_url: https://gitlab.work.example\n" +
		"review:\n" +
		"  mcp_servers:\n" +
		"    fetch:\n" +
		"      url: https://mcp.example/sse\n"
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}

	values, err := FileValues(path)
	if err != nil {
		t.Fatalf("FileValues: %v", err)
	}
	// Edit a managed scalar, leaving instances / mcp_servers untouched.
	SetValue(values, "gitlab.base_url", "https://gitlab.com")
	if err := SaveFile(path, values); err != nil {
		t.Fatalf("SaveFile: %v", err)
	}

	res, err := Load(Options{File: path, LookupEnv: func(string) (string, bool) { return "", false }})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(res.Config.GitLab.Instances) != 1 || res.Config.GitLab.Instances[0].Name != "work" {
		t.Fatalf("instances not preserved: %+v", res.Config.GitLab.Instances)
	}
	if _, ok := res.Config.Review.MCPServers["fetch"]; !ok {
		t.Fatalf("mcp_servers not preserved: %+v", res.Config.Review.MCPServers)
	}
	if res.Config.GitLab.BaseURL != "https://gitlab.com" {
		t.Fatalf("base_url = %q", res.Config.GitLab.BaseURL)
	}
}

func TestValidateFileValues(t *testing.T) {
	ok := map[string]any{}
	SetValue(ok, "gitlab.base_url", "https://gitlab.com")
	SetValue(ok, "review.provider", "anthropic")
	if err := ValidateFileValues(ok); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	bad := map[string]any{}
	SetValue(bad, "gitlab.base_url", "not-a-url")
	SetValue(bad, "review.provider", "nonsense")
	err := ValidateFileValues(bad)
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "gitlab.base_url") || !strings.Contains(err.Error(), "review.provider") {
		t.Fatalf("error should name the bad keys: %v", err)
	}
}
