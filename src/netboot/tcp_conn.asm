; tcp_conn.asm — the i70 TCP connection state machine (client active open).
;
; This is the SAM-side Z80 port of the netboot-oracle Go authority
; tools/netboot-oracle/tcp/conn.go::Conn — the client-side connection management
; the i70 HTTP self-provisioning fetch rides on. It composes the host-verified
; build_tcp_segment primitive + the real vendored driver (encdrv.asm) into one
; binary and runs them over the i80 emulated Trinity:
;
;   tcp_connect:
;     originate the active open — emit the SYN (seq=ISS, ack=0, FlagSYN),
;     drv_write it, advance sndNxt past the SYN's one consumed sequence number
;     (RFC 793 §3.3), state = SYN_SENT.
;   tcp_conn_recv:
;     drv_read one frame; parse + dispatch on CONN_STATE:
;       SYN_SENT  : SYN|ACK acking our SYN (ack == sndNxt)? rcvNxt = serverSeq+1,
;                   state = ESTABLISHED, send the handshake ACK.
;       ESTABLISHED: server FIN (seq == rcvNxt)? account any riding payload,
;                   rcvNxt += 1, send FIN|ACK, sndNxt += 1, state = FIN_WAIT.
;                   else in-order payload (seq == rcvNxt)? accumulate into
;                   CONN_DATA, rcvNxt += len, send ACK. A bare ACK or an
;                   out-of-order/duplicate segment is ignored (nothing sent).
;       FIN_WAIT  : ACK of our FIN (ack == sndNxt)? state = CLOSED, send nothing.
;
; This brick is connection management only: it originates the SYN and emits the
; control segments (ACK / FIN-ACK), but sends no application payload of its own
; (the HTTP/1.0 GET is the next brick). It accepts and ACKs inbound server data
; so the response body can flow, accumulating it into CONN_DATA.
;
; The dispatch, seq/ack arithmetic (SYN/FIN consume one; data consumes len), and
; the accumulate+ACK / teardown steps mirror Conn.OnSegment step for step; the
; segments are produced by build_tcp_segment, so the frames on the virtual wire
; are byte-for-byte the Go Conn authority output — the host check tcp_conn_test.go
; asserts.
;
; The one genuinely new Z80 piece vs the UDP loops is 32-bit big-endian seq/ack
; arithmetic (the rest of the stack is 16-bit). The four bytes are held in wire
; order (big-endian); tcp_inc32 / tcp_add16_to_32 operate from the low byte up,
; and tcp_cmp32 compares for equality.
;
; PROVENANCE: the connection state machine + the SYN/FIN-consume-one rule are
; RFC 793 (conn.go); the framing layout is build_tcp_segment (trinload-derived,
; Simon Owen BSD-style licence). VERIFICATION: host-verifiable end-to-end under
; the i80 emulation — inject the server's segment, build + drv_write the reply,
; assert each frame on the virtual wire matches the Go Conn byte-for-byte. NOT
; host-verifiable: a real TCP handshake against a live server — gated on real
; Trinity (CLAUDE.md §5). Emulation-verified is not hardware-verified.

                org     &8000

; Inbound frame offsets (mirror tcp/tcp.go; CRX_ prefix to avoid a clash with
; the build_tcp_segment TCP_OFF_* equates it includes). The connection is keyed
; by ports + the pre-configured server endpoint, so only the L4 fields are read.
CRX_TCP_SRCPORT:  equ 34                 ; big-endian on the wire
CRX_TCP_DSTPORT:  equ 36
CRX_TCP_SEQ:      equ 38                 ; 4 bytes big-endian
CRX_TCP_ACK:      equ 42                 ; 4 bytes big-endian
CRX_TCP_FLAGS:    equ 47
CRX_TCP_PAYLOAD:  equ 54                 ; data offset = 5 (no options) assumed

; TCP control flags (RFC 793).
CF_FIN:           equ &01
CF_SYN:           equ &02
CF_ACK:           equ &10

; Connection states.
ST_CLOSED:        equ 0
ST_SYN_SENT:      equ 1
ST_ESTABLISHED:   equ 2
ST_FIN_WAIT:      equ 3

; ---------------------------------------------------------------------------
; tcp_connect — originate the active open: emit the SYN, drv_write it, advance
; the send sequence past the SYN, state = SYN_SENT.
; In:  CONFIG/state block filled (incl. CONN_ISS); emulated Trinity attached.
; Out: BC = bytes transmitted (the SYN frame length).
; ---------------------------------------------------------------------------
tcp_connect:
                ; sndNxt = ISS (the SYN carries ISS as its seq).
                ld      hl, CONN_ISS
                ld      de, CONN_SND_NXT
                ld      bc, 4
                ldir
                ; ack = 0 in the SYN.
                xor     a
                ld      (CONN_RCV_NXT), a
                ld      (CONN_RCV_NXT+1), a
                ld      (CONN_RCV_NXT+2), a
                ld      (CONN_RCV_NXT+3), a
                ; build + send SYN, no payload.
                ld      a, CF_SYN
                ld      (CONN_TX_FLAGS), a
                ld      hl, 0
                ld      (CONN_TX_PAYLEN), hl
                call    conn_build_and_send
                push    bc                     ; save tx length to return
                ; sndNxt = ISS + 1 (SYN consumes one).
                call    tcp_inc32_sndnxt
                ld      a, ST_SYN_SENT
                ld      (CONN_STATE), a
                pop     bc
                ret

; ---------------------------------------------------------------------------
; tcp_conn_recv — read one frame and advance the connection, emitting a reply
; segment if the state machine produces one.
; Out: BC = bytes transmitted, or 0 if nothing was sent.
; ---------------------------------------------------------------------------
tcp_conn_recv:
                ld      hl, RXBUF
                call    drv_read
                ld      a, b
                or      c
                jp      z, conn_none           ; empty wire
                ld      (CONN_RX_LEN), bc      ; stash the received frame length

                ; dst port == our port? (2 big-endian bytes)
                ld      a, (RXBUF + CRX_TCP_DSTPORT)
                ld      hl, CONN_CLIENT_PORT
                cp      (hl)
                jp      nz, conn_none
                ld      a, (RXBUF + CRX_TCP_DSTPORT + 1)
                inc     hl
                cp      (hl)
                jp      nz, conn_none
                ; src port == server port?
                ld      a, (RXBUF + CRX_TCP_SRCPORT)
                ld      hl, CONN_SERVER_PORT
                cp      (hl)
                jp      nz, conn_none
                ld      a, (RXBUF + CRX_TCP_SRCPORT + 1)
                inc     hl
                cp      (hl)
                jp      nz, conn_none

                ; dispatch on state.
                ld      a, (CONN_STATE)
                cp      ST_SYN_SENT
                jp      z, conn_syn_sent
                cp      ST_ESTABLISHED
                jp      z, conn_established
                cp      ST_FIN_WAIT
                jp      z, conn_fin_wait
                jp      conn_none

; --- SYN_SENT: expect SYN|ACK acking our SYN -------------------------------
conn_syn_sent:
                ld      a, (RXBUF + CRX_TCP_FLAGS)
                ld      b, a
                and     CF_SYN
                jp      z, conn_none           ; no SYN bit
                ld      a, b
                and     CF_ACK
                jp      z, conn_none           ; no ACK bit
                ; ack (RXBUF+CRX_TCP_ACK, 4 BE) == sndNxt?
                ld      hl, RXBUF + CRX_TCP_ACK
                ld      de, CONN_SND_NXT
                call    tcp_cmp32
                jp      nz, conn_none          ; acks something we did not send
                ; rcvNxt = serverSeq + 1.
                ld      hl, RXBUF + CRX_TCP_SEQ
                ld      de, CONN_RCV_NXT
                ld      bc, 4
                ldir                           ; rcvNxt = serverSeq
                call    tcp_inc32_rcvnxt       ; +1 (their SYN consumes one)
                ld      a, ST_ESTABLISHED
                ld      (CONN_STATE), a
                ; send the handshake ACK (no payload).
                ld      a, CF_ACK
                ld      (CONN_TX_FLAGS), a
                ld      hl, 0
                ld      (CONN_TX_PAYLEN), hl
                jp      conn_build_and_send

; --- ESTABLISHED: FIN teardown, or in-order data ---------------------------
conn_established:
                ld      a, (RXBUF + CRX_TCP_FLAGS)
                and     CF_FIN
                jp      nz, conn_est_fin
                ; in-order data? seq == rcvNxt
                ld      hl, RXBUF + CRX_TCP_SEQ
                ld      de, CONN_RCV_NXT
                call    tcp_cmp32
                jp      nz, conn_none          ; out-of-order / duplicate: ignore
                ; payload length = frame length - 54 (header, no options).
                call    conn_payload_len       ; HL = payload length
                ld      a, h
                or      l
                jp      z, conn_none           ; bare ACK: nothing to send back
                ; accumulate the payload into CONN_DATA at the running offset,
                ; then advance rcvNxt by the same length. Save the length across
                ; conn_accumulate (which clobbers HL).
                push    hl                     ; save payload length
                call    conn_accumulate
                pop     hl                     ; HL = payload length
                call    tcp_add16_to_rcvnxt    ; rcvNxt += payload length
                ; send ACK (no payload).
                ld      a, CF_ACK
                ld      (CONN_TX_FLAGS), a
                ld      hl, 0
                ld      (CONN_TX_PAYLEN), hl
                jp      conn_build_and_send

conn_est_fin:
                ; FIN must be in order (seq == rcvNxt).
                ld      hl, RXBUF + CRX_TCP_SEQ
                ld      de, CONN_RCV_NXT
                call    tcp_cmp32
                jp      nz, conn_none
                ; account any payload riding on the FIN segment first.
                call    conn_payload_len       ; HL = payload length
                ld      a, h
                or      l
                jr      z, conn_fin_no_data
                push    hl
                call    conn_accumulate        ; uses HL = length
                pop     hl
                call    tcp_add16_to_rcvnxt    ; rcvNxt += payload length
conn_fin_no_data:
                call    tcp_inc32_rcvnxt       ; rcvNxt += 1 (FIN consumes one)
                ; send FIN|ACK (no payload).
                ld      a, CF_FIN | CF_ACK
                ld      (CONN_TX_FLAGS), a
                ld      hl, 0
                ld      (CONN_TX_PAYLEN), hl
                call    conn_build_and_send
                push    bc                     ; save tx length
                call    tcp_inc32_sndnxt       ; our FIN consumes one
                ld      a, ST_FIN_WAIT
                ld      (CONN_STATE), a
                pop     bc
                ret

; --- FIN_WAIT: final ACK of our FIN closes ---------------------------------
conn_fin_wait:
                ld      a, (RXBUF + CRX_TCP_FLAGS)
                and     CF_ACK
                jp      z, conn_none
                ld      hl, RXBUF + CRX_TCP_ACK
                ld      de, CONN_SND_NXT
                call    tcp_cmp32
                jp      nz, conn_none
                ld      a, ST_CLOSED
                ld      (CONN_STATE), a
                jp      conn_none              ; nothing to send

conn_none:
                ld      bc, 0
                ret

; ---------------------------------------------------------------------------
; conn_payload_len — payload length of the received frame = RX_LEN - 54.
; Out: HL = payload length. (Reads CONN_RX_LEN.)  Clobbers: DE.
; ---------------------------------------------------------------------------
conn_payload_len:
                ld      hl, (CONN_RX_LEN)
                ld      de, CRX_TCP_PAYLOAD
                or      a
                sbc     hl, de
                ret

; ---------------------------------------------------------------------------
; conn_accumulate — copy HL bytes of payload from RXBUF+54 into CONN_DATA at the
; running CONN_DATA_LEN offset, then advance CONN_DATA_LEN by HL.
; In:  HL = payload length.  Clobbers: A, BC, DE, HL.
; ---------------------------------------------------------------------------
conn_accumulate:
                push    hl                     ; save length
                ld      de, (CONN_DATA_LEN)
                ld      hl, CONN_DATA
                add     hl, de                 ; HL = dest
                ex      de, hl                 ; DE = dest
                ld      hl, RXBUF + CRX_TCP_PAYLOAD
                pop     bc                     ; BC = length
                push    bc
                ldir
                ; CONN_DATA_LEN += length.
                pop     bc                     ; BC = length again
                ld      hl, (CONN_DATA_LEN)
                add     hl, bc
                ld      (CONN_DATA_LEN), hl
                ret

; ---------------------------------------------------------------------------
; conn_build_and_send — fill the build_tcp_segment param block from the
; connection state (flags in CONN_TX_FLAGS, payload length in CONN_TX_PAYLEN,
; payload at CONN_TX_PAYLOAD), build the segment, drv_write it.
; Out: BC = the transmitted frame length.
; ---------------------------------------------------------------------------
conn_build_and_send:
                ; dst MAC = server, src MAC = client.
                ld      hl, CONN_SERVER_MAC
                ld      de, TPARAM_DST_MAC
                ld      bc, 6
                ldir
                ld      hl, CONN_CLIENT_MAC
                ld      de, TPARAM_SRC_MAC
                ld      bc, 6
                ldir
                ; src IP = client, dst IP = server.
                ld      hl, CONN_CLIENT_IP
                ld      de, TPARAM_SRC_IP
                ld      bc, 4
                ldir
                ld      hl, CONN_SERVER_IP
                ld      de, TPARAM_DST_IP
                ld      bc, 4
                ldir
                ; ports (big-endian, copied verbatim).
                ld      hl, CONN_CLIENT_PORT
                ld      de, TPARAM_SRC_PORT
                ld      bc, 2
                ldir
                ld      hl, CONN_SERVER_PORT
                ld      de, TPARAM_DST_PORT
                ld      bc, 2
                ldir
                ; seq = sndNxt, ack = rcvNxt (both held wire-order = big-endian).
                ld      hl, CONN_SND_NXT
                ld      de, TPARAM_SEQ
                ld      bc, 4
                ldir
                ld      hl, CONN_RCV_NXT
                ld      de, TPARAM_ACK
                ld      bc, 4
                ldir
                ; flags.
                ld      a, (CONN_TX_FLAGS)
                ld      (TPARAM_FLAGS), a
                ; window (big-endian).
                ld      a, CONN_WINDOW >> 8
                ld      (TPARAM_WINDOW), a
                ld      a, CONN_WINDOW & &ff
                ld      (TPARAM_WINDOW + 1), a
                ; payload pointer + length.
                ld      hl, CONN_TX_PAYLOAD
                ld      (TPARAM_PAYLOAD_PTR), hl
                ld      hl, (CONN_TX_PAYLEN)
                ld      (TPARAM_PAYLOAD_LEN), hl

                call    build_tcp_segment      ; segment at TCP_PACKET, BC = length
                push    bc
                ld      hl, TCP_PACKET
                call    drv_write
                pop     bc
                ret

; ===========================================================================
; 32-bit big-endian arithmetic helpers. The four bytes are stored in wire order
; (byte 0 = most significant, byte 3 = least significant), so arithmetic walks
; from offset+3 down to offset+0.
; ===========================================================================

; tcp_inc32_sndnxt / tcp_inc32_rcvnxt — increment the 4-byte BE value by 1.
tcp_inc32_sndnxt:
                ld      hl, CONN_SND_NXT
                jr      tcp_inc32
tcp_inc32_rcvnxt:
                ld      hl, CONN_RCV_NXT
tcp_inc32:
                ; HL -> 4-byte BE value. Add 1 with carry from the low byte up.
                inc     hl
                inc     hl
                inc     hl                     ; HL -> byte 3 (least significant)
                inc     (hl)
                ret     nz                     ; no carry out of byte 3
                dec     hl
                inc     (hl)                   ; byte 2
                ret     nz
                dec     hl
                inc     (hl)                   ; byte 1
                ret     nz
                dec     hl
                inc     (hl)                   ; byte 0 (most significant)
                ret

; tcp_add16_to_rcvnxt — rcvNxt += HL (a 16-bit unsigned addend), 32-bit BE.
; In:  HL = addend.  Clobbers: A, B, DE, HL.
tcp_add16_to_rcvnxt:
                ; add the low byte of HL to byte 3, then the high byte to byte 2,
                ; propagating carries up to byte 0.
                ld      d, h
                ld      e, l                   ; DE = addend (D high, E low)
                ld      hl, CONN_RCV_NXT + 3   ; least significant byte
                ld      a, (hl)
                add     a, e
                ld      (hl), a                ; byte 3 += low addend byte
                dec     hl                     ; HL -> byte 2
                ld      a, (hl)
                adc     a, d
                ld      (hl), a                ; byte 2 += high addend byte + carry
                ret     nc
                dec     hl                     ; HL -> byte 1
                inc     (hl)
                ret     nz
                dec     hl                     ; HL -> byte 0
                inc     (hl)
                ret

; tcp_cmp32 — compare two 4-byte BE values for equality.
; In:  HL -> value A, DE -> value B.  Out: Z set iff equal.  Clobbers: A, B, HL, DE.
tcp_cmp32:
                ld      b, 4
tcp_cmp32_loop:
                ld      a, (de)
                cp      (hl)
                ret     nz                     ; Z clear -> not equal
                inc     hl
                inc     de
                djnz    tcp_cmp32_loop
                ret                            ; Z set -> equal

; ===========================================================================
; Connection identity + state. The harness / boot code fills the CONFIG part
; (CONN_CLIENT_*, CONN_SERVER_*, CONN_ISS); the loop owns sndNxt/rcvNxt/state/
; the accumulation. Ports / seq / ack are stored big-endian (wire order).
; ===========================================================================
CONN_WINDOW:      equ 5840                 ; advertised receive window

CONN_CLIENT_MAC:  defs 6
CONN_CLIENT_IP:   defs 4
CONN_CLIENT_PORT: defs 2                   ; big-endian
CONN_SERVER_MAC:  defs 6
CONN_SERVER_IP:   defs 4
CONN_SERVER_PORT: defs 2                   ; big-endian
CONN_ISS:         defs 4                   ; initial send sequence (big-endian)

CONN_STATE:       defs 1
CONN_SND_NXT:     defs 4                   ; our next seq (big-endian)
CONN_RCV_NXT:     defs 4                   ; next expected server seq (big-endian)

CONN_TX_FLAGS:    defs 1                   ; flags for the next built segment
CONN_TX_PAYLEN:   defs 2                   ; payload length for the next segment
CONN_RX_LEN:      defs 2                   ; length of the last received frame

CONN_DATA_LEN:    defs 2                   ; accumulated inbound bytes
RXBUF:            defs 1518                ; received frame buffer
CONN_TX_PAYLOAD:  defs 1460                ; outbound payload staging (control segs use 0)
CONN_DATA:        defs 4096                ; accumulated response body (test inspects)

; ===========================================================================
; The host-verified packet primitive + the real driver, composed in.
; ===========================================================================
                include "build_tcp_segment.asm"
                include "encdrv.asm"
