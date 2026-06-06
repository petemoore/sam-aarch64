# Question registry — the project-wide open-question index

Canonical, milestone-neutral index of **open questions for Pete** (decisions the
agents cannot settle themselves). Parallel to the item registry
(`docs/notes/item-registry.md`): the item registry tracks *work*, this tracks
*unresolved decisions*. Created 2026-06-08 (Pete asked for a dedicated home;
previously open questions lived only inline in the ROADMAP "Open for Pete" notes
and the per-milestone status docs' "Open questions" sections).

**Convention:** each question has a stable id `Q-<context>-<n>` (e.g. `Q-i48-1`).
Once resolved, mark it ✅ with the resolution + where the decision is recorded
(spec/registry/commit), and leave the row — do not delete (so the rationale
survives). Per-milestone status docs may still surface the active subset; this is
the never-archived home.

| id | question | status | pointer |
|----|----------|--------|---------|
| **Q-i48-1** | Host packaging once the symbolic `.tbn` is gone: two CLIs (`text2bin`+`refenc`) over a shared front-end library (recommended — keeps `text2bin` as a command; minor host pass-1 duplication) **vs** one merged tool (no duplication; retires `text2bin`). | ⏳ open | `docs/specs/2026-06-08-i48-single-format-syntactic-encoder-design.md` §3, §8 |
| **Q-i48-2** | Editor in-memory model for **value-dependent base words with unresolved symbols** (e.g. `mov Rd, fwd_label` before the label is defined): keep the element tentative/symbolic until assemble-time, or default-then-validate? Interacts with **i41** (edit-model). | ⏳ open — resolve at editor phase | i48 spec §8; item registry i41 |
| **Q-i48-3** | Strictness scope: we forego GNU's silent `ldr→ldur` and `add` `lsl #12` auto-rewrite (→ syntactic/error). Are there other "generous" GNU rewrites in the corpus to treat the same way? | ⏳ open — sweep when implementing i48b | i48 spec §4, §8 |
| **Q-llist-5** | LLIST/`llist-normalise` disposition (the one remaining accidentally-committed Go binary under i34). | ⏳ open (pre-existing; migrated here 2026-06-08) | item registry i34 |
