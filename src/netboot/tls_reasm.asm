; tls_reasm.asm — the i88 6b TLS-record reassembler (the wire/reassembly layer).
;
; TLS records do not align to TCP segment boundaries: one inbound chunk of TCP
; payload may carry a partial record, several whole records, or a record (even
; its 5-byte header) split across chunks.  The 6a state machine (tls_client.asm)
; consumes one complete record per call; this leaf bridges the gap — it is fed
; arbitrary byte chunks and emits each complete record exactly once, in order,
; buffering the partial-record tail for the next chunk.
;
; It consumes a chunk incrementally — taking only the bytes the in-progress
; record still needs (first to complete its 5-byte header, then its declared
; payload) — so REASM_BUF never holds more than one complete record.  The record
; length is the 16-bit big-endian field at header bytes 3..4
; (length = hdr[3]<<8 | hdr[4]); the record total is 5 + that.
;
; CONTRACT: records must not exceed the TLS 1.3 maximum (REASM_MAX = 5 + 2^14 +
; 256).  i88's scope is a single public-key-pinned server (github.com), which is
; spec-compliant, so REASM_BUF is sized to exactly that maximum; an over-maximum
; length field is a protocol violation outside this layer's scope (hardening for
; an untrusted server is a later, hardware-path concern).
;
; PROVENANCE: the framing arithmetic mirrors tls/capture.go::readRecord
; (length = hdr[3]<<8 | hdr[4]); the incremental buffering is new Z80 code (the
; Go conn already delivers an ordered stream, so it never reassembles).  This
; increment adds the matching Go authority tls/reassembler.go::RecordReassembler.
; VERIFICATION: host-verifiable.  tls_reasm_test.go assembles this standalone
; module under the koron-go/z80 harness, feeds tls_reasm_feed a sequence of
; chunks (deliberately mis-aligned to record boundaries — split header, split
; body, coalesced records, byte-at-a-time) and asserts the emitted records + the
; per-record boundaries recorded by the reasm_record_leaf test double match the
; Go RecordReassembler fed the identical chunks.  NOT host-verifiable: wiring this
; into tcp_conn's CONN_SINK_FILTER and the bootable/paging/RST-8 path is the
; hardware-gated remainder of i88 6b (CLAUDE.md §5) — this leaf stops at the wire.

                if defined(NETBOOT_STANDALONE)
                org     &8000
                endif

REASM_MAX:        equ 16645             ; TLS 1.3 max record: 5 + (2^14 + 256)

; ===========================================================================
; State + buffers (data first so every label is defined before the code, the
; same idiom as body_sink.asm).
; ===========================================================================
REASM_LEN:        defw 0               ; bytes of the in-progress record in REASM_BUF
rsm_src:          defw 0               ; feed: current chunk read cursor
rsm_rem:          defw 0               ; feed: chunk bytes still to consume
REASM_BUF:        defs REASM_MAX       ; the in-progress record accumulator

; REASM_EMIT_PTR — the call-through for a completed record.  Standalone: the
; recording test double; the composed build sets it to the 6a record handler
; (tls_client_on_record's feeder) before use.
                if defined(NETBOOT_STANDALONE)
REASM_EMIT_PTR:   defw reasm_record_leaf
                else
REASM_EMIT_PTR:   defw 0
                endif

                if defined(NETBOOT_STANDALONE)
; REASM_IN — the host test stages each chunk here before calling tls_reasm_feed
; (HL = REASM_IN, BC = chunk length).
REASM_IN:         defs 4096

; The recording double — the Z80 analogue of the Go RecordReassembler's returned
; [][]byte.  Each emitted record appends to REASM_OUT and records its length into
; REASM_RECS, so the test can assert both the concatenated records AND the
; per-record boundaries/count match the Go authority fed the same chunks.
REASM_OUT_LEN:    defw 0               ; total emitted-record bytes so far
REASM_REC_COUNT:  defw 0               ; number of records emitted
REASM_RECS:       defs 128             ; per-record lengths (16-bit LE), up to 64
REASM_OUT:        defs 8192            ; concatenation of all emitted records
                endif

; ===========================================================================
; tls_reasm_init — reset the reassembler (no in-progress record).
; The composed build calls this once before the first feed; the standalone host
; test relies on the freshly-loaded image's zeros (a new machine per run).
; ===========================================================================
tls_reasm_init:
                ld      hl, 0
                ld      (REASM_LEN), hl
                ret

; ===========================================================================
; tls_reasm_feed — accumulate one chunk, emitting each complete record via
; REASM_EMIT_PTR as soon as its bytes have all arrived.
; In:  HL = chunk pointer, BC = chunk length.
; Out: 0..N records emitted (HL = record ptr, BC = record length to the callee);
;      REASM_BUF/REASM_LEN hold the leftover partial-record tail.
; Clobbers: A, BC, DE, HL, IX (+ whatever the emit callee clobbers).
; ===========================================================================
tls_reasm_feed:
                ld      (rsm_src), hl
                ld      (rsm_rem), bc
trf_loop:
                ld      hl, (rsm_rem)           ; chunk exhausted?
                ld      a, h
                or      l
                ret     z

                ; --- need (into DE) = bytes the in-progress record still wants ---
                ld      hl, (REASM_LEN)
                ld      a, h
                or      a
                jr      nz, trf_have_hdr        ; REASM_LEN >= 256 -> header present
                ld      a, l
                cp      5
                jr      nc, trf_have_hdr        ; REASM_LEN >= 5 -> header present
                ld      a, 5                    ; header pending: need = 5 - REASM_LEN
                sub     l
                ld      e, a
                ld      d, 0
                jr      trf_have_need
trf_have_hdr:
                ld      a, (REASM_BUF + 3)      ; payloadlen = hdr[3]<<8 | hdr[4]
                ld      d, a
                ld      a, (REASM_BUF + 4)
                ld      e, a                    ; DE = payloadlen
                ld      hl, 5
                add     hl, de                  ; HL = total = 5 + payloadlen
                ld      de, (REASM_LEN)
                or      a
                sbc     hl, de                  ; HL = total - REASM_LEN = need
                ex      de, hl                  ; DE = need
trf_have_need:
                ; --- take (into BC) = min(need DE, rem) ---
                ld      hl, (rsm_rem)
                or      a
                sbc     hl, de                  ; CF set if rem < need
                jr      c, trf_take_rem
                ld      b, d                    ; rem >= need: take = need
                ld      c, e
                jr      trf_copy
trf_take_rem:
                ld      bc, (rsm_rem)           ; take = rem (the whole remainder)
trf_copy:
                ; copy BC (= take, > 0) bytes from (rsm_src) to REASM_BUF[REASM_LEN].
                ld      hl, (REASM_LEN)
                ld      de, REASM_BUF
                add     hl, de
                ex      de, hl                  ; DE = REASM_BUF + REASM_LEN (dest)
                ld      hl, (rsm_src)           ; HL = source
                push    bc                      ; save take
                ldir                            ; copy; HL/DE advance, BC = 0
                ld      (rsm_src), hl           ; advance the read cursor
                pop     bc                      ; BC = take
                ld      hl, (rsm_rem)           ; rem -= take
                or      a
                sbc     hl, bc
                ld      (rsm_rem), hl
                ld      hl, (REASM_LEN)         ; REASM_LEN += take
                add     hl, bc
                ld      (REASM_LEN), hl         ; HL = new REASM_LEN

                ; --- emit if the in-progress record is now complete ---
                ld      a, h                    ; REASM_LEN >= 5?
                or      a
                jr      nz, trf_check
                ld      a, l
                cp      5
                jp      c, trf_loop             ; < 5: header still incomplete
trf_check:
                ld      a, (REASM_BUF + 3)
                ld      d, a
                ld      a, (REASM_BUF + 4)
                ld      e, a                    ; DE = payloadlen
                ld      hl, 5
                add     hl, de                  ; HL = total
                ld      de, (REASM_LEN)
                or      a
                sbc     hl, de                  ; HL = total - REASM_LEN
                ld      a, h
                or      l
                jp      nz, trf_loop            ; not yet complete
                ld      bc, (REASM_LEN)         ; complete: emit REASM_BUF[0..total)
                ld      hl, REASM_BUF
                call    reasm_emit
                ld      hl, 0                   ; reset for the next record
                ld      (REASM_LEN), hl
                jp      trf_loop

; ===========================================================================
; reasm_emit — tail-call through (REASM_EMIT_PTR) with HL = record ptr, BC = len.
; ===========================================================================
reasm_emit:
                ld      ix, (REASM_EMIT_PTR)
                jp      (ix)

                if defined(NETBOOT_STANDALONE)
; ===========================================================================
; reasm_record_leaf — the recording test double.  Append BC bytes from HL to
; REASM_OUT, record the length into REASM_RECS, bump the count.  Mirrors
; body_sink.asm's body_record_leaf / the Go RecordReassembler's returned slice.
; In: HL = ptr, BC = length (> 0).  Clobbers A, BC, DE, HL.
; ===========================================================================
reasm_record_leaf:
                push    bc                      ; save length
                ld      de, (REASM_OUT_LEN)     ; append [HL, HL+BC) to REASM_OUT tail
                push    hl
                ld      hl, REASM_OUT
                add     hl, de
                ex      de, hl                  ; DE = dest tail
                pop     hl                      ; HL = source
                ldir                            ; copy BC bytes (BC consumed)
                pop     bc                      ; length
                push    bc
                ld      hl, (REASM_OUT_LEN)     ; REASM_OUT_LEN += length
                add     hl, bc
                ld      (REASM_OUT_LEN), hl
                ld      hl, (REASM_REC_COUNT)   ; REASM_RECS[count] = length
                add     hl, hl                  ; *2 (16-bit entries)
                ld      de, REASM_RECS
                add     hl, de
                pop     bc                      ; length
                ld      (hl), c
                inc     hl
                ld      (hl), b
                ld      hl, (REASM_REC_COUNT)   ; count += 1
                inc     hl
                ld      (REASM_REC_COUNT), hl
                ret
                endif
