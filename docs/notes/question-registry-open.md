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
| **q18** | **Brick 7 of the `http_main` port — build the hardware B-DOS HSAVE-per-record store leaf speculatively now, or leave it as the real-Trinity persist (your hardware test)?** (autonomous-loop, 2026-06-16.) Bricks 1–6 — the **full host-verifiable slice** of the Z80 `http_main` multi-file fetch loop — are complete + merged (PRs #308–#314: compose → dynamic path → bodySink seam → `prov_start` → the per-file store double → the `prov_first`/`prov_onframe`/`prov_next` loop, every frame host-verified byte-for-byte vs the Go `Provisioner`). Brick 7 (the FINAL brick, `docs/plans/z80-http-main-port-plan.md`) migrates the **real bootable** `http_main` onto that streaming loop and adds the **real B-DOS HSAVE-per-record store leaf** (un-guarding streaming + verify + sha256 into the bootable; `store_begin`/`store_end`/`storage_sink_leaf` = `fw_span` record split + `bdos_seam` HSAVE). Two reasons it is not autonomous: (1) the **record cap** (the RAM budget for one HSAVE staging buffer) is, per the Go authority `bdos/span.go`, "a hardware detail **pinned when the real persist is built**" — choosing it speculatively bakes the very constant span.go refused to bake; (2) the HSAVE leaf is **non-host-verifiable** (the q16 hardware gate — no ROM/SAMDOS/RST 8 in the harness), so Brick 7 would merge unverified hardware code (host tests only re-cover the already-tested loop), and the i100 row already frames the storage sink as "validated only on real Trinity — **Pete's test**." Also footprint-sensitive (sha256 + the streaming buffers + a cap-sized staging buffer must fit under `&10000`; the plan says raise a `qN` if it overruns). **Question:** pin the record cap (your Trinity RAM budget) + say whether an agent should build Brick 7 speculatively (assembles + fits + loop-tests-green, HSAVE leaf unverified until your hardware test) or leave it as your hardware-persist work. The legacy single-file bootable `http_main` stays meanwhile (unchanged). | OPEN — Bricks 1–6 shipped (host slice complete); Brick 7 = hardware/Pete decision | `docs/plans/z80-http-main-port-plan.md` (Brick 7); `tools/netboot-oracle/bdos/span.go`; `src/netboot/fw_span.asm`; `src/netboot/bdos_seam.asm`; item registry i100; q16 (spanning approach, resolved) |
