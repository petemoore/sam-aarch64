; disasm.asm — NOP-only disassembler stub on physical page 15.
;
; This file is assembled STANDALONE (org &8000) into build/disasm.bin
; and HLOAD'd into physical page 15 at boot by
; src/loader.asm::load_page15_payload.  It is a PRODUCTION feature —
; the disassembler is needed by every build once strand-B PR-3+ lands.
;
; This stub handles two cases:
;   - word == 0xD503201F (NOP): writes "nop\0" to DISASM_COMM_MNEM
;     and "\0" to DISASM_COMM_OPS.
;   - any other word: writes ".inst\0" to DISASM_COMM_MNEM and the
;     8-hex-digit word (lowercase, "0x" prefix) to DISASM_COMM_OPS.
;
; Calling convention (paged_call ABI; see src/trampoline.asm §ABI):
;   paged_call clobbers A, HL, DE, F; preserves BC, IX, IY.
;   BC:IX carry the 32-bit word to decode (BC = high word, IX = low
;   word), consistent with the convention used by the full decoder.
;   On return, DISASM_COMM_MNEM and DISASM_COMM_OPS in section B are
;   populated.
;
; Execution environment:
;   This code runs with HMPR=15 (section C = page 15, section D = page
;   16).  Section B (LMPR-stable) remains accessible at &4000-&7FFF;
;   the comm buffer at DISASM_COMM_MNEM (&7E99) is readable and
;   writable throughout.  Section D (&C000-&FFFF) maps to page 16 —
;   do not access section-D addresses (OPVAL_ARRAY, scratch, etc.)
;   from within this routine.
;
; Output format: lowercase hex matching binutils objdump conventions
; (tools/aarch64dec/ is the Go-side authority).
;
; See docs/notes/2026-06-07-disassembler-page-placement.md for the
; full design rationale and page-placement decision.

; Comm-buffer addresses — MUST match the equ definitions in
; src/trampoline.asm exactly (this file is assembled standalone, so
; it carries its own copies; trampoline.asm is the canonical source).
DISASM_COMM_MNEM:   equ     &7E99   ; 12 bytes — mnemonic (null-terminated)
DISASM_COMM_OPS:    equ     &7EA5   ; 24 bytes — operands (null-terminated)

; NOP encoding (D503201F LE: bytes 1F, 20, 03, D5).
; paged_call preserves BC (= high word 0x0000D503) and IX (= low word
; 0x201F), so we check both.
NOP_HI:     equ     &D503   ; BC = high 16 bits (big end of the 32-bit word)
NOP_LO:     equ     &201F   ; IX = low  16 bits

                org     &8000

; -----------------------------------------------------------------------
; disasm_entry — entry point, called via paged_call from section B.
;
; Input:  BC = high 16 bits of the 32-bit aarch64 word (big end)
;         IX = low  16 bits of the 32-bit aarch64 word (little end)
; Output: DISASM_COMM_MNEM and DISASM_COMM_OPS populated.
; Clobbers: A, HL, DE, F (paged_call contract).
; Preserves: BC, IX, IY (paged_call contract).
; -----------------------------------------------------------------------
disasm_entry:

; Check BC against NOP high word.
                ld      hl, NOP_HI
                ld      a, b
                cp      h
                jr      nz, disasm_inst
                ld      a, c
                cp      l
                jr      nz, disasm_inst

; Check IX against NOP low word.  IX is a 16-bit register; test each
; byte via IX+0 (low byte) and IX+1 (high byte).  NOP_LO = &201F so
; IX+0 = &1F (low) and IX+1 = &20 (high).
                ld      a, (ix + 0)         ; low byte of IX
                cp      NOP_LO & &FF        ; &1F
                jr      nz, disasm_inst
                ld      a, (ix + 1)         ; high byte of IX
                cp      (NOP_LO >> 8) & &FF ; &20
                jr      nz, disasm_inst

; -----------------------------------------------------------------------
; NOP path: write "nop\0" to DISASM_COMM_MNEM, "\0" to DISASM_COMM_OPS.
; -----------------------------------------------------------------------
disasm_nop:
                ld      hl, DISASM_COMM_MNEM
                ld      (hl), "n"
                inc     hl
                ld      (hl), "o"
                inc     hl
                ld      (hl), "p"
                inc     hl
                ld      (hl), 0             ; null terminator
                ld      hl, DISASM_COMM_OPS
                ld      (hl), 0             ; empty operands
                ret

; -----------------------------------------------------------------------
; .inst path: write ".inst\0" to DISASM_COMM_MNEM and the 8-digit
; lowercase hex word (with "0x" prefix) to DISASM_COMM_OPS.
;
; The 32-bit word is BC:IX where BC is the high 16 bits and IX is
; the low 16 bits.  The aarch64 word in memory is little-endian, so
; the on-disk / in-flight order is: byte0=lo(IX), byte1=hi(IX),
; byte2=lo(BC), byte3=hi(BC).  For the "0xXXXXXXXX" display string
; the digits run from the most-significant nibble to the least —
; that is: hi(BC) hi_nib, hi(BC) lo_nib, lo(BC) hi_nib, lo(BC) lo_nib,
; hi(IX) hi_nib, hi(IX) lo_nib, lo(IX) hi_nib, lo(IX) lo_nib.
; -----------------------------------------------------------------------
disasm_inst:
; Write ".inst\0" to DISASM_COMM_MNEM.
                ld      hl, DISASM_COMM_MNEM
                ld      (hl), "."
                inc     hl
                ld      (hl), "i"
                inc     hl
                ld      (hl), "n"
                inc     hl
                ld      (hl), "s"
                inc     hl
                ld      (hl), "t"
                inc     hl
                ld      (hl), 0

; Write "0x" prefix to DISASM_COMM_OPS.
                ld      hl, DISASM_COMM_OPS
                ld      (hl), "0"
                inc     hl
                ld      (hl), "x"
                inc     hl

; Emit B (high byte of BC = bits 31..24).
                ld      a, b
                call    disasm_emit_hex_byte

; Emit C (low byte of BC = bits 23..16).
                ld      a, c
                call    disasm_emit_hex_byte

; Emit IX high byte (bits 15..8).
                ld      a, (ix + 1)
                call    disasm_emit_hex_byte

; Emit IX low byte (bits 7..0).
                ld      a, (ix + 0)
                call    disasm_emit_hex_byte

; Null-terminate the operands string.
                ld      (hl), 0
                ret


; -----------------------------------------------------------------------
; disasm_emit_hex_byte — emit A as two lowercase ASCII hex digits,
; writing to (HL) and (HL+1), advancing HL by 2.
;
; Input:  A = byte to encode, HL = destination pointer.
; Output: two ASCII hex chars written; HL advanced by 2.
; Clobbers: A, F.  Preserves BC, DE, IX.
; -----------------------------------------------------------------------
disasm_emit_hex_byte:
                push    af              ; save original byte
                rra
                rra
                rra
                rra                     ; high nibble into low 4 bits
                call    disasm_emit_hex_nibble
                pop     af              ; restore original byte
                ; fall through to emit low nibble
disasm_emit_hex_nibble:
                and     &0f             ; isolate low nibble
                add     a, &30          ; '0'
                cp      &3a             ; '0' + 10
                jr      c, disasm_hex_emit
                add     a, &27          ; 'a' - '0' - 10 (makes 'a'-'f')
disasm_hex_emit:
                ld      (hl), a
                inc     hl
                ret
