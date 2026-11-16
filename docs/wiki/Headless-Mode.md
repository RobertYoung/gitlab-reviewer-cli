# Headless mode

`gitlab-reviewer review` runs one AI review non-interactively — no TUI, no
browser — for CI jobs, bots, and scripting. It drives exactly the same
pipeline as the interactive frontends: the MR branch is checked out into a
detached worktree, the configured [review agents](Review-Agents.md) run
with full repository context, and the result is stored so it can be
reopened later from the MR's past reviews in the TUI or GUI.

```sh
gitlab-reviewer review mygroup/myapp!123
gitlab-reviewer review https://gitlab.example.com/mygroup/myapp/-/merge_requests/123
gitlab-reviewer review mygroup/myapp!123 --publish immediate
gitlab-reviewer review mygroup/myapp!123 --publish none --output json > findings.json
```

The merge request is named as `project!iid` — in GitLab CI that is
`"$CI_PROJECT_PATH!$CI_MERGE_REQUEST_IID"` — or as its web URL, pasted
straight from the browser (a trailing `/diffs`, query string, or
`#note_…` anchor is tolerated).

## Flags

| Flag | Default | Meaning |
|---|---|---|
| `--publish` | `none` | what to do with findings: `none`, `draft`, or `immediate` |
| `--output` | `text` | result format on stdout: `text` or `json` |
| `--full` | off | review the whole diff even when a stored review allows an [incremental run](#incremental-re-review) |

Every configuration setting is also available as a persistent flag,
environment variable, or settings-file key with the usual precedence
(flags > environment > file > defaults), so a CI job can be driven
entirely by environment variables — see the
[Configuration Reference](Configuration-Reference.md). `--agents`,
`--max-budget-usd`, and `--review-timeout` are the ones most worth pinning
in automation.

## Incremental re-review

When the MR already has a stored review, the run is **incremental by
default**: the new head is compared against the last reviewed commit and
only the changed files go through the review passes — faster, cheaper, and
it does not re-surface findings a human already triaged.

- Findings from the previous review **carry forward with their curation
  states** (pending, accepted, rejected, published). Findings whose anchor
  lines were changed or removed since the last review are dropped (each
  drop is reported on stderr).
- Already-published or rejected findings are never re-posted by
  `--publish`; only new (and still-pending) findings are.
- If the head has not moved at all, no review passes run: the previous
  findings simply carry into a fresh record.
- The run **falls back to a full review** — with the reason on stderr —
  when there is no stored review yet, the MR was rebased (the diff base
  moved), the stored record predates commit tracking, or the last reviewed
  commit is unreachable (force-push, GC).

`--full` forces a full scan of the entire diff regardless. Pushed-commit
pipeline jobs stay cheap and low-noise with the default; use `--full` for
a periodic deep pass or after changing agents/instructions.

## Publish semantics

Headless runs have no curation step, so publishing is **off by default**:

- **`none`** (default) — nothing is posted to GitLab. Findings are stored
  and reported on stdout; a human can reopen the review in the TUI or GUI
  (past reviews on the MR detail screen) to curate and publish. This is
  the human-in-the-loop mode.
- **`immediate`** — every finding is posted as a positioned inline
  discussion as it resolves, with the usual
  [general-note fallback](Publishing.md) when a position cannot be
  resolved.
- **`draft`** — findings are collected into a GitLab draft review and
  published in one action at the end, producing a single review event
  instead of a stream of comments. The draft grouping is used for
  atomicity; if you want drafts to *stay* pending for a human to publish
  from the GitLab UI, use `--publish none` and curate from the TUI/GUI
  instead.

`publish.mode`, `publish.template`, `publish.attribution`, and
`publish.fallback_to_note` from the configuration apply as usual (the
`--publish` flag replaces `publish.mode` for the run).

## Agent-driven review loop

Beyond one-shot `review` runs, the CLI covers the whole curated loop the GUI offers, so an agent (or a script) can review an MR on a user's behalf: find the MR, inspect it, run the review, read and triage the stored findings, publish only the accepted ones, submit the draft review, and approve. Every command takes `--output json` and never prompts.

```sh
gitlab-reviewer mr list --project mygroup/myapp --output json
gitlab-reviewer mr show mygroup/myapp!123 --output json
gitlab-reviewer review mygroup/myapp!123 --output json          # stores the review, posts nothing
gitlab-reviewer review show mygroup/myapp!123 --output json     # findings with ids and states
gitlab-reviewer review accept mygroup/myapp!123 f001 f003
gitlab-reviewer review reject mygroup/myapp!123 f002
gitlab-reviewer review edit mygroup/myapp!123 f003 --body "Tighter wording."
gitlab-reviewer review publish mygroup/myapp!123 --mode draft  # accepted findings only
gitlab-reviewer review submit mygroup/myapp!123                 # publish the draft review
gitlab-reviewer mr approve mygroup/myapp!123 --sha <head_sha>
```

Triage only changes the stored record, exactly as in the TUI and GUI, and the record stays reopenable there. `review publish` posts nothing but accepted findings, skips findings that restate a comment already on the MR, and writes each outcome back into the record: `published`, `note` (posted as a general note), `below-threshold`, or `pending` when posting failed, as in the GUI, so a failed finding is re-accepted before the next publish. `review list` names each stored review, and `--record <name>` pins any of these commands to one; by default they use the newest. See the [CLI Reference](CLI-Reference.md#stored-review-subcommands) for every command and its JSON fields.

`mr approve` pins the approval to `--sha` (for example the `head_sha` from `mr show` or `review show` that the review covered) or, without it, to the MR's current head, so GitLab refuses the approval if new commits arrived. With `gate.approvals: block` it refuses to approve while the newest stored review still has blocking findings.

## Output and exit codes

Progress (the same lines the TUI review screen shows) streams to
**stderr**. The outcome goes to **stdout**:

- `--output text` — a short human summary: finding count by severity, the
  review summary, warnings, cost, per-finding lines, and where the record
  was stored.
- `--output json` — the stored review record as JSON: `ref`, `title`,
  `summary`, `warnings`, `cost_usd`, `findings` (each with file, line,
  severity, category, agent, body, and its publish `state`), `log_path`,
  `record_path`, and — when the gate is configured — a `gate` object
  (`min_severity`, `blocking`, `passed`).

Exit codes:

| Code | Meaning |
|---|---|
| `0` | review completed; no gate configured, or the gate passed |
| `1` | any failure: configuration errors, GitLab/API errors, a failed review run, an unknown record or finding id, or a finding that failed to publish (`review` and `review publish`) |
| `2` | the review completed but the [severity gate](Configuration-Reference.md#gate) failed: at least one finding at or above `gate.min_severity`; or `mr approve` was refused by `gate.approvals: block` |

Set `gate.min_severity` (flag `--gate-min-severity`) to make blocking
findings fail the CI job:

```sh
gitlab-reviewer review "$CI_PROJECT_PATH!$CI_MERGE_REQUEST_IID" \
    --gate-min-severity major   # exits 2 if any major/critical finding
```

Without a configured gate, findings do not affect the exit code.

## Multiple GitLab instances

The command never prompts. With multiple
[`gitlab.instances`](Configuration-Reference.md) configured:

- A **URL** target selects the instance whose `base_url` host matches the
  URL — no `--instance` needed, and the review can never run against the
  wrong instance (a host that matches no instance is an error). When
  several instances share a host (e.g. different tokens),
  `--instance`/`gitlab.default_instance` breaks the tie.
- A **`project!iid`** target carries no host, so it follows the explicit
  rules: `--instance`, `gitlab.default_instance`, or a single configured
  instance — otherwise the command errors instead of prompting.

## Non-interactive guarantees

- The clone-cache budget is enforced synchronously before exit (the
  interactive frontends do this in the background).
- The [review sandbox](Security-Model.md) is unchanged: the review
  session gets `Read`/`Grep`/`Glob` only.

## GitLab CI example

A review job that runs on every merge request and posts findings as one
draft review published in a single action:

```yaml
ai-review:
  stage: review
  rules:
    - if: $CI_PIPELINE_SOURCE == "merge_request_event"
  variables:
    GIT_STRATEGY: none # the tool manages its own checkout
  script:
    - gitlab-reviewer review "$CI_PROJECT_PATH!$CI_MERGE_REQUEST_IID"
        --publish draft
        --agents bug,security
        --max-budget-usd 2
```

Provide `GITLAB_REVIEWER_GITLAB_TOKEN` (a token with `api` scope — a
project or group access token works well for bots) and an Anthropic
credential (or Bedrock access) as masked CI/CD variables. The `claude` CLI
≥ 2.0 and `git` must be on the image's `PATH`.

For a report-only job, use `--publish none --output json` and feed the
JSON to your own tooling.
