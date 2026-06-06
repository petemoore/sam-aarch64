# Disassembler round-trip test — design spec

**Strand B, PR 2.** Validates that the Go encoder and decoder are true inverses: assemble source → binary → disassemble → reassemble → same binary. Also promotes `aarch64dec -asm` to the canonical editor-import format.

## Context

The Go disassembler (`tools/aarch64dec/`) landed in PR #93 and is gated by the `disasm` CI job (aarch64dec vs objdump, 100% exact match on the code-only release binary). This PR is the next strand-B step:

```
✅ PR 1 — Go disassembler (aarch64dec, objdump oracle)
← PR 2 — Round-trip test (this spec)
   PR 3 — Z80 port of the disassembler
   PR 4 — Compact .tbn format
   PR 5 — Editor integration
```

## Goals

1. Prove encode→decode→encode is self-consistent for our toolchain (no silent asymmetry between refenc and aarch64dec).
2. Establish `aarch64dec -asm` as the editor-import path: a binary can be disassembled to a `.s` file that `text2bin` can parse and `refenc` can reassemble to identical bytes.
3. Validate that branch labels survive the round-trip (insert-instruction safety — see below).

## The `-asm` flag on `aarch64dec`

Add `-asm` to `tools/aarch64dec/cmd/aarch64dec/main.go`. When set, output is bare assembly — no address prefix, no byte column — one instruction per line:

```
# default (objdump) mode          # -asm mode
   0:  d503201f    nop         →  	nop
   4:  14000004    b  0x10     →  	b	L0
```

### Two-pass output for branch labels

The key motivation: absolute hex branch targets break when a user inserts or removes instructions — every address below the edit shifts. Labels absorb that automatically.

**Pass 1** — scan all 4-byte words, decode each instruction, collect every PC-relative target address from: `b`, `bl`, `b.cond` (all 14 condition variants), `cbz`, `cbnz`, `tbz`, `tbnz`. Sort the collected target addresses; assign `L0`, `L1`, `L2`, ... in ascending address order.

**Pass 2** — emit assembly:
- Before any instruction whose address is in the label set, emit `L<N>:` on its own line.
- Replace each branch's PC-relative operand with its label name.
- Tab-indent every instruction line.

Example output for a short sequence:

```asm
	stp	x29, x30, [sp, #-16]!
	mov	x29, sp
	cbz	x0, L1
	bl	L2
L0:
	adrp	x0, 0x71000
	ldr	x0, [x0, #8]
L1:
	ret
L2:
	add	x0, x1, x2
```

### `adrp` / `add` pairs — deferred

`adrp`/`add` (or `adrp`/`ldr`) pairs reconstruct a full address across two instructions via `:pg:`/`:lo12:` relocations. Resolving these from a raw binary requires data-flow analysis (track the register written by `adrp`, find the consuming instruction). This is valuable for editor import but out of scope for this PR. For now:

- `adrp` emits its absolute page address as a hex literal (`adrp x0, 0x71000`).
- The consuming `add`/`ldr` emits its immediate as a literal (`#8`).

Pair detection is a tracked follow-up (see m7-status.md).

### Out-of-range targets

If a PC-relative target resolves outside `[0, binary_size)` (shouldn't occur in a well-formed intra-binary code-only file, but handled defensively), fall back to the hex address for that operand and emit a `// out-of-range` comment.

### Declined words

Words that `aarch64dec` does not decode (atomics, reserved encodings) already emit `.inst 0xNNNNNNNN` in both modes — this is a valid `text2bin` directive, no special handling needed.

### File header

The `-asm` output begins with a single `.text` line so `text2bin` sees a recognisable section marker. If `text2bin` is verified to accept bare-instruction files without it, the header can be dropped; the implementation should check this first.

## The round-trip script

`tools/run-disasm-roundtrip.sh` — pure Go pipeline, no aarch64 binutils or SimCoupé needed.

```
Input: tests/m6/release/release.s  (vendored, same as oracle comparison)

1. Build: make text2bin refenc aarch64dec
2. Strip data directives + ldr=imm from release.s → tmp/code-only.s
   (identical greps to run-oracle-comparison.sh)
3. text2bin tmp/code-only.s       → tmp/code-only.tbn
4. refenc   tmp/code-only.tbn     → tmp/v1.bin
5. aarch64dec -asm tmp/v1.bin     → tmp/disasm.s
6. text2bin tmp/disasm.s          → tmp/disasm.tbn
7. refenc   tmp/disasm.tbn        → tmp/v2.bin
8. cmp tmp/v1.bin tmp/v2.bin      → PASS / FAIL
```

On failure, report:
- Sizes of v1 and v2.
- The first differing word (byte offset, both hex values).
- The `aarch64dec` rendering of each value — shows whether it's a mis-encode or a mis-decode.

The data-stripping greps are duplicated from `run-oracle-comparison.sh` rather than shared via a helper, matching the existing project pattern of self-contained scripts.

## CI wiring

New GHA job `disasm-roundtrip` in `.github/workflows/ci.yml`, alongside the existing `disasm` job:

- Runs on `ubuntu-latest`.
- No `apt-get install binutils-aarch64-linux-gnu` step (pure Go pipeline).
- Single step: `make ci-disasm-roundtrip`.

New Makefile target:

```make
.PHONY: ci-disasm-roundtrip
ci-disasm-roundtrip:
	./tools/run-disasm-roundtrip.sh
```

Add `disasm-roundtrip` as a required branch-protection check (14th required check; currently 13).

## ROADMAP / tracking updates

The PR updates:
- `docs/ROADMAP.md` Current State block: strand-B PR-2 complete → next is Z80 port.
- `docs/notes/m7-status.md`: flip PR-2 to done, advance next-action pointer to Z80 port.

## What this does NOT cover

- `adrp`/`add` pair detection (tracked follow-up).
- Z80 port of the disassembler (PR 3).
- Compact `.tbn` format (PR 4).
- Editor integration (PR 5).
