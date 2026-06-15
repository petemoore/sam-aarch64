; body_sink.asm — the i100 HTTP-header-skip sink adapter (the bodySink Z80 port).
;
; This is the SAM-side Z80 port of the netboot-oracle Go authority
; tools/netboot-oracle/http/fetcher.go::bodySink — the thin adapter that sits
; between the i99 streaming receive (tcp_conn.asm's storage_sink_flush) and the
; storage sink, dropping the HTTP/1.0 response header so the bytes that reach
; storage (and the SHA-256 verify) begin at the first body byte.
;
; Why it exists: tcp_conn.asm is a pure byte transport with no notion of an HTTP
; header, so its streaming flush hands the sink the header bytes too. The fetch
; knows the stream is an HTTP/1.0 response, so it interposes this adapter: it
; scans the first flushed window for the "\r\n\r\n" header terminator, drops
; everything up to and including it, forwards the remaining body bytes, and from
; then on forwards every window whole.  This keeps the header out of both the
; bytes written to Trinity storage AND the streamed-body SHA-256 (i100, q15
; option c) — the digest is of the body alone, matching the pinned hash.
;
; The header-fits-in-the-first-window assumption (documented on the Go
; StreamTo): a flush window comfortably exceeds any HTTP/1.0 header, so the
; terminator lands within the first window — the header never straddles two, and
; body_find_header only has to scan the chunk it is handed.  A chunk with no
; terminator is dropped (forward nothing) and scanning continues with the next,
; so a malformed/header-too-large response yields a missing/short body, never a
; header leak — byte-for-byte the Go bodySink's behaviour.
;
; PROVENANCE: the header-skip + the response-parse arithmetic mirror
; http/fetcher.go::bodySink.Write and http/http.go::ParseResponse /
; indexCRLFCRLF (the same 4-byte "\r\n\r\n" scan src/netboot/http_get.asm's
; http_parse_response walks over CONN_DATA; this module walks an arbitrary chunk,
; so the bootable http_get.asm is left untouched and byte-identical).
; VERIFICATION: host-verifiable in the project's standard way.  body_sink_test.go
; assembles this standalone module under the koron-go/z80 harness, feeds
; body_sink_write a sequence of chunks, and asserts the forwarded body bytes +
; the per-Write boundaries recorded by the body_dst_write test double match the
; Go bodySink (wrapping a tcp.ChunkSink) fed the identical chunks — including the
; degenerate no-terminator / split-header cases, where both must agree.  NOT
; host-verifiable: nothing new here is hardware-gated; the real storage sink this
; adapter forwards to (the B-DOS bounded write) is q16/hardware-gated and lives
; in tcp_conn.asm's storage_sink_flush, not here.

                if defined(NETBOOT_STANDALONE)
                org     &8000
                endif

; ===========================================================================
; State + buffers (data first so every label is defined before the code that
; references it, the same idiom as fw_source.asm).
; ===========================================================================
BODY_HDR_DONE:    defb 0          ; 0 until the "\r\n\r\n" header terminator is seen

; BODY_IN — the input staging the host test writes each chunk into before calling
; body_sink_write (HL = BODY_IN, BC = chunk length).  Sized well above any test
; chunk; the real adapter is handed the connection's flush buffer, not this.
BODY_IN:          defs 4096

; The recording sink — the Z80 analogue of tcp.ChunkSink (the test's dst).  Each
; forwarded body Write appends to BODY_OUT and records its length into
; BODY_CHUNKS, so the test can assert both the concatenated body AND the
; per-Write boundaries/count match the Go bodySink fed the same chunks.
BODY_OUT_LEN:     defs 2          ; total body bytes forwarded so far
BODY_CHUNK_COUNT: defs 2          ; number of forwarded Writes
BODY_CHUNKS:      defs 128        ; per-Write lengths (16-bit LE), up to 64 writes
BODY_OUT:         defs 8192       ; concatenation of all forwarded body bytes

; ===========================================================================
; body_sink_write — process one flushed window (the bodySink.Write port).
; In:  HL = chunk pointer, BC = chunk length.
; Clobbers: A, BC, DE, HL.
;
; Mirrors http/fetcher.go bodySink.Write step for step:
;   header done    -> forward the whole chunk (if non-empty).
;   header pending -> parse this chunk for "\r\n\r\n"; if absent drop it (keep
;                     scanning the next chunk); if present mark the header done
;                     and forward chunk[bodyoff:] (if non-empty).
; ===========================================================================
body_sink_write:
                ld      a, (BODY_HDR_DONE)
                or      a
                jr      nz, bsw_forward_all     ; header already skipped

                ; --- header pending: parse this chunk ---
                push    hl                      ; save chunk ptr (find_header clobbers)
                push    bc                      ; save chunk len
                call    body_find_header        ; CF=1 + DE=bodyoff if ok && complete
                pop     bc                      ; restore chunk len
                pop     hl                      ; restore chunk ptr
                ret     nc                      ; no complete header here: drop

                ; header complete: mark done, forward chunk[bodyoff:].
                ld      a, 1
                ld      (BODY_HDR_DONE), a
                ; forward length = chunk_len - bodyoff = BC - DE.
                push    hl                      ; save chunk ptr
                ld      h, b
                ld      l, c                    ; HL = chunk_len
                or      a
                sbc     hl, de                  ; HL = forward length
                ld      b, h
                ld      c, l                    ; BC = forward length
                pop     hl                      ; HL = chunk ptr
                add     hl, de                  ; HL = chunk + bodyoff = body pointer
                ; Go forwards only when len(body) > 0.
                ld      a, b
                or      c
                ret     z
                jp      body_dst_write

bsw_forward_all:
                ; header already skipped: forward the whole chunk if non-empty.
                ld      a, b
                or      c
                ret     z
                jp      body_dst_write

; ===========================================================================
; body_find_header — locate the HTTP/1.0 header terminator in one chunk.
; In:  HL = chunk pointer, BC = chunk length.
; Out: CF = 1 and DE = body offset (bytes past "\r\n\r\n") iff the chunk has a
;      status line (a space) AND the "\r\n\r\n" terminator; CF = 0 otherwise.
; Clobbers: A, BC, DE, HL.
;
; Mirrors http/http.go ParseResponse: ok = a status-line space is present (no
; space -> not an HTTP status line -> drop), Complete = the first "\r\n\r\n" was
; found, BodyOff = its index + 4.  bodySink forwards only when ok && Complete, so
; the space check is load-bearing for byte-faithfulness even though a real
; response with a terminator always has a status-line space.
; ===========================================================================
body_find_header:
                ; --- ok: is there a space anywhere in [HL, HL+BC)? ---
                push    hl                      ; save chunk ptr / len for the
                push    bc                      ; terminator scan below
bfh_sp_loop:
                ld      a, b
                or      c
                jr      z, bfh_no_sp            ; scanned the whole chunk, no space
                ld      a, (hl)
                cp      " "
                jr      z, bfh_have_sp
                inc     hl
                dec     bc
                jr      bfh_sp_loop
bfh_no_sp:
                pop     bc
                pop     hl
                or      a                       ; CF = 0: no status line -> drop
                ret
bfh_have_sp:
                pop     bc                      ; restore chunk len
                pop     hl                      ; restore chunk ptr
                ; --- complete: need at least 4 bytes for a "\r\n\r\n" ---
                ld      a, b
                or      a
                jr      nz, bfh_len_ok          ; len >= 256 -> certainly >= 4
                ld      a, c
                cp      4
                jr      c, bfh_drop             ; len < 4: too short, no terminator
bfh_len_ok:
                push    hl                      ; save chunk start (for the bodyoff calc)
                ; window count = len - 3 (= (len-4)+1), >= 1 here.
                ld      d, b
                ld      e, c                    ; DE = len
                ld      hl, 3
                ex      de, hl                  ; HL = len, DE = 3
                or      a
                sbc     hl, de                  ; HL = len - 3 = window count
                ld      d, h
                ld      e, l                    ; DE = window count
                pop     hl                      ; HL = chunk start
                push    hl                      ; keep a copy on the stack for bodyoff
bfh_term_loop:
                ld      a, d
                or      e
                jr      z, bfh_drop_pop         ; no windows left -> no terminator
                ld      a, (hl)
                cp      13
                jr      nz, bfh_term_next
                inc     hl
                ld      a, (hl)
                cp      10
                jr      nz, bfh_term_dec1
                inc     hl
                ld      a, (hl)
                cp      13
                jr      nz, bfh_term_dec2
                inc     hl
                ld      a, (hl)
                cp      10
                jr      nz, bfh_term_dec3
                ; matched "\r\n\r\n"; HL -> the second LF.  body starts at HL+1.
                inc     hl                      ; HL -> first body byte
                pop     de                      ; DE = chunk start
                or      a
                sbc     hl, de                  ; HL = body offset
                ex      de, hl                  ; DE = body offset
                scf                             ; CF = 1: header complete
                ret
bfh_term_dec3:  dec     hl
bfh_term_dec2:  dec     hl
bfh_term_dec1:  dec     hl
bfh_term_next:  inc     hl                      ; advance the window base by one
                dec     de
                jr      bfh_term_loop
bfh_drop_pop:
                pop     hl                      ; discard the saved chunk start
bfh_drop:
                or      a                       ; CF = 0: no complete header -> drop
                ret

; ===========================================================================
; body_dst_write — the recording test double (the dst Sink).  Append BC bytes
; from HL to BODY_OUT, record the write length into BODY_CHUNKS, bump the count.
; The Z80 analogue of tcp.ChunkSink.Write; the real adapter forwards to the
; storage sink instead.  In: HL = ptr, BC = length (> 0).  Clobbers A, BC, DE, HL.
; ===========================================================================
body_dst_write:
                push    bc                      ; save length
                ; append [HL, HL+BC) to BODY_OUT at its tail.
                ld      de, (BODY_OUT_LEN)
                push    hl                      ; save source ptr
                ld      hl, BODY_OUT
                add     hl, de                  ; HL = dest tail
                ex      de, hl                  ; DE = dest
                pop     hl                      ; HL = source
                ldir                            ; copy BC bytes (BC consumed)
                ; BODY_OUT_LEN += length.
                pop     bc                      ; length
                push    bc
                ld      hl, (BODY_OUT_LEN)
                add     hl, bc
                ld      (BODY_OUT_LEN), hl
                ; record the length into BODY_CHUNKS[count] (16-bit LE), bump count.
                ld      hl, (BODY_CHUNK_COUNT)
                add     hl, hl                  ; *2 (each entry is 2 bytes)
                ld      de, BODY_CHUNKS
                add     hl, de                  ; HL = &BODY_CHUNKS[count]
                pop     bc                      ; length
                ld      (hl), c
                inc     hl
                ld      (hl), b
                ld      hl, (BODY_CHUNK_COUNT)
                inc     hl
                ld      (BODY_CHUNK_COUNT), hl
                ret
