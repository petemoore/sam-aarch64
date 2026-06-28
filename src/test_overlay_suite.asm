; test_overlay_suite.asm — the i204b overlay_classify boot-self-test
; suite payload (enc-tests variant, BUILD_TESTS_ENCODE / i234).
;
; Assembles STANDALONE (not included by assembler.asm) into
; build/overlay_suite.bin against the main binary's symbol export
; (--importfile=build/assembler-enc-tests.sym, for encode_inst /
; insn_fold / fail / PASS_PC / ... and OVERLAY_SUITE_RAM itself) plus
; the fixture payload's export (--importfile=build/enc_fix_payload.sym,
; for the toc_* fixture tables).  See the Makefile `overlay-suite` rule.
;
; WHY A PAYLOAD: the suite (the insn_overlay.asm routines + the fixture
; driver, ~1.5 KB) is ENCTAB-coupled — it calls encode_inst, which reads
; ENCTAB in section A — so like the encode family it cannot live in an
; off-axis section-A cluster.  Inline in section C it overruns the
; enc-tests variant's &C000 budget by ~400 B.  Section-D RAM is the
; relief: HLOADed into physical page 12 at boot ("ovl12",
; loader.asm::load_overlay_suite), LDIR'd to OVERLAY_SUITE_RAM (&F080)
; by the boot stub in assembler.asm, and executed there — section D is
; HMPR-controlled, so the code stays addressable inside the suite's own
; enctab_map_in bracket (LMPR-only) and calls section-C routines
; directly.  Full rationale: src/trampoline.asm (OVERLAY_SUITE_RAM).
;
; Wire format (the boot stub's contract):
;   +0  code length (u16 LE) — the stub's LDIR count
;   +2  the suite code, org'd at OVERLAY_SUITE_RAM; the first byte is
;       the entry point (the stub does `call OVERLAY_SUITE_RAM`).

                org     OVERLAY_SUITE_RAM - 2

                defw    overlay_suite_end - overlay_suite_entry

; Entry point at OVERLAY_SUITE_RAM: the driver's entry sits right here,
; first in the include order, so no jump thunk is needed.
overlay_suite_entry:
                include "test_overlay_classify.asm"
                include "insn_overlay.asm"
overlay_suite_end:
