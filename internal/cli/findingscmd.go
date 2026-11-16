package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review/publisher"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review/resultstore"
)

// addStoredReviewCmds attaches the stored-review subcommands to the review
// command: reading past reviews, triaging their findings, and publishing
// the accepted ones.
func addStoredReviewCmds(review *cobra.Command, st *state) {
	review.AddCommand(
		newReviewListCmd(),
		newReviewShowCmd(),
		newTriageCmd("accept", "accept "+mrArg+" <finding-id>...", "Accept findings for publishing", cobra.MinimumNArgs(2)),
		newTriageCmd("reject", "reject "+mrArg+" <finding-id>...", "Reject findings so they are never published", cobra.MinimumNArgs(2)),
		newTriageCmd("accept-all", "accept-all "+mrArg, "Accept every pending finding", cobra.ExactArgs(1)),
		newTriageCmd("edit", "edit "+mrArg+" <finding-id> --body <markdown>", "Replace a finding's comment body", cobra.ExactArgs(2)),
		newReviewPublishCmd(st),
		newReviewSubmitCmd(st),
	)
}

func newReviewListCmd() *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:   "list " + mrArg,
		Short: "List the stored reviews of a merge request, newest first",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkOutput(output); err != nil {
				return err
			}
			target, err := parseTarget(args[0])
			if err != nil {
				return err
			}
			entries, err := reviewStore().List(target.ref())
			if err != nil {
				return err
			}
			reviews := make([]reviewEntry, 0, len(entries))
			for _, e := range entries {
				reviews = append(reviews, newReviewEntry(e))
			}
			if output == "json" {
				return printJSON(cmd, struct {
					Ref     string        `json:"ref"`
					Reviews []reviewEntry `json:"reviews"`
				}{target.ref(), reviews})
			}
			w := cmd.OutOrStdout()
			if len(reviews) == 0 {
				_, _ = fmt.Fprintf(w, "no stored reviews for %s\n", target.ref())
			}
			for _, r := range reviews {
				_, _ = fmt.Fprintf(w, "%s  %s  %s, %s\n", r.Record, r.Started.Local().Format(time.DateTime), r.Stage, countsText(r.Counts))
			}
			return nil
		},
	}
	addOutputFlag(cmd, &output)
	return cmd
}

// storedReview is the JSON shape of one stored review with its findings.
type storedReview struct {
	Name   string             `json:"record"`
	Path   string             `json:"path"`
	Stage  string             `json:"stage"`
	Counts resultstore.Counts `json:"counts"`
	resultstore.Record
}

func newStoredReview(store *resultstore.Store, name string, rec resultstore.Record) storedReview {
	counts := resultstore.CountFindings(rec.Findings)
	return storedReview{Name: name, Path: store.Path(rec), Stage: counts.Stage(), Counts: counts, Record: rec}
}

func newReviewShowCmd() *cobra.Command {
	var record, output string
	cmd := &cobra.Command{
		Use:   "show " + mrArg,
		Short: "Show a stored review with its findings, ids, and states",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkOutput(output); err != nil {
				return err
			}
			target, err := parseTarget(args[0])
			if err != nil {
				return err
			}
			store := reviewStore()
			rec, name, err := loadRecord(store, target.ref(), record)
			if err != nil {
				return err
			}
			out := newStoredReview(store, name, rec)
			if output == "json" {
				return printJSON(cmd, out)
			}
			w := cmd.OutOrStdout()
			_, _ = fmt.Fprintf(w, "%s  %s  %s\n", out.Name, out.Ref, out.Started.Local().Format(time.DateTime))
			_, _ = fmt.Fprintf(w, "%s, %s\n", out.Stage, countsText(out.Counts))
			if rec.HeadSHA != "" {
				_, _ = fmt.Fprintf(w, "head: %s\n", rec.HeadSHA)
			}
			if rec.Summary != "" {
				_, _ = fmt.Fprintf(w, "\n%s\n", rec.Summary)
			}
			for _, f := range rec.Findings {
				_, _ = fmt.Fprintf(w, "\n[%s] %s  %s %s  %s\n  %s\n", f.State, f.ID, f.Severity, findingRef(f), f.Title, strings.ReplaceAll(f.Body, "\n", "\n  "))
			}
			return nil
		},
	}
	addRecordFlag(cmd, &record)
	addOutputFlag(cmd, &output)
	return cmd
}

func addRecordFlag(cmd *cobra.Command, record *string) {
	cmd.Flags().StringVar(record, "record", "", "stored review to use, by its record name from review list (default: the newest)")
}

func newTriageCmd(action, use, short string, args cobra.PositionalArgs) *cobra.Command {
	var record, body, output string
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Long: short + ".\n\nThe change is saved to the stored review (the newest unless --record\n" +
			"names one), exactly as triaging in the TUI or GUI does. Nothing is posted\n" +
			"to GitLab until review publish.",
		Args: args,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkOutput(output); err != nil {
				return err
			}
			target, err := parseTarget(args[0])
			if err != nil {
				return err
			}
			store := reviewStore()
			rec, name, err := loadRecord(store, target.ref(), record)
			if err != nil {
				return err
			}
			ids := args[1:]
			if action == "accept-all" {
				ids = []string{""}
			}
			for _, id := range ids {
				if err := rec.Triage(action, id, body); err != nil {
					return err
				}
			}
			if err := store.Save(rec); err != nil {
				return fmt.Errorf("saving %s: %w", name, err)
			}
			out := newStoredReview(store, name, rec)
			if output == "json" {
				return printJSON(cmd, out)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s: %s, %s\n", name, out.Stage, countsText(out.Counts))
			return nil
		},
	}
	addRecordFlag(cmd, &record)
	if action == "edit" {
		cmd.Flags().StringVar(&body, "body", "", "new comment body (markdown)")
		_ = cmd.MarkFlagRequired("body")
	}
	addOutputFlag(cmd, &output)
	return cmd
}

type publishItem struct {
	ID    string              `json:"id"`
	Title string              `json:"title"`
	File  string              `json:"file,omitempty"`
	State review.FindingState `json:"state"`
	Error string              `json:"error,omitempty"`
}

type publishOutput struct {
	Ref          string        `json:"ref"`
	Record       string        `json:"record"`
	Mode         string        `json:"mode"`
	Results      []publishItem `json:"results"`
	Inline       int           `json:"inline"`
	Notes        int           `json:"notes"`
	Failed       int           `json:"failed"`
	DraftPending bool          `json:"draft_pending"`
}

func newReviewPublishCmd(st *state) *cobra.Command {
	var record, mode, output string
	cmd := &cobra.Command{
		Use:   "publish " + mrArg,
		Short: "Publish the accepted findings of a stored review to the MR",
		Long: "publish posts only the accepted findings of a stored review (the newest\n" +
			"unless --record names one) through the same publisher as the TUI and GUI,\n" +
			"skipping findings that restate a comment already on the MR, and records\n" +
			"each finding's outcome back into the review. --mode defaults to\n" +
			"publish.mode. In draft mode the comments wait in a pending draft review\n" +
			"until `review submit`. Exits non-zero when any finding failed to post.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkOutput(output); err != nil {
				return err
			}
			if mode != "" && mode != "draft" && mode != "immediate" {
				return fmt.Errorf("--mode must be draft or immediate (got %q)", mode)
			}
			target, err := parseTarget(args[0])
			if err != nil {
				return err
			}
			cfg, svc, err := st.connect(target.host)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			detail, err := svc.GetMergeRequest(ctx, target.project, target.iid)
			if err != nil {
				return st.redactor.RedactError(fmt.Errorf("fetching %s: %w", target.ref(), err))
			}
			store := reviewStore()
			rec, name, err := loadRecord(store, detail.Ref(), record)
			if err != nil {
				return err
			}
			diffs, err := svc.ListDiffs(ctx, detail.Project(), detail.IID)
			if err != nil {
				return st.redactor.RedactError(fmt.Errorf("fetching diffs for %s: %w", detail.Ref(), err))
			}

			stderr := cmd.ErrOrStderr()
			warn := func(msg string) { _, _ = fmt.Fprintln(stderr, "warning:", st.redactor.Redact(msg)) }
			projectCfg := st.projectConfig(cfg, detail.ProjectPath)
			if mode == "" {
				mode = projectCfg.Publish.Mode
			}
			pub, tmplErr := publisher.New(svc, *detail, diffs, projectCfg.Publish)
			if tmplErr != nil {
				warn(tmplErr.Error())
			}
			pub.Draft = mode == "draft"

			var accepted []review.Finding
			for _, f := range rec.Findings {
				if f.State == review.StateAccepted {
					accepted = append(accepted, f)
				}
			}
			out := publishOutput{Ref: detail.Ref(), Record: name, Mode: mode, Results: []publishItem{}}
			if len(accepted) > 0 {
				if err := pub.LoadExisting(ctx); err != nil {
					warn("duplicate detection unavailable: " + err.Error())
				}
				results := pub.PublishAll(ctx, accepted)
				out.Inline, out.Notes, out.Failed = publisher.Tally(results)
				for _, r := range results {
					item := publishItem{ID: r.Finding.ID, Title: r.Finding.Title, File: r.Finding.File, State: r.State}
					if r.Err != nil {
						item.Error = st.redactor.Redact(r.Err.Error())
					}
					out.Results = append(out.Results, item)
					rec.SetState(r.Finding.ID, r.State)
				}
				out.DraftPending = pub.Draft && out.Inline+out.Notes > 0
				if err := store.Save(rec); err != nil {
					return fmt.Errorf("saving %s after publishing (states on GitLab are not recorded locally): %w", name, err)
				}
			}

			if output == "json" {
				if err := printJSON(cmd, out); err != nil {
					return err
				}
			} else {
				writePublishText(cmd, out)
			}
			if out.Failed > 0 {
				return fmt.Errorf("%d finding(s) failed to publish", out.Failed)
			}
			return nil
		},
	}
	addRecordFlag(cmd, &record)
	cmd.Flags().StringVar(&mode, "mode", "", "draft (a pending review, see review submit) or immediate (default: publish.mode)")
	addOutputFlag(cmd, &output)
	return cmd
}

func writePublishText(cmd *cobra.Command, out publishOutput) {
	w := cmd.OutOrStdout()
	if len(out.Results) == 0 {
		_, _ = fmt.Fprintf(w, "%s has no accepted findings to publish\n", out.Record)
		return
	}
	for _, r := range out.Results {
		line := fmt.Sprintf("  [%s] %s  %s", r.State, r.ID, r.Title)
		if r.Error != "" {
			line += "  (" + r.Error + ")"
		}
		_, _ = fmt.Fprintln(w, line)
	}
	_, _ = fmt.Fprintf(w, "published (%s): %d inline, %d as notes, %d failed\n", out.Mode, out.Inline, out.Notes, out.Failed)
	if out.DraftPending {
		_, _ = fmt.Fprintf(w, "draft review pending: run `gitlab-reviewer review submit %s` to publish it\n", out.Ref)
	}
}

func newReviewSubmitCmd(st *state) *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:   "submit " + mrArg,
		Short: "Submit the MR's pending draft review, publishing its draft comments",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkOutput(output); err != nil {
				return err
			}
			target, err := parseTarget(args[0])
			if err != nil {
				return err
			}
			cfg, svc, err := st.connect(target.host)
			if err != nil {
				return err
			}
			detail, err := svc.GetMergeRequest(cmd.Context(), target.project, target.iid)
			if err != nil {
				return st.redactor.RedactError(fmt.Errorf("fetching %s: %w", target.ref(), err))
			}
			// The body template only matters when posting; a bad one must not
			// block submitting drafts that are already written.
			pub, _ := publisher.New(svc, *detail, nil, st.projectConfig(cfg, detail.ProjectPath).Publish)
			ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
			defer cancel()
			if err := pub.PublishReview(ctx); err != nil {
				return st.redactor.RedactError(fmt.Errorf("publishing draft review: %w", err))
			}
			if output == "json" {
				return printJSON(cmd, struct {
					Ref       string `json:"ref"`
					Submitted bool   `json:"submitted"`
				}{detail.Ref(), true})
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "submitted the draft review on %s\n", detail.Ref())
			return nil
		},
	}
	addOutputFlag(cmd, &output)
	return cmd
}
