; tftp_server_loop.asm — the i83 TFTP server transfer loop: the reply-driven
; wire-I/O state machine that serves a file to a netbooting Pi over TFTP.
;
; This is the SAM-side Z80 port of the netboot-oracle Go authority
; tools/netboot-oracle/tftp/serverloop.go::ServerLoop. It composes the already-
; host-verified packet pieces — parse_request / resolve (tftp_parse.asm),
; build_oack / build_data / build_error (tftp_build.asm), build_udp_frame — and
; the real vendored driver (encdrv.asm) into one binary, and runs them over the
; i80 emulated Trinity:
;
;   tftp_handle_rrq:
;     drv_read a frame; UDP dst port 69? parse_request; resolve the filename.
;       hit  -> build_oack (blksize echoed + tsize = the stored size), wrap as a
;               UDP frame (server TID -> client TID), drv_write; arm the transfer
;               (source ptr/size, blksize, next block = 1).
;       miss -> build_error(1, "File not found"), wrap, drv_write; serve nothing
;               (keep serving — the headline robustness rule). Serial-subdir
;               prefixes resolve to ERROR404 too (the Pi retries at root).
;   tftp_first_data:  after the client ACKs block 0, send DATA block 1.
;   tftp_handle_ack:  drv_read an ACK; advance the transfer; send the next DATA
;                     (a short block ends it) or report completion.
;
; The dispatch, the serve-by-name resolution, the OACK option string (blksize
; then tsize, the captured order), the streamed DATA cadence, and the short-
; final-block termination all mirror serverloop.go step for step; the payload +
; frame bytes are produced by the same builders the host harness already proved
; byte-exact (i92). So the OACK/DATA/ERROR frames on the virtual wire are
; byte-for-byte the Go ServerLoop authority output — the host check this file's
; test asserts.
;
; PROVENANCE: the reply-driven server loop + serve-by-name + ERROR(1)-on-miss-
; keep-serving + serial-subdir-404 are serverloop.go / server.go (oracle §2-§3);
; the TFTP wire format is RFC 1350 + 2347 (OACK). The reply framing uses
; build_udp_frame (server IP + transfer TID -> client IP + client TID), matching
; the captured exchange (server replies from an ephemeral TID to the client TID).
;
; VERIFICATION: host-verifiable end-to-end under the i80 emulation — drv_read a
; captured RRQ, build + drv_write the OACK, then the DATA/ACK loop, and assert
; each frame on the virtual wire matches the Go ServerLoop byte-for-byte
; (tftp_server_loop_test.go). NOT host-verifiable: an end-to-end Pi boot +
; real-silicon TX/RX timing — gated on real Trinity hardware (CLAUDE.md §5).
; Emulation-verified is not hardware-verified.

                org     &8000

; Frame offsets (mirror build_udp_frame.asm OFF_*); RX_ prefix to avoid a clash.
RX_ETH_SRCMAC:    equ 6
RX_IP_SRC:        equ 26
RX_UDP_SRCPORT:   equ 34                 ; big-endian on the wire
RX_UDP_DSTPORT:   equ 36
RX_UDP_PAYLOAD:   equ 42

TFTP_SERVER_PORT: equ 69
ACTION_OACK_:     equ 0                  ; mirror tftp.ActionOACK / RESOLVE_ACTION

; ---------------------------------------------------------------------------
; tftp_handle_rrq — read one frame, answer an RRQ for a stored file with an
; OACK (and arm the transfer) or a miss with ERROR(1).
;
; In:  the emulated Trinity is attached; CONFIG_* + the store + the source are
;      filled. Out: BC = bytes transmitted (the reply frame length), or 0 if the
;      frame was not a TFTP RRQ to port 69 (nothing sent).
; ---------------------------------------------------------------------------
tftp_handle_rrq:
                ; --- receive into RXBUF ---------------------------------
                ld      hl, RXBUF
                call    drv_read
                ld      a, b
                or      c
                jp      z, srv_none
                ld      (SRV_RX_LEN), bc

                ; --- UDP dst port == 69? --------------------------------
                ld      a, (RXBUF + RX_UDP_DSTPORT)
                or      a
                jp      nz, srv_none
                ld      a, (RXBUF + RX_UDP_DSTPORT + 1)
                cp      TFTP_SERVER_PORT
                jp      nz, srv_none

                ; --- learn the client endpoint --------------------------
                ; client MAC <- Ethernet source MAC (6)
                ld      hl, RXBUF + RX_ETH_SRCMAC
                ld      de, CLIENT_MAC
                ld      bc, 6
                ldir
                ; client IP <- IP source (4)
                ld      hl, RXBUF + RX_IP_SRC
                ld      de, CLIENT_IP
                ld      bc, 4
                ldir
                ; client TID <- UDP source port (2, big-endian as-is)
                ld      hl, RXBUF + RX_UDP_SRCPORT
                ld      de, CLIENT_TID
                ld      bc, 2
                ldir

                ; --- parse the RRQ payload ------------------------------
                ; copy the UDP payload (frame length - 42 bytes) into the
                ; parser's RRQ_IN buffer.
                ld      hl, (SRV_RX_LEN)
                ld      de, RX_UDP_PAYLOAD
                or      a
                sbc     hl, de                 ; HL = payload length
                ld      (RRQ_IN_LEN), hl
                push    hl
                pop     bc                     ; BC = payload length
                ld      hl, RXBUF + RX_UDP_PAYLOAD
                ld      de, RRQ_IN
                ldir
                call    parse_request
                ld      a, (PARSE_OK)
                or      a
                jp      z, srv_none            ; not a valid RRQ/WRQ

                ; --- resolve the filename -------------------------------
                ld      hl, (PARSE_FILENAME)
                ld      (RESOLVE_NAME_PTR), hl
                call    resolve
                ld      a, (RESOLVE_ACTION)
                cp      ACTION_OACK_
                jr      z, srv_hit

                ; --- miss: ERROR(1, "File not found") -------------------
                ld      hl, 1
                ld      a, h
                ld      (ERR_CODE), a
                ld      a, l
                ld      (ERR_CODE+1), a        ; big-endian code = 0x0001
                ld      hl, err_notfound_msg
                ld      (ERR_MSG_PTR), hl
                call    build_error            ; packet at TBUF, BC = length
                ; mark the transfer inactive
                xor     a
                ld      (XFER_ACTIVE), a
                jp      srv_send_tbuf

srv_hit:
                ; --- arm the transfer ----------------------------------
                ; copy the stored size (4-byte LE) into XFER_SIZE.
                ld      hl, RESOLVE_SIZE
                ld      de, XFER_SIZE
                ld      bc, 4
                ldir
                ; next block = 1, offset = 0, active = 1
                ld      hl, 1
                ld      (XFER_NEXT_BLK), hl
                ld      hl, 0
                ld      (XFER_OFFSET), hl
                ld      (XFER_OFFSET+2), hl
                ld      a, 1
                ld      (XFER_ACTIVE), a

                ; --- negotiate the blksize -----------------------------
                ; find the request's "blksize" option, parse + clamp it
                ; (AcceptedBlksize: 8..1468 echoed, else the 512 default).
                call    negotiate_blksize      ; -> XFER_BLKSIZE

                ; --- build the OACK option string ----------------------
                ; "blksize\0<bs>\0tsize\0<size>\0" into OACK_OPTS; BC = length.
                call    build_oack_opts
                ld      (OACK_OPTS_LEN), hl    ; HL = the option byte count

                call    build_oack             ; packet at TBUF, BC = length
                jp      srv_send_tbuf

; ---------------------------------------------------------------------------
; tftp_first_data — send DATA block 1 (after the client ACKs block 0 of the
; OACK). Out: BC = bytes transmitted, or 0 if no transfer is armed.
; ---------------------------------------------------------------------------
tftp_first_data:
                ld      a, (XFER_ACTIVE)
                or      a
                jp      z, srv_none
                jp      send_next_data

; ---------------------------------------------------------------------------
; tftp_handle_ack — read an ACK and send the next DATA, or finish.
; Out: BC = bytes transmitted (the next DATA frame), or 0 if the transfer is
;      complete (the short final block was ACKed) or the ACK is unexpected.
; ---------------------------------------------------------------------------
tftp_handle_ack:
                ld      a, (XFER_ACTIVE)
                or      a
                jp      z, srv_none

                ld      hl, RXBUF
                call    drv_read
                ld      a, b
                or      c
                jp      z, srv_none
                ld      (SRV_RX_LEN), bc

                ; opcode 4 (ACK)? payload[0..1] big-endian == 0x0004
                ld      a, (RXBUF + RX_UDP_PAYLOAD)
                or      a
                jp      nz, srv_none
                ld      a, (RXBUF + RX_UDP_PAYLOAD + 1)
                cp      4
                jp      nz, srv_none

                ; ACKed block number (big-endian) -> DE
                ld      a, (RXBUF + RX_UDP_PAYLOAD + 2)
                ld      d, a
                ld      a, (RXBUF + RX_UDP_PAYLOAD + 3)
                ld      e, a                   ; DE = acked block

                ; Did we just send the short final block, now ACKed? If the last
                ; DATA payload was < blksize and its block == acked, finish.
                ld      a, (XFER_LAST_SHORT)
                or      a
                jr      z, ack_advance
                ; final block was short; this ACK completes the transfer.
                xor     a
                ld      (XFER_ACTIVE), a
                jp      srv_none               ; nothing more to send

ack_advance:
                ; advance: next block = acked + 1, offset += last payload len.
                inc     de
                ld      (XFER_NEXT_BLK), de
                jp      send_next_data

; ---------------------------------------------------------------------------
; send_next_data — read the next block from the source, build the DATA packet,
; wrap + transmit. Updates XFER_OFFSET and XFER_LAST_SHORT.
; Out: BC = bytes transmitted.
; ---------------------------------------------------------------------------
send_next_data:
                ; remaining = XFER_SIZE - XFER_OFFSET (32-bit). For our files this
                ; fits 16 bits comfortably; compute a 16-bit chunk length capped
                ; at the negotiated blksize.
                ; chunk = min(blksize, remaining)
                ; remaining (low 16) = size_lo - offset_lo (sizes here < 64 KB).
                ld      hl, (XFER_SIZE)        ; low 16 of size
                ld      de, (XFER_OFFSET)      ; low 16 of offset
                or      a
                sbc     hl, de                 ; HL = remaining (low 16)
                ; cap at blksize
                ld      de, (XFER_BLKSIZE)
                ; if remaining >= blksize, chunk = blksize, else chunk = remaining
                push    hl                     ; save remaining
                or      a
                sbc     hl, de                 ; remaining - blksize
                pop     hl                     ; HL = remaining
                jr      c, snd_use_remaining   ; remaining < blksize
                ld      hl, (XFER_BLKSIZE)     ; chunk = blksize
                xor     a
                ld      (XFER_LAST_SHORT), a   ; full block
                jr      snd_have_chunk
snd_use_remaining:
                ; HL = remaining (< blksize) -> short (final) block
                ld      a, 1
                ld      (XFER_LAST_SHORT), a
snd_have_chunk:
                ld      (DATA_LEN), hl

                ; data pointer = SOURCE + offset
                ld      hl, (XFER_OFFSET)
                ld      de, (SRC_PTR)
                add     hl, de
                ld      (DATA_PTR), hl

                ; block number (big-endian) <- XFER_NEXT_BLK (stored LE)
                ld      a, (XFER_NEXT_BLK+1)
                ld      (DATA_BLOCK), a        ; high byte first (big-endian)
                ld      a, (XFER_NEXT_BLK)
                ld      (DATA_BLOCK+1), a

                call    build_data             ; packet at TBUF, BC = length

                ; advance offset by the chunk length just sent
                ld      hl, (XFER_OFFSET)
                ld      de, (DATA_LEN)
                add     hl, de
                ld      (XFER_OFFSET), hl

                jp      srv_send_tbuf

; ---------------------------------------------------------------------------
; srv_send_tbuf — wrap the TFTP packet at TBUF (length in BC) as a UDP frame
; (server IP + server TID -> client IP + client TID) and transmit it.
; Out: BC = the transmitted frame length.
; ---------------------------------------------------------------------------
srv_send_tbuf:
                ld      (TFTP_PKT_LEN), bc

                ; dst MAC = client MAC
                ld      hl, CLIENT_MAC
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
                ; dst IP = client IP
                ld      hl, CLIENT_IP
                ld      de, PARAM_DST_IP
                ld      bc, 4
                ldir
                ; src port = server TID, dst port = client TID (both big-endian)
                ld      hl, CONFIG_SERVERTID
                ld      de, PARAM_SRC_PORT
                ld      bc, 2
                ldir
                ld      hl, CLIENT_TID
                ld      de, PARAM_DST_PORT
                ld      bc, 2
                ldir
                ; payload = TBUF, length = TFTP_PKT_LEN
                ld      hl, TBUF
                ld      (PARAM_PAYLOAD_PTR), hl
                ld      hl, (TFTP_PKT_LEN)
                ld      (PARAM_PAYLOAD_LEN), hl

                call    build_udp_frame        ; frame at PACKET, BC = length

                push    bc                     ; save for the return
                ld      hl, PACKET
                call    drv_write
                pop     bc
                ret

srv_none:
                ld      bc, 0
                ret

; ---------------------------------------------------------------------------
; negotiate_blksize — find the request's "blksize" option, parse its decimal
; value, clamp it (AcceptedBlksize: 8..1468 echoed, else 512), store in
; XFER_BLKSIZE. Default 512 when absent or out of range. Mirrors
; tftp.AcceptedBlksize + the OnRRQ blksize lookup in serverloop.go.
;
; Options live in RRQ_IN as NUL-terminated name/value pairs; PARSE_OPTS points
; at the first option name, PARSE_OPT_COUNT holds the pair count. Clobbers
; A, BC, DE, HL.
; ---------------------------------------------------------------------------
negotiate_blksize:
                ; default 512
                ld      hl, 512
                ld      (XFER_BLKSIZE), hl

                ld      bc, (PARSE_OPT_COUNT)
                ld      a, b
                or      c
                ret     z                      ; no options: keep 512
                ld      hl, (PARSE_OPTS)        ; HL -> first option name
nb_loop:
                ; compare the name at HL with "blksize"
                push    bc
                push    hl
                ld      de, str_blksize
                call    streq_cstr             ; CY set if equal; HL/DE advanced past NUL on match
                jr      c, nb_found
                pop     hl
                ; skip this name and its value (two NUL-terminated strings)
                call    skip_cstr
                call    skip_cstr
                pop     bc
                dec     bc
                ld      a, b
                or      c
                jr      nz, nb_loop
                ret                            ; "blksize" not present: keep 512
nb_found:
                ; HL (from streq) points at the value string; parse it.
                pop     de                     ; discard the saved name ptr
                pop     bc                     ; discard the saved count
                call    parse_dec_u16          ; HL = value (0 on bad)
                ; clamp: 8 <= v <= 1468 -> echo; else 512.
                ld      de, 8
                or      a
                sbc     hl, de
                add     hl, de                 ; restore HL (flags from the compare)
                jr      c, nb_default          ; v < 8
                ld      de, 1469
                or      a
                sbc     hl, de                 ; v - 1469
                add     hl, de
                jr      nc, nb_default         ; v >= 1469 (i.e. > 1468)
                ld      (XFER_BLKSIZE), hl      ; in range: echo it
                ret
nb_default:
                ld      hl, 512
                ld      (XFER_BLKSIZE), hl
                ret

; streq_cstr — compare the NUL-terminated string at HL with the one at DE.
; Out: CY set if equal, with HL advanced to just past the matched NUL (the next
; field); CY clear and HL unspecified otherwise. Clobbers A, DE.
streq_cstr:
                ld      a, (de)
                cp      (hl)
                jr      nz, sq_no
                or      a                       ; both equal; was it the NUL?
                jr      z, sq_yes
                inc     hl
                inc     de
                jr      streq_cstr
sq_yes:
                inc     hl                      ; HL -> next field (past the NUL)
                scf
                ret
sq_no:
                or      a
                ret

; skip_cstr — advance HL past a NUL-terminated string. Clobbers A.
skip_cstr:
                ld      a, (hl)
                inc     hl
                or      a
                jr      nz, skip_cstr
                ret

; parse_dec_u16 — parse the decimal ASCII string at HL into HL (16-bit, wraps on
; overflow; stops at the NUL or first non-digit). Clobbers A, BC, DE.
parse_dec_u16:
                ld      de, 0                   ; accumulator
pd_loop:
                ld      a, (hl)
                sub     "0"
                jr      c, pd_done              ; < '0'
                cp      10
                jr      nc, pd_done             ; > '9'
                ; acc = acc*10 + digit
                push    hl
                ld      h, d
                ld      l, e                    ; HL = acc
                add     hl, hl                  ; *2
                push    hl
                add     hl, hl                  ; *4
                add     hl, hl                  ; *8
                pop     bc                      ; BC = acc*2
                add     hl, bc                  ; *8 + *2 = *10
                ld      e, a
                ld      d, 0
                add     hl, de                  ; + digit
                ld      d, h
                ld      e, l                    ; acc = HL
                pop     hl
                inc     hl
                jr      pd_loop
pd_done:
                ex      de, hl                  ; HL = accumulator
                ret

; ---------------------------------------------------------------------------
; build_oack_opts — format the OACK option string into OACK_OPTS:
;   "blksize" NUL <blksize-decimal> NUL "tsize" NUL <size-decimal> NUL
; <blksize> = XFER_BLKSIZE; <size> = XFER_SIZE (low 32, here < 64 KB).
; Out: HL = the byte count written. Clobbers A, BC, DE, HL.
; ---------------------------------------------------------------------------
build_oack_opts:
                ld      de, OACK_OPTS          ; output cursor
                ; "blksize\0"
                ld      hl, str_blksize
                call    copy_cstr_incl_nul
                ; <blksize> decimal NUL
                ld      hl, (XFER_BLKSIZE)
                call    write_dec_u16          ; writes digits at DE, advances DE
                xor     a
                ld      (de), a
                inc     de
                ; "tsize\0"
                ld      hl, str_tsize
                call    copy_cstr_incl_nul
                ; <size> decimal NUL
                ld      hl, (XFER_SIZE)        ; low 16 of size
                call    write_dec_u16
                xor     a
                ld      (de), a
                inc     de
                ; length = DE - OACK_OPTS
                ld      hl, OACK_OPTS
                ex      de, hl                 ; HL = cursor, DE = OACK_OPTS
                or      a
                sbc     hl, de                 ; HL = length
                ret

; copy_cstr_incl_nul — copy a NUL-terminated string at HL to DE, including the
; NUL. HL advances past the NUL; DE advances past the copied NUL.
copy_cstr_incl_nul:
                ld      a, (hl)
                ld      (de), a
                inc     hl
                inc     de
                or      a
                jr      nz, copy_cstr_incl_nul
                ret

; write_dec_u16 — write HL as a decimal ASCII string at DE (no leading zeros,
; "0" for zero). DE advances past the last digit. Clobbers A, BC, HL.
;
; Digits come out least-significant-first, so they are pushed on the stack and
; popped back in most-significant order. A NUL sentinel marks the bottom of the
; pushed run so the pop loop needs no separate counter (div_hl_10 clobbers B).
write_dec_u16:
                ld      a, &ff
                push    af                     ; sentinel (high byte 0xFF won't be a digit)
wd_div_loop:
                call    div_hl_10              ; HL = HL/10, A = HL mod 10
                add     a, "0"                 ; digit -> ASCII (pyz80: "x" char literal)
                push    af                     ; stack the digit char
                ld      a, h
                or      l
                jr      nz, wd_div_loop
wd_pop_loop:
                pop     af
                cp      &ff
                jr      z, wd_done             ; hit the sentinel: all digits written
                ld      (de), a
                inc     de
                jr      wd_pop_loop
wd_done:
                ret

; div_hl_10 — HL = HL / 10, A = HL mod 10.
;
; Restoring shift-subtract long division: shift the 16-bit dividend (HL) left
; bit by bit into a running remainder (C); whenever the remainder reaches 10,
; subtract 10 and set the corresponding quotient bit. After 16 iterations HL
; holds the quotient (built in place from the shifted-out bits) and C the
; remainder. Clobbers B, C, DE.
div_hl_10:
                ld      c, 0                   ; C = remainder accumulator
                ld      b, 16                  ; 16 dividend bits
dh_bit:
                add     hl, hl                 ; HL <<= 1; MSB -> CY (the next bit)
                ld      a, c
                rla                            ; A = (remainder << 1) | CY
                cp      10
                jr      c, dh_no_sub           ; remainder < 10: quotient bit 0
                sub     10                      ; remainder -= 10
                inc     l                       ; set the new quotient LSB (HL bit0=0 after shift)
dh_no_sub:
                ld      c, a                   ; store updated remainder
                djnz    dh_bit
                ld      a, c                   ; A = remainder
                ret

; --- constants -------------------------------------------------------------
err_notfound_msg: defm "File not found"
                  defb 0
str_blksize:      defm "blksize"
                  defb 0
str_tsize:        defm "tsize"
                  defb 0

; ===========================================================================
; Configuration + transfer state (harness / boot code fills CONFIG_* + the
; store + the source; the loop owns the XFER_* state and CLIENT_* endpoint).
; ===========================================================================
CONFIG_SERVERMAC: defs 6
CONFIG_SERVERIP:  defs 4
CONFIG_SERVERTID: defs 2                 ; the SAM's ephemeral source port (BE)

CLIENT_MAC:       defs 6
CLIENT_IP:        defs 4
CLIENT_TID:       defs 2                 ; the client's RRQ source port (BE)

SRC_PTR:          defs 2                 ; pointer to the file source bytes
XFER_BLKSIZE:     defs 2                 ; negotiated block size
XFER_SIZE:        defs 4                 ; file size (LE)
XFER_OFFSET:      defs 4                 ; bytes streamed so far (LE)
XFER_NEXT_BLK:    defs 2                 ; next block number (LE)
XFER_ACTIVE:      defs 1                 ; 1 = a transfer is armed
XFER_LAST_SHORT:  defs 1                 ; 1 = the last DATA was a short block
SRV_RX_LEN:       defs 2
TFTP_PKT_LEN:     defs 2
RXBUF:            defs 1518

; ===========================================================================
; The host-verified packet pieces, composed into this translation unit.
; ===========================================================================
                include "build_udp_frame.asm"
                include "tftp_build.asm"
                include "tftp_parse.asm"
                include "encdrv.asm"
