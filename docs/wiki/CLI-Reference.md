# CLI reference

```
gitlab-reviewer [flags]              # launch the TUI
gitlab-reviewer gui [flags]          # launch the browser GUI
gitlab-reviewer review <project!iid | MR URL>  # run one review non-interactively (CI/scripting)
gitlab-reviewer review list|show <mr>          # read stored reviews and their findings
gitlab-reviewer review accept|reject <mr> <id>...  # triage findings in a stored review
gitlab-reviewer review accept-all|edit <mr>    # accept every pending finding, or rewrite one body
gitlab-reviewer review publish|submit <mr>     # post accepted findings, submit the draft review
gitlab-reviewer mr list                        # list merge requests with the GUI's filters
gitlab-reviewer mr show <mr>                   # one MR: detail, commits, pipeline, threads
gitlab-reviewer mr approve|unapprove <mr>      # approve (gate-aware) or remove approval
gitlab-reviewer config show          # print effective configuration (secrets redacted)
gitlab-reviewer config validate      # check the configuration
gitlab-reviewer cache ls             # list cached clones
gitlab-reviewer cache clean [--all]  # remove worktrees, evict over-budget clones
gitlab-reviewer models               # list models to use with --model / review.model
gitlab-reviewer version              # print version, commit, build date
```

Every configuration setting is also a **persistent flag** available on the
root command and all subcommands — the full flag ↔ key ↔ env mapping is in
the [Configuration Reference](Configuration-Reference.md). This page
covers the commands themselves and their command-specific flags.

## `gitlab-reviewer` (root)

Launches the TUI. With no projects or groups configured it opens a
group/project picker; otherwise it goes straight to the MR list.

```sh
gitlab-reviewer --project mygroup/myapp --project mygroup/other
gitlab-reviewer --group platform-team
gitlab-reviewer --instance work          # pick a named GitLab instance
gitlab-reviewer --config ./config.yaml   # settings file elsewhere
```

`--config` selects a non-default settings file (default
`~/.config/gitlab-reviewer/config.yaml`).

## `gitlab-reviewer gui`

Serves the browser GUI on `127.0.0.1` and prints a tokenised launch URL.

| Flag | Default | Meaning |
|---|---|---|
| `--port` | random free port | port to listen on |
| `--no-browser` | `false` | don't open the browser, just print the URL |

See the [GUI Guide](GUI-Guide.md).

## `gitlab-reviewer review`

Runs one review non-interactively and prints the outcome to stdout —
progress streams to stderr. Nothing is posted to GitLab unless `--publish`
says so. The MR is named as `project!iid` or by its web URL; a URL's host
also selects the matching `gitlab.instances` entry.

```sh
gitlab-reviewer review mygroup/myapp!123 --publish draft --agents bug,security
gitlab-reviewer review https://gitlab.example.com/mygroup/myapp/-/merge_requests/123
```

| Flag | Default | Meaning |
|---|---|---|
| `--publish` | `none` | `none` (store/report only), `draft` (one review, published in one action), `immediate` |
| `--output` | `text` | `text` or `json` on stdout |
| `--full` | off | scan the whole diff; without it a stored review makes the run incremental (only changes since the last reviewed commit, previous findings carried forward) |

With [`gate.min_severity`](Configuration-Reference.md#gate) configured
(flag `--gate-min-severity`), the command exits `2` when the review finds
blocking findings, so CI pipelines can fail the job.

See [Headless Mode](Headless-Mode.md) for incremental re-review, publish
semantics, exit codes, and a GitLab CI example.

### Stored-review subcommands

These act on the reviews `review` stores, so an agent or script can triage and publish without the TUI or GUI. Each takes the MR as `project!iid` or URL, picks the newest stored review unless `--record <name>` names one from `review list` (a bare `review-*.json` name, which must belong to that MR), and supports `--output text|json`.

| Command | Does |
|---|---|
| `review list <mr>` | stored reviews of the MR, newest first: `record`, `path`, `title`, `started`, `stage`, and `counts` (`findings`, `accepted`, `rejected`, `pending`, `published`) |
| `review show <mr>` | one stored review with every finding (`id`, `file`, `line`, `severity`, `title`, `body`, `state`), plus `record`, `path`, `stage`, `counts`, `head_sha` |
| `review accept <mr> <id>...` | mark findings accepted |
| `review reject <mr> <id>...` | mark findings rejected (never published) |
| `review accept-all <mr>` | every pending finding becomes accepted |
| `review edit <mr> <id> --body <markdown>` | replace a finding's comment body |
| `review publish <mr> [--mode draft\|immediate]` | post only the accepted findings and record each outcome back into the review; `--mode` defaults to `publish.mode` |
| `review submit <mr>` | publish the MR's pending draft review |

Triage behaves exactly like the TUI and GUI and only changes the local record; an unknown finding id fails the command without saving anything. `publish` skips findings that restate a comment already on the MR, reports `results`, `inline`, `notes`, `failed`, and `draft_pending` (true when a draft review is waiting for `review submit`), and exits `1` when any finding failed to post.

## `gitlab-reviewer mr`

Merge request commands for headless use. All support `--output text|json` and never prompt.

| Command | Flags | Does |
|---|---|---|
| `mr list` | `--state opened\|merged\|closed\|all`, `--search`, `--author`, `--target`, `--page`, plus the global `--project`/`--group` | lists MRs in the configured scope (or only the given `--project`/`--group`), newest activity first, each with its `latest_review` summary; JSON: `page`, `has_more`, `merge_requests` |
| `mr show <mr>` | | detail, `commits`, `pipeline` (`status`, `url`), `threads` and `unresolved_threads`, `needs_rebase` and `rebase_warning`, and the `latest_review` summary |
| `mr approve <mr>` | `--sha` | approves, pinned to `--sha` or else the MR's current head so GitLab refuses it if the MR moved on |
| `mr unapprove <mr>` | | removes your approval |

With `gate.min_severity` set and `gate.approvals: block`, `mr approve` refuses to approve while the MR's newest stored review has blocking findings and exits `2`, the same code the review gate uses.

## `gitlab-reviewer config`

- **`config show`** — prints the effective configuration after merging
  flags > environment > file > defaults. Secrets (tokens, MCP headers) are
  redacted, including in per-project sections.
- **`config validate`** — checks completeness and consistency: required
  values (a GitLab credential, region for Bedrock), enums, URL and
  duration formats, instance names, MCP server definitions, and that
  `publish.template` parses.

Both are useful before launching the TUI, and in dotfiles/CI checks.

## `gitlab-reviewer cache`

Manages the clone cache used by the default `clone` checkout mode (under
`checkout.cache_dir`, default `~/.cache/gitlab-reviewer`).

- **`cache ls`** — lists cached clones with sizes.
- **`cache clean`** — removes review worktrees and evicts
  least-recently-used clones until the cache fits `checkout.cache_max_mb`
  (default 2048 MiB).
- **`cache clean --all`** — removes every cached clone, emptying the cache.

Eviction also runs automatically in the background at startup. See
[Checkout Modes](Checkout-Modes.md).

## `gitlab-reviewer models`

Lists the models offered for the review: `review.models` from the
settings file when set, otherwise a curated list of common Claude models
for the selected provider. The configured `review.model` is marked with
`*`. The same list backs shell completion of `--model`. It is suggestions,
not validation — `--model` accepts any model ID the claude CLI
understands. See the
[Configuration Reference](Configuration-Reference.md#review).

## `gitlab-reviewer version`

Prints `gitlab-reviewer <version> (commit <hash>, built <date>)`.
