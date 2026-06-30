; samboot_config.asm — the i176 SAMBOOT BIOS config reader.
;
; The SAMBOOT "BIOS" is a persisted, editable default-boot-record setting held in
; a named Trinity EEPROM chunk. At power-on the patched bootblock (i135d) reads it
; to decide whether to auto-boot a chosen record with no keypress, or fall through
; to a normal boot. This file provides the Z80 READER for that config; the host
; editor (tools/samboot-config/) produces the chunk bytes, and the actual flash of
; the chunk to a real EEPROM is the i135c hardware path (out of scope here).
;
; The config lives in a named chunk found by name via find_index (NOT a fixed
; chunk number — the real-card layout is unknown, and a named chunk is found
; wherever it lives, exactly like the existing "Trinity Network " config chunk the
; bootblock infrastructure already reads). The read reuses eeprom.asm's find_index
; + read_chunk verbatim, adding only the name key + a few bytes of parse — no new
; EEPROM primitive. Design: docs/specs/samboot.md §4; mechanism:
; docs/notes/samboot-bootblock-analysis.md §4.
;
; CHUNK NAME: "SAMBOOT Config  " — exactly 16 bytes (two trailing spaces),
;   space-padded to 16 like "Trinity Network " (16 bytes). "SAMBOOT Config " with
;   one trailing space is only 15 bytes, so a second trailing space pads it to 16.
;
; PAYLOAD FORMAT (in the chunk's 1 KB data; offsets are byte indices into `chunk`):
;   chunk+0  format version  (SAMBOOT_CFG_VERSION = 1)
;   chunk+1  mode            (0 = none / no auto-boot, wait for user;
;                             1 = auto-boot the record at chunk+2..3)
;   chunk+2  record number   low byte  (16-bit little-endian)
;   chunk+3  record number   high byte
;   chunk+4.. reserved, all 0
;
; READER SEMANTICS (docs/specs/samboot.md §4): the result is "no auto-boot" if the
; "SAMBOOT Config  " chunk is absent (find_index miss), OR the version is not
; recognised, OR mode != 1. Only version 1 with mode 1 yields "auto-boot record N".
;
; REGISTER CONTRACT of samboot_read_config (the i135d caller depends on this):
;   In:  nothing (the EEPROM is read via the Trinity ports; no inputs).
;   Out: A  = decision: 0 = no auto-boot (fall through to a normal boot),
;             1 = auto-boot the record in HL.
;        HL = record number (16-bit) when A = 1; undefined when A = 0.
;        CY = mirror of A for a convenient `jr c` / `ret nc`: set when A = 1
;             (auto-boot), clear when A = 0 (no auto-boot).
;   Clobbers: AF, BC, DE, HL, and eeprom.asm's value/part/total/name/chunk storage
;             (the same scratch the bootblock's own config reads use).
;
; VERIFICATION: host-verifiable under the i80 harness (samboot_config_test.go):
; ProgramNamedChunk lays the "SAMBOOT Config  " index entry + the encoded payload
; into the emulated EEPROM, then samboot_read_config runs against the real
; find_index + read_chunk and the decision (A/HL) is asserted. The flash-to-
; hardware write is NOT modelled (reads only) and stays the i135c hardware path.
; Emulation-verified is not hardware-verified (CLAUDE.md §5).

                ; Standalone assembly origin (the host harness invokes
                ; samboot_read_config by symbol; nothing CALLs &8000).
                org     &8000

SAMBOOT_CFG_VERSION:  equ 1                       ; chunk+0 format version
SAMBOOT_CFG_MODE_NONE: equ 0                      ; chunk+1: no auto-boot
SAMBOOT_CFG_MODE_BOOT: equ 1                      ; chunk+1: auto-boot the record

                ; samboot_read_config is a leaf called by symbol (the host harness
                ; and the i135d forked bootloader both enter it directly), so &8000
                ; just falls through into the reader. The bootable build still wants
                ; a jp shim at &8000; the host harness calls the symbol, so skip it.
                if defined(NETBOOT_HOSTTEST)==0
                jp      samboot_read_config
                endif

; ===========================================================================
; samboot_read_config — read the SAMBOOT BIOS config from the EEPROM and decide
; whether to auto-boot a record. Register contract documented in the header.
; ===========================================================================
samboot_read_config:
                ; --- build the 18-byte find_index key: part=1, total=1, name ---
                ; (mirrors the bootblock's "Trinity Network " read; the dumper does
                ;  the same at netboot_dumper.asm:278-285.)
                ld      a, 1
                ld      (part), a               ; eeprom.asm key byte 0
                ld      (total), a              ; eeprom.asm key byte 1
                ld      hl, samboot_cfg_name
                ld      de, name                ; eeprom.asm key bytes 2..17 (16-byte name)
                ld      bc, 16
                ldir

                call    find_index              ; eeprom.asm:161 — sets (value) = chunk number, 0 = miss
                ld      a, (value)
                and     a
                jr      z, src_none             ; chunk absent -> no auto-boot

                call    read_chunk              ; eeprom.asm:312 — chunk[0..1023] filled
                ld      a, (value)
                and     a
                jr      z, src_none             ; read could not resolve the chunk -> no auto-boot

                ; --- parse the payload --------------------------------------
                ld      a, (chunk + 0)          ; format version
                cp      SAMBOOT_CFG_VERSION
                jr      nz, src_none            ; unrecognised version -> no auto-boot

                ld      a, (chunk + 1)          ; mode
                cp      SAMBOOT_CFG_MODE_BOOT
                jr      nz, src_none            ; mode != 1 (none/unknown) -> no auto-boot

                ; mode == auto-boot: return the record number in HL, A=1, CY set.
                ld      hl, (chunk + 2)         ; 16-bit little-endian record number
                ld      a, SAMBOOT_CFG_MODE_BOOT
                scf                             ; CY = 1 (auto-boot)
                ret

src_none:
                ; no auto-boot: A=0, CY clear; HL is left undefined per the contract.
                xor     a                       ; A = 0, CY = 0
                ret

samboot_cfg_name: defm "SAMBOOT Config  "       ; exactly 16 bytes (two trailing spaces)

; wait_ready — poll the Trinity microcontroller busy flag (port &DC bit 3) until
; the EEPROM is ready. eeprom.asm's own copy is commented out because its other
; includers share encdrv.asm's copy; this standalone reader does not include
; encdrv.asm, so it provides the routine here (identical to eeprom.asm:453-457 /
; encdrv.asm:418-421). The harness's EEPROM model reports never-busy, so this
; exits at once (enc28j60.go In(portTrinityCtl) returns 0).
wait_ready:
                in      a, (&DC)
                and     &08
                jr      nz, wait_ready
                ret

; ===========================================================================
; The Trinity flash reader (find_index / read_chunk + the value/chunk/name/part/
; total storage). Included in EVERY build (host-test too): the EEPROM read is the
; emulation-verified path, never carved out.
; ===========================================================================
                include "eeprom.asm"
