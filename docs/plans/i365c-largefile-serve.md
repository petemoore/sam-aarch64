# i365c — large-file serve-from-disk: implementation plan

Ephemeral execution plan (deleted by the completing PR). Design home:
`docs/specs/i365-demo-architecture.md` (Wall 2). Goal: make the netboot TFTP
server (`src/netboot/netboot_server.asm`) serve files LARGER than the 1518-byte
RAM arena by streaming their bytes from the boot record's SD sectors on demand,
using the raw-CMD17 read `bd_record_read_hw` (already wired into the build).

## Design (decided — do not re-derive)

**Parallel disk table, arena path untouched.** Small files (≤`NB_FILE_MAX`) keep
the existing arena path in `nb_walk_entry` / `resolve_src` / `send_next_data`
BYTE-FOR-BYTE. Large files get a *separate* `NB_DISK_TABLE` and a disk-backed
serve path selected by a per-transfer `XFER_DISK` flag. Do NOT change the
`NB_SRC_TABLE` / `STORE` entry encoding (that would ripple through the Go parsers
and the manifest rewrite).

**Chain-following is MANDATORY (not linear).** The `.mgt` stores files
track-major, but a Trinity record stores them SIDE-major (`mgt_to_record_linear`,
`src/netboot/sd_push.asm:637-693`). So a file that is contiguous in the `.mgt` is
FRAGMENTED in record-linear space — you CANNOT serve it by incrementing the
record-linear sector. You MUST follow the MGT sector chain: each 512-byte sector
is `510 data bytes + [next-track, next-sector]`; end-of-chain = `00 00`
(`docs/notes/test-mgt-byte-layout.md:194-197`). Convert each chain (track,sector)
to a record-linear sector and read it via `bd_record_read_hw`.

**MGT (track_byte, sector) → record-linear (side-major), cited formula**
(`sd_push.asm:640-644`, `test-mgt-byte-layout.md:16-17`):
```
side = (track_byte & 0x80) >> 7
cyl  =  track_byte & 0x7F
record_linear = side*800 + cyl*10 + (sector - 1)     ; 0..1599
```
Sanity: directory sectors (track 0-3, side 0, sectors 1..10) → record_linear 0..39.

**The 9-byte body header.** The served bytes must equal what HLOAD returns (the
payload) — the arena path LDIRs `lengthMod16K` payload bytes, no header. On disk,
every file body is `[9-byte body header][payload]` packed into the 510-byte data
regions (`test-mgt-byte-layout.md` "9-byte body header"). So payload byte `O` is
at BODY offset `O + 9`; body offset `B` lives in chain-sector `B/510` at position
`B%510` within that sector's 510 data bytes.

**Incremental body-base cursor — no 32-bit division.** Keep per-transfer:
`XFER_CHAIN_TS` (current chain sector's [track,sector], 2 bytes),
`XFER_CHAIN_BODYBASE` (body offset of that sector's first data byte, 4 bytes LE),
`XFER_START_TS` (chain head [track,sector], 2 bytes). To serve payload bytes
starting at `XFER_OFFSET`: `body_start = XFER_OFFSET + 9`. Position the cursor:
```
if body_start < XFER_CHAIN_BODYBASE:            ; backward retransmit (rare)
    XFER_CHAIN_TS = XFER_START_TS; XFER_CHAIN_BODYBASE = 0
while body_start >= XFER_CHAIN_BODYBASE + 510:
    read current chain sector; XFER_CHAIN_TS = its [+510,+511] link
    XFER_CHAIN_BODYBASE += 510
pos = body_start - XFER_CHAIN_BODYBASE          ; 0..509
```
Then fill `chunk` bytes into `DISK_BLK_BUF`: read the current sector, copy
`min(need, 510-pos)` bytes from `SECT_BUF[pos]`, advance to the next chain sector
(follow the link, `BODYBASE += 510`, `pos = 0`), repeat until `need == 0`. This is
O(n) total for sequential serve (the cursor advances once through the chain) —
mandatory for hardware viability (O(n²) chain-rescans would be hours per file).

## Changes (exact)

### 1. Boot wiring (`netboot_main`, ~netboot_server.asm:1108-1191)
- Call `csd_set_bd_records` before the serve loop to populate `csd_base` /
  `BD_RECORDS` (needed by `bd_record_read_hw`). Mirror the ordering + shared-
  microcontroller comments in `netboot_serve.asm:1568-1595` (it is a raw-SPI
  transaction under DI; place near `drv_init`).
- Add a patchable `NB_BOOT_RECORD: defb 0` byte (host/loader patches it to the
  1-based boot record number — mirror `boot_record.asm:112 BOOT_CFG_RECORD`).
  Copy it into `BD_REC_REC` (16-bit LE) at boot. (Who patches it in the full demo
  is q77-adjacent; the i365c test patches it directly.)

### 2. `nb_walk_entry` — index large CODE files as disk-backed (netboot_server.asm:1404-1559)
- Currently `ret nz` at :1419 skips files whose full-page count != 0 (i.e. ≥16K),
  and :1432 skips size > `NB_FILE_MAX`. ADD a branch: when the entry is a CODE
  file that is large (pages != 0 OR size > NB_FILE_MAX), instead of skipping,
  build a `NB_DISK_TABLE` entry and return.
- Compute the 32-bit size = `pages*16384 + (lengthMod16K & 0x3FFF)` (pages at
  entry+`NB_ENTRY_PAGES`=0xEF, lengthMod16K at 0xF0-0xF1). Read the first
  (track,sector) at entry+0x0D..0x0E (the chain head).
- Extract the name as the existing code does (`nbwe_name`).
- Append to `NB_DISK_TABLE`: `name\0 + [track,sector] (2) + size32 (4)`, single-0
  terminated. Add room checks vs `NB_DISK_TABLE_LEN`. Also append the name+size32
  to `STORE` so listing/`resolve` sees it (STORE entry is name\0+size32 — same as
  small files; keep it uniform so `resolve` finds the name). NOTE: `resolve`
  (tftp_parse) sets `RESOLVE_ACTION`/`RESOLVE_SIZE` from STORE — confirm large
  files must be in STORE for `resolve` to OACK them; if so, add them.

### 3. `resolve_src` (netboot_server.asm:1629-1675) — disk-table fallback
- After the arena-table walk misses (`rsv_nomatch`), walk `NB_DISK_TABLE` the same
  way. On a match: set `XFER_START_TS` (2 bytes from the entry), `XFER_SIZE` (4),
  `XFER_DISK`=1, init the cursor (`XFER_CHAIN_TS = XFER_START_TS`,
  `XFER_CHAIN_BODYBASE = 0`), `scf`. Arena matches must set `XFER_DISK`=0.
- `rrq_hit` (:723) already copies RESOLVE_SIZE→XFER_SIZE then calls resolve_src
  (which overwrites). Ensure `XFER_DISK` is set to 0 by default before resolve_src,
  so a stale flag from a prior transfer can't leak.

### 4. `send_next_data` (netboot_server.asm:814) — 32-bit + streaming split
- At entry, branch on `XFER_DISK`: 0 → the existing arena path UNCHANGED;
  1 → `send_next_data_disk`.
- `send_next_data_disk`: 32-bit `remaining = XFER_SIZE - XFER_OFFSET`;
  `chunk = min(XFER_BLKSIZE, remaining)`; set `XFER_LAST_SHORT` when
  remaining ≤ blksize. Fill `DISK_BLK_BUF` via the cursor+re-block loop above
  (`bd_record_read_hw` into `SECT_BUF` per chain sector; CY-on-fail → treat as a
  read error: abort the transfer cleanly, do not serve garbage). `DATA_PTR =
  DISK_BLK_BUF`, `DATA_LEN = chunk`; advance `XFER_OFFSET += chunk` (32-bit);
  `build_data` + `jp srv_send_tbuf` — same tail as the arena path.

### 5. State + buffers (netboot_server.asm ~1288-1300, ~1910-1945)
- Add: `XFER_DISK: defb 0`, `XFER_START_TS: defs 2`, `XFER_CHAIN_TS: defs 2`,
  `XFER_CHAIN_BODYBASE: defs 4`, `SECT_BUF: defs 512`,
  `DISK_BLK_BUF: defs 1024` (max blksize), `NB_DISK_TABLE: defs NB_DISK_TABLE_LEN`
  (`NB_DISK_TABLE_LEN equ 256`), `NB_DISK_W: defs 2` (write cursor).
- `bd_record_read_hw` reads `BD_REC_REC` (record, LE16) + `BD_REC_LINEAR`
  (linearSec, LE16), dest in HL, CY on fail. Set BD_REC_REC once at boot; set
  BD_REC_LINEAR per chain sector from the (track,sector)→record_linear helper.

### 6. Add the (track,sector)→record_linear helper
```
; mgt_ts_to_record_linear — In: D = track_byte, E = sector(1-based). Out: HL = record_linear.
;   record_linear = ((D&0x80)>>7)*800 + (D&0x7F)*10 + (E-1)
```

## Test (`tools/netboot-oracle/z80/` — new file, SKIP_PRIVATE_TESTS)

Model on `netboot_server_faithful_test.go` (serve) + `list_records_body_test.go`
(record seeding). Build a `.mgt` containing ONE CODE file > 16 KB (e.g. 40 KB of
a distinctive ramp, via the build-disk tooling or `tools/sam-aarch64-format`/an
existing MGT builder — reuse whatever the record-vessel tests use). Seed it into a
record via `seedRecordFromMGT` (which lays each `.mgt` sector at its side-major
record-linear LBA — the same mapping the serve path inverts). Patch
`NB_BOOT_RECORD` to that record (like `stageBootRecord` patches BOOT_CFG_RECORD).
Boot the server to `nb_serve_loop`. Drive a TFTP RRQ for the file (blksize 512 AND
a second run at blksize 1024 — the 1024 case spans 3 MGT sectors/block, exercising
the re-block boundary), collect the DATA blocks, and assert the reassembled bytes
byte-match the file's payload (what HLOAD would return) via `tftp.ByteSource`.
Also assert a small file on the same record STILL serves from the arena
(regression), and that the SD model saw ZERO writes (`sd.WrittenSectors()` empty —
serve is read-only).

Update the Go table parsers only if you touched `STORE`/`NB_SRC_TABLE` encoding
(you should NOT have — the disk table is separate).

## Constraints for the implementer
- Work on the CURRENT branch `i365c-largefile-serve`. Commit your work with clear
  messages. Do NOT switch branches, do NOT open a PR, do NOT merge — the
  orchestrator does the PR + §3 review + merge.
- The arena/small-file path must stay byte-for-byte behaviourally identical;
  `TestNetbootServerFaithful` must still pass.
- `make netboot-server` must fit (≤32768) — it is at 18270 now, ample room.
- Gate on the FAITHFUL rig (this host has `~/sam-archive`; `SKIP_PRIVATE_TESTS`
  unset runs it). Do NOT rely on the `bdos_store.go` mock (it doesn't enforce the
  `"BDOS"@232` gate — i356). Iterate until the new test is byte-exact green.
- Cite `bd_record_read_hw` usage from `list_records.asm:388-402`; geometry from
  `sd_push.asm:640-644`; the seed helper from `trinload_idle_faithful_test.go:195`.
</content>
