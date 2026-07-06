package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/providers/structs"
	"github.com/knadh/koanf/v2"
	"go.yaml.in/yaml/v3"
)

// FileValues reads the settings file at path into a nested map — the raw,
// editable view of the file with no defaults, environment, or flags folded
// in. A missing file yields an empty map (not an error): it is the starting
// point a settings editor round-trips into a first save. Keys the editor
// does not manage (gitlab.instances, review.mcp_servers, per-project
// overrides) survive untouched through Set/Delete and back to disk.
func FileValues(path string) (map[string]any, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path is the operator's settings file location, not user input
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, nil
		}
		return nil, err
	}
	values := map[string]any{}
	if err := yaml.Unmarshal(data, &values); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if values == nil {
		values = map[string]any{}
	}
	return values, nil
}

// SetValue sets a delimited key (e.g. "gitlab.base_url") in a nested map,
// creating intermediate maps as needed. An intermediate key that holds a
// non-map value is overwritten with a fresh map.
func SetValue(values map[string]any, key string, value any) {
	parts := strings.Split(key, delim)
	m := values
	for _, p := range parts[:len(parts)-1] {
		next, ok := m[p].(map[string]any)
		if !ok {
			next = map[string]any{}
			m[p] = next
		}
		m = next
	}
	m[parts[len(parts)-1]] = value
}

// DeleteValue removes a delimited key from a nested map, pruning any
// intermediate maps left empty so the written file stays tidy.
func DeleteValue(values map[string]any, key string) {
	parts := strings.Split(key, delim)
	// Track the chain of maps so emptied parents can be pruned bottom-up.
	chain := make([]map[string]any, 0, len(parts))
	m := values
	for _, p := range parts[:len(parts)-1] {
		chain = append(chain, m)
		next, ok := m[p].(map[string]any)
		if !ok {
			return // path does not exist; nothing to delete
		}
		m = next
	}
	chain = append(chain, m)
	delete(m, parts[len(parts)-1])
	for i := len(parts) - 1; i > 0; i-- {
		if len(chain[i]) == 0 {
			delete(chain[i-1], parts[i-1])
		}
	}
}

// ValidateFileValues builds the effective configuration from the built-in
// defaults overlaid with the given file values — the same defaults→file
// layering Load performs, minus environment and flags — and validates it,
// so an editor can reject a bad edit before writing it to disk.
func ValidateFileValues(values map[string]any) error {
	k := koanf.New(delim)
	if err := k.Load(structs.Provider(Default(), "koanf"), nil); err != nil {
		return fmt.Errorf("loading defaults: %w", err)
	}
	// Empty delim: values is already nested, so koanf must not try to
	// unflatten dotted keys that are not there.
	if err := k.Load(confmap.Provider(values, ""), nil); err != nil {
		return fmt.Errorf("loading settings: %w", err)
	}
	var cfg Config
	if err := k.Unmarshal("", &cfg); err != nil {
		return fmt.Errorf("parsing settings: %w", err)
	}
	finalizeAgents(&cfg)
	return cfg.Validate()
}

// SaveFile writes values to path as YAML, atomically: a temporary file in
// the same directory is written then renamed over the target. Parent
// directories are created 0700 and the file 0600 — it may hold a token.
func SaveFile(path string, values map[string]any) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := yaml.Marshal(values)
	if err != nil {
		return fmt.Errorf("encoding settings: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".config-*.yaml.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
