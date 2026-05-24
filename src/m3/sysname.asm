; sysname.asm — OpSysName encoders (M5 PR-D Task 11).
;
; Mac-side reference: tools/refenc/pass2.go:1540-1686 (encodeMrs /
; encodeMsr / encodeDc / encodeTlbi / sysRegEncode) plus
; tools/sam-aarch64-format/sysregs.go (the four name → fields tables).
;
; Four mnemonics share OpSysName:
;
;   mrs Xt, <sysreg>            ID 76   ARM ARM C6.2.137
;   msr <sysreg>, Xt            ID 77   ARM ARM C6.2.141 (register form)
;   msr <pstatefield>, #imm     ID 77   ARM ARM C6.2.140 (immediate form)
;   dc <op>, Xt                 ID 78   ARM ARM C6.2.66
;   tlbi <op>[, Xt]             ID 79   ARM ARM C6.2.281
;
; Dispatch is via try_mnemonic_intercept (intercepts.asm).  Each
; encoder reads OPVAL_ARRAY[k] +2..+5 to get the (ptr, len) of the
; sysname (a 16-bit pointer into IN_BUF and a 16-bit length, set by
; main_parse_sys_name in main_loop.asm), looks the name up in the
; appropriate table, packs the encoding, and returns DE:HL = 32-bit
; word for intercept_emit_dehl to emit.
;
; Table format (compact, option-(a) in the PR brief):
;   [name_len u8][name_bytes...][fields...]
;   Terminator: name_len = 0.
;
; Field counts per table:
;   sysreg_table  — 5 bytes: op0, op1, CRn, CRm, op2
;   dc_table      — 4 bytes: op1, CRn, CRm, op2     (all DC ops need Xt)
;   tlbi_table    — 5 bytes: op1, CRn, CRm, op2, NeedsXt
;   pstate_table  — 2 bytes: op1, op2
;
; Name comparison is case-insensitive (ASCII): the Mac-side parser
; preserves whatever the source wrote; the .go ParseSysReg lowercases
; before lookup.  We mirror that here so `MIDR_EL1` works as well as
; `midr_el1`.
;
; Only the sysregs / pstate / dc / tlbi ops actually referenced by
; M5 fixtures are included.  Adding new ops is mechanical: append the
; entry (name + fields) before the terminator.

; -----------------------------------------------------------------------
; encode_mrs_word — `mrs Xt, <sysreg>`
;
; Encoding (refenc/pass2.go:1540-1558):
;   1101 0101 0011 op0:1 op1:3 CRn:4 CRm:4 op2:3 Rt:5
;   base 0xd5300000 | ((op0&1)<<19) | (op1<<16) | (CRn<<12) |
;                     (CRm<<8) | (op2<<5) | Rt
;
; OPVAL[0] = Rt (OpRegX, Rt at +1)
; OPVAL[1] = OpSysName (ptr at +2..+3, len at +4..+5)
;
; Output: DE:HL = 32-bit word.
; Errors: jp fail on bad operand kinds / unknown sysreg.
; -----------------------------------------------------------------------
encode_mrs_word:
                ld      a, (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 0)
                cp      OP_KIND_REG_X
                jp      nz, fail
                ld      a, (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 0)
                cp      OP_KIND_SYS_NAME
                jp      nz, fail
                ld      a, 1                ; operand index 1 = sysname
                call    sysname_lookup_sysreg   ; → expects fields at sysreg_fields
                ld      a, (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 1)
                and     &1f                 ; Rt
                ld      c, a
                ld      hl, &0000           ; bits 0..15 of 0xd5300000
                ld      de, &d530           ; bits 16..31 of 0xd5300000
                jp      sysreg_pack         ; tail-call → DE:HL


; -----------------------------------------------------------------------
; encode_msr_word — two forms:
;
;   msr <sysreg>, Xt        (register form, refenc/pass2.go:1581-1586)
;     1101 0101 0001 op0:1 op1:3 CRn:4 CRm:4 op2:3 Rt:5
;     base 0xd5100000 | ((op0&1)<<19) | (op1<<16) | (CRn<<12) |
;                       (CRm<<8) | (op2<<5) | Rt
;
;   msr <pstatefield>, #imm (immediate / PSTATE form, refenc/pass2.go:1587-1604)
;     1101 0101 0000 op1:3 0100 CRm:4 op2:3 11111
;     base 0xd500401f | (op1<<16) | (imm<<8) | (op2<<5)
;       where CRm carries the 4-bit immediate.
;
; OPVAL[0] = OpSysName (sysreg or PSTATE field name).
; OPVAL[1] = either OpRegX (register form) or OpImmExpr (PSTATE form).
;
; Output: DE:HL = 32-bit word.
; Errors: jp fail on bad operand kinds / unknown name / imm > 15.
; -----------------------------------------------------------------------
encode_msr_word:
                ld      a, (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 0)
                cp      OP_KIND_SYS_NAME
                jp      nz, fail
                ld      a, (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 0)
                cp      OP_KIND_REG_X
                jp      z, encode_msr_reg
                cp      OP_KIND_IMM_EXPR
                jp      z, encode_msr_pstate
                jp      fail

encode_msr_reg:
                xor     a                   ; operand index 0 = sysname
                call    sysname_lookup_sysreg
                ld      a, (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 1)
                and     &1f                 ; Rt
                ld      c, a
                ld      hl, &0000           ; bits 0..15 of 0xd5100000
                ld      de, &d510           ; bits 16..31 of 0xd5100000
                jp      sysreg_pack

encode_msr_pstate:
; Look up PSTATE field at operand 0; op1 → B, op2 → C.
                xor     a
                call    sysname_lookup_pstate   ; returns: B = op1, C = op2
                push    bc                  ; save (op1, op2)

; Read imm low byte from OPVAL[1] +2 (8-byte LE).  Range [0,15].
                ld      a, (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 2)
                cp      16
                jp      nc, fail
; Reject any non-zero high bytes (imm must fit in u8 and be < 16).
                ld      a, (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 3)
                or      a
                jp      nz, fail
                ld      a, (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 4)
                or      a
                jp      nz, fail
                ld      a, (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 5)
                or      a
                jp      nz, fail
                ld      a, (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 6)
                or      a
                jp      nz, fail
                ld      a, (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 7)
                or      a
                jp      nz, fail
                ld      a, (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 8)
                or      a
                jp      nz, fail
                ld      a, (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 9)
                or      a
                jp      nz, fail

                ld      a, (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 2)
                ld      e, a                ; E = imm (CRm)
                pop     bc                  ; B = op1, C = op2

; Build word = 0xd500401f | (op1 << 16) | (imm << 8) | (op2 << 5).
;   HL = bits 0..15:   0x401f | (imm << 8) | (op2 << 5)
;   DE = bits 16..31:  0xd500 | op1
                ld      a, &1f
                ld      l, a                ; HL low = 0x1f
                ld      a, &40
                or      e                   ; HL high low-byte = 0x40 OR imm
; Wait: imm goes into bits 8..11 (CRm), so HL high byte = 0x40 | imm.
                ld      h, a
                ld      a, c                ; op2
                and     7
                rrca
                rrca
                rrca                        ; A = (op2 << 5)
                or      l
                ld      l, a                ; HL low = 0x1f | (op2 << 5)
; DE = 0xd500 | (op1 << 0) — op1 lives in bits 18:16, i.e. DE low byte bits 0..2.
                ld      a, b                ; op1
                and     7
                ld      e, a
                ld      d, &d5
                ret


; -----------------------------------------------------------------------
; encode_dc_word — `dc <op>, Xt`
;
; Encoding (refenc/pass2.go:1616-1636):
;   1101 0101 0000 1 op1:3 0111 CRm:4 op2:3 Rt:5
;   base 0xd5080000 | (op1<<16) | (CRn<<12) | (CRm<<8) | (op2<<5) | Rt
;
; OPVAL[0] = OpSysName (DC op name).
; OPVAL[1] = OpRegX (Xt).
;
; Output: DE:HL = 32-bit word.
; -----------------------------------------------------------------------
encode_dc_word:
                ld      a, (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 0)
                cp      OP_KIND_SYS_NAME
                jp      nz, fail
                ld      a, (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 0)
                cp      OP_KIND_REG_X
                jp      nz, fail
                xor     a                   ; operand index 0
                call    sysname_lookup_dc   ; sysreg_fields = (op1, CRn, CRm, op2)
                ld      a, (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 1)
                and     &1f                 ; Rt
                ld      c, a
                ld      hl, &0000           ; bits 0..15 of 0xd5080000
                ld      de, &d508           ; bits 16..31 of 0xd5080000
; Reuse sysreg_pack via shared sys-instruction packing — DC and TLBI use
; the same field layout but op0 is implicit (the 0xd508 base already has
; op0=1 baked in); pass op0=0 in sysreg_fields[0] so sysreg_pack's OR is
; a no-op for those fields.  See sysname_lookup_dc which writes
; sysreg_fields[0] = 0 explicitly.
                jp      sysreg_pack


; -----------------------------------------------------------------------
; encode_tlbi_word — `tlbi <op>[, Xt]`
;
; Encoding (refenc/pass2.go:1642-1671):
;   1101 0101 0000 1 op1:3 1000 CRm:4 op2:3 Rt:5
;   base 0xd5080000 | (op1<<16) | (CRn<<12) | (CRm<<8) | (op2<<5) | Rt
;
; Rt defaults to 11111 (XZR) when no register is given; op.NeedsXt
; controls whether a register operand is required / allowed.
;
; OPVAL[0] = OpSysName (TLBI op name).
; OPVAL[1] = OpRegX (Xt) — optional.
;
; Output: DE:HL = 32-bit word.
; -----------------------------------------------------------------------
encode_tlbi_word:
                ld      a, (OPVAL_ARRAY + 0 * OPVAL_STRIDE + 0)
                cp      OP_KIND_SYS_NAME
                jp      nz, fail
                xor     a                   ; operand index 0
                call    sysname_lookup_tlbi ; sysreg_fields = (0,op1,CRn,CRm,op2); A = NeedsXt
                ld      (tlbi_needs_xt), a

; Default Rt = 31 (XZR).
                ld      c, 31

                ld      a, (main_op_count)
                cp      2
                jr      c, encode_tlbi_no_reg
; 2-operand form: validate kind and read Rt.
                ld      a, (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 0)
                cp      OP_KIND_REG_X
                jp      nz, fail
                ld      a, (tlbi_needs_xt)
                or      a
                jp      z, fail             ; this TLBI op does not take Xt
                ld      a, (OPVAL_ARRAY + 1 * OPVAL_STRIDE + 1)
                and     &1f
                ld      c, a
                jr      encode_tlbi_pack

encode_tlbi_no_reg:
                ld      a, (tlbi_needs_xt)
                or      a
                jp      nz, fail            ; this TLBI op requires Xt

encode_tlbi_pack:
                ld      hl, &0000
                ld      de, &d508
                jp      sysreg_pack

tlbi_needs_xt:   defb    0


; -----------------------------------------------------------------------
; sysreg_pack — pack base + sysreg_fields[0..4] + Rt → 32-bit word.
;
; Shared by MRS/MSR-reg (via op0:1 from sysreg_fields[0]) and DC/TLBI
; (where sysreg_fields[0] = 0 because op0 is fixed in the base).
;
; Input:  HL = base bits 0..15  (e.g. 0x3000)
;         DE = base bits 16..31 (e.g. 0xd530)
;         C  = Rt (low 5 bits)
;         sysreg_fields = [op0, op1, CRn, CRm, op2] (5 bytes)
;
; Output: DE:HL = encoded 32-bit word (HL bits 0..15, DE bits 16..31).
; Clobbers: A, BC (C consumed), DE, HL.
;
; Bit layout:
;   bits 0..4   = Rt              → HL low byte bits 0..4
;   bits 5..7   = op2 low 3       → HL low byte bits 5..7
;   bits 8..11  = CRm low 4       → HL high byte bits 0..3
;   bits 12..15 = CRn low 4       → HL high byte bits 4..7
;   bits 16..18 = op1 low 3       → DE low byte bits 0..2
;   bit  19     = op0 & 1         → DE low byte bit 3
; -----------------------------------------------------------------------
sysreg_pack:
; OR Rt into HL low byte bits 0..4.
                ld      a, c
                and     &1f
                or      l
                ld      l, a
; OR (op2 & 7) << 5 into HL low byte bits 5..7.
                ld      a, (sysreg_fields + 4)
                and     7
                rrca
                rrca
                rrca                        ; A = (op2 << 5)
                or      l
                ld      l, a
; OR CRm low 4 into HL high byte bits 0..3.
                ld      a, (sysreg_fields + 3)
                and     &0f
                or      h
                ld      h, a
; OR (CRn & 0x0f) << 4 into HL high byte bits 4..7.
                ld      a, (sysreg_fields + 2)
                and     &0f
                rrca
                rrca
                rrca
                rrca                        ; A = (CRn << 4)
                or      h
                ld      h, a
; OR (op1 & 7) into DE low byte bits 0..2.
                ld      a, (sysreg_fields + 1)
                and     7
                or      e
                ld      e, a
; OR (op0 & 1) << 3 into DE low byte bit 3.
                ld      a, (sysreg_fields + 0)
                and     1
                add     a, a
                add     a, a
                add     a, a                ; A = (op0 << 3)
                or      e
                ld      e, a
                ret


; -----------------------------------------------------------------------
; sysname_lookup_sysreg — look up OPVAL_ARRAY[A].name in sysreg_table or
; the generic Sn_op1_Cm_Cn_op2 form.  Writes (op0,op1,CRn,CRm,op2) to
; sysreg_fields[0..4].  jp fail on miss.
;
; Input:  A = operand index (0 or 1).
; Output: sysreg_fields[0..4] = (op0, op1, CRn, CRm, op2).
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
sysname_lookup_sysreg:
                call    sysname_setup       ; HL=ptr, BC=len, fields_dest=sysreg_fields
                ld      a, 5
                ld      (sysname_field_count), a
                ld      hl, sysreg_table
                call    sysname_match
                ret     z
; Fall through to generic Sn_op1_Cm_Cn_op2 parser.
                jp      sysname_parse_generic


; -----------------------------------------------------------------------
; sysname_lookup_pstate — look up PSTATE field name; return (op1, op2).
;
; Input:  A = operand index.
; Output: B = op1, C = op2.
; -----------------------------------------------------------------------
sysname_lookup_pstate:
                call    sysname_setup
                ld      a, 2
                ld      (sysname_field_count), a
                ld      hl, pstate_table
                call    sysname_match
                jp      nz, fail
                ld      a, (sysreg_fields + 0)
                ld      b, a
                ld      a, (sysreg_fields + 1)
                ld      c, a
                ret


; -----------------------------------------------------------------------
; sysname_lookup_dc — look up DC op name; return (op1, CRn, CRm, op2) in
; sysreg_fields[1..4] with sysreg_fields[0] = 0 (op0 is implicit in the
; DC base pattern).
;
; Input:  A = operand index.
; -----------------------------------------------------------------------
sysname_lookup_dc:
                call    sysname_setup
                ld      a, 4
                ld      (sysname_field_count), a
                ld      hl, dc_table
; sysname_match writes the matched fields starting at sysname_field_dest;
; for DC we want them at sysreg_fields+1.  Adjust the dest first.
                push    hl
                ld      hl, sysreg_fields + 1
                ld      (sysname_field_dest), hl
                pop     hl
                call    sysname_match
                jp      nz, fail
                xor     a
                ld      (sysreg_fields + 0), a
                ret


; -----------------------------------------------------------------------
; sysname_lookup_tlbi — look up TLBI op name; return:
;   sysreg_fields[0] = 0 (op0 implicit)
;   sysreg_fields[1] = op1
;   sysreg_fields[2] = CRn
;   sysreg_fields[3] = CRm
;   sysreg_fields[4] = op2
;   A = NeedsXt (0 or 1)
;
; Input:  A = operand index.
; -----------------------------------------------------------------------
sysname_lookup_tlbi:
                call    sysname_setup
                ld      a, 5
                ld      (sysname_field_count), a
                ld      hl, tlbi_table
                push    hl
                ld      hl, sysreg_fields + 1
                ld      (sysname_field_dest), hl
                pop     hl
                call    sysname_match
                jp      nz, fail
                xor     a
                ld      (sysreg_fields + 0), a
; The 5th matched field is NeedsXt.  Restore: shift entries so
; (op1,CRn,CRm,op2) land at +1..+4 and NeedsXt is returned in A.
                ld      a, (sysreg_fields + 5)
                ret


; -----------------------------------------------------------------------
; sysname_setup — given operand index in A, populate sysname scratch
; with name_ptr / name_len read from OPVAL_ARRAY[A] +2..+5, and reset
; sysname_field_dest to sysreg_fields.
;
; Input:  A = operand index.
; Output: sysname_ptr / sysname_len set, sysname_field_dest = sysreg_fields.
; -----------------------------------------------------------------------
sysname_setup:
; Compute OPVAL_ARRAY + idx * OPVAL_STRIDE.  STRIDE=10 = 8+2.
                add     a, a                ; A = 2*idx
                ld      c, a
                add     a, a
                add     a, a                ; A = 8*idx
                add     a, c                ; A = 10*idx
                ld      c, a
                ld      b, 0
                ld      hl, OPVAL_ARRAY
                add     hl, bc              ; HL → OPVAL[idx]
                inc     hl
                inc     hl                  ; HL → +2 (name_ptr LSB)
                ld      e, (hl)
                inc     hl
                ld      d, (hl)
                inc     hl
                ld      (sysname_ptr), de
                ld      e, (hl)
                inc     hl
                ld      d, (hl)
                ld      (sysname_len), de
                ld      hl, sysreg_fields
                ld      (sysname_field_dest), hl
                ret


; -----------------------------------------------------------------------
; sysname_match — walk a (name_len, name_bytes, fields) table looking
; for a case-insensitive ASCII match against (sysname_ptr, sysname_len).
;
; Input:  HL = table start.
;         (sysname_ptr) / (sysname_len) — source name.
;         (sysname_field_count) — fields per entry.
;         (sysname_field_dest) — where to copy matched fields.
;
; Output: Z=1 + fields copied if match found; Z=0 if reached terminator.
; Clobbers: A, BC, DE, HL.
; -----------------------------------------------------------------------
sysname_match:
sysname_match_entry:
                ld      a, (hl)             ; entry name_len
                or      a
                jp      z, sysname_match_done_nz   ; terminator
                ld      c, a                ; C = entry name_len
                inc     hl                  ; HL → entry name bytes
; Compare lengths first.
                ld      a, (sysname_len + 0)
                cp      c
                jp      nz, sysname_skip_entry
                ld      a, (sysname_len + 1)
                or      a
                jp      nz, sysname_skip_entry    ; src len > 255 ≠ entry len
; Lengths match.  Compare bytes case-insensitively.
                ld      de, (sysname_ptr)
                ld      b, c                ; B = bytes to compare
sysname_match_loop:
                ld      a, (de)
                call    to_lower_ascii
                ld      c, a
                ld      a, (hl)             ; entry byte (already lower)
                cp      c
                jp      nz, sysname_skip_entry_inloop
                inc     hl
                inc     de
                djnz    sysname_match_loop
; Match!  HL → first field byte.  Copy sysname_field_count bytes to
; sysname_field_dest.
                ld      a, (sysname_field_count)
                ld      b, a
                ld      de, (sysname_field_dest)
sysname_copy_fields:
                ld      a, (hl)
                ld      (de), a
                inc     hl
                inc     de
                djnz    sysname_copy_fields
                xor     a                   ; Z=1
                ret

sysname_skip_entry_inloop:
; HL currently points partway through entry name; advance to end of entry.
                ld      a, b                ; A = bytes remaining
                ld      c, a
                ld      b, 0
                add     hl, bc              ; HL → first field
                jp      sysname_skip_fields

sysname_skip_entry:
; HL points at first byte of entry name; skip C name bytes + fields.
                ld      b, 0
                add     hl, bc              ; HL → first field
sysname_skip_fields:
                ld      a, (sysname_field_count)
                ld      c, a
                ld      b, 0
                add     hl, bc              ; HL → next entry
                jp      sysname_match_entry

sysname_match_done_nz:
                or      &ff
                ret


; -----------------------------------------------------------------------
; to_lower_ascii — A: convert ASCII upper case to lower case in-place.
;   'A'..'Z' (0x41..0x5A) → +0x20.  Other bytes unchanged.
; Preserves flags-relevant state only via direct CMP; safe to assume only
; A is modified.
; -----------------------------------------------------------------------
to_lower_ascii:
                cp      &41                 ; 'A'
                ret     c
                cp      &5b                 ; 'Z' + 1
                ret     nc
                add     a, 32
                ret


; -----------------------------------------------------------------------
; sysname_parse_generic — parse Sn_op1_Cm_Cn_op2 (case-insensitive).
;
; Format: s<op0>_<op1>_c<CRn>_c<CRm>_<op2>
;   op0  ∈ 0..3 (single digit per format)
;   op1  ∈ 0..7
;   CRn  ∈ 0..15  (one or two digits after 'c')
;   CRm  ∈ 0..15
;   op2  ∈ 0..7
;
; Reads (sysname_ptr, sysname_len); writes (op0,op1,CRn,CRm,op2) into
; sysreg_fields[0..4].  jp fail on any malformed input.
;
; Mirrors tools/sam-aarch64-format/sysregs.go:parseGenericSysReg.
; -----------------------------------------------------------------------
sysname_parse_generic:
                ld      hl, (sysname_ptr)
                ld      de, (sysname_len)
; Need at least 2 chars and length must fit in u8 for the parser.
                ld      a, d
                or      a
                jp      nz, fail
                ld      a, e
                cp      2
                jp      c, fail
                ld      b, e                ; B = bytes remaining
; First byte must be 's' or 'S'.
                ld      a, (hl)
                call    to_lower_ascii
                cp      &73                 ; 's'
                jp      nz, fail
                inc     hl
                dec     b
                jp      z, fail
; Parse op0 digit, store at sysreg_fields[0].
                call    sysname_parse_digit ; A = digit, advances HL/B, expects '_'
                cp      4
                jp      nc, fail
                ld      (sysreg_fields + 0), a
                call    sysname_expect_underscore

; Parse op1 (0..7).
                call    sysname_parse_digit
                cp      8
                jp      nc, fail
                ld      (sysreg_fields + 1), a
                call    sysname_expect_underscore

; Expect 'c'.
                ld      a, b
                or      a
                jp      z, fail
                ld      a, (hl)
                call    to_lower_ascii
                cp      &63                 ; 'c'
                jp      nz, fail
                inc     hl
                dec     b
                jp      z, fail
; Parse CRn (0..15, 1-2 digits).
                call    sysname_parse_15
                ld      (sysreg_fields + 2), a
                call    sysname_expect_underscore

; Expect 'c'.
                ld      a, b
                or      a
                jp      z, fail
                ld      a, (hl)
                call    to_lower_ascii
                cp      &63                 ; 'c'
                jp      nz, fail
                inc     hl
                dec     b
                jp      z, fail
; Parse CRm.
                call    sysname_parse_15
                ld      (sysreg_fields + 3), a
                call    sysname_expect_underscore

; Parse op2 (0..7).
                call    sysname_parse_digit
                cp      8
                jp      nc, fail
                ld      (sysreg_fields + 4), a
; B should be 0 now.
                ld      a, b
                or      a
                jp      nz, fail
                ret


; sysname_parse_digit — read one ASCII '0'..'9' digit at HL, return in A.
; Advances HL, decrements B.  jp fail on non-digit or end-of-string.
sysname_parse_digit:
                ld      a, b
                or      a
                jp      z, fail
                ld      a, (hl)
                sub     &30                 ; '0'
                jp      c, fail
                cp      10
                jp      nc, fail
                inc     hl
                dec     b
                ret


; sysname_parse_15 — read 1 or 2 decimal digits, value in 0..15.  Stop
; at '_' or end-of-string.  jp fail on invalid value.
sysname_parse_15:
                call    sysname_parse_digit
                ld      c, a                ; first digit
                ld      a, b
                or      a
                jp      z, sysname_parse_15_done
                ld      a, (hl)
                cp      &5f                 ; '_'
                jp      z, sysname_parse_15_done
; Second digit (0..9).
                sub     &30                 ; '0'
                jp      c, fail
                cp      10
                jp      nc, fail
                ld      d, a                ; second digit
                inc     hl
                dec     b
; Compute C * 10 + D.  C * 10 = C * 8 + C * 2.
                ld      a, c
                add     a, a                ; *2
                ld      e, a
                add     a, a                ; *4
                add     a, a                ; *8
                add     a, e                ; *10
                add     a, d
                cp      16
                jp      nc, fail
                ret
sysname_parse_15_done:
                ld      a, c
                cp      16
                jp      nc, fail
                ret


; sysname_expect_underscore — eat one '_' or jp fail.
sysname_expect_underscore:
                ld      a, b
                or      a
                jp      z, fail
                ld      a, (hl)
                cp      &5f                 ; '_'
                jp      nz, fail
                inc     hl
                dec     b
                ret


; -----------------------------------------------------------------------
; Data tables.  Only entries actually used by current M5 fixtures are
; listed; adding a new sysreg / DC / TLBI op is mechanical (append the
; entry before the 0-terminator).  All names must be lower case (the
; matcher lowercases the source bytes before compare).
; -----------------------------------------------------------------------

; sysreg_table — 12 entries.
; Format per entry: [name_len u8][name_bytes][op0 u8][op1 u8][CRn u8][CRm u8][op2 u8].
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


; -----------------------------------------------------------------------
; Scratch.
; -----------------------------------------------------------------------
sysname_ptr:           defw    0       ; pointer to name bytes in IN_BUF
sysname_len:           defw    0       ; length of name (u16; effective u8)
sysname_field_count:   defb    0       ; bytes to copy on match
sysname_field_dest:    defw    0       ; where to copy matched fields
sysreg_fields:         defs    8       ; 5..8 bytes — packed encoding tuple
