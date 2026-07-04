; chain_paged_driver.asm — Main-image driver for the b8d brick-2 paged chain
; (i48c-b8j): text → parse_run → pass1_ir_walk → compact_ir_walk (skeleton
; INST arm), entirely on-Z80 over the paged arrangement.
;
; Built at org &8000 with CHAIN_PAGED_DRIVER=1, which adjusts the included
; harnesses for the paged context:
;
;   test_pass1_ir.asm: PASS1_IR_BUF is an equ alias of &0000 (the PARSE_RECS
;   page-8 window address) instead of the flat 10752-byte defs buffer; the IR is
;   read through the live section A window while LMPR=&28.
;
;   test_compact_ir.asm: COMPACT_LABELROWS/COMPACT_LOCALROWS relocate from the
;   flat low-RAM block (&5000/&5400 — inside the page-9 window at &4000-&7FFF
;   when LMPR=&28) into page-8 spare (&1200/&1500 — after PARSE_RECS &0000,
;   LEX_SRC &0800, SYM_NAMES &1000).  The walk writes them via section A;
;   the Go test reads them directly from pager.RAM[8].
;
; The parser image (build/asmparse_paged.bin) lives in physical pages 8 and 9.
; LMPR=&28 maps them to sections A and B:
;   Section A (&0000-&3FFF) = physical page 8: PARSE_RECS(&0000), LEX_SRC(&0800), SYM_NAMES(&1000)
;   Section B (&4000-&7FFF) = physical page 9: lexer+parser code + LEX_TOKS + LEX_STRPOOL
;
; Cross-image symbols (parse_run, PARSE_RECPTR, PARSE_ERR, PARSE_RECS) resolve
; via --importfile=build/asmparse_paged.sym (the same .sym the b8i driver uses).
;
; The pass1 and compact walks run from this main image (section C/D via HMPR)
; with LMPR=&28 kept live throughout both walks, so the IR at &0000 is always
; readable through section A.  Section C (&8000-&BFFF) and section D
; (&C000-&FFFF) are controlled by HMPR and are unaffected by LMPR changes —
; the pass-1 leaf tables at &C100-&EFFF and the compact state at &F000-&FFFF
; are stable for the entire run.
;
; The harness (koron-go/z80) plants SP=&6FFE and a HALT trap at &7000 in the
; boot-mapped section B (page 2 at FlatLMPR=&21).  An LMPR switch replaces
; page 2 with page 9, hiding both; the driver must save the boot SP, relocate
; to the HMPR-stable section D stack at &C0FE, do the paged work, then restore
; SP before RET (mirrors the b8i driver pattern exactly).

                org     &8000

                include "test_compact_ir.asm"   ; includes test_pass1_ir.asm


; ===========================================================================
; b8j_chain_paged — drive parse → pass1 → compact walk via LMPR=&28.
;
; Entry: BC = source byte length (source already written to physical page 8
;             at offset &0800 = LEX_SRC by the Go test).
; Exit:  Compact outputs in COMPACT_SIDECAR / COMPACT_GLOBALS / COMPACT_RECS
;        (section D, HMPR-stable; readable via mac.Read after RET) and
;        COMPACT_LABELROWS / COMPACT_LOCALROWS (physical page 8 at &1200/&1500;
;        readable via pager.RAM[8] after RET — NOT via mac.Read, which maps
;        the restored section A = page 1, not page 8).
;        LMPR and SP restored to their entry values.
; On parse error: hits the fail trap with tag &f1 (PARSE_ERR non-zero).
; ===========================================================================
b8j_chain_paged:
                ; --- Phase 0: save boot state, relocate stack ---
                in      a, (&fa)
                ld      (B8J_SAVED_LMPR), a

                ld      hl, 0
                add     hl, sp
                ld      (B8J_SAVED_SP), hl

                ld      sp, &C0FE               ; move stack to section D (HMPR-stable)

                ; --- Phase 1: switch to parser window, run parse_run ---
                ld      a, &28                  ; LMPR=&28: secA=page8 (&0000), secB=page9 (&4000)
                out     (&fa), a

                call    parse_run               ; BC in = src len; BC out = record count

                ; Check parse error flag (in page9, readable via section B)
                ld      a, (PARSE_ERR)
                or      a
                jr      nz, b8j_parse_err

                ; PASS1_IR_LEN := PARSE_RECPTR - PARSE_RECS
                ; PARSE_RECS = equ &0000 = PASS1_IR_BUF, so length = PARSE_RECPTR directly.
                ld      hl, (PARSE_RECPTR)
                ld      de, PARSE_RECS          ; &0000
                or      a                       ; clear CF
                sbc     hl, de
                ld      (PASS1_IR_LEN), hl      ; write into main-image cell before walk

                ; --- Phase 2: pass1 + compact (LMPR=&28 kept live throughout) ---
                call    compact_ir_walk         ; reads IR from &0000, writes state to &F000+

                ; --- Phase 3: restore boot state ---
b8j_restore:
                ld      a, (B8J_SAVED_LMPR)
                out     (&fa), a                ; restore LMPR (section A back to page 1)

                ld      hl, (B8J_SAVED_SP)
                ld      sp, hl                  ; restore SP (&6FFE) so RET pops haltTrap (&7000)

                ret

b8j_parse_err:
                ld      a, &f1
                jp      fail_with_tag           ; tag &f1: PARSE_ERR non-zero in chain driver


; ===========================================================================
; Driver state cells (section C, HMPR-stable across the LMPR window).
; ===========================================================================
B8J_SAVED_LMPR: defs 1          ; boot LMPR saved across the window
B8J_SAVED_SP:   defs 2          ; boot SP saved across the window


; ===========================================================================
; b8d additions — COMPACT_WALK_REAL_ENCODER=1 only.
;
; When built with -D COMPACT_WALK_REAL_ENCODER=1 the chain extends from the
; skeleton compact walk (b8j) to the full encoder pipeline:
;   parse → pass1 → compact_ir_walk (real cemit_add_inst INST arm)
;       → compact_serialize → compact .tbn output
;
; Additional includes (encoder, serializer) and the b8d_chain_paged entry
; point are gated here so the b8j binary is unaffected.
; ===========================================================================
if defined(COMPACT_WALK_REAL_ENCODER)

; ENCTAB paging constants (normally in src/trampoline.asm, not included here).
ENCTAB_BASE:            equ     &0000           ; ENCTAB base address in section A
LMPR_ENCTAB:            equ     &24             ; LMPR value: section A = page 4 (ENCTAB)

; Diagnostic cell — stores a context pointer on loud fails (compact_emit.asm).
LAST_FAIL_PC:           defw    0

; compact_walk_inst temporaries (emitted here rather than in test_compact_ir.asm
; to keep the b8b/b8j image lean; these are only needed for REAL_ENCODER builds).
cwi_mnem_id:    defw    0               ; mnemonic id saved across LMPR bracket
cwi_op_count:   defb    0               ; operand count saved across LMPR bracket
cwi_ops_len:    defw    0               ; operand-stream byte length

; OPVAL_STRIDE (from encoder.asm, not included here): the operand-value
; record stride used by the slot encoders.
OPVAL_STRIDE:           equ     10

; Slot encoders (same 11 files and order as src/assembler.asm).
                include "slots/xreg.asm"
                include "slots/imm_small.asm"
                include "slots/imm12_shifted.asm"
                include "slots/imm16_shifted.asm"
                include "slots/extend_op.asm"
                include "slots/branch_imm.asm"
                include "slots/adrp_imm.asm"
                include "slots/logical_imm.asm"
                include "slots/shifted_reg.asm"
                include "slots/extended_reg.asm"
                include "slots/mem.asm"

; ENCTAB form-table lookup (needed by encode_inst, called from compact_inst).
                include "form_lookup.asm"

; paged_call and SYSREG constants (mirrors trampoline.asm, not included here).
; paged_call lives at &7E40 in section B; in the b8d chain, section B = page 9
; (LMPR=&28), so the body is installed there at b8d_chain_paged startup.
TRAMPOLINE_DST:         equ     &7E00
PAGED_CALL_DST:         equ     TRAMPOLINE_DST + &40    ; = &7E40
PAGED_CALL_HMPR_SAVE:   equ     TRAMPOLINE_DST + &D0    ; = &7ED0
PAGED_CALL_SP_SAVE:     equ     TRAMPOLINE_DST + &D1    ; = &7ED1
TRAMP_SAFE_SP:          equ     TRAMPOLINE_DST + 256    ; = &7F00
paged_call:             equ     PAGED_CALL_DST

SYSREG_DATA_PAGE:       equ     13
SYSREG_SYSREG_ENTRY:    equ     &8000
SYSREG_PSTATE_ENTRY:    equ     &8008
SYSREG_DC_ENTRY:        equ     &8010
SYSREG_TLBI_ENTRY:      equ     &8018
SYSREG_COMM_NAME:       equ     TRAMPOLINE_DST + &80    ; = &7E80, 16 bytes
SYSREG_COMM_LEN:        equ     TRAMPOLINE_DST + &90    ; = &7E90, 1 byte
SYSREG_COMM_RESULT:     equ     TRAMPOLINE_DST + &91    ; = &7E91, up to 8 bytes

; LMPR save slot used by sysname.asm's paged_call bracket (mirrors trampoline.asm).
LMPR_DEFAULT_RUNTIME:   defb    0

; paged_call body bytes — LDIR'd into page-9 section B at b8d_chain_paged startup.
                include "paged_bodies.asm"

; System-name encoders (encode_mrs_word etc.) via the real page-13 sysreg lookup.
; sysname.asm calls paged_call at PAGED_CALL_DST (&7E40 in section B = page 9
; when LMPR=&28) and reads results from SYSREG_COMM_RESULT there.
                include "sysname.asm"

; insn_run.asm in fold-only mode: FSID_* constants + insn_fold helpers only;
; main_handle_insn_run (depends on walk_records/emit_byte) is excluded.
; INSN_RUN_FOLD_ONLY=1 is passed as a -D flag by the b8d Makefile target.
                include "insn_run.asm"

; Core encoder + overlay classifier.
                include "insn_encode.asm"
                include "insn_overlay.asm"

; compact_emit.asm with external CEMIT_ELEMS/CEMIT_ELEMS_END (defined by the
; COMPACT_WALK_REAL_ENCODER block in test_compact_ir.asm's memory map).
; CEMIT_BUFS_EXTERNAL=1 is passed as a -D flag by the b8d Makefile target.
                include "compact_emit.asm"

; compact_serialize.asm emits code into the binary. In the paged chain driver,
; compact_ir_walk internally calls pass1_ir_walk, which initialises the SYMTAB
; (symbol_table_init: 256 × 8-byte hash buckets at &C160-&C95F) by writing
; 0xFF to bytes 0-1 of each bucket. Any b8d or serialiser code assembled into
; &C160-&C95F would be overwritten. Skip to &C9A4 (first free byte after
; SYMTAB + SYMTAB_ABS_BITMAP per assembler.asm §memory map) before emitting
; the serialiser. The gap bytes land in the binary but the SYMTAB init
; overwrites them at runtime; the gap is harmless dead space.
if $ <= &C9A4
                defs    &C9A4 - $
endif

; .tbn serializer.
                include "compact_serialize.asm"


; ---------------------------------------------------------------------------
; Page-8 layout for b8d (accessed via section A when LMPR=&28).
; Picking up after COMPACT_RECS ends at &2540:
;   &2B00: names wire buffer (SYM_NAMES converted to ser_names format)
;   &2F00: .tbn output (up to &3FFF = 4352 B)
; ---------------------------------------------------------------------------
B8D_NAMES_WIRE:         equ     &2B00           ; [count:2 LE][len:1][bytes]* per name
B8D_NAMES_WIRE_END:     equ     &2F00           ; one past (1024 B — more than SYM_NAMES)
B8D_SER_OUT:            equ     &2F00           ; .tbn output base (page-8 address)
B8D_SER_OUT_END:        equ     &3FFF           ; one past (last usable byte)


; ---------------------------------------------------------------------------
; b8d_build_names_wire — convert SYM_NAMES (page-8 offset &1000, format
; [len:1][bytes]*[0]) to the ser_names format ([count:2 LE][len:1][bytes]*)
; and store the result at B8D_NAMES_WIRE (also in page 8).  Must be called
; with LMPR=&28 so section A = page 8.
; Clobbers: A, BC, DE, HL.
; ---------------------------------------------------------------------------
b8d_build_names_wire:
                ld      hl, SYM_NAMES               ; &1000 in page 8 via section A
                ld      de, B8D_NAMES_WIRE + 2      ; dest: skip [count:2] placeholder
                ld      bc, 0                        ; BC = name count
bnw_loop:
                ld      a, (hl)                      ; len byte (0 = end of names list)
                or      a
                jr      z, bnw_done
                ld      (de), a                      ; copy len byte
                inc     de
                push    bc
                ld      c, a
                ld      b, 0                         ; BC = len (bytes to copy)
bnw_copy:
                inc     hl
                ld      a, (hl)
                ld      (de), a
                inc     de
                dec     bc
                ld      a, b
                or      c
                jr      nz, bnw_copy
                inc     hl                           ; advance past last name byte → next len
                pop     bc
                inc     bc                           ; count++
                jr      bnw_loop
bnw_done:
                ld      hl, B8D_NAMES_WIRE
                ld      (hl), c                      ; count lo
                inc     hl
                ld      (hl), b                      ; count hi
                ret


; ---------------------------------------------------------------------------
; b8d_setup_serialize — populate the compact_serialize parameter cells from
; the compact walk outputs and b8d layout constants.  Must be called with
; LMPR=&28.  Clobbers: HL.
; ---------------------------------------------------------------------------
b8d_setup_serialize:
                ld      hl, COMPACT_LABELROWS
                ld      (ser_labels_ptr), hl
                ld      hl, (compact_labelrows_count)
                ld      (ser_labels_count), hl

                ld      hl, COMPACT_LOCALROWS
                ld      (ser_locals_ptr), hl
                ld      hl, (compact_localrows_count)
                ld      (ser_locals_count), hl

                ld      hl, COMPACT_RECS
                ld      (ser_recs_ptr), hl
                ld      hl, (compact_recs_len)
                ld      (ser_recs_len), hl

                ld      hl, B8D_NAMES_WIRE
                ld      (ser_names_ptr), hl

                ld      hl, COMPACT_GLOBALS
                ld      (ser_globals_ptr), hl
                ld      hl, (compact_globals_count)
                ld      (ser_globals_count), hl

                ld      hl, COMPACT_SIDECAR
                ld      (ser_sidecar_ptr), hl
                ld      hl, (compact_sidecar_count)
                ld      (ser_sidecar_count), hl

                ld      hl, B8D_SER_OUT
                ld      (ser_out_base), hl
                ld      hl, B8D_SER_OUT_END
                ld      (ser_out_end), hl
                ret


; ===========================================================================
; b8d_chain_paged — drive parse → pass1 → compact_ir_walk (real encoder) →
; compact_serialize → .tbn output.
;
; Entry: BC = source byte length (source written to page 8 at &0800 = LEX_SRC).
; Exit:  .tbn bytes at B8D_SER_OUT (page-8 address &2F00), length in
;        ser_out_len (section C, readable via mac.Read or pager.RAM[8]).
;        LMPR and SP restored to entry values.
; On error: hits the fail trap (tag &f1 parse error, or &cd/&d0-&d3 encoder/
;           serializer overflow).
; ===========================================================================
b8d_chain_paged:
                ; Phase 0: save boot state, relocate stack.
                in      a, (&fa)
                ld      (B8D_SAVED_LMPR), a
                ld      hl, 0
                add     hl, sp
                ld      (B8D_SAVED_SP), hl
                ld      sp, &C0FE               ; section D stack (HMPR-stable)

                ; Phase 1: parser window, sysreg setup → parse_run.
                ; Set LMPR=&28 (section B = page 9), install the paged_call body
                ; there, and record LMPR_DEFAULT_RUNTIME so sysname lookups can
                ; bracket the encode window (sysname.asm:sysname_lmpr_default_in).
                ld      a, &28                  ; LMPR=&28: secA=page8, secB=page9
                out     (&fa), a
                ld      (LMPR_DEFAULT_RUNTIME), a       ; = &28 for sysname bracket
                push    bc                      ; save source length (LDIR clobbers BC)
                ld      hl, paged_call_body
                ld      de, PAGED_CALL_DST      ; &7E40 in section B (= page 9)
                ld      bc, paged_call_body_end - paged_call_body
                ldir                            ; install paged_call in page 9
                pop     bc                      ; restore source length
                call    parse_run               ; BC = src len in; BC = rec count out
                ld      a, (PARSE_ERR)
                or      a
                jr      nz, b8d_parse_err

                ; PASS1_IR_LEN = PARSE_RECPTR - PARSE_RECS.
                ld      hl, (PARSE_RECPTR)
                ld      de, PARSE_RECS          ; &0000
                or      a
                sbc     hl, de
                ld      (PASS1_IR_LEN), hl

                ; Phase 2: form_lookup_init (needs ENCTAB in section A).
                ld      a, LMPR_ENCTAB          ; &24
                out     (&fa), a
                call    form_lookup_init
                ld      a, &28
                out     (&fa), a                ; restore LMPR=&28

                ; Phase 3: compact walk with real encoder (cemit_add_inst per INST).
                call    compact_ir_walk

                ; Phase 4: build names wire (SYM_NAMES → ser_names format).
                call    b8d_build_names_wire

                ; Phase 5: set up compact_serialize parameter cells.
                call    b8d_setup_serialize

                ; Phase 6: serialize to .tbn.
                call    compact_serialize

                ; Phase 7: restore boot state.
b8d_restore:
                ld      a, (B8D_SAVED_LMPR)
                out     (&fa), a
                ld      hl, (B8D_SAVED_SP)
                ld      sp, hl
                ret

b8d_parse_err:
                ld      a, &f1
                jp      fail_with_tag

; b8d driver state cells (section C, HMPR-stable).
B8D_SAVED_LMPR: defs 1
B8D_SAVED_SP:   defs 2


if defined(PREP_CHAIN_DRIVER)
; ===========================================================================
; prep_chain_paged — run the preprocessor in front of the b8d assemble chain
; (i31b-b4b), entirely on-Z80 over the paged arrangement.
;
; This extends the b8d two-image pattern with a THIRD window: the prep window
; (build/asmprep_paged.bin) lives in physical pages 10 (section A = the PREP
; buffers PREP_OUT/PREP_SRC/PREP_FILES) and 11 (section B = prep code), reached
; via LMPR=&2A.  The parser window (build/asmparse_paged.bin) stays in pages 8/9
; (LMPR=&28); ENCTAB in page 4 (LMPR=&24).  This combined driver runs in section
; C/D (HMPR=5) and imports symbols from BOTH window .syms
; (--importfile asmprep_paged.sym + asmparse_paged.sym): prep_run/PREP_ERR/
; PREP_OUT from the prep window, parse_run/LEX_SRC/... from the parser window.
;
; Flow:  source text (planted at page-10 PREP_SRC) -> prep_run -> expanded text
;        at page-10 PREP_OUT -> copied to page-8 LEX_SRC -> b8d_chain_paged ->
;        .tbn at page-8 B8D_SER_OUT.
;
; Entry: BC = source byte length (source at page-10 PREP_SRC = &2000; the
;        memory reader's PREP_FILES/PREP_PATH/... pre-planted by the harness).
; Exit:  expanded text left resident at page-10 PREP_OUT (&0000), length in
;        PC_EXP_LEN (so the harness can gate expanded-text byte-equality without
;        re-reading it from LEX_SRC, which parse_run leaves intact but page-10 is
;        untouched by the chain); .tbn at page-8 B8D_SER_OUT (&2F00), length in
;        ser_out_len.  LMPR and SP restored to entry values.
; On error: prep error -> fail tag &e0; expanded text over the LEX_SRC window
;        ceiling -> fail tag &e1 (explicit, never a silent truncation).
;
; The expanded text is copied page-10 -> page-8 through a staging buffer in the
; free section-D gap between this driver's footprint (code + state end ~&CEDD)
; and the b8d compact buffers (COMPACT_SIDECAR=&F200 upward) — page 6, HMPR-stable
; and unused during the prep phase (b8d has not run yet).  The prep phase uses a
; deep stack in that same gap since prep_run recurses through macro/include
; expansion; the b8d chain's shallow &C0FE stack would risk the section-C driver
; code below &C000.
; ===========================================================================
PC_PREP_LMPR:   equ &2A          ; secA=page10 (PREP bufs), secB=page11 (prep code)
PC_STAGING:     equ &D000        ; handoff staging (section-D gap, 2 KB to &D800)
PC_PREP_SP:     equ &F000        ; prep-phase stack top (grows down to &D800, ~6 KB)
PC_LEXSRC_CAP:  equ 2048         ; LEX_SRC window size (expanded-text ceiling)

prep_chain_paged:
                ; --- prep phase: save boot state, relocate to a deep stack ---
                in      a, (&fa)
                ld      (PC_SAVED_LMPR), a
                ld      hl, 0
                add     hl, sp
                ld      (PC_SAVED_SP), hl
                ld      sp, PC_PREP_SP

                ; run prep in its window (BC = source length on entry)
                ld      a, PC_PREP_LMPR
                out     (&fa), a
                call    prep_run                ; BC in = src len; BC out = expanded len
                ld      a, (PREP_ERR)           ; page-11 (section B), mapped now
                or      a
                jr      nz, pc_prep_err

                ; expanded length; over-cap check vs the LEX_SRC window
                ld      (PC_EXP_LEN), bc
                ld      hl, PC_LEXSRC_CAP
                or      a
                sbc     hl, bc                  ; CAP - len; CF if len > CAP
                jr      c, pc_overcap
                ld      a, b                    ; empty output? skip (LDIR BC=0 = 64K)
                or      c
                jr      z, pc_handoff_done

                ; --- handoff copy 1: PREP_OUT (page-10 &0000) -> staging ---
                ld      hl, PREP_OUT            ; &0000 (section A = page 10)
                ld      de, PC_STAGING          ; &D000 (section D = page 6)
                ldir                            ; BC still = expanded len

                ; --- handoff copy 2: staging -> LEX_SRC (page-8 &0800) ---
                ld      a, &28                  ; LMPR=&28: secA=page8
                out     (&fa), a
                ld      hl, PC_STAGING
                ld      de, LEX_SRC             ; &0800 (section A = page 8)
                ld      bc, (PC_EXP_LEN)
                ldir
pc_handoff_done:
                ; --- close the prep bracket ---
                ld      a, (PC_SAVED_LMPR)
                out     (&fa), a
                ld      hl, (PC_SAVED_SP)
                ld      sp, hl

                ; --- chain phase: source now at page-8 LEX_SRC (b8d owns its
                ; own LMPR/SP bracket) ---
                ld      bc, (PC_EXP_LEN)
                call    b8d_chain_paged
                ret

pc_prep_err:
                ld      a, (PC_SAVED_LMPR)
                out     (&fa), a
                ld      hl, (PC_SAVED_SP)
                ld      sp, hl
                ld      a, &e0
                jp      fail_with_tag
pc_overcap:
                ld      a, (PC_SAVED_LMPR)
                out     (&fa), a
                ld      hl, (PC_SAVED_SP)
                ld      sp, hl
                ld      a, &e1
                jp      fail_with_tag

; prep_chain driver state cells (section C, HMPR-stable).
PC_SAVED_LMPR:  defs 1
PC_SAVED_SP:    defs 2
PC_EXP_LEN:     defs 2
endif   ; defined(PREP_CHAIN_DRIVER)

endif   ; defined(COMPACT_WALK_REAL_ENCODER)
