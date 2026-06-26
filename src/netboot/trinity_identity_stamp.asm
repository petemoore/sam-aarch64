; trinity_identity_stamp.asm — the i213 Trinity firmware IDENTITY STAMP reader.
;
; When we customise the Trinity firmware (the i135c bootblock flash) we write a
; small magic-signature + version marker into a named EEPROM chunk. This file is
; the Z80 READER for that marker: it lets our software DETECT whether our patched
; firmware is the one actually running, and handle a mismatch gracefully.
;
; Why (Pete, 2026-06-23): the B-DOS in control is NOT guaranteed to be the one we
; flashed — B-DOS can be loaded from FLOPPY (the traditional DOS-load path), the
; normal case for anyone without our customised ROM chip. So our software cannot
; assume the EEPROM's patched firmware is present; this stamp lets it tell. It is
; NOT capability negotiation (record access through the EEPROM B-DOS is transparent
; — i214 WONTFIX), just an identity marker.
;
; The stamp lives in a named chunk found BY NAME via find_index (NOT a fixed chunk
; number — the real-card layout is unknown, and a named chunk is found wherever it
; lives, exactly like the existing "Trinity Network " / "SAMBOOT Config  " chunks
; the bootblock infrastructure already reads). The read reuses eeprom.asm's
; find_index + read_chunk verbatim, adding only the name key + a few bytes of
; parse — no new EEPROM primitive. The host format authority + round-trip test are
; tools/netboot-oracle/trinityfw + .../z80/trinity_identity_stamp_test.go.
;
; CHUNK NAME: "Trinity Firmware" — exactly 16 bytes (no padding needed), matching
;   the 16-byte EEPROM index-entry name field like "Trinity Network ".
;
; PAYLOAD FORMAT (offsets are byte indices into the chunk's 1 KB data):
;   chunk+0..3  magic signature  ("SAMB" = &53 &41 &4D &42) — belt-and-braces over
;                                 the unique name, so an unrelated same-named chunk
;                                 is not mistaken for our stamp.
;   chunk+4     firmware version (TRINITY_STAMP_VERSION = 1; bump per patch change)
;   chunk+5..   reserved, all 0
;
; READER SEMANTICS: "our firmware present, version N" only when the chunk is found
; AND the 4-byte magic matches AND the version byte is non-zero. Chunk absent, or
; magic mismatch, or version 0 -> "not our firmware" (stock / floppy-loaded B-DOS).
;
; REGISTER CONTRACT of trinity_read_stamp:
;   In:  nothing (the EEPROM is read via the Trinity ports; no inputs).
;   Out: A  = firmware version (>= 1) when our stamp is present; 0 when not.
;        CY = mirror of "present" for a convenient `jr c` / `ret nc`: set when our
;             firmware is detected (A >= 1), clear otherwise (A = 0).
;   Clobbers: AF, BC, DE, HL, and eeprom.asm's value/part/total/name/chunk storage
;             (the same scratch the bootblock's own config reads use).
;
; VERIFICATION: host-verifiable under the i80 harness (trinity_identity_stamp_test.go):
; ProgramNamedChunk lays the "Trinity Firmware" index entry + the encoded payload
; into the emulated EEPROM, then trinity_read_stamp runs against the real find_index
; + read_chunk and the decision (A/CY) is asserted. The flash-to-hardware WRITE is
; NOT modelled (reads only) and stays the i135c hardware path (private fork).
; Emulation-verified is not hardware-verified (CLAUDE.md §5).

                org     &8000

TRINITY_STAMP_VERSION: equ 1                      ; chunk+4 firmware/patch version
TRINITY_STAMP_MAGIC0:  equ &53                    ; 'S'  chunk+0
TRINITY_STAMP_MAGIC1:  equ &41                    ; 'A'  chunk+1
TRINITY_STAMP_MAGIC2:  equ &4D                    ; 'M'  chunk+2
TRINITY_STAMP_MAGIC3:  equ &42                    ; 'B'  chunk+3

                ; A standalone/bootable use enters at &8000 via this shim. The host
                ; harness invokes trinity_read_stamp by symbol and never CALLs &8000,
                ; so the shim is harmless dead code there — included in every build
                ; (i231b: no carve-out).
                jp      trinity_read_stamp

; ===========================================================================
; trinity_read_stamp — read the firmware identity stamp from the EEPROM and report
; whether our patched firmware is present. Register contract in the header.
; ===========================================================================
trinity_read_stamp:
                ; --- build the 18-byte find_index key: part=1, total=1, name ---
                ; (mirrors the bootblock's "Trinity Network " read; smoke_test.asm
                ;  and samboot_config.asm do the same.)
                ld      a, 1
                ld      (part), a               ; eeprom.asm key byte 0
                ld      (total), a              ; eeprom.asm key byte 1
                ld      hl, trinity_stamp_name
                ld      de, name                ; eeprom.asm key bytes 2..17 (16-byte name)
                ld      bc, 16
                ldir

                call    find_index              ; sets (value) = chunk number, 0 = miss
                ld      a, (value)
                and     a
                jr      z, tis_none             ; chunk absent -> not our firmware

                call    read_chunk              ; chunk[0..1023] filled
                ld      a, (value)
                and     a
                jr      z, tis_none             ; read could not resolve the chunk

                ; --- verify the 4-byte magic ---------------------------------
                ld      a, (chunk + 0)
                cp      TRINITY_STAMP_MAGIC0
                jr      nz, tis_none
                ld      a, (chunk + 1)
                cp      TRINITY_STAMP_MAGIC1
                jr      nz, tis_none
                ld      a, (chunk + 2)
                cp      TRINITY_STAMP_MAGIC2
                jr      nz, tis_none
                ld      a, (chunk + 3)
                cp      TRINITY_STAMP_MAGIC3
                jr      nz, tis_none            ; magic mismatch -> not our stamp

                ; --- the version byte ----------------------------------------
                ld      a, (chunk + 4)
                and     a
                jr      z, tis_none             ; version 0 = malformed -> not ours
                scf                             ; CY = 1 (present); A = version
                ret

tis_none:
                ; not our firmware: A = 0, CY clear.
                xor     a                       ; A = 0, CY = 0
                ret

trinity_stamp_name: defm "Trinity Firmware"     ; exactly 16 bytes (no padding)

; wait_ready — poll the Trinity microcontroller busy flag (port &DC bit 3) until
; the EEPROM is ready. eeprom.asm's own copy is commented out because its other
; includers share encdrv.asm's copy; this standalone reader does not include
; encdrv.asm, so it provides the routine here (identical to encdrv.asm:418-421 /
; samboot_config.asm). The harness's EEPROM model reports never-busy, so this exits
; at once (enc28j60.go In(portTrinityCtl) returns 0).
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
