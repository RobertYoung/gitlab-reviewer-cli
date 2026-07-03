# ADR-0008: Opt-in MCP servers for the review session

## Status

Accepted

## Context

Some reviews benefit from live reference material the model should not
guess at — the motivating case is an AWS IAM repository whose agents want
to cross-check policies against the AWS documentation MCP server. But the
review sandbox (ADR-0001) denies all network tools on purpose: MR content
is untrusted, and denying egress is what makes prompt injection harmless.
Reaching the network through an MCP server is a real relaxation of that
guarantee, so it must be explicit, scoped, and never influenced by the
repository under review.

## Decision

Add `review.mcp_servers` — a settings-file-only map of named server
definitions mirroring Claude Code's `.mcp.json` (stdio via
`command`/`args`/`env`, remote via `url`/`headers`). When set, the backend
passes the definitions as an inline `--mcp-config` and allow-lists the
servers' tools (`mcp__<name>`, or `mcp__<name>__<tool>` when the
definition's optional `tools` list narrows the grant) — required because
`--permission-mode dontAsk` denies MCP tools without an allow rule.

MCP-enabled runs omit the `--tools` flag: an explicit `--tools` list
strips MCP tools regardless of how they are named in it (verified against
claude 2.1.199, contrary to its documentation). Read-only is enforced
through permissions instead — the deny list extended with the
capability-bearing built-ins `--tools` used to exclude
(`SlashCommand`, `Skill`), and dontAsk denying every remaining tool that
is neither read-only nor allow-listed.

Boundaries that hold the trust model:

- `--strict-mcp-config` stays on: only the explicit grant loads; ambient
  `.mcp.json` files, including one shipped in the reviewed repo, never do.
- Grants come from the user's settings file (globally or per-project),
  never from repo-shipped agent definitions — the reviewed repo cannot
  grant itself network access (upholding ADR-0007's invariant).
- `WebFetch`/`WebSearch` and write/exec tools stay denied; `GITLAB*` env
  keys in server definitions are rejected at validation; remote-server
  headers are redacted from `config show`.
- No flag or environment form: a network grant should not be introducible
  from a one-off command line.

## Consequences

- Reviewers can consult live documentation; the IAM use case works with a
  three-line per-project override.
- Every granted server widens the exfiltration surface to that server's
  egress. The README steers users toward narrow-egress servers,
  per-project grants, and an OS/proxy allowlist for defence in depth.
- Enforcement still relies on the claude CLI honouring its flags, as
  everywhere else in this backend (ADR-0001).
