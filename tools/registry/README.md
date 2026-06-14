# tools/registry

CLI for validating and generating the `iN`/`qN` work-item registries from YAML.

Subcommands: `validate`, `gen`, `add`, `split`, `set-status`, `set-pr`, `dep`, `answer`, `next-id`.

## `--migrating` flag

Defers invariant 10 (id-shaped ref existence) so refs may point at ids not yet
in YAML. Invariants 11/12/13 (depends_on DAG, WONTFIX-target, delete-gate) remain strict.

## Environment variables

| Variable | Default | Purpose |
|---|---|---|
| `REGISTRY_ITEMS` | `<toolDir>/testdata/items.yaml` | items YAML path |
| `REGISTRY_QUESTIONS` | `<toolDir>/testdata/questions.yaml` | questions YAML path |
| `REGISTRY_DIR` | `<toolDir>` | directory for `.id-ledger.txt` |
| `REGISTRY_TEMPLATES` | `<toolDir>/templates` | `*.head.md` template files |
| `REGISTRY_OUTDIR` | _(empty = stdout)_ | write generated `.md` files here |

## Makefile targets

`make registry-gen` — build binary.
`make registry` — regen `docs/notes/*` in place (needs `registry/` from i115d migration).
`make registry-sync-check` — diff views against YAML (not yet wired to CI).

## Canonical design doc

`docs/specs/registry-source-of-truth-design.md`
