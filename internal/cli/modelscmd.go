package cli

import (
	"slices"

	"github.com/spf13/cobra"
)

func newModelsCmd(st *state) *cobra.Command {
	return &cobra.Command{
		Use:   "models",
		Short: "List models to use with --model / review.model",
		Long: "List the models offered for the AI review: review.models from the\n" +
			"settings file when set, otherwise a curated list of common Claude\n" +
			"models for the selected provider. The list is suggestions only —\n" +
			"--model accepts any model ID the claude CLI understands.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg := st.loaded.Config
			models := cfg.ModelOptions()

			source := "curated defaults for provider " + cfg.Review.Provider
			if len(cfg.Review.Models) > 0 {
				source = "review.models"
			}
			cmd.Println("# " + source + "; * marks the configured review.model")
			current := cfg.Review.Model
			for _, m := range models {
				marker := "  "
				if m == current {
					marker = "* "
				}
				cmd.Println(marker + m)
			}
			switch {
			case current == "":
				cmd.Println("\nreview.model is not set: the claude CLI's own default model is used.")
			case !slices.Contains(models, current):
				cmd.Println("\nreview.model is set to " + current + " (not in the list above).")
			}
			return nil
		},
	}
}
