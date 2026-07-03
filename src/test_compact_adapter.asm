; test_compact_adapter.asm — boot self-test for the compact encoder
; adapter (src/compact_emit.asm, item i48c-b8e): the full Z80 compact
; pipeline over a mixed record stream, byte-compared against the Go
; authority assemble.Compact.
;
; The fixture input is a serialized IR record stream (the parse_run wire
; layout serializeIR mirrors) baked into the enc_fix payload
; (src/test_encode_inst_payload.asm, cadapt_ir), alongside the expected
; compact record-stream bytes (cadapt_expected) — both derived by and
; drift-guarded against tools/netboot-oracle/z80/
; compact_adapter_fixture_test.go::TestCompactAdapterFixture (the Go
; authority per CLAUDE.md §6: it re-derives both byte arrays from
; serializeIR + assemble.Compact and fails on any divergence from the
; baked copies, forcing a re-bake here in the same change).
;
; The walk mirrors Compact's record dispatch (compact.go:85-186) for the
; record kinds the fixture carries:
;   INST       -> flushData; PASS_PC := running PC; cemit_add_inst
;                 (compact.go:149-160); PC += 4
;   COMMENT    -> record stream untouched, spans the open runs
;                 (compact.go:103-122; the sidecar row it also produces
;                 is the b8b flat-walk's output, out of scope here —
;                 this suite compares recordsOut only)
;   DIRECTIVE  -> .org: flushAll + verbatim pass-through
;                 (compact.go:175-184) + PC := eval(op0) (mirroring
;                 pass1.go:270-296's origin/PC effect);
;                 .byte/.short/.hword/.word/.quad: flushInst, coalesce
;                 into the open LIT_DATA run (compact.go:162-174)
; Any other record kind / directive is out of fixture scope -> loud &cc.
;
; The running PC mirrors pass1's accounting for these kinds (INST +4,
; const-data +width*count, COMMENT +0), so PASS_PC seen by compact_inst
; equals p1.InstPC[i] (compact.go:82-84).  Fixture PCs stay below &10000,
; so the 16-bit counter with zeroed PASS_PC high bytes is exact.
;
; The data-run arm is the same shape as the b8b flat port
; (test_compact_ir.asm compact_flush_data), narrowed to the fixture
; scope: operands are known-constant IMM_EXPRs (eval_expr_const jp-fails
; loudly on anything else), and the whole-element split boundary is the
; constant LIT_DATA_MAX itself because litDataMaxBytes is divisible by
; every legal width (compact.go:23-27).
;
; Runs in the enc-tests variant (BUILD_TESTS_ENCODE) from section-D RAM,
; second suite in the ovl12 payload (after the overlay-classify suite),
; inside its own enctab_map_in / form_lookup_init / enctab_map_out
; bracket.
;
; Fail tags (continuing the overlay suite's &c0-&c8; &cd is the
; compact_emit overflow tag):
;   &c9  cemit_add_inst reported a loud gap    (LAST_FAIL_PC = record ptr)
;   &ca  output length mismatch                (LAST_FAIL_PC = out cursor)
;   &cb  output byte mismatch                  (LAST_FAIL_PC = the failing
;        EXPECTED byte's address — the fail probe keys on this)
;   &cc  unexpected record kind / directive    (LAST_FAIL_PC = record ptr)
; -----------------------------------------------------------------------

CADAPT_TAG_STATUS:      equ     &c9
CADAPT_TAG_LEN:         equ     &ca
CADAPT_TAG_BYTES:       equ     &cb
CADAPT_TAG_KIND:        equ     &cc

CADAPT_LITDATA_MAX:     equ     1016        ; litDataMaxBytes (compact.go:27)

run_compact_adapter_self_tests:
                call    enctab_map_in
                call    form_lookup_init

; Reset the adapter (output stream = [CADAPT_OUT, CADAPT_OUT_END)) and
; the driver's data-run + PC state.
                ld      hl, CADAPT_OUT
                ld      de, CADAPT_OUT_END
                call    cemit_init
                xor     a
                ld      (cadapt_data_width), a
                ld      hl, CADAPT_DATA
                ld      (cadapt_data_ptr), hl
                ld      hl, 0
                ld      (cadapt_pc), hl

; Walk the serialized IR fixture.
                ld      hl, cadapt_ir
                ld      (cadapt_cursor), hl
                ld      hl, (cadapt_ir_len)
                ld      (cadapt_remaining), hl
cadapt_loop:
                ld      hl, (cadapt_remaining)
                ld      a, h
                or      l
                jp      z, cadapt_finish
                ld      hl, (cadapt_cursor)
                ld      a, (hl)
                cp      REC_KIND_INST
                jp      z, cadapt_inst
                cp      REC_KIND_COMMENT
                jp      z, cadapt_comment
                cp      REC_KIND_DIRECTIVE
                jp      z, cadapt_directive
                jp      cadapt_fail_kind        ; out of fixture scope

; cadapt_advance — cursor += BC (the framed record size), loop.
cadapt_advance:
                ld      hl, (cadapt_cursor)
                add     hl, bc
                ld      (cadapt_cursor), hl
                ld      hl, (cadapt_remaining)
                or      a
                sbc     hl, bc
                ld      (cadapt_remaining), hl
                jp      cadapt_loop

; -----------------------------------------------------------------------
; INST (compact.go:149-160): flushData, seed PASS_PC, add the element.
; Wire: [kind][mnem:2][op_count:1][ops_len:2][ops]; size = 6 + ops_len.
; -----------------------------------------------------------------------
cadapt_inst:
                call    cadapt_flush_data
                ld      hl, (cadapt_pc)
                ld      (PASS_PC + 0), hl
                ld      hl, 0
                ld      (PASS_PC + 2), hl
                ld      hl, (cadapt_cursor)
                inc     hl
                ld      e, (hl)
                inc     hl
                ld      d, (hl)                 ; DE = mnemonic id
                inc     hl
                ld      a, (hl)                 ; op count
                inc     hl
                ld      c, (hl)
                inc     hl
                ld      b, (hl)                 ; BC = ops_len
                inc     hl                      ; -> operand stream
                push    bc
                call    cemit_add_inst
                pop     bc
                or      a
                jr      z, cadapt_inst_ok
                ld      hl, (cadapt_cursor)
                ld      (LAST_FAIL_PC), hl
                ld      a, CADAPT_TAG_STATUS
                jp      fail_with_tag
cadapt_inst_ok:
; PC += 4: every instruction occupies 4 output bytes (compact.go:34-36).
                ld      hl, (cadapt_pc)
                inc     hl
                inc     hl
                inc     hl
                inc     hl
                ld      (cadapt_pc), hl
                ld      hl, 6
                add     hl, bc
                ld      b, h
                ld      c, l                    ; BC = 6 + ops_len
                jp      cadapt_advance

; -----------------------------------------------------------------------
; COMMENT (compact.go:103-122): no record-stream output, no flush — the
; open instruction/data run spans it.  Wire: [kind][len:2][payload].
; -----------------------------------------------------------------------
cadapt_comment:
                ld      hl, (cadapt_cursor)
                inc     hl
                ld      c, (hl)
                inc     hl
                ld      b, (hl)
                inc     bc
                inc     bc
                inc     bc                      ; BC = 3 + payload_len
                jp      cadapt_advance

; -----------------------------------------------------------------------
; DIRECTIVE: stage {dir_id, op_count, ops ptr}, then dispatch.
; Wire: [kind][len:2][dir_id:1][op_count:1][operands]; size = 3 + len.
; -----------------------------------------------------------------------
cadapt_directive:
                ld      hl, (cadapt_cursor)
                inc     hl
                ld      c, (hl)
                inc     hl
                ld      b, (hl)                 ; BC = payload len
                inc     hl                      ; -> dir_id
                push    bc
                inc     bc
                inc     bc
                inc     bc
                ld      (cadapt_dir_size), bc
                pop     bc
                ld      a, (hl)
                ld      (cadapt_dir_id), a
                inc     hl
                ld      a, (hl)
                ld      (cadapt_op_count), a
                inc     hl
                ld      (cadapt_ops), hl
                ld      a, (cadapt_dir_id)
                cp      DIR_ORG
                jr      z, cadapt_org
; Const-data width (ConstDataWidth, litinsts.go — narrowed to the fixture
; scope: operands are known-constant IMM_EXPRs, checked by the loud
; eval_expr_const rather than a pre-scan).
                cp      DIR_BYTE
                ld      b, 1
                jr      z, cadapt_const_data
                cp      DIR_SHORT
                ld      b, 2
                jr      z, cadapt_const_data
                cp      DIR_HWORD
                jr      z, cadapt_const_data
                cp      DIR_WORD
                ld      b, 4
                jr      z, cadapt_const_data
                cp      DIR_QUAD
                ld      b, 8
                jr      z, cadapt_const_data
cadapt_fail_kind:
                ld      hl, (cadapt_cursor)
                ld      (LAST_FAIL_PC), hl
                ld      a, CADAPT_TAG_KIND
                jp      fail_with_tag

; -----------------------------------------------------------------------
; .org — flushAll + verbatim pass-through (compact.go:175-184;
; WriteDirective's re-serialisation equals the IR framing byte-for-byte),
; then PC := eval(op0) (the origin effect pass1 applies, pass1.go:270-296).
; -----------------------------------------------------------------------
cadapt_org:
                call    cemit_flush_inst
                call    cadapt_flush_data
                ld      hl, (cadapt_dir_size)
                dec     hl
                dec     hl
                dec     hl                      ; payload len
                ld      a, REC_KIND_DIRECTIVE
                call    cemit_out_hdr           ; DE -> payload dest, HL = len
                ld      b, h
                ld      c, l                    ; BC = payload len
                ld      hl, (cadapt_cursor)
                inc     hl
                inc     hl
                inc     hl                      ; -> payload source
                ldir
                ld      (cemit_out_ptr), de
; PC := eval(op0).  op0 is [IMM_EXPR][len:2][bytecode] (fixture scope);
; the low word is the running PC (fixture PCs < &10000).
                ld      hl, (cadapt_ops)
                inc     hl                      ; skip the IMM_EXPR kind byte
                ld      c, (hl)
                inc     hl
                ld      b, (hl)
                inc     hl                      ; -> bytecode
                call    eval_expr_const         ; -> expr_result (8 B LE)
                ld      hl, (expr_result)
                ld      (cadapt_pc), hl
                ld      bc, (cadapt_dir_size)
                jp      cadapt_advance

; -----------------------------------------------------------------------
; Const-data directive, width in B (compact.go:162-174): flushInst; a
; different-directive open run flushes first; append each operand's
; constant value as `width` LE bytes (evalImmsAsBytes — the same shape as
; the b8b flat port's compact_emit_const_data).
; -----------------------------------------------------------------------
cadapt_const_data:
                ld      a, b
                ld      (cadapt_width), a
                call    cemit_flush_inst        ; flushInst (compact.go:166)
                ld      a, (cadapt_data_width)
                or      a
                jr      z, cadapt_data_open
                ld      a, (cadapt_data_dir)
                ld      b, a
                ld      a, (cadapt_dir_id)
                cp      b
                jr      z, cadapt_data_append
                call    cadapt_flush_data       ; different directive: new run
cadapt_data_open:
                ld      a, (cadapt_dir_id)
                ld      (cadapt_data_dir), a
                ld      a, (cadapt_width)
                ld      (cadapt_data_width), a
cadapt_data_append:
                ld      a, (cadapt_op_count)
                or      a
                jr      z, cadapt_data_pc
                ld      (cadapt_data_n), a
                ld      hl, (cadapt_ops)
                ld      (cadapt_data_src), hl
cadapt_data_loop:
                ld      hl, (cadapt_data_src)
                inc     hl                      ; skip IMM_EXPR kind byte
                ld      c, (hl)
                inc     hl
                ld      b, (hl)                 ; BC = expr len
                inc     hl                      ; -> bytecode
                push    bc
                call    eval_expr_const         ; -> expr_result
                pop     bc
                ld      hl, (cadapt_data_src)
                inc     hl
                inc     hl
                inc     hl
                add     hl, bc
                ld      (cadapt_data_src), hl   ; advance past this operand
; Append the low `width` bytes of expr_result (room-checked).
                ld      a, (cadapt_width)
                ld      b, a
                ld      hl, (cadapt_data_ptr)
                push    bc
                ld      c, b
                ld      b, 0
                push    hl
                add     hl, bc
                ld      bc, CADAPT_DATA_END + 1
                or      a
                sbc     hl, bc                  ; CF iff ptr+width <= END
                pop     hl
                pop     bc
                jr      c, cadapt_data_room_ok
                ld      (LAST_FAIL_PC), hl
                ld      a, CEMIT_TAG_OVERFLOW
                jp      fail_with_tag
cadapt_data_room_ok:
                ex      de, hl                  ; DE = dest
                ld      hl, expr_result
cadapt_data_copy:
                ld      a, (hl)
                ld      (de), a
                inc     hl
                inc     de
                djnz    cadapt_data_copy
                ld      (cadapt_data_ptr), de
                ld      a, (cadapt_data_n)
                dec     a
                ld      (cadapt_data_n), a
                jr      nz, cadapt_data_loop
cadapt_data_pc:
; PC += width * op_count (pass1's const-data size accounting).
                ld      a, (cadapt_op_count)
                or      a
                jr      z, cadapt_data_done
                ld      b, a
                ld      a, (cadapt_width)
                ld      e, a
                ld      d, 0
                ld      hl, (cadapt_pc)
cadapt_data_pcl:
                add     hl, de
                djnz    cadapt_data_pcl
                ld      (cadapt_pc), hl
cadapt_data_done:
                ld      bc, (cadapt_dir_size)
                jp      cadapt_advance

; -----------------------------------------------------------------------
; cadapt_flush_data — flushData (compact.go:61-79): emit the open run as
; LIT_DATA records, split at the whole-element boundary.  litDataMaxBytes
; (1016) is divisible by every legal width 1/2/4/8 (compact.go:23-27), so
; max = 1016/width*width = CADAPT_LITDATA_MAX for every run this driver
; can open.  Record: [REC_KIND_LIT_DATA][len:2 = 1+n][dir_id][bytes].
; -----------------------------------------------------------------------
cadapt_flush_data:
                ld      a, (cadapt_data_width)
                or      a
                ret     z                       ; no open run
                ld      hl, (cadapt_data_ptr)
                ld      de, CADAPT_DATA
                or      a
                sbc     hl, de
                ld      (cadapt_data_total), hl
                ld      hl, CADAPT_DATA
                ld      (cadapt_data_rd), hl
cadapt_fd_rec:
                ld      hl, (cadapt_data_total)
                ld      a, h
                or      l
                jr      z, cadapt_fd_done
; n = min(total, max)
                ld      de, CADAPT_LITDATA_MAX
                or      a
                sbc     hl, de
                jr      c, cadapt_fd_use_total
                ld      hl, CADAPT_LITDATA_MAX
                jr      cadapt_fd_have_n
cadapt_fd_use_total:
                ld      hl, (cadapt_data_total)
cadapt_fd_have_n:
                ld      (cadapt_data_nb), hl
                inc     hl                      ; payload = 1 (dir_id) + n
                ld      a, REC_KIND_LIT_DATA
                call    cemit_out_hdr           ; DE -> payload dest
                ld      a, (cadapt_data_dir)
                ld      (de), a
                inc     de
                ld      hl, (cadapt_data_rd)
                ld      bc, (cadapt_data_nb)
                ldir
                ld      (cemit_out_ptr), de
                ld      (cadapt_data_rd), hl
                ld      hl, (cadapt_data_total)
                ld      de, (cadapt_data_nb)
                or      a
                sbc     hl, de
                ld      (cadapt_data_total), hl
                jr      cadapt_fd_rec
cadapt_fd_done:
                xor     a
                ld      (cadapt_data_width), a
                ld      hl, CADAPT_DATA
                ld      (cadapt_data_ptr), hl
                ret

; -----------------------------------------------------------------------
; End of input: flushAll (compact.go:186), then byte-compare the produced
; record stream against the Go-authority expected bytes.
; -----------------------------------------------------------------------
cadapt_finish:
                call    cemit_flush_inst
                call    cadapt_flush_data
; Produced length must equal the baked expected length.
                ld      hl, (cemit_out_ptr)
                ld      de, CADAPT_OUT
                or      a
                sbc     hl, de
                ld      de, (cadapt_expected_len)
                or      a
                sbc     hl, de
                jr      z, cadapt_cmp
                ld      hl, (cemit_out_ptr)
                ld      (LAST_FAIL_PC), hl
                ld      a, CADAPT_TAG_LEN
                jp      fail_with_tag
cadapt_cmp:
                ld      hl, cadapt_expected
                ld      de, CADAPT_OUT
                ld      bc, (cadapt_expected_len)
cadapt_cmp_loop:
                ld      a, b
                or      c
                jr      z, cadapt_pass
                ld      a, (de)
                cp      (hl)
                jr      nz, cadapt_cmp_fail
                inc     hl
                inc     de
                dec     bc
                jr      cadapt_cmp_loop
cadapt_cmp_fail:
; LAST_FAIL_PC = the failing EXPECTED byte's address (inside the enc_fix
; payload copy) — TestBootSelfTestsCompactAdapterFailProbe corrupts
; cadapt_expected+0 and asserts exactly this address in the banner.
                ld      (LAST_FAIL_PC), hl
                ld      a, CADAPT_TAG_BYTES
                jp      fail_with_tag
cadapt_pass:
                jp      enctab_map_out          ; tail-call; RETs to the entry stub

; -- Driver state (section-D RAM: part of the ovl12 payload copy) --------
cadapt_cursor:          defw    0               ; IR walk cursor
cadapt_remaining:       defw    0               ; IR bytes remaining
cadapt_pc:              defw    0               ; running output PC (low 16)
cadapt_dir_id:          defb    0
cadapt_op_count:        defb    0
cadapt_ops:             defw    0               ; staged directive operands
cadapt_dir_size:        defw    0               ; framed directive size
cadapt_width:           defb    0               ; current directive width
cadapt_data_width:      defb    0               ; open data run width (0=none)
cadapt_data_dir:        defb    0               ; open data run directive id
cadapt_data_ptr:        defw    0               ; data run append cursor
cadapt_data_n:          defb    0               ; operand loop counter
cadapt_data_src:        defw    0               ; operand read cursor
cadapt_data_total:      defw    0               ; flush: bytes in the run
cadapt_data_rd:         defw    0               ; flush: read cursor
cadapt_data_nb:         defw    0               ; flush: bytes this record
