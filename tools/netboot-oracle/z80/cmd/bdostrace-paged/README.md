# bdostrace-paged

Paged-boot diagnostic for Colin Piggot's real B-DOS 1.5t Trinity SD record-write
machinery. It boots the real captured patched ROM + Trinity EEPROM to a
B-DOS-resident editor idle (the genuine paged environment), then drives the real
HWSAD/HRSAD hook machinery to capture the **gold entry contract** a successful SD
record write needs — the input the i280b `src/netboot/bdos_seam.asm` fix consumes.

It is the paged counterpart of `cmd/bdostrace` (i280a): that flat tool runs the §8
SD-write *core*, but cannot run the hook *entry prelude* (it escapes into SAM-ROM
bridges the flat 64 KB model lacks). See `docs/notes/trinity-sd-z80-interface.md`
§8b for the captured contract and `docs/plans/i280-bdos-write-trace.md` for the
task. A **read-only diagnostic** (in `cmd/`, not a `Test*`, per the testing
policy); CI builds but does not run it (the captures are not in the repo).

## Requirements

The captured ROM + EEPROM at `~/sam-archive/samboot-capture/{rom.bin,eeprom.bin}`
(or `$SAMBOOT_CAPTURE_DIR`). Absent → the tool errors out non-zero.

## Usage

```
go run ./cmd/bdostrace-paged
```

It boots to B-DOS idle, then runs five experiments and prints the gold contract.
No flags:

1. rst-8 dispatch faithfulness;
2. the `&8662` device-select → `&780B`;
3. HWSAD-vs-HRSAD gate symmetry;
4. the `hk.a`=A' shadow-accumulator device discriminator (§8h);
5. **the paged-pointer `hk.hl` page-switch (§8j/§8k, i280b-b2h)** — drives the real
   prelude with each candidate `hk.hl`, trapping at `&9E60` to read the page the
   `out (&fb)` switched into section C: the flat `&BE42` displaces B-DOS, and a
   correctly-paged `hk.hl` reads our own buffer. The `SKIP_PRIVATE_TESTS`-gated
   `TestHWSADPagedPointerContract` (`samboot_real_boot_test.go`) asserts the same.
