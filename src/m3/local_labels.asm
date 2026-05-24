; local_labels.asm — per-digit local-label table for the M4 assembler.
;
; Per docs/specs/2026-05-24-m4-symbols-multipass-design.md §2.3.
;
; Local labels are written as a single decimal digit `1..9` and are
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
; Nine fixed-size per-digit lists, packed contiguously at
; LOCAL_LABEL_TABLE (= &CD60).  Each list is LOCAL_LIST_STRIDE bytes:
;
;   offset 0      count   u16 LE     (entries used; 0..LOCAL_LIST_MAX)
;   offset 2..97  pcs     u32 LE * LOCAL_LIST_MAX
;
; LOCAL_LIST_MAX = 24 entries per digit → stride 2 + 24*4 = 98 bytes.
; Total reservation: 9 × 98 = 882 bytes  (&CD60..&D0C1 inclusive),
; comfortably inside the &CD60..&D15F (1 KB) budget called out in
; assembler.asm.
;
; The 9 lists are addressed via a static base-pointer table
; (local_list_bases) rather than by multiplying digit*stride — stride
; 98 has no nice power-of-two factor, and a 9-entry pointer table is 18
; bytes of ROM, which is cheaper than 6+ shifts at every call site.
;
; Sort order: definition order (= PC order).  Pass 1 walks records
; sequentially, so PCs at each call to local_def_append are
; monotonically non-decreasing within a digit.  This means appending in
; order automatically keeps the list sorted; no insertion-point search
; is required.
;
; -----------------------------------------------------------------------
; ABI summary (full per-routine headers below)
; -----------------------------------------------------------------------
;
;   local_label_table_init                           — reset all 9 counts
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
;   * invalid digit (< 1 or > 9)             → jp fail
;   * append into full per-digit list        → jp fail
; Both are unrecoverable; same single-shot termination as symbol_insert.
;
; -----------------------------------------------------------------------

; ---------------------------------------------------------------------
; Memory map (must match the reservation comment in assembler.asm).
; ---------------------------------------------------------------------
LOCAL_LABEL_TABLE:      equ     &CD60          ; 9 × 98 bytes = 882 bytes
LOCAL_LIST_MAX:         equ     24             ; max entries per digit
LOCAL_LIST_STRIDE:      equ     98             ; 2 (count) + 24 * 4 (pcs)


; -----------------------------------------------------------------------
; local_label_table_init — zero all per-digit counts.
;
; Only the 2-byte count field of each per-digit list needs zeroing; the
; PC slots are don't-care until the count grows to include them.
;
; Input:  none.
; Output: count = 0 in every per-digit list.
; Clobbers: A, BC, HL.
; -----------------------------------------------------------------------
local_label_table_init:
                ld      hl, LOCAL_LABEL_TABLE
                ld      a, 9                        ; A = digits remaining
local_label_table_init_loop:
                ld      (hl), 0                     ; count LSB
                inc     hl
                ld      (hl), 0                     ; count MSB
                dec     hl                          ; HL back at count[0]
                push    af                          ; preserve counter
                ld      bc, LOCAL_LIST_STRIDE
                add     hl, bc                      ; HL = next digit's count
                pop     af
                dec     a
                jr      nz, local_label_table_init_loop
                ret


; -----------------------------------------------------------------------
; local_get_list_base — internal helper: HL := base of digit A's list.
;
; Input:  A = digit (1..9).
; Output: HL = address of the digit's count field (= list base).
; Errors: A < 1 or A > 9 → jp fail.
; Clobbers: A, BC, HL.
;
; Implementation: 9-entry pointer table (local_list_bases) holds the
; base address of each digit's list.  Digit d (1-indexed) is at table
; offset (d-1)*2.
; -----------------------------------------------------------------------
local_get_list_base:
                or      a
                jp      z, fail                     ; digit 0 invalid
                cp      10
                jp      nc, fail                    ; digit >= 10 invalid

                dec     a                           ; 0..8 = table index
                ld      l, a
                ld      h, 0
                add     hl, hl                      ; * 2 (pointer entry size)
                ld      bc, local_list_bases
                add     hl, bc                      ; HL = &local_list_bases[d-1]
                ld      c, (hl)
                inc     hl
                ld      b, (hl)                     ; BC = digit's list base
                ld      h, b
                ld      l, c                        ; HL = digit's list base
                ret


; -----------------------------------------------------------------------
; local_def_append — append a PC to digit A's per-digit list.
;
; Input:
;   A                              = digit (1..9).
;   (local_label_pc_buf + 0..3)    = pc u32 LE.
;
; Output: returns normally on success.  local_label_pc_buf unchanged.
;
; Errors:
;   Invalid digit (< 1 or > 9)           → jp fail.
;   Per-digit list full (count == MAX)   → jp fail.
;
; Clobbers: A, BC, DE, HL.
;
; Strategy:
;   1. Resolve list base via local_get_list_base.
;   2. Read count; range-check against LOCAL_LIST_MAX.
;   3. Append PC at &(base + 2 + count*4).
;   4. Bump count.
; -----------------------------------------------------------------------
local_def_append:
                call    local_get_list_base         ; HL = list base
                ld      (local_pending_base), hl    ; save for write-back

; Read count (16-bit LE at HL+0..1).
                ld      e, (hl)
                inc     hl
                ld      d, (hl)                     ; DE = count
                dec     hl                          ; HL = base

; Range check: count must be < LOCAL_LIST_MAX.
; Counts are bounded by LOCAL_LIST_MAX (= 24), so the high byte is 0.
                ld      a, d
                or      a
                jp      nz, fail                    ; > 255 — impossible but defensive
                ld      a, e
                cp      LOCAL_LIST_MAX
                jp      nc, fail                    ; full — overflow

; Compute insertion address: base + 2 + count*4.
                ex      de, hl                      ; HL = count, DE = base
                add     hl, hl                      ; * 2
                add     hl, hl                      ; * 4
                ld      bc, 2
                add     hl, bc                      ; + 2 (skip count field)
                add     hl, de                      ; + base → HL = &pcs[count]

; Write 4 bytes from local_label_pc_buf into &pcs[count].
                ld      de, local_label_pc_buf
                ex      de, hl                      ; HL = src, DE = dest
                ld      bc, 4
                ldir

; Bump count.  HL was clobbered by ldir; reload from saved base.
                ld      hl, (local_pending_base)
                ld      e, (hl)
                inc     hl
                ld      d, (hl)
                dec     hl
                inc     de                          ; count++
                ld      (hl), e
                inc     hl
                ld      (hl), d
                ret


; -----------------------------------------------------------------------
; local_find_forward — `Nf` lookup: smallest pc in digit A's list with
; pc strictly greater than ref_pc.
;
; Input:
;   A                              = digit (1..9).
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
; Errors: invalid digit → jp fail (via local_get_list_base).
;
; Clobbers: A, BC, DE, HL.
;
; Strategy: the list is sorted ascending (definition order = PC order).
; Linear scan from low to high; first pc with pc > ref_pc wins.  Lists
; cap at 24 entries so linear is fine.
; -----------------------------------------------------------------------
local_find_forward:
                call    local_get_list_base         ; HL = list base

; Read count.
                ld      e, (hl)
                inc     hl
                ld      d, (hl)                     ; DE = count
                inc     hl                          ; HL → &pcs[0]

; If count == 0, miss.
                ld      a, d
                or      e
                jr      z, local_find_forward_miss

                ld      b, e                        ; B = remaining entries (< 256)

local_find_forward_loop:
; Compare *HL (4 bytes LE) against ref_pc.  Want: *HL > ref_pc, i.e.
; ref_pc < *HL.  Compute (*HL) - ref_pc via SUB on each byte from LSB
; up; if final CF=0 AND any byte differs, *HL > ref_pc.  Easier: use
; the CP-style sequence below, but track equality separately because
; CP only tests for <.
;
; Concretely we compute: candidate - ref_pc.  CF=1 ⇒ candidate < ref_pc
; (skip).  CF=0 ⇒ candidate >= ref_pc; need to disambiguate ==/>.
                call    cmp_pc_at_hl_vs_ref         ; sets flags; preserves HL, DE
; cmp returns:
;   A = 0, CF = 0 ⇒ *HL == ref_pc  (skip — need strictly greater)
;   A = 0, CF = 1 ⇒ *HL  < ref_pc  (skip)
;   A != 0, CF = 0 ⇒ *HL > ref_pc  (HIT)
                jr      c, local_find_forward_skip
                or      a
                jr      nz, local_find_forward_hit
local_find_forward_skip:
; Advance HL by 4 bytes to next entry, decrement remaining.
                ld      a, 4
                add     a, l
                ld      l, a
                jr      nc, local_find_forward_no_carry_skip
                inc     h
local_find_forward_no_carry_skip:
                djnz    local_find_forward_loop

local_find_forward_miss:
                scf
                ret

local_find_forward_hit:
; Copy 4 bytes at HL into local_label_pc_buf, CF=0.
                ld      de, local_label_pc_buf
                ld      bc, 4
                ldir
                or      a                           ; CF = 0
                ret


; -----------------------------------------------------------------------
; local_find_backward — `Nb` lookup: largest pc in digit A's list with
; pc <= ref_pc.
;
; Input:
;   A                              = digit (1..9).
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
; Strategy: list is sorted ascending.  Walk from low to high; remember
; the most recent entry that satisfies entry <= ref_pc; stop when we
; see an entry > ref_pc (no further match possible since the list is
; sorted).  Wins because we only ever need one pass.
; -----------------------------------------------------------------------
local_find_backward:
                call    local_get_list_base         ; HL = list base

; Read count.
                ld      e, (hl)
                inc     hl
                ld      d, (hl)                     ; DE = count
                inc     hl                          ; HL → &pcs[0]

; If count == 0, miss.
                ld      a, d
                or      e
                jp      z, local_find_backward_miss

                ld      b, e                        ; B = remaining (< 256)

; local_pending_match stores the address (in the table) of the best
; candidate seen so far.  0x0000 indicates "no candidate yet".  Table
; addresses are >= LOCAL_LABEL_TABLE (= &CD60), so 0x0000 is safely
; outside the valid range.
                ld      de, 0
                ld      (local_pending_match), de

local_find_backward_loop:
                call    cmp_pc_at_hl_vs_ref         ; preserves HL
; A,CF semantics same as in forward:
;   A=0, CF=0 ⇒ *HL == ref_pc → candidate (<= ref_pc), and since list
;                                is sorted ascending and we want the
;                                LARGEST <= ref_pc, this is the best
;                                possible match: record and STOP.
;   A=0, CF=1 ⇒ *HL  < ref_pc → candidate; update and keep scanning,
;                                a later entry may also be <= ref_pc.
;   A!=0,CF=0 ⇒ *HL  > ref_pc → no longer eligible; stop (list sorted).
                jr      c, local_find_backward_remember
                or      a
                jr      nz, local_find_backward_done
; Equal case: record and stop.
                ld      (local_pending_match), hl
                jr      local_find_backward_done

local_find_backward_remember:
; *HL < ref_pc — record as best-so-far, then advance.
                ld      (local_pending_match), hl

; Advance HL by 4.
                ld      a, 4
                add     a, l
                ld      l, a
                jr      nc, local_find_backward_no_carry
                inc     h
local_find_backward_no_carry:
                djnz    local_find_backward_loop

local_find_backward_done:
; If local_pending_match is still 0, no entry was <= ref_pc → miss.
                ld      hl, (local_pending_match)
                ld      a, h
                or      l
                jr      z, local_find_backward_miss

; Copy 4 bytes from match address into local_label_pc_buf, CF=0.
                ld      de, local_label_pc_buf
                ld      bc, 4
                ldir
                or      a                           ; CF = 0
                ret

local_find_backward_miss:
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

; local_pending_base — saved digit-list base across the append routine.
local_pending_base:     defw    0

; local_pending_match — best-so-far candidate address (in the list)
; during local_find_backward.  0x0000 means "no match yet".
local_pending_match:    defw    0

; local_list_bases — base address of each per-digit list, indexed by
; (digit - 1).  Used by local_get_list_base to skip the awkward
; multiplication by LOCAL_LIST_STRIDE (= 98).
local_list_bases:
                defw    LOCAL_LABEL_TABLE + 0 * LOCAL_LIST_STRIDE   ; digit 1
                defw    LOCAL_LABEL_TABLE + 1 * LOCAL_LIST_STRIDE   ; digit 2
                defw    LOCAL_LABEL_TABLE + 2 * LOCAL_LIST_STRIDE   ; digit 3
                defw    LOCAL_LABEL_TABLE + 3 * LOCAL_LIST_STRIDE   ; digit 4
                defw    LOCAL_LABEL_TABLE + 4 * LOCAL_LIST_STRIDE   ; digit 5
                defw    LOCAL_LABEL_TABLE + 5 * LOCAL_LIST_STRIDE   ; digit 6
                defw    LOCAL_LABEL_TABLE + 6 * LOCAL_LIST_STRIDE   ; digit 7
                defw    LOCAL_LABEL_TABLE + 7 * LOCAL_LIST_STRIDE   ; digit 8
                defw    LOCAL_LABEL_TABLE + 8 * LOCAL_LIST_STRIDE   ; digit 9
