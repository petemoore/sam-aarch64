; sysreg_tables.inc — the four AArch64 System-group name↔encoding tables
; (sysreg / pstate / dc / tlbi), shared by BOTH page-13 and page-15.
;
; This is the single source of truth for the table bytes, `include`d by:
;   - src/sysreg_data.asm (page 13, name→encoding matcher, do_match)
;   - src/disasm.asm via src/sysreg_names.inc (page 15, encoding→name)
;
; Layout per record: `[name_len u8][name bytes][N field bytes...]`,
; terminated by name_len == 0.  Entries are sorted by name within each
; table (the matcher and disassembler do order-insensitive linear walks
; over unique names, so order affects only walk order, not behaviour).
; Field counts (and the architectural field order) per table:
;   sysreg_table  — 5 bytes: op0, op1, CRn, CRm, op2
;   pstate_table  — 2 bytes: op1, op2
;   dc_table      — 4 bytes: op1, CRn, CRm, op2
;   tlbi_table    — 5 bytes: op1, CRn, CRm, op2, NeedsXt
;
; The pstate / dc / tlbi tables are at FULL parity with the Go authority
; tools/sam-aarch64-format/sysregs.go (those three families have no generic
; Sn_op1_Cm_Cn_op2 fallback in Go, so a missing entry would make the SAM
; assembler fail-hard on a name Go encodes — see sysname.asm's lookup-miss
; paths).  The sysreg_table is still a subset: any unnamed system register
; is reachable via the generic Sn_op1_Cm_Cn_op2 form (matching Go's
; ParseSysReg / SysRegName fallback), so missing named sysregs are not a
; correctness gap.  Every entry here MUST match the Go encoding
; byte-for-byte: the sync guard tools/sam-aarch64-format/
; sysregs_z80sync_test.go (CI job `sysreg-sync`, make target
; `sysreg-sync-check`) parses THIS file and fails on any name/encoding
; divergence — so if you add or edit an entry here, update sysregs.go too
; (and vice versa).  All names are lower case (the page-13 matcher
; lowercases the source bytes before compare).

; sysreg_table — 20 entries (a subset of the Go authority; unnamed
; system registers reach both sides via the generic Sn_op1_Cm_Cn_op2 form).
; Format per entry: [name_len u8][name_bytes][op0][op1][CRn][CRm][op2].
sysreg_table:
                defb    12
                defm    "cntp_ctl_el0"
                defb    3, 3, 14, 2, 1
                defb    13
                defm    "cntp_cval_el0"
                defb    3, 3, 14, 2, 2
                defb    10
                defm    "cntpct_el0"
                defb    3, 3, 14, 0, 1
                defb    9
                defm    "currentel"
                defb    3, 0, 4, 2, 2
                defb    7
                defm    "elr_el1"
                defb    3, 0, 4, 0, 1
                defb    7
                defm    "elr_el3"
                defb    3, 6, 4, 0, 1
                defb    7
                defm    "esr_el1"
                defb    3, 0, 5, 2, 0
                defb    7
                defm    "far_el1"
                defb    3, 0, 6, 0, 0
                defb    7
                defm    "hcr_el2"
                defb    3, 4, 1, 1, 0
                defb    8
                defm    "mair_el1"
                defb    3, 0, 10, 2, 0
                defb    8
                defm    "midr_el1"
                defb    3, 0, 0, 0, 0
                defb    9
                defm    "mpidr_el1"
                defb    3, 0, 0, 0, 5
                defb    4
                defm    "nzcv"
                defb    3, 3, 4, 2, 0
                defb    7
                defm    "scr_el3"
                defb    3, 6, 1, 1, 0
                defb    9
                defm    "sctlr_el1"
                defb    3, 0, 1, 0, 0
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


; pstate_table — full parity with the Go authority pstateFields.  8 entries.
; Format per entry: [name_len u8][name_bytes][op1 u8][op2 u8].
pstate_table:
                defb    7
                defm    "daifclr"
                defb    3, 7
                defb    7
                defm    "daifset"
                defb    3, 6
                defb    3
                defm    "dit"
                defb    3, 2
                defb    3
                defm    "pan"
                defb    0, 4
                defb    5
                defm    "spsel"
                defb    0, 5
                defb    4
                defm    "ssbs"
                defb    3, 1
                defb    3
                defm    "tco"
                defb    3, 4
                defb    3
                defm    "uao"
                defb    0, 3
                defb    0


; dc_table — full parity with the Go authority dcOps.  9 entries.
; Format per entry: [name_len u8][name_bytes][op1 u8][CRn u8][CRm u8][op2 u8].
dc_table:
                defb    4
                defm    "cisw"
                defb    0, 7, 14, 2
                defb    5
                defm    "civac"
                defb    3, 7, 14, 1
                defb    3
                defm    "csw"
                defb    0, 7, 10, 2
                defb    4
                defm    "cvac"
                defb    3, 7, 10, 1
                defb    4
                defm    "cvap"
                defb    3, 7, 12, 1
                defb    4
                defm    "cvau"
                defb    3, 7, 11, 1
                defb    3
                defm    "isw"
                defb    0, 7, 6, 2
                defb    4
                defm    "ivac"
                defb    0, 7, 6, 1
                defb    3
                defm    "zva"
                defb    3, 7, 4, 1
                defb    0


; tlbi_table — full parity with the Go authority tlbiOps.  32 entries.
; Format per entry:
;   [name_len u8][name_bytes][op1 u8][CRn u8][CRm u8][op2 u8][NeedsXt u8].
tlbi_table:
                defb    5
                defm    "alle1"
                defb    4, 8, 7, 4, 0
                defb    7
                defm    "alle1is"
                defb    4, 8, 3, 4, 0
                defb    5
                defm    "alle2"
                defb    4, 8, 7, 0, 0
                defb    7
                defm    "alle2is"
                defb    4, 8, 3, 0, 0
                defb    5
                defm    "alle3"
                defb    6, 8, 7, 0, 0
                defb    7
                defm    "alle3is"
                defb    6, 8, 3, 0, 0
                defb    6
                defm    "aside1"
                defb    0, 8, 7, 2, 1
                defb    8
                defm    "aside1is"
                defb    0, 8, 3, 2, 1
                defb    7
                defm    "ipas2e1"
                defb    4, 8, 4, 1, 1
                defb    9
                defm    "ipas2e1is"
                defb    4, 8, 0, 1, 1
                defb    8
                defm    "ipas2le1"
                defb    4, 8, 4, 5, 1
                defb    10
                defm    "ipas2le1is"
                defb    4, 8, 0, 5, 1
                defb    5
                defm    "vaae1"
                defb    0, 8, 7, 3, 1
                defb    7
                defm    "vaae1is"
                defb    0, 8, 3, 3, 1
                defb    6
                defm    "vaale1"
                defb    0, 8, 7, 7, 1
                defb    8
                defm    "vaale1is"
                defb    0, 8, 3, 7, 1
                defb    4
                defm    "vae1"
                defb    0, 8, 7, 1, 1
                defb    6
                defm    "vae1is"
                defb    0, 8, 3, 1, 1
                defb    4
                defm    "vae2"
                defb    4, 8, 7, 1, 1
                defb    6
                defm    "vae2is"
                defb    4, 8, 3, 1, 1
                defb    4
                defm    "vae3"
                defb    6, 8, 7, 1, 1
                defb    6
                defm    "vae3is"
                defb    6, 8, 3, 1, 1
                defb    5
                defm    "vale1"
                defb    0, 8, 7, 5, 1
                defb    7
                defm    "vale1is"
                defb    0, 8, 3, 5, 1
                defb    5
                defm    "vale2"
                defb    4, 8, 7, 5, 1
                defb    7
                defm    "vale2is"
                defb    4, 8, 3, 5, 1
                defb    5
                defm    "vale3"
                defb    6, 8, 7, 5, 1
                defb    7
                defm    "vale3is"
                defb    6, 8, 3, 5, 1
                defb    7
                defm    "vmalle1"
                defb    0, 8, 7, 0, 0
                defb    9
                defm    "vmalle1is"
                defb    0, 8, 3, 0, 0
                defb    10
                defm    "vmalls12e1"
                defb    4, 8, 7, 6, 0
                defb    12
                defm    "vmalls12e1is"
                defb    4, 8, 3, 6, 0
                defb    0
