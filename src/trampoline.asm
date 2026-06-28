; trampoline.asm — paged-RAM trampoline machinery for HLOAD and ENCTAB
; runtime reads.
;
; Per docs/specs/samdos-file-io.md (the design source).
;
; Purpose
; -------
; The DOS HLOAD hook (SAMDOS 2 / B-DOS) requires HL ∈ &8000..&BFFF
; (section C) and the
; destination physical page mapped via HMPR.  When the caller wants the
; file to land somewhere OTHER than the caller's own current section-C
; page (i.e. where the running code lives), the canonical SAM pattern
; is a "trampoline" that temporarily reprograms HMPR around the RST 8.
; COMET (a contemporary SAM Coupé assembler) uses exactly this pattern;
; we follow it verbatim modulo small adjustments forced by our memory
; layout.
;
; Why does the trampoline live OUTSIDE section C?
; -----------------------------------------------
; HMPR controls section C (and, automatically, section D = HMPR+1).
; The trampoline issues `out (251), a` to swing section C onto the
; target page — but the trampoline's OWN instruction stream is being
; fetched from somewhere in the 64 KB address map.  If the trampoline
; lived in section C, the very next instruction fetch after the
; `out (251), a` would come from the NEW physical page (the load
; target), which holds whatever undefined bytes happen to be there —
; a guaranteed crash.
;
; Therefore the trampoline body must run from section A (LMPR low 5
; bits) or section B (LMPR low 5 bits + 1).  Both are LMPR-controlled
; and unaffected by HMPR changes.
;
; Why we copy into section B at TRAMPOLINE_DST instead of section A
; -----------------------------------------------------------------
; Section A normally holds ROM0 (LMPR bit 5 = 0).  `RST 8` jumps to
; &0008 (in ROM0), so DOS hooks (SAMDOS 2 / B-DOS) REQUIRE ROM0 to be in
; section A
; during dispatch.  Replacing section A with the trampoline page would
; break every subsequent DOS hook call.  Section B (page 1 = BASIC
; system page in the default LMPR=&00 state) is free for us to
; overwrite once we've finished with BASIC — which we have, by the
; time `enctab_trampoline_setup` runs (BASIC's only job was the AUTO
; `LOAD CODE 32768; CALL 32768`; we never call back into it).
;
; The 14-byte trampoline body fits easily near the top of section B
; (TRAMPOLINE_DST = &7E00) without disturbing anything we care about.
;
; Stack handling — static-save-in-section-B pattern
; --------------------------------------------------
; The design note (`docs/specs/samdos-file-io.md`
; §"Pre-built trampoline reference") shows a `push af / ... / pop af`
; pair BRACKETING the HMPR change (push BEFORE the change, pop AFTER).
; That works only if SP points into LMPR-stable memory (section A or
; B).  In our memory layout SP points into section D (HMPR+1; the
; default stack at &C100 grows down into section D) — and section D
; IS paged out by the HMPR change.  Push-before-pop-after would write
; to one physical page and read from another, returning garbage.
;
; COMET avoids this by switching SP into a section-A/B scratch area
; via `LD SP, (sproom)` at the call site (`comet.asm:1189`).  We avoid
; it differently: we save the HMPR byte in a STATIC LOCATION in
; section B, right next to the trampoline body.  Section B is
; LMPR-stable across the trampoline's HMPR change, so the byte
; survives.
;
; We must NOT save it in a register, because:
;   - B/C are clobbered by ROM PTDOS (B = LMPR save, C = port number)
;   - HL is clobbered by ROM PTDOS (ADD HL, SP)
;   - A, D, E, F are clobbered by SAMDOS hook body or RFHK epilogue
;   - IX is left at SAMDOS's `dchan` (the dispatcher saves it but
;     does not restore)
;   - IY happens to survive, but we don't want to depend on undocumented
;     behaviour
;
; We also must NOT save it via stack push BEFORE the HMPR change and
; pop AFTER, because that crosses the HMPR window (push goes to
; orig+1 physical, pop reads target+1 physical — different memory).
;
; The static-save approach addresses these issues cleanly: one byte
; of section B holds the original HMPR across the RST 8.
;
; Position independence + the static-save absolute address
; --------------------------------------------------------
; The trampoline body uses absolute addressing for the static save
; (`ld (HMPR_SAVE), a` / `ld a, (HMPR_SAVE)`).  HMPR_SAVE is defined
; as a SECTION-B address (TRAMPOLINE_DST + offset).  So when pyz80
; assembles the source-side `trampoline_body` (in section C), the
; absolute address baked into the LD instructions ALREADY references
; section B.  After LDIR copies the body to TRAMPOLINE_DST, the COPY
; reads/writes the section-B byte at HMPR_SAVE — independent of
; where the COPY itself happens to live, and independent of HMPR
; (which controls section C/D only).
;
; All other instructions in the body use register-relative addressing
; (caller-supplied registers, RST 8's stack push), so they're
; trivially position-independent.
;
; ENCTAB runtime access — LMPR-swap pattern
; -----------------------------------------
; Once ENCTAB is loaded into physical page 4, the encoder + form_lookup
; need to READ from it during the assemble passes.  Approach: map page
; 4 into section A by setting LMPR = LMPR_ENCTAB (&24 = RAM0 bit + low
; 5 bits = 4).  The encoder reads ENCTAB at base address &0000 via
; section A.
;
; This is bracketed at a coarse grain in `main_assemble`:
; `enctab_map_in` is called after `load_in_file` (which needs ROM in
; section A for its RST 8), and `enctab_map_out` is called at the end
; of main_assemble before save_out_file.  Between those calls the
; encoder + form_lookup may read ENCTAB freely via direct addressing.
;
; LMPR = LMPR_ENCTAB means section A = page 4 (ENCTAB, RAM-replaces-
; ROM).  The trampoline copy at TRAMPOLINE_DST lives in section B
; under LMPR = LMPR_DEFAULT; with LMPR = LMPR_ENCTAB section B is a
; different page, so the trampoline copy is INVISIBLE — but we never
; call it during the LMPR = LMPR_ENCTAB window, so this is fine.
;
; The IN and OUT buffers extend this same paging pattern: IN is read
; per-record via an LMPR bracket into section A (reader.asm;
; docs/specs/paged-in-design.md), and OUT is written per-byte via an
; LMPR bracket into section B (emit_byte in encoder.asm;
; docs/specs/paged-out-design.md).  Both live in page-pool runs
; allocated at assemble time (src/pagepool.asm).


; -----------------------------------------------------------------------
; Page allocation constants.
; -----------------------------------------------------------------------
;
; SAM Coupé physical-page layout on a 256 KB machine after SAMDOS load
; (Tech Manual v3.0, page 57, "PAGE ALLOCATION TABLE" at &5100):
;
;   pages 0..3   BASIC program        (40H)
;   pages 4..12  Unused              (00H)   <-- we pick from here
;   page  13     DOS                 (60H)
;   pages 14..15 Screen              (C0H)
;   pages 16+    FFH                 (non-existent on 256 KB)
;
; We pick page 4 for ENCTAB: lowest free page, far from DOS / screen /
; BASIC defaults, no risk of stomping anything SAMDOS uses internally.
; (Tech Manual §"PAGE ALLOCATION TABLE": "Free pages can be used as
; temporary workspaces, provided you are sure that nothing is going
; to overwrite the page while you are using it.  (Interrupts do not
; do this, but the DOS might).")  Since our DOS use is HGTHD / HLOAD
; / HSAVE only — none of which use page 4 — this is safe.
;
; A formal ALLOCT update (marking page 4 as 20H "utilities" or
; similar) is NOT required for our case because BASIC will never run
; again after our `di; halt` exit.  If a future milestone returns
; control to BASIC, ALLOCT bookkeeping will be needed.

ENCTAB_PAGE:    equ     4              ; physical page holding ENCTAB
ENCTAB_BASE:    equ     &0000          ; section-A base address when
                                       ; LMPR = LMPR_ENCTAB (page 4
                                       ; mapped in via RAM0 bit + low
                                       ; 5 bits = 4)

; Note: there's no compile-time LMPR_DEFAULT constant.  Section B's
; page depends on what BASIC's CALL left LMPR at, and that varies
; (commonly &1F on 256 KB Coupé → section B = page 0 = BASIC sys
; page).  We capture the real boot value at startup into
; LMPR_DEFAULT_RUNTIME and use that for restoration.  See
; assembler.asm `start:`.
LMPR_ENCTAB:    equ     &20 + ENCTAB_PAGE
                                       ; RAM0 bit (=&20) + page 4 →
                                       ; section A = page 4.  We
                                       ; deliberately use a known
                                       ; absolute LMPR value for
                                       ; enctab_map_in, because the
                                       ; bit-5 RAM0 toggle has to
                                       ; happen unconditionally (the
                                       ; boot LMPR has bit 5 = 0; we
                                       ; need it = 1 to swap ROM out).

; OUT buffer: no compile-time page constants.
;
; Per docs/specs/paged-out-design.md, OUT is a contiguous page-pool run
; allocated at assemble time (reset_out_buffer in main_loop.asm); the
; runtime state (OUT_RUN_BASE / OUT_RUN_PAGES / OUT_LMPR_CUR / OUT_PC /
; OUT_LEN) lives in main_loop.asm.  HSAVE at end of pass 2 reads via
; section C with UIFA[31] = OUT_RUN_BASE, HMPR auto-paging at &C000
; across the run (see docs/specs/samdos-file-io.md).


; IN-buffer paged-input constants.
;
; Per docs/specs/paged-in-design.md.  IN lives in physical
; pages 7..12 (6 contiguous pages = 96 KB ceiling, raised 2026-05-28
; from the original 4-page / 64 KB allocation to fit spectrum4's 88 KB
; stripped release.tbn), HLOAD'd once at startup.  Each
; reader_next_kind brackets a brief LMPR=&27-derived window mapping the
; current IN page into section A, stages the record into STAGING_BUF in
; section D, and restores LMPR_ENCTAB before returning to the caller.
; Pages 7..12 are all in the "00H unused" range per the Tech Manual
; page-allocation table cited at trampoline.asm:188-208 above.

IN_BASE_PAGE:   equ     7              ; first physical page of IN
LMPR_IN_BASE:   equ     &20 + IN_BASE_PAGE
                                       ; = &27; section A = page 7 (IN[0])


; Off-axis test-payload page (BUILD_TESTS only — see plan-PR 3 of
; https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/notes/2026-05-28-paged-call-architecture.md and the brief at
; https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/plans/2026-05-28-plan-pr3-test-corpus-off-axis.md).
;
; Page 13 is reserved by https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/notes/2026-05-28-memory-layout-brainstorm.md
; §3 for "disasm aux + sysreg DB + rewrite-hint table".  That role
; lands in a later strand-B PR; until then plan-PR 3 reuses page 13
; as the off-axis test payload page.  When PR #50 is salvaged and the
; disasm-aux infrastructure lands, the test payload will move to a
; dedicated page (or be migrated to paged_call) — see the §6 PR
; sequence in the paged-call architecture doc.
;
; LMPR_TEST_MEM = &20 | 13 = &2D puts section A = page 13.  Section B
; under this LMPR = page 14 (free).  HMPR is unchanged, so section
; C/D still map to the assembler code + scratch — calls FROM off-axis
; test code TO production code (encode_mem_word, assert_eq32_de_hl_imm,
; fail) work as if the swap had never happened.
TEST_MEM_PAGE:  equ     13
LMPR_TEST_MEM:  equ     &20 + TEST_MEM_PAGE         ; = &2D


; Off-axis "M5 + misc encoder" cluster page (BUILD_TESTS only — M6
; budget-relief PR, 2026-05-29; see src/test_offaxis_cluster.asm).
;
; Holds the relocated pc_rel / directives / ror_imm / shifted_reg /
; extended_reg / litpool self-test suites (1225 B), HLOADed at boot and
; invoked once via an LMPR swap (cluster_dispatch at section-A &0000).
;
; Page 11: encode_inst fixture data payload (BUILD_TESTS_ENCODE only —
; the enc-tests boot variant, i234; page assignment from i69 lever 3).
;
; The enc_fix_table rows and operand-stream data that feed
; run_encode_inst_self_tests are assembled into a separate binary
; (src/test_encode_inst_payload.asm, org &E100) and HLOADed here at
; boot.  At the start of run_encode_inst_self_tests the entire payload
; is bulk-copied via LDIR from section-A page 11 into section-D RAM
; at ENC_FIX_TABLE_RAM = &E100, then the driver loop runs unchanged
; against the section-D copy.  This keeps the ~528 B + future growth
; out of section C, banking that headroom against the &C000 cliff.
;
; Page 11 is free during the entire boot-self-test phase.  The IN
; .tbn buffer occupies pages 7..12 only during main_assemble, which
; runs after all boot self-tests are complete.  Page 12 holds the
; off-axis cluster (TEST_CLUSTER_PAGE below), so page 11 is the next
; free page before it.
;
; LMPR_ENC_FIX = &20 | 11 = &2B puts section A = page 11.  The LDIR
; source is HL = &0000 (start of section A).  Section B under this
; LMPR = page 12 (the cluster page), but the LDIR touches only
; section A; the copy completes before enctab_map_in, so ENCTAB
; (page 4) is not involved.
if defined(BUILD_TESTS_ENCODE)
ENC_FIX_PAGE:       equ     11
LMPR_ENC_FIX:       equ     &20 + ENC_FIX_PAGE          ; = &2B
; ENC_FIX_TABLE_RAM: section-D destination for the LDIR bulk-copy.
; Overlaps LITPOOL_PC_MAP (&E100..&E27F), which is safe because the
; bulk-copy runs before main_assemble initialises the litpool.
ENC_FIX_TABLE_RAM:  equ     &E100
; ENC_FIX_PAYLOAD_LEN is imported at build time from enc_fix_payload.sym
; (see Makefile: assembler target depends on enc-fix-payload and uses
; --importfile=$(BUILD)/enc_fix_payload.sym).
endif

; Page 12: a free page per the Tech Manual PAGE ALLOCATION TABLE (pages
; 4..12 unused).  At the boot-self-test phase it holds nothing — IN is
; not HLOADed into pages 7..12 until main_assemble, long after the
; cluster self-tests have run to completion (same time-multiplex the
; test_mem/page-13 and IN/pages-7..12 payloads already rely on).
;
; LMPR_TEST_CLUSTER = &20 | 12 = &2C puts section A = page 12.  Section B
; under this LMPR = page 13 (which holds test_mem at this point — but the
; cluster suites never touch section B; they only call HMPR-stable
; section-C/D production routines, verified to not reach paged_call).
; HMPR is unchanged, so calls FROM cluster code TO production code work
; as if the swap had never happened.
TEST_CLUSTER_PAGE:  equ     12
LMPR_TEST_CLUSTER:  equ     &20 + TEST_CLUSTER_PAGE     ; = &2C


; paged_call test-payload page (BUILD_TESTS only — see plan-PR 1 of
; https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/notes/2026-05-28-paged-call-architecture.md).
;
; Page 14 holds the boot self-test target stub for the section-B
; paged_call helper.  Picked distinct from page 13 (which plan-PR 3
; reuses for the off-axis test_mem corpus) so the test_mem off-axis
; pattern is undisturbed.  The brainstorm doc's long-term role for
; page 14 is "explanation prose" (memory-layout-brainstorm.md §3) —
; that lands later; until then plan-PR 1 reuses page 14 as the
; paged_call self-test payload page.
;
; HMPR-page form: the helper combines this low-5-bit page number
; with the entry HMPR's top 3 bits (CLUT + ext-mem) at runtime, so
; no LMPR_PAGED_CALL_TEST constant is needed at the call site.
PAGED_CALL_TEST_PAGE:   equ     14


; sysreg lookup data page (PRODUCTION feature — both variants).
;
; Per https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/plans/2026-05-29-m6-closure-release-bytematch.md PR-2, as
; corrected by the PR-2 implementation spec.  The four sysname lookup
; tables (sysreg / pstate / dc / tlbi) and the generic table-walker
; were moved off the section-C code budget into physical page 13.  The
; matcher runs under HMPR=13 (reached via paged_call) and is fully
; self-contained — it reads the operand name from / writes the matched
; fields to the section-B comm buffer below, never touching section
; C/D (which are paged away under HMPR=13).  See src/sysreg_data.asm
; and src/sysname.asm headers for the split-design rationale.
;
; Page 13 was previously reused by the BUILD_TESTS-only off-axis
; test_mem payload (LMPR_TEST_MEM above).  That use is a transient
; LMPR-swap into section A; this sysreg use is an HMPR=13 paged_call.
; They never overlap in time (test_mem self-tests run to completion at
; boot before main_assemble; the sysreg matcher only runs during
; main_assemble).  Both occupy page 13's physical bytes from &0000 /
; &8000 respectively — but the off-axis payload assembles at org &0000
; (section-A view) while sysreg_data assembles at org &8000 (section-C
; view); the SAME physical page-13 bytes back both.  They do not clash
; because they are loaded at DIFFERENT times: in the test variant
; load_test_mem_off_axis loads test_mem.bin first and run_mem_self_tests
; fully consumes it; load_page13_payload then overwrites page 13 with
; sysreg_data.bin before main_assemble.  In the production variant
; test_mem is never loaded, so page 13 is free for sysreg_data from the
; start.  See assembler.asm boot-sequence ordering.
SYSREG_DATA_PAGE:       equ     13

; paged_call entry points into the page-13 data file (one jump-table
; slot per sysname table; 8 bytes each — see sysreg_data.asm).  Used as
; the `defw <target>` in the call-site shape
; `call paged_call / defw SYSREG_*_ENTRY / defb SYSREG_DATA_PAGE`.
SYSREG_SYSREG_ENTRY:    equ     &8000
SYSREG_PSTATE_ENTRY:    equ     &8008
SYSREG_DC_ENTRY:        equ     &8010
SYSREG_TLBI_ENTRY:      equ     &8018

; sysreg comm buffer — LMPR-stable section B, shared between the
; main-side staging code (sysname.asm, default HMPR) and the page-13
; matcher (HMPR=13).  Placed in the verified-free region &7E72..&7ECF
; between the paged_call body+trailer end (&7E71; body is 50 bytes from
; PAGED_CALL_DST=&7E40) and PAGED_CALL_HMPR_SAVE (&7ED0).
;
; Safety from stack clobber: during a paged_call the SP is set to
; TRAMP_SAFE_SP=&7F00 and grows DOWN as the target pushes.  The page-13
; matcher is a shallow leaf: do_match enters with SP=&7EFE and its only
; push is sysreg_tolower's single return slot (&7EFC..&7EFD).  The
; deepest stack write is thus &7EFC — 100 bytes (&64) clear of the
; buffer end (&7E98), and well clear of PAGED_CALL_HMPR_SAVE (&7ED0).
; The boot self-test
; run_sysreg_paged_self_tests (BUILD_TESTS) proves the buffer survives a
; paged_call's stack usage with a sentinel.
;
; These addresses are duplicated (and kept in lock-step) in
; src/sysreg_data.asm, which is assembled standalone and cannot see
; these equ's.
SYSREG_COMM_NAME:       equ     TRAMPOLINE_DST + &80    ; = &7E80, 16 bytes
SYSREG_COMM_LEN:        equ     TRAMPOLINE_DST + &90    ; = &7E90, 1 byte
SYSREG_COMM_RESULT:     equ     TRAMPOLINE_DST + &91    ; = &7E91, up to 8 bytes


; Disassembler page (PRODUCTION feature — both variants).
;
; Per https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/notes/2026-06-07-disassembler-page-placement.md.
; Page 15 holds disasm.bin (standalone stub, assembles at org &8000).
; Invoked via paged_call (BUILD_TESTS: run_disasm_self_test at
; DISASM_SELF_TEST_ENTRY; runtime: on-SAM editor).  Not re-entrant —
; paged_call has a single static HMPR save slot; callers must not nest.
DISASM_PAGE:    equ     15

; Entry point address in section C when HMPR = DISASM_PAGE (= 15).
DISASM_ENTRY:   equ     &8000

; Disassembler comm buffer — LMPR-stable section B, shared between the
; caller (default HMPR) and the disasm routine (HMPR = DISASM_PAGE).
; Placed immediately after SYSREG_COMM_RESULT (&7E91 + 8 = &7E99),
; well clear of PAGED_CALL_HMPR_SAVE (&7ED0).
;
; Constants (DISASM_COMM_MNEM, DISASM_COMM_OPS, DISASM_SELF_TEST_ENTRY)
; are defined in src/disasm_comm.inc — a shared file included by both
; this file and src/disasm.asm (which is assembled standalone and cannot
; see our equ's directly).
                include "disasm_comm.inc"


; ZX0 payload constants (PRODUCTION feature — both variants).
;
; Per docs/specs/comment-storage-design.md §5: the greedy ZX0 compressor
; (&8400) + turbo decoder (&8B00) co-reside on page 13 with sysreg_data
; (&8000-&83B3), loaded at boot by load_zx0_payload (loader.asm) at the
; non-page-start offset &8400 and invoked via paged_call (HMPR=13, which
; also maps page 14 — the staging area — at section D).  ZX0_PAGE equals
; SYSREG_DATA_PAGE; the same BUILD_TESTS boot-time multiplex applies
; (test_mem.bin first, fully consumed, then sysreg_data + zx0).
;
; Constants (ZX0_PAGE, ZX0_LOAD_ADDR, ZX0_COMPRESS_ENTRY,
; ZX0_DECODE_ENTRY, ZX0_WORKSPACE, ZX0_SELF_TEST_ENTRY,
; ZX0_STAGING_SRC/DST) are defined in src/zx0_comm.inc — shared with
; src/zx0_payload.asm (assembled standalone), which asserts its layout
; against them so the two builds cannot drift.
                include "zx0_comm.inc"


TRAMPOLINE_DST: equ     &7E00          ; section-B copy destination
                                       ; (under LMPR_DEFAULT, section B
                                       ; = page 1).  Near the top of
                                       ; section B so we don't
                                       ; interfere with anything we
                                       ; might read from page 1 —
                                       ; which is nothing, since BASIC
                                       ; is dead by the time we copy.

; Absolute address of the static HMPR-save byte in section B.  The
; trampoline body's `ld (HMPR_SAVE), a` and `ld a, (HMPR_SAVE)`
; instructions encode THIS address; after the LDIR copy, those
; instructions access section B (LMPR-stable across the HMPR change)
; regardless of where the trampoline copy itself lives.
;
; Placed at TRAMPOLINE_DST + 32 to leave plenty of headroom for the
; trampoline body (30 bytes post-SP-switch).
HMPR_SAVE:      equ     TRAMPOLINE_DST + 32

; SP_SAVE — 2 bytes immediately after HMPR_SAVE.  The trampoline
; switches SP into a section-B-stable location for the duration of
; the RST 8 (see "Why we switch SP" below).  HLOAD's auto-paging
; writes can clobber addresses in section D (= HMPR+1 page during
; HLOAD), and the RST 8 hardware push lands wherever SP points —
; if that's in section D, the saved return address is corrupted by
; HLOAD's spillover for files > 16632 B.  Per
; https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/notes/2026-05-28-hload-16k-limit-investigation.md.
SP_SAVE:        equ     TRAMPOLINE_DST + 33    ; 2 bytes (33, 34)

; DOSER (&5BC0) file-I/O error handler — section-B residence (i25b,
; PRODUCTION only).  The handler body is copied here at boot (see
; doser_handler_body in src/assembler.asm) and (&5BC0) points at it.
;
; Why section B, not section C: the ROM's post-hook DOSER dispatch (DOSC,
; rom-v3.0 disassembly &3830-&3856) does `jp (DOSER)` with the CALLER'S
; HMPR still live — the ROM restores only LMPR (port 250), never HMPR
; (port 251).  An HLOAD via the trampoline runs with HMPR = the payload's
; target page (the trampoline restores HMPR only AFTER the RST 8 returns),
; so section C is paged onto the payload at dispatch time.  A section-C
; handler would be unreachable there (it executed payload garbage → a
; &0038 trap).  Section B is LMPR-stable and stays mapped under any HMPR,
; so the handler always runs.  Lives in the free gap between SP_SAVE
; (ends &7E22) and PAGED_CALL_DST (&7E40); body is 12 bytes (&7E23-&7E2E).
DOSER_HANDLER_DST:   equ     TRAMPOLINE_DST + &23   ; = &7E23
; DOSER_DEFAULT_HMPR — 1 byte holding the boot/default HMPR, captured at
; handler-install time.  The error path restores it (so section C maps the
; assembler code again) before jumping to the section-C fail_with_tag —
; the HMPR analogue of COMET dier's `OUT (250),A` LMPR restore
; (comet.asm:1361).  Top of the gap, clear of the 12-byte body.
DOSER_DEFAULT_HMPR:  equ     TRAMPOLINE_DST + &3F   ; = &7E3F

; TRAMP_SAFE_SP — value SP is switched to during the RST 8.  Chosen
; near the top of section B (which is LMPR-stable) but well clear of
; the trampoline body and HMPR_SAVE/SP_SAVE bytes.  The RST 8 push
; lands at TRAMP_SAFE_SP-1 and TRAMP_SAFE_SP-2 (= &7EFE / &7EFF) —
; in section B, untouched by HLOAD.  Matches COMET's `LD SP,(sproom)`
; pattern at `comet.asm:1189` (see investigation note).
;
; Also reused as the safe SP for the paged_call helper — it likewise
; needs SP in LMPR-stable section B for the duration of the target
; routine, because section D may map to a different physical page
; during the call.
TRAMP_SAFE_SP:  equ     TRAMPOLINE_DST + 256   ; = &7F00


; =======================================================================
; paged_call — section-B helper for generic paged target routines.
; =======================================================================
;
; Per https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/notes/2026-05-28-paged-call-architecture.md plan-PR 1 of §6.
;
; Lives in section B (LMPR-stable) alongside the HLOAD trampoline.
; Generalises the HLOAD trampoline's "swap HMPR, dispatch, swap back"
; pattern from (RST 8 with fixed hook number) to (any target address
; in section C with any target page number).  See paged_bodies.asm
; header for the call-site shape and the mechanism walkthrough; see
; the architecture doc §3 for the full design rationale.
;
; Section-B layout (TRAMPOLINE_DST = &7E00):
;
;   &7E00..&7E1?  HLOAD trampoline body (~30 bytes)
;   &7E20..&7E22  HMPR_SAVE (1) + SP_SAVE (2) — HLOAD static slots
;   &7E40..&7E??  paged_call body + trailer
;   &7ED0..&7ED2  PAGED_CALL_HMPR_SAVE (1) + PAGED_CALL_SP_SAVE (2)
;   &7F00         TRAMP_SAFE_SP (top-of-safe-SP scratch)
;
; HMPR_SAVE / SP_SAVE keep their original offsets at +32/+33 so the
; HLOAD trampoline body bytes are byte-identical to pre-PR-#50 state
; (lower regression risk during the salvage).  The new paged_call
; static slots live well above the body region (at +&D0+) and well
; below TRAMP_SAFE_SP scratch.
;
; ABI (architecture doc §3.4): paged_call clobbers A, HL, DE, F;
; preserves BC, IX, IY.  Target args go in BC / IX / IY.  Target runs
; with section C/D = target page; on RET, HMPR is restored bit-
; identically (including CLUT bits 5-6 and ext-mem bit 7).  Not
; re-entrant — the static save slots are one deep.
PAGED_CALL_DST:           equ     TRAMPOLINE_DST + &40    ; = &7E40
                                                          ; (clear of HLOAD body
                                                          ; ~30 B at +0 and
                                                          ; HMPR_SAVE/SP_SAVE at
                                                          ; +32/+33)
PAGED_CALL_HMPR_SAVE:     equ     TRAMPOLINE_DST + &D0    ; = &7ED0 (1 byte)
PAGED_CALL_SP_SAVE:       equ     TRAMPOLINE_DST + &D1    ; = &7ED1 (2 bytes)

; Lowercase alias for call-site readability (`call paged_call / defw
; addr / defb page` is the on-the-page shape).
paged_call:               equ     PAGED_CALL_DST


; -----------------------------------------------------------------------
; enctab_trampoline_setup — copy section-B helper bodies into section B.
;
; Installs both the HLOAD trampoline body and the paged_call body in
; one pass.  Must be called ONCE at startup, BEFORE the first call
; to TRAMPOLINE_DST or paged_call.  Idempotent (safe to call multiple
; times).
;
; (Historical aside: this routine used to copy only the HLOAD
; trampoline body — hence the "enctab" prefix in its name.  It was
; extended to also copy the paged_call body in plan-PR 1 of the
; paging-architecture rollout.  The name stayed for diff-blame
; continuity.)
;
; Input:  none.
; Output: HLOAD body installed at TRAMPOLINE_DST; paged_call body
;         installed at PAGED_CALL_DST.  Both in section B.
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
enctab_trampoline_setup:
                ; HLOAD trampoline body.
                ld      hl, trampoline_body
                ld      de, TRAMPOLINE_DST
                ld      bc, trampoline_body_end - trampoline_body
                ldir
                ; paged_call body (source lives in src/paged_bodies.asm,
                ; included at the END of assembler.asm — see
                ; paged_bodies.asm header for the source-position note).
                ld      hl, paged_call_body
                ld      de, PAGED_CALL_DST
                ld      bc, paged_call_body_end - paged_call_body
                ldir
                ret


; -----------------------------------------------------------------------
; trampoline_body / trampoline_body_end — the bytes that get copied
; into section B at startup.  The CALLER does NOT call this label;
; they call TRAMPOLINE_DST, which holds the COPY in section B (which
; is LMPR-stable across the HMPR change the body issues).
;
; The static-save absolute address (HMPR_SAVE) is in section B, so
; the LD (HMPR_SAVE), A and LD A, (HMPR_SAVE) instructions baked into
; the body access section B regardless of where the body's COPY runs
; from — making the body fully position-correct once LDIRed to
; TRAMPOLINE_DST.
;
; Critical: we MUST NOT clobber the caller's B/C/D/E/H/L between
; reading HMPR and the RST 8, because the SAMDOS hook dispatcher saves
; HL/BC/DE from the caller's MAIN register set at hook entry — and
; HLOAD's dschd then reads them back as the destination and pages-
; count.  Clobbering C (for example) would make HLOAD think the file
; spans more 16K pages than it actually does, and the load would
; over-read past EOF.
;
; Input (set up by caller before CALLing TRAMPOLINE_DST):
;   HL = &8000..&BFFF (HLOAD's section-C window)
;   B  = target physical page (5-bit, only low 5 bits used)
;   IX = UIFA pointer
;   C  = pages count (from DIFA+34)
;   DE = length modulo 16K (from DIFA+35, with bit 7 of D cleared)
;
; Output: HMPR restored to its entry value; HL/BC/DE/AF clobbered;
;         AF carries HLOAD's status (CY = error — but HLOAD longjmps
;         on error so CY in practice means "didn't longjmp = success").
;
; Caller must ensure interrupts are disabled BEFORE calling (the
; trampoline runs DI internally at the end but does not gate the
; entry — and PTDOS does `DI / OUT (250), A / LD SP, &8000 / EI`
; around its own work, so an interrupt during the HMPR-set window
; could see a deranged section C/D map).
trampoline_body:
                in      a, (251)                ; A = current HMPR
                ld      (HMPR_SAVE), a          ; save to absolute section-B
                                                ; address (LMPR-stable across
                                                ; the upcoming HMPR change)
                ld      (SP_SAVE), sp           ; save caller's SP — see "Why
                                                ; we switch SP" below.
                ld      sp, TRAMP_SAFE_SP       ; switch to section-B-stable SP
                                                ; so the upcoming RST 8 push
                                                ; lands in section B, not in
                                                ; section D where HLOAD writes.
                ld      a, b                    ; A = target page
                out     (251), a                ; HMPR = target
                                                ; (section C/D now points at
                                                ;  target page)
                rst     8
                defb    HOOK_HLOAD              ; 130 — longjmps on read error
                ex      af, af'                 ; preserve HLOAD's AF (CY = error)
                ld      a, (HMPR_SAVE)          ; A = saved HMPR (section B
                                                ; READ is LMPR-stable; AF
                                                ; flags from this LD don't
                                                ; matter because we restore
                                                ; AF below)
                out     (251), a                ; HMPR restored
                ld      sp, (SP_SAVE)           ; restore caller's SP — must
                                                ; happen AFTER HMPR is restored
                                                ; so that any subsequent stack
                                                ; access lands in the caller's
                                                ; native section D = HMPR+1.
                ex      af, af'                 ; restore HLOAD's AF
                di                              ; PTDOS does EI inside RST 8;
                                                ; restore the no-interrupts
                                                ; invariant
                ret
trampoline_body_end:

; -----------------------------------------------------------------------
; Why we switch SP
; ----------------
;
; Without the SP-switch, the trampoline issues RST 8 with SP still
; pointing into the CALLER's section D — and after the trampoline
; sets HMPR = IN_BASE_PAGE, section D maps to physical page
; (IN_BASE_PAGE + 1).  The Z80's RST 8 hardware push lands the return
; address at offsets in that page corresponding to (caller_SP - 2).
;
; For SP = &C100 and IN_BASE_PAGE = 7, the push lands at page 8
; offset &00F8 / &00F9.  HLOAD's auto-paging spills writes into page 8
; starting at offset 0; once the file exceeds 16384 + ~248 = 16632
; bytes, the spillover reaches offset &F8 and overwrites the saved
; return address.  PTDOS's eventual RET pops garbage and hangs.
;
; The fix is the COMET workaround at `comet.asm:1189`: switch SP to
; an LMPR-stable section-B address for the duration of the RST 8.
; PTDOS reads caller's SP via `LD HL, 0; ADD HL, SP` (saving it on its
; OWN stack at &8000), then switches SP to &8000 for dispatch, and
; restores caller's SP at exit.  Our trampoline's SP_SAVE value gets
; pushed/popped by PTDOS as part of that dance — invisible to HLOAD's
; section-D writes.
;
; With the switch in place, paged-IN files up to ≥ 32 KB load cleanly
; (verified empirically per
; https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/notes/2026-05-28-hload-16k-limit-investigation.md).


; -----------------------------------------------------------------------
; enctab_map_in — map ENCTAB (physical page 4) into section A.
;
; LMPR = LMPR_ENCTAB (&24): RAM0 bit set + low 5 bits = 4.  After this
; call, reads from &0000..&3FFF return ENCTAB bytes; the ROM is paged
; out of section A.  RST 8 calls will FAULT if attempted in this
; state — and so would the ROM interrupt handlers at &0038 / &0066,
; so we DI before the swap and leave interrupts disabled.
;
; Must be balanced by enctab_map_out before any RST 8 call.
;
; Clobbers: A.  Disables interrupts.
; -----------------------------------------------------------------------
enctab_map_in:
                di                              ; ROM handlers at &0038 / &0066
                                                ; would execute from section A,
                                                ; which is about to become ENCTAB
                                                ; data — must block interrupts.
                ld      a, LMPR_ENCTAB
                out     (250), a
                ret


; -----------------------------------------------------------------------
; enctab_map_out — restore the LMPR captured at boot.
;
; Reverse of enctab_map_in.  After this call, RST 8 (and any other ROM
; entry) works as it did under BASIC's original LMPR.  Interrupts
; remain DISABLED — the caller is responsible for re-enabling them if
; needed (the M3 assembler runs with interrupts disabled throughout;
; DOS hooks internally EI/DI around their own dispatch).
;
; The restore value comes from LMPR_DEFAULT_RUNTIME, populated by
; assembler.asm's `start:` from `IN A, (250)` at the very first
; opportunity.  Hard-coding LMPR_DEFAULT=&00 (RAM0 off, low bits 0)
; is WRONG: BASIC's CALL 32768 typically leaves LMPR at &1F (low bits
; = 31 → section B = page 0 = BASIC sys page).  Restoring &00 instead
; would silently shift section B to page 1 (different memory) and the
; next RST 8's UIFA-via-section-B reads would target wrong addresses
; (or, more visibly, the DOS hook would hang on the DOS-side
; stack switch because PTDOS's `IN B,(250)` and later `OUT (250),B`
; restore-pair would round-trip the WRONG initial value).
;
; Clobbers: A.  Leaves interrupts in their current state (typically
; disabled — see above).
; -----------------------------------------------------------------------
enctab_map_out:
                ld      a, (LMPR_DEFAULT_RUNTIME)
                out     (250), a
                ret


; -----------------------------------------------------------------------
; Static storage — captured at boot, used by enctab_map_out.
; Lives in section C alongside the rest of the trampoline source.
; Read/written via absolute addressing; section C is HMPR-managed but
; nothing in our flow changes HMPR while these are being touched
; (other than the trampoline itself, which lives entirely in section B
; and doesn't read this slot).
; -----------------------------------------------------------------------
LMPR_DEFAULT_RUNTIME:   defb    0
