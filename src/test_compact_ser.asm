; test_compact_ser.asm — flat-memory harness for the compact v2 .tbn
; serializer (i48c-b8c) and the compact_emit INSN_RUN frame packer's
; boundary-split coverage (the PR 827 review follow-up).
;
; TWO test surfaces, one binary (both verified by
; tools/netboot-oracle/z80/compact_serialize_test.go):
;
;   * compact_serialize (src/compact_serialize.asm): the format.WriteFile
;     port. Every input is host-written into low RAM in the b8b walk's
;     buffer formats (label/local rows, sidecar rows, global ids, the
;     record stream) plus the interned name list; the produced .tbn bytes
;     are compared byte-for-byte against assemble.CompactTBNBytes.
;
;   * cemit_init / cemit_flush_inst (src/compact_emit.asm): the INSN_RUN
;     frame packer, driven over host-written PRE-CLASSIFIED element runs.
;     The packer needs no ENCTAB — only classification does, and
;     compact_inst is stubbed to a trap below — which is what makes the
;     >253-element mode-0 split and the >1016-byte mode-1 payload split
;     exercisable here: the paged section-D payload's 256-byte element
;     window cannot hold a 254-element run (1270 bytes), so the flat
;     harness repoints the window at low RAM via CEMIT_BUFS_EXTERNAL.
;
; Memory map (flat harness: code at &8000+, harness stack &6FFE down,
; HALT trap &7000 — everything below &6E00 is free). Mirrored as Go
; constants in compact_serialize_test.go; a drift is a silent corruption,
; the same contract as the b8a/b8b table addresses.
;
;   &0800-&1DFF  CEMIT_ELEMS       element window (5.5 KB)
;   &1E00-&2DFF  cemit output      window passed to cemit_init by the test
;   &3000-&32FF  SERBUF_LABELROWS  128 rows x 6 B
;   &3300-&357F  SERBUF_LOCALROWS  128 rows x 5 B
;   &3600-&36FF  SERBUF_GLOBALS    128 ids x 2 B
;   &3700-&3EFF  SERBUF_NAMES      [count:2][len:1][bytes]... (2 KB)
;   &3F00-&46FF  SERBUF_SIDECAR    b8b sidecar row format (2 KB)
;   &4700-&51FF  SERBUF_RECS       compacted record stream (2816 B)
;   &5200-&6DFF  SERBUF_OUT        the produced .tbn (7 KB)
;
; Fail plumbing follows the b8a harness pattern: fail/fail_with_tag park
; the tag at ser_fail_tag and halt at fail_halt; the host asserts a clean
; (non-fail_halt) return and reads the tag on failure.
; -----------------------------------------------------------------------

                org     &8000

                include "tbn_constants.inc"

; Element window override (see the CEMIT_BUFS_EXTERNAL note in
; src/compact_emit.asm): the boundary fixtures need room for >253
; elements, which the default 256-byte section-D window cannot hold.
CEMIT_BUFS_EXTERNAL:    equ     1
CEMIT_ELEMS:            equ     &0800
CEMIT_ELEMS_END:        equ     &1E00

; Serializer input/output buffer homes (documentation + the Go mirror;
; the serializer itself reads only its ser_* parameter cells, which the
; host points at these addresses). The SERBUF_ prefix keeps them clear of
; the ser_* parameter-cell labels — pyz80 symbols are case-insensitive.
SERBUF_LABELROWS:       equ     &3000
SERBUF_LOCALROWS:       equ     &3300
SERBUF_GLOBALS:         equ     &3600
SERBUF_NAMES:           equ     &3700
SERBUF_SIDECAR:         equ     &3F00
SERBUF_RECS:            equ     &4700
SERBUF_OUT:             equ     &5200
SERBUF_OUT_END:         equ     &6E00

                include "compact_serialize.asm"
                include "compact_emit.asm"

; -----------------------------------------------------------------------
; Stubs satisfying compact_emit.asm's adapter links. cemit_add_inst is
; never driven in this harness (the tests feed pre-classified elements
; straight into CEMIT_ELEMS and call cemit_flush_inst); compact_inst is
; ENCTAB-coupled and must never run flat, so reaching it traps loudly.
; The ovl_* cells exist only to resolve cemit_add_inst's assembly.
; -----------------------------------------------------------------------
compact_inst:
                ld      a, &e1
                jp      fail_with_tag           ; tag e1: ENCTAB-coupled stub reached

ovl_is_literal: defb    0
ovl_base:       defb    0, 0, 0, 0
OVL_SLOT:       defb    0
OVL_EXPR_PTR:   defw    0
OVL_EXPR_LEN:   defw    0
LAST_FAIL_PC:   defw    0                       ; fail diagnostics (host-readable)

; -----------------------------------------------------------------------
; fail / fail_with_tag — the b8a harness fail trap: park the tag, halt.
; -----------------------------------------------------------------------
fail:
                xor     a
                ld      (ser_fail_tag), a
fail_halt:
                halt
                jr      fail_halt

fail_with_tag:
                ld      (ser_fail_tag), a
                jr      fail_halt

ser_fail_tag:   defb    0
