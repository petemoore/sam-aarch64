; netglue.asm — the shared UDP-reply wire glue for trinload-pushed serving
; programs (sd_push, list_records), ported from trinload.asm (simonowen/trinload
; @ a4b7af7): ack_len / set_udp_data_len / ip_to_eth_len / return_eth /
; return_arp / return_ip / return_udp + the RFC-1071 checksum. These are the
; "little network bit-pushing": they swap MAC/IP/port fields in place and
; recompute the IP/ICMP checksums for a reply built from the received frame.
;
; CONTRACT: the including file defines (before or after this include — labels
; forward-resolve across pyz80's two passes):
;   packet   — the RX/TX frame buffer (defs 1518)
;   sam_mac  — 6-byte SAM MAC   (typically equ chunk+0, the EEPROM net chunk)
;   sam_ip   — 4-byte SAM IP    (typically equ chunk+6)
;   drv_write — the ENC28J60 TX entry (encdrv.asm)
; One shared copy: a checksum/swap fix lands in every pushable at once instead
; of drifting per program.


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
