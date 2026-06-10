# `tools/enctab-gen` — encoder form-table generator

Generates the binary form table the SAM-side encoder loads at boot
(`make enctab` → `build/enctab.enc`) and the Go mirror of the same MRA
projection (`make enctab-regen-source` → `tools/aarch64enc/data.go`), both
from the vendored ARM MRA snapshot (`reference/arm-mra/`) plus the
hand-curated `tools/aarch64enc/manual_forms.go`.

The `.enc` mirrors the Go runtime form table by construction, so the Z80
and Go encoders share one table. **`ENCTAB_LEN` sync rule**: after any
change that grows the table, `src/loader.asm:ENCTAB_LEN` must equal
`wc -c build/enctab.enc` exactly. See `docs/ARCHITECTURE.md` §4.
