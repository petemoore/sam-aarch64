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
; See https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/notes/2026-06-07-disassembler-page-placement.md for the
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
if defined(BUILD_TESTS)
                jp      run_disasm_self_test    ; &8003  DISASM_SELF_TEST_ENTRY
endif

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

; --- System instruction group (mrs/msr/dc/at/ic/tlbi/barriers/eret/wfi) -
; aarch64dec decodeSys (sys.go) runs second in DecodeAt (after the mem
; family).  Its encoding space (bits[31:22]==0b1101010100, plus the exact
; eret/wfi words) is disjoint from every other ported family, so trying it
; here is order-faithful.  It declines (B,C,D,E intact) for non-system
; words and for the hint sub-space (nop is special-cased above; wfi is the
; only other corpus hint and is matched exactly here).
                jp      disasm_try_sys
disasm_not_sys:

; --- test-and-branch (tbz/tbnz) ---------------------------------------
; aarch64dec decodeTestBranch (tbranch.go) runs ahead of udf/aliases on the
; Go side.  bits[30:25] == 0b011011.  Disjoint from NOP/UDF/move-wide, so
; trying it here is order-faithful and harmless.
                jp      disasm_try_tbranch
disasm_not_tbranch:

; --- UDF (bits[31:16] == 0) -------------------------------------------
; aarch64dec disasm.go:77 — `if word>>16 == 0 { "udf", "#<dec imm16>" }`.
; Top 16 bits are B and C; both zero ⇒ UDF.  imm16 = DE.
                ld      a, b
                or      c
                jr      nz, disasm_not_udf
                jp      disasm_udf
disasm_not_udf:

; --- branch + PC-relative families (b/bl/b.cond/cbz/cbnz/adr/adrp +
;     ret/br/blr) — the AllForms() branch/adr/adrp forms in the Go form
;     walk, plus the register-branch forms.  Each is a disjoint encoding
;     space; trying them here is order-faithful (the form walk runs after
;     aliases in Go, but no alias claims these spaces).  PC-relative
;     families read DISASM_COMM_PC.
                jp      disasm_try_branch
disasm_not_branch:

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

; --- bitfield (ubfm/sbfm/bfm + lsr/lsl/asr/ror/ubfx/sbfx/ubfiz/sbfiz/
;     bfi/bfxil/bfc/uxtb/uxth/sxtb/sxth/sxtw aliases) ------------------
; aarch64dec decodeBitfieldAlias (aliases.go:186) inside decodeAlias —
; runs before the form walk and shadows the base UBFM/SBFM/BFM.
                jp      disasm_try_bitfield
disasm_not_bitfield:

; --- conditional compare (ccmp/ccmn, immediate + register forms) -------
; aarch64dec form walk: ccmp (ID 88) / ccmn (ID 100) in AllForms().  This
; encoding space is disjoint from conditional-select (bits[28:21] = 0xd2
; here vs 0xd4 there), so ordering relative to condsel is harmless.
                jp      disasm_try_ccmp
disasm_not_ccmp:

; --- conditional select (csel/csinc/csinv/csneg + cset/csetm/cinc/cinv/
;     cneg aliases) ----------------------------------------------------
; aarch64dec decodeCondSelAlias (aliases.go:502) plus the base csel/csinc/
; csinv/csneg forms.  Inside decodeAlias on the Go side.
                jp      disasm_try_condsel
disasm_not_condsel:

; --- data-processing (3-source) multiply (madd/msub/mul/mneg/smaddl/
;     umaddl/smsubl/umsubl/smulh/umulh/smull/umull/smnegl/umnegl) ------
; aarch64dec decodeMul3Source (aliases.go:649) inside decodeAlias.
                jp      disasm_try_mul3
disasm_not_mul3:

; --- data-processing (2-source) variable shift (lslv/lsrv/asrv/rorv →
;     lsl/lsr/asr/ror register form) -----------------------------------
; aarch64dec decodeShiftVarAlias (aliases.go:823) inside decodeAlias.
                jp      disasm_try_shiftvar
disasm_not_shiftvar:

; --- EXTR / ROR-immediate (extract register, 32- or 64-bit) -----------
; aarch64dec tryDecodeExtr (aliases.go:778) inside decodeAlias; runs
; after decodeMovk and before decodeAlias returns.  Rm==Rn → ror alias.
                jp      disasm_try_extr
disasm_not_extr:

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
; Branch + PC-relative families — Z80 port of the AllForms() branch / adr /
; adrp forms plus the register-branch forms and decodeTestBranch:
;   b, bl            (imm26)   tools/aarch64dec branchtarget.go / data.go
;   b.<cond>         (imm19)   data.go MnemonicID 26..41 + format.CondCode
;   cbz, cbnz        (imm19)   data.go MnemonicID 20/21
;   tbz, tbnz        (imm14)   tools/aarch64dec/tbranch.go
;   adr              (imm21)   manual_forms.go MnemonicID 48 / slots_branch.go
;   adrp             (imm21)   data.go MnemonicID 13 / slots_branch.go
;   ret, br, blr     (Rn)      manual_forms.go / data.go (no PC target)
;   ldr (literal)    (imm19)   tools/aarch64dec/mem.go decodeLiteralMem
;
; The PC-relative families render the ABSOLUTE target address (DecodeAt's
; raw operand string, `0x<minimal-width-hex>`) computed as in the Go side:
;   imm26/19/14 branches: target = pc + (sext(imm) << 2)
;   adr:                  target = pc + sext(imm21)
;   adrp:                 target = (pc & ~0xfff) + (sext(imm21) << 12)
;   ldr (literal):        target = pc + (sext(imm19) << 2)
; pc is the 8-byte little-endian value at DISASM_COMM_PC.
;
; ABI: clobbers BC/IX on success; saves after the LAST decline and restores
; via disasm_br_done.  Decline paths leave B,C,D,E intact (the field
; extraction writes only this page's scratch).
; =======================================================================

; -----------------------------------------------------------------------
; disasm_br_save_word / restore — stash the 4 word bytes (B,C,D,E) to
; scratch so the field arithmetic below can use all registers freely while
; the decline contract (B,C,D,E intact for .inst) is honoured.  The branch
; decoders read the word ONLY from these scratch bytes after saving.
; -----------------------------------------------------------------------
disasm_br_save_word:
                ld      a, b
                ld      (disasm_br_wb), a
                ld      a, c
                ld      (disasm_br_wc), a
                ld      a, d
                ld      (disasm_br_wd), a
                ld      a, e
                ld      (disasm_br_we), a
                ret


; -----------------------------------------------------------------------
; disasm_try_tbranch — tbz/tbnz.  bits[30:25] == 0b011011 (= (B>>1)&0x3f).
; Renders `tbz/tbnz <reg>, #<bitpos-dec>, 0x<target>`.
; -----------------------------------------------------------------------
disasm_try_tbranch:
                ld      a, b
                rrca
                and     &3f
                cp      &1b                         ; 0b011011
                jp      nz, disasm_not_tbranch
                call    disasm_br_save_word
; op = wb bit0 (0 tbz / 1 tbnz).
                ld      a, (disasm_br_wb)
                and     1
                ld      (disasm_br_op), a
; b5 = wb bit7 → is64.
                ld      a, (disasm_br_wb)
                rlca
                and     1
                ld      (disasm_br_is64), a
; b40 = (wc>>3)&0x1f.
                ld      a, (disasm_br_wc)
                rrca
                rrca
                rrca
                and     &1f
                ld      l, a
; bitpos = (b5<<5)|b40.
                ld      a, (disasm_br_is64)
                add     a, a
                add     a, a
                add     a, a
                add     a, a
                add     a, a                        ; b5<<5
                or      l
                ld      (disasm_br_bit), a
; Rt = we&0x1f.
                ld      a, (disasm_br_we)
                and     &1f
                ld      (disasm_br_rt), a
; imm14 = bits[18:5] = ((wc&7)<<11)|(wd<<3)|(we>>5)  → disasm_br_off (LE).
                ld      a, (disasm_br_wc)
                and     7
                ld      h, a
                ld      l, 0
                add     hl, hl
                add     hl, hl
                add     hl, hl                      ; (wc&7)<<11
                ld      a, (disasm_br_wd)
                ld      e, a
                ld      d, 0
                ex      de, hl
                add     hl, hl
                add     hl, hl
                add     hl, hl                      ; wd<<3
                ex      de, hl                      ; DE = wd<<3, HL = (wc&7)<<11
                add     hl, de                      ; HL = ((wc&7)<<11)|(wd<<3)
                ld      a, (disasm_br_we)
                rlca
                rlca
                rlca
                and     7                           ; we>>5
                or      l
                ld      l, a
                ld      (disasm_br_off), hl
                call    disasm_br_off_zero_hi
; Sign-extend imm14 (width 14), shift left 2, add pc → disasm_mw_val.
                ld      a, 14
                ld      (disasm_br_width), a
                ld      a, 2
                ld      (disasm_br_shift), a
                xor     a
                ld      (disasm_br_pgmask), a         ; not adrp
                call    disasm_br_compute_target
; mnemonic.
                ld      hl, disasm_br_tbz_txt
                ld      a, (disasm_br_op)
                or      a
                jr      z, disasm_tbr_setm
                ld      hl, disasm_br_tbnz_txt
disasm_tbr_setm:
                call    disasm_asi_set_mnem
; operands: "<reg>, #<bit>, 0x<target>".
                ld      hl, DISASM_COMM_OPS
                ld      a, (disasm_br_rt)
                ld      c, a
                ld      a, (disasm_br_is64)
                ld      b, a
                call    disasm_br_emit_reg          ; <reg> (zr at idx 31)
                ld      (hl), ","
                inc     hl
                ld      (hl), " "
                inc     hl
                ld      (hl), "#"
                inc     hl
                ld      a, (disasm_br_bit)
                ld      e, a
                ld      d, 0
                call    disasm_emit_dec16           ; bitpos (decimal)
                ld      (hl), ","
                inc     hl
                ld      (hl), " "
                inc     hl
                call    disasm_br_emit_target       ; "0x<hex target>"
                ld      (hl), 0
                jp      disasm_br_done


; -----------------------------------------------------------------------
; disasm_try_branch — b/bl, b.<cond>, cbz/cbnz, adr/adrp, ret/br/blr.
; Each sub-family is a distinct discriminator; words matching none fall
; through to disasm_not_branch with B,C,D,E intact.
; -----------------------------------------------------------------------
disasm_try_branch:
; --- ret/br/blr: B == 0xd6 -------------------------------------------
                ld      a, b
                cp      &d6
                jp      z, disasm_br_reg
; --- b (bits31:26 == 0b000101 = 0x05) --------------------------------
                ld      a, b
                rrca
                rrca
                and     &3f
                cp      &05
                jp      z, disasm_br_b
                cp      &25                         ; bl  (0b100101)
                jp      z, disasm_br_bl
; --- cbz/cbnz/b.cond: discriminated on full B (mask 0xff) ------------
                ld      a, b
                cp      &34
                jp      z, disasm_br_cbz
                cp      &b4
                jp      z, disasm_br_cbz
                cp      &35
                jp      z, disasm_br_cbnz
                cp      &b5
                jp      z, disasm_br_cbnz
                cp      &54
                jp      z, disasm_br_bcond
; --- adr/adrp: bit31=0/1, bits28:24 == 0b10000 -----------------------
                ld      a, b
                and     &1f
                cp      &10
                jp      nz, disasm_not_branch
                bit     7, b
                jp      z, disasm_br_adr
                jp      disasm_br_adrp


; --- ret / br / blr (register; no PC target) -------------------------
; ret  = 0xd65f03c0 (full word).  br = 0xd61f0000 | Rn<<5.  blr = 0xd63f0000.
disasm_br_reg:
                call    disasm_br_save_word
; Rn = ((wd&3)<<3)|(we>>5).
                ld      a, (disasm_br_wd)
                and     3
                add     a, a
                add     a, a
                add     a, a
                ld      l, a
                ld      a, (disasm_br_we)
                rlca
                rlca
                rlca
                and     7
                or      l
                ld      (disasm_br_rt), a           ; reuse rt slot for Rn
; All three share mask 0xfffffc1f: bits[9:5]=Rn free, bits[4:0]=0 fixed.
; So require (wd & 0xfc)==0 (bits15:10 zero) and (we & 0x1f)==0 (Rt field
; zero).  wc selects the family: 0x5f ret, 0x1f br, 0x3f blr.
                ld      a, (disasm_br_wd)
                and     &fc
                jr      nz, disasm_not_branch_j
                ld      a, (disasm_br_we)
                and     &1f
                jr      nz, disasm_not_branch_j
                ld      a, (disasm_br_wc)
                cp      &5f
                jr      z, disasm_br_reg_ret
                cp      &1f
                jr      z, disasm_br_reg_br
                cp      &3f
                jr      z, disasm_br_reg_blr
                jr      disasm_not_branch_j
disasm_br_reg_ret:
; ret: bare "ret" when Rn==30 (the default link register, objdump omits it);
; otherwise "ret <xN>".
                ld      hl, disasm_br_ret_txt
                call    disasm_asi_set_mnem
                ld      hl, DISASM_COMM_OPS
                ld      a, (disasm_br_rt)           ; Rn
                cp      30
                jr      nz, disasm_br_reg_ret_reg
                ld      (hl), 0                     ; Rn==30 → no operands
                jp      disasm_br_done
disasm_br_reg_ret_reg:
                ld      c, a
                ld      b, 1                        ; always X register
                call    disasm_br_emit_reg
                ld      (hl), 0
                jp      disasm_br_done
disasm_br_reg_br:
                ld      hl, disasm_br_br_txt
                jr      disasm_br_reg_emit
disasm_br_reg_blr:
                ld      hl, disasm_br_blr_txt
disasm_br_reg_emit:
                call    disasm_asi_set_mnem
                ld      hl, DISASM_COMM_OPS
                ld      a, (disasm_br_rt)           ; Rn
                ld      c, a
                ld      b, 1                        ; always X register
                call    disasm_br_emit_reg
                ld      (hl), 0
                jp      disasm_br_done

; Trampoline: a `jr`-reachable decline (the body above is past the 128-byte
; range of the original disasm_not_branch label).
disasm_not_branch_j:
                jp      disasm_not_branch


; --- b (imm26) -------------------------------------------------------
disasm_br_b:
                call    disasm_br_save_word
                call    disasm_br_build_imm26
                ld      hl, disasm_br_b_txt
                jp      disasm_br_emit_target_only
; --- bl (imm26) ------------------------------------------------------
disasm_br_bl:
                call    disasm_br_save_word
                call    disasm_br_build_imm26
                ld      hl, disasm_br_bl_txt
                jp      disasm_br_emit_target_only


; --- cbz/cbnz (imm19 + Rt + sf) --------------------------------------
disasm_br_cbz:
                call    disasm_br_save_word
                call    disasm_br_build_cbr_fields
                call    disasm_br_build_imm19
                ld      hl, disasm_br_cbz_txt
                jp      disasm_br_emit_reg_target
disasm_br_cbnz:
                call    disasm_br_save_word
                call    disasm_br_build_cbr_fields
                call    disasm_br_build_imm19
                ld      hl, disasm_br_cbnz_txt
                jp      disasm_br_emit_reg_target


; --- b.<cond> (imm19 + cond4) ----------------------------------------
; Requires we bit4 == 0 (bit4 of the word; pattern 0x54000000 mask
; 0xff00001f — the o0 bit must be 0).
disasm_br_bcond:
                call    disasm_br_save_word
                ld      a, (disasm_br_we)
                bit     4, a
                jp      nz, disasm_not_branch
                call    disasm_br_build_imm19       ; sets width=19, shift=2, adrp=0
; mnemonic "b.<cc>": copy "b." then the 2-char cond name (cond = we&0xf).
                call    disasm_br_compute_target
; write "b." + cond name to DISASM_COMM_MNEM.
                ld      hl, DISASM_COMM_MNEM
                ld      (hl), "b"
                inc     hl
                ld      (hl), "."
                inc     hl
                ld      a, (disasm_br_we)
                and     &0f                         ; cond
                add     a, a                        ; *2 (2-char entries)
                ld      e, a
                ld      d, 0
                ld      ix, disasm_br_cond_tbl
                add     ix, de
                ld      a, (ix+0)
                ld      (hl), a
                inc     hl
                ld      a, (ix+1)
                ld      (hl), a
                inc     hl
                ld      (hl), 0
; operands: "0x<target>".
                ld      hl, DISASM_COMM_OPS
                call    disasm_br_emit_target
                ld      (hl), 0
                jp      disasm_br_done


; --- adr (imm21, no shift, base = pc) --------------------------------
disasm_br_adr:
                call    disasm_br_save_word
                call    disasm_br_build_imm21
                ld      a, 21
                ld      (disasm_br_width), a
                xor     a
                ld      (disasm_br_shift), a        ; no shift
                ld      (disasm_br_pgmask), a         ; base = full pc
                ld      hl, disasm_br_adr_txt
                jp      disasm_br_emit_rd_target
; --- adrp (imm21, shift 12, base = pc & ~0xfff) ----------------------
disasm_br_adrp:
                call    disasm_br_save_word
                call    disasm_br_build_imm21
                ld      a, 21
                ld      (disasm_br_width), a
                ld      a, 12
                ld      (disasm_br_shift), a
                ld      a, 1
                ld      (disasm_br_pgmask), a         ; base = pc & ~0xfff
                ld      hl, disasm_br_adrp_txt
                jp      disasm_br_emit_rd_target


; =======================================================================
; Shared field builders + emit tails.
; =======================================================================

; --- build imm26 byte offset: off = sext(imm26)<<2 -------------------
; imm26 = bits[25:0] = ((wb&3)<<24)|(wc<<16)|(wd<<8)|we.
disasm_br_build_imm26:
                ld      a, (disasm_br_we)
                ld      (disasm_br_off+0), a
                ld      a, (disasm_br_wd)
                ld      (disasm_br_off+1), a
                ld      a, (disasm_br_wc)
                ld      (disasm_br_off+2), a
                ld      a, (disasm_br_wb)
                and     3
                ld      (disasm_br_off+3), a
                ld      hl, disasm_br_off+4
                call    disasm_br_zero4_at_hl       ; off[4..7] = 0
                ld      a, 26
                ld      (disasm_br_width), a
                ld      a, 2
                ld      (disasm_br_shift), a
                xor     a
                ld      (disasm_br_pgmask), a
                ret


; --- build imm19 byte offset: off = sext(imm19)<<2 -------------------
; imm19 = bits[23:5] = (wc<<11)|(wd<<3)|(we>>5).
disasm_br_build_imm19:
                ld      a, (disasm_br_wc)
                ld      h, a
                ld      l, 0                        ; HL = wc<<8
                add     hl, hl
                add     hl, hl
                add     hl, hl                      ; wc<<11
                ld      a, (disasm_br_wd)
                ld      e, a
                ld      d, 0
                ex      de, hl
                add     hl, hl
                add     hl, hl
                add     hl, hl                      ; wd<<3
                ex      de, hl                      ; DE=wd<<3, HL=wc<<11
                add     hl, de
                ld      a, (disasm_br_we)
                rlca
                rlca
                rlca
                and     7                           ; we>>5
                or      l
                ld      l, a
                ld      (disasm_br_off), hl         ; off[0..1] = imm19[15:0]
; bits18:16 of imm19 = wc>>5 (wc<<11 puts wc bits7:5 at imm19 bits18:16);
; these overflow HL, so store them in off[2] directly.
                ld      a, (disasm_br_wc)
                rlca
                rlca
                rlca
                and     7
                ld      (disasm_br_off+2), a
                ld      hl, disasm_br_off+3
                call    disasm_br_zero5_at_hl       ; off[3..7] = 0
                ld      a, 19
                ld      (disasm_br_width), a
                ld      a, 2
                ld      (disasm_br_shift), a
                xor     a
                ld      (disasm_br_pgmask), a
                ret


; --- build imm21 raw value (adr/adrp): immhi:immlo, no shift here ----
; imm21 = (immhi<<2)|immlo ; immhi = bits[23:5] (19 bits) ; immlo =
; bits[30:29] = (wb>>5)&3.  Stored zero-extended in disasm_br_off.
disasm_br_build_imm21:
; immhi (19 bits) = (wc<<11)|(wd<<3)|(we>>5), max 0x7ffff.  Build it into
; disasm_br_off zero-extended:  low 16 bits in HL, bits18:16 = wc>>5.
                ld      a, (disasm_br_wc)
                ld      h, a
                ld      l, 0                        ; HL = wc<<8
                add     hl, hl
                add     hl, hl
                add     hl, hl                      ; HL = wc<<11 (low 16 bits)
                ld      a, (disasm_br_wd)
                ld      e, a
                ld      d, 0
                ex      de, hl
                add     hl, hl
                add     hl, hl
                add     hl, hl                      ; HL = wd<<3
                ex      de, hl                      ; DE = wd<<3, HL = wc<<11
                add     hl, de                      ; HL = low 16 bits so far
                ld      a, (disasm_br_we)
                rlca
                rlca
                rlca
                and     7                           ; we>>5
                or      l
                ld      l, a                        ; HL = immhi[15:0]
                ld      (disasm_br_off), hl
; bits18:16 of immhi = wc>>5 (wc<<11 puts wc bits7:5 at immhi bits18:16).
                ld      a, (disasm_br_wc)
                rlca
                rlca
                rlca
                and     7
                ld      (disasm_br_off+2), a
                ld      hl, disasm_br_off+3
                call    disasm_br_zero5_at_hl       ; off[3..7] = 0
; imm21 = (immhi<<2)|immlo.  Shift <<2 then OR immlo = (wb>>5)&3.
                call    disasm_br_off_shl1
                call    disasm_br_off_shl1
                ld      a, (disasm_br_wb)
                rlca
                rlca
                rlca
                and     3                           ; wb>>5 (= immlo, bits30:29)
                ld      l, a
                ld      a, (disasm_br_off)
                or      l
                ld      (disasm_br_off), a
                ret


; --- build cbz/cbnz reg fields: sf, Rt -------------------------------
disasm_br_build_cbr_fields:
                ld      a, (disasm_br_wb)
                rlca
                and     1
                ld      (disasm_br_is64), a         ; sf
                ld      a, (disasm_br_we)
                and     &1f
                ld      (disasm_br_rt), a
                ret


; --- emit tail: mnemonic (HL) + operands "0x<target>" ----------------
; HL on entry → mnemonic string.  compute_target clobbers HL, so the
; mnemonic pointer is stashed first.
disasm_br_emit_target_only:
                ld      (disasm_br_mnem_ptr), hl
                call    disasm_br_compute_target
                ld      hl, (disasm_br_mnem_ptr)
                call    disasm_asi_set_mnem         ; mnemonic from HL
                ld      hl, DISASM_COMM_OPS
                call    disasm_br_emit_target
                ld      (hl), 0
                jp      disasm_br_done

; --- emit tail: mnemonic (HL) + "<reg>, 0x<target>" (cbz/cbnz) -------
disasm_br_emit_reg_target:
                ld      (disasm_br_mnem_ptr), hl
                call    disasm_br_compute_target
                ld      hl, (disasm_br_mnem_ptr)
                call    disasm_asi_set_mnem
                ld      hl, DISASM_COMM_OPS
                ld      a, (disasm_br_rt)
                ld      c, a
                ld      a, (disasm_br_is64)
                ld      b, a
                call    disasm_br_emit_reg
                ld      (hl), ","
                inc     hl
                ld      (hl), " "
                inc     hl
                call    disasm_br_emit_target
                ld      (hl), 0
                jp      disasm_br_done

; --- emit tail: mnemonic (HL) + "<xreg>, 0x<target>" (adr/adrp) ------
disasm_br_emit_rd_target:
                ld      (disasm_br_mnem_ptr), hl
                call    disasm_br_compute_target
                ld      hl, (disasm_br_mnem_ptr)
                call    disasm_asi_set_mnem
                ld      hl, DISASM_COMM_OPS
                ld      a, (disasm_br_we)
                and     &1f                         ; Rd
                ld      c, a
                ld      b, 1                        ; always X register
                call    disasm_br_emit_reg
                ld      (hl), ","
                inc     hl
                ld      (hl), " "
                inc     hl
                call    disasm_br_emit_target
                ld      (hl), 0
                jp      disasm_br_done


; -----------------------------------------------------------------------
; disasm_br_compute_target — fill disasm_mw_val[0..7] (LE) with the absolute
; target address.  Inputs (scratch):
;   disasm_br_off[0..3]  = raw immediate field (zero-extended, LE)
;   disasm_br_width      = field bit width (sign bit at width-1)
;   disasm_br_shift      = left-shift count applied AFTER sign-extension
;   disasm_br_pgmask       = 1 → base = pc & ~0xfff ; 0 → base = pc
; pc is the 8-byte LE value at DISASM_COMM_PC.
; Clobbers A, BC, DE, HL.
; -----------------------------------------------------------------------
disasm_br_compute_target:
; 1. Sign-extend disasm_br_off (8 bytes) from bit (width-1) to full 64 bits.
;    The sign bit lives at bit (width-1).  Determine its byte/bit, then if
;    set, fill all higher bits with 1.
                ld      a, (disasm_br_width)
                dec     a                           ; sign bit index (0..25)
                ld      b, a                        ; B = sign-bit index
; byte = sign>>3 ; bit = sign&7.
                ld      a, b
                rrca
                rrca
                rrca
                and     &1f                         ; byte index (0..3)
                ld      l, a
                ld      h, 0
                ld      de, disasm_br_off
                add     hl, de                      ; HL = &off[byte]
                ld      a, b
                and     7                           ; bit within byte
                ld      c, a
; build mask = 1<<bit.
                ld      a, 1
                inc     c
                dec     c
                jr      z, disasm_br_se_haymask
disasm_br_se_shl:
                add     a, a
                dec     c
                jr      nz, disasm_br_se_shl
disasm_br_se_haymask:
                ld      c, a                        ; C = sign mask
                ld      a, (hl)
                and     c
                jr      z, disasm_br_se_positive
; negative: set every bit above the sign bit across the 4 bytes.
                call    disasm_br_off_signfill
disasm_br_se_positive:
; 2. Shift left by disasm_br_shift (multiply), over the 8-byte buffer.
;    disasm_br_off_shl1 clobbers B/HL, so the loop count lives in memory.
                ld      a, (disasm_br_shift)
                or      a
                jr      z, disasm_br_shifted
                ld      (disasm_br_shctr), a
disasm_br_shift_loop:
                call    disasm_br_off_shl1
                ld      a, (disasm_br_shctr)
                dec     a
                ld      (disasm_br_shctr), a
                jr      nz, disasm_br_shift_loop
disasm_br_shifted:
; 3. target = base + off → disasm_mw_val[0..7].  off is now a full 64-bit
;    sign-extended, shifted value.  Copy pc (8 bytes) from DISASM_COMM_PC,
;    mask low 12 bits when adrp, then add off (plain 8-byte add).
                ld      hl, DISASM_COMM_PC
                ld      de, disasm_mw_val
                ld      bc, 8
                ldir
                ld      a, (disasm_br_pgmask)
                or      a
                jr      z, disasm_br_ct_nomask
                xor     a
                ld      (disasm_mw_val), a          ; clear bits7:0
                ld      a, (disasm_mw_val+1)
                and     &f0                         ; clear bits11:8
                ld      (disasm_mw_val+1), a
disasm_br_ct_nomask:
; 8-byte add: disasm_mw_val += disasm_br_off.
                ld      hl, disasm_mw_val
                ld      de, disasm_br_off
                or      a                           ; clear carry
                ld      b, 8
disasm_br_add_loop:
                ld      a, (de)
                adc     a, (hl)
                ld      (hl), a
                inc     hl
                inc     de
                djnz    disasm_br_add_loop
                ret


; -----------------------------------------------------------------------
; disasm_br_off_shl1 — shift disasm_br_off (8 bytes LE) left by 1.
; Clobbers A, B, F, HL.
; -----------------------------------------------------------------------
disasm_br_off_shl1:
                ld      hl, disasm_br_off
                or      a                           ; clear carry
                ld      b, 8
disasm_br_shl1_loop:
                ld      a, (hl)
                adc     a, a
                ld      (hl), a
                inc     hl
                djnz    disasm_br_shl1_loop
                ret


; -----------------------------------------------------------------------
; disasm_br_zero4/5/6_at_hl — write 4/5/6 zero bytes starting at (HL).
; Used to zero the high part of disasm_br_off after a builder fills only the
; low bytes.  Clobbers A, B, HL.
; -----------------------------------------------------------------------
disasm_br_zero6_at_hl:
                ld      b, 6
                jr      disasm_br_zero_n
disasm_br_zero5_at_hl:
                ld      b, 5
                jr      disasm_br_zero_n
disasm_br_zero4_at_hl:
                ld      b, 4
disasm_br_zero_n:
                xor     a
disasm_br_zero_loop:
                ld      (hl), a
                inc     hl
                djnz    disasm_br_zero_loop
                ret

; disasm_br_off_zero_hi — zero disasm_br_off[2..7] (6 bytes).
disasm_br_off_zero_hi:
                ld      hl, disasm_br_off+2
                jr      disasm_br_zero6_at_hl


; -----------------------------------------------------------------------
; disasm_br_off_signfill — given HL = &off[byte] (the sign byte) and C =
; sign-bit mask (1<<bit) within it, set every bit strictly above the sign
; bit, then fill all higher whole bytes (up to disasm_br_off+7) with 0xff.
; Clobbers A, DE, F.  HL is advanced.
; -----------------------------------------------------------------------
disasm_br_off_signfill:
; hibits = ~((mask<<1) - 1) = bits strictly above the sign bit in this byte.
                ld      a, c
                add     a, a                        ; mask<<1 (0 if bit==7)
                dec     a                           ; (mask<<1)-1 = bits 0..bit
                cpl                                 ; ~ → bits bit+1..7
                or      (hl)
                ld      (hl), a
; Fill bytes (HL+1)..(disasm_br_off+7) with 0xff.
                ld      de, disasm_br_off+7
disasm_br_sf_hi_loop:
                ld      a, l
                cp      e                           ; HL == off+7 ? (H fixed)
                ret     z
                jr      nc, disasm_br_sf_ret
                inc     hl
                ld      (hl), &ff
                jr      disasm_br_sf_hi_loop
disasm_br_sf_ret:
                ret


; -----------------------------------------------------------------------
; disasm_br_emit_reg — emit register: B = is64 (0=w/1=x), C = index.
; idx 31 → wzr/xzr (the zero register; branch-family regs are never SP).
; Advances HL.  Clobbers A, DE.
; -----------------------------------------------------------------------
disasm_br_emit_reg:
                ld      a, c
                cp      31
                jr      z, disasm_br_reg_zero
                ld      a, b
                or      a
                ld      a, "w"
                jr      z, disasm_br_reg_pfx
                ld      a, "x"
disasm_br_reg_pfx:
                ld      (hl), a
                inc     hl
                ld      a, c
                ld      e, a
                ld      d, 0
                call    disasm_emit_dec16
                ret
disasm_br_reg_zero:
                ld      a, b
                or      a
                ld      a, "w"
                jr      z, disasm_br_reg_zp
                ld      a, "x"
disasm_br_reg_zp:
                ld      (hl), a
                inc     hl
                ld      (hl), "z"
                inc     hl
                ld      (hl), "r"
                inc     hl
                ret


; -----------------------------------------------------------------------
; disasm_br_emit_target — emit "0x<minimal-width-hex>" of disasm_mw_val (8
; bytes LE) to (HL), advancing HL.  Clobbers A, BC, DE.
; -----------------------------------------------------------------------
disasm_br_emit_target:
                ld      (hl), "0"
                inc     hl
                ld      (hl), "x"
                inc     hl
                ld      a, 8
                call    disasm_mw_emit_hexbuf
                ret


; -----------------------------------------------------------------------
; disasm_br_done — common success epilogue.  The branch decoders clobber
; BC/IX freely (the field builders use B and IX as scratch), so rather than
; stack-saving them we RECONSTRUCT the original word here from the saved
; bytes: BC = high word = (wb<<8)|wc ; IX = low word = (wd<<8)|we.  This
; honours disasm_entry's "Preserves: BC, IX, IY" ABI without any push/pop.
; -----------------------------------------------------------------------
disasm_br_done:
                ld      a, (disasm_br_wb)
                ld      b, a
                ld      a, (disasm_br_wc)
                ld      c, a
                ld      a, (disasm_br_wd)
                ld      h, a
                ld      a, (disasm_br_we)
                ld      l, a
                push    hl
                pop     ix
                ret


; --- branch-family mnemonic strings -----------------------------------
disasm_br_b_txt:        defm    "b"
                        defb    0
disasm_br_bl_txt:       defm    "bl"
                        defb    0
disasm_br_ret_txt:      defm    "ret"
                        defb    0
disasm_br_br_txt:       defm    "br"
                        defb    0
disasm_br_blr_txt:      defm    "blr"
                        defb    0
disasm_br_cbz_txt:      defm    "cbz"
                        defb    0
disasm_br_cbnz_txt:     defm    "cbnz"
                        defb    0
disasm_br_tbz_txt:      defm    "tbz"
                        defb    0
disasm_br_tbnz_txt:     defm    "tbnz"
                        defb    0
disasm_br_adr_txt:      defm    "adr"
                        defb    0
disasm_br_adrp_txt:     defm    "adrp"
                        defb    0

; Condition-name table (2 chars per entry, indexed by cond*2).  Mirrors
; format.CondCode.Name() (sam-aarch64-format/operands.go).
disasm_br_cond_tbl:     defm    "eqnecsccmiplvsvc"
                        defm    "hilsgeltgtlealnv"

; --- branch-family working scratch (this page) ------------------------
disasm_br_wb:           defb    0       ; saved word byte bits31:24
disasm_br_wc:           defb    0       ;                  bits23:16
disasm_br_wd:           defb    0       ;                  bits15:8
disasm_br_we:           defb    0       ;                  bits7:0
disasm_br_off:          defs    8       ; raw/sign-extended/shifted offset (LE, 64-bit)
disasm_br_width:        defb    0       ; immediate field bit width
disasm_br_shift:        defb    0       ; post-sext left-shift count
disasm_br_pgmask:         defb    0       ; 1 = mask pc low 12 bits (adrp)
disasm_br_op:           defb    0       ; tbz(0)/tbnz(1)
disasm_br_is64:         defb    0       ; register width (sf / b5)
disasm_br_bit:          defb    0       ; tbz/tbnz bit position
disasm_br_rt:           defb    0       ; Rt / Rn index
disasm_br_shctr:        defb    0       ; shift-loop counter (compute_target)
disasm_br_mnem_ptr:     defw    0       ; stashed mnemonic ptr across compute_target
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
; Bitfield (UBFM / SBFM / BFM) family — Z80 port of decodeBitfieldAlias
; (tools/aarch64dec/aliases.go:186).
;
; Encoding: sf | opc(2) | 100110 | N(1) | immr(6) | imms(6) | Rn(5) | Rd(5)
;   opc 00 SBFM   opc 01 BFM   opc 10 UBFM   opc 11 undefined.
;
; Discriminator bits[28:23] == 0b100110 = ((B&0x1f)<<1)|(C>>7) == 0x26.
;
; d = 32 (sf=0) or 64 (sf=1).  The alias selection and lsb/width transforms
; (see per-branch notes) follow the ARM ARM and the Go authority exactly:
;   UBFM: imms==d-1 → lsr Rd,Rn,#immr
;         imms+1==immr → lsl Rd,Rn,#(d-1-imms)
;         imms<immr → ubfiz Rd,Rn,#(d-immr),#(imms+1)
;         else (imms>=immr): uxtb/uxth (sf=0,immr=0,imms=7/15) else
;               ubfx Rd,Rn,#immr,#(imms-immr+1)
;   SBFM: imms==d-1 → asr Rd,Rn,#immr
;         immr==0 → sxtb/sxth/sxtw (imms=7/15/31[,sf=1]); source is W-reg
;         imms<immr → sbfiz Rd,Rn,#(d-immr),#(imms+1)
;         else → sbfx Rd,Rn,#immr,#(imms-immr+1)
;   BFM:  imms<immr → bfi Rd,Rn,#(d-immr),#(imms+1) ; bfc (Rn=31) drops Rn
;         else → bfxil Rd,Rn,#immr,#(imms-immr+1)
;
; ABI: BC/IX clobbered by the emit code; saved after the last decline and
; restored via disasm_bf_done (reconstruct from saved bytes is unnecessary —
; we push/pop).  Decline/undefined paths leave B,C,D,E intact.
; =======================================================================
disasm_try_bitfield:
; Discriminator: ((B&0x1f)<<1)|(C>>7) == 0x26.
                ld      a, b
                and     &1f
                add     a, a
                ld      l, a
                ld      a, c
                rlca
                and     1
                or      l
                cp      &26
                jp      nz, disasm_not_bitfield
; sf = B>>7.
                ld      a, b
                rlca
                and     1
                ld      (disasm_bf_sf), a
; opc = (B>>5)&3.
                ld      a, b
                rlca
                rlca
                rlca
                and     3
                ld      (disasm_bf_opc), a
; N = (C>>6)&1.
                ld      a, c
                rlca
                rlca
                and     1
                ld      (disasm_bf_n), a
; immr = ((C&0x3f)<<? ) — immr is bits[21:16] = ((C&0x3f)<<0)? No:
; immr occupies bits 21..16.  bit22=N, so immr = C & 0x3f.
                ld      a, c
                and     &3f
                ld      (disasm_bf_immr), a
; imms = bits[15:10] = (D>>2)&0x3f.
                ld      a, d
                rrca
                rrca
                and     &3f
                ld      (disasm_bf_imms), a
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
                ld      (disasm_bf_rn), a
; Rd = E&0x1f.
                ld      a, e
                and     &1f
                ld      (disasm_bf_rd), a

; Undefined guard: N != sf OR opc == 11 → objdump renders .inst.  This
; decoder owns the whole bitfield class, so claim it as .inst directly.
                ld      a, (disasm_bf_n)
                ld      l, a
                ld      a, (disasm_bf_sf)
                cp      l
                jp      nz, disasm_inst
                ld      a, (disasm_bf_opc)
                cp      3
                jp      z, disasm_inst

; d-1 = 31 (sf=0) or 63 (sf=1) → disasm_bf_dm1.
                ld      a, (disasm_bf_sf)
                or      a
                ld      a, 31
                jr      z, disasm_bf_set_dm1
                ld      a, 63
disasm_bf_set_dm1:
                ld      (disasm_bf_dm1), a

; Past the last decline — commit to success; save BC/IX (emit clobbers).
                push    bc
                push    ix

                ld      a, (disasm_bf_opc)
                cp      2
                jp      z, disasm_bf_ubfm
                cp      1
                jp      z, disasm_bf_bfm
                jp      disasm_bf_sbfm          ; opc 00


; --- UBFM (opc 10) ----------------------------------------------------
disasm_bf_ubfm:
; lsr: imms == d-1 → lsr Rd,Rn,#immr.
                ld      a, (disasm_bf_imms)
                ld      l, a
                ld      a, (disasm_bf_dm1)
                cp      l
                jr      nz, disasm_bf_u_chklsl
                ld      hl, disasm_dpr_lsr_txt
                ld      a, (disasm_bf_immr)
                jp      disasm_bf_emit_sh
disasm_bf_u_chklsl:
; lsl: imms+1 == immr → lsl Rd,Rn,#(d-1-imms).
                ld      a, (disasm_bf_imms)
                inc     a
                ld      l, a
                ld      a, (disasm_bf_immr)
                cp      l
                jr      nz, disasm_bf_u_chkubfiz
                ; shift = d-1-imms = dm1 - imms; stash in tmp, then emit.
                ld      a, (disasm_bf_dm1)
                ld      e, a
                ld      a, (disasm_bf_imms)
                ld      d, a
                ld      a, e
                sub     d                        ; dm1 - imms
                ld      hl, disasm_dpr_lsl_txt
                jp      disasm_bf_emit_sh
disasm_bf_u_chkubfiz:
; ubfiz: imms < immr → ubfiz Rd,Rn,#(d-immr),#(imms+1).
                ld      a, (disasm_bf_imms)
                ld      l, a
                ld      a, (disasm_bf_immr)
                cp      l                       ; immr - imms ; C clear if immr<=imms
                jr      z, disasm_bf_u_extx     ; imms==immr → not <  → ubfx path
                jr      c, disasm_bf_u_extx     ; immr<imms  → not <  → ubfx path
                ; imms < immr → ubfiz
                ld      hl, disasm_bf_ubfiz_txt
                jp      disasm_bf_emit_immr2     ; lsb=d-immr width=imms+1
disasm_bf_u_extx:
; uxtb/uxth sub-aliases: sf==0 && immr==0 && imms==7/15.
                ld      a, (disasm_bf_sf)
                or      a
                jr      nz, disasm_bf_u_ubfx
                ld      a, (disasm_bf_immr)
                or      a
                jr      nz, disasm_bf_u_ubfx
                ld      a, (disasm_bf_imms)
                cp      7
                jr      nz, disasm_bf_u_chk15
                ld      hl, disasm_dpr_uxtb_txt
                jp      disasm_bf_emit_ext2
disasm_bf_u_chk15:
                cp      15
                jr      nz, disasm_bf_u_ubfx
                ld      hl, disasm_dpr_uxth_txt
                jp      disasm_bf_emit_ext2
disasm_bf_u_ubfx:
; ubfx Rd,Rn,#immr,#(imms-immr+1).
                ld      hl, disasm_bf_ubfx_txt
                jp      disasm_bf_emit_immr_w


; --- SBFM (opc 00) ----------------------------------------------------
disasm_bf_sbfm:
; asr: imms == d-1 → asr Rd,Rn,#immr.
                ld      a, (disasm_bf_imms)
                ld      l, a
                ld      a, (disasm_bf_dm1)
                cp      l
                jr      nz, disasm_bf_s_chkext
                ld      hl, disasm_dpr_asr_txt
                ld      a, (disasm_bf_immr)
                jp      disasm_bf_emit_sh
disasm_bf_s_chkext:
; sxtb/sxth/sxtw: immr==0, imms=7/15/31(sf=1).  Source is always a W-reg.
                ld      a, (disasm_bf_immr)
                or      a
                jr      nz, disasm_bf_s_chksbfiz
                ld      a, (disasm_bf_imms)
                cp      7
                jr      nz, disasm_bf_s_chk15
                ld      hl, disasm_dpr_sxtb_txt
                jp      disasm_bf_emit_sxt
disasm_bf_s_chk15:
                cp      15
                jr      nz, disasm_bf_s_chk31
                ld      hl, disasm_dpr_sxth_txt
                jp      disasm_bf_emit_sxt
disasm_bf_s_chk31:
                cp      31
                jr      nz, disasm_bf_s_chksbfiz
                ld      a, (disasm_bf_sf)
                or      a
                jr      z, disasm_bf_s_chksbfiz    ; sxtw only when sf=1
                ld      hl, disasm_dpr_sxtw_txt
                jp      disasm_bf_emit_sxt
disasm_bf_s_chksbfiz:
; sbfiz: imms < immr → sbfiz Rd,Rn,#(d-immr),#(imms+1).
                ld      a, (disasm_bf_imms)
                ld      l, a
                ld      a, (disasm_bf_immr)
                cp      l
                jr      z, disasm_bf_s_sbfx
                jr      c, disasm_bf_s_sbfx
                ld      hl, disasm_bf_sbfiz_txt
                jp      disasm_bf_emit_immr2
disasm_bf_s_sbfx:
; sbfx Rd,Rn,#immr,#(imms-immr+1).
                ld      hl, disasm_bf_sbfx_txt
                jp      disasm_bf_emit_immr_w


; --- BFM (opc 01) -----------------------------------------------------
disasm_bf_bfm:
; bfi/bfc: imms < immr.  lsb=d-immr width=imms+1.  bfc when Rn==31.
                ld      a, (disasm_bf_imms)
                ld      l, a
                ld      a, (disasm_bf_immr)
                cp      l
                jr      z, disasm_bf_bfxil
                jr      c, disasm_bf_bfxil
                ; imms < immr → bfi or bfc
                ld      a, (disasm_bf_rn)
                cp      31
                jr      z, disasm_bf_bfc
                ld      hl, disasm_bf_bfi_txt
                jp      disasm_bf_emit_immr2
disasm_bf_bfc:
; bfc Rd, #lsb, #width — no source register.  lsb=d-immr, width=imms+1.
                ld      hl, disasm_bf_bfc_txt
                call    disasm_bf_set_mnem
                ld      hl, DISASM_COMM_OPS
                call    disasm_bf_emit_rd
                ld      (hl), ","
                inc     hl
                ld      (hl), " "
                inc     hl
                call    disasm_bf_lsb_width      ; "#lsb, #width"
                ld      (hl), 0
                jp      disasm_bf_done
disasm_bf_bfxil:
; bfxil Rd,Rn,#immr,#(imms-immr+1).
                ld      hl, disasm_bf_bfxil_txt
                jp      disasm_bf_emit_immr_w


; =======================================================================
; Bitfield emit tails.
; =======================================================================

; --- disasm_bf_emit_sh: "Rd, Rn, #<A>" (shift form: lsr/lsl/asr) ------
; HL = mnemonic string; A = shift amount (decimal).  Rd,Rn full-width.
disasm_bf_emit_sh:
                ld      (disasm_bf_tmp), a       ; stash shift before set_mnem
                call    disasm_bf_set_mnem
                ld      hl, DISASM_COMM_OPS
                call    disasm_bf_emit_rd_rn     ; "Rd, Rn, "
                ld      (hl), "#"
                inc     hl
                ld      a, (disasm_bf_tmp)
                ld      e, a
                ld      d, 0
                call    disasm_emit_dec16
                ld      (hl), 0
                jp      disasm_bf_done

; --- disasm_bf_emit_immr_w: "Rd, Rn, #immr, #(imms-immr+1)" -----------
; ubfx/sbfx/bfxil.  HL = mnemonic string.
disasm_bf_emit_immr_w:
                call    disasm_bf_set_mnem
                ld      hl, DISASM_COMM_OPS
                call    disasm_bf_emit_rd_rn     ; "Rd, Rn, "
; #immr
                ld      (hl), "#"
                inc     hl
                ld      a, (disasm_bf_immr)
                ld      e, a
                ld      d, 0
                call    disasm_emit_dec16        ; HL advanced past digits
                ld      (hl), ","
                inc     hl
                ld      (hl), " "
                inc     hl
; #(imms-immr+1)
                ld      (hl), "#"
                inc     hl
                ld      a, (disasm_bf_imms)
                ld      e, a
                ld      a, (disasm_bf_immr)
                ld      d, a
                ld      a, e
                sub     d
                inc     a                        ; imms-immr+1
                ld      e, a
                ld      d, 0
                call    disasm_emit_dec16
                ld      (hl), 0
                jp      disasm_bf_done

; --- disasm_bf_emit_immr2: "Rd, Rn, #(d-immr), #(imms+1)" -------------
; ubfiz/sbfiz/bfi.  lsb=d-immr, width=imms+1.  HL = mnemonic string.
disasm_bf_emit_immr2:
                call    disasm_bf_set_mnem
                ld      hl, DISASM_COMM_OPS
                call    disasm_bf_emit_rd_rn     ; "Rd, Rn, "
                call    disasm_bf_lsb_width      ; "#(d-immr), #(imms+1)"
                ld      (hl), 0
                jp      disasm_bf_done

; --- disasm_bf_emit_ext2: "Rd, Rn"  (uxtb/uxth — both full-width) -----
disasm_bf_emit_ext2:
                call    disasm_bf_set_mnem
                ld      hl, DISASM_COMM_OPS
                call    disasm_bf_emit_rd
                ld      (hl), ","
                inc     hl
                ld      (hl), " "
                inc     hl
                call    disasm_bf_emit_rn
                ld      (hl), 0
                jp      disasm_bf_done

; --- disasm_bf_emit_sxt: "Rd, Wn"  (sxtb/sxth/sxtw — src is W-reg) ----
disasm_bf_emit_sxt:
                call    disasm_bf_set_mnem
                ld      hl, DISASM_COMM_OPS
                call    disasm_bf_emit_rd        ; Rd full-width
                ld      (hl), ","
                inc     hl
                ld      (hl), " "
                inc     hl
; Wn: source register always a W-reg.
                ld      a, (disasm_bf_rn)
                ld      c, a
                ld      b, 0                     ; force w-width
                call    disasm_br_emit_reg
                ld      (hl), 0
                jp      disasm_bf_done


; -----------------------------------------------------------------------
; disasm_bf_lsb_width — emit "#<d-immr>, #<imms+1>" at (HL), advancing HL.
; Used by ubfiz/sbfiz/bfi/bfc.  Clobbers A, BC, DE.
; -----------------------------------------------------------------------
disasm_bf_lsb_width:
                ld      (hl), "#"
                inc     hl
; lsb = d - immr = (dm1+1) - immr.
                ld      a, (disasm_bf_dm1)
                inc     a                        ; d
                ld      e, a
                ld      a, (disasm_bf_immr)
                ld      d, a
                ld      a, e
                sub     d                        ; d - immr
                push    hl
                ld      e, a
                ld      d, 0
                pop     hl
                call    disasm_emit_dec16
                ld      (hl), ","
                inc     hl
                ld      (hl), " "
                inc     hl
                ld      (hl), "#"
                inc     hl
; width = imms + 1.
                ld      a, (disasm_bf_imms)
                inc     a
                ld      e, a
                ld      d, 0
                call    disasm_emit_dec16
                ret


; -----------------------------------------------------------------------
; disasm_bf_emit_rd_rn — emit "<Rd>, <Rn>, " (both full-width).  HL=dest.
; -----------------------------------------------------------------------
disasm_bf_emit_rd_rn:
                call    disasm_bf_emit_rd
                ld      (hl), ","
                inc     hl
                ld      (hl), " "
                inc     hl
                call    disasm_bf_emit_rn
                ld      (hl), ","
                inc     hl
                ld      (hl), " "
                inc     hl
                ret

; disasm_bf_emit_rd — emit Rd at full width (sf).  HL=dest, advanced.
disasm_bf_emit_rd:
                ld      a, (disasm_bf_rd)
                ld      c, a
                ld      a, (disasm_bf_sf)
                ld      b, a
                jp      disasm_br_emit_reg

; disasm_bf_emit_rn — emit Rn at full width (sf).  HL=dest, advanced.
disasm_bf_emit_rn:
                ld      a, (disasm_bf_rn)
                ld      c, a
                ld      a, (disasm_bf_sf)
                ld      b, a
                jp      disasm_br_emit_reg

; disasm_bf_set_mnem — copy null-terminated mnemonic at (HL) to MNEM.
disasm_bf_set_mnem:
                ld      de, DISASM_COMM_MNEM
disasm_bf_set_mnem_loop:
                ld      a, (hl)
                ld      (de), a
                or      a
                ret     z
                inc     hl
                inc     de
                jr      disasm_bf_set_mnem_loop

disasm_bf_done:
                pop     ix
                pop     bc
                ret

; --- bitfield mnemonic strings (those not already in the dpr_*_txt set) -
disasm_bf_ubfx_txt:     defm    "ubfx"
                        defb    0
disasm_bf_sbfx_txt:     defm    "sbfx"
                        defb    0
disasm_bf_bfi_txt:      defm    "bfi"
                        defb    0
disasm_bf_bfc_txt:      defm    "bfc"
                        defb    0
disasm_bf_bfxil_txt:    defm    "bfxil"
                        defb    0
disasm_bf_ubfiz_txt:    defm    "ubfiz"
                        defb    0
disasm_bf_sbfiz_txt:    defm    "sbfiz"
                        defb    0

; --- bitfield scratch (this page) -------------------------------------
disasm_bf_sf:           defb    0
disasm_bf_opc:          defb    0
disasm_bf_n:            defb    0
disasm_bf_immr:         defb    0
disasm_bf_imms:         defb    0
disasm_bf_rn:           defb    0
disasm_bf_rd:           defb    0
disasm_bf_dm1:          defb    0
disasm_bf_tmp:          defb    0


; =======================================================================
; Conditional compare (CCMP / CCMN) family — Z80 port of the ccmp (ID 88)
; and ccmn (ID 100) form-table entries (tools/aarch64enc/manual_forms.go),
; decoded by the Go form walk.
;
; Encoding (ARM ARM C6.2.41-44):
;   immediate: sf op 1 11010 0 10 imm5 cond 1 0 Rn 0 nzcv
;   register:  sf op 1 11010 0 10  Rm  cond 0 0 Rn 0 nzcv
;   op=1 → ccmp, op=0 → ccmn.  bit11=1 selects the immediate form (imm5 in
;   bits[20:16]); bit11=0 selects the register form (Rm in bits[20:16]).
;
; Discriminator bits[28:21] == 0b11010010 = ((B&0x1f)<<3)|(C>>5) == 0xD2.
; The conditional-compare class fixes bit29 (the "S" slot) = 1 — unlike
; conditional-select where it must be 0 — so require B bit5 = 1.  bit10
; (D bit2) and bit4 (E bit4) are fixed 0; decline (→ .inst) otherwise.
;
; Render: "<mnem> Rn, #0x<imm5>|Rm, #0x<nzcv>, <cond>".  Both imm5 and nzcv
; render in hex (minimal width), matching objdump (and the Go Imm5 decoder).
;
; ABI: BC/IX saved after the last decline, restored via disasm_cc_done.
; =======================================================================
disasm_try_ccmp:
; Discriminator: ((B&0x1f)<<3)|(C>>5) == 0xD2.
                ld      a, b
                and     &1f
                add     a, a
                add     a, a
                add     a, a                     ; (B&0x1f)<<3
                ld      l, a
                ld      a, c
                rlca
                rlca
                rlca
                and     7                        ; C>>5
                or      l
                cp      &d2
                jp      nz, disasm_not_ccmp
; bit29 (the "S" slot) must be 1 for the conditional-compare class.
                ld      a, b
                and     &20
                jp      z, disasm_not_ccmp
; bit10 = D bit2 must be 0.
                ld      a, d
                and     &04
                jp      nz, disasm_not_ccmp
; bit4 = E bit4 must be 0.
                ld      a, e
                and     &10
                jp      nz, disasm_not_ccmp

; Past the last decline — commit; save BC/IX (emit clobbers).
                push    bc
                push    ix

; sf = B>>7.
                ld      a, b
                rlca
                and     1
                ld      (disasm_cc_sf), a
; op = (B>>6)&1 → ccmp(1)/ccmn(0).
                ld      a, b
                rlca
                rlca
                and     1
                ld      (disasm_cc_op), a
; bit11 = D bit3 → 1 immediate / 0 register.
                ld      a, d
                and     &08
                ld      (disasm_cc_isimm), a     ; 0 = register, nonzero = immediate
; imm5/Rm = bits[20:16] = C & 0x1f.
                ld      a, c
                and     &1f
                ld      (disasm_cc_immrm), a
; cond = bits[15:12] = D>>4.
                ld      a, d
                rrca
                rrca
                rrca
                rrca
                and     &0f
                ld      (disasm_cc_cond), a
; Rn = bits[9:5] = ((D&3)<<3)|(E>>5).
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
                ld      (disasm_cc_rn), a
; nzcv = bits[3:0] = E & 0xf.
                ld      a, e
                and     &0f
                ld      (disasm_cc_nzcv), a

; mnemonic: op=1 → ccmp, op=0 → ccmn.
                ld      a, (disasm_cc_op)
                or      a
                ld      hl, disasm_cc_ccmn_txt
                jr      z, disasm_cc_set_mnem
                ld      hl, disasm_cc_ccmp_txt
disasm_cc_set_mnem:
                ld      de, DISASM_COMM_MNEM
disasm_cc_mnem_loop:
                ld      a, (hl)
                ld      (de), a
                or      a
                jr      z, disasm_cc_mnem_done
                inc     hl
                inc     de
                jr      disasm_cc_mnem_loop
disasm_cc_mnem_done:

; operands.
                ld      hl, DISASM_COMM_OPS
; Rn.
                ld      a, (disasm_cc_rn)
                ld      c, a
                ld      a, (disasm_cc_sf)
                ld      b, a
                call    disasm_br_emit_reg
                ld      (hl), ","
                inc     hl
                ld      (hl), " "
                inc     hl
; second operand: #0x<imm5> (immediate) or Rm (register).
                ld      a, (disasm_cc_isimm)
                or      a
                jr      z, disasm_cc_reg_operand
; immediate: "#0x<imm5>".
                ld      (hl), "#"
                inc     hl
                ld      (hl), "0"
                inc     hl
                ld      (hl), "x"
                inc     hl
                ld      a, (disasm_cc_immrm)
                call    disasm_cc_emit_hex_a
                jr      disasm_cc_after_op2
disasm_cc_reg_operand:
; register: Rm.
                ld      a, (disasm_cc_immrm)
                ld      c, a
                ld      a, (disasm_cc_sf)
                ld      b, a
                call    disasm_br_emit_reg
disasm_cc_after_op2:
                ld      (hl), ","
                inc     hl
                ld      (hl), " "
                inc     hl
; "#0x<nzcv>".
                ld      (hl), "#"
                inc     hl
                ld      (hl), "0"
                inc     hl
                ld      (hl), "x"
                inc     hl
                ld      a, (disasm_cc_nzcv)
                call    disasm_cc_emit_hex_a
                ld      (hl), ","
                inc     hl
                ld      (hl), " "
                inc     hl
; <cond>.
                ld      a, (disasm_cc_cond)
                call    disasm_cc_emit_cond_a
                ld      (hl), 0
                jp      disasm_cc_done

; --- helpers ----------------------------------------------------------
; disasm_cc_emit_hex_a — emit minimal-width lowercase hex of the value in A
; (A is a small field, 0..0x1f) at (HL), advancing HL.  High nibble emitted
; only when nonzero; the low nibble is always emitted (so 0 → "0").
disasm_cc_emit_hex_a:
                ld      c, a                     ; save value
                rra
                rra
                rra
                rra
                and     &0f
                jr      z, disasm_cc_hex_low     ; high nibble zero → skip
                call    disasm_emit_hex_nibble
disasm_cc_hex_low:
                ld      a, c
                jp      disasm_emit_hex_nibble   ; emits low nibble, advances HL, ret

; emit condition name for A (2-char), advancing HL.  Shares the cond table
; with the condsel/branch families.
disasm_cc_emit_cond_a:
                add     a, a                     ; *2
                ld      e, a
                ld      d, 0
                ld      ix, disasm_br_cond_tbl
                add     ix, de
                ld      a, (ix+0)
                ld      (hl), a
                inc     hl
                ld      a, (ix+1)
                ld      (hl), a
                inc     hl
                ret

disasm_cc_done:
                pop     ix
                pop     bc
                ret

disasm_cc_ccmp_txt:     defm    "ccmp"
                        defb    0
disasm_cc_ccmn_txt:     defm    "ccmn"
                        defb    0

disasm_cc_sf:           defb    0
disasm_cc_op:           defb    0
disasm_cc_isimm:        defb    0
disasm_cc_immrm:        defb    0
disasm_cc_cond:         defb    0
disasm_cc_rn:           defb    0
disasm_cc_nzcv:         defb    0


; =======================================================================
; Conditional select (CSEL / CSINC / CSINV / CSNEG) family — Z80 port of
; the base csel/csinc/csinv/csneg forms plus decodeCondSelAlias
; (tools/aarch64dec/aliases.go:502).
;
; Encoding: sf | op(1) | S(1) | 11010100 | Rm(5) | cond(4) | op2(2) | Rn | Rd
;   op=0,op2=00 CSEL   op=0,op2=01 CSINC   op=1,op2=00 CSINV   op=1,op2=01 CSNEG
;
; Discriminator bits[28:21] == 0b11010100 = ((B&0x1f)<<3)|(C>>5) == 0xD4,
; and op2 high bit (bit11 = D bit3) must be 0.  S=1 (bit29 = B bit5) is
; unallocated → decline (falls to .inst).
;
; Aliases (cond' = cond^1, only when cond[3:1] != 111):
;   CSINC Rn==Rm==31 → cset Rd, cond'
;   CSINC Rn==Rm     → cinc Rd, Rn, cond'
;   CSINV Rn==Rm==31 → csetm Rd, cond'
;   CSINV Rn==Rm     → cinv Rd, Rn, cond'
;   CSNEG Rn==Rm     → cneg Rd, Rn, cond'
; Otherwise the base form: <mnem> Rd, Rn, Rm, <cond>.
;
; ABI: BC/IX saved after the last decline, restored via disasm_cs_done.
; =======================================================================
disasm_try_condsel:
; Discriminator: ((B&0x1f)<<3)|(C>>5) == 0xD4.
                ld      a, b
                and     &1f
                add     a, a
                add     a, a
                add     a, a                     ; (B&0x1f)<<3
                ld      l, a
                ld      a, c
                rlca
                rlca
                rlca
                and     7                        ; C>>5
                or      l
                cp      &d4
                jp      nz, disasm_not_condsel
; op2 high bit (bit11 = D bit3) must be 0.
                ld      a, d
                bit     3, a
                jp      nz, disasm_not_condsel
; sf = B>>7.
                ld      a, b
                rlca
                and     1
                ld      (disasm_cs_sf), a
; op = (B>>6)&1.
                ld      a, b
                rlca
                rlca
                and     1
                ld      (disasm_cs_op), a
; S = (B>>5)&1 → unallocated when 1.
                ld      a, b
                rlca
                rlca
                rlca
                and     1
                jp      nz, disasm_not_condsel
; Rm = C&0x1f.
                ld      a, c
                and     &1f
                ld      (disasm_cs_rm), a
; cond = (D>>4)&0xf.
                ld      a, d
                rrca
                rrca
                rrca
                rrca
                and     &0f
                ld      (disasm_cs_cond), a
; op2 low bit = (D>>2)&1.
                ld      a, d
                rrca
                rrca
                and     1
                ld      (disasm_cs_op2), a
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
                ld      (disasm_cs_rn), a
; Rd = E&0x1f.
                ld      a, e
                and     &1f
                ld      (disasm_cs_rd), a

; Past the last decline — commit; save BC/IX (emit clobbers).
                push    bc
                push    ix

; Determine whether an alias applies.  Requires invertable cond (cond>>1
; != 0b111) AND a matching (op,op2) with the Rn/Rm shape.
                ld      a, (disasm_cs_cond)
                rrca
                and     7
                cp      7
                jp      z, disasm_cs_base        ; cond 111x → no alias

; op2 must be 1 for cset/cinc (CSINC) and cneg (CSNEG); 0 for CSINV.
                ld      a, (disasm_cs_op)
                or      a
                jp      nz, disasm_cs_op1
; op == 0: only CSINC (op2==1) has aliases.
                ld      a, (disasm_cs_op2)
                or      a
                jp      z, disasm_cs_base        ; CSEL (op2=0) → base
; CSINC: cset (Rn==Rm==31) / cinc (Rn==Rm).
                ld      a, (disasm_cs_rn)
                ld      l, a
                ld      a, (disasm_cs_rm)
                cp      l
                jp      nz, disasm_cs_base       ; Rn!=Rm → base
                ld      a, (disasm_cs_rn)
                cp      31
                jr      nz, disasm_cs_cinc
                ld      hl, disasm_cs_cset_txt
                jp      disasm_cs_emit_set
disasm_cs_cinc:
                ld      hl, disasm_cs_cinc_txt
                jp      disasm_cs_emit_rn_cond

disasm_cs_op1:
; op == 1: CSINV (op2==0) → csetm/cinv ; CSNEG (op2==1) → cneg.
                ld      a, (disasm_cs_op2)
                or      a
                jr      nz, disasm_cs_csneg
; CSINV: csetm (Rn==Rm==31) / cinv (Rn==Rm).
                ld      a, (disasm_cs_rn)
                ld      l, a
                ld      a, (disasm_cs_rm)
                cp      l
                jp      nz, disasm_cs_base
                ld      a, (disasm_cs_rn)
                cp      31
                jr      nz, disasm_cs_cinv
                ld      hl, disasm_cs_csetm_txt
                jp      disasm_cs_emit_set
disasm_cs_cinv:
                ld      hl, disasm_cs_cinv_txt
                jp      disasm_cs_emit_rn_cond
disasm_cs_csneg:
; CSNEG: cneg when Rn==Rm.
                ld      a, (disasm_cs_rn)
                ld      l, a
                ld      a, (disasm_cs_rm)
                cp      l
                jp      nz, disasm_cs_base
                ld      hl, disasm_cs_cneg_txt
                jp      disasm_cs_emit_rn_cond


; --- disasm_cs_emit_set: "<mnem> Rd, <invcond>" (cset/csetm) ----------
disasm_cs_emit_set:
                call    disasm_cs_set_mnem
                ld      hl, DISASM_COMM_OPS
                call    disasm_cs_emit_rd
                ld      (hl), ","
                inc     hl
                ld      (hl), " "
                inc     hl
                call    disasm_cs_emit_invcond
                ld      (hl), 0
                jp      disasm_cs_done

; --- disasm_cs_emit_rn_cond: "<mnem> Rd, Rn, <invcond>" (cinc/cinv/cneg) -
disasm_cs_emit_rn_cond:
                call    disasm_cs_set_mnem
                ld      hl, DISASM_COMM_OPS
                call    disasm_cs_emit_rd
                ld      (hl), ","
                inc     hl
                ld      (hl), " "
                inc     hl
                call    disasm_cs_emit_rn
                ld      (hl), ","
                inc     hl
                ld      (hl), " "
                inc     hl
                call    disasm_cs_emit_invcond
                ld      (hl), 0
                jp      disasm_cs_done


; --- disasm_cs_base: "<mnem> Rd, Rn, Rm, <cond>" ----------------------
disasm_cs_base:
                ld      a, (disasm_cs_op)
                or      a
                jr      nz, disasm_cs_base_op1
                ld      a, (disasm_cs_op2)
                or      a
                ld      hl, disasm_cs_csel_txt
                jr      z, disasm_cs_base_set
                ld      hl, disasm_cs_csinc_txt
                jr      disasm_cs_base_set
disasm_cs_base_op1:
                ld      a, (disasm_cs_op2)
                or      a
                ld      hl, disasm_cs_csinv_txt
                jr      z, disasm_cs_base_set
                ld      hl, disasm_cs_csneg_txt
disasm_cs_base_set:
                call    disasm_cs_set_mnem
                ld      hl, DISASM_COMM_OPS
                call    disasm_cs_emit_rd
                ld      (hl), ","
                inc     hl
                ld      (hl), " "
                inc     hl
                call    disasm_cs_emit_rn
                ld      (hl), ","
                inc     hl
                ld      (hl), " "
                inc     hl
; Rm.
                ld      a, (disasm_cs_rm)
                ld      c, a
                ld      a, (disasm_cs_sf)
                ld      b, a
                call    disasm_br_emit_reg
                ld      (hl), ","
                inc     hl
                ld      (hl), " "
                inc     hl
; <cond> (not inverted).
                ld      a, (disasm_cs_cond)
                call    disasm_cs_emit_cond_a
                ld      (hl), 0
                jp      disasm_cs_done


; --- helpers ----------------------------------------------------------
disasm_cs_emit_rd:
                ld      a, (disasm_cs_rd)
                ld      c, a
                ld      a, (disasm_cs_sf)
                ld      b, a
                jp      disasm_br_emit_reg
disasm_cs_emit_rn:
                ld      a, (disasm_cs_rn)
                ld      c, a
                ld      a, (disasm_cs_sf)
                ld      b, a
                jp      disasm_br_emit_reg

; emit inverted condition (cond ^ 1) name.
disasm_cs_emit_invcond:
                ld      a, (disasm_cs_cond)
                xor     1
                ; fall through
; emit condition name for A (2-char), advancing HL.
disasm_cs_emit_cond_a:
                add     a, a                     ; *2
                ld      e, a
                ld      d, 0
                ld      ix, disasm_br_cond_tbl
                add     ix, de
                ld      a, (ix+0)
                ld      (hl), a
                inc     hl
                ld      a, (ix+1)
                ld      (hl), a
                inc     hl
                ret

disasm_cs_set_mnem:
                ld      de, DISASM_COMM_MNEM
disasm_cs_set_mnem_loop:
                ld      a, (hl)
                ld      (de), a
                or      a
                ret     z
                inc     hl
                inc     de
                jr      disasm_cs_set_mnem_loop

disasm_cs_done:
                pop     ix
                pop     bc
                ret

disasm_cs_csel_txt:     defm    "csel"
                        defb    0
disasm_cs_csinc_txt:    defm    "csinc"
                        defb    0
disasm_cs_csinv_txt:    defm    "csinv"
                        defb    0
disasm_cs_csneg_txt:    defm    "csneg"
                        defb    0
disasm_cs_cset_txt:     defm    "cset"
                        defb    0
disasm_cs_csetm_txt:    defm    "csetm"
                        defb    0
disasm_cs_cinc_txt:     defm    "cinc"
                        defb    0
disasm_cs_cinv_txt:     defm    "cinv"
                        defb    0
disasm_cs_cneg_txt:     defm    "cneg"
                        defb    0

disasm_cs_sf:           defb    0
disasm_cs_op:           defb    0
disasm_cs_rm:           defb    0
disasm_cs_cond:         defb    0
disasm_cs_op2:          defb    0
disasm_cs_rn:           defb    0
disasm_cs_rd:           defb    0


; =======================================================================
; Data-processing (3-source) multiply family — Z80 port of
; decodeMul3Source (tools/aarch64dec/aliases.go:649).
;
; Encoding: sf | 00 | 11011 | op54(2) | op31(3) | Rm | o0(1) | Ra | Rn | Rd
;   op54 must be 00.  (op31,o0) selects the operation:
;     000 0 madd   000 1 msub                     (Ra=31 → mul / mneg)
;     001 0 smaddl 001 1 smsubl  (Xd,Wn,Wm,Xa)    (Ra=31 → smull / smnegl)
;     010 0 smulh                (Xd,Xn,Xm)
;     101 0 umaddl 101 1 umsubl  (Xd,Wn,Wm,Xa)    (Ra=31 → umull / umnegl)
;     110 0 umulh                (Xd,Xn,Xm)
;   smaddl/smulh/umaddl/umulh are 64-bit only (sf=1).
;
; Discriminator bits[28:24] == 0b11011 = ((B&0x1f)<<1)|(C>>7) == 0x1b, and
; op54 (bits[30:29] = (B>>5)&3) must be 0.
;
; ABI: BC/IX saved after the last decline, restored via disasm_m3_done.
; =======================================================================
disasm_try_mul3:
; Discriminator bits[28:24] == 0b11011 = (B & 0x1f) == 0x1b.
                ld      a, b
                and     &1f
                cp      &1b
                jp      nz, disasm_not_mul3
; op54 = (B>>5)&3 must be 0.
                ld      a, b
                rlca
                rlca
                rlca
                and     3
                jp      nz, disasm_not_mul3
; sf = B>>7.
                ld      a, b
                rlca
                and     1
                ld      (disasm_m3_sf), a
; op31 = (C>>5)&7  (bits[23:21]).
                ld      a, c
                rlca
                rlca
                rlca
                and     7
                ld      (disasm_m3_op31), a
; Rm = C&0x1f.
                ld      a, c
                and     &1f
                ld      (disasm_m3_rm), a
; o0 = (D>>7)&1  (bit15).
                ld      a, d
                rlca
                and     1
                ld      (disasm_m3_o0), a
; Ra = (D>>2)&0x1f  (bits[14:10]).
                ld      a, d
                rrca
                rrca
                and     &1f
                ld      (disasm_m3_ra), a
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
                ld      (disasm_m3_rn), a
; Rd = E&0x1f.
                ld      a, e
                and     &1f
                ld      (disasm_m3_rd), a

; op31 dispatch — validate before committing (the 64-bit-only and o0
; guards may decline) so BC/IX stays intact for .inst.
                ld      a, (disasm_m3_op31)
                cp      0
                jr      z, disasm_m3_ok
                cp      1
                jr      z, disasm_m3_need64
                cp      2
                jr      z, disasm_m3_need64_o0
                cp      5
                jr      z, disasm_m3_need64
                cp      6
                jr      z, disasm_m3_need64_o0
                jp      disasm_not_mul3          ; op31 011/100/111 unallocated
disasm_m3_need64_o0:
; smulh/umulh: require sf=1 and o0=0.
                ld      a, (disasm_m3_o0)
                or      a
                jp      nz, disasm_not_mul3
disasm_m3_need64:
                ld      a, (disasm_m3_sf)
                or      a
                jp      z, disasm_not_mul3       ; widening/high ops 64-bit only
disasm_m3_ok:

; Past the last decline — commit; save BC/IX.
                push    bc
                push    ix

                ld      a, (disasm_m3_op31)
                cp      0
                jp      z, disasm_m3_madd
                cp      2
                jp      z, disasm_m3_smulh
                cp      6
                jp      z, disasm_m3_umulh
; op31 001 (smaddl/smsubl/smull/smnegl) or 101 (umaddl/...): widening.
; Select the signed/unsigned text bank by op31 bit2.
                cp      1
                jp      z, disasm_m3_swiden
                jp      disasm_m3_uwiden


; --- madd / msub / mul / mneg (op31 000, full width) ------------------
disasm_m3_madd:
                ld      a, (disasm_m3_ra)
                cp      31
                jr      nz, disasm_m3_madd_full
; Ra==31 → mul (o0=0) / mneg (o0=1): "Rd, Rn, Rm".
                ld      a, (disasm_m3_o0)
                or      a
                ld      hl, disasm_m3_mul_txt
                jr      z, disasm_m3_madd_3
                ld      hl, disasm_m3_mneg_txt
disasm_m3_madd_3:
                call    disasm_m3_set_mnem
                ld      hl, DISASM_COMM_OPS
                ld      a, (disasm_m3_sf)
                ld      (disasm_m3_w), a         ; full width for all 3 regs
                call    disasm_m3_emit_rd_rn_rm
                ld      (hl), 0
                jp      disasm_m3_done
disasm_m3_madd_full:
; madd (o0=0) / msub (o0=1): "Rd, Rn, Rm, Ra".
                ld      a, (disasm_m3_o0)
                or      a
                ld      hl, disasm_m3_madd_txt
                jr      z, disasm_m3_madd_4
                ld      hl, disasm_m3_msub_txt
disasm_m3_madd_4:
                call    disasm_m3_set_mnem
                ld      hl, DISASM_COMM_OPS
                ld      a, (disasm_m3_sf)
                ld      (disasm_m3_w), a
                call    disasm_m3_emit_rd_rn_rm
                ld      (hl), ","
                inc     hl
                ld      (hl), " "
                inc     hl
; Ra at full width.
                ld      a, (disasm_m3_ra)
                ld      c, a
                ld      a, (disasm_m3_sf)
                ld      b, a
                call    disasm_br_emit_reg
                ld      (hl), 0
                jp      disasm_m3_done


; --- smulh / umulh (op31 010/110, all X-regs) -------------------------
disasm_m3_smulh:
                ld      hl, disasm_m3_smulh_txt
                jr      disasm_m3_high
disasm_m3_umulh:
                ld      hl, disasm_m3_umulh_txt
disasm_m3_high:
                call    disasm_m3_set_mnem
                ld      hl, DISASM_COMM_OPS
                ld      a, 1
                ld      (disasm_m3_w), a         ; X regs
                call    disasm_m3_emit_rd_rn_rm
                ld      (hl), 0
                jp      disasm_m3_done


; --- widening: Xd, Wn, Wm[, Xa] (op31 001 signed / 101 unsigned) ------
disasm_m3_swiden:
                ld      a, (disasm_m3_ra)
                cp      31
                jr      nz, disasm_m3_sw_4
                ld      a, (disasm_m3_o0)
                or      a
                ld      hl, disasm_m3_smull_txt
                jr      z, disasm_m3_widen3
                ld      hl, disasm_m3_smnegl_txt
                jr      disasm_m3_widen3
disasm_m3_sw_4:
                ld      a, (disasm_m3_o0)
                or      a
                ld      hl, disasm_m3_smaddl_txt
                jr      z, disasm_m3_widen4
                ld      hl, disasm_m3_smsubl_txt
                jr      disasm_m3_widen4
disasm_m3_uwiden:
                ld      a, (disasm_m3_ra)
                cp      31
                jr      nz, disasm_m3_uw_4
                ld      a, (disasm_m3_o0)
                or      a
                ld      hl, disasm_m3_umull_txt
                jr      z, disasm_m3_widen3
                ld      hl, disasm_m3_umnegl_txt
                jr      disasm_m3_widen3
disasm_m3_uw_4:
                ld      a, (disasm_m3_o0)
                or      a
                ld      hl, disasm_m3_umaddl_txt
                jr      z, disasm_m3_widen4
                ld      hl, disasm_m3_umsubl_txt
                ; fall through

; widen4: "Xd, Wn, Wm, Xa".
disasm_m3_widen4:
                call    disasm_m3_set_mnem
                ld      hl, DISASM_COMM_OPS
                call    disasm_m3_emit_widen_drnm   ; Xd, Wn, Wm
                ld      (hl), ","
                inc     hl
                ld      (hl), " "
                inc     hl
                ld      a, (disasm_m3_ra)
                ld      c, a
                ld      b, 1                        ; Xa
                call    disasm_br_emit_reg
                ld      (hl), 0
                jp      disasm_m3_done

; widen3: "Xd, Wn, Wm".
disasm_m3_widen3:
                call    disasm_m3_set_mnem
                ld      hl, DISASM_COMM_OPS
                call    disasm_m3_emit_widen_drnm
                ld      (hl), 0
                jp      disasm_m3_done

; "Xd, Wn, Wm" — Rd is X, Rn/Rm are W.  HL=dest, advanced.
disasm_m3_emit_widen_drnm:
                ld      a, (disasm_m3_rd)
                ld      c, a
                ld      b, 1
                call    disasm_br_emit_reg
                ld      (hl), ","
                inc     hl
                ld      (hl), " "
                inc     hl
                ld      a, (disasm_m3_rn)
                ld      c, a
                ld      b, 0
                call    disasm_br_emit_reg
                ld      (hl), ","
                inc     hl
                ld      (hl), " "
                inc     hl
                ld      a, (disasm_m3_rm)
                ld      c, a
                ld      b, 0
                call    disasm_br_emit_reg
                ret


; "Rd, Rn, Rm" at width disasm_m3_w.  HL=dest, advanced.
disasm_m3_emit_rd_rn_rm:
                ld      a, (disasm_m3_rd)
                ld      c, a
                ld      a, (disasm_m3_w)
                ld      b, a
                call    disasm_br_emit_reg
                ld      (hl), ","
                inc     hl
                ld      (hl), " "
                inc     hl
                ld      a, (disasm_m3_rn)
                ld      c, a
                ld      a, (disasm_m3_w)
                ld      b, a
                call    disasm_br_emit_reg
                ld      (hl), ","
                inc     hl
                ld      (hl), " "
                inc     hl
                ld      a, (disasm_m3_rm)
                ld      c, a
                ld      a, (disasm_m3_w)
                ld      b, a
                call    disasm_br_emit_reg
                ret

disasm_m3_set_mnem:
                ld      de, DISASM_COMM_MNEM
disasm_m3_set_mnem_loop:
                ld      a, (hl)
                ld      (de), a
                or      a
                ret     z
                inc     hl
                inc     de
                jr      disasm_m3_set_mnem_loop

disasm_m3_done:
                pop     ix
                pop     bc
                ret

disasm_m3_madd_txt:     defm    "madd"
                        defb    0
disasm_m3_msub_txt:     defm    "msub"
                        defb    0
disasm_m3_mul_txt:      defm    "mul"
                        defb    0
disasm_m3_mneg_txt:     defm    "mneg"
                        defb    0
disasm_m3_smaddl_txt:   defm    "smaddl"
                        defb    0
disasm_m3_smsubl_txt:   defm    "smsubl"
                        defb    0
disasm_m3_smull_txt:    defm    "smull"
                        defb    0
disasm_m3_smnegl_txt:   defm    "smnegl"
                        defb    0
disasm_m3_umaddl_txt:   defm    "umaddl"
                        defb    0
disasm_m3_umsubl_txt:   defm    "umsubl"
                        defb    0
disasm_m3_umull_txt:    defm    "umull"
                        defb    0
disasm_m3_umnegl_txt:   defm    "umnegl"
                        defb    0
disasm_m3_smulh_txt:    defm    "smulh"
                        defb    0
disasm_m3_umulh_txt:    defm    "umulh"
                        defb    0

disasm_m3_sf:           defb    0
disasm_m3_op31:         defb    0
disasm_m3_rm:           defb    0
disasm_m3_o0:           defb    0
disasm_m3_ra:           defb    0
disasm_m3_rn:           defb    0
disasm_m3_rd:           defb    0
disasm_m3_w:            defb    0


; =======================================================================
; Data-processing (2-source) variable shift (LSLV/LSRV/ASRV/RORV) → the
; lsl/lsr/asr/ror register-form aliases.  Z80 port of decodeShiftVarAlias
; (tools/aarch64dec/aliases.go:823).
;
; Encoding: sf | 0 | 0 | 11010110 | Rm(5) | opcode2(6) | Rn(5) | Rd(5)
;   opcode2 001000 LSLV  001001 LSRV  001010 ASRV  001011 RORV.
;
; Discriminator bits[30:21] == 0b0011010110 = ((B&0x7f)<<3)|(C>>5).  We
; check bits[28:21]==0b11010110 (=0xD6) and bits[30:29]==00 separately.
; Other opcode2 values (udiv/sdiv/crc32/...) keep their base form, so they
; decline to .inst.
;
; ABI: BC/IX saved after the last decline, restored via disasm_sv_done.
; =======================================================================
disasm_try_shiftvar:
; bits[28:21] == 0b11010110 = ((B&0x1f)<<3)|(C>>5) == 0xd6.
                ld      a, b
                and     &1f
                add     a, a
                add     a, a
                add     a, a
                ld      l, a
                ld      a, c
                rlca
                rlca
                rlca
                and     7
                or      l
                cp      &d6
                jp      nz, disasm_not_shiftvar
; bits[30:29] (op,S = (B>>5)&3) must be 00.
                ld      a, b
                rlca
                rlca
                rlca
                and     3
                jp      nz, disasm_not_shiftvar
; opcode2 = (D>>2)&0x3f  (bits[15:10]).  Two sub-families share this
; dp-2-source space here:
;   001000..001011  variable shift  → lsl/lsr/asr/ror  (decodeShiftVarAlias)
;   000010          udiv            (decodeDP2Source)
;   000011          sdiv            (decodeDP2Source)
; Keep B,C,D,E intact on the decline path: only A and scratch are touched.
                ld      a, d
                rrca
                rrca
                and     &3f
                ld      (disasm_sv_opc2), a      ; stash opcode2 (E must stay intact)
; variable-shift?  high bits 001000 → (opcode2 & ~3)==8.
                and     &fc
                cp      8
                jr      z, disasm_sv_fields      ; variable shift
; udiv/sdiv?  opcode2 == 0000010 (udiv) / 0000011 (sdiv) → (opcode2 & ~1)==2.
                ld      a, (disasm_sv_opc2)
                and     &fe
                cp      2
                jp      nz, disasm_not_shiftvar  ; neither sub-family → .inst
; (mnemonic is selected after field extraction, which clobbers D.)
disasm_sv_fields:
; sf = B>>7.
                ld      a, b
                rlca
                and     1
                ld      (disasm_sv_sf), a
; Rm = C&0x1f.
                ld      a, c
                and     &1f
                ld      (disasm_sv_rm), a
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
                ld      (disasm_sv_rn), a
; Rd = E&0x1f.
                ld      a, e
                and     &1f
                ld      (disasm_sv_rd), a

; Past the last decline — commit; save BC/IX.
                push    bc
                push    ix

; Select the mnemonic now that the word bytes (D) are no longer needed.
;   opcode2 001000..001011 → variable shift (lsl/lsr/asr/ror) by low 2 bits
;   opcode2 0000010/0000011 → udiv/sdiv by bit0
                ld      a, (disasm_sv_opc2)
                and     &fc
                cp      8
                jr      nz, disasm_sv_mnem_div
; variable shift: table indexed by (opcode2 & 3)*4.
                ld      a, (disasm_sv_opc2)
                and     3
                add     a, a
                add     a, a                     ; sel*4
                ld      e, a
                ld      d, 0
                ld      hl, disasm_sv_tbl
                add     hl, de
                jr      disasm_sv_mnem_set
disasm_sv_mnem_div:
                ld      hl, disasm_sv_udiv_txt
                ld      a, (disasm_sv_opc2)
                bit     0, a
                jr      z, disasm_sv_mnem_set
                ld      hl, disasm_sv_sdiv_txt
disasm_sv_mnem_set:
                call    disasm_sv_set_mnem
; operands: "Rd, Rn, Rm" at sf width.
                ld      hl, DISASM_COMM_OPS
                ld      a, (disasm_sv_rd)
                ld      c, a
                ld      a, (disasm_sv_sf)
                ld      b, a
                call    disasm_br_emit_reg
                ld      (hl), ","
                inc     hl
                ld      (hl), " "
                inc     hl
                ld      a, (disasm_sv_rn)
                ld      c, a
                ld      a, (disasm_sv_sf)
                ld      b, a
                call    disasm_br_emit_reg
                ld      (hl), ","
                inc     hl
                ld      (hl), " "
                inc     hl
                ld      a, (disasm_sv_rm)
                ld      c, a
                ld      a, (disasm_sv_sf)
                ld      b, a
                call    disasm_br_emit_reg
                ld      (hl), 0
                jp      disasm_sv_done

disasm_sv_set_mnem:
                ld      de, DISASM_COMM_MNEM
disasm_sv_set_mnem_loop:
                ld      a, (hl)
                ld      (de), a
                or      a
                ret     z
                inc     hl
                inc     de
                jr      disasm_sv_set_mnem_loop

disasm_sv_done:
                pop     ix
                pop     bc
                ret

; mnemonic table, 4 bytes per entry (3 chars + NUL), indexed sel*4.
disasm_sv_tbl:
                defm    "lsl"
                defb    0
                defm    "lsr"
                defb    0
                defm    "asr"
                defb    0
                defm    "ror"
                defb    0
disasm_sv_udiv_txt:     defm    "udiv"
                        defb    0
disasm_sv_sdiv_txt:     defm    "sdiv"
                        defb    0

disasm_sv_opc2:         defb    0       ; opcode2 bits[15:10] (sub-family select)
disasm_sv_sf:           defb    0
disasm_sv_rm:           defb    0
disasm_sv_rn:           defb    0
disasm_sv_rd:           defb    0


; =======================================================================
; EXTR (extract register) / ROR-immediate — Z80 port of tryDecodeExtr
; (tools/aarch64dec/aliases.go:778).
;
; Encoding (ARM ARM C6.2.72):
;   sf | 00100111 | N | 0 | Rm(5) | imms(6) | Rn(5) | Rd(5)
;
;   mask 0xFFE00000 captures bits[31:21]:
;     32-bit: sf=0, N=0 → pattern 0x13800000 → B=0x13, C & 0xE0 = 0x80
;     64-bit: sf=1, N=1 → pattern 0x93C00000 → B=0x93, C & 0xE0 = 0xC0
;
;   Discriminator: bits[30:24] == 0x13 (B & 0x7f == 0x13), and
;   bit23=1 (C & 0x80 == 0x80), bit21=0 (C & 0x20 == 0).
;   Combined: (B & 0x7f == 0x13) AND (C & 0xA0 == 0x80).
;   Additionally N (bit22 = (C>>6)&1) must equal sf.
;
; When Rm==Rn: `ror Rd, Rn, #imms`.  When Rm!=Rn: `extr Rd, Rn, Rm, #imms`.
;
; Fields (B=bits31:24, C=bits23:16, D=bits15:8, E=bits7:0):
;   sf = B >> 7
;   Rm = C & 0x1f
;   imms = (D >> 2) & 0x3f
;   Rn = ((D & 3) << 3) | (E >> 5)
;   Rd = E & 0x1f
; =======================================================================
disasm_try_extr:
; bits[30:24] must equal 0x13: (B & 0x7f) == 0x13.
                ld      a, b
                and     &7f
                cp      &13
                jp      nz, disasm_not_extr
; bit23=1 and bit21=0: (C & 0xA0) == 0x80.
                ld      a, c
                and     &a0
                cp      &80
                jp      nz, disasm_not_extr
; N (bit22 = (C>>6)&1) must equal sf (B>>7).
                ld      a, c
                rlca
                rlca
                and     1                    ; N
                ld      l, a
                ld      a, b
                rlca
                and     1                    ; sf
                cp      l                    ; sf == N?
                jp      nz, disasm_not_extr
; sf.
                ld      (disasm_ex_sf), a
; Rm = C & 0x1f.
                ld      a, c
                and     &1f
                ld      (disasm_ex_rm), a
; imms = (D >> 2) & 0x3f.
                ld      a, d
                rrca
                rrca
                and     &3f
                ld      (disasm_ex_imms), a
; Rn = ((D & 3) << 3) | (E >> 5).
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
                ld      (disasm_ex_rn), a
; Rd = E & 0x1f.
                ld      a, e
                and     &1f
                ld      (disasm_ex_rd), a

; Past the last decline — commit; save BC/IX.
                push    bc
                push    ix

; Rm==Rn? → ror; else → extr.
                ld      a, (disasm_ex_rm)
                ld      l, a
                ld      a, (disasm_ex_rn)
                cp      l
                jr      nz, disasm_ex_extr

; ror Rd, Rn, #imms.
                ld      hl, disasm_dpr_ror_txt
                call    disasm_ex_set_mnem
                ld      hl, DISASM_COMM_OPS
                call    disasm_ex_emit_rd
                ld      (hl), ","
                inc     hl
                ld      (hl), " "
                inc     hl
                call    disasm_ex_emit_rn
                ld      (hl), ","
                inc     hl
                ld      (hl), " "
                inc     hl
                ld      (hl), "#"
                inc     hl
                ld      a, (disasm_ex_imms)
                ld      e, a
                ld      d, 0
                call    disasm_emit_dec16
                ld      (hl), 0
                jp      disasm_ex_done

; extr Rd, Rn, Rm, #imms.
disasm_ex_extr:
                ld      hl, disasm_ex_extr_txt
                call    disasm_ex_set_mnem
                ld      hl, DISASM_COMM_OPS
                call    disasm_ex_emit_rd
                ld      (hl), ","
                inc     hl
                ld      (hl), " "
                inc     hl
                call    disasm_ex_emit_rn
                ld      (hl), ","
                inc     hl
                ld      (hl), " "
                inc     hl
; Rm at full width (same as Rn for EXTR).
                ld      a, (disasm_ex_rm)
                ld      c, a
                ld      a, (disasm_ex_sf)
                ld      b, a
                call    disasm_br_emit_reg
                ld      (hl), ","
                inc     hl
                ld      (hl), " "
                inc     hl
                ld      (hl), "#"
                inc     hl
                ld      a, (disasm_ex_imms)
                ld      e, a
                ld      d, 0
                call    disasm_emit_dec16
                ld      (hl), 0
                jp      disasm_ex_done

disasm_ex_emit_rd:
                ld      a, (disasm_ex_rd)
                ld      c, a
                ld      a, (disasm_ex_sf)
                ld      b, a
                jp      disasm_br_emit_reg

disasm_ex_emit_rn:
                ld      a, (disasm_ex_rn)
                ld      c, a
                ld      a, (disasm_ex_sf)
                ld      b, a
                jp      disasm_br_emit_reg

disasm_ex_set_mnem:
                ld      de, DISASM_COMM_MNEM
disasm_ex_set_mnem_loop:
                ld      a, (hl)
                ld      (de), a
                or      a
                ret     z
                inc     hl
                inc     de
                jr      disasm_ex_set_mnem_loop

disasm_ex_done:
                pop     ix
                pop     bc
                ret

disasm_ex_extr_txt:     defm    "extr"
                        defb    0

disasm_ex_sf:           defb    0
disasm_ex_rm:           defb    0
disasm_ex_imms:         defb    0
disasm_ex_rn:           defb    0
disasm_ex_rd:           defb    0


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
                cp      &06                 ; 0b0110 literal LDR (needs PC)
                jp      z, disasm_mem_literal_grp
                jp      disasm_not_mem

disasm_mem_literal_grp:
; LDR (literal) requires bits[25:24]==00 (B&3==0); other shares of bits29:26
; ==0110 with bits25:24!=00 are csel / dp-3-source — decline so the form
; walk / aliases handle them.
                ld      a, b
                and     3
                jp      nz, disasm_not_mem
                jp      disasm_mem_literal


; -----------------------------------------------------------------------
; disasm_mem_literal — LDR (literal) family.  bits29:26==0110, bits25:24==00.
; Z80 port of aarch64dec decodeLiteralMem (mem.go).  opc[31:30] picks the
; mnemonic + width:  00 ldr Wt ; 01 ldr Xt ; 10 ldrsw Xt ; 11 → decline
; (PRFM literal, not in our set).  target = pc + sext(imm19)<<2, where
; imm19 = bits[23:5].  Renders `<mnem> <reg>, 0x<target>`.
; -----------------------------------------------------------------------
disasm_mem_literal:
                call    disasm_br_save_word
; opc = (wb>>6)&3.
                ld      a, (disasm_br_wb)
                rlca
                rlca
                and     3
                ld      (disasm_mem_lit_opc), a
                cp      3
                jp      z, disasm_not_mem           ; opc 11 PRFM → decline
; Rt = we&0x1f ; width: opc 00 → w (is64=0), else x (is64=1).
                ld      a, (disasm_br_we)
                and     &1f
                ld      (disasm_br_rt), a
                ld      a, (disasm_mem_lit_opc)
                or      a
                ld      a, 0
                jr      z, disasm_mem_lit_w
                ld      a, 1
disasm_mem_lit_w:
                ld      (disasm_br_is64), a
; imm19 → off, sign-extend width 19, shift 2, pc base (sets adrp=0).
; The builders + compute_target clobber BC/IX; disasm_br_done reconstructs
; the original word from the saved bytes, so no push/pop is needed here.
                call    disasm_br_build_imm19
                call    disasm_br_compute_target
; mnemonic: opc 10 → ldrsw, else ldr.
                ld      hl, disasm_mem_ldr_str
                ld      a, (disasm_mem_lit_opc)
                cp      2
                jr      nz, disasm_mem_lit_setm
                ld      hl, disasm_mem_ldrsw_str
disasm_mem_lit_setm:
                call    disasm_asi_set_mnem
; operands: "<reg>, 0x<target>".
                ld      hl, DISASM_COMM_OPS
                ld      a, (disasm_br_rt)
                ld      c, a
                ld      a, (disasm_br_is64)
                ld      b, a
                call    disasm_br_emit_reg
                ld      (hl), ","
                inc     hl
                ld      (hl), " "
                inc     hl
                call    disasm_br_emit_target
                ld      (hl), 0
                jp      disasm_br_done

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
disasm_mem_lit_opc:     defb    0       ; LDR-literal opc (00 ldr-w/01 ldr-x/10 ldrsw)


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


; =======================================================================
; System instruction group — Z80 port of tools/aarch64dec/sys.go
; (decodeSys).  Decodes mrs / msr (register + immediate) / dc / at / ic /
; tlbi / dsb / dmb / isb, plus the exact-word hints eret and wfi.
;
; Encoding (the group): bits[31:22] == 0b1101010100, i.e. B == 0xd5 and
; (C & 0xc0) == 0 (word & 0xffc00000 == 0xd5000000).  Two exact hint words
; sit just outside that mask and are matched directly: eret 0xd69f03e0 and
; wfi 0xd503207f (nop 0xd503201f is special-cased earlier; all other hints
; do not occur in release.img and decline to .inst, matching the Go form
; walk which carries only nop/wfi/eret as exact patterns).
;
; Field layout (word = B:C:D:E, B = bits31:24):
;   l      = bit21       = (C>>5)&1
;   op0fld = bits[20:19] = (C>>3)&3      (the switch discriminant)
;   op1    = bits[18:16] = C&7
;   CRn    = bits[15:12] = (D>>4)&0xf
;   CRm    = bits[11:8]  = D&0xf
;   op2    = bits[7:5]   = (E>>5)&7
;   Rt     = bits[4:0]   = E&0x1f
;
; ABI: clobbers BC/IX on success; B,C,D,E are never modified by the decline
; checks (only A/HL/scratch), so decline paths reach disasm_not_sys with the
; word intact.  Success paths reconstruct BC/IX via disasm_sys_done.
; =======================================================================
disasm_try_sys:
; Save the word bytes to scratch up front so success paths can clobber
; BC/IX freely and reconstruct them in disasm_sys_done.  Decline paths
; never modify B,C,D,E, so they reach disasm_not_sys with the word intact
; regardless of these scratch writes.
                ld      a, b
                ld      (disasm_sys_wb), a
                ld      a, c
                ld      (disasm_sys_wc), a
                ld      a, d
                ld      (disasm_sys_wd), a
                ld      a, e
                ld      (disasm_sys_we), a

; --- exact-word hints: eret (0xd69f03e0) -------------------------------
                ld      a, b
                cp      &d6
                jr      nz, disasm_sys_not_eret
                ld      a, c
                cp      &9f
                jr      nz, disasm_sys_not_eret
                ld      a, d
                cp      &03
                jr      nz, disasm_sys_not_eret
                ld      a, e
                cp      &e0
                jr      nz, disasm_sys_not_eret
                ld      hl, disasm_sys_eret_txt
                jp      disasm_sys_emit_mnem_noops
disasm_sys_not_eret:

; --- group discriminator: B==0xd5 and (C&0xc0)==0 ----------------------
                ld      a, b
                cp      &d5
                jp      nz, disasm_not_sys
                ld      a, c
                and     &c0
                jp      nz, disasm_not_sys

; --- exact-word hint: wfi (0xd503207f) ---------------------------------
; (Inside the group: C==0x03, D==0x20, E==0x7f.)
                ld      a, c
                cp      &03
                jr      nz, disasm_sys_not_wfi
                ld      a, d
                cp      &20
                jr      nz, disasm_sys_not_wfi
                ld      a, e
                cp      &7f
                jr      nz, disasm_sys_not_wfi
                ld      hl, disasm_sys_wfi_txt
                jp      disasm_sys_emit_mnem_noops
disasm_sys_not_wfi:

; Extract the common fields into page scratch (B,C,D,E left intact).
                ld      a, c
                and     7
                ld      (disasm_sys_op1), a           ; op1 = C&7
                ld      a, c
                rrca
                rrca
                rrca
                and     3
                ld      (disasm_sys_op0f), a          ; op0fld = (C>>3)&3
                ld      a, d
                rrca
                rrca
                rrca
                rrca
                and     &0f
                ld      (disasm_sys_crn), a           ; CRn = (D>>4)&0xf
                ld      a, d
                and     &0f
                ld      (disasm_sys_crm), a           ; CRm = D&0xf
                ld      a, e
                rlca
                rlca
                rlca
                and     7
                ld      (disasm_sys_op2), a           ; op2 = (E>>5)&7
                ld      a, e
                and     &1f
                ld      (disasm_sys_rt), a            ; Rt = E&0x1f
; L = bit21 (mrs vs msr), stashed.
                ld      a, c
                rlca
                rlca
                rlca
                and     1
                ld      (disasm_sys_l), a             ; L = (C>>5)&1

; Switch on op0fld (the bits[20:19] discriminant).
                ld      a, (disasm_sys_op0f)
                or      a
                jp      z, disasm_sys_op0_zero        ; 00 → barriers / msr-imm / hint
                cp      1
                jp      z, disasm_sys_instr           ; 01 → dc / at / ic / tlbi
; 10 / 11 → mrs / msr register form.
                jp      disasm_sys_mrsmsr


; -----------------------------------------------------------------------
; op0fld == 00: CRn picks the sub-family.  CRn==3 barriers; CRn==4 msr-imm;
; CRn==2 is the hint space (wfi already handled, nop special-cased) → all
; other hints decline.  Anything else declines to .inst.
; -----------------------------------------------------------------------
disasm_sys_op0_zero:
                ld      a, (disasm_sys_crn)
                cp      3
                jp      z, disasm_sys_barrier
                cp      4
                jp      z, disasm_sys_msrimm
                jp      disasm_not_sys


; -----------------------------------------------------------------------
; Barriers: dsb / dmb / isb.  op2 4→dsb 5→dmb 6→isb; Rt must be 11111.
; CRm carries the option.
; -----------------------------------------------------------------------
disasm_sys_barrier:
                ld      a, (disasm_sys_rt)
                cp      &1f
                jp      nz, disasm_not_sys           ; Rt != 31 → not a barrier
                ld      a, (disasm_sys_op2)
                cp      4
                jr      z, disasm_sys_dsb
                cp      5
                jr      z, disasm_sys_dmb
                cp      6
                jr      z, disasm_sys_isb
                jp      disasm_not_sys

disasm_sys_dsb:
                ld      hl, disasm_sys_dsb_txt
                call    disasm_sys_set_mnem
                ld      hl, DISASM_COMM_OPS
                ld      a, 1                          ; isDsb = 1
                call    disasm_sys_emit_baropt
                jp      disasm_sys_done

disasm_sys_dmb:
                ld      hl, disasm_sys_dmb_txt
                call    disasm_sys_set_mnem
                ld      hl, DISASM_COMM_OPS
                xor     a                            ; isDsb = 0
                call    disasm_sys_emit_baropt
                jp      disasm_sys_done

disasm_sys_isb:
; objdump: bare "isb" when CRm==15 (sy); else "isb #0xN".
                ld      hl, disasm_sys_isb_txt
                call    disasm_sys_set_mnem
                ld      a, (disasm_sys_crm)
                cp      &0f
                jr      z, disasm_sys_isb_bare
                ld      hl, DISASM_COMM_OPS
                ld      (hl), "#"
                inc     hl
                ld      (hl), "0"
                inc     hl
                ld      (hl), "x"
                inc     hl
                ld      a, (disasm_sys_crm)           ; CRm 0..14 → one hex digit
                call    disasm_emit_hex_nibble
                ld      (hl), 0
                jp      disasm_sys_done
disasm_sys_isb_bare:
                ld      hl, DISASM_COMM_OPS
                ld      (hl), 0
                jp      disasm_sys_done


; -----------------------------------------------------------------------
; disasm_sys_emit_baropt — emit the dsb/dmb barrier option keyword for
; (disasm_sys_crm) to (HL), advancing HL and NUL-terminating.  A = isDsb.
; Mirrors barrierOption (sys.go): named CRm → keyword; CRm 0/4 → ssbb/pssbb
; for dsb only; otherwise "#0xNN" (two hex digits, like Go "%#02x").
; Clobbers A, BC, DE, HL.
; -----------------------------------------------------------------------
disasm_sys_emit_baropt:
                ld      (disasm_sys_isdsb), a
; Look up CRm in the option-name table (16 entries, 0 ptr = unnamed).
                ld      a, (disasm_sys_crm)
                add     a, a                          ; CRm*2 (word entries)
                ld      e, a
                ld      d, 0
                ld      ix, disasm_sys_baropt_tbl
                add     ix, de
                ld      a, (ix+0)
                ld      e, a
                ld      a, (ix+1)
                ld      d, a                          ; DE = name ptr (0 if none)
                ld      a, d
                or      e
                jr      z, disasm_sys_baropt_special  ; no general name → ssbb/#imm
; Copy the named keyword (DE) to (HL).
                ex      de, hl                        ; HL=src ptr, DE=dest
disasm_sys_baropt_copy:
                ld      a, (hl)
                ld      (de), a
                or      a
                jr      z, disasm_sys_baropt_copied
                inc     hl
                inc     de
                jr      disasm_sys_baropt_copy
disasm_sys_baropt_copied:
                ex      de, hl                        ; HL = dest (at the NUL)
                ret
disasm_sys_baropt_special:
; CRm has no shared name.  For dsb: CRm 0→ssbb, 4→pssbb.  Else "#0xNN".
                ld      a, (disasm_sys_isdsb)
                or      a
                jr      z, disasm_sys_baropt_imm      ; dmb → numeric
                ld      a, (disasm_sys_crm)
                or      a
                jr      nz, disasm_sys_baropt_chk4
                ld      hl, DISASM_COMM_OPS
                ld      de, disasm_sys_ssbb_txt
                jr      disasm_sys_baropt_cpyspec
disasm_sys_baropt_chk4:
                cp      4
                jr      nz, disasm_sys_baropt_imm
                ld      hl, DISASM_COMM_OPS
                ld      de, disasm_sys_pssbb_txt
disasm_sys_baropt_cpyspec:
                ex      de, hl                        ; HL=src, DE=dest
disasm_sys_baropt_cpyspec_loop:
                ld      a, (hl)
                ld      (de), a
                or      a
                jr      z, disasm_sys_baropt_cpyspec_done
                inc     hl
                inc     de
                jr      disasm_sys_baropt_cpyspec_loop
disasm_sys_baropt_cpyspec_done:
                ex      de, hl
                ret
disasm_sys_baropt_imm:
; "#0xNN" — two hex digits (Go "%#02x").
                ld      hl, DISASM_COMM_OPS
                ld      (hl), "#"
                inc     hl
                ld      (hl), "0"
                inc     hl
                ld      (hl), "x"
                inc     hl
                ld      a, (disasm_sys_crm)
                call    disasm_emit_hex_byte          ; CRm < 16 → "0N"
                ld      (hl), 0
                ret


; -----------------------------------------------------------------------
; op0fld == 00, CRn==4: msr (immediate) PSTATE form.  Rt must be 11111.
; (op1,op2) → pstate field name; CRm is the 4-bit immediate.
;   "msr <field>, #0xN"
; -----------------------------------------------------------------------
disasm_sys_msrimm:
                ld      a, (disasm_sys_rt)
                cp      &1f
                jp      nz, disasm_not_sys
; Look up (op1,op2) in the pstate table (search by field bytes).
                ld      ix, disasm_pstate_tbl
                call    disasm_sys_find_pstate
                jp      z, disasm_not_sys             ; unknown pstate → .inst
; HL = pstate record ptr.  Stash it, set the "msr" mnemonic, then build ops.
                ld      (disasm_sys_nameptr), hl
                call    disasm_sys_set_mnem_msr       ; mnem = "msr"
; operands: "<field>, #0x<crm-nibble>".
                ld      hl, (disasm_sys_nameptr)      ; HL = src record
                ld      de, DISASM_COMM_OPS           ; DE = dest
                call    disasm_sys_copy_named         ; DE advanced past name
                ex      de, hl                        ; HL = dest after name
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
                ld      a, (disasm_sys_crm)
                call    disasm_emit_hex_nibble        ; imm 0..15 → one digit
                ld      (hl), 0
                jp      disasm_sys_done


; -----------------------------------------------------------------------
; op0fld == 01: SYS / SYSL — dc / at / ic (CRn==7) / tlbi (CRn==8).
; -----------------------------------------------------------------------
disasm_sys_instr:
                ld      a, (disasm_sys_crn)
                cp      8
                jp      z, disasm_sys_tlbi
                cp      7
                jp      z, disasm_sys_dc_at_ic
                jp      disasm_not_sys

disasm_sys_tlbi:
; Search disasm_tlbi_tbl by (op1,CRn,CRm,op2); entry trailing byte = NeedsXt.
                ld      ix, disasm_tlbi_tbl
                call    disasm_sys_find_tlbi          ; Z=miss; HL=name, sets needsxt
                jp      z, disasm_not_sys
                push    hl                            ; save name ptr
                push    bc
                push    ix
                ld      hl, disasm_sys_tlbi_txt
                call    disasm_sys_set_mnem
                pop     ix
                pop     bc
                pop     hl                            ; HL = name ptr
                jp      disasm_sys_emit_op_name

disasm_sys_dc_at_ic:
; Try dc table first (search by op1,CRn,CRm,op2).
                ld      ix, disasm_dc_tbl
                call    disasm_sys_find_dc
                jr      z, disasm_sys_try_at
                ld      a, 1
                ld      (disasm_sys_needsxt), a       ; all dc ops take Xt
                push    hl
                push    bc
                push    ix
                ld      hl, disasm_sys_dc_txt
                call    disasm_sys_set_mnem
                pop     ix
                pop     bc
                pop     hl
                jp      disasm_sys_emit_op_name
disasm_sys_try_at:
; AT ops (CRm 8, all take Xt) — small inline table.
                ld      a, (disasm_sys_crm)
                cp      8
                jr      nz, disasm_sys_try_ic
                ld      ix, disasm_sys_at_tbl
                call    disasm_sys_find_atic          ; match by (op1,op2); Z=miss
                jr      z, disasm_sys_try_ic
                ld      a, 1
                ld      (disasm_sys_needsxt), a
                push    hl
                push    bc
                push    ix
                ld      hl, disasm_sys_at_txt
                call    disasm_sys_set_mnem
                pop     ix
                pop     bc
                pop     hl
                jp      disasm_sys_emit_op_name
disasm_sys_try_ic:
; IC ops (CRm 1/5).  ialluis/iallu take no Xt; ivau takes Xt.  find_atic
; matches on (op1,CRm,op2) and sets disasm_sys_needsxt from the table.
                ld      ix, disasm_sys_ic_tbl
                call    disasm_sys_find_atic
                jp      z, disasm_not_sys
                push    hl
                push    bc
                push    ix
                ld      hl, disasm_sys_ic_txt
                call    disasm_sys_set_mnem
                pop     ix
                pop     bc
                pop     hl
                jp      disasm_sys_emit_op_name


; -----------------------------------------------------------------------
; disasm_sys_emit_op_name — common tail for dc/at/ic/tlbi.  HL = ptr to the
; length-prefixed op-name record.  Writes the name to DISASM_COMM_OPS, then
; appends ", x<Rt>" (xzr at 31) when (disasm_sys_needsxt) != 0.
; -----------------------------------------------------------------------
disasm_sys_emit_op_name:
                ld      de, DISASM_COMM_OPS
                call    disasm_sys_copy_named         ; HL=record, DE=dest after
                ex      de, hl                        ; HL = dest (at end)
                ld      a, (disasm_sys_needsxt)
                or      a
                jr      z, disasm_sys_eon_done
                ld      (hl), ","
                inc     hl
                ld      (hl), " "
                inc     hl
                ld      a, (disasm_sys_rt)
                ld      c, a
                ld      b, 1                          ; x-width
                call    disasm_br_emit_reg            ; x<Rt> / xzr
disasm_sys_eon_done:
                ld      (hl), 0
                jp      disasm_sys_done


; -----------------------------------------------------------------------
; op0fld 10/11: mrs / msr register form.
;   op0 = 2 | (op0fld & 1) ; L picks mrs (1) / msr (0).
;   mrs x<Rt>, <sysreg>      msr <sysreg>, x<Rt>
; <sysreg> = named lookup, else generic s<op0>_<op1>_c<CRn>_c<CRm>_<op2>.
; -----------------------------------------------------------------------
disasm_sys_mrsmsr:
; op0 = 2 | (op0fld & 1).
                ld      a, (disasm_sys_op0f)
                and     1
                or      2
                ld      (disasm_sys_op0), a
; Resolve the sysreg name into disasm_sys_namebuf (named or generic).
                call    disasm_sys_resolve_sysreg     ; HL → name (NUL-term)
                ld      (disasm_sys_nameptr), hl
; mnem = "mrs" (L==1) / "msr" (L==0).
                ld      a, (disasm_sys_l)
                or      a
                jr      z, disasm_sys_mm_msr
; mrs x<Rt>, <name>
                ld      hl, disasm_sys_mrs_txt
                call    disasm_sys_set_mnem
                ld      hl, DISASM_COMM_OPS
                ld      a, (disasm_sys_rt)
                ld      c, a
                ld      b, 1
                call    disasm_br_emit_reg
                ld      (hl), ","
                inc     hl
                ld      (hl), " "
                inc     hl
                ld      de, (disasm_sys_nameptr)
                call    disasm_sys_copy_de_to_hl
                ld      (hl), 0
                jp      disasm_sys_done
disasm_sys_mm_msr:
; msr <name>, x<Rt>
                ld      hl, disasm_sys_msr_txt
                call    disasm_sys_set_mnem
                ld      hl, DISASM_COMM_OPS
                ld      de, (disasm_sys_nameptr)
                call    disasm_sys_copy_de_to_hl
                ld      (hl), ","
                inc     hl
                ld      (hl), " "
                inc     hl
                ld      a, (disasm_sys_rt)
                ld      c, a
                ld      b, 1
                call    disasm_br_emit_reg
                ld      (hl), 0
                jp      disasm_sys_done


; -----------------------------------------------------------------------
; disasm_sys_resolve_sysreg — set HL to a NUL-terminated sysreg name for
; the (op0,op1,CRn,CRm,op2) in scratch.  Searches disasm_sysreg_tbl; on a
; hit returns the table name ptr.  On a miss builds the generic
; s<op0>_<op1>_c<CRn>_c<CRm>_<op2> spelling into disasm_sys_namebuf and
; returns that.  Clobbers A, BC, DE, HL, IX.
; -----------------------------------------------------------------------
disasm_sys_resolve_sysreg:
                ld      ix, disasm_sysreg_tbl
                call    disasm_sys_find_sysreg        ; Z=miss; HL=record on hit
                jr      z, disasm_sys_rs_generic
; Named — copy the length-prefixed name to namebuf, NUL-terminate, return it.
                ld      de, disasm_sys_namebuf
                call    disasm_sys_copy_named         ; HL=record, DE=dest
                ld      a, 0
                ld      (de), a                       ; NUL-terminate
                ld      hl, disasm_sys_namebuf
                ret
disasm_sys_rs_generic:
; Generic spelling into disasm_sys_namebuf.
                ld      hl, disasm_sys_namebuf
                ld      (hl), "s"
                inc     hl
                ld      a, (disasm_sys_op0)
                call    disasm_sys_emit_u8dec
                ld      (hl), "_"
                inc     hl
                ld      a, (disasm_sys_op1)
                call    disasm_sys_emit_u8dec
                ld      (hl), "_"
                inc     hl
                ld      (hl), "c"
                inc     hl
                ld      a, (disasm_sys_crn)
                call    disasm_sys_emit_u8dec
                ld      (hl), "_"
                inc     hl
                ld      (hl), "c"
                inc     hl
                ld      a, (disasm_sys_crm)
                call    disasm_sys_emit_u8dec
                ld      (hl), "_"
                inc     hl
                ld      a, (disasm_sys_op2)
                call    disasm_sys_emit_u8dec
                ld      (hl), 0
                ld      hl, disasm_sys_namebuf
                ret


; -----------------------------------------------------------------------
; disasm_sys_emit_u8dec — emit A (0..15, the field range) as minimal
; decimal at (HL), advancing HL.  Fields are <=15 so at most two digits.
; Clobbers A, F (and B via the tens loop).
; -----------------------------------------------------------------------
disasm_sys_emit_u8dec:
                cp      10
                jr      c, disasm_sys_u8_units
                ld      b, "0"                        ; tens digit, counts up
disasm_sys_u8_tens:
                inc     b
                sub     10
                cp      10
                jr      nc, disasm_sys_u8_tens
                ld      (hl), b
                inc     hl
disasm_sys_u8_units:
                add     a, "0"
                ld      (hl), a
                inc     hl
                ret


; -----------------------------------------------------------------------
; disasm_sys_copy_de_to_hl — copy NUL-terminated string (DE) to (HL),
; leaving HL at the terminating NUL (not past it).  Clobbers A, DE, HL.
; Used for the generic s<..> spelling (which IS NUL-terminated).
; -----------------------------------------------------------------------
disasm_sys_copy_de_to_hl:
                ld      a, (de)
                or      a
                ret     z
                ld      (hl), a
                inc     hl
                inc     de
                jr      disasm_sys_copy_de_to_hl


; -----------------------------------------------------------------------
; disasm_sys_copy_named — copy a length-prefixed table name to (DE).
; Input: HL = ptr to the [len][name bytes...] record start; DE = dest.
; Output: name bytes copied (NO terminator); DE advanced past them; HL
; advanced past the name.  Clobbers A, B, DE, HL.
; (The sysreg/pstate/dc/tlbi tables store names length-prefixed, not
; NUL-terminated — same layout as src/sysreg_data.asm.)
; -----------------------------------------------------------------------
disasm_sys_copy_named:
                ld      a, (hl)                       ; name_len
                inc     hl
                or      a
                ret     z
                ld      b, a
disasm_sys_copy_named_loop:
                ld      a, (hl)
                ld      (de), a
                inc     hl
                inc     de
                djnz    disasm_sys_copy_named_loop
                ret


; -----------------------------------------------------------------------
; Table finders.  Each walks a [len][name][fields...]-format table (IX =
; base), comparing the field bytes (NOT the name) against scratch.  On a
; hit: returns NZ, HL = name ptr (and A = the relevant trailing field for
; tlbi).  On a miss: returns Z.  Clobber A, BC, DE, HL, IX.
;
; Generic walker disasm_sys_tblwalk advances IX over one record and sets
; DE = name ptr, BC = ptr to first field byte, A = name_len; Z if at the
; 0 terminator.
; -----------------------------------------------------------------------

; --- sysreg: 5 fields (op0,op1,CRn,CRm,op2) ---------------------------
disasm_sys_find_sysreg:
                ld      a, (ix+0)
                or      a
                jr      z, disasm_sys_fs_miss
                ld      c, (ix+0)                     ; name_len
; field offset = 1 + name_len.
                push    ix
                ld      b, 0
                add     ix, bc
                inc     ix                            ; IX → first field
                ld      a, (ix+0)
                ld      hl, disasm_sys_op0
                cp      (hl)
                jr      nz, disasm_sys_fs_next
                ld      a, (ix+1)
                ld      hl, disasm_sys_op1
                cp      (hl)
                jr      nz, disasm_sys_fs_next
                ld      a, (ix+2)
                ld      hl, disasm_sys_crn
                cp      (hl)
                jr      nz, disasm_sys_fs_next
                ld      a, (ix+3)
                ld      hl, disasm_sys_crm
                cp      (hl)
                jr      nz, disasm_sys_fs_next
                ld      a, (ix+4)
                ld      hl, disasm_sys_op2
                cp      (hl)
                jr      nz, disasm_sys_fs_next
; Hit — HL = record start (length byte) = saved IX.
                pop     hl
                or      &ff                           ; NZ
                ret
disasm_sys_fs_next:
                pop     ix                            ; restore record start
                ld      c, (ix+0)
                ld      b, 0
                add     ix, bc                        ; skip name
                ld      bc, 6                         ; 1 (len) + 5 fields
                add     ix, bc
                jr      disasm_sys_find_sysreg
disasm_sys_fs_miss:
                xor     a                             ; Z
                ret


; --- pstate: 2 fields (op1,op2) ---------------------------------------
disasm_sys_find_pstate:
                ld      a, (ix+0)
                or      a
                jr      z, disasm_sys_fp_miss
                ld      c, (ix+0)
                push    ix
                ld      b, 0
                add     ix, bc
                inc     ix
                ld      a, (ix+0)
                ld      hl, disasm_sys_op1
                cp      (hl)
                jr      nz, disasm_sys_fp_next
                ld      a, (ix+1)
                ld      hl, disasm_sys_op2
                cp      (hl)
                jr      nz, disasm_sys_fp_next
                pop     hl                            ; HL = record start (len byte)
                or      &ff
                ret
disasm_sys_fp_next:
                pop     ix
                ld      c, (ix+0)
                ld      b, 0
                add     ix, bc
                ld      bc, 3                          ; 1 + 2
                add     ix, bc
                jr      disasm_sys_find_pstate
disasm_sys_fp_miss:
                xor     a
                ret


; --- dc: 4 fields (op1,CRn,CRm,op2) -----------------------------------
disasm_sys_find_dc:
                ld      a, (ix+0)
                or      a
                jr      z, disasm_sys_fd_miss
                ld      c, (ix+0)
                push    ix
                ld      b, 0
                add     ix, bc
                inc     ix
                ld      a, (ix+0)
                ld      hl, disasm_sys_op1
                cp      (hl)
                jr      nz, disasm_sys_fd_next
                ld      a, (ix+1)
                ld      hl, disasm_sys_crn
                cp      (hl)
                jr      nz, disasm_sys_fd_next
                ld      a, (ix+2)
                ld      hl, disasm_sys_crm
                cp      (hl)
                jr      nz, disasm_sys_fd_next
                ld      a, (ix+3)
                ld      hl, disasm_sys_op2
                cp      (hl)
                jr      nz, disasm_sys_fd_next
                pop     hl                            ; HL = record start (len byte)
                or      &ff
                ret
disasm_sys_fd_next:
                pop     ix
                ld      c, (ix+0)
                ld      b, 0
                add     ix, bc
                ld      bc, 5                          ; 1 + 4
                add     ix, bc
                jr      disasm_sys_find_dc
disasm_sys_fd_miss:
                xor     a
                ret


; --- tlbi: 5 fields (op1,CRn,CRm,op2,NeedsXt); returns A=NeedsXt on hit -
disasm_sys_find_tlbi:
                ld      a, (ix+0)
                or      a
                jr      z, disasm_sys_ft_miss
                ld      c, (ix+0)
                push    ix
                ld      b, 0
                add     ix, bc
                inc     ix
                ld      a, (ix+0)
                ld      hl, disasm_sys_op1
                cp      (hl)
                jr      nz, disasm_sys_ft_next
                ld      a, (ix+1)
                ld      hl, disasm_sys_crn
                cp      (hl)
                jr      nz, disasm_sys_ft_next
                ld      a, (ix+2)
                ld      hl, disasm_sys_crm
                cp      (hl)
                jr      nz, disasm_sys_ft_next
                ld      a, (ix+3)
                ld      hl, disasm_sys_op2
                cp      (hl)
                jr      nz, disasm_sys_ft_next
                ld      a, (ix+4)                      ; NeedsXt
                pop     hl                            ; HL = record start (len byte)
                ld      (disasm_sys_needsxt), a        ; stash NeedsXt
                or      &ff                            ; A=0xFF → NZ (hit)
                ret
disasm_sys_ft_next:
                pop     ix
                ld      c, (ix+0)
                ld      b, 0
                add     ix, bc
                ld      bc, 6                          ; 1 + 5
                add     ix, bc
                jr      disasm_sys_find_tlbi
disasm_sys_ft_miss:
                xor     a
                ret


; --- at/ic inline tables: [op1][CRm][op2][NeedsXt][len][name...] ------
; These tables match by (op1,CRm,op2).  Format per entry:
;   [op1][CRm][op2][len][name bytes...]  terminated by a 0xFF op1 sentinel.
; Returns NZ + HL = name ptr on hit (NeedsXt handled by the dc/at/ic
; dispatch: at always Xt, ic via disasm_sys_needsxt set by the table).
; -----------------------------------------------------------------------
disasm_sys_find_atic:
                ld      a, (ix+0)
                cp      &ff
                jr      z, disasm_sys_fa_miss
                ld      a, (ix+0)
                ld      hl, disasm_sys_op1
                cp      (hl)
                jr      nz, disasm_sys_fa_next
                ld      a, (ix+1)
                ld      hl, disasm_sys_crm
                cp      (hl)
                jr      nz, disasm_sys_fa_next
                ld      a, (ix+2)
                ld      hl, disasm_sys_op2
                cp      (hl)
                jr      nz, disasm_sys_fa_next
; Hit.  NeedsXt = (ix+3); set scratch.  HL = name ptr = IX+5.
                ld      a, (ix+3)
                ld      (disasm_sys_needsxt), a
                push    ix
                pop     hl
                ld      bc, 4
                add     hl, bc                        ; HL → len byte (then name)
                or      &ff                           ; NZ
                ret
disasm_sys_fa_next:
                ld      c, (ix+4)                      ; name_len
                ld      b, 0
                push    ix
                pop     hl
                ld      a, 5
                add     a, c
                ld      c, a
                ld      b, 0
                add     hl, bc                        ; HL → next entry
                push    hl
                pop     ix
                jr      disasm_sys_find_atic
disasm_sys_fa_miss:
                xor     a
                ret


; -----------------------------------------------------------------------
; disasm_sys_set_mnem — copy NUL-terminated string (HL) to DISASM_COMM_MNEM.
; Clobbers A, DE, HL.
; -----------------------------------------------------------------------
disasm_sys_set_mnem:
                ld      de, DISASM_COMM_MNEM
disasm_sys_set_mnem_loop:
                ld      a, (hl)
                ld      (de), a
                or      a
                ret     z
                inc     hl
                inc     de
                jr      disasm_sys_set_mnem_loop

; "msr" mnemonic, used by the immediate form.
disasm_sys_set_mnem_msr:
                ld      hl, disasm_sys_msr_txt
                jr      disasm_sys_set_mnem


; -----------------------------------------------------------------------
; disasm_sys_emit_mnem_noops — mnem = (HL), operands empty.  Used by the
; zero-operand exact-word hints eret/wfi.  Restores nothing (B,C,D,E are
; still intact — these paths never touched them), so it can ret directly
; with the ABI honoured.
; -----------------------------------------------------------------------
disasm_sys_emit_mnem_noops:
                call    disasm_sys_set_mnem
                ld      hl, DISASM_COMM_OPS
                ld      (hl), 0
                ret


; -----------------------------------------------------------------------
; disasm_sys_done — success epilogue.  Reconstructs BC/IX from the word
; bytes saved at disasm_try_sys entry, honouring the "Preserves: BC, IX"
; ABI without stack juggling (the decoders clobber both freely).
; -----------------------------------------------------------------------
disasm_sys_done:
                ld      a, (disasm_sys_wb)
                ld      b, a
                ld      a, (disasm_sys_wc)
                ld      c, a
                ld      a, (disasm_sys_wd)
                ld      h, a
                ld      a, (disasm_sys_we)
                ld      l, a
                push    hl
                pop     ix
                ret


; --- system-group mnemonic / option strings ---------------------------
disasm_sys_eret_txt:    defm    "eret"
                        defb    0
disasm_sys_wfi_txt:     defm    "wfi"
                        defb    0
disasm_sys_dsb_txt:     defm    "dsb"
                        defb    0
disasm_sys_dmb_txt:     defm    "dmb"
                        defb    0
disasm_sys_isb_txt:     defm    "isb"
                        defb    0
disasm_sys_mrs_txt:     defm    "mrs"
                        defb    0
disasm_sys_msr_txt:     defm    "msr"
                        defb    0
disasm_sys_dc_txt:      defm    "dc"
                        defb    0
disasm_sys_at_txt:      defm    "at"
                        defb    0
disasm_sys_ic_txt:      defm    "ic"
                        defb    0
disasm_sys_tlbi_txt:    defm    "tlbi"
                        defb    0
disasm_sys_ssbb_txt:    defm    "ssbb"
                        defb    0
disasm_sys_pssbb_txt:   defm    "pssbb"
                        defb    0

; Barrier-option name strings.
disasm_sys_bo_oshld:    defm    "oshld"
                        defb    0
disasm_sys_bo_oshst:    defm    "oshst"
                        defb    0
disasm_sys_bo_osh:      defm    "osh"
                        defb    0
disasm_sys_bo_nshld:    defm    "nshld"
                        defb    0
disasm_sys_bo_nshst:    defm    "nshst"
                        defb    0
disasm_sys_bo_nsh:      defm    "nsh"
                        defb    0
disasm_sys_bo_ishld:    defm    "ishld"
                        defb    0
disasm_sys_bo_ishst:    defm    "ishst"
                        defb    0
disasm_sys_bo_ish:      defm    "ish"
                        defb    0
disasm_sys_bo_ld:       defm    "ld"
                        defb    0
disasm_sys_bo_st:       defm    "st"
                        defb    0
disasm_sys_bo_sy:       defm    "sy"
                        defb    0

; Barrier-option table indexed by CRm (0..15).  Each entry is a 16-bit
; pointer to a name string, or 0 for "no shared name" (CRm 0/4/8/12 →
; ssbb/pssbb/#imm handled by disasm_sys_emit_baropt).  Mirrors
; barrierOption (sys.go).
disasm_sys_baropt_tbl:
                defw    0                  ; 0  (ssbb for dsb / #imm)
                defw    disasm_sys_bo_oshld ; 1
                defw    disasm_sys_bo_oshst ; 2
                defw    disasm_sys_bo_osh   ; 3
                defw    0                  ; 4  (pssbb for dsb / #imm)
                defw    disasm_sys_bo_nshld ; 5
                defw    disasm_sys_bo_nshst ; 6
                defw    disasm_sys_bo_nsh   ; 7
                defw    0                  ; 8  (#imm)
                defw    disasm_sys_bo_ishld ; 9
                defw    disasm_sys_bo_ishst ; 10
                defw    disasm_sys_bo_ish   ; 11
                defw    0                  ; 12 (#imm)
                defw    disasm_sys_bo_ld    ; 13
                defw    disasm_sys_bo_st    ; 14
                defw    disasm_sys_bo_sy    ; 15

; AT op table — [op1][CRm][op2][NeedsXt=1][len][name...], 0xFF sentinel.
; ARM ARM C5.3.1.  All AT ops take Xt.
disasm_sys_at_tbl:
                defb    0, 8, 0, 1
                defb    5
                defm    "s1e1r"
                defb    0, 8, 1, 1
                defb    5
                defm    "s1e1w"
                defb    0, 8, 2, 1
                defb    5
                defm    "s1e0r"
                defb    0, 8, 3, 1
                defb    5
                defm    "s1e0w"
                defb    4, 8, 0, 1
                defb    5
                defm    "s1e2r"
                defb    4, 8, 1, 1
                defb    5
                defm    "s1e2w"
                defb    4, 8, 4, 1
                defb    6
                defm    "s12e1r"
                defb    4, 8, 5, 1
                defb    6
                defm    "s12e1w"
                defb    4, 8, 6, 1
                defb    6
                defm    "s12e0r"
                defb    4, 8, 7, 1
                defb    6
                defm    "s12e0w"
                defb    6, 8, 0, 1
                defb    5
                defm    "s1e3r"
                defb    6, 8, 1, 1
                defb    5
                defm    "s1e3w"
                defb    &ff

; IC op table — [op1][CRm][op2][NeedsXt][len][name...], 0xFF sentinel.
; ARM ARM C5.3.10.  ialluis/iallu no Xt; ivau Xt.
disasm_sys_ic_tbl:
                defb    0, 1, 0, 0
                defb    7
                defm    "ialluis"
                defb    0, 5, 0, 0
                defb    5
                defm    "iallu"
                defb    3, 5, 1, 1
                defb    4
                defm    "ivau"
                defb    &ff


; --- system-group working scratch (this page) -------------------------
disasm_sys_wb:          defb    0       ; saved word byte bits31:24
disasm_sys_wc:          defb    0       ;                  bits23:16
disasm_sys_wd:          defb    0       ;                  bits15:8
disasm_sys_we:          defb    0       ;                  bits7:0
disasm_sys_op0f:        defb    0       ; op0 field bits[20:19]
disasm_sys_op0:         defb    0       ; full op0 (2|bit19) for mrs/msr
disasm_sys_op1:         defb    0
disasm_sys_crn:         defb    0
disasm_sys_crm:         defb    0
disasm_sys_op2:         defb    0
disasm_sys_rt:          defb    0
disasm_sys_l:           defb    0       ; bit21: mrs(1)/msr(0)
disasm_sys_needsxt:     defb    0
disasm_sys_isdsb:       defb    0
disasm_sys_nameptr:     defw    0
disasm_sys_namebuf:     defs    20      ; generic s<..> spelling (max ~13)

; Encoding→name data for the System group (disasm_sysreg_tbl /
; disasm_pstate_tbl / disasm_dc_tbl / disasm_tlbi_tbl).  Included here so
; the tables land in THIS page alongside the decoder.  See the file's
; header for the no-drift design and the deferred shared-include de-dup
; with src/sysreg_data.asm.
                include "sysreg_names.inc"


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


if defined(BUILD_TESTS)
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
;   &6A — branch b (imm26 PC-relative target) check failed.
;   &69 — branch bl (imm26 PC-relative target) check failed.
;   &68 — ret (bare) check failed.
;   &67 — cbz (imm19 PC-relative target) check failed.
;   &66 — adrp (imm21 page-relative target) check failed.
;   &5E — mrs (named + generic sysreg) check failed.
;   &5D — msr (named sysreg + immediate/pstate) check failed.
;   &5C — dsb / dc check failed.
;   &5B — eret / wfi check failed.
;   &5A — udiv check failed.
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

; --- branch + PC-relative fixtures (write DISASM_COMM_PC first) --------

; b: 14000004 at pc=0x1000 → "b", "0x1010".
                ld      hl, &1000
                call    disasm_stest_set_pc
                ld      bc, &1400
                ld      ix, &0004
                call    disasm_entry
                ld      hl, DISASM_COMM_MNEM
                ld      de, disasm_stest_b_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_b
                ld      hl, DISASM_COMM_OPS
                ld      de, disasm_stest_b_ops_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_b

; bl: 94000004 at pc=0x1000 → "bl", "0x1010".
                ld      hl, &1000
                call    disasm_stest_set_pc
                ld      bc, &9400
                ld      ix, &0004
                call    disasm_entry
                ld      hl, DISASM_COMM_MNEM
                ld      de, disasm_stest_bl_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_bl
                ld      hl, DISASM_COMM_OPS
                ld      de, disasm_stest_bl_ops_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_bl

; ret: D65F03C0 (Rn=30 → bare "ret", no operands).  pc irrelevant; set 0.
                ld      hl, 0
                call    disasm_stest_set_pc
                ld      bc, &D65F
                ld      ix, &03C0
                call    disasm_entry
                ld      hl, DISASM_COMM_MNEM
                ld      de, disasm_stest_ret_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_ret
                ld      a, (DISASM_COMM_OPS)
                or      a                           ; operands must be empty
                jp      nz, disasm_stest_fail_ret

; cbz: 34000040 at pc=0x2000 → "cbz", "w0, 0x2008".
                ld      hl, &2000
                call    disasm_stest_set_pc
                ld      bc, &3400
                ld      ix, &0040
                call    disasm_entry
                ld      hl, DISASM_COMM_MNEM
                ld      de, disasm_stest_cbz_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_cbz
                ld      hl, DISASM_COMM_OPS
                ld      de, disasm_stest_cbz_ops_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_cbz

; adrp: 90000000 at pc=0x1000 → "adrp", "x0, 0x1000".
                ld      hl, &1000
                call    disasm_stest_set_pc
                ld      bc, &9000
                ld      ix, &0000
                call    disasm_entry
                ld      hl, DISASM_COMM_MNEM
                ld      de, disasm_stest_adrp_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_adrp
                ld      hl, DISASM_COMM_OPS
                ld      de, disasm_stest_adrp_ops_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_adrp

; bitfield: lsr immediate (UBFM).  D35EFC62 → "lsr", "x2, x3, #30".
                ld      bc, &D35E
                ld      ix, &FC62
                call    disasm_entry
                ld      hl, DISASM_COMM_MNEM
                ld      de, disasm_stest_bflsr_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_bflsr
                ld      hl, DISASM_COMM_OPS
                ld      de, disasm_stest_bflsr_ops_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_bflsr

; bitfield: bfi (BFM).  B35C1C62 → "bfi", "x2, x3, #36, #8".
                ld      bc, &B35C
                ld      ix, &1C62
                call    disasm_entry
                ld      hl, DISASM_COMM_MNEM
                ld      de, disasm_stest_bfi_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_bfi
                ld      hl, DISASM_COMM_OPS
                ld      de, disasm_stest_bfi_ops_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_bfi

; condsel: cset alias (CSINC zr,zr).  1A9F07E5 → "cset", "w5, ne".
                ld      bc, &1A9F
                ld      ix, &07E5
                call    disasm_entry
                ld      hl, DISASM_COMM_MNEM
                ld      de, disasm_stest_cset_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_cset
                ld      hl, DISASM_COMM_OPS
                ld      de, disasm_stest_cset_ops_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_cset

; condsel: csel base.  1A851020 → "csel", "w0, w1, w5, ne".
                ld      bc, &1A85
                ld      ix, &1020
                call    disasm_entry
                ld      hl, DISASM_COMM_MNEM
                ld      de, disasm_stest_csel_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_csel
                ld      hl, DISASM_COMM_OPS
                ld      de, disasm_stest_csel_ops_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_csel

; multiply: mul alias (MADD Ra=zr).  9B007C20 → "mul", "x0, x1, x0".
                ld      bc, &9B00
                ld      ix, &7C20
                call    disasm_entry
                ld      hl, DISASM_COMM_MNEM
                ld      de, disasm_stest_mul_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_mul
                ld      hl, DISASM_COMM_OPS
                ld      de, disasm_stest_mul_ops_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_mul

; multiply: madd base.  9B041020 → "madd", "x0, x1, x4, x4".
                ld      bc, &9B04
                ld      ix, &1020
                call    disasm_entry
                ld      hl, DISASM_COMM_MNEM
                ld      de, disasm_stest_madd_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_madd
                ld      hl, DISASM_COMM_OPS
                ld      de, disasm_stest_madd_ops_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_madd

; shift-variable: lsrv → lsr register.  1ACE258C → "lsr", "w12, w12, w14".
                ld      bc, &1ACE
                ld      ix, &258C
                call    disasm_entry
                ld      hl, DISASM_COMM_MNEM
                ld      de, disasm_stest_svlsr_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_svlsr
                ld      hl, DISASM_COMM_OPS
                ld      de, disasm_stest_svlsr_ops_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_svlsr

; --- system group + dp-2-source div fixtures --------------------------

; mrs (named sysreg).  D5384240 → "mrs", "x0, currentel".
                ld      bc, &D538
                ld      ix, &4240
                call    disasm_entry
                ld      hl, DISASM_COMM_MNEM
                ld      de, disasm_stest_mrs_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_mrs
                ld      hl, DISASM_COMM_OPS
                ld      de, disasm_stest_mrs_ops_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_mrs

; mrs (generic sysreg spelling).  D539B040 → "mrs", "x0, s3_1_c11_c0_2".
                ld      bc, &D539
                ld      ix, &B040
                call    disasm_entry
                ld      hl, DISASM_COMM_OPS
                ld      de, disasm_stest_mrsgen_ops_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_mrs

; msr (named sysreg).  D5181000 → "msr", "sctlr_el1, x0".
                ld      bc, &D518
                ld      ix, &1000
                call    disasm_entry
                ld      hl, DISASM_COMM_MNEM
                ld      de, disasm_stest_msr_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_msr
                ld      hl, DISASM_COMM_OPS
                ld      de, disasm_stest_msr_ops_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_msr

; msr (immediate / pstate).  D50343DF → "msr", "daifset, #0x3".
                ld      bc, &D503
                ld      ix, &43DF
                call    disasm_entry
                ld      hl, DISASM_COMM_OPS
                ld      de, disasm_stest_msrimm_ops_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_msr

; dsb sy.  D5033F9F → "dsb", "sy".
                ld      bc, &D503
                ld      ix, &3F9F
                call    disasm_entry
                ld      hl, DISASM_COMM_MNEM
                ld      de, disasm_stest_dsb_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_dsb
                ld      hl, DISASM_COMM_OPS
                ld      de, disasm_stest_dsb_ops_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_dsb

; dc civac, x10.  D50B7E2A → "dc", "civac, x10".
                ld      bc, &D50B
                ld      ix, &7E2A
                call    disasm_entry
                ld      hl, DISASM_COMM_MNEM
                ld      de, disasm_stest_dc_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_dsb
                ld      hl, DISASM_COMM_OPS
                ld      de, disasm_stest_dc_ops_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_dsb

; eret.  D69F03E0 → "eret", "".
                ld      bc, &D69F
                ld      ix, &03E0
                call    disasm_entry
                ld      hl, DISASM_COMM_MNEM
                ld      de, disasm_stest_eret_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_eret
                ld      a, (DISASM_COMM_OPS)
                or      a
                jp      nz, disasm_stest_fail_eret

; wfi.  D503207F → "wfi", "".
                ld      bc, &D503
                ld      ix, &207F
                call    disasm_entry
                ld      hl, DISASM_COMM_MNEM
                ld      de, disasm_stest_wfi_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_eret
                ld      a, (DISASM_COMM_OPS)
                or      a
                jp      nz, disasm_stest_fail_eret

; udiv.  1AC30842 → "udiv", "w2, w2, w3".
                ld      bc, &1AC3
                ld      ix, &0842
                call    disasm_entry
                ld      hl, DISASM_COMM_MNEM
                ld      de, disasm_stest_udiv_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_udiv
                ld      hl, DISASM_COMM_OPS
                ld      de, disasm_stest_udiv_ops_expect
                call    disasm_stest_strcmp
                jp      nz, disasm_stest_fail_udiv

                ld      bc, 0
                ret


; disasm_stest_set_pc — write HL (a 16-bit PC) to DISASM_COMM_PC as an
; 8-byte little-endian value (HL low 2 bytes, then 6 zero bytes).
; Clobbers A, DE, HL.
disasm_stest_set_pc:
                ld      de, DISASM_COMM_PC
                ld      a, l
                ld      (de), a
                inc     de
                ld      a, h
                ld      (de), a
                inc     de
                xor     a
                ld      b, 6
disasm_stest_setpc_loop:
                ld      (de), a
                inc     de
                djnz    disasm_stest_setpc_loop
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
disasm_stest_fail_b:
                ld      bc, &6A
                ret
disasm_stest_fail_bl:
                ld      bc, &69
                ret
disasm_stest_fail_ret:
                ld      bc, &68
                ret
disasm_stest_fail_cbz:
                ld      bc, &67
                ret
disasm_stest_fail_adrp:
                ld      bc, &66
                ret
disasm_stest_fail_bflsr:
                ld      bc, &65
                ret
disasm_stest_fail_bfi:
                ld      bc, &64
                ret
disasm_stest_fail_cset:
                ld      bc, &63
                ret
disasm_stest_fail_csel:
                ld      bc, &62
                ret
disasm_stest_fail_mul:
                ld      bc, &61
                ret
disasm_stest_fail_madd:
                ld      bc, &60
                ret
disasm_stest_fail_svlsr:
                ld      bc, &5F
                ret
disasm_stest_fail_mrs:
                ld      bc, &5E
                ret
disasm_stest_fail_msr:
                ld      bc, &5D
                ret
disasm_stest_fail_dsb:
                ld      bc, &5C
                ret
disasm_stest_fail_eret:
                ld      bc, &5B
                ret
disasm_stest_fail_udiv:
                ld      bc, &5A
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
disasm_stest_b_expect:          defm    "b"
                                defb    0
disasm_stest_b_ops_expect:      defm    "0x1010"
                                defb    0
disasm_stest_bl_expect:         defm    "bl"
                                defb    0
disasm_stest_bl_ops_expect:     defm    "0x1010"
                                defb    0
disasm_stest_ret_expect:        defm    "ret"
                                defb    0
disasm_stest_cbz_expect:        defm    "cbz"
                                defb    0
disasm_stest_cbz_ops_expect:    defm    "w0, 0x2008"
                                defb    0
disasm_stest_adrp_expect:       defm    "adrp"
                                defb    0
disasm_stest_adrp_ops_expect:   defm    "x0, 0x1000"
                                defb    0
disasm_stest_bflsr_expect:      defm    "lsr"
                                defb    0
disasm_stest_bflsr_ops_expect:  defm    "x2, x3, #30"
                                defb    0
disasm_stest_bfi_expect:        defm    "bfi"
                                defb    0
disasm_stest_bfi_ops_expect:    defm    "x2, x3, #36, #8"
                                defb    0
disasm_stest_cset_expect:       defm    "cset"
                                defb    0
disasm_stest_cset_ops_expect:   defm    "w5, ne"
                                defb    0
disasm_stest_csel_expect:       defm    "csel"
                                defb    0
disasm_stest_csel_ops_expect:   defm    "w0, w1, w5, ne"
                                defb    0
disasm_stest_mul_expect:        defm    "mul"
                                defb    0
disasm_stest_mul_ops_expect:    defm    "x0, x1, x0"
                                defb    0
disasm_stest_madd_expect:       defm    "madd"
                                defb    0
disasm_stest_madd_ops_expect:   defm    "x0, x1, x4, x4"
                                defb    0
disasm_stest_svlsr_expect:      defm    "lsr"
                                defb    0
disasm_stest_svlsr_ops_expect:  defm    "w12, w12, w14"
                                defb    0
disasm_stest_mrs_expect:        defm    "mrs"
                                defb    0
disasm_stest_mrs_ops_expect:    defm    "x0, currentel"
                                defb    0
disasm_stest_mrsgen_ops_expect: defm    "x0, s3_1_c11_c0_2"
                                defb    0
disasm_stest_msr_expect:        defm    "msr"
                                defb    0
disasm_stest_msr_ops_expect:    defm    "sctlr_el1, x0"
                                defb    0
disasm_stest_msrimm_ops_expect: defm    "daifset, #0x3"
                                defb    0
disasm_stest_dsb_expect:        defm    "dsb"
                                defb    0
disasm_stest_dsb_ops_expect:    defm    "sy"
                                defb    0
disasm_stest_dc_expect:         defm    "dc"
                                defb    0
disasm_stest_dc_ops_expect:     defm    "civac, x10"
                                defb    0
disasm_stest_eret_expect:       defm    "eret"
                                defb    0
disasm_stest_wfi_expect:        defm    "wfi"
                                defb    0
disasm_stest_udiv_expect:       defm    "udiv"
                                defb    0
disasm_stest_udiv_ops_expect:   defm    "w2, w2, w3"
                                defb    0

endif ; defined(BUILD_TESTS)
