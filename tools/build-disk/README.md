# `tools/build-disk` — bootable disk-image builder

Packs a bootable SAM `.mgt` disk image for the round-trip pipeline:
the boot DOS (B-DOS AL 1.5a at `reference/bdos/al-bdos15a.bin` by default), the
assembler binary, a BASIC auto-run loader, `enctab.enc`, the off-axis
payloads (`-test-mem`, `-cluster`, `-paged-call`, `-sysreg-data`,
`-disasm`), and the input `.tbn`.

`-dos` / `-dos-name` / `-dos-load` swap the boot DOS for any hook-compatible
image. SAMDOS 2 implements the same RST-8 hook interface, so a SAMDOS 2
compatibility build is `-dos reference/samdos/samdos2.bin -dos-name samdos2
-dos-load 491529`. B-DOS became the shipped/CI boot DOS in i75 (the q10
resolution): the swap is hook-portable (verified i62), licence-clean
(Edwin Blink's freeware grant), and boots on a plain floppy machine with no
mass storage attached (proven i75). See `docs/specs/samdos-file-io.md`.

Entry point: `main.go`. Build: `make build-disk`; `make disk`
assembles the full test disk. Every SimCoupé round-trip driver invokes it.
See `docs/ARCHITECTURE.md` §7.
