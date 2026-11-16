package cli

import (
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review/resultstore"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review/runner"
)

const mrArg = "<project!iid | MR URL>"

func newMRCmd(st *state) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mr",
		Short: "List, inspect, and approve merge requests without the TUI",
		Long: "mr covers the merge request side of a headless review: list MRs with the\n" +
			"same filters as the GUI, inspect one, and approve or unapprove it. Every\n" +
			"subcommand supports --output json for agents and scripts, and never prompts.",
	}
	cmd.AddCommand(newMRListCmd(st), newMRShowCmd(st), newMRApproveCmd(st), newMRUnapproveCmd(st))
	return cmd
}

// mrItem is the JSON shape of one merge request.
type mrItem struct {
	Ref          string       `json:"ref"`
	Project      string       `json:"project"`
	IID          int64        `json:"iid"`
	Title        string       `json:"title"`
	State        string       `json:"state"`
	Draft        bool         `json:"draft"`
	Author       string       `json:"author"`
	SourceBranch string       `json:"source_branch"`
	TargetBranch string       `json:"target_branch"`
	HeadSHA      string       `json:"head_sha"`
	WebURL       string       `json:"web_url"`
	UpdatedAt    time.Time    `json:"updated_at"`
	LatestReview *reviewEntry `json:"latest_review"`
}

func newMRItem(m gitlabx.MRSummary, latest map[string]resultstore.Entry) mrItem {
	item := mrItem{
		Ref:          m.Ref(),
		Project:      m.ProjectPath,
		IID:          m.IID,
		Title:        m.Title,
		State:        m.State,
		Draft:        m.Draft,
		Author:       m.Author,
		SourceBranch: m.SourceBranch,
		TargetBranch: m.TargetBranch,
		HeadSHA:      m.HeadSHA,
		WebURL:       m.WebURL,
		UpdatedAt:    m.UpdatedAt,
	}
	if e, ok := latest[m.Ref()]; ok && m.ProjectPath != "" {
		entry := newReviewEntry(e)
		item.LatestReview = &entry
	}
	return item
}

func newMRListCmd(st *state) *cobra.Command {
	var (
		filter gitlabx.MRFilter
		page   int
		output string
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List merge requests with the GUI's filters",
		Long: "list prints merge requests in the configured scope (gitlab.projects and\n" +
			"gitlab.groups), newest activity first, with the latest stored review of\n" +
			"each. --project and --group replace the configured scope for this call.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := checkOutput(output); err != nil {
				return err
			}
			switch filter.State {
			case "opened", "merged", "closed", "all":
			default:
				return fmt.Errorf("--state must be opened, merged, closed, or all (got %q)", filter.State)
			}
			if page < 1 {
				return fmt.Errorf("--page must be at least 1 (got %d)", page)
			}
			cfg, svc, err := st.connect("")
			if err != nil {
				return err
			}
			if cmd.Flags().Changed("project") || cmd.Flags().Changed("group") {
				filter.Projects, _ = cmd.Flags().GetStringArray("project")
				filter.Groups, _ = cmd.Flags().GetStringArray("group")
			}
			if len(filter.Projects) == 0 && len(filter.Groups) == 0 && len(cfg.GitLab.Projects) == 0 && len(cfg.GitLab.Groups) == 0 {
				return errors.New("no projects or groups to list: pass --project or --group, or set gitlab.projects / gitlab.groups")
			}
			mrs, hasMore, err := svc.ListOpenMergeRequests(cmd.Context(), filter, gitlabx.Page{Number: page, PerPage: cfg.GitLab.PerPage})
			if err != nil {
				return st.redactor.RedactError(err)
			}
			latest, _ := reviewStore().LatestByRef()
			items := make([]mrItem, 0, len(mrs))
			for _, m := range mrs {
				items = append(items, newMRItem(m, latest))
			}
			if output == "json" {
				return printJSON(cmd, struct {
					Page          int      `json:"page"`
					HasMore       bool     `json:"has_more"`
					MergeRequests []mrItem `json:"merge_requests"`
				}{page, hasMore, items})
			}
			w := cmd.OutOrStdout()
			for _, m := range items {
				draft := ""
				if m.Draft {
					draft = " [draft]"
				}
				reviewed := "not reviewed"
				if m.LatestReview != nil {
					reviewed = fmt.Sprintf("review: %s, %s", m.LatestReview.Stage, countsText(m.LatestReview.Counts))
				}
				_, _ = fmt.Fprintf(w, "%s%s  %s\n  @%s  %s -> %s  (%s)\n", m.Ref, draft, m.Title, m.Author, m.SourceBranch, m.TargetBranch, reviewed)
			}
			if len(items) == 0 {
				_, _ = fmt.Fprintln(w, "no merge requests")
			}
			if hasMore {
				_, _ = fmt.Fprintf(w, "more on --page %d\n", page+1)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&filter.State, "state", "opened", "merge request state: opened|merged|closed|all")
	cmd.Flags().StringVar(&filter.Search, "search", "", "search titles and descriptions")
	cmd.Flags().StringVar(&filter.AuthorUsername, "author", "", "author username")
	cmd.Flags().StringVar(&filter.TargetBranch, "target", "", "target branch")
	cmd.Flags().IntVar(&page, "page", 1, "page number")
	addOutputFlag(cmd, &output)
	return cmd
}

type mrShowOutput struct {
	mrItem
	Description       string          `json:"description"`
	MergeStatus       string          `json:"merge_status"`
	HasConflicts      bool            `json:"has_conflicts"`
	DivergedCommits   int64           `json:"diverged_commits"`
	NeedsRebase       bool            `json:"needs_rebase"`
	RebaseWarning     string          `json:"rebase_warning,omitempty"`
	Pipeline          *pipelineOutput `json:"pipeline"`
	Threads           *int            `json:"threads"`
	UnresolvedThreads *int            `json:"unresolved_threads"`
	Commits           []commitOutput  `json:"commits"`
}

type pipelineOutput struct {
	Status string `json:"status"`
	URL    string `json:"url"`
}

type commitOutput struct {
	ShortID string `json:"short_id"`
	Title   string `json:"title"`
}

func newMRShowCmd(st *state) *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:   "show " + mrArg,
		Short: "Show one merge request: detail, commits, pipeline, threads, rebase state",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkOutput(output); err != nil {
				return err
			}
			target, err := parseTarget(args[0])
			if err != nil {
				return err
			}
			_, svc, err := st.connect(target.host)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			detail, err := svc.GetMergeRequest(ctx, target.project, target.iid)
			if err != nil {
				return st.redactor.RedactError(fmt.Errorf("fetching %s: %w", target.ref(), err))
			}
			latest, _ := reviewStore().LatestByRef()
			out := mrShowOutput{
				mrItem:          newMRItem(detail.MRSummary, latest),
				Description:     detail.Description,
				MergeStatus:     detail.DetailedMergeStatus,
				HasConflicts:    detail.HasConflicts,
				DivergedCommits: detail.DivergedCommits,
				NeedsRebase:     detail.NeedsRebase(),
				Commits:         []commitOutput{},
			}
			if out.NeedsRebase {
				out.RebaseWarning = runner.RebaseWarning(*detail)
			}
			if detail.Pipeline != nil {
				out.Pipeline = &pipelineOutput{Status: detail.Pipeline.Status, URL: detail.Pipeline.WebURL}
			}
			stderr := cmd.ErrOrStderr()
			if commits, err := svc.ListCommits(ctx, target.project, target.iid); err != nil {
				_, _ = fmt.Fprintln(stderr, "warning: listing commits:", st.redactor.Redact(err.Error()))
			} else {
				for _, c := range commits {
					out.Commits = append(out.Commits, commitOutput{ShortID: c.ShortID, Title: c.Title})
				}
			}
			if discussions, err := svc.ListDiscussions(ctx, target.project, target.iid); err != nil {
				_, _ = fmt.Fprintln(stderr, "warning: listing discussions:", st.redactor.Redact(err.Error()))
			} else {
				threads, unresolved := gitlabx.CountThreads(discussions)
				out.Threads, out.UnresolvedThreads = &threads, &unresolved
			}
			if output == "json" {
				return printJSON(cmd, out)
			}
			writeMRShowText(cmd, out)
			return nil
		},
	}
	addOutputFlag(cmd, &output)
	return cmd
}

func writeMRShowText(cmd *cobra.Command, out mrShowOutput) {
	w := cmd.OutOrStdout()
	p := func(format string, a ...any) { _, _ = fmt.Fprintf(w, format+"\n", a...) }
	p("%s  %s", out.Ref, out.Title)
	if out.Draft {
		p("state: %s (draft)", out.State)
	} else {
		p("state: %s", out.State)
	}
	p("author: @%s", out.Author)
	p("branches: %s -> %s", out.SourceBranch, out.TargetBranch)
	p("head: %s", out.HeadSHA)
	p("url: %s", out.WebURL)
	if out.MergeStatus != "" {
		p("merge status: %s", out.MergeStatus)
	}
	if out.RebaseWarning != "" {
		p("warning: %s", out.RebaseWarning)
	}
	if out.Pipeline != nil {
		p("pipeline: %s (%s)", out.Pipeline.Status, out.Pipeline.URL)
	} else {
		p("pipeline: none")
	}
	if out.Threads != nil {
		p("threads: %d unresolved of %d", *out.UnresolvedThreads, *out.Threads)
	}
	if out.LatestReview != nil {
		p("latest review: %s (%s), %s", out.LatestReview.Record, out.LatestReview.Stage, countsText(out.LatestReview.Counts))
	} else {
		p("latest review: none")
	}
	if len(out.Commits) > 0 {
		p("commits:")
		for _, c := range out.Commits {
			p("  %s %s", c.ShortID, c.Title)
		}
	}
	if out.Description != "" {
		p("\n%s", out.Description)
	}
}

type approvalOutput struct {
	Ref    string `json:"ref"`
	Action string `json:"action"`
	SHA    string `json:"sha,omitempty"`
}

func newMRApproveCmd(st *state) *cobra.Command {
	var sha, output string
	cmd := &cobra.Command{
		Use:   "approve " + mrArg,
		Short: "Approve a merge request (honours gate.approvals: block)",
		Long: "approve approves the merge request as the configured GitLab user. The\n" +
			"approval is pinned to a head commit: --sha when given (for example the\n" +
			"head_sha of the review you acted on), otherwise the MR's current head,\n" +
			"so GitLab refuses it if the MR moved on. With gate.min_severity set and\n" +
			"gate.approvals: block, an MR whose latest stored review still has\n" +
			"blocking findings is not approved and the command exits 2.",
		Args: cobra.ExactArgs(1),
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
			gate := st.projectConfig(cfg, target.project).Gate
			if err := reviewStore().CheckApproval(gate, target.ref()); err != nil {
				return &exitError{code: gateExitCode, msg: err.Error()}
			}
			ctx := cmd.Context()
			if sha == "" {
				detail, err := svc.GetMergeRequest(ctx, target.project, target.iid)
				if err != nil {
					return st.redactor.RedactError(fmt.Errorf("fetching %s: %w", target.ref(), err))
				}
				sha = detail.HeadSHA
			}
			if err := svc.Approve(ctx, target.project, target.iid, sha); err != nil {
				return st.redactor.RedactError(err)
			}
			out := approvalOutput{Ref: target.ref(), Action: "approve", SHA: sha}
			if output == "json" {
				return printJSON(cmd, out)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "approved %s at %s\n", out.Ref, out.SHA)
			return nil
		},
	}
	cmd.Flags().StringVar(&sha, "sha", "", "head commit the approval applies to (default: the MR's current head)")
	addOutputFlag(cmd, &output)
	return cmd
}

func newMRUnapproveCmd(st *state) *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:   "unapprove " + mrArg,
		Short: "Remove your approval from a merge request",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkOutput(output); err != nil {
				return err
			}
			target, err := parseTarget(args[0])
			if err != nil {
				return err
			}
			_, svc, err := st.connect(target.host)
			if err != nil {
				return err
			}
			if err := svc.Unapprove(cmd.Context(), target.project, target.iid); err != nil {
				return st.redactor.RedactError(err)
			}
			out := approvalOutput{Ref: target.ref(), Action: "unapprove"}
			if output == "json" {
				return printJSON(cmd, out)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "removed approval from %s\n", out.Ref)
			return nil
		},
	}
	addOutputFlag(cmd, &output)
	return cmd
}
