; build_arp_request.asm — originate a fresh Ethernet ARP request (RFC 826).
;
; This is the SAM-side Z80 port of the netboot-oracle Go authority
; tools/netboot-oracle/frame/frame.go::BuildARPRequest — the ARP variant of the
; "fresh-frame primitive" (netboot impl plan §5.1) that trinload lacks. The i82
; client originates an ARP request to learn the netboot server's MAC before it
; can send its RRQ (plan §2 step 1): trinload has no fresh-frame origination
; (every send is a reply-by-address-swap, plan §0), so the client builds this
; whole 42-byte frame with no incoming packet to swap from, broadcasts it, and
; caches the reply's sender MAC.
;
; The frame is the 14-byte Ethernet header (broadcast dst, sam_mac src,
; EtherType &0806) followed by the 28-byte ARP payload: HTYPE=Ethernet,
; PTYPE=IPv4, HLEN=6, PLEN=4, OPER=request, sender MAC/IP = ours, target MAC =
; zero (the unknown being resolved), target IP = the address we are asking about.
;
; PROVENANCE: ARP wire format is RFC 826; the framing layout matches the Go
; `frame` package and trinload's `packet` buffer (simonowen/trinload, Simon
; Owen, BSD-style "do what you like" licence). The fresh ARP-request composition
; is new (the Go BuildARPRequest authority).
;
; VERIFICATION: host-verifiable. tools/netboot-oracle/z80 assembles this file,
; runs it under koron-go/z80 with the same src MAC/IP + target IP the Go test
; uses, and asserts the emitted 42-byte frame is byte-for-byte identical to the
; Go authority's BuildARPRequest output. The wire transmission (ENC28J60 I/O) is
; NOT host-verifiable — gated on i80 / real Trinity, and lives elsewhere.

                org     &8000

; ---------------------------------------------------------------------------
; Frame field offsets (Ethernet header shares the §1.2 offset contract).
; ---------------------------------------------------------------------------
OFF_DST_MAC:      equ 0
OFF_SRC_MAC:      equ 6
OFF_ETHERTYPE:    equ 12
OFF_ARP_PAYLOAD:  equ 14                 ; ARP payload begins after the Eth header

ETHERTYPE_ARP:    equ &0806

; ARP payload field offsets, measured from OFF_ARP_PAYLOAD (14).
OFF_ARP_HTYPE:    equ 0                  ; 2 bytes  hardware type
OFF_ARP_PTYPE:    equ 2                  ; 2 bytes  protocol type
OFF_ARP_HLEN:     equ 4                  ; 1 byte   hardware address length
OFF_ARP_PLEN:     equ 5                  ; 1 byte   protocol address length
OFF_ARP_OPER:     equ 6                  ; 2 bytes  operation
OFF_ARP_SHA:      equ 8                  ; 6 bytes  sender hardware address (MAC)
OFF_ARP_SPA:      equ 14                 ; 4 bytes  sender protocol address (IP)
OFF_ARP_THA:      equ 18                 ; 6 bytes  target hardware address (MAC)
OFF_ARP_TPA:      equ 24                 ; 4 bytes  target protocol address (IP)

ARP_HTYPE_ETH:    equ 1                  ; hardware type: Ethernet
ARP_PTYPE_IPV4:   equ &0800              ; protocol type: IPv4
ARP_HLEN:         equ 6                  ; MAC length
ARP_PLEN:         equ 4                  ; IPv4 length
ARP_OP_REQUEST:   equ 1                  ; operation: request

ARP_FRAME_LEN:    equ OFF_ARP_PAYLOAD + 28   ; 14 + 28 = 42

; ---------------------------------------------------------------------------
; Entry: build_arp_request
;
; Inputs (a parameter block at ARP_PARAMS, filled by the caller / harness):
;   ARP_SRC_MAC    6 bytes  the SAM's own MAC (sam_mac on hardware)
;   ARP_SRC_IP     4 bytes  the SAM's own IP  (sam_ip on hardware)
;   ARP_TARGET_IP  4 bytes  the IP whose MAC we are resolving (the server)
;
; Output:
;   The complete 42-byte ARP request is written to ARP_PACKET.
;   BC = ARP_FRAME_LEN (42), ready for drv_write.
; ---------------------------------------------------------------------------
build_arp_request:
                ; --- Ethernet header ---------------------------------------
                ; dst MAC = broadcast (ff:ff:ff:ff:ff:ff)
                ld      hl, ARP_PACKET + OFF_DST_MAC
                ld      b, 6
                ld      a, &ff
arp_bcast_loop:
                ld      (hl), a
                inc     hl
                djnz    arp_bcast_loop
                ; src MAC (6 bytes) <- ARP_SRC_MAC
                ld      hl, ARP_SRC_MAC
                ld      de, ARP_PACKET + OFF_SRC_MAC
                ld      bc, 6
                ldir
                ; EtherType = &0806 (big-endian on the wire)
                ld      a, ETHERTYPE_ARP >> 8
                ld      (ARP_PACKET + OFF_ETHERTYPE), a
                ld      a, ETHERTYPE_ARP & &ff
                ld      (ARP_PACKET + OFF_ETHERTYPE + 1), a

                ; --- ARP payload (at OFF_ARP_PAYLOAD) ----------------------
                ; HTYPE = 1 (Ethernet, big-endian)
                ld      a, ARP_HTYPE_ETH >> 8
                ld      (ARP_PACKET + OFF_ARP_PAYLOAD + OFF_ARP_HTYPE), a
                ld      a, ARP_HTYPE_ETH & &ff
                ld      (ARP_PACKET + OFF_ARP_PAYLOAD + OFF_ARP_HTYPE + 1), a
                ; PTYPE = &0800 (IPv4, big-endian)
                ld      a, ARP_PTYPE_IPV4 >> 8
                ld      (ARP_PACKET + OFF_ARP_PAYLOAD + OFF_ARP_PTYPE), a
                ld      a, ARP_PTYPE_IPV4 & &ff
                ld      (ARP_PACKET + OFF_ARP_PAYLOAD + OFF_ARP_PTYPE + 1), a
                ; HLEN = 6, PLEN = 4
                ld      a, ARP_HLEN
                ld      (ARP_PACKET + OFF_ARP_PAYLOAD + OFF_ARP_HLEN), a
                ld      a, ARP_PLEN
                ld      (ARP_PACKET + OFF_ARP_PAYLOAD + OFF_ARP_PLEN), a
                ; OPER = 1 (request, big-endian)
                ld      a, ARP_OP_REQUEST >> 8
                ld      (ARP_PACKET + OFF_ARP_PAYLOAD + OFF_ARP_OPER), a
                ld      a, ARP_OP_REQUEST & &ff
                ld      (ARP_PACKET + OFF_ARP_PAYLOAD + OFF_ARP_OPER + 1), a

                ; sender hardware address (6 bytes) <- ARP_SRC_MAC
                ld      hl, ARP_SRC_MAC
                ld      de, ARP_PACKET + OFF_ARP_PAYLOAD + OFF_ARP_SHA
                ld      bc, 6
                ldir
                ; sender protocol address (4 bytes) <- ARP_SRC_IP
                ld      hl, ARP_SRC_IP
                ld      de, ARP_PACKET + OFF_ARP_PAYLOAD + OFF_ARP_SPA
                ld      bc, 4
                ldir
                ; target hardware address (6 bytes) = zero (unknown)
                ld      hl, ARP_PACKET + OFF_ARP_PAYLOAD + OFF_ARP_THA
                ld      b, 6
                xor     a
arp_tha_loop:
                ld      (hl), a
                inc     hl
                djnz    arp_tha_loop
                ; target protocol address (4 bytes) <- ARP_TARGET_IP
                ld      hl, ARP_TARGET_IP
                ld      de, ARP_PACKET + OFF_ARP_PAYLOAD + OFF_ARP_TPA
                ld      bc, 4
                ldir

                ; --- Return total frame length in BC -----------------------
                ld      bc, ARP_FRAME_LEN
                ret

; ===========================================================================
; Data region — the parameter block and the output `packet` buffer.
;
; On real hardware the source MAC/IP come from the EEPROM "Trinity Network "
; chunk (sam_mac/sam_ip) and the frame is sent via drv_write. Here they are
; caller-supplied so the host harness can drive arbitrary inputs and compare
; the output against the Go authority.
; ===========================================================================
ARP_PARAMS:
ARP_SRC_MAC:      defs 6
ARP_SRC_IP:       defs 4
ARP_TARGET_IP:    defs 4

ARP_PACKET:       defs 1518              ; the output frame buffer
