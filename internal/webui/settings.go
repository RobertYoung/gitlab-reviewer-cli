package webui

import (
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/config"
	"github.com/knadh/koanf/providers/structs"
	"github.com/knadh/koanf/v2"
)

// settingKind is how one setting is rendered and parsed. It maps a config
// key to a form control and back to the value type the settings file holds.
type settingKind string

const (
	kindText     settingKind = "text"     // free-text string
	kindSecret   settingKind = "secret"   // write-only string (e.g. a token)
	kindDuration settingKind = "duration" // Go duration string, e.g. "10m"
	kindInt      settingKind = "int"
	kindFloat    settingKind = "float"
	kindBool     settingKind = "bool"
	kindSelect   settingKind = "select" // one of Options
	kindList     settingKind = "list"   // []string, one per line
	kindMap      settingKind = "map"    // map[string]string, "k=v" per line
)

// settingField describes one configurable setting: its dotted config key,
// how to render it, and human-facing labels.
type settingField struct {
	Key     string
	Label   string
	Help    string
	Kind    settingKind
	Options []string // for kindSelect; an empty entry renders as "(unset)"
}

// settingSection groups related fields under a heading.
type settingSection struct {
	Title  string
	Anchor string
	Help   string
	Fields []settingField
}

// settingsSchema is the full set of editable settings, grouped as they are
// in the config struct. Deliberately omitted are the structured collections
// that a flat form cannot edit without loss — gitlab.instances,
// review.mcp_servers, and per-project overrides — which the round-trip save
// preserves untouched and which are edited in the file directly.
func settingsSchema() []settingSection {
	sev := config.Severities
	return []settingSection{
		{Title: "GitLab", Anchor: "gitlab", Help: "Connection and the merge requests to browse.", Fields: []settingField{
			{Key: "gitlab.base_url", Label: "Base URL", Kind: kindText, Help: "GitLab instance URL, e.g. https://gitlab.com"},
			{Key: "gitlab.token", Label: "Token", Kind: kindSecret, Help: "Personal or project access token. Leave blank to keep the current value."},
			{Key: "gitlab.default_instance", Label: "Default instance", Kind: kindText, Help: "Name of a configured instance to use without prompting."},
			{Key: "gitlab.projects", Label: "Projects", Kind: kindList, Help: "Project full paths to scope the MR list to, one per line."},
			{Key: "gitlab.groups", Label: "Groups", Kind: kindList, Help: "Group full paths to scope the MR list to, one per line."},
			{Key: "gitlab.per_page", Label: "Page size", Kind: kindInt, Help: "MRs fetched per page (1–100)."},
		}},
		{Title: "Review", Anchor: "review", Help: "How AI reviews are run.", Fields: []settingField{
			{Key: "review.provider", Label: "Provider", Kind: kindSelect, Options: []string{"anthropic", "bedrock"}},
			{Key: "review.model", Label: "Model", Kind: kindText, Help: "Model id passed to the claude CLI. Blank uses its default."},
			{Key: "review.models", Label: "Model suggestions", Kind: kindList, Help: "Models offered by the picker, one per line."},
			{Key: "review.claude_path", Label: "Claude path", Kind: kindText, Help: "Path to the claude executable."},
			{Key: "review.timeout", Label: "Timeout", Kind: kindDuration, Help: "Per-review time limit, e.g. 10m."},
			{Key: "review.max_budget_usd", Label: "Max budget (USD)", Kind: kindFloat, Help: "Spend cap per review; 0 disables the cap."},
			{Key: "review.agents", Label: "Agents", Kind: kindList, Help: "Default agent selection, one per line. Blank uses all builtins."},
			{Key: "review.agent_concurrency", Label: "Agent concurrency", Kind: kindInt, Help: "How many agent passes run at once (≥1)."},
			{Key: "review.instructions", Label: "Instructions", Kind: kindText, Help: "Extra reviewer instructions (inline)."},
			{Key: "review.instructions_file", Label: "Instructions file", Kind: kindText, Help: "Path to a file with extra reviewer instructions."},
			{Key: "review.max_diff_kb", Label: "Max diff (KB)", Kind: kindInt, Help: "Largest diff sent to the model (≥1)."},
			{Key: "review.exclude", Label: "Exclude globs", Kind: kindList, Help: "Paths excluded from the diff, one glob per line."},
			{Key: "review.bare", Label: "Bare mode", Kind: kindBool, Help: "Review the diff alone, without a repository checkout."},
			{Key: "review.use_agents", Label: "Use subagents", Kind: kindBool, Help: "Let the reviewer delegate to Claude Code subagents."},
			{Key: "review.env", Label: "Environment", Kind: kindMap, Help: "Variables set in the review subprocess, KEY=value per line."},
		}},
		{Title: "Bedrock", Anchor: "bedrock", Help: "Used when the provider is bedrock.", Fields: []settingField{
			{Key: "bedrock.region", Label: "Region", Kind: kindText},
			{Key: "bedrock.profile", Label: "Profile", Kind: kindText},
		}},
		{Title: "Checkout", Anchor: "checkout", Help: "How the MR's repository is obtained for review.", Fields: []settingField{
			{Key: "checkout.mode", Label: "Mode", Kind: kindSelect, Options: []string{"clone", "path", "root"}},
			{Key: "checkout.path", Label: "Path", Kind: kindText, Help: "Repository path when mode is path."},
			{Key: "checkout.root", Label: "Root", Kind: kindText, Help: "Directory of repositories when mode is root."},
			{Key: "checkout.transport", Label: "Transport", Kind: kindSelect, Options: []string{"https", "ssh"}},
			{Key: "checkout.cache_dir", Label: "Cache dir", Kind: kindText, Help: "Where clones and worktrees are cached."},
			{Key: "checkout.cache_max_mb", Label: "Cache max (MB)", Kind: kindInt},
			{Key: "checkout.keep", Label: "Keep worktrees", Kind: kindBool},
			{Key: "checkout.clone_missing", Label: "Clone missing", Kind: kindBool, Help: "Clone repositories absent from a path/root layout."},
			{Key: "checkout.local_overlay", Label: "Local overlay globs", Kind: kindList, Help: "Untracked files copied into the review worktree, one glob per line."},
		}},
		{Title: "Publish", Anchor: "publish", Help: "How findings are posted back to GitLab.", Fields: []settingField{
			{Key: "publish.mode", Label: "Mode", Kind: kindSelect, Options: []string{"draft", "immediate"}},
			{Key: "publish.auto_comment", Label: "Auto comment", Kind: kindBool, Help: "Post accepted findings automatically after a review."},
			{Key: "publish.auto_min_severity", Label: "Auto min severity", Kind: kindSelect, Options: sev},
			{Key: "publish.min_severity", Label: "Publish floor", Kind: kindSelect, Options: sev, Help: "Findings below this are never posted."},
			{Key: "publish.fallback_to_note", Label: "Fall back to note", Kind: kindBool, Help: "Post as a plain note when an inline position is unavailable."},
			{Key: "publish.attribution", Label: "Attribution", Kind: kindBool, Help: "Add a tool attribution line to comments."},
			{Key: "publish.template", Label: "Comment template", Kind: kindText, Help: "Go text/template for the comment body. Blank uses the built-in layout."},
		}},
		{Title: "Gate", Anchor: "gate", Help: "Tie the review outcome to a severity policy.", Fields: []settingField{
			{Key: "gate.min_severity", Label: "Blocking severity", Kind: kindSelect, Options: append([]string{""}, sev...), Help: "Findings at or above this block; unset disables the gate."},
			{Key: "gate.approvals", Label: "Approvals", Kind: kindSelect, Options: []string{"off", "warn", "block"}, Help: "What approving does while blocking findings remain."},
		}},
		{Title: "Interface", Anchor: "ui", Help: "MR detail screen defaults.", Fields: []settingField{
			{Key: "ui.diff_view", Label: "Diff view", Kind: kindSelect, Options: []string{"unified", "split"}},
			{Key: "ui.file_explorer", Label: "File explorer", Kind: kindSelect, Options: []string{"open", "closed"}},
		}},
		{Title: "Logging", Anchor: "log", Fields: []settingField{
			{Key: "log.level", Label: "Level", Kind: kindSelect, Options: []string{"debug", "info", "warn", "error"}},
			{Key: "log.file", Label: "File", Kind: kindText},
		}},
	}
}

// fieldView is one field resolved for rendering: the current value in the
// shape the template needs, plus the schema metadata.
type fieldView struct {
	settingField
	Value    string   // single-line controls (text/select/int/float/duration)
	Text     string   // multi-line controls (list/map)
	Checked  bool // bool
	TokenSet bool // secret: whether a value is currently configured
}

// Is reports whether the field is of the named kind, for template branching
// (settingKind is a distinct type, so template eq cannot compare it to a
// string literal directly).
func (v fieldView) Is(kind string) bool { return string(v.Kind) == kind }

// settingsContent is the settings page model.
type settingsContent struct {
	Sections []settingSectionView
	FilePath string
	SaveURL  string
	Saved    bool
	// Applied reports whether a successful save also took effect in the
	// running session (only meaningful when Saved).
	Applied bool
	// HotReload reports whether saved changes apply without a restart.
	HotReload bool
	Err       string
	// FileExists reports whether the settings file is present yet.
	FileExists bool
}

type settingSectionView struct {
	settingSection
	Views []fieldView
}

// effectiveValues returns the base configuration as a nested map keyed by the
// config (koanf) keys, with the token removed (write-only) and the duration
// rendered as its string form. It is the source for the initial form values.
func effectiveValues(cfg config.Config) map[string]any {
	k := koanf.New(".")
	// structs provider mirrors the koanf tags, so keys match settingField.Key.
	_ = k.Load(structs.Provider(cfg, "koanf"), nil)
	m := k.Raw()
	config.DeleteValue(m, "gitlab.token")
	config.SetValue(m, "review.timeout", cfg.Review.Timeout.String())
	return m
}

// buildViews resolves every schema field against a values map.
func buildViews(values map[string]any, tokenSet bool) []settingSectionView {
	out := make([]settingSectionView, 0, len(settingsSchema()))
	for _, sec := range settingsSchema() {
		sv := settingSectionView{settingSection: sec}
		for _, f := range sec.Fields {
			raw, _ := mapGet(values, f.Key)
			view := fieldView{settingField: f}
			switch f.Kind {
			case kindBool:
				view.Checked = toBool(raw)
			case kindList:
				view.Text = strings.Join(toStringSlice(raw), "\n")
			case kindMap:
				view.Text = mapLines(raw)
			case kindSecret:
				view.TokenSet = tokenSet
			case kindFloat:
				if s := toString(raw); s != "" && s != "0" {
					view.Value = s
				}
			default:
				view.Value = toString(raw)
			}
			sv.Views = append(sv.Views, view)
		}
		out = append(out, sv)
	}
	return out
}

// handleSettings renders the settings page seeded from the effective base
// configuration.
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	cfg := s.currentConfig()
	values := effectiveValues(cfg)
	content := settingsContent{
		Sections:   buildViews(values, cfg.GitLab.Token != ""),
		FilePath:   s.settingsFile(),
		SaveURL:    "/settings",
		Saved:      r.URL.Query().Get("saved") == "1",
		Applied:    r.URL.Query().Get("applied") == "1",
		HotReload:  s.opts.Reload != nil,
		FileExists: fileExists(s.settingsFile()),
	}
	s.render(w, http.StatusOK, "settings", pageData{Title: "settings", Content: content})
}

// handleSettingsSave round-trips the settings file: it reads the raw file,
// applies the submitted fields on top (preserving keys the form does not
// manage), validates the result, and writes it back atomically.
func (s *Server) handleSettingsSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderError(w, http.StatusBadRequest, err)
		return
	}
	path := s.settingsFile()
	values, err := config.FileValues(path)
	if err != nil {
		s.renderError(w, http.StatusInternalServerError, err)
		return
	}

	var parseErrs []string
	for _, sec := range settingsSchema() {
		for _, f := range sec.Fields {
			if err := applyField(values, f, r); err != nil {
				parseErrs = append(parseErrs, err.Error())
			}
		}
	}

	fail := func(status int, msg string) {
		tokenSet := s.currentConfig().GitLab.Token != ""
		if _, ok := mapGet(values, "gitlab.token"); ok {
			tokenSet = true
		}
		content := settingsContent{
			Sections:   buildViews(values, tokenSet),
			FilePath:   path,
			SaveURL:    "/settings",
			HotReload:  s.opts.Reload != nil,
			Err:        msg,
			FileExists: fileExists(path),
		}
		s.render(w, status, "settings", pageData{Title: "settings", Content: content})
	}

	if len(parseErrs) > 0 {
		fail(http.StatusBadRequest, strings.Join(parseErrs, "\n"))
		return
	}
	if err := config.ValidateFileValues(values); err != nil {
		fail(http.StatusBadRequest, err.Error())
		return
	}
	if err := config.SaveFile(path, values); err != nil {
		fail(http.StatusInternalServerError, "writing "+path+": "+err.Error())
		return
	}

	// Apply the change to the running session. A reload failure is not a save
	// failure — the file is written — so it surfaces as "restart to apply".
	redirect := "/settings?saved=1"
	if s.opts.Reload != nil {
		if err := s.reload(); err != nil {
			slog.Warn("settings saved but hot reload failed", "error", err)
		} else {
			redirect += "&applied=1"
		}
	}
	http.Redirect(w, r, redirect, http.StatusSeeOther)
}

// applyField writes one submitted field into the values map, deleting the
// key when the field is cleared so defaults reapply, and preserving an
// unchanged secret. It returns an error only when a numeric field cannot be
// parsed.
func applyField(values map[string]any, f settingField, r *http.Request) error {
	raw := r.FormValue(f.Key)
	switch f.Kind {
	case kindBool:
		config.SetValue(values, f.Key, raw == "on")
	case kindSecret:
		if v := strings.TrimSpace(raw); v != "" {
			config.SetValue(values, f.Key, v)
		}
	case kindInt:
		v := strings.TrimSpace(raw)
		if v == "" {
			config.DeleteValue(values, f.Key)
			return nil
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("%s: %q is not a whole number", f.Label, raw)
		}
		config.SetValue(values, f.Key, n)
	case kindFloat:
		v := strings.TrimSpace(raw)
		if v == "" {
			config.DeleteValue(values, f.Key)
			return nil
		}
		n, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return fmt.Errorf("%s: %q is not a number", f.Label, raw)
		}
		config.SetValue(values, f.Key, n)
	case kindList:
		if lines := formLines(raw); len(lines) > 0 {
			config.SetValue(values, f.Key, toAnySlice(lines))
		} else {
			config.DeleteValue(values, f.Key)
		}
	case kindMap:
		m := parseKeyVals(raw)
		if len(m) > 0 {
			config.SetValue(values, f.Key, m)
		} else {
			config.DeleteValue(values, f.Key)
		}
	default: // kindText, kindSelect, kindDuration
		if v := strings.TrimSpace(raw); v != "" {
			config.SetValue(values, f.Key, v)
		} else {
			config.DeleteValue(values, f.Key)
		}
	}
	return nil
}

func (s *Server) settingsFile() string {
	if s.opts.SettingsFile != "" {
		return s.opts.SettingsFile
	}
	return config.DefaultFile()
}
