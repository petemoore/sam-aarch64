; pagepool.asm — the on-SAM IDE page allocator core (item i2a).
;
; SAM-side Z80 port of the Go authority:
;   tools/sam-aarch64/pagepool/pagepool.go
;
; Implements the page_owner[] ownership table + alloc_page/free_page discipline
; of docs/specs/ide-memory-model-design.md §4.1: a single dynamic page pool
; shared by the editor and the assembler. This brick is the allocator MECHANISM
; only — it does NOT yet wire the assembler's IN/OUT/DOC buffers to the pool
; (spec §4.4), nor read PRAMTP/VMPR/etc. for boot-time sizing (spec §4.2). Those
; are later i2 bricks; here pp_init takes the page count explicitly and
; pp_reserve marks the system pages, so the boot survey can layer on top
; unchanged.
;
; page_owner[] is the single source of truth for who holds each physical page.
; A page-ownership bug here is the RAM equivalent of clobbering another user's
; SD record (the Trinity shared-resource discipline), so free_page asserts the
; caller's tag before returning a page to FREE.
;
; PROVENANCE: algorithmic port of pagepool.go. The first-FREE allocation policy
; (alloc hands out the lowest-numbered FREE page) is deterministic so this port
; and the Go oracle return identical page numbers for an identical op sequence.
;
; VERIFICATION: tools/netboot-oracle/z80/pagepool_test.go drives these routines
; under koron-go/z80, replays random alloc/free ops, and asserts the page
; numbers + page_owner[] table match the Go oracle on every step, plus the spec
; §6 claim-all/free-all round-trip (reserved pages never move).

                ; org only when assembled standalone (host harness uses
                ; -D PP_STANDALONE=1); an including file supplies its own org.
                if defined(PP_STANDALONE)
                org     &8000
                endif

; ---------------------------------------------------------------------------
; Constants — owner-tag byte values. These MUST match the Owner consts in
; pagepool.go so the Go authority and this port agree on table contents.
; ---------------------------------------------------------------------------

; PP_RESERVED: a page the IDE never hands out (absent / ROM / BASIC / DOS /
; screen / resident code). Also the init value for pages above the physical
; page count. Value 0 so a cleared table reads as all-reserved (fail-safe).
PP_RESERVED:    equ 0
; PP_FREE: an allocatable page held by no subsystem.
PP_FREE:        equ 1
; Owner tags (>= PP_FIRST_TAG): a page handed out to a subsystem.
PP_DOC:         equ 2
PP_IN:          equ 3
PP_OUT:         equ 4
PP_SCRATCH:     equ 5
PP_ENCTAB:      equ 6
PP_DISASM:      equ 7
PP_PAYLOAD:     equ 8
; PP_FIRST_TAG: lowest value denoting a handed-out page. free_page only accepts
; a page whose current tag is >= this.
PP_FIRST_TAG:   equ 2

; PP_MAX_PAGES: page_owner[] table size — 32 covers a 512 KB SAM (pages 0..31).
; Matches MaxPages in pagepool.go.
PP_MAX_PAGES:   equ 32

; PP_FAIL: sentinel returned in A when alloc finds no FREE page or free rejects a
; tag mismatch. 0..31 are valid pages, so &FF is unambiguous. The harness reads
; the result register (carry flags aren't visible across the CallEntry boundary),
; so every routine also encodes success/failure in A.
PP_FAIL:        equ &FF

; ===========================================================================
; pp_init — size the pool for a machine with A physical pages.
;
; Entry: A = nPages (e.g. PRAMTP+1).
; Effect: pages [0,A) -> PP_FREE, [A,PP_MAX_PAGES) -> PP_RESERVED (absent).
;         Stores nPages in PP_NPAGES.
; Clobbers: A, B, C, HL.
; ===========================================================================
pp_init:
                ld      (PP_NPAGES), a
                ld      hl, PP_OWNER
                ld      b, a            ; B = nPages = FREE pages to write
                or      a
                jr      z, pp_init_reserved   ; nPages==0 -> all reserved
                ld      a, PP_FREE
pp_init_free_loop:
                ld      (hl), a
                inc     hl
                djnz    pp_init_free_loop
pp_init_reserved:
                ; Fill the remaining (PP_MAX_PAGES - nPages) pages RESERVED.
                ld      a, (PP_NPAGES)
                ld      c, a
                ld      a, PP_MAX_PAGES
                sub     c               ; A = pages remaining
                ret     z               ; none -> done
                ld      b, a
                ld      a, PP_RESERVED
pp_init_res_loop:
                ld      (hl), a
                inc     hl
                djnz    pp_init_res_loop
                ret

; ===========================================================================
; pp_reserve — mark page A RESERVED so it is never handed out.
;
; Entry: A = page (0..PP_MAX_PAGES-1).
; Return: A = 0 on success, A = PP_FAIL if the page is out of range.
; Clobbers: D, E, HL (and A holds the result).
; ===========================================================================
pp_reserve:
                cp      PP_MAX_PAGES
                jr      nc, pp_reserve_fail
                ld      hl, PP_OWNER
                ld      e, a
                ld      d, 0
                add     hl, de
                ld      (hl), PP_RESERVED
                xor     a               ; A = 0 = success
                ret
pp_reserve_fail:
                ld      a, PP_FAIL
                ret

; ===========================================================================
; pp_alloc_page — hand out the lowest-numbered FREE page, tagged with owner A.
;
; Entry: A = owner tag (PP_DOC..PP_PAYLOAD).
; Return: A = page number (0..PP_MAX_PAGES-1) on success;
;         A = PP_FAIL when no FREE page remains (pool exhausted).
; Clobbers: B, C, D, E, HL (A holds the result).
; ===========================================================================
pp_alloc_page:
                ld      c, a            ; C = owner tag to write
                ld      a, (PP_NPAGES)
                or      a
                jr      z, pp_alloc_fail        ; empty pool
                ld      b, a            ; B = nPages = pages to scan
                ld      hl, PP_OWNER
                ld      d, 0            ; D = running page index
pp_alloc_scan:
                ld      a, (hl)
                cp      PP_FREE
                jr      z, pp_alloc_hit
                inc     hl
                inc     d
                djnz    pp_alloc_scan
pp_alloc_fail:
                ld      a, PP_FAIL
                ret
pp_alloc_hit:
                ld      (hl), c         ; tag the page with the owner
                ld      a, d            ; A = page number
                ret

; ===========================================================================
; pp_free_page — return page A to FREE, asserting it currently carries tag B.
;
; Entry: A = page (0..PP_MAX_PAGES-1), B = expected owner tag.
; Return: A = 0 on success; A = PP_FAIL on out-of-range or tag mismatch
;         (double-free, freeing a RESERVED page, or wrong owner). On a mismatch
;         the table is left unchanged.
; Clobbers: D, E, HL (A holds the result).
; ===========================================================================
pp_free_page:
                cp      PP_MAX_PAGES
                jr      nc, pp_free_fail
                ld      hl, PP_OWNER
                ld      e, a
                ld      d, 0
                add     hl, de
                ld      a, (hl)
                cp      b               ; current owner == expected?
                jr      nz, pp_free_fail
                ld      (hl), PP_FREE
                xor     a               ; A = 0 = success
                ret
pp_free_fail:
                ld      a, PP_FAIL
                ret

; ===========================================================================
; pp_alloc_run — hand out the lowest-indexed CONTIGUOUS run of B FREE pages,
; tagging all B with owner A. First-fit, deterministic: identical outcome to the
; Go oracle AllocRun for an identical op sequence.
;
; Entry: A = owner tag (PP_DOC..PP_PAYLOAD), B = n (number of pages, >= 1).
; Return: A = first page number (0..PP_MAX_PAGES-1) on success;
;         A = PP_FAIL when no run of B consecutive FREE pages exists. n==0 and
;         n > PP_NPAGES both fail WITHOUT tagging anything.
; Clobbers: B, C, D, E, H, L (A holds the result). Stack is balanced.
;
; Strategy: for start = 0 .. PP_NPAGES-n, test PP_OWNER[start..start+n-1] all
; FREE; on the first full match, tag all n and return start. C holds n (run
; length); the owner tag is held on the stack and reloaded for the tag pass; E
; holds the current start index; the scan address is recomputed from PP_OWNER + E
; each start so a partial match cheaply rewinds.
; ===========================================================================
pp_alloc_run:
                push    af              ; stash owner tag (A) for the tag pass
                ld      c, b            ; C = n (run length)
                ld      a, b
                or      a
                jr      z, pp_alloc_run_fail    ; n==0 -> fail, tag nothing
                ld      a, (PP_NPAGES)
                sub     b               ; A = PP_NPAGES - n = last valid start
                jr      c, pp_alloc_run_fail    ; n > PP_NPAGES -> no run fits
                ; A = max start index (inclusive); loop start = 0..A.
                inc     a               ; A = number of start positions to try
                ld      d, a            ; D = remaining start positions
                ld      e, 0            ; E = current start index
pp_alloc_run_try:
                ; Check PP_OWNER[E .. E+n-1] all FREE. HL = PP_OWNER + E.
                ld      hl, PP_OWNER
                ld      a, l
                add     a, e
                ld      l, a
                ld      a, h
                adc     a, 0
                ld      h, a            ; HL = &PP_OWNER[E]
                ld      b, c            ; B = n pages to check
pp_alloc_run_check:
                ld      a, (hl)
                cp      PP_FREE
                jr      nz, pp_alloc_run_next   ; not all free -> next start
                inc     hl
                djnz    pp_alloc_run_check
                ; Full match. HL now points one past the run (PP_OWNER+E+n);
                ; tag all n walking BACKWARDS, avoiding a second base recompute.
                ld      b, c            ; B = n pages to tag
                pop     af              ; A = owner tag (balances the entry push)
pp_alloc_run_tag:
                dec     hl
                ld      (hl), a         ; tag the page with the owner
                djnz    pp_alloc_run_tag
                ld      a, e            ; A = first page = start index
                ret
pp_alloc_run_next:
                inc     e               ; advance start index
                dec     d
                jr      nz, pp_alloc_run_try
pp_alloc_run_fail:
                pop     af              ; balance the entry push
                ld      a, PP_FAIL
                ret

; ===========================================================================
; pp_free_run — return B consecutive pages [A,A+n) to FREE, asserting EVERY one
; currently carries tag C. All-or-nothing: all n are validated BEFORE any is
; freed, so a partial mismatch leaves the table unchanged.
;
; Entry: A = first page, B = n (>= 1), C = expected owner tag.
; Return: A = 0 on success; A = PP_FAIL on out-of-range (page+n > PP_MAX_PAGES,
;         n==0) or any tag mismatch. On failure the table is unchanged.
; Clobbers: B, D, E, H, L (A holds the result; C preserved through validation).
;
; STANDALONE-ONLY: the symmetric counterpart to pp_alloc_run, but no PRODUCTION
; consumer exists yet — i23's load_in_file frees its single head page with
; pp_free_page and never run-frees the IN buffer (the assembler is single-shot;
; i24's OUT migration / the IDE edit-assemble cycle is the first run-free
; caller). Compiled only into the standalone harness build (PP_STANDALONE) so it
; stays Go-authority-verified yet costs the production assembler zero bytes
; against the tight &C000 test-variant budget. Drop the guard when a production
; caller appears.
; ===========================================================================
                if defined(PP_STANDALONE)
pp_free_run:
                ld      e, a            ; E = first page (saved for the free pass)
                ld      a, b
                or      a
                jr      z, pp_free_run_fail     ; n==0 -> fail
                ; Range: page + n must be <= PP_MAX_PAGES.
                ld      a, e
                add     a, b            ; A = page + n (fits in 8 bits: <= 32+255)
                jr      c, pp_free_run_fail     ; wrapped -> out of range
                cp      PP_MAX_PAGES + 1
                jr      nc, pp_free_run_fail    ; page+n > PP_MAX_PAGES
                ; Validation pass: PP_OWNER[E .. E+n-1] all == C, mutate nothing.
                ld      hl, PP_OWNER
                ld      a, l
                add     a, e
                ld      l, a
                ld      a, h
                adc     a, 0
                ld      h, a            ; HL = &PP_OWNER[E]
                ; B already = n; D = n saved for the free pass.
                ld      d, b
pp_free_run_check:
                ld      a, (hl)
                cp      c               ; current owner == expected?
                jr      nz, pp_free_run_fail    ; mismatch -> abort, unchanged
                inc     hl
                djnz    pp_free_run_check
                ; All n validated. Free pass: PP_OWNER[E .. E+n-1] = PP_FREE.
                ld      hl, PP_OWNER
                ld      a, l
                add     a, e
                ld      l, a
                ld      a, h
                adc     a, 0
                ld      h, a            ; HL = &PP_OWNER[E]
                ld      b, d            ; B = n pages to free
pp_free_run_free:
                ld      (hl), PP_FREE
                inc     hl
                djnz    pp_free_run_free
                xor     a               ; A = 0 = success
                ret
pp_free_run_fail:
                ld      a, PP_FAIL
                ret
                endif                   ; PP_STANDALONE (pp_free_run)

; ===========================================================================
; pp_owner_of — return the current ownership tag of page A.
;
; Entry: A = page (0..PP_MAX_PAGES-1).
; Return: A = owner byte (PP_RESERVED for an out-of-range page).
; Clobbers: D, E, HL.
; ===========================================================================
pp_owner_of:
                cp      PP_MAX_PAGES
                jr      nc, pp_owner_oor
                ld      hl, PP_OWNER
                ld      e, a
                ld      d, 0
                add     hl, de
                ld      a, (hl)
                ret
pp_owner_oor:
                ld      a, PP_RESERVED
                ret

; ===========================================================================
; pp_free_count — count currently-FREE pages (the document budget).
;
; Return: A = number of FREE pages (0..PP_NPAGES).
; Clobbers: B, C, HL (A holds the result).
; ===========================================================================
pp_free_count:
                ld      c, 0            ; C = running count
                ld      a, (PP_NPAGES)
                or      a
                jr      z, pp_free_count_done
                ld      b, a
                ld      hl, PP_OWNER
pp_free_count_loop:
                ld      a, (hl)
                cp      PP_FREE
                jr      nz, pp_free_count_next
                inc     c
pp_free_count_next:
                inc     hl
                djnz    pp_free_count_loop
pp_free_count_done:
                ld      a, c
                ret

; ===========================================================================
; Resident state. Never swapped — page_owner[] is the IDE's authority for the
; whole session.
;
; Production build (the assembler includes this file): the caller defines
; PP_TABLE_BASE, a fixed section-D address, so the table is resident and never
; lands in the section-C code stream. Standalone harness build (PP_STANDALONE,
; no PP_TABLE_BASE): the table simply follows the code.
; ===========================================================================
                if defined(PP_TABLE_BASE)
PP_NPAGES:      equ PP_TABLE_BASE       ; physical pages present (PRAMTP+1)
PP_OWNER:       equ PP_TABLE_BASE + 1   ; one ownership byte per physical page
                else
PP_NPAGES:      defs 1
PP_OWNER:       defs PP_MAX_PAGES
                endif
