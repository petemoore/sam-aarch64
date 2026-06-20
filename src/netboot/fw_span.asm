; fw_span.asm — the i99/q16 firmware-spanning primitives (the Z80 port of the Go
; authority tools/netboot-oracle/bdos/span.go).
;
; A fetched object larger than one bounded storage record is persisted as a
; sequence of records, each a plain HSAVE (the only verified save hook), and
; reassembled in order at serve time. This file is the host-verifiable arithmetic
; that drives that split + naming; the streaming-persist loop + the serve read
; loop that call it, and the real RST 8 HSAVE/HLOAD per record, are the
; hardware-gated callers (CLAUDE.md §5).
;
;   fw_span_chunk_len:
;     the per-record byte count during streaming — len = min(cap, remaining),
;     32-bit (a real firmware blob is multi-MB). The persist loop seeds
;     FW_SPAN_REMAINING = total size, then each pass: chunk_len -> HSAVE that many
;     bytes under the record name -> remaining -= len -> repeat until 0. The
;     record count falls out of the loop (= ceil(size/cap)), so no 32-bit divide
;     is needed. Mirrors bdos.SpanPlan's per-record length min(cap, size - i*cap).
;   fw_span_record_name:
;     build the content-addressed storage record name <hash6><NNN> into
;     FW_SPAN_NAME — the first 3 bytes of the blob's SHA-256 digest emitted as 6
;     lowercase hex chars, followed by the 3-digit zero-padded decimal record
;     index. The result is always 9 chars, within the 10-char B-DOS NameLen field.
;     Mirrors bdos.SpanRecordName(blobHash [32]byte, index int).
;
; The index is bounded to 0..999 (the 3-digit suffix / the documented 1000-record
; limit — ample: even a 3 MB blob at a small RAM-bounded cap stays well under).
;
; AUTHORITY / VERIFICATION: host-verifiable in the project's standard way.
; fw_span_test.go assembles this standalone module under the koron-go/z80 harness,
; drives the streaming chunk-length loop + the record naming, and asserts the
; length sequence + the names match bdos.SpanPlan / bdos.SpanRecordName
; byte-for-byte. NOT host-verifiable: the real RST 8 HSAVE/HLOAD per record + the
; end-to-end multi-MB persist/serve — gated on real Trinity. Emulation-verified is
; not hardware-verified.

                if defined(NETBOOT_STANDALONE)
                org     &8000
                endif

; ===========================================================================
; Parameter blocks + output buffers (data first so every label is defined).
; ===========================================================================
FW_SPAN_REMAINING: defs 4       ; in : bytes of the object still to write (32-bit LE)
FW_SPAN_CAP:       defs 4       ; in : per-record capacity (32-bit LE)
FW_SPAN_LEN:       defs 4       ; out: min(cap, remaining) (32-bit LE)
FW_SPAN_HASH:      defs 32     ; in : 32-byte SHA-256 digest of the blob (big-endian)
FW_SPAN_NAME:      defs 16      ; out: built record name (NUL-terminated, 9+NUL chars)

; ===========================================================================
; fw_span_chunk_len — FW_SPAN_LEN = min(FW_SPAN_CAP, FW_SPAN_REMAINING), 32-bit.
; Clobbers A, BC, DE, HL.  Mirrors bdos.SpanPlan's per-record length.
; ===========================================================================
fw_span_chunk_len:
                call    fw_span_cmp             ; CF = 1 iff remaining < cap
                ld      hl, FW_SPAN_CAP         ; remaining >= cap -> len = cap
                jr      nc, fscl_copy
                ld      hl, FW_SPAN_REMAINING   ; remaining <  cap -> len = remaining
fscl_copy:
                ld      de, FW_SPAN_LEN
                ld      bc, 4
                ldir
                ret

; fw_span_cmp — 32-bit compare: CF = 1 iff FW_SPAN_REMAINING < FW_SPAN_CAP.
; Computes remaining - cap LSB-first (the borrow out of the MSB is the result).
; Clobbers A, B, DE, HL.
fw_span_cmp:
                ld      de, FW_SPAN_REMAINING   ; the minuend (read via (de) into A)
                ld      hl, FW_SPAN_CAP         ; the subtrahend (sbc a,(hl))
                or      a                       ; CF = 0 (no incoming borrow)
                ld      b, 4
fsc_loop:
                ld      a, (de)
                sbc     a, (hl)                 ; A = remaining_byte - cap_byte - borrow
                inc     de
                inc     hl
                djnz    fsc_loop                ; (does not disturb CF)
                ret                             ; CF = final borrow = remaining < cap

; ===========================================================================
; fw_span_record_name — build <hash6><NNN> into FW_SPAN_NAME, NUL-terminated.
; In:  HL = pointer to 32-byte blob SHA-256 digest (big-endian); BC = record
;      index (0..999).
; Out: FW_SPAN_NAME = the 9-char content-addressed record name (6 lowercase hex
;      chars from hash[0..2] followed by 3 decimal index digits), NUL-terminated.
; Clobbers A, BC, DE, HL.
; Mirrors bdos.SpanRecordName(blobHash [32]byte, index int) string.
; ===========================================================================
fw_span_record_name:
                push    bc                      ; save the index
                ld      de, FW_SPAN_NAME        ; dest

                ; Emit hash[0], hash[1], hash[2] as 6 lowercase hex nibbles.
                ld      b, 3                    ; 3 bytes to convert
fsrn_hex_loop:
                ld      a, (hl)                 ; load next hash byte
                inc     hl
                push    af                      ; stash full byte
                rrca
                rrca
                rrca
                rrca                            ; A = high nibble (bits 7..4 -> bits 3..0)
                and     &0F
                call    fsrn_nibble_to_hex
                ld      (de), a
                inc     de
                pop     af                      ; restore full byte
                and     &0F                     ; A = low nibble
                call    fsrn_nibble_to_hex
                ld      (de), a
                inc     de
                djnz    fsrn_hex_loop

                pop     bc                      ; BC = index

                ; hundreds digit = how many times 100 fits in BC.
                ld      a, "0"
fsrn_h:
                ld      hl, -100
                add     hl, bc                  ; CF = 1 iff BC >= 100; HL = BC - 100
                jr      nc, fsrn_h_done
                ld      b, h
                ld      c, l                    ; BC -= 100
                inc     a
                jr      fsrn_h
fsrn_h_done:
                ld      (de), a
                inc     de
                ; tens digit.
                ld      a, "0"
fsrn_t:
                ld      hl, -10
                add     hl, bc                  ; CF = 1 iff BC >= 10; HL = BC - 10
                jr      nc, fsrn_t_done
                ld      b, h
                ld      c, l                    ; BC -= 10
                inc     a
                jr      fsrn_t
fsrn_t_done:
                ld      (de), a
                inc     de
                ; units digit (BC now 0..9).
                ld      a, c
                add     a, "0"
                ld      (de), a
                inc     de
                xor     a
                ld      (de), a                 ; NUL-terminate
                ret

; fsrn_nibble_to_hex — convert the low nibble of A (0..15) to a lowercase hex
; char ('0'..'9', 'a'..'f'). In: A = nibble (bits 7..4 must be 0). Out: A = char.
; Clobbers A only.
fsrn_nibble_to_hex:
                cp      10
                jr      c, fsrn_n_digit         ; 0..9 -> '0'..'9'
                add     a, "a" - 10             ; 10..15 -> 'a'..'f'
                ret
fsrn_n_digit:
                add     a, "0"
                ret
