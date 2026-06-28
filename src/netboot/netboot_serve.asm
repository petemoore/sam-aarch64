; netboot_serve.asm — the i96 serve-files TFTP demo server: a focused, bootable
; "serve a few files to a plain TFTP client" program. Distinct from the i95
; integrated Pi-netboot server: NO DHCP and NO Pi PXE option-43 blob — just ARP +
; TFTP — so it is testable from any machine with a stock `tftp` / `curl` client
; (no Raspberry Pi, no DHCP server, no option negotiation required). Boot it on a
; SAM + Quazar Trinity, then from any LAN machine `tftp <sam-ip>` + `get hello.txt`
; or `curl tftp://<sam-ip>/hello.txt`.
;
; This is the SAM-side Z80 port of the netboot-oracle Go authority
; tools/netboot-oracle/serve/serve.go::Responder.OnFrame. Like netboot_server.asm
; it is a NEW state machine — it does NOT `include` the standalone loop files
; (their RXBUF / CONFIG_* / build_udp_frame / encdrv collide); it composes the
; host-verified packet builders/parsers directly (build_udp_frame, build_arp_reply,
; tftp_build, tftp_parse) + the real vendored driver (encdrv.asm).
;
;   serve_serve_once:
;     drv_read -> a received Ethernet frame (or nothing)
;     dispatch (mirrors Responder.OnFrame, in this order):
;       1. ARP request for our IP   -> an ARP reply   (build_arp_reply) — lets a
;          plain client resolve the SAM's MAC with no DHCP.
;       2. not IPv4/UDP             -> ignore
;       3. UDP dst 69 (TFTP RRQ)    -> serve-by-name:
;            * hit + options requested -> an OACK (arm the OACK->ACK0->DATA1 path)
;            * hit + NO options (a bare RRQ, RFC 2347) -> DATA block 1 directly,
;              at the 512-byte default, no OACK
;            * miss                    -> ERROR(1) and keep serving
;       4. UDP dst = our transfer TID (TFTP ACK) -> FirstData (ack 0, OACK path)
;          or the next DATA (advance), or nothing at the end
;       5. anything else            -> ignore
;     drv_write the chosen reply (or nothing).
;
; The bare-RRQ -> DATA-block-1 branch is the one behaviour beyond the i95 server:
; a classic `tftp get` sends no options, and RFC 2347 requires the server to omit
; the OACK and stream DATA straight away. An RRQ that *does* request options (e.g.
; curl's tsize) takes the OACK path, byte-identical to the i95 server.
;
; PROVENANCE: the dispatch is serve.go::Responder.OnFrame; the ARP bring-up is
; smoke.go; the serve-by-name resolve + ERROR(1)-keep-serving + the OACK/streamed-
; DATA cadence + short-final-block termination is tftp/serverloop.go (the bare-RRQ
; path is ServerLoop.StartTransfer with sendOACK=false). Wire formats: ARP (RFC
; 826), TFTP (RFC 1350 + 2347 OACK). Framing matches trinload's `packet` buffer.
;
; VERIFICATION: serve_serve_once is host-verifiable end-to-end under the i80
; emulation — inject ARP / bare-RRQ / optioned-RRQ / DATA-ACK sessions and assert
; every reply frame on the virtual wire matches serve.Responder.OnFrame
; byte-for-byte (netboot_serve_test.go). NOT host-verifiable: the real ENC28J60
; silicon and an end-to-end run on real hardware — gated on real Trinity
; (CLAUDE.md §5). Emulation-verified is not hardware-verified.

                ; When the dumper (netboot_dumper.asm) includes this file it owns
                ; the org + the boot entry + its own main; this file then supplies
                ; only the shared serve state machine (serve_serve_once + helpers +
                ; CONFIG/STORE/SRC_TABLE). DUMPER also arms the rrq_hit refresh hook.
                if defined(DUMPER)==0
                org     &8000

                ; The boot entry (CALL 32768) must be the first instruction at
                ; &8000. serve_main is defined later (under NETBOOT_HOSTTEST==0);
                ; the host harness invokes routines by symbol and never CALLs
                ; 32768, so this jp is bootable-only.
                if defined(NETBOOT_HOSTTEST)==0
                jp      serve_main
                endif
                endif

; ===========================================================================
; Frame field offsets in the received frame (RX_ prefix so they don't clash with
; the included primitives' OFF_*/AR_OFF_*).
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

TFTP_PORT:        equ 69                 ; the SAM's TFTP server port
ACTION_OACK_:     equ 0                  ; mirror tftp.ActionOACK / RESOLVE_ACTION

; ===========================================================================
; serve_serve_once — read one frame and dispatch it, transmitting the chosen
; reply (or nothing). Mirrors Responder.OnFrame.
;
; In:  the emulated Trinity is attached; CONFIG_* + the store + the source table
;      are filled; drv_init has run.
; Out: BC = bytes transmitted (the reply frame length), or 0 if ignored.
; ===========================================================================
serve_serve_once:
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
                ld      a, (RXBUF + RX_UDP_DSTPORT)        ; high
                ld      h, a
                ld      a, (RXBUF + RX_UDP_DSTPORT + 1)    ; low
                ld      l, a                              ; HL = dst port

                ; === 3. UDP dst 69 -> TFTP RRQ ========================
                ld      a, h
                or      a
                jr      nz, ns_check_tid       ; high byte != 0: not 69
                ld      a, l
                cp      TFTP_PORT
                jp      z, handle_rrq

ns_check_tid:
                ; === 4. UDP dst == our transfer TID ====================
                ld      a, (CONFIG_SERVERTID)             ; high
                cp      h
                jp      nz, ns_none
                ld      a, (CONFIG_SERVERTID + 1)         ; low
                cp      l
                jp      nz, ns_none

                ; A frame on our transfer TID. During a WRQ receive a DATA(3)
                ; frame is the pushed file's next block (accumulate + ACK); an
                ; ACK(4) advances an RRQ serve. Mirror serve.go OnFrame: route a
                ; DATA to recvData when a WRQ receive is armed.
                ld      a, (WRQ_RECV_ACTIVE)
                or      a
                jp      z, handle_ack          ; no WRQ receive: the RRQ ACK path
                ld      a, (RXBUF + RX_UDP_PAYLOAD)
                or      a
                jp      nz, handle_ack         ; opcode high != 0
                ld      a, (RXBUF + RX_UDP_PAYLOAD + 1)
                cp      OP_DATA
                jp      z, handle_data
                jp      handle_ack

ns_none:
                ld      bc, 0
                ret

; ===========================================================================
; 1. ARP — answer a who-has for our IP with an ARP reply (smoke.go).
; ===========================================================================
; try_arp — if RXBUF is a well-formed ARP request for CONFIG_SERVERIP, build +
; transmit the ARP reply (BC = sent length); else return BC = 0.
; arp_no (reject) sits first so every accept-check's `jr nz` is a short backward
; branch.
arp_no:
                ld      bc, 0
                ret
try_arp:
                ld      a, (RXBUF + RX_ETHERTYPE)
                cp      ETYPE_ARP >> 8
                jr      nz, arp_no
                ld      a, (RXBUF + RX_ETHERTYPE + 1)
                cp      ETYPE_ARP & &ff
                jr      nz, arp_no
                ld      a, (RXBUF + RX_ARP_HTYPE)
                or      a
                jr      nz, arp_no
                ld      a, (RXBUF + RX_ARP_HTYPE + 1)
                cp      ARP_HTYPE_ETH
                jr      nz, arp_no
                ld      a, (RXBUF + RX_ARP_PTYPE)
                cp      ARP_PTYPE_IPV4 >> 8
                jr      nz, arp_no
                ld      a, (RXBUF + RX_ARP_PTYPE + 1)
                cp      ARP_PTYPE_IPV4 & &ff
                jr      nz, arp_no
                ld      a, (RXBUF + RX_ARP_HLEN)
                cp      ARP_HLEN_
                jr      nz, arp_no
                ld      a, (RXBUF + RX_ARP_PLEN)
                cp      ARP_PLEN_
                jr      nz, arp_no
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
                ld      hl, CONFIG_SERVERMAC
                ld      de, AR_SRC_MAC
                ld      bc, 6
                ldir
                ld      hl, CONFIG_SERVERIP
                ld      de, AR_SRC_IP
                ld      bc, 4
                ldir
                ld      hl, RXBUF + RX_ARP_SHA
                ld      de, AR_DST_MAC
                ld      bc, 6
                ldir
                ld      hl, RXBUF + RX_ARP_SPA
                ld      de, AR_DST_IP
                ld      bc, 4
                ldir

                call    build_arp_reply        ; frame at AR_PACKET, BC = length
                push    bc
                ld      hl, AR_PACKET
                call    drv_write
                pop     bc
                ret

; ===========================================================================
; 3. TFTP RRQ — serve-by-name. OACK on a hit with options, DATA block 1 directly
; on a hit with NO options (RFC 2347), ERROR(1) on a miss. Mirrors
; serverloop.StartTransfer (sendOACK chosen by PARSE_OPT_COUNT).
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

                ; WRQ (opcode 2)? learn the client endpoint and reply.
                ; Port of serve.go::Responder.startWrite (i121a).
                ld      a, (PARSE_OPCODE+1)    ; low byte of big-endian opcode
                cp      OP_WRQ
                jp      z, handle_wrq

                ; resolve the filename (RRQ path)
                ld      hl, (PARSE_FILENAME)
                ld      (RESOLVE_NAME_PTR), hl
                call    resolve
                ld      a, (RESOLVE_ACTION)
                cp      ACTION_OACK_
                jr      z, rrq_hit

                ; --- miss: ERROR(1, "File not found") ------------------
                xor     a
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
                ; install the source for the resolved name + its size.
                call    resolve_src            ; sets SRC_PTR + XFER_SIZE; CY=hit
                ; (resolve always matched in STORE, so SRC_TABLE has it too.)

                ; The dumper hook: stage the resolved region's bytes into the
                ; buffer SRC_PTR/XFER_SIZE point at (and, for rom1.bin, override
                ; SRC_PTR to a section-A scratch page) before the stream begins.
                ; Inert in the standalone serve build (the call is omitted), so the
                ; existing serve program + its tests are unaffected.
                if defined(DUMPER)
                call    dumper_refresh_region
                endif

                ; arm the transfer
                ld      hl, 1
                ld      (XFER_NEXT_BLK), hl
                ld      hl, 0
                ld      (XFER_OFFSET), hl
                ld      (XFER_OFFSET+2), hl
                ld      a, 1
                ld      (XFER_ACTIVE), a

                ; bare RRQ (no options)? RFC 2347: stream DATA block 1 directly.
                ld      bc, (PARSE_OPT_COUNT)
                ld      a, b
                or      c
                jr      nz, rrq_oack
                ; --- no options: blksize 512, no OACK, send DATA 1 ------
                ld      hl, 512
                ld      (XFER_BLKSIZE), hl
                xor     a
                ld      (XFER_JUST_OACKED), a  ; not the OACK path
                jp      send_next_data

rrq_oack:
                ; --- options requested: OACK, then ACK0 -> FirstData ----
                ld      a, 1
                ld      (XFER_JUST_OACKED), a  ; the ACK of block 0 -> FirstData
                call    negotiate_blksize      ; -> XFER_BLKSIZE
                call    build_oack_opts
                ld      (OACK_OPTS_LEN), hl
                call    build_oack             ; packet at TBUF, BC = length
                jp      srv_send_tbuf

; ===========================================================================
; WRQ handler. Port of serve.go::Responder.startWrite.
;
; A WRQ arrives on UDP dst 69 (same port as RRQ). The server learns the client
; endpoint (MAC/IP/TID) and replies:
;   - bare WRQ (no options): ACK-0 (`00 04 00 00`)    — tftp.BuildACK(0)
;   - optioned WRQ:          OACK echoing blksize      — same build_oack path
;
; The WRQ filename's "trinity-sam-disks/" prefix picks the storage class (manifest
; design §6.5; bdos_strip_disk_prefix is the bdos.Classify port):
;   - prefixed -> DiskRecord (i121f): claim a FREE Trinity record before the handshake
;     and stream the pushed .mgt straight into it (raw 512-byte sectors, the i122 store
;     machinery), validated on the final block as an exactly-819200-byte "BDOS"-stamped
;     disk image (wd_finalize).
;   - NOT prefixed -> FlatFile (i121c, the DEFAULT): flat-accumulate into WRQ_STAGING
;     and HSAVE the whole file into the claimed record on the final block
;     (wd_finalize_flat) — no size validation; a flat file is any size up to the
;     staging window.
; Both classes claim a free record; if none is free the server replies ERROR(3, "no
; free record") and does NOT arm the receiver — never touching a named record (the
; shared-resource invariant, memory/trinity_storage_shared_resource).
;
; The claim + both finalizes live behind NETBOOT_HOSTTEST==0 (they need the
; bdos_seam.asm RST 8 hooks + raw_record_sink.asm, present only in the bootable
; build). The HOSTTEST build (the i121a/i121b wire tests) leaves WRQ_SINK_MODE clear
; and just accumulates into WRQ_STAGING, ACKing the final block (no store), unchanged.
; ===========================================================================
handle_wrq:
                if defined(NETBOOT_DEBUG)
                ld      a, DBG_WRQ_ENTRY
                call    dbg_marker
                endif
                ; learn the client endpoint (same pattern as handle_rrq).
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

                ; --- reserved control name `tftp.done`: end the session, hand control
                ; back to trinload (manifest design decision 6; i121e). Checked FIRST,
                ; before any free-record find / claim / arm, so the control name is
                ; never stored. On match: set XFER_STOP_REQUESTED (sv_serve_loop polls
                ; it and RETs to trinload's pushed return addr) and send a courtesy
                ; ACK-0 so a stock `tftp put tftp.done` sees the handshake accepted.
                ; The empty push's subsequent 0-byte DATA may time out on the client
                ; after we exit — acceptable; the point is the RET back to trinload.
                ld      hl, (PARSE_FILENAME)
                ld      de, sv_done_name
                call    streq_cstr             ; CY set if the filename == "tftp.done"
                jr      nc, wrq_not_done
                ld      a, 1
                ld      (XFER_STOP_REQUESTED), a
                if defined(NETBOOT_DEBUG)
                ld      a, DBG_DONE_CTRL
                call    dbg_marker
                endif
                call    build_ack0             ; courtesy ACK-0 (packet at TBUF, BC = 4)
                jp      srv_send_tbuf          ; reply, then return; no claim, no arm
wrq_not_done:

                ; --- storage-class + free-record setup (bootable build): classify the
                ; WRQ filename, claim a free record (both classes need one), then arm
                ; the per-class payload sink. No free record -> ERROR(3), no handshake. ---
                xor     a
                ld      (WRQ_SINK_MODE), a     ; default: flat accumulate (HOSTTEST path)
                if (defined(NETBOOT_HOSTTEST)==0) * (defined(DUMPER)==0)
                ; classify: a "trinity-sam-disks/" prefix opts into the validated
                ; disk-record class; any other name is the DEFAULT flat-file archive
                ; (design §6.5; bdos_strip_disk_prefix is the bdos.Classify port).
                ld      a, 1
                ld      (WRQ_FLAT_MODE), a     ; assume flat-file (the default class)
                ld      hl, (PARSE_FILENAME)
                call    bdos_strip_disk_prefix ; CY set if the prefix is present
                jr      nc, wrq_class_done     ; no prefix -> flat-file (WRQ_FLAT_MODE=1)
                xor     a
                ld      (WRQ_FLAT_MODE), a     ; prefixed -> disk-record push
wrq_class_done:
                call    wrq_claim_record       ; CY set = a free record was claimed
                jr      c, wrq_claimed         ; claimed: arm the sink, re-arm ENC, handshake
                ; no free record: ERROR(3, "no free record"), do not arm. The finder's
                ; CMD17 list reads disturbed the ENC; re-arm before the ERROR reply TX.
                call    serve_rearm_enc
                if defined(NETBOOT_DEBUG)
                ld      a, DBG_WRQ_NOFREE
                call    dbg_marker
                endif
                xor     a
                ld      (ERR_CODE), a
                ld      a, 3
                ld      (ERR_CODE+1), a        ; big-endian code = 0x0003
                ld      hl, err_nofree_msg
                ld      (ERR_MSG_PTR), hl
                call    build_error            ; packet at TBUF, BC = length
                jp      srv_send_tbuf
wrq_claimed:
                ; arm the per-block payload sink for the classified storage class:
                ;   flat-file  -> WRQ_SINK_MODE=0, flat-accumulate into WRQ_STAGING
                ;                 (HSAVE'd whole by wd_finalize_flat on the final block).
                ;   disk-record -> reset the raw sink + WRQ_SINK_MODE=1, stream each
                ;                 block into the record's sectors (wd_finalize validates).
                ld      a, (WRQ_FLAT_MODE)
                or      a
                jr      nz, wrq_arm_flat
                call    raw_record_sink_reset  ; RRS_FILL/LINEAR/TOTAL = 0
                ld      a, 1
                ld      (WRQ_SINK_MODE), a     ; handle_data streams into the record
                jr      wrq_armed
wrq_arm_flat:
                xor     a
                ld      (WRQ_SINK_MODE), a     ; handle_data flat-accumulates into WRQ_STAGING
wrq_armed:
                ; The claim's free-record finder did CMD17 SD list reads (and selected
                ; the record), each disturbing the shared one-PIC Trinity controller's
                ; ENC RX state. Re-arm before the handshake reply's ENC TX (i244).
                call    serve_rearm_enc
                if defined(NETBOOT_DEBUG)
                ld      a, DBG_WRQ_CLAIMED
                call    dbg_marker
                endif
                endif

wrq_handshake:
                if defined(NETBOOT_DEBUG)
                ld      a, DBG_WRQ_HANDSHAKE
                call    dbg_marker
                endif
                ; bare WRQ? (no options: PARSE_OPT_COUNT == 0)
                ld      bc, (PARSE_OPT_COUNT)
                ld      a, b
                or      c
                jr      nz, wrq_oack

                ; --- bare WRQ: arm the receiver at blksize 512, reply ACK-0 ---
                ld      hl, 512
                ld      (WRQ_BLKSIZE), hl
                call    wrq_arm_receiver       ; WRQ_RECV_ACTIVE=1, ACKED/OFFSET=0
                call    build_ack0             ; packet at TBUF, BC = 4
                jp      srv_send_tbuf

wrq_oack:
                ; --- optioned WRQ: OACK echoing blksize (and tsize if present).
                ; Reuse negotiate_blksize -> XFER_BLKSIZE, then build_oack_opts
                ; formats "blksize\0<n>\0" (and optionally "tsize\0<ts>\0") into
                ; OACK_OPTS. Port of serve.go::Responder.startWrite optioned path.
                call    negotiate_blksize      ; -> XFER_BLKSIZE
                ld      hl, (XFER_BLKSIZE)     ; arm the receiver at the negotiated
                ld      (WRQ_BLKSIZE), hl      ; block size
                call    wrq_arm_receiver
                call    build_oack_opts_wrq    ; -> OACK_OPTS, HL = byte count
                ld      (OACK_OPTS_LEN), hl
                call    build_oack             ; packet at TBUF, BC = length
                jp      srv_send_tbuf

; wrq_arm_receiver — arm the WRQ receive-to-staging loop: mark it active and reset
; the per-transfer state (ACKED = 0 so the first expected block is 1, the staging
; offset back to the start, the short-block flag clear). WRQ_BLKSIZE must already
; be set by the caller. Mirrors serve.go::startWrite arming r.wrqRecv.
wrq_arm_receiver:
                ld      hl, 0
                ld      (WRQ_ACKED), hl
                ld      (WRQ_STAGE_OFFSET), hl
                xor     a
                ld      (WRQ_DONE), a
                ld      a, 1
                ld      (WRQ_RECV_ACTIVE), a
                ret

                if (defined(NETBOOT_HOSTTEST)==0) * (defined(DUMPER)==0)
; serve_rearm_enc — re-arm the ENC28J60 RX path after an SD transaction on the
; shared one-PIC Trinity controller (i244). The WRQ disk-record push drives SD I/O
; (the free-record finder's CMD17 list reads, the per-block HWSAD sink writes, the
; finalize HRSAD read + CMD17/CMD24 claim); each disturbs the ENC's persistent RX
; state (RXEN, ring pointers, MAC/PHY), and drv_read restores only per-call
; select/bank — never that once-only arming. enc_rx_reestablish (encdrv.asm) is
; drv_init's RX-arm body MINUS chk_trinity (which would fail right after the &38
; SD-init — i242), so it restores serving without re-probing the settling PIC.
; Bracketed di/ei like probe_main's startup re-arm. Clobbers AF/BC/DE/HL.
serve_rearm_enc:
                di
                xor     a
                ld      (enc_timed_out), a     ; fresh budget for this re-arm (i280b-b2c)
                ld      hl, CONFIG_SERVERMAC
                call    enc_rx_reestablish
                ei
                if defined(NETBOOT_DEBUG)
                ; i280b-b2c: the bound in wait_ready means a stuck shared bus (the §8d
                ; ENC/SD contention) now RETURNS here instead of wedging; report it via
                ; a non-perturbing post-window marker so a hardware shot sees the re-arm
                ; timed out (rather than the serve silently dying). The serve stays alive
                ; and trinload-recoverable either way.
                ld      a, (enc_timed_out)
                or      a
                ret     z
                ld      a, DBG_REARM_TIMEOUT
                call    dbg_marker
                endif
                ret

; wrq_claim_record — claim a FREE Trinity record for the push. Pick the record per
; the configured placement STRATEGY (bdos_find_record_for_strategy reads
; SERVE_CFG_STRATEGY: highest-free by default — manifest design §4 s3 / decision 4;
; lowest-free or an explicit record when patched) and HRECORD-select it. The pick
; never touches a named record (free ⇔ the record-list name byte is 0). BD_RECORDS
; (the card record count) is the CSD-derived value (hardware-gated i145; the
; emulation E2E injects it) — 0 records ⇒ none free. The per-class sink arm (raw sink
; for DiskRecord, flat accumulate for FlatFile) is the CALLER's job (handle_wrq), so
; both storage classes share this find+select.
;
; Out: CY set + WRQ_RECORD set + the record selected, if a record was placed; CY
;      clear if none is available (no free record, or the explicit record is named).
; Mirrors client_fetch_boot's select.
wrq_claim_record:
                if defined(NETBOOT_DEBUG)
                ; i280b-b2: split the claim into its two SD steps so a hardware shot
                ; pins which hangs. FIND_PRE→SELECT_PRE gap = bdos_find_record_for_strategy
                ; (our CMD17 list reads); SELECT_PRE→SELECT_POST gap = bdos_select_record
                ; (the B-DOS HRECORD hook). Both are at SD-transaction boundaries (no CS
                ; asserted), the sanctioned dbg_marker call sites.
                ld      a, DBG_CLAIM_FIND_PRE
                call    dbg_marker
                endif
                call    bdos_find_record_for_strategy  ; -> BD_FREE_RECORD (1-based, 0 = none)
                ld      a, (BD_FREE_RECORD)
                ld      b, a
                ld      a, (BD_FREE_RECORD+1)
                or      b
                jr      z, wrq_no_free         ; BD_FREE_RECORD == 0: nothing free
                ld      a, (BD_FREE_RECORD)    ; low byte = the record number (>=1)
                ld      (WRQ_RECORD), a
                if defined(NETBOOT_DEBUG)
                ld      a, DBG_CLAIM_SELECT_PRE
                call    dbg_marker
                endif
                call    bdos_select_record     ; HRECORD-select it (HWSADs target it)
                if defined(NETBOOT_DEBUG)
                ld      a, DBG_CLAIM_SELECT_POST
                call    dbg_marker             ; preserves flags; the scf below still sets CY
                endif
                scf
                ret
wrq_no_free:
                or      a                      ; clear carry: no free record
                ret

; bdos_find_record_for_strategy — pick the record a WRQ disk-record push lands in,
; per the placement STRATEGY in the serve config block (SERVE_CFG_STRATEGY, below).
; This is the strategy-aware entry the serve WRQ path uses; bdos_find_free_record
; keeps its lowest-first semantics for the i119/i122 client paths that depend on it.
; It lives here (not in bdos_seam.asm) because it reads the serve config block; the
; seam supplies only the strategy-agnostic finders it dispatches to.
;
; Reads SERVE_CFG_STRATEGY straight from RAM at find-time — cheap, and the config is
; set at launch and not re-read from disk (Pete, i121h). Dispatch:
;   0 (SERVE_STRAT_HIGHEST, the default) -> bdos_find_highest_free_record
;       (scan BD_RECORDS..1, the highest free record; manifest design §4 s3 /
;        decision 4: TFTP storage grows down from the top).
;   1 (SERVE_STRAT_LOWEST)               -> bdos_find_free_record
;       (scan 1..BD_RECORDS, the lowest free record — the existing behaviour).
;   2 (SERVE_STRAT_EXPLICIT)             -> the record at SERVE_CFG_RECORD if it is
;       free; else BD_FREE_RECORD = 0 (-> the caller's no-record ERROR(3) path).
;   any other value                      -> treated as the default (highest-free).
;
; In:  BD_RECORDS  2 bytes  total record count (>= 1)
;      the serve config block (SERVE_CFG_STRATEGY / SERVE_CFG_RECORD)
; Out: BD_FREE_RECORD  2 bytes  the 1-based record to place into, or 0 if none is
;                               available (no free record, or the explicit record is
;                               already named).
; Clobbers: A, BC, DE, HL.
;
; Mirrors the Go authority serve.Responder.nextRecordForStrategy (serve.go).
bdos_find_record_for_strategy:
                ld      a, (SERVE_CFG_STRATEGY)
                cp      SERVE_STRAT_LOWEST
                jp      z, bdos_find_free_record        ; 1: lowest-free (existing)
                cp      SERVE_STRAT_EXPLICIT
                jr      z, bfrs_explicit                ; 2: a named explicit record
                ; 0 or any unrecognised value: the baked default (highest-free).
                jp      bdos_find_highest_free_record

bfrs_explicit:
                ; The explicit record is SERVE_CFG_RECORD. Default BD_FREE_RECORD = 0
                ; (none) so every reject path below just returns.
                xor     a
                ld      (BD_FREE_RECORD), a
                ld      (BD_FREE_RECORD + 1), a

                ld      hl, (SERVE_CFG_RECORD)
                ; undefined (&FFFF, the i121j prompt placeholder)? -> none.
                ld      a, h
                and     l
                inc     a
                jr      z, bfrs_done                    ; HL == &FFFF: undefined
                ; zero record number is not valid (records are 1-based) -> none.
                ld      a, h
                or      l
                jr      z, bfrs_done

                ; read the explicit record's list entry and apply the free test.
                ld      (BD_ENTRY_REC), hl
                call    bdos_record_entry
                ld      a, (BD_ENTRY_BUF)
                and     &7F                             ; strip write-protect bit
                jr      nz, bfrs_done                   ; named ⇒ not available -> 0

                ; free: place at the explicit record.
                ld      hl, (SERVE_CFG_RECORD)
                ld      (BD_FREE_RECORD), hl
bfrs_done:
                ret
                endif

; ===========================================================================
; 4. TFTP ACK — on our transfer TID: FirstData (ack 0, OACK path) or the next
; DATA (advance), or nothing at the end. Mirrors serverloop.FirstData/OnACK + the
; Responder.OnFrame justFirst split.
; ===========================================================================
handle_ack:
                ld      a, (XFER_ACTIVE)
                or      a
                jp      z, ns_none

                ; justOACKed? the client's ACK of block 0 starts the data flow.
                ld      a, (XFER_JUST_OACKED)
                or      a
                jr      z, ack_advance_path
                xor     a
                ld      (XFER_JUST_OACKED), a
                jp      send_next_data

ack_advance_path:
                ; UDP source port == the client TID? (ignore a stray ACK)
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

; send_next_data — read the next block from SRC_PTR+offset, build the DATA packet,
; wrap + transmit. Updates XFER_OFFSET and XFER_LAST_SHORT.
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

; ===========================================================================
; WRQ DATA — accumulate one received DATA block into STAGING and ACK it. Reached
; from ns_check_tid when a WRQ receive is armed and the frame on our transfer TID
; carries opcode DATA(3). Port of serve.go::Responder.recvData, which mirrors the
; i82 receive side (tftp.ClientXfer.OnData / tftp_client_loop.asm::tftp_recv_data):
; the block-check (acked+1 store, <= acked re-ACK, future ignore), the ldir into
; STAGING, the STAGE_OFFSET advance, and the short-final-block end. The peer of a
; WRQ is the client (learned by handle_wrq), so the ACK wraps back to it.
; Out: BC = bytes transmitted (the ACK frame), or 0 if nothing was sent.
; ===========================================================================
handle_data:
                ; UDP source port == the WRQ client TID? (ignore a stray sender)
                ld      a, (RXBUF + RX_UDP_SRCPORT)
                ld      hl, CLIENT_TID
                cp      (hl)
                jp      nz, ns_none
                ld      a, (RXBUF + RX_UDP_SRCPORT + 1)
                inc     hl
                cp      (hl)
                jp      nz, ns_none

                ; block number (big-endian) -> DE
                ld      a, (RXBUF + RX_UDP_PAYLOAD + 2)
                ld      d, a
                ld      a, (RXBUF + RX_UDP_PAYLOAD + 3)
                ld      e, a                   ; DE = block

                ; expected = WRQ_ACKED + 1.
                ld      hl, (WRQ_ACKED)
                inc     hl                     ; HL = acked + 1
                or      a
                sbc     hl, de                 ; (acked+1) - block
                jr      z, wd_next_block       ; block == acked+1: the next block
                ; block <= acked => duplicate (re-ACK); block > acked+1 => future.
                ld      hl, (WRQ_ACKED)
                or      a
                sbc     hl, de                 ; acked - block
                jp      c, ns_none             ; acked < block (future, not +1): ignore
                ; duplicate: re-ACK the received block (DE) without storing.
                jp      wd_send_ack            ; ACK block = DE (the received block)

wd_next_block:
                if defined(NETBOOT_DEBUG)
                ld      a, DBG_DATA_BLOCK
                call    dbg_marker
                endif
                ; payload length = frame length - (42 + 4 TFTP header)
                ld      hl, (RX_LEN)
                ld      de, RX_UDP_PAYLOAD + 4
                or      a
                sbc     hl, de                 ; HL = data length
                ld      (WRQ_DATA_LEN), hl

                ; route the payload: the flat WRQ_STAGING accumulate (sink mode 0 —
                ; the i121b host path, now also the i121c FlatFile HSAVE staging)
                ; or the i121f raw-record streaming sink (sink mode 1 — re-block into
                ; 512-byte sectors + HWSAD each into the claimed record, so a full
                ; 819200-byte disk image never sits whole in RAM). WRQ_SINK_MODE
                ; selects; only the bootable build has the sink. Mirrors
                ; netboot_client.asm cl_sink_payload.
                if (defined(NETBOOT_HOSTTEST)==0) * (defined(DUMPER)==0)
                ld      a, (WRQ_SINK_MODE)
                or      a
                jr      nz, wd_sink_payload

                ; mode 0 in the bootable build is the FlatFile path (disk-record is
                ; mode 1, handled above). Bound the flat staging buffer: if this block
                ; would push WRQ_STAGE_OFFSET past WRQ_STAGING_CAP, reply ERROR(3,
                ; "flat file too large") and disarm — never overflow WRQ_STAGING into
                ; the stack (no silent truncation, i253). The HSAVE-of-RAM design caps a
                ; flat file at the free section-D window (~14 KB); a bigger archive needs
                ; the deferred record-spanning store (manifest §3), not this v1 path.
                ld      hl, (WRQ_STAGE_OFFSET)
                ld      de, (WRQ_DATA_LEN)
                add     hl, de                 ; HL = staging offset after this block
                ld      de, WRQ_STAGING_CAP
                or      a
                sbc     hl, de                 ; (offset+len) - cap
                jr      c, wd_flat_fits        ; under cap: accept
                jr      z, wd_flat_fits        ; exactly cap: accept
                ; over cap: disarm the receiver, ERROR(3) in place of the ACK. The
                ; ENC was disturbed by no SD op here, but re-arm for symmetry with the
                ; other reply paths (cheap, and keeps the serve loop's RX armed).
                xor     a
                ld      (WRQ_RECV_ACTIVE), a
                call    serve_rearm_enc
                xor     a
                ld      (ERR_CODE), a
                ld      a, 3
                ld      (ERR_CODE+1), a        ; big-endian code = 0x0003
                ld      hl, err_toobig_msg
                ld      (ERR_MSG_PTR), hl
                call    build_error            ; packet at TBUF, BC = length
                jp      srv_send_tbuf
wd_flat_fits:
                endif

                ; --- mode 0: accumulate the payload into WRQ_STAGING at the offset ---
                ld      hl, (WRQ_STAGE_OFFSET)
                ld      de, WRQ_STAGING
                add     hl, de                 ; HL = dest
                ex      de, hl                 ; DE = dest
                ld      hl, RXBUF + RX_UDP_PAYLOAD + 4   ; src = payload data
                ld      bc, (WRQ_DATA_LEN)
                ld      a, b
                or      c
                jr      z, wd_no_copy          ; zero-length block: nothing to copy
                ldir
wd_no_copy:
                ; WRQ_STAGE_OFFSET += data length
                ld      hl, (WRQ_STAGE_OFFSET)
                ld      de, (WRQ_DATA_LEN)
                add     hl, de
                ld      (WRQ_STAGE_OFFSET), hl
                jr      wd_after_payload

                if (defined(NETBOOT_HOSTTEST)==0) * (defined(DUMPER)==0)
wd_sink_payload:
                ; --- mode 1: stream the payload into the claimed record. The sink
                ; re-blocks into sectors + HWSADs each full one and tracks the 32-bit
                ; image size in RRS_TOTAL (the size authority wd_finalize validates;
                ; WRQ_STAGE_OFFSET is left alone — its 16 bits would overflow a full
                ; record). ---
                ld      hl, RXBUF + RX_UDP_PAYLOAD + 4
                ld      bc, (WRQ_DATA_LEN)
                ld      a, b
                or      c
                jr      z, wd_after_payload    ; zero-length block: nothing to sink
                call    raw_record_sink_leaf
                endif
wd_after_payload:

                ; WRQ_ACKED = block (re-read from the frame to assemble the LE value).
                ld      a, (RXBUF + RX_UDP_PAYLOAD + 2)
                ld      d, a                   ; high byte
                ld      a, (RXBUF + RX_UDP_PAYLOAD + 3)
                ld      e, a                   ; low byte
                ld      (WRQ_ACKED), de        ; ACKED = the just-accepted block

                ; done if data length < WRQ_BLKSIZE.
                ld      hl, (WRQ_DATA_LEN)
                ld      de, (WRQ_BLKSIZE)
                or      a
                sbc     hl, de                 ; datalen - blksize
                jr      nc, wd_send_ack_de     ; datalen >= blksize: not the last
                ld      a, 1
                ld      (WRQ_DONE), a
                ; final block — commit per storage class (bootable build):
                ;   disk-record (WRQ_SINK_MODE=1) -> wd_finalize: flush + validate the
                ;     streamed record, ACK on a valid image / ERROR(3) on a bad one.
                ;   flat-file   (WRQ_FLAT_MODE=1)  -> wd_finalize_flat: HSAVE the staged
                ;     bytes into the claimed record, then ACK (i121c).
                ; HOSTTEST has neither finalize (no bdos hooks): the i121b flat receive
                ; just ACKs the short final block, unchanged.
                if (defined(NETBOOT_HOSTTEST)==0) * (defined(DUMPER)==0)
                ld      a, (WRQ_SINK_MODE)
                or      a
                jp      nz, wd_finalize
                ld      a, (WRQ_FLAT_MODE)
                or      a
                jp      nz, wd_finalize_flat
                endif
wd_send_ack_de:
                ; the blksize compare overwrote DE; reload the block to ACK
                ; (the just-accepted block, == WRQ_ACKED).
                ld      de, (WRQ_ACKED)

wd_send_ack:
                ; Re-arm the ENC before the ACK TX (i244). In the bootable push path
                ; each accepted DATA block was streamed into the record via HWSAD (an SD
                ; transaction on the shared one-PIC Trinity controller), and the final
                ; block additionally ran wd_finalize's HRSAD read + CMD17/CMD24 claim —
                ; every one disturbs the ENC's persistent RX state, which drv_read never
                ; restores. enc_rx_reestablish re-arms it so the next serve-loop drv_read
                ; + this ACK's drv_write work. (DE holds the block to ACK; preserved
                ; across the re-arm.) Bootable-only: the HOSTTEST flat-accumulate path
                ; does no SD op, so it keeps its proven behaviour unchanged.
                if (defined(NETBOOT_HOSTTEST)==0) * (defined(DUMPER)==0)
                push    de
                call    serve_rearm_enc
                pop     de
                endif
                ; build ACK(DE) at TBUF: opcode 4 (big-endian), block (big-endian).
                ld      hl, TBUF
                ld      (hl), 0                ; opcode high
                inc     hl
                ld      (hl), OP_ACK           ; opcode low = 4
                inc     hl
                ld      (hl), d                ; block high (DE is the block value)
                inc     hl
                ld      (hl), e                ; block low
                ld      bc, 4                  ; ACK is 4 bytes
                jp      srv_send_tbuf          ; wrap to the client + transmit

                if (defined(NETBOOT_HOSTTEST)==0) * (defined(DUMPER)==0)
; wd_finalize — the disk-record push commit (i121f): the short final block has
; arrived and the whole image is streamed into the claimed record. Flush the sink's
; final partial sector, validate the result as a Trinity disk record (size == 819200
; from RRS_TOTAL AND the "BDOS" stamp@232 read back via HRSAD), then reply: the
; final ACK on a valid image (TFTP success), or ERROR(3, "invalid disk record")
; instead of the ACK on a bad one — so a stock `tftp put` client surfaces the
; rejection on its last block. This is the §6.5 "validate before committing, reject
; on mismatch" intent in TFTP-push form. Mirrors netboot_client.asm client_finalize
; (the store+validate half; the boot half is i122c, not this push path).
wd_finalize:
                if defined(NETBOOT_DEBUG)
                ld      a, DBG_FINALIZE
                call    dbg_marker
                endif
                call    raw_record_sink_finish ; flush any final partial sector

                ; size = RRS_TOTAL -> BD_REC_SIZE (32-bit LE).
                ld      hl, (RRS_TOTAL)
                ld      (BD_REC_SIZE), hl
                ld      hl, (RRS_TOTAL + 2)
                ld      (BD_REC_SIZE + 2), hl

                ; read sector 0 (track 0, sector 1) back via HRSAD so the validator
                ; can check the "BDOS" stamp@232 of the just-written record.
                xor     a
                ld      (BD_READ_TRACK), a
                inc     a
                ld      (BD_READ_SECTOR), a    ; sector 1 (1-based)
                call    bdos_read_sector       ; -> BD_READ_BUF (512 bytes)

                call    bdos_validate_disk_record  ; -> BD_REC_VALID (1 = valid)
                ld      a, (BD_REC_VALID)
                or      a
                jr      z, wd_invalid

                ; CLAIM the record (i121g): the data sectors are written, but the
                ; central record-LIST name entry is not yet — bdos_find_free_record
                ; keys "free" off that list name (byte 0 == 0), so without this an
                ; unnamed-but-written record reads as free and the NEXT push overwrites
                ; it. Write the list entry now (the B-DOS new.rec / RENAME path; design
                ; §4.3) so the next push lands on the next free record. The name comes
                ; from the WRQ filename (a leading "trinity-sam-disks/" prefix stripped,
                ; dotted suffix dropped, 10-char B-DOS cap). bdos_claim_record touches
                ; ONLY this record's own entry (the free slot the claim picked) — never
                ; a named record (the shared-resource invariant). Claim ONLY on the
                ; valid path: an invalid push (below) leaves the record free for reuse.
                ld      hl, (PARSE_FILENAME)
                ld      (BD_CLAIM_NAME_PTR), hl
                call    bdos_claim_record

                if defined(NETBOOT_DEBUG)
                ld      a, DBG_FINALIZE_VALID
                call    dbg_marker
                endif
                ; valid: ACK the final block (TFTP success). DE was clobbered by the
                ; validate + claim paths; reload the just-accepted block (== WRQ_ACKED).
                ld      de, (WRQ_ACKED)
                jp      wd_send_ack

wd_invalid:
                ; invalid image: ERROR(3, "invalid disk record") in place of the ACK.
                ; wd_finalize's HRSAD read (+ no claim on the reject path) disturbed the
                ; ENC; re-arm before the ERROR reply's TX (i244), same as wd_send_ack.
                call    serve_rearm_enc
                if defined(NETBOOT_DEBUG)
                ld      a, DBG_FINALIZE_BAD
                call    dbg_marker
                endif
                xor     a
                ld      (ERR_CODE), a
                ld      a, 3
                ld      (ERR_CODE+1), a        ; big-endian code = 0x0003
                ld      hl, err_badimage_msg
                ld      (ERR_MSG_PTR), hl
                call    build_error            ; packet at TBUF, BC = length
                jp      srv_send_tbuf

; wd_finalize_flat — the flat-file archive commit (i121c): the short final block has
; arrived and the whole pushed file is flat-accumulated in WRQ_STAGING. HSAVE it into
; the claimed record as a plain file (the DEFAULT storage class — NO 819200-byte
; disk-record validation; a flat file is any size), claim the record's list-name entry
; so the next push lands elsewhere, then ACK the final block (TFTP success). The server
; mirror of netboot_client.asm client_main's flat-file write-out (the i119 bdos_save_hook
; seam) + the disk-record path's bdos_claim_record; the Go authority is serve.go
; finalizeFlat. A flat file is never content-rejected: the explicit "trinity-sam-disks/"
; prefix is the only opt-in to the validated disk-record class (design §6.5).
wd_finalize_flat:
                ; build the HSAVE UIFA over the staging window: name, source page/offset
                ; = WRQ_STAGING, size = bytes accumulated. The save reads physical page
                ; (WRQ_STAGING>>14) at offset (WRQ_STAGING&&3FFF) — the same page/offset
                ; encoding client_main uses for its STAGING buffer.
                ld      hl, (PARSE_FILENAME)
                ld      (BD_NAME_PTR), hl      ; bdos_name_to_uifa strips prefix + dotted suffix
                ld      a, WRQ_STAGING >> 14
                ld      (BD_SAVE_PAGE), a
                ld      hl, WRQ_STAGING & &3FFF | &8000
                ld      (BD_SAVE_ADDR), hl
                ld      hl, (WRQ_STAGE_OFFSET) ; total bytes accumulated
                ld      (BD_SAVE_SIZE), hl
                call    bdos_fill_save_uifa
                call    bdos_save_hook         ; HSAVE (real B-DOS only)

                ; CLAIM the record's central record-LIST name entry so the next push
                ; lands on the next free record (shared with the disk-record path, i121g;
                ; bdos_find_free_record keys "free" off this list name). The name comes
                ; from the WRQ filename, hardened identically (bdos_build_claim_entry).
                ld      hl, (PARSE_FILENAME)
                ld      (BD_CLAIM_NAME_PTR), hl
                call    bdos_claim_record

                ; ACK the final block (TFTP success). DE was clobbered by the HSAVE +
                ; claim; reload the just-accepted block (== WRQ_ACKED). wd_send_ack
                ; re-arms the ENC the HSAVE/claim SD transactions disturbed before the TX.
                ld      de, (WRQ_ACKED)
                jp      wd_send_ack
                endif

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

; ns_send_packet — transmit the frame at PACKET (length in BC), returning BC.
ns_send_packet:
                push    bc
                ld      hl, PACKET
                call    drv_write
                pop     bc
                ret

; ===========================================================================
; resolve_src — set SRC_PTR + XFER_SIZE for the resolved filename by walking
; SRC_TABLE, which parallels STORE. SRC_TABLE is a list of records:
;   name (NUL-terminated) | 2-byte LE source pointer | 4-byte LE size
; terminated by a single 0 byte (an empty name). The name to look up is the one
; resolve already matched, at (PARSE_FILENAME). On a match: SRC_PTR/XFER_SIZE set,
; CY set. No match (should not happen — STORE and SRC_TABLE are filled in step):
; SRC_PTR/XFER_SIZE left as-is, CY clear.
; ===========================================================================
resolve_src:
                ld      hl, SRC_TABLE
rsv_entry:
                ld      a, (hl)
                or      a
                jr      z, rsv_nomatch         ; end-of-table sentinel
                ; compare this entry's name with (PARSE_FILENAME)
                ld      de, (PARSE_FILENAME)
                push    hl
rsv_name_cmp:
                ld      a, (de)
                cp      (hl)
                jr      nz, rsv_next
                or      a
                jr      z, rsv_found           ; both hit NUL together: match
                inc     hl
                inc     de
                jr      rsv_name_cmp
rsv_next:
                ; not this entry: advance HL past name NUL + 2 (ptr) + 4 (size).
                pop     hl
                call    skip_cstr              ; HL past the name's NUL
                ld      de, 6
                add     hl, de
                jr      rsv_entry
rsv_found:
                pop     de                     ; discard the entry-start copy
                ; HL points at this entry's NUL; skip it to the ptr field.
                inc     hl                     ; past the matched NUL (HL was at it)
                ; HL -> 2-byte LE source pointer
                ld      e, (hl)
                inc     hl
                ld      d, (hl)
                inc     hl
                ld      (SRC_PTR), de          ; SRC_PTR = source pointer
                ; HL -> 4-byte LE size
                ld      e, (hl)
                inc     hl
                ld      d, (hl)
                inc     hl
                ld      (XFER_SIZE), de
                ld      e, (hl)
                inc     hl
                ld      d, (hl)
                ld      (XFER_SIZE+2), de
                scf
                ret
rsv_nomatch:
                or      a
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

; build_oack_opts_wrq — format "blksize\0<bs>\0" (+ "tsize\0<ts>\0" if the
; client sent tsize) into OACK_OPTS for a WRQ OACK. Port of
; serve.go::Responder.startWrite optioned path.
;
; The blksize is taken from XFER_BLKSIZE (already set by negotiate_blksize).
; tsize is read from the client's options (PARSE_OPTS / PARSE_OPT_COUNT): if
; the client included "tsize", its value string is echoed verbatim.
;
; Out: HL = the byte count written.
build_oack_opts_wrq:
                ; Phase 1: find a "tsize" value pointer in the client's options.
                ; Store the result in WRQ_TSIZE_PTR (0 = not found).
                xor     a
                ld      (WRQ_TSIZE_PTR), a
                ld      (WRQ_TSIZE_PTR+1), a   ; default: tsize not found

                ld      bc, (PARSE_OPT_COUNT)
                ld      a, b
                or      c
                jr      z, boww_write          ; no options at all

                ld      hl, (PARSE_OPTS)
boww_scan:
                push    bc
                push    hl                     ; save: name pointer
                ld      de, str_tsize
                call    streq_cstr             ; CY set + HL past NUL if "tsize" matched
                jr      c, boww_found_tsize
                ; not a match: advance past name NUL (already done by streq_cstr
                ; not advancing on mismatch — reload from saved copy and skip both)
                pop     hl
                call    skip_cstr              ; skip name
                call    skip_cstr              ; skip value
                pop     bc
                dec     bc
                ld      a, b
                or      c
                jr      nz, boww_scan
                jr      boww_write             ; exhausted; tsize not found

boww_found_tsize:
                ; HL = pointer to the tsize value string (NUL-terminated in RRQ_IN)
                pop     de                     ; discard saved name ptr
                pop     bc                     ; discard saved count
                ld      (WRQ_TSIZE_PTR), hl    ; save value pointer

boww_write:
                ; Phase 2: write "blksize\0<value>\0" to OACK_OPTS.
                ld      de, OACK_OPTS
                ld      hl, str_blksize
                call    copy_cstr_incl_nul
                ld      hl, (XFER_BLKSIZE)
                call    write_dec_u16
                xor     a
                ld      (de), a
                inc     de

                ; Phase 3: if tsize was found, write "tsize\0<client-value>\0".
                ld      hl, (WRQ_TSIZE_PTR)
                ld      a, h
                or      l
                jr      z, boww_done           ; tsize not found

                push    hl                     ; save client tsize value ptr
                ld      hl, str_tsize
                call    copy_cstr_incl_nul
                pop     hl                     ; HL = client tsize value ptr
                call    copy_cstr_incl_nul     ; writes value + NUL

boww_done:
                ; HL = length: DE (end) - OACK_OPTS (start).
                ld      hl, OACK_OPTS
                ex      de, hl                 ; HL = end cursor, DE = OACK_OPTS start
                or      a
                sbc     hl, de                 ; HL = length written
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
err_notfound_msg: defm "File not found"
                  defb 0
sv_done_name:     defm "tftp.done"           ; reserved control name (i121e); never stored
                  defb 0
str_blksize:      defm "blksize"
                  defb 0
str_tsize:        defm "tsize"
                  defb 0
                if (defined(NETBOOT_HOSTTEST)==0) * (defined(DUMPER)==0)
err_nofree_msg:   defm "no free record"
                  defb 0
err_badimage_msg: defm "invalid disk record"
                  defb 0
err_toobig_msg:   defm "flat file too large"
                  defb 0
                endif

; ===========================================================================
; Real-hardware bootable entry (excluded from the host harness build, which has
; no EEPROM / real silicon, and from the dumper build, which supplies its own
; boot main + provision). CALL 32768 lands here on boot.
; ===========================================================================
                ; `*` is logical AND for 0/1 conditions (pyz80's if has no `&&`/`and`).
                if (defined(NETBOOT_HOSTTEST)==0) * (defined(DUMPER)==0)

; serve_main — read the SAM's MAC + IP from the Trinity EEPROM "Trinity Network "
; chunk, fill CONFIG, set a fixed transfer TID, provision the baked-in demo files
; into STORE + SRC_TABLE, init the ENC28J60, then loop forever serving. On any
; bring-up failure it sets a distinctive border colour and halts.
serve_main:
                di
                ; --- locate + read the "Trinity Network " flash chunk ---
                ld      a, 1
                ld      (part), a
                ld      (total), a
                ld      hl, sv_chunk_name
                ld      de, name
                ld      bc, 16
                ldir
                call    find_index
                ld      a, (value)
                and     a
                jp      z, sv_fail_cfg
                call    read_chunk
                ld      a, (value)
                and     a
                jp      z, sv_fail_cfg

                ; copy sam_mac (chunk+0) / sam_ip (chunk+6) into CONFIG.
                ld      hl, chunk + 0
                ld      de, CONFIG_SERVERMAC
                ld      bc, 6
                ldir
                ld      hl, chunk + 6
                ld      de, CONFIG_SERVERIP
                ld      bc, 4
                ldir

                ; fixed transfer source TID (an ephemeral high port), big-endian.
                ld      a, 40136 >> 8
                ld      (CONFIG_SERVERTID), a
                ld      a, 40136 & &ff
                ld      (CONFIG_SERVERTID + 1), a

                xor     a
                ld      (XFER_ACTIVE), a
                ld      (XFER_JUST_OACKED), a
                ld      (XFER_STOP_REQUESTED), a   ; no stop pending at boot (i121e)

                ; --- provision the baked-in demo files ------------------
                call    provision_demo

                ; --- init the ENC28J60 FIRST, before any SD work (i244) --
                ; drv_init's chk_trinity identity probe uses fixed DJNZ delays, NOT a
                ; BUSY-poll, so it must run against a quiescent PIC. The heavy &38 SD
                ; init (csd_set_bd_records below) leaves the shared microcontroller
                ; settling; doing the CSD read first made chk_trinity's &08 select land
                ; while the PIC was still busy — the select is ignored, the identity
                ; read is stale, and drv_init returns BC=0 -> blue sv_fail_init. This is
                ; the same i242 bug the CSD probe hit (probe_main); it crashed Pete's
                ; SAM on a Test B disk-push (2026-06-24). trinload inits the ENC with no
                ; prior SD transaction — mirror it: ENC up first, SD read after.
                ld      hl, CONFIG_SERVERMAC
                call    drv_init
                ld      a, b
                or      c
                jp      z, sv_fail_init

                ; --- read the SD CSD -> BD_RECORDS (i145b) ---------------
                ; The WRQ record-push picker needs the card's total record count;
                ; csd_set_bd_records computes it from the inserted SD card's CSD
                ; (replacing the inject-only shortcut that left it 0 on real
                ; hardware). On any read failure BD_RECORDS stays 0 (safe decline).
                ;
                ; SHIPPED as a section-D overlay (i145b-b2). The SD-read driver +
                ; CSD decode + records math (~600 bytes, sd_csd.asm) pushes the boot
                ; image past &C000 into section D; that is fine — section D is RAM at
                ; boot (LOAD CODE 32768 deposits the >&BFFF bytes into section-D RAM
                ; and ROM1 is off at run, LMPR &1F), proven in SimCoupe by the
                ; section-D loadability probe (docs/notes/sam-paging.md, the
                ; secd-loadability make target). The image stays within the
                ; &8000-&FFFF (32768-byte) window the boot fit-check enforces.
                call    csd_set_bd_records

                ; --- re-arm the ENC RX path the SD transaction disturbed (i244) --
                ; The SD ladder leaves the ENC's persistent RX state (RXEN, ring
                ; pointers, MAC/PHY) disturbed; drv_read never restores it, so serving
                ; is dead until we re-run drv_init's RX-arming (enc_rx_reestablish:
                ; ereset + ring/MAC/PHY re-arm + RX enable, MINUS chk_trinity, which
                ; would fail right after the &38 SD-init — i242). Mirrors probe_main.
                di
                ld      hl, CONFIG_SERVERMAC
                call    enc_rx_reestablish
                ei

sv_serve_loop:
                ; Attended Esc-to-exit (mirrors netboot_dumper.asm dm_serve_loop;
                ; trinload.asm read_loop): poll the keyboard, and on Esc RET to
                ; trinload's pushed `start` return address. serve_main is `jp`'d to and
                ; this loop falls through with no extra stack frame, so the stack top
                ; here is exactly trinload's return address — a plain RET reaches it.
                ; The serve loop never repages its own section, so the RET is clean
                ; (no LMPR/HMPR restore, unlike the dumper's i188 ROM read). Under the
                ; host harness, with no keyboard device on port &f9, the IN reads &FF
                ; so bit 5 is set and the poll falls through "not pressed" — the host
                ; tests are unaffected (a test models the key to assert this exit).
                ld      a, &f7
                in      a, (&f9)
                bit     5, a                   ; Esc pressed?
                jp      z, sv_exit_to_trinload ; -> quiesce, then RET to trinload's start

                call    serve_serve_once

                ; Unattended `tftp.done` exit (i121e): handle_wrq set XFER_STOP_REQUESTED
                ; on the reserved control name. RET to trinload's start, same clean
                ; unwind as the Esc exit.
                ld      a, (XFER_STOP_REQUESTED)
                or      a
                jp      nz, sv_exit_to_trinload ; -> quiesce, then RET to trinload's start

                jr      sv_serve_loop

; sv_exit_to_trinload — the single clean-exit point for BOTH serve exits (Esc and the
; `tftp.done` control name). Quiesces the shared Trinity microcontroller / SD-SPI bus
; before RETing to trinload's pushed `start` return address, so trinload's `start`
; (which re-runs chk_trinity: OUT &DC,8/9 + IN &DD, with only fixed DJNZ delays, no
; busy-poll) resumes against an IDLE controller rather than one mid-transaction.
;
; A disk-record push leaves the &DC microcontroller having just driven a burst of
; CMD24 sector writes + list-sector CMD17 reads; without a deselect + settle the next
; owner's first &DC select can land while the PIC is still busy and be silently
; dropped (manual:22) — the same shared-microcontroller-busy hazard that produced the
; i242 chk_trinity-after-&38 stale read. The quiesce is: OUT (&DC),&04 (all-deselect,
; auto-null off — the transaction-close bracket the SD ladder uses), a BOUNDED wait
; for &DC bit 3 (BUSY) to clear, then a short fixed settle delay.
;
; DESIGNED, NOT HARDWARE-VERIFIED (CLAUDE.md §5). The Go harness models &DC bit-3 BUSY
; (raised one SPI-byte-time per OUT, cleared on the next IN &DC), so the deselect +
; busy-poll + RET path EXECUTES in emulation (a test asserts the RET still reaches
; trinload after the quiesce) — but whether a real PIC resumes chk_trinity cleanly
; after this sequence is gated on Pete's Trinity. The wait is bounded so a stuck
; controller can NEVER hang the SAM on the way out (the prime directive: an exit path
; must always reach trinload; a 2026-06-24-style unbounded SD busy-wait hang here
; would strand the machine at the worst moment — right as it tries to hand back).
sv_exit_to_trinload:
                di                              ; no interrupt window mid-quiesce
                ld      a, SDC_NULLOFF          ; &04: all-deselect / auto-null off
                out     (SDC_PORT), a           ;   close any open &DC transaction
                ld      bc, SV_QUIESCE_LIMIT     ; bounded busy-poll budget
sv_q_busy:
                in      a, (SDC_PORT)           ; reading &DC also clears BUSY in HW/model
                and     %00001000               ; &DC bit 3 = BUSY
                jr      z, sv_q_settled          ; clear -> controller idle
                dec     bc
                ld      a, b
                or      c
                jr      nz, sv_q_busy            ; budget left -> keep polling
                ; budget exhausted: give up waiting (a stuck PIC must not trap us here)
sv_q_settled:
                ; short fixed settle delay so the PIC is fully idle before trinload's
                ; fixed-delay chk_trinity probes it (no BUSY-poll on that side).
                ld      bc, SV_SETTLE_LOOPS
sv_q_settle:
                dec     bc
                ld      a, b
                or      c
                jr      nz, sv_q_settle
                ei
                ret                             ; -> trinload's pushed `start`

SV_QUIESCE_LIMIT: equ &FFFF                     ; ~65535 busy-polls (~0.5s ceiling); a working
                                                ; controller clears BUSY in ~1 poll, so this is
                                                ; only the stuck-hardware bound, never the norm.
SV_SETTLE_LOOPS:  equ &0200                     ; ~512-iter fixed settle (~a few hundred us) so
                                                ; the PIC is idle before chk_trinity's fixed delays.

sv_fail_cfg:
                ld      a, 2                   ; red border: no/bad network settings
                jr      sv_fail_show
sv_fail_init:
                ld      a, 1                   ; blue border: ENC28J60 init failed
sv_fail_show:
                ; Show the diagnostic border, hold so the operator can read it, then
                ; hand control back to trinload via tr_terminate (di;halt under test,
                ; RET to trinload on hardware — the i228 unmapped-port probe). The
                ; serve binary is trinload-pushable (netboot-serve-trinload pushes
                ; netboot_serve_boot.bin), and a raw di;halt strands the SAM, costing
                ; a power-cycle on every failed push (Pete, 2026-06-24; i243b).
                out     (&fe), a
sv_fail_wait:
                ld      a, &f7                 ; poll Esc (trinload's key idiom)
                in      a, (&f9)
                bit     5, a
                jr      nz, sv_fail_wait       ; hold the border until Esc, then return
                jp      tr_terminate

; provision_demo — copy the assembled demo STORE + SRC_TABLE templates into the
; live STORE/SRC_TABLE the resolve + resolve_src walk. The templates are built at
; assembly time (demo_store_tmpl / demo_src_tmpl below).
provision_demo:
                ld      hl, demo_store_tmpl
                ld      de, STORE
                ld      bc, demo_store_tmpl_end - demo_store_tmpl
                ldir
                ld      hl, demo_src_tmpl
                ld      de, SRC_TABLE
                ld      bc, demo_src_tmpl_end - demo_src_tmpl
                ldir
                ret

sv_chunk_name:    defm "Trinity Network "     ; the flash chunk holding MAC+IP

; --- the baked-in demo files -----------------------------------------------
; Two tiny text files served by name. The filenames live (once) in the
; demo_store_tmpl / demo_src_tmpl directories below; here are their byte bodies.
hello_data:       defm "Hello from a SAM Coupe over Trinity TFTP!"
                  defb 13, 10
hello_end:
hello_len:        equ hello_end - hello_data

readme_data:      defm "This SAM Coupe is serving files over TFTP via a Quazar Trinity."
                  defb 13, 10
                  defm "No Pi, no DHCP - just plain TFTP. Try: tftp <ip> then get hello.txt"
                  defb 13, 10
readme_end:
readme_len:       equ readme_end - readme_data

; STORE format (resolve walks it):    name\0 | 4-byte LE size, then a 0 sentinel.
; SRC_TABLE format (resolve_src walks it):
;                       name\0 | 2-byte LE source ptr | 4-byte LE size, then a 0.

demo_store_tmpl:
                  defm "hello.txt"
                  defb 0
                  defw hello_len
                  defw 0                       ; size high word
                  defm "readme.txt"
                  defb 0
                  defw readme_len
                  defw 0
                  defb 0                        ; end-of-store sentinel
demo_store_tmpl_end:

demo_src_tmpl:
                  defm "hello.txt"
                  defb 0
                  defw hello_data
                  defw hello_len
                  defw 0
                  defm "readme.txt"
                  defb 0
                  defw readme_data
                  defw readme_len
                  defw 0
                  defb 0                        ; end-of-table sentinel
demo_src_tmpl_end:

                endif  ; !NETBOOT_HOSTTEST

; ===========================================================================
; CONFIG + shared state. The harness writes CONFIG_* + STORE + SRC_TABLE directly;
; on the bootable build serve_main fills CONFIG from the EEPROM + provision_demo.
; ===========================================================================
CONFIG_SERVERMAC: defs 6
CONFIG_SERVERIP:  defs 4
CONFIG_SERVERTID: defs 2                 ; the SAM's transfer source port (BE)

; The current TFTP client endpoint + transfer state.
CLIENT_MAC:       defs 6
CLIENT_IP:        defs 4
CLIENT_TID:       defs 2                 ; the client's RRQ source port (BE)

SRC_PTR:          defs 2                 ; pointer to the resolved file's bytes
XFER_BLKSIZE:     defs 2
XFER_SIZE:        defs 4                 ; file size (LE)
XFER_OFFSET:      defs 4                 ; bytes streamed so far (LE)
XFER_NEXT_BLK:    defs 2                 ; next block number (LE)
XFER_ACTIVE:      defs 1                 ; 1 = a transfer is armed
XFER_LAST_SHORT:  defs 1                 ; 1 = the last DATA was a short block
XFER_JUST_OACKED: defs 1                 ; 1 = the next ACK (block 0) -> FirstData

; Server-lifecycle stop flag (i121e). Set by a `tftp put tftp.done` (the reserved
; control name) or — on hardware — by an attended keyboard Esc; sv_serve_loop polls
; it after each frame and RETs to trinload's pushed return address, the only exit
; until an auto-boot BIOS exists (manifest design decision 6).
XFER_STOP_REQUESTED: defs 1              ; 1 = end the session, return control to trinload

; WRQ state. WRQ_TSIZE_PTR is a pointer into RRQ_IN: non-zero if the client sent
; a "tsize" option in the WRQ (the value is echoed back in the OACK); 0 if no
; tsize was sent (OACK includes only blksize).
WRQ_TSIZE_PTR:    defs 2

; WRQ receive-to-staging state (i121b). Mirrors serve.go::wrqReceiver. A WRQ
; receive is armed by handle_wrq (after the ACK-0 / OACK handshake) and consumed
; one DATA block at a time by handle_data.
WRQ_RECV_ACTIVE:  defs 1                 ; 1 = a WRQ receive is in progress
WRQ_BLKSIZE:      defs 2                 ; negotiated block size (LE)
WRQ_ACKED:        defs 2                 ; highest block accumulated/ACKed (LE)
WRQ_STAGE_OFFSET: defs 2                 ; bytes accumulated into WRQ_STAGING
WRQ_DATA_LEN:     defs 2                 ; the last DATA block's payload length
WRQ_DONE:         defs 1                 ; 1 = a short final block ended the xfer

; Disk-record push state (i121f). WRQ_SINK_MODE picks the per-block payload route:
;   0 = flat accumulate into WRQ_STAGING (the HOSTTEST wire path + the i121c FlatFile
;       HSAVE staging),
;   1 = stream into a claimed Trinity record via raw_record_sink (the i121f DiskRecord
;       push path). handle_wrq sets it per the classified storage class (1 for a
;       prefixed DiskRecord push, 0 for a FlatFile push); handle_data reads it. It
;       lives in both builds (the default-clear at the
;       top of handle_wrq runs in the HOSTTEST build too), so it is unconditional.
WRQ_SINK_MODE:    defs 1                 ; 0 = flat WRQ_STAGING, 1 = raw-record sink
WRQ_RECORD:       defs 1                 ; the claimed record number streamed into (bootable)

; WRQ storage class (i121c). handle_wrq classifies the WRQ filename via the
; "trinity-sam-disks/" prefix (bdos_strip_disk_prefix, the bdos.Classify port):
;   0 = DiskRecord  — prefixed push: stream into the record + validate as an
;       819200-byte disk image (the i121f path, WRQ_SINK_MODE=1).
;   1 = FlatFile    — the DEFAULT class (no prefix): flat-accumulate into
;       WRQ_STAGING and HSAVE into the claimed record on the final block
;       (wd_finalize_flat, WRQ_SINK_MODE=0). Mirrors serve.go wrqReceiver.flat.
; Bootable-only (the classify + both finalizes live behind NETBOOT_HOSTTEST==0).
WRQ_FLAT_MODE:    defs 1                 ; 1 = flat-file (HSAVE), 0 = disk-record push

; The pushed file flat-accumulates here (sink mode 0) for the FlatFile class (i121c).
; It sits at &C800 — AFTER the serve binary (which ends ~&C4E8: the &Cxxx tail holds
; the trinload-return code tr_terminate at &C0A3, the TR_* exit state, and the
; once-at-boot CSD-read overlay, all BELOW &C800 and untouched) and BELOW the stack
; (emulation SP &6FFE; on hardware the inherited stack is low, the same assumption the
; netboot_client.asm STAGING buffer relies on). This mirrors client_main's flat-file
; staging: a dedicated RAM buffer the HSAVE reads via physical page (WRQ_STAGING>>14)
; at offset (WRQ_STAGING&&3FFF) — no HMPR paging. WRQ_STAGING_CAP bounds it to the
; free window up to &FF00 (a 256-byte stack guard below &10000); wd_next_block rejects
; an over-cap push with ERROR(3) rather than overflow (no silent truncation, i253).
; ROM1 is off at serve runtime, so &C800-&FFFF is RAM. (The HOSTTEST i121b receive
; test reads the WRQ_STAGING symbol, so the address move is transparent to it.)
WRQ_STAGING:      equ &C800
WRQ_STAGING_CAP:  equ &FF00 - WRQ_STAGING    ; max bytes a flat push may stage (&3700 = 14080)

RX_LEN:           defs 2
TFTP_PKT_LEN:     defs 2
RXBUF:            defs 1518              ; the single received-frame buffer

; STORE (the flat name->size directory resolve walks) is defined in the included
; tftp_parse.asm. SRC_TABLE is the parallel name->source-ptr+size table resolve_src
; walks; it is filled by the harness, or by provision_demo on hardware.
SRC_TABLE:        defs 256

; ===========================================================================
; The host-verified packet builders/parsers, composed into this one translation
; unit (their org is suppressed — no NETBOOT_STANDALONE — so this file's org
; governs). encdrv.asm is the real vendored ENC28J60 driver; eeprom.asm is the
; real flash config reader (real-hardware path only).
; ===========================================================================
                include "build_udp_frame.asm"
                include "build_arp_reply.asm"
                include "tftp_build.asm"
                include "tftp_parse.asm"
                include "encdrv.asm"
                ; The dumper includes eeprom.asm itself (it reads the EEPROM in
                ; every build), so suppress this conditional include there to avoid
                ; a double definition. The standalone serve build keeps it: the
                ; bootable image reads the SAM's MAC/IP from flash, the host test
                ; build has no EEPROM and excludes it. (`*` = logical AND.)
                ;
                ; The disk-record push store machinery (i121f) — the B-DOS seam
                ; (free-record find / record select / raw-sector write+read /
                ; validate, with the RST 8 hook dispatch) + the streaming sink — is
                ; included for the same builds: only the bootable serve image streams
                ; a push into a record. The HOSTTEST wire-test build and the dumper
                ; (RRQ-only) leave these out, matching the sink-code guards above.
                if (defined(NETBOOT_HOSTTEST)==0) * (defined(DUMPER)==0)
                include "eeprom.asm"
                ; The serve unit is the only consumer of the WRQ record-claim path
                ; (wd_finalize -> bdos_claim_record), so it asks bdos_seam.asm to
                ; assemble the claim/list-write routines. The client never claims, so
                ; it omits them (boot-window budget — i195).
NETBOOT_WANT_CLAIM: equ 1
                include "bdos_seam.asm"        ; i121f: free-record find + record select + HWSAD/HRSAD + validate
                include "raw_record_sink.asm"  ; i121f: streaming disk-image -> raw record (HWSAD per sector)
                ; tr_terminate (i228): serve_main's bring-up error paths end via it,
                ; not a raw di;halt, so a failed trinload push leaves the SAM usable
                ; (i243b). build_udp_frame (its only external dep) is included above.
                ; The dumper/csd_probe builds (DUMPER=1) include test_report.asm
                ; themselves, so this serve-only include can't double-define.
                include "test_report.asm"
                ; i271 network debug step-markers — additive, only under
                ; -D NETBOOT_DEBUG (reuses build_udp_frame/drv_write/TR_SRC_* above).
                if defined(NETBOOT_DEBUG)
                include "dbg_marker.asm"
                endif
                ; i145b CSD-read -> BD_RECORDS (i145b-b2): shipped as a section-D
                ; overlay. The ~600-byte module places the boot image's tail above
                ; &BFFF into section D, which is RAM at boot (see the csd_set_bd_records
                ; call site). Verified standalone by csd_to_bd_records_test.go and via
                ; serve_main by the boot integration test.
                include "sd_csd.asm"

; ===========================================================================
; SERVE_CONFIG — the placement-strategy config block (i121h). A small, fixed,
; host-patchable region at the END of the bootable binary, so the packaging vessel
; (the i121d host launcher patching a trinload code block, or a `.mgt` disk) can set
; the WRQ disk-record PLACEMENT strategy by file offset before the program runs.
; This program is a vessel-agnostic library: whoever loads it populates this block;
; an un-patched binary uses the baked default (highest-free, manifest design §4 s3 /
; decision 4 — keeps the user's low, memorable record slots for their own disks; TFTP
; storage grows down from the top).
;
; The block is READ at find-time straight from RAM (cheap) by
; bdos_find_record_for_strategy; it is not re-read from disk. Pete: the config is read
; at launch and not re-read routinely.
;
; BYTE LAYOUT (the i121d host launcher patches by file offset from the SERVE_CONFIG
; symbol — the block's anchor at +0; keep STABLE):
;   +0  SERVE_CONFIG        1 byte   magic/version = SERVE_CFG_MAGIC_VAL (&5A); a
;                                    launcher sanity-checks it found the block. This
;                                    label is the patch anchor (file offset
;                                    SERVE_CONFIG - &8000, the binary's load org).
;   +1  SERVE_CFG_STRATEGY  1 byte   placement strategy:
;                                      0 = highest-free  (SERVE_STRAT_HIGHEST — the
;                                          DEFAULT, baked here per §4 decision 4)
;                                      1 = lowest-free   (SERVE_STRAT_LOWEST)
;                                      2 = explicit      (SERVE_STRAT_EXPLICIT — use
;                                          the record at +2..3)
;   +2  SERVE_CFG_RECORD    2 bytes  explicit record number (LE); used only when
;                                    strategy == 2. &FFFF = undefined (reserved for the
;                                    future i121j SAM-prompt path; not implemented here).
; Total: 4 bytes.
; ===========================================================================
SERVE_CFG_MAGIC_VAL:  equ &5A           ; the magic/version byte value
SERVE_STRAT_HIGHEST:  equ 0             ; place at the highest free record (default)
SERVE_STRAT_LOWEST:   equ 1             ; place at the lowest free record
SERVE_STRAT_EXPLICIT: equ 2             ; place at SERVE_CFG_RECORD if free
SERVE_CFG_RECORD_NONE: equ &FFFF        ; explicit record undefined (i121j prompt path)

SERVE_CONFIG:         defb SERVE_CFG_MAGIC_VAL      ; +0 magic/version (patch anchor)
SERVE_CFG_STRATEGY:   defb SERVE_STRAT_HIGHEST      ; +1 baked default: highest-free (§4 s3)
SERVE_CFG_RECORD:     defw SERVE_CFG_RECORD_NONE    ; +2 explicit record (LE); &FFFF = none
                endif
