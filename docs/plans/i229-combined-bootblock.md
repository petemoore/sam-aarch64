# i229 — combined patched-bootblock image (Stage 1 of the i135c no-brick plan)

Ephemeral plan. Deleted by the PR that completes i229. Stage 1 of the staged
i135c plan: **build + emulation-verify the combined patched bootblock**, so it can
be RAM-tested (i230, Pete-present) and then flashed (i135c) — never rushing boot
code (prime directive).

**q50 is RESOLVED (Pete, 2026-06-25) and deleted** — i229 is UNBLOCKED. The
resolution is recorded verbatim in the i229 registry row; the three decisions it
settled are folded into this plan below. (Earlier drafts of this file said "i229
now depends on q50 / must not be built until answered" — that prose is stale and
has been removed.)

## The q50 decisions (the design, settled)

1. **SPLICE (3 bytes change).** At bootblock file offset `&9E` (logical `&409E`)
   the original `CALL &805F` is bytes `CD 5F 80`. Change it to `CALL inject`
   where `inject` lives in the free space at file `&15E..&3FF` (logical
   `&415E..&43FF`). The injected routine:
   ```
   inject: call  &805F                 ; the real B-DOS init (returns on hardware)
           call  samboot_read_config    ; i176: CY+HL=record (auto-boot) or NC (none)
           ret   nc                     ; no auto-boot -> RET to &40A1 = restore: (verbatim)
           ld    a, l
           ld    (BD_BOOT_RECORD), a
           jp    bdos_boot_record        ; i122a: HRECORD select + ALHK boot, no return
   ```
   Only the 3 bytes at `&409E` change; `restore:` (`&40A1`) and the screen-redraw
   tail (`&40A9..&40BB`, `CALL &06B5` + pokes + `JP &102F`) stay byte-identical.
   On the no-auto-boot path `inject` RETs to `&40A1` (the address after the
   `CALL inject` at `&409E`), so Colin's own screen redraw + BASIC exit run exactly
   as before — **our code never tramples the screen**, so no `samboot_stripes`
   redraw is needed (this supersedes the i135d prototype's i112 stripes fold; the
   stripe PIXELS are an i230 hardware concern).

2. **CONFIG READ: BY-NAME via `find_index`** for the `"SAMBOOT Config  "` chunk
   (16 bytes, two trailing spaces) per `samboot.md §4` — NOT by a fixed chunk
   number. This is exactly what `samboot_read_config` (src/netboot/samboot_config.asm,
   i176) already does. The 1 KB scratch-buffer RAM home is an emulation-verified
   implementation detail (this plan's choice), not a Pete call.

3. **VERIFY SCOPE: LIMITED.** Assert (a) the **spliced** chunk-1 LOADS + runs
   coherently to `&805F` (the 12 `read_chunk` B-DOS loads, then `CALL inject` ->
   `inject` -> `call &805F`), and (b) the decision logic (config -> dispatch) via a
   host decision test. The post-`&805F` in-chain decision (the config read + the
   ALHK record boot) and the stripe PIXELS go to the **i230 hardware test**. Do
   **NOT** teach the emulator past B-DOS init (i232) — `&805F` does not return in
   the Go core, by design.

## Confirmed byte-level map (verified against the local capture, 2026-06-25)

`chunk 1` = `~/sam-archive/samboot-capture/eeprom.bin` file offset `0`, runs at
`ORG &4000`. Verified directly from the bytes:

| file off | logical | bytes | meaning |
|---|---|---|---|
| `&9E` | `&409E` | `CD 5F 80` | `CALL &805F` — **the 3 bytes the splice patches** |
| `&A1` | `&40A1` | `3E 00 D3 FA 3E 00 D3 FB` | `restore:` (`LD A,0/OUT (250)/LD A,0/OUT (251)`) |
| `&A9` | `&40A9` | `CD B5 06 ...` | screen tail `CALL &06B5` + pokes + `JP &102F` |
| `&15D` | `&415D` | `C9` | last bootblock byte (`RET`) |
| `&15E..&3FF` | `&415E..&43FF` | all `00` | **674 free bytes — the injection space** |

Resident bootblock helpers (by-NUMBER, NOT reused — see below): `read_chunk &40BD`,
`get_chunk &412C`, `eeprom_enable &4103`, `eeprom_disable &410A`, `wait_ready &411A`.
**There is NO `find_index` in the bootblock** — BY-NAME (q50 decision 2) requires
bringing our own `find_index`, so the inject is self-contained (below) and does NOT
couple to Colin's proprietary resident addresses.

## Byte budget — it fits (the make-or-break finding)

Free space = **674 bytes**. The minimal read-only closure the inject needs:

| piece | ~bytes |
|---|---|
| `inject` glue (call &805F / read / ret nc / ld / jp) | ~14 |
| `samboot_read_config` glue + `samboot_cfg_name` (16) | ~74 |
| `wait_ready` (from samboot_config.asm) | ~31 |
| `find_index` + `index_loop` + `index_back` | ~81 |
| `check_index` + `check_loop` + `check_return` + `index_store`(18) code | ~29 |
| `read_chunk` + `read_cloop` | ~58 |
| `eeprom_enable` + `eeprom_disable` + `exit` + `write_disable` | ~36 |
| `get_chunk` + `chunk_loop` | ~12 |
| `bdos_boot_record` + `bdos_select_record` (2 RST-8 shims) | ~25 |
| **total code** | **~360** |

~360 < 674 (≈310 B margin). The **full** `eeprom.asm` + `bdos_seam.asm` is ~818 B
(over) because of the write/delete/find-empty/read-index paths and the
HSAVE-write/claim path — auto-boot needs NONE of them. So the bootblock build must
**conditionally exclude** the unneeded routines (the gating pattern bdos_seam.asm
already uses) and **relocate** the 1 KB `chunk` buffer + the index/name storage out
of the flashed image into a RAM scratch home.

`find_index` reads each 64-byte index header inline (it does NOT call `read_index`),
and `read_chunk` ends in `exit` which calls `write_disable` — so the kept set is
exactly: `find_index, check_index (+index_store), read_chunk, eeprom_enable,
eeprom_disable, exit, write_disable, get_chunk`. Everything else is gated out.

## Implementation

### File 1 — `src/netboot/eeprom.asm` (gate + relocate, behind `SAMBOOT_BOOTBLOCK`)

This file is vendored from trinload but already lightly adapted (its `wait_ready`
is commented out). Add `SAMBOOT_BOOTBLOCK`-conditional guards that change **zero
bytes** for every existing includer (none of them define the flag) and only take
effect for the bootblock build:

1. **Relocate storage.** Wrap the storage block (`value`/`part`/`total`/`name`/
   `description`/`chunk`, lines 51-62) and `index_store` (line 237):
   ```
   if defined(SAMBOOT_BOOTBLOCK)
   value       equ SAMBOOT_SCRATCH+0
   part        equ SAMBOOT_SCRATCH+1
   total       equ SAMBOOT_SCRATCH+2
   name        equ SAMBOOT_SCRATCH+3
   description equ SAMBOOT_SCRATCH+19
   chunk       equ SAMBOOT_SCRATCH+65        ; 1 KB, ends SAMBOOT_SCRATCH+1089
   index_store equ SAMBOOT_SCRATCH+1089      ; 18 bytes
   else
   value:       DEFB 0
   ... (verbatim original) ...
   index_store: DEFS 18
   endif
   ```
   (`SAMBOOT_SCRATCH` is defined by the includer — see File 2.) The EQU layout
   mirrors the verbatim DEFS sizes exactly.
2. **Gate the BASIC JP jump table** (lines 40-47) and the **unused routines**
   (`count_empty`, `find_empty`, `delete_index`, `read_index`, `write_index`,
   `write_chunk`+`write_256`, `write_enable`, `write_delay`) behind
   `if defined(SAMBOOT_BOOTBLOCK)==0 ... endif`. Keep `find_index`, `check_index`,
   `read_chunk`, `eeprom_enable`, `eeprom_disable`, `exit`, `write_disable`,
   `get_chunk` unconditionally (their bytes are unchanged).
   - NOTE: `exit` (line 448) tail-calls `write_disable` — keep `write_disable`.
     `find_index`/`read_chunk` use `eeprom_enable/disable` + `wait_ready` (the
     latter supplied by samboot_config.asm). `read_chunk` uses `get_chunk`.
3. Run `go test ./tools/netboot-oracle/...` after this edit to confirm the
   existing includers (smoke/server/serve/client/dumper) still build & pass
   byte-for-byte — the guards must be invisible to them.

### File 2 — `src/netboot/samboot_bootblock.asm` (NEW — the real splice inject)

Replaces the i135d prototype `samboot_inject.asm` (which folded the now-obsolete
`samboot_stripes`). Structure:
```
                org     SAMBOOT_INJECT_ORG          ; &415E (the free space)
SAMBOOT_SCRATCH:        equ &E000                   ; 1 KB+ RAM scratch home for the
                                                    ; config read. EMULATION-VALID (flat
                                                    ; RAM in the harness); the HARDWARE-SAFE
                                                    ; address is confirmed at i230 (Pete
                                                    ; present, post-B-DOS-init free RAM).
SAMBOOT_BOOTBLOCK:      equ 1                        ; -> eeprom.asm gates+relocates

inject:         call    &805F                       ; real B-DOS init (no return in the Go core)
inject_decision:                                    ; host decision test enters HERE (skips &805F)
                call    samboot_read_config
                ret     nc
                ld      a, l
                ld      (BD_BOOT_RECORD), a
                jp      bdos_boot_record

                include "samboot_config.asm"        ; samboot_read_config + wait_ready + (gated) eeprom.asm
                ; bdos_boot_record + bdos_select_record only — NOT the write/claim path.
                ; Pull them in WITHOUT NETBOOT_WANT_CLAIM (excludes the WRQ write path,
                ; bdos_seam.asm:663-928) and WITHOUT NETBOOT_HOSTTEST (keeps the RST-8
                ; dispatch so bdos_boot_record + BD_BOOT_RECORD are defined). If the
                ; ungated bdos_seam.asm still pulls in unused routines (bdos_find_free_record,
                ; bdos_inspect_record, ...) that push the image over 674 B, add a
                ; `SAMBOOT_BOOTBLOCK`-gate around those too (same pattern), keeping only
                ; bdos_boot_record + bdos_select_record + BD_BOOT_RECORD.
                include "bdos_seam.asm"
```
- samboot_config.asm currently does `org &8000` at its top — for this build that
  org must NOT fire (the inject supplies `org &415E`). Gate samboot_config.asm's
  `org &8000` behind `if defined(SAMBOOT_BOOTBLOCK)==0` (one-line guard), mirroring
  bdos_seam.asm's `NETBOOT_STANDALONE` org guard.
- After assembling, **check the end address ≤ `&43FF`** (assert in the Makefile
  step, see File 4) — the inject MUST fit the 674 free bytes.

### File 3 — `tools/samboot-splice/main.go` (NEW — the splice tool)

A host Go tool. Reproducibly produces the combined ≤1024-byte chunk-1 image; the
proprietary capture bytes are NEVER committed (output goes to a git-ignored path).
```
flags: -eeprom <path>   default ~/sam-archive/samboot-capture/eeprom.bin
       -inject <path>   default build/samboot_bootblock.bin
       -out <path>      default build/samboot_bootblock_chunk1.bin (git-ignored)
logic:
  1. read eeprom[0:1024]  -> chunk1 (the original bootblock; offset 0 = device &2000)
  2. read inject bytes    -> must be <= 674 (else error: "inject too big for free space")
  3. assert chunk1[0x9E:0xA1] == {0xCD,0x5F,0x80} (the CALL &805F splice site; else
     error — the capture/layout changed, do not guess)
  4. injectLogical = 0x415E; lo,hi = injectLogical&0xFF, injectLogical>>8
     patch chunk1[0x9E:0xA1] = {0xCD, lo, hi}   (CALL inject)
  5. copy inject bytes into chunk1[0x15E : 0x15E+len(inject)]
  6. assert len(chunk1)==1024; write chunk1 to -out
```
Add a unit test `tools/samboot-splice/main_test.go` that runs the splice against a
SYNTHETIC 1 KB chunk-1 (CD 5F 80 at 0x9E, zeros at 0x15E+) + a synthetic inject
blob, and asserts the 3 patched bytes + the spliced region + the untouched
`restore:`/screen tail — so the tool is tested WITHOUT the proprietary capture.

### File 4 — `Makefile` wiring

- Replace the `netboot-samboot-inject` target's source with `samboot_bootblock.asm`
  (org `&415E`; `SAMBOOT_BOOTBLOCK`/`SAMBOOT_SCRATCH`/`SAMBOOT_INJECT_ORG` are set
  inside the asm via equ, so no `-D` flag is needed; pass nothing that defines
  NETBOOT_HOSTTEST/NETBOOT_WANT_CLAIM). Emit `build/samboot_bootblock.bin` + `.map`.
  Add a post-assemble shell assert that the highest address in the `.map` is
  `<= &43FF` (fail the build otherwise).
- Add `samboot-splice: build/samboot_bootblock.bin` -> `go run ./tools/samboot-splice`
  producing `build/samboot_bootblock_chunk1.bin`. (Skips gracefully — exit 0 with a
  log — if the proprietary `eeprom.bin` is absent, like the other capture-gated
  convenience targets; the reset-chain TEST is where the no-silent-skip rule
  applies, not this build helper.)
- Add `build/samboot_bootblock_chunk1.bin` (and any `build/*chunk1*`) to
  `.gitignore` — it contains proprietary capture bytes.

### File 5 — `tools/netboot-oracle/z80/samboot_bootblock_test.go` (NEW — both verifications)

Two test groups, both honest to the q50 LIMITED scope:

**(A) Decision logic** (port samboot_inject_test.go, minus the stripes probe; enter
at the `inject_decision` symbol so &805F is skipped). Build = `samboot_bootblock.bin`.
Reuse `runInject`-style: AttachIO(ENC28J60) + AttachBDOS(store), optionally
`ProgramNamedChunk(slot, samboot.ChunkName, cfg)`, `CallEntry("inject_decision")`,
assert `store.Selected()`/`store.Boots()` for: auto-boot rec 7, second record 0x12,
mode=none (no boot), absent chunk (no boot), bad version (no boot). This exercises
the gated+relocated eeprom.asm reader (scratch at &E000 is writable in the flat
harness), closing the "did the gating break the reader" gap.

**(B) Spliced reset chain** (the i229 headline; mirror samboot_real_boot_test.go's
`TestRealBootLoadsBDOSCoherently`, but with the SPLICED chunk-1). Use
`requirePrivateCapture` (FAILS if absent unless `SKIP_PRIVATE_TESTS=true` — the i253
rule; NO silent skip). Steps:
  1. load rom.bin + eeprom.bin; build the device-linear EEPROM
     (`deviceLinearEEPROM`); **replace device `&2000..&23FF` (= file offset 0,
     chunk 1) with the spliced image** (run the splice in-Go from
     `build/samboot_bootblock.bin` + the captured `eeprom[0:1024]`, OR shell out to
     the splice tool and read its output — prefer in-Go to keep the test
     self-contained).
  2. boot from reset (`RunBootFrom(0x0000, StepCap)`), trace PCs.
  3. assert: `read_chunk &40BD` hit **12** times (B-DOS chunks 2..13 still load —
     the splice didn't break the loader); the splice site `&409E` (now `CALL inject`)
     is reached; `&415E` (`inject`) is reached; and `&805F` is reached **via the
     inject** (i.e. `inject`'s `call &805F`, not the original direct call). The run
     then halts inside B-DOS init (finalPC ≥ &8000) — expected; do NOT assert past
     it (q50 decision 3 / i232).
  4. a control assertion that `&409E` now disassembles to `CD <lo> <hi>` pointing at
     `&415E` (read the spliced chunk-1 bytes), proving the patch is the 3-byte
     `CALL inject` and `restore:`/screen tail at `&40A1`/`&40A9` are byte-identical
     to the original capture.

### Cleanup / hygiene (for the §3 review)

- DELETE `src/netboot/samboot_inject.asm` + `samboot_inject_test.go` (superseded by
  `samboot_bootblock.asm` + `samboot_bootblock_test.go`; the q50 design drops the
  `samboot_stripes` fold). Update any Makefile/CI references.
- DELETE this plan file in the completing PR.
- Update `docs/notes/samboot-bootblock-analysis.md` if its §3 TODO-hook prose now
  conflicts with the settled q50 splice (point it at this design / the registry row).

## Verification (run before opening the PR)

```
make netboot-samboot-inject              # assembles samboot_bootblock.bin, asserts <= &43FF
make samboot-splice                      # produces the git-ignored combined chunk-1
go test ./tools/netboot-oracle/...       # decision logic (A) + spliced reset chain (B)
go test ./tools/samboot-splice/...       # splice tool unit test (synthetic, no capture)
make ci-netboot-z80                      # the netboot Z80 CI aggregate
make registry-sync-check                 # registry views in sync
```
SimCoupé is NOT used here (it does not model the Trinity EEPROM read/boot); the Go
core is the faithful emulation for this path (CLAUDE.md §7 — the netboot-oracle Z80
core IS the everything-emulator for Trinity).

## Acceptance (i229)

- The splice tool produces a reproducible ≤1024-byte combined chunk-1 from the
  local `eeprom.bin` + our inject, with **no proprietary bytes committed**.
- The inject image fits the 674 free bytes (`.map` end ≤ `&43FF`, asserted in the build).
- The spliced reset-chain test boots coherently to `&805F` via `inject` (12 chunk
  loads), and the decision-logic test passes all 5 cases — both green under
  `make ci-netboot-z80`.
- Then i230 (hardware RAM test, Pete present) and only after that i135c (flash).

## What stays hardware-only (i230 RAM test + i135c flash)

The stripe PIXELS, the real ALHK record load, the physical reset-handoff timing,
the post-`&805F` in-chain config read, AND the hardware-safety of the
`SAMBOOT_SCRATCH` RAM address. Emulation-verified is not hardware-verified
(CLAUDE.md §5/§7).
