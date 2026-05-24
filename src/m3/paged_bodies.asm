; paged_bodies.asm — section-B body sources for paged_call /
; paged_data_map_hmpr / paged_data_unmap_hmpr.
;
; Per docs/notes/2026-05-28-paged-call-architecture.md plan-PR 1.
;
; PLACEMENT NOTE — load-bearing
; -----------------------------
;
; Included at the VERY END of src/m3/assembler.asm (after the
; BUILD_TESTS test_*.asm includes).  The bodies are not executed
; from their source location in section C; enctab_trampoline_setup
; LDIRs them into section B at boot.  Their SOURCE position is
; therefore irrelevant for runtime correctness — but it IS relevant
; for the addresses of subsequent labels (specifically the BUILD_TESTS
; storage bytes `boot_hmpr`, `lmpr_save_test`,
; `reader_paged_lmpr_save`, etc.) which are sensitive to total
; section-C source size.
;
; The first attempt at plan-PR 1 placed these bodies inline at the
; top of trampoline.asm (right after trampoline_body).  That shifted
; test-data labels deeper into the SP=&C100 stack-growth zone — far
; enough that pre-load self-tests' pushes overwrote `boot_hmpr`,
; which broke the post-load `run_trampoline_self_tests` assertion
; and caused the test variant to hang.  See
; docs/notes/2026-05-28-plan-pr1-stuck.md "Issue 3" for the diagnosis.
;
; Placing the bodies at the END of the section-C source keeps the
; test-data label addresses byte-identical to the pre-plan-PR-1 layout,
; sidestepping the stack-overlap regression.
;
; Forward-reference invariants:
;   - trampoline.asm declares the EQU constants PAGED_CALL_DST etc.
;     using `(paged_call_body - trampoline_body)` etc.  pyz80 resolves
;     these in pass 2 once both labels are known; the bodies being
;     later in source than the EQU is fine.
;   - enctab_trampoline_setup's LDIR uses
;     `paged_data_unmap_body_end - trampoline_body` as the byte count.
;     pyz80 resolves this same-pass-2 way.
;   - paged_call_body emits absolute references to
;     PAGED_CALL_HMPR_SAVE / PAGED_CALL_SP_SAVE /
;     TRAMP_SAFE_SP / paged_call_trailer_dst.  All four are EQUs
;     defined in trampoline.asm; pyz80 inlines them at assembly time.


; -----------------------------------------------------------------------
; paged_call_body — bytes that get LDIR'd to PAGED_CALL_DST in section B
; (by enctab_trampoline_setup's combined LDIR — see trampoline.asm).
;
; CALLER does NOT call this label; they CALL PAGED_CALL (= the section-B
; copy at PAGED_CALL_DST).  All absolute addresses baked into the body
; refer to section-B slots (PAGED_CALL_HMPR_SAVE, PAGED_CALL_SP_SAVE,
; paged_call_trailer_dst), so the body is position-correct once LDIR'd
; to PAGED_CALL_DST.
;
; Call site shape (6 bytes per site):
;       CALL    paged_call
;       DEFW    target_addr_in_C       ; target's address in &8000..&BFFF
;       DEFB    target_page            ; physical page number (low 5 bits)
;       ; control resumes here after the target's RET
;
; Mechanism (per architecture doc §3.3):
;   1. Save SP BEFORE the pop, so trailer's RET reads from the right
;      stack slot.  (The architecture doc's §3.3 pseudocode saves
;      AFTER the pop — that's a bug, fixed here.  See
;      docs/notes/2026-05-28-plan-pr1-stuck.md "Issue 1".)
;   2. Pop the post-CALL return address — it points at the inline DEFW.
;      The pop's SP-increment is fine because we already saved the
;      pre-pop value.
;   3. Read DEFW target, DEFB target_page from the inline payload;
;      advance HL past it (HL = real post-payload return address).
;   4. PUSH HL back to caller's stack — overwriting the original
;      return-after-CALL address with the post-payload return.  The
;      trailer's final RET reads from this slot to return to caller.
;   5. Switch SP to TRAMP_SAFE_SP (section-B-stable).  Push trailer
;      address, push target.  Combine HMPR's CLUT bits with target
;      page bits.  OUT (251) → HMPR := target.  RET → target.
;   6. Target runs from section C in target page.  Target's RET pops
;      trailer (in section B, fetched stably across the HMPR change).
;   7. Trailer restores HMPR (full byte, CLUT preserved) and SP,
;      then RETs to the caller's post-payload return address.
;
; Clobbers: A, HL, DE, F.  Preserves BC, IX, IY — caller passes target
; arguments in those registers.
;
; Not re-entrant: paged_call_hmpr_save / paged_call_sp_save are static
; one-deep slots.  Targets must not paged_call.
paged_call_body:
                di
                ld      (PAGED_CALL_SP_SAVE), sp        ; SP = caller_SP - 2
                                                        ; (post-CALL hardware push state)
                in      a, (251)                        ; A = entry HMPR
                ld      (PAGED_CALL_HMPR_SAVE), a
                pop     hl                              ; HL → inline payload;
                                                        ; SP = caller_SP

                ld      e, (hl)                         ; target lo
                inc     hl
                ld      d, (hl)                         ; target hi (DE = target)
                inc     hl
                ld      a, (hl)                         ; A = target page number
                inc     hl                              ; HL = post-payload return

                ; Rewrite caller's return-addr slot with the real
                ; post-payload return.  push hl decrements SP by 2
                ; (back to caller_SP - 2 = PAGED_CALL_SP_SAVE) and
                ; writes HL — overwriting the original (now stale)
                ; return-after-CALL pointer that pointed at the DEFW.
                push    hl                              ; SP := caller_SP - 2;
                                                        ; mem[SP..+1] := post-payload-return

                ld      sp, TRAMP_SAFE_SP               ; section-B-stable SP

                ld      hl, paged_call_trailer_dst
                push    hl                              ; target's RET lands here
                push    de                              ; target → popped by final RET

                ; HMPR mask discipline (architecture doc §3.3, §7 risk #8):
                ; caller-supplied A holds only the page number; OR in
                ; the saved HMPR's CLUT + ext-mem bits before writing.
                and     %00011111                       ; keep page bits
                ld      e, a
                ld      a, (PAGED_CALL_HMPR_SAVE)
                and     %11100000                       ; keep CLUT + ext-mem
                or      e                               ; combine
                out     (251), a                        ; HMPR := target page
                ret                                     ; → target

paged_call_trailer:
                ; LIVES IN SECTION B at (PAGED_CALL_DST + (this - paged_call_body)).
                ; Fetched stably across the HMPR change because section B is
                ; LMPR-controlled, not HMPR-controlled.
                ld      a, (PAGED_CALL_HMPR_SAVE)
                out     (251), a                        ; HMPR restored (full byte)
                ld      sp, (PAGED_CALL_SP_SAVE)        ; SP = caller_SP - 2;
                                                        ; mem[SP..+1] = post-payload-return
                ; NB: don't EI — caller chose DI state; the M3 assembler runs
                ; DI throughout, so leaving DI matches the caller's invariant.
                ret                                     ; → caller's post-payload return

; paged_call_trailer_dst — the absolute SECTION-B address of the trailer
; AFTER the LDIR copy.  paged_call_body is LDIR'd to PAGED_CALL_DST,
; so the trailer (its source offset relative to paged_call_body) lands
; at PAGED_CALL_DST + that offset.
paged_call_trailer_dst:   equ   PAGED_CALL_DST + (paged_call_trailer - paged_call_body)


; -----------------------------------------------------------------------
; paged_data_map_body — LDIR'd to PAGED_DATA_MAP_DST by the combined
; setup in enctab_trampoline_setup.
;
; CALL PAGED_DATA_MAP_HMPR with A = target page (low 5 bits).  Saves
; the entry HMPR (to PAGED_DATA_HMPR_SAVE in section B), masks A to bits
; 0-4, OR-s in the entry HMPR's top 3 bits (CLUT + ext-mem
; preservation), and sets HMPR.  Section C/D = target page after this
; helper returns.
;
; Input:   A = target page (low 5 bits used).
; Output:  HMPR = (entry_HMPR & %11100000) | (A & %00011111).
;          PAGED_DATA_HMPR_SAVE = entry HMPR (for later paged_data_unmap).
; Clobbers: A, B, F.  Preserves C, DE, HL, IX, IY.  Disables interrupts.
;
; KNOWN LIMITATION: caller code must be in LMPR-stable memory
; (section A or B).  Callers in section C or D will crash on the
; helper's RET because the section-D return-address slot is now in a
; different physical page after the HMPR change.  See
; docs/notes/2026-05-28-plan-pr1-stuck.md "Issue 2".
paged_data_map_body:
                di
                and     %00011111                       ; mask caller A → page bits
                ld      b, a                            ; B = page bits
                in      a, (251)                        ; A = entry HMPR
                ld      (PAGED_DATA_HMPR_SAVE), a       ; save entry HMPR for unmap
                and     %11100000                       ; A = CLUT + ext-mem bits
                or      b                               ; A = combined HMPR
                out     (251), a                        ; HMPR := combined
                ret


; -----------------------------------------------------------------------
; paged_data_unmap_body — LDIR'd to PAGED_DATA_UNMAP_DST.
;
; CALL PAGED_DATA_UNMAP_HMPR to restore HMPR to the value saved by the
; matching paged_data_map_hmpr.  Pairs once-for-once with map.
;
; Input:   none (reads PAGED_DATA_HMPR_SAVE).
; Output:  HMPR = the entry-time value saved by paged_data_map_hmpr.
; Clobbers: A, F.  Preserves BC, DE, HL, IX, IY.
paged_data_unmap_body:
                ld      a, (PAGED_DATA_HMPR_SAVE)
                out     (251), a
                ret
paged_data_unmap_body_end:
