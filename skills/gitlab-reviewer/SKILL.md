---
name: gitlab-reviewer
description: Drive the gitlab-reviewer CLI to review GitLab merge requests headless: list and inspect MRs, run the AI review, read and triage stored findings, publish accepted findings, submit the draft review, and approve. Use when asked to review a GitLab merge request (project!iid or an MR URL), to triage or publish gitlab-reviewer findings, or to use gitlab-reviewer from an agent or script.
---

# Reviewing GitLab merge requests with gitlab-reviewer

`gitlab-reviewer` reviews a GitLab merge request with Claude, stores the findings locally, and lets you curate and publish them. Every command below runs without a terminal UI, never prompts, and supports `--output json`. Always pass `--output json` and parse stdout; progress and warnings go to stderr.

## Prerequisites

Check these before anything else and stop with a clear message if one is missing:

- The `gitlab-reviewer` binary on `PATH` (`gitlab-reviewer version`). This plugin does not ship it. Install a prebuilt binary from https://github.com/RobertYoung/gitlab-reviewer-cli/releases or run `go install github.com/RobertYoung/gitlab-reviewer-cli/cmd/gitlab-reviewer@latest` (see https://robertyoung.github.io/gitlab-reviewer-cli/getting-started/). Ask the user before installing anything.
- The `claude` CLI 2.0 or newer and `git` on `PATH`. Only `gitlab-reviewer review <mr>` (the AI review itself) needs them.
- A GitLab token with `api` scope in `GITLAB_REVIEWER_GITLAB_TOKEN` (or `GITLAB_TOKEN`). Never pass the token with `--gitlab-token`, never echo it, and never write it to a file.
- The GitLab URL: `gitlab.base_url` in the settings file (`${XDG_CONFIG_HOME:-~/.config}/gitlab-reviewer/config.yaml`, or `--config <file>`), `GITLAB_REVIEWER_GITLAB_BASE_URL`, or `--gitlab-base-url`. With several `gitlab.instances` configured, pass `--instance <name>` or use the MR's web URL, whose host selects the instance.

`gitlab-reviewer config validate` checks the configuration and `gitlab-reviewer config show` prints it with secrets redacted.

## Naming a merge request

Every `<mr>` argument is `project!iid` (for example `mygroup/myapp!123`) or the MR's web URL (`https://gitlab.example.com/mygroup/myapp/-/merge_requests/123`). Quote it in the shell: `!` can trigger history expansion.

## The review loop

1. Find MRs: `gitlab-reviewer mr list --output json`. Filters: `--state opened|merged|closed|all`, `--search`, `--author`, `--target`, `--page`. `--project <path>` and `--group <path>` (repeatable) replace the configured scope. Each entry carries `latest_review` (null when never reviewed).
2. Inspect one: `gitlab-reviewer mr show <mr> --output json` gives `head_sha`, `pipeline`, `unresolved_threads`, `needs_rebase` and `rebase_warning`, `commits`, and `latest_review`.
3. Run the review: `gitlab-reviewer review <mr> --output json`. It checks the MR out, runs the review agents, and stores a record. It posts nothing unless `--publish draft|immediate` is given; keep the default `--publish none` so a human or the triage steps below decide what is posted. Useful flags: `--agents bug,security`, `--max-budget-usd <n>`, `--review-timeout <duration>`.
4. Read the findings: `gitlab-reviewer review list <mr> --output json` lists stored reviews newest first, each with a `record` name. `gitlab-reviewer review show <mr> --output json` returns the newest one with every finding's `id`, `file`, `line`, `severity`, `title`, `body`, and `state` (`pending`, `accepted`, `rejected`, `published`, `note`, `below-threshold`).
5. Triage, which only changes the local record:
   - `gitlab-reviewer review accept <mr> <id>...`
   - `gitlab-reviewer review reject <mr> <id>...`
   - `gitlab-reviewer review accept-all <mr>` (every pending finding)
   - `gitlab-reviewer review edit <mr> <id> --body "<markdown>"`
   An unknown id fails the command and saves nothing.
6. Publish: `gitlab-reviewer review publish <mr> --mode draft --output json` posts only the accepted findings, skips ones that restate an existing MR comment, and records each outcome. A finding that failed to post goes back to `pending`. `--mode` defaults to the configured `publish.mode`.
7. Submit: after a draft-mode publish, `draft_pending` is true; `gitlab-reviewer review submit <mr> --output json` publishes the draft review.
8. Approve: `gitlab-reviewer mr approve <mr> --sha <head_sha> --output json`, passing the `head_sha` the review covered so GitLab refuses the approval if new commits arrived. `gitlab-reviewer mr unapprove <mr>` removes it.

Steps 4 to 7 use the newest stored review; pass `--record <record>` (a name from `review list`) to act on a specific one.

## Incremental and full reviews

When the MR already has a stored review, `review <mr>` is incremental: only changes since the last reviewed commit are reviewed and earlier findings carry forward with their states. It falls back to a full review after a rebase or force-push. Pass `--full` to review the whole diff again, for example after changing agents or instructions.

## Exit codes

- `0`: success.
- `1`: any failure, including configuration or GitLab errors, an unknown record or finding id, or a finding that failed to publish (the JSON on stdout still lists every result).
- `2`: the severity gate. `review <mr>` exits 2 when findings at or above `gate.min_severity` remain; `mr approve` exits 2 when `gate.approvals: block` refuses the approval. Treat 2 as a finding to report, not as a crash, and never work around it.

## Safety

`review publish`, `review submit`, `mr approve`, `mr unapprove`, and `review <mr> --publish draft|immediate` act on GitLab as the user and are visible to everyone on the MR. Run them only when the user has explicitly authorized that action for that MR in this conversation; authorization to review is not authorization to publish or approve. Before publishing, show the user the accepted findings (titles and bodies) you are about to post. Never approve an MR whose gate failed, and never approve without checking the review covered the current `head_sha`. Reading commands (`mr list`, `mr show`, `review list`, `review show`) and triage commands are local or read-only and safe to run.
