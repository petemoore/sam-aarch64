; build_udp_frame.asm — originate a fresh UDP/IPv4/Ethernet frame from scratch.
;
; This is the SAM-side Z80 port of the netboot-oracle Go authority
; tools/netboot-oracle/frame/frame.go::BuildUDPFrame — the "fresh-frame
; primitive" (netboot impl plan §5.1) that trinload lacks: every trinload send
; is a reply-by-address-swap (plan §0), so a client that originates an RRQ (or a
; DHCP responder that broadcasts an OFFER) needs this routine to write a whole
; frame with no incoming packet to swap from.
;
; The IP-header checksum is the trinload chksum_blk idiom (RFC 1071 one's-
; complement word sum, folded, then inverted), ported from
; ~/git/trinload/trinload.asm:chksum_blk. The UDP checksum is left zero — legal
; for IPv4 (RFC 768), as trinload's checksum_udp does.
;
; Field offsets are the §1.2 offset contract, identical to the Go `frame`
; package's Off* constants and to trinload's `packet` buffer layout.
;
; PROVENANCE: the framing layout + checksum idiom are from simonowen/trinload
; (Simon Owen, BSD-style "do what you like" licence per its ReadMe.txt). The
; fresh-frame composition is new (the Go BuildUDPFrame authority).
;
; VERIFICATION: host-verifiable. tools/netboot-oracle/z80 assembles this file,
; runs it under koron-go/z80 with the captured frame's fields as input, and
; asserts the emitted frame matches the golden vector byte-for-byte (the same
; check TestBuildUDPFrameRoundTrips applies to the Go authority). The wire
; transmission itself (ENC28J60 I/O) is NOT host-verifiable — that is gated on
; i80 / real Trinity and lives elsewhere.

                ; org only when assembled standalone (the host harness builds
                ; this file on its own with -D NETBOOT_STANDALONE=1); when a
                ; state-machine file `include`s it, that file supplies the org.
                if defined(NETBOOT_STANDALONE)
                org     &8000
                endif

; ---------------------------------------------------------------------------
; Frame field offsets (the §1.2 offset contract — mirror frame/frame.go).
; ---------------------------------------------------------------------------
OFF_DST_MAC:      equ 0
OFF_SRC_MAC:      equ 6
OFF_ETHERTYPE:    equ 12
OFF_IP_VERIHL:    equ 14
OFF_IP_TOTALLEN:  equ 16
OFF_IP_TTL:       equ 22
OFF_IP_PROTO:     equ 23
OFF_IP_CHECKSUM:  equ 24
OFF_IP_SRC:       equ 26
OFF_IP_DST:       equ 30
OFF_UDP_SRCPORT:  equ 34
OFF_UDP_DSTPORT:  equ 36
OFF_UDP_LEN:      equ 38
OFF_UDP_CHECKSUM: equ 40
OFF_UDP_PAYLOAD:  equ 42

ETHERTYPE_IPV4:   equ &0800
IP_VER_IHL:       equ &45               ; version 4, IHL 5 (20-byte header)
IP_TTL:           equ 64
PROTO_UDP:        equ &11
IP_HEADER_LEN:    equ 20
UDP_HEADER_LEN:   equ 8

; ---------------------------------------------------------------------------
; Entry: build_udp_frame
;
; Inputs (a parameter block at PARAMS, filled by the caller / harness):
;   PARAM_DST_MAC   6 bytes  destination MAC
;   PARAM_SRC_MAC   6 bytes  source MAC (the SAM's own, sam_mac on hardware)
;   PARAM_SRC_IP    4 bytes  source IP   (sam_ip on hardware)
;   PARAM_DST_IP    4 bytes  destination IP
;   PARAM_SRC_PORT  2 bytes  UDP source port (big-endian)
;   PARAM_DST_PORT  2 bytes  UDP dest port   (big-endian)
;   PARAM_PAYLOAD_PTR 2 bytes pointer to the UDP payload
;   PARAM_PAYLOAD_LEN 2 bytes payload length (bytes)
;
; Output:
;   The complete frame is written to PACKET (the `packet` buffer).
;   BC = total frame length (HeaderLen + payload), ready for drv_write.
; ---------------------------------------------------------------------------
build_udp_frame:
                ; --- Ethernet header ---------------------------------------
                ; dst MAC (6 bytes) <- PARAM_DST_MAC
                ld      hl, PARAM_DST_MAC
                ld      de, PACKET + OFF_DST_MAC
                ld      bc, 6
                ldir
                ; src MAC (6 bytes) <- PARAM_SRC_MAC
                ld      hl, PARAM_SRC_MAC
                ld      de, PACKET + OFF_SRC_MAC
                ld      bc, 6
                ldir
                ; EtherType = &0800 (big-endian on the wire)
                ld      a, ETHERTYPE_IPV4 >> 8
                ld      (PACKET + OFF_ETHERTYPE), a
                ld      a, ETHERTYPE_IPV4 & &ff
                ld      (PACKET + OFF_ETHERTYPE + 1), a

                ; --- IPv4 header (no options, IHL=5) -----------------------
                ld      a, IP_VER_IHL
                ld      (PACKET + OFF_IP_VERIHL), a
                xor     a
                ld      (PACKET + OFF_IP_VERIHL + 1), a   ; DSCP/ECN = 0
                ; identification (16-17) + flags/frag (20-21) = 0
                ld      (PACKET + 18), a
                ld      (PACKET + 19), a
                ld      (PACKET + 20), a
                ld      (PACKET + 21), a
                ; TTL
                ld      a, IP_TTL
                ld      (PACKET + OFF_IP_TTL), a
                ; protocol = UDP
                ld      a, PROTO_UDP
                ld      (PACKET + OFF_IP_PROTO), a
                ; IP checksum field cleared (filled after the header is laid out)
                xor     a
                ld      (PACKET + OFF_IP_CHECKSUM), a
                ld      (PACKET + OFF_IP_CHECKSUM + 1), a

                ; IP total length = 20 + 8 + payloadLen (big-endian)
                ; HL = IP_HEADER_LEN + UDP_HEADER_LEN + payloadLen
                ld      hl, (PARAM_PAYLOAD_LEN)
                ld      bc, IP_HEADER_LEN + UDP_HEADER_LEN
                add     hl, bc
                ld      a, h
                ld      (PACKET + OFF_IP_TOTALLEN), a      ; big-endian high
                ld      a, l
                ld      (PACKET + OFF_IP_TOTALLEN + 1), a  ; big-endian low

                ; IP src (4 bytes) <- PARAM_SRC_IP
                ld      hl, PARAM_SRC_IP
                ld      de, PACKET + OFF_IP_SRC
                ld      bc, 4
                ldir
                ; IP dst (4 bytes) <- PARAM_DST_IP
                ld      hl, PARAM_DST_IP
                ld      de, PACKET + OFF_IP_DST
                ld      bc, 4
                ldir

                ; IP header checksum over the 20-byte header (RFC 1071).
                ; IHL=5 -> 10 words. Ported from trinload chksum_blk.
                ld      ix, PACKET + OFF_IP_VERIHL
                ld      bc, IP_HEADER_LEN / 2              ; 10 words
                call    ip_checksum
                ld      a, h
                ld      (PACKET + OFF_IP_CHECKSUM), a      ; big-endian
                ld      a, l
                ld      (PACKET + OFF_IP_CHECKSUM + 1), a

                ; --- UDP header --------------------------------------------
                ; src port (big-endian) <- PARAM_SRC_PORT (already big-endian)
                ld      hl, PARAM_SRC_PORT
                ld      de, PACKET + OFF_UDP_SRCPORT
                ld      bc, 2
                ldir
                ; dst port (big-endian) <- PARAM_DST_PORT
                ld      hl, PARAM_DST_PORT
                ld      de, PACKET + OFF_UDP_DSTPORT
                ld      bc, 2
                ldir
                ; UDP length = 8 + payloadLen (big-endian)
                ld      hl, (PARAM_PAYLOAD_LEN)
                ld      bc, UDP_HEADER_LEN
                add     hl, bc
                ld      a, h
                ld      (PACKET + OFF_UDP_LEN), a
                ld      a, l
                ld      (PACKET + OFF_UDP_LEN + 1), a
                ; UDP checksum = 0 (legal for IPv4, RFC 768)
                xor     a
                ld      (PACKET + OFF_UDP_CHECKSUM), a
                ld      (PACKET + OFF_UDP_CHECKSUM + 1), a

                ; --- Payload -----------------------------------------------
                ; copy PARAM_PAYLOAD_LEN bytes from PARAM_PAYLOAD_PTR
                ; to PACKET+OFF_UDP_PAYLOAD.
                ld      hl, (PARAM_PAYLOAD_PTR)
                ld      de, PACKET + OFF_UDP_PAYLOAD
                ld      bc, (PARAM_PAYLOAD_LEN)
                ld      a, b
                or      c
                jr      z, no_payload
                ldir
no_payload:
                ; --- Return total frame length in BC -----------------------
                ; BC = OFF_UDP_PAYLOAD + payloadLen
                ld      hl, (PARAM_PAYLOAD_LEN)
                ld      bc, OFF_UDP_PAYLOAD
                add     hl, bc
                ld      b, h
                ld      c, l
                ret

; ---------------------------------------------------------------------------
; ip_checksum — RFC 1071 one's-complement header checksum.
;
; Faithful port of trinload chksum_blk (~/git/trinload/trinload.asm): sum
; big-endian words with carry, fold the final carry once, then invert.
;
; In:  IX = start of the block, BC = block length in WORDS.
; Out: HL = the inverted checksum (store big-endian: H then L).
; Clobbers: A, BC, DE, HL, IX.
; ---------------------------------------------------------------------------
ip_checksum:
                ld      hl, 0                  ; checksum accumulator
                ld      a, c                   ; swap byte order to invert loops
                ld      c, b
                ld      b, a
                inc     c                      ; MSB count needs +1
                and     a                      ; clear carry for the first ADC
ipck_loop:
                ld      d, (ix)                ; big-endian word
                ld      e, (ix+1)
                adc     hl, de
                inc     ix
                inc     ix
                djnz    ipck_loop
                dec     c
                jr      nz, ipck_loop
                jr      nc, ipck_end
                inc     hl                     ; add the final carry
ipck_end:
                ld      a, h
                cpl
                ld      h, a
                ld      a, l
                cpl
                ld      l, a
                ret

; ===========================================================================
; Data region — the parameter block and the output `packet` buffer.
;
; On real hardware the packet buffer is trinload's `packet: defs 1518`
; (trinload.asm:419) and the source MAC/IP come from the EEPROM "Trinity
; Network " chunk (sam_mac/sam_ip). Here they are caller-supplied so the host
; harness can drive arbitrary inputs and compare the output against the
; captured golden frame.
; ===========================================================================

                ; The parameter block. The harness writes inputs here before
                ; calling build_udp_frame. Labels are exported via the pyz80
                ; symbol file so the harness addresses them by name.
PARAMS:
PARAM_DST_MAC:    defs 6
PARAM_SRC_MAC:    defs 6
PARAM_SRC_IP:     defs 4
PARAM_DST_IP:     defs 4
PARAM_SRC_PORT:   defs 2                 ; big-endian on the wire
PARAM_DST_PORT:   defs 2                 ; big-endian on the wire
PARAM_PAYLOAD_PTR: defs 2
PARAM_PAYLOAD_LEN: defs 2

PACKET:           defs 1518              ; the output frame buffer
