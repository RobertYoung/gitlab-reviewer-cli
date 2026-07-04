# TUI guide

`gitlab-reviewer` (no subcommand) launches the terminal UI. Press `?` on
any screen for the built-in keybinding reference; this page is the complete
tour. Keys listed as `x/y` mean either works.

Global keys: `?` help, `ctrl+c` quit, `esc` go back / cancel (screen-specific).

## Instance picker

Shown first only when several [`gitlab.instances`](Configuration-Reference.md#multiple-gitlab-instances)
are configured and none is named via `--instance` or
`gitlab.default_instance`.

| Key | Action |
|---|---|
| `↑/↓` `j/k` | move |
| `enter` | select instance |
| `q` / `esc` | quit |

## Group/project selector

Shown when no projects or groups are configured — browse your available
groups and projects and pick a scope without touching the settings file.

| Key | Action |
|---|---|
| `↑/↓` `j/k` | move |
| `enter` | open group / browse project / "your projects" |
| `b` | browse all MRs in the selected group |
| `/` | search (server-side) |
| `esc` | clear search, else back to groups |
| `q` | quit |

## Merge request list

| Key | Action |
|---|---|
| `↑/↓` `j/k` | move (more MRs load as you near the end) |
| `enter` | open the MR detail screen |
| `o` | open the MR in your browser |
| `/` | free-text search |
| `a` | filter by author |
| `t` | filter by target branch |
| `s` | cycle state: opened → merged → closed → all |
| `r` | reload |
| `esc` | clear active filters, else back to the selector |
| `q` | quit |

The text filters open an input line: `enter` applies, `esc` cancels.

## MR detail (diff view)

The heart of the TUI: the diff with a movable line cursor, plus everything
you can do to an MR.

### Navigating the diff

| Key | Action |
|---|---|
| `↑/↓` `j/k` | move the line cursor |
| `n`/`p` (or `→`/`←`) | next / previous file |
| `]`/`[` | next / previous hunk |
| `g`/`G` | top / bottom |
| `v` | toggle unified / side-by-side layout (default: `ui.diff_view`) |
| `d` | flip to the MR overview — description and commit list (`d`/`esc` flips back) |
| `o` | open the MR in your browser |
| `esc` | back to the MR list |

### The file explorer

A collapsible directory tree of the changed files, with a status glyph per
file (`A`dded, `M`odified, `D`eleted, `R`enamed) and the number of
discussion threads anchored to it. Initial state comes from
`ui.file_explorer` (default closed). It needs a terminal at least 80
columns wide and hides itself below that.

| Key | Action |
|---|---|
| `e` | show / hide the explorer |
| `tab` | switch focus between explorer and diff |
| `↑/↓` `j/k` | move (when focused) |
| `enter` / `l` / `→` | open file / unfold directory |
| `h` / `←` | fold directory, or jump to parent |
| `g`/`G` | top / bottom |
| `esc` | focus back to the diff |

### Acting on the MR

| Key | Action |
|---|---|
| `a` | approve, or remove your approval. The approval is pinned to the head commit you reviewed; the header shows who has approved. With a [severity gate](Publishing.md#severity-gate) configured, blocking findings in the last review make `warn` ask for a confirming second press and `block` refuse |
| `c` | comment on the selected line (`ctrl+s` saves, `esc` discards) |
| `C` | general MR-level comment |
| `P` | publish pending manual comments on their own |
| `t` | chat with Claude about the selected line |
| `T` | chat with Claude about the whole MR |
| `r` | run an AI review (opens the agent picker; pending comments ride along) |
| `L` | browse past reviews of this MR |

Manual comments post **verbatim** — no template or attribution footer —
and go through the same publish pipeline as review findings: publish them
directly with `P`, or press `r` and curate them together with the review's
findings. Until published they exist only in this session.

## Agent picker (`r` on the MR detail)

Choose which [review agents](Review-Agents.md) run in this scan. The
selection is seeded from your last choice for this project (falling back
to `review.agents`), and saved when you start the review.

| Key | Action |
|---|---|
| `↑/↓` `j/k` | move |
| `space` | toggle agent |
| `a` / `n` | select all / none |
| `enter` | start the review |
| `esc` / `q` | back |

In `clone` checkout mode, repo-shipped agents are fetched over the GitLab
API in the background and appear in the picker when they arrive; invalid
definitions are listed with a warning.

## Review run

| Key | Action |
|---|---|
| `esc` | cancel the running review; when done, go back |
| `l` | view the run log (after a failure) |
| `o` | open the MR in your browser |

Progress lines are prefixed per agent and persisted to a run log you can
reopen later. Large MRs are split into several passes automatically.

## Findings

Every suggested comment, shown against its diff hunk, with severity,
category, and the agent that produced it.

| Key | Action |
|---|---|
| `↑/↓` `j/k` | move |
| `a` / `x` | accept / reject |
| `A` | accept all pending |
| `e` | edit the comment body (`ctrl+s` saves, `esc` discards) |
| `c` | add your own MR-level comment |
| `p` | publish accepted findings |
| `l` | view this run's progress log |
| `o` | open the MR in your browser |
| `esc` | back |

Curation state is saved on every change — you can leave and reopen the
review later (`L` on the MR detail) without losing decisions.

Findings below [`publish.min_severity`](Publishing.md#publish-floor-publishmin_severity)
are marked `below-threshold`: they stay visible here for context but are
never posted to GitLab, even if accepted.

With `publish.auto_comment` enabled, findings at or above
`publish.auto_min_severity` are published automatically when this screen
opens; the rest wait for your decision ([Publishing](Publishing.md)).

## Publish

| Key | Action |
|---|---|
| `enter` | start publishing |
| `m` | toggle draft / immediate mode for this run |
| `P` | publish the draft review (draft mode, once notes are created) |
| `esc` | draft mode: keep the notes as pending drafts for the GitLab web UI |
| `o` | open the MR in your browser |

## Past reviews (`L` on the MR detail)

Every stored review of the MR, newest first — results with their curation
states, plus orphaned run logs.

| Key | Action |
|---|---|
| `↑/↓` `j/k` | move |
| `enter` | reopen the review's findings, curation state included |
| `l` | view the run's progress log |
| `esc` / `q` | back / quit |

The log viewer scrolls with `↑/↓`, `g`/`G`.

## Chat (`t`/`T` on the MR detail)

A multi-turn conversation with Claude about the MR (or one diff line),
running inside a checkout of the MR branch — Claude reads the surrounding
code, callers, and tests while answering.

| Key | Action |
|---|---|
| `ctrl+s` | send your message |
| `pgup`/`pgdn`, `ctrl+u`/`ctrl+d` | scroll the conversation |
| `esc` | cancel the reply being written; pressed again, end the chat |

Nothing from a chat is posted to GitLab, and the conversation is not
stored (a raw `chat-*.jsonl` debug transcript does land in the state
directory, like review transcripts). The checkout is released when the
chat ends.
