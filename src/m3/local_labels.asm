; local_labels.asm — local-label table for the M4+ assembler.
;
; Per docs/specs/2026-05-24-m4-symbols-multipass-design.md §2.3 (v2,
; multi-digit) and docs/plans/2026-05-27-multi-digit-local-labels.md.
;
; Local labels are written as a decimal digit `1..99` and are
; referenced as either `Nf` (forward — next occurrence with PC strictly
; greater than the reference PC) or `Nb` (backward — most recent
; occurrence with PC less than or equal to the reference PC).
;
; This table is SEPARATE from the global symbol table (symbols.asm) and
; is consulted only when expr_eval resolves a LOCAL_REF operand.
;
; -----------------------------------------------------------------------
; On-disk layout
; -----------------------------------------------------------------------
;
; Single shared sorted list of (digit, pc) tuples, at LOCAL_LABEL_TABLE
; (= &CD60).  Five bytes per entry (1 B digit + 4 B PC LE), with a
; 2-byte count prefix:
;
; Memory layout (must fit in &CD60..&D0FF — below OPMEM_OFF at &D100):
;
;   offset 0..1   count   u16 LE     (entries used; 0..LOCAL_LIST_MAX)
;   offset 2..    entries — for i in 0..count:
;     base + 2 + i*5 + 0       = digit (u8, 1..99)
;     base + 2 + i*5 + 1..4    = pc    (u32 LE)
;
; LOCAL_LIST_MAX = 180 → 2 + 180*5 = 902 bytes; table ends at &D0E5.
; Capped below OPMEM_OFF (&D100, allocated for M5 OpMem encoder) so the
; two regions cannot overlap.  Pass 1 populates the table; pass 2 reads
; it (and writes OPMEM_OFF) — temporally disjoint within a single pass,
; but a high-locals fixture exceeding ~168 entries would silently corrupt
; OPMEM_OFF on the pass boundary if the bound weren't enforced here.
;
; Sort order: definition order (= PC order).  Pass 1 walks records
; sequentially, so PCs at each call to local_def_append are
; monotonically non-decreasing across the whole list (and so within
; each digit's subsequence too).  Appending in order automatically
; keeps the list sorted; no insertion-point search is required.
;
; -----------------------------------------------------------------------
; ABI summary (full per-routine headers below)
; -----------------------------------------------------------------------
;
;   local_label_table_init                            — reset the count
;   local_def_append      A=digit, (local_label_pc_buf)=pc      — append
;   local_find_forward    A=digit, (local_label_pc_buf)=ref_pc  — > ref
;   local_find_backward   A=digit, (local_label_pc_buf)=ref_pc  — <=ref
;
; PC values (input and output) are passed via the static 4-byte
; little-endian buffer `local_label_pc_buf`, mirroring the
; `symbol_value_buf` convention from symbols.asm.  This keeps register
; pressure off the caller (A is the digit; BC/DE/HL are scratch).
;
; Errors:
;   * invalid digit (< 1 or > 99)            → jp fail
;   * append into full shared list           → jp fail
; Both are unrecoverable; same single-shot termination as symbol_insert.
;
; -----------------------------------------------------------------------

; ---------------------------------------------------------------------
; Memory map (must match the reservation comment in assembler.asm).
; ---------------------------------------------------------------------
LOCAL_LABEL_TABLE:      equ     &CD60   ; 2-byte count + 180 * 5-byte entries = 902 bytes
LOCAL_LIST_MAX:         equ     180     ; total entries across ALL digits
                                        ; capped at 180 so the table (&CD60..&D0E5)
                                        ; ends below OPMEM_OFF at &D100.
LOCAL_ENTRY_SIZE:       equ     5       ; 1 byte digit + 4 bytes PC (LE)
LOCAL_MAX_DIGIT:        equ     99      ; digit range 1..99 inclusive


; -----------------------------------------------------------------------
; local_label_table_init — zero the entry count.
;
; Only the 2-byte count field needs zeroing; the entry slots are
; don't-care until the count grows to include them.
;
; Input:  none.
; Output: count = 0.
; Clobbers: A, HL.
; -----------------------------------------------------------------------
local_label_table_init:
                ld      hl, LOCAL_LABEL_TABLE
                ld      (hl), 0                     ; count LSB
                inc     hl
                ld      (hl), 0                     ; count MSB
                ret


; -----------------------------------------------------------------------
; local_def_append — append a (digit, pc) entry to the shared list.
;
; Input:
;   A                              = digit (1..99).
;   (local_label_pc_buf + 0..3)    = pc u32 LE.
;
; Output: returns normally on success.  local_label_pc_buf unchanged.
;
; Errors:
;   Invalid digit (< 1 or > 99)          → jp fail.
;   List full (count == LOCAL_LIST_MAX)  → jp fail.
;
; Clobbers: A, BC, DE, HL.
;
; Strategy:
;   1. Validate digit.
;   2. Read count; range-check against LOCAL_LIST_MAX.
;   3. Write digit at base + 2 + count*5.
;   4. Write 4-byte PC at base + 2 + count*5 + 1.
;   5. Bump count.
; -----------------------------------------------------------------------
local_def_append:
                ld      (local_saved_digit), a      ; A = digit, save

; Validate digit ∈ [1, 99].
                or      a
                jp      z, fail                     ; digit 0 invalid
                cp      LOCAL_MAX_DIGIT + 1
                jp      nc, fail                    ; digit > 99 invalid

; Load count.  Cap < 256 so high byte must be 0; check both halves
; defensively (any non-zero MSB ⇒ corruption).
                ld      hl, LOCAL_LABEL_TABLE
                inc     hl                          ; HL → count MSB
                ld      a, (hl)
                or      a
                jp      nz, fail                    ; MSB != 0 ⇒ corruption
                dec     hl                          ; HL = base
                ld      a, (hl)                     ; A = count LSB
                cp      LOCAL_LIST_MAX
                jp      nc, fail                    ; list full

; Compute entry address: base + 2 + count*5.
                ld      e, a                        ; E = count
                ld      d, 0
                ex      de, hl                      ; HL = count, DE = base
                ld      b, h
                ld      c, l                        ; BC = count
                add     hl, hl                      ; *2
                add     hl, hl                      ; *4
                add     hl, bc                      ; *5
                ld      bc, 2
                add     hl, bc                      ; + 2 (skip count field)
                add     hl, de                      ; + base → HL = &entries[count]

; Write digit byte.
                ld      a, (local_saved_digit)
                ld      (hl), a
                inc     hl

; Write 4-byte PC from local_label_pc_buf.
                ld      de, local_label_pc_buf
                ex      de, hl                      ; HL = src, DE = dest
                ld      bc, 4
                ldir

; Bump count (LSB only; high byte stays 0 because cap < 256).
                ld      hl, LOCAL_LABEL_TABLE
                inc     (hl)
                ret


; -----------------------------------------------------------------------
; local_find_forward — `Nf` lookup: smallest pc in the entries with
; digit == A, where pc is strictly greater than ref_pc.
;
; Input:
;   A                              = digit (1..99).
;   (local_label_pc_buf + 0..3)    = ref_pc u32 LE.
;
; Output (hit):
;   CF = 0.
;   (local_label_pc_buf + 0..3)    = resolved pc u32 LE.
;
; Output (miss — no entry > ref_pc):
;   CF = 1.
;   local_label_pc_buf is unchanged.
;
; Errors: invalid digit → jp fail.
;
; Clobbers: A, BC, DE, HL.
;
; Strategy: the list is sorted ascending by PC across all digits
; (definition order = PC order).  Linear scan from low to high, skip
; entries whose digit doesn't match; first matching pc with pc > ref_pc
; wins.  List caps at 200 entries so linear is fine.
; -----------------------------------------------------------------------
local_find_forward:
                ld      (local_saved_digit), a      ; A = target digit

; Validate digit ∈ [1, 99] (mirrors append).
                or      a
                jp      z, fail
                cp      LOCAL_MAX_DIGIT + 1
                jp      nc, fail

                ld      hl, LOCAL_LABEL_TABLE
                ld      a, (hl)                     ; count LSB
                inc     hl                          ; (skip high byte — known 0)
                inc     hl                          ; HL = &entries[0]

                or      a
                jr      z, lff_miss
                ld      b, a                        ; B = entries remaining

lff_loop:
                push    hl                          ; save base of this entry
                ld      a, (hl)                     ; A = entry digit
                ld      c, a
                ld      a, (local_saved_digit)
                cp      c
                jr      nz, lff_advance             ; digit mismatch — skip
                inc     hl                          ; HL → entry's pc field
                call    cmp_pc_at_hl_vs_ref         ; preserves HL, BC
                jr      c, lff_advance              ; cand < ref → skip
                or      a
                jr      nz, lff_hit                 ; cand > ref → HIT
                ; cand == ref → skip (need strictly greater)
lff_advance:
                pop     hl
                ld      a, LOCAL_ENTRY_SIZE
                add     a, l
                ld      l, a
                jr      nc, lff_no_carry
                inc     h
lff_no_carry:
                djnz    lff_loop

lff_miss:
                scf
                ret

lff_hit:
                ; HL → pc field of matching entry.  Copy 4 B to local_label_pc_buf.
                ld      de, local_label_pc_buf
                ld      bc, 4
                ldir
                pop     bc                          ; discard saved entry base
                or      a                           ; CF = 0
                ret


; -----------------------------------------------------------------------
; local_find_backward — `Nb` lookup: largest pc in the entries with
; digit == A, where pc is less than or equal to ref_pc.
;
; Input:
;   A                              = digit (1..99).
;   (local_label_pc_buf + 0..3)    = ref_pc u32 LE.
;
; Output (hit):
;   CF = 0.
;   (local_label_pc_buf + 0..3)    = resolved pc u32 LE.
;
; Output (miss — no entry <= ref_pc):
;   CF = 1.
;   local_label_pc_buf is unchanged.
;
; Errors: invalid digit → jp fail.
;
; Clobbers: A, BC, DE, HL.
;
; Strategy: list is sorted ascending by PC.  Walk from low to high;
; remember the address of the pc-field of the most recent matching
; entry that satisfies entry.pc <= ref_pc; stop when we see a matching
; entry with pc > ref_pc (no further match possible since the list is
; sorted).  Non-matching digits are skipped.
; -----------------------------------------------------------------------
local_find_backward:
                ld      (local_saved_digit), a

                or      a
                jp      z, fail
                cp      LOCAL_MAX_DIGIT + 1
                jp      nc, fail

                ld      hl, LOCAL_LABEL_TABLE
                ld      a, (hl)                     ; count LSB
                inc     hl
                inc     hl                          ; HL = &entries[0]

                or      a
                jp      z, lfb_miss
                ld      b, a

                ld      de, 0
                ld      (local_pending_match), de   ; no candidate yet

lfb_loop:
                push    hl                          ; save entry base
                ld      a, (hl)
                ld      c, a
                ld      a, (local_saved_digit)
                cp      c
                jr      nz, lfb_advance             ; digit mismatch — skip
                inc     hl                          ; HL → pc field
                call    cmp_pc_at_hl_vs_ref         ; preserves HL, BC
                jr      c, lfb_remember             ; cand < ref → record + keep scanning
                or      a
                jr      nz, lfb_advance             ; cand > ref → not eligible; skip
                ; cand == ref → exact match; record and STOP (no later entry can beat ==)
                ld      (local_pending_match), hl
                pop     bc                          ; discard saved entry base
                jr      lfb_done

lfb_remember:
                ld      (local_pending_match), hl   ; HL → pc field
lfb_advance:
                pop     hl
                ld      a, LOCAL_ENTRY_SIZE
                add     a, l
                ld      l, a
                jr      nc, lfb_no_carry
                inc     h
lfb_no_carry:
                djnz    lfb_loop

lfb_done:
                ld      hl, (local_pending_match)
                ld      a, h
                or      l
                jr      z, lfb_miss
                ld      de, local_label_pc_buf
                ld      bc, 4
                ldir
                or      a
                ret

lfb_miss:
                scf
                ret


; -----------------------------------------------------------------------
; cmp_pc_at_hl_vs_ref — internal helper: compare the 4-byte LE pc at
; HL against the 4-byte LE ref_pc at local_label_pc_buf.
;
; Input:  HL → candidate pc (4 bytes LE).
; Output:
;   HL preserved.
;   CF = 1  ⇒ candidate <  ref_pc.
;   CF = 0  ⇒ candidate >= ref_pc.
;   A  = 0  if candidate == ref_pc (only meaningful when CF = 0).
;   A != 0  if candidate >  ref_pc (only meaningful when CF = 0).
;
; Encoding:
;   For each byte i in 0..3 we compute (candidate[i] - ref_pc[i]) with
;   borrow-in from byte i-1.  A subtraction chain with SBC handles
;   borrow correctly.  Final CF is the borrow out of the top byte: 1
;   if candidate < ref_pc as unsigned, 0 otherwise.  To distinguish
;   equality from "greater" we OR the running difference bytes; if all
;   four sub-results are zero, candidate == ref_pc.
;
; Clobbers: A, DE.  BC, HL preserved.  Caller relies on:
;   * HL preservation — for the equality fast-path (record &candidate)
;     and the advance step that follows.
;   * BC preservation — the find_* loops use B as the remaining-entries
;     counter across the call.
; -----------------------------------------------------------------------
cmp_pc_at_hl_vs_ref:
                push    bc                          ; preserve caller's BC (loop ctr in B)
                push    hl                          ; save candidate pointer for caller
; Layout:  DE = &cand  (= original HL),  HL = &ref_pc_buf.
                ex      de, hl
                ld      hl, local_label_pc_buf

                ld      b, 0                        ; B = OR of diff bytes
                or      a                           ; clear CF for first SUB

; Byte 0: cand[0] - ref[0].  `or b` would clobber the CF that the next
; SBC needs, so we wrap the accumulator update in push af / pop af —
; same pattern as bytes 1..3 below.
                ld      a, (de)
                sub     (hl)
                push    af                          ; preserve CF
                or      b
                ld      b, a
                pop     af                          ; restore CF for next SBC

; Bytes 1..3: chained SBC, with the same CF-preservation discipline.
                inc     hl
                inc     de
                ld      a, (de)
                sbc     a, (hl)
                push    af                          ; preserve CF
                or      b
                ld      b, a
                pop     af                          ; restore CF

                inc     hl
                inc     de
                ld      a, (de)
                sbc     a, (hl)
                push    af
                or      b
                ld      b, a
                pop     af

                inc     hl
                inc     de
                ld      a, (de)
                sbc     a, (hl)                     ; final CF = borrow (1 ⇒ cand<ref)
                push    af
                or      b
                ld      b, a                        ; B = OR of all 4 diff bytes
                pop     af                          ; restore final CF

                pop     hl                          ; restore candidate pointer
                ld      a, b                        ; A = OR of diff bytes (0 ⇒ ==)
                pop     bc                          ; restore caller's BC (does not touch flags)
                ret


; -----------------------------------------------------------------------
; Scratch / state.
; -----------------------------------------------------------------------
; local_label_pc_buf — 4-byte LE buffer used by callers to pass pc into
; local_def_append and ref_pc into local_find_*, and to receive the
; resolved pc from local_find_*.  See ABI note at the top of this file.
local_label_pc_buf:     defb    0, 0, 0, 0

; local_pending_match — best-so-far candidate address (pc-field of an
; entry) during local_find_backward.  0x0000 means "no match yet".
local_pending_match:    defw    0

; local_saved_digit — caller's A across append / find_forward / find_backward.
local_saved_digit:      defb    0
