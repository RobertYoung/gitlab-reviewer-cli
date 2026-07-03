package config

import "slices"

// knownModels is the curated per-provider list offered by the `models`
// command and --model shell completion. It is suggestions, not validation:
// any string is still passed through to the claude CLI. review.models
// replaces it entirely when set (new releases land here, but a settings-file
// list never goes stale behind the user's back).
var knownModels = map[string][]string{
	// Aliases the claude CLI resolves itself, then full model IDs.
	"anthropic": {
		"opus",
		"sonnet",
		"haiku",
		"claude-opus-4-8",
		"claude-opus-4-7",
		"claude-opus-4-6",
		"claude-sonnet-5",
		"claude-sonnet-4-6",
		"claude-haiku-4-5",
	},
	// Bedrock cross-region inference-profile IDs. Other regions or
	// account-specific profiles belong in review.models.
	"bedrock": {
		"us.anthropic.claude-opus-4-8",
		"us.anthropic.claude-sonnet-5",
		"us.anthropic.claude-sonnet-4-6",
		"us.anthropic.claude-haiku-4-5",
		"eu.anthropic.claude-opus-4-8",
		"eu.anthropic.claude-sonnet-5",
		"eu.anthropic.claude-sonnet-4-6",
		"eu.anthropic.claude-haiku-4-5",
	},
}

// ModelOptions returns the model IDs to offer for review.model:
// review.models when configured, otherwise the curated list for the
// selected provider.
func (c Config) ModelOptions() []string {
	if len(c.Review.Models) > 0 {
		return slices.Clone(c.Review.Models)
	}
	return slices.Clone(knownModels[c.Review.Provider])
}
