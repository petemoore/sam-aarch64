; dhcp_loop.asm — the i86 DHCP responder loop: the wire-I/O state machine that
; answers a netbooting Pi's DHCP DISCOVER/REQUEST with an OFFER/ACK.
;
; This is the SAM-side Z80 port of the netboot-oracle Go authority
; tools/netboot-oracle/dhcp/responder.go::Responder.OnRequest. It composes the
; already-host-verified packet primitives into a working state machine and runs
; it over the i80 emulated Trinity Ethernet path (the real vendored driver
; encdrv.asm against tools/netboot-oracle/z80/enc28j60.go):
;
;   dhcp_serve_once:
;     drv_read  -> a received Ethernet frame (or nothing)
;     parse     -> UDP dst port 67? op BOOTREQUEST? option-53 message type?
;     dispatch  -> DISCOVER(1) => OFFER(2);  REQUEST(3) => ACK(5);  else ignore
;     gate      -> option-60 vendor class starts "PXEClient"?  else ignore
;                  (rogue-DHCP protection: only PXE netboot clients are served)
;     lease     -> a yiaddr from a tiny fixed pool keyed by client MAC
;     build     -> build_dhcp_reply (the OFFER/ACK body) then build_udp_frame
;                  (wrap as a UDP 67->68 L2-broadcast)
;     drv_write -> the reply frame onto the wire
;
; The dispatch + pool + reply-build mirror responder.go step for step; the body
; and frame bytes are produced by the same build_dhcp_reply / build_udp_frame
; routines the host harness already proved byte-exact (i92). So the emitted
; OFFER/ACK frame on the virtual wire is byte-for-byte the Go authority's
; Responder.OnRequest output — the host check this file's test asserts.
;
; PROVENANCE: the DORA dispatch (DISCOVER->OFFER, REQUEST->ACK) + the per-MAC
; lease pool are responder.go; the DHCP/BOOTP wire format is RFC 2131/2132; the
; option-43 PXE blob is the captured Pi 400 netboot
; (docs/notes/pi-netboot-capture-analysis.md §1).
;
; VERIFICATION: host-verifiable end-to-end under the i80 emulation — drv_read an
; injected DISCOVER, dispatch + build, drv_write, and assert the OFFER on the
; virtual wire matches the Go Responder byte-for-byte (dhcp_loop_test.go). What
; is NOT host-verifiable: an end-to-end Pi boot + real-silicon TX/RX timing —
; gated on real Trinity hardware (CLAUDE.md §5). Emulation-verified is not
; hardware-verified.

                org     &8000

; ===========================================================================
; The composed state machine. It calls into the included primitives, so it
; supplies the single org and the primitives are included with their own org
; suppressed (they org only under -D NETBOOT_STANDALONE).
; ===========================================================================

; DHCP/BOOTP body offsets within the UDP payload (mirror dhcp_reply.asm; named
; with an RX_ prefix here so they don't clash with the included file's OFF_*).
RX_DHCP_OP:       equ 0
RX_DHCP_XID:      equ 4
RX_DHCP_FLAGS:    equ 10
RX_DHCP_CHADDR:   equ 28
RX_DHCP_COOKIE:   equ 236
RX_DHCP_OPTIONS:  equ 240

; Frame offsets for the received frame (mirror build_udp_frame.asm OFF_*).
RX_UDP_DSTPORT:   equ 36                 ; big-endian on the wire
RX_UDP_PAYLOAD:   equ 42

BOOTREQUEST:      equ 1
MSG_DISCOVER:     equ 1
MSG_OFFER:        equ 2
MSG_REQUEST:      equ 3
MSG_ACK:          equ 5
DHCP_SERVER_PORT: equ 67                 ; the SAM listens here

OPT53_MSGTYPE:    equ 53
OPT97_UUID:       equ 97
OPTPAD:           equ 0
OPTEND:           equ 255

; ---------------------------------------------------------------------------
; dhcp_serve_once — read one frame, answer a DISCOVER/REQUEST, ignore the rest.
;
; In:  the emulated Trinity is attached; CONFIG_* / pool params are filled.
; Out: BC = total bytes transmitted (the reply frame length), or 0 if the frame
;      was not a DHCP DISCOVER/REQUEST (nothing sent).
; ---------------------------------------------------------------------------
dhcp_serve_once:
                ; --- receive a frame into RXBUF --------------------------
                ld      hl, RXBUF
                call    drv_read               ; BC = length (0 if nothing)
                ld      a, b
                or      c
                jp      z, serve_none          ; empty wire: nothing to do
                ld      (RX_LEN), bc

                ; --- UDP dst port == 67? --------------------------------
                ld      a, (RXBUF + RX_UDP_DSTPORT)        ; big-endian high
                or      a
                jp      nz, serve_none
                ld      a, (RXBUF + RX_UDP_DSTPORT + 1)    ; low byte
                cp      DHCP_SERVER_PORT
                jp      nz, serve_none

                ; --- op == BOOTREQUEST? ---------------------------------
                ld      a, (RXBUF + RX_UDP_PAYLOAD + RX_DHCP_OP)
                cp      BOOTREQUEST
                jp      nz, serve_none

                ; --- option 53 message type -> A ------------------------
                call    find_msgtype           ; A = msgtype (0 if absent), CY=found
                jr      c, mt_found
                jp      serve_none
mt_found:
                ; DISCOVER -> OFFER ; REQUEST -> ACK ; else ignore.
                cp      MSG_DISCOVER
                jr      nz, not_discover
                ld      a, MSG_OFFER
                jr      have_reply_type
not_discover:
                cp      MSG_REQUEST
                jp      nz, serve_none
                ld      a, MSG_ACK
have_reply_type:
                ld      (DP_MSGTYPE), a        ; into the build_dhcp_reply param

                ; --- vendor class (option 60) must start "PXEClient" ----
                ; Rogue-DHCP protection — port of responder.go::OnRequest's
                ; option-60 gate: the SAM serves only PXE netboot clients, so
                ; any other DHCP client on a shared LAN is ignored.
                call    check_vendor_pxe       ; CY set if conformant
                jp      nc, serve_none

                ; --- echo request fields into the reply params ----------
                ; xid (4)
                ld      hl, RXBUF + RX_UDP_PAYLOAD + RX_DHCP_XID
                ld      de, DP_XID
                ld      bc, 4
                ldir
                ; flags (2)
                ld      hl, RXBUF + RX_UDP_PAYLOAD + RX_DHCP_FLAGS
                ld      de, DP_FLAGS
                ld      bc, 2
                ldir
                ; chaddr (6) — the client MAC
                ld      hl, RXBUF + RX_UDP_PAYLOAD + RX_DHCP_CHADDR
                ld      de, DP_CHADDR
                ld      bc, 6
                ldir

                ; --- option 97 UUID (copy verbatim, set length) ---------
                call    copy_uuid              ; fills DP_UUID / DP_UUID_LEN

                ; --- config-fixed reply fields (server IP, subnet, etc.) -
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

                ; --- yiaddr from the per-MAC lease pool -----------------
                call    lease_for_chaddr       ; fills DP_YIADDR

                ; --- build the DHCP body -------------------------------
                call    build_dhcp_reply       ; body at DBODY, BC = length
                ld      (BODY_LEN), bc

                ; --- wrap as a UDP 67->68 L2-broadcast frame -----------
                ; dst MAC = broadcast
                ld      hl, broadcast_mac
                ld      de, PARAM_DST_MAC
                ld      bc, 6
                ldir
                ; src MAC = the SAM's
                ld      hl, CONFIG_SERVERMAC
                ld      de, PARAM_SRC_MAC
                ld      bc, 6
                ldir
                ; src IP = the SAM's
                ld      hl, CONFIG_SERVERIP
                ld      de, PARAM_SRC_IP
                ld      bc, 4
                ldir
                ; dst IP = 255.255.255.255 (limited broadcast)
                ld      hl, broadcast_ip
                ld      de, PARAM_DST_IP
                ld      bc, 4
                ldir
                ; src port 67, dst port 68 (big-endian on the wire)
                ld      a, 0
                ld      (PARAM_SRC_PORT), a
                ld      a, 67
                ld      (PARAM_SRC_PORT + 1), a
                ld      a, 0
                ld      (PARAM_DST_PORT), a
                ld      a, 68
                ld      (PARAM_DST_PORT + 1), a
                ; payload = DBODY, length = BODY_LEN
                ld      hl, DBODY
                ld      (PARAM_PAYLOAD_PTR), hl
                ld      hl, (BODY_LEN)
                ld      (PARAM_PAYLOAD_LEN), hl

                call    build_udp_frame        ; frame at PACKET, BC = length

                ; --- transmit -------------------------------------------
                push    bc                     ; save the frame length for the return
                ld      hl, PACKET             ; HL = frame, BC = length
                call    drv_write              ; transmit (returns BC=1 success)
                pop     bc                     ; BC = the frame length sent
                ret

serve_none:
                ld      bc, 0
                ret

; ---------------------------------------------------------------------------
; find_msgtype — scan the received DHCP options for option 53 (message type).
; Out: A = the message-type value, CY set, if found; CY clear if absent.
;      Bounded by RX_LEN so a malformed packet can't run away.
; Clobbers: A, BC, DE, HL.
; ---------------------------------------------------------------------------
find_msgtype:
                ; HL = first option byte; DE = one past the frame end.
                ld      hl, RXBUF + RX_UDP_PAYLOAD + RX_DHCP_OPTIONS
                ld      de, (RX_LEN)
                push    hl
                ld      hl, RXBUF
                add     hl, de
                ex      de, hl                 ; DE = RXBUF + RX_LEN (end)
                pop     hl
fmt_loop:
                ; stop if HL >= end
                push    hl
                or      a
                sbc     hl, de
                pop     hl
                jr      nc, fmt_absent
                ld      a, (hl)                ; A = option code
                cp      OPTPAD
                jr      z, fmt_pad
                cp      OPTEND
                jr      z, fmt_absent
                ld      c, a                   ; C = option code (kept)
                inc     hl                     ; -> length
                push    hl
                or      a
                sbc     hl, de
                pop     hl
                jr      nc, fmt_absent         ; length byte past end
                ld      b, (hl)                ; B = option length
                inc     hl                     ; HL -> value
                ld      a, c
                cp      OPT53_MSGTYPE
                jr      z, fmt_got53
                ; skip this option's value (B bytes)
                ld      c, b
                ld      b, 0
                add     hl, bc                 ; HL += length
                jr      fmt_loop
fmt_pad:
                inc     hl
                jr      fmt_loop
fmt_got53:
                ld      a, (hl)                ; the message-type value
                scf
                ret
fmt_absent:
                or      a                      ; clear CY
                ret

; ---------------------------------------------------------------------------
; check_vendor_pxe — scan the received DHCP options for option 60 (vendor
; class) and require its value to carry the 9-byte "PXEClient" prefix. Port of
; responder.go::OnRequest's option-60 gate (rogue-DHCP protection: the SAM
; serves only PXE netboot clients). Prefix match, not equality — the real
; Pi 400 sends the 32-byte "PXEClient:Arch:00000:UNDI:002001". The compare
; string is the included dhcp_reply.asm's pxeclient (the outbound echo), so
; the gate and the echo share the same bytes.
; Out: CY set if option 60 is present with the PXEClient prefix; CY clear
;      otherwise. Bounded by RX_LEN so a malformed packet can't run away.
; Clobbers: A, BC, DE, HL.
; ---------------------------------------------------------------------------
check_vendor_pxe:
                ; HL = first option byte; DE = one past the frame end.
                ld      hl, RXBUF + RX_UDP_PAYLOAD + RX_DHCP_OPTIONS
                ld      de, (RX_LEN)
                push    hl
                ld      hl, RXBUF
                add     hl, de
                ex      de, hl                 ; DE = RXBUF + RX_LEN (end)
                pop     hl
cvp_loop:
                ; stop if HL >= end
                push    hl
                or      a
                sbc     hl, de
                pop     hl
                jr      nc, cvp_no
                ld      a, (hl)                ; A = option code
                cp      OPTPAD
                jr      z, cvp_pad
                cp      OPTEND
                jr      z, cvp_no
                ld      c, a                   ; C = option code (kept)
                inc     hl                     ; -> length
                push    hl
                or      a
                sbc     hl, de
                pop     hl
                jr      nc, cvp_no             ; length byte past end
                ld      b, (hl)                ; B = option length
                inc     hl                     ; HL -> value
                ld      a, c
                cp      OPT_VCLASS
                jr      z, cvp_got60
                ; skip this option's value (B bytes)
                ld      c, b
                ld      b, 0
                add     hl, bc                 ; HL += length
                jr      cvp_loop
cvp_pad:
                inc     hl
                jr      cvp_loop
cvp_got60:
                ; prefix match: length >= 9 and the first 9 bytes = "PXEClient".
                ld      a, b
                cp      pxeclient_len
                jr      c, cvp_no              ; value too short for the prefix
                ; the 9 compared value bytes must lie inside the frame: a
                ; truncated option 60 whose claimed length runs past RX_LEN
                ; would otherwise be compared against stale RXBUF bytes
                ; beyond the frame (the same push/sbc bound style as the
                ; code/length checks above, applied to the value; i349).
                push    hl
                ld      bc, pxeclient_len
                add     hl, bc
                or      a
                sbc     hl, de                 ; (value + 9) - end
                pop     hl
                jr      z, cvp_prefix          ; value+9 == end: still in frame
                jr      nc, cvp_no             ; value+9 > end: truncated, ignore
cvp_prefix:
                ld      de, pxeclient
                ld      b, pxeclient_len
cvp_cmp:
                ld      a, (de)
                cp      (hl)
                jr      nz, cvp_no
                inc     hl
                inc     de
                djnz    cvp_cmp
                scf
                ret
cvp_no:
                or      a                      ; clear CY
                ret

; ---------------------------------------------------------------------------
; copy_uuid — find option 97 in the received DHCP and copy its value verbatim
; into DP_UUID, setting DP_UUID_LEN. Absent => DP_UUID_LEN = 0 (option omitted).
; Clobbers: A, BC, DE, HL.
; ---------------------------------------------------------------------------
copy_uuid:
                xor     a
                ld      (DP_UUID_LEN), a
                ; HL = first option byte; DE = end.
                ld      hl, RXBUF + RX_UDP_PAYLOAD + RX_DHCP_OPTIONS
                ld      de, (RX_LEN)
                push    hl
                ld      hl, RXBUF
                add     hl, de
                ex      de, hl                 ; DE = end
                pop     hl
cu_loop:
                push    hl
                or      a
                sbc     hl, de
                pop     hl
                ret     nc                     ; reached end: option 97 absent
                ld      a, (hl)
                cp      OPTPAD
                jr      z, cu_pad
                cp      OPTEND
                ret     z
                ld      c, a                   ; C = option code
                inc     hl                     ; -> length
                push    hl
                or      a
                sbc     hl, de
                pop     hl
                ret     nc
                ld      b, (hl)                ; B = length
                inc     hl                     ; HL -> value
                ld      a, c
                cp      OPT97_UUID
                jr      z, cu_found
                ; skip the value
                ld      c, b
                ld      b, 0
                add     hl, bc
                jr      cu_loop
cu_pad:
                inc     hl
                jr      cu_loop
cu_found:
                ; B = length, HL -> value; copy verbatim into DP_UUID.
                ld      a, b
                ld      (DP_UUID_LEN), a
                ld      c, b
                ld      b, 0
                ld      de, DP_UUID
                ldir
                ret

; ---------------------------------------------------------------------------
; lease_for_chaddr — assign a yiaddr from the fixed pool, keyed by the client
; MAC now in DP_CHADDR. A MAC already in the lease table gets its existing
; address; a new MAC gets pool_base + (next_idx mod pool_size) in the low byte,
; and next_idx advances. Mirrors responder.go::lease.
;
; The lease table is LEASE_TBL: repeated (6-byte MAC, 4-byte IP) entries, count
; in LEASE_COUNT. Out: DP_YIADDR filled. Clobbers: A, BC, DE, HL, IX.
; ---------------------------------------------------------------------------
lease_for_chaddr:
                ; search the existing leases for DP_CHADDR
                ld      a, (LEASE_COUNT)
                or      a
                jr      z, lease_new           ; empty table
                ld      b, a                   ; B = entry count
                ld      ix, LEASE_TBL
lease_search:
                push    bc
                ; compare 6 MAC bytes at IX with DP_CHADDR
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
                ; match: DE now points at the entry's 4-byte IP
                ld      hl, DP_YIADDR
                ld      bc, 4
                ex      de, hl                 ; HL=IP src, DE=DP_YIADDR dst
                ldir
                pop     bc
                ret
lease_miss:
                pop     bc
                ; advance IX past this 10-byte entry
                ld      de, 10
                add     ix, de
                djnz    lease_search
                ; fall through: not found -> allocate a new lease
lease_new:
                ; new address = CONFIG_POOLBASE with low byte += (next_idx mod size)
                ld      hl, CONFIG_POOLBASE
                ld      de, DP_YIADDR
                ld      bc, 4
                ldir
                ; compute offset = next_idx mod pool_size
                ld      a, (LEASE_NEXT_IDX)
                ld      b, a
                ld      a, (CONFIG_POOLSIZE)
                ld      c, a
                ld      a, b
ln_mod:
                cp      c
                jr      c, ln_have_off         ; A < size: A is the remainder
                sub     c
                jr      ln_mod
ln_have_off:
                ; DP_YIADDR low byte += offset
                ld      b, a
                ld      a, (DP_YIADDR + 3)
                add     a, b
                ld      (DP_YIADDR + 3), a

                ; record the lease: append (DP_CHADDR, DP_YIADDR) to LEASE_TBL
                ; entry slot = LEASE_TBL + LEASE_COUNT*10
                ld      a, (LEASE_COUNT)
                ld      l, a
                ld      h, 0
                ; HL = count*10
                add     hl, hl                 ; *2
                ld      d, h
                ld      e, l
                add     hl, hl                 ; *4
                add     hl, hl                 ; *8
                add     hl, de                 ; *8 + *2 = *10
                ld      de, LEASE_TBL
                add     hl, de                 ; HL = slot
                ex      de, hl                 ; DE = slot
                ; copy MAC (6)
                ld      hl, DP_CHADDR
                ld      bc, 6
                ldir
                ; copy IP (4)
                ld      hl, DP_YIADDR
                ld      bc, 4
                ldir
                ; count++
                ld      a, (LEASE_COUNT)
                inc     a
                ld      (LEASE_COUNT), a
                ; next_idx++
                ld      a, (LEASE_NEXT_IDX)
                inc     a
                ld      (LEASE_NEXT_IDX), a
                ret

; --- constants -------------------------------------------------------------
broadcast_mac:    defb &ff, &ff, &ff, &ff, &ff, &ff
broadcast_ip:     defb &ff, &ff, &ff, &ff

; ===========================================================================
; Configuration block (the harness / boot code fills these with the SAM's
; network identity; on hardware they come from the EEPROM "Trinity Network"
; chunk + a chosen pool).
; ===========================================================================
CONFIG_SERVERMAC: defs 6
CONFIG_SERVERIP:  defs 4
CONFIG_SUBNET:    defs 4
CONFIG_BROADCAST: defs 4
CONFIG_LEASE:     defs 4                 ; big-endian seconds
CONFIG_T1:        defs 4
CONFIG_T2:        defs 4
CONFIG_POOLBASE:  defs 4                 ; first pool address
CONFIG_POOLSIZE:  defs 1                 ; number of pool addresses (>=1)

; Lease table + scratch.
LEASE_COUNT:      defs 1
LEASE_NEXT_IDX:   defs 1
LEASE_TBL:        defs 320               ; up to 32 (MAC,IP) entries
RX_LEN:           defs 2
BODY_LEN:         defs 2
RXBUF:            defs 1518              ; received frame buffer

; ===========================================================================
; The host-verified packet primitives, composed into this translation unit.
; Their org is suppressed (no NETBOOT_STANDALONE) so this file's org governs.
; ===========================================================================
                include "build_udp_frame.asm"
                include "dhcp_reply.asm"
                include "encdrv.asm"
