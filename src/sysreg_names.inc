; sysreg_names.inc — encoding→name tables for the System instruction
; group, consumed by the page-15 disassembler (src/disasm.asm).
;
; The disassembler needs the REVERSE of the page-13 encoder tables: given
; an (op0,op1,CRn,CRm,op2) tuple it must emit the architectural name.
; These tables carry exactly the names the release.img corpus exercises
; (the oracle in tools/z80-test-harness-go/disasm_oracle_test.go is the
; authority on which) — a SUBSET of the Go authority
; tools/sam-aarch64-format/sysregs.go.  Tuples NOT found here decode to the
; generic GNU/objdump spelling s<op0>_<op1>_c<CRn>_c<CRm>_<op2> (built by
; disasm_sys_emit_generic), mirroring format.SysRegName.
;
; The table BYTES are shared with the page-13 matcher.  Both pages
; `include "sysreg_tables.inc"` (the single source of truth) — page 13 via
; src/sysreg_data.asm, page 15 via this file — so the two cannot drift.
; The shared file defines the labels sysreg_table / pstate_table /
; dc_table / tlbi_table; the `equ` aliases below give the disassembler its
; preferred disasm_*_tbl names without renaming every reference in
; disasm.asm.  The sync guard (tools/sam-aarch64-format/
; sysregs_z80sync_test.go) parses sysreg_tables.inc directly, so it stays
; faithful regardless of which page embeds the bytes.
;
; Field order per table (matches sysreg_tables.inc):
;   disasm_sysreg_tbl  — op0, op1, CRn, CRm, op2          (5 fields)
;   disasm_pstate_tbl  — op1, op2                          (2 fields)
;   disasm_dc_tbl      — op1, CRn, CRm, op2                (4 fields)
;   disasm_tlbi_tbl    — op1, CRn, CRm, op2, NeedsXt       (5 fields)

                include "sysreg_tables.inc"

disasm_sysreg_tbl:      equ     sysreg_table
disasm_pstate_tbl:      equ     pstate_table
disasm_dc_tbl:          equ     dc_table
disasm_tlbi_tbl:        equ     tlbi_table
