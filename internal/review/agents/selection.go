package agents

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// SelectionStore remembers the last agent selection per project so pickers
// reopen with the user's previous choice. It is best-effort: read and write
// errors are swallowed, matching how transient UI state is treated
// elsewhere; a nil store is safe to use.
type SelectionStore struct {
	path string
}

// NewSelectionStore returns a store backed by a JSON file, typically under
// config.DefaultStateDir().
func NewSelectionStore(path string) *SelectionStore { return &SelectionStore{path: path} }

func (s *SelectionStore) read() map[string][]string {
	out := map[string][]string{}
	if s == nil || s.path == "" {
		return out
	}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return out
	}
	_ = json.Unmarshal(raw, &out)
	return out
}

// Load returns the remembered selection for a project, or nil if none.
func (s *SelectionStore) Load(project string) []string {
	return s.read()[project]
}

// Save remembers the selection for a project.
func (s *SelectionStore) Save(project string, names []string) {
	if s == nil || s.path == "" || project == "" {
		return
	}
	all := s.read()
	all[project] = names
	raw, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return
	}
	_ = os.WriteFile(s.path, raw, 0o600)
}
