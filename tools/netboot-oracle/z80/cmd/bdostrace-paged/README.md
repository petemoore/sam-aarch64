# bdostrace-paged

Paged-boot diagnostic for Colin Piggot's real B-DOS 1.5t Trinity SD record-write
machinery. It boots the real captured patched ROM + Trinity EEPROM to a
B-DOS-resident editor idle (the genuine paged environment), then drives the real
HWSAD/HRSAD hook machinery to capture the **gold entry contract** a successful SD
record write needs — the input the i280b `src/netboot/bdos_seam.asm` fix consumes.

It is the paged counterpart of `cmd/bdostrace` (i280a): that flat tool runs the §8
SD-write *core*, but cannot run the hook *entry prelude* (it escapes into SAM-ROM
bridges the flat 64 KB model lacks). See `docs/notes/trinity-sd-z80-interface.md`
§8b for the captured contract and §8a / `docs/plans/i280-bdos-write-trace.md` for
the surrounding task.

This is a **read-only diagnostic that emits a report, not an assertion** — it lives
in `cmd/` (not as a `Test*`) per the testing policy. CI builds it; running it needs
Colin's proprietary captures (referenced by path, never in the repo), so CI does
not run it.

## Requirements

The captured ROM + EEPROM at `~/sam-archive/samboot-capture/{rom.bin,eeprom.bin}`
(or `$SAMBOOT_CAPTURE_DIR`). Absent → the tool errors out non-zero.

## Usage

```
go run ./cmd/bdostrace-paged
```

It boots to B-DOS idle, then runs three experiments (rst-8 dispatch faithfulness;
the &8662 device-select → &780B; HWSAD-vs-HRSAD gate symmetry) and prints the gold
contract. No flags.
