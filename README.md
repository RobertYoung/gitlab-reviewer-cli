# gitlab-reviewer

A terminal UI that helps you review GitLab merge requests with Claude.

`gitlab-reviewer` lists open MRs across your configured GitLab projects and
groups, checks the MR branch out locally, and has the [Claude Code
CLI](https://docs.anthropic.com/en/docs/claude-code) review the change with
full repository context. Suggested review comments (bugs, security issues,
missing docs, style concerns) land in the TUI where you can edit, accept, or
reject each one before publishing them back to the MR as inline discussions
— immediately, or as a GitLab draft review published in one action.

Prefer a browser? The same workflow is available as a local web app via
[`gitlab-reviewer gui`](#browser-gui).

![Diff view — syntax-highlighted diff with file explorer, inline discussions, and click-to-comment](docs/screenshots/gui-diff.png)

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

Along the way you can approve the MR, leave manual comments, and open a
multi-turn chat with Claude about the MR or a single diff line — all
without leaving the TUI. The [TUI guide](docs/wiki/TUI-Guide.md) walks
through every screen.

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

Every review is stored, so nothing is lost by navigating away or closing
the terminal: press `L` on the MR detail screen to reopen past reviews with
their curation state, and `l` to read a run's progress log. See
[Getting started](docs/wiki/Getting-Started.md) for a guided first review
and the [TUI guide](docs/wiki/TUI-Guide.md) for everything the TUI can do —
approving, manual comments, chatting with Claude about the change, filters,
the file explorer, and the complete keymap.

## Browser GUI

Prefer a browser to a terminal? `gitlab-reviewer gui` serves the same
workflow as a local web app:

```sh
gitlab-reviewer gui                # random free port, opens your browser
gitlab-reviewer gui --port 8080    # fixed port
gitlab-reviewer gui --no-browser   # just print the URL
```

<details>
<summary>Screenshots: MR list, review progress, findings triage</summary>

![MR list with filters](docs/screenshots/gui-mr-list.png)

![Review progress streaming live](docs/screenshots/gui-review-run.png)

![Findings triage — accept, reject, edit, publish](docs/screenshots/gui-findings.png)

</details>

The GUI mirrors the TUI screen for screen over the exact same core:
reviews run through the same pipeline, results land in the same stores,
and a review started in one frontend can be reopened in the other
([feature matrix](docs/features.md)). What the browser adds is rendering
the terminal can't match: syntax-highlighted diffs with soft wrapping, a
persistent file-explorer tree, existing MR discussions shown inline, and
click-to-comment on any diff line. The server binds to `127.0.0.1` only
and every session is protected by a random token baked into the launch
URL. See the [GUI guide](docs/wiki/GUI-Guide.md).

## Headless / CI

`gitlab-reviewer review` runs one review non-interactively for CI jobs and
scripting — same pipeline, no UI:

```sh
# report only: findings stored + printed, nothing posted to GitLab
gitlab-reviewer review mygroup/myapp!123 --output json

# post the findings as one draft review, published in one action
gitlab-reviewer review "$CI_PROJECT_PATH!$CI_MERGE_REQUEST_IID" --publish draft
```

Progress streams to stderr, the outcome to stdout (`text` or `json`), and
the exit code is non-zero on failure. Publishing is off by default
(`--publish none`): the stored review can be reopened later in the TUI or
GUI to curate and publish with a human in the loop. Re-runs are
incremental by default — only the changes pushed since the last reviewed
commit are reviewed, and triaged findings carry forward (`--full`
overrides). See [Headless mode](docs/wiki/Headless-Mode.md) for
incremental re-review, publish semantics, exit codes, and a GitLab CI job
example.

## Configuration

Every setting is available as a flag, an environment variable, and a key in
the settings file at `${XDG_CONFIG_HOME:-~/.config}/gitlab-reviewer/config.yaml`,
with precedence **flags > environment > file > defaults**. Run
`gitlab-reviewer config show` to see the effective configuration (secrets
redacted) and `gitlab-reviewer config validate` to check it.

A taste of what is configurable:

```yaml
gitlab:
  base_url: https://gitlab.example.com
  groups: [platform-team]
review:
  agents: [bug, security, performance]   # which review agents run
  max_budget_usd: 3                      # total spend cap per review
  instructions: "We prefer table-driven tests; flag missing OpenAPI updates."
publish:
  mode: draft                            # or immediate
projects:
  mygroup/infra:                         # per-project overrides
    review:
      use_agents: true
```

The **[configuration reference](docs/wiki/Configuration-Reference.md)**
documents every key, environment variable, flag, and default, including
multiple GitLab instances and per-project overrides. Deeper guides:

- [Review agents](docs/wiki/Review-Agents.md) — the built-in reviewers,
  per-scan selection, cost controls, and bringing your own agents as
  Markdown files
- [Checkout modes](docs/wiki/Checkout-Modes.md) — managed clones vs your
  local clone, cache management, and the local-overlay for uncommitted
  `CLAUDE.md`/`.claude/` files
- [Publishing](docs/wiki/Publishing.md) — draft vs immediate, auto-publish
  thresholds, comment templates
- [MCP servers](docs/wiki/MCP-Servers.md) — opt-in reference material for
  the review session
- [Recipes](docs/wiki/Recipes.md) — worked configurations for common setups

### Secrets

Treat the GitLab token as a secret: pass it via
`GITLAB_REVIEWER_GITLAB_TOKEN` (or `GITLAB_TOKEN`, or per-instance
`token_env`) rather than a flag — flags are visible in `ps` and shell
history. The token is never logged, is redacted from error messages and
`config show`, is handed to git through an in-memory credential helper (it
never lands in `.git/config` or process arguments), and is **never** passed
to the `claude` subprocess. OS keychain support is a planned enhancement.

### The review sandbox

Reviews process code and MR text written by the **MR author**, who may not
be trusted — a prompt-injection surface. The review session is therefore
allowed only `Read`, `Grep`, and `Glob`; `Bash`, `Edit`/`Write`, and all
network tools are denied, so even a fully hijacked model can read local
files but has no tool with which to transmit them. Any GitLab metadata a
check needs is fetched by the tool itself over the API and injected as
prompt text. The one deliberate, opt-in exception is
[`review.mcp_servers`](docs/wiki/MCP-Servers.md). The full threat model is
in [Security model](docs/wiki/Security-Model.md).

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

Ambient AWS credentials are passed through to the `claude` subprocess; see
[Configuration reference — Bedrock](docs/wiki/Configuration-Reference.md#bedrock)
for the details and `review.env` for extras like a corporate proxy.
`gitlab-reviewer models` lists common model IDs for the selected provider
(inference-profile IDs for Bedrock); set `review.models` to pin your
team's own list.

## Development

Requires Go ≥ 1.26, `git`, and the `claude` CLI on your `PATH`.

```sh
# Build and run from source
go build ./cmd/gitlab-reviewer

export GITLAB_REVIEWER_GITLAB_TOKEN=glpat-...
./gitlab-reviewer --project mygroup/myapp

# Or without the build step
go run ./cmd/gitlab-reviewer --project mygroup/myapp

# Tests (includes an end-to-end run against a fake GitLab and scripted claude)
go test -race ./...

# Lint (same config as CI)
golangci-lint run ./...
```

Releases are cut automatically by semantic-release from [conventional
commits](https://www.conventionalcommits.org) on `main`, so commit messages
matter: `feat:`/`fix:` trigger releases.

## Documentation

- **[Wiki](docs/wiki/Home.md)** — usage guides, worked examples, and the
  full configuration reference:
  [Getting started](docs/wiki/Getting-Started.md) ·
  [TUI guide](docs/wiki/TUI-Guide.md) ·
  [GUI guide](docs/wiki/GUI-Guide.md) ·
  [Headless mode](docs/wiki/Headless-Mode.md) ·
  [Configuration reference](docs/wiki/Configuration-Reference.md) ·
  [Review agents](docs/wiki/Review-Agents.md) ·
  [MCP servers](docs/wiki/MCP-Servers.md) ·
  [Checkout modes](docs/wiki/Checkout-Modes.md) ·
  [Publishing](docs/wiki/Publishing.md) ·
  [Security model](docs/wiki/Security-Model.md) ·
  [Recipes](docs/wiki/Recipes.md) ·
  [Troubleshooting](docs/wiki/Troubleshooting.md)
- [Architecture](docs/architecture.md) — component and sequence diagrams
- [Features by frontend](docs/features.md) — what the TUI and the browser
  GUI each support
- [Design decisions](docs/adr/) — short ADRs for the choices that shaped
  the tool

## Roadmap

- [x] M0 — installable skeleton: config, CLI, CI/CD, release pipeline
- [x] M1 — MR browser (list, filter, diff view)
- [x] M2 — review MVP (Claude review, findings editor, inline publish)
- [x] M3 — draft reviews, auto-comment, discussions in context, syntax highlighting
- [x] M4 — multi-pass reviews for large MRs, cache management
- [x] M5 — browser GUI (`gitlab-reviewer gui`): the same workflow served as a local web app
- [x] M6 — headless review (`gitlab-reviewer review`): the same pipeline as a one-shot command for CI
- [ ] Homebrew tap
- [ ] OS keychain storage for the GitLab token
- [ ] OAuth authentication

## License

[MIT](LICENSE)
