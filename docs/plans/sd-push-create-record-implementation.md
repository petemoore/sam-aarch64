# sd_push — create-record + write-body implementation spec (own-LBA, no B-DOS)

**Status:** spec for implementation (2026-06-30 session). The durable trace of the design
so it survives context refresh. If you are a fresh agent: read this whole file first.

## Goal
Push a `.mgt` SAM disk image (cj.mgt) over the network to a **free Trinity SD record** so
the record (a) is **registered** (appears in the SAM `RECORD` listing) and (b) **boots**.
Self-contained: raw SD writes only, **no dependency on B-DOS being resident**.

## CRITICAL — where the work happens (#771 vs #772, do not confuse)
- **Implementation target = the WORKTREE `~/git/sam-aarch64-771recover`** (git detached at
  `b24957a`, the **#771** code). This HAS `bd_record_write_hw` (own-CMD24 record write) —
  the routine the design needs.
- The **main repo `~/git/sam-aarch64`** is at **#772** (`2a255f0`), which **DELETED**
  `bd_record_write_hw`. Do NOT implement there; do NOT get confused that the routine is
  "missing" — it exists in the worktree.
- Build: `cd ~/git/sam-aarch64-771recover && make build/sd_push.bin` (the Makefile already
  passes `-D NETBOOT_WANT_CLAIM=1`).
- Host pusher: `tools/trinload-push/sd-push.py` (streams all 1600 sectors, linearSec 0..1599).

## Architecture (cited; confirmed by SamDisk + B-DOS 1.5t disassembly)
A Trinity SD card stores SAM disks as B-DOS **records** (1600×512B = 819200 B each). TWO
separate on-card metadata structures — do not conflate them:

1. **Catalogue / record-LIST table** — card sectors `[1 .. base_sectors-1]` (after boot
   sector 0, before the record bodies). 16-byte **name** entries, 32 per 512-B sector.
   Registers a record's existence/name. A slot is FREE iff `(name[0] & 127) == 0`.
   - record n's entry: **sector `1 + (n-1)/32`**, **byte offset `((n-1) & 31) << 4`**.

2. **Record BODY** — 1600 sectors at `csd_base + 1600*(n-1) + i`, i = 0..1599. The `.mgt`
   content. The B-DOS validity stamp **"BDOS" lives at offset 232 of the body's FIRST
   sector** (track-0 sector-1 = i=0; the disk's first directory entry).
   - Cited: B-DOS `get.label` (`bdos15a.src.txt:2834-2848`) reads body track-0 and checks
     offset 232 == "BDOS"; `new.lab` (`bdos15a.src.txt:2773-2794`) writes it there.

**The stamp is INSIDE the body's first sector, not a separate head.** (The catalogue/list
entry is the only thing that is separate.)

## The definitive transformation (SamDisk `WriteRecord`, the working authority)
SamDisk 3.x preserved source (`#if 0` body, commit `d00acbe`, `src/types/record.cpp`).
Writing a non-B-DOS `.mgt` to a record **MUTATES it (NOT bit-for-bit):**
1. `memcpy(buf+232, "BDOS", 4)` into the image's **first sector** (`record.cpp:163-165`).
2. Write the 16-char disk label across **+210 (10 B)** + **+250 (6 B)** of the first
   sector (`SAMCoupe.cpp:101-110`). Use the same 16-byte name as the catalogue entry.
3. Everything else of the `.mgt`: bit-for-bit.
4. Write the 1600 (mutated) body sectors by **absolute LBA** (no B-DOS hooks).
5. Write the catalogue/list entry (16-byte name) by **absolute LBA** (read-modify-write the
   list sector) — `record.cpp:171-187`. Geometry `SAMCoupe.cpp:326-345` matches our model
   (boot + list, then 1600-sector bodies at `base+1600*(n-1)`).

SamDisk writes by absolute LBA because it is a host tool with no B-DOS. We do the same
(raw CMD24) on the SAM side, so the program has **no B-DOS dependency** (this is why it
sheds the HRECORD `rst 8` crash class — HRECORD needed B-DOS paged a way it was not).

## IMPLEMENTATION — do EXACTLY this, nothing more

### KEEP (already correct in the worktree)
- Free-record scan (`bdos_find_free_record` → `BD_FREE_RECORD` = n; raw CMD17).
- `bdos_claim_record` (writes catalogue 16-byte name via `bd_list_write_hw`/raw CMD24; it
  internally calls `bdos_build_claim_entry` which leaves the sanitised 16-byte name in
  `BD_CLAIM_ENTRY`).
- CSD read (`csd_set_bd_records` → `csd_base`).
- `bd_record_write_hw` (`sd_csd.asm:606`): In `BD_REC_WRITE_REC`=n, `BD_REC_WRITE_LINEAR`=i,
  `HL`=512-B source buffer → CMD24 to `csd_base+1600*(n-1)+i`.
- Debug markers (`dbg_char` + '1'..'5'); add '7' and '9'.
- The receive loop / `@`-block protocol / discovery / ARP / ICMP.

### CHANGE
1. **REMOVE the HWSAD/HRECORD body path entirely:**
   - Remove `call bdos_select_record` (HRECORD) from the create section.
   - Remove the serve-loop body write via `bdos_write_sector` (HWSAD).
   - Remove the routine `bdos_write_record_label` (the separate-label-sector approach is
     WRONG — the stamp goes INSIDE the body's sector 0, not a separate sector).
2. **Create section = JUST the catalogue claim** (no HRECORD, no separate label):
   ```
   ld   hl, sp_record_name
   ld   (BD_CLAIM_NAME_PTR), hl
   call bdos_claim_record       ; catalogue 16-byte name; builds BD_CLAIM_ENTRY
   ld   a, "7" / call dbg_char   ; DBG: catalogue entry written
   ```
3. **Body write (serve loop) = own-LBA via `bd_record_write_hw`, with SECTOR-0 MUTATION:**
   For each received `@`-block (linearSec `i`, ≤512 data bytes copied into `BD_WRITE_BUF`):
   - **If `i == 0`**, mutate `BD_WRITE_BUF` before writing:
     - copy `BD_CLAIM_ENTRY[0..9]`  → `BD_WRITE_BUF+210`
     - copy `bdos_id_str` ("BDOS", 4 bytes) → `BD_WRITE_BUF+232`
     - copy `BD_CLAIM_ENTRY[10..15]` → `BD_WRITE_BUF+250`
   - `ld hl,(BD_FREE_RECORD) / ld (BD_REC_WRITE_REC),hl`
   - `ld hl,i / ld (BD_REC_WRITE_LINEAR),hl`  (i = the @-block's linearSec)
   - `ld hl, BD_WRITE_BUF / call bd_record_write_hw`
   - ACK the block as the existing protocol does.
   - On finalize (count == 1600): marker '9'; clean `RET` to trinload (re-pushable).
4. **Name:** `sp_record_name` = the pushed filename. For now `defm "cj.mgt"` + a NUL byte
   (sanitised by `bdos_build_claim_entry`); comment it as hardcoded-for-now / TODO plumb
   from host. The SAME 16-byte name (`BD_CLAIM_ENTRY`) feeds both the catalogue (step 2)
   and the sector-0 label mutation (step 3).

### GUARDRAILS — do NOT
- Do NOT use `bdos_select_record` (HRECORD) or `bdos_write_sector` (HWSAD) — no B-DOS
  `rst 8` hooks on the write path.
- Do NOT write a separate label sector — the stamp is mutated INTO body sector 0.
- Do NOT reintroduce an A/B toggle or a Path-B stub (single path only).
- Do NOT touch the receive protocol / discovery / ARP / ICMP.
- Do NOT byte-swap "BDOS" — write `bdos_id_str` ("BDOS") directly (it matches B-DOS
  `get.label`). (Byte-swap is a SamDisk host-side concern for ATA disks; verify on hardware
  by reading back sector-0 +232 — expect "BDOS", not "DBSO".)
- Do NOT add new buffers if `BD_WRITE_BUF` suffices.
- Do NOT depend on B-DOS being resident.

### Cited symbols/addresses
- `get.label` body track-0 +232 == "BDOS": `bdos15a.src.txt:2834-2848`.
- `new.lab` name@210 / "BDOS"@232 / name@250: `bdos15a.src.txt:2773-2794`.
- SamDisk mutation `record.cpp:163-165` (BDOS@232) + `SAMCoupe.cpp:101-110` (label);
  catalogue `record.cpp:171-187`; geometry `SAMCoupe.cpp:326-345`.
- In-tree: `bd_record_write_hw` (`sd_csd.asm:606`, LBA math `:578`), `bdos_claim_record`
  (`bdos_seam.asm:735`), `bdos_build_claim_entry` → `BD_CLAIM_ENTRY` (`bdos_seam.asm:1133`),
  `bdos_id_str` "BDOS" (`bdos_seam.asm:278`), `BD_WRITE_BUF`, `BD_REC_WRITE_REC/LINEAR`.

### Build + report (implementer)
`cd ~/git/sam-aarch64-771recover && make build/sd_push.bin`. Report size, boot-fit, a diff
summary, and cite each symbol used. **NO push / deploy / commit.** Flag any UNKNOWN rather
than guessing — this writes Pete's real shared SD card.

## Testing (orchestrator, after the build)
1. **Faithful emulation** (`sd_push_faithful_test.go`, real ROM + B-DOS): verify the
   catalogue-entry + body + sector-0 mutation land at the correct LBAs, data-safe. NOTE the
   emulation is lenient on `get.label`, so the **hardware screen markers** are the real
   validity proof.
2. **Hardware shot (traced):** trinload is up (no power-cycle needed). `tcpdump` the shot.
   Push via `DEPLOY_CHECKED=1 python3 tools/trinload-push/sd-push.py 192.168.2.75 ~/cj.mgt
   build/sd_push.bin`. Watch the SAM screen: `1`..`5` (scan), `7` (catalogue written), the
   body stream (own-LBA), `9` (complete). Then on the SAM: `RECORD` lists the new record;
   booting it runs cjs.
3. **Byte-swap check:** read back the record's sector-0 +232; expect "BDOS" not "DBSO".

## Why this is the right design (summary)
- Matches SamDisk (the working host tool) exactly: mutate the image (BDOS@232 + label),
  write body + catalogue by absolute LBA.
- No B-DOS dependency → sheds the HRECORD `rst 8` crash class.
- Footprint stays ~6.9 KB (SD routines already present; +~20 instr mutation, −~80 dropping
  HWSAD/HRECORD).
- Produces a FULLY valid record (stamp permanently in body sector 0), listed and bootable.

## RESULTS (2026-06-30 hardware run) — WRITE WORKS, but record reads as INVALID
- **The create + write succeeded end-to-end on real hardware.** sd_push (own-LBA + sector-0
  mutation, build 6933 B) deployed cj.mgt to **record 13** (screen showed `123457R000D`):
  catalogue claim + all **1600 body sectors in 102.6 s** (`DONE: validated a complete record`),
  **no crash, no hang.** The HRECORD-crash class is gone.
- **BUT** `RECORD 13` on the SAM reports **"81 Invalid record, 0:1"** — B-DOS `get.label`
  rejects it. The **catalogue is correct** (B-DOS *found* record 13 → our list-write LBA +
  geometry match B-DOS). So the **body's "BDOS"@232 stamp is not where `get.label` reads.**
- **Diagnose next:** read record 13's body sector 0 off the card (LBA `csd_base+1600*12`,
  csd_base=2438 → ~21638) and inspect bytes +210/+232/+250. Tool: `build/netboot_dumper.bin`
  (Pete: "we already have a netboot program for pulling data off the SD card") — check whether
  it can target an arbitrary LBA; else adapt it / use the sd_listread path.
  - Hypotheses (untested): (a) the i==0 sector-0 mutation didn't land "BDOS" correctly
    (offset/trigger bug); (b) the **body base** (`csd_base`) is off-by-one vs B-DOS's record
    base even though the list matched (list region vs body region computed differently);
    (c) `get.label` reads a track-0 sub-sector we didn't map to linearSec 0. Reading the card
    settles it. NOTE byte-swap is unlikely (the list read/write agreed with B-DOS, proving the
    byte layout is consistent).
- **Speed:** ~8 KB/s because `bd_cmd24_write_core` runs `sdc_init_ladder` (full CMD0/8/41/58/59)
  **per sector**. Pete: B-DOS already inited the card at boot, and no per-block ENC re-arm
  disturbs the bus (settled fix) — so drop the per-sector init (init once, or not at all) →
  tens of KB/s. Secondary to the invalid-record fix.
- **State:** worktree `~/git/sam-aarch64-771recover` (#771) holds the working write code; the
  card currently has cj.mgt at record 13 (catalogue-named, body written, but not get.label-valid).
  trinload is up (no power-cycle needed). The deploy-guard false-fires on the word "tftp" in any
  command (i268) — avoid it in greps.

## LANDED (2026-06-30) + NEXT STEPS
- **Committed/pushed/PR'd** per Pete ("bank progress even if failing"):
  - Spec → **main** (`39a9457`, doc-only).
  - Working code (from #771 worktree) → branch **`i295-create-record`** (pushed).
  - Clean main-based landing → **PR #773** (`i295-create-record-land`): **builds green** (6933 B),
    restores `bd_record_write_hw` + adds `-D NETBOOT_WANT_CLAIM` to the Makefile sd_push recipe,
    reverts #772's *minor* sd_csd.asm comment tweaks (core purge preserved). **CI expected RED**
    (Go tests assert the old HWSAD path; record not yet get.label-valid). Do NOT bypass branch
    protection to merge — Pete's call.
- **NEXT (priority order), for a fresh-context agent:**
  1. **Diagnose the invalid record (the blocker).** Read record 13's body sector 0 off the card
     (LBA `csd_base+1600*12`, csd_base=2438 → ~21638) and inspect +210/+232/+250. No existing tool
     reads an arbitrary SD sector (dumper=ROM/EEPROM; sd_listread targets the list region) — adapt
     `sd_listread_standalone`/`bd_list_read_hw` to a chosen LBA, or add a tiny read-and-report-over-UDP
     probe. Compare what's at +232 (expect "BDOS"). Test the hypotheses in the RESULTS block:
     (a) sector-0 mutation didn't land (offset/trigger bug — but binary decode showed
     `ld de,BD_WRITE_BUF+232` + "BDOS" present, so less likely); (b) **body base off-by-one** vs
     B-DOS's record base (the catalogue/list matched, but the body uses csd_base directly — verify
     csd_base == B-DOS &80C2 base, and the linearSec-0 → track0/sector1 mapping). (c) get.label's
     exact read sector. THIS is what makes RECORD 13 valid + bootable.
  2. **Update the Go tests** (`sd_push_test.go`, `sd_push_faithful_test.go`) for the own-LBA +
     sector-0-mutation flow (they still assert HWSAD) → green CI → mergeable.
  3. **Speed:** drop the per-sector `sdc_init_ladder` (init once, or rely on B-DOS's boot-time init —
     Pete: B-DOS already inited the card; no per-block ENC re-arm disturbs the bus). ~8KB/s → tens of KB/s.
  4. **Boot record 13** to confirm cjs runs (after it's get.label-valid): configure boot to record 13
     + power-cycle, or via the record-picker (i264b).
  5. Prune the dead HWSAD/HRECORD seam routines from the binary (Pete: "delete the other code");
     plumb the record name from the host filename (currently hardcoded "cj.mgt").
