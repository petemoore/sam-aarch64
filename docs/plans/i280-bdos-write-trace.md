# i280 — Get writing SAM .mgt disk images to Trinity SD records working

**Goal (Pete):** the serve receives a `.mgt` disk image over TFTP and must write it
into a Trinity SD **record**; today the per-block write **hangs on real hardware**.
Approach (Pete): capture a *working* B-DOS record write in emulation, diff our serve's
write against it, fix the gap.

**Status:** i280a DONE (the `bdostrace` tool). i280b is a long, well-documented
investigation — `docs/notes/trinity-sd-z80-interface.md` **§8a–§8u** is the authority;
read it before touching this. Ephemeral plan — delete in the PR that completes i280b.
Live state: registry **i280b-b2q DONE** (PRs #737/#738), **i280b-b2i OPEN** (the fix).

---

## IMPLEMENTING-AGENT HANDOVER — START HERE (2026-06-29)

**Read FIRST, before any code:** `docs/notes/trinity-sd-z80-interface.md` §8s, §8t, §8u
(the most recent findings), then the SOURCES table at the bottom of this section. Heed the
research-first rule (`memory/feedback_docs_first`): the hardware primary sources below
were NOT read until late and that caused a wrong experiment design — read them up front.

### Where it stands (what's proven, what's the leading cause)

- **Reproduced in emulation (PRs #737/#738, guards in
  `tools/netboot-oracle/z80/bdos_write_core_reach_test.go`):** HWSAD reaches the B-DOS SD
  write core `&A8F4`/CMD24 when the two device-select gates are satisfied, and the hang
  reproduces — wedging `&DC` bit-3 BUSY (`ENC28J60.StuckBusy`) makes the write path spin at
  `&A7CC` (= `&67CE` under the serve map). Detail: §8s.
- **The hang is the manual's `check_busy` poll on `&DC` bit 3 (`&A7CC`).** `&DC` BUSY is the
  WHOLE shared microcontroller's busy flag (one PIC for SD+ENC+EEPROM); it cannot
  distinguish SD-busy from ENC-busy. Detail + citations: §8u.
- **Leading root cause (grounded, §8u):** an ENC transmit/op that wedges the shared PIC
  (Simon Owen's documented ENC transmit-hang; or `serve_rearm_enc`'s ereset; or a mis-timed
  OUT-while-busy dropping an SD command) leaves `&DC` BUSY stuck → the next SD op's `&A7CC`
  poll spins forever on a BUSY bit that reflects the ENC/PIC, not the SD card.
- **The §8s "device-select gate" (`hk.a`=2 + `&80AF`) is an EMULATION finding, not confirmed
  as the hardware blocker** — hardware shows `HWSAD_PRE → silence`, consistent with both a
  device-select abort AND the `&A7CC` hang (§8t). Do not assume the gate is the hardware
  obstacle; the one-PIC BUSY story (§8u) is the better-grounded hypothesis.

### DO NOT (hard-won pitfalls)

- **Never emit a network/ENC-TX debug marker inside an SD transaction** (CS asserted). It
  clobbers the shared `&DD/&DE/&DF` read-back latch and the one-PIC state — it would CAUSE
  or mask the hang. `src/netboot/dbg_marker.asm` already forbids TX during an asserted SD
  CS. The existing `DBG_HWSAD_PRE` TX fires right before the write and is itself a SUSPECT.
- **Hardware observation must use a NON-network channel** — `&DC` reads are always safe
  (the one port not via the PIC, §8u); or border/screen for a human; or a RAM breadcrumb.
- **Don't commit a serve-code fix off emulation alone** until the emulation reproduces the
  hang via the *modelled one-PIC interaction* (not the manual `StuckBusy` toggle) — CLAUDE.md
  §7 (emulator-is-contract) + the prime directive (understand before changing). A serve
  change off the wrong branch risks breaking the working HGTHD/HLOAD/HSAVE hooks.

### Plan (emulation-first, grounded)

1. **Model the one-PIC ENC↔SD interaction** in `tools/netboot-oracle/z80/enc28j60.go`
   (it already models the shared read-back latch, `rxDisarmed`, `sdInitSettling`,
   `StuckBusy`): make an ENC ereset / ENC TX leave `&DC` BUSY asserted (or the PIC wedged)
   unless the proper settle/quiesce is done — grounded in §8u (BUSY semantics, the 50 µs
   ENC-reset settle, the transmit-hang), NOT invented. Goal: the REAL serve sequence
   (claim-select → `serve_rearm_enc` → HWSAD write) reproduces the `&A7CC` hang with no
   manual `StuckBusy`. Trace the serve's actual sequence first (is `serve_rearm_enc` between
   the data-block RX and the HWSAD write? — `src/netboot/netboot_serve.asm`, the
   `DBG_DATA_BLOCK`/sink path + `serve_rearm_enc` ~567).
2. **Fix:** quiesce/re-init the SD side and wait for `&DC` BUSY clear (+ the 50 µs ENC-reset
   settle) after `serve_rearm_enc` and before the HWSAD write; ensure NO ENC TX (debug
   marker or serve reply) interleaves the SD write window. Verify the modelled hang clears.
3. **Hardware confirm with a PRODUCTION / marker-minimal build** (no ENC TX near the SD
   write, so point 7 doesn't confound the test). Success = the push completes (record
   written, final ACK to curl). Hardware is Pete-gated (TAPO; the autonomy enabler is i272).

### SOURCES (exact paths — dig deeper here)

**Primary hardware docs** (OCR; verify figures against the photo originals in
`~/sam-archive/trinity-docs/photos/` before relying on a number — OCR is noisy):
- `~/sam-archive/trinity-docs/DISCOVERY_REPORT.md` — the summary index (its §3.7 = the
  shared-latch gotcha; §2a.4 = the transmit-hang). It cites the per-photo `.txt` below.
- `~/sam-archive/trinity-docs/text/IMG_20260617_162550.txt` — status register `%1100BWFE`,
  BUSY-bit semantics, the `check_busy` routine, "`&DC` readable any time".
- `~/sam-archive/trinity-docs/text/IMG_20260617_162608.txt` — chip-select/init commands,
  ENC reset `%00101000` (+50 µs), SD init `%00111000` (0/1/2), the OUT→busy→IN SPI protocol.
- `~/sam-archive/trinity-docs/text/IMG_20260617_162617.txt` — shared `&DD/&DE/&DF` read-back
  latch (point 7), per-peripheral auto-null, PUSH/POP, ENC `/CS` pulse `%00100011`.
- `~/sam-archive/trinity-docs/text/IMG_20260617_162626.txt` — ENC interrupt polling (ENCINT).
- `~/sam-archive/trinity-docs/text/IMG_20260617_163210.txt` + `…_163218.txt` — Simon Owen's
  diary: SPI lag, MAC double-read, the **ENC transmit-hang**, 6.5K/1.5K buffer split.
- `~/sam-archive/trinity-docs/text/combined.txt` — all OCR concatenated (grep-friendly).

**B-DOS write-core disassembly:**
- `~/sam-archive/bdos/analysis/bdos15t-beta6.annotated.dis` — `&A7CC` wait/`check_busy`,
  `&A8F4` SD write core, `&A925` CMD24, `&8662` device-select, `&8319` hook dispatcher,
  `&9E16` HWSAD handler, `&9FAB` HRECORD. (Section-B aliases subtract `&4000`.)

**Findings authority + this plan:**
- `docs/notes/trinity-sd-z80-interface.md` §8a–§8u (§8s/§8t/§8u are newest).
- `docs/notes/trinity-capabilities.md` (the verified capability doc).
- `docs/plans/i280-bdos-write-trace.md` (this file).
- Registry: `i280b-b2i` (the fix), `i280b-b2` (umbrella) — `build/registry view --id i280b-b2i`.

**SAM-side serve code (the write path):**
- `src/netboot/netboot_serve.asm` — `serve_rearm_enc` (~567), WRQ/data-block sink + `DBG_*`.
- `src/netboot/raw_record_sink.asm` — `rrs_flush_sector` → `bdos_write_record`.
- `src/netboot/bdos_seam.asm` — `bdos_write_record` (~1029), `bdos_write_sector` (~972, the
  `A'`=0 pin + `DBG_HWSAD_PRE/POST`).
- `src/netboot/encdrv.asm` — `ereset`/`epulse`, `enc_rx_reestablish`, `wait_ready`.
- `src/netboot/dbg_marker.asm` — the i271 UDP marker channel + its SD-CS constraint.

**Emulator (model + tests):**
- `tools/netboot-oracle/z80/enc28j60.go` — shared read-back latch (~688), `ctlStatus` (~591),
  `isBusy`/`clearBusy`/`StuckBusy` (~509-524), `rxDisarmed`/`sdInitSettling` (~227-260).
- `tools/netboot-oracle/z80/sdcard.go` — the SD model (`ctlStatus` ~305, CMD24 path).
- `tools/netboot-oracle/z80/bdos_write_core_reach_test.go` — the §8s guards (write-core
  reach + the `StuckBusy` `&A7CC` hang repro).

---

## What is established (don't re-derive — see §8a–§8q + the registry)

- **The hang is downstream of `HWSAD_PRE`, inside B-DOS's own write path.** Cleared as
  causes, each with evidence: addressing/page-displacement (§8l), the `hk.hl`/`hk.de`/
  `hk.a` **register bank** (§8o — all MAIN bank; `hk.hl`=our `&BE42`), DOS-call paging
  context (§8n — PTDOS re-pages every hook), SD-bus health (§8m, hardware), DI/EI (§8j).
- **§8p (DONE, i280b-b2n):** the HWSAD handler now runs **end-to-end in emulation** via
  the `DOSCNT &5BC3 = 0` arming + the real-boot harness's real ROM bridges (the §8b
  "honest boundary" is gone). Regression guard: `TestHWSADHandlerTraceable`.
- **§8q (this work):** the **authoritative Trinity/BDOS card format** (from samdisk
  `~/git/samdisk/src/SAMCoupe.cpp` `GetBDOSCaps`/`IsBDOSDisk`/`UpdateBDOSBootSector` +
  `cmd_format.cpp`):
  - `list_sectors = bdos_sectors/51200 + 1`; `base_sectors = 1 + list_sectors`;
    `records = (bdos_sectors − base)/1600`. For our `csdV2(0x001D59)` card:
    **`base_sectors = 152`** (matches the observed CMD17 152); records ≈ 4806.
  - record *n* data at `base + 1600·(n−1)`; **selection gate = `"BDOS"` at byte 232**
    of the record's first sector; record-list (16-B labels, 32/sector) at sectors
    `1..base−1`; boot sector at sector 0 (DVAR-0 geometry, `base_sectors` at bytes
    `0x104–0x107` and the DVAR block at `0x10e` — see `UpdateBDOSBootSector`).
  - samdisk's `WriteRecord` (.mgt→record) is **not implemented**, so we build the card
    image in Go from this spec; samdisk is **not** needed at runtime.
- **Verified:** `SDCard.SeedSector(block, data)` is served (`CapturedSector` confirms).

## THE CURRENT BLOCKER (re-aimed again — §8s localizes the §8r write blocker)

**§8s (i280b-b2q, guard `TestHWSADReachesWriteCore`): the write blocker is device-select
aborting on `hk.a=0`.** Driving `HWSAD`(149) through the §8o dispatch against a genuine
`RECORD 1` select reaches device-select (`&8662`) then diverges into the `&8680→&9A8B`
abort (B-DOS's error reporter) — no SD command, returns to editor. Cause: device-select
`cp 1 / cp 2 / jr nz &8680`, and `hk.a` (from the alternate `A'`, dispatcher `&8321 exx /
&8322 ex af,af' / &8323 ld (&81D9),a`) arrives as **0** — the external `rst 8` path resets
A', so a caller's A' doesn't reach hk.a (true for A'=0 AND A'=2). §8b's "force A=2" was a
no-op (it set *main* A). The write core needs device-select to see **hk.a=2 AND `&80AF`≠0**
(the `&8677` SD-claimed gate). `&DC` bit-3 busy is already modelled (`StuckBusy`). NEXT:
resolve how the real serve sets hk.a/`&80AF` (avoid the DOSCNT=0 external path so A'
survives, or explicitly claim the SD device first), then reach `&A8F4`/`CMD24` and diff vs
`HSAVE`. Full detail: `docs/notes/trinity-sd-z80-interface.md` §8s.

## THE EARLIER BLOCKER (re-aimed — §8r supersedes the §8q "no mount" framing)

**The mount + select are SOLVED; the write blocker moved DOWNSTREAM to `SAVE`/`HSAVE`.**
Per §8r (guards `TestBDOSBootNoMountDeviceMounts` + `TestBDOSRecordSelectSelfHeals`):
- Boot does not mount (`last.record`=0) — but `DEVICE` re-runs HDINIT (`&A1B1`) and mounts
  the card **from the CSD alone**: `last.record`=4809 (the exact `GetBDOSCaps` count). So a
  full card-level format is **not** needed for the mount; `DEVICE` is the trigger.
- With the full 1.5t mount var-set poked into B-DOS's page (`last.record`=`&80C4`,
  base=`&80C2`, capacity=`&80BD`, record.no=`&80C6`, hd.wp=`&80C8`), **`RECORD 1` selects
  and persists** — it runs the faithful self-heal init ladder + `CMD17` block-152 read and
  `last.record` stays 4809. This holds for a bare `"BDOS"@232` stamp OR a real MGT
  directory, so the record-1 sector content is **not** the gate.
- **`SAVE`/`HSAVE` is where it fails:** after a selected `RECORD 1`, `SAVE` issues **no SD
  I/O**, resets `last.record`=0, and falls back to the floppy. A stock SAM error `&0C` (12)
  appears around the path, raised **downstream of the directory read** (not B-DOS `rep81`).
  Suspected model-fidelity gap (Trinity-detect `&DC` `&08/&09`→`'TR'`, a post-init
  ready/status, or default-device routing). Full detail: `trinity-sd-z80-interface.md` §8r.

## NEXT — drive + diff the write directly (the §8r re-aim)

The mount/select are no longer in the way — skip the "build a full card format / make boot
HDINIT mount" steps below (superseded by §8r: poke the mount var-set, or use `DEVICE`). The
capture target is now the **SAVE/HSAVE write step**:
1. Fresh boot; poke the mount var-set (see §8r / `bdos_record_mount_test.go`).
2. Via the §8o-armed dispatch (`DOSCNT &5BC3=0`, serve map), drive `HRECORD`(156) then
   `HSAVE`(132) (build the UIFA at `&4B00` like `bdos_seam.asm bdos_fill_save_uifa`); trace
   IN/OUT + hooks and find where the path diverges (why no `CMD24`, where `&0C` is raised).
3. Same with `HRECORD`+`HWSAD`(149) (our serve's shape); diff the two traces.
4. Fix `src/netboot/bdos_seam.asm` (reuse B-DOS entry points; Pete: don't reimplement);
   verify in emulation, then a TAPO hardware retest.

The historical "build a full card format" plan below is retained for reference but is NOT
the current path (the mount is CSD-derived; §8r).

### (superseded) original card-format build

All work in `tools/netboot-oracle/z80/`. The capture rig is `bdos_save_capture_wip_test.go`;
the arming pattern is in `hwsad_handler_traceable_test.go` (§8o `DOSCNT=0` + serve map).

**Step 1 — build a full card-level Trinity format in the SD model so HDINIT mounts it.**
Write a Go helper (e.g. `seedBDOSCard(sd *z80h.SDCard, totalSectors int)`) that, using
`SDCard.SeedSector`, lays down per the samdisk spec:
  - compute `list_sectors`, `base_sectors`, `records` from `totalSectors` (the
    `GetBDOSCaps` formula above).
  - **sector 0:** a BDOS boot sector — port `UpdateBDOSBootSector`'s DVAR-0 layout
    (geometry + `base_sectors` at `0x104–0x107`/`0x10e`). Read it from
    `~/git/samdisk/src/SAMCoupe.cpp:255-320` and mirror the exact bytes. (Trinity is LBA;
    if HDINIT computes the count from the LBA size alone, sector 0 may be optional — test
    both: stamp-only first, add the boot sector if `last.record` is still 0.)
  - **sectors 1..base−1:** the record-list. For record 1, write a named entry (16 bytes
    at offset `((1-1)&0x1f)<<4` of list sector `1 + (1-1)/32`) so it is "in use/named".
  - **record 1 first sector (block `base`):** a valid MGT directory sector with `"BDOS"`
    at byte 232 (and ideally a real MGT dir header — see `SAMCoupe.cpp` `SetDiskInfo`).
  Seed this **before boot**, so boot-time HDINIT sees a formatted card.

**Step 2 — verify the mount.** Boot (`newRealBootMachine`), then read `last.record`
(find its real 1.5t address: trace `sel.record &A0CD` / `hd.init` in
`~/sam-archive/bdos/analysis/bdos15t-beta6.annotated.dis`, or read it after a successful
`RECORD n`). **Gate:** `last.record` must be non-zero. If still 0, B-DOS's boot does not
mount the SD records at all — then drive the real mount hook explicitly (find the correct
mount entry in the 1.5t disasm; the `DEVICE` command handler is the lead) before capture.

**Step 3 — capture the WORKING write.** With the card mounted, via the §8o-armed dispatch
(`DOSCNT=0`, serve map) drive `HRECORD`(156) (`A=0`, `HL=record#`) then `HSAVE`(132)
(`IX → UIFA at &4B00`; build the UIFA like `src/netboot/bdos_seam.asm` `bdos_fill_save_uifa`
does) of a small CODE file. Capture the IN/OUT + hook landmarks with the rig's decoder.
This is the **gold** sequence.

**Step 4 — capture OURS + diff.** Same arming, drive `HRECORD`(156) then `HWSAD`(149) (our
serve's `bdos_write_sector` shape). Diff the two IN/OUT + hook traces. The setup/
orchestration `HSAVE` does that raw `HWSAD` skips (or a missing wait/ready-gate) is the
bug — option 1 in §8q, still **unconfirmed** until this diff lands.

**Step 5 — fix `src/netboot/bdos_seam.asm`** to do the missing setup (reuse B-DOS entry
points; Pete: don't reimplement). Verify in emulation (the rig + a new regression test),
then a TAPO hardware retest (i271 UDP markers; `tools/hardware-shot/run-shot.sh`).

## Key references

- Findings authority: `docs/notes/trinity-sd-z80-interface.md` §8a–§8q.
- Format spec source: `~/git/samdisk/src/SAMCoupe.cpp` (`GetBDOSCaps` ~:330,
  `IsBDOSDisk` :199, `UpdateBDOSBootSector` :255) + `src/types/record.cpp`.
- B-DOS 1.5t disasm: `~/sam-archive/bdos/analysis/bdos15t-beta6.annotated.dis`
  (`sel.record &A0CD`, HRECORD `&9FAB`, HSAVE `&9D54`, HWSAD `&9E16`); 1.5a source
  `bdos15a.src.txt` (`hd.init` :1778) + `bdos14e.src.txt` (`FORMAT` :2565).
- Rig + arming: `bdos_save_capture_wip_test.go`, `hwsad_handler_traceable_test.go`,
  `hwsad_hook_bank_test.go`; SD model `sdcard.go` (`SeedSector`/`CapturedSector`).
- Serve write path to fix: `src/netboot/bdos_seam.asm` (`bdos_write_sector` :972).
