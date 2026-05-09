; M0 stub assembler — opens IN, discards its contents, writes 4 NOP bytes to OUT.
;
; The 4 bytes written are the little-endian aarch64 encoding of NOP:
;   0xd503201f → bytes 1f 20 03 d5  (low byte first).
;
; Output mechanism — HSAVE (hook 132), not HOFLE/SBYT/CFSM:
;   The streaming-byte trio is broken when called externally via RST 8
;   in canonical SAMDOS 2: their bodies never reset IX to `dchan`, so
;   `ofsm` writes 256 bytes through (caller_ix + ffsa..) which lands
;   inside SAMDOS's own code in section B, corrupting the live image.
;   HSAVE writes the whole file in one call AND calls `gtixd` at
;   `h.s:145` first, so it works correctly externally. We pre-populate
;   UIFA bytes 31-36 with source page (from HMPR), source address
;   (in 8000-BFFF form, which our payload already is given org &8000),
;   pages count, and length-mod-16K. Full citations:
;   `docs/notes/sam-stub-audit.md` and `docs/notes/archive/2026-05-10-handoff.md`.
;
; SAMDOS calling convention notes that still apply:
;   - IX must point to UIFA at &4B00 before each RST 8 hook call;
;     fill_uifa (in sam_io.inc) loads IX and populates bytes 0-14.
;   - Most hooks longjmp to BASIC on error and do not return. HSAVE
;     also longjmps on disk-full / write-error — fail: below is dead
;     code under success and kept only as a defensive halt.
;   - SAMDOS has no explicit close-for-read; close_input is a no-op.
;   - LBYT longjmps on EOF; we skip reading IN entirely. HGFLE opens
;     it (which is the only externally-correct streaming hook — gtfle
;     calls fdhr which does `ld ix, dchan`) and HSAVE then independently
;     handles the OUT side.
;
; Note: pyz80 does not support the END directive. Assembly ends at EOF.
; The org directive sets the load address; the entry point is the first byte.

                org     &8000

; Jump table at the entry point: CALL 32768 lands on the first byte (&8000).
; (Note: SAMDOS lives at logical &8000-&BFFF when its page is mapped via HMPR.
;  During RST 8 hooks ROM pages SAMDOS into section B (&4000-&7FFF), so our
;  &8000-resident stub is shadowed by HMPR-mapped SAMDOS data, not SAMDOS code.
;  This matches Defender Compilation's `CALL 32768` convention. If this
;  collides at hook time we'll see it as a separate failure mode; for now the
;  point is to test the CLEAR WKEND-RAMTOP fix.)
; The subroutines from sam_io.inc follow immediately; start: is after them.
                jp      start

                include "sam_io.inc"

; -----------------------------------------------------------------------
; Main program — entry via jp from &8000.
; -----------------------------------------------------------------------

start:          di                     ; one-shot batch program; no interrupts needed

; -- open IN for reading -----------------------------------------------
; HERAZ (hook 166) longjmps if the file is absent, so we can't call it
; speculatively to erase a pre-existing OUT.  The test harness builds a
; fresh disk on every run, so OUT never pre-exists; this is safe for M0.
                ld      hl, name_IN
                call    fill_uifa      ; populate UIFA + set IX = &4B00
                call    open_input     ; RST 8 / DEFB 158 — longjmps on error

; -- skip reading IN (LBYT longjmps on EOF; we don't know the file length) --
; HGFLE and HOFLE use a single DOS channel but independently.  HOFLE will
; reset the channel state; abandoning the read is safe for M0.

; -- write OUT via HSAVE (hook 132, whole-file write) ---------------------
; HSAVE pulls source addr from UIFA bytes 32-33 and length from 35-36.
; It writes UIFA byte 31 (& 0x1f) to HMPR before the body write, so we
; feed back our current HMPR — the page our payload (and we) live in.
                ld      hl, name_OUT
                call    fill_uifa      ; UIFA bytes 0-14; pads 15-47 with FF; IX = &4B00
                in      a, (251)       ; HMPR — current section-C physical page
                and     &1f            ; 5-bit page number
                ld      (UIFA + 31), a ; source page for HSAVE
                ld      hl, payload    ; source address (already in &8000-BFFF form)
                ld      (UIFA + 32), hl
                xor     a
                ld      (UIFA + 34), a ; pages = 0 (length < 16K)
                ld      hl, 4
                ld      (UIFA + 35), hl ; length-mod-16K = 4
                rst     8
                defb    HOOK_HSAVE     ; 132 — writes header, body, dir entry; longjmps on error

; -- magic exit signal -----------------------------------------------------
; The patched SimCoupé's `-exitonhalt 1` flag has two exit mechanisms,
; both needed for full cross-platform coverage:
;   1. OUT (&DEAD), &C0 — detected by sam_cpu::on_output. Works reliably
;      under linux/gcc; CRTP dispatch silently fails on Apple/clang, where
;      on_output is never called for any port write.
;   2. HALT with IFF1=0 — detected by sam_cpu::on_halt. The reverse:
;      reliably fires on Apple/clang, harmless on linux/gcc (the OUT
;      mechanism quits first). The IFF1=0 condition matters because
;      SAMDOS's RST 8 dispatcher (ROM `PTDOS`) does `EI` inside the
;      hook window — our initial `di` at `start:` has been undone by
;      the time we get here, so we must `di` again before halting.
                ld      bc, &dead
                ld      a, &c0
                out     (c), a         ; primary: linux on_output magic port
                di                     ; secondary: re-disable interrupts so
                halt                   ; on_halt's !iff1 check fires on macOS

fail:           ld      a, &02         ; red border = error indicator for debug
                out     (&fe), a
                halt

; -----------------------------------------------------------------------
; Data
; -----------------------------------------------------------------------

; 4-byte aarch64 NOP (little-endian): low byte first.
payload:        defb    &1f, &20, &03, &d5

; UIFA name blocks: 1 byte type + 10-char space-padded name + 4-char ext.
; Type 19 = code file (matches samfile add -c used by build-disk.sh).
name_IN:        defb    19
                defm    "IN        "   ; 10 chars (I, N, 8 spaces)
                defm    "    "         ; 4-char ext (4 spaces)

name_OUT:       defb    19
                defm    "OUT       "   ; 10 chars (O, U, T, 7 spaces)
                defm    "    "         ; 4-char ext (4 spaces)
