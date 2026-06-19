# Item registry — closed

Work items with status `DONE` or `WONTFIX`. Open items live in
`item-registry-open.md`.

## Controlled status vocabulary (closed file)

| token | meaning |
|---|---|
| `DONE — PR #N` | completed; the single completing PR is recorded |
| `WONTFIX — <reason>` | decided not to implement; reason is in the description |

A `DONE` leaf has exactly one completing PR. Umbrellas never appear as `DONE`
here directly — they are `DONE` only when all leaf children are `DONE`/`WONTFIX`,
and the generator emits them here once that condition holds.

Git history is the archive for per-PR detail, rationale, and iteration history.
This table holds the stable structured fact — what was decided and where to find it.

<!-- The table below is generated — do not edit it by hand. -->

