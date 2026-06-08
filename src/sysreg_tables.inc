; sysreg_tables.inc — the four AArch64 System-group name↔encoding tables
; (sysreg / pstate / dc / tlbi), shared by BOTH page-13 and page-15.
;
; This is the single source of truth for the table bytes, `include`d by:
;   - src/sysreg_data.asm (page 13, name→encoding matcher, do_match)
;   - src/disasm.asm via src/sysreg_names.inc (page 15, encoding→name)
;
; Layout per record: `[name_len u8][name bytes][N field bytes...]`,
; terminated by name_len == 0.  Field counts (and the architectural field
; order) per table:
;   sysreg_table  — 5 bytes: op0, op1, CRn, CRm, op2
;   pstate_table  — 2 bytes: op1, op2
;   dc_table      — 4 bytes: op1, CRn, CRm, op2
;   tlbi_table    — 5 bytes: op1, CRn, CRm, op2, NeedsXt
;
; These tables are a SUBSET of the Go authority
; tools/sam-aarch64-format/sysregs.go (only the entries the M5/M6 fixtures
; and the disasm oracle exercise; other tuples are handled by the generic
; Sn_op1_Cm_Cn_op2 form).  Every entry here MUST match the Go encoding
; byte-for-byte: the sync guard tools/sam-aarch64-format/
; sysregs_z80sync_test.go (CI job `sysreg-sync`, make target
; `sysreg-sync-check`) parses THIS file and fails on any name/encoding
; divergence — so if you add or edit an entry here, update sysregs.go too
; (and vice versa).  All names are lower case (the page-13 matcher
; lowercases the source bytes before compare).

; sysreg_table — 12 original + 8 new = 20 entries.
; Format per entry: [name_len u8][name_bytes][op0][op1][CRn][CRm][op2].
sysreg_table:
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
; --- 8 new entries (PR-2; verified vs sam-aarch64-format/sysregs.go) ---
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


; pstate_table — 2 entries.
; Format per entry: [name_len u8][name_bytes][op1 u8][op2 u8].
pstate_table:
                defb    7
                defm    "daifset"
                defb    3, 6
                defb    7
                defm    "daifclr"
                defb    3, 7
                defb    0


; dc_table — 3 entries.
; Format per entry: [name_len u8][name_bytes][op1 u8][CRn u8][CRm u8][op2 u8].
dc_table:
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


; tlbi_table — 2 entries.
; Format per entry:
;   [name_len u8][name_bytes][op1 u8][CRn u8][CRm u8][op2 u8][NeedsXt u8].
tlbi_table:
                defb    7
                defm    "vmalle1"
                defb    0, 8, 7, 0, 0
                defb    6
                defm    "vae1is"
                defb    0, 8, 3, 1, 1
                defb    0
