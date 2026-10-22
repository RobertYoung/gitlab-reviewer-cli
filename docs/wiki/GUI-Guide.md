# GUI guide

`gitlab-reviewer gui` serves the same review workflow as a local web app.
It shares every core component with the TUI — reviews run through the same
pipeline, results land in the same stores, and a review started in one
frontend can be reopened in the other. The
[feature matrix](https://github.com/RobertYoung/gitlab-reviewer-cli/blob/main/docs/features.md)
tracks the few deliberate differences.

## Launching

```sh
gitlab-reviewer gui                # random free port, opens your browser
gitlab-reviewer gui --port 8080    # fixed port
gitlab-reviewer gui --no-browser   # just print the URL
```

The server binds to `127.0.0.1` only and prints a launch URL containing a
per-session random token. Opening the URL once exchanges the token for a
strict same-site, http-only session cookie and strips it from the address
bar; requests without the cookie get a 403. Other local processes cannot
drive your review session. See
[Security Model](Security-Model.md#the-browser-guis-session-security).

With several `gitlab.instances` configured, the GUI starts on an instance
picker and serves each instance under its own URL path — no `--instance`
flag needed.

## Appearance

The GUI follows your OS light/dark preference and remembers an explicit
choice made with the sun/moon toggle in the top navigation bar. Syntax highlighting
switches with the theme.

## Keyboard shortcuts

Press `?` on any page for the full list. The diff view has `]`/`[` for
next/previous hunk and `n`/`p` for next/previous file; the findings page
has `j`/`k` to move between findings, `a` accept, `x` reject, `e` edit,
and `A` accept-all. `⌘`/`ctrl`+`enter` submits comment and chat forms,
`esc` closes the inline comment form.

## The screens

The GUI mirrors the TUI screen for screen. A top navigation bar holds the instance (a switcher when several are configured), Merge requests, Browse and Settings, plus the shortcut help and theme toggle.

- **MR list**: filter by state with the tabs, and by author, target branch and free-text search with the filter bar (filters are URL parameters, so views are bookmarkable). Each row shows the MR with its project, branches and author, then three columns that fill in lazily after the list renders: the head pipeline's outcome (linking to the pipeline in GitLab), the unresolved discussion-thread count (amber while threads are open, muted once all are resolved), and the AI review stage of the latest stored review (triage with N pending, ready to publish, published, or no findings), linking to the MR's past reviews.
- **MR header and review pipeline**: every MR page opens with the same header: breadcrumb, title and state, author and branches (each linking to GitLab), then the review pipeline: AI review, Triage, Publish and Approve, each with its count and a link to where that step happens. The first unfinished step is highlighted and the next action is the one primary button: Run AI review, Continue triage, Publish N accepted, or Approve. It follows triage as you work, and while a review is running it links back to its progress. Tabs below switch between Overview, Changes, Findings and Past reviews, with Chat with Claude at the right.
- **Overview**: the description rendered as markdown, the commit list, your pending comments with the MR-level comment form, an approval card with approve/unapprove, and the MR's details (pipeline, branches, head commit). A needs-rebase warning shows when the branch is behind its target. With a [severity gate](Publishing.md#severity-gate) configured, blocking findings show a warning banner; `warn` relabels the button "Approve anyway" and `block` disables it (the server refuses direct POSTs too).
- **Changes (diff view)**: syntax-highlighted, soft-wrapped diffs in unified or side-by-side layout (default from `ui.diff_view`, switchable in the file panel), with the changed span inside modified line pairs emphasised. The sticky file panel shows collapsible folders with comment counts and a severity dot per finding, filters by path, and highlights the file you are reading. Existing MR discussions render inline where they were made, the MR description folds open above the diff, and each file header has a *Viewed* toggle that collapses the file and ticks it off in the panel (tracked per head commit, so a new push resets it); clicking a header folds the file. The latest review's findings appear inline on their lines and can be accepted or rejected in place.
- **Review options page**: Run AI review opens an options page before the run starts. It lists the review agents matching the TUI's picker, each with a model dropdown (see [Review Agents](Review-Agents.md#per-agent-model)), plus per-run overrides for the run-wide model, agent concurrency (`review.agent_concurrency`), max budget (`review.max_budget_usd`), extra instructions appended to the review prompt, and any configured web or shell grants. Everything is remembered per project; cleared fields revert to the configured defaults, and the agent selection and model picks are shared with the TUI. Once the MR has a stored review, runs default to an incremental re-review (only the changes since the last reviewed commit, previous findings and their curation states carried forward); a *Full re-review* checkbox forces scanning the entire diff.
- **Review progress**: the run log streams live over server-sent events, with a per-agent status strip (running, done with finding count, failed; click a chip to filter the log to that agent) above it; the page jumps to the findings when the run completes. A cancel button stops the run.
- **Findings**: the review summary, then a list of findings grouped by state beside the selected finding's detail (body, suggested change, the diff excerpt). Accept, reject, edit and publish without page reloads; triaging a pending finding moves on to the next pending one, and chips filter by state or severity. The location link opens the same finding inline in the diff.
- **Past reviews**: reopen any stored review with its curation state, or its run log.

## Commenting and chat

- **Click-to-comment**: the `+` button on any diff line opens an inline
  comment form (both layouts). `⌘`/`ctrl`+`enter` submits, `esc` closes.
  Pending comments can be deleted before publishing, published on their
  own, or left to ride along with the next review run.
- **General comments**: an MR-level comment form on the detail page.
- **Chat**: "Chat with Claude" on the MR overview (or the diff sidebar)
  opens a conversation about the whole MR; the `+` menu on any diff line
  offers "Ask Claude" alongside "Add comment" — whatever you typed becomes
  the first message of a chat anchored to that line. Replies stream their
  progress live; `⌘`/`ctrl`+`enter` sends. The conversation is multi-turn
  and continues until you end the chat.

## Auto-publish

With `publish.auto_comment` enabled, the GUI publishes qualifying findings
straight after the run, like the TUI. In draft mode the notes are created
as a pending review and the run page offers a one-click *Publish review
now*.

## Differences from the TUI

- The GUI can **delete** a pending manual comment; the TUI cannot (it can
  only publish or let it ride into a review).
- The GUI shows review findings inline on the diff, tracks viewed files,
  emphasises word-level changes, and badges reviewed MRs on the list —
  TUI counterparts are tracked in the feature matrix.
- Browser-native affordances apply: text selection, in-page search, soft
  wrapping.

## Lifecycle notes

Stored review results and run logs survive restarts (they live in the
shared state directory). Pending manual comments and running-review state
are held in memory and die with the process — publish or finish before
stopping the server. Viewed-file ticks live in the browser's local
storage.
