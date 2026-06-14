; asmparse.asm — aarch64 assembler-source parser, i48c Brick B2 (text→records).
;
; SAM-side Z80 port of the Go authority's parser:
;   tools/sam-aarch64/frontend/parser.go  (Parse / parseInst / parseOperand /
;   matchReg) feeding tools/sam-aarch64-format (Record / OperandWriter).
;
; The parser is the stage after the lexer (src/asmlex.asm): it turns the token
; stream into the in-memory symbolic record IR (format.Record) that the
; assembler consumes — the editor input path's back half. Brick B2 builds it up
; in sub-bricks:
;   B2a (this file's first routine): mnemonic_lookup — turn a lexed mnemonic
;       identifier's bytes back into its on-disk mnemonic ID. This is the
;       runtime-data counterpart of mnemonic_ids.inc's assemble-time equates.
;   B2b: the generic simple-instruction parse (reg/reg/reg) → INST record.
;   B2c: the #imm operand (reg/reg/#imm, single-literal immediate).
;
; PROVENANCE: algorithmic port of parser.go; the name→id table layout is
; generated from the Go authority (tables-gen -mnemonic-names-inc →
; src/mnemonic_names.inc). VERIFICATION: tools/netboot-oracle/z80/asmparse_test.go
; drives mnemonic_lookup under koron-go/z80 and compares the returned ID against
; frontend/format.MnemonicID for every name in MnemonicTable, plus a batch of
; non-mnemonics (asserting not-found).

                if defined(ASMPARSE_STANDALONE)
                org     &8000
                endif

; ===========================================================================
; mnemonic_lookup — map a mnemonic name's bytes to its on-disk mnemonic ID.
; Port of format.MnemonicID (a name→index map lookup); the Z80 walks the
; generated MNEM_NAMES table linearly, since the table is small (~103 entries)
; and the parser looks up one mnemonic per source line.
;
; Entry: HL = pointer to the candidate name bytes; C = name length (B ignored —
;        mnemonic names are 1..255 bytes; the lexer never produces a longer
;        single identifier than the buffer holds).
; Exit:  A = 1 and HL = mnemonic ID (the table index)   if the name matches;
;        A = 0 (HL undefined)                            if not found.
;        BC/DE clobbered.
; ===========================================================================
mnemonic_lookup:
                ld      (ML_PTR), hl        ; save candidate pointer
                ld      a, c
                ld      (ML_LEN), a         ; save candidate length
                ld      de, MNEM_NAMES
                ld      hl, 0               ; HL = running index = candidate id
ml_loop:
                ld      a, (de)             ; entry length (0 = sentinel)
                or      a
                jr      z, ml_notfound
                ld      b, a                ; B = entry length
                ld      a, (ML_LEN)
                cp      b
                jr      nz, ml_next         ; length mismatch -> skip entry
                ; Lengths match: compare B bytes of entry name vs candidate.
                push    hl                  ; save index
                push    de                  ; save entry pointer (at length byte)
                inc     de                  ; DE -> entry name bytes
                ld      hl, (ML_PTR)        ; HL -> candidate bytes
ml_cmp:
                ld      a, (de)
                cp      (hl)
                jr      nz, ml_cmp_fail
                inc     de
                inc     hl
                djnz    ml_cmp
                ; All bytes matched.
                pop     de                  ; discard saved entry pointer
                pop     hl                  ; HL = index = id
                ld      a, 1
                ret
ml_cmp_fail:
                pop     de                  ; restore entry pointer (length byte)
                pop     hl                  ; restore index
ml_next:
                ; Advance DE past this record: skip the length byte + name bytes.
                ld      a, (de)             ; entry length
                inc     de                  ; past the length byte
                add     a, e
                ld      e, a
                jr      nc, ml_next_nc
                inc     d
ml_next_nc:
                inc     hl                  ; index++
                jr      ml_loop
ml_notfound:
                xor     a                   ; A = 0 (not found)
                ret

; ===========================================================================
; Generated name→id table (do not edit; regen with `make tables`).
; ===========================================================================
                include "mnemonic_names.inc"

; ===========================================================================
; Working storage
; ===========================================================================
ML_PTR:         defs 2          ; mnemonic_lookup: saved candidate pointer
ML_LEN:         defs 1          ; mnemonic_lookup: saved candidate length

; ===========================================================================
; Public I/O buffer
; ===========================================================================
AP_NAMEBUF:     defs 32         ; scratch the harness fills with a candidate name
