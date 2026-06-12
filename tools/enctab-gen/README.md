# `tools/enctab-gen` — encoder form-table generator

Generates the binary form table the SAM-side encoder loads at boot
(`make enctab` → `build/enctab.enc`) and the Go mirror of the same MRA
projection (`make enctab-regen-source` → `tools/aarch64enc/data.go`), both
from the vendored ARM MRA snapshot (`reference/arm-mra/`) plus the
hand-curated `tools/aarch64enc/manual_forms.go`.

The `.enc` mirrors the Go runtime form table by construction, so the Z80
and Go encoders share one table.  The loader reads the file length from the
SAMDOS DIFA header deposited by HGTHD, so no assembly-time length constant
is needed (i7 phase A eliminated `ENCTAB_LEN`).  See `docs/ARCHITECTURE.md` §4.
