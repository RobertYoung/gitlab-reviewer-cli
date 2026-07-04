# CLI reference

```
gitlab-reviewer [flags]              # launch the TUI
gitlab-reviewer gui [flags]          # launch the browser GUI
gitlab-reviewer review <project!iid | MR URL>  # run one review non-interactively (CI/scripting)
gitlab-reviewer config show          # print effective configuration (secrets redacted)
gitlab-reviewer config validate      # check the configuration
gitlab-reviewer cache ls             # list cached clones
gitlab-reviewer cache clean [--all]  # remove worktrees, evict over-budget clones
gitlab-reviewer models               # list models to use with --model / review.model
gitlab-reviewer version              # print version, commit, build date
```

Every configuration setting is also a **persistent flag** available on the
root command and all subcommands — the full flag ↔ key ↔ env mapping is in
the [Configuration Reference](Configuration-Reference.md). This page
covers the commands themselves and their command-specific flags.

## `gitlab-reviewer` (root)

Launches the TUI. With no projects or groups configured it opens a
group/project picker; otherwise it goes straight to the MR list.

```sh
gitlab-reviewer --project mygroup/myapp --project mygroup/other
gitlab-reviewer --group platform-team
gitlab-reviewer --instance work          # pick a named GitLab instance
gitlab-reviewer --config ./config.yaml   # settings file elsewhere
```

`--config` selects a non-default settings file (default
`~/.config/gitlab-reviewer/config.yaml`).

## `gitlab-reviewer gui`

Serves the browser GUI on `127.0.0.1` and prints a tokenised launch URL.

| Flag | Default | Meaning |
|---|---|---|
| `--port` | random free port | port to listen on |
| `--no-browser` | `false` | don't open the browser, just print the URL |

See the [GUI Guide](GUI-Guide.md).

## `gitlab-reviewer review`

Runs one review non-interactively and prints the outcome to stdout —
progress streams to stderr. Nothing is posted to GitLab unless `--publish`
says so. The MR is named as `project!iid` or by its web URL; a URL's host
also selects the matching `gitlab.instances` entry.

```sh
gitlab-reviewer review mygroup/myapp!123 --publish draft --agents bug,security
gitlab-reviewer review https://gitlab.example.com/mygroup/myapp/-/merge_requests/123
```

| Flag | Default | Meaning |
|---|---|---|
| `--publish` | `none` | `none` (store/report only), `draft` (one review, published in one action), `immediate` |
| `--output` | `text` | `text` or `json` on stdout |

See [Headless Mode](Headless-Mode.md) for publish semantics, exit codes,
and a GitLab CI example.

## `gitlab-reviewer config`

- **`config show`** — prints the effective configuration after merging
  flags > environment > file > defaults. Secrets (tokens, MCP headers) are
  redacted, including in per-project sections.
- **`config validate`** — checks completeness and consistency: required
  values (a GitLab credential, region for Bedrock), enums, URL and
  duration formats, instance names, MCP server definitions, and that
  `publish.template` parses.

Both are useful before launching the TUI, and in dotfiles/CI checks.

## `gitlab-reviewer cache`

Manages the clone cache used by the default `clone` checkout mode (under
`checkout.cache_dir`, default `~/.cache/gitlab-reviewer`).

- **`cache ls`** — lists cached clones with sizes.
- **`cache clean`** — removes review worktrees and evicts
  least-recently-used clones until the cache fits `checkout.cache_max_mb`
  (default 2048 MiB).
- **`cache clean --all`** — removes every cached clone, emptying the cache.

Eviction also runs automatically in the background at startup. See
[Checkout Modes](Checkout-Modes.md).

## `gitlab-reviewer models`

Lists the models offered for the review: `review.models` from the
settings file when set, otherwise a curated list of common Claude models
for the selected provider. The configured `review.model` is marked with
`*`. The same list backs shell completion of `--model`. It is suggestions,
not validation — `--model` accepts any model ID the claude CLI
understands. See the
[Configuration Reference](Configuration-Reference.md#review).

## `gitlab-reviewer version`

Prints `gitlab-reviewer <version> (commit <hash>, built <date>)`.
