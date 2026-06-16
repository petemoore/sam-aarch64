; tls_transcript.asm — the TLS 1.3 handshake transcript hash (RFC 8446 §4.4.1),
; handshake brick 5 (docs/specs/tls13-handshake.md).  The transcript hash is the
; running SHA-256 over the concatenated handshake messages (each the 4-byte-header
; handshake struct, no record framing); Derive-Secret takes a *snapshot* of it at
; specific points (after ServerHello, after the server Finished, ...) as the
; key-schedule context, while the stream keeps growing for later snapshots.
;
; This is a thin wrapper over the verified sha256.asm streaming hash:
;   tls_transcript_init      = sha256_init
;   tls_transcript_update    = sha256_update (HL = msg ptr, BC = msg len)
;   tls_transcript_snapshot  = SHA-256(messages so far) -> (HL), WITHOUT ending the
;                              stream: save the SHA-256 state, sha256_final into the
;                              output (which consumes the state by padding), then
;                              restore the state so subsequent updates continue.
; The persistent SHA-256 state is the contiguous SHA_H..SHA_BLOCK region (the H
; words, the 64-bit bit length, the partial-block fill count, and the block
; buffer); SHA_W and the round scratch are transient (recomputed per compression).
;
; Built with -D NETBOOT_TLS_TRANSCRIPT (NOT NETBOOT_STANDALONE), so sha256.asm's
; org guard stays inert and this file sets the org once (the aead.asm idiom).
;
; AUTHORITY / VERIFICATION: host-verifiable in the project's standard way.
; tools/netboot-oracle/z80/tls_transcript_test.go assembles this file, feeds
; message sequences with interleaved snapshots under the koron-go/z80 harness, and
; asserts each snapshot equals Go crypto/sha256 of the concatenation so far —
; including a snapshot of the empty transcript and snapshots straddling the 64-byte
; block boundary (which exercise the save/restore). NOT host-verifiable: nothing
; here is hardware-gated.

                if defined(NETBOOT_TLS_TRANSCRIPT)
                org     &8000
                endif

                if defined(NETBOOT_TLS_CLIENT)
                else                           ; standalone / server-flight build:
                include "sha256.asm"           ; sha256_init/update/final + SHA_H..SHA_BLOCK
                endif                          ; in the 6a composite sha256 arrives via
                                               ; the key-schedule chain (included first)

; The SHA-256 persistent state is SHA_H(32) || SHA_BITLEN(8) || SHA_FILL(1) ||
; SHA_BLOCK(64) = 105 contiguous bytes starting at SHA_H.
TT_STATE_LEN:   equ 105
TT_SAVE:        defs TT_STATE_LEN       ; saved state across a snapshot's sha256_final

; ===========================================================================
; tls_transcript_init — start a fresh transcript.  Clobbers what sha256_init does.
; ===========================================================================
tls_transcript_init:
                jp      sha256_init

; ===========================================================================
; tls_transcript_update — fold a handshake message into the transcript.
; In: HL = message pointer, BC = message length.  Clobbers sha256_update's set.
; ===========================================================================
tls_transcript_update:
                jp      sha256_update

; ===========================================================================
; tls_transcript_snapshot — SHA-256(messages so far) -> (HL), leaving the running
; transcript intact for later updates/snapshots.
; In: HL = 32-byte output pointer.  Clobbers A, BC, DE, HL + sha256 scratch.
; ===========================================================================
tls_transcript_snapshot:
                push    hl                      ; save the output pointer
                ld      hl, SHA_H              ; save the live state
                ld      de, TT_SAVE
                ld      bc, TT_STATE_LEN
                ldir
                pop     hl                      ; output pointer
                call    sha256_final           ; digest -> (HL); consumes the state
                ld      hl, TT_SAVE            ; restore the live state
                ld      de, SHA_H
                ld      bc, TT_STATE_LEN
                ldir
                ret
