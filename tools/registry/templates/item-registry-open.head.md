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
graph. An item with an unsatisfied `depends_on` edge is still `OPEN`; its gating
item/question ids are listed in the `deps` column (the edges of the dependency DAG).

## Atomic-item rule

One row = one deliverable. If a row bundles independently completable parts, split
it into letter sub-ids (`i81a`/`i81b`…) so no finished part hides inside an
unfinished row.

## Dependency model

`depends_on` edges declare what must finish before this item can proceed, and are
shown in the `deps` column. A target may be an item (must reach `DONE`) or a
question (must be answered and deleted). Edges form a DAG; the validator rejects
cycles and dangling targets. The DAG is built from the `depends_on` fields in
`registry/items.yaml`; `grep depends_on` there for the machine-readable edge list.
The `dependents` column shows the reverse edges (items that depend on this one).

## Column layout

The generated table has 7 columns: `id | item | status | PR | deps | dependents |
refs/links`. `PR`, `deps`, `dependents`, and `refs/links` are each independent
first-class columns. The generated `.md` files are views of `registry/items.yaml`
— do not edit them by hand (hand edits fail CI).

<!-- The table below is generated — do not edit it by hand. -->

