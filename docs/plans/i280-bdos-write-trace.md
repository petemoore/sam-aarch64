# i280 — Get writing SAM .mgt disk images to Trinity SD records working

**Goal (Pete):** the serve receives a `.mgt` disk image over TFTP and must write it
into a Trinity SD **record**; today the per-block write **hangs on real hardware**.
Approach (Pete): capture a *working* B-DOS record write in emulation, diff our serve's
write against it, fix the gap.

**Status:** i280a DONE (the `bdostrace` tool). i280b is a long, well-documented
investigation — `docs/notes/trinity-sd-z80-interface.md` **§8a–§8q** is the authority;
read it before touching this. Ephemeral plan — delete in the PR that completes i280b.
Live state: branch `i280b-b2n-hwsad-traceable` (pushed), registry **i280b-b2q OPEN**.

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

## THE CURRENT BLOCKER (precisely diagnosed — §8q)

**B-DOS has not MOUNTED the seeded card: `last.record` reads 0 after boot.** The boot
path (patched ROM → trinload → B-DOS → editor idle) leaves B-DOS resident but does **not**
run its record-device mount/HDINIT. So `sel.record` range-checks every record number
against a count of 0 and bails — **both** the BASIC `RECORD` command **and** the
`HRECORD`(156) hook reach their handlers but refuse to select (no SD read, `&780B` stays
0). Failed shortcuts: bare `rst 8/defb 135` (HDINIT) is a no-op; poking inferred 1.5a
sysvars `&80C4/&80C6/&80C9` held but didn't change behaviour (wrong 1.5t addresses, or the
count lives elsewhere).

## NEXT — execution plan (a fresh, deliberate build)

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
