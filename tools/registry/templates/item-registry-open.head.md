# Item registry — open

Work items with status `OPEN` or `IN_PROGRESS`. Items with status `DONE` or
`WONTFIX` live in `item-registry-closed.md`.

## Id conventions

`i<N>` · `i<N><letter>` · `i<N><letter>-b<M>` · `i<N><letter>-b<M><letter>`
(base → sub-item → brick → brick-part). Ids are immutable once used in a PR,
branch, or commit; never reused. Sort order is true-numeric (i5 < i41 < i237).

## Controlled status vocabulary (open file)

| token | meaning |
|---|---|
| `OPEN` | not yet started; or started but no agent has it in-hand |
| `IN_PROGRESS` | actively being worked right now |

"Blocked" is not a status token — it is a *derived* property of the dependency
graph. An item with an unsatisfied `depends_on` edge is still `OPEN`; the edge
to the gating item or question appears in the refs/links column (`gated-on:iN`).

## Atomic-item rule

One row = one deliverable = one completing PR. If a row bundles independently
completable parts, split it into letter sub-ids (`i81a`/`i81b`…) so no finished
part hides inside an unfinished row.

## Dependency model

`depends_on` edges declare what must finish before this item can proceed. A target
may be an item (must reach `DONE`) or a question (must be answered and deleted).
Edges form a DAG; the validator rejects cycles and dangling targets.

<!-- The table below is generated — do not edit it by hand. -->

