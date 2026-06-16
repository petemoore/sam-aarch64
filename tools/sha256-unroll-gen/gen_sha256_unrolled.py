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

RENAMING MATH (verified byte-exact in isolation earlier — the unrolled compress
produced the correct "abc" digest; only the pyz80 EQU FOR *mechanism* failed):
  At phase p (0..7), logical role r (a=0,b=1,c=2,d=3,e=4,f=5,g=6,h=7) lives in
  wv slot (r - p) mod 8, i.e. address  wv_abcdefgh + 4*((r - p) & 7).
  Standard round: T1 = h + S1(e) + Ch(e,f,g) + K + W ; T2 = S0(a) + Maj(a,b,c) ;
  new e = old d + T1  -> written to slot of role d (== role e at phase p+1) ;
  new a = T1 + T2      -> written to slot of role h (== role a at phase p+1).
  (new a overwrites old h, dead after T1=h is read; new e overwrites old d, read
  only to form new e. All reads happen before the two writes.)

The emitted block is committed verbatim inside src/netboot/sha256.asm's
64-rounds section, in the `else` (non-NETBOOT_TLS_CLIENT) branch. The
regen-guard test (regen_guard_test.go) re-runs this generator and asserts the
committed inline block still matches it byte-for-byte, so the inline copy can
never silently drift from the generator. To change the round logic, edit this
script and regenerate — never hand-edit the inline phases.

USAGE:  python3 gen_sha256_unrolled.py   # prints the block to stdout
"""

WV = "wv_abcdefgh"

def slot(role, p):
    return f"{WV} + 4*(({role} - {p}) & 7)"

# add4 / copy4 emit the 4-byte primitives as FLAT instructions (no MACRO call).
# Macro-free is deliberate: pyz80's EQU FOR rewind desyncs when a MACRO expands
# inside a FOR body (it corrupts the assembly of earlier code), which is exactly
# why this block is generated as plain text rather than written with EQU FOR.
def add4(dst_lsb, src_lsb):
    # dst += src, big-endian, HL=dst+3, DE=src+3, walk down (LSB->MSB).
    return [
        f"                ld      hl, {dst_lsb}",
        f"                ld      de, {src_lsb}",
        "                or      a",
        "                ld      a, (de)",
        "                adc     a, (hl)",
        "                ld      (hl), a",
        "                dec     hl",
        "                dec     de",
        "                ld      a, (de)",
        "                adc     a, (hl)",
        "                ld      (hl), a",
        "                dec     hl",
        "                dec     de",
        "                ld      a, (de)",
        "                adc     a, (hl)",
        "                ld      (hl), a",
        "                dec     hl",
        "                dec     de",
        "                ld      a, (de)",
        "                adc     a, (hl)",
        "                ld      (hl), a",
    ]

def copy4(dst, src):
    # 4-byte copy, HL=src, DE=dst, walk up.
    out = [f"                ld      hl, {src}", f"                ld      de, {dst}"]
    for _ in range(4):
        out += ["                ld      a, (hl)", "                ld      (de), a",
                "                inc     hl", "                inc     de"]
    return out

# The K[t]/W[t] adds read a *runtime* pointer (sha_kt / sha_wptr), so they can't
# use the literal-address add4 above. Emit them explicitly: DE holds the pointer
# word loaded from (sha_kt)/(sha_wptr), and the carry walks LSB->MSB as usual.
def add4_ptr(dst_lsb, ptr_expr):
    return [
        f"                ld      de, {ptr_expr}",
        f"                ld      hl, {dst_lsb}",
        "                or      a",
        "                ld      a, (de)", "                adc     a, (hl)", "                ld      (hl), a",
        "                dec     hl", "                dec     de",
        "                ld      a, (de)", "                adc     a, (hl)", "                ld      (hl), a",
        "                dec     hl", "                dec     de",
        "                ld      a, (de)", "                adc     a, (hl)", "                ld      (hl), a",
        "                dec     hl", "                dec     de",
        "                ld      a, (de)", "                adc     a, (hl)", "                ld      (hl), a",
    ]

# ch_inline / maj_inline — Ch(e,f,g) and Maj(a,b,c) emitted FLAT with the slot
# addresses baked in as constants (the unrolled path knows each role's slot at
# assemble time). This beats the shared sha_ch/sha_maj subroutines, which pay a
# 19T (ix+d)/(iy+d) indexed access per operand byte plus the call/djnz overhead,
# because they must take their three source pointers at runtime. Here e/f/g (or
# a/b/c) load with plain `ld a,(nnnn)` (13T) and the boolean is done in A with
# H,L,D as scratch — no index registers, no loop, no call. Result goes to
# sha_tmpa, exactly as the subroutines did, so the following add4 is unchanged.
# (The shared subroutines remain for the rolled NETBOOT_TLS_CLIENT path.)
def ch_inline(E, F, G):
    # Ch(e,f,g) = g ^ (e & (f ^ g)). H := f, L := g; A walks f^g -> e&(f^g) -> ^g.
    out = []
    for i in range(4):
        out += [
            f"                ld      a, ({F} + {i})",   # f
            "                ld      h, a",
            f"                ld      a, ({G} + {i})",   # g
            "                ld      l, a",
            "                xor     h",                  # f ^ g
            "                ld      d, a",
            f"                ld      a, ({E} + {i})",   # e
            "                and     d",                  # e & (f^g)
            "                xor     l",                  # ^ g
            f"                ld      (sha_tmpa + {i}), a",
        ]
    return out

def maj_inline(A, B, C):
    # Maj(a,b,c) = (a & b) | (c & (a ^ b)). H := a, L := b; one load each, no reload.
    out = []
    for i in range(4):
        out += [
            f"                ld      a, ({A} + {i})",   # a
            "                ld      h, a",
            f"                ld      a, ({B} + {i})",   # b
            "                ld      l, a",
            "                xor     h",                  # a ^ b
            "                ld      d, a",
            f"                ld      a, ({C} + {i})",   # c
            "                and     d",                  # c & (a^b)
            "                ld      d, a",
            "                ld      a, h",               # a
            "                and     l",                  # a & b
            "                or      d",                  # (a&b) | (c&(a^b))
            f"                ld      (sha_tmpa + {i}), a",
        ]
    return out

def phase_real(p):
    L = [f"                ; ---- phase {p} ----"]
    A, B, C, D, E, F, G, H = (slot(r, p) for r in range(8))
    L += copy4("sha_t1", H)                                   # T1 = h
    L += [f"                ld      hl, {E}", "                call    sha_bigsigma1"]
    L += add4("sha_t1+3", "sha_tmpa+3")                       # T1 += S1(e)
    L += ch_inline(E, F, G)                                   # sha_tmpa = Ch(e,f,g)
    L += add4("sha_t1+3", "sha_tmpa+3")                       # T1 += Ch
    L += add4_ptr("sha_t1+3", "(sha_kt)")                     # T1 += K[t]  (ptr is LSB)
    L += add4_ptr("sha_t1+3", "(sha_wptr)")                   # T1 += W[t]
    L += [f"                ld      hl, {A}", "                call    sha_bigsigma0"]
    L += copy4("sha_t2", "sha_tmpa")                          # T2 = S0(a)
    L += maj_inline(A, B, C)                                  # sha_tmpa = Maj(a,b,c)
    L += add4("sha_t2+3", "sha_tmpa+3")                       # T2 += Maj
    L += add4(f"{slot(3,p)} + 3", "sha_t1+3")                 # new e = old d + T1 (slot d)
    L += copy4(slot(7, p), "sha_t1")                          # new a = T1 ...
    L += add4(f"{slot(7,p)} + 3", "sha_t2+3")                 #         ... + T2 (slot h)
    # advance W and K pointers by one word (they track the LSB, +4 -> next LSB)
    L += ["                ld      hl, (sha_wptr)", "                inc     hl", "                inc     hl",
          "                inc     hl", "                inc     hl", "                ld      (sha_wptr), hl",
          "                ld      hl, (sha_kt)", "                inc     hl", "                inc     hl",
          "                inc     hl", "                inc     hl", "                ld      (sha_kt), hl"]
    return L

def main():
    out = []
    out.append("                ; 8 groups of 8 phases. a..h NEVER move; each phase")
    out.append("                ; hard-codes which wv_ slot is a..h. GENERATED by")
    out.append("                ; tools/sha256-unroll-gen/gen_sha256_unrolled.py and")
    out.append("                ; guarded by its regen test — DO NOT hand-edit the")
    out.append("                ; phases; change the generator and regenerate.")
    out.append("                ld      a, 8                    ; 8 groups")
    out.append("                ld      (sha_round_ctr), a")
    out.append("sha_round_group:")
    for p in range(8):
        out += phase_real(p)
    out.append("                ld      a, (sha_round_ctr)")
    out.append("                dec     a")
    out.append("                ld      (sha_round_ctr), a")
    out.append("                jp      nz, sha_round_group")
    print("\n".join(out))

if __name__ == "__main__":
    main()
