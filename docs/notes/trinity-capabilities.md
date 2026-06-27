# Trinity Hardware Capabilities

**Purpose:** Establish what the Quazar Trinity interface actually provides in terms of storage and I/O, to ground two sam-aarch64 design questions: (a) can the Trinity serve as spill storage for large comment data that doesn't fit in SAM RAM, and (b) what can Phase-3 TFTP work rely on? (Research: i58, 2026-06-10; updated with i61 corpus findings same day. Companion: `bdos-version-landscape.md`.)

---

## 1. What Is the Trinity?

The Quazar Trinity is a SAM Coupé expansion card produced by Colin Piggot (Quazar). The name reflects its three distinct subsystems:

1. **Ethernet controller** — a Microchip **ENC28J60** chip. [Source: Simon Owen blog 2007-11-30, https://simonowen.com/blog/2007/11/30/trinity-ethernet/; confirmed by `encdrv.asm` header comment ("Quazar Trinity ethernet driver for ENC28J60"), `github.com/simonowen/encdrv`; and `trinload.asm` error message `"ENC28J60 init failed."`, `github.com/simonowen/trinload`.]

2. **128 K EEPROM** — used for storing network settings, application configuration, and optionally a boot ROM block. Can hold up to 120 × 1 KB named chunks (plus a 64-byte index header per chunk), totalling ≈120 KB of user-accessible data. The Trinity Boot ROM feature stores a 1 KB B-DOS bootblock in chunk 1. [Source: `eeprom.asm`, `github.com/simonowen/trinload` — `count_empty` loop iterates `B=120` with `DE=64` stride; `read_chunk` loads `DE=&0400` = 1024 bytes. Trinity Boot ROM product page, https://www.worldofsam.org/products/trinity-boot-rom.]

3. **MMC/SD card slot** — provides mass storage. B-DOS 1.5t (a fork by Colin Piggot) and later Chris Pile's extensions support MMC and SD/SDHC cards up to 64 GB. [Source: https://www.worldofsam.org/products/b-dos; https://www.worldofsam.org/products/trinity-ethernet-interface.]

An on-board **microcontroller** acts as a central hub: it gates access to each of the three peripherals. All three are accessed over the **same SPI bus** but with separate enable lines controlled via port `&DC`. [Source: Simon Owen blog, https://simonowen.com/blog/2007/11/30/trinity-ethernet/.]

The microcontroller is a **Microchip PIC16F74** (`-I/P`: industrial-temp, **40-pin PDIP** — the 40-pin member of the PIC16F7x family; the 28-pin 4K-flash part is the PIC16F73). [Source: Pete read the part number directly off the physical chip on his board, 2026-06-29; datasheet [DS30325B](https://ww1.microchip.com/downloads/aemDocuments/documents/MCU08/ProductDocuments/DataSheets/30325b.pdf); Flash programming spec DS30324.] The PIC16F74 is a mid-range 8-bit PIC with **4K×14 words of program flash, 192 bytes of RAM, no data-EEPROM**, and an **SSP module (hardware SPI master)** plus a USART — i.e. its host link to the SAM (port `&DC`) is the SPI/SSP path, the PIC's own SSP is the SPI bus master to the ENC/SD/EEPROM, and the BUSY flag (`&DC` bit 3) is a *firmware* construct of this PIC, not silicon.

This small SPI-bridge firmware (≤8 KB) is the shared "one PIC" behind the SD/ENC/EEPROM BUSY contention investigated in `trinity-sd-z80-interface.md` §8u/§8v. The datasheet pins down a concrete wedge mechanism: the SSP master has error flags **SSPOV** (receive-overflow) and **WCOL** (write-collision) that "must be cleared in software" (DS30325B §9). If the firmware's per-byte wait-loop polls **BF** (buffer-full) to detect completion but does not handle an SSPOV/WCOL raised by a stalled ENC SPI op (Simon Owen's documented ENC transmit-hang), it spins waiting for a completion that never latches and **never reaches the code that clears `&DC` BUSY** — so the SAM's `&A7CC` busy-poll spins forever. That is the §8v permanent-wedge, sharpened from "abstract ENC stall" to a specific SSP failure path.

**Firmware extraction is almost certainly blocked** (datasheet-grounded):
- The 4K×14 flash is read over ICSP — **MCLR/VPP = pin 1, PGC/RB6 = pin 39, PGD/RB7 = pin 40, VDD, VSS** (40-pin pinout) — with a PICkit (or a Pi bit-bang programmer), **only if the code-protect bit CP0 is clear**.
- On the Flash F7x, a code-protected read returns **all `0x0000`** (not scrambled — that lore is the older EPROM PICs). DS30324 §2.3.1.3: *"If the device is code protected, user program memory will read all '0's."* So you can **tell locked-vs-not by a single read** (all-zeros ⇒ protected; or read the always-readable config word's CP0 bit).
- **Un-protecting requires a Chip Erase, which WIPES the firmware** — so a read is safe (and worth ~10 min with a PICkit to confirm), but **never chip-erase** this part. Glitch/decap bypasses are destructive/lab-grade, not realistic for a board we must keep working.
- A **serial connection cannot dump it** — the firmware bridges via the SSP, not the USART; no serial monitor is expected. Serial is at best a runtime-observation channel.

The pragmatic substitutes for the locked firmware: **ask Colin Piggot** (its designer — the definitive source for the exact BUSY/SSP-wait semantics; `q61`), a **logic analyzer on the PIC↔peripheral SPI lines** (observes the SSP stall directly), and **black-box measurement** of `&DC` bit 3 (the one port not routed through the PIC, so always readable) across the SD-write sequence.

### Board component inventory (photo-confirmed)

A high-quality photo of the board front — `~/sam-archive/trinity-docs/photos/trinity-board-v1.1-front-IMG_20260629_140203.jpg` (Pete, 2026-06-29) — confirms the parts directly from their package markings. Silkscreen: **"QUAZAR TRINITY ETHERNET INTERFACE · V1.1 (C) 2019 COLIN PIGGOT"** (matches the `"TRI v1.1"` firmware IDENT).

| Part (marking) | Pkg | Role |
|---|---|---|
| **PIC16F74-I/P** (Microchip) | 40-pin DIP, **socketed** | The gateway microcontroller (SSP/SPI bridge; owns the `&DC` BUSY firmware). Socketed ⇒ can be pulled and read in a standalone PICkit/ZIF, no in-circuit ICSP. |
| **ENC28J60-I/SP** (Microchip) | 28-pin DIP | Ethernet controller. |
| **25LC1024** (Microchip; silkscreen "EPROM") | 8-pin DIP | 128 K SPI EEPROM (network/config/boot-block store). |
| **GAL16V8D** | 20-pin DIP | Programmable logic = the SAM-bus glue / address decode (the manual's "general control logic"); decodes ports `&DC–&DF` to the PIC. Its JEDEC equations are a separate readable artifact if ever needed (bus decode, not the BUSY logic). |
| **20.000 MHz crystal** | HC-49 can | System clock (near the PIC). |
| RJ45 + magnetics, microSD-in-adapter slot, A/B status LEDs, electrolytics, resistor networks, the SAM expansion **edge connector** | — | Per the manual's component tour. |

So the board carries **four ICs** of interest: the PIC16F74 (MCU), ENC28J60 (ethernet), 25LC1024 (EEPROM), and the GAL16V8D (bus glue). No RTC or auxiliary SRAM is present (confirmed against the parts list above). A photo of the **back** would additionally show any ICSP/programming header and the routing, if needed.

The Trinity also supports a **Trinity Boot ROM** chip option that replaces the standard SAM ROM page, allowing automatic B-DOS load from EEPROM at power-on. [Source: https://www.worldofsam.org/products/trinity-boot-rom.]

No RTC (real-time clock) or extra RAM is documented anywhere in the sources consulted. The manual's labelled **component tour** (scan `IMG_20260617_162538.jpg`) enumerates every board part — microcontroller, EEPROM, ENC28J60, status LEDs, RJ45 socket, Ethernet-interrupt jumper, MMC/SD flashcard slot + LED, general control logic, voltage regulator, standoffs — and lists **no RTC and no auxiliary SRAM**. So the board carries no auxiliary SRAM beyond the ENC28J60's 8 KB frame buffer as far as the manual shows. (Photos checked; not shown to the contrary — a tiny unlabelled part can't be ruled out from a parts list alone, but nothing in the docs suggests one.)

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

The SD card (`&DF`) select values are **confirmed by B-DOS 1.5t itself** — the fork is their canonical consumer (the B-DOS 1.5t analysis, i71, `bdos-trinity-fork-analysis.md`), upgrading the earlier empirical recovery from period Trinity utility software: `&30` = SD deselect, `&31` = SD select, `&38` = SD initialise (microcontroller command; returns 1 for MMC, 2 for SD), `&3F` = SD select with auto-null. In addition, `&04` is the SD-idle / all-deselect state the fork writes around every transaction (auto-null off). The microcontroller identity probe writes commands `&08` then `&09` to `&DC` and reads the replies — expected `'T'`,`'R'` — from port `&DD` (the EEPROM SPI data port, not `&DF`). `IN (&DC)` bit 1 = card present; bit 2 = the write-protect switch sense, inverted.

### `IN &DC` — the Trinity Status Register (verified against the manual scan)

The manual's "Trinity Status Register" section gives the full read-back byte as
**`%1100BWFE`** — the **top nibble is FIXED** (bits 7,6 = 1; bits 5,4 = 0), so a read
is never `&00`; the idle no-card value is **`&C0`**. The low nibble is dynamic:

| Bit | Name | Meaning |
|---|---|---|
| 3 | BUSY | microcontroller busy — `1` = do not access any Trinity port except `IN &DC` |
| 2 | WRITE | flash-card write status — `1` = write enabled, `0` = disabled (driver reads it `CPL/AND 4`, sense-inverted) |
| 1 | FLASH | flash card inserted — `1` = yes |
| 0 | ENCINT | ENC28J60 interrupt — `1` = interrupt triggered |

[Source: Trinity manual "Trinity Status Register", scan `~/sam-archive/trinity-docs/photos/IMG_20260617_162550.jpg`; verified visually 2026-06-23.]

### `&08`–`&0F` — the 8-byte firmware IDENT string

The identity probe is a full **8-byte IDENT string**, not just two bytes: `OUT &DC` with
each of `&08`..`&0F` then `IN &DD` returns the Nth character. The string is
**`"TRI v1.1"`** (`T`,`R`,`I`,*space*,`v`,`1`,`.`,`1` — "Trinity v1.1", the firmware
version). The 4th char is a **space**, **settled by the high-resolution manual scan**: the
IDENT table prints the 4th glyph as an empty pair of parentheses `()` — i.e. a literal
SPACE, the same as the OCR — and no literal `"TRINv1.1"` form appears anywhere in the
photographed manual. `chk_trinity` only reads the first two (`'T'`,`'R'`) as a presence
gate. [Source: Trinity manual "Trinity – Ident", scan `IMG_20260617_162601.jpg`;
re-verified at high resolution 2026-06-24. This resolves the `"TRI v1.1"` vs `"TRINv1.1"`
question that `docs/specs/trinity-emulation-fidelity.md` lists as "genuinely unspecified":
the manual settles it as a SPACE — the emulator's `"TRI v1.1"` constant is correct.]

The host emulator (`tools/netboot-oracle/z80/enc28j60.go`, `sdcard.go`) models both the
`&C0`-based status register and the full IDENT string (`TestTrinityStatusRegister`,
`TestTrinityIdentString`). **EEPROM addressing gotcha:** the i87a capture `eeprom.bin`
is *chunk-ordered* (file offset 0 = device `&2000` = chunk 1), so any tool loading it
must un-rotate to device-linear first — see `samboot-bootblock-analysis.md` §8.

### `&DC` command bytes — independently confirmed by the manual

The control-byte table above (recovered from `encdrv.asm`/`eeprom.asm` and B-DOS 1.5t)
is **independently confirmed by the manual's "Controlling the Peripherals" section**
[scan `IMG_20260617_162608.jpg`], which spells out each chip-select group:

- **EEPROM** (`%0001xxxx`): `&10` disable, `&11` enable.
- **Ethernet** (`%0010xxxx`): `&20` disable, `&21` enable, **`&28` reset — wait 50 µs after this for the ENC28J60 to fully reset its registers**.
- **Flashcard / SD** (`%0011xxxx`): `&30` disable, `&31` enable, **`&38` initialise — return value `0` = card not present / could not initialise, `1` = MMC detected+initialised, `2` = SD detected+initialised**.

The manual also documents the **push/pop read-byte** and **auto-null** commands [scan
`IMG_20260617_162617.jpg`]: `&02` push, `&03` pop; auto-null on per peripheral —
`&1F` (EEPROM), `&2F` (Ethernet), `&3F` (Flash) — and `&04` auto-null off; and that
**`IN &DD`/`&DE`/`&DF` all return the same last byte** clocked in from any peripheral.

### EEPROM layout — manual-confirmed

The 128 KB EEPROM is split into **120 chunks of 1 KB each**, preceded by a master index
of **120 × 64-byte headers = 7680 bytes at address 0**; bytes `7680..8192` are unused;
chunk data runs `8192..131071`, with chunk *N* at `8192 + (N-1)×1024` (chunk 1 = 8192,
chunk 120 = **130048**). Each 64-byte index header is: offset 0 = part number (1 B),
offset 1 = total parts (1 B), offset 2 = application name (16 B), offset 18 = description
(46 B). The network settings live in the chunk named **"Trinity Network "** with field
order MAC (6 B @ 0), IP (4 B @ 6), Gateway (4 B @ 10), Subnet/Mask (4 B @ 14), DNS 1
(4 B @ 18), DNS 2 (4 B @ 22), DHCP flag (1 B @ 26). [Source: Trinity manual "Using the
EEPROM", scans `IMG_20260617_162653.jpg` (memory map + config screen) /
`IMG_20260617_162702.jpg` (chunk addresses + header layout) / `IMG_20260617_162711.jpg`
(`name: DEFS 16`, `description: DEFS 46`, `chunk: DEFS 1024`).]

---

## 3. SPI Mechanics

SPI is **full duplex**: every `OUT (port), byte` is simultaneously paired with a byte clocked in from the device. The microcontroller stores that inbound byte so a subsequent `IN (port)` reads it without needing another clock cycle. This introduces a **one-byte lag**: what you read is what was clocked in _before_ the most recent write, so an extra dummy `OUT (port), 0` is needed to get the real response. [Source: Simon Owen blog, https://simonowen.com/blog/2007/11/30/trinity-ethernet/.]

For **MAC registers** in the ENC28J60, there is an additional hardware latency requiring a **double read** (two dummy writes). [Source: `encdrv.asm` `rd_m_reg` function, lines 292–302.]

The **auto-null feature** (firmware `&2F` / `enullon`) has the microcontroller automatically issue the dummy null write, removing it from the Z80 side of the bulk read loop. This is used in `rd_buf_mem` for bulk Ethernet frame reads (`encdrv.asm` lines 352, 368). **Auto-null for the SD port is VERIFIED**: the B-DOS 1.5t analysis (i71, `bdos-trinity-fork-analysis.md`) finds the fork's bulk SD read *and* write loops run under the `&3F` select (parallel to Ethernet's `&2F`) with no per-byte dummy writes — pure `INI`/`OUTI` gated on the busy flag.

---

## 4. Storage: SD Card

**Medium and capacity:** Standard MMC and SD/SDHC cards. B-DOS 1.5t beta 6 supports cards up to 64 GB. [Source: https://www.worldofsam.org/products/b-dos.]

**Filesystem / access model:** B-DOS does not use FAT. The SD card is divided into 800 KB **records**, each formatted identically to a SAM floppy disk (80 tracks × 10 sectors × 512 bytes × 2 sides ≈ 800 KB). B-DOS commands operate on records via raw sector access (READ AT / WRITE AT / VERIFY AT). [Source: https://www.worldofsam.org/products/b-dos.] The record model is **confirmed from the implementation side** by the B-DOS 1.5t analysis (i71, `bdos-trinity-fork-analysis.md`): the fork uses the same 1600-sector record stride, the same record-list/base formulas as the public 1.5a source, and the same `BDOS` record-ID stamp — so sub-8 GB Trinity media and Atom-era media remain interchangeable at the format level (which is why SimCoupé's Atom Lite path mounts under-8 GB Trinity images at all).

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
- Data phase (512 bytes at bulk speed under `&3F` auto-null — now confirmed for `&DF`): ~4.5 ms.
- Total best-case: ~4.7 ms → ~110 KB/s.
- Realistic (additional busy-waits): **20–80 KB/s** is a conservative working estimate. The B-DOS 1.5t analysis (i71, `bdos-trinity-fork-analysis.md`) confirms the loop model these figures assume — busy-poll on `&DC` bit 3 + `INI`/`OUTI` per byte under auto-null — so the working band stands with the auto-null caveat resolved in its favour.

**These figures are Z80-side-only.** SD card command-response latency (CMD17 → data token: typically 1–10 ms for a real card) is not included and would dominate at low transfer counts.

---

## 6. Phase-3 TFTP: What We Can Rely On

The `trinload`/`encdrv.asm` library directly addresses the Phase-3 need:

- **Ethernet driver** (`encdrv.asm`): complete, tested, public. Provides `drv_init`, `drv_read`, `drv_write`, `drv_exit`. ENC28J60 RX buffer = 6.5 KB; TX buffer = 1.5 KB. [Source: `encdrv.asm` EQU constants, `github.com/simonowen/trinload`.]
- **ARP responder** and **IPv4/UDP/ICMP** handling: fully implemented in `trinload.asm`. The existing code handles ARP requests, ICMP echo, and UDP datagrams. A TFTP sender on the SAM side would follow the same pattern.
- **TFTP WRQ (ship file to Pi)**: not in trinload, but the UDP write path (`drv_write`) is available. A TFTP WRQ state machine can sit directly on top of `encdrv.asm` — exactly the plan in `docs/specs/phase3-tftp-design.md`.
- **Fixed IP/MAC configuration**: trinload reads MAC and IP from EEPROM chunk "Trinity Network ". That same mechanism handles Phase-3 configuration.

**What Phase-3 cannot rely on without additional work:**
- Auto-MDIX: **the Trinity has no Auto-MDIX** — the manual instructs using a patch cable to a router/switch/hub, or a **crossover cable to connect directly to a PC** [Source: Trinity manual / Coupé Correspondence Q&A, scan `IMG_20260617_163239.jpg`: "a patch cable to connect it to your home network router, switch or hub, or a crossover cable to connect it to the likes of a PC"]. Needing a crossover for a direct PC link is exactly the symptom of a fixed-MDI 10BASE-T port (the ENC28J60 does not do MDI/MDI-X switching natively). So for Phase-3, a crossover cable or an auto-MDIX-capable switch is required; the Pi end may handle Auto-MDIX. (The photos confirm the *operational* requirement; they do not show a board-level MDI-switching circuit, and none is expected given the ENC28J60.)
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
| Auto-null mode works for SD port `&DF` | **VERIFIED** (B-DOS 1.5t bulk SD read+write loops run under `&3F` with no per-byte dummy writes — i71 analysis) |
| Port `&DC` select byte value for SD | **VERIFIED** (`&30`/`&31`/`&38`/`&3F` — confirmed by B-DOS 1.5t itself, i71 analysis; earlier recovered from period Trinity utility software) |
| Microcontroller identity probe + card-present / WP status bits | **VERIFIED** (probe `&08`/`&09`→`&DC`, replies `'T','R'` from `&DD`; `IN(&DC)` bit 1 = card present, bit 2 = WP inverted — i71 analysis) |
| Trinity has Auto-MDIX | **VERIFIED NOT** (manual: crossover cable needed for a direct PC link — fixed-MDI 10BASE-T; scan `IMG_20260617_163239.jpg`) |
| EEPROM full 128 K usable (chunk 120 @ byte 130048, above 64 K) | **VERIFIED** (manual EEPROM memory map, scans `IMG_20260617_162653.jpg` + `IMG_20260617_162702.jpg`) |
| EEPROM = 120 chunks × 1 KB, 64-byte index header each, master index 7680 B @ 0, chunk data from 8192 | **VERIFIED** (manual "Using the EEPROM", scans `IMG_20260617_162653.jpg` / `IMG_20260617_162702.jpg`) |
| Flashcard init (`&38`) return: 0 = absent, 1 = MMC, 2 = SD; ENC reset (`&28`) needs 50 µs settle | **VERIFIED** (manual "Controlling the Peripherals", scan `IMG_20260617_162608.jpg`) |
| No RTC or extra RAM on board | **LIKELY** (manual component tour `IMG_20260617_162538.jpg` lists no RTC/SRAM; name = "Trinity" = 3 things) |

---

## 9. Open Questions (Require Hardware or Documentation)

1. ~~Port `&DC` SD-select byte value~~ — **RESOLVED** (`&31` select / `&30` deselect / `&38` init / `&3F` auto-null; recovered from period Trinity utility software, private archive).
2. ~~Auto-null for `&DF`~~ — **RESOLVED (LIKELY)**: `&3F` is the SD select-with-auto-null value (same evidence).
3. ~~**SD initialisation sequence timing**: real cards can hold `CMD17` response for 1–10 ms. Does the Trinity's microcontroller buffer this, or does the Z80 poll a status register?~~ — **ANSWERED (i71, `bdos-trinity-fork-analysis.md`)**: the Z80 polls everything (R1 response, data token, write-busy completion — each a bounded retry loop), with the microcontroller's `&38` SD-init command run once before the Z80-driven SPI init ladder. The microcontroller's only per-byte role is the busy flag on `&DC` bit 3.
4. ~~**Auto-MDIX**: does the Trinity's ENC28J60 circuit include MDI/MDI-X switching, or does Phase-3 need a crossover cable?~~ — **ANSWERED (manual, `IMG_20260617_162653.jpg`-era doc, scan `IMG_20260617_163239.jpg`)**: no Auto-MDIX — the manual directs a **crossover cable for a direct PC link** (patch cable to a router/switch/hub). Phase-3 needs a crossover cable or an auto-MDIX switch (or the Pi end handling it).
5. **EEPROM address space above 64 K** — **the full 128 K is usable** (manual-confirmed): the manual's EEPROM memory map (Figure 1) lays out Index `0..7680`, Unused `7680..8192`, Chunk Data `8192..131071`, with chunk *N* at `8192 + (N-1)×1024` — **chunk 120 sits at byte address `130048`** (`&1FC00`, above 64 K), proving the upper half is reachable. [Source: Trinity manual "Using the EEPROM", scans `IMG_20260617_162653.jpg` (memory map) + `IMG_20260617_162702.jpg` (chunk-120 = `130048`); a magazine reprint OCR'd this as `430048`, but the cleaner manual scan and the arithmetic (`8192 + 119×1024 = 130048`) both give `130048`.] **Still open (not settled by the manual):** *how* the raw 17-bit byte address is conveyed at the SPI level. The manual documents only the BASIC chunk API (`read_chunk`/`write_chunk` by chunk number); the `eeprom.asm` driver emits a 2-byte address after the opcode, so the firmware/chunk layer abstracts the high bit — the raw addressing mechanism is not shown in the photographed pages.
6. **SD write source recovery**: the SAM Revival 21 cover disk carries the SD driver article source AND the B-DOS 1.5t source+executable (samcoupe.com/samrevival.htm); available in private reference materials. NOTE: the B-DOS hook route (see `bdos-version-landscape.md`) makes a raw SPI driver unnecessary — the B-DOS 1.5t analysis (i71, `bdos-trinity-fork-analysis.md`) confirms the fork's own SD write path is reached through the unchanged hook surface, so HSAVE/HOFLE+HSBYT via HRECORD covers writes without a separate driver.

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
- `bdos-trinity-fork-analysis.md` — the B-DOS 1.5t (Trinity fork) static analysis (i71): the canonical consumer of the `&DC`/`&DF` SD port values, confirming the select bytes, auto-null, probe, and status bits documented above
- Memory entry `trinity_hardware` (verified against the above rather than trusted)
