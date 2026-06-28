; bdos_seam.asm — the SAM-side netboot storage seam: the UIFA/DIFA field
; arithmetic that glues the i83 TFTP server (serve a file by name) and the i82
; TFTP client (write a received file by name) to the B-DOS / SAMDOS-2 file-I/O
; hooks (HGTHD / HLOAD / HSAVE over the HRECORD-selected flat store).
;
; This is the SAM-side Z80 port of the netboot-oracle Go authority
; tools/netboot-oracle/bdos/bdos.go (NameToUIFA / DifaToSize / SaveUIFA). The
; field conventions are taken verbatim from the assembler's own real hook call
; sites — src/io.asm (fill_uifa), src/loader.asm (load_payload_generic's DIFA
; read), src/main_loop.asm (save_out_file's length encoding) — and from
; docs/specs/samdos-file-io.md. This is a port of established, in-tree
; conventions, not new design.
;
; THREE host-verifiable routines (pure arithmetic + memory writes):
;
;   bdos_name_to_uifa:
;     build the 48-byte UIFA name block HGTHD / HSAVE read for a CODE file —
;     type 19 at +0, the NUL-terminated filename space-padded into the 10-byte
;     name field (+1..10), 4 spaces in the ext field (+11..14), 0xFF filler in
;     +15..47 (the fill_uifa convention).
;   bdos_difa_to_size:
;     decode the DIFA HGTHD deposits into a 32-bit byte length — pages-count
;     (+34) * 16384 + length-mod-16K (+35..36 LE, masking the +36 bit-7 marker).
;     This is the size the OACK `tsize` reports + the byte count the server
;     streams.
;   bdos_fill_save_uifa:
;     the inverse for the i82 client write-out — build the UIFA HSAVE reads from
;     a filename + a source page + a section-C offset + a total byte length
;     (UIFA[31]=page, [32..33]=offset, [34]=size>>14, [35..36]=size&0x3FFF).
;
; FREE-RECORD DETECTION — read the record list directly. The Trinity SD card has
; a central RECORD LIST in its boot area (sectors 1..base-1, before record 1;
; 16-byte entries, 32 per sector). It IS reachable from a user program: the
; `RECORD` BASIC command lists every record's name (bdos15a.src.txt:886-906), so
; the Z80 plainly reads it; B-DOS itself reads it via a clean sequential read of
; the list sectors (find.rec -> sel.base -> seek.base -> hd.lbuf,
; bdos15a.src.txt:906-919 — no HRECORD, no error 81, no error trap). The narrow
; true fact is only that there is no DEDICATED RST-8 hook named "list records".
; The robust design treats the on-card layout as a FROZEN INTERFACE and reads the
; list sectors ourselves, parsing the 16-byte entries: an entry whose first name
; byte (bit 7 = write-protect, masked off) is 0 is unnamed/free (the frec3x test,
; bdos15a.src.txt:946-948); a named entry is in use. This depends only on frozen
; interfaces (the Trinity port map, the SD protocol, the on-card B-DOS format —
; identical 1.5a<->1.5t per the i71 fork analysis) and on ZERO B-DOS internal
; routine addresses (those DO relocate between versions). No nmi.sp trap, no
; version-specific addresses. See docs/specs/trinity-record-detection-design.md.
;
; The list (the card-level view) enumerates records and finds a free one; a
; record's per-record identity (the selected-record view) is read from its OWN
; first directory sector after selecting it: HRECORD n -> bdos_read_sector(0,1) ->
; bdos_inspect_record, which reads the "BDOS" stamp (+232) and the disk label
; (+210) exactly as B-DOS get.label does (bdos15a.src.txt:2834-2858). That
; per-record inspect is what this seam ships; it backs the confirm-before-overwrite
; gate (the Trinity SD card is a SHARED user resource — never clobber a record we
; did not create). The card-absolute list-read routine is the next increment
; (built against the harness card model first — emulation-first).
;
; THE HONESTY LINE (CLAUDE.md §5): these routines are host-verifiable — they
; only build / decode memory. The actual hook DISPATCH (RST 8 / DEFB 129 HGTHD,
; / DEFB 130 HLOAD, / DEFB 132 HSAVE, and the HRECORD &9C record select) is NOT
; host-verifiable in this file's own execution: the flat-memory koron-go/z80
; harness has no ROM, no DOS bank, and no real RST 8 vector. Instead bdos_store.go
; MODELS the RST 8 dispatch (which hook, with which UIFA / record), so the
; bootable binaries that include this seam (client / serve / http / sd_push)
; exercise the dispatch path in emulation through that model; real B-DOS
; persistence stays UNVERIFIED until exercised on real Trinity hardware (the i62
; dual-run proved the hooks round-trip on real SAMDOS-2 + B-DOS-AL backends; the
; netboot fetch end-to-end is the final integration gate). Emulation-verified is
; not hardware-verified.
;
; PROVENANCE: the UIFA/DIFA layout + hook codes are docs/specs/samdos-file-io.md
; + src/sam_io.inc; the length encoding is src/main_loop.asm save_out_file +
; src/loader.asm load_payload_generic. The Go authority is bdos/bdos.go.
;
; VERIFICATION: host-verifiable. tools/netboot-oracle/z80 assembles this file
; (with NETBOOT_HOSTTEST so the hook dispatch is excluded), runs each routine,
; and byte-compares the built UIFA / decoded size against the Go authority
; (bdos_seam_test.go). The RST 8 hook calls are NOT exercised host-side.

                ; org only when assembled standalone (the host harness builds
                ; this file on its own with -D NETBOOT_STANDALONE=1); when a
                ; state-machine file `include`s it, that file supplies the org.
                if defined(NETBOOT_STANDALONE)
                org     &8000
                endif

; --- UIFA / DIFA field layout (mirror bdos/bdos.go + src/sam_io.inc) --------
BD_UIFA_LEN:      equ 48
BD_OFF_TYPE:      equ 0                  ; file type (19 = CODE)
BD_OFF_NAME:      equ 1                  ; 10 bytes, space-padded
BD_OFF_EXT:       equ 11                 ; 4 bytes, space-padded
BD_OFF_PAGE:      equ 31                 ; HSAVE source page (low 5 bits)
BD_OFF_LOAD:      equ 32                 ; HSAVE source offset (LE word)
BD_OFF_PAGES:     equ 34                 ; pages-count
BD_OFF_LENGTH:    equ 35                 ; length-mod-16K (LE word)
BD_NAME_LEN:      equ 10                 ; the 10-char B-DOS file-name field width
BD_REC_NAME_LEN:  equ 16                 ; the full 16-char central-list record-name field
BD_EXT_LEN:       equ 4
BD_TYPE_CODE:     equ 19
BD_DIFA_MARKER:   equ &7F                ; mask to clear the +36 bit-7 marker
BD_SPACE:         equ &20                ; ' ' (the name/ext padding byte)

; ---------------------------------------------------------------------------
; bdos_name_to_uifa — build the UIFA name block for a CODE file.
;
; In:  BD_NAME_PTR  2 bytes  pointer to the NUL-terminated filename
; Out: the 48-byte UIFA name block at BD_UIFA (type + name + ext + 0xFF filler);
;      ready for HGTHD / HSAVE (which read IX -> BD_UIFA on real hardware).
;
; The on-card SAM name is the fetched filename with any dotted suffix dropped,
; truncated to 10 chars (recovery.mgt -> recovery).
; ---------------------------------------------------------------------------
bdos_name_to_uifa:
                ; type = CODE
                ld      a, BD_TYPE_CODE
                ld      (BD_UIFA + BD_OFF_TYPE), a

                ; name field: copy up to 10 chars, stopping at NUL or '.', then pad.
                ld      hl, (BD_NAME_PTR)      ; source filename
                ld      de, BD_UIFA + BD_OFF_NAME
                ld      b, BD_NAME_LEN
bnu_name:
                ld      a, (hl)
                or      a
                jr      z, bnu_pad             ; NUL: pad the remainder
                cp      "."                    ; extension separator
                jr      z, bnu_pad             ; drop the dotted suffix; pad the rest
                ld      (de), a
                inc     hl
                inc     de
                djnz    bnu_name
                jr      bnu_ext                ; 10 chars consumed without NUL/'.': stop
bnu_pad:
                ld      a, BD_SPACE
bnu_pad_loop:
                ld      (de), a
                inc     de
                djnz    bnu_pad_loop
bnu_ext:
                ; ext field: 4 spaces.
                ld      de, BD_UIFA + BD_OFF_EXT
                ld      b, BD_EXT_LEN
                ld      a, BD_SPACE
bnu_ext_loop:
                ld      (de), a
                inc     de
                djnz    bnu_ext_loop

                ; bytes 15..47: 0xFF filler.
                ld      de, BD_UIFA + BD_OFF_EXT + BD_EXT_LEN
                ld      b, BD_UIFA_LEN - (BD_OFF_EXT + BD_EXT_LEN)
                ld      a, &FF
bnu_fill:
                ld      (de), a
                inc     de
                djnz    bnu_fill
                ret

; ---------------------------------------------------------------------------
; bdos_difa_to_size — decode a DIFA's length fields into a 32-bit byte length.
;
; In:  the 48-byte DIFA at BD_DIFA (as HGTHD deposits it).
; Out: BD_SIZE  4 bytes LE  = pages(+34) * 16384 + lengthMod16K(+35..36, marker
;      bit cleared). 32-bit because pages*16384 can exceed 16 bits.
; Clobbers: A, BC, DE, HL.
; ---------------------------------------------------------------------------
bdos_difa_to_size:
                ; Seed the 32-bit accumulator BD_SIZE with lengthMod16K (the low
                ; 16 bits; high 16 bits zero). Clear the +36 bit-7 marker.
                ld      a, (BD_DIFA + BD_OFF_LENGTH)
                ld      (BD_SIZE), a
                ld      a, (BD_DIFA + BD_OFF_LENGTH + 1)
                and     BD_DIFA_MARKER         ; clear bit 7
                ld      (BD_SIZE + 1), a
                xor     a
                ld      (BD_SIZE + 2), a
                ld      (BD_SIZE + 3), a

                ; Add pages * 16384 by adding 16384 to the 32-bit accumulator
                ; `pages` times. pages <= 255 (one byte), so at most 255 adds —
                ; trivially fast and obviously correct (no shift trickery).
                ld      a, (BD_DIFA + BD_OFF_PAGES)   ; A = pages-count
                or      a
                ret     z                      ; zero pages: size = lengthMod16K
                ld      b, a                   ; B = loop count
bdts_add:
                ; BD_SIZE += 16384 (0x00004000), 32-bit little-endian.
                ld      hl, (BD_SIZE)          ; low 16 bits
                ld      de, 16384
                add     hl, de
                ld      (BD_SIZE), hl
                jr      nc, bdts_no_carry
                ; propagate carry into the high 16 bits at BD_SIZE+2.
                ld      hl, (BD_SIZE + 2)
                inc     hl
                ld      (BD_SIZE + 2), hl
bdts_no_carry:
                djnz    bdts_add
                ret

; ---------------------------------------------------------------------------
; bdos_fill_save_uifa — build the UIFA HSAVE reads for the client write-out.
;
; In:  BD_NAME_PTR   2 bytes  pointer to the NUL-terminated filename
;      BD_SAVE_PAGE  1 byte    source physical page (low 5 bits used)
;      BD_SAVE_ADDR  2 bytes   section-C source offset (LE, e.g. &8000)
;      BD_SAVE_SIZE  2 bytes   total byte length (<= 65535; 64 KB ceiling, i24)
; Out: the 48-byte UIFA at BD_UIFA ready for HSAVE: name block + page(+31) +
;      offset(+32..33) + pages(+34 = size>>14) + lengthMod16K(+35..36 = size&0x3FFF).
; ---------------------------------------------------------------------------
bdos_fill_save_uifa:
                call    bdos_name_to_uifa      ; name block + filler

                ; UIFA[31] = page (low 5 bits).
                ld      a, (BD_SAVE_PAGE)
                and     &1F
                ld      (BD_UIFA + BD_OFF_PAGE), a

                ; UIFA[32..33] = source offset (LE).
                ld      hl, (BD_SAVE_ADDR)
                ld      (BD_UIFA + BD_OFF_LOAD), hl

                ; length encoding (mirror save_out_file): H carries the top bits.
                ld      hl, (BD_SAVE_SIZE)     ; HL = total length
                ; pages = length >> 14 = top 2 bits of H.
                ld      a, h
                rlca
                rlca
                and     3
                ld      (BD_UIFA + BD_OFF_PAGES), a    ; UIFA[34] = pages

                ; lengthMod16K = length & 0x3FFF (clear top 2 bits of H).
                ld      a, h
                and     &3F
                ld      h, a
                ld      (BD_UIFA + BD_OFF_LENGTH), hl  ; UIFA[35..36] LE
                ret

; ---------------------------------------------------------------------------
; bdos_inspect_record — classify the currently-selected record from its first
; directory sector. Reads the two record-identity fields B-DOS get.label reads
; (bdos15a.src.txt:2834-2859): the 4-byte "BDOS" stamp at +232 and the 10-byte
; disk label at +210 (bit 7 of byte 0 = write-protect). This is the per-record
; (selected-record) identity view, used to confirm a chosen record before
; overwrite; the card-level record list (read directly, see the header) is the
; enumerate/find-free view. Host-verifiable: pure memory reads, no RST.
;
; Pre: the caller has HRECORD-selected the record and bdos_read_sector'd its
;      first directory sector (track 0, sector 1) into BD_READ_BUF.
; Out: BD_REC_IS_BDOS  1 byte    1 if the "BDOS" stamp is present at +232, else 0
;      BD_REC_NAME    10 bytes   the disk label copied verbatim from +210 (bit 7
;                                of byte 0 = write-protect, preserved for the caller)
; Clobbers: A, BC, DE, HL.
; ---------------------------------------------------------------------------
bdos_inspect_record:
                ; stamp check: the 4 bytes at BD_READ_BUF+232 == "BDOS"?
                ld      hl, BD_READ_BUF + 232
                ld      de, bdos_id_str
                ld      b, 4
bir_stamp:
                ld      a, (de)
                cp      (hl)
                jr      nz, bir_nostamp
                inc     hl
                inc     de
                djnz    bir_stamp
                ld      a, 1                   ; all four bytes matched
                jr      bir_name
bir_nostamp:
                xor     a                      ; no stamp
bir_name:
                ld      (BD_REC_IS_BDOS), a
                ; copy the 10-byte disk label from BD_READ_BUF+210 (WP bit intact).
                ld      hl, BD_READ_BUF + 210
                ld      de, BD_REC_NAME
                ld      bc, 10
                ldir
                ret

bdos_id_str:    defm "BDOS"

; ---------------------------------------------------------------------------
; bdos_validate_disk_record — validate a staged image as a raw Trinity SAM
; disk record (i114c storage-class DiskRecord).
;
; Validation is SIZE-ONLY: BD_REC_SIZE == 819200 (80 tracks × 10 sectors ×
; 2 sides × 512 bytes). Any full SAM disk image is a valid bootable record — it
; does NOT need B-DOS installed on it. Byte 232 ("BDOS") is B-DOS's own
; catalog/format signature (bdos_inspect_record reads it to IDENTIFY B-DOS disks
; for the overwrite-safety show-name gate), NOT a boot or selection requirement:
; booting a record executes the disk's own boot sector and never checks +232.
; (Pete, 2026-06-21 + 2026-06-29; see the inline rationale in the body below.)
;
; Pre: BD_REC_SIZE holds the total image byte length (32-bit LE).
; Out: BD_REC_VALID  1 byte   1 if the size matches, else 0
; Clobbers: A, BC, DE, HL.
;
; This is the Z80 port of the Go authority bdos.ValidateDiskRecord
; (tools/netboot-oracle/bdos/storage.go). Host-verifiable: pure memory reads,
; no RST 8.
;
; 819200 = 0x000C8000 (80 tracks × 10 sectors × 2 sides × 512 = 819200).
; Split as 32-bit LE: low word = 0x8000, high word = 0x000C.
; ---------------------------------------------------------------------------
bdos_validate_disk_record:
                ; --- size check: BD_REC_SIZE == 819200 (0x000C8000) ---
                ; Compare low 16 bits first (LE layout in BD_REC_SIZE).
                ld      hl, (BD_REC_SIZE)
                ld      de, &8000                ; low word of 819200
                or      a                        ; clear carry
                sbc     hl, de
                jr      nz, bvdr_fail            ; low word mismatch
                ld      hl, (BD_REC_SIZE + 2)
                ld      de, &000C                ; high word of 819200
                or      a
                sbc     hl, de
                jr      nz, bvdr_fail            ; high word mismatch

                ; Size is the whole contract: a Trinity record is exactly 819200
                ; bytes, and any full SAM disk image (B-DOS, SAMDOS, a game, our
                ; build-disk output) is a valid bootable record — it does NOT need
                ; B-DOS installed on it. The "BDOS"@232 stamp is B-DOS's own
                ; catalog/format signature (bdos_inspect_record still reads it to
                ; IDENTIFY B-DOS disks for the overwrite-safety show-name gate), NOT
                ; a boot or selection requirement: booting a record executes the
                ; disk's own boot sector and never checks +232. Validating the stamp
                ; here wrongly rejected every bootable non-B-DOS-formatted disk
                ; (including all of ours). Drop it; keep size. (Pete, 2026-06-21 +
                ; 2026-06-29; analysis in the 2026-06-21 session record.)
                ld      a, 1                     ; size check passed
                ld      (BD_REC_VALID), a
                ret

bvdr_fail:
                xor     a
                ld      (BD_REC_VALID), a
                ret


; ===========================================================================
; The real B-DOS hook dispatch. The bodies issue `rst 8` with the B-DOS hook
; codes; on real hardware they route through the ROM/SAMDOS bank + the Trinity
; SD driver. The flat harness has no ROM / RST-8 vector, so bdos_store.go models
; each hook (HRECORD/HGTHD/HSAVE/HRSAD/HWSAD/ALHK/LISTREAD): every bootable
; binary that includes this seam (client / serve / http / sd_push) exercises the
; dispatch in emulation through that model. The standalone unit-test binary
; (netboot_bdos_seam.bin) carries these bodies too, but bdos_seam_test drives
; only the arithmetic leaves above — it never reaches a `rst 8`. Real-Trinity
; RST 8 stays the final gate (CLAUDE.md §5); emulation-verified is not hardware-
; verified. The bodies are the established in-tree idioms (src/loader.asm HGTHD
; path; src/io.asm HSAVE note); the HRECORD &9C select is the B-DOS mass-storage
; step.
; ===========================================================================
BD_UIFA_ADDR:     equ &4B00              ; the SAMDOS UIFA buffer (real address)
BD_DIFA_ADDR:     equ &4B50              ; SAMDOS deposits the DIFA here
BD_HOOK_HRECORD:  equ &9C                ; B-DOS record select (156)
BD_HOOK_ALHK:     equ 136                ; auto-load hook (&88): stage the record's AUTO file
BD_LMPR_PORT:     equ &FA                ; Low Memory Page Register
BD_LMPR_ROM1:     equ &40                ; LMPR bit 6: ROM1 at section D
BD_HOOK_HGTHD:    equ 129                ; get file header (find by name)
BD_HOOK_HSAVE:    equ 132                ; save whole file
BD_HOOK_HRSAD:    equ 160                ; read raw 512-byte sector (HRSAD)
BD_HOOK_HWSAD:    equ 149                ; write raw 512-byte sector (HWSAD)
BD_HOOK_LISTREAD: equ 161                ; card-absolute list-sector read: E=listSec, HL=dest
BD_HOOK_LISTWRITE: equ 162               ; card-absolute list-sector write: E=listSec, HL=source

; Drive number the HRSAD/HWSAD rwsad prelude device-selects on. The prelude does
; `ld a,(hk.a)` then `call &8662`; hk.a is the MAIN A the caller held at the RST 8
; (faithfully re-derived in i280b-b2t §8af — NOT the alternate A' the contaminated
; §8h harness reported). &8662: cp 1 -> floppy; cp 2 -> Trinity SD. So the raw
; sector hooks MUST present A = 2 to reach the SD write/read core. The gold BASIC
; SAVE -> HSAVE -> set.drive path presents exactly A=2 at this same &8662.
BD_DRIVE_TRINITY: equ 2                   ; HRSAD/HWSAD drive = Trinity SD (&8662 cp 2)

; bdos_select_record — HRECORD: select the mass-storage record (0 = floppy).
; In: A = record number. On real B-DOS, all subsequent HGTHD/HSAVE/HLOAD use it.
bdos_select_record:
                ld      l, a
                ld      h, 0                   ; HL = record number
                xor     a                      ; A = 0 (select)
                rst     8
                defb    BD_HOOK_HRECORD
                ret

; bdos_lookup_hook — copy the built UIFA to the real &4B00, issue HGTHD, then
; copy the deposited DIFA from &4B50 back to BD_DIFA for bdos_difa_to_size.
; longjmps on "file not found" (no graceful return — registry i25).
bdos_lookup_hook:
                ld      hl, BD_UIFA
                ld      de, BD_UIFA_ADDR
                ld      bc, BD_UIFA_LEN
                ldir
                ld      ix, BD_UIFA_ADDR
                rst     8
                defb    BD_HOOK_HGTHD
                ld      hl, BD_DIFA_ADDR
                ld      de, BD_DIFA
                ld      bc, BD_UIFA_LEN
                ldir
                ret

; bdos_save_hook — copy the built save-UIFA to &4B00, issue HSAVE.
bdos_save_hook:
                ld      hl, BD_UIFA
                ld      de, BD_UIFA_ADDR
                ld      bc, BD_UIFA_LEN
                ldir
                ld      ix, BD_UIFA_ADDR
                rst     8
                defb    BD_HOOK_HSAVE
                ret

; bdos_read_sector — read 512 bytes from the selected record at (track, sector).
;
; In:  BD_READ_TRACK   1 byte   track (0-79)
;      BD_READ_SECTOR  1 byte   sector (1-10)
; Out: BD_READ_BUF   512 bytes  the sector data
; Clobbers: A, DE, HL.
;
; On real hardware the hook dispatch routes this through the B-DOS sector cache
; and the Trinity SD driver. In the harness the HRSAD handler (i119 brick 1)
; intercepts the RST 8 and fills BD_READ_BUF from the CardModel.
bdos_read_sector:
                ; i280b-b2t (§8af): HRSAD shares the rwsad prelude with HWSAD, so it
                ; device-selects on hk.a = the caller's MAIN A at the RST 8 (see
                ; bdos_write_sector). Present A = 2 (Trinity SD) so the read reaches
                ; the SD core (CMD17), not the floppy poll. Load A LAST, after D/E.
                ld      a, (BD_READ_TRACK)
                ld      d, a
                ld      a, (BD_READ_SECTOR)
                ld      e, a
                ld      hl, BD_READ_BUF
                ld      a, BD_DRIVE_TRINITY     ; hk.a = main A = 2 (Trinity SD)
                rst     8
                defb    BD_HOOK_HRSAD
                ret

; ---------------------------------------------------------------------------
; bdos_read_list_sector — read one 512-byte list sector from the card (card-
; absolute, not record-clamped).
;
; In:  BD_LIST_SECTOR  1 byte   1-based list-sector number (sectors 1..base-1
;                               of the card hold the 16-byte record-list entries;
;                               docs/specs/trinity-record-detection-design.md §4.1)
; Out: BD_LIST_BUF   512 bytes  the list sector (32 × 16-byte entries)
; Clobbers: A, DE, HL.
;
; Two builds select the read path:
;   * default (harness B3 logic tests): issue the harness hook BD_HOOK_LISTREAD,
;     which the SD CardModel serves from its RecordList — the detection geometry +
;     free-test are exercised without modelling the SPI transaction.
;   * NETBOOT_REAL_LISTREAD (i141, the boot images' real path): call bd_list_read_hw
;     (sd_csd.asm), the raw SD CMD17 single-block read on ports &DC–&DF. CY-on-failure
;     is treated as "no free record", the same safe decline as a 0 BD_RECORDS.
; The real path is host-verified against the i145c/i145h SD model; the real-Trinity
; SPI run is the final gate (CLAUDE.md §5; trinity-record-detection-design.md §8).
bdos_read_list_sector:
                if defined(NETBOOT_REAL_LISTREAD)
                jp      bd_list_read_hw        ; real CMD17 SPI read (tail-call, returns to caller)
                else
                ld      a, (BD_LIST_SECTOR)
                ld      e, a                   ; E = list-sector number (1-based)
                ld      hl, BD_LIST_BUF
                rst     8
                defb    BD_HOOK_LISTREAD
                ret
                endif

; ---------------------------------------------------------------------------
; bdos_record_entry — fetch the 16-byte list entry for one record into
; BD_ENTRY_BUF. Reads the containing list sector and copies the entry's 16
; bytes.
;
; In:  BD_ENTRY_REC  2 bytes  1-based record number n (>= 1)
; Out: BD_ENTRY_BUF 16 bytes  the record's list entry
;                             (byte 0 bit 7 = write-protect; bits 0-6 = first
;                             name char; 0 ⇒ free; docs/specs/trinity-record-
;                             detection-design.md §4.3 / §4.4)
; Clobbers: A, BC, DE, HL.
;
; Geometry (docs/specs/trinity-record-detection-design.md §4.3, §5):
;   recordIndex = n - 1
;   listSector  = (recordIndex >> 5) + 1    (/ 32 then + 1)
;   byteOffset  = (recordIndex AND 31) << 4 (mod 32 then × 16, 9-bit result)
;
; (n-1) fits in 16 bits; for any practical card size (a 1 GB card has ~1300
; records so n-1 <= 1299 < 2^11) the >> 5 result fits in 8 bits. The 16-bit
; shift is computed as: A = ((H & 7) << 3) | (L >> 5), which is correct for
; any 16-bit value where the result of >> 5 fits in 8 bits (n-1 < 8192 for
; all foreseeable card sizes).
;
; byteOffset = (n-1 mod 32) * 16 fits in 9 bits (max 31*16 = 496 = 0x1F0),
; so it is held in BC as a 16-bit value: B = high byte (0 or 1), C = low byte.
; For m = (n-1) AND 31 (0..31): B = m >> 4 (0 or 1), C = (m AND 15) << 4.
bdos_record_entry:
                call    bdos_record_geometry   ; -> BD_LIST_SECTOR set, BC = byteOffset
                push    bc                     ; save byteOffset across the call
                call    bdos_read_list_sector
                pop     bc                     ; restore byteOffset

                ; copy 16 bytes from BD_LIST_BUF + byteOffset to BD_ENTRY_BUF
                ld      hl, BD_LIST_BUF
                add     hl, bc                 ; HL = BD_LIST_BUF + byteOffset (BC = 16-bit offset)
                ld      de, BD_ENTRY_BUF
                ld      bc, 16
                ldir
                ret

; ---------------------------------------------------------------------------
; bdos_record_geometry — map a 1-based record number to its position in the
; central record list: which list sector holds it, and the byte offset of its
; 16-byte entry within that sector. The READ (bdos_record_entry) and the WRITE
; (bdos_claim_record) share this one tested computation so they target the same
; entry for a given record.
;
; In:  BD_ENTRY_REC  2 bytes  1-based record number n (>= 1)
; Out: BD_LIST_SECTOR  1 byte   the 1-based list sector holding record n's entry
;      BC              16-bit   byteOffset of the entry within that list sector
; Clobbers: A, DE, HL.
;
; Geometry (docs/specs/trinity-record-detection-design.md §4.3, §5):
;   recordIndex = n - 1
;   listSector  = (recordIndex >> 5) + 1    (/ 32 then + 1)
;   byteOffset  = (recordIndex AND 31) << 4 (mod 32 then × 16, 9-bit result)
bdos_record_geometry:
                ld      hl, (BD_ENTRY_REC)     ; HL = n (1-based)
                dec     hl                     ; HL = n-1 (0-based record index)

                ; byteOffset (16-bit BC) = ((n-1) AND 31) * 16
                ; m = (n-1) AND 31 = low 5 bits, all in L.
                ; B = m >> 4 (0 or 1); C = (m AND 15) << 4.
                ld      a, l
                and     &1F                    ; A = m = (n-1) mod 32 (0..31)
                rrca
                rrca
                rrca
                rrca                           ; A = m >> 4 (= 0 if m<16, 1 if m>=16, wraps into bit 7)
                and     &01                    ; B = high byte of byteOffset (0 or 1)
                ld      b, a
                ld      a, l
                and     &0F                    ; A = m AND 15 (low nibble of m)
                rlca
                rlca
                rlca
                rlca                           ; C = (m AND 15) << 4 = low byte of byteOffset
                ld      c, a                   ; BC = 16-bit byteOffset

                ; listSector = (n-1) / 32 + 1
                ; compute (n-1) >> 5 = ((H AND 7) << 3) OR (L >> 5)
                ld      a, h
                and     &07                    ; keep bits 10-8 of n-1 (H <= 5 for <=1300 records)
                rlca
                rlca
                rlca                           ; shift to positions 5-3
                ld      e, a                   ; save high contribution
                ld      a, l
                rrca
                rrca
                rrca
                rrca
                rrca                           ; L >> 5 via 5 right-rotates
                and     &07                    ; keep low 3 bits (bits 7-5 of L → bits 2-0)
                or      e                      ; A = (n-1) >> 5
                inc     a                      ; listSector = (n-1)/32 + 1
                ld      (BD_LIST_SECTOR), a
                ret

; ---------------------------------------------------------------------------
; bdos_find_free_record — scan the central record list for the first free
; (unnamed) record. Free ⇔ (entry[0] AND 0x7F) == 0 (the frec3x test;
; bdos15a.src.txt:946-948; docs/specs/trinity-record-detection-design.md §4.4).
;
; In:  BD_RECORDS  2 bytes  total record count (>= 1)
; Out: BD_FREE_RECORD  2 bytes  1-based number of the first free record, or 0
;                               if all records are named/in-use.
; Clobbers: A, BC, DE, HL.
;
; Algorithm (docs/specs/trinity-record-detection-design.md §5): iterate n = 1
; to BD_RECORDS; for each n, read its entry via bdos_record_entry and apply
; the free test. The first free record's number is stored and the routine
; returns. If no free record is found, BD_FREE_RECORD is set to 0.
;
; NOTE: this re-reads the list sector for EVERY record. For practical card sizes
; (~1300 records on 1 GB), the list fits in ~41 sectors so this sweeps at most
; ~41 sector reads. Correctness first; caching is a later optimisation.
bdos_find_free_record:
                ; initialise BD_FREE_RECORD = 0 (no free record found yet)
                xor     a
                ld      (BD_FREE_RECORD), a
                ld      (BD_FREE_RECORD + 1), a

                ; loop: n = 1 to BD_RECORDS
                ld      hl, 1                  ; HL = current record n (start at 1)
bffr_loop:
                ; if n > BD_RECORDS, no free record found — exit
                push    hl                     ; save n
                ld      de, (BD_RECORDS)
                ; compare HL <= DE: check if n > records
                ; subtract: DE - HL; borrow ⇒ n > records
                ld      a, e
                sub     l
                ld      a, d
                sbc     a, h
                pop     hl                     ; restore n
                jr      c, bffr_done           ; n > records: done (BD_FREE_RECORD stays 0)

                ; fetch the entry for record n
                ld      (BD_ENTRY_REC), hl
                push    hl                     ; save n
                call    bdos_record_entry
                pop     hl                     ; restore n

                ; free test: (BD_ENTRY_BUF[0] AND 0x7F) == 0?
                ; docs/specs/trinity-record-detection-design.md §4.4 (frec3x)
                ld      a, (BD_ENTRY_BUF)
                and     &7F                    ; strip write-protect bit
                jr      nz, bffr_next          ; non-zero ⇒ named ⇒ skip

                ; free record found — store n in BD_FREE_RECORD and return
                ld      (BD_FREE_RECORD), hl
                ret

bffr_next:
                inc     hl                     ; n++
                jr      bffr_loop

bffr_done:
                ret

; ---------------------------------------------------------------------------
; bdos_find_highest_free_record — scan the central record list DOWNWARD for the
; HIGHEST free (unnamed) record. The placement-strategy sibling of
; bdos_find_free_record (which returns the LOWEST): same free test (free ⇔
; (entry[0] AND 0x7F) == 0; the frec3x test, bdos15a.src.txt:946-948), opposite
; scan direction, so TFTP storage grows DOWN from the top record and the user's low,
; memorable record slots stay free for their own disks (manifest design §4 s3 /
; decision 4). The READ-only free detection never touches a named record.
;
; In:  BD_RECORDS  2 bytes  total record count (>= 1)
; Out: BD_FREE_RECORD  2 bytes  1-based number of the highest free record, or 0 if
;                               all records are named/in-use.
; Clobbers: A, BC, DE, HL.
;
; Algorithm: iterate n = BD_RECORDS down to 1; for each n read its entry via
; bdos_record_entry and apply the free test; the first free record found (the
; highest) is stored and the routine returns. None free ⇒ BD_FREE_RECORD = 0.
bdos_find_highest_free_record:
                ; BD_FREE_RECORD = 0 (no free record found yet)
                xor     a
                ld      (BD_FREE_RECORD), a
                ld      (BD_FREE_RECORD + 1), a

                ; HL = BD_RECORDS (start at the top). If 0, nothing to scan.
                ld      hl, (BD_RECORDS)
                ld      a, h
                or      l
                ret     z                      ; 0 records: BD_FREE_RECORD stays 0
bfhr_loop:
                ; fetch the entry for record n (HL).
                ld      (BD_ENTRY_REC), hl
                push    hl                     ; save n
                call    bdos_record_entry
                pop     hl                     ; restore n

                ; free test: (BD_ENTRY_BUF[0] AND 0x7F) == 0?
                ld      a, (BD_ENTRY_BUF)
                and     &7F                    ; strip write-protect bit
                jr      nz, bfhr_next          ; non-zero ⇒ named ⇒ go lower

                ; highest free record found — store n and return.
                ld      (BD_FREE_RECORD), hl
                ret

bfhr_next:
                ; n--; stop after n == 1 has been tested (HL reaching 0 = done).
                dec     hl
                ld      a, h
                or      l
                jr      nz, bfhr_loop          ; n >= 1 still: keep scanning down
                ret                            ; scanned 1..BD_RECORDS, none free

                ; The WRQ record-claim path (list-write + claim build/sanitise) is
                ; assembled only for the serve unit, which sets NETBOOT_WANT_CLAIM
                ; before including this file. The client never claims a record, so it
                ; omits this code and stays inside its boot window (i195).
                if defined(NETBOOT_WANT_CLAIM)

; ---------------------------------------------------------------------------
; bdos_write_list_sector — write one 512-byte list sector back to the card (card-
; absolute, not record-clamped) — the WRITE sibling of bdos_read_list_sector.
;
; In:  BD_LIST_SECTOR  1 byte    1-based list-sector number
;      BD_LIST_BUF   512 bytes   the list sector to write (a read-modified copy)
; Clobbers: A, DE, HL.
;
; Two builds select the write path (same flag as the READ — they flip together):
;   * default (harness B3 logic tests): issue the harness hook BD_HOOK_LISTWRITE,
;     which the BDOSStore captures — the claim geometry + RMW safety are
;     exercised without modelling the SPI transaction.
;   * NETBOOT_REAL_LISTREAD (i198, the boot images' real path): call bd_list_write_hw
;     (sd_csd.asm), the raw SD CMD24 single-block write on ports &DC–&DF. The
;     CMD24 write path is host-verified by sd_listwrite_test.go (the real RMW
;     round-trip through CMD17+CMD24 on the SD model); the real-Trinity SPI write
;     is the final gate (CLAUDE.md §5; docs/specs/trinity-record-detection-design.md §8).
bdos_write_list_sector:
                if defined(NETBOOT_REAL_LISTREAD)
                jp      bd_list_write_hw       ; real CMD24 SPI write (tail-call, returns to caller)
                else
                ld      a, (BD_LIST_SECTOR)
                ld      e, a                   ; E = list-sector number (1-based)
                ld      hl, BD_LIST_BUF
                rst     8
                defb    BD_HOOK_LISTWRITE
                ret
                endif

; ---------------------------------------------------------------------------
; bdos_claim_record — mark a pushed Trinity record as USED by writing its central
; record-LIST name entry, so bdos_find_free_record no longer reports it free and
; the NEXT push lands on the next free record. This is the WRITE counterpart of
; the free-record detection (the READ side: bdos_find_free_record /
; bdos_record_entry), and the B-DOS new.rec / RENAME path is its reference: locate
; the record's 16-byte entry within its list sector and LDIR the name into it
; (bdos15a.src.txt:2801-2820; docs/specs/trinity-record-detection-design.md §4.3).
;
; In:  BD_FREE_RECORD   2 bytes  the 1-based record this push claimed (the slot
;                                bdos_find_free_record returned; >= 1).
;      BD_CLAIM_NAME_PTR 2 bytes pointer to the NUL-terminated WRQ filename.
; Out: the record's list entry written with the filename-derived name; the card
;      now reads that record as NAMED.
; Clobbers: A, BC, DE, HL.
;
; SAFETY (the whole point — the Trinity SD card is a SHARED user resource): this
; writes ONLY the 16-byte entry of BD_FREE_RECORD, the free slot the claim picked.
; It reads the entry's containing list sector, overwrites EXACTLY those 16 bytes
; in the loaded sector (the read-modify-write keeps every other entry byte-for-byte
; intact), then writes that one sector back. A named record is never reached: the
; only entry touched is the one at this record's own (listSector, byteOffset).
;
; The name field: the on-card list name is the WRQ filename derived + sanitised by
; bdos_build_claim_entry — a leading "trinity-sam-disks/" prefix stripped, the final
; path segment taken, any dotted suffix dropped, every byte forced into the legal
; printable charset 0x20..0x7E, truncated to the full 16-char record-name field, and
; space-padded. The WRQ filename is attacker-controllable; see bdos_build_claim_entry
; for the hardened derivation. Mirrors the Go authority serve.claimRecordName.
bdos_claim_record:
                ; 1. Build the 16-byte list entry name into BD_CLAIM_ENTRY.
                call    bdos_build_claim_entry

                ; 2. Geometry: which list sector holds this record's entry, and the
                ;    byte offset of the entry within it. BD_FREE_RECORD is the record.
                ld      hl, (BD_FREE_RECORD)
                ld      (BD_ENTRY_REC), hl
                call    bdos_record_geometry   ; -> BD_LIST_SECTOR set, BC = byteOffset

                ; 3. Read-modify-write: load the containing list sector, overwrite
                ;    ONLY this record's 16 bytes, write the sector back. Every other
                ;    entry in the sector is preserved byte-for-byte (the safety
                ;    invariant — never touch a record we did not claim).
                push    bc                     ; save byteOffset across the read
                call    bdos_read_list_sector  ; -> BD_LIST_BUF (512 bytes)
                pop     bc                     ; restore byteOffset

                ld      hl, BD_CLAIM_ENTRY     ; source: the built 16-byte name
                ld      de, BD_LIST_BUF
                ex      de, hl                 ; HL = BD_LIST_BUF, DE = BD_CLAIM_ENTRY
                add     hl, bc                 ; HL = BD_LIST_BUF + byteOffset
                ex      de, hl                 ; DE = dest entry slot, HL = BD_CLAIM_ENTRY
                ld      bc, 16
                ldir                           ; place the entry; rest of sector intact

                call    bdos_write_list_sector ; write the modified sector back
                ret

; ---------------------------------------------------------------------------
; bdos_free_record — mark a Trinity record FREE by clearing its central record-LIST
; name entry (zeroing the 16-byte entry), so bdos_find_free_record reports it free
; again and the slot is reusable. The exact INVERSE of bdos_claim_record: the same
; single-entry read-modify-write geometry, but it writes 16 ZERO bytes instead of a
; name. This is the record-list side of B-DOS's new.rec / RENAME "change label in
; record list" RMW (bdos15a.src.txt:2801-2823: load the record-list sector, LDIR a
; 16-byte name into the entry, save the sector) with an all-zero label — which reads
; back FREE ((entry[0] AND 0x7F) == 0, the frec3x test; bdos15a.src.txt:946-948;
; docs/specs/trinity-record-detection-design.md §4.4). It completes the
; store/boot/delete testing toolkit (i317) so the autonomous loop can re-push cleanly
; without exhausting records.
;
; In:  BD_DELETE_RECORD  2 bytes  the 1-based record to free (>= 1).
; Out: the record's list entry zeroed; the card now reads that record as FREE.
; Clobbers: A, BC, DE, HL.
;
; SAFETY (the Trinity SD card is a SHARED user resource — trinity_storage_shared_
; resource): this writes ONLY the 16-byte entry of BD_DELETE_RECORD. It reads the
; entry's containing list sector, zeroes EXACTLY those 16 bytes in the loaded sector
; (the read-modify-write keeps every other entry byte-for-byte intact), then writes
; that one sector back. No neighbour is ever touched — the only entry mutated is the
; one at this record's own (listSector, byteOffset). The CALLER is responsible for
; confirming the record is one WE may free (show-name + confirm-before-overwrite,
; trinity_storage_shared_resource) BEFORE calling; this primitive frees whatever
; record it is handed.
bdos_free_record:
                ; 1. Geometry: which list sector holds this record's entry, and the
                ;    byte offset of the entry within it. Shared with the READ
                ;    (bdos_record_entry) and the CLAIM (bdos_claim_record) so all three
                ;    target the same entry for a given record.
                ld      hl, (BD_DELETE_RECORD)
                ld      (BD_ENTRY_REC), hl
                call    bdos_record_geometry   ; -> BD_LIST_SECTOR set, BC = byteOffset

                ; 2. Read-modify-write: load the containing list sector, zero ONLY this
                ;    record's 16 bytes, write the sector back. Every other entry in the
                ;    sector is preserved byte-for-byte (the safety invariant — never
                ;    touch a record we were not asked to free).
                push    bc                     ; save byteOffset across the read
                call    bdos_read_list_sector  ; -> BD_LIST_BUF (512 bytes)
                pop     bc                     ; restore byteOffset

                ld      hl, BD_LIST_BUF
                add     hl, bc                 ; HL = BD_LIST_BUF + byteOffset (the entry)
                ld      b, 16                  ; 16 bytes to zero (the whole entry)
                xor     a                      ; A = 0 (the free / unnamed byte)
bfr_zero:
                ld      (hl), a
                inc     hl
                djnz    bfr_zero

                call    bdos_write_list_sector ; write the modified sector back
                ret

; ---------------------------------------------------------------------------
; bdos_build_claim_entry — build the 16-byte central-list name entry for a record
; from BD_CLAIM_NAME_PTR, the WRQ filename. This is the FULL 16-char record-name
; field (the whole list entry IS the record name — design §4.3), so the pushed
; disk images are legible via the `RECORD` command. Host-verifiable (pure memory
; build, no RST). Mirrors the Go authority serve.claimRecordName.
;
; THE WRQ FILENAME IS ATTACKER-CONTROLLABLE (it arrives over the network) and this
; entry is written into a SHARED user resource (the Trinity SD card's central
; record list, alongside other people's disks). The derivation + sanitisation is
; therefore HARDENED — no input can overrun the 16-byte slot or write a byte that
; corrupts the on-card list or makes the entry read as free/illegal:
;
;   1. Strip a leading "trinity-sam-disks/" prefix (the disk-record namespace).
;   2. Take the FINAL path segment: scan to the NUL, remembering the byte after
;      the last '/'; that is the segment start (so "a/b/c.mgt" -> "c.mgt", and a
;      traversal payload like "../../../etc/passwd" -> "passwd"). '/' can never
;      appear in the stored name.
;   3. Drop a dotted suffix: copy until NUL or the first '.' ("disk.mgt" -> "disk").
;   4. SANITISE each candidate byte to the legal printable record-name charset
;      0x20..0x7E (space..'~'). B-DOS's own print path masks AND 127 then renders
;      every masked byte < 0x21 as a space-replacement and would render 0x7F (DEL)
;      as garbage (bdos15a.src.txt:4098-4115, L18652). So any byte outside
;      0x20..0x7E (high-bit bytes, control bytes 0x00..0x1F, DEL 0x7F) is replaced
;      with '_' (0x5F) — a legible, legal stand-in — rather than dropped, so the
;      name length is preserved and no illegal byte ever reaches the card.
;   5. TRUNCATE to 16 (BD_CLAIM_ENTRY is 16 bytes) — a hard bound; no input of any
;      length can overrun the entry or the 16-byte on-card slot.
;   6. Guarantee a VALID NAMED entry: if sanitisation produced no characters (empty
;      or all-separator/all-dot input), substitute the default name "pushed" so the
;      first byte is a legal non-zero char with bit 7 clear — never accidentally
;      free (firstByte AND 0x7F == 0) or write-protected (bit 7 set). The charset
;      0x20..0x7E is already bit-7-clear and the '_' replacement is non-zero, so the
;      first written char is always legal; the empty-name default covers the case
;      where nothing was written at all.
;
; In:  BD_CLAIM_NAME_PTR  2 bytes  pointer to the NUL-terminated WRQ filename
; Out: BD_CLAIM_ENTRY    16 bytes  the sanitised, space-padded 16-byte list-entry name
; Clobbers: A, B, C, DE, HL.
bdos_build_claim_entry:
                ; space-fill the whole 16-byte entry first; the name overwrites
                ; the leading bytes, leaving the tail as padding.
                ld      hl, BD_CLAIM_ENTRY
                ld      b, 16
                ld      a, BD_SPACE
bbce_fill:
                ld      (hl), a
                inc     hl
                djnz    bbce_fill

                ; HL = filename; strip a leading "trinity-sam-disks/" prefix.
                ld      hl, (BD_CLAIM_NAME_PTR)
                call    bdos_strip_disk_prefix ; HL -> past the prefix if present

                ; Take the final path segment: scan to NUL, tracking the byte after
                ; the last '/'. Any path separator collapses to its final segment, so
                ; a traversal payload ("../../../x") yields only the leaf and '/' is
                ; never stored. HL is preserved as the segment start.
                call    bdos_last_segment      ; HL -> start of the final path segment

                ; Copy up to 16 sanitised chars into the entry, stopping at NUL or
                ; '.' (drop the dotted suffix). C counts chars written (for the
                ; empty-name guard). The entry is already space-padded.
                ld      de, BD_CLAIM_ENTRY
                ld      b, BD_REC_NAME_LEN     ; cap at 16 chars (the full record-name field)
                ld      c, 0                   ; C = chars written so far
bbce_name:
                ld      a, (hl)
                or      a
                jr      z, bbce_endname        ; NUL: stop (padding already in place)
                cp      "."
                jr      z, bbce_endname        ; dotted suffix: stop
                call    bdos_sanitise_char     ; A -> legal 0x20..0x7E (else '_')
                ld      (de), a
                inc     c                      ; one more char written
                inc     hl
                inc     de
                djnz    bbce_name
bbce_endname:
                ; Empty-name guard: if nothing was written (C == 0) the entry is all
                ; spaces — entry[0] AND 0x7F == 0 reads as FREE. Substitute a safe
                ; default so the record is a valid NAMED entry.
                ld      a, c
                or      a
                ret     nz                     ; at least one char: a valid named entry
                ; copy the default name into the (still space-padded) entry.
                ld      hl, bdos_default_name
                ld      de, BD_CLAIM_ENTRY
                ld      b, BD_DEFAULT_NAME_LEN
bbce_default:
                ld      a, (hl)
                ld      (de), a
                inc     hl
                inc     de
                djnz    bbce_default
                ret

bdos_default_name:    defm "pushed"
BD_DEFAULT_NAME_LEN:  equ 6                  ; len("pushed")

; ---------------------------------------------------------------------------
; bdos_sanitise_char — map a candidate name byte to the legal printable record-name
; charset 0x20..0x7E. Any byte outside that range (high-bit set, control 0x00..0x1F,
; or DEL 0x7F) becomes '_' (0x5F). B-DOS prints names with AND 127 per byte and
; treats masked bytes < 0x21 as a space-replacement (L18652); keeping only
; 0x20..0x7E guarantees every stored byte renders cleanly via `RECORD` and is
; bit-7-clear (so the first char can never set the write-protect bit).
;
; In:  A  candidate byte
; Out: A  the byte if 0x20 <= A <= 0x7E, else '_' (0x5F)
; Clobbers: A, F.
bdos_sanitise_char:
                cp      &20                    ; below space?
                jr      c, bsc_replace         ; 0x00..0x1F: control byte
                cp      &7F                    ; 0x7F (DEL) or high-bit (>= 0x80)?
                jr      nc, bsc_replace         ; 0x7F..0xFF: DEL or high-bit
                ret                            ; 0x20..0x7E: legal, keep verbatim
bsc_replace:
                ld      a, "_"                 ; legible, legal stand-in (0x5F)
                ret

; ---------------------------------------------------------------------------
; bdos_last_segment — advance HL to the start of the final path segment of the
; NUL-terminated string at HL: the byte after the LAST '/'. If there is no '/',
; HL is unchanged. This collapses any directory traversal to its leaf and ensures
; no '/' is ever copied into the record name.
;
; In:  HL  pointer to the NUL-terminated string
; Out: HL  pointer to the start of the final segment (past the last '/')
; Clobbers: A, DE.
bdos_last_segment:
                ld      d, h
                ld      e, l                   ; DE = candidate segment start (= HL initially)
bls_scan:
                ld      a, (hl)
                or      a
                jr      z, bls_done            ; NUL: end of string
                inc     hl
                cp      "/"
                jr      nz, bls_scan           ; not a separator: keep scanning
                ld      d, h
                ld      e, l                   ; '/' seen: segment starts at the next byte
                jr      bls_scan
bls_done:
                ex      de, hl                 ; HL = final segment start
                ret

; ---------------------------------------------------------------------------
; bdos_strip_disk_prefix — if the string at HL begins with "trinity-sam-disks/",
; advance HL past it and set CY; otherwise leave HL unchanged and clear CY. Mirrors
; the Go authority bdos.Classify, which returns BOTH the class (CY = DiskRecord) and
; the stripped internal name (HL). Host-verifiable.
;
; In:  HL  pointer to the candidate string
; Out: HL  advanced past the prefix if it matched, else unchanged
;      CY  set if the prefix matched (DiskRecord class), clear otherwise (FlatFile)
; Clobbers: A, B, DE.
bdos_strip_disk_prefix:
                push    hl                     ; remember the start (restore on mismatch)
                ld      de, bdos_disk_prefix
                ld      b, BD_DISK_PREFIX_LEN
bsdp_cmp:
                ld      a, (de)
                cp      (hl)
                jr      nz, bsdp_nomatch       ; a byte differs: not the prefix
                inc     hl
                inc     de
                djnz    bsdp_cmp
                ; full prefix matched — HL is past it; discard the saved start.
                pop     de                     ; drop the start; keep HL advanced
                scf                             ; CY = matched (DiskRecord class)
                ret
bsdp_nomatch:
                pop     hl                     ; restore HL to the start (no prefix)
                or      a                       ; CY = 0 (FlatFile class)
                ret

bdos_disk_prefix: defm "trinity-sam-disks/"
BD_DISK_PREFIX_LEN: equ 18                 ; len("trinity-sam-disks/")

                endif                          ; NETBOOT_WANT_CLAIM (the WRQ record-claim path)

; ---------------------------------------------------------------------------
; bdos_write_sector — write 512 bytes from BD_WRITE_BUF to the selected record
; at (track, sector) using HWSAD (hook 149, bdos15a.src.txt:528-531).
;
; HWSAD register contract (bdos15a.src.txt:535-537, shared rwsad entry point):
;   A  = drive number (same as HRSAD); MUST be 2 (Trinity SD) — the rwsad prelude
;        device-selects on hk.a = this main A (i280b-b2t §8af). A=1 selects floppy
;        -> the FDC poll hang; A=2 selects Trinity SD -> the SD write core.
;   D  = track  (0-79)
;   E  = sector (1-10)
;   HL = source memory address (the 512 bytes to write)
; The RST 8 / DEFB BD_HOOK_HWSAD entry point is HWSAD (bdos15a.src.txt:530),
; which LD HL,wr.buff then falls into rwsad; the caller's HL is the real source
; (hk.hl), loaded at rwsad+2. This is the write sibling of HRSAD (hook 160).
;
; In:  BD_WRITE_TRACK   1 byte   track (0-79)
;      BD_WRITE_SECTOR  1 byte   sector (1-10)
;      BD_WRITE_BUF   512 bytes  the sector data to write
; Clobbers: A, DE, HL.
;
; Hardware-gated (bdos15a.src.txt:557-602): the hook routes through the B-DOS
; sector cache and the Trinity SD driver. The harness intercepts RST 8 and
; writes BD_WRITE_BUF into the CardModel (bdos_store.go bdHookHWSAD case).
bdos_write_sector:
                if defined(NETBOOT_DEBUG)
                ld      a, DBG_HWSAD_PRE
                call    dbg_marker
                ; i280b-b2i: report the runtime LMPR/HMPR at the write moment. §8k
                ; proved the HWSAD prelude pages (H>>6)-1 into section C; whether our
                ; flat HL=BD_WRITE_BUF=&BE42 names the buffer's real page depends on
                ; HMPR here (the serve is pushed to page 1, so &BE42 may already be
                ; correct — the §8k displacement was an editor-idle-harness artifact).
                call    dbg_report_paging
                endif
                ; i280b-b2t (§8af): present A = 2 (Trinity SD) to the rwsad prelude's
                ; device-select. The prelude does `ld a,(hk.a)` then `call &8662`,
                ; and hk.a is the caller's MAIN A at the RST 8 (faithfully re-derived
                ; in the Continue/WTKY2 rig — the §8h/#730 "hk.a = alternate A'"
                ; finding was a &01CB reboot-artifact, §8ae). &8662 keys the ambient
                ; device on A: cp 1 -> floppy, cp 2 -> Trinity SD. Our prior code
                ; left main A = BD_WRITE_SECTOR at the RST 8, so the FIRST .mgt sector
                ; (linear 0 -> sector 1) selected A=1 = FLOPPY -> the un-timed FDC
                ; poll = the hardware hang (faithfully reproduced: A=1 spins in the
                ; B-DOS floppy poll, A=2 issues CMD24 and returns clean). Load A LAST,
                ; after D/E, so the value the prelude reads is the drive, not a
                ; leftover sector. (The gold BASIC SAVE -> HSAVE -> set.drive path
                ; presents this same A=2 at &8662.)
                ld      a, (BD_WRITE_TRACK)
                ld      d, a
                ld      a, (BD_WRITE_SECTOR)
                ld      e, a
                ld      hl, BD_WRITE_BUF
                ld      a, BD_DRIVE_TRINITY     ; hk.a = main A = 2 (Trinity SD)
                rst     8
                defb    BD_HOOK_HWSAD
                if defined(NETBOOT_DEBUG)
                ld      a, DBG_HWSAD_POST
                call    dbg_marker
                endif
                ret

; ---------------------------------------------------------------------------
; bdos_write_record — write N contiguous 512-byte sectors from BD_WRITE_BUF
; into the currently-HRECORD-selected record, starting at the linear sector
; BD_WRITE_START and continuing for BD_WRITE_COUNT sectors.
;
; In:  BD_WRITE_BUF    512 bytes  the source data for each sector in turn
;      BD_WRITE_START  2 bytes    starting linear sector (0-based: track*10+(sector-1))
;      BD_WRITE_COUNT  2 bytes    number of sectors to write
;
; The linear sector is decoded the same way the harness bdHookHWSAD handler
; and bdHookHRSAD handler do (bdos_store.go: linearSec = track*bdSectorsPerTrack
; + (sector-1), with bdSectorsPerTrack = 10):
;   track  = linearSec / 10    (integer divide)
;   sector = linearSec mod 10 + 1    (1-based)
;
; Clobbers: A, BC, DE, HL.
;
; The caller is responsible for pointing BD_WRITE_BUF at successive 512-byte
; source windows. This routine advances BD_WRITE_TRACK / BD_WRITE_SECTOR after
; each sector, carrying across track boundaries.
bdos_write_record:
                ld      hl, (BD_WRITE_START)    ; HL = current linear sector
                ld      bc, (BD_WRITE_COUNT)    ; BC = sectors remaining

bwr_loop:
                ; exit when BC == 0
                ld      a, b
                or      c
                ret     z

                ; decode linear sector HL into track/sector:
                ;   track = HL / 10, sector = (HL mod 10) + 1
                ; divide HL by 10: use 16-bit div-by-10 via repeated subtract.
                ; For record-sized writes (1600 sectors max), a simple div loop
                ; is fast enough (correctness first).
                push    bc                      ; save sector count
                push    hl                      ; save linear sector

                ; compute track = HL / 10
                ld      de, 10
                ld      bc, 0
bwr_div:
                or      a                       ; clear carry
                sbc     hl, de                  ; HL -= 10
                jr      c, bwr_divdone
                inc     bc                      ; BC = quotient (track)
                jr      bwr_div
bwr_divdone:
                add     hl, de                  ; restore remainder (HL = mod)
                ; HL = remainder (0..9), BC = quotient (track)
                ld      a, c                    ; A = track (BC < 256 for any record)
                ld      (BD_WRITE_TRACK), a
                ld      a, l                    ; A = remainder = sector - 1
                inc     a                       ; sector (1-based)
                ld      (BD_WRITE_SECTOR), a

                pop     hl                      ; restore linear sector
                pop     bc                      ; restore sector count

                push    hl
                push    bc
                call    bdos_write_sector
                pop     bc
                pop     hl

                inc     hl                      ; advance to next linear sector
                dec     bc                      ; one fewer sector to write
                jr      bwr_loop


; ---------------------------------------------------------------------------
; bdos_boot_record — the i122a boot-a-record primitive: HRECORD-select a record,
; then fire ALHK (hook 136) to boot that record's AUTO file. This is the B-DOS
; BOOT routine's "DOS resident" branch (KEYBOARD_BOOT_WORKAROUND.md §2: RST 8 /
; DEFB ALHK at D8DCH).
;
; In:  BD_BOOT_RECORD  1 byte  the record number to select and boot (0 = floppy).
;
; THE ALHK CONTRACT (B-DOS 1.5t HAUTO, real &9DBD — the i328 root cause): ALHK
; itself does NOT load or run anything. It scans the SELECTED record's directory
; for an AUTO* file, stages that file's UIFA/DIFA into the system buffers at
; &4B00/&4B50, and returns E=1 through the ROM's hook exit — which then vectors
; into the ROM1 LOAD continuation at &E274+ to perform the actual body load and
; the jump to the file's execute address. That continuation lives in ROM1, so
; SECTION D MUST MAP ROM1 (LMPR bit 6) when the hook fires. The BOOT keyword and
; the power-on bootloader satisfy this implicitly (their callers execute in/under
; ROM1); a trinload-pushed caller does NOT — its LMPR has bit 6 clear, so the
; hook exit lands in RAM garbage at &E274, the SAM crashes and re-autoboots, and
; the push appears to "bounce back to trinload" ~10 s later (the i328 hardware
; signature). Hence the OR &40 below: map ROM1 at section D before the RST 8.
;
; On a successful boot the ROM1 continuation never returns here (it jumps into
; the loaded image); on a failed search B-DOS raises rep101 ("no AUTO file"),
; which longjmps to the editor context — also never here. The `ret` below is
; harness-only: the flat-harness BDOSStore models the whole boot as a captured
; event and resumes at retAddr+1.
;
; Hardware-gated like the other RST 8 hooks (CLAUDE.md §5): emulation-verified
; is not hardware-verified.
bdos_boot_record:
                ld      a, (BD_BOOT_RECORD)
                call    bdos_select_record      ; HRECORD: select the record (A = record)
                in      a, (BD_LMPR_PORT)
                or      BD_LMPR_ROM1            ; map ROM1 at section D: the post-ALHK
                out     (BD_LMPR_PORT), a       ;   ROM1 LOAD continuation runs there
                rst     8
                defb    BD_HOOK_ALHK            ; stage the AUTO file; ROM1 loads + runs it
                ret                             ; harness-only: never reached on hardware

; ===========================================================================
; Data region — the UIFA / DIFA buffers and the routine parameters.
; ===========================================================================
BD_NAME_PTR:      defs 2                 ; pointer to the NUL-terminated filename
BD_SAVE_PAGE:     defs 1
BD_SAVE_ADDR:     defs 2
BD_SAVE_SIZE:     defs 2
BD_SIZE:          defs 4                 ; bdos_difa_to_size output (LE)

BD_UIFA:          defs BD_UIFA_LEN       ; the built UIFA (host-inspectable)
BD_DIFA:          defs BD_UIFA_LEN       ; the DIFA to decode (harness-filled)

BD_READ_TRACK:    defs 1                 ; bdos_read_sector: track to read (0-79)
BD_READ_SECTOR:   defs 1                 ; bdos_read_sector: sector to read (1-10)
BD_READ_BUF:      defs 512               ; bdos_read_sector: output (512 bytes)

BD_REC_IS_BDOS:   defs 1                 ; bdos_inspect_record: 1 = "BDOS" stamp present
BD_REC_NAME:      defs 10                ; bdos_inspect_record: the record's disk label (+210)

; --- free-record detection (B3) -----------------------------------------------
BD_LIST_SECTOR:   defs 1                 ; bdos_read_list_sector: 1-based list-sector number
BD_LIST_BUF:      defs 512              ; bdos_read_list_sector: the 512-byte list-sector data
BD_RECORDS:       defs 2                 ; bdos_find_free_record: total record count (LE word)
BD_FREE_RECORD:   defs 2                 ; bdos_find_free_record: first free record (1-based), or 0
BD_ENTRY_REC:     defs 2                 ; bdos_record_entry: input record number (1-based, LE)
BD_ENTRY_BUF:     defs 16               ; bdos_record_entry: the 16-byte list entry

; --- claim a pushed record (write its record-list name entry) -----------------
BD_CLAIM_NAME_PTR: defs 2                ; bdos_claim_record: pointer to the WRQ filename
BD_CLAIM_ENTRY:   defs 16               ; bdos_build_claim_entry: the 16-byte name entry to write

; --- i114c storage-class validation and raw-record write (B1-B3) ---------------
BD_REC_SIZE:      defs 4                 ; bdos_validate_disk_record: total image byte length (32-bit LE)
BD_REC_VALID:     defs 1                 ; bdos_validate_disk_record: 1 = valid DiskRecord, else 0

BD_WRITE_TRACK:   defs 1                 ; bdos_write_sector: track to write (0-79)
BD_WRITE_SECTOR:  defs 1                 ; bdos_write_sector: sector to write (1-10)
BD_WRITE_BUF:     defs 512              ; bdos_write_sector: source data (512 bytes)

BD_WRITE_START:   defs 2                 ; bdos_write_record: starting linear sector (0-based)
BD_WRITE_COUNT:   defs 2                 ; bdos_write_record: number of sectors to write

; --- i122a boot-a-record primitive --------------------------------------------
BD_BOOT_RECORD:   defs 1                 ; bdos_boot_record: record to select + ALHK-boot

; --- i317 delete-a-record primitive -------------------------------------------
BD_DELETE_RECORD: defs 2                 ; bdos_free_record: 1-based record to free (zero its list entry)
