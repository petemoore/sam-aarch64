; litpool.asm — literal pool data structures + pass-1 / pass-2 helpers.
;
; Per https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/specs/2026-05-27-m5-compound-operands-directives-design.md §2.3
; and https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/plans/2026-05-27-m5-compound-operands-directives.md Task 13.
;
; Mac-side reference: tools/refenc/pass1.go:60-218 (pool slot allocation +
; dedup + flush) and tools/refenc/pass2.go:21-76 (flush emit) +
; tools/refenc/pass2.go:593-634 (encodeLdrLitPoolInst).
;
; -----------------------------------------------------------------------
; Data structures.
;
; LITPOOL_TABLE lives in section D scratch at &D200 (well clear of
; OPMEM_OFF at &D100).  LITPOOL_PC_MAP was moved to the &E100+ free
; region (2026-05-28) so its capacity can grow to 64 entries without
; colliding with STAGING_BUF at &D500 — release.tbn peaks at 44 active
; pc-map entries per flush (census `docs/notes/2026-05-28-z80-table-
; sizing-census.md`), which the original 32-entry cap couldn't fit.
; -----------------------------------------------------------------------
;
; LITPOOL_TABLE     entries indexed by slot.  14 bytes per entry:
;   +0   width       u8       (4 = Wn, 8 = Xn)
;   +1   expr_ptr    u16 LE   (into IN_BUF; bytecode start)
;   +3   expr_len    u16 LE
;   +5   eval_pc     u32 LE   (PC of FIRST ldr referencing this slot)
;   +9   entry_pc    u32 LE   (PC of pool-entry bytes; filled by flush)
;   +13  segment     u8       (which pool segment this slot belongs to;
;                              flush emits slots whose segment equals
;                              the current LITPOOL_SEGMENT_FLUSH counter)
;
; LITPOOL_PC_MAP    one entry per LDR-litpool instruction.  6 bytes each:
;   +0   inst_pc     u32 LE
;   +4   slot_idx    u16 LE
;
; LITPOOL_COUNT             u8       slots allocated so far (cap LITPOOL_MAX = 32)
; LITPOOL_PCM_COUNT         u8       pc-map entries so far  (cap LITPOOL_PCM_MAX = 64)
; LITPOOL_SEGMENT_ALLOC     u8       segment for newly-allocated slots
;                                    (incremented at each flush in pass 1)
; LITPOOL_SEGMENT_FLUSH     u8       segment currently being flushed
;                                    (advances in lockstep across passes)
; LITPOOL_SAVED_PC          u32 LE   scratch — saved PASS_PC across flush
;
; Segment counter rationale: `.ltorg` partitions the slot stream into
; pools.  Mac refenc keeps separate pending maps per pool segment
; (refenc/pass1.go:127 resets `pending` at each flush).  We emulate the
; same with a per-slot segment ID plus matched ALLOC/FLUSH counters:
;
;   * Allocate: slot.segment = LITPOOL_SEGMENT_ALLOC (current).
;   * Pass-1 dedup: only match slots whose segment == current ALLOC.
;     A repeat (value,width) after a flush therefore creates a new slot
;     in the next segment, matching GNU.
;   * Pass-1 / pass-2 flush: emit slots with segment == FLUSH counter,
;     then FLUSH++.  ALLOC is bumped from FLUSH in pass 1 too (so the
;     next allocation lands in the next segment); pass 2 doesn't
;     allocate so ALLOC is irrelevant after pass 1.
;
; Capacities (post-2026-05-28 census against release.tbn):
;   LITPOOL_MAX     = 32  slots (peak 30; 2 slack — keep an eye on this)
;   LITPOOL_PCM_MAX = 64  pc-map entries (peak 44; 20 slack)
; -----------------------------------------------------------------------

LITPOOL_MAX:            equ     32
LITPOOL_PCM_MAX:        equ     64
LITPOOL_ENTRY_STRIDE:   equ     14
LITPOOL_PCM_STRIDE:     equ     6

LITPOOL_TABLE:          equ     &D200          ; 32 * 14 = 448 B
LITPOOL_PC_MAP:         equ     &E100          ; 64 * 6  = 384 B (moved to &E100+ region 2026-05-28)
LITPOOL_COUNT:          equ     &D3C0          ; 1 byte
LITPOOL_PCM_COUNT:      equ     &D3C1          ; 1 byte
LITPOOL_SEGMENT_ALLOC:  equ     &D3C2          ; 1 byte
LITPOOL_SEGMENT_FLUSH:  equ     &D3C3          ; 1 byte
LITPOOL_SAVED_PC:       equ     &D3C4          ; 4 bytes


; -----------------------------------------------------------------------
; litpool_init — reset the pool table + pc-map.  Called once per pass.
;
; Both passes call this.  Pass 1 then populates the tables; pass 2 reads
; them (and re-populates them identically — the work is idempotent given
; the same record stream, and re-emitting in pass 2 keeps PC accounting
; correct without having to thread the pass-1 table state across).
;
; Input:  none.
; Output: LITPOOL_COUNT = 0, LITPOOL_PCM_COUNT = 0.
; Clobbers: A.
; -----------------------------------------------------------------------
litpool_init:
                xor     a
                ld      (LITPOOL_COUNT), a
                ld      (LITPOOL_PCM_COUNT), a
                ld      (LITPOOL_SEGMENT_ALLOC), a
                ld      (LITPOOL_SEGMENT_FLUSH), a

; Reset the section-D expr pool's bump-allocator top to the pool base
; so pass 1 registers from a fresh start.  Pass 2 must NOT call
; litpool_init (which would reset the pointer mid-flush and would
; clobber the still-active pass-1 copies); see litpool_reset_pending.
                ld      hl, LITPOOL_EXPR_BUF
                ld      (litpool_expr_buf_top), hl
                ret


; -----------------------------------------------------------------------
; litpool_reset_pending — preserve LITPOOL_TABLE / LITPOOL_PC_MAP from
; pass 1 and reset the FLUSH counter to 0 so pass 2's flushes walk the
; same segments as pass 1 in lockstep.  The per-slot segment IDs are
; already correct from pass 1.
;
; Input:  none.
; Output: LITPOOL_SEGMENT_FLUSH = 0.  ALLOC counter is left alone (pass
;         2 doesn't allocate).
; Clobbers: A.
; -----------------------------------------------------------------------
litpool_reset_pending:
                xor     a
                ld      (LITPOOL_SEGMENT_FLUSH), a
                ret


; -----------------------------------------------------------------------
; slot_ptr_for_idx — given an unsigned slot index in A, set HL to the
; address of LITPOOL_TABLE[A].  Clobbers A, DE.  Preserves BC.
; -----------------------------------------------------------------------
slot_ptr_for_idx:
                ld      hl, LITPOOL_TABLE
                or      a
                ret     z
slot_ptr_for_idx_loop:
                ld      de, LITPOOL_ENTRY_STRIDE
                add     hl, de
                dec     a
                jr      nz, slot_ptr_for_idx_loop
                ret


; pcm_ptr_for_idx — same for LITPOOL_PC_MAP[A].
pcm_ptr_for_idx:
                ld      hl, LITPOOL_PC_MAP
                or      a
                ret     z
pcm_ptr_for_idx_loop:
                ld      de, LITPOOL_PCM_STRIDE
                add     hl, de
                dec     a
                jr      nz, pcm_ptr_for_idx_loop
                ret


; -----------------------------------------------------------------------
; litpool_register — pass-1 helper.  Look up (width, expr-bytes) in the
; table; on miss append a new slot.  Always record a pc-map entry
; mapping the current PASS_PC to the slot index.
;
; Input:  A  = width (4 or 8)
;         HL = pointer to expr bytecode (in IN_BUF)
;         BC = expr bytecode length (u16)
; Output: none observable; modifies LITPOOL_TABLE, LITPOOL_PC_MAP.
; Clobbers: A, BC, DE, HL.
;
; On capacity overflow: jp fail.
; -----------------------------------------------------------------------
litpool_register:
                ld      (litpool_reg_width), a
                ld      (litpool_reg_expr_ptr), hl
                ld      (litpool_reg_expr_len), bc

; -- Scan existing slots for a match -----------------------------------
                ld      a, (LITPOOL_COUNT)
                or      a
                jr      z, litpool_register_append
                ld      (litpool_reg_total), a
                xor     a
                ld      (litpool_reg_idx), a

litpool_register_scan_loop:
                ld      a, (litpool_reg_idx)
                call    slot_ptr_for_idx
                ld      (litpool_reg_slot_ptr), hl
; Skip slots from a previous pool segment — dedup is per-pool-segment,
; matching Mac's `pending = make(map[poolKey]int)` reset at every
; .ltorg flush (refenc/pass1.go:127).  A second `ldr x0, =0xaaaa`
; after a .ltorg must allocate a new slot, not reuse the flushed one.
                push    hl
                ld      bc, 13
                add     hl, bc
                ld      a, (hl)                       ; slot.segment
                pop     hl
                ld      e, a
                ld      a, (LITPOOL_SEGMENT_ALLOC)
                cp      e
                jr      nz, litpool_register_scan_next
; Compare width.
                ld      a, (hl)
                ld      e, a
                ld      a, (litpool_reg_width)
                cp      e
                jr      nz, litpool_register_scan_next
; Compare expr_len.
                ld      hl, (litpool_reg_slot_ptr)
                inc     hl
                inc     hl
                inc     hl                            ; HL → +3 expr_len LSB
                ld      c, (hl)
                inc     hl
                ld      b, (hl)                       ; BC = stored len
                ld      hl, (litpool_reg_expr_len)
                ld      a, h
                cp      b
                jr      nz, litpool_register_scan_next
                ld      a, l
                cp      c
                jr      nz, litpool_register_scan_next
; Lengths match — byte compare.
                ld      hl, (litpool_reg_slot_ptr)
                inc     hl                            ; HL → +1 expr_ptr LSB
                ld      e, (hl)
                inc     hl
                ld      d, (hl)                       ; DE = stored expr_ptr
                ld      hl, (litpool_reg_expr_ptr)
                ld      bc, (litpool_reg_expr_len)
                ld      a, b
                or      c
                jr      z, litpool_register_hit       ; zero-len → match
litpool_register_cmp:
                ld      a, (de)
                cp      (hl)
                jr      nz, litpool_register_scan_next
                inc     de
                inc     hl
                dec     bc
                ld      a, b
                or      c
                jr      nz, litpool_register_cmp

litpool_register_hit:
                ld      a, (litpool_reg_idx)
                jp      litpool_register_record_pcmap

litpool_register_scan_next:
                ld      a, (litpool_reg_idx)
                inc     a
                ld      (litpool_reg_idx), a
                ld      e, a
                ld      a, (litpool_reg_total)
                cp      e
                jr      nz, litpool_register_scan_loop
; Fall through to append.

litpool_register_append:
                ld      a, (LITPOOL_COUNT)
                cp      LITPOOL_MAX
                jr      c, litpool_count_ok
                ld      a, &10
                jp      fail_with_tag       ; tag 10: LITPOOL_TABLE overflow
litpool_count_ok:
                ld      (litpool_reg_idx), a

; -- Copy expr bytecode out of the staging buffer into the section-D
; cross-pass pool BEFORE storing the slot.  M6 PR 2: the IN reader's
; staging buffer is overwritten on every reader_next_kind call, so the
; pointer captured in pass 1 is invalid by the time pass-2's flush
; reads it.  litpool_copy_expr copies BC bytes from HL (staging-buf)
; to LITPOOL_EXPR_BUF and returns HL = section-D copy address.  We
; overwrite litpool_reg_expr_ptr with the new (page-stable) pointer
; before the slot store below.
                ld      hl, (litpool_reg_expr_ptr)    ; HL = staging-buf src
                ld      bc, (litpool_reg_expr_len)    ; BC = expr_len
                call    litpool_copy_expr             ; HL = section-D dest
                ld      (litpool_reg_expr_ptr), hl

                ld      a, (litpool_reg_idx)
                call    slot_ptr_for_idx              ; HL → fresh slot base

; +0 width
                ld      a, (litpool_reg_width)
                ld      (hl), a
                inc     hl
; +1..+2 expr_ptr (now points into LITPOOL_EXPR_BUF, page-stable)
                ld      a, (litpool_reg_expr_ptr + 0)
                ld      (hl), a
                inc     hl
                ld      a, (litpool_reg_expr_ptr + 1)
                ld      (hl), a
                inc     hl
; +3..+4 expr_len
                ld      a, (litpool_reg_expr_len + 0)
                ld      (hl), a
                inc     hl
                ld      a, (litpool_reg_expr_len + 1)
                ld      (hl), a
                inc     hl
; +5..+8 eval_pc := PASS_PC
                ld      a, (PASS_PC + 0)
                ld      (hl), a
                inc     hl
                ld      a, (PASS_PC + 1)
                ld      (hl), a
                inc     hl
                ld      a, (PASS_PC + 2)
                ld      (hl), a
                inc     hl
                ld      a, (PASS_PC + 3)
                ld      (hl), a
                inc     hl
; +9..+12 entry_pc := 0
                xor     a
                ld      (hl), a
                inc     hl
                ld      (hl), a
                inc     hl
                ld      (hl), a
                inc     hl
                ld      (hl), a
                inc     hl
; +13 segment := LITPOOL_SEGMENT_ALLOC
                ld      a, (LITPOOL_SEGMENT_ALLOC)
                ld      (hl), a

; Bump LITPOOL_COUNT and pick up new idx for pc-map.
                ld      a, (LITPOOL_COUNT)
                inc     a
                ld      (LITPOOL_COUNT), a
                ld      a, (litpool_reg_idx)          ; new slot index

litpool_register_record_pcmap:
                ld      (litpool_reg_idx), a          ; save slot idx
                ld      a, (LITPOOL_PCM_COUNT)
                cp      LITPOOL_PCM_MAX
                jr      c, litpool_pcm_ok
                ld      a, &11
                jp      fail_with_tag       ; tag 11: LITPOOL_PC_MAP overflow
litpool_pcm_ok:
                call    pcm_ptr_for_idx               ; HL → fresh pcm entry

                ld      a, (PASS_PC + 0)
                ld      (hl), a
                inc     hl
                ld      a, (PASS_PC + 1)
                ld      (hl), a
                inc     hl
                ld      a, (PASS_PC + 2)
                ld      (hl), a
                inc     hl
                ld      a, (PASS_PC + 3)
                ld      (hl), a
                inc     hl
                ld      a, (litpool_reg_idx)
                ld      (hl), a                       ; slot_idx LSB
                inc     hl
                ld      (hl), 0                       ; slot_idx MSB
                ld      a, (LITPOOL_PCM_COUNT)
                inc     a
                ld      (LITPOOL_PCM_COUNT), a
                ret


; -----------------------------------------------------------------------
; litpool_copy_expr — copy BC bytes of expr bytecode from HL (staging
; buffer) to the section-D cross-pass pool at LITPOOL_EXPR_BUF.
;
; Per docs/specs/paged-in-design.md §"Litpool copy hook".
; Under paged IN the staging buffer is overwritten on every
; reader_next_kind call, so a section-C-style expr_ptr captured in
; pass 1 is invalid by the time pass-2's flush reads it.  This helper
; copies the bytecode out at registration time into a dedicated pool
; that lives until end-of-assemble.
;
; Bound-check: post-copy top (DE + BC) must be <= LITPOOL_EXPR_BUF_END
; or we'd run off into the section-D scratch.  Overflow → jp fail.
;
; Input:  HL = src (staging-buf addr), BC = expr_len.
; Output: HL = dest (section-D copy address).  litpool_expr_buf_top
;         advanced by BC.
; Clobbers: A, BC (consumed by LDIR), DE.
; -----------------------------------------------------------------------
litpool_copy_expr:
                ld      de, (litpool_expr_buf_top)    ; next free byte

; Bound: post-copy top = DE + BC.  If > LITPOOL_EXPR_BUF_END → fail.
                push    hl                            ; preserve src
                push    bc                            ; preserve len for LDIR
                ld      h, d
                ld      l, e
                add     hl, bc                        ; HL = DE + BC = post-copy top
                ld      bc, LITPOOL_EXPR_BUF_END
                or      a                             ; clear CF
                sbc     hl, bc                        ; HL = (DE + BC) - END
                jp      nc, fail
                pop     bc
                pop     hl                            ; restore src

                push    de                            ; remember dest for return
                ldir
                ld      (litpool_expr_buf_top), de
                pop     hl                            ; HL = dest (start of copy)
                ret


litpool_expr_buf_top:   defw    LITPOOL_EXPR_BUF


; -----------------------------------------------------------------------
; litpool_lookup — pass-2 helper.  Find the slot whose pc-map entry
; matches PASS_PC.  Returns HL pointing at the slot base, or jp fail
; on miss.
;
; Input:  PASS_PC = inst_pc.
; Output: HL = LITPOOL_TABLE + slot_idx*stride.
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
litpool_lookup:
                ld      a, (LITPOOL_PCM_COUNT)
                or      a
                jp      z, fail
                ld      (litpool_lookup_total), a
                xor     a
                ld      (litpool_lookup_idx), a

litpool_lookup_loop:
                ld      a, (litpool_lookup_idx)
                call    pcm_ptr_for_idx
                ld      a, (PASS_PC + 0)
                cp      (hl)
                jr      nz, litpool_lookup_next
                inc     hl
                ld      a, (PASS_PC + 1)
                cp      (hl)
                jr      nz, litpool_lookup_next
                inc     hl
                ld      a, (PASS_PC + 2)
                cp      (hl)
                jr      nz, litpool_lookup_next
                inc     hl
                ld      a, (PASS_PC + 3)
                cp      (hl)
                jr      nz, litpool_lookup_next
                inc     hl                            ; +4 slot_idx LSB
                ld      a, (hl)
                jp      slot_ptr_for_idx              ; HL = slot ptr

litpool_lookup_next:
                ld      a, (litpool_lookup_idx)
                inc     a
                ld      (litpool_lookup_idx), a
                ld      e, a
                ld      a, (litpool_lookup_total)
                cp      e
                jr      nz, litpool_lookup_loop
                jp      fail


; -----------------------------------------------------------------------
; litpool_encode_ldr_word — pass-2 helper.  Encode an LDR-litpool
; instruction at the current PASS_PC.
;
; Input:  OPVAL_ARRAY[0] populated (Rt at +1, kind at +0 selects width):
;           kind 0x01 (RegX)         → 8-byte pool entry → base 0x58000000
;           kind 0x02 (RegW)         → 4-byte pool entry → base 0x18000000
;         OPVAL_ARRAY[1] is the OpLitPool record (kind 0x0C at +0).
;         LITPOOL_TABLE / LITPOOL_PC_MAP populated by pass 1.
;
; Output: DE:HL = encoded 32-bit word (HL = bits 0..15, DE = bits 16..31).
; Errors: jp fail on lookup miss / pool entry not yet flushed / offset
;         out of range.
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
litpool_encode_ldr_word:
                call    litpool_lookup                ; HL = slot ptr
                ld      (litpool_enc_slot_ptr), hl

; Read width at +0 (Mac uses width 8 → X-form base; we mirror).
                ld      a, (hl)
                ld      (litpool_enc_width), a

; Read entry_pc at +9..+12 into a 4-byte scratch.
                ld      bc, 9
                add     hl, bc
                ld      a, (hl)
                ld      (litpool_enc_entry_pc + 0), a
                inc     hl
                ld      a, (hl)
                ld      (litpool_enc_entry_pc + 1), a
                inc     hl
                ld      a, (hl)
                ld      (litpool_enc_entry_pc + 2), a
                inc     hl
                ld      a, (hl)
                ld      (litpool_enc_entry_pc + 3), a

; Compute off = entry_pc - PASS_PC (signed 32-bit; M5 fixtures stay
; within ±64 KB so the high bytes are well-behaved).
                ld      a, (litpool_enc_entry_pc + 0)
                ld      hl, PASS_PC
                sub     (hl)
                ld      (litpool_enc_off + 0), a
                ld      a, (litpool_enc_entry_pc + 1)
                inc     hl
                sbc     a, (hl)
                ld      (litpool_enc_off + 1), a
                ld      a, (litpool_enc_entry_pc + 2)
                inc     hl
                sbc     a, (hl)
                ld      (litpool_enc_off + 2), a
                ld      a, (litpool_enc_entry_pc + 3)
                inc     hl
                sbc     a, (hl)
                ld      (litpool_enc_off + 3), a

; Verify off % 4 == 0 (low 2 bits zero).
                ld      a, (litpool_enc_off + 0)
                and     3
                jp      nz, fail
; imm19 = off / 4.  Shift the 4-byte off right twice (arithmetic, since
; off may be negative — but for M5 forward-pool layouts, off is
; positive; backward .ltorg layouts would carry sign).
                ld      a, (litpool_enc_off + 3)
                rla                                   ; CF = sign bit
                sbc     a, a                          ; A = sign mask (0 or 0xff)
                ld      e, a                          ; E preserves sign byte

                ld      a, (litpool_enc_off + 3)
                rra                                   ; bit 0 → CF (was a low bit of byte 3)
                ld      (litpool_enc_off + 3), a
                ld      a, (litpool_enc_off + 2)
                rra
                ld      (litpool_enc_off + 2), a
                ld      a, (litpool_enc_off + 1)
                rra
                ld      (litpool_enc_off + 1), a
                ld      a, (litpool_enc_off + 0)
                rra
                ld      (litpool_enc_off + 0), a

                ld      a, e                          ; reload sign mask
                rra                                   ; shift sign into CF
                ld      a, (litpool_enc_off + 3)
                rra
                ld      (litpool_enc_off + 3), a
                ld      a, (litpool_enc_off + 2)
                rra
                ld      (litpool_enc_off + 2), a
                ld      a, (litpool_enc_off + 1)
                rra
                ld      (litpool_enc_off + 1), a
                ld      a, (litpool_enc_off + 0)
                rra
                ld      (litpool_enc_off + 0), a

; imm19 now sits in litpool_enc_off (still 4 bytes; only low 19 bits
; significant).  Range check: byte3 should match sign-extension of bit 18.
; For M5 fixtures imm19 is small positive, so the high two bytes are
; both zero.  Skip the strict range check (we'd need it for M6 if pools
; cross ±1 MiB; not needed now).

; Word = base | (imm19 << 5) | Rt.  Rt = OPVAL_ARRAY[0]+1 & 0x1f.
                ld      a, (OPVAL_ARRAY + 1)
                and     &1f
                ld      c, a                          ; C = Rt

; Build word in DE:HL.  Start with imm19 << 5.
; imm19 occupies bits 23:5.  In a 32-bit word laid out as DE:HL:
;   HL low byte (bits 0..7)   = Rt | (imm19[0:3] << 5)
;   HL high byte (bits 8..15) = imm19[3:11]
;   DE low byte (bits 16..23) = imm19[11:19]
;   DE high byte (bits 24..31)= base byte (0x18 W / 0x58 X)
;
; Use `add a, a` for logical shift-left (== sla a; clears bit 0,
; clean unlike `rla` which rotates the old CF into bit 0).
                ld      a, (litpool_enc_off + 0)      ; imm19 byte0
                add     a, a
                add     a, a
                add     a, a
                add     a, a
                add     a, a                          ; A = (byte0 << 5) low 8 bits
                and     &e0                           ; keep only bits 5..7
                or      c
                ld      l, a                          ; HL low = Rt | (byte0[0:3]<<5)

; HL high byte = (byte1 << 5) | (byte0 >> 3)
                ld      a, (litpool_enc_off + 0)
                srl     a
                srl     a
                srl     a                             ; A = byte0 >> 3 (5 valid bits)
                ld      h, a
                ld      a, (litpool_enc_off + 1)
                add     a, a
                add     a, a
                add     a, a
                add     a, a
                add     a, a
                and     &e0
                or      h
                ld      h, a

; DE low byte = (byte2 << 5) | (byte1 >> 3)
                ld      a, (litpool_enc_off + 1)
                srl     a
                srl     a
                srl     a
                ld      e, a
                ld      a, (litpool_enc_off + 2)
                add     a, a
                add     a, a
                add     a, a
                add     a, a
                add     a, a
                and     &e0
                or      e
                ld      e, a

; DE high byte = base bits 24..31 = 0x18 (W) or 0x58 (X).
                ld      a, (litpool_enc_width)
                cp      8
                jr      z, litpool_enc_x_base
                ld      d, &18
                ret
litpool_enc_x_base:
                ld      d, &58
                ret


; -----------------------------------------------------------------------
; litpool_flush — emit any pending pool entries.
;
; Behaviour:
;   * Pass 1: pad PASS_PC to 4 before any pending Wn; advance PASS_PC by
;     4 for each Wn entry (recording entry_pc); then pad to 8 before any
;     pending Xn; advance by 8 per Xn entry.  Mirrors
;     refenc/pass1.go:85-128.
;   * Pass 2: same alignment/pad behaviour but also EMITs the bytes:
;     zero-pad for alignment, then evaluate each entry's expr (with
;     PASS_PC overridden to that entry's eval_pc) and emit 4 or 8 LE
;     bytes.  Mirrors refenc/pass2.go:21-76.
;
; After flush all pending flags are cleared so a subsequent flush
; doesn't re-emit them.
;
; Input:  none.
; Output: PASS_PC advanced; pool bytes emitted in pass 2.
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
litpool_flush:
                ld      a, (LITPOOL_COUNT)
                or      a
                ret     z

; Pad to 4 if any pending Wn exist.  litpool_any_pending returns Z=1
; when found; we want to call pad in that case.
                ld      a, 4
                call    litpool_any_pending
                jr      nz, litpool_flush_no_w
                call    litpool_pad_to_4
litpool_flush_no_w:
; Emit each pending Wn (width=4) entry in encounter order.
                ld      a, 4
                call    litpool_emit_by_width

; Pad to 8 if any pending Xn exist.
                ld      a, 8
                call    litpool_any_pending
                jr      nz, litpool_flush_no_x
                call    litpool_pad_to_8
litpool_flush_no_x:
                ld      a, 8
                call    litpool_emit_by_width

; Advance segment counters.  FLUSH always advances (both passes).  ALLOC
; only advances in pass 1 — pass 2 doesn't allocate, so leaving ALLOC
; at its pass-1 final value is harmless.
                ld      a, (LITPOOL_SEGMENT_FLUSH)
                inc     a
                ld      (LITPOOL_SEGMENT_FLUSH), a
                ld      a, (PASS_MODE)
                cp      PASS_PASS1
                ret     nz
                ld      a, (LITPOOL_SEGMENT_ALLOC)
                inc     a
                ld      (LITPOOL_SEGMENT_ALLOC), a
                ret


; -----------------------------------------------------------------------
; litpool_any_pending — A = width.  Return Z=1 if at least one pending
; slot of that width exists; Z=0 otherwise.
;
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
litpool_any_pending:
                ld      (litpool_ap_width), a
                ld      a, (LITPOOL_COUNT)
                or      a
                jr      z, litpool_any_none           ; no slots → Z=0
                ld      (litpool_ap_total), a
                xor     a
                ld      (litpool_ap_idx), a

litpool_any_loop:
                ld      a, (litpool_ap_idx)
                call    slot_ptr_for_idx
                ld      a, (hl)                       ; width
                ld      e, a
                ld      a, (litpool_ap_width)
                cp      e
                jr      nz, litpool_any_next
                ld      bc, 13
                add     hl, bc
                ld      a, (hl)                       ; slot.segment
                ld      e, a
                ld      a, (LITPOOL_SEGMENT_FLUSH)
                cp      e
                jr      z, litpool_any_found

litpool_any_next:
                ld      a, (litpool_ap_idx)
                inc     a
                ld      (litpool_ap_idx), a
                ld      e, a
                ld      a, (litpool_ap_total)
                cp      e
                jr      nz, litpool_any_loop
litpool_any_none:
; None found — return Z=0.
                or      &ff
                ret
litpool_any_found:
; Found — return Z=1.
                xor     a
                ret


; -----------------------------------------------------------------------
; litpool_pad_to_4 / litpool_pad_to_8 — pad PASS_PC up to a 4/8 byte
; boundary.  Pass-1: just advance PASS_PC.  Pass-2: also emit zero
; bytes.
;
; Clobbers: A, BC.
; -----------------------------------------------------------------------
litpool_pad_to_4:
                ld      a, (PASS_PC + 0)
                and     3
                ret     z
                ld      b, a
                ld      a, 4
                sub     b
                jp      litpool_pad_emit_n

litpool_pad_to_8:
                ld      a, (PASS_PC + 0)
                and     7
                ret     z
                ld      b, a
                ld      a, 8
                sub     b
                ; fall through

litpool_pad_emit_n:
                or      a
                ret     z
                ld      b, a
litpool_pad_loop:
                ld      a, (PASS_MODE)
                cp      PASS_PASS2
                jr      nz, litpool_pad_no_emit
                push    bc
                xor     a
                call    emit_byte
                pop     bc
litpool_pad_no_emit:
                push    bc
                ld      a, 1
                call    pass_pc_advance_a
                pop     bc
                djnz    litpool_pad_loop
                ret


; -----------------------------------------------------------------------
; litpool_emit_by_width — walk pending slots in encounter order; for
; each matching-width pending slot, record its entry_pc as PASS_PC, then
; in pass-2 emit its 4 or 8 bytes.  PASS_PC advances by entry size each
; iteration.
;
; Input:  A = width (4 or 8).
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
litpool_emit_by_width:
                ld      (litpool_emit_width), a
                ld      a, (LITPOOL_COUNT)
                or      a
                ret     z
                ld      (litpool_emit_total), a
                xor     a
                ld      (litpool_emit_idx), a

litpool_emit_loop:
                ld      a, (litpool_emit_idx)
                call    slot_ptr_for_idx              ; HL = slot ptr
                ld      a, (hl)                       ; width
                ld      e, a
                ld      a, (litpool_emit_width)
                cp      e
                jp      nz, litpool_emit_next

; Filter by current flush segment.  Slots from other segments belong
; to a different .ltorg flush and must NOT be emitted here.
                push    hl
                ld      bc, 13
                add     hl, bc
                ld      a, (hl)                       ; slot.segment
                pop     hl
                ld      e, a
                ld      a, (LITPOOL_SEGMENT_FLUSH)
                cp      e
                jp      nz, litpool_emit_next

; Record entry_pc := PASS_PC into slot +9..+12.
                push    hl
                ld      bc, 9
                add     hl, bc
                ld      a, (PASS_PC + 0)
                ld      (hl), a
                inc     hl
                ld      a, (PASS_PC + 1)
                ld      (hl), a
                inc     hl
                ld      a, (PASS_PC + 2)
                ld      (hl), a
                inc     hl
                ld      a, (PASS_PC + 3)
                ld      (hl), a
                pop     hl

; Pass 2: override PASS_PC with eval_pc, evaluate, emit, restore PASS_PC.
                ld      a, (PASS_MODE)
                cp      PASS_PASS2
                jp      nz, litpool_emit_advance_pc

; Snapshot PASS_PC (the pool entry PC we just recorded as entry_pc).
                ld      a, (PASS_PC + 0)
                ld      (LITPOOL_SAVED_PC + 0), a
                ld      a, (PASS_PC + 1)
                ld      (LITPOOL_SAVED_PC + 1), a
                ld      a, (PASS_PC + 2)
                ld      (LITPOOL_SAVED_PC + 2), a
                ld      a, (PASS_PC + 3)
                ld      (LITPOOL_SAVED_PC + 3), a

; Override PASS_PC := eval_pc (slot +5..+8).
                push    hl
                ld      bc, 5
                add     hl, bc
                ld      a, (hl)
                ld      (PASS_PC + 0), a
                inc     hl
                ld      a, (hl)
                ld      (PASS_PC + 1), a
                inc     hl
                ld      a, (hl)
                ld      (PASS_PC + 2), a
                inc     hl
                ld      a, (hl)
                ld      (PASS_PC + 3), a
                pop     hl

; Pull expr_ptr and expr_len.
                push    hl
                inc     hl                            ; +1 expr_ptr LSB
                ld      e, (hl)
                inc     hl
                ld      d, (hl)
                inc     hl
                ld      c, (hl)
                inc     hl
                ld      b, (hl)
                push    de
                push    bc
                pop     bc                            ; BC = len
                pop     hl                            ; HL = expr_ptr
                call    eval_expr_const
                pop     hl                            ; restore slot base

; Emit width bytes from expr_result.
                ld      a, (litpool_emit_width)
                ld      b, a
                push    hl
                ld      hl, expr_result
litpool_emit_byte_loop:
                ld      a, (hl)
                push    bc
                push    hl
                call    emit_byte
                pop     hl
                pop     bc
                inc     hl
                djnz    litpool_emit_byte_loop
                pop     hl

; Restore PASS_PC from snapshot.
                ld      a, (LITPOOL_SAVED_PC + 0)
                ld      (PASS_PC + 0), a
                ld      a, (LITPOOL_SAVED_PC + 1)
                ld      (PASS_PC + 1), a
                ld      a, (LITPOOL_SAVED_PC + 2)
                ld      (PASS_PC + 2), a
                ld      a, (LITPOOL_SAVED_PC + 3)
                ld      (PASS_PC + 3), a

litpool_emit_advance_pc:
                ld      a, (litpool_emit_width)
                call    pass_pc_advance_a

litpool_emit_next:
                ld      a, (litpool_emit_idx)
                inc     a
                ld      (litpool_emit_idx), a
                ld      e, a
                ld      a, (litpool_emit_total)
                cp      e
                jp      nz, litpool_emit_loop
                ret


; -----------------------------------------------------------------------
; litpool_scan_inst_record — pass-1 helper.  Given a KindInst record's
; payload start pointer, scan its operands for OpLitPool.  Each
; OpLitPool found calls litpool_register with (width, expr_ptr, expr_len).
;
; Only mnemonic 5 (ldr) currently produces OpLitPool; we still scan only
; for that mnemonic (matches refenc/pass1.go:142-161 short-circuit).
;
; Input:  HL = payload ptr (mnemonic_id LSB byte).
; Output: HL clobbered (we don't need it on return).
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
litpool_scan_inst_record:
; mnemonic_id u16 LE
                ld      e, (hl)
                inc     hl
                ld      d, (hl)
                inc     hl
                ld      a, d
                or      a
                ret     nz                            ; mnem > 255 → not ldr
                ld      a, e
                cp      5
                ret     nz                            ; not ldr

; op_count
                ld      a, (hl)
                inc     hl
                or      a
                ret     z
                ld      (litpool_scan_remaining), a

litpool_scan_loop:
                ld      a, (hl)                       ; kind byte
                inc     hl
                cp      OP_KIND_LIT_POOL
                jr      z, litpool_scan_hit

; Skip other operand kinds.
                call    litpool_skip_operand
                jp      litpool_scan_after

litpool_scan_hit:
; Payload: [width u8][expr_len u16 LE][bytecode...].
                ld      a, (hl)                       ; width
                inc     hl
                ld      (litpool_scan_width), a
                ld      c, (hl)
                inc     hl
                ld      b, (hl)
                inc     hl                            ; HL → bytecode start
                ld      (litpool_scan_expr_ptr), hl
                ld      (litpool_scan_expr_len), bc

; Advance HL past the bytecode for any subsequent operands.
                add     hl, bc
                ex      de, hl                        ; DE = post-bytecode ptr
                push    de

                ld      a, (litpool_scan_width)
                ld      hl, (litpool_scan_expr_ptr)
                ld      bc, (litpool_scan_expr_len)
                call    litpool_register

                pop     hl                            ; HL = post-bytecode ptr

litpool_scan_after:
                ld      a, (litpool_scan_remaining)
                dec     a
                ret     z
                ld      (litpool_scan_remaining), a
                jp      litpool_scan_loop


; -----------------------------------------------------------------------
; litpool_skip_operand — A = kind byte; HL = ptr just past kind byte.
; Advance HL past the operand's payload.  Clobbers A, BC, DE.
; -----------------------------------------------------------------------
litpool_skip_operand:
                cp      OP_KIND_REG_X
                jr      z, litpool_skip_1
                cp      OP_KIND_REG_W
                jr      z, litpool_skip_1
                cp      OP_KIND_REG_XSP
                jr      z, litpool_skip_1
                cp      OP_KIND_REG_WSP
                jr      z, litpool_skip_1
                cp      OP_KIND_COND
                jr      z, litpool_skip_1
                cp      OP_KIND_IMM_EXPR
                jr      z, litpool_skip_lenu16
                cp      OP_KIND_STRING
                jr      z, litpool_skip_lenu16
                cp      OP_KIND_SYS_NAME
                jr      z, litpool_skip_lenu16
                cp      OP_KIND_SHIFTED_REG
                jr      z, litpool_skip_shifted
                cp      OP_KIND_EXTENDED_REG
                jr      z, litpool_skip_shifted
                cp      OP_KIND_MEM
                jr      z, litpool_skip_mem
                cp      OP_KIND_LIT_POOL
                jr      z, litpool_skip_lp
                jp      fail

litpool_skip_1:
                inc     hl
                ret

litpool_skip_lenu16:
                ld      c, (hl)
                inc     hl
                ld      b, (hl)
                inc     hl
                add     hl, bc
                ret

litpool_skip_shifted:
                inc     hl                            ; width
                inc     hl                            ; reg
                inc     hl                            ; shift/extend
                ld      c, (hl)
                inc     hl
                ld      b, (hl)
                inc     hl
                add     hl, bc
                ret

litpool_skip_lp:
                inc     hl                            ; width
                ld      c, (hl)
                inc     hl
                ld      b, (hl)
                inc     hl
                add     hl, bc
                ret

litpool_skip_mem:
                ld      a, (hl)                       ; shape
                inc     hl                            ; shape
                inc     hl                            ; base
                or      a
                ret     z
                cp      1
                jr      z, litpool_skip_mem_off
                cp      2
                jr      z, litpool_skip_mem_off
                cp      3
                jr      z, litpool_skip_mem_off
                cp      4
                jr      z, litpool_skip_mem_idx
                cp      5
                jr      z, litpool_skip_mem_idxs
                cp      6
                jr      z, litpool_skip_mem_idxe
                jp      fail

litpool_skip_mem_off:
                ld      c, (hl)
                inc     hl
                ld      b, (hl)
                inc     hl
                add     hl, bc
                ret

litpool_skip_mem_idx:
                inc     hl
                inc     hl
                ret

litpool_skip_mem_idxs:
                inc     hl
                inc     hl
                inc     hl
                ret

litpool_skip_mem_idxe:
                inc     hl
                inc     hl
                inc     hl
                inc     hl
                ret


; -----------------------------------------------------------------------
; Scratch.
; -----------------------------------------------------------------------
litpool_reg_width:              defb    0
litpool_reg_expr_ptr:           defw    0
litpool_reg_expr_len:           defw    0
litpool_reg_slot_ptr:           defw    0
litpool_reg_idx:                defb    0
litpool_reg_total:              defb    0

litpool_lookup_idx:             defb    0
litpool_lookup_total:           defb    0

litpool_enc_slot_ptr:           defw    0
litpool_enc_width:              defb    0
litpool_enc_entry_pc:           defb    0, 0, 0, 0
litpool_enc_off:                defb    0, 0, 0, 0

litpool_ap_width:               defb    0
litpool_ap_idx:                 defb    0
litpool_ap_total:               defb    0

litpool_emit_width:             defb    0
litpool_emit_idx:               defb    0
litpool_emit_total:             defb    0


litpool_scan_remaining:         defb    0
litpool_scan_width:             defb    0
litpool_scan_expr_ptr:          defw    0
litpool_scan_expr_len:          defw    0
