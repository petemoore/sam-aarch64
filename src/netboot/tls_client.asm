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

TC_STATUS_CONTINUE: equ 0        ; mirror the Go Status enum (client.go:48-54)
TC_STATUS_DONE:     equ 1

; The composite has ONE sha256 instance: the running transcript hash lives in the
; shared SHA_H..SHA_BLOCK region (TT_STATE_LEN = 105 bytes).  But the key-schedule
; and hmac_sha256 calls below ALSO use SHA_*, clobbering the transcript's running
; state.  The Go authority uses separate hash.Hash objects so it never collides;
; the Z80 manages it explicitly — save SHA_H..SHA_BLOCK here around every sha256/hmac
; user OUTSIDE the transcript, and around the idempotent (per-record) flight walk.
TC_TRANSCRIPT_SAVE: defs 105     ; saved transcript SHA state (= TT_STATE_LEN)
TC_SRV_FIN_CALC:    defs 32      ; recomputed server Finished verify_data (for the compare)
TC_CLIENT_VERIFY:   defs 32      ; the client Finished verify_data
TC_FIN_MSG:         defs 36      ; the client Finished handshake message (14 00 00 20 || verify)

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

; ===========================================================================
; tls_client_on_record — mirror Client.OnRecord (client.go:141).  Feed one inbound
; TLS record (header + payload) in TC_RX / TC_RX_LEN; dispatch on the record type
; (TC_RX[0]) and update TC_TX / TC_TX_LEN / TC_STATUS / TC_PHASE.  Require >= 5 bytes.
;   0x14 ChangeCipherSpec -> ignore: TC_TX_LEN=0, TC_STATUS=CONTINUE.
;   0x15 Alert            -> TC_PHASE=ERROR (the host test treats it as failure).
;   0x16 plaintext handshake (ServerHello)        -> tls_client_on_server_hello.
;   0x17 application_data (encrypted flight)       -> tls_client_on_encrypted.
;   any other type        -> TC_PHASE=ERROR.
; Clobbers everything the sub-handlers do.
; ===========================================================================
tls_client_on_record:
                ; require len(rx) >= 5 (client.go:142)
                ld      hl, (TC_RX_LEN)
                ld      de, 5
                or      a
                sbc     hl, de
                jr      c, tcor_fail            ; TC_RX_LEN < 5 -> short record

                ld      a, (TC_RX)              ; dispatch on rx[0]
                cp      &14
                jr      z, tcor_ccs
                cp      &15
                jr      z, tcor_fail            ; alert
                cp      &16
                jp      z, tls_client_on_server_hello
                cp      &17
                jp      z, tls_client_on_encrypted
                ; default: unexpected record type
tcor_fail:
                ld      a, TC_PHASE_ERROR       ; mirror Client.fail (client.go:159)
                ld      (TC_PHASE), a
                ret
tcor_ccs:
                ; ChangeCipherSpec — TLS 1.3 middlebox compat; ignored (client.go:146).
                ld      hl, 0
                ld      (TC_TX_LEN), hl
                ld      a, TC_STATUS_CONTINUE
                ld      (TC_STATUS), a
                ret

; ===========================================================================
; tls_client_on_server_hello — port of Client.onServerHello (client.go:166).
; Require TC_PHASE=SENT_CH.  Copy TC_RX[5:] -> SH_MSG, parse it, fold it into the
; transcript, snapshot hashHS, compute the ECDHE, and derive the handshake secrets.
; Out: TC_PHASE=GOT_SH, TC_TX_LEN=0, TC_STATUS=CONTINUE.  Clobbers everything.
; ===========================================================================
tls_client_on_server_hello:
                ld      a, (TC_PHASE)           ; require PhaseSentCH (client.go:167)
                cp      TC_PHASE_SENT_CH
                jr      nz, tcor_fail

                ; SH_MSG = TC_RX[5 : TC_RX_LEN]  (the handshake message, sans record header)
                ld      hl, (TC_RX_LEN)
                ld      de, 5
                or      a
                sbc     hl, de                  ; HL = SH length = TC_RX_LEN - 5
                ld      b, h
                ld      c, l                    ; BC = SH length
                ld      hl, TC_RX + 5
                ld      de, SH_MSG
                ldir

                call    tls_parse_server_hello  ; -> SH_OK, SH_SERVER_PUB (client.go:170)
                ld      a, (SH_OK)
                or      a
                jr      z, tcosh_fail           ; ServerHello rejected

                ; transcript.Write(sh) (client.go:175): fold the ServerHello message
                ; (SH length = TC_RX_LEN - 5).
                ld      hl, (TC_RX_LEN)
                ld      de, 5
                or      a
                sbc     hl, de
                ld      b, h
                ld      c, l
                ld      hl, SH_MSG
                call    tls_transcript_update

                ; hashHS = transcript.Sum(nil) -> KS_HASH_HS (client.go:176)
                ld      hl, KS_HASH_HS
                call    tls_transcript_snapshot

                ; ECDHE: X25519(client scalar, server pub) (client.go:182)
                ld      hl, TC_CLIENT_PRIV
                ld      de, X25519_K
                ld      bc, 32
                ldir
                ld      hl, SH_SERVER_PUB
                ld      de, X25519_U
                ld      bc, 32
                ldir
                call    x25519                  ; X25519_OUT = the shared secret
                ld      hl, X25519_OUT
                ld      de, KS_ECDHE
                ld      bc, 32
                ldir

                ; DeriveHandshake (client.go:186): the key schedule uses sha256/hmac,
                ; which clobbers the running transcript — save SHA_* around it.
                call    tc_save_transcript
                call    tls_key_schedule_handshake
                call    tc_restore_transcript

                ld      a, TC_PHASE_GOT_SH      ; c.phase = PhaseGotSH (client.go:187)
                ld      (TC_PHASE), a
                ld      hl, 0
                ld      (TC_TX_LEN), hl
                ld      a, TC_STATUS_CONTINUE
                ld      (TC_STATUS), a
                ret
tcosh_fail:
                ld      a, TC_PHASE_ERROR
                ld      (TC_PHASE), a
                ret

; ===========================================================================
; tls_client_on_encrypted — port of Client.onEncrypted (client.go:194).  Require
; TC_PHASE=GOT_SH.  Decrypt one server handshake-flight record, accumulate the
; plaintext into SF_FLIGHT, and — once the flight is complete — verify the server
; Finished, derive the application secrets, and produce + seal the client Finished.
; Clobbers everything.
; ===========================================================================
tls_client_on_encrypted:
                ld      a, (TC_PHASE)           ; require PhaseGotSH (client.go:195)
                cp      TC_PHASE_GOT_SH
                jp      nz, tcor_fail

                ; Open(SHSKey, SHSIV, serverSeq, record) (client.go:198): decrypt the
                ; record in TC_RX under the server handshake key at TC_SERVER_SEQ.
                ld      hl, KS_SHS_KEY
                ld      de, TR_KEY
                ld      bc, 32
                ldir
                ld      hl, KS_SHS_IV
                ld      de, TR_IV
                ld      bc, 12
                ldir
                ld      hl, TC_SERVER_SEQ
                ld      de, TR_SEQ
                ld      bc, 8
                ldir
                ld      hl, TC_RX
                ld      de, TR_RECORD
                ld      bc, (TC_RX_LEN)
                ldir
                ld      hl, (TC_RX_LEN)
                ld      (TR_RECORD_LEN), hl
                call    tls_record_open         ; -> TR_OK, TR_TYPE, TR_CONTENT/_LEN

                ld      a, (TR_OK)
                or      a
                jp      z, tcor_fail            ; failed to decrypt (client.go:199)

                call    tc_inc_server_seq       ; c.serverSeq++ (client.go:202)

                ld      a, (TR_TYPE)            ; inner type must be handshake(0x16) (client.go:203)
                cp      &16
                jp      nz, tcor_fail

                ; c.flight = append(c.flight, content...) (client.go:206): append the
                ; decrypted content at SF_FLIGHT + SF_FLIGHT_LEN.
                ld      bc, (TR_CONTENT_LEN)
                ld      a, b
                or      c
                jr      z, tce_appended         ; empty content
                ld      hl, SF_FLIGHT
                ld      de, (SF_FLIGHT_LEN)
                add     hl, de                  ; HL = SF_FLIGHT + SF_FLIGHT_LEN
                ex      de, hl                  ; DE = append dest
                ld      hl, TR_CONTENT
                ld      bc, (TR_CONTENT_LEN)
                ldir
                ld      hl, (SF_FLIGHT_LEN)     ; SF_FLIGHT_LEN += TR_CONTENT_LEN
                ld      bc, (TR_CONTENT_LEN)
                add     hl, bc
                ld      (SF_FLIGHT_LEN), hl
tce_appended:
                ; WalkServerFlight(c.flight) (client.go:208).  The Z80 walk folds the
                ; flight into the running transcript and snapshots SF_HASH_BEFORE_FIN;
                ; called once per record, it must be idempotent w.r.t. the transcript,
                ; so save SHA_* before and restore on an incomplete flight.
                call    tc_save_transcript
                call    tls_walk_server_flight  ; -> SF_OK, SF_FINISHED, SF_HASH_BEFORE_FIN

                ld      a, (SF_OK)
                or      a
                jr      nz, tce_complete
                ; flight not complete (client.go:212): undo the partial fold, ask for more.
                call    tc_restore_transcript
                ld      hl, 0
                ld      (TC_TX_LEN), hl
                ld      a, TC_STATUS_CONTINUE
                ld      (TC_STATUS), a
                ret

tce_complete:
                ; The walk left the running transcript = Hash(CH..serverFinished); the
                ; Go folds BeforeFin then Finished (client.go:217,222) reaching the same
                ; state.  hashAP = transcript.Sum(nil) (client.go:226).
                ld      hl, KS_HASH_AP
                call    tls_transcript_snapshot

                ; Verify the server Finished (client.go:219): hmacSHA256(SHSFin,
                ; hashBeforeFin) == sf.VerifyData.  hashBeforeFin = SF_HASH_BEFORE_FIN
                ; (the walk snapshotted Hash(CH..CertVerify)).  hmac clobbers SHA_* —
                ; save the running transcript around it.
                call    tc_save_transcript
                ld      hl, KS_SHS_FIN
                ld      (HMAC_KEY_PTR), hl
                ld      hl, 32
                ld      (HMAC_KEY_LEN), hl
                ld      hl, SF_HASH_BEFORE_FIN
                ld      (HMAC_MSG_PTR), hl
                ld      hl, 32
                ld      (HMAC_MSG_LEN), hl
                ld      hl, TC_SRV_FIN_CALC
                call    hmac_sha256
                ; compare TC_SRV_FIN_CALC[0..31] == SF_FINISHED[0..31]
                ld      hl, TC_SRV_FIN_CALC
                ld      de, SF_FINISHED
                ld      b, 32
tce_fin_cmp:
                ld      a, (de)
                cp      (hl)
                jp      nz, tcosh_fail          ; mismatch -> ERROR (client.go:220)
                inc     hl
                inc     de
                djnz    tce_fin_cmp

                ; DeriveApplication(hashAP) (client.go:227) — clobbers SHA_* (saved).
                call    tls_key_schedule_application

                ; clientVerify = hmacSHA256(CHSFin, hashAP) (client.go:228) — clobbers SHA_*.
                ld      hl, KS_CHS_FIN
                ld      (HMAC_KEY_PTR), hl
                ld      hl, 32
                ld      (HMAC_KEY_LEN), hl
                ld      hl, KS_HASH_AP
                ld      (HMAC_MSG_PTR), hl
                ld      hl, 32
                ld      (HMAC_MSG_LEN), hl
                ld      hl, TC_CLIENT_VERIFY
                call    hmac_sha256

                ; restore the running transcript (CH..serverFinished) so the client
                ; Finished can be folded in (client.go:230), mirroring the authority.
                call    tc_restore_transcript

                ; finMsg = 14 00 00 20 || clientVerify (client.go:229): 36 bytes.
                ld      hl, TC_FIN_MSG
                ld      (hl), &14               ; handshake type = finished(20)
                inc     hl
                ld      (hl), 0                 ; length[23:16]
                inc     hl
                ld      (hl), 0                 ; length[15:8]
                inc     hl
                ld      (hl), &20               ; length[7:0] = 32
                inc     hl
                ld      de, TC_CLIENT_VERIFY    ; HL = TC_FIN_MSG + 4
                ex      de, hl
                ld      bc, 32
                ldir                            ; TC_FIN_MSG[4..35] = clientVerify

                ; transcript.Write(finMsg) (client.go:230).
                ld      hl, TC_FIN_MSG
                ld      bc, 36
                call    tls_transcript_update

                ; Seal(CHSKey, CHSIV, 0, 0x16, finMsg) (client.go:231): the client
                ; Finished record at client seq 0.  AEAD (chacha20+poly1305) uses no
                ; SHA, so the transcript is undisturbed.
                ld      hl, KS_CHS_KEY
                ld      de, TR_KEY
                ld      bc, 32
                ldir
                ld      hl, KS_CHS_IV
                ld      de, TR_IV
                ld      bc, 12
                ldir
                xor     a                       ; TR_SEQ = 0 (8 bytes)
                ld      hl, TR_SEQ
                ld      (hl), a
                ld      d, h
                ld      e, l
                inc     de
                ld      bc, 7
                ldir
                ld      a, &16                  ; TR_TYPE = handshake
                ld      (TR_TYPE), a
                ld      hl, TC_FIN_MSG
                ld      de, TR_CONTENT
                ld      bc, 36
                ldir
                ld      hl, 36
                ld      (TR_CONTENT_LEN), hl
                call    tls_record_seal         ; -> TR_RECORD / TR_RECORD_LEN

                ; TC_TX / TC_TX_LEN = the sealed client Finished record.
                ld      hl, (TR_RECORD_LEN)
                ld      (TC_TX_LEN), hl
                ld      b, h
                ld      c, l
                ld      hl, TR_RECORD
                ld      de, TC_TX
                ldir

                ld      a, TC_PHASE_DONE        ; c.phase = PhaseDone (client.go:232)
                ld      (TC_PHASE), a
                ld      a, TC_STATUS_DONE       ; StatusDone (client.go:233)
                ld      (TC_STATUS), a
                ret

; ---------------------------------------------------------------------------
; tc_save_transcript / tc_restore_transcript — copy the 105-byte running SHA-256
; transcript state (SHA_H..SHA_BLOCK) to/from TC_TRANSCRIPT_SAVE.  Used to bracket
; every sha256/hmac user OUTSIDE the transcript (the key schedule + the Finished
; HMACs) and the per-record flight walk, so the running transcript survives them.
; Clobbers BC, DE, HL.
; ---------------------------------------------------------------------------
tc_save_transcript:
                ld      hl, SHA_H
                ld      de, TC_TRANSCRIPT_SAVE
                ld      bc, TT_STATE_LEN
                ldir
                ret
tc_restore_transcript:
                ld      hl, TC_TRANSCRIPT_SAVE
                ld      de, SHA_H
                ld      bc, TT_STATE_LEN
                ldir
                ret

; ---------------------------------------------------------------------------
; tc_inc_server_seq — TC_SERVER_SEQ += 1 (a big-endian 64-bit counter, LSB last).
; Clobbers A, HL, B.
; ---------------------------------------------------------------------------
tc_inc_server_seq:
                ld      hl, TC_SERVER_SEQ + 7   ; least-significant byte (big-endian)
                ld      b, 8
tcis_loop:
                inc     (hl)
                jr      nz, tcis_done           ; no carry out of this byte
                dec     hl
                djnz    tcis_loop
tcis_done:
                ret

; tls_client_end marks the top of the emitted image.  The host test asserts
; tls_client_end < qsq_table (&FB00 under NETBOOT_TLS_CLIENT): the regenerable
; multiply table must sit above the image, not inside a live buffer.
tls_client_end:
