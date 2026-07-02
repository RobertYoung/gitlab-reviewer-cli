// Package cli wires the cobra command tree: the root command launches the
// TUI; subcommands cover version, config inspection, and cache maintenance.
package cli

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/config"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/secret"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/tui"
)

// state carries objects built in PersistentPreRunE to the command RunEs.
type state struct {
	configFile string // --config
	loaded     *config.Result
	redactor   *secret.Redactor
}

// New builds the root command.
func New() *cobra.Command {
	return newRoot(&state{redactor: secret.NewRedactor()})
}

func newRoot(st *state) *cobra.Command {
	root := &cobra.Command{
		Use:           "gitlab-reviewer",
		Short:         "Review GitLab merge requests with Claude from your terminal",
		Long:          "gitlab-reviewer is a terminal UI that lists GitLab merge requests,\nhas Claude review them with full repository context, and publishes the\naccepted suggestions back to the MR as inline discussions.",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			res, err := config.Load(config.Options{
				File:  st.configFile,
				Flags: cmd.Flags(),
			})
			if err != nil {
				return err
			}
			st.loaded = res
			st.redactor.Add(res.Config.GitLab.Token)
			return setupLogging(res.Config.Log, st.redactor)
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg := st.loaded.Config
			if err := cfg.Validate(); err != nil {
				return err
			}
			if err := cfg.ValidateGitLab(); err != nil {
				return err
			}
			svc, err := gitlabx.New(cfg.GitLab.BaseURL, cfg.GitLab.Token, cfg.GitLab.Projects, cfg.GitLab.Groups)
			if err != nil {
				return st.redactor.RedactError(err)
			}
			if err := tui.Run(cfg, svc); err != nil {
				return st.redactor.RedactError(err)
			}
			return nil
		},
	}

	root.PersistentFlags().StringVar(&st.configFile, "config", "", "path to settings file (default "+config.DefaultFile()+")")
	addSettingFlags(root)

	root.AddCommand(newVersionCmd(), newConfigCmd(st))
	return root
}

// addSettingFlags declares one flag per setting; values are merged into the
// config by internal/config with flags at the highest precedence.
func addSettingFlags(root *cobra.Command) {
	f := root.PersistentFlags()

	f.String("gitlab-base-url", "", "GitLab base URL (default https://gitlab.com)")
	f.String("gitlab-token", "", "GitLab access token (prefer GITLAB_REVIEWER_GITLAB_TOKEN)")
	f.StringArray("project", nil, "project path to browse, repeatable (group/app)")
	f.StringArray("group", nil, "group path to browse, repeatable")
	f.Int("per-page", 0, "GitLab API page size")

	f.String("provider", "", "AI provider: anthropic|bedrock")
	f.String("model", "", "model passed to the claude CLI")
	f.String("claude-path", "", "path to the claude binary")
	f.Duration("review-timeout", 0, "maximum duration for one review")
	f.Int("max-turns", 0, "maximum agent turns per review")
	f.Float64("max-budget-usd", 0, "maximum spend per review in USD")
	f.StringSlice("categories", nil, "finding categories to request (bug,security,performance,docs,style,design)")
	f.String("instructions", "", "extra review instructions appended to the prompt")
	f.String("instructions-file", "", "file with extra review instructions")
	f.Int("max-diff-kb", 0, "diff budget sent to Claude, in KiB")
	f.StringArray("exclude", nil, "glob of files to exclude from review, repeatable")
	f.Bool("bare", false, "run claude with --bare (skips user hooks/CLAUDE.md; breaks OAuth auth)")
	f.StringArray("review-env", nil, "extra KEY=VALUE env for the claude subprocess, repeatable")

	f.String("aws-region", "", "AWS region for Bedrock")
	f.String("aws-profile", "", "AWS profile for Bedrock")

	f.String("checkout-mode", "", "repo checkout mode: clone|path|root")
	f.String("repo-path", "", "existing local clone (checkout-mode=path)")
	f.String("git-root", "", "structured git root, e.g. ~/git (checkout-mode=root)")
	f.String("clone-transport", "", "clone/fetch transport: https|ssh")
	f.String("cache-dir", "", "cache directory for clones and worktrees")
	f.Int("cache-max-mb", 0, "clone cache size budget in MiB")
	f.Bool("keep-worktree", false, "keep review worktrees after the review")

	f.String("publish-mode", "", "publish mode: draft|immediate")
	f.Bool("auto-comment", false, "publish findings at/above --auto-min-severity without confirmation")
	f.String("auto-min-severity", "", "severity threshold for --auto-comment (info|minor|major|critical)")
	f.Bool("fallback-to-note", true, "post a general MR note when an inline position cannot be resolved")
	f.Bool("attribution", false, "append an attribution footer to published comments")

	f.String("log-level", "", "log level: debug|info|warn|error")
	f.String("log-file", "", "log file path")
}

func setupLogging(cfg config.Log, redactor *secret.Redactor) error {
	var level slog.Level
	if err := level.UnmarshalText([]byte(cfg.Level)); err != nil {
		return fmt.Errorf("log.level: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(cfg.File), 0o700); err != nil {
		return fmt.Errorf("creating log directory: %w", err)
	}
	f, err := os.OpenFile(cfg.File, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("opening log file: %w", err)
	}
	handler := slog.NewTextHandler(f, &slog.HandlerOptions{Level: level})
	slog.SetDefault(slog.New(secret.NewLogHandler(handler, redactor)))
	return nil
}

// Execute runs the CLI and returns a process exit code. Errors are printed
// with secrets redacted through the same redactor that learned the token
// during config loading.
func Execute() int {
	st := &state{redactor: secret.NewRedactor()}
	root := newRoot(st)
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", st.redactor.Redact(err.Error()))
		return 1
	}
	return 0
}
