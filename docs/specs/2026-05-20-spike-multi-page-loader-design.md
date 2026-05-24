# basic-detokeniser-spike: multi-page program loader

**Date:** 2026-05-20
**Status:** Design, awaiting reviewer-agent verification and user approval.
**Predecessor:** `2026-05-14-basic-detokeniser-spike-design.md` (original
single-page spike design).

## Goal

Extend `tools/basic-detokeniser-spike` to decode tokenised BASIC
programs that span more than one physical RAM page, lifting the
current `~25 KB` ceiling. The spike is the intended **final**
implementation — `samfile basic-to-text` and the LLIST-under-SimCoupé
oracle exist only to validate it — so the spike must succeed in every
case the SAM ROM itself would. After this work, the spike supports
programs up to the full 512 KB hardware limit, the same envelope a
real 512 K SAM Coupé permits.

## Background

`loadProgViaPoke` (`tools/basic-detokeniser-spike/main.go:306`) writes
the tokenised program contiguously into the Z80 16-bit address space
starting at `PROG = 0x9CD5`. It temporarily clears `LMPR` bit 6 so
section D maps to RAM rather than ROM1, giving it section-C + section-D
to write into. The upper bound is `0xFFFF`, so the maximum program
length is ~25 KB. Anything larger calls `log.Fatalf` with the message
"program too large for two-page poke: HMPR-paged multi-page programs
not supported" (`main.go:329`).

In the 2026-05-20 full-corpus 4-way sweep this limit hit **109 of
6667 attempted jobs (1.6%)**. Size distribution:

| size class      | count |
|-----------------|------:|
| < 32 KB         |    46 |
| 32 – 64 KB      |    49 |
| 64 – 128 KB     |    12 |
| > 128 KB        |     2 |

The SAM ROM itself loads and edits programs of arbitrary size up to
RAM availability — it stores all BASIC-area pointers in **REL PAGE
FORM** (`<NAME>P` page-byte + `<NAME>` 16-bit offset pairs), and its
editor / FNDLINE / OUTLINE traversal uses page-aware helpers
(`TSURPG`, `INCURPAGE`, `DECURPAGE`) that follow the line linked list
across page boundaries transparently. The piece our spike is missing
is **not the editor path** (which the ROM handles) but **the loader**
(which currently bails before the editor gets a chance).

## Approach (chosen)

**Extend the poke loader to be paging-aware, set all BASIC sysvars in
REL PAGE FORM, and update ALLOCT / LASTPAGE / RAMTOP to claim the
extra pages.**

A separate investigation (`Agent: ROM LOAD-BASIC investigation`,
2026-05-20) confirmed the ROM's own LOAD path is partly tractable but
involves ~15-20 routines including PTDOS hook dispatch, the BUFF256
scratch buffer, R1OSR/POPOUT bracketing, and a complex XOINTERS
pointer-adjustment subroutine. Reproducing it faithfully via
`cpu.Run` is bordering-tractable but high-effort. Extending our
existing direct-poke approach is cleaner: the page-advancement
pattern itself (after 16 K, bump page, reset offset, continue) is
mechanically simple, and we already bypass SAMDOS / TESTROOM /
MAKEROOM successfully for the small-program case.

The samfile-side aspiration to "eventually write to disk via the DOS
on the disk, like a real SAM" is **explicitly deferred** —
out of scope for this work.

## Design

### 1. Memory layout + REL PAGE FORM

PROG itself stays at the ROM-canonical `0x9CD5`. In page terms that's
`(page=1, offset_in_page=0x1CD5)`. The loader writes the tokenised
program byte-by-byte across consecutive physical pages 1, 2, 3, …
until the program + trailer + 150-byte WKEND headroom is exhausted.

```
       physical page    offset    content
       --------------   ------    -------
PROG → page 1           0x1CD5    program byte 0
                        ...
       page 1           0x3FFF    program byte 0x232A   (= 9003 bytes in page 1)
       page 2           0x0000    program byte 0x232B
                        ...
       page N           offset    program byte len-1 = 0xFF (end-of-program sentinel)
NVARS → page N          offset    92-byte canonicalNumericVars
NUMEND →                          start of 512-byte zero-filled gap
SAVARS →                          (typically empty for fresh saves)
ELINE  →                          ← editor line buffer
WORKSP →                          ← editor workspace
WKEND  →                          150 bytes below RAMTOP (ROM headroom rule)
```

Every BASIC-area pointer is stored as a `<NAME>P / <NAME>` pair
(verified against the ROM v3.0 disasm at lines 869-900):

```
SAVARSP / SAVARS    0x5A81 / 0x5A82
NUMENDP / NUMEND    0x5A84 / 0x5A85
NVARSP  / NVARS     0x5A87 / 0x5A88
WKENDP  / WKEND     0x5A8D / 0x5A8E
WORKSPP / WORKSP    0x5A90 / 0x5A91
ELINEP  / ELINE     0x5A93 / 0x5A94
CHADP   / CHAD      0x5A96 / 0x5A97
KCURP   / KCUR      0x5A99 / 0x5A9A
NXTLINEP/ NXTLINE   0x5A9C / 0x5A9D
PROGP   / PROG      0x5A9F / 0x5AA0
RAMTOPP / RAMTOP    0x5CB1 / 0x5CB2
```

For each pair, the page byte is the **physical** RAM page (0–31)
where the byte lives; the offset is `0x8000 | (offset_within_page &
0x3FFF)` — the ROM's convention of forcing the offset into the
section-C address window. Encoding is per `UNSTLEN` at ROM 0x3F8C
(disasm lines 14773-14786). `DATADD / DATADDP` (0x5A8A/0x5A8B) is
**not** in this list — `DATADD` points outside the BASIC area and
isn't bumped by load.

ALLOCT (`0x5100`, 32 bytes, one per page) gets `0x40` ("IN USE,
CONTEXT 0", matching the boot-time table at disasm line 24496) for
each page used beyond the boot-default 0..3. `LASTPAGE` (`0x5CB0`)
is updated to the highest page actually used. `RAMTOP` is set to the
section-C offset `0xBFFF` with `RAMTOPP` equal to `LASTPAGE`.

### 2. Loader shape

Replace the current `loadProgViaPoke` entirely. The unified loader
handles small programs as the degenerate case of the multi-page
algorithm — no fork in the control flow:

```go
func loadProgViaPoke(hw *Hardware, progBytes []byte) {
    const (
        progPageStart    = uint8(1)
        progOffsetInPage = uint16(0x1CD5)   // 0x9CD5 in section-C form
        trailerShiftLen  = 1024             // matches current spike (covers
                                            // canonicalNumericVars + gap +
                                            // ELINE/WORKSP scratch)
        wkendHeadroom    = 150              // ROM 0x1E25 rule
    )

    // Snapshot the post-boot relationship between NVARS and every
    // downstream sysvar so we can preserve it in the new layout. This
    // matches the current spike's "bump every sysvar by the same
    // delta" pattern, just generalised to (page, offset) coordinates.
    progPos := pos{progPageStart, progOffsetInPage}
    oldNVARS := peekRAM16(hw, sysNVARS)
    if oldNVARS != peekRAM16(hw, sysPROG)+1 {
        log.Fatalf("expected post-boot NVARS=PROG+1, got NVARS=%04X PROG=%04X",
            oldNVARS, peekRAM16(hw, sysPROG))
    }
    type sysvarSpec struct{ pageAddr, offsetAddr, deltaFromNVARS uint16 }
    bumpedSysvars := []sysvarSpec{
        {sysNVARSP,    sysNVARS,    0},
        {sysNUMENDP,   sysNUMEND,   peekRAM16(hw, sysNUMEND)   - oldNVARS},
        {sysSAVARSP,   sysSAVARS,   peekRAM16(hw, sysSAVARS)   - oldNVARS},
        {sysWKENDP,    sysWKEND,    peekRAM16(hw, sysWKEND)    - oldNVARS},
        {sysWORKSPP,   sysWORKSP,   peekRAM16(hw, sysWORKSP)   - oldNVARS},
        {sysELINEP,    sysELINE,    peekRAM16(hw, sysELINE)    - oldNVARS},
        {sysCHADP,     sysCHAD,     peekRAM16(hw, sysCHAD)     - oldNVARS},
        {sysKCURP,     sysKCUR,     peekRAM16(hw, sysKCUR)     - oldNVARS},
        {sysNXTLINEP,  sysNXTLINE,  peekRAM16(hw, sysNXTLINE)  - oldNVARS},
    }

    // Size guard: bail before any writes if the program + trailer
    // shift + editor headroom won't fit in physical RAM.
    pramtp := peekRAM(hw, sysPRAMTP)
    totalNeeded := len(progBytes) + trailerShiftLen + wkendHeadroom
    totalAvailable := (int(pramtp)+1)*0x4000 -
                      int(progPageStart)*0x4000 -
                      int(progOffsetInPage)
    if totalNeeded > totalAvailable {
        log.Fatalf("program does not fit in BASIC pages: len=%d "+
            "shift=%d headroom=%d need=%d available=%d "+
            "(PRAMTP=%02X, pages %d..%d)",
            len(progBytes), trailerShiftLen, wkendHeadroom,
            totalNeeded, totalAvailable, pramtp,
            progPageStart, pramtp)
    }

    // Step 1. Shift the post-boot trailer (canonicalNumericVars + gap
    // + ELINE/WORKSP scratch — 1024 bytes from oldNVARS upward) to its
    // new home at NVARS+delta. Walk backwards to avoid clobbering on
    // overlap when the program is short (small-program case). Source
    // reads use the paged peekRAM (everything's in page 1's section-C
    // window); destination writes use pokeRAMPage to land bytes in
    // the correct physical page regardless of HMPR state.
    newNVARSPos := progPos.advance(len(progBytes))
    for i := trailerShiftLen - 1; i >= 0; i-- {
        b := peekRAM(hw, oldNVARS+uint16(i))
        dst := newNVARSPos.advance(i)
        pokeRAMPage(hw, dst.page, dst.offset, b)
    }

    // Step 2. Write progBytes byte-by-byte starting at PROG, walking
    // across pages as needed. Done AFTER the trailer shift so that
    // for small programs the source/dest overlap is identical to the
    // current spike.
    cur := progPos
    for _, b := range progBytes {
        pokeRAMPage(hw, cur.page, cur.offset, b)
        cur = cur.advance(1)
    }

    // Step 3. Write all sysvar pairs in REL PAGE FORM. PROG itself is
    // unchanged but we write the pair explicitly so PROGP is set to 1
    // rather than left at whatever the boot init produced.
    setSysvarPair(hw, sysPROGP, sysPROG, progPos)
    for _, s := range bumpedSysvars {
        p := newNVARSPos.advance(int(s.deltaFromNVARS))
        setSysvarPair(hw, s.pageAddr, s.offsetAddr, p)
    }

    // Step 4. ALLOCT + LASTPAGE + RAMTOP for any page beyond 0..3.
    wkendPos := newNVARSPos.advance(int(peekRAM16(hw, sysWKEND) - oldNVARS))
    maxPage := wkendPos.page
    for p := uint8(4); p <= maxPage; p++ {
        pokeRAM(hw, allocTableBase+uint16(p), 0x40)
    }
    if maxPage > 3 {
        pokeRAM(hw, sysLASTPAGE, maxPage)
        pokeRAM(hw, sysRAMTOPP, maxPage)
        pokeRAM16(hw, sysRAMTOP, 0xBFFF)
    }
}
```

Three new helpers in the same file:

```go
// pos is a (page, offset_in_page) coordinate. Encapsulates the page-
// boundary carry so callers don't repeat the arithmetic.
type pos struct {
    page   uint8
    offset uint16
}

func (p pos) advance(n int) pos {
    total := uint32(p.offset) + uint32(n)
    return pos{
        page:   p.page + uint8(total >> 14),
        offset: uint16(total & 0x3FFF),
    }
}

// pokeRAMPage writes directly to a specific physical RAM page,
// bypassing LMPR/HMPR resolution. Used only by the multi-page loader.
func pokeRAMPage(hw *Hardware, page uint8, offset uint16, v uint8) {
    hw.ram[page&0x1F][offset&0x3FFF] = v
}

// setSysvarPair stores a (page, offset) in REL PAGE FORM at the given
// sysvar addresses. The offset gets the section-C bit (0x8000) set;
// the page byte is the physical RAM page.
func setSysvarPair(hw *Hardware, pageAddr, offsetAddr uint16, p pos) {
    pokeRAM(hw, pageAddr, p.page)
    pokeRAM16(hw, offsetAddr, 0x8000 | (p.offset & 0x3FFF))
}
```

### 3. Error handling

The size guard above is the only new error case. Everything else
degrades through existing paths:

| condition                                 | path                                                              |
|-------------------------------------------|-------------------------------------------------------------------|
| Per-line length > 0x3EFF (ROM 0x10C1)     | EDKY-driven capture doesn't invoke that check; if ELINE overruns its buffer during capture, existing `editLineAndCapture` returns "no 0x0D found within N bytes" |
| Garbage program bytes (bad sentinel etc.) | Pre-validated by `samfile basic-to-text` and READ-ERROR path upstream |
| Empty program (0 lines)                   | `extractAllLines` iterates over `basFile.Lines`; empty input ⇒ empty output |
| Programs overlapping screen pages         | Spike never refreshes display; screen-page collision invisible |

### 4. Testing strategy

**Layer 1 — unit tests** (`tools/basic-detokeniser-spike/loader_test.go`):

- `TestPageWalk_*` — boundary cases for `pos.advance` and writes that
  stay in page 1, cross to page 2, and span 3+ pages.
- `TestRelPageFormEncode` — table of `(linear_offset_from_PROG,
  expected_page_byte, expected_offset_with_0x8000_bit)` covering each
  page transition.
- `TestSysvarPairsAfterLoad` — synthetic 40 KB program; every `<NAME>P
  / <NAME>` pair matches expected REL-PAGE-FORM encoding; ALLOCT
  entries for pages 4+ are 0x40; LASTPAGE updated.
- `TestSizeGuard_*` — at PRAMTP=0x0F and PRAMTP=0x1F, programs that
  fit and programs that don't.

These construct a fresh `Hardware`, call `loadProgViaPoke`, and
inspect `hw.ram` directly. No ROM emulation needed.

**Layer 2 — corpus regression.**

- **Phase A — the 109 previously-failing files.** Spike on each;
  confirm non-empty `.spike.txt` for every case. Compare against the
  existing `.llist.txt` modulo the known wrap/`>` differences (handled
  by Pete's tomorrow-task; not in this design's scope).
- **Phase B — 20 random previously-CAPTURED files, with timing.** For
  each: run new spike, diff vs existing `.spike.txt`. Pass criterion:
  byte-identical output. Record per-file wall time. Average yields an
  empirical `seconds-per-spike` figure.

**Decision point.** After Phase B, project a full spike-only rerun
cost = `avg_seconds × 6667`. If it's < 30 minutes, ratify with a full
spike-only sweep to produce a clean ground-truth dataset. If higher,
defer and proceed with Phase A+B alone as sufficient evidence.

**Layer 3 — optional full spike-only sweep.** Decision deferred per
Layer 2. A small orchestrator script (~30 lines, `run-spike-only.sh`
or `tools/spike-sweep/`) loops the spike binary over the corpus
without the slow llist / samfile-b2t comparison paths. Spec for that
tool is a follow-up, not part of this work.

## Open questions / risks

Honest list of things this design assumes but doesn't fully prove.
The reviewer agent should flag any that need empirical verification
before implementation, and the implementation plan should explicitly
test each.

1. **Screen page overlap.** On a 512 K SAM, the ROM init typically
   places the screen near the top of physical RAM (e.g. pages 30-31).
   If a large BASIC program extends into the screen pages, the program
   bytes physically overwrite whatever the screen page held. The spike
   never refreshes the display, but if the ROM's EDKY path reads any
   pixel-data area as part of editor housekeeping, our program bytes
   could end up "looking like" garbage screen state to it. Mitigation
   options: set VMPR to a safe sentinel page at spike boot, or cap
   `maxPage` below the screen's location. Needs an empirical check
   against a real 128 KB+ program.

2. **ROM editor traversal across page boundaries.** The agent's
   investigation showed `FNDLINE` / `EDKY` / `OUTLINE` rely on
   `INCURPAGE` / `DECURPAGE` / `PDPSR2` to walk multi-page linked
   lists. We're trusting the ROM's REL-PAGE-FORM-aware logic across
   the full page range, not just within the 4 BASIC default pages
   the small-program case has exercised. Phase A's diff-against-llist
   catches any silent divergence here. (`DATADDP` was a concern in an
   earlier draft; the reviewer agent confirmed ROM boot doesn't
   initialise it either and our skip is correct.)

3. **`trailerShiftLen = 1024` accuracy for large programs.** The
   current spike's 1024-byte memmove was sized "generously" to cover
   the post-boot canonicalNumericVars + gap + editor scratch. For a
   multi-page program, that 1024 bytes lands across a page boundary
   if and only if NVARS lands within 1024 bytes of a page edge. The
   page-aware walk handles this fine, but it's worth flagging that
   the 1024 is empirically chosen and may not be exactly the right
   number for unusual programs.

4. **PROGP write is required, not redundant.** Earlier draft of this
   spec assumed ROM boot initialises PROGP to 1; the reviewer agent
   confirmed against the disasm that **ROM boot does not write PROGP
   at all** (no `LD (PROGP), …` in the init sequence between disasm
   lines 24400-24700). Code that reads PROGP later (e.g. FNDLINE at
   ROM 0x1A52) is reading whatever happens to be there. The current
   spike has gotten away with this because small programs leave
   FNDLINE's traversal entirely within page 1, where PROGP=0 versus
   PROGP=1 doesn't matter for the math. Multi-page programs make
   the page-byte load-bearing, so our explicit `setSysvarPair(hw,
   sysPROGP, sysPROG, progPos)` is the right move.

5. **Detail-truncation feedback loop.** During Phase A and B, the
   sweep TSV's 200-char truncation could obscure which tool produced
   what error. Mitigation: write a tiny side-by-side `diff` for each
   Phase A failure into a per-job sidecar file, independent of the
   TSV.

## Follow-ups (out of scope, noted for tracking)

- **TSV detail truncation.** `basic-detokeniser-sweep`'s `sanitize()`
  caps the detail column at 200 chars (`main.go:184`). Verbose
  multi-tool-fail rows lose the llist/b2t-lossy markers, causing
  the morning analysis to under-count llist failures by 410. Easy
  fix: bump cap, or write full detail to per-job sidecar files.
- **Spike-only sweep tool.** Mentioned above. The current
  `basic-detokeniser-sweep` always pairs spike with at least one
  oracle; a "spike alone, capture only" mode would simplify
  regression runs.
- **samfile-via-DOS.** Pete's long-term goal: samfile writes to MGT
  via whatever DOS is on the disk, so it operates independently of
  ROM-version assumptions. Wholly separate work; no overlap with
  this design.

## References

- ROM v3.0 disasm:
  `docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt`
- SAM BASIC SAVE format: `docs/notes/sam-basic-save-format.md`
- SAM paging hardware + REL PAGE FORM:
  `docs/notes/sam-paging.md`
- Original spike design:
  `docs/superpowers/specs/2026-05-14-basic-detokeniser-spike-design.md`
- Current loader: `tools/basic-detokeniser-spike/main.go:306-356`
- Sweep & 4-way captures (2026-05-20 run):
  `~/detok-captures/`, `~/detok-sweep.tsv`, `~/detok-sweep.log`
