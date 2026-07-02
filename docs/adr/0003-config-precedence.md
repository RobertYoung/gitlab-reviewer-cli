# ADR-0003: koanf with explicit env/flag mapping

## Status

Accepted

## Context

Every setting must be reachable via flag, environment variable, and settings
file with the precedence flags > env > file > defaults, plus per-project
overrides. Candidates: viper, koanf, hand-rolled.

## Decision

Use koanf v2 with cobra/pflag. Layers are loaded in order — defaults
(structs provider), YAML file, environment, changed flags — so later layers
win, giving exactly the required precedence.

Environment variables and flags are mapped to config keys through **explicit
tables** rather than algorithmic name transformation: keys like
`gitlab.base_url` contain underscores, so an automatic underscore→dot
mapping would be ambiguous. The tables also give one obvious place to read
every setting's three names.

Per-project overrides (`projects.<path>.*`) are applied by merging that
subtree of the koanf tree over the `review`/`checkout`/`publish` sections
and re-unmarshalling.

Why not viper: it force-lowercases keys, drags in a much larger dependency
tree, and its precedence rules are implicit. Why not hand-rolled: koanf's
file parsing, merging, and struct unmarshalling (with duration hooks) are
exactly the boring parts worth outsourcing.

## Consequences

- Adding a setting means touching the struct, the defaults, and two mapping
  tables — verbose but grep-able, and the config precedence matrix test
  catches mistakes.
- The GitLab token is redacted from logs, errors, and `config show`
  (internal/secret); keychain storage is a future enhancement.
