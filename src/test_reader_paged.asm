; test_reader_paged.asm — boot-time self-tests for the paged IN reader.
;
; Per https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/plans/2026-05-27-m6-paged-in.md Task 6.  Verifies the runtime
; reader path described in
; docs/specs/paged-in-design.md.
;
; Preconditions: load_enctab has completed (so LMPR_DEFAULT_RUNTIME is
; live).  Self-tests run BEFORE main_assemble — so the IN pool pages have
; NOT been HLOAD'd yet.  We populate one ourselves with a tiny synthetic
; blob via a brief LMPR-bracket-and-write loop.
;
; Coverage:
;   1. page-cross helper (in_normalise_hl): set IN_POS_PAGE =
;      LMPR_IN_BASE, HL = &7FFE.  Call in_normalise_hl.  Assert
;      H < &40 (= &3FFE) and LMPR low 5 bits incremented by 1.
;   2. synthetic record read at a NON-default base: stamp a 21-byte ".tbn"
;      blob into page 8 (a deliberately non-7 base) via an LMPR=&28 bracket,
;      seed IN_BASE_LMPR = page 8.  Blob = "SA64", version=2, flags=0,
;      editor_region_offset=21, label_count=0, local_count=0, then one
;      record [kind=&77][len=&02 &00][&AB &CD].  Call
;      reset_reader_to_in_buf (which tail-calls reader_init); reader_init
;      derives IN_END = (page=&28, offset=21) from the section index — proving
;      the reader walks from IN_BASE_LMPR, not a hardcoded page 7.
;      Then reader_at_end → not at end, then reader_next_kind.  Assert
;      A=&77, BC=2, (STAGING_BUF)=&AB, (STAGING_BUF+1)=&CD, then
;      reader_at_end → at end (Z=1).
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

; ----- (2) synthetic record fetch at a NON-default base -----------------
;
; Stamp a 21-byte blob into physical page 8 — a DELIBERATELY non-7 base.
; load_in_file allocates the IN run from the pool and sets IN_BASE_LMPR to
; wherever it landed (page 7 on a 256 KB SAM, up to the 16..31 run on 512 KB);
; the reader must walk from IN_BASE_LMPR, not a hardcoded page 7.  The base-7
; release-paged regression cannot catch a stale "hardcoded 7" because there the
; base IS 7 — so this self-test pins a non-7 base (page 8, which exists on a
; 256 KB machine) to prove reset_reader/reader_init honour IN_BASE_LMPR (i23).
; We stamp from the test code via a brief LMPR bracket so the source bytes (in
; section C) and the dest (section A under the page-8 LMPR) are both
; reachable.  The blob is:
;
;   offset 0..3   : "SA64"
;   offset 4..5   : version u16 LE = 0x0002
;   offset 6..7   : flags u16 LE = 0x0000
;   offset 8..11  : editor_region_offset u32 LE = 21  (section index, M8 / i39b-2)
;   offset 12..13 : label_count u16 LE = 0x0000  (compact `.tbn` v2 header table)
;   offset 14..15 : local_count u16 LE = 0x0000  (compact `.tbn` v2 header table)
;   offset 16     : record kind = &77
;   offset 17..18 : payload length u16 LE = 2
;   offset 19..20 : payload bytes &AB, &CD
;
; Total = 21 bytes; reader_init derives IN_END_OFFSET = 21 from the
; section index (the editor region after the record is empty).
;
; Note: writes go to section A under the page-8 LMPR, so the page-8
; physical bytes are clobbered (no other test relies on those bytes
; pre-main_assemble — main_assemble's load_in_file overwrites whatever IN
; pages it allocates anyway).
READER_TEST_BASE:       equ     &20 + 8     ; LMPR for page 8 (RAM0 | 8) — a non-7
                                            ;   base with no boot payload (page 11 is
                                            ;   ENC_FIX_PAGE; 4=ENCTAB, 13-15=payloads;
                                            ;   5..12 are free pool pages here — the
                                            ;   IN/OUT runs aren't allocated until
                                            ;   main_assemble)

                in      a, (250)
                ld      (reader_paged_lmpr_save), a

                ld      a, READER_TEST_BASE
                out     (250), a            ; section A = page 8 (IN[0])

                ld      hl, reader_paged_synthetic_tbn
                ld      de, 0               ; section-A dest = IN[0] offset 0
                ld      bc, reader_paged_synthetic_tbn_end - reader_paged_synthetic_tbn
                ldir

                ld      a, (reader_paged_lmpr_save)
                out     (250), a            ; restore LMPR_ENCTAB

; Pre-set IN_END to a deliberately-wrong value (page 7, the OLD hardcoded base)
; so the test proves reader_init DERIVES IN_END from the section index
; (editor_region_offset = 21) over the page-8 base, rather than relying on a
; caller-supplied bound or a stale page-7 assumption.
                ld      a, LMPR_IN_BASE
                ld      (IN_END_PAGE), a
                ld      hl, 0
                ld      (IN_END_OFFSET), hl

; This self-test stamps its synthetic blob into page 8 directly (it does not
; run load_in_file, which is what normally allocates the IN run and sets
; IN_BASE_LMPR).  So seed IN_BASE_LMPR = page 8 by hand, the way load_in_file
; would for a run that landed there, before the reader reads it.
                ld      a, READER_TEST_BASE
                ld      (IN_BASE_LMPR), a

; Reset reader to start; this tail-calls reader_init which validates the
; magic, reads the section index → IN_END = (READER_TEST_BASE, 21), parses the
; empty header tables, and restores LMPR_ENCTAB.
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
; Synthetic .tbn blob — 21 bytes (compact `.tbn` v2, editor-region split).
; Used by the record-fetch test above.  The editor_region_offset section
; index (21) points one byte past the only record, so the editor region is
; empty and reader_init sets IN_END to that boundary.
; -----------------------------------------------------------------------
reader_paged_synthetic_tbn:
                defm    "SA64"              ; magic (4 bytes)
                defw    2                   ; version u16 LE = 2 (compact `.tbn` v2)
                defw    0                   ; flags u16 LE = 0
                defw    21                  ; editor_region_offset u32 LE = 21 (end of records)
                defw    0
                defw    0                   ; label_count u16 LE = 0 (header table)
                defw    0                   ; local_count u16 LE = 0 (header table)
                defb    &77                 ; record kind
                defw    2                   ; record payload length
                defb    &AB, &CD            ; payload
reader_paged_synthetic_tbn_end:

reader_paged_lmpr_save: defb    0
