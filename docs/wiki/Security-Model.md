# Security model

Three things matter here: the review subprocess handles **untrusted MR
content**, the tool holds your **GitLab token**, and the GUI is a **local
web server** acting with that token. Each gets its own defence.

## The review sandbox

The review runs Claude in headless, non-interactive mode
(`--permission-mode dontAsk`) over a checkout of code written by the **MR
author**, who may not be you and may not be trusted. The MR's diff, title,
description, and commit messages are all fed into the prompt as untrusted
text — a prompt-injection surface.

The defence is capability restriction: the review session is allowed only
`Read`, `Grep`, and `Glob`; `Bash`, `Edit`/`Write`, and **all network
tools (`WebFetch`, `WebSearch`) are denied**, and those denials cascade
into any delegated subagents (`review.use_agents`).

That network denial is deliberate and load-bearing. Even a fully hijacked
model can *read* local files (`~/.aws/credentials`, `.env`, SSH keys) but
has no tool with which to *transmit* them — the sandbox is what turns a
possible data-exfiltration into a non-event. If you ever need the
subprocess to reach the network, do it with an egress allowlist at the
OS/proxy layer (permit only your GitLab host and the model endpoint), not
by removing tools from the deny list.

Related boundaries:

- Repo-shipped agent definitions steer the reviewer's attention but cannot
  alter tool permissions ([Review Agents](Review-Agents.md)).
- `--strict-mcp-config` is always passed, so `.mcp.json` files — including
  one shipped in the reviewed repo — are never loaded.
- `review.env` values are forwarded to the subprocess, but `GITLAB*` keys
  are stripped.

### MR hygiene checks without network access

Beyond code review, the reviewer surfaces lightweight MR *hygiene*
signals without ever giving the model network access — GitLab metadata is
fetched by the **tool itself** over the API and injected into the prompt
as text, or computed directly:

- **Rebase status** is computed from the MR's diverged-commits count and
  conflict state and shown as a warning on the findings screen when the
  branch is behind its target. Always on, no configuration.
- **Commit messages**, the **MR description**, and the project's **default
  MR template** (including group-inherited templates) are placed in the
  prompt alongside the diff. These power *opt-in* checks you enable
  through `review.instructions` — see
  [Recipes](Recipes.md#mr-hygiene-checks) for a ready-made block.

Checks that would need *live* GitLab writes or arbitrary web access are
deliberately out of scope.

### The MCP exception

The one deliberate, opt-in relaxation is
[`review.mcp_servers`](MCP-Servers.md): a server you grant there runs
inside the review subprocess with whatever reach *it* has. `WebFetch`/
`WebSearch` stay denied, `--strict-mcp-config` stays on, and the GitLab
token still never enters the subprocess — but the exfiltration analysis is
now only as good as the granted server's egress. Choose servers whose
destinations are narrow and trusted, scope grants per-project, and back
them with an OS/proxy egress allowlist when the stakes warrant it.

## The GitLab token

Pass the token via environment (`GITLAB_REVIEWER_GITLAB_TOKEN`,
`GITLAB_TOKEN`, or per-instance `token_env`), not flags — flags are
visible in `ps` and shell history. The token (including every per-instance
token):

- is never logged and is redacted from error messages and `config show`;
- is handed to git through an in-memory credential helper — it never lands
  in `.git/config`, remotes, or process arguments;
- is **never** passed to the `claude` subprocess: `GITLAB*` keys are
  stripped from `review.env` and rejected in MCP server definitions.

OS keychain storage is a planned enhancement.

## The browser GUI's session security

`gitlab-reviewer gui` drives publishes and approvals with your token, so
it must not be drivable by other local processes or web pages:

- The server binds to **`127.0.0.1` only**.
- Every session is protected by a random token baked into the launch URL;
  opening it once sets a strict same-site, http-only session cookie and
  strips the token from the address bar. Requests without the cookie get
  a 403.
- State-changing requests carry cross-origin protection (Sec-Fetch-Site
  checking), and responses set `Cache-Control: no-store` and
  `X-Content-Type-Options: nosniff`.

## What ends up on disk

Review artifacts — raw model transcripts (`.jsonl`, including chat
transcripts), run logs, and findings with curation state — are stored
under `~/.local/state/gitlab-reviewer/reviews/` with owner-only file
modes. They can contain source code from reviewed MRs, so treat the state
directory with the same care as your clones.
