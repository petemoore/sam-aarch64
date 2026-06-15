; aead.asm — the AEAD_CHACHA20_POLY1305 construction (RFC 8439 §2.8), the
; authenticated encryption TLS 1.3 record protection uses.  It composes the
; already-verified ChaCha20 (chacha20.asm) and Poly1305 (poly1305.asm) primitives:
;
;   otk        = poly1305_key_gen(key, nonce) = chacha20_block(key, 0, nonce)[0..32]
;   ciphertext = chacha20_encrypt(key, counter=1, nonce, plaintext)
;   mac_data   = AAD || pad16(AAD) || ciphertext || pad16(ciphertext)
;                    || le64(len AAD) || le64(len ciphertext)
;   tag        = poly1305_mac(otk, mac_data)
;
; aead_encrypt produces ciphertext + tag; aead_decrypt recomputes the tag over the
; received ciphertext, compares (difference-accumulating, no early-out), and only
; then decrypts — setting AEAD_OK to 1 (authentic) or 0 (forged/tampered).
;
; This is built with -D NETBOOT_AEAD (NOT NETBOOT_STANDALONE): the org is set here
; once, and chacha20.asm / poly1305.asm are include'd with their own org guard
; inert, so their code+data lay out sequentially after this file's.  Their
; standalone builds are unchanged.
;
; NOTE: not constant-time (the underlying primitives branch on data); acceptable
; for this one-shot, content-hash-pinned firmware fetch (i88 scope, design §7).
;
; AUTHORITY / VERIFICATION: host-verifiable in the project's standard way.
; tools/netboot-oracle/z80/aead_test.go assembles this file, runs aead_encrypt /
; aead_decrypt / aead_keygen under the koron-go/z80 harness, and asserts: the RFC
; 8439 §2.8.2 (encryption) vector byte-for-byte on ciphertext + tag; the §2.6.2
; (poly1305_key_gen) vector on the one-time key; an encrypt-then-decrypt round-trip
; recovering the plaintext with AEAD_OK = 1 across lengths (empty / partial /
; multi-block) with and without AAD; and tamper rejection (a flipped tag /
; ciphertext / AAD byte must set AEAD_OK = 0).

                if defined(NETBOOT_AEAD)
                org     &8000
                endif

                include "chacha20.asm"          ; chacha20_block / chacha20_encrypt + CC_*
                include "poly1305.asm"          ; poly1305_mac + POLY_*

; ===========================================================================
; Inputs / outputs / working buffers.
; ===========================================================================
AEAD_KEY:       defs 32                 ; in: the 256-bit key
AEAD_NONCE:     defs 12                 ; in: the 96-bit nonce
AEAD_AAD:       defs 64                 ; in: the additional authenticated data
AEAD_AAD_LEN:   defs 2                  ; in: AAD length
AEAD_PT:        defs 1024               ; plaintext (in for encrypt / out for decrypt)
AEAD_CT:        defs 1024               ; ciphertext (out for encrypt / in for decrypt)
AEAD_LEN:       defs 2                  ; in: the plaintext/ciphertext length
AEAD_TAG:       defs 16                 ; out (encrypt) / in (decrypt): the 16-byte tag
AEAD_OK:        defs 1                  ; out (decrypt): 1 = authentic, 0 = forged

AEAD_OTK:       defs 32                 ; the one-time Poly1305 key
AEAD_BLOCK:     defs 64                 ; chacha20_block output (keygen scratch)
AEAD_TAG2:      defs 16                 ; the recomputed tag (decrypt)
AEAD_MACDATA:   defs 2176              ; the Poly1305 input (AAD|pad|CT|pad|lens)
AEAD_MACLEN:    defs 2                  ; its length
aead_mp:        defs 2                  ; the running mac_data append pointer

; ===========================================================================
; aead_keygen — AEAD_OTK = poly1305_key_gen(AEAD_KEY, AEAD_NONCE) = the first 32
; bytes of chacha20_block(key, counter=0, nonce).  Clobbers A, BC, DE, HL + the
; chacha scratch.
; ===========================================================================
aead_keygen:
                ld      hl, AEAD_KEY
                ld      de, CC_KEY
                ld      bc, 32
                ldir
                ld      hl, AEAD_NONCE
                ld      de, CC_NONCE
                ld      bc, 12
                ldir
                xor     a                       ; counter = 0
                ld      (CC_COUNTER), a
                ld      (CC_COUNTER + 1), a
                ld      (CC_COUNTER + 2), a
                ld      (CC_COUNTER + 3), a
                ld      hl, AEAD_BLOCK
                call    chacha20_block
                ld      hl, AEAD_BLOCK
                ld      de, AEAD_OTK
                ld      bc, 32
                ldir
                ret

; ---------------------------------------------------------------------------
; aead_stream — encrypt/decrypt: AEAD_CT or AEAD_PT = chacha20_encrypt(key,
; counter=1, nonce, src), for AEAD_LEN bytes.  In: HL = output buffer,
; DE = source pointer.  Clobbers A, BC, DE, HL + chacha scratch.
; ---------------------------------------------------------------------------
aead_stream:
                push    hl                      ; save output pointer
                ld      (CC_MSG_PTR), de        ; source
                ld      hl, AEAD_KEY
                ld      de, CC_KEY
                ld      bc, 32
                ldir
                ld      hl, AEAD_NONCE
                ld      de, CC_NONCE
                ld      bc, 12
                ldir
                xor     a                       ; counter = 1
                ld      (CC_COUNTER + 1), a
                ld      (CC_COUNTER + 2), a
                ld      (CC_COUNTER + 3), a
                inc     a
                ld      (CC_COUNTER), a
                ld      hl, (AEAD_LEN)
                ld      (CC_MSG_LEN), hl
                pop     hl                      ; output pointer
                jp      chacha20_encrypt

; ===========================================================================
; aead_encrypt — AEAD_CT[0..LEN] + AEAD_TAG from AEAD_KEY / AEAD_NONCE /
; AEAD_AAD[0..AAD_LEN] / AEAD_PT[0..LEN].  Clobbers everything + scratch.
; ===========================================================================
aead_encrypt:
                call    aead_keygen
                ld      hl, AEAD_CT             ; CT = chacha20_encrypt(PT)
                ld      de, AEAD_PT
                call    aead_stream
                call    aead_build_macdata      ; mac_data over AAD + CT
                ld      hl, AEAD_OTK
                ld      de, POLY_KEY
                ld      bc, 32
                ldir
                ld      hl, AEAD_MACDATA
                ld      (POLY_MSG_PTR), hl
                ld      hl, (AEAD_MACLEN)
                ld      (POLY_MSG_LEN), hl
                ld      hl, AEAD_TAG            ; tag -> AEAD_TAG
                jp      poly1305_mac

; ===========================================================================
; aead_decrypt — verify AEAD_TAG over AEAD_AAD + AEAD_CT[0..LEN]; on success set
; AEAD_OK=1 and decrypt into AEAD_PT, else AEAD_OK=0 (plaintext not released).
; Clobbers everything + scratch.
; ===========================================================================
aead_decrypt:
                call    aead_keygen
                call    aead_build_macdata      ; mac_data over AAD + CT
                ld      hl, AEAD_OTK
                ld      de, POLY_KEY
                ld      bc, 32
                ldir
                ld      hl, AEAD_MACDATA
                ld      (POLY_MSG_PTR), hl
                ld      hl, (AEAD_MACLEN)
                ld      (POLY_MSG_LEN), hl
                ld      hl, AEAD_TAG2          ; recomputed tag
                call    poly1305_mac

                ld      hl, AEAD_TAG            ; compare (difference-accumulating)
                ld      de, AEAD_TAG2
                ld      b, 16
                ld      c, 0
aead_cmp_loop:
                ld      a, (de)
                xor     (hl)
                or      c
                ld      c, a
                inc     hl
                inc     de
                djnz    aead_cmp_loop
                ld      a, c
                or      a
                jr      nz, aead_dec_fail

                ld      a, 1                    ; authentic -> decrypt
                ld      (AEAD_OK), a
                ld      hl, AEAD_PT
                ld      de, AEAD_CT
                jp      aead_stream
aead_dec_fail:
                xor     a
                ld      (AEAD_OK), a
                ret

; ---------------------------------------------------------------------------
; aead_build_macdata — AEAD_MACDATA = AAD || pad16(AAD) || CT || pad16(CT) ||
; le64(AAD_LEN) || le64(LEN); AEAD_MACLEN set.  Clobbers A, BC, DE, HL.
; ---------------------------------------------------------------------------
aead_build_macdata:
                ld      hl, AEAD_MACDATA
                ld      (aead_mp), hl
                ld      hl, AEAD_AAD
                ld      bc, (AEAD_AAD_LEN)
                call    aead_append
                ld      hl, (AEAD_AAD_LEN)
                call    aead_pad16
                ld      hl, AEAD_CT
                ld      bc, (AEAD_LEN)
                call    aead_append
                ld      hl, (AEAD_LEN)
                call    aead_pad16
                ld      hl, (AEAD_AAD_LEN)
                call    aead_append_le64
                ld      hl, (AEAD_LEN)
                call    aead_append_le64
                ld      hl, (aead_mp)           ; maclen = mp - MACDATA
                ld      de, AEAD_MACDATA
                or      a
                sbc     hl, de
                ld      (AEAD_MACLEN), hl
                ret

; aead_append — copy BC bytes from (HL) to the running mac_data pointer.
aead_append:
                ld      a, b
                or      c
                ret     z                       ; nothing to append (e.g. empty AAD)
                ld      de, (aead_mp)
                ldir
                ld      (aead_mp), de
                ret

; aead_pad16 — append (16 - (HL & 15)) & 15 zero bytes to mac_data.
aead_pad16:
                ld      a, l
                and     15
                ret     z                       ; already a multiple of 16
                ld      b, a
                ld      a, 16
                sub     b
                ld      b, a                    ; pad count (1..15)
                ld      de, (aead_mp)
                xor     a
aead_pad_loop:
                ld      (de), a
                inc     de
                djnz    aead_pad_loop
                ld      (aead_mp), de
                ret

; aead_append_le64 — append HL as a little-endian 64-bit value (low 16 + 6 zeros).
aead_append_le64:
                ld      de, (aead_mp)
                ld      a, l
                ld      (de), a
                inc     de
                ld      a, h
                ld      (de), a
                inc     de
                xor     a
                ld      b, 6
aead_le64_loop:
                ld      (de), a
                inc     de
                djnz    aead_le64_loop
                ld      (aead_mp), de
                ret
