# 2026-05-28 — HLOAD 16 KB limit investigation (PR #37 follow-up)

Investigation of the "effective IN ceiling ~16.5 KB" caveat shipped
with PR #37 (M6 PR 2 — paged IN).  The PR's author hypothesised that
HLOAD's auto-paging writes spill into section D under HMPR=IN_BASE_PAGE
and corrupt the user stack at `SP=&C100`, with **page 8 being shared
between the IN buffer's second page and the user stack**.  This
investigation tests that hypothesis empirically and traces the actual
root cause.

## Verdict

**The hang is real and deterministic.**  The threshold is `.tbn` size
`16632` bytes (works) → `16633` bytes (hangs); that's `16384 + 248`,
i.e. one full section-C page plus 248 bytes of section-D spillover.

**The agent's mechanism is conceptually right (HLOAD does write into
section D and that page coincides with HMPR+1) but the specific stack
frame being corrupted is wrong.**  The caller's stack at `SP=&C100`
(= page 8 offset `&100` under HMPR=7) is NOT live during HLOAD; ROM
PTDOS switches SP to `&8000` at hook entry
(`sam-coupe_rom-v3.0_annotated-disassembly.txt:12956`,
`LD SP,8000H ;STACK NOW OK`) and restores it on return.  The
caller-pushed return addresses at `&C0FA..&C0FF` (in page 8 offsets
`&FA..&FF`) **are** in HLOAD's potential write region, but HLOAD's
writes start filling page 8 from offset 0 upward — at file size
16632, HLOAD's last byte lands at page 8 offset `&F7`.

**The actual culprit is the trampoline's own `RST 8`.**  The trampoline
sets HMPR=IN_BASE_PAGE *before* the `RST 8`.  That `RST 8` pushes the
return address at the current SP (= `&C0F8`, with the user's pre-call
stack underneath at `&C0FA..&C0FF`) — but `&C0F8/&C0F9` is in section D
under HMPR=7, i.e. **page 8 offset `&F8/&F9`**.  HLOAD's writes need to
reach offset `&F8` to clobber the low byte of that return address, and
they do at exactly file byte 16384 + 248 = 16632 (= the last "good"
size; the 16633-byte file is the first to write offset `&F8`).

When HLOAD eventually returns, ROM PTDOS restores caller-SP to `&C0F8`
and executes `RET`; the `RET` pops the corrupted return address (the
file's load bytes 248/249 are not valid Z80 code) and jumps to garbage
→ hang.

## COMET pattern — confirms or refutes?

**Confirms our diagnosis indirectly: COMET solves this by switching
SP to LMPR-stable memory BEFORE the trampoline.**

Reading `reference/comet-decoded/comet.asm`:

- `comet.asm:1189` — caller does `LD SP,(sproom)` BEFORE setting
  HMPR.  `sproom` (line 4876) lives in COMET's data segment in
  page 31; with LMPR=31 (set at line 1185 `OUT (250),A`), `sproom`
  is in section A — **LMPR-stable across the trampoline's HMPR
  change**.
- `comet.asm:1273-1284 loaddata` — the actual trampoline body.
  Pushes AF onto the now-SP-switched stack (which is in section A,
  not section D).  The `RST 8` push therefore lands in section A
  too — safe from HMPR-controlled paging.

So COMET routinely loads files larger than 16 KB via HLOAD with HMPR
set to the load destination — **because it parks its stack in
LMPR-stable memory first**.  Our trampoline does NOT do this.

This is the workaround the design note at `trampoline.asm:57-62`
explicitly mentions: "COMET avoids this by switching SP into a
section-A/B scratch area via `LD SP, (sproom)` at the call site."
The design then chose a different workaround (the `HMPR_SAVE` static
byte in section B) — but that workaround only addresses the HMPR
save/restore problem, NOT the `RST 8` return-address problem.

## SAMDOS HLOAD mechanism (source-confirmed)

From `~/git/samdos/src/`:

1. **PTDOS dispatcher** (`sam-coupe_rom-v3.0:12944-12978`):
   - Reads caller SP into HL (`LD HL,0; ADD HL,SP`).
   - **Switches SP to `&8000`** (`LD SP,8000H`).
   - PUSHes saved BC (LMPR) and HL (caller SP) onto the new SP=&8000
     stack — i.e. into section B under whatever LMPR is.
   - Calls SAMDOS hook handler at `&4200..` (in section B = DOS page
     under PTDOS's LMPR).
   - On return: pops BC, HL; restores LMPR and caller SP.
   - **Caller's stack memory is not touched by PTDOS.**

2. **SAMDOS hook entry** (`b.s:439 hook`):
   - Saves SP to `entsp`.
   - Calls per-code handler via `samhk` table.

3. **HLOAD → `c.s:ldblk`** (the per-byte loader loop):
   - `ldb1` (line 579-585): reads next byte from sector buffer,
     `LD (svhl),HL → LD A,(buf) → LD (HL),A`, increments HL, stores
     back.  **Writes pass through any address &8000..&FFFF without
     a per-byte HMPR check.**
   - `ctas` (line 318+, called between sectors): checks if H ≥ &C0,
     and if so, clears bit 6 of H (re-map `&Cxxx` to `&8xxx`) and
     increments HMPR's low 5 bits (`c.s:354-369`).
   - **Crucial**: ctas runs PER SECTOR (~512 bytes), not per byte.
     So within a sector, writes proceed through `&BFFF → &C000 →
     &C0xx` until the sector boundary, where ctas catches up.

So writes to addresses &C000..&C200 (up to one sector past the section
boundary) actually happen, depositing data into section D = page
HMPR+1.  This is the spill region the agent identified.

## Empirical bisect

Pipeline: dev-toolchain text2bin + Mac SimCoupé (patched local build
with `-exitonhalt`).  Fixture: header + N long comment records
sized to hit a target `.tbn` size; tested on the production
`assembler.bin` from this worktree's PR #37 state.

| `.tbn` size | rc | status | wall-clock | meaning |
|---|---|---|---|---|
| 16632 | 0 | OK | 2 s | last clean size |
| 16633 | 124 | (empty) | 31 s | first hang (SimCoupé killed by 30 s timeout) |
| 16634 | 124 | (empty) | 31 s | hang |
| 16636 | 124 | (empty) | 31 s | hang |
| 16800 | 124 | (empty) | 31 s | hang |
| 32000 | 124 | (empty) | 31 s | hang |

The threshold is **exactly** 16384 + 248 / 16384 + 249.

Arithmetic check: HMPR=7 → section D = page 8.  HLOAD writes byte
`N` of the file at address `&8000 + N` modulo the section structure.

| File byte index | Address | Page-8 offset |
|---|---|---|
| 0..16383 | `&8000..&BFFF` | n/a (in page 7) |
| 16384 | `&C000` | `&00` |
| 16384+247 = 16631 | `&C0F7` | `&F7` |
| 16384+248 = 16632 | `&C0F8` | `&F8`  ← first byte hitting the RST 8 return-address low byte |

So writing 16633 bytes of file data means byte 16632 (the 16633rd byte)
gets written to page 8 offset `&F8` — exactly where the trampoline's
`RST 8` parked its return-address low byte.

## IN_BASE_PAGE alternative test

Changed `IN_BASE_PAGE` from 7 to 9, rebuilt, retested.  Result: **the
threshold is identical** — 16632 OK, 16636 hang.

This refutes the agent's stated hypothesis (which framed the issue as
"page 8 happens to be the IN buffer's second page AND the stack page").
The hang has nothing to do with the specific page number — it's about
the fact that **section D under HMPR=IN_BASE_PAGE is the same physical
page as HMPR+1**, which by definition holds the bytes that overflow past
`&BFFF`, AND the RST 8 return address ends up in that page because the
trampoline didn't relocate SP.

## Trampoline review — bugs found

The trampoline (`src/m3/trampoline.asm:344-367`) does NOT switch SP
before changing HMPR.  Quoting the existing comment at line 57-62:

> COMET avoids this by switching SP into a section-A/B scratch area
> via `LD SP, (sproom)` at the call site (`comet.asm:1189`).  We
> avoid it differently: we save the HMPR byte in a STATIC LOCATION
> in section B, right next to the trampoline body.

That comment correctly identified the HMPR-save problem, but the chosen
workaround (static save) addresses only the `IN/POP` mismatch on the
HMPR-byte preservation — it does NOT address the fact that **the `RST
8` push itself lands in section D**.  At file sizes < 16632 the RST 8
push at `&C0F8/&C0F9` survives because HLOAD's writes stop before
reaching those offsets, so the bug is latent.

## Verification of the fix

A one-line addition to the trampoline (switch SP to a section-B safe
location around the RST 8) eliminates the limit.  Verified by patching
the trampoline body to:

```asm
trampoline_body:
        in      a, (251)
        ld      (HMPR_SAVE), a
        ld      a, b
        ld      (SP_SAVE), sp           ; new: save caller's SP
        ld      sp, TRAMP_SAFE_SP       ; new: switch to section-B SP
        out     (251), a                ; HMPR := target
        rst     8
        defb    HOOK_HLOAD
        ex      af, af'
        ld      a, (HMPR_SAVE)
        out     (251), a                ; HMPR restored
        ld      sp, (SP_SAVE)           ; new: restore caller SP
        ex      af, af'
        di
        ret
```

with `SP_SAVE` and `TRAMP_SAFE_SP` defined as section-B addresses near
`HMPR_SAVE`.  Re-bisect with this patch:

| `.tbn` size | rc | status | meaning |
|---|---|---|---|
| 16632 | 0 | OK | (previously clean) still clean |
| 16800 | 0 | OK | **previously hung; now clean** |
| 17000 | 0 | OK | clean |
| 20000 | 0 | OK | clean |
| 24000 | 0 | OK | clean |
| 32000 | 0 | OK | clean |
| 48000 | 1 | FAIL | clean exit with banner; unrelated downstream cap (likely litpool/symbol budget at this scale, not HLOAD) |

Full `make ci-m6` (2 fixtures) passes with the fix in place.

## Root cause (precise)

`src/m3/trampoline.asm:344-367` — the trampoline issues `RST 8` while
SP points into section D under the trampoline's own HMPR change.  The
`RST 8` push lands in page IN_BASE_PAGE+1 at offset (SP-2) & 0x3FFF.
With our memory layout that's page 8 offset `&F8`.  HLOAD's auto-paging
fills page 8 from offset 0 upward; once file bytes reach offset `&F8`
the saved return address is overwritten and the subsequent `RET` (in
PTDOS's dispatcher, after HLOAD completes) jumps to garbage → hang.

## Recommended action

**Fix in a follow-up PR, not in PR #37** (PR #37's mid-flight; merging
the caveat and addressing it next preserves a clean fix-vs-test
attribution and matches the design intent of the existing PR body).

Specifically:

1. Land PR #37 as-is, possibly tightening the caveat language in
   `docs/notes/m6-status.md` to reflect the corrected mechanism
   (the rst 8 return address, not the user stack frame).
2. Follow-up PR: add the SP-switch to `trampoline_body` (the patch
   above).  Three additional instructions (`ld (SP_SAVE),sp; ld sp,
   TRAMP_SAFE_SP; ld sp,(SP_SAVE)`), 11 bytes of code.  This lifts
   the ceiling from ~16.5 KB to the design's intended 64 KB (page
   8..11 = 4 × 16 KB).
3. Update `docs/specs/2026-05-27-samdos-load-idiom.md` "Pre-built
   trampoline reference" §"Stack handling" to document the SP-switch
   pattern as the canonical fix and explain why the static-save
   approach was insufficient.

The follow-up PR's fixture should include a > 32 KB `.tbn` to lock
in the corrected behaviour.

## References

- `src/m3/trampoline.asm:344-367` — trampoline body (PR #37 state)
- `src/m3/main_loop.asm:2129-2169` — `load_in_file` caller
- `~/git/samdos/src/c.s:318-369` — `ctas` (HLOAD's HMPR auto-paging)
- `~/git/samdos/src/c.s:575-672` — `ldblk` (per-byte load loop)
- `~/git/samdos/src/h.s:70-90` — `hload` / `dschd`
- `~/git/samdos/src/b.s:439-470` — `hook` (RST 8 entry)
- `docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt:12944-12978`
  — ROM PTDOS dispatcher (SP-switch to &8000)
- `reference/comet-decoded/comet.asm:1184-1284` — COMET's call sites
  + `loaddata` trampoline (the canonical example)
- `reference/comet-decoded/comet.asm:4876` — COMET's `sproom`
  (section-A SP-save slot)
