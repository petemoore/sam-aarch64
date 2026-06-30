; sd_push.asm — the small trinload-pushable cj.mgt -> Trinity-SD-record pusher (i293).
;
; THE WHOLE JOB, in trinload's own idiom: "a little network bit-pushing + a couple
; of B-DOS hook calls". Pushed to the SAM via trinload (page P, offset &8000), it:
;
;   1. reads the SAM's MAC/IP from the "Trinity Network " EEPROM chunk (eeprom.asm),
;      inits the ENC28J60 (encdrv.asm), reads the inserted card's CSD to learn its
;      record count + data base (sd_csd.asm), and auto-picks the FIRST FREE record
;      (bdos_seam.asm bdos_find_free_record — reads the on-card record list via the
;      real CMD17, NETBOOT_REAL_LISTREAD). SAFETY: it only ever writes a free record.
;   2. HRECORD-selects that free record (bdos_select_record), then listens on UDP
;      port 0xEDB0 for the .mgt stream and writes each 512-byte sector into the
;      selected record with the B-DOS HWSAD hook (bdos_write_sector, A=2 = Trinity
;      SD). NO own-CMD24, NO per-sector SD init-ladder. NO per-block ENC re-arm: an
;      SD transaction does NOT disturb the ENC's autonomous RX (the &DC byte only
;      mux-selects which SPI peripheral the Z80 talks to; the ENC's ECON1.RXEN, set
;      once in drv_init, keeps the RX FIFO filling independently — ENC28J60 datasheet
;      §7.2 + simonowen/trinload encdrv.asm, where drv_read never re-enables RX and
;      only &28 ereset disturbs it, at init/exit only). The byte-level drain rule
;      (poll &DC bit 3 between port switches, which encdrv/sd_csd/bdos already do) is
;      what keeps the alternating ENC/SD port switches safe.
;   3. on a finalize message, validates the received sector count and RETs to
;      trinload (re-pushable), exactly like trinload's own clean exit.
;
; RECORD-DIRECTED WRITE — HRECORD sets PERSISTENT B-DOS state that crosses the rst 8
; boundary by construction: the record.no/record.t DVARs and, via record-selection,
; the self-modifying seek-base immediates at &A185/&A188 in the resident B-DOS page.
; HWSAD's seek reads those immediates + the per-access linear sector to form the
; CMD24 argument, so sector i of record n lands at csd_base + 1600*(n-1) + i. One
; HRECORD-select up front therefore directs every subsequent per-sector HWSAD into
; the chosen record — no per-write re-select.
;
; PROTOCOL (our own small framing on port 0xEDB0, modelled on trinload's ?/@):
;   '?'  discovery            -> reply "!"
;   '@'  data block           -> [linearSec LE16][<=512 data bytes]; copy the data
;                                into the 512-byte sector buffer and HWSAD-write it
;                                to (track = linearSec/10, sector = linearSec%10+1)
;                                of the selected record; then ACK the 4-byte header.
;   'F'  finalize             -> reply "D" (done) if the received-count == 1600
;                                (size-only, the bdos_validate_disk_record contract:
;                                a record is exactly 1600 sectors = 819200 bytes),
;                                else "E"; then RET to trinload (clean, re-pushable).
;   plus ARP-request and ICMP-echo replies (so the host can reach us), ported from
;   trinload's return_eth / return_arp / return_ip + the RFC-1071 checksum.
;
; WHY THIS IS THE RIGHT SHAPE (CLAUDE.md rule 8 — the SAM/Colin code is the Trinity
; authority): the working, hardware-proven path to write a Trinity SD record is the
; B-DOS HWSAD hook a real BASIC `SAVE` drives (RECORD n; SAVE) — captured in
; emulation by TestBASICSaveWritesRecordToSD, which boots Colin's forked ROM + real
; B-DOS 1.5t and confirms `SAVE` issues a CMD24 that lands in the SD model. This
; program issues that same hook (RST 8 / DEFB 149 with A=2), so on real hardware the
; ROM's own SD driver does the CMD24 — no reimplementation. The free-record list
; read (CMD17) is the only raw-SPI step, and it is read-only (it never writes).
;
; VERIFICATION (emulation-first, CLAUDE.md rule 7):
;   * Faithful (real B-DOS): sd_push_faithful_test.go boots Colin's ROM + real
;     B-DOS, loads this program, injects the cj.mgt stream over the ENC model, and
;     asserts the pushed bytes land in the SD model (sdcard.go) at the chosen free
;     record's absolute LBA range — and ONLY there (data safety). The HWSAD RST 8
;     dispatches to the REAL ROM handler, which issues the real CMD24 to sdcard.go.
;     Gated on the proprietary captures (SKIP_PRIVATE_TESTS).
;   * Logic (CI, no captures): sd_push_test.go runs this same binary under the flat
;     harness with AttachBDOS intercepting the HWSAD/HRECORD dispatch and AttachSD
;     serving the free-record CMD17 list read, asserting the receive -> free-pick ->
;     per-sector HWSAD dispatch -> ACK logic deterministically.
; Emulation-verified is not hardware-verified (CLAUDE.md §5): the final real-Trinity
; run is the remaining gate.
;
; ONE BUILD, FLAG-FREE (no NETBOOT_HOSTTEST carve-out, i231b): this file contains no
; `if defined(NETBOOT_HOSTTEST)` directive. Both tests load the SAME .bin; the flat
; harness intercepts the RST 8 hooks rather than excluding them from the build (the
; carve-out anti-pattern). NETBOOT_REAL_LISTREAD selects the real CMD17 list read in
; bdos_seam.asm/sd_csd.asm (the boot images' path).

                org     &8000

HMPR:       equ &fb                     ; High Memory Page Register

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
                include "bdos_seam.asm"        ; bdos_find_free_record / bdos_select_record / bdos_write_record
                include "sd_csd.asm"           ; csd_set_bd_records (BD_RECORDS + csd_base) / bd_list_read_hw

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

                ; --- read the inserted card's CSD -> BD_RECORDS + csd_base ---------
                ; csd_set_bd_records (sd_csd.asm) runs the bounded init ladder + CMD9,
                ; decodes the record count, and stores csd_base (the first data
                ; sector). On a read failure BD_RECORDS stays 0 -> "no free record".
                call    csd_set_bd_records

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

                ; --- HRECORD-select the free record (the FIRST B-DOS hook call) ----
                ; HRECORD sets B-DOS's PERSISTENT record state (the record.no/record.t
                ; DVARs and, via record-selection, the self-modifying seek-base immediates
                ; at &A185/&A188 in the resident B-DOS page). That state crosses the rst 8
                ; boundary by construction, so every subsequent per-sector HWSAD seeks
                ; from this record's base — no per-write re-select needed.
                ld      a, l                   ; A = free record number (low byte; records << 256)
                call    bdos_select_record

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
                cp      "F"                    ; finalize?
                jp      nz, sp_serve_loop
                jp      sp_finalize

; ---------------------------------------------------------------------------
; sp_data_block — a '@' block: payload = [linearSec LE16][<=512 data bytes].
; Copy the data into the 512-byte sector buffer, write it to the selected record
; via the HWSAD hook, count it, then ACK the 4-byte header.
; ---------------------------------------------------------------------------
sp_data_block:
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

                ; THE .mgt -> sector MAP (the single, switchable place, D3): the host
                ; sends a 0-based LINEAR SECTOR; the shipping track-major convention
                ; is track = linearSec/10, sector = linearSec%10+1, which is exactly
                ; what bdos_write_record decodes. We do NOT assume the side-major
                ; (side*800 + 10*track + sector) layout; if a future card needs it,
                ; the host changes the linearSec it sends here — this Z80 side stays
                ; the plain track-major decode. linearSec arrives at packet+43 (LE16).
                ld      hl, (packet+43)        ; linearSec (LE16)
                ld      (BD_WRITE_START), hl
                ld      hl, 1
                ld      (BD_WRITE_COUNT), hl   ; one sector per block

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
                ; write the sector via the HWSAD hook (A=2 inside bdos_write_sector).
                ; bdos_write_record decodes BD_WRITE_START -> track/sector and calls
                ; bdos_write_sector once (BD_WRITE_COUNT == 1). On real B-DOS the HWSAD
                ; RST 8 routes through the ROM's own SD driver, which issues the CMD24 —
                ; we do NOT reimplement the write (CLAUDE.md rule 8: the SAM/Colin code
                ; is the Trinity authority).
                ;
                ; The write lands in the HRECORD-selected record's LBA range: HRECORD poked
                ; the seek-base immediates (&A185/&A188) for this record, and HWSAD's seek
                ; reads those immediates + the per-access linear sector to form the CMD24
                ; argument — so sector i of record n lands at csd_base + 1600*(n-1) + i.
                ; (Faithfully verified in sd_push_faithful_test.go against Colin's real
                ; B-DOS 1.5t: every sector lands in the chosen record's range and nowhere
                ; else.) Emulation-verified is not hardware-verified (CLAUDE.md §5).
                call    bdos_write_record

                ; count the received sector (finalize checks the total == 1600).
                ld      hl, (sp_recv_count)
                inc     hl
                ld      (sp_recv_count), hl

                ; NO ENC re-arm after the HWSAD write. The write is an SD transaction, but
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
                ; clean exit: RET to trinload's start (it pushed start as our return).
                ret

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
sp_recv_count:  defw 0                         ; sectors received this session

packet:         defs 1518                      ; RX/TX frame buffer (drv_read fills it)

length:         equ $ - sd_push_main           ; code length (diagnostic)
