# i280 — Trace B-DOS's own Trinity SD record write to derive our write contract

**Status:** plan (execution not started). Ephemeral — delete in the PR that completes i280.

## Why

Our TFTP-push serve writes a received `.mgt` to a free Trinity record via
`bdos_write_sector` → `rst 8 / defb 149` (the B-DOS **HWSAD** hook). On real
hardware (2026-06-28) it **hangs in the per-block write** after DATA block 1. B-DOS
*is* resident (it loaded trinload), so the hook is serviced — yet our invocation
hangs where B-DOS's own record writes succeed. We fixed one contract bug (HWSAD
wants `A`=drive=0; ours left `A`=sector — on branch `i270-hwsad-drive-contract`),
but hardware showed **`A=0` alone is not sufficient**. The captured Trinity docs
have **no SD sector write example** (the Issue-21 article was never written), so
**Colin's B-DOS 1.5t is the only authority** for the write — and the way to learn
the *correct invocation contract* is to **trace B-DOS's own working record write in
emulation**.

Pete's directive: **reuse B-DOS, do not reimplement the SD protocol from first
principles.** The trace tells us which B-DOS prelude/entry-points a successful write
needs (SD-init, seek/record-base poke, `set.drive.a`, the `&251` page set), so our
serve replicates that — ideally by calling the same entry points.

## Vehicle decision

**Extend the koron-go harness (`tools/netboot-oracle/z80/`); reject SimCoupé.**
- SimCoupé is GUI/SDL and, decisively, **does not model the `&DC`–`&DF` Trinity
  ports** — B-DOS's SD I/O would read open bus.
- The koron-go harness already has port IN/OUT interception (`harness.go` `mem.In`
  ~:235 / `mem.Out` ~:313), per-instruction PC trace (`in.Trace` ~:805),
  memory-access trace (`SetAccessTrace` ~:149), flat-memory load (`Write`/`LoadAt`
  ~:357/:522), and a **working SD-SPI model** (`sdcard.go`, CMD0/8/41/58/9/16/17/24,
  written against Colin's `&A918`/`&A86B`).
- The change vs today: **load the real B-DOS binary**
  (`~/sam-archive/bdos/analysis/extracted/bdos15t-beta6.bin`) into harness RAM and
  run its real `&A623`/`&A81F`/`&A918` code against the SD model, with **no
  `AttachBDOS` Go hook** intercepting RST 8 (today `bdos_store.go` *re-implements*
  the hooks; to trace B-DOS itself we run B-DOS's Z80, not our Go model — i273).

### Loading notes (risks)
- B-DOS executes paged: `&8000`–`&BFFF` with `&4000`–`&7FFF` aliases, and the SD
  primitives are *called* at the `&67xx` alias (e.g. `call 0x67cc`). The flat 64 KB
  harness must have the image **mirrored into the `&4000`–`&7FFF` (and `&67xx`)
  window** too, or those calls hit empty RAM. Verify the first traced `call 0x67cc`
  lands on real code.
- B-DOS RAM workspace (`hd.wp`@&40c8, page/addr@&4119/&411c, buffer@&7805, record
  vars) must be sane — **prefer running B-DOS's own init/select path** to populate
  them over poking guesses.

## What to trace (order)

1. **HWSAD single-sector write (our exact path).** HRECORD-select a record, set
   `hk.de`=track/sector, `hk.hl`=buffer, `A`=0, `RunFrom` the `HWSAD` label. If it
   hangs in emulation too, we've reproduced the bug traceably; if it succeeds, the
   trace shows the state the HRECORD-select left that our serve omits.
2. **The record-copy command (record N→M) — the gold reference.** Drives the full
   successful init→select→read→select→write→deselect sequence; its `&DC`/`&DF` log
   is the authoritative CMD24 stream and its entry/register order is the template to
   reuse. Drive the copy/backup subroutine directly (pre-poke workspace) rather than
   via the BASIC tokeniser.

## Instrumentation (a logging layer on the existing hooks)

- OUT `&DC`: decode `&30`/`&31`/`&38`/`&3F`/`&04` + PC.
- IN `&DC`: value + busy bit (3) + PC — the hang loop shows here.
- IN/OUT `&DD`/`&DE`/`&DF`: the literal SPI byte stream (CMD24 opcode `&58`, 4 addr
  bytes, `&FE` token, 512 payload, CRC, data-response `&05`, busy→`&FF`).
- PC+registers at: HRECORD select, SD-init `&A623`, seek (`&A16B`/`&A1A6`),
  `set.drive.a`, `&A81F` (CMD sender — capture B=opcode + the poked LBA immediates
  read at `&A836`/`&A843`), `&A918` (write core), `&A86B` (tail), `&A8D7` (deselect).
- `SetAccessTrace` on `&A836`/`&A837`/`&A843`/`&A844` — the dest LBA B-DOS computes.
- Hang detector: if `maxSteps` hit inside the `&A81F`/`&A918` busy-poll, emit
  `HANG at &XXXX, last &DC IN=0xNN`.

**Artifact:** an annotated trace with (A) entry/register sequence, (B) port byte
stream, (C) the LBA computation. Tool lives in `tools/netboot-oracle/z80/cmd/bdostrace/`
with a README; read-only/diagnostic.

## Trace → fix

- **Outcome A (preferred): call B-DOS entry points correctly.** If the gold trace
  reaches HWSAD's core only after a prelude (SD-init via HRECORD/`chk.hd.ex`, seek
  poking the LBA, `set.drive.a` A=0, `&251` page), the fix in `bdos_seam.asm` is to
  **replicate that prelude** — call the same higher-level record-write entry, or
  invoke B-DOS's SD-init+seek entries before HWSAD. Confirmed when adding the missing
  call makes the emulated HWSAD complete (busy clears, `&FF` release) where it hung.
- **Outcome B (fallback): mirror the protocol** in our own direct-SPI routine
  (`raw_record_sink.asm`/`sd_csd.asm`) per the byte stream — only if entry-point
  reuse is shown impossible. (Re-implements Colin; least preferred.)
- **Discriminator:** run our *current* seam path under the same harness+real-B-DOS
  and diff against the gold trace; the first divergent call (or first non-clearing
  `&DC` poll) is the missing contract.

## Risks
- Emulation may be too forgiving (`sdcard.go` always clears busy) and the hang may
  NOT reproduce — acceptable: the gold trace still gives the target sequence; the
  hang then stays a hardware gate (CLAUDE.md §5) and we make our path match + retest.
- The `MMCSD` software-disk utility (not captured) may be a cleaner record-copy
  authority — worth requesting from Colin in parallel (q-note).

## Step list / split
1. bdostrace scaffold: load real B-DOS, mirror aliases, attach SD, reach `&A623`
   init alive. 2. Instrument port/register/memory traces → artifact. 3. Trace HWSAD
   single write (path 1). 4. Trace record-copy (gold ref, path 2). 5. Diff our path
   vs gold → "missing contract" conclusion. 6. **Fix `bdos_seam.asm`** (Outcome A/B;
   deps 1-5; modifies `src/`). 7. README + Colin/MMCSD request.
Items 1-5 are diagnostic; only 6 changes `src/`.

## Relationship to i270a/#713
i270a/#713 (fix `bd_list_write_hw`, the direct-SPI **list-claim** write) is a correct
fix for *that* routine but is **not the gate** on i194 — it's only reached at finalize,
after the per-block data write that hangs. The trace may show the record-claim should
also use a B-DOS entry point (making `bd_list_write_hw` removable). Keep #713 draft
pending the trace outcome.
