package agents

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review"
)

// ProjectAgentsDir is the repo-relative directory teams use to ship agents
// with their project.
const ProjectAgentsDir = ".gitlab-reviewer/agents"

// ClaudeAgentsDir is Claude Code's subagents directory. Teams that keep
// review guidance there ship it to both tools with one set of files; the
// definition format is compatible (YAML frontmatter + prompt body), and
// fields this tool does not know are ignored. The user-scope counterpart
// (~/.claude/agents, config.DefaultClaudeAgentsDir) is loaded via NewCatalog.
const ClaudeAgentsDir = ".claude/agents"

// ProjectAgentDirs are the repo-relative directories scanned for
// project-shipped agents, in increasing precedence: a definition in
// ProjectAgentsDir shadows a same-named one in ClaudeAgentsDir.
var ProjectAgentDirs = []string{ClaudeAgentsDir, ProjectAgentsDir}

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// frontmatter is the YAML header of an agent definition file.
type frontmatter struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Categories  []string `yaml:"categories"`
	Severity    string   `yaml:"severity"`
	Model       string   `yaml:"model"`
}

// loadDir reads every *.md agent definition in dir. Invalid files are
// skipped and reported as warnings; a missing directory is not an error.
func loadDir(dir string, source Source) (agents []Agent, warnings []string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			warnings = append(warnings, fmt.Sprintf("agents: cannot read %s: %v", dir, err))
		}
		return nil, warnings
	}
	seen := map[string]string{} // name → file, for duplicate detection
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		a, err := loadFile(path, source)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("agents: skipping %s: %v", path, err))
			continue
		}
		if prev, dup := seen[a.Name]; dup {
			warnings = append(warnings, fmt.Sprintf("agents: skipping %s: duplicate name %q (already defined by %s)", path, a.Name, prev))
			continue
		}
		seen[a.Name] = path
		agents = append(agents, a)
	}
	return agents, warnings
}

// loadTree reads every *.md agent definition under root, recursively —
// unlike the user and project directories, plugin layouts are not under
// this tool's control, so nested subdirectories are honoured. Same
// skip-and-warn and duplicate handling as loadDir; a missing root is not
// an error.
func loadTree(root string, source Source) (agents []Agent, warnings []string) {
	seen := map[string]string{} // name → file, for duplicate detection
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == root && os.IsNotExist(err) {
				return filepath.SkipAll
			}
			warnings = append(warnings, fmt.Sprintf("agents: cannot read %s: %v", path, err))
			return nil
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".md") {
			return nil
		}
		a, err := loadFile(path, source)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("agents: skipping %s: %v", path, err))
			return nil
		}
		if prev, dup := seen[a.Name]; dup {
			warnings = append(warnings, fmt.Sprintf("agents: skipping %s: duplicate name %q (already defined by %s)", path, a.Name, prev))
			return nil
		}
		seen[a.Name] = path
		agents = append(agents, a)
		return nil
	})
	return agents, warnings
}

// File is one agent definition fetched from a repository (e.g. over the
// GitLab API), named by its base file name within Dir — the agents
// directory it came from (one of ProjectAgentDirs; empty means
// ProjectAgentsDir).
type File struct {
	Dir     string
	Name    string
	Content []byte
}

// LoadProjectFiles parses fetched project agent definitions with the same
// skip-and-warn and duplicate handling as loadDir. Non-.md files are
// ignored, matching the on-disk loader. Duplicate names are only skipped
// within one directory; across directories both agents are returned, and
// the catalog merge lets the later file shadow the earlier — so callers
// must supply files in ProjectAgentDirs order.
func LoadProjectFiles(files []File) (agents []Agent, warnings []string) {
	seen := map[string]string{} // dir + name → file, for duplicate detection
	for _, f := range files {
		if !strings.HasSuffix(f.Name, ".md") {
			continue
		}
		dir := f.Dir
		if dir == "" {
			dir = ProjectAgentsDir
		}
		ref := dir + "/" + f.Name
		a, err := parse(f.Content, ref, SourceProject)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("agents: skipping %s: %v", ref, err))
			continue
		}
		key := dir + "\x00" + a.Name
		if prev, dup := seen[key]; dup {
			warnings = append(warnings, fmt.Sprintf("agents: skipping %s: duplicate name %q (already defined by %s)", ref, a.Name, prev))
			continue
		}
		seen[key] = ref
		agents = append(agents, a)
	}
	return agents, warnings
}

// loadFile parses one agent definition file from disk.
func loadFile(path string, source Source) (Agent, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // path comes from listing the configured agents directories
	if err != nil {
		return Agent{}, err
	}
	return parse(raw, path, source)
}

// parse builds an Agent from one definition: optional YAML frontmatter
// between --- lines, then the prompt body. Without frontmatter the whole
// file is the prompt and the name comes from the file stem of path.
func parse(raw []byte, path string, source Source) (Agent, error) {
	fm, body, err := splitFrontmatter(string(raw))
	if err != nil {
		return Agent{}, err
	}

	a := Agent{
		Name:        fm.Name,
		Description: fm.Description,
		Source:      source,
		Model:       strings.TrimSpace(fm.Model),
		Prompt:      strings.TrimSpace(body),
		Path:        path,
	}
	if a.Name == "" {
		a.Name = strings.TrimSuffix(filepath.Base(path), ".md")
	}
	if !nameRe.MatchString(a.Name) {
		return Agent{}, fmt.Errorf("invalid agent name %q (want lowercase letters, digits, - or _)", a.Name)
	}
	if a.Prompt == "" {
		return Agent{}, fmt.Errorf("empty prompt body")
	}
	for _, c := range fm.Categories {
		cat := review.Category(c)
		if !cat.Valid() {
			return Agent{}, fmt.Errorf("unknown category %q (known: %v)", c, review.AllCategories)
		}
		a.Categories = append(a.Categories, cat)
	}
	if len(a.Categories) == 0 {
		// Custom agents default to the full label vocabulary.
		a.Categories = append(a.Categories, review.AllCategories...)
	}
	if fm.Severity != "" {
		sev := review.Severity(fm.Severity)
		if !sev.Valid() {
			return Agent{}, fmt.Errorf("unknown severity %q", fm.Severity)
		}
		a.Severity = sev
	}
	return a, nil
}

// splitFrontmatter separates the optional YAML header from the body.
func splitFrontmatter(raw string) (frontmatter, string, error) {
	var fm frontmatter
	content := strings.ReplaceAll(raw, "\r\n", "\n")
	if !strings.HasPrefix(content, "---\n") {
		return fm, content, nil
	}
	rest := content[len("---\n"):]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return fm, "", fmt.Errorf("unterminated frontmatter (missing closing ---)")
	}
	header := rest[:end]
	body := rest[end+len("\n---"):]
	if i := strings.IndexByte(body, '\n'); i >= 0 {
		body = body[i+1:]
	} else {
		body = ""
	}
	if err := yaml.Unmarshal([]byte(header), &fm); err != nil {
		// Claude Code writes descriptions as one long unquoted scalar that
		// may contain ": ", which strict YAML rejects. Fall back to reading
		// the known fields line by line so those definitions stay loadable.
		return parseLenient(header), body, nil
	}
	return fm, body, nil
}

var fieldRe = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9_-]*):\s?(.*)$`)

// parseLenient reads one `key: value` field per line, taking everything
// after the first colon verbatim. Only inline lists ([a, b] or a, b) are
// understood for categories.
func parseLenient(header string) frontmatter {
	var fm frontmatter
	for _, line := range strings.Split(header, "\n") {
		m := fieldRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		val := strings.TrimSpace(m[2])
		switch m[1] {
		case "name":
			fm.Name = val
		case "description":
			fm.Description = val
		case "severity":
			fm.Severity = val
		case "model":
			fm.Model = val
		case "categories":
			fm.Categories = splitInlineList(val)
		}
	}
	return fm
}

func splitInlineList(v string) []string {
	v = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(v), "["), "]")
	var out []string
	for _, item := range strings.Split(v, ",") {
		if item = strings.Trim(strings.TrimSpace(item), `"'`); item != "" {
			out = append(out, item)
		}
	}
	return out
}
