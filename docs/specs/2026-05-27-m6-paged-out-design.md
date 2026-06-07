# M6 PR 1 — paged OUT buffer design

**Status**: design spec.  Pairs with `docs/specs/samdos-file-io.md` (the research note covering the HSAVE call site and the HLOAD trampoline pattern behind the ENCTAB paging). Date: 2026-05-27.

This is the design for the first M6 PR.  Scope: relocate the OUT buffer out of section C so that outputs > 2 KB are possible.  Paged source loading (IN > 2 KB) is a follow-on PR.

## Goal

Allow the SAM-side assembler to emit binary outputs of up to ~16 KB inside one physical page (`OUT_BASE_PAGE`) and, if needed, up to ~64 KB by spilling into the next contiguous physical page.  Real target: spectrum4 release.bin at ~21.7 KB across pages 5 + 6.

The HSAVE call site is already designed in `docs/specs/samdos-file-io.md`; this spec covers the **runtime emit path** — how `emit_byte` writes to a physical page that isn't the assembler's running page.

## Constraints

1. The assembler code (sections C + D content) MUST keep running normally throughout pass 2.  Anything that disturbs HMPR breaks the running code, the stack, or both.
2. The encoder needs ENCTAB live in section A throughout pass 2 (LMPR = `LMPR_ENCTAB`).  Any LMPR change to reach OUT must restore LMPR_ENCTAB before the encoder makes its next read.
3. Production code budget is 420 B headroom (`m3-asm-prod` = 11868 / 12288).  Net cost must stay well under that — target ≤ 100 B added.
4. OUT_LEN is 16-bit (cap 64 KB).  Sufficient for release.bin (~22 KB).  Debug builds (~274 KB) are out of scope for M6.

## Design

### Where OUT lives

- **Physical pages 5 (+ 6 if needed for outputs > 16 KB)**, contiguous to ENCTAB on page 4.  Page 5 is free per the Tech Manual page allocation table (`src/trampoline.asm:188-208`); contiguity to ENCTAB simplifies allocation reasoning but isn't strictly required.
- **Section A in the assembler's address space (`&0000-&3FFF`)** during emit.  LMPR's bit 5 (RAM0 bit, value `&20`) is set so the page is RAM-mapped rather than ROM.  `LMPR_OUT_BASE = &25` (RAM0 + low 5 bits = 5).
- **HSAVE will read OUT via section C (`&8000-&BFFF`)** at the end of pass 2 — see `docs/specs/samdos-file-io.md`.  UIFA byte 31 is set to `OUT_BASE_PAGE`; UIFA bytes 32-33 to `&8000`; HSAVE auto-increments HMPR across `&C000`.

The same physical page is reached through section A during emit (LMPR-controlled) and through section C during save (HMPR-controlled, by HSAVE itself).  These are independent mechanisms and don't conflict.

### Two-zone `emit_byte_paged`

Crucial observation from `docs/notes/sam-paging.md:88-91` (Tech Manual `tech-man_v3-0.txt:908-910`): **section B = LMPR+1 automatically**.  With `LMPR_ENCTAB = &24` (low 5 bits = 4) already set during pass 2, section B (`&4000-&7FFF`) maps to physical page **5** for free.  Reads/writes to `&4000..&7FFF` during the encoder window land in page 5 with zero LMPR overhead.

`src/trampoline.asm:110-115` notes this explicitly: *"LMPR = LMPR_ENCTAB means section A = page 4 (ENCTAB, RAM-replaces-ROM) and section B = page 5 (currently unused — we never write or read here while LMPR = LMPR_ENCTAB)."*

So the OUT buffer's **first 16 KB** is reachable via section B with the LMPR state we already have, and `emit_byte_paged` reduces to essentially the current `emit_byte` — just with `OUT_PC` in the section-B range instead of section C.

The **second 16 KB** (bytes 16384..32767 of OUT) lives in page 6, which is not reachable through any section of the current LMPR/HMPR configuration.  For bytes in this range, `emit_byte_paged` must bracket the write with an LMPR swap (LMPR=&25 → section A = page 5 = OUT first half, section B = page 6 = OUT second half — write to section B, restore LMPR=&24).

```dot
    OUT_PC in [&4000, &8000)              ; "low" zone — first 16 KB
       → write (HL), no LMPR change       ; section B with LMPR_ENCTAB
                                          ; cost: ~0 extra T-states over the
                                          ; existing emit_byte
    OUT_PC in [&4000, &8000)              ; "high" zone — second 16 KB
       AND a "high-zone" flag set         ; tracked in OUT_ZONE
       → save LMPR; LMPR := LMPR_OUT_HIGH ; LMPR = &25
       → write (HL)                       ; section B = page 6
       → restore LMPR_ENCTAB              ; LMPR = &24
                                          ; cost: ~60 T-states over emit_byte
```

The zone transition happens once per pass at the byte-16384 mark.  `OUT_PC` wraps from `&7FFF + 1` back to `&4000`, and a single `OUT_ZONE` byte (0 or 1) flips.  After the transition, every subsequent emit pays ~60 extra T-states.

For spectrum4 release.bin (~22 KB): 16384 bytes at ~0 overhead + 5376 bytes at ~60 T-states = ~320K T-states ≈ 90 ms.  For outputs ≤ 16 KB (every M3/M4/M5 fixture, many M6 fixtures): zero overhead.

The current `emit_bytes_n` (bulk emit; `encoder.asm:438`) is rewritten to call `emit_byte_paged` in a loop rather than open-coding the bulk write — simpler than building a separate bulk LMPR-bracketed path, and the call sites (mainly the `OpString` directive emitter) are not on the hot path.

### Why not uniform LMPR-A swap?

A simpler design swaps LMPR every emit, mapping `LMPR_OUT_CUR` (starting at &25, bumping on page cross) into section A and writing to `&0000-&3FFF`.  This works for any OUT page (no contiguity dependency between ENCTAB and OUT) and is uniform — no zone flag.  But it costs ~60 T-states per emit on every byte, including the common ≤ 16 KB case.  For a 22 KB output: ~1.3M T-states ≈ 380 ms.  Slower by 280 ms and gives up the zero-overhead common case.

We accept the dual-mode complexity for the perf benefit.  If page 5 ever becomes unavailable (e.g. a future M7 needs it for something else), the design can fall back to uniform-LMPR-A-swap without changing the call interface.

### `OUT_PC` and `OUT_ZONE` semantics

Old: `OUT_PC` is a section-C address in `&B800..&C000`.  Initialised to `OUT_BUF = &B800` in `reset_out_buffer` (`main_loop.asm:179`).

New:

- `OUT_PC` is a section-B address in `&4000..&8000`.  Initialised to `&4000` in `reset_out_buffer`.
- `OUT_ZONE` is a single byte: `0` = low (writes go to section B with LMPR_ENCTAB, page 5); `1` = high (writes bracket with LMPR=`&25`, page 6).  Initialised to `0`.

`OUT_BASE` (the start-of-output address) goes away — `save_out_file_paged` cares about `OUT_LEN` only (and the constant `OUT_BASE_PAGE`).

### Zone-crossing logic

`emit_byte_paged` flow:

```
1. push AF (preserve byte to emit)
2. fetch HL := (OUT_PC)
3. if OUT_ZONE == 0:
      pop AF
      ld (HL), A                  ; write to section B (page 5 via LMPR_ENCTAB)
   else:
      in  A,(251); ld (slot), A   ; save current LMPR (= LMPR_ENCTAB)
      ld  A, LMPR_OUT_HIGH        ; = &25
      out (251), A                ; LMPR = &25; section B = page 6
      pop AF                      ; byte to emit
      ld (HL), A                  ; write to section B (page 6 via LMPR=&25)
      ld  A, (slot)
      out (251), A                ; restore LMPR_ENCTAB
4. inc HL
5. if HL == &8000:                ; just wrote the last byte of the current zone
      OUT_ZONE := 1               ; cross over to high zone
      HL := &4000                 ; wrap back to section B base
6. (OUT_PC) := HL
7. inc (OUT_LEN) [16-bit]
```

The zone-crossing test is `if H == &80 → cross-zone`: cheaper than a 16-bit compare since `OUT_PC` only advances by 1 per emit.

`OUT_ZONE == 1` and a *second* `H == &80` (i.e. writing 32769th byte) is an error — the design supports up to 32 KB.  Add a `jp z, fail` guard at the second zone cross.  spectrum4 release.bin at ~22 KB is well inside this limit.

`OUT_LEN` is 16-bit, capping at 64 KB.  The 32 KB output limit comes from only allocating two contiguous physical pages (5 + 6); a 4+-page version would need a different design (e.g. uniform LMPR-A swap with `LMPR_OUT_CUR` incrementing through &25, &26, &27, ...).

### Save call site

Replace the current `save_out_file` (`main_loop.asm:2079`) with `save_out_file_paged` from the design note (`docs/specs/samdos-file-io.md` §"Pre-built code snippet"):

```asm
save_out_file_paged:
                ld      hl, name_OUT
                call    fill_uifa

                ld      a, OUT_BASE_PAGE
                ld      (UIFA + 31), a

                ld      hl, &8000               ; section-C offset
                ld      (UIFA + 32), hl

                ld      hl, (OUT_LEN)
                ld      a, h
                rlca
                rlca
                and     3
                ld      (UIFA + 34), a          ; pages = len >> 14

                ld      a, h
                and     &3f
                ld      h, a
                ld      (UIFA + 35), hl         ; remainder = len & 0x3FFF

                rst     8
                defb    HOOK_HSAVE
                ret
```

Crucially: the save call site is **after** `enctab_map_out` is called (which restores LMPR to `LMPR_DEFAULT`).  HSAVE itself manages HMPR; the caller only has to ensure LMPR is in a state where SAMDOS bank is reachable from section B (= LMPR_DEFAULT).

### LMPR state across the pass

| Stage | LMPR | HMPR | Notes |
|-------|------|------|-------|
| Start of `main_assemble` | LMPR_DEFAULT | (assembler page) | Captured at boot via `main_loop.asm` |
| `enctab_map_in` | `LMPR_ENCTAB = &24` | (assembler page) | ENCTAB at section A, page 5 at section B |
| `emit_byte_paged` (low zone) | LMPR_ENCTAB = &24 | (assembler page) | Write to section B = page 5; no LMPR change |
| `emit_byte_paged` (high zone) | LMPR=&25 within the call; LMPR_ENCTAB outside | (assembler page) | Saved/restored within emit; encoder sees LMPR_ENCTAB |
| Encoder ENCTAB read | LMPR_ENCTAB = &24 | (assembler page) | Always; emit's swap is fully bracketed |
| `enctab_map_out` | LMPR_DEFAULT | (assembler page) | ENCTAB out of section A |
| `save_out_file_paged` | LMPR_DEFAULT | (HSAVE-managed) | HSAVE OUTs its own HMPR per UIFA[31] |
| After `save_out_file_paged` | LMPR_DEFAULT | (assembler page) — HSAVE restores | Ready for `di / halt` |

### Page allocation

`OUT_BASE_PAGE = 5` as a constant; the assembler doesn't dynamically pick — there's nothing else competing for page 5 in M3..M6 scope.

`LMPR_OUT_CUR` is the runtime variable tracking the currently-mapped OUT page.  Lives in `main_loop.asm` scratch (1 byte).

### Removing the old section-C OUT buffer

`OUT_BUF = &B800` and `OUT_BUF_END = &C000` go away (`assembler.asm:33-34`).

The freed 2 KB at `&B800-&BFFF` becomes available for future use.  We don't repurpose it in this PR — leave for a follow-up if needed.

`OUT_BASE` storage word (`main_loop.asm` scratch) goes away.

### Boot-time self-tests

Add a self-test routine `run_out_paged_self_tests` that:

1. Resets the OUT buffer.
2. Emits 4 bytes (`&AA &BB &CC &DD`) via `emit_byte_paged`.
3. Reads back via LMPR-A swap, asserts the 4 bytes are present at `&0000..&0003` in page 5.
4. Asserts `OUT_LEN = 4`, `OUT_PC = 4`, `LMPR_OUT_CUR = &25`.

(Don't add a page-crossing test in this PR — that would require emitting 16 KB which slows boot.  Verify via a fixture instead.)

The self-test is included in the `m3-asm` (test) variant only; production omits it.  Existing pattern from M3/M4/M5 — gate via `if defined(BUILD_TESTS)`.

### Fixtures

Add a fixture `tests/m6/sources/inst_long_emit.s` (or similar) that exercises >2 KB of output to verify the paged path works end-to-end against GNU `as`:

```asm
.text
  .rept 1000
  add x0, x0, x0
  .endr
```

(`.rept` is a `text2bin`-side macro per `docs/specs/2026-05-25-macro-expansion-research.md`; verify it works on the SAM side or precompute the 4000-byte source.)

If `.rept` isn't supported by `text2bin`, generate the source via a small shell helper at the top of the M6 fixture sweep.

A second fixture exercising page-crossing (> 16 KB of output) is optional for the first M6 PR — page-crossing logic is exercised by the unit test in production; the cross-fixture test is a nice-to-have but adds CI time.

## What's NOT in this PR

- **Paged IN buffer.**  Source > 2 KB is the next M6 PR.  Will reuse the HLOAD trampoline from PR #31 with chunked loading.
- **Compact `.tbn` format.**  Separate strand (M6 PR series).
- **Disassembler.**  Falls out of compact `.tbn` work; separate strand.
- **Multi-output emission** via HSVBK.  Single HSAVE at end of pass 2 is enough for spectrum4 release.
- **64 KB output limit.**  16-bit OUT_LEN; debug builds need M7+.
- **`(hksp)` error handler** for graceful HSAVE failures.  Out of scope; current `di/halt` exit is consistent with M3..M5 behaviour.

## Risks

1. **LMPR bracket bug.** If `emit_byte_paged` ever returns without restoring LMPR to LMPR_ENCTAB, the next encoder read corrupts.  Mitigation: structure the routine so the restore path is unconditional; cover with a boot self-test.
2. **Page-crossing off-by-one.** If page bump happens AFTER write rather than BEFORE, the last byte of the old page is fine but the next byte goes to (old_page, &0000) instead of (new_page, &0000).  Spec: write first, increment OUT_PC after, then test for crossing. The crossing-test happens after the write so the first byte of new-page emits at the right place.
3. **HSAVE UIFA byte 34 encoding.** Length-mod-16K = `length & &3FFF`; pages = `length >> 14`. The snippet uses two RLCAs and AND 3 — verify against `samdos/src/h.s:352-359`.
4. **Code budget.** ~100 B for `emit_byte_paged` + `save_out_file_paged` + bookkeeping.  Production headroom drops from 420 to ~320 B. Sufficient buffer for the paged-IN PR to follow.

## Open questions deferred to plan / impl

- Exact placement of `emit_byte_paged` (`encoder.asm` vs new `src/emit.asm` module). Probably keep in `encoder.asm` for minimal churn.
- Whether to keep `OUT_LEN` as `defw` at the existing scratch location or move it. Probably keep — minimises diff.
- Whether the boot self-test fits in test-variant headroom (current: 4280 B in disk slot; new code is small).
