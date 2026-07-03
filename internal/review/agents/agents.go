// Package agents defines the pluggable review agents that a scan can run:
// the six built-in agents (one per finding category) plus user- and
// project-provided agents loaded from markdown files with YAML frontmatter.
// Each selected agent runs as its own reviewer invocation with its own
// system prompt; findings carry the agent's name for attribution.
package agents

import (
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review"
)

// Source records where an agent definition came from. When names collide,
// project shadows user shadows builtin.
type Source string

const (
	SourceBuiltin Source = "builtin"
	SourceUser    Source = "user"
	SourceProject Source = "project"
)

// Agent is one review focus that runs as its own reviewer pass.
type Agent struct {
	// Name is the unique key used in config, flags, and pickers.
	Name string
	// Description is the one-liner shown in pickers.
	Description string
	Source      Source
	// Categories its findings may be labelled with.
	Categories []review.Category
	// Severity is an optional frontmatter hint folded into the prompt.
	Severity review.Severity
	// Model is an optional frontmatter default: the review model this agent
	// runs with unless the picker overrides it. Empty falls back to
	// review.model, then the claude CLI's own default.
	Model string
	// Prompt is the agent's persona/focus text, appended to the shared
	// reviewer system prompt.
	Prompt string
	// Path is the definition file for user/project agents; "" for builtins.
	Path string
}
