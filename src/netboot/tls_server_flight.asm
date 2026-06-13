; tls_server_flight.asm — the TLS 1.3 server-flight parser (RFC 8446 §4.1.3,
; §4.3), handshake brick 4 (docs/specs/tls13-handshake.md).  Two functions over
; the server's 1-RTT response:
;
;   tls_parse_server_hello — given the ServerHello handshake message, extract the
;     server's X25519 key_share and validate the negotiated parameters.  Sets
;     SH_OK=1 only if: msg type=server_hello(2), the random is NOT the
;     HelloRetryRequest sentinel (§4.1.3 — we do not handle HRR), the chosen
;     cipher_suite is TLS_CHACHA20_POLY1305_SHA256 (0x1303), the supported_versions
;     extension selects TLS 1.3 (0x0304), and a key_share extension carries group
;     x25519 (0x001d) with a 32-byte key (copied to SH_SERVER_PUB).
;
;   tls_walk_server_flight — walk the *decrypted* handshake flight that follows the
;     ServerHello (a contiguous buffer of concatenated handshake messages —
;     EncryptedExtensions, Certificate, CertificateVerify, Finished; the caller
;     reassembles records into one buffer before calling).  Each message
;     (type || uint24 length || body) is folded into the running transcript hash;
;     just before the Finished is folded, a snapshot of the transcript so far
;     (Hash(CH..CertificateVerify), the context for verifying the server Finished)
;     is written to SF_HASH_BEFORE_FIN; the Finished's verify_data is captured to
;     SF_FINISHED.  Sets SF_OK=1 iff all four expected messages were seen.  The
;     transcript must already contain ClientHello..ServerHello (the caller feeds
;     those and tls_transcript_init's the stream); this walk feeds only the
;     encrypted flight.
;
; Built with -D NETBOOT_TLS_SF (the aead.asm composition idiom): it include's
; tls_transcript.asm (-> sha256.asm) so the walk can fold + snapshot the
; transcript; those modules' org guards stay inert and this file sets the org once.
;
; AUTHORITY / VERIFICATION: host-verifiable in the project's standard way.
; tools/netboot-oracle/z80/tls_server_flight_test.go assembles this file and, under
; the koron-go/z80 harness, asserts: (a) tls_parse_server_hello recovers the
; X25519 key_share from a hand-built ServerHello (SH_OK=1, SH_SERVER_PUB == the
; server pub), and rejects (SH_OK=0) a wrong cipher suite, a missing key_share, a
; non-1.3 supported_versions, and the HRR sentinel random; (b) tls_walk_server
; _flight over a hand-built EE/Certificate/CertificateVerify/Finished flight
; captures the Finished verify_data, sets SF_OK=1 with all four flags, and that
; SF_HASH_BEFORE_FIN == Go SHA-256(EE||Cert||CertVerify) while a post-walk
; transcript snapshot == Go SHA-256(the whole flight) — proving the messages were
; folded in order with the snapshot taken at the right point.  NOT host-verifiable:
; nothing here is hardware-gated.

                if defined(NETBOOT_TLS_SF)
                org     &8000
                endif

                include "tls_transcript.asm"  ; tls_transcript_init/update/snapshot
                                              ; -> sha256.asm

; ===========================================================================
; ServerHello parser — inputs / outputs / scratch.
; ===========================================================================
SH_MSG:         defs 512                ; in: the ServerHello handshake message
SH_OK:          defs 1                  ; out: 1 = valid TLS 1.3 ServerHello for our suite
SH_SERVER_PUB:  defs 32                 ; out: the server's X25519 key_share

sh_found_ks:    defs 1                  ; saw a valid x25519 key_share
sh_found_sv:    defs 1                  ; saw supported_versions = TLS 1.3
sh_data:        defs 2                  ; current extension's data pointer
sh_ext_end:     defs 2                  ; one past the extensions block

; SHA-256("HelloRetryRequest"), the special ServerHello.random that marks a
; HelloRetryRequest (RFC 8446 §4.1.3).  We offer x25519, which github supports, so
; an HRR should never occur; if it does we reject rather than retry.
sh_hrr_sentinel:
                defb &CF,&21,&AD,&74,&E5,&9A,&61,&11
                defb &BE,&1D,&8C,&02,&1E,&65,&B8,&91
                defb &C2,&A2,&11,&16,&7A,&BB,&8C,&5E
                defb &07,&9E,&09,&E2,&C8,&A8,&33,&9C

; ===========================================================================
; Server-flight walk — inputs / outputs / scratch.
; ===========================================================================
SF_FLIGHT:      defs 8192               ; in: the decrypted concatenated flight
SF_FLIGHT_LEN:  defs 2                  ; in: its length
SF_OK:          defs 1                  ; out: 1 = saw EE + Cert + CertVerify + Finished
SF_FINISHED:    defs 64                 ; out: the server Finished verify_data
SF_FINISHED_LEN: defs 2                 ; out: its length (32 for SHA-256)
SF_HASH_BEFORE_FIN: defs 32             ; out: Hash(CH..CertificateVerify), the
                                        ;      context for verifying the server Finished
SF_SNAP:        defs 32                 ; scratch: a snapshot buffer for verification

sf_saw_ee:      defs 1
sf_saw_cert:    defs 1
sf_saw_cv:      defs 1
sf_saw_fin:     defs 1
sf_end:         defs 2                  ; one past the flight
sf_msg_start:   defs 2                  ; the current message's first byte
sf_msg_type:    defs 1
sf_body_len:    defs 2                  ; the current message's body length

; ===========================================================================
; tls_parse_server_hello — validate SH_MSG and extract SH_SERVER_PUB.
; Clobbers A, BC, DE, HL.
; ===========================================================================
tls_parse_server_hello:
                xor     a
                ld      (SH_OK), a
                ld      (sh_found_ks), a
                ld      (sh_found_sv), a

                ld      a, (SH_MSG)             ; handshake type must be server_hello(2)
                cp      2
                ret     nz

                ld      hl, SH_MSG + 4          ; body start (skip the 4-byte header)
                inc     hl                      ; skip legacy_version (2 bytes)
                inc     hl

                ; reject the HelloRetryRequest sentinel random
                ld      de, sh_hrr_sentinel
                ld      b, 32
sh_hrr_cmp:
                ld      a, (de)
                cp      (hl)
                jr      nz, sh_not_hrr
                inc     hl
                inc     de
                djnz    sh_hrr_cmp
                ret                             ; all 32 matched -> HRR -> SH_OK stays 0
sh_not_hrr:
                ; HL is mid-random; realign to just past the 32-byte random.
                ld      hl, SH_MSG + 4 + 2 + 32 ; past header+version+random
                ; legacy_session_id_echo: 1-byte length then that many bytes
                ld      a, (hl)
                inc     hl
                ld      e, a
                ld      d, 0
                add     hl, de                  ; HL = cipher_suite

                ; cipher_suite must be TLS_CHACHA20_POLY1305_SHA256 (0x1303)
                ld      a, (hl)
                cp      &13
                ret     nz
                inc     hl
                ld      a, (hl)
                cp      &03
                ret     nz
                inc     hl                      ; HL = legacy_compression_method
                inc     hl                      ; skip it -> HL = extensions length

                ; extensions length (2 bytes big-endian) -> DE; HL = first extension
                ld      d, (hl)
                inc     hl
                ld      e, (hl)
                inc     hl
                push    hl                      ; first extension
                add     hl, de                  ; ext_end = first + ext_len
                ld      (sh_ext_end), hl
                pop     hl                      ; HL = cursor at first extension

sh_ext_loop:
                ld      de, (sh_ext_end)        ; done when cursor >= ext_end
                or      a
                push    hl
                sbc     hl, de
                pop     hl
                jr      nc, sh_ext_done

                ld      d, (hl)                 ; extension_type (2)
                inc     hl
                ld      e, (hl)
                inc     hl
                ld      b, (hl)                 ; extension_data length (2)
                inc     hl
                ld      c, (hl)
                inc     hl                      ; HL = extension_data; BC = its length
                ld      (sh_data), hl

                ld      a, d                    ; our extensions all have type-hi 0
                or      a
                jr      nz, sh_ext_advance
                ld      a, e
                cp      &33
                jr      z, sh_do_ks
                cp      &2b
                jr      z, sh_do_sv
sh_ext_advance:
                ld      hl, (sh_data)
                add     hl, bc                  ; skip the data -> next extension
                jr      sh_ext_loop
sh_do_ks:
                push    bc
                call    sh_extract_ks
                pop     bc
                jr      sh_ext_advance
sh_do_sv:
                push    bc
                call    sh_check_sv
                pop     bc
                jr      sh_ext_advance

sh_ext_done:
                ld      a, (sh_found_ks)        ; SH_OK = found_ks AND found_sv
                ld      b, a
                ld      a, (sh_found_sv)
                and     b
                ld      (SH_OK), a
                ret

; sh_extract_ks — the key_share extension_data at (sh_data) is one KeyShareEntry:
; group(2) || key_exchange<1..>(2-byte len || key).  Require group x25519 (0x001d)
; and a 32-byte key; copy it to SH_SERVER_PUB and set sh_found_ks.  Clobbers A,BC,DE,HL.
sh_extract_ks:
                ld      hl, (sh_data)
                ld      a, (hl)                 ; group hi
                or      a
                ret     nz
                inc     hl
                ld      a, (hl)                 ; group lo = 0x1d
                cp      &1d
                ret     nz
                inc     hl
                ld      a, (hl)                 ; key_exchange length hi
                or      a
                ret     nz
                inc     hl
                ld      a, (hl)                 ; key_exchange length lo = 0x20 (32)
                cp      &20
                ret     nz
                inc     hl                      ; HL = the 32-byte key
                ld      de, SH_SERVER_PUB
                ld      bc, 32
                ldir
                ld      a, 1
                ld      (sh_found_ks), a
                ret

; sh_check_sv — the supported_versions extension_data at (sh_data) in a ServerHello
; is the single selected version (2 bytes); require TLS 1.3 (0x0304).  Clobbers A,HL.
sh_check_sv:
                ld      hl, (sh_data)
                ld      a, (hl)
                cp      &03
                ret     nz
                inc     hl
                ld      a, (hl)
                cp      &04
                ret     nz
                ld      a, 1
                ld      (sh_found_sv), a
                ret

; ===========================================================================
; tls_walk_server_flight — fold each handshake message in SF_FLIGHT into the
; running transcript, snapshot before the Finished, and capture the Finished.
; Clobbers A, BC, DE, HL, IX + the sha256 scratch.
; ===========================================================================
tls_walk_server_flight:
                xor     a
                ld      (sf_saw_ee), a
                ld      (sf_saw_cert), a
                ld      (sf_saw_cv), a
                ld      (sf_saw_fin), a
                ld      (SF_OK), a
                ld      hl, 0
                ld      (SF_FINISHED_LEN), hl

                ld      hl, SF_FLIGHT           ; sf_end = SF_FLIGHT + SF_FLIGHT_LEN
                ld      de, (SF_FLIGHT_LEN)
                add     hl, de
                ld      (sf_end), hl
                ld      hl, SF_FLIGHT           ; cursor

sf_loop:
                ld      de, (sf_end)            ; done when cursor >= sf_end
                or      a
                push    hl
                sbc     hl, de
                pop     hl
                jp      nc, sf_done             ; jp (not jr): the loop body exceeds jr range

                ld      (sf_msg_start), hl      ; message = type || uint24 len || body
                ld      a, (hl)
                ld      (sf_msg_type), a
                inc     hl                      ; past type
                inc     hl                      ; past length[23:16] (flight < 64KB)
                ld      d, (hl)
                inc     hl                      ; length[15:8]
                ld      e, (hl)
                inc     hl                      ; length[7:0]; HL = body start
                ld      (sf_body_len), de       ; DE = body length

                ; if Finished, snapshot Hash(CH..CertVerify) before folding it
                ld      a, (sf_msg_type)
                cp      20
                jr      nz, sf_after_snap
                ld      hl, SF_HASH_BEFORE_FIN
                call    tls_transcript_snapshot
sf_after_snap:
                ; fold the whole message (4-byte header + body) into the transcript
                ld      hl, (sf_msg_start)
                ld      de, (sf_body_len)
                ld      a, e
                add     a, 4
                ld      c, a
                ld      a, d
                adc     a, 0
                ld      b, a                    ; BC = body_len + 4
                call    tls_transcript_update

                ld      a, (sf_msg_type)        ; per-type bookkeeping
                cp      8
                jr      z, sf_mark_ee
                cp      11
                jr      z, sf_mark_cert
                cp      15
                jr      z, sf_mark_cv
                cp      20
                jr      z, sf_capture_fin
                jr      sf_advance
sf_mark_ee:
                ld      a, 1
                ld      (sf_saw_ee), a
                jr      sf_advance
sf_mark_cert:
                ld      a, 1
                ld      (sf_saw_cert), a
                jr      sf_advance
sf_mark_cv:
                ld      a, 1
                ld      (sf_saw_cv), a
                jr      sf_advance
sf_capture_fin:
                ld      a, 1
                ld      (sf_saw_fin), a
                ld      de, (sf_body_len)
                ld      (SF_FINISHED_LEN), de
                ld      a, d
                or      e
                jr      z, sf_advance           ; zero-length Finished (shouldn't happen)
                ld      hl, (sf_msg_start)      ; body = msg_start + 4
                inc     hl
                inc     hl
                inc     hl
                inc     hl
                ld      bc, (sf_body_len)
                ld      de, SF_FINISHED
                ldir
sf_advance:
                ld      hl, (sf_msg_start)      ; cursor = msg_start + 4 + body_len
                ld      de, (sf_body_len)
                add     hl, de
                ld      de, 4
                add     hl, de
                jp      sf_loop                 ; jp (not jr): back across the loop body

sf_done:
                ld      a, (sf_saw_ee)          ; SF_OK = all four messages seen
                ld      b, a
                ld      a, (sf_saw_cert)
                and     b
                ld      b, a
                ld      a, (sf_saw_cv)
                and     b
                ld      b, a
                ld      a, (sf_saw_fin)
                and     b
                ld      (SF_OK), a
                ret
