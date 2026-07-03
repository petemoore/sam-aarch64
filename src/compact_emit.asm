; compact_emit.asm — the compact encoder adapter (item i48c-b8e): the
; INST arm of assemble.Compact, wiring the i204b primitives (compact_inst
; / literal_word / overlay_classify, src/insn_overlay.asm) into real
; INSN_RUN emission.
;
; Z80 port of:
;   tools/sam-aarch64/assemble/compact.go::Compact's INST arm (149-160),
;       flushInst (57-60), emitInsnRunFrames (279-309),
;       emitMode0InsnRun (311-321), emitMode1InsnRun (323-338),
;       insnElementSize (340-346), insnRunMaxPayload (266),
;       litBreak (272)
;   tools/sam-aarch64-format/writer.go::WriteInsnRun (43-73) — the
;       mode-0 / mode-1 on-wire frame layout.
;
; Each instruction record becomes one serialized INSN_RUN element,
; accumulated in CEMIT_ELEMS in the mode-1 wire layout
; (writer.go:53-67):
;
;   [base_word:4 LE][patch_count:1]{[slot:1][expr_len:1][expr...]}*
;
; A fully-literal, PC-invariant instruction is a bare word (patch_count
; 0); a symbol/PC-bearing one carries exactly one {slot, expr} patch
; (encodeInstOverlay returns a single patch, overlay.go:25/36/48).
; Storing elements in the wire layout makes a mode-1 frame a straight
; LDIR of a contiguous element range, and a mode-0 frame a 4-of-5-byte
; copy per element (the patch_count byte is dropped).
;
; cemit_flush_inst packs the open run into frames exactly as
; emitInsnRunFrames does: maximal patch-free stretches become mode-0
; frames (4 B/instruction, split at 253 words), stretches around
; overlay elements become mode-1 frames absorbing literal gaps shorter
; than litBreak (4), split at the 1016-byte payload cap.
;
; HOSTING: like the i204b routines this adapter is ENCTAB-coupled (it
; calls compact_inst -> encode_inst), so it rides the ovl12 section-D
; payload (src/test_overlay_suite.asm) of the enc-tests boot variant and
; executes from OVERLAY_SUITE_RAM.  The b8c serializer is the intended
; production consumer: cemit_init / cemit_add_inst / cemit_flush_inst
; produce the record-stream bytes WriteFile embeds.  The FLAT-harness
; compactor walk (src/test_compact_ir.asm, i48c-b8b) cannot call this
; adapter — the flat harness has no ENCTAB — so it keeps emitting
; skeleton frames; the full-pipeline verification is the boot self-test
; in src/test_compact_adapter.asm.
;
; Fail tags continue the overlay suite's &c0-&c8 range (LAST_FAIL_PC
; carries the relevant pointer):
;   &cd  CEMIT_TAG_OVERFLOW — element/output buffer overflow, or a patch
;        expr longer than the u8 wire field (Go panics there too,
;        writer.go:56-63: a compaction bug, never a data condition).
; -----------------------------------------------------------------------

; -- Buffers (section-D free region, above the suite code) ---------------
; The suite code is LDIR'd to OVERLAY_SUITE_RAM (&F080) and must end
; below CEMIT_ELEMS — the Makefile overlay-suite rule enforces the
; resulting payload size cap.  Caps are sized for the boot fixture with
; ample slack; an overflow is a loud &cd fail, never silent truncation.
;
; A flat harness that reuses the frame packer (src/test_compact_ser.asm,
; the i48c-b8c boundary-split coverage) defines CEMIT_BUFS_EXTERNAL=1 and
; supplies its own CEMIT_ELEMS / CEMIT_ELEMS_END before including this
; file: the default 256-byte window cannot hold a >253-element mode-0 run
; (254 elements need 1270 bytes), so the boundary fixtures need a larger
; window than the paged payload has room for.
if defined(CEMIT_BUFS_EXTERNAL)==0
CEMIT_ELEMS:            equ     &FD00       ; serialized element run
CEMIT_ELEMS_END:        equ     &FE00       ; one past (256 B cap)
endif
CADAPT_OUT:             equ     &FE00       ; compact record-stream output
CADAPT_OUT_END:         equ     &FF00       ; one past (256 B cap)
CADAPT_DATA:            equ     &FF00       ; open LIT_DATA run (driver)
CADAPT_DATA_END:        equ     &FF80       ; one past (128 B cap)

CEMIT_TAG_OVERFLOW:     equ     &cd

; Frame-packing constants (compact.go).
CEMIT_MAX_PAYLOAD:      equ     1016        ; insnRunMaxPayload (compact.go:266)
CEMIT_M0_MAX_WORDS:     equ     253         ; (1016-1)/4 (compact.go:312)
CEMIT_LIT_BREAK:        equ     4           ; litBreak (compact.go:272)

; -----------------------------------------------------------------------
; cemit_init — reset the adapter: empty element run, output stream at
; [HL, DE).  Input: HL = output base, DE = output end (one past).
;
; Window contract: base <= end as u16 values — the window may not wrap
; past &FFFF, so a window reaching the top of memory is expressed with
; end = &FFFF (last usable byte &FFFE).  cemit_out_hdr's room check
; computes room as end - cursor (never cursor + need), so it is exact for
; any window honouring this contract; a violated contract fails loudly on
; the first emission rather than wrapping mod 65536 into a false fit.
; Clobbers: HL.
; -----------------------------------------------------------------------
cemit_init:
                ld      (cemit_out_ptr), hl
                ld      (cemit_out_end), de
                ld      hl, CEMIT_ELEMS
                ld      (cemit_elem_ptr), hl
                ld      hl, 0
                ld      (cemit_elem_count), hl
                ret

; -----------------------------------------------------------------------
; cemit_add_inst — the INST arm (compact.go:149-160): run compact_inst on
; one instruction record and append the resulting element to the open run.
;
; Input:  HL = operand stream ptr, A = operand count, DE = mnemonic id,
;         PASS_PC = the instruction's true PC (compact_inst preserves it).
; Output: A = 0 element appended; A = 1 loud gap (propagated from
;         compact_inst — Go's compactInst error return, compact.go:156-158;
;         the caller decides how to fail).
; Clobbers: everything except PASS_PC.
; -----------------------------------------------------------------------
cemit_add_inst:
                call    compact_inst            ; sets ovl_base/ovl_is_literal/OVL_*
                or      a
                ret     nz                      ; loud gap -> propagate
                ld      hl, (cemit_elem_ptr)
                ld      a, (ovl_is_literal)
                or      a
                jr      z, cemit_ai_overlay
; -- bare literal element: [base:4][patch_count=0] (5 bytes) -------------
                ld      de, 5
                call    cemit_elems_room
                call    cemit_copy_base         ; ovl_base -> (HL), HL += 4
                ld      (hl), 0                 ; patch_count = 0
                inc     hl
                jr      cemit_ai_done
cemit_ai_overlay:
; -- overlay element: [base:4][1][slot][expr_len][expr] ------------------
; The wire expr_len is u8 (writer.go:61-63 panics past 255) — a longer
; expr is a compaction bug, so fail loudly.
                ld      a, (OVL_EXPR_LEN + 1)
                or      a
                jp      nz, cemit_fail_elems
                ld      de, (OVL_EXPR_LEN)
                ld      hl, 7
                add     hl, de
                ex      de, hl                  ; DE = 7 + expr_len
                ld      hl, (cemit_elem_ptr)
                call    cemit_elems_room
                call    cemit_copy_base         ; ovl_base -> (HL), HL += 4
                ld      (hl), 1                 ; patch_count = 1
                inc     hl
                ld      a, (OVL_SLOT)
                ld      (hl), a                 ; slot
                inc     hl
                ld      a, (OVL_EXPR_LEN)
                ld      (hl), a                 ; expr_len (u8)
                inc     hl
                or      a
                jr      z, cemit_ai_done        ; empty expr: nothing to copy
; Copy the expr bytes.  OVL_EXPR_PTR points at the operand body's
; [len_lo][len_hi][expr...]; the patch carries only the expr bytes.
                ex      de, hl                  ; DE = dest
                ld      hl, (OVL_EXPR_PTR)
                inc     hl
                inc     hl                      ; -> expr bytes
                ld      bc, (OVL_EXPR_LEN)
                ldir
                ex      de, hl                  ; HL = dest after expr
cemit_ai_done:
                ld      (cemit_elem_ptr), hl
                ld      hl, (cemit_elem_count)
                inc     hl
                ld      (cemit_elem_count), hl
                xor     a                       ; success
                ret

; cemit_elems_room — element-buffer capacity guard: DE (bytes needed) must
; fit between HL (append ptr) and CEMIT_ELEMS_END.  Wrap-safe: room is
; computed as END - ptr (never ptr + need), so an element buffer placed
; near top-of-memory (CEMIT_BUFS_EXTERNAL layouts) cannot wrap mod 65536
; into a false fit.  Preserves HL, DE.
cemit_elems_room:
                push    hl
                push    de
                ld      b, h
                ld      c, l
                ld      hl, CEMIT_ELEMS_END
                or      a
                sbc     hl, bc                  ; HL = room = END - ptr
                jr      c, cer_fail             ; ptr past END: state corrupt
                sbc     hl, de                  ; room - need (CF clear from above)
                jr      c, cer_fail
                pop     de
                pop     hl
                ret
cer_fail:
                ; fail_with_tag never returns; the two pushes stay parked.
                jp      cemit_fail_elems
cemit_fail_elems:
                ld      hl, (cemit_elem_ptr)
                ld      (LAST_FAIL_PC), hl
                ld      a, CEMIT_TAG_OVERFLOW
                jp      fail_with_tag

; cemit_copy_base — copy the 4-byte ovl_base to (HL), advancing HL.
; Clobbers: A, B, DE.
cemit_copy_base:
                ld      de, ovl_base
                ld      b, 4
cemit_cb_loop:
                ld      a, (de)
                ld      (hl), a
                inc     de
                inc     hl
                djnz    cemit_cb_loop
                ret

; -----------------------------------------------------------------------
; cemit_flush_inst — flushInst (compact.go:57-60): pack the open element
; run into INSN_RUN frames via the emitInsnRunFrames walk
; (compact.go:279-309), writing records at (cemit_out_ptr), then reset
; the run.  No-op on an empty run.
;
; The walk tracks three (ptr, index) element cursors i/j/k exactly as the
; Go loop does; elements are variable-size, so the pointer advances by
; cemit_elem_size while the index drives the count comparisons.
; Clobbers: everything.
; -----------------------------------------------------------------------
cemit_flush_inst:
                ld      hl, (cemit_elem_count)
                ld      (cf_count), hl
                ld      a, h
                or      l
                ret     z                       ; empty run: nothing to pack
                ld      hl, CEMIT_ELEMS
                ld      (cf_i_ptr), hl
                ld      hl, 0
                ld      (cf_i_idx), hl
cfi_outer:
                ld      hl, (cf_i_idx)
                ld      de, (cf_count)
                or      a
                sbc     hl, de
                jp      z, cfi_done             ; i == count -> all packed
                ld      hl, (cf_i_ptr)
                call    cemit_elem_patchfree
                jr      nz, cfi_ovl_start

; -- patch-free stretch (compact.go:283-289): j := i; advance j while
; -- patch-free; emit mode-0 over [i, j); i := j.
                ld      hl, (cf_i_ptr)
                ld      (cf_j_ptr), hl
                ld      hl, (cf_i_idx)
                ld      (cf_j_idx), hl
cfi_m0_scan:
                call    cf_j_at_end
                jr      z, cfi_m0_emit
                ld      hl, (cf_j_ptr)
                call    cemit_elem_patchfree
                jr      nz, cfi_m0_emit
                call    cf_j_advance
                jr      cfi_m0_scan
cfi_m0_emit:
                call    cemit_emit_mode0
                call    cf_i_take_j
                jp      cfi_outer

; -- overlay stretch (compact.go:291-307): j := i+1; absorb overlay
; -- elements and literal gaps shorter than litBreak; a gap of >= litBreak
; -- or a gap reaching the end closes the frame at the gap start.
cfi_ovl_start:
                ld      hl, (cf_i_ptr)
                ld      (cf_j_ptr), hl
                ld      hl, (cf_i_idx)
                ld      (cf_j_idx), hl
                call    cf_j_advance            ; j = i + 1
cfi_m1_scan:
                call    cf_j_at_end
                jr      z, cfi_m1_emit          ; j == count: frame = [i, count)
                ld      hl, (cf_j_ptr)
                call    cemit_elem_patchfree
                jr      z, cfi_m1_gap
                call    cf_j_advance            ; overlay element: absorb
                jr      cfi_m1_scan
cfi_m1_gap:
; k := j; advance k while patch-free (the literal gap).
                ld      hl, (cf_j_ptr)
                ld      (cf_k_ptr), hl
                ld      hl, (cf_j_idx)
                ld      (cf_k_idx), hl
cfi_m1_kscan:
                call    cf_k_at_end
                jr      z, cfi_m1_kdone
                ld      hl, (cf_k_ptr)
                call    cemit_elem_patchfree
                jr      nz, cfi_m1_kdone
                call    cf_k_advance
                jr      cfi_m1_kscan
cfi_m1_kdone:
; Long gap (k-j >= litBreak) or tail gap (k == count): close the frame at
; j — the literals become a following mode-0 frame (compact.go:301-303).
                ld      hl, (cf_k_idx)
                ld      de, (cf_j_idx)
                or      a
                sbc     hl, de                  ; HL = k - j (gap length)
                ld      de, CEMIT_LIT_BREAK
                or      a
                sbc     hl, de
                jr      nc, cfi_m1_emit         ; gap >= litBreak
                call    cf_k_at_end
                jr      z, cfi_m1_emit          ; gap runs to the end
; Short interior gap: absorb it (j := k) and continue (compact.go:304).
                ld      hl, (cf_k_ptr)
                ld      (cf_j_ptr), hl
                ld      hl, (cf_k_idx)
                ld      (cf_j_idx), hl
                jr      cfi_m1_scan
cfi_m1_emit:
                call    cemit_emit_mode1
                call    cf_i_take_j
                jp      cfi_outer

cfi_done:
; Reset the run (Go: instRun = nil, compact.go:59).
                ld      hl, CEMIT_ELEMS
                ld      (cemit_elem_ptr), hl
                ld      hl, 0
                ld      (cemit_elem_count), hl
                ret

; -- Cursor helpers -------------------------------------------------------
; cf_j_at_end / cf_k_at_end — Z iff the cursor index == cf_count.
cf_j_at_end:
                ld      hl, (cf_j_idx)
                jr      cf_at_end_tail
cf_k_at_end:
                ld      hl, (cf_k_idx)
cf_at_end_tail:
                ld      de, (cf_count)
                or      a
                sbc     hl, de
                ret

; cf_j_advance / cf_k_advance — cursor ptr += element size, index += 1.
cf_j_advance:
                ld      hl, (cf_j_ptr)
                call    cemit_elem_size
                add     hl, de
                ld      (cf_j_ptr), hl
                ld      hl, (cf_j_idx)
                inc     hl
                ld      (cf_j_idx), hl
                ret
cf_k_advance:
                ld      hl, (cf_k_ptr)
                call    cemit_elem_size
                add     hl, de
                ld      (cf_k_ptr), hl
                ld      hl, (cf_k_idx)
                inc     hl
                ld      (cf_k_idx), hl
                ret

; cf_i_take_j — i := j.
cf_i_take_j:
                ld      hl, (cf_j_ptr)
                ld      (cf_i_ptr), hl
                ld      hl, (cf_j_idx)
                ld      (cf_i_idx), hl
                ret

; -----------------------------------------------------------------------
; cemit_elem_size — DE := on-wire size of the serialized element at HL
; (insnElementSize, compact.go:340-346): 5 + per patch (2 + expr_len).
; Preserves HL.  Clobbers A, BC.
; -----------------------------------------------------------------------
cemit_elem_size:
                push    hl
                ld      de, 5
                ld      bc, 4
                add     hl, bc
                ld      b, (hl)                 ; patch_count
                inc     hl                      ; -> first patch (slot)
                ld      a, b
                or      a
                jr      z, ces_done
ces_patch:
                inc     hl                      ; skip slot -> expr_len
                ld      a, (hl)
                inc     hl                      ; -> expr bytes
                inc     de
                inc     de                      ; size += 2 (slot + expr_len)
                push    bc
                ld      c, a
                ld      b, 0
                ex      de, hl
                add     hl, bc                  ; size += expr_len
                ex      de, hl
                add     hl, bc                  ; ptr += expr_len
                pop     bc
                djnz    ces_patch
ces_done:
                pop     hl
                ret

; -----------------------------------------------------------------------
; cemit_elem_patchfree — Z iff the element at HL has patch_count == 0.
; Preserves HL.  Clobbers A, BC.
; -----------------------------------------------------------------------
cemit_elem_patchfree:
                push    hl
                ld      bc, 4
                add     hl, bc
                ld      a, (hl)
                pop     hl
                or      a
                ret

; -----------------------------------------------------------------------
; cemit_emit_mode0 — emitMode0InsnRun (compact.go:311-321): emit the
; patch-free elements [cf_i, cf_j) as mode-0 frames of at most 253 words.
; Frame: [REC_KIND_INSN_RUN][len:2 LE][mode=0][word:4]*.  Each source
; element is 5 bytes (4-byte word + the zero patch_count, dropped here).
; Clobbers: everything.
; -----------------------------------------------------------------------
cemit_emit_mode0:
                ld      hl, (cf_j_idx)
                ld      de, (cf_i_idx)
                or      a
                sbc     hl, de
                ld      (cf_m0_n), hl           ; words remaining
                ld      hl, (cf_i_ptr)
                ld      (cf_cur), hl            ; source cursor
cem0_frame:
                ld      hl, (cf_m0_n)
                ld      a, h
                or      l
                ret     z
; m = min(remaining, 253)
                ld      de, CEMIT_M0_MAX_WORDS
                or      a
                sbc     hl, de
                jr      c, cem0_use_n
                ld      hl, CEMIT_M0_MAX_WORDS
                jr      cem0_have_m
cem0_use_n:
                ld      hl, (cf_m0_n)
cem0_have_m:
                ld      (cf_m0_m), hl
; remaining -= m
                ld      hl, (cf_m0_n)
                ld      de, (cf_m0_m)
                or      a
                sbc     hl, de
                ld      (cf_m0_n), hl
; header: payload = 1 (mode) + 4*m
                ld      hl, (cf_m0_m)
                add     hl, hl
                add     hl, hl
                inc     hl
                ld      a, REC_KIND_INSN_RUN
                call    cemit_out_hdr           ; DE -> payload dest
                xor     a
                ld      (de), a                 ; mode = 0
                inc     de
; copy m words: 4 bytes per element, dropping the patch_count byte
                ld      hl, (cf_cur)
                ld      bc, (cf_m0_m)
cem0_copy:
                push    bc
                ld      bc, 4
                ldir                            ; word bytes (HL/DE advance)
                pop     bc
                inc     hl                      ; skip patch_count (0)
                dec     bc
                ld      a, b
                or      c
                jr      nz, cem0_copy
                ld      (cf_cur), hl
                ld      (cemit_out_ptr), de
                jr      cem0_frame

; -----------------------------------------------------------------------
; cemit_emit_mode1 — emitMode1InsnRun (compact.go:323-338): emit the
; elements [cf_i, cf_j) as mode-1 frames, splitting when a frame's
; payload would exceed 1016 bytes.  Elements are already stored in the
; mode-1 wire layout and are contiguous, so a frame body is one LDIR.
; Frame: [REC_KIND_INSN_RUN][len:2 LE][mode=1][element bytes].
; Clobbers: everything.
; -----------------------------------------------------------------------
cemit_emit_mode1:
                ld      hl, (cf_i_ptr)
                ld      (cf_m1_start), hl
                ld      (cf_cur), hl
                ld      hl, 1
                ld      (cf_m1_payload), hl     ; mode byte
cem1_loop:
                ld      hl, (cf_cur)
                ld      de, (cf_j_ptr)
                or      a
                sbc     hl, de
                jr      z, cem1_final           ; cursor reached j: final frame
                ld      hl, (cf_cur)
                call    cemit_elem_size         ; DE = sz (HL preserved)
; split before this element when the frame is non-empty and payload + sz
; would exceed the cap (compact.go:328)
                ld      hl, (cf_m1_start)
                ld      bc, (cf_cur)
                or      a
                sbc     hl, bc
                jr      z, cem1_add             ; frame still empty: never split
                ld      hl, (cf_m1_payload)
                add     hl, de
                ld      bc, CEMIT_MAX_PAYLOAD
                or      a
                sbc     hl, bc
                jr      c, cem1_add             ; payload + sz <  cap: fits
                jr      z, cem1_add             ; payload + sz == cap: fits (> test)
                push    de                      ; save sz
                ld      hl, (cf_m1_start)
                ld      de, (cf_cur)
                call    cem1_emit_range         ; frame [start, cur)
                ld      hl, (cf_cur)
                ld      (cf_m1_start), hl
                ld      hl, 1
                ld      (cf_m1_payload), hl
                pop     de
cem1_add:
                ld      hl, (cf_m1_payload)
                add     hl, de
                ld      (cf_m1_payload), hl
                ld      hl, (cf_cur)
                add     hl, de
                ld      (cf_cur), hl
                jr      cem1_loop
cem1_final:
                ld      hl, (cf_m1_start)
                ld      de, (cf_j_ptr)
                ; fall through: emit the final frame [start, j) — non-empty
                ; because the caller only emits over i < j

; cem1_emit_range — write one mode-1 frame from the element bytes [HL, DE).
cem1_emit_range:
                push    hl                      ; start (LDIR source)
                ex      de, hl                  ; HL = end
                or      a
                sbc     hl, de                  ; HL = byte length
                inc     hl                      ; payload = 1 + length
                ld      a, REC_KIND_INSN_RUN
                call    cemit_out_hdr           ; DE -> payload dest; HL preserved
                ld      a, 1
                ld      (de), a                 ; mode = 1
                inc     de
                dec     hl                      ; HL = element byte length
                ld      b, h
                ld      c, l
                pop     hl                      ; HL = start
                ldir
                ld      (cemit_out_ptr), de
                ret

; -----------------------------------------------------------------------
; cemit_out_hdr — write a record header [kind][payload_len:2 LE] at the
; output cursor after checking room for the whole record (3 + payload).
; The room check is wrap-safe (PR 827 review follow-up, i48c-b8c): room
; is computed as end - cursor, never cursor + need, so a window abutting
; top-of-memory (see the cemit_init window contract) cannot wrap mod
; 65536 into a false fit.
; Input:  A = record kind, HL = payload length.
; Output: DE -> payload destination (the header is written; the caller
;         writes the payload and stores the advanced cemit_out_ptr).
;         HL preserved (still the payload length).
; Overflow: loud &cd fail (LAST_FAIL_PC = the output cursor).
; -----------------------------------------------------------------------
cemit_out_hdr:
                push    af
                push    hl
                ld      bc, 3
                add     hl, bc                  ; HL = need = 3 + payload
                jr      c, coh_fail             ; need wrapped: can never fit
                ex      de, hl                  ; DE = need
                ld      hl, (cemit_out_end)
                ld      bc, (cemit_out_ptr)
                or      a
                sbc     hl, bc                  ; HL = room = end - cursor
                jr      c, coh_fail             ; cursor past end: state corrupt
                sbc     hl, de                  ; room - need (CF clear from above)
                jr      nc, coh_ok              ; need <= room (exact fill ok)
coh_fail:
                ld      hl, (cemit_out_ptr)
                ld      (LAST_FAIL_PC), hl
                ld      a, CEMIT_TAG_OVERFLOW
                jp      fail_with_tag
coh_ok:
                pop     hl                      ; payload length
                pop     af                      ; kind
                ld      de, (cemit_out_ptr)
                ld      (de), a
                inc     de
                ld      a, l
                ld      (de), a
                inc     de
                ld      a, h
                ld      (de), a
                inc     de
                ret

; -- Adapter state (section-D RAM: part of the ovl12 payload copy) -------
cemit_elem_ptr:         defw    0               ; next free byte in CEMIT_ELEMS
cemit_elem_count:       defw    0               ; open-run element count
cemit_out_ptr:          defw    0               ; output stream write cursor
cemit_out_end:          defw    0               ; output stream end (one past)
cf_count:               defw    0               ; packer: total element count
cf_i_ptr:               defw    0               ; packer cursors (ptr + index)
cf_i_idx:               defw    0
cf_j_ptr:               defw    0
cf_j_idx:               defw    0
cf_k_ptr:               defw    0
cf_k_idx:               defw    0
cf_cur:                 defw    0               ; frame-emission source cursor
cf_m0_n:                defw    0               ; mode-0: words remaining
cf_m0_m:                defw    0               ; mode-0: words this frame
cf_m1_start:            defw    0               ; mode-1: frame start ptr
cf_m1_payload:          defw    0               ; mode-1: accumulated payload
