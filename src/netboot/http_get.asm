; http_get.asm — the i70 HTTP/1.0 GET client (rides the TCP connection).
;
; This is the SAM-side Z80 port of the netboot-oracle Go authority
; tools/netboot-oracle/http/http.go — the firmware self-provisioning layer that
; fetches a Raspberry Pi firmware blob from a plain-HTTP server. It composes the
; TCP connection state machine (tcp_conn.asm, included whole — which itself pulls
; in build_tcp_segment.asm + the real driver encdrv.asm) with two new pieces and
; runs over the i80 emulated Trinity:
;
;   http_get_start:
;     build the request "GET <path> HTTP/1.0\r\nHost: <host>\r\n\r\n" into the
;     connection's payload staging buffer, then send it as a PSH|ACK data segment
;     over the established connection (advancing sndNxt by the payload length —
;     data consumes len, RFC 793 §3.3).  The response then streams in: the
;     caller's read loop runs tcp_conn_recv (in tcp_conn.asm) which accumulates
;     the body into CONN_DATA and ACKs each segment.
;   http_parse_response:
;     parse the accumulated CONN_DATA — the status code from the status line and
;     the body offset (just past the \r\n\r\n header terminator).
;
; HTTP/1.0 (not 1.1) so the server closes the connection after the response; the
; FIN teardown (handled by tcp_conn_recv) is the end-of-body signal — no chunked
; / keep-alive parsing needed.  The transport (sending the GET, ACKing the
; response, the FIN) is the connection's job; this file only adds the request
; bytes and the response parse, mirroring http.Client.Start / ParseResponse.
;
; PROVENANCE: HTTP/1.0 request/response shape is http.go; the framing + the
; connection are tcp_conn.asm (trinload-derived, RFC 793).  VERIFICATION:
; host-verifiable end-to-end under the i80 emulation — http_get_test.go drives a
; handshake, asserts the GET segment on the virtual wire is byte-for-byte the Go
; http.Client.Start output, streams a response, and asserts http_parse_response's
; status / body offset match Go ParseResponse.  NOT host-verifiable: a real HTTP
; fetch against a live server — gated on real Trinity (CLAUDE.md §5).
; Emulation-verified is not hardware-verified.

                include "tcp_conn.asm"          ; org &8000 + the connection +
                                                ; build_tcp_segment + encdrv

CF_PSH:         equ &08                         ; TCP PUSH flag (RFC 793)

; ---------------------------------------------------------------------------
; http_get_start — build the GET request and send it over the established
; connection.  Must be called after the handshake completes (CONN_STATE =
; ESTABLISHED).
; Out: BC = bytes transmitted (the GET frame length).
; ---------------------------------------------------------------------------
http_get_start:
                call    http_build_request      ; BC = request length, staged at
                                                ; CONN_TX_PAYLOAD
                ld      (CONN_TX_PAYLEN), bc
                ld      a, CF_PSH | CF_ACK
                ld      (CONN_TX_FLAGS), a
                call    conn_build_and_send      ; BC = tx frame length
                push    bc                       ; save tx length to return
                ld      hl, (CONN_TX_PAYLEN)
                call    http_add16_to_sndnxt     ; sndNxt += payload (data consumes len)
                pop     bc
                ret

; ---------------------------------------------------------------------------
; http_build_request — concatenate "GET " + path + " HTTP/1.0\r\nHost: " + host +
; "\r\n\r\n" into CONN_TX_PAYLOAD (the connection's outbound payload staging,
; which conn_build_and_send reads).
; Out: BC = the request length.  Clobbers: A, DE, HL.
; ---------------------------------------------------------------------------
http_build_request:
                ld      de, CONN_TX_PAYLOAD     ; running dest pointer
                ld      hl, http_lit_get
                call    http_copy_cstr
                ld      hl, (HTTP_PATH_PTR)     ; the per-file path (default HTTP_PATH)
                call    http_copy_cstr
                ld      hl, http_lit_ver
                call    http_copy_cstr
                ld      hl, (HTTP_HOST_PTR)     ; the per-file host (default HTTP_HOST)
                call    http_copy_cstr
                ld      hl, http_lit_end
                call    http_copy_cstr
                ; BC = DE (end) - CONN_TX_PAYLOAD (start).
                ex      de, hl                  ; HL = end pointer
                ld      de, CONN_TX_PAYLOAD
                or      a
                sbc     hl, de                  ; HL = length
                ld      b, h
                ld      c, l
                ret

; http_copy_cstr — copy the NUL-terminated string at HL to DE (excluding the
; NUL), advancing DE past the copied bytes.  In: HL=src, DE=dest.  Clobbers A.
http_copy_cstr:
                ld      a, (hl)
                or      a
                ret     z
                ld      (de), a
                inc     hl
                inc     de
                jr      http_copy_cstr

; http_add16_to_sndnxt — sndNxt += HL (a 16-bit unsigned addend), 32-bit BE.
; Mirrors tcp_add16_to_rcvnxt (tcp_conn.asm) for the send sequence.  Clobbers
; A, DE, HL.
http_add16_to_sndnxt:
                ld      d, h
                ld      e, l                    ; DE = addend (D high, E low)
                ld      hl, CONN_SND_NXT + 3    ; least significant byte
                ld      a, (hl)
                add     a, e
                ld      (hl), a                 ; byte 3 += low addend byte
                dec     hl
                ld      a, (hl)
                adc     a, d
                ld      (hl), a                 ; byte 2 += high addend byte + carry
                ret     nc
                dec     hl
                inc     (hl)                    ; byte 1
                ret     nz
                dec     hl
                inc     (hl)                    ; byte 0
                ret

; ---------------------------------------------------------------------------
; http_parse_response — parse the accumulated response in CONN_DATA (length
; CONN_DATA_LEN).  Writes four outputs:
;   HTTP_OK       1 if a status line (a space) was found, else 0
;   HTTP_STATUS   the status code (16-bit), e.g. 200
;   HTTP_BODY_OFF the body offset within CONN_DATA (past the \r\n\r\n)
;   HTTP_COMPLETE 1 if the \r\n\r\n header terminator was found, else 0
; Mirrors http.ParseResponse step for step.  Clobbers A, BC, DE, HL.
;
; Legacy (non-streaming) parser: it reads the whole-body CONN_DATA buffer, so it
; is built only where that buffer is — the host-test/default builds. The
; NETBOOT_STREAM bootable skips headers inline via body_sink_write and never
; accumulates CONN_DATA, so this parser and its private http_mul10_add helper are
; excluded there (i360, together with the CONN_DATA buffer they depend on).
; ---------------------------------------------------------------------------
                if defined(NETBOOT_STREAM)==0
http_parse_response:
                xor     a
                ld      (HTTP_OK), a
                ld      (HTTP_COMPLETE), a
                ld      hl, 0
                ld      (HTTP_STATUS), hl
                ld      (HTTP_BODY_OFF), hl

                ; --- find the first space (the status-line delimiter) ---
                ld      bc, (CONN_DATA_LEN)
                ld      hl, CONN_DATA
hpr_find_sp:
                ld      a, b
                or      c
                ret     z                       ; no space -> not ok (HTTP_OK = 0)
                ld      a, (hl)
                cp      " "
                jr      z, hpr_got_sp
                inc     hl
                dec     bc
                jr      hpr_find_sp
hpr_got_sp:
                inc     hl                      ; HL -> first char after the space
                dec     bc
                ld      a, 1
                ld      (HTTP_OK), a            ; status line present

                ; --- parse the decimal status code into DE ---
                ld      de, 0
hpr_digits:
                ld      a, b
                or      c
                jr      z, hpr_digits_done
                ld      a, (hl)
                cp      "0"
                jr      c, hpr_digits_done
                cp      &3A                     ; '9'+1
                jr      nc, hpr_digits_done
                sub     "0"                     ; A = digit value (0..9)
                call    http_mul10_add          ; DE = DE*10 + digit (keeps BC,HL)
                inc     hl
                dec     bc
                jr      hpr_digits
hpr_digits_done:
                ld      (HTTP_STATUS), de

                ; --- find the "\r\n\r\n" header terminator from CONN_DATA ---
                ld      de, (CONN_DATA_LEN)
                ld      hl, 4
                or      a
                ex      de, hl                  ; HL = LEN, DE = 4
                sbc     hl, de                  ; HL = LEN - 4 (carry if LEN < 4)
                ret     c                       ; too short for a terminator
                inc     hl                      ; window count = (LEN-4)+1 = LEN-3
                ld      d, h
                ld      e, l                    ; DE = window count
                ld      hl, CONN_DATA           ; HL = base pointer
hpr_term_loop:
                ld      a, d
                or      e
                ret     z                       ; no windows left -> no terminator
                ld      a, (hl)
                cp      13
                jr      nz, hpr_term_next
                inc     hl
                ld      a, (hl)
                cp      10
                jr      nz, hpr_term_dec1
                inc     hl
                ld      a, (hl)
                cp      13
                jr      nz, hpr_term_dec2
                inc     hl
                ld      a, (hl)
                cp      10
                jr      nz, hpr_term_dec3
                ; match: HL -> the last LF (base+3); body starts at base+4.
                inc     hl
                ld      de, CONN_DATA
                or      a
                sbc     hl, de                  ; HL = body offset
                ld      (HTTP_BODY_OFF), hl
                ld      a, 1
                ld      (HTTP_COMPLETE), a
                ret
hpr_term_dec3:  dec     hl
hpr_term_dec2:  dec     hl
hpr_term_dec1:  dec     hl
hpr_term_next:  inc     hl                      ; advance base by 1
                dec     de
                jr      hpr_term_loop

; http_mul10_add — DE = DE*10 + A (A a single decimal digit).  Preserves BC, HL.
; Clobbers AF.
http_mul10_add:
                push    hl
                push    bc
                ld      l, e
                ld      h, d                    ; HL = DE
                add     hl, hl                  ; *2
                ld      c, l
                ld      b, h                    ; BC = value*2
                add     hl, hl                  ; *4
                add     hl, hl                  ; *8
                add     hl, bc                  ; *10
                add     a, l
                ld      l, a
                jr      nc, hma_nocarry
                inc     h
hma_nocarry:
                ld      e, l
                ld      d, h                    ; DE = value*10 + digit
                pop     bc
                pop     hl
                ret
                endif                           ; NETBOOT_STREAM==0 (legacy parser)

; ===========================================================================
; Request literals + the configured target.  The harness reads HTTP_PATH /
; HTTP_HOST back from the binary and feeds the same strings to the Go authority,
; so the two build identical request bytes (one source of truth).
; ===========================================================================
http_lit_get:   defm "GET "
                defb 0
http_lit_ver:   defm " HTTP/1.0"
                defb 13, 10
                defm "Host: "
                defb 0
http_lit_end:   defb 13, 10, 13, 10
                defb 0

HTTP_PATH:      defm "/firmware/start4.elf"
                defb 0
HTTP_HOST:      defm "fw.local"
                defb 0

; The path + host http_build_request copies — settable so the multi-file fetch
; loop (http_main's prov_start) can point each request at the file's fw_plan_path
; output (FW_PATH) and the firmware host (FW_HOST). Both default to the baked
; HTTP_PATH / HTTP_HOST, so the single-file fetch and the bootable build are
; behaviourally unchanged. Placed after the strings above so their addresses
; (which the harness reads back) are unshifted. `ld hl,(nn)` is the same length as
; `ld hl,nn`, so the only bootable growth is these two defw words.
HTTP_PATH_PTR:  defw HTTP_PATH
HTTP_HOST_PTR:  defw HTTP_HOST

; http_parse_response outputs (the harness reads these).
HTTP_OK:        defs 1
HTTP_STATUS:    defs 2
HTTP_BODY_OFF:  defs 2
HTTP_COMPLETE:  defs 1
