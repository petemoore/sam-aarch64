# `tools/sha256-unroll-gen` — SHA-256 unrolled-round-block generator

`gen_sha256_unrolled.py` emits the 8x circular-renamed SHA-256 round block that
is committed verbatim inside `src/netboot/sha256.asm` (the `else`,
non-`NETBOOT_TLS_CLIENT`, max-speed branch of the 64-rounds section).

Circular renaming unrolls 8 rounds into a group where the working vars a..h
never physically move — each of the 8 phases hard-codes which `wv_` slot is
a..h, so only the two written words (new a, new e) are stored per phase. That
removes the rolled loop's 28-byte `lddr` shuffle (~37k T-states/block of pure
data movement), dropping per-block compress from 418,843 to 377,371 T-states.

Because the slots are compile-time constants in this branch, the phases also
inline `Ch(e,f,g)` and `Maj(a,b,c)` with their operand addresses baked in,
instead of calling the shared `sha_ch`/`sha_maj` subroutines. The subroutines
take their three source pointers at runtime and so pay a 19T `(ix+d)`/`(iy+d)`
indexed access per operand byte plus call/`djnz` overhead; the inline form loads
each operand with a plain `ld a,(nnnn)` and does the boolean in A with H,L (and
D, for Maj) as scratch. That drops per-block compress from 377,371 to 346,267
T-states. (The shared subroutines remain — the rolled `NETBOOT_TLS_CLIENT` path
still calls them.)

The round then accumulates `T1 = h + S1(e) + Ch + K[t] + W[t]` and
`T2 = S0(a) + Maj` in the register quad `B,C,D,E` (B=MSB..E=LSB, big-endian)
rather than memory-to-memory: each term folds in with a `BCDE += memory` carry
chain, so the four intermediate write-backs of the T1 chain and both of the T2
chain are gone — only the final `new e`/`new a` words touch memory. The sigma
subroutines own B,C,D,E to rotate, so each sigma is computed to `sha_tmpa` first
and the accumulator is loaded after it returns; `Ch` is written with A,H,L only
so it can run while the T1 accumulator sits in B,C,D,E, and `Maj` parks its
result in `sha_majs` while S0 occupies `sha_tmpa`. That drops per-block compress
from 342,747 to 315,355 T-states (and, as a side effect of the more compact
sequence, shrinks the emitted block too).

The block is generated rather than written with pyz80's `EQU FOR` because that
loop's rewind desyncs when a MACRO expands inside its body, corrupting the
assembly of earlier code; emitting flat, macro-free text sidesteps the bug.

The round logic's source of truth is this script — do **not** hand-edit the
inline phases in `sha256.asm`. To change them, edit the script, regenerate
(`python3 gen_sha256_unrolled.py`), and re-splice the output into the `else`
branch. `regen_guard_test.go` re-runs the generator and asserts the inline copy
still matches byte-for-byte, so the two can never silently drift. The NIST KAT
in `tools/netboot-oracle/z80` is the correctness guardrail on the emitted code.
