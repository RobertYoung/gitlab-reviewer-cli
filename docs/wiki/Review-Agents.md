# Review agents

A review scan is run by **agents**: focused reviewers that each make their
own pass over the MR with their own prompt. Six built-in agents mirror the
finding categories — `bug`, `security`, `performance`, `docs`, `style`,
`design` — and you can add your own.

Findings carry the agent that produced them: the findings screens show it
alongside severity and category, and `publish.template` can reference it
as `{{.agent}}` ([Publishing](Publishing.md)).

> **Naming note:** `review.agents` selects which *review agents* run (this
> page). `review.use_agents` is an unrelated setting that lets the
> reviewer delegate to Claude Code *subagents* within a pass — see the
> [Configuration Reference](Configuration-Reference.md#review).

## Choosing what runs

In the TUI, pressing `r` opens a picker listing the available agents; in
the GUI the *Run AI review* button has an agents selector. Both remember
your last selection per project (shared across frontends). Non-interactively,
`--agents bug,security` (or `review.agents`) sets the default selection:

```yaml
review:
  agents: [bug, security, design]
```

Unknown names fail the run loudly rather than silently reviewing less. The
deprecated `review.categories` key still works as an alias.

### Per-agent model

Each agent can run on a different model, chosen from the configured list
([`review.models`](Configuration-Reference.md#review), or the built-in
suggestions — the same list `gitlab-reviewer models` prints):

- In the **TUI** picker, press `m` on the highlighted agent; the chosen
  model shows on the agent's row, and the `(default)` entry clears it.
- In the **GUI**, the *Run AI review* form shows a model dropdown next to
  each agent.

Either way the pick is remembered per project alongside the agent
selection, and both frontends share it.

To pin an agent's model globally instead of per project, set
`review.agent_models` in the settings file (or the GUI settings page,
*Review → Agent models*):

```yaml
review:
  agent_models:
    security: opus
    docs: haiku
```

The model an agent runs with resolves in this order:

1. the picker choice for this project (remembered);
2. [`review.agent_models`](Configuration-Reference.md#review), the global
   per-agent setting;
3. the agent's frontmatter `model:` (see below) — how repo/user agents set
   their own default;
4. [`review.model`](Configuration-Reference.md#review), the run-wide default;
5. the `claude` CLI's own default when none of the above is set.

Entries for agents that do not exist are inert (no error): the map keys
are matched against whatever agents actually run.

## Cost and limits

Each selected agent is one `claude` invocation per diff chunk, so six
agents cost roughly six times one combined pass. Large MRs are split into
multiple chunks, multiplying further.

- `review.max_budget_usd` is the **total** for the run, divided evenly
  across the planned passes.
- `review.timeout` (default `10m`) applies to **each** pass.
- `review.agent_concurrency` (default 3) caps how many passes run at once.

Trim the selection (e.g. `agents: [bug, security]`) if cost or latency
matters more than coverage. A failed agent degrades to a warning while the
others' findings survive; the run errors only when every pass fails.

## Bring your own agents

Drop Markdown files in `~/.config/gitlab-reviewer/agents/` (yours) or
`.gitlab-reviewer/agents/` in the reviewed repo (the team's). Claude Code's
agent directories are picked up too, at both scopes — `~/.claude/agents/`
(user) and `.claude/agents/` in the reviewed repo (project) — so one set of
files serves both tools: the definition format is compatible, and
frontmatter fields this tool does not know (`tools`, …) are ignored.

The file body is the agent's prompt; an optional YAML frontmatter adds
metadata:

```markdown
---
name: sql-migrations           # optional; defaults to the file name
description: Reviews schema migrations for lock hazards
categories: [bug, performance] # finding labels it may use (default: all)
severity: major                # optional severity hint
model: opus                    # optional default model for this agent
---
You are reviewing database schema migrations. Focus on long-running
locks, missing indexes for new query patterns, and irreversible
migrations without a documented rollback.
```

Agent names must match `^[a-z0-9][a-z0-9_-]*$`. More examples in
[Recipes](Recipes.md#custom-agents-in-practice).

### Claude Code plugin agents

Agents installed via Claude Code *plugins* can join the catalog too, but
only for plugins you explicitly accept — a plugin agent's prompt steers the
reviewer, so installing a plugin for Claude Code must never silently add
reviewers here:

```yaml
review:
  claude_plugins:
    - review-pack@official-marketplace  # full form
    - security-toolkit                  # bare name, if only one marketplace has it
```

Accepted plugins are resolved through Claude Code's install manifest
(`~/.claude/plugins/installed_plugins.json`) and their `agents/`
directories are loaded (recursively — plugin layouts vary), in allowlist
order. A bare name that exists in several marketplaces is skipped with a
warning asking you to qualify it, as is a name that isn't installed. When
a plugin is installed at both user and project scope, the user-scope
install is used. There is no wildcard — each plugin is a trust decision,
so the key is settings-file only (no env var or flag).

### Precedence and safety

Name collisions resolve as **repo > user > plugin > built-in**, so a repo
can sharpen the stock `security` agent by shipping its own `security.md`,
and your own definition beats a same-named plugin one; within a scope the
tool's own directory beats Claude Code's (`.gitlab-reviewer/agents/` over
`.claude/agents/` in a repo, `~/.config/gitlab-reviewer/agents/` over
`~/.claude/agents/` for the user). Invalid definition files are skipped
with a warning in the picker and the run log.

Repo-shipped agents steer the reviewer's attention but run in the same
read-only sandbox as every review (`Read`/`Grep`/`Glob` only) — an agent
definition cannot alter tool permissions or grant network access
([Security Model](Security-Model.md)).

### How repo agents are discovered

Discovery depends on [`checkout.mode`](Checkout-Modes.md):

- **`path` / `root` modes** — the pickers read both agent directories
  straight from your local clone, which also picks up definitions your
  team deliberately keeps untracked (e.g. via `.git/info/exclude`, like
  `checkout.local_overlay` files). The run resolves against the same
  directories, with definitions committed at the MR head taking precedence
  over local ones of the same name.
- **`clone` mode** — the directories are fetched over the GitLab API at
  the MR's head commit (cached per project and SHA), so repo agents are
  toggleable before any checkout exists — including agents the MR itself
  adds or changes. If the fetch fails, the picker falls back to the
  built-in and user agents with a warning, and the runner still merges the
  repo's agents from the checkout at run time.

## How a run executes

For the curious (details in
[ADR-0007](https://github.com/RobertYoung/gitlab-reviewer-cli/blob/main/docs/adr/0007-pluggable-review-agents.md)):
the runner excludes `review.exclude` files, chunks the remaining diff to
`review.max_diff_kb` per pass, and fans out one `claude` invocation per
agent per chunk under the concurrency cap. Individual diffs too large to
inline are written into the checkout as files for Claude to read. Results
are merged without cross-agent deduplication — two agents can flag the
same line, with the agent badge making provenance visible — and every
finding is stamped with its agent by the tool, not the model.
