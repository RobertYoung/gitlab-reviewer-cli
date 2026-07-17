# Configuration reference

Every setting is available three ways, with precedence
**flags > environment > settings file > defaults**:

- **Settings file**: `${XDG_CONFIG_HOME:-~/.config}/gitlab-reviewer/config.yaml`
  (YAML; override the path with `--config`).
- **Environment**: variables prefixed `GITLAB_REVIEWER_`, derived from the
  key — `gitlab.base_url` → `GITLAB_REVIEWER_GITLAB_BASE_URL`. List-valued
  settings take comma-separated values.
- **Flags**: on the root command and every subcommand.

Inspect and check the result:

```sh
gitlab-reviewer config show       # effective configuration, secrets redacted
gitlab-reviewer config validate   # completeness and consistency checks
```

A few settings are **file-only** (no env var or flag): `gitlab.instances`,
`review.mcp_servers`, and the map form of `review.env`. One has an env var
but no flag: `checkout.clone_missing`.

**Unprefixed fallbacks** — honoured only when the prefixed variable is
unset: `GITLAB_TOKEN` → `gitlab.token`, `AWS_REGION` → `bedrock.region`,
`AWS_PROFILE` → `bedrock.profile`.

## gitlab

| File key | Environment variable | Flag | Default | Notes |
|---|---|---|---|---|
| `gitlab.base_url` | `GITLAB_REVIEWER_GITLAB_BASE_URL` | `--gitlab-base-url` | `https://gitlab.com` | must be a valid URL |
| `gitlab.token` | `GITLAB_REVIEWER_GITLAB_TOKEN` (or `GITLAB_TOKEN`) | `--gitlab-token` (discouraged — see [Secrets](#secrets)) | — | **required** unless every instance supplies its own token |
| `gitlab.projects` | `GITLAB_REVIEWER_GITLAB_PROJECTS` (comma-separated) | `--project` (repeatable) | `[]` | full paths, e.g. `mygroup/myapp` |
| `gitlab.groups` | `GITLAB_REVIEWER_GITLAB_GROUPS` (comma-separated) | `--group` (repeatable) | `[]` | |
| `gitlab.per_page` | `GITLAB_REVIEWER_GITLAB_PER_PAGE` | `--per-page` | `50` | 1–100 |
| `gitlab.instances` | — (file only, list) | — | `[]` | see below |
| `gitlab.default_instance` | `GITLAB_REVIEWER_GITLAB_DEFAULT_INSTANCE` | `--instance` | unset | must name a configured instance |

### Multiple GitLab instances

If you work across more than one GitLab (say a company self-hosted
instance and gitlab.com), define them as named instances instead of
editing `gitlab.base_url`/`gitlab.token` by hand:

```yaml
gitlab:
  instances:
    - name: work
      base_url: https://gitlab.example.com
      token_env: WORK_GITLAB_TOKEN   # read the token from this env var
    - name: staging
      base_url: https://gitlab.staging.example.com
      token: glpat-staging...        # or put the token in the file
    - name: personal
      base_url: https://gitlab.com
      # token omitted — falls back to gitlab.token / GITLAB_REVIEWER_GITLAB_TOKEN
  default_instance: work   # optional: skip the picker
```

Instance fields:

| Field | Required | Meaning |
|---|---|---|
| `name` | yes | unique identifier, used by `--instance` and the pickers |
| `base_url` | yes | valid URL of the instance |
| `token` | no | token in the file |
| `token_env` | no | *name* of an environment variable holding the token; consulted only when `token` is empty |

One instance is selected at startup and its `base_url`/`token` replace the
top-level `gitlab` settings. Selection order: `--instance` flag →
`gitlab.default_instance` → automatic when only one is configured →
interactive picker. Non-interactive runs with several instances must name
one, or they error.

Each instance takes its token from, in order: `token` in the file, the
environment variable named by `token_env`, then `gitlab.token` (and its
fallbacks). `token_env` keeps per-instance secrets out of the settings
file, and the named variable only has to be set on machines where that
instance is actually selected — selecting an instance whose variable is
unset is an **error**, not a silent fallback to the shared token.

## review

| File key | Environment variable | Flag | Default | Notes |
|---|---|---|---|---|
| `review.provider` | `GITLAB_REVIEWER_REVIEW_PROVIDER` | `--provider` | `anthropic` | `anthropic` \| `bedrock` |
| `review.model` | `GITLAB_REVIEWER_REVIEW_MODEL` | `--model` | claude CLI default | passed to `claude` verbatim |
| `review.models` | `GITLAB_REVIEWER_REVIEW_MODELS` (comma-separated) | `--models` | curated per-provider list | suggestions offered by `gitlab-reviewer models` — see below |
| `review.claude_path` | `GITLAB_REVIEWER_REVIEW_CLAUDE_PATH` | `--claude-path` | `claude` on `PATH` | |
| `review.timeout` | `GITLAB_REVIEWER_REVIEW_TIMEOUT` | `--review-timeout` | `10m` | Go duration; **per review pass**, must be > 0 |
| `review.max_budget_usd` | `GITLAB_REVIEWER_REVIEW_MAX_BUDGET_USD` | `--max-budget-usd` | unset | **total per run**, split evenly across passes |
| `review.agents` | `GITLAB_REVIEWER_REVIEW_AGENTS` (comma-separated) | `--agents` | all built-ins | see [Review Agents](Review-Agents.md); unknown names fail the run |
| `review.agent_models` | — (file only, map) | — | `{}` | model per agent (`security: opus`) — see [Review Agents](Review-Agents.md#per-agent-model) |
| `review.agent_concurrency` | `GITLAB_REVIEWER_REVIEW_AGENT_CONCURRENCY` | `--agent-concurrency` | `3` | ≥ 1; how many passes run at once |
| `review.categories` | `GITLAB_REVIEWER_REVIEW_CATEGORIES` (comma-separated) | `--categories` | all six | **deprecated** alias — see below |
| `review.instructions` | `GITLAB_REVIEWER_REVIEW_INSTRUCTIONS` | `--instructions` | `""` | appended to the review prompt |
| `review.instructions_file` | `GITLAB_REVIEWER_REVIEW_INSTRUCTIONS_FILE` | `--instructions-file` | unset | contents appended too |
| `review.max_diff_kb` | `GITLAB_REVIEWER_REVIEW_MAX_DIFF_KB` | `--max-diff-kb` | `256` | ≥ 1; diff budget per pass, in KiB |
| `review.exclude` | `GITLAB_REVIEWER_REVIEW_EXCLUDE` (comma-separated globs) | `--exclude` (repeatable) | see below | files removed from the review entirely |
| `review.bare` | `GITLAB_REVIEWER_REVIEW_BARE` | `--bare` | `false` | run `claude --bare`; see caveat below |
| `review.use_agents` | `GITLAB_REVIEWER_REVIEW_USE_AGENTS` | `--use-agents` | `false` | allow Claude Code *subagents* — unrelated to `review.agents` |
| `review.claude_plugins` | — (file only, list) | — | `[]` | Claude Code plugins whose agents join the catalog — see [Review Agents](Review-Agents.md#claude-code-plugin-agents) |
| `review.env` | — (file only, map) | `--review-env KEY=VALUE` (repeatable) | `{}` | extra env for the `claude` subprocess; `GITLAB*` keys are stripped |
| `review.mcp_servers` | — (file only, map) | — | `{}` | see [MCP Servers](MCP-Servers.md) |
| `review.allowed_domains` | — (file only, list) | — | `[]` | grants `WebFetch` scoped to these domains only; the GUI's per-run picker can narrow this, never widen it |
| `review.allowed_commands` | — (file only, list) | — | `[]` | grants `Bash` scoped to these command patterns only (e.g. `npm test:*`); same per-run narrowing as above |

Notes:

- **`review.models`** feeds the `gitlab-reviewer models` command, shell
  completion of `--model`, and the TUI picker's per-agent model chooser
  (press `m` — see [Review Agents](Review-Agents.md#per-agent-model)):
  when unset, a curated list of common Claude
  models for the selected provider is offered (aliases like
  `opus`/`sonnet`/`haiku` plus full IDs for `anthropic`; cross-region
  inference-profile IDs for `bedrock`). It is suggestions, not
  validation — `review.model` accepts any model ID the claude CLI
  understands. Set it to pin your team's own list (e.g. account-specific
  Bedrock inference profiles):

  ```yaml
  review:
    models:
      - eu.anthropic.claude-sonnet-4-6
      - eu.anthropic.claude-haiku-4-5
  ```

- **`review.instructions`** (and/or the contents of
  `review.instructions_file`) are appended to the built-in review prompt —
  use them for team conventions ("we prefer table-driven tests", "flag
  missing OpenAPI updates"). See [Recipes](Recipes.md) for examples.
- **`review.bare`** runs claude with `--bare` for fully deterministic runs
  (no user hooks or CLAUDE.md), but `--bare` skips OAuth/keychain auth —
  leave it off if you authenticate with a Claude subscription rather than
  an API key.
- **`review.use_agents`** lets the reviewer delegate to your Claude Code
  subagents (the project's `.claude/agents/*.md` plus your user-level
  agents) — useful when you keep standard agents for specific tools and
  frameworks. The review stays read-only either way: mutating and network
  tools are denied session-wide and subagents inherit the denials.
  Subagents multiply token usage, so pair this with
  `review.max_budget_usd`. **Naming note:** this is unrelated to
  `review.agents`, which selects the *review agents* that run.
- **`review.claude_plugins`** loads review agents shipped by installed
  Claude Code plugins. It is an explicit allowlist — installing a plugin
  never silently adds reviewers — and file-only, like `review.mcp_servers`,
  because it is a trust decision. See
  [Review Agents](Review-Agents.md#claude-code-plugin-agents).
- **Cost model**: each selected agent is one `claude` invocation per diff
  chunk, so six agents cost roughly six times one combined pass.
  `review.max_budget_usd` is divided evenly across the planned passes
  (unspent slices are not redistributed); `review.timeout` applies to each
  pass.

### Deprecated: `review.categories`

`review.categories` is an alias for `review.agents` from before reviews
were agent-based: when `review.agents` is unset, it is filled from
`review.categories`, whose default is all six built-ins — that is what
makes "all built-in agents" the effective default. Values must be built-in
names. Setting it logs a deprecation warning, `--categories` is marked
deprecated in `--help`, and the key will be removed in a future release.
Use `review.agents`.

### Default `review.exclude` globs

```
**/go.sum, **/package-lock.json, **/yarn.lock, **/pnpm-lock.yaml,
**/Cargo.lock, **/poetry.lock, **/uv.lock, **/Gemfile.lock,
vendor/**, node_modules/**, **/*.pb.go, **/*_generated.go,
**/*.min.js, **/*.min.css, **/*.svg, dist/**
```

Setting `review.exclude` **replaces** this list, so re-include the
defaults you still want.

## bedrock

| File key | Environment variable | Flag | Default | Notes |
|---|---|---|---|---|
| `bedrock.region` | `GITLAB_REVIEWER_BEDROCK_REGION` (or `AWS_REGION`) | `--aws-region` | — | required when `review.provider: bedrock` |
| `bedrock.profile` | `GITLAB_REVIEWER_BEDROCK_PROFILE` (or `AWS_PROFILE`) | `--aws-profile` | — | |

With `review.provider: bedrock` the tool sets `CLAUDE_CODE_USE_BEDROCK=1`
on the `claude` subprocess and passes through ambient AWS credentials:
`AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_SESSION_TOKEN`,
`AWS_CONFIG_FILE`, `AWS_SHARED_CREDENTIALS_FILE`,
`AWS_BEARER_TOKEN_BEDROCK`, and `AWS_DEFAULT_REGION`. (With the default
`anthropic` provider, `ANTHROPIC_API_KEY`, `ANTHROPIC_BASE_URL`, and
`ANTHROPIC_AUTH_TOKEN` pass through instead.) Proxy variables and common
locale/CA variables pass through in both cases; anything else your setup
needs goes in `review.env`. See
[Recipes — Bedrock](Recipes.md#reviewing-via-aws-bedrock).

## checkout

| File key | Environment variable | Flag | Default | Notes |
|---|---|---|---|---|
| `checkout.mode` | `GITLAB_REVIEWER_CHECKOUT_MODE` | `--checkout-mode` | `clone` | `clone` \| `path` \| `root` |
| `checkout.path` | `GITLAB_REVIEWER_CHECKOUT_PATH` | `--repo-path` | — | required in `path` mode |
| `checkout.root` | `GITLAB_REVIEWER_CHECKOUT_ROOT` | `--git-root` | — | required in `root` mode |
| `checkout.transport` | `GITLAB_REVIEWER_CHECKOUT_TRANSPORT` | `--clone-transport` | `https` | `https` \| `ssh` |
| `checkout.cache_dir` | `GITLAB_REVIEWER_CHECKOUT_CACHE_DIR` | `--cache-dir` | `${XDG_CACHE_HOME:-~/.cache}/gitlab-reviewer` | |
| `checkout.cache_max_mb` | `GITLAB_REVIEWER_CHECKOUT_CACHE_MAX_MB` | `--cache-max-mb` | `2048` | LRU eviction budget |
| `checkout.keep` | `GITLAB_REVIEWER_CHECKOUT_KEEP` | `--keep-worktree` | `false` | keep review worktrees afterwards |
| `checkout.clone_missing` | `GITLAB_REVIEWER_CHECKOUT_CLONE_MISSING` | — (no flag) | `false` | `root` mode: create missing clones |
| `checkout.local_overlay` | `GITLAB_REVIEWER_CHECKOUT_LOCAL_OVERLAY` (comma-separated globs) | `--local-overlay` (repeatable) | `**/CLAUDE.md`, `**/CLAUDE.local.md`, `.claude/**` | `path`/`root` modes only |

See [Checkout Modes](Checkout-Modes.md) for what the modes mean, cache
management (`gitlab-reviewer cache ls` / `cache clean`), and the local
overlay for uncommitted convention files. Whatever the mode, reviews
always run in a **detached git worktree at the MR head commit** — never in
your working tree.

## publish

| File key | Environment variable | Flag | Default | Notes |
|---|---|---|---|---|
| `publish.mode` | `GITLAB_REVIEWER_PUBLISH_MODE` | `--publish-mode` | `draft` | `draft` \| `immediate` |
| `publish.auto_comment` | `GITLAB_REVIEWER_PUBLISH_AUTO_COMMENT` | `--auto-comment` | `false` | auto-publish strong findings |
| `publish.auto_min_severity` | `GITLAB_REVIEWER_PUBLISH_AUTO_MIN_SEVERITY` | `--auto-min-severity` | `major` | `info` \| `minor` \| `major` \| `critical` |
| `publish.min_severity` | `GITLAB_REVIEWER_PUBLISH_MIN_SEVERITY` | `--publish-min-severity` | `info` | publish floor: findings below it are never posted |
| `publish.fallback_to_note` | `GITLAB_REVIEWER_PUBLISH_FALLBACK_TO_NOTE` | `--fallback-to-note` | `true` | general note when no position resolves |
| `publish.attribution` | `GITLAB_REVIEWER_PUBLISH_ATTRIBUTION` | `--attribution` | `false` | AI-suggested footer |
| `publish.template` | `GITLAB_REVIEWER_PUBLISH_TEMPLATE` | `--publish-template` | built-in layout | Go text/template; fields `{{.severity}}`, `{{.category}}`, `{{.agent}}`, `{{.title}}`, `{{.body}}`, `{{.file}}` |

See [Publishing](Publishing.md) for the modes, auto-publish behaviour, the
publish floor, the note fallback, and template examples. Templates are
syntax-checked at config validation and fail early on unknown fields.

## gate

| File key | Environment variable | Flag | Default | Notes |
|---|---|---|---|---|
| `gate.min_severity` | `GITLAB_REVIEWER_GATE_MIN_SEVERITY` | `--gate-min-severity` | unset (gate off) | findings at/above this severity are blocking |
| `gate.approvals` | `GITLAB_REVIEWER_GATE_APPROVALS` | `--gate-approvals` | `warn` | `off` \| `warn` \| `block`; only consulted when the gate is on |

With the gate on, a finding is **blocking** while it is at or above
`gate.min_severity` and has not been rejected in triage (manual comments
never block). The `review` command exits with code 2 while the review has
blocking findings (see [Headless Mode](Headless-Mode.md#output-and-exit-codes)),
and approving from the TUI/GUI warns or refuses per `gate.approvals` (see
[Publishing](Publishing.md#severity-gate)). The gate is advisory from
GitLab's perspective: it restricts this tool, not the GitLab UI or API.

## ui

| File key | Environment variable | Flag | Default | Notes |
|---|---|---|---|---|
| `ui.diff_view` | `GITLAB_REVIEWER_UI_DIFF_VIEW` | `--diff-view` | `unified` | `unified` \| `split`; both frontends |
| `ui.file_explorer` | `GITLAB_REVIEWER_UI_FILE_EXPLORER` | `--file-explorer` | `closed` | `open` \| `closed`; initial explorer state |

Both are session defaults: `v` in the TUI (or the layout links in the
GUI) switches the diff layout for the current session, and `e` toggles the
explorer.

## log

| File key | Environment variable | Flag | Default | Notes |
|---|---|---|---|---|
| `log.level` | `GITLAB_REVIEWER_LOG_LEVEL` | `--log-level` | `info` | `debug` \| `info` \| `warn` \| `error` |
| `log.file` | `GITLAB_REVIEWER_LOG_FILE` | `--log-file` | `~/.local/state/gitlab-reviewer/gitlab-reviewer.log` | dir 0700, file 0600 |

Review artifacts are stored separately from the log, under
`${XDG_STATE_HOME:-~/.local/state}/gitlab-reviewer/reviews/`: raw review
transcripts (`.jsonl`), per-run progress logs (`.log`), and review results
(`.json`, the findings with their curation states). Results and logs are
browsable in both frontends via the past-reviews screen.

## Per-project overrides

The `review`, `checkout`, `publish`, and `gate` sections — and only those —
can be overridden per project in the settings file, keyed by the full
project path:

```yaml
review:
  max_diff_kb: 256
projects:
  mygroup/myapp:
    review:
      instructions: "This service is latency-critical; flag every allocation in the hot path."
      agents: [bug, security, performance]
    publish:
      mode: immediate
```

Per-project `review.mcp_servers` and `review.env` work too. `gitlab.*`,
`ui.*`, and `log.*` are not overridable per project.

## Secrets

Treat the GitLab token as a secret: pass it via
`GITLAB_REVIEWER_GITLAB_TOKEN` (or `GITLAB_TOKEN`, or per-instance
`token_env`) rather than a flag — flags are visible in `ps` and shell
history. The token (including every per-instance token) is never logged,
is redacted from error messages and `config show` (as are MCP remote-server
`headers`), is handed to git through an in-memory credential helper (it
never lands in `.git/config` or process arguments), and is **never**
passed to the `claude` subprocess — `GITLAB*` keys are stripped from
`review.env` and rejected in MCP server definitions. OS keychain support
is a planned enhancement.
