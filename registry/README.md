# registry — structured source of truth for the work-item registries

The `iN` item and `qN` question registries are generated from validated YAML
here. The markdown views under `docs/notes/` (`item-registry-open.md`,
`item-registry-closed.md`, `question-registry-open.md`, `backlog.md`) are
**output** — never hand-edit them (the `registry-sync` CI job fails on drift).

- `items.yaml` / `questions.yaml` — the canonical item/question records.
- `priority.yaml` — the ordered backlog (open pullable items, highest first);
  `registry ready` returns the unblocked tip. Every mutation auto-repairs this
  order so each item always sorts after its dependencies (topological repair) —
  you never hand-fix the ordering after `dep add`.
- `.id-ledger.txt` — append-only high-water list of every id ever minted
  (ids are never reused, even after deletion).

Run the CLI **from the repo root**: it locates this live registry by walking up
for `registry/items.yaml`. It **never** falls back to the bundled
`tools/registry/testdata` fixtures — run somewhere it can't find the live
registry (with no `REGISTRY_ITEMS` set) and it errors (exit 1) rather than risk
an accidental testdata read/write. Set `REGISTRY_ITEMS` to point elsewhere.

Inspect a single record with `registry view --id iN|qN` (add `--format json` for
scripting) — it prints the record plus its computed dependents (reverse edges)
and priority rank, which the raw YAML does not carry.

Mutating commands are **concurrency-safe**: each takes an exclusive advisory lock
(`registry/.lock`, flock) around its load→mutate→write and writes files
atomically (temp + rename), so two simultaneous invocations serialize instead of
clobbering each other and a reader never sees a half-written file. A mutator
waits up to ~10 s for the lock, then errors rather than hanging. Read-only
queries (`ready`/`view`/`dependents`/`dag`) do not lock. (Cross-checkout parallel
sessions additionally need git merge handling — out of scope here.)

Edit work-tracking via the `tools/registry` CLI (`add` / `split` / `set-status`
/ `set-pr` / `dep` / `answer` / `prioritize` / `move`), then `make registry` to
regenerate the views; commit the YAML and regenerated `.md` together. Full
design: `docs/specs/registry-source-of-truth-design.md`.
