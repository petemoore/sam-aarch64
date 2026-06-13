; tls_client.asm — the i88 brick-6 TLS 1.3 client, sub-brick 6a: the record-level
; handshake state machine.  A faithful port of the Go authority tls.Client
; (tools/netboot-oracle/tls/client.go) — CLAUDE.md §6: Go is the authority, the Z80
; is a mechanical port.  This file COMPOSES the five landed handshake bricks plus
; x25519 into one binary and adds the client driver on top.
;
; This increment lands the composition + the ClientHello sub-brick:
;   tls_client_init  — mirror NewClientDeterministic + the X25519 pubkey step.
;   tls_client_first — mirror Client.First (build ClientHello, fold the transcript,
;                      emit the plaintext handshake record).
; The record-driven state machine (tls_client_on_record, mirroring Client.OnRecord)
; is the next increment.  TC_* below lays out the full 6a state block now so that
; increment does not reshuffle the host-test ABI.
;
; AUTHORITY / VERIFICATION: host-verified by tls_client_test.go (capture-then-
; replay against the Go authority): drive NewClientDeterministic against
; crypto/tls.Server, capture the client scalar/random/sid + the ClientHello record,
; inject them here, and assert CH_PUBKEY == the captured key_share and
; TC_TX[:TC_TX_LEN] == the captured ClientHello record, byte-for-byte.
;
; Built with -D NETBOOT_TLS_CLIENT=1: this file sets the org once; every included
; leaf keys its own org guard off a DIFFERENT flag (NETBOOT_TLS_KS / _TLS_RECORD /
; _AEAD / _STANDALONE / _TLS_SF / _TLS_TRANSCRIPT), all inert here.  The flag also
; dedups the two cross-brick collisions the composition exposes (port plan Part 0):
; sha256 (suppressed in tls_transcript.asm, it arrives via the key-schedule chain)
; and the qsq quarter-square multiply (suppressed in x25519.asm, it arrives via
; tls_record -> aead -> poly1305).  pyz80 is 2-pass, so forward references across
; the includes resolve regardless of order.

                if defined(NETBOOT_TLS_CLIENT)
                org     &8000
                endif

                include "tls_keyschedule.asm"   ; brick 1 (+ hkdf -> hmac -> sha256, the sole sha256)
                include "tls_record.asm"         ; brick 2 (+ aead -> chacha20 + poly1305 -> qsq, the sole qsq)
                include "tls_client_hello.asm"   ; brick 3 (CH_* + tls_build_client_hello)
                include "tls_server_flight.asm"  ; brick 4 (+ tls_transcript; its sha256 suppressed)
                include "x25519.asm"             ; ECDHE + client pubkey (its qsq suppressed)

; ===========================================================================
; 6a client state + buffers (the data block the host test reads/writes).  The
; handshake bricks own their own buffers (KS_*, TR_*, CH_*, SH_*, SF_*, X25519_*,
; the tls_transcript SHA state); these are 6a's additions.
; ===========================================================================
TC_PHASE_INIT:    equ 0          ; mirror the Go Phase enum (client.go:35-43)
TC_PHASE_SENT_CH: equ 1
TC_PHASE_GOT_SH:  equ 2
TC_PHASE_DONE:    equ 3
TC_PHASE_ERROR:   equ 4

TC_CLIENT_PRIV: defs 32          ; injected X25519 scalar (host test); real HW: q19
TC_PHASE:       defs 1           ; current handshake phase (TC_PHASE_*)
TC_SERVER_SEQ:  defs 8           ; server handshake-record seq (big-endian), reset 0 at key change
TC_FLIGHT_OFF:  defs 2           ; bytes of SF_FLIGHT already folded into the transcript
TC_RX:          defs 1056        ; one inbound TLS record (header + payload), caller-filled
TC_RX_LEN:      defs 2
TC_TX:          defs 1056        ; one outbound TLS record (the CH record / client Finished)
TC_TX_LEN:      defs 2
TC_STATUS:      defs 1           ; 0=CONTINUE 1=DONE (mirror Go Status)

; ===========================================================================
; tls_client_init — mirror NewClientDeterministic + the pubkey derivation
; (client.go:101 + First's pub = priv.PublicKey()).  Inputs (caller-filled):
; TC_CLIENT_PRIV (the raw scalar), CH_RANDOM, CH_SESSION_ID, CH_HOSTNAME/_LEN.
; Computes CH_PUBKEY = X25519(scalar, basepoint), inits the transcript, and zeroes
; the run state.  Clobbers everything (x25519 is a full field machine).
; ===========================================================================
tls_client_init:
                ; X25519_K = the client scalar
                ld      hl, TC_CLIENT_PRIV
                ld      de, X25519_K
                ld      bc, 32
                ldir

                ; X25519_U = the RFC 7748 base point: u = 9 (byte0=9, bytes1..31=0)
                ld      hl, X25519_U
                ld      (hl), 0
                ld      d, h
                ld      e, l
                inc     de
                ld      bc, 31
                ldir                            ; zero all 32 bytes
                ld      a, 9
                ld      (X25519_U), a           ; u[0] = 9

                call    x25519                  ; X25519_OUT = the client public key

                ; CH_PUBKEY = the public key (the ClientHello key_share)
                ld      hl, X25519_OUT
                ld      de, CH_PUBKEY
                ld      bc, 32
                ldir

                call    tls_transcript_init     ; start the running handshake hash

                ; run state: phase=INIT, server seq=0, flight empty
                xor     a
                ld      (TC_PHASE), a
                ld      hl, TC_SERVER_SEQ
                ld      (hl), 0
                ld      d, h
                ld      e, l
                inc     de
                ld      bc, 7
                ldir                            ; TC_SERVER_SEQ[0..7] = 0
                ld      hl, 0
                ld      (SF_FLIGHT_LEN), hl
                ld      (TC_FLIGHT_OFF), hl
                ret

; ===========================================================================
; tls_client_first — mirror Client.First (client.go:130).  Build the ClientHello
; handshake message, fold it into the transcript, and emit the plaintext handshake
; record (0x16 0x03 0x01 len16 || ClientHello) into TC_TX.  Advances to SENT_CH.
; In: tls_client_init already run (CH_PUBKEY set) + CH_RANDOM/CH_SESSION_ID/
; CH_HOSTNAME/_LEN filled.  Out: TC_TX / TC_TX_LEN = the ClientHello record.
; ===========================================================================
tls_client_first:
                call    tls_build_client_hello  ; -> CH_MSG / CH_MSG_LEN

                ld      hl, CH_MSG              ; transcript_update(CH_MSG, CH_MSG_LEN)
                ld      bc, (CH_MSG_LEN)
                call    tls_transcript_update

                ; record header: 0x16 (handshake) 0x03 0x01 (legacy version) len16 BE
                ld      hl, TC_TX
                ld      (hl), &16
                inc     hl
                ld      (hl), &03
                inc     hl
                ld      (hl), &01
                inc     hl
                ld      bc, (CH_MSG_LEN)        ; B = len hi, C = len lo
                ld      (hl), b                 ; length, big-endian
                inc     hl
                ld      (hl), c
                inc     hl

                ex      de, hl                  ; DE = TC_TX + 5 (record body dest)
                ld      hl, CH_MSG
                ld      bc, (CH_MSG_LEN)
                ldir                            ; record body = the ClientHello message

                ld      hl, (CH_MSG_LEN)        ; TC_TX_LEN = 5 + CH_MSG_LEN
                ld      bc, 5
                add     hl, bc
                ld      (TC_TX_LEN), hl

                ld      a, TC_PHASE_SENT_CH
                ld      (TC_PHASE), a
                ret

; tls_client_end marks the top of the emitted image.  The host test asserts
; tls_client_end < qsq_table (&FB00 under NETBOOT_TLS_CLIENT): the regenerable
; multiply table must sit above the image, not inside a live buffer.
tls_client_end:
