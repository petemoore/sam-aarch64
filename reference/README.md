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
  source + the Atom Lite 1.5a binary (`al-bdos15a.bin`). This binary is the
  shipped boot DOS as of i75 — `tools/build-disk` packs it into every bootable
  disk image by default. Its hook surface is SAMDOS-compatible (verified i62),
  and it boots on a plain floppy machine with no mass storage attached (proven
  i75). See `bdos/README.md` for provenance + the freeware/public-domain
  licence basis.
- `samdos/` — `samdos2.bin`, the SAMDOS 2 binary. Retained for compatibility
  builds: `tools/build-disk -dos reference/samdos/samdos2.bin -dos-name
  samdos2 -dos-load 491529` packs it instead of the default B-DOS. SAMDOS 2's
  source stays authoritative for the shared RST-8 hook semantics
  (`docs/specs/samdos-file-io.md`).
