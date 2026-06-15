; tls_client_hello.asm — the TLS 1.3 ClientHello builder (RFC 8446 §4.1.2),
; handshake brick 3 (docs/specs/tls13-handshake.md).  It emits the bytes of our
; single fixed offer: one cipher suite (TLS_CHACHA20_POLY1305_SHA256), an X25519
; key_share from a supplied ephemeral public key, and the four required extensions
; (server_name / supported_versions / supported_groups / signature_algorithms +
; key_share).  The output is the Handshake message (type=client_hello || uint24
; length || ClientHello body) — the bytes that both feed the transcript hash and,
; wrapped in a plaintext record by the state machine (brick 6), go on the wire.
;
; Pure byte assembly — no crypto primitives, so nothing is include'd; built with
; -D NETBOOT_TLS_CH it sets the org once.  Variable-length container lengths
; (the handshake length, the extensions-block length, and the SNI extension's
; nested lengths) are emitted as placeholders and back-patched from the actual
; emitted positions, so they cannot drift from the content; the fixed-size
; extensions carry literal length bytes.
;
; The only variable input is the SNI host name (CH_HOSTNAME / CH_HOSTNAME_LEN);
; the cipher suite, version, group, and signature_algorithms list are constants.
; legacy_session_id is always the supplied 32-byte value (the middlebox-compat
; convention, RFC 8446 §4.1.2 / §D.4) and legacy_compression_methods is the single
; null method.  No ALPN/PSK/early-data extensions — the smallest conformant offer
; github completes (HTTP/1.1 is the default with no ALPN).
;
; AUTHORITY / VERIFICATION: host-verifiable in the project's standard way.
; tools/netboot-oracle/z80/tls_client_hello_test.go assembles this file, runs
; tls_build_client_hello under the koron-go/z80 harness, and asserts: (a) the
; emitted bytes equal an independent Go reconstruction of the §4.1.2 framing
; (byte-exact golden); (b) Go crypto/tls parses the message into a
; tls.ClientHelloInfo with the expected ServerName / CipherSuites / Supported
; Versions / SupportedCurves / SupportedSignatureSchemes — a genuinely independent
; structural check by the stdlib TLS implementation, which also fails if the
; message is malformed; and (c) the key_share entry carries group x25519 and the
; supplied 32-byte public key.  NOT host-verifiable: nothing here is hardware-gated.

                if defined(NETBOOT_TLS_CH)
                org     &8000
                endif

; ===========================================================================
; Inputs (the caller fills these before calling tls_build_client_hello).
; ===========================================================================
CH_RANDOM:      defs 32                 ; in: the 32-byte ClientHello.random
CH_SESSION_ID:  defs 32                 ; in: the 32-byte legacy_session_id (middlebox-compat)
CH_PUBKEY:      defs 32                 ; in: the X25519 ephemeral public key (the key_share)
CH_HOSTNAME:    defs 256                ; in: the SNI host name bytes
CH_HOSTNAME_LEN: defs 2                 ; in: the SNI host name length (1..255)

; ===========================================================================
; Outputs.
; ===========================================================================
CH_MSG:         defs 512                ; out: the ClientHello handshake message
CH_MSG_LEN:     defs 2                  ; out: its length (4-byte header + body)

; ===========================================================================
; Scratch — saved positions of the back-patched length fields.
; ===========================================================================
ch_p_hs:        defs 2                  ; the handshake message length (low 2 of the uint24)
ch_p_ext:       defs 2                  ; the extensions-block length
ch_p_sni_ext:   defs 2                  ; the server_name extension_data length
ch_p_sni_list:  defs 2                  ; the server_name_list length

; ===========================================================================
; tls_build_client_hello — build CH_MSG / CH_MSG_LEN from the inputs above.
; Clobbers A, BC, DE, HL.
; ===========================================================================
tls_build_client_hello:
                ld      de, CH_MSG              ; DE = the running write pointer

                ; --- Handshake header: type=client_hello(1) || uint24 length ---
                ld      a, 1
                call    ch_emit_a               ; HandshakeType client_hello
                ld      a, 0
                call    ch_emit_a               ; uint24 length, high byte (always 0)
                ld      (ch_p_hs), de           ; back-patch the low 2 bytes
                inc     de
                inc     de

                ; --- ClientHello body ---
                ; legacy_version = 0x0303
                ld      a, &03
                call    ch_emit_a
                ld      a, &03
                call    ch_emit_a
                ; random[32]
                ld      hl, CH_RANDOM
                ld      bc, 32
                ldir
                ; legacy_session_id<0..32> = 32 || the supplied id
                ld      a, 32
                call    ch_emit_a
                ld      hl, CH_SESSION_ID
                ld      bc, 32
                ldir
                ; cipher_suites<2..> = 0x0002 || TLS_CHACHA20_POLY1305_SHA256
                ld      a, 0
                call    ch_emit_a
                ld      a, 2
                call    ch_emit_a
                ld      hl, ch_cipher_suites
                ld      bc, 2
                ldir
                ; legacy_compression_methods<1..> = 0x01 || null(0x00)
                ld      a, 1
                call    ch_emit_a
                ld      a, 0
                call    ch_emit_a

                ; --- extensions<8..> ---
                ld      (ch_p_ext), de          ; back-patch the 2-byte extensions length
                inc     de
                inc     de

                ; server_name (SNI), RFC 6066 §3
                ld      a, &00
                call    ch_emit_a               ; extension_type = server_name (0x0000)
                ld      a, &00
                call    ch_emit_a
                ld      (ch_p_sni_ext), de      ; extension_data length (= 5 + hlen)
                inc     de
                inc     de
                ld      (ch_p_sni_list), de     ; server_name_list length (= 3 + hlen)
                inc     de
                inc     de
                ld      a, &00
                call    ch_emit_a               ; name_type = host_name (0x00)
                ld      hl, (CH_HOSTNAME_LEN)   ; HostName<1..> length, big-endian
                ld      a, h
                call    ch_emit_a
                ld      a, l
                call    ch_emit_a
                ld      bc, (CH_HOSTNAME_LEN)   ; the host name bytes
                ld      hl, CH_HOSTNAME
                ld      a, b
                or      c
                jr      z, ch_no_host
                ldir
ch_no_host:
                ld      hl, (ch_p_sni_list)
                call    ch_close_len16
                ld      hl, (ch_p_sni_ext)
                call    ch_close_len16

                ; supported_versions = TLS 1.3 only (RFC 8446 §4.2.1)
                ld      hl, ch_ext_supported_versions
                ld      bc, 7
                ldir
                ; supported_groups = x25519 only (RFC 8446 §4.2.7)
                ld      hl, ch_ext_supported_groups
                ld      bc, 8
                ldir
                ; signature_algorithms (RFC 8446 §4.2.3)
                ld      hl, ch_ext_sig_algs
                ld      bc, ch_ext_sig_algs_len
                ldir
                ; key_share = one X25519 entry (RFC 8446 §4.2.8)
                ld      hl, ch_ext_key_share_prefix
                ld      bc, 10
                ldir
                ld      hl, CH_PUBKEY
                ld      bc, 32
                ldir

                ; close extensions, then the handshake length
                ld      hl, (ch_p_ext)
                call    ch_close_len16
                ld      hl, (ch_p_hs)
                call    ch_close_len16

                ; CH_MSG_LEN = DE - CH_MSG
                ld      hl, CH_MSG
                ex      de, hl                  ; HL = end ptr, DE = CH_MSG
                or      a
                sbc     hl, de
                ld      (CH_MSG_LEN), hl
                ret

; ---------------------------------------------------------------------------
; ch_emit_a — store A at (DE) and advance DE.  Preserves BC, HL.
; ---------------------------------------------------------------------------
ch_emit_a:
                ld      (de), a
                inc     de
                ret

; ---------------------------------------------------------------------------
; ch_close_len16 — back-patch a 2-byte big-endian length container.  HL points at
; the (placeholder) length field; the content occupies [HL+2 .. DE).  Writes
; (DE - (HL+2)) big-endian into HL[0],HL[1].  Preserves DE; clobbers A, HL.
; ---------------------------------------------------------------------------
ch_close_len16:
                push    de                      ; save the write pointer
                inc     hl
                inc     hl                      ; HL = content start (field + 2)
                ex      de, hl                  ; DE = content start, HL = write ptr
                or      a
                sbc     hl, de                  ; HL = content length
                dec     de
                dec     de                      ; DE = the length field
                ld      a, h
                ld      (de), a                 ; high byte
                inc     de
                ld      a, l
                ld      (de), a                 ; low byte
                pop     de                      ; restore the write pointer
                ret

; ===========================================================================
; Constant offer data.
; ===========================================================================
ch_cipher_suites:
                defb    &13,&03                 ; TLS_CHACHA20_POLY1305_SHA256 (the only suite)

ch_ext_supported_versions:
                defb    &00,&2b                 ; extension_type = supported_versions
                defb    &00,&03                 ; extension_data length
                defb    &02                     ; ProtocolVersion versions<2..254> length
                defb    &03,&04                 ; TLS 1.3

ch_ext_supported_groups:
                defb    &00,&0a                 ; extension_type = supported_groups
                defb    &00,&04                 ; extension_data length
                defb    &00,&02                 ; NamedGroup list length
                defb    &00,&1d                 ; x25519

ch_ext_sig_algs:
                defb    &00,&0d                 ; extension_type = signature_algorithms
                defb    &00,&12                 ; extension_data length (2 + 2*8 = 18)
                defb    &00,&10                 ; SignatureScheme list length (16)
                defb    &04,&03                 ; ecdsa_secp256r1_sha256
                defb    &08,&04                 ; rsa_pss_rsae_sha256
                defb    &04,&01                 ; rsa_pkcs1_sha256
                defb    &05,&03                 ; ecdsa_secp384r1_sha384
                defb    &08,&05                 ; rsa_pss_rsae_sha384
                defb    &05,&01                 ; rsa_pkcs1_sha384
                defb    &08,&06                 ; rsa_pss_rsae_sha512
                defb    &06,&01                 ; rsa_pkcs1_sha512
ch_ext_sig_algs_len: equ 22

ch_ext_key_share_prefix:
                defb    &00,&33                 ; extension_type = key_share
                defb    &00,&26                 ; extension_data length (2 + 36)
                defb    &00,&24                 ; client_shares length (36)
                defb    &00,&1d                 ; KeyShareEntry.group = x25519
                defb    &00,&20                 ; key_exchange<1..> length (32) — pubkey follows
