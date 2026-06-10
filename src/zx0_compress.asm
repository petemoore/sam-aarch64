; zx0_compress.asm — greedy ZX0-format compressor (standalone, org 0).
;
; Z80 port of tools/zx0-greedy/compress.go (Go authority).
; Every routine maps to a named Go function; the Go function name is
; cited above each routine.  Port law: mirror the Go faithfully; do not
; redesign.  Reference: CLAUDE.md §"If Go already implements it, the Z80
; side is a port, not a design".
;
; ── Calling convention ───────────────────────────────────────────────
;
; Entry:
;   HL = src        pointer to input block
;   DE = src_len    16-bit input length (<= 8192)
;   BC = dst        pointer to output buffer (caller provides headroom)
;   IX = ws_base    pointer to scratch workspace (see layout below)
;
; Return:
;   HL = compressed length (bytes written starting at dst)
;   All other registers undefined, including IX, IY and the full
;   shadow set (BC'/DE'/HL'/AF').
;
; Requirements:
;   * Interrupts must be disabled (or the interrupt handler must
;     preserve the shadow registers): the bit-writer state lives in
;     BC'/DE'/HL' for the whole run.
;   * The code is self-modifying (constants are patched into
;     immediates at entry), so it must run from RAM and is not
;     reentrant.
;   * Undocumented-but-universal Z80 ops are used (IXh/IXl/IYl
;     register halves).  These work on all NMOS Z80s including the
;     SAM's Z80B, and are implemented by SimCoupé and koron-go/z80;
;     pyz80 assembles them natively.
;
; ── Workspace layout (mirroring Go: compress.go ScratchBytes) ────────
;
; Offsets relative to ws_base:
;
;   0 .. HASH_SIZE*2-1             hash table (uint16[HASH_SIZE], init &FFFF)
;   HASH_SIZE*2 .. +src_len*2-1   chain array (uint16[src_len], init &FFFF)
;   HASH_SIZE*2 + src_len*2 ..+29  fixed state block (30 bytes)
;
; Total: HASH_SIZE*2 + src_len*2 + 30 bytes.
; At H=512, blockLen=4096: 1024 + 8192 + 30 = 9246 B.
; Matches Go: ScratchBytes(Params{512,16}, 4096) = 9246.  ✓
;
; ── Implementation note on state access ──────────────────────────────
;
; Hot state is register-resident: the bit writer lives in the shadow
; bank (D'E' = out_ptr, H'L' = bit_byte_ptr, C' = mask, B' =
; backtrack) and the match-finder walk keeps its working set in
; DE/BC/IXh/IXl/IYl with per-call constants patched into instruction
; immediates (self-modifying code; legal because the payload runs from
; RAM).  Only the cold parse state (pos, litStart, lastOffset,
; prevWasLit, best_len, best_off) uses absolute-addressed variables.
;
; ── Parameters (Go: DefaultParams = Params{HashSize:512, ChainDepth:16}) ─

HASH_SIZE:       equ     512     ; power of two in {256,512,1024,2048}
CHAIN_DEPTH:     equ     16      ; in {4,8,16,32}
HASH_MASK:       equ     (HASH_SIZE - 1)
HASH_SIZE_BYTES: equ     (HASH_SIZE * 2)

                org     0

; ════════════════════════════════════════════════════════════════════
; zx0_compress — top-level entry point.
; Go: compress.go Compress().
;
; Entry: HL=src, DE=src_len, BC=dst, IX=ws_base.
; Return: HL=compressed_length.
; ════════════════════════════════════════════════════════════════════
zx0_compress:
        ; Stash entry arguments.
        ld      (zxc_src),     hl
        ld      (zxc_h2_src+1), hl      ; patch src base into zxc_hash2
        ld      (zxc_fb_src1+1), hl     ; patch src base into zxc_find_best
        ld      (zxc_fb_src2+1), hl
        ld      (zxc_it_src+1), hl      ; patch src base into zxc_insert_tail
        ld      (zxc_dst),     bc
        ld      (zxc_src_len), de
        ld      (zxc_ml_len+1), de      ; patch src_len into the main loop

        ; ws_base is in IX; save to a module variable too.
        push    ix
        pop     hl                      ; HL = ws_base
        ld      (zxc_ws_base), hl
        ld      (zxc_h2_ws+1), hl       ; patch hashHead base into zxc_hash2
        ld      (zxc_it_ws+1), hl       ; patch hashHead base into zxc_insert_tail

        ; chain_base = ws_base + HASH_SIZE_BYTES, patched into every
        ; chain-array user.
        ld      bc, HASH_SIZE_BYTES
        add     hl, bc                  ; HL = chain_base
        ld      (zxc_fb_chain+1), hl    ; patch chain_base into zxc_find_best
        ld      (zxc_fb_chain2+1), hl
        ld      (zxc_it_chain+1), hl    ; patch chain_base into zxc_insert_tail

        ; ── Fill hash table with &FF (sentinel &FFFF) ─────────────────
        ; Go: newMatchFinder initialises hashHead[] = 0xFFFF.
        ; LDIR ripple-fill: write the first byte, then copy each byte to
        ; the next address (21 T/byte vs ~33 T/byte for a manual loop).
        ld      hl, (zxc_ws_base)
        ld      (hl), &FF
        ld      d, h
        ld      e, l
        inc     de
        ld      bc, HASH_SIZE_BYTES - 1
        ldir

        ; The chain array is NOT initialised.  Go fills chain[] = 0xFFFF,
        ; but that fill is unobservable: chain[c] is only ever read for a
        ; candidate c obtained from hashHead[] or a previous chain read,
        ; and every non-sentinel candidate enters those structures via
        ; insert(c), which writes chain[c] BEFORE publishing c in
        ; hashHead[].  The 0xFFFF sentinel terminates the walk before
        ; chain[0xFFFF] could be read.  Hence every chain entry is
        ; written before it can be read and the fill value never reaches
        ; the output (oracle-verified; saves ~66 T per input byte).

        ; ── Initialise the bit writer in the shadow registers ─────────
        ; Go: newBitWriter (bitMask = 0, backtrack = true).
        ; Shadow bank, resident until return:
        ;   D'E' = out_ptr   H'L' = bit_byte_ptr (valid once a holder opens)
        ;   C'   = bit_mask  B'   = backtrack flag (0/1)
        exx
        ld      de, (zxc_dst)           ; out_ptr = dst
        ld      c, 0                    ; bit_mask = 0
        ld      b, 1                    ; backtrack = 1
        exx

        ; pos = 0, litStart = 0, lastOffset = 1, prevWasLit = 0
        ld      hl, 0
        ld      (zxc_pos),       hl
        ld      (zxc_litstart),  hl
        ld      hl, 1
        ld      (zxc_lastoff),   hl
        xor     a
        ld      (zxc_prevlit),   a

        ; ── Handle empty input ────────────────────────────────────────
        ld      hl, (zxc_src_len)
        ld      a, h
        or      l
        jp      nz, zxc_main_loop

        ; Go consumes the initial backtrack bit without writing (its
        ; len(out) > 0 guard): clear the flag instead of calling
        ; zxc_bit1, which would touch dst[-1].
        exx
        ld      b, 0
        exx
        ld      hl, 256
        call    zxc_eg_inv
        jp      zxc_return_len

; ════════════════════════════════════════════════════════════════════
; Main greedy parse loop.  Go: compress.go lines 99–163.
; ════════════════════════════════════════════════════════════════════
zxc_main_loop:
        ; Exit when pos >= src_len.
        ld      hl, (zxc_pos)
zxc_ml_len:
        ld      de, 0                   ; SMC: src_len, patched at entry
        ld      a, l
        sub     e
        ld      a, h
        sbc     a, d
        jp      nc, zxc_after_loop

        ; findBest(pos).  Updates zxc_best_len, zxc_best_off.
        call    zxc_find_best

        ; Any match at all?
        ld      a, (zxc_best_len)
        or      a
        jp      z, zxc_no_match

        ; bestOff == lastOffset?
        ld      hl, (zxc_best_off)
        ld      de, (zxc_lastoff)
        ld      a, l
        xor     e
        jp      nz, zxc_check_new
        ld      a, h
        xor     d
        jp      nz, zxc_check_new

        ; offsets equal.  canRep: bestLen>=2 AND (prevWasLit||litCount>0).
        ld      a, (zxc_best_len)
        cp      2
        jp      c, zxc_no_match
        ld      a, (zxc_prevlit)
        or      a
        jp      nz, zxc_do_rep
        ; Check litCount = pos - litStart > 0 (HL was clobbered by best_off load).
        ld      hl, (zxc_pos)
        ld      de, (zxc_litstart)
        sbc     hl, de                  ; HL = pos - litStart (borrow from prior ops ok)
        ld      a, h
        or      l
        jp      nz, zxc_do_rep
        jp      zxc_no_match            ; litCount==0, prevlit==0: not a rep

zxc_check_new:
        ; isNew: bestLen >= 3.
        ld      a, (zxc_best_len)
        cp      3
        jp      c, zxc_no_match
        jp      zxc_do_new              ; bestLen >= 3: emit new-offset match

; ─────────────────────────────────────────────────────────────────
zxc_no_match:
        ld      hl, (zxc_pos)
        inc     hl
        ld      (zxc_pos), hl
        jp      zxc_main_loop           ; advance past literal, loop

; ─────────────────────────────────────────────────────────────────
; Rep match.  Go: compress.go lines 137–142.
; ─────────────────────────────────────────────────────────────────
zxc_do_rep:
        ; Compute litCount = pos - litStart into HL.
        ld      hl, (zxc_pos)
        ld      de, (zxc_litstart)
        ld      a, l
        sub     e
        ld      l, a
        ld      a, h
        sbc     a, d
        ld      h, a
        ld      a, h
        or      l
        jp      z, zxc_rep_nf
        push    hl
        call    zxc_bit0
        pop     hl
        call    zxc_eg
        call    zxc_flush_lits
zxc_rep_nf:
        call    zxc_bit0
        ld      hl, (zxc_best_len)
        call    zxc_eg
        xor     a
        ld      (zxc_prevlit), a
        call    zxc_insert_tail
        ld      hl, (zxc_pos)
        ld      de, (zxc_best_len)
        add     hl, de
        ld      (zxc_pos),      hl
        ld      (zxc_litstart), hl
        jp      zxc_main_loop           ; advance past matched bytes, loop

; ─────────────────────────────────────────────────────────────────
; New-offset match.  Go: compress.go lines 143–151.
; ─────────────────────────────────────────────────────────────────
zxc_do_new:
        ; Recompute litCount.
        ld      hl, (zxc_pos)
        ld      de, (zxc_litstart)
        ld      a, l
        sub     e
        ld      l, a
        ld      a, h
        sbc     a, d
        ld      h, a

        ld      a, h
        or      l
        jp      z, zxc_new_nf
        push    hl
        call    zxc_bit0
        pop     hl
        call    zxc_eg
        call    zxc_flush_lits
zxc_new_nf:
        call    zxc_bit1                ; new-offset separator

        ; msb = (bestOff-1)/128 + 1.
        ld      hl, (zxc_best_off)
        dec     hl                      ; HL = bestOff-1
        ; (HL)>>7: for HL = H:L, result = H*2 + bit7(L).
        ld      a, h
        add     a, a                    ; A = H<<1
        ld      b, a
        ld      a, l
        rlca                            ; A = rotate L left; bit7 → bit0
        and     1
        or      b                       ; A = (HL)>>7 = (bestOff-1)/128
        inc     a                       ; A = msb
        ld      l, a
        ld      h, 0
        call    zxc_eg_inv

        ; lsbByte = (127 - (bestOff-1)%128) << 1
        ld      hl, (zxc_best_off)
        dec     hl
        ld      a, l
        and     &7F
        ld      c, a
        ld      a, 127
        sub     c
        add     a, a                    ; = lsbByte
        call    zxc_write_bbt

        ; writeEliasGamma(bestLen-1)
        ld      hl, (zxc_best_len)
        dec     hl
        call    zxc_eg

        ld      hl, (zxc_best_off)
        ld      (zxc_lastoff), hl
        xor     a
        ld      (zxc_prevlit), a
        call    zxc_insert_tail
        ld      hl, (zxc_pos)
        ld      de, (zxc_best_len)
        add     hl, de
        ld      (zxc_pos),      hl
        ld      (zxc_litstart), hl
        jp      zxc_main_loop           ; advance past matched bytes, loop

; ─────────────────────────────────────────────────────────────────
; After loop: flush trailing literals + end marker.
; Go: compress.go lines 165–177.
; ─────────────────────────────────────────────────────────────────
zxc_after_loop:
        ld      hl, (zxc_src_len)
        ld      de, (zxc_litstart)
        ld      a, l
        sub     e
        ld      l, a
        ld      a, h
        sbc     a, d
        ld      h, a
        ld      a, h
        or      l
        jp      z, zxc_end_marker
        push    hl
        call    zxc_bit0
        pop     hl
        call    zxc_eg
        call    zxc_flush_lits
zxc_end_marker:
        call    zxc_bit1
        ld      hl, 256
        call    zxc_eg_inv

zxc_return_len:
        exx
        push    de                      ; D'E' = final out_ptr
        exx
        pop     hl
        ld      de, (zxc_dst)
        or      a
        sbc     hl, de                  ; HL = compressed length
        ret


; ════════════════════════════════════════════════════════════════════
; zxc_flush_lits — write src[litStart..pos-1] to output.
; Go: compress.go lines 130–134 and 167–170.
; Sets prevWasLit = 1.
; Clobbers: A, HL, DE, BC.
; ════════════════════════════════════════════════════════════════════
zxc_flush_lits:
        ld      hl, (zxc_pos)
        ld      de, (zxc_litstart)
        or      a
        sbc     hl, de                  ; HL = count (DE = litStart kept)
        ret     z
        ld      b, h
        ld      c, l                    ; BC = count
        ld      hl, (zxc_src)
        add     hl, de                  ; HL = &src[litStart]
        ; LDIR straight into the output: fetch out_ptr from the shadow
        ; bank, copy, and put the advanced pointer back.
        exx
        push    de
        exx
        pop     de                      ; DE = out_ptr
        ldir
        push    de
        exx
        pop     de                      ; out_ptr += count
        exx
        ld      a, 1
        ld      (zxc_prevlit), a
        ret


; ════════════════════════════════════════════════════════════════════
; zxc_insert_tail — insert positions pos+1..pos+bestLen-1 into chain.
; Go: compress.go lines 157–159.
;
; The loop keeps i in DE and the count in IXl (bestLen-1 <= 254 fits
; 8 bits; both callers guarantee bestLen >= 2, so the count is >= 1),
; and inlines the insert with its hash so nothing is saved across a
; call.  &chain[i] is computed first and parked in an immediate while
; the hash clobbers BC.
; Clobbers: A, HL, DE, BC, IXl.
; ════════════════════════════════════════════════════════════════════
zxc_insert_tail:
        ld      a, (zxc_best_len)       ; bestLen (<= 255)
        dec     a
        ld      ixl, a                  ; count = bestLen-1 (>= 1)
        ld      de, (zxc_pos)
        ; Park &chain[pos+1] in the cslot immediate; the loop advances
        ; it by 2 per iteration (chain slots are consecutive).
        ld      h, d
        ld      l, e
        inc     hl
        add     hl, hl
zxc_it_chain:
        ld      bc, 0                   ; SMC: chain_base, patched at entry
        add     hl, bc
        ld      (zxc_it_cslot+1), hl
zxc_it_loop:
        inc     de                      ; i = pos+k
        ; Inline zxc_hash2 body (see there for the boundary-check proof).
        ld      h, d
        ld      l, e
zxc_it_src:
        ld      bc, 0                   ; SMC: src base, patched at entry
        add     hl, bc                  ; HL = &src[i]
        ld      a, (hl)
        inc     hl
        ld      c, (hl)
        ld      h, zxc_tab31lo >> 8
        ld      l, a
        ld      a, (hl)
        xor     c
        inc     h
        ld      h, (hl)
        ld      l, a
        add     hl, hl
zxc_it_ws:
        ld      bc, 0                   ; SMC: hashHead base, patched at entry
        add     hl, bc                  ; HL = &hashHead[hash]
        ; Old head out, i in.
        ld      c, (hl)
        ld      (hl), e
        inc     hl
        ld      b, (hl)
        ld      (hl), d                 ; BC = old head; hashHead[h] = i
zxc_it_cslot:
        ld      hl, 0                   ; SMC: &chain[i]
        ld      (hl), c
        inc     hl
        ld      (hl), b                 ; chain[i] = old head
        inc     hl
        ld      (zxc_it_cslot+1), hl    ; advance to &chain[i+1]
        dec     ixl
        jp      nz, zxc_it_loop
        ret


; ════════════════════════════════════════════════════════════════════
; zxc_find_best — find best match at pos; insert pos into chain.
; Go: matchFinder.findBest (compress.go lines 402–438).
; Updates zxc_best_len, zxc_best_off.
; Clobbers: A, HL, DE, BC, IXh, IXl, IYl.
;
; Walk-loop register allocation:
;   DE  = candidate index        BC = pos_ptr (src + pos, constant)
;   IXh = bestLen so far         IXl = chain steps remaining
;   HL  = scratch                IYl = deep-compare counter
; Walk constants live in immediates patched per call (pos, maxML,
; screen byte/base, hashHead slot) or at entry (src, chain_base).
;
; Time-only deviations from the Go shape (output proven identical by
; the byte-identity oracle):
;
; * Screen test: cand_ptr[bestLen] is compared with pos_ptr[bestLen]
;   before a full compare.  A candidate failing it has ml <= bestLen,
;   which cannot change (bestLen, bestOff): Go updates only on
;   ml > bestLen, and its ml == bestLen tie case rewrites the same
;   values.  Candidates are walked in the same order with the same
;   chain-depth budget, so the selected match is identical.
; * Go's off-range checks are dropped: off <= 0 is impossible (every
;   chain entry is a previously-inserted position strictly below the
;   probing pos) and off > 32640 is impossible at src_len <= 8192.
; * Ceiling exit: once bestLen == maxML no candidate can satisfy
;   ml > bestLen, and the walk has no side effects, so it ends early.
; * The sentinel test checks D == &FF only: positions are <= 8191
;   (D <= &1F), so D == &FF occurs exactly for the &FFFF sentinel.
; ════════════════════════════════════════════════════════════════════
zxc_find_best:
        ; bestLen = 0.  zxc_best_off is left stale: the main loop reads
        ; it only when bestLen > 0, and any update writes it first.
        ld      ixh, 0

        ; Patch pos into the off-computation immediates.
        ld      de, (zxc_pos)
        ld      (zxc_fb_pos2+1), de
        ld      (zxc_fb_pos3+1), de

        ; maxML = min(255, src_len - pos), patched into both users.
        ld      hl, (zxc_src_len)
        or      a
        sbc     hl, de                  ; HL = src_len - pos (>= 1)
        ld      a, h
        or      a
        jp      z, zxc_fb_ml_ok
        ld      l, 255
zxc_fb_ml_ok:
        ld      a, l
        ld      (zxc_fb_maxml+1), a
        ld      (zxc_fb_maxml2+1), a

        ; Hash pos before pos_ptr lands in BC (zxc_hash2 clobbers BC).
        ; The slot address is cached for the post-walk insert (the walk
        ; never writes hashHead, so the probed slot is still current).
        ld      h, d
        ld      l, e
        call    zxc_hash2               ; HL = &hashHead[h]; DE preserved
        ld      (zxc_fb_slot+1), hl
        push    hl                      ; save slot for the candidate read

        ; pos_ptr = src + pos -> BC; screen byte for bestLen=0 is
        ; src[pos]; screen base for bestLen=0 is src itself.
zxc_fb_src1:
        ld      hl, 0                   ; SMC: src, patched at entry
        add     hl, de
        ld      b, h
        ld      c, l                    ; BC = pos_ptr
        ld      a, (hl)
        ld      (zxc_fb_scrv+1), a      ; screen byte = src[pos]
        ld      hl, (zxc_src)
        ld      (zxc_fb_scrb+1), hl     ; screen base = src + bestLen(0)

        ; candidate = hashHead[hash2(pos)]
        pop     hl
        ld      e, (hl)
        inc     hl
        ld      d, (hl)                 ; DE = candidate
        ld      ixl, CHAIN_DEPTH

zxc_fb_loop:
        ld      a, d
        inc     a                       ; D == &FF <=> sentinel &FFFF
        jp      z, zxc_fb_done
        ; Screen: src[candidate + bestLen] vs src[pos + bestLen].
zxc_fb_scrb:
        ld      hl, 0                   ; SMC: src + bestLen
        add     hl, de
        ld      a, (hl)
zxc_fb_scrv:
        cp      0                       ; SMC: src[pos + bestLen]
        jp      z, zxc_fb_deep
zxc_fb_next:
        ; candidate = chain[candidate]
zxc_fb_chain:
        ld      hl, 0                   ; SMC: chain_base, patched at entry
        add     hl, de
        add     hl, de
        ld      e, (hl)
        inc     hl
        ld      d, (hl)
        dec     ixl
        jp      nz, zxc_fb_loop
        jp      zxc_fb_done

; ── Deep compare: screen passed; count the match length ──────────────
zxc_fb_deep:
        push    de                      ; save candidate
zxc_fb_src2:
        ld      hl, 0                   ; SMC: src, patched at entry
        add     hl, de                  ; HL = cand_ptr
        ld      d, b
        ld      e, c                    ; DE = pos_ptr (advancing copy)
zxc_fb_maxml:
        ld      a, 0                    ; SMC: maxML
        ld      iyl, a                  ; IYl = compare budget
zxc_fb_dloop:
        ld      a, (de)
        cp      (hl)
        jp      nz, zxc_fb_dend
        inc     de
        inc     hl
        dec     iyl
        jp      nz, zxc_fb_dloop
zxc_fb_dend:
        ; ml = E - C (exact: ml <= 255)
        ld      a, e
        sub     c                       ; A = ml
        cp      ixh
        jp      c, zxc_fb_nextp         ; ml < bestLen: no improvement
        jp      z, zxc_fb_nextp         ; ml == bestLen: Go tie rewrite is a no-op
        ; ── ml > bestLen: update bestLen/bestOff ─────────────────────
        ld      ixh, a                  ; bestLen = ml
        pop     de                      ; DE = candidate
        push    de
zxc_fb_pos2:
        ld      hl, 0                   ; SMC: pos
        or      a
        sbc     hl, de                  ; HL = off = pos - candidate
        ld      (zxc_best_off), hl
        ; Ceiling: bestLen == maxML ends the walk (no side effects left).
zxc_fb_maxml2:
        cp      0                       ; SMC: maxML
        jp      z, zxc_fb_done_pop
        ; Repatch the screen for the new bestLen:
        ; screen byte = pos_ptr[bestLen]; screen base = src + bestLen.
        ld      l, a
        ld      h, 0
        add     hl, bc                  ; HL = pos_ptr + bestLen
        ld      a, (hl)
        ld      (zxc_fb_scrv+1), a
zxc_fb_pos3:
        ld      de, 0                   ; SMC: pos
        or      a
        sbc     hl, de                  ; HL = src + bestLen
        ld      (zxc_fb_scrb+1), hl
        pop     de                      ; DE = candidate
        jp      zxc_fb_next

zxc_fb_nextp:
        pop     de                      ; DE = candidate
        jp      zxc_fb_next

zxc_fb_done_pop:
        pop     de                      ; discard saved candidate
zxc_fb_done:
        ; Publish bestLen for the main loop.
        ld      a, ixh
        ld      (zxc_best_len), a
        xor     a
        ld      (zxc_best_len+1), a
        ; Insert pos using the cached hashHead slot (no second hash2).
        ld      de, (zxc_pos)
zxc_fb_slot:
        ld      hl, 0                   ; SMC: &hashHead[hash2(pos)]
        ld      c, (hl)
        ld      (hl), e
        inc     hl
        ld      b, (hl)
        ld      (hl), d                 ; BC = old head; hashHead[h] = pos
        ex      de, hl
        add     hl, hl
zxc_fb_chain2:
        ld      de, 0                   ; SMC: chain_base, patched at entry
        add     hl, de
        ld      (hl), c
        inc     hl
        ld      (hl), b                 ; chain[pos] = old head
        ret


; ════════════════════════════════════════════════════════════════════
; zxc_hash2 — hash position HL; return HL = &hashHead[hash].
; Go: matchFinder.hash2 (compress.go lines 371–377).
;
; hash = (src[i]*31 ^ src[i+1]) & HASH_MASK, looked up via the
; assemble-time tables zxc_tab31lo/zxc_tab31hi (page-aligned, one page
; each), so the *31 is an H/L substitution instead of 16-bit shifts.
;
; Go's i+1 boundary check (single-byte hash at the last position) is
; dropped as a time-only optimisation: position srclen-1 is the only
; one it affects, and the hash chosen there cannot reach the output —
; the probe at srclen-1 is bounded by maxML = 1, below both match
; thresholds (rep >= 2, new >= 3), and an insert at srclen-1 is never
; probed afterwards because the parse ends.  src[srclen] is read as a
; don't-care byte (oracle-verified byte-identical).
;
; Entry: HL = position i.  The zxc_h2_src/zxc_h2_ws immediates are
; patched at zx0_compress entry.
; Return: HL = &hashHead[hash].
; Clobbers: A, BC.  Preserves DE (zxc_find_best keeps pos there).
; ════════════════════════════════════════════════════════════════════
zxc_hash2:
zxc_h2_src:
        ld      bc, 0                   ; SMC: src base, patched at entry
        add     hl, bc                  ; HL = &src[i]
        ld      a, (hl)                 ; A = src[i]
        inc     hl
        ld      c, (hl)                 ; C = src[i+1] (don't-care at srclen-1)
        ld      h, zxc_tab31lo >> 8     ; tables are page-aligned
        ld      l, a                    ; HL = &tab31lo[src[i]]
        ld      a, (hl)                 ; A = (src[i]*31) & &FF
        xor     c                       ; A = hash low byte
        inc     h                       ; HL = &tab31hi[src[i]]
        ld      h, (hl)                 ; H = hash bit 8 (0 or 1)
        ld      l, a                    ; HL = 9-bit hash
        add     hl, hl                  ; HL = hash * 2
zxc_h2_ws:
        ld      bc, 0                   ; SMC: hashHead base, patched at entry
        add     hl, bc                  ; HL = &hashHead[hash]
        ret


; ════════════════════════════════════════════════════════════════════
; Bit-writer routines.  Go: bitWriter methods (compress.go lines 206–240).
;
; All bit-writer state is resident in the shadow registers (see the
; init block in zx0_compress): D'E' = out_ptr, H'L' = bit_byte_ptr,
; C' = bit_mask, B' = backtrack.  Each routine banks in with EXX and
; clobbers only A and flags in the primary bank, so callers (the
; Elias-gamma loops in particular) keep their values across calls.
; ════════════════════════════════════════════════════════════════════

; ── zxc_bit0 — write bit 0 ───────────────────────────────────────────
; Go: bitWriter.writeBit(0).  Clobbers: A.
zxc_bit0:
        exx
        ld      a, b
        or      a
        jp      nz, zxc_b0_bt
        ld      a, c
        or      a
        jp      z, zxc_b0_new
        srl     c                       ; advance mask; holder keeps 0 here
        exx
        ret
zxc_b0_new:
        ; Open a new bit-holder byte (Go: append 0, mask = 128 >> 1).
        ld      h, d
        ld      l, e                    ; bit_byte_ptr = out_ptr
        ld      (hl), 0
        inc     de
        ld      c, 64
        exx
        ret
zxc_b0_bt:
        ld      b, 0                    ; consume backtrack; bit=0 writes nothing
        exx
        ret


; ── zxc_bit1 — write bit 1 ───────────────────────────────────────────
; Go: bitWriter.writeBit(1).  Clobbers: A.
zxc_bit1:
        exx
        ld      a, b
        or      a
        jp      nz, zxc_b1_bt
        ld      a, c
        or      a
        jp      z, zxc_b1_new
        or      (hl)                    ; set the mask bit in the holder
        ld      (hl), a
        srl     c
        exx
        ret
zxc_b1_new:
        ; Open a new bit-holder byte with bit 7 already set.
        ld      h, d
        ld      l, e
        ld      (hl), 128
        inc     de
        ld      c, 64
        exx
        ret
zxc_b1_bt:
        ; Backtrack: OR 1 into the last written byte (out_ptr - 1).
        ; The holder pointer in HL must survive (the open holder's mask
        ; bits may still be pending), so the work happens in DE/HL
        ; swapped and swapped back.
        ld      b, 0
        ex      de, hl                  ; HL = out_ptr, DE = bit_byte_ptr
        dec     hl
        ld      a, (hl)
        or      1
        ld      (hl), a
        inc     hl
        ex      de, hl                  ; restore out_ptr / bit_byte_ptr
        exx
        ret


; ── zxc_write_bbt — writeByteWithBacktrack(A) ────────────────────────
; Go: bitWriter.writeByteWithBacktrack.  Clobbers: nothing (A kept).
zxc_write_bbt:
        exx
        ld      (de), a                 ; append byte
        inc     de
        ld      b, 1                    ; next bit lands in this byte's LSB
        exx
        ret


; ── zxc_bitpair — emit direction bit 0 then value bit A ──────────────
; Go: the writeBit(0) + writeBit(v) pair inside writeEliasGamma /
; writeEliasGammaInv.  One EXX round serves both bits, and the value
; bit skips the backtrack test: after the direction bit, backtrack is
; always clear (it was either already clear or consumed by that bit).
; Entry: A = 0 emits (0,0); A != 0 emits (0,1).  Clobbers: A.
zxc_bitpair:
        exx
        push    af                      ; park the value bit
        ; ── direction bit 0 ──
        ld      a, b
        or      a
        jp      nz, zxc_bp0_bt
        ld      a, c
        or      a
        jp      z, zxc_bp0_new
        srl     c
        jp      zxc_bp_second
zxc_bp0_new:
        ld      h, d
        ld      l, e                    ; open a new holder
        ld      (hl), 0
        inc     de
        ld      c, 64
        jp      zxc_bp_second
zxc_bp0_bt:
        ld      b, 0                    ; consume backtrack; 0 writes nothing
zxc_bp_second:
        ; ── value bit (backtrack known clear) ──
        pop     af
        or      a
        jp      z, zxc_bp_v0
        ld      a, c
        or      a
        jp      z, zxc_bp1_new
        or      (hl)                    ; set the mask bit in the holder
        ld      (hl), a
        srl     c
        exx
        ret
zxc_bp1_new:
        ld      h, d
        ld      l, e
        ld      (hl), 128
        inc     de
        ld      c, 64
        exx
        ret
zxc_bp_v0:
        ld      a, c
        or      a
        jp      z, zxc_bp0n2
        srl     c
        exx
        ret
zxc_bp0n2:
        ld      h, d
        ld      l, e
        ld      (hl), 0
        inc     de
        ld      c, 64
        exx
        ret


; ════════════════════════════════════════════════════════════════════
; zxc_eg — writeEliasGamma(HL).
; Go: bitWriter.writeEliasGamma (compress.go lines 251–267).
; HL = v >= 1.  Clobbers: A, HL, DE, BC.
; ════════════════════════════════════════════════════════════════════
zxc_eg:
        ; Find p = highest power of two <= v.  v in DE; p in BC.
        ld      d, h
        ld      e, l                    ; DE = v
        ld      bc, 1
zxc_eg_find:
        ld      h, b
        ld      l, c
        add     hl, hl                  ; HL = 2*BC
        jp      c, zxc_eg_found         ; overflow → 2*BC > v
        ; Compare HL vs DE (v) using CP for correct 16-bit ordering.
        ld      a, h
        cp      d                       ; compare high bytes
        jp      c, zxc_eg_adv           ; H < D → HL < DE (2*BC < v)
        jp      nz, zxc_eg_found        ; H > D → HL > DE (2*BC > v)
        ; High bytes equal: compare low bytes.
        ld      a, l
        cp      e                       ; compare low bytes
        jp      c, zxc_eg_adv           ; L < E → HL < DE (2*BC < v)
        jp      nz, zxc_eg_found        ; L > E → HL > DE (2*BC > v)
        ; HL == DE (2*BC == v): p = v is exact power, set p to this value.
zxc_eg_adv2:
        ld      b, h
        ld      c, l
        jp      zxc_eg_found            ; 2*BC == v: p = v, loop exits
zxc_eg_adv:
        ld      b, h
        ld      c, l
        jp      zxc_eg_find             ; 2*BC < v: keep doubling
zxc_eg_found:
        srl     b
        rr      c                       ; BC = p>>1
zxc_eg_bits:
        ld      a, b
        or      c
        jp      z, zxc_eg_term
        ld      a, c
        and     e
        ld      h, a
        ld      a, b
        and     d
        or      h                       ; A = v & p (any nonzero = bit 1)
        call    zxc_bitpair             ; emit (0, value bit); A-only clobber
        srl     b
        rr      c
        jp      zxc_eg_bits             ; check loop termination at top
zxc_eg_term:
        jp      zxc_bit1


; ════════════════════════════════════════════════════════════════════
; zxc_eg_inv — writeEliasGammaInv(HL).
; Go: bitWriter.writeEliasGammaInv (compress.go lines 277–291).
; Value bits inverted.  HL = v >= 1.  Clobbers: A, HL, DE, BC.
; ════════════════════════════════════════════════════════════════════
zxc_eg_inv:
        ld      d, h
        ld      e, l
        ld      bc, 1
zxc_egi_find:
        ld      h, b
        ld      l, c
        add     hl, hl                  ; HL = 2*BC
        jp      c, zxc_egi_found        ; overflow → 2*BC > v
        ; Compare HL vs DE (v) using CP for correct 16-bit ordering.
        ld      a, h
        cp      d                       ; compare high bytes
        jp      c, zxc_egi_adv          ; H < D → HL < DE (2*BC < v)
        jp      nz, zxc_egi_found       ; H > D → HL > DE (2*BC > v)
        ; High bytes equal: compare low bytes.
        ld      a, l
        cp      e                       ; compare low bytes
        jp      c, zxc_egi_adv          ; L < E → HL < DE (2*BC < v)
        jp      nz, zxc_egi_found       ; L > E → HL > DE (2*BC > v)
        ; HL == DE (2*BC == v): p = v is exact power.
zxc_egi_adv2:
        ld      b, h
        ld      c, l
        jp      zxc_egi_found           ; 2*BC == v: p = v, loop exits
zxc_egi_adv:
        ld      b, h
        ld      c, l
        jp      zxc_egi_find            ; 2*BC < v: keep doubling
zxc_egi_found:
        srl     b
        rr      c
zxc_egi_bits:
        ld      a, b
        or      c
        jp      z, zxc_egi_term
        ; Inverted bit: (v & BC)==0 → 1, else → 0.
        ld      a, c
        and     e
        ld      h, a
        ld      a, b
        and     d
        or      h
        cp      1
        sbc     a, a                    ; A = (v&p)==0 ? &FF : 0 (inverted)
        call    zxc_bitpair             ; emit (0, value bit); A-only clobber
        srl     b
        rr      c
        jp      zxc_egi_bits            ; check loop termination at top
zxc_egi_term:
        jp      zxc_bit1


; ════════════════════════════════════════════════════════════════════
; Module-level variables.  Initialised at zx0_compress entry.
; ════════════════════════════════════════════════════════════════════
zxc_ws_base:     defw    0       ; ws_base (IX on entry)
zxc_src:         defw    0       ; src block pointer
zxc_dst:         defw    0       ; dst buffer pointer
zxc_src_len:     defw    0       ; src block length
zxc_pos:         defw    0       ; current input position (index)
zxc_litstart:    defw    0       ; litStart (pending literal run start index)
zxc_prevlit:     defb    0       ; prevWasLit flag
zxc_lastoff:     defw    0       ; lastOffset (ZX0 state)
zxc_best_len:    defw    0       ; best match length scratch
zxc_best_off:    defw    0       ; best match offset scratch

; ════════════════════════════════════════════════════════════════════
; Hash lookup tables (assemble-time).  zxc_tab31lo[x] = (x*31) & &FF;
; zxc_tab31hi[x] = ((x*31) >> 8) & 1 — the bit kept by the 9-bit
; HASH_MASK.  Page-aligned and adjacent so zxc_hash2 switches tables
; with INC H.
; ════════════════════════════════════════════════════════════════════
        align   256
zxc_tab31lo:
        for 256, defb ((FOR*31) & &FF)
zxc_tab31hi:
        for 256, defb (((FOR*31) >> 8) & 1)
        assert  zxc_tab31hi == zxc_tab31lo + 256
