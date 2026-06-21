; tftp_client_loop.asm — the i82 TFTP client transfer loop (the receive side):
; the lock-step DATA/ACK loop that pulls a file from a TFTP server.
;
; This is the SAM-side Z80 port of the netboot-oracle Go authority
; tools/netboot-oracle/tftp/clientloop.go::ClientLoop (the framed wrapper around
; tftp/client.go::ClientXfer). It composes the host-verified pieces —
; build_udp_frame, parse_oack (tftp_client.asm) — and the real vendored driver
; (encdrv.asm) into one binary, and runs them over the i80 emulated Trinity:
;
;   tftp_recv_data:
;     drv_read a frame; UDP dst port == our TID? opcode DATA(3)?
;     first DATA -> learn the server endpoint (MAC/IP/source-port = server TID).
;     wrong server TID -> build_ack-style ERROR(5) to the stray sender, drv_write
;       (RFC 1350 §4 "unknown transfer ID"); discard, do not accept.
;     block == acked+1 -> accumulate the payload into STAGING, acked = block,
;       build ACK(block), wrap (our TID -> server TID), drv_write; a payload
;       shorter than blksize marks the transfer done.
;     block <= acked -> a duplicate (the server retransmitted): re-ACK, no store.
;     future block -> ignore (no gap-filling).
;   tftp_recv_timeout:
;     the Sorcerer's-Apprentice-Syndrome fix — retransmit the LAST ACK only,
;     never the RRQ (so a duplicated DATA cannot cascade). Nothing before the
;     first ACK (the caller re-sends the RRQ at that stage).
;
; The dispatch, TID validation, block accounting, accumulation, short-final-
; block termination, and SAS retransmit mirror ClientXfer.OnData/OnTimeout step
; for step; the ACK/ERROR frames are produced by build_ack + build_udp_frame, so
; the frames on the virtual wire are byte-for-byte the Go ClientLoop authority
; output — the host check this file's test asserts.
;
; PROVENANCE: the receive loop + SAS fix + unknown-TID rule are
; client.go/clientloop.go (research note §1.7; RFC 1350 §4); the TFTP wire format
; is RFC 1350. Reply framing uses build_udp_frame (our IP + our TID -> server IP
; + server TID).
;
; VERIFICATION: host-verifiable end-to-end under the i80 emulation — drv_read a
; captured DATA, build + drv_write the ACK, and assert each frame on the virtual
; wire matches the Go ClientLoop byte-for-byte (tftp_client_loop_test.go). NOT
; host-verifiable: an end-to-end Pi boot — gated on real Trinity (CLAUDE.md §5).
; Emulation-verified is not hardware-verified.

                org     &8000

; Frame offsets (mirror build_udp_frame.asm OFF_*); RX_ prefix to avoid a clash.
RX_ETH_SRCMAC:    equ 6
RX_IP_SRC:        equ 26
RX_UDP_SRCPORT:   equ 34                 ; big-endian on the wire
RX_UDP_DSTPORT:   equ 36
RX_UDP_PAYLOAD:   equ 42

OP_ACK:           equ 4
OP_DATA:          equ 3
OP_ERROR:         equ 5
ERR_UNKNOWN_TID:  equ 5

; ---------------------------------------------------------------------------
; tftp_recv_data — read one frame and answer a DATA with an ACK (or ERROR(5)
; for a stray TID). Out: BC = bytes transmitted (the ACK/ERROR frame), or 0 if
; the frame was not a DATA to our TID (nothing sent).
; ---------------------------------------------------------------------------
tftp_recv_data:
                ld      hl, RXBUF
                call    drv_read
                ld      a, b
                or      c
                jp      z, cl_none
                ld      (CL_RX_LEN), bc

                ; UDP dst port == our TID? (compare 2 big-endian bytes)
                ld      a, (RXBUF + RX_UDP_DSTPORT)
                ld      hl, CLIENT_TID
                cp      (hl)
                jp      nz, cl_none
                ld      a, (RXBUF + RX_UDP_DSTPORT + 1)
                inc     hl
                cp      (hl)
                jp      nz, cl_none

                ; opcode DATA(3)?
                ld      a, (RXBUF + RX_UDP_PAYLOAD)
                or      a
                jp      nz, cl_none
                ld      a, (RXBUF + RX_UDP_PAYLOAD + 1)
                cp      OP_DATA
                jp      nz, cl_none

                ; first DATA? learn the server endpoint.
                ld      a, (GOT_SERVER)
                or      a
                jr      nz, cl_have_server
                ; server MAC <- Ethernet source
                ld      hl, RXBUF + RX_ETH_SRCMAC
                ld      de, SERVER_MAC
                ld      bc, 6
                ldir
                ; server IP <- IP source
                ld      hl, RXBUF + RX_IP_SRC
                ld      de, SERVER_IP
                ld      bc, 4
                ldir
                ; server TID <- UDP source port
                ld      hl, RXBUF + RX_UDP_SRCPORT
                ld      de, SERVER_TID
                ld      bc, 2
                ldir
                ld      a, 1
                ld      (GOT_SERVER), a
                jr      cl_tid_ok              ; first DATA: TID defines the server

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

                ; windowed receive (RFC 7440) when WINDOWSIZE > 1; else lock-step.
                ; Only A is used here, so DE (block) is preserved either way.
                ld      a, (WINDOWSIZE+1)
                or      a
                jp      nz, cl_windowed        ; high byte set: > 255, windowed
                ld      a, (WINDOWSIZE)
                cp      2
                jp      nc, cl_windowed        ; low byte >= 2: windowed

                ; --- lock-step path (windowsize <= 1, unchanged) ---
                ; compare block with ACKED (the highest block ACKed so far).
                ; expected = ACKED + 1.
                ld      hl, (ACKED)
                inc     hl                     ; HL = acked + 1
                or      a
                sbc     hl, de                 ; (acked+1) - block
                jr      z, cl_next_block       ; block == acked+1: the next block
                ; block <= acked  => duplicate (re-ACK); block > acked+1 => future.
                ld      hl, (ACKED)
                or      a
                sbc     hl, de                 ; acked - block
                jp      c, cl_none             ; acked < block (future, not +1): ignore
                ; duplicate: re-ACK the *received* block (DE) without storing
                ; (faithful to ClientXfer.OnData: BuildACK(block), acked unchanged).
                ld      (ACK_BLOCK), de
                jp      cl_send_ack

cl_next_block:
                call    cl_accept_block        ; accumulate + acked=block + done
                jp      cl_send_ack            ; lock-step: ACK the accepted block

; ---------------------------------------------------------------------------
; cl_windowed — RFC 7440 windowed receive (mirrors ClientXfer.onDataWindowed).
; In: DE = received block. Accumulate an in-sequence block (acked+1) and ACK
; only at the window boundary or the short final block; any gap or duplicate
; re-ACKs the last in-sequence block (ACKED) to make the sender rewind, and
; resets the window counter. Mid-window blocks accumulate silently (no ACK).
; ---------------------------------------------------------------------------
cl_windowed:
                ld      hl, (ACKED)
                inc     hl                     ; HL = acked + 1
                or      a
                sbc     hl, de                 ; (acked+1) - block
                jr      z, cl_w_inseq          ; block == acked+1: the next block
                ; gap or duplicate: reset the window, re-ACK the last good block.
                ld      hl, 0
                ld      (WINCOUNT), hl
                ld      hl, (ACKED)
                ld      (ACK_BLOCK), hl
                jp      cl_send_ack
cl_w_inseq:
                call    cl_accept_block        ; accumulate + acked=block + done
                ld      hl, (WINCOUNT)
                inc     hl
                ld      (WINCOUNT), hl         ; winCount++
                ld      a, (XFER_DONE)
                or      a
                jr      nz, cl_w_ack           ; short final block: ACK now
                ld      hl, (WINCOUNT)
                ld      de, (WINDOWSIZE)
                or      a
                sbc     hl, de                 ; winCount - windowsize
                jp      c, cl_none             ; winCount < windowsize: mid-window, no ACK
cl_w_ack:
                ld      hl, 0
                ld      (WINCOUNT), hl         ; reset the window
                ld      hl, (ACKED)
                ld      (ACK_BLOCK), hl        ; ACK the last in-sequence block
                jp      cl_send_ack

; ---------------------------------------------------------------------------
; cl_accept_block — accept an in-sequence DATA block: append its payload to
; STAGING, advance STAGE_OFFSET, set ACKED/ACK_BLOCK = block, and set XFER_DONE
; when the payload is shorter than the block size (the short final block).
; Shared by the lock-step (cl_next_block) and windowed (cl_w_inseq) paths.
; ---------------------------------------------------------------------------
cl_accept_block:
                ; payload length = frame length - (42 + 4 TFTP header)
                ld      hl, (CL_RX_LEN)
                ld      de, RX_UDP_PAYLOAD + 4
                or      a
                sbc     hl, de                 ; HL = data length
                ld      (LAST_DATA_LEN), hl

                ; accumulate the payload into STAGING at the running offset.
                push    hl                     ; save data length
                ld      hl, (STAGE_OFFSET)
                ld      de, STAGING
                add     hl, de                 ; HL = dest
                ex      de, hl                 ; DE = dest
                ld      hl, RXBUF + RX_UDP_PAYLOAD + 4   ; src = payload data
                pop     bc                     ; BC = data length
                ld      a, b
                or      c
                jr      z, cl_no_copy          ; zero-length block: nothing to copy
                push    bc
                ldir
                pop     bc
cl_no_copy:
                ; STAGE_OFFSET += data length
                ld      hl, (STAGE_OFFSET)
                ld      de, (LAST_DATA_LEN)
                add     hl, de
                ld      (STAGE_OFFSET), hl

                ; acked = block. The frame stores the block big-endian; assemble
                ; the 16-bit value into HL and store it little-endian in ACKED.
                ld      a, (RXBUF + RX_UDP_PAYLOAD + 2)
                ld      h, a                   ; high byte
                ld      a, (RXBUF + RX_UDP_PAYLOAD + 3)
                ld      l, a                   ; low byte
                ld      (ACKED), hl
                ld      (ACK_BLOCK), hl        ; ACK the just-accepted block

                ; done if data length < blksize.
                ld      hl, (LAST_DATA_LEN)
                ld      de, (CLIENT_BLKSIZE)
                or      a
                sbc     hl, de                 ; datalen - blksize
                ret     nc                     ; datalen >= blksize: not the last
                ld      a, 1
                ld      (XFER_DONE), a
                ret
cl_send_ack:
                ; build ACK(ACK_BLOCK) — the accepted block, or the duplicate's
                ; received block on the re-ACK path.
                call    build_ack_frame_and_send
                ret

cl_stray:
                ; a DATA from a wrong server TID: ERROR(5) "unknown transfer ID"
                ; back to the stray sender, then discard (do not accept).
                ; learn the stray sender endpoint into REPLY_* for the wrap.
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
                ; build the ERROR packet at TPKT: opcode 5, code 5, msg, NUL.
                ld      hl, TPKT
                ld      (hl), 0
                inc     hl
                ld      (hl), OP_ERROR
                inc     hl
                ld      (hl), 0
                inc     hl
                ld      (hl), ERR_UNKNOWN_TID
                inc     hl                     ; HL = TPKT+4, dest cursor
                ld      de, err_unknown_tid_msg
ce_copy:
                ld      a, (de)
                ld      (hl), a
                inc     de
                inc     hl
                or      a
                jr      nz, ce_copy
                ; length = HL - TPKT
                ld      de, TPKT
                or      a
                sbc     hl, de
                push    hl                     ; payload length
                ; wrap to the stray sender (REPLY_*) and send.
                pop     bc
                jp      wrap_reply_to_stray

; ---------------------------------------------------------------------------
; tftp_recv_timeout — SAS fix: retransmit the last ACK only (never the RRQ).
; Out: BC = bytes transmitted, or 0 if no block has been ACKed yet.
; ---------------------------------------------------------------------------
tftp_recv_timeout:
                ld      hl, (ACKED)
                ld      a, h
                or      l
                jp      z, cl_none             ; nothing ACKed yet: caller re-sends RRQ
                ld      (ACK_BLOCK), hl        ; SAS: retransmit the last ACK (= ACKED)
                jp      build_ack_frame_and_send

; ---------------------------------------------------------------------------
; build_ack_frame_and_send — build ACK(ACK_BLOCK) at TPKT, wrap (our TID ->
; server TID), transmit. Out: BC = the transmitted frame length.
; ---------------------------------------------------------------------------
build_ack_frame_and_send:
                ; ACK packet: opcode 4 (big-endian), block (big-endian) = ACK_BLOCK.
                ld      hl, TPKT
                ld      (hl), 0
                inc     hl
                ld      (hl), OP_ACK
                inc     hl
                ld      a, (ACK_BLOCK+1)       ; block high byte (LE store -> BE wire)
                ld      (hl), a
                inc     hl
                ld      a, (ACK_BLOCK)         ; block low byte
                ld      (hl), a
                ld      bc, 4                  ; ACK is 4 bytes

                ; wrap our TID -> server TID.
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
                pop     bc                     ; BC = payload length (4)
                jp      wrap_reply

; ---------------------------------------------------------------------------
; wrap_reply / wrap_reply_to_stray — frame the TFTP packet at TPKT (length in
; BC) as a UDP datagram from us (CLIENT_*) to REPLY_* and transmit it. The two
; entries differ only in which dst the caller set in REPLY_* (server vs stray).
; Out: BC = the transmitted frame length.
; ---------------------------------------------------------------------------
wrap_reply_to_stray:
wrap_reply:
                ld      (TFTP_PKT_LEN), bc

                ; dst MAC <- REPLY_MAC
                ld      hl, REPLY_MAC
                ld      de, PARAM_DST_MAC
                ld      bc, 6
                ldir
                ; src MAC = ours
                ld      hl, CLIENT_MAC
                ld      de, PARAM_SRC_MAC
                ld      bc, 6
                ldir
                ; src IP = ours
                ld      hl, CLIENT_IP
                ld      de, PARAM_SRC_IP
                ld      bc, 4
                ldir
                ; dst IP <- REPLY_IP
                ld      hl, REPLY_IP
                ld      de, PARAM_DST_IP
                ld      bc, 4
                ldir
                ; src port = our TID, dst port = REPLY_TID
                ld      hl, CLIENT_TID
                ld      de, PARAM_SRC_PORT
                ld      bc, 2
                ldir
                ld      hl, REPLY_TID
                ld      de, PARAM_DST_PORT
                ld      bc, 2
                ldir
                ; payload = TPKT, length = TFTP_PKT_LEN
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

cl_none:
                ld      bc, 0
                ret

; --- constants -------------------------------------------------------------
err_unknown_tid_msg: defm "unknown transfer ID"
                     defb 0

; ===========================================================================
; Configuration + transfer state (harness / boot code fills CONFIG/CLIENT_* +
; CLIENT_BLKSIZE; the loop owns ACKED/STAGE_OFFSET/GOT_SERVER/etc).
; ===========================================================================
CLIENT_MAC:       defs 6
CLIENT_IP:        defs 4
CLIENT_TID:       defs 2                 ; our source port (BE)
CLIENT_BLKSIZE:   defs 2                 ; negotiated block size
WINDOWSIZE:       defs 2                 ; RFC 7440 window (from OACK); <=1 = lock-step
WINCOUNT:         defs 2                 ; in-sequence blocks taken since the last ACK

SERVER_MAC:       defs 6                 ; learned from the first DATA
SERVER_IP:        defs 4
SERVER_TID:       defs 2                 ; learned (BE)
GOT_SERVER:       defs 1

REPLY_MAC:        defs 6                 ; dst of the reply (server or stray)
REPLY_IP:         defs 4
REPLY_TID:        defs 2

ACKED:            defs 2                 ; highest block ACKed (LE value)
ACK_BLOCK:        defs 2                 ; the block the next ACK names (LE value)
STAGE_OFFSET:     defs 2                 ; bytes accumulated so far
LAST_DATA_LEN:    defs 2
XFER_DONE:        defs 1
CL_RX_LEN:        defs 2
TFTP_PKT_LEN:     defs 2
TPKT:             defs 64                ; the ACK/ERROR packet buffer
RXBUF:            defs 1518
STAGING:          defs 4096              ; accumulated file bytes (test inspects)

; ===========================================================================
; The host-verified packet pieces, composed into this translation unit.
; ===========================================================================
                include "build_udp_frame.asm"
                include "tftp_client.asm"
                include "encdrv.asm"
