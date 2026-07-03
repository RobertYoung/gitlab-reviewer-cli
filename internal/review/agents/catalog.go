package agents

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// Catalog is the merged, ordered view of available agents: builtins first,
// then user agents, then project extras. A user or project agent with a
// builtin's name replaces it in place, so shadowing never reorders pickers.
type Catalog struct {
	agents   []Agent
	warnings []string
}

// NewCatalog merges the builtins with user agents from userDir (typically
// config.DefaultAgentsDir()). Load problems become Warnings, not errors.
func NewCatalog(userDir string) *Catalog {
	c := &Catalog{agents: Builtins()}
	if userDir != "" {
		user, warns := loadDir(userDir, SourceUser)
		c.merge(user)
		c.warnings = append(c.warnings, warns...)
	}
	return c
}

// WithProject returns a copy of the catalog extended with agents shipped in
// the repo checkout under ProjectAgentsDir. Project agents shadow user and
// builtin agents of the same name.
func (c *Catalog) WithProject(repoPath string) *Catalog {
	out := &Catalog{
		agents:   append([]Agent(nil), c.agents...),
		warnings: append([]string(nil), c.warnings...),
	}
	project, warns := loadDir(filepath.Join(repoPath, filepath.FromSlash(ProjectAgentsDir)), SourceProject)
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
