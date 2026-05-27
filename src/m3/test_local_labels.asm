; test_local_labels.asm — boot-time self-tests for the M4 local-label
; table.
;
; Per docs/specs/2026-05-24-m4-symbols-multipass-design.md §2.3 and the
; M4 plan (docs/plans/2026-05-24-m4-symbols-multipass.md Task 3).
;
; Mirrors test_symbols.asm's structure: runs from start: BEFORE
; load_enctab, with assertion failures going `jp fail` (red border +
; 30 s spin → ci-m3 timeout).
;
; Test coverage:
;   1. After local_label_table_init, lookup of any digit returns CF=1.
;   2. Build digit-1 list = [4, 12, 24, 100] via local_def_append.
;   3. Forward lookups (`Nf`-style — strictly greater than ref_pc):
;        ref_pc=0   → 4
;        ref_pc=12  → 24      (strictly greater, NOT 12)
;        ref_pc=50  → 100
;        ref_pc=200 → CF=1
;   4. Backward lookups (`Nb`-style — less than or equal to ref_pc):
;        ref_pc=0   → CF=1    (4 > 0)
;        ref_pc=12  → 12      (less than OR equal, NOT 4)
;        ref_pc=50  → 24
;        ref_pc=200 → 100
;   5. Cross-digit isolation: appending to digit 3 must not perturb
;      digit 1's list — re-run the digit-1 lookups after appending
;      digit=3 pc=999.
;
; Not exercised (one-way `jp fail` paths, same caveat as test_slots /
; test_symbols):
;   - Invalid digit (0 or >=10).
;   - Per-digit list overflow (>= LOCAL_LIST_MAX).
;   Both → jp fail; covered by inspection.


; -----------------------------------------------------------------------
; run_local_label_self_tests — entry point called from assembler.asm
; start: (between run_symbol_table_self_tests and load_enctab).
;
; Input:  none.
; Output: returns to caller on success.  On any mismatch: jp fail.
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
run_local_label_self_tests:

; -- (1) Init then forward / backward lookup → miss --------------------
                call    local_label_table_init

                call    set_local_pc_buf_imm
                defb    &00, &00, &00, &00
                ld      a, 1
                call    local_find_forward
                call    assert_cf_set

                call    set_local_pc_buf_imm
                defb    &FF, &FF, &FF, &FF
                ld      a, 1
                call    local_find_backward
                call    assert_cf_set

; -- (2) Build digit-1 list = [4, 12, 24, 100] -------------------------

                call    set_local_pc_buf_imm
                defb    &04, &00, &00, &00
                ld      a, 1
                call    local_def_append

                call    set_local_pc_buf_imm
                defb    &0C, &00, &00, &00          ; 12
                ld      a, 1
                call    local_def_append

                call    set_local_pc_buf_imm
                defb    &18, &00, &00, &00          ; 24
                ld      a, 1
                call    local_def_append

                call    set_local_pc_buf_imm
                defb    &64, &00, &00, &00          ; 100
                ld      a, 1
                call    local_def_append

; -- (3) Forward lookups ----------------------------------------------

; ref_pc = 0 → 4 (smallest entry > 0).
                call    set_local_pc_buf_imm
                defb    &00, &00, &00, &00
                ld      a, 1
                call    local_find_forward
                call    assert_cf_clear
                call    assert_local_pc_buf_eq_imm
                defb    &04, &00, &00, &00

; ref_pc = 12 → 24 (STRICTLY greater than 12; must NOT return 12).
                call    set_local_pc_buf_imm
                defb    &0C, &00, &00, &00
                ld      a, 1
                call    local_find_forward
                call    assert_cf_clear
                call    assert_local_pc_buf_eq_imm
                defb    &18, &00, &00, &00

; ref_pc = 50 → 100.
                call    set_local_pc_buf_imm
                defb    &32, &00, &00, &00          ; 50
                ld      a, 1
                call    local_find_forward
                call    assert_cf_clear
                call    assert_local_pc_buf_eq_imm
                defb    &64, &00, &00, &00          ; 100

; ref_pc = 200 → CF=1 (nothing greater).
                call    set_local_pc_buf_imm
                defb    &C8, &00, &00, &00          ; 200
                ld      a, 1
                call    local_find_forward
                call    assert_cf_set

; -- (4) Backward lookups ---------------------------------------------

; ref_pc = 0 → CF=1 (no entry <= 0; smallest is 4).
                call    set_local_pc_buf_imm
                defb    &00, &00, &00, &00
                ld      a, 1
                call    local_find_backward
                call    assert_cf_set

; ref_pc = 12 → 12 (less than OR EQUAL; must NOT return 4).
                call    set_local_pc_buf_imm
                defb    &0C, &00, &00, &00
                ld      a, 1
                call    local_find_backward
                call    assert_cf_clear
                call    assert_local_pc_buf_eq_imm
                defb    &0C, &00, &00, &00

; ref_pc = 50 → 24.
                call    set_local_pc_buf_imm
                defb    &32, &00, &00, &00
                ld      a, 1
                call    local_find_backward
                call    assert_cf_clear
                call    assert_local_pc_buf_eq_imm
                defb    &18, &00, &00, &00

; ref_pc = 200 → 100 (largest entry; 100 <= 200).
                call    set_local_pc_buf_imm
                defb    &C8, &00, &00, &00
                ld      a, 1
                call    local_find_backward
                call    assert_cf_clear
                call    assert_local_pc_buf_eq_imm
                defb    &64, &00, &00, &00

; -- (5) Cross-digit isolation ----------------------------------------
; Append digit=3, pc=999 (= &03E7).  Digit 1's list must be untouched.

                call    set_local_pc_buf_imm
                defb    &E7, &03, &00, &00          ; 999
                ld      a, 3
                call    local_def_append

; Verify digit-3 saw the append.
                call    set_local_pc_buf_imm
                defb    &00, &00, &00, &00
                ld      a, 3
                call    local_find_forward
                call    assert_cf_clear
                call    assert_local_pc_buf_eq_imm
                defb    &E7, &03, &00, &00

; Re-run two digit-1 probes that span the original list (one forward,
; one backward).  Both must still return the original values.

                call    set_local_pc_buf_imm
                defb    &0C, &00, &00, &00          ; ref_pc = 12
                ld      a, 1
                call    local_find_forward
                call    assert_cf_clear
                call    assert_local_pc_buf_eq_imm
                defb    &18, &00, &00, &00          ; 24

                call    set_local_pc_buf_imm
                defb    &32, &00, &00, &00          ; ref_pc = 50
                ld      a, 1
                call    local_find_backward
                call    assert_cf_clear
                call    assert_local_pc_buf_eq_imm
                defb    &18, &00, &00, &00          ; 24

; All assertions passed.
                ret


; -----------------------------------------------------------------------
; set_local_pc_buf_imm — load 4 bytes from the caller's inline literal
; into local_label_pc_buf.
;
; Caller pattern:
;     call set_local_pc_buf_imm
;     defb b0, b1, b2, b3        ; little-endian
;
; Mirrors set_value_buf_imm (test_symbols.asm) but writes into the
; local-label PC buffer.  A separate helper keeps the two buffers
; independent so a test mistake in one suite cannot mask a bug in the
; other.
;
; Clobbers: A, BC, HL.  Preserves DE.
; -----------------------------------------------------------------------
set_local_pc_buf_imm:
                pop     bc                          ; BC = ptr to inline literal
                ld      hl, local_label_pc_buf
                ld      a, (bc)
                ld      (hl), a
                inc     bc
                inc     hl
                ld      a, (bc)
                ld      (hl), a
                inc     bc
                inc     hl
                ld      a, (bc)
                ld      (hl), a
                inc     bc
                inc     hl
                ld      a, (bc)
                ld      (hl), a
                inc     bc                          ; BC = past the literal
                push    bc                          ; restore as return address
                ret


; -----------------------------------------------------------------------
; assert_local_pc_buf_eq_imm — assert local_label_pc_buf equals a
; 4-byte little-endian literal that immediately follows the call in
; the caller's code stream.
;
; Caller pattern:
;     call assert_local_pc_buf_eq_imm
;     defb b0, b1, b2, b3
;
; Mirrors assert_value_buf_eq_imm (test_symbols.asm) but reads from
; local_label_pc_buf.  On mismatch: jp fail.
;
; Clobbers: A, BC, HL.
; -----------------------------------------------------------------------
assert_local_pc_buf_eq_imm:
                pop     bc                          ; BC = ptr to inline literal
                ld      hl, local_label_pc_buf

                ld      a, (bc)
                cp      (hl)
                jp      nz, fail
                inc     bc
                inc     hl

                ld      a, (bc)
                cp      (hl)
                jp      nz, fail
                inc     bc
                inc     hl

                ld      a, (bc)
                cp      (hl)
                jp      nz, fail
                inc     bc
                inc     hl

                ld      a, (bc)
                cp      (hl)
                jp      nz, fail
                inc     bc

                push    bc
                ret
