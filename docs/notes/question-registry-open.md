# Question index — open questions (`qN` registry, open half)

This is the **open half** of the canonical, milestone-neutral `qN` registry. It
holds every **unresolved decision for Pete** (things an agent cannot settle itself,
or chose to defer rather than guess). The sibling item registry is
`docs/notes/item-registry-open.md`.

**Governance is identical to the item registry — see `docs/notes/item-registry-open.md`
for the shared registry discipline** (stable ids locked once assigned, never
renumbered, ids globally unique across open + closed halves, marked-resolved by
moving the row to `question-registry-closed.md` rather than deleting it). In one
line for questions: `qN` ids; this table is the **single sure-fire list of what's
still open**; add the moment a question arises (not just in chat — chat questions
get lost in simultaneous-edit races); when answered, move the row to
`question-registry-closed.md` **in the same PR that resolves it**; land on `main`
via PR so Pete sees it where he reads.

## Controlled status vocabulary

Every status cell starts with exactly one token:
- `OPEN` — awaiting Pete's answer, no additional blocker
- `BLOCKED:<what>` — additionally gated on the named prerequisite or person

| id | question | status | pointer |
|----|----------|--------|---------|
| **q13** | **Editor default rendering config** (i76 / `editor-rendering-rules-design.md` §10) — which look does the on-SAM editor ship as its *default* (user-overridable)? The rendering *rules* are specified (§3–§9, the config lab is the authority) and the corpus measurements justify *some* compression (64 cols fits only 76% of comments uncompressed), but choosing the default *style* — baseline-clean (`binutils-baseline`) vs compressed-dense (a tuned `compressed`) vs chromeless (`comet-minimal`) — is a lived-use taste call against the real PNGs / SimCoupé SCREEN$. The agent **recommends** a tuned `compressed.config` (adaptive mnemonic column, SAM-native `&`-immediates, stripped registers + marked *immediates* not registers per the iteration-2 insight, label-truncate 16, sameline comments at the commented-lines-only column, `wrap:3` cursor-line expansion, max-width 40). Not a blocker — any answer is a one-line resolution + a default config file, and the renderer port is independent of it. | OPEN — agent recommendation on the table; Pete's sign-off — A/B the four starter configs | `docs/specs/editor-rendering-rules-design.md` §10; `tools/editor-prototype/configs/`; item registry i76; i6 (geometry/font, separately gated on the P3 memo) |
