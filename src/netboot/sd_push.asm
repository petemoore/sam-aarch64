; sd_push.asm — the small trinload-pushable cj.mgt -> Trinity-SD-record pusher (i293).
;
; THE WHOLE JOB, in trinload's own idiom ("a little network bit-pushing"), but with
; RAW absolute-LBA SD writes and NO B-DOS dependency. Pushed to the SAM via trinload
; (page P, offset &8000), it:
;
;   1. reads the SAM's MAC/IP from the "Trinity Network " EEPROM chunk (eeprom.asm),
;      inits the ENC28J60 (encdrv.asm), reads the inserted card's CSD to learn its
;      record count + data base (sd_csd.asm csd_base), and auto-picks the FIRST FREE
;      record (bdos_seam.asm bdos_find_free_record — reads the on-card record list via
;      the real CMD17, NETBOOT_REAL_LISTREAD). SAFETY: it only ever writes a free
;      record (one whose 16-byte catalogue name has (name[0] AND 127) == 0).
;   2. CLAIMS the record's catalogue/list name (bdos_claim_record — a read-modify-write
;      of the one list sector via raw CMD17+CMD24, NOT a B-DOS hook), so the record
;      appears in the SAM `RECORD` listing. This also builds the sanitised 16-byte
;      name in BD_CLAIM_ENTRY, reused for the sector-0 label below.
;   3. listens on UDP port 0xEDB0 for the .mgt stream and writes each 512-byte sector
;      to the record BODY by its ABSOLUTE card LBA (csd_base+1600*(n-1)+i) with our own
;      CMD24 (sd_csd.asm bd_record_write_hw; the card is inited once then streamed,
;      like B-DOS 1.5t hd.svb-t). On the FIRST body sector
;      (i==0) it MUTATES the buffer first — disk name@+210/+250 and "BDOS"@+232 — so
;      the record carries the B-DOS validity stamp permanently inside its first sector
;      (the SamDisk WriteRecord transformation). NO HRECORD, NO HWSAD, NO B-DOS rst 8
;      on the write path: every write is a raw CMD24 the ROM's SD driver is not needed
;      for, so the program does not depend on B-DOS being resident.
;   4. on a finalize message, validates the received sector count and RETs to trinload
;      (re-pushable), exactly like trinload's own clean exit.
;
; WHY OWN-LBA, NOT HWSAD/HRECORD (the design reversal, i295): the working host-tool
; reference is SamDisk's WriteRecord (SamDisk 3.x preserved source, record.cpp /
; SAMCoupe.cpp), which writes a non-B-DOS .mgt to a record by MUTATING the image
; (BDOS@232 + label@210/250 in sector 0) and writing the body + catalogue entry by
; absolute LBA — no B-DOS. Doing the same on the SAM sheds the HRECORD rst-8 crash
; class (HRECORD needed B-DOS paged a way it was not). The .mgt content is bit-for-bit
; except sector 0's mutated metadata.
;
; PROTOCOL (our own small framing on port 0xEDB0, modelled on trinload's ?/@):
;   '?'  discovery            -> reply "!"
;   '@'  data block           -> [linearSec LE16][<=512 data bytes]; copy the data into
;                                the 512-byte buffer, mutate sector 0 if i==0, write it
;                                to record-LBA csd_base+1600*(n-1)+i by own CMD24, then
;                                ACK the 4-byte header.
;   'F'  finalize             -> reply "D" (done) if the received-count == 1600
;                                (size-only: a record is exactly 1600 sectors =
;                                819200 bytes), else "E"; then RET to trinload (clean,
;                                re-pushable).
;   plus ARP-request and ICMP-echo replies (so the host can reach us), ported from
;   trinload's return_eth / return_arp / return_ip + the RFC-1071 checksum.
;
; VERIFICATION (emulation-first, CLAUDE.md rule 7): sd_push_faithful_test.go boots
; Colin's ROM + real B-DOS, loads this program, injects the cj.mgt stream over the ENC
; model, and asserts the catalogue entry + the 1600 body sectors + the sector-0
; mutation land at the correct absolute LBAs in the SD model (sdcard.go) — and ONLY
; there (data safety). Emulation-verified is not hardware-verified (CLAUDE.md §5): the
; real-Trinity run + a sector-0 +232 read-back ("BDOS", not "DBSO") is the final gate.
;
; ONE BUILD, FLAG-FREE (no NETBOOT_HOSTTEST carve-out, i231b): this file contains no
; `if defined(NETBOOT_HOSTTEST)` directive. NETBOOT_REAL_LISTREAD selects the real
; CMD17/CMD24 list read+write in bdos_seam.asm/sd_csd.asm (the boot images' path).

                org     &8000

HMPR:       equ &fb                     ; High Memory Page Register

; ===========================================================================
; CREATE-RECORD STRATEGY — own-LBA raw writes, image mutated (SamDisk's approach).
; ===========================================================================
; The root bug this file fixes (i293/i295): a Trinity record has TWO independent
; on-card structures and sd_push only ever wrote ONE of them, so the record was not
; registered and the old HRECORD-select rst 8 crashed (HRECORD needed B-DOS paged a
; way it was not). This rewrite sheds B-DOS entirely: it writes BOTH structures by
; RAW absolute-LBA CMD24, exactly as the SamDisk host tool's WriteRecord does
; (SamDisk 3.x preserved source, record.cpp / SAMCoupe.cpp).
;
;   1. The CATALOGUE / record-LIST name — the 16-byte name at card list sectors
;      1..base-1 (record n's entry: sector 1+(n-1)/32, byte offset ((n-1)&31)<<4).
;      Registers the record so it appears in the SAM `RECORD` listing. A slot is
;      FREE iff (name[0] AND 127) == 0. Written by bdos_claim_record (raw CMD24,
;      SamDisk record.cpp:171-187).
;   2. The record BODY — 1600 sectors at csd_base+1600*(n-1)+i. The .mgt content,
;      written bit-for-bit EXCEPT the FIRST sector (i==0), into which we MUTATE the
;      B-DOS validity metadata: the disk name at +210/+250 and the "BDOS" stamp at
;      +232 (SamDisk record.cpp:163-165 + SAMCoupe.cpp:101-110). cj.mgt itself
;      carries NEITHER (cj.mgt[232]=00); B-DOS get.label reads body track-0 +232 for
;      "BDOS" (bdos15a.src.txt:2834-2848), new.lab writes name@210/"BDOS"@232/name@250
;      (bdos15a.src.txt:2773-2794). The stamp is INSIDE body sector 0 — NOT a
;      separate sector — so it is permanent and survives the full stream.
;
; ONE path, no toggle, no B-DOS rst-8 hooks on the write path: claim the catalogue
; name (raw CMD24) -> stream the body by own-LBA CMD24 (bd_record_write_hw), mutating
; sector 0 in flight. The SAME 16-byte name (BD_CLAIM_ENTRY, built by the claim) feeds
; both the catalogue entry and the sector-0 label mutation.

                ; Entry: trinload's X packet does `out (HMPR),P; jp &8000`, landing
                ; here. The host harness runs sd_push_main by symbol; this jp is the
                ; hardware/trinload entry.
                jp      sd_push_main

; ===========================================================================
; Composed includes — the real wire driver, the EEPROM reader, the SD CSD/record
; math, and the B-DOS storage seam (the HWSAD/HRECORD hooks + free-record scan).
; They come FIRST (right after the boot jp) so every symbol they define — drv_init,
; the EEPROM `chunk` buffer sam_mac/sam_ip alias it, the bdos_* hooks, csd_base — is
; resolved before the code below references it (pyz80 is single-symbol-table, no
; forward `equ`-of-a-later-label). NETBOOT_REAL_LISTREAD selects the real CMD17 list
; read in the seam (sd_csd.asm bd_list_read_hw) — the boot image's path, host-
; verified against the SD model.
; ===========================================================================
                include "encdrv.asm"          ; drv_init / drv_read / drv_write / enc_rx_reestablish
                include "eeprom.asm"           ; find_index / read_chunk + value/chunk/name/part/total
                include "bdos_seam.asm"        ; bdos_find_free_record / bdos_claim_record / BD_CLAIM_ENTRY / bdos_id_str
                include "sd_csd.asm"           ; csd_set_bd_records (BD_RECORDS + csd_base) / bd_list_read_hw / bd_record_write_hw

; The SAM MAC/IP live in the EEPROM reader's `chunk` buffer (eeprom.asm): the
; "Trinity Network " chunk is read into `chunk`, MAC at +0, IP at +6. Defined here,
; after the include, so these aliases resolve before the code below uses them.
sam_mac:        equ chunk+0                    ; SAM MAC, from the loaded flash chunk
sam_ip:         equ chunk+6                    ; SAM IP

; ===========================================================================
; Network wire glue — ported from trinload.asm (simonowen/trinload @ a4b7af7):
; return_eth / return_arp / return_ip / return_udp + the RFC-1071 checksum. These
; are the "little network bit-pushing"; they swap MAC/IP/port fields in place and
; recompute the IP/ICMP checksums for a reply built from the received frame.
; ===========================================================================

; ack_len — ack a received UDP packet by returning part of it (possibly modified).
; BC holds the UDP data length to return.
ack_len:
                call    set_udp_data_len
                push    bc
                call    return_eth
                call    return_ip
                call    return_udp
                call    checksum_ip
                call    checksum_udp
                ld      hl, packet
                pop     bc
                jp      drv_write

; set_udp_data_len — set the UDP data length, updating the IP header. BC returns the
; total Ethernet frame length.
set_udp_data_len:
                ld      ix, packet
                ld      hl, 8                  ; UDP header length
                add     hl, bc
                ld      (ix+38), h             ; total UDP length
                ld      (ix+39), l
                ld      bc, 20                 ; IP header length
                add     hl, bc
                ld      (ix+16), h             ; total IP length
                ld      (ix+17), l
                ld      bc, 14                 ; Ethernet header length
                add     hl, bc                 ; full frame length
                ld      b, h
                ld      c, l
                ret

; ip_to_eth_len — Ethernet frame length from the IP header. BC = length on return.
ip_to_eth_len:
                ld      a, (packet+17)
                add     a, 6+6+2               ; Ethernet header (dst+src MAC + type)
                ld      c, a
                ld      a, (packet+16)
                adc     a, 0
                ld      b, a
                ret

; return_arp — swap MAC and IP in the ARP header.
return_arp:
                ld      hl, packet+22
                ld      de, packet+32
                ld      bc, 6+4
                ldir                           ; sender MAC+IP -> target
                ld      hl, sam_mac
                ld      de, packet+22
                ld      c, 6
                ldir                           ; SAM MAC -> sender
                ld      hl, sam_ip
                ld      c, 4
                ldir                           ; SAM IP -> sender
                ret

; return_eth — swap MAC addresses in the Ethernet header.
return_eth:
                ld      hl, packet+6           ; source MAC
                ld      de, packet             ; dest MAC
                ld      bc, 6
                ldir
                ld      hl, sam_mac
                ld      c, 6
                ldir
                ret

; return_ip — swap addresses in the IP header.
return_ip:
                ld      hl, packet+26          ; source IP
                ld      de, packet+30          ; dest IP
                ld      bc, 4
                ldir
                ld      hl, sam_ip
                ld      de, packet+26
                ld      c, 4
                ldir
                ret

; return_udp — swap ports in the UDP header.
return_udp:
                ld      hl, (packet+34)        ; source port
                ld      de, (packet+36)        ; dest port
                ld      (packet+36), hl
                ld      (packet+34), de
                ret

; checksum_ip — IP-header checksum (RFC 1071, big-endian).
checksum_ip:
                ld      ix, packet+14
                ld      a, (ix)
                and     &0f                    ; DWORDs in header
                add     a, a                   ; -> WORDs
                ld      c, a
                ld      b, 0
                ld      (ix+10), b             ; clear checksum for calculation
                ld      (ix+11), b
                call    chksum_blk
                ld      (ix+10), h             ; big-endian!
                ld      (ix+11), l
                ret

; checksum_icmp — ICMP header+data checksum (RFC 1071, big-endian).
checksum_icmp:
                ld      e, 0
                ld      ix, packet+6+6+2+20    ; ICMP header
                ld      a, (ix-20)             ; IP type + header length
                and     &0f
                add     a, a
                add     a, a                   ; number of bytes
                ld      d, a
                ld      a, (ix-17)             ; total length LSB
                sub     d                      ; subtract IP header size
                ld      c, a
                ld      a, (ix-18)             ; total length MSB
                sbc     a, e
                ld      b, a
                ld      hl, packet+6+6+2+20
                add     hl, bc                 ; position after ICMP data
                ld      (hl), e                ; clear in case checksum uses it
                inc     bc                     ; round up for the word count
                srl     b
                rr      c
                ld      (ix+2), e              ; clear checksum for calculation
                ld      (ix+3), e
                call    chksum_blk
                ld      (ix+2), h              ; big-endian!
                ld      (ix+3), l
                ret

; checksum_udp — UDP checksum is optional; zero it.
checksum_udp:
                ld      hl, 0
                ld      (packet+40), hl
                ret

; chksum_blk — RFC-1071 internet checksum. IX = block start, BC = block length in
; words. Returns the inverted 16-bit sum in HL.
chksum_blk:
                push    ix
                ld      hl, 0
                ld      a, c                   ; swap byte order for the loop counters
                ld      c, b
                ld      b, a
                inc     c
                and     a                      ; clear carry for ADC
cblk_loop:
                ld      d, (ix)                ; big-endian word
                ld      e, (ix+1)
                adc     hl, de
                inc     ix
                inc     ix
                djnz    cblk_loop
                dec     c
                jr      nz, cblk_loop
                jr      nc, cblk_end
                inc     hl                     ; final carry
cblk_end:
                ld      a, h
                cpl
                ld      h, a
                ld      a, l
                cpl
                ld      l, a
                pop     ix
                ret

; ===========================================================================
; sd_push_main — the bootable / trinload entry.
; ===========================================================================
sd_push_main:
                di
                ld      a, "1"                 ; DBG: entered main (after di)
                call    dbg_char

                ; --- locate + read the "Trinity Network " EEPROM chunk -> MAC/IP --
                ld      a, 1
                ld      (part), a
                ld      (total), a
                ld      hl, sp_chunk_name
                ld      de, name
                ld      bc, 16
                ldir
                call    find_index
                ld      a, (value)
                and     a
                jp      z, sp_fail_cfg
                call    read_chunk
                ld      a, (value)
                and     a
                jp      z, sp_fail_cfg

                ; both MAC and IP zero? -> missing settings.
                ld      a, (sam_mac+0)
                ld      b, a
                ld      a, (sam_ip+0)
                or      b
                jp      z, sp_fail_cfg
                ld      a, "2"                 ; DBG: EEPROM net-config (MAC/IP) OK
                call    dbg_char

                ; --- init the ENC28J60 FIRST, before any SD work (i242) -----------
                ; drv_init's chk_trinity identity probe uses fixed delays, not a BUSY
                ; poll, so it must run against a quiescent microcontroller. The heavy
                ; &38 SD init leaves the shared PIC settling, which makes a later
                ; chk_trinity read stale (the i242 finding) — so ENC first, SD second.
                ld      hl, sam_mac
                call    drv_init
                ld      a, b
                or      c
                jp      z, sp_fail_init
                ld      a, "3"                 ; DBG: drv_init (ENC28J60) OK
                call    dbg_char

                ; --- read the inserted card's CSD -> BD_RECORDS + csd_base ---------
                ; csd_set_bd_records (sd_csd.asm) runs the bounded init ladder + CMD9,
                ; decodes the record count, and stores csd_base (the first data
                ; sector). On a read failure BD_RECORDS stays 0 -> "no free record".
                call    csd_set_bd_records
                ld      a, "4"                 ; DBG: CSD read done (CMD9)
                call    dbg_char

                ; --- auto-pick the first FREE record (read-only list scan) ---------
                ; bdos_find_free_record reads the on-card record list (the real CMD17)
                ; and returns the lowest free record in BD_FREE_RECORD (0 if none free).
                ; SAFETY: a free record is one whose list entry is unnamed, so the write
                ; can never land on a record someone else created.
                call    bdos_find_free_record
                ld      hl, (BD_FREE_RECORD)
                ld      a, h
                or      l
                jp      z, sp_fail_nofree      ; no free record: decline, do not write
                ld      a, "5"                 ; DBG: free record found (CMD17 list scan OK)
                call    dbg_char

                ; =====================================================================
                ; The record's CATALOGUE / list-name entry (the ONLY create step) makes it
                ; appear in the SAM `RECORD` listing — registered by the DEFERRED claim
                ; (sp_claim_once, on the first body block; contract below = bdos_claim_record).
                ; The "BDOS"
                ; validity stamp goes INSIDE the body's first sector (mutated in the serve
                ; loop, i==0), not a separate sector, and the body is written by own LBA.
                ; NO HRECORD-select and NO HWSAD: the write path uses no B-DOS rst-8 hook.
                ;
                ; bdos_claim_record (bdos_seam.asm:735) register contract (READ + cited):
                ;   In:  BD_FREE_RECORD   2B  the 1-based record to claim (>=1; set by the
                ;                             scan above).
                ;        BD_CLAIM_NAME_PTR 2B  pointer to a NUL-terminated name; sanitised +
                ;                             space-padded to the 16-byte record-name field
                ;                             in BD_CLAIM_ENTRY by bdos_build_claim_entry,
                ;                             which GUARANTEES name[0] is a legal non-zero,
                ;                             bit7-clear char (so the slot reads NAMED, never
                ;                             free/write-protected).
                ;   Out: the record's 16-byte list entry written via read-modify-write of its
                ;        one containing list sector (raw CMD17 read + CMD24 write under
                ;        NETBOOT_REAL_LISTREAD, no rst 8); BD_CLAIM_ENTRY now holds the built
                ;        16-byte name, which the sector-0 label mutation reuses. Clobbers
                ;        A, BC, DE, HL.
                ;   SAFETY: it touches ONLY BD_FREE_RECORD's own 16-byte entry; every other
                ;   entry in the list sector is preserved byte-for-byte, and the slot was
                ;   proven free, so no other user's record is ever overwritten.
                ; The claim is DEFERRED to the first body block (sp_claim_once, called
                ; from sp_data_block) so an 'N' message can set the pushed record name
                ; before the 16-byte list entry + BD_CLAIM_ENTRY are built. Only the
                ; free-record PICK (above) runs at boot; nothing is written to the card
                ; until the first '@' block arrives.

                ; NO ENC re-arm after the startup SD work. An SD transaction on the Trinity
                ; controller does NOT disturb the ENC's autonomous RX: the &DC byte only
                ; mux-selects which SPI peripheral the Z80 talks to; the ENC's ECON1.RXEN
                ; (set once in drv_init) keeps the RX FIFO filling independently of the mux
                ; (ENC28J60 datasheet §7.2 + simonowen/trinload encdrv.asm: drv_read never
                ; re-enables RX; only &28 ereset disturbs it, and that is init/exit only).
                ; The byte-level drain rule (poll &DC bit 3 between port switches, which
                ; encdrv/sd_csd/bdos already do) keeps individual port switches safe; with
                ; RX undisturbed there is nothing further to restore.

                ; reset the received-sector counter (finalize checks it == 1600).
                ld      hl, 0
                ld      (sp_recv_count), hl

sp_serve_loop:
                ; Esc-to-exit (trinload idiom): poll the keyboard; on Esc, RET to
                ; trinload's start (it pushed start as our return address).
                ld      a, &f7
                in      a, (&f9)
                bit     5, a                   ; Esc pressed?
                ret     z                      ; -> trinload's start

                ; read one frame; loop if nothing available.
                ld      hl, packet
                call    drv_read               ; BC = length (0 if nothing)
                ld      a, b
                or      c
                jr      z, sp_serve_loop

                ; --- frame type: IPv4 or ARP? -----------------------------------
                ld      a, (packet+12)         ; Ethernet frame type MSB
                cp      &08
                jr      nz, sp_serve_loop

                ld      a, (packet+13)
                cp      &06                    ; ARP?
                jr      nz, sp_try_ipv4

                ; ARP request for our IP? -> reply.
                ld      a, (packet+21)
                cp      &01                    ; request?
                jr      nz, sp_serve_loop
                inc     a                      ; -> reply (2)
                ld      (packet+21), a
                ld      hl, (packet+38)        ; requested IP (first 2 bytes)
                ld      de, (sam_ip)
                and     a
                sbc     hl, de
                jr      nz, sp_serve_loop      ; not for us
                ld      hl, (packet+40)        ; requested IP (second 2 bytes)
                ld      de, (sam_ip+2)
                and     a
                sbc     hl, de
                jr      nz, sp_serve_loop      ; not for us
                call    return_eth
                call    return_arp
                ld      hl, packet
                ld      bc, 42
                call    drv_write
                jr      sp_serve_loop

sp_try_ipv4:
                and     a                      ; IPv4? (type LSB == 0)
                jr      nz, sp_serve_loop
                ld      a, (packet+20)         ; IP flags
                and     %00100000              ; fragmented?
                jr      nz, sp_serve_loop      ; we don't reassemble

                ld      a, (packet+23)
                cp      &01                    ; ICMP?
                jr      nz, sp_try_udp

                ld      a, (packet+34)
                cp      &08                    ; echo request?
                jr      nz, sp_serve_loop
                xor     a
                ld      (packet+34), a         ; -> echo reply
                call    return_eth
                call    return_ip
                call    checksum_ip
                call    checksum_icmp
                ld      hl, packet
                call    ip_to_eth_len
                call    drv_write
                jp      sp_serve_loop

sp_try_udp:
                cp      &11                    ; UDP?
                jp      nz, sp_serve_loop
                ld      de, (packet+36)        ; destination port
                ld      hl, &b0ed              ; port 0xEDB0
                and     a
                sbc     hl, de
                jp      nz, sp_serve_loop      ; not for us

                ; unicast to our IP? (the data/finalize messages are unicast)
                ld      hl, (packet+30)        ; target IP (first 2 bytes)
                ld      de, (sam_ip)
                and     a
                sbc     hl, de
                jp      nz, sp_serve_loop
                ld      hl, (packet+32)        ; target IP (second 2 bytes)
                ld      de, (sam_ip+2)
                and     a
                sbc     hl, de
                jp      nz, sp_serve_loop

                ; dispatch on the first UDP data byte (packet+42).
                ld      a, (packet+42)
                cp      "?"                    ; discovery?
                jr      nz, sp_not_disc
                ld      a, "!"
                ld      (packet+42), a
                ld      bc, 1
                call    ack_len
                jp      sp_serve_loop

sp_not_disc:
                cp      "@"                    ; data block?
                jr      nz, sp_not_data
                jp      sp_data_block

sp_not_data:
                cp      "N"                    ; record name?
                jr      nz, sp_not_name
                jp      sp_set_name
sp_not_name:
                cp      "F"                    ; finalize?
                jp      nz, sp_serve_loop
                jp      sp_finalize

; ---------------------------------------------------------------------------
; sp_set_name — an 'N' message: payload = [<=16 record-name bytes]. Copy into the
; writable sp_record_name buffer (truncated to 16, NUL-terminated) so the deferred
; claim (sp_claim_once) and the sector-0 label carry the PUSHED filename instead of
; the default. Must arrive before the first '@' block (the pusher sends it right after
; discovery). Clobbers AF, BC, DE, HL.
; ---------------------------------------------------------------------------
sp_set_name:
                ; name length = UDP data length - 8 (UDP hdr) - 1 ('N'), clamped to 16.
                ld      ix, packet+14+20       ; UDP header start
                ld      a, (ix+5)              ; UDP length LSB (a record name never spans 256 B)
                sub     8+1
                cp      17                     ; >16 (or an underflow) -> clamp to 16
                jr      c, spsn_len_ok
                ld      a, 16
spsn_len_ok:
                ld      hl, packet+43          ; first name byte
                ld      de, sp_record_name
                or      a
                jr      z, spsn_terminate      ; empty name: just NUL-terminate
                ld      b, a
spsn_copy:
                ld      a, (hl)
                ld      (de), a
                inc     hl
                inc     de
                djnz    spsn_copy
spsn_terminate:
                xor     a
                ld      (de), a                ; NUL-terminate (<=16 chars in a 17-byte buffer)
                ld      a, "."                 ; ack (mirror the block ack)
                ld      (packet+42), a
                ld      bc, 1
                call    ack_len
                jp      sp_serve_loop

; ---------------------------------------------------------------------------
; sp_claim_once — register the picked free record's catalogue list-name entry from the
; (default or 'N'-set) sp_record_name and build BD_CLAIM_ENTRY (reused by the sector-0
; label). Idempotent via sp_claimed: the first body block claims, the rest skip.
; Deferred from boot so an 'N' message can set the name first. The claim is a raw CMD24
; (bdos_claim_record, no B-DOS rst 8). Clobbers AF, BC, DE, HL.
; ---------------------------------------------------------------------------
sp_claim_once:
                ld      a, (sp_claimed)
                or      a
                ret     nz                     ; already claimed this push
                ld      a, 1
                ld      (sp_claimed), a
                ld      hl, sp_record_name     ; default 'cj.mgt' or the 'N'-pushed name
                ld      (BD_CLAIM_NAME_PTR), hl
                call    bdos_claim_record      ; builds BD_CLAIM_ENTRY + writes the list entry
                ld      a, "7"                 ; DBG: catalogue entry written
                call    dbg_char
                ld      a, "R"                 ; DBG: record number claimed + written to
                call    dbg_char
                ld      hl, (BD_FREE_RECORD)
                call    dbg_hl                 ; the 1-based record number (hex)
                ret

; ---------------------------------------------------------------------------
; sp_data_block — a '@' block: payload = [linearSec LE16][<=512 data bytes].
; Copy the data into the 512-byte sector buffer; on linearSec 0 mutate the B-DOS
; validity metadata into it; write it to record n's body sector by absolute LBA
; (own CMD24); count it; then ACK the 4-byte header.
; ---------------------------------------------------------------------------
sp_data_block:
                ; Register the catalogue entry on the FIRST body block (idempotent), so an
                ; 'N' message's pushed name is captured before BD_CLAIM_ENTRY is built and
                ; reused by the sector-0 label below.
                call    sp_claim_once
                ; UDP data length (bytes after the 8-byte UDP header) -> BC.
                ld      ix, packet+14+20       ; UDP header start
                ld      a, (ix+5)              ; UDP length LSB
                sub     8                      ; minus the UDP header
                ld      c, a
                ld      a, (ix+4)              ; UDP length MSB
                sbc     a, 0
                ld      b, a                   ; BC = UDP data length (= 1 + 2 + dataLen)

                ; data length = UDP data length - 3 (the '@' byte + 2 linearSec bytes).
                ld      hl, -3
                add     hl, bc                 ; HL = data byte count
                ld      b, h
                ld      c, l                   ; BC = data byte count (0..512)

                ; THE .mgt -> record-LBA MAP: the host sends a 0-based LINEAR SECTOR i
                ; (0..1599); we write it to record n's body sector i by absolute card LBA
                ; csd_base+1600*(n-1)+i (bd_record_write_hw does that math). linearSec
                ; arrives at packet+43 (LE16); latch it for the write + the i==0 test.
                ld      hl, (packet+43)        ; linearSec i (LE16)
                ld      (BD_WRITE_START), hl

                ; clear the sector buffer, then copy the <=512 data bytes into it (a
                ; short final block leaves the tail zero — the record's last sector).
                push    bc                     ; save data byte count
                ld      hl, BD_WRITE_BUF
                ld      de, BD_WRITE_BUF+1
                ld      bc, 511
                ld      (hl), 0
                ldir                           ; zero-fill 512 bytes
                pop     bc                     ; restore data byte count

                ; copy BC data bytes from the packet (packet+42+3 = packet+45).
                ld      a, b
                or      c
                jr      z, sp_data_write       ; empty block: write a zero sector
                ld      hl, packet+45          ; first data byte
                ld      de, BD_WRITE_BUF
                ldir

sp_data_write:
                ; SECTOR-0 MUTATION (the SamDisk WriteRecord transformation): on the FIRST
                ; body sector (i==0) only, write the B-DOS validity metadata INTO the image
                ; before the SD write, so the record carries the stamp permanently in its
                ; first sector (cj.mgt has none of its own). Offsets cited from B-DOS
                ; new.lab (bdos15a.src.txt:2773-2794) / SamDisk record.cpp:163-165 +
                ; SAMCoupe.cpp:101-110:
                ;   * disk name  = BD_CLAIM_ENTRY[0..9]   -> BD_WRITE_BUF+210 (10 bytes)
                ;   * "BDOS" stamp = bdos_id_str (4 bytes) -> BD_WRITE_BUF+232 (get.label reads
                ;                                             body track-0 +232, bdos15a:2839)
                ;   * name tail  = BD_CLAIM_ENTRY[10..15] -> BD_WRITE_BUF+250 (6 bytes)
                ; The 16-byte name is the SAME BD_CLAIM_ENTRY the catalogue claim built, so
                ; the on-card label matches the catalogue title. "BDOS" is written verbatim
                ; (NOT byte-swapped) to match B-DOS get.label — a hardware sector-0 +232
                ; read-back should show "BDOS", not "DBSO".
                ld      hl, (BD_WRITE_START)   ; i
                ld      a, h
                or      l
                jr      nz, sp_data_lba        ; i != 0: no mutation, write as-is
                ld      hl, BD_CLAIM_ENTRY     ; name[0..9] -> +210
                ld      de, BD_WRITE_BUF+210
                ld      bc, 10
                ldir
                ld      hl, bdos_id_str        ; "BDOS" -> +232
                ld      de, BD_WRITE_BUF+232
                ld      bc, 4
                ldir
                ld      hl, BD_CLAIM_ENTRY+10  ; name[10..15] -> +250
                ld      de, BD_WRITE_BUF+250
                ld      bc, 6
                ldir

sp_data_lba:
                ; write the (possibly mutated) sector to record n's body sector i by its
                ; ABSOLUTE card LBA, via our own CMD24 (init-once) — NO B-DOS rst 8.
                ; bd_record_write_hw (sd_csd.asm:606) contract: In BD_REC_WRITE_REC = n,
                ; BD_REC_WRITE_LINEAR = i, HL = 512-byte source -> CMD24 to
                ; csd_base+1600*(n-1)+i (LBA math sd_csd.asm:578; data-safe band :588).
                ; Clobbers AF,BC,DE,HL; card deselected + EI on every path.
                ld      hl, (BD_FREE_RECORD)   ; n = the claimed record
                ld      (BD_REC_WRITE_REC), hl
                ; THE .mgt -> record REORDER (i315/i294): the host streams a .mgt
                ; container TRACK-major (samfile: m = cyl*20 + side*10 + (sector-1)); a
                ; Trinity SD record stores the disk SIDE-major (B-DOS 1.5t conv.de &A151:
                ; rec = side*800 + track*10 + (sector-1)). Map the received .mgt linear m
                ; to its side-major record linear BEFORE the write, so a streamed .mgt
                ; boots (a track-major write shows the directory but reads scrambled
                ; side-1/higher-track data -> '108 End of file'). Directory sector m==0
                ; maps to rec 0 (aligned under both orders), so the sector-0 label
                ; mutation above — keyed off the raw m — stays correct.
                ld      hl, (BD_WRITE_START)   ; m = this block's .mgt linear sector
                call    mgt_to_record_linear   ; HL = side-major record linear
                ld      (BD_REC_WRITE_LINEAR), hl
                ld      hl, BD_WRITE_BUF       ; 512-byte source sector
                call    bd_record_write_hw     ; own CMD24, absolute LBA

                ; count the received sector (finalize checks the total == 1600).
                ld      hl, (sp_recv_count)
                inc     hl
                ld      (sp_recv_count), hl

                ; NO ENC re-arm after the CMD24 write. The write is an SD transaction, but
                ; an SD transaction does NOT disturb the ENC's autonomous RX (only the &DC
                ; mux-select changes, which the ENC's internal RXEN cannot see; the RX FIFO
                ; keeps filling — ENC28J60 datasheet §7.2 + encdrv.asm: drv_read never
                ; re-enables RX, only &28 ereset does, init/exit only). So the NEXT drv_read
                ; and this ACK's drv_write both work with zero re-arm between blocks. The
                ; byte-level drain rule (already honoured by encdrv/sd_csd/bdos) is what
                ; keeps the alternating ENC/SD port switches safe.

                ; ACK the 4-byte header (mirror trinload's @-ack).
                ld      a, "."
                ld      (packet+42), a         ; reuse the header bytes as the ack body
                ld      bc, 4
                call    ack_len
                jp      sp_serve_loop

; ---------------------------------------------------------------------------
; mgt_to_record_linear (i315/i294) — map a .mgt TRACK-major linear sector to the
; SIDE-major linear sector a Trinity SD record stores, so a streamed .mgt boots.
;
;   .mgt   (track-major, samfile):                 m   = cyl*20  + side*10 + (sector-1)
;   record (side-major, B-DOS 1.5t conv.de &A151): rec = side*800 + cyl*10 + (sector-1)
;
; With sec1 = m mod 10, q10 = m / 10, side = q10 bit0, cyl = q10 >> 1:
;   rec = side*800 + cyl*10 + sec1
; m==0 (track0/side0/sector1, the directory/label) maps to rec 0 — aligned under
; both orders — so the caller's sector-0 mutation, keyed off the raw m, is correct.
;
; Compute record_linear FROM the received m (not an incrementing counter): the
; windowed push (sd-push.py) may retransmit, so blocks can arrive out of order.
;
; In:  HL = m    (0..1599, the received .mgt linear sector)
; Out: HL = rec  (0..1599, the side-major record linear sector)
; Clobbers A, BC, DE.
; ---------------------------------------------------------------------------
mgt_to_record_linear:
                ; divide m by 10: HL := q10 (0..159, H=0), C := sec1 (0..9). (div_hl_10.)
                ld      c, 0
                ld      b, 16
m2r_bit:
                add     hl, hl
                ld      a, c
                rla
                cp      10
                jr      c, m2r_nosub
                sub     10
                inc     l
m2r_nosub:
                ld      c, a
                djnz    m2r_bit
                ; C = sec1 (untouched to the end). Split q10 (in L, H=0): side=bit0, cyl=>>1.
                ld      a, l
                srl     a                      ; A = cyl (0..79), CF = side
                ld      b, 0
                rl      b                      ; B = side (0 or 1)
                ; HL := cyl*10
                ld      l, a
                ld      h, 0
                add     hl, hl                 ; 2*cyl
                ld      d, h
                ld      e, l                   ; DE = 2*cyl
                add     hl, hl                 ; 4*cyl
                add     hl, hl                 ; 8*cyl
                add     hl, de                 ; 10*cyl
                ; += side*800
                bit     0, b
                jr      z, m2r_addsec1         ; side 0: no 800
                ld      de, 800
                add     hl, de
m2r_addsec1:
                ; += sec1 (in C; B=0 so BC = sec1)
                ld      b, 0
                add     hl, bc
                ret

; ---------------------------------------------------------------------------
; sp_finalize — an 'F' message: validate the received sector count (size-only:
; a Trinity record is exactly 1600 sectors = 819200 bytes — the
; bdos_validate_disk_record contract), reply "D" (done) or "E" (error), then RET
; to trinload (clean, re-pushable).
; ---------------------------------------------------------------------------
sp_finalize:
                ld      hl, (sp_recv_count)
                ld      de, 1600               ; a full record is 1600 sectors
                and     a
                sbc     hl, de
                ld      a, "E"                 ; default: wrong count -> error
                jr      nz, sp_fin_reply
                ld      a, "D"                 ; complete record -> done
sp_fin_reply:
                ld      (packet+42), a
                ld      bc, 1
                call    ack_len
                ld      a, "9"                 ; DBG: write-complete (record body finalised)
                call    dbg_char
                ; clean exit: RET to trinload's start (it pushed start as our return).
                ret

; ---------------------------------------------------------------------------
; dbg_char — print the character in A to the SAM screen via the ROM print-a-char
; restart (RST &10, the trinload idiom). Preserves EVERY register so a marker can
; sit between live-register startup stages without disturbing them. i295 debug:
; localise where sd_push dies on hardware (which marker is the last one shown).
; ---------------------------------------------------------------------------
dbg_char:
                push    af
                push    bc
                push    de
                push    hl
                push    ix
                push    iy
                rst     &10                    ; ROM prints A to the current channel
                pop     iy
                pop     ix
                pop     hl
                pop     de
                pop     bc
                pop     af
                ret

; dbg_hex — print A as two hex digits; dbg_hl — print HL as four. i295 debug.
dbg_hex:
                push    af
                push    bc
                ld      c, a
                rrca
                rrca
                rrca
                rrca
                call    dbg_nib                ; high nibble
                ld      a, c
                call    dbg_nib                ; low nibble
                pop     bc
                pop     af
                ret
dbg_nib:
                and     15
                cp      10
                jr      c, dbg_nib_dec
                add     a, "A" - 10
                jr      dbg_nib_emit
dbg_nib_dec:
                add     a, "0"
dbg_nib_emit:
                call    dbg_char
                ret
dbg_hl:
                push    af
                ld      a, h
                call    dbg_hex
                ld      a, l
                call    dbg_hex
                pop     af
                ret
; dbg_hold_esc — hold the CPU (screen frozen) until Esc, then RET to trinload.
; Keeps trinload from resuming + scrolling away the debug output. i295 debug.
dbg_hold_esc:
                ld      a, &f7
                in      a, (&f9)
                bit     5, a                   ; Esc?  (same poll as the serve loop)
                jr      nz, dbg_hold_esc       ; not pressed -> keep holding
                ret                            ; Esc -> RET to trinload (recoverable)

; ---------------------------------------------------------------------------
; Failure paths — show a diagnostic border, hold until Esc, then RET to trinload
; (never a raw di;halt, which would strand the SAM and cost a power-cycle).
; ---------------------------------------------------------------------------
sp_fail_cfg:
                ld      a, 2                   ; red: no/bad network settings
                jr      sp_fail_show
sp_fail_init:
                ld      a, 1                   ; blue: ENC28J60 init failed
                jr      sp_fail_show
sp_fail_nofree:
                ld      a, 6                   ; yellow: no free record (nothing written)
sp_fail_show:
                out     (&fe), a
sp_fail_wait:
                ld      a, &f7
                in      a, (&f9)
                bit     5, a                   ; Esc?
                jr      nz, sp_fail_wait       ; hold the border until Esc
                ret                            ; -> trinload's start

; ===========================================================================
; Data.
; ===========================================================================
sp_chunk_name:  defm "Trinity Network "       ; the EEPROM chunk holding MAC+IP

; The record name written into BOTH the catalogue entry (bdos_claim_record) and the
; sector-0 label mutation (the serve loop, i==0) — both read the SAME 16-byte name from
; BD_CLAIM_ENTRY that the claim builds from this string. An 'N' message overwrites this
; buffer with the pushed filename (sp_set_name) BEFORE the deferred claim (sp_claim_once)
; runs, so each push is catalogued under its own name; "cj.mgt" is the default used when
; no 'N' is sent. The buffer is 17 bytes: a <=16-char name + NUL. bdos_build_claim_entry
; strips a "trinity-sam-disks/" prefix, takes the final path segment, drops the dotted
; suffix (so "cj.mgt" -> "cj"), sanitises to the legal 0x20..0x7E charset, and space-pads
; to the 16-byte record-name field. byte0 non-zero, bit7 clear -> the slot reads NAMED
; (never accidentally free) and is not write-protected, so bdos_find_free_record skips
; this record on the next push.
sp_record_name: defm "cj.mgt"                  ; DEFAULT; an 'N' message overwrites it
                defb 0                          ; NUL terminator
                defs 10                         ; room for a <=16-char pushed name + NUL (17 B)
sp_claimed:     defb 0                          ; 0 until sp_claim_once registers the record
sp_recv_count:  defw 0                         ; sectors received this session

packet:         defs 1518                      ; RX/TX frame buffer (drv_read fills it)

length:         equ $ - sd_push_main           ; code length (diagnostic)
