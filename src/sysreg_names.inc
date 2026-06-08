; sysreg_names.inc — encoding→name tables for the System instruction
; group, consumed by the page-15 disassembler (src/disasm.asm).
;
; The disassembler needs the REVERSE of the page-13 encoder tables in
; src/sysreg_data.asm: given an (op0,op1,CRn,CRm,op2) tuple it must emit
; the architectural name.  These tables carry exactly the names the
; release.img corpus exercises (the oracle in
; tools/z80-test-harness-go/disasm_oracle_test.go is the authority on
; which) — a SUBSET of the Go authority tools/sam-aarch64-format/
; sysregs.go, same as src/sysreg_data.asm.  Tuples NOT found here decode
; to the generic GNU/objdump spelling s<op0>_<op1>_c<CRn>_c<CRm>_<op2>
; (built by disasm_sys_emit_generic), mirroring format.SysRegName.
;
; Layout is byte-IDENTICAL to the page-13 matcher tables in
; src/sysreg_data.asm — `[name_len u8][name bytes][field bytes...]`,
; terminated by name_len == 0 — so a future refactor that has BOTH
; sysreg_data.asm (name→encoding, page 13) and disasm.asm
; (encoding→name, page 15) `include` ONE shared file is mechanical.
; That de-dup is deferred but NOT blocked by the sync guard: the guard
; (tools/sam-aarch64-format/sysregs_z80sync_test.go) parses ONLY the four
; `<name>_table:` sections and nothing else from sysreg_data.asm, so it
; does not need to follow `include`s — once those four blocks move into a
; shared `.inc`, just repoint the guard's asmPath at that `.inc` (keeping
; the four label names) and it stays faithful, reading the exact bytes
; both pages embed. The encodings below are copied verbatim from
; sysreg_data.asm (which the sync guard verifies against Go), so the two
; cannot disagree.
;
; Field order per table (matches sysreg_data.asm):
;   disasm_sysreg_tbl  — op0, op1, CRn, CRm, op2          (5 fields)
;   disasm_pstate_tbl  — op1, op2                          (2 fields)
;   disasm_dc_tbl      — op1, CRn, CRm, op2                (4 fields)
;   disasm_tlbi_tbl    — op1, CRn, CRm, op2, NeedsXt       (5 fields)

; --- sysreg name table (mrs/msr register form) ------------------------
disasm_sysreg_tbl:
                defb    9
                defm    "sctlr_el1"
                defb    3, 0, 1, 0, 0
                defb    4
                defm    "nzcv"
                defb    3, 3, 4, 2, 0
                defb    9
                defm    "currentel"
                defb    3, 0, 4, 2, 2
                defb    8
                defm    "midr_el1"
                defb    3, 0, 0, 0, 0
                defb    9
                defm    "mpidr_el1"
                defb    3, 0, 0, 0, 5
                defb    7
                defm    "esr_el1"
                defb    3, 0, 5, 2, 0
                defb    7
                defm    "elr_el1"
                defb    3, 0, 4, 0, 1
                defb    7
                defm    "far_el1"
                defb    3, 0, 6, 0, 0
                defb    10
                defm    "cntpct_el0"
                defb    3, 3, 14, 0, 1
                defb    12
                defm    "cntp_ctl_el0"
                defb    3, 3, 14, 2, 1
                defb    13
                defm    "cntp_cval_el0"
                defb    3, 3, 14, 2, 2
                defb    7
                defm    "elr_el3"
                defb    3, 6, 4, 0, 1
                defb    7
                defm    "hcr_el2"
                defb    3, 4, 1, 1, 0
                defb    8
                defm    "mair_el1"
                defb    3, 0, 10, 2, 0
                defb    7
                defm    "scr_el3"
                defb    3, 6, 1, 1, 0
                defb    8
                defm    "spsr_el3"
                defb    3, 6, 4, 0, 0
                defb    7
                defm    "tcr_el1"
                defb    3, 0, 2, 0, 2
                defb    9
                defm    "ttbr0_el1"
                defb    3, 0, 2, 0, 0
                defb    9
                defm    "ttbr1_el1"
                defb    3, 0, 2, 0, 1
                defb    8
                defm    "vbar_el1"
                defb    3, 0, 12, 0, 0
                defb    0

; --- pstate field table (msr immediate form) --------------------------
disasm_pstate_tbl:
                defb    7
                defm    "daifset"
                defb    3, 6
                defb    7
                defm    "daifclr"
                defb    3, 7
                defb    0

; --- dc op table ------------------------------------------------------
disasm_dc_tbl:
                defb    5
                defm    "civac"
                defb    3, 7, 14, 1
                defb    4
                defm    "cvac"
                defb    3, 7, 10, 1
                defb    4
                defm    "ivac"
                defb    0, 7, 6, 1
                defb    0

; --- tlbi op table ----------------------------------------------------
disasm_tlbi_tbl:
                defb    7
                defm    "vmalle1"
                defb    0, 8, 7, 0, 0
                defb    6
                defm    "vae1is"
                defb    0, 8, 3, 1, 1
                defb    0
