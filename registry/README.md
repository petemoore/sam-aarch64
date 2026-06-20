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
for `registry/items.yaml`. Run from elsewhere it warns loudly on stderr and
falls back to the bundled `tools/registry/testdata` fixtures (never silently) —
set `REGISTRY_ITEMS` to override.

Edit work-tracking via the `tools/registry` CLI (`add` / `split` / `set-status`
/ `set-pr` / `dep` / `answer` / `prioritize` / `move`), then `make registry` to
regenerate the views; commit the YAML and regenerated `.md` together. Full
design: `docs/specs/registry-source-of-truth-design.md`.
