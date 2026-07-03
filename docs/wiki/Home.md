# gitlab-reviewer wiki

`gitlab-reviewer` reviews GitLab merge requests with Claude, from a
terminal UI or a local browser GUI: it checks the MR branch out locally,
runs focused review agents with full repository context, and lets you
curate every suggested comment before anything is published back to the MR.

This wiki is the long-form documentation: usage guides with examples and a
complete reference for every configuration setting. For a quick overview
and installation, start with the
[README](https://github.com/RobertYoung/gitlab-reviewer-cli#readme).

## Guides

| Page | What it covers |
|---|---|
| [Getting Started](Getting-Started.md) | Install, token setup, your first review end to end |
| [TUI Guide](TUI-Guide.md) | Every screen and keybinding: browsing, reviewing, curating, chat, approvals |
| [GUI Guide](GUI-Guide.md) | The browser frontend: launching, screens, shortcuts, its security model |
| [Review Agents](Review-Agents.md) | Built-in reviewers, per-scan selection, cost control, writing your own agents |
| [MCP Servers](MCP-Servers.md) | Granting the review session live reference material, safely |
| [Checkout Modes](Checkout-Modes.md) | Managed clones vs your own, the cache, worktrees, local overlays |
| [Publishing](Publishing.md) | Draft vs immediate, auto-publish, note fallback, comment templates |
| [Security Model](Security-Model.md) | The review sandbox, token handling, and the GUI's session security |
| [Recipes](Recipes.md) | Worked configurations: multi-instance, Bedrock, monorepos, team agents, MR hygiene |
| [Troubleshooting](Troubleshooting.md) | Common errors and how to read the logs |

## Reference

| Page | What it covers |
|---|---|
| [Configuration Reference](Configuration-Reference.md) | Every key, environment variable, flag, default, and validation rule |
| [CLI Reference](CLI-Reference.md) | All commands: the TUI, `gui`, `config`, `cache`, `version` |

## Design documentation

Architecture diagrams, the TUI/GUI feature matrix, and the ADRs behind the
big decisions live in the repository itself:
[docs/architecture.md](https://github.com/RobertYoung/gitlab-reviewer-cli/blob/main/docs/architecture.md),
[docs/features.md](https://github.com/RobertYoung/gitlab-reviewer-cli/blob/main/docs/features.md),
[docs/adr/](https://github.com/RobertYoung/gitlab-reviewer-cli/tree/main/docs/adr).
