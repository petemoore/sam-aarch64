; spill.asm — the on-SAM IDE page-persistence (spill) manager (item i215b).
;
; SAM-side Z80 port of the Go authority:
;   tools/sam-aarch64/pagepool/spill.go  (item i215a)
;
; Layers a lazy-spill policy over the i2a page allocator (pagepool.asm): when the
; FREE pool is exhausted, instead of refusing an allocation outright (the i2
; baseline), the manager evicts the COLDEST resident editor-document (DOC) block
; to a pluggable backend, frees its page, and satisfies the request. A spilled
; block reloads on demand (which may itself spill another cold block). With no
; backend configured the manager degrades to the i2 baseline — refuse-with-
; message (SP_FAIL). Design authority: docs/specs/ide-memory-model-design.md §4.5
; (lazy spill), §5 (pool-exhaustion policy), §7 (q36: spill is downstream of i2,
; pluggable backend, must degrade to refuse on a Trinity-less machine).
;
; Only DOC blocks are spill victims: IN/OUT/scratch pages are the assembler's
; live working set and are never evicted (design §4.5).
;
; PROVENANCE: algorithmic port of spill.go. The strictly-monotonic LRU clock
; (SP_SEQ) gives a unique coldest block, so this port and the Go oracle pick the
; same victim and produce the same spill/reload sequence for an identical op
; order. BlockIDs are allocated monotonically and never reused (SP_NEXTID),
; matching the Go Manager so the harness can compare returned ids directly.
;
; REPRESENTATION (the Z80-specific choices the opaque-[]byte Go authority leaves
; to the port — design-autonomy decisions, not a new policy fork):
;   * Block registry: fixed parallel arrays indexed by slot (0..SP_MAX_BLOCKS-1).
;     A slot holds one DOC block's residency for its whole life; the slot is the
;     internal handle, the monotonic SP_ID[] value is the external BlockID.
;   * Payload modelling: a DOC block's payload (on hardware the 16 KB page; here
;     an opaque <=255-byte buffer, exactly as the Go authority treats it) lives
;     in PAGE_DATA[page] when resident and in SP_BACKSTORE[slot] when spilled —
;     faithfully exercising the spill/reload copy lifecycle without binding to the
;     i40 .tbn block format. 256 bytes/slot so the *256 index is a high-byte add.
;   * Backend: an in-RAM store (the memBackend analogue) reached only when
;     SP_HAS_BACKEND is set; a clear flag is the nil-backend "degrade-to-refuse"
;     case. i215c replaces the built-in store with real Trinity/floppy backends.
;
; VERIFICATION: tools/netboot-oracle/z80/spill_test.go drives these routines
; under koron-go/z80 and asserts the same observable outcomes as spill_test.go's
; Go scenarios — spill/reload counts, resident/spilled counts, payload round-trip,
; LRU victim selection, refuse-without-backend, and a failed Store leaving the
; victim resident.

                ; org only when assembled standalone (host harness uses
                ; -D SP_STANDALONE=1); an including file supplies its own org.
                if defined(SP_STANDALONE)
                org     &8000
                endif

; ---------------------------------------------------------------------------
; Constants.
; ---------------------------------------------------------------------------

; SP_MAX_BLOCKS: registry size — the most DOC blocks (resident + spilled) ever
; tracked at once. The harness packs a 3-page pool with up to 9 live blocks; 16
; gives headroom. Slot indices 0..SP_MAX_BLOCKS-1.
SP_MAX_BLOCKS:  equ 16

; SP_SLOT_SIZE: bytes reserved per page/backstore slot for the payload. 256 makes
; the *256 slot index a pure high-byte addition (the editmodel/EM_BLOCK_CAP
; idiom). Payloads modelled here are <=255 bytes (length in SP_PAYLEN).
SP_SLOT_SIZE:   equ 256

; SP_NOPAGE: SP_PAGE[] value for a spilled (page-less) block.
SP_NOPAGE:      equ &FF

; SP_FAIL / SP_OK: A-register result sentinels (the harness reads A across the
; CallEntry boundary). &FF is unambiguous: valid pages are 0..31 and SP_OK is 0.
SP_FAIL:        equ &FF
SP_OK:          equ 0

; ===========================================================================
; sp_init — reset the whole spill manager for a machine with A physical pages.
;
; Entry: A = nPages (forwarded to pp_init).
; Effect: sizes the page pool (pp_init), clears the block registry, zeroes the
;         LRU clock / id counter / observability counters, and starts with NO
;         backend (SP_HAS_BACKEND = 0 — the harness installs one with
;         sp_set_backend). Clears the one-shot backend-fail latch.
; Clobbers: A, B, C, D, E, HL.
; ===========================================================================
sp_init:
                call    pp_init                 ; A = nPages; sizes the pool

                ; Scalars to zero.
                ld      hl, 0
                ld      (SP_NEXTID), hl
                ld      (SP_SEQ), hl
                ld      (SP_SPILLS), hl
                ld      (SP_RELOADS), hl
                ld      (SP_BE_STORES), hl
                ld      (SP_BE_LOADS), hl
                ld      (SP_BE_DISCARDS), hl
                xor     a
                ld      (SP_HAS_BACKEND), a
                ld      (SP_BE_FAILNEXT), a

                ; Clear the in-use flags (a free slot is the only thing the
                ; allocator inspects; the other arrays are written on claim).
                ld      hl, SP_INUSE
                ld      b, SP_MAX_BLOCKS
sp_init_clear:
                ld      (hl), 0
                inc     hl
                djnz    sp_init_clear
                ret

; ===========================================================================
; sp_set_backend — install (A!=0) or remove (A==0) the spill backend.
;
; Entry: A = 0 -> nil backend (degrade-to-refuse); A != 0 -> backend present.
; Clobbers: A.
; ===========================================================================
sp_set_backend:
                or      a
                jr      z, sp_set_backend_off
                ld      a, 1
sp_set_backend_off:
                ld      (SP_HAS_BACKEND), a
                ret

; ===========================================================================
; sp_arm_fail — arm the one-shot backend failure latch (the Go memBackend
; failNext): the next Store/Load/Discard returns SP_FAIL, then the latch clears.
;
; Clobbers: A.
; ===========================================================================
sp_arm_fail:
                ld      a, 1
                ld      (SP_BE_FAILNEXT), a
                ret

; ===========================================================================
; sp_tick — advance and return the monotonic LRU clock so each touch orders
; strictly after every prior one (no ties) — deterministic coldest selection.
;
; Return: HL = new SP_SEQ value.
; Clobbers: HL.
; ===========================================================================
sp_tick:
                ld      hl, (SP_SEQ)
                inc     hl
                ld      (SP_SEQ), hl
                ret

; ===========================================================================
; sp_find_slot — find the registry slot whose SP_ID == DE (the external BlockID).
;
; Entry: DE = BlockID.
; Return: A = SP_OK and C = slot when found; A = SP_FAIL when no live slot has it.
; Clobbers: A, B, C, H, L.
; ===========================================================================
sp_find_slot:
                ld      hl, SP_INUSE
                ld      c, 0                    ; C = slot index
                ld      b, SP_MAX_BLOCKS
sp_find_loop:
                ld      a, (hl)                 ; in use?
                or      a
                jr      z, sp_find_next
                ; Compare SP_ID[slot] (2 bytes LE) against DE.
                push    hl
                push    bc
                ld      hl, SP_ID
                ld      b, 0
                sla     c                       ; C = slot*2 (slot < 128, no carry)
                add     hl, bc
                ld      a, (hl)                 ; id low
                cp      e
                jr      nz, sp_find_mismatch
                inc     hl
                ld      a, (hl)                 ; id high
                cp      d
                jr      nz, sp_find_mismatch
                pop     bc                      ; restore slot in C
                pop     hl
                ld      a, SP_OK                ; found: C already = slot
                ret
sp_find_mismatch:
                pop     bc
                pop     hl
sp_find_next:
                inc     hl
                inc     c
                djnz    sp_find_loop
                ld      a, SP_FAIL
                ret

; ===========================================================================
; sp_coldest — the spill victim: the resident DOC block with the lowest SP_LRU
; (least recently used), optionally excluding one protected block (the block a
; reload is faulting in, so it is never its own victim).
;
; Entry: SP_PROTECTING = 1 to exclude SP_PROTECT_ID from the candidate set.
; Return: A = SP_OK and C = slot when a victim exists; A = SP_FAIL when no
;         resident DOC block is eligible.
; Clobbers: A, B, C, D, E, H, L. Uses SP_MIN_LRU / SP_VICTIM as scratch.
; ===========================================================================
sp_coldest:
                ld      a, SP_FAIL
                ld      (SP_VICTIM), a          ; no victim yet
                ld      c, 0                    ; C = slot index
                ld      b, SP_MAX_BLOCKS
sp_cold_loop:
                push    bc                      ; save slot + remaining count

                ; Skip slots that are not resident DOC blocks.
                ld      b, 0                    ; BC = slot
                ld      hl, SP_INUSE
                add     hl, bc
                ld      a, (hl)
                or      a
                jr      z, sp_cold_skip         ; not in use
                ld      hl, SP_SPILLED
                add     hl, bc
                ld      a, (hl)
                or      a
                jr      nz, sp_cold_skip        ; spilled, not resident

                ; Honour the protected-block exclusion.
                ld      a, (SP_PROTECTING)
                or      a
                jr      z, sp_cold_candidate
                ; SP_ID[slot] == SP_PROTECT_ID ?
                ld      hl, SP_ID
                sla     c                       ; slot*2
                add     hl, bc
                ld      a, (hl)                 ; id low
                ld      de, (SP_PROTECT_ID)
                cp      e
                jr      nz, sp_cold_candidate
                inc     hl
                ld      a, (hl)                 ; id high
                cp      d
                jr      z, sp_cold_skip         ; this is the protected block

sp_cold_candidate:
                ; Candidate's LRU (2 bytes LE) -> DE.
                pop     bc                      ; restore slot in C
                push    bc
                ld      b, 0
                ld      hl, SP_LRU
                sla     c                       ; slot*2
                add     hl, bc
                ld      e, (hl)
                inc     hl
                ld      d, (hl)                 ; DE = SP_LRU[slot]

                ; First candidate always wins; else keep the strictly-lower LRU.
                ld      a, (SP_VICTIM)
                cp      SP_FAIL
                jr      z, sp_cold_take
                ; cand(DE) < min(SP_MIN_LRU) ?  carry set after 16-bit subtract.
                ld      hl, (SP_MIN_LRU)
                ld      a, e
                sub     l
                ld      a, d
                sbc     a, h                    ; CF set <=> DE < HL
                jr      nc, sp_cold_skip        ; cand not colder -> keep current
sp_cold_take:
                ld      (SP_MIN_LRU), de
                pop     bc                      ; slot in C
                push    bc
                ld      a, c
                ld      (SP_VICTIM), a          ; remember the coldest slot
sp_cold_skip:
                pop     bc                      ; restore slot + count
                inc     c
                djnz    sp_cold_loop

                ld      a, (SP_VICTIM)
                cp      SP_FAIL
                ret     z                       ; A = SP_FAIL, no victim
                ld      c, a                    ; C = victim slot
                ld      a, SP_OK
                ret

; ===========================================================================
; sp_be_store — back the resident payload of slot C to the in-RAM store.
; Copies SP_PAYLEN[slot] bytes from PAGE_DATA[page] to SP_BACKSTORE[slot].
;
; Entry: C = slot (must be resident: SP_PAGE[slot] valid).
; Return: A = SP_OK on success; A = SP_FAIL if the one-shot fail latch was armed.
; Clobbers: A, B, D, E, H, L (C preserved). Increments SP_BE_STORES on success.
; ===========================================================================
sp_be_store:
                call    sp_check_fail
                ret     nz                      ; latched failure -> A=SP_FAIL
                ; src = PAGE_DATA + page*256 ; page = SP_PAGE[slot]
                push    bc
                ld      b, 0
                ld      hl, SP_PAGE
                add     hl, bc
                ld      a, (hl)                 ; A = page
                add     a, PAGE_DATA >> 8
                ld      h, a
                ld      l, PAGE_DATA & &FF      ; HL = &PAGE_DATA[page]
                ; dst = SP_BACKSTORE + slot*256
                ld      a, c                    ; slot
                add     a, SP_BACKSTORE >> 8
                ld      d, a
                ld      e, SP_BACKSTORE & &FF   ; DE = &SP_BACKSTORE[slot]
                call    sp_copy_payload         ; LDIR of SP_PAYLEN[slot] bytes
                pop     bc
                ld      hl, (SP_BE_STORES)
                inc     hl
                ld      (SP_BE_STORES), hl
                ld      a, SP_OK
                ret

; ===========================================================================
; sp_be_load — restore the spilled payload of slot C into a freshly-claimed page.
; Copies SP_PAYLEN[slot] bytes from SP_BACKSTORE[slot] to PAGE_DATA[page].
;
; Entry: C = slot, B = destination page.
; Return: A = SP_OK on success; A = SP_FAIL if the fail latch was armed.
; Clobbers: A, B, D, E, H, L (C preserved). Increments SP_BE_LOADS on success.
; ===========================================================================
sp_be_load:
                ld      a, b
                ld      (SP_TMP_PAGE), a        ; stash dest page across the check
                call    sp_check_fail
                ret     nz
                ; src = SP_BACKSTORE + slot*256
                push    bc
                ld      a, c
                add     a, SP_BACKSTORE >> 8
                ld      h, a
                ld      l, SP_BACKSTORE & &FF   ; HL = &SP_BACKSTORE[slot]
                ; dst = PAGE_DATA + page*256
                ld      a, (SP_TMP_PAGE)
                add     a, PAGE_DATA >> 8
                ld      d, a
                ld      e, PAGE_DATA & &FF      ; DE = &PAGE_DATA[page]
                call    sp_copy_payload
                pop     bc
                ld      hl, (SP_BE_LOADS)
                inc     hl
                ld      (SP_BE_LOADS), hl
                ld      a, SP_OK
                ret

; ===========================================================================
; sp_be_discard — release the backend copy of slot C (no data move; the store is
; per-slot so the slot's reuse on FreeBlock reclaims it).
;
; Entry: C = slot.
; Return: A = SP_OK on success; A = SP_FAIL if the fail latch was armed.
; Clobbers: A, H, L (C preserved). Increments SP_BE_DISCARDS on success.
; ===========================================================================
sp_be_discard:
                call    sp_check_fail
                ret     nz
                ld      hl, (SP_BE_DISCARDS)
                inc     hl
                ld      (SP_BE_DISCARDS), hl
                ld      a, SP_OK
                ret

; sp_check_fail — consume the one-shot backend-fail latch.
; Return: Z set (A=SP_OK) to proceed; NZ (A=SP_FAIL) when the latch was armed
;         (and now cleared). Clobbers A.
sp_check_fail:
                ld      a, (SP_BE_FAILNEXT)
                or      a
                jr      nz, sp_check_fail_armed
                xor     a                       ; A=SP_OK, Z set
                ret
sp_check_fail_armed:
                xor     a
                ld      (SP_BE_FAILNEXT), a     ; clear the one-shot
                ld      a, SP_FAIL              ; NZ
                or      a
                ret

; sp_copy_payload — LDIR SP_PAYLEN[slot] bytes from HL (src) to DE (dst).
; Entry: HL = src, DE = dst, C = slot. A zero length copies nothing (LDIR with
; BC=0 would copy 65536). Preserves HL/DE inputs across the length read.
; Clobbers: A, B, C (slot destroyed — callers restore it from the stack).
sp_copy_payload:
                push    hl
                push    de
                ld      b, 0
                ld      hl, SP_PAYLEN
                add     hl, bc                  ; BC = slot
                ld      a, (hl)                 ; A = len
                pop     de
                pop     hl
                or      a
                ret     z                       ; len 0 -> copy nothing
                ld      c, a
                ld      b, 0                    ; BC = len
                ldir
                ret

; ===========================================================================
; sp_ensure_free — guarantee at least one FREE page, spilling the coldest
; resident DOC block if necessary. The lazy hinge of design §4.5: touches the
; backend ONLY when FREE is empty, and exactly one eviction per shortfall.
;
; Entry: SP_PROTECTING / SP_PROTECT_ID name a block the spill must not evict
;        (the block a reload is faulting in); SP_PROTECTING=0 protects nothing.
; Return: A = SP_OK when a FREE page is now available; A = SP_FAIL when the pool
;         is full and the shortfall cannot be covered (no backend, or no resident
;         DOC block to evict). On a Store failure the victim stays resident,
;         owning its page (nothing mutated) — matching spill.go.
; Clobbers: A, B, C, D, E, H, L. Uses SP_VICTIM / SP_MIN_LRU.
; ===========================================================================
sp_ensure_free:
                call    pp_free_count           ; A = FREE pages
                or      a
                jr      nz, sp_ef_ok            ; FREE non-empty: the common path
                ld      a, (SP_HAS_BACKEND)
                or      a
                jr      z, sp_ef_fail          ; i2 baseline: no backend, refuse
                call    sp_coldest              ; A=SP_OK & C=slot, or A=SP_FAIL
                cp      SP_FAIL
                jr      z, sp_ef_fail          ; nothing to evict
                ld      a, c
                ld      (SP_VICTIM), a          ; remember the victim slot
                call    sp_be_store             ; C=slot; back it to the store
                cp      SP_FAIL
                jr      z, sp_ef_fail          ; Store failed: victim unchanged
                ; Free the victim's page (it is still resident here).
                ld      a, (SP_VICTIM)
                ld      c, a
                ld      b, 0
                ld      hl, SP_PAGE
                add     hl, bc
                ld      a, (hl)                 ; A = victim page
                ld      b, PP_DOC               ; expected owner tag
                call    pp_free_page
                ; Mark the victim spilled: page-less, off the resident set.
                ld      a, (SP_VICTIM)
                ld      c, a
                ld      b, 0
                ld      hl, SP_SPILLED
                add     hl, bc
                ld      (hl), 1
                ld      hl, SP_PAGE
                add     hl, bc
                ld      (hl), SP_NOPAGE
                ld      hl, (SP_SPILLS)
                inc     hl
                ld      (SP_SPILLS), hl
sp_ef_ok:
                ld      a, SP_OK
                ret
sp_ef_fail:
                ld      a, SP_FAIL
                ret

; ===========================================================================
; sp_new_block — create a resident DOC block holding B bytes at (HL), return its
; id. Spills a cold block first if the pool is full; the payload is copied so the
; caller may reuse its buffer.
;
; Entry: HL = payload source, B = length (0..255).
; Return: A = SP_OK and HL = new BlockID on success; A = SP_FAIL on exhaustion
;         (no backend / nothing to evict) or registry full.
; Clobbers: A, B, C, D, E, H, L.
; ===========================================================================
sp_new_block:
                ld      (SP_TMP_PTR), hl        ; stash payload ptr + len
                ld      a, b
                ld      (SP_TMP_LEN), a

                xor     a
                ld      (SP_PROTECTING), a      ; protect nothing
                call    sp_ensure_free
                cp      SP_FAIL
                jp      z, sp_nb_fail

                ld      a, PP_DOC
                call    pp_alloc_page           ; A = page (ensure_free guaranteed)
                cp      SP_FAIL
                jp      z, sp_nb_fail
                ld      (SP_TMP_PAGE), a        ; A = freshly-claimed page

                call    sp_alloc_slot           ; C = free slot, A=SP_OK/SP_FAIL
                cp      SP_FAIL
                jp      z, sp_nb_fail           ; registry full (harness-sized away)
                ld      a, c
                ld      (SP_VICTIM), a          ; reuse SP_VICTIM as "this slot"

                ; Assign the monotonic id.
                ld      de, (SP_NEXTID)
                ld      hl, SP_NEXTID
                inc     (hl)                    ; low++
                jr      nz, sp_nb_id_done
                inc     hl
                inc     (hl)                    ; carry into high
sp_nb_id_done:
                ; SP_ID[slot] = DE (the id we just consumed).
                ld      b, 0
                ld      hl, SP_ID
                sla     c                       ; slot*2
                add     hl, bc
                ld      (hl), e
                inc     hl
                ld      (hl), d

                ; Residency fields: in use, not spilled, page, length.
                ld      a, (SP_VICTIM)
                ld      c, a
                ld      b, 0
                ld      hl, SP_INUSE
                add     hl, bc
                ld      (hl), 1
                ld      hl, SP_SPILLED
                add     hl, bc
                ld      (hl), 0
                ld      hl, SP_PAGE
                add     hl, bc
                ld      a, (SP_TMP_PAGE)
                ld      (hl), a
                ld      hl, SP_PAYLEN
                add     hl, bc
                ld      a, (SP_TMP_LEN)
                ld      (hl), a

                ; Copy the payload into the page: PAGE_DATA[page] <- (SP_TMP_PTR).
                ld      a, (SP_TMP_PAGE)
                add     a, PAGE_DATA >> 8
                ld      d, a
                ld      e, PAGE_DATA & &FF      ; DE = dst
                ld      hl, (SP_TMP_PTR)        ; HL = src
                call    sp_copy_payload         ; C = slot (still set)

                ; lastUsed = tick (slot in SP_VICTIM -> C).
                ld      a, (SP_VICTIM)
                ld      c, a
                call    sp_touch

                ; Return the id in HL.
                ld      hl, (SP_RET_ID)
                ld      a, SP_OK
                ret
sp_nb_fail:
                ld      a, SP_FAIL
                ret

; sp_alloc_slot — find a free registry slot (SP_INUSE==0).
; Return: A=SP_OK & C=slot, or A=SP_FAIL when full. Clobbers A,B,C,H,L.
sp_alloc_slot:
                ld      hl, SP_INUSE
                ld      c, 0
                ld      b, SP_MAX_BLOCKS
sp_as_loop:
                ld      a, (hl)
                or      a
                jr      z, sp_as_found
                inc     hl
                inc     c
                djnz    sp_as_loop
                ld      a, SP_FAIL
                ret
sp_as_found:
                ld      a, SP_OK                ; C = free slot
                ret

; sp_touch — mark slot C most-recently-used; also records its id in SP_RET_ID
; for sp_new_block's return. Takes the slot in C (not a global) because the
; reload path mutates SP_VICTIM mid-flight. Clobbers A,B,C,D,E,H,L.
sp_touch:
                ld      b, 0
                push    bc                      ; save slot across sla
                ; Record SP_RET_ID = SP_ID[slot] (used by sp_new_block).
                ld      hl, SP_ID
                sla     c
                add     hl, bc
                ld      e, (hl)
                inc     hl
                ld      d, (hl)
                ld      (SP_RET_ID), de
                ; SP_LRU[slot] = tick.
                call    sp_tick                 ; HL = new seq (preserves C)
                ex      de, hl                  ; DE = seq
                pop     bc                      ; restore slot in C
                ld      b, 0
                ld      hl, SP_LRU
                sla     c
                add     hl, bc
                ld      (hl), e
                inc     hl
                ld      (hl), d
                ret

; ===========================================================================
; sp_reload — fault the spilled block in slot C back into RAM: secure a FREE page
; (spilling another cold block if needed, but never this one), read the payload
; from the backend, mark it resident, discard the stale backend copy.
;
; Entry: C = slot (must be spilled).
; Return: A = SP_OK on success; A = SP_FAIL on exhaustion or backend failure
;         (the grabbed page is released so the table is unchanged on failure).
; Clobbers: A, B, C, D, E, H, L. Increments SP_RELOADS on success.
; ===========================================================================
sp_reload:
                ld      a, c
                ld      (SP_RELOAD_SLOT), a

                ; Protect this block from being its own spill victim.
                ld      b, 0
                ld      hl, SP_ID
                sla     c                       ; slot*2
                add     hl, bc
                ld      e, (hl)
                inc     hl
                ld      d, (hl)
                ld      (SP_PROTECT_ID), de
                ld      a, 1
                ld      (SP_PROTECTING), a
                call    sp_ensure_free
                cp      SP_FAIL
                jr      z, sp_reload_fail

                ld      a, PP_DOC
                call    pp_alloc_page
                cp      SP_FAIL
                jr      z, sp_reload_fail
                ld      (SP_TMP_PAGE), a

                ; Load: SP_BACKSTORE[slot] -> PAGE_DATA[page].
                ld      a, (SP_RELOAD_SLOT)
                ld      c, a
                ld      a, (SP_TMP_PAGE)
                ld      b, a
                call    sp_be_load              ; C=slot, B=page
                cp      SP_FAIL
                jr      z, sp_reload_undo

                ; Discard the now-stale backend copy.
                ld      a, (SP_RELOAD_SLOT)
                ld      c, a
                call    sp_be_discard
                cp      SP_FAIL
                jr      z, sp_reload_undo

                ; Mark resident: page set, spilled cleared.
                ld      a, (SP_RELOAD_SLOT)
                ld      c, a
                ld      b, 0
                ld      hl, SP_PAGE
                add     hl, bc
                ld      a, (SP_TMP_PAGE)
                ld      (hl), a
                ld      hl, SP_SPILLED
                add     hl, bc
                ld      (hl), 0
                ld      hl, (SP_RELOADS)
                inc     hl
                ld      (SP_RELOADS), hl
                ld      a, SP_OK
                ret
sp_reload_undo:
                ; Release the page we grabbed so the table is unchanged on failure.
                ld      a, (SP_TMP_PAGE)
                ld      b, PP_DOC
                call    pp_free_page
sp_reload_fail:
                ld      a, SP_FAIL
                ret

; ===========================================================================
; sp_block_data — return the payload of DOC block HL, reloading it from the
; backend first if it was spilled, and mark it most-recently-used.
;
; Entry: HL = BlockID.
; Return: A = SP_OK, DE = &payload, B = length (BC: C=0) on success;
;         A = SP_FAIL if the id is unknown or a reload fails.
; Clobbers: A, B, C, D, E, H, L.
; ===========================================================================
sp_block_data:
                ex      de, hl                  ; DE = id for sp_find_slot
                call    sp_find_slot
                cp      SP_FAIL
                ret     z                       ; unknown id
                ; SP_CURSLOT (not SP_VICTIM): the reload path mutates SP_VICTIM.
                ld      a, c
                ld      (SP_CURSLOT), a

                ; Reload if spilled.
                ld      b, 0
                ld      hl, SP_SPILLED
                add     hl, bc
                ld      a, (hl)
                or      a
                jr      z, sp_bd_resident
                ld      a, (SP_CURSLOT)
                ld      c, a
                call    sp_reload
                cp      SP_FAIL
                ret     z                       ; reload failed
sp_bd_resident:
                ; Touch (most-recently-used).
                ld      a, (SP_CURSLOT)
                ld      c, a
                call    sp_touch

                ; DE = &PAGE_DATA[page]; B = SP_PAYLEN[slot].
                ld      a, (SP_CURSLOT)
                ld      c, a
                ld      b, 0
                ld      hl, SP_PAGE
                add     hl, bc
                ld      a, (hl)                 ; page
                add     a, PAGE_DATA >> 8
                ld      d, a
                ld      e, PAGE_DATA & &FF      ; DE = payload ptr
                ld      a, (SP_CURSLOT)
                ld      c, a
                ld      b, 0
                ld      hl, SP_PAYLEN
                add     hl, bc
                ld      b, (hl)                 ; B = length
                ld      c, 0
                ld      a, SP_OK
                ret

; ===========================================================================
; sp_free_block — release DOC block HL: its page returns to the pool if resident,
; or its backend copy is discarded if spilled. The id becomes unknown afterwards.
;
; Entry: HL = BlockID.
; Return: A = SP_OK on success; A = SP_FAIL if the id is unknown or a backend
;         Discard fails.
; Clobbers: A, B, C, D, E, H, L.
; ===========================================================================
sp_free_block:
                ex      de, hl
                call    sp_find_slot
                cp      SP_FAIL
                ret     z                       ; unknown id
                ld      a, c
                ld      (SP_VICTIM), a

                ld      b, 0
                ld      hl, SP_SPILLED
                add     hl, bc
                ld      a, (hl)
                or      a
                jr      nz, sp_fb_spilled

                ; Resident: free the page (asserting the DOC tag).
                ld      a, (SP_VICTIM)
                ld      c, a
                ld      b, 0
                ld      hl, SP_PAGE
                add     hl, bc
                ld      a, (hl)                 ; page
                ld      b, PP_DOC
                call    pp_free_page
                jr      sp_fb_release

sp_fb_spilled:
                ; Spilled: discard the backend copy (only if a backend exists).
                ld      a, (SP_HAS_BACKEND)
                or      a
                jr      z, sp_fb_release        ; nil backend: nothing to discard
                ld      a, (SP_VICTIM)
                ld      c, a
                call    sp_be_discard
                cp      SP_FAIL
                ret     z                       ; Discard failed

sp_fb_release:
                ; Free the registry slot (the id is now unknown).
                ld      a, (SP_VICTIM)
                ld      c, a
                ld      b, 0
                ld      hl, SP_INUSE
                add     hl, bc
                ld      (hl), 0
                ld      a, SP_OK
                ret

; ===========================================================================
; sp_alloc — hand out a non-DOC page (IN/OUT/scratch) for the assembler's live
; working set, spilling a cold DOC block first if the pool is full.
;
; Entry: A = owner tag (must NOT be PP_DOC; DOC pages come from sp_new_block).
; Return: A = page (0..31) on success; A = SP_FAIL on a bad tag or exhaustion.
; Clobbers: A, B, C, D, E, H, L.
; ===========================================================================
sp_alloc:
                cp      PP_DOC
                jr      z, sp_alloc_fail        ; DOC must use sp_new_block
                cp      PP_FIRST_TAG
                jr      c, sp_alloc_fail        ; non-owner tag
                ld      (SP_TMP_OWNER), a
                xor     a
                ld      (SP_PROTECTING), a
                call    sp_ensure_free
                cp      SP_FAIL
                jr      z, sp_alloc_fail
                ld      a, (SP_TMP_OWNER)
                jp      pp_alloc_page           ; A = page or PP_FAIL (== SP_FAIL)
sp_alloc_fail:
                ld      a, SP_FAIL
                ret

; ===========================================================================
; sp_free — return a non-DOC page (from sp_alloc) to the pool. Thin pass-through
; to pp_free_page; DOC blocks are released with sp_free_block.
;
; Entry: A = page, B = expected owner tag.
; Return: A = SP_OK (0) on success; A = SP_FAIL on out-of-range / tag mismatch.
; ===========================================================================
sp_free:
                jp      pp_free_page

; ===========================================================================
; sp_resident_doc_count / sp_spilled_doc_count — observability for the harness.
; Return: A = number of in-use DOC blocks that are resident / spilled.
; Clobbers: A, B, C, D, E, H, L.
; ===========================================================================
sp_resident_doc_count:
                ld      e, 0                    ; want spilled flag == 0
                jr      sp_count
sp_spilled_doc_count:
                ld      e, 1                    ; want spilled flag == 1
sp_count:
                ld      d, 0                    ; D = running count
                ld      c, 0                    ; C = slot
                ld      b, SP_MAX_BLOCKS
sp_count_loop:
                push    bc
                ld      b, 0                    ; BC = slot
                ld      hl, SP_INUSE
                add     hl, bc
                ld      a, (hl)
                or      a
                jr      z, sp_count_next        ; not in use
                ld      hl, SP_SPILLED
                add     hl, bc
                ld      a, (hl)                 ; spilled flag
                cp      e                       ; matches the wanted state?
                jr      nz, sp_count_next
                inc     d
sp_count_next:
                pop     bc
                inc     c
                djnz    sp_count_loop
                ld      a, d
                ret

; ===========================================================================
; The page allocator core (pp_*) + its page_owner[] table.
; ===========================================================================
                include "pagepool.asm"

; ===========================================================================
; Resident state. In the standalone harness build this follows the code; the
; harness reads the counters and arrays straight out of memory by symbol.
; ===========================================================================
SP_NEXTID:      defw 0          ; next BlockID to hand out (monotonic, never reused)
SP_SEQ:         defw 0          ; LRU clock (strictly increasing)
SP_SPILLS:      defw 0          ; DOC-block evictions (observability)
SP_RELOADS:     defw 0          ; on-demand reloads
SP_HAS_BACKEND: defb 0          ; 0 => nil backend (degrade-to-refuse)
SP_BE_FAILNEXT: defb 0          ; one-shot backend-fail latch
SP_BE_STORES:   defw 0          ; backend Store call count
SP_BE_LOADS:    defw 0          ; backend Load call count
SP_BE_DISCARDS: defw 0          ; backend Discard call count

; Scratch used across the multi-call routines (register pressure parks here).
SP_PROTECTING:  defb 0          ; sp_coldest: exclude SP_PROTECT_ID when set
SP_PROTECT_ID:  defw 0          ; the protected BlockID (block being reloaded)
SP_VICTIM:      defb 0          ; current working slot (victim / new-block slot)
SP_CURSLOT:     defb 0          ; sp_block_data's slot, stable across a reload
SP_MIN_LRU:     defw 0          ; sp_coldest running minimum LRU
SP_RELOAD_SLOT: defb 0          ; sp_reload's target slot across backend calls
SP_TMP_PTR:     defw 0          ; sp_new_block payload source
SP_TMP_LEN:     defb 0          ; sp_new_block payload length
SP_TMP_PAGE:    defb 0          ; freshly-claimed page across copies
SP_TMP_OWNER:   defb 0          ; sp_alloc owner tag across sp_ensure_free
SP_RET_ID:      defw 0          ; sp_touch -> sp_new_block id hand-back

; Block registry — parallel arrays, one entry per slot (0..SP_MAX_BLOCKS-1).
SP_INUSE:       defs SP_MAX_BLOCKS      ; 1 = slot holds a live DOC block
SP_SPILLED:     defs SP_MAX_BLOCKS      ; 1 = block is spilled (page-less)
SP_PAGE:        defs SP_MAX_BLOCKS      ; physical page when resident; SP_NOPAGE spilled
SP_PAYLEN:      defs SP_MAX_BLOCKS      ; payload length (bytes)
SP_ID:          defs SP_MAX_BLOCKS * 2  ; external BlockID (LE) per slot
SP_LRU:         defs SP_MAX_BLOCKS * 2  ; lastUsed LRU stamp (LE) per slot

; Payload backing store. PAGE_DATA holds a resident block's payload (indexed by
; physical page); SP_BACKSTORE holds a spilled block's payload (indexed by slot).
; 256 bytes/entry keeps the *256 index a high-byte add.
PAGE_DATA:      defs PP_MAX_PAGES * SP_SLOT_SIZE
SP_BACKSTORE:   defs SP_MAX_BLOCKS * SP_SLOT_SIZE
