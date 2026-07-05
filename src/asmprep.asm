; asmprep.asm — on-SAM assembler-source preprocessor, i31b (Bricks 1 + 2a).
;
; SAM-side Z80 port of the Go authority's preprocessor:
;   tools/sam-aarch64/frontend/preprocess.go  (Preprocess / processFile /
;   processLines / evalIfCondition / tryParseSet / parseIntLiteral /
;   isBareIdent / stripTrailingComment / parseMacroHeader / collectMacroBody).
;
; i31 is the on-SAM front-end preprocessor: a text->text pass that runs in
; front of the lexer (docs/specs/on-sam-preprocessor-design.md). It consumes
; raw source text and emits expanded source text plus cpp-style
; `# <line> "<file>"` line directives. It never sees tokens, IR, or .tbn.
;
; Implemented so far:
;   - the leading `# 1 "<file>"` directive (processFile);
;   - `.set NAME, INT`  — capture literal-integer constants (truthiness only,
;     which is all the .if consumer reads), and pass the line through;
;   - `.if SYMBOL` / `.else` / `.endif` — bare-symbol conditional assembly with
;     a nesting frame stack (evalIfCondition + the processLines stack logic);
;   - `.macro NAME a, b` … `.endm` — parse the header (name + positional params)
;     and collect the body up to .endm (nested/unterminated .macro and
;     .endm-outside-.macro are errors); store the macroDef in MACRO_TAB. A
;     definition is consumed (emits nothing); an inactive-.if definition is
;     skipped but its body still consumed;
;   - plain line pass-through; the source line counter (CUR_LINE) that records
;     each macro's defline.
; Macro INVOCATION (splitMacroArgs / buildSubstituter / tryExpandMacroInvocation
; + the reentrant processLines and mid-stream `# line` emission) is Brick 2b;
; .include is Brick 3; the b8d-chain wiring + corpus gate is Brick 4.
;
; PROVENANCE: algorithmic port of preprocess.go. VERIFICATION:
; tools/netboot-oracle/z80/asmprep_test.go drives prep_run under koron-go/z80
; and byte-compares its output against frontend.Preprocess (conditional-assembly
; + macro-definition fixtures), and validates the stored macroDef records
; (name / params / body-count / defline) directly via exposed symbols.
;
; CALLING CONVENTION (flat standalone harness):
;   Entry:  prep_run, with BC = source byte length. The caller writes the raw
;           source bytes to PREP_SRC and a NUL-terminated file path to
;           PREP_PATH before the call.
;   Exit:   BC = number of expanded-output bytes written to PREP_OUT.
;           PREP_ERR is non-zero iff Preprocess would have returned an error
;           (in which case PREP_OUT is not meaningful, matching Go's nil,err).

                if defined(PREP_STANDALONE)
                org     &8000
                endif

; ---------------------------------------------------------------------------
; Character constants.
; ---------------------------------------------------------------------------
CH_TAB:           equ &09
CH_NL:            equ &0A
CH_CR:            equ &0D
CH_SPACE:         equ &20
CH_QUOTE:         equ &22   ; "
CH_HASH:          equ &23   ; #
CH_STAR:          equ &2A   ; *
CH_PLUS:          equ &2B   ; +
CH_COMMA:         equ &2C   ; ,
CH_MINUS:         equ &2D   ; -
CH_DOT:           equ &2E   ; .
CH_SLASH:         equ &2F   ; /
CH_BSLASH:        equ &5C   ; backslash

IF_MAX:           equ 64    ; max .if nesting depth (frames)

; ===========================================================================
; prep_run — preprocess PREP_SRC (BC = length) into PREP_OUT.
; ===========================================================================
prep_run:
                ld      (SRC_LEN), bc

                ; SRC_PTR = PREP_SRC; SRC_END = PREP_SRC + len.
                ld      hl, PREP_SRC
                ld      (SRC_PTR), hl
                add     hl, bc
                ld      (SRC_END), hl

                ; OUT_PTR = PREP_OUT; OUT_END = PREP_OUT + PREP_OUT_CAP.
                ld      hl, PREP_OUT
                ld      (OUT_PTR), hl
                ld      de, PREP_OUT_CAP
                add     hl, de
                ld      (OUT_END), hl

                ; Clear error, empty .set table, empty name arena.
                xor     a
                ld      (PREP_ERR), a
                ld      (SET_COUNT), a
                ld      hl, SET_NAMES
                ld      (SET_NAMES_PTR), hl

                ; Empty macro table + pools; source line counter = 1.
                xor     a
                ld      (MACRO_COUNT), a
                ld      hl, MACRO_STRPOOL
                ld      (STRPOOL_TOP), hl
                ld      hl, PARAM_IDX
                ld      (PARAM_TOP), hl
                ld      hl, BODY_IDX
                ld      (BODY_TOP), hl
                ld      hl, 1
                ld      (CUR_LINE), hl

                ; Frame stack: one active frame {active=1, taken=0, hadElse=0}.
                ld      a, 1
                ld      (IF_DEPTH), a
                ld      hl, IF_STACK
                ld      (hl), 1                 ; active
                inc     hl
                ld      (hl), 0                 ; taken
                inc     hl
                ld      (hl), 0                 ; hadElse

                ; Leading directive: # 1 "<path>"\n  (processFile).
                ld      a, CH_HASH
                call    prep_emit_byte
                ld      a, CH_SPACE
                call    prep_emit_byte
                ld      a, &31                  ; '1'; processFile always emits line 1
                call    prep_emit_byte
                ld      a, CH_SPACE
                call    prep_emit_byte
                ld      a, CH_QUOTE
                call    prep_emit_byte
                call    prep_emit_path          ; PREP_PATH bytes up to NUL
                ld      a, CH_QUOTE
                call    prep_emit_byte
                ld      a, CH_NL
                call    prep_emit_byte

; --- main line loop --------------------------------------------------------
prep_loop:
                ; Any input left?  (SRC_PTR < SRC_END)
                ld      hl, (SRC_PTR)
                ld      de, (SRC_END)
                or      a
                sbc     hl, de
                jr      c, prep_haveline        ; SRC_PTR < SRC_END
                jp      prep_done               ; no more input

prep_haveline:
                call    prep_get_line           ; sets LINE_PTR/LINE_LEN, advances SRC_PTR
                call    prep_trim_left_line     ; sets TRIM_PTR/TRIM_LEN from LINE

                ; --- switch on the trimmed line ---
                ; .if  (".if " | ".if" | ".if\t")
                ld      de, pat_dot_if
                call    prep_prefix
                jr      nc, prep_chk_else
                jp      z, prep_do_if           ; exact ".if"
                cp      CH_SPACE
                jp      z, prep_do_if
                cp      CH_TAB
                jp      z, prep_do_if
                jr      prep_chk_else           ; ".ifXXX" — not a directive

prep_chk_else:
                ; .else  (".else" | ".else ")
                ld      de, pat_dot_else
                call    prep_prefix
                jr      nc, prep_chk_endif
                jp      z, prep_do_else
                cp      CH_SPACE
                jp      z, prep_do_else
                jr      prep_chk_endif

prep_chk_endif:
                ; .endif  (".endif" | ".endif ")
                ld      de, pat_dot_endif
                call    prep_prefix
                jr      nc, prep_chk_macro
                jp      z, prep_do_endif
                cp      CH_SPACE
                jp      z, prep_do_endif
                ; fall through to macro check

prep_chk_macro:
                ; .macro  (".macro " | ".macro\t")  — NOT bare ".macro"
                ld      de, pat_dot_macro
                call    prep_prefix
                jr      nc, prep_chk_endm
                jr      z, prep_chk_endm        ; exact ".macro" — not a directive
                cp      CH_SPACE
                jp      z, prep_do_macro
                cp      CH_TAB
                jp      z, prep_do_macro
                jr      prep_chk_endm

prep_chk_endm:
                ; .endm  (".endm" | ".endm ") — error: .endm outside .macro
                ld      de, pat_dot_endm
                call    prep_prefix
                jr      nc, prep_default
                jp      z, prep_fail
                cp      CH_SPACE
                jp      z, prep_fail
                ; fall through to default

; --- default: pass-through / .set capture (only when active) ---------------
prep_default:
                call    prep_active
                jr      z, prep_after_line      ; inactive: skip the line

                call    prep_try_set            ; capture .set NAME, INT if valid
                call    prep_emit_line          ; emit the original physical line + \n
                jp      prep_after_line

; --- advance to the next line -----------------------------------------------
; Every physical line — emitted, skipped, or a directive — advances the source
; line counter exactly once (Go: srcLine = startLine + i for every line).
prep_after_line:
                ld      hl, (CUR_LINE)
                inc     hl
                ld      (CUR_LINE), hl
                jp      prep_loop

; --- .macro ----------------------------------------------------------------
; Collect the body up to .endm (always, to skip it); if the definition is
; active, parse the header and store the macroDef. defline = this line's number.
prep_do_macro:
                call    prep_active
                ld      (MAC_ACTIVE), a
                ld      hl, (CUR_LINE)
                ld      (MAC_DEFLINE), hl
                or      a
                jr      z, pdm_collect          ; inactive: header not parsed (Go)
                call    prep_parse_macro_header ; reads TRIM (the .macro line); CF=error
                jp      c, prep_fail
pdm_collect:
                call    prep_collect_macro_body ; scans body from SRC_PTR; CF=error
                jp      c, prep_fail
                ld      a, (MAC_ACTIVE)
                or      a
                jp      z, prep_after_line      ; inactive: nothing stored
                call    prep_macro_store
                jp      prep_after_line

; --- .if -------------------------------------------------------------------
prep_do_if:
                call    prep_active
                jr      nz, prep_if_active
                ; parent inactive: push {active=0, taken=1, hadElse=0}
                ld      b, 0
                ld      c, 1
                call    prep_push_frame
                jp      prep_after_line
prep_if_active:
                call    prep_eval_if            ; CF=error; else A=cond (0/1)
                jp      c, prep_fail
                ; push {active=cond, taken=cond, hadElse=0}
                ld      b, a
                ld      c, a
                call    prep_push_frame
                jp      prep_after_line

; --- .else -----------------------------------------------------------------
prep_do_else:
                ld      a, (IF_DEPTH)
                cp      2
                jp      c, prep_fail            ; .else outside .if (depth < 2)
                ; top frame
                call    prep_top_frame          ; HL -> top frame
                inc     hl
                inc     hl                      ; HL -> hadElse
                ld      a, (hl)
                or      a
                jp      nz, prep_fail           ; duplicate .else
                ld      (hl), 1                 ; hadElse = 1
                dec     hl
                dec     hl                      ; HL -> active
                ; parentActive = active over frames[0 .. depth-2]
                ld      a, (IF_DEPTH)
                dec     a
                ld      b, a                    ; test depth-1 frames
                call    prep_active_n           ; A=1 if all active
                or      a
                jr      z, prep_else_off        ; parent inactive -> top.active=0
                ; parent active: flip iff !taken
                inc     hl                      ; HL -> taken (top+1)
                ld      a, (hl)
                dec     hl                      ; HL -> active (top+0)
                or      a
                jr      nz, prep_else_off       ; taken already -> active=0
                ld      (hl), 1                 ; active = 1
                inc     hl
                ld      (hl), 1                 ; taken = 1
                jp      prep_after_line
prep_else_off:
                ld      (hl), 0                 ; active = 0 (HL -> top.active)
                jp      prep_after_line

; --- .endif ----------------------------------------------------------------
prep_do_endif:
                ld      a, (IF_DEPTH)
                cp      2
                jp      c, prep_fail            ; .endif outside .if
                dec     a
                ld      (IF_DEPTH), a
                jp      prep_after_line

; --- end of input ----------------------------------------------------------
prep_done:
                ld      a, (IF_DEPTH)
                cp      2
                jp      nc, prep_fail           ; unterminated .if (depth > 1)
                ; success: BC = output length
                ld      hl, (OUT_PTR)
                ld      de, PREP_OUT
                or      a
                sbc     hl, de
                ld      b, h
                ld      c, l
                ret

prep_fail:
                ld      a, 1
                ld      (PREP_ERR), a
                ld      bc, 0
                ret

; ===========================================================================
; prep_get_line — extract the next physical line from PREP_SRC.
;   In:  SRC_PTR (< SRC_END).
;   Out: LINE_PTR/LINE_LEN = the line (trailing '\r' of a CRLF excluded);
;        SRC_PTR advanced past the terminating '\n' (or to SRC_END).
; ===========================================================================
prep_get_line:
                ld      hl, (SRC_PTR)
                ld      (LINE_PTR), hl
                ld      de, (SRC_END)
                ld      bc, 0                   ; BC = scanned length
pgl_scan:
                ; end of buffer?
                or      a
                push    hl
                sbc     hl, de
                pop     hl
                jr      nc, pgl_eob             ; HL >= SRC_END
                ld      a, (hl)
                cp      CH_NL
                jr      z, pgl_nl
                inc     hl
                inc     bc
                jr      pgl_scan
pgl_nl:
                ; HL points at '\n'. Line = [LINE_PTR, HL); advance SRC_PTR past '\n'.
                inc     hl
                ld      (SRC_PTR), hl
                ; strip a trailing '\r' if present (CRLF normalisation).
                call    pgl_strip_cr
                ret
pgl_eob:
                ; no '\n': line runs to SRC_END. SRC_PTR = SRC_END.
                ld      (SRC_PTR), hl
                call    pgl_strip_cr
                ret

; pgl_strip_cr — BC = raw line length; if the last byte is '\r', drop it.
pgl_strip_cr:
                ld      a, b
                or      c
                jr      z, pgl_setlen           ; empty line
                ld      hl, (LINE_PTR)
                dec     bc
                add     hl, bc                  ; HL -> last byte
                inc     bc
                ld      a, (hl)
                cp      CH_CR
                jr      nz, pgl_setlen
                dec     bc                      ; exclude the '\r'
pgl_setlen:
                ld      (LINE_LEN), bc
                ret

; ===========================================================================
; prep_trim_left_line — TRIM_PTR/TRIM_LEN = LINE with leading ' '/'\t' removed.
; ===========================================================================
prep_trim_left_line:
                ld      hl, (LINE_PTR)
                ld      bc, (LINE_LEN)
ptl_loop:
                ld      a, b
                or      c
                jr      z, ptl_done
                ld      a, (hl)
                cp      CH_SPACE
                jr      z, ptl_adv
                cp      CH_TAB
                jr      nz, ptl_done
ptl_adv:
                inc     hl
                dec     bc
                jr      ptl_loop
ptl_done:
                ld      (TRIM_PTR), hl
                ld      (TRIM_LEN), bc
                ret

; ===========================================================================
; prep_prefix — does the trimmed line start with the NUL-terminated pattern DE?
;   In:  DE = pattern (NUL-terminated); reads TRIM_PTR/TRIM_LEN.
;   Out: CF=0 -> no match.
;        CF=1 -> matched; Z=1 & A=0 if the line ends exactly at the prefix,
;                else Z=0 & A = the byte following the prefix.
; ===========================================================================
prep_prefix:
                ld      hl, (TRIM_PTR)
                ld      bc, (TRIM_LEN)
ppf_loop:
                ld      a, (de)
                or      a
                jr      z, ppf_matched          ; pattern exhausted
                ; need a text byte
                ld      a, b
                or      c
                jr      z, ppf_nomatch          ; text exhausted first
                ld      a, (de)
                cp      (hl)
                jr      nz, ppf_nomatch
                inc     hl
                inc     de
                dec     bc
                jr      ppf_loop
ppf_matched:
                ld      a, b
                or      c
                jr      z, ppf_exact            ; text ends exactly at prefix
                ld      a, (hl)                 ; A = following byte (Z=0 from 'or c')
                scf
                ret
ppf_exact:
                xor     a                       ; A=0, Z=1
                scf
                ret
ppf_nomatch:
                or      a                       ; CF=0
                ret

; ===========================================================================
; prep_eval_if — evaluate ".if SYMBOL" (evalIfCondition).
;   Reads TRIM_PTR/TRIM_LEN (known to start with ".if").
;   Out: CF=1 on error; else CF=0 and A = condition (0/1).
; ===========================================================================
prep_eval_if:
                ; rest = trimmed + 3 (".if"), len - 3
                ld      hl, (TRIM_PTR)
                ld      bc, (TRIM_LEN)
                inc     hl
                inc     hl
                inc     hl
                dec     bc
                dec     bc
                dec     bc
                call    prep_trim_space         ; HL/BC = TrimSpace(rest)
                call    prep_strip_comment      ; HL=STRIP_BUF, BC=len (stripped)
                ; empty -> error (missing symbol)
                ld      a, b
                or      c
                jr      z, pei_err
                ; must be a bare identifier
                call    prep_is_bare_ident      ; CF=1 if bare ident
                jr      nc, pei_err
                ; look up truthiness (0 if absent)
                call    prep_set_lookup         ; A = truthy (0/1)
                or      a                       ; CF=0
                ret
pei_err:
                scf
                ret

; ===========================================================================
; prep_try_set — capture ".set NAME, INT" (tryParseSet) if valid.
;   Reads TRIM_PTR/TRIM_LEN.  No output regs; stores into the .set table when
;   the line is a literal-integer .set.  Never errors (a non-literal .set is
;   simply not captured; it still passes through via prep_emit_line).
; ===========================================================================
prep_try_set:
                ld      de, pat_dot_set
                call    prep_prefix
                ret     nc                      ; not ".set..."
                ret     z                       ; exactly ".set" (no args)
                cp      CH_SPACE
                jr      z, pts_ok
                cp      CH_TAB
                ret     nz                      ; ".setX" — not a .set
pts_ok:
                ; rest = trimmed + 4 (".set"), len - 4
                ld      hl, (TRIM_PTR)
                ld      bc, (TRIM_LEN)
                inc     hl
                inc     hl
                inc     hl
                inc     hl
                dec     bc
                dec     bc
                dec     bc
                dec     bc
                call    prep_trim_space
                call    prep_strip_comment      ; HL=STRIP_BUF, BC=len
                ; find first comma
                ld      (PTS_LEN), bc
                push    hl                       ; save start
                ld      d, h
                ld      e, l                     ; DE = scan ptr
                ld      a, b
                or      c
                jr      z, pts_nocomma_pop
pts_findc:
                ld      a, (de)
                cp      CH_COMMA
                jr      z, pts_gotc
                inc     de
                dec     bc
                ld      a, b
                or      c
                jr      nz, pts_findc
pts_nocomma_pop:
                pop     hl
                ret                              ; no comma -> not captured
pts_gotc:
                ; DE -> comma. name = STRIP_BUF..comma ; val = comma+1..end
                pop     hl                       ; HL = STRIP_BUF (name start)
                ; name length = DE - HL
                push    de                       ; save comma ptr
                ex      de, hl                   ; DE=name start, HL=comma
                or      a
                sbc     hl, de                   ; HL = name length
                ld      b, h
                ld      c, l                     ; BC = name length
                ex      de, hl                   ; HL = name start
                call    prep_trim_space          ; HL/BC = TrimSpace(name)
                ; save trimmed name
                ld      (PTS_NAME_PTR), hl
                ld      (PTS_NAME_LEN), bc
                ; must be a bare identifier
                call    prep_is_bare_ident
                jr      nc, pts_drop
                ; val = comma+1 .. end-of-strip
                pop     de                       ; DE = comma ptr
                inc     de                       ; DE = val start
                ; val length = (STRIP_BUF + PTS_LEN) - (comma+1)
                ld      hl, STRIP_BUF
                ld      bc, (PTS_LEN)
                add     hl, bc                   ; HL = end of stripped
                or      a
                sbc     hl, de                   ; HL = val length
                ld      b, h
                ld      c, l
                ex      de, hl                   ; HL = val start, BC = val length
                call    prep_trim_space
                call    prep_parse_int_literal   ; CF=1 if literal; A=truthy
                ret     nc                       ; non-literal -> not captured
                ; store name (PTS_NAME_PTR/LEN) with truthiness A
                call    prep_set_store
                ret
pts_drop:
                pop     de                       ; discard saved comma ptr
                ret

; ===========================================================================
; prep_trim_space — TrimSpace: strip leading and trailing ' '/'\t'.
;   In/Out: HL=ptr, BC=len.
; ===========================================================================
prep_trim_space:
                ; leading
pts_l:
                ld      a, b
                or      c
                jr      z, pts_ts_done
                ld      a, (hl)
                cp      CH_SPACE
                jr      z, pts_l_adv
                cp      CH_TAB
                jr      nz, pts_r
pts_l_adv:
                inc     hl
                dec     bc
                jr      pts_l
                ; trailing
pts_r:
                ld      a, b
                or      c
                jr      z, pts_ts_done
                push    hl
                dec     bc
                add     hl, bc                   ; HL -> last byte
                inc     bc
                ld      a, (hl)
                pop     hl
                cp      CH_SPACE
                jr      z, pts_r_dec
                cp      CH_TAB
                jr      nz, pts_ts_done
pts_r_dec:
                dec     bc
                jr      pts_r
pts_ts_done:
                ret

; ===========================================================================
; prep_strip_comment — stripTrailingComment: forward-copy HL/BC into STRIP_BUF,
; removing an unquoted "//" tail and "/* ... */" blocks, then TrimRight ' '/'\t'
; (except when a block comment is left unterminated, mirroring the Go return).
;   In:  HL=ptr, BC=len.
;   Out: HL=STRIP_BUF, BC=stripped length.
; ===========================================================================
prep_strip_comment:
                ld      (SC_IN), hl
                ld      (SC_LEN), bc
                ld      hl, STRIP_BUF
                ld      (SC_OUT), hl
                xor     a
                ld      (SC_INSTR), a           ; inStr = 0
                ld      (SC_UNCL), a            ; unclosed = 0
                ld      hl, 0
                ld      (SC_I), hl              ; i = 0
sc_loop:
                ld      hl, (SC_I)
                ld      bc, (SC_LEN)
                or      a
                sbc     hl, bc
                jp      nc, sc_end              ; i >= len
                ; c = in[i]
                call    sc_getc                 ; A = in[i]
                ; quote toggle?
                cp      CH_QUOTE
                jr      nz, sc_notquote
                ; Toggle inStr unless the preceding byte is '\'. Go tests
                ; s[i-1] on the string it is building (blocks already spliced
                ; out), so the faithful predecessor here is the LAST EMITTED
                ; byte (STRIP_BUF[SC_OUT-1]), not the raw input's in[i-1] —
                ; which differ exactly when a /* */ block was removed just
                ; before the quote.
                ld      hl, (SC_OUT)
                ld      de, STRIP_BUF
                or      a
                sbc     hl, de
                ld      a, h
                or      l
                jr      z, sc_toggle            ; nothing emitted -> toggle
                ld      hl, (SC_OUT)
                dec     hl
                ld      a, (hl)                 ; last emitted byte
                cp      CH_BSLASH
                jr      z, sc_emit_cur          ; escaped quote: not a toggle
sc_toggle:
                ld      a, (SC_INSTR)
                xor     1
                ld      (SC_INSTR), a
                jr      sc_emit_cur
sc_notquote:
                ; inStr? then just emit
                ld      a, (SC_INSTR)
                or      a
                jr      nz, sc_emit_cur
                ; c=='/' and next=='/' -> truncate
                call    sc_getc
                cp      CH_SLASH
                jr      nz, sc_emit_cur
                ; peek next
                call    sc_peeknext             ; CF=1 if a next byte exists, A=next
                jr      nc, sc_emit_cur         ; '/' is last char
                cp      CH_SLASH
                jp      z, sc_end               ; "//" -> stop (normal end)
                cp      CH_STAR
                jr      nz, sc_emit_cur
                ; "/* ... */" block: find closing "*/" from i+2
                call    sc_find_close           ; CF=1 if found; HL=index after "*/"
                jr      nc, sc_unclosed
                ld      (SC_I), hl              ; skip the block
                jp      sc_loop
sc_unclosed:
                ; no closing: emit in[i..len] verbatim, mark unclosed, stop.
                ld      a, 1
                ld      (SC_UNCL), a
sc_uncl_loop:
                ld      hl, (SC_I)
                ld      bc, (SC_LEN)
                or      a
                sbc     hl, bc
                jp      nc, sc_end
                call    sc_getc
                call    sc_putc
                ld      hl, (SC_I)
                inc     hl
                ld      (SC_I), hl
                jr      sc_uncl_loop
sc_emit_cur:
                call    sc_getc
                call    sc_putc
                ld      hl, (SC_I)
                inc     hl
                ld      (SC_I), hl
                jp      sc_loop
sc_end:
                ; TrimRight ' '/'\t' unless unclosed.
                ld      a, (SC_UNCL)
                or      a
                jr      nz, sc_ret
sc_trimr:
                ld      hl, (SC_OUT)
                ld      de, STRIP_BUF
                or      a
                sbc     hl, de                  ; HL = current out length
                ld      a, h
                or      l
                jr      z, sc_ret               ; empty
                ld      hl, (SC_OUT)
                dec     hl
                ld      a, (hl)
                cp      CH_SPACE
                jr      z, sc_trimr_dec
                cp      CH_TAB
                jr      nz, sc_ret
sc_trimr_dec:
                ld      hl, (SC_OUT)
                dec     hl
                ld      (SC_OUT), hl
                jr      sc_trimr
sc_ret:
                ld      hl, STRIP_BUF
                ld      de, (SC_OUT)
                ex      de, hl
                or      a
                sbc     hl, de                  ; HL = SC_OUT - STRIP_BUF = length
                ld      b, h
                ld      c, l
                ld      hl, STRIP_BUF
                ret

; sc_getc — A = SC_IN[SC_I].
sc_getc:
                ld      hl, (SC_IN)
                ld      bc, (SC_I)
                add     hl, bc
                ld      a, (hl)
                ret

; sc_putc — write A to *SC_OUT, SC_OUT++.
sc_putc:
                ld      hl, (SC_OUT)
                ld      (hl), a
                inc     hl
                ld      (SC_OUT), hl
                ret

; sc_peeknext — CF=1 and A=in[i+1] if i+1 < len, else CF=0.
sc_peeknext:
                ld      hl, (SC_I)
                inc     hl
                ld      bc, (SC_LEN)
                or      a
                sbc     hl, bc
                jr      nc, sc_peek_no          ; i+1 >= len
                ld      hl, (SC_IN)
                ld      bc, (SC_I)
                add     hl, bc
                inc     hl
                ld      a, (hl)
                scf
                ret
sc_peek_no:
                or      a
                ret

; sc_find_close — search for "*/" starting at index i+2.
;   Out: CF=1 and HL = index just past "*/" if found, else CF=0.
sc_find_close:
                ld      hl, (SC_I)
                inc     hl
                inc     hl                      ; j = i+2
sc_fc_loop:
                ; need j+1 < len for a "*/" pair
                push    hl
                inc     hl                      ; j+1
                ld      bc, (SC_LEN)
                or      a
                sbc     hl, bc
                pop     hl
                jr      nc, sc_fc_no            ; j+1 >= len
                ; in[j]=='*' && in[j+1]=='/'?
                push    hl
                ld      bc, (SC_IN)
                add     hl, bc                  ; &in[j]
                ld      a, (hl)
                cp      CH_STAR
                jr      nz, sc_fc_next
                inc     hl
                ld      a, (hl)
                cp      CH_SLASH
                jr      nz, sc_fc_next
                pop     hl
                inc     hl
                inc     hl                      ; index past "*/"
                scf
                ret
sc_fc_next:
                pop     hl
                inc     hl
                jr      sc_fc_loop
sc_fc_no:
                or      a
                ret

; ===========================================================================
; prep_parse_int_literal — parseIntLiteral over HL/BC.
;   In:  HL=ptr, BC=len (already TrimSpace'd by the caller).
;   Out: CF=1 if a valid literal (0x/0b/decimal, optional +/-); A=truthy (1 if
;        the value is non-zero, else 0).  CF=0 if not a literal.
;   Only truthiness is retained — the .if consumer reads nothing else; this is
;   exact for every non-overflowing literal (the whole realistic input domain).
; ===========================================================================
prep_parse_int_literal:
                ld      a, b
                or      c
                jr      z, ppi_no               ; empty
                ; optional sign
                ld      a, (hl)
                cp      CH_MINUS
                jr      z, ppi_sign
                cp      CH_PLUS
                jr      nz, ppi_base
ppi_sign:
                inc     hl
                dec     bc
                ld      a, b
                or      c
                jr      z, ppi_no               ; sign only
ppi_base:
                ld      d, 10                   ; base
                ; check 0x / 0b
                ld      a, b
                or      c
                jr      z, ppi_no
                ld      a, (hl)
                cp      &30                     ; '0'
                jr      nz, ppi_digits
                ; need a second char to be a prefix
                ld      a, b
                or      c
                dec     bc
                inc     bc
                ; peek second char (len>=2?)
                push    hl
                push    bc
                ld      a, c
                sub     2
                ld      a, b
                sbc     0
                jr      c, ppi_notprefix        ; len < 2
                inc     hl
                ld      a, (hl)
                cp      &78                     ; 'x'
                jr      z, ppi_hex
                cp      &58                     ; 'X'
                jr      z, ppi_hex
                cp      &62                     ; 'b'
                jr      z, ppi_bin
                cp      &42                     ; 'B'
                jr      z, ppi_bin
ppi_notprefix:
                pop     bc
                pop     hl
                jr      ppi_digits
ppi_hex:
                pop     bc
                pop     hl
                ld      d, 16
                inc     hl
                inc     hl
                dec     bc
                dec     bc
                jr      ppi_digits_chk
ppi_bin:
                pop     bc
                pop     hl
                ld      d, 2
                inc     hl
                inc     hl
                dec     bc
                dec     bc
ppi_digits_chk:
                ld      a, b
                or      c
                jr      z, ppi_no               ; prefix with no digits
ppi_digits:
                ; E = anyNonzero accumulator
                ld      e, 0
ppi_dloop:
                ld      a, b
                or      c
                jr      z, ppi_ok
                ld      a, (hl)
                call    prep_digit_val          ; A = digit value, CF=0 if bad char
                jr      nc, ppi_no
                ; digit >= base?  (A - D >= 0)
                cp      d
                jr      nc, ppi_no
                or      a
                jr      z, ppi_dnext            ; zero digit
                ld      e, 1                    ; a non-zero digit seen
ppi_dnext:
                inc     hl
                dec     bc
                jr      ppi_dloop
ppi_ok:
                ld      a, e                    ; truthy
                scf
                ret
ppi_no:
                or      a                       ; CF=0
                ret

; prep_digit_val — A=char; returns A=digit(0..15) with CF=1, or CF=0 if not a
; hex digit character.
prep_digit_val:
                cp      &30                     ; '0'
                jr      c, pdv_no
                cp      &39+1                   ; '9'+1
                jr      c, pdv_dec
                cp      &61                     ; 'a'
                jr      c, pdv_upper
                cp      &66+1                   ; 'f'+1
                jr      c, pdv_lower
                jr      pdv_no
pdv_upper:
                cp      &41                     ; 'A'
                jr      c, pdv_no
                cp      &46+1                   ; 'F'+1
                jr      nc, pdv_no
                sub     &41-10                  ; 'A'-10
                scf
                ret
pdv_lower:
                sub     &61-10                  ; 'a'-10
                scf
                ret
pdv_dec:
                sub     &30                     ; '0'
                scf
                ret
pdv_no:
                or      a
                ret

; ===========================================================================
; prep_is_bare_ident — isBareIdent(HL,BC): non-empty, first byte isIdentStart,
; every following byte isIdentCont.  Out: CF=1 if a bare identifier.
;   Preserves HL/BC.
; ===========================================================================
prep_is_bare_ident:
                ld      a, b
                or      c
                jr      z, pib_no               ; empty
                push    hl
                push    bc
                ld      a, (hl)
                call    prep_is_ident_start
                jr      nc, pib_no_pop
                inc     hl
                dec     bc
pib_loop:
                ld      a, b
                or      c
                jr      z, pib_yes_pop
                ld      a, (hl)
                call    prep_is_ident_cont
                jr      nc, pib_no_pop
                inc     hl
                dec     bc
                jr      pib_loop
pib_yes_pop:
                pop     bc
                pop     hl
                scf
                ret
pib_no_pop:
                pop     bc
                pop     hl
pib_no:
                or      a
                ret

; prep_is_ident_start — A=char; CF=1 iff [A-Za-z_.]. A preserved.
prep_is_ident_start:
                cp      &61                     ; 'a'
                jr      c, pis_upper
                cp      &7A+1                    ; 'z'+1
                jr      c, pis_yes
pis_upper:
                cp      &41                     ; 'A'
                jr      c, pis_punct
                cp      &5A+1                    ; 'Z'+1
                jr      c, pis_yes
pis_punct:
                cp      &5F                     ; '_'
                jr      z, pis_yes
                cp      CH_DOT
                jr      z, pis_yes
                or      a
                ret
pis_yes:
                scf
                ret

; prep_is_ident_cont — A=char; CF=1 iff ident-start or [0-9]. A preserved.
prep_is_ident_cont:
                call    prep_is_ident_start
                ret     c
                cp      &30                     ; '0'
                jr      c, pic_no
                cp      &39+1                    ; '9'+1
                jr      c, pic_yes
pic_no:
                or      a
                ret
pic_yes:
                scf
                ret

; ===========================================================================
; .set table — records {name_off:2, name_len:1, truthy:1}; names in SET_NAMES.
; ===========================================================================

; prep_set_store — store name (PTS_NAME_PTR/LEN) with truthiness A.
; Last-write-wins: overwrite an existing record with the same name, else append.
prep_set_store:
                ld      (SS_TRUTHY), a
                ; search for an existing record with this name
                call    prep_set_find           ; CF=1 & HL->record if found
                jr      nc, ss_append
                ; overwrite truthy (HL -> name_off; truthy at +3)
                inc     hl
                inc     hl
                inc     hl
                ld      a, (SS_TRUTHY)
                ld      (hl), a
                ret
ss_append:
                ld      a, (SET_COUNT)
                cp      SET_TAB_MAX
                ret     nc                      ; table full: silently drop (defensive)
                ; copy name into the arena
                ld      hl, (SET_NAMES_PTR)
                ld      (SS_NAMEOFF), hl        ; arena offset (absolute ptr)
                ld      de, (PTS_NAME_PTR)
                ld      bc, (PTS_NAME_LEN)
                ld      a, b
                or      c
                jr      z, ss_copied
ss_copy:
                ld      a, (de)
                ld      (hl), a
                inc     hl
                inc     de
                dec     bc
                ld      a, b
                or      c
                jr      nz, ss_copy
ss_copied:
                ld      (SET_NAMES_PTR), hl
                ; append record at SET_TAB + count*4
                ld      a, (SET_COUNT)
                ld      l, a
                ld      h, 0
                add     hl, hl
                add     hl, hl                  ; *4
                ld      de, SET_TAB
                add     hl, de                  ; HL -> new record
                ld      de, (SS_NAMEOFF)
                ld      (hl), e
                inc     hl
                ld      (hl), d
                inc     hl
                ld      a, (PTS_NAME_LEN)
                ld      (hl), a                 ; name_len (names are short)
                inc     hl
                ld      a, (SS_TRUTHY)
                ld      (hl), a
                ld      a, (SET_COUNT)
                inc     a
                ld      (SET_COUNT), a
                ret

; prep_set_find — find a record whose name == PTS_NAME_PTR/LEN.
;   Out: CF=1 & HL -> record if found, else CF=0.
prep_set_find:
                ld      a, (SET_COUNT)
                or      a
                jr      z, sf_no
                ld      b, a                    ; count
                ld      hl, SET_TAB
sf_loop:
                push    bc
                push    hl
                ; record: name_off:2, name_len:1
                ld      e, (hl)
                inc     hl
                ld      d, (hl)                 ; DE = name_off (abs ptr)
                inc     hl
                ld      a, (hl)                 ; A = name_len
                ; compare to PTS_NAME_LEN
                ld      c, a
                ld      a, (PTS_NAME_LEN)
                cp      c
                jr      nz, sf_next
                ; same length: compare bytes (DE vs PTS_NAME_PTR, length C)
                ld      a, c
                or      a
                jr      z, sf_hit               ; both empty
                ld      hl, (PTS_NAME_PTR)
sf_cmp:
                ld      a, (de)
                cp      (hl)
                jr      nz, sf_next
                inc     de
                inc     hl
                dec     c
                jr      nz, sf_cmp
sf_hit:
                pop     hl                      ; HL -> record
                pop     bc
                scf
                ret
sf_next:
                pop     hl
                ld      de, 4
                add     hl, de                  ; next record
                pop     bc
                djnz    sf_loop
sf_no:
                or      a
                ret

; prep_set_lookup — A = truthiness of the symbol at STRIP_BUF/BC (0 if absent).
;   In: HL=STRIP_BUF, BC=len (from prep_eval_if).
prep_set_lookup:
                ld      (PTS_NAME_PTR), hl
                ld      (PTS_NAME_LEN), bc
                call    prep_set_find
                jr      nc, sl_zero
                inc     hl
                inc     hl
                inc     hl                      ; HL -> truthy
                ld      a, (hl)
                ret
sl_zero:
                xor     a
                ret

; ===========================================================================
; frame-stack helpers
; ===========================================================================

; prep_top_frame — HL -> the top frame (IF_STACK + (IF_DEPTH-1)*3).
prep_top_frame:
                ld      a, (IF_DEPTH)
                dec     a
                ld      l, a
                ld      h, 0
                ld      d, h
                ld      e, l
                add     hl, hl                  ; *2
                add     hl, de                  ; *3
                ld      de, IF_STACK
                add     hl, de
                ret

; prep_push_frame — push {active=B, taken=C, hadElse=0}.  A/HL/DE clobbered.
prep_push_frame:
                ld      a, (IF_DEPTH)
                cp      IF_MAX
                jp      nc, prep_fail           ; nesting too deep
                ld      l, a
                ld      h, 0
                ld      d, h
                ld      e, l
                add     hl, hl
                add     hl, de                  ; depth*3
                ld      de, IF_STACK
                add     hl, de                  ; HL -> new frame slot
                ld      (hl), b                 ; active
                inc     hl
                ld      (hl), c                 ; taken
                inc     hl
                ld      (hl), 0                 ; hadElse
                ld      a, (IF_DEPTH)
                inc     a
                ld      (IF_DEPTH), a
                ret

; prep_active — A=1 (NZ) if all IF_DEPTH frames are active, else A=0 (Z).
prep_active:
                ld      a, (IF_DEPTH)
                ld      b, a
                ; fall through to prep_active_n

; prep_active_n — A=1 (NZ) if frames[0..B-1] all active, else A=0 (Z).
prep_active_n:
                ld      a, b
                or      a
                jr      z, pan_yes              ; zero frames -> vacuously active
                ld      hl, IF_STACK
pan_loop:
                ld      a, (hl)                 ; active byte
                or      a
                jr      z, pan_no
                ld      de, 3
                add     hl, de
                djnz    pan_loop
pan_yes:
                ld      a, 1
                or      a                       ; NZ
                ret
pan_no:
                xor     a                       ; Z
                ret

; ===========================================================================
; output helpers
; ===========================================================================

; prep_emit_byte — write A to *OUT_PTR (bounds-checked), OUT_PTR++.
prep_emit_byte:
                push    hl
                push    de
                ld      hl, (OUT_PTR)
                ld      de, (OUT_END)
                push    af
                or      a
                sbc     hl, de
                jr      nc, peb_over            ; OUT_PTR >= OUT_END
                pop     af
                ld      hl, (OUT_PTR)
                ld      (hl), a
                inc     hl
                ld      (OUT_PTR), hl
                pop     de
                pop     hl
                ret
peb_over:
                pop     af
                pop     de
                pop     hl
                jp      prep_fail

; prep_emit_line — emit the current physical line (LINE_PTR/LEN) then '\n'.
prep_emit_line:
                ld      bc, (LINE_LEN)
                ld      a, b
                or      c
                jr      z, pel_nl
                ld      hl, (LINE_PTR)
pel_loop:
                ld      a, (hl)
                push    hl
                push    bc
                call    prep_emit_byte
                pop     bc
                pop     hl
                inc     hl
                dec     bc
                ld      a, b
                or      c
                jr      nz, pel_loop
pel_nl:
                ld      a, CH_NL
                call    prep_emit_byte
                ret

; prep_emit_path — emit PREP_PATH bytes up to the NUL terminator.
prep_emit_path:
                ld      hl, PREP_PATH
pep_loop:
                ld      a, (hl)
                or      a
                ret     z
                push    hl
                call    prep_emit_byte
                pop     hl
                inc     hl
                jr      pep_loop

; ===========================================================================
; Macro definition — .macro/.endm (parseMacroHeader + collectMacroBody).
;
; A macroDef is stored as a MACRO_TAB record:
;   name_ptr:2 (into PREP_SRC) | name_len:1 | nparams:1 |
;   params_ptr:2 (into PARAM_IDX) | nbody:2 | body_ptr:2 (into BODY_IDX) |
;   defline:2
; PARAM_IDX entries are {ptr:2 (into MACRO_STRPOOL), len:2}; the param bytes are
; copied into MACRO_STRPOOL because they are derived through stripTrailingComment
; (a transient buffer). BODY_IDX entries are {ptr:2 (into PREP_SRC), len:2} —
; whole physical body lines, which persist in the source buffer.
; ===========================================================================

; prep_scan_ident — BC = count of leading isIdentCont bytes at HL (0..maxlen).
;   In: HL=ptr, BC=maxlen. Out: BC=count; HL preserved.
prep_scan_ident:
                push    hl
                ld      de, 0
psi_loop:
                ld      a, b
                or      c
                jr      z, psi_done
                ld      a, (hl)
                call    prep_is_ident_cont
                jr      nc, psi_done
                inc     hl
                dec     bc
                inc     de
                jr      psi_loop
psi_done:
                pop     hl
                ld      b, d
                ld      c, e
                ret

; prep_parse_macro_header — parseMacroHeader over TRIM (the ".macro …" line).
; Sets MAC_NAME_PTR/LEN, MAC_NPARAMS, MAC_PARAMS_PTR; copies param bytes into
; MACRO_STRPOOL and appends PARAM_IDX entries (both committed here — this runs
; only for an active definition, always followed by prep_macro_store).
;   Out: CF=1 on error (missing/invalid name).
prep_parse_macro_header:
                ; rest = TrimSpace(trimmed + 6 [".macro"], len - 6)
                ld      hl, (TRIM_PTR)
                ld      bc, (TRIM_LEN)
                ld      de, 6
                add     hl, de
                ld      a, c
                sub     6
                ld      c, a
                ld      a, b
                sbc     0
                ld      b, a
                call    prep_trim_space
                ; empty -> error
                ld      a, b
                or      c
                jp      z, pmh_err
                ld      (PMH_REST_PTR), hl
                ld      (PMH_REST_LEN), bc
                ; name = leading isIdentCont run
                ld      (MAC_NAME_PTR), hl
                call    prep_scan_ident          ; BC = name length
                ld      a, b
                or      c
                jp      z, pmh_err               ; no name
                ld      (MAC_NAME_LEN), bc
                ; argStr = TrimSpace(rest[namelen:])
                ld      hl, (PMH_REST_PTR)
                add     hl, bc                   ; HL = rest + namelen
                ex      de, hl                   ; DE = argStr ptr
                ld      hl, (PMH_REST_LEN)
                or      a
                sbc     hl, bc                   ; HL = restlen - namelen
                ld      b, h
                ld      c, l
                ex      de, hl                   ; HL = argStr ptr, BC = argStr len
                call    prep_trim_space
                ; drop one leading ',' then TrimSpace again
                ld      a, b
                or      c
                jr      z, pmh_split_init
                ld      a, (hl)
                cp      CH_COMMA
                jr      nz, pmh_dropcomma_done
                inc     hl
                dec     bc
                call    prep_trim_space
pmh_dropcomma_done:
pmh_split_init:
                call    prep_strip_comment       ; HL=STRIP_BUF, BC=argStr len
                ld      (PMH_STRIP_LEN), bc
                ; params start
                xor     a
                ld      (MAC_NPARAMS), a
                ld      hl, (PARAM_TOP)
                ld      (MAC_PARAMS_PTR), hl
                ; empty argStr -> 0 params
                ld      a, b
                or      c
                jr      z, pmh_ok
                ld      hl, STRIP_BUF
                ld      (PMH_P), hl
                ld      bc, (PMH_STRIP_LEN)
                ld      (PMH_R), bc
pmh_seg_loop:
                ld      hl, (PMH_P)
                ld      bc, (PMH_R)
                ld      de, 0                    ; DE = segment length
pmh_seg_scan:
                ld      a, b
                or      c
                jr      z, pmh_seg_end           ; end of buffer, no comma
                ld      a, (hl)
                cp      CH_COMMA
                jr      z, pmh_seg_comma
                inc     hl
                dec     bc
                inc     de
                jr      pmh_seg_scan
pmh_seg_comma:
                call    pmh_add_param            ; (PMH_P, DE) segment
                ; advance past comma: PMH_P += DE+1; PMH_R -= DE+1
                ld      hl, (PMH_P)
                add     hl, de
                inc     hl
                ld      (PMH_P), hl
                ld      hl, (PMH_R)
                or      a
                sbc     hl, de
                dec     hl
                ld      (PMH_R), hl
                jr      pmh_seg_loop
pmh_seg_end:
                call    pmh_add_param            ; last segment
pmh_ok:
                or      a                        ; CF=0
                ret
pmh_err:
                scf
                ret

; pmh_add_param — add the segment (PMH_P, DE=len) as a param if non-empty after
; TrimSpace. Copies bytes into MACRO_STRPOOL, appends a PARAM_IDX entry,
; increments MAC_NPARAMS. Preserves DE (the caller's segment length).
pmh_add_param:
                push    de
                ld      hl, (PMH_P)
                ld      b, d
                ld      c, e
                call    prep_trim_space          ; HL/BC = trimmed segment
                ld      a, b
                or      c
                jr      z, pap_done              ; empty -> skip
                ld      de, (STRPOOL_TOP)
                ld      (PAP_OFF), de
                ld      (PAP_LEN), bc
pap_copy:
                ld      a, (hl)
                ld      (de), a
                inc     hl
                inc     de
                dec     bc
                ld      a, b
                or      c
                jr      nz, pap_copy
                ld      (STRPOOL_TOP), de
                ; append (PAP_OFF, PAP_LEN) to PARAM_IDX
                ld      hl, (PARAM_TOP)
                ld      de, (PAP_OFF)
                ld      (hl), e
                inc     hl
                ld      (hl), d
                inc     hl
                ld      de, (PAP_LEN)
                ld      (hl), e
                inc     hl
                ld      (hl), d
                inc     hl
                ld      (PARAM_TOP), hl
                ld      a, (MAC_NPARAMS)
                inc     a
                ld      (MAC_NPARAMS), a
pap_done:
                pop     de
                ret

; prep_collect_macro_body — collectMacroBody: consume lines from SRC_PTR up to
; and including the matching .endm, recording each body line's span in BODY_IDX
; when MAC_ACTIVE. Nested .macro / unterminated .macro are errors. Each consumed
; line advances CUR_LINE.  Sets MAC_BODY_PTR (start) and MAC_NBODY.
;   Out: CF=1 on error.
prep_collect_macro_body:
                ld      hl, (BODY_TOP)
                ld      (MAC_BODY_PTR), hl
                ld      hl, 0
                ld      (MAC_NBODY), hl
pcmb_loop:
                ld      hl, (SRC_PTR)
                ld      de, (SRC_END)
                or      a
                sbc     hl, de
                jr      c, pcmb_have             ; SRC_PTR < SRC_END
                scf                              ; unterminated .macro
                ret
pcmb_have:
                call    prep_get_line
                ld      hl, (CUR_LINE)
                inc     hl
                ld      (CUR_LINE), hl
                call    prep_trim_left_line
                ; nested .macro?  (".macro " | ".macro\t")
                ld      de, pat_dot_macro
                call    prep_prefix
                jr      nc, pcmb_chkendm
                jr      z, pcmb_chkendm          ; exact ".macro" -> body line
                cp      CH_SPACE
                jr      z, pcmb_nested
                cp      CH_TAB
                jr      z, pcmb_nested
pcmb_chkendm:
                ld      de, pat_dot_endm
                call    prep_prefix
                jr      nc, pcmb_body
                jr      z, pcmb_done             ; exact ".endm"
                cp      CH_SPACE
                jr      z, pcmb_done
pcmb_body:
                ld      a, (MAC_ACTIVE)
                or      a
                jr      z, pcmb_loop             ; inactive: consume without recording
                ld      hl, (BODY_TOP)
                ld      de, (LINE_PTR)
                ld      (hl), e
                inc     hl
                ld      (hl), d
                inc     hl
                ld      de, (LINE_LEN)
                ld      (hl), e
                inc     hl
                ld      (hl), d
                inc     hl
                ld      (BODY_TOP), hl
                ld      hl, (MAC_NBODY)
                inc     hl
                ld      (MAC_NBODY), hl
                jr      pcmb_loop
pcmb_nested:
                scf
                ret
pcmb_done:
                or      a
                ret

; prep_macro_store — commit the MACRO_TAB record from MAC_* staging.
prep_macro_store:
                ld      a, (MACRO_COUNT)
                cp      MACRO_MAX
                ret     nc                       ; table full: drop (defensive)
                ; record ptr = MACRO_TAB + count*12
                ld      l, a
                ld      h, 0
                add     hl, hl                   ; *2
                add     hl, hl                   ; *4
                ld      d, h
                ld      e, l                     ; *4
                add     hl, hl                   ; *8
                add     hl, de                   ; *12
                ld      de, MACRO_TAB
                add     hl, de
                ld      de, (MAC_NAME_PTR)
                ld      (hl), e
                inc     hl
                ld      (hl), d
                inc     hl
                ld      a, (MAC_NAME_LEN)
                ld      (hl), a                  ; name_len (names are short)
                inc     hl
                ld      a, (MAC_NPARAMS)
                ld      (hl), a
                inc     hl
                ld      de, (MAC_PARAMS_PTR)
                ld      (hl), e
                inc     hl
                ld      (hl), d
                inc     hl
                ld      de, (MAC_NBODY)
                ld      (hl), e
                inc     hl
                ld      (hl), d
                inc     hl
                ld      de, (MAC_BODY_PTR)
                ld      (hl), e
                inc     hl
                ld      (hl), d
                inc     hl
                ld      de, (MAC_DEFLINE)
                ld      (hl), e
                inc     hl
                ld      (hl), d
                ld      a, (MACRO_COUNT)
                inc     a
                ld      (MACRO_COUNT), a
                ret

; ===========================================================================
; Pattern strings (NUL-terminated).
; ===========================================================================
pat_dot_if:       defm ".if"
                  defb 0
pat_dot_else:     defm ".else"
                  defb 0
pat_dot_endif:    defm ".endif"
                  defb 0
pat_dot_set:      defm ".set"
                  defb 0
pat_dot_macro:    defm ".macro"
                  defb 0
pat_dot_endm:     defm ".endm"
                  defb 0

; ===========================================================================
; Scratch variables and buffers.
; ===========================================================================
SRC_LEN:          defs 2
SRC_PTR:          defs 2
SRC_END:          defs 2
OUT_PTR:          defs 2
OUT_END:          defs 2
LINE_PTR:         defs 2
LINE_LEN:         defs 2
TRIM_PTR:         defs 2
TRIM_LEN:         defs 2
IF_DEPTH:         defs 1

SET_COUNT:        defs 1
SET_NAMES_PTR:    defs 2
SS_TRUTHY:        defs 1
SS_NAMEOFF:       defs 2
PTS_LEN:          defs 2
PTS_NAME_PTR:     defs 2
PTS_NAME_LEN:     defs 2

SC_IN:            defs 2
SC_LEN:           defs 2
SC_OUT:           defs 2
SC_I:             defs 2
SC_INSTR:         defs 1
SC_UNCL:          defs 1

; Source line counter + macro-definition staging.
CUR_LINE:         defs 2
MAC_ACTIVE:       defs 1
MAC_DEFLINE:      defs 2
MAC_NAME_PTR:     defs 2
MAC_NAME_LEN:     defs 2
MAC_NPARAMS:      defs 1
MAC_PARAMS_PTR:   defs 2
MAC_NBODY:        defs 2
MAC_BODY_PTR:     defs 2
PMH_REST_PTR:     defs 2
PMH_REST_LEN:     defs 2
PMH_STRIP_LEN:    defs 2
PMH_P:            defs 2
PMH_R:            defs 2
PAP_OFF:          defs 2
PAP_LEN:          defs 2
MACRO_COUNT:      defs 1
STRPOOL_TOP:      defs 2
PARAM_TOP:        defs 2
BODY_TOP:         defs 2

PREP_ERR:         defs 1

SET_TAB_MAX:      equ 64
SET_TAB:          defs 4*64       ; {name_off:2, name_len:1, truthy:1}
SET_NAMES:        defs 1024       ; name arena
IF_STACK:         defs 3*64       ; {active, taken, hadElse}
STRIP_BUF:        defs 512        ; stripTrailingComment scratch (>= max line)

; Macro table + pools (see the "Macro definition" section header). Sized with
; margin over the real spectrum4 corpus (~33 macro defs, short params/bodies);
; explicit overflow-to-error guards land with b2b's error plumbing.
MACRO_MAX:        equ 64
MACRO_TAB:        defs 12*64      ; {name_ptr:2,name_len:1,nparams:1,params_ptr:2,nbody:2,body_ptr:2,defline:2}
PARAM_IDX:        defs 4*256      ; {ptr:2 (into MACRO_STRPOOL), len:2}
BODY_IDX:         defs 4*1024     ; {ptr:2 (into PREP_SRC), len:2}
MACRO_STRPOOL:    defs 2048       ; copied param-name bytes

PREP_PATH:        defs 128        ; caller writes a NUL-terminated path
PREP_OUT_CAP:     equ 8192
PREP_OUT:         defs 8192       ; expanded output
PREP_SRC:         defs 4096       ; caller writes source; BC = length
