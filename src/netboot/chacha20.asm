; chacha20.asm — the ChaCha20 block function (RFC 8439 §2.3), a from-scratch ARX
; port for the i88 TLS cipher suite (ChaCha20-Poly1305).
;
; chacha20_block(key, counter, nonce) produces one 64-byte keystream block: build
; the 16-word state (the 16-byte constant "expand 32-byte k" || 32-byte key ||
; 4-byte counter || 12-byte nonce, all little-endian), run 20 rounds (10
; column+diagonal double-rounds of the quarter-round) on a working copy, add the
; original state word-wise (mod 2^32), and serialise little-endian.  The stream
; cipher (XOR the keystream into the plaintext, incrementing the counter per
; block) is a thin follow-up; this is the verifiable core.
;
; The Z80 has no 32-bit ops, so every word operation is byte-at-a-time: cc_add
; (4-byte ADC chain), cc_xor (4-byte XOR), and the rotates.  The quarter-round's
; four rotates are ROTL by 16, 12, 8, 7; built from byte permutes (ROTL16/ROTL8)
; plus single-bit rotates (ROTL12 = ROTL8 then 4x ROTL1; ROTL7 = ROTL8 then ROTR1).
; All words are little-endian 4-byte blocks (b0 = LSB at the lowest address), so
; the constant is the ASCII "expand 32-byte k" verbatim and the final block is the
; working state copied out as-is.
;
; AUTHORITY / VERIFICATION: host-verifiable in the project's standard way.
; tools/netboot-oracle/z80/chacha20_test.go assembles this file, runs
; chacha20_block under the koron-go/z80 harness, and asserts the 64-byte keystream
; equals the RFC 8439 known-answer vectors (§2.3.2 + Appendix A.1 #1/#2 - distinct
; keys, counters and nonces) byte-for-byte.  There is no Go-side asm authority;
; ChaCha20 is specified by RFC 8439 and the references are its KATs.  NOT
; host-verifiable: nothing here is hardware-gated - pure arithmetic + memory.

                if defined(NETBOOT_STANDALONE)
                org     &8000
                endif

; ===========================================================================
; Inputs + working state + scratch (data first).
; ===========================================================================
CC_KEY:         defs 32                 ; in: the 256-bit key (little-endian words)
CC_COUNTER:     defs 4                  ; in: the 32-bit block counter (little-endian)
CC_NONCE:       defs 12                 ; in: the 96-bit nonce (little-endian words)

CC_STATE:       defs 64                 ; the initial 16-word state
CC_WORK:        defs 64                 ; the working state (rounds mutate this)

cc_tmp4:        defs 4                  ; a scratch word for the byte-permute rotates
cc_pa:          defs 2                  ; the four quarter-round word pointers
cc_pb:          defs 2
cc_pc:          defs 2
cc_pd:          defs 2
cc_round_ctr:   defs 1                  ; the 10-double-round counter (memory, not B)

; chacha20_encrypt (the stream cipher, RFC 8439 §2.4) parameters + state.
CC_MSG_PTR:     defs 2                  ; in: the plaintext/ciphertext input pointer
CC_MSG_LEN:     defs 2                  ; in: the message length in bytes
CC_KS:          defs 64                 ; one keystream block buffer
cc_enc_src:     defs 2                  ; running input pointer
cc_enc_out:     defs 2                  ; running output pointer
cc_enc_rem:     defs 2                  ; bytes still to process
cc_enc_n:       defs 1                  ; this block's byte count (<= 64)

cc_const:       defm "expand 32-byte k" ; the 16 constant bytes (4 LE words)

; ===========================================================================
; chacha20_block — one 64-byte keystream block into (HL).
; In:  HL = output pointer; CC_KEY / CC_COUNTER / CC_NONCE set.
; Out: 64 bytes at the output pointer.  Clobbers A, BC, DE, HL.
; ===========================================================================
chacha20_block:
                push    hl                      ; save the output pointer

                ; --- build CC_STATE = const || key || counter || nonce ---
                ld      hl, cc_const
                ld      de, CC_STATE
                ld      bc, 16
                ldir
                ld      hl, CC_KEY
                ld      bc, 32                  ; DE already at CC_STATE+16
                ldir
                ld      hl, CC_COUNTER
                ld      bc, 4                   ; DE at CC_STATE+48
                ldir
                ld      hl, CC_NONCE
                ld      bc, 12                  ; DE at CC_STATE+52
                ldir

                ; --- working state = a copy of the initial state ---
                ld      hl, CC_STATE
                ld      de, CC_WORK
                ld      bc, 64
                ldir

                ; --- 10 double-rounds ---
                ld      a, 10
                ld      (cc_round_ctr), a
cc_rounds:
                call    cc_double_round
                ld      a, (cc_round_ctr)
                dec     a
                ld      (cc_round_ctr), a
                jr      nz, cc_rounds

                ; --- working state += initial state (16 words, mod 2^32) ---
                ld      hl, CC_WORK
                ld      de, CC_STATE
                ld      b, 16
cc_addstate:
                push    bc
                call    cc_add                  ; (HL) += (DE), preserves HL/DE
                ld      bc, 4
                add     hl, bc                  ; advance HL one word
                ex      de, hl
                add     hl, bc
                ex      de, hl                  ; advance DE one word
                pop     bc
                djnz    cc_addstate

                ; --- serialise: the working state IS the little-endian block ---
                pop     de                      ; the output pointer
                ld      hl, CC_WORK
                ld      bc, 64
                ldir
                ret

; ===========================================================================
; cc_double_round — the 8 quarter-rounds of one ChaCha double-round on CC_WORK:
; 4 column rounds then 4 diagonal rounds (RFC 8439 §2.3.1).  Each sets the four
; word pointers (CC_WORK + index*4) then calls cc_qr.
; ===========================================================================
cc_double_round:
                ; column (0,4,8,12)
                ld      hl, CC_WORK + 0*4
                ld      (cc_pa), hl
                ld      hl, CC_WORK + 4*4
                ld      (cc_pb), hl
                ld      hl, CC_WORK + 8*4
                ld      (cc_pc), hl
                ld      hl, CC_WORK + 12*4
                ld      (cc_pd), hl
                call    cc_qr
                ; column (1,5,9,13)
                ld      hl, CC_WORK + 1*4
                ld      (cc_pa), hl
                ld      hl, CC_WORK + 5*4
                ld      (cc_pb), hl
                ld      hl, CC_WORK + 9*4
                ld      (cc_pc), hl
                ld      hl, CC_WORK + 13*4
                ld      (cc_pd), hl
                call    cc_qr
                ; column (2,6,10,14)
                ld      hl, CC_WORK + 2*4
                ld      (cc_pa), hl
                ld      hl, CC_WORK + 6*4
                ld      (cc_pb), hl
                ld      hl, CC_WORK + 10*4
                ld      (cc_pc), hl
                ld      hl, CC_WORK + 14*4
                ld      (cc_pd), hl
                call    cc_qr
                ; column (3,7,11,15)
                ld      hl, CC_WORK + 3*4
                ld      (cc_pa), hl
                ld      hl, CC_WORK + 7*4
                ld      (cc_pb), hl
                ld      hl, CC_WORK + 11*4
                ld      (cc_pc), hl
                ld      hl, CC_WORK + 15*4
                ld      (cc_pd), hl
                call    cc_qr
                ; diagonal (0,5,10,15)
                ld      hl, CC_WORK + 0*4
                ld      (cc_pa), hl
                ld      hl, CC_WORK + 5*4
                ld      (cc_pb), hl
                ld      hl, CC_WORK + 10*4
                ld      (cc_pc), hl
                ld      hl, CC_WORK + 15*4
                ld      (cc_pd), hl
                call    cc_qr
                ; diagonal (1,6,11,12)
                ld      hl, CC_WORK + 1*4
                ld      (cc_pa), hl
                ld      hl, CC_WORK + 6*4
                ld      (cc_pb), hl
                ld      hl, CC_WORK + 11*4
                ld      (cc_pc), hl
                ld      hl, CC_WORK + 12*4
                ld      (cc_pd), hl
                call    cc_qr
                ; diagonal (2,7,8,13)
                ld      hl, CC_WORK + 2*4
                ld      (cc_pa), hl
                ld      hl, CC_WORK + 7*4
                ld      (cc_pb), hl
                ld      hl, CC_WORK + 8*4
                ld      (cc_pc), hl
                ld      hl, CC_WORK + 13*4
                ld      (cc_pd), hl
                call    cc_qr
                ; diagonal (3,4,9,14)
                ld      hl, CC_WORK + 3*4
                ld      (cc_pa), hl
                ld      hl, CC_WORK + 4*4
                ld      (cc_pb), hl
                ld      hl, CC_WORK + 9*4
                ld      (cc_pc), hl
                ld      hl, CC_WORK + 14*4
                ld      (cc_pd), hl
                jp      cc_qr                   ; tail call (the last QR)

; ===========================================================================
; cc_qr — the ChaCha quarter-round on the words at cc_pa/cc_pb/cc_pc/cc_pd:
;   a += b; d ^= a; d = ROTL16(d);
;   c += d; b ^= c; b = ROTL12(b);
;   a += b; d ^= a; d = ROTL8(d);
;   c += d; b ^= c; b = ROTL7(b);
; Clobbers A, BC, DE, HL.
; ===========================================================================
cc_qr:
                ld      hl, (cc_pa)             ; a += b
                ld      de, (cc_pb)
                call    cc_add
                ld      hl, (cc_pd)             ; d ^= a
                ld      de, (cc_pa)
                call    cc_xor
                ld      hl, (cc_pd)             ; d = ROTL16(d)
                call    cc_rotl16
                ld      hl, (cc_pc)             ; c += d
                ld      de, (cc_pd)
                call    cc_add
                ld      hl, (cc_pb)             ; b ^= c
                ld      de, (cc_pc)
                call    cc_xor
                ld      hl, (cc_pb)             ; b = ROTL12(b)
                call    cc_rotl12
                ld      hl, (cc_pa)             ; a += b
                ld      de, (cc_pb)
                call    cc_add
                ld      hl, (cc_pd)             ; d ^= a
                ld      de, (cc_pa)
                call    cc_xor
                ld      hl, (cc_pd)             ; d = ROTL8(d)
                call    cc_rotl8
                ld      hl, (cc_pc)             ; c += d
                ld      de, (cc_pd)
                call    cc_add
                ld      hl, (cc_pb)             ; b ^= c
                ld      de, (cc_pc)
                call    cc_xor
                ld      hl, (cc_pb)             ; b = ROTL7(b)
                jp      cc_rotl7

; ---------------------------------------------------------------------------
; cc_add — (HL) += (DE), 32-bit little-endian.  HL/DE unchanged on return.
; ---------------------------------------------------------------------------
cc_add:
                push    hl
                push    de
                or      a                       ; clear carry
                ld      b, 4
cc_add_lp:
                ld      a, (de)
                adc     a, (hl)
                ld      (hl), a
                inc     hl
                inc     de
                djnz    cc_add_lp
                pop     de
                pop     hl
                ret

; ---------------------------------------------------------------------------
; cc_xor — (HL) ^= (DE), 4 bytes.  HL/DE unchanged on return.
; ---------------------------------------------------------------------------
cc_xor:
                push    hl
                push    de
                ld      b, 4
cc_xor_lp:
                ld      a, (de)
                xor     (hl)
                ld      (hl), a
                inc     hl
                inc     de
                djnz    cc_xor_lp
                pop     de
                pop     hl
                ret

; ---------------------------------------------------------------------------
; cc_load_tmp — cc_tmp4 = the 4 bytes at HL; HL preserved.
; ---------------------------------------------------------------------------
cc_load_tmp:
                ld      a, (hl)
                ld      (cc_tmp4), a
                inc     hl
                ld      a, (hl)
                ld      (cc_tmp4+1), a
                inc     hl
                ld      a, (hl)
                ld      (cc_tmp4+2), a
                inc     hl
                ld      a, (hl)
                ld      (cc_tmp4+3), a
                dec     hl
                dec     hl
                dec     hl
                ret

; cc_rotl16 — ROTL the LE word at HL by 16 bits: new bytes [b2,b3,b0,b1].
cc_rotl16:
                call    cc_load_tmp
                ld      a, (cc_tmp4+2)
                ld      (hl), a
                inc     hl
                ld      a, (cc_tmp4+3)
                ld      (hl), a
                inc     hl
                ld      a, (cc_tmp4+0)
                ld      (hl), a
                inc     hl
                ld      a, (cc_tmp4+1)
                ld      (hl), a
                dec     hl
                dec     hl
                dec     hl
                ret

; cc_rotl8 — ROTL the LE word at HL by 8 bits: new bytes [b3,b0,b1,b2].
cc_rotl8:
                call    cc_load_tmp
                ld      a, (cc_tmp4+3)
                ld      (hl), a
                inc     hl
                ld      a, (cc_tmp4+0)
                ld      (hl), a
                inc     hl
                ld      a, (cc_tmp4+1)
                ld      (hl), a
                inc     hl
                ld      a, (cc_tmp4+2)
                ld      (hl), a
                dec     hl
                dec     hl
                dec     hl
                ret

; cc_rotl1 — ROTL the LE word at HL by 1 bit.  HL preserved.
cc_rotl1:
                push    hl
                inc     hl
                inc     hl
                inc     hl                      ; -> b3 (MSB)
                ld      a, (hl)
                add     a, a                    ; CF = old MSB bit7
                pop     hl
                push    hl
                rl      (hl)                    ; b0
                inc     hl
                rl      (hl)                    ; b1
                inc     hl
                rl      (hl)                    ; b2
                inc     hl
                rl      (hl)                    ; b3
                pop     hl
                ret

; cc_rotr1 — ROTR the LE word at HL by 1 bit.  HL preserved.
cc_rotr1:
                ld      a, (hl)
                rrca                            ; CF = old b0 bit0
                push    hl
                inc     hl
                inc     hl
                inc     hl                      ; -> b3
                rr      (hl)                    ; b3
                dec     hl
                rr      (hl)                    ; b2
                dec     hl
                rr      (hl)                    ; b1
                dec     hl
                rr      (hl)                    ; b0
                pop     hl
                ret

; cc_rotl12 — ROTL by 12 = ROTL8 then ROTL4 (4 single-bit ROTLs).
cc_rotl12:
                call    cc_rotl8
                call    cc_rotl1
                call    cc_rotl1
                call    cc_rotl1
                jp      cc_rotl1

; cc_rotl7 — ROTL by 7 = ROTL8 then ROTR1.
cc_rotl7:
                call    cc_rotl8
                jp      cc_rotr1

; ===========================================================================
; chacha20_encrypt — the ChaCha20 stream cipher (RFC 8439 §2.4): XOR the message
; with the keystream, one 64-byte block at a time, incrementing the block counter
; per block (so this both encrypts and decrypts).
; In:  HL = output pointer; CC_MSG_PTR / CC_MSG_LEN / CC_KEY / CC_COUNTER /
;      CC_NONCE set (CC_COUNTER is the starting block counter).
; Out: CC_MSG_LEN bytes at the output pointer; CC_COUNTER advanced past the last
;      block.  Clobbers A, BC, DE, HL, IX.
; ===========================================================================
chacha20_encrypt:
                ld      (cc_enc_out), hl
                ld      hl, (CC_MSG_PTR)
                ld      (cc_enc_src), hl
                ld      hl, (CC_MSG_LEN)
                ld      (cc_enc_rem), hl
cc_enc_loop:
                ld      hl, (cc_enc_rem)
                ld      a, h
                or      l
                ret     z                       ; all bytes processed

                ; keystream block for the current counter.
                ld      hl, CC_KS
                call    chacha20_block

                ; n = min(64, remaining).
                ld      hl, (cc_enc_rem)
                ld      a, h
                or      a
                jr      nz, cc_enc_n64          ; remaining >= 256 -> full block
                ld      a, l
                cp      64
                jr      c, cc_enc_have_n        ; remaining < 64 -> partial final block
cc_enc_n64:
                ld      a, 64
cc_enc_have_n:
                ld      (cc_enc_n), a

                ; XOR n bytes: out[i] = src[i] ^ keystream[i].
                ld      b, a                    ; B = n (<= 64)
                ld      hl, (cc_enc_src)        ; HL = source
                ld      de, CC_KS               ; DE = keystream
                ld      ix, (cc_enc_out)        ; IX = output
cc_enc_xor:
                ld      a, (de)
                xor     (hl)
                ld      (ix+0), a
                inc     hl
                inc     de
                inc     ix
                djnz    cc_enc_xor

                ; advance src/out by n, remaining -= n.
                ld      a, (cc_enc_n)
                ld      c, a
                ld      b, 0                    ; BC = n
                ld      hl, (cc_enc_src)
                add     hl, bc
                ld      (cc_enc_src), hl
                ld      hl, (cc_enc_out)
                add     hl, bc
                ld      (cc_enc_out), hl
                ld      hl, (cc_enc_rem)
                or      a
                sbc     hl, bc
                ld      (cc_enc_rem), hl

                ; next block uses the next counter.
                call    cc_counter_inc
                jr      cc_enc_loop

; cc_counter_inc — CC_COUNTER += 1 (32-bit little-endian).  Clobbers A, HL.
cc_counter_inc:
                ld      hl, CC_COUNTER
                inc     (hl)
                ret     nz
                inc     hl
                inc     (hl)
                ret     nz
                inc     hl
                inc     (hl)
                ret     nz
                inc     hl
                inc     (hl)
                ret
