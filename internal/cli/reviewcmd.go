package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/checkout"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/config"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review/agents"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review/claudecli"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review/publisher"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review/resultstore"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review/runlog"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review/runner"
)

// newReviewCmd runs one review non-interactively: the same pipeline as the
// TUI and GUI (checkout, agent passes, result storage), driven from a single
// command for CI jobs and scripting. Progress streams to stderr; the result
// goes to stdout as text or JSON.
func newReviewCmd(st *state) *cobra.Command {
	var (
		publishMode string
		output      string
		full        bool
	)
	cmd := &cobra.Command{
		Use:   "review <project!iid | MR URL>",
		Short: "Run one review non-interactively (for CI and scripting)",
		Long: "review runs the AI review for one merge request without the TUI: it\n" +
			"checks the MR branch out, runs the configured review agents, stores the\n" +
			"result (reopenable later in the TUI or GUI), and optionally publishes\n" +
			"the findings. Progress streams to stderr; the outcome is written to\n" +
			"stdout as text or JSON. The merge request is named as project!iid\n" +
			"(e.g. mygroup/myapp!123 — in GitLab CI that is\n" +
			"\"$CI_PROJECT_PATH!$CI_MERGE_REQUEST_IID\") or as its web URL, whose\n" +
			"host also selects the matching gitlab.instances entry.\n\n" +
			"By default nothing is posted to GitLab (--publish none): findings are\n" +
			"only stored and reported. --publish immediate posts every finding as\n" +
			"it resolves; --publish draft collects them into a draft review and\n" +
			"publishes it in one action.\n\n" +
			"When the MR already has a stored review, the run is incremental: only\n" +
			"the changes pushed since the last reviewed commit go through the review\n" +
			"passes, and the previous findings — including their accepted, rejected,\n" +
			"and published states — carry forward (findings whose code changed are\n" +
			"dropped). It falls back to a full review after a rebase or when the\n" +
			"last reviewed commit is unreachable. Pass --full to scan the entire\n" +
			"diff again regardless.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runHeadlessReview(cmd, st, args[0], publishMode, output, full)
		},
	}
	cmd.Flags().StringVar(&publishMode, "publish", "none", "publish findings to the MR: none|draft|immediate")
	cmd.Flags().StringVar(&output, "output", "text", "result format on stdout: text|json")
	cmd.Flags().BoolVar(&full, "full", false, "review the whole diff even when a stored review allows an incremental run")
	return cmd
}

func runHeadlessReview(cmd *cobra.Command, st *state, ref, publishMode, output string, full bool) error {
	switch publishMode {
	case "none", "draft", "immediate":
	default:
		return fmt.Errorf("--publish must be none, draft, or immediate (got %q)", publishMode)
	}
	switch output {
	case "text", "json":
	default:
		return fmt.Errorf("--output must be text or json (got %q)", output)
	}
	project, iid, host, err := parseMRTarget(ref)
	if err != nil {
		return err
	}

	cfg := st.loaded.Config
	if err := cfg.Validate(); err != nil {
		return err
	}
	// Never prompt: this command must work without a terminal. A URL target
	// also names the GitLab host, which selects the matching instance.
	cfg, err = resolveInstanceForHost(cfg, host)
	if err != nil {
		return err
	}
	if err := cfg.ValidateGitLab(); err != nil {
		return err
	}
	// Full template check (field names included) — cfg.Validate only covers
	// syntax.
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

	ctx := cmd.Context()
	detail, err := svc.GetMergeRequest(ctx, project, iid)
	if err != nil {
		return st.redactor.RedactError(fmt.Errorf("fetching %s: %w", ref, err))
	}
	diffs, err := svc.ListDiffs(ctx, project, iid)
	if err != nil {
		return st.redactor.RedactError(fmt.Errorf("fetching diffs for %s: %w", ref, err))
	}
	commits, _ := svc.ListCommits(ctx, project, iid) // best-effort, like the GUI

	projectCfg, err := st.loaded.ForProject(detail.ProjectPath)
	if err != nil {
		projectCfg = cfg
	} else {
		// Per-project overrides cover review/checkout/publish/gate only; keep the
		// resolved instance's gitlab settings.
		projectCfg.GitLab = cfg.GitLab
	}

	stderr := cmd.ErrOrStderr()
	emit := func(line string) { _, _ = fmt.Fprintln(stderr, st.redactor.Redact(line)) }

	results := resultstore.NewStore(reviewsDir)
	r := runner.Runner{
		Cfg:      projectCfg,
		Svc:      svc,
		Reviewer: reviewer,
		Checkout: func(ctx context.Context, mr gitlabx.MRDetail, progress func(string)) (string, func(context.Context) error, error) {
			co, err := manager.Ensure(ctx, mr, progress)
			if err != nil {
				return "", nil, st.redactor.RedactError(err)
			}
			return co.Path, co.Close, nil
		},
		Catalog:     agents.NewCatalog(config.DefaultAgentsDir()),
		Logs:        runlog.NewStore(reviewsDir),
		Results:     results,
		Incremental: !full,
	}
	out := r.Run(ctx, *detail, diffs, commits, emit)

	// Enforce the clone-cache budget before exiting; unlike the long-lived
	// frontends there is no later moment, so run it synchronously.
	if res, err := manager.EvictIfNeeded(context.Background()); err != nil {
		slog.Warn("cache eviction failed", "error", err)
	} else if len(res.Removed) > 0 {
		slog.Info("evicted cached clones", "count", len(res.Removed), "freed_bytes", res.FreedBytes)
	}

	if out.Err != nil {
		return st.redactor.RedactError(out.Err)
	}
	rec := out.Rec

	// Headless has no curation step: publish everything the review produced.
	var pubFailed int
	if publishMode != "none" && len(rec.Findings) > 0 {
		pubFailed = publishHeadless(ctx, svc, *detail, diffs, projectCfg, rec, publishMode == "draft", emit)
		if err := results.Save(*rec); err != nil {
			emit("warning: could not store the review result: " + err.Error())
		}
	}

	gate := gateOutcome(projectCfg.Gate, rec.Findings)

	if output == "json" {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		if err := enc.Encode(struct {
			resultstore.Record
			RecordPath string      `json:"record_path,omitempty"`
			Gate       *gateReport `json:"gate,omitempty"`
		}{*rec, results.Path(*rec), gate}); err != nil {
			return err
		}
	} else {
		writeTextSummary(cmd, rec, results.Path(*rec), publishMode, gate)
	}

	if pubFailed > 0 {
		return fmt.Errorf("%d finding(s) failed to publish", pubFailed)
	}
	if gate != nil && !gate.Passed {
		return &exitError{code: gateExitCode, msg: fmt.Sprintf(
			"gate failed: %d finding(s) at or above %s (gate.min_severity)", gate.Blocking, gate.MinSeverity)}
	}
	return nil
}

// gateExitCode distinguishes "the review found blocking findings" (the gate)
// from ordinary failures (exit 1), so CI pipelines can tell them apart.
const gateExitCode = 2

// gateReport is the gate section of the review command's output.
type gateReport struct {
	MinSeverity string `json:"min_severity"`
	Blocking    int    `json:"blocking"`
	Passed      bool   `json:"passed"`
}

// gateOutcome evaluates the severity gate over a completed review's
// findings; nil when no gate is configured.
func gateOutcome(gate config.Gate, findings []review.Finding) *gateReport {
	if !gate.Enabled() {
		return nil
	}
	blocking := review.CountBlocking(findings, review.Severity(gate.MinSeverity))
	return &gateReport{MinSeverity: gate.MinSeverity, Blocking: blocking, Passed: blocking == 0}
}

// publishHeadless posts every publishable finding through the shared
// publisher, updating each finding's state in place, and returns how many
// failed. Findings carried forward from a previous review in a decided
// state (already published, fell back to a note, or rejected) are left
// alone, so an incremental run only posts what is new. In draft mode the
// pending review is published in one action at the end — the draft grouping
// is used for atomicity, not for later human confirmation; use
// --publish none when a human should stay in the loop.
func publishHeadless(ctx context.Context, svc gitlabx.Service, detail gitlabx.MRDetail, diffs []gitlabx.FileDiff, cfg config.Config, rec *resultstore.Record, draft bool, emit func(string)) int {
	pub, err := publisher.New(svc, detail, diffs, cfg.Publish)
	if err != nil {
		emit("warning: " + err.Error())
	}
	pub.Draft = draft

	failed, posted := 0, 0
	// Sequential on purpose: GitLab rate limits are unkind to bursts.
	for i, f := range rec.Findings {
		switch f.State {
		case review.StatePublished, review.StateFellBack, review.StateRejected:
			continue
		}
		postCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		state, err := pub.PublishOne(postCtx, f)
		cancel()
		rec.Findings[i].State = state
		if err != nil {
			failed++
			emit(fmt.Sprintf("publish failed: %s: %v", findingRef(f), err))
			continue
		}
		if state != review.StateBelowThreshold {
			posted++
		}
	}
	if draft && posted > 0 {
		pubCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		if err := pub.PublishReview(pubCtx); err != nil {
			failed++
			emit("publishing draft review: " + err.Error())
		}
	}
	return failed
}

func writeTextSummary(cmd *cobra.Command, rec *resultstore.Record, recordPath, publishMode string, gate *gateReport) {
	w := cmd.OutOrStdout()
	_, _ = fmt.Fprintf(w, "review complete: %d finding(s)%s\n", len(rec.Findings), severityBreakdown(rec.Findings))
	if gate != nil {
		if gate.Passed {
			_, _ = fmt.Fprintf(w, "gate: passed (no findings at or above %s)\n", gate.MinSeverity)
		} else {
			_, _ = fmt.Fprintf(w, "gate: failed — %d finding(s) at or above %s\n", gate.Blocking, gate.MinSeverity)
		}
	}
	if rec.Summary != "" {
		_, _ = fmt.Fprintln(w, rec.Summary)
	}
	for _, warn := range rec.Warnings {
		_, _ = fmt.Fprintln(w, "warning:", warn)
	}
	if rec.CostUSD > 0 {
		_, _ = fmt.Fprintf(w, "cost: $%.2f\n", rec.CostUSD)
	}
	if len(rec.Findings) > 0 {
		_, _ = fmt.Fprintln(w)
		for _, f := range rec.Findings {
			_, _ = fmt.Fprintf(w, "  [%s] %s — %s\n", f.State, findingRef(f), f.Title)
		}
		_, _ = fmt.Fprintln(w)
	}
	switch {
	case publishMode == "none" && len(rec.Findings) > 0:
		_, _ = fmt.Fprintln(w, "not published (--publish none)")
	case publishMode != "none":
		inline, notes := 0, 0
		for _, f := range rec.Findings {
			switch {
			case f.State == review.StatePublished && f.File == "":
				notes++
			case f.State == review.StatePublished:
				inline++
			case f.State == review.StateFellBack:
				notes++
			}
		}
		_, _ = fmt.Fprintf(w, "published: %d inline, %d as notes\n", inline, notes)
	}
	if recordPath != "" {
		_, _ = fmt.Fprintf(w, "stored: %s (reopen it from the MR's past reviews in the TUI or GUI)\n", recordPath)
	}
}

// severityBreakdown renders " — 1 critical, 2 major" for the summary line,
// strongest first; empty when there are no findings.
func severityBreakdown(findings []review.Finding) string {
	counts := map[review.Severity]int{}
	for _, f := range findings {
		counts[f.Severity]++
	}
	var parts []string
	for _, s := range []review.Severity{review.SeverityCritical, review.SeverityMajor, review.SeverityMinor, review.SeverityInfo} {
		if n := counts[s]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, s))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return " — " + strings.Join(parts, ", ")
}

// findingRef locates a finding for progress and summary lines.
func findingRef(f review.Finding) string {
	if f.File == "" {
		return "MR-level comment"
	}
	if f.Line.NewLine != nil {
		return fmt.Sprintf("%s:%d", f.File, *f.Line.NewLine)
	}
	if f.Line.OldLine != nil {
		return fmt.Sprintf("%s:%d (old)", f.File, *f.Line.OldLine)
	}
	return f.File
}

// parseMRTarget parses the merge request argument: either a project!iid
// reference (e.g. mygroup/myapp!123) or the MR's web URL (e.g.
// https://gitlab.example.com/mygroup/myapp/-/merge_requests/123, with any
// trailing tab, query, or anchor tolerated). For a URL the host is returned
// too, so the caller can select the matching GitLab instance.
func parseMRTarget(ref string) (project string, iid int64, host string, err error) {
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		return parseMRURL(ref)
	}
	project, iid, err = parseMRRef(ref)
	return project, iid, "", err
}

// parseMRRef parses a merge request reference of the form project!iid,
// e.g. mygroup/myapp!123.
func parseMRRef(ref string) (project string, iid int64, err error) {
	project, iidStr, found := strings.Cut(ref, "!")
	if !found || project == "" || iidStr == "" {
		return "", 0, fmt.Errorf("invalid merge request reference %q: expected project!iid, e.g. mygroup/myapp!123", ref)
	}
	iid, err = strconv.ParseInt(iidStr, 10, 64)
	if err != nil || iid <= 0 {
		return "", 0, fmt.Errorf("invalid merge request IID in %q: expected project!iid, e.g. mygroup/myapp!123", ref)
	}
	return project, iid, nil
}

// parseMRURL extracts project, IID, and host from a merge request web URL.
func parseMRURL(ref string) (project string, iid int64, host string, err error) {
	u, err := url.Parse(ref)
	if err != nil {
		return "", 0, "", fmt.Errorf("invalid merge request URL %q: %w", ref, err)
	}
	const marker = "/-/merge_requests/"
	before, after, found := strings.Cut(u.Path, marker)
	if !found {
		return "", 0, "", fmt.Errorf("invalid merge request URL %q: expected …%s<iid>", ref, marker)
	}
	project = strings.Trim(before, "/")
	// Tolerate trailing path segments like /diffs or /commits; query and
	// fragment are already excluded from u.Path.
	iidStr, _, _ := strings.Cut(after, "/")
	iid, err = strconv.ParseInt(iidStr, 10, 64)
	if project == "" || err != nil || iid <= 0 {
		return "", 0, "", fmt.Errorf("invalid merge request URL %q: expected …/<project>%s<iid>", ref, marker)
	}
	return project, iid, strings.ToLower(u.Host), nil
}

// resolveInstanceForHost narrows the configuration to one GitLab instance
// without ever prompting. With no host (a project!iid target) it applies the
// explicit selection rules; with a host (a URL target) the instance whose
// base_url matches is selected, so a URL is enough to pick the right
// instance — and can never run against the wrong one.
func resolveInstanceForHost(cfg config.Config, host string) (config.Config, error) {
	if host == "" {
		return resolveInstanceHeadless(cfg)
	}
	instances := cfg.GitLab.Instances
	if len(instances) == 0 {
		if h := urlHost(cfg.GitLab.BaseURL); h != host {
			return cfg, fmt.Errorf("the merge request URL host %q does not match gitlab.base_url (%s)", host, cfg.GitLab.BaseURL)
		}
		return cfg, nil
	}
	// Prefer the explicit selection (--instance / gitlab.default_instance)
	// when it agrees with the URL; it may carry the intended token when
	// several instances share a host.
	if name := cfg.GitLab.DefaultInstance; name != "" {
		for _, inst := range instances {
			if inst.Name == name && urlHost(inst.BaseURL) == host {
				return cfg.WithInstance(name)
			}
		}
	}
	var matches []string
	for _, inst := range instances {
		if urlHost(inst.BaseURL) == host {
			matches = append(matches, inst.Name)
		}
	}
	switch len(matches) {
	case 1:
		return cfg.WithInstance(matches[0])
	case 0:
		return cfg, fmt.Errorf("no configured gitlab instance matches the merge request URL host %q (have %s)",
			host, strings.Join(cfg.InstanceNames(), ", "))
	default:
		return cfg, fmt.Errorf("%d gitlab instances match host %q (%s): pass --instance or set gitlab.default_instance",
			len(matches), host, strings.Join(matches, ", "))
	}
}

// urlHost returns the lowercased host (with port, when present) of a URL,
// or "" when it does not parse.
func urlHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Host)
}

// resolveInstanceHeadless narrows the configuration to one GitLab instance
// like resolveInstance, but never prompts: with several instances configured
// it requires --instance or gitlab.default_instance even on a terminal.
func resolveInstanceHeadless(cfg config.Config) (config.Config, error) {
	instances := cfg.GitLab.Instances
	if len(instances) == 0 {
		return cfg, nil
	}
	name := cfg.GitLab.DefaultInstance
	if name == "" && len(instances) == 1 {
		name = instances[0].Name
	}
	if name == "" {
		return cfg, fmt.Errorf("multiple GitLab instances configured (%s): pass --instance or set gitlab.default_instance",
			strings.Join(cfg.InstanceNames(), ", "))
	}
	return cfg.WithInstance(name)
}
