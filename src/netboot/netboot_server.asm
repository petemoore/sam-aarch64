; netboot_server.asm — the i95 integrated netboot server: the single frame-in /
; reply-out main-loop dispatcher that turns the three host-verified sub-responders
; (ARP bring-up, DHCP, TFTP server) into one program. This is the headline
; Phase-3 program — a SAM + Quazar Trinity that netboots a Raspberry Pi by
; answering its ARP, DHCP, and TFTP requests, serving boot files by name.
;
; This is the SAM-side Z80 port of the netboot-oracle Go authority
; tools/netboot-oracle/server/server.go::Server.OnFrame. It is a NEW state
; machine — it does NOT `include` the standalone loop files (dhcp_loop.asm /
; tftp_server_loop.asm / smoke_test.asm), because each of those defines its own
; RXBUF / CONFIG_* / build_udp_frame / encdrv and they would collide. Instead it
; composes the already-host-verified packet builders/parsers directly
; (build_udp_frame, build_arp_reply, dhcp_reply, tftp_build, tftp_parse) + the
; real vendored driver (encdrv.asm), sharing ONE RXBUF, ONE CONFIG block, ONE
; client endpoint, ONE transfer state, ONE lease table.
;
;   netboot_serve_once:
;     drv_read -> a received Ethernet frame (or nothing)
;     dispatch (mirrors Server.OnFrame, in this order):
;       1. ARP request for our IP        -> an ARP reply       (build_arp_reply)
;       2. not IPv4/UDP                   -> ignore
;       3. UDP dst 67  (DHCP DISCOVER/REQUEST) -> an OFFER/ACK  (dhcp dispatch + pool)
;       4. UDP dst 69  (TFTP RRQ)         -> an OACK (hit) / ERROR(1) (miss), arm xfer
;       5. UDP dst = our transfer TID (TFTP ACK) -> FirstData (ack 0) / next DATA
;       6. anything else                  -> ignore
;     drv_write the chosen reply (or nothing).
;
; The dispatch order + the per-protocol handler bodies + the lease pool + the
; serve-by-name resolve + the OACK/streamed-DATA cadence + the short-final-block
; termination all mirror server.go / its sub-responders step for step; every
; reply byte is produced by the same builders the host harness already proved
; byte-exact (i92/i94). So each emitted frame on the virtual wire is byte-for-byte
; the Go authority output — the host check netboot_server_test.go asserts over a
; full DISCOVER->OFFER->REQUEST->ACK->ARP->RRQ->OACK->ACK->DATA session.
;
; The justOACKed flag (XFER_JUST_OACKED) mirrors server.go: after an OACK reply,
; the next ACK (of block 0) on our transfer TID triggers FirstData (block 1)
; rather than the OnACK advance — matching the Z80 tftp_first_data / tftp_handle_ack
; split in the standalone loop.
;
; PROVENANCE: the combined dispatch is server.go::Server.OnFrame; the ARP bring-up
; is smoke.go; the DHCP DORA + per-MAC pool is dhcp/responder.go; the TFTP
; serve-by-name + ERROR(1)-on-miss-keep-serving + serial-subdir-404 + streamed
; transfer is tftp/serverloop.go. Wire formats: ARP (RFC 826), DHCP/BOOTP (RFC
; 2131/2132 + the captured PXE option-43 blob), TFTP (RFC 1350 + 2347 OACK). The
; framing layout matches trinload's `packet` buffer (simonowen/trinload).
;
; VERIFICATION: host-verifiable end-to-end under the i80 emulation — inject a full
; netboot session and assert every reply frame on the virtual wire matches the Go
; authority byte-for-byte (netboot_server_test.go). NOT host-verifiable: an
; end-to-end Pi boot, the real ENC28J60 silicon, and the B-DOS RST-8 hook dispatch
; (the real src(name) Source) — gated on real Trinity hardware (CLAUDE.md §5).
; Emulation-verified is not hardware-verified.

                org     &8000

                ; The boot entry (CALL 32768) must be the first instruction at
                ; &8000. netboot_main is defined later (under NETBOOT_HOSTTEST==0);
                ; the host harness invokes routines by symbol and never CALLs
                ; 32768, so this jp is bootable-only.
                if defined(NETBOOT_HOSTTEST)==0
                jp      netboot_main
                endif

; ===========================================================================
; Frame field offsets in the received frame (the §1.2 offset contract; RX_
; prefix so they don't clash with the included primitives' OFF_*/AR_OFF_*).
; ===========================================================================
RX_ETH_SRCMAC:    equ 6
RX_ETHERTYPE:     equ 12
RX_IP_FLAGS:      equ 20
RX_IP_PROTO:      equ 23
RX_IP_SRC:        equ 26
RX_UDP_SRCPORT:   equ 34                 ; big-endian on the wire
RX_UDP_DSTPORT:   equ 36                 ; big-endian on the wire
RX_UDP_PAYLOAD:   equ 42

; ARP payload offsets (after the 14-byte Ethernet header).
RX_ARP_HTYPE:     equ 14 + 0
RX_ARP_PTYPE:     equ 14 + 2
RX_ARP_HLEN:      equ 14 + 4
RX_ARP_PLEN:      equ 14 + 5
RX_ARP_OPER:      equ 14 + 6
RX_ARP_SHA:       equ 14 + 8             ; sender hardware address (MAC)
RX_ARP_SPA:       equ 14 + 14            ; sender protocol address (IP)
RX_ARP_TPA:       equ 14 + 24            ; target protocol address (IP)

ETYPE_IPV4:       equ &0800
ETYPE_ARP:        equ &0806
PROTO_UDP_:       equ &11
ARP_HTYPE_ETH:    equ 1
ARP_PTYPE_IPV4:   equ &0800
ARP_HLEN_:        equ 6
ARP_PLEN_:        equ 4
ARP_OP_REQUEST:   equ 1

; DHCP/BOOTP body offsets within the UDP payload.
DH_OP:            equ 0
DH_XID:           equ 4
DH_FLAGS:         equ 10
DH_CHADDR:        equ 28
DH_OPTIONS:       equ 240

BOOTREQUEST_:     equ 1
MSG_DISCOVER_:    equ 1
MSG_OFFER_:       equ 2
MSG_REQUEST_:     equ 3
MSG_ACK_:         equ 5
OPT53_MSGTYPE_:   equ 53
OPT97_UUID_:      equ 97
OPTPAD_:          equ 0
OPTEND_:          equ 255

DHCP_PORT:        equ 67                 ; the SAM's DHCP server port
TFTP_PORT:        equ 69                 ; the SAM's TFTP server port
ACTION_OACK_:     equ 0                  ; mirror tftp.ActionOACK / RESOLVE_ACTION

; ===========================================================================
; netboot_serve_once — read one frame and dispatch it to the right responder,
; transmitting the chosen reply (or nothing). Mirrors Server.OnFrame.
;
; In:  the emulated Trinity is attached; CONFIG_* + the store + the source are
;      filled; drv_init has run.
; Out: BC = bytes transmitted (the reply frame length), or 0 if the frame was
;      ignored (nothing sent — keep serving).
; ===========================================================================
netboot_serve_once:
                ; --- receive a frame into RXBUF -------------------------
                ld      hl, RXBUF
                call    drv_read               ; BC = length (0 if nothing)
                ld      a, b
                or      c
                jp      z, ns_none             ; empty wire: nothing to do
                ld      (RX_LEN), bc

                ; === 1. ARP request for our IP? ========================
                call    try_arp                ; BC = sent length, or 0 if not ARP-for-us
                ld      a, b
                or      c
                ret     nz                     ; an ARP reply was sent

                ; === 2. is it IPv4/UDP at all? =========================
                ; EtherType == IPv4 (&0800, big-endian)?
                ld      a, (RXBUF + RX_ETHERTYPE)
                cp      ETYPE_IPV4 >> 8
                jp      nz, ns_none
                ld      a, (RXBUF + RX_ETHERTYPE + 1)
                cp      ETYPE_IPV4 & &ff
                jp      nz, ns_none
                ; not fragmented (IP flags bit 5 of the high byte clear)?
                ld      a, (RXBUF + RX_IP_FLAGS)
                and     &20
                jp      nz, ns_none
                ; protocol == UDP?
                ld      a, (RXBUF + RX_IP_PROTO)
                cp      PROTO_UDP_
                jp      nz, ns_none

                ; --- read the UDP dst port (big-endian) -----------------
                ; high byte must be 0 for all three of our ports (67/69/TID<256?
                ; no — the TID is a full 16-bit value, so compare it as 16-bit).
                ld      a, (RXBUF + RX_UDP_DSTPORT)        ; high
                ld      h, a
                ld      a, (RXBUF + RX_UDP_DSTPORT + 1)    ; low
                ld      l, a                              ; HL = dst port

                ; === 3. UDP dst 67 -> DHCP =============================
                ld      a, h
                or      a
                jr      nz, ns_check_tid       ; high byte != 0: not 67 or 69
                ld      a, l
                cp      DHCP_PORT
                jp      z, handle_dhcp
                cp      TFTP_PORT
                jp      z, handle_rrq

ns_check_tid:
                ; === 5. UDP dst == our transfer TID -> TFTP ACK ========
                ; compare HL (dst port) with CONFIG_SERVERTID (big-endian).
                ld      a, (CONFIG_SERVERTID)             ; high
                cp      h
                jp      nz, ns_none
                ld      a, (CONFIG_SERVERTID + 1)         ; low
                cp      l
                jp      nz, ns_none
                jp      handle_ack

ns_none:
                ld      bc, 0
                ret

; ===========================================================================
; 1. ARP — answer a who-has for our IP with an ARP reply (smoke.go).
; ===========================================================================
; try_arp — if RXBUF is a well-formed ARP request for CONFIG_SERVERIP, build +
; transmit the ARP reply and return BC = the sent length; else return BC = 0
; (the dispatcher then falls through to the UDP responders).
;
; arp_no (the reject path) sits at the top so every accept-check's `jr nz` is a
; short backward branch — the build-and-send body that follows is too long for a
; forward `jr` to reach a trailing label.
arp_no:
                ld      bc, 0
                ret
try_arp:
                ; EtherType == ARP (&0806, big-endian)?
                ld      a, (RXBUF + RX_ETHERTYPE)
                cp      ETYPE_ARP >> 8
                jr      nz, arp_no
                ld      a, (RXBUF + RX_ETHERTYPE + 1)
                cp      ETYPE_ARP & &ff
                jr      nz, arp_no
                ; HTYPE == Ethernet (1, big-endian)?
                ld      a, (RXBUF + RX_ARP_HTYPE)
                or      a
                jr      nz, arp_no
                ld      a, (RXBUF + RX_ARP_HTYPE + 1)
                cp      ARP_HTYPE_ETH
                jr      nz, arp_no
                ; PTYPE == IPv4 (&0800, big-endian)?
                ld      a, (RXBUF + RX_ARP_PTYPE)
                cp      ARP_PTYPE_IPV4 >> 8
                jr      nz, arp_no
                ld      a, (RXBUF + RX_ARP_PTYPE + 1)
                cp      ARP_PTYPE_IPV4 & &ff
                jr      nz, arp_no
                ; HLEN == 6, PLEN == 4?
                ld      a, (RXBUF + RX_ARP_HLEN)
                cp      ARP_HLEN_
                jr      nz, arp_no
                ld      a, (RXBUF + RX_ARP_PLEN)
                cp      ARP_PLEN_
                jr      nz, arp_no
                ; OPER == request (1, big-endian)?
                ld      a, (RXBUF + RX_ARP_OPER)
                or      a
                jr      nz, arp_no
                ld      a, (RXBUF + RX_ARP_OPER + 1)
                cp      ARP_OP_REQUEST
                jr      nz, arp_no
                ; target protocol address == CONFIG_SERVERIP? (4 bytes)
                ld      hl, RXBUF + RX_ARP_TPA
                ld      de, CONFIG_SERVERIP
                ld      b, 4
arp_ip_cmp:
                ld      a, (de)
                cp      (hl)
                jr      nz, arp_no
                inc     hl
                inc     de
                djnz    arp_ip_cmp

                ; --- it is asking for us: build the ARP reply ----------
                ; src MAC/IP = ours (the answer)
                ld      hl, CONFIG_SERVERMAC
                ld      de, AR_SRC_MAC
                ld      bc, 6
                ldir
                ld      hl, CONFIG_SERVERIP
                ld      de, AR_SRC_IP
                ld      bc, 4
                ldir
                ; dst MAC = the asker's (its ARP sender-hardware-address)
                ld      hl, RXBUF + RX_ARP_SHA
                ld      de, AR_DST_MAC
                ld      bc, 6
                ldir
                ; dst IP = the asker's (its ARP sender-protocol-address)
                ld      hl, RXBUF + RX_ARP_SPA
                ld      de, AR_DST_IP
                ld      bc, 4
                ldir

                call    build_arp_reply        ; frame at AR_PACKET, BC = length
                push    bc
                ld      hl, AR_PACKET
                call    drv_write
                pop     bc                     ; BC = the frame length sent
                ret

; ===========================================================================
; 3. DHCP — DISCOVER->OFFER, REQUEST->ACK, per-MAC lease pool (responder.go).
; ===========================================================================
handle_dhcp:
                ; op == BOOTREQUEST?
                ld      a, (RXBUF + RX_UDP_PAYLOAD + DH_OP)
                cp      BOOTREQUEST_
                jp      nz, ns_none

                ; option 53 message type -> A
                call    find_msgtype           ; A = msgtype, CY=found
                jp      nc, ns_none
                ; DISCOVER -> OFFER ; REQUEST -> ACK ; else ignore.
                cp      MSG_DISCOVER_
                jr      nz, dh_not_disc
                ld      a, MSG_OFFER_
                jr      dh_have_type
dh_not_disc:
                cp      MSG_REQUEST_
                jp      nz, ns_none
                ld      a, MSG_ACK_
dh_have_type:
                ld      (DP_MSGTYPE), a

                ; echo request fields into the reply params.
                ; xid (4)
                ld      hl, RXBUF + RX_UDP_PAYLOAD + DH_XID
                ld      de, DP_XID
                ld      bc, 4
                ldir
                ; flags (2)
                ld      hl, RXBUF + RX_UDP_PAYLOAD + DH_FLAGS
                ld      de, DP_FLAGS
                ld      bc, 2
                ldir
                ; chaddr (6) — the client MAC
                ld      hl, RXBUF + RX_UDP_PAYLOAD + DH_CHADDR
                ld      de, DP_CHADDR
                ld      bc, 6
                ldir

                ; option 97 UUID (copy verbatim, set length)
                call    copy_uuid

                ; config-fixed reply fields
                ld      hl, CONFIG_SERVERIP
                ld      de, DP_SERVERIP
                ld      bc, 4
                ldir
                ld      hl, CONFIG_SUBNET
                ld      de, DP_SUBNET
                ld      bc, 4
                ldir
                ld      hl, CONFIG_BROADCAST
                ld      de, DP_BROADCAST
                ld      bc, 4
                ldir
                ld      hl, CONFIG_LEASE
                ld      de, DP_LEASE
                ld      bc, 4
                ldir
                ld      hl, CONFIG_T1
                ld      de, DP_T1
                ld      bc, 4
                ldir
                ld      hl, CONFIG_T2
                ld      de, DP_T2
                ld      bc, 4
                ldir

                ; yiaddr from the per-MAC lease pool
                call    lease_for_chaddr       ; fills DP_YIADDR

                ; build the DHCP body
                call    build_dhcp_reply       ; body at DBODY, BC = length
                ld      (BODY_LEN), bc

                ; wrap as a UDP 67->68 L2-broadcast frame
                ld      hl, broadcast_mac
                ld      de, PARAM_DST_MAC
                ld      bc, 6
                ldir
                ld      hl, CONFIG_SERVERMAC
                ld      de, PARAM_SRC_MAC
                ld      bc, 6
                ldir
                ld      hl, CONFIG_SERVERIP
                ld      de, PARAM_SRC_IP
                ld      bc, 4
                ldir
                ld      hl, broadcast_ip
                ld      de, PARAM_DST_IP
                ld      bc, 4
                ldir
                ; src port 67, dst port 68 (big-endian on the wire)
                xor     a
                ld      (PARAM_SRC_PORT), a
                ld      a, 67
                ld      (PARAM_SRC_PORT + 1), a
                xor     a
                ld      (PARAM_DST_PORT), a
                ld      a, 68
                ld      (PARAM_DST_PORT + 1), a
                ; payload = DBODY, length = BODY_LEN
                ld      hl, DBODY
                ld      (PARAM_PAYLOAD_PTR), hl
                ld      hl, (BODY_LEN)
                ld      (PARAM_PAYLOAD_LEN), hl

                call    build_udp_frame        ; frame at PACKET, BC = length
                jp      ns_send_packet

; find_msgtype — scan the received DHCP options for option 53 (message type).
; Out: A = the message-type value, CY set, if found; CY clear if absent.
;      Bounded by RX_LEN so a malformed packet can't run away.
find_msgtype:
                ld      hl, RXBUF + RX_UDP_PAYLOAD + DH_OPTIONS
                ld      de, (RX_LEN)
                push    hl
                ld      hl, RXBUF
                add     hl, de
                ex      de, hl                 ; DE = RXBUF + RX_LEN (end)
                pop     hl
fmt_loop:
                push    hl
                or      a
                sbc     hl, de
                pop     hl
                jr      nc, fmt_absent
                ld      a, (hl)                ; option code
                cp      OPTPAD_
                jr      z, fmt_pad
                cp      OPTEND_
                jr      z, fmt_absent
                ld      c, a
                inc     hl                     ; -> length
                push    hl
                or      a
                sbc     hl, de
                pop     hl
                jr      nc, fmt_absent
                ld      b, (hl)                ; length
                inc     hl                     ; -> value
                ld      a, c
                cp      OPT53_MSGTYPE_
                jr      z, fmt_got53
                ld      c, b
                ld      b, 0
                add     hl, bc
                jr      fmt_loop
fmt_pad:
                inc     hl
                jr      fmt_loop
fmt_got53:
                ld      a, (hl)
                scf
                ret
fmt_absent:
                or      a
                ret

; copy_uuid — find option 97 in the received DHCP and copy its value verbatim
; into DP_UUID, setting DP_UUID_LEN. Absent => DP_UUID_LEN = 0 (option omitted).
copy_uuid:
                xor     a
                ld      (DP_UUID_LEN), a
                ld      hl, RXBUF + RX_UDP_PAYLOAD + DH_OPTIONS
                ld      de, (RX_LEN)
                push    hl
                ld      hl, RXBUF
                add     hl, de
                ex      de, hl
                pop     hl
cu_loop:
                push    hl
                or      a
                sbc     hl, de
                pop     hl
                ret     nc
                ld      a, (hl)
                cp      OPTPAD_
                jr      z, cu_pad
                cp      OPTEND_
                ret     z
                ld      c, a
                inc     hl
                push    hl
                or      a
                sbc     hl, de
                pop     hl
                ret     nc
                ld      b, (hl)
                inc     hl
                ld      a, c
                cp      OPT97_UUID_
                jr      z, cu_found
                ld      c, b
                ld      b, 0
                add     hl, bc
                jr      cu_loop
cu_pad:
                inc     hl
                jr      cu_loop
cu_found:
                ld      a, b
                ld      (DP_UUID_LEN), a
                ld      c, b
                ld      b, 0
                ld      de, DP_UUID
                ldir
                ret

; lease_for_chaddr — assign a yiaddr from the fixed pool keyed by the client MAC
; in DP_CHADDR. A known MAC gets its existing address; a new MAC gets
; pool_base + (next_idx mod pool_size) in the low byte. Mirrors responder.lease.
lease_for_chaddr:
                ld      a, (LEASE_COUNT)
                or      a
                jr      z, lease_new
                ld      b, a
                ld      ix, LEASE_TBL
lease_search:
                push    bc
                ld      hl, DP_CHADDR
                push    ix
                pop     de                     ; DE = entry MAC ptr
                ld      b, 6
lease_cmp:
                ld      a, (de)
                cp      (hl)
                jr      nz, lease_miss
                inc     hl
                inc     de
                djnz    lease_cmp
                ; match: DE points at the entry's 4-byte IP
                ld      hl, DP_YIADDR
                ld      bc, 4
                ex      de, hl
                ldir
                pop     bc
                ret
lease_miss:
                pop     bc
                ld      de, 10
                add     ix, de
                djnz    lease_search
lease_new:
                ld      hl, CONFIG_POOLBASE
                ld      de, DP_YIADDR
                ld      bc, 4
                ldir
                ; offset = next_idx mod pool_size
                ld      a, (LEASE_NEXT_IDX)
                ld      b, a
                ld      a, (CONFIG_POOLSIZE)
                ld      c, a
                ld      a, b
ln_mod:
                cp      c
                jr      c, ln_have_off
                sub     c
                jr      ln_mod
ln_have_off:
                ld      b, a
                ld      a, (DP_YIADDR + 3)
                add     a, b
                ld      (DP_YIADDR + 3), a
                ; record the lease: append (DP_CHADDR, DP_YIADDR) to LEASE_TBL
                ld      a, (LEASE_COUNT)
                ld      l, a
                ld      h, 0
                add     hl, hl                 ; *2
                ld      d, h
                ld      e, l
                add     hl, hl                 ; *4
                add     hl, hl                 ; *8
                add     hl, de                 ; *10
                ld      de, LEASE_TBL
                add     hl, de
                ex      de, hl                 ; DE = slot
                ld      hl, DP_CHADDR
                ld      bc, 6
                ldir
                ld      hl, DP_YIADDR
                ld      bc, 4
                ldir
                ld      a, (LEASE_COUNT)
                inc     a
                ld      (LEASE_COUNT), a
                ld      a, (LEASE_NEXT_IDX)
                inc     a
                ld      (LEASE_NEXT_IDX), a
                ret

; ===========================================================================
; 4. TFTP RRQ — serve-by-name: OACK on a hit (arm the transfer), ERROR(1) on a
; miss / serial-subdir (keep serving). Mirrors serverloop.OnRRQ.
; ===========================================================================
handle_rrq:
                ; learn the client endpoint
                ld      hl, RXBUF + RX_ETH_SRCMAC
                ld      de, CLIENT_MAC
                ld      bc, 6
                ldir
                ld      hl, RXBUF + RX_IP_SRC
                ld      de, CLIENT_IP
                ld      bc, 4
                ldir
                ld      hl, RXBUF + RX_UDP_SRCPORT
                ld      de, CLIENT_TID
                ld      bc, 2
                ldir

                ; clear the OACK-armed flag until a hit re-arms it.
                xor     a
                ld      (XFER_JUST_OACKED), a

                ; copy the UDP payload (frame length - 42) into RRQ_IN.
                ld      hl, (RX_LEN)
                ld      de, RX_UDP_PAYLOAD
                or      a
                sbc     hl, de                 ; HL = payload length
                ld      (RRQ_IN_LEN), hl
                push    hl
                pop     bc
                ld      hl, RXBUF + RX_UDP_PAYLOAD
                ld      de, RRQ_IN
                ldir
                call    parse_request
                ld      a, (PARSE_OK)
                or      a
                jp      z, ns_none             ; not a valid RRQ/WRQ

                ; resolve the filename
                ld      hl, (PARSE_FILENAME)
                ld      (RESOLVE_NAME_PTR), hl
                call    resolve
                ld      a, (RESOLVE_ACTION)
                cp      ACTION_OACK_
                jr      z, rrq_hit

                ; --- miss: ERROR(1, "File not found") ------------------
                ld      a, 0
                ld      (ERR_CODE), a
                ld      a, 1
                ld      (ERR_CODE+1), a        ; big-endian code = 0x0001
                ld      hl, err_notfound_msg
                ld      (ERR_MSG_PTR), hl
                call    build_error            ; packet at TBUF, BC = length
                xor     a
                ld      (XFER_ACTIVE), a       ; no transfer
                jp      srv_send_tbuf

rrq_hit:
                ; arm the transfer
                ld      hl, RESOLVE_SIZE
                ld      de, XFER_SIZE
                ld      bc, 4
                ldir
                ld      hl, 1
                ld      (XFER_NEXT_BLK), hl
                ld      hl, 0
                ld      (XFER_OFFSET), hl
                ld      (XFER_OFFSET+2), hl
                ld      a, 1
                ld      (XFER_ACTIVE), a
                ld      (XFER_JUST_OACKED), a  ; the ACK of block 0 -> FirstData

                ; negotiate the blksize, build the OACK option string
                call    negotiate_blksize      ; -> XFER_BLKSIZE
                call    build_oack_opts
                ld      (OACK_OPTS_LEN), hl
                call    build_oack             ; packet at TBUF, BC = length
                jp      srv_send_tbuf

; ===========================================================================
; 5. TFTP ACK — on our transfer TID: FirstData (ack 0, just OACKed) or the next
; DATA (advance), or nothing at the end. Mirrors serverloop.FirstData/OnACK +
; the Server.OnFrame justOACKed split.
; ===========================================================================
handle_ack:
                ld      a, (XFER_ACTIVE)
                or      a
                jp      z, ns_none

                ; justOACKed? the client's ACK of block 0 starts the data flow.
                ld      a, (XFER_JUST_OACKED)
                or      a
                jr      z, ack_advance_path
                ; clear it and send block 1 (FirstData).
                xor     a
                ld      (XFER_JUST_OACKED), a
                jp      send_next_data

ack_advance_path:
                ; UDP source port == the client TID? (serverloop.OnACK ignores an
                ; ACK whose source is not the client of this transfer.) The frame's
                ; UDP source port is big-endian at RX_UDP_SRCPORT; CLIENT_TID is the
                ; client's RRQ source port, stored big-endian.
                ld      a, (RXBUF + RX_UDP_SRCPORT)
                ld      hl, CLIENT_TID
                cp      (hl)
                jp      nz, ns_none
                ld      a, (RXBUF + RX_UDP_SRCPORT + 1)
                inc     hl
                cp      (hl)
                jp      nz, ns_none

                ; opcode 4 (ACK)? payload[0..1] big-endian == 0x0004
                ld      a, (RXBUF + RX_UDP_PAYLOAD)
                or      a
                jp      nz, ns_none
                ld      a, (RXBUF + RX_UDP_PAYLOAD + 1)
                cp      4
                jp      nz, ns_none

                ; ACKed block number (big-endian) -> DE
                ld      a, (RXBUF + RX_UDP_PAYLOAD + 2)
                ld      d, a
                ld      a, (RXBUF + RX_UDP_PAYLOAD + 3)
                ld      e, a                   ; DE = acked block

                ; Was the last DATA the short final block, now ACKed? finish.
                ld      a, (XFER_LAST_SHORT)
                or      a
                jr      z, ack_advance
                xor     a
                ld      (XFER_ACTIVE), a
                jp      ns_none                ; transfer complete

ack_advance:
                inc     de
                ld      (XFER_NEXT_BLK), de
                jp      send_next_data

; send_next_data — read the next block from SRC_PTR+offset, build the DATA
; packet, wrap + transmit. Updates XFER_OFFSET and XFER_LAST_SHORT.
send_next_data:
                ; chunk = min(blksize, remaining); remaining = size - offset (low 16).
                ld      hl, (XFER_SIZE)
                ld      de, (XFER_OFFSET)
                or      a
                sbc     hl, de                 ; HL = remaining (low 16)
                ld      de, (XFER_BLKSIZE)
                push    hl
                or      a
                sbc     hl, de                 ; remaining - blksize
                pop     hl                     ; HL = remaining
                jr      c, snd_use_remaining   ; remaining < blksize
                ld      hl, (XFER_BLKSIZE)     ; full block
                xor     a
                ld      (XFER_LAST_SHORT), a
                jr      snd_have_chunk
snd_use_remaining:
                ld      a, 1
                ld      (XFER_LAST_SHORT), a   ; short (final) block
snd_have_chunk:
                ld      (DATA_LEN), hl

                ; data pointer = SRC_PTR + offset
                ld      hl, (XFER_OFFSET)
                ld      de, (SRC_PTR)
                add     hl, de
                ld      (DATA_PTR), hl

                ; block number (big-endian) <- XFER_NEXT_BLK (stored LE)
                ld      a, (XFER_NEXT_BLK+1)
                ld      (DATA_BLOCK), a
                ld      a, (XFER_NEXT_BLK)
                ld      (DATA_BLOCK+1), a

                call    build_data             ; packet at TBUF, BC = length

                ; advance the offset by the chunk just sent
                ld      hl, (XFER_OFFSET)
                ld      de, (DATA_LEN)
                add     hl, de
                ld      (XFER_OFFSET), hl

                jp      srv_send_tbuf

; srv_send_tbuf — wrap the TFTP packet at TBUF (length BC) as a UDP frame (server
; IP + server TID -> client IP + client TID) and transmit it.
srv_send_tbuf:
                ld      (TFTP_PKT_LEN), bc

                ld      hl, CLIENT_MAC
                ld      de, PARAM_DST_MAC
                ld      bc, 6
                ldir
                ld      hl, CONFIG_SERVERMAC
                ld      de, PARAM_SRC_MAC
                ld      bc, 6
                ldir
                ld      hl, CONFIG_SERVERIP
                ld      de, PARAM_SRC_IP
                ld      bc, 4
                ldir
                ld      hl, CLIENT_IP
                ld      de, PARAM_DST_IP
                ld      bc, 4
                ldir
                ld      hl, CONFIG_SERVERTID
                ld      de, PARAM_SRC_PORT
                ld      bc, 2
                ldir
                ld      hl, CLIENT_TID
                ld      de, PARAM_DST_PORT
                ld      bc, 2
                ldir
                ld      hl, TBUF
                ld      (PARAM_PAYLOAD_PTR), hl
                ld      hl, (TFTP_PKT_LEN)
                ld      (PARAM_PAYLOAD_LEN), hl

                call    build_udp_frame        ; frame at PACKET, BC = length
                jp      ns_send_packet

; ns_send_packet — transmit the frame at PACKET (length in BC), returning BC =
; the sent length. The common tail for the DHCP + TFTP replies (both use PACKET).
ns_send_packet:
                push    bc
                ld      hl, PACKET
                call    drv_write
                pop     bc
                ret

; ===========================================================================
; blksize negotiation + OACK option formatting + decimal helpers (serverloop).
; ===========================================================================
; negotiate_blksize — find the request's "blksize" option, parse + clamp it
; (8..1468 echoed, else 512), store in XFER_BLKSIZE.
negotiate_blksize:
                ld      hl, 512
                ld      (XFER_BLKSIZE), hl
                ld      bc, (PARSE_OPT_COUNT)
                ld      a, b
                or      c
                ret     z
                ld      hl, (PARSE_OPTS)
nb_loop:
                push    bc
                push    hl
                ld      de, str_blksize
                call    streq_cstr
                jr      c, nb_found
                pop     hl
                call    skip_cstr
                call    skip_cstr
                pop     bc
                dec     bc
                ld      a, b
                or      c
                jr      nz, nb_loop
                ret
nb_found:
                pop     de                     ; discard the saved name ptr
                pop     bc                     ; discard the saved count
                call    parse_dec_u16          ; HL = value
                ld      de, 8
                or      a
                sbc     hl, de
                add     hl, de
                jr      c, nb_default          ; v < 8
                ld      de, 1469
                or      a
                sbc     hl, de
                add     hl, de
                jr      nc, nb_default         ; v >= 1469
                ld      (XFER_BLKSIZE), hl
                ret
nb_default:
                ld      hl, 512
                ld      (XFER_BLKSIZE), hl
                ret

; streq_cstr — compare the NUL-terminated string at HL with the one at DE.
; Out: CY set if equal, HL advanced past the matched NUL; CY clear otherwise.
streq_cstr:
                ld      a, (de)
                cp      (hl)
                jr      nz, sq_no
                or      a
                jr      z, sq_yes
                inc     hl
                inc     de
                jr      streq_cstr
sq_yes:
                inc     hl
                scf
                ret
sq_no:
                or      a
                ret

; skip_cstr — advance HL past a NUL-terminated string.
skip_cstr:
                ld      a, (hl)
                inc     hl
                or      a
                jr      nz, skip_cstr
                ret

; parse_dec_u16 — parse the decimal ASCII string at HL into HL (16-bit, wraps).
parse_dec_u16:
                ld      de, 0
pd_loop:
                ld      a, (hl)
                sub     "0"
                jr      c, pd_done
                cp      10
                jr      nc, pd_done
                push    hl
                ld      h, d
                ld      l, e
                add     hl, hl                 ; *2
                push    hl
                add     hl, hl                 ; *4
                add     hl, hl                 ; *8
                pop     bc                     ; BC = acc*2
                add     hl, bc                 ; *10
                ld      e, a
                ld      d, 0
                add     hl, de                 ; + digit
                ld      d, h
                ld      e, l
                pop     hl
                inc     hl
                jr      pd_loop
pd_done:
                ex      de, hl
                ret

; build_oack_opts — format "blksize\0<bs>\0tsize\0<size>\0" into OACK_OPTS.
; Out: HL = the byte count written.
build_oack_opts:
                ld      de, OACK_OPTS
                ld      hl, str_blksize
                call    copy_cstr_incl_nul
                ld      hl, (XFER_BLKSIZE)
                call    write_dec_u16
                xor     a
                ld      (de), a
                inc     de
                ld      hl, str_tsize
                call    copy_cstr_incl_nul
                ld      hl, (XFER_SIZE)
                call    write_dec_u16
                xor     a
                ld      (de), a
                inc     de
                ld      hl, OACK_OPTS
                ex      de, hl
                or      a
                sbc     hl, de                 ; HL = length
                ret

; copy_cstr_incl_nul — copy a NUL-terminated string at HL to DE (incl. the NUL).
copy_cstr_incl_nul:
                ld      a, (hl)
                ld      (de), a
                inc     hl
                inc     de
                or      a
                jr      nz, copy_cstr_incl_nul
                ret

; write_dec_u16 — write HL as a decimal ASCII string at DE (no leading zeros).
write_dec_u16:
                ld      a, &ff
                push    af                     ; sentinel
wd_div_loop:
                call    div_hl_10
                add     a, "0"
                push    af
                ld      a, h
                or      l
                jr      nz, wd_div_loop
wd_pop_loop:
                pop     af
                cp      &ff
                jr      z, wd_done
                ld      (de), a
                inc     de
                jr      wd_pop_loop
wd_done:
                ret

; div_hl_10 — HL = HL / 10, A = HL mod 10.
div_hl_10:
                ld      c, 0
                ld      b, 16
dh_bit:
                add     hl, hl
                ld      a, c
                rla
                cp      10
                jr      c, dh_no_sub
                sub     10
                inc     l
dh_no_sub:
                ld      c, a
                djnz    dh_bit
                ld      a, c
                ret

; --- constants -------------------------------------------------------------
broadcast_mac:    defb &ff, &ff, &ff, &ff, &ff, &ff
broadcast_ip:     defb &ff, &ff, &ff, &ff
err_notfound_msg: defm "File not found"
                  defb 0
str_blksize:      defm "blksize"
                  defb 0
str_tsize:        defm "tsize"
                  defb 0

; ===========================================================================
; Real-hardware bootable entry (excluded from the host harness build, which has
; no EEPROM / real silicon / B-DOS store). CALL 32768 lands here on boot.
; ===========================================================================
                if defined(NETBOOT_HOSTTEST)==0

; netboot_main — read the SAM's MAC + IP from the Trinity EEPROM "Trinity Network"
; chunk, fill the CONFIG block, init the ENC28J60, then loop forever serving the
; netboot session. Mirrors trinload's startup + the smoke test's bring-up.
;
; The DHCP pool + lease/T1/T2 + the server TID are fixed here (a sane default
; pool on the SAM's own subnet); the B-DOS flat store walk that fills STORE +
; SRC_PTR for a resolved name is the i93 seam, wired on real hardware (not in the
; host build). On any bring-up failure it sets a distinctive border colour and
; halts so Pete sees which stage failed.
netboot_main:
                di
                ; --- locate + read the "Trinity Network " flash chunk ---
                ld      a, 1
                ld      (part), a
                ld      (total), a
                ld      hl, nb_chunk_name
                ld      de, name
                ld      bc, 16
                ldir
                call    find_index
                ld      a, (value)
                and     a
                jp      z, nb_fail_cfg
                call    read_chunk
                ld      a, (value)
                and     a
                jp      z, nb_fail_cfg

                ; copy sam_mac (chunk+0) / sam_ip (chunk+6) into CONFIG.
                ld      hl, chunk + 0
                ld      de, CONFIG_SERVERMAC
                ld      bc, 6
                ldir
                ld      hl, chunk + 6
                ld      de, CONFIG_SERVERIP
                ld      bc, 4
                ldir

                ; --- fixed DHCP pool + lease config (the SAM's own subnet) ---
                ; subnet 255.255.255.0
                ld      a, 255
                ld      (CONFIG_SUBNET + 0), a
                ld      (CONFIG_SUBNET + 1), a
                ld      (CONFIG_SUBNET + 2), a
                xor     a
                ld      (CONFIG_SUBNET + 3), a
                ; broadcast = serverIP with the host byte = 255.
                ld      hl, CONFIG_SERVERIP
                ld      de, CONFIG_BROADCAST
                ld      bc, 3
                ldir
                ld      a, 255
                ld      (CONFIG_BROADCAST + 3), a
                ; pool base = serverIP with low byte 100, size 8.
                ld      hl, CONFIG_SERVERIP
                ld      de, CONFIG_POOLBASE
                ld      bc, 3
                ldir
                ld      a, 100
                ld      (CONFIG_POOLBASE + 3), a
                ld      a, 8
                ld      (CONFIG_POOLSIZE), a
                ; lease 7200 / T1 3600 / T2 6300 (big-endian)
                ld      hl, nb_lease_vals
                ld      de, CONFIG_LEASE
                ld      bc, 12
                ldir
                ; server transfer TID (an ephemeral high port), big-endian.
                ld      a, 40136 >> 8
                ld      (CONFIG_SERVERTID), a
                ld      a, 40136 & &ff
                ld      (CONFIG_SERVERTID + 1), a
                ; clear the lease table.
                xor     a
                ld      (LEASE_COUNT), a
                ld      (LEASE_NEXT_IDX), a
                ld      (XFER_ACTIVE), a
                ld      (XFER_JUST_OACKED), a

                ; --- init the ENC28J60 with the SAM's real MAC ----------
                ld      hl, CONFIG_SERVERMAC
                call    drv_init
                ld      a, b
                or      c
                jp      z, nb_fail_init

nb_serve_loop:
                call    netboot_serve_once
                jr      nb_serve_loop

nb_fail_cfg:
                ld      a, 2                   ; red border: no/bad network settings
                out     (&fe), a
                di
                halt
nb_fail_init:
                ld      a, 1                   ; blue border: ENC28J60 init failed
                out     (&fe), a
                di
                halt

nb_chunk_name:    defm "Trinity Network "     ; the flash chunk holding MAC+IP
nb_lease_vals:    defb 0, 0, &1c, &20         ; lease 7200
                  defb 0, 0, &0e, &10         ; T1 3600
                  defb 0, 0, &18, &9c          ; T2 6300

                endif  ; !NETBOOT_HOSTTEST

; ===========================================================================
; CONFIG + shared state. The harness writes CONFIG_* + the store + the source
; directly; on the bootable build netboot_main fills CONFIG from the EEPROM + the
; fixed pool, and the B-DOS seam (i93) fills STORE + SRC_PTR for a resolved name.
; ===========================================================================
CONFIG_SERVERMAC: defs 6
CONFIG_SERVERIP:  defs 4
CONFIG_SUBNET:    defs 4
CONFIG_BROADCAST: defs 4
CONFIG_LEASE:     defs 4                 ; big-endian seconds
CONFIG_T1:        defs 4
CONFIG_T2:        defs 4
CONFIG_POOLBASE:  defs 4
CONFIG_POOLSIZE:  defs 1
CONFIG_SERVERTID: defs 2                 ; the SAM's transfer source port (BE)

; DHCP lease table + scratch.
LEASE_COUNT:      defs 1
LEASE_NEXT_IDX:   defs 1
LEASE_TBL:        defs 320               ; up to 32 (MAC,IP) entries

; The current TFTP client endpoint + transfer state.
CLIENT_MAC:       defs 6
CLIENT_IP:        defs 4
CLIENT_TID:       defs 2                 ; the client's RRQ source port (BE)

SRC_PTR:          defs 2                 ; pointer to the file source bytes
XFER_BLKSIZE:     defs 2
XFER_SIZE:        defs 4                 ; file size (LE)
XFER_OFFSET:      defs 4                 ; bytes streamed so far (LE)
XFER_NEXT_BLK:    defs 2                 ; next block number (LE)
XFER_ACTIVE:      defs 1                 ; 1 = a transfer is armed
XFER_LAST_SHORT:  defs 1                 ; 1 = the last DATA was a short block
XFER_JUST_OACKED: defs 1                 ; 1 = the next ACK (block 0) -> FirstData

RX_LEN:           defs 2
BODY_LEN:         defs 2
TFTP_PKT_LEN:     defs 2
RXBUF:            defs 1518              ; the single received-frame buffer

; ===========================================================================
; The host-verified packet builders/parsers, composed into this one translation
; unit (their org is suppressed — no NETBOOT_STANDALONE — so this file's org
; governs). encdrv.asm is the real vendored ENC28J60 driver; eeprom.asm is the
; real flash config reader (real-hardware path only).
; ===========================================================================
                include "build_udp_frame.asm"
                include "build_arp_reply.asm"
                include "dhcp_reply.asm"
                include "tftp_build.asm"
                include "tftp_parse.asm"
                include "encdrv.asm"
                if defined(NETBOOT_HOSTTEST)==0
                include "eeprom.asm"
                endif
