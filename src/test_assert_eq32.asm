; test_assert_eq32.asm — the shared inline-literal assertion helper for
; the boot-time self-test suites.  BUILD_TESTS only.
;
; Extracted from test_slots.asm in the M6 budget-relief PR (2026-05-29)
; so that test_slots.asm could be relocated off-axis (page 12) while this
; helper stays resident in the main binary.  Many suites depend on it:
;   - staying inline (resolve directly): test_symbols, test_sysname,
;     test_extended_reg-style direct callers, ...
;   - relocated off-axis (resolve via --importfile=build/assembler.sym):
;     test_slots, test_pc_rel, test_litpool, test_ror_imm,
;     test_shifted_reg, test_extended_reg, test_mem.
;
; Keeping the DEFINITION in the main binary (section C, HMPR-stable) is
; load-bearing: off-axis callers run under an LMPR swap (section A = the
; off-axis page) but HMPR is unchanged, so a `call assert_eq32_de_hl_imm`
; reaches this section-C address correctly.  The helper's `pop bc; (bc)`
; reads the inline `defb` literal via section A — which IS the off-axis
; page under the swap — so the literal that follows the call (in the
; off-axis page) is read correctly.  See test_offaxis_cluster.asm and
; test_mem_offaxis.asm:36-43 for the full caveat.
;
; Included from src/assembler.asm's BUILD_TESTS include block, BEFORE
; the suites that reference it (so the symbol exists for inline callers
; and is exported into assembler.sym for the off-axis builds).


; -----------------------------------------------------------------------
; assert_eq32_de_hl_imm — assert DEHL equals a 32-bit literal that
; immediately follows the call in the caller's code stream.
;
; Caller pattern:
;     call assert_eq32_de_hl_imm
;     defb b0, b1, b2, b3        ; little-endian: b0 = bit 0..7
;
; The return address on the stack points at the first defb byte.  This
; routine compares DEHL against the four bytes (HL low first, then DE
; high), advances the return address past them on match, and RETs.
; On mismatch: jp fail_at_bc (records the call site, then fails).
;
; Layout reminder: DEHL where HL is bits 0..15 and DE is bits 16..31,
; so the in-memory little-endian byte order is L, H, E, D.
;
; Rationale: inline-literal style reads cleanly at the call site and is
; consistent with the pyz80 convention used for SAMDOS hooks (`rst 8`
; followed by `defb <hook>`).  See src/sam_io.inc lines 87-101.
;
; Clobbers: A, BC.  Preserves DE and HL so the caller's "actual" result
; remains intact (useful when chaining diagnostics).
; -----------------------------------------------------------------------
; -----------------------------------------------------------------------
; fail_at_bc / fail_at_ret — shared diagnostic fail entries (resident, so
; off-axis suites reach them via build/assembler.sym).
;
; fail_at_bc records BC (an inline-literal pointer just past the failing
; assertion's `call`, i.e. within a few bytes of the call site) into
; LAST_FAIL_PC, then falls through to fail.  fail_at_ret records the
; caller's return address from the top of the stack instead — for helpers
; that did NOT pop it (the assert_cf_* checks take no inline literal).
;
; Reached only on the fail path; `fail` halts, so clobbering BC/HL and
; leaving the stack unbalanced is harmless.  See LAST_FAIL_PC in
; src/assembler.asm for why one write per run is never stale.
; -----------------------------------------------------------------------
fail_at_ret:    pop     hl             ; HL = caller's return addr (the site)
                ld      (LAST_FAIL_PC), hl
                jp      fail

fail_at_bc:     ld      (LAST_FAIL_PC), bc
                jp      fail

assert_eq32_de_hl_imm:
                pop     bc             ; BC = pointer to inline literal

                ld      a, (bc)        ; byte 0: low byte of HL
                cp      l
                jp      nz, fail_at_bc
                inc     bc

                ld      a, (bc)        ; byte 1: high byte of HL
                cp      h
                jp      nz, fail_at_bc
                inc     bc

                ld      a, (bc)        ; byte 2: low byte of DE
                cp      e
                jp      nz, fail_at_bc
                inc     bc

                ld      a, (bc)        ; byte 3: high byte of DE
                cp      d
                jp      nz, fail_at_bc
                inc     bc             ; BC now points just past the literal

                push    bc             ; restore as return address
                ret
