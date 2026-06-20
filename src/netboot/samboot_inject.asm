; samboot_inject.asm — i135d emulation prototype of the patched-bootblock
; decision+dispatch (samboot-bootblock-analysis.md §3 TODO hook). It models only
; the INJECTED SEGMENT the SAMBOOT flash adds between the bootblock's `CALL
; stripes` and `restore:` — the reset->ROM->bootblock->B-DOS-load chain and the
; restore/JP-4143 exit around it are hardware (i135c), not modelled here.
;
; The segment:
;   1. redraw the MGT opening stripes UNCONDITIONALLY (i112 — the injected code
;      trampled the opening screen, so it must repaint it on every boot),
;   2. read the SAMBOOT BIOS config (i176, samboot_read_config),
;   3. auto-boot the configured record (i122a, bdos_boot_record) or, when the
;      config says "no auto-boot", fall through to a normal boot (RET to caller).
;
; Both primitives already exist and are harness-tested verbatim:
;   * samboot_read_config — src/netboot/samboot_config.asm (i176). In: nothing.
;     Out: A=1 + HL=record + CY set (auto-boot), or A=0 + CY clear (no auto-boot).
;   * bdos_boot_record    — src/netboot/bdos_seam.asm (i122a). In: BD_BOOT_RECORD
;     (1 byte). HRECORD-selects the record, then fires ALHK to load+run its AUTO
;     file. ALHK does NOT return on hardware; the harness captures the boot.
; So this file is pure glue: control flow + a stripes stub + the host test. No Go
; authority (nothing new to encode — it dispatches already-ported primitives).
;
; BUILD: assembled WITHOUT NETBOOT_HOSTTEST (like netboot_client_boot), because
; the test must capture the real RST 8 ALHK boot. bdos_boot_record and the whole
; RST 8 hook dispatch live behind `if defined(NETBOOT_HOSTTEST)==0` in
; bdos_seam.asm; under NETBOOT_HOSTTEST they are excluded, leaving `jp
; bdos_boot_record` an undefined symbol and no ALHK dispatch for the harness's
; AttachBDOS to intercept. The harness drives this build by symbol (CallEntry)
; and intercepts RST 8 at run time via AttachBDOS, exactly as bdos_boot_test.go
; drives netboot_client_boot.bin. eeprom.asm's find_index/read_chunk (the EEPROM
; read) are NOT host-gated, so the config read stays emulation-verified either
; way. Design: docs/specs/samboot.md §4/§6; mechanism:
; docs/notes/samboot-bootblock-analysis.md §2/§3.
;
; THE HONESTY LINE (CLAUDE.md §5): the decision+dispatch control flow + the
; UNCONDITIONAL stripes redraw (the i112 fold) ARE emulation-verified — the test
; asserts the boot decision in every branch AND that samboot_stripes is CALLED on
; every path (the probe counter). The stripes PIXELS (the MODE-2 palette+banner
; blit) and the real reset->ROM->bootblock chain + the ALHK auto-load are
; HARDWARE-FIRST (i135c), gated out of the host build exactly like
; bdos_picker.asm:563 picker_render. Emulation-verified is not hardware-verified.

                ; The two primitives this glue dispatches. samboot_config.asm
                ; supplies the single `org &8000`; bdos_seam.asm orgs only under
                ; NETBOOT_STANDALONE (undefined here), so it appends — exactly one
                ; org across this translation unit (verified from the .map).
                include "samboot_config.asm"     ; samboot_read_config (+ eeprom.asm reader)
                include "bdos_seam.asm"           ; bdos_boot_record + BD_BOOT_RECORD

; ===========================================================================
; samboot_inject — the injected bootblock segment's decision+dispatch.
;
; In:  nothing (samboot_read_config reads the EEPROM via the Trinity ports).
; Out: on auto-boot, control transfers to bdos_boot_record and never returns on
;      hardware (ALHK boots into the AUTO file); on no-auto-boot, RET to the
;      caller (the bootblock continues to a normal boot / BASIC).
; ===========================================================================
samboot_inject:
                call    samboot_stripes         ; ALWAYS redraw the MGT stripes (i112 fold)
                call    samboot_read_config     ; i176: A=1/HL=record (CY set) or A=0 (CY clr)
                ret     nc                      ; A=0: no auto-boot -> fall through to BASIC
                ld      a, l                    ; BD_BOOT_RECORD is 1 byte (record <= 255 on a
                                                ; real card; a >255 record would need
                                                ; bdos_select_record widening — out of scope)
                ld      (BD_BOOT_RECORD), a
                jp      bdos_boot_record        ; i122a: HRECORD-select + ALHK-boot, no keypress

; ===========================================================================
; samboot_stripes — redraw the MGT opening stripes (i112). The injected segment
; trampled the opening screen, so the patched bootblock repaints it on every
; boot. The decision-free MODE-2 palette+banner blit is HARDWARE-FIRST: the
; pixels are drawn for real at i135c and are gated out of the host build exactly
; like bdos_picker.asm:563 picker_render. The host test asserts only that this is
; CALLED — in BOTH the auto-boot and the no-auto-boot branch — via the probe
; counter, which proves the redraw is unconditional and there is no wait-for-key.
; ===========================================================================
samboot_stripes:
                ld      hl, SAMBOOT_STRIPES_CALLS
                inc     (hl)                    ; probe: count this call (host test reads it)
                if defined(NETBOOT_HOSTTEST)==0
                ; Real-hardware stripes (analysis §2): clearscn, then step the
                ; PALTAB (&55D8) rainbow into LINICOLS (&5600) by &0B per line up
                ; to &A6, and print the MGT banner via RST 16. Drawn for real at
                ; i135c against the captured bootblock; unverifiable in the flat
                ; harness (no screen RAM model), so gated here and asserted only
                ; by the call-probe above.
                endif
                ret

SAMBOOT_STRIPES_CALLS:  defs 1                  ; host-test probe: stripes call count
