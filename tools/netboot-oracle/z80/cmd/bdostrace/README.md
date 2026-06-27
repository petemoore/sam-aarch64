# bdostrace

Diagnostic tracer for Colin Piggot's real B-DOS 1.5t (beta 6) Trinity SD
record-write code paths, run inside the koron-go Z80 harness against the modelled
SD-SPI card. It exists to derive the invocation contract the netboot serve
(`src/netboot/bdos_seam.asm`) must satisfy for a record write — see
`docs/notes/trinity-sd-z80-interface.md` §8a for the contract findings and
`docs/plans/i280-bdos-write-trace.md` for the task.

This is a **read-only diagnostic that emits a report, not an assertion** — it
lives in `cmd/` (not as a `Test*`) per the testing policy. CI builds it; running
it needs Colin's proprietary `bdos15t-beta6.bin` (referenced by path, never in the
repo), so CI does not run it.

## Requirements

The B-DOS 1.5t binary at `~/sam-archive/bdos/analysis/extracted/bdos15t-beta6.bin`
(or `$BDOS15T_BIN`). Absent → the tool errors out.

## Usage

```
go run ./cmd/bdostrace [-scenario init|writecore|hwsad|all] [flags]
```

- `-scenario init` — run B-DOS's real `&A623` SD-init ladder against the modelled
  card; confirms the model answers the full CMD0/8/41/58/9/16 sequence (halts with
  A=card-type).
- `-scenario writecore` — init, then drive Colin's CMD24 write-core (`&A918`) +
  write-tail (`&A86B`) directly, with the LBA poked into the seek immediates. This
  is the **gold successful write byte-stream** (port `&DC`/`&DF` log); it commits a
  sector into the model.
- `-scenario hwsad` — init, then drive the HWSAD hook **handler** (`&9E16`) with
  the hook workspace pre-poked. This is the entry path our serve uses; in the flat
  section-B harness it **escapes into SAM ROM** (`call &0103`) it cannot model —
  the report prints the escape point. Tracing it faithfully is i280b (needs the
  paged-boot harness).
- `-scenario all` — all three in order.

Flags for the `hwsad` scenario:
- `-hka N` — the A (drive) value passed to HWSAD (0 = our seam; 2 = Trinity device).
- `-dev N` — preset the ambient-device var `&780B` before HWSAD (`2` = Trinity, as
  HRECORD-select leaves it; `-1` = leave as init left it).
- `-maxsteps N` — instruction step cap (hang detector).

## Output

Per scenario: the symbol path entered (init/cmd-send/write-core/tail/deselect),
watched memory (seek immediates, `hd.wp`, hook workspace, ambient-device var), the
`&DC`–`&DF` port byte-stream (the SD command/data frames), and — on a hang — the
last PCs plus the window-escape point.
