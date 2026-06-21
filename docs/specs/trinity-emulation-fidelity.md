# Trinity emulation fidelity inventory + emulator gap audit

This is the authoritative behaviour-by-behaviour map between the **documented
Trinity hardware contract** and its **Go emulator implementation**
(`tools/netboot-oracle/z80/`). Every behaviour mined from the three authority
extractions — the OCR'd Trinity manual (the primary hardware contract), the
`simonowen/trinload` ENC/EEPROM drivers, and Colin Piggot's B-DOS 1.5t SD driver
disassembly — is mapped to its emulator status (PRESENT / PARTIAL / ABSENT /
N/A-Z80) with a citation on each side. **No behaviour is skipped.**

The emulator must reach **hardware parity**: real Trinity is **one shared PIC
microcontroller on one SPI bus** (shared `&DC` busy/select, `&DD/&DE/&DF`
aliased read-back), not the independent per-device model the Go code currently
implements. This inventory is the spec that drives closing that gap (registry
**i235**). The GAP LIST at the end is the prioritised work queue; the
"Genuinely unspecified" section fences off what no source settles (do not
guess).

Authority cites: `manual:N` = item N of `manual-behaviours.md` (→ `combined.txt`);
`trinload:§/line` = `trinload-behaviours.md` (→ `encdrv.asm`/`eeprom.asm`/`trinload.asm`);
`bdos:N` = item N of `bdos-sd-behaviours.md` (→ `bdos15t-beta6.annotated.dis`).
Emulator cites are `file:line` under `tools/netboot-oracle/z80/`.

Status counts across the 114 inventoried rows: **PRESENT 68 · PARTIAL 19 ·
ABSENT 19 · N/A 8** (a handful of Gotchas-group rows restate headline items —
e.g. #109/#113/#114 cross-reference #6/#22/#31 — so the distinct-behaviour count
is slightly lower).

---

## 1. Microcontroller core (port map, SPI gateway, read-back model)

| # | Behaviour | Authority cite | Emulator status | Emulator cite | Notes |
|---|-----------|----------------|-----------------|---------------|-------|
| 1 | Ports `&DC..&DF` (4 ports): `&DC` control/status, `&DD` EEPROM, `&DE` ENC, `&DF` SD | manual:1,2,3 | PRESENT | enc28j60.go:38-44 | `portTrinityCtl/EEP/ENC/SD` constants |
| 2 | OUT `&DC` = command/select to PIC; return read back via `&DD/&DE/&DF` | manual:5; trinload:§DC | PRESENT | enc28j60.go:366-390 (`Out`), 393-427 (`ctlSelect`) | dispatch present |
| 3 | OUT `&DD`/`&DE`/`&DF` = byte to EEPROM/ENC/SD peripheral | manual:6 | PRESENT | enc28j60.go:371-388 | routed to `eep.clock`/`spiClock`/`sd.out` |
| 4 | PIC is the central SPI gateway between SAM and 3 peripherals | manual:27 | PARTIAL | enc28j60.go:128-200 | Modelled as **3 independent devices**, not one shared PIC; `&DC` select state is split (ENC `encSelected`, EEP `selected`, SD `selected`) instead of one MUX. **This is the load-bearing gap.** |
| 5 | PIC runs its own embedded program (offload/control) | manual:28 | N/A-Z80 | — | Firmware internals not modelled; only its observable port contract matters |
| 6 | **`IN &DD/&DE/&DF` all alias ONE PIC port — return the last byte clocked in from ANY peripheral, not per-peripheral** | manual:4,8,34,125 | **ABSENT** | enc28j60.go:337-361 | Emulator returns a **per-device latch**: `&DD`→`eep.miso`/`probeReply`, `&DE`→`spiMISO`/RBM, `&DF`→`sd.in()`. Two OUTs then two INs would (wrongly) return two different devices' data — the manual's aliasing trap is not reproduced. |
| 7 | A0–A4 address bits tell the PIC which peripheral an OUT targets | manual:7 | PARTIAL | enc28j60.go:371-388 | Implicit in the port-number switch; not modelled as address-bit decode (harmless given fixed `&DD/&DE/&DF`) |
| 8 | Per-OUT peripheral sequence: PIC marks busy → clocks SPI → places return byte → marks not-busy | manual:26 | PARTIAL | enc28j60.go:330-336 (busy always clear) | Return-byte latch modelled; busy transition is **not** (bit 3 hardwired clear, see #14) |
| 9 | SPI full-duplex: one byte out clocks one byte in simultaneously | manual:29; bdos:3 | PRESENT | eeprom.go:145-178; sdcard.go:309-358 | dummy-clock-to-read modelled per device |
| 10 | **One-byte SPI read-lag**: byte readable immediately after a command is stale; need a dummy clock then read | manual:31,33,127; trinload:2c; bdos:3,4 | PRESENT | enc28j60.go:558-575 (`spiMISO`); eeprom.go:91-92,170-178; sdcard.go:127-130,402-418 | Each device latches MISO on the clocking OUT; the next IN returns it |
| 11 | IN returns only the *stored* value (no device re-access on IN) | manual:32 | PRESENT | enc28j60.go:354; sdcard.go:417 | manual-mode IN returns the latch |
| 12 | Correct usage: interleave OUT/IN per byte (`OUT; IN; OUT; IN`) | manual:35,126 | PRESENT | (driver discipline; emulator honours read-lag) | emulator is correct *for* interleaved use; #6 is the trap it does not catch for *mis*-use |

## 2. Status register `IN (&DC)` (`%1100BWFE`)

| # | Behaviour | Authority cite | Emulator status | Emulator cite | Notes |
|---|-----------|----------------|-----------------|---------------|-------|
| 13 | `IN &DC` returns status; the ONLY port not via the PIC, readable any time | manual:9,10 | PRESENT | enc28j60.go:330-336; sdcard.go:270-275 | `ctlStatus()` always returns a value, never stalls |
| 14 | Layout `%1100BWFE`; bits 7,6=1 fixed, bits 5,4=0 fixed (top nibble `0xC0`) | manual:11-15 | PRESENT | sdcard.go:270-275 | base `0xC0` |
| 15 | **Bit 3 = BUSY** (1=busy); while set, touch nothing but `IN &DC` | manual:16,20,21,22,124; bdos:23,47 | **PARTIAL** | sdcard.go:271-274 ("busy clear") | BUSY is **hardwired clear** — model never stalls, so `wait_ready`/`wait` always exit. Real PIC raises it per SPI byte. Drivers tolerate always-clear (they only spin *while* set), but the bit is not modelled as a real state and so cannot gate OUTs (see #17). |
| 16 | Bit 2 = WRITE (SD write-enable/WP); 1=write enabled | manual:17,102; bdos:42,43,46 | PARTIAL | sdcard.go:274 (returns `0xC6`) | Returns bit 2 SET (writable) when a card is configured. Sense matches the driver's `CPL/AND 4` WP gate. **Not configurable** — cannot model a write-protected card (would need bit 2 clear). |
| 17 | **OUT while BUSY is silently ignored**; BUSY gates all writes to `&DD/&DE/&DF` and the next OUT `&DC` | manual:22,124; bdos:47 | **ABSENT** | — | No code drops an OUT while busy (BUSY is always clear). The gating semantics are not modelled at all. |
| 18 | Bit 1 = FLASH (card present); 1=present | manual:18,103; bdos:44,46 | PRESENT | sdcard.go:270-275 | bit 1 set iff a CSD is configured |
| 19 | Bit 0 = ENCINT (ENC interrupt; 1=interrupt pending) | manual:19,99,100; trinload(poll path) | ABSENT | sdcard.go:269 ("ENCINT not modelled") | EIR is tracked inside the ENC (enc28j60.go:735) but never surfaced on `&DC` bit 0; the polling interrupt path (the supported v1.1 path, jumper removed) is not wired to the status byte |
| 20 | Busy-poll routine `IN &DC / AND 8 / JR NZ` after every OUT | manual:23,25,133; trinload:2a; bdos:2,47 | PRESENT (vacuously) | sdcard.go:271-274 | poll always exits immediately (busy clear) |
| 21 | BASIC need not poll BUSY (slow enough); machine code MUST | manual:24,133 | N/A-Z80 | — | host/driver-side timing note, no emulator obligation |

## 3. Select / command bytes `OUT (&DC)`

| # | Behaviour | Authority cite | Emulator status | Emulator cite | Notes |
|---|-----------|----------------|-----------------|---------------|-------|
| 22 | `&02` PUSH stored read-byte (save pending IN byte for ISR) | manual:37,39 | ABSENT | — | not modelled; no stored-byte stack |
| 23 | `&03` POP stored read-byte (restore) | manual:38,39 | ABSENT | — | not modelled |
| 24 | `&1F` EEPROM auto-null ON | manual:40,44 | ABSENT | — | EEPROM model has no auto-null mode (driver `read_chunk` uses manual dummy clocks, so untested) |
| 25 | `&2F` Ethernet auto-null ON | manual:41,44; trinload:§DC.5,2h | PRESENT | enc28j60.go:412-414 (`autoNull=true`) | set; but see #26 note — it is a per-ENC flag, not the global PIC mode |
| 26 | `&3F` Flash/SD auto-null ON | manual:42,44; bdos:25,48 | PRESENT | sdcard.go:238-242 (`autoNull=true`) | SD reads auto-advance under `&3F` |
| 27 | `&04` switch auto-nulling OFF (global, clears whichever ON mode) | manual:43,44; trinload:§DC.6; bdos:8,49 | PARTIAL | enc28j60.go:414-418 | `&04` clears **only the ENC** `autoNull` + resets SD; it does NOT clear the EEPROM auto-null (none exists) and the two auto-null flags (`ENC.autoNull`, `SD.autoNull`) are independent, not one global PIC mode |
| 28 | **Auto-null is ONE global PIC mode** targeting the single selected peripheral, set by the ON command, cleared only by `&04` | manual:44 | **ABSENT** (as a unified mode) | enc28j60.go:412-415; sdcard.go:111,238-242 | Two separate per-device booleans; `&2F` and `&3F` should be mutually exclusive selections of the *same* mode. |
| 29 | `&08..&0F` IDENT read → `"TRINv1.1"` (8 chars) | manual:46-56; trinload:§DC.9-10,§3 | PARTIAL | enc28j60.go:62-66,419-421 | Modelled, but the string constant is **`"TRI v1.1"`** (4th char SPACE) not `"TRINv1.1"`. The driver only gates on `&08`→'T', `&09`→'R' (both correct), so `chk_trinity` passes; the 4th char disagrees with the manual (manual:49 OCR-garbled the glyph; trinity_fidelity_test.go:20 asserts the SPACE form). **Unresolved conflict — see Genuinely-unspecified.** |
| 30 | `chk_trinity` uses fixed DJNZ delays (not BUSY) — identity bytes available immediately after the select write | trinload:§3 | PRESENT | enc28j60.go:419-421 | `probeReply` latched on the `&DC` select, ready for the next `IN &DD` |
| 31 | `%11fedcba` LED Twinkle (6 LED segments; cosmetic) | manual:57-64,116,117,134 | ABSENT | — | not modelled; no LED state. Cosmetic — no driver depends on a read-back, but a fidelity model should accept+ignore `&C0..&FF` writes |
| 32 | `&10` EEPROM CS disable / `&11` EEPROM CS enable | manual:65,66; trinload:§4a | PRESENT | enc28j60.go:422-425 (`csAssert`/`csDeassert`) | |
| 33 | `&20` ENC CS disable / `&21` ENC CS enable | manual:67,68; trinload:§DC.1-2,2b | PRESENT | enc28j60.go:401-407 | |
| 34 | `&23` ENC pulse CS (disable+enable in one) | manual:69,71,130; trinload:§DC.3 | PARTIAL | enc28j60.go:407 (treated as plain disable) | `selENCPulse` falls into the disable case; it does not re-assert. Dead in the drivers (trinload:§DC.3), so untested, but not faithful to "pulse = end-of-command" |
| 35 | `&28` ENC reset; wait 50µs after | manual:70,92,112,132; trinload:1a; bdos(—) | PARTIAL | enc28j60.go:409-411 (`softReset`) | Reset modelled as instantaneous `softReset()`; the **50µs settle** is not modelled (no time concept). Driver uses blind DJNZ delay so functionally fine; timing fidelity absent |
| 36 | `&30` SD CS disable / `&31` SD CS enable (manual mode) | manual:72,73; bdos:49 | PRESENT | sdcard.go:229-237,255-258 | |
| 37 | `&38` SD init: detect MMC/SD, return **0/1/2** (0=absent/fail, 1=MMC, 2=SD); LED blue→orange/off | manual:74,75,76,77,104 | **PARTIAL** | sdcard.go:243-254 | `&38` only sets `woken=true` and makes the next read `&FF` (matching Colin's `&A643` wake poll, which breaks on `&FF` — bdos:10). The documented **0/1/2 return code is NOT produced**; card-type detection in the emulated path happens via CMD8/ACMD41/CMD58 instead. A BASIC-level or alt-driver consumer reading the `&38` return would get the wrong value. |
| 38 | CS controls /chipselect only (not power); disable when idle (good practice) | manual:78,79,80,131 | PRESENT | (CS assert/deassert per device) | select state is CS, not power |

## 4. EEPROM (Microchip 25LC1024, 128 KB)

| # | Behaviour | Authority cite | Emulator status | Emulator cite | Notes |
|---|-----------|----------------|-----------------|---------------|-------|
| 39 | 128 KB part, addr 0..131071, accessed via `&DD` | manual:81,82 | PRESENT | eeprom.go:66-69 (`eepDeviceSize`) | |
| 40 | SPI READ opcode `&03` + 3-byte addr (MSB first) + auto-incrementing data clocks | manual:83; trinload:§4b,4c; eeprom.go | PRESENT | eeprom.go:72-77,145-178 | `eepCmdRead`; address MSB-first; auto-increment |
| 41 | SPI WRITE opcode `&02`, page-write, gated by WREN | trinload:§5a,5c,5d,5f | PRESENT | eeprom.go:73,173-197 | WEL latch; ignored without WREN |
| 42 | WREN `&06` sets write-enable latch; WRDI `&04` clears it; latch self-clears after write | trinload:§5a,5f | PRESENT | eeprom.go:75-77,131-138,152-157 | WEL self-clears on CS-deassert after a write |
| 43 | 256-byte page-write boundary (counter wraps within page) | trinload:§5d | PRESENT | eeprom.go:69,188-197 | low 8 bits wrap, page bits hold |
| 44 | Post-write busy delay (driver uses blind `write_delay`, not RDSR WIP) | trinload:§5b | PRESENT (by omission) | eeprom.go:50-53 (no WIP state) | faithful: driver never polls WIP, model needs none |
| 45 | Index region: 120 × 64-byte headers @0..7679; unused 512B @7680..8191; chunk data @8192..131071 | manual:83,84,85 | PRESENT | eeprom.go:56-59; enc28j60.go:434-467,510-513 | `eepIndexStride=64`, `eepChunkBase=0x2000`; chunk N at (28+4N)<<8 |
| 46 | 64-byte header: part(1), total(1), name(16), desc(46); part=0 ⇒ empty | manual:85; trinload:§4d,4e | PRESENT | enc28j60.go:436-440,461-465 | Program* helpers lay this out |
| 47 | "Trinity Network " chunk (16 chars) holds MAC@0,IP@6,Gateway@10,Subnet@14,DNS@18,2ndDNS@22,DHCP@26 | manual:86,87; trinload:§4c,7a | PARTIAL | eeprom.go:79-82; enc28j60.go:434-446 | name + MAC(6) + IP(4) modelled (what the boot wrappers read); gateway/subnet/DNS/DHCP fields are not laid out (unused by the emulated drivers) |
| 48 | `find_index` (match part/total/name → chunk number) | trinload:§4e | PRESENT | enc28j60.go:434-446 (`ProgramTrinityNetwork` lays it so `find_index` matches) | runs the real `find_index` against the model |
| 49 | `read_chunk` (read 1 KB chunk by value) | trinload:§4c | PRESENT | eeprom.go:170-172; enc28j60.go:441-446 | |
| 50 | `read_index`, `count_empty`/`find_empty` slot scans | trinload:§4d,4f | PRESENT | eeprom.go:145-178 | generic READ machinery serves all of these |
| 51 | `delete_index` (zero first bytes of a slot) | trinload:§5e | PRESENT | eeprom.go:173-197 (WRITE path) | a write of zeros via the modelled WRITE |
| 52 | EEPROM auto-null mode (`&1F`) for fast reads | manual:40,44 | ABSENT | — | driver doesn't use it for EEPROM; see #24 |
| 53 | No separate per-EEPROM 'T'/'R' identity probe — IDENT is the PIC-level string | manual:89 | PRESENT | enc28j60.go:337-343 | `&DD` returns `probeReply` (the PIC IDENT) when not in an EEPROM data phase |

## 5. ENC28J60 (Ethernet controller, port `&DE`)

| # | Behaviour | Authority cite | Emulator status | Emulator cite | Notes |
|---|-----------|----------------|-----------------|---------------|-------|
| 54 | SPI opcode decode: RCR(000)/WCR(010 bit6)/BFS(100 bit7)/BFC(101 bits5+7)/RBM(`&3A`)/WBM(`&7A`)/SRC(`&FF`) | manual:91; trinload:§6 | PRESENT | enc28j60.go:70-79,583-631 | full opcode set decoded |
| 55 | 4 banks × 32 regs; final 5 (0x1B-0x1F) common across banks | manual:93; trinload:§2g | PRESENT | enc28j60.go:131,275-292 | common-window aliasing modelled |
| 56 | 8 KB buffer, user-defined circular RX region, rest TX | manual:93,94; trinload:1,2h | PRESENT | enc28j60.go:118-122,656-690,751-818 | `bufSize=0x2000`, RX `0..0x19FF`, ring wrap |
| 57 | Power-on state: no MAC, no RX buffer, filter ignores everything ("sees but does not receive") | manual:95 | PARTIAL | enc28j60.go:254-271 (`softReset`) | regs zeroed on reset (no MAC, no RX alloc) — correct. But the **packet filter is not modelled**: the emulator injects frames directly into the RX FIFO regardless of MAC/filter config (enc28j60.go:34-35 self-documents this), so the "ignore everything until configured" behaviour is bypassed |
| 58 | Writing 0 to packet-filter reg = sniffer mode (receive all) | manual:96 | ABSENT | — | no filter model at all (frames always delivered) |
| 59 | **M-prefixed (MAC/MII) registers need DOUBLE-READ** (extra lag over normal SPI lag) | manual:97,128; trinload:§2d,6 | PRESENT | enc28j60.go:294-310,600-607 | `isMACMII` → dummy on byteIdx 1, real data on byteIdx 2 |
| 60 | ETH (non-M) registers return data after ONE dummy clock | trinload:§2c,6 | PRESENT | enc28j60.go:608-611 | |
| 61 | ENC documented transmit-stuck silicon bug needs a SW work-around | manual:98,129 | ABSENT (N/A) | enc28j60.go:33-35 (self-documented as not modelled) | R5 errata not modelled; emulator never wedges TX. Marking N/A — modelling the bug would only test the workaround, no driver-correctness gain |
| 62 | /CS pulse ends an ENC command (datasheet §4 requirement) | manual:69,71,130 | PARTIAL | enc28j60.go:407 | see #34 — pulse collapses to disable |
| 63 | MAADR registers not in MAC-byte order (driver feeds 4,5,2,3,0,1) | trinload:§1b | PRESENT | enc28j60.go (regs store verbatim; driver's mapping just writes/reads them) | model stores whatever the driver writes; ordering is the driver's concern |
| 64 | `drv_init` full register init sequence (MACON1/3, MAIPG, MAMXFL, PHCON, ECON1.RXEN…) | trinload:§1 | PRESENT | enc28j60.go:583-631 (WCR/BFS/BFC handlers) | the real `drv_init` runs against the model |
| 65 | RBM bulk read auto-advances ERDPT with ring wrap at ERXND→ERXST | trinload:§2h; manual:30 | PRESENT | enc28j60.go:656-675 (`rbmNext`) | AUTOINC + wrap |
| 66 | WBM bulk write auto-advances EWRPT | trinload:§2i | PRESENT | enc28j60.go:677-690 (`wbmWrite`) | |
| 67 | Transmit: ETXST/ETXND + TXRTS start; completion EIR.TXIF set, TXRTS self-clear | trinload:§1(step20); manual(—) | PRESENT | enc28j60.go:636-643,692-738 (`doTransmit`) | |
| 68 | EPKTCNT (bank1 0x19) packet count; ECON2.PKTDEC decrements it | manual(—); datasheet | PRESENT | enc28j60.go:101,591-599,644-650 | RX materialised lazily on EPKTCNT read |
| 69 | RX header: 2-byte next-ptr LE + 4-byte RSV LE (byte count incl. 4-byte CRC) | datasheet; enc28j60.go | PRESENT | enc28j60.go:751-818 (`materialiseRX`) | |
| 70 | PHY register write via MIREGADR/MIWR + MISTAT.BUSY poll | trinload:§2j | PRESENT | enc28j60.go:636-654 (settles instantly: MISTAT=0) | |
| 71 | PHY link-up delay after init; TX before link-up silently lost (i127, HW-confirmed) | (i127 hardware finding) | PRESENT | enc28j60.go:172-186,515-538,708-738 | `linkUpAfterOps`/LSTAT read + silent-drop modelled |
| 72 | ENCINT routable via NMI/INT jumpers (not fitted v1.1) or ENC-register check | manual:99,100 | N/A | — | v1.1 has no jumper; polling path is the supported one (see #19) |

## 6. SD / MMC flash (port `&DF`)

| # | Behaviour | Authority cite | Emulator status | Emulator cite | Notes |
|---|-----------|----------------|-----------------|---------------|-------|
| 73 | Supports MMC and SD; card up to 2GB at PIC-init level (SDHC not at init) | manual:101,105 | PARTIAL | sdcard.go:159-164 (`isV2`) | v1/v2 branch modelled; the manual's "SDHC unsupported at `&38` init" is moot because `&38` doesn't return 0/1/2 (see #37) |
| 74 | `sd.out` SPI write: wait BUSY then `OUT &DF` | bdos:1 | PRESENT | sdcard.go:309-358 (`out`) | |
| 75 | `wait` busy-poll `IN &DC / AND 8 / JR NZ`, no timeout | bdos:2,47 | PRESENT (vacuous) | sdcard.go:271-274 | always exits (BUSY clear) |
| 76 | `sd.in` read = dummy `&FF` OUT, wait, `IN &DF` (one-byte lag) | bdos:3 | PRESENT | sdcard.go:402-418 | manual-mode latch |
| 77 | `sd.in` double-lag (skip 2 throwaway then real) for CMD8/CMD58 trailers | bdos:4 | PRESENT | sdcard.go:476,499 (R7/OCR streams include the skipped bytes) | response stream lays them out so the driver's double-read lands right |
| 78 | 18-byte token+block reader (token `&FE`, 256-try bound, 16 data + 2 CRC) | bdos:5,18 | PRESENT | sdcard.go:502-512 (CMD9 stream = `&FE` + 16 CSD + 2 CRC) | |
| 79 | `sd.cmd` 6-byte frame: cmd, 32-bit arg (D,E,H,L big-endian), CRC; R1 poll (bit7 clear=valid) | bdos:6,54 | PRESENT | sdcard.go:300-303 (`frameAddr` BE), 464-547 (`completeCommand`) | R1 first byte of each response stream |
| 80 | `&04` opening bracket / all-deselect before SD init | bdos:8 | PRESENT | enc28j60.go:414-418; sdcard.go:279-288 (`sdReset`) | |
| 81 | `&38` wake then `&FF`-settle poll (loop until SPI read != `&FF` → actually breaks ON `&FF`) | bdos:10 | PRESENT | sdcard.go:243-254 | matches the disassembly (breaks on `&FF`) |
| 82 | CMD0 GO_IDLE → R1=1 (idle) | bdos:11 | PRESENT | sdcard.go:469-470 | |
| 83 | CMD8 SEND_IF_COND: SDv2 → R1=1 + `&01AA` echo; SDv1/MMC → R1 illegal-cmd (bit2) | bdos:13 | PRESENT | sdcard.go:471-483 | v1 returns `0x05`, v2 returns echo `0x01AA` |
| 84 | CMD55+ACMD41 loop (HCS=`&40000000` when SDv2); ACMD41 retry 2500 | bdos:14 | PRESENT | sdcard.go:484-489 | CMD55→R1=1, ACMD41→R1=0 (ready) |
| 85 | CMD1 MMC fallback (retry 5000) when CMD55/ACMD41 illegal | bdos:15 | PRESENT | sdcard.go:488-489 | CMD1→R1=0 |
| 86 | CMD58 READ_OCR → CCS bit selects block vs byte addressing | bdos:16 | PRESENT | sdcard.go:490-499 | OCR `0xC0` (power-up + CCS) for v2 |
| 87 | CMD59 CRC_ON_OFF → R1=0 (then CRCs are dummies) | bdos:17,55 | PRESENT | sdcard.go:500-501 | |
| 88 | CMD9 SEND_CSD → 18 bytes via token reader → capacity parse | bdos:18,20 | PRESENT | sdcard.go:502-512 | CSD streamed; parse runs driver-side |
| 89 | CMD16 SET_BLOCKLEN 512 → R1=0 | bdos:19 | PRESENT | sdcard.go:513-514 | |
| 90 | `&3F` SD select-with-auto-null for the bulk data phase (PIC auto-clocks dummies) | bdos:25,48 | PRESENT | sdcard.go:238-242,414-417 | bare IN auto-advances under `&3F` |
| 91 | Self-modifying 32-bit LBA poked into `&A836/&A843`, sent big-endian | bdos:24,50 | N/A-Z80 | — | Z80 self-modification — the model reads the resulting command frame (sdcard.go:300-303), never the immediates |
| 92 | CMD17 READ_SINGLE: R1=0, `&FE` token, 510+2 bytes via INI | bdos:27-30 | PRESENT | sdcard.go:515-531 | `&FE` + 512 sector + 2 CRC |
| 93 | CMD24 WRITE_SINGLE: WP gate, `&FE` token, 510 OUTI + 2 tail, data-response (`&1E`→`&04` accepted), busy-wait | bdos:33-37 | PRESENT | sdcard.go:532-547,360-448 | full write data phase + handshake modelled |
| 94 | Write-protect gate: `IN &DC / CPL / AND 4` (sense-inverted) on every write | bdos:33,42,43 | PARTIAL | sdcard.go:274 (bit2 set = writable) | gate honoured for a writable card; cannot model a WP card (bit 2 not configurable, see #16) |
| 95 | Trailing CRC discard after read (2 bytes) | bdos:31 | PRESENT | sdcard.go:530 (CRC bytes in stream) | |
| 96 | **Deselect-tail (proven close): `&30` → dummy `&DF` → `&30` → `&04`** | bdos:40,41 | PARTIAL | sdcard.go:255-258 (`&30`→deselect), 279-288 (`&04`→reset) | Each byte handled in isolation; the **ordered tail sequence is not asserted/required** — the model accepts the bytes in any order and a missing dummy clock or second `&30` is not detected. Faithful execution of the real tail works; the load-bearing ordering is not a modelled contract |
| 97 | Hot-swap / `RESTORE DEVICE` re-init | manual:107; bdos:52 | N/A-Z80 | — | host/B-DOS re-entry; the model's `sdReset` supports re-init but the RESTORE path is driver-side |
| 98 | B-DOS 800K RECORD abstraction, SamDisk format | manual:108,109 | N/A-Z80 | — | host filesystem layer above the SPI contract |
| 99 | Block size 510-on-wire-loops / 512-to-card (`512 MOD 6 = 2` trick) | bdos:56 | PRESENT | sdcard.go:55,377-390 (`sdSectorSz=512`) | card sees 512; the 510+2 split is the driver's |
| 100 | Retry/timeout bounds (init 10, ACMD41 2500, CMD1 5000, token 256, write-busy 65536) | bdos:57 | PARTIAL | sdcard.go:66-71 (busy reads = 2) | model returns ready promptly so bounds are never approached; the bounds themselves are driver constants, not emulator obligations, but the model must keep responses inside them (it does) |

## 7. Timing / LED / identity / power-on

| # | Behaviour | Authority cite | Emulator status | Emulator cite | Notes |
|---|-----------|----------------|-----------------|---------------|-------|
| 101 | 50µs settle after ENC reset | manual:70,112,132 | ABSENT | — | no µs timing; driver's blind DJNZ covers it (see #35) |
| 102 | BUSY momentarily set after each OUT (no T-state figure) | manual:111 | ABSENT | — | BUSY never set (see #15) |
| 103 | Power-up LED sequence orange→blue→off | manual:113 | ABSENT | — | no LED/power-on model |
| 104 | Three orange/blue status LEDs (EEPROM/ENC/SD); orange=CS active, blue flash=data | manual:116,117 | ABSENT | — | no LED model |
| 105 | Two ENC-driven LEDs Green(A)=cable, Yellow(B)=traffic | manual:118,119,120,121 | ABSENT | — | PHLCON written by driver (enc28j60.go ENC regs) but LED outputs not surfaced |
| 106 | IDENT string identifies firmware/version; used as presence + version check | manual:54,55,56 | PARTIAL | enc28j60.go:62-66 | string present but `"TRI v1.1"` vs manual's `"TRINv1.1"` (see #29) |
| 107 | PING round-trip ~7-8ms (informational) | manual:115 | N/A | — | software-level, not a port behaviour |
| 108 | MAC `02 54 52 49 4E BC` (locally-administered) | manual:5(header),135 | N/A | — | per-board datum, supplied by `ProgramTrinityNetwork`, not a fixed emulator constant |

## 8. Gotchas / edge cases / trinload handoff state

| # | Behaviour | Authority cite | Emulator status | Emulator cite | Notes |
|---|-----------|----------------|-----------------|---------------|-------|
| 109 | Read-back is global, not per-port (two OUTs then two INs loses the first) | manual:125,8,34 | ABSENT | enc28j60.go:337-361 | the aliasing trap — duplicate of #6, the headline gap |
| 110 | Always read back when expected (interleave) | manual:126 | PRESENT | (per-device read-lag) | correct for interleaved use |
| 111 | SPI dummy-byte lag trap | manual:127 | PRESENT | (see #10) | |
| 112 | trinload `X` handoff: ENC just-reset (last `&DC`=`&28`), EEPROM disabled, auto-null off, INTs enabled, HMPR=push page, stack→`start` | trinload:§8 | PARTIAL | enc28j60.go:380-383 (HMPR recorded, not relocated) | The model records the handoff writes; full paging/stack inheritance is **hardware-gated** (flat harness, no relocation). ENC-reset/EEPROM-disabled state is reproduced |
| 113 | `&02`/`&03` PUSH/POP read-byte stack for ISR safety | manual:37,38,39 | ABSENT | — | duplicate of #22/#23 |
| 114 | LED Twinkle reverts on next peripheral use | manual:64,134 | ABSENT | — | no LED model (see #31) |

---

## GAP LIST (prioritised)

Ordered by importance for hardware parity. The shared-controller items (a–d)
are first — they are load-bearing: the emulator models three independent
devices where hardware is one shared PIC, and that mismatch is what hides
mis-sequencing bugs (the i145g probe-hang class).

1. **(a) Auto-null is not ONE global PIC mode (#28, #24, #27).** *Implement:* a
   single `autoNull` + `autoNullTarget` (none/EEP/ENC/SD) on the shared
   controller; `&1F/&2F/&3F` set mode+target (mutually exclusive), `&04` is the
   only clear. Reads under auto-null on the *selected* peripheral auto-clock;
   on a non-selected one do not. Retire the two per-device booleans.

2. **(b) BUSY (bit 3) is hardwired clear and gates nothing (#15, #17, #102).**
   *Implement:* a real BUSY state the PIC raises for one "SPI byte" after each
   OUT to `&DD/&DE/&DF` (and command OUT `&DC`), cleared on the next status
   read or step; **drop any OUT issued while BUSY is set** (manual:22). This is
   what makes a missing busy-poll a reproducible failure instead of silently
   passing.

3. **(c) `IN &DD/&DE/&DF` alias ONE latch, not per-port (#6, #109).**
   *Implement:* a single shared `lastClockedIn` byte on the controller that
   every `IN &DD/&DE/&DF` returns, written by whichever peripheral last clocked
   a byte. Then the manual's two-OUT-two-IN aliasing trap reproduces (the second
   IN returns the same byte). Keep RBM/auto-null auto-advance feeding that one
   latch.

4. **(d) Peripheral select is not a MUX (#4).** *Implement:* one
   `selectedPeripheral` on the controller; selecting one deselects the others
   (`&11/&21/&31/&3F` are mutually exclusive). Today three `selected` booleans
   can be true at once. Fold ENC/EEP/SD select state into the shared controller.

5. **ENCINT (bit 0) never surfaced on `&DC` (#19).** *Implement:* mirror the
   ENC's EIR/interrupt state into status bit 0 so the supported v1.1 polling
   interrupt path is exercised in emulation.

6. **`&38` SD init does not return 0/1/2 (#37).** *Implement:* make the `&38`
   wake place a documented return code (0 absent / 1 MMC / 2 SD) on the read
   latch per `configured`+`isV2`, in addition to the `&FF` settle Colin's driver
   polls — so a consumer reading the `&38` return code (not just Colin's ladder)
   gets the right value.

7. **Write-protect not configurable (#16, #94).** *Implement:* a `writeProtect`
   flag making `&DC` bit 2 clear (so `CPL/AND 4` flags WP), to exercise the
   driver's WP-abort path. Today every card reads writable.

8. **Packet filter not modelled (#57, #58).** *Implement:* honour the ENC
   packet-filter register — drop injected RX frames whose dest MAC fails the
   configured filter (and pass all when filter=0/sniffer). Today frames are
   injected straight into the FIFO regardless of MAC/filter config.

9. **ENC `&23` pulse and `&28`-50µs settle are simplified (#34, #35, #62, #101).**
   *Implement:* make `&23` a real CS de-assert+assert (end-of-command), and (if
   a time model is added for (b)) a settle delay after `&28`.

10. **Deselect-tail ordering not a modelled contract (#96).** *Implement:* once
    (b)/(d) land, require the `&30`→dummy→`&30`→`&04` close before another
    peripheral may be selected; flag an out-of-order close — so a driver that
    skips the proven tail fails in emulation.

Lower-priority / cosmetic (model for completeness, no driver-correctness gain):

11. **PUSH/POP read-byte stack `&02/&03` (#22, #23, #113).** *Implement:* a
    one-deep saved-read-byte slot toggled by `&02`/`&03`.
12. **LED state: Twinkle `&C0..&FF`, status LEDs, ENC green/yellow, power-on
    sequence (#31, #103, #104, #105, #114).** *Implement:* accept+track LED
    writes and CS/data-driven colour so a fidelity read or trace is possible.
13. **EEPROM gateway/subnet/DNS/DHCP fields in the network chunk (#47).**
    *Implement:* lay out the full 27-byte settings record in the Program*
    helpers.
14. **R5 transmit-stuck errata (#61).** *Marked N/A* — only worth it to test the
    driver's workaround; revisit if that workaround is ever ported SAM-side.

---

## Genuinely unspecified (do NOT guess)

No available source settles these; encoding a value would be a guess.

- **IDENT 4th character (#29, #106).** The manual's glyph is OCR-garbled (`()`,
  manual:49) — it reads as `"TRINv1.1"` by context (manual:54) but the source
  scan the emulator constant was taken from (IMG_20260617_162601) shows a SPACE,
  giving `"TRI v1.1"` (enc28j60.go:66, asserted trinity_fidelity_test.go:20). The
  drivers gate only on `&08`→'T'/`&09`→'R', so the conflict is invisible to
  every code path. **Do not "fix" the constant to `"TRINv1.1"` without a clean
  re-scan of the hardware/source** — the two authorities genuinely disagree.

- **PIC firmware internal `&38` init timing / SPI sequence (manual:208).** The
  manual deliberately abstracts what the PIC does internally during `&38`
  ("saving the SAM a lot of work") — only the 0/1/2 return and LED colour are
  documented. Model the *return code* (gap #6) but do not invent the internal
  command sequence.

- **Exact T-state / µs figures for BUSY-set duration and the 50µs ENC settle
  beyond the one stated number (manual:111, 114).** The manual gives "momentarily
  busy" with no T-state figure and only the single 50µs settle number. A BUSY
  model (gap b) should use a nominal one-byte duration, not a fabricated precise
  timing.

- **SD block-vs-byte SPI addressing at the PIC level (manual:110).** The manual
  documents only `&38` (0/1/2) and the B-DOS RECORD abstraction; the SPI-level
  block/byte choice is settled by Colin's driver (CMD58 CCS bit, bdos:16), which
  the model already follows — but the *manual* leaves it unspecified, so the
  driver disassembly is the only authority here.

- **A0–A4 peripheral address decode beyond `&DD/&DE/&DF` (#7, manual:205).** The
  OCR garbles the exact bit-to-port mapping; only the three fixed ports are
  legible. Model the three ports (done); do not infer a wider decode.

- **PHY link-up post-init delay duration (#71).** Hardware-confirmed that the
  link is down for a while and TX is silently lost, but the datasheet gives no
  duration and the chip has no auto-negotiation. The emulator parameterises it
  (`linkUpAfterOps`) rather than asserting a specific delay — keep it
  hardware-gated.
