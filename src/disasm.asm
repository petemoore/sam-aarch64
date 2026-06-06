; disasm.asm — aarch64 disassembler on physical page 15.
;
; This file is assembled STANDALONE (org &8000) into build/disasm.bin
; and HLOAD'd into physical page 15 at boot by
; src/loader.asm::load_page15_payload.  It is a PRODUCTION feature —
; the disassembler is needed by every build once strand-B PR-4+ lands.
;
; It is the Z80 port of the Go disassembler tools/aarch64dec/ (the
; encoding authority).  Decoding is grown family-by-family, mirroring
; the dispatch order of aarch64dec.DecodeAt (disasm.go): each family
; not yet ported falls through to the `.inst 0xNNNNNNNN` default, which
; is always a correct (if unrefined) rendering — never wrong.
;
; Correctness is driven test-first: tools/z80-test-harness-go runs this
; binary in a Z80 emulator and compares its output, word-by-word, against
; aarch64dec.DecodeAt over the full release.img corpus (the same oracle
; that the Go round-trip tests use).  See disasm_oracle_test.go.
;
; Families currently ported:
;   - NOP   (0xD503201F → "nop")            [special-cased ahead of UDF]
;   - UDF   (bits[31:16]==0 → "udf #<dec>")  [aarch64dec disasm.go:77]
;   - move-wide (movz/movn/movk + mov #imm) [aliases.go:117 + :737]
;   - default: ".inst 0xNNNNNNNN"            [aarch64dec asm.go:75]
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
;   from within this routine.  Working scratch lives in *this* page
;   (section C, below the self-test), so it is always accessible both
;   under paged_call and in the standalone oracle emulator.
;
; Output format: lowercase hex matching binutils objdump conventions
; (tools/aarch64dec/ is the Go-side authority).
;
; See docs/notes/2026-06-07-disassembler-page-placement.md for the
; full design rationale and page-placement decision.

; Comm-buffer addresses and self-test entry — shared with
; src/trampoline.asm via the include below (single source of truth).
                include "disasm_comm.inc"

; NOP encoding (D503201F LE: bytes 1F, 20, 03, D5).
; paged_call preserves BC (= high word 0x0000D503) and IX (= low word
; 0x201F), so we check both.
NOP_HI:     equ     &D503   ; BC = high 16 bits (big end of the 32-bit word)
NOP_LO:     equ     &201F   ; IX = low  16 bits

                org     &8000

; -----------------------------------------------------------------------
; Fixed-address jump table (page top).  paged_call targets these two
; addresses; the bodies they jump to grow freely after the table, so the
; binary is never padded.  The addresses MUST stay in lock-step with
; DISASM_ENTRY (src/trampoline.asm) and DISASM_SELF_TEST_ENTRY
; (src/disasm_comm.inc), which src/assembler.asm names at its own build.
; -----------------------------------------------------------------------
                jp      disasm_entry            ; &8000  DISASM_ENTRY
                jp      run_disasm_self_test    ; &8003  DISASM_SELF_TEST_ENTRY

; -----------------------------------------------------------------------
; disasm_entry — decoder entry, reached via the &8000 jump above.
;
; Input:  BC = high 16 bits of the 32-bit aarch64 word (bits 31..16)
;         IX = low  16 bits of the 32-bit aarch64 word (bits 15..0)
; Output: DISASM_COMM_MNEM and DISASM_COMM_OPS populated.
; Clobbers: A, HL, DE, F (paged_call contract).
; Preserves: BC, IX, IY (paged_call contract).
;
; The 32-bit word is held throughout in the four registers B, C, D, E:
;   B = bits 31..24   C = bits 23..16   D = bits 15..8   E = bits 7..0
; (D, E are the IX bytes, extracted via push/pop below.)  This big-end
; register layout makes UDF's "top 16 bits zero" test a simple B|C test.
; -----------------------------------------------------------------------
disasm_entry:

; Extract IX bytes into DE via push/pop — (ix+N) is an indexed memory
; read (address IX+N), not a register-byte access.  push/pop is the
; standard Z80 idiom.  D = high byte of lo-word (bits 15..8),
; E = low byte (bits 7..0).  IX itself is unchanged.
                push    ix
                pop     de

; -----------------------------------------------------------------------
; Dispatch chain — mirrors aarch64dec.DecodeAt (tools/aarch64dec/disasm.go)
; family order.  Each `try_*` decoder either handles the word and returns,
; or falls through to the next.  The chain ends at disasm_inst (.inst).
;
; In DecodeAt the load/store (mem) family is decoded FIRST, ahead of
; everything else.  Its encoding space is disjoint from NOP/UDF/move-wide,
; so trying it first here is order-faithful and harmless.  disasm_try_mem
; either handles the word (ret) or falls through to disasm_not_mem with
; B,C,D,E intact for the later families.
; -----------------------------------------------------------------------
                jp      disasm_try_mem
disasm_not_mem:

; -----------------------------------------------------------------------
; NOP is special-cased first (it is in AllForms on the Go side, decoded by
; the form-walk; we have no form-walk yet, so we recognise it directly).
; NOP and UDF are disjoint encoding spaces, so the ordering is harmless.
; -----------------------------------------------------------------------

; --- NOP (0xD503201F) -------------------------------------------------
                ld      a, b
                cp      NOP_HI >> 8         ; &D5
                jr      nz, disasm_not_nop
                ld      a, c
                cp      NOP_HI & &FF        ; &03
                jr      nz, disasm_not_nop
                ld      a, d
                cp      (NOP_LO >> 8) & &FF ; &20
                jr      nz, disasm_not_nop
                ld      a, e
                cp      NOP_LO & &FF        ; &1F
                jr      nz, disasm_not_nop
                jr      disasm_nop
disasm_not_nop:

; --- UDF (bits[31:16] == 0) -------------------------------------------
; aarch64dec disasm.go:77 — `if word>>16 == 0 { "udf", "#<dec imm16>" }`.
; Top 16 bits are B and C; both zero ⇒ UDF.  imm16 = DE.
                ld      a, b
                or      c
                jr      nz, disasm_not_udf
                jp      disasm_udf
disasm_not_udf:

; --- (further families ported here, in DecodeAt order) ----------------

; --- move wide (movz / movn / movk and the `mov #imm` alias) ----------
                jp      disasm_try_movewide
disasm_not_movewide:

; --- default: .inst 0xNNNNNNNN ----------------------------------------
                jp      disasm_inst


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
; UDF path: write "udf\0" to DISASM_COMM_MNEM and "#<decimal imm16>\0"
; to DISASM_COMM_OPS.  imm16 is in DE (bits 15..0).  objdump renders the
; immediate in DECIMAL (aarch64dec disasm.go:78, fmt "#%d"), unlike the
; hex used everywhere else.
; -----------------------------------------------------------------------
disasm_udf:
                ld      hl, DISASM_COMM_MNEM
                ld      (hl), "u"
                inc     hl
                ld      (hl), "d"
                inc     hl
                ld      (hl), "f"
                inc     hl
                ld      (hl), 0
                ld      hl, DISASM_COMM_OPS
                ld      (hl), "#"
                inc     hl
                call    disasm_emit_dec16   ; DE → decimal at (HL), HL advanced
                ld      (hl), 0
                ret


; -----------------------------------------------------------------------
; .inst path: write ".inst\0" to DISASM_COMM_MNEM and the 8-digit
; lowercase hex word (with "0x" prefix) to DISASM_COMM_OPS.
;
; The 32-bit word is BC:IX where BC is the high 16 bits and IX is
; the low 16 bits.  For the "0xXXXXXXXX" display string the digits run
; from the most-significant nibble to the least: hi(BC), lo(BC) — i.e.
; B then C — then D then E (the IX bytes).  Matches Go's "%#08x"
; (zero-padded to 8 digits).
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

; Emit B (bits 31..24), C (23..16), D (15..8), E (7..0).
                ld      a, b
                call    disasm_emit_hex_byte
                ld      a, c
                call    disasm_emit_hex_byte
                ld      a, d
                call    disasm_emit_hex_byte
                ld      a, e
                call    disasm_emit_hex_byte

; Null-terminate the operands string.
                ld      (hl), 0
                ret


; -----------------------------------------------------------------------
; disasm_try_movewide — move-wide family decoder (movz / movn / movk and
; the `mov #imm` alias).  Z80 port of aarch64dec decodeMoveWideAlias
; (aliases.go:117) + decodeMovk (aliases.go:737).
;
; Encoding:  sf | opc(2) | 100101 | hw(2) | imm16(16) | Rd(5)
;
; Registers on entry (disasm_entry layout):
;   B = bits 31..24   C = bits 23..16   D = bits 15..8   E = bits 7..0
;
; On a match: populates DISASM_COMM_MNEM/OPS and returns.
; On a non-match: jumps to disasm_not_movewide (falls through to .inst).
;
; Derived fields are stashed in disasm_mw_* scratch (this page) so the
; B,C,D,E word stays intact for the .inst fallback.
; -----------------------------------------------------------------------
disasm_try_movewide:
; Discriminator: (B & 0x1F) == 0x12  AND  (C & 0x80) != 0.
                ld      a, b
                and     &1f
                cp      &12
                jp      nz, disasm_not_movewide
                bit     7, c
                jp      z, disasm_not_movewide

; sf = B bit 7 → store 0 or 1 in disasm_mw_sf.
                ld      a, b
                rlca                    ; bit7 → carry (and into bit0)
                and     1
                ld      (disasm_mw_sf), a

; opc = (B >> 5) & 3.
                ld      a, b
                rrca
                rrca
                rrca
                rrca
                rrca                    ; (B>>5) in low 3 bits
                and     3
                ld      (disasm_mw_opc), a

; hw = (C >> 5) & 3.
                ld      a, c
                rrca
                rrca
                rrca
                rrca
                rrca
                and     3
                ld      (disasm_mw_hw), a

; imm16 high byte = ((C & 0x1F) << 3) | (D >> 5).
                ld      a, c
                and     &1f
                add     a, a
                add     a, a
                add     a, a            ; (C & 0x1F) << 3
                ld      l, a
                ld      a, d
                rrca
                rrca
                rrca
                rrca
                rrca
                and     7               ; D >> 5
                or      l
                ld      (disasm_mw_imm_hi), a

; imm16 low byte = ((D & 0x1F) << 3) | (E >> 5).
                ld      a, d
                and     &1f
                add     a, a
                add     a, a
                add     a, a            ; (D & 0x1F) << 3
                ld      l, a
                ld      a, e
                rrca
                rrca
                rrca
                rrca
                rrca
                and     7               ; E >> 5
                or      l
                ld      (disasm_mw_imm_lo), a

; Rd = E & 0x1F.
                ld      a, e
                and     &1f
                ld      (disasm_mw_rd), a

; Width guard: sf==0 AND hw>=2 → UNDEFINED → decline to .inst.
                ld      a, (disasm_mw_sf)
                or      a
                jr      nz, disasm_mw_sf_ok
                ld      a, (disasm_mw_hw)
                cp      2
                jp      nc, disasm_not_movewide   ; hw>=2 with sf=0 → undefined
disasm_mw_sf_ok:

; Dispatch on opc: 0b00 movn, 0b10 movz, 0b11 movk, 0b01 unallocated.
; opc encoding: 0b00=movn (0), 0b10=movz (2), 0b11=movk (3), 0b01=unalloc (1).
                ld      a, (disasm_mw_opc)
                cp      1
                jp      z, disasm_not_movewide    ; opc=01 unallocated → .inst
; Past the last decline: every remaining path is a success that ret's.
; The emit code below clobbers BC and IX as scratch, but disasm_entry's
; ABI contract (paged_call) requires BC/IX/IY preserved for the caller —
; the production bytes->text loop relies on it.  Save them here and
; restore via disasm_mw_done before returning.  (The decline paths above
; never reach here, and they leave BC/IX/D/E untouched, so the .inst
; fallback still sees the original word.)  IY is already preserved (no
; move-wide code touches it; disasm_emit_dec16 save/restores it).
                push    bc
                push    ix
                cp      3                          ; opc=11 → movk
                jp      z, disasm_mw_movk
                cp      2                          ; opc=10 → movz
                jp      z, disasm_mw_movz
                jp      disasm_mw_movn             ; opc=00 → movn


; --- movz (opc=10) ----------------------------------------------------
; KEPT (render "movz") iff imm16==0 && hw!=0; else alias to "mov".
disasm_mw_movz:
                call    disasm_mw_imm16_is_zero
                jr      nz, disasm_mw_movz_alias   ; imm16 != 0 → alias
                ld      a, (disasm_mw_hw)
                or      a
                jr      z, disasm_mw_movz_alias    ; hw==0 → alias
; KEPT movz.
                ld      hl, disasm_mw_movz_txt
                call    disasm_mw_set_mnem
                jp      disasm_mw_emit_keptops
disasm_mw_movz_alias:
; val = imm16 << (hw*16), masked to width by the buffer renderer.
                call    disasm_mw_build_val        ; fills disasm_mw_val[0..7]
                jp      disasm_mw_emit_mov_alias


; --- movn (opc=00) ----------------------------------------------------
; KEPT (render "movn") iff (imm16==0 && hw!=0) || (sf==0 && imm16==0xffff);
; else alias to "mov" with val = NOT(imm16 << (hw*16)).
disasm_mw_movn:
; First predicate: imm16==0 && hw!=0.
                call    disasm_mw_imm16_is_zero
                jr      nz, disasm_mw_movn_chk2
                ld      a, (disasm_mw_hw)
                or      a
                jr      nz, disasm_mw_movn_kept    ; imm16==0 && hw!=0 → kept
disasm_mw_movn_chk2:
; Second predicate: sf==0 && imm16==0xffff.
                ld      a, (disasm_mw_sf)
                or      a
                jr      nz, disasm_mw_movn_alias   ; sf!=0 → not this predicate
                ld      a, (disasm_mw_imm_hi)
                and     a
                inc     a                           ; 0xff → 0x00 (Z if 0xff)
                jr      nz, disasm_mw_movn_alias
                ld      a, (disasm_mw_imm_lo)
                inc     a
                jr      nz, disasm_mw_movn_alias
                ; fall through: imm16==0xffff and sf==0 → kept
disasm_mw_movn_kept:
                ld      hl, disasm_mw_movn_txt
                call    disasm_mw_set_mnem
                jp      disasm_mw_emit_keptops
disasm_mw_movn_alias:
                call    disasm_mw_build_val        ; fills disasm_mw_val[0..7]
; one's complement all 8 bytes.
                ld      hl, disasm_mw_val
                ld      b, 8
disasm_mw_movn_not:
                ld      a, (hl)
                cpl
                ld      (hl), a
                inc     hl
                djnz    disasm_mw_movn_not
                jp      disasm_mw_emit_mov_alias


; --- movk (opc=11) — never aliased -----------------------------------
disasm_mw_movk:
                ld      hl, disasm_mw_movk_txt
                call    disasm_mw_set_mnem
                jp      disasm_mw_emit_keptops


; -----------------------------------------------------------------------
; disasm_mw_emit_keptops — operands for kept movz/movn/movk:
;   <reg>, #0x<hex imm16>[, lsl #<hw*16>]
; -----------------------------------------------------------------------
disasm_mw_emit_keptops:
                ld      hl, DISASM_COMM_OPS
                call    disasm_mw_emit_reg          ; HL advanced past reg
                ld      (hl), ","
                inc     hl
                ld      (hl), " "
                inc     hl
                ld      (hl), "#"
                inc     hl
                ld      (hl), "0"
                inc     hl
                ld      (hl), "x"
                inc     hl
; emit minimal-width hex of imm16 (2 bytes, big-end first via the buffer).
                ld      a, (disasm_mw_imm_lo)
                ld      (disasm_mw_val), a
                ld      a, (disasm_mw_imm_hi)
                ld      (disasm_mw_val+1), a
                ld      a, 2                        ; 2 bytes
                call    disasm_mw_emit_hexbuf
; append ", lsl #<hw*16>" when hw != 0.
                ld      a, (disasm_mw_hw)
                or      a
                jr      z, disasm_mw_keptops_done
                call    disasm_mw_emit_lsl
disasm_mw_keptops_done:
                ld      (hl), 0
                jp      disasm_mw_done


; -----------------------------------------------------------------------
; disasm_mw_emit_mov_alias — mnemonic "mov", operands:
;   <reg>, #0x<minimal-hex val>
; val is in disasm_mw_val[0..7] (little-endian); width = 8 bytes (sf=1)
; or 4 bytes (sf=0).
; -----------------------------------------------------------------------
disasm_mw_emit_mov_alias:
                ld      hl, disasm_mw_mov_txt
                call    disasm_mw_set_mnem
                ld      hl, DISASM_COMM_OPS
                call    disasm_mw_emit_reg
                ld      (hl), ","
                inc     hl
                ld      (hl), " "
                inc     hl
                ld      (hl), "#"
                inc     hl
                ld      (hl), "0"
                inc     hl
                ld      (hl), "x"
                inc     hl
                ld      a, (disasm_mw_sf)
                or      a
                ld      a, 4
                jr      z, disasm_mw_alias_width
                ld      a, 8
disasm_mw_alias_width:
                call    disasm_mw_emit_hexbuf       ; A bytes of disasm_mw_val
                ld      (hl), 0
                ; fall through to disasm_mw_done (restore BC/IX, return)

; -----------------------------------------------------------------------
; disasm_mw_done — common success epilogue: restore the BC/IX that the
; move-wide emit code clobbered (saved after the last decline) and return,
; honouring disasm_entry's "Preserves: BC, IX, IY" ABI contract.
; -----------------------------------------------------------------------
disasm_mw_done:
                pop     ix
                pop     bc
                ret


; -----------------------------------------------------------------------
; disasm_mw_build_val — fill disasm_mw_val[0..7] little-endian with
; imm16 placed at byte offsets 2*hw (low) and 2*hw+1 (high); all other
; bytes zero.  (hw*16 is a whole number of bytes → pure byte placement.)
; Clobbers A, B, HL, DE.
; -----------------------------------------------------------------------
disasm_mw_build_val:
                ld      hl, disasm_mw_val
                xor     a
                ld      b, 8
disasm_mw_bv_clear:
                ld      (hl), a
                inc     hl
                djnz    disasm_mw_bv_clear
; offset = 2*hw.
                ld      a, (disasm_mw_hw)
                add     a, a                        ; 2*hw
                ld      e, a
                ld      d, 0
                ld      hl, disasm_mw_val
                add     hl, de                      ; HL = &val[2*hw]
                ld      a, (disasm_mw_imm_lo)
                ld      (hl), a
                inc     hl
                ld      a, (disasm_mw_imm_hi)
                ld      (hl), a
                ret


; -----------------------------------------------------------------------
; disasm_mw_imm16_is_zero — Z set iff imm16 (hi:lo) is zero.
; Clobbers A, F.
; -----------------------------------------------------------------------
disasm_mw_imm16_is_zero:
                ld      a, (disasm_mw_imm_hi)
                or      a
                ret     nz
                ld      a, (disasm_mw_imm_lo)
                or      a
                ret


; -----------------------------------------------------------------------
; disasm_mw_set_mnem — copy the null-terminated mnemonic at (HL) to
; DISASM_COMM_MNEM.  Clobbers A, DE, HL.
; -----------------------------------------------------------------------
disasm_mw_set_mnem:
                ld      de, DISASM_COMM_MNEM
disasm_mw_set_mnem_loop:
                ld      a, (hl)
                ld      (de), a
                or      a
                ret     z
                inc     hl
                inc     de
                jr      disasm_mw_set_mnem_loop


; -----------------------------------------------------------------------
; disasm_mw_emit_reg — emit the destination register name to (HL),
; advancing HL.  Uses disasm_mw_rd and disasm_mw_sf:
;   Rd==31 → "xzr"/"wzr"; else prefix('x'/'w') + decimal(Rd).
; Clobbers A, DE, F (and IX/IY preserved by disasm_emit_dec16).
; -----------------------------------------------------------------------
disasm_mw_emit_reg:
                ld      a, (disasm_mw_rd)
                cp      31
                jr      z, disasm_mw_emit_zero
; prefix char.
                ld      a, (disasm_mw_sf)
                or      a
                ld      a, "w"
                jr      z, disasm_mw_emit_prefix
                ld      a, "x"
disasm_mw_emit_prefix:
                ld      (hl), a
                inc     hl
; decimal(Rd) via disasm_emit_dec16 (value in DE).
                ld      a, (disasm_mw_rd)
                ld      e, a
                ld      d, 0
                call    disasm_emit_dec16           ; preserves IX/IY
                ret
disasm_mw_emit_zero:
                ld      a, (disasm_mw_sf)
                or      a
                ld      a, "w"
                jr      z, disasm_mw_emit_zero_p
                ld      a, "x"
disasm_mw_emit_zero_p:
                ld      (hl), a
                inc     hl
                ld      (hl), "z"
                inc     hl
                ld      (hl), "r"
                inc     hl
                ret


; -----------------------------------------------------------------------
; disasm_mw_emit_lsl — append ", lsl #<hw*16>" to (HL), advancing HL.
; hw is in disasm_mw_hw (1..3); shift = hw*16 ∈ {16,32,48}, emitted from
; a tiny string table.  Caller guarantees hw != 0.  Clobbers A, DE.
; -----------------------------------------------------------------------
disasm_mw_emit_lsl:
                ld      (hl), ","
                inc     hl
                ld      (hl), " "
                inc     hl
                ld      (hl), "l"
                inc     hl
                ld      (hl), "s"
                inc     hl
                ld      (hl), "l"
                inc     hl
                ld      (hl), " "
                inc     hl
                ld      (hl), "#"
                inc     hl
; index the shift-amount table by (hw-1)*3 (each entry is "NN\0", 3 bytes).
                ld      a, (disasm_mw_hw)
                dec     a
                ld      c, a                        ; c = hw-1
                add     a, a                        ; 2*(hw-1)
                add     a, c                        ; 3*(hw-1)
                ld      e, a
                ld      d, 0
                ld      ix, disasm_mw_lsl_tbl
                add     ix, de
                ld      a, (ix+0)
                ld      (hl), a
                inc     hl
                ld      a, (ix+1)
                ld      (hl), a
                inc     hl
                ret


; -----------------------------------------------------------------------
; disasm_mw_emit_hexbuf — emit minimal-width lowercase hex of an N-byte
; little-endian buffer disasm_mw_val[0..N-1] to (HL), advancing HL.
;
; Walk bytes most-significant → least; skip leading 0x00 bytes; for the
; first nonzero byte emit its high nibble only if nonzero, then its low
; nibble; thereafter emit BOTH nibbles of every remaining byte.  If all
; bytes zero, emit a single '0'.
;
; Input:  A = N (number of bytes, 2/4/8), HL = dest.
; Clobbers A, B, C, DE, F.  Preserves HL semantics (advanced).
; -----------------------------------------------------------------------
disasm_mw_emit_hexbuf:
                ld      b, a                        ; B = byte count
                ld      c, 0                        ; C = "emitted a digit" flag
; DE points at the most-significant byte = &val[N-1].
                ld      a, b
                dec     a
                ld      e, a
                ld      d, 0
                ld      ix, disasm_mw_val
                add     ix, de                      ; IX = &val[N-1]
disasm_mw_hb_loop:
                ld      a, (ix+0)
                or      a
                jr      nz, disasm_mw_hb_nonzero
; zero byte.
                ld      a, c
                or      a
                jr      z, disasm_mw_hb_next        ; still suppressing leading zeros
; already emitting → print both nibbles of this 0x00.
                ld      a, 0
                call    disasm_emit_hex_byte        ; "00"
                jr      disasm_mw_hb_next
disasm_mw_hb_nonzero:
                ld      a, c
                or      a
                jr      nz, disasm_mw_hb_full       ; already emitting → both nibbles
; first nonzero byte: high nibble only if nonzero, then low nibble.
                ld      a, (ix+0)
                rra
                rra
                rra
                rra
                and     &0f
                jr      z, disasm_mw_hb_first_low   ; high nibble zero → skip it
                call    disasm_emit_hex_nibble
disasm_mw_hb_first_low:
                ld      a, (ix+0)
                call    disasm_emit_hex_nibble
                ld      c, 1
                jr      disasm_mw_hb_next
disasm_mw_hb_full:
                ld      a, (ix+0)
                call    disasm_emit_hex_byte
disasm_mw_hb_next:
                dec     ix
                djnz    disasm_mw_hb_loop
; if nothing was emitted, the value was all-zero → emit a single '0'.
                ld      a, c
                or      a
                ret     nz
                ld      (hl), "0"
                inc     hl
                ret


; =======================================================================
; Load/store (memory) family — Z80 port of tools/aarch64dec/mem.go
; (decodeMem + decodeScalarMem + decodeIndexed + decodeRegOffset +
;  decodePairMem; decodeLiteralMem is declined, see below).
;
; Register layout on entry (disasm_entry): B=bits31:24 C=bits23:16
; D=bits15:8 E=bits7:0.  Bit-field extractions:
;   bits29:26 = (B>>2)&0xf       size bits31:30 = (B>>6)&3
;   bits25:24 = B&3              opc  bits23:22 = (C>>6)&3
;   bit21     = (C>>5)&1         Rt   bits4:0   = E&0x1f
;   Rn bits9:5  = ((D&3)<<3)|(E>>5)    idxBits bits11:10 = (D>>2)&3
;   imm12 bits21:10 = ((C&0x3f)<<6)|(D>>2)
;   imm9  bits20:12 = ((C&0x1f)<<4)|(D>>4)
;   Rm bits20:16 = C&0x1f   option bits15:13 = (D>>5)&7  S bit12 = (D>>4)&1
; Pair: opc=(B>>6)&3  mode bits24:23=((B&1)<<1)|(C>>7)  L bit22=(C>>6)&1
;   imm7 bits21:15=((C&0x3f)<<1)|(D>>7)  Rt2 bits14:10=(D>>2)&0x1f
;
; decodeLiteralMem (LDR/LDRSW literal, bits29:26==0110) renders a
; PC-relative *target address* (pc + sext(imm19)<<2).  The Z80 decoder
; receives no PC (the paged_call ABI passes only the word), so it cannot
; reproduce objdump's target — it DECLINES the literal space (falls through
; to .inst).  This is the one mem sub-form intentionally left unported.
;
; ABI: this path clobbers BC/IX as scratch on success; it pushes them
; after the LAST decline and restores via disasm_mem_done (pop ix/pop bc).
; All decline paths reach disasm_not_mem with B,C,D,E intact.
; =======================================================================
disasm_try_mem:
; group = bits29:26 = (B>>2)&0xf.
                ld      a, b
                rrca
                rrca
                and     &0f
                cp      &0e                 ; 0b1110 scalar load/store
                jp      z, disasm_mem_scalar
                cp      &0a                 ; 0b1010 load/store pair
                jp      z, disasm_mem_pair_grp
                ; 0b0110 literal LDR is declined (no PC); all else not mem.
                jp      disasm_not_mem

disasm_mem_pair_grp:
; Pair requires bit25==0 (group selector bits29:25 == 0b1010_0).
                bit     1, b                ; bit25 = B bit1
                jp      nz, disasm_not_mem
                jp      disasm_mem_pair


; -----------------------------------------------------------------------
; Scalar (single-register) load/store.  bits29:26 == 0b1110.
; -----------------------------------------------------------------------
disasm_mem_scalar:
; Decode common fields into scratch.
                call    disasm_mem_decode_common
; mode = bits25:24 = B&3.
                ld      a, b
                and     3
                cp      1
                jp      z, disasm_mem_uimm   ; mode 01 → unsigned imm12 offset
                cp      0
                jp      nz, disasm_not_mem   ; mode 10/11 → not in our set
; mode 00: split on idxBits (bits11:10) and bit21.
                ld      a, (disasm_mem_idx)
                cp      0
                jp      z, disasm_mem_unscaled
                cp      2
                jp      z, disasm_mem_regoff
                cp      1
                jp      z, disasm_mem_post
                ; idxBits == 3 → pre-index.
                jp      disasm_mem_pre


; -----------------------------------------------------------------------
; disasm_mem_decode_common — extract size,opc,Rt,Rn,idxBits,bit21 into
; scratch (scalar forms).  Clobbers A, HL.  Leaves B,C,D,E intact.
; -----------------------------------------------------------------------
disasm_mem_decode_common:
; size = (B>>6)&3.
                ld      a, b
                rlca
                rlca
                and     3
                ld      (disasm_mem_size), a
; opc = (C>>6)&3.
                ld      a, c
                rlca
                rlca
                and     3
                ld      (disasm_mem_opc), a
; Rt = E&0x1f.
                ld      a, e
                and     &1f
                ld      (disasm_mem_rt), a
; Rn = ((D&3)<<3)|(E>>5).
                ld      a, d
                and     3
                add     a, a
                add     a, a
                add     a, a                ; (D&3)<<3
                ld      l, a
                ld      a, e
                rlca
                rlca
                rlca
                and     7                   ; E>>5
                or      l
                ld      (disasm_mem_rn), a
; idxBits = (D>>2)&3.
                ld      a, d
                rrca
                rrca
                and     3
                ld      (disasm_mem_idx), a
; bit21 = (C>>5)&1.
                ld      a, c
                rlca
                rlca
                rlca
                and     1
                ld      (disasm_mem_bit21), a
; Compute imm12 and imm9 into scratch NOW, while the word bytes B,C,D,E are
; still in registers.  The mnemonic table lookup (disasm_mem_mnem_lookup)
; clobbers DE, so the offset must be derived before that runs.  imm9 is
; computed first because the imm12 step destroys the D register.
; imm9 = ((C&0x1f)<<4)|(D>>4)  → 9-bit, stored disasm_mem_imm9 (h = bit8).
                ld      a, c
                and     &1f
                ld      l, a
                ld      h, 0
                add     hl, hl
                add     hl, hl
                add     hl, hl
                add     hl, hl                      ; (C&0x1f)<<4
                ld      a, d
                rrca
                rrca
                rrca
                rrca
                and     &0f                         ; D>>4
                or      l
                ld      l, a
                ld      (disasm_mem_imm9), hl       ; HL = imm9 (h = bit8)
; imm12 = ((C&0x3f)<<6)|(D>>2)  → HL, stored disasm_mem_imm12.
                ld      a, c
                and     &3f
                ld      l, a
                ld      h, 0
                add     hl, hl
                add     hl, hl
                add     hl, hl
                add     hl, hl
                add     hl, hl
                add     hl, hl                      ; (C&0x3f)<<6
                ld      a, d
                rrca
                rrca
                and     &3f                         ; D>>2 (0..63)
                add     a, l                        ; low byte += (D>>2)
                ld      l, a
                jr      nc, disasm_dc_imm12_done
                inc     h                           ; carry into high byte
disasm_dc_imm12_done:
                ld      (disasm_mem_imm12), hl
; Register-offset fields (used only by the regoff path, but extracted here
; because the mnemonic table lookup clobbers the D register downstream).
; option = (D>>5)&7 ; S = (D>>4)&1 ; Rm = C&0x1f.
                ld      a, d
                rlca
                rlca
                rlca
                and     7
                ld      (disasm_mem_option), a
                ld      a, d
                rrca
                rrca
                rrca
                rrca
                and     1
                ld      (disasm_mem_s), a
                ld      a, c
                and     &1f
                ld      (disasm_mem_rm), a
                ret


; -----------------------------------------------------------------------
; mode 01: unsigned immediate offset.  off = imm12 * (1<<size).
; -----------------------------------------------------------------------
disasm_mem_uimm:
                call    disasm_mem_scalar_mnem      ; sets mnem+is64 or declines
                jp      nc, disasm_not_mem
; Committed to success — save BC/IX (the emit code clobbers them) before
; any register-clobbering arithmetic.  Restored via disasm_mem_done.
                push    bc
                push    ix
; imm12 = ((C&0x3f)<<6)|(D>>2)  → 12-bit unsigned.  Then scale by 1<<size.
; Build 16-bit value in HL: hi = (C&0x3f)>>2 ... do it as full shift.
; imm12 fits in 12 bits; *scale (1,2,4,8) keeps it within 16 bits for the
; corpus.  Compute imm12 into DE first.
                call    disasm_mem_imm12_to_de      ; DE = imm12
; scale: shift DE left by size.
                ld      a, (disasm_mem_size)
                or      a
                jr      z, disasm_mem_uimm_scaled
                ld      b, a
disasm_mem_uimm_shl:
                sla     e
                rl      d
                djnz    disasm_mem_uimm_shl
disasm_mem_uimm_scaled:
; off is unsigned (>=0).  Emit "<Rt>, " then memOffset(base, off).
                ld      (disasm_mem_off_lo), de
                xor     a
                ld      (disasm_mem_off_neg), a     ; positive
                jp      disasm_mem_emit_base_off


; -----------------------------------------------------------------------
; mode 00, idxBits 00: unscaled STUR/LDUR.  off = sext(imm9).  bit21 must
; be 0 (else atomic memory-ops sub-space → decline).
; -----------------------------------------------------------------------
disasm_mem_unscaled:
                ld      a, (disasm_mem_bit21)
                or      a
                jp      nz, disasm_not_mem
                call    disasm_mem_stur_mnem
                jp      nc, disasm_not_mem
                push    bc
                push    ix
                call    disasm_mem_imm9_signed      ; sets off + off_neg
                jp      disasm_mem_emit_base_off


; -----------------------------------------------------------------------
; mode 00, idxBits 01: post-index `[Rn], #N`.  bit21 must be 0.
; -----------------------------------------------------------------------
disasm_mem_post:
                ld      a, (disasm_mem_bit21)
                or      a
                jp      nz, disasm_not_mem
                call    disasm_mem_scalar_mnem
                jp      nc, disasm_not_mem
                push    bc
                push    ix
                call    disasm_mem_imm9_signed
                xor     a
                ld      (disasm_mem_idxmode), a     ; 0 = post
                jp      disasm_mem_emit_indexed


; -----------------------------------------------------------------------
; mode 00, idxBits 11: pre-index `[Rn, #N]!`.  bit21 must be 0.
; -----------------------------------------------------------------------
disasm_mem_pre:
                ld      a, (disasm_mem_bit21)
                or      a
                jp      nz, disasm_not_mem
                call    disasm_mem_scalar_mnem
                jp      nc, disasm_not_mem
                push    bc
                push    ix
                call    disasm_mem_imm9_signed
                ld      a, 1
                ld      (disasm_mem_idxmode), a     ; 1 = pre
                jp      disasm_mem_emit_indexed


; -----------------------------------------------------------------------
; mode 00, idxBits 10: register offset.  bit21 must be 1.
; `<Rt>, [Rn, Rm{, <ext> {#amt}}]`.
; -----------------------------------------------------------------------
disasm_mem_regoff:
                ld      a, (disasm_mem_bit21)
                cp      1
                jp      nz, disasm_not_mem
                call    disasm_mem_scalar_mnem
                jp      nc, disasm_not_mem
; option/S/Rm were precomputed into scratch by disasm_mem_decode_common
; (before the mnemonic table lookup clobbered the D register).
; Decline (word intact) if option not in {010,011,110,111}.
                ld      a, (disasm_mem_option)
                cp      2
                jp      z, disasm_mem_emit_regoff
                cp      3
                jp      z, disasm_mem_emit_regoff
                cp      6
                jp      z, disasm_mem_emit_regoff
                cp      7
                jp      z, disasm_mem_emit_regoff
                jp      disasm_not_mem


; =======================================================================
; Emit helpers.  All run with BC/IX saved (pushed at the top of the emit
; path) and return through disasm_mem_done.
; =======================================================================

; --- emit `<Rt>, [Rn{, #off}]` (unsigned/unscaled, no index writeback) --
; Caller has already saved BC/IX.
disasm_mem_emit_base_off:
                call    disasm_mem_write_mnem
                ld      hl, DISASM_COMM_OPS
                call    disasm_mem_emit_rt          ; <Rt>
                ld      (hl), ","
                inc     hl
                ld      (hl), " "
                inc     hl
                call    disasm_mem_emit_addr_off    ; [Rn] or [Rn, #off]
                ld      (hl), 0
                jp      disasm_mem_done

; --- emit indexed `<Rt>, [Rn, #off]!` (pre) or `<Rt>, [Rn], #off` (post) -
; Caller has already saved BC/IX.
disasm_mem_emit_indexed:
                call    disasm_mem_write_mnem
                ld      hl, DISASM_COMM_OPS
                call    disasm_mem_emit_rt
                ld      (hl), ","
                inc     hl
                ld      (hl), " "
                inc     hl
                ld      a, (disasm_mem_idxmode)
                or      a
                jr      z, disasm_mem_emit_post
; pre-index: "[Rn, #off]!"
                ld      (hl), "["
                inc     hl
                call    disasm_mem_emit_base
                ld      (hl), ","
                inc     hl
                ld      (hl), " "
                inc     hl
                call    disasm_mem_emit_signed_imm
                ld      (hl), "]"
                inc     hl
                ld      (hl), "!"
                inc     hl
                ld      (hl), 0
                jp      disasm_mem_done
disasm_mem_emit_post:
; post-index: "[Rn], #off"
                ld      (hl), "["
                inc     hl
                call    disasm_mem_emit_base
                ld      (hl), "]"
                inc     hl
                ld      (hl), ","
                inc     hl
                ld      (hl), " "
                inc     hl
                call    disasm_mem_emit_signed_imm
                ld      (hl), 0
                jp      disasm_mem_done

; --- emit register-offset `<Rt>, [Rn, Rm{, <ext> {#amt}}]` --------------
; Reached only with a valid option (validated in disasm_mem_regoff).
disasm_mem_emit_regoff:
                push    bc
                push    ix
                call    disasm_mem_write_mnem
                ld      hl, DISASM_COMM_OPS
                call    disasm_mem_emit_rt
                ld      (hl), ","
                inc     hl
                ld      (hl), " "
                inc     hl
                ld      (hl), "["
                inc     hl
                call    disasm_mem_emit_base
                ld      (hl), ","
                inc     hl
                ld      (hl), " "
                inc     hl
; Determine Rm width: option 011/111 → x (Xm), 010/110 → w (Wm).
                ld      a, (disasm_mem_option)
                cp      3                           ; 011 LSL
                jr      z, disasm_mem_ro_lsl
                cp      2                           ; 010 UXTW
                jr      z, disasm_mem_ro_uxtw
                cp      6                           ; 110 SXTW
                jr      z, disasm_mem_ro_sxtw
                jr      disasm_mem_ro_sxtx          ; option 111 SXTX

disasm_mem_ro_lsl:
; Xm, then optional ", lsl #amt" when S=1.  amt = size.
                ld      a, 1
                call    disasm_mem_emit_rm          ; A=1 → x
                ld      a, (disasm_mem_s)
                or      a
                jr      z, disasm_mem_ro_close
                ld      de, disasm_mem_lsl_txt
                call    disasm_mem_emit_ext_amt
                jr      disasm_mem_ro_close

disasm_mem_ro_uxtw:
                xor     a
                call    disasm_mem_emit_rm          ; A=0 → w
                ld      de, disasm_mem_uxtw_txt
                jr      disasm_mem_ro_ext_common
disasm_mem_ro_sxtw:
                xor     a
                call    disasm_mem_emit_rm          ; w
                ld      de, disasm_mem_sxtw_txt
                jr      disasm_mem_ro_ext_common
disasm_mem_ro_sxtx:
                ld      a, 1
                call    disasm_mem_emit_rm          ; x
                ld      de, disasm_mem_sxtx_txt
disasm_mem_ro_ext_common:
; For UXTW/SXTW/SXTX: when S=1 emit ", <ext> #amt"; when S=0 emit ", <ext>".
                ld      a, (disasm_mem_s)
                or      a
                jr      nz, disasm_mem_ro_ext_amt
; ", <ext>" (no amount).
                push    de
                ld      (hl), ","
                inc     hl
                ld      (hl), " "
                inc     hl
                pop     de
                call    disasm_mem_emit_str_de
                jr      disasm_mem_ro_close
disasm_mem_ro_ext_amt:
                call    disasm_mem_emit_ext_amt
disasm_mem_ro_close:
                ld      (hl), "]"
                inc     hl
                ld      (hl), 0
                jp      disasm_mem_done


; -----------------------------------------------------------------------
; disasm_mem_emit_ext_amt — emit ", <ext> #amt" where DE→ext string and
; amt = size.  Advances HL.  Clobbers A, BC, DE.
; -----------------------------------------------------------------------
disasm_mem_emit_ext_amt:
                push    de
                ld      (hl), ","
                inc     hl
                ld      (hl), " "
                inc     hl
                pop     de
                call    disasm_mem_emit_str_de      ; ext keyword
                ld      (hl), " "
                inc     hl
                ld      (hl), "#"
                inc     hl
                ld      a, (disasm_mem_size)
                ld      e, a
                ld      d, 0
                call    disasm_emit_dec16
                ret


; -----------------------------------------------------------------------
; disasm_mem_emit_str_de — copy null-terminated string at (DE) to (HL),
; advancing HL past it (terminator not copied).  Clobbers A, DE.
; -----------------------------------------------------------------------
disasm_mem_emit_str_de:
                ld      a, (de)
                or      a
                ret     z
                ld      (hl), a
                inc     hl
                inc     de
                jr      disasm_mem_emit_str_de


; -----------------------------------------------------------------------
; disasm_mem_emit_addr_off — emit "[Rn]" (off==0) or "[Rn, #off]".
; Uses disasm_mem_off_lo/hi + disasm_mem_off_neg.  Advances HL.
; -----------------------------------------------------------------------
disasm_mem_emit_addr_off:
                ld      (hl), "["
                inc     hl
                call    disasm_mem_emit_base
; off == 0 ?  (neg flag clear AND value zero)
                ld      a, (disasm_mem_off_neg)
                or      a
                jr      nz, disasm_mem_ao_nonzero
                ld      a, (disasm_mem_off_lo)
                ld      b, a
                ld      a, (disasm_mem_off_hi)
                or      b
                jr      nz, disasm_mem_ao_nonzero
; zero offset → just "]"
                ld      (hl), "]"
                inc     hl
                ret
disasm_mem_ao_nonzero:
                ld      (hl), ","
                inc     hl
                ld      (hl), " "
                inc     hl
                call    disasm_mem_emit_signed_imm
                ld      (hl), "]"
                inc     hl
                ret


; -----------------------------------------------------------------------
; disasm_mem_emit_signed_imm — emit "#N" or "#-N" (decimal) from the
; 16-bit magnitude disasm_mem_off_lo/hi with sign disasm_mem_off_neg.
; Advances HL.  Clobbers A, BC, DE.
; -----------------------------------------------------------------------
disasm_mem_emit_signed_imm:
                ld      (hl), "#"
                inc     hl
                ld      a, (disasm_mem_off_neg)
                or      a
                jr      z, disasm_mem_si_pos
                ld      (hl), "-"
                inc     hl
disasm_mem_si_pos:
                ld      a, (disasm_mem_off_lo)
                ld      e, a
                ld      a, (disasm_mem_off_hi)
                ld      d, a
                call    disasm_emit_dec16
                ret


; -----------------------------------------------------------------------
; disasm_mem_emit_rt — emit the Rt register (idx disasm_mem_rt, width
; disasm_mem_is64).  Advances HL.  Clobbers A, DE.
; disasm_mem_emit_base — emit base register Rn: x<n> or sp (idx 31).
; disasm_mem_emit_rm  — emit Rm given width flag in A (0=w,1=x).
; -----------------------------------------------------------------------
disasm_mem_emit_rt:
                ld      a, (disasm_mem_rt)
                ld      c, a
                ld      a, (disasm_mem_is64)
                ld      b, a                        ; B = is64
                jp      disasm_mem_emit_genreg

disasm_mem_emit_rm:
; A = width flag (0=w,1=x); index = Rm (disasm_mem_rm).
                ld      b, a                        ; B = is64 flag
                ld      a, (disasm_mem_rm)
                ld      c, a                        ; C = Rm index
                jp      disasm_mem_emit_genreg

; emit a general Rt-style register: B=is64(0/1), C=index(0..31).
; idx 31 → "wzr"/"xzr".  Advances HL.  Clobbers A, DE.
disasm_mem_emit_genreg:
                ld      a, c
                cp      31
                jr      z, disasm_mem_gr_zero
                ld      a, b
                or      a
                ld      a, "w"
                jr      z, disasm_mem_gr_prefix
                ld      a, "x"
disasm_mem_gr_prefix:
                ld      (hl), a
                inc     hl
                ld      a, c
                ld      e, a
                ld      d, 0
                call    disasm_emit_dec16
                ret
disasm_mem_gr_zero:
                ld      a, b
                or      a
                ld      a, "w"
                jr      z, disasm_mem_gr_zp
                ld      a, "x"
disasm_mem_gr_zp:
                ld      (hl), a
                inc     hl
                ld      (hl), "z"
                inc     hl
                ld      (hl), "r"
                inc     hl
                ret

; base register: always 64-bit; idx 31 → "sp", else "x<n>".
disasm_mem_emit_base:
                ld      a, (disasm_mem_rn)
                cp      31
                jr      z, disasm_mem_base_sp
                ld      (hl), "x"
                inc     hl
                ld      e, a
                ld      d, 0
                call    disasm_emit_dec16
                ret
disasm_mem_base_sp:
                ld      (hl), "s"
                inc     hl
                ld      (hl), "p"
                inc     hl
                ret


; -----------------------------------------------------------------------
; disasm_mem_write_mnem — copy the mnemonic at disasm_mem_mnem_ptr (a
; null-terminated string) to DISASM_COMM_MNEM.  Clobbers A, DE, HL.
; -----------------------------------------------------------------------
disasm_mem_write_mnem:
                ld      hl, (disasm_mem_mnem_ptr)
                ld      de, DISASM_COMM_MNEM
disasm_mem_wm_loop:
                ld      a, (hl)
                ld      (de), a
                or      a
                ret     z
                inc     hl
                inc     de
                jr      disasm_mem_wm_loop


; -----------------------------------------------------------------------
; disasm_mem_imm12_to_de — DE = imm12 = ((C&0x3f)<<6)|(D>>2).
; Clobbers A, HL.  Leaves B,C,D,E intact.
; -----------------------------------------------------------------------
disasm_mem_imm12_to_de:
; imm12 was precomputed into scratch by disasm_mem_decode_common (before the
; mnemonic table lookup clobbered the D register).
                ld      de, (disasm_mem_imm12)
                ret


; -----------------------------------------------------------------------
; disasm_mem_imm9_signed — compute signed imm9 (bits20:12), store magnitude
; in disasm_mem_off_lo/hi and sign in disasm_mem_off_neg (1=negative).
; imm9 = ((C&0x1f)<<4)|(D>>4).  Sign bit = bit8 of imm9.
; Clobbers A, BC?, DE, HL — but B,C,D,E (the word) must survive: we only
; read them, and use HL/local A.  Preserves the word.
; -----------------------------------------------------------------------
disasm_mem_imm9_signed:
; imm9 (0..511) was precomputed into scratch by disasm_mem_decode_common.
                ld      hl, (disasm_mem_imm9)
; sign: bit8 set?  HL >= 0x100 → negative.
                ld      a, h
                or      a
                jr      nz, disasm_mem_i9_neg        ; h!=0 means bit8 set
                xor     a
                ld      (disasm_mem_off_neg), a
                ld      (disasm_mem_off_lo), hl
                ret
disasm_mem_i9_neg:
; magnitude = 512 - imm9 = (0x200 - HL).  HL in [256..511].
                ld      a, 1
                ld      (disasm_mem_off_neg), a
                ex      de, hl                      ; DE = imm9
                ld      hl, &0200
                or      a
                sbc     hl, de                      ; HL = 512 - imm9
                ld      (disasm_mem_off_lo), hl
                ret


; -----------------------------------------------------------------------
; disasm_mem_scalar_mnem / disasm_mem_stur_mnem — map (size,opc) to a
; mnemonic pointer (disasm_mem_mnem_ptr) and is64 flag (disasm_mem_is64).
; Carry SET on success, CLEAR on decline.  Clobbers A, HL, DE.
; -----------------------------------------------------------------------
disasm_mem_scalar_mnem:
                ld      de, disasm_mem_scalar_tbl
                jp      disasm_mem_mnem_lookup
disasm_mem_stur_mnem:
                ld      de, disasm_mem_stur_tbl
                ; fall through

; Table format per (size*4+opc) slot, 3 bytes:
;   byte0 = is64 flag (0/1) | 0x80 set when slot is VALID; 0 = invalid.
;   bytes1:2 = little-endian pointer to mnemonic string.
; Indexed by (size<<2)|opc → 16 slots.
; Each slot is 3 bytes → offset = slot*3 where slot = (size<<2)|opc.
disasm_mem_mnem_lookup:
                ld      a, (disasm_mem_size)
                add     a, a
                add     a, a
                ld      l, a
                ld      a, (disasm_mem_opc)
                add     a, l                        ; slot
                ld      l, a
                add     a, a                        ; 2*slot
                add     a, l                        ; 3*slot
                ld      l, a
                ld      h, 0
                add     hl, de                      ; HL = &tbl[slot*3]
                ld      a, (hl)
                or      a
                jr      z, disasm_mem_mnem_invalid   ; 0 → invalid slot
; valid: bit0 = is64.
                and     1
                ld      (disasm_mem_is64), a
                inc     hl
                ld      e, (hl)
                inc     hl
                ld      d, (hl)
                ld      (disasm_mem_mnem_ptr), de
                scf
                ret
disasm_mem_mnem_invalid:
                or      a                           ; clear carry
                ret


; =======================================================================
; Pair load/store.  bits29:26==0b1010, bit25==0.
; =======================================================================
disasm_mem_pair:
; opc = (B>>6)&3.  Only 00 (W,scale4) and 10 (X,scale8) supported.
                ld      a, b
                rlca
                rlca
                and     3
                cp      0
                jr      z, disasm_mem_pair_w
                cp      2
                jp      nz, disasm_not_mem          ; opc 01/11 → SIMD/undef
; opc 10 → 64-bit, scale 8.
                ld      a, 1
                ld      (disasm_mem_is64), a
                ld      a, 8
                jr      disasm_mem_pair_scale
disasm_mem_pair_w:
                xor     a
                ld      (disasm_mem_is64), a
                ld      a, 4
disasm_mem_pair_scale:
                ld      (disasm_mem_pscale), a
; L = (C>>6)&1 → stp(0)/ldp(1).
                ld      a, c
                rlca
                rlca
                and     1
                jr      z, disasm_mem_pair_stp
                ld      hl, disasm_mem_ldp_txt
                jr      disasm_mem_pair_mnemset
disasm_mem_pair_stp:
                ld      hl, disasm_mem_stp_txt
disasm_mem_pair_mnemset:
                ld      (disasm_mem_mnem_ptr), hl
; Rt1 = E&0x1f ; Rn = ((D&3)<<3)|(E>>5) ; Rt2 = (D>>2)&0x1f.
                ld      a, e
                and     &1f
                ld      (disasm_mem_rt), a          ; Rt1
                ld      a, d
                and     3
                add     a, a
                add     a, a
                add     a, a
                ld      l, a
                ld      a, e
                rlca
                rlca
                rlca
                and     7
                or      l
                ld      (disasm_mem_rn), a
                ld      a, d
                rrca
                rrca
                and     &1f
                ld      (disasm_mem_rt2), a         ; Rt2
; imm7 = ((C&0x3f)<<1)|(D>>7) ; 7-bit signed.  off = sext(imm7)*scale.
                ld      a, c
                and     &3f
                add     a, a                        ; (C&0x3f)<<1
                ld      l, a
                ld      a, d
                rlca                                ; D>>7 → carry; bring into bit0
                and     1
                or      l
                ld      l, a                        ; L = imm7 (0..127)
; sign-extend bit6: if imm7>=64 → negative, magnitude = (128-imm7).
; Then multiply magnitude by scale.
                ld      a, l
                cp      64
                jr      nc, disasm_mem_pair_neg
; positive: magnitude = imm7.
                xor     a
                ld      (disasm_mem_off_neg), a
                ld      a, l                        ; imm7
                jr      disasm_mem_pair_mul
disasm_mem_pair_neg:
                ld      a, 1
                ld      (disasm_mem_off_neg), a
                ld      a, 128
                sub     l                           ; 128 - imm7
disasm_mem_pair_mul:
; A = magnitude (0..64) ; multiply by pscale (4 or 8) → 16-bit in HL.
                ld      l, a
                ld      h, 0
                ld      a, (disasm_mem_pscale)
                cp      8
                jr      z, disasm_mem_pair_mul8
; *4
                add     hl, hl
                add     hl, hl
                jr      disasm_mem_pair_store
disasm_mem_pair_mul8:
                add     hl, hl
                add     hl, hl
                add     hl, hl
disasm_mem_pair_store:
                ld      (disasm_mem_off_lo), hl
; mode = ((B&1)<<1)|(C>>7).  10 signed-off, 11 pre, 01 post.
                ld      a, b
                and     1
                add     a, a                        ; (B&1)<<1
                ld      l, a
                ld      a, c
                rlca                                ; C>>7 → carry→bit0
                and     1
                or      l
                ld      (disasm_mem_idxmode), a     ; 1=post,2=signed,3=pre
                cp      2
                jp      z, disasm_mem_pair_emit_off
                cp      3
                jp      z, disasm_mem_pair_emit_pre
                cp      1
                jp      z, disasm_mem_pair_emit_post
                jp      disasm_not_mem              ; mode 00 → undefined here

; common pair register prefix: "<Rt1>, <Rt2>, "
disasm_mem_pair_regs:
                ld      hl, DISASM_COMM_OPS
                call    disasm_mem_emit_rt          ; Rt1 (uses disasm_mem_rt)
                ld      (hl), ","
                inc     hl
                ld      (hl), " "
                inc     hl
; Rt2.
                ld      a, (disasm_mem_rt2)
                ld      c, a
                ld      a, (disasm_mem_is64)
                ld      b, a
                call    disasm_mem_emit_genreg
                ld      (hl), ","
                inc     hl
                ld      (hl), " "
                inc     hl
                ret

disasm_mem_pair_emit_off:
                push    bc
                push    ix
                call    disasm_mem_write_mnem
                call    disasm_mem_pair_regs
                call    disasm_mem_emit_addr_off    ; [Rn] or [Rn, #off]
                ld      (hl), 0
                jp      disasm_mem_done
disasm_mem_pair_emit_pre:
                push    bc
                push    ix
                call    disasm_mem_write_mnem
                call    disasm_mem_pair_regs
                ld      (hl), "["
                inc     hl
                call    disasm_mem_emit_base
                ld      (hl), ","
                inc     hl
                ld      (hl), " "
                inc     hl
                call    disasm_mem_emit_signed_imm
                ld      (hl), "]"
                inc     hl
                ld      (hl), "!"
                inc     hl
                ld      (hl), 0
                jp      disasm_mem_done
disasm_mem_pair_emit_post:
                push    bc
                push    ix
                call    disasm_mem_write_mnem
                call    disasm_mem_pair_regs
                ld      (hl), "["
                inc     hl
                call    disasm_mem_emit_base
                ld      (hl), "]"
                inc     hl
                ld      (hl), ","
                inc     hl
                ld      (hl), " "
                inc     hl
                call    disasm_mem_emit_signed_imm
                ld      (hl), 0
                jp      disasm_mem_done


; -----------------------------------------------------------------------
; disasm_mem_done — restore BC/IX (saved at the start of each emit path)
; and return.  Honours disasm_entry's "Preserves: BC, IX, IY" ABI.
; -----------------------------------------------------------------------
disasm_mem_done:
                pop     ix
                pop     bc
                ret


; --- load/store mnemonic strings (null-terminated) --------------------
disasm_mem_str_str:     defm    "str"
                        defb    0
disasm_mem_ldr_str:     defm    "ldr"
                        defb    0
disasm_mem_strb_str:    defm    "strb"
                        defb    0
disasm_mem_ldrb_str:    defm    "ldrb"
                        defb    0
disasm_mem_ldrsb_str:   defm    "ldrsb"
                        defb    0
disasm_mem_strh_str:    defm    "strh"
                        defb    0
disasm_mem_ldrh_str:    defm    "ldrh"
                        defb    0
disasm_mem_ldrsh_str:   defm    "ldrsh"
                        defb    0
disasm_mem_ldrsw_str:   defm    "ldrsw"
                        defb    0
disasm_mem_stur_str:    defm    "stur"
                        defb    0
disasm_mem_ldur_str:    defm    "ldur"
                        defb    0
disasm_mem_sturb_str:   defm    "sturb"
                        defb    0
disasm_mem_ldurb_str:   defm    "ldurb"
                        defb    0
disasm_mem_ldursb_str:  defm    "ldursb"
                        defb    0
disasm_mem_sturh_str:   defm    "sturh"
                        defb    0
disasm_mem_ldurh_str:   defm    "ldurh"
                        defb    0
disasm_mem_ldursh_str:  defm    "ldursh"
                        defb    0
disasm_mem_ldursw_str:  defm    "ldursw"
                        defb    0
disasm_mem_stp_txt:     defm    "stp"
                        defb    0
disasm_mem_ldp_txt:     defm    "ldp"
                        defb    0
disasm_mem_lsl_txt:     defm    "lsl"
                        defb    0
disasm_mem_uxtw_txt:    defm    "uxtw"
                        defb    0
disasm_mem_sxtw_txt:    defm    "sxtw"
                        defb    0
disasm_mem_sxtx_txt:    defm    "sxtx"
                        defb    0

; --- (size<<2)|opc → (is64|valid, mnemonic-ptr) tables -----------------
; Slot byte0: 0 = invalid; else 1+is64 encoded as just is64 in bit0 with
; the slot being nonzero only because we never store 0 for a valid entry —
; but is64 can be 0.  To distinguish, valid slots store (is64 ? 1 : 1)?  No:
; we need a validity marker independent of is64.  Use byte0 bit1 as "valid"
; and bit0 as is64; invalid = 0x00.
; Reworked below: byte0 = 0x02 | is64  (valid), 0x00 (invalid).
;   value & 1 = is64 ; value != 0 = valid.
disasm_mem_scalar_tbl:
; size 00 (byte): strb(w) ldrb(w) ldrsb(x) ldrsb(w)
                defb    &02
                defw    disasm_mem_strb_str         ; opc00 strb is64=0
                defb    &02
                defw    disasm_mem_ldrb_str         ; opc01 ldrb is64=0
                defb    &03
                defw    disasm_mem_ldrsb_str        ; opc10 ldrsb is64=1
                defb    &02
                defw    disasm_mem_ldrsb_str        ; opc11 ldrsb is64=0
; size 01 (half): strh(w) ldrh(w) ldrsh(x) ldrsh(w)
                defb    &02
                defw    disasm_mem_strh_str
                defb    &02
                defw    disasm_mem_ldrh_str
                defb    &03
                defw    disasm_mem_ldrsh_str
                defb    &02
                defw    disasm_mem_ldrsh_str
; size 10 (word): str(w) ldr(w) ldrsw(x) invalid
                defb    &02
                defw    disasm_mem_str_str
                defb    &02
                defw    disasm_mem_ldr_str
                defb    &03
                defw    disasm_mem_ldrsw_str
                defb    &00
                defw    0                           ; opc11 invalid
; size 11 (double): str(x) ldr(x) invalid invalid
                defb    &03
                defw    disasm_mem_str_str
                defb    &03
                defw    disasm_mem_ldr_str
                defb    &00
                defw    0
                defb    &00
                defw    0

disasm_mem_stur_tbl:
; size 00: sturb(w) ldurb(w) ldursb(x) ldursb(w)
                defb    &02
                defw    disasm_mem_sturb_str
                defb    &02
                defw    disasm_mem_ldurb_str
                defb    &03
                defw    disasm_mem_ldursb_str
                defb    &02
                defw    disasm_mem_ldursb_str
; size 01: sturh(w) ldurh(w) ldursh(x) ldursh(w)
                defb    &02
                defw    disasm_mem_sturh_str
                defb    &02
                defw    disasm_mem_ldurh_str
                defb    &03
                defw    disasm_mem_ldursh_str
                defb    &02
                defw    disasm_mem_ldursh_str
; size 10: stur(w) ldur(w) ldursw(x) invalid
                defb    &02
                defw    disasm_mem_stur_str
                defb    &02
                defw    disasm_mem_ldur_str
                defb    &03
                defw    disasm_mem_ldursw_str
                defb    &00
                defw    0
; size 11: stur(x) ldur(x) invalid invalid
                defb    &03
                defw    disasm_mem_stur_str
                defb    &03
                defw    disasm_mem_ldur_str
                defb    &00
                defw    0
                defb    &00
                defw    0

; --- load/store working scratch (this page) ---------------------------
disasm_mem_size:        defb    0
disasm_mem_opc:         defb    0
disasm_mem_rt:          defb    0
disasm_mem_rt2:         defb    0
disasm_mem_rm:          defb    0
disasm_mem_rn:          defb    0
disasm_mem_idx:         defb    0       ; idxBits 11:10
disasm_mem_bit21:       defb    0
disasm_mem_is64:        defb    0
disasm_mem_idxmode:     defb    0       ; 0=post,1=pre (scalar); pair: 1/2/3
disasm_mem_option:      defb    0
disasm_mem_s:           defb    0
disasm_mem_pscale:      defb    0
disasm_mem_imm12:       defw    0       ; precomputed unsigned imm12
disasm_mem_imm9:        defw    0       ; precomputed imm9 (h byte = bit8)
disasm_mem_off_neg:     defb    0       ; 1 = offset negative
disasm_mem_off_lo:      defw    0       ; offset magnitude (lo,hi)
disasm_mem_off_hi:      equ     disasm_mem_off_lo+1
disasm_mem_mnem_ptr:    defw    0       ; pointer to chosen mnemonic string


; --- move-wide mnemonic strings (null-terminated) ---------------------
disasm_mw_mov_txt:      defm    "mov"
                        defb    0
disasm_mw_movz_txt:     defm    "movz"
                        defb    0
disasm_mw_movn_txt:     defm    "movn"
                        defb    0
disasm_mw_movk_txt:     defm    "movk"
                        defb    0

; --- lsl shift-amount table: hw=1→"16", 2→"32", 3→"48" ----------------
; Indexed by (hw-1)*3; each entry is two digits + a 0 pad (3 bytes).
disasm_mw_lsl_tbl:      defm    "16"
                        defb    0
                        defm    "32"
                        defb    0
                        defm    "48"
                        defb    0

; --- move-wide working scratch (this page; always accessible) ---------
disasm_mw_sf:           defb    0       ; 0 = 32-bit (w), 1 = 64-bit (x)
disasm_mw_opc:          defb    0       ; 0=movn 2=movz 3=movk
disasm_mw_hw:           defb    0       ; 0..3
disasm_mw_imm_hi:       defb    0       ; imm16 high byte
disasm_mw_imm_lo:       defb    0       ; imm16 low byte
disasm_mw_rd:           defb    0       ; destination register index 0..31
disasm_mw_val:          defs    8       ; little-endian materialised value


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


; -----------------------------------------------------------------------
; disasm_emit_dec16 — emit DE (unsigned 16-bit) as minimal-width decimal
; ASCII at (HL), advancing HL past the digits.  Value 0 emits "0".
; Leading zeros suppressed.  Matches Go's fmt "%d".
;
; Method: repeated subtraction against the descending power-of-ten table
; {10000, 1000, 100, 10, 1}.  For each power, count how many times it
; subtracts (0..9) → that digit.  Suppress leading zeros until the first
; non-zero digit (or the units place, so 0 still prints one digit).
;
; Register map during the loop:
;   HL = remaining value      BC = current power-of-ten
;   IY = output pointer       IX = power-of-ten table pointer
;   D  = "emitted a digit" flag (0/1)   E = current digit count
;
; Input:  DE = value, HL = dest.
; Output: HL advanced past the emitted digits.
; Clobbers: A, BC, DE, F.  Preserves IX, IY.
; -----------------------------------------------------------------------
disasm_emit_dec16:
                push    ix
                push    iy
                push    hl
                pop     iy              ; IY = output pointer
                ex      de, hl          ; HL = value to convert
                ld      ix, disasm_pow10_tbl
                ld      d, 0            ; D = emitted flag = false
disasm_dec_pow_loop:
                ld      c, (ix+0)       ; BC = current power (little-endian)
                ld      b, (ix+1)
                ld      a, b
                or      c
                jr      z, disasm_dec_done   ; sentinel 0 → finished
                ld      e, &ff          ; E = digit count, pre-incremented from -1
disasm_dec_count:
                inc     e
                or      a               ; clear carry for the subtract
                sbc     hl, bc          ; HL -= power
                jr      nc, disasm_dec_count ; still >= 0 → count another
                add     hl, bc          ; over-subtracted once → restore remainder
; E = digit (0..9).  Emit with leading-zero suppression.
                ld      a, e
                or      a
                jr      nz, disasm_dec_write      ; non-zero digit → always write
                ld      a, d
                or      a
                jr      nz, disasm_dec_write_zero ; already emitting → write the 0
; digit is a leading zero — suppress unless this is the units place (power == 1)
                ld      a, (ix+0)
                cp      1
                jr      nz, disasm_dec_next
                ld      a, (ix+1)
                or      a
                jr      nz, disasm_dec_next
disasm_dec_write_zero:
                ld      e, 0
disasm_dec_write:
                ld      a, e
                add     a, "0"
                ld      (iy+0), a
                inc     iy
                ld      d, 1            ; mark emitted
disasm_dec_next:
                inc     ix
                inc     ix              ; advance to next power (2 bytes)
                jr      disasm_dec_pow_loop
disasm_dec_done:
                push    iy
                pop     hl              ; HL = output pointer past the digits
                pop     iy
                pop     ix
                ret

; Descending powers of ten for the 16-bit decimal emitter, little-endian,
; terminated by a 0x0000 sentinel.
disasm_pow10_tbl:
                defw    10000
                defw    1000
                defw    100
                defw    10
                defw    1
                defw    0               ; sentinel


; -----------------------------------------------------------------------
; run_disasm_self_test — boot self-test, invoked via paged_call from the
; BUILD_TESTS boot sequence in src/assembler.asm.
;
; Reached via the &8003 jump-table slot (DISASM_SELF_TEST_ENTRY).  The
; self-test calls disasm_entry directly (no paged_call), because both
; routines are on the same page (15); no re-entrancy risk.
;
; Each fixture loads BC:IX with a 32-bit word, calls disasm_entry, and
; checks the resulting comm-buffer strings against the Go oracle's known
; output (captured in disasm_oracle_test.go).  Fixtures grow family-by-
; family alongside the decoder.
;
; Input:  none.
; Output: BC = 0 on success; BC = fail-tag (B=0, C=tag) on error.
;   &7D — NOP mnemonic check failed.
;   &7E — .inst mnemonic check failed.
;   &7C — UDF mnemonic check failed.
;   &7B — UDF operands check failed.
;   &7A — move-wide mov-alias check failed (mnemonic or operands).
;   &79 — move-wide kept-movz check failed (mnemonic or operands).
;   &78 — move-wide movk check failed (mnemonic or operands).
;   &77 — load/store scaled str check failed (mnemonic or operands).
;   &76 — load/store pair stp check failed (mnemonic or operands).
;   &75 — load/store ldrb check failed (mnemonic or operands).
;   &74 — load/store register-offset ldr check failed (mnemonic or operands).
; Clobbers: A, DE, HL, F (paged_call ABI; BC is the return value).
; -----------------------------------------------------------------------
run_disasm_self_test:

; NOP case: D503201F (BC = &D503, IX = &201F) → "nop".
                ld      bc, NOP_HI
                ld      ix, NOP_LO
                call    disasm_entry
                ld      a, (DISASM_COMM_MNEM)
                cp      "n"
                jp      nz, disasm_stest_fail_nop

; .inst case: a genuine corpus data word.  00010001 has nonzero top16
; (not UDF) and bits[28:23] != 100101 (not move-wide), so it falls through
; to the .inst default.
                ld      bc, &0001
                ld      ix, &0001
                call    disasm_entry
                ld      a, (DISASM_COMM_MNEM)
                cp      "."
                jp      nz, disasm_stest_fail_inst

; UDF case: 00001234 (BC = &0000, IX = &1234) → "udf", "#4660".
                ld      bc, &0000
                ld      ix, &1234
                call    disasm_entry
                ld      a, (DISASM_COMM_MNEM)
                cp      "u"
                jp      nz, disasm_stest_fail_udf
; operands must be "#4660\0".
                ld      hl, DISASM_COMM_OPS
                ld      de, disasm_stest_udf_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_udf_ops

; UDF imm16 = 0: 00000000 → "udf", "#0".
                ld      bc, &0000
                ld      ix, &0000
                call    disasm_entry
                ld      hl, DISASM_COMM_OPS
                ld      de, disasm_stest_udf0_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_udf_ops

; UDF imm16 = 0xFFFF: 0000FFFF → "udf", "#65535".
                ld      bc, &0000
                ld      ix, &FFFF
                call    disasm_entry
                ld      hl, DISASM_COMM_OPS
                ld      de, disasm_stest_udfmax_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_udf_ops

; move-wide: mov alias.  D2B00000 → "mov", "x0, #0x80000000".
                ld      bc, &D2B0
                ld      ix, &0000
                call    disasm_entry
                ld      hl, DISASM_COMM_MNEM
                ld      de, disasm_stest_mov_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_mov
                ld      hl, DISASM_COMM_OPS
                ld      de, disasm_stest_mov_ops_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_mov

; move-wide: kept movz.  D2A00000 → "movz", "x0, #0x0, lsl #16".
                ld      bc, &D2A0
                ld      ix, &0000
                call    disasm_entry
                ld      hl, DISASM_COMM_MNEM
                ld      de, disasm_stest_movz_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_movz
                ld      hl, DISASM_COMM_OPS
                ld      de, disasm_stest_movz_ops_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_movz

; move-wide: movk with lsl.  F2E025ED → "movk", "x13, #0x12f, lsl #48".
                ld      bc, &F2E0
                ld      ix, &25ED
                call    disasm_entry
                ld      hl, DISASM_COMM_MNEM
                ld      de, disasm_stest_movk_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_movk
                ld      hl, DISASM_COMM_OPS
                ld      de, disasm_stest_movk_ops_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_movk

; load/store: scaled str.  F9003983 → "str", "x3, [x12, #112]".
                ld      bc, &F900
                ld      ix, &3983
                call    disasm_entry
                ld      hl, DISASM_COMM_MNEM
                ld      de, disasm_stest_str_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_str
                ld      hl, DISASM_COMM_OPS
                ld      de, disasm_stest_str_ops_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_str

; load/store pair: stp.  A9027BFD → "stp", "x29, x30, [sp, #32]".
                ld      bc, &A902
                ld      ix, &7BFD
                call    disasm_entry
                ld      hl, DISASM_COMM_MNEM
                ld      de, disasm_stest_stp_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_stp
                ld      hl, DISASM_COMM_OPS
                ld      de, disasm_stest_stp_ops_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_stp

; load/store byte: ldrb.  39432190 → "ldrb", "w16, [x12, #200]".
                ld      bc, &3943
                ld      ix, &2190
                call    disasm_entry
                ld      hl, DISASM_COMM_MNEM
                ld      de, disasm_stest_ldrb_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_ldrb
                ld      hl, DISASM_COMM_OPS
                ld      de, disasm_stest_ldrb_ops_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_ldrb

; load/store register offset: F8607843 → "ldr", "x3, [x2, x0, lsl #3]".
                ld      bc, &F860
                ld      ix, &7843
                call    disasm_entry
                ld      hl, DISASM_COMM_MNEM
                ld      de, disasm_stest_ldr_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_ldrro
                ld      hl, DISASM_COMM_OPS
                ld      de, disasm_stest_ldrro_ops_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_ldrro

                ld      bc, 0
                ret

disasm_stest_fail_nop:
                ld      bc, &7D
                ret
disasm_stest_fail_inst:
                ld      bc, &7E
                ret
disasm_stest_fail_udf:
                ld      bc, &7C
                ret
disasm_stest_fail_udf_ops:
                ld      bc, &7B
                ret
disasm_stest_fail_mov:
                ld      bc, &7A
                ret
disasm_stest_fail_movz:
                ld      bc, &79
                ret
disasm_stest_fail_movk:
                ld      bc, &78
                ret
disasm_stest_fail_str:
                ld      bc, &77
                ret
disasm_stest_fail_stp:
                ld      bc, &76
                ret
disasm_stest_fail_ldrb:
                ld      bc, &75
                ret
disasm_stest_fail_ldrro:
                ld      bc, &74
                ret

; disasm_stest_strcmp — compare null-terminated strings at (HL) and (DE).
; Returns Z set if equal, Z clear if not.  Clobbers A, HL, DE, F.
disasm_stest_strcmp:
                ld      a, (de)
                cp      (hl)
                ret     nz
                or      a               ; both bytes equal — at terminator?
                ret     z               ; Z set, equal up to and incl. the \0
                inc     hl
                inc     de
                jr      disasm_stest_strcmp

disasm_stest_udf_expect:    defm    "#4660"
                            defb    0
disasm_stest_udf0_expect:   defm    "#0"
                            defb    0
disasm_stest_udfmax_expect: defm    "#65535"
                            defb    0
disasm_stest_mov_expect:        defm    "mov"
                                defb    0
disasm_stest_mov_ops_expect:    defm    "x0, #0x80000000"
                                defb    0
disasm_stest_movz_expect:       defm    "movz"
                                defb    0
disasm_stest_movz_ops_expect:   defm    "x0, #0x0, lsl #16"
                                defb    0
disasm_stest_movk_expect:       defm    "movk"
                                defb    0
disasm_stest_movk_ops_expect:   defm    "x13, #0x12f, lsl #48"
                                defb    0
disasm_stest_str_expect:        defm    "str"
                                defb    0
disasm_stest_str_ops_expect:    defm    "x3, [x12, #112]"
                                defb    0
disasm_stest_stp_expect:        defm    "stp"
                                defb    0
disasm_stest_stp_ops_expect:    defm    "x29, x30, [sp, #32]"
                                defb    0
disasm_stest_ldrb_expect:       defm    "ldrb"
                                defb    0
disasm_stest_ldrb_ops_expect:   defm    "w16, [x12, #200]"
                                defb    0
disasm_stest_ldr_expect:        defm    "ldr"
                                defb    0
disasm_stest_ldrro_ops_expect:  defm    "x3, [x2, x0, lsl #3]"
                                defb    0
