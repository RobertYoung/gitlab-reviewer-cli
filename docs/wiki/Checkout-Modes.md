# Checkout modes

Claude reviews with full repository context, so the repository has to be on
disk. `checkout.mode` decides where it comes from; whatever the mode, the
review itself always runs in a **detached git worktree at the MR head
commit** — never in your working tree, so a review can never touch
uncommitted work and two reviews cannot interfere.

## `clone` (default) — the tool manages clones

The tool keeps cached bare clones under the cache dir
(`checkout.cache_dir`, default `~/.cache/gitlab-reviewer`), fetching MR
branches on demand. Nothing to configure; any project you can browse can
be reviewed.

```sh
gitlab-reviewer cache ls           # what is cached, with sizes
gitlab-reviewer cache clean        # remove worktrees, evict LRU clones over budget
gitlab-reviewer cache clean --all  # empty the cache entirely
```

The cache is bounded by `checkout.cache_max_mb` (default 2048 MiB);
least-recently-used clones are evicted over that budget, both at startup
and on `cache clean`.

## `path` — you point at an existing clone

```yaml
checkout:
  mode: path
  path: ~/git/myapp
```

Your clone's origin remote is verified against the MR's project, then MR
head commits are fetched into it and worktrees are created from it. Use
this when you already have the repository and want to reuse its objects —
or when you want the [local overlay](#local-convention-files-uncommitted-claudemd-claude)
and local agent discovery, which need a local clone.

`path` mode naturally suits a single project; use `root` for many.

## `root` — clones under a structured root

```yaml
checkout:
  mode: root
  root: ~/git
  clone_missing: true   # optional: create missing clones
```

Clones are resolved as `<root>/<host>/<group>/<project>` (e.g.
`~/git/gitlab.com/mygroup/myapp`). With `clone_missing: true`, a project
you haven't cloned yet is cloned into place; otherwise a missing clone is
an error. Note `checkout.clone_missing` has an environment variable but no
flag.

## Transport and credentials

`checkout.transport` picks how clones and fetches talk to GitLab:

- **`https`** (default) — the GitLab token is supplied through an
  in-memory credential helper scoped to the git subprocess; it never lands
  in `.git/config`, remotes, or process arguments.
- **`ssh`** — uses your own SSH agent and keys.

## Worktree lifecycle

Review worktrees live under the cache dir and are removed when the review
finishes. Set `checkout.keep: true` (or `--keep-worktree`) to keep them for
debugging — `cache clean` removes them later.

## Local convention files (uncommitted CLAUDE.md, .claude/)

Teams often keep Claude conventions — `CLAUDE.md`, `.claude/` agents and
skills — locally before they are ready to commit them, typically listed in
`.git/info/exclude`. Because reviews run in a clean worktree, those files
would normally be invisible to the reviewer.

In `path` and `root` modes, untracked files in your clone matching
`checkout.local_overlay` are copied into the review worktree so Claude
follows them. The default globs cover exactly the files Claude Code reads
(`**/CLAUDE.md`, `**/CLAUDE.local.md`, `.claude/**`); extend them per
project for other convention files:

```yaml
projects:
  mygroup/myapp:
    checkout:
      local_overlay: ["**/CLAUDE.md", "**/CLAUDE.local.md", ".claude/**", "Taskfile*.yaml"]
```

Guardrails:

- Files tracked at the MR head commit are never overridden — the review
  always sees the committed state of real code.
- Nothing else from `.git/info/exclude` (e.g. a local `.env`) is copied
  unless a glob matches it.
- Symlinks are skipped, and writes are containment-checked against the
  worktree root.

Untracked repo agent definitions in `.gitlab-reviewer/agents/` and
`.claude/agents/` are picked up by the same local-clone reading — see
[Review Agents](Review-Agents.md#how-repo-agents-are-discovered).
