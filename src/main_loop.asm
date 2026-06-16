; main_loop.asm — top-level M3/M4 driver.
;
; M4 two-pass design (see https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/specs/2026-05-24-m4-symbols-multipass-design.md
; §2.1):
;
; 1. Load IN .tbn into IN_BUF via HGTHD+HLOAD (once, before pass 1).
;
; 2. Pass 1 — table build, no emit:
;      Reset PASS_PC = 0; init symbol + local-label tables.
;      Rewind reader to first record.  Walk records:
;        KindInst        PASS_PC += 4
;        KindLabelDef    symbol_insert(symbol_id, PASS_PC)
;        KindLocalDef    local_def_append(digit, PASS_PC)
;        KindDirective   PASS_PC += directive size
;        KindComment     skip
;
; 3. Pass 2 — emit:
;      Reset PASS_PC = 0; reset OUT state.  Rewind reader.  Walk records:
;        KindInst        parse operands → form lookup → encode → emit;
;                        PASS_PC += 4 (kept in lockstep with OUT_LEN).
;        KindLabelDef    skip (already in table)
;        KindLocalDef    skip (already in table)
;        KindDirective   evaluate operands, emit bytes; PASS_PC += size.
;        KindComment     skip
;
; 4. After pass 2, HSAVE OUT_BUF[0..OUT_LEN] as "OUT".
;
; Per https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/specs/2026-05-24-m3-z80-emitter-design.md §2.6 / §2.5 / §3.
; Operand kind / directive id values come from
; tools/sam-aarch64-format/{operands,directives,kinds}.go.
;
; PASS_PC invariant (correctness gate for Tasks 5-6):
;   At any record boundary, PASS_PC = "address of the next record to
;   process".  Equivalently, in pass 2, PASS_PC == OUT_LEN at every
;   record boundary (M3's OUT_BUF starts at PC 0).  Both passes advance
;   by the same per-record amount, so the invariant follows.


; OperandKind / RecordKind / Directive-ID constants — the OP_KIND_* /
; REC_KIND_* / DIR_* equates that interpret a .tbn record stream.  Generated
; from the Go authority tools/sam-aarch64-format/{operands,kinds,directives}.go
; by tools/tables-gen (make tables); a hand edit or a forgotten regen fails
; `make tables-sync-check` in CI.  Equates only — zero runtime bytes.
                include "tbn_constants.inc"


; -----------------------------------------------------------------------
; main_assemble — orchestrate the two-pass assemble + the ENCTAB-in-
; section-A window bracketing.
;
; Called after load_enctab (which leaves LMPR = LMPR_DEFAULT).
;
; Sequence:
;   1. load_in_file (RST 8 — needs LMPR = LMPR_DEFAULT for ROM access)
;   2. enctab_map_in — page ENCTAB into section A for encoder reads
;   3. form_lookup_init — first call that READS ENCTAB; must be inside
;      the map_in window
;   4. Pass 1 — table build (reads ENCTAB heavily via form_lookup)
;   5. Pass 2 — emit (reads ENCTAB + writes OUT)
;   6. enctab_map_out — restore LMPR = LMPR_DEFAULT before returning so
;      the caller can safely call save_out_file (RST 8)
;
; Input: none.
; Output: OUT_BUF populated, OUT_LEN set; ready for HSAVE.
;         LMPR = LMPR_DEFAULT on return.
; On any error: jp fail.
; -----------------------------------------------------------------------
main_assemble:
; -- Load IN via HGTHD + HLOAD into IN_BUF (LMPR = boot default) ------
                call    load_in_file

; -- Bracket open: ENCTAB into section A for encoder reads -----------
                call    enctab_map_in

; -- First ENCTAB-reading call: compute form/index pointers ----------
                call    form_lookup_init

; ----- Pass 1: table build -------------------------------------------
                ld      a, PASS_PASS1
                ld      (PASS_MODE), a
                call    symbol_table_init
                call    local_label_table_init
                call    litpool_init
                call    pass_pc_reset
                call    reset_reader_to_in_buf
                call    walk_records
                call    litpool_flush               ; implicit end-of-source

; ----- Pass 2: emit --------------------------------------------------
                ld      a, PASS_PASS2
                ld      (PASS_MODE), a
                call    pass_pc_reset
                call    reset_out_buffer
                call    reset_reader_to_in_buf
; Pass 2 re-uses the LITPOOL_TABLE / LITPOOL_PC_MAP populated by pass 1.
; Reset the per-slot `pending` flag (cleared by pass-1 flush) so the
; pass-2 flush emits each slot exactly once.  The slot count and the
; pc-map (which pass 2 only reads) are untouched.
                call    litpool_reset_pending
                call    walk_records
                call    litpool_flush               ; implicit end-of-source

; -- Bracket close: restore LMPR so save_out_file's RST 8 finds ROM
; in section A.
                call    enctab_map_out
                ret


; -----------------------------------------------------------------------
; reset_reader_to_in_buf — IN_POS := (LMPR_IN_BASE, 0), then reader_init.
;
; reader_init validates the magic, skips the name table, and leaves the
; cursor at the first record.  It does not write outside the cursor
; vars, so calling it twice (once per pass) is safe.  Pass 2 uses this
; to rewind the in-memory cursor with no disk re-read.
;
; Input:  IN_END_PAGE / IN_END_OFFSET must be set (by load_in_file).
; Output: cursor at the first record's kind byte; LMPR back at
;         LMPR_ENCTAB (reader_init restores it before returning).
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
reset_reader_to_in_buf:
                ld      a, LMPR_IN_BASE
                ld      (IN_POS_PAGE), a
                ld      hl, 0
                ld      (IN_POS_OFFSET), hl
                jp      reader_init                 ; tail-call


; -----------------------------------------------------------------------
; reset_out_buffer — reset output cursor + counter to empty.
;
; Called before pass 2 only (pass 1 never emits).
;
; Per docs/specs/paged-out-design.md the OUT buffer
; lives in physical pages 5+6, reached via section B (&4000..&7FFF).
; OUT_PC starts at the section-B base; OUT_ZONE starts at 0 (= low
; zone, section B = page 5 for free under LMPR_ENCTAB).
;
; Input:  none.
; Output: OUT_PC = &4000; OUT_ZONE = 0; OUT_LEN = 0.
; Clobbers: A, HL.
; -----------------------------------------------------------------------
reset_out_buffer:
                ld      hl, &4000           ; section-B base
                ld      (OUT_PC), hl
                xor     a
                ld      (OUT_ZONE), a       ; start in low zone (page 5)
                ld      hl, 0
                ld      (OUT_LEN), hl
                ret


; -----------------------------------------------------------------------
; Paged-IN cursor helpers.
;
; Per docs/specs/paged-in-design.md.  Three primitives:
;   in_map_current   — write IN_POS_PAGE to LMPR, mapping the current
;                      IN page into section A (&0000..&3FFF).
;   in_persist_hl    — write HL back to IN_POS_OFFSET and snapshot the
;                      current LMPR into IN_POS_PAGE.  Used at the end
;                      of a reader bracket to commit the new cursor.
;   in_normalise_hl  — COMET-style adjustpo.  While H >= &40, subtract
;                      &40 from H and bump LMPR's low 5 bits.  The
;                      RAM0 bit at &20 is preserved across the inc
;                      because page numbers can't go above 31, so the
;                      low 5 bits never overflow into bit 5.
;
; All three use port 250 (LMPR).  Port 251 is HMPR (sections C+D)
; and is untouched here.  See trampoline.asm and the SAM Coupé Tech
; Manual §6.10 for the port assignments.
; -----------------------------------------------------------------------

; in_map_current — LMPR := IN_POS_PAGE; section A = current IN page.
;
; Input:  none.  Output: LMPR programmed.  Clobbers: A.
in_map_current:
                ld      a, (IN_POS_PAGE)
                out     (250), a            ; port 250 = LMPR
                ret


; in_persist_hl — IN_POS_OFFSET := HL; IN_POS_PAGE := current LMPR.
;
; Called at the end of a reader bracket so a later in_map_current
; restores the cursor's new position.
;
; Input:  HL = section-A offset (in [&0000, &4000)).
; Output: IN_POS_OFFSET / IN_POS_PAGE updated.  Clobbers: A.
in_persist_hl:
                ld      (IN_POS_OFFSET), hl
                in      a, (250)            ; A = current LMPR
                ld      (IN_POS_PAGE), a
                ret


; in_normalise_hl — while H >= &40, subtract &40 from H and bump LMPR's
; low 5 bits by 1 (the RAM0 bit at &20 stays set because the low 5 bits
; never exceed 31 in practice — IN spans pages 7..12 max post 2026-05-28
; bump, which is well under the 31-page low5 ceiling).
;
; Mirrors COMET's adjustpo (reference/comet-decoded/comet.asm:3180-3188)
; — the standard "renormalise (page, offset) after a section-A
; address-arithmetic step" idiom.
;
; Input:  HL = section-A-ish offset (possibly >= &4000 after an add).
; Output: HL normalised into [&0000, &4000).  LMPR bumped per page
;         boundary crossed.  Clobbers: A.
in_normalise_hl:
in_normalise_loop:
                ld      a, h
                cp      &40
                ret     c                   ; H < &40 → done
                sub     &40
                ld      h, a
                in      a, (250)
                inc     a                   ; low 5 bits += 1 (RAM0 bit
                                            ;   preserved — pages 7..12
                                            ;   never overflow into bit 5)
                out     (250), a
                jr      in_normalise_loop


; -----------------------------------------------------------------------
; PASS_PC helpers.
; -----------------------------------------------------------------------

; pass_pc_reset — PASS_PC := 0, ORIGIN_HIGH := 0.
;
; ORIGIN_HIGH is cleared here so that, absent a leading `.org` with a
; non-zero high word (i.e. the `-Ttext=0` fixture corpus, origin 0), every
; origin-relative push contributes high=0 — preserving M3/M4/M5/M6 byte
; output.  A leading `.org 0xfffffff0_00000000` re-sets it via
; main_dir_org_set_pc at the start of each pass's record walk.
;
; Input:  none.  Output: PASS_PC = 0, ORIGIN_HIGH = 0.  Clobbers: A, B, HL.
pass_pc_reset:
                xor     a
                ld      (PASS_PC + 0), a
                ld      (PASS_PC + 1), a
                ld      (PASS_PC + 2), a
                ld      (PASS_PC + 3), a
                ld      hl, ORIGIN_HIGH
                ld      b, 4
pass_pc_reset_oh:
                ld      (hl), a
                inc     hl
                djnz    pass_pc_reset_oh
                ret


; pass_pc_advance_4 — PASS_PC += 4.
;
; Used by KindInst (4 bytes per aarch64 instruction).
;
; Input:  none.  Output: PASS_PC bumped by 4.  Clobbers: A.
pass_pc_advance_4:
                ld      a, 4
                ; fall through into pass_pc_advance_a


; pass_pc_advance_a — PASS_PC += A (u8, zero-extended to 32 bits).
;
; Used by directive size computation when op_count × bytes-per-op
; fits in a byte.
;
; Input:  A = unsigned byte to add.
; Output: PASS_PC bumped by A.
; Clobbers: A, HL.
pass_pc_advance_a:
                ld      hl, PASS_PC
                add     a, (hl)
                ld      (hl), a
                ret     nc
                ; propagate carry through bytes 1..3
pass_pc_carry_b1:
                inc     hl
                ld      a, (hl)
                inc     a
                ld      (hl), a
                ret     nz
                inc     hl
                ld      a, (hl)
                inc     a
                ld      (hl), a
                ret     nz
                inc     hl
                ld      a, (hl)
                inc     a
                ld      (hl), a
                ret


; pass_pc_advance_de — PASS_PC += DE (u16, zero-extended to 32 bits).
;
; Used when summing string lengths (which can exceed 255).
;
; Input:  DE = unsigned word to add.
; Output: PASS_PC bumped by DE.
; Clobbers: A, HL, DE.
pass_pc_advance_de:
                ld      hl, PASS_PC
; byte 0 := (PASS_PC[0] + E)
                ld      a, (hl)
                add     a, e
                ld      (hl), a
                ld      e, 0
                jr      nc, pass_pc_advance_de_b1
                inc     e                           ; carry into byte 1
pass_pc_advance_de_b1:
                inc     hl
; byte 1 := PASS_PC[1] + D + carry-from-byte-0 (= E or 0).  Add D first,
; then E (carry).
                ld      a, (hl)
                add     a, d
                ld      d, 0
                jr      nc, pass_pc_advance_de_b1_addc
                inc     d
pass_pc_advance_de_b1_addc:
                add     a, e
                ld      (hl), a
                jr      nc, pass_pc_advance_de_b1_done
                inc     d
pass_pc_advance_de_b1_done:
                ld      a, d
                or      a
                ret     z
; Propagate D (final carry into bytes 2..3).
                inc     hl
                ld      a, (hl)
                inc     a
                ld      (hl), a
                ret     nz
                inc     hl
                ld      a, (hl)
                inc     a
                ld      (hl), a
                ret


; -----------------------------------------------------------------------
; walk_records — per-pass main loop.
;
; Dispatches on record kind.  Each handler chooses pass-1 / pass-2
; behaviour by reading PASS_MODE.  All handlers tail-jp back here when
; done.
;
; Input:  PASS_MODE set; reader positioned at first record.
; Output: when reader_at_end returns Z, returns to caller.
; -----------------------------------------------------------------------
walk_records:
                call    reader_at_end
                ret     z

                call    reader_next_kind    ; A = kind, HL = payload, BC = len

                cp      REC_KIND_DIRECTIVE
                jp      z, main_handle_directive
                cp      REC_KIND_COMMENT
                jp      z, walk_records             ; skip (both passes)
                cp      REC_KIND_INSN_RUN
                jp      z, main_handle_insn_run     ; compact `.tbn` v2 overlay
; LIT_DATA (0x08) — a 1-byte directive_id tag (irrelevant to the assembler)
; followed by raw output bytes; main_handle_lit_insts memcpys them.  (The
; old LIT_INSTS (0x07) run is gone — INSN_RUN mode 0 subsumes it.)
                cp      REC_KIND_LIT_DATA
                jp      z, main_handle_lit_insts
                jp      fail


; -----------------------------------------------------------------------
; main_handle_lit_insts — KindLitData (0x08) AND INSN_RUN (0x09) mode 0.
;
; Both are "a 1-byte tag the assembler ignores, then raw output bytes"
; (compact `.tbn` v2 — produced by `refenc -emit-compact-tbn`):
;   LIT_DATA      payload = [dir_id u8][raw…]   — constant data run (the
;                 directive_id only matters to the disassembler)
;   INSN_RUN mode 0 payload = [mode u8 = 0][word0..] — a run of fully-
;                 literal instructions (the LIT_INSTS floor; main_handle_insn_run
;                 dispatches mode 0 straight here, the mode byte playing the
;                 role of the ignored tag)
; In both, the run occupies (payload_len - 1) output bytes.
;
;   Pass 1: PASS_PC += nbytes.  No operand parse, no litpool scan.
;   Pass 2: memcpy the nbytes raw bytes straight to OUT (zero encoding
;           work), then PASS_PC += nbytes — keeping PASS_PC == OUT_LEN.
;
; PC accounting is identical to the instruction / data-directive records the
; run replaced, so label positions and 2-pass values are unchanged.
;
; Input:  HL = payload ptr, BC = payload len (= 1 + nbytes).
; Output: jp walk_records.
; -----------------------------------------------------------------------
main_handle_lit_insts:
; nbytes = payload_len - 1 (the leading 1-byte tag), so BC-1 is the
; output-byte count directly — no need to read the tag.
                dec     bc                  ; BC = nbytes
                inc     hl                  ; HL → first output byte
                ld      a, (PASS_MODE)
                cp      PASS_PASS1
                jr      z, advance_bc_and_walk      ; pass 1: no emit
; Pass 2: copy nbytes from (HL) to OUT (no encoding work).
                push    bc                  ; save nbytes across the emit
                call    main_emit_string_bytes  ; HL=ptr, BC=len → OUT
                pop     bc                  ; BC = nbytes
; fall through into advance_bc_and_walk


; advance_bc_and_walk — PASS_PC += BC (zero-extended), then jp
; walk_records.  Shared tail for the lit-insts handler and the directive
; size-advance paths (both arrive with the byte count in BC).
advance_bc_and_walk:
                ld      d, b
                ld      e, c                ; DE = byte count
                call    pass_pc_advance_de
                jp      walk_records


; -----------------------------------------------------------------------
; Directives: .byte / .short / .word / .quad / .ascii / .asciz / .hword.
;
; Both passes parse the directive header (dir_id + op_count), then:
;   * Pass 1: compute size, advance PASS_PC, return.
;   * Pass 2: dispatch to the per-directive emit routine (which
;             evaluates operands and emits bytes), then advance PASS_PC
;             by the same size.
;
; Sizes (per docs/specs/2026-05-24-m1-binary-tokenised-format-design.md
; §3 and aarch64 ELF as-rules):
;
;   DIR_TEXT  / DIR_DATA      0  (no-op for M3/M4)
;   DIR_BYTE                  op_count × 1
;   DIR_SHORT / DIR_HWORD     op_count × 2
;   DIR_WORD                  op_count × 4
;   DIR_QUAD                  op_count × 8
;   DIR_ASCII                 sum of string lengths
;   DIR_ASCIZ                 sum of string lengths + op_count (NUL/str)
; -----------------------------------------------------------------------
main_handle_directive:
                ld      (main_payload_ptr), hl
                ld      (main_payload_len), bc

; First byte: directive_id (u8).
                ld      a, (hl)
                inc     hl
                ld      (main_dir_id), a

; Second byte: operand_count (u8).
                ld      a, (hl)
                inc     hl
                ld      (main_op_count), a
                ld      (main_op_count_remaining), a
                ld      (main_opval_src), hl
                ld      (main_dir_payload_after_header), hl

; Branch on PASS_MODE: pass 1 → size-only; pass 2 → emit then advance.
                ld      a, (PASS_MODE)
                cp      PASS_PASS1
                jp      z, main_handle_directive_pass1
                jp      main_handle_directive_pass2


; ----- Pass 1: compute directive size, advance PASS_PC, return ---------
;
; Special-case the directives whose pass-1 effect is something other than
; "PASS_PC += size":
;
;   .equ / .set      — pass-1 inserts (symbol_id, value) into SYMTAB;
;                      size is 0.  Mac-side: refenc/pass1.go:166-170,
;                      :241-271 (resolveEquDirective).
;
;   .org             — pass-1 sets PASS_PC := target directly (not +=).
;                      Backward .org → fail.  Mac-side: pass1.go:173-203.
;                      OriginVMA (at-start .org with target != 0 from
;                      PASS_PC == 0) is punted here per M5 PR-A scope —
;                      no current M1 fixture exercises it.
;
; Every other directive falls through to compute_directive_size +
; pass_pc_advance_de.
main_handle_directive_pass1:
                ld      a, (main_dir_id)
                cp      DIR_EQU
                jp      z, main_dir_equ_pass1
                cp      DIR_SET
                jp      z, main_dir_equ_pass1
                cp      DIR_ORG
                jp      z, main_dir_org_pass1
                cp      DIR_LTORG
                jp      z, main_dir_ltorg

                call    compute_directive_size      ; result → BC (size, 16-bit)
                jp      advance_bc_and_walk


; ----- Pass 2: emit, then advance PASS_PC by directive size ------------
main_handle_directive_pass2:
                ld      a, (main_dir_id)
                cp      DIR_BYTE
                jp      z, main_dir_byte_emit
                cp      DIR_SHORT
                jp      z, main_dir_short_emit
                cp      DIR_HWORD
                jp      z, main_dir_short_emit
                cp      DIR_WORD
                jp      z, main_dir_word_emit
                cp      DIR_QUAD
                jp      z, main_dir_quad_emit
                cp      DIR_ASCII
                jp      z, main_dir_ascii_emit
                cp      DIR_ASCIZ
                jp      z, main_dir_asciz_emit
                cp      DIR_TEXT
                jp      z, walk_records             ; .text — no-op
                cp      DIR_DATA
                jp      z, walk_records             ; .data — no-op
; ---- M5 PR A directives -------------------------------------------------
                cp      DIR_EQU
                jp      z, walk_records             ; symbol already inserted in pass 1
                cp      DIR_SET
                jp      z, walk_records             ; .set is an .equ synonym
                cp      DIR_GLOBAL
                jp      z, walk_records             ; no-op (refenc/pass2.go:1719)
                cp      DIR_SECTION
                jp      z, walk_records             ; no-op (refenc/pass2.go:1721-1724)
                cp      DIR_ARCH
                jp      z, walk_records             ; no-op (refenc/pass2.go:1725-1728)
                cp      DIR_CPU
                jp      z, walk_records             ; no-op
                cp      DIR_SKIP
                jp      z, main_dir_skip_emit
                cp      DIR_SPACE
                jp      z, main_dir_skip_emit       ; same handler
                cp      DIR_INST
                jp      z, main_dir_inst_emit
                cp      DIR_BALIGN
                jp      z, main_dir_balign_emit
                cp      DIR_ALIGN
                jp      z, main_dir_align_emit
                cp      DIR_ORG
                jp      z, main_dir_org_pass2
                cp      DIR_LTORG
                jp      z, main_dir_ltorg
                jp      fail


; -----------------------------------------------------------------------
; compute_directive_size — work out how many bytes a directive emits.
;
; Reads (main_dir_id), (main_op_count), (main_dir_payload_after_header).
; Does NOT disturb main_opval_src (saved/restored when walking strings).
;
; Output: BC = size in bytes (u16).  For M3/M4 directives, sizes fit in
;         a u16 well within OUT_BUF (2 KB).
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
compute_directive_size:
                ld      a, (main_dir_id)
                cp      DIR_BYTE
                jp      z, compute_dir_size_x1
                cp      DIR_SHORT
                jp      z, compute_dir_size_x2
                cp      DIR_HWORD
                jp      z, compute_dir_size_x2
                cp      DIR_WORD
                jp      z, compute_dir_size_x4
                cp      DIR_QUAD
                jp      z, compute_dir_size_x8
                cp      DIR_ASCII
                jp      z, compute_dir_size_ascii
                cp      DIR_ASCIZ
                jp      z, compute_dir_size_asciz
                cp      DIR_TEXT
                jr      z, compute_dir_size_zero
                cp      DIR_DATA
                jr      z, compute_dir_size_zero
; ---- M5 PR A directives -------------------------------------------------
                cp      DIR_EQU
                jr      z, compute_dir_size_zero
                cp      DIR_SET
                jr      z, compute_dir_size_zero
                cp      DIR_GLOBAL
                jr      z, compute_dir_size_zero
                cp      DIR_SECTION
                jr      z, compute_dir_size_zero
                cp      DIR_ARCH
                jr      z, compute_dir_size_zero
                cp      DIR_CPU
                jr      z, compute_dir_size_zero
                cp      DIR_ORG
                jr      z, compute_dir_size_zero    ; .org handled in pass-1
                cp      DIR_INST
                jp      z, compute_dir_size_inst
                cp      DIR_SKIP
                jp      z, compute_dir_size_skip
                cp      DIR_SPACE
                jp      z, compute_dir_size_skip
                cp      DIR_BALIGN
                jp      z, compute_dir_size_balign
                cp      DIR_ALIGN
                jp      z, compute_dir_size_align
                cp      DIR_LTORG
                jp      z, compute_dir_size_zero    ; flush is PC-side; size 0
                jp      fail

compute_dir_size_zero:
                ld      bc, 0
                ret

; .inst — fixed 4 bytes (refenc/pass1.go:346-347).
compute_dir_size_inst:
                ld      bc, 4
                ret

; .skip / .space — size = eval(operand1).  We use the low 16 bits;
; M5 PR-A fixtures stay well under 64 KB.  Mac-side: pass1.go:337-345.
compute_dir_size_skip:
                call    main_eval_first_imm
                ld      a, (expr_result + 0)
                ld      c, a
                ld      a, (expr_result + 1)
                ld      b, a
                ret

; .balign N — size = (N - PC%N) % N.
; Mirrors refenc/pass1.go:365-381 with unsigned modulus.  N is read from
; the operand; PASS_PC is the current pc at directive entry.
;
; Both passes call this routine: in pass 1 PASS_PC is the pre-directive
; PC (advanced afterwards via pass_pc_advance_de); in pass 2 the emit
; handler runs first but doesn't advance PASS_PC, then dir_emit_done
; re-calls compute_directive_size — at which point PASS_PC is still the
; pre-directive PC, so the recomputed pad matches what was emitted.
compute_dir_size_balign:
                call    main_eval_first_imm         ; expr_result = N (s64)
                jp      compute_balign_pad_from_n


; .align N — Mac convention `align to 2^N`.  Same as .balign with
; align = 1 << N.  N = 0 is a no-op (pad = 0).  M5 PR-A only sees
; small N (corpus values are 2..4), so we just shift in place.
compute_dir_size_align:
                call    main_eval_first_imm         ; expr_result = N (s64)
; `.align N` aligns to 2^N bytes.  Valid N on the SAM is 1..15: the OUT
; buffer is 32 KB (pages 5 + 6), so an alignment of 2^16 or more can
; never be reached within it and the 16-bit pad machinery cannot hold it.
; Reject N >= 16 (and negatives / N >= 256, which show up as a non-zero
; byte above the low byte) with tag a0, rather than letting 1<<N overflow
; HL to a silent no-op.
                ld      hl, expr_result + 1
                ld      b, 7
                xor     a
compute_dir_size_align_hi:
                or      (hl)                        ; A = OR of bytes 1..7
                inc     hl                          ; (16-bit inc: no flags)
                djnz    compute_dir_size_align_hi   ; (no flags)
                jp      nz, compute_dir_size_align_bad  ; N negative or >= 256
                ld      a, (expr_result + 0)
                or      a
                jr      z, compute_dir_size_align_pad0  ; N == 0 → pad 0
                cp      16
                jp      nc, compute_dir_size_align_bad  ; N >= 16 → too large
; HL = 1 << A for N in 1..15 (alignment 2..32768, 16-bit representable).
                ld      hl, 1
compute_dir_size_align_shift:
                add     hl, hl
                dec     a
                jr      nz, compute_dir_size_align_shift
; Stash HL as N in expr_result+0..1 so compute_balign_pad_from_n can pick
; it up.  Top bytes are already zero from main_eval_first_imm (expr is
; small positive); but zero them explicitly to be safe.
                ld      a, l
                ld      (expr_result + 0), a
                ld      a, h
                ld      (expr_result + 1), a
                xor     a
                ld      (expr_result + 2), a
                ld      (expr_result + 3), a
                jp      compute_balign_pad_from_n

compute_dir_size_align_bad:
                ld      a, &a0
                jp      fail_with_tag               ; tag a0: .align exponent out of SAM range

compute_dir_size_align_pad0:
                ld      bc, 0
                ret


; compute_balign_pad_from_n — given N in (expr_result + 0..1) (16-bit
; unsigned), compute pad = (N - PASS_PC%N) % N.  Returns BC = pad.
;
; PASS_PC is a 32-bit value.  For all M5 PR-A fixtures N is a small
; power of two (≤ 16), so PASS_PC % N == (PASS_PC low byte) & (N-1).
; Use the low-byte mask form when N is a power of two for simplicity;
; fall back to long division otherwise (no current fixture needs it).
compute_balign_pad_from_n:
                ld      a, (expr_result + 1)
                or      a
                jp      nz, compute_balign_pad_large_n
                ld      a, (expr_result + 0)
                cp      2
                jp      c, compute_balign_pad_zero  ; N < 2 → pad = 0
; Check power-of-two: N & (N-1) == 0.
                ld      b, a                        ; B = N
                dec     a                           ; A = N-1
                ld      c, a                        ; C = N-1
                and     b
                jp      nz, fail
; PASS_PC low byte AND (N-1) gives remainder for power-of-two N.
                ld      a, (PASS_PC + 0)
                and     c                           ; A = PASS_PC % N
                jp      z, compute_balign_pad_zero  ; already aligned
                ld      d, a                        ; D = remainder
                ld      a, b                        ; A = N
                sub     d                           ; A = N - remainder
                ld      c, a
                ld      b, 0
                ret

compute_balign_pad_zero:
                ld      bc, 0
                ret

; compute_balign_pad_large_n — N >= 256 path.  Supports N up to 16 bits
; (covers spectrum4's `.align 12` = 4096, `.align 11` = 2048, etc.).
; N is in (expr_result + 0..1) as a 16-bit unsigned; bytes 2..3 are 0
; (text2bin's `-flatten` constant-folds the operand into a 16-bit
; immediate for all observed cases; values > 0xFFFF surface as tag 63).
;
; Assumes N is a power-of-two (which it is for `.align <small N>` =
; 1 << N).  pad = (N - (PASS_PC mod N)) mod N.  For power-of-two N this
; reduces to pad = (N - (PASS_PC & (N-1))) & (N-1).
compute_balign_pad_large_n:
; Reject N >= 2^16.
                ld      a, (expr_result + 2)
                or      a
                jr      nz, balign_pad_large_overflow
                ld      a, (expr_result + 3)
                or      a
                jr      nz, balign_pad_large_overflow
; HL = N (16-bit).
                ld      hl, (expr_result + 0)
; DE = N - 1 (mask).
                ld      d, h
                ld      e, l
                dec     de
; Validate power-of-two: N AND (N-1) == 0.
                ld      a, l
                and     e
                jr      nz, balign_pad_large_not_pow2
                ld      a, h
                and     d
                jr      nz, balign_pad_large_not_pow2
; remainder = (PASS_PC low16) AND mask.
                ld      a, (PASS_PC + 0)
                and     e
                ld      c, a
                ld      a, (PASS_PC + 1)
                and     d
                ld      b, a
; pad = N - remainder.  If remainder == 0, pad = 0 (already aligned).
                ld      a, b
                or      c
                jr      z, balign_pad_large_zero
                ld      a, l
                sub     c
                ld      c, a
                ld      a, h
                sbc     a, b
                ld      b, a
                ret
balign_pad_large_zero:
                ld      bc, 0
                ret
balign_pad_large_overflow:
                ld      a, &63
                jp      fail_with_tag       ; tag 63: .balign N >= 2^16
balign_pad_large_not_pow2:
                ld      a, &62
                jp      fail_with_tag       ; tag 62: .balign N (>=256) not power-of-two

; size = op_count * 1
compute_dir_size_x1:
                ld      a, (main_op_count)
                ld      c, a
                ld      b, 0
                ret

; size = op_count * 2
compute_dir_size_x2:
                ld      a, (main_op_count)
                ld      c, a
                ld      b, 0
                sla     c
                rl      b
                ret

; size = op_count * 4
compute_dir_size_x4:
                ld      a, (main_op_count)
                ld      c, a
                ld      b, 0
                sla     c
                rl      b
                sla     c
                rl      b
                ret

; size = op_count * 8
compute_dir_size_x8:
                ld      a, (main_op_count)
                ld      c, a
                ld      b, 0
                sla     c
                rl      b
                sla     c
                rl      b
                sla     c
                rl      b
                ret

; size = sum of STRING operand lengths
compute_dir_size_ascii:
                ld      hl, 0
                ld      (compute_dir_size_acc), hl
                call    walk_strings_sum_lengths
                ld      bc, (compute_dir_size_acc)
                ret

; size = sum of STRING lengths + op_count (one NUL per string)
compute_dir_size_asciz:
                ld      hl, 0
                ld      (compute_dir_size_acc), hl
                call    walk_strings_sum_lengths
                ld      hl, (compute_dir_size_acc)
                ld      a, (main_op_count)
                ld      c, a
                ld      b, 0
                add     hl, bc
                ld      b, h
                ld      c, l
                ret


; walk_strings_sum_lengths — walk the STRING operand list starting at
; (main_dir_payload_after_header) and add each string's len u16 into
; compute_dir_size_acc.  Does NOT modify main_opval_src.
;
; Per-string layout: [kind=0x09][len u16 LE][bytes...].
;
; Input:  (main_dir_payload_after_header) = first operand byte.
;         (main_op_count) = number of STRING operands.
; Output: (compute_dir_size_acc) += sum of all string lengths.
; Clobbers: A, BC, DE, HL.
walk_strings_sum_lengths:
                ld      a, (main_op_count)
                or      a
                ret     z
                ld      b, a                        ; B = strings remaining
                ld      hl, (main_dir_payload_after_header)

walk_strings_loop:
                ld      a, (hl)                     ; kind byte
                cp      OP_KIND_STRING
                jp      nz, fail
                inc     hl
                ld      e, (hl)                     ; len LSB
                inc     hl
                ld      d, (hl)                     ; len MSB
                inc     hl                          ; HL → string bytes

; acc += DE
                push    bc                          ; preserve counter
                push    hl                          ; preserve cursor
                ld      hl, (compute_dir_size_acc)
                add     hl, de
                ld      (compute_dir_size_acc), hl
                pop     hl
                pop     bc

; Skip the string bytes: HL += DE.
                add     hl, de
                djnz    walk_strings_loop
                ret


; -----------------------------------------------------------------------
; Pass-2 directive emit routines.
;
; Each routine evaluates operands and emits bytes (existing M3 logic).
; On completion it jumps to dir_emit_done, which advances PASS_PC by
; the directive size (recomputed via compute_directive_size to keep the
; pass-1 / pass-2 sizing in lockstep).
; -----------------------------------------------------------------------
main_dir_byte_emit:
                call    main_eval_each_imm_init
main_dir_byte_loop:
                ld      a, (main_op_count_remaining)
                or      a
                jp      z, dir_emit_done
                call    main_eval_next_imm
                ld      a, (expr_result + 0)
                call    emit_byte
                jp      main_dir_byte_loop

main_dir_short_emit:
                call    main_eval_each_imm_init
main_dir_short_loop:
                ld      a, (main_op_count_remaining)
                or      a
                jp      z, dir_emit_done
                call    main_eval_next_imm
                ld      a, (expr_result + 0)
                call    emit_byte
                ld      a, (expr_result + 1)
                call    emit_byte
                jp      main_dir_short_loop

main_dir_word_emit:
                call    main_eval_each_imm_init
main_dir_word_loop:
                ld      a, (main_op_count_remaining)
                or      a
                jp      z, dir_emit_done
                call    main_eval_next_imm
                ld      a, (expr_result + 0)
                call    emit_byte
                ld      a, (expr_result + 1)
                call    emit_byte
                ld      a, (expr_result + 2)
                call    emit_byte
                ld      a, (expr_result + 3)
                call    emit_byte
                jp      main_dir_word_loop

main_dir_quad_emit:
                call    main_eval_each_imm_init
main_dir_quad_loop:
                ld      a, (main_op_count_remaining)
                or      a
                jp      z, dir_emit_done
                call    main_eval_next_imm
                ld      b, 8
                ld      hl, expr_result
main_dir_quad_inner:
                ld      a, (hl)
                push    bc
                push    hl
                call    emit_byte
                pop     hl
                pop     bc
                inc     hl
                djnz    main_dir_quad_inner
                jp      main_dir_quad_loop


; .ascii / .asciz — operands are STRING records.  Each STRING:
;   kind=0x09, len u16, bytes…
main_dir_ascii_emit:
                ld      a, (main_op_count_remaining)
                or      a
                jp      z, dir_emit_done
                ld      hl, (main_opval_src)
                ld      a, (hl)             ; kind byte
                cp      OP_KIND_STRING
                jp      nz, fail
                inc     hl
                ld      c, (hl)
                inc     hl
                ld      b, (hl)
                inc     hl                  ; HL → string bytes; BC = len
                push    bc
                call    main_emit_string_bytes
                pop     bc
                add     hl, bc
                ld      (main_opval_src), hl
                ld      a, (main_op_count_remaining)
                dec     a
                ld      (main_op_count_remaining), a
                jp      main_dir_ascii_emit

main_dir_asciz_emit:
                ld      a, (main_op_count_remaining)
                or      a
                jp      z, dir_emit_done
                ld      hl, (main_opval_src)
                ld      a, (hl)
                cp      OP_KIND_STRING
                jp      nz, fail
                inc     hl
                ld      c, (hl)
                inc     hl
                ld      b, (hl)
                inc     hl
                push    bc
                call    main_emit_string_bytes
                pop     bc
                add     hl, bc
                ld      (main_opval_src), hl
; Emit trailing NUL.
                xor     a
                call    emit_byte
                ld      a, (main_op_count_remaining)
                dec     a
                ld      (main_op_count_remaining), a
                jp      main_dir_asciz_emit


; dir_emit_done — common pass-2 directive tail: recompute size, advance
; PASS_PC, return to walk_records.
;
; Recomputing is safer than threading the size through the emit loops
; (which clobber lots of state).  compute_directive_size reads only
; main_dir_id / main_op_count / main_dir_payload_after_header — all of
; which are untouched by the emit loops.
dir_emit_done:
                call    compute_directive_size
                jp      advance_bc_and_walk


; -----------------------------------------------------------------------
; M5 PR-A directive handlers (Tasks 1-3).
; -----------------------------------------------------------------------

; ---- .equ / .set (pass 1) — insert (symbol_id, value) into SYMTAB ----
;
; Both operands are IMM_EXPR records (see emitEqu in
; tools/text2bin/internal/translate/flatten.go).  Operand 1's bytecode
; is exactly [PUSH_SYM=0x05][id_lo][id_hi] (3 bytes) — we extract the
; id by inspection rather than evaluating (the symbol may not be in
; SYMTAB yet, but we only want its id).  Operand 2 is the value
; expression — eval it via main_eval_next_imm.
;
; Pass-2 .equ / .set are no-ops (the symbol was inserted in pass 1).
;
; Mac-side reference: refenc/pass1.go:241-271 (resolveEquDirective);
; format/operands writer: text2bin/internal/translate/flatten.go:283-296
; (emitEqu).
main_dir_equ_pass1:
; Position main_opval_src at operand 1.
                ld      hl, (main_dir_payload_after_header)

; Operand 1 layout: [kind=0x05 IMM_EXPR][len u16][bytecode...].
                ld      a, (hl)
                cp      OP_KIND_IMM_EXPR
                jp      nz, fail
                inc     hl                          ; HL → len LSB
                ld      e, (hl)
                inc     hl
                ld      d, (hl)
                inc     hl                          ; HL → bytecode start

; Validate bytecode shape: must be exactly 3 bytes [PUSH_SYM, id_lo, id_hi].
                ld      a, d
                or      a
                jp      nz, fail
                ld      a, e
                cp      3
                jp      nz, fail
                ld      a, (hl)
                cp      &05                         ; PUSH_SYM opcode
                jp      nz, fail
                inc     hl                          ; HL → id_lo
                ld      c, (hl)
                inc     hl
                ld      b, (hl)                     ; BC = symbol_id (C=lo, B=hi)
                inc     hl                          ; HL past operand-1 bytecode

; Stash symbol id; HL still on the kind byte of operand 2.
                ld      (main_dir_equ_pending_id), bc
                ld      (main_opval_src), hl
                ld      a, 1
                ld      (main_op_count_remaining), a    ; only operand 2 left

; Evaluate operand 2 → expr_result (8 bytes LE).
                call    main_eval_next_imm

; Copy low 4 bytes of expr_result into symbol_value_buf.  The address
; field in SYMTAB is u32 — same width as PASS_PC.  M4's evaluator
; already returns 8-byte LE; we drop the high 4 bytes here.
                ld      a, (expr_result + 0)
                ld      (symbol_value_buf + 0), a
                ld      a, (expr_result + 1)
                ld      (symbol_value_buf + 1), a
                ld      a, (expr_result + 2)
                ld      (symbol_value_buf + 2), a
                ld      a, (expr_result + 3)
                ld      (symbol_value_buf + 3), a

; Classify origin-relative vs absolute: compare the evaluated high word
; expr_result[4..7] against ORIGIN_HIGH.  Equal → origin-relative (e.g.
; `.set FOO, some_label`); unequal → absolute constant (e.g.
; `.set RAM_DISK_SIZE, 0x10000000`, high word 0).  Mark the absolute case
; so eval_push_sym does NOT re-apply ORIGIN_HIGH to it.  See
; assembler.asm SYMTAB_ABS_BITMAP.  (Faithful to Go where Symbols[name]
; carries the full evaluated value with no eval-time origin fixup.)
                ld      hl, expr_result + 4
                ld      de, ORIGIN_HIGH
                ld      b, 4
main_dir_equ_high_cmp:
                ld      a, (de)
                cp      (hl)
                jr      nz, main_dir_equ_mark_abs
                inc     hl
                inc     de
                djnz    main_dir_equ_high_cmp
                jr      main_dir_equ_do_insert      ; high == ORIGIN_HIGH → origin-relative
main_dir_equ_mark_abs:
; Absolute classification: SYMTAB stores only the low 32 bits.  A 64-bit
; consumer (.quad, X-register movz) needs the full value, so a constant
; whose magnitude exceeds 32 bits would silently lose its high word.  The
; faithful representation in low-32 storage is exactly a value that
; sign-extends from bit 31: high32 must equal 0x00000000 (non-negative,
; bit 31 clear) or 0xFFFFFFFF (negative, bit 31 set).  Anything else
; (e.g. 0x1_00000000) genuinely truncates — refuse it with a tagged fail
; rather than emit wrong bytes.  Negative absolutes (high32 = 0xFFFFFFFF)
; pass here and round-trip correctly through every <=32-bit consumer;
; eval reconstructs their high word as 0 (eval_push_sym_zero_high), which
; only matters for a 64-bit consumer of a negative .set — a distinct,
; pre-existing limitation, not made worse here (i73).
                ld      a, (expr_result + 3)
                add     a, a                ; bit 31 -> carry
                sbc     a, a                ; A = 0x00 (CY=0) or 0xFF (CY=1) = expected sign byte
                ld      e, a
                ld      hl, expr_result + 4
                ld      b, 4
main_dir_equ_signext_cmp:
                ld      a, (hl)
                cp      e
                jr      nz, main_dir_equ_trunc_fail
                inc     hl
                djnz    main_dir_equ_signext_cmp
                ld      hl, (main_dir_equ_pending_id)
                call    symbol_mark_absolute
                jr      main_dir_equ_do_insert
main_dir_equ_trunc_fail:
                ld      a, &64
                jp      fail_with_tag       ; tag 64: .equ/.set absolute value exceeds signed/unsigned 32-bit range
main_dir_equ_do_insert:

; Insert (id, value) — duplicate id → symbol_insert does jp fail.
                ld      hl, (main_dir_equ_pending_id)
                call    symbol_insert
                jp      walk_records


; ---- .org (pass 1) — PASS_PC := target -------------------------------
;
; Per M5 PR-A scope, OriginVMA semantics (at-start .org with target > 0
; from PASS_PC == 0) are punted — no PR-A fixture exercises it.
; Backward .org is an error.  Pass-2 mirror is below.
;
; Mac-side reference: refenc/pass1.go:194-203.
main_dir_org_pass1:
                call    main_eval_first_imm         ; expr_result = target
; Backward .org is an error in pass 1 too — without this, pass 1 sets
; PASS_PC backward and computes garbage label addresses before pass 2's
; mirror check catches it (tag a1; mirrors Go pass1.go:297).  PASS_PC
; tracks the low 32 bits, so compare target's low 32 against PASS_PC
; within the current origin window.  A leading origin .org (e.g.
; 0xfffffff0_00000000) has low 32 == PASS_PC == 0 at the start of the
; pass, so it is not backward.
                ld      a, (expr_result + 0)
                ld      hl, PASS_PC
                sub     (hl)
                ld      a, (expr_result + 1)
                inc     hl
                sbc     a, (hl)
                ld      a, (expr_result + 2)
                inc     hl
                sbc     a, (hl)
                ld      a, (expr_result + 3)
                inc     hl
                sbc     a, (hl)                      ; CY = (target_low32 < PASS_PC)
                jp      c, main_dir_org_backward
                jp      main_dir_org_set_pc

main_dir_org_backward:
                ld      a, &a1
                jp      fail_with_tag               ; tag a1: backward .org

; ---- .org (pass 2) — zero-fill from current PC to target ------------
;
; PR-A scope: 16-bit delta only (max 64 KB pad).  M5's IN/OUT buffers
; are 2 KB each, so anything larger would be unusable anyway.  Larger
; deltas are out-of-scope until a fixture needs them.
;
; Mirrors refenc/pass2.go:102-128: emit (target - pc) zero bytes and
; set PASS_PC = target.
main_dir_org_pass2:
                call    main_eval_first_imm         ; expr_result = target

; Validate target's upper 16 bits match PASS_PC (else delta > 64 K).
                ld      a, (expr_result + 2)
                ld      hl, PASS_PC + 2
                cp      (hl)
                jp      nz, fail
                ld      a, (expr_result + 3)
                inc     hl
                cp      (hl)
                jp      nz, fail
; BC = delta = (target_low16 - PASS_PC_low16).  Backward → fail.
                ld      a, (expr_result + 0)
                ld      hl, PASS_PC
                sub     (hl)
                ld      c, a
                ld      a, (expr_result + 1)
                inc     hl
                sbc     a, (hl)
                ld      b, a
                jp      c, fail
main_dir_org_emit_loop:
                ld      a, b
                or      c
                jr      z, main_dir_org_set_pc
                push    bc
                xor     a
                call    emit_byte
                pop     bc
                dec     bc
                jr      main_dir_org_emit_loop

; ---- .ltorg — flush the literal pool (both passes) -------------------
;
; Pass 1: advance PASS_PC by sum(pending pool entry sizes), recording
;         each slot's entry_pc.  No emit.
; Pass 2: same alignment / PC accounting + emit pool bytes.
;
; The litpool_flush helper checks PASS_MODE internally and does the
; right thing.  Both passes follow `walk_records` so a single shared
; handler suffices.
;
; Mac-side reference: refenc/pass1.go:171-172 + refenc/pass2.go:96-100.
main_dir_ltorg:
                call    litpool_flush
                jp      walk_records


; PASS_PC := expr_result[0..3]; ORIGIN_HIGH := expr_result[4..7].
; Shared by pass-1 and pass-2 .org tails.
;
; PASS_PC tracks only the low 32 bits of the VMA (sufficient: the image
; is < 4 GB, so every label's offset fits the low word).  The high 32
; bits of the origin are stashed in ORIGIN_HIGH and re-applied by the
; origin-relative push handlers (eval_push_sym / eval_push_pc /
; eval_push_local) so a `.quad <label>` materialises the full 64-bit
; origin-relative address.  Mirrors the Go reference, where `pc` (and
; thus every symbol value) is seeded from OriginVMA — the full 64-bit
; origin flows into `.quad` via evalImmsAsBytes (refenc/pass2.go:1775).
main_dir_org_set_pc:
                ld      a, (expr_result + 0)
                ld      (PASS_PC + 0), a
                ld      a, (expr_result + 1)
                ld      (PASS_PC + 1), a
                ld      a, (expr_result + 2)
                ld      (PASS_PC + 2), a
                ld      a, (expr_result + 3)
                ld      (PASS_PC + 3), a
                ld      hl, expr_result + 4
                ld      de, ORIGIN_HIGH
                ld      bc, 4
                ldir
                jp      walk_records


; ---- .skip / .space (pass 2 emit) — N zero bytes --------------------
;
; N is computed by compute_dir_size_skip and returned in BC.  We loop
; emit_byte N times.  After emit, jp dir_emit_done re-runs the size
; compute (same value) and advances PASS_PC.
;
; Mac-side reference: refenc/pass2.go:1763-1770.
main_dir_skip_emit:
                call    compute_directive_size      ; BC = N (size, 16-bit)
                ld      a, b
                or      c
                jp      z, dir_emit_done
main_dir_skip_emit_loop:
                push    bc
                xor     a
                call    emit_byte
                pop     bc
                dec     bc
                ld      a, b
                or      c
                jr      nz, main_dir_skip_emit_loop
                jp      dir_emit_done


; ---- .inst (pass 2 emit) — one 32-bit LE word ------------------------
;
; Mac-side reference: refenc/pass1.go:346-347 (size = 4).  Pass-2 emit
; is by-inspection: evaluate the operand expression and emit four bytes
; little-endian.
main_dir_inst_emit:
                call    main_eval_first_imm         ; expr_result = u32 (low 4 bytes)
                ld      a, (expr_result + 0)
                call    emit_byte
                ld      a, (expr_result + 1)
                call    emit_byte
                ld      a, (expr_result + 2)
                call    emit_byte
                ld      a, (expr_result + 3)
                call    emit_byte
                jp      dir_emit_done


; ---- .balign / .align (pass 2 emit) — pad bytes ---------------------
;
; compute_directive_size returns the pad count in BC.  We mirror
; refenc's alignPadBytes (tools/refenc/pass2.go:403-429): zero-fill
; leading bytes until PC is 4-aligned, then NOPs (0xd503201f LE) while
; >= 4 bytes remain, then trailing zeros.
;
; D tracks the running emit-PC offset since the directive started; we
; recompute (PASS_PC + D) % 4 = (PASS_PC + D) & 3 each iteration to
; decide between zero and NOP.  PASS_PC itself is left alone so
; dir_emit_done's compute_directive_size re-evaluates to the same pad.
;
; PR-A fixtures all have pad = 0 (the fixtures' `.balign` directives
; hit at PC=0, where every alignment is a no-op).  The handler is
; general so future promotions don't surface byte-mismatch bugs.
main_dir_balign_emit:
main_dir_align_emit:
                call    compute_directive_size      ; BC = pad
                ld      a, b
                or      c
                jp      z, dir_emit_done
                ld      d, 0                        ; D = bytes emitted so far

main_dir_pad_loop:
                ld      a, b
                or      c
                jp      z, dir_emit_done

; (cur_pc & 3) = (PASS_PC[0] + D) & 3.
                ld      a, (PASS_PC + 0)
                add     a, d
                and     3
                jr      nz, main_dir_pad_zero       ; not 4-aligned → zero

; 4-aligned.  Emit NOP if at least 4 bytes remain; else fall through.
                ld      a, c
                cp      4
                jr      nc, main_dir_pad_nop
                ld      a, b
                or      a
                jr      z, main_dir_pad_zero        ; < 4 left → trailing zero

main_dir_pad_nop:
                push    bc
                push    de
                ld      a, &1f
                call    emit_byte
                ld      a, &20
                call    emit_byte
                ld      a, &03
                call    emit_byte
                ld      a, &d5
                call    emit_byte
                pop     de
                pop     bc
                dec     bc
                dec     bc
                dec     bc
                dec     bc
                ld      a, d
                add     a, 4
                ld      d, a
                jr      main_dir_pad_loop

main_dir_pad_zero:
                push    bc
                push    de
                xor     a
                call    emit_byte
                pop     de
                pop     bc
                dec     bc
                inc     d
                jr      main_dir_pad_loop


main_emit_string_bytes:
; Input: HL = bytes ptr, BC = length.  Emit each byte via emit_byte.
                ld      a, b
                or      c
                ret     z
main_emit_string_loop:
                ld      a, (hl)
                push    hl
                push    bc
                call    emit_byte
                pop     bc
                pop     hl
                inc     hl
                dec     bc
                ld      a, b
                or      c
                jr      nz, main_emit_string_loop
                ret


; -----------------------------------------------------------------------
; main_eval_each_imm_init — set state for walking N IMM_EXPR operands.
; No-op for now (main_opval_src and main_op_count_remaining were set
; on entry to main_handle_directive).
; -----------------------------------------------------------------------
main_eval_each_imm_init:
                ret


; -----------------------------------------------------------------------
; main_eval_first_imm — reset main_opval_src to the start of the
; directive's operand list, then evaluate the first IMM_EXPR.  Used by
; compute_directive_size for PC-dependent / size-from-operand directives
; (.skip, .space, .inst, .balign, .align, .org) so they can be
; re-evaluated in dir_emit_done after the emit loop has consumed
; main_opval_src.  M5 PR-A.
;
; Side-effect: decrements main_op_count_remaining (harmless — pass-1's
; main_handle_directive_pass1 doesn't read it afterwards, and pass-2
; emit handlers manage their own counter explicitly).
;
; Input:  none (reads main_dir_payload_after_header).
; Output: expr_result = 8-byte LE eval of operand 1.
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
main_eval_first_imm:
                ld      hl, (main_dir_payload_after_header)
                ld      (main_opval_src), hl
                jp      main_eval_next_imm          ; tail-call


; -----------------------------------------------------------------------
; main_eval_next_imm — consume the next IMM_EXPR operand from
; (main_opval_src), evaluate it, leave the 8-byte LE result in
; expr_result.  Decrements main_op_count_remaining.
; -----------------------------------------------------------------------
main_eval_next_imm:
                ld      hl, (main_opval_src)
                ld      a, (hl)             ; kind
                cp      OP_KIND_IMM_EXPR
                jp      nz, fail
                inc     hl
                ld      c, (hl)
                inc     hl
                ld      b, (hl)
                inc     hl                  ; HL → bytecode start
                push    bc
                call    eval_expr_const     ; HL=ptr, BC=len → expr_result
                pop     bc
                ld      hl, (main_opval_src)
                inc     hl
                inc     hl
                inc     hl                  ; HL → bytecode start
                add     hl, bc
                ld      (main_opval_src), hl
                ld      a, (main_op_count_remaining)
                dec     a
                ld      (main_op_count_remaining), a
                ret


; -----------------------------------------------------------------------
; load_in_file — two-phase HGTHD + trampoline-HLOAD loading ONLY the
; assembler-facing prefix of the "IN" .tbn into pages 7..12.
;
; Per docs/specs/paged-in-design.md and docs/specs/samdos-file-io.md.
;
; WHY TWO PHASES: the byte count HLOAD needs is caller-supplied via C/DE
; (samdos-file-io.md trampoline contract; trampoline.asm::trampoline_body
; comment "C = pages count, DE = length modulo 16K"), and the assembler-
; facing prefix length lives at file offset 8 — so a small head read must
; come first to discover the prefix boundary before the full load.
;
; WHY EARLY TRUNCATION IS SAFE: SAMDOS's count-driven HLOAD loop
; (samdos/src/c.s:354-369 ctas) stops at the requested byte exactly.
; This is the same mechanism that ends every normal whole-file load
; mid-sector at the file tail — requesting fewer bytes than the on-disk
; file holds is safe.  The full file stays on disk; only the prefix
; enters the IN buffer.
;
; WHAT THIS BUYS (i40 load-path / i51): the editor region (front-coded
; name table, .global flags, comment sidecar at editor_region_offset)
; never enters the IN buffer, so comment volume no longer limits what
; the SAM can assemble.  reader_init derives the same editor_region_offset
; boundary and overwrites IN_END with it, so the record walk stops at
; exactly the assembler-facing prefix end — consistent with the load.
;
; Phase 1 (head read): fill_uifa + HGTHD.  Read the whole-file geometry
; from DIFA (&4B50+34..36), masking bit 7 of the high length byte as
; SAMDOS's `set 7, d` marker (h.s:hgthd).  Load 512 bytes into page 7
; (clamped to the full file when the file is smaller), enough to cover
; the fixed 12-byte header plus both header tables.
;
; Phase 2 (prefix load): re-issue fill_uifa + HGTHD (HLOAD consumes the
; sector-chain state HGTHD arms, so HGTHD must be re-issued for the
; second load).  Decode editor_region_offset from bytes &0008..&000A of
; the head-loaded page 7 (same decode as reader_init, mirroring
; reader.asm::reader_init lines ~110-140).  Normalise to SAMDOS's
; pages/remainder convention: if remainder == 0 and pages > 0, adjust
; to pages-1 / 16384 (HGTHD emits pages-1/16384 for page-aligned lengths
; — see harness HGTHD emulation and samdos-file-io.md register contract).
; Bounds-check the prefix (≤ 6 pages; tag &03 otherwise — the FILE may
; exceed 6 pages, but the prefix must fit in the IN buffer), store into
; in_file_pages / in_file_len, call TRAMPOLINE_DST.
;
; Output: IN_END_PAGE / IN_END_OFFSET set to the prefix end; the prefix
;         is resident in pages 7..7+pages-1; LMPR restored to whatever
;         the caller had (TRAMPOLINE_DST touches HMPR only).
;
; Citation: samdos/src/h.s::hgthd lines 59-67 + ::txhed; c.s:354-369
; ctas (count-driven stop); trampoline.asm::trampoline_body register
; contract.  The editor_region_offset decode mirrors
; reader.asm::reader_init lines ~110-140.
; -----------------------------------------------------------------------
load_in_file:
; -- Phase 1: head read — 512 bytes into page 7 --------------------------
                ld      hl, name_IN
                call    fill_uifa
                rst     8
                defb    HOOK_HGTHD          ; longjmps on file-not-found

; Read the whole-file geometry from DIFA (&4B50+34..36).
                ld      hl, (&4B50 + 35)
                ld      a, h
                and     &7F                 ; clear SAMDOS's `set 7, d` marker
                ld      h, a
                ld      (in_file_len), hl   ; whole-file length-mod-16K (temp)
                ld      a, (&4B50 + 34)
                ld      (in_file_pages), a  ; whole-file page count (temp)

; Request a 512-byte head load, clamped to the actual file size when the
; file is < 512 bytes (small fixtures).  Request C=0, DE=512 unless the
; whole file fits in fewer bytes, in which case use the whole-file size.
; (A file is < 512 bytes only when pages == 0 and length-mod-16K < 512.)
                ld      b, IN_BASE_PAGE
                ld      c, 0                ; 0 full pages
                ld      de, 512             ; 512 bytes
                ld      a, (in_file_pages)
                or      a
                jr      nz, load_in_head_call   ; pages > 0 — head fits
                ld      hl, (in_file_len)
                ld      a, h
                cp      2                       ; HL >= 512?
                jr      nc, load_in_head_call   ; yes — head fits
                ld      d, h
                ld      e, l                    ; no — clamp to actual size (DE = HL)
load_in_head_call:
                ld      hl, &8000
; The preceding RST 8 HGTHD ran ROM PTDOS, which does EI — re-enabling
; interrupts.  The trampoline's documented entry contract requires DI
; (trampoline.asm:589-593): an interrupt during its HMPR-set window could
; see a deranged section C/D map.  Re-assert DI before the call.
                di
                call    TRAMPOLINE_DST      ; head read: 512 bytes into page 7

; -- Read editor_region_offset from file offset 8 in the just-loaded head --
; The trampoline restored HMPR on return, so page 7 is not mapped anywhere;
; bracket LMPR (port 250) to map page 7 into section A and read
; &0008..&000A.  Interrupts are already DI throughout the assembler; the
; stack lives in section D (independent of LMPR change).
                in      a, (250)            ; save current LMPR
                ld      (in_lmpr_save), a
                ld      a, LMPR_IN_BASE     ; map IN page 7 into section A
                out     (250), a
                ld      a, (&0008)          ; b0 = editor_region_offset bits 0..7
                ld      e, a
                ld      a, (&0009)          ; b1 = bits 8..15
                ld      d, a
                ld      a, (&000A)          ; b2 = bits 16..23 (0..5 for ≤96 KB)
                ld      c, a
                ld      a, (in_lmpr_save)   ; restore LMPR
                out     (250), a

; Decode: page_index = (b2<<2)|(b1>>6); remainder = ((b1 & &3F)<<8)|b0.
; Mirrors reader.asm::reader_init's identical decode (~lines 119-140).
                ld      a, d                ; b1
                rlca
                rlca
                and     3                   ; A = b1 >> 6
                ld      b, a
                ld      a, c                ; b2
                rlca
                rlca                        ; A = b2 << 2
                add     a, b                ; A = page_index (0..5)
                ld      (in_file_pages), a  ; prefix page count
                ld      a, d                ; b1
                and     &3F
                ld      h, a
                ld      l, e                ; HL = ((b1 & &3F)<<8)|b0 = remainder

; Normalise to SAMDOS pages/remainder convention: if remainder == 0 and
; pages > 0, adjust to pages-1 / 16384 (page-aligned prefix).  HGTHD
; emits this form for page-aligned file lengths (samdos-file-io.md).
                ld      a, h
                or      l
                jr      nz, load_in_prefix_normed   ; remainder != 0 — already normal
                ld      a, (in_file_pages)
                or      a
                jr      z, load_in_prefix_normed    ; pages == 0 — zero-length ok
                dec     a
                ld      (in_file_pages), a  ; pages - 1
                ld      hl, 16384           ; remainder = 16384
load_in_prefix_normed:
                ld      (in_file_len), hl   ; prefix length-mod-16K

; -- Phase 2: prefix load -------------------------------------------------
; Bounds check: the assembler-facing prefix must fit in the 6-page (96 KB)
; IN buffer.  The full file may exceed 6 pages; only the prefix counts here.
                ld      a, (in_file_pages)
                cp      7                   ; allow up to 6 pages (= 96 KB ceiling)
                jr      c, load_in_prefix_pages_ok
                ld      a, &03
                jp      fail_with_tag       ; tag 03: prefix pages > 6
load_in_prefix_pages_ok:
; Re-issue fill_uifa + HGTHD: HLOAD consumed the sector-chain state
; HGTHD armed for the head read, so HGTHD must be re-issued before the
; prefix HLOAD.
                ld      hl, name_IN
                call    fill_uifa
                rst     8
                defb    HOOK_HGTHD          ; longjmps on file-not-found

; Call the section-B trampoline with the prefix geometry.
;   HL = &8000      (section-C window; HLOAD's required start address)
;   B  = IN_BASE_PAGE
;   C  = prefix pages count
;   DE = prefix length-mod-16K
;   IX = UIFA (already set by fill_uifa)
                ld      hl, &8000
                ld      b, IN_BASE_PAGE
                ld      a, (in_file_pages)
                ld      c, a
                ld      de, (in_file_len)
; As at the head read: the RST 8 HGTHD above re-enabled interrupts (PTDOS
; EI), so re-assert DI to honour the trampoline's DI-entry contract
; (trampoline.asm:589-593) before calling.
                di
                call    TRAMPOLINE_DST

; Compute (IN_END_PAGE, IN_END_OFFSET) from the prefix geometry.
; The `or &20` sets the RAM0 bit so IN_END_PAGE stores a full LMPR value,
; matching the IN_POS_PAGE convention.  reader_init later overwrites IN_END
; with the boundary it derives from the loaded header — the same boundary,
; though at a page-aligned offset the two representations differ (page N
; offset 0 here vs page N-1 offset &4000 after the SAMDOS pages/remainder
; normalisation above); nothing reads IN_END before reader_init runs.
                ld      a, (in_file_pages)
                add     a, IN_BASE_PAGE
                or      &20
                ld      (IN_END_PAGE), a
                ld      hl, (in_file_len)
                ld      (IN_END_OFFSET), hl
                ret


; Scratch byte for LMPR save during load_in_file's LMPR bracket.
in_lmpr_save:   defb    0


; -----------------------------------------------------------------------
; save_out_file — HSAVE the paged OUT buffer as file "OUT".
;
; Per docs/specs/samdos-file-io.md.  HSAVE manages its
; own HMPR (saves at entry, restores at exit) and auto-pages across
; &C000 inside its save loop (`samdos/src/c.s:354-369 ctas`).  So the
; caller only has to populate UIFA[31..36]:
;
;   byte 31    : start page → HSAVE sets HMPR low 5 bits from this
;                (= OUT_BASE_PAGE = 5; page 5 then page 6 via auto-page).
;   bytes 32-33: source offset in section-C form (= &8000).
;   byte 34    : pages count = OUT_LEN >> 14.
;   bytes 35-36: remainder length = OUT_LEN & 0x3FFF.
;
; LMPR is unchanged across HSAVE (ROM PTDOS save/restores).  After
; this call: OUT is on disk; assembler-side LMPR/HMPR back to
; pre-call values; IX clobbered to dchan (we don't read it).
; -----------------------------------------------------------------------
save_out_file:
                ld      hl, name_OUT
                call    fill_uifa

                ld      a, OUT_BASE_PAGE
                ld      (UIFA + 31), a              ; HSAVE: HMPR low5 = page

                ld      hl, &8000                   ; section-C source offset
                ld      (UIFA + 32), hl             ; HSAVE: HL = (hd0d1) = UIFA[32-33]

                ld      hl, (OUT_LEN)               ; total bytes (0..32767)
                ld      a, h
                rlca
                rlca
                and     3
                ld      (UIFA + 34), a              ; pages = OUT_LEN >> 14

                ld      a, h
                and     &3f
                ld      h, a
                ld      (UIFA + 35), hl             ; remainder = OUT_LEN & 0x3FFF

                rst     8
                defb    HOOK_HSAVE
                ret


; -----------------------------------------------------------------------
; UIFA name blocks for the IN and OUT files.
; -----------------------------------------------------------------------
name_IN:        defb    19                  ; type 19 = code
                defm    "IN        "
                defm    "    "

name_OUT:       defb    19
                defm    "OUT       "
                defm    "    "


; -----------------------------------------------------------------------
; Scratch globals.
; -----------------------------------------------------------------------
main_payload_ptr:               defw    0
main_payload_len:               defw    0
main_mnemonic_id:               defw    0
main_op_count:                  defb    0
main_op_count_remaining:        defb    0
main_opval_src:                 defw    0
main_opval_dest:                defw    0
main_dir_id:                    defb    0
main_kinds_n:                   defb    0
in_file_len:                    defw    0
in_file_pages:                  defb    0

; main_dir_payload_after_header — start of the first operand of a
; directive (i.e. payload + 2, skipping dir_id + op_count).  Captured
; on entry to main_handle_directive so compute_directive_size can walk
; STRING operands without disturbing main_opval_src (which the pass-2
; emit loops advance).
main_dir_payload_after_header:  defw    0

; compute_dir_size_acc — 16-bit accumulator used by
; walk_strings_sum_lengths.  Reset to 0 before each pass at the
; directive's size-compute step.
compute_dir_size_acc:           defw    0

; M5 PR-A scratch — .equ helper.
;
; main_dir_equ_pending_id: symbol id extracted from operand 1 (held
;                          across the operand-2 evaluation).
main_dir_equ_pending_id:        defw    0

; -----------------------------------------------------------------------
; Globals shared between reader / encoder / main_loop.
; -----------------------------------------------------------------------

; Paged IN cursor — see docs/specs/paged-in-design.md.
;
; IN lives in physical pages 7..12 (off-axis; 96 KB ceiling).  The
; 24-bit cursor is stored as a (page, offset) pair: IN_POS_PAGE holds
; the LMPR low5+RAM0 byte (i.e. a full LMPR value, &27..&2C for pages
; 7..12), IN_POS_OFFSET the section-A offset (&0000..&3FFF).
; in_normalise_hl re-normalises into [&0000, &4000) after a
; page-crossing add, bumping LMPR's low 5 bits.
;
; IN_END_PAGE / IN_END_OFFSET together point one past the last valid
; byte; they're set by load_in_file_paged from the .tbn's DIFA bytes.
IN_POS_PAGE:            defb    0           ; current LMPR low5+RAM0 for IN
IN_POS_OFFSET:          defw    0           ; current offset in that page
                                            ;   (&0000..&3FFF)
IN_END_PAGE:            defb    0           ; last byte's LMPR low5+RAM0
IN_END_OFFSET:          defw    0           ; last byte's offset in that page

; Paged OUT cursor state — see docs/specs/paged-out-design.md.
; OUT_PC walks section B (&4000..&7FFF); OUT_ZONE flips 0 → 1 at the
; first byte 16384 to switch from page 5 (under LMPR_ENCTAB) to page 6
; (under LMPR_OUT_HIGH).  OUT_LEN is the total emitted byte count
; (16-bit; cap is 32 KB given the two-page allocation).
OUT_PC:                 defw    0           ; next emit position (section B)
OUT_LEN:                defw    0           ; bytes emitted so far
OUT_ZONE:               defb    0           ; 0 = low zone (section B, page 5);
                                            ; 1 = high zone (LMPR=&25, page 6)
