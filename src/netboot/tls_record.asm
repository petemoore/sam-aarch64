; tls_record.asm — TLS 1.3 record protection (RFC 8446 §5.2–5.3), handshake brick 2
; (docs/specs/tls13-handshake.md).  The thin framing layer over the verified
; AEAD_CHACHA20_POLY1305 (aead.asm): it builds the per-record nonce and AAD and the
; TLSInnerPlaintext, then calls aead_encrypt / aead_decrypt.
;
;   nonce       = write_iv XOR (zeros(4) || seq_be64)         (§5.3)
;   AAD         = the 5-byte TLSCiphertext header:
;                   opaque_type=23 || legacy_version=0x0303 || length            (§5.2)
;                 where length = len(TLSInnerPlaintext) + 16 (the AEAD tag)
;   inner       = content || real_content_type  (no padding sent)               (§5.2)
;   record      = AAD(5) || aead_encrypt(key, nonce, AAD, inner)
;
; tls_record_seal produces a full TLSCiphertext record; tls_record_open verifies +
; decrypts one (AEAD authenticity gates output), then strips any zero padding and
; the trailing content-type byte to recover (content, type).  The sequence number
; is the caller's (it increments per record, resetting on a key change) — passed in,
; not tracked here.
;
; Built with -D NETBOOT_TLS_RECORD (NOT NETBOOT_AEAD / NETBOOT_STANDALONE), so the
; include chain's org guard (in chacha20.asm/poly1305.asm, the bases) and aead.asm's
; own NETBOOT_AEAD org stay inert and this file sets the org once — the composed
; modules' standalone builds are unchanged (the aead.asm composition idiom).
;
; NOTE: not constant-time (the AEAD branches on data); acceptable for the pinned
; one-shot fetch (i88 scope, design §7).
;
; AUTHORITY / VERIFICATION: host-verifiable in the project's standard way.
; tools/netboot-oracle/z80/tls_record_test.go assembles this file, runs
; tls_record_seal / tls_record_open under the koron-go/z80 harness, and asserts:
; (a) a seal->open round-trip recovers (content, type) with TR_OK=1 across content
; types, lengths, and sequence numbers; (b) the sealed record's header + encrypted
; bytes equal an independent Go reconstruction of the §5.2/§5.3 framing (nonce =
; iv XOR seq, AAD = header, inner = content||type) fed through the verified Z80
; aead_encrypt; and (c) a tampered record / wrong sequence number fails (TR_OK=0).
; NOT host-verifiable: nothing here is hardware-gated.

                if defined(NETBOOT_TLS_RECORD)
                org     &8000
                endif

                include "aead.asm"             ; aead_encrypt/aead_decrypt + AEAD_* ;
                                               ; -> chacha20.asm + poly1305.asm

; ===========================================================================
; Inputs / outputs / scratch.
; ===========================================================================
TR_KEY:         defs 32                 ; in: the record write/read key
TR_IV:          defs 12                 ; in: the record write/read IV
TR_SEQ:         defs 8                  ; in: the 64-bit record sequence (big-endian)
TR_TYPE:        defs 1                  ; in (seal): real content type / out (open): recovered
TR_CONTENT:     defs 1024               ; in (seal) / out (open): the plaintext content
TR_CONTENT_LEN: defs 2                  ; in (seal) / out (open): content length
TR_RECORD:      defs 1056               ; out (seal) / in (open): the TLSCiphertext record
TR_RECORD_LEN:  defs 2                  ; out (seal) / in (open): record length (header+ct+tag)
TR_OK:          defs 1                  ; out (open): 1 = authentic, 0 = forged/malformed

; ===========================================================================
; tls_record_seal — TR_RECORD = a TLSCiphertext sealing TR_CONTENT[0..LEN] as
; content type TR_TYPE, under TR_KEY/TR_IV at sequence TR_SEQ.
; Clobbers everything + AEAD/cipher scratch.
; ===========================================================================
tls_record_seal:
                call    tr_set_nonce            ; AEAD_NONCE = iv XOR seq

                ; inner = content || type  -> AEAD_PT ; AEAD_LEN = content_len + 1
                ld      hl, TR_CONTENT
                ld      de, AEAD_PT
                ld      bc, (TR_CONTENT_LEN)
                ld      a, b
                or      c
                jr      z, trs_no_content
                ldir
trs_no_content:
                ld      a, (TR_TYPE)            ; DE points just past the copied content
                ld      (de), a
                ld      hl, (TR_CONTENT_LEN)
                inc     hl                      ; inner length = content_len + 1
                ld      (AEAD_LEN), hl

                ; AAD header = 17 03 03 (len+16 big-endian) -> AEAD_AAD + TR_RECORD
                ld      hl, (AEAD_LEN)
                ld      de, 16
                add     hl, de                  ; L = inner_len + tag(16)
                ld      a, &17
                ld      (AEAD_AAD), a
                ld      a, &03
                ld      (AEAD_AAD + 1), a
                ld      (AEAD_AAD + 2), a
                ld      a, h                    ; length big-endian
                ld      (AEAD_AAD + 3), a
                ld      a, l
                ld      (AEAD_AAD + 4), a
                ld      hl, 5
                ld      (AEAD_AAD_LEN), hl
                ld      hl, AEAD_AAD            ; header -> TR_RECORD[0..4]
                ld      de, TR_RECORD
                ld      bc, 5
                ldir

                ld      hl, TR_KEY             ; key -> AEAD_KEY
                ld      de, AEAD_KEY
                ld      bc, 32
                ldir
                call    aead_encrypt           ; AEAD_CT (AEAD_LEN) + AEAD_TAG (16)

                ; record = header || ciphertext || tag
                ld      hl, AEAD_CT
                ld      de, TR_RECORD + 5
                ld      bc, (AEAD_LEN)
                ld      a, b
                or      c
                jr      z, trs_no_ct
                ldir
trs_no_ct:
                ld      hl, AEAD_TAG           ; DE points just past the ciphertext
                ld      bc, 16
                ldir
                ; TR_RECORD_LEN = 5 + inner_len + 16
                ld      hl, (AEAD_LEN)
                ld      de, 21                 ; 5 header + 16 tag
                add     hl, de
                ld      (TR_RECORD_LEN), hl
                ret

; ===========================================================================
; tls_record_open — verify + decrypt the TLSCiphertext in TR_RECORD[0..LEN]:
; on success set TR_OK=1 and recover (TR_CONTENT[0..CONTENT_LEN], TR_TYPE); else
; TR_OK=0.  Clobbers everything + AEAD/cipher scratch.
; ===========================================================================
tls_record_open:
                call    tr_set_nonce            ; AEAD_NONCE = iv XOR seq

                ; AAD = the 5-byte header (TR_RECORD[0..4])
                ld      hl, TR_RECORD
                ld      de, AEAD_AAD
                ld      bc, 5
                ldir
                ld      hl, 5
                ld      (AEAD_AAD_LEN), hl

                ; ct_len = record_len - 5 - 16
                ld      hl, (TR_RECORD_LEN)
                ld      de, 21
                or      a
                sbc     hl, de
                ld      (AEAD_LEN), hl          ; the ciphertext length

                ; ciphertext -> AEAD_CT, tag -> AEAD_TAG
                ld      hl, TR_RECORD + 5
                ld      de, AEAD_CT
                ld      bc, (AEAD_LEN)
                ld      a, b
                or      c
                jr      z, tro_no_ct
                ldir
tro_no_ct:
                ; HL now points just past the ciphertext = the tag (TR_RECORD+5+ct_len)
                ld      de, AEAD_TAG
                ld      bc, 16
                ldir

                ld      hl, TR_KEY             ; key -> AEAD_KEY
                ld      de, AEAD_KEY
                ld      bc, 32
                ldir
                call    aead_decrypt           ; AEAD_OK + AEAD_PT (inner)

                ld      a, (AEAD_OK)
                ld      (TR_OK), a
                or      a
                ret     z                      ; not authentic -> leave content unset

                ; strip trailing zero padding; the last non-zero byte is the type.
                ; scan AEAD_PT[AEAD_LEN-1] downward to the first non-zero.
                ld      hl, (AEAD_LEN)
                ld      a, h
                or      l
                jr      z, tro_malformed       ; empty inner -> no content type
                ld      de, AEAD_PT
                add     hl, de                 ; HL = AEAD_PT + inner_len (one past end)
tro_scan:
                dec     hl
                ld      a, (hl)
                or      a
                jr      nz, tro_found
                ; was this byte index 0? if HL == AEAD_PT we ran out -> malformed
                push    hl
                ld      de, AEAD_PT
                or      a
                sbc     hl, de                 ; HL = offset; 0 => we just tested index 0
                ld      a, h
                or      l
                pop     hl
                jr      z, tro_malformed
                jr      tro_scan
tro_found:
                ; HL -> the content-type byte; type = (HL); content len = HL - AEAD_PT
                ld      a, (hl)
                ld      (TR_TYPE), a
                push    hl
                ld      de, AEAD_PT
                or      a
                sbc     hl, de                 ; HL = content length
                ld      (TR_CONTENT_LEN), hl
                ld      b, h
                ld      c, l                   ; BC = content length
                pop     hl                     ; HL -> type byte (unused now)
                ld      a, b
                or      c
                jr      z, tro_done            ; zero-length content
                ld      hl, AEAD_PT
                ld      de, TR_CONTENT
                ldir
tro_done:
                ret
tro_malformed:
                xor     a
                ld      (TR_OK), a
                ret

; ---------------------------------------------------------------------------
; tr_set_nonce — AEAD_NONCE = TR_IV XOR (zeros(4) || TR_SEQ), per RFC 8446 §5.3.
; Clobbers A, B, DE, HL.
; ---------------------------------------------------------------------------
tr_set_nonce:
                ld      hl, TR_IV
                ld      de, AEAD_NONCE
                ld      bc, 12
                ldir                            ; nonce = iv
                ld      hl, TR_SEQ              ; XOR the 64-bit seq into the low 8 bytes
                ld      de, AEAD_NONCE + 4
                ld      b, 8
tr_nonce_xor:
                ld      a, (de)
                xor     (hl)
                ld      (de), a
                inc     hl
                inc     de
                djnz    tr_nonce_xor
                ret
