; build_tcp_segment.asm — originate a fresh TCP/IPv4/Ethernet segment from scratch.
;
; This is the SAM-side Z80 port of the netboot-oracle Go authority
; tools/netboot-oracle/tcp/tcp.go::BuildSegment — the TCP analogue of the
; build_udp_frame fresh-frame primitive. The i70 firmware self-provisioning HTTP
; client rides on TCP, which the trinload/UDP stack does not provide; this routine
; builds the one new transport.
;
; What is new vs build_udp_frame:
;   * the L4 header is the 20-byte TCP header (ports, seq, ack, data-offset+flags,
;     window, checksum, urgent), not the 8-byte UDP header;
;   * the TCP checksum is MANDATORY (RFC 793) and is computed over an IPv4
;     pseudo-header (src IP, dst IP, 0, proto=6, TCP length) prepended to the TCP
;     header + data. UDP could legally zero its checksum for IPv4 (RFC 768); TCP
;     cannot. This is the only arithmetic difference from the UDP path.
;
; The Ethernet/IPv4 header layout + the IP-header checksum are the same
; trinload-derived idiom as build_udp_frame (the §1.2 offset contract).
;
; PROVENANCE: framing layout + the one's-complement checksum idiom are from
; simonowen/trinload (Simon Owen, BSD-style "do what you like" licence per its
; ReadMe.txt). The TCP composition + pseudo-header checksum are new (the Go
; tcp.BuildSegment authority).
;
; VERIFICATION: host-verifiable. tools/netboot-oracle/z80 assembles this file,
; runs it under koron-go/z80 with the same inputs the Go authority gets, and
; asserts the emitted segment matches BuildSegment byte-for-byte. The wire
; transmission (ENC28J60 I/O) + a real TCP handshake are NOT host-verifiable —
; gated on i80 / real Trinity.

                if defined(NETBOOT_STANDALONE)
                org     &8000
                endif

; ---------------------------------------------------------------------------
; Frame field offsets (the §1.2 offset contract — mirror frame/frame.go and
; tcp/tcp.go). The TCP header starts at the same byte the UDP header does (34).
; ---------------------------------------------------------------------------
TCP_OFF_DST_MAC:     equ 0
TCP_OFF_SRC_MAC:     equ 6
TCP_OFF_ETHERTYPE:   equ 12
TCP_OFF_IP_VERIHL:   equ 14
TCP_OFF_IP_TOTALLEN: equ 16
TCP_OFF_IP_TTL:      equ 22
TCP_OFF_IP_PROTO:    equ 23
TCP_OFF_IP_CHECKSUM: equ 24
TCP_OFF_IP_SRC:      equ 26
TCP_OFF_IP_DST:      equ 30
; TCP header (relative to the frame start, L4 begins at 34).
TCP_OFF_L4:          equ 34
TCP_OFF_SRCPORT:     equ 34            ; L4+0
TCP_OFF_DSTPORT:     equ 36            ; L4+2
TCP_OFF_SEQ:         equ 38            ; L4+4
TCP_OFF_ACK:         equ 42            ; L4+8
TCP_OFF_DATAOFF:     equ 46            ; L4+12 (high nibble = words)
TCP_OFF_FLAGS:       equ 47            ; L4+13
TCP_OFF_WINDOW:      equ 48            ; L4+14
TCP_OFF_CHECKSUM:    equ 50            ; L4+16
TCP_OFF_URGENT:      equ 52            ; L4+18
TCP_OFF_PAYLOAD:     equ 54            ; L4+20

ETHERTYPE_IPV4_T:    equ &0800
IP_VER_IHL_T:        equ &45           ; version 4, IHL 5 (20-byte header)
IP_TTL_T:            equ 64
PROTO_TCP:           equ &06
IP_HEADER_LEN_T:     equ 20
TCP_HEADER_LEN:      equ 20
TCP_DATAOFF_VAL:     equ &50           ; data offset = 5 words << 4, reserved = 0

; ---------------------------------------------------------------------------
; Entry: build_tcp_segment
;
; Inputs (a parameter block at TCP_PARAMS, filled by the caller / harness):
;   TPARAM_DST_MAC    6 bytes  destination MAC
;   TPARAM_SRC_MAC    6 bytes  source MAC (sam_mac on hardware)
;   TPARAM_SRC_IP     4 bytes  source IP  (sam_ip on hardware)
;   TPARAM_DST_IP     4 bytes  destination IP
;   TPARAM_SRC_PORT   2 bytes  TCP source port (big-endian)
;   TPARAM_DST_PORT   2 bytes  TCP dest port   (big-endian)
;   TPARAM_SEQ        4 bytes  sequence number (big-endian)
;   TPARAM_ACK        4 bytes  ack number      (big-endian)
;   TPARAM_FLAGS      1 byte   control flags
;   TPARAM_WINDOW     2 bytes  window          (big-endian)
;   TPARAM_PAYLOAD_PTR 2 bytes pointer to the TCP payload
;   TPARAM_PAYLOAD_LEN 2 bytes payload length (bytes)
;
; Output:
;   The complete segment is written to TCP_PACKET (the `packet` buffer).
;   BC = total frame length (54 + payload), ready for drv_write.
; ---------------------------------------------------------------------------
build_tcp_segment:
                ; --- Ethernet header ---------------------------------------
                ld      hl, TPARAM_DST_MAC
                ld      de, TCP_PACKET + TCP_OFF_DST_MAC
                ld      bc, 6
                ldir
                ld      hl, TPARAM_SRC_MAC
                ld      de, TCP_PACKET + TCP_OFF_SRC_MAC
                ld      bc, 6
                ldir
                ld      a, ETHERTYPE_IPV4_T >> 8
                ld      (TCP_PACKET + TCP_OFF_ETHERTYPE), a
                ld      a, ETHERTYPE_IPV4_T & &ff
                ld      (TCP_PACKET + TCP_OFF_ETHERTYPE + 1), a

                ; --- IPv4 header (no options, IHL=5) -----------------------
                ld      a, IP_VER_IHL_T
                ld      (TCP_PACKET + TCP_OFF_IP_VERIHL), a
                xor     a
                ld      (TCP_PACKET + TCP_OFF_IP_VERIHL + 1), a   ; DSCP/ECN
                ; identification (16-17) + flags/frag (20-21) = 0
                ld      (TCP_PACKET + 18), a
                ld      (TCP_PACKET + 19), a
                ld      (TCP_PACKET + 20), a
                ld      (TCP_PACKET + 21), a
                ld      a, IP_TTL_T
                ld      (TCP_PACKET + TCP_OFF_IP_TTL), a
                ld      a, PROTO_TCP
                ld      (TCP_PACKET + TCP_OFF_IP_PROTO), a
                xor     a
                ld      (TCP_PACKET + TCP_OFF_IP_CHECKSUM), a
                ld      (TCP_PACKET + TCP_OFF_IP_CHECKSUM + 1), a

                ; IP total length = 20 + 20 + payloadLen (big-endian)
                ld      hl, (TPARAM_PAYLOAD_LEN)
                ld      bc, IP_HEADER_LEN_T + TCP_HEADER_LEN
                add     hl, bc
                ld      a, h
                ld      (TCP_PACKET + TCP_OFF_IP_TOTALLEN), a
                ld      a, l
                ld      (TCP_PACKET + TCP_OFF_IP_TOTALLEN + 1), a

                ; IP src/dst
                ld      hl, TPARAM_SRC_IP
                ld      de, TCP_PACKET + TCP_OFF_IP_SRC
                ld      bc, 4
                ldir
                ld      hl, TPARAM_DST_IP
                ld      de, TCP_PACKET + TCP_OFF_IP_DST
                ld      bc, 4
                ldir

                ; IP header checksum over the 20-byte header.
                ld      hl, 0                  ; seed sum = 0
                ld      ix, TCP_PACKET + TCP_OFF_IP_VERIHL
                ld      bc, IP_HEADER_LEN_T    ; 20 bytes
                call    tcp_sum_bytes
                call    tcp_fold_invert
                ld      a, h
                ld      (TCP_PACKET + TCP_OFF_IP_CHECKSUM), a
                ld      a, l
                ld      (TCP_PACKET + TCP_OFF_IP_CHECKSUM + 1), a

                ; --- TCP header --------------------------------------------
                ld      hl, TPARAM_SRC_PORT
                ld      de, TCP_PACKET + TCP_OFF_SRCPORT
                ld      bc, 2
                ldir
                ld      hl, TPARAM_DST_PORT
                ld      de, TCP_PACKET + TCP_OFF_DSTPORT
                ld      bc, 2
                ldir
                ld      hl, TPARAM_SEQ
                ld      de, TCP_PACKET + TCP_OFF_SEQ
                ld      bc, 4
                ldir
                ld      hl, TPARAM_ACK
                ld      de, TCP_PACKET + TCP_OFF_ACK
                ld      bc, 4
                ldir
                ld      a, TCP_DATAOFF_VAL
                ld      (TCP_PACKET + TCP_OFF_DATAOFF), a
                ld      a, (TPARAM_FLAGS)
                ld      (TCP_PACKET + TCP_OFF_FLAGS), a
                ld      hl, TPARAM_WINDOW
                ld      de, TCP_PACKET + TCP_OFF_WINDOW
                ld      bc, 2
                ldir
                ; checksum (16-17) + urgent (18-19) = 0 before the checksum calc
                xor     a
                ld      (TCP_PACKET + TCP_OFF_CHECKSUM), a
                ld      (TCP_PACKET + TCP_OFF_CHECKSUM + 1), a
                ld      (TCP_PACKET + TCP_OFF_URGENT), a
                ld      (TCP_PACKET + TCP_OFF_URGENT + 1), a

                ; --- Payload -----------------------------------------------
                ld      hl, (TPARAM_PAYLOAD_PTR)
                ld      de, TCP_PACKET + TCP_OFF_PAYLOAD
                ld      bc, (TPARAM_PAYLOAD_LEN)
                ld      a, b
                or      c
                jr      z, tcp_no_payload
                ldir
tcp_no_payload:

                ; --- TCP checksum ------------------------------------------
                ; Sum (one's-complement, RFC 793) over:
                ;   pseudo-header: srcIP(4) + dstIP(4) + (0<<8|proto) + tcpLen
                ;   TCP header + data: tcpLen bytes from L4.
                ; tcpLen = 20 + payloadLen.
                ld      hl, (TPARAM_PAYLOAD_LEN)
                ld      bc, TCP_HEADER_LEN
                add     hl, bc
                ld      (tcp_seg_len), hl      ; stash tcpLen

                ; (1) srcIP + dstIP — 8 contiguous bytes from OFF_IP_SRC.
                ld      hl, 0
                ld      ix, TCP_PACKET + TCP_OFF_IP_SRC
                ld      bc, 8
                call    tcp_sum_bytes
                ; (2) + (0<<8 | proto)
                ld      de, PROTO_TCP
                add     hl, de
                jr      nc, tcp_ck_proto
                inc     hl
tcp_ck_proto:
                ; (3) + tcpLen
                ld      de, (tcp_seg_len)
                add     hl, de
                jr      nc, tcp_ck_len
                inc     hl
tcp_ck_len:
                ; (4) + the TCP header + data (tcpLen bytes from L4)
                ld      ix, TCP_PACKET + TCP_OFF_L4
                ld      bc, (tcp_seg_len)
                call    tcp_sum_bytes
                call    tcp_fold_invert
                ld      a, h
                ld      (TCP_PACKET + TCP_OFF_CHECKSUM), a
                ld      a, l
                ld      (TCP_PACKET + TCP_OFF_CHECKSUM + 1), a

                ; --- Return total frame length in BC -----------------------
                ld      hl, (TPARAM_PAYLOAD_LEN)
                ld      bc, TCP_OFF_PAYLOAD
                add     hl, bc
                ld      b, h
                ld      c, l
                ret

; ---------------------------------------------------------------------------
; tcp_sum_bytes — accumulate the one's-complement word sum of a byte region
; into HL, folding each carry so HL stays 16-bit.
;
; Treats the region as big-endian 16-bit words. An ODD trailing byte is summed
; as the HIGH byte of a word padded with a zero low byte (RFC 1071).
;
; In:  IX = region start, BC = region length in BYTES, HL = running sum.
; Out: HL = updated running sum, IX advanced past the region.
; Clobbers: A, BC, DE, IX. (HL carried in/out.)
; ---------------------------------------------------------------------------
tcp_sum_bytes:
tsb_loop:
                ld      a, b
                or      c
                jr      z, tsb_done            ; 0 bytes left
                dec     bc                     ; tentatively consume 1
                ld      a, b
                or      c
                jr      z, tsb_odd             ; that was the last byte -> odd tail
                dec     bc                     ; consume the 2nd of the pair
                ld      d, (ix)                ; big-endian high
                ld      e, (ix+1)              ; low
                inc     ix
                inc     ix
                add     hl, de
                jr      nc, tsb_loop
                inc     hl                     ; fold carry
                jr      tsb_loop
tsb_odd:
                ld      d, (ix)                ; trailing byte = high, low = 0
                ld      e, 0
                inc     ix
                add     hl, de
                jr      nc, tsb_done
                inc     hl
tsb_done:
                ret

; ---------------------------------------------------------------------------
; tcp_fold_invert — one's-complement the 16-bit sum in HL. tcp_sum_bytes folds
; carries per-add, so no extra fold loop is needed here; this just inverts.
;
; In:  HL = folded sum.  Out: HL = ~sum (store big-endian: H then L).
; Clobbers: A.
; ---------------------------------------------------------------------------
tcp_fold_invert:
                ld      a, h
                cpl
                ld      h, a
                ld      a, l
                cpl
                ld      l, a
                ret

; ===========================================================================
; Data region — the parameter block, a scratch word, and the output buffer.
; ===========================================================================

TCP_PARAMS:
TPARAM_DST_MAC:    defs 6
TPARAM_SRC_MAC:    defs 6
TPARAM_SRC_IP:     defs 4
TPARAM_DST_IP:     defs 4
TPARAM_SRC_PORT:   defs 2                 ; big-endian on the wire
TPARAM_DST_PORT:   defs 2                 ; big-endian on the wire
TPARAM_SEQ:        defs 4                 ; big-endian on the wire
TPARAM_ACK:        defs 4                 ; big-endian on the wire
TPARAM_FLAGS:      defs 1
TPARAM_WINDOW:     defs 2                 ; big-endian on the wire
TPARAM_PAYLOAD_PTR: defs 2
TPARAM_PAYLOAD_LEN: defs 2

tcp_seg_len:       defs 2                 ; scratch: tcpLen (header+payload)

TCP_PACKET:        defs 1518              ; the output frame buffer
