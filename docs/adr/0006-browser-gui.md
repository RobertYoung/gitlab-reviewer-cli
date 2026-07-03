# ADR-0006: Server-rendered browser GUI over the shared core

## Status

Accepted

## Context

The terminal caps how much the TUI can show and how richly it can render:
diff width fights the file explorer, intra-line and syntax highlighting are
limited by cell-based output, and inline discussion threads compete with
code for rows. A browser removes those limits, but the GUI must not fork
the review logic, must ship inside the single released binary, and — since
it drives publishes with the user's GitLab token — must not be drivable by
other local processes.

## Decision

Serve the GUI from the Go binary (`gitlab-reviewer gui`) as server-rendered
`html/template` pages with a thin vanilla-JS layer, all embedded via
`go:embed`. No SPA, no Node toolchain, no new runtime dependencies beyond
the chroma HTML formatter already in the module.

Business logic that both frontends need was extracted rather than
duplicated: `internal/review/runner` owns the run pipeline (checkout →
prompt → passes → merge → stored record) and `internal/review/publisher`
owns posting (position resolution, draft/immediate, note fallback). The
TUI's screens and the GUI's handlers are both thin frontends over these,
and both persist to the same resultstore/runlog stores, so reviews reopen
across frontends.

Reviews run in server goroutines tracked by a run registry; progress
streams to the page over server-sent events (one-directional, reconnect
gets a full replay — WebSockets would buy nothing here). Security: the
server binds to `127.0.0.1` only, and every request requires a per-session
random token, delivered once in the launch URL and exchanged for a strict
same-site, http-only cookie.

## Consequences

- One binary serves both frontends; the GUI works offline from any modern
  browser with no install step.
- Server-rendered pages keep state on the server (stores, run registry,
  pending comments), so curation state stays consistent with the TUI's.
- Interactivity is deliberately modest (forms + redirects + SSE). Rich
  client-side behaviour like optimistic updates or side-by-side diffs
  needs incremental JS rather than coming free from a framework.
- Session state (pending manual comments, run registry) is in-memory and
  dies with the process; stored records and logs survive.
