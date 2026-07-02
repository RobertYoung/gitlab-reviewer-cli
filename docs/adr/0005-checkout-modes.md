# ADR-0005: Three checkout modes, one worktree invariant

## Status

Accepted

## Context

Claude Code needs the repository on disk. Users differ: some want the tool
to manage clones, some already have a clone, some keep all clones under a
structured root like `~/git/$HOST/$PROJECT_PATH`.

## Decision

Support three modes behind one `checkout.Manager`:

1. **clone** (default) — cached clone per project under the XDG cache dir,
   fetched on reuse, with LRU size-based eviction and a `cache clean`
   command.
2. **path** — user-supplied existing clone (remote verified against the
   project).
3. **root** — resolve `<root>/<host>/<project_path>` automatically.

All modes converge on the same invariant: the review runs in a **detached
`git worktree` at the MR head SHA** (fetched via GitLab's
`refs/merge-requests/<iid>/head`), never in the user's working tree, so a
review can never touch uncommitted work and two reviews cannot interfere.

Transport is configurable: HTTPS (default) with the token supplied through
an inline git credential helper that reads it from an environment variable
scoped to the git subprocess — the token never lands in `.git/config`,
remotes, or process arguments — or SSH using the user's own agent and keys.

**Local convention overlay.** Teams sometimes keep Claude convention files
(`CLAUDE.md`, `.claude/` agents and skills) locally via `.git/info/exclude`
before committing them, which a clean worktree would hide from the
reviewer. In `path`/`root` modes, untracked files in the source clone
matching `checkout.local_overlay` globs are copied into the worktree after
creation. Guardrails: the default globs cover only the files Claude Code
itself reads (so an excluded `.env` is never swept in), paths tracked at
the MR head are never overridden (the review must see committed state),
symlinks are skipped, and writes are containment-checked against the
worktree root.

## Consequences

- Mode-independent review code: everything downstream sees "a path at the
  right SHA".
- Worktrees are cheap but must be cleaned up (`checkout.keep` opts out);
  the cache needs size management, handled by eviction + `cache clean`.
