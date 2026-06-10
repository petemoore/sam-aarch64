; paged_bodies.asm — section-B body source for paged_call.
;
; Per https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/notes/2026-05-28-paged-call-architecture.md plan-PR 1 of §6.
;
; The body is NOT executed from its source location in section C; the
; bytes are LDIR'd into section B at boot by enctab_trampoline_setup
; (see trampoline.asm).  Source position is therefore irrelevant for
; runtime correctness; the body's absolute references all target
; section-B addresses (PAGED_CALL_HMPR_SAVE, PAGED_CALL_SP_SAVE,
; TRAMP_SAFE_SP, paged_call_trailer_dst) so the LDIR'd copy is
; position-correct.
;
; Forward-reference invariants:
;   - trampoline.asm declares the EQU constants PAGED_CALL_DST etc.
;     and the static save slots.  pyz80 resolves the absolute
;     addresses baked into this body at assembly time.
;   - enctab_trampoline_setup's LDIR uses
;     `paged_call_body_end - paged_call_body` as the byte count.
;
; KEEP-OUT NOTE — paged_data_map_hmpr / paged_data_unmap_hmpr (the
; split-bracket primitives from architecture doc §4.2) were
; investigated as part of the original draft of this PR and removed:
; the split pattern cannot be safely called from code in section C/D
; (the caller's next-instruction fetch after the bracket-open CALL
; returns lands in the remapped section).  For paged-data access from
; section-C/D callers, the right shape is a `paged_call` to a target
; routine in the data page that does its work and returns; that's
; what the SAM ROM / COMET prior art does.  See
; https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/notes/2026-05-28-paged-call-architecture.md §4 (post-salvage
; revision) and https://github.com/petemoore/sam-aarch64/blob/c0f62fa/docs/notes/archive/2026-05-28-session-handoff.md "Critical
; read of PR #50" point #2.


; -----------------------------------------------------------------------
; paged_call_body — bytes that get LDIR'd to PAGED_CALL_DST in section B
; (by enctab_trampoline_setup, see trampoline.asm).
;
; CALLERS do NOT call this label; they CALL `paged_call` (= the
; section-B copy at PAGED_CALL_DST).  All absolute addresses baked
; into the body refer to section-B slots (PAGED_CALL_HMPR_SAVE,
; PAGED_CALL_SP_SAVE, TRAMP_SAFE_SP, paged_call_trailer_dst), so the
; body is position-correct once LDIR'd to PAGED_CALL_DST.
;
; Call site shape (6 bytes per site):
;       call    paged_call
;       defw    target_addr_in_C       ; target's address in &8000..&BFFF
;       defb    target_page            ; physical page number (low 5 bits)
;       ; control resumes here after the target's RET
;
; Mechanism (architecture doc §3.3, with the trailer SP-save ordering
; corrected per the original PR-#50 investigation — see the §3.3
; editorial patch landed in this PR):
;
;   1. Save SP **BEFORE** the pop, so the trailer's RET reads from the
;      right stack slot.  (The architecture doc's earlier §3.3
;      pseudocode saved AFTER the pop — that's wrong: pop has already
;      incremented SP by 2, so the saved value is caller_SP, not
;      caller_SP - 2; the trailer's later `ld sp, (save)` puts SP one
;      slot above the return address, and `ret` then reads garbage.)
;   2. Pop the post-CALL return address — it points at the inline
;      DEFW.  SP is now caller_SP.
;   3. Read target_addr from the inline DEFW, target_page from the
;      DEFB; advance HL past it (HL = real post-payload return).
;   4. PUSH HL back to caller's stack — overwriting the original
;      return-after-CALL address with the post-payload return.  The
;      trailer's final RET reads from this slot.  SP back to
;      caller_SP - 2.
;   5. Switch SP to TRAMP_SAFE_SP (section-B-stable).  Push trailer
;      address, push target.  Combine HMPR's CLUT bits with target
;      page bits.  OUT (251) -> HMPR := target.  RET -> target.
;   6. Target runs from section C in target page.  Target's RET pops
;      trailer (in section B, fetched stably across the HMPR change).
;   7. Trailer restores HMPR (full byte, CLUT preserved) and SP,
;      then RETs to the caller's post-payload return address.
;
; Mirrors the structure of the existing HLOAD trampoline
; (trampoline_body in trampoline.asm), generalised to any
; (target_addr, target_page) instead of (RST 8, fixed HOOK_HLOAD).
;
; Clobbers: A, HL, DE, F.  Preserves BC, IX, IY — caller passes target
; arguments in those registers.
;
; Not re-entrant: PAGED_CALL_HMPR_SAVE / PAGED_CALL_SP_SAVE are static
; one-deep slots.  Targets must not paged_call.
paged_call_body:
                di
                ld      (PAGED_CALL_SP_SAVE), sp        ; SP = caller_SP - 2
                                                        ; (post-CALL hardware-push state)
                in      a, (251)                        ; A = entry HMPR
                ld      (PAGED_CALL_HMPR_SAVE), a
                pop     hl                              ; HL -> inline payload;
                                                        ; SP = caller_SP

                ld      e, (hl)                         ; target lo
                inc     hl
                ld      d, (hl)                         ; target hi (DE = target)
                inc     hl
                ld      a, (hl)                         ; A = target page number
                inc     hl                              ; HL = post-payload return

                ; Rewrite caller's return-addr slot with the real
                ; post-payload return.  push hl decrements SP by 2
                ; (back to caller_SP - 2 = saved PAGED_CALL_SP_SAVE)
                ; and writes HL — overwriting the original (now
                ; stale) return-after-CALL pointer that pointed at
                ; the DEFW.
                push    hl                              ; SP := caller_SP - 2;
                                                        ; mem[SP..+1] := post-payload-return

                ld      sp, TRAMP_SAFE_SP               ; section-B-stable SP

                ld      hl, paged_call_trailer_dst
                push    hl                              ; target's RET lands here
                push    de                              ; target -> popped by final RET

                ; HMPR mask discipline (architecture doc §3.3, §7 risk #5):
                ; caller-supplied A holds only the page number; OR in
                ; the saved HMPR's CLUT + ext-mem bits before writing.
                ; Bits 5-6 are mode-3 CLUT, bit 7 is external-memory —
                ; clobbering them silently glitches palette / maps
                ; external memory (docs/notes/sam-paging.md:140-150).
                and     %00011111                       ; keep page bits
                ld      e, a
                ld      a, (PAGED_CALL_HMPR_SAVE)
                and     %11100000                       ; keep CLUT + ext-mem
                or      e                               ; combine
                out     (251), a                        ; HMPR := target page
                ret                                     ; -> target

paged_call_trailer:
                ; LIVES IN SECTION B at (PAGED_CALL_DST +
                ; (paged_call_trailer - paged_call_body)).  Fetched
                ; stably across the HMPR change because section B is
                ; LMPR-controlled, not HMPR-controlled.
                ;
                ; ABI: target's return registers (A / F + BC, IX, IY)
                ; flow back to the caller untouched.  Targets returning
                ; a byte in A or a status in F must see those values
                ; preserved across the trailer.  We use AF' as the
                ; scratch slot (the HLOAD trampoline uses the same
                ; pattern; AF' is callee-saved by Z80 convention so
                ; this is safe under the normal "targets don't touch
                ; AF'" assumption).
                ex      af, af'                         ; save target's AF
                ld      a, (PAGED_CALL_HMPR_SAVE)
                out     (251), a                        ; HMPR restored (full byte)
                ld      sp, (PAGED_CALL_SP_SAVE)        ; SP = caller_SP - 2;
                                                        ; mem[SP..+1] = post-payload-return
                ex      af, af'                         ; restore target's AF
                ; NB: don't EI — caller chose DI state; the M3 assembler
                ; runs DI throughout, so leaving DI matches the caller's
                ; invariant.
                ret                                     ; -> caller's post-payload return
paged_call_body_end:

; paged_call_trailer_dst — the absolute SECTION-B address of the trailer
; AFTER the LDIR copy.  paged_call_body is LDIR'd to PAGED_CALL_DST,
; so the trailer (its source offset relative to paged_call_body) lands
; at PAGED_CALL_DST + that offset.
paged_call_trailer_dst:   equ   PAGED_CALL_DST + (paged_call_trailer - paged_call_body)
