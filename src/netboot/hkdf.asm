; hkdf.asm — HKDF (RFC 5869) over HMAC-SHA256, built on hmac_sha256.asm.
;
; HKDF is the TLS 1.3 key schedule (RFC 8446 §7.1): every traffic/handshake
; secret is an HKDF-Expand-Label off an HKDF-Extract of the ECDH shared secret.
; This file is the reusable two-step core:
;
;   hkdf_extract: PRK = HMAC-SHA256(salt, IKM)  — one HMAC (a salt of zero length
;                 means HMAC's all-zero block key, == RFC's "HashLen zeros" salt).
;   hkdf_expand:  OKM = T(1) || T(2) || … truncated to L, where T(0) = "" and
;                 T(i) = HMAC-SHA256(PRK, T(i-1) || info || byte(i)), i = 1..N,
;                 N = ceil(L / 32).  byte(i) is a single octet (N <= 255).
;
; It adds no new hashing arithmetic — it is orchestration over hmac_sha256.asm
; (which is itself orchestration over sha256.asm), so it inherits their
; verification. It is the next i88 (on-SAM TLS) building block above HMAC; the
; TLS-specific HKDF-Expand-Label / Derive-Secret wrappers (which only shape the
; `info`) come later.
;
; AUTHORITY / VERIFICATION: host-verifiable in the project's standard way.
; tools/netboot-oracle/z80/hkdf_test.go assembles this file, runs hkdf_extract /
; hkdf_expand under the koron-go/z80 harness over the RFC 5869 Appendix-A test
; vectors (incl. the multi-block-OKM and zero-salt/empty-info cases), and asserts
; the PRK + OKM equal Go's crypto/hkdf.Extract / Expand (Go 1.24+) byte-for-byte.
; There is no Go-side asm authority to port; HKDF is specified by RFC 5869 and the
; reference is Go's stdlib. NOT host-verifiable: nothing here is hardware-gated.

                include "hmac_sha256.asm"   ; -> sha256.asm; org &8000 under
                                            ; NETBOOT_STANDALONE; provides hmac_sha256

HKDF_HASHLEN:   equ 32                  ; SHA-256 output size (the HMAC output / L block)

; ===========================================================================
; Parameters (the caller fills these) + working buffers.  Data first.
; ===========================================================================
HKDF_SALT_PTR:  defs 2                  ; in (extract): salt pointer
HKDF_SALT_LEN:  defs 2                  ; in (extract): salt length
HKDF_IKM_PTR:   defs 2                  ; in (extract): input keying material pointer
HKDF_IKM_LEN:   defs 2                  ; in (extract): IKM length
HKDF_INFO_PTR:  defs 2                  ; in (expand): info/context pointer
HKDF_INFO_LEN:  defs 2                  ; in (expand): info length
HKDF_L:         defs 2                  ; in (expand): desired OKM length in bytes

HKDF_PRK:       defs HKDF_HASHLEN       ; the extracted pseudorandom key (the expand key)
HKDF_T:         defs HKDF_HASHLEN       ; the current T(i) block (and T(i-1) next round)
HKDF_MSG:       defs HKDF_HASHLEN + 256 + 1   ; T(i-1) || info || counter assembly

; expand loop state.
hkx_out:        defs 2                  ; the OKM output pointer
hkx_written:    defs 2                  ; bytes of OKM produced so far
hkx_counter:    defs 1                  ; the T-block counter byte (1..N)
hkx_tlen:       defs 1                  ; length of T(i-1) prepended this round (0 or 32)

; ===========================================================================
; hkdf_extract — PRK = HMAC-SHA256(salt, IKM) into HKDF_PRK.
; In:  HKDF_SALT_* / HKDF_IKM_* set.  Out: 32 bytes at HKDF_PRK.
; Clobbers A, BC, DE, HL, IX + hmac/sha256 scratch.
; ===========================================================================
hkdf_extract:
                ld      hl, (HKDF_SALT_PTR)
                ld      (HMAC_KEY_PTR), hl
                ld      hl, (HKDF_SALT_LEN)
                ld      (HMAC_KEY_LEN), hl
                ld      hl, (HKDF_IKM_PTR)
                ld      (HMAC_MSG_PTR), hl
                ld      hl, (HKDF_IKM_LEN)
                ld      (HMAC_MSG_LEN), hl
                ld      hl, HKDF_PRK
                jp      hmac_sha256             ; PRK -> HKDF_PRK

; ===========================================================================
; hkdf_expand — OKM = HKDF-Expand(HKDF_PRK, info, L) into (HL).
; In:  HL = OKM output pointer; HKDF_PRK set; HKDF_INFO_* / HKDF_L set.
; Out: L bytes at the output pointer.  Clobbers A, BC, DE, HL, IX + scratch.
; ===========================================================================
hkdf_expand:
                ld      (hkx_out), hl
                ld      hl, 0
                ld      (hkx_written), hl
                ld      a, 1
                ld      (hkx_counter), a        ; T(1) first
                xor     a
                ld      (hkx_tlen), a           ; T(0) is empty
hkx_loop:
                ; done when written >= L.
                ld      hl, (hkx_written)
                ld      de, (HKDF_L)
                or      a
                sbc     hl, de                  ; written - L; CF set => written < L
                jr      nc, hkx_done

                call    hkx_build_msg           ; HKDF_MSG = T(prev)||info||ctr; BC = len
                ; T(i) = HMAC-SHA256(PRK, HKDF_MSG) -> HKDF_T
                ld      hl, HKDF_PRK
                ld      (HMAC_KEY_PTR), hl
                ld      hl, HKDF_HASHLEN
                ld      (HMAC_KEY_LEN), hl
                ld      hl, HKDF_MSG
                ld      (HMAC_MSG_PTR), hl
                ld      (HMAC_MSG_LEN), bc
                ld      hl, HKDF_T
                call    hmac_sha256

                ; n = min(32, L - written).
                ld      hl, (HKDF_L)
                ld      de, (hkx_written)
                or      a
                sbc     hl, de                  ; HL = L - written (> 0 here)
                ld      a, h
                or      a
                jr      nz, hkx_clamp           ; >= 256 left -> clamp to 32
                ld      a, l
                cp      HKDF_HASHLEN + 1
                jr      c, hkx_have_n           ; l <= 32 -> n = l
hkx_clamp:
                ld      hl, HKDF_HASHLEN
hkx_have_n:
                ; copy n = HL bytes of HKDF_T to OKM + written.
                ld      b, h
                ld      c, l                    ; BC = n
                push    bc
                ld      hl, (hkx_out)
                ld      de, (hkx_written)
                add     hl, de                  ; HL = OKM + written
                ex      de, hl                  ; DE = dest
                ld      hl, HKDF_T              ; src
                pop     bc                      ; n
                push    bc
                ldir
                ; written += n.
                pop     bc
                ld      hl, (hkx_written)
                add     hl, bc
                ld      (hkx_written), hl
                ; T(i) becomes T(i-1) (length 32); counter++.
                ld      a, HKDF_HASHLEN
                ld      (hkx_tlen), a
                ld      a, (hkx_counter)
                inc     a
                ld      (hkx_counter), a
                jr      hkx_loop
hkx_done:
                ret

; hkx_build_msg — assemble HKDF_MSG = T(i-1)[hkx_tlen] || info[HKDF_INFO_LEN] ||
; counter[1].  Out: BC = total length.  Clobbers A, BC, DE, HL.
hkx_build_msg:
                ld      de, HKDF_MSG            ; running dest
                ; T(i-1): hkx_tlen bytes (0 or 32) from HKDF_T.
                ld      a, (hkx_tlen)
                or      a
                jr      z, hkx_bm_info
                ld      c, a
                ld      b, 0
                ld      hl, HKDF_T
                ldir
hkx_bm_info:
                ; info: HKDF_INFO_LEN bytes from HKDF_INFO_PTR.
                ld      bc, (HKDF_INFO_LEN)
                ld      a, b
                or      c
                jr      z, hkx_bm_ctr
                ld      hl, (HKDF_INFO_PTR)
                ldir
hkx_bm_ctr:
                ; the counter byte.
                ld      a, (hkx_counter)
                ld      (de), a
                inc     de
                ; BC = DE - HKDF_MSG.
                ex      de, hl
                ld      de, HKDF_MSG
                or      a
                sbc     hl, de
                ld      b, h
                ld      c, l
                ret
