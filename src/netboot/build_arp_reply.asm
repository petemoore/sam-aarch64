; build_arp_reply.asm — originate a fresh Ethernet ARP reply (RFC 826).
;
; This is the SAM-side Z80 port of the netboot-oracle Go authority
; tools/netboot-oracle/frame/frame.go::BuildARPReply — the inverse of
; build_arp_request.asm. The bring-up smoke test (i94) uses it to answer a
; machine's "who has my IP?" request: a unicast frame back to the asker saying
; "TargetIP is at our MAC", the one observable network action that proves the
; Trinity ENC28J60 path comes up and talks (docs/notes/trinity-capabilities.md).
;
; trinload has no fresh-frame origination (every send is a reply-by-address-swap,
; netboot impl plan §0), so — like build_arp_request and build_udp_frame — this
; builds the whole 42-byte frame from scratch. The frame is the 14-byte Ethernet
; header (dst = the asker's MAC, sam_mac src, EtherType &0806) followed by the
; 28-byte ARP payload: HTYPE=Ethernet, PTYPE=IPv4, HLEN=6, PLEN=4, OPER=reply,
; sender MAC/IP = ours (the answer), target MAC/IP = the asker's (echoed).
;
; PROVENANCE: ARP wire format is RFC 826; the framing layout matches the Go
; `frame` package and trinload's `packet` buffer (simonowen/trinload, Simon
; Owen, BSD-style "do what you like" licence). The fresh ARP-reply composition is
; new (the Go BuildARPReply authority).
;
; VERIFICATION: host-verifiable. tools/netboot-oracle/z80 assembles this file,
; runs it under koron-go/z80 with the same src/dst MAC + src/dst IP the Go test
; uses, and asserts the emitted 42-byte frame is byte-for-byte identical to the
; Go authority's BuildARPReply output. The wire transmission (ENC28J60 I/O) is
; NOT host-verifiable on its own — exercised by the smoke_test state machine over
; the i80 emulation, and gated on real Trinity for true wire I/O (CLAUDE.md §5).

                ; org only when assembled standalone (the host harness builds
                ; this file on its own with -D NETBOOT_STANDALONE=1); when a
                ; state-machine file `include`s it, that file supplies the org.
                if defined(NETBOOT_STANDALONE)
                org     &8000
                endif

; ---------------------------------------------------------------------------
; Frame field offsets (Ethernet header shares the §1.2 offset contract).
; AR_ prefix avoids clashing with build_arp_request's OFF_* / a state machine's
; equs when both are included.
; ---------------------------------------------------------------------------
AR_OFF_DST_MAC:   equ 0
AR_OFF_SRC_MAC:   equ 6
AR_OFF_ETHERTYPE: equ 12
AR_OFF_PAYLOAD:   equ 14                 ; ARP payload begins after the Eth header

AR_ETHERTYPE_ARP: equ &0806

; ARP payload field offsets, measured from AR_OFF_PAYLOAD (14).
AR_OFF_HTYPE:     equ 0                  ; 2 bytes  hardware type
AR_OFF_PTYPE:     equ 2                  ; 2 bytes  protocol type
AR_OFF_HLEN:      equ 4                  ; 1 byte   hardware address length
AR_OFF_PLEN:      equ 5                  ; 1 byte   protocol address length
AR_OFF_OPER:      equ 6                  ; 2 bytes  operation
AR_OFF_SHA:       equ 8                  ; 6 bytes  sender hardware address (MAC)
AR_OFF_SPA:       equ 14                 ; 4 bytes  sender protocol address (IP)
AR_OFF_THA:       equ 18                 ; 6 bytes  target hardware address (MAC)
AR_OFF_TPA:       equ 24                 ; 4 bytes  target protocol address (IP)

AR_HTYPE_ETH:     equ 1                  ; hardware type: Ethernet
AR_PTYPE_IPV4:    equ &0800              ; protocol type: IPv4
AR_HLEN:          equ 6                  ; MAC length
AR_PLEN:          equ 4                  ; IPv4 length
AR_OP_REPLY:      equ 2                  ; operation: reply

AR_FRAME_LEN:     equ AR_OFF_PAYLOAD + 28   ; 14 + 28 = 42

; ---------------------------------------------------------------------------
; Entry: build_arp_reply
;
; Inputs (a parameter block at AR_PARAMS, filled by the caller / harness):
;   AR_SRC_MAC   6 bytes  the SAM's own MAC (sam_mac on hardware) — the answer
;   AR_DST_MAC   6 bytes  the asker's MAC (the reply destination)
;   AR_SRC_IP    4 bytes  the SAM's own IP (sam_ip) — the IP being announced
;   AR_DST_IP    4 bytes  the asker's IP (echoed as the ARP target)
;
; Output:
;   The complete 42-byte ARP reply is written to AR_PACKET.
;   BC = AR_FRAME_LEN (42), ready for drv_write.
; ---------------------------------------------------------------------------
build_arp_reply:
                ; --- Ethernet header ---------------------------------------
                ; dst MAC (6 bytes) <- AR_DST_MAC (unicast back to the asker)
                ld      hl, AR_DST_MAC
                ld      de, AR_PACKET + AR_OFF_DST_MAC
                ld      bc, 6
                ldir
                ; src MAC (6 bytes) <- AR_SRC_MAC
                ld      hl, AR_SRC_MAC
                ld      de, AR_PACKET + AR_OFF_SRC_MAC
                ld      bc, 6
                ldir
                ; EtherType = &0806 (big-endian on the wire)
                ld      a, AR_ETHERTYPE_ARP >> 8
                ld      (AR_PACKET + AR_OFF_ETHERTYPE), a
                ld      a, AR_ETHERTYPE_ARP & &ff
                ld      (AR_PACKET + AR_OFF_ETHERTYPE + 1), a

                ; --- ARP payload (at AR_OFF_PAYLOAD) -----------------------
                ; HTYPE = 1 (Ethernet, big-endian)
                ld      a, AR_HTYPE_ETH >> 8
                ld      (AR_PACKET + AR_OFF_PAYLOAD + AR_OFF_HTYPE), a
                ld      a, AR_HTYPE_ETH & &ff
                ld      (AR_PACKET + AR_OFF_PAYLOAD + AR_OFF_HTYPE + 1), a
                ; PTYPE = &0800 (IPv4, big-endian)
                ld      a, AR_PTYPE_IPV4 >> 8
                ld      (AR_PACKET + AR_OFF_PAYLOAD + AR_OFF_PTYPE), a
                ld      a, AR_PTYPE_IPV4 & &ff
                ld      (AR_PACKET + AR_OFF_PAYLOAD + AR_OFF_PTYPE + 1), a
                ; HLEN = 6, PLEN = 4
                ld      a, AR_HLEN
                ld      (AR_PACKET + AR_OFF_PAYLOAD + AR_OFF_HLEN), a
                ld      a, AR_PLEN
                ld      (AR_PACKET + AR_OFF_PAYLOAD + AR_OFF_PLEN), a
                ; OPER = 2 (reply, big-endian)
                ld      a, AR_OP_REPLY >> 8
                ld      (AR_PACKET + AR_OFF_PAYLOAD + AR_OFF_OPER), a
                ld      a, AR_OP_REPLY & &ff
                ld      (AR_PACKET + AR_OFF_PAYLOAD + AR_OFF_OPER + 1), a

                ; sender hardware address (6 bytes) <- AR_SRC_MAC (the answer)
                ld      hl, AR_SRC_MAC
                ld      de, AR_PACKET + AR_OFF_PAYLOAD + AR_OFF_SHA
                ld      bc, 6
                ldir
                ; sender protocol address (4 bytes) <- AR_SRC_IP
                ld      hl, AR_SRC_IP
                ld      de, AR_PACKET + AR_OFF_PAYLOAD + AR_OFF_SPA
                ld      bc, 4
                ldir
                ; target hardware address (6 bytes) <- AR_DST_MAC (the asker's)
                ld      hl, AR_DST_MAC
                ld      de, AR_PACKET + AR_OFF_PAYLOAD + AR_OFF_THA
                ld      bc, 6
                ldir
                ; target protocol address (4 bytes) <- AR_DST_IP (the asker's)
                ld      hl, AR_DST_IP
                ld      de, AR_PACKET + AR_OFF_PAYLOAD + AR_OFF_TPA
                ld      bc, 4
                ldir

                ; --- Return total frame length in BC -----------------------
                ld      bc, AR_FRAME_LEN
                ret

; ===========================================================================
; Data region — the parameter block and the output `packet` buffer.
;
; On real hardware the source MAC/IP come from the EEPROM "Trinity Network "
; chunk (sam_mac/sam_ip) and the frame is sent via drv_write. Here they are
; caller-supplied so the host harness can drive arbitrary inputs and compare
; the output against the Go authority.
; ===========================================================================
AR_PARAMS:
AR_SRC_MAC:       defs 6
AR_DST_MAC:       defs 6
AR_SRC_IP:        defs 4
AR_DST_IP:        defs 4

AR_PACKET:        defs 1518              ; the output frame buffer
