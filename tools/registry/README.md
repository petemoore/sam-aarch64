# tools/registry

CLI for validating and generating the `iN`/`qN` work-item registries from YAML.

Subcommands: `validate`, `gen`, `ready`, `in-progress`, `view`, `dependents`, `dag`, `prioritize`, `move`, `add`, `split`, `set-status`, `set-pr`, `dep`, `answer`.

Item/question ids are **always tool-determined**: `add` auto-allocates the next free top-level `iN` (or `qN` with `--space questions`); `split` determines a child id from its parent (`iN`→`iNa`, `iNx`→`iNx-b1`, capped at 26 letters per level — the unbounded `-bN` brick level is the escape hatch). There is no `next-id` and no caller-supplied `--id` (closes the next-id→add race). `ready` lists not-yet-started unblocked work in priority order; `in-progress` lists the live IN_PROGRESS threads.

## `--migrating` flag

Defers invariant 10 (id-shaped ref existence) so refs may point at ids not yet
in YAML. Invariants 11/12/13 (depends_on DAG, WONTFIX-target, delete-gate) remain strict.

## Environment variables

The data source is resolved explicitly: an explicit `REGISTRY_*` env var wins; otherwise the **live repo `registry/`** discovered by walking up from the cwd for a `registry/items.yaml`. It is **never** the bundled test fixtures — with no resolvable source the CLI errors out rather than risk a stale/testdata read (the fuller "always-explicit" design is tracked as i150).

| Variable | Default | Purpose |
|---|---|---|
| `REGISTRY_ITEMS` | `<live registry/>/items.yaml` | items YAML path |
| `REGISTRY_QUESTIONS` | sibling of items.yaml | questions YAML path |
| `REGISTRY_PRIORITY` | sibling of items.yaml | priority queue path |
| `REGISTRY_DIR` | the live `registry/` dir | directory for `.id-ledger.txt` |
| `REGISTRY_TEMPLATES` | `<repo>/tools/registry/templates` | `*.head.md` template files |
| `REGISTRY_OUTDIR` | _(empty = stdout for `gen`; mutations regen silently)_ | write generated `.md` files here |

## Makefile targets

`make registry-gen` — build binary.
`make registry` — regen `docs/notes/*` in place (needs `registry/` from i115d migration).
`make registry-sync-check` — diff views against YAML (the `registry-sync` CI job runs this; fails on drift).

## Canonical design doc

`docs/specs/registry-source-of-truth-design.md`
