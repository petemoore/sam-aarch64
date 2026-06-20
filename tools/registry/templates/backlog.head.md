# Backlog — priority queue

The curated order of open pullable work items (leaves / childless items;
umbrellas are spanned by their leaves). Highest priority first. The order is
stored in `registry/priority.yaml` and validated in CI as a strict permutation
of all open pullable items + a topological extension of the dependency DAG
(nothing ranked before an item it depends on).

**Agents pull from the top.** The `registry ready` command returns the unblocked
tip — items whose dependencies are all satisfied — in this priority order. Pull
from the first `ready` item to ensure the highest-priority unblocked work is
always picked up.

**Re-rank with:**

```
registry prioritize --id iN --to-top
registry move --id iN --before iM
registry move --id iN --after  iM
```

This view supersedes the prose "proposed sequence" in `docs/ROADMAP.md`.
Source of truth: `registry/priority.yaml`. Spec: `docs/specs/registry-source-of-truth-design.md §"Priority queue + ready"`.

<!-- The table below is generated — do not edit it by hand. -->

