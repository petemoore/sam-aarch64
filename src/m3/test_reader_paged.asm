; test_reader_paged.asm — boot-time self-tests for the paged IN reader.
;
; Per docs/plans/2026-05-27-m6-paged-in.md Task 6.  Verifies the runtime
; reader path described in
; docs/specs/2026-05-27-m6-paged-in-design.md.
;
; Preconditions: load_enctab has completed (so LMPR_DEFAULT_RUNTIME is
; live).  Self-tests run BEFORE main_assemble — so IN_BUF (now pages
; 7..10) has NOT been HLOAD'd yet.  We populate it ourselves with a
; tiny synthetic blob via a brief LMPR-bracket-and-write loop.
;
; Coverage:
;   1. page-cross helper (in_normalise_hl): set IN_POS_PAGE =
;      LMPR_IN_BASE, HL = &7FFE.  Call in_normalise_hl.  Assert
;      H < &40 (= &3FFE) and LMPR low 5 bits incremented by 1.
;   2. synthetic record read: stamp a 14-byte ".tbn" blob into page 7
;      via an LMPR=&27 bracket.  Blob = "SA64", version=1, flags=0,
;      name_count=0, then one record [kind=&77][len=&02 &00][&AB &CD].
;      Set IN_END = (page=&27, offset=14).  Call reset_reader_to_in_buf
;      (which tail-calls reader_init), then reader_at_end → not at end,
;      then reader_next_kind.  Assert A=&77, BC=2, (STAGING_BUF)=&AB,
;      (STAGING_BUF+1)=&CD, then reader_at_end → at end (Z=1).
;   3. post-read LMPR check: read port 250 after reader_next_kind,
;      assert &24 (= LMPR_ENCTAB) — proves the reader restored LMPR.
;
; A failure does `jp fail` — same red-border + printer-channel "FAIL"
; banner as every other self-test suite.  We bracket the whole test
; with enctab_map_in / enctab_map_out so we observe the same LMPR
; baseline the encoder sees.
;
; Compile-time included only when BUILD_TESTS is defined (the test
; build variant); the call site in assembler.asm is also gated.


; -----------------------------------------------------------------------
; run_reader_paged_self_tests — entry point.
;
; Input:  none.  Output: returns on success.  On any mismatch: jp fail.
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
run_reader_paged_self_tests:

; The reader's invariant is "called with LMPR = LMPR_ENCTAB; returns
; with LMPR = LMPR_ENCTAB".  Establish that baseline.
                call    enctab_map_in

; ----- (1) page-cross helper test ---------------------------------------
;
; Synthesise the state "we just added some BC to HL, so HL now points
; one byte past the end of section A (= &7FFE)" and call
; in_normalise_hl.  Expected: H is decremented by &40 (= &3FFE) and
; LMPR low 5 bits are bumped by 1 (LMPR_IN_BASE = &27 → &28).
                ld      a, LMPR_IN_BASE
                ld      (IN_POS_PAGE), a
                ld      a, &27
                out     (250), a            ; sync LMPR to IN_POS_PAGE
                ld      hl, &7FFE
                call    in_normalise_hl

                ld      a, h
                cp      &3F
                jp      nz, reader_paged_fail_with_lmpr
                ld      a, l
                cp      &FE
                jp      nz, reader_paged_fail_with_lmpr
                in      a, (250)
                cp      &28                  ; expect bumped LMPR low 5 bits
                jp      nz, reader_paged_fail_with_lmpr

; Restore LMPR_ENCTAB before the next test (which calls
; reset_reader_to_in_buf → reader_init → enctab_map_in at end; we
; don't strictly need LMPR_ENCTAB here, but the bracket invariant is
; cleaner).
                call    enctab_map_in

; ----- (2) synthetic record fetch ---------------------------------------
;
; Stamp a 14-byte blob into physical page 7 (= LMPR_IN_BASE) at offset
; 0.  We do this from the test code itself, using a brief LMPR bracket
; so the source bytes (in section C) and the dest (section A under
; LMPR_IN_BASE) are both reachable.  The bytes-to-stamp live in
; section C, which is HMPR-controlled, so section A under LMPR=&27
; doesn't displace them.  The blob is:
;
;   offset 0..3   : "SA64"
;   offset 4..5   : version u16 LE = 0x0001
;   offset 6..7   : flags u16 LE = 0x0000
;   offset 8..9   : name_count u16 LE = 0x0000
;   offset 10     : record kind = &77
;   offset 11..12 : payload length u16 LE = 2
;   offset 13..14 : payload bytes &AB, &CD
;
; Total = 15 bytes; IN_END_OFFSET = 15.
;
; Note: writes go to section A under LMPR_IN_BASE, so the page-7
; physical bytes are clobbered (no other test relies on those bytes
; pre-main_assemble — main_assemble's load_in_file overwrites them
; anyway).

                in      a, (250)
                ld      (reader_paged_lmpr_save), a

                ld      a, LMPR_IN_BASE
                out     (250), a            ; section A = page 7 (IN[0])

                ld      hl, reader_paged_synthetic_tbn
                ld      de, 0               ; section-A dest = IN[0] offset 0
                ld      bc, reader_paged_synthetic_tbn_end - reader_paged_synthetic_tbn
                ldir

                ld      a, (reader_paged_lmpr_save)
                out     (250), a            ; restore LMPR_ENCTAB

; Wire IN_END to point past our synthetic blob.
                ld      a, LMPR_IN_BASE
                ld      (IN_END_PAGE), a
                ld      hl, reader_paged_synthetic_tbn_end - reader_paged_synthetic_tbn
                ld      (IN_END_OFFSET), hl

; Reset reader to start; this tail-calls reader_init which validates
; the magic, walks the name table (count = 0 → no walk), and
; restores LMPR_ENCTAB.
                call    reset_reader_to_in_buf

; Verify LMPR was restored to LMPR_ENCTAB by reader_init.
                in      a, (250)
                cp      LMPR_ENCTAB
                jp      nz, reader_paged_fail_with_lmpr

; Reader should NOT be at end yet (one record still to consume).
                call    reader_at_end
                jp      z, reader_paged_fail_with_lmpr

; Fetch the record.  Expected: A=&77, BC=2, HL=STAGING_BUF,
; STAGING_BUF[0]=&AB, STAGING_BUF[1]=&CD, LMPR=LMPR_ENCTAB on return.
                call    reader_next_kind

                cp      &77
                jp      nz, reader_paged_fail_with_lmpr
                ld      a, b
                or      a
                jp      nz, reader_paged_fail_with_lmpr
                ld      a, c
                cp      2
                jp      nz, reader_paged_fail_with_lmpr
; HL = STAGING_BUF expected.  Verify by computing the delta in DE:
;   DE := HL - STAGING_BUF.  Expected: DE = 0.
                ld      de, STAGING_BUF
                or      a                            ; clear CF
                sbc     hl, de
                ld      a, h
                or      l
                jp      nz, reader_paged_fail_with_lmpr

                ld      hl, STAGING_BUF
                ld      a, (hl)
                cp      &AB
                jp      nz, reader_paged_fail_with_lmpr
                inc     hl
                ld      a, (hl)
                cp      &CD
                jp      nz, reader_paged_fail_with_lmpr

; ----- (3) post-read LMPR check ----------------------------------------
                in      a, (250)
                cp      LMPR_ENCTAB
                jp      nz, reader_paged_fail_with_lmpr

; After consuming the only record, the reader should be at end.
                call    reader_at_end
                jp      nz, reader_paged_fail_with_lmpr

; ----- Cleanup --------------------------------------------------------
; Zero IN_END_*/IN_POS_* so main_assemble's load_in_file starts from a
; clean slate (it sets them itself, but no harm being explicit).
                xor     a
                ld      (IN_POS_PAGE), a
                ld      (IN_END_PAGE), a
                ld      hl, 0
                ld      (IN_POS_OFFSET), hl
                ld      (IN_END_OFFSET), hl

; LMPR is LMPR_ENCTAB; exit the encoder window so the caller (in
; assembler.asm) sees LMPR back at LMPR_DEFAULT_RUNTIME.
                jp      enctab_map_out          ; tail-call


; -----------------------------------------------------------------------
; Failure paths.
;
; LMPR may be in any state at the point of failure (page-cross test
; leaves it at the synthetic bumped value; reader-fetch test leaves it
; at LMPR_ENCTAB).  We restore via the standard enctab_map_out path so
; the printer-channel banner runs under the default LMPR.
; -----------------------------------------------------------------------
reader_paged_fail_with_lmpr:
                call    enctab_map_out
                jp      fail


; -----------------------------------------------------------------------
; Synthetic .tbn blob — 15 bytes.  Used by the record-fetch test above.
; -----------------------------------------------------------------------
reader_paged_synthetic_tbn:
                defm    "SA64"              ; magic (4 bytes)
                defw    1                   ; version u16 LE = 1
                defw    0                   ; flags u16 LE = 0
                defw    0                   ; name_count u16 LE = 0
                defb    &77                 ; record kind
                defw    2                   ; record payload length
                defb    &AB, &CD            ; payload
reader_paged_synthetic_tbn_end:

reader_paged_lmpr_save: defb    0
