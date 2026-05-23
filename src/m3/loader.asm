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
;   &C000-&C0FF  stack (SP = &C100, grows down)
;   &C100+       enctab buffer (holds entire enctab.enc file body)
;
; &C000–&FFFF is section D, HMPR+1 page — always writable RAM.  enctab.enc
; is 1090 bytes today; section D gives ~16 KB headroom for any future M3
; table.  HLOAD's target HL=&C100 is in section D, which violates the Tech
; Manual's "HL must be &8000-&BFFF" rule — but the rule only matters when
; the auto-wrap-fix in SAMDOS's `ctas` (`samdos/src/c.s:347-369`) fires,
; which happens only when a track step occurs mid-load.  enctab.enc lives
; entirely on track 6 (T6S5..T6S7 per build-m3-disk layout), so no track
; step happens during the load and HL stays in section D throughout.
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

ENCTAB_BUF:     equ     &C100          ; load enctab.enc body here
STACK_TOP:      equ     &C100          ; SP before any call (grows down)
ENCTAB_LEN:     equ     1090           ; current enctab.enc body size; build-time
                                       ; constant (matches build/enctab.enc)


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
;   HL = destination address (Tech Manual: 8000-BFFF; see header comment
;        for why &C100 works for enctab-sized loads).
;   C  = number of full 16K pages used by the file (BC: only C matters,
;        B is discarded).
;   DE = length modulo 16K; HLOAD's dschd does `res 7, d` to cap at <16K.
;   IX = UIFA (already set by fill_uifa above).
; For enctab.enc (1090 bytes < 16K): C=0, DE=1090 (=0x0442).
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
