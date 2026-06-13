# `tools/tables-gen` — Z80 data-table generator

Generates the Z80-side data tables whose authority is Go source, so the SAM
assembler's tables follow the Go encoder/format packages automatically
instead of being hand-mirrored (i7 — eliminates the hand-sync drift bug
class). It imports both authority packages (`aarch64enc`,
`sam-aarch64-format`).

Outputs:

- `make enctab` → `build/enctab.enc` — the binary form table the SAM-side
  encoder loads at boot (MRA projection + hand-curated `manual_forms.go`).
  The loader reads the file length from the SAMDOS DIFA header, so no
  assembly-time length constant is needed (i7 phase A eliminated
  `ENCTAB_LEN`).
- `make enctab-regen-source` → `tools/aarch64enc/data.go` — the Go mirror of
  the same MRA projection.
- `make tables` → `src/sysreg_tables.inc` — the four AArch64 System-group
  name↔encoding tables (sysreg / pstate / dc / tlbi), projected from
  `tools/sam-aarch64-format/sysregs.go` (i7 phase B). `make tables-sync-check`
  fails CI if the committed file drifts from the generator output.

The `.enc` and `.inc` mirror the Go authority by construction, so the Z80 and
Go sides share one table. See `docs/ARCHITECTURE.md` §4.
