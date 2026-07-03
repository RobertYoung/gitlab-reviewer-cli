package config

import (
	"slices"
	"testing"
)

func TestModelOptions(t *testing.T) {
	t.Run("anthropic curated list by default", func(t *testing.T) {
		cfg := Default()
		got := cfg.ModelOptions()
		if len(got) == 0 || !slices.Contains(got, "claude-opus-4-8") || !slices.Contains(got, "sonnet") {
			t.Errorf("models = %v", got)
		}
	})

	t.Run("bedrock provider switches the curated list", func(t *testing.T) {
		cfg := Default()
		cfg.Review.Provider = "bedrock"
		got := cfg.ModelOptions()
		if len(got) == 0 || !slices.Contains(got, "eu.anthropic.claude-sonnet-4-6") {
			t.Errorf("models = %v", got)
		}
		if slices.Contains(got, "opus") {
			t.Errorf("bedrock list must not contain anthropic aliases: %v", got)
		}
	})

	t.Run("review.models replaces the curated list", func(t *testing.T) {
		cfg := Default()
		cfg.Review.Models = []string{"my-inference-profile", "claude-opus-4-8"}
		if got := cfg.ModelOptions(); !slices.Equal(got, cfg.Review.Models) {
			t.Errorf("models = %v", got)
		}
	})

	t.Run("review.models from file and env list", func(t *testing.T) {
		file := writeFile(t, "review:\n  models: [alpha, beta]\n")
		res, err := Load(Options{File: file, LookupEnv: envLookup(nil)})
		if err != nil {
			t.Fatal(err)
		}
		if got := res.Config.Review.Models; !slices.Equal(got, []string{"alpha", "beta"}) {
			t.Errorf("models from file = %v", got)
		}

		res, err = Load(Options{File: file, LookupEnv: envLookup(map[string]string{
			"GITLAB_REVIEWER_REVIEW_MODELS": "gamma, delta",
		})})
		if err != nil {
			t.Fatal(err)
		}
		if got := res.Config.Review.Models; !slices.Equal(got, []string{"gamma", "delta"}) {
			t.Errorf("models from env = %v", got)
		}
	})
}
