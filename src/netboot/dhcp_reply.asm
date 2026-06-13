; dhcp_reply.asm — build the DHCP OFFER/ACK body the SAM netboot responder
; (i86) emits in answer to a Pi boot ROM's DISCOVER/REQUEST.
;
; This is the SAM-side Z80 port of the netboot-oracle Go authority
; tools/netboot-oracle/dhcp/dhcp.go::BuildReply. It writes the 240-byte
; BOOTP/DHCP header (op/htype/hlen/xid/flags/yiaddr/siaddr/chaddr/cookie) then
; the option block in the oracle §1 order: message-type, server-id, lease/T1/T2,
; subnet, broadcast, router, PXEClient vendor-class, the echoed client UUID, and
; the fixed 32-byte option-43 "Raspberry Pi Boot" PXE blob, then OptEnd.
;
; The option-43 blob and the PXEClient string are byte-for-byte the constants
; the Go authority sends verbatim (oracle §1; confirmed against the real Pi 400
; capture). siaddr / server-id (54) / router (3) are all the SAM's own IP — this
; is how the Pi learns the TFTP next-server. Option 66/67 are omitted (the Pi
; requests its own filenames).
;
; PROVENANCE: the framing/option layout is the DHCP/BOOTP wire format (RFC
; 2131/2132); the PXE option-43 blob is from the captured Pi 400 netboot
; (docs/notes/pi-netboot-capture-analysis.md §1).
;
; VERIFICATION: host-verifiable. tools/netboot-oracle/z80 assembles this file,
; runs it under koron-go/z80 with the captured DISCOVER's echoed fields as
; input, and asserts the emitted body matches dhcp.BuildReply byte-for-byte.
; The wire transmission (the UDP 67->68 broadcast over the ENC28J60) is NOT
; host-verifiable — gated on i80 / real Trinity.

                org     &8000

; --- DHCP/BOOTP body offsets (from the start of the UDP payload) ------------
OFF_OP:           equ 0
OFF_HTYPE:        equ 1
OFF_HLEN:         equ 2
OFF_HOPS:         equ 3
OFF_XID:          equ 4
OFF_SECS:         equ 8
OFF_FLAGS:        equ 10
OFF_CIADDR:       equ 12
OFF_YIADDR:       equ 16
OFF_SIADDR:       equ 20
OFF_GIADDR:       equ 24
OFF_CHADDR:       equ 28
OFF_SNAME:        equ 44
OFF_FILE:         equ 108
OFF_COOKIE:       equ 236
OFF_OPTIONS:      equ 240

BOOTREPLY:        equ 2
HTYPE_ETH:        equ 1
HLEN_ETH:         equ 6

; Option codes (RFC 2132 + PXE).
OPT_SUBNET:       equ 1
OPT_ROUTER:       equ 3
OPT_LEASE:        equ 51
OPT_MSGTYPE:      equ 53
OPT_SERVERID:     equ 54
OPT_T1:           equ 58
OPT_T2:           equ 59
OPT_VCLASS:       equ 60
OPT_VENCAP:       equ 43
OPT_UUID:         equ 97
OPT_BROADCAST:    equ 28
OPT_END:          equ 255

; ---------------------------------------------------------------------------
; Entry: build_dhcp_reply
;
; Inputs (a parameter block at DPARAMS, filled by the caller / harness):
;   DP_MSGTYPE   1 byte   2 = OFFER, 5 = ACK
;   DP_XID       4 bytes  echoed from the request (network order, as received)
;   DP_FLAGS     2 bytes  echoed request flags, big-endian (0x8000 = broadcast)
;   DP_YIADDR    4 bytes  the address handed to the client (from the pool)
;   DP_SERVERIP  4 bytes  the SAM's IP (server-id / router / siaddr)
;   DP_SUBNET    4 bytes  option 1 netmask
;   DP_BROADCAST 4 bytes  option 28 broadcast address
;   DP_LEASE     4 bytes  option 51 lease seconds, big-endian
;   DP_T1        4 bytes  option 58 renewal T1, big-endian
;   DP_T2        4 bytes  option 59 rebind  T2, big-endian
;   DP_CHADDR    6 bytes  echoed client MAC -> chaddr
;   DP_UUID_LEN  1 byte   length of the echoed option-97 UUID (0 = omit)
;   DP_UUID      up to 32 bytes  the echoed option-97 value (verbatim)
;
; Output:
;   The DHCP body is written to DBODY.
;   BC = the body length (DBODY..end), ready to wrap with build_udp_frame.
; ---------------------------------------------------------------------------
build_dhcp_reply:
                ; --- zero the 240-byte BOOTP header -------------------------
                ld      hl, DBODY
                ld      de, DBODY + 1
                ld      bc, OFF_OPTIONS - 1
                ld      (hl), 0
                ldir

                ; op / htype / hlen
                ld      a, BOOTREPLY
                ld      (DBODY + OFF_OP), a
                ld      a, HTYPE_ETH
                ld      (DBODY + OFF_HTYPE), a
                ld      a, HLEN_ETH
                ld      (DBODY + OFF_HLEN), a

                ; xid (4) <- DP_XID
                ld      hl, DP_XID
                ld      de, DBODY + OFF_XID
                ld      bc, 4
                ldir
                ; flags (2) <- DP_FLAGS (big-endian, copied straight through)
                ld      hl, DP_FLAGS
                ld      de, DBODY + OFF_FLAGS
                ld      bc, 2
                ldir
                ; yiaddr (4) <- DP_YIADDR
                ld      hl, DP_YIADDR
                ld      de, DBODY + OFF_YIADDR
                ld      bc, 4
                ldir
                ; siaddr (4) <- DP_SERVERIP (next-server = the SAM)
                ld      hl, DP_SERVERIP
                ld      de, DBODY + OFF_SIADDR
                ld      bc, 4
                ldir
                ; chaddr (first 6) <- DP_CHADDR
                ld      hl, DP_CHADDR
                ld      de, DBODY + OFF_CHADDR
                ld      bc, 6
                ldir
                ; magic cookie
                ld      hl, magic_cookie
                ld      de, DBODY + OFF_COOKIE
                ld      bc, 4
                ldir

                ; --- options (DE walks the output cursor) ------------------
                ld      de, DBODY + OFF_OPTIONS

                ; opt 53 message-type (1 byte from DP_MSGTYPE)
                ld      a, OPT_MSGTYPE
                ld      (de), a
                inc     de
                ld      a, 1
                ld      (de), a
                inc     de
                ld      a, (DP_MSGTYPE)
                ld      (de), a
                inc     de

                ; opt 54 server-id (4) = DP_SERVERIP
                ld      a, OPT_SERVERID
                ld      hl, DP_SERVERIP
                ld      b, 4
                call    write_opt
                ; opt 51 lease (4) = DP_LEASE
                ld      a, OPT_LEASE
                ld      hl, DP_LEASE
                ld      b, 4
                call    write_opt
                ; opt 58 T1 (4) = DP_T1
                ld      a, OPT_T1
                ld      hl, DP_T1
                ld      b, 4
                call    write_opt
                ; opt 59 T2 (4) = DP_T2
                ld      a, OPT_T2
                ld      hl, DP_T2
                ld      b, 4
                call    write_opt
                ; opt 1 subnet (4) = DP_SUBNET
                ld      a, OPT_SUBNET
                ld      hl, DP_SUBNET
                ld      b, 4
                call    write_opt
                ; opt 28 broadcast (4) = DP_BROADCAST
                ld      a, OPT_BROADCAST
                ld      hl, DP_BROADCAST
                ld      b, 4
                call    write_opt
                ; opt 3 router (4) = DP_SERVERIP
                ld      a, OPT_ROUTER
                ld      hl, DP_SERVERIP
                ld      b, 4
                call    write_opt
                ; opt 60 vendor-class "PXEClient" (9)
                ld      a, OPT_VCLASS
                ld      hl, pxeclient
                ld      b, pxeclient_len
                call    write_opt

                ; opt 97 client UUID (DP_UUID_LEN bytes) — only if length > 0
                ld      a, (DP_UUID_LEN)
                or      a
                jr      z, skip_uuid
                ld      b, a
                ld      a, OPT_UUID
                ld      hl, DP_UUID
                call    write_opt
skip_uuid:
                ; opt 43 vendor-encap = the fixed 32-byte "Raspberry Pi Boot" blob
                ld      a, OPT_VENCAP
                ld      hl, option43
                ld      b, option43_len
                call    write_opt

                ; OptEnd
                ld      a, OPT_END
                ld      (de), a
                inc     de

                ; --- length in BC = DE - DBODY -----------------------------
                ld      hl, DBODY
                ex      de, hl                 ; HL = cursor (end), DE = DBODY
                and     a
                sbc     hl, de                 ; HL = length
                ld      b, h
                ld      c, l
                ret

; ---------------------------------------------------------------------------
; write_opt — append one TLV option to the output at DE.
;   A  = option code
;   B  = value length
;   HL = pointer to the value bytes
;   DE = output cursor (advanced past code+len+value on return)
; Clobbers A, BC (C scratched), HL, DE advances.
; ---------------------------------------------------------------------------
write_opt:
                ld      (de), a                ; code
                inc     de
                ld      a, b
                ld      (de), a                ; length
                inc     de
                ld      c, b
                ld      b, 0
                ldir                           ; copy B value bytes HL->DE
                ret

; --- constants -------------------------------------------------------------
magic_cookie:   defb &63, &82, &53, &63

pxeclient:      defm "PXEClient"
pxeclient_len:  equ $ - pxeclient

; The fixed 32-byte PXE vendor-encapsulated-options blob (oracle §1), sent
; verbatim. Decode: sub-opt 6 DISCOVERY_CONTROL=3, sub-opt 10 MENU_PROMPT
; timeout-0 "PXE", sub-opt 9 BOOT_MENU item 0 len 0x11 "Raspberry Pi Boot", end.
option43:       defb &06, &01, &03
                defb &0a, &04, &00
                defm "PXE"
                defb &09, &14, &00, &00, &11
                defm "Raspberry Pi Boot"
                defb &ff
option43_len:   equ $ - option43

; ===========================================================================
; Data region — the parameter block and the output body buffer.
; ===========================================================================
DPARAMS:
DP_MSGTYPE:       defs 1
DP_XID:           defs 4
DP_FLAGS:         defs 2
DP_YIADDR:        defs 4
DP_SERVERIP:      defs 4
DP_SUBNET:        defs 4
DP_BROADCAST:     defs 4
DP_LEASE:         defs 4
DP_T1:            defs 4
DP_T2:            defs 4
DP_CHADDR:        defs 6
DP_UUID_LEN:      defs 1
DP_UUID:          defs 32

DBODY:            defs 400              ; the output DHCP body buffer
