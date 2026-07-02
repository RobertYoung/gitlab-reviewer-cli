# gitlab-reviewer

A terminal UI that helps you review GitLab merge requests with Claude.

`gitlab-reviewer` lists open MRs across your configured GitLab projects and
groups, checks the MR branch out locally, and has the [Claude Code
CLI](https://docs.anthropic.com/en/docs/claude-code) review the change with
full repository context. Suggested review comments (bugs, security issues,
missing docs, style concerns) land in the TUI where you can edit, accept, or
reject each one before publishing them back to the MR as inline discussions.

> 🚧 **Status: under active development.** The MR browser and review flow are
> being built milestone by milestone; see the roadmap below.

![demo](docs/demo.gif) <!-- TODO: record demo GIF -->

## How it works

1. **Browse** — the TUI lists open merge requests across the projects and
   groups you configure, with filtering by state, author, target branch, and
   free-text search.
2. **Review** — Claude runs locally against a checkout of the MR branch
   (fresh cached clone, an existing local clone, or a structured git root
   like `~/git/gitlab.com/group/app`), so it can read the whole repository,
   not just the diff.
3. **Curate** — every suggested comment is editable and individually
   acceptable or rejectable before anything leaves your machine.
4. **Publish** — accepted comments become positioned inline discussions on
   the MR, either immediately or as a draft review published in one action.

## Requirements

- The [`claude` CLI](https://docs.anthropic.com/en/docs/claude-code) on your
  `PATH` (hard requirement — `gitlab-reviewer` shells out to it)
- `git`
- A GitLab personal or project access token with `api` scope
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

# One-off, all flags:
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

## Configuration

Every setting is available as a flag, an environment variable
(`GITLAB_REVIEWER_*`), and a key in the settings file at
`${XDG_CONFIG_HOME:-~/.config}/gitlab-reviewer/config.yaml`, with precedence
**flags > environment > file > defaults**. Run `gitlab-reviewer config show`
to see the effective configuration (secrets redacted).

<!-- TODO(M5): generated full flag/env/file reference table -->

Key settings:

| Setting | Purpose | Default |
|---|---|---|
| `gitlab.base_url` | GitLab instance URL | `https://gitlab.com` |
| `gitlab.token` | Access token (**required**; prefer the env var) | — |
| `gitlab.projects` / `gitlab.groups` | What to browse | — |
| `review.provider` | `anthropic` or `bedrock` | `anthropic` |
| `review.categories` | Finding categories to request | all |
| `review.instructions` | Extra text appended to the review prompt | — |
| `checkout.mode` | `clone`, `path`, or `root` | `clone` |
| `publish.mode` | `draft` or `immediate` | `draft` |
| `publish.auto_comment` | Publish findings ≥ `publish.auto_min_severity` without confirmation | `false` |

Per-project overrides live under `projects.<full/project/path>` in the
settings file and may override any `review.*`, `checkout.*`, or `publish.*`
setting.

### Secrets

Treat the GitLab token as a secret: pass it via `GITLAB_REVIEWER_GITLAB_TOKEN`
(or `GITLAB_TOKEN`) rather than a flag or the settings file. The token is
never logged, is redacted from error messages and `config show`, and is not
passed to the `claude` subprocess. OS keychain support is a planned
enhancement.

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
`claude` subprocess. Any additional environment (e.g. corporate proxy or
`AWS_BEARER_TOKEN_BEDROCK`) can be passed through with `review.env`.
<!-- TODO(M3): full Bedrock setup guide -->

## Documentation

- [Architecture](docs/architecture.md) — component and sequence diagrams
- [Design decisions](docs/adr/) — short ADRs for the choices that shaped the tool

## Roadmap

- [x] M0 — installable skeleton: config, CLI, CI/CD, release pipeline
- [ ] M1 — MR browser (list, filter, diff view)
- [ ] M2 — review MVP (Claude review, findings editor, inline publish)
- [ ] M3 — draft reviews, auto-comment, Bedrock guide, syntax highlighting
- [ ] M4 — large-MR handling, cache management, rate-limit hardening
- [ ] M5 — full docs, demo, polish; Homebrew tap

## License

[MIT](LICENSE)
