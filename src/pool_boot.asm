; pool_boot.asm — boot-time integration of the page allocator (item i2b).
;
; Stands the pagepool.asm allocator up LIVE on the production assembler's paged
; boot path: at boot it surveys the machine and builds the page_owner[] table,
; reserving the pages the assembler statically occupies and leaving the rest
; FREE for future pool consumers (the IDE document, and the i23/i24 IN/OUT
; ceiling lifts that migrate those buffers to alloc_page).
;
; The assembler's IN and OUT buffers are live pool consumers (i23 / i24):
; pages 5..12 stay FREE here and are claimed as contiguous runs at assemble
; time. The remaining static off-axis pages (4 = ENCTAB, 13..15 = payloads /
; disasm) are RESERVED. The BASIC-page reclaim (NEW BASIC) and the screen-tail
; survey (spec §4.2 / §7.3) belong with the IDE shell, not this brick.
;
; Authority: docs/specs/ide-memory-model-design.md §4.2 (boot-time pool sizing)
; and §6 (the boot self-test). Allocator: src/pagepool.asm.

; PRAMTP (&5CB4): SAM system variable holding the highest physical page number
; present (0x0F on a 256 KB machine, 0x1F on 512 KB). The ROM sets it at
; startup, so it is valid when the assembler runs (CALL 32768 from BASIC).
PRAMTP:         equ &5CB4

; Reserved-page set. The statically-used pages the allocator never hands out:
;   0..3  BASIC / system / screen / DOS
;   4     ENCTAB
;   13..14 production payloads / ZX0 staging
;   15    disassembler
; (docs/notes/memory-layout.md). The two reserved ranges are 0..4 and 13..15.
;
; Pages 5..12 are pool pages, claimed by the two buffer CONSUMERS:
;   IN  — load_in_file allocates a contiguous run via pp_alloc_run(PP_IN)
;         at assemble time (i23; docs/plans deleted, see PR history).
;   OUT — reset_out_buffer allocates a contiguous run via
;         pp_alloc_run(PP_OUT) between the passes, sized from the pass-1
;         total, and pp_free_run's it on the next assemble (i24).
; Expansion RAM (16..PRAMTP on a >256 KB machine) is also FREE, so large
; buffers can land in the 16..31 run. ENCTAB/payloads/disasm migrate to
; the pool in later i2 bricks; until then they stay statically reserved
; here.
;
; CONSUMER CAVEAT: a 512 KB SAM may load its DOS into expansion RAM (page >15),
; which this reserve set does not cover. IN/OUT-page contents are written only
; into pages this survey hands out from the FREE set (5..12, or 16..31 which on
; such a machine could collide with a DOS that relocated there). Before the IDE
; document or the buffers write pool pages on a DOS-in-expansion-RAM machine,
; extend the survey to reserve the real DOSFLG page (&5BC2) and the VMPR screen
; pages per spec §4.2.
POOL_RESV_A_BASE: equ 0          ; reserved range A: pages 0..4
POOL_RESV_A_N:    equ 5
POOL_RESV_B_BASE: equ 13         ; reserved range B: pages 13..15
POOL_RESV_B_N:    equ 3

; ===========================================================================
; pool_boot_init — size the pool for the running machine and reserve the
; statically-used pages. Runs unconditionally at boot (production + test).
;
; Reads PRAMTP -> page count; pp_init marks [0,count) FREE and [count,32)
; RESERVED (absent); then reserves ranges A (0..4) and B (13..15), leaving the
; buffer pages 5..12 (and 16..PRAMTP) FREE for the pool.
; Clobbers: A, B, C, D, E, HL.
; ===========================================================================
pool_boot_init:
                ld      a, (PRAMTP)
                inc     a                       ; A = page count (PRAMTP + 1)
                call    pp_init                 ; clobbers A, B, C, HL
; Reserve range A: pages 0..4.
                ld      b, POOL_RESV_A_N
                ld      c, POOL_RESV_A_BASE
pool_boot_resv_a:
                ld      a, c
                call    pp_reserve              ; preserves B, C; clobbers A, D, E, HL
                inc     c
                djnz    pool_boot_resv_a
; Reserve range B: pages 13..15.
                ld      b, POOL_RESV_B_N
                ld      c, POOL_RESV_B_BASE
pool_boot_resv_b:
                ld      a, c
                call    pp_reserve
                inc     c
                djnz    pool_boot_resv_b
                ret

; The boot self-test that exercises this live pool (the spec §6 claim-all /
; free-all round-trip) lives off-axis in src/test_pagepool.asm — invoked from
; the page-12 cluster (src/test_offaxis_cluster.asm), so its code stays out of
; the tight section-C test-variant budget. It reaches pp_*/PP_*/fail_with_tag
; via the cluster's --importfile=assembler.sym, the same as every other
; relocated suite.
