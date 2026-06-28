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

## RESULTS (2026-07-01) — HARDWARE READ-BACK + 1.5t SOURCE: our write is CORRECT; source says it should be VALID; CONTRADICTION
A READ-ONLY sector probe was built and run against the real card to settle the
hypotheses above. **Tool: `src/netboot/csd_probe.asm` built with `-D SD_SECTOR_PROBE`
(`make netboot-sd-sector-probe` → `build/sd_sector_probe.bin`)** — the hardware-proven
csd_probe + a CMD17-only extension (NO CMD24 in the binary → cannot write the card)
that serves `list.bin`/`r2.bin`/`r13.bin`/`rax.bin`/`quit.bin` over TFTP, each reading
raw card sectors into STAGE. Emulation-gated by `sd_sector_probe_test.go`
(TestSectorProbeReadsSeededLBAs) before deploy. Host fetch = plain `curl tftp://…`.

**Hardware reads (csd_base = 2438, re-confirmed live from the CSD):**
1. **Catalogue (LBA 1) is CORRECT.** Record 13's 16-byte entry at +192 = `636a` + spaces
   ("cj"), claimed, among Pete's real records (record 2 = "Comet v18", records 1,3-32 = "LN").
2. **Our record-13 body write is CORRECT *per our formula*.** LBA `csd_base+1600*12` = **21638**
   holds the cj.mgt first sector: `+210`="cj        ", **`+232`="BDOS" (42444f53)**, first bytes
   = the real cj.mgt directory (`samdos.bin`). The mutation ran and landed exactly where we wrote.
3. **Record bodies ARE at `csd_base+1600*(n-1)` (our formula).** Record 2 ("Comet", real) has
   content at LBA 4038 = `csd_base+1600*1`; the **alt-formula** LBA `csd_base+1600*13` = 23238
   is ZEROS. So the (n-1) base is right; `n*1600+base` is wrong.
4. **A WORKING record (Comet, rec 2) has NO "BDOS"@232** at its sector-0 (LBA 4038), and its
   sector 0 isn't a SAMDOS directory. **⇒ the "BDOS"@232-stamp theory the whole design rested on
   is FALSIFIED as the validity gate** (a working record lacks it). [[feedback_bdos_record_header_vs_disk_body]]
5. **Catalogue metadata differs:** working entries have bytes [10:15] = `000000000000` (Comet, L3,
   L11…) or an `ffff`-prefixed value (L1=`ffff0c0000`, L5=`ffff8f4400`, L32=`ffff796200`); **ours
   = `202020202020` (spaces)** — our claim space-padded all 16 bytes instead of leaving the metadata
   region zero. The one concrete structural difference between our record and every working one.

**1.5t SOURCE (the real-HW authority — `bdos15t-beta6.annotated.dis`, NOT 1.5a; [[feedback_bdos_15t_not_15a]]):**
- get.label (&8DE0): reads the record's first directory entry, checks `+232 == "BDOS"`; CY (invalid)
  iff mismatch → `RECORD n` reports rep81 "Invalid record" (exprcd, 1.5a line 877; 1.5t &8DC2).
- RECORD-select (&A0E4 → &A100): `dec de` (a0ec) makes record# = **record-1**, then
  mult16-32 `(record-1)*1600` (&A113) `+ base` (&80C2=2438), poked to the seek base immediates
  &A185/&A188. conv.de(track0,sector1) (&A151) = **0**. So get.label for record 13 reads
  `(13-1)*1600 + 2438 + 0` = **LBA 21638** — EXACTLY where our "BDOS"@232 is.
- **⇒ Per 1.5t source, record 13 SHOULD validate** (BDOS present at the read LBA), and the count
  check passes (last.record ≫ 13; ≥824 records listed). **This CONTRADICTS the hardware "invalid".**

**THE OPEN CONTRADICTION (this is i299's crux now):** our write is byte-correct at the LBA 1.5t
get.label reads, yet hardware reported "81 Invalid record, 0:1". Candidate explanations, in order:
  (a) the hardware "invalid" was STALE / a directory cache (re-test `RECORD 13` on the live card now
      that the write is confirmed in place); (b) the catalogue metadata (bytes 10:15 = spaces vs
      zero) matters to select/validation; (c) the "0:1" suffix = drive 0 (floppy) — a drive-select
      slip on the RECORD path; (d) a get.label setup call (0x45d2/0x5c6a/0x444f) I haven't fully traced.
**DEFINITIVE resolver = EMULATION (CLAUDE.md §7, which this strand should have used first):** seed the
faithful rig's SD model with record 13 exactly as written (catalogue + body+BDOS@232 at (n-1)*1600+base)
and run real B-DOS 1.5t `RECORD`-select/get.label — observe valid/invalid AND the exact LBA(s) read.
If invalid in emulation → reproduced + fully traceable; if valid → the HW observation was stale →
re-test hardware. (Hardware re-test needs `RECORD 13` typed at the SAM = Pete-present, or driven.)

**ROOT-CAUSE FIX for the recurring 1.5a-vs-1.5t lure (Pete's idea, 2026-07-01 → i304):**
reconstruct a *labelled 1.5t source* by starting from `bdos15a.src.txt`, swapping in 1.5t's changed
routines, and reassembling until it produces the EXACT 1.5t binary (byte-match check = the proof it's
right) — retaining all the 1.5a comments, then annotating the new routines. Differences are minimal.
This gives a clean, greppable 1.5t authority so agents stop reaching for 1.5a.

## RESOLUTION (2026-07-01, EMULATION — CONTRADICTION SETTLED): the write is CORRECT; "invalid" was a stale-state artifact
`tools/netboot-oracle/z80/sd_record_validity_test.go` (TestRecordSelectValidityViaGetLabel) runs the
**REAL B-DOS 1.5t** RECORD-select (bootToEditorIdleSD = real ROM + B-DOS 1.5t + the SD-SPI model;
`editorRunLine "RECORD 13"`) against a card seeded with record 13's body sector 0 EXACTLY as our
hardware write produced it (cj.mgt dir + "BDOS"@232 + name@210 at LBA (13-1)*1600+base). Result:
- **WITH "BDOS"@232 → errnr = 0 (VALID).** Real B-DOS 1.5t accepts our write structure.
- **WITHOUT the stamp → errnr = 81 ("Invalid record").** Negative control: confirms get.label IS the
  gate and the seeded LBA is exactly where it reads.

**⇒ Our create-record write is structurally CORRECT — no fix to the write is needed.** The hardware
"81 Invalid record, 0:1" was a **stale/unclean-state artifact**: sd_push writes raw (bypassing B-DOS)
and on that run "exited without returning cleanly to trinload" (i296), so when `RECORD 13` was typed
in the SAME session B-DOS's device/seek/SD state was disturbed (the "0:1" hints drive 0 = floppy) →
get.label read the wrong place. A CLEAN B-DOS boot (which re-reads the card) validates the record.

## CORRECTION (2026-07-01, Pete at keyboard): "stale" was WRONG — RECORD 13 fails on a clean restart; the bug is a record→LBA DIVERGENCE
Pete re-tested on the real SAM: **RECORD 13 still "81 Invalid record, 0:1" after a restart** (not stale);
**RECORD 3 and RECORD 12 WORK**; the catalogue correctly shows entry 13 = "cj". So our earlier
"emulation validates → stale artifact" conclusion was WRONG (the emulation used a SMALL card, base=152,
which is not faithful to the 64 GB card). Established facts now:
- **sd_push is NOT hardcoded** — it calls `csd_set_bd_records` (reads the card CSD via CMD9) and computes
  `csd_base` at runtime (`csd_base: defs 2`, sd_csd.asm:905); `bd_record_write_hw` uses `(csd_base)+1600*(n-1)+i`.
  Only the *throwaway diagnostic probe* hardcoded 21638 (= 2438+1600*12), a host shortcut.
- **Real B-DOS 1.5t computes base=2438** from Pete's exact 64 GB CSD (TestRealCardBaseBDOSvsSdPush:
  isolated &A736 decode + &A45A records-math → base 2438, BD_RECORDS 12423 [16-bit wrap of true 77959]).
  So sd_push's base == B-DOS's *formula* base == 2438. **The math matches the formula.**
- **BUT the real card's records are NOT at base=2438 positions.** The read-only probe (csd_probe + SD_SECTOR_PROBE,
  CMD17-only) read records 3 & 12 at 5638 & 20038 (= 2438+1600*(n-1)) as **completely zero** (24 sectors each),
  yet they RECORD-select fine — so **Pete's disks are stored at a base ≠ 2438** (Pete: "the disk images are
  located somewhere else … you are looking at the wrong place"). The probe DOES read correctly (it found cj at
  21638 and Comet's content at 4038 = 2438+1600*1 in the same runs; catalogue at LBA 1 = the real record list
  L1/Lemmings/Zubdemo…), so this is not a probe-can't-read issue — it's the LBA we compute.
- **HOT LEAD (unconfirmed): B-DOS's REAL seek base may be ~2050, not 2438.** TestRecordSeekLBAReal64GB boots
  real B-DOS 1.5t with the 64 GB CSD and traps the CMD17 read LBA: **RECORD 1 → CMD17 LBA 2050** (NOT 2438).
  If base=2050: rec3→5250, rec12→19650 (where Pete's disks are), rec13→21250 (empty — we wrote 21638) → invalid.
  Δ(2438−2050)=388. CAVEAT: that rig boot left base(&80C2)=0 / last.record(&80C4)=0 (the SD geometry did NOT
  initialise in the rig — so RECORD 3/12/13 issued NO CMD17, failing the count check on last.record=0). So 2050
  is a strong clue from a half-initialised state, NOT yet a confirmed base. The divergence between B-DOS's
  *formula* base (2438) and its *actual* seek (2050) is the thing to nail.

**TWO live hypotheses (Pete, do not guess — isolate B-DOS):**
  (A) B-DOS's actual record→LBA mapping differs from `(n-1)*1600 + formula_base` — a different base (stored vs
      computed?), an offset, or a stride. **Isolate the B-DOS routine that DEFINES record storage and mirror it
      EXACTLY** (sd_push must compute offsets identically to B-DOS, ideally by the same maths).
  (B) The probe's SD read INTERFACE returns different sectors than intended (Pete: "maybe the IN/OUT ports is
      different … maybe there is an offset there") — verify the probe's CMD17 addressing/byte-lag matches B-DOS's
      `bd_list_read_hw` exactly.

**NON-GUESSING CONTINUATION (next session / fresh context):**
  1. Fix the 64 GB-CSD faithful boot so base/last.record actually compute (currently 0 — the SD init/records-math
     didn't run/store in the rig; debug vs the small-card boot which gets base=152). Then trap RECORD 3 & 12's
     CMD17 LBA — that is B-DOS DEFINING where record bodies live. Compare to 2438+1600*(n-1) AND to the 2050 lead.
  2. If B-DOS reads rec3 at e.g. 5250 (base 2050), confirm base=2050 and find WHY the formula (2438) is wrong for
     this card (stored geometry? a different last.record at format? the 16-bit BD_RECORDS wrap feeding the base?).
  3. Make sd_push compute the body LBA by the SAME maths/routine B-DOS uses (mirror or call it), then re-push cj.
  4. ALSO settle hypothesis (B): compare the probe's CMD17 read path byte-for-byte to bd_list_read_hw.
  New diagnostics committed: `sd_real_card_base_test.go`, `sd_record_seek_trap_test.go`, `sd_sector_probe_test.go`
  (+ the read-only `sd_sector_probe` build target). The probe currently reads list/r3/r12/boot windows + quit.bin.

**Consequences for the goals:**
- **i299 (this item) is DIAGNOSED-RESOLVED:** the write is correct; "invalid" is downstream of i296
  (sd_push clean exit), not a write defect. Remaining = a clean-boot hardware confirm of `RECORD 13`
  (needs RECORD typed at the SAM = Pete-present, OR a driven net RECORD-select probe).
- **BOOTING a pushed record (i302 / the i194/i284 end goal) never used get.label** — boot runs the
  disk's own boot sector. So "boot cjs" was never blocked by the RECORD-select invalidity.
- Prioritise **i296** (sd_push clean return to trinload): it both removes the stale-state cause AND
  is the precondition for re-pushing without a power-cycle.
