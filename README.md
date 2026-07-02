# gitlab-reviewer

A terminal UI that helps you review GitLab merge requests with Claude.

`gitlab-reviewer` lists open MRs across your configured GitLab projects and
groups, checks the MR branch out locally, and has the [Claude Code
CLI](https://docs.anthropic.com/en/docs/claude-code) review the change with
full repository context. Suggested review comments (bugs, security issues,
missing docs, style concerns) land in the TUI where you can edit, accept, or
reject each one before publishing them back to the MR as inline discussions
— immediately, or as a GitLab draft review published in one action.

![demo](docs/demo.gif) <!-- TODO: record demo GIF -->

## How it works

1. **Browse** — the TUI lists open merge requests across the projects and
   groups you configure, with filtering by state, author, target branch, and
   free-text search. With no scope configured it first lists your available
   groups and projects so you can pick one inside the TUI.
2. **Review** — Claude runs locally against a checkout of the MR branch, so
   it can explore the whole repository (read files, grep for callers), not
   just the diff. Reviews always run in a detached git worktree at the MR
   head commit; your working tree is never touched. Large MRs are split into
   multiple review passes automatically.
3. **Curate** — every suggested comment is shown against its diff hunk and
   is editable and individually acceptable or rejectable before anything
   leaves your machine.
4. **Publish** — accepted comments become positioned inline discussions on
   the MR (with a general-note fallback when a position cannot be resolved),
   either immediately or as a draft review published in one action.

## Requirements

- The [`claude` CLI](https://docs.anthropic.com/en/docs/claude-code) ≥ 2.0
  on your `PATH` (hard requirement — `gitlab-reviewer` shells out to it)
- `git`
- A GitLab personal or project access token with `api` scope (GitLab ≥ 16.x
  for draft reviews)
- An Anthropic API key / Claude subscription, or AWS Bedrock access

## Installation

### Prebuilt binaries

Download the archive for your platform (linux/darwin, amd64/arm64) from the
[releases page](https://github.com/RobertYoung/gitlab-reviewer-cli/releases),
verify it against `checksums.txt`, and put `gitlab-reviewer` on your `PATH`.

### go install

```sh
go install github.com/RobertYoung/gitlab-reviewer-cli/cmd/gitlab-reviewer@latest
```

## Quickstart

```sh
export GITLAB_REVIEWER_GITLAB_TOKEN=glpat-...   # or GITLAB_TOKEN

# Zero config: with no projects/groups set, the TUI lists your available
# groups and projects so you can pick what to browse
gitlab-reviewer

# Or scope it up front:
gitlab-reviewer --project mygroup/myapp

# Or create ~/.config/gitlab-reviewer/config.yaml:
cat > ~/.config/gitlab-reviewer/config.yaml <<'EOF'
gitlab:
  base_url: https://gitlab.example.com   # self-hosted works too
  projects:
    - mygroup/myapp
  groups:
    - platform-team
EOF

gitlab-reviewer config validate
gitlab-reviewer
```

Inside the TUI press `?` for the full keybinding reference. The core flow:
pick an MR (`enter`), inspect the diff, press `r` to run the review, then
`a`/`x`/`e` to accept/reject/edit findings and `p` to publish them.

## Configuration

Every setting is available as a flag, an environment variable, and a key in
the settings file at `${XDG_CONFIG_HOME:-~/.config}/gitlab-reviewer/config.yaml`,
with precedence **flags > environment > file > defaults**. Run
`gitlab-reviewer config show` to see the effective configuration (secrets
redacted) and `gitlab-reviewer config validate` to check it.

### GitLab

| File key | Environment variable | Flag | Default |
|---|---|---|---|
| `gitlab.base_url` | `GITLAB_REVIEWER_GITLAB_BASE_URL` | `--gitlab-base-url` | `https://gitlab.com` |
| `gitlab.token` | `GITLAB_REVIEWER_GITLAB_TOKEN` (or `GITLAB_TOKEN`) | `--gitlab-token` (discouraged — see [Secrets](#secrets)) | — **required** |
| `gitlab.projects` | `GITLAB_REVIEWER_GITLAB_PROJECTS` (comma-separated) | `--project` (repeatable) | `[]` |
| `gitlab.groups` | `GITLAB_REVIEWER_GITLAB_GROUPS` (comma-separated) | `--group` (repeatable) | `[]` |
| `gitlab.per_page` | `GITLAB_REVIEWER_GITLAB_PER_PAGE` | `--per-page` | `50` |

### Review

| File key | Environment variable | Flag | Default |
|---|---|---|---|
| `review.provider` | `GITLAB_REVIEWER_REVIEW_PROVIDER` | `--provider` | `anthropic` (`anthropic`\|`bedrock`) |
| `review.model` | `GITLAB_REVIEWER_REVIEW_MODEL` | `--model` | claude CLI default |
| `review.claude_path` | `GITLAB_REVIEWER_REVIEW_CLAUDE_PATH` | `--claude-path` | `claude` on `PATH` |
| `review.timeout` | `GITLAB_REVIEWER_REVIEW_TIMEOUT` | `--review-timeout` | `10m` |
| `review.max_budget_usd` | `GITLAB_REVIEWER_REVIEW_MAX_BUDGET_USD` | `--max-budget-usd` | unset |
| `review.categories` | `GITLAB_REVIEWER_REVIEW_CATEGORIES` (comma-separated) | `--categories` | all (`bug,security,performance,docs,style,design`) |
| `review.instructions` | `GITLAB_REVIEWER_REVIEW_INSTRUCTIONS` | `--instructions` | `""` |
| `review.instructions_file` | `GITLAB_REVIEWER_REVIEW_INSTRUCTIONS_FILE` | `--instructions-file` | unset |
| `review.max_diff_kb` | `GITLAB_REVIEWER_REVIEW_MAX_DIFF_KB` | `--max-diff-kb` | `256` |
| `review.exclude` | `GITLAB_REVIEWER_REVIEW_EXCLUDE` (comma-separated globs) | `--exclude` (repeatable) | lockfiles, `vendor/**`, generated/minified files |
| `review.bare` | `GITLAB_REVIEWER_REVIEW_BARE` | `--bare` | `false` |
| `review.use_agents` | `GITLAB_REVIEWER_REVIEW_USE_AGENTS` | `--use-agents` | `false` |
| `review.env` | — (file only, map) | `--review-env KEY=VALUE` (repeatable) | `{}` |

`review.instructions` (and/or the contents of `review.instructions_file`)
are appended to the built-in review prompt — use them for team conventions
("we prefer table-driven tests", "flag missing OpenAPI updates").
`review.bare` runs claude with `--bare` for fully deterministic runs (no
user hooks or CLAUDE.md), but `--bare` skips OAuth/keychain auth — leave it
off if you authenticate with a Claude subscription rather than an API key.

`review.use_agents` lets the reviewer delegate to your Claude Code
subagents — the project's `.claude/agents/*.md` (which the [local
overlay](#local-convention-files-uncommitted-claudemd-claude) carries into
the review worktree even when uncommitted) plus your user-level agents. Use
it when you keep standard agents for specific tools and frameworks
(Terraform, Ansible, your CI conventions) and want reviews to lean on their
expertise. The review stays read-only either way: mutating and network
tools are denied as session-wide permission rules that subagents inherit.
Agents multiply token usage, so pairing this with `review.max_budget_usd`
is a good idea. Per-project enablement works like any other override:

```yaml
projects:
  mygroup/infra:
    review:
      use_agents: true
      max_budget_usd: 5
```

### Bedrock

| File key | Environment variable | Flag | Default |
|---|---|---|---|
| `bedrock.region` | `GITLAB_REVIEWER_BEDROCK_REGION` (or `AWS_REGION`) | `--aws-region` | — |
| `bedrock.profile` | `GITLAB_REVIEWER_BEDROCK_PROFILE` (or `AWS_PROFILE`) | `--aws-profile` | — |

### Checkout

| File key | Environment variable | Flag | Default |
|---|---|---|---|
| `checkout.mode` | `GITLAB_REVIEWER_CHECKOUT_MODE` | `--checkout-mode` | `clone` (`clone`\|`path`\|`root`) |
| `checkout.path` | `GITLAB_REVIEWER_CHECKOUT_PATH` | `--repo-path` | — |
| `checkout.root` | `GITLAB_REVIEWER_CHECKOUT_ROOT` | `--git-root` | — |
| `checkout.transport` | `GITLAB_REVIEWER_CHECKOUT_TRANSPORT` | `--clone-transport` | `https` (`https`\|`ssh`) |
| `checkout.cache_dir` | `GITLAB_REVIEWER_CHECKOUT_CACHE_DIR` | `--cache-dir` | `${XDG_CACHE_HOME:-~/.cache}/gitlab-reviewer` |
| `checkout.cache_max_mb` | `GITLAB_REVIEWER_CHECKOUT_CACHE_MAX_MB` | `--cache-max-mb` | `2048` |
| `checkout.keep` | `GITLAB_REVIEWER_CHECKOUT_KEEP` | `--keep-worktree` | `false` |
| `checkout.clone_missing` | `GITLAB_REVIEWER_CHECKOUT_CLONE_MISSING` | — | `false` |
| `checkout.local_overlay` | `GITLAB_REVIEWER_CHECKOUT_LOCAL_OVERLAY` (comma-separated globs) | `--local-overlay` (repeatable) | `**/CLAUDE.md`, `**/CLAUDE.local.md`, `.claude/**` |

Checkout modes:

- **`clone`** (default) — the tool manages cached bare clones under the
  cache dir, fetching MR branches on demand. `gitlab-reviewer cache ls`
  shows what is cached; `gitlab-reviewer cache clean` removes review
  worktrees and evicts least-recently-used clones over the size budget
  (`--all` empties the cache).
- **`path`** — you point `checkout.path` at an existing local clone. Its
  origin remote is verified against the MR's project.
- **`root`** — clones live under a structured root:
  `<root>/<host>/<group>/<project>` (e.g. `~/git/gitlab.com/mygroup/myapp`).
  Set `checkout.clone_missing: true` to have missing clones created.

Whatever the mode, the review itself always runs in a **detached git
worktree at the MR head commit** — never in your working tree.

#### Local convention files (uncommitted CLAUDE.md, .claude/)

Teams often keep Claude conventions — `CLAUDE.md`, `.claude/` agents and
skills — locally before they are ready to commit them, typically listed in
`.git/info/exclude`. Because reviews run in a clean worktree, those files
would normally be invisible to the reviewer. In `path` and `root` modes,
untracked files in your clone matching `checkout.local_overlay` are copied
into the review worktree so Claude follows them. The default globs cover
exactly the files Claude Code reads (`**/CLAUDE.md`, `**/CLAUDE.local.md`,
`.claude/**`); extend them per project for other convention files:

```yaml
projects:
  mygroup/myapp:
    checkout:
      local_overlay: ["**/CLAUDE.md", "**/CLAUDE.local.md", ".claude/**", "Taskfile*.yaml"]
```

Files tracked at the MR head commit are never overridden — the review
always sees the committed state of real code — and nothing else from
`.git/info/exclude` (e.g. a local `.env`) is copied unless a glob matches
it.

### Publishing

| File key | Environment variable | Flag | Default |
|---|---|---|---|
| `publish.mode` | `GITLAB_REVIEWER_PUBLISH_MODE` | `--publish-mode` | `draft` (`draft`\|`immediate`) |
| `publish.auto_comment` | `GITLAB_REVIEWER_PUBLISH_AUTO_COMMENT` | `--auto-comment` | `false` |
| `publish.auto_min_severity` | `GITLAB_REVIEWER_PUBLISH_AUTO_MIN_SEVERITY` | `--auto-min-severity` | `major` |
| `publish.fallback_to_note` | `GITLAB_REVIEWER_PUBLISH_FALLBACK_TO_NOTE` | `--fallback-to-note` | `true` |
| `publish.attribution` | `GITLAB_REVIEWER_PUBLISH_ATTRIBUTION` | `--attribution` | `false` |
| `publish.template` | `GITLAB_REVIEWER_PUBLISH_TEMPLATE` | `--publish-template` | built-in layout |

- **`draft`** mode creates GitLab draft notes (a pending review) and
  publishes them in one action — or leaves them pending for the web UI.
  **`immediate`** posts each comment as a live discussion as it is
  accepted. The mode can be toggled per run (`m` on the publish screen).
- With `publish.auto_comment` on, findings at or above
  `publish.auto_min_severity` are published without confirmation; weaker
  findings still go through the interactive findings screen.
- `publish.attribution` appends a small footer marking comments as
  AI-suggested.

#### Comment layout

`publish.template` is a Go [text/template](https://pkg.go.dev/text/template)
that controls how each comment body is built. The default layout is

```
**[{{.severity}} · {{.category}}] {{.title}}**

{{.body}}
```

which renders as `**[major · design] Title**` followed by the body. If you
would rather your comments read like something you typed yourself, drop the
badge:

```yaml
publish:
  template: "{{.body}}"           # body only, no header at all
  # template: "{{.title}} — {{.body}}"
```

Available fields: `{{.severity}}`, `{{.category}}`, `{{.title}}`,
`{{.body}}`, `{{.file}}`. Severity and category are still shown in the TUI
findings screen either way, so nothing is lost by omitting them from the
published comment. Suggestion blocks and the optional attribution footer are
appended after the templated body. To also change the *tone* of the comment
text itself, add guidance via `review.instructions`, e.g.
`"Write comment bodies in first person, as a colleague would phrase them."`

### Logging

| File key | Environment variable | Flag | Default |
|---|---|---|---|
| `log.level` | `GITLAB_REVIEWER_LOG_LEVEL` | `--log-level` | `info` |
| `log.file` | `GITLAB_REVIEWER_LOG_FILE` | `--log-file` | `~/.local/state/gitlab-reviewer/gitlab-reviewer.log` |

Raw review transcripts are kept under
`~/.local/state/gitlab-reviewer/reviews/` for debugging.

### Per-project overrides

Any `review.*`, `checkout.*`, or `publish.*` setting can be overridden per
project in the settings file:

```yaml
review:
  max_diff_kb: 256
projects:
  mygroup/myapp:
    review:
      instructions: "This service is latency-critical; flag every allocation in the hot path."
      categories: [bug, security, performance]
    publish:
      mode: immediate
```

### Secrets

Treat the GitLab token as a secret: pass it via
`GITLAB_REVIEWER_GITLAB_TOKEN` (or `GITLAB_TOKEN`) rather than a flag —
flags are visible in `ps` and shell history. The token is never logged, is
redacted from error messages and `config show`, is handed to git through an
in-memory credential helper (it never lands in `.git/config` or process
arguments), and is **never** passed to the `claude` subprocess. OS keychain
support is a planned enhancement.

## Using AWS Bedrock

`gitlab-reviewer` drives Claude Code's native Bedrock support:

```yaml
review:
  provider: bedrock
  model: eu.anthropic.claude-sonnet-4-6   # Bedrock model/inference-profile ID
bedrock:
  region: eu-west-2      # or AWS_REGION
  profile: my-profile    # or AWS_PROFILE
```

This sets `CLAUDE_CODE_USE_BEDROCK=1` plus your AWS region/profile on the
`claude` subprocess, and passes through ambient AWS credentials
(`AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY`/`AWS_SESSION_TOKEN`, config and
shared-credentials paths, `AWS_BEARER_TOKEN_BEDROCK`). Anything extra your
setup needs (e.g. a corporate proxy) can be forwarded with `review.env`:

```yaml
review:
  env:
    HTTPS_PROXY: http://proxy.corp:3128
```

Verify with a normal review run — the progress log shows the model the
session started with.

## Development

Requires Go ≥ 1.26, `git`, and the `claude` CLI on your `PATH`.

```sh
# Build and run from source
go build ./cmd/gitlab-reviewer

export GITLAB_REVIEWER_GITLAB_TOKEN=glpat-...
./gitlab-reviewer --project mygroup/myapp

# Or without the build step
go run ./cmd/gitlab-reviewer --project mygroup/myapp
```

`gitlab-reviewer config validate` checks your configuration is complete and
`gitlab-reviewer config show` prints the effective settings (token
redacted) — both useful before launching the TUI.

```sh
# Tests (includes an end-to-end run against a fake GitLab and scripted claude)
go test -race ./...

# Lint (same config as CI)
golangci-lint run ./...
```

Releases are cut automatically by semantic-release from [conventional
commits](https://www.conventionalcommits.org) on `main`, so commit messages
matter: `feat:`/`fix:` trigger releases.

## Documentation

- [Architecture](docs/architecture.md) — component and sequence diagrams
- [Design decisions](docs/adr/) — short ADRs for the choices that shaped the tool

## Roadmap

- [x] M0 — installable skeleton: config, CLI, CI/CD, release pipeline
- [x] M1 — MR browser (list, filter, diff view)
- [x] M2 — review MVP (Claude review, findings editor, inline publish)
- [x] M3 — draft reviews, auto-comment, discussions in context, syntax highlighting
- [x] M4 — multi-pass reviews for large MRs, cache management
- [ ] Homebrew tap
- [ ] OS keychain storage for the GitLab token
- [ ] OAuth authentication

## License

[MIT](LICENSE)
