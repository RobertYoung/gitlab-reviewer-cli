---
title: Getting started
description: "Install gitlab-reviewer, connect it to GitLab and Claude, and publish your first review."
---

This page takes you from nothing to your first published review.

## 1. Install the prerequisites

You need three things on your `PATH`:

- **The `claude` CLI, version ≥ 2.0**: `gitlab-reviewer` shells out to it for every review. Install per the [Claude Code docs](https://docs.anthropic.com/en/docs/claude-code) (`npm install -g @anthropic-ai/claude-code` or `brew install claude-code`), then make sure it is authenticated: an Anthropic API key, a Claude subscription login, or AWS Bedrock access (see [Recipes: Bedrock](/gitlab-reviewer-cli/recipes/#reviewing-via-aws-bedrock)).
- **`git`**: checkouts and worktrees are plain git operations.
- **`gitlab-reviewer` itself**: download a prebuilt binary from the [releases page](https://github.com/RobertYoung/gitlab-reviewer-cli/releases) or build from source:

  ```sh
  go install github.com/RobertYoung/gitlab-reviewer-cli/cmd/gitlab-reviewer@latest
  ```

The tool checks `claude --version` at startup and refuses to run with a version older than 2.0, with a pointer to `claude update`.

## 2. Create a GitLab token

Create a personal (or project) access token with the `api` scope: *Settings → Access tokens* in GitLab. Draft reviews need GitLab ≥ 16.x.

Export it as an environment variable (never pass it as a flag: see [Security Model](/gitlab-reviewer-cli/security-model/#the-gitlab-token)):

```sh
export GITLAB_REVIEWER_GITLAB_TOKEN=glpat-...
# GITLAB_TOKEN also works if the prefixed variable is unset
```

## 3. First run

You can start with zero configuration:

```sh
gitlab-reviewer
```

With no projects or groups configured, the TUI opens a picker listing your available groups and projects: choose one and you are browsing its open MRs. To scope it up front instead:

```sh
gitlab-reviewer --project mygroup/myapp          # one project
gitlab-reviewer --group platform-team            # a whole group
```

For a self-hosted GitLab, set the base URL too:

```sh
export GITLAB_REVIEWER_GITLAB_BASE_URL=https://gitlab.example.com
```

## 4. A settings file, so you stop typing flags

Create `~/.config/gitlab-reviewer/config.yaml` (the path honours `XDG_CONFIG_HOME`):

```yaml
gitlab:
  base_url: https://gitlab.example.com   # omit for gitlab.com
  projects:
    - mygroup/myapp
  groups:
    - platform-team
```

Then check it:

```sh
gitlab-reviewer config validate   # complete and consistent?
gitlab-reviewer config show       # effective settings, secrets redacted
```

Every setting is also available as a flag and an environment variable, with precedence **flags > environment > file > defaults**: the [Configuration Reference](/gitlab-reviewer-cli/reference/configuration/) lists all three names for every key.

## 5. Your first review

1. Pick an MR from the list and press `enter`: you get the MR detail screen with the diff. Press `d` for an overview (description and commits), `e` for the changed-files explorer.
2. Press `r`. An **agent picker** appears so you can choose which review agents run. The built-ins are `bug`, `security`, `performance`, `docs`, `style`, and `design` ([Review Agents](/gitlab-reviewer-cli/review-agents/)). `space` toggles, `enter` starts. Your selection is remembered per project.
3. Watch the progress log while Claude explores the checkout. Reviews run in a detached git worktree at the MR head commit, never in your working tree. Press `esc` to cancel.
4. When the run finishes you land on the **findings** screen. For each finding: `a` accept, `x` reject, `e` edit the comment text (`ctrl+s` saves). `A` accepts everything pending.
5. Press `p` to publish the accepted findings. In the default **draft** mode they become a GitLab draft review you publish in one action (`P`), or leave pending to finish in the GitLab web UI. Press `m` on the publish screen to switch to **immediate** posting for this run.

That's the whole loop. Comments land as positioned inline discussions; anything that cannot be anchored to the diff falls back to a general MR note ([Publishing](/gitlab-reviewer-cli/publishing/)).

## 6. Where things live on disk

| Purpose | Default path |
|---|---|
| Settings file | `${XDG_CONFIG_HOME:-~/.config}/gitlab-reviewer/config.yaml` |
| Your custom agents | `${XDG_CONFIG_HOME:-~/.config}/gitlab-reviewer/agents/` (plus `~/.claude/agents/`) |
| Clone cache + worktrees | `${XDG_CACHE_HOME:-~/.cache}/gitlab-reviewer/` |
| Review results, run logs, raw transcripts | `${XDG_STATE_HOME:-~/.local/state}/gitlab-reviewer/reviews/` |
| Log file | `${XDG_STATE_HOME:-~/.local/state}/gitlab-reviewer/gitlab-reviewer.log` |
| Remembered agent selections | `${XDG_STATE_HOME:-~/.local/state}/gitlab-reviewer/agent-selection.json` |

Reviews are persisted automatically: results (findings plus your accept/reject decisions) are saved when a run completes and re-saved on every curation change, so you can close the terminal and pick up where you left off: press `L` on the MR detail screen to browse past reviews.

## Next steps

- [TUI Guide](/gitlab-reviewer-cli/tui/): everything else the TUI does: chat with Claude about the change, approve MRs, leave your own comments, filters.
- [GUI Guide](/gitlab-reviewer-cli/gui/): the same workflow in your browser via `gitlab-reviewer gui`.
- [Recipes](/gitlab-reviewer-cli/recipes/): worked configurations for common setups.
