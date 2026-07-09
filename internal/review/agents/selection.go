package agents

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
)

// SelectionStore remembers, per project, the last agent selection and the
// per-agent model overrides so pickers reopen with the user's previous
// choices. It is best-effort: read and write errors are swallowed, matching
// how transient UI state is treated elsewhere; a nil store is safe to use.
type SelectionStore struct {
	path string
}

// NewSelectionStore returns a store backed by a JSON file, typically under
// config.DefaultStateDir().
func NewSelectionStore(path string) *SelectionStore { return &SelectionStore{path: path} }

// projectSelection is the per-project record: the checked agent names plus
// any per-agent model overrides (agent name → model ID) and remembered
// run options.
type projectSelection struct {
	Agents  []string          `json:"agents"`
	Models  map[string]string `json:"models,omitempty"`
	Options *RunOptions       `json:"options,omitempty"`
}

// RunOptions are the per-run review overrides remembered for a project.
// Zero/empty fields mean "use the configured default".
type RunOptions struct {
	Concurrency  int      `json:"concurrency,omitempty"`
	Model        string   `json:"model,omitempty"`
	MaxBudgetUSD *float64 `json:"max_budget_usd,omitempty"`
	Instructions string   `json:"instructions,omitempty"`
}

// empty reports whether every field is at its "use the default" value.
func (o *RunOptions) empty() bool {
	return o == nil || (o.Concurrency == 0 && o.Model == "" && o.MaxBudgetUSD == nil && o.Instructions == "")
}

// UnmarshalJSON accepts both the current object form and the legacy form,
// where a project mapped straight to an array of agent names, so old state
// files keep loading without a migration step.
func (p *projectSelection) UnmarshalJSON(data []byte) error {
	if trimmed := bytes.TrimSpace(data); len(trimmed) > 0 && trimmed[0] == '[' {
		return json.Unmarshal(data, &p.Agents)
	}
	type alias projectSelection
	return json.Unmarshal(data, (*alias)(p))
}

func (s *SelectionStore) read() map[string]projectSelection {
	out := map[string]projectSelection{}
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

func (s *SelectionStore) write(all map[string]projectSelection) {
	raw, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return
	}
	_ = os.WriteFile(s.path, raw, 0o600)
}

// Load returns the remembered agent selection for a project, or nil if none.
func (s *SelectionStore) Load(project string) []string {
	return s.read()[project].Agents
}

// LoadModels returns the remembered per-agent model overrides for a project
// (agent name → model ID), or nil if none.
func (s *SelectionStore) LoadModels(project string) map[string]string {
	return s.read()[project].Models
}

// Save remembers the agent selection for a project, preserving any stored
// model overrides.
func (s *SelectionStore) Save(project string, names []string) {
	if s == nil || s.path == "" || project == "" {
		return
	}
	all := s.read()
	sel := all[project]
	sel.Agents = names
	all[project] = sel
	s.write(all)
}

// LoadOptions returns the remembered run options for a project, or nil if
// none.
func (s *SelectionStore) LoadOptions(project string) *RunOptions {
	return s.read()[project].Options
}

// SaveOptions remembers the run options for a project, preserving the stored
// agent selection and model overrides. All-default options clear the record
// so cleared fields revert to config.
func (s *SelectionStore) SaveOptions(project string, opts *RunOptions) {
	if s == nil || s.path == "" || project == "" {
		return
	}
	all := s.read()
	sel := all[project]
	if opts.empty() {
		sel.Options = nil
	} else {
		copied := *opts
		sel.Options = &copied
	}
	all[project] = sel
	s.write(all)
}

// SaveModels remembers the per-agent model overrides for a project,
// preserving the stored agent selection. Entries with an empty model are
// dropped so a cleared override is not persisted.
func (s *SelectionStore) SaveModels(project string, models map[string]string) {
	if s == nil || s.path == "" || project == "" {
		return
	}
	cleaned := map[string]string{}
	for name, m := range models {
		if m != "" {
			cleaned[name] = m
		}
	}
	all := s.read()
	sel := all[project]
	if len(cleaned) == 0 {
		sel.Models = nil
	} else {
		sel.Models = cleaned
	}
	all[project] = sel
	s.write(all)
}
