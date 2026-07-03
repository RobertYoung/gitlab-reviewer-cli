# ADR-0007: Pluggable review agents

## Status

Accepted

## Context

A review scan used to be one reviewer pass covering six hardcoded finding
categories, with the per-category guidance baked into a single prompt.
There was no way to run only some review dimensions, no per-dimension
progress or attribution, and no way for a team to add a review focus of
their own (say, database migrations or API compatibility) without wedging
it into `review.instructions`, where it dilutes everything else.

## Decision

Reviews are run by **agents**: named review focuses, each executed as its
own `claude -p` invocation with its own system prompt
(`internal/review/agents`).

- **Built-ins subsume categories.** Six built-in agents mirror the finding
  categories (`bug`, `security`, …); their prompts are the former
  per-category guidance. `Category` survives only as a label on findings,
  and `review.categories` becomes a deprecated alias for `review.agents`
  (resolved at config load: an empty `agents` falls back to `categories`,
  whose defaults are all six — no provenance tracking needed).
- **One subprocess per agent per diff chunk**, fanned out by the runner
  under `review.agent_concurrency` (default 3). A failed agent degrades to
  a warning while the others' findings survive; the run errors only when
  every pass fails. `review.max_budget_usd` is the run **total**, divided
  evenly across the planned passes; `review.timeout` stays per pass.
  Findings are stamped with their agent by the runner — the model never
  reports it, so the output schema is unchanged.
- **Bring-your-own agents** are Markdown files with optional YAML
  frontmatter (name, description, categories, severity hint) in
  `~/.config/gitlab-reviewer/agents/` and repo-local
  `.gitlab-reviewer/agents/`. The catalog merges them over the builtins
  with repo > user > builtin shadowing; invalid files are skipped with
  warnings, and unknown selected names fail the run loudly.
- **Selection is per scan**: a multi-select picker in the TUI and agent
  checkboxes in the GUI, seeded from the last selection per project, then
  `review.agents`. `--agents` covers non-interactive use.

`Finding.Agent` is a new optional JSON field, so stored records need no
version bump: old records load with no agent badge, and old binaries ignore
the new key.

## Consequences

- Running all six built-ins costs roughly six subprocess invocations per
  chunk where one sufficed before. The budget split bounds spend but not
  latency; the picker and `review.agents` make trimming the selection the
  first-class mitigation, and the concurrency cap bounds parallel load.
- Agents can flag the same line independently; results are merged without
  deduplication, with the agent badge making provenance visible.
  Cross-agent dedup is a possible follow-up.
- Repo-shipped agent prompts steer the reviewer but run in the same
  read-only tool sandbox as every review; agent definitions cannot alter
  tool allowlists. In path/root checkout modes the pickers and the runner
  read `.gitlab-reviewer/agents/` from the user's local clone, which covers
  definitions deliberately kept untracked (the local_overlay pattern);
  definitions committed at the MR head shadow same-named local ones. In
  clone mode the pickers fetch the directory over the GitLab API at the MR
  head SHA (cached per project + sha), so repo agents are toggleable before
  any checkout exists — including agents the MR itself adds or edits, which
  also means different MRs of one project can legitimately show different
  agent lists. The fetch is best-effort: on failure the picker warns and
  shows builtins + user agents, and the runner still re-resolves the
  selection from the checkout post-fetch, noting unselected repo agents in
  the log.
- `review.use_agents` (Claude Code subagents via the Task tool) is an
  unrelated, older knob; documentation calls the distinction out, and
  renaming it to `use_subagents` is a candidate follow-up.
