# `tools/build-disk` — bootable disk-image builder

Packs a bootable SAM `.mgt` disk image for the round-trip pipeline:
the boot DOS (SAMDOS 2 at `reference/samdos/samdos2.bin` by default), the
assembler binary, a BASIC auto-run loader, `enctab.enc`, the off-axis
payloads (`-test-mem`, `-cluster`, `-paged-call`, `-sysreg-data`,
`-disasm`), and the input `.tbn`.

`-dos` / `-dos-name` / `-dos-load` swap the boot DOS for a hook-compatible
image (B-DOS implements the same RST-8 hook interface — verified i62 — e.g.
`-dos reference/bdos/al-bdos15a.bin -dos-load 32777 -dos-name bdos`). The
default invocation is byte-identical to before; the production/CI swap is
gated on q10. See `docs/specs/samdos-file-io.md`.

Entry point: `main.go`. Build: `make build-disk`; `make disk`
assembles the full test disk. Every SimCoupé round-trip driver invokes it.
See `docs/ARCHITECTURE.md` §7.
