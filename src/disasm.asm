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

; --- add/sub immediate (add/sub/adds/subs + cmp/cmn/mov-sp aliases) ----
                jp      disasm_try_addsubimm
disasm_not_addsubimm:

; --- logical immediate (and/orr/eor/ands + mov/tst aliases) -----------
                jp      disasm_try_logimm
disasm_not_logimm:

; --- dp-register aliases (mov/mvn/cmp/cmn/tst/neg/negs/ngc) ------------
; Per Go DecodeAt order the register-space aliases (decodeDPRegAlias,
; inside decodeAlias) run BEFORE the base decodeDPReg form walk, so they
; must shadow the base mnemonics.  The base decoder sits at the end of the
; chain (just before .inst), mirroring decodeDPReg running last in Go.
                jp      disasm_try_dpreg_alias
disasm_not_dpreg_alias:

; --- dp-register base (add/sub/adds/subs/and/orr/eor/ands/bic/orn/eon) -
; shifted + extended register forms.  Runs LAST before .inst (decodeDPReg
; is the final fallback after the form walk in Go).
                jp      disasm_try_dpreg
disasm_not_dpreg:

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
; Add/sub immediate family — Z80 port of aarch64dec decodeAddSubImmAlias
; (aliases.go:305).
;
; Encoding:  sf | op(1) | S(1) | 100010 | sh(1) | imm12(12) | Rn(5) | Rd(5)
;   op 0 → ADD/ADDS   op 1 → SUB/SUBS   S = set-flags
;
; Aliases objdump prefers:
;   S=1, Rd=31:  subs → cmp Rn, #imm ; adds → cmn Rn, #imm
;   S=0, op=0, sh=0, imm12=0, (Rd==31 || Rn==31): add → mov Rd, Rn  (sp form)
; Base forms: add/adds/sub/subs Rd, Rn, #imm[, lsl #12].
;   Rd and Rn are SP-able (sp/wsp at idx 31) for the non-flag forms; for
;   adds/subs Rd is the zero register (Rd=31 is the cmp/cmn case above).
;
; ABI: clobbers BC/IX on success; saves them after the LAST decline and
; restores via disasm_asi_done.  Decline paths leave B,C,D,E intact.
;
; Scratch (this page): disasm_asi_sf/op/s/sh/imm_hi/imm_lo/rn/rd.
; =======================================================================
disasm_try_addsubimm:
; Discriminator bits28:23 == 0b100010.  = ((B&0x1f)<<1)|(C>>7).
                ld      a, b
                and     &1f
                add     a, a                        ; (B&0x1f)<<1
                ld      l, a
                ld      a, c
                rlca                                ; C>>7 → bit0
                and     1
                or      l
                cp      &22
                jp      nz, disasm_not_addsubimm
; sf = B>>7.
                ld      a, b
                rlca
                and     1
                ld      (disasm_asi_sf), a
; op = (B>>6)&1.
                ld      a, b
                rlca
                rlca
                and     1
                ld      (disasm_asi_op), a
; S = (B>>5)&1.
                ld      a, b
                rlca
                rlca
                rlca
                and     1
                ld      (disasm_asi_s), a
; sh = (C>>6)&1.
                ld      a, c
                rlca
                rlca
                and     1
                ld      (disasm_asi_sh), a
; imm12 = ((C&0x3f)<<6)|(D>>2)  → hi:lo bytes (12-bit value).
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
                and     &3f                         ; D>>2
                or      l
                ld      l, a
                ld      a, l
                ld      (disasm_asi_imm_lo), a
                ld      a, h
                ld      (disasm_asi_imm_hi), a
; Rn = ((D&3)<<3)|(E>>5).
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
                ld      (disasm_asi_rn), a
; Rd = E&0x1f.
                ld      a, e
                and     &1f
                ld      (disasm_asi_rd), a

; Past the last decline — commit to success.  Save BC/IX (emit clobbers).
                push    bc
                push    ix

; --- cmp/cmn: S=1 && Rd==31 ---
                ld      a, (disasm_asi_s)
                or      a
                jr      z, disasm_asi_chk_mov
                ld      a, (disasm_asi_rd)
                cp      31
                jr      nz, disasm_asi_base
; cmn (op=0) / cmp (op=1).
                ld      a, (disasm_asi_op)
                or      a
                ld      hl, disasm_asi_cmn_txt
                jr      z, disasm_asi_cmp_set
                ld      hl, disasm_asi_cmp_txt
disasm_asi_cmp_set:
                call    disasm_asi_set_mnem
; operands: "<Rn>, #imm[, lsl #12]".  Rn is SP-able here? No — cmp/cmn use
; the zero-register name (Go decodeReg with zero).
                ld      hl, DISASM_COMM_OPS
                ld      a, (disasm_asi_rn)
                ld      c, a
                xor     a                           ; spable=0 (zr/wzr)
                call    disasm_asi_emit_reg
                jp      disasm_asi_emit_imm_tail

disasm_asi_chk_mov:
; --- mov Rd, Rn: S=0, op=0, sh=0, imm12=0, (Rd==31 || Rn==31) ---
                ld      a, (disasm_asi_op)
                or      a
                jr      nz, disasm_asi_base
                ld      a, (disasm_asi_sh)
                or      a
                jr      nz, disasm_asi_base
                ld      a, (disasm_asi_imm_hi)
                ld      l, a
                ld      a, (disasm_asi_imm_lo)
                or      l
                jr      nz, disasm_asi_base         ; imm12 != 0
                ld      a, (disasm_asi_rd)
                cp      31
                jr      z, disasm_asi_mov
                ld      a, (disasm_asi_rn)
                cp      31
                jr      nz, disasm_asi_base
disasm_asi_mov:
                ld      hl, disasm_asi_mov_txt
                call    disasm_asi_set_mnem
                ld      hl, DISASM_COMM_OPS
                ld      a, (disasm_asi_rd)
                ld      c, a
                ld      a, 1                        ; spable=1 (sp/wsp)
                call    disasm_asi_emit_reg
                ld      (hl), ","
                inc     hl
                ld      (hl), " "
                inc     hl
                ld      a, (disasm_asi_rn)
                ld      c, a
                ld      a, 1                        ; spable
                call    disasm_asi_emit_reg
                ld      (hl), 0
                jp      disasm_asi_done

; --- base add/adds/sub/subs ---
disasm_asi_base:
; mnem index = (op<<1)|S → 0 add, 1 adds, 2 sub, 3 subs.
                ld      a, (disasm_asi_op)
                add     a, a
                ld      l, a
                ld      a, (disasm_asi_s)
                or      l                           ; index 0..3
                add     a, a                        ; *2 (table of word ptrs)
                ld      e, a
                ld      d, 0
                ld      hl, disasm_asi_mnem_tbl
                add     hl, de
                ld      e, (hl)
                inc     hl
                ld      d, (hl)
                ex      de, hl                      ; HL = mnem string ptr
                call    disasm_asi_set_mnem
; Rd: SP-able for add/sub (S=0); zero-reg for adds/subs (S=1).
                ld      hl, DISASM_COMM_OPS
                ld      a, (disasm_asi_rd)
                ld      c, a
                ld      a, (disasm_asi_s)
                xor     1                           ; S=0 → spable=1 ; S=1 → 0
                call    disasm_asi_emit_reg
                ld      (hl), ","
                inc     hl
                ld      (hl), " "
                inc     hl
; Rn: always SP-able.
                ld      a, (disasm_asi_rn)
                ld      c, a
                ld      a, 1
                call    disasm_asi_emit_reg
; fall through to ", #imm[, lsl #12]" tail.
disasm_asi_emit_imm_tail:
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
                ld      a, (disasm_asi_imm_lo)
                ld      (disasm_mw_val), a
                ld      a, (disasm_asi_imm_hi)
                ld      (disasm_mw_val+1), a
                ld      a, 2
                call    disasm_mw_emit_hexbuf
                ld      a, (disasm_asi_sh)
                or      a
                jr      z, disasm_asi_imm_done
; ", lsl #12"
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
                ld      (hl), "1"
                inc     hl
                ld      (hl), "2"
                inc     hl
disasm_asi_imm_done:
                ld      (hl), 0
                ; fall through to disasm_asi_done.

disasm_asi_done:
                pop     ix
                pop     bc
                ret


; -----------------------------------------------------------------------
; disasm_asi_set_mnem — copy null-terminated mnemonic at (HL) to
; DISASM_COMM_MNEM.  Clobbers A, DE, HL.
; -----------------------------------------------------------------------
disasm_asi_set_mnem:
                ld      de, DISASM_COMM_MNEM
disasm_asi_sm_loop:
                ld      a, (hl)
                ld      (de), a
                or      a
                ret     z
                inc     hl
                inc     de
                jr      disasm_asi_sm_loop


; -----------------------------------------------------------------------
; disasm_asi_emit_reg — emit register C with width from disasm_asi_sf and
; zero/sp naming from A (0 → xzr/wzr ; 1 → sp/wsp).  Advances HL.
; Clobbers A, DE, F (IX/IY preserved by disasm_emit_dec16).
; -----------------------------------------------------------------------
disasm_asi_emit_reg:
                ld      (disasm_asi_spable), a
                ld      a, c
                cp      31
                jr      z, disasm_asi_reg_special
; ordinary reg: prefix + decimal index.
                ld      a, (disasm_asi_sf)
                or      a
                ld      a, "w"
                jr      z, disasm_asi_reg_pfx
                ld      a, "x"
disasm_asi_reg_pfx:
                ld      (hl), a
                inc     hl
                ld      a, c
                ld      e, a
                ld      d, 0
                call    disasm_emit_dec16
                ret
disasm_asi_reg_special:
                ld      a, (disasm_asi_spable)
                or      a
                jr      nz, disasm_asi_reg_sp
; zr form: "xzr"/"wzr".
                ld      a, (disasm_asi_sf)
                or      a
                ld      a, "w"
                jr      z, disasm_asi_reg_zp
                ld      a, "x"
disasm_asi_reg_zp:
                ld      (hl), a
                inc     hl
                ld      (hl), "z"
                inc     hl
                ld      (hl), "r"
                inc     hl
                ret
disasm_asi_reg_sp:
; sp form: "sp" (64) / "wsp" (32).
                ld      a, (disasm_asi_sf)
                or      a
                jr      nz, disasm_asi_reg_sp64
                ld      (hl), "w"
                inc     hl
disasm_asi_reg_sp64:
                ld      (hl), "s"
                inc     hl
                ld      (hl), "p"
                inc     hl
                ret


; --- add/sub-imm mnemonic strings + table (index (op<<1)|S) ------------
disasm_asi_add_txt:     defm    "add"
                        defb    0
disasm_asi_adds_txt:    defm    "adds"
                        defb    0
disasm_asi_sub_txt:     defm    "sub"
                        defb    0
disasm_asi_subs_txt:    defm    "subs"
                        defb    0
disasm_asi_cmp_txt:     defm    "cmp"
                        defb    0
disasm_asi_cmn_txt:     defm    "cmn"
                        defb    0
disasm_asi_mov_txt:     defm    "mov"
                        defb    0
disasm_asi_mnem_tbl:    defw    disasm_asi_add_txt  ; 0 add
                        defw    disasm_asi_adds_txt ; 1 adds
                        defw    disasm_asi_sub_txt  ; 2 sub
                        defw    disasm_asi_subs_txt ; 3 subs

; --- add/sub-imm working scratch (this page) --------------------------
disasm_asi_sf:          defb    0
disasm_asi_op:          defb    0
disasm_asi_s:           defb    0
disasm_asi_sh:          defb    0
disasm_asi_imm_hi:      defb    0
disasm_asi_imm_lo:      defb    0
disasm_asi_rn:          defb    0
disasm_asi_rd:          defb    0
disasm_asi_spable:      defb    0


; =======================================================================
; Logical immediate family — Z80 port of aarch64dec decodeLogicalImmAlias
; (aliases.go:570), the base and/orr/eor/ands logical-imm forms, plus the
; decodeBitMasks (slots_logical.go) N:immr:imms → bitmask expansion.
;
; Encoding:  sf | opc(2) | 100100 | N(1) | immr(6) | imms(6) | Rn(5) | Rd(5)
;   opc 00 → AND   01 → ORR   10 → EOR   11 → ANDS
; Aliases: orr Rn=31 → mov Rd, #imm  (unless moveWidePreferred → keep orr);
;          ands Rd=31 → tst Rn, #imm.
;
; decodeBitMasks rejects illegal encodings (esize>regsize, immr>=esize
; non-canonical, all-ones run) — those DECLINE to .inst.  Operand registers:
; Rd/Rn are SP-able for and/orr/eor (not flag-setting); Rd is zr for ands;
; Rn is always a zr-register source.  (Go decodeReg uses prefix/zero, never
; sp, for logical — but and/orr/eor allow SP as Rd.  Matches objdump.)
;
; ABI: clobbers BC/IX on success; saves after the LAST decline, restores via
; disasm_li_done.  Decline paths leave B,C,D,E intact.
; =======================================================================
disasm_try_logimm:
; Discriminator bits28:23 == 0b100100.  = ((B&0x1f)<<1)|(C>>7).
                ld      a, b
                and     &1f
                add     a, a
                ld      l, a
                ld      a, c
                rlca
                and     1
                or      l
                cp      &24
                jp      nz, disasm_not_logimm
; sf = B>>7.
                ld      a, b
                rlca
                and     1
                ld      (disasm_li_sf), a
; opc = (B>>5)&3.
                ld      a, b
                rlca
                rlca
                rlca
                and     3
                ld      (disasm_li_opc), a
; N = (C>>6)&1.
                ld      a, c
                rlca
                rlca
                and     1
                ld      (disasm_li_n), a
; immr = C&0x3f.
                ld      a, c
                and     &3f
                ld      (disasm_li_immr), a
; imms = (D>>2)&0x3f.
                ld      a, d
                rrca
                rrca
                and     &3f
                ld      (disasm_li_imms), a
; Rn = ((D&3)<<3)|(E>>5).
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
                ld      (disasm_li_rn), a
; Rd = E&0x1f.
                ld      a, e
                and     &1f
                ld      (disasm_li_rd), a

; For sf=0 (32-bit), N must be 0 (else UNDEFINED → decline).
                ld      a, (disasm_li_sf)
                or      a
                jr      nz, disasm_li_sf_ok
                ld      a, (disasm_li_n)
                or      a
                jp      nz, disasm_not_logimm
disasm_li_sf_ok:

; disasm_li_bitmasks and the emit code clobber BC/IX, so save them now
; (the discriminator/field-extraction above left B,C,D,E intact for the
; decline at the top).  On an illegal-encoding decline we must restore the
; word before falling through to .inst.
                push    bc
                push    ix

; Expand the bitmask into disasm_mw_val[0..7]; decline on failure.
                call    disasm_li_bitmasks
                jr      c, disasm_li_decoded
                pop     ix
                pop     bc
                jp      disasm_not_logimm           ; illegal encoding → .inst
disasm_li_decoded:

; --- orr (opc=01) with Rn=31 → mov, unless moveWidePreferred → keep orr ---
                ld      a, (disasm_li_opc)
                cp      1
                jr      nz, disasm_li_chk_tst
                ld      a, (disasm_li_rn)
                cp      31
                jr      nz, disasm_li_base
                call    disasm_li_movewide_pref     ; Z set if preferred
                jr      z, disasm_li_base           ; movz/movn-encodable → keep orr
; mov Rd, #imm.
                ld      hl, disasm_li_mov_txt
                call    disasm_asi_set_mnem
                ld      hl, DISASM_COMM_OPS
                ld      a, (disasm_li_rd)
                ld      c, a
                xor     a                           ; mov target uses zr naming
                call    disasm_li_emit_reg
                jp      disasm_li_emit_imm_tail

disasm_li_chk_tst:
; --- ands (opc=11) with Rd=31 → tst Rn, #imm ---
                ld      a, (disasm_li_opc)
                cp      3
                jr      nz, disasm_li_base
                ld      a, (disasm_li_rd)
                cp      31
                jr      nz, disasm_li_base
                ld      hl, disasm_li_tst_txt
                call    disasm_asi_set_mnem
                ld      hl, DISASM_COMM_OPS
                ld      a, (disasm_li_rn)
                ld      c, a
                xor     a                           ; Rn zr-source
                call    disasm_li_emit_reg
                jp      disasm_li_emit_imm_tail

; --- base and/orr/eor/ands ---
disasm_li_base:
                ld      a, (disasm_li_opc)
                add     a, a                        ; *2 (word ptrs)
                ld      e, a
                ld      d, 0
                ld      hl, disasm_li_mnem_tbl
                add     hl, de
                ld      e, (hl)
                inc     hl
                ld      d, (hl)
                ex      de, hl
                call    disasm_asi_set_mnem
; Rd: SP-able for and/orr/eor (opc != 11); zr for ands (opc 11).
                ld      hl, DISASM_COMM_OPS
                ld      a, (disasm_li_rd)
                ld      c, a
                ld      a, (disasm_li_opc)
                cp      3
                ld      a, 1                        ; spable for non-ands
                jr      nz, disasm_li_base_rd
                xor     a                           ; ands → zr
disasm_li_base_rd:
                call    disasm_li_emit_reg
                ld      (hl), ","
                inc     hl
                ld      (hl), " "
                inc     hl
; Rn: zr-register source (decodeReg with zero).
                ld      a, (disasm_li_rn)
                ld      c, a
                xor     a
                call    disasm_li_emit_reg
; fall through to ", #imm" tail.
disasm_li_emit_imm_tail:
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
; minimal-width hex of the mask: 8 bytes (sf=1) or 4 bytes (sf=0).
                ld      a, (disasm_li_sf)
                or      a
                ld      a, 4
                jr      z, disasm_li_imm_w
                ld      a, 8
disasm_li_imm_w:
                call    disasm_mw_emit_hexbuf
                ld      (hl), 0
                ; fall through to disasm_li_done.

disasm_li_done:
                pop     ix
                pop     bc
                ret


; -----------------------------------------------------------------------
; disasm_li_emit_reg — emit register C; width from disasm_li_sf; A selects
; zero naming: 0 → xzr/wzr ; 1 → sp/wsp (SP-able forms).  Advances HL.
; Clobbers A, DE, F.
; -----------------------------------------------------------------------
disasm_li_emit_reg:
                ld      (disasm_li_spable), a
                ld      a, c
                cp      31
                jr      z, disasm_li_reg_special
                ld      a, (disasm_li_sf)
                or      a
                ld      a, "w"
                jr      z, disasm_li_reg_pfx
                ld      a, "x"
disasm_li_reg_pfx:
                ld      (hl), a
                inc     hl
                ld      a, c
                ld      e, a
                ld      d, 0
                call    disasm_emit_dec16
                ret
disasm_li_reg_special:
                ld      a, (disasm_li_spable)
                or      a
                jr      nz, disasm_li_reg_sp
                ld      a, (disasm_li_sf)
                or      a
                ld      a, "w"
                jr      z, disasm_li_reg_zp
                ld      a, "x"
disasm_li_reg_zp:
                ld      (hl), a
                inc     hl
                ld      (hl), "z"
                inc     hl
                ld      (hl), "r"
                inc     hl
                ret
disasm_li_reg_sp:
                ld      a, (disasm_li_sf)
                or      a
                jr      nz, disasm_li_reg_sp64
                ld      (hl), "w"
                inc     hl
disasm_li_reg_sp64:
                ld      (hl), "s"
                inc     hl
                ld      (hl), "p"
                inc     hl
                ret


; -----------------------------------------------------------------------
; disasm_li_bitmasks — port of decodeBitMasks (slots_logical.go).
; Inputs (scratch): disasm_li_n, disasm_li_immr, disasm_li_imms, disasm_li_sf.
; On success: CARRY set, mask written little-endian to disasm_mw_val[0..7]
; (8 bytes; high 4 zero for sf=0).  On illegal encoding: CARRY clear.
; Clobbers A, BC, DE, HL.
;
; Algorithm:
;   combined = (N<<6) | (~imms & 0x3f) ; length = highest set bit (0..6).
;   length<1 invalid unless length==0 (esize 2).  esize = 1<<length.
;   esize>regsize invalid.  levels = esize-1.
;   immr & levels != immr → invalid (non-canonical).
;   s = imms & levels ; r = immr & levels ; s==levels invalid.
;   welem = (1<<(s+1))-1 masked to esize.  rotate right by r within esize.
;   replicate esize-bit pattern to regsize.
; -----------------------------------------------------------------------
disasm_li_bitmasks:
; combined = (N<<6) | (~imms & 0x3f).
                ld      a, (disasm_li_imms)
                cpl
                and     &3f
                ld      l, a
                ld      a, (disasm_li_n)
                or      a
                jr      z, disasm_li_bm_comb
                ld      a, l
                or      &40                         ; set bit6
                ld      l, a
disasm_li_bm_comb:
; L = combined (0..127).  length = highest set bit (scan 6..0).
                ld      a, l
                bit     6, a
                jr      nz, disasm_li_bm_len6
                bit     5, a
                jr      nz, disasm_li_bm_len5
                bit     4, a
                jr      nz, disasm_li_bm_len4
                bit     3, a
                jr      nz, disasm_li_bm_len3
                bit     2, a
                jr      nz, disasm_li_bm_len2
                bit     1, a
                jr      nz, disasm_li_bm_len1
                bit     0, a
                jr      nz, disasm_li_bm_len0
                ; all zero → length -1 → invalid.
                or      a                           ; clear carry
                ret
disasm_li_bm_len6:
                ld      a, 6
                jr      disasm_li_bm_haslen
disasm_li_bm_len5:
                ld      a, 5
                jr      disasm_li_bm_haslen
disasm_li_bm_len4:
                ld      a, 4
                jr      disasm_li_bm_haslen
disasm_li_bm_len3:
                ld      a, 3
                jr      disasm_li_bm_haslen
disasm_li_bm_len2:
                ld      a, 2
                jr      disasm_li_bm_haslen
disasm_li_bm_len1:
                ld      a, 1
                jr      disasm_li_bm_haslen
disasm_li_bm_len0:
                xor     a                           ; length 0
disasm_li_bm_haslen:
                ld      (disasm_li_length), a
; esize = 1 << length.  length is 0..6 → esize 1..64.
                ld      b, a                        ; B = length (shift count)
                ld      a, 1
                inc     b
                dec     b
                jr      z, disasm_li_bm_esize_set   ; length 0 → esize 1
disasm_li_bm_esize_loop:
                add     a, a
                djnz    disasm_li_bm_esize_loop
disasm_li_bm_esize_set:
                ld      (disasm_li_esize), a
; regsize = sf ? 64 : 32.  esize > regsize → invalid.
                ld      c, 32
                ld      a, (disasm_li_sf)
                or      a
                jr      z, disasm_li_bm_haveregsz
                ld      c, 64
disasm_li_bm_haveregsz:
                ld      a, (disasm_li_esize)
                cp      c
                jr      z, disasm_li_bm_esize_ok
                jr      c, disasm_li_bm_esize_ok
                or      a                           ; esize>regsize → clear carry
                ret
disasm_li_bm_esize_ok:
; levels = esize-1.
                ld      a, (disasm_li_esize)
                dec     a
                ld      (disasm_li_levels), a
; Non-canonical: immr & levels != immr → invalid.
                ld      a, (disasm_li_immr)
                ld      b, a
                ld      a, (disasm_li_levels)
                and     b                           ; immr & levels
                cp      b
                jr      z, disasm_li_bm_canon
                or      a                           ; differ → clear carry
                ret
disasm_li_bm_canon:
; s = imms & levels ; r = immr & levels (== immr, just verified).
                ld      a, (disasm_li_levels)
                ld      b, a
                ld      a, (disasm_li_imms)
                and     b
                ld      (disasm_li_s), a
; s == levels → invalid.
                ld      c, a
                ld      a, (disasm_li_levels)
                cp      c
                jr      nz, disasm_li_bm_s_ok
                or      a                           ; s==levels → clear carry
                ret
disasm_li_bm_s_ok:
                ld      a, (disasm_li_immr)
                ld      (disasm_li_r), a            ; r = immr (& levels == immr)
; Build welem in a 64-bit buffer disasm_li_welem[0..7] = (1<<(s+1))-1,
; i.e. (s+1) low bits set.  Then rotate-right by r within esize, then
; replicate to regsize, into disasm_mw_val[0..7].
                call    disasm_li_build_pattern
                scf
                ret


; -----------------------------------------------------------------------
; disasm_li_build_pattern — construct the rotated, replicated mask into
; disasm_mw_val[0..7] (little-endian, 64-bit; sf=0 leaves high zero — the
; replication naturally fills only regsize bits but we always fill 8 bytes
; and the renderer reads 4 for sf=0).
;
; Strategy: work bit-by-bit over the esize-bit element.  For element bit
; position j (0..esize-1), the rotated value's bit j = welem[(j+r) mod esize]
; where welem bit k = (k <= s).  Then replicate: dest bit (e*esize + j) =
; element bit j, for all e while e*esize < regsize.
;
; Implemented by computing, for each destination bit i in [0,regsize):
;   j = i mod esize ; src = (j + r) mod esize ; bit = (src <= s) ? 1 : 0.
; Set that bit in disasm_mw_val.  This is O(regsize) bit ops — fine here.
;
; Uses scratch: disasm_li_s, disasm_li_r, disasm_li_esize ; sf for regsize.
; Clobbers A, BC, DE, HL.
; -----------------------------------------------------------------------
disasm_li_build_pattern:
; zero disasm_mw_val[0..7].
                ld      hl, disasm_mw_val
                xor     a
                ld      b, 8
disasm_li_bp_clear:
                ld      (hl), a
                inc     hl
                djnz    disasm_li_bp_clear
; regsize → D.
                ld      a, (disasm_li_sf)
                or      a
                ld      a, 32
                jr      z, disasm_li_bp_havers
                ld      a, 64
disasm_li_bp_havers:
                ld      d, a                        ; D = regsize
; loop i = 0..regsize-1 in E.
                ld      e, 0
disasm_li_bp_loop:
                ld      a, e
                cp      d
                ret     nc                          ; i >= regsize → done
; j = i mod esize.
                ld      a, e
                ld      b, a                        ; B = i
                ld      a, (disasm_li_esize)
                ld      c, a                        ; C = esize
                ld      a, b
disasm_li_bp_jmod:
                cp      c
                jr      c, disasm_li_bp_have_j
                sub     c
                jr      disasm_li_bp_jmod
disasm_li_bp_have_j:
; A = j.  src = (j + r) mod esize.
                ld      b, a                        ; B = j
                ld      a, (disasm_li_r)
                add     a, b                        ; j + r
disasm_li_bp_srcmod:
                cp      c
                jr      c, disasm_li_bp_have_src
                sub     c
                jr      disasm_li_bp_srcmod
disasm_li_bp_have_src:
; A = src.  bit = (src <= s) ? 1 : 0.
                ld      b, a                        ; B = src
                ld      a, (disasm_li_s)
                cp      b                           ; s - src ; carry if s<src
                jr      c, disasm_li_bp_next        ; s<src → bit 0
; set bit i (E) in disasm_mw_val.  byte = i>>3, bit = i&7.
                ld      a, e
                rrca
                rrca
                rrca
                and     &1f                         ; i>>3 (0..7)
                ld      l, a
                ld      h, 0
                ld      bc, disasm_mw_val
                add     hl, bc                      ; HL = &val[i>>3]
                ld      a, e
                and     7                           ; bit index (0..7)
                ld      b, a
                ld      a, 1
                inc     b
                dec     b
                jr      z, disasm_li_bp_setbit      ; bit 0 → mask 1
disasm_li_bp_shl:
                add     a, a
                djnz    disasm_li_bp_shl
disasm_li_bp_setbit:
                or      (hl)
                ld      (hl), a
disasm_li_bp_next:
                inc     e
                jr      disasm_li_bp_loop


; -----------------------------------------------------------------------
; disasm_li_movewide_pref — port of moveWidePreferred (aliases.go:608).
; Z set (== true) when the (sf,N,imms,immr) logical-imm value is also a
; movz/movn-encodable immediate (so objdump keeps `orr`).  Z clear → not
; preferred (render the mov bitmask alias).
; Inputs (scratch): disasm_li_sf, disasm_li_n, disasm_li_imms, disasm_li_immr.
; Clobbers A, BC, DE, HL.
;
;   width = sf?64:32
;   sf=1 && N!=1 → false
;   sf=0 && !(N==0 && bit5(imms)==0) → false
;   s = imms ; r = immr
;   if s < 16:  return ((16 - (r mod 16)) mod 16) <= (15 - s)
;   if s >= width-15: return (r mod 16) <= (s - (width-16))
;   else false
; -----------------------------------------------------------------------
disasm_li_movewide_pref:
                ld      a, (disasm_li_sf)
                or      a
                jr      z, disasm_li_mwp_32
; sf=1: require N==1.
                ld      a, (disasm_li_n)
                cp      1
                jr      z, disasm_li_mwp_w64
                jp      disasm_li_mwp_false
disasm_li_mwp_w64:
                ld      a, 64
                ld      (disasm_li_mwp_width), a
                jr      disasm_li_mwp_body
disasm_li_mwp_32:
; sf=0: require N==0 && bit5(imms)==0.
                ld      a, (disasm_li_n)
                or      a
                jp      nz, disasm_li_mwp_false
                ld      a, (disasm_li_imms)
                bit     5, a
                jp      nz, disasm_li_mwp_false
                ld      a, 32
                ld      (disasm_li_mwp_width), a
disasm_li_mwp_body:
; s = imms, r = immr.
                ld      a, (disasm_li_imms)
                ld      (disasm_li_mwp_s), a
                cp      16
                jr      nc, disasm_li_mwp_shi
; s < 16: return ((16 - (r mod 16)) mod 16) <= (15 - s).
                ld      a, (disasm_li_immr)
                and     &0f                         ; r mod 16
                ld      b, a                        ; B = r%16
                ld      a, 16
                sub     b                           ; 16 - r%16  (1..16)
                and     &0f                         ; mod 16 → 0..15
                ld      b, a                        ; B = lhs
                ld      a, 15
                ld      c, a
                ld      a, (disasm_li_mwp_s)
                ld      e, a
                ld      a, c
                sub     e                           ; 15 - s
                ; compare lhs (B) <= (15-s) (A): true if B <= A → A-B no borrow.
                sub     b                           ; (15-s) - lhs
                jr      nc, disasm_li_mwp_true
                jr      disasm_li_mwp_false
disasm_li_mwp_shi:
; s >= 16.  Check s >= width-15.
                ld      a, (disasm_li_mwp_width)
                sub     15                          ; width-15
                ld      b, a
                ld      a, (disasm_li_mwp_s)
                cp      b
                jr      c, disasm_li_mwp_false      ; s < width-15 → false
; return (r mod 16) <= (s - (width-16)).
                ld      a, (disasm_li_mwp_width)
                sub     16                          ; width-16
                ld      b, a
                ld      a, (disasm_li_mwp_s)
                sub     b                           ; s - (width-16)
                ld      c, a                        ; C = rhs
                ld      a, (disasm_li_immr)
                and     &0f                         ; r mod 16
                cp      c
                jr      z, disasm_li_mwp_true
                jr      c, disasm_li_mwp_true       ; r%16 < rhs
                jr      disasm_li_mwp_false
disasm_li_mwp_true:
                xor     a                           ; Z set = true
                ret
disasm_li_mwp_false:
                or      1                           ; Z clear = false
                ret


; --- logical-imm mnemonic strings + table (index opc) -----------------
disasm_li_and_txt:      defm    "and"
                        defb    0
disasm_li_orr_txt:      defm    "orr"
                        defb    0
disasm_li_eor_txt:      defm    "eor"
                        defb    0
disasm_li_ands_txt:     defm    "ands"
                        defb    0
disasm_li_mov_txt:      defm    "mov"
                        defb    0
disasm_li_tst_txt:      defm    "tst"
                        defb    0
disasm_li_mnem_tbl:     defw    disasm_li_and_txt   ; opc 00 and
                        defw    disasm_li_orr_txt   ; opc 01 orr
                        defw    disasm_li_eor_txt   ; opc 10 eor
                        defw    disasm_li_ands_txt  ; opc 11 ands

; --- logical-imm working scratch (this page) --------------------------
disasm_li_sf:           defb    0
disasm_li_opc:          defb    0
disasm_li_n:            defb    0
disasm_li_immr:         defb    0
disasm_li_imms:         defb    0
disasm_li_rn:           defb    0
disasm_li_rd:           defb    0
disasm_li_spable:       defb    0
disasm_li_length:       defb    0
disasm_li_esize:        defb    0
disasm_li_levels:       defb    0
disasm_li_s:            defb    0
disasm_li_r:            defb    0
disasm_li_mwp_width:    defb    0
disasm_li_mwp_s:        defb    0


; =======================================================================
; Data-processing (shifted/extended register) family — Z80 port of
; tools/aarch64dec/dpreg.go (decodeDPReg) and the register-space aliases
; in tools/aarch64dec/aliases.go (decodeDPRegAlias).
;
; Class selector bits[28:24] (= ((B&0x1f)<<1)|(C>>7)):
;   0b01011 (0x0b) — arithmetic add/sub: bit21 (C bit5) picks shifted(0)/
;                    extended(1).
;   0b01010 (0x0a) — logical and/orr/eor/ands (+ bic/orn/eon via N bit);
;                    shifted only.
;
; Shifted-register fields:
;   sf=B>>7  opc=(B>>5)&3  shift=(C>>6)&3  N/bit21=(C>>5)&1  Rm=C&0x1f
;   imm6=(D>>2)&0x3f  Rn=((D&3)<<3)|(E>>5)  Rd=E&0x1f
; Extended-register additionally:  option=(D>>5)&7  imm3=(D>>2)&7
;
; Shared scratch (disasm_dpr_*) is filled by disasm_dpr_extract, which
; leaves B,C,D,E intact so the decline paths can still reach .inst with
; the original word.  Both the alias and base decoders call it.
; =======================================================================

; -----------------------------------------------------------------------
; disasm_dpr_extract — decode the common shifted/extended fields into
; scratch.  Sets disasm_dpr_cls (0x0a/0x0b/other).  Leaves B,C,D,E intact.
; Clobbers A, HL.
; -----------------------------------------------------------------------
disasm_dpr_extract:
; cls = bits[28:24] = B & 0x1f.
                ld      a, b
                and     &1f
                ld      (disasm_dpr_cls), a
; sf = B>>7.
                ld      a, b
                rlca
                and     1
                ld      (disasm_dpr_sf), a
; opc = (B>>5)&3.
                ld      a, b
                rlca
                rlca
                rlca
                and     3
                ld      (disasm_dpr_opc), a
; shift = (C>>6)&3.
                ld      a, c
                rlca
                rlca
                and     3
                ld      (disasm_dpr_shift), a
; bit21 (N) = (C>>5)&1.
                ld      a, c
                rlca
                rlca
                rlca
                and     1
                ld      (disasm_dpr_n), a
; Rm = C&0x1f.
                ld      a, c
                and     &1f
                ld      (disasm_dpr_rm), a
; imm6 = (D>>2)&0x3f.
                ld      a, d
                rrca
                rrca
                and     &3f
                ld      (disasm_dpr_imm6), a
; option = (D>>5)&7.
                ld      a, d
                rlca
                rlca
                rlca
                and     7
                ld      (disasm_dpr_option), a
; imm3 = (D>>2)&7.
                ld      a, d
                rrca
                rrca
                and     7
                ld      (disasm_dpr_imm3), a
; Rn = ((D&3)<<3)|(E>>5).
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
                ld      (disasm_dpr_rn), a
; Rd = E&0x1f.
                ld      a, e
                and     &1f
                ld      (disasm_dpr_rd), a
                ret


; -----------------------------------------------------------------------
; disasm_dpr_class_ok — A := class (cls in {0x0a,0x0b}); Z set iff this is
; a dp-register class word.  Clobbers A.
; -----------------------------------------------------------------------
disasm_dpr_class_ok:
                ld      a, (disasm_dpr_cls)
                cp      &0a
                ret     z
                cp      &0b
                ret


; -----------------------------------------------------------------------
; disasm_dpr_shifted_valid — apply the shifted-register validity guards
; (carry SET = valid, carry CLEAR = decline).  Mirrors decodeShiftedReg /
; decodeDPRegAlias guards:
;   - sf==0 && imm6>=32 → undefined.
;   - arithmetic (cls 0x0b) shift==0b11 (ror) → reserved.
; Clobbers A.
; -----------------------------------------------------------------------
disasm_dpr_shifted_valid:
                ld      a, (disasm_dpr_sf)
                or      a
                jr      nz, disasm_dpr_sv_chkror
                ld      a, (disasm_dpr_imm6)
                cp      32
                jr      c, disasm_dpr_sv_chkror
                or      a                           ; sf=0 && imm6>=32 → clear carry
                ret
disasm_dpr_sv_chkror:
                ld      a, (disasm_dpr_cls)
                cp      &0b
                jr      nz, disasm_dpr_sv_ok        ; logical: ror allowed
                ld      a, (disasm_dpr_shift)
                cp      3
                jr      nz, disasm_dpr_sv_ok
                or      a                           ; arith ror → clear carry
                ret
disasm_dpr_sv_ok:
                scf
                ret


; =======================================================================
; disasm_try_dpreg_alias — register-space aliases (Z80 port of
; decodeDPRegAlias, aliases.go:391).  Shifted-register only.
;
; Arithmetic (cls 0x0b, bit21==0):
;   opc 01 adds, Rd==31 → cmn Rn, Rm[, shift]
;   opc 10 sub,  Rn==31 → neg Rd, Rm[, shift]
;   opc 11 subs, Rd==31 → cmp Rn, Rm[, shift]
;            "    Rn==31 → negs Rd, Rm[, shift]
; Logical (cls 0x0a):
;   opc 01 N1 orn, Rn==31 → mvn Rd, Rm[, shift]
;   opc 01 N0 orr, Rn==31 && imm6==0 && shift==0 → mov Rd, Rm
;   opc 11 N0 ands, Rd==31 → tst Rn, Rm[, shift]
; All other words DECLINE to disasm_not_dpreg_alias (word intact).
;
; ABI: clobbers BC/IX on success; saves them after the LAST decline,
; restores via disasm_dpr_done.  Decline paths leave B,C,D,E intact.
; =======================================================================
disasm_try_dpreg_alias:
                call    disasm_dpr_extract
                call    disasm_dpr_class_ok
                jp      nz, disasm_not_dpreg_alias
; Arithmetic: bit21==1 is extended-register → no register aliases here.
                ld      a, (disasm_dpr_cls)
                cp      &0b
                jr      nz, disasm_dpra_have_class  ; logical: bit21 is N
                ld      a, (disasm_dpr_n)
                or      a
                jp      nz, disasm_not_dpreg_alias  ; arith extended → decline
disasm_dpra_have_class:
; Validity guards (same as base) — an undefined alias word must decline so
; .inst still matches.
                call    disasm_dpr_shifted_valid
                jp      nc, disasm_not_dpreg_alias
; Determine which alias (if any) fires.  We only COMMIT (push BC/IX) once an
; alias is confirmed, so the no-alias case can decline with the word intact.
                ld      a, (disasm_dpr_cls)
                cp      &0b
                jp      z, disasm_dpra_arith
; --- logical aliases (cls 0x0a) ---
                ld      a, (disasm_dpr_opc)
                cp      1
                jr      z, disasm_dpra_orr_orn
                cp      3
                jp      z, disasm_dpra_ands
                jp      disasm_not_dpreg_alias
disasm_dpra_orr_orn:
                ld      a, (disasm_dpr_n)
                or      a
                jr      nz, disasm_dpra_orn
; orr (N=0) → mov only when Rn==31 && imm6==0 && shift==0.
                ld      a, (disasm_dpr_rn)
                cp      31
                jp      nz, disasm_not_dpreg_alias
                ld      a, (disasm_dpr_imm6)
                or      a
                jp      nz, disasm_not_dpreg_alias
                ld      a, (disasm_dpr_shift)
                or      a
                jp      nz, disasm_not_dpreg_alias
; mov Rd, Rm.
                push    bc
                push    ix
                ld      hl, disasm_dpr_mov_txt
                call    disasm_asi_set_mnem
                ld      hl, DISASM_COMM_OPS
                ld      a, (disasm_dpr_rd)
                call    disasm_dpr_emit_reg
                ld      (hl), ","
                inc     hl
                ld      (hl), " "
                inc     hl
                ld      a, (disasm_dpr_rm)
                call    disasm_dpr_emit_reg
                ld      (hl), 0
                jp      disasm_dpr_done
disasm_dpra_orn:
; orn (N=1) → mvn when Rn==31.
                ld      a, (disasm_dpr_rn)
                cp      31
                jp      nz, disasm_not_dpreg_alias
                ld      hl, disasm_dpr_mvn_txt
                jp      disasm_dpra_emit_2op_rd     ; mvn Rd, Rm[, shift]
disasm_dpra_ands:
; ands (N must be 0) → tst when Rd==31.
                ld      a, (disasm_dpr_n)
                or      a
                jp      nz, disasm_not_dpreg_alias
                ld      a, (disasm_dpr_rd)
                cp      31
                jp      nz, disasm_not_dpreg_alias
                ld      hl, disasm_dpr_tst_txt
                jp      disasm_dpra_emit_2op_rn     ; tst Rn, Rm[, shift]

; --- arithmetic aliases (cls 0x0b, bit21==0) ---
disasm_dpra_arith:
                ld      a, (disasm_dpr_opc)
                cp      1
                jr      z, disasm_dpra_adds
                cp      2
                jr      z, disasm_dpra_sub
                cp      3
                jp      z, disasm_dpra_subs
                jp      disasm_not_dpreg_alias      ; opc 00 add → no alias
disasm_dpra_adds:
                ld      a, (disasm_dpr_rd)
                cp      31
                jp      nz, disasm_not_dpreg_alias
                ld      hl, disasm_dpr_cmn_txt
                jp      disasm_dpra_emit_2op_rn     ; cmn Rn, Rm[, shift]
disasm_dpra_sub:
                ld      a, (disasm_dpr_rn)
                cp      31
                jp      nz, disasm_not_dpreg_alias
                ld      hl, disasm_dpr_neg_txt
                jp      disasm_dpra_emit_2op_rd     ; neg Rd, Rm[, shift]
disasm_dpra_subs:
; subs: Rd==31 → cmp Rn, Rm ; else Rn==31 → negs Rd, Rm.
                ld      a, (disasm_dpr_rd)
                cp      31
                jr      nz, disasm_dpra_subs_neg
                ld      hl, disasm_dpr_cmp_txt
                jp      disasm_dpra_emit_2op_rn     ; cmp Rn, Rm[, shift]
disasm_dpra_subs_neg:
                ld      a, (disasm_dpr_rn)
                cp      31
                jp      nz, disasm_not_dpreg_alias
                ld      hl, disasm_dpr_negs_txt
                jp      disasm_dpra_emit_2op_rd     ; negs Rd, Rm[, shift]


; -----------------------------------------------------------------------
; disasm_dpra_emit_2op_rd / _rn — emit a two-register alias whose first
; operand is Rd (neg/negs/mvn) or Rn (cmp/cmn/tst): "<first>, <Rm>[, shift]".
; HL on entry → mnemonic string.  Commits (push BC/IX) then returns via
; disasm_dpr_done.
; -----------------------------------------------------------------------
disasm_dpra_emit_2op_rd:
                ld      a, (disasm_dpr_rd)
                jr      disasm_dpra_emit_2op
disasm_dpra_emit_2op_rn:
                ld      a, (disasm_dpr_rn)
disasm_dpra_emit_2op:
                ld      (disasm_dpr_first), a
                push    bc
                push    ix
                call    disasm_asi_set_mnem         ; mnemonic from HL
                ld      hl, DISASM_COMM_OPS
                ld      a, (disasm_dpr_first)
                call    disasm_dpr_emit_reg
                ld      (hl), ","
                inc     hl
                ld      (hl), " "
                inc     hl
                ld      a, (disasm_dpr_rm)
                call    disasm_dpr_emit_reg
                call    disasm_dpr_emit_shift       ; optional ", <kind> #imm6"
                ld      (hl), 0
                jp      disasm_dpr_done


; =======================================================================
; disasm_try_dpreg — base shifted/extended register decoder (Z80 port of
; decodeDPReg).  Runs LAST before .inst; the aliases above already claimed
; the alias words, so this emits the plain base mnemonic.
;
; ABI: clobbers BC/IX on success; saves them after the LAST decline,
; restores via disasm_dpr_done.  Decline paths leave B,C,D,E intact.
; =======================================================================
disasm_try_dpreg:
                call    disasm_dpr_extract
                call    disasm_dpr_class_ok
                jp      nz, disasm_not_dpreg
; Arithmetic with bit21==1 → extended register.
                ld      a, (disasm_dpr_cls)
                cp      &0b
                jr      nz, disasm_dpr_shifted      ; logical → always shifted
                ld      a, (disasm_dpr_n)
                or      a
                jp      nz, disasm_dpr_extended
; --- shifted register ---
disasm_dpr_shifted:
                call    disasm_dpr_shifted_valid
                jp      nc, disasm_not_dpreg
; Mnemonic: arithmetic by opc (add/adds/sub/subs); logical by opc+N.
                ld      a, (disasm_dpr_cls)
                cp      &0b
                jr      nz, disasm_dpr_sh_logical
; arithmetic: index opc into the add/sub table (reuse asi table).
                ld      a, (disasm_dpr_opc)
                add     a, a
                ld      e, a
                ld      d, 0
                ld      hl, disasm_asi_mnem_tbl
                add     hl, de
                ld      e, (hl)
                inc     hl
                ld      d, (hl)
                ex      de, hl
                jr      disasm_dpr_sh_emit
disasm_dpr_sh_logical:
; logical: opc*2 + N → index into disasm_dpr_log_tbl (8 word ptrs).
                ld      a, (disasm_dpr_opc)
                add     a, a                        ; opc*2
                ld      l, a
                ld      a, (disasm_dpr_n)
                or      l                           ; (opc<<1)|N
                add     a, a                        ; *2 word ptrs
                ld      e, a
                ld      d, 0
                ld      hl, disasm_dpr_log_tbl
                add     hl, de
                ld      e, (hl)
                inc     hl
                ld      d, (hl)
                ex      de, hl
disasm_dpr_sh_emit:
; HL = mnemonic string.  Commit and emit "Rd, Rn, Rm[, shift]".
                push    bc
                push    ix
                call    disasm_asi_set_mnem
                ld      hl, DISASM_COMM_OPS
                ld      a, (disasm_dpr_rd)
                call    disasm_dpr_emit_reg
                ld      (hl), ","
                inc     hl
                ld      (hl), " "
                inc     hl
                ld      a, (disasm_dpr_rn)
                call    disasm_dpr_emit_reg
                ld      (hl), ","
                inc     hl
                ld      (hl), " "
                inc     hl
                ld      a, (disasm_dpr_rm)
                call    disasm_dpr_emit_reg
                call    disasm_dpr_emit_shift
                ld      (hl), 0
                jp      disasm_dpr_done


; -----------------------------------------------------------------------
; Extended register (add/adds/sub/subs).  bits[23:22]==00 required; imm3<=4.
;   Rd/Rn are SP-able (sp/wsp at idx 31); Rm width = X for uxtx/sxtx
;   (option 011/111), else W.
;   lslOption = sf?011:010.  When option==lslOption && (Rd==31 || Rn==31):
;     imm3==0 → omit extend phrase ; imm3!=0 → ", lsl #imm3".
;   Otherwise emit ", <ext>" (imm3==0) or ", <ext> #imm3".
; -----------------------------------------------------------------------
disasm_dpr_extended:
; opt (bits[23:22]) must be 0.  shift scratch holds (C>>6)&3 = bits[23:22].
                ld      a, (disasm_dpr_shift)
                or      a
                jp      nz, disasm_not_dpreg
; imm3 > 4 → undefined.
                ld      a, (disasm_dpr_imm3)
                cp      5
                jp      nc, disasm_not_dpreg
; Mnemonic by opc (add/adds/sub/subs).
                ld      a, (disasm_dpr_opc)
                add     a, a
                ld      e, a
                ld      d, 0
                ld      hl, disasm_asi_mnem_tbl
                add     hl, de
                ld      e, (hl)
                inc     hl
                ld      d, (hl)
                ex      de, hl
                push    bc
                push    ix
                call    disasm_asi_set_mnem
; Rd (SP-able), Rn (SP-able).
                ld      hl, DISASM_COMM_OPS
                ld      a, (disasm_dpr_rd)
                call    disasm_dpr_emit_reg_sp
                ld      (hl), ","
                inc     hl
                ld      (hl), " "
                inc     hl
                ld      a, (disasm_dpr_rn)
                call    disasm_dpr_emit_reg_sp
                ld      (hl), ","
                inc     hl
                ld      (hl), " "
                inc     hl
; Rm: width depends on option (011/111 → x, else w).
                ld      a, (disasm_dpr_option)
                cp      3
                jr      z, disasm_dpr_ext_rm_x
                cp      7
                jr      z, disasm_dpr_ext_rm_x
                ld      a, (disasm_dpr_rm)
                call    disasm_dpr_emit_reg_w       ; force W width
                jr      disasm_dpr_ext_phrase
disasm_dpr_ext_rm_x:
                ld      a, (disasm_dpr_rm)
                call    disasm_dpr_emit_reg_x       ; force X width
disasm_dpr_ext_phrase:
; lslOption = sf?3:2.
                ld      a, (disasm_dpr_sf)
                or      a
                ld      a, 2
                jr      z, disasm_dpr_ext_haslsl
                ld      a, 3
disasm_dpr_ext_haslsl:
                ld      b, a                        ; B = lslOption
                ld      a, (disasm_dpr_option)
                cp      b
                jr      nz, disasm_dpr_ext_kw       ; not the lsl option → keyword
; option == lslOption: lsl branch when Rd==31 or Rn==31.
                ld      a, (disasm_dpr_rd)
                cp      31
                jr      z, disasm_dpr_ext_lsl
                ld      a, (disasm_dpr_rn)
                cp      31
                jr      nz, disasm_dpr_ext_kw       ; no SP → keyword form
disasm_dpr_ext_lsl:
; SP involved + lsl option.  imm3==0 → omit phrase; else ", lsl #imm3".
                ld      a, (disasm_dpr_imm3)
                or      a
                jr      z, disasm_dpr_ext_done
                ld      de, disasm_mem_lsl_txt      ; "lsl"
                call    disasm_dpr_emit_extamt
                jr      disasm_dpr_ext_done
disasm_dpr_ext_kw:
; keyword form.  imm3==0 → ", <ext>" ; else ", <ext> #imm3".  The ext
; keyword string ptr is fetched via disasm_dpr_ext_str (preserves HL).
                ld      a, (disasm_dpr_option)
                call    disasm_dpr_ext_str          ; DE = ext string ptr (HL kept)
                ld      a, (disasm_dpr_imm3)
                or      a
                jr      nz, disasm_dpr_ext_kwamt
; ", <ext>" (no amount).
                ld      (hl), ","
                inc     hl
                ld      (hl), " "
                inc     hl
                call    disasm_dpr_emit_str_de
                jr      disasm_dpr_ext_done
disasm_dpr_ext_kwamt:
                call    disasm_dpr_emit_extamt
disasm_dpr_ext_done:
                ld      (hl), 0
                jp      disasm_dpr_done


; -----------------------------------------------------------------------
; disasm_dpr_ext_str — A = option (0..7) → DE = pointer to the extend
; keyword string (uxtb..sxtx).  Preserves HL.  Clobbers A, DE.
; -----------------------------------------------------------------------
disasm_dpr_ext_str:
                push    hl
                add     a, a                        ; *2 word ptrs
                ld      e, a
                ld      d, 0
                ld      hl, disasm_dpr_ext_tbl
                add     hl, de
                ld      e, (hl)
                inc     hl
                ld      d, (hl)                      ; DE = ext string ptr
                pop     hl
                ret


; -----------------------------------------------------------------------
; disasm_dpr_emit_extamt — emit ", <kw> #imm3" where DE→keyword string and
; imm3 = disasm_dpr_imm3.  Advances HL.  Clobbers A, BC, DE.
; -----------------------------------------------------------------------
disasm_dpr_emit_extamt:
                push    de
                ld      (hl), ","
                inc     hl
                ld      (hl), " "
                inc     hl
                pop     de
                call    disasm_dpr_emit_str_de
                ld      (hl), " "
                inc     hl
                ld      (hl), "#"
                inc     hl
                ld      a, (disasm_dpr_imm3)
                ld      e, a
                ld      d, 0
                call    disasm_emit_dec16
                ret


; -----------------------------------------------------------------------
; disasm_dpr_emit_str_de — copy null-terminated string at (DE) to (HL),
; advancing HL.  Clobbers A, DE.
; -----------------------------------------------------------------------
disasm_dpr_emit_str_de:
                ld      a, (de)
                or      a
                ret     z
                ld      (hl), a
                inc     hl
                inc     de
                jr      disasm_dpr_emit_str_de


; -----------------------------------------------------------------------
; disasm_dpr_emit_shift — append ", <kind> #imm6" to (HL) when imm6 != 0.
; kind from disasm_dpr_shift (lsl/lsr/asr/ror).  Advances HL.
; Clobbers A, BC, DE.
; -----------------------------------------------------------------------
disasm_dpr_emit_shift:
                ld      a, (disasm_dpr_imm6)
                or      a
                ret     z
                ld      (hl), ","
                inc     hl
                ld      (hl), " "
                inc     hl
; kind string ptr from table (preserves HL), then copy + " #imm6".
                ld      a, (disasm_dpr_shift)
                push    hl
                add     a, a                        ; *2 word ptrs
                ld      e, a
                ld      d, 0
                ld      hl, disasm_dpr_shift_tbl
                add     hl, de
                ld      e, (hl)
                inc     hl
                ld      d, (hl)                      ; DE = kind string ptr
                pop     hl
                call    disasm_dpr_emit_str_de
                ld      (hl), " "
                inc     hl
                ld      (hl), "#"
                inc     hl
                ld      a, (disasm_dpr_imm6)
                ld      e, a
                ld      d, 0
                call    disasm_emit_dec16
                ret


; -----------------------------------------------------------------------
; disasm_dpr_emit_reg / _sp / _w / _x — emit register index A.
;   _reg : width from disasm_dpr_sf, zero index → xzr/wzr.
;   _sp  : width from disasm_dpr_sf, zero index → sp/wsp (SP-able).
;   _w   : force W width, zero index → wzr.
;   _x   : force X width, zero index → xzr.
; Advances HL.  Clobbers A, DE (IX/IY preserved by disasm_emit_dec16).
; -----------------------------------------------------------------------
disasm_dpr_emit_reg:
                ld      c, a                        ; C = index
                ld      a, (disasm_dpr_sf)
                ld      b, a                        ; B = is64
                xor     a
                jr      disasm_dpr_reg_emit         ; spable=0 → zr/wzr
disasm_dpr_emit_reg_sp:
                ld      c, a
                ld      a, (disasm_dpr_sf)
                ld      b, a
                ld      a, 1                        ; spable=1 → sp/wsp
                jr      disasm_dpr_reg_emit
disasm_dpr_emit_reg_w:
                ld      c, a
                ld      b, 0                        ; force W
                xor     a
                jr      disasm_dpr_reg_emit
disasm_dpr_emit_reg_x:
                ld      c, a
                ld      b, 1                        ; force X
                xor     a
                ; fall through
; B = is64, C = index, A = spable (0=zr/1=sp naming for idx 31).
disasm_dpr_reg_emit:
                ld      (disasm_dpr_spable), a
                ld      a, c
                cp      31
                jr      z, disasm_dpr_reg_special
; ordinary register: prefix + decimal.
                ld      a, b
                or      a
                ld      a, "w"
                jr      z, disasm_dpr_reg_pfx
                ld      a, "x"
disasm_dpr_reg_pfx:
                ld      (hl), a
                inc     hl
                ld      a, c
                ld      e, a
                ld      d, 0
                call    disasm_emit_dec16
                ret
disasm_dpr_reg_special:
                ld      a, (disasm_dpr_spable)
                or      a
                jr      nz, disasm_dpr_reg_sp
; zr form: "xzr"/"wzr".
                ld      a, b
                or      a
                ld      a, "w"
                jr      z, disasm_dpr_reg_zp
                ld      a, "x"
disasm_dpr_reg_zp:
                ld      (hl), a
                inc     hl
                ld      (hl), "z"
                inc     hl
                ld      (hl), "r"
                inc     hl
                ret
disasm_dpr_reg_sp:
; sp form: "sp" (64) / "wsp" (32).
                ld      a, b
                or      a
                jr      nz, disasm_dpr_reg_sp64
                ld      (hl), "w"
                inc     hl
disasm_dpr_reg_sp64:
                ld      (hl), "s"
                inc     hl
                ld      (hl), "p"
                inc     hl
                ret


; -----------------------------------------------------------------------
; disasm_dpr_done — common success epilogue: restore BC/IX (saved after
; the last decline) and return.  Honours disasm_entry's ABI.
; -----------------------------------------------------------------------
disasm_dpr_done:
                pop     ix
                pop     bc
                ret


; --- dp-register mnemonic strings -------------------------------------
disasm_dpr_mov_txt:     defm    "mov"
                        defb    0
disasm_dpr_mvn_txt:     defm    "mvn"
                        defb    0
disasm_dpr_cmp_txt:     defm    "cmp"
                        defb    0
disasm_dpr_cmn_txt:     defm    "cmn"
                        defb    0
disasm_dpr_tst_txt:     defm    "tst"
                        defb    0
disasm_dpr_neg_txt:     defm    "neg"
                        defb    0
disasm_dpr_negs_txt:    defm    "negs"
                        defb    0
disasm_dpr_and_txt:     defm    "and"
                        defb    0
disasm_dpr_bic_txt:     defm    "bic"
                        defb    0
disasm_dpr_orr_txt:     defm    "orr"
                        defb    0
disasm_dpr_orn_txt:     defm    "orn"
                        defb    0
disasm_dpr_eor_txt:     defm    "eor"
                        defb    0
disasm_dpr_eon_txt:     defm    "eon"
                        defb    0
disasm_dpr_ands_txt:    defm    "ands"
                        defb    0
disasm_dpr_bics_txt:    defm    "bics"
                        defb    0
disasm_dpr_lsl_txt:     defm    "lsl"
                        defb    0
disasm_dpr_lsr_txt:     defm    "lsr"
                        defb    0
disasm_dpr_asr_txt:     defm    "asr"
                        defb    0
disasm_dpr_ror_txt:     defm    "ror"
                        defb    0
disasm_dpr_uxtb_txt:    defm    "uxtb"
                        defb    0
disasm_dpr_uxth_txt:    defm    "uxth"
                        defb    0
disasm_dpr_uxtw_txt:    defm    "uxtw"
                        defb    0
disasm_dpr_uxtx_txt:    defm    "uxtx"
                        defb    0
disasm_dpr_sxtb_txt:    defm    "sxtb"
                        defb    0
disasm_dpr_sxth_txt:    defm    "sxth"
                        defb    0
disasm_dpr_sxtw_txt:    defm    "sxtw"
                        defb    0
disasm_dpr_sxtx_txt:    defm    "sxtx"
                        defb    0

; logical shifted-register mnemonic table, indexed (opc<<1)|N (8 entries):
;   opc00 and/bic  opc01 orr/orn  opc10 eor/eon  opc11 ands/bics
disasm_dpr_log_tbl:     defw    disasm_dpr_and_txt  ; 000 and
                        defw    disasm_dpr_bic_txt  ; 001 bic
                        defw    disasm_dpr_orr_txt  ; 010 orr
                        defw    disasm_dpr_orn_txt  ; 011 orn
                        defw    disasm_dpr_eor_txt  ; 100 eor
                        defw    disasm_dpr_eon_txt  ; 101 eon
                        defw    disasm_dpr_ands_txt ; 110 ands
                        defw    disasm_dpr_bics_txt ; 111 bics

; shift-kind table (indexed by 2-bit shift field): lsl/lsr/asr/ror.
disasm_dpr_shift_tbl:   defw    disasm_dpr_lsl_txt
                        defw    disasm_dpr_lsr_txt
                        defw    disasm_dpr_asr_txt
                        defw    disasm_dpr_ror_txt

; extend-kind table (indexed by 3-bit option field): uxtb..sxtx.
disasm_dpr_ext_tbl:     defw    disasm_dpr_uxtb_txt
                        defw    disasm_dpr_uxth_txt
                        defw    disasm_dpr_uxtw_txt
                        defw    disasm_dpr_uxtx_txt
                        defw    disasm_dpr_sxtb_txt
                        defw    disasm_dpr_sxth_txt
                        defw    disasm_dpr_sxtw_txt
                        defw    disasm_dpr_sxtx_txt

; --- dp-register working scratch (this page) --------------------------
disasm_dpr_cls:         defb    0       ; class 0x0a/0x0b/other
disasm_dpr_sf:          defb    0
disasm_dpr_opc:         defb    0
disasm_dpr_shift:       defb    0       ; shift field (also opt bits23:22)
disasm_dpr_n:           defb    0       ; N / bit21
disasm_dpr_rm:          defb    0
disasm_dpr_imm6:        defb    0
disasm_dpr_option:      defb    0       ; extend option bits15:13
disasm_dpr_imm3:        defb    0       ; extend amount bits12:10
disasm_dpr_rn:          defb    0
disasm_dpr_rd:          defb    0
disasm_dpr_first:       defb    0       ; first operand reg for 2-op aliases
disasm_dpr_spable:      defb    0


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
;   &73 — add/sub-imm add check failed (mnemonic or operands).
;   &72 — add/sub-imm cmp check failed (mnemonic or operands).
;   &71 — logical-imm mov (orr bitmask alias) check failed.
;   &70 — logical-imm and check failed (mnemonic or operands).
;   &6F — logical-imm non-canonical → .inst check failed.
;   &6E — dp-register shifted add (lsl #n) check failed.
;   &6D — dp-register mov (orr-xzr alias) check failed.
;   &6C — dp-register cmp (subs-xzr alias) check failed.
;   &6B — dp-register extended add (uxtw) check failed.
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

; add/sub immediate: kept add.  91000000 → "add", "x0, x0, #0x0".
                ld      bc, &9100
                ld      ix, &0000
                call    disasm_entry
                ld      hl, DISASM_COMM_MNEM
                ld      de, disasm_stest_add_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_addi
                ld      hl, DISASM_COMM_OPS
                ld      de, disasm_stest_add_ops_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_addi

; add/sub immediate: cmp alias.  F100301F → "cmp", "x0, #0xc".
                ld      bc, &F100
                ld      ix, &301F
                call    disasm_entry
                ld      hl, DISASM_COMM_MNEM
                ld      de, disasm_stest_cmp_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_cmpi
                ld      hl, DISASM_COMM_OPS
                ld      de, disasm_stest_cmp_ops_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_cmpi

; logical immediate: orr-bitmask mov alias.  B202E7EF → "mov",
; "x15, #0xcccccccccccccccc".
                ld      bc, &B202
                ld      ix, &E7EF
                call    disasm_entry
                ld      hl, DISASM_COMM_MNEM
                ld      de, disasm_stest_limov_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_limov
                ld      hl, DISASM_COMM_OPS
                ld      de, disasm_stest_limov_ops_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_limov

; logical immediate: and base.  927E0400 → "and", "x0, x0, #0xc".
                ld      bc, &927E
                ld      ix, &0400
                call    disasm_entry
                ld      hl, DISASM_COMM_MNEM
                ld      de, disasm_stest_and_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_andi
                ld      hl, DISASM_COMM_OPS
                ld      de, disasm_stest_and_ops_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_andi

; logical immediate: non-canonical immr (immr>=esize) → .inst.
; 32200013 = orr w19,w0,#0x1 with esize=32, immr=32 → decodeBitMasks
; rejects (immr & levels != immr) → ".inst", "0x32200013".
                ld      bc, &3220
                ld      ix, &0013
                call    disasm_entry
                ld      hl, DISASM_COMM_MNEM
                ld      de, disasm_stest_inst_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_noncanon
                ld      hl, DISASM_COMM_OPS
                ld      de, disasm_stest_noncanon_ops_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_noncanon

; dp-register shifted add with lsl.  8B100A10 → "add", "x16, x16, x16, lsl #2".
                ld      bc, &8B10
                ld      ix, &0A10
                call    disasm_entry
                ld      hl, DISASM_COMM_MNEM
                ld      de, disasm_stest_dadd_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_dadd
                ld      hl, DISASM_COMM_OPS
                ld      de, disasm_stest_dadd_ops_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_dadd

; dp-register mov (orr-xzr alias).  AA0103E2 → "mov", "x2, x1".
                ld      bc, &AA01
                ld      ix, &03E2
                call    disasm_entry
                ld      hl, DISASM_COMM_MNEM
                ld      de, disasm_stest_dmov_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_dmov
                ld      hl, DISASM_COMM_OPS
                ld      de, disasm_stest_dmov_ops_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_dmov

; dp-register cmp (subs-xzr alias).  EB01001F → "cmp", "x0, x1".
                ld      bc, &EB01
                ld      ix, &001F
                call    disasm_entry
                ld      hl, DISASM_COMM_MNEM
                ld      de, disasm_stest_dcmp_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_dcmp
                ld      hl, DISASM_COMM_OPS
                ld      de, disasm_stest_dcmp_ops_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_dcmp

; dp-register extended add.  8B234041 → "add", "x1, x2, w3, uxtw".
                ld      bc, &8B23
                ld      ix, &4041
                call    disasm_entry
                ld      hl, DISASM_COMM_MNEM
                ld      de, disasm_stest_dext_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_dext
                ld      hl, DISASM_COMM_OPS
                ld      de, disasm_stest_dext_ops_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_dext

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
disasm_stest_fail_addi:
                ld      bc, &73
                ret
disasm_stest_fail_cmpi:
                ld      bc, &72
                ret
disasm_stest_fail_limov:
                ld      bc, &71
                ret
disasm_stest_fail_andi:
                ld      bc, &70
                ret
disasm_stest_fail_noncanon:
                ld      bc, &6F
                ret
disasm_stest_fail_dadd:
                ld      bc, &6E
                ret
disasm_stest_fail_dmov:
                ld      bc, &6D
                ret
disasm_stest_fail_dcmp:
                ld      bc, &6C
                ret
disasm_stest_fail_dext:
                ld      bc, &6B
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
disasm_stest_add_expect:        defm    "add"
                                defb    0
disasm_stest_add_ops_expect:    defm    "x0, x0, #0x0"
                                defb    0
disasm_stest_cmp_expect:        defm    "cmp"
                                defb    0
disasm_stest_cmp_ops_expect:    defm    "x0, #0xc"
                                defb    0
disasm_stest_limov_expect:      defm    "mov"
                                defb    0
disasm_stest_limov_ops_expect:  defm    "x15, #0xcccccccccccccccc"
                                defb    0
disasm_stest_and_expect:        defm    "and"
                                defb    0
disasm_stest_and_ops_expect:    defm    "x0, x0, #0xc"
                                defb    0
disasm_stest_inst_expect:       defm    ".inst"
                                defb    0
disasm_stest_noncanon_ops_expect: defm  "0x32200013"
                                defb    0
disasm_stest_dadd_expect:       defm    "add"
                                defb    0
disasm_stest_dadd_ops_expect:   defm    "x16, x16, x16, lsl #2"
                                defb    0
disasm_stest_dmov_expect:       defm    "mov"
                                defb    0
disasm_stest_dmov_ops_expect:   defm    "x2, x1"
                                defb    0
disasm_stest_dcmp_expect:       defm    "cmp"
                                defb    0
disasm_stest_dcmp_ops_expect:   defm    "x0, x1"
                                defb    0
disasm_stest_dext_expect:       defm    "add"
                                defb    0
disasm_stest_dext_ops_expect:   defm    "x1, x2, w3, uxtw"
                                defb    0
