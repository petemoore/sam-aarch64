; tftp_client_front.asm — the i82 TFTP client's request-origination front: the
; step that runs *before* the receive loop (tftp_client_loop.asm). The client
; broadcasts an ARP request to learn the netboot server's MAC, then sends its
; RRQ to that MAC; afterwards it enters tftp_recv_data to pull the file.
;
; This is the SAM-side Z80 port of the netboot-oracle Go authority
; tools/netboot-oracle/tftp/clientfront.go::ClientFront (+ frame.ParseARPReply).
; trinload has no fresh-frame origination — every send is a reply-by-address-
; swap (impl plan §0) — so the client builds these frames from scratch, reusing
; the host-verified primitives:
;
;   tftp_send_arp:
;     build_arp_request (broadcast "who has SERVER_IP?"), drv_write.
;     Out: BC = the transmitted frame length.
;   tftp_recv_arp:
;     drv_read a frame; is it an ARP reply (EtherType &0806, OPER=2) for
;     SERVER_IP? If so, learn the server MAC into SERVER_MAC, set GOT_MAC, return
;     BC=1. Any other frame (non-ARP, or an ARP reply for a different IP) returns
;     BC=0 — the caller keeps reading until a match arrives.
;   tftp_send_rrq:
;     build_rrq (octet mode, the settled ClientOptionSet) into CRBUF, wrap it as
;     a UDP datagram (our IP + our TID -> the learned server MAC + IP, port 69),
;     drv_write. Out: BC = the transmitted frame length.
;
; The ARP request, the learn-MAC step, and the RRQ frame mirror ClientFront
; step for step; the frames on the virtual wire are byte-for-byte the Go
; ClientFront authority output — the host check this file's test asserts.
;
; PROVENANCE: ARP wire format RFC 826; the RRQ + option set RFC 1350/2347/2348/
; 2349/7440 (the client set is research note §5.7). The fresh-frame composition
; (ARP request + RRQ wrap) is new — the Go ClientFront/ParseARPReply authority;
; framing layout matches trinload's `packet` buffer (simonowen/trinload).
;
; VERIFICATION: host-verifiable end-to-end under the i80 emulation — drv_write
; the ARP request, inject an ARP reply, drv_read+parse it, then drv_write the
; RRQ, asserting each frame on the virtual wire matches the Go ClientFront byte-
; for-byte (tftp_client_front_test.go). NOT host-verifiable: an end-to-end Pi
; boot — gated on real Trinity (CLAUDE.md §5). Emulation-verified is not
; hardware-verified.

                org     &8000

; ---------------------------------------------------------------------------
; Frame field offsets shared with the receive side (FR_ prefix to avoid a clash
; with the build_udp_frame OFF_* equs included below). ARP payload field
; offsets are measured from the Ethernet header (14), matching the Go
; frame.ParseARPReply layout and build_arp_request's.
; ---------------------------------------------------------------------------
FR_ETHERTYPE:     equ 12
FR_ARP_BASE:      equ 14                 ; ARP payload begins after the Eth header
FR_ARP_HTYPE:     equ 14 + 0             ; 2 bytes  hardware type
FR_ARP_PTYPE:     equ 14 + 2             ; 2 bytes  protocol type
FR_ARP_HLEN:      equ 14 + 4             ; 1 byte   hardware address length
FR_ARP_PLEN:      equ 14 + 5             ; 1 byte   protocol address length
FR_ARP_OPER:      equ 14 + 6             ; 2 bytes  operation
FR_ARP_SHA:       equ 14 + 8             ; 6 bytes  sender hardware address (MAC)
FR_ARP_SPA:       equ 14 + 14            ; 4 bytes  sender protocol address (IP)

ETYPE_ARP:        equ &0806
ARP_HTYPE_ETH:    equ 1                  ; hardware type: Ethernet
ARP_PTYPE_IPV4:   equ &0800              ; protocol type: IPv4
ARP_HLEN:         equ 6                  ; MAC length
ARP_PLEN:         equ 4                  ; IPv4 length
ARP_OP_REPLY:     equ 2

UDP_PORT_TFTP:    equ 69                 ; the server's RRQ listen port

; ---------------------------------------------------------------------------
; tftp_send_arp — broadcast an ARP request for SERVER_IP. Out: BC = frame length.
; ---------------------------------------------------------------------------
tftp_send_arp:
                ; fill build_arp_request's parameter block from our identity.
                ld      hl, CLIENT_MAC
                ld      de, ARP_SRC_MAC
                ld      bc, 6
                ldir
                ld      hl, CLIENT_IP
                ld      de, ARP_SRC_IP
                ld      bc, 4
                ldir
                ld      hl, SERVER_IP
                ld      de, ARP_TARGET_IP
                ld      bc, 4
                ldir

                call    build_arp_request      ; frame at ARP_PACKET, BC = length
                push    bc
                ld      hl, ARP_PACKET
                call    drv_write
                pop     bc                     ; BC = the frame length we sent
                ret

; ---------------------------------------------------------------------------
; tftp_recv_arp — read one frame; if it is a well-formed ARP reply for
; SERVER_IP, learn the server MAC. Out: BC = 1 if learned, 0 otherwise.
;
; The accept tests + the learned field mirror the Go authority
; frame.ParseARPReply: EtherType ARP, HTYPE=Ethernet, PTYPE=IPv4, HLEN=6,
; PLEN=4, OPER=reply; the server MAC is the ARP sender-hardware-address (SHA),
; not the Ethernet source (the two are equal in a conformant reply, but SHA is
; the authoritative field), and the sender-protocol-address must equal SERVER_IP.
; ---------------------------------------------------------------------------
tftp_recv_arp:
                ld      hl, RXBUF
                call    drv_read
                ld      a, b
                or      c
                jp      z, arp_no              ; nothing read

                ; EtherType == ARP (&0806, big-endian)?
                ld      a, (RXBUF + FR_ETHERTYPE)
                cp      ETYPE_ARP >> 8
                jr      nz, arp_no
                ld      a, (RXBUF + FR_ETHERTYPE + 1)
                cp      ETYPE_ARP & &ff
                jr      nz, arp_no

                ; HTYPE == Ethernet (1, big-endian)?
                ld      a, (RXBUF + FR_ARP_HTYPE)
                or      a
                jr      nz, arp_no             ; high byte must be 0
                ld      a, (RXBUF + FR_ARP_HTYPE + 1)
                cp      ARP_HTYPE_ETH
                jr      nz, arp_no
                ; PTYPE == IPv4 (&0800, big-endian)?
                ld      a, (RXBUF + FR_ARP_PTYPE)
                cp      ARP_PTYPE_IPV4 >> 8
                jr      nz, arp_no
                ld      a, (RXBUF + FR_ARP_PTYPE + 1)
                cp      ARP_PTYPE_IPV4 & &ff
                jr      nz, arp_no
                ; HLEN == 6, PLEN == 4?
                ld      a, (RXBUF + FR_ARP_HLEN)
                cp      ARP_HLEN
                jr      nz, arp_no
                ld      a, (RXBUF + FR_ARP_PLEN)
                cp      ARP_PLEN
                jr      nz, arp_no

                ; OPER == reply (2, big-endian)?
                ld      a, (RXBUF + FR_ARP_OPER)
                or      a
                jr      nz, arp_no             ; high byte must be 0
                ld      a, (RXBUF + FR_ARP_OPER + 1)
                cp      ARP_OP_REPLY
                jr      nz, arp_no

                ; sender protocol address == SERVER_IP? (4 bytes)
                ld      hl, RXBUF + FR_ARP_SPA
                ld      de, SERVER_IP
                ld      b, 4
arp_ip_cmp:
                ld      a, (de)
                cp      (hl)
                jr      nz, arp_no
                inc     hl
                inc     de
                djnz    arp_ip_cmp

                ; match: learn the server MAC from the ARP sender-hardware-addr.
                ld      hl, RXBUF + FR_ARP_SHA
                ld      de, SERVER_MAC
                ld      bc, 6
                ldir
                ld      a, 1
                ld      (GOT_MAC), a
                ld      bc, 1                  ; BC=1: learned
                ret
arp_no:
                ld      bc, 0
                ret

; ---------------------------------------------------------------------------
; tftp_send_rrq — build the RRQ for the file at RRQ_FILENAME, wrap it UDP (our
; TID -> the learned server MAC/IP, port 69), and transmit. Out: BC = frame len.
;
; The caller sets RRQ_FILENAME to the NUL-terminated filename before calling.
; ---------------------------------------------------------------------------
tftp_send_rrq:
                ; point build_rrq at our filename, the octet mode, and the
                ; pre-formatted ClientOptionSet bytes.
                ld      hl, RRQ_FILENAME
                ld      (RRQ_FILENAME_PTR), hl
                ld      hl, rrq_mode_octet
                ld      (RRQ_MODE_PTR), hl
                ; copy the option template into build_rrq's RRQ_OPTS buffer.
                ld      hl, rrq_opt_template
                ld      de, RRQ_OPTS
                ld      bc, RRQ_OPT_LEN
                ldir
                ld      hl, RRQ_OPT_LEN
                ld      (RRQ_OPTS_LEN), hl

                call    build_rrq              ; RRQ payload at CRBUF, BC = length

                ; wrap CRBUF (BC bytes) as a UDP datagram to the server.
                ld      (TFTP_PKT_LEN), bc

                ; dst MAC = the learned server MAC
                ld      hl, SERVER_MAC
                ld      de, PARAM_DST_MAC
                ld      bc, 6
                ldir
                ; src MAC = ours
                ld      hl, CLIENT_MAC
                ld      de, PARAM_SRC_MAC
                ld      bc, 6
                ldir
                ; src IP = ours, dst IP = the server
                ld      hl, CLIENT_IP
                ld      de, PARAM_SRC_IP
                ld      bc, 4
                ldir
                ld      hl, SERVER_IP
                ld      de, PARAM_DST_IP
                ld      bc, 4
                ldir
                ; src port = our TID (big-endian), dst port = 69
                ld      hl, CLIENT_TID
                ld      de, PARAM_SRC_PORT
                ld      bc, 2
                ldir
                ld      a, UDP_PORT_TFTP >> 8
                ld      (PARAM_DST_PORT), a
                ld      a, UDP_PORT_TFTP & &ff
                ld      (PARAM_DST_PORT + 1), a
                ; payload = CRBUF, length = TFTP_PKT_LEN
                ld      hl, CRBUF
                ld      (PARAM_PAYLOAD_PTR), hl
                ld      hl, (TFTP_PKT_LEN)
                ld      (PARAM_PAYLOAD_LEN), hl

                call    build_udp_frame        ; frame at PACKET, BC = length
                push    bc
                ld      hl, PACKET
                call    drv_write
                pop     bc                     ; BC = the frame length we sent
                ret

; ===========================================================================
; Configuration + originate state. The harness / boot code fills CLIENT_* +
; SERVER_IP + RRQ_FILENAME; this front owns SERVER_MAC/GOT_MAC.
; ===========================================================================
CLIENT_MAC:       defs 6
CLIENT_IP:        defs 4
CLIENT_TID:       defs 2                 ; our source port (big-endian)

SERVER_IP:        defs 4                 ; the IP we resolve + send the RRQ to
SERVER_MAC:       defs 6                 ; learned from the ARP reply
GOT_MAC:          defs 1

TFTP_PKT_LEN:     defs 2
RRQ_FILENAME:     defs 128               ; caller-supplied NUL-terminated name
RXBUF:            defs 1518

; --- constants -------------------------------------------------------------
rrq_mode_octet:   defm "octet"
                  defb 0

; The ClientOptionSet, pre-formatted as the wire bytes build_rrq copies verbatim:
; each "name",0,"value",0. Byte-identical to the Go tftp.ClientOptionSet ordering
; (blksize, tsize, timeout). windowsize is NOT requested: a lock-step receiver
; must not ask for RFC 7440 windowed delivery it cannot handle (i118/i120).
rrq_opt_template:
                  defm "blksize"
                  defb 0
                  defm "1428"
                  defb 0
                  defm "tsize"
                  defb 0
                  defm "0"
                  defb 0
                  defm "timeout"
                  defb 0
                  defm "2"
                  defb 0
RRQ_OPT_LEN:      equ $ - rrq_opt_template

; ===========================================================================
; The host-verified packet pieces, composed into this translation unit.
; build_udp_frame (PACKET/PARAM_*), build_arp_request (ARP_PACKET/ARP_*), and
; tftp_client (build_rrq + CRBUF/RRQ_*). encdrv supplies drv_init/read/write.
; ===========================================================================
                include "build_udp_frame.asm"
                include "build_arp_request.asm"
                include "tftp_client.asm"
                include "encdrv.asm"
