# Disassembler page placement — design note (2026-06-07)

This note answers the page-placement question for the Z80 port of
`tools/aarch64dec/` into `src/disasm.asm`, using the documented
paging mechanics from existing design docs.

---

## 1. Recommended page(s) and execution address

**Place the disassembler code on physical page 15, executed from
section C at `&8000–&BFFF` via `paged_call`.**

Page 15 is free (the ALLOCT table in `src/trampoline.asm:188-208`
shows pages 4–12 as "00H unused"; pages 13–14 are assigned to
sysreg data and paged_call self-test respectively;
`docs/notes/2026-05-28-memory-layout-brainstorm.md §3` reserves
pages 14–15 for "explanation prose" but that role has not landed
and can migrate to pages 16–17 when it does).

The disassembler is a production feature (not `BUILD_TESTS`-only),
so it needs a page that neither conflicts with existing production
data (page 13 = sysreg data, pages 5–6 = OUT buffer, pages 7–12 =
IN buffer, page 4 = ENCTAB) nor clobbers any test-only transient
payload (page 12 = cluster tests, page 14 = paged_call self-test).

Page 15 satisfies all constraints today.

Paired section D under HMPR=15 is page 16, which is currently free
on a 512 KB machine (pages 16–29 are unassigned in the brainstorm
doc). On a 256 KB machine pages 14–15 are screen pages; the
project targets 512 KB (PRAMTP = `0x1F` = 31 per `trampoline.asm`,
citing Tech Manual page allocation) — pages 15–17 are accessible.

---

## 2. Execution mechanism — step-by-step call sequence

The mechanism is the already-shipped `paged_call` helper
(`src/trampoline.asm:437–478`, body in `src/paged_bodies.asm`,
installed at `PAGED_CALL_DST = &7E40` in section B at boot).
No new mechanism is required; the disassembler port is the first
code-bearing consumer of `paged_call` (prior consumers are
data-on-code — the sysreg matcher at page 13 also lives in section
C under HMPR=13).

### Pre-conditions

- Interrupts disabled (`di` in effect throughout assembly, per
  `src/trampoline.asm:441`).
- `paged_call` body copied to `PAGED_CALL_DST = &7E40` in section B
  by `enctab_trampoline_setup` at boot.
- Disassembler code binary HLOAD'd into physical page 15 at boot,
  assembled at `org &8000` (section-C form).

### Call sequence

```
; Caller is in section C (&8000–&BFFF), HMPR = 1 (normal assembler
; code page).

; Pass the 32-bit word to decode via BC:IX or another HMPR-stable
; register pair (paged_call clobbers A, HL, DE; preserves BC, IX, IY).
ld      bc, <word_hi>
ld      ix, <word_lo>           ; or whichever ABI the Z80 port defines

call    paged_call              ; jumps to PAGED_CALL_DST = &7E40 in
                                ; section B (LMPR-stable, unaffected by
                                ; what paged_call does to HMPR)
defw    &8000                   ; entry point of disassembler in section C
defb    15                      ; target page number (low 5 bits)
                                ; paged_call ORs in HMPR bits 5-7 at runtime
; execution resumes here
```

Inside `paged_call` (in section B, fetched stably across the HMPR change):

1. DI (already disabled; re-affirmed).
2. Save SP to `PAGED_CALL_SP_SAVE` (section-B static slot `&7ED1`).
3. `IN A, (251)` — capture current HMPR (= 1, the assembler's page)
   to `PAGED_CALL_HMPR_SAVE` (`&7ED0`).
4. Pop inline payload: `defw &8000` → DE; `defb 15` → A.
5. Rewrite caller's return-addr slot with post-payload address.
6. `LD SP, TRAMP_SAFE_SP` (`&7F00`, in section B).
7. Push `paged_call_trailer` and then DE (`&8000`) onto the safe SP.
8. Merge: `A &= 0x1F; A |= (saved HMPR & 0xE0)` — preserves CLUT
   bits 5–6 and ext-mem bit 7 (`sam-paging.md:140-150`).
9. `OUT (251), A` — HMPR = 15: section C = page 15 (disassembler),
   section D = page 16 (free scratch).
10. `RET` — jumps to `&8000` in the now-mapped page 15: disassembler
    entry point.

Disassembler executes in section C (page 15), section D = page 16.
Section B = page 1 throughout (LMPR unchanged). On `RET` from the
disassembler:

11. Lands in `paged_call_trailer` (section B, `&7E40 + ~50 B`).
12. `EX AF, AF'` — preserve disassembler's AF return value.
13. `LD A, (PAGED_CALL_HMPR_SAVE); OUT (251), A` — HMPR restored to 1.
14. `LD SP, (PAGED_CALL_SP_SAVE)` — SP back to caller's stack.
15. `EX AF, AF'` — restore disassembler's AF.
16. `RET` — lands at caller's post-payload address.

### LMPR/HMPR values at each stage

| Stage | LMPR | HMPR | Section A | Section B | Section C | Section D |
|-------|------|------|-----------|-----------|-----------|-----------|
| Normal assembler | default (`&1F`) | 1 | ROM0 | page 0 | assembler code | assembler scratch |
| During paged_call | unchanged | **15** | ROM0 | page 0 | **disassembler** | page 16 (free) |
| After return | unchanged | 1 (restored) | ROM0 | page 0 | assembler code | assembler scratch |

LMPR is never touched. The trampoline in section B (`&7E40`) is
on page 1 under the default LMPR, which does not change — section B
fetch is stable across the HMPR swap. This is the structural
guarantee the existing HLOAD trampoline and the sysreg matcher
already rely on (`docs/notes/2026-05-28-paged-call-architecture.md
§3.2–3.3`).

---

## 3. Background: LMPR/HMPR independence and section-pair coupling

**LMPR and HMPR control independent halves of the address space.**
LMPR (port `0xFA`) controls section A (`&0000–&3FFF`) = page N,
and section B (`&4000–&7FFF`) = page N+1, automatically.  HMPR
(port `0xFB`) controls section C (`&8000–&BFFF`) = page M, and
section D (`&C000–&FFFF`) = page M+1, automatically.  "You can't
map non-adjacent pages to A and B" (`sam-paging.md:88–91`, citing
Tech Manual v3.0 lines 908–910).  The two registers are
independent: changing HMPR leaves LMPR (and therefore sections A
and B) entirely undisturbed.

**Why not section A?** The SAMDOS hook (RST 8 → `&0008` in ROM0)
requires ROM0 in section A during dispatch.  Paging the
disassembler code into section A via LMPR would displace ROM0 and
break all subsequent SAMDOS calls (HLOAD, HSAVE).  This was
analysed in `2026-05-28-paged-call-architecture.md §2.3` and is
why the project uses HMPR (section C) for code calls, not LMPR.

**Why not HMPR with section C being the current assembler page?**
Swapping HMPR puts section C = disassembler page.  The assembler
code currently in section C is temporarily invisible — but the call
originates from a `CALL paged_call` instruction that completes
before HMPR is changed, and `paged_call` itself lives in section B.
Post-swap instruction fetches come from section B (the trailer) or
from section C page 15 (the target), never from the displaced
assembler page.  On return, HMPR is restored before the caller's
next instruction is fetched.

---

## 4. Real-world SAM precedents for paged code execution

The existing design docs surface three real-world SAM precedents for
executing code from a paged-in page:

**SAMDOS hook dispatch (`PTDOS` at `&380E`)** — documented in
`sam-paging.md:§7` and `paged-call-architecture.md §1.4`.  When a
SAMDOS hook fires, PTDOS sets `LMPR = DOSFLG - 1`, placing SAMDOS
in section B (`&4000–&7FFF`), switches SP to `&8000`, and calls the
hook handler in SAMDOS code.  On return it restores LMPR.  This is
the SAM OS's own "page in a page of code, call it, restore" pattern.
The mechanism is specialised to SAMDOS's fixed page rather than
being parameterised, but the structural shape is identical to what
we need.

**COMET `getset` / `restore`** — documented in
`paged-call-architecture.md §1.6`, citing
`reference/comet-decoded/comet.asm:3161–3178`.  COMET (a
contemporary SAM assembler) executes code from paged pages via a
`getset` / `restore` bracket pair: save SP, switch SP to a
section-A scratch area, write the target page to LMPR (section A =
target page), `JP (HL)` to the target, target `CALL restore` to
pop LMPR back.  COMET uses this at ~15 call sites
(`paged-call-architecture.md §1.6`).  Unlike our `paged_call`
(HMPR-based, section-C target), COMET uses LMPR/section-A — a
viable alternative when the caller's code lives in section A, but
the section-B fetch-stability argument makes HMPR the correct
choice for our section-C callers.

**Spectrum 128K `RST 28H`** — documented in
`paged-call-architecture.md §1.5`, citing a ROM disassembly.  The
128K uses a RAM-resident `SWAP` + `YOUNGER` stub to execute code
from ROM1 with a symmetric call/return via stacked addresses.  The
structural note that "SWAP lives in RAM, not in either ROM" is
explicitly cited as the direct analogue of our trampoline living in
section B rather than section C (`paged-call-architecture.md:237`).

**SAM ROM `RST 30H`** — documented in `paged-call-architecture.md
§1.1`.  The SAM ROM uses RST 30H as a paged-call primitive for
calling into ROM1, with the target address inline after the RST.
Shape is similar but specialised to LMPR bit 6 (ROM1 on/off); no
HMPR or arbitrary RAM support.

**Conclusion**: the docs document real-world SAM paged-code
execution in COMET and SAMDOS (both obtained from source).  The
exact `paged_call` shape (HMPR-swapped, section-B trampoline,
inline `defw addr; defb page`) is the project's own synthesis from
those precedents, confirmed working by the existing paged_call
infrastructure (PR #55 + sysreg matcher landing).

The project sources on the current machine (`/home/pmoore/git/`)
include `reference/comet-decoded/comet.asm` and
`~/git/samdos/src/`.  No additional sources from another machine
are needed to confirm the mechanism — the `paged_call` primitive is
already shipped and exercised in CI.

---

## 5. Estimated Z80 footprint

The Go disassembler has ~1,800 lines of implementation across 9 files
(excluding tests).  In Z80 terms, the relevant mapping is:

| Go file | Lines (impl) | Z80 analogue | Rough Z80 size |
|---------|-------------|--------------|----------------|
| `disasm.go` | 143 | Top-level dispatch + form-table walk | ~200 B |
| `aliases.go` | ~500 | Bitfield/movwide/condsel/shift alias decoders | ~700 B |
| `mem.go` | ~350 | Load/store scalar, pair, literal, reg-offset | ~500 B |
| `sys.go` | ~250 | MRS/MSR/barriers/DC/TLBI/AT/IC | ~400 B |
| `dpreg.go` | ~200 | Shifted + extended register | ~300 B |
| `slots.go` | ~130 | Per-slot decoders (reg, imm, cond, branch) | ~200 B |
| `slots_branch.go` | ~90 | Branch + ADR/ADRP target calculation | ~150 B |
| `slots_logical.go` | ~120 | Logical-imm DecodeBitMasks | ~200 B |
| `tbranch.go` | ~45 | TBZ/TBNZ | ~80 B |
| `asm.go` | ~82 | Top-level CLI glue | not needed on SAM |
| **Total** | **~1,800** | | **~2,700–3,500 B** |

The Go code uses `fmt.Sprintf` extensively for string output.  The
Z80 port writes directly to an output buffer (no format-string
interpreter overhead), so many call sites compress to simple
byte-copy loops or nibble-to-hex routines.  Alias selection is a
series of bit tests — translates to Z80 efficiently.  The bitmask
expansion (`decodeBitMasks`, ~60 lines in Go) is the most
algorithmically complex piece; expect ~300 B in Z80 including the
loop and the replicate-to-width step.

**Best estimate: 3–4 KB.**  A 16 KB page is comfortably sufficient;
the disassembler fits on a single page with room for future growth
(partial alias decoders etc. that are currently stubs in the Go
code).

The brainstorm doc's original estimate was "~2–3 KB rough"
(`2026-05-28-memory-layout-brainstorm.md §2 row 7`); the detailed
Go source scan raises this to 3–4 KB, still well within one 16 KB page.

---

## 6. Open questions and risks

1. **Page 15 vs pages 14–15 for "explanation prose"** — the brainstorm
   doc reserves pages 14–15 for explanation prose
   (`memory-layout-brainstorm.md §3`).  Page 14 is currently the
   `paged_call` self-test payload (`BUILD_TESTS` only); page 15 is
   free.  Using page 15 for the disassembler code means explanation
   prose must move to pages 16–17.  This is a minor page-assignment
   shift; it needs a constants update in `trampoline.asm` and the
   brainstorm doc, but no mechanism change.  Alternatively, keep
   pages 14–15 as prose and assign the disassembler to pages 16–17.
   **Pete's call.**

2. **String output ABI** — the Go disassembler returns `(mnem, operands
   string)`.  The Z80 port needs an output convention.  Candidates:
   (a) two null-terminated strings written to a section-B comm buffer
   (same pattern as the sysreg comm buffer at `SYSREG_COMM_NAME`), or
   (b) a single pre-formatted string with a tab separator into an
   output buffer already mapped in section D.  Option (a) matches the
   existing `paged_call` ABI; option (b) is slightly simpler to
   render but requires section D to hold the output buffer at call
   time.  Needs design before the Z80 port begins.

3. **PC-relative slot values** — `DecodeAt(pc, word)` feeds branch
   targets, ADR/ADRP, and LDR-literal offsets.  The Z80 port needs
   the current PC passed in.  The caller (disasm loop) knows the PC;
   it can be passed in IX or a section-B staging slot.

4. **Non-re-entrance of `paged_call`** — if the disassembler itself
   needs to call `paged_call` (e.g. to look up a sysreg name from
   page 13 while running under HMPR=15), the single static-save slot
   at `PAGED_CALL_HMPR_SAVE` is not safe.  The sysreg matcher on page
   13 is likely needed for `mrs`/`msr` decoding.  Options: (a) embed
   a minimal sysreg name table inside the disassembler page itself
   (duplicates ~400 B of data); (b) extend `paged_call` to a
   2-deep save stack; (c) structure the disassembler so the sysreg
   lookup is done by the caller (section-C code) before the
   `paged_call` into the disassembler.  **This is the key design
   decision for the Z80 port** and must be resolved before
   implementation begins.

5. **Test payload boot-sequencing with page 15** — if any
   `BUILD_TESTS`-only self-test needs to occupy page 15 transiently
   before the disassembler is loaded (analogous to
   `test_mem.bin`/page-13 + `sysreg_data.bin`/page-13 time-
   multiplex), the sequencing must be explicit in `loader.asm`.
   Check whether any existing test cluster spills onto page 15.

6. **256 KB machine compatibility** — on a 256 KB SAM, page 15 is the
   last RAM page and is marked screen memory by BASIC
   (`trampoline.asm:190-194`).  The project currently targets 512 KB;
   if 256 KB support is ever added, page-15 use needs revisiting.
   Not an immediate blocker.

---

## 7. Sources

- `docs/notes/sam-paging.md §1–2` — LMPR/HMPR semantics, section
  pairing, CLUT bits
- `docs/notes/2026-05-28-paged-call-architecture.md §1–3` — full
  mechanism design, precedent survey (SAMDOS/COMET/RST-30H/RST-28H)
- `docs/notes/2026-05-28-memory-layout-brainstorm.md §2–3` — page
  axis assignment, disassembler footprint estimate
- `docs/notes/memory-layout.md` — current physical page assignments
- `src/trampoline.asm:188–208, 437–478` — page constants, `paged_call`
  ABI and section-B layout
- `tools/aarch64dec/` — all 9 implementation files scanned for Z80
  footprint estimate
