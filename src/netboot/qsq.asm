; qsq.asm — the quarter-square 8×8→16 multiply (i102), shared between poly1305.asm
; and x25519.asm.  The Z80 has no multiply instruction, so every byte-radix
; big-integer routine in the suite needs a fast 8-bit multiply; this is it.
;
; mul8 computes a·b via the quarter-square identity
;   a·b = floor((a+b)^2/4) − floor((a−b)^2/4) = QSQ[a+b] − QSQ[|a−b|],
; exact because a+b and a−b share parity (their sum 2a is even), so each floor is
; exact.  Two table lookups + a 16-bit subtract replace the 8-iteration shift-add
; loop — the hot inner multiply of both the Poly1305 MAC and the Curve25519 field
; layer.  qsq_init builds the table once (a pure 16-bit running recurrence, no
; multiply); callers run it before the first mul8.
;
; This is a pure-leaf include (no org of its own): the including file sets the org
; under its own standalone guard, and mul8/qsq_init land in that file's address
; space.  poly1305.asm and x25519.asm each `include "qsq.asm"` in their code
; section; both reach the same single copy.  When a composite includes both bricks
; (the i88 6a TLS client), one of the two includes is flag-suppressed (-D
; NETBOOT_TLS_CLIENT) so qsq is emitted exactly once — see
; docs/plans/z80-tls-client-port-plan.md Part 0.
;
; The two in-file copies (poly1305's ported from x25519's, i102 #319→#320) were
; byte-identical; this is the DRY extraction.  Documented opcodes only.

; qsq_table — the quarter-square multiply lookup, QSQ[n] = floor(n^2/4) for
; n = 0..510 (511 little-endian 16-bit entries, 1022 bytes).  Pure regenerable
; scratch (qsq_init rebuilds it via a running recurrence, no multiply), so it lives
; in RAM above the module image — costing zero file/ROM bytes — rather than as
; emitted data.  The standalone bricks are tiny, so &C000 is safely above their
; images.  The i88 6a TLS-client composite is large (its SF_FLIGHT buffer alone
; straddles &C000), so under NETBOOT_TLS_CLIENT the table relocates to &FB00 —
; above the composite image and below the top of RAM (the host test asserts the
; image stays clear of it: tls_client_end < qsq_table).
                if defined(NETBOOT_TLS_CLIENT)
qsq_table:      equ &FB00
                else
qsq_table:      equ &C000
                endif

; ---------------------------------------------------------------------------
; mul8 — HL = A * E (8x8 -> 16) via the quarter-square identity
;   a*b = floor((a+b)^2/4) - floor((a-b)^2/4) = QSQ[a+b] - QSQ[|a-b|].
; qsq_table must already be built (the caller runs qsq_init once at entry).
; In: A, E = the two byte operands (D is ignored). Out: HL = A*E.
; Clobbers A, BC, DE, HL. (Every caller reloads BC/DE after the call, so mul8
; does not preserve them — that saves the push/pop on the hot inner multiply.)
; ---------------------------------------------------------------------------
mul8:
                ld      b, a                    ; B = multiplier
                ld      c, e                    ; C = multiplicand
                ; QSQ[a+b]: build the sum index (0..510) in HL, fetch the entry.
                add     a, e                    ; A = (a+b) & 0xFF, CF = bit8
                ld      l, a
                ld      h, 0
                rl      h                       ; H = bit8 -> HL = a+b (0..510)
                add     hl, hl                  ; 2*(a+b) for the 16-bit stride
                ld      de, qsq_table
                add     hl, de
                ld      e, (hl)
                inc     hl
                ld      d, (hl)                 ; DE = QSQ[a+b]
                push    de                      ; save QSQ[a+b]
                ; QSQ[|a-b|]: build the |difference| index, fetch the entry.
                ld      a, b
                sub     c                       ; A = a-b, CF if a<b
                jr      nc, mul8_diff_pos
                neg                             ; A = b-a (absolute value)
mul8_diff_pos:
                ld      l, a
                ld      h, 0
                add     hl, hl                  ; 2*|a-b|
                ld      de, qsq_table
                add     hl, de
                ld      e, (hl)
                inc     hl
                ld      d, (hl)                 ; DE = QSQ[|a-b|]
                pop     hl                      ; HL = QSQ[a+b]
                or      a                       ; clear carry
                sbc     hl, de                  ; HL = QSQ[a+b] - QSQ[|a-b|] = a*b
                ret

; ---------------------------------------------------------------------------
; qsq_init — build qsq_table: QSQ[n] = floor(n^2/4) for n = 0..510, via the
; recurrence QSQ[n] = QSQ[n-2] + (n-1) (exact, pure 16-bit adds, no multiply).
; Runs once before any mul8. Clobbers A, BC, DE, HL.
; ---------------------------------------------------------------------------
qsq_init:
                ld      hl, qsq_table
                ld      (hl), 0                 ; QSQ[0] = 0
                inc     hl
                ld      (hl), 0
                inc     hl
                ld      (hl), 0                 ; QSQ[1] = 0
                inc     hl
                ld      (hl), 0
                inc     hl                      ; HL -> &QSQ[2]
                ld      bc, 1                   ; BC = (n-1); n starts at 2
qsq_init_loop:
                dec     hl
                dec     hl
                dec     hl
                dec     hl                      ; HL -> &QSQ[n-2]
                ld      e, (hl)
                inc     hl
                ld      d, (hl)                 ; DE = QSQ[n-2]
                inc     hl
                inc     hl
                inc     hl                      ; HL -> &QSQ[n]
                ex      de, hl                  ; HL = QSQ[n-2], DE = &QSQ[n]
                add     hl, bc                  ; HL = QSQ[n-2] + (n-1) = QSQ[n]
                ex      de, hl                  ; DE = QSQ[n], HL = &QSQ[n]
                ld      (hl), e
                inc     hl
                ld      (hl), d
                inc     hl                      ; HL -> &QSQ[n+1]
                inc     bc                      ; advance (n-1)
                ; stop once (n-1) reaches 510 (i.e. QSQ[510] just written): B=1,C=254
                ld      a, b
                cp      1
                jr      nz, qsq_init_loop
                ld      a, c
                cp      254
                jr      nz, qsq_init_loop
                ret
