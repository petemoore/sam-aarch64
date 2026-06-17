#!/usr/bin/env python3
"""
gen_sha256_unrolled.py — generate the 8x-unrolled, circular-renamed SHA-256
round block for src/netboot/sha256.asm (the firmware / standalone "max speed"
build, NOT the size-critical NETBOOT_TLS_CLIENT composite).

WHY THIS EXISTS
---------------
The compact rolled round loop physically shifts a..h every round with a 28-byte
`lddr` (~37k T-states/block of pure data movement). Circular renaming kills that:
unroll 8 rounds into a "group" where each phase hard-codes which of the 8 wv_
slots is a,b,..,h, so a..h never move — only the two written words (new a, new e)
are stored. After 8 phases the slots return to their start positions, so one
group repeats 8 times for the 64 rounds.

pyz80's assemble-time `EQU FOR` loop CANNOT be used for this: its rewind
mechanism (line tracking via global_currentfile) desyncs when a MACRO expands
inside the FOR body, corrupting the assembly of *earlier* code (observed: K-table
bytes overwriting sha256_init). So instead we generate the 8 flat phase bodies
here in Python and emit ordinary mnemonics — pyz80 then assembles plain text with
no FOR and no macros-in-FOR, sidestepping the bug entirely.

REGISTER-ACCUMULATOR ROUND (the i102r architecture)
---------------------------------------------------
The standard round is
  T1 = h + S1(e) + Ch(e,f,g) + K[t] + W[t]
  T2 = S0(a) + Maj(a,b,c)
  new e = old d + T1 ;  new a = T1 + T2.
The five T1 terms and the two T2 terms are each a 32-bit add. Done memory-to-
memory (the earlier shape) every add is a ~124T carry chain that writes its 4-byte
result back to memory only to be re-read by the next add. Here the running T1 (then
T2) sum lives in the register quad B,C,D,E (B=byte0/MSB .. E=byte3/LSB, big-endian,
matching memory). Each term is folded in with a `BCDE += (memory word)` carry chain
(~92T, no write-back), so the four intermediate stores of the T1 chain and both of
the T2 chain are gone; only the final values touch memory. The sigma functions
(sha_bigsigma0/1) still run as shared subroutines writing sha_tmpa (they own B,C,D,E
to rotate), so the order is: compute the sigma to sha_tmpa FIRST, then load BCDE
from it and fold the remaining terms in — the accumulator never has to survive a
sigma call.

Ch and Maj are inlined with their operand slots baked in as assemble-time
constants (the shared sha_ch/sha_maj subroutines pay a 19T (ix+d)/(iy+d) indexed
access per operand byte plus call/djnz overhead; the inline form loads each
operand with a plain `ld a,(nnnn)`). Ch is written to use ONLY A,H,L so it can run
while the T1 accumulator sits in B,C,D,E; Maj runs while S0 occupies sha_tmpa, so
it is free to clobber any register and parks its result in sha_majs.

RENAMING MATH (verified byte-exact by the NIST KAT):
  At phase p (0..7), logical role r (a=0,b=1,c=2,d=3,e=4,f=5,g=6,h=7) lives in
  wv slot (r - p) mod 8, i.e. address  wv_abcdefgh + 4*((r - p) & 7).
  new e overwrites old d (role d at phase p == role e at phase p+1), new a
  overwrites old h. All reads of a..h happen before the two writes.

The emitted block is committed verbatim inside src/netboot/sha256.asm's
64-rounds section, in the `else` (non-NETBOOT_TLS_CLIENT) branch. The
regen-guard test (regen_guard_test.go) re-runs this generator and asserts the
committed inline block still matches it byte-for-byte. To change the round logic,
edit this script and regenerate — never hand-edit the inline phases.

USAGE:  python3 gen_sha256_unrolled.py   # prints the block to stdout
"""

WV = "wv_abcdefgh"
IND = "                "  # 16-space body indent, matching the surrounding file

def slot(role, p):
    return f"{WV} + 4*(({role} - {p}) & 7)"

def emit(*lines):
    return [IND + l for l in lines]

# ld_bcde_from — load the big-endian word at `src` into B,C,D,E (B=MSB..E=LSB).
def ld_bcde_from(src):
    return emit(
        f"ld      hl, {src}",
        "ld      b, (hl)", "inc     hl",
        "ld      c, (hl)", "inc     hl",
        "ld      d, (hl)", "inc     hl",
        "ld      e, (hl)",
    )

# st_bcde — store B,C,D,E to the big-endian word at `dst`.
def st_bcde(dst):
    return emit(
        f"ld      hl, {dst}",
        "ld      (hl), b", "inc     hl",
        "ld      (hl), c", "inc     hl",
        "ld      (hl), d", "inc     hl",
        "ld      (hl), e",
    )

# bcde_add — BCDE += (big-endian word at `src`). HL walks src+3 (LSB) down to
# src+0 (MSB); the carry rides LSB->MSB. The intervening ld/dec do not touch the
# carry flag, so the chain is correct.
def bcde_add(src):
    return emit(
        f"ld      hl, {src}+3",
        "or      a",
        "ld      a, e", "adc     a, (hl)", "ld      e, a", "dec     hl",
        "ld      a, d", "adc     a, (hl)", "ld      d, a", "dec     hl",
        "ld      a, c", "adc     a, (hl)", "ld      c, a", "dec     hl",
        "ld      a, b", "adc     a, (hl)", "ld      b, a",
    )

# bcde_add_ptr — BCDE += (word whose LSB address is held at `ptr_expr`). The
# K/W pointers (sha_kt / sha_wptr) already track the word LSB (+3).
def bcde_add_ptr(ptr_expr):
    return emit(
        f"ld      hl, {ptr_expr}",
        "or      a",
        "ld      a, e", "adc     a, (hl)", "ld      e, a", "dec     hl",
        "ld      a, d", "adc     a, (hl)", "ld      d, a", "dec     hl",
        "ld      a, c", "adc     a, (hl)", "ld      c, a", "dec     hl",
        "ld      a, b", "adc     a, (hl)", "ld      b, a",
    )

# mem_add_bcde — (big-endian word at `dst`) += BCDE, in place. Uses HL + A only
# (BCDE stays intact as the addend), so the caller can keep using BCDE after.
def mem_add_bcde(dst):
    return emit(
        f"ld      hl, {dst}+3",
        "or      a",
        "ld      a, (hl)", "adc     a, e", "ld      (hl), a", "dec     hl",
        "ld      a, (hl)", "adc     a, d", "ld      (hl), a", "dec     hl",
        "ld      a, (hl)", "adc     a, c", "ld      (hl), a", "dec     hl",
        "ld      a, (hl)", "adc     a, b", "ld      (hl), a",
    )

# copy4 — 4-byte copy, HL=src, DE=dst, walk up. (Clobbers A,DE,HL — used only
# where BCDE is free.)
def copy4(dst, src):
    out = emit(f"ld      hl, {src}", f"ld      de, {dst}")
    for _ in range(4):
        out += emit("ld      a, (hl)", "ld      (de), a",
                    "inc     hl", "inc     de")
    return out

# ch_inline — Ch(e,f,g) = g ^ (e & (f ^ g)), written to sha_tmpa using ONLY
# A,H,L so the T1 accumulator can sit in B,C,D,E across it. Per byte: H:=f,
# L:=g, A walks f^g -> e&(f^g) -> ^g.
def ch_inline(E, F, G):
    out = []
    for i in range(4):
        out += emit(
            f"ld      a, ({F} + {i})",   # f
            "ld      h, a",
            f"ld      a, ({G} + {i})",   # g
            "ld      l, a",
            "xor     h",                  # f ^ g
            "ld      h, a",               # H := f^g
            f"ld      a, ({E} + {i})",   # e
            "and     h",                  # e & (f^g)
            "xor     l",                  # ^ g
            f"ld      (sha_tmpa + {i}), a",
        )
    return out

# maj_inline — Maj(a,b,c) = (a & b) | (c & (a ^ b)), written to sha_majs. Runs
# while S0 occupies sha_tmpa and BCDE is dead, so it freely uses A,H,L,D.
def maj_inline(A, B, C):
    out = []
    for i in range(4):
        out += emit(
            f"ld      a, ({A} + {i})",   # a
            "ld      h, a",
            f"ld      a, ({B} + {i})",   # b
            "ld      l, a",
            "xor     h",                  # a ^ b
            "ld      d, a",
            f"ld      a, ({C} + {i})",   # c
            "and     d",                  # c & (a^b)
            "ld      d, a",
            "ld      a, h",               # a
            "and     l",                  # a & b
            "or      d",                  # (a&b) | (c&(a^b))
            f"ld      (sha_majs + {i}), a",
        )
    return out

def advance_wk():
    return emit(
        "ld      hl, (sha_wptr)", "inc     hl", "inc     hl",
        "inc     hl", "inc     hl", "ld      (sha_wptr), hl",
        "ld      hl, (sha_kt)", "inc     hl", "inc     hl",
        "inc     hl", "inc     hl", "ld      (sha_kt), hl",
    )

def phase_real(p):
    A, B, C, D, E, F, G, H = (slot(r, p) for r in range(8))
    L = emit(f"; ---- phase {p} ----")
    # --- T1 = h + S1(e) + Ch(e,f,g) + K[t] + W[t], accumulated in BCDE -------
    L += emit(f"ld      hl, {E}", "call    sha_bigsigma1")  # sha_tmpa = S1(e)
    L += ld_bcde_from("sha_tmpa")                            # BCDE = S1(e)
    L += ch_inline(E, F, G)                                  # sha_tmpa = Ch (A,H,L only)
    L += bcde_add("sha_tmpa")                                # BCDE += Ch
    L += bcde_add_ptr("(sha_kt)")                            # BCDE += K[t]
    L += bcde_add_ptr("(sha_wptr)")                          # BCDE += W[t]
    L += bcde_add(H)                                         # BCDE += h  -> T1
    # new e = old d + T1 (writes the slot of role d); BCDE (T1) stays intact.
    L += mem_add_bcde(slot(3, p))
    # stash T1 for new a, then build T2 = S0(a) + Maj(a,b,c) in BCDE.
    L += st_bcde("sha_t1")
    L += emit(f"ld      hl, {A}", "call    sha_bigsigma0")  # sha_tmpa = S0(a)
    L += maj_inline(A, B, C)                                 # sha_majs = Maj
    L += ld_bcde_from("sha_tmpa")                            # BCDE = S0(a)
    L += bcde_add("sha_majs")                                # BCDE += Maj -> T2
    # new a = T1 + T2: fold T2 into the stashed T1, then copy into role-h slot.
    L += mem_add_bcde("sha_t1")                              # sha_t1 = T1 + T2
    L += copy4(slot(7, p), "sha_t1")                         # new a -> slot of role h
    L += advance_wk()
    return L

def main():
    out = emit(
        "; 8 groups of 8 phases. a..h NEVER move; each phase",
        "; hard-codes which wv_ slot is a..h. GENERATED by",
        "; tools/sha256-unroll-gen/gen_sha256_unrolled.py and",
        "; guarded by its regen test — DO NOT hand-edit the",
        "; phases; change the generator and regenerate.",
        "ld      a, 8                    ; 8 groups",
        "ld      (sha_round_ctr), a",
    )
    out.append("sha_round_group:")
    for p in range(8):
        out += phase_real(p)
    out += emit(
        "ld      a, (sha_round_ctr)",
        "dec     a",
        "ld      (sha_round_ctr), a",
        "jp      nz, sha_round_group",
    )
    print("\n".join(out))

if __name__ == "__main__":
    main()
