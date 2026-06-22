; asmparse.asm — aarch64 assembler-source parser, i48c Bricks B2a-B5a + B7
; (text→records).
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
;   B3a3: the multiply operator `*` (precedence 4) — `mov x0, #2*3`. The fold
;        runs a 64-bit shift-add multiply keeping the low 64 bits (port of
;        applyBinary(OpMul): a*b wraps mod 2^64; signed and unsigned agree on
;        the low word).
;   B3a4: the divide operator `/` (precedence 4). The fold runs a signed 64-bit
;        division (port of applyBinary(OpDiv)): a zero divisor yields 0;
;        otherwise the result truncates toward zero, computed as an unsigned
;        long division of the absolute values with a final sign correction. With
;        B3a4 the full constant-expression grammar (operators + unary + parens)
;        is parsed and folded.
;   B3b: the symbol primary — a non-register identifier operand becomes a
;        symbol reference (`mov x0, foo`, `add x0, x1, label+4`). parseExprPrimary's
;        TokIdent case interns the name into a document symbol table (sym_intern,
;        mirroring format.SymbolTable.Intern: name -> first-encounter u16 id) and
;        emits PUSH_SYM,id. A symbol makes the expression non-constant, so the
;        fold keeps the raw bytecode (EvalConst stops at PUSH_SYM) — the operand
;        is the raw expr stream rather than a folded immediate.
;   B3c (this): the three remaining non-constant expression primaries — closes
;        the expression parser:
;        · PC primary: TOK_DOT (`.`) → emit PUSH_PC (0x07).
;        · local-ref primary: TOK_LOCALREF (e.g. `1f`/`2b`) → emit
;          PUSH_LOCAL (0x06), digit, dir (dir=0 for 'f', dir=1 for 'b'). The
;          token layout stores the digit in value[0] (offset 6) and the 'f'/'b'
;          char in base (offset 5).
;        · reloc primary: TOK_COLON (`:lo12:foo`) → consume ':', require a
;          TOK_IDENT name, consume ':', recurse parse_expr_primary for the
;          operand, then emit the reloc ExprOp (0x30–0x38) from a name table.
;          An unknown reloc name or malformed `:name:` sets PARSE_ERR.
;        The parse_operand dispatch is extended to route TOK_DOT, TOK_LOCALREF
;        and TOK_COLON to parse_operand_expr. With B3c, the full expression
;        parser (B3a–B3c) is complete.
;   B4 (B4/B4b/B4c): the remaining operand kinds — memory operands
;        (parse_operand_mem, the 7 `[...]` shapes), register shift/extend
;        suffixes (`x0, lsl #3` / `w0, uxtb`, via parse_operand_reg's lookahead),
;        and condition-code operands (`csel …, eq`, via match_cond). With B4 the
;        generic operand parser is complete.
;   B5a (this): the first special-form instruction intercept — movk/movz/movn.
;        These take `<Rd>, #imm16 [, lsl #N]` (N ∈ {0,16,32,48}); the lsl slot
;        index hw=N/16 selects which 16-bit slot to write and is folded into
;        bits[17:16] of the emitted immediate so the encoder can recover it
;        without a new operand kind (port of parseMovk, parser.go:592-665). A
;        constant immediate is range-checked to [0,65535] and folded to a single
;        literal `(hw<<16)|imm`; a symbolic immediate emits the expression
;        `(hw<<16) | (immExpr & 0xffff)`. parse_inst dispatches to parse_movk on
;        the movk/movz/movn mnemonic ids before the generic operand loop.
;   B7 (this): the line-level non-statement records — COMMENT and BLANK_RUN
;        (port of parseLine's blank-run count + comment handling, parser.go:
;        72-145, plus emitComment/emitBlankRun, parser.go:48-57). parse_run's
;        outer loop now mirrors parseLine: per logical line it first counts the
;        leading run of genuine blank lines (a BLANK_RUN record), then parses the
;        statement line, emitting a COMMENT record for each `//`/`#`/`/* */`
;        comment token with placement 0 (line-leading) or 1 (after a statement
;        on the same line). The lexer already carries each comment's body as the
;        token span.
;   B5 (label/local defs): the statement-leading definition records (port of
;        parseLine's TokIdent+':' / TokInt+':' cases, parser.go:103-119, plus
;        emitLabelDef/emitLocalDef, parser.go:38-45). A leading identifier
;        followed by ':' interns its name (sym_intern) and emits a LABEL_DEF
;        carrying the id; a leading integer in [1,99] followed by ':' emits a
;        LOCAL_DEF carrying the digit. Both are checked before the
;        mnemonic/directive dispatch, matching parseLine. These are in-memory
;        IR records (compacted into the header tables at serialize-time).
;
; RECORD STREAM (emission form — original framing for the host harness; the
; OPERAND bytes are byte-faithful to format.OperandWriter). The stream is
; self-describing: every record begins with a REC_KIND_* tag byte so a reader
; can walk a mix of record kinds. The kinds emitted:
;   INST:      REC_KIND_INST | mnemonic_id:2 LE | operand_count:1 |
;              operands_len:2 LE | operands[]
;   LABEL_DEF: REC_KIND_LABEL_DEF | len:2 LE | sym_id:2 LE            (len = 2)
;   LOCAL_DEF: REC_KIND_LOCAL_DEF | len:2 LE | digit:1               (len = 1)
;   COMMENT:   REC_KIND_COMMENT | len:2 LE | placement:1 | body[]   (len = 1+body)
;   BLANK_RUN: REC_KIND_BLANK_RUN | len:2 LE | run_len:4 LE          (len = 4)
; LABEL_DEF / LOCAL_DEF / COMMENT / BLANK_RUN use the format-package
; [kind:1][len:2][payload] framing
; (tools/sam-aarch64-format/reader.go); INST keeps its richer header after the
; tag. A register operand is `kind:1, reg:1` (kind = OP_KIND_REG_X/W/XSP/WSP);
; an immediate operand is `OP_KIND_IMM_EXPR:1, expr_len:2 LE, expr[]` where expr
; is `PUSH_IMMn:1, value:n LE` (n = 1/2/4/8, the shortest signed fit).
;
; PROVENANCE: algorithmic port of parser.go (Parse / parseLine / parseInst /
; parseOperand / parseExprPrec / parseExprPrimary / tokPrec / emitComment /
; emitBlankRun) + format/operands.go + format/expr.go (ExprWriter / EvalConst);
; the name→id table is generated from the Go authority (tables-gen
; -mnemonic-names-inc → src/mnemonic_names.inc).
; VERIFICATION: tools/netboot-oracle/z80/asmparse_test.go drives mnemonic_lookup
; + parse_run under koron-go/z80 and compares against the authority
; (format.MnemonicID for the table; faithful refParse / refParseFull built on
; the real format.OperandWriter / format.ExprWriter / format.EvalConst for the
; INST / COMMENT / BLANK_RUN records), with mutation-tested teeth.

                if defined(ASMPARSE_STANDALONE)
                org     &8000
                endif

; OP_KIND_* operand-kind equates (generated; zero runtime bytes) + the lexer
; (lex_run, the TOK_* equates, the LEX_SRC/LEX_TOKS buffers).
                include "tbn_constants.inc"
                include "asmlex.asm"

; MNEM_<NAME> equates (generated; zero runtime bytes) — the B5 special-form
; intercepts in parse_inst dispatch on these mnemonic ids by name. Nothing in
; the integrated assembler build includes mnemonic_ids.inc today, so the
; standalone asmparse.bin pulls it in itself; the guard keeps that include out
; of a future B8 integrated build, where the integrator owns where the equates
; come from and a second include here would double-define (mirrors the org
; guard above).
                if defined(ASMPARSE_STANDALONE)
                include "mnemonic_ids.inc"
                endif

; Expression-bytecode opcodes (ExprOp in tools/sam-aarch64-format/expr.go; the
; existing evaluator src/expr_eval.asm dispatches on the same bare values).
EXPR_PUSH_IMM8:  equ &01
EXPR_PUSH_IMM16: equ &02
EXPR_PUSH_IMM32: equ &03
EXPR_PUSH_IMM64: equ &04
EXPR_PUSH_SYM:   equ &05
EXPR_PUSH_LOCAL: equ &06
EXPR_PUSH_PC:    equ &07
EXPR_OP_ADD:     equ &10
EXPR_OP_SUB:     equ &11
EXPR_OP_MUL:     equ &12
EXPR_OP_DIV:     equ &13
EXPR_OP_AND:     equ &14
EXPR_OP_OR:      equ &15
EXPR_OP_XOR:     equ &16
EXPR_OP_SHL:     equ &17
EXPR_OP_SHR:     equ &18
EXPR_OP_NEG:     equ &20
EXPR_OP_NOT:     equ &21
; reloc operators (port of OpRelLo12..OpRelAbsG3 in expr.go)
EXPR_REL_LO12:     equ &30
EXPR_REL_HI12:     equ &31
EXPR_REL_ABS_G0:   equ &32
EXPR_REL_ABS_G0NC: equ &33
EXPR_REL_ABS_G1:   equ &34
EXPR_REL_ABS_G1NC: equ &35
EXPR_REL_ABS_G2:   equ &36
EXPR_REL_ABS_G2NC: equ &37
EXPR_REL_ABS_G3:   equ &38

; ===========================================================================
; parse_run — tokenise LEX_SRC (BC = length) and parse it into records.
;
; Entry: BC = source byte length; source already written to LEX_SRC.
; Exit:  BC = number of records emitted (at PARSE_RECS). PARSE_ERR is non-zero
;        if the lexer erred or a line fell outside the parser's domain.
;
; The outer loop is a faithful port of parser.go's parseLine (parser.go:72-144),
; iterated once per logical source line by Parse (parser.go:19-23): each pass
; counts the leading run of blank lines (a BLANK_RUN record), then parses the
; statement line — instructions plus the COMMENT records the line carries, with
; placement set by whether a statement has been emitted yet on the line (B7).
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
                ld      (SYM_NAMES), a      ; empty document symbol table (sentinel)
                ld      a, (LEX_ERR)        ; a lexical error stops the parse
                or      a
                jp      nz, pr_lexerr
; --- parseLine: count the leading run of blank lines (genuine TOK_EOL only) ---
pr_loop:
                ld      hl, 0
                ld      (PR_BLANKS), hl     ; blanks = 0 (u16 run length)
pr_blank_loop:
                ld      hl, (PARSE_TOK)
                ld      a, (hl)
                cp      TOK_EOL
                jr      nz, pr_blank_done   ; non-blank token ends the blank run
                call    parse_advance_tok   ; consume the blank TOK_EOL
                ld      hl, (PR_BLANKS)
                inc     hl
                ld      (PR_BLANKS), hl     ; blanks++
                jr      pr_blank_loop
pr_blank_done:
                ld      hl, (PR_BLANKS)
                ld      a, h
                or      l
                jr      z, pr_no_blanks     ; blanks == 0: emit nothing
                call    parse_emit_blank_run    ; one BLANK_RUN(blanks) record
pr_no_blanks:
                ; If the blank run ran into EOF the document is done.
                ld      hl, (PARSE_TOK)
                ld      a, (hl)
                cp      TOK_EOF
                jp      z, pr_done
                ; emittedStatement = false for this statement line.
                xor     a
                ld      (PR_EMITTED), a
; --- the per-line inner loop (parseLine's `for { ... }`) ---
pr_line:
                ld      hl, (PARSE_TOK)
                ld      a, (hl)             ; current token kind
                cp      TOK_EOL
                jr      z, pr_line_eol      ; statement terminator (NOT a blank)
                cp      TOK_EOF
                jp      z, pr_done
                cp      TOK_LINECOMMENT
                jr      z, pr_comment
                cp      TOK_BLOCKCOMMENT
                jr      z, pr_comment
                cp      TOK_IDENT
                jp      z, pr_ident         ; mnemonic / directive / label def
                cp      TOK_INT
                jp      z, pr_localdef      ; B5: `1:` local-label def (or error)
                cp      TOK_DOT
                jr      z, pr_dot           ; B6: `. = expr` location-counter set
                jp      pr_domainerr        ; line-leading non-ident: out of domain
pr_line_eol:
                call    parse_advance_tok   ; consume the line-terminating TOK_EOL
                jr      pr_loop             ; next line: re-count blanks
pr_comment:
                call    parse_emit_comment  ; emit COMMENT(placement, body)
                call    parse_advance_tok   ; consume the comment token
                jr      pr_line
pr_inst:
                ; parseInstOrDirective: a leading-'.' identifier is a directive
                ; (the lexer collapses `.set` etc. into one TOK_IDENT whose first
                ; span byte is '.'); anything else is a mnemonic. (parser.go:147.)
                ld      hl, (PARSE_TOK)
                inc     hl
                ld      e, (hl)
                inc     hl
                ld      d, (hl)             ; DE = span ptr
                ld      a, (de)             ; first character of the identifier
                cp      &2E             ; '.'
                jr      z, pr_directive
                call    parse_inst
                jp      c, pr_done          ; error -> stop
                ld      a, 1
                ld      (PR_EMITTED), a     ; emittedStatement = true
                jr      pr_line             ; back to the inner loop (trailing comment / EOL)
pr_directive:
                call    parse_directive     ; B6: `.set`/`.word`/`.org`/`.section`/…
                jp      c, pr_done
                ld      a, 1
                ld      (PR_EMITTED), a
                jr      pr_line
pr_dot:
                ; GNU as `. = expr` ≡ `.org expr`. Peek the token after '.': it
                ; must be '=' for this form; a bare '.' at statement start is an
                ; error. (parser.go:127-140.)
                ld      hl, (PARSE_TOK)
                ld      de, TOK_REC_SIZE
                add     hl, de              ; HL -> token after '.'
                ld      a, (hl)
                cp      TOK_EQUALS
                jp      nz, pr_domainerr
                call    parse_advance_tok   ; consume '.'
                call    parse_advance_tok   ; consume '='
                call    parse_org_rhs
                jp      c, pr_done
                ld      a, 1
                ld      (PR_EMITTED), a
                jr      pr_line
; --- TOK_IDENT at statement start: a label def (`foo:`) takes priority over the
;     mnemonic/directive dispatch (parser.go:115-119 checks the colon first). ---
pr_ident:
                ld      hl, (PARSE_TOK)
                ld      de, TOK_REC_SIZE
                add     hl, de              ; HL -> token after the identifier
                ld      a, (hl)
                cp      TOK_COLON
                jr      z, pr_labeldef
                jp      pr_inst             ; not a label -> mnemonic / directive
pr_labeldef:
                ; Intern the label name (its identifier span) to a u16 id and
                ; emit a LABEL_DEF record carrying that id (emitLabelDef).
                ld      hl, (PARSE_TOK)
                inc     hl
                ld      e, (hl)
                inc     hl
                ld      d, (hl)             ; DE = span_ptr
                inc     hl
                ld      c, (hl)             ; C = span_len low (label names < 256)
                ex      de, hl              ; HL = span_ptr
                call    sym_intern          ; HL = id
                call    parse_emit_label_def
                call    parse_advance_tok   ; consume the identifier
                call    parse_advance_tok   ; consume the ':'
                ld      a, 1
                ld      (PR_EMITTED), a
                jp      pr_line
; --- TOK_INT at statement start: a local-label def (`1:`) when followed by ':'
;     with the value in [1,99]; any other leading number is an error
;     (parser.go:103-114). ---
pr_localdef:
                ld      hl, (PARSE_TOK)
                ld      de, TOK_REC_SIZE
                add     hl, de              ; HL -> token after the number
                ld      a, (hl)
                cp      TOK_COLON
                jr      nz, pr_domainerr    ; bare number at statement start -> error
                ; The token value (offset 6, 8 bytes LE) must be in [1,99]: the low
                ; byte holds the digit and bytes 1..7 must be zero.
                ld      hl, (PARSE_TOK)
                ld      de, 6
                add     hl, de              ; HL -> value low byte
                ld      a, (hl)
                ld      b, a                ; B = candidate digit
                inc     hl
                ld      c, 7                ; check the upper 7 value bytes are zero
pld_zchk:
                ld      a, (hl)
                or      a
                jr      nz, pr_domainerr    ; value > 0xFF -> out of [1,99]
                inc     hl
                dec     c
                jr      nz, pld_zchk
                ld      a, b                ; A = digit
                or      a
                jr      z, pr_domainerr     ; 0 -> not a valid local label
                cp      100
                jr      nc, pr_domainerr    ; >= 100 -> out of [1,99]
                call    parse_emit_local_def    ; A = digit
                call    parse_advance_tok   ; consume the number
                call    parse_advance_tok   ; consume the ':'
                ld      a, 1
                ld      (PR_EMITTED), a
                jp      pr_line
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
                jp      z, pi_err           ; unknown mnemonic -> out of domain
                ld      (PI_MNEMID), hl     ; save id
                call    parse_advance_tok   ; consume the mnemonic
                ld      hl, PARSE_OPSBUF
                ld      (PI_OPSPTR), hl     ; operand-bytes write ptr
                xor     a
                ld      (PI_COUNT), a       ; operand count
                ; --- B5 special-form intercepts (dispatch on mnemonic id) ---
                ld      a, (PI_MNEMID+1)    ; id high byte
                or      a
                jr      nz, pi_loop         ; id >= 256: no B5 special form
                ld      a, (PI_MNEMID)      ; id low byte
                cp      MNEM_MOVK
                jp      z, parse_movk       ; B5a: movk/movz/movn
                cp      MNEM_MOVZ
                jp      z, parse_movk
                cp      MNEM_MOVN
                jp      z, parse_movk
                cp      MNEM_MOVL
                jp      z, parse_movl       ; B5b: movl pseudo (movz+movk expansion)
                cp      MNEM_DSB
                jp      z, parse_barrier_req ; B5c: dsb (mandatory barrier arg)
                cp      MNEM_DMB
                jp      z, parse_barrier_req ; B5c: dmb (mandatory barrier arg)
                cp      MNEM_ISB
                jp      z, parse_barrier_opt ; B5c: isb (optional arg, default sy)
                cp      MNEM_MRS
                jp      z, parse_mrs        ; B5d: mrs Xt, <sysreg>
                cp      MNEM_MSR
                jp      z, parse_msr        ; B5d: msr <sysreg|pstate>, Xt|#imm
                cp      MNEM_DC
                jp      z, parse_dc         ; B5e: dc <op>, Xt
                cp      MNEM_TLBI
                jp      z, parse_tlbi       ; B5e: tlbi <op>[, Xt]
                cp      MNEM_LDR
                jp      z, parse_ldr        ; B5f: ldr Rn, =expr literal-pool pseudo
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
; parse_movk — B5a special-form parse for movk/movz/movn. (Port of parseMovk,
; parser.go:592-665.) Syntax `<mn> Rd, #imm16 [, lsl #N]`, N in {0,16,32,48};
; the slot index hw=N/16 is folded into bits[17:16] of the emitted immediate so
; the encoder recovers it without a new operand kind. A constant immediate is
; range-checked to [0,65535] and folded to a single literal `(hw<<16)|imm`; a
; symbolic immediate emits `(hw<<16) | (immExpr & 0xffff)`. Entry (from the
; parse_inst dispatch): PI_MNEMID/PI_OPSPTR/PI_COUNT set up, mnemonic consumed.
; Exit: one INST record emitted via pi_emit (CY clear); on error jumps pi_err
; (sets PARSE_ERR, CY).
; ===========================================================================
parse_movk:
                ; Operand 1: destination register.
                call    parse_operand
                jp      c, pi_err
                ; Expect ',' after the register.
                ld      hl, (PARSE_TOK)
                ld      a, (hl)
                cp      TOK_COMMA
                jp      nz, pi_err
                call    parse_advance_tok
                ; Operand 2: immediate expression -> EXPR_BUF, then fold to the
                ; parseExpression() form (single PUSH_IMMn if constant, else raw).
                ld      hl, EXPR_BUF
                ld      (EXPR_PTR), hl
                xor     a                   ; minPrec = 0
                call    parse_expr_prec
                jp      c, pi_err
                call    expr_fold
                ; Save the folded immExpr bytecode: the optional shift parse and
                ; the rebuild below both reuse EXPR_BUF.
                ld      hl, (EXPR_PTR)
                ld      de, EXPR_BUF
                or      a
                sbc     hl, de
                ld      (MOVK_IMMLEN), hl
                ld      b, h
                ld      c, l
                ld      hl, EXPR_BUF
                ld      de, MOVK_IMMEXPR
                ld      a, b
                or      c
                jr      z, pmk_saved        ; (defensive) zero-length expr
                ldir
pmk_saved:
                ; Constant or symbolic? expr_buf_single_imm loads IMM_VAL and
                ; clears CY when EXPR_BUF holds a single folded literal.
                call    expr_buf_single_imm
                jr      c, pmk_symbolic
                ; Constant: range-check immConst to [0,65535] — valid iff bytes
                ; 2..7 of the sign-extended int64 are all zero (non-negative and
                ; < 2^16).
                ld      hl, IMM_VAL+2
                ld      b, 6
pmk_rng:
                ld      a, (hl)
                or      a
                jp      nz, pi_err
                inc     hl
                djnz    pmk_rng
                ; Save the constant value for the rebuild.
                ld      hl, IMM_VAL
                ld      de, MOVK_IMM
                ld      bc, 8
                ldir
                ld      a, 1
                ld      (MOVK_ISCONST), a
                jr      pmk_shift
pmk_symbolic:
                xor     a
                ld      (MOVK_ISCONST), a
pmk_shift:
                ; Optional `, lsl #N` suffix -> hw in {0,1,2,3} (default 0).
                xor     a
                ld      (MOVK_HW), a
                ld      hl, (PARSE_TOK)
                ld      a, (hl)
                cp      TOK_COMMA
                jr      nz, pmk_build       ; no suffix
                call    parse_advance_tok
                ; Expect the `lsl` keyword.
                ld      hl, (PARSE_TOK)
                ld      a, (hl)
                cp      TOK_IDENT
                jp      nz, pi_err
                call    movk_tok_is_lsl
                jp      c, pi_err
                call    parse_advance_tok   ; consume `lsl`
                ; Expect `#`.
                ld      hl, (PARSE_TOK)
                ld      a, (hl)
                cp      TOK_HASH
                jp      nz, pi_err
                call    parse_advance_tok
                ; Parse + fold the shift amount; it must be a constant.
                ld      hl, EXPR_BUF
                ld      (EXPR_PTR), hl
                xor     a
                call    parse_expr_prec
                jp      c, pi_err
                call    expr_fold
                call    expr_buf_single_imm
                jp      c, pi_err           ; shift amount must be a constant
                ; Validate {0,16,32,48}: bytes 1..7 zero, byte0 <= 48, byte0%16=0.
                ld      hl, IMM_VAL+1
                ld      b, 7
pmk_shrng:
                ld      a, (hl)
                or      a
                jp      nz, pi_err
                inc     hl
                djnz    pmk_shrng
                ld      a, (IMM_VAL)
                cp      49
                jp      nc, pi_err          ; > 48
                ld      c, a
                and     &0f
                jp      nz, pi_err          ; not a multiple of 16
                ld      a, c
                rrca
                rrca
                rrca
                rrca                        ; A = byte0 / 16 (byte0 in {0,16,32,48})
                ld      (MOVK_HW), a
pmk_build:
                ; Rebuild the emitted immediate expression in EXPR_BUF.
                ld      hl, EXPR_BUF
                ld      (EXPR_PTR), hl
                ld      a, (MOVK_ISCONST)
                or      a
                jr      z, pmk_build_sym
                ; Constant path: value = (hw<<16) | immConst.
                call    movk_set_immval_zero
                ld      a, (MOVK_IMM)
                ld      (IMM_VAL), a        ; byte0 = imm low
                ld      a, (MOVK_IMM+1)
                ld      (IMM_VAL+1), a      ; byte1 = imm high
                ld      a, (MOVK_HW)
                ld      (IMM_VAL+2), a      ; byte2 = hw
                call    expr_emit_imm_from_immval
                jr      pmk_emit
pmk_build_sym:
                ; Symbolic path: PUSH (hw<<16), AppendRaw immExpr, PUSH 0xffff,
                ; AND, OR  ->  (hw<<16) | (immExpr & 0xffff).
                call    movk_set_immval_zero
                ld      a, (MOVK_HW)
                ld      (IMM_VAL+2), a      ; byte2 = hw  (value = hw<<16)
                call    expr_emit_imm_from_immval
                ld      hl, MOVK_IMMEXPR
                ld      bc, (MOVK_IMMLEN)
                call    expr_append_raw
                call    movk_set_immval_zero
                ld      a, &ff
                ld      (IMM_VAL), a
                ld      (IMM_VAL+1), a      ; value = 0x0000ffff
                call    expr_emit_imm_from_immval
                ld      a, EXPR_OP_AND
                call    expr_emit_byte
                ld      a, EXPR_OP_OR
                call    expr_emit_byte
pmk_emit:
                call    emit_imm_expr_operand
                ld      a, 2
                ld      (PI_COUNT), a
                jp      pi_emit

; ===========================================================================
; movk_set_immval_zero — zero the 8-byte IMM_VAL scratch. Clobbers A/B/HL.
; ===========================================================================
movk_set_immval_zero:
                ld      hl, IMM_VAL
                ld      b, 8
                xor     a
msiz_loop:
                ld      (hl), a
                inc     hl
                djnz    msiz_loop
                ret

; ===========================================================================
; movk_tok_is_lsl — CY clear iff the current token (PARSE_TOK, a TOK_IDENT)
; spans exactly the three bytes "lsl". (Go: parseMovk's `cur().Text == "lsl"`,
; parser.go:623.) Token layout: kind:1, span_ptr:2, span_len:2, base:1, val:8.
; Clobbers A/DE/HL.
; ===========================================================================
movk_tok_is_lsl:
                ld      hl, (PARSE_TOK)
                inc     hl
                ld      e, (hl)
                inc     hl
                ld      d, (hl)             ; DE = span ptr
                inc     hl
                ld      a, (hl)             ; span_len low
                cp      3
                jr      nz, mtil_no
                inc     hl
                ld      a, (hl)             ; span_len high
                or      a
                jr      nz, mtil_no
                ld      a, (de)
                cp      &6c                 ; 'l'
                jr      nz, mtil_no
                inc     de
                ld      a, (de)
                cp      &73                 ; 's'
                jr      nz, mtil_no
                inc     de
                ld      a, (de)
                cp      &6c                 ; 'l'
                jr      nz, mtil_no
                or      a                   ; clear carry: match
                ret
mtil_no:
                scf
                ret

; ===========================================================================
; parse_movl — B5b special-form parse for the spectrum4 `movl Rd, #imm32`
; pseudo-instruction. (Port of parseMovl, parser.go:741-836.) It expands to one
; or two real instructions:
;   constant case (imm folds to a literal): lo16 = imm&0xffff, hi16 = (imm>>16)&0xffff
;     - lo16==0 && hi16!=0 : a single MOVZ Rd,#((1<<16)|hi16)
;     - otherwise          : MOVZ Rd,#lo16  [+ MOVK Rd,#((1<<16)|hi16) if hi16!=0]
;   symbolic case:
;       mov  Rd, :abs_g0_nc:expr   (MOVZ low16, no-carry)
;       movk Rd, (1<<16)|:abs_g1:expr   (bits 31:16, lsl #16)
; The first emitted record carries mnemonic id MNEM_MOV (the encoder treats a
; 2-operand `mov Rd,#imm` as MOVZ); the second carries MNEM_MOVK. hw is folded
; into bits[17:16] of the emitted immediate (same convention as parse_movk). The
; saved immExpr / its length reuse the parse_movk scratch (MOVK_IMMEXPR/_IMMLEN) —
; movk and movl never parse concurrently. Entry (from parse_inst dispatch):
; PI_MNEMID=movl, PI_OPSPTR=PARSE_OPSBUF, PI_COUNT=0, mnemonic consumed. Exit: 1 or
; 2 INST records emitted (CY clear); on error jumps pi_err.
; ===========================================================================
parse_movl:
                ; Operand 1: destination register -> PARSE_OPSBUF[0..1] = [kind,reg].
                call    parse_operand
                jp      c, pi_err
                ld      hl, PARSE_OPSBUF
                ld      a, (hl)
                ld      (MOVL_RDKIND), a
                inc     hl
                ld      a, (hl)
                ld      (MOVL_RDREG), a
                ; Expect ',' after the register.
                ld      hl, (PARSE_TOK)
                ld      a, (hl)
                cp      TOK_COMMA
                jp      nz, pi_err
                call    parse_advance_tok
                ; Operand 2: value expression -> EXPR_BUF, folded.
                ld      hl, EXPR_BUF
                ld      (EXPR_PTR), hl
                xor     a                   ; minPrec = 0
                call    parse_expr_prec
                jp      c, pi_err
                call    expr_fold
                ; Save the folded immExpr bytecode (the symbolic rebuild reuses it).
                ld      hl, (EXPR_PTR)
                ld      de, EXPR_BUF
                or      a
                sbc     hl, de
                ld      (MOVK_IMMLEN), hl
                ld      b, h
                ld      c, l
                ld      hl, EXPR_BUF
                ld      de, MOVK_IMMEXPR
                ld      a, b
                or      c
                jr      z, pml_saved
                ldir
pml_saved:
                ; Constant or symbolic?
                call    expr_buf_single_imm
                jp      c, pml_symbolic
                ; --- Constant case: split the folded literal into lo16/hi16. -----
                ; Faithful to the Go: no 32-bit range check; bits above 32 dropped.
                ld      a, (IMM_VAL)
                ld      (MOVL_LO16), a
                ld      a, (IMM_VAL+1)
                ld      (MOVL_LO16+1), a
                ld      a, (IMM_VAL+2)
                ld      (MOVL_HI16), a
                ld      a, (IMM_VAL+3)
                ld      (MOVL_HI16+1), a
                ; hi16 == 0 ?
                ld      a, (MOVL_HI16)
                ld      hl, MOVL_HI16+1
                or      (hl)
                jr      z, pml_const_lo_only   ; hi16==0 -> single MOVZ #lo16
                ; hi16 != 0. lo16 == 0 ?
                ld      a, (MOVL_LO16)
                ld      hl, MOVL_LO16+1
                or      (hl)
                jr      nz, pml_const_both     ; lo16!=0 && hi16!=0
                ; lo16==0 && hi16!=0 -> single MOVZ Rd,#((1<<16)|hi16).
                ld      a, 1                   ; hw = 1
                ld      hl, MOVL_HI16
                ld      e, MNEM_MOV
                call    pml_emit_const
                jp      pml_done
pml_const_lo_only:
                ; MOVZ Rd, #lo16 (hw=0), only.
                xor     a                      ; hw = 0
                ld      hl, MOVL_LO16
                ld      e, MNEM_MOV
                call    pml_emit_const
                jp      pml_done
pml_const_both:
                ; MOVZ Rd, #lo16 (hw=0).
                xor     a
                ld      hl, MOVL_LO16
                ld      e, MNEM_MOV
                call    pml_emit_const
                ; MOVK Rd, #((1<<16)|hi16) (hw=1).
                ld      a, 1
                ld      hl, MOVL_HI16
                ld      e, MNEM_MOVK
                call    pml_emit_const
                jp      pml_done
pml_symbolic:
                ; inst1: MOV(Z) Rd, [immExpr ; OpRelAbsG0NC].
                call    pml_begin_ops
                ld      hl, EXPR_BUF
                ld      (EXPR_PTR), hl
                ld      hl, MOVK_IMMEXPR
                ld      bc, (MOVK_IMMLEN)
                call    expr_append_raw
                ld      a, EXPR_REL_ABS_G0NC
                call    expr_emit_byte
                call    emit_imm_expr_operand
                ld      hl, MNEM_MOV
                call    pml_emit_inst
                ; inst2: MOVK Rd, (1<<16) | (:abs_g1:immExpr).
                call    pml_begin_ops
                ld      hl, EXPR_BUF
                ld      (EXPR_PTR), hl
                call    movk_set_immval_zero
                ld      a, 1
                ld      (IMM_VAL+2), a         ; value = 1<<16 (hw=1 marker)
                call    expr_emit_imm_from_immval
                ld      hl, MOVK_IMMEXPR
                ld      bc, (MOVK_IMMLEN)
                call    expr_append_raw
                ld      a, EXPR_REL_ABS_G1
                call    expr_emit_byte
                ld      a, EXPR_OP_OR
                call    expr_emit_byte
                call    emit_imm_expr_operand
                ld      hl, MNEM_MOVK
                call    pml_emit_inst
pml_done:
                or      a                       ; CY clear: success
                ret

; pml_emit_const — emit one constant MOV(Z)/MOVK inst: Rd, value=(hw<<16)|imm16.
; In: A = hw (0/1); HL -> 2-byte LE imm16; E = mnemonic id (MNEM_MOV / MNEM_MOVK).
pml_emit_const:
                ld      (MOVL_HW), a
                ld      (MOVL_TMPPTR), hl
                ld      a, e
                ld      (MOVL_TMPMNEM), a
                call    pml_begin_ops          ; reset PI_OPSPTR + write Rd operand
                ld      hl, EXPR_BUF
                ld      (EXPR_PTR), hl
                call    movk_set_immval_zero
                ld      hl, (MOVL_TMPPTR)
                ld      a, (hl)
                ld      (IMM_VAL), a            ; byte0 = imm16 low
                inc     hl
                ld      a, (hl)
                ld      (IMM_VAL+1), a          ; byte1 = imm16 high
                ld      a, (MOVL_HW)
                ld      (IMM_VAL+2), a          ; byte2 = hw  (value = (hw<<16)|imm16)
                call    expr_emit_imm_from_immval
                call    emit_imm_expr_operand
                ld      a, (MOVL_TMPMNEM)
                ld      l, a
                ld      h, 0
                jp      pml_emit_inst           ; tail (returns to caller)

; pml_begin_ops — reset PI_OPSPTR to PARSE_OPSBUF and write the saved Rd register
; operand [kind,reg], leaving PI_OPSPTR after it. Clobbers A/HL.
pml_begin_ops:
                ld      hl, PARSE_OPSBUF
                ld      a, (MOVL_RDKIND)
                ld      (hl), a
                inc     hl
                ld      a, (MOVL_RDREG)
                ld      (hl), a
                inc     hl
                ld      (PI_OPSPTR), hl
                ret

; pml_emit_inst — set PI_MNEMID=HL, PI_COUNT=2, emit one INST record. Tail-jumps
; parse_emit_inst (returns to caller). Clobbers regs.
pml_emit_inst:
                ld      (PI_MNEMID), hl
                ld      a, 2
                ld      (PI_COUNT), a
                jp      parse_emit_inst

; ===========================================================================
; parse_barrier — B5c special-form parse for dsb/dmb/isb. (Port of parseBarrier,
; parser.go:704-732.) The barrier-arg keyword (sy/st/ld/ish/ishst/ishld/nsh/
; nshst/nshld/osh/oshst/oshld) maps to its CRm field value; one INST record is
; emitted with a single imm-expr operand carrying CRm. For dsb/dmb the arg is
; mandatory; for isb it is optional and defaults to `sy` (CRm=0xf). Entry (from
; parse_inst dispatch): PI_MNEMID=dsb/dmb/isb, PI_OPSPTR=PARSE_OPSBUF, PI_COUNT=0,
; mnemonic consumed. Exit: one INST record (CY clear); on error jumps pi_err.
; ===========================================================================
parse_barrier_req:                          ; dsb/dmb: arg mandatory
                xor     a
                jr      parse_barrier
parse_barrier_opt:                          ; isb: arg optional (default sy)
                ld      a, 1
parse_barrier:
                ld      (BARRIER_OPT), a
                ; Is the current token a barrier-arg keyword (an identifier)?
                ld      hl, (PARSE_TOK)
                ld      a, (hl)
                cp      TOK_IDENT
                jr      z, pb_has_arg
                ; No arg present. Mandatory -> error; optional -> default sy (0xf).
                ld      a, (BARRIER_OPT)
                or      a
                jp      z, pi_err
                ld      a, &0f
                jr      pb_emit
pb_has_arg:
                call    barrier_lookup      ; A = CRm, CY set if unknown keyword
                jp      c, pi_err
                push    af
                call    parse_advance_tok   ; consume the keyword
                pop     af
pb_emit:
                ; Emit one INST: 1 operand = imm-expr WriteImm(CRm). A = CRm.
                ld      (BARRIER_CRM), a
                ld      hl, EXPR_BUF
                ld      (EXPR_PTR), hl
                call    movk_set_immval_zero
                ld      a, (BARRIER_CRM)
                ld      (IMM_VAL), a
                call    expr_emit_imm_from_immval
                call    emit_imm_expr_operand
                ld      a, 1
                ld      (PI_COUNT), a
                jp      pi_emit

; ===========================================================================
; barrier_lookup — match the current token's (PARSE_TOK) span against the barrier
; keyword table. Out: A = CRm and CY clear on match; CY set if the span is not a
; known barrier keyword. Clobbers AF/BC/DE/HL.
; ===========================================================================
barrier_lookup:
                ld      hl, (PARSE_TOK)
                inc     hl
                ld      e, (hl)
                inc     hl
                ld      d, (hl)             ; DE = span ptr
                inc     hl
                ld      a, (hl)             ; span_len low
                ld      (BARRIER_SPANLEN), a
                inc     hl
                ld      a, (hl)             ; span_len high
                or      a
                scf
                ret     nz                  ; len >= 256: not a barrier keyword
                ld      (BARRIER_SPANPTR), de
                ld      hl, barrier_tbl
bl_loop:
                ld      a, (hl)             ; entry length (0 = sentinel)
                or      a
                jr      z, bl_nf
                ld      c, a                ; C = entry length
                ld      a, (BARRIER_SPANLEN)
                cp      c
                jr      nz, bl_next         ; length mismatch
                push    hl                  ; save entry ptr (at length byte)
                inc     hl                  ; HL -> entry name bytes
                ld      de, (BARRIER_SPANPTR)
                ld      b, c
bl_cmp:
                ld      a, (de)
                cp      (hl)
                jr      nz, bl_cmpfail
                inc     hl
                inc     de
                djnz    bl_cmp
                ld      a, (hl)             ; all matched: HL -> the CRm byte
                pop     de                  ; discard saved entry ptr
                or      a                   ; CY clear: found
                ret
bl_cmpfail:
                pop     hl                  ; restore entry ptr (length byte)
bl_next:
                ld      a, (hl)             ; entry length
                ld      c, a
                ld      b, 0
                inc     hl                  ; skip length byte
                add     hl, bc              ; skip name bytes
                inc     hl                  ; skip CRm byte
                jr      bl_loop
bl_nf:
                scf
                ret

; barrier keyword -> CRm (bits 11:8) table: [len:1, name[len], crm:1], 0-terminated.
; (Port of barrierCRm, parser.go:670-698 / ARM ARM C6.2.73-74,99.)
barrier_tbl:
                defb    2
                defm    "sy"
                defb    &0f
                defb    2
                defm    "st"
                defb    &0e
                defb    2
                defm    "ld"
                defb    &0d
                defb    3
                defm    "ish"
                defb    &0b
                defb    5
                defm    "ishst"
                defb    &0a
                defb    5
                defm    "ishld"
                defb    &09
                defb    3
                defm    "nsh"
                defb    &07
                defb    5
                defm    "nshst"
                defb    &06
                defb    5
                defm    "nshld"
                defb    &05
                defb    3
                defm    "osh"
                defb    &03
                defb    5
                defm    "oshst"
                defb    &02
                defb    5
                defm    "oshld"
                defb    &01
                defb    0

; ===========================================================================
; parse_mrs / parse_msr — B5d special-form parse for mrs/msr. (Port of parseMrs/
; parseMsr, parser.go:842-926.) The system-register / PSTATE-field name is a
; bareword identifier emitted as an OP_KIND_SYS_NAME operand carrying the name
; verbatim ([&0B, len:2 LE, name[]]); the assembler's encoder (sysreg_data.asm —
; the single home of the sysreg/PSTATE tables) resolves and validates the name in
; a later pass, so the parser does not duplicate those tables. Shapes:
;   mrs Xt, <sysreg>            -> [reg Xt][sysname]
;   msr <sysreg>, Xt            -> [sysname][reg Xt]
;   msr <pstatefield>, #imm     -> [sysname][imm-expr]
; The msr second operand is parsed by the generic parse_operand, which emits a
; register or an imm-expr exactly as the Go dispatches on the operand kind. Entry
; (from parse_inst dispatch): PI_MNEMID=mrs/msr, PI_OPSPTR=PARSE_OPSBUF,
; PI_COUNT=0, mnemonic consumed. Exit: one INST record (CY clear); else pi_err.
; ===========================================================================
parse_mrs:
                ; Operand 1: destination register (Xt).
                call    parse_operand
                jp      c, pi_err
                ld      hl, (PARSE_TOK)
                ld      a, (hl)
                cp      TOK_COMMA
                jp      nz, pi_err
                call    parse_advance_tok
                ; Operand 2: system-register name (bareword identifier).
                ld      hl, (PARSE_TOK)
                ld      a, (hl)
                cp      TOK_IDENT
                jp      nz, pi_err
                call    emit_sysname_operand
                call    parse_advance_tok
                ld      a, 2
                ld      (PI_COUNT), a
                jp      pi_emit

parse_msr:
                ; Operand 1: sysreg / PSTATE-field name (bareword identifier).
                ld      hl, (PARSE_TOK)
                ld      a, (hl)
                cp      TOK_IDENT
                jp      nz, pi_err
                call    emit_sysname_operand
                call    parse_advance_tok
                ld      hl, (PARSE_TOK)
                ld      a, (hl)
                cp      TOK_COMMA
                jp      nz, pi_err
                call    parse_advance_tok
                ; Operand 2: register (Xt) or immediate expression — parse_operand
                ; emits the right kind, mirroring the Go's operand-kind dispatch.
                call    parse_operand
                jp      c, pi_err
                ld      a, 2
                ld      (PI_COUNT), a
                jp      pi_emit

; ===========================================================================
; emit_sysname_operand — append an OP_KIND_SYS_NAME operand at (PI_OPSPTR) from the
; current token's (PARSE_TOK) identifier span: [&0B, name_len:2 LE, name[]].
; (Port of OperandWriter.WriteSysName, operands.go:219.) Advances (PI_OPSPTR); does
; NOT consume the token (the caller does). Clobbers AF/BC/DE/HL.
; ===========================================================================
emit_sysname_operand:
                ld      hl, (PARSE_TOK)
                inc     hl
                ld      e, (hl)
                inc     hl
                ld      d, (hl)             ; DE = span ptr
                inc     hl
                ld      c, (hl)             ; name_len low
                inc     hl
                ld      b, (hl)             ; name_len high
                ld      hl, (PI_OPSPTR)
                ld      (hl), OP_KIND_SYS_NAME
                inc     hl
                ld      (hl), c             ; name_len low
                inc     hl
                ld      (hl), b             ; name_len high
                inc     hl
                ex      de, hl              ; HL = name src, DE = operand dest
                ld      a, b
                or      c
                jr      z, eso_done         ; (defensive) zero-length name
                ldir                        ; copy name bytes; DE -> end
eso_done:
                ld      (PI_OPSPTR), de
                ret

; ===========================================================================
; parse_dc / parse_tlbi — B5e special-form parse for dc/tlbi. (Port of
; parseDcTlbi, parser.go:932-964.) The operation name is a bareword identifier
; emitted as an OP_KIND_SYS_NAME operand (emit_sysname_operand); an optional
; `, Xt` register operand follows. For dc the register is mandatory; for tlbi it
; is optional. Shapes:
;   dc <op>, Xt        -> [sysname][reg Xt]
;   tlbi <op>          -> [sysname]
;   tlbi <op>, Xt      -> [sysname][reg Xt]
; The op-name table lookup + Xt-requirement check live in the encoder
; (sysreg_data.asm — single home of the DC/TLBI op tables), so the parser emits
; the name verbatim, the same deferral as mrs/msr (B5d). Entry (from parse_inst
; dispatch): PI_MNEMID=dc/tlbi, PI_OPSPTR=PARSE_OPSBUF, PI_COUNT=0, mnemonic
; consumed. Exit: one INST record (CY clear); else pi_err.
; ===========================================================================
parse_dc:                                   ; Xt mandatory
                xor     a
                jr      parse_dc_tlbi
parse_tlbi:                                 ; Xt optional
                ld      a, 1
parse_dc_tlbi:
                ld      (DCTLBI_XTOPT), a
                ; Operand 1: operation name (bareword identifier).
                ld      hl, (PARSE_TOK)
                ld      a, (hl)
                cp      TOK_IDENT
                jp      nz, pi_err
                call    emit_sysname_operand
                call    parse_advance_tok
                ; Optional `, Xt`.
                ld      hl, (PARSE_TOK)
                ld      a, (hl)
                cp      TOK_COMMA
                jr      z, pdt_has_xt
                ; No register: dc requires one (error); tlbi emits a 1-operand inst.
                ld      a, (DCTLBI_XTOPT)
                or      a
                jp      z, pi_err
                ld      a, 1
                ld      (PI_COUNT), a
                jp      pi_emit
pdt_has_xt:
                call    parse_advance_tok   ; consume the comma
                ld      hl, (PARSE_TOK)
                ld      a, (hl)
                cp      TOK_IDENT
                jp      nz, pi_err          ; expected a register after ','
                call    parse_operand       ; emit the Xt register operand
                jp      c, pi_err
                ld      a, 2
                ld      (PI_COUNT), a
                jp      pi_emit

; ===========================================================================
; parse_ldr — B5f special-form parse for the `ldr <Xn|Wn>, =<expr>` literal-pool
; pseudo-instruction. (Port of tryParseLdrLitPool, parser.go:975-1021.) The shape
; is recognised by peeking three tokens WITHOUT consuming — an X/W register, a
; comma, then `=`. On a match it emits [reg][OP_KIND_LIT_POOL, width, expr] where
; width is 8 (X) or 4 (W) and the expr is the constant-folded pool value. Any
; other shape (memory addressing, PC-relative literal, etc.) is NOT this pseudo:
; we fall through to pi_loop and let the generic operand parser handle the `ldr`
; like any other instruction — matching the Go's (false, nil) return. Entry (from
; parse_inst dispatch): PI_MNEMID=ldr, PI_OPSPTR=PARSE_OPSBUF, PI_COUNT=0,
; mnemonic consumed. Exit: one INST record (CY clear via pi_emit); else pi_err.
; ===========================================================================
parse_ldr:
                ; Peek op0: must be a register-name identifier.
                ld      hl, (PARSE_TOK)
                ld      a, (hl)
                cp      TOK_IDENT
                jp      nz, pi_loop         ; not an ident -> generic ldr
                inc     hl
                ld      e, (hl)
                inc     hl
                ld      d, (hl)             ; DE = span_ptr
                inc     hl
                ld      a, (hl)             ; A = span_len low (reg names < 256)
                ex      de, hl              ; HL = span_ptr
                call    match_reg           ; CY = is-reg, B = kind, C = reg
                jp      nc, pi_loop         ; not a register -> generic ldr
                ; Require X or W kind (not XSP/WSP).
                ld      a, b
                cp      OP_KIND_REG_X
                jr      z, pl_kind_ok
                cp      OP_KIND_REG_W
                jp      nz, pi_loop         ; SP forms -> generic ldr
pl_kind_ok:
                ld      a, b
                ld      (POR_KIND), a       ; save destination register kind
                ld      a, c
                ld      (POR_REG), a        ; save destination register number
                ; Peek op1 head: token after the register must be a comma.
                ld      hl, (PARSE_TOK)
                ld      de, TOK_REC_SIZE
                add     hl, de              ; HL -> tok1
                ld      a, (hl)
                cp      TOK_COMMA
                jp      nz, pi_loop         ; no comma -> generic ldr
                ; Peek the token after the comma: must be `=`.
                add     hl, de              ; HL -> tok2
                ld      a, (hl)
                cp      TOK_EQUALS
                jp      nz, pi_loop         ; not `=expr` -> generic ldr
                ; Commit: emit the destination register operand, then consume
                ; the reg, comma and `=` tokens (3 tokens).
                ld      hl, (PI_OPSPTR)
                ld      a, (POR_KIND)
                ld      (hl), a             ; operand kind
                inc     hl
                ld      a, (POR_REG)
                ld      (hl), a             ; register number
                inc     hl
                ld      (PI_OPSPTR), hl
                call    parse_advance_tok   ; consume register
                call    parse_advance_tok   ; consume comma
                call    parse_advance_tok   ; consume `=`
                ; Parse the pool-value expression (minPrec 0), constant-fold it.
                ld      hl, EXPR_BUF
                ld      (EXPR_PTR), hl
                xor     a                   ; minPrec = 0
                call    parse_expr_prec
                jp      c, pi_err
                call    expr_fold           ; collapse a pure-constant stream
                ; width = 8 (X destination) or 4 (W destination).
                ld      a, (POR_KIND)
                cp      OP_KIND_REG_W
                ld      a, 8
                jr      nz, pl_have_width
                ld      a, 4
pl_have_width:
                ld      (LITPOOL_WIDTH), a
                call    emit_litpool_operand
                ld      a, 2
                ld      (PI_COUNT), a
                jp      pi_emit

; ===========================================================================
; parse_directive — B6 parse for an assembler directive line. (Port of
; parseDirective, parser.go:154-191.) Entry: PARSE_TOK -> the directive's
; TOK_IDENT (a leading-'.' identifier such as `.set`/`.word`/`.org`), not yet
; consumed. Looks up the directive id (directive_lookup over DIRECTIVE_NAMES),
; then dispatches:
;   .section          -> parse_directive_section (NAME[, "flags"[, %type]])
;   .arch / .cpu       -> parse_directive_rest    (rest-of-line as one sysname)
;   everything else    -> a generic operand loop (expressions / registers /
;                         strings, commas optional) terminated by EOL/EOF/comment
; Each emits one DIRECTIVE record. Exit: CY clear on success, CY set on error
; (PARSE_ERR set). An unknown directive name is an error.
; ===========================================================================
parse_directive:
                ; Look up the directive id from the current token's span.
                ld      hl, (PARSE_TOK)
                inc     hl
                ld      e, (hl)
                inc     hl
                ld      d, (hl)             ; DE = span ptr
                inc     hl
                ld      c, (hl)             ; C = span len low (directive names < 256)
                ex      de, hl              ; HL = span ptr
                call    directive_lookup    ; A = found, L = id
                or      a
                jp      z, pd_err           ; unknown directive
                ld      a, l
                ld      (PD_DIRID), a       ; save directive id
                call    parse_advance_tok   ; consume the directive token
                ld      a, (PD_DIRID)
                cp      DIR_SECTION
                jp      z, parse_directive_section
                cp      DIR_ARCH
                jp      z, parse_directive_rest
                cp      DIR_CPU
                jp      z, parse_directive_rest
                ; --- generic operand loop ---
                ld      hl, PARSE_OPSBUF
                ld      (PI_OPSPTR), hl
                xor     a
                ld      (PI_COUNT), a
pd_loop:
                ld      hl, (PARSE_TOK)
                ld      a, (hl)
                cp      TOK_EOL
                jr      z, pd_emit
                cp      TOK_EOF
                jr      z, pd_emit
                cp      TOK_LINECOMMENT
                jr      z, pd_emit
                cp      TOK_BLOCKCOMMENT
                jr      z, pd_emit
                cp      TOK_COMMA
                jr      z, pd_comma
                cp      TOK_STRING
                jr      z, pd_string
                call    parse_operand       ; expression / register operand
                jp      c, pd_err
                jr      pd_count
pd_string:
                call    emit_string_operand
                call    parse_advance_tok
pd_count:
                ld      a, (PI_COUNT)
                inc     a
                ld      (PI_COUNT), a
                jr      pd_loop
pd_comma:
                ld      a, (PI_COUNT)
                or      a
                jp      z, pd_err           ; leading comma
                call    parse_advance_tok   ; commas are optional separators
                jr      pd_loop
pd_emit:
                call    parse_emit_directive
                or      a                   ; clear carry: success
                ret
pd_err:
                ld      a, 1
                ld      (PARSE_ERR), a
                scf
                ret

; ===========================================================================
; parse_org_rhs — emit a `.org expr` DIRECTIVE for the GNU as `. = expr` form.
; (Port of parseOrgRHS, parser.go:197-214.) The leading '.' and '=' are already
; consumed by the caller (pr_dot). The expression runs to EOL/EOF/comment and is
; emitted as a single OP_KIND_IMM_EXPR operand. Exit: CY clear on success, CY set
; on error.
; ===========================================================================
parse_org_rhs:
                ld      hl, PARSE_OPSBUF
                ld      (PI_OPSPTR), hl
                ld      a, DIR_ORG
                ld      (PD_DIRID), a
                ld      hl, EXPR_BUF
                ld      (EXPR_PTR), hl
                xor     a                   ; minPrec = 0
                call    parse_expr_prec
                jp      c, pd_err
                call    expr_fold
                call    emit_imm_expr_operand
                ; The expression must be followed by a statement terminator.
                ld      hl, (PARSE_TOK)
                ld      a, (hl)
                cp      TOK_EOL
                jr      z, pog_ok
                cp      TOK_EOF
                jr      z, pog_ok
                cp      TOK_LINECOMMENT
                jr      z, pog_ok
                cp      TOK_BLOCKCOMMENT
                jr      z, pog_ok
                jp      pd_err
pog_ok:
                ld      a, 1
                ld      (PI_COUNT), a
                call    parse_emit_directive
                or      a
                ret

; ===========================================================================
; parse_directive_section — B6 parse for `.section NAME[, "flags"[, %type]]`.
; (Port of parseDirectiveSection, parser.go:234-283.) Operands:
;   op0: OP_KIND_SYS_NAME(NAME)      — bareword section name (required)
;   op1: OP_KIND_STRING(flags)       — ELF flags string (optional)
;   op2: OP_KIND_SYS_NAME("%type")   — '%'-prefixed ELF type keyword (optional)
; refenc treats .section as a flat-layout no-op; the operands are preserved only
; so bin2text round-trips the source. Entry: directive token consumed,
; PD_DIRID = DIR_SECTION. Exit: CY clear on success, CY set on error.
; ===========================================================================
parse_directive_section:
                ld      hl, PARSE_OPSBUF
                ld      (PI_OPSPTR), hl
                ; op0: section NAME (bareword identifier, required).
                ld      hl, (PARSE_TOK)
                ld      a, (hl)
                cp      TOK_IDENT
                jp      nz, pd_err
                call    emit_sysname_operand
                call    parse_advance_tok
                ld      a, 1
                ld      (PI_COUNT), a
                ; optional ', "flags"'
                ld      hl, (PARSE_TOK)
                ld      a, (hl)
                cp      TOK_COMMA
                jr      nz, psec_end
                call    parse_advance_tok
                ld      hl, (PARSE_TOK)
                ld      a, (hl)
                cp      TOK_STRING
                jp      nz, pd_err
                call    emit_string_operand
                call    parse_advance_tok
                ld      a, 2
                ld      (PI_COUNT), a
                ; optional ', %type'
                ld      hl, (PARSE_TOK)
                ld      a, (hl)
                cp      TOK_COMMA
                jr      nz, psec_end
                call    parse_advance_tok
                ld      hl, (PARSE_TOK)
                ld      a, (hl)
                cp      TOK_PERCENT
                jp      nz, pd_err
                call    parse_advance_tok
                ld      hl, (PARSE_TOK)
                ld      a, (hl)
                cp      TOK_IDENT
                jp      nz, pd_err
                call    emit_sysname_pct_operand    ; "%" + type keyword
                call    parse_advance_tok
                ld      a, 3
                ld      (PI_COUNT), a
psec_end:
                ld      hl, (PARSE_TOK)
                ld      a, (hl)
                cp      TOK_EOL
                jr      z, psec_emit
                cp      TOK_EOF
                jr      z, psec_emit
                cp      TOK_LINECOMMENT
                jr      z, psec_emit
                cp      TOK_BLOCKCOMMENT
                jr      z, psec_emit
                jp      pd_err
psec_emit:
                call    parse_emit_directive
                or      a
                ret

; ===========================================================================
; parse_directive_rest — B6 parse for `.arch`/`.cpu`: consume the rest of the
; logical line as a single OP_KIND_SYS_NAME operand whose text is the
; concatenation of the operand tokens' source spellings (whitespace dropped, so
; `armv8-a` survives the '-' that would otherwise be a binary operator). (Port of
; parseDirectiveRestOfLineAsSysName, parser.go:297-315.) With no operand tokens
; the directive is emitted with zero operands. Entry: directive token consumed,
; PD_DIRID = DIR_ARCH/DIR_CPU. Exit: CY clear (always succeeds).
; ===========================================================================
parse_directive_rest:
                ld      hl, PARSE_OPSBUF
                ld      (PI_OPSPTR), hl
                ; Build the concatenated spelling into SPELL_BUF.
                ld      hl, SPELL_BUF
                ld      (SPELL_PTR), hl
prest_loop:
                ld      hl, (PARSE_TOK)
                ld      a, (hl)
                cp      TOK_EOL
                jr      z, prest_done
                cp      TOK_EOF
                jr      z, prest_done
                cp      TOK_LINECOMMENT
                jr      z, prest_done
                cp      TOK_BLOCKCOMMENT
                jr      z, prest_done
                call    tok_spelling_append ; append this token's spelling
                call    parse_advance_tok
                jr      prest_loop
prest_done:
                ; If any text was collected, emit one OP_KIND_SYS_NAME operand.
                ld      hl, (SPELL_PTR)
                ld      de, SPELL_BUF
                or      a
                sbc     hl, de              ; HL = collected length
                ld      a, h
                or      l
                jr      z, prest_zero       ; no operand tokens -> zero operands
                ld      b, h
                ld      c, l                ; BC = name length
                ld      hl, (PI_OPSPTR)
                ld      (hl), OP_KIND_SYS_NAME
                inc     hl
                ld      (hl), c             ; name_len low
                inc     hl
                ld      (hl), b             ; name_len high
                inc     hl
                ex      de, hl              ; DE = operand dest
                ld      hl, SPELL_BUF       ; HL = collected text
                ldir
                ld      (PI_OPSPTR), de
                ld      a, 1
                ld      (PI_COUNT), a
                jr      prest_emit
prest_zero:
                xor     a
                ld      (PI_COUNT), a
prest_emit:
                call    parse_emit_directive
                or      a
                ret

; ===========================================================================
; tok_spelling_append — append the source spelling of the token at (PARSE_TOK)
; to the buffer at (SPELL_PTR), advancing (SPELL_PTR). (Port of tokSpelling,
; parser.go:321-374.) TOK_IDENT and TOK_INT copy their source span; every other
; punctuation/operator token contributes a fixed literal from TOK_SPELL. Tokens
; with no spelling contribute nothing. Clobbers A/BC/DE/HL.
; ===========================================================================
tok_spelling_append:
                ld      hl, (PARSE_TOK)
                ld      a, (hl)
                cp      TOK_IDENT
                jr      z, tsa_span
                cp      TOK_INT
                jr      z, tsa_span
                ; Punctuation/operator: look up its fixed spelling in TOK_SPELL,
                ; indexed by (kind - TOK_COMMA). Kinds below TOK_COMMA, or any
                ; out-of-range kind, contribute nothing.
                sub     TOK_COMMA
                ret     c                   ; kind < TOK_COMMA: no spelling
                cp      TOK_SPELL_COUNT
                ret     nc                  ; kind > last tabled: no spelling
                ; entry = TOK_SPELL + kind_index*2 -> [len:1][char:1] (len 1 or 2)
                add     a, a                ; *2 (2 bytes per entry)
                ld      e, a
                ld      d, 0
                ld      hl, TOK_SPELL
                add     hl, de              ; HL -> entry
                ld      b, (hl)             ; B = spelling length (0, 1 or 2)
                inc     hl                  ; HL -> spelling char
                ld      a, b
                or      a
                ret     z                   ; no spelling for this token kind
                ; For a 2-char entry ('<<'/'>>') the single stored char repeats.
                ld      a, (hl)             ; A = the spelling char
                ld      hl, (SPELL_PTR)
tsa_punc_copy:
                ld      (hl), a
                inc     hl
                djnz    tsa_punc_copy
                ld      (SPELL_PTR), hl
                ret
tsa_span:
                ; Copy the token's source span (ptr at offset 1, len at offset 3).
                ld      hl, (PARSE_TOK)
                inc     hl
                ld      e, (hl)
                inc     hl
                ld      d, (hl)             ; DE = span ptr
                inc     hl
                ld      c, (hl)
                inc     hl
                ld      b, (hl)             ; BC = span len
                ld      a, b
                or      c
                ret     z                   ; empty span -> nothing
                ld      hl, (SPELL_PTR)
                ex      de, hl              ; HL = span src, DE = dest
                ldir
                ld      (SPELL_PTR), de
                ret

; ===========================================================================
; directive_lookup — map a directive name's bytes to its id (index into
; DIRECTIVE_NAMES). Same linear-search shape as mnemonic_lookup; the table is
; small (~22 entries) and one directive is looked up per directive line.
; Entry: HL = candidate name bytes; C = name length (B ignored).
; Exit:  A = 1 and L = id (the table index) if matched; A = 0 if not found.
;        BC/DE/HL clobbered.
; ===========================================================================
directive_lookup:
                ld      (DL_PTR), hl        ; save candidate pointer
                ld      a, c
                ld      (DL_LEN), a         ; save candidate length
                ld      de, DIRECTIVE_NAMES
                ld      hl, 0               ; L = running index = candidate id (H stays 0)
dl_loop:
                ld      a, (de)             ; entry length (0 = sentinel)
                or      a
                jr      z, dl_notfound
                ld      b, a                ; B = entry length
                ld      a, (DL_LEN)
                cp      b
                jr      nz, dl_next         ; length mismatch -> skip entry
                push    hl                  ; save index
                push    de                  ; save entry pointer (at length byte)
                inc     de                  ; DE -> entry name bytes
                ld      hl, (DL_PTR)        ; HL -> candidate bytes
dl_cmp:
                ld      a, (de)
                cp      (hl)
                jr      nz, dl_cmp_fail
                inc     de
                inc     hl
                djnz    dl_cmp
                pop     de                  ; discard saved entry pointer
                pop     hl                  ; HL = index (id in L)
                ld      a, 1
                ret
dl_cmp_fail:
                pop     de                  ; restore entry pointer (length byte)
                pop     hl                  ; restore index
dl_next:
                ld      a, (de)             ; entry length
                inc     de                  ; past the length byte
                add     a, e
                ld      e, a
                jr      nc, dl_next_nc
                inc     d
dl_next_nc:
                inc     l                   ; index++
                jr      dl_loop
dl_notfound:
                xor     a
                ret

; ===========================================================================
; emit_string_operand — append an OP_KIND_STRING operand at (PI_OPSPTR) from the
; current token's (PARSE_TOK) TOK_STRING span (decoded body in LEX_STRPOOL):
; [&09, len:2 LE, body[]]. (Port of OperandWriter.WriteString, operands.go:200.)
; Advances (PI_OPSPTR); does NOT consume the token. Clobbers AF/BC/DE/HL.
; ===========================================================================
emit_string_operand:
                ld      hl, (PARSE_TOK)
                inc     hl
                ld      e, (hl)
                inc     hl
                ld      d, (hl)             ; DE = span ptr (into LEX_STRPOOL)
                inc     hl
                ld      c, (hl)             ; body_len low
                inc     hl
                ld      b, (hl)             ; body_len high
                ld      hl, (PI_OPSPTR)
                ld      (hl), OP_KIND_STRING
                inc     hl
                ld      (hl), c             ; body_len low
                inc     hl
                ld      (hl), b             ; body_len high
                inc     hl
                ex      de, hl              ; HL = body src, DE = operand dest
                ld      a, b
                or      c
                jr      z, estr_done        ; empty string body
                ldir
estr_done:
                ld      (PI_OPSPTR), de
                ret

; ===========================================================================
; emit_sysname_pct_operand — append an OP_KIND_SYS_NAME operand at (PI_OPSPTR)
; whose name is '%' followed by the current TOK_IDENT span: [&0B, len+1:2 LE,
; '%', name[]]. (Port of `WriteSysName("%" + text)`, parser.go:270.) Advances
; (PI_OPSPTR); does NOT consume the token. Clobbers AF/BC/DE/HL.
; ===========================================================================
emit_sysname_pct_operand:
                ld      hl, (PARSE_TOK)
                inc     hl
                ld      e, (hl)
                inc     hl
                ld      d, (hl)             ; DE = span ptr
                inc     hl
                ld      c, (hl)
                inc     hl
                ld      b, (hl)             ; BC = name len
                ; total length = name_len + 1 (the leading '%')
                inc     bc
                ld      hl, (PI_OPSPTR)
                ld      (hl), OP_KIND_SYS_NAME
                inc     hl
                ld      (hl), c             ; len low
                inc     hl
                ld      (hl), b             ; len high
                inc     hl
                ld      (hl), &25           ; leading '%'
                inc     hl
                ex      de, hl              ; HL = name src, DE = operand dest
                dec     bc                  ; BC back to name_len for the copy
                ld      a, b
                or      c
                jr      z, espc_done        ; (defensive) empty name
                ldir
espc_done:
                ld      (PI_OPSPTR), de
                ret

; ===========================================================================
; parse_emit_directive — write one DIRECTIVE record from PD_DIRID + PI_COUNT +
; the operand bytes in PARSE_OPSBUF, advancing PARSE_RECPTR and PARSE_RECN.
; (Port of emitDirective, parser.go:60-67; framing from RecordWriter.WriteDirective,
; writer.go:83.) Framing: [REC_KIND_DIRECTIVE | len:2 LE | dir_id:1 | op_count:1 |
; operands[]], len = 2 + operands_len.
; ===========================================================================
parse_emit_directive:
                ld      hl, (PI_OPSPTR)
                ld      de, PARSE_OPSBUF
                or      a
                sbc     hl, de              ; HL = operand byte length
                ld      b, h
                ld      c, l                ; BC = operand byte length
                ; payload length = 2 + operand length
                inc     bc
                inc     bc
                ld      hl, (PARSE_RECPTR)
                ld      (hl), REC_KIND_DIRECTIVE
                inc     hl
                ld      (hl), c             ; len low
                inc     hl
                ld      (hl), b             ; len high
                inc     hl
                ld      a, (PD_DIRID)
                ld      (hl), a             ; directive id
                inc     hl
                ld      a, (PI_COUNT)
                ld      (hl), a             ; operand count
                inc     hl
                ; copy operand bytes (length = payload - 2)
                dec     bc
                dec     bc
                ex      de, hl              ; DE = record write ptr (after header)
                ld      hl, PARSE_OPSBUF
                ld      a, b
                or      c
                jr      z, ped_nocopy
                ldir
ped_nocopy:
                ld      (PARSE_RECPTR), de
                ld      hl, (PARSE_RECN)
                inc     hl
                ld      (PARSE_RECN), hl
                ret

; ===========================================================================
; expr_buf_single_imm — if EXPR_BUF..(EXPR_PTR) holds exactly one PUSH_IMMn (the
; folded-constant form produced by expr_fold), load its sign-extended int64 into
; IMM_VAL and return CY clear. Otherwise (a symbolic/raw bytecode stream, or a
; literal push with trailing bytes) return CY set, IMM_VAL untouched. Clobbers
; A/BC/DE/HL.
; ===========================================================================
expr_buf_single_imm:
                ld      hl, EXPR_BUF
                ld      a, (hl)
                cp      EXPR_PUSH_IMM8
                jr      z, ebsi_n1
                cp      EXPR_PUSH_IMM16
                jr      z, ebsi_n2
                cp      EXPR_PUSH_IMM32
                jr      z, ebsi_n4
                cp      EXPR_PUSH_IMM64
                jr      z, ebsi_n8
                scf
                ret                         ; not a literal push -> symbolic
ebsi_n1:        ld      b, 1
                jr      ebsi_chk
ebsi_n2:        ld      b, 2
                jr      ebsi_chk
ebsi_n4:        ld      b, 4
                jr      ebsi_chk
ebsi_n8:        ld      b, 8
ebsi_chk:
                ; require total length == 1 + B, else there are trailing ops
                ; (a non-folded stream that merely starts with a literal).
                ld      hl, (EXPR_PTR)
                ld      de, EXPR_BUF
                or      a
                sbc     hl, de              ; HL = total bytes
                ld      a, h
                or      a
                jr      nz, ebsi_sym        ; length >= 256 -> symbolic
                ld      a, b
                inc     a                   ; 1 + B
                cp      l
                jr      nz, ebsi_sym
                ; constant: copy B value bytes, then sign-extend to 8.
                ld      hl, EXPR_BUF+1
                ld      de, IMM_VAL
                ld      c, b
                ld      b, 0
                push    bc                  ; save width
                ldir                        ; copy C value bytes
                pop     bc                  ; C = width
                ; sign byte from the last value byte IMM_VAL[width-1].
                ld      hl, IMM_VAL
                dec     bc                  ; width-1
                add     hl, bc              ; HL -> IMM_VAL[width-1]
                inc     bc                  ; restore width
                ld      a, (hl)
                add     a, a                ; bit7 -> carry
                sbc     a, a                ; A = 0x00 / 0xFF sign fill
                inc     hl                  ; HL -> first fill byte
                ld      d, a                ; D = fill byte
                ld      a, 8
                sub     c                   ; A = 8 - width = fill count
                jr      z, ebsi_ok
                ld      b, a
ebsi_fill:
                ld      (hl), d
                inc     hl
                djnz    ebsi_fill
ebsi_ok:
                or      a                   ; clear carry: constant, IMM_VAL set
                ret
ebsi_sym:
                scf
                ret

; ===========================================================================
; expr_append_raw — copy BC bytes from HL to (EXPR_PTR), advancing (EXPR_PTR).
; A zero count is a no-op (never an ldir of 65536). Clobbers A/BC/DE/HL.
; ===========================================================================
expr_append_raw:
                ld      a, b
                or      c
                ret     z
                ld      de, (EXPR_PTR)
                ldir
                ld      (EXPR_PTR), de
                ret

; ===========================================================================
; parse_operand — dispatch one operand at the current token. (Port of
; parseOperand's switch, parser.go:1023-1090.) An identifier -> parse_operand_reg,
; which resolves it to a register operand (with optional shift/extend suffix for
; X/W registers, B4b) or, if it is not a register, a symbol expression. An
; expression-leading token (`#` / int / unary `-` / `~` / `(`) ->
; parse_operand_expr. The `.`, local-refs and `:reloc:` primaries are B3c. A
; `[` token -> parse_operand_mem (B4).
; ===========================================================================
parse_operand:
                ld      hl, (PARSE_TOK)
                ld      a, (hl)
                cp      TOK_IDENT
                jp      z, parse_operand_reg ; register; non-reg ident -> B3b
                cp      TOK_HASH
                jp      z, parse_operand_expr
                cp      TOK_INT
                jp      z, parse_operand_expr
                cp      TOK_MINUS
                jp      z, parse_operand_expr
                cp      TOK_TILDE
                jp      z, parse_operand_expr
                cp      TOK_LPAREN
                jp      z, parse_operand_expr
                cp      TOK_DOT
                jp      z, parse_operand_expr
                cp      TOK_LOCALREF
                jp      z, parse_operand_expr
                cp      TOK_COLON
                jp      z, parse_operand_expr
                cp      TOK_LBRACKET
                jp      z, parse_operand_mem ; B4: memory operands
                scf                         ; out of domain
                ret

; ===========================================================================
; parse_operand_reg — the TOK_IDENT operand path: a register identifier appends
; its operand bytes to (PI_OPSPTR). For X or W registers, a following comma +
; shift/extend keyword suffix produces a SHIFTED_REG or EXTENDED_REG operand;
; a non-register identifier falls through to parse_operand_expr. (Port of
; parseOperand's register branch, parser.go:1032-1073.)
; Exit: CY clear on success (token(s) consumed), CY set on error.
; ===========================================================================
parse_operand_reg:
                ld      hl, (PARSE_TOK)
                ld      a, (hl)
                cp      TOK_IDENT
                jp      nz, por_err         ; caller dispatches only TOK_IDENT here
                inc     hl
                ld      e, (hl)
                inc     hl
                ld      d, (hl)             ; DE = span_ptr
                inc     hl
                ld      a, (hl)             ; A = span_len low (reg names < 256)
                ex      de, hl              ; HL = span_ptr
                ; check for condition-code name before register (Go: matchCond first,
                ; parser.go:1027)
                call    match_cond          ; CY set + A = CondCode if matched
                jr      nc, por_try_reg     ; not a cond name -> try register
                ; condition-code matched: A = cond value; save it across parse_advance_tok
                ld      (MC_COND), a
                call    parse_advance_tok   ; consume the cond token
                ; emit [OP_KIND_COND, condValue] at (PI_OPSPTR)
                ld      hl, (PI_OPSPTR)
                ld      (hl), OP_KIND_COND
                inc     hl
                ld      a, (MC_COND)
                ld      (hl), a
                inc     hl
                ld      (PI_OPSPTR), hl
                or      a                   ; clear carry: success
                ret
por_try_reg:
                ; restore HL = span_ptr and A = span_len for match_reg
                ld      hl, (MC_PTR)
                ld      a, (MC_LEN)
                call    match_reg           ; CY = is-reg, B = kind, C = reg
                jp      nc, parse_operand_expr ; not a register -> symbol expression
                ; register matched: save kind (B) and reg (C) and consume the token
                ld      a, b
                ld      (POR_KIND), a       ; save kind
                ld      a, c
                ld      (POR_REG), a        ; save register number
                call    parse_advance_tok   ; consume the register token

                ; shift/extend lookahead: only for X(1) or W(2) register kinds
                ld      a, (POR_KIND)
                cp      OP_KIND_REG_X
                jr      z, por_check_suffix
                cp      OP_KIND_REG_W
                jr      z, por_check_suffix
                jp      por_emit_plain      ; XSP/WSP: no suffix, emit plain reg

por_check_suffix:
                ; peek at the next token: must be TOK_COMMA
                ld      hl, (PARSE_TOK)
                ld      a, (hl)
                cp      TOK_COMMA
                jp      nz, por_emit_plain  ; no comma -> emit plain reg

                ; peek one more: the token after the comma must be TOK_IDENT
                ld      de, TOK_REC_SIZE
                add     hl, de              ; HL -> token after comma
                ld      a, (hl)
                cp      TOK_IDENT
                jp      nz, por_emit_plain  ; not an ident -> emit plain reg + leave comma

                ; extract the ident's span ptr and length
                inc     hl
                ld      e, (hl)
                inc     hl
                ld      d, (hl)             ; DE = span_ptr
                inc     hl
                ld      a, (hl)             ; A = span_len
                ld      (POR_KWLEN), a
                ld      (POR_KWPTR), de

                ; try match_shift_kind on the keyword
                ex      de, hl              ; HL = span_ptr
                ld      a, (POR_KWLEN)
                call    match_shift_kind    ; CY set + A = ShiftKind if matched
                jp      nc, por_try_extend

                ; shift keyword matched — consume comma + keyword, require '#'
                ld      (POR_SHIFTKIND), a  ; save ShiftKind
                call    parse_advance_tok   ; consume comma
                call    parse_advance_tok   ; consume shift keyword
                ld      hl, (PARSE_TOK)
                ld      a, (hl)
                cp      TOK_HASH
                jp      nz, por_err         ; '#' required after shift
                call    parse_advance_tok   ; consume '#'
                ; parse the amount expression into EXPR_BUF
                ld      hl, EXPR_BUF
                ld      (EXPR_PTR), hl
                xor     a                   ; minPrec = 0
                call    parse_expr_prec
                jp      c, por_err          ; parse error in amount
                call    expr_fold
                ; compute width: 1 if X, 0 if W
                ld      a, (POR_KIND)
                cp      OP_KIND_REG_X
                ld      a, 0
                jp      nz, por_shift_emit
                ld      a, 1
por_shift_emit:
                ld      (POR_WIDTH), a
                ; emit [OP_KIND_SHIFTED_REG, width, reg, shiftKind, len_lo, len_hi, expr...]
                ld      hl, (PI_OPSPTR)
                ld      (hl), OP_KIND_SHIFTED_REG
                inc     hl
                ld      a, (POR_WIDTH)
                ld      (hl), a
                inc     hl
                ld      a, (POR_REG)
                ld      (hl), a
                inc     hl
                ld      a, (POR_SHIFTKIND)
                ld      (hl), a
                inc     hl
                ; write expr length (LE u16) and copy expr bytes
                ld      hl, (EXPR_PTR)
                ld      de, EXPR_BUF
                or      a
                sbc     hl, de              ; HL = expr byte count
                ld      b, h
                ld      c, l                ; BC = expr byte count
                ld      hl, (PI_OPSPTR)
                ld      de, 4
                add     hl, de              ; HL -> len_lo slot
                ld      (hl), c             ; len_lo
                inc     hl
                ld      (hl), b             ; len_hi
                inc     hl
                ex      de, hl              ; DE = dest (after len)
                ld      hl, EXPR_BUF        ; HL = source
                ld      a, b
                or      c
                jr      z, por_shift_nocopy
                ldir
por_shift_nocopy:
                ld      (PI_OPSPTR), de
                or      a                   ; clear carry: success
                ret

por_try_extend:
                ; try match_extend on the keyword
                ld      hl, (POR_KWPTR)
                ld      a, (POR_KWLEN)
                call    match_extend        ; CY set + A = ExtendKind if matched
                jp      nc, por_emit_plain  ; unknown keyword -> emit plain reg + leave comma

                ; extend keyword matched — consume comma + keyword
                ld      (POR_EXTKIND), a    ; save ExtendKind
                call    parse_advance_tok   ; consume comma
                call    parse_advance_tok   ; consume extend keyword
                ; '#amt' is OPTIONAL for extend
                ld      hl, EXPR_BUF
                ld      (EXPR_PTR), hl      ; reset buffer (empty by default)
                ld      hl, (PARSE_TOK)
                ld      a, (hl)
                cp      TOK_HASH
                jp      nz, por_ext_emit    ; no '#' -> empty amount
                call    parse_advance_tok   ; consume '#'
                ; parse the amount expression
                xor     a                   ; minPrec = 0
                call    parse_expr_prec
                jp      c, por_err          ; parse error
                call    expr_fold
por_ext_emit:
                ; compute width: 1 if X, 0 if W
                ld      a, (POR_KIND)
                cp      OP_KIND_REG_X
                ld      a, 0
                jp      nz, por_ext_width_done
                ld      a, 1
por_ext_width_done:
                ld      (POR_WIDTH), a
                ; expr byte count = EXPR_PTR - EXPR_BUF
                ld      hl, (EXPR_PTR)
                ld      de, EXPR_BUF
                or      a
                sbc     hl, de
                ld      b, h
                ld      c, l                ; BC = expr byte count (0 if no #amt)
                ; emit [OP_KIND_EXTENDED_REG, width, reg, extKind, len_lo, len_hi, expr...]
                ld      hl, (PI_OPSPTR)
                ld      (hl), OP_KIND_EXTENDED_REG
                inc     hl
                ld      a, (POR_WIDTH)
                ld      (hl), a
                inc     hl
                ld      a, (POR_REG)
                ld      (hl), a
                inc     hl
                ld      a, (POR_EXTKIND)
                ld      (hl), a
                inc     hl
                ld      (hl), c             ; len_lo
                inc     hl
                ld      (hl), b             ; len_hi
                inc     hl
                ex      de, hl              ; DE = dest (after header)
                ld      hl, EXPR_BUF
                ld      a, b
                or      c
                jr      z, por_ext_nocopy
                ldir
por_ext_nocopy:
                ld      (PI_OPSPTR), de
                or      a                   ; clear carry: success
                ret

por_emit_plain:
                ; emit plain register: [kind, reg] at PI_OPSPTR
                ld      hl, (PI_OPSPTR)
                ld      a, (POR_KIND)
                ld      (hl), a             ; operand kind
                inc     hl
                ld      a, (POR_REG)
                ld      (hl), a             ; register number
                inc     hl
                ld      (PI_OPSPTR), hl
                or      a                   ; clear carry: success
                ret
por_err:
                ld      a, 1
                ld      (PARSE_ERR), a
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
                call    emit_imm_expr_operand
                or      a                   ; clear carry: success
                ret

; ===========================================================================
; emit_imm_expr_operand — append an OP_KIND_IMM_EXPR operand at (PI_OPSPTR)
; carrying the expression bytecode currently in EXPR_BUF..(EXPR_PTR). Advances
; (PI_OPSPTR) past the new operand. (The WriteImmExpr tail of parseOperand's
; expression path, shared by parse_operand_expr and the B5 special forms.)
; Clobbers A/BC/DE/HL.
; ===========================================================================
emit_imm_expr_operand:
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
                jr      z, eieo_nocopy      ; (defensive) zero-length expr
                ldir                        ; copy expr bytes; DE -> end
eieo_nocopy:
                ld      (PI_OPSPTR), de
                ret

; ===========================================================================
; emit_litpool_operand — append an OP_KIND_LIT_POOL operand at (PI_OPSPTR)
; carrying the expression bytecode currently in EXPR_BUF..(EXPR_PTR):
; [OP_KIND_LIT_POOL, width:1, expr_len:2 LE, expr[]]. width is taken from
; (LITPOOL_WIDTH) (4 for a W destination, 8 for X). Advances (PI_OPSPTR) past the
; new operand. (Port of OperandWriter.WriteLitPool, operands.go:213.) Clobbers
; A/BC/DE/HL.
; ===========================================================================
emit_litpool_operand:
                ; expr_len = EXPR_PTR - EXPR_BUF
                ld      hl, (EXPR_PTR)
                ld      de, EXPR_BUF
                or      a
                sbc     hl, de
                ld      b, h
                ld      c, l                ; BC = expr byte length
                ld      hl, (PI_OPSPTR)
                ld      (hl), OP_KIND_LIT_POOL
                inc     hl
                ld      a, (LITPOOL_WIDTH)
                ld      (hl), a             ; width (4 or 8)
                inc     hl
                ld      (hl), c             ; expr_len low
                inc     hl
                ld      (hl), b             ; expr_len high
                inc     hl
                ex      de, hl              ; DE = operand dest (after the header)
                ld      hl, EXPR_BUF        ; HL = source bytecode
                ld      a, b
                or      c
                jr      z, elpo_nocopy      ; (defensive) zero-length expr
                ldir                        ; copy expr bytes; DE -> end
elpo_nocopy:
                ld      (PI_OPSPTR), de
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
; parseExprPrimary, parser.go:1203-1290.) Emits bytecode into (EXPR_PTR).
; Exit: CY set on error. Handles `#` (consume + recurse), an integer literal,
; a symbol identifier, unary `-`/`~`, a parenthesised sub-expression, `.`
; (PC), a local-ref (`1f`/`2b`), and a `:reloc:` prefix. This covers the
; complete primary set (B3a–B3c).
; ===========================================================================
parse_expr_primary:
                ld      hl, (PARSE_TOK)
                ld      a, (hl)
                cp      TOK_HASH
                jr      z, ppr_hash
                cp      TOK_INT
                jr      z, ppr_int
                cp      TOK_IDENT
                jr      z, ppr_ident
                cp      TOK_MINUS
                jr      z, ppr_neg
                cp      TOK_TILDE
                jr      z, ppr_not
                cp      TOK_LPAREN
                jr      z, ppr_paren
                cp      TOK_DOT
                jp      z, ppr_pc
                cp      TOK_LOCALREF
                jp      z, ppr_localref
                cp      TOK_COLON
                jp      z, ppr_reloc
                scf                         ; not a known primary -> error
                ret
ppr_hash:
                call    parse_advance_tok   ; consume '#'
                jr      parse_expr_primary  ; tail-recurse on the next primary
ppr_int:
                call    expr_emit_imm_from_tok
                call    parse_advance_tok   ; consume the int token
                or      a                   ; clear carry: success
                ret
ppr_ident:
                ; intern the identifier and emit PUSH_SYM, id (port of
                ; parseExprPrimary's TokIdent: st.Intern + WriteSym).
                ld      hl, (PARSE_TOK)
                inc     hl
                ld      e, (hl)
                inc     hl
                ld      d, (hl)             ; DE = span_ptr
                inc     hl
                ld      c, (hl)             ; C = span_len low (idents < 256)
                ex      de, hl              ; HL = span_ptr
                call    sym_intern          ; HL = symbol id (u16)
                ld      (PPR_SYMID), hl
                ld      a, EXPR_PUSH_SYM
                call    expr_emit_byte
                ld      a, (PPR_SYMID)
                call    expr_emit_byte      ; id low
                ld      a, (PPR_SYMID+1)
                call    expr_emit_byte      ; id high
                call    parse_advance_tok   ; consume the identifier
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
ppr_pc:
                ; TOK_DOT → emit PUSH_PC (port of parseExprPrimary's TokDot:
                ; WritePC). No operand bytes; advance past '.'.
                ld      a, EXPR_PUSH_PC
                call    expr_emit_byte
                call    parse_advance_tok   ; consume '.'
                or      a                   ; clear carry: success
                ret
ppr_localref:
                ; TOK_LOCALREF → emit PUSH_LOCAL, digit, dir (port of
                ; parseExprPrimary's TokLocalRef: WriteLocal(digit, dir)).
                ; Token layout: kind(0) | span_ptr(1-2) | span_len(3-4) |
                ; base(5)='f'/'b' | value[0](6)=digit. dir=0 for 'f', 1 for 'b'.
                ld      hl, (PARSE_TOK)
                ld      de, 6
                add     hl, de
                ld      a, (hl)             ; A = digit (value[0])
                ld      (PPR_LOCDIG), a
                dec     hl                  ; offset 5 = base = 'f'/'b' char
                ld      a, (hl)             ; A = direction char
                ld      b, 0                ; B = dir = 0 (forward default)
                cp      &62                 ; 'b' = 0x62
                jr      nz, ppr_lr_emit
                ld      b, 1                ; B = dir = 1 (backward)
ppr_lr_emit:
                ld      a, EXPR_PUSH_LOCAL
                call    expr_emit_byte
                ld      a, (PPR_LOCDIG)
                call    expr_emit_byte      ; digit
                ld      a, b
                call    expr_emit_byte      ; dir (0=forward, 1=backward)
                call    parse_advance_tok   ; consume the TOK_LOCALREF token
                or      a                   ; clear carry: success
                ret
ppr_reloc:
                ; TOK_COLON → `:name:operand` (port of parseExprPrimary's
                ; TokColon: consume ':', require TOK_IDENT name, consume ':',
                ; recurse parseExprPrimary, emit relocOp(name)).
                call    parse_advance_tok   ; consume ':'
                ld      hl, (PARSE_TOK)
                ld      a, (hl)
                cp      TOK_IDENT
                jr      nz, ppr_reloc_err   ; expected name after ':'
                ; capture the reloc name span
                inc     hl
                ld      e, (hl)
                inc     hl
                ld      d, (hl)             ; DE = span_ptr
                inc     hl
                ld      a, (hl)             ; A = span_len low
                ld      (PPR_RELLEN), a
                ld      (PPR_RELPTR), de
                call    parse_advance_tok   ; consume the name token
                ld      hl, (PARSE_TOK)
                ld      a, (hl)
                cp      TOK_COLON
                jr      nz, ppr_reloc_err   ; expected ':' after name
                call    parse_advance_tok   ; consume ':'
                call    parse_expr_primary  ; recurse for the operand primary
                ret     c                   ; parse error in operand -> propagate
                ; look up the reloc name in RELOC_NAMES to get the op byte
                ld      hl, (PPR_RELPTR)
                ld      a, (PPR_RELLEN)
                ld      c, a                ; C = name length
                call    reloc_op_lookup     ; A = op byte; CY set if unknown
                jr      c, ppr_reloc_err
                call    expr_emit_byte      ; emit the reloc op
                or      a                   ; clear carry: success
                ret
ppr_reloc_err:
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
                cp      TOK_SLASH
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
                cp      TOK_SLASH
                jr      z, eeb_div
                scf
                ret
eeb_mul:        ld      a, EXPR_OP_MUL
                jr      eeb_emit
eeb_div:        ld      a, EXPR_OP_DIV
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
                cp      EXPR_OP_DIV
                jp      z, fold_do_div
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

; fold_do_div — a / b (signed, truncate toward zero; port of applyBinary(OpDiv)).
; A zero divisor yields 0. Otherwise the magnitudes are divided by an unsigned
; restoring long division and the quotient is negated iff the operand signs
; differ. MinInt64/-1 falls out as MinInt64 (|MinInt64| is 2^63 as unsigned,
; /1 = 2^63, no negate -> the two's-complement MinInt64).
fold_do_div:
                call    fold_ab_ptrs        ; HL -> a (dividend, dest), DE -> b
                jp      c, fold_fail
                push    hl                  ; save a's slot (result dest)
                ex      de, hl              ; HL = b
                ld      de, DIV_DVSR
                ld      bc, 8
                ldir                        ; DIV_DVSR = b
                pop     hl                  ; HL = a's slot
                push    hl
                ld      de, DIV_QUOT
                ld      bc, 8
                ldir                        ; DIV_QUOT = a (dividend)
                ld      hl, DIV_DVSR
                call    is_zero_8
                jr      nz, div_nonzero
                pop     hl                  ; b == 0 -> result 0
                call    fold_zero_a
                call    fold_pop1
                jp      fold_loop
div_nonzero:
                ld      hl, DIV_QUOT        ; result sign = sign(a) xor sign(b)
                call    sign_8
                ld      b, a                ; B = sign(a)
                ld      hl, DIV_DVSR
                call    sign_8
                xor     b
                ld      (DIV_SIGN), a
                ld      hl, DIV_QUOT        ; divide the magnitudes
                call    abs_8
                ld      hl, DIV_DVSR
                call    abs_8
                ld      hl, DIV_REM
                call    fold_zero_a         ; remainder starts at 0
                ld      b, 64
div_loop:
                push    bc
                ; shift the 128-bit {DIV_REM:DIV_QUOT} left by 1
                ld      hl, DIV_QUOT
                sla     (hl)                ; quot byte0 (bit0 <- 0)
                ld      b, 7
dq_shl:
                inc     hl
                rl      (hl)                ; quot bytes 1..7; carry-out = quot MSB
                djnz    dq_shl
                ld      hl, DIV_REM
                rl      (hl)                ; rem byte0 <- quot's old MSB
                ld      b, 7
dr_shl:
                inc     hl
                rl      (hl)                ; rem bytes 1..7
                djnz    dr_shl
                ld      a, 0
                adc     a, 0                ; A = the 65th bit shifted out of rem
                ld      (DIV_C2), a
                ; trial subtract DIV_DVSR from DIV_REM in place; capture borrow
                ld      hl, DIV_REM
                ld      de, DIV_DVSR
                or      a
                ld      b, 8
ds_loop:
                ld      a, (de)
                ld      c, a
                ld      a, (hl)
                sbc     a, c
                ld      (hl), a
                inc     hl
                inc     de
                djnz    ds_loop
                ld      a, 0
                adc     a, 0                ; A = borrow (1 = rem < dvsr)
                ld      c, a
                ld      a, (DIV_C2)
                or      a
                jr      nz, div_commit      ; 65th bit set -> rem >= dvsr
                ld      a, c
                or      a
                jr      nz, div_restore     ; borrow and no 65th bit -> rem < dvsr
div_commit:
                ld      hl, DIV_QUOT
                set     0, (hl)             ; quotient bit for this step
                jr      div_next
div_restore:
                ld      hl, DIV_REM         ; undo the trial subtract
                ld      de, DIV_DVSR
                or      a
                ld      b, 8
da_loop:
                ld      a, (de)
                ld      c, a
                ld      a, (hl)
                adc     a, c
                ld      (hl), a
                inc     hl
                inc     de
                djnz    da_loop
div_next:
                pop     bc
                djnz    div_loop
                ld      a, (DIV_SIGN)
                or      a
                jr      z, div_store
                ld      hl, DIV_QUOT
                call    neg_8               ; negate the quotient
div_store:
                pop     hl                  ; HL = a's slot (dest)
                ld      de, DIV_QUOT
                ex      de, hl              ; HL = DIV_QUOT (src), DE = dest
                ld      bc, 8
                ldir
                call    fold_pop1
                jp      fold_loop

; is_zero_8 — HL -> 8-byte LE value; Z set iff all bytes zero. HL/BC kept.
is_zero_8:
                push    hl
                push    bc
                xor     a
                ld      b, 8
izz_loop:
                or      (hl)
                inc     hl
                djnz    izz_loop
                pop     bc
                pop     hl
                or      a                   ; Z iff the OR of all bytes is 0
                ret

; sign_8 — HL -> 8-byte LE value; A = 1 if negative (MSB bit7 set), else 0.
; HL kept; clobbers DE.
sign_8:
                push    hl
                ld      de, 7
                add     hl, de              ; HL -> byte7 (MSB)
                ld      a, (hl)
                pop     hl
                add     a, a                ; bit7 -> carry
                sbc     a, a                ; A = 0x00 / 0xFF
                and     1
                ret

; neg_8 — HL -> 8-byte LE value; negate in place (two's complement). HL/BC kept.
neg_8:
                push    hl
                push    bc
                or      a                   ; clear borrow
                ld      b, 8
neg8_loop:
                ld      a, 0
                sbc     a, (hl)             ; 0 - byte - borrow
                ld      (hl), a
                inc     hl
                djnz    neg8_loop
                pop     bc
                pop     hl
                ret

; abs_8 — HL -> 8-byte LE value; negate in place iff negative. HL/BC kept.
abs_8:
                call    sign_8              ; A = sign (HL preserved)
                or      a
                ret     z
                jp      neg_8

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
; match_extend — is the identifier (HL = ptr, A = length) an extend keyword?
; (Port of matchExtend, parser.go:1398, + ExtendKind, operands.go.) The 8
; extend names are exact lowercase: uxtb uxth uxtw uxtx sxtb sxth sxtw sxtx.
; `lsl` is NOT in this table — it is handled separately by parse_operand_mem.
; Entry: HL = name ptr, A = length. Exit: CY set with A = ExtendKind value
; (0..7) if matched; CY clear otherwise. Clobbers A/BC/DE/HL.
; ===========================================================================
match_extend:
                ld      (ME_PTR), hl
                ld      (ME_LEN), a
                ld      de, EXT_NAMES
me_loop:
                ld      a, (de)             ; name length (0 = end)
                or      a
                jr      z, me_notfound
                ld      b, a                ; B = name length
                ld      a, (ME_LEN)
                cp      b
                jr      nz, me_skip         ; length mismatch
                push    de                  ; save entry pointer (at length byte)
                inc     de                  ; DE -> entry name bytes
                ld      hl, (ME_PTR)        ; HL -> candidate bytes
me_cmp:
                ld      a, (de)
                cp      (hl)
                jr      nz, me_cmp_fail
                inc     de
                inc     hl
                djnz    me_cmp
                ; matched: DE -> value byte
                ld      a, (de)
                pop     hl                  ; discard saved entry pointer
                scf                         ; CY set: matched, A = value
                ret
me_cmp_fail:
                pop     de                  ; restore entry pointer (length byte)
me_skip:
                ; advance DE past: length(1) + name[len] + value(1)
                ld      a, (de)
                add     a, 2
                add     a, e
                ld      e, a
                jr      nc, me_skip_nc
                inc     d
me_skip_nc:
                jr      me_loop
me_notfound:
                or      a                   ; clear carry: not matched
                ret

; ===========================================================================
; EXT_NAMES — extend name → ExtendKind value table (port of matchExtend,
; parser.go:1398 + ExtendKind.Name(), operands.go). Record: len:1 | name[len]
; | value:1; a 0-length record terminates. Names are exact lowercase.
; ===========================================================================
EXT_NAMES:
                defb    4
                defm    "uxtb"
                defb    EXT_UXTB
                defb    4
                defm    "uxth"
                defb    EXT_UXTH
                defb    4
                defm    "uxtw"
                defb    EXT_UXTW
                defb    4
                defm    "uxtx"
                defb    EXT_UXTX
                defb    4
                defm    "sxtb"
                defb    EXT_SXTB
                defb    4
                defm    "sxth"
                defb    EXT_SXTH
                defb    4
                defm    "sxtw"
                defb    EXT_SXTW
                defb    4
                defm    "sxtx"
                defb    EXT_SXTX
                defb    0                   ; sentinel

; ===========================================================================
; match_shift_kind — is the identifier (HL = ptr, A = length) a shift keyword?
; (Port of matchShiftKind, parser.go:1407, + ShiftKind, operands.go.) The 4
; shift names are exact lowercase: lsl(0) lsr(1) asr(2) ror(3).
; Entry: HL = name ptr, A = length. Exit: CY set with A = ShiftKind value
; (0..3) if matched; CY clear otherwise. Clobbers A/BC/DE/HL.
; ===========================================================================
match_shift_kind:
                ld      (MSK_PTR), hl
                ld      (MSK_LEN), a
                ld      de, SHIFT_NAMES
msk_loop:
                ld      a, (de)             ; name length (0 = end)
                or      a
                jr      z, msk_notfound
                ld      b, a                ; B = name length
                ld      a, (MSK_LEN)
                cp      b
                jr      nz, msk_skip        ; length mismatch
                push    de                  ; save entry pointer (at length byte)
                inc     de                  ; DE -> entry name bytes
                ld      hl, (MSK_PTR)       ; HL -> candidate bytes
msk_cmp:
                ld      a, (de)
                cp      (hl)
                jr      nz, msk_cmp_fail
                inc     de
                inc     hl
                djnz    msk_cmp
                ; matched: DE -> value byte
                ld      a, (de)
                pop     hl                  ; discard saved entry pointer
                scf                         ; CY set: matched, A = value
                ret
msk_cmp_fail:
                pop     de                  ; restore entry pointer (length byte)
msk_skip:
                ; advance DE past: length(1) + name[len] + value(1)
                ld      a, (de)
                add     a, 2
                add     a, e
                ld      e, a
                jr      nc, msk_skip_nc
                inc     d
msk_skip_nc:
                jr      msk_loop
msk_notfound:
                or      a                   ; clear carry: not matched
                ret

; ===========================================================================
; SHIFT_NAMES — shift name → ShiftKind value table (port of matchShiftKind,
; parser.go:1407 + ShiftKind.Name(), operands.go). Record: len:1 | name[len]
; | value:1; a 0-length record terminates. Names are exact lowercase.
; ===========================================================================
SHIFT_NAMES:
                defb    3
                defm    "lsl"
                defb    SHIFT_LSL
                defb    3
                defm    "lsr"
                defb    SHIFT_LSR
                defb    3
                defm    "asr"
                defb    SHIFT_ASR
                defb    3
                defm    "ror"
                defb    SHIFT_ROR
                defb    0                   ; sentinel

; ===========================================================================
; match_cond — is the identifier (HL = ptr, A = length) a condition-code name?
; (Port of matchCond, parser.go:1416, + CondCode.Name(), operands.go:128.) The
; 16 canonical names in value order are: eq(0) ne(1) cs(2) cc(3) mi(4) pl(5)
; vs(6) vc(7) hi(8) ls(9) ge(10) lt(11) gt(12) le(13) al(14) nv(15), plus two
; GNU as aliases hs(->2) and lo(->3). Values are the aarch64 condition-code
; ISA encoding.
; Entry: HL = name ptr, A = length. Exit: CY set with A = CondCode value
; (0..15) if matched; CY clear otherwise. Clobbers A/BC/DE/HL.
; ===========================================================================
match_cond:
                ld      (MC_PTR), hl
                ld      (MC_LEN), a
                ld      de, COND_NAMES
mc_loop:
                ld      a, (de)             ; name length (0 = end)
                or      a
                jr      z, mc_notfound
                ld      b, a                ; B = name length
                ld      a, (MC_LEN)
                cp      b
                jr      nz, mc_skip         ; length mismatch
                push    de                  ; save entry pointer (at length byte)
                inc     de                  ; DE -> entry name bytes
                ld      hl, (MC_PTR)        ; HL -> candidate bytes
mc_cmp:
                ld      a, (de)
                cp      (hl)
                jr      nz, mc_cmp_fail
                inc     de
                inc     hl
                djnz    mc_cmp
                ; matched: DE -> value byte
                ld      a, (de)
                pop     hl                  ; discard saved entry pointer
                scf                         ; CY set: matched, A = value
                ret
mc_cmp_fail:
                pop     de                  ; restore entry pointer (length byte)
mc_skip:
                ; advance DE past: length(1) + name[len] + value(1)
                ld      a, (de)
                add     a, 2
                add     a, e
                ld      e, a
                jr      nc, mc_skip_nc
                inc     d
mc_skip_nc:
                jr      mc_loop
mc_notfound:
                or      a                   ; clear carry: not matched
                ret

; ===========================================================================
; COND_NAMES — condition-code name → CondCode value table (port of matchCond,
; parser.go:1416 + CondCode.Name(), operands.go:128). Record: len:1 | name[len]
; | value:1; a 0-length record terminates. The 16 canonical names appear first
; in value order (values 0..15 are the aarch64 ISA encoding), followed by the
; two GNU as aliases hs(->2/CS) and lo(->3/CC). Names are exact lowercase.
; ===========================================================================
COND_NAMES:
                defb    2
                defm    "eq"
                defb    0                   ; CondEQ
                defb    2
                defm    "ne"
                defb    1                   ; CondNE
                defb    2
                defm    "cs"
                defb    2                   ; CondCS
                defb    2
                defm    "cc"
                defb    3                   ; CondCC
                defb    2
                defm    "mi"
                defb    4                   ; CondMI
                defb    2
                defm    "pl"
                defb    5                   ; CondPL
                defb    2
                defm    "vs"
                defb    6                   ; CondVS
                defb    2
                defm    "vc"
                defb    7                   ; CondVC
                defb    2
                defm    "hi"
                defb    8                   ; CondHI
                defb    2
                defm    "ls"
                defb    9                   ; CondLS
                defb    2
                defm    "ge"
                defb    10                  ; CondGE
                defb    2
                defm    "lt"
                defb    11                  ; CondLT
                defb    2
                defm    "gt"
                defb    12                  ; CondGT
                defb    2
                defm    "le"
                defb    13                  ; CondLE
                defb    2
                defm    "al"
                defb    14                  ; CondAL
                defb    2
                defm    "nv"
                defb    15                  ; CondNV
                ; GNU as aliases (parser.go:1421-1428)
                defb    2
                defm    "hs"
                defb    2                   ; alias for CondCS
                defb    2
                defm    "lo"
                defb    3                   ; alias for CondCC
                defb    0                   ; sentinel

; ===========================================================================
; parse_operand_mem — parse a memory operand starting with `[`. (Port of
; parseMem, parser.go:1278-1388.) Handles all 7 MEM shapes:
;   MemBase           [xn]
;   MemBaseOff        [xn, #off] (unsigned offset)
;   MemBaseOffPre     [xn, #off]!
;   MemBaseOffPost    [xn], #off
;   MemBaseIdx        [xn, xm/wm]
;   MemBaseIdxShifted [xn, xm, lsl #N]
;   MemBaseIdxExtended [xn, wm/xm, extend #N]
; Entry: current token is TOK_LBRACKET.
; Exit: CY clear on success (tokens consumed, operand bytes appended at
;       PI_OPSPTR); CY set on parse error (PARSE_ERR set).
; ===========================================================================
parse_operand_mem:
                ; consume '['
                call    parse_advance_tok

                ; base register must be a TOK_IDENT
                ld      hl, (PARSE_TOK)
                ld      a, (hl)
                cp      TOK_IDENT
                jp      nz, pom_err         ; not an ident after '['

                ; extract name ptr and length from the token
                inc     hl
                ld      e, (hl)
                inc     hl
                ld      d, (hl)             ; DE = span_ptr
                inc     hl
                ld      a, (hl)             ; A = span_len low
                ex      de, hl              ; HL = span_ptr
                call    match_reg           ; CY = is-reg, B = kind, C = reg
                jp      nc, pom_err         ; not a register
                ld      a, b
                cp      OP_KIND_REG_X
                jr      z, pom_base_x
                cp      OP_KIND_REG_XSP
                jr      z, pom_base_x
                jp      pom_err             ; W / WSP registers not allowed as base
pom_base_x:
                ld      a, c
                ld      (POM_BASE), a       ; save base register number
                call    parse_advance_tok   ; consume the base ident

                ; --- check for ']' (plain base or post-index) ---
                ld      hl, (PARSE_TOK)
                ld      a, (hl)
                cp      TOK_RBRACKET
                jr      nz, pom_not_rbracket

                ; consumed the ']' — check for post-index: [base], #imm
                call    parse_advance_tok   ; consume ']'
                ld      hl, (PARSE_TOK)
                ld      a, (hl)
                cp      TOK_COMMA
                jr      nz, pom_plain_base  ; no comma -> plain [base]

                ; lookahead: next token after the comma must be in
                ; {TokHash, TokInt, TokMinus, TokIdent, TokLParen}
                ld      hl, (PARSE_TOK)
                ld      de, TOK_REC_SIZE
                add     hl, de              ; HL -> token after comma
                ld      a, (hl)             ; token kind
                cp      TOK_HASH
                jr      z, pom_post_consume_comma
                cp      TOK_INT
                jr      z, pom_post_consume_comma
                cp      TOK_MINUS
                jr      z, pom_post_consume_comma
                cp      TOK_IDENT
                jr      z, pom_post_consume_comma
                cp      TOK_LPAREN
                jr      z, pom_post_consume_comma
                jr      pom_plain_base      ; lookahead not in set -> plain [base]

pom_post_consume_comma:
                call    parse_advance_tok   ; consume ','

                ; parse the post-index offset expression
                call    pom_parse_off_expr
                ret     c                   ; parse error in expression
                ; emit OP_KIND_MEM, MEM_SHAPE_BASE_OFF_POST, base, len_lo, len_hi, expr...
                ld      hl, (PI_OPSPTR)
                ld      (hl), OP_KIND_MEM
                inc     hl
                ld      (hl), MEM_SHAPE_BASE_OFF_POST
                inc     hl
                ld      a, (POM_BASE)
                ld      (hl), a
                inc     hl
                ld      (PI_OPSPTR), hl     ; update ptr past the 3-byte header
                ; write expr len (LE) and copy bytes
                call    pom_write_expr
                or      a                   ; clear carry: success
                ret

pom_plain_base:
                ; emit OP_KIND_MEM, MEM_SHAPE_BASE, base (3 bytes)
                ld      hl, (PI_OPSPTR)
                ld      (hl), OP_KIND_MEM
                inc     hl
                ld      (hl), MEM_SHAPE_BASE
                inc     hl
                ld      a, (POM_BASE)
                ld      (hl), a
                inc     hl
                ld      (PI_OPSPTR), hl
                or      a                   ; clear carry: success
                ret

                ; --- comma after base: index register or offset expression ---
pom_not_rbracket:
                cp      TOK_COMMA
                jp      nz, pom_err         ; not ']' and not ',' -> error
                call    parse_advance_tok   ; consume ','

                ; check if the next token is an ident that is an X or W register
                ld      hl, (PARSE_TOK)
                ld      a, (hl)
                cp      TOK_IDENT
                jp      nz, pom_offset      ; not an ident -> offset expression

                ; attempt to match as a register
                inc     hl
                ld      e, (hl)
                inc     hl
                ld      d, (hl)
                inc     hl
                ld      a, (hl)
                ex      de, hl
                call    match_reg
                jp      nc, pom_offset      ; not a register -> offset expression
                ld      a, b
                cp      OP_KIND_REG_X
                jr      z, pom_idx_x
                cp      OP_KIND_REG_W
                jr      z, pom_idx_w
                jp      pom_offset          ; XSP/WSP not valid index registers

pom_idx_x:
                ld      a, 1
                jr      pom_idx_common
pom_idx_w:
                ld      a, 0
pom_idx_common:
                ld      (POM_WIDTH), a      ; width: 0=W, 1=X
                ld      a, c
                ld      (POM_IDX), a        ; save index register number
                call    parse_advance_tok   ; consume the index register ident

                ; check for optional ', mod' suffix
                ld      hl, (PARSE_TOK)
                ld      a, (hl)
                cp      TOK_COMMA
                jp      nz, pom_emit_baseidx ; no modifier -> plain BaseIdx

                ; comma present: expect a modifier keyword (lsl or extend)
                call    parse_advance_tok   ; consume ','
                ld      hl, (PARSE_TOK)
                ld      a, (hl)
                cp      TOK_IDENT
                jp      nz, pom_err         ; expected ident keyword

                ; extract the keyword span
                inc     hl
                ld      e, (hl)
                inc     hl
                ld      d, (hl)             ; DE = span_ptr
                inc     hl
                ld      a, (hl)             ; A = span_len low
                ld      (POM_KWLEN), a
                ld      (POM_KWPTR), de

                ; check for "lsl"
                cp      3
                jr      nz, pom_try_extend
                ex      de, hl              ; HL = span_ptr
                ld      a, (hl)
                cp      &6C                 ; 'l'
                jr      nz, pom_try_extend
                inc     hl
                ld      a, (hl)
                cp      &73                 ; 's'
                jr      nz, pom_try_extend
                inc     hl
                ld      a, (hl)
                cp      &6C                 ; 'l'
                jr      nz, pom_try_extend

                ; matched "lsl" — consume it, then expect '#', then TOK_INT
                call    parse_advance_tok   ; consume 'lsl'
                ld      hl, (PARSE_TOK)
                ld      a, (hl)
                cp      TOK_HASH
                jp      nz, pom_err         ; expected '#'
                call    parse_advance_tok   ; consume '#'
                ld      hl, (PARSE_TOK)
                ld      a, (hl)
                cp      TOK_INT
                jp      nz, pom_err         ; expected literal int
                ; read shift amount: low byte of token value (offset 6)
                ld      hl, (PARSE_TOK)
                ld      de, 6
                add     hl, de
                ld      a, (hl)             ; A = low byte of int64 value
                ld      (POM_AMT), a
                call    parse_advance_tok   ; consume the int
                ; expect ']'
                ld      hl, (PARSE_TOK)
                ld      a, (hl)
                cp      TOK_RBRACKET
                jp      nz, pom_err
                call    parse_advance_tok   ; consume ']'
                ; emit OP_KIND_MEM, MEM_SHAPE_BASE_IDX_SHIFTED, base, idx, idxWidth, shiftAmt
                ld      hl, (PI_OPSPTR)
                ld      (hl), OP_KIND_MEM
                inc     hl
                ld      (hl), MEM_SHAPE_BASE_IDX_SHIFTED
                inc     hl
                ld      a, (POM_BASE)
                ld      (hl), a
                inc     hl
                ld      a, (POM_IDX)
                ld      (hl), a
                inc     hl
                ld      a, (POM_WIDTH)
                ld      (hl), a
                inc     hl
                ld      a, (POM_AMT)
                ld      (hl), a
                inc     hl
                ld      (PI_OPSPTR), hl
                or      a                   ; clear carry: success
                ret

pom_try_extend:
                ; not "lsl" — try matchExtend on the keyword
                ld      hl, (POM_KWPTR)
                ld      a, (POM_KWLEN)
                call    match_extend
                jp      nc, pom_err         ; unknown extend keyword
                ld      (POM_EXT), a        ; save ExtendKind value
                call    parse_advance_tok   ; consume the extend keyword
                ; optional '#N'
                xor     a
                ld      (POM_AMT), a        ; default amt = 0
                ld      hl, (PARSE_TOK)
                ld      a, (hl)
                cp      TOK_HASH
                jr      nz, pom_emit_extended ; no '#' -> amt stays 0
                call    parse_advance_tok   ; consume '#'
                ld      hl, (PARSE_TOK)
                ld      a, (hl)
                cp      TOK_INT
                jp      nz, pom_err         ; expected literal int after '#'
                ; read extend amount: low byte of token value
                ld      hl, (PARSE_TOK)
                ld      de, 6
                add     hl, de
                ld      a, (hl)
                ld      (POM_AMT), a
                call    parse_advance_tok   ; consume the int
pom_emit_extended:
                ; expect ']'
                ld      hl, (PARSE_TOK)
                ld      a, (hl)
                cp      TOK_RBRACKET
                jp      nz, pom_err
                call    parse_advance_tok   ; consume ']'
                ; emit OP_KIND_MEM, MEM_SHAPE_BASE_IDX_EXTENDED, base, idx, idxWidth, extend, shiftAmt
                ld      hl, (PI_OPSPTR)
                ld      (hl), OP_KIND_MEM
                inc     hl
                ld      (hl), MEM_SHAPE_BASE_IDX_EXTENDED
                inc     hl
                ld      a, (POM_BASE)
                ld      (hl), a
                inc     hl
                ld      a, (POM_IDX)
                ld      (hl), a
                inc     hl
                ld      a, (POM_WIDTH)
                ld      (hl), a
                inc     hl
                ld      a, (POM_EXT)
                ld      (hl), a
                inc     hl
                ld      a, (POM_AMT)
                ld      (hl), a
                inc     hl
                ld      (PI_OPSPTR), hl
                or      a                   ; clear carry: success
                ret

pom_emit_baseidx:
                ; expect ']'
                ld      hl, (PARSE_TOK)
                ld      a, (hl)
                cp      TOK_RBRACKET
                jp      nz, pom_err
                call    parse_advance_tok   ; consume ']'
                ; emit OP_KIND_MEM, MEM_SHAPE_BASE_IDX, base, idx, idxWidth
                ld      hl, (PI_OPSPTR)
                ld      (hl), OP_KIND_MEM
                inc     hl
                ld      (hl), MEM_SHAPE_BASE_IDX
                inc     hl
                ld      a, (POM_BASE)
                ld      (hl), a
                inc     hl
                ld      a, (POM_IDX)
                ld      (hl), a
                inc     hl
                ld      a, (POM_WIDTH)
                ld      (hl), a
                inc     hl
                ld      (PI_OPSPTR), hl
                or      a                   ; clear carry: success
                ret

pom_offset:
                ; general case: offset expression [base, expr] with optional '!' or post
                ; parse the offset expression
                call    pom_parse_off_expr
                ret     c                   ; parse error
                ; expect ']'
                ld      hl, (PARSE_TOK)
                ld      a, (hl)
                cp      TOK_RBRACKET
                jp      nz, pom_err
                call    parse_advance_tok   ; consume ']'
                ; check for '!' (pre-index)
                ld      hl, (PARSE_TOK)
                ld      a, (hl)
                cp      TOK_BANG
                jr      nz, pom_emit_off    ; no '!' -> plain offset
                call    parse_advance_tok   ; consume '!'
                ; emit OP_KIND_MEM, MEM_SHAPE_BASE_OFF_PRE, base, len_lo, len_hi, expr...
                ld      hl, (PI_OPSPTR)
                ld      (hl), OP_KIND_MEM
                inc     hl
                ld      (hl), MEM_SHAPE_BASE_OFF_PRE
                inc     hl
                ld      a, (POM_BASE)
                ld      (hl), a
                inc     hl
                ld      (PI_OPSPTR), hl     ; update ptr past the 3-byte header
                call    pom_write_expr
                or      a                   ; clear carry: success
                ret

pom_emit_off:
                ; emit OP_KIND_MEM, MEM_SHAPE_BASE_OFF, base, len_lo, len_hi, expr...
                ld      hl, (PI_OPSPTR)
                ld      (hl), OP_KIND_MEM
                inc     hl
                ld      (hl), MEM_SHAPE_BASE_OFF
                inc     hl
                ld      a, (POM_BASE)
                ld      (hl), a
                inc     hl
                ld      (PI_OPSPTR), hl     ; update ptr past the 3-byte header
                call    pom_write_expr
                or      a                   ; clear carry: success
                ret

pom_err:
                ld      a, 1
                ld      (PARSE_ERR), a
                scf
                ret

; pom_parse_off_expr — parse an offset expression into EXPR_BUF and fold it,
; storing the byte count in POM_EXPRLEN. Reuses the parse_operand_expr idiom:
; reset EXPR_BUF/EXPR_PTR, call parse_expr_prec (minPrec 0), call expr_fold.
pom_parse_off_expr:
                ld      hl, EXPR_BUF
                ld      (EXPR_PTR), hl      ; reset the bytecode build buffer
                xor     a                   ; minPrec = 0
                call    parse_expr_prec
                ret     c                   ; parse error -> propagate CY
                call    expr_fold
                ld      hl, (EXPR_PTR)
                ld      de, EXPR_BUF
                or      a
                sbc     hl, de
                ld      (POM_EXPRLEN), hl   ; save expr byte count (16-bit)
                or      a                   ; clear carry: success
                ret

; pom_write_expr — write expr length (LE u16) then copy the EXPR_BUF bytes to
; (PI_OPSPTR), updating PI_OPSPTR. POM_EXPRLEN must be set. Clobbers BC/DE/HL.
pom_write_expr:
                ld      hl, (PI_OPSPTR)     ; HL = write pointer (just past header)
                ; write len_lo
                ld      a, (POM_EXPRLEN)    ; low byte of expr length
                ld      (hl), a
                inc     hl
                ld      a, (POM_EXPRLEN+1)  ; high byte of expr length
                ld      (hl), a
                inc     hl
                ; copy expr bytes (BC = length)
                ld      bc, (POM_EXPRLEN)
                ld      a, b
                or      c
                jr      z, pom_we_nocopy    ; zero-length expr (defensive)
                ex      de, hl              ; DE = dest
                ld      hl, EXPR_BUF        ; HL = source
                ldir                        ; copy; DE -> end
                ld      (PI_OPSPTR), de
                ret
pom_we_nocopy:
                ld      (PI_OPSPTR), hl
                ret

; ===========================================================================
; parse_emit_inst — write one INST record from PI_MNEMID / PI_COUNT / the
; operand bytes in PARSE_OPSBUF, advancing PARSE_RECPTR and PARSE_RECN.
;
; Record framing (the record stream is self-describing so a reader can walk a
; mix of INST / COMMENT / BLANK_RUN records — see parse_emit_comment and
; parse_emit_blank_run): a leading REC_KIND_* byte tags the record, then the
; kind-specific header + payload. INST = [REC_KIND_INST | mnem_id:2 LE |
; operand_count:1 | operands_len:2 LE | operands[]].
; ===========================================================================
parse_emit_inst:
                ld      hl, (PI_OPSPTR)
                ld      de, PARSE_OPSBUF
                or      a
                sbc     hl, de              ; HL = operand byte length
                ld      b, h
                ld      c, l                ; BC = operand byte length
                ld      hl, (PARSE_RECPTR)
                ld      (hl), REC_KIND_INST ; record-kind tag
                inc     hl
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
; parse_emit_comment — write one COMMENT record for the comment token at
; (PARSE_TOK), advancing PARSE_RECPTR and PARSE_RECN. (Port of emitComment,
; parser.go:49-51; payload layout from reader.go.) The token (a
; TOK_LINECOMMENT or TOK_BLOCKCOMMENT) carries the comment body as its source
; span (span_ptr at token offset 1, span_len at offset 3); the lexer already
; stripped the `//` / `#` / `/* */` delimiters, so the span IS the body.
;
; Record framing: [REC_KIND_COMMENT | len:2 LE | placement:1 | body[]], where
; len = 1 + body_len (placement byte + body bytes). placement is PR_EMITTED:
; 0 when no statement has been emitted on this line yet (line-leading comment),
; 1 when the comment follows a statement on the same line.
; ===========================================================================
parse_emit_comment:
                ; Read the comment token's span: span_ptr (offset 1, LE) and
                ; span_len (offset 3, LE). Comment bodies fit in a u16.
                ld      hl, (PARSE_TOK)
                inc     hl
                ld      e, (hl)
                inc     hl
                ld      d, (hl)             ; DE = body source pointer
                inc     hl
                ld      c, (hl)
                inc     hl
                ld      b, (hl)             ; BC = body length (u16)
                push    de                  ; save body src ptr
                push    bc                  ; save body length
                ; Write the record header: [REC_KIND_COMMENT | len:2 | placement].
                ; len = body_len + 1 (the placement byte).
                ld      hl, (PARSE_RECPTR)
                ld      (hl), REC_KIND_COMMENT
                inc     hl
                inc     bc                  ; len = body_len + 1
                ld      (hl), c             ; len low
                inc     hl
                ld      (hl), b             ; len high
                inc     hl
                ld      a, (PR_EMITTED)     ; placement (0 line-start / 1 after stmt)
                ld      (hl), a
                inc     hl                  ; HL -> body destination
                ; Copy the body bytes (BC restored to body_len).
                ex      de, hl              ; DE = body destination
                pop     bc                  ; BC = body length
                pop     hl                  ; HL = body source ptr
                ld      a, b
                or      c
                jr      z, pec_nocopy       ; empty body (e.g. `//` with no text)
                ldir                        ; copy body; DE -> end of record
pec_nocopy:
                ld      (PARSE_RECPTR), de  ; DE = end of record
                ld      hl, (PARSE_RECN)
                inc     hl
                ld      (PARSE_RECN), hl
                ret

; ===========================================================================
; parse_emit_blank_run — write one BLANK_RUN record recording PR_BLANKS
; consecutive blank source lines, advancing PARSE_RECPTR and PARSE_RECN.
; (Port of emitBlankRun, parser.go:55-57; payload layout from reader.go.)
;
; Record framing: [REC_KIND_BLANK_RUN | len:2 LE | runLen:4 LE], len = 4.
; runLen is the u16 PR_BLANKS zero-extended to the format's uint32 (the run is
; always >= 1; the caller only emits when PR_BLANKS != 0).
; ===========================================================================
parse_emit_blank_run:
                ld      hl, (PARSE_RECPTR)
                ld      (hl), REC_KIND_BLANK_RUN
                inc     hl
                ld      (hl), 4             ; len low  (payload = 4-byte runLen)
                inc     hl
                ld      (hl), 0             ; len high
                inc     hl
                ; runLen:4 LE — PR_BLANKS (u16) in the low two bytes, zero above.
                ld      de, (PR_BLANKS)
                ld      (hl), e
                inc     hl
                ld      (hl), d
                inc     hl
                ld      (hl), 0
                inc     hl
                ld      (hl), 0
                inc     hl
                ld      (PARSE_RECPTR), hl
                ld      hl, (PARSE_RECN)
                inc     hl
                ld      (PARSE_RECN), hl
                ret

; ===========================================================================
; parse_emit_label_def — emit one LABEL_DEF record (emitLabelDef, parser.go:39).
; Entry: HL = interned symbol id (u16). The record uses the format-package
; [kind:1][len:2 LE][payload] framing with payload = id (2 bytes LE).
; ===========================================================================
parse_emit_label_def:
                push    hl                  ; save id
                ld      hl, (PARSE_RECPTR)
                ld      (hl), REC_KIND_LABEL_DEF
                inc     hl
                ld      (hl), 2             ; len low  (payload = 2-byte id)
                inc     hl
                ld      (hl), 0             ; len high
                inc     hl
                pop     de                  ; DE = id
                ld      (hl), e
                inc     hl
                ld      (hl), d
                inc     hl
                ld      (PARSE_RECPTR), hl
                ld      hl, (PARSE_RECN)
                inc     hl
                ld      (PARSE_RECN), hl
                ret

; ===========================================================================
; parse_emit_local_def — emit one LOCAL_DEF record (emitLocalDef, parser.go:44).
; Entry: A = local-label digit (1..99). Payload = digit (1 byte).
; ===========================================================================
parse_emit_local_def:
                ld      c, a                ; save digit
                ld      hl, (PARSE_RECPTR)
                ld      (hl), REC_KIND_LOCAL_DEF
                inc     hl
                ld      (hl), 1             ; len low  (payload = 1-byte digit)
                inc     hl
                ld      (hl), 0             ; len high
                inc     hl
                ld      (hl), c             ; digit
                inc     hl
                ld      (PARSE_RECPTR), hl
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
; sym_intern — intern a symbol name into the document symbol table, returning
; its first-encounter id. Port of format.SymbolTable.Intern: the id is the
; name's 0-based index in first-encounter order. The table is the SYM_NAMES
; buffer holding `len:1, name[len]` records in id order, terminated by a
; 0-length sentinel; an unseen name is appended (its id = the record count so
; far) and a fresh sentinel written after it.
;
; Entry: HL = name ptr; C = name length (B ignored — idents < 256 bytes).
; Exit:  HL = id (u16). Clobbers A/BC/DE.
; ===========================================================================
sym_intern:
                ld      (SI_PTR), hl        ; save candidate pointer
                ld      a, c
                ld      (SI_LEN), a         ; save candidate length
                ld      de, SYM_NAMES
                ld      hl, 0               ; HL = running index = candidate id
si_loop:
                ld      a, (de)             ; record length (0 = sentinel/end)
                or      a
                jr      z, si_append        ; reached the end -> not found
                ld      b, a                ; B = record length
                ld      a, (SI_LEN)
                cp      b
                jr      nz, si_next         ; length mismatch -> skip record
                push    hl                  ; save index
                push    de                  ; save record pointer (at length byte)
                inc     de                  ; DE -> record name bytes
                ld      hl, (SI_PTR)        ; HL -> candidate bytes
si_cmp:
                ld      a, (de)
                cp      (hl)
                jr      nz, si_cmp_fail
                inc     de
                inc     hl
                djnz    si_cmp
                pop     de                  ; discard saved record pointer
                pop     hl                  ; HL = index = id
                ret
si_cmp_fail:
                pop     de                  ; restore record pointer (length byte)
                pop     hl                  ; restore index
si_next:
                ld      a, (de)             ; record length
                inc     de                  ; past the length byte
                add     a, e
                ld      e, a
                jr      nc, si_next_nc
                inc     d
si_next_nc:
                inc     hl                  ; index++
                jr      si_loop
si_append:
                ; DE -> the sentinel (append point); HL = index = the new id.
                ld      a, (SI_LEN)
                ld      (de), a             ; write the record length
                inc     de                  ; DE -> name area
                push    hl                  ; save id
                ld      hl, (SI_PTR)        ; HL = candidate name (src)
                ld      a, (SI_LEN)
                ld      c, a
                ld      b, 0                ; BC = name length
                ldir                        ; copy name; DE -> just past it
                xor     a
                ld      (de), a             ; fresh 0-length sentinel
                pop     hl                  ; HL = id
                ret

; ===========================================================================
; reloc_op_lookup — map a relocation name to its ExprOp byte. (Port of
; parser.go's relocOp switch.) Linear scan of RELOC_NAMES: records are
; `len:1 | name[len] | op:1`; a 0-length record terminates the table.
;
; Entry: HL = name ptr; C = name length.
; Exit:  A = op byte on success (CY clear); CY set if name not found.
; Clobbers A/BC/DE/HL.
; ===========================================================================
reloc_op_lookup:
                ld      (ROL_PTR), hl       ; save candidate pointer
                ld      a, c
                ld      (ROL_LEN), a        ; save candidate length
                ld      de, RELOC_NAMES
rol_loop:
                ld      a, (de)             ; record length (0 = end)
                or      a
                jr      z, rol_notfound
                ld      b, a                ; B = entry name length
                ld      a, (ROL_LEN)
                cp      b
                jr      nz, rol_skip        ; length mismatch
                push    de                  ; save entry pointer (at length byte)
                inc     de                  ; DE -> entry name bytes
                ld      hl, (ROL_PTR)       ; HL -> candidate bytes
rol_cmp:
                ld      a, (de)
                cp      (hl)
                jr      nz, rol_cmp_fail
                inc     de
                inc     hl
                djnz    rol_cmp
                ; All bytes matched: DE now points at the op byte.
                ld      a, (de)             ; A = op byte
                pop     hl                  ; discard saved entry pointer
                or      a                   ; clear carry: success
                ret
rol_cmp_fail:
                pop     de                  ; restore entry pointer (length byte)
rol_skip:
                ; Advance DE past: length byte + name[len] + op byte.
                ld      a, (de)             ; entry name length
                inc     de                  ; past the length byte
                add     a, e
                ld      e, a
                jr      nc, rol_skip_nc
                inc     d
rol_skip_nc:
                inc     de                  ; past the op byte
                jp      rol_loop
rol_notfound:
                scf
                ret

; ===========================================================================
; RELOC_NAMES — reloc name → ExprOp table (port of relocOp, parser.go:1433).
; Record: len:1 | name[len] | op:1; a 0-length record terminates.
; ===========================================================================
RELOC_NAMES:
                defb    4
                defm    "lo12"
                defb    EXPR_REL_LO12
                defb    4
                defm    "hi12"
                defb    EXPR_REL_HI12
                defb    6
                defm    "abs_g0"
                defb    EXPR_REL_ABS_G0
                defb    9
                defm    "abs_g0_nc"
                defb    EXPR_REL_ABS_G0NC
                defb    6
                defm    "abs_g1"
                defb    EXPR_REL_ABS_G1
                defb    9
                defm    "abs_g1_nc"
                defb    EXPR_REL_ABS_G1NC
                defb    6
                defm    "abs_g2"
                defb    EXPR_REL_ABS_G2
                defb    9
                defm    "abs_g2_nc"
                defb    EXPR_REL_ABS_G2NC
                defb    6
                defm    "abs_g3"
                defb    EXPR_REL_ABS_G3
                defb    0                   ; sentinel

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
; DIRECTIVE_NAMES — directive name→id table (id = the entry's 0-based index).
; A faithful copy of format.DirectiveTable (directives.go:4-31, append-only). The
; id IS the index, so the order here is load-bearing; directive_lookup walks it
; linearly. Record: len:1 | name (with the leading '.') | …; 0-length ends it.
; ===========================================================================
DIRECTIVE_NAMES:
                defb 5
                defm ".text"        ; 0
                defb 5
                defm ".data"        ; 1
                defb 5
                defm ".byte"        ; 2
                defb 6
                defm ".short"       ; 3
                defb 5
                defm ".word"        ; 4
                defb 5
                defm ".quad"        ; 5
                defb 6
                defm ".ascii"       ; 6
                defb 6
                defm ".asciz"       ; 7
                defb 4
                defm ".equ"         ; 8
                defb 4
                defm ".set"         ; 9
                defb 7
                defm ".global"      ; 10
                defb 7
                defm ".balign"      ; 11
                defb 4
                defm ".org"         ; 12  (DIR_ORG)
                defb 5
                defm ".skip"        ; 13
                defb 6
                defm ".space"       ; 14
                defb 5
                defm ".inst"        ; 15
                defb 6
                defm ".align"       ; 16
                defb 6
                defm ".ltorg"       ; 17
                defb 8
                defm ".section"     ; 18  (DIR_SECTION)
                defb 5
                defm ".arch"        ; 19  (DIR_ARCH)
                defb 4
                defm ".cpu"         ; 20  (DIR_CPU)
                defb 6
                defm ".hword"       ; 21
                defb 0              ; sentinel

DIR_ORG:        equ 12
DIR_SECTION:    equ 18
DIR_ARCH:       equ 19
DIR_CPU:        equ 20

; ===========================================================================
; TOK_SPELL — fixed source spellings for punctuation/operator tokens, indexed by
; (token_kind - TOK_COMMA). (Port of tokSpelling's punctuation cases, parser.go:
; 321-374.) Each entry is [len:1 | char:1]; a 2-char operator ('<<'/'>>') stores
; len=2 and its single repeated char. len=0 = no spelling (the token kind
; contributes nothing). TOK_IDENT/TOK_INT are handled by span-copy, not here.
; ===========================================================================
; (Char codes are written as hex ASCII — pyz80 does not accept 'x' char
; literals; the trailing comment names the character.)
TOK_SPELL:
                defb 1, &2C     ; TOK_COMMA       (5)   ','
                defb 1, &23     ; TOK_HASH        (6)   '#'
                defb 1, &3A     ; TOK_COLON       (7)   ':'
                defb 1, &21     ; TOK_BANG        (8)   '!'
                defb 1, &2E     ; TOK_DOT         (9)   '.'
                defb 1, &5B     ; TOK_LBRACKET    (10)  '['
                defb 1, &5D     ; TOK_RBRACKET    (11)  ']'
                defb 1, &28     ; TOK_LPAREN      (12)  '('
                defb 1, &29     ; TOK_RPAREN      (13)  ')'
                defb 1, &2B     ; TOK_PLUS        (14)  '+'
                defb 1, &2D     ; TOK_MINUS       (15)  '-'
                defb 1, &2A     ; TOK_STAR        (16)  '*'
                defb 1, &2F     ; TOK_SLASH       (17)  '/'
                defb 1, &26     ; TOK_AMP         (18)  '&'
                defb 1, &7C     ; TOK_PIPE        (19)  '|'
                defb 1, &5E     ; TOK_CARET       (20)  '^'
                defb 1, &7E     ; TOK_TILDE       (21)  '~'
                defb 2, &3C     ; TOK_SHL         (22)  "<<"
                defb 2, &3E     ; TOK_SHR         (23)  ">>"
                defb 0, 0       ; TOK_LINECOMMENT (24)  no spelling
                defb 0, 0       ; TOK_BLOCKCOMMENT(25)  no spelling
                defb 0, 0       ; TOK_LOCALREF    (26)  no spelling
                defb 1, &3D     ; TOK_EQUALS      (27)  '='
                defb 1, &25     ; TOK_PERCENT     (28)  '%'
TOK_SPELL_COUNT: equ (TOK_PERCENT - TOK_COMMA + 1)

; ===========================================================================
; Working storage
; ===========================================================================
ML_PTR:         defs 2          ; mnemonic_lookup: saved candidate pointer
ML_LEN:         defs 1          ; mnemonic_lookup: saved candidate length
MR_PTR:         defs 2          ; match_reg: saved candidate pointer
MR_LEN:         defs 1          ; match_reg: saved candidate length
PARSE_TOK:      defs 2          ; current token-record pointer (into LEX_TOKS)
PARSE_RECPTR:   defs 2          ; current record write pointer
PARSE_RECN:     defs 2          ; records emitted so far
PR_BLANKS:      defs 2          ; parseLine: count of leading blank lines (u16)
PR_EMITTED:     defs 1          ; parseLine: emittedStatement flag (comment placement)
PI_MNEMID:      defs 2          ; parse_inst: current mnemonic ID
PI_OPSPTR:      defs 2          ; parse_inst: operand-bytes write pointer
PI_COUNT:       defs 1          ; parse_inst: operand count
IMM_VAL:        defs 8          ; scratch int64 (LE) for the shortest-PUSH_IMMn emit
MOVK_ISCONST:   defs 1          ; parse_movk: 1 if the immediate folded to a constant
MOVK_IMM:       defs 8          ; parse_movk: saved constant immediate (LE int64)
MOVK_HW:        defs 1          ; parse_movk: lsl-slot index hw (0/1/2/3)
MOVK_IMMLEN:    defs 2          ; parse_movk: saved immExpr bytecode length
MOVK_IMMEXPR:   defs 256        ; parse_movk/movl: saved immExpr bytecode (symbolic rebuild)
MOVL_RDKIND:    defs 1          ; parse_movl: destination register kind (re-emitted per expanded inst)
MOVL_RDREG:     defs 1          ; parse_movl: destination register index
MOVL_LO16:      defs 2          ; parse_movl: imm&0xffff (LE)
MOVL_HI16:      defs 2          ; parse_movl: (imm>>16)&0xffff (LE)
MOVL_HW:        defs 1          ; parse_movl: hw slot for the inst being emitted (0/1)
MOVL_TMPPTR:    defs 2          ; parse_movl: imm16 source ptr saved across pml_begin_ops
MOVL_TMPMNEM:   defs 1          ; parse_movl: mnemonic id saved across the operand build
BARRIER_OPT:    defs 1          ; parse_barrier: 1 if the arg is optional (isb), 0 if mandatory
BARRIER_CRM:    defs 1          ; parse_barrier: resolved CRm value for the emitted operand
BARRIER_SPANPTR: defs 2         ; barrier_lookup: saved token span pointer
BARRIER_SPANLEN: defs 1         ; barrier_lookup: saved token span length
DCTLBI_XTOPT:   defs 1          ; parse_dc_tlbi: 1 if the Xt operand is optional (tlbi), 0 if mandatory (dc)
LITPOOL_WIDTH:  defs 1          ; parse_ldr: pool-entry width (4 for W dest, 8 for X dest)
PD_DIRID:       defs 1          ; parse_directive: current directive id (index into DIRECTIVE_NAMES)
DL_PTR:         defs 2          ; directive_lookup: saved candidate pointer
DL_LEN:         defs 1          ; directive_lookup: saved candidate length
SPELL_PTR:      defs 2          ; parse_directive_rest: SPELL_BUF write pointer
SPELL_BUF:      defs 256        ; parse_directive_rest: concatenated rest-of-line spelling
PARSE_OPSBUF:   defs 256        ; one instruction's operand bytes (staging)
EXPR_BUF:       defs 256        ; one operand's expression bytecode (build buffer)
EXPR_PTR:       defs 2          ; expression-bytecode write pointer (into EXPR_BUF)
FOLD_CUR:       defs 2          ; expr_fold: bytecode read cursor
FOLD_SP:        defs 2          ; expr_fold: value-stack pointer (past the top cell)
FOLD_STACK:     defs 128        ; expr_fold: 16 cells of 8-byte LE int64
MUL_A:          defs 8          ; fold_do_mul: multiplicand, shifted left each step
MUL_B:          defs 8          ; fold_do_mul: multiplier, shifted right each step
MUL_R:          defs 8          ; fold_do_mul: running product (low 64 bits)
DIV_QUOT:       defs 8          ; fold_do_div: |dividend|, becomes the quotient
DIV_REM:        defs 8          ; fold_do_div: running remainder
DIV_DVSR:       defs 8          ; fold_do_div: |divisor|
DIV_C2:         defs 1          ; fold_do_div: 65th remainder bit each step
DIV_SIGN:       defs 1          ; fold_do_div: result-negative flag (sa xor sb)
PPR_SYMID:      defs 2          ; parse_expr_primary: interned symbol id (PUSH_SYM)
PPR_LOCDIG:     defs 1          ; ppr_localref: digit byte from the TOK_LOCALREF value
PPR_RELPTR:     defs 2          ; ppr_reloc: reloc name span pointer
PPR_RELLEN:     defs 1          ; ppr_reloc: reloc name span length
ROL_PTR:        defs 2          ; reloc_op_lookup: saved candidate pointer
ROL_LEN:        defs 1          ; reloc_op_lookup: saved candidate length
SI_PTR:         defs 2          ; sym_intern: saved candidate pointer
SI_LEN:         defs 1          ; sym_intern: saved candidate length
ME_PTR:         defs 2          ; match_extend: saved candidate pointer
ME_LEN:         defs 1          ; match_extend: saved candidate length
MSK_PTR:        defs 2          ; match_shift_kind: saved candidate pointer
MSK_LEN:        defs 1          ; match_shift_kind: saved candidate length
MC_PTR:         defs 2          ; match_cond: saved candidate pointer
MC_LEN:         defs 1          ; match_cond: saved candidate length
MC_COND:        defs 1          ; parse_operand_reg: matched condition-code value
POR_KIND:       defs 1          ; parse_operand_reg: saved register kind
POR_REG:        defs 1          ; parse_operand_reg: saved register number
POR_WIDTH:      defs 1          ; parse_operand_reg: 0=W, 1=X (shift/extend width)
POR_SHIFTKIND:  defs 1          ; parse_operand_reg: ShiftKind value
POR_EXTKIND:    defs 1          ; parse_operand_reg: ExtendKind value
POR_KWPTR:      defs 2          ; parse_operand_reg: keyword span pointer
POR_KWLEN:      defs 1          ; parse_operand_reg: keyword span length
POM_BASE:       defs 1          ; parse_operand_mem: base register number
POM_IDX:        defs 1          ; parse_operand_mem: index register number
POM_WIDTH:      defs 1          ; parse_operand_mem: index width (0=W, 1=X)
POM_AMT:        defs 1          ; parse_operand_mem: shift/extend amount byte
POM_EXT:        defs 1          ; parse_operand_mem: ExtendKind value
POM_KWPTR:      defs 2          ; parse_operand_mem: keyword span pointer
POM_KWLEN:      defs 1          ; parse_operand_mem: keyword span length
POM_EXPRLEN:    defs 2          ; parse_operand_mem: offset expression byte count
SYM_NAMES:      defs 2048       ; document symbol table: `len,name` records + sentinel

; ===========================================================================
; Public I/O buffers
; ===========================================================================
PARSE_ERR:      defs 1          ; non-zero after a parse error / out-of-domain line
AP_NAMEBUF:     defs 32         ; scratch the harness fills with a candidate name
PARSE_RECS:     defs 2048       ; emitted INST record stream (harness reads here)
