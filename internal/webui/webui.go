// Package webui serves the browser-based GUI: the same review workflow as
// the TUI — pick an instance, browse MRs, read the diff, comment, run a
// review, curate findings, publish — rendered as HTML over a loopback-only
// HTTP server. It is a second frontend over the same core packages
// (gitlabx, review/runner, review/publisher, resultstore, runlog); no
// review logic lives here.
package webui

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/checkout"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/config"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review/agents"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review/resultstore"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review/runlog"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review/runner"
)

//go:embed templates static
var assets embed.FS

// Deps bundles everything the handlers need for one GitLab instance. CfgFor
// resolves per-project overrides on top of the instance's configuration.
type Deps struct {
	Cfg      config.Config
	Svc      gitlabx.Service
	Reviewer review.Reviewer
	// Chatter answers conversational questions about an MR inside its
	// checkout; nil disables the chat pages.
	Chatter  review.Chatter
	Checkout runner.CheckoutFunc
	CfgFor   func(projectPath string) config.Config
	// Agents is the catalog of available review agents (builtins + user
	// agents); nil means builtins only.
	Agents *agents.Catalog
	// Selection remembers the last agent selection per project; nil is a
	// no-op store.
	Selection *agents.SelectionStore
	// ProjectAgents caches the repo-shipped agent definitions the review
	// form fetches over the API, per (project, MR head); nil fetches
	// uncached.
	ProjectAgents *agents.RemoteCache
	Logs          *runlog.Store
	Results       *resultstore.Store
}

// catalog returns the agent catalog, defaulting to builtins only.
func (d *Deps) catalog() *agents.Catalog {
	if d.Agents == nil {
		return agents.NewCatalog(nil, nil)
	}
	return d.Agents
}

// projectCatalog extends the catalog with agents shipped in the MR's
// repository under .gitlab-reviewer/agents/ or .claude/agents/, so the
// review form can offer them before any checkout exists: read from the
// local clone in path/root checkout modes (covering definitions kept
// untracked), otherwise fetched over the API at the MR head. Best-effort:
// on fetch failure the base catalog is returned with a warning.
func (d *Deps) projectCatalog(ctx context.Context, detail *gitlabx.MRDetail) (*agents.Catalog, []string) {
	base := d.catalog()
	cfg := d.cfgFor(detail.ProjectPath)
	if dir, ok := checkout.LocalRepoDir(cfg.Checkout, cfg.GitLab.BaseURL, detail.ProjectPath); ok {
		return base.WithProject(dir), nil
	}
	if detail.HeadSHA == "" {
		return base, nil
	}
	cat, err := d.ProjectAgents.Extend(base, detail.ProjectPath, detail.HeadSHA, func() ([]agents.File, error) {
		var files []agents.File
		for _, dir := range agents.ProjectAgentDirs {
			repoFiles, err := d.Svc.ListDirectoryFiles(ctx, detail.Project(), dir, detail.HeadSHA)
			if err != nil {
				return nil, err
			}
			for _, f := range repoFiles {
				files = append(files, agents.File{Dir: dir, Name: f.Name, Content: f.Content})
			}
		}
		return files, nil
	})
	if err != nil {
		return base, []string{"agents: could not fetch repo agents: " + err.Error()}
	}
	return cat, nil
}

func (d *Deps) cfgFor(projectPath string) config.Config {
	if d.CfgFor == nil {
		return d.Cfg
	}
	return d.CfgFor(projectPath)
}

// defaultInstance is the pseudo-instance name used when no named instances
// are configured; it cannot collide because real names only apply when
// gitlab.instances is set.
const defaultInstance = "default"

// instanceNames returns the selectable instance names for a configuration,
// falling back to the single unnamed pseudo-instance when none are named.
func instanceNames(cfg config.Config) []string {
	names := cfg.InstanceNames()
	if len(names) == 0 {
		return []string{defaultInstance}
	}
	return names
}

// Options configure the server.
type Options struct {
	// Instances are the selectable GitLab instance names, in config order.
	// Empty means a single unnamed instance served as "default".
	Instances []string
	// MakeDeps builds the dependencies for one instance; called lazily on
	// first use and cached. It may fail (e.g. an unset token_env variable),
	// which surfaces as an error page for that instance only.
	MakeDeps func(instance string) (*Deps, error)
	// ReviewsDir is where run logs and result records live; record and log
	// links are validated against it.
	ReviewsDir string
	// Version is shown in the page footer.
	Version string
	// SettingsFile is the settings file the settings page reads and writes.
	// Empty falls back to config.DefaultFile().
	SettingsFile string
	// BaseConfig is the effective configuration (before any instance is
	// selected) the settings page shows as the current values.
	BaseConfig config.Config
	// Reload re-reads the settings file from disk, rebuilds the shared
	// config-derived state, and returns the new effective base config. It is
	// called after the settings page writes the file so changes take effect
	// without a restart. Nil disables hot reload: saved changes then apply
	// only on the next launch.
	Reload func() (config.Config, error)
}

// Server is the browser GUI: an http.Handler plus the session state that
// backs it (per-instance deps, in-flight review runs, pending comments).
type Server struct {
	opts      Options
	token     string
	pages     map[string]*template.Template
	chromaCSS []byte

	mu         sync.Mutex
	instances  []string // guarded by mu; refreshed by reload
	deps       map[string]*Deps
	baseConfig config.Config // guarded by mu; swapped by reload

	runs     *runRegistry
	comments *commentStore
	chats    *chatRegistry
}

// New builds the server and generates its per-session access token.
func New(opts Options) (*Server, error) {
	instances := opts.Instances
	if len(instances) == 0 {
		instances = []string{defaultInstance}
	}
	pages, err := parseTemplates()
	if err != nil {
		return nil, err
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return nil, fmt.Errorf("generating session token: %w", err)
	}
	css, err := syntaxCSS()
	if err != nil {
		return nil, fmt.Errorf("generating syntax stylesheet: %w", err)
	}
	return &Server{
		opts:       opts,
		instances:  instances,
		token:      hex.EncodeToString(buf),
		pages:      pages,
		chromaCSS:  css,
		deps:       map[string]*Deps{},
		baseConfig: opts.BaseConfig,
		runs:       newRunRegistry(),
		comments:   newCommentStore(),
		chats:      newChatRegistry(),
	}, nil
}

// currentConfig returns the effective base configuration, which reload swaps
// in place when the settings file changes.
func (s *Server) currentConfig() config.Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.baseConfig
}

// reload re-reads the settings file and rebuilds the config-derived state,
// then discards the cached per-instance deps so the next request rebuilds
// them against the new configuration. In-flight requests keep the deps they
// already hold. It is a no-op when hot reload is not configured.
func (s *Server) reload() error {
	if s.opts.Reload == nil {
		return nil
	}
	cfg, err := s.opts.Reload()
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.baseConfig = cfg
	s.instances = instanceNames(cfg)
	s.deps = map[string]*Deps{}
	s.mu.Unlock()
	return nil
}

// instanceList returns a snapshot of the selectable instance names.
func (s *Server) instanceList() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.instances...)
}

// Token returns the session access token baked into the launch URL.
func (s *Server) Token() string { return s.token }

// instanceDeps returns the cached dependencies for one instance, building
// them on first use.
func (s *Server) instanceDeps(name string) (*Deps, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !validInstance(s.instances, name) {
		return nil, fmt.Errorf("unknown GitLab instance %q", name)
	}
	if d, ok := s.deps[name]; ok {
		return d, nil
	}
	arg := name
	if name == defaultInstance && len(s.baseConfig.GitLab.Instances) == 0 {
		arg = "" // unnamed single instance
	}
	d, err := s.opts.MakeDeps(arg)
	if err != nil {
		return nil, err
	}
	s.deps[name] = d
	return d, nil
}

func validInstance(instances []string, name string) bool {
	return slices.Contains(instances, name)
}

// Handler returns the routed, authenticated handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	static, _ := fs.Sub(assets, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	mux.HandleFunc("GET /static/chroma.css", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		_, _ = w.Write(s.chromaCSS)
	})

	mux.HandleFunc("GET /{$}", s.handleHome)
	mux.HandleFunc("GET /settings", s.handleSettings)
	mux.HandleFunc("POST /settings", s.handleSettingsSave)
	mux.HandleFunc("GET /i/{inst}/{$}", s.withDeps(s.handleMRList))
	mux.HandleFunc("GET /i/{inst}/browse", s.withDeps(s.handleBrowse))
	mux.HandleFunc("GET /i/{inst}/mr", s.withDeps(s.handleMRDetail))
	mux.HandleFunc("GET /i/{inst}/mr/status", s.withDeps(s.handleMRStatus))
	mux.HandleFunc("GET /i/{inst}/mr/diff", s.withDeps(s.handleDiff))
	mux.HandleFunc("GET /i/{inst}/mr/diff/context", s.withDeps(s.handleDiffContext))
	mux.HandleFunc("POST /i/{inst}/mr/comment", s.withDeps(s.handleCommentAdd))
	mux.HandleFunc("POST /i/{inst}/mr/comment/delete", s.withDeps(s.handleCommentDelete))
	mux.HandleFunc("POST /i/{inst}/mr/approve", s.withDeps(s.handleApprove))
	mux.HandleFunc("POST /i/{inst}/mr/review", s.withDeps(s.handleReviewStart))
	mux.HandleFunc("GET /i/{inst}/run/{run}", s.withDeps(s.handleRunPage))
	mux.HandleFunc("GET /i/{inst}/run/{run}/events", s.withDeps(s.handleRunEvents))
	mux.HandleFunc("POST /i/{inst}/run/{run}/cancel", s.withDeps(s.handleRunCancel))
	mux.HandleFunc("GET /i/{inst}/mr/findings", s.withDeps(s.handleFindings))
	mux.HandleFunc("POST /i/{inst}/mr/findings/state", s.withDeps(s.handleFindingState))
	mux.HandleFunc("GET /i/{inst}/mr/publish", s.withDeps(s.handlePublishForm))
	mux.HandleFunc("POST /i/{inst}/mr/publish", s.withDeps(s.handlePublish))
	mux.HandleFunc("POST /i/{inst}/mr/publish/review", s.withDeps(s.handlePublishReview))
	mux.HandleFunc("GET /i/{inst}/mr/history", s.withDeps(s.handleHistory))
	mux.HandleFunc("GET /i/{inst}/mr/log", s.withDeps(s.handleLogView))
	mux.HandleFunc("POST /i/{inst}/mr/chat/start", s.withDeps(s.handleChatStart))
	mux.HandleFunc("GET /i/{inst}/chat/{chat}", s.withDeps(s.handleChatPage))
	mux.HandleFunc("POST /i/{inst}/chat/{chat}/send", s.withDeps(s.handleChatSend))
	mux.HandleFunc("GET /i/{inst}/chat/{chat}/events", s.withDeps(s.handleChatEvents))
	mux.HandleFunc("POST /i/{inst}/chat/{chat}/cancel", s.withDeps(s.handleChatCancel))
	mux.HandleFunc("POST /i/{inst}/chat/{chat}/end", s.withDeps(s.handleChatEnd))

	return s.auth(mux)
}

// withDeps resolves the {inst} path segment to its dependencies before the
// handler runs, rendering an error page when the instance cannot be used
// (unknown, or its token_env variable is unset).
func (s *Server) withDeps(h func(http.ResponseWriter, *http.Request, *Deps)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		d, err := s.instanceDeps(r.PathValue("inst"))
		if err != nil {
			s.renderError(w, http.StatusBadRequest, err)
			return
		}
		h(w, r, d)
	}
}

// Serve listens on 127.0.0.1:port (0 picks a free port), reports the
// tokenised launch URL through ready, and serves until ctx is cancelled.
func (s *Server) Serve(ctx context.Context, port int, ready func(url string)) error {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return err
	}
	srv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() { //nolint:gosec // shutdown must outlive ctx: it runs after ctx is done
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// End open conversations first so their worktrees are released.
		s.chats.closeAll(shutdownCtx)
		if err := srv.Shutdown(shutdownCtx); err != nil {
			slog.Warn("gui server shutdown", "error", err)
		}
	}()
	if ready != nil {
		ready(fmt.Sprintf("http://%s/?token=%s", ln.Addr().String(), s.token))
	}
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// mrKey identifies one MR across the session state (pending comments).
func mrKey(instance, project string, iid int64) string {
	return instance + "\x00" + project + "\x00" + fmt.Sprint(iid)
}

// mrQuery extracts the ?project=&iid= pair every MR-scoped page carries.
func mrQuery(r *http.Request) (project string, iid int64, err error) {
	project = r.FormValue("project")
	if project == "" {
		return "", 0, errors.New("missing project parameter")
	}
	if _, err := fmt.Sscanf(r.FormValue("iid"), "%d", &iid); err != nil || iid <= 0 {
		return "", 0, errors.New("missing or invalid iid parameter")
	}
	return project, iid, nil
}

// safeStoreFile validates a user-supplied record/log reference: it must be
// a bare well-known file name, resolved inside the reviews directory.
func (s *Server) safeStoreFile(name, ext string) (string, error) {
	if name == "" || name != path.Base(name) || !strings.HasPrefix(name, "review-") || !strings.HasSuffix(name, ext) {
		return "", fmt.Errorf("invalid file reference %q", name)
	}
	return path.Join(s.opts.ReviewsDir, name), nil
}
