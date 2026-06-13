; hkdf_expand_label.asm — TLS 1.3 HKDF-Expand-Label (RFC 8446 §7.1), over hkdf.asm.
;
; HKDF-Expand-Label(Secret, Label, Context, Length) =
;   HKDF-Expand(Secret, HkdfLabel, Length), where HkdfLabel is the encoding
;     struct {
;       uint16 length = Length;                      // 2 bytes, big-endian
;       opaque label<7..255>   = "tls13 " + Label;   // 1-byte len prefix + bytes
;       opaque context<0..255> = Context;            // 1-byte len prefix + bytes
;     }
;
; Every TLS 1.3 handshake/traffic secret + the record keys/IVs are an
; Expand-Label off a Derive-Secret (RFC 8446 §7.1): this is the function the whole
; key schedule is built from.  It adds no new arithmetic — it assembles the
; HkdfLabel bytes and calls hkdf_expand — so it inherits the SHA-256/HMAC/HKDF
; verification.  The next i88 building block above HKDF; Derive-Secret is just
; Expand-Label with Context = the transcript hash (a later thin caller).
;
; AUTHORITY / VERIFICATION: host-verifiable in the project's standard way.
; tools/netboot-oracle/z80/hkdf_expand_label_test.go assembles this file, runs
; expand_label under the koron-go/z80 harness, and asserts the output equals a Go
; reference of RFC 8446 §7.1 (built on Go's crypto/hkdf) — itself anchored to the
; independent RFC 8448 known-answer (the TLS 1.3 Early-Secret "derived" secret) —
; across the TLS key-schedule labels (derived / key / iv / finished, empty and
; hash contexts).  NOT host-verifiable: nothing here is hardware-gated.

                include "hkdf.asm"      ; -> hmac_sha256.asm -> sha256.asm; org &8000
                                        ; under NETBOOT_STANDALONE; provides hkdf_expand

EL_HASHLEN:     equ 32                  ; the secret size (a TLS 1.3 secret is HashLen)

; ===========================================================================
; Parameters (the caller fills these) + the HkdfLabel assembly buffer.
; ===========================================================================
EL_SECRET_PTR:  defs 2                  ; in: the secret (EL_HASHLEN bytes)
EL_LABEL_PTR:   defs 2                  ; in: the label (without the "tls13 " prefix)
EL_LABEL_LEN:   defs 2                  ; in: the label length (label <= 249)
EL_CTX_PTR:     defs 2                  ; in: the context
EL_CTX_LEN:     defs 2                  ; in: the context length (context <= 255)
EL_LENGTH:      defs 2                  ; in: the desired output length

; The assembled HkdfLabel.  Sized to the RFC 8446 §7.1 maximum so it cannot
; overflow on any input: length(2) + label-vector(1 + up-to-255) + context-vector
; (1 + up-to-255) = 514.  (TLS 1.3 in practice uses tiny labels/contexts — a key
; schedule HkdfLabel is ~54 bytes — but the primitive accepts the full range.)
EL_INFO:        defs 514

; The fixed TLS 1.3 label prefix.
el_prefix:      defm "tls13 "           ; 6 bytes: t l s 1 3 space

; ===========================================================================
; expand_label — HKDF-Expand-Label per RFC 8446 §7.1; the OKM is written to (HL).
; In:  HL = output pointer; EL_SECRET_PTR / EL_LABEL_* / EL_CTX_* / EL_LENGTH set.
; Out: EL_LENGTH bytes at the output pointer.  Clobbers A, BC, DE, HL, IX + scratch.
; ===========================================================================
expand_label:
                push    hl                      ; save the output pointer

                ; the secret becomes the HKDF PRK (EL_HASHLEN bytes).
                ld      hl, (EL_SECRET_PTR)
                ld      de, HKDF_PRK
                ld      bc, EL_HASHLEN
                ldir

                ; --- assemble the HkdfLabel into EL_INFO ---
                ld      de, EL_INFO            ; running dest pointer
                ; uint16 length, big-endian.
                ld      hl, (EL_LENGTH)
                ld      a, h
                ld      (de), a
                inc     de
                ld      a, l
                ld      (de), a
                inc     de
                ; opaque label<7..255>: 1-byte length = 6 + label_len, then prefix+label.
                ld      a, (EL_LABEL_LEN)      ; low byte (label_len <= 249)
                add     a, 6
                ld      (de), a
                inc     de
                ld      hl, el_prefix          ; "tls13 "
                ld      bc, 6
                ldir
                ld      bc, (EL_LABEL_LEN)     ; the label bytes (if any)
                ld      a, b
                or      c
                jr      z, el_ctx
                ld      hl, (EL_LABEL_PTR)
                ldir
el_ctx:
                ; opaque context<0..255>: 1-byte length, then the context bytes.
                ld      a, (EL_CTX_LEN)        ; low byte (context <= 255)
                ld      (de), a
                inc     de
                ld      bc, (EL_CTX_LEN)
                ld      a, b
                or      c
                jr      z, el_done_info
                ld      hl, (EL_CTX_PTR)
                ldir
el_done_info:
                ; HKDF_INFO = (EL_INFO, DE - EL_INFO); HKDF_L = EL_LENGTH.
                ld      hl, EL_INFO
                ld      (HKDF_INFO_PTR), hl
                ex      de, hl                  ; HL = end pointer
                ld      de, EL_INFO
                or      a
                sbc     hl, de                  ; HL = HkdfLabel length
                ld      (HKDF_INFO_LEN), hl
                ld      hl, (EL_LENGTH)
                ld      (HKDF_L), hl

                pop     hl                      ; the output pointer
                jp      hkdf_expand
