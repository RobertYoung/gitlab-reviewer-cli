---
title: Agent-driven reviews
description: "Let Claude Code or a script drive the whole review loop through the CLI, and install the gitlab-reviewer Claude Code plugin."
---

Beyond one-shot `review` runs, the CLI covers the whole curated loop the GUI offers, so an agent (or a script) can review an MR on a user's behalf: find the MR, inspect it, run the review, read and triage the stored findings, publish only the accepted ones, submit the draft review, and approve. Every command takes `--output json` and never prompts. Quote `project!iid` arguments in the shell: `!` can trigger history expansion.

```sh
gitlab-reviewer mr list --project mygroup/myapp --output json
gitlab-reviewer mr show 'mygroup/myapp!123' --output json
gitlab-reviewer review 'mygroup/myapp!123' --output json          # stores the review, posts nothing
gitlab-reviewer review show 'mygroup/myapp!123' --output json     # findings with ids and states
gitlab-reviewer review accept 'mygroup/myapp!123' f001 f003
gitlab-reviewer review reject 'mygroup/myapp!123' f002
gitlab-reviewer review edit 'mygroup/myapp!123' f003 --body "Tighter wording."
gitlab-reviewer review publish 'mygroup/myapp!123' --mode draft  # accepted findings only
gitlab-reviewer review submit 'mygroup/myapp!123'                 # publish the draft review
gitlab-reviewer mr approve 'mygroup/myapp!123' --sha <head_sha>
```

Triage only changes the stored record, exactly as in the TUI and GUI, and the record stays reopenable there. `review publish` posts nothing but accepted findings, skips findings that restate a comment already on the MR, and writes each outcome back into the record: `published`, `note` (posted as a general note), `below-threshold`, or `pending` when posting failed, as in the GUI, so a failed finding is re-accepted before the next publish. `review list` names each stored review, and `--record <name>` pins any of these commands to one; by default they use the newest. See the [CLI Reference](/gitlab-reviewer-cli/reference/cli/#gitlab-reviewer-review) for every command and its JSON fields.

`mr approve` pins the approval to `--sha` (for example the `head_sha` from `mr show` or `review show` that the review covered) or, without it, to the MR's current head, so GitLab refuses the approval if new commits arrived. With `gate.approvals: block` it refuses to approve while the newest stored review still has blocking findings.

## Claude Code plugin

The repository doubles as a Claude Code plugin marketplace whose `gitlab-reviewer` plugin ships a skill describing this loop, so Claude can drive it for you.

Inside Claude Code:

```text
/plugin marketplace add RobertYoung/gitlab-reviewer-cli
/plugin install gitlab-reviewer@gitlab-reviewer-cli
```

Or from a shell:

```sh
claude plugin marketplace add RobertYoung/gitlab-reviewer-cli
claude plugin install gitlab-reviewer@gitlab-reviewer-cli
```

On Claude Code 2.1.292 or later, `claude plugin install gitlab-reviewer --marketplace RobertYoung/gitlab-reviewer-cli` does both in one step. The plugin ships only the skill, not the binary: install `gitlab-reviewer` itself (see [Getting Started](/gitlab-reviewer-cli/getting-started/)) and make the `claude` CLI and a GitLab token available as usual. The skill keeps publishing, submitting, and approving behind the user's explicit say-so and runs `review` with the default `--publish none`.

## Exit codes and output

Every command prints JSON on stdout with `--output json` and progress and warnings on stderr. `review` exits `2` when the [severity gate](/gitlab-reviewer-cli/reference/configuration/#gate.min_severity) fails, and `mr approve` exits `2` when `gate.approvals: block` refuses the approval; any other failure exits `1`. See [Headless and CI](/gitlab-reviewer-cli/headless/#output-and-exit-codes) for the JSON shape and the [CLI reference](/gitlab-reviewer-cli/reference/cli/) for every command.
