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
                ; === 4. UDP dst == our transfer TID -> TFTP ACK ========
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
; WRQ handler (i121a — handshake only). Port of serve.go::Responder.startWrite.
;
; A WRQ arrives on UDP dst 69 (same port as RRQ). The server learns the client
; endpoint (MAC/IP/TID) and replies:
;   - bare WRQ (no options): ACK-0 (`00 04 00 00`)    — tftp.BuildACK(0)
;   - optioned WRQ:          OACK echoing blksize      — same build_oack path
;
; DATA reception is deferred to i121b. This routine only produces the handshake
; reply frame; no receive state is set up here.
; ===========================================================================
handle_wrq:
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

                ; bare WRQ? (no options: PARSE_OPT_COUNT == 0)
                ld      bc, (PARSE_OPT_COUNT)
                ld      a, b
                or      c
                jr      nz, wrq_oack

                ; --- bare WRQ: reply ACK-0 (00 04 00 00) ------------------
                call    build_ack0             ; packet at TBUF, BC = 4
                jp      srv_send_tbuf

wrq_oack:
                ; --- optioned WRQ: OACK echoing blksize (and tsize if present).
                ; Reuse negotiate_blksize -> XFER_BLKSIZE, then build_oack_opts
                ; formats "blksize\0<n>\0" (and optionally "tsize\0<ts>\0") into
                ; OACK_OPTS. Port of serve.go::Responder.startWrite optioned path.
                call    negotiate_blksize      ; -> XFER_BLKSIZE
                call    build_oack_opts_wrq    ; -> OACK_OPTS, HL = byte count
                ld      (OACK_OPTS_LEN), hl
                call    build_oack             ; packet at TBUF, BC = length
                jp      srv_send_tbuf

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
str_blksize:      defm "blksize"
                  defb 0
str_tsize:        defm "tsize"
                  defb 0

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

                ; --- provision the baked-in demo files ------------------
                call    provision_demo

                ; --- init the ENC28J60 with the SAM's real MAC ----------
                ld      hl, CONFIG_SERVERMAC
                call    drv_init
                ld      a, b
                or      c
                jp      z, sv_fail_init

sv_serve_loop:
                call    serve_serve_once
                jr      sv_serve_loop

sv_fail_cfg:
                ld      a, 2                   ; red border: no/bad network settings
                out     (&fe), a
                di
                halt
sv_fail_init:
                ld      a, 1                   ; blue border: ENC28J60 init failed
                out     (&fe), a
                di
                halt

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

; WRQ state (i121a — handshake only). WRQ_TSIZE_PTR is a pointer into RRQ_IN:
; non-zero if the client sent a "tsize" option in the WRQ (the value is echoed
; back in the OACK); 0 if no tsize was sent (OACK includes only blksize).
WRQ_TSIZE_PTR:    defs 2

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
                if (defined(NETBOOT_HOSTTEST)==0) * (defined(DUMPER)==0)
                include "eeprom.asm"
                endif
