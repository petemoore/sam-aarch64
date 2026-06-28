# Plan: i48c-b8d — integrated on-Z80 text→`.tbn` chain, corpus byte-match

Goal (the m9 i48c done-criterion): one Z80 run per fixture takes source text through
`parse_run → pass1_ir_walk → compact_ir_walk(+real encoder via cemit) → compact_serialize`
with **no host glue between stages**, and the produced compact v2 `.tbn` byte-matches host
`assemble.CompactTBNBytes` (tools/sam-aarch64/assemble/api.go:38) over the screened corpus.
Prereq: i48c-b8g (parser corpus byte-match) is merged — its buffer conditionals and its
measured token/IR sizes feed this layout.

## Why flat is impossible (measured, 2026-07-03 investigation)

- Code: lexer 1732 B + parser 6713 B + pass1 walk+leaves 4665 B + compact walk 1183 B +
  serializer 947 B + cemit 781 B ≈ 16.0 KB, **plus** the real encoder
  (`insn_encode.asm`/`insn_overlay.asm`/`form_lookup.asm`, not in that total) ≈ 20-22 KB.
- Fixed regions that cannot move: pass-1 leaf tables `&C100-&EFFF` (~11.9 KB equ, shared
  with the production assembler) + compact-core state `&F000-&FFFF` (4 KB).
- Buffers: LEX_SRC + LEX_TOKS + LEX_STRPOOL + SYM_NAMES + PARSE_RECS(=IR, ≤10752 B for the
  screened corpus) + cemit elems + `.tbn` out ≈ 35-45 KB depending on sizing.
- Sum ≫ 64 KB. Additionally the ENCTAB bracket (`enctab_map_in`, trampoline.asm:697) maps
  LMPR=&24 → sections A **and B** (`&0000-&7FFF`) both repage during every encode, so
  nothing the encode path reads may live below `&8000` at encode time; and INST payload
  bytes can only come from `cemit_add_inst → compact_inst → encode_inst` (ENCTAB-coupled)
  — the flat b8b skeleton path cannot byte-match (compact_emit.asm:34-43).

## Design: real SAM paging inside the koron harness

The koron harness is NOT flat: it runs the full sampage pager (harness.go:89-96, 269-272,
313-318 — OUT `&FA`/`&FB` are faithful) and exposes physical page seeding
(`Pager().RAM[n]`, harness.go:469-472). So the integrated chain uses **real paging**, the
same mechanics as the production assembler and the q36 unified-pool direction
(docs/specs/ide-memory-model-design.md): the parser stage's code+tables live in their own
page pair, exactly like a pool-allocated scratch page.

Two built images, one run:

1. **Main image** (org `&8000`, sections C/D via HMPR, loaded normally): driver +
   pass1 walk + leaves + compact walk + cemit + encoder + serializer. Code must fit
   `&8000-&C0FF` (≈13-15 KB estimate — escape valve: move the serializer into page 9,
   it runs post-encode). Pass-1 tables `&C100-&EFFF` and compact state `&F000-&FFFF`
   unchanged.
2. **Parser image** (seeded into physical pages 8+9; window = sections A+B while
   `LMPR=&28` = RAM0 bit + page 8): page 8 (`&0000-&3FFF` window) holds PARSE_RECS
   (=IR, sized from b8g data, ≤10752), LEX_SRC, SYM_NAMES; page 9 (`&4000-&7FFF` window)
   holds lexer+parser code (~8.4 KB) + LEX_TOKS + LEX_STRPOOL (sizes from b8g's measured
   token counts). Cross-image symbols resolve via `--importfile` (the
   test_cluster/test_mem precedent). ENCTAB seeds page 4 from `build/enctab.enc`
   (3970 B, `make enctab`) as today.

Run phases (the driver, in the main image):

- **Phase 0**: relocate SP to `&C0FE` (the assembler's own stack region) — the harness
  plants SP=&6FFE/haltTrap=&7000 in the boot-mapped pages, which vanish under LMPR
  switches; mirror the trampoline SP-switch pattern (trampoline.asm:33-90). Restore LMPR
  + SP before the final RET.
- **Phase 1 (parse)**: LMPR=&28; Go test wrote source into LEX_SRC (page 8) beforehand;
  `call parse_run` (page-9 address via importfile); IR length = `PARSE_RECPTR − PARSE_RECS`.
- **Phase 2 (pass1 + compact + encode)**: keep LMPR=&28 so the IR is readable at its
  section-A window address (`PASS1_IR_BUF: equ` that address; it is symbol-addressed only,
  test_pass1_ir.asm:137-139, so an equ alias works — but note it is `defs` today: b8d adds
  the same conditional-equ mechanism b8g introduced). The compact walk's INST arm calls the
  real cemit adapter instead of the skeleton (new define, e.g. `COMPACT_WALK_REAL_ENCODER`).
  **Per-INST ENCTAB bracket with staging copy**: copy the current INST record bytes into an
  `&8000+` staging buffer (STAGING_BUF `&D500` is idle at that moment — verify — else a new
  small buffer), then `enctab_map_in` → encode → `enctab_map_out` (restores saved LMPR=&28),
  continue walking the IR. Rationale: the bracket hides sections A+B, so the walk cannot
  read the IR while ENCTAB is mapped.
- **Phase 3 (serialize)**: names wire = `[count:2 LE]` + SYM_NAMES verbatim up to the
  0-length sentinel (SYM_NAMES is `[len:1][bytes]` in first-encounter order == host ID
  order, api.go:43-46; no SYM_COUNT cell exists — derive count by the sentinel walk).
  Small new routine builds this (or emits count while copying). `.tbn` out window: reuse
  LEX_TOKS space (page 9, dead after parse) via `ser_out_base` — the serializer's existing
  out-window guard fails loud if a `.tbn` outgrows it. b8b's header-row buffers at
  `&5000/&5400` (low RAM) must relocate — they'd sit inside the page-9 window; move them
  into page-8 spare or compact-state spare.
- **Phase 4**: restore LMPR + SP, RET; Go reads the `.tbn` (`ser_out_len`) and
  byte-matches host `CompactTBNBytes` with first-divergence reporting.

## Corpus screening

Same reviewed exclude-list pattern as the siblings (union of parseKnownOversize +
compactKnownOversize + serKnownOversize; the 10 compact-oversize fixtures + in_long_source).
Host-side computed capacity guards for every buffer (loud t.Fatal on unlisted overflow,
stale-entry errors) — the lexer/parser layer has NO Z80-side bounds checks (silent
corruption), so the guards are the safety net. `inst_out_over32k.s` never materialises as
an OUT problem here (we stop at `.tbn`), but stays excluded if its IR/sidecar exceeds caps.

## Bricks (likely one PR each)

1. **Parser page image**: build target for the two-image split (parser org'd for the
   page-8/9 window, buffer placement conditionals extending b8g's), harness seeding +
   importfile plumbing, and a smoke test: parse one fixture via the paged arrangement,
   byte-match IR vs serializeIR — proves phase 0/1 and the window layout.
2. **Chain phases 2-3**: PASS1_IR_BUF equ-alias + real-encoder walk arm (per-INST bracket
   + staging copy) + names transform + row/out relocation + `b8d_run` driver; corpus test
   text→`.tbn` byte-match. This is the item's done-criterion.
3. (= i48c-b8h, separate item) boot/SimCoupé self-test assessment.

## Verification per brick

`make` the new targets + `go test -count=1 ./z80/ -run <new tests>` + full package
(expect only the known private-artifact failure in fresh worktrees:
TestFlashChunk1WriteFaultReportsFail needs the gitignored bootloader_chunk1_data.asm).
CI (the full SimCoupé matrix) as the final gate per PR. Delete this plan in the PR that
completes brick 2 (registry: set i48c-b8d DONE there).

## Open measurements this plan waits on (from b8g)

- Max token count + strpool bytes over the screened corpus → LEX_TOKS/LEX_STRPOOL sizing
  in page 9 (budget: code ~8.4 KB + TOKS + STRPOOL ≤ 16 KB).
- Max IR bytes over the *screened* (not just pass1-screened) corpus → PARSE_RECS sizing in
  page 8 (budget: RECS + LEX_SRC + SYM_NAMES ≤ 16 KB).
- Max `.tbn` size over the screened corpus → out-window fit inside the freed LEX_TOKS span.
