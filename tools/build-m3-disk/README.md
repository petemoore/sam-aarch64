# `tools/build-m3-disk` — bootable disk-image builder

Packs a bootable SAM `.mgt` disk image for the round-trip pipeline:
SAMDOS 2 (`reference/samdos/samdos2.bin`), the assembler binary, a BASIC
auto-run loader, `enctab.enc`, the off-axis payloads (`-test-mem`,
`-cluster`, `-paged-call`, `-sysreg-data`, `-disasm`), and the input
`.tbn`.

Entry point: `main.go`. Build: `make build-m3-disk`; `make m3-disk`
assembles the full test disk. Every SimCoupé round-trip driver invokes it.
See `docs/ARCHITECTURE.md` §7.
