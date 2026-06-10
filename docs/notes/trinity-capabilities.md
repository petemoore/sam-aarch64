# Trinity Hardware Capabilities

**Purpose:** Establish what the Quazar Trinity interface actually provides in terms of storage and I/O, to ground two sam-aarch64 design questions: (a) can the Trinity serve as spill storage for large comment data that doesn't fit in SAM RAM, and (b) what can Phase-3 TFTP work rely on? (Research: i58, 2026-06-10; updated with i61 corpus findings same day. Companion: `bdos-version-landscape.md`.)

---

## 1. What Is the Trinity?

The Quazar Trinity is a SAM Coupé expansion card produced by Colin Piggot (Quazar). The name reflects its three distinct subsystems:

1. **Ethernet controller** — a Microchip **ENC28J60** chip. [Source: Simon Owen blog 2007-11-30, https://simonowen.com/blog/2007/11/30/trinity-ethernet/; confirmed by `encdrv.asm` header comment ("Quazar Trinity ethernet driver for ENC28J60"), `github.com/simonowen/encdrv`; and `trinload.asm` error message `"ENC28J60 init failed."`, `github.com/simonowen/trinload`.]

2. **128 K EEPROM** — used for storing network settings, application configuration, and optionally a boot ROM block. Can hold up to 120 × 1 KB named chunks (plus a 64-byte index header per chunk), totalling ≈120 KB of user-accessible data. The Trinity Boot ROM feature stores a 1 KB B-DOS bootblock in chunk 1. [Source: `eeprom.asm`, `github.com/simonowen/trinload` — `count_empty` loop iterates `B=120` with `DE=64` stride; `read_chunk` loads `DE=&0400` = 1024 bytes. Trinity Boot ROM product page, https://www.worldofsam.org/products/trinity-boot-rom.]

3. **MMC/SD card slot** — provides mass storage. B-DOS 1.5t (a fork by Colin Piggot) and later Chris Pile's extensions support MMC and SD/SDHC cards up to 64 GB. [Source: https://www.worldofsam.org/products/b-dos; https://www.worldofsam.org/products/trinity-ethernet-interface.]

An on-board **microcontroller** acts as a central hub: it gates access to each of the three peripherals. All three are accessed over the **same SPI bus** but with separate enable lines controlled via port `&DC`. [Source: Simon Owen blog, https://simonowen.com/blog/2007/11/30/trinity-ethernet/.]

The Trinity also supports a **Trinity Boot ROM** chip option that replaces the standard SAM ROM page, allowing automatic B-DOS load from EEPROM at power-on. [Source: https://www.worldofsam.org/products/trinity-boot-rom.]

No RTC (real-time clock) or extra RAM is documented anywhere in the sources consulted. **UNCERTAIN: whether the Trinity board carries any auxiliary SRAM beyond the ENC28J60's 8 KB frame buffer.**

---

## 2. Port Map

All four Trinity ports are in the range `&DC`–`&DF`:

| Port | Device | Direction |
|------|--------|-----------|
| `&DC` | Microcontroller — select/status | `OUT` to select, `IN` bit 3 = busy flag |
| `&DD` | EEPROM (128 K) | SPI data byte |
| `&DE` | ENC28J60 Ethernet chip | SPI data byte |
| `&DF` | MMC/SD card | SPI data byte |

[Source: `eeprom.asm` uses `C=&DD`; `encdrv.asm` uses `C=&DE` (`wr_reg: LD C,&DE`); Simon Owen blog attributes `&DF` to MMC/SD; memory entry `trinity_hardware` cross-references these consistently.]

### Port `&DC` control byte values (from `encdrv.asm` and `eeprom.asm`)

| Value written | Meaning |
|---------------|---------|
| `%00000000` (`&00`) | All devices deselected |
| `%00010000` (`&10`) | EEPROM disabled (`eeprom_disable`) |
| `%00010001` (`&11`) | EEPROM enabled (`eeprom_enable`) |
| `%00100000` (`&20`) | ENC disabled (`eoff`) |
| `%00100001` (`&21`) | ENC enabled (`eon`) |
| `%00100011` (`&23`) | ENC pulse (disable+enable, `epulse`) |
| `%00101000` (`&28`) | ENC reset (`ereset`) |
| `%00000100` (`&04`) | Ethernet auto-nulling off (`enulloff`) |
| `%00101111` (`&2F`) | Ethernet auto-nulling on (`enullon`) |

Bit 3 read back from `&DC` = **busy flag**: `wait_ready` polls `IN A,(&DC); AND %00001000; JR NZ,wait_ready`. [Source: `encdrv.asm` lines 391–394, `github.com/simonowen/encdrv`.]

The SD card (`&DF`) select values are **not in any public source**, but have been **recovered empirically from period Trinity utility software (private archive)**: `&30` = SD deselect, `&31` = SD select, `&38` = SD initialise (microcontroller command; returns 1 for MMC, 2 for SD), `&3F` = SD select with auto-null. The Quazar programming manual (private reference materials) remains the authoritative confirmation source.

---

## 3. SPI Mechanics

SPI is **full duplex**: every `OUT (port), byte` is simultaneously paired with a byte clocked in from the device. The microcontroller stores that inbound byte so a subsequent `IN (port)` reads it without needing another clock cycle. This introduces a **one-byte lag**: what you read is what was clocked in _before_ the most recent write, so an extra dummy `OUT (port), 0` is needed to get the real response. [Source: Simon Owen blog, https://simonowen.com/blog/2007/11/30/trinity-ethernet/.]

For **MAC registers** in the ENC28J60, there is an additional hardware latency requiring a **double read** (two dummy writes). [Source: `encdrv.asm` `rd_m_reg` function, lines 292–302.]

The **auto-null feature** (firmware `&2F` / `enullon`) has the microcontroller automatically issue the dummy null write, removing it from the Z80 side of the bulk read loop. This is used in `rd_buf_mem` for bulk Ethernet frame reads (`encdrv.asm` lines 352, 368). **Auto-null for the SD port is available**: the `&3F` select value (parallel to Ethernet's `&2F`) is used in period software's bulk SD read path (private archive). 

---

## 4. Storage: SD Card

**Medium and capacity:** Standard MMC and SD/SDHC cards. B-DOS 1.5t beta 6 supports cards up to 64 GB. [Source: https://www.worldofsam.org/products/b-dos.]

**Filesystem / access model:** B-DOS does not use FAT. The SD card is divided into 800 KB **records**, each formatted identically to a SAM floppy disk (80 tracks × 10 sectors × 512 bytes × 2 sides ≈ 800 KB). B-DOS commands operate on records via raw sector access (READ AT / WRITE AT / VERIFY AT). [Source: https://www.worldofsam.org/products/b-dos.]

This is important for comment-spill use: **there is no FAT layer** accessible to arbitrary Z80 programs. A Z80 program wanting to use the SD card must either (a) go through B-DOS record/sector calls, or (b) implement its own raw-sector SPI driver and choose a storage layout.

**Existing Z80 driver code:**

- **SD read**: Colin Piggot published raw-sector read routines in **SAM Revival issue 21** (print), including CMD0/CMD8/CMD55/ACMD41/CMD16/CMD17 sequences for SD initialisation and single-block read. Source code was included on the accompanying disk. [Source: https://www.worldofsam.org/products/sam-revival-issue-21.] **This source is not in any public repository** — per samcoupe.com/samrevival.htm the SR21 cover disk carries both the article source AND the B-DOS 1.5t source+executable; available in private reference materials.
- **SD write**: The same SAM Revival 21 article covers write (CMD24). B-DOS 1.5t itself performs writes. **Write support exists conceptually, but no public Z80 source is available.**
- **EEPROM read/write**: Fully public in `eeprom.asm` (`github.com/simonowen/trinload`). Colin Piggot is the author. Both read and write functions are present and well-commented.

---

## 5. Throughput Estimates

All estimates below are **derived** from T-state counting of `encdrv.asm` at the SAM Coupé's 6 MHz Z80 clock. They are labelled as estimates.

**Z80 clock:** 6 MHz → 1 T-state ≈ 166.7 ns.

**Busy-wait loop overhead:** `wait_ready` = `IN A,(&DC)` (11 T) + `AND` (7 T) + `JR NZ` (12 T) = **30 T per iteration** when busy.

**Bulk read (ENC28J60 via `rd_buf_lp`, auto-null mode):**
- Inner loop per byte: `IN A,(&DC)` (11) + `AND` (7) + `JR NZ not-taken` (7) + `INI` (16) + `JR NZ taken` (12) = **53 T = ~8.8 µs/byte** (zero busy-waits).
- With 1 busy-wait iteration per byte: 83 T = ~13.8 µs/byte.
- Throughput range: **70–110 KB/s** depending on busy-wait frequency.

**Single SPI register read (eon + OUT + OUT + IN + eoff, no busy waits):** ~123 T ≈ 20.5 µs.

**SD card sector read (512-byte block, ESTIMATED):**
- Command phase (~8 SPI bytes at register-read speed): ~164 µs.
- Data phase (512 bytes at bulk speed, _if_ auto-null works for `&DF`): ~4.5 ms.
- Total best-case: ~4.7 ms → ~110 KB/s.
- Realistic (without confirmed auto-null, additional busy-waits): **20–80 KB/s** is a conservative working estimate.

**These figures are Z80-side-only.** SD card command-response latency (CMD17 → data token: typically 1–10 ms for a real card) is not included and would dominate at low transfer counts.

---

## 6. Phase-3 TFTP: What We Can Rely On

The `trinload`/`encdrv.asm` library directly addresses the Phase-3 need:

- **Ethernet driver** (`encdrv.asm`): complete, tested, public. Provides `drv_init`, `drv_read`, `drv_write`, `drv_exit`. ENC28J60 RX buffer = 6.5 KB; TX buffer = 1.5 KB. [Source: `encdrv.asm` EQU constants, `github.com/simonowen/trinload`.]
- **ARP responder** and **IPv4/UDP/ICMP** handling: fully implemented in `trinload.asm`. The existing code handles ARP requests, ICMP echo, and UDP datagrams. A TFTP sender on the SAM side would follow the same pattern.
- **TFTP WRQ (ship file to Pi)**: not in trinload, but the UDP write path (`drv_write`) is available. A TFTP WRQ state machine can sit directly on top of `encdrv.asm` — exactly the plan in `docs/specs/phase3-tftp-design.md`.
- **Fixed IP/MAC configuration**: trinload reads MAC and IP from EEPROM chunk "Trinity Network ". That same mechanism handles Phase-3 configuration.

**What Phase-3 cannot rely on without additional work:**
- Auto-MDIX: not documented for the Trinity. A crossover cable or a switch that handles it is needed. [UNCERTAIN: whether the ENC28J60 PHY circuit on the Trinity board supports MDI/MDI-X switching — the ENC28J60 does not natively; the Pi end may handle Auto-MDIX.]
- Flow control / full-duplex: ENC28J60 is 10BASE-T only, half-duplex as configured. [Source: ENC28J60 datasheet; `encdrv.asm` `PHCON1` writes `&0000` = half-duplex.]

---

## 7. Comment-Spill Use: Practicality Assessment

The idea: use the Trinity SD card as overflow storage for comment data that doesn't fit in SAM RAM.

**What would be needed:**
1. A Z80 SD card driver for port `&DF` (raw-sector read + write). Source from SAM Revival 21 or a clean reimplementation.
2. A storage layout for comment data (a flat file of variable-length comment strings, seekable by index).
3. B-DOS integration _or_ a standalone driver running outside DOS. If B-DOS is loaded, its hooks could be used; otherwise the driver must manage the SD SPI sequence itself.

**Key constraints:**
- Throughput (20–80 KB/s estimated) is adequate for occasional spill/fill of comment blocks during editing; it is _not_ suitable for streaming large comment volumes per instruction.
- Write support is confirmed in principle (SAM Revival 21, B-DOS 1.5t) but requires recovering the non-public driver source or reimplementing.
- No FAT: comment data would be stored in raw sectors with a custom simple index.
- This is a **new driver-writing project** (≈several days of Z80 work) on top of the existing codebase.

---

## 8. Verified / Likely / Unknown Summary

| Claim | Status |
|-------|--------|
| Ethernet chip = ENC28J60 | **VERIFIED** (code + blog) |
| Port `&DE` = Ethernet SPI | **VERIFIED** (`encdrv.asm`) |
| Port `&DD` = EEPROM SPI | **VERIFIED** (`eeprom.asm`) |
| Port `&DC` = microcontroller select/status | **VERIFIED** (both drivers) |
| Port `&DF` = SD card SPI | **VERIFIED** (Simon Owen blog, memory entry) |
| Port `&DC` bit 3 = busy flag | **VERIFIED** (`wait_ready` in `encdrv.asm`) |
| EEPROM capacity = 128 K | **VERIFIED** (Quazar product page) |
| SD cards up to 64 GB work | **VERIFIED** (B-DOS worldofsam page) |
| SD storage model = raw sectors, 800 KB records | **VERIFIED** (B-DOS docs) |
| SD read/write driver exists in print (SR21) | **VERIFIED** (worldofsam SR21 page) |
| SD driver is public on GitHub | **VERIFIED NOT** (none found) |
| ENC28J60 = 10BASE-T half-duplex only | **VERIFIED** (datasheet + PHCON1 init) |
| EEPROM read/write both work (code available) | **VERIFIED** (`eeprom.asm`) |
| Ethernet driver ready for TFTP reuse | **VERIFIED** (`trinload`/`encdrv.asm`) |
| Throughput ~70–110 KB/s bulk (Ethernet path) | **LIKELY** (derived from T-state analysis) |
| Throughput ~20–80 KB/s SD sectors | **LIKELY** (estimated; no SD benchmark found) |
| Auto-null mode works for SD port `&DF` | **LIKELY** (`&3F` select-with-auto-null observed in period software's bulk SD path — private archive) |
| Port `&DC` select byte value for SD | **VERIFIED** (`&30`/`&31`/`&38`/`&3F` — recovered from period Trinity utility software, private archive) |
| Trinity PHY supports Auto-MDIX | **UNKNOWN** (ENC28J60 does not natively; board-level unclear) |
| No RTC or extra RAM on board | **LIKELY** (not mentioned anywhere; name = "Trinity" = 3 things) |

---

## 9. Open Questions (Require Hardware or Documentation)

1. ~~Port `&DC` SD-select byte value~~ — **RESOLVED** (`&31` select / `&30` deselect / `&38` init / `&3F` auto-null; recovered from period Trinity utility software, private archive).
2. ~~Auto-null for `&DF`~~ — **RESOLVED (LIKELY)**: `&3F` is the SD select-with-auto-null value (same evidence).
3. **SD initialisation sequence timing**: real cards can hold `CMD17` response for 1–10 ms. Does the Trinity's microcontroller buffer this, or does the Z80 poll a status register?
4. **Auto-MDIX**: does the Trinity's ENC28J60 circuit include MDI/MDI-X switching, or does Phase-3 need a crossover cable?
5. **EEPROM address space above 64 K**: the 128 K EEPROM needs a 17-bit address, but the `eeprom.asm` driver only emits a 2-byte address after the opcode. Is the upper half reached via a bank-select mechanism in the microcontroller, or inaccessible? Affects how much EEPROM is truly usable.
6. **SD write source recovery**: the SAM Revival 21 cover disk carries the SD driver article source AND the B-DOS 1.5t source+executable (samcoupe.com/samrevival.htm); available in private reference materials. NOTE: the B-DOS hook route (see `bdos-version-landscape.md`) may make a raw SPI driver unnecessary.

---

## Sources

- `github.com/simonowen/trinload` — `encdrv.asm` (ENC28J60 driver), `eeprom.asm` (EEPROM driver by Colin Piggot), `trinload.asm` (ARP/IPv4/UDP harness), `ReadMe.txt`
- `github.com/simonowen/encdrv` — standalone encdrv release
- `github.com/LongSteve/z80` — Trinity bootloader (modified from Colin Piggot's SR23 source)
- Simon Owen blog 2007-11-30: https://simonowen.com/blog/2007/11/30/trinity-ethernet/
- Quazar product page: https://www.samcoupe.com/hardtrin.htm
- World of SAM — Trinity: https://www.worldofsam.org/products/trinity-ethernet-interface
- World of SAM — B-DOS: https://www.worldofsam.org/products/b-dos
- World of SAM — SAM Revival issue 21: https://www.worldofsam.org/products/sam-revival-issue-21
- World of SAM — Trinity Boot ROM: https://www.worldofsam.org/products/trinity-boot-rom
- `docs/specs/phase3-tftp-design.md`
- Memory entry `trinity_hardware` (verified against the above rather than trusted)
