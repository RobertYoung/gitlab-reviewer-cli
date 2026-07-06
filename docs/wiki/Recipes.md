# Recipes

Worked configurations for common setups. Everything here composes — mix
and match. The [Configuration Reference](Configuration-Reference.md) has
the full key list.

## A team's day-to-day setup

```yaml
gitlab:
  base_url: https://gitlab.example.com
  groups: [platform-team]
review:
  agents: [bug, security, design]     # trimmed for cost and latency
  max_budget_usd: 3                   # total cap per review
  instructions: |
    We prefer table-driven tests. Flag missing OpenAPI updates when
    handlers change.
publish:
  mode: draft                         # curate, then publish in one action
```

## Work and personal GitLab side by side

```yaml
gitlab:
  instances:
    - name: work
      base_url: https://gitlab.example.com
      token_env: WORK_GITLAB_TOKEN
    - name: personal
      base_url: https://gitlab.com
      # falls back to GITLAB_REVIEWER_GITLAB_TOKEN
  default_instance: work
```

`gitlab-reviewer --instance personal` switches; with no
`default_instance`, the TUI opens a picker and the GUI serves each
instance under its own URL path.

## Reviewing via AWS Bedrock

```yaml
review:
  provider: bedrock
  model: eu.anthropic.claude-sonnet-4-6   # Bedrock model/inference-profile ID
bedrock:
  region: eu-west-2      # or AWS_REGION
  profile: my-profile    # or AWS_PROFILE
```

This sets `CLAUDE_CODE_USE_BEDROCK=1` plus your AWS region/profile on the
`claude` subprocess and passes through ambient AWS credentials (access
keys, session token, config/shared-credentials paths,
`AWS_BEARER_TOKEN_BEDROCK`). Anything extra your setup needs — e.g. a
corporate proxy — can be forwarded with `review.env`:

```yaml
review:
  env:
    HTTPS_PROXY: http://proxy.corp:3128
```

`gitlab-reviewer models` lists common inference-profile IDs once
`review.provider` is `bedrock`; set `review.models` if your account uses
different regions or profile names.

Verify with a normal review run — the progress log shows the model the
session started with.

## Per-project strictness

Any `review.*`, `checkout.*`, or `publish.*` setting can be overridden per
project:

```yaml
review:
  agents: [bug, security]               # default everywhere
projects:
  mygroup/payments:
    review:
      agents: [bug, security, performance, design]
      instructions: "This service is latency-critical; flag every allocation in the hot path."
      max_budget_usd: 5
    publish:
      auto_comment: true                # strong findings publish themselves
      auto_min_severity: critical
  mygroup/docs-site:
    review:
      agents: [docs, style]
```

## Monorepo: keep the noise down

```yaml
projects:
  mygroup/monorepo:
    review:
      max_diff_kb: 512                  # bigger chunks before splitting passes
      exclude:                          # note: replaces the default list
        - "**/go.sum"
        - "**/package-lock.json"
        - "vendor/**"
        - "node_modules/**"
        - "**/*.pb.go"
        - "**/*_generated.go"
        - "third_party/**"
        - "**/testdata/**"
```

## Custom agents in practice

Your own review focus, applied everywhere
(`~/.config/gitlab-reviewer/agents/api-compat.md`):

```markdown
---
description: Guards public API compatibility
categories: [design, bug]
---
You are reviewing for public API compatibility. Flag removed or renamed
exported symbols, changed function signatures, narrowed interfaces, and
HTTP/JSON contract changes without a version bump or deprecation note.
```

A team agent shipped in the repo (`.gitlab-reviewer/agents/migrations.md`)
— committed, so every teammate's reviews use it:

```markdown
---
description: Reviews schema migrations for lock hazards
categories: [bug, performance]
severity: major
---
You are reviewing database schema migrations. Focus on long-running
locks, missing indexes for new query patterns, and irreversible
migrations without a documented rollback.
```

Repos that already keep review guidance in Claude Code's
`.claude/agents/` work as-is, and so do your personal subagents in
`~/.claude/agents/`. See [Review Agents](Review-Agents.md) for precedence
and discovery rules.

## Claude Code subagents inside reviews

If your team maintains Claude Code subagents for specific tools
(Terraform, Ansible, CI conventions), let the reviewer delegate to them —
and cap the extra token spend:

```yaml
projects:
  mygroup/infra:
    review:
      use_agents: true
      max_budget_usd: 5
```

## Uncommitted CLAUDE.md and .claude/ files

Teams that keep Claude conventions untracked (via `.git/info/exclude`)
until they stabilise can still have reviews follow them — switch to a
local-clone checkout mode and the default overlay globs do the rest:

```yaml
checkout:
  mode: root
  root: ~/git
  clone_missing: true
```

Untracked `**/CLAUDE.md`, `**/CLAUDE.local.md`, and `.claude/**` files are
copied into the review worktree; committed files are never overridden. See
[Checkout Modes](Checkout-Modes.md#local-convention-files-uncommitted-claudemd-claude).

## MR hygiene checks

Have the reviewer sanity-check commit messages and the MR description
against the diff — the metadata is injected by the tool, so the sandbox
stays closed ([Security Model](Security-Model.md#mr-hygiene-checks-without-network-access)):

```yaml
review:
  # keep the 'docs' agent selected so hygiene findings have a home
  agents: [bug, security, performance, docs, style, design]
  instructions: |
    Also run these MR-hygiene checks, reported as 'docs' findings
    (minor/info severity, never blocking), each anchored on a
    representative changed line:
    - Flag any commit message that describes something not in the diff or
      omits a significant change that is in the diff.
    - Flag the description if it claims changes not in the diff, omits a
      significant change, or leaves the MR template's placeholder comments
      (e.g. `<!-- ... -->`) unfilled.
```

Rebase status needs none of this — it is always computed and shown.

## Comments that read like you wrote them

```yaml
publish:
  template: "{{.body}}"       # no severity/category badge
review:
  instructions: |
    Write comment bodies in first person, as a colleague would phrase
    them. Ask questions rather than issuing verdicts when uncertain.
```

## Live reference material for one repo

The IAM-roles example — the review can consult AWS documentation, and only
in this project ([MCP Servers](MCP-Servers.md) has the security notes):

```yaml
projects:
  mygroup/iam-roles:
    review:
      mcp_servers:
        aws-documentation:
          command: uvx
          args: [awslabs.aws-documentation-mcp-server@latest]
          env:
            AWS_DOCUMENTATION_PARTITION: aws
          tools: [search_documentation, read_documentation, recommend]
```
