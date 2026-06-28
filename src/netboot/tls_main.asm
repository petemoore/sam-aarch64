; tls_main.asm — i88c-b1: the TLS 6b integration composition (host-verifiable).
;
; The i88b reassembler (tls_reasm.asm) and the i88a handshake state machine
; (tls_client.asm) were each host-verified in isolation. This file WIRES them:
; it composes both and adds tls_record_shim — the REASM_EMIT_PTR call-through
; that hands each complete reassembled record to tls_client_on_record. The path
; a chunk of inbound TCP payload takes is:
;
;   storage_sink_flush (tcp_conn, mode 1)  ->  CONN_SINK_FILTER = tls_reasm_feed
;     -> (per complete record) REASM_EMIT_PTR = tls_record_shim
;     -> tls_client_on_record (advance the 6a handshake on that record)
;
; The composed bootable (i88c-b3) arms exactly that: CONN_SINK_FILTER_MODE=1,
; CONN_SINK_FILTER=tls_reasm_feed, REASM_EMIT_PTR=tls_record_shim — the same
; function-pointer sink dispatch body_sink_write already uses (host-proven), so
; tls_reasm_feed's (HL=chunk ptr, BC=len) contract matches the dispatch's call
; convention exactly and needs no wrapper on the sink side. The ONLY new code is
; tls_record_shim (the emit side).
;
; VERIFICATION (host, emulation-first — tls_integration_test.go): the captured
; server handshake flight is concatenated into one byte stream, split into
; mis-aligned chunks (byte-at-a-time, small fixed, one-shot), and each chunk is
; fed to tls_reasm_feed (the sink target). The reassembler frames each record and
; drives the 6a machine to DONE; the test asserts TC_PHASE=DONE, the four traffic
; secrets, and the client Finished record match the Go authority — the SAME
; asserts as tls_client_test.go's TestTLSClientHandshakeReplay, but driven THROUGH
; the reassembler under adversarial chunking instead of one whole record at a time.
;
; PAGING: this bounded host build fits a flat &8000 image because REASM_MAX is
; pre-set small (below) — the capture's records are <=332 B. A REAL github.com
; Certificate record needs the full 16645 B REASM_BUF plus a 16645 B TC_RX, which
; cannot coexist in a flat build (measured: the tls_client composite is already
; &8000-&F7A5). That full-size paged layout is i88c-b2 (design q72), and the real
; RST-8 hardware shot is i88c-b4 — both gated (CLAUDE.md §5).
;
; Built with -D NETBOOT_TLS_CLIENT=1 (tls_client.asm owns the single org &8000).

; Bound the reassembler buffer to the host capture's largest record (<=332 B) so
; the reassembler module lands in the free gap between tls_client_end (&F7A5) and
; the x25519 qsq_table scratch (&FB00) — the whole image stays a flat &8000 build.
REASM_MAX:      equ 512

                include "tls_client.asm"        ; org &8000; the 6a state machine
                                                ; + every crypto brick + TC_* block
                include "tls_reasm.asm"         ; tls_reasm_feed + REASM_BUF (bounded
                                                ; by the REASM_MAX above) + the emit
                                                ; call-through REASM_EMIT_PTR

; ===========================================================================
; tls_record_shim — the REASM_EMIT_PTR target. Reached (via reasm_emit's
; jp (ix)) once tls_reasm_feed has framed a complete record.
; In:  HL = record ptr (REASM_BUF), BC = record length (> 0, <= REASM_MAX).
; Out: the 6a handshake has consumed the record (TC_PHASE / TC_TX / TC_STATUS
;      updated). Tail-returns to tls_reasm_feed's post-emit continuation.
; Copies the record into TC_RX (the buffer tls_client_on_record reads), sets
; TC_RX_LEN, and tail-calls the handshake step. tls_client_on_record touches only
; the TC_*/crypto state — never the rsm_*/REASM_LEN cells tls_reasm_feed keeps its
; loop position in — so multi-record chunks reassemble correctly across the call.
; ===========================================================================
tls_record_shim:
                ld      (TC_RX_LEN), bc         ; TC_RX_LEN = record length
                ld      de, TC_RX
                ldir                            ; TC_RX <- record (BC bytes; BC->0)
                jp      tls_client_on_record    ; advance the handshake on this record

tls_main_end:
