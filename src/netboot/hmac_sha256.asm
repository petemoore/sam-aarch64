; hmac_sha256.asm — HMAC-SHA256 (RFC 2104 / FIPS 198-1), built on sha256.asm.
;
; HMAC(K, m) = SHA256( (K0 ^ opad) || SHA256( (K0 ^ ipad) || m ) ), where K0 is
; the key sized to the 64-byte SHA-256 block: K0 = K padded with zeros if
; |K| <= 64, else K0 = SHA256(K) zero-padded to 64; ipad = 0x36 repeated, opad =
; 0x5c repeated.
;
; This is the first i88 (on-SAM TLS) building block above the landed SHA-256
; primitive: TLS 1.3's key schedule is HKDF (RFC 5869), and HKDF-Extract /
; HKDF-Expand are HMAC-SHA256. It is a thin orchestration over sha256.asm's
; streaming init/update/final — no new hashing arithmetic — so it inherits that
; primitive's verification and adds only the key prep + the two ipad/opad passes.
;
; AUTHORITY / VERIFICATION: host-verifiable in the project's standard way.
; tools/netboot-oracle/z80/hmac_sha256_test.go assembles this file, runs
; hmac_sha256 under the koron-go/z80 harness over the RFC 4231 test vectors (incl.
; the >64-byte-key case that exercises the K0 = SHA256(K) path, and empty
; key/message), and asserts the 32-byte MAC equals Go's crypto/hmac +
; crypto/sha256 of the same (key, message) byte-for-byte. There is no Go-side asm
; authority to port; HMAC is specified by RFC 2104 and the reference is Go's
; stdlib. NOT host-verifiable: nothing here is hardware-gated — it is pure
; arithmetic + memory, like sha256.asm.

                include "sha256.asm"    ; sets org &8000 under NETBOOT_STANDALONE;
                                        ; provides sha256_init/update/final + state

HMAC_BLOCK:     equ 64                  ; the SHA-256 block size (B)
HMAC_DIGEST:    equ 32                  ; the SHA-256 output size (L)
HMAC_IPAD:      equ &36
HMAC_OPAD:      equ &5c

; ===========================================================================
; Parameters (the caller fills these before calling hmac_sha256) + working
; buffers.  Data first so every label is defined before the code references it.
; ===========================================================================
HMAC_KEY_PTR:   defs 2                  ; in: key pointer
HMAC_KEY_LEN:   defs 2                  ; in: key length in bytes
HMAC_MSG_PTR:   defs 2                  ; in: message pointer
HMAC_MSG_LEN:   defs 2                  ; in: message length in bytes

HMAC_K0:        defs HMAC_BLOCK         ; the block-sized key (padded / hashed)
HMAC_PAD:       defs HMAC_BLOCK         ; K0 ^ ipad, then reused for K0 ^ opad
HMAC_INNER:     defs HMAC_DIGEST        ; the inner digest

; ===========================================================================
; hmac_sha256 — HMAC-SHA256 of (HMAC_MSG_PTR, HMAC_MSG_LEN) keyed by
; (HMAC_KEY_PTR, HMAC_KEY_LEN); the 32-byte MAC is written to (HL).
; In:  HL = output pointer; the four parameter cells set.
; Out: 32 bytes at the output pointer.  Clobbers A, BC, DE, HL, IX + sha256 scratch.
; ===========================================================================
hmac_sha256:
                push    hl                      ; save the output pointer
                call    hmac_prepare_k0         ; build HMAC_K0 (64 bytes)

                ; --- inner = SHA256( (K0 ^ ipad) || message ) ---
                ld      a, HMAC_IPAD
                call    hmac_xor_pad            ; HMAC_PAD = K0 ^ ipad
                call    sha256_init
                ld      hl, HMAC_PAD
                ld      bc, HMAC_BLOCK
                call    sha256_update
                ld      hl, (HMAC_MSG_PTR)
                ld      bc, (HMAC_MSG_LEN)
                call    sha256_update
                ld      hl, HMAC_INNER
                call    sha256_final

                ; --- MAC = SHA256( (K0 ^ opad) || inner ) ---
                ld      a, HMAC_OPAD
                call    hmac_xor_pad            ; HMAC_PAD = K0 ^ opad
                call    sha256_init
                ld      hl, HMAC_PAD
                ld      bc, HMAC_BLOCK
                call    sha256_update
                ld      hl, HMAC_INNER
                ld      bc, HMAC_DIGEST
                call    sha256_update
                pop     hl                      ; the output pointer
                jp      sha256_final            ; MAC -> (HL)

; ---------------------------------------------------------------------------
; hmac_prepare_k0 — fill HMAC_K0 (64 bytes) with the block-sized key: the key
; zero-padded if it fits a block, else SHA256(key) zero-padded.  Clobbers A, BC,
; DE, HL, IX + sha256 scratch.
; ---------------------------------------------------------------------------
hmac_prepare_k0:
                ; zero all 64 bytes (the pad tail of either branch).
                ld      hl, HMAC_K0
                ld      (hl), 0
                ld      de, HMAC_K0 + 1
                ld      bc, HMAC_BLOCK - 1
                ldir
                ; key_len > 64 ?  (compute key_len - 64; borrow => <64, zero => ==64)
                ld      hl, (HMAC_KEY_LEN)
                ld      de, HMAC_BLOCK
                or      a
                sbc     hl, de
                jr      c, hmac_k0_copy         ; key_len < 64  -> copy the key
                jr      z, hmac_k0_copy         ; key_len == 64 -> copy (fills exactly)
                ; key_len > 64 -> K0[0..31] = SHA256(key); [32..63] stay zero.
                call    sha256_init
                ld      hl, (HMAC_KEY_PTR)
                ld      bc, (HMAC_KEY_LEN)
                call    sha256_update
                ld      hl, HMAC_K0
                jp      sha256_final
hmac_k0_copy:
                ld      bc, (HMAC_KEY_LEN)
                ld      a, b
                or      c
                ret     z                       ; empty key -> K0 stays all-zero
                ld      hl, (HMAC_KEY_PTR)
                ld      de, HMAC_K0
                ldir
                ret

; ---------------------------------------------------------------------------
; hmac_xor_pad — HMAC_PAD[i] = HMAC_K0[i] ^ A, for i in 0..63.
; In:  A = the pad byte (ipad / opad).  Clobbers A, BC, DE, HL.
; ---------------------------------------------------------------------------
hmac_xor_pad:
                ld      c, a                    ; the pad byte
                ld      hl, HMAC_K0
                ld      de, HMAC_PAD
                ld      b, HMAC_BLOCK
hxp_loop:
                ld      a, (hl)
                xor     c
                ld      (de), a
                inc     hl
                inc     de
                djnz    hxp_loop
                ret
