; netboot_client.asm — the i82 TFTP client boot disk (Phase-3 increment 3): a
; bootable program that fetches a file (a .mgt disk image) from a TFTP server and
; writes it to Trinity storage via the B-DOS record hooks (i93).
;
; This is the SAM-side Z80 port of the netboot-oracle Go authority
; tools/netboot-oracle/client/client.go::Client. Like netboot_server.asm /
; netboot_serve.asm it is a NEW state machine — it does NOT `include` the
; standalone front/loop files (tftp_client_front.asm / tftp_client_loop.asm), each
; of which defines its own CLIENT_*/SERVER_*/RXBUF/build_udp_frame/encdrv and would
; collide. It ports the front (ARP-for-server → RRQ) + the receive loop (DATA/ACK
; accumulate) inline into ONE translation unit composing the host-verified
; primitives once (build_udp_frame, build_arp_request, tftp_client (build_rrq),
; bdos_seam) + the real vendored driver (encdrv.asm).
;
; The fetch is a phase machine (trinload has no fresh-frame origination, so the
; client originates the whole flow), mirroring client.go::Client:
;
;   client_first      -> broadcast the ARP request for SERVER_IP (the first frame)
;   client_run_once   -> read one frame and step the phase machine:
;     PHASE_ARP : an ARP reply for SERVER_IP -> learn the server MAC, send the RRQ,
;                 advance to PHASE_XFER; any other frame -> nothing (keep waiting)
;     PHASE_XFER: a DATA frame to our TID -> accumulate into STAGING, send the ACK;
;                 the short final block -> set XFER_DONE; a stray-TID DATA ->
;                 ERROR(5) to the stray sender (RFC 1350 §4)
;     Out: BC = bytes transmitted (0 if nothing sent); (XFER_DONE) set at the end.
;
; On completion the bootable client_main writes the staged bytes to Trinity storage
; via the B-DOS seam: bdos_fill_save_uifa + bdos_save_hook (HSAVE). The HSAVE hook
; dispatch is behind NETBOOT_HOSTTEST (NOT host-verifiable — no ROM/DOS/RST 8 in
; the harness); the field arithmetic (bdos_fill_save_uifa) IS host-verified (i93).
;
; PROVENANCE: the phase machine is client.go::Client; the ARP-for-server + RRQ-send
; front is clientfront.go (ported from tftp_client_front.asm); the DATA/ACK receive
; + SAS retransmit is clientloop.go (ported from tftp_client_loop.asm); the B-DOS
; write-out is bdos.go::SaveUIFA (bdos_seam.asm). Wire formats: ARP (RFC 826), TFTP
; (RFC 1350 + 2347). Framing matches trinload's `packet` buffer.
;
; VERIFICATION: client_first + client_run_once are host-verifiable end-to-end under
; the i80 emulation — drive the ARP request, inject the ARP reply, assert the RRQ;
; inject DATA blocks, assert the ACK cadence + the accumulated STAGING bytes — all
; byte-for-byte against client.go::Client (netboot_client_test.go). NOT
; host-verifiable: the B-DOS RST-8 HSAVE write-out, the real ENC28J60 silicon, and
; an end-to-end fetch on real hardware — gated on real Trinity (CLAUDE.md §5).
; Emulation-verified is not hardware-verified.

                org     &8000

                ; The boot entry (CALL 32768) must be the first instruction at
                ; &8000. client_main is defined later (under NETBOOT_HOSTTEST==0);
                ; the host harness invokes routines by symbol and never CALLs
                ; 32768, so this jp is bootable-only.
                if defined(NETBOOT_HOSTTEST)==0
                jp      client_main
                endif

; ===========================================================================
; Frame field offsets (RX_/FR_ prefixes so they don't clash with the included
; primitives' OFF_*/ARP_*).
; ===========================================================================
RX_ETH_SRCMAC:    equ 6
RX_ETHERTYPE:     equ 12
RX_IP_SRC:        equ 26
RX_UDP_SRCPORT:   equ 34                 ; big-endian on the wire
RX_UDP_DSTPORT:   equ 36
RX_UDP_PAYLOAD:   equ 42

; ARP payload offsets (after the 14-byte Ethernet header).
RX_ARP_HTYPE:     equ 14 + 0
RX_ARP_PTYPE:     equ 14 + 2
RX_ARP_HLEN:      equ 14 + 4
RX_ARP_PLEN:      equ 14 + 5
RX_ARP_OPER:      equ 14 + 6
RX_ARP_SHA:       equ 14 + 8             ; sender hardware address (MAC)
RX_ARP_SPA:       equ 14 + 14            ; sender protocol address (IP)

ETYPE_ARP:        equ &0806
; ARP_HTYPE_ETH (1) and ARP_PTYPE_IPV4 (&0800) come from the included
; build_arp_request.asm.
ARP_HLEN_:        equ 6
ARP_PLEN_:        equ 4
ARP_OP_REPLY:     equ 2

UDP_PORT_TFTP:    equ 69                 ; the server's RRQ listen port
OP_DATA:          equ 3
OP_ACK:           equ 4
OP_ERROR:         equ 5
OP_OACK:          equ 6
ERR_UNKNOWN_TID:  equ 5

PHASE_ARP:        equ 0
PHASE_XFER:       equ 1

; ===========================================================================
; client_first — broadcast the ARP request for SERVER_IP (the first frame the
; client puts on the wire). Out: BC = the frame length sent.
; Mirrors client.go::First / tftp_send_arp.
; ===========================================================================
client_first:
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
                pop     bc
                ret

; ===========================================================================
; client_run_once — read one frame and step the phase machine. Out: BC = bytes
; transmitted (0 if nothing sent). Mirrors client.go::OnFrame.
; ===========================================================================
client_run_once:
                ld      hl, RXBUF
                call    drv_read
                ld      a, b
                or      c
                jp      z, cl_none             ; empty wire: nothing to do
                ld      (CL_RX_LEN), bc

                ld      a, (PHASE)
                cp      PHASE_XFER
                jp      z, do_recv_data
                ; else PHASE_ARP
                jp      do_recv_arp

cl_none:
                ld      bc, 0
                ret

; ===========================================================================
; PHASE_ARP — learn the server MAC from a matching ARP reply, then send the RRQ.
; Mirrors clientfront.OnARPReply + RRQFrame (tftp_recv_arp + tftp_send_rrq).
; ===========================================================================
; arp_no (reject) sits first so the accept checks use short backward `jr`.
arp_no:
                ld      bc, 0
                ret
do_recv_arp:
                ; EtherType == ARP?
                ld      a, (RXBUF + RX_ETHERTYPE)
                cp      ETYPE_ARP >> 8
                jr      nz, arp_no
                ld      a, (RXBUF + RX_ETHERTYPE + 1)
                cp      ETYPE_ARP & &ff
                jr      nz, arp_no
                ; HTYPE == Ethernet?
                ld      a, (RXBUF + RX_ARP_HTYPE)
                or      a
                jr      nz, arp_no
                ld      a, (RXBUF + RX_ARP_HTYPE + 1)
                cp      ARP_HTYPE_ETH
                jr      nz, arp_no
                ; PTYPE == IPv4?
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
                ; OPER == reply (2)?
                ld      a, (RXBUF + RX_ARP_OPER)
                or      a
                jr      nz, arp_no
                ld      a, (RXBUF + RX_ARP_OPER + 1)
                cp      ARP_OP_REPLY
                jr      nz, arp_no
                ; sender protocol address == SERVER_IP? (4 bytes)
                ld      hl, RXBUF + RX_ARP_SPA
                ld      de, SERVER_IP
                ld      b, 4
arp_ip_cmp:
                ld      a, (de)
                cp      (hl)
                jr      nz, arp_no
                inc     hl
                inc     de
                djnz    arp_ip_cmp

                ; learn the server MAC from the ARP sender-hardware-address.
                ld      hl, RXBUF + RX_ARP_SHA
                ld      de, SERVER_MAC
                ld      bc, 6
                ldir
                ld      a, 1
                ld      (GOT_MAC), a
                ; advance to the transfer phase and send the RRQ.
                ld      a, PHASE_XFER
                ld      (PHASE), a
                jp      send_rrq

; send_rrq — build the RRQ for (RRQ_FILENAME), wrap it UDP (our TID -> the learned
; server MAC/IP:69), transmit. Out: BC = the frame length sent. Mirrors
; clientfront.RRQFrame / tftp_send_rrq.
send_rrq:
                ld      hl, RRQ_FILENAME
                ld      (RRQ_FILENAME_PTR), hl
                ld      hl, rrq_mode_octet
                ld      (RRQ_MODE_PTR), hl
                ld      hl, rrq_opt_template
                ld      de, RRQ_OPTS
                ld      bc, RRQ_OPT_LEN
                ldir
                ld      hl, RRQ_OPT_LEN
                ld      (RRQ_OPTS_LEN), hl

                call    build_rrq              ; RRQ payload at CRBUF, BC = length

                ld      (TFTP_PKT_LEN), bc
                ld      hl, SERVER_MAC
                ld      de, PARAM_DST_MAC
                ld      bc, 6
                ldir
                ld      hl, CLIENT_MAC
                ld      de, PARAM_SRC_MAC
                ld      bc, 6
                ldir
                ld      hl, CLIENT_IP
                ld      de, PARAM_SRC_IP
                ld      bc, 4
                ldir
                ld      hl, SERVER_IP
                ld      de, PARAM_DST_IP
                ld      bc, 4
                ldir
                ld      hl, CLIENT_TID
                ld      de, PARAM_SRC_PORT
                ld      bc, 2
                ldir
                ld      a, UDP_PORT_TFTP >> 8
                ld      (PARAM_DST_PORT), a
                ld      a, UDP_PORT_TFTP & &ff
                ld      (PARAM_DST_PORT + 1), a
                ld      hl, CRBUF
                ld      (PARAM_PAYLOAD_PTR), hl
                ld      hl, (TFTP_PKT_LEN)
                ld      (PARAM_PAYLOAD_LEN), hl

                call    build_udp_frame        ; frame at PACKET, BC = length
                push    bc
                ld      hl, PACKET
                call    drv_write
                pop     bc
                ret

; ===========================================================================
; PHASE_XFER — receive a DATA frame, accumulate into STAGING, send the ACK. The
; short final block ends the transfer; a stray-TID DATA gets ERROR(5). Mirrors
; clientloop.OnDATA / tftp_recv_data.
; ===========================================================================
do_recv_data:
                ; UDP dst port == our TID? (2 big-endian bytes)
                ld      a, (RXBUF + RX_UDP_DSTPORT)
                ld      hl, CLIENT_TID
                cp      (hl)
                jp      nz, cl_none
                ld      a, (RXBUF + RX_UDP_DSTPORT + 1)
                inc     hl
                cp      (hl)
                jp      nz, cl_none

                ; opcode high byte 0, then dispatch: OACK(6) starts the transfer
                ; (option negotiation), DATA(3) carries a block.
                ld      a, (RXBUF + RX_UDP_PAYLOAD)
                or      a
                jp      nz, cl_none
                ld      a, (RXBUF + RX_UDP_PAYLOAD + 1)
                cp      OP_OACK
                jp      z, do_recv_oack
                cp      OP_DATA
                jp      nz, cl_none

                ; first DATA? learn the server endpoint from it.
                ld      a, (GOT_SERVER)
                or      a
                jr      nz, cl_have_server
                ld      hl, RXBUF + RX_ETH_SRCMAC
                ld      de, SERVER_MAC
                ld      bc, 6
                ldir
                ld      hl, RXBUF + RX_IP_SRC
                ld      de, SERVER_IP
                ld      bc, 4
                ldir
                ld      hl, RXBUF + RX_UDP_SRCPORT
                ld      de, SERVER_TID
                ld      bc, 2
                ldir
                ld      a, 1
                ld      (GOT_SERVER), a
                jr      cl_tid_ok

cl_have_server:
                ; validate the source port == the learned server TID.
                ld      a, (RXBUF + RX_UDP_SRCPORT)
                ld      hl, SERVER_TID
                cp      (hl)
                jp      nz, cl_stray
                ld      a, (RXBUF + RX_UDP_SRCPORT + 1)
                inc     hl
                cp      (hl)
                jp      nz, cl_stray
cl_tid_ok:
                ; block number (big-endian) -> DE
                ld      a, (RXBUF + RX_UDP_PAYLOAD + 2)
                ld      d, a
                ld      a, (RXBUF + RX_UDP_PAYLOAD + 3)
                ld      e, a                   ; DE = block

                ; expected == ACKED + 1?
                ld      hl, (ACKED)
                inc     hl
                or      a
                sbc     hl, de                 ; (acked+1) - block
                jr      z, cl_next_block
                ; block <= acked => duplicate (re-ACK); block > acked+1 => future.
                ld      hl, (ACKED)
                or      a
                sbc     hl, de                 ; acked - block
                jp      c, cl_none             ; future, not +1: ignore
                ; duplicate: re-ACK the received block (DE) without storing.
                ld      (ACK_BLOCK), de
                jp      cl_send_ack

cl_next_block:
                ; payload length = frame length - (42 + 4 TFTP header)
                ld      hl, (CL_RX_LEN)
                ld      de, RX_UDP_PAYLOAD + 4
                or      a
                sbc     hl, de                 ; HL = data length
                ld      (LAST_DATA_LEN), hl

                ; route the payload: STAGING (mode 0, default — the i82 host-verified
                ; whole-file accumulate) or the i122b raw-record streaming sink
                ; (mode 1 — the i122c fetch-and-boot path: write 512-byte sectors as
                ; they arrive, so a full 819200-byte disk image never sits whole in
                ; RAM). CLIENT_SINK_MODE selects; only the boot build has the sink.
                if defined(NETBOOT_HOSTTEST)==0
                ld      a, (CLIENT_SINK_MODE)
                or      a
                jr      nz, cl_sink_payload
                endif

                ; --- mode 0: accumulate the payload into STAGING at the offset ---
                ld      hl, (LAST_DATA_LEN)
                push    hl
                ld      hl, (STAGE_OFFSET)
                ld      de, STAGING
                add     hl, de
                ex      de, hl                 ; DE = dest
                ld      hl, RXBUF + RX_UDP_PAYLOAD + 4
                pop     bc                     ; BC = data length
                ld      a, b
                or      c
                jr      z, cl_no_copy
                push    bc
                ldir
                pop     bc
cl_no_copy:
                ld      hl, (STAGE_OFFSET)
                ld      de, (LAST_DATA_LEN)
                add     hl, de
                ld      (STAGE_OFFSET), hl
                jr      cl_after_payload

                if defined(NETBOOT_HOSTTEST)==0
cl_sink_payload:
                ; --- mode 1: stream the payload into the selected record. The sink
                ; re-blocks into sectors + HWSADs each full one, and tracks the
                ; 32-bit image size in RRS_TOTAL (STAGE_OFFSET is left alone — its
                ; 16 bits would overflow on a full record; RRS_TOTAL is the size
                ; authority the finalize step validates). ---
                ld      hl, RXBUF + RX_UDP_PAYLOAD + 4
                ld      bc, (LAST_DATA_LEN)
                call    raw_record_sink_leaf
                endif
cl_after_payload:

                ; acked = block (assemble big-endian -> store little-endian).
                ld      a, (RXBUF + RX_UDP_PAYLOAD + 2)
                ld      h, a
                ld      a, (RXBUF + RX_UDP_PAYLOAD + 3)
                ld      l, a
                ld      (ACKED), hl
                ld      (ACK_BLOCK), hl

                ; done if data length < blksize.
                ld      hl, (LAST_DATA_LEN)
                ld      de, (CLIENT_BLKSIZE)
                or      a
                sbc     hl, de
                jr      nc, cl_send_ack
                ld      a, 1
                ld      (XFER_DONE), a
cl_send_ack:
                call    build_ack_frame_and_send
                ret

cl_stray:
                ; a DATA from a wrong server TID: ERROR(5) back to the stray sender.
                ld      hl, RXBUF + RX_ETH_SRCMAC
                ld      de, REPLY_MAC
                ld      bc, 6
                ldir
                ld      hl, RXBUF + RX_IP_SRC
                ld      de, REPLY_IP
                ld      bc, 4
                ldir
                ld      hl, RXBUF + RX_UDP_SRCPORT
                ld      de, REPLY_TID
                ld      bc, 2
                ldir
                ld      hl, TPKT
                ld      (hl), 0
                inc     hl
                ld      (hl), OP_ERROR
                inc     hl
                ld      (hl), 0
                inc     hl
                ld      (hl), ERR_UNKNOWN_TID
                inc     hl
                ld      de, err_unknown_tid_msg
ce_copy:
                ld      a, (de)
                ld      (hl), a
                inc     de
                inc     hl
                or      a
                jr      nz, ce_copy
                ld      de, TPKT
                or      a
                sbc     hl, de                 ; HL = payload length
                push    hl
                pop     bc
                jp      wrap_reply_to_stray

; build_ack_frame_and_send — build ACK(ACK_BLOCK) at TPKT, wrap (our TID -> server
; TID), transmit. Out: BC = the transmitted frame length.
build_ack_frame_and_send:
                ld      hl, TPKT
                ld      (hl), 0
                inc     hl
                ld      (hl), OP_ACK
                inc     hl
                ld      a, (ACK_BLOCK+1)       ; LE store -> BE wire
                ld      (hl), a
                inc     hl
                ld      a, (ACK_BLOCK)
                ld      (hl), a
                ld      bc, 4

                ld      hl, SERVER_MAC
                ld      de, REPLY_MAC
                push    bc
                ld      bc, 6
                ldir
                ld      hl, SERVER_IP
                ld      de, REPLY_IP
                ld      bc, 4
                ldir
                ld      hl, SERVER_TID
                ld      de, REPLY_TID
                ld      bc, 2
                ldir
                pop     bc
                jp      wrap_reply

; ===========================================================================
; PHASE_XFER (OACK) — a standards-compliant server answers the *optioned* RRQ
; with an OACK (RFC 2347) BEFORE any DATA. Learn the server endpoint, adopt the
; server's negotiated blksize (absent/garbled -> the 512 default stands), and ACK
; block 0 to tell the server to start sending DATA. Mirrors clientloop.onOACK.
; ===========================================================================
do_recv_oack:
                ; learn the server endpoint (the OACK's source) if not yet known.
                ld      a, (GOT_SERVER)
                or      a
                jr      nz, oack_have_server
                ld      hl, RXBUF + RX_ETH_SRCMAC
                ld      de, SERVER_MAC
                ld      bc, 6
                ldir
                ld      hl, RXBUF + RX_IP_SRC
                ld      de, SERVER_IP
                ld      bc, 4
                ldir
                ld      hl, RXBUF + RX_UDP_SRCPORT
                ld      de, SERVER_TID
                ld      bc, 2
                ldir
                ld      a, 1
                ld      (GOT_SERVER), a
oack_have_server:
                ; copy the OACK payload (CL_RX_LEN - 42) into OACK_IN for parsing.
                ld      hl, (CL_RX_LEN)
                ld      de, RX_UDP_PAYLOAD
                or      a
                sbc     hl, de
                ld      (OACK_IN_LEN), hl
                ld      b, h
                ld      c, l
                ld      a, b
                or      c
                jr      z, oack_ack0           ; empty: keep the 512 default
                ld      hl, RXBUF + RX_UDP_PAYLOAD
                ld      de, OACK_IN
                ldir
                call    parse_oack
                ld      a, (OACK_OK)
                or      a
                jr      z, oack_ack0           ; not a valid OACK: keep 512
                ld      hl, oack_blksize_name
                ld      (FIND_NAME_PTR), hl
                call    find_option
                ld      a, (FIND_OK)
                or      a
                jr      z, oack_ack0           ; no blksize option: keep 512
                ld      hl, (FIND_VALUE_PTR)
                call    atoi_dec               ; (HL) decimal string -> DE
                ld      a, d
                or      e
                jr      z, oack_ack0           ; zero/garbage: keep 512
                ex      de, hl
                ld      (CLIENT_BLKSIZE), hl   ; adopt the negotiated blksize
oack_ack0:
                ; ACK block 0 -> the server starts sending DATA at block 1.
                ld      hl, 0
                ld      (ACK_BLOCK), hl
                jp      build_ack_frame_and_send

; atoi_dec — parse a NUL/non-digit-terminated decimal string at HL into DE
; (16-bit, wraps past 65535 — TFTP blksizes are well within range).
; Clobbers A, BC, DE, HL.
atoi_dec:
                ld      bc, 0                  ; BC = accumulator
atd_loop:
                ld      a, (hl)
                sub     "0"                    ; '0' = 0x30; strip digit bias
                jr      c, atd_done            ; below '0'
                cp      10
                jr      nc, atd_done           ; above '9'
                inc     hl
                push    hl
                ld      h, b
                ld      l, c                   ; HL = acc
                add     hl, hl                 ; acc*2
                push    hl
                add     hl, hl                 ; acc*4
                add     hl, hl                 ; acc*8
                pop     de                     ; DE = acc*2
                add     hl, de                 ; acc*10
                ld      e, a
                ld      d, 0                   ; DE = digit
                add     hl, de                 ; acc*10 + digit
                ld      b, h
                ld      c, l                   ; acc = HL
                pop     hl                     ; restore the string cursor
                jr      atd_loop
atd_done:
                ld      d, b
                ld      e, c                   ; DE = the parsed value
                ret

oack_blksize_name: defm "blksize"
                   defb 0

; tftp_recv_timeout — SAS fix: retransmit the last ACK only (never the RRQ).
; Out: BC = bytes transmitted, or 0 if no block has been ACKed yet.
client_recv_timeout:
                ld      hl, (ACKED)
                ld      a, h
                or      l
                jp      z, cl_none             ; nothing ACKed yet: caller re-sends RRQ
                ld      (ACK_BLOCK), hl
                jp      build_ack_frame_and_send

; wrap_reply / wrap_reply_to_stray — frame the TFTP packet at TPKT (length BC) as a
; UDP datagram from us (CLIENT_*) to REPLY_* and transmit. Out: BC = the sent length.
wrap_reply_to_stray:
wrap_reply:
                ld      (TFTP_PKT_LEN), bc
                ld      hl, REPLY_MAC
                ld      de, PARAM_DST_MAC
                ld      bc, 6
                ldir
                ld      hl, CLIENT_MAC
                ld      de, PARAM_SRC_MAC
                ld      bc, 6
                ldir
                ld      hl, CLIENT_IP
                ld      de, PARAM_SRC_IP
                ld      bc, 4
                ldir
                ld      hl, REPLY_IP
                ld      de, PARAM_DST_IP
                ld      bc, 4
                ldir
                ld      hl, CLIENT_TID
                ld      de, PARAM_SRC_PORT
                ld      bc, 2
                ldir
                ld      hl, REPLY_TID
                ld      de, PARAM_DST_PORT
                ld      bc, 2
                ldir
                ld      hl, TPKT
                ld      (PARAM_PAYLOAD_PTR), hl
                ld      hl, (TFTP_PKT_LEN)
                ld      (PARAM_PAYLOAD_LEN), hl
                call    build_udp_frame        ; frame at PACKET, BC = length
                push    bc
                ld      hl, PACKET
                call    drv_write
                pop     bc
                ret

; --- constants -------------------------------------------------------------
rrq_mode_octet:   defm "octet"
                  defb 0

; The ClientOptionSet, byte-identical to the Go tftp.ClientOptionSet ordering
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

err_unknown_tid_msg: defm "unknown transfer ID"
                     defb 0

; ===========================================================================
; Real-hardware bootable entry (excluded from the host harness build, which has
; no EEPROM / real silicon / B-DOS store). CALL 32768 lands here on boot.
; ===========================================================================
                if defined(NETBOOT_HOSTTEST)==0

; client_main — read the SAM's MAC + IP from the Trinity EEPROM, fill CLIENT_*, set
; SERVER_IP + the file to fetch (a fixed default), init the ENC28J60, broadcast the
; ARP request, then loop receiving until the transfer completes; finally HSAVE the
; staged image to Trinity storage via the B-DOS seam. On any bring-up failure it
; sets a distinctive border colour and halts.
; client_setup — shared boot preamble for the netboot client entries: read the
; SAM's MAC+IP from the Trinity EEPROM into CLIENT_*, set the fixed fetch target
; (SERVER_IP + filename + the requested blksize), clear the transfer state
; (CLIENT_SINK_MODE = 0 = STAGING by default), init the ENC28J60, and wait for the
; PHY link. Halts with a distinct border on any bring-up failure (config/init/
; link); returns on success.
client_setup:
                di
                ; --- locate + read the "Trinity Network " flash chunk ---
                ld      a, 1
                ld      (part), a
                ld      (total), a
                ld      hl, cl_chunk_name
                ld      de, name
                ld      bc, 16
                ldir
                call    find_index
                ld      a, (value)
                and     a
                jp      z, cl_fail_cfg
                call    read_chunk
                ld      a, (value)
                and     a
                jp      z, cl_fail_cfg

                ; copy sam_mac (chunk+0) / sam_ip (chunk+6) into CLIENT_*.
                ld      hl, chunk + 0
                ld      de, CLIENT_MAC
                ld      bc, 6
                ldir
                ld      hl, chunk + 6
                ld      de, CLIENT_IP
                ld      bc, 4
                ldir

                ; --- the fixed fetch target (edit these for your network) ---
                ; SERVER_IP = the TFTP server holding the .mgt image.
                ld      hl, cl_server_ip
                ld      de, SERVER_IP
                ld      bc, 4
                ldir
                ; our TID (an ephemeral high port), big-endian.
                ld      a, 30574 >> 8
                ld      (CLIENT_TID), a
                ld      a, 30574 & &ff
                ld      (CLIENT_TID + 1), a
                ; the file to fetch + the negotiated block size we request.
                ld      hl, cl_filename
                ld      de, RRQ_FILENAME
                ld      bc, cl_filename_end - cl_filename
                ldir
                ; the effective blksize starts at the 512 TFTP default; an OACK
                ; (the RRQ requests blksize 1428) raises it; a no-OACK server keeps 512.
                ld      hl, 512
                ld      (CLIENT_BLKSIZE), hl

                ; clear the transfer state.
                xor     a
                ld      (PHASE), a             ; PHASE_ARP
                ld      (GOT_MAC), a
                ld      (GOT_SERVER), a
                ld      (XFER_DONE), a
                ld      (CLIENT_SINK_MODE), a  ; default = STAGING; client_fetch_boot sets 1
                ld      hl, 0
                ld      (ACKED), hl
                ld      (STAGE_OFFSET), hl

                ; --- init the ENC28J60 with the SAM's real MAC ----------
                ld      hl, CLIENT_MAC
                call    drv_init
                ld      a, b
                or      c
                jp      z, cl_fail_init

                ; --- wait for the PHY link before the first (proactive) TX -
                ; client_first broadcasts an ARP request immediately; a frame
                ; sent before the 10BASE-T link is up is silently dropped, and
                ; drv_init does not wait for link. A reactive server gets this for
                ; free (its first send is a reply); a proactive client must wait.
                ; (i127 — root-caused via the i124 boot-path emulation.)
                call    drv_wait_link
                ld      a, b
                or      c
                jp      z, cl_fail_link
                ret

; client_main — fetch the configured file into STAGING and HSAVE it to a picked
; Trinity record (the flat-file write-out, with the show-name+confirm picker).
; CALL 32768 lands here on boot.
client_main:
                call    client_setup
                ; --- read the SD CSD -> BD_RECORDS (i145b) ---------------
                ; The record picker below needs the card's total record count;
                ; csd_set_bd_records computes it from the inserted SD card's CSD
                ; (replacing the inject-only shortcut that left it 0 on real
                ; hardware -> no free record -> safe decline). Shipped as a
                ; section-D overlay (i145b-b2): the ~600-byte SD-read+decode pushes
                ; the boot image past &C000 into section D, which is RAM at boot
                ; (LOAD CODE 32768 deposits the >&BFFF bytes; ROM1 off at run) — the
                ; image stays inside the 32768-byte &8000-&FFFF window. Verified by
                ; csd_to_bd_records_test.go and the serve_main boot integration test.
                call    csd_set_bd_records
                ; --- broadcast the ARP request, then receive until done --
                call    client_first
cl_fetch_loop:
                call    client_run_once
                ld      a, (XFER_DONE)
                or      a
                jr      z, cl_fetch_loop

                ; --- pick a Trinity record (show-name + confirm), then write ---
                ; The Trinity SD is a SHARED user resource: the picker shows the
                ; chosen record's name (or "free / unnamed") and requires the user
                ; to confirm BEFORE any write. No confirm => no write (i119e).
                ; BD_RECORDS (card record count) is computed from the SD CSD
                ; capacity read at client_main startup (csd_set_bd_records, i145b).
                ; On any SD read failure it stays 0 => no free record => safe
                ; decline (no write).
                xor     a
                ld      (BD_PICK_MODE), a          ; mode 0 = auto-pick-free
                call    bdos_pick_record
                ld      a, (BD_PICK_CONFIRMED)
                or      a
                jp      z, cl_write_declined        ; not confirmed => abort the write
                ld      a, (BD_PICK_RECORD)         ; low byte = record n (>=1)
                call    bdos_select_record
                ; build the HSAVE UIFA: name, source page/offset = STAGING, size.
                ld      hl, RRQ_FILENAME
                ld      (BD_NAME_PTR), hl
                ; STAGING lives in section C; its page/offset are the HMPR page the
                ; loader maps + the &xxxx offset. Use the staging label's address.
                ld      a, STAGING >> 14       ; the physical page of STAGING
                ld      (BD_SAVE_PAGE), a
                ld      hl, STAGING & &3FFF | &8000   ; section-C offset
                ld      (BD_SAVE_ADDR), hl
                ld      hl, (STAGE_OFFSET)     ; total bytes received
                ld      (BD_SAVE_SIZE), hl
                call    bdos_fill_save_uifa
                call    bdos_save_hook         ; HSAVE (real B-DOS only)

                ; success: green border, halt (the image is on Trinity storage).
                ld      a, 4
                out     (&fe), a
                di
                halt

cl_write_declined:
                ld      a, 5                        ; cyan border: write declined, no record claimed
                out     (&fe), a
                di
                halt

cl_fail_cfg:
                ld      a, 2                   ; red border: no/bad network settings
                out     (&fe), a
                di
                halt
cl_fail_init:
                ld      a, 1                   ; blue border: ENC28J60 init failed
                out     (&fe), a
                di
                halt
cl_fail_link:
                ld      a, 6                   ; yellow border: PHY link never came up
                out     (&fe), a               ; (cable unplugged, or no link partner)
                di
                halt

; ===========================================================================
; client_fetch_boot — the i122 PXE-style fetch-and-boot entry: read the SAM's
; network identity from EEPROM, TFTP-fetch the configured disk image STRAIGHT
; into a reusable scratch Trinity record (the i122b streaming sink — a full
; 819200-byte image never sits whole in RAM), validate it, and ALHK-boot it.
; No control return on success (the booted record's AUTO file takes over).
;
; The capstone composing the four i122 bricks: i82 (fetch loop) + i122b (raw
; streaming write) + i114c (validate) + i122a (boot-a-record). The scratch record
; (cl_boot_record) is a netboot-owned, reusable slot — we overwrite our own
; scratch each boot, no per-boot cleanup (i122 design note a). An image that
; fails validation (not exactly 819200 bytes, or no BDOS stamp) is rejected and
; NOT booted (magenta border) — corrupt disk-records never boot (design §6.5).
client_fetch_boot:
                call    client_setup           ; EEPROM config + drv_init + link (halts on failure)

                ; --- select the scratch record + arm the streaming sink ---
                ld      a, (cl_boot_record)
                ld      (BD_BOOT_RECORD), a    ; the record to stream into, validate, and boot
                call    bdos_select_record     ; HRECORD-select it (HWSADs target it)
                call    raw_record_sink_reset
                ld      a, 1
                ld      (CLIENT_SINK_MODE), a  ; stream the body into the record

                ; --- fetch (ARP -> RRQ -> DATA/ACK), streaming to the record ---
                call    client_first
cfb_loop:
                call    client_run_once
                ld      a, (XFER_DONE)
                or      a
                jr      z, cfb_loop

                ; --- commit: flush + validate + boot-if-valid (no return on boot) -
                call    client_finalize

                ; reached only if the image was rejected (not a valid disk record).
                ld      a, 3                   ; magenta border: invalid image, not booted
                out     (&fe), a
                di
                halt

; ===========================================================================
; client_finalize — commit the streamed image: flush the sink's final partial
; sector, validate the result as a Trinity disk record (size == 819200 from
; RRS_TOTAL AND the BDOS stamp@232 read back via HRSAD), and — only if valid —
; ALHK-boot the record (BD_BOOT_RECORD; never returns on hardware). On an invalid
; image it sets CLIENT_BOOT_RESULT = 0 and returns so the caller can signal it.
;
; Pre: the scratch record is HRECORD-selected (so HRSAD reads it back) and
;      BD_BOOT_RECORD names it. Out: CLIENT_BOOT_RESULT = 1 iff valid (then it
;      boots and never returns); 0 iff rejected (returns).
client_finalize:
                call    raw_record_sink_finish ; flush any final partial sector

                ; size = RRS_TOTAL -> BD_REC_SIZE (32-bit).
                ld      hl, (RRS_TOTAL)
                ld      (BD_REC_SIZE), hl
                ld      hl, (RRS_TOTAL + 2)
                ld      (BD_REC_SIZE + 2), hl

                ; read sector 0 (track 0, sector 1) back via HRSAD so the validator
                ; can check the BDOS stamp@232 of the just-written record.
                xor     a
                ld      (BD_READ_TRACK), a
                inc     a
                ld      (BD_READ_SECTOR), a    ; sector 1 (1-based)
                call    bdos_read_sector       ; -> BD_READ_BUF (512 bytes)

                call    bdos_validate_disk_record  ; -> BD_REC_VALID (1 = valid)
                ld      a, (BD_REC_VALID)
                or      a
                jr      z, cfin_reject

                ; valid: boot the record (ALHK auto-load + run; never returns on HW).
                ld      a, 1
                ld      (CLIENT_BOOT_RESULT), a
                call    bdos_boot_record       ; HRECORD-select BD_BOOT_RECORD + ALHK
                ret                            ; harness-only: ALHK never returns on hardware
cfin_reject:
                xor     a
                ld      (CLIENT_BOOT_RESULT), a    ; not a valid disk record: do not boot
                ret

; The reusable netboot scratch record — edit for your card. We overwrite OUR OWN
; scratch each boot (i122 note a), so this is not the shared-resource confirm gate
; (that gate is the client_main picker); on real hardware reserve a slot here.
cl_boot_record:   defb 1

cl_chunk_name:    defm "Trinity Network "     ; the flash chunk holding MAC+IP
; The fixed fetch target — edit for your network (the TFTP server + the file).
cl_server_ip:     defb 192, 168, 0, 1         ; the server holding the .mgt image
cl_filename:      defm "recovery.mgt"
                  defb 0
cl_filename_end:

                endif  ; !NETBOOT_HOSTTEST

; ===========================================================================
; CONFIG + transfer state. The harness writes CLIENT_*/SERVER_IP/RRQ_FILENAME/
; CLIENT_BLKSIZE directly; on the bootable build client_main fills them.
; ===========================================================================
CLIENT_MAC:       defs 6
CLIENT_IP:        defs 4
CLIENT_TID:       defs 2                 ; our source port (big-endian)
CLIENT_BLKSIZE:   defs 2                 ; the block size we requested

SERVER_IP:        defs 4                 ; the TFTP server we fetch from
SERVER_MAC:       defs 6                 ; learned (ARP reply, or first DATA)
SERVER_TID:       defs 2                 ; learned from the first DATA (big-endian)
GOT_MAC:          defs 1
GOT_SERVER:       defs 1

PHASE:            defs 1                 ; PHASE_ARP / PHASE_XFER

REPLY_MAC:        defs 6                 ; dst of a reply (server or stray)
REPLY_IP:         defs 4
REPLY_TID:        defs 2

ACKED:            defs 2                 ; highest block ACKed (LE value)
ACK_BLOCK:        defs 2                 ; the block the next ACK names (LE value)
STAGE_OFFSET:     defs 2                 ; bytes accumulated so far (mode 0 / STAGING)
LAST_DATA_LEN:    defs 2
XFER_DONE:        defs 1
CLIENT_SINK_MODE: defs 1                 ; 0 = STAGING (default), 1 = raw-record streaming sink (i122c)
CLIENT_BOOT_RESULT: defs 1               ; client_finalize: 1 = valid image booted, 0 = rejected
CL_RX_LEN:        defs 2
TFTP_PKT_LEN:     defs 2
TPKT:             defs 64                ; the ACK/ERROR packet buffer

RRQ_FILENAME:     defs 128              ; the NUL-terminated filename to fetch
RXBUF:            defs 1518
; STAGING holds the accumulated file bytes. Its size differs by build target —
; the accumulation logic is identical, only the buffer capacity changes:
;   - The host harness (NETBOOT_HOSTTEST) is a flat 64 KB Z80, so it gets a
;     generous buffer that comfortably holds the multi-block test file
;     (netboot_client_test.go stages 2 full 1428-byte blocks + a tail).
;   - The bootable build keeps this buffer small (2 KB). Section D (&C000-&FFFF)
;     is RAM at boot (LOAD CODE 32768 deposits there; ROM1 off at run — proven by
;     the section-D loadability probe, see docs/notes/sam-paging.md), and the image
;     does spill into it for the i145b CSD overlay; but a full 16 KB staging buffer
;     would still bloat the boot image needlessly. Real large-file staging belongs
;     in paged RAM (i119); the boot fit-check guards the &10000 ceiling.
                if defined(NETBOOT_HOSTTEST)
STAGING:          defs 16384
                else
STAGING:          defs 2048
                endif

; ===========================================================================
; The host-verified packet builders/parsers + the storage seam, composed into
; this one translation unit (their org is suppressed — no NETBOOT_STANDALONE — so
; this file's org governs). encdrv.asm is the real vendored ENC28J60 driver;
; eeprom.asm is the real flash config reader (real-hardware path only).
; ===========================================================================
                include "build_udp_frame.asm"
                include "build_arp_request.asm"
                include "tftp_client.asm"
                include "bdos_seam.asm"
                include "encdrv.asm"
                include "enc_link.asm"         ; drv_wait_link (PHY link-up, i127)
                include "key_read_test.asm"    ; i138: keyboard sysvar poll (KYIP2 inlined)
                if defined(NETBOOT_HOSTTEST)==0
                include "bdos_picker.asm"      ; i119d: record-selection UX (B4)
                include "eeprom.asm"
                include "raw_record_sink.asm"  ; i122b: streaming disk-image -> raw record (HWSAD)
                ; i145b CSD-read -> BD_RECORDS (i145b-b2): shipped as a section-D
                ; overlay (see the call site in client_main). The ~600-byte module
                ; places the boot image's tail above &BFFF into section D (RAM at
                ; boot). Verified standalone by csd_to_bd_records_test.go.
                include "sd_csd.asm"
                endif
