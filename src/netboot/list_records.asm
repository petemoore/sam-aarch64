; list_records.asm — the small trinload-pushable "list the Trinity SD record
; inventory" program (i322). The READ-ONLY counterpart to sd_push (i293, push a
; disk into a record), boot_record (i316) and delete_record (i317): a host asks
; over the wire and this program serves the on-card record LIST back, sector by
; sector, so an agent can re-discover what is on the card remotely — e.g. to keep
; re-writing an iterated disk image to the SAME record — without physical access.
; It completes the store/boot/delete/LIST testing toolkit.
;
; THE WHOLE JOB: read the SAM's MAC/IP from the "Trinity Network " EEPROM chunk
; (eeprom.asm), init the ENC28J60 (encdrv.asm), read the inserted card's CSD to
; learn its record count (sd_csd.asm csd_set_bd_records), then serve UDP port
; 0xEDB0 with a tiny framing (modelled on sd_push's):
;   '?'  discovery          -> reply "!" + BD_RECORDS (LE16) — the host learns the
;                              card's record count (0 = CSD unreadable) up front.
;   'L'  list-sector query  -> [listSec LE16, 1-based]; reply "R" + listSec (LE16)
;                              + the RAW 512-byte list sector (32 × 16-byte name
;                              entries — the host decodes free/used/write-protect,
;                              docs/specs/trinity-record-detection-design.md §4).
;                              Out-of-range listSec or a CMD17 read failure ->
;                              reply "E" + listSec (LE16), nothing read.
;   'Q'  quit               -> reply "q", then RET to trinload (clean,
;                              re-pushable — the host can chain the next tool).
;   plus ARP-request and ICMP-echo replies (netglue.asm) so the host can reach us.
;
; DATA-SAFETY (trinity_storage_shared_resource): this program is structurally
; READ-ONLY — it is built WITHOUT NETBOOT_WANT_CLAIM, so the list-WRITE primitives
; (bdos_write_list_sector / claim / free) are not even assembled into the binary.
; The only SD commands it can issue are the CSD read (CMD9 + init ladder) and the
; CMD17 single-block list read.
;
; VERIFICATION (emulation-first, CLAUDE.md rule 7): list_records_test.go boots this
; binary under the flat harness with the ENC + SD-SPI models attached, seeds the
; card's list sectors with named records, drives the '?'/'L'/'Q' protocol over the
; ENC model, and asserts the exact reply payloads AND that the SD model saw ZERO
; writes. The real-Trinity run stays hardware-gated (CLAUDE.md §5). ONE BUILD,
; FLAG-FREE of carve-outs (i231): no `if defined(NETBOOT_HOSTTEST)` here;
; NETBOOT_REAL_LISTREAD selects the real CMD17 list read (the boot images' path).

                org     &8000

HMPR:       equ &fb                     ; High Memory Page Register

                ; Entry: trinload's X packet does `out (HMPR),P; jp &8000`, landing
                ; here. The host harness runs list_records_main by symbol; this jp
                ; is the hardware/trinload entry.
                jp      list_records_main

; ===========================================================================
; Composed includes — the real wire driver, the EEPROM reader, the SD CSD/list
; read seam, and the shared reply plumbing. They come FIRST (right after the
; boot jp) so every symbol they define is resolved for the code below (pyz80 is
; single-symbol-table, no forward `equ`-of-a-later-label). NETBOOT_REAL_LISTREAD
; selects the real CMD17 list read (sd_csd.asm bd_list_read_hw); NETBOOT_WANT_CLAIM
; is deliberately ABSENT (read-only build, see the data-safety note above).
; ===========================================================================
                include "encdrv.asm"          ; drv_init / drv_read / drv_write
                include "eeprom.asm"           ; find_index / read_chunk + value/chunk/name/part/total
                include "bdos_seam.asm"        ; bdos_read_list_sector / BD_LIST_SECTOR / BD_LIST_BUF
                include "sd_csd.asm"           ; csd_set_bd_records (BD_RECORDS) / bd_list_read_hw

; The SAM MAC/IP live in the EEPROM reader's `chunk` buffer (eeprom.asm): the
; "Trinity Network " chunk is read into `chunk`, MAC at +0, IP at +6.
sam_mac:        equ chunk+0                    ; SAM MAC, from the loaded flash chunk
sam_ip:         equ chunk+6                    ; SAM IP

; Network wire glue — the shared trinload-idiom reply plumbing (ack_len /
; return_* / checksums). Contract: packet / sam_mac / sam_ip / drv_write.
                include "netglue.asm"

; ===========================================================================
; list_records_main — the bootable / trinload entry.
; ===========================================================================
list_records_main:
                di
                ; Clear the lower screen + home the print position (CLSLOWER, stock
                ; ROM &06B5) BEFORE any RST &10 print — a CR printed at the screen
                ; bottom sends the ROM into its scroll key-wait prompt, wedging an
                ; unattended tool (the i319a sd_push hardware wedge; see
                ; rom_print_scroll_test.go). CLS resets the scroll count first.
                call    &06b5
                ld      hl, lr_str_banner      ; CR + "SD-LIST " — announce the tool;
                call    lr_print_str           ; the stage markers follow on the line

                ; --- locate + read the "Trinity Network " EEPROM chunk -> MAC/IP --
                ld      a, 1
                ld      (part), a
                ld      (total), a
                ld      hl, lr_chunk_name
                ld      de, name
                ld      bc, 16
                ldir
                call    find_index
                ld      a, (value)
                and     a
                jp      z, lr_fail_cfg
                call    read_chunk
                ld      a, (value)
                and     a
                jp      z, lr_fail_cfg

                ; both MAC and IP zero? -> missing settings.
                ld      a, (sam_mac+0)
                ld      b, a
                ld      a, (sam_ip+0)
                or      b
                jp      z, lr_fail_cfg
                ld      a, "1"                 ; DBG: EEPROM net-config (MAC/IP) OK
                call    dbg_char

                ; --- init the ENC28J60 FIRST, before any SD work (i242) -----------
                ; drv_init's chk_trinity identity probe uses fixed delays against a
                ; quiescent microcontroller; the heavy &38 SD init leaves the shared
                ; PIC settling — so ENC first, SD second (the i242 finding).
                ld      hl, sam_mac
                call    drv_init
                ld      a, b
                or      c
                jp      z, lr_fail_init
                ld      a, "2"                 ; DBG: drv_init (ENC28J60) OK
                call    dbg_char

                ; --- read the inserted card's CSD -> BD_RECORDS ---------------------
                ; On a read failure BD_RECORDS stays 0: we still SERVE (the host sees
                ; "!"+0 and reports the unreadable card remotely) — better than a
                ; local-only failure hold for a remote-query tool.
                call    csd_set_bd_records
                ld      a, "3"                 ; DBG: CSD read done (CMD9)
                call    dbg_char

                ; --- number of list sectors = ceil(BD_RECORDS / 32) ----------------
                ; The card's record-list entries live 32-per-sector in card-absolute
                ; sectors 1..N; entries past BD_RECORDS are padding the host ignores.
                ; Computed as (BD_RECORDS >> 5) + (1 if BD_RECORDS mod 32 != 0) — NOT
                ; the usual (n+31)>>5, which OVERFLOWS 16 bits for BD_RECORDS >= 65505.
                ; That is not theoretical: B-DOS 1.5t's >51GB clamp makes a 64GB card
                ; (Pete's real card) decode to records = 65535 EXACTLY, and the +31
                ; form wrapped nlist to 0 there, refusing every query (found on real
                ; hardware, i319a; pinned by TestListRecords64GBCard).
                ld      hl, (BD_RECORDS)
                ld      a, l
                and     31                     ; the mod-32 remainder
                ld      c, a                   ; C != 0 -> a partial last list sector
                srl     h
                rr      l                      ; /2
                srl     h
                rr      l                      ; /4
                srl     h
                rr      l                      ; /8
                srl     h
                rr      l                      ; /16
                srl     h
                rr      l                      ; /32
                ld      a, c
                or      a
                jr      z, lr_nlist_whole
                inc     hl                     ; round the partial sector up
lr_nlist_whole:
                ; The list-read seam addresses sectors with ONE byte (BD_LIST_SECTOR,
                ; bdos_seam.asm), so this tool serves list sectors 1..255 (records
                ; 1..8160). Clamp nlist so a bigger card's higher sectors are REFUSED
                ; ('E') by the range check rather than silently truncated to a wrong
                ; low sector by the 8-bit store in lr_list_query. Widening the seam
                ; to 16-bit is tracked separately (the whole toolkit shares the limit).
                ld      a, h
                or      a
                jr      z, lr_nlist_ok
                ld      hl, 255
lr_nlist_ok:
                ld      (lr_nlist), hl
                ld      a, "4"                 ; DBG: serving (inventory ready)
                call    dbg_char

lr_serve_loop:
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
                jr      z, lr_serve_loop

                ; --- frame type: IPv4 or ARP? -----------------------------------
                ld      a, (packet+12)         ; Ethernet frame type MSB
                cp      &08
                jr      nz, lr_serve_loop

                ld      a, (packet+13)
                cp      &06                    ; ARP?
                jr      nz, lr_try_ipv4

                ; ARP request for our IP? -> reply.
                ld      a, (packet+21)
                cp      &01                    ; request?
                jr      nz, lr_serve_loop
                inc     a                      ; -> reply (2)
                ld      (packet+21), a
                ld      hl, (packet+38)        ; requested IP (first 2 bytes)
                ld      de, (sam_ip)
                and     a
                sbc     hl, de
                jr      nz, lr_serve_loop      ; not for us
                ld      hl, (packet+40)        ; requested IP (second 2 bytes)
                ld      de, (sam_ip+2)
                and     a
                sbc     hl, de
                jr      nz, lr_serve_loop      ; not for us
                call    return_eth
                call    return_arp
                ld      hl, packet
                ld      bc, 42
                call    drv_write
                jr      lr_serve_loop

lr_try_ipv4:
                and     a                      ; IPv4? (type LSB == 0)
                jr      nz, lr_serve_loop
                ld      a, (packet+20)         ; IP flags
                and     %00100000              ; fragmented?
                jr      nz, lr_serve_loop      ; we don't reassemble

                ld      a, (packet+23)
                cp      &01                    ; ICMP?
                jr      nz, lr_try_udp

                ld      a, (packet+34)
                cp      &08                    ; echo request?
                jr      nz, lr_serve_loop
                xor     a
                ld      (packet+34), a         ; -> echo reply
                call    return_eth
                call    return_ip
                call    checksum_ip
                call    checksum_icmp
                ld      hl, packet
                call    ip_to_eth_len
                call    drv_write
                jp      lr_serve_loop

lr_try_udp:
                cp      &11                    ; UDP?
                jp      nz, lr_serve_loop
                ld      de, (packet+36)        ; destination port
                ld      hl, &b0ed              ; port 0xEDB0
                and     a
                sbc     hl, de
                jp      nz, lr_serve_loop      ; not for us

                ; unicast to our IP? (the query messages are unicast)
                ld      hl, (packet+30)        ; target IP (first 2 bytes)
                ld      de, (sam_ip)
                and     a
                sbc     hl, de
                jp      nz, lr_serve_loop
                ld      hl, (packet+32)        ; target IP (second 2 bytes)
                ld      de, (sam_ip+2)
                and     a
                sbc     hl, de
                jp      nz, lr_serve_loop

                ; dispatch on the first UDP data byte (packet+42).
                ld      a, (packet+42)
                cp      "?"                    ; discovery?
                jr      nz, lr_not_disc
                ; reply "!" + BD_RECORDS (LE16): the record count rides discovery so
                ; the host knows how many list sectors to query (0 = CSD unreadable).
                ld      a, "!"
                ld      (packet+42), a
                ld      hl, (BD_RECORDS)
                ld      (packet+43), hl        ; LE16 after the status byte
                ld      bc, 3
                call    ack_len
                jp      lr_serve_loop

lr_not_disc:
                cp      "L"                    ; list-sector query?
                jr      nz, lr_not_list
                jp      lr_list_query
lr_not_list:
                cp      "Q"                    ; quit?
                jp      nz, lr_serve_loop
                ; ack the quit, then RET to trinload (clean, re-pushable) so the
                ; host can chain the next tool without touching the SAM.
                ld      a, "q"
                ld      (packet+42), a
                ld      bc, 1
                call    ack_len
                ld      hl, lr_str_bye
                call    lr_print_str           ; CR + "DONE" + CR
                ret                            ; -> trinload's start

; ---------------------------------------------------------------------------
; lr_list_query — an 'L' message: payload = [listSec LE16, 1-based]. Range-check
; it (1..lr_nlist — so a bad index can never address a stray LBA), read that list
; sector via the real CMD17 (bdos_read_list_sector), and reply "R" + listSec +
; the raw 512 bytes. Out-of-range or a CY read failure replies "E" + listSec.
; ---------------------------------------------------------------------------
lr_list_query:
                ld      hl, (packet+43)        ; listSec (LE16), echoed in the reply
                ld      a, h
                or      l
                jr      z, lr_list_bad         ; sector 0: the boot sector, not list
                ld      de, (lr_nlist)
                ld      a, e                   ; DE - HL: borrow => HL > DE
                sub     l
                ld      a, d
                sbc     a, h
                jr      c, lr_list_bad         ; beyond the card's list: refuse
                ld      a, l                   ; 1..151 fits the 1-byte seam input
                ld      (BD_LIST_SECTOR), a
                call    bdos_read_list_sector  ; real CMD17 -> BD_LIST_BUF; CY on fail
                jr      c, lr_list_bad
                ld      a, "R"
                ld      (packet+42), a         ; listSec at +43/+44 is untouched
                ld      hl, BD_LIST_BUF
                ld      de, packet+45
                ld      bc, 512
                ldir                           ; the raw sector rides after the header
                ld      bc, 3+512
                call    ack_len
                jp      lr_serve_loop
lr_list_bad:
                ld      a, "E"
                ld      (packet+42), a         ; echo the rejected listSec at +43/+44
                ld      bc, 3
                call    ack_len
                jp      lr_serve_loop

; ---------------------------------------------------------------------------
; dbg_char / lr_print_str — the RST &10 print idiom (trinload/sd_push): dbg_char
; prints A preserving EVERY register; lr_print_str prints the NUL-terminated
; string at HL (clobbers A, HL).
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

lr_print_str:
                ld      a, (hl)
                or      a
                ret     z
                call    dbg_char
                inc     hl
                jr      lr_print_str

; ---------------------------------------------------------------------------
; Failure paths — print the reason, show a diagnostic border, hold until Esc,
; then RET to trinload (never a raw di;halt, which would strand the SAM and
; cost a power-cycle). Only the no-network cases land here; an unreadable CSD
; still serves (the host sees "!"+0).
; ---------------------------------------------------------------------------
lr_fail_cfg:
                ld      hl, lr_str_fail_cfg
                ld      a, 2                   ; red: no/bad network settings
                jr      lr_fail_show
lr_fail_init:
                ld      hl, lr_str_fail_enc
                ld      a, 1                   ; blue: ENC28J60 init failed
lr_fail_show:
                out     (&fe), a
                call    lr_print_str           ; the reason, next to the border colour
lr_fail_wait:
                ld      a, &f7
                in      a, (&f9)
                bit     5, a                   ; Esc?
                jr      nz, lr_fail_wait       ; hold the border until Esc
                ret                            ; -> trinload's start

; ===========================================================================
; Data.
; ===========================================================================
lr_chunk_name:  defm "Trinity Network "       ; the EEPROM chunk holding MAC+IP

lr_nlist:       defw 0                         ; list sectors = ceil(BD_RECORDS/32)

; Screen-status strings — NUL-terminated for lr_print_str; 13 = CR.
lr_str_banner:  defb 13
                defm "SD-LIST "
                defb 0
lr_str_bye:     defb 13
                defm "DONE"
                defb 13, 0
lr_str_fail_cfg:
                defb 13
                defm "NO NET CONFIG"
                defb 13, 0
lr_str_fail_enc:
                defb 13
                defm "ENC INIT FAIL"
                defb 13, 0

packet:         defs 1518                      ; RX/TX frame buffer (drv_read fills it)

length:         equ $ - list_records_main      ; code length (diagnostic)
