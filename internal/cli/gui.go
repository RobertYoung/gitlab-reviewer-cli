package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/spf13/cobra"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/checkout"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/config"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review/agents"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review/claudecli"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review/resultstore"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review/runlog"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/version"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/webui"
)

// newGUICmd serves the browser-based GUI: the same review workflow as the
// TUI over a loopback-only web server.
func newGUICmd(st *state) *cobra.Command {
	var (
		port      int
		noBrowser bool
	)
	cmd := &cobra.Command{
		Use:   "gui",
		Short: "Serve the browser-based GUI",
		Long:  "gui starts a local web server on 127.0.0.1 and opens your browser on a\nsession-tokenised URL. It offers the same workflow as the TUI — browse MRs,\nread the diff, comment, run AI reviews, curate findings, publish — with\nHTML rendering: syntax-highlighted diffs, a persistent file explorer, and\ninline discussion threads.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg := st.loaded.Config
			if err := cfg.Validate(); err != nil {
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
			logs := runlog.NewStore(reviewsDir)
			results := resultstore.NewStore(reviewsDir)
			catalog := agents.NewCatalog(config.UserAgentDirs()...)
			selection := agents.NewSelectionStore(filepath.Join(config.DefaultStateDir(), "agent-selection.json"))

			// The live configuration the GUI serves. The settings page swaps
			// it in place (via Reload below) so saved changes take effect
			// without a restart; MakeDeps reads it on every deps rebuild.
			cfgHolder := &guiConfigHolder{loaded: st.loaded, reviewer: reviewer}

			// Enforce the clone-cache budget in the background; the server
			// must not wait on a filesystem walk.
			if manager, err := checkout.NewManager(cfg.Checkout, cfg.GitLab.BaseURL, cfg.GitLab.Token); err == nil {
				go func() {
					if res, err := manager.EvictIfNeeded(context.Background()); err != nil {
						slog.Warn("cache eviction failed", "error", err)
					} else if len(res.Removed) > 0 {
						slog.Info("evicted cached clones", "count", len(res.Removed), "freed_bytes", res.FreedBytes)
					}
				}()
			}

			srv, err := webui.New(webui.Options{
				Instances:    cfg.InstanceNames(),
				ReviewsDir:   reviewsDir,
				Version:      version.Version,
				SettingsFile: st.loaded.FilePath,
				BaseConfig:   cfg,
				MakeDeps: func(instance string) (*webui.Deps, error) {
					loaded, reviewer := cfgHolder.get()
					icfg := loaded.Config
					if instance != "" {
						var err error
						if icfg, err = loaded.Config.WithInstance(instance); err != nil {
							return nil, err
						}
					}
					svc, err := gitlabx.New(icfg.GitLab.BaseURL, icfg.GitLab.Token, icfg.GitLab.Projects, icfg.GitLab.Groups)
					if err != nil {
						return nil, st.redactor.RedactError(err)
					}
					manager, err := checkout.NewManager(icfg.Checkout, icfg.GitLab.BaseURL, icfg.GitLab.Token)
					if err != nil {
						return nil, st.redactor.RedactError(err)
					}
					deps := &webui.Deps{
						Cfg:       icfg,
						Svc:       svc,
						Reviewer:  reviewer,
						Chatter:   reviewer,
						Agents:    catalog,
						Selection: selection,
						// Per-instance cache: the (project, sha) key is only
						// unique within one GitLab instance.
						ProjectAgents: agents.NewRemoteCache(),
						Logs:          logs,
						Results:       results,
						Checkout: func(ctx context.Context, mr gitlabx.MRDetail, progress func(string)) (string, func(context.Context) error, error) {
							co, err := manager.Ensure(ctx, mr, progress)
							if err != nil {
								return "", nil, st.redactor.RedactError(err)
							}
							return co.Path, co.Close, nil
						},
						CfgFor: func(projectPath string) config.Config {
							projectCfg, err := loaded.ForProject(projectPath)
							if err != nil {
								return icfg
							}
							// Per-project overrides cover review/checkout/publish/gate
							// only; keep the resolved instance's gitlab settings.
							projectCfg.GitLab = icfg.GitLab
							return projectCfg
						},
					}
					return deps, nil
				},
				Reload: func() (config.Config, error) {
					res, err := config.Load(config.Options{File: st.configFile, Flags: cmd.Flags()})
					if err != nil {
						return config.Config{}, st.redactor.RedactError(err)
					}
					if err := res.Config.Validate(); err != nil {
						return config.Config{}, err
					}
					if _, err := review.ParseBodyTemplate(res.Config.Publish.Template); err != nil {
						return config.Config{}, err
					}
					st.redactor.Add(res.Config.GitLab.Token)
					for _, inst := range res.Config.GitLab.Instances {
						st.redactor.Add(inst.Token)
					}
					// Rebuild the reviewer: provider, model, claude_path,
					// timeout and env may all have changed.
					cfgHolder.set(res, claudecli.New(res.Config, reviewsDir))
					return res.Config, nil
				},
			})
			if err != nil {
				return err
			}

			err = srv.Serve(cmd.Context(), port, func(url string) {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "gitlab-reviewer GUI listening — open:\n\n  %s\n\nPress ctrl+c to stop.\n", url)
				if !noBrowser {
					if err := openBrowser(url); err != nil {
						slog.Warn("could not open the browser", "error", err)
					}
				}
			})
			if err != nil {
				return st.redactor.RedactError(err)
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&port, "port", 0, "port to listen on (default: a random free port)")
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "do not open the browser automatically")
	return cmd
}

// guiConfigHolder holds the configuration the GUI serves, together with the
// reviewer built from it. The settings page swaps both atomically when the
// settings file is saved, so handlers pick up new values without a restart.
type guiConfigHolder struct {
	mu       sync.RWMutex
	loaded   *config.Result
	reviewer *claudecli.Backend
}

func (h *guiConfigHolder) get() (*config.Result, *claudecli.Backend) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.loaded, h.reviewer
}

func (h *guiConfigHolder) set(loaded *config.Result, reviewer *claudecli.Backend) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.loaded = loaded
	h.reviewer = reviewer
}

// openBrowser opens url in the platform's default browser. The URL is a
// single argv element (no shell), so it cannot inject commands.
func openBrowser(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start() //nolint:gosec // loopback URL built by the server, passed as one arg
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start() //nolint:gosec // loopback URL built by the server, passed as one arg
	default:
		return exec.Command("xdg-open", url).Start() //nolint:gosec // loopback URL built by the server, passed as one arg
	}
}
