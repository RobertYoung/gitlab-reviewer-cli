# Troubleshooting

## Startup

**"claude CLI … is too old (need >= 2.0)"** — update the Claude Code CLI
(`claude update`, or reinstall via npm/brew). The tool checks
`claude --version` before launching either frontend.

**claude binary not found** — install it
(`npm install -g @anthropic-ai/claude-code` or `brew install claude-code`)
or point `review.claude_path` at it if it is not on `PATH`.

**Missing GitLab token** — set `GITLAB_REVIEWER_GITLAB_TOKEN` (or
`GITLAB_TOKEN`). `gitlab-reviewer config validate` tells you exactly what
is missing. The token needs the `api` scope.

**"instance … token env var is unset"** — you selected an instance whose
`token_env` variable isn't exported in this shell. That is a deliberate
error, not a fallback to the shared token: export the named variable, or
remove `token_env` to use `gitlab.token`.

**Multiple instances in a non-interactive run** — pass `--instance <name>`
or set `gitlab.default_instance`; with several instances and no picker
there is no way to choose.

## Reviews

**"unknown agent" fails the run** — `review.agents` (or `--agents`) names
an agent that doesn't exist in the merged catalog. This is loud on
purpose — a typo should not silently review less. Check spelling against
the picker, and remember repo agents are only discoverable where the repo
provides them ([Review Agents](Review-Agents.md#how-repo-agents-are-discovered)).

**A custom agent doesn't appear in the picker** — invalid definition files
are skipped with a warning in the picker and the run log. Check the
frontmatter parses and the name matches `^[a-z0-9][a-z0-9_-]*$`. In
`clone` mode, repo agents are fetched over the API at the MR head — if the
MR doesn't contain the definition, it won't be offered.

**Review times out** — `review.timeout` (default `10m`) applies per pass.
Raise it, or reduce the work per pass: fewer agents, smaller
`review.max_diff_kb`, more excludes.

**Review stops early on a large MR** — check the run log (`l`) for budget
messages. `review.max_budget_usd` is a total split evenly across the
planned passes, so many agents × many chunks means a thin slice each.
Raise the budget or trim the agent selection.

**Findings on files you don't care about** — extend `review.exclude`. Note
that setting it replaces the default list (lockfiles, `vendor/**`,
generated/minified files), so re-include the defaults you want to keep.

**A finding landed as a general note instead of an inline comment** — its
position could not be resolved against the diff (or GitLab rejected it),
so it fell back per `publish.fallback_to_note`. The note carries the
file/line reference and a permalink. See
[Publishing](Publishing.md#positioning-and-the-note-fallback).

**`--bare` breaks authentication** — `claude --bare` skips OAuth/keychain
auth. Leave `review.bare` off if you authenticate with a Claude
subscription rather than an API key.

## TUI

**No file-explorer sidebar** — it needs a terminal at least 80 columns
wide and hides itself below that. Press `e` to toggle it once the window
is wide enough.

**A key doesn't respond** — some keys are contextual: `t`/`T` (chat) only
work when a chat backend is available; `l` on the review screen only after
a failed run; `P` only with pending manual comments. Press `?` for the
current keymap.

## GUI

**403 page** — your session cookie is missing (new browser profile,
cookies cleared, or the URL was shared without its token). Restart
`gitlab-reviewer gui` and open the freshly printed URL; the token is
single-session and the server only trusts its cookie.

**Pending comments disappeared after a restart** — pending manual comments
and running-review state are held in memory and die with the process.
Stored results and run logs survive.

## Logs and diagnostics

- Application log: `~/.local/state/gitlab-reviewer/gitlab-reviewer.log`
  (`log.file`); crank `log.level: debug` when reporting an issue.
- Per-run artifacts under `~/.local/state/gitlab-reviewer/reviews/`: the
  progress log (`review-<iid>-<ts>.log`, also viewable with `l` in the
  TUI), the findings record (`.json`), and the raw model transcript
  (`.jsonl`) — the transcript is the ground truth for "what did Claude
  actually do".
- `gitlab-reviewer config show` prints the effective merged configuration
  (secrets redacted) — the fastest way to see which layer a value came
  from when a flag, env var, and file disagree.
- Cache growing large? `gitlab-reviewer cache ls` then `cache clean` (or
  lower `checkout.cache_max_mb`).
