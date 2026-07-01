; boot_record.asm — the small trinload-pushable "boot Trinity SD record N" program
; (i316). The non-interactive network-driven counterpart to the i264 hold-key picker:
; a host names a record number, this program HRECORD-selects that record and fires
; ALHK to load + run its AUTO file — so the autonomous loop can build a disk, push it
; into a record (sd_push, i293), then BOOT it (this program) and observe, all without
; a human at the SAM.
;
; THE WHOLE JOB is a thin wrapper around bdos_boot_record (bdos_seam.asm, the i122a
; primitive): set BD_BOOT_RECORD to the requested record number, then
; `call bdos_boot_record`. That primitive does HRECORD-select-record-N + RST 8 / DEFB
; ALHK (hook 136), the B-DOS BOOT "DOS resident" branch (KEYBOARD_BOOT_WORKAROUND.md
; §2) / HAUTO (bdos15a.src.txt:463) — it loads and runs record N's AUTO file, which on
; real hardware never returns (control boots into the loaded image). This program is
; therefore mostly plumbing: read the host-chosen record number, hand it to the
; primitive, and — for the paths that DO return (a floppy-boot that returned control, or
; the harness model of ALHK) — exit cleanly back to trinload, never a bare di;halt (a
; raw halt strands the SAM and costs a power-cycle on every push; Pete, 2026-06-24).
;
; RECORD-NUMBER INPUT — a host-patched config block (the sd_push / netboot_serve idiom).
; BOOT_CONFIG is a small fixed region at the END of the binary that the host launcher
; (tools/trinload-push/boot-record.py) patches by file offset BEFORE pushing:
;   +0  BOOT_CONFIG        1 byte   magic/version = BOOT_CFG_MAGIC_VAL (&5A); the launcher
;                                   sanity-checks it found the block. This label is the
;                                   patch anchor (file offset BOOT_CONFIG - &8000, the
;                                   binary's load org). Never written by the launcher.
;   +1  BOOT_CFG_RECORD    1 byte   the record number to boot (0 = floppy). Patched in.
; An un-patched binary uses the baked default (record 1). Using a patched byte (not a
; network message) keeps the program trivial: there is no wire loop to run — trinload's
; X packet lands us at &8000, we read RAM, boot. This mirrors netboot_serve's
; SERVE_CONFIG (src/netboot/netboot_serve.asm) and its host launcher trinpush-serve.py.
;
; VERIFICATION (emulation-first, CLAUDE.md rule 7): boot_record_test.go loads this
; binary under the flat harness, attaches the BDOSStore (which models the RST 8 HRECORD
; + ALHK dispatch — bdos_store.go), patches BOOT_CFG_RECORD to a chosen record, runs
; boot_record_main, and asserts the store HRECORD-SELECTED that record and captured
; exactly one ALHK boot against it. The actual on-hardware auto-load + boot routes
; through the B-DOS loader + the Trinity SD driver and stays hardware-gated (CLAUDE.md
; §5) — the real boot shot is a SEPARATE follow-up, NOT this program's verification.
; Emulation-verified is not hardware-verified.
;
; ONE BUILD, FLAG-FREE (no NETBOOT_HOSTTEST carve-out, i231): this file contains no
; `if defined(NETBOOT_HOSTTEST)` directive. It composes only bdos_seam.asm, and calls
; only its unconditional bdos_boot_record / bdos_select_record path (no free-record
; scan, no CMD17/CMD24 SD writes), so it needs neither NETBOOT_REAL_LISTREAD nor
; NETBOOT_WANT_CLAIM.

                org     &8000

                ; Entry: trinload's X packet does `out (HMPR),P; jp &8000`, landing
                ; here. The host harness runs boot_record_main by symbol; this jp is
                ; the hardware/trinload entry.
                jp      boot_record_main

; ===========================================================================
; Composed include — the B-DOS storage seam supplies bdos_boot_record (the i122a
; HRECORD-select + ALHK primitive), bdos_select_record, and BD_BOOT_RECORD. It comes
; FIRST (right after the boot jp) so every symbol it defines is resolved before the code
; below references it (pyz80 is single-symbol-table, no forward equ-of-a-later-label).
; No NETBOOT_REAL_LISTREAD / NETBOOT_WANT_CLAIM: this program never scans the free-record
; list nor writes a record, so it does not include sd_csd.asm.
; ===========================================================================
                include "bdos_seam.asm"        ; bdos_boot_record / bdos_select_record / BD_BOOT_RECORD

; ===========================================================================
; boot_record_main — the bootable / trinload entry.
; ===========================================================================
boot_record_main:
                di

                ; --- read the host-chosen record number from the config block -----
                ; An un-patched binary carries the baked default (BOOT_CFG_RECORD = 1);
                ; the host launcher patches it to the record to boot before pushing.
                ld      a, (BOOT_CFG_RECORD)
                ld      (BD_BOOT_RECORD), a

                ; --- HRECORD-select the record + fire ALHK to boot its AUTO file --
                ; bdos_boot_record (bdos_seam.asm:1104) contract:
                ;   In:  BD_BOOT_RECORD  1 byte  the record number (0 = floppy).
                ;   Out: HRECORD-selects the record, then RST 8 / DEFB ALHK loads+runs
                ;        its AUTO file. On real hardware ALHK NEVER RETURNS — control
                ;        boots into the loaded image, so nothing below runs. The harness
                ;        models the boot as a captured event and returns, so the code
                ;        below is exercised only in emulation / on a floppy-boot that
                ;        returned control.
                call    bdos_boot_record

                ; If we get here (harness, or a returned floppy-boot), exit cleanly back
                ; to trinload (it pushed its `start` as our return address), never a bare
                ; di;halt. On hardware this is unreached because ALHK booted.
                ei
                ret

; ===========================================================================
; BOOT_CONFIG — the record-number config block (the sd_push / netboot_serve idiom).
; A small, fixed, host-patchable region at the END of the binary so the host launcher
; (tools/trinload-push/boot-record.py) can set the record to boot by file offset before
; the program runs. Read straight from RAM at boot (cheap; never re-read). An un-patched
; binary uses the baked default (record 1).
;
; BYTE LAYOUT (the launcher patches by file offset from the BOOT_CONFIG symbol — the
; block's anchor at +0; keep STABLE):
;   +0  BOOT_CONFIG      1 byte   magic/version = BOOT_CFG_MAGIC_VAL (&5A); the launcher
;                                 sanity-checks it found the block. Patch anchor (file
;                                 offset BOOT_CONFIG - &8000). Never written.
;   +1  BOOT_CFG_RECORD  1 byte   the record number to boot (0 = floppy). Patched in.
; Total: 2 bytes.
; ===========================================================================
BOOT_CFG_MAGIC_VAL:   equ &5A                  ; the magic/version byte value

BOOT_CONFIG:          defb BOOT_CFG_MAGIC_VAL   ; +0 magic/version (patch anchor)
BOOT_CFG_RECORD:      defb 1                    ; +1 baked default: record 1 (patched by the host)

length:               equ $ - boot_record_main  ; code length (diagnostic)
