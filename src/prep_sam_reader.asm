; prep_sam_reader.asm — real SAM `.include` file reader for the on-SAM
; assembler-source preprocessor (i31b-b3b).
;
; Binds the preprocessor's pluggable reader-vector contract (src/asmprep.asm:
; REQ_NAME_* in, READ_CONTENT_* + CF out — the same ABI as prep_mem_reader) to
; the real SAM disk loader: map the resolved candidate to a SAM catalogue name,
; HGTHD to find it, then trampoline-HLOAD it (the load_in_file pattern,
; src/main_loop.asm) into a target physical page.
;
; A missing file must return CF=0 (so handleInclude's search loop falls through
; to the next candidate and ultimately to prep_fail — a clean assembler error),
; NOT longjmp to BASIC the way a bare HGTHD does on file-not-found. HGTHD's
; not-found path (samdos c.s:1357 `jp nz,rep26` → d.s:430 `derr`) restores SP to
; (entsp) and pops into BASIC (samdos-file-io.md:552); `(hksp)` is unavailable
; (the hook dispatcher zeroes it, samdos/src/b.s:451 — i185). The sanctioned
; recovery is the DOSER (&5BC0) epilogue: ROM PTDOS's DOSC dispatches `JP (DOSER)`
; after every hook with A = SAMDOS error number (0 = success), samdos-file-io.md:562.
; So we bracket the HGTHD with a temporary DOSER handler that converts a
; not-found (A≠0) into a clean CF=0 return, restoring the prior vector after.
;
; The bracket is UNINSTALLED after a successful HGTHD, before the HLOAD: HLOAD's
; trampoline remaps section C to the target page, so a section-C DOSER handler
; would be unmapped when HLOAD's DOSER dispatch fires (this is exactly why the
; assembler's persistent handler lives in section B). HGTHD keeps section C
; stable, so a section-C handler is safe for the not-found point; HLOAD can't be
; not-found once HGTHD found the file, so no handler is needed across it.
;
; The includer supplies (asmprep.asm in the real paged build, the reader driver
; in the b3b unit harness):
;   - REQ_NAME_PTR / REQ_NAME_LEN  (reader input: resolved candidate name)
;   - READ_CONTENT_PTR / READ_CONTENT_LEN  (reader output: file bytes)
;   - PREP_READER_PAGE  (equ: the physical page HLOAD lands the include in)
;   - the trampoline installed at TRAMPOLINE_DST (src/trampoline.asm)
;   - sam_io.inc (fill_uifa, UIFA, HOOK_HGTHD)
;
; v1 scope: include files fit in a single page (≤16 KB). A multi-page include is
; rejected as a miss until the paged PP_PREP buffer pool lands (i370); the
; oracle fixtures and the spectrum4 macro consumers are all well under a page.

PREP_DOSER_VEC:   equ     &5BC0          ; SAM DOSER post-hook dispatch sysvar

; ---------------------------------------------------------------------------
; prep_sam_reader — READER_VEC entry.
;   In:  REQ_NAME_PTR = ptr to the resolved candidate name; REQ_NAME_LEN < 256.
;   Out: CF=1 with READ_CONTENT_PTR (=&8000 window, target page mapped by the
;        caller) / READ_CONTENT_LEN on a hit; CF=0 on a miss (name unusable,
;        file not found, or multi-page in v1).
;   Preserves nothing beyond the documented outputs (mid-prep call; the
;   preprocessor re-reads its own state after each reader call).
; ---------------------------------------------------------------------------
prep_sam_reader:
                ; --- reject names that cannot be a SAM catalogue name ---
                ld      a, (REQ_NAME_LEN+1)
                or      a
                jp      nz, psr_miss            ; len ≥ 256
                ld      a, (REQ_NAME_LEN)
                or      a
                jp      z, psr_miss             ; empty name
                cp      11
                jp      nc, psr_miss            ; > 10 chars — not a 10.4 name

                ; --- build the 15-byte UIFA name block (type + 10 name + 4 ext) ---
                ; SAM CODE files carry a 10-char name and 4 space "ext" bytes
                ; (mirrors name_IN, main_loop.asm:1874). Copy the candidate
                ; verbatim, space-pad the name to 10, ext = 4 spaces. Names may
                ; contain '.', so `.include "utils.s"` → SAM file `UTILS.S`.
                ld      hl, PREP_NAME_BLK
                ld      (hl), 19                ; type 19 = code
                inc     hl                      ; -> name field (10 bytes)
                ld      de, (REQ_NAME_PTR)      ; DE = src
                ex      de, hl                  ; HL = src, DE = name field
                ld      a, (REQ_NAME_LEN)
                ld      b, a                    ; B = copy count (1..10)
                ld      c, a                    ; C = remember it for the pad
psr_copy:
                ld      a, (hl)
                ld      (de), a
                inc     hl
                inc     de
                djnz    psr_copy
                ld      a, 10
                sub     c                       ; A = 10 - copied (name padding)
                jr      z, psr_ext
                ld      b, a
psr_pad:
                ld      a, " "
                ld      (de), a
                inc     de
                djnz    psr_pad
psr_ext:
                ld      b, 4                    ; 4-space ext
psr_ext_pad:
                ld      a, " "
                ld      (de), a
                inc     de
                djnz    psr_ext_pad

                ; --- install the temporary DOSER not-found bracket ---
                ld      (PREP_READER_SP), sp    ; longjmp target for the handler
                ld      hl, (PREP_DOSER_VEC)
                ld      (PREP_DOSER_SAVE), hl
                ld      hl, prep_reader_doser
                ld      (PREP_DOSER_VEC), hl

                ; --- HGTHD: find the file + read its geometry ---
                ; Not-found → DOSER → prep_reader_doser → prep_reader_notfound (CF=0).
                ld      hl, PREP_NAME_BLK
                call    fill_uifa               ; UIFA populated, IX = UIFA
                rst     8
                defb    HOOK_HGTHD

                ; --- HGTHD succeeded: uninstall the bracket BEFORE the HLOAD ---
                ; (HLOAD remaps section C; its DOSER dispatch must not reach a
                ; now-unmapped section-C handler. It can't be not-found anyway.)
                ld      hl, (PREP_DOSER_SAVE)
                ld      (PREP_DOSER_VEC), hl

                ; --- decode geometry from DIFA (&4B50+34..36), as load_in_file ---
                ld      hl, (&4B50 + 35)
                ld      a, h
                and     &7F                     ; clear SAMDOS's `set 7,d` marker
                ld      h, a
                ld      (PREP_READ_LEN), hl     ; length-mod-16K
                ld      a, (&4B50 + 34)
                or      a
                jp      nz, psr_miss            ; pages > 0 → multi-page, v1 miss

                ; --- re-issue HGTHD (HLOAD consumes the armed sector state) ---
                ld      hl, PREP_NAME_BLK
                call    fill_uifa
                rst     8
                defb    HOOK_HGTHD

                ; --- trampoline-HLOAD the file into PREP_READER_PAGE ---
                ;   HL = &8000 (section-C window HLOAD requires)
                ;   B  = target physical page (trampoline sets HMPR = B)
                ;   C  = pages count (0 — single-page include)
                ;   DE = length-mod-16K
                ;   IX = UIFA (fill_uifa left it there)
                ld      hl, &8000
                ld      b, PREP_READER_PAGE
                ld      c, 0
                ld      de, (PREP_READ_LEN)
                di                              ; trampoline DI-entry contract
                call    TRAMPOLINE_DST

                ; --- success: content resident in PREP_READER_PAGE at &8000 ---
                ld      hl, &8000
                ld      (READ_CONTENT_PTR), hl
                ld      hl, (PREP_READ_LEN)
                ld      (READ_CONTENT_LEN), hl
                scf
                ret

psr_miss:
                ; A miss reached here only with the bracket already restored
                ; (the pre-HGTHD rejects) or never installed. Restore defensively
                ; is unnecessary; just return CF=0.
                or      a
                ret

; prep_reader_notfound — the not-found landing (reached from the DOSER handler
; after it restored SP). Restore the prior DOSER vector and return CF=0.
prep_reader_notfound:
                ld      hl, (PREP_DOSER_SAVE)
                ld      (PREP_DOSER_VEC), hl
                or      a                       ; CF=0
                ret

; prep_reader_doser — temporary DOSER handler. A = SAMDOS error number.
;   A==0 (success): return to prep_sam_reader immediately after the RST 8.
;   A≠0  (error):   longjmp back to the reader's not-found path.
; Lives in the includer's code page (section C for the driver / the prep window
; for i370); safe because it only ever fires for the HGTHD, which does not remap
; section C.
prep_reader_doser:
                and     a
                ret     z                       ; success → resume the reader
                ld      sp, (PREP_READER_SP)
                jp      prep_reader_notfound

; Reader-private state (the includer provides REQ_NAME_*/READ_CONTENT_*).
PREP_NAME_BLK:    defs    15                    ; UIFA name block: type + 10 + 4
PREP_DOSER_SAVE:  defs    2                     ; prior (&5BC0) across the bracket
PREP_READER_SP:   defs    2                     ; SP at the not-found longjmp target
PREP_READ_LEN:    defs    2                     ; decoded length-mod-16K
