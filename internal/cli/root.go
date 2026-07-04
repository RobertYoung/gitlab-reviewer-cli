// Package cli wires the cobra command tree: the root command launches the
// TUI; subcommands cover the browser GUI, version, config inspection, and
// cache maintenance.
package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/checkout"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/config"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review/agents"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review/claudecli"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review/resultstore"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review/runlog"
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
			for _, inst := range res.Config.GitLab.Instances {
				st.redactor.Add(inst.Token)
			}
			return setupLogging(res.Config.Log, st.redactor)
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg := st.loaded.Config
			if err := cfg.Validate(); err != nil {
				return err
			}
			cfg, err := resolveInstance(cfg)
			if err != nil {
				return err
			}
			if err := cfg.ValidateGitLab(); err != nil {
				return err
			}
			// Full template check (field names included) — cfg.Validate
			// only covers syntax.
			if _, err := review.ParseBodyTemplate(cfg.Publish.Template); err != nil {
				return err
			}

			// Raw stream transcripts and run logs share one directory.
			reviewsDir := filepath.Join(config.DefaultStateDir(), "reviews")
			reviewer := claudecli.New(cfg, reviewsDir)
			if err := reviewer.CheckAvailable(cmd.Context()); err != nil {
				return err
			}

			svc, err := gitlabx.New(cfg.GitLab.BaseURL, cfg.GitLab.Token, cfg.GitLab.Projects, cfg.GitLab.Groups)
			if err != nil {
				return st.redactor.RedactError(err)
			}
			manager, err := checkout.NewManager(cfg.Checkout, cfg.GitLab.BaseURL, cfg.GitLab.Token)
			if err != nil {
				return st.redactor.RedactError(err)
			}
			// Enforce the clone-cache budget in the background; the TUI
			// must not wait on a filesystem walk.
			go func() {
				if res, err := manager.EvictIfNeeded(context.Background()); err != nil {
					slog.Warn("cache eviction failed", "error", err)
				} else if len(res.Removed) > 0 {
					slog.Info("evicted cached clones", "count", len(res.Removed), "freed_bytes", res.FreedBytes)
				}
			}()

			deps := tui.Deps{
				Cfg:           cfg,
				Svc:           svc,
				Reviewer:      reviewer,
				Chatter:       reviewer,
				Agents:        agents.NewCatalog(config.DefaultAgentsDir()),
				Selection:     agents.NewSelectionStore(filepath.Join(config.DefaultStateDir(), "agent-selection.json")),
				ProjectAgents: agents.NewRemoteCache(),
				Logs:          runlog.NewStore(reviewsDir),
				Results:       resultstore.NewStore(reviewsDir),
				Checkout: func(ctx context.Context, mr gitlabx.MRDetail, progress func(string)) (string, func(context.Context) error, error) {
					co, err := manager.Ensure(ctx, mr, progress)
					if err != nil {
						return "", nil, st.redactor.RedactError(err)
					}
					return co.Path, co.Close, nil
				},
				CfgFor: func(projectPath string) config.Config {
					projectCfg, err := st.loaded.ForProject(projectPath)
					if err != nil {
						return cfg
					}
					// Per-project overrides cover review/checkout/publish/gate
					// only; keep the resolved instance's gitlab settings.
					projectCfg.GitLab = cfg.GitLab
					return projectCfg
				},
			}
			if err := tui.Run(deps); err != nil {
				return st.redactor.RedactError(err)
			}
			return nil
		},
	}

	root.PersistentFlags().StringVar(&st.configFile, "config", "", "path to settings file (default "+config.DefaultFile()+")")
	addSettingFlags(root)

	// --model completes to the same list the models command prints. The
	// completion runs outside the normal command path, so load the
	// configuration on demand if PersistentPreRunE hasn't populated it.
	_ = root.RegisterFlagCompletionFunc("model", func(cmd *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		if st.loaded == nil {
			res, err := config.Load(config.Options{File: st.configFile, Flags: cmd.Flags()})
			if err != nil {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			st.loaded = res
		}
		return st.loaded.Config.ModelOptions(), cobra.ShellCompDirectiveNoFileComp
	})

	root.AddCommand(newVersionCmd(), newModelsCmd(st), newConfigCmd(st), newCacheCmd(st), newGUICmd(st), newReviewCmd(st))
	return root
}

// addSettingFlags declares one flag per setting; values are merged into the
// config by internal/config with flags at the highest precedence.
func addSettingFlags(root *cobra.Command) {
	f := root.PersistentFlags()

	f.String("gitlab-base-url", "", "GitLab base URL (default https://gitlab.com)")
	f.String("gitlab-token", "", "GitLab access token (prefer GITLAB_REVIEWER_GITLAB_TOKEN)")
	f.String("instance", "", "named GitLab instance to use (see gitlab.instances)")
	f.StringArray("project", nil, "project path to browse, repeatable (group/app)")
	f.StringArray("group", nil, "group path to browse, repeatable")
	f.Int("per-page", 0, "GitLab API page size")

	f.String("provider", "", "AI provider: anthropic|bedrock")
	f.String("model", "", "model passed to the claude CLI")
	f.StringSlice("models", nil, "models offered by the models command and --model completion (default: curated per-provider list)")
	f.String("claude-path", "", "path to the claude binary")
	f.Duration("review-timeout", 0, "maximum duration for one review")
	f.Float64("max-budget-usd", 0, "maximum spend per review in USD, shared across the selected agents")
	f.StringSlice("agents", nil, "review agents to run (builtin: bug,security,performance,docs,style,design; plus custom agents by name)")
	f.Int("agent-concurrency", 0, "how many agent passes run at once (default 3)")
	f.StringSlice("categories", nil, "finding categories to request (bug,security,performance,docs,style,design)")
	_ = f.MarkDeprecated("categories", "use --agents; category names are builtin agent names")
	f.String("instructions", "", "extra review instructions appended to the prompt")
	f.String("instructions-file", "", "file with extra review instructions")
	f.Int("max-diff-kb", 0, "diff budget sent to Claude, in KiB")
	f.StringArray("exclude", nil, "glob of files to exclude from review, repeatable")
	f.Bool("bare", false, "run claude with --bare (skips user hooks/CLAUDE.md; breaks OAuth auth)")
	f.Bool("use-agents", false, "let the reviewer delegate to Claude Code subagents (.claude/agents)")
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
	f.StringArray("local-overlay", nil, "glob of untracked files copied from the local clone into the review worktree, repeatable (path/root modes)")

	f.String("publish-mode", "", "publish mode: draft|immediate")
	f.Bool("auto-comment", false, "publish findings at/above --auto-min-severity without confirmation")
	f.String("auto-min-severity", "", "severity threshold for --auto-comment (info|minor|major|critical)")
	f.String("publish-min-severity", "", "publish floor: findings below it are never posted (info|minor|major|critical)")
	f.Bool("fallback-to-note", true, "post a general MR note when an inline position cannot be resolved")
	f.Bool("attribution", false, "append an attribution footer to published comments")
	f.String("publish-template", "", "comment body template ({{.severity}} {{.category}} {{.agent}} {{.title}} {{.body}} {{.file}}); e.g. '{{.body}}' for plain comments")

	f.String("gate-min-severity", "", "findings at/above this severity are blocking: the review command exits 2 and approvals warn or block (info|minor|major|critical)")
	f.String("gate-approvals", "", "approving while blocking findings remain: off|warn|block")

	f.String("diff-view", "", "diff layout in the MR detail screen: unified|split")
	f.String("file-explorer", "", "initial state of the changed-files explorer in the MR detail screen: open|closed")

	f.String("log-level", "", "log level: debug|info|warn|error")
	f.String("log-file", "", "log file path")
}

// resolveInstance narrows the configuration to one GitLab instance: the one
// named by --instance / gitlab.default_instance, the only one configured, or
// an interactive pick when several are. With no instances configured the
// top-level gitlab settings are used unchanged.
func resolveInstance(cfg config.Config) (config.Config, error) {
	instances := cfg.GitLab.Instances
	if len(instances) == 0 {
		return cfg, nil
	}
	name := cfg.GitLab.DefaultInstance
	if name == "" && len(instances) == 1 {
		name = instances[0].Name
	}
	if name == "" {
		if !term.IsTerminal(os.Stdin.Fd()) {
			return cfg, fmt.Errorf("multiple GitLab instances configured (%s): pass --instance or set gitlab.default_instance",
				strings.Join(cfg.InstanceNames(), ", "))
		}
		var err error
		if name, err = tui.SelectInstance(instances); err != nil {
			return cfg, err
		}
	}
	return cfg.WithInstance(name)
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

// exitError carries a distinct process exit code through cobra's error path;
// plain errors exit 1.
type exitError struct {
	code int
	msg  string
}

func (e *exitError) Error() string { return e.msg }

// Execute runs the CLI and returns a process exit code. Errors are printed
// with secrets redacted through the same redactor that learned the token
// during config loading.
func Execute() int {
	st := &state{redactor: secret.NewRedactor()}
	root := newRoot(st)
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", st.redactor.Redact(err.Error()))
		if ee, ok := errors.AsType[*exitError](err); ok {
			return ee.code
		}
		return 1
	}
	return 0
}
