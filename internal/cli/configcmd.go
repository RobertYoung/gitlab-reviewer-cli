package cli

import (
	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v3"
)

func newConfigCmd(st *state) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Inspect and validate the effective configuration",
	}

	show := &cobra.Command{
		Use:   "show",
		Short: "Print the effective configuration (flags > env > file > defaults), secrets redacted",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if st.loaded.FilePath != "" {
				cmd.Println("# settings file:", st.loaded.FilePath)
			} else {
				cmd.Println("# settings file: none (using defaults, env, and flags)")
			}
			out, err := yaml.Marshal(st.loaded.Redacted())
			if err != nil {
				return err
			}
			cmd.Print(string(out))
			return nil
		},
	}

	validate := &cobra.Command{
		Use:   "validate",
		Short: "Validate the effective configuration, including GitLab credentials presence",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg := st.loaded.Config
			if err := cfg.Validate(); err != nil {
				return err
			}
			if err := cfg.ValidateGitLab(); err != nil {
				return err
			}
			cmd.Println("Configuration is valid.")
			return nil
		},
	}

	cmd.AddCommand(show, validate)
	return cmd
}
