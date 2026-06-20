# Item registry — closed

Work items with status `DONE` or `WONTFIX`. Open items live in
`item-registry-open.md`.

## Controlled status vocabulary (closed file)

| token | meaning |
|---|---|
| `DONE` | completed; delivering PRs are in the `PR` column |
| `WONTFIX` | decided not to implement; reason is in the description |

A `DONE` leaf may have zero or more PRs — operational work (e.g. config changes)
needs no PR; items delivered across several PRs may list them all. Umbrellas never
appear as `DONE` here directly — they are `DONE` only when all leaf children are
`DONE`/`WONTFIX`, and the generator emits them here once that condition holds.

Git history is the archive for per-PR detail, rationale, and iteration history.
This table holds the stable structured fact — what was decided and where to find it.

The `PR`, `deps`, `dependents`, and `refs/links` columns are each independent
first-class columns. The generated `.md` files are the source-of-truth views of
`registry/items.yaml` — do not edit them by hand (hand edits fail CI).

<!-- The table below is generated — do not edit it by hand. -->

