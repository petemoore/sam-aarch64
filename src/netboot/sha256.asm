; sha256.asm — SHA-256 (FIPS 180-4) with a streaming init/update/final API.
;
; The verify primitive for the i70/i100 firmware-download path (q15 option c):
; the SAM fetches firmware over plain HTTP from cdn.githubraw.com and verifies
; each file against a pinned SHA-256 hash. Because a real firmware blob is
; multi-MB and cannot live in RAM (see i99), the API is STREAMING — init once,
; update with each chunk as it arrives, final to read the digest — so the hash
; is computed incrementally as the body streams to storage. Also a building
; block for the i88 TLS work later (its ARX cipher suite uses SHA-256).
;
; AUTHORITY / VERIFICATION: host-verifiable in the project's standard way.
; tools/netboot-oracle/z80/sha256_test.go assembles this file, runs it under the
; koron-go/z80 harness, feeds the FIPS 180-4 / NIST test vectors (the empty
; string, "abc", the 56-byte two-block vector, the 1,000,000-'a' long vector,
; and the 56/64-byte padding-boundary cases) — both whole AND fed in awkward
; chunks to exercise the partial-block carry — and asserts the 32-byte digest
; equals Go's crypto/sha256.Sum256 of the same input, byte-for-byte. There is no
; Go-side asm authority to port here; SHA-256 is specified by FIPS 180-4 and the
; reference is Go's crypto/sha256.
;
; ALL 32-bit values are stored BIG-ENDIAN (4 bytes, MSB first). That matches the
; FIPS spec's word/byte order, makes the K table a plain big-endian defb list,
; and means the final digest is just the 32 state bytes copied out verbatim. The
; Z80 has no 32-bit ops, so every word operation (rotate-right, shift-right, xor,
; and, not, add-mod-2^32) is done byte-at-a-time by small helpers below.

                if defined(NETBOOT_STANDALONE)
                org     &8000
                endif

; ===========================================================================
; Inline-able macros for the hot 4-byte primitives. Defined as pyz80 MACROs so
; the hot call sites can emit the body directly and elide the call/ret (27T)
; per invocation — sha_add4 alone runs ~648x per compression. Each macro body
; carries NO internal label (the byte loop is fully unrolled) so it can expand
; any number of times.
; ===========================================================================

; add4_body — 32-bit add mod 2^32: (HL word) += (DE word), big-endian, carry
; from LSB (byte 3) up to MSB (byte 0). In: HL=dst+3, DE=src+3 (both -> the LSB,
; the HIGH byte address of the big-endian word). Out: dst += src. Clobbers A, DE,
; HL (HL ends at dst+0, DE at src+0). Callers pass `label+3` (free for a literal
; label) so the macro skips any LSB-positioning.
add4_body:      MACRO
                or      a                       ; clear carry
                ld      a, (de)
                adc     a, (hl)
                ld      (hl), a                 ; byte3 (LSB)
                dec     hl
                dec     de
                ld      a, (de)
                adc     a, (hl)
                ld      (hl), a                 ; byte2
                dec     hl
                dec     de
                ld      a, (de)
                adc     a, (hl)
                ld      (hl), a                 ; byte1
                dec     hl
                dec     de
                ld      a, (de)
                adc     a, (hl)
                ld      (hl), a                 ; byte0 (MSB)
ENDM

; copy4_body — copy a 4-byte word. In: HL=src, DE=dst. Out: HL=src+4, DE=dst+4.
; Clobbers A, DE, HL.
copy4_body:     MACRO
                ld      a, (hl)
                ld      (de), a
                inc     hl
                inc     de
                ld      a, (hl)
                ld      (de), a
                inc     hl
                inc     de
                ld      a, (hl)
                ld      (de), a
                inc     hl
                inc     de
                ld      a, (hl)
                ld      (de), a
                inc     hl
                inc     de
ENDM

; ===========================================================================
; Register-resident 32-bit rotate/shift macros. A word lives in B,C,D,E with
; B = byte0 (MSB, bits 31..24) ... E = byte3 (LSB, bits 7..0) — big-endian, the
; same byte order as memory. These keep the rotate in 8T register ops (vs 15T
; `rr (hl)` on memory), and whole-byte rotates are free register relabelings
; done by the caller (it just chooses which register is which byte). Each body
; has NO internal label so it inlines anywhere.
; ===========================================================================

; rotr1_bcde — rotate B,C,D,E right by 1 bit. The LSB (E bit0) wraps into the
; MSB (B bit7). Clobbers A and the flags. 32T (4x rr r) + 8T (seed carry).
rotr1_bcde:     MACRO
                ld      a, e
                rrca                            ; carry := E bit0 (the wrap bit)
                rr      b                        ; B: carry(wrap) -> bit7, B bit0 -> carry
                rr      c
                rr      d
                rr      e
ENDM

; rotl1_bcde — rotate B,C,D,E left by 1 bit. The MSB (B bit7) wraps into the LSB
; (E bit0). Clobbers A and the flags.
rotl1_bcde:     MACRO
                ld      a, b
                rlca                            ; carry := B bit7 (the wrap bit)
                rl      e                        ; E: carry(wrap) -> bit0, E bit7 -> carry
                rl      d
                rl      c
                rl      b
ENDM

; shr1_bcde — logical shift B,C,D,E right by 1, feeding 0 into the MSB. Clobbers
; flags (not A).
shr1_bcde:      MACRO
                or      a                       ; clear carry (feed 0 into MSB)
                rr      b
                rr      c
                rr      d
                rr      e
ENDM

; ld_bcde — load the 4-byte big-endian word at (HL) into B,C,D,E in natural
; order (B=byte0/MSB ... E=byte3/LSB). HL ends at word+3. Clobbers HL.
ld_bcde:        MACRO
                ld      b, (hl)
                inc     hl
                ld      c, (hl)
                inc     hl
                ld      d, (hl)
                inc     hl
                ld      e, (hl)
ENDM

; rotr8_bcde — rotate B,C,D,E right by 8 (whole-byte): [b0 b1 b2 b3] -> [b3 b0 b1
; b2]. The LSB becomes the new MSB. 20T. Clobbers A.
rotr8_bcde:     MACRO
                ld      a, e
                ld      e, d
                ld      d, c
                ld      c, b
                ld      b, a
ENDM

; rotl8_bcde — rotate B,C,D,E left by 8 (== ROTR24): [b0 b1 b2 b3] -> [b1 b2 b3
; b0]. The MSB becomes the new LSB. 20T. Clobbers A.
rotl8_bcde:     MACRO
                ld      a, b
                ld      b, c
                ld      c, d
                ld      d, e
                ld      e, a
ENDM

; rotr16_bcde — rotate B,C,D,E right by 16: [b0 b1 b2 b3] -> [b2 b3 b0 b1]. 16T.
; Clobbers A.
rotr16_bcde:    MACRO
                ld      a, b
                ld      b, d
                ld      d, a
                ld      a, c
                ld      c, e
                ld      e, a
ENDM

; xor_bcde_tmpa — sha_tmpa ^= (B,C,D,E). Clobbers A,HL. (B,C,D,E preserved.)
xor_bcde_tmpa:  MACRO
                ld      hl, sha_tmpa
                ld      a, b
                xor     (hl)
                ld      (hl), a
                inc     hl
                ld      a, c
                xor     (hl)
                ld      (hl), a
                inc     hl
                ld      a, d
                xor     (hl)
                ld      (hl), a
                inc     hl
                ld      a, e
                xor     (hl)
                ld      (hl), a
ENDM

; st_bcde_tmpa — sha_tmpa = (B,C,D,E). Clobbers A,HL. (B,C,D,E preserved.)
st_bcde_tmpa:   MACRO
                ld      hl, sha_tmpa
                ld      (hl), b
                inc     hl
                ld      (hl), c
                inc     hl
                ld      (hl), d
                inc     hl
                ld      (hl), e
ENDM

; ===========================================================================
; State + scratch (the data block the host test can read/reset). Placed FIRST
; so every data label is defined before the code references it — a forward data
; reference confuses pyz80's two-pass size estimation here.
; ===========================================================================

SHA_STATE:
SHA_H:          defs 32                 ; H0..H7, 8 words big-endian
SHA_BITLEN:     defs 8                  ; 64-bit message bit length, big-endian
SHA_FILL:       defs 1                  ; partial-block fill count (0..64)
SHA_BLOCK:      defs 64                 ; the 64-byte block buffer

; Compression scratch.
SHA_W:          defs 64*4               ; the message schedule W[0..63]
wv_abcdefgh:                           ; working vars a..h (8 words)
wv_a:          defs 4
wv_b:          defs 4
wv_c:          defs 4
wv_d:          defs 4
wv_e:          defs 4
wv_f:          defs 4
wv_g:          defs 4
wv_h:          defs 4
sha_t1:         defs 4
sha_t2:         defs 4
sha_tmp2:       defs 4                  ; s0(W[t-15]) stash during the extend loop
sha_tmpa:       defs 4                  ; sigma/Ch/Maj result word
sha_wt:         defs 2                  ; address of W[t] during the extend loop
sha_wptr:       defs 2                  ; address of W[t] during the round loop
sha_kt:         defs 2                  ; address of K[t] during the round loop
sha_round_ctr:  defs 1                  ; 64-round loop counter (memory, not B)
sha_ext_ctr:    defs 1                  ; 48-word extend loop counter (memory)

; ===========================================================================
; Public entry points
; ===========================================================================

; ---------------------------------------------------------------------------
; sha256_init — reset all state: the 8 IV words, the 64-bit bit-length counter,
; and the partial-block fill count. Must FULLY reset so a re-init after a final
; produces a fresh hash (final must not leave H in a broken state).
; In:  (nothing)   Out: (nothing)   Clobbers: A, BC, DE, HL.
; ---------------------------------------------------------------------------
sha256_init:
                ld      hl, sha256_iv
                ld      de, SHA_H
                ld      bc, 32
                ldir                            ; H0..H7 <- the IVs (big-endian)
                xor     a
                ld      hl, SHA_BITLEN
                ld      b, 8
sha_init_zlen:
                ld      (hl), a
                inc     hl
                djnz    sha_init_zlen           ; 64-bit bit-length = 0
                ld      (SHA_FILL), a           ; partial-block fill = 0
                ret

; ---------------------------------------------------------------------------
; sha256_update — append BC bytes at HL to the running hash.
; In:  HL = data pointer, BC = byte length.
; Out: (nothing)   Clobbers: A, BC, DE, HL, IX, and all working scratch.
;
; Adds len*8 to the 64-bit bit-length counter, then copies the bytes into the
; 64-byte block buffer; each time the buffer fills (fill==64) it runs the
; compression on it and resets fill to 0. Correctly handles: len 0; len smaller
; than the remaining block space (just buffer it); len spanning many blocks; and
; being called repeatedly with arbitrary chunk sizes (a partial block carried in
; SHA_FILL across calls).
; ---------------------------------------------------------------------------
sha256_update:
                ; Add len*8 to the 64-bit big-endian bit-length counter. len is
                ; a 16-bit byte count (BC); len*8 is at most 19 bits, so it
                ; spans the low 3 bytes of the 8-byte counter. Compute len<<3 as
                ; a 24-bit value in (E:D:A)=high..low and add it big-endian.
                push    hl
                push    bc
                ld      h, b
                ld      l, c                    ; HL = len
                ld      de, 0                   ; DE will hold the overflow bits
                add     hl, hl                  ; <<1
                rl      e
                add     hl, hl                  ; <<2
                rl      e
                add     hl, hl                  ; <<3  (HL = (len<<3) low 16, E = bits 16..)
                rl      e
                ; Now (E:H:L) = len*8 as a 24-bit big-endian-ish triple
                ; (E = byte for bits 16-23, H = bits 8-15, L = bits 0-7).
                ; Add it into SHA_BITLEN (big-endian, MSB at SHA_BITLEN+0,
                ; LSB at SHA_BITLEN+7).
                ld      a, (SHA_BITLEN+7)
                add     a, l
                ld      (SHA_BITLEN+7), a
                ld      a, (SHA_BITLEN+6)
                adc     a, h
                ld      (SHA_BITLEN+6), a
                ld      a, (SHA_BITLEN+5)
                adc     a, e
                ld      (SHA_BITLEN+5), a
                ; propagate the carry through the remaining high bytes
                ld      b, 5
                ld      hl, SHA_BITLEN+4
sha_upd_carry:
                ld      a, (hl)
                adc     a, 0
                ld      (hl), a
                dec     hl
                djnz    sha_upd_carry
                pop     bc
                pop     hl

                ; Copy BC bytes from HL into the block buffer, compressing each
                ; time it fills.
sha_upd_loop:
                ld      a, b
                or      c
                ret     z                       ; no bytes left
                ; Append one byte: dst = SHA_BLOCK + fill.
                push    bc
                ld      a, (SHA_FILL)
                ld      e, a
                ld      d, 0
                push    hl
                ld      hl, SHA_BLOCK
                add     hl, de                  ; HL -> SHA_BLOCK + fill
                ex      de, hl                  ; DE -> dst
                pop     hl                      ; HL -> src
                ld      a, (hl)
                ld      (de), a
                inc     hl                      ; advance src
                ld      a, (SHA_FILL)
                inc     a
                ld      (SHA_FILL), a
                pop     bc
                dec     bc                      ; one byte consumed
                cp      64
                jr      nz, sha_upd_loop        ; block not full yet
                ; Block full: compress it and reset the fill.
                push    bc
                push    hl
                call    sha256_compress
                xor     a
                ld      (SHA_FILL), a
                pop     hl
                pop     bc
                jr      sha_upd_loop

; ---------------------------------------------------------------------------
; sha256_final — finish the hash and write the 32-byte big-endian digest.
; In:  HL = output pointer (32 bytes).   Out: 32 bytes written at HL.
; Clobbers: A, BC, DE, HL, IX, and all working scratch.
;
; Appends the FIPS padding: a 0x80 byte, then 0x00 bytes until 8 bytes remain in
; the block, then the 64-bit message bit-length BIG-ENDIAN — running the
; compression on the final 1 or 2 blocks — then writes H0..H7 as 32 big-endian
; bytes to (HL). Does not disturb the IVs, so a subsequent sha256_init fully
; resets.
; ---------------------------------------------------------------------------
sha256_final:
                push    hl                      ; save output pointer

                ; Append the 0x80 byte at SHA_BLOCK+fill, bumping fill.
                ld      a, (SHA_FILL)
                ld      e, a
                ld      d, 0
                ld      hl, SHA_BLOCK
                add     hl, de
                ld      (hl), &80
                ld      a, e
                inc     a
                ld      (SHA_FILL), a           ; fill now points just past 0x80

                ; If fill > 56 there is no room for the 8-byte length in this
                ; block: zero-fill to 64, compress, then start a fresh zero
                ; block. (fill == 57..64 here means > 56.)
                cp      57
                jr      c, sha_final_padlen     ; fill <= 56: length fits

                ; Zero-fill the rest of this block (indices fill..63) and
                ; compress it.
                call    sha_zerofill_to_64
                call    sha256_compress
                xor     a
                ld      (SHA_FILL), a

sha_final_padlen:
                ; Zero-fill indices fill..55 (leave 56..63 for the length).
                ld      a, (SHA_FILL)
                ld      e, a
                ld      d, 0
                ld      hl, SHA_BLOCK
                add     hl, de                  ; HL -> SHA_BLOCK + fill
                ld      a, 56
                sub     e                       ; A = 56 - fill = bytes to zero
                jr      z, sha_final_len        ; already at 56
                ld      b, a
                xor     a
sha_final_zloop:
                ld      (hl), a
                inc     hl
                djnz    sha_final_zloop

sha_final_len:
                ; Write the 64-bit bit-length big-endian into bytes 56..63.
                ld      hl, SHA_BITLEN
                ld      de, SHA_BLOCK+56
                ld      bc, 8
                ldir
                call    sha256_compress

                ; Write H0..H7 (32 big-endian bytes) to the output pointer.
                pop     de                      ; DE = output pointer
                ld      hl, SHA_H
                ld      bc, 32
                ldir
                ret

; ---------------------------------------------------------------------------
; sha_zerofill_to_64 — zero block bytes (fill..63). In: SHA_FILL. Clobbers A,B,
; DE,HL. (Used only when fill <= 64.)
; ---------------------------------------------------------------------------
sha_zerofill_to_64:
                ld      a, (SHA_FILL)
                ld      e, a
                ld      d, 0
                ld      hl, SHA_BLOCK
                add     hl, de
                ld      a, 64
                sub     e                       ; A = 64 - fill
                ret     z
                ld      b, a
                xor     a
sha_zf_loop:
                ld      (hl), a
                inc     hl
                djnz    sha_zf_loop
                ret

; ===========================================================================
; sha256_compress — the FIPS 180-4 compression function over SHA_BLOCK (64
; bytes). Builds W[0..63], runs 64 rounds, folds a..h into H0..H7.
; Clobbers: A, BC, DE, HL, IX, and the W/working scratch.
; ===========================================================================
sha256_compress:
                ; --- W[0..15] = the 16 block words (already big-endian) -------
                ld      hl, SHA_BLOCK
                ld      de, SHA_W
                ld      bc, 64
                ldir

                ; --- W[16..63] = s1(W[t-2]) + W[t-7] + s0(W[t-15]) + W[t-16] ---
                ; t runs 16..63; each W is a 4-byte big-endian word at SHA_W+4*t.
                ; sha_wt holds the address of W[t]; the source words are at fixed
                ; negative offsets from it (-k*4). Z80 cannot `add hl,ix`, so the
                ; pointer arithmetic is done in HL via the sha_woff helper.
                ld      hl, SHA_W+16*4
                ld      (sha_wt), hl            ; W[t] for t=16
                ld      a, 48                   ; 48 words to extend (16..63);
                ld      (sha_ext_ctr), a        ; memory counter (body > djnz range)
sha_extend:
                ; tmp2 = s0(W[t-15])  (computed first; the running sum lives in
                ; sha_tmpa, which sha_sigma0/1 overwrite, so stash s0 aside)
                ld      de, -15*4
                call    sha_woff
                call    sha_sigma0
                ld      hl, sha_tmpa
                ld      de, sha_tmp2
                copy4_body
                ; sha_tmpa = s1(W[t-2])  (the running accumulator)
                ld      de, -2*4
                call    sha_woff                ; HL -> W[t-2]
                call    sha_sigma1
                ; sha_tmpa += s0(W[t-15])
                ld      de, sha_tmp2+3
                ld      hl, sha_tmpa+3
                add4_body
                ; sha_tmpa += W[t-7]
                ld      de, -7*4+3
                call    sha_woff                ; HL -> W[t-7]+3 (LSB)
                ex      de, hl                  ; DE -> W[t-7]+3
                ld      hl, sha_tmpa+3
                add4_body
                ; sha_tmpa += W[t-16]
                ld      de, -16*4+3
                call    sha_woff                ; HL -> W[t-16]+3 (LSB)
                ex      de, hl                  ; DE -> W[t-16]+3
                ld      hl, sha_tmpa+3
                add4_body
                ; W[t] = sha_tmpa
                ld      hl, sha_tmpa
                ld      de, (sha_wt)            ; DE -> W[t]
                copy4_body
                ; advance to W[t+1]
                ld      hl, (sha_wt)
                ld      bc, 4
                add     hl, bc
                ld      (sha_wt), hl
                ld      a, (sha_ext_ctr)
                dec     a
                ld      (sha_ext_ctr), a
                jp      nz, sha_extend

                ; --- Initialise working vars a..h <- H0..H7 -------------------
                ld      hl, SHA_H
                ld      de, wv_abcdefgh
                ld      bc, 32
                ldir

                ; --- 64 rounds -----------------------------------------------
                ; T1 = h + S1(e) + Ch(e,f,g) + K[t] + W[t]
                ; T2 = S0(a) + Maj(a,b,c)
                ; h=g; g=f; f=e; e=d+T1; d=c; c=b; b=a; a=T1+T2
                ld      a, 64                   ; round counter (in memory: the
                ld      (sha_round_ctr), a      ; loop body is too big for djnz's
                ld      hl, SHA_W               ; 8-bit displacement)
                ld      (sha_wptr), hl          ; W[t] address, kept in memory:
                ld      hl, sha256_k            ; sha_ch/sha_maj clobber IX/IY, so
                ld      (sha_kt), hl            ; the W/K pointers cannot live there
sha_round:
                ; T1 = h
                ld      hl, wv_h
                ld      de, sha_t1
                copy4_body
                ; T1 += S1(e)
                ld      hl, wv_e
                call    sha_bigsigma1
                ld      de, sha_tmpa+3
                ld      hl, sha_t1+3
                add4_body
                ; T1 += Ch(e,f,g)  (clobbers IX/IY — that is why W/K are in memory)
                call    sha_ch                  ; result in sha_tmpa
                ld      de, sha_tmpa+3
                ld      hl, sha_t1+3
                add4_body
                ; T1 += K[t]
                ld      de, (sha_kt)            ; DE -> K[t] base
                inc     de
                inc     de
                inc     de                      ; DE -> K[t]+3 (LSB)
                ld      hl, sha_t1+3
                add4_body
                ; T1 += W[t]
                ld      de, (sha_wptr)          ; DE -> W[t] base
                inc     de
                inc     de
                inc     de                      ; DE -> W[t]+3 (LSB)
                ld      hl, sha_t1+3
                add4_body
                ; T2 = S0(a)
                ld      hl, wv_a
                call    sha_bigsigma0
                ld      hl, sha_tmpa
                ld      de, sha_t2
                copy4_body
                ; T2 += Maj(a,b,c)
                call    sha_maj                 ; result in sha_tmpa
                ld      de, sha_tmpa+3
                ld      hl, sha_t2+3
                add4_body

                ; Rotate the working vars. Move from h downward so we don't
                ; clobber a source before it is copied:
                ;   h=g; g=f; f=e; e=d; d=c; c=b; b=a   (32 bytes shift up by 4)
                ; then e += T1 and a = T1 + T2.
                ld      hl, wv_g+3
                ld      de, wv_h+3
                ld      bc, 28
                lddr                            ; shift a..g up into b..h
                ; e = d + T1  (after the shift, e holds old d, so e += T1 = d+T1)
                ld      de, sha_t1+3
                ld      hl, wv_e+3
                add4_body
                ; a = T1 + T2
                ld      hl, sha_t1
                ld      de, wv_a
                copy4_body
                ld      de, sha_t2+3
                ld      hl, wv_a+3
                add4_body

                ; advance W and K pointers
                ld      hl, (sha_wptr)
                inc     hl
                inc     hl
                inc     hl
                inc     hl
                ld      (sha_wptr), hl
                ld      hl, (sha_kt)
                inc     hl
                inc     hl
                inc     hl
                inc     hl
                ld      (sha_kt), hl
                ld      a, (sha_round_ctr)
                dec     a
                ld      (sha_round_ctr), a
                jp      nz, sha_round

                ; --- H0..H7 += a..h ------------------------------------------
                ; Both regions are 8 contiguous 4-byte big-endian words. Walk a
                ; pair of LSB pointers (word base +3) up by 4 per word, adding
                ; in place with add4_body (which walks each word LSB->MSB).
                ld      b, 8                    ; 8 words
                ld      hl, SHA_H+3             ; HL -> H[0] LSB
                ld      de, wv_abcdefgh+3       ; DE -> a LSB
sha_addstate:
                push    bc
                push    hl                      ; save word-base LSB pointers
                push    de
                add4_body                       ; H[i] += working[i] (HL,DE walk down)
                pop     de
                pop     hl
                ld      bc, 4
                add     hl, bc                  ; -> next H[i] LSB
                ex      de, hl
                add     hl, bc                  ; -> next working[i] LSB
                ex      de, hl
                pop     bc
                djnz    sha_addstate
                ret

; ===========================================================================
; 32-bit word helpers. Words are 4 bytes BIG-ENDIAN (byte 0 = MSB).
; ===========================================================================

; ---------------------------------------------------------------------------
; sha_woff — return (sha_wt + DE) in HL: the address of W[t-k] for a signed
; offset DE = -k*4. In: DE = signed byte offset. Out: HL = sha_wt + DE.
; Clobbers HL only (DE preserved-as-input is consumed).
; ---------------------------------------------------------------------------
sha_woff:
                ld      hl, (sha_wt)
                add     hl, de
                ret

; ---------------------------------------------------------------------------
; Rotation strategy. Each sigma/Sigma term is a fixed-distance rotate-right,
; built in registers (the rot*_bcde / shr1_bcde / ld_bcde macros above) from the
; cheapest mix of whole-byte rotates (free register relabels) and single-bit
; rotates. Each target is reached by the SHORT way — ROTR(k) == ROTR(8*q) then a
; small bit residual that rotates LEFT when that is nearer: e.g. ROTR7 = ROTR8 +
; ROTL1, ROTR22 = ROTL8 + ROTL2, ROTR25 = ROTL8 + ROTR1. SHR by >=8 drops the
; top byte(s) and feeds zeros: SHR10 = SHR8 + SHR1 + SHR1. The byte-for-byte
; NIST-vector test is the guardrail.
; ---------------------------------------------------------------------------

; ---------------------------------------------------------------------------
; The sigma/Sigma functions compute their three rotated/shifted terms in
; registers (B,C,D,E = byte0/MSB..byte3/LSB), XOR-reducing into sha_tmpa. Each
; loads the word into B,C,D,E and rotates it PROGRESSIVELY through the three
; targets — st_bcde_tmpa at the first, xor_bcde_tmpa at the next two — so a
; rotated form is never recomputed from scratch. The two sigmas with a SHR term
; (s0,s1) save the source pointer and reload it for the shift (which loses bits
; and cannot sit in the rotation chain). Each composed step's NET rotation
; equals the term's target; the byte-for-byte NIST-vector test is the guardrail.
; ---------------------------------------------------------------------------

; sha_sigma0 — s0(x) = ROTR7(x) ^ ROTR18(x) ^ SHR3(x). In: HL -> x.
; Out: sha_tmpa = result. Clobbers A,BC,DE,HL.
sha_sigma0:
                push    hl                       ; save source (the shift term reloads)
                ld_bcde                          ; B,C,D,E <- x
                ; term1 = ROTR7 = ROTR8 + ROTL1
                rotr8_bcde
                rotl1_bcde
                st_bcde_tmpa
                ; term2 = ROTR18 = ROTR7 then +ROTR11 (ROTR8 + ROTR3)
                rotr8_bcde
                rotr1_bcde
                rotr1_bcde
                rotr1_bcde
                xor_bcde_tmpa
                ; term3 = SHR3(x) (reload pristine; shift loses bits)
                pop     hl
                ld_bcde
                shr1_bcde
                shr1_bcde
                shr1_bcde
                xor_bcde_tmpa
                ret

; sha_sigma1 — s1(x) = ROTR17(x) ^ ROTR19(x) ^ SHR10(x). In: HL -> x.
sha_sigma1:
                push    hl                       ; save source (the shift term reloads)
                ld_bcde                          ; B,C,D,E <- x
                ; term1 = ROTR17 = ROTR16 + ROTR1
                rotr16_bcde
                rotr1_bcde
                st_bcde_tmpa
                ; term2 = ROTR19 = ROTR17 then +ROTR2
                rotr1_bcde
                rotr1_bcde
                xor_bcde_tmpa
                ; term3 = SHR10 = SHR8 + SHR2 (reload pristine; shift loses bits)
                pop     hl
                ld_bcde
                ; SHR8: [b0 b1 b2 b3] -> [0 b0 b1 b2]
                ld      e, d
                ld      d, c
                ld      c, b
                ld      b, 0
                shr1_bcde
                shr1_bcde
                xor_bcde_tmpa
                ret

; sha_bigsigma0 — S0(x) = ROTR2(x) ^ ROTR13(x) ^ ROTR22(x). In: HL -> x.
; A pure rotate (no SHR term), so the chain needs the word only once: it loads
; B,C,D,E straight from the source, no save/reload.
sha_bigsigma0:
                ld_bcde                          ; B,C,D,E <- x (HL walks to x+3)
                ; term1 = ROTR2
                rotr1_bcde
                rotr1_bcde
                st_bcde_tmpa
                ; term2 = ROTR13 = ROTR2 then +ROTR11 (ROTR8 + ROTR3)
                rotr8_bcde
                rotr1_bcde
                rotr1_bcde
                rotr1_bcde
                xor_bcde_tmpa
                ; term3 = ROTR22 = ROTR13 then +ROTR9 (ROTR8 + ROTR1)
                rotr8_bcde
                rotr1_bcde
                xor_bcde_tmpa
                ret

; sha_bigsigma1 — S1(x) = ROTR6(x) ^ ROTR11(x) ^ ROTR25(x). In: HL -> x.
sha_bigsigma1:
                ld_bcde                          ; B,C,D,E <- x (HL walks to x+3)
                ; term1 = ROTR6 = ROTR8 + ROTL2
                rotr8_bcde
                rotl1_bcde
                rotl1_bcde
                st_bcde_tmpa
                ; term2 = ROTR11 = ROTR6 then +ROTR5 (ROTR8 + ROTL3)
                rotr8_bcde
                rotl1_bcde
                rotl1_bcde
                rotl1_bcde
                xor_bcde_tmpa
                ; term3 = ROTR25 = ROTR11 then +ROTR14 (ROTR16 + ROTL2)
                rotr16_bcde
                rotl1_bcde
                rotl1_bcde
                xor_bcde_tmpa
                ret

; ---------------------------------------------------------------------------
; sha_ch — Ch(e,f,g) = (e AND f) XOR ((NOT e) AND g), computed in the simplified
; form g XOR (e AND (f XOR g)) — one fewer op per byte and no CPL.
; Reads wv_e/wv_f/wv_g. Out: sha_tmpa = result. Clobbers A,B,DE,HL,IX,IY.
; ---------------------------------------------------------------------------
sha_ch:
                ld      hl, wv_e                ; HL -> e[i]
                ld      de, wv_f                ; DE -> f[i]
                ld      ix, wv_g                ; IX -> g[i]
                ld      iy, sha_tmpa            ; IY -> dst[i]
                ld      b, 4
sha_ch_lp:
                ld      a, (de)                 ; f
                xor     (ix+0)                  ; f ^ g
                and     (hl)                    ; e & (f^g)
                xor     (ix+0)                  ; g ^ (e & (f^g))
                ld      (iy+0), a
                inc     hl
                inc     de
                inc     ix
                inc     iy
                djnz    sha_ch_lp
                ret

; ---------------------------------------------------------------------------
; sha_maj — Maj(a,b,c) = (a AND b) XOR (a AND c) XOR (b AND c), computed in the
; simplified form (a AND b) OR (c AND (a XOR b)) — 1 XOR + 2 AND + 1 OR.
; Reads wv_a/wv_b/wv_c. Out: sha_tmpa = result. Clobbers A,B,C,DE,HL,IX,IY.
; ---------------------------------------------------------------------------
sha_maj:
                ld      hl, wv_a               ; HL -> a[i]
                ld      ix, wv_b               ; IX -> b[i]
                ld      iy, wv_c               ; IY -> c[i]
                ld      de, sha_tmpa            ; DE -> dst[i]
                ld      b, 4
sha_maj_lp:
                ld      a, (hl)                 ; a
                xor     (ix+0)                  ; a ^ b
                and     (iy+0)                  ; c & (a^b)
                ld      c, a                    ; save c & (a^b)
                ld      a, (hl)                 ; a
                and     (ix+0)                  ; a & b
                or      c                       ; (a&b) | (c & (a^b))
                ld      (de), a
                inc     hl
                inc     de
                inc     ix
                inc     iy
                djnz    sha_maj_lp
                ret

; ===========================================================================
; Constants
; ===========================================================================

; The eight SHA-256 initial hash values H0..H7 (FIPS 180-4 §5.3.3), big-endian.
sha256_iv:
                defb &6a,&09,&e6,&67
                defb &bb,&67,&ae,&85
                defb &3c,&6e,&f3,&72
                defb &a5,&4f,&f5,&3a
                defb &51,&0e,&52,&7f
                defb &9b,&05,&68,&8c
                defb &1f,&83,&d9,&ab
                defb &5b,&e0,&cd,&19

; The 64 round constants K[0..63] (FIPS 180-4 §4.2.2), each big-endian 32-bit.
sha256_k:
                defb &42,&8a,&2f,&98, &71,&37,&44,&91, &b5,&c0,&fb,&cf, &e9,&b5,&db,&a5
                defb &39,&56,&c2,&5b, &59,&f1,&11,&f1, &92,&3f,&82,&a4, &ab,&1c,&5e,&d5
                defb &d8,&07,&aa,&98, &12,&83,&5b,&01, &24,&31,&85,&be, &55,&0c,&7d,&c3
                defb &72,&be,&5d,&74, &80,&de,&b1,&fe, &9b,&dc,&06,&a7, &c1,&9b,&f1,&74
                defb &e4,&9b,&69,&c1, &ef,&be,&47,&86, &0f,&c1,&9d,&c6, &24,&0c,&a1,&cc
                defb &2d,&e9,&2c,&6f, &4a,&74,&84,&aa, &5c,&b0,&a9,&dc, &76,&f9,&88,&da
                defb &98,&3e,&51,&52, &a8,&31,&c6,&6d, &b0,&03,&27,&c8, &bf,&59,&7f,&c7
                defb &c6,&e0,&0b,&f3, &d5,&a7,&91,&47, &06,&ca,&63,&51, &14,&29,&29,&67
                defb &27,&b7,&0a,&85, &2e,&1b,&21,&38, &4d,&2c,&6d,&fc, &53,&38,&0d,&13
                defb &65,&0a,&73,&54, &76,&6a,&0a,&bb, &81,&c2,&c9,&2e, &92,&72,&2c,&85
                defb &a2,&bf,&e8,&a1, &a8,&1a,&66,&4b, &c2,&4b,&8b,&70, &c7,&6c,&51,&a3
                defb &d1,&92,&e8,&19, &d6,&99,&06,&24, &f4,&0e,&35,&85, &10,&6a,&a0,&70
                defb &19,&a4,&c1,&16, &1e,&37,&6c,&08, &27,&48,&77,&4c, &34,&b0,&bc,&b5
                defb &39,&1c,&0c,&b3, &4e,&d8,&aa,&4a, &5b,&9c,&ca,&4f, &68,&2e,&6f,&f3
                defb &74,&8f,&82,&ee, &78,&a5,&63,&6f, &84,&c8,&78,&14, &8c,&c7,&02,&08
                defb &90,&be,&ff,&fa, &a4,&50,&6c,&eb, &be,&f9,&a3,&f7, &c6,&71,&78,&f2
