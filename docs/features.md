# Features by frontend

Both frontends — the TUI (`gitlab-reviewer`) and the browser GUI
(`gitlab-reviewer gui`) — sit over the same core packages: reviews run
through the same pipeline, results land in the same stores, and a review
started in one frontend can be reopened in the other. This page tracks
which user-facing features each frontend offers. Anything marked missing
on one side is a parity gap; anything marked n/a only makes sense in the
other modality (keyboard-driven vs browser-native).

## Browsing

| Feature | TUI | GUI | Notes |
|---|:-:|:-:|---|
| Instance picker (multiple `gitlab.instances`) | ✅ | ✅ | GUI serves each instance under its own URL path |
| Group/project scope picker when nothing is configured | ✅ | ✅ | |
| MR list across configured projects/groups | ✅ | ✅ | |
| Filter by state, search text, author, target branch | ✅ | ✅ | TUI: `s`/`/`/`a`/`t`; GUI: filter bar / URL params |
| Pagination | ✅ | ✅ | TUI loads more as you scroll; GUI pages |
| Open MR in browser | ✅ | ✅ | TUI: `o`; GUI: "open in GitLab" link |

## MR detail

| Feature | TUI | GUI | Notes |
|---|:-:|:-:|---|
| Title, branches, author, state, draft badge | ✅ | ✅ | |
| Description and commit list | ✅ | ✅ | TUI: `d` toggles the overview over the diff |
| Needs-rebase / conflict warning | ✅ | ✅ | |
| Approval status (who approved, approvals outstanding) | ✅ | ✅ | |
| Approve / unapprove | ✅ | ✅ | Approval is pinned to the head commit that was on screen; TUI: `a` |

## Diff

| Feature | TUI | GUI | Notes |
|---|:-:|:-:|---|
| Unified diff with syntax highlighting | ✅ | ✅ | |
| Split (side-by-side) layout | ✅ | ✅ | TUI: `v` toggles; GUI: layout links in the sidebar; `ui.diff_view` sets the default for both |
| File explorer: collapsible directory tree, status glyphs, comment counts | ✅ | ✅ | TUI: `e` shows/hides, `h`/`l` fold; GUI: sticky sidebar, folders fold in place |
| Existing GitLab discussions shown inline | ✅ | ✅ | |
| Hunk-to-hunk navigation | ✅ | n/a | TUI `]`/`[`; the browser scrolls |
| Soft wrapping, text selection, in-page search | n/a | ✅ | Browser-native |

## Commenting

| Feature | TUI | GUI | Notes |
|---|:-:|:-:|---|
| Manual line-anchored comments | ✅ | ✅ | TUI: line cursor + `c`; GUI: click `+` on a line (both layouts) |
| Manual MR-level comments | ✅ | ✅ | TUI: `C` |
| Delete a pending comment | ✅ | ✅ | |
| Publish pending comments on their own | ✅ | ✅ | TUI: `P` |
| Pending comments ride along with a review run | ✅ | ✅ | |

## Review

| Feature | TUI | GUI | Notes |
|---|:-:|:-:|---|
| Run AI review (multi-pass for large MRs) | ✅ | ✅ | Same runner |
| Live progress log | ✅ | ✅ | GUI streams over server-sent events |
| Cancel a running review | ✅ | ✅ | |
| Findings triage: accept / reject / edit / accept-all | ✅ | ✅ | |
| Publish immediately or as a draft review | ✅ | ✅ | |
| Position fallback to a general note | ✅ | ✅ | Shared publisher |
| Past reviews: reopen findings with curation state | ✅ | ✅ | Shared result store — cross-frontend |
| View a run's progress log later | ✅ | ✅ | |

## Modality-specific

| Feature | TUI | GUI | Notes |
|---|:-:|:-:|---|
| Full keyboard driving + `?` help screen | ✅ | n/a | GUI is form/link-driven; `⌘`/`ctrl`+`enter` submits comment forms, `esc` cancels |
| Session token auth, loopback-only server | n/a | ✅ | GUI security model |

When adding a feature to one frontend, add it to the other (or record it
here as `n/a` with the reason) and update this table.
