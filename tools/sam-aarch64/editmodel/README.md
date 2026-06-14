# editmodel

The paged block-list document model for the on-SAM editor (item i41). This is
the host-side correctness prototype of the algorithm the SAM editor uses to
hold source lines in RAM.

**Design authority:** [`docs/specs/editor-edit-model-design.md`](../../../docs/specs/editor-edit-model-design.md)

Lines are grouped into blocks sized to fit within one ½-page (8 KB) of SAM RAM.
Blocks split on overflow and merge on underflow. Every line carries a stable,
monotonically-allocated `RecordID` (u24). A `loc` map (record-id → block pointer)
provides fast goto-by-id (a scan of the small resident block list, design §2.5);
only the O(½ block) records that actually move between blocks are re-pointed on a
split or merge (design §3.4). The SAM byte layout
(in-block gap buffer, offset tables, physical paging) is the Z80 port's concern
(i41d).

## Seams and sub-items

- **Serialize/load seam** — `serialize.go` implements a simple self-describing
  v1 format; sub-item i41c swaps this for the v2 `.tbn` encoding via the i48
  encoder without touching the block-list algorithm.
- **Undo journal** — deferred to i41b.
- **Z80 port** — i41d.

## Testing

```
go test ./editmodel/...
```
