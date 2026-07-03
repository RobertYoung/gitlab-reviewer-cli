package agents

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review"
)

func TestBuiltinsCoverAllCategories(t *testing.T) {
	b := Builtins()
	if len(b) != len(review.AllCategories) {
		t.Fatalf("got %d builtins, want %d", len(b), len(review.AllCategories))
	}
	for i, c := range review.AllCategories {
		a := b[i]
		if a.Name != string(c) {
			t.Errorf("builtin %d: name %q, want %q", i, a.Name, c)
		}
		if a.Source != SourceBuiltin {
			t.Errorf("builtin %q: source %q", a.Name, a.Source)
		}
		if !slices.Equal(a.Categories, []review.Category{c}) {
			t.Errorf("builtin %q: categories %v", a.Name, a.Categories)
		}
		if a.Prompt == "" || a.Description == "" {
			t.Errorf("builtin %q: empty prompt or description", a.Name)
		}
		if !strings.Contains(a.Prompt, builtinGuidance[c]) {
			t.Errorf("builtin %q: prompt missing guidance text", a.Name)
		}
	}
}

func writeAgent(t *testing.T, dir, name, content string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadFileFrontmatter(t *testing.T) {
	dir := t.TempDir()
	path := writeAgent(t, dir, "migrations.md", `---
name: sql-migrations
description: Reviews schema migrations for lock hazards
categories: [bug, performance]
severity: major
model: opus
---
Look for long-running locks in migrations.
`)
	a, err := loadFile(path, SourceUser)
	if err != nil {
		t.Fatal(err)
	}
	if a.Name != "sql-migrations" || a.Description == "" {
		t.Errorf("unexpected agent: %+v", a)
	}
	if !slices.Equal(a.Categories, []review.Category{"bug", "performance"}) {
		t.Errorf("categories: %v", a.Categories)
	}
	if a.Severity != review.SeverityMajor {
		t.Errorf("severity: %v", a.Severity)
	}
	if a.Model != "opus" {
		t.Errorf("model: %q", a.Model)
	}
	if a.Prompt != "Look for long-running locks in migrations." {
		t.Errorf("prompt: %q", a.Prompt)
	}
}

func TestLoadFileNoFrontmatter(t *testing.T) {
	dir := t.TempDir()
	path := writeAgent(t, dir, "api-compat.md", "Flag breaking API changes.\n")
	a, err := loadFile(path, SourceProject)
	if err != nil {
		t.Fatal(err)
	}
	if a.Name != "api-compat" {
		t.Errorf("name from stem: %q", a.Name)
	}
	if !slices.Equal(a.Categories, review.AllCategories) {
		t.Errorf("default categories: %v", a.Categories)
	}
}

func TestLoadFileErrors(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]string{
		"empty.md":    "---\nname: empty\n---\n",
		"badcat.md":   "---\ncategories: [nonsense]\n---\nprompt\n",
		"badsev.md":   "---\nseverity: fatal\n---\nprompt\n",
		"Bad Name.md": "prompt\n",
		"unterm.md":   "---\nname: unterm\nprompt\n",
	}
	for file, content := range cases {
		path := writeAgent(t, dir, file, content)
		if _, err := loadFile(path, SourceUser); err == nil {
			t.Errorf("%s: expected error", file)
		}
	}
}

func TestLoadDirSkipsInvalidWithWarnings(t *testing.T) {
	dir := t.TempDir()
	writeAgent(t, dir, "good.md", "A valid prompt.\n")
	writeAgent(t, dir, "bad.md", "---\ncategories: [nope]\n---\nprompt\n")
	writeAgent(t, dir, "notes.txt", "ignored\n")
	agents, warns := loadDir(dir, SourceUser)
	if len(agents) != 1 || agents[0].Name != "good" {
		t.Fatalf("agents: %+v", agents)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "bad.md") {
		t.Fatalf("warnings: %v", warns)
	}
}

func TestLoadDirDuplicateName(t *testing.T) {
	dir := t.TempDir()
	writeAgent(t, dir, "a.md", "---\nname: same\n---\nfirst\n")
	writeAgent(t, dir, "b.md", "---\nname: same\n---\nsecond\n")
	agents, warns := loadDir(dir, SourceUser)
	if len(agents) != 1 {
		t.Fatalf("agents: %+v", agents)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "duplicate") {
		t.Fatalf("warnings: %v", warns)
	}
}

func TestLoadDirMissingIsNotError(t *testing.T) {
	agents, warns := loadDir(filepath.Join(t.TempDir(), "nope"), SourceUser)
	if len(agents) != 0 || len(warns) != 0 {
		t.Fatalf("agents=%v warns=%v", agents, warns)
	}
}

func TestCatalogShadowing(t *testing.T) {
	userDir := t.TempDir()
	// User agent shadows the builtin "security" and adds a new one.
	writeAgent(t, userDir, "security.md", "---\ndescription: custom security\ncategories: [security]\n---\nMy security prompt.\n")
	writeAgent(t, userDir, "extra.md", "Extra prompt.\n")

	c := NewCatalog(userDir)
	all := c.All()
	// Order: builtins in place, then user extras.
	if all[1].Name != "security" || all[1].Source != SourceUser {
		t.Errorf("security slot: %+v", all[1])
	}
	if all[len(all)-1].Name != "extra" {
		t.Errorf("last agent: %+v", all[len(all)-1])
	}

	// Project shadows user.
	repo := t.TempDir()
	writeAgent(t, filepath.Join(repo, ".gitlab-reviewer", "agents"), "security.md", "Project security prompt.\n")
	pc := c.WithProject(repo)
	if got := pc.All()[1]; got.Source != SourceProject {
		t.Errorf("project shadowing: %+v", got)
	}
	// Original catalog untouched.
	if got := c.All()[1]; got.Source != SourceUser {
		t.Errorf("catalog mutated by WithProject: %+v", got)
	}
}

func TestCatalogWithProjectClaudeAgents(t *testing.T) {
	repo := t.TempDir()
	writeAgent(t, filepath.Join(repo, ".claude", "agents"), "sql.md", "---\ndescription: from claude\n---\nClaude prompt.\n")
	writeAgent(t, filepath.Join(repo, ".claude", "agents"), "shared.md", "Claude shared prompt.\n")
	writeAgent(t, filepath.Join(repo, ".gitlab-reviewer", "agents"), "shared.md", "Reviewer shared prompt.\n")

	cat := NewCatalog("").WithProject(repo)
	byName := map[string]Agent{}
	for _, a := range cat.All() {
		byName[a.Name] = a
	}
	if a := byName["sql"]; a.Source != SourceProject || a.Prompt != "Claude prompt." {
		t.Errorf("claude agent: %+v", a)
	}
	// On a name collision .gitlab-reviewer/agents wins, without duplicating
	// the entry.
	if a := byName["shared"]; a.Prompt != "Reviewer shared prompt." {
		t.Errorf("shared agent: %+v", a)
	}
	if n := len(cat.All()); n != len(Builtins())+2 {
		t.Errorf("catalog size %d: %v", n, cat.Names())
	}
	if len(cat.Warnings()) != 0 {
		t.Errorf("warnings: %v", cat.Warnings())
	}
}

func TestCatalogResolve(t *testing.T) {
	c := NewCatalog("")
	got, err := c.Resolve([]string{"security", "bug"})
	if err != nil {
		t.Fatal(err)
	}
	// Catalog order, not selection order.
	if len(got) != 2 || got[0].Name != "bug" || got[1].Name != "security" {
		t.Fatalf("resolved: %+v", got)
	}
	if _, err := c.Resolve([]string{"bug", "nonsense"}); err == nil || !strings.Contains(err.Error(), "nonsense") {
		t.Fatalf("expected unknown-agent error, got %v", err)
	}
	if _, err := c.Resolve(nil); err == nil {
		t.Fatal("expected error for empty selection")
	}
}

func TestSelectionStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "agent-selection.json")
	s := NewSelectionStore(path)
	if got := s.Load("group/proj"); got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
	s.Save("group/proj", []string{"bug", "security"})
	s.Save("other/proj", []string{"docs"})
	if got := s.Load("group/proj"); !slices.Equal(got, []string{"bug", "security"}) {
		t.Fatalf("got %v", got)
	}
	var nilStore *SelectionStore
	nilStore.Save("x", []string{"bug"}) // must not panic
	if got := nilStore.Load("x"); got != nil {
		t.Fatalf("nil store load: %v", got)
	}
}

func TestSelectionStoreModels(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "agent-selection.json")
	s := NewSelectionStore(path)

	// Names and models coexist per project without clobbering each other.
	s.Save("group/proj", []string{"bug", "security"})
	s.SaveModels("group/proj", map[string]string{"security": "opus"})
	if got := s.Load("group/proj"); !slices.Equal(got, []string{"bug", "security"}) {
		t.Fatalf("names lost after SaveModels: %v", got)
	}
	if got := s.LoadModels("group/proj"); !maps.Equal(got, map[string]string{"security": "opus"}) {
		t.Fatalf("models: %v", got)
	}

	// Re-saving names preserves the stored models.
	s.Save("group/proj", []string{"bug"})
	if got := s.LoadModels("group/proj"); !maps.Equal(got, map[string]string{"security": "opus"}) {
		t.Fatalf("models lost after Save: %v", got)
	}

	// Empty entries are dropped, and an empty map clears the overrides.
	s.SaveModels("group/proj", map[string]string{"bug": "", "docs": "haiku"})
	if got := s.LoadModels("group/proj"); !maps.Equal(got, map[string]string{"docs": "haiku"}) {
		t.Fatalf("empty model not dropped: %v", got)
	}
	s.SaveModels("group/proj", nil)
	if got := s.LoadModels("group/proj"); got != nil {
		t.Fatalf("models not cleared: %v", got)
	}

	var nilStore *SelectionStore
	nilStore.SaveModels("x", map[string]string{"bug": "opus"}) // must not panic
	if got := nilStore.LoadModels("x"); got != nil {
		t.Fatalf("nil store load models: %v", got)
	}
}

// TestSelectionStoreLegacyFormat reads a state file written before per-agent
// models existed, when a project mapped straight to an array of names.
func TestSelectionStoreLegacyFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sel.json")
	if err := os.WriteFile(path, []byte(`{"group/proj": ["bug", "docs"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewSelectionStore(path)
	if got := s.Load("group/proj"); !slices.Equal(got, []string{"bug", "docs"}) {
		t.Fatalf("legacy names: %v", got)
	}
	if got := s.LoadModels("group/proj"); got != nil {
		t.Fatalf("legacy models: %v", got)
	}
	// Adding a model migrates the record to the new form transparently.
	s.SaveModels("group/proj", map[string]string{"bug": "opus"})
	if got := s.Load("group/proj"); !slices.Equal(got, []string{"bug", "docs"}) {
		t.Fatalf("names after migration: %v", got)
	}
}

func TestLoadProjectFiles(t *testing.T) {
	files := []File{
		{Name: "notes.txt", Content: []byte("not an agent")},
		{Name: "sql.md", Content: []byte("---\nname: sql-migrations\ndescription: Lock hazards\n---\nLook for locks.\n")},
		{Name: "dup.md", Content: []byte("---\nname: sql-migrations\n---\nAnother prompt.\n")},
		{Name: "bad.md", Content: []byte("---\nname: NOT VALID\n---\nPrompt.\n")},
	}
	got, warns := LoadProjectFiles(files)
	if len(got) != 1 {
		t.Fatalf("agents: %+v", got)
	}
	a := got[0]
	if a.Name != "sql-migrations" || a.Source != SourceProject || a.Prompt != "Look for locks." {
		t.Errorf("agent: %+v", a)
	}
	if a.Path != ProjectAgentsDir+"/sql.md" {
		t.Errorf("path: %q", a.Path)
	}
	if len(warns) != 2 {
		t.Fatalf("warnings: %v", warns)
	}
	if !strings.Contains(warns[0], "duplicate name") || !strings.Contains(warns[1], "invalid agent name") {
		t.Errorf("warnings: %v", warns)
	}
}

func TestLoadProjectFilesAcrossDirs(t *testing.T) {
	files := []File{
		{Dir: ClaudeAgentsDir, Name: "shared.md", Content: []byte("Claude prompt.")},
		{Dir: ProjectAgentsDir, Name: "shared.md", Content: []byte("Reviewer prompt.")},
	}
	// Same name in different directories is shadowing, not a duplicate:
	// both parse, and the catalog merge keeps the later (ProjectAgentsDir).
	got, warns := LoadProjectFiles(files)
	if len(got) != 2 || len(warns) != 0 {
		t.Fatalf("agents=%+v warns=%v", got, warns)
	}
	if got[0].Path != ClaudeAgentsDir+"/shared.md" || got[1].Path != ProjectAgentsDir+"/shared.md" {
		t.Errorf("paths: %q, %q", got[0].Path, got[1].Path)
	}

	cat := NewCatalog("").WithProjectFiles(files)
	count := 0
	for _, a := range cat.All() {
		if a.Name == "shared" {
			count++
			if a.Prompt != "Reviewer prompt." {
				t.Errorf("shared agent: %+v", a)
			}
		}
	}
	if count != 1 {
		t.Errorf("%d shared agents in catalog: %v", count, cat.Names())
	}
}

func TestCatalogWithProjectFiles(t *testing.T) {
	base := NewCatalog("")
	cat := base.WithProjectFiles([]File{
		{Name: "security.md", Content: []byte("Shadowed security prompt.")},
		{Name: "extra.md", Content: []byte("Extra prompt.")},
	})

	// Shadowing replaces the builtin in place; the new agent is appended.
	names := cat.Names()
	if got, want := slices.Index(names, "security"), slices.Index(base.Names(), "security"); got != want {
		t.Errorf("security moved: index %d, want %d", got, want)
	}
	if names[len(names)-1] != "extra" {
		t.Errorf("names: %v", names)
	}
	for _, a := range cat.All() {
		if a.Name == "security" && (a.Source != SourceProject || a.Prompt != "Shadowed security prompt.") {
			t.Errorf("security not shadowed: %+v", a)
		}
	}
	// The base catalog is unchanged.
	if len(base.All()) != len(cat.All())-1 {
		t.Errorf("base grew: %v", base.Names())
	}
}

func TestRemoteCacheExtend(t *testing.T) {
	base := NewCatalog("")
	rc := NewRemoteCache()
	calls := 0
	fetch := func() ([]File, error) {
		calls++
		return []File{{Name: "extra.md", Content: []byte("Prompt.")}}, nil
	}

	cat, err := rc.Extend(base, "group/app", "sha1", fetch)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(cat.Names(), "extra") {
		t.Fatalf("names: %v", cat.Names())
	}
	if _, err := rc.Extend(base, "group/app", "sha1", fetch); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("same (project, sha) must hit the cache: %d fetches", calls)
	}
	// A new head SHA fetches fresh.
	if _, err := rc.Extend(base, "group/app", "sha2", fetch); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("new sha must refetch: %d fetches", calls)
	}

	// Failures return the base catalog and are not cached.
	failures := 0
	failing := func() ([]File, error) { failures++; return nil, errors.New("boom") }
	for range 2 {
		cat, err := rc.Extend(base, "group/other", "sha1", failing)
		if err == nil || len(cat.All()) != len(base.All()) {
			t.Fatalf("failed fetch: cat %v, err %v", cat.Names(), err)
		}
	}
	if failures != 2 {
		t.Errorf("failures must not be cached: %d fetches", failures)
	}

	// A nil cache still fetches, just without memoising.
	var nilRC *RemoteCache
	cat, err = nilRC.Extend(base, "group/app", "sha1", fetch)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(cat.Names(), "extra") || calls != 3 {
		t.Errorf("nil cache: names %v, %d fetches", cat.Names(), calls)
	}
}
