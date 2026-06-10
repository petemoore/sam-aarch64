# `src/slots/` — per-operand-kind slot encoders

Each file is the Z80 port of one slot-encoder family from the Go authority
`tools/aarch64enc/slots_*.go` (the file headers cite the exact Go
function). They are `include`d by `src/assembler.asm` as part of its
load-bearing include order (see `src/README.md`); pyz80's flat symbol
space means every local label is prefixed with its routine name.

| File | Encodes |
|------|---------|
| `xreg.asm` | Xreg / Wreg / XregOrSp / WregOrSp register slots |
| `imm_small.asm` | Imm5/Imm6 small immediates, condition codes, shift amounts |
| `imm12_shifted.asm` | 12-bit optionally-shifted arithmetic immediates |
| `imm16_shifted.asm` | 16-bit mov-wide immediates (movz/movk/movn) |
| `logical_imm.asm` | bitmask (logical) immediates |
| `adrp_imm.asm` | adrp page-immediate + `:lo12:` companions |
| `branch_imm.asm` | PC-relative branch/conditional/test offsets |
| `shifted_reg.asm` | shifted-register operands |
| `extended_reg.asm` / `extend_op.asm` | extended-register operands |
| `mem.asm` | the memory addressing shapes |
