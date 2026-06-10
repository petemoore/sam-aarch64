; encoder.asm — output-buffer emit primitives.
;
; The compact `.tbn` v2 overlay (insn_run.asm) assembles each instruction
; by re-folding its base word and emitting the 4 bytes directly, so the
; old form-table dispatcher (encode_inst) and its per-slot handlers are
; gone.  What remains here is the OUT emit path every record handler uses:
; emit_byte (one byte, zone/LMPR-aware) and emit_bytes_n.
;
; The per-slot value encoders the folds reuse (encode_branch_imm,
; encode_imm12_shifted, encode_imm16_shifted, encode_adrp_imm/encode_adr_imm,
; encode_logical_imm) live in src/slots/*.asm.
;
; ---------------------------------------------------------------------
; Operand value record layout (OPVAL_STRIDE) — still consumed by the
; BUILD_TESTS slot self-tests and the directive/sysname paths that stage
; operands before calling a slot encoder.
;
;   offset 0   kind byte (matches format.OperandKind)
;   offset 1   reg byte (if reg-kind)
;   offset 2-9 8-byte LE evaluator result (if OpImmExpr) OR cond byte.
;              10 bytes reserved per operand so array indexing is trivial.
; ---------------------------------------------------------------------
OPVAL_STRIDE:           equ     10

; ---------------------------------------------------------------------
; emit_byte — append one byte to the output buffer at PC, advance PC.
;
; Per docs/specs/paged-out-design.md.  OUT lives in
; physical pages 5 (low zone) + 6 (high zone), reached via section B
; (&4000-&7FFF):
;
;   Low zone  (OUT_ZONE = 0; OUT bytes 0..16383):
;       LMPR is already LMPR_ENCTAB during the encoder window, so
;       section B maps to page 5 (= LMPR_ENCTAB + 1) for free.  Write
;       lands directly with no LMPR change.
;   High zone (OUT_ZONE = 1; OUT bytes 16384..32767):
;       Bracket the write with `in a,(250)` to snapshot the current
;       LMPR (= LMPR_ENCTAB at the call site), `out (250), LMPR_OUT_HIGH`
;       to put page 6 in section B, write, then restore the snapshot.
;       Reading LMPR live (rather than hard-coding LMPR_ENCTAB on the
;       restore) keeps us correct against the boot-time top-bits in
;       LMPR_DEFAULT_RUNTIME (see assembler.asm:142, trampoline.asm).
;       (Port 250 = LMPR, port 251 = HMPR per SAM Coupé Tech Manual
;       §6.10 and the existing trampoline / enctab_map_in usage.)
;
; OUT_PC walks &4000..&7FFF; when it hits &8000 we flip OUT_ZONE to 1
; and wrap OUT_PC back to &4000.  A second wrap (i.e. OUT_LEN reaching
; 32768) is an error — jp fail.
;
; Input:    A = byte to emit.
; Output:   byte stored; OUT_PC advanced; OUT_LEN incremented; on a
;           zone boundary, OUT_ZONE flipped and OUT_PC wrapped.
; Clobbers: A, HL.  BC, DE preserved.
; ---------------------------------------------------------------------
emit_byte:
                push    af                  ; preserve byte across LMPR/store
                push    bc                  ; high-zone path clobbers HL only,
                push    de                  ; but BC/DE must survive per ABI
                ld      hl, (OUT_PC)
                ld      a, (OUT_ZONE)
                or      a
                jr      nz, emit_byte_high

; ----- Low zone — write to section B with LMPR_ENCTAB live -----------
                pop     de
                pop     bc
                pop     af                  ; A = byte
                ld      (hl), a
                jr      emit_byte_advance

; ----- High zone — bracket the store with LMPR=LMPR_OUT_HIGH ---------
; Port 250 is LMPR (sections A+B); port 251 is HMPR (sections C+D).
; The OUT buffer is reached via section B so we touch LMPR only.
emit_byte_high:
                in      a, (250)
                ld      (emit_lmpr_save), a
                ld      a, LMPR_OUT_HIGH
                out     (250), a
                pop     de
                pop     bc
                pop     af                  ; A = byte
                ld      (hl), a             ; section B = page 6 under &25
                ld      a, (emit_lmpr_save)
                out     (250), a            ; restore LMPR_ENCTAB
                ; fall through; A is dead beyond this point

; ----- Common tail: advance OUT_PC, handle zone boundary, bump LEN ----
emit_byte_advance:
                inc     hl
                ld      a, h
                cp      &80
                jr      nz, emit_byte_no_zone_cross

; Hit &8000 — last byte of the current zone was just written.  Flip
; OUT_ZONE 0 → 1 and wrap OUT_PC back to &4000.  A second crossing
; (OUT_ZONE already 1) means OUT_LEN ≥ 32768 — past the M6 ceiling.
                ld      a, (OUT_ZONE)
                or      a
                jr      z, emit_byte_zone_room_ok
                ld      a, &b0
                jp      fail_with_tag       ; tag b0: OUT > 32 KB
emit_byte_zone_room_ok:
                ld      a, 1
                ld      (OUT_ZONE), a
                ld      hl, &4000

emit_byte_no_zone_cross:
                ld      (OUT_PC), hl

; Bump 16-bit OUT_LEN.  HL is free to clobber per the ABI.
                ld      hl, (OUT_LEN)
                inc     hl
                ld      (OUT_LEN), hl
                ret


; ---------------------------------------------------------------------
; emit_bytes_n — append A bytes from (HL) to the output buffer.
;
; Input:    HL = source, A = number of bytes.
; Output:   HL advanced past the source; OUT_PC / OUT_LEN bumped.
; Clobbers: A, BC, DE, HL.
;
; The pre-M6 implementation open-coded the inner loop as `LD (DE), A`
; using DE = OUT_PC.  That bypassed the paged-emit machinery (the
; section-B / LMPR-bracket dance per emit_byte) and won't work once
; OUT lives off-axis.  We just loop over emit_byte instead; the call
; sites (mainly OpString) aren't on the hot path.
; ---------------------------------------------------------------------
emit_bytes_n:
                or      a
                ret     z
                ld      b, a
emit_bytes_n_loop:
                ld      a, (hl)
                push    hl
                push    bc
                call    emit_byte
                pop     bc
                pop     hl
                inc     hl
                djnz    emit_bytes_n_loop
                ret


; ---------------------------------------------------------------------
; Scratch / state.
; ---------------------------------------------------------------------
; LMPR snapshot used by emit_byte's high-zone bracket.  Live LMPR at
; the call site (= LMPR_ENCTAB during the encoder window) is captured
; here so the restore goes back to the *exact* boot-derived value
; rather than a hard-coded constant.
emit_lmpr_save:         defb    0
