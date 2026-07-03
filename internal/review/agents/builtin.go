package agents

import (
	"fmt"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review"
)

// builtinGuidance is the focus text for each built-in agent. It was the
// per-category guidance in the shared review prompt before agents became
// separate passes.
var builtinGuidance = map[review.Category]string{
	"bug":         "logic errors, race conditions, unhandled failure paths, off-by-one errors, broken edge cases",
	"security":    "injection, authn/authz gaps, secrets in code, unsafe deserialisation, SSRF, path traversal",
	"performance": "algorithmic complexity, N+1 queries, unbounded memory, missing caching where it clearly matters",
	"docs":        "missing or stale documentation and comments for non-obvious public behaviour",
	"style":       "readability, naming, idiomatic usage, dead code — only where it genuinely hurts maintainability",
	"design":      "API shape, layering violations, error-handling strategy, extensibility problems",
}

var builtinDescriptions = map[review.Category]string{
	"bug":         "logic errors, races, broken edge cases",
	"security":    "vulnerabilities, secrets, unsafe input handling",
	"performance": "complexity, N+1 queries, unbounded memory",
	"docs":        "missing or stale docs and comments",
	"style":       "readability and idiomatic usage",
	"design":      "API shape, layering, error-handling strategy",
}

// Builtins returns the built-in agents, one per finding category, in the
// canonical category order.
func Builtins() []Agent {
	out := make([]Agent, 0, len(review.AllCategories))
	for _, c := range review.AllCategories {
		out = append(out, Agent{
			Name:        string(c),
			Description: builtinDescriptions[c],
			Source:      SourceBuiltin,
			Categories:  []review.Category{c},
			Prompt:      builtinPrompt(c),
		})
	}
	return out
}

func builtinPrompt(c review.Category) string {
	return fmt.Sprintf(`You are the %q review agent. Focus exclusively on: %s.
Other review concerns are handled by other agents; do not stray into them.
If you find nothing in your focus area, return an empty findings list.`, c, builtinGuidance[c])
}
