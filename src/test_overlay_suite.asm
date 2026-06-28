; test_overlay_suite.asm — the enc-tests section-D suite payload: the
; i204b overlay_classify boot self-test plus the i48c-b8e compact
; encoder adapter and its boot self-test (BUILD_TESTS_ENCODE / i234).
;
; Assembles STANDALONE (not included by assembler.asm) into
; build/overlay_suite.bin against the main binary's symbol export
; (--importfile=build/assembler-enc-tests.sym, for encode_inst /
; insn_fold / fail / PASS_PC / ... and OVERLAY_SUITE_RAM itself) plus
; the fixture payload's export (--importfile=build/enc_fix_payload.sym,
; for the toc_* / cadapt_* fixture tables).  See the Makefile
; `overlay-suite` rule, which also enforces the payload size cap (the
; suite code must end below CEMIT_ELEMS — see src/compact_emit.asm's
; buffer map).
;
; WHY A PAYLOAD: the suites (the insn_overlay.asm + compact_emit.asm
; routines and their drivers) are ENCTAB-coupled — they call
; encode_inst, which reads ENCTAB in section A — so like the encode
; family they cannot live in an off-axis section-A cluster.  Inline in
; section C they overrun the enc-tests variant's &C000 budget.
; Section-D RAM is the relief: HLOADed into physical page 12 at boot
; ("ovl12", loader.asm::load_overlay_suite), LDIR'd to
; OVERLAY_SUITE_RAM (&F080) by the boot stub in assembler.asm, and
; executed there — section D is HMPR-controlled, so the code stays
; addressable inside each suite's own enctab_map_in bracket (LMPR-only)
; and calls section-C routines directly.  Full rationale:
; src/trampoline.asm (OVERLAY_SUITE_RAM).
;
; Wire format (the boot stub's contract):
;   +0  code length (u16 LE) — the stub's LDIR count
;   +2  the suite code, org'd at OVERLAY_SUITE_RAM; the first byte is
;       the entry point (the stub does `call OVERLAY_SUITE_RAM`).

                org     OVERLAY_SUITE_RAM - 2

                defw    overlay_suite_end - overlay_suite_entry

; Entry point at OVERLAY_SUITE_RAM: run both suites in order.  The
; compact-adapter suite runs second — each opens its own ENCTAB bracket.
overlay_suite_entry:
                call    run_overlay_classify_self_tests
                jp      run_compact_adapter_self_tests  ; tail; RETs to the stub

                include "test_overlay_classify.asm"
                include "test_compact_adapter.asm"
                include "compact_emit.asm"
                include "insn_overlay.asm"
overlay_suite_end:
