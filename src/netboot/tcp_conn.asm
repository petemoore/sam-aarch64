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
; Inbound body bytes take one of two paths, selected by CONN_SINK_ENABLED (the
; port of conn.go's nil-sink check; default 0 = legacy):
;   0  : accumulate the whole body into CONN_DATA (the legacy behavior).
;   !=0: stream (i99) — buffer into a bounded CONN_FLUSH_WINDOW and flush a full
;        window to storage_sink_flush as it fills, the partial remainder at the
;        FIN (conn_flush_final). CONN_DATA never holds the whole body — the RAM
;        bound for a multi-MB firmware fetch. The wire behavior (ACK cadence,
;        seq/ack arithmetic, every emitted frame) is byte-identical either way.
; The real B-DOS bounded write binds in storage_sink_flush (q16/hardware-gated);
; here it is a recording test double (CONN_SINK_OUT / CONN_SINK_CHUNKS).
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

                ; tcp_conn is the org provider for every netboot build that
                ; composes it (the standalone tcp_conn, http_get, netboot_http and
                ; the host-test http_main binaries all inherit this &8000). The one
                ; exception is the real bootable (NETBOOT_STREAM): there the
                ; composing http_main.asm owns the org so it can place a `jp
                ; http_main` at &8000 — the address the boot AUTO BASIC CALLs.
                if defined(NETBOOT_STREAM)==0
                org     &8000
                endif

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
                ; end-of-body: drain the partial remainder to the sink (streaming
                ; mode only; a no-op otherwise). Mirrors conn.go::OnSegment, which
                ; calls flushFinal() before the rcvNxt++ for the FIN. This touches
                ; the body sink only — never rcvNxt/sndNxt or the emitted segment.
                call    conn_flush_final
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
; conn_accumulate — record one in-order payload of HL bytes from RXBUF+54.
;
; Two modes, selected by CONN_SINK_ENABLED (the Z80 port of conn.go's nil-sink
; check). The wire behavior (ACK cadence, seq/ack arithmetic) is identical in
; both modes — only where the bytes land differs:
;
;   CONN_SINK_ENABLED == 0  (default, legacy): copy the payload into CONN_DATA
;     at the running CONN_DATA_LEN offset and advance CONN_DATA_LEN by HL. This
;     path is byte-for-byte the original accumulate behavior.
;
;   CONN_SINK_ENABLED != 0  (streaming, i99): buffer the payload into
;     CONN_FLUSH_BUF and flush full CONN_FLUSH_WINDOW windows to
;     storage_sink_flush as the buffer fills, keeping any overflow for the next
;     flush (the port of conn.go::acceptPayload's loop). CONN_DATA is left
;     untouched, so it never holds the whole body — the i99 RAM bound.
;
; In:  HL = payload length.  Clobbers: A, BC, DE, HL.
; ---------------------------------------------------------------------------
conn_accumulate:
                if defined(NETBOOT_HOSTTEST) | defined(NETBOOT_STREAM)
                ; The streaming sink builds into the host harness
                ; (NETBOOT_HOSTTEST) and the real bootable (NETBOOT_STREAM).
                ; CONN_SINK_ENABLED selects the path at run time: 0 (the default)
                ; takes the legacy CONN_DATA accumulate; the bootable sets it to 1
                ; so the body streams in bounded windows to the HSAVE store leaf.
                ld      a, (CONN_SINK_ENABLED)
                or      a
                jp      nz, conn_accumulate_sink
                endif
                ; --- legacy: append to CONN_DATA ---
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

                if defined(NETBOOT_HOSTTEST) | defined(NETBOOT_STREAM)
; --- streaming: append to CONN_FLUSH_BUF, flush full windows -----------------
; Mirrors conn.go::acceptPayload's sink branch. First copy the whole payload to
; the tail of CONN_FLUSH_BUF (advancing CONN_FLUSH_LEN), then flush as many full
; windows as the buffer now holds, copying any overflow down to the front so the
; backing store stays bounded (the Z80 analogue of the Go copy-down reslice).
; Built into both the host harness (NETBOOT_HOSTTEST) and the real bootable
; (NETBOOT_STREAM): the bootable flushes each full window to the HSAVE store leaf.
conn_accumulate_sink:
                ; --- append payload to the buffer tail ---
                push    hl                     ; save payload length
                ld      de, (CONN_FLUSH_LEN)
                ld      hl, CONN_FLUSH_BUF
                add     hl, de                 ; HL = dest (buffer tail)
                ex      de, hl                 ; DE = dest
                ld      hl, RXBUF + CRX_TCP_PAYLOAD
                pop     bc                     ; BC = payload length
                push    bc
                ldir
                pop     bc                     ; BC = payload length again
                ld      hl, (CONN_FLUSH_LEN)
                add     hl, bc
                ld      (CONN_FLUSH_LEN), hl   ; CONN_FLUSH_LEN += payload length
                ; --- flush every full window now buffered ---
conn_flush_loop:
                ; while CONN_FLUSH_LEN >= CONN_FLUSH_WINDOW:
                ld      hl, (CONN_FLUSH_WINDOW)
                ex      de, hl                 ; DE = window
                ld      hl, (CONN_FLUSH_LEN)
                or      a
                sbc     hl, de                 ; HL = LEN - WINDOW, C set if LEN < WINDOW
                ret     c                      ; buffer below a window: done
                ; one full window: flush it, then drop it from the front.
                push    hl                     ; save overflow count (LEN - WINDOW)
                ld      hl, (CONN_FLUSH_WINDOW)
                call    storage_sink_flush     ; flush HL = window bytes from front
                ; copy the overflow down to the front: CONN_FLUSH_BUF <- tail.
                pop     bc                     ; BC = overflow count
                ld      (CONN_FLUSH_LEN), bc   ; remaining bytes = overflow count
                ld      a, b
                or      c
                jr      z, conn_flush_loop     ; nothing to copy down
                ld      hl, (CONN_FLUSH_WINDOW)
                ld      de, CONN_FLUSH_BUF
                add     hl, de                 ; HL = source (buf + window) for ldir
                ld      de, CONN_FLUSH_BUF     ; DE = dest (front) for ldir
                ldir
                jr      conn_flush_loop
                endif

; ---------------------------------------------------------------------------
; conn_flush_final — drain any partial remainder to the sink at end-of-body (the
; FIN). The port of conn.go::flushFinal: a no-op in legacy mode (CONN_DATA holds
; everything) and when the buffer is empty (an empty body emits no spurious
; zero-length flush). In streaming mode with a non-empty buffer, flush the
; remainder and reset CONN_FLUSH_LEN to 0.
;
; This touches the body sink ONLY — it never reads or writes rcvNxt / sndNxt nor
; emits a segment, so the FIN's seq/ack arithmetic is unaffected (mirrors the
; ordering in conn.go::OnSegment: flushFinal() precedes the rcvNxt++ for the FIN).
; Clobbers: A, BC, DE, HL.
; ---------------------------------------------------------------------------
conn_flush_final:
                if defined(NETBOOT_HOSTTEST) | defined(NETBOOT_STREAM)
                ld      a, (CONN_SINK_ENABLED)
                or      a
                ret     z                      ; legacy: CONN_DATA already complete
                ld      hl, (CONN_FLUSH_LEN)
                ld      a, h
                or      l
                ret     z                      ; empty buffer: no zero-length flush
                call    storage_sink_flush     ; flush HL = remainder bytes
                ld      hl, 0
                ld      (CONN_FLUSH_LEN), hl
                ret
                else
                ret                            ; streaming not built into this target
                endif

                if defined(NETBOOT_HOSTTEST) | defined(NETBOOT_STREAM)
; ---------------------------------------------------------------------------
; storage_sink_flush — the Brick 3 dispatcher: sets HL = CONN_FLUSH_BUF (the
; front of the flush buffer), BC = flush length (passed in HL by the caller),
; then dispatches on CONN_SINK_FILTER_MODE:
;   0 (default) — tail-calls storage_sink_leaf directly (raw bytes, hash +
;                 record the whole window; preserves the pre-Brick-3 behaviour).
;   1           — tail-calls CONN_SINK_FILTER (default = storage_sink_leaf;
;                 the composed build sets it to body_sink_write, the HTTP
;                 header-skip filter, which forwards body bytes to
;                 storage_sink_leaf via BODY_DST_PTR).
;
; A function pointer (CONN_SINK_FILTER) is used instead of a hardcoded
; `jp body_sink_write` because tcp_conn.asm does not include body_sink.asm —
; the label body_sink_write is only defined in the composed http_main build.
; Mode 0 still calls storage_sink_leaf directly (no indirection cost on the
; hot, unfiltered path used by all existing stream tests).
;
; In:  HL = flush length.
; Clobbers: A, BC, DE, HL, IX (and whatever the callee clobbers).
; ---------------------------------------------------------------------------
storage_sink_flush:
                ld      b, h
                ld      c, l                   ; BC = flush length
                ld      hl, CONN_FLUSH_BUF     ; HL = pointer to the window bytes
                ld      a, (CONN_SINK_FILTER_MODE)
                or      a
                jr      z, ssf_leaf            ; mode 0: raw (default, no indirection)
                ; mode 1: load the filter ptr into IX and tail-call through it.
                ; HL (data ptr) and BC (length) are preserved for the callee.
                ld      ix, (CONN_SINK_FILTER)
                jp      (ix)
ssf_leaf:
                jp      storage_sink_leaf
                endif  ; NETBOOT_HOSTTEST | NETBOOT_STREAM (flush dispatcher)

                if defined(NETBOOT_HOSTTEST)
; ---------------------------------------------------------------------------
; storage_sink_leaf — the hash + record body (the host-test recording double).
; Takes an arbitrary ptr + len so body_sink_write can call it after the header
; skip (the body starts at an offset inside the window, not necessarily at
; CONN_FLUSH_BUF front).
;
; This is a TEST DOUBLE for the real B-DOS bounded write (q16/hardware-gated):
; host builds record the streamed bytes here so the oracle tests can inspect
; them. The real bootable (NETBOOT_STREAM, no NETBOOT_HOSTTEST) supplies its own
; storage_sink_leaf in http_main.asm — the fw_span + bdos_seam HSAVE-per-record
; store — so this recording double is host-test-only.
;
; In:  HL = pointer to bytes to hash + record, BC = length.
; Clobbers: A, BC, DE, HL, IX.
; ---------------------------------------------------------------------------
storage_sink_leaf:
                ; Hash this window into the running SHA-256 (the streamed-body
                ; verify, i100). Every body byte passes through here exactly once,
                ; in arrival order, so sha256_update over the window covers the whole
                ; body in order — the digest matches crypto/sha256 of the body.
                ; conn_verify_init resets the SHA state before the body arrives.
                push    hl                     ; save ptr across the hash
                push    bc                     ; save length across the hash
                call    sha256_update          ; hash BC bytes at HL (clobbers IX + scratch)
                pop     bc                     ; restore length
                pop     hl                     ; restore ptr
                push    bc                     ; save length for the recorder
                ; append [HL, HL+BC) to CONN_SINK_OUT at its tail.
                ld      de, (CONN_SINK_OUT_LEN)
                push    hl                     ; save source ptr
                ld      hl, CONN_SINK_OUT
                add     hl, de                 ; HL = dest (sink-out tail)
                ex      de, hl                 ; DE = dest
                pop     hl                     ; HL = source
                ldir                           ; copy BC bytes (BC consumed)
                ; CONN_SINK_OUT_LEN += length.
                pop     bc                     ; BC = length again
                push    bc
                ld      hl, (CONN_SINK_OUT_LEN)
                add     hl, bc
                ld      (CONN_SINK_OUT_LEN), hl
                ; record this flush length into CONN_SINK_CHUNKS[count] (2 bytes
                ; little-endian) and bump the count.
                ld      hl, (CONN_SINK_CHUNK_COUNT)
                add     hl, hl                 ; *2 (each entry is 2 bytes)
                ld      de, CONN_SINK_CHUNKS
                add     hl, de                 ; HL = &CONN_SINK_CHUNKS[count]
                pop     bc                     ; BC = length
                ld      (hl), c
                inc     hl
                ld      (hl), b                ; store length (low, high)
                ld      hl, (CONN_SINK_CHUNK_COUNT)
                inc     hl
                ld      (CONN_SINK_CHUNK_COUNT), hl
                ret
                endif  ; NETBOOT_HOSTTEST (recording double)

                if defined(NETBOOT_HOSTTEST) | defined(NETBOOT_STREAM)
CONN_SINK_FILTER_MODE: defb 0             ; 0 = raw (storage_sink_leaf); 1 = via CONN_SINK_FILTER
CONN_SINK_FILTER: defw  storage_sink_leaf ; filter fn pointer; composed build sets body_sink_write

; ---------------------------------------------------------------------------
; conn_verify_init — start the streamed-body SHA-256 (i100, q15 option c). Reset
; the hash state so storage_sink_flush hashes the body fresh. The host test calls
; this once, before the body arrives (alongside enabling the sink); on hardware
; the http_main migration will call it at the same point. Resetting the hash is
; separate from CONN_HASH_MATCH (which conn_verify_final sets), so this only needs
; sha256_init.
; In:  (nothing)   Out: (nothing)   Clobbers: A, BC, DE, HL.
; ---------------------------------------------------------------------------
conn_verify_init:
                call    sha256_init
                ret

; ---------------------------------------------------------------------------
; conn_verify_final — finish the streamed-body hash and check it against the
; pinned hash. Writes the 32-byte digest of the whole streamed body into
; CONN_HASH, then byte-compares CONN_HASH against CONN_PINNED_HASH (which the
; caller fills before the fetch) and sets CONN_HASH_MATCH = 1 on a full match,
; 0 otherwise. Call this at end-of-body — after the FIN flush has drained the
; partial remainder through storage_sink_flush (so every body byte is hashed).
; In:  CONN_PINNED_HASH filled.   Out: CONN_HASH = digest; CONN_HASH_MATCH = 0/1.
; Clobbers: A, BC, DE, HL, IX, and all SHA scratch.
; ---------------------------------------------------------------------------
conn_verify_final:
                ld      hl, CONN_HASH
                call    sha256_final           ; CONN_HASH = SHA-256 of the body
                ; byte-compare CONN_HASH vs CONN_PINNED_HASH (32 bytes).
                ld      hl, CONN_HASH
                ld      de, CONN_PINNED_HASH
                ld      b, 32
conn_vf_cmp:
                ld      a, (de)
                cp      (hl)
                jr      nz, conn_vf_mismatch
                inc     hl
                inc     de
                djnz    conn_vf_cmp
                ld      a, 1                   ; all 32 bytes equal: match
                ld      (CONN_HASH_MATCH), a
                ret
conn_vf_mismatch:
                xor     a                      ; a byte differed: mismatch
                ld      (CONN_HASH_MATCH), a
                ret
                endif  ; NETBOOT_HOSTTEST | NETBOOT_STREAM (filter + verify)

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
; Streaming sink state (i99) — opt-in bounded flush of inbound body bytes
; instead of accumulating them whole in CONN_DATA. The Z80 port of conn.go's
; sink/flushWindow/flushBuf fields + the ChunkSink recording double.
;
; Default off: CONN_SINK_ENABLED is 0 (defs zero-fills the binary), so the
; legacy CONN_DATA path is byte-identical and every existing oracle test stays
; green. A host test opts in by writing CONN_SINK_ENABLED=1 + a window into
; CONN_FLUSH_WINDOW before any data segment arrives, then reads CONN_SINK_OUT /
; CONN_SINK_CHUNKS to assert the streamed bytes / chunk boundaries. The bootable
; (NETBOOT_STREAM) enables the sink at boot and sets the window to the HSAVE
; record cap; its real store leaf (http_main.asm) writes each window to Trinity.
;
; Two tiers of state:
;   * SHARED (NETBOOT_HOSTTEST | NETBOOT_STREAM) — the flush machinery's own
;     working state + the SHA-256 verify state. Both the host harness and the
;     real bootable carry these.
;   * RECORDING DOUBLE (NETBOOT_HOSTTEST only) — CONN_SINK_OUT / CONN_SINK_CHUNKS,
;     where the host build records the streamed bytes for the oracle tests to
;     inspect. The bootable does not record (its leaf HSAVEs instead), so these
;     ~4.5 KB stay out of the boot footprint.
; ===========================================================================
                if defined(NETBOOT_HOSTTEST) | defined(NETBOOT_STREAM)
; Shared flush working state.
                if defined(NETBOOT_STREAM)
; Bootable: the flush window equals the HSAVE record cap (http_main.asm's
; FW_RECORD_CAP). The buffer must hold one full window plus one maximum inbound
; TCP payload (a full Ethernet frame, <=1464 body bytes) before the window
; drains — so CONN_FLUSH_BUF_LEN >= FW_RECORD_CAP + 1464. With FW_RECORD_CAP =
; 4096 that is 5560; 6144 gives margin. (If q22 raises the cap on hardware, bump
; this to keep cap + 1464 <= CONN_FLUSH_BUF_LEN.)
CONN_FLUSH_BUF_LEN: equ 6144
                else
CONN_FLUSH_BUF_LEN: equ 2048                ; host-test: oracle tests use small windows
                endif
CONN_SINK_ENABLED: defs 1                  ; 0 = legacy accumulate; !=0 = stream
CONN_FLUSH_WINDOW: defs 2                   ; flush chunk size (boot/test sets it)
CONN_FLUSH_LEN:    defs 2                   ; bytes currently buffered toward a flush
CONN_FLUSH_BUF:    defs CONN_FLUSH_BUF_LEN  ; >= one window + one max inbound payload

; Streamed-body SHA-256 verify state (i100, q15 option c). conn_verify_init
; resets the running hash; storage_sink_flush feeds each flushed window into it;
; conn_verify_final writes the digest into CONN_HASH and sets CONN_HASH_MATCH by
; comparing it against CONN_PINNED_HASH (the caller fills the pin). Shared by
; both builds — the bootable verifies each fetched firmware file against its
; pinned hash exactly as the host harness does.
CONN_HASH:         defs 32                  ; SHA-256 of the streamed body (digest out)
CONN_PINNED_HASH:  defs 32                  ; expected hash the caller pins
CONN_HASH_MATCH:   defs 1                   ; 1 = CONN_HASH == CONN_PINNED_HASH, else 0
                endif  ; NETBOOT_HOSTTEST | NETBOOT_STREAM (shared sink state)

                if defined(NETBOOT_HOSTTEST)
; Recording double (host-test only): the test sink storage_sink_leaf appends the
; streamed bytes here so the oracle tests can read them back.
CONN_SINK_OUT_LEN: defs 2                   ; bytes streamed to the test sink so far
CONN_SINK_CHUNK_COUNT: defs 2              ; number of recorded flushes
CONN_SINK_CHUNKS:  defs 512                 ; per-flush length list (<=256 entries, 2 B each)
CONN_SINK_OUT:     defs 4096                ; concatenation of all flushes (test inspects)
                endif  ; NETBOOT_HOSTTEST (recording double)

; ===========================================================================
; The host-verified packet primitive + the real driver, composed in.
; ===========================================================================
                include "build_tcp_segment.asm"
                include "encdrv.asm"

; ===========================================================================
; The SHA-256 primitive, composed in for the streamed-body verify (i100). Both
; the host-test build (NETBOOT_HOSTTEST) and the real bootable (NETBOOT_STREAM)
; carry the streaming sink that uses it, so both include it; a target that builds
; neither (e.g. the standalone http_get) excludes it. sha256.asm's
; NETBOOT_STANDALONE org is off here (we don't define it), so it lays down after
; the code above with no org collision; its labels (SHA_*, wv_*, sha_*) don't
; clash with this file's.
; ===========================================================================
                if defined(NETBOOT_HOSTTEST) | defined(NETBOOT_STREAM)
                include "sha256.asm"
                endif
