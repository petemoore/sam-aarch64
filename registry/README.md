# registry — structured source of truth for the work-item registries

The `iN` item and `qN` question registries are generated from validated YAML
here. The markdown views under `docs/notes/` (`item-registry-open.md`,
`item-registry-closed.md`, `question-registry-open.md`) are **output** — never
hand-edit them.

- `items.yaml` / `questions.yaml` — the canonical source records.
- `.id-ledger.txt` — append-only high-water list of every id ever minted
  (ids are never reused, even after deletion).
- `_migration/` — **ephemeral** scratch worklist for the in-progress
  markdown→YAML migration (item i115d): full copies of the old registries with
  rows deleted as they migrate. Deleted at the cutover commit.

Edit work-tracking via the `tools/registry` CLI (`add` / `split` / `set-status`
/ `dep` / `answer`), then `make registry` to regenerate the views; commit the
YAML and regenerated `.md` together. Full design:
`docs/specs/registry-source-of-truth-design.md`.
