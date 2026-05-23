; test_slots.asm — boot-time self-tests for the per-slot encoders.
;
; These run BEFORE load_enctab (see assembler.asm start:) so they exercise
; the encoder against inline literal slot records and never touch the disk.
; Any assertion failure does `jp fail` — the same red-border-halt path used
; by load_enctab.  On all-pass the entry point RETs to the caller.
;
; Test vectors mirror the SUCCESS cases in
; tools/aarch64enc/slots_trivial_test.go::TestEncodeXreg and
; ::TestEncodeXregOrSpAcceptsThirtyOne.  The error case (reg=32) is
; intentionally skipped: there is no way to test the `jp fail` path
; without halting the program.  (See plan §"Self-test framework".)


; -----------------------------------------------------------------------
; run_slot_self_tests — entry point called from assembler.asm start:.
;
; Input:  none
; Output: returns to caller on success.  On any mismatch: jp fail.
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
run_slot_self_tests:

; -- encode_reg(Xreg{BP=0,BW=5}, 5)  =>  0x00000005 --------------------
                ld      hl, slot_xreg_bp0_bw5
                ld      a, 5
                call    encode_reg
                call    assert_eq32_de_hl_imm
                defb    5, 0, 0, 0     ; little-endian 0x00000005

; -- encode_reg(Xreg{BP=0,BW=5}, 30) =>  0x0000001e --------------------
                ld      hl, slot_xreg_bp0_bw5
                ld      a, 30
                call    encode_reg
                call    assert_eq32_de_hl_imm
                defb    30, 0, 0, 0

; -- encode_reg(Xreg{BP=0,BW=5}, 31) =>  0x0000001f --------------------
                ld      hl, slot_xreg_bp0_bw5
                ld      a, 31
                call    encode_reg
                call    assert_eq32_de_hl_imm
                defb    31, 0, 0, 0

; -- encode_reg(Xreg{BP=5,BW=5}, 5)  =>  0x000000a0 (5 << 5 = 160) -----
                ld      hl, slot_xreg_bp5_bw5
                ld      a, 5
                call    encode_reg
                call    assert_eq32_de_hl_imm
                defb    160, 0, 0, 0

; -- encode_reg(XregOrSp{BP=0,BW=5}, 31) =>  0x0000001f ----------------
; Confirms the encoder treats XregOrSp the same as Xreg at encode time
; (slots_trivial.go lines 6-8 / slots_trivial_test.go:34-40).
                ld      hl, slot_xregorsp_bp0_bw5
                ld      a, 31
                call    encode_reg
                call    assert_eq32_de_hl_imm
                defb    31, 0, 0, 0

; All assertions passed.
                ret


; -----------------------------------------------------------------------
; assert_eq32_de_hl_imm — assert DEHL equals a 32-bit literal that
; immediately follows the call in the caller's code stream.
;
; Caller pattern:
;     call assert_eq32_de_hl_imm
;     defb b0, b1, b2, b3        ; little-endian: b0 = bit 0..7
;
; The return address on the stack points at the first defb byte.  This
; routine compares DEHL against the four bytes (HL low first, then DE
; high), advances the return address past them on match, and RETs.
; On mismatch: jp fail.
;
; Layout reminder: DEHL where HL is bits 0..15 and DE is bits 16..31,
; so the in-memory little-endian byte order is L, H, E, D.
;
; Rationale: inline-literal style reads cleanly at the call site and is
; consistent with the pyz80 convention used for SAMDOS hooks (`rst 8`
; followed by `defb <hook>`).  See src/sam_io.inc lines 87-101.
;
; Clobbers: A, BC.  Preserves DE and HL so the caller's "actual" result
; remains intact (useful when chaining diagnostics).
; -----------------------------------------------------------------------
assert_eq32_de_hl_imm:
                pop     bc             ; BC = pointer to inline literal

                ld      a, (bc)        ; byte 0: low byte of HL
                cp      l
                jp      nz, fail
                inc     bc

                ld      a, (bc)        ; byte 1: high byte of HL
                cp      h
                jp      nz, fail
                inc     bc

                ld      a, (bc)        ; byte 2: low byte of DE
                cp      e
                jp      nz, fail
                inc     bc

                ld      a, (bc)        ; byte 3: high byte of DE
                cp      d
                jp      nz, fail
                inc     bc             ; BC now points just past the literal

                push    bc             ; restore as return address
                ret


; -----------------------------------------------------------------------
; Static slot-record literals for the self-tests.
;
; Layout (per docs/specs/2026-05-24-m2-encoder-tables-design.md §2):
;   defb slot_kind, expected_kind, bit_position, bit_width
;
; slot_kind values (per tools/aarch64enc/types.go lines 16-19):
;   Xreg     = 0x01
;   Wreg     = 0x02
;   XregOrSp = 0x03
;   WregOrSp = 0x04
;
; expected_kind is set to 0 here: encode_reg does not consult it
; (text2bin uses it earlier in the pipeline).
; -----------------------------------------------------------------------
slot_xreg_bp0_bw5:      defb    &01, 0, 0, 5
slot_xreg_bp5_bw5:      defb    &01, 0, 5, 5
slot_xregorsp_bp0_bw5:  defb    &03, 0, 0, 5
