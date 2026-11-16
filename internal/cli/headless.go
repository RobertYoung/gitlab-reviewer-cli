package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/config"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review/resultstore"
)

type mrTarget struct {
	project string
	iid     int64
	host    string
}

func (t mrTarget) ref() string { return fmt.Sprintf("%s!%d", t.project, t.iid) }

func parseTarget(arg string) (mrTarget, error) {
	project, iid, host, err := parseMRTarget(arg)
	return mrTarget{project: project, iid: iid, host: host}, err
}

// connect resolves the GitLab instance without ever prompting (a URL
// target's host selects it) and builds a client for it.
func (st *state) connect(host string) (config.Config, gitlabx.Service, error) {
	cfg := st.loaded.Config
	if err := cfg.Validate(); err != nil {
		return cfg, nil, err
	}
	cfg, err := resolveInstanceForHost(cfg, host)
	if err != nil {
		return cfg, nil, err
	}
	if err := cfg.ValidateGitLab(); err != nil {
		return cfg, nil, err
	}
	svc, err := gitlabx.New(cfg.GitLab.BaseURL, cfg.GitLab.Token, cfg.GitLab.Projects, cfg.GitLab.Groups)
	if err != nil {
		return cfg, nil, st.redactor.RedactError(err)
	}
	return cfg, svc, nil
}

// projectConfig applies the per-project overrides for projectPath. They
// cover review/checkout/publish/gate only, so the resolved instance's gitlab
// settings are kept.
func (st *state) projectConfig(cfg config.Config, projectPath string) config.Config {
	projectCfg, err := st.loaded.ForProject(projectPath)
	if err != nil {
		return cfg
	}
	projectCfg.GitLab = cfg.GitLab
	return projectCfg
}

func reviewStore() *resultstore.Store {
	return resultstore.NewStore(filepath.Join(config.DefaultStateDir(), "reviews"))
}

func checkOutput(output string) error {
	if output != "text" && output != "json" {
		return fmt.Errorf("--output must be text or json (got %q)", output)
	}
	return nil
}

func addOutputFlag(cmd *cobra.Command, output *string) {
	cmd.Flags().StringVar(output, "output", "text", "result format on stdout: text|json")
}

func printJSON(cmd *cobra.Command, v any) error {
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// loadRecord loads the stored review named by name, or the MR's newest one
// when name is empty. The record must belong to ref, so a record name can
// never act on a different MR.
func loadRecord(store *resultstore.Store, ref, name string) (resultstore.Record, string, error) {
	var rec resultstore.Record
	if name == "" {
		entries, err := store.List(ref)
		if err != nil {
			return rec, "", err
		}
		if len(entries) == 0 {
			return rec, "", fmt.Errorf("no stored review for %s: run `gitlab-reviewer review %s` first", ref, ref)
		}
		name = filepath.Base(entries[0].Path)
	}
	path, err := store.Resolve(name)
	if err != nil {
		return rec, "", err
	}
	rec, err = store.Load(path)
	if errors.Is(err, fs.ErrNotExist) {
		return rec, "", fmt.Errorf("no stored review %q (see `gitlab-reviewer review list %s`)", name, ref)
	}
	if err != nil {
		return rec, "", err
	}
	if rec.Ref != ref {
		return rec, "", fmt.Errorf("stored review %q belongs to %s, not %s", name, rec.Ref, ref)
	}
	return rec, name, nil
}

// reviewEntry is the JSON summary of one stored review.
type reviewEntry struct {
	Record  string             `json:"record"`
	Path    string             `json:"path"`
	Title   string             `json:"title"`
	Started time.Time          `json:"started"`
	Stage   string             `json:"stage"`
	Counts  resultstore.Counts `json:"counts"`
}

func newReviewEntry(e resultstore.Entry) reviewEntry {
	return reviewEntry{
		Record:  filepath.Base(e.Path),
		Path:    e.Path,
		Title:   e.Title,
		Started: e.Started,
		Stage:   e.Stage(),
		Counts:  e.Counts,
	}
}

func countsText(c resultstore.Counts) string {
	return fmt.Sprintf("%d finding(s): %d accepted, %d pending, %d rejected, %d published",
		c.Findings, c.Accepted, c.Pending, c.Rejected, c.Published)
}
