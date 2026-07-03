; asmlex.asm — aarch64 assembler-source tokenizer, i48c Bricks B1–B1d.
;
; SAM-side Z80 port of the Go authority's lexer:
;   tools/sam-aarch64/frontend/lexer.go  (Lex / lexer.next / readIdent /
;   readNumber / readLineComment / readBlockComment / readCharLit / readString).
;
; i48c is the editor input path — the SAM-side mirror of the host front-end
; (text -> symbolic overlay). asmlex is the TOKENIZER: it splits source bytes
; into typed tokens, recording each token's KIND, its source-text SPAN, the
; integer BASE, and the int64 VALUE. Brick B1 built the token extents (kind +
; span + base); B1b added the int64 value of numeric literals (parseIntInBase)
; and character literals (readCharLit); B1c added local-label refs (Nf/Nb,
; readNumberOrLocal); B1d adds string literals (readString). This completes the
; editor lexer. cpp line-directives are out of scope (a preprocessor artifact,
; like the host Preprocess pass — a line-start '#' lexes as a line comment).
; asmlex is byte-faithful to Lex on the input domain spanned by the supported
; tokens.
;
; TOKEN RECORD (LEX_TOKS, 14 bytes each, in emission order):
;   kind:1 | span_ptr:2 LE | span_len:2 LE | base:1 | value:8 LE
; span_ptr/span_len point into LEX_SRC. For TOK_IDENT the span is the whole
; identifier (leading '.' included); for a numeric TOK_INT it is the digits
; AFTER any 0x/0b prefix (matching Go's Tok.Text), base is 2/10/16, and value is
; the parsed int64; for a character-literal TOK_INT value is the char code and
; there is no span; for the two comment kinds the span is the comment body
; (matching Go's Tok.Bytes). For TOK_STRING the span points into LEX_STRPOOL and
; holds the DECODED body bytes (escapes resolved, matching Go's Tok.Bytes), not
; the raw source. For TOK_LOCALREF the value holds the label digit and base holds
; the 'f'/'b' direction char (no span). Other tokens carry a zero span/base/value.
; The trailing TOK_EOF token is emitted and ends the stream.
;
; PROVENANCE: algorithmic port of lexer.go; the flat token-array layout is
; original for the Z80 port. VERIFICATION: tools/netboot-oracle/z80/asmlex_test.go
; drives lex_run under koron-go/z80 and compares every token's kind/span/base
; against frontend.Lex on the identical source.

                if defined(ASMLEX_STANDALONE)
                org     &8000
                endif

; ---------------------------------------------------------------------------
; Token kinds — MUST match the iota order of TokKind in lexer.go. The harness
; reads these bytes back and compares them to int(Tok.Kind), so any drift is
; caught by asmlex_test.go.
; ---------------------------------------------------------------------------
TOK_EOF:          equ 0
TOK_EOL:          equ 1
TOK_IDENT:        equ 2
TOK_INT:          equ 3
TOK_STRING:       equ 4     ; span -> decoded body in LEX_STRPOOL
TOK_COMMA:        equ 5
TOK_HASH:         equ 6
TOK_COLON:        equ 7
TOK_BANG:         equ 8
TOK_DOT:          equ 9
TOK_LBRACKET:     equ 10
TOK_RBRACKET:     equ 11
TOK_LPAREN:       equ 12
TOK_RPAREN:       equ 13
TOK_PLUS:         equ 14
TOK_MINUS:        equ 15
TOK_STAR:         equ 16
TOK_SLASH:        equ 17
TOK_AMP:          equ 18
TOK_PIPE:         equ 19
TOK_CARET:        equ 20
TOK_TILDE:        equ 21
TOK_SHL:          equ 22
TOK_SHR:          equ 23
TOK_LINECOMMENT:  equ 24
TOK_BLOCKCOMMENT: equ 25
TOK_LOCALREF:     equ 26    ; digit in value, 'f'/'b' direction in base
TOK_EQUALS:       equ 27
TOK_PERCENT:      equ 28

TOK_REC_SIZE:     equ 14    ; bytes per output token record (kind/ptr/len/base/val)

; ===========================================================================
; lex_run — tokenise LEX_SRC (BC = source length) into LEX_TOKS.
;
; Entry: BC = source byte length; source bytes already written to LEX_SRC.
; Exit:  BC = token count (including the trailing TOK_EOF). LEX_ERR is non-zero
;        if a lexical error was hit (unterminated comment / lone '<' or '>' /
;        unexpected character); the stream is terminated with TOK_EOF.
; ===========================================================================
lex_run:
                ld      hl, LEX_SRC
                ld      (LEX_CUR), hl
                add     hl, bc
                ld      (LEX_END), hl       ; END = SRC + len
                ld      a, 1
                ld      (LEX_ATLINE), a     ; atLineStart = true at file start
                xor     a
                ld      (LEX_ERR), a
                ld      hl, LEX_TOKS
                ld      (LEX_TOKPTR), hl
                ld      hl, LEX_STRPOOL
                ld      (LEX_STRP), hl      ; string-pool write ptr
                ld      hl, 0
                ld      (LEX_TOKN), hl
lex_run_loop:
                call    lex_next            ; emit one token, A = its kind
                push    af
                ld      hl, (LEX_TOKN)
                inc     hl
                ld      (LEX_TOKN), hl
                ld      hl, (LEX_TOKPTR)
                ld      de, TOK_REC_SIZE
                add     hl, de
                ld      (LEX_TOKPTR), hl
                pop     af
                or      a                   ; TOK_EOF == 0?
                jr      z, lex_run_done
                cp      TOK_EOL
                jr      nz, lex_run_noteol
                ld      a, 1
                ld      (LEX_ATLINE), a
                jr      lex_run_loop
lex_run_noteol:
                xor     a
                ld      (LEX_ATLINE), a
                jr      lex_run_loop
lex_run_done:
                ld      bc, (LEX_TOKN)
                ret

; ===========================================================================
; lex_next — emit the next token at (LEX_TOKPTR); return its kind in A.
; ===========================================================================
lex_next:
                ; Skip whitespace: space, tab, CR.
lex_skip_ws:
                ld      hl, (LEX_CUR)
                ld      de, (LEX_END)
                or      a
                sbc     hl, de              ; CUR - END; carry iff CUR < END
                jr      nc, lex_eof         ; CUR >= END -> EOF
                ld      hl, (LEX_CUR)
                ld      a, (hl)
                cp      &20
                jr      z, lex_ws_adv
                cp      9
                jr      z, lex_ws_adv
                cp      13
                jr      nz, lex_dispatch
lex_ws_adv:
                call    lex_advance
                jr      lex_skip_ws

lex_eof:
                ld      a, TOK_EOF
                jp      lex_put_simple

lex_dispatch:
                ld      hl, (LEX_CUR)
                ld      a, (hl)             ; current char
                cp      10                  ; '\n'
                jr      nz, lex_d_comma
                call    lex_advance
                ld      a, TOK_EOL
                jp      lex_put_simple
lex_d_comma:
                cp      &2C                 ; ','
                jr      nz, lex_d_hash
                call    lex_advance
                ld      a, TOK_COMMA
                jp      lex_put_simple
lex_d_hash:
                cp      &23
                jr      nz, lex_d_colon
                ld      a, (LEX_ATLINE)
                or      a
                jr      z, lex_hash_mid
                jp      lex_line_comment_char ; '#' at line start: line comment
lex_hash_mid:
                call    lex_advance
                ld      a, TOK_HASH
                jp      lex_put_simple
lex_d_colon:
                cp      &3A
                jr      nz, lex_d_bang
                call    lex_advance
                ld      a, TOK_COLON
                jp      lex_put_simple
lex_d_bang:
                cp      &21
                jr      nz, lex_d_dot
                call    lex_advance
                ld      a, TOK_BANG
                jp      lex_put_simple
lex_d_dot:
                cp      &2E
                jr      nz, lex_d_lbr
                ; '.' followed by an ident-start char begins an identifier.
                ld      hl, (LEX_CUR)
                inc     hl
                ld      de, (LEX_END)
                push    hl
                or      a
                sbc     hl, de
                pop     hl
                jr      nc, lex_dot_plain   ; CUR+1 >= END
                ld      a, (hl)             ; src[CUR+1]
                call    lex_is_ident_start
                jp      c, lex_read_ident
lex_dot_plain:
                call    lex_advance
                ld      a, TOK_DOT
                jp      lex_put_simple
lex_d_lbr:
                cp      &5B
                jr      nz, lex_d_rbr
                call    lex_advance
                ld      a, TOK_LBRACKET
                jp      lex_put_simple
lex_d_rbr:
                cp      &5D
                jr      nz, lex_d_lpar
                call    lex_advance
                ld      a, TOK_RBRACKET
                jp      lex_put_simple
lex_d_lpar:
                cp      &28
                jr      nz, lex_d_rpar
                call    lex_advance
                ld      a, TOK_LPAREN
                jp      lex_put_simple
lex_d_rpar:
                cp      &29
                jr      nz, lex_d_plus
                call    lex_advance
                ld      a, TOK_RPAREN
                jp      lex_put_simple
lex_d_plus:
                cp      &2B
                jr      nz, lex_d_minus
                call    lex_advance
                ld      a, TOK_PLUS
                jp      lex_put_simple
lex_d_minus:
                cp      &2D
                jr      nz, lex_d_star
                call    lex_advance
                ld      a, TOK_MINUS
                jp      lex_put_simple
lex_d_star:
                cp      &2A
                jr      nz, lex_d_slash
                call    lex_advance
                ld      a, TOK_STAR
                jp      lex_put_simple
lex_d_slash:
                cp      &2F
                jr      nz, lex_d_amp
                ld      hl, (LEX_CUR)
                inc     hl
                ld      de, (LEX_END)
                push    hl
                or      a
                sbc     hl, de
                pop     hl
                jr      nc, lex_slash_plain ; CUR+1 >= END
                ld      a, (hl)             ; src[CUR+1]
                cp      &2F
                jp      z, lex_line_comment
                cp      &2A
                jp      z, lex_block_comment
lex_slash_plain:
                call    lex_advance
                ld      a, TOK_SLASH
                jp      lex_put_simple
lex_d_amp:
                cp      &26
                jr      nz, lex_d_pipe
                call    lex_advance
                ld      a, TOK_AMP
                jp      lex_put_simple
lex_d_pipe:
                cp      &7C
                jr      nz, lex_d_caret
                call    lex_advance
                ld      a, TOK_PIPE
                jp      lex_put_simple
lex_d_caret:
                cp      &5E
                jr      nz, lex_d_tilde
                call    lex_advance
                ld      a, TOK_CARET
                jp      lex_put_simple
lex_d_tilde:
                cp      &7E
                jr      nz, lex_d_eq
                call    lex_advance
                ld      a, TOK_TILDE
                jp      lex_put_simple
lex_d_eq:
                cp      &3D
                jr      nz, lex_d_pct
                call    lex_advance
                ld      a, TOK_EQUALS
                jp      lex_put_simple
lex_d_pct:
                cp      &25
                jr      nz, lex_d_lt
                call    lex_advance
                ld      a, TOK_PERCENT
                jp      lex_put_simple
lex_d_lt:
                cp      &3C
                jr      nz, lex_d_gt
                ld      hl, (LEX_CUR)
                inc     hl
                ld      de, (LEX_END)
                push    hl
                or      a
                sbc     hl, de
                pop     hl
                jr      nc, lex_op_err      ; CUR+1 >= END: lone '<'
                ld      a, (hl)
                cp      &3C
                jr      nz, lex_op_err
                call    lex_advance
                call    lex_advance
                ld      a, TOK_SHL
                jp      lex_put_simple
lex_d_gt:
                cp      &3E
                jr      nz, lex_d_num
                ld      hl, (LEX_CUR)
                inc     hl
                ld      de, (LEX_END)
                push    hl
                or      a
                sbc     hl, de
                pop     hl
                jr      nc, lex_op_err      ; lone '>'
                ld      a, (hl)
                cp      &3E
                jr      nz, lex_op_err
                call    lex_advance
                call    lex_advance
                ld      a, TOK_SHR
                jp      lex_put_simple
lex_d_num:
                ; Leading digit -> a number or a local-label ref (Nf/Nb).
                cp      &30
                jr      c, lex_d_identchk
                cp      &39+1
                jp      c, lex_read_number_or_local
lex_d_identchk:
                call    lex_is_ident_start
                jp      c, lex_read_ident
                cp      &27                 ; '\'' char literal
                jp      z, lex_read_char
                cp      &22                 ; '"' string literal
                jp      z, lex_read_string
                ; cpp line-directives are out of scope (a preprocessor artifact,
                ; like the host Preprocess pass); a line-start '#' is handled
                ; above as a line comment. Else error.
lex_op_err:
                ld      a, 1
                ld      (LEX_ERR), a
                ld      a, TOK_EOF
                jp      lex_put_simple

; ---------------------------------------------------------------------------
; lex_read_ident — TOK_IDENT spanning the run of ident-continuation chars from
; LEX_CUR. (Mirrors readIdent.)
; ---------------------------------------------------------------------------
lex_read_ident:
                ld      hl, (LEX_CUR)
                ld      (LEX_RP), hl        ; span start
lex_ident_loop:
                ld      hl, (LEX_CUR)
                ld      de, (LEX_END)
                or      a
                sbc     hl, de
                jr      nc, lex_ident_end   ; CUR >= END
                ld      hl, (LEX_CUR)
                ld      a, (hl)
                call    lex_is_ident_cont
                jr      nc, lex_ident_end
                call    lex_advance
                jr      lex_ident_loop
lex_ident_end:
                call    lex_span_len        ; LEX_RL = CUR - RP
                xor     a
                ld      (LEX_RB), a
                ld      a, TOK_IDENT
                ld      (LEX_RK), a
                jp      lex_put

; ---------------------------------------------------------------------------
; lex_read_number_or_local — a leading digit begins either a local-label ref
; (one/two decimal digits + 'f'/'b', not followed by an ident-cont char) or a
; numeric literal. (Port of readNumberOrLocal.) A local ref emits TOK_LOCALREF
; with the digit in LEX_RV (low byte) and the direction char in LEX_RB; any
; other shape falls through to lex_read_number.
; ---------------------------------------------------------------------------
lex_read_number_or_local:
                ; --- try a 2-digit local: need CUR+2 < END ---
                ld      hl, (LEX_CUR)
                ld      bc, 2
                add     hl, bc
                ld      de, (LEX_END)
                or      a
                sbc     hl, de              ; (CUR+2)-END; carry iff CUR+2 < END
                jr      nc, lex_trylocal1
                ld      hl, (LEX_CUR)
                ld      a, (hl)
                call    lex_is_dec_digit
                jr      nc, lex_trylocal1
                inc     hl
                ld      a, (hl)
                call    lex_is_dec_digit
                jr      nc, lex_trylocal1
                inc     hl
                ld      a, (hl)             ; src[CUR+2] = direction candidate
                call    lex_is_fb
                jr      nc, lex_trylocal1
                ; src[CUR+3] (if present) must not be an ident-cont char.
                ld      hl, (LEX_CUR)
                ld      bc, 3
                add     hl, bc
                ld      de, (LEX_END)
                or      a
                sbc     hl, de              ; (CUR+3)-END; carry iff CUR+3 < END
                jr      nc, lex_local2_ok   ; CUR+3 >= END: nothing follows
                ld      hl, (LEX_CUR)
                ld      bc, 3
                add     hl, bc
                ld      a, (hl)
                call    lex_is_ident_cont
                jr      c, lex_trylocal1    ; ident-cont follows: not a local ref
lex_local2_ok:
                ; direction = src[CUR+2].
                ld      hl, (LEX_CUR)
                ld      bc, 2
                add     hl, bc
                ld      a, (hl)
                ld      (LEX_RB), a
                ; digit = hi*10 + lo.
                ld      hl, (LEX_CUR)
                ld      a, (hl)
                sub     &30
                ld      b, a                ; hi
                add     a, a                ; *2
                add     a, a                ; *4
                add     a, b                ; *5
                add     a, a                ; *10
                ld      b, a
                inc     hl
                ld      a, (hl)
                sub     &30                 ; lo
                add     a, b                ; hi*10 + lo
                push    af
                call    lex_val_zero
                pop     af
                ld      (LEX_RV), a
                call    lex_advance
                call    lex_advance
                call    lex_advance
                jr      lex_emit_local
lex_trylocal1:
                ; --- try a 1-digit local: need CUR+1 < END ---
                ld      hl, (LEX_CUR)
                inc     hl
                ld      de, (LEX_END)
                or      a
                sbc     hl, de              ; (CUR+1)-END; carry iff CUR+1 < END
                jp      nc, lex_read_number
                ld      hl, (LEX_CUR)
                ld      a, (hl)
                call    lex_is_dec_digit
                jp      nc, lex_read_number
                inc     hl
                ld      a, (hl)             ; src[CUR+1] = direction candidate
                call    lex_is_fb
                jp      nc, lex_read_number
                ; src[CUR+2] (if present) must not be an ident-cont char.
                ld      hl, (LEX_CUR)
                ld      bc, 2
                add     hl, bc
                ld      de, (LEX_END)
                or      a
                sbc     hl, de              ; (CUR+2)-END; carry iff CUR+2 < END
                jr      nc, lex_local1_ok
                ld      hl, (LEX_CUR)
                ld      bc, 2
                add     hl, bc
                ld      a, (hl)
                call    lex_is_ident_cont
                jp      c, lex_read_number  ; ident-cont follows: not a local ref
lex_local1_ok:
                ld      hl, (LEX_CUR)
                inc     hl
                ld      a, (hl)             ; direction = src[CUR+1]
                ld      (LEX_RB), a
                ld      hl, (LEX_CUR)
                ld      a, (hl)
                sub     &30                 ; digit
                push    af
                call    lex_val_zero
                pop     af
                ld      (LEX_RV), a
                call    lex_advance
                call    lex_advance
lex_emit_local:
                ld      hl, 0
                ld      (LEX_RP), hl        ; no span
                ld      (LEX_RL), hl
                ld      a, TOK_LOCALREF
                ld      (LEX_RK), a
                jp      lex_put

; ---------------------------------------------------------------------------
; lex_is_dec_digit — A=char; carry set iff '0'-'9'. A preserved.
; lex_is_fb — A=char; carry set iff 'f' or 'b'. A preserved.
; ---------------------------------------------------------------------------
lex_is_dec_digit:
                cp      &30
                jr      c, lidd_no
                cp      &39+1
                jr      c, lidd_yes
lidd_no:
                or      a
                ret
lidd_yes:
                scf
                ret
lex_is_fb:
                cp      &66                 ; 'f'
                jr      z, lifb_yes
                cp      &62                 ; 'b'
                jr      z, lifb_yes
                or      a
                ret
lifb_yes:
                scf
                ret

; ---------------------------------------------------------------------------
; lex_read_number — TOK_INT for a base-2/10/16 literal. Records the
; prefix-stripped digit span, the base, and the parsed int64 value in LEX_RV.
; (Mirrors readNumber + parseIntInBase.)
; ---------------------------------------------------------------------------
lex_read_number:
                ld      a, 10
                ld      (LEX_RB), a         ; default base 10
                ld      hl, (LEX_CUR)
                ld      a, (hl)
                cp      &30
                jr      nz, lex_num_setup   ; not '0' -> base 10
                ; Need a char after the '0' to be a prefix.
                ld      hl, (LEX_CUR)
                inc     hl
                ld      de, (LEX_END)
                push    hl
                or      a
                sbc     hl, de
                pop     hl
                jr      nc, lex_num_setup   ; CUR+1 >= END -> plain "0"
                ld      a, (hl)             ; src[CUR+1]
                cp      &78
                jr      z, lex_num_hex
                cp      &58
                jr      z, lex_num_hex
                cp      &62
                jr      z, lex_num_bin
                cp      &42
                jr      z, lex_num_bin
                jr      lex_num_setup       ; "0" then non-prefix -> base 10
lex_num_hex:
                ld      a, 16
                ld      (LEX_RB), a
                call    lex_advance         ; past '0'
                call    lex_advance         ; past 'x'
                jr      lex_num_setup
lex_num_bin:
                ld      a, 2
                ld      (LEX_RB), a
                call    lex_advance
                call    lex_advance
lex_num_setup:
                ld      hl, (LEX_CUR)
                ld      (LEX_RP), hl        ; span start (after any prefix)
                call    lex_val_zero        ; LEX_RV = 0 (running int64 value)
lex_num_loop:
                ld      hl, (LEX_CUR)
                ld      de, (LEX_END)
                or      a
                sbc     hl, de
                jr      nc, lex_num_end     ; CUR >= END
                ld      hl, (LEX_CUR)
                ld      a, (hl)
                call    lex_is_digit_for_base ; CY iff valid; char preserved in C
                jr      nc, lex_num_end
                ; value = value*base + digit  (port of parseIntInBase).
                ld      a, c                ; the digit char
                call    lex_digit_value     ; A = 0..15
                ld      c, a                ; C = digit value
                ld      a, (LEX_RB)
                ld      b, a                ; B = base
                call    lex_val_muladd      ; LEX_RV = LEX_RV*B + C
                call    lex_advance
                jr      lex_num_loop
lex_num_end:
                call    lex_span_len        ; LEX_RL = CUR - RP
                ld      a, TOK_INT
                ld      (LEX_RK), a
                jp      lex_put

; ---------------------------------------------------------------------------
; lex_line_comment — TOK_LINECOMMENT, body from after "//" to end of line.
; lex_line_comment_char — same but the opener is a single '#'.
; ---------------------------------------------------------------------------
lex_line_comment:
                call    lex_advance         ; past first '/'
                call    lex_advance         ; past second '/'
                jr      lex_lc_body
lex_line_comment_char:
                call    lex_advance         ; past '#'
lex_lc_body:
                ld      hl, (LEX_CUR)
                ld      (LEX_RP), hl        ; body start
lex_lc_loop:
                ld      hl, (LEX_CUR)
                ld      de, (LEX_END)
                or      a
                sbc     hl, de
                jr      nc, lex_lc_end      ; CUR >= END
                ld      hl, (LEX_CUR)
                ld      a, (hl)
                cp      10                  ; '\n'
                jr      z, lex_lc_end
                call    lex_advance
                jr      lex_lc_loop
lex_lc_end:
                call    lex_span_len
                xor     a
                ld      (LEX_RB), a
                ld      a, TOK_LINECOMMENT
                ld      (LEX_RK), a
                jp      lex_put

; ---------------------------------------------------------------------------
; lex_block_comment — TOK_BLOCKCOMMENT, body between "/*" and "*/".
; ---------------------------------------------------------------------------
lex_block_comment:
                call    lex_advance         ; past '/'
                call    lex_advance         ; past '*'
                ld      hl, (LEX_CUR)
                ld      (LEX_RP), hl        ; body start
lex_bc_loop:
                ; Need CUR+1 < END (else unterminated, per Go).
                ld      hl, (LEX_CUR)
                inc     hl
                ld      de, (LEX_END)
                or      a
                sbc     hl, de
                jr      nc, lex_bc_unterm   ; CUR+1 >= END
                ld      hl, (LEX_CUR)
                ld      a, (hl)
                cp      &2A
                jr      nz, lex_bc_adv
                inc     hl
                ld      a, (hl)             ; src[CUR+1]
                cp      &2F
                jr      z, lex_bc_close
lex_bc_adv:
                call    lex_advance
                jr      lex_bc_loop
lex_bc_close:
                call    lex_span_len        ; body length (excludes "*/")
                call    lex_advance         ; past '*'
                call    lex_advance         ; past '/'
                xor     a
                ld      (LEX_RB), a
                ld      a, TOK_BLOCKCOMMENT
                ld      (LEX_RK), a
                jp      lex_put
lex_bc_unterm:
                ld      a, 1
                ld      (LEX_ERR), a
                ld      a, TOK_EOF
                jp      lex_put_simple

; ===========================================================================
; Internal helpers
; ===========================================================================

; ---------------------------------------------------------------------------
; lex_advance — CUR++ (B1 does not track line/col; positions are not compared).
; ---------------------------------------------------------------------------
lex_advance:
                ld      hl, (LEX_CUR)
                inc     hl
                ld      (LEX_CUR), hl
                ret

; ---------------------------------------------------------------------------
; lex_span_len — LEX_RL = LEX_CUR - LEX_RP (span length).
; ---------------------------------------------------------------------------
lex_span_len:
                ld      hl, (LEX_CUR)
                ld      de, (LEX_RP)
                or      a
                sbc     hl, de
                ld      (LEX_RL), hl
                ret

; ---------------------------------------------------------------------------
; lex_put — write the 14-byte record at (LEX_TOKPTR) from LEX_RK/RP/RL/RB.
; lex_put_simple — write kind A with a zero span/base.
; ---------------------------------------------------------------------------
lex_put:
                ld      hl, (LEX_TOKPTR)
                ld      a, (LEX_RK)
                ld      (hl), a
                inc     hl
                ld      a, (LEX_RP)
                ld      (hl), a
                inc     hl
                ld      a, (LEX_RP+1)
                ld      (hl), a
                inc     hl
                ld      a, (LEX_RL)
                ld      (hl), a
                inc     hl
                ld      a, (LEX_RL+1)
                ld      (hl), a
                inc     hl
                ld      a, (LEX_RB)
                ld      (hl), a
                inc     hl
                ; val (8 bytes LE) from LEX_RV
                ld      de, LEX_RV
                ld      b, 8
lex_put_val:
                ld      a, (de)
                ld      (hl), a
                inc     hl
                inc     de
                djnz    lex_put_val
                ld      a, (LEX_RK)         ; return kind in A
                ret
lex_put_simple:
                ld      hl, (LEX_TOKPTR)
                ld      (hl), a             ; kind
                push    af
                ld      b, TOK_REC_SIZE-1   ; zero the remaining record bytes
                inc     hl
lex_ps_loop:
                ld      (hl), 0
                inc     hl
                djnz    lex_ps_loop
                pop     af                  ; return kind in A
                ret

; ---------------------------------------------------------------------------
; lex_is_ident_start — A=char; carry set iff char is an ident-start
; (a-z, A-Z, '_', '.'). A is preserved.
; ---------------------------------------------------------------------------
lex_is_ident_start:
                cp      &61
                jr      c, lis_upper
                cp      &7A+1
                jr      c, lis_yes          ; a..z
lis_upper:
                cp      &41
                jr      c, lis_punct
                cp      &5A+1
                jr      c, lis_yes          ; A..Z
lis_punct:
                cp      &5F
                jr      z, lis_yes
                cp      &2E
                jr      z, lis_yes
                or      a                   ; clear carry
                ret
lis_yes:
                scf
                ret

; ---------------------------------------------------------------------------
; lex_is_ident_cont — A=char; carry set iff ident-continuation
; (ident-start or 0-9). A is preserved.
; ---------------------------------------------------------------------------
lex_is_ident_cont:
                call    lex_is_ident_start
                ret     c
                cp      &30
                jr      c, lic_no
                cp      &39+1
                jr      c, lic_yes
lic_no:
                or      a
                ret
lic_yes:
                scf
                ret

; ---------------------------------------------------------------------------
; lex_is_digit_for_base — A=char, base in LEX_RB; carry set iff char is a valid
; digit for the base. A is clobbered; the char is preserved in C.
; ---------------------------------------------------------------------------
lex_is_digit_for_base:
                ld      c, a                ; save char
                ld      a, (LEX_RB)
                cp      16
                jr      z, lidb_hex
                cp      2
                jr      z, lidb_bin
                ; base 10
                ld      a, c
                cp      &30
                jr      c, lidb_no
                cp      &39+1
                jr      c, lidb_yes
                jr      lidb_no
lidb_bin:
                ld      a, c
                cp      &30
                jr      z, lidb_yes
                cp      &31
                jr      z, lidb_yes
                jr      lidb_no
lidb_hex:
                ld      a, c
                cp      &30
                jr      c, lidb_no
                cp      &39+1
                jr      c, lidb_yes         ; 0..9
                cp      &61
                jr      c, lidb_hex_up      ; between '9'+1 and 'a' -> try A-F
                cp      &66+1
                jr      c, lidb_yes         ; a..f
                jr      lidb_no
lidb_hex_up:
                cp      &41
                jr      c, lidb_no
                cp      &46+1
                jr      c, lidb_yes         ; A..F
lidb_no:
                or      a
                ret
lidb_yes:
                scf
                ret

; ---------------------------------------------------------------------------
; lex_read_char — TOK_INT from a character literal '<c>' / '\<esc>' (port of
; readCharLit). The token carries the character's value in LEX_RV (no span).
; ---------------------------------------------------------------------------
lex_read_char:
                call    lex_advance         ; past opening '
                call    lex_at_end
                jr      c, lex_char_err     ; unterminated
                ld      hl, (LEX_CUR)
                ld      a, (hl)
                call    lex_advance         ; consume the char
                cp      &5C                 ; '\'
                jr      nz, lex_char_have
                ; escape sequence: decode the next char.
                call    lex_at_end
                jr      c, lex_char_err
                ld      hl, (LEX_CUR)
                ld      a, (hl)
                call    lex_advance
                call    lex_char_escape     ; A = byte; CY set if unknown escape
                jr      c, lex_char_err
lex_char_have:
                push    af                  ; A = character value
                call    lex_val_zero
                pop     af
                ld      (LEX_RV), a         ; value byte 0 (rest already 0)
                ; require the closing '
                call    lex_at_end
                jr      c, lex_char_err
                ld      hl, (LEX_CUR)
                ld      a, (hl)
                cp      &27                 ; '
                jr      nz, lex_char_err
                call    lex_advance         ; past closing '
                ld      hl, 0
                ld      (LEX_RP), hl        ; no span
                ld      (LEX_RL), hl
                xor     a
                ld      (LEX_RB), a
                ld      a, TOK_INT
                ld      (LEX_RK), a
                jp      lex_put
lex_char_err:
                ld      a, 1
                ld      (LEX_ERR), a
                ld      a, TOK_EOF
                jp      lex_put_simple

; ---------------------------------------------------------------------------
; lex_read_string — TOK_STRING from a "..." literal (port of readString). The
; decoded body bytes are appended to the string pool at (LEX_STRP); the token
; span points at them (ptr/len), matching Go's Tok.Bytes (the decoded bytes, not
; the raw source). Escapes: n r t \ " ' 0 and \xNN. Unterminated string, a
; truncated/bad \xNN, or an unknown escape -> LEX_ERR.
; ---------------------------------------------------------------------------
lex_read_string:
                call    lex_advance         ; past opening '"'
                ld      hl, (LEX_STRP)
                ld      (LEX_RP), hl        ; span start = pool write ptr
lex_str_loop:
                call    lex_at_end
                jr      c, lex_str_err      ; unterminated string literal
                ld      hl, (LEX_CUR)
                ld      a, (hl)
                call    lex_advance         ; consume c
                cp      &22                 ; '"' -> end of string
                jr      z, lex_str_end
                cp      &5C                 ; '\' -> escape
                jr      z, lex_str_escape
                call    lex_str_emit        ; literal byte: append A to pool
                jr      lex_str_loop
lex_str_escape:
                call    lex_at_end
                jr      c, lex_str_err      ; unterminated string escape
                ld      hl, (LEX_CUR)
                ld      a, (hl)
                call    lex_advance         ; consume escape char
                cp      &78                 ; 'x' -> \xNN hex escape
                jr      z, lex_str_hex
                call    lex_char_escape     ; n/r/t/\/'/"/0; CY set if unknown
                jr      c, lex_str_err
                call    lex_str_emit
                jr      lex_str_loop
lex_str_hex:
                ; Need two hex-digit chars present (Go: l.pos+1 >= len -> trunc;
                ; at this point CUR points at the first hex digit).
                ld      hl, (LEX_CUR)
                inc     hl
                ld      de, (LEX_END)
                or      a
                sbc     hl, de              ; (CUR+1)-END; carry iff CUR+1 < END
                jr      nc, lex_str_err     ; CUR+1 >= END: truncated \xNN
                ld      hl, (LEX_CUR)
                ld      a, (hl)
                call    lex_advance
                call    lex_hex_nibble      ; A = 0..15, CY set if not hex
                jr      c, lex_str_err
                add     a, a                ; hi nibble * 16
                add     a, a
                add     a, a
                add     a, a
                ld      c, a                ; save hi*16 (lex_hex_nibble preserves C)
                ld      hl, (LEX_CUR)
                ld      a, (hl)
                call    lex_advance
                call    lex_hex_nibble
                jr      c, lex_str_err
                add     a, c                ; hi*16 + lo
                call    lex_str_emit
                jr      lex_str_loop
lex_str_end:
                ld      hl, (LEX_STRP)
                ld      de, (LEX_RP)
                or      a
                sbc     hl, de              ; span length = STRP - RP (decoded body)
                ld      (LEX_RL), hl
                call    lex_val_zero
                xor     a
                ld      (LEX_RB), a
                ld      a, TOK_STRING
                ld      (LEX_RK), a
                jp      lex_put
lex_str_err:
                ld      a, 1
                ld      (LEX_ERR), a
                ld      a, TOK_EOF
                jp      lex_put_simple

; ---------------------------------------------------------------------------
; lex_str_emit — append the byte in A to the string pool at (LEX_STRP); STRP++.
; Preserves A.
; ---------------------------------------------------------------------------
lex_str_emit:
                push    hl
                ld      hl, (LEX_STRP)
                ld      (hl), a
                inc     hl
                ld      (LEX_STRP), hl
                pop     hl
                ret

; ---------------------------------------------------------------------------
; lex_hex_nibble — A = char; returns A = 0..15 (CY clear) for a hex digit
; (0-9/a-f/A-F), or CY set if not a hex digit. (Port of hexNibble's -1 sentinel.)
; Preserves C.
; ---------------------------------------------------------------------------
lex_hex_nibble:
                cp      &30
                jr      c, lhn_bad
                cp      &39+1
                jr      c, lhn_dec          ; '0'..'9'
                cp      &61
                jr      c, lhn_upper        ; between '9'+1 and 'a' -> try A-F
                cp      &66+1
                jr      c, lhn_lower        ; 'a'..'f'
                jr      lhn_bad
lhn_upper:
                cp      &41
                jr      c, lhn_bad
                cp      &46+1
                jr      c, lhn_upval        ; 'A'..'F'
                jr      lhn_bad
lhn_dec:
                sub     &30
                or      a                   ; clear carry
                ret
lhn_lower:
                sub     &61-10              ; 'a'..'f' -> 10..15
                or      a
                ret
lhn_upval:
                sub     &41-10              ; 'A'..'F' -> 10..15
                or      a
                ret
lhn_bad:
                scf
                ret

; ---------------------------------------------------------------------------
; lex_at_end — carry set iff LEX_CUR >= LEX_END (no more input).
; ---------------------------------------------------------------------------
lex_at_end:
                push    hl
                push    de
                ld      hl, (LEX_CUR)
                ld      de, (LEX_END)
                or      a
                sbc     hl, de              ; carry iff CUR < END
                pop     de
                pop     hl
                ccf                         ; carry iff CUR >= END
                ret

; ---------------------------------------------------------------------------
; lex_char_escape — A = escape char; returns A = decoded byte (CY clear), or
; CY set if the escape is unknown. (Port of readCharLit's escape switch.)
; ---------------------------------------------------------------------------
lex_char_escape:
                cp      &6E                 ; 'n'
                jr      nz, lce_r
                ld      a, 10
                or      a
                ret
lce_r:
                cp      &72                 ; 'r'
                jr      nz, lce_t
                ld      a, 13
                or      a
                ret
lce_t:
                cp      &74                 ; 't'
                jr      nz, lce_bs
                ld      a, 9
                or      a
                ret
lce_bs:
                cp      &5C                 ; '\'
                jr      nz, lce_sq
                ld      a, &5C
                or      a
                ret
lce_sq:
                cp      &27                 ; '
                jr      nz, lce_dq
                ld      a, &27
                or      a
                ret
lce_dq:
                cp      &22                 ; '"'
                jr      nz, lce_zero
                ld      a, &22
                or      a
                ret
lce_zero:
                cp      &30                 ; '0'
                jr      nz, lce_unknown
                xor     a                   ; NUL, carry clear
                ret
lce_unknown:
                scf
                ret

; ---------------------------------------------------------------------------
; lex_digit_value — A = digit char ('0'-'9','a'-'f','A'-'F'); returns A = 0..15.
; ---------------------------------------------------------------------------
lex_digit_value:
                cp      &39+1               ; '9'+1
                jr      c, ldv_dec
                cp      &61                 ; 'a'
                jr      c, ldv_upper
                sub     &61-10              ; 'a'..'f' -> 10..15
                ret
ldv_upper:
                sub     &41-10              ; 'A'..'F' -> 10..15
                ret
ldv_dec:
                sub     &30                 ; '0'..'9' -> 0..9
                ret

; ---------------------------------------------------------------------------
; lex_val_zero — LEX_RV = 0 (8-byte accumulator).
; ---------------------------------------------------------------------------
lex_val_zero:
                ld      hl, LEX_RV
                ld      b, 8
                xor     a
lvz_loop:
                ld      (hl), a
                inc     hl
                djnz    lvz_loop
                ret

; ---------------------------------------------------------------------------
; lex_val_muladd — LEX_RV = LEX_RV * B + C (B = base 2/10/16, C = digit value).
; Multiplies by repeated addition (base <= 16). Preserves nothing.
; ---------------------------------------------------------------------------
lex_val_muladd:
                ; LEX_VTMP = LEX_RV
                push    bc
                ld      hl, LEX_RV
                ld      de, LEX_VTMP
                ld      bc, 8
                ldir
                pop     bc
                ; LEX_RV = 0
                push    bc
                call    lex_val_zero
                pop     bc
                ; add LEX_VTMP to LEX_RV, B times.
                ld      a, b                ; A = base counter
lvm_mulloop:
                or      a
                jr      z, lvm_adddigit
                push    af
                call    lex_val_add_vtmp    ; LEX_RV += LEX_VTMP (C preserved)
                pop     af
                dec     a
                jr      lvm_mulloop
lvm_adddigit:
                call    lex_val_add_byte    ; LEX_RV += C
                ret

; ---------------------------------------------------------------------------
; lex_val_add_vtmp — LEX_RV += LEX_VTMP (8-byte LE add). Preserves C.
; ---------------------------------------------------------------------------
lex_val_add_vtmp:
                ld      hl, LEX_RV
                ld      de, LEX_VTMP
                or      a                   ; clear carry
                ld      b, 8
lvav_loop:
                ld      a, (de)
                adc     a, (hl)
                ld      (hl), a
                inc     hl
                inc     de
                djnz    lvav_loop
                ret

; ---------------------------------------------------------------------------
; lex_val_add_byte — LEX_RV += C (carry propagated through 8 bytes).
; ---------------------------------------------------------------------------
lex_val_add_byte:
                ld      hl, LEX_RV
                ld      a, c
                add     a, (hl)
                ld      (hl), a
                ret     nc
                ld      b, 7
lvab_prop:
                inc     hl
                inc     (hl)
                ret     nz
                djnz    lvab_prop
                ret

; ===========================================================================
; Working storage
; ===========================================================================
LEX_CUR:        defs 2          ; current source pointer
LEX_END:        defs 2          ; one past last source byte
LEX_ATLINE:     defs 1          ; atLineStart flag
LEX_TOKPTR:     defs 2          ; current output record pointer
LEX_TOKN:       defs 2          ; tokens emitted so far
LEX_RK:         defs 1          ; staging: token kind
LEX_RP:         defs 2          ; staging: span pointer
LEX_RL:         defs 2          ; staging: span length
LEX_RB:         defs 1          ; staging: base (TOK_INT) else 0
LEX_RV:         defs 8          ; staging: int64 value (TOK_INT) else 0
LEX_VTMP:       defs 8          ; scratch for the multiply-by-base step
LEX_STRP:       defs 2          ; string-pool write pointer (TOK_STRING bodies)

; ===========================================================================
; Public I/O buffers
; ===========================================================================
LEX_ERR:        defs 1          ; non-zero after a lexical error
                if defined(ASMPARSE_CORPUS_BUFS)
; Corpus build: LEX_TOKS relocated to low RAM (free in the flat harness:
; &0100-&63FA unused — harness.go:842-845 reserves only SP=&6FFE/haltTrap=&7000);
; LEX_SRC and LEX_STRPOOL grown to hold the longest corpus source.
LEX_SRC:        defs 4096       ; input source bytes (corpus: 4096 B cap)
LEX_TOKS:       equ  &0100      ; output token records (14 B each -> 1024 tokens, 14336 B, ends &3900)
LEX_STRPOOL:    defs 4096       ; decoded TOK_STRING bodies (corpus: 4096 B cap)
                else
                if defined(ASMPARSE_PAGED_BUFS)
; Paged build (i48c-b8i/b8d): all large buffers as equates placed in the
; two-page window mapped by LMPR=&28 (section A=page8, section B=page9).
; Page-8 window (&0000-&3FFF): PARSE_RECS at &0000, LEX_SRC at &0800, SYM_NAMES at &1000.
; Page-9 window (&4000-&7FFF): code+defs at &4000, LEX_TOKS at &6700, LEX_STRPOOL at &7500.
; Code (excl. large buffers) ends ~&611E; scratch-variable defs follow to AP_NAMEBUF end ~&665F.
; LEX_TOKS starts at &6700 (above &665F) so token writes never collide with the defs region.
; 256-token LEX_TOKS cap (x14 B = 3584 B, ends &7500) and 1024-byte LEX_STRPOOL
; (ends &7900) are sized for the b8d brick-1 single-fixture smoke test; final
; corpus-scale sizing lands in brick 2.
LEX_SRC:        equ  &0800      ; page-8 window; 2048 B cap (ends &1000)
LEX_TOKS:       equ  &6700      ; page-9 window, above defs end ~&665F; 3584 B (256 tokens, ends &7500)
LEX_STRPOOL:    equ  &7500      ; page-9 window; 1024 B cap (ends &7900)
                else
LEX_SRC:        defs 2048       ; input source bytes (caller writes; BC = len)
LEX_TOKS:       defs 3584       ; output token records (14 B each -> 256 tokens)
LEX_STRPOOL:    defs 2048       ; decoded TOK_STRING bodies (span ptr/len index here)
                endif
                endif
