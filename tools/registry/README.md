# tools/registry

CLI tool for validating and generating the `iN`/`qN` work-item registries from
structured YAML source.

## What it does

- `registry validate <items.yaml> [questions.yaml]` — enforce all 10 schema
  invariants (id uniqueness, sort order, status payloads, one-PR-per-leaf,
  bounded fields, ref integrity); exit 1 on any violation.
- `registry gen <items.yaml> <questions.yaml>` — render the four split
  open/closed markdown views to stdout (Phase 1: to a buffer; Phase 4: in
  place to `docs/notes/`).

Subcommands for mutation (`add`, `split`, `set-status`, `set-pr`, `next-id`)
land in Phase 3.

## How to use

```
make registry-gen           # build the binary to build/registry
build/registry validate tools/registry/testdata/items.yaml
build/registry validate tools/registry/testdata/items.yaml tools/registry/testdata/questions.yaml
build/registry gen     tools/registry/testdata/items.yaml tools/registry/testdata/questions.yaml
```

## Canonical design doc

`docs/specs/registry-source-of-truth-design.md`
