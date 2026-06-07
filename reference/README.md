# `reference/` — vendored inputs

Third-party material vendored so the build and the research trail are
hermetic. Never edited, only re-vendored.

- `arm-mra/` — snapshot of ARM's Machine Readable Architecture XML for the
  instructions we encode. The input to `tools/enctab-gen` (→
  `build/enctab.enc` + `tools/aarch64enc/data.go`); see
  `docs/ARCHITECTURE.md` §4. `manifest.json` records the snapshot
  provenance.
- `comet-decoded/` — disassembled sources of the COMET Z80 assembler, the
  SAM-era prior art our SAMDOS paging/file-io idioms were studied from
  (`docs/specs/samdos-file-io.md` cites it).
- `comet-disk/` — the original COMET disk contents the decode came from.
- `bdos/` — B-DOS 1.5a (Edwin Blink's freeware improved SAMDOS with
  mass-storage support): the pristine release disk image + detokenised Z80
  source + the Atom Lite 1.5a binary the i62 rig boots. B-DOS is the project's
  target DOS going forward (i72); its hook surface is SAMDOS-compatible. See
  `bdos/README.md` for provenance + the freeware/public-domain licence basis.
- `samdos/` — `samdos2.bin`, the SAMDOS 2 binary currently packed into every
  bootable disk image by `tools/build-disk`. Remains the shipped boot DOS
  pending the B-DOS migration (q10); the target DOS going forward is B-DOS
  (i72, vendored in `bdos/`).
