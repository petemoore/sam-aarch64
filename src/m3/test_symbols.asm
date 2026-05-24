; test_symbols.asm — boot-time self-tests for the M4 symbol table.
;
; Per docs/specs/2026-05-24-m4-symbols-multipass-design.md §2.2.
;
; These run BEFORE load_enctab (see assembler.asm start:) so they
; exercise the symbol-table routines against hard-coded ids/addresses
; with no disk I/O.  Any assertion failure does `jp fail` — same
; red-border + printer-channel "FAIL" banner path that the slot
; self-tests use.
;
; Test coverage:
;   1. After symbol_table_init, lookup of any id returns CF=1.
;   2. Insert 5 (id, address) pairs, including TWO bucket-collision
;      pairs (so the chain logic is actually exercised):
;        id=0x0001 → bucket 01, addr 0x11111111
;        id=0x0101 → bucket 01 chained, addr 0x22222222
;        id=0x0042 → bucket 42, addr 0x33333333
;        id=0x0142 → bucket 42 chained, addr 0x44444444
;        id=0x0200 → bucket 00, addr 0x55555555
;      Lookup each of the 5 and assert CF=0 with correct address.
;   3. Lookup id=0x0099 (never inserted, bucket 99 untouched) → CF=1.
;
; Not exercised (same caveat as test_slots.asm header):
;   - Duplicate insert  → jp fail.
;   - Overflow exhaust  → jp fail.
;   These can't be tested without halting the program; covered by
;   inspection.


; -----------------------------------------------------------------------
; run_symbol_table_self_tests — entry point called from assembler.asm
; start: (between run_slot_self_tests and load_enctab).
;
; Input:  none.
; Output: returns to caller on success.  On any mismatch: jp fail.
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
run_symbol_table_self_tests:

; -- (1) Init then lookup any id → CF=1 --------------------------------
                call    symbol_table_init

                ld      hl, &0001
                call    symbol_lookup
                call    assert_cf_set

                ld      hl, &00AB
                call    symbol_lookup
                call    assert_cf_set

; -- (2) Insert 5 ids with bucket collisions ---------------------------

; Insert id=0x0001, address=0x11111111 (bucket 01 fast path).
                call    set_value_buf_imm
                defb    &11, &11, &11, &11
                ld      hl, &0001
                call    symbol_insert

; Insert id=0x0101, address=0x22222222 (bucket 01 chain — index 0).
                call    set_value_buf_imm
                defb    &22, &22, &22, &22
                ld      hl, &0101
                call    symbol_insert

; Insert id=0x0042, address=0x33333333 (bucket 42 fast path).
                call    set_value_buf_imm
                defb    &33, &33, &33, &33
                ld      hl, &0042
                call    symbol_insert

; Insert id=0x0142, address=0x44444444 (bucket 42 chain — index 1).
                call    set_value_buf_imm
                defb    &44, &44, &44, &44
                ld      hl, &0142
                call    symbol_insert

; Insert id=0x0200, address=0x55555555 (bucket 00 fast path).
                call    set_value_buf_imm
                defb    &55, &55, &55, &55
                ld      hl, &0200
                call    symbol_insert

; -- Lookup each id and verify hit + address ---------------------------

; Lookup id=0x0001 → 0x11111111.
                ld      hl, &0001
                call    symbol_lookup
                call    assert_cf_clear
                call    assert_value_buf_eq_imm
                defb    &11, &11, &11, &11

; Lookup id=0x0101 → 0x22222222 (this is the bucket-01 CHAINED entry —
; exercises the chain-walk from the first bucket slot into overflow).
                ld      hl, &0101
                call    symbol_lookup
                call    assert_cf_clear
                call    assert_value_buf_eq_imm
                defb    &22, &22, &22, &22

; Lookup id=0x0042 → 0x33333333.
                ld      hl, &0042
                call    symbol_lookup
                call    assert_cf_clear
                call    assert_value_buf_eq_imm
                defb    &33, &33, &33, &33

; Lookup id=0x0142 → 0x44444444 (bucket-42 CHAINED entry).
                ld      hl, &0142
                call    symbol_lookup
                call    assert_cf_clear
                call    assert_value_buf_eq_imm
                defb    &44, &44, &44, &44

; Lookup id=0x0200 → 0x55555555 (different bucket, no collision).
                ld      hl, &0200
                call    symbol_lookup
                call    assert_cf_clear
                call    assert_value_buf_eq_imm
                defb    &55, &55, &55, &55

; -- (3) Lookup an un-inserted id → CF=1 -------------------------------
; id=0x0099 hashes to bucket 99, which has never been touched.
                ld      hl, &0099
                call    symbol_lookup
                call    assert_cf_set

; Extra robustness: lookup id whose LOW byte collides with a populated
; bucket but whose HIGH byte differs from every chained entry there.
; id=0x0301 → bucket 01, ids in chain are 0x0001 and 0x0101 — miss.
                ld      hl, &0301
                call    symbol_lookup
                call    assert_cf_set

; All assertions passed.
                ret


; -----------------------------------------------------------------------
; set_value_buf_imm — load 4 bytes from the caller's inline literal
; into symbol_value_buf.
;
; Caller pattern:
;     call set_value_buf_imm
;     defb b0, b1, b2, b3        ; little-endian
;
; Used to set up the address operand before calling symbol_insert.
; Mirrors the inline-literal style of assert_eq32_de_hl_imm in
; test_slots.asm so the test source reads top-to-bottom.
;
; Clobbers: A, BC, HL.  Preserves DE.
; -----------------------------------------------------------------------
set_value_buf_imm:
                pop     bc                          ; BC = ptr to inline literal
                ld      hl, symbol_value_buf
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
; assert_value_buf_eq_imm — assert symbol_value_buf equals a 4-byte
; little-endian literal that immediately follows the call in the
; caller's code stream.
;
; Caller pattern:
;     call assert_value_buf_eq_imm
;     defb b0, b1, b2, b3
;
; Mirrors assert_eq32_de_hl_imm (test_slots.asm) but reads the
; "actual" bytes from symbol_value_buf instead of DEHL.  On mismatch:
; jp fail.
;
; Clobbers: A, BC, HL.
; -----------------------------------------------------------------------
assert_value_buf_eq_imm:
                pop     bc                          ; BC = ptr to inline literal
                ld      hl, symbol_value_buf

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


; -----------------------------------------------------------------------
; assert_cf_set / assert_cf_clear — assert the carry-flag state on
; entry.  Failure path: jp fail.
;
; These take NO inline literal, so callers just do `call assert_cf_*`
; with no defb suffix.  Used to validate symbol_lookup's CF semantics.
;
; Clobbers: A.
; -----------------------------------------------------------------------
assert_cf_set:
                jp      nc, fail
                ret

assert_cf_clear:
                jp      c, fail
                ret
