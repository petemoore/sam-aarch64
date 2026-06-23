; pool_boot.asm — boot-time integration of the page allocator (item i2b).
;
; Stands the pagepool.asm allocator up LIVE on the production assembler's paged
; boot path: at boot it surveys the machine and builds the page_owner[] table,
; reserving the pages the assembler statically occupies and leaving the rest
; FREE for future pool consumers (the IDE document, and the i23/i24 IN/OUT
; ceiling lifts that migrate those buffers to alloc_page).
;
; This brick is ADDITIVE: it does not yet migrate any buffer to the pool, so
; pages 0..15 (BASIC/system/screen + the assembler's static off-axis pages
; 4..15) all stay statically used and are RESERVED here. Until a consumer
; claims pool pages, the FREE pages (16..PRAMTP on a >256 KB machine) are simply
; tracked. The BASIC-page reclaim (NEW BASIC) and the screen-tail survey
; (spec §4.2 / §7.3) belong with the IDE shell, not this brick.
;
; Authority: docs/specs/ide-memory-model-design.md §4.2 (boot-time pool sizing)
; and §6 (the boot self-test). Allocator: src/pagepool.asm.

; PRAMTP (&5CB4): SAM system variable holding the highest physical page number
; present (0x0F on a 256 KB machine, 0x1F on 512 KB). The ROM sets it at
; startup, so it is valid when the assembler runs (CALL 32768 from BASIC).
PRAMTP:         equ &5CB4

; POOL_FIRST_FREE: the first page the pool may hand out. Pages 0..POOL_FIRST_FREE-1
; are the base 256 KB the current system occupies — 0..3 BASIC/system/screen/DOS,
; 4 ENCTAB, 5..6 OUT, 7..12 IN, 13..14 payloads, 15 disasm
; (docs/notes/memory-layout.md) — and are RESERVED so the allocator never hands
; them out. Expansion RAM (16..PRAMTP on a >256 KB machine) becomes the FREE pool.
;
; CONSUMER CAVEAT: a 512 KB SAM may load its DOS into expansion RAM (page >15),
; which this reserve set does not cover. That is safe for THIS brick — nothing
; writes pool-page contents yet, the self-test only tags pages in page_owner[].
; Before any consumer WRITES to a pool page (i23/i24's buffer migration, the IDE
; document), it must extend the survey to reserve the real DOSFLG page (&5BC2)
; and the VMPR screen pages per spec §4.2.
POOL_FIRST_FREE: equ 16

; ===========================================================================
; pool_boot_init — size the pool for the running machine and reserve the
; statically-used pages. Runs unconditionally at boot (production + test).
;
; Reads PRAMTP -> page count; pp_init marks [0,count) FREE and [count,32)
; RESERVED (absent); then reserves pages 0..POOL_FIRST_FREE-1.
; Clobbers: A, B, C, D, E, HL.
; ===========================================================================
pool_boot_init:
                ld      a, (PRAMTP)
                inc     a                       ; A = page count (PRAMTP + 1)
                call    pp_init                 ; clobbers A, B, C, HL
                ld      b, POOL_FIRST_FREE      ; pages to reserve
                ld      c, 0                    ; page index
pool_boot_reserve:
                ld      a, c
                call    pp_reserve              ; preserves B, C; clobbers A, D, E, HL
                inc     c
                djnz    pool_boot_reserve
                ret

; The boot self-test that exercises this live pool (the spec §6 claim-all /
; free-all round-trip) lives off-axis in src/test_pagepool.asm — invoked from
; the page-12 cluster (src/test_offaxis_cluster.asm), so its code stays out of
; the tight section-C test-variant budget. It reaches pp_*/PP_*/fail_with_tag
; via the cluster's --importfile=assembler.sym, the same as every other
; relocated suite.
