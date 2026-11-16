---
title: Review agents
description: "How review agents work, how to choose and configure them, and how to bring your own."
---

A review scan is run by **agents**: focused reviewers that each make their own pass over the MR with their own prompt. Six built-in agents mirror the finding categories (`bug`, `security`, `performance`, `docs`, `style`, `design`) and you can add your own.

Findings carry the agent that produced them: the findings screens show it alongside severity and category, and `publish.template` can reference it as `{{.agent}}` ([Publishing](/gitlab-reviewer-cli/publishing/)).

:::note[Naming]
`review.agents` selects which *review agents* run (this page). `review.use_agents` is an unrelated setting that lets the reviewer delegate to Claude Code *subagents* within a pass; see the [configuration reference](/gitlab-reviewer-cli/reference/configuration/#review.use_agents).
:::

## Choosing what runs

In the TUI, pressing `r` opens a picker listing the available agents; in the GUI the *Run AI review* button has an agents selector. Both remember your last selection per project (shared across frontends). Non-interactively, `--agents bug,security` (or `review.agents`) sets the default selection:

```yaml
review:
  agents: [bug, security, design]
```

Unknown names fail the run loudly rather than silently reviewing less. The deprecated `review.categories` key still works as an alias.

### Per-agent model

Each agent can run on a different model, chosen from the configured list ([`review.models`](/gitlab-reviewer-cli/reference/configuration/#review.models), or the built-in suggestions: the same list `gitlab-reviewer models` prints):

- In the **TUI** picker, press `m` on the highlighted agent; the chosen model shows on the agent's row, and the `(default)` entry clears it.
- In the **GUI**, the *Run AI review* form shows a model dropdown next to each agent.

Either way the pick is remembered per project alongside the agent selection, and both frontends share it.

To pin an agent's model globally instead of per project, set `review.agent_models` in the settings file (or the GUI settings page, *Review → Agent models*):

```yaml
review:
  agent_models:
    security: opus
    docs: haiku
```

The model an agent runs with resolves in this order:

1. the picker choice for this project (remembered);
2. [`review.agent_models`](/gitlab-reviewer-cli/reference/configuration/#review.agent_models), the global per-agent setting;
3. the agent's frontmatter `model:` (see below): how repo/user agents set their own default;
4. [`review.model`](/gitlab-reviewer-cli/reference/configuration/#review.model), the run-wide default;
5. the `claude` CLI's own default when none of the above is set.

Entries for agents that do not exist are inert (no error): the map keys are matched against whatever agents actually run.

## Cost and limits

Each selected agent is one `claude` invocation per diff chunk, so six agents cost roughly six times one combined pass. Large MRs are split into multiple chunks, multiplying further.

- `review.max_budget_usd` is the **total** for the run, divided evenly across the planned passes plus one slice for the aggregation phase (see below).
- `review.timeout` (default `10m`) applies to **each** pass.
- `review.agent_concurrency` (default 3) caps how many passes run at once.

Trim the selection (e.g. `agents: [bug, security]`) if cost or latency matters more than coverage. A failed agent degrades to a warning while the others' findings survive; the run errors only when every pass fails.

## Aggregating duplicate findings

Independent agents often report the same defect in different words, sometimes on a nearby line or in a different category. After every pass finishes, the shared runner sends the complete set of findings to an **aggregation agent** that groups them by underlying defect, so the TUI, GUI and headless runs all curate and publish one finding per issue.

- **Input.** Every finding goes in with a stable id, its agent, location, severity, category, title, body and suggestion. Findings carried forward by an incremental review are included so new duplicates of them are caught too. Manual comments are left out.
- **Grouping.** The agent merges findings only when it is confident they describe the same defect. Distinct problems on the same line stay separate, and a group the agent marks as uncertain is split back into its members. A finding with no duplicate stays exactly as the reviewing agent wrote it.
- **The merged finding.** Its location, category and suggestion come from one of the source findings (the anchor the agent picks), never from the model, and its severity is the highest any source reported. The title and body combine the sources' evidence. The original findings, the contributing agents and the agent's merge rationale stay attached for inspection: the GUI shows them under *Merged from N findings* on the finding, the TUI lists them in the detail pane, and they are stored with the review. When sources propose different fixes the merge note says so.
- **Curation is preserved.** A finding carried forward from an earlier review (accepted, rejected, published, or edited by hand) is never rewritten: fresh duplicates are folded into it as sources and its state and wording stay put, so nothing is republished. When the agent thinks two already curated findings are duplicates, both stay as they are and a warning names them for you to resolve.
- **Validation.** Every input id must land in exactly one group and every reference must exist. Missing, invented or doubly assigned ids reject the whole answer.
- **Batching.** One call sees up to 150 findings. Larger sets are aggregated in batches (grouped by file), then the batches' results go through one more call so duplicates split across batches still meet. If more than 150 distinct findings remain after batching, the phase falls back as below.
- **Budget and time.** With `review.max_budget_usd` set, the aggregation phase gets one slice of the total, alongside the passes, and never more than what the passes left unspent. It runs under `review.timeout` and its cost is added to the run's cost. Progress lines in the run log are prefixed `[aggregate]`.
- **Fallback.** On a timeout, malformed or inconsistent output, a claude error, or insufficient budget, every source finding is kept and the deterministic deduplication runs instead (same file and line with at least 60% word overlap), with a warning on the review explaining why. Cancelling the run cancels aggregation too.

The aggregation agent runs with no tools at all, no MCP servers, and an empty scratch directory as its working directory, so the finding text it reads (which comes from the merge request and is treated as untrusted) cannot reach the repository, the network or a shell. It never sees the GitLab token and cannot publish. It uses `review.model`.

Set `review.aggregate: false` (or `--aggregate=false`) to skip the phase and use only the deterministic deduplication. Publishing still checks findings against comments already on the MR either way (see [Publishing](/gitlab-reviewer-cli/publishing/)).

## Bring your own agents

Drop Markdown files in `~/.config/gitlab-reviewer/agents/` (yours) or `.gitlab-reviewer/agents/` in the reviewed repo (the team's). Claude Code's agent directories are picked up too, at both scopes, `~/.claude/agents/` (user) and `.claude/agents/` in the reviewed repo (project), so one set of files serves both tools: the definition format is compatible, and frontmatter fields this tool does not know (`tools`, …) are ignored.

The file body is the agent's prompt; an optional YAML frontmatter adds metadata:

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

Agent names must match `^[a-z0-9][a-z0-9_-]*$`. Ready-to-copy definitions are on [Example agents](/gitlab-reviewer-cli/example-agents/), and more in [Recipes](/gitlab-reviewer-cli/recipes/#custom-agents-in-practice).

### Claude Code plugin agents

Agents installed via Claude Code *plugins* can join the catalog too, but only for plugins you explicitly accept: a plugin agent's prompt steers the reviewer, so installing a plugin for Claude Code must never silently add reviewers here:

```yaml
review:
  claude_plugins:
    - review-pack@official-marketplace  # full form
    - security-toolkit                  # bare name, if only one marketplace has it
```

Accepted plugins are resolved through Claude Code's install manifest (`~/.claude/plugins/installed_plugins.json`) and their `agents/` directories are loaded recursively (plugin layouts vary), in allowlist order. A bare name that exists in several marketplaces is skipped with a warning asking you to qualify it, as is a name that isn't installed. When a plugin is installed at both user and project scope, the user-scope install is used. There is no wildcard) each plugin is a trust decision, so the key is settings-file only (no env var or flag).

### Precedence and safety

Name collisions resolve as **repo > user > plugin > built-in**, so a repo can sharpen the stock `security` agent by shipping its own `security.md`, and your own definition beats a same-named plugin one; within a scope the tool's own directory beats Claude Code's (`.gitlab-reviewer/agents/` over `.claude/agents/` in a repo, `~/.config/gitlab-reviewer/agents/` over `~/.claude/agents/` for the user). Invalid definition files are skipped with a warning in the picker and the run log.

Repo-shipped agents steer the reviewer's attention but run in the same read-only sandbox as every review (`Read`/`Grep`/`Glob` only): an agent definition cannot alter tool permissions or grant network access ([Security Model](/gitlab-reviewer-cli/security-model/)).

### How repo agents are discovered

Discovery depends on [`checkout.mode`](/gitlab-reviewer-cli/checkout-modes/):

- **`path` / `root` modes**: the pickers read both agent directories straight from your local clone, which also picks up definitions your team deliberately keeps untracked (e.g. via `.git/info/exclude`, like `checkout.local_overlay` files). The run resolves against the same directories, with definitions committed at the MR head taking precedence over local ones of the same name.
- **`clone` mode**: the directories are fetched over the GitLab API at the MR's head commit (cached per project and SHA), so repo agents are toggleable before any checkout exists, including agents the MR itself adds or changes. If the fetch fails, the picker falls back to the built-in and user agents with a warning, and the runner still merges the repo's agents from the checkout at run time.

## How a run executes

For the curious (details in [ADR-0007](https://github.com/RobertYoung/gitlab-reviewer-cli/blob/main/docs/adr/0007-pluggable-review-agents.md)): the runner excludes `review.exclude` files, chunks the remaining diff to `review.max_diff_kb` per pass, and fans out one `claude` invocation per agent per chunk under the concurrency cap. Individual diffs too large to inline are written into the checkout as files for Claude to read. Every finding is stamped with its agent by the tool, not the model.

The results then go through [aggregation](#aggregating-duplicate-findings), which merges findings that report the same defect and keeps each contributing agent and its original evidence visible.
