; sd_listread_standalone.asm — host-test harness fixture for the i141 REAL
; card-absolute list-sector read (bd_list_read_hw, sd_csd.asm).
;
; Built with NETBOOT_REAL_LISTREAD (Makefile netboot-sd-listread) and WITHOUT
; NETBOOT_HOSTTEST, so the detection routines (bdos_read_list_sector,
; bdos_record_entry, bdos_find_free_record — which live behind the
; `if defined(NETBOOT_HOSTTEST)==0` production block in bdos_seam.asm) ARE
; assembled, and bdos_read_list_sector tail-calls the real CMD17 read. The real
; read uses no RST 8, so the routines the test drives (read-list / find-free) need
; no B-DOS hook dispatch — they talk straight to the modelled SD-SPI ports.
; NETBOOT_WANT_CLAIM is left undefined (the record-claim write path is a separate
; increment; i141 is the READ only).
;
; sd_listread_test.go Loads this, attaches the i145c/i145h SD model, SeedSectors the
; record-list sectors at their card-absolute LBAs, and asserts the detection routines
; read them back through the raw CMD17 SPI path. Same module set the serve/client
; boot images include; verified in isolation (CLAUDE.md §5: emulation ≠ hardware).

                org     &8000

                include "encdrv.asm"           ; wait_ready (the shared &DC busy poll)
                include "bdos_seam.asm"         ; the detection routines + BD_RECORDS/BD_LIST_* storage
                include "sd_csd.asm"            ; the CSD read + the i141 bd_list_read_hw CMD17 read
