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
- `samdos/` — `samdos2.bin`, the SAMDOS 2 binary packed into every bootable
  disk image by `tools/build-disk`.
