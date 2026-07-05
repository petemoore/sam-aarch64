; asmprep.asm — on-SAM assembler-source preprocessor, i31b (Bricks 1 + 2a + 2b).
;
; SAM-side Z80 port of the Go authority's preprocessor:
;   tools/sam-aarch64/frontend/preprocess.go  (Preprocess / processFile /
;   processLines / evalIfCondition / tryParseSet / parseIntLiteral /
;   isBareIdent / stripTrailingComment / parseMacroHeader / collectMacroBody /
;   splitMacroArgs / buildSubstituter / tryExpandMacroInvocation).
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
;   - macro INVOCATION — a known macro name at line start expands: arg-split
;     (paren/string-aware splitMacroArgs), arity check, recursion-cycle guard,
;     \param substitution (longest-match, token-paste, inside-string, \\ literal
;     — buildSubstituter), then the substituted body is RE-preprocessed by a
;     reentrant prep_process_lines, with mid-stream `# <line> "<file>"`
;     directives at the macro-def and post-call boundaries. Macro-table lookup
;     is last-wins (newest record) so a redefined macro shadows the older one,
;     matching Go's map-overwrite semantics;
;   - plain line pass-through; the source line counter (CUR_LINE) that records
;     each macro's defline.
; .include is Brick 3; the b8d-chain wiring + corpus gate is Brick 4.
;
; ERROR MODEL: prep_run saves SP on entry; prep_fail restores it (a longjmp)
; and returns to prep_run's caller. So `jp prep_fail` is correct from any call
; depth — including inside the reentrant expansion recursion — which is why the
; pool-overflow guards (MACRO_TAB / PARAM_IDX / BODY_IDX / MACRO_STRPOOL /
; SET_TAB / SET_NAMES / ARG_IDX / SUBST_ARENA) jump straight to it rather than
; threading a carry flag up every frame. Overflow is an explicit error, never
; silent corruption (spec §3).
;
; PROVENANCE: algorithmic port of preprocess.go. VERIFICATION:
; tools/netboot-oracle/z80/asmprep_test.go drives prep_run under koron-go/z80
; and byte-compares its output against frontend.Preprocess (conditional-assembly,
; macro-definition, and macro-invocation/expansion fixtures), and validates the
; stored macroDef records (name / params / body-count / defline) directly via
; exposed symbols.
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
                else
; Paged SAM build (i370): code + small buffers in section B (page 9), org &4000;
; the big buffers relocate to the section-A window (page 8) — see the buffer
; block below. Mirrors ASMPARSE_PAGED_BUFS.
                if defined(ASMPREP_PAGED_BUFS)
                org     &4000
                endif
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
                ld      (PREP_SP_SAVE), sp      ; longjmp target for prep_fail
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

                ; Substitution arena (bump stack) and per-macro expanding flags.
                ld      hl, SUBST_ARENA
                ld      (SUBST_TOP), hl
                call    prep_clear_expanding

                ; Path arena; CURRENT_FILE = a persistent copy of PREP_PATH
                ; (the top-level file). Include recursion tracks the active file
                ; here so directives name the right file.
                ld      hl, PATH_ARENA
                ld      (PATH_TOP), hl
                call    prep_intern_path_prep   ; copy PREP_PATH -> CURRENT_FILE

                ; Reader vector: default to the memory-backed provider if the
                ; caller left it unset (the SAM build points it at the real
                ; UIFA+HGTHD+HLOAD reader before prep_run — no conditional asm).
                ld      hl, (READER_VEC)
                ld      a, h
                or      l
                jr      nz, prep_reader_set
                ld      hl, prep_mem_reader
                ld      (READER_VEC), hl
prep_reader_set:

                ; Frame stack: one active frame {active=1, taken=0, hadElse=0};
                ; base = 0 (the top-level stack starts at IF_STACK[0]).
                xor     a
                ld      (IF_BASE), a
                ld      a, 1
                ld      (IF_DEPTH), a
                ld      hl, IF_STACK
                ld      (hl), 1                 ; active
                inc     hl
                ld      (hl), 0                 ; taken
                inc     hl
                ld      (hl), 0                 ; hadElse

                ; Leading directive: # 1 "<current file>"\n  (processFile).
                ld      hl, 1
                call    prep_emit_line_directive

                ; Preprocess the top-level source window.
                call    prep_process_lines

                ; Success: BC = number of expanded-output bytes.
                ld      hl, (OUT_PTR)
                ld      de, PREP_OUT
                or      a
                sbc     hl, de
                ld      b, h
                ld      c, l
                ret

; ===========================================================================
; prep_process_lines — port of processLines: process every physical line in the
; current SRC window ([SRC_PTR, SRC_END)) with the conditional-assembly frame
; stack based at IF_BASE (frames [IF_BASE, IF_DEPTH)). The caller sets up the
; window, CUR_LINE, IF_BASE and the initial frame. Returns when the window is
; exhausted; a fatal error longjmps via prep_fail. Reentrant: macro expansion
; re-enters it over the substituted body (see prep_try_expand).
; ===========================================================================
; --- main line loop --------------------------------------------------------
prep_process_lines:
prep_loop:
                ; Any input left?  (SRC_PTR < SRC_END)
                ld      hl, (SRC_PTR)
                ld      de, (SRC_END)
                or      a
                sbc     hl, de
                jr      c, prep_haveline        ; SRC_PTR < SRC_END
                ; Window exhausted: the current stack must be balanced —
                ; (IF_DEPTH - IF_BASE) > 1 is an unterminated .if.
                ld      a, (IF_DEPTH)
                ld      hl, IF_BASE
                sub     (hl)
                cp      2
                jp      nc, prep_fail           ; depth-base > 1 -> unterminated .if
                ret                             ; window done -> return to caller

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

; --- default: pass-through / .set capture / macro invocation (only active) --
prep_default:
                call    prep_active
                jp      z, prep_after_line      ; inactive: skip the line

                call    prep_try_set            ; capture .set NAME, INT if valid
                call    prep_try_include        ; A=1 if the line was an .include
                or      a
                jp      nz, prep_after_line     ; included: don't emit the .include line
                call    prep_try_expand         ; A=1 if consumed as a macro call
                or      a
                jp      nz, prep_after_line     ; expanded: don't emit the call line
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
                call    prep_stack_size         ; A = IF_DEPTH - IF_BASE
                cp      2
                jp      c, prep_fail            ; .else outside .if (size < 2)
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
                push    hl                      ; save top.active ptr
                ; parentActive = active over frames[IF_BASE .. depth-2]
                call    prep_stack_size
                dec     a                       ; count = size-1
                ld      b, a
                call    prep_ifbase_ptr         ; HL -> IF_STACK + IF_BASE*3
                call    prep_active_from        ; A=1 if all B frames active
                pop     hl                      ; HL -> top.active
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
                call    prep_stack_size         ; A = IF_DEPTH - IF_BASE
                cp      2
                jp      c, prep_fail            ; .endif outside .if
                ld      a, (IF_DEPTH)
                dec     a
                ld      (IF_DEPTH), a
                jp      prep_after_line

; --- fatal error (longjmp) -------------------------------------------------
; Restore SP to prep_run's entry frame and return BC=0 with PREP_ERR set. The
; SP restore unwinds any nested prep_process_lines / prep_try_expand calls, so
; `jp prep_fail` is a correct abort from any depth (matching Go's nil,err).
prep_fail:
                ld      a, 1
                ld      (PREP_ERR), a
                ld      sp, (PREP_SP_SAVE)
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
                jp      nc, prep_fail           ; .set table overflow -> error
                ; copy name into the arena (bounds-checked)
                ld      hl, (SET_NAMES_PTR)
                ld      (SS_NAMEOFF), hl        ; arena offset (absolute ptr)
                ; overflow guard: SET_NAMES_PTR + len > SET_NAMES_END -> error
                ld      bc, (PTS_NAME_LEN)
                add     hl, bc                  ; HL = end of copy
                ex      de, hl                  ; DE = end of copy
                ld      hl, SET_NAMES_END
                or      a
                sbc     hl, de                  ; SET_NAMES_END - end; <0 -> overflow
                jp      c, prep_fail
                ld      hl, (SET_NAMES_PTR)
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

; prep_stack_size — A = IF_DEPTH - IF_BASE (the current stack's frame count).
prep_stack_size:
                ld      a, (IF_DEPTH)
                ld      hl, IF_BASE
                sub     (hl)
                ret

; prep_ifbase_ptr — HL -> IF_STACK + IF_BASE*3 (bottom frame of current stack).
prep_ifbase_ptr:
                ld      a, (IF_BASE)
                ld      l, a
                ld      h, 0
                ld      d, h
                ld      e, l
                add     hl, hl                  ; *2
                add     hl, de                  ; *3
                ld      de, IF_STACK
                add     hl, de
                ret

; prep_active — A=1 (NZ) if every frame in the current stack (frames
; [IF_BASE, IF_DEPTH)) is active, else A=0 (Z).
prep_active:
                call    prep_stack_size         ; A = frame count
                ld      b, a
                call    prep_ifbase_ptr         ; HL -> first frame of current stack
                ; fall through to prep_active_from

; prep_active_from — A=1 (NZ) if the B frames starting at HL are all active,
; else A=0 (Z).  B=0 is vacuously active.
prep_active_from:
                ld      a, b
                or      a
                jr      z, pan_yes              ; zero frames -> vacuously active
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
                ; STRPOOL overflow guard: STRPOOL_TOP + len > MACRO_STRPOOL_END.
                push    hl
                ld      hl, (STRPOOL_TOP)
                add     hl, bc                   ; end of copy
                ex      de, hl                   ; DE = end of copy
                ld      hl, MACRO_STRPOOL_END
                or      a
                sbc     hl, de                   ; END - end; <0 -> overflow
                jp      c, prep_fail
                pop     hl
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
                ; PARAM_IDX overflow guard: PARAM_TOP + 4 > PARAM_IDX_END.
                ld      hl, (PARAM_TOP)
                ld      de, 4
                add     hl, de
                ex      de, hl                   ; DE = PARAM_TOP + 4
                ld      hl, PARAM_IDX_END
                or      a
                sbc     hl, de                   ; END - (top+4); <0 -> overflow
                jp      c, prep_fail
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
                ; BODY_IDX overflow guard: BODY_TOP + 4 > BODY_IDX_END.
                ld      hl, (BODY_TOP)
                ld      de, 4
                add     hl, de
                ex      de, hl                   ; DE = BODY_TOP + 4
                ld      hl, BODY_IDX_END
                or      a
                sbc     hl, de                   ; END - (top+4); <0 -> overflow
                jp      c, prep_fail
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
                jp      nc, prep_fail            ; macro table overflow -> error
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
; Macro invocation — tryExpandMacroInvocation / splitMacroArgs / buildSubstituter.
;
; A default-case line whose first word (splitIndent → leading isIdentCont run)
; names a known macro is expanded: split the args (paren/string-aware), arity-
; check, guard against a recursion cycle, substitute \param in every body line,
; and RE-preprocess the substituted text via a reentrant prep_process_lines.
; Mid-stream `# <line> "<file>"` directives are emitted at the macro-definition
; boundary (defline+1) and after the call (callline+1), byte-identical to host
; emitLineDirective.  There is one file in this brick (PREP_PATH) — .include is
; Brick 3 — so defPos.file and pos.file are both PREP_PATH.
; ===========================================================================

; prep_clear_expanding — zero the per-macro expanding flags.
prep_clear_expanding:
                ld      hl, MACRO_EXPANDING
                ld      b, MACRO_MAX
pce_loop:
                ld      (hl), 0
                inc     hl
                djnz    pce_loop
                ret

; prep_emit_line_directive — emit `# <HL> "<CURRENT_FILE>"\n` (emitLineDirective
; for the currently-active file). CURRENT_FILE is PREP_PATH at top level and the
; resolved include path inside an .include.
;   In: HL = line number (u16).
prep_emit_line_directive:
                ld      de, (CURRENT_FILE_PTR)
                ld      bc, (CURRENT_FILE_LEN)
                ; fall through to prep_emit_line_directive_for

; prep_emit_line_directive_for — emit `# <HL> "<file>"\n` for an explicit file.
;   In: HL = line number; DE = file ptr; BC = file length.
prep_emit_line_directive_for:
                ld      (ELD_FILE_PTR), de
                ld      (ELD_FILE_LEN), bc
                push    hl
                ld      a, CH_HASH
                call    prep_emit_byte
                ld      a, CH_SPACE
                call    prep_emit_byte
                pop     hl
                call    prep_emit_u16_dec       ; decimal line number
                ld      a, CH_SPACE
                call    prep_emit_byte
                ld      a, CH_QUOTE
                call    prep_emit_byte
                call    prep_emit_file          ; ELD_FILE_PTR bytes (ELD_FILE_LEN)
                ld      a, CH_QUOTE
                call    prep_emit_byte
                ld      a, CH_NL
                call    prep_emit_byte
                ret

; prep_emit_file — emit ELD_FILE_LEN bytes from ELD_FILE_PTR.
prep_emit_file:
                ld      bc, (ELD_FILE_LEN)
                ld      a, b
                or      c
                ret     z
                ld      hl, (ELD_FILE_PTR)
pef_loop:
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
                jr      nz, pef_loop
                ret

; prep_emit_u16_dec — emit HL as decimal ASCII (no leading zeros; 0 -> "0").
prep_emit_u16_dec:
                xor     a
                ld      (PED_STARTED), a
                ld      de, 10000
                call    ped_place
                ld      de, 1000
                call    ped_place
                ld      de, 100
                call    ped_place
                ld      de, 10
                call    ped_place
                ; units digit: always emitted
                ld      a, l
                add     a, &30
                call    prep_emit_byte
                ret

; ped_place — emit the (HL / DE) digit unless it is a leading zero; leave the
; remainder in HL.  DE = the place value (10000/1000/100/10). B/DE preserved by
; prep_emit_byte, so HL flows to the next place.
ped_place:
                ld      b, &30                  ; '0' + count
ped_sub:
                or      a
                sbc     hl, de
                jr      c, ped_sub_done
                inc     b
                jr      ped_sub
ped_sub_done:
                add     hl, de                  ; undo the final over-subtraction
                ld      a, b
                cp      &30
                jr      nz, ped_emit            ; non-zero digit -> emit
                ld      a, (PED_STARTED)
                or      a
                ret     z                       ; leading zero -> suppress
                ld      a, &30
                call    prep_emit_byte
                ret
ped_emit:
                ld      a, 1
                ld      (PED_STARTED), a
                ld      a, b
                call    prep_emit_byte
                ret

; prep_macro_recptr — HL = MACRO_TAB + A*12 (record ptr for record index A).
prep_macro_recptr:
                ld      l, a
                ld      h, 0
                add     hl, hl                  ; *2
                add     hl, hl                  ; *4
                ld      d, h
                ld      e, l                    ; DE = *4
                add     hl, hl                  ; *8
                add     hl, de                  ; *12
                ld      de, MACRO_TAB
                add     hl, de
                ret

; prep_macro_find — find the NEWEST MACRO_TAB record whose name equals
; TE_NAME_PTR/TE_NAME_LEN (last-wins: a redefined macro shadows the older, as
; Go's map overwrite does).  Out: CF=1 with TE_REC/TE_K set if found, else CF=0.
prep_macro_find:
                ld      a, (MACRO_COUNT)
                or      a
                jr      z, pmf_no
                ld      (TE_FIND_I), a          ; scan index = count, pre-decremented
pmf_loop:
                ld      a, (TE_FIND_I)
                or      a
                jr      z, pmf_no               ; scanned every record
                dec     a
                ld      (TE_FIND_I), a          ; current record index
                call    prep_macro_recptr       ; HL -> record
                ld      (TE_REC), hl            ; tentative
                ld      e, (hl)
                inc     hl
                ld      d, (hl)
                inc     hl                      ; DE = name_ptr
                ld      a, (hl)                 ; A = name_len (1 byte)
                ld      b, a
                ld      hl, (TE_NAME_LEN)
                ld      a, h
                or      a
                jr      nz, pmf_loop            ; wanted len >= 256 -> mismatch
                ld      a, l
                cp      b
                jr      nz, pmf_loop            ; length mismatch
                or      a
                jr      z, pmf_hit              ; both zero-length (name is nonempty, defensive)
                ld      hl, (TE_NAME_PTR)
pmf_cmp:
                ld      a, (de)
                cp      (hl)
                jr      nz, pmf_loop
                inc     de
                inc     hl
                dec     b
                jr      nz, pmf_cmp
pmf_hit:
                ld      a, (TE_FIND_I)
                ld      (TE_K), a
                scf
                ret
pmf_no:
                or      a
                ret

; prep_try_expand — tryExpandMacroInvocation on the current line.
;   Reads TRIM_PTR/TRIM_LEN (splitIndent result), LINE_*, CUR_LINE.
;   Out: A=1 if the line was consumed as a macro call, A=0 if not a macro.
;   Fatal errors (arity / recursion cycle / pool overflow) longjmp via prep_fail.
prep_try_expand:
                ; rest == "" -> not a macro.
                ld      bc, (TRIM_LEN)
                ld      a, b
                or      c
                jp      z, pte_no
                ; name = leading isIdentCont run of rest.
                ld      hl, (TRIM_PTR)
                ld      (TE_NAME_PTR), hl
                ld      bc, (TRIM_LEN)
                call    prep_scan_ident         ; BC = name length
                ld      a, b
                or      c
                jp      z, pte_no               ; first word is empty
                ld      (TE_NAME_LEN), bc
                ; look up macro (last-wins).
                call    prep_macro_find
                jp      nc, pte_no
                ; extract record fields: nparams(+3), params_ptr(+4), nbody(+6),
                ; body_ptr(+8), defline(+10).
                ld      hl, (TE_REC)
                ld      de, 3
                add     hl, de
                ld      a, (hl)
                ld      (TE_NPARAMS), a
                inc     hl                      ; -> params_ptr
                ld      e, (hl)
                inc     hl
                ld      d, (hl)
                inc     hl
                ld      (TE_PARAMSPTR), de
                ld      e, (hl)
                inc     hl
                ld      d, (hl)
                inc     hl
                ld      (TE_NBODY), de
                ld      e, (hl)
                inc     hl
                ld      d, (hl)
                inc     hl
                ld      (TE_BODYPTR), de
                ld      e, (hl)
                inc     hl
                ld      d, (hl)
                ld      (TE_DEFLINE), de
                ; argText = stripTrailingComment(TrimSpace(rest[namelen:])).
                ld      hl, (TRIM_PTR)
                ld      bc, (TE_NAME_LEN)
                add     hl, bc                  ; rest + namelen
                ex      de, hl                  ; DE = ptr
                ld      hl, (TRIM_LEN)
                or      a
                sbc     hl, bc                  ; TRIM_LEN - namelen
                ld      b, h
                ld      c, l
                ex      de, hl                  ; HL = ptr, BC = len
                call    prep_trim_space
                call    prep_strip_comment      ; HL=STRIP_BUF, BC=argText len
                call    prep_split_args         ; -> ARG_IDX / ARG_COUNT
                ; arity: ARG_COUNT == nparams?
                ld      a, (ARG_COUNT)
                ld      b, a
                ld      a, (TE_NPARAMS)
                cp      b
                jp      nz, prep_fail           ; arg-count mismatch
                ; recursion-cycle guard: MACRO_EXPANDING[TE_K] set?
                ld      a, (TE_K)
                ld      l, a
                ld      h, 0
                ld      de, MACRO_EXPANDING
                add     hl, de
                ld      a, (hl)
                or      a
                jp      nz, prep_fail           ; recursive expansion (cycle)
                ld      (hl), 1                 ; mark expanding
                ; emit `# <defline+1> "<path>"`.
                ld      hl, (TE_DEFLINE)
                inc     hl
                call    prep_emit_line_directive
                ; build the substituted body into the arena (bump stack).
                ld      hl, (SUBST_TOP)
                ld      (TE_WIN_START), hl      ; window start = arena base
                call    prep_build_subst_body   ; advances SUBST_TOP to window end
                ; --- save caller context, then set up the recursion window ---
                ld      hl, (SRC_PTR)
                push    hl
                ld      hl, (SRC_END)
                push    hl
                ld      hl, (CUR_LINE)          ; call-site line
                push    hl
                ld      a, (IF_BASE)
                ld      h, 0
                ld      l, a
                push    hl
                ld      hl, (TE_WIN_START)      ; arena base (freed on return)
                push    hl
                ld      a, (TE_K)
                ld      h, 0
                ld      l, a
                push    hl
                ; new window = [TE_WIN_START, SUBST_TOP); CUR_LINE = defline+1.
                ld      hl, (TE_WIN_START)
                ld      (SRC_PTR), hl
                ld      hl, (SUBST_TOP)
                ld      (SRC_END), hl
                ld      hl, (TE_DEFLINE)
                inc     hl
                ld      (CUR_LINE), hl
                ; fresh frame stack based above the caller's frames.
                ld      a, (IF_DEPTH)
                ld      (IF_BASE), a
                ld      b, 1                    ; active = 1
                ld      c, 0                    ; taken = 0
                call    prep_push_frame
                ; re-preprocess the substituted body.
                call    prep_process_lines
                ; --- restore caller context ---
                ld      a, (IF_BASE)
                ld      (IF_DEPTH), a           ; pop the recursion's frames
                pop     hl                      ; k
                ld      a, l
                ld      l, a
                ld      h, 0
                ld      de, MACRO_EXPANDING
                add     hl, de
                ld      (hl), 0                 ; unmark expanding
                pop     hl                      ; arena base
                ld      (SUBST_TOP), hl         ; free the arena region
                pop     hl                      ; IF_BASE
                ld      a, l
                ld      (IF_BASE), a
                pop     hl                      ; call-site line
                ld      (CUR_LINE), hl
                pop     hl                      ; SRC_END
                ld      (SRC_END), hl
                pop     hl                      ; SRC_PTR
                ld      (SRC_PTR), hl
                ; emit `# <callline+1> "<path>"`.
                ld      hl, (CUR_LINE)
                inc     hl
                call    prep_emit_line_directive
                ld      a, 1                    ; consumed
                ret
pte_no:
                xor     a                       ; not a macro invocation
                ret

; ===========================================================================
; prep_split_args — splitMacroArgs over the argText in STRIP_BUF.
;   In:  HL=STRIP_BUF, BC=argText length.
;   Out: ARG_IDX filled with {ptr:2,len:2} spans; ARG_COUNT set.  An empty
;        argText yields zero args (matching Go's `if s=="" { return nil }`).
; ===========================================================================
prep_split_args:
                ld      (SA_BASE), hl
                ld      (SA_LEN), bc
                xor     a
                ld      (ARG_COUNT), a
                ld      hl, ARG_IDX
                ld      (ARG_TOP), hl
                ld      a, b
                or      c
                ret     z                       ; empty -> 0 args
                ld      hl, 0
                ld      (SA_I), hl
                ld      (SA_START), hl
                xor     a
                ld      (SA_DEPTH), a
                ld      (SA_INSTR), a
sa_loop:
                ld      hl, (SA_I)
                ld      bc, (SA_LEN)
                or      a
                sbc     hl, bc
                jr      nc, sa_end              ; i >= len
                call    sa_getc                 ; A = base[i]
                cp      CH_QUOTE
                jr      nz, sa_notq
                ; quote: toggle inStr unless prev byte is '\' (escaped quote).
                ld      hl, (SA_I)
                ld      a, h
                or      l
                jr      z, sa_toggle            ; i==0 -> toggle
                call    sa_getprev              ; A = base[i-1]
                cp      CH_BSLASH
                jr      z, sa_adv               ; escaped quote -> no toggle
sa_toggle:
                ld      a, (SA_INSTR)
                xor     1
                ld      (SA_INSTR), a
                jr      sa_adv
sa_notq:
                ld      a, (SA_INSTR)
                or      a
                jr      nz, sa_adv              ; inside string -> skip specials
                call    sa_getc
                cp      &28                     ; '('
                jr      nz, sa_notopen
                ld      a, (SA_DEPTH)
                inc     a
                ld      (SA_DEPTH), a
                jr      sa_adv
sa_notopen:
                cp      &29                     ; ')'
                jr      nz, sa_notclose
                ld      a, (SA_DEPTH)
                or      a
                jr      z, sa_adv               ; unbalanced ')' -> ignore (Go: depth>0)
                dec     a
                ld      (SA_DEPTH), a
                jr      sa_adv
sa_notclose:
                cp      CH_COMMA
                jr      nz, sa_adv
                ld      a, (SA_DEPTH)
                or      a
                jr      nz, sa_adv              ; comma inside parens -> not a separator
                ; separator at depth 0: emit arg [SA_START, SA_I).
                call    sa_emit_arg
                ld      hl, (SA_I)
                inc     hl
                ld      (SA_START), hl
sa_adv:
                ld      hl, (SA_I)
                inc     hl
                ld      (SA_I), hl
                jr      sa_loop
sa_end:
                ; final arg [SA_START, SA_LEN); at loop exit SA_I == SA_LEN.
                call    sa_emit_arg
                ret

; sa_getc — A = SA_BASE[SA_I].
sa_getc:
                ld      hl, (SA_BASE)
                ld      bc, (SA_I)
                add     hl, bc
                ld      a, (hl)
                ret

; sa_getprev — A = SA_BASE[SA_I - 1].
sa_getprev:
                ld      hl, (SA_BASE)
                ld      bc, (SA_I)
                add     hl, bc
                dec     hl
                ld      a, (hl)
                ret

; sa_emit_arg — append TrimSpace(SA_BASE[SA_START..SA_I)) to ARG_IDX.
sa_emit_arg:
                ld      hl, (SA_BASE)
                ld      bc, (SA_START)
                add     hl, bc                  ; HL = segment start ptr
                push    hl
                ld      hl, (SA_I)
                ld      bc, (SA_START)
                or      a
                sbc     hl, bc                  ; HL = segment length
                ld      b, h
                ld      c, l
                pop     hl                      ; HL = segment start ptr
                call    prep_trim_space         ; HL/BC = trimmed span
                ; overflow guard: ARG_COUNT < ARG_MAX.
                ld      a, (ARG_COUNT)
                cp      ARG_MAX
                jp      nc, prep_fail           ; too many args -> error
                ld      de, (ARG_TOP)
                ld      a, l
                ld      (de), a
                inc     de
                ld      a, h
                ld      (de), a
                inc     de
                ld      a, c
                ld      (de), a
                inc     de
                ld      a, b
                ld      (de), a
                inc     de
                ld      (ARG_TOP), de
                ld      a, (ARG_COUNT)
                inc     a
                ld      (ARG_COUNT), a
                ret

; ===========================================================================
; prep_build_subst_body — substitute \param in each body line and write the
; result (each line + '\n') into the substitution arena.  Reads TE_BODYPTR /
; TE_NBODY (spans into PREP_SRC) plus the params/args for the substituter.
; ===========================================================================
prep_build_subst_body:
                ld      hl, (TE_NBODY)
                ld      (BSB_REMAIN), hl
                ld      hl, (TE_BODYPTR)
                ld      (BSB_BODYP), hl
bsb_loop:
                ld      hl, (BSB_REMAIN)
                ld      a, h
                or      l
                ret     z                       ; all body lines done
                ; read body-line span {ptr:2, len:2}.
                ld      hl, (BSB_BODYP)
                ld      e, (hl)
                inc     hl
                ld      d, (hl)
                inc     hl
                ld      (BSB_LPTR), de
                ld      e, (hl)
                inc     hl
                ld      d, (hl)
                inc     hl
                ld      (BSB_LLEN), de
                ld      (BSB_BODYP), hl
                call    prep_substitute_line    ; emit substituted line to arena
                ld      a, CH_NL
                call    prep_subst_emit         ; line terminator
                ld      hl, (BSB_REMAIN)
                dec     hl
                ld      (BSB_REMAIN), hl
                jr      bsb_loop

; ===========================================================================
; prep_substitute_line — buildSubstituter applied to BSB_LPTR/BSB_LLEN, writing
; to the arena.  \\ is a literal backslash pair; \param (longest match, followed
; by a non-isIdentCont byte or EOL) is replaced by its argument, inside idents
; and strings alike; an unrecognised \x keeps the backslash and rescans from x.
; ===========================================================================
prep_substitute_line:
                ld      hl, 0
                ld      (SL_I), hl
sl_loop:
                ld      hl, (SL_I)
                ld      bc, (BSB_LLEN)
                or      a
                sbc     hl, bc
                ret     nc                      ; i >= len -> done
                call    sl_getc                 ; A = line[i]
                cp      CH_BSLASH
                jr      nz, sl_plain
                ; backslash: need i+1 < len, else emit '\' literally.
                ld      hl, (SL_I)
                inc     hl
                ld      bc, (BSB_LLEN)
                or      a
                sbc     hl, bc
                jr      nc, sl_backslash_lit
                call    sl_getnext              ; A = line[i+1]
                cp      CH_BSLASH
                jr      nz, sl_try_param
                ; "\\": emit both, advance 2.
                ld      a, CH_BSLASH
                call    prep_subst_emit
                ld      a, CH_BSLASH
                call    prep_subst_emit
                ld      hl, (SL_I)
                inc     hl
                inc     hl
                ld      (SL_I), hl
                jr      sl_loop
sl_try_param:
                call    sl_match_param          ; CF=1 -> SL_MATCH_J/SL_MATCH_LEN
                jr      nc, sl_backslash_lit
                call    sl_emit_arg_j           ; emit arg[SL_MATCH_J]
                ld      hl, (SL_I)
                inc     hl                      ; skip '\'
                ld      bc, (SL_MATCH_LEN)
                add     hl, bc                  ; skip the param name
                ld      (SL_I), hl
                jr      sl_loop
sl_backslash_lit:
                ld      a, CH_BSLASH
                call    prep_subst_emit
                ld      hl, (SL_I)
                inc     hl
                ld      (SL_I), hl
                jr      sl_loop
sl_plain:
                call    sl_getc
                call    prep_subst_emit
                ld      hl, (SL_I)
                inc     hl
                ld      (SL_I), hl
                jr      sl_loop

; sl_getc — A = BSB_LPTR[SL_I].
sl_getc:
                ld      hl, (BSB_LPTR)
                ld      bc, (SL_I)
                add     hl, bc
                ld      a, (hl)
                ret

; sl_getnext — A = BSB_LPTR[SL_I + 1].
sl_getnext:
                ld      hl, (BSB_LPTR)
                ld      bc, (SL_I)
                add     hl, bc
                inc     hl
                ld      a, (hl)
                ret

; sl_match_param — does a \param match at line[SL_I]?  Scans all params, keeping
; the LONGEST whose name equals line[i+1 .. i+1+plen) and whose terminator
; (line[i+1+plen] or EOL) is a non-isIdentCont byte.  (Given the terminator
; check, at most one param can match at a position; longest-first is belt-and-
; braces for the \address-vs-\addr case.)
;   Out: CF=1 with SL_MATCH_J (index) + SL_MATCH_LEN (plen) if matched, else CF=0.
sl_match_param:
                ld      hl, 0
                ld      (SL_BEST_LEN), hl       ; best plen = 0 (none yet)
                ld      a, (TE_NPARAMS)
                or      a
                jr      z, smp_none
                ld      (SMP_NREM), a
                ld      hl, (TE_PARAMSPTR)
                ld      (SMP_PP), hl
                xor     a
                ld      (SMP_J), a
smp_loop:
                ld      a, (SMP_NREM)
                or      a
                jr      z, smp_done
                ; read param span {ptr:2, len:2}.
                ld      hl, (SMP_PP)
                ld      e, (hl)
                inc     hl
                ld      d, (hl)
                inc     hl
                ld      (SMP_PPTR), de
                ld      e, (hl)
                inc     hl
                ld      d, (hl)
                inc     hl
                ld      (SMP_PLEN), de
                ld      (SMP_PP), hl
                call    smp_check               ; CF=1 if param matches here
                jr      nc, smp_next
                ; matched: keep it if plen > best.
                ld      hl, (SMP_PLEN)
                ld      de, (SL_BEST_LEN)
                or      a
                sbc     hl, de                  ; plen - best
                jr      c, smp_next             ; plen < best
                jr      z, smp_next             ; plen == best -> keep earlier
                ld      hl, (SMP_PLEN)
                ld      (SL_BEST_LEN), hl
                ld      a, (SMP_J)
                ld      (SL_MATCH_J), a
smp_next:
                ld      a, (SMP_J)
                inc     a
                ld      (SMP_J), a
                ld      a, (SMP_NREM)
                dec     a
                ld      (SMP_NREM), a
                jr      smp_loop
smp_done:
                ld      hl, (SL_BEST_LEN)
                ld      a, h
                or      l
                jr      z, smp_none
                ld      (SL_MATCH_LEN), hl
                scf
                ret
smp_none:
                or      a
                ret

; smp_check — CF=1 iff param (SMP_PPTR, SMP_PLEN) equals line[i+1 .. i+1+plen)
; AND the following byte (or EOL) is a non-isIdentCont terminator.
smp_check:
                ld      hl, (SL_I)
                inc     hl                      ; i+1
                ld      bc, (SMP_PLEN)
                add     hl, bc                  ; end = i+1+plen
                ld      (SMP_END), hl
                ld      de, (BSB_LLEN)
                or      a
                sbc     hl, de                  ; end - len
                jr      z, smp_chk_eol          ; end == len -> EOL terminator
                jr      nc, smp_no              ; end > len -> too long
                ; end < len: compare bytes, then check the terminator byte.
                call    smp_cmpbytes
                jr      nc, smp_no
                ld      hl, (BSB_LPTR)
                ld      bc, (SMP_END)
                add     hl, bc
                ld      a, (hl)                 ; line[end]
                call    prep_is_ident_cont
                jr      c, smp_no               ; ident-cont -> not a terminator
                scf
                ret
smp_chk_eol:
                call    smp_cmpbytes            ; CF from the byte compare
                ret
smp_no:
                or      a
                ret

; smp_cmpbytes — CF=1 iff line[SL_I+1 ..] matches param (SMP_PPTR, SMP_PLEN).
smp_cmpbytes:
                ld      bc, (SMP_PLEN)
                ld      a, b
                or      c
                jr      z, smp_cb_eq            ; zero-length param (defensive) -> equal
                ld      hl, (BSB_LPTR)
                ld      de, (SL_I)
                add     hl, de
                inc     hl                      ; &line[i+1]
                ld      de, (SMP_PPTR)          ; &param[0]
smp_cb_loop:
                ld      a, (de)
                cp      (hl)
                jr      nz, smp_cb_ne
                inc     hl
                inc     de
                dec     bc
                ld      a, b
                or      c
                jr      nz, smp_cb_loop
smp_cb_eq:
                scf
                ret
smp_cb_ne:
                or      a
                ret

; sl_emit_arg_j — emit arg[SL_MATCH_J] (span in ARG_IDX -> STRIP_BUF) to arena.
sl_emit_arg_j:
                ld      a, (SL_MATCH_J)
                ld      l, a
                ld      h, 0
                add     hl, hl
                add     hl, hl                  ; *4
                ld      de, ARG_IDX
                add     hl, de
                ld      e, (hl)
                inc     hl
                ld      d, (hl)
                inc     hl                      ; DE = arg ptr
                ld      c, (hl)
                inc     hl
                ld      b, (hl)                 ; BC = arg len
                ld      a, b
                or      c
                ret     z                       ; empty arg
                ld      (SL_AE_PTR), de
sl_ae_loop:
                ld      de, (SL_AE_PTR)
                ld      a, (de)
                push    bc
                call    prep_subst_emit
                pop     bc
                ld      hl, (SL_AE_PTR)
                inc     hl
                ld      (SL_AE_PTR), hl
                dec     bc
                ld      a, b
                or      c
                jr      nz, sl_ae_loop
                ret

; prep_subst_emit — write A to the substitution arena at SUBST_TOP (bounds-
; checked), SUBST_TOP++.  Preserves BC/DE/HL.
prep_subst_emit:
                push    hl
                push    de
                push    bc
                ld      hl, (SUBST_TOP)
                ld      de, SUBST_ARENA_END
                push    af
                or      a
                sbc     hl, de
                jr      nc, sse_over            ; SUBST_TOP >= arena end
                pop     af
                ld      hl, (SUBST_TOP)
                ld      (hl), a
                inc     hl
                ld      (SUBST_TOP), hl
                pop     bc
                pop     de
                pop     hl
                ret
sse_over:
                pop     af
                pop     bc
                pop     de
                pop     hl
                jp      prep_fail

; ===========================================================================
; .include — tryParseInclude / handleInclude / processFile recursion.
;
; File access is a pluggable reader vector (READER_VEC, defaulted to the
; memory-backed prep_mem_reader at init; the SAM build points it at the real
; UIFA+HGTHD+HLOAD reader before prep_run — no conditional assembly). handleInclude
; builds candidate paths (dir of the including file first, then each -I dir) via a
; restricted filepath.Join/Dir, calls the reader for each, and on the first hit
; re-preprocesses the returned bytes as a new file: emit `# 1 "<resolved>"`, run a
; reentrant prep_process_lines over the file with CURRENT_FILE=resolved and a fresh
; frame stack, then restore the includer and emit `# <includeline+1> "<includer>"`.
; ===========================================================================

; prep_intern_path_prep — copy PREP_PATH (NUL-terminated) into PATH_ARENA and set
; CURRENT_FILE to that persistent copy (the top-level file at prep_run init).
prep_intern_path_prep:
                ld      hl, PREP_PATH
                ld      bc, 0
pipp_len:
                ld      a, (hl)
                or      a
                jr      z, pipp_gotlen
                inc     hl
                inc     bc
                jr      pipp_len
pipp_gotlen:
                ld      (CURRENT_FILE_LEN), bc
                ld      hl, PREP_PATH
                call    prep_intern_path        ; DE = arena copy
                ld      (CURRENT_FILE_PTR), de
                ret

; prep_intern_path — copy (HL=ptr, BC=len) into PATH_ARENA at PATH_TOP.
;   Out: DE = the arena copy ptr.  Paths persist for the whole prep_run (macro
;   def-file and CURRENT_FILE point into the arena), so nothing frees them.
prep_intern_path:
                ; overflow guard: PATH_TOP + len > PATH_ARENA_END -> error.
                push    hl
                push    bc
                ld      hl, (PATH_TOP)
                add     hl, bc
                ex      de, hl                  ; DE = PATH_TOP + len
                ld      hl, PATH_ARENA_END
                or      a
                sbc     hl, de
                jp      c, prep_fail
                pop     bc
                pop     hl
                ld      de, (PATH_TOP)
                push    de                      ; save copy start
                ld      a, b
                or      c
                jr      z, pin_copied
pin_copy:
                ld      a, (hl)
                ld      (de), a
                inc     hl
                inc     de
                dec     bc
                ld      a, b
                or      c
                jr      nz, pin_copy
pin_copied:
                ld      (PATH_TOP), de
                pop     de                      ; DE = copy start
                ret

; prep_try_include — tryParseInclude on the current line, and handle it.
;   Reads TRIM_PTR/TRIM_LEN.  Out: A=1 if the line was an .include (consumed),
;   A=0 otherwise.  A missing file / pool overflow longjmps via prep_fail.
prep_try_include:
                ld      de, pat_dot_include
                call    prep_prefix
                jr      nc, pti_no
                jr      z, pti_no               ; exact ".include" (no path) -> not
                cp      CH_SPACE
                jr      z, pti_ok
                cp      CH_TAB
                jr      z, pti_ok
                jr      pti_no                  ; ".includeX" -> not a directive
pti_ok:
                ; rest = TrimSpace(trimmed[8:]) then stripTrailingComment.
                ld      hl, (TRIM_PTR)
                ld      bc, (TRIM_LEN)
                ld      de, 8                   ; len(".include")
                add     hl, de
                ld      a, c
                sub     8
                ld      c, a
                ld      a, b
                sbc     0
                ld      b, a
                call    prep_trim_space
                call    prep_strip_comment      ; HL=STRIP_BUF, BC=len
                ; need len >= 2, first byte '"', last byte '"'.
                ld      a, b
                or      a
                jr      nz, pti_len_ok          ; len >= 256 -> certainly >= 2
                ld      a, c
                cp      2
                jr      c, pti_no               ; len < 2
pti_len_ok:
                ld      hl, STRIP_BUF
                ld      a, (hl)
                cp      CH_QUOTE
                jr      nz, pti_no
                ld      hl, STRIP_BUF
                add     hl, bc
                dec     hl                      ; -> last byte
                ld      a, (hl)
                cp      CH_QUOTE
                jr      nz, pti_no
                ; path = STRIP_BUF[1 .. len-1); INC_PATH_LEN = len - 2.
                ld      hl, STRIP_BUF
                inc     hl
                ld      (INC_PATH_PTR), hl
                dec     bc
                dec     bc
                ld      (INC_PATH_LEN), bc
                call    prep_do_include
                ld      a, 1
                ret
pti_no:
                xor     a
                ret

; prep_do_include — resolve INC_PATH, read it, and re-preprocess it as a new file.
prep_do_include:
                call    prep_resolve_include    ; CF=1 with resolved name + content
                jp      nc, prep_fail           ; .include not found in search path
                ; --- save the includer's context ---
                ld      hl, (SRC_PTR)
                push    hl
                ld      hl, (SRC_END)
                push    hl
                ld      hl, (CUR_LINE)          ; the .include line number
                push    hl
                ld      hl, (CURRENT_FILE_PTR)
                push    hl
                ld      hl, (CURRENT_FILE_LEN)
                push    hl
                ld      a, (IF_BASE)
                ld      h, 0
                ld      l, a
                push    hl
                ; --- switch to the included file ---
                ld      hl, CAND_BUF
                ld      bc, (RESOLVED_LEN)
                ld      (CURRENT_FILE_LEN), bc
                call    prep_intern_path        ; DE = persistent resolved-name copy
                ld      (CURRENT_FILE_PTR), de
                ld      hl, (READ_CONTENT_PTR)
                ld      (SRC_PTR), hl
                ld      de, (READ_CONTENT_LEN)
                add     hl, de
                ld      (SRC_END), hl
                ld      hl, 1
                ld      (CUR_LINE), hl          ; processFile startLine = 1
                ld      a, (IF_DEPTH)
                ld      (IF_BASE), a
                ld      b, 1
                ld      c, 0
                call    prep_push_frame         ; fresh active frame
                ld      hl, 1
                call    prep_emit_line_directive ; `# 1 "<resolved>"`
                call    prep_process_lines
                ; --- restore the includer ---
                ld      a, (IF_BASE)
                ld      (IF_DEPTH), a
                pop     hl                      ; IF_BASE
                ld      a, l
                ld      (IF_BASE), a
                pop     hl                      ; CURRENT_FILE_LEN
                ld      (CURRENT_FILE_LEN), hl
                pop     hl                      ; CURRENT_FILE_PTR
                ld      (CURRENT_FILE_PTR), hl
                pop     hl                      ; the .include line number
                ld      (CUR_LINE), hl
                pop     hl                      ; SRC_END
                ld      (SRC_END), hl
                pop     hl                      ; SRC_PTR
                ld      (SRC_PTR), hl
                ; `# <includeline+1> "<includer>"`
                ld      hl, (CUR_LINE)
                inc     hl
                call    prep_emit_line_directive
                ret

; prep_resolve_include — try INC_PATH along the search path: candidate 0 =
; join(dir(CURRENT_FILE), INC_PATH), then join(each -I dir, INC_PATH).
;   Out: CF=1 with CAND_BUF/RESOLVED_LEN = resolved name and READ_CONTENT_* set
;        (first hit); CF=0 if not found.
prep_resolve_include:
                call    prep_dir_of_current     ; DIR_PTR/DIR_LEN = dir(CURRENT_FILE)
                call    prep_build_and_try
                ret     c
                ld      a, (PREP_NINCDIRS)
                or      a
                ret     z                       ; no -I dirs -> not found (CF=0)
                ld      (RI_NREM), a
                ld      hl, PREP_INCDIRS
                ld      (RI_PTR), hl
ri_loop:
                ld      a, (RI_NREM)
                or      a
                jr      z, ri_no
                ; -I entry: [len:1][bytes]
                ld      hl, (RI_PTR)
                ld      c, (hl)                 ; dir len
                inc     hl
                ld      (DIR_PTR), hl
                ld      a, c
                ld      (DIR_LEN), a
                xor     a
                ld      (DIR_LEN+1), a
                ld      b, 0
                add     hl, bc                  ; -> next entry
                ld      (RI_PTR), hl
                call    prep_build_and_try
                ret     c
                ld      a, (RI_NREM)
                dec     a
                ld      (RI_NREM), a
                jr      ri_loop
ri_no:
                or      a
                ret

; prep_dir_of_current — DIR_PTR/DIR_LEN = filepath.Dir(CURRENT_FILE): the bytes
; up to the last '/', or length 0 (meaning ".") if there is no '/'.
prep_dir_of_current:
                ld      hl, (CURRENT_FILE_PTR)
                ld      (DIR_PTR), hl
                ld      hl, 0
                ld      (DIR_LEN), hl           ; default: no slash -> "."
                ld      (DOFC_I), hl
dofc_scan:
                ld      hl, (DOFC_I)
                ld      bc, (CURRENT_FILE_LEN)
                or      a
                sbc     hl, bc
                jr      nc, dofc_done           ; i >= len
                ld      hl, (CURRENT_FILE_PTR)
                ld      bc, (DOFC_I)
                add     hl, bc
                ld      a, (hl)
                cp      CH_SLASH
                jr      nz, dofc_next
                ld      hl, (DOFC_I)
                ld      (DIR_LEN), hl           ; dirlen = index of this '/'
dofc_next:
                ld      hl, (DOFC_I)
                inc     hl
                ld      (DOFC_I), hl
                jr      dofc_scan
dofc_done:
                ret

; prep_build_and_try — build CAND_BUF = join(DIR, INC_PATH) and call the reader.
;   Out: CF=1 if the reader found the file (READ_CONTENT_* set).
prep_build_and_try:
                call    prep_build_candidate    ; CAND_BUF / RESOLVED_LEN
                ld      hl, CAND_BUF
                ld      (REQ_NAME_PTR), hl
                ld      hl, (RESOLVED_LEN)
                ld      (REQ_NAME_LEN), hl
                jp      prep_call_reader        ; tail-call: returns CF from reader

; prep_call_reader — call the pluggable reader vector.
prep_call_reader:
                ld      hl, (READER_VEC)
                jp      (hl)

; prep_build_candidate — CAND_BUF = filepath.Join(DIR, INC_PATH), RESOLVED_LEN set.
; join(".", p) == join("", p) == p; else DIR + "/" + p.
prep_build_candidate:
                ; overflow guard: DIR_LEN + 1 + INC_PATH_LEN > CAND_CAP -> error.
                ld      hl, (DIR_LEN)
                ld      bc, (INC_PATH_LEN)
                add     hl, bc
                ld      bc, 1
                add     hl, bc
                ld      bc, CAND_CAP
                or      a
                sbc     hl, bc                  ; (dirlen+1+plen) - CAND_CAP
                jr      c, pbc_room             ; < CAP -> ok
                jr      z, pbc_room             ; == CAP -> ok (no NUL needed)
                jp      prep_fail
pbc_room:
                ; dir empty -> path only.
                ld      hl, (DIR_LEN)
                ld      a, h
                or      l
                jr      z, pbc_pathonly
                ; dir == "." -> path only.
                ld      a, h
                or      a
                jr      nz, pbc_withdir         ; len >= 256 -> with dir
                ld      a, l
                cp      1
                jr      nz, pbc_withdir
                ld      hl, (DIR_PTR)
                ld      a, (hl)
                cp      CH_DOT
                jr      z, pbc_pathonly
pbc_withdir:
                ld      de, CAND_BUF
                ld      hl, (DIR_PTR)
                ld      bc, (DIR_LEN)
                call    pbc_copy
                ld      a, CH_SLASH
                ld      (de), a
                inc     de
                ld      hl, (INC_PATH_PTR)
                ld      bc, (INC_PATH_LEN)
                call    pbc_copy
                ex      de, hl                  ; HL = end ptr
                ld      de, CAND_BUF
                or      a
                sbc     hl, de                  ; HL = resolved length
                ld      (RESOLVED_LEN), hl
                ret
pbc_pathonly:
                ld      de, CAND_BUF
                ld      hl, (INC_PATH_PTR)
                ld      bc, (INC_PATH_LEN)
                ld      (RESOLVED_LEN), bc
                call    pbc_copy
                ret

; pbc_copy — copy BC bytes from HL to DE; DE/HL advanced, BC=0 on return.
pbc_copy:
                ld      a, b
                or      c
                ret     z
pbc_c_loop:
                ld      a, (hl)
                ld      (de), a
                inc     hl
                inc     de
                dec     bc
                ld      a, b
                or      c
                jr      nz, pbc_c_loop
                ret

; prep_mem_reader — the default reader vector: look REQ_NAME up in PREP_FILES,
; a harness-written table of [name_len:1][name][content_len:2][content] entries
; (PREP_NFILES of them).  Out: CF=1 with READ_CONTENT_PTR/LEN on a name match.
prep_mem_reader:
                ld      a, (PREP_NFILES)
                or      a
                jr      z, pmr_no
                ld      b, a                    ; entries remaining
                ld      hl, PREP_FILES
pmr_loop:
                ld      a, (hl)                 ; name_len
                ld      (PMR_NAMELEN), a
                inc     hl                      ; -> name bytes
                ld      (PMR_NAMEPTR), hl
                ld      e, a
                ld      d, 0
                add     hl, de                  ; -> content_len (lo)
                ld      e, (hl)
                inc     hl
                ld      d, (hl)
                inc     hl                      ; -> content bytes; DE = content_len
                ld      (PMR_CONTENTLEN), de
                ld      (PMR_CONTENTPTR), hl
                add     hl, de                  ; -> next entry
                push    hl                      ; save next-entry ptr
                ; requested len must be < 256 and equal this entry's name_len.
                ld      a, (REQ_NAME_LEN+1)
                or      a
                jr      nz, pmr_next
                ld      a, (REQ_NAME_LEN)
                ld      c, a
                ld      a, (PMR_NAMELEN)
                cp      c
                jr      nz, pmr_next
                or      a
                jr      z, pmr_hit              ; both zero-length (defensive)
                ld      hl, (PMR_NAMEPTR)
                ld      de, (REQ_NAME_PTR)
pmr_cmp:
                ld      a, (de)
                cp      (hl)
                jr      nz, pmr_next
                inc     hl
                inc     de
                dec     c
                jr      nz, pmr_cmp
pmr_hit:
                pop     hl                      ; discard next-entry ptr
                ld      de, (PMR_CONTENTPTR)
                ld      (READ_CONTENT_PTR), de
                ld      de, (PMR_CONTENTLEN)
                ld      (READ_CONTENT_LEN), de
                scf
                ret
pmr_next:
                pop     hl                      ; HL = next entry ptr
                djnz    pmr_loop
pmr_no:
                or      a
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
pat_dot_include:  defm ".include"
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
IF_BASE:          defs 1          ; bottom frame index of the current stack
PREP_SP_SAVE:     defs 2          ; SP at prep_run entry (prep_fail longjmp target)
PED_STARTED:      defs 1          ; prep_emit_u16_dec: leading-zero suppression

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

; Macro-invocation staging (tryExpandMacroInvocation / splitMacroArgs /
; buildSubstituter).  None is live across the expansion recursion — the values
; the post-recursion tail needs (arena base, call-site line, expanding index)
; ride the hardware stack, so nested expansions may reuse these freely.
TE_NAME_PTR:      defs 2
TE_NAME_LEN:      defs 2
TE_REC:           defs 2          ; found MACRO_TAB record ptr
TE_K:             defs 1          ; found record index (MACRO_EXPANDING slot)
TE_FIND_I:        defs 1          ; prep_macro_find scan cursor
TE_NPARAMS:       defs 1
TE_PARAMSPTR:     defs 2
TE_NBODY:         defs 2
TE_BODYPTR:       defs 2
TE_DEFLINE:       defs 2
TE_WIN_START:     defs 2          ; substituted-body window base

SA_BASE:          defs 2          ; splitMacroArgs cursor state
SA_LEN:           defs 2
SA_I:             defs 2
SA_START:         defs 2
SA_DEPTH:         defs 1
SA_INSTR:         defs 1
ARG_COUNT:        defs 1
ARG_TOP:          defs 2

BSB_REMAIN:       defs 2          ; prep_build_subst_body loop state
BSB_BODYP:        defs 2
BSB_LPTR:         defs 2
BSB_LLEN:         defs 2

SL_I:             defs 2          ; prep_substitute_line state
SL_MATCH_J:       defs 1
SL_MATCH_LEN:     defs 2
SL_BEST_LEN:      defs 2
SL_AE_PTR:        defs 2
SMP_NREM:         defs 1          ; sl_match_param state
SMP_J:            defs 1
SMP_PP:           defs 2
SMP_PPTR:         defs 2
SMP_PLEN:         defs 2
SMP_END:          defs 2

; .include staging (tryParseInclude / handleInclude / the reader vector).
CURRENT_FILE_PTR: defs 2          ; active file path (into PATH_ARENA)
CURRENT_FILE_LEN: defs 2
PATH_TOP:         defs 2          ; PATH_ARENA bump pointer
ELD_FILE_PTR:     defs 2          ; prep_emit_line_directive_for: file to name
ELD_FILE_LEN:     defs 2
INC_PATH_PTR:     defs 2          ; the unquoted .include path (into STRIP_BUF)
INC_PATH_LEN:     defs 2
DIR_PTR:          defs 2          ; current candidate's directory
DIR_LEN:          defs 2
DOFC_I:           defs 2          ; prep_dir_of_current scan cursor
RESOLVED_LEN:     defs 2          ; length of the candidate name in CAND_BUF
REQ_NAME_PTR:     defs 2          ; reader-vector input: resolved name
REQ_NAME_LEN:     defs 2
READ_CONTENT_PTR: defs 2          ; reader-vector output: file bytes
READ_CONTENT_LEN: defs 2
RI_NREM:          defs 1          ; prep_resolve_include -I loop state
RI_PTR:           defs 2
PMR_NAMELEN:      defs 1          ; prep_mem_reader entry cursor
PMR_NAMEPTR:      defs 2
PMR_CONTENTLEN:   defs 2
PMR_CONTENTPTR:   defs 2
READER_VEC:       defs 2          ; pluggable file reader (0 => default at init)

PREP_ERR:         defs 1

; Pool sizes below are for the FLAT STANDALONE HARNESS only (org &8000 -> a
; 32 KB window, 0x8000..0xFFFF, with the harness stack at 0x6FFE). The real
; on-SAM deployment (b4) is paged with its own image, so these need only cover
; the oracle fixtures, not the spectrum4 corpus. Every append is bounds-checked
; against its _END bound (jp prep_fail on overflow), so an under-size is a loud
; error, never silent corruption.
SET_TAB_MAX:      equ 64
SET_TAB:          defs 4*64       ; {name_off:2, name_len:1, truthy:1}
SET_NAMES:        defs 512        ; name arena
SET_NAMES_END:                    ; (overflow guard bound)
IF_STACK:         defs 3*64       ; {active, taken, hadElse}
STRIP_BUF:        defs 512        ; stripTrailingComment scratch (>= max line)

; Macro table + pools (see the "Macro definition" section header).
MACRO_MAX:        equ 64
MACRO_TAB:        defs 12*64      ; {name_ptr:2,name_len:1,nparams:1,params_ptr:2,nbody:2,body_ptr:2,defline:2}
MACRO_EXPANDING:  defs 64         ; per-record recursion-cycle flag (== MACRO_MAX)
PARAM_IDX:        defs 4*128      ; {ptr:2 (into MACRO_STRPOOL), len:2}
PARAM_IDX_END:                    ; (overflow guard bound)
BODY_IDX:         defs 4*256      ; {ptr:2 (into PREP_SRC/PREP_FILES), len:2}
BODY_IDX_END:                     ; (overflow guard bound)
MACRO_STRPOOL:    defs 1024       ; copied param-name bytes
MACRO_STRPOOL_END:                ; (overflow guard bound)

; Macro-invocation buffers.
ARG_MAX:          equ 32
ARG_IDX:          defs 4*32       ; {ptr:2 (into STRIP_BUF), len:2} per call arg
SUBST_CAP:        equ 2048
SUBST_ARENA:      defs 2048       ; substituted-body bump stack (nested expansion)
SUBST_ARENA_END:                  ; (overflow guard bound)
SUBST_TOP:        defs 2          ; arena bump pointer

; .include buffers.
PATH_ARENA:       defs 512        ; persistent file-path strings (CURRENT_FILE)
PATH_ARENA_END:                   ; (overflow guard bound)
CAND_CAP:         equ 256
CAND_BUF:         defs 256        ; a resolved candidate path being tried

PREP_PATH:        defs 128        ; caller writes a NUL-terminated path
PREP_OUT_CAP:     equ 8192
                  if defined(ASMPREP_PAGED_BUFS)
; Paged build (i370): the big three buffers tile the section-A window (page 8) —
; exactly 16 KB (8 K + 4 K + 4 K) — leaving code + small buffers in section B.
; These are oracle-fixture sizes; the full spectrum4 corpus needs the i2 PP_PREP
; pool (deferred to b4).
PREP_OUT:         equ &0000       ; page-8 window (section A); 8 KB, ends &2000
PREP_SRC:         equ &2000       ; page-8 window; 4 KB, ends &3000
                  else
PREP_OUT:         defs 8192       ; expanded output
PREP_SRC:         defs 4096       ; caller writes source; BC = length
                  endif

; Include-file provider input (memory-backed harness reader). PREP_FILES holds
; PREP_NFILES entries, each [name_len:1][name][content_len:2][content]; the SAM
; build points READER_VEC elsewhere and leaves these empty. PREP_INCDIRS holds
; PREP_NINCDIRS entries, each [len:1][bytes] (the -I search path). Both are
; harness input, kept last; PREP_FILES must end below &FFFF (a file's content
; span is SRC_PTR..SRC_PTR+len, which must not wrap the address space).
PREP_NFILES:      defs 1
PREP_NINCDIRS:    defs 1
PREP_INCDIRS:     defs 256
                  if defined(ASMPREP_PAGED_BUFS)
PREP_FILES:       equ &3000       ; page-8 window (section A); 4 KB, ends &4000
                  else
PREP_FILES:       defs 4096
                  endif
