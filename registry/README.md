# registry/

Structured source for the `iN` item registry and `qN` question registry.

## What lives here

- **`items.yaml`** and **`questions.yaml`** — the canonical source of truth for
  work items and open questions (created in Phase 4, `i115d`).
- **`templates/`** — hand-authored header prose for the four generated registry
  views.  Each `*.head.md` file contains the title and governance prose that
  appears above the generated table; the generator prepends the banner then
  concatenates the template.
- **`.id-ledger.txt`** — append-only list of every id ever minted (created in
  Phase 3, `i115c`); prevents id reuse after deletion.

## How it relates

The `tools/registry` CLI reads the YAML source and writes the four generated
markdown views under `docs/notes/`:

```
registry/items.yaml      → docs/notes/item-registry-open.md
                           docs/notes/item-registry-closed.md
registry/questions.yaml  → docs/notes/question-registry-open.md
                           docs/notes/question-registry-closed.md
```

Until Phase 4 (`i115d`) the YAML source files don't exist; the live registry
is still hand-edited markdown.  See the design doc for the full rollout plan.

## Deep doc

`docs/specs/registry-source-of-truth-design.md`
