; asmparse.asm — aarch64 assembler-source parser, i48c Brick B2 (text→records).
;
; SAM-side Z80 port of the Go authority's parser:
;   tools/sam-aarch64/frontend/parser.go  (Parse / parseInst / parseOperand /
;   matchReg) feeding tools/sam-aarch64-format (Record / OperandWriter).
;
; The parser is the stage after the lexer (src/asmlex.asm): it turns the token
; stream into the in-memory symbolic record IR (format.Record) that the
; assembler consumes — the editor input path's back half. Brick B2 builds it up
; in sub-bricks:
;   B2a: mnemonic_lookup — turn a lexed mnemonic identifier's bytes back into
;        its on-disk mnemonic ID. The runtime-data counterpart of
;        mnemonic_ids.inc's assemble-time equates.
;   B2b: parse_run / parse_inst — the generic simple-instruction parse.
;        A line whose first token is an identifier is a mnemonic followed by a
;        comma-separated (commas optional, per parser.go) operand list; B2b
;        handles register operands (reg/reg/reg, mov reg/reg, ret, br reg, …),
;        emitting an INST record.
;   B2c (this): the `#imm` operand — `add x0, x1, #4`. A `#`-prefixed or bare
;        integer literal becomes an OP_KIND_IMM_EXPR operand whose expression
;        bytecode is a single folded immediate (port of parseOperand's TokHash/
;        TokInt path → parseExpression constant-fold → ExprWriter.WriteImm).
;        Multi-term/symbol/PC expressions, the `,lsl #12` suffix, mem operands,
;        special-form insts, directives, labels, comments and blank-runs are
;        later bricks (B3-B7) — a line that needs one is outside this domain and
;        sets PARSE_ERR.
;
; INST record (emission form — original framing for the host harness; the
; OPERAND bytes are byte-faithful to format.OperandWriter):
;   mnemonic_id:2 LE | operand_count:1 | operands_len:2 LE | operands[]
; A register operand is `kind:1, reg:1` (kind = OP_KIND_REG_X/W/XSP/WSP); an
; immediate operand is `OP_KIND_IMM_EXPR:1, expr_len:2 LE, expr[]` where expr is
; `PUSH_IMMn:1, value:n LE` (n = 1/2/4/8, the shortest signed fit).
;
; PROVENANCE: algorithmic port of parser.go + format/operands.go; the name→id
; table is generated from the Go authority (tables-gen -mnemonic-names-inc →
; src/mnemonic_names.inc). VERIFICATION: tools/netboot-oracle/z80/asmparse_test.go
; drives mnemonic_lookup + parse_run under koron-go/z80 and compares against the
; authority (format.MnemonicID for the table; a faithful refParse built on the
; real format.OperandWriter for the INST records), with mutation-tested teeth.

                if defined(ASMPARSE_STANDALONE)
                org     &8000
                endif

; OP_KIND_* operand-kind equates (generated; zero runtime bytes) + the lexer
; (lex_run, the TOK_* equates, the LEX_SRC/LEX_TOKS buffers).
                include "tbn_constants.inc"
                include "asmlex.asm"

; Expression-bytecode push-immediate opcodes (ExprOp in
; tools/sam-aarch64-format/expr.go; the existing evaluator src/expr_eval.asm
; dispatches on the same bare values 1..4).
EXPR_PUSH_IMM8:  equ &01
EXPR_PUSH_IMM16: equ &02
EXPR_PUSH_IMM32: equ &03
EXPR_PUSH_IMM64: equ &04

; ===========================================================================
; parse_run — tokenise LEX_SRC (BC = length) and parse it into INST records.
;
; Entry: BC = source byte length; source already written to LEX_SRC.
; Exit:  BC = number of INST records emitted (at PARSE_RECS). PARSE_ERR is
;        non-zero if the lexer erred or a line fell outside B2b's domain.
; ===========================================================================
parse_run:
                call    lex_run             ; tokenize into LEX_TOKS
                ld      hl, LEX_TOKS
                ld      (PARSE_TOK), hl     ; token cursor
                ld      hl, PARSE_RECS
                ld      (PARSE_RECPTR), hl  ; record write ptr
                ld      hl, 0
                ld      (PARSE_RECN), hl    ; records emitted
                xor     a
                ld      (PARSE_ERR), a
                ld      a, (LEX_ERR)        ; a lexical error stops the parse
                or      a
                jr      nz, pr_lexerr
pr_loop:
                ld      hl, (PARSE_TOK)
                ld      a, (hl)             ; current token kind
                cp      TOK_EOF
                jr      z, pr_done
                cp      TOK_EOL
                jr      z, pr_skip          ; B2b has no blank-runs/labels/comments
                cp      TOK_IDENT
                jr      z, pr_inst
                jr      pr_domainerr        ; line-leading non-ident: out of domain
pr_skip:
                call    parse_advance_tok
                jr      pr_loop
pr_inst:
                call    parse_inst
                jr      c, pr_done          ; error -> stop
                jr      pr_loop
pr_domainerr:
pr_lexerr:
                ld      a, 1
                ld      (PARSE_ERR), a
pr_done:
                ld      bc, (PARSE_RECN)
                ret

; ===========================================================================
; parse_inst — parse one instruction line starting at the mnemonic token.
; (Port of parseInst's generic tail loop, parser.go:453-500 — the special-form
; intercepts above it are B5.) Exit: CY clear on success (one INST record
; emitted), CY set on error (PARSE_ERR set).
; ===========================================================================
parse_inst:
                ; Look up the mnemonic from the current token's span.
                ld      hl, (PARSE_TOK)
                inc     hl
                ld      e, (hl)
                inc     hl
                ld      d, (hl)             ; DE = span_ptr
                inc     hl
                ld      c, (hl)             ; C = span_len low (mnemonics < 256)
                ex      de, hl              ; HL = span_ptr
                call    mnemonic_lookup     ; A = found, HL = id
                or      a
                jr      z, pi_err           ; unknown mnemonic -> out of domain
                ld      (PI_MNEMID), hl     ; save id
                call    parse_advance_tok   ; consume the mnemonic
                ld      hl, PARSE_OPSBUF
                ld      (PI_OPSPTR), hl     ; operand-bytes write ptr
                xor     a
                ld      (PI_COUNT), a       ; operand count
pi_loop:
                ld      hl, (PARSE_TOK)
                ld      a, (hl)
                cp      TOK_EOL
                jr      z, pi_emit
                cp      TOK_EOF
                jr      z, pi_emit
                cp      TOK_LINECOMMENT
                jr      z, pi_emit
                cp      TOK_BLOCKCOMMENT
                jr      z, pi_emit
                cp      TOK_COMMA
                jr      z, pi_comma
                call    parse_operand       ; append one operand (register or #imm)
                jr      c, pi_err
                ld      a, (PI_COUNT)
                inc     a
                ld      (PI_COUNT), a
                jr      pi_loop
pi_comma:
                ld      a, (PI_COUNT)
                or      a
                jr      z, pi_err           ; leading comma
                call    parse_advance_tok   ; commas are optional separators
                jr      pi_loop
pi_emit:
                call    parse_emit_inst
                or      a                   ; clear carry: success
                ret
pi_err:
                ld      a, 1
                ld      (PARSE_ERR), a
                scf
                ret

; ===========================================================================
; parse_operand — dispatch one operand at the current token. (Port of
; parseOperand's switch, parser.go:1023-1090, restricted to the register and
; #imm shapes.) A register identifier -> parse_operand_reg; a `#`-prefixed or
; bare integer literal -> parse_operand_imm; anything else (shifted/extended
; reg, mem operand, symbol/expression immediate) is a later brick -> CY set.
; ===========================================================================
parse_operand:
                ld      hl, (PARSE_TOK)
                ld      a, (hl)
                cp      TOK_HASH
                jr      z, po_hash
                cp      TOK_INT
                jp      z, parse_operand_imm
                cp      TOK_IDENT
                jr      z, parse_operand_reg
                scf                         ; out of domain
                ret
po_hash:
                call    parse_advance_tok   ; consume '#'
                ld      hl, (PARSE_TOK)
                ld      a, (hl)
                cp      TOK_INT
                jp      z, parse_operand_imm
                scf                         ; '#' not followed by an int -> B3
                ret

; ===========================================================================
; parse_operand_reg — parse one register operand at the current token and
; append its `kind:1, reg:1` bytes to (PI_OPSPTR). (Port of parseOperand's
; register path, parser.go:1032-1073, minus the shift/extend lookahead which
; is a later brick.) Exit: CY clear on success (token consumed), CY set on a
; non-register operand.
; ===========================================================================
parse_operand_reg:
                ld      hl, (PARSE_TOK)
                ld      a, (hl)
                cp      TOK_IDENT
                jr      nz, por_err         ; B2b only handles register idents
                inc     hl
                ld      e, (hl)
                inc     hl
                ld      d, (hl)             ; DE = span_ptr
                inc     hl
                ld      a, (hl)             ; A = span_len low (reg names < 256)
                ex      de, hl              ; HL = span_ptr
                call    match_reg           ; CY = is-reg, B = kind, C = reg
                jr      nc, por_err
                ld      hl, (PI_OPSPTR)
                ld      (hl), b             ; operand kind
                inc     hl
                ld      (hl), c             ; register number
                inc     hl
                ld      (PI_OPSPTR), hl
                call    parse_advance_tok
                or      a                   ; clear carry: success
                ret
por_err:
                scf
                ret

; ===========================================================================
; parse_operand_imm — parse one immediate operand: the current token is a
; TOK_INT (the `#` prefix, if any, is already consumed). Append an
; OP_KIND_IMM_EXPR operand whose expression bytecode is the single folded
; immediate `PUSH_IMMn, value[n]` (port of parseOperand's TokInt path ->
; parseExpression constant-fold -> ExprWriter.WriteImm). Always succeeds (CY
; clear); the lexer already validated and computed the int64 value.
; ===========================================================================
parse_operand_imm:
                ; copy the token's 8-byte value (record offset 6) to IMM_VAL.
                ld      hl, (PARSE_TOK)
                ld      de, 6
                add     hl, de
                ld      de, IMM_VAL
                ld      bc, 8
                ldir
                call    expr_imm_width      ; A = PUSH_IMMn opcode, B = n
                ld      (IMM_OP), a
                ld      a, b
                ld      (IMM_N), a
                ld      hl, (PI_OPSPTR)
                ld      (hl), OP_KIND_IMM_EXPR
                inc     hl
                ld      a, (IMM_N)
                inc     a                   ; expr_len = 1 (opcode) + n
                ld      (hl), a             ; expr_len low
                inc     hl
                ld      (hl), 0             ; expr_len high (expr is <= 9 bytes)
                inc     hl
                ld      a, (IMM_OP)
                ld      (hl), a             ; PUSH_IMMn opcode
                inc     hl
                ex      de, hl              ; DE = dest (after the opcode)
                ld      hl, IMM_VAL         ; source = the low n value bytes
                ld      a, (IMM_N)
                ld      c, a
                ld      b, 0
                ldir                        ; copy n value bytes; DE -> end
                ld      (PI_OPSPTR), de
                call    parse_advance_tok   ; consume the int token
                or      a                   ; clear carry: success
                ret

; ===========================================================================
; expr_imm_width — choose the shortest signed PUSH_IMMn for the 8-byte LE value
; in IMM_VAL. (Port of ExprWriter.WriteImm's range ladder, expr.go:140-154.)
; Exit: A = PUSH_IMMn opcode, B = n (1/2/4/8). A value fits n signed bytes iff
; the bytes above byte n-1 are all the sign extension of byte n-1's MSB.
; ===========================================================================
expr_imm_width:
                ld      a, (IMM_VAL)        ; int8: IMM_VAL[1..7] == signext(byte0)?
                call    signext_of_a
                ld      c, a
                ld      hl, IMM_VAL+1
                ld      b, 7
                call    all_equal_c
                jr      z, eiw_imm8
                ld      a, (IMM_VAL+1)      ; int16: IMM_VAL[2..7] == signext(byte1)?
                call    signext_of_a
                ld      c, a
                ld      hl, IMM_VAL+2
                ld      b, 6
                call    all_equal_c
                jr      z, eiw_imm16
                ld      a, (IMM_VAL+3)      ; int32: IMM_VAL[4..7] == signext(byte3)?
                call    signext_of_a
                ld      c, a
                ld      hl, IMM_VAL+4
                ld      b, 4
                call    all_equal_c
                jr      z, eiw_imm32
                ld      a, EXPR_PUSH_IMM64
                ld      b, 8
                ret
eiw_imm8:
                ld      a, EXPR_PUSH_IMM8
                ld      b, 1
                ret
eiw_imm16:
                ld      a, EXPR_PUSH_IMM16
                ld      b, 2
                ret
eiw_imm32:
                ld      a, EXPR_PUSH_IMM32
                ld      b, 4
                ret

; signext_of_a — A = byte; return A = 0x00 (bit7 clear) or 0xFF (bit7 set).
signext_of_a:
                add     a, a                ; bit7 -> carry
                sbc     a, a                ; A = 0x00 or 0xFF
                ret

; all_equal_c — HL = ptr, B = count (>0), C = expected byte. Returns ZF set iff
; all `count` bytes equal C. Clobbers A, HL, B.
all_equal_c:
                ld      a, (hl)
                cp      c
                ret     nz
                inc     hl
                djnz    all_equal_c
                ret                         ; last cp left ZF set (equal)

; ===========================================================================
; match_reg — is the identifier (HL = ptr, A = length) a register name?
; (Port of matchReg, parser.go:1096-1132.) Exit: CY set with B = OperandKind
; and C = register number if a register; CY clear otherwise. Clobbers A/DE/HL.
; ===========================================================================
match_reg:
                ld      (MR_PTR), hl
                ld      (MR_LEN), a
                ; --- named specials (sp/wsp/xzr/wzr/fp/lr) ---
                ld      de, REGSPECIALS
mr_spec_loop:
                ld      a, (de)             ; special name length (0 = end)
                or      a
                jr      z, mr_xw            ; no special matched -> try x/w form
                ld      b, a                ; B = special name length
                ld      a, (MR_LEN)
                cp      b
                jr      nz, mr_spec_next    ; length mismatch
                push    de
                inc     de                  ; DE -> special name bytes
                ld      hl, (MR_PTR)
mr_spec_cmp:
                ld      a, (de)
                cp      (hl)
                jr      nz, mr_spec_cmpfail
                inc     de
                inc     hl
                djnz    mr_spec_cmp
                ; matched: DE -> kind byte (just past the name)
                ld      a, (de)
                ld      b, a                ; B = kind
                inc     de
                ld      a, (de)
                ld      c, a                ; C = reg
                pop     de                  ; balance the stack
                scf
                ret
mr_spec_cmpfail:
                pop     de                  ; restore entry ptr (length byte)
mr_spec_next:
                ; advance DE past: length(1) + name(len) + kind(1) + reg(1)
                ld      a, (de)
                add     a, 3
                add     a, e
                ld      e, a
                jr      nc, mr_spec_nc
                inc     d
mr_spec_nc:
                jr      mr_spec_loop
mr_xw:
                ; --- x<N> / w<N>, N in 0..30 ---
                ld      a, (MR_LEN)
                cp      2
                jr      c, mr_notreg        ; need a prefix + at least one digit
                ld      hl, (MR_PTR)
                ld      a, (hl)
                cp      &78                 ; 'x'
                jr      z, mr_xw_x
                cp      &77                 ; 'w'
                jr      z, mr_xw_w
                jr      mr_notreg
mr_xw_x:
                ld      b, OP_KIND_REG_X
                jr      mr_xw_num
mr_xw_w:
                ld      b, OP_KIND_REG_W
mr_xw_num:
                inc     hl                  ; HL -> first digit
                ld      a, (MR_LEN)
                dec     a
                ld      d, a                ; D = remaining digit count (>= 1)
                ld      c, 0                ; C = accumulated number
mr_num_loop:
                ld      a, (hl)
                sub     &30                 ; '0'
                jr      c, mr_notreg        ; char < '0'
                cp      10
                jr      nc, mr_notreg       ; char > '9'
                ld      e, a                ; E = digit value
                ld      a, c                ; A = number
                add     a, a                ; 2n
                ld      c, a                ; keep 2n
                add     a, a                ; 4n
                add     a, a                ; 8n
                add     a, c                ; 10n
                jr      c, mr_notreg        ; 10n overflowed a byte -> > 30
                add     a, e                ; + digit
                jr      c, mr_notreg
                cp      31
                jr      nc, mr_notreg       ; number > 30
                ld      c, a                ; C = number
                inc     hl
                dec     d
                jr      nz, mr_num_loop
                scf                         ; B = kind, C = number
                ret
mr_notreg:
                or      a                   ; clear carry
                ret

; ===========================================================================
; parse_emit_inst — write one INST record from PI_MNEMID / PI_COUNT / the
; operand bytes in PARSE_OPSBUF, advancing PARSE_RECPTR and PARSE_RECN.
; ===========================================================================
parse_emit_inst:
                ld      hl, (PI_OPSPTR)
                ld      de, PARSE_OPSBUF
                or      a
                sbc     hl, de              ; HL = operand byte length
                ld      b, h
                ld      c, l                ; BC = operand byte length
                ld      hl, (PARSE_RECPTR)
                ld      a, (PI_MNEMID)
                ld      (hl), a
                inc     hl
                ld      a, (PI_MNEMID+1)
                ld      (hl), a
                inc     hl
                ld      a, (PI_COUNT)
                ld      (hl), a             ; operand count
                inc     hl
                ld      (hl), c             ; operands_len low
                inc     hl
                ld      (hl), b             ; operands_len high
                inc     hl
                ex      de, hl              ; DE = record write ptr (after header)
                ld      hl, PARSE_OPSBUF    ; HL = source operand bytes
                ld      a, b
                or      c
                jr      z, pei_nocopy       ; zero-length operand stream
                ldir                        ; copy BC bytes -> DE advances to end
pei_nocopy:
                ld      (PARSE_RECPTR), de  ; DE = end of record
                ld      hl, (PARSE_RECN)
                inc     hl
                ld      (PARSE_RECN), hl
                ret

; ===========================================================================
; parse_advance_tok — step the token cursor to the next 14-byte token record.
; ===========================================================================
parse_advance_tok:
                ld      hl, (PARSE_TOK)
                ld      de, TOK_REC_SIZE
                add     hl, de
                ld      (PARSE_TOK), hl
                ret

; ===========================================================================
; mnemonic_lookup — map a mnemonic name's bytes to its on-disk mnemonic ID.
; Port of format.MnemonicID (a name→index map lookup); the Z80 walks the
; generated MNEM_NAMES table linearly, since the table is small (~103 entries)
; and the parser looks up one mnemonic per source line.
;
; Entry: HL = pointer to the candidate name bytes; C = name length (B ignored —
;        mnemonic names are 1..255 bytes; the lexer never produces a longer
;        single identifier than the buffer holds).
; Exit:  A = 1 and HL = mnemonic ID (the table index)   if the name matches;
;        A = 0 (HL undefined)                            if not found.
;        BC/DE clobbered.
; ===========================================================================
mnemonic_lookup:
                ld      (ML_PTR), hl        ; save candidate pointer
                ld      a, c
                ld      (ML_LEN), a         ; save candidate length
                ld      de, MNEM_NAMES
                ld      hl, 0               ; HL = running index = candidate id
ml_loop:
                ld      a, (de)             ; entry length (0 = sentinel)
                or      a
                jr      z, ml_notfound
                ld      b, a                ; B = entry length
                ld      a, (ML_LEN)
                cp      b
                jr      nz, ml_next         ; length mismatch -> skip entry
                ; Lengths match: compare B bytes of entry name vs candidate.
                push    hl                  ; save index
                push    de                  ; save entry pointer (at length byte)
                inc     de                  ; DE -> entry name bytes
                ld      hl, (ML_PTR)        ; HL -> candidate bytes
ml_cmp:
                ld      a, (de)
                cp      (hl)
                jr      nz, ml_cmp_fail
                inc     de
                inc     hl
                djnz    ml_cmp
                ; All bytes matched.
                pop     de                  ; discard saved entry pointer
                pop     hl                  ; HL = index = id
                ld      a, 1
                ret
ml_cmp_fail:
                pop     de                  ; restore entry pointer (length byte)
                pop     hl                  ; restore index
ml_next:
                ; Advance DE past this record: skip the length byte + name bytes.
                ld      a, (de)             ; entry length
                inc     de                  ; past the length byte
                add     a, e
                ld      e, a
                jr      nc, ml_next_nc
                inc     d
ml_next_nc:
                inc     hl                  ; index++
                jr      ml_loop
ml_notfound:
                xor     a                   ; A = 0 (not found)
                ret

; ===========================================================================
; Generated name→id table (do not edit; regen with `make tables`).
; ===========================================================================
                include "mnemonic_names.inc"

; ===========================================================================
; REGSPECIALS — the named special registers matchReg recognises (parser.go:
; 1098-1109). Record: len:1 | name | kind:1 | reg:1; a 0-length record ends it.
; ===========================================================================
REGSPECIALS:
                defb    2
                defm    "sp"
                defb    OP_KIND_REG_XSP, 31
                defb    3
                defm    "wsp"
                defb    OP_KIND_REG_WSP, 31
                defb    3
                defm    "xzr"
                defb    OP_KIND_REG_X, 31
                defb    3
                defm    "wzr"
                defb    OP_KIND_REG_W, 31
                defb    2
                defm    "fp"
                defb    OP_KIND_REG_X, 29
                defb    2
                defm    "lr"
                defb    OP_KIND_REG_X, 30
                defb    0                   ; sentinel

; ===========================================================================
; Working storage
; ===========================================================================
ML_PTR:         defs 2          ; mnemonic_lookup: saved candidate pointer
ML_LEN:         defs 1          ; mnemonic_lookup: saved candidate length
MR_PTR:         defs 2          ; match_reg: saved candidate pointer
MR_LEN:         defs 1          ; match_reg: saved candidate length
PARSE_TOK:      defs 2          ; current token-record pointer (into LEX_TOKS)
PARSE_RECPTR:   defs 2          ; current INST-record write pointer
PARSE_RECN:     defs 2          ; INST records emitted so far
PI_MNEMID:      defs 2          ; parse_inst: current mnemonic ID
PI_OPSPTR:      defs 2          ; parse_inst: operand-bytes write pointer
PI_COUNT:       defs 1          ; parse_inst: operand count
IMM_VAL:        defs 8          ; parse_operand_imm: the immediate's int64 (LE)
IMM_OP:         defs 1          ; parse_operand_imm: chosen PUSH_IMMn opcode
IMM_N:          defs 1          ; parse_operand_imm: value byte count (1/2/4/8)
PARSE_OPSBUF:   defs 256        ; one instruction's operand bytes (staging)

; ===========================================================================
; Public I/O buffers
; ===========================================================================
PARSE_ERR:      defs 1          ; non-zero after a parse error / out-of-domain line
AP_NAMEBUF:     defs 32         ; scratch the harness fills with a candidate name
PARSE_RECS:     defs 2048       ; emitted INST record stream (harness reads here)
