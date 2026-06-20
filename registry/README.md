# registry — structured source of truth for the work-item registries

The `iN` item and `qN` question registries are generated from validated YAML
here. The markdown views under `docs/notes/` (`item-registry-open.md`,
`item-registry-closed.md`, `question-registry-open.md`, `backlog.md`) are
**output** — never hand-edit them (the `registry-sync` CI job fails on drift).

- `items.yaml` / `questions.yaml` — the canonical item/question records.
- `priority.yaml` — the ordered backlog (open pullable items, highest first);
  `registry ready` returns the unblocked tip.
- `.id-ledger.txt` — append-only high-water list of every id ever minted
  (ids are never reused, even after deletion).

Edit work-tracking via the `tools/registry` CLI (`add` / `split` / `set-status`
/ `set-pr` / `dep` / `answer` / `prioritize` / `move`), then `make registry` to
regenerate the views; commit the YAML and regenerated `.md` together. Full
design: `docs/specs/registry-source-of-truth-design.md`.
