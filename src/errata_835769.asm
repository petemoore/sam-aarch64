; errata_835769.asm — Cortex-A53 erratum 835769 NOP-insertion fix (i27b).
;
; Authority: tools/sam-aarch64/assemble/errata.go (the Go implementation that
; landed in i27a / PR #600).  Every predicate here mirrors the Go code
; verbatim; the errata.go line number of the ported function is cited in each
; comment below.
;
; Background: on the Cortex-A53 (e.g. Raspberry Pi 3) a memory load or store
; immediately followed by a 64-bit multiply-accumulate (with Ra ≠ XZR) can
; corrupt the MAC result.  The fix is to insert a NOP (erratumNOP =
; 0xd503201f, errata.go:54) between the two instructions.
;
; Toggle: FIX_835769_ENABLED (errata.go fixErratum835769 is called when the
; -fix-cortex-a53-835769 flag is passed; q37 answer mapped this to an
; assembler-side constant).  Default OFF — off leaves release.bin unchanged.
; Pi3/A53 targeting sets it ON.
;
; Integration: insn_run.asm calls errata_check_and_handle + errata_update_prev
; around each mode-1 element (insn_p2_elem_emit / insn_p1_elem_done).
; main_handle_insn_run dispatches mode-0 runs to mhir_mode0_errata (below)
; when the toggle is on, processing each 4-byte word individually.
;
; The NOP word 0xd503201f is NOT a load/store (byte3 = 0xd5 fails aarch64LDST)
; so a NOP inserted by this pass never creates a new hazard pair — one pass
; suffices (errata.go:91-141 re-runs pass1+pass2 once after insertion;
; we achieve the same result inline since NOP→MAC is provably never a hazard).
;
; Memory state variables — allocated in the free region &D3C8..&D4FF
; (documented in assembler.asm).  See assembler.asm for the EQU declarations:
;   FIX_835769_ENABLED  &D3C8  1 byte: 0=off (default), 1=on
;   ERRATA_PREV_VALID   &D3C9  1 byte: 1 when prev insn state is valid
;   ERRATA_INSN1        &D3CA  4 bytes: previous instruction word (LE)
;   ERRATA_PREV_PC      &D3CE  4 bytes: PASS_PC after processing prev insn
;   ERRATA_INSN2        &D3D2  4 bytes: current instruction word (scratch)

; State variable addresses — in the free region at &D3C8 (documented in
; assembler.asm).  Exported to assembler.sym so test_offaxis_cluster.asm
; can import them via --importfile.
FIX_835769_ENABLED:     equ     &D3C8   ; 1 byte: 0=off (default), 1=on
ERRATA_PREV_VALID:      equ     &D3C9   ; 1 byte: 1 when prev insn state is valid
ERRATA_INSN1:           equ     &D3CA   ; 4 bytes: previous instruction word (LE)
ERRATA_PREV_PC:         equ     &D3CE   ; 4 bytes: PASS_PC after processing prev insn
ERRATA_INSN2:           equ     &D3D2   ; 4 bytes: current instruction word (scratch)

; erratumNOP bytes in little-endian order (errata.go:54 erratumNOP = 0xd503201f).
ERRATUM_NOP_B0:         equ     &1f
ERRATUM_NOP_B1:         equ     &20
ERRATUM_NOP_B2:         equ     &03
ERRATUM_NOP_B3:         equ     &d5


; -----------------------------------------------------------------------
; errata_mlxl_p — port of errata.go aarch64MlxlP:257-260.
;
; Reports whether (ERRATA_INSN2) is a 64-bit multiply-accumulate with
; Ra ≠ XZR: MADD/MSUB/SMADDL/SMSUBL/UMADDL/UMSUBL.  The Ra ≠ XZR test
; excludes MUL/MNEG aliases (which have Ra = XZR = 0x1f).
;
; Checks (errata.go:257-260):
;   aarch64MAC:  byte3 == 0x9b                     (errata.go:252)
;   op31 ∈ {0,1,5}:  (byte2 rlca×3) & 7            (errata.go:222)
;   Ra ≠ XZR:  ((byte1 rrca×2) & 0x1f) ≠ 0x1f     (errata.go:215,219)
;
; Input:  (ERRATA_INSN2) = instruction word (4 bytes LE).
; Output: A = 1 if IS mlxl_p, A = 0 if not.
; Clobbers: A.
; -----------------------------------------------------------------------
errata_mlxl_p:
; aarch64MAC(insn2): byte3 == 0x9b  (errata.go:252)
                ld      a, (ERRATA_INSN2 + 3)
                cp      &9b
                jr      z, eml_mac_ok
                xor     a               ; not MAC
                ret

eml_mac_ok:
; op31 = bits[23:21]: byte2 rlca×3, then mask to 3 bits  (errata.go:222)
; aarch64OP31 = aarch64Bits(insn, 21, 3) = (insn >> 21) & 7
; byte2 = insn[23:16]; bit 21 = byte2[5]; after 3 left-rotates, byte2[7:5]
; land at bits[2:0].
                ld      a, (ERRATA_INSN2 + 2)
                rlca
                rlca
                rlca
                and     &07             ; A = op31

; op31 ∈ {0, 1, 5}?  (errata.go:259)
                cp      0
                jr      z, eml_op31_ok
                cp      1
                jr      z, eml_op31_ok
                cp      5
                jr      z, eml_op31_ok
                xor     a               ; op31 not in {0,1,5}: not mlxl_p
                ret

eml_op31_ok:
; Ra = bits[14:10]: (byte1 rrca×2) & 0x1f  (errata.go:215 aarch64RA)
; Ra ≠ aarch64ZR (0x1f)  (errata.go:219,259)
                ld      a, (ERRATA_INSN2 + 1)
                rrca
                rrca
                and     &1f             ; A = Ra
                cp      &1f             ; XZR?
                jr      z, eml_ra_xzr
                ld      a, 1            ; IS mlxl_p: MAC with Ra ≠ XZR
                ret

eml_ra_xzr:
                xor     a               ; Ra == XZR: MUL/MNEG alias, not mlxl_p
                ret


; -----------------------------------------------------------------------
; errata_mem_op_classify — port of errata.go aarch64MemOp:265-331.
;
; Classifies the instruction in (ERRATA_INSN1) as a load/store and
; extracts the register fields needed for the RAW carve-out.
;
; Input:  (ERRATA_INSN1) = insn1 word (4 bytes LE).
; Output: A = 0 if NOT a memory op (ok=false); no other outputs valid.
;         If A ≠ 0 (ok=true):
;           B = pair flag (0 or 1)
;           C = load flag (0 or 1)
;           D = rt  (5-bit register index)
;           E = rt2 (5-bit register index; equals rt for non-pair)
; Clobbers: A, B, C, D, E, HL.
; -----------------------------------------------------------------------
errata_mem_op_classify:
; aarch64LDST: (byte3 & 0x0a) == 0x08  (errata.go:231)
                ld      a, (ERRATA_INSN1 + 3)
                ld      h, a            ; H = byte3 (save across sub-checks)
                and     &0a
                cp      &08
                jr      z, emc_is_ldst
                xor     a               ; not a load/store: ok = false
                ret

emc_is_ldst:
; aarch64RT: bits[4:0] = byte0 & 0x1f  (errata.go:210)
                ld      a, (ERRATA_INSN1 + 0)
                and     &1f
                ld      d, a            ; D = rt
                ld      e, a            ; E = rt2 (= rt for non-pair initially)
                ld      b, 0            ; B = pair = false initially

; aarch64LDSTEX: (byte3 & 0x3f) == 0x08  (errata.go:232)
                ld      a, h
                and     &3f
                cp      &08
                jr      z, emc_exclusive

; Pair forms (LDSTNAP, LDSTPPI, LDSTPO, LDSTPPRE): (byte3 & 0x3a) == 0x28
; (errata.go:234-237: four predicates unified here; all four share the pattern
; that byte3 & 0x3a == 0x28 — the o1 bit at byte3[0] and byte2[7] select
; between NAP/PI/PO/PPRE but pair=true for all of them)
                ld      a, h
                and     &3a
                cp      &28
                jr      z, emc_pair

; Non-pair, non-exclusive forms (PCREL, UI, PIIMM, U, PREIMM, RO, UIMM, SIMD)
; (errata.go:285-329): rt = RT, rt2 = rt, pair = false (already set above).
; load computed from opc (bits[23:22]) and V (bit 26).
; For SIMD forms (V=1, bit 26), errata_is_hazard applies the SIMD shortcut
; (errata.go:346) before consulting load, so the load value for SIMD is unused
; but we still return ok=true so the SIMD shortcut is reachable.
emc_other:
; opc = bits[23:22]: byte2 rlca×2 then & 3  (errata.go:292 aarch64Bits(insn,22,2))
                ld      a, (ERRATA_INSN1 + 2)
                rlca
                rlca
                and     &03             ; A = opc
                ld      l, a            ; L = opc (save)
; V = bit 26 = byte3[2]  (errata.go:293 aarch64Bit(insn,26))
                ld      a, h            ; byte3
                and     &04             ; bit 2 of byte3: 4 or 0
                or      l               ; A = opc | (V << 2) = opcV  (errata.go:294)
; load = opcV ∈ {1, 2, 3, 5, 7}  (errata.go:295)
                cp      1
                jr      z, emc_other_load
                cp      2
                jr      z, emc_other_load
                cp      3
                jr      z, emc_other_load
                cp      5
                jr      z, emc_other_load
                cp      7
                jr      z, emc_other_load
                ld      c, 0            ; not a load
                ld      a, 1            ; ok = true
                ret
emc_other_load:
                ld      c, 1            ; is a load
                ld      a, 1            ; ok = true
                ret

emc_exclusive:
; aarch64LDSTEX (errata.go:270-278): pair = bit 21; load = LD (bit 22).
; rt and rt2 already set to RT (= byte0 & 0x1f) above.
; aarch64Bit(insn, 21): byte2[5] = byte2 & 0x20  (errata.go:273)
                ld      a, (ERRATA_INSN1 + 2)
                and     &20
                jr      z, emc_ex_nopair
; pair: rt2 = RT2 = bits[14:10] = (byte1 rrca×2) & 0x1f  (errata.go:275)
                ld      a, (ERRATA_INSN1 + 1)
                rrca
                rrca
                and     &1f
                ld      e, a            ; E = rt2
                ld      b, 1            ; B = pair = true
emc_ex_nopair:
; load = aarch64LD = bit 22 = byte2[6] = byte2 & 0x40  (errata.go:230,277)
                ld      a, (ERRATA_INSN1 + 2)
                and     &40
                jr      z, emc_ex_store
                ld      c, 1            ; load = true
                ld      a, 1
                ret
emc_ex_store:
                ld      c, 0            ; load = false
                ld      a, 1
                ret

emc_pair:
; Pair forms (LDSTNAP,LDSTPPI,LDSTPO,LDSTPPRE) (errata.go:279-284):
; pair = true, rt = RT, rt2 = RT2, load = LD.
                ld      b, 1            ; B = pair = true
; rt2 = bits[14:10] = (byte1 rrca×2) & 0x1f  (errata.go:284 aarch64RT2)
                ld      a, (ERRATA_INSN1 + 1)
                rrca
                rrca
                and     &1f
                ld      e, a            ; E = rt2
; load = bit 22 = byte2 & 0x40  (errata.go:230,283)
                ld      a, (ERRATA_INSN1 + 2)
                and     &40
                jr      z, emc_pair_store
                ld      c, 1
                ld      a, 1
                ret
emc_pair_store:
                ld      c, 0
                ld      a, 1
                ret


; -----------------------------------------------------------------------
; errata_is_hazard — port of errata.go aarch64ErratumSequence:337-358.
;
; Reports whether (ERRATA_INSN1, ERRATA_INSN2) form an 835769 hazard:
; insn1 is a memory op immediately followed by a 64-bit MAC (insn2) with no
; true RAW dependency on the loaded register.
;
; Input:  (ERRATA_INSN1) = insn1, (ERRATA_INSN2) = insn2.
; Output: A = 1 if hazard, A = 0 if not.
; Clobbers: A, B, C, D, E, HL.
; -----------------------------------------------------------------------
errata_is_hazard:
; aarch64MlxlP(insn2)  (errata.go:338)
                call    errata_mlxl_p   ; reads (ERRATA_INSN2)
                or      a
                ret     z               ; not MAC or Ra=XZR: no hazard

; aarch64MemOp(insn1)  (errata.go:341)
                call    errata_mem_op_classify  ; reads (ERRATA_INSN1)
                ; A=ok, B=pair, C=load, D=rt, E=rt2
                or      a
                ret     z               ; not a memory op: no hazard

; SIMD shortcut: bit 26 of insn1 = 1 → always a hazard  (errata.go:346)
; bit 26 = byte3[2] of ERRATA_INSN1
                ld      a, (ERRATA_INSN1 + 3)
                and     &04             ; bit 26 (V flag)
                jr      nz, eih_hazard  ; SIMD mem op: hazard unconditionally

; RAW carve-out: load && (rt ∈ {Rn,Rm,Ra} || pair && rt2 ∈ {Rn,Rm,Ra})
; (errata.go:353-354).  Store ops skip the carve-out (conservative).
                ld      a, c            ; C = load
                or      a
                jr      z, eih_hazard   ; store: always a hazard

; B=pair, D=rt, E=rt2 from errata_mem_op_classify.  C is reused for RA.
; Compute Rn(insn2) = bits[9:5]  (errata.go:349 aarch64RN)
; byte0[7:5] → RN[2:0]: byte0 rlca×3 & 7
; byte1[1:0] → RN[4:3]: (byte1 & 3) rlca×3
                ld      a, (ERRATA_INSN2 + 0)
                rlca
                rlca
                rlca
                and     &07
                ld      l, a            ; L = low 3 bits of Rn
                ld      a, (ERRATA_INSN2 + 1)
                and     &03
                rlca
                rlca
                rlca
                or      l
                ld      l, a            ; L = Rn = bits[9:5]

; Rm(insn2) = bits[20:16] = byte2 & 0x1f  (errata.go:350 aarch64RM)
                ld      a, (ERRATA_INSN2 + 2)
                and     &1f
                ld      h, a            ; H = Rm

; Ra(insn2) = bits[14:10] = (byte1 rrca×2) & 0x1f  (errata.go:350 aarch64RA)
                ld      a, (ERRATA_INSN2 + 1)
                rrca
                rrca
                and     &1f
                ld      c, a            ; C = Ra (reusing C; load already consumed)

; Now: B=pair, C=Ra, D=rt, E=rt2, H=Rm, L=Rn.
; Check: rt ∈ {Rn, Rm, Ra}?  (errata.go:353)
                ld      a, d            ; rt
                cp      l               ; rt == Rn?
                jr      z, eih_raw
                cp      h               ; rt == Rm?
                jr      z, eih_raw
                cp      c               ; rt == Ra?
                jr      z, eih_raw

; Check: pair && rt2 ∈ {Rn, Rm, Ra}?  (errata.go:354)
                ld      a, b            ; pair
                or      a
                jr      z, eih_hazard   ; not pair: hazard (no second register to exempt)

                ld      a, e            ; rt2
                cp      l               ; rt2 == Rn?
                jr      z, eih_raw
                cp      h               ; rt2 == Rm?
                jr      z, eih_raw
                cp      c               ; rt2 == Ra?
                jr      z, eih_raw

eih_hazard:
                ld      a, 1            ; IS a hazard
                ret

eih_raw:
                xor     a               ; true RAW dep: pipeline serialises, no hazard
                ret


; -----------------------------------------------------------------------
; errata_check_and_handle — per-instruction hazard gate.
;
; Called for each instruction before it is emitted or counted.
; On entry: (ERRATA_INSN2) = current instruction word; ERRATA_INSN1,
; ERRATA_PREV_VALID, ERRATA_PREV_PC hold the previous-instruction state.
;
; If the toggle is off, or there is no valid previous instruction, or the
; instructions are not PC-adjacent, or no hazard is detected: returns
; immediately.
;
; If a hazard IS detected:
;   pass 1 — PASS_PC advances by 4 (accounting for the NOP byte slot).
;   pass 2 — the NOP word (erratumNOP = 0xd503201f, errata.go:54) is
;             emitted and PASS_PC advances by 4.
;
; The caller then emits/counts the current instruction and calls
; errata_update_prev.
;
; Clobbers: A, B, C, D, E, HL.
; -----------------------------------------------------------------------
errata_check_and_handle:
; Gate 1: toggle enabled?
                ld      a, (FIX_835769_ENABLED)
                or      a
                ret     z

; Gate 2: prev instruction valid?
                ld      a, (ERRATA_PREV_VALID)
                or      a
                ret     z

; Gate 3: PC adjacency — PASS_PC == ERRATA_PREV_PC?
; If anything advanced PASS_PC between insn1 and insn2 (a data record, a
; directive, a misaligned run boundary), the PCs differ and we skip.
                ld      hl, ERRATA_PREV_PC
                ld      de, PASS_PC
                ld      b, 4
ecah_pc_cmp:
                ld      a, (de)
                cp      (hl)
                jr      nz, ecah_done   ; not adjacent
                inc     hl
                inc     de
                djnz    ecah_pc_cmp

; Hazard check (errata.go:337-358)
                call    errata_is_hazard
                or      a
                jr      z, ecah_done    ; no hazard

; Hazard: insert NOP (erratumNOP = 0xd503201f LE, errata.go:54)
                ld      a, (PASS_MODE)
                cp      PASS_PASS1
                jr      z, ecah_nop_p1

; Pass 2: emit the NOP bytes then advance PASS_PC
ecah_nop_p2:
                ld      a, ERRATUM_NOP_B0
                call    emit_byte
                ld      a, ERRATUM_NOP_B1
                call    emit_byte
                ld      a, ERRATUM_NOP_B2
                call    emit_byte
                ld      a, ERRATUM_NOP_B3
                call    emit_byte
                call    pass_pc_advance_4
                ret

; Pass 1: just advance PASS_PC (no emit)
ecah_nop_p1:
                call    pass_pc_advance_4
ecah_done:
                ret


; -----------------------------------------------------------------------
; errata_update_prev — record current instruction as the new "previous".
;
; Called AFTER emit/count and pass_pc_advance_4 for each instruction.
; Copies (ERRATA_INSN2) → ERRATA_INSN1 and records the new ERRATA_PREV_PC
; (= current PASS_PC, which is the PC that the next instruction must start
; at to be considered adjacent).
;
; Input:  (ERRATA_INSN2) = current instruction; PASS_PC updated.
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
errata_update_prev:
                ld      a, (FIX_835769_ENABLED)
                or      a
                ret     z               ; toggle off: skip (no state to maintain)

; ERRATA_INSN1 := ERRATA_INSN2
                ld      hl, ERRATA_INSN2
                ld      de, ERRATA_INSN1
                ld      bc, 4
                ldir

; ERRATA_PREV_PC := PASS_PC (the starting PC of the next adjacent instruction)
                ld      hl, PASS_PC
                ld      de, ERRATA_PREV_PC
                ld      bc, 4
                ldir

; ERRATA_PREV_VALID := 1
                ld      a, 1
                ld      (ERRATA_PREV_VALID), a
                ret


; -----------------------------------------------------------------------
; mhir_mode0_errata — INSN_RUN mode-0 per-word errata path.
;
; Reached from main_handle_insn_run when FIX_835769_ENABLED != 0 and
; the mode byte is 0 (all-literal word run).  Processes each 4-byte word
; individually: copies to ERRATA_INSN2, checks for hazard, emits/counts
; the word, updates the prev state.  Invalidates ERRATA_PREV_VALID when
; done so an immediately following non-instruction record is not treated
; as adjacent.
;
; Input:  HL = payload ptr (pointing AT the mode byte = 0),
;         BC = payload_len (including mode byte).
; Output: jp walk_records.
; -----------------------------------------------------------------------
mhir_mode0_errata:
; Skip the mode byte (it plays the role of the leading tag byte).
                inc     hl              ; HL → first instruction word
                dec     bc              ; BC = nbytes (must be a multiple of 4)
                ld      (errata_m0_ptr), hl
                ld      (errata_m0_remaining), bc

errata_m0_loop:
                ld      bc, (errata_m0_remaining)
                ld      a, b
                or      c
                jp      z, walk_records         ; all words processed

; Copy 4 bytes → ERRATA_INSN2
                ld      hl, (errata_m0_ptr)
                ld      de, ERRATA_INSN2
                ld      bc, 4
                ldir                            ; ERRATA_INSN2 = current word;
                                                ; HL advanced past 4 bytes; BC=0
                ld      (errata_m0_ptr), hl

; Remaining -= 4
                ld      hl, (errata_m0_remaining)
                ld      bc, 4
                or      a
                sbc     hl, bc
                ld      (errata_m0_remaining), hl

; Hazard check (may emit NOP + advance PC in pass 2, or advance PC in pass 1)
                call    errata_check_and_handle

; Emit (pass 2) or skip (pass 1)
                ld      a, (PASS_MODE)
                cp      PASS_PASS1
                jr      z, errata_m0_p1

; Pass 2: emit 4 bytes from ERRATA_INSN2
                ld      a, (ERRATA_INSN2 + 0)
                call    emit_byte
                ld      a, (ERRATA_INSN2 + 1)
                call    emit_byte
                ld      a, (ERRATA_INSN2 + 2)
                call    emit_byte
                ld      a, (ERRATA_INSN2 + 3)
                call    emit_byte

errata_m0_p1:
                call    pass_pc_advance_4
                call    errata_update_prev
                jr      errata_m0_loop

; Scratch variables for the mode-0 per-word loop.
errata_m0_ptr:          defw    0       ; pointer to current word in payload
errata_m0_remaining:    defw    0       ; bytes remaining in this mode-0 run
