; loader.asm — enctab.enc reader and header validator.
;
; Loads the entire encoder-table file from disk into ENCTAB_BUF via SAMDOS
; HGTHD (hook 129) + HLOAD (hook 130), then validates the magic "ENC1" and
; version=1 in RAM.  This mirrors how BASIC's `LOAD CODE` ultimately drives
; SAMDOS (gtfle + ldblk) and is the documented "production" path for
; loading whole files into a non-section-A target.
;
; Per docs/specs/2026-05-24-m3-z80-emitter-design.md §2.3.
; Format citation: docs/specs/2026-05-24-m2-encoder-tables-design.md §2.
;
; Why HGTHD + HLOAD and not HGFLE + LBYT?
;
; HGFLE+LBYT is the byte-at-a-time API, but a previous spike found the
; first LBYT after HGFLE returning 0x00 instead of the file's first payload
; byte ('E' = 0x45).  The audit (`docs/notes/sam-stub-audit.md` §"Hook 158
; — HGFLE") asserts HGFLE leaves the read pointer past the 9-byte file
; header — but that claim is from static reading of the SAMDOS source,
; never observed in practice.  HSAVE-then-extract diagnostics confirmed all
; eight LBYTs after HGFLE return 0x00.  Root cause unclear; HLOAD instead
; bypasses LBYT entirely and uses ldblk's block-copy loop, which is what
; BASIC's `LOAD CODE` provably exercises every time the auto-boot loads
; "assembler" before we even start running.
;
; Memory layout used here:
;   &8000-&8FFF  assembler code (this file + includes; ~683 bytes today)
;   &9000-&BFFF  enctab buffer (12 KB; holds entire enctab.enc file body)
;   &C000-&C0FF  stack (SP = &C100, grows down into section D)
;
; ENCTAB_BUF (&9000) lives inside section C (&8000-&BFFF, the HMPR page),
; satisfying the Tech Manual's "HL must be &8000-&BFFF" rule for HLOAD
; (page 211).  The rule is enforced by the auto-wrap-fix in SAMDOS's
; `ctas` (`samdos/src/c.s:347-369`) which fires on every track step:
; HL just outside &8000-&BFFF wraps to &8000 the next time a sector
; crosses a track boundary.  Earlier M3 spikes loaded into section D
; (&C100) and dodged the wrap by ensuring enctab.enc lived on a single
; track — fragile, since growing the table or moving it on disk would
; silently corrupt the load.  Keeping HL in section C makes the load
; correct regardless of the file's on-disk layout.
;
; Stack at &C100 is fine: only HLOAD's destination is constrained by
; the section-C rule.  SP grows down into section D (&C000-&FFFF) which
; is always writable RAM.
;
; UIFA name block convention (from src/sam_io.inc / M0's stub.asm):
;   1 byte   type     (19 = code file, FT_CODE)
;   10 bytes name     space-padded to 10 chars
;   4 bytes  ext      space-padded to 4 chars (unused on SAM)
; samfile v3 addFile() pads the name field the same way
; (samfile.go:948: `name + "          "` truncated to 10 bytes).
;
; IMPORTANT: SAMDOS hook calls (RST 8 + DEFB code) clobber a few caller
; registers via the ROM PTDOS dispatcher and the SAMDOS rfhk epilogue:
;   - B is overwritten with the previous LMPR value by PTDOS step 1
;     (rom-v3.0_annotated-disassembly.txt:12944-12978).  SAMDOS does not
;     restore B from hkbc (`samdos/src/d.s:284-289 bcr` is the normal
;     return path; it only restores the border colour).
;   - E is forced to 0 by `rfhk` (`samdos/src/b.s:475-479`), which does
;     `xor a; ld e, a` before tail-calling bcr.
;   - IX is left at `dchan` after any hook that calls `gtixd` or `fdhr`
;     (HLOAD, HGTHD, HGFLE, HSAVE, ...).  The dispatcher saves caller's
;     IX to `(svhdr)` at `b.s:440` but never restores it.
; Consequence: do NOT use B as a loop counter, or E as a low byte of a
; pointer, across any RST 8 hook.  Re-load IX if you need it elsewhere.


; -----------------------------------------------------------------------
; Constants
; -----------------------------------------------------------------------

ENCTAB_BUF:     equ     &9000          ; load enctab.enc body here (section C,
                                       ; inside HLOAD's required &8000-&BFFF range)
STACK_TOP:      equ     &C100          ; SP before any call (grows down into section D)
ENCTAB_LEN:     equ     3329           ; current enctab.enc body size; build-time
                                       ; constant (matches build/enctab.enc;
                                       ; 135 forms = 87 manual + 48 MRA-derived)


; -----------------------------------------------------------------------
; load_enctab — load enctab.enc into ENCTAB_BUF via HGTHD+HLOAD, validate
;               header.
;
; Input:  none
; Output: HL = ENCTAB_BUF (pointer to validated enctab data)
; On mismatch: jp fail (red border + spin → 30s timeout → exit 124)
; Clobbers: A, BC, DE, HL, IX (everything except SP).
; -----------------------------------------------------------------------
load_enctab:
; SP must be set by the caller (start: in assembler.asm) before calling here.
; Precondition: SP = STACK_TOP (&C100), set in assembler.asm start:

; -- HGTHD (hook 129): find file, populate SAMDOS's internal svde --------
; HGTHD body: rxhed → ckdrv → gtixd → gtfle → ld de,(difa+35); set 7,d; ...
;             → txhed → ret  (samdos/src/h.s:59-67).
; The crucial side effect is `gtfle` (called inside HGTHD) doing
; `ld (svde), de` (samdos/src/c.s:1486) — this writes the file's first
; track+sector into dchan+2, which the subsequent HLOAD's `dschd`
; consumes (samdos/src/h.s:74-90).  Without HGTHD first, HLOAD reads
; from a stale svde and either fails or loads garbage.
                ld      hl, name_enctab
                call    fill_uifa      ; populate UIFA + IX = &4B00
                rst     8
                defb    HOOK_HGTHD     ; 129 — longjmps on "file not found"

; -- HLOAD (hook 130): copy file body to memory --------------------------
; Calling convention (samdos/src/b.s:439-470 dispatcher does `exx` then
; saves the now-swapped HL'/DE'/BC' to (hkhl)/(hkde)/(hkbc) — meaning the
; CALLER'S MAIN register set is what gets saved, not the alternates).
; HLOAD's dschd then reads them back via `ld hl,(hkhl)` etc.
; (samdos/src/h.s:74-90):
;   HL = destination address (Tech Manual: 8000-BFFF — satisfied by
;        ENCTAB_BUF=&9000; see header comment for the wrap-fix details).
;   C  = number of full 16K pages used by the file (BC: only C matters,
;        B is discarded).
;   DE = length modulo 16K; HLOAD's dschd does `res 7, d` to cap at <16K.
;   IX = UIFA (already set by fill_uifa above).
; For enctab.enc (3329 bytes < 16K): C=0, DE=3329 (=0x0d01).
                ld      hl, ENCTAB_BUF
                ld      bc, 0          ; B=0 (don't care), C=0 (0 full 16K pages)
                ld      de, ENCTAB_LEN ; length modulo 16K (whole file fits)
                rst     8
                defb    HOOK_HLOAD     ; 130 — longjmps on read error

; -- Validate magic "ENC1" -----------------------------------------------
; pyz80 character literals use double-quoted single chars: "E" = ord('E') = 69.
; Citation: pyz80 source (pyz80.py:436: char literal substitution via double quotes).
                ld      hl, ENCTAB_BUF
                ld      a, (hl)
                cp      "E"
                jp      nz, fail
                inc     hl
                ld      a, (hl)
                cp      "N"
                jp      nz, fail
                inc     hl
                ld      a, (hl)
                cp      "C"
                jp      nz, fail
                inc     hl
                ld      a, (hl)
                cp      "1"
                jp      nz, fail

; -- Validate version = 1 ------------------------------------------------
; Version is u16 LE at bytes 4-5 of the file (ENCTAB_BUF+4, ENCTAB_BUF+5).
; Expect version_lo = 1, version_hi = 0.
; Citation: docs/specs/2026-05-24-m2-encoder-tables-design.md §2.
                inc     hl             ; hl = ENCTAB_BUF + 4 (version_lo)
                ld      a, (hl)
                cp      1
                jp      nz, fail
                inc     hl             ; hl = ENCTAB_BUF + 5 (version_hi)
                ld      a, (hl)
                or      a              ; expect 0
                jp      nz, fail

; -- Header validated.  Return HL = ENCTAB_BUF. -------------------------
                ld      hl, ENCTAB_BUF
                ret


; -----------------------------------------------------------------------
; UIFA name block for "enctab.enc"
; -----------------------------------------------------------------------
; Type 19 = FT_CODE.  Name is "enctab.enc" — exactly 10 characters so no
; trailing spaces needed.  Extension field is 4 spaces (unused on SAM).
; Byte layout: [type:1][name:10][ext:4] = 15 bytes total, as required by
; fill_uifa in src/sam_io.inc.
name_enctab:    defb    19
                defm    "enctab.enc"   ; 10 chars
                defm    "    "         ; 4-char ext (4 spaces)
