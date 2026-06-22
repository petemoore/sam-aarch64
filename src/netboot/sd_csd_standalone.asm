; sd_csd_standalone.asm — host-test harness fixture for the i145b CSD->BD_RECORDS
; decode (sd_csd.asm). Assembled standalone (org &8000) with NETBOOT_HOSTTEST so the
; Go test (csd_to_bd_records_test.go) can Load it into a flat 64 KB space, attach the
; i145c SD model with a configured CSD, and CALL csd_set_bd_records by symbol to
; assert BD_RECORDS is COMPUTED from the modelled card (not injected).
;
; It pulls in the real dependencies sd_csd.asm needs: wait_ready (encdrv.asm) and
; BD_RECORDS (bdos_seam.asm, built host-test so its RST 8 hook dispatch is excluded).
; This is the same set the serve/client boot images include, exercised in isolation
; so the decode is verified even though the full driver does not fit the boot budget
; of those images (i145b finding).

                org     &8000

                include "encdrv.asm"           ; wait_ready (the shared &DC busy poll)
                include "bdos_seam.asm"         ; BD_RECORDS (+ the picker seam storage)
                include "sd_csd.asm"            ; the i145b CSD read + decode under test
