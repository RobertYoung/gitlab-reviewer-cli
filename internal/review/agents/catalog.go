package agents

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// Catalog is the merged, ordered view of available agents: builtins first,
// then plugin and user agents, then project extras. An agent carrying a
// known name replaces it in place, so shadowing never reorders pickers.
type Catalog struct {
	agents   []Agent
	warnings []string
}

// NewCatalog merges the builtins with the user-scope layers in increasing
// precedence: plugin agents from pluginDirs (accepted Claude Code plugins,
// resolved by PluginAgentDirs), then user agents from userDirs (typically
// config.UserAgentDirs()). Within a layer a definition in a later directory
// shadows a same-named one in an earlier directory. Empty dirs are skipped;
// load problems become Warnings, not errors.
func NewCatalog(pluginDirs, userDirs []string) *Catalog {
	c := &Catalog{agents: Builtins()}
	for _, dir := range pluginDirs {
		if dir == "" {
			continue
		}
		plug, warns := loadTree(dir, SourcePlugin)
		c.merge(plug)
		c.warnings = append(c.warnings, warns...)
	}
	for _, dir := range userDirs {
		if dir == "" {
			continue
		}
		user, warns := loadDir(dir, SourceUser)
		c.merge(user)
		c.warnings = append(c.warnings, warns...)
	}
	return c
}

// WithWarnings returns a copy of the catalog with extra warnings appended —
// how plugin discovery problems (PluginAgentDirs) reach the pickers and run
// logs alongside definition-load warnings.
func (c *Catalog) WithWarnings(msgs ...string) *Catalog {
	if len(msgs) == 0 {
		return c
	}
	return &Catalog{
		agents:   append([]Agent(nil), c.agents...),
		warnings: append(append([]string(nil), c.warnings...), msgs...),
	}
}

// WithProject returns a copy of the catalog extended with agents shipped in
// the repo checkout under the ProjectAgentDirs. Project agents shadow user
// and builtin agents of the same name; across the project directories the
// merge order makes ProjectAgentsDir shadow ClaudeAgentsDir.
func (c *Catalog) WithProject(repoPath string) *Catalog {
	out := &Catalog{
		agents:   append([]Agent(nil), c.agents...),
		warnings: append([]string(nil), c.warnings...),
	}
	for _, dir := range ProjectAgentDirs {
		project, warns := loadDir(filepath.Join(repoPath, filepath.FromSlash(dir)), SourceProject)
		out.merge(project)
		out.warnings = append(out.warnings, warns...)
	}
	return out
}

// WithProjectFiles returns a copy of the catalog extended with agent
// definitions fetched from the repository (e.g. over the GitLab API) — the
// remote counterpart of WithProject, with the same shadowing.
func (c *Catalog) WithProjectFiles(files []File) *Catalog {
	out := &Catalog{
		agents:   append([]Agent(nil), c.agents...),
		warnings: append([]string(nil), c.warnings...),
	}
	project, warns := LoadProjectFiles(files)
	out.merge(project)
	out.warnings = append(out.warnings, warns...)
	return out
}

// merge appends extras, replacing same-named agents in place.
func (c *Catalog) merge(extra []Agent) {
	for _, a := range extra {
		replaced := false
		for i := range c.agents {
			if c.agents[i].Name == a.Name {
				c.agents[i] = a
				replaced = true
				break
			}
		}
		if !replaced {
			c.agents = append(c.agents, a)
		}
	}
}

// All returns the agents in stable display order.
func (c *Catalog) All() []Agent { return append([]Agent(nil), c.agents...) }

// Warnings reports definition files that were skipped and why.
func (c *Catalog) Warnings() []string { return append([]string(nil), c.warnings...) }

// Names returns the agent names in display order.
func (c *Catalog) Names() []string {
	out := make([]string, len(c.agents))
	for i, a := range c.agents {
		out[i] = a.Name
	}
	return out
}

// Resolve maps a selection of names to agents, in catalog order. Unknown
// names error so a typo in config or --agents fails loudly instead of
// silently reviewing less.
func (c *Catalog) Resolve(names []string) ([]Agent, error) {
	want := map[string]bool{}
	for _, n := range names {
		want[strings.TrimSpace(n)] = true
	}
	var out []Agent
	for _, a := range c.agents {
		if want[a.Name] {
			out = append(out, a)
			delete(want, a.Name)
		}
	}
	if len(want) > 0 {
		missing := make([]string, 0, len(want))
		for n := range want {
			missing = append(missing, n)
		}
		slices.Sort(missing)
		return nil, fmt.Errorf("unknown agent(s) %s (available: %s)", strings.Join(missing, ", "), strings.Join(c.Names(), ", "))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no agents selected")
	}
	return out, nil
}
