; loader.asm — enctab.enc reader and header validator.
;
; Loads the entire encoder-table file from disk into ENCTAB's dedicated
; physical RAM page (page 4) via SAMDOS HGTHD (hook 129) + HLOAD
; (hook 130), then validates the magic "ENC1" and version=1 in RAM.
;
; Per https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/specs/2026-05-24-m3-z80-emitter-design.md §2.3.
; Format citation: https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/specs/2026-05-24-m2-encoder-tables-design.md §2.
;
; M5 budget-lever change: ENCTAB no longer lives in section C
; -----------------------------------------------------------
; Pre-M5: ENCTAB was loaded directly into &A000 in section C (alongside
; the assembler code).  This stole 4 KB from the code budget — fine for
; M3 / M4 but blocked M5's compound-operand encoders which push code
; size past 8 KB.  Per the design source `docs/specs/2026-05-27-samdos-
; load-idiom.md` we now use the COMET-style trampoline pattern to load
; ENCTAB into a dedicated physical RAM page (page 4) outside section C.
; This frees &A000-&AFFF for code use, opening 12 KB total for code
; (&8000-&AFFF) instead of 8 KB (&8000-&9FFF).
;
; Memory layout (M5 post-budget-lever):
;   &0000-&3FFF  ROM0 (section A, default)     OR  page 4 = ENCTAB
;                                                  (when LMPR = LMPR_ENCTAB)
;   &4000-&7FFF  page 1 (section B, default)
;                  with trampoline copy at TRAMPOLINE_DST (= &7E00)
;   &8000-&AFFF  assembler code (12 KB; this file + all M3/M4/M5 includes)
;   &B000-&B7FF  IN .tbn buffer (2 KB)
;   &B800-&BFFF  OUT buffer (2 KB)
;   &C000-&C0FF  stack (SP = &C100, grows down into section D)
;   &C100-&FFFF  scratch (OPVAL arrays, eval stack, SYMTAB etc.) —
;                section D RAM
;
;   Physical page 4: ENCTAB (paged into section A on demand via
;                LMPR = LMPR_ENCTAB).
;
; The trampoline lives in section B because HMPR changes paged out
; whatever was in section C/D — so the trampoline's own code must
; live in LMPR-controlled memory (A or B) to remain executable across
; the HMPR change.  See `src/trampoline.asm` for full design notes.
;
; Why HGTHD + HLOAD and not HGFLE + LBYT?
;
; HGFLE+LBYT is the byte-at-a-time API, but a previous spike found the
; first LBYT after HGFLE returning 0x00 instead of the file's first
; payload byte ('E' = 0x45).  The audit
; (`docs/notes/sam-stub-audit.md` §"Hook 158 — HGFLE") asserts HGFLE
; leaves the read pointer past the 9-byte file header — but that
; claim is from static reading of the SAMDOS source, never observed
; in practice.  HSAVE-then-extract diagnostics confirmed all eight
; LBYTs after HGFLE return 0x00.  Root cause unclear; HLOAD instead
; bypasses LBYT entirely and uses ldblk's block-copy loop, which is
; what BASIC's `LOAD CODE` provably exercises every time the
; auto-boot loads "assembler" before we even start running.
;
; UIFA name block convention (from src/sam_io.inc; originally also in
; M0's stub.asm, retired in M7):
;   1 byte   type     (19 = code file, FT_CODE)
;   10 bytes name     space-padded to 10 chars
;   4 bytes  ext      space-padded to 4 chars (unused on SAM)
; samfile v3 addFile() pads the name field the same way
; (samfile.go:948: `name + "          "` truncated to 10 bytes).
;
; IMPORTANT: SAMDOS hook calls (RST 8 + DEFB code) clobber a few caller
; registers via the ROM PTDOS dispatcher and the SAMDOS rfhk epilogue:
;   - B is overwritten with the previous LMPR value by PTDOS step 1
;     (rom-v3.0_annotated-disassembly.txt:12944-12978).  SAMDOS does
;     not restore B from hkbc (`samdos/src/d.s:284-289 bcr` is the
;     normal return path; it only restores the border colour).
;   - E is forced to 0 by `rfhk` (`samdos/src/b.s:475-479`), which does
;     `xor a; ld e, a` before tail-calling bcr.
;   - IX is left at `dchan` after any hook that calls `gtixd` or
;     `fdhr` (HLOAD, HGTHD, HGFLE, HSAVE, ...).  The dispatcher saves
;     caller's IX to `(svhdr)` at `b.s:440` but never restores it.
; Consequence: do NOT use B as a loop counter, or E as a low byte of
; a pointer, across any RST 8 hook.  Re-load IX if you need it
; elsewhere.


; -----------------------------------------------------------------------
; Constants
; -----------------------------------------------------------------------

; HLOAD destination address — must lie in section C per Tech Manual
; (page 211).  The trampoline reprograms HMPR so this address maps to
; ENCTAB_PAGE for the duration of the HLOAD call.  &8000 chosen
; because (a) it satisfies the &8000-&BFFF constraint, (b) it's the
; start of section C so the entire 16 KB section maps to physical
; page 4 cleanly (no offset arithmetic needed).
ENCTAB_LOAD_HL: equ     &8000

STACK_TOP:      equ     &C100          ; SP before any call (grows down into section D)


; -----------------------------------------------------------------------
; load_enctab — load enctab.enc into ENCTAB physical page (page 4) via
;               HGTHD+trampoline_hload, validate header.
;
; Input:  none (precondition: enctab_trampoline_setup has been called
;         to install the trampoline copy at TRAMPOLINE_DST).
; Output: ENCTAB byte 0 sits at ENCTAB_PAGE physical address; reads
;         via section A (after enctab_map_in) at &0000 see the
;         validated table.  HL is undefined.
; On mismatch: jp fail (red border + printer-channel "FAIL" banner,
;              then clean exit → exit 124)
; Clobbers: A, BC, DE, HL, IX (everything except SP).
; -----------------------------------------------------------------------
load_enctab:
; SP must be set by the caller (start: in assembler.asm) before calling here.
; Precondition: SP = STACK_TOP (&C100), set in assembler.asm start:

; -- HGTHD (hook 129): find file, populate SAMDOS's internal svde --------
; HGTHD body: rxhed → ckdrv → gtixd → gtfle → ld de,(difa+35); set 7,d; ...
;             → txhed → ret  (samdos/src/h.s:59-67).
; The crucial side effect is `gtfle` (called inside HGTHD) doing
; `ld (svde), de` (samdos/src/c.s:1486) — this writes the file's first
; track+sector into dchan+2, which the subsequent HLOAD's `dschd`
; consumes (samdos/src/h.s:74-90).  Without HGTHD first, HLOAD reads
; from a stale svde and either fails or loads garbage.
                ld      hl, name_enctab
                call    fill_uifa      ; populate UIFA + IX = &4B00
                rst     8
                defb    HOOK_HGTHD     ; 129 — longjmps on "file not found"

; -- HLOAD via the section-B trampoline ---------------------------------
; The trampoline (installed at TRAMPOLINE_DST by enctab_trampoline_setup)
; reprograms HMPR to ENCTAB_PAGE around the RST 8, so the load writes
; through HL=&8000 land in physical page 4 instead of the page our code
; is currently running from (page 2).  See `src/trampoline.asm` and
; `docs/specs/samdos-file-io.md` for the full pattern.
;
; Read length-mod-16K from the SAMDOS-deposited DIFA header at &4B50+35,
; clearing the `set 7, d` marker left by HGTHD (same pattern as
; load_payload_generic at src/loader.asm:248-255).  The length comes from
; the on-disk truth written by build-disk, so no assembly-time constant
; is needed and the ENCTAB_LEN equ is gone (i7 phase A).
;
; Calling convention (mirrors COMET's `comet.asm:1191-1200`):
;   HL = &8000      (section-C window; satisfies Tech Manual constraint)
;   B  = ENCTAB_PAGE (target physical page for the load)
;   C  = page count from DIFA+34 (0 for any payload < 16 KB)
;   DE = length-mod-16K from DIFA+35 (on-disk truth)
;   IX = UIFA       (already set by fill_uifa above)
                ld      hl, (&4B50 + 35)
                ld      a, h
                and     &7F
                ld      h, a
                ld      e, l
                ld      d, h           ; DE = length-mod-16K
                ld      a, (&4B50 + 34)
                ld      c, a           ; C = page count
                ld      b, ENCTAB_PAGE
                ld      hl, ENCTAB_LOAD_HL
                call    TRAMPOLINE_DST  ; runs the trampoline copy in section B

; -- Validate magic "ENC1" via section-A mapping -------------------------
; The trampoline left HMPR at its original value, so section C is back
; to our code page.  To read ENCTAB now, map page 4 into section A.
;
; pyz80 character literals use double-quoted single chars:
; "E" = ord('E') = 69.  Citation: pyz80 source (pyz80.py:436).
                call    enctab_map_in           ; LMPR=&24 → section A = page 4
                ld      hl, ENCTAB_BASE
                ld      a, (hl)
                cp      "E"
                jp      nz, load_enctab_fail
                inc     hl
                ld      a, (hl)
                cp      "N"
                jp      nz, load_enctab_fail
                inc     hl
                ld      a, (hl)
                cp      "C"
                jp      nz, load_enctab_fail
                inc     hl
                ld      a, (hl)
                cp      "1"
                jp      nz, load_enctab_fail

; -- Validate version = 1 ------------------------------------------------
; Version is u16 LE at bytes 4-5 of the file (ENCTAB_BASE+4, +5).
; Expect version_lo = 1, version_hi = 0.
; Citation: https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/specs/2026-05-24-m2-encoder-tables-design.md §2.
                inc     hl             ; hl = ENCTAB_BASE + 4 (version_lo)
                ld      a, (hl)
                cp      1
                jp      nz, load_enctab_fail
                inc     hl             ; hl = ENCTAB_BASE + 5 (version_hi)
                ld      a, (hl)
                or      a              ; expect 0
                jp      nz, load_enctab_fail

; -- Header validated.  Restore section A to ROM before returning so
; the caller can safely call SAMDOS hooks (load_in_file does so).
; The caller will re-issue enctab_map_in before walking the form
; table.
                call    enctab_map_out
                ret

; -- Mismatch path: restore LMPR before jp fail so the fail handler
; runs in a known LMPR state (defensive — fail itself doesn't access
; ROM, but downstream printer / border-port writes are easier to
; reason about with the default mapping).
load_enctab_fail:
                call    enctab_map_out
                jp      fail


; -----------------------------------------------------------------------
; UIFA name block for "enctab.enc"
; -----------------------------------------------------------------------
; Type 19 = FT_CODE.  Name is "enctab.enc" — exactly 10 characters so no
; trailing spaces needed.  Extension field is 4 spaces (unused on SAM).
; Byte layout: [type:1][name:10][ext:4] = 15 bytes total, as required by
; fill_uifa in src/sam_io.inc.
name_enctab:    defb    19
                defm    "enctab.enc"   ; 10 chars
                defm    "    "         ; 4-char ext (4 spaces)


; -----------------------------------------------------------------------
; load_payload_generic — shared HGTHD + HLOAD-via-trampoline body.
;
; Called via JP from each load_pageN_payload stub.  Each stub provides:
;   HL = UIFA name block (type + 10-char name + 4-char ext, 15 bytes).
;   A  = target physical page (5-bit page number).
; The file lands at the start of the target page (HLOAD destination
; &8000, the bottom of the section-C window).
;
; load_payload_generic_at is the offset-bearing entry: DE supplies the
; HLOAD destination address (&8000..&BFFF), so a payload can land at a
; non-zero offset within the target page — used by load_zx0_payload to
; place the zx0 payload at &8400, beside sysreg_data at &8000 on the
; same page.  The destination is parked in a static (lpg_dest) because
; the SAMDOS hook clobbers E and the DIFA read below needs DE.
;
; The body does:
;   stash DE (dest), push A (saves target page), fill_uifa, HOOK_HGTHD,
;   read DIFA+34/35 for page-count + length-mod-16K, pop A into B,
;   reload HL = dest, then call TRAMPOLINE_DST to execute the section-B
;   HLOAD trampoline.
;
; Input:  HL = name block, A = target page
;         (+ DE = destination address for the _at entry).
;         Precondition: enctab_trampoline_setup has been called.
; Output: file in target page at the destination offset, HMPR restored
;         on return.
; Clobbers: A, BC, DE, HL, IX.
; -----------------------------------------------------------------------
load_payload_generic:
                ld      de, &8000      ; default destination: page start
load_payload_generic_at:
                ld      (lpg_dest), de ; park dest (E dies in the hook)
                push    af             ; save A = target page
                call    fill_uifa
                rst     8
                defb    HOOK_HGTHD     ; longjmps on "file not found"
; Read length-mod-16K from SAMDOS-deposited DIFA header at &4B50+35,
; clearing the `set 7, d` marker left by HGTHD.
                ld      hl, (&4B50 + 35)
                ld      a, h
                and     &7F
                ld      h, a
                ld      e, l
                ld      d, h           ; DE = length-mod-16K
; Read page count from DIFA+34.  Zero for any payload < 16 KB.
                ld      a, (&4B50 + 34)
                ld      c, a           ; C = pages count
                pop     af             ; restore A = target page
                ld      b, a           ; B = target page for HLOAD trampoline
                ld      hl, (lpg_dest) ; section-C window (HLOAD requirement)
                call    TRAMPOLINE_DST
                ret

; Static destination slot for load_payload_generic_at.  Section-C RAM;
; written and read under the caller's HMPR (the trampoline's HMPR swap
; happens strictly inside TRAMPOLINE_DST, after the read).
lpg_dest:       defw    0


; -----------------------------------------------------------------------
; load_page13_payload — PRODUCTION feature (both variants).  HLOAD the
; sysreg lookup-data binary (build/sysreg_data.bin, CODE file "sd13")
; into physical page 13.
;
; Per https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/plans/2026-05-29-m6-closure-release-bytematch.md PR-2.
; Needed by EVERY build (sysreg/dc/tlbi/pstate operands appear in
; production sources).  ORDERING: after enctab_trampoline_setup and,
; in BUILD_TESTS, after test_mem self-tests (page 13 is reused); before
; main_assemble.
; -----------------------------------------------------------------------
load_page13_payload:
                ld      hl, name_sysreg_data
                ld      a, SYSREG_DATA_PAGE
                jp      load_payload_generic


; -----------------------------------------------------------------------
; UIFA name block for "sd13" (sysreg_data, both variants).
;
; The on-disk catalogue entry is created Mac-side by build-disk
; (using samfile) when invoked with the -sysreg-data flag.
; -----------------------------------------------------------------------
name_sysreg_data:
                defb    19
                defm    "sd13      "   ; 10 chars (4 + 6 trailing spaces)
                defm    "    "         ; 4-char ext (unused)


; -----------------------------------------------------------------------
; load_zx0_payload — PRODUCTION feature (both variants).  HLOAD the
; page-13 zx0 payload (compressor &8400 + turbo decoder &8B00; CODE
; file "zx013") into physical page 13 at ZX0_LOAD_ADDR = &8400 — the
; only payload that loads at a non-page-start offset, so it goes via
; load_payload_generic_at.  It co-resides with sysreg_data.bin, which
; occupies the same page at &8000-&83B3 (addresses pinned in
; src/zx0_comm.inc per docs/specs/comment-storage-design.md §5).
;
; Needed by EVERY build: the editor's comment-block compress-at-save /
; decode-at-read paths call ZX0_COMPRESS_ENTRY / ZX0_DECODE_ENTRY via
; paged_call.  ORDERING: identical to load_page13_payload — after
; enctab_trampoline_setup and, in BUILD_TESTS, after the test_mem
; self-tests (page 13 is time-multiplexed with test_mem.bin at boot);
; before the BUILD_TESTS zx0 self-test paged_call and before
; main_assemble.
; -----------------------------------------------------------------------
load_zx0_payload:
                ld      hl, name_zx0
                ld      de, ZX0_LOAD_ADDR
                ld      a, ZX0_PAGE
                jp      load_payload_generic_at


; -----------------------------------------------------------------------
; UIFA name block for "zx013" (zx0 payload, both variants).
;
; The on-disk catalogue entry is created by build-disk when invoked
; with the -zx0 flag.
; -----------------------------------------------------------------------
name_zx0:       defb    19
                defm    "zx013     "   ; 10 chars (5 + 5 trailing spaces)
                defm    "    "         ; 4-char ext (unused)


; -----------------------------------------------------------------------
; load_page15_payload — PRODUCTION feature (both variants).  HLOAD the
; disassembler binary (build/disasm.bin, CODE file "d15") into physical
; page 15.
;
; Per https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/notes/2026-06-07-disassembler-page-placement.md (strand-B PR-3).
; ORDERING: after enctab_trampoline_setup; before the BUILD_TESTS
; disasm self-test paged_call.
; -----------------------------------------------------------------------
load_page15_payload:
                ld      hl, name_disasm
                ld      a, DISASM_PAGE
                jp      load_payload_generic


; -----------------------------------------------------------------------
; UIFA name block for "d15" (disasm, both variants).
;
; The on-disk catalogue entry is created by build-disk when invoked
; with the -disasm flag.
; -----------------------------------------------------------------------
name_disasm:    defb    19
                defm    "d15       "   ; 10 chars (3 + 7 trailing spaces)
                defm    "    "         ; 4-char ext (unused)


if defined(BUILD_TESTS)

; -----------------------------------------------------------------------
; load_test_mem_off_axis — BUILD_TESTS only.  HLOAD the off-axis
;                           test_mem binary into physical page 13 via
;                           the trampoline.
;
; Per plan-PR 3 of https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/notes/2026-05-28-paged-call-architecture.md
; (re-ordered per https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/notes/archive/2026-05-28-session-handoff.md): the
; largest single BUILD_TESTS-only self-test suite (test_mem.asm,
; ~780 B of section-C code) is moved off-axis so the test variant's
; binary tail doesn't spill past &C100 into OPVAL_ARRAY scratch.
;
; The off-axis binary is assembled separately (src/test_mem_offaxis.asm
; with `--importfile=build/assembler.sym`, see Makefile).  Its first
; byte is `run_mem_self_tests`; assemble.asm's BUILD_TESTS block
; reaches it via LMPR-swap-call-restore.
;
; Input:  none (precondition: enctab_trampoline_setup has been called).
; Output: test_mem.bin content sits at physical page 13.  Reads via
;         section A (LMPR = LMPR_TEST_MEM = &2D) see the code at
;         &0000-onward.  LMPR/HMPR restored on return.
; Clobbers: A, BC, DE, HL, IX (everything except SP).
; -----------------------------------------------------------------------
load_test_mem_off_axis:
                ld      hl, name_test_mem
                ld      a, TEST_MEM_PAGE
                jp      load_payload_generic


; -----------------------------------------------------------------------
; UIFA name block for "test_mem" (BUILD_TESTS only).
;
; The on-disk catalogue entry is created Mac-side by build-disk
; (using samfile) when invoked with the -test-mem flag, matching the
; same `tools/build-disk/main.go` pattern used for enctab.enc.
; -----------------------------------------------------------------------
name_test_mem:  defb    19
                defm    "test_mem  "   ; 10 chars (8 + 2 trailing spaces)
                defm    "    "         ; 4-char ext (unused)


; -----------------------------------------------------------------------
; load_enc_fix_payload — BUILD_TESTS only.  HLOAD the encode_inst
;                        fixture data payload into physical page 11
;                        via the trampoline.
;
; i69 lever 3: the enc_fix_table rows + operand streams (~528 B, growing
; with future fixtures) are assembled into a separate binary
; (src/test_encode_inst_payload.asm, org &E100) and deposited here at
; boot.  run_encode_inst_self_tests bulk-copies them via LDIR into
; section-D RAM at ENC_FIX_TABLE_RAM before enctab_map_in.
;
; Structural twin of load_offaxis_cluster below: HGTHD +
; HLOAD-via-trampoline.  The only differences are the file name
; ("enc_fix") and the target page (ENC_FIX_PAGE = 11).
;
; Input:  none (precondition: enctab_trampoline_setup has been called).
; Output: enc_fix_payload.bin content sits at physical page 11.  Reads
;         via section A (LMPR = LMPR_ENC_FIX = &2B) see the data at
;         &E100-onward (the org address).  LMPR/HMPR restored on return.
; Clobbers: A, BC, DE, HL, IX (everything except SP).
; -----------------------------------------------------------------------
load_enc_fix_payload:
                ld      hl, name_enc_fix
                ld      a, ENC_FIX_PAGE
                jp      load_payload_generic


; -----------------------------------------------------------------------
; UIFA name block for "enc_fix" (BUILD_TESTS only).
;
; The on-disk catalogue entry is created by build-disk when invoked
; with the -enc-fix flag.
; -----------------------------------------------------------------------
name_enc_fix:   defb    19
                defm    "enc_fix   "   ; 10 chars (7 + 3 trailing spaces)
                defm    "    "         ; 4-char ext (unused)


; -----------------------------------------------------------------------
; load_offaxis_cluster — BUILD_TESTS only.  HLOAD the off-axis "M5 +
;                        misc encoder" cluster binary into physical
;                        page 12 via the trampoline.
;
; M6 budget-relief PR (2026-05-29).  Structural twin of
; load_test_mem_off_axis above: HGTHD + HLOAD-via-trampoline.  The only
; differences are the file name ("cluster") and the target page
; (TEST_CLUSTER_PAGE = 12).  The payload (~1225 B) holds the relocated
; pc_rel / directives / ror_imm / shifted_reg / extended_reg /
; litpool suites; its entry is cluster_dispatch at section-A &0000.
;
; The off-axis binary is assembled separately (src/test_offaxis_cluster.asm
; with `--importfile=build/assembler.sym`, see Makefile).  The
; BUILD_TESTS block in assembler.asm reaches it via LMPR-swap-call-restore.
;
; Input:  none (precondition: enctab_trampoline_setup has been called).
; Output: cluster binary content sits at physical page 12.  Reads via
;         section A (LMPR = LMPR_TEST_CLUSTER = &2C) see the code at
;         &0000-onward.  LMPR/HMPR restored on return.
; Clobbers: A, BC, DE, HL, IX (everything except SP).
; -----------------------------------------------------------------------
load_offaxis_cluster:
                ld      hl, name_cluster
                ld      a, TEST_CLUSTER_PAGE
                jp      load_payload_generic


; -----------------------------------------------------------------------
; UIFA name block for "cluster" (BUILD_TESTS only).
;
; The on-disk catalogue entry is created Mac-side by build-disk
; (using samfile) when invoked with the -cluster flag.
; -----------------------------------------------------------------------
name_cluster:   defb    19
                defm    "cluster   "   ; 10 chars (7 + 3 trailing spaces)
                defm    "    "         ; 4-char ext (unused)


; -----------------------------------------------------------------------
; load_page14_payload — BUILD_TESTS only.  HLOAD the paged_call self-
;                       test payload into physical page 14 via the
;                       trampoline.
;
; Per plan-PR 1 of https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/notes/2026-05-28-paged-call-architecture.md.
; Structural cousin of load_test_mem_off_axis above (and load_enctab):
; HGTHD + HLOAD-via-trampoline.  The only differences are file name
; ("p14"), target page (PAGED_CALL_TEST_PAGE = 14), and the payload
; being trivially small (3 bytes — `ld a, &42; ret`).
;
; The payload is consumed by run_paged_call_self_tests in
; src/test_paged_call.asm; not used in any production path.
;
; Input:  none (precondition: enctab_trampoline_setup has been called).
; Output: physical page 14 holds the payload (`ld a, &42; ret` stub at
;         offset &8000-onward when HMPR maps page 14 in).  HMPR
;         restored on return.
; Clobbers: A, BC, DE, HL, IX (everything except SP).
; -----------------------------------------------------------------------
load_page14_payload:
                ld      hl, name_page14
                ld      a, PAGED_CALL_TEST_PAGE
                jp      load_payload_generic


; -----------------------------------------------------------------------
; UIFA name block for "p14" (BUILD_TESTS only).
;
; The on-disk catalogue entry is created Mac-side by build-disk
; (using samfile) when invoked with the -paged-call flag.
; -----------------------------------------------------------------------
name_page14:    defb    19
                defm    "p14       "   ; 10 chars (3 + 7 trailing spaces)
                defm    "    "         ; 4-char ext (unused)

endif
