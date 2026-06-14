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
; THE HONESTY LINE (CLAUDE.md §5): these routines are host-verifiable — they
; only build / decode memory. The actual hook DISPATCH (RST 8 / DEFB 129 HGTHD,
; / DEFB 130 HLOAD, / DEFB 132 HSAVE, and the HRECORD &9C record select) is NOT
; host-verifiable: the flat-memory koron-go/z80 harness has no ROM, no SAMDOS
; bank, and no RST 8 dispatch, so the hook bodies cannot run host-side. Those
; calls live in bdos_hooks (below) behind `ifndef NETBOOT_HOSTTEST`, excluded
; from the host build, and stay UNVERIFIED until exercised on real Trinity
; hardware (the i62 dual-run proved the hooks round-trip on real SAMDOS-2 +
; B-DOS-AL backends; the netboot fetch end-to-end is the final integration
; gate). Emulation-verified is not hardware-verified.
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
BD_NAME_LEN:      equ 10
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
; ---------------------------------------------------------------------------
bdos_name_to_uifa:
                ; type = CODE
                ld      a, BD_TYPE_CODE
                ld      (BD_UIFA + BD_OFF_TYPE), a

                ; name field: copy up to 10 chars, then space-pad the rest.
                ld      hl, (BD_NAME_PTR)      ; source filename
                ld      de, BD_UIFA + BD_OFF_NAME
                ld      b, BD_NAME_LEN
bnu_name:
                ld      a, (hl)
                or      a
                jr      z, bnu_pad             ; hit the NUL: pad the remainder
                ld      (de), a
                inc     hl
                inc     de
                djnz    bnu_name
                jr      bnu_ext                ; exactly 10 (or more) chars: no pad
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

; ===========================================================================
; The real B-DOS hook dispatch — NOT host-verifiable (no ROM / SAMDOS bank in
; the harness). Excluded from the host build (NETBOOT_HOSTTEST). Stays
; unverified until exercised on real Trinity hardware (CLAUDE.md §5). The bodies
; are the established in-tree idioms (src/loader.asm HGTHD path; src/io.asm
; HSAVE note); the HRECORD &9C select is the B-DOS mass-storage step.
; ===========================================================================
                if defined(NETBOOT_HOSTTEST)==0

BD_UIFA_ADDR:     equ &4B00              ; the SAMDOS UIFA buffer (real address)
BD_DIFA_ADDR:     equ &4B50              ; SAMDOS deposits the DIFA here
BD_HOOK_HRECORD:  equ &9C                ; B-DOS record select (156)
BD_HOOK_HGTHD:    equ 129                ; get file header (find by name)
BD_HOOK_HSAVE:    equ 132                ; save whole file
BD_HOOK_HRSAD:    equ 160                ; read raw 512-byte sector (HRSAD)

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
                ld      a, (BD_READ_TRACK)
                ld      d, a
                ld      a, (BD_READ_SECTOR)
                ld      e, a
                ld      hl, BD_READ_BUF
                rst     8
                defb    BD_HOOK_HRSAD
                ret

                endif

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
