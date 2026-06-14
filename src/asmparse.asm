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
;   B2c: the `#imm` operand — `add x0, x1, #4`. A `#`-prefixed or bare integer
;        literal becomes an OP_KIND_IMM_EXPR operand whose expression bytecode is
;        a single folded immediate.
;   B3a: the constant-expression operand — `add x0, x1, #4+1`, `mov x0, ~0`,
;        `mov x0, -(1+2)&7`. A `#`/int/`-`/`~`/`(`-leading operand becomes an
;        OP_KIND_IMM_EXPR whose bytecode is built by a precedence-climbing parser
;        (port of parseExprPrec / parseExprPrimary / tokPrec, parser.go:1149-1290)
;        and then collapsed to a single folded immediate by a self-contained
;        constant evaluator (port of format.EvalConst, expr.go:223). B3a covers
;        the operators `+ - & | ^` and the unary `- ~` plus parens.
;   B3a2: the shift operators `<< >>` (precedence 2) — `mov x0, #1<<8`,
;        `mov x0, ~0>>1`. The fold runs a variable-count 64-bit shift: logical
;        left and arithmetic right, with Go's >=64-count clamp (a<<n -> 0;
;        a>>n -> sign extension).
;   B3a3 (this): the multiply operator `*` (precedence 4) — `mov x0, #2*3`,
;        `add x0, x1, #(1+2)*4`. The fold runs a 64-bit shift-add multiply
;        keeping the low 64 bits (port of applyBinary(OpMul): a*b wraps mod
;        2^64; signed and unsigned agree on the low word). Division `/` (B3a4)
;        and the non-constant primaries (symbol idents, `.`, local-refs,
;        `:reloc:`, B3b/B3c) remain later sub-bricks. The `,lsl #12` suffix, mem
;        operands, special-form insts, directives, labels, comments and
;        blank-runs are later bricks (B4-B7) — a line that needs one is outside
;        this domain and sets PARSE_ERR.
;
; INST record (emission form — original framing for the host harness; the
; OPERAND bytes are byte-faithful to format.OperandWriter):
;   mnemonic_id:2 LE | operand_count:1 | operands_len:2 LE | operands[]
; A register operand is `kind:1, reg:1` (kind = OP_KIND_REG_X/W/XSP/WSP); an
; immediate operand is `OP_KIND_IMM_EXPR:1, expr_len:2 LE, expr[]` where expr is
; `PUSH_IMMn:1, value:n LE` (n = 1/2/4/8, the shortest signed fit).
;
; PROVENANCE: algorithmic port of parser.go (parseInst / parseOperand /
; parseExprPrec / parseExprPrimary / tokPrec) + format/operands.go + format/
; expr.go (ExprWriter / EvalConst); the name→id table is generated from the Go
; authority (tables-gen -mnemonic-names-inc → src/mnemonic_names.inc).
; VERIFICATION: tools/netboot-oracle/z80/asmparse_test.go drives mnemonic_lookup
; + parse_run under koron-go/z80 and compares against the authority
; (format.MnemonicID for the table; a faithful refParse built on the real
; format.OperandWriter / format.ExprWriter / format.EvalConst for the INST
; records), with mutation-tested teeth.

                if defined(ASMPARSE_STANDALONE)
                org     &8000
                endif

; OP_KIND_* operand-kind equates (generated; zero runtime bytes) + the lexer
; (lex_run, the TOK_* equates, the LEX_SRC/LEX_TOKS buffers).
                include "tbn_constants.inc"
                include "asmlex.asm"

; Expression-bytecode opcodes (ExprOp in tools/sam-aarch64-format/expr.go; the
; existing evaluator src/expr_eval.asm dispatches on the same bare values).
EXPR_PUSH_IMM8:  equ &01
EXPR_PUSH_IMM16: equ &02
EXPR_PUSH_IMM32: equ &03
EXPR_PUSH_IMM64: equ &04
EXPR_OP_ADD:     equ &10
EXPR_OP_SUB:     equ &11
EXPR_OP_MUL:     equ &12
EXPR_OP_AND:     equ &14
EXPR_OP_OR:      equ &15
EXPR_OP_XOR:     equ &16
EXPR_OP_SHL:     equ &17
EXPR_OP_SHR:     equ &18
EXPR_OP_NEG:     equ &20
EXPR_OP_NOT:     equ &21

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
; constant-expression shapes.) A register identifier -> parse_operand_reg; an
; expression-leading token (`#` / int / unary `-` / `~` / `(`) -> the constant
; expression path parse_operand_expr. The non-register identifier (a symbol),
; `.`, local-refs and `:reloc:` are later bricks (B3b/B3c); the shift/extend
; suffix and mem operands are B4 — all set CY (out of domain).
; ===========================================================================
parse_operand:
                ld      hl, (PARSE_TOK)
                ld      a, (hl)
                cp      TOK_IDENT
                jp      z, parse_operand_reg ; register; non-reg ident -> B3b
                cp      TOK_HASH
                jr      z, parse_operand_expr
                cp      TOK_INT
                jr      z, parse_operand_expr
                cp      TOK_MINUS
                jr      z, parse_operand_expr
                cp      TOK_TILDE
                jr      z, parse_operand_expr
                cp      TOK_LPAREN
                jr      z, parse_operand_expr
                scf                         ; out of domain (.,localref,:,[ -> later)
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
; parse_operand_expr — parse one constant-expression operand. The current token
; leads an expression (`#` / int / `-` / `~` / `(`). Build the expression
; bytecode into EXPR_BUF via the precedence-climbing parser, collapse it to a
; single folded immediate (every B3a expression is pure-constant), then append
; an `OP_KIND_IMM_EXPR:1, expr_len:2 LE, expr[]` operand at (PI_OPSPTR). (Port
; of parseOperand's expression path -> parseExpression -> WriteImmExpr.)
; Exit: CY clear on success (tokens consumed), CY set on a parse error.
; ===========================================================================
parse_operand_expr:
                ld      hl, EXPR_BUF
                ld      (EXPR_PTR), hl      ; reset the bytecode build buffer
                xor     a                   ; minPrec = 0
                call    parse_expr_prec
                ret     c                   ; parse error -> propagate CY
                call    expr_fold           ; collapse a pure-constant stream
                ; expr_len = EXPR_PTR - EXPR_BUF
                ld      hl, (EXPR_PTR)
                ld      de, EXPR_BUF
                or      a
                sbc     hl, de
                ld      b, h
                ld      c, l                ; BC = expr byte length
                ld      hl, (PI_OPSPTR)
                ld      (hl), OP_KIND_IMM_EXPR
                inc     hl
                ld      (hl), c             ; expr_len low
                inc     hl
                ld      (hl), b             ; expr_len high
                inc     hl
                ex      de, hl              ; DE = operand dest (after the header)
                ld      hl, EXPR_BUF        ; HL = source bytecode
                ld      a, b
                or      c
                jr      z, poe_nocopy       ; (defensive) zero-length expr
                ldir                        ; copy expr bytes; DE -> end
poe_nocopy:
                ld      (PI_OPSPTR), de
                or      a                   ; clear carry: success
                ret

; ===========================================================================
; parse_expr_prec — precedence-climbing expression parse. (Port of
; parseExprPrec, parser.go:1170-1201.) Entry: A = minPrec. Emits expression
; bytecode into (EXPR_PTR), consuming tokens. Exit: CY set on a parse error,
; CY clear on success. Recursive (depth bounded by the precedence levels and
; nesting of one operand expression).
; ===========================================================================
parse_expr_prec:
                push    af                  ; save minPrec (high byte of AF)
                call    parse_expr_primary
                jr      c, pep_err1
pep_loop:
                ld      hl, (PARSE_TOK)
                ld      a, (hl)             ; current token kind
                call    tok_prec            ; A = precedence (0xFF if not an op)
                cp      &FF
                jr      z, pep_done
                pop     bc                  ; B = minPrec
                push    bc                  ; keep it on the stack
                cp      b
                jr      c, pep_done         ; prec < minPrec -> expression ends
                ; the operator binds: remember its kind, recurse with prec+1.
                ld      hl, (PARSE_TOK)
                ld      c, (hl)             ; C = operator token kind
                inc     a                   ; A = prec + 1 (recursion's minPrec)
                push    bc                  ; save operator kind (in C)
                ld      hl, (PARSE_TOK)     ; advance past the operator token
                ld      de, TOK_REC_SIZE
                add     hl, de
                ld      (PARSE_TOK), hl
                call    parse_expr_prec     ; right operand (minPrec = A)
                jr      c, pep_err2
                pop     bc                  ; C = operator kind
                ld      a, c
                call    expr_emit_binop     ; emit the ExprOp for this operator
                jr      c, pep_err1
                jr      pep_loop
pep_done:
                pop     af                  ; discard minPrec
                or      a                   ; clear carry: success
                ret
pep_err2:
                pop     bc                  ; discard saved operator kind
pep_err1:
                pop     af                  ; discard minPrec
                scf
                ret

; ===========================================================================
; parse_expr_primary — parse one primary at the current token. (Port of
; parseExprPrimary, parser.go:1203-1290, B3a subset.) Emits bytecode into
; (EXPR_PTR). Exit: CY set on error. Handles `#` (consume + recurse), an
; integer literal, unary `-`/`~`, and a parenthesised sub-expression. The
; symbol-ident, `.`, local-ref and `:reloc:` primaries are later bricks.
; ===========================================================================
parse_expr_primary:
                ld      hl, (PARSE_TOK)
                ld      a, (hl)
                cp      TOK_HASH
                jr      z, ppr_hash
                cp      TOK_INT
                jr      z, ppr_int
                cp      TOK_MINUS
                jr      z, ppr_neg
                cp      TOK_TILDE
                jr      z, ppr_not
                cp      TOK_LPAREN
                jr      z, ppr_paren
                scf                         ; not a B3a primary -> error
                ret
ppr_hash:
                call    parse_advance_tok   ; consume '#'
                jr      parse_expr_primary  ; tail-recurse on the next primary
ppr_int:
                call    expr_emit_imm_from_tok
                call    parse_advance_tok   ; consume the int token
                or      a                   ; clear carry: success
                ret
ppr_neg:
                call    parse_advance_tok   ; consume '-'
                call    parse_expr_primary
                ret     c
                ld      a, EXPR_OP_NEG
                call    expr_emit_byte
                or      a
                ret
ppr_not:
                call    parse_advance_tok   ; consume '~'
                call    parse_expr_primary
                ret     c
                ld      a, EXPR_OP_NOT
                call    expr_emit_byte
                or      a
                ret
ppr_paren:
                call    parse_advance_tok   ; consume '('
                xor     a                   ; minPrec = 0 inside the parens
                call    parse_expr_prec
                ret     c
                ld      hl, (PARSE_TOK)
                ld      a, (hl)
                cp      TOK_RPAREN
                jr      nz, ppr_err
                call    parse_advance_tok   ; consume ')'
                or      a
                ret
ppr_err:
                scf
                ret

; ===========================================================================
; tok_prec — binary-operator precedence for a token kind. (Port of tokPrec,
; parser.go:1149-1163.) Entry: A = token kind. Exit: A = precedence, or 0xFF
; when the token is not yet a binary operator (the `* /` operators are later
; sub-bricks, so they read as not-an-operator here and end the expression).
; Clobbers F only.
; ===========================================================================
tok_prec:
                cp      TOK_PIPE
                jr      z, tp_0
                cp      TOK_CARET
                jr      z, tp_0
                cp      TOK_AMP
                jr      z, tp_1
                cp      TOK_SHL
                jr      z, tp_2
                cp      TOK_SHR
                jr      z, tp_2
                cp      TOK_PLUS
                jr      z, tp_3
                cp      TOK_MINUS
                jr      z, tp_3
                cp      TOK_STAR
                jr      z, tp_4
                ld      a, &FF              ; not yet an operator
                ret
tp_0:           xor     a                   ; precedence 0 (| ^)
                ret
tp_1:           ld      a, 1                ; precedence 1 (&)
                ret
tp_2:           ld      a, 2                ; precedence 2 (<< >>)
                ret
tp_3:           ld      a, 3                ; precedence 3 (+ -)
                ret
tp_4:           ld      a, 4                ; precedence 4 (*)
                ret

; ===========================================================================
; expr_emit_binop — emit the ExprOp byte for a binary-operator token kind.
; Entry: A = token kind. Exit: CY clear on success, CY set for an unsupported
; operator (cannot happen: tok_prec gates which operators reach here).
; ===========================================================================
expr_emit_binop:
                cp      TOK_PLUS
                jr      z, eeb_add
                cp      TOK_MINUS
                jr      z, eeb_sub
                cp      TOK_AMP
                jr      z, eeb_and
                cp      TOK_PIPE
                jr      z, eeb_or
                cp      TOK_CARET
                jr      z, eeb_xor
                cp      TOK_SHL
                jr      z, eeb_shl
                cp      TOK_SHR
                jr      z, eeb_shr
                cp      TOK_STAR
                jr      z, eeb_mul
                scf
                ret
eeb_mul:        ld      a, EXPR_OP_MUL
                jr      eeb_emit
eeb_add:        ld      a, EXPR_OP_ADD
                jr      eeb_emit
eeb_sub:        ld      a, EXPR_OP_SUB
                jr      eeb_emit
eeb_and:        ld      a, EXPR_OP_AND
                jr      eeb_emit
eeb_or:         ld      a, EXPR_OP_OR
                jr      eeb_emit
eeb_xor:        ld      a, EXPR_OP_XOR
                jr      eeb_emit
eeb_shl:        ld      a, EXPR_OP_SHL
                jr      eeb_emit
eeb_shr:        ld      a, EXPR_OP_SHR
eeb_emit:
                call    expr_emit_byte
                or      a                   ; clear carry: success
                ret

; ===========================================================================
; expr_emit_byte — append A to EXPR_BUF, advancing (EXPR_PTR). Clobbers HL.
; ===========================================================================
expr_emit_byte:
                ld      hl, (EXPR_PTR)
                ld      (hl), a
                inc     hl
                ld      (EXPR_PTR), hl
                ret

; ===========================================================================
; expr_emit_imm_from_tok — emit the shortest PUSH_IMMn for the current INT
; token's int64 value (record offset 6). (Port of ExprWriter.WriteImm via the
; TokInt primary.) Clobbers A/BC/DE/HL.
; ===========================================================================
expr_emit_imm_from_tok:
                ld      hl, (PARSE_TOK)
                ld      de, 6
                add     hl, de
                ld      de, IMM_VAL
                ld      bc, 8
                ldir                        ; IMM_VAL = token's int64 (LE)
                ; fall through

; ===========================================================================
; expr_emit_imm_from_immval — emit the shortest PUSH_IMMn for the 8-byte LE
; value in IMM_VAL, appending to EXPR_BUF. (Port of ExprWriter.WriteImm.)
; Clobbers A/BC/DE/HL.
; ===========================================================================
expr_emit_imm_from_immval:
                call    expr_imm_width      ; A = PUSH_IMMn opcode, B = n
                ld      hl, (EXPR_PTR)
                ld      (hl), a             ; opcode
                inc     hl
                ld      c, b                ; BC = n (value byte count)
                ld      b, 0
                ld      de, IMM_VAL
                ex      de, hl              ; HL = IMM_VAL (src), DE = dest
                ldir                        ; copy n value bytes; DE -> end
                ld      (EXPR_PTR), de
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
; expr_fold — constant-fold the bytecode in EXPR_BUF..EXPR_PTR. (Port of
; parseExpression's fold step: format.EvalConst + a re-emitted WriteImm,
; parser.go:1133-1147.) If every push is a literal and the stream reduces to
; exactly one value, EXPR_BUF is rewritten to that single folded PUSH_IMMn and
; EXPR_PTR updated. Otherwise (any PUSH_SYM/PC/LOCAL/REL or a malformed stream)
; the raw bytecode is kept unchanged. Never errors. The value stack is 8-byte
; LE int64 cells in FOLD_STACK; FOLD_SP points past the top cell.
; ===========================================================================
expr_fold:
                ld      hl, FOLD_STACK
                ld      (FOLD_SP), hl       ; empty value stack
                ld      hl, EXPR_BUF
                ld      (FOLD_CUR), hl      ; bytecode read cursor
fold_loop:
                ld      hl, (FOLD_CUR)
                ld      de, (EXPR_PTR)
                or      a
                sbc     hl, de
                jr      nc, fold_end        ; cursor >= end -> stream consumed
                ld      hl, (FOLD_CUR)
                ld      a, (hl)             ; next opcode
                inc     hl
                ld      (FOLD_CUR), hl
                cp      EXPR_PUSH_IMM8
                jp      z, fold_push8
                cp      EXPR_PUSH_IMM16
                jp      z, fold_push16
                cp      EXPR_PUSH_IMM32
                jp      z, fold_push32
                cp      EXPR_PUSH_IMM64
                jp      z, fold_push64
                cp      EXPR_OP_ADD
                jp      z, fold_do_add
                cp      EXPR_OP_SUB
                jp      z, fold_do_sub
                cp      EXPR_OP_MUL
                jp      z, fold_do_mul
                cp      EXPR_OP_AND
                jp      z, fold_do_and
                cp      EXPR_OP_OR
                jp      z, fold_do_or
                cp      EXPR_OP_XOR
                jp      z, fold_do_xor
                cp      EXPR_OP_SHL
                jp      z, fold_do_shl
                cp      EXPR_OP_SHR
                jp      z, fold_do_shr
                cp      EXPR_OP_NEG
                jp      z, fold_do_neg
                cp      EXPR_OP_NOT
                jp      z, fold_do_not
                jr      fold_fail           ; non-constant / unknown -> keep raw
fold_end:
                ; success requires exactly one value on the stack.
                ld      hl, (FOLD_SP)
                ld      de, FOLD_STACK
                or      a
                sbc     hl, de              ; HL = used byte count
                ld      de, 8
                or      a
                sbc     hl, de
                jr      nz, fold_fail       ; not exactly one cell -> keep raw
                ld      hl, FOLD_STACK      ; the single result value
                ld      de, IMM_VAL
                ld      bc, 8
                ldir
                ld      hl, EXPR_BUF        ; rewrite EXPR_BUF as one folded imm
                ld      (EXPR_PTR), hl
                jp      expr_emit_imm_from_immval
fold_fail:
                ret                         ; keep the raw bytecode as-is

; fold_push8/16/32/64 — push a sign-extended literal from the bytecode onto the
; value stack and advance FOLD_CUR past its operand bytes.
fold_push8:
                ld      b, 1
                jr      fold_push_n
fold_push16:
                ld      b, 2
                jr      fold_push_n
fold_push32:
                ld      b, 4
                jr      fold_push_n
fold_push64:
                ld      b, 8
fold_push_n:                                ; B = operand width (1/2/4/8)
                ld      hl, (FOLD_SP)       ; dest = top free cell
                ld      de, (FOLD_CUR)      ; src = operand bytes
                ld      c, b
fpn_copy:
                ld      a, (de)
                ld      (hl), a
                inc     de
                inc     hl
                dec     c
                jr      nz, fpn_copy
                ld      (FOLD_CUR), de      ; advance past the operand bytes
                dec     hl                  ; HL -> last value byte
                ld      a, (hl)
                inc     hl                  ; HL -> first fill byte (base + width)
                add     a, a                ; bit7 -> carry
                sbc     a, a                ; A = 0x00 / 0xFF sign fill
                ld      d, a
                ld      a, 8
                sub     b                   ; A = 8 - width = fill count
                jr      z, fpn_done
                ld      b, a
fpn_fill:
                ld      (hl), d
                inc     hl
                djnz    fpn_fill
fpn_done:
                ld      (FOLD_SP), hl       ; HL = base + 8 = new top
                jp      fold_loop

; fold_do_* — binary/unary operations on the value stack (8-byte LE int64).
fold_do_add:
                call    fold_ab_ptrs        ; HL -> a, DE -> b; CY on underflow
                jp      c, fold_fail
                or      a                   ; clear carry
                ld      b, 8
fda_loop:
                ld      a, (de)
                ld      c, a
                ld      a, (hl)
                adc     a, c
                ld      (hl), a
                inc     hl
                inc     de
                djnz    fda_loop
                call    fold_pop1
                jp      fold_loop
fold_do_sub:
                call    fold_ab_ptrs
                jp      c, fold_fail
                or      a                   ; clear borrow
                ld      b, 8
fds_loop:
                ld      a, (de)
                ld      c, a
                ld      a, (hl)
                sbc     a, c
                ld      (hl), a
                inc     hl
                inc     de
                djnz    fds_loop
                call    fold_pop1
                jp      fold_loop
fold_do_and:
                call    fold_ab_ptrs
                jp      c, fold_fail
                ld      b, 8
fan_loop:
                ld      a, (de)
                and     (hl)
                ld      (hl), a
                inc     hl
                inc     de
                djnz    fan_loop
                call    fold_pop1
                jp      fold_loop
fold_do_or:
                call    fold_ab_ptrs
                jp      c, fold_fail
                ld      b, 8
for_loop:
                ld      a, (de)
                or      (hl)
                ld      (hl), a
                inc     hl
                inc     de
                djnz    for_loop
                call    fold_pop1
                jp      fold_loop
fold_do_xor:
                call    fold_ab_ptrs
                jp      c, fold_fail
                ld      b, 8
fxr_loop:
                ld      a, (de)
                xor     (hl)
                ld      (hl), a
                inc     hl
                inc     de
                djnz    fxr_loop
                call    fold_pop1
                jp      fold_loop
fold_do_neg:
                call    fold_top_ptr        ; HL -> top; CY on underflow
                jp      c, fold_fail
                or      a                   ; clear borrow
                ld      b, 8
fne_loop:
                ld      a, 0
                sbc     a, (hl)             ; 0 - x with borrow = two's complement
                ld      (hl), a
                inc     hl
                djnz    fne_loop
                jp      fold_loop
fold_do_not:
                call    fold_top_ptr
                jp      c, fold_fail
                ld      b, 8
fno_loop:
                ld      a, (hl)
                cpl
                ld      (hl), a
                inc     hl
                djnz    fno_loop
                jp      fold_loop

; fold_do_shl — a << b (logical left, port of applyBinary(OpShl)). The shift
; count is uint64(b): a count >= 64 yields 0 (Go's >=width-shift rule), else a
; is shifted left `count` bits.
fold_do_shl:
                call    fold_ab_ptrs        ; HL -> a (value), DE -> b (count)
                jp      c, fold_fail
                call    fold_shift_count    ; A = count (CY set if >= 64); HL kept
                jr      c, fshl_big
                or      a
                jr      z, fshl_done        ; count 0 -> unchanged
                ld      b, a
fshl_loop:
                call    shl1_8
                djnz    fshl_loop
fshl_done:
                call    fold_pop1
                jp      fold_loop
fshl_big:
                call    fold_zero_a         ; count >= 64 -> 0
                call    fold_pop1
                jp      fold_loop

; fold_do_shr — a >> b (arithmetic right, port of applyBinary(OpShr); a is
; int64). A count >= 64 yields the sign extension of a (0 if a >= 0, -1 if
; a < 0), else a is shifted right `count` bits with the sign preserved.
fold_do_shr:
                call    fold_ab_ptrs        ; HL -> a (value), DE -> b (count)
                jp      c, fold_fail
                call    fold_shift_count
                jr      c, fshr_big
                or      a
                jr      z, fshr_done
                ld      b, a
fshr_loop:
                call    sra1_8
                djnz    fshr_loop
fshr_done:
                call    fold_pop1
                jp      fold_loop
fshr_big:
                call    fold_signext_a      ; count >= 64 -> sign extension
                call    fold_pop1
                jp      fold_loop

; fold_do_mul — a * b keeping the low 64 bits (port of applyBinary(OpMul): the
; product wraps mod 2^64, and the low word is the same for signed and unsigned).
; Shift-add: for each bit i of b (LSB first), add a<<i into the accumulator.
; MUL_A holds a shifted left, MUL_B holds b shifted right (logical), MUL_R the
; running sum. 64 iterations.
fold_do_mul:
                call    fold_ab_ptrs        ; HL -> a, DE -> b
                jp      c, fold_fail
                push    hl                  ; save a's stack slot (result dest)
                ex      de, hl              ; HL = b
                ld      de, MUL_B
                ld      bc, 8
                ldir                        ; MUL_B = b
                pop     hl                  ; HL = a's slot
                push    hl
                ld      de, MUL_A
                ld      bc, 8
                ldir                        ; MUL_A = a
                ld      hl, MUL_R
                call    fold_zero_a         ; MUL_R = 0
                ld      b, 64
mul_loop:
                push    bc                  ; save the iteration counter
                ld      a, (MUL_B)          ; low byte of b's running value
                and     1                   ; current LSB
                jr      z, mul_noadd
                ld      hl, MUL_R           ; MUL_R += MUL_A (8-byte add)
                ld      de, MUL_A
                or      a                   ; clear carry
                ld      b, 8
mul_add:
                ld      a, (de)
                ld      c, a
                ld      a, (hl)
                adc     a, c
                ld      (hl), a
                inc     hl
                inc     de
                djnz    mul_add
mul_noadd:
                ld      hl, MUL_A
                call    shl1_8              ; MUL_A <<= 1
                ld      hl, MUL_B
                call    srl1_8              ; MUL_B >>= 1 (logical)
                pop     bc
                djnz    mul_loop
                pop     hl                  ; HL = a's slot (dest)
                ld      de, MUL_R
                ex      de, hl              ; HL = MUL_R (src), DE = dest
                ld      bc, 8
                ldir
                call    fold_pop1
                jp      fold_loop

; shl1_8 — one-bit logical left shift of the 8-byte LE value at HL. HL/BC kept.
shl1_8:
                push    hl
                push    bc
                sla     (hl)                ; byte0: bit7 -> CY, bit0 <- 0
                ld      b, 7
shl1_loop:
                inc     hl
                rl      (hl)                ; bytes 1..7: CY <- in, bit7 -> CY
                djnz    shl1_loop
                pop     bc
                pop     hl
                ret

; sra1_8 — one-bit arithmetic right shift of the 8-byte LE value at HL (sign
; bit preserved). HL/BC kept.
sra1_8:
                push    hl
                push    bc
                ld      bc, 7
                add     hl, bc              ; HL -> byte7 (MSB)
                sra     (hl)                ; byte7: bit0 -> CY, bit7 preserved
                ld      b, 7
sra1_loop:
                dec     hl
                rr      (hl)                ; bytes 6..0: CY <- in, bit0 -> CY
                djnz    sra1_loop
                pop     bc
                pop     hl
                ret

; srl1_8 — one-bit logical right shift of the 8-byte LE value at HL (a 0 fills
; the vacated MSB). HL/BC kept.
srl1_8:
                push    hl
                push    bc
                ld      bc, 7
                add     hl, bc              ; HL -> byte7 (MSB)
                srl     (hl)                ; byte7: bit0 -> CY, bit7 <- 0
                ld      b, 7
srl1_loop:
                dec     hl
                rr      (hl)                ; bytes 6..0: CY <- in, bit0 -> CY
                djnz    srl1_loop
                pop     bc
                pop     hl
                ret

; fold_shift_count — DE -> the 8-byte LE shift count b. Exit: CY clear with
; A = count (0..63) when uint64(b) < 64 (b's bytes 1..7 all zero and byte0 <=
; 63); CY set when uint64(b) >= 64 (covers b >= 64 and any negative b, whose
; uint64 cast is huge). HL/DE preserved. Clobbers A, B.
fold_shift_count:
                push    de
                inc     de                  ; DE -> b byte1
                ld      b, 7
fsc_hiloop:
                ld      a, (de)
                or      a
                jr      nz, fsc_hi_nonzero   ; a high byte set -> >= 64 (or negative)
                inc     de
                djnz    fsc_hiloop
                pop     de
                ld      a, (de)             ; byte0 (bytes 1..7 are zero)
                cp      64
                jr      nc, fsc_toobig      ; byte0 >= 64 -> >= 64
                or      a                   ; clear carry; A = count
                ret
fsc_hi_nonzero:
                pop     de
fsc_toobig:
                scf
                ret

; fold_zero_a — set the 8-byte value at HL to 0. HL/BC kept.
fold_zero_a:
                push    hl
                push    bc
                ld      b, 8
                xor     a
fza_loop:
                ld      (hl), a
                inc     hl
                djnz    fza_loop
                pop     bc
                pop     hl
                ret

; fold_signext_a — set the 8-byte value at HL to the sign extension of its MSB
; (0x00.. or 0xFF.. by byte7's bit7). HL/BC kept.
fold_signext_a:
                push    hl
                push    bc
                push    hl                  ; base for the fill
                ld      bc, 7
                add     hl, bc              ; HL -> byte7 (MSB)
                ld      a, (hl)
                add     a, a                ; bit7 -> CY
                sbc     a, a                ; A = 0x00 / 0xFF
                pop     hl                  ; HL -> base
                ld      b, 8
fsa_loop:
                ld      (hl), a
                inc     hl
                djnz    fsa_loop
                pop     bc
                pop     hl
                ret

; fold_ab_ptrs — for a binary op: HL -> the second-from-top cell (a), DE -> the
; top cell (b). CY set (and registers undefined) if fewer than two cells.
fold_ab_ptrs:
                call    fold_depth          ; HL = used byte count
                ld      de, 16
                or      a
                sbc     hl, de
                jr      c, fab_under        ; used < 16 -> underflow
                ld      hl, (FOLD_SP)
                ld      de, -8
                add     hl, de              ; HL = top (b)
                push    hl
                add     hl, de              ; HL = second (a)
                pop     de                  ; DE = top (b)
                or      a                   ; clear carry: success
                ret
fab_under:
                scf
                ret

; fold_top_ptr — for a unary op: HL -> the top cell. CY set if the stack empty.
fold_top_ptr:
                call    fold_depth          ; HL = used byte count
                ld      de, 8
                or      a
                sbc     hl, de
                jr      c, ftp_under        ; used < 8 -> empty
                ld      hl, (FOLD_SP)
                ld      de, -8
                add     hl, de              ; HL = top
                or      a                   ; clear carry: success
                ret
ftp_under:
                scf
                ret

; fold_depth — HL = FOLD_SP - FOLD_STACK (used byte count). Clobbers DE.
fold_depth:
                ld      hl, (FOLD_SP)
                ld      de, FOLD_STACK
                or      a
                sbc     hl, de
                ret

; fold_pop1 — discard the top value cell (FOLD_SP -= 8). Clobbers HL/DE.
fold_pop1:
                ld      hl, (FOLD_SP)
                ld      de, -8
                add     hl, de
                ld      (FOLD_SP), hl
                ret

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
IMM_VAL:        defs 8          ; scratch int64 (LE) for the shortest-PUSH_IMMn emit
PARSE_OPSBUF:   defs 256        ; one instruction's operand bytes (staging)
EXPR_BUF:       defs 256        ; one operand's expression bytecode (build buffer)
EXPR_PTR:       defs 2          ; expression-bytecode write pointer (into EXPR_BUF)
FOLD_CUR:       defs 2          ; expr_fold: bytecode read cursor
FOLD_SP:        defs 2          ; expr_fold: value-stack pointer (past the top cell)
FOLD_STACK:     defs 128        ; expr_fold: 16 cells of 8-byte LE int64
MUL_A:          defs 8          ; fold_do_mul: multiplicand, shifted left each step
MUL_B:          defs 8          ; fold_do_mul: multiplier, shifted right each step
MUL_R:          defs 8          ; fold_do_mul: running product (low 64 bits)

; ===========================================================================
; Public I/O buffers
; ===========================================================================
PARSE_ERR:      defs 1          ; non-zero after a parse error / out-of-domain line
AP_NAMEBUF:     defs 32         ; scratch the harness fills with a candidate name
PARSE_RECS:     defs 2048       ; emitted INST record stream (harness reads here)
