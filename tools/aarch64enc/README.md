# `tools/aarch64enc` — the encoding authority

Go library: the aarch64 instruction encoder the Z80 encoder is ported
from. The form table (`types.go`, `encode.go`), per-slot operand encoders
(`slots_*.go`), expression evaluation (`expr.go`), and the overlay
`Fold`/`ZeroSlot` rules (`overlay.go`).

- `data.go` — purely the MRA projection; rewritten verbatim by
  `make enctab-regen-source`. Never hand-edit.
- `manual_forms.go` — hand-curated forms (MRA gaps, GNU alias
  preferences); consulted first, so it wins ties. Never regenerated.

When a Z80 encoding is wrong, read the Go function here and port it
faithfully (`CLAUDE.md` §"If Go already implements it, the Z80 side is a
port, not a design"). See `docs/ARCHITECTURE.md` §3–§4.
