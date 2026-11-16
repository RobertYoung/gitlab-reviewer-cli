package agents

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestExampleAgentsLoad(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "examples", "agents")
	files, err := filepath.Glob(filepath.Join(dir, "*.md"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no example agents in %s: %v", dir, err)
	}
	loaded, warnings := loadDir(dir, SourceUser)
	if len(warnings) > 0 {
		t.Fatalf("example agents failed to load: %v", warnings)
	}
	if len(loaded) != len(files) {
		t.Fatalf("loaded %d agents from %d files", len(loaded), len(files))
	}
	builtins := NewCatalog(nil, nil).Names()
	for _, a := range loaded {
		if stem := strings.TrimSuffix(filepath.Base(a.Path), ".md"); a.Name != stem {
			t.Errorf("%s: name %q should match the file name", a.Path, a.Name)
		}
		if a.Description == "" {
			t.Errorf("%s: missing description", a.Path)
		}
		if slices.Contains(builtins, a.Name) {
			t.Errorf("%s: shadows the built-in %q agent", a.Path, a.Name)
		}
	}
}
