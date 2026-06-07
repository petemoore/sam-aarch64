# `reference/zx0/` — ZX0 compressor (vendored)

## Provenance

- **Source**: <https://github.com/einar-saukas/ZX0>
- **Commit**: `ecde3a2ae05061fe06469ed46df81a33b7de7d86`
- **License**: BSD 3-Clause (see `LICENSE`)
- **Vendored**: 2026-06-10 for i60(a) — harness-measured ZX0 decode speed

## Contents

| Path | Description |
|------|-------------|
| `src/zx0.c`, `src/zx0.h` | Main compressor entry point |
| `src/compress.c` | Optimal-parse compressor |
| `src/optimize.c` | Optimal-parse dynamic programming |
| `src/memory.c` | Memory allocation helpers |
| `src/dzx0.c` | Standalone decompressor (host-side, for verification) |
| `src/Makefile` | Upstream build rules (not used here; we compile directly) |
| `z80/dzx0_standard.asm` | Z80 "Standard" decoder, 68 bytes, ZX0 bitstream |
| `z80/dzx0_turbo.asm` | Z80 "Turbo" decoder, 126 bytes, ~21% faster |
| `z80/dzx0_mega.asm` | Z80 "Mega" decoder, 673 bytes, ~28% faster (vendored 2026-06-11 for i67, same commit) |
| `z80/dzx0_fast.asm` | Speed-optimized decoder by spke, 187 bytes, ~turbo+5% (vendored 2026-06-11 for i67, same commit; carries its own zlib-style licence notice) |

## Building the compressor

```
gcc -O2 -o /tmp/zx0 src/compress.c src/memory.c src/optimize.c src/zx0.c
```

The `tools/z80-test-harness-go/testdata/` directory contains pyz80 ports of the
three Z80 decoders (syntax-only changes; upstream logic unchanged).

## Usage

```
/tmp/zx0 input.bin output.zx0
```
