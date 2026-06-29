# Trinity SD-card Z80 interface — port authority for the SD-SPI port

**What this is.** A focused engineering reference for the Z80 ↔ Trinity SD-card interface, extracted from Colin Piggot's annotated disassembly of his B-DOS 1.5t (beta 6) Trinity fork (`~/sam-archive/bdos/analysis/bdos15t-beta6.annotated.dis`, **private** — citations are by line/address, no wholesale copy). The fork is the **port authority**: its SD driver is proven on real Trinity hardware, so the fresh-Z80 work (i141 raw list-sector read, i145a/b CSD/capacity read) mirrors these routines rather than reinventing them. This doc is the SD-interface deep-dive that `bdos-trinity-fork-analysis.md` (the i71 fork overview) and `trinity-capabilities.md` (the general port map / SPI mechanics, §2–§4) point down into — read those for the surrounding context; this restates none of it.

**Address convention.** The fork's DOS page also executes paged at `&4000–&7FFF`, so the disassembly shows internal `CALL`s to **section-B aliases**: a `&67xx`/`&66xx` target is the real `&A7xx`/`&A6xx` routine (add `&4000`). All real addresses below are written `&Axxx`. Citations are `dis:LINE` against the annotated file.

**Key finding up front (the i145 crux): the fork DOES read the CSD via a bare CMD9** (`SEND_CSD`, opcode `&49`) at **&A711** (`dis:5678`), after using **CMD58** (`READ_OCR`, `&7A`, &A6E2 / `dis:5655`) purely to obtain the CCS bit (SDHC vs SDSC / block-vs-byte addressing). Capacity is decoded from the 16 CSD bytes — **not** from the `&38` microcontroller init return (which only reports MMC=1/SD=2), **not** from OCR alone. See §6.

---

## 1. Ports

The Trinity port map and the general SPI lag/auto-null mechanics live in `trinity-capabilities.md` §2–§3 — not repeated here. The two ports this interface uses:

- **`&DC`** — microcontroller select + status. `OUT` writes a select byte (§2 table); `IN` returns status: **bit 3 = busy**, **bit 1 = card present**, **bit 2 = write-protect** (sense inverted — the fork reads it as `CPL / AND 4`, `dis:5996`). **Concretely (for a model/probe): a *writable* card must read `&DC` bit 2 SET** — `hd.svb-t` at &A91B does `IN(&DC) / CPL / AND 4`, so bit-2-set → `CPL` clears it → no WP abort, whereas a model returning `&DC`=`0` reads as write-protected and **every CMD24 write aborts** to the ROM WP-fail path (`&444B`). (i145h finding, verified against the disassembly.)
- **`&DF`** — SD-card transparent SPI byte relay (the SD analogue of the ENC's `&DE`). The standard SD-SPI command ladder ports directly onto it.

(The Trinity-presence identity probe — commands `&08`/`&09` to `&DC`, replies `'T'`,`'R'` read from **`&DD`**, the EEPROM data port — is documented in `trinity-capabilities.md` §2 / `bdos-trinity-fork-analysis.md`; it is not part of the SD data path.)

## 2. The `&DC` SD select bytes

| OUT `&DC` | Effect | Used by |
|-----------|--------|---------|
| `&04` | Idle / all-deselect, **auto-null off** | bracket every transaction (init entry &A626 `dis:5553`; deselect tail &A8EE `dis:5956`) |
| `&31` | **SD select** (manual mode — Z80 supplies its own dummy `&FF` per byte) | command phase (&A823 `dis:5846`) |
| `&30` | SD deselect | failure path &A667 `dis:5588`; deselect tail &A8DA/&A8E7 `dis:5947` |
| `&38` | Microcontroller **"SD init"** command — returns 1=MMC, 2=SD in the next SPI read | init ladder &A637 `dis:5561` |
| `&3F` | **SD select with auto-null** — the microcontroller injects the per-byte dummy write, so the bulk loops are pure `INI`/`OUTI` (no `OUT` per byte) | data/response phase (&A85A `dis:5871`) |

`&31` and `&3F` are the two SD-select modes: `&31` for the short command/argument phase (Z80 clocks each byte explicitly), `&3F` for the long response/data phase (auto-null). Every transaction is bracketed `&04 … &04` and runs **under `DI`** (set in the command sender, cleared in the deselect tail), so there is no ESC-abort window inside SD I/O.

## 3. SPI byte primitives

The three primitives are the heart of the port (annotation block `dis:5777-5786`):

- **`wait` (&A7CC, `dis:5791`)** — busy-poll before every byte:
  ```
  a7cc:  in a,(&dc)      ; status
  a7ce:  and 8           ; bit 3 = busy
  a7d0:  ret z           ; ready
  a7d1:  jr a7cc
  ```
- **`sd.out` (&A7C5, `dis:5787`)** — SPI write byte: `wait`, then `OUT (&DF),A`.
- **`sd.in` (&A7F0, `dis:5810`)** — SPI read byte with the one-byte lag: write a dummy `&FF` (`sd.out`), then `IN A,(&DF)`. The `&A7EA` entry (`dis:5808`) does a *double* dummy first (for two-byte-lag reads where a leading byte must be skipped).
  ```
  a7f0:  ld a,&ff
  a7f2:  call &67c5      ; = sd.out  (wait + OUT (&DF),&FF)
  a7f5:  in a,(&df)      ; the byte clocked in by the previous OUT
  ```

In **auto-null (`&3F`) mode** the dummy `&FF` is supplied by the microcontroller, so bulk reads/writes drop `sd.out` entirely and become `wait` + `INI` / `wait` + `OUTI` per byte (§7, §8). This is the SD analogue of the ENC's `&2F` auto-null bulk path in `encdrv.asm`.

## 4. SD init / detect ladder (&A623, `dis:5552`)

Called as `DI / CALL &A623 / EI` from the Trinity-detect path once the card-present bit is set (`dis:4888`). The sequence:

1. **`OUT (&DC),&04`** — all-deselect, auto-null off (&A626).
2. **Microcontroller wake** (&A62C–&A64D): `&31` select → **`&38`** (microcontroller SD-init; B = its 1=MMC/2=SD reply) → `&31` reselect → poll `sd.in` until it returns **`&FF`**. The wake loop at &A643 is `call sd.in / inc a / jr z` (breaks when `sd.in` returns `&FF` — the card settling to SPI-idle MISO-high), looping B (the `&38` reply) times. **This is the OPPOSITE sense to the textbook CMD0-response poll** (which waits for the response to *drop* from `&FF`): a port/probe that waits here for `!= &FF` hangs. (Verified against the disassembly while running Colin's real ladder through the i145c model, i145f.)
3. Then the **standard SPI-mode command ladder**, Z80-driven, each command sent via `sd.cmd` (§5):

| Cmd | Addr / cite | Sent as (opcode, CRC, arg) | Purpose |
|-----|-------------|----------------------------|---------|
| CMD0  | &A64F `dis:5574` | `&40`, CRC `&95`, arg 0 | GO_IDLE → expect R1=1 |
| CMD8  | &A679 `dis:5598` | `&48`, CRC `&87`, arg `&01AA` | SDv2 if-cond; echo `&01AA` verified (`dis:5604`) |
| CMD55+ACMD41 | &A696 `dis:5613` | `&77` then `&69`, HCS=`&40000000` when SDv2, ≤2500 tries | init; ready when R1 bit0 clears |
| CMD1  | &A6CB `dis:5642` | `&41`, ≤5000 tries | MMC fallback (if ACMD41 path declined) |
| CMD58 | &A6E2 `dis:5655` | `&7A` | READ_OCR → **CCS bit** → card type (block vs byte addr); also pokes the seek `<<9` skip (§7) |
| CMD59 | &A705 `dis:5674` | `&7B` | CRC off |
| CMD9  | &A711 `dis:5678` | `&49` | **SEND_CSD** → 18 bytes (16 CSD + 2 CRC) to buffer `&780F`/`&B80F` via the token-read helper &A7D3 |
| CMD16 | &A728 `dis:5687` | `&50`, arg 512 | SET_BLOCKLEN 512 |
| CSD parse | &A736 `dis:5692` | — | decode capacity → 32-bit block count (§6) |

Failure path **&A662** (`dis:5585`): clear the OK flag, flush, `OUT (&DC),&30` deselect, retry/abort. Fixed CRCs are sent because cards do not enforce CRC on reads in SPI mode (only CMD0/CMD8 carry real CRCs `&95`/`&87`).

The **CSD data-token read** uses helper **&A7D3** (`dis:5795`): poll `sd.in` up to 256 tries for the `&FE` start-of-data token, then read 18 bytes (`&12`) into `(HL)` — 16 CSD bytes plus 2 trailing CRC bytes, the CRC discarded.

## 5. The CMD17/CMD24 command sender — `sd.cmd-with-address` (&A81F, `dis:5844`)

This is the read/write sector command path (distinct from the init-phase `sd.cmd` &A7F8 which sends a literal-arg command). The byte sequence:

```
a81f:  di
a820:  wait ; OUT (&DC),&31        ; SD select (manual)
a82b:  out (&df),a (A=&FF dummy)   ; one flush byte
a833:  out (&df),B                 ; command byte  (CMD17 = &51 read, CMD24 = &58 write)
       ; 32-bit address from the POKED IMMEDIATES, big-endian:
a835:  ld hl,nn  -> out (&df),H / out (&df),L   ; HIGH word, operand at &A836/7
a842:  ld hl,nn  -> out (&df),H / out (&df),L   ; LOW  word, operand at &A843/4
a84f:  out (&df),&FF               ; dummy CRC
a85a:  OUT (&DC),&3F               ; switch to SD select WITH AUTO-NULL
a85e:  wait ; in a,(&df)           ; poll R1 until bit7 clear (top bit = 0), ≤&58 tries
```

The 32-bit sector/byte address is **not passed in a register** — it is *poked into the `LD HL,nn` immediates* at `&A836` (high) and `&A843` (low) by the seek path (§7, self-modifying code). After the argument+CRC are clocked out under `&31`, the sender flips to **`&3F` auto-null** so the entire data phase that follows needs no per-byte dummy. The data phase is then:

- **Read (CMD17)** — wait for the `&FE` data token, then `INI`×510 + the 2-byte tail, each gated on `wait` (`hd.ldb-t` at &A999, `dis:6055`).
- **Write (CMD24)** — WP check (§8), `&FE` token out, `OUTI`×510 + 2-byte tail, 2 dummy CRC bytes, then the post-CRC tail (`&A893`): a **throwaway `IN (&DF)`** (the gap byte, *discarded*) + a `&DC` busy-poll, **then** the data-response read (`&A89B`: `AND &1E / SUB 4`, accepted == `&04`; `&05` masks to `&04`), **then** the busy-wait — a `&DC` busy-poll then `IN (&DF) / INC A / JR Z` (`&A8AB`) looping until `&DF` returns `&FF` (write complete), ≤65536 reads. The throwaway read before the data-response is easy to miss: a model that answers the data-response on the *first* post-CRC `&DF` read fails (the driver's real check is the *second* read). (`hd.svb-t` at &A918, `dis:5995`; tail at &A86B `dis:5889`. i145h finding.)

**Deselect (&A8D7, `dis:5946`)**: flush `&FF`, `OUT (&DC),&30` twice (around a dummy write), `OUT (&DC),&04` (auto-null off / all-deselect), `EI`, `RET`.

## 6. Capacity → record count (the i145 crux)

**Mechanism: bare CMD9 → 16 CSD bytes → version-aware decode → 32-bit block count → records.** No capacity is taken from the `&38` init reply (MMC/SD only) or from OCR alone (CMD58 yields only the CCS addressing-mode bit).

**CSD parse (&A736, `dis:5692`).** The 16 CSD bytes sit at buffer `&780F` (= `&B80F`, the workspace 1.5a used for the ATA IDENTIFY block). The `(&780F) AND &C0; cp &C0; jp nc` at &A736 is only a **reserved-value reject** (CSD_STRUCTURE == `11` → error), **not** the v1/v2 selector. The actual **v2-vs-v1 branch is `cp 3` at &A743** on the **card-type flag the init ladder accumulated from CMD8 + CMD58** (kept in the alternate `A'`), **not** on CSD byte 0: `A' == 3` (CMD8 accepted + OCR CCS set) → the v2 layout, else → the v1 layout. For real cards the two agree (an SDHC card answers CMD8 *and* has CSD_STRUCTURE `01`), but a faithful **emulation model / probe must keep them consistent**: a v1 card must reject CMD8 (R1 `&05`, illegal-command) and report a CCS-clear OCR, or the decode takes the wrong layout branch (the i145c/i145f finding). The two layouts:

- **v2.0 (SDHC/SDXC, CSD_STRUCTURE == `01`)** — branch at &A746 (`dis:5700`): `C_SIZE` is the 22-bit field in CSD bytes 7..9 (`&7816/7/8`), and `blocks = (C_SIZE + 1) << 10`, i.e. `(C_SIZE+1) × 1024` 512-byte blocks (`dis:5703-5732`).
- **v1.0 (SDSC, CSD_STRUCTURE == `00`)** — branch at &A779 (`dis:5733`): `blocks = (C_SIZE + 1) × 2^(C_SIZE_MULT+2) × 2^READ_BL_LEN / 512`, assembled from `C_SIZE` (CSD bytes 6..8, masked), `C_SIZE_MULT` (CSD bytes 9..10), `READ_BL_LEN` (CSD byte 5 low nibble). The fork computes `2^(C_SIZE_MULT+2)` as a shift count `B` (&A797 `dis:5750` builds `B = C_SIZE_MULT + 2 + ...`) and a second `READ_BL_LEN` shift via the shift helper **&A7BB** (`dis:5770`: `B` iterations of `ADD HL,HL` across a 32-bit `HL:DE` with `RL C`), then a final `>>9` (`/512`, the `SRL D / RR E / RR H / RR L` at &A7B0 `dis:5765`).

The resulting **32-bit block count** is stored to the DVAR slots `&80BD–C0` (the old `hd.sct`/`tot.sct` Atom geometry slots, repurposed — see `bdos-trinity-fork-analysis.md` and the boot-block map `dis:141-148`).

**Block count → records (&A3D2, `dis:5146`)**, 32-bit arithmetic using the divide helper **&A58C** (`dis:5419`, a 32-bit/16-bit restoring divide, 32 iterations):

- `records = blocks / 1600`. The fork reaches `/1600` in two steps at &A3D2 (`dis:5146`): first `LD BC,&01F4 / CALL &A58C` divides by **500** (`&01F4`), then the `ADD HL,HL`/`ADC HL,HL` + `×3` accumulate + final `SRL/RR` chain at &A3D8–&A3F7 (`dis:5148-5177`) scales by `×3.2` (500 × 3.2 = 1600). **The formulas are byte-for-byte the 1.5a `hd.init` formulas** (1.5a source ~lines 1739–1768), now in 32-bit form:
  - `records = blocks / 1600`
  - `record-list size (sectors) = (records + 32) / 32` (truncating)
  - `base = record-list size + 1` (stored to `&80C2`; `base+1` = first record's start sector)
  - `usable = blocks − base`; `records = usable / 1600`, counting a partial last record when ≥ 5 tracks (50 sectors) remain.

These are the same formulas the i145 plan names and the i62 experiment verified against AL 1.5a and SimCoupé's `IsBDOSDisk` — see the i145 plan and `bdos-trinity-fork-analysis.md` §5/§7; **single source of truth: do not re-derive them here, mirror the cited code.** The human-readable size print ("` Kil`/` Meg`/` Gig`/` Ter`" + "`bytes`" + "`BDOS records available = n`") is the `/1000` suffix walk at &A406 (`dis:5144`) — UI only, not load-bearing for the port.

`BD_RECORDS` (this project's name for the card's total record count, `&80C4/5` = `last.record`) is therefore exactly the `records` value above. The i145 port re-derives it from a fresh CMD9 because B-DOS exposes no stable hook/DVAR for it (the fork relocated its internals).

## 7. Record → sector mapping

**Record selection (&A0A2, `dis:4534`).** `record# × 1600` (32-bit multiply `mult16-32` at &A113, `dis:4613`) `+ (base+1)` (the `&80C2` constant loaded at &A103, `dis:4602`) = the **32-bit base sector** of the record, **poked into the seek immediates at &A185 / &A188** (`dis:4606`, self-modifying). No CHS anywhere — the 1.5a cylinder/head/sector decomposition is deleted. The 800 KB record model (1600 sectors × 512 B per record) is identical to 1.5a.

**Seek (`hd.seek-t`, &A16B, `dis:4702`).** Validates track (`2×track < &9F`, i.e. ≤79) and sector (1–10) exactly as 1.5a (else rep83 "sector not found"), then:

1. `linear = conv.de(D=track, E=sector)` — `conv.de` at **&A151** (`dis:4657`) computes `10×track + sector − 1`, with side-2 handling via bit 7 of the track (`ADD A,A / ... ADD A,&A0`). Identical to 1.5a.
2. `32-bit sector = record_base (poked) + linear` (&A17F–&A18D `dis:4715`).
3. **Address-mode shift** — the `JR +d` at **&A18E** (`dis:4723`) has its displacement byte at **&A18F poked at card init** from the CMD58 CCS bit:
   - `d = 0` → fall into the `<<9` shift (`ADD HL,HL` ×9 across `HL:DE`, &A195 `dis:4728`): **byte addressing** — MMC / SDv1, CMD17/24 take a *byte* offset, usable capacity capped at 4 GB.
   - `d = &0A` → skip the 10 shift bytes: **block addressing** — SDHC/SDXC, CMD17/24 take a *block* (sector) number. This is what carries beta 6 to its advertised 64 GB.
4. Result poked into the SD command-argument immediates at **&A836 / &A843** (the `sd.cmd-with-address` operands, §5) via the store at &A19A/&A19D (`dis:4732`).

`seek.base-t` (&A1A6, `dis:4737`) loads the poked record base alone (directory access — seek to record start) and merges into the same store path.

**List-sector layout.** Sectors `1 .. base−1` of the card hold the record-list directory entries (16 bytes each, the format `RECORD` lists); a record is **free ⇔ its masked first-name byte == 0**. That detection, and the mandatory show-name+confirm-before-overwrite gate it backs (the `"BDOS"` stamp @ offset 232 of a record's first dir entry, `get.label` at &A8DC region, `dis:1951-2007`), are the subject of `docs/specs/trinity-record-detection-design.md` and the i119 work — **see those; not restated here.** The i141 raw list-sector read mirrors this same `read sectors 1..base-1, parse 16-byte entries` shape against the frozen on-card layout.

## 8. Storage hooks and sector dispatch

The fork leaves the **39-entry SAMDOS/B-DOS hook surface byte-identical to 1.5a** (verified by the i71 diff — `bdos-trinity-fork-analysis.md` §"hook surface untouched"); only the device layer underneath swapped. Storage-relevant hooks (hook table at &A39F dispatch, `dis:533-545`):

| Hook | # | Role |
|------|---|------|
| HSAVE | 132 | save file |
| HDINIT | 135 | boot-time mass-storage init (runs the §4 ladder) |
| HOFLE | 147 | open file for output |
| HSBYT | 148 | save byte |
| HWSAD | 149 | write sector at (record/track/sector) |
| HSVBK | 150 | save 510-byte block |
| **HRECORD** | 156 | select record (A=0 + record# in HL → §7 selection) |
| HVEBK | 157 | verify block |
| HLBYT | 159 | load byte |
| HRSAD | 160 | read sector at |
| HLDBK | 161 | load block |

The six **device-dispatch sites** in the common sector-I/O code each test the ambient device and branch to one of the six SD routines (1.5a Atom `&20` READ-SECTOR / `&30` WRITE-SECTOR commands are gone; the SD equivalents funnel into `sd.cmd-with-address` §5):

- `hd.lbuf-t` &A954 (`dis:6026`) — load sector buffer (CMD17, `INI` loop).
- `hd.ldb-t` &A999 (`dis:6055`) — load/verify 510-byte block (CMD17 = `&51`, `&FE` token, `INI`).
- `hd.svb-t` &A918 (`dis:5995`) — write core (WP check `IN(&DC) CPL AND 4` OR `hd.wp`, then CMD24 = `&58`, `&FE` token, `OUTI`).
- `hd.svbk-t` &A903, `hd.veb-t` &A9FF, `hd.lds-t` &AA3D (dir-sector read building the BAM).

Higher-level writes therefore reach the card as: **HRECORD to select the record, then ordinary HSAVE / HOFLE+HSBYT** — the hook call shapes proven on AL 1.5a in i62 work unchanged on 1.5t (no raw SPI driver needed for the HSAVE path; the raw SPI path is only for the CSD probe and any direct list-sector read).

## 8a. The hook ENTRY path before the sector dispatch (HWSAD/HRSAD &9E16) — i280 findings

§8 covers the sector-I/O routines once the device is selected and the LBA poked. The hook **entry** path that gets there — what HWSAD/HRSAD do *between* `rst 8 / defb n` and the §8 dispatch — is a separate, more entangled layer. i280 traced it (tool: `tools/netboot-oracle/z80/cmd/bdostrace/`) because our netboot serve's per-block write **hangs on real hardware** in exactly this region (`bdos_write_sector` → `rst 8 / defb 149`), while B-DOS's own record writes succeed.

What the trace establishes (the lower layers all run clean in the flat koron-go harness against the modelled SD card):

- **The §4 init ladder, the records math, and the §5/§8 CMD24 write-core + CMD17 read-core all PASS in emulation** (the existing `sd_init_colin_test.go` / `csd_decode_colin_test.go` / `sd_record_io_colin_test.go`, re-confirmed by `bdostrace -scenario init|writecore`). So the SD-SPI model and the low-level write are faithful — the hardware hang is **not** in the write core.

- **The HWSAD hook HANDLER is &9E16** (hook table at &839F, `dis:606`; HWSAD=149 → index (149−128)·2=&2A → entry `16 5e` → handler &5E16+§B = real &9E16). Before reaching the §8 write core it runs a **prelude**:
  1. **page-setup** (&9E27–&9E60): reads `hk.hl` (&81DA), takes its top two bits as a source *page* (`AND &C0 / SUB &40 / RLCA RLCA`), reads HMPR (`IN A,(&FB)`), and `OUT (&FB)` switches the page in — i.e. HWSAD interprets the caller's HL as a **paged pointer (page in the top bits)**, not a flat address. `H & &C0 == 0` (a section-A pointer) skips the switch (`JR Z,&9E89`).
  2. **device-select** (&9E3F `call &8662`, keyed on `hk.a`): `A==2` → Trinity store (`&780B`=2); `A==1` → floppy store (`&780B`=1); `A==0` → runs the floppy-port setup (&8680: pokes FDC base `&E0` into the I/O routines at &45EA/&45A5) and leaves `&780B` unchanged.
  3. falls into **xsad/wr.buff** (&83ED), whose **device-dispatch** (&83F7) does `call &8684` (reads `&780B`: `dec a; ret nz`) then `jp nz,&A8F4` (the §8 SD path) — **else falls through to &8406+, which polls the FDC ports `IN A,(&E0)` / `IN A,(&E4)` in an un-timed loop**. With no controller that poll never exits: this is the **shape of the hardware hang** (ambient device wrongly = floppy when the write runs).

- **The full entry prelude is NOT traceable in the flat section-B harness.** It calls SAM-ROM/system routines the flat model lacks — the trace **escapes the run window at real &9BF1 → `call &0103`** (a B-DOS↔ROM bridge helper invoked throughout the hook machinery) and runs off into unmapped low memory. Faithfully tracing the prelude requires the real paged environment (SAM ROM at &0000/&C000, system vars at &5000, B-DOS in its real &8000 page via `LoadROMImage` + paged boot) — the flatten trick that works for the self-contained §8 routines breaks here because the prelude reaches into ROM + sysvars the section-B window overlaps. **This is i280b.**

- **The hardware busy-wait hang cannot reproduce in koron-go regardless**: the SD model always clears `&DC` bit 3 (`sdcard.go`). So emulation can derive the *successful* entry contract (the gold sequence) but cannot reproduce the *fault*; pinning the exact missing contract bit needs the i280b paged-boot trace and/or hardware instrumentation (the i271 UDP marker channel).

**Carry into the fix (i280b → `bdos_seam.asm`):** the entry contract is *paged-pointer HL + device in `&780B` + drive in `hk.a`*, not the flat `(A=drive, D=track, E=sector, HL=flat-source)` our seam currently assumes. Note the symmetry constraint: our `bdos_read_sector` (HRSAD, shares the `rwsad` entry) passes the same flat shape and **works on hardware**, so the discriminator between the working read and the hanging write is still to be pinned — do **not** assume it is `hk.a` alone (the i270 `A=0` change was necessary-but-insufficient on hardware).

## 8b. The gold entry contract — paged-boot trace (i280b)

§8a localised the hardware write hang to the HWSAD hook entry prelude but could
not trace it: the flat section-B harness escapes into SAM-ROM bridges (`call
&0103`/`&0033`/`&0005`) it has no model for. i280b runs the prelude in the **real
paged environment** — a tool (`tools/netboot-oracle/z80/cmd/bdostrace-paged/`)
that boots Colin's captured ROM + EEPROM to a B-DOS-resident editor idle (the
`samboot_real_boot_test.go` recipe: `LoadROMImage` + device-linear EEPROM + paged
boot to `&01CB`), with the SD card attached so HDINIT mounts it. In that state
ROM0 is at `&0000` (LMPR `&1F`) and B-DOS is resident at section C (HMPR `&1D` =
page 29), so the real dispatcher/handler bytes are present and the ROM bridges
resolve. Findings (all cross-checked against the 1.5t annotated disassembly,
addresses cited; the disassembly's internal targets are section-B aliases =
real − `&4000`):

**The hook-table dispatch is confirmed byte-exact.** The dispatcher is at real
**`&8319`** (`ld (hk.sp),sp` / saves IX + the caller's **alternate-set** regs
`A'`→hk.a (`&81D9`), `HL'`→hk.hl (`&81DA`), `DE'`→hk.de (`&81DC`), `BC'`→hk.bc
(`&81DE`); reads the hook code from main `A` as index = code−128, doubled, into
the table at **`&839F`**). The captured table resolves slot 149→handler `&5E16`
(= real **`&9E16`** HWSAD), 160→`&5E1B` (**`&9E1B`** HRSAD), 156→`&5FAB`
(**`&9FAB`** HRECORD) — matching §8a exactly.

**The discriminator is the AMBIENT DEVICE, and read/write share it — the
wr.buff-vs-rd.buff asymmetry hypothesis is REFUTED.** HWSAD (`ld hl,wr.buff`,
real `&83ED`) and HRSAD (`ld hl,rd.buff`, real `&844F`) join a **shared** prelude
at `&9E1E` and the **same** device-select **`&8662`** (called with `A`=hk.a). The
two device trampolines are byte-identical at the gate:
- wr.buff `&83ED`: `… call &8684 ; jp nz,&A8F4` (SD save) — else falls into the
  FDC poll at `&8406+` (`in a,(&E0)`, un-timed = the §8a hang shape).
- rd.buff `&844F`: `… call &8684 ; jp nz,&A954` (SD load) — else FDC poll `&8460+`.

Both gate on the **same** `&8684` read of the ambient-device var **`&780B`**
(`dec a; ret nz` → Z iff `&780B`==1). So there is no write-specific device
configuration distinct from the read's: a *successful* HRSAD and a *successful*
HWSAD require the identical ambient state, and our working read proves that state
is reachable. **The discriminator between our working read and hanging write is
therefore the ambient device at write time, not a separate write path.**

**The gold contract a successful HWSAD write needs** (the state HRECORD-select
establishes, read from the disasm + confirmed reachable in the paged boot):
- **`&8135` (device class) == `&44`.** The device-select's entry at `&8657` does
  `ld a,(&8135); cp &44; jp nz,&9F31` (B-DOS "device not present" error) before
  anything else. (Boot's HDINIT already leaves `&8135`=`&44` once a card mounts —
  the tool observes this post-boot.)
- **`&8132` (device number) == 2** (Trinity). `&8662` turns it into `&780B`:
  `cp 1`→`&780B`=1 (floppy), `cp 2`→`&780B`=2 (Trinity), else (A==0)→runs the
  floppy-port setup `&8680` and **leaves `&780B` unchanged**.
- **`&780B` (ambient device) == 2** at the dispatch, so both trampolines take
  `jp nz` → the SD path (`&A8F4`/`&A954`) instead of the FDC poll.

`&8132` and `&8135` are both written by the **HRECORD handler** (`&9F11`/`&9F15`)
— i.e. record-select is what arms the device class+number. The crucial corollary
for the seam: **hk.a (the drive in `A`) only re-runs `&8662`**; `A`=0 (our seam
post-i270) **leaves `&780B` as the prior select left it**. So if a stale `&780B`=1
(floppy) is in effect at the write, `A`=0 keeps it floppy → the FDC-poll hang —
which is exactly the necessary-but-insufficient shape i270 saw on hardware. The
fix must ensure the **ambient device is Trinity (`&780B`!=1)** at the write, which
the HRECORD-select that precedes our `bdos_write_sector` is supposed to do — so
the seam's job is to (a) HRECORD-select the target record immediately before the
write (re-arming `&8132`/`&8135`/`&780B`), and (b) pass an `A` that does not
*clear* it back to floppy (`A`=0 is safe **iff** the preceding select left
`&780B`=2; `A`=2 would re-assert it explicitly).

**Honest boundary (what did NOT run).** A raw `rst 8 / defb N` from arbitrary
post-boot code does **not** reach `&8319` in this snapshot: the ROM RST8 handler
(`&37CE`) dispatches DOS through the relocated DOS-call stack chain (`ld
sp,(&5C3D); jp &1D95`), and the editor-idle snapshot does not arm that chain (the
DOS-hook vector `&5AEE` reads `&0000`) for an *external* caller — so the hook
returns to the editor. Likewise, calling `&8319` or a handler directly is only
partially faithful: the prelude's page-setup `out (&fb)` repages section C away
from B-DOS mid-handler and the run then escapes into a ROM bridge. The tool
therefore confirms the **dispatch table + the device gate** empirically (boot,
table resolution, the `&8662`→`&780B` keying, both handlers reaching the shared
`&8662`) and derives the rest from the disassembly authority — the reliable source
per `feedback_port_diff_authority_first`. Reproducing the *fault* (the busy-wait)
remains impossible in koron-go regardless (the SD model always clears `&DC` bit 3,
§8a); that stays a hardware gate (the i271 UDP marker channel).

## 8c. Hardware retest — the `&780B` / hk.a theory is REFUTED (i280b-b2)

The §8b gold contract predicted that forcing the ambient device `&780B`=2 (the SD
path) would fix the per-block write hang, and that our seam's bug was
`bdos_write_sector` leaving `A`=sector (so block 0 = sector 1 → `A`=1 → `&8662`
sets `&780B`=1 floppy → the `&8684` FDC-poll). The fix passed `A`=2
(`BD_DEVICE_TRINITY`) to HWSAD/HRSAD so `&8662` forces `&780B`=2.

**Hardware retest (2026-06-28, TAPO self-serve + i271 UDP markers) REFUTES it.**
The exact fixed binary was pushed (`netboot_serve_boot_debug.bin`; the `A`=2 byte
`3e 02` verified present immediately before `cf 95` = `rst 8 / defb 149` at
`bdos_write_sector`), then a disk-record WRQ push (`curl -T … tftp://…/trinity-sam-disks/…`).
Markers: **`WRQ_ENTRY ×3 → DATA_BLOCK ×1 → hang`** — the per-block write still
hangs after the first 512-byte block (curl uploaded exactly 512 B then timed
out), the **same symptom** as before the change.

Conclusions:
- **The discriminator is NOT hk.a / the `&780B` floppy-gate.** Forcing `&780B`=2
  does not avoid the hang — so the §8a "ambient device wrongly = floppy → FDC
  poll" model is **not** (or not the whole of) the real fault. The handover's
  caution ("our working read shares the flat shape; don't assume hk.a") was right.
- **`A`=2 is not even a safe no-op.** The captured `&8662` A==2 branch runs
  Trinity-setup sub-calls (`call &4677; call &60e4`) *before* storing `&780B`=2;
  the hang may now be inside those, i.e. `A`=2 can trade one hang for another.
  (`A`=0 — i270 — takes the `jr nz,&8680` branch that skips both and leaves
  `&780B`; it was "insufficient" but for a different reason.)
- **The hang is somewhere in B-DOS's own HWSAD/SD-write code reached after
  `DATA_BLOCK`, and is still unlocalized.** Our six hardware fixes (leading `&FF`
  flush, bounded busy-wait, 4-step deselect, …) live in *our* `sd_csd.asm` /
  `encdrv.asm`; HWSAD runs **B-DOS's own** SD primitives (`&A918` etc.), which we
  do not patch — yet B-DOS's record writes work for Colin, so it is our
  *invocation*, not B-DOS's code, that is wrong. What that invocation gets wrong
  is now the open question (i280b-b2).

Next-step options for i280b-b2 (need fresh analysis, not another blind shot):
(1) extend the §8b paged-boot trace to actually run HWSAD end-to-end so the hang
point is observable in emulation (the §8b honest-boundary blocker — the handler
wanders without the real DOS-call SP/paging context — must be solved first);
(2) add finer i271 markers *around* (not inside) the HWSAD call and a bounded
guard so a hang reports rather than wedges; (3) diff our HRECORD-select →
HWSAD invocation sequence against how B-DOS's own RECORD-copy command reaches
HWSAD (the §8a/§8b authority path), register-for-register and paging-state for
paging-state. The fault does not reproduce in koron-go (the SD model clears
busy), so each hypothesis still ends in a hardware retest.

## 8d. Hardware re-localization (i280b-b2) — the failure is an ENC/SD shared-bus Heisenbug in the CLAIM path, not the per-block write

Option (2) above was taken: finer i271 markers were added around the per-block
write (`DBG_FLUSH_PRE` &21 at `rrs_flush_sector`; `DBG_HWSAD_PRE` &22 / `_POST`
&23 bracketing the `rst 8`/`defb 149` in `bdos_write_sector`) and, after the first
shot pointed earlier, around the two SD steps of the free-record claim
(`DBG_CLAIM_FIND_PRE` &14 before `bdos_find_record_for_strategy`'s CMD17 list reads;
`DBG_CLAIM_SELECT_PRE` &15 / `_POST` &16 bracketing the `bdos_select_record`
HRECORD hook). Three TAPO self-serve shots (current-main serve + markers, pushed
via `netboot_serve_boot_debug.bin`; disk-record WRQ `curl -T … tftp://…/trinity-sam-disks/x.mgt`):

| shot | markers compiled | UDP:9001 markers seen | curl |
|------|------------------|-----------------------|------|
| 1 | HWSAD only | `WRQ_ENTRY` ×1, then nothing | 0 B, timed out |
| 2 | HWSAD only | `WRQ_ENTRY` ×1, then nothing | 0 B, timed out |
| 3 | HWSAD + CLAIM | `WRQ_ENTRY → FIND_PRE → SELECT_PRE → SELECT_POST`, **×5** (curl re-sends the WRQ every ~6 s); never `WRQ_CLAIMED`/`WRQ_HANDSHAKE`/`DATA_BLOCK` | 0 B, timed out |

**Solid, reproducible conclusions (3/3):**
- **The disk-record push reproducibly fails to hand-shake** — curl receives 0 bytes
  every time (it never gets an OACK/ACK), independent of UDP marker delivery. So the
  serve never reaches `wrq_handshake`.
- **The per-block HWSAD write is NEVER reached.** None of `FLUSH_PRE`/`HWSAD_PRE`/`HWSAD_POST`
  ever fired. **This supersedes the §8a/§8b/§8c framing** that the blocker is inside
  B-DOS's HWSAD per-block write — the run dies upstream, in the WRQ free-record
  **claim → ENC re-arm → handshake** region, before any sector is written.
- **The failure point is acutely sensitive to where the ENC-TX markers sit (a
  Heisenbug).** Without the claim markers (shots 1–2) the serve stops after the first
  `WRQ_ENTRY` and **wedges** (curl's later WRQ retransmits are not processed — only one
  `WRQ_ENTRY`). With the claim markers interleaved (shot 3) the claim's SD find + HRECORD
  select **succeed and repeat** (`SELECT_POST` fires 5×) and the serve **loops** instead
  of wedging — but still never emits `WRQ_CLAIMED`. The only non-trivial step between
  `SELECT_POST` and `WRQ_CLAIMED` is **`serve_rearm_enc`** (the ENC RX re-arm after the
  SD list-reads + HRECORD select; `raw_record_sink_reset` between them is pure memory).

**Why this is the root-cause signal, not noise:** `dbg_marker` is itself an **ENC
transmit** (`build_udp_frame` + `drv_write`) on the **one-PIC Trinity controller the
SD shares**. Inserting ENC transmits between the SD operations changes the controller
state and **moves the symptom** (wedge-in-claim → loop-past-claim, hang-in-find/select
→ hang-in-rearm). A fixed logic bug in one routine would not migrate under
instrumentation; an ENC↔SD shared-bus / controller-state contention does exactly this.
The existing i242/i244/i245 fixes (drv_init-before-SD, `enc_rx_reestablish` after SD)
are evidently **incomplete for this exact sequence** — find (CMD17 reads) + HRECORD
select + `serve_rearm_enc`, interleaved with the serve's ENC serving.

**Methodology wall for the next step:** ENC-based remote markers **cannot cleanly
localize an ENC/SD-contention bug — they perturb it.** The next step is therefore NOT
another marker shot. It is one of: (a) **bounded guards** — convert every unbounded
SD/ENC busy-wait on this path (the find's CMD17 reads, the HRECORD hook's waits, and
especially `serve_rearm_enc`) into a *bounded* wait that, on expiry, reports a distinct
failure marker and returns rather than wedging — so the hang becomes observable without
adding ENC traffic *inside* the contended window; and/or (b) a **non-ENC observability
channel** (border/screen state, captured out-of-band). Then fix the ENC↔SD transition
so the claim → re-arm → handshake completes and the per-block write (the §8c target,
still unexercised on hardware) is finally reached. The fault still does not reproduce
in koron-go (the SD model clears busy and there is no shared ENC/SD controller), so this
remains a hardware-gated investigation. (i280b-b2 split here: the localization is
**i280b-b2b**; the bound is **i280b-b2c**; the fix is **i280b-b2d**.)

## 8e. Hardware shot (i280b-b2d attempt) — the failure is a POST-re-arm ENC-TX-readiness window, not the pre-re-arm bus hand-off; and the marker channel cannot observe it

A TAPO self-serve shot ran the **b2c-bound + a b2d candidate fix** build: a *quiesce
before the re-arm* — `sd_bus_quiesce` (OUT `&DC,&04` + bounded `&DC` busy-poll + fixed
settle, the same proven hand-off `sv_exit_to_trinload` uses to return to trinload) called
in `serve_rearm_enc` *before* `enc_rx_reestablish`, on the theory that the claim's SD ops
left `&DC` bit-3 hung so the re-arm's first `wait_ready` could not proceed. Tooling now
committed: `tools/hardware-shot/` (`listen-markers.py` + `run-shot.sh`).

**Markers seen (curl `-T … tftp://…/trinity-sam-disks/x.mgt`, 6 curl WRQ retransmits):**
`WRQ_ENTRY → CLAIM_FIND_PRE → CLAIM_SELECT_PRE → CLAIM_SELECT_POST`, repeated **×6**;
**never** `WRQ_CLAIMED`/`WRQ_HANDSHAKE`/`DATA_BLOCK`; **no `DBG_REARM_TIMEOUT` (&17)**;
curl received 0 bytes (no handshake), same end-state as the §8d shot-3.

**Conclusions:**
- **The quiesce-before-re-arm did NOT fix it** — the push still never hand-shakes. So the
  pre-re-arm bus hand-off was the *wrong side* of the re-arm (it was a sound but
  unnecessary change; not landed on `main` — prime directive: no unverified behavioural
  fix merges).
- **The dividing line is exactly `serve_rearm_enc`, and the symptom is POST-re-arm.** Every
  marker *before* the re-arm (`WRQ_ENTRY`/`FIND`/`SELECT`) escapes reliably on **all 6**
  iterations; every marker *at/after* it (`WRQ_CLAIMED`, the handshake reply itself) escapes
  on **none** — yet the ENC TX **recovers by the next serve-loop iteration** (the next
  `WRQ_ENTRY` escapes). So right after `serve_rearm_enc` runs (post-SD-claim) the ENC
  **accepts TX commands but puts no valid frame on the wire for a window**, then settles.
  The handshake reply (a `drv_write` immediately after the re-arm) falls in that dead
  window → curl never gets it → retransmit loop.
- **`DBG_REARM_TIMEOUT` is NOT the reliable signal b2c assumed.** `&17` is itself an ENC-TX
  `drv_write` emitted right after the re-arm, so it shares the dead window and cannot
  escape — its **absence is inconclusive** (could be "no timeout" *or* "timed out but the
  marker couldn't transmit"). This **extends the §8d methodology wall**: even a
  "post-window" ENC marker cannot observe a fault *in* the ENC-TX path.

**Next step (the real b2d gate) — a window-independent observability channel first.** The
clean, cheap discriminator: at `WRQ_ENTRY` (which escapes reliably), also emit the
`enc_timed_out` value left by the **previous** WRQ's `serve_rearm_enc`. curl's retransmits
make iterations 2–6 report iterations 1–5's re-arm result from *outside* the dead window —
distinguishing **(a) the re-arm timed out** (`&DC` bit-3 stayed hung → a bus-hand-off fix)
from **(b) the re-arm succeeded but the ENC TX was not yet wire-ready** (→ settle/verify the
ENC is TX-capable before the handshake, or retry the handshake). Only then is the actual fix
chosen and re-shot. The fault still does not reproduce in koron-go (the SD model clears busy;
no shared controller), so this stays hardware-gated.

## 8f. Hardware shot (i280b-b2f) — the discriminator's verdict: the re-arm SUCCEEDS; the gap is post-`ereset` TX-readiness, NOT a stuck bus

i280b-b2f added the window-independent channel §8e prescribed: `serve_rearm_enc` latches
`enc_timed_out` into `last_rearm_timed_out` at its end, and `handle_wrq` emits
`DBG_PRIOR_REARM_TIMEOUT` (&18) right after the (reliably-escaping) `WRQ_ENTRY` marker iff
that latch is set — so each WRQ retransmit reports the *previous* WRQ's claim re-arm result
from **outside** the post-re-arm dead TX window. All additive under `NETBOOT_DEBUG` (production
byte-identical). A TAPO shot:

**Markers (6 curl WRQ retransmits):** `WRQ_ENTRY → CLAIM_FIND_PRE → CLAIM_SELECT_PRE →
CLAIM_SELECT_POST` ×6 — and **NO `&18` on any iteration** (incl. iterations 2–6, which
report iterations 1–5's claim re-arm). curl 0 bytes, as before.

**Verdict (definitive):** `last_rearm_timed_out` was **0** every iteration ⇒ the claim's
`serve_rearm_enc` **did not time out** — every `wait_ready` saw `&DC` bit-3 clear and the
routine *returned* each time (we reach the next `WRQ_ENTRY`, so it neither timed out nor
wedged anywhere, incl. the unbounded `wr_phy_wait` PHY poll). **This kills the b2c/b2d
"`&DC` stays hung / the re-arm times out on a stuck bus" hypothesis** — the bus is
responsive and the re-arm completes. The failure is the *other* §8e branch: **the re-arm
SUCCEEDS but the ENC TX is not yet wire-ready for a window afterwards.** Decisive corroborating
detail: the markers *before* the claim re-arm (`WRQ_ENTRY`/`FIND`/`SELECT`, themselves ENC
TX) escape on every iteration, but everything *after* it (`WRQ_CLAIMED`, `WRQ_HANDSHAKE`, and
the handshake reply) escapes on none — so **the re-arm itself (its `ereset` full ENC soft-reset)
breaks TX for a window**, recovering by the next serve loop. (The SD claim ops disturb ENC
**RX** but not TX — hence the pre-re-arm markers work; the re-arm restores RX by resetting the
whole ENC, which transiently kills **TX**.)

**The b2d fix is now well-posed (no more hardware guessing needed to choose it):** after
`serve_rearm_enc`, make the ENC TX genuinely wire-ready before the handshake reply — e.g. wait
for `ESTAT.CLKRDY`/OST after the `ereset` soft-reset (the driver's own errata note already flags
CLKRDY as unreliable, so a bounded settle or a TX self-check), and/or retry the handshake reply
(it provably succeeds by the next iteration). Mirror the ENC driver's existing primitives; keep
it bounded (a stuck ENC must never wedge the serve). Then a confirming shot: the disk push
should reach `WRQ_CLAIMED → WRQ_HANDSHAKE` and curl should hand-shake.

## 8g. Hardware shot (i280b-b2d fix) — CONFIRMED: gating replies on `drv_wait_link` makes the push hand-shake and reach the per-block write; reveals the original §8a HWSAD hang

Research (datasheet + driver + the fix-#3 audit) established the mechanism: `serve_rearm_enc`'s
`ereset` is a full ENC28J60 soft-reset that resets the PHY → drops the 10BASE-T link; the ENC has
no auto-negotiation, so the link takes real time to re-establish, and a frame TX'd before it is up
is **silently lost** (TXRTS clears, TXIF sets, no egress). The serve is reactive everywhere EXCEPT
the WRQ handshake reply — the one immediate proactive-TX-after-`ereset` — so it landed in the
link-down window. Fix (b): `srv_send_tbuf` (the serve's universal reply path — RRQ DATA, WRQ
handshake, ACKs, ERROR all `jp` here) now `call drv_wait_link` (the existing i127 gate) before every
transmit — cheap when the link is up (every reactive reply), waits out the post-`ereset`
re-establishment when down. `enc_link.asm` is now included in the serve build. A TAPO shot of the
debug build:

**Markers:** `WRQ_ENTRY → CLAIM_FIND_PRE → CLAIM_SELECT_PRE → CLAIM_SELECT_POST → DATA_BLOCK →
FLUSH_PRE → HWSAD_PRE`, then silence (curl timed out at 35 s).

**Decisive — the §8d/§8e/§8f blocker is FIXED:** a single clean progression (not the 6× `WRQ_ENTRY`
retransmit loop of §8d/§8f). The handshake reply now reaches curl (held until link-up by
`drv_wait_link`), curl sends DATA block 1, the serve **receives** it (`DATA_BLOCK`), stages a full
sector (`FLUSH_PRE`), and **enters the B-DOS per-block write** (`HWSAD_PRE`). §8d established the
per-block write was **NEVER reached**; it is now reached. (The `WRQ_CLAIMED`/`WRQ_HANDSHAKE` *markers*
— raw `dbg_marker` TX in the link-down window, not via `srv_send_tbuf` — are still lost; the real
reply is what got through. `DATA_BLOCK`/`FLUSH_PRE`/`HWSAD_PRE` escape because they fire with the
link already up, no `ereset` between them.)

**The next blocker is now the original §8a HWSAD hang:** the run stops at `HWSAD_PRE` with no
`HWSAD_POST` (no ACK to curl → timeout). This is the B-DOS HWSAD per-block write hang §8a/§8b
localized (the i280b-b2 umbrella's original target), which the §8d handshake blocker had been
masking. **b2d is DONE** (the hand-shake deliverable, hardware-confirmed); the HWSAD write hang is a
**separate root cause** (B-DOS's own write code via the `rst 8`/`defb 149` hook) tracked as a fresh
item. Per §8a the hang is in the HWSAD hook entry prelude (handler `&9E16`: paged-pointer page-setup
+ the `&83F7` device-dispatch whose floppy branch is an un-timed FDC poll), not the §5/§8 write core
(which traces clean in koron-go).

## 8h. Root cause SETTLED (i280b-b2g) — the hook keys the device on `hk.a` = the SHADOW accumulator A', which our seam never set

The §8a hang is the un-timed FDC poll at `&8406`, reached when `&780B` (ambient device) is `1`
(floppy) at the write. The unanswered question through §8a–§8g was *what makes `&780B`=1 at our
write* when the preceding HRECORD-select set it to `2`. The disassembly settles it.

**The hook dispatcher reads `hk.a` from the ALTERNATE accumulator A', not main A.** At `&8319`
(`bdos15t-beta6.annotated.dis`):
```
8321: exx
8322: ex af,af'          ; <- switch to the ALTERNATE set
8323: ld (&81d9),a        ; hk.a = A'  (the shadow accumulator)
8331: ex af,af'
```
The HWSAD/HRSAD shared prelude then does `&9E3C ld a,(&81d9) ; &9E3F call &8662` — the device-select
**re-derives `&780B` from `hk.a` = A' on every call** (it enters `&8662` at the `cp 1`/`cp 2` keying,
*bypassing* the `&8657` `&8135`-class path): `A'==0` LEAVES `&780B` (so the prior select's `2` stands
→ SD), `A'==1` sets `&780B`=1 (floppy → the FDC-poll hang), `A'==2` runs the `&4677`/`&60E4` Trinity
sub-calls (§8c's wedge risk) then `&780B`=2.

**Our seam (`bdos_write_sector`/`bdos_read_sector`) loaded only main `A`/`D`/`E`/`HL` and never set
A'.** So `hk.a` was an **uncontrolled inherited shadow value** — whatever the last `ex af,af'` in the
ROM / IM1 frame-interrupt / driver machinery happened to leave. When that stray A' is `1`, the write
diverts to the FDC poll and hangs.

This **explains every prior result**:
- **§8c refutation:** the "force `A`=2" fix set *main* `A` (`3e 02` before `cf 95`), which the
  dispatcher never reads — so it had zero effect, exactly as the hardware shot showed ("same symptom").
- **Read/write asymmetry (§8a caveat):** both paths leave A' uncontrolled; they are issued from
  *different call sites* (read from `wd_finalize`, write per-block from `handle_data`) that inherit
  *different* stray A'. "Read works" was luck, not a real read-vs-write difference.
- **§8d Heisenbug:** inserting `dbg_marker` ENC-TX calls runs ROM/driver code that perturbs the
  inherited shadow A' → moves the symptom. Not a bus contention after all (for *this* hang).

**Emulation corroboration** (`cmd/bdostrace-paged` experiment 4, since the busy-wait itself can't
reproduce — the SD model always clears busy): with a clean post-select `&780B`=2, driving the device
gate shows `&780B`=2 → reaches the SD core `&A8F4`; `&780B`=1 → enters the `&8406` FDC poll and spins
to the step cap (the hang). And `&8662` with `hk.a`=1 forces `&780B`=1. So the A'→`&780B`→branch chain
is mechanical.

**Fix (`src/netboot/bdos_seam.asm`):** honor the documented `hk.a`=A' contract — `xor a ; ex af,af'`
immediately before the `rst 8` in both `bdos_write_sector` and `bdos_read_sector`, pinning `hk.a`=0.
That LEAVES the HRECORD-select's `&780B`=2 (Trinity SD) in force — deterministically, instead of
depending on a stray shadow value — and avoids the `A'`=2 sub-call wedge §8c flagged. Reuses B-DOS
entirely (one-instruction contract fix, no reimplementation). **Hardware-gated:** the busy-wait hang
cannot reproduce in koron-go, so the confirming gate is a TAPO shot — success = `HWSAD_PRE` →
`HWSAD_POST` per block and the push completes.

**The A' fix is correct (a real latent bug — the seam relied on undefined shadow state) but is
NOT, on its own, the hang fix.** A TAPO shot of the A'=0 build (verified: `af 08` = `xor a ; ex
af,af'` immediately before the `cf 95` HWSAD `rst 8` in the binary) still stops at `HWSAD_PRE` with no
`HWSAD_POST` — the **same** end-state as §8g. So pinning `hk.a`=0 removed the nondeterminism but did
not stop the hang. With A' now controlled, the FDC-vs-SD branch is decided **solely** by whether the
claim-select's `&780B`=2 persisted to write time — which the next probe tested.

## 8i. Hardware probe (i280b-b2g) — the hang is NOT HWSAD-specific: ANY data-phase SD hook hangs

To test "did the claim-select's state survive to the per-block write", a probe build re-issued the
**HRECORD-select immediately before the write** in `rrs_flush_sector` (reusing `bdos_select_record`,
keeping the A'=0 fix on the write). A TAPO shot:

**Markers:** `WRQ_ENTRY → CLAIM_FIND_PRE → CLAIM_SELECT_PRE → CLAIM_SELECT_POST → DATA_BLOCK →
FLUSH_PRE`, then silence — **it hangs in the re-select itself, before `HWSAD_PRE`.**

**This is the decisive re-localization.** The re-`HRECORD`-select is a *different* SD hook than HWSAD,
yet it **also hangs**, and at the *same* place in the flow (the first SD hook after `FLUSH_PRE`). The
identical HRECORD-select **succeeded at claim time** (`CLAIM_SELECT_POST` fires every shot). So the
hang is **not specific to HWSAD or to the write contract** — it is that **any B-DOS SD `rst 8` hook
invoked in the data phase hangs**, while the same hook works in the claim phase. The differentiator is
the **context between the two phases**: `serve_rearm_enc`'s ENC `ereset` (a full soft-reset of the
shared one-PIC ENC/SD Trinity controller) + the handshake + the serve-loop return. This **resurfaces
the §8d shared-bus framing** (an `ereset` leaving the SD side of the shared controller in a state where
the next SD command's busy-wait never clears) — now sharpened: it is reproducibly the **first
data-phase SD hook** that wedges, and b2d's `drv_wait_link` (which fixed the ENC-TX *handshake* after
`ereset`) does **not** cover the **SD side** after `ereset`.

(Confound to control next: the probe's re-select did not carry the A'=0 fix, so its HRECORD ran with a
stale A' too — a stale-A' HRECORD path is a possible alternative cause. But HRECORD at claim time also
had a stale A' and worked, so the phase/context remains the dominant variable.) The A'=0 contract fix
and the `cmd/bdostrace-paged` experiment-4 tooling land regardless (real latent-bug fix + investigation
harness); the remaining data-phase-SD-hook hang is tracked as a fresh item. **Next:** re-init / quiesce
the SD side of the shared controller after `serve_rearm_enc`'s `ereset`, before the first data-phase SD
hook (with the A'=0 fix applied to the re-select probe to remove that confound), then re-shoot.

## 8j. Root cause of the data-phase hang (i280b-b2h) — the paged-pointer `hk.hl` contract, as §8a predicted; the SD-bus and DI/EI theories REFUTED

Two grounded refutations cleared the field, leaving the §8a contract bug:

- **REFUTED — `serve_rearm_enc`'s `ereset` (`OUT &DC,&28`) damages the SD side.** `&DC`'s
  high nibble is a **peripheral mux** (Colin's Trinity manual: `&1x`=EEPROM, `&2x`=ENC,
  `&3x`=SD): `&28` is the **ENC-only** soft-reset. The ENC28J60 datasheet §11.2 confirms the
  SPI System-Command reset reverts only ENC registers and does not drive the RESET pin —
  it cannot touch the SD card or the PIC's SD state, and the `&DC` BUSY bit is a momentary
  per-`OUT` busy, not an SD-session state (§8f already proved the bus is responsive after
  the re-arm). So a quiesce/re-init of the SD after `ereset` is the wrong side (consistent
  with §8e's quiesce-before failing). The koron-go `&28` handler likewise resets only the
  ENC.
- **REFUTED — the data-phase hooks run with interrupts enabled and the claim-phase under
  `DI`.** Both run under the **same** ambient `EI`: `sv_serve_loop` runs `EI` (the `ei` after
  `serve_main`'s bring-up), and neither `handle_wrq`'s claim nor `handle_data`'s write adds a
  `di` around its SD hook. Same interrupt state in both phases → not the discriminator.

**The real discriminator — the source pointer's top two bits.** The HWSAD/HRSAD prelude
(§8a, `&9E27-&9E60`) reads `hk.hl`, takes its **top two bits as a source page** (`AND &C0 /
SUB &40 / RLCA RLCA`), and `OUT (&FB)` (HMPR) **pages that in at section C** — *unless* `H &
&C0 == 0` (a section-A pointer), when it skips the switch (`JR Z,&9E89`). Our seam passes
`HL = BD_WRITE_BUF = &BE42` (HWSAD) / `BD_READ_BUF = &BA07` (HRSAD) — both **section C, top
bits `10`** — so the prelude **fires the `OUT (&FB)`** and repages section C, **displacing
B-DOS from section C mid-handler** → the handler's own code vanishes → the hang (exactly the
§8b honest-boundary failure mode). The **claim**'s HRECORD passes a *record number* in HL
(top bits `00`) → the switch is **skipped** → it works. **That is the claim-vs-data
asymmetry**, fully grounded — and it means the §8i re-select hang (HRECORD, record# in HL,
no page-switch) was the **separate stale-A' confound**, not a generic "any data-phase SD
hook hangs".

This is precisely the fix §8a flagged and that got buried under the §8d–§8h handshake/A'
work: **"the entry contract is paged-pointer HL + device in `&780B` + drive in `hk.a`, not
the flat `HL=flat-source` our seam currently assumes."** The A'=0 fix (§8h, #730) was real
but secondary — the page-switch fires regardless of A'.

**Fix direction (i280b-b2h):** make `hk.hl` honor the paged-pointer contract — read the
prelude's exact page+offset semantics (`&9E27-&9E60`) and how B-DOS's own write supplies its
source (does it page the caller's data into a section that does NOT overlap B-DOS, then copy
to `wr.buff`?), then either (a) pass our staging buffer as a correctly-encoded paged pointer,
or (b) stage the data where the prelude reads it flat (top bits `00`), or (c) hand B-DOS the
source the way its own HSAVE path does. Reuse B-DOS; do not reimplement. The page-switch is
observable in the `bdostrace-paged` harness (drive HWSAD with a section-C `hk.hl` and watch
the `OUT (&FB)` repage B-DOS away — the wander §8b already saw), so the fix is
emulation-checkable before a TAPO shot (success = `HWSAD_PRE → HWSAD_POST` + push completes).

## 8k. §8j CONFIRMED in emulation against Colin's bytes — the decode + the page-0/1/2 constraint (i280b-b2h)

§8j was static analysis. This is the **running proof** against Colin's real B-DOS 1.5t:
`cmd/bdostrace-paged` experiment 5 + the `SKIP_PRIVATE_TESTS`-gated regression
`TestHWSADPagedPointerContract` (`samboot_real_boot_test.go`) boot the captured ROM/EEPROM to
the B-DOS-resident idle, then drive the **real** HWSAD prelude (`&9E1F-&9E60`, run at its
section-B alias `&5E16` with B-DOS paged into section B) with each candidate `hk.hl`,
`hk.a`=1 (so the device-select `&8662` RETs cleanly), trapping at `&9E60` to read the page the
`out (&fb)` actually switched into section C.

**The decode, measured (not derived):**
- `page = (H>>6) − 1`, written **raw** to HMPR (port `&FB`). It is **not** combined with any
  base page — `&8662` and `&9C6A` (the two intervening calls) leave `&81DE` untouched. So the
  page is one of **0, 1, 2** only: `H[7:6]`=`01`→0, `10`→1, `11`→2.
  - `hk.hl=&7E42 → section C ← page 0`; `&BE42 → page 1`; `&FE42 → page 2`.
- `H[7:6]==00` (a `<16384` address) takes the `&9E89` **range-error** path — the `out (&fb)`
  is never reached. There is **no flat / no-switch branch** (so §8j option (b) — "stage where
  the prelude reads it flat" — does **not exist**; a section-A pointer is rejected, not read
  in place).
- After the switch the SD writer streams the 512 source bytes **from section C** at
  `((H & &3F) | &80):L` (the `set 7,h` at `&9E59` frames the offset into `&8000-&BFFF`).

**Our seam's bug, confirmed:** `BD_WRITE_BUF = &BE42` (`H=&BE`) → page 1 → section C is
repaged to **absolute page 1**, **displacing B-DOS** (its own resident page, 29 in the boot
snapshot) mid-handler — exactly the §8d/§8g hardware hang (`HWSAD_PRE` fires, `HWSAD_POST`
never does). The test asserts the switch happens AND that page ≠ B-DOS's page for all three
non-zero patterns.

**The fix mechanism, also confirmed (the positive sub-case):** stage a sentinel in **absolute
page 1** at the in-page offset the prelude reads (for `hk.hl=&BE42` that is section-C `&BE42` =
page-1 offset `&3E42`), drive the prelude, and section C (now page 1) reads the **sentinel** —
i.e. a correctly-paged `hk.hl` makes the SD writer stream **our** buffer, not B-DOS. So the
contract is: **`hk.hl = ((sourcePage + 1) << 14) | (sectionC-framed 14-bit offset)`**, and
**the source must live in absolute page 0, 1, or 2** (the only pages the encoding can name).

**⚠️ CORRECTION (§8l): the premise of this paragraph is WRONG and the low-page-staging fix it
proposes is REFUTED by hardware.** trinload pushes the serve to **page 1** (not a high page), so
`BD_WRITE_BUF=&BE42` is **already in page 1** and `hk.hl=&BE42` already names it correctly (HMPR=1
at the write, measured). The "displacement" below is an editor-idle-**harness** artifact, not the
serve's behaviour. Read §8l; the text below is retained only as the (mistaken) reasoning §8l corrects.

**The remaining design problem (the actual seam fix, split to i280b-b2i).** The serve runs in
high memory (trinload pushes it to a high page `P`; `BD_WRITE_BUF` is in page `P`, not 0/1/2),
so the encoding **cannot name our buffer's page**. The fix must **copy each 512-byte sector
into an absolute page 0/1/2 scratch** immediately before the `rst 8`, then pass the paged
`hk.hl` for that scratch. The clean "reuse B-DOS" candidate is **B-DOS's own sector buffer
`res.buf` (&7913, in section B → page 1 when LMPR maps page 1 there)** — the buffer B-DOS's SD
path is built around. The open prerequisite before coding/shooting: **the serve's runtime
LMPR/HMPR page map** — which absolute page section B holds at serve time, and whether `res.buf`
(or another low-page region) is safe to use transiently without corrupting B-DOS/trinload/the
screen. A wrong page = a wasted shot on a **shared** SD card, so this is pinned down (research
the serve boot paging, or instrument it) before the write is attempted. The §8h `A'=0` fix
(#730) stays (real, independent); the page-switch fires regardless of `A'`.

## 8l. Hardware measurement REFUTES the §8k low-page-staging fix — the serve runs at HMPR=1, so &BE42 already names the buffer's page; the hang is DOWNSTREAM of the page-setup (i280b-b2i/b2j)

§8k derived a fix (stage the source in an absolute page 0/1/2 + pass a paged `hk.hl`) from
the premise that *"the serve runs in a high page P; `BD_WRITE_BUF` is in page P, not 0/1/2,
so the encoding cannot name our buffer's page."* **That premise is factually wrong, and a
hardware measurement refutes the whole fix direction.**

**The measurement (TAPO self-serve, i280b-b2i instrumentation).** The debug serve now reports
its live LMPR/HMPR as tag+value markers (`dbg_report_paging`, `DBG_HMPR_NEXT`/`DBG_LMPR_NEXT`)
at **both** the staging point (`FLUSH_PRE`) and the write (`HWSAD_PRE`). A shot read, at **both**
points, identically:
- **HMPR = `&01`** → section C (`&8000-&BFFF`) = absolute **page 1**, section D = page 2.
- **LMPR = `&1F`**.
- Same end-state as §8d/§8g: `… DATA_BLOCK → FLUSH_PRE → HWSAD_PRE`, **no `HWSAD_POST`** (hangs in the `rst 8`).

**Why this refutes §8k.** trinload pushes the serve to **page 1** (`trinpush-serve.py --page`
default 1; `out (HMPR),1 ; jp &8000`, `trinload.asm:234-238`), so `BD_WRITE_BUF = &BE42` (section
C) **is in absolute page 1** — confirmed live (HMPR=1 at staging *and* write). Our `hk.hl = &BE42`
(`H=&BE`) decodes to `page = (H>>6)-1 = 1`, so the prelude's `out (&fb),1` pages **page 1** into
section C — but HMPR is **already 1**, so it is a **no-op**: the buffer stays at `&BE42` and the SD
writer streams **our** bytes correctly. **There is no wrong-page read and no displacement.** The
§8k "displacement" was an artifact of the editor-idle **harness**, where B-DOS sits at page 29 in
section C (so `out (&fb),1` there pages B-DOS away) — *not* the serve's runtime map, where section
C is the serve's own page 1. So staging into a low page is **unnecessary**: the addressing is
already correct.

**What the hang is NOT (now cleared):** not the paged-pointer/`hk.hl` contract (§8k — addressing
is correct on the serve), not `hk.a`/A' alone (§8h `A'=0` fix is in this binary, still hangs — as
§8i already showed), not the page-switch displacing B-DOS (HMPR=1 ⇒ no-op). The earlier refutations
stand: not `ereset` damaging the SD side (§8j), not DI/EI (§8j).

**Where the hang IS (the live hypothesis for i280b-b2i):** downstream of the page-setup, in the
device dispatch / SD write core reached by the `rst 8`. With `&780B` armed Trinity by the claim's
HRECORD-select (CLAIM_SELECT_POST fired) and A'=0 leaving it, the write should take the SD path
(`&A8F4`), not the FDC poll — so the prime suspects are (a) the **ambient device `&780B` not still
2 at the data-phase write** (something between the claim and the write resets it — re-confirm by
reporting `&780B` at `HWSAD_PRE`, the same way this shot reported HMPR/LMPR), or (b) the real
**SD CMD24 write core busy-wait on hardware** (the `&DC` bit-3 poll that the koron-go SD model
cannot reproduce because it always clears busy, §8a), i.e. the ENC/SD **one-PIC shared-bus** state
left by `serve_rearm_enc` before the data-phase SD transaction (the §8d framing, for the *write*
core rather than the claim handshake b2d fixed). Next instrumentation: report `&780B` (and the
`&DC` busy bit) at `HWSAD_PRE`, and a marker *inside* the write core's busy-wait, to split (a) vs
(b).

**⚠️ Soundness traps for that next measurement (identified, NOT yet handled — design around them):**
- **Reading `&780B` from our seam is paging-fragile.** `&780B` is a section-B address
  (`&4000-&7FFF`), and B-DOS's ambient-device var lives in **B-DOS's own page** (the one the `&0033`
  ROM bridge maps into section B *during* a hook — that is why §8a/§8h read it only after
  `pageDOSIntoSectionB`). But at `HWSAD_PRE` the serve has **LMPR=`&1F` → section B = page 0**
  (measured this shot), so B-DOS's page is **not** in section B and a plain `ld a,(&780B)` reads
  page-0 garbage, NOT the real ambient-device var. To report `&780B` soundly the seam must first
  page B-DOS's var-page into section B (its serve page number is unknown — editor-idle shows 29, the
  serve's may differ), then restore — fragile. A cleaner discriminator is likely needed.
- **`&DC` is the Trinity *select* port, not a clean status read.** `IN (&DC)` returns controller
  status (busy=bit 3) but has select-side semantics; reading it can perturb the shared ENC/SD
  controller. It is paging-independent (a port read), but treat the read as potentially disturbing
  and cross-check against the §8d–§8f shared-bus findings.
- **Prefer the authority-diff route first** (`feedback_port_diff_authority_first`): read Colin's
  fork (`region-diffs.txt` + the SD region in `bdos15t-beta6.annotated.dis`) for what happens to
  `&780B` and the SD/ENC shared bus **between** a record-select (HRECORD) and a raw sector write
  (HWSAD). This overlaps i270a and may pin (a) vs (b) **statically**, with no paging-fragile read.

This measurement is i280b-b2j (DONE); the re-localized fix is i280b-b2i (reframed OPEN).

## 8m. Authority diff + hardware probe REFUTE the SD-bus suspect — the hang is inside the B-DOS HWSAD handler, not the SD card's state (i280b-b2l)

§8l left two live suspects for the downstream hang: **(a)** the ambient device `&780B` not
still 2 at the write (→ the FDC-poll hang), and **(b)** the real SD CMD24 write-core
busy-wait wedging because our ENC `ereset` (`serve_rearm_enc`, between the claim's
HRECORD-select and the data-phase write) left the shared one-PIC controller in a bad state
for a subsequent SD transaction. i280b-b2l settled both **without** the paging-fragile
`&780B` read §8l warned against — by an authority diff plus one decisive hardware probe.

**Authority diff (`feedback_port_diff_authority_first`).** Against Colin's 1.5t disassembly
(`~/sam-archive/bdos/analysis/bdos15t-beta6.annotated.dis`) and our serve:
- **(a) refuted.** `&780B` has exactly **one** writer in the whole binary — `&8673` inside
  the device-select `&8662`, keyed on `hk.a`=A′ (`cp 1`→floppy, `cp 2`→Trinity). Nothing
  between the claim's HRECORD-select and HWSAD touches it, and the #730 `A′=0` fix (present
  in the hanging binary) **leaves** the claim's `&780B`=2 in force. Also §8i already showed
  a data-phase *re-select* (HRECORD, which does **not** dispatch on `&780B` to an FDC poll)
  itself hangs — a wrong-`&780B` floppy-dispatch cannot explain that.
- **HWSAD ≠ HRECORD in one specific way.** HRECORD (a `rst 8` hook) **works** at claim time
  (`CLAIM_SELECT_POST` fires every shot) because record-select is pure LBA arithmetic
  (`&A0A2`: `record×1600 + base`, poked into the seek immediates) — **no SD I/O**. HWSAD's
  handler `&9E16`, by contrast, runs a **source-copy prelude** the disasm confirms byte for
  byte: `&9E5E out (&fb),a` pages the caller's source page into section C, `&9E60 ld
  (&780F),hl`, then **`&9E66 call &0005`** (a SAM-ROM bridge) copies the bytes into B-DOS's
  sector buffer, before falling through to the `&83ED` write dispatch. This copy/paging
  dance — touching a section-B address (`&780F`) and a ROM bridge from the serve's own
  paging context (HMPR=1, LMPR=`&1F` → section B = page 0, **not** B-DOS's workspace page) —
  is exactly the §8a/§8b "honest boundary" the flat harness could never trace, and is the
  HWSAD-specific step HRECORD lacks.
- **Why the claim's raw SD reads succeed is NOT evidence the bus is fine (the self-heal
  trap).** The claim's free-record finder reads list sectors via `bd_list_read_hw`
  (`src/netboot/sd_csd.asm`), which **calls `sdc_init_ladder` every time** (CMD0/8/41/58/59)
  — it re-enters SPI mode on every read, so it **self-heals** any bus disturbance. B-DOS's
  HWSAD does the opposite: it relies on the boot-time HDINIT SPI mode **persisting** (the
  per-op write core `&A81F`/`&A918` re-issues only `OUT (&DC),&31`, never the `&38`/`&04`
  init — exhaustive: the binary has 10 `OUT (&DC),imm`, all in HDINIT `&A623` or the per-op
  sender). So "claim reads work" is consistent with *either* a healthy bus *or* a bus our
  ereset breaks — it cannot discriminate (b).

**The decisive hardware probe (H2′).** To split (b) — "the ereset breaks the SD card's
persistent SPI state; HWSAD assumes persistence → hangs" — from an HWSAD-handler-internal
cause, a NETBOOT_DEBUG probe re-ran `sdc_init_ladder` + `sdc_deselect` (the known-good,
**read-only**, LBA-poking-**free** re-establish, zero record-list-clobber risk) immediately
before the `rst 8` HWSAD, bracketed by new markers `SD_REINIT_PRE`/`SD_REINIT_POST`. A TAPO
self-serve shot read:

```
… DATA_BLOCK → FLUSH_PRE → (HMPR=01,LMPR=1F) → HWSAD_PRE → (HMPR=01,LMPR=1F)
  → SD_REINIT_PRE → SD_REINIT_POST → [no HWSAD_POST]
```

`SD_REINIT_POST` **fired** — the full SD init ladder runs clean post-`ereset` (the bus is
demonstrably **not** wedged; suspect (b) as "the ereset leaves the SD unusable" is
**REFUTED on hardware**). Yet with the card freshly re-established in SPI mode and idle
*one instruction before* the write, the `rst 8` HWSAD **still hangs** (`HWSAD_POST` never
fires). **So the hang is not the SD card's state at all — it is inside the B-DOS HWSAD
handler**, downstream of `HWSAD_PRE` and unaffected by re-initialising the card.

**Net (the surviving hypothesis for the i280b-b2i fix).** With addressing correct (§8l:
HMPR=1, no displacement), `&780B`/A′ controlled (§8h/#730), and the SD bus proven healthy
at the write (this shot), the only thing left is the **HWSAD prelude itself running in the
wrong DOS-call paging context**: the `&9E66 call &0005` ROM bridge + the `&780F` section-B
buffer access assume B-DOS's resident workspace is paged into section B (the state the ROM
RST8 → DOS-call entry `&37CE`/`&1D95` arms), whereas our serve fires `rst 8 / defb 149`
with section B = page 0. HRECORD survives that context (no copy, no `&780F`, no `&0005`);
HWSAD does not. The fix direction (i280b-b2i): arm the DOS-call paging context the prelude
needs before the `rst 8` (e.g. page B-DOS's workspace into section B, save/restore), or
route the write through B-DOS's own DOS-call record-write entry rather than a bare hook
`rst 8` from a foreign paging state. The probe was a measurement (reverted — it is a
behaviour-mutating re-init that would taint future observational shots; this section is its
reproducibility record: `sdc_init_ladder`+`sdc_deselect` before the `rst 8`, markers `&24`/
`&25`). This measurement is i280b-b2l (DONE).

## 8n. The ROM DOS-call path RE-PAGES on every hook — §8m's "wrong paging context" fix direction is refuted; the real open question is the `hk.hl` register bank (i280b-b2i)

§8m proposed the surviving cause was the HWSAD prelude (`&9E66 call &0005` + the `&780F`
section-B access) running in the **wrong DOS-call paging context** (the serve fires `rst 8`
with section B = page 0, not B-DOS's workspace). Tracing the **ROM RST8 → DOS dispatch path**
(annotated ROM v3.0 disassembly) **refutes that direction** and corrects two §8 assumptions:

- **`call &0005` is `HLJUMP: JP (HL)`** (ROM `&0005`), the ROM "call indirect via HL"
  trampoline — *not* a far-copy routine. In the HWSAD prelude HL = `&83ED` (the `wr.buff`
  write dispatch, `pop hl` at `&9E63`), so `&9E66 call &0005` simply **invokes the device
  dispatch / SD write core**. The prelude is: page the caller's source into section C
  (`out (&fb)` at `&9E5E`), stash the framed source ptr at `&780F`, then call the write
  dispatch once per chunk (count=1 for HWSAD). So the hang downstream of `HWSAD_PRE` is in
  that dispatch (`&83ED → &83F7 → &A8F4` SD write, or the `&8406` FDC poll), as §8a framed.

- **The ROM DOS-call entry sets up the paging context itself — our pre-`rst 8` LMPR is
  irrelevant.** `rst 8` → ROM `&0008` (`NOP / EXX / JP &37CE`) → `ERROR2 &37CE`
  (`ld hl,(CHAD); ex af,af'; pop de; ld a,(de)`=hook code; `call NZ,HLJUMP` to `RST8V`) →
  falls to **`PTDOS &380B`** when `DOSFLG` is set (DOS booted = the serve). PTDOS at `&381C`
  does **`out (250),A` with `A = DOSFLG_page − 1`** → **section A = ROM0, section B = the DOS
  page** (and `ld sp,&8000`), *then* `call &4200` (the B-DOS hook handler). So **every** DOS
  hook runs with section B = the DOS page and ROM0 at `&0000` **regardless of the caller's
  LMPR before `rst 8`** — the §8l-measured serve LMPR=`&1F` is overwritten by PTDOS before the
  handler runs. The prelude's `&780F` (section B = DOS page now) and `call &0005` (ROM0 now)
  therefore resolve correctly. **§8m's "arm the paging context" fix is unnecessary.** (HMPR /
  section C is **not** touched by PTDOS, so the serve's HMPR=1 persists — our `BD_WRITE_BUF`
  in section C stays readable, consistent with §8l's no-displacement conclusion *if* `hk.hl`
  is our buffer address.)

- **The real open question (the i280b-b2i fix gate): which register bank populates `hk.hl`
  / `hk.de`?** The dispatcher `&8319` does `exx` + `ex af,af'` **then** saves
  `hk.hl←HL, hk.de←DE, hk.bc←BC, hk.a←A` (so it reads the *post-swap* bank). The ROM path
  also swaps (`&0009 EXX`, `&37D4 EX AF,AF'`), and PTDOS clobbers A (the hook code) — so the
  net bank that reaches `hk.hl` depends on the full `&0009 EXX` + `&37D4 EX AF,AF'` + PTDOS +
  `&4200 → &8319` swap chain, which is **too tangled to settle by static reading** (the
  relocated `&4200`/RST8V entry is the §8b "honest boundary"). **§8k/§8l simply ASSUMED
  `hk.hl` = our main `HL` = `&BE42`** (→ page 1, no-op). That assumption is **unverified**,
  and the #730 result — `hk.a` comes from `A'`, *not* main A (main-A=2 had no hardware
  effect, §8c) — is the warning sign: if `hk.hl` likewise comes from a bank our seam does not
  set, the prelude `out (&fb)` pages in a **garbage** source page → the §8k displacement
  (which §8l "refuted" only under the main-`HL` assumption) **is** the hang, and our seam's
  `ld hl,BD_WRITE_BUF` (main HL) never reaches `hk.hl`.

  **NEXT (the decisive experiment, fix-gating):** determine the `hk.hl`/`hk.de` source bank
  empirically — the existing `TestHWSADPagedPointerContract` **pre-pokes** `hk.hl`, so it does
  **not** exercise the dispatch and cannot answer this. Either (1) extend the paged-boot
  harness to run the **full** `rst 8` PTDOS dispatch (set `DOSFLG`/`RST8V`, let PTDOS page DOS
  in) with **distinguishable** main vs alternate `HL`/`DE`, then read `hk.hl`=`&81DA` /
  `hk.de`=`&81DC` — the same way §8h established `hk.a`=`&81D9` from `A'`; or (2) a
  both-banks hardware probe: set the params in **both** banks (`ld hl,BD_WRITE_BUF` *and*
  `exx; ld hl,BD_WRITE_BUF; …; exx`) and TAPO-retest — if the write then completes, the bank
  was the bug (then narrow which). If `hk.hl` ≠ `&BE42`, the fix is the #730 pattern applied
  to `hk.hl`/`hk.de`: load them in the bank `&8319` actually reads. This is i280b-b2i.

## 8o. §8n's `hk.hl` register-bank hypothesis REFUTED in emulation — `hk.hl`/`hk.de`/`hk.a` all come from the MAIN bank; addressing is correct, the hang is downstream in the SD write core (i280b-b2m)

§8n pinned the i280b-b2i fix gate as: does `hk.hl` come from our **main** `HL` (so the seam's
`ld hl,BD_WRITE_BUF` reaches it) or from a bank our seam never sets (so the prelude `out (&fb)`
pages a **garbage** source page → the hang)? It flagged that the existing
`TestHWSADPagedPointerContract` **pre-pokes** `hk.hl` and so cannot answer this. This section
runs the decisive experiment — the §8n "option (1)" — and **refutes the bank hypothesis**.

**The measurement (`TestHWSADHookBankContract`, netboot-oracle).** Boot Colin's real ROM v3.0 +
B-DOS 1.5t to editor idle, then drive a real `rst 8 / defb 149` (HWSAD) through the **full** ROM
PTDOS dispatch — the genuine `&0008 EXX → ERROR2 &37CE → PTDOS &380B → out (250) &381C → call &4200
→ dispatcher &8319` chain — with the caller's **main** and **alternate** register banks set to
disjoint sentinels (`exx; ld hl,…` for the alt bank), then read what the dispatcher saved into
`hk.a`/`hk.hl`/`hk.de`. **Result, two sentinel configs, fresh boot each:**

| sentinel | main HL | alt HL′ | → `hk.hl` | main DE | alt DE′ | → `hk.de` | main A | alt A′ | → `hk.a` |
|----------|---------|---------|-----------|---------|---------|-----------|--------|--------|----------|
| A | `&9400` | `&3333` | **`&9400`** | `&2222` | `&4444` | **`&2222`** | `&00` | `&AA` | **`&00`** |
| B | `&BE42` | `&8642` | **`&BE42`** | `&1357` | `&9753` | **`&1357`** | `&07` | `&55` | **`&07`** |

**All three save the MAIN bank.** sentinel B is decisive: main `HL = &BE42` (our actual
`BD_WRITE_BUF`) → `hk.hl = &BE42`. The swap chain is exactly **two `EXX`** (ROM `&0009` + dispatcher
`&8321`) and **two `EX AF,AF'`** (ROM `&37D4` + dispatcher `&8322`) — even counts, so `HL`/`DE`/`A`
net back to main at the `&8319` saves (`ld (&81DA),hl` etc.). The captured PC trail confirms the
path register-for-register.

**Arming the dispatch faithfully — the `DOSCNT` gate (a §8n correction).** The editor-idle snapshot
does **not** dispatch a stub's `rst 8` to the hook handler, and §8n mis-attributed the reason. The
real gate is **`DOSCNT` (`&5BC3`)**, the ROM recursion guard: `&37E8 ld a,(DOSCNT) / rrca / jr
c,NORMERR` ("DON'T RECURSE"). The snapshot has `DOSCNT=1` ("DOS in control") → the `rst 8` is routed
to `NORMERR → &1D95` and **never reaches the dispatcher**. An **external** caller invoking a DOS hook
(BASIC, or our serve) runs with **`DOSCNT=0`**; setting it so is what carries the `rst 8` through
`&37F4 jr nz,PTDOS` (`DOSFLG=&1D` is already set at boot) into `&381C out (250)` → `&4200` → the
dispatcher at its **section-B alias `&4319`** (PTDOS maps B-DOS into section B). Hardware corroborates
`DOSCNT=0` is the faithful state: `HWSAD_PRE` fires on the real serve (§8g/§8l), which only happens if
the `rst 8` reaches the handler. *(`RST8V` `&5AEE`=0 throughout — DOS does not arm it; dispatch is
purely the `DOSCNT`/`DOSFLG` fall-through, not the `RST8V` hook.)*

**What this refutes / re-confirms:**
- **§8n's bank hypothesis: REFUTED.** `hk.hl` = main `HL`. The seam's `ld hl,BD_WRITE_BUF` reaches
  `hk.hl` unchanged; the §8k/§8l assumption (`hk.hl=&BE42`) was correct, not unverified. So the
  prelude reads **our** buffer (page 1, HMPR already 1 → the `out (&fb)` is a no-op) — **§8l's
  no-displacement conclusion stands, now confirmed via the independent register-bank route.**
- **The §8h/#730 `hk.a=A'` inference: REFUTED.** `hk.a` = main `A`. The §8c null result ("force main
  `A`=2 had no hardware effect") is fully explained by the hang being **downstream** (§8j/§8n), not by
  `hk.a` reading the alternate bank. The seam's `xor a; ex af,af'` (A′=0) is harmless but does not
  touch `hk.a`; with main `A`=0 the device-select takes its "else" branch and **leaves** the claim's
  `&780B=2` (SD) in force — consistent with the write reaching the SD core.

**Net (the i280b-b2i state after §8o).** Addressing (§8l), register bank (§8o), DOS-call paging
context (§8n: PTDOS re-pages every hook), and SD-bus health at the write (§8m) are **all cleared**.
The **only surviving cause** of the data-phase hang is **downstream in B-DOS's own SD CMD24 write
core** — the `&DC` bit-3 busy-wait the koron-go SD model cannot reproduce (it always clears busy), the
§8l suspect (b). §8m's read-only `sdc_init_ladder` reinit re-enters SPI mode but the per-op write core
(`&A81F`/`&A918`) re-issues only `out (&DC),&31` and relies on the boot-time HDINIT SPI state
**persisting**; the ENC `ereset` (`serve_rearm_enc`) between the claim-select and the data-phase write
is the remaining suspect for leaving the shared one-PIC controller's SD side unrecoverable without the
full `&38`/`&04` init. Next (i280b-b2i, hardware-gated): authority-diff i270a for what the write core
assumes about persistent SD/SPI state across an `ereset`, then a marker **inside** the write-core
busy-wait + a full SD-side re-init after `serve_rearm_enc`, then a TAPO retest. This measurement is
i280b-b2m (DONE).

## 8p. The §8b "honest boundary" is SOLVED — the HWSAD handler now runs END-TO-END in emulation (the §8o `DOSCNT=0` arming is the key) (i280b-b2n)

Since §8a, the HWSAD handler was held to be **un-traceable in emulation**: the prelude
`call`s a SAM-ROM bridge (the flat harness "escapes the run window at real `&9BF1 → call
&0103`" and runs off into unmapped memory). Every downstream question therefore deflected to
a hardware shot. §8o's arming dissolves that barrier — the **full real ROM dispatch** now
carries an external caller's `rst 8` into the handler, and the **real-boot harness has the
real ROM** at `&0000`/`&C000`, so the `&0103` bridge (and `&0005`, `&0033`) **execute and
return** instead of wandering.

**The unlock (`TestHWSADHandlerTraceable`).** Boot Colin's real ROM v3.0 + B-DOS 1.5t, set the
faithful post-claim device vars (`&780B`=2 Trinity SD, `&8135`=`&44` class, `&8132`=2 number)
in B-DOS's page, arm the serve map (LMPR=`&1F`, HMPR=1) **and `DOSCNT &5BC3`=0** (§8o), then
`rst 8 / defb 149`. The handler is observed running through, in order: the dispatcher (`&8319`
alias `&4319`) → **handler entry `&9E16` (alias `&5E16`)** → **prelude `&9E27`** → device-select
(`&8662` alias `&4662`) → and across the **`&0103` ROM bridge reached from the real `&9BF1`
escape point** — then it unwinds and **returns to the editor idle loop** (`finalPC=&01BF`), with
**no fault and no wander into unmapped memory**. So the §8a/§8b "honest boundary" is gone: the
handler is now directly observable.

**Two corrections to the earlier framing this surfaced:**
- The reason a raw `rst 8` from the editor-idle snapshot does **not** dispatch (bdostrace-paged
  experiment 1) was mis-attributed to "the DOS-call stack chain is not armed." The real gate is the
  ROM recursion guard at **`&37E8`** (`ld a,(DOSCNT &5BC3); rrca; jr c,NORMERR`): the snapshot has
  `DOSCNT=1` ("DOS in control") → it diverts to `&1D95` and returns. `DOSCNT=0` (the external-caller
  state) carries it through. (Experiment 1's finding text updated.)
- The device-select `&8662` is keyed on `hk.a`: `cp 1`→floppy (`&8673`), `cp 2`→Trinity SD-setup
  (`call &4677`/`&60E4`), **else** (incl. `hk.a`=0) → `&8680`, which is **not** "floppy-port
  setup" but the **FDC-vs-SD dispatch read** itself (`&8684 ld a,(&780B); dec a; ret nz` → `&780B`=2
  routes SD). So `hk.a`=0 leaves `&780B`=2 in force, as §8m's authority diff said.

**SCOPE (honest — what this does and does NOT do).** This guards end-to-end *traceability*, not
hang reproduction. With the hand-set device state here the handler returns cleanly **without
reaching the SD CMD24 write core (`&A8F4`)** — driving it that far needs the **fully faithful
claim-select state** the real serve's HRECORD leaves (the `&80AF` flag `&4677` tests, and whatever
`&60E4` sets up), which is a deeper state reconstruction. And even reaching the write core, the
koron-go SD model **always clears busy**, so the suspected busy-wait hang (§8l suspect b) still
cannot reproduce there without modelling that timing. So §8p is a **methodology unlock** (future
i280b-b2i steps are now emulation-observable up to the write core) — not the fix. **NEXT
(i280b-b2i continuation):** reconstruct the faithful claim-select state to drive the handler into
`&A8F4` in emulation and read exactly how far the write core gets; in parallel, model the `&DC`
bit-3 busy semantics in the SD model so the busy-wait can be exercised. This unlock is i280b-b2n.

## 8q. Capture-and-diff the ground-truth write (Pete's plan) — harness built, blocked on a valid Trinity card format; option-1 (raw HWSAD vs orchestrated write) is the leading hypothesis (i280b-b2q)

Pete's steer (2026-06-29): stop theorising — drive a *working* B-DOS record write in
emulation (`RECORD n : SAVE …`), trap every IN/OUT + B-DOS hook, and diff our serve's
write against it; the gap is the bug. This section records the capture rig built for that
and where it's blocked.

**The capture rig works** (`bdos_save_capture_wip_test.go`): boot Colin's real ROM v3.0 +
B-DOS 1.5t, **type real BASIC commands** at the prompt via `InjectKeys` + `FrameIntPeriod`
(the editor tokenises + executes them), with a `&DC-&DF` port logger that **decodes the SD
command frames** (CMD + 32-bit address). It cleanly captures the live SD I/O of any command.

**Empirical findings:**
- `RECORD n` runs the **full SD init ladder every time** (CMD0/8/ACMD41/59/9/16) before its
  access — B-DOS **self-heals the SD bus on every record op** (this is why the claim/read path
  works regardless of bus state; §8m). Our per-op write core does **not** (it relies on
  boot-time SPI persistence) — a real asymmetry.
- `RECORD n` then issues **one `CMD17` read of block 152** — and **the same block 152 for
  `RECORD 1` *and* `RECORD 300`**. So block 152 (= `base` = `⌊(⌊total/1600⌋+32)/32⌋+1`) is a
  **fixed card directory / record-list sector** B-DOS reads to resolve *any* record number, not
  record-n's private data. On a blank card it is empty → "Invalid record" → the select fails, so
  `SAVE` never reaches a write (zero CMD24s).
- The device-select `&8662` disasm (read this shot): `cp 1`→floppy `&8673`; `cp 2`→Trinity
  SD-setup (`call &4677`/`&60E4`); **else** (incl. `hk.a`=0) → `&8680` = the **FDC-vs-SD dispatch
  read** (`&8684 ld a,(&780B); dec a; ret nz`), *not* "floppy-port setup". So `hk.a`=0 leaves
  `&780B`=2 (SD), as §8m's authority diff said.

**The blocker (and why it's not a dead end).** A working `SAVE`/`FORMAT RECORD` needs a card
that is **Trinity-formatted at the card level** — the supplied BASIC formatter builds the
record-list/master structure (Trinity manual `IMG_20260617_162816/823`: `RECORD n` = select
record n as DRIVE 2; `RECORD 0` = floppy; records are 800 KB, formatted before use). A bare
`BDOS`-stamp at byte 232 (the i62 minimal recipe) is enough for the **`HRECORD` *hook*** but
**not** for the BASIC `RECORD` command's card-directory read, and `FORMAT RECORD 1` alone
writes nothing (it assumes the card structure exists). **The format is fully derivable from the
docs we hold** — `bdos15a.src.txt` `hd.init` (`last.record`/`last.recs` at `:1778`) + the
`FORMAT` command (`bdos14e.src.txt:2565`) specify the record-list + base layout; no hardware or
card dump is required (Pete: "you have all the information I have").

**Leading hypothesis (option 1), not yet confirmed by capture.** Both the code-level recon and
the i62 experiment show a working record write goes **`HRECORD`-select → `HSAVE`** (hook 132 →
`open.file`/`HSVBK`/`HCFSM` orchestration, byte-identical to the floppy call sites that work),
whereas our serve does **`HRECORD`-select → raw `HWSAD`** (hook 149, the "bookkeeping-free WRITE
AT" primitive that *deliberately skips directory/allocation/setup*). The capture will pin the
exact setup/sequence our raw-HWSAD path omits.

**The authoritative card format (from samdisk `~/git/samdisk/src/SAMCoupe.cpp` —
`GetBDOSCaps`/`IsBDOSDisk`/format; same B-DOS record format as Trinity, only the storage
backend differs):**
- `list_sectors = bdos_sectors / ((512/16)·1600) + 1 = bdos_sectors/51200 + 1`;
  `base_sectors = 1 + list_sectors` (+1 boot sector); `records = (bdos_sectors − base)/1600`.
  For our `csdV2(0x001D59)` card (7 694 336 sectors): `list_sectors=151`, **`base_sectors=152`**,
  records≈4806 — **exactly the observed CMD17 152**.
- record *n* data at `base_sectors + 1600·(n−1)`.
- **detection / selection gate:** `"BDOS"` at **byte 232** of the sector at `base_sectors`
  (record 1's first MGT directory sector); `"DBSO"` if byteswapped (Atom IDE only — Trinity SD is
  not byteswapped). The record-list (labels, 16 B/entry, 32/sector) is at sectors `1..base−1`.
- a FORMAT zero-fills the boot+list area (sectors `0..base−1`) and writes each record's first
  sector with `"BDOS"`@232 (`cmd_format.cpp`). samdisk's `WriteRecord` (.mgt → record) is
  **`throw "not implemented"`** — so samdisk can't build the image for us; we build it in Go from
  this spec. (samdisk is not even needed at runtime — reading the spec was enough.)

**Verified:** `SDCard.SeedSector(152, …)` IS served (`CapturedSector(152)` returns the `"BDOS"`
stamp). So the seed mechanism is correct. **But the BASIC `RECORD n` command still rejects a
bare-stamped record** — it wants the card-level record-list / a named record, more than the
selection gate. The **`HRECORD` *hook* (156)**, by contrast, selects with **just the stamp**
(the i62 finding) — and the hook is exactly what our serve uses (`HRECORD` then `HWSAD`).

**The real blocker, precisely diagnosed (Pete's "load B-DOS into memory" hint): B-DOS has not
MOUNTED the seeded card — `last.record` is 0.** Driving the `HRECORD`(156) hook via the
§8o-armed dispatch (`A=0`, `HL=1`) reaches the HRECORD handler (`&9FAB`) but **issues no SD
read and does not select** (`&780B` stays 0) — it returns early, because `sel.record`
range-checks the record number against the in-memory **record count, which is 0**. Confirmed:
`last.record` reads 0 after boot. So the boot path that reaches editor idle (patched ROM →
trinload → B-DOS) **never ran B-DOS's card-mount (HDINIT)** for the SD records — B-DOS is
resident but the SD card is not mounted as a record device. Two fixes attempted and **failed**:
(1) driving HDINIT as a bare hook `rst 8/defb 135` — no-op (reached the dispatcher but issued no
SD I/O, `last.record` still 0; so 135 is not a callable bare-hook mount, or needs setup); (2)
poking the inferred 1.5a sysvars `last.record &80C4` / `record.no &80C6` / `record.t &80C9`
high — the poke held (re-read = 1000) but HRECORD's behaviour did **not** change, so either
those are the wrong **1.5t** addresses or `sel.record` reads the count elsewhere. Neither the
BASIC `RECORD` command nor the `HRECORD` hook will select until B-DOS has mounted the card.

**NEXT (i280b-b2q) — make B-DOS mount the card, then capture:** the clean route is to build a
**full card-level Trinity format** in the SD model (boot sector at 0 per samdisk's
`UpdateBDOSBootSector` DVAR-0 layout — geometry + `base_sectors` at bytes `0x104-0x107` /
`0x10e`, the record-list at sectors `1..base-1`, plus the per-record `"BDOS"`@232 stamps) **so
B-DOS's boot-time HDINIT recognises and mounts it** (`last.record` set from the card). *Then*
`RECORD n` / the `HRECORD`+`HSAVE` vs `HRECORD`+`HWSAD` hook diff runs and the capture works.
Alternatively, trace B-DOS 1.5t's real mount path (`hd.init`, `bdos15a.src.txt:1778`) /
`sel.record` (`&A0CD`) to find the exact 1.5t record sysvars and the mount trigger. The rig
(`bdos_save_capture_wip_test.go`), the §8o/§8p arming, the samdisk format spec, and the verified
seed mechanism are all in place; only B-DOS mounting the card remains.

## 8r. The mount works from the CSD; the §8q "boot doesn't mount" blocker is resolved and the gap is re-aimed at the SAVE/HSAVE write (i280b-b2r)

Empirical, in emulation (real ROM v3.0 + B-DOS 1.5t + a `csdV2(0x001D59)` SD card seeded
with a `"BDOS"@232` record-1 stamp), reading `last.record` from B-DOS's resident page
(page 29 = section C at editor idle; 1.5t var map per `ANALYSIS.md §3`: **`last.record`
=`&80C4`**, base=`&80C2`, capacity=`&80BD`, record.no=`&80C6`, hd.wp=`&80C8`). Regression
guards: `TestBDOSBootNoMountDeviceMounts` + `TestBDOSRecordSelectSelfHeals`
(`bdos_record_mount_test.go`).

- **Fact 1 — boot does NOT mount.** At editor idle `last.record`=0 (confirms §8q's
  measurement, now read from the *correct* page — the prior session's "didn't change"
  poke was on the wrong page / partial var-set).
- **Fact 2 — `DEVICE` mounts the card FROM THE CSD ALONE.** Typing `DEVICE` re-runs HDINIT
  (`&A1B1`): the full SD init ladder, a `CMD17` read of block 152 (the record directory =
  `base_sectors`), and **`last.record` = 4809** — *exactly* the CSD-derived count
  (`GetBDOSCaps(7 694 336)` ⇒ base=152, records=4809). HDINIT computes the count from the
  **CSD (`CMD9`)**, NOT from any on-card boot sector or record-list. **So the §8q plan's
  premise — "build a full card-level format so boot-time HDINIT mounts" — is unnecessary
  for the mount/count; `DEVICE` is the mount trigger and the count is CSD-derived.** (The
  boot path simply never calls HDINIT — a deliberate safe-mode stance, not a missing
  format.)
- **Fact 3 — the record SELECT reaches the card and PERSISTS.** With the full mount
  var-set poked into B-DOS's page (range-checks satisfied), `RECORD 1` runs the **faithful
  self-heal init ladder** (`CMD0..CMD16`, the §8m read/write asymmetry: every record op
  re-inits the bus) + the `CMD17` read of block 152, and the select **sticks**
  (`last.record` stays 4809). This is **identical whether block 152 holds a bare
  `"BDOS"@232` stamp or a real valid MGT directory sector** (A/B tested with
  `build/empty.mgt`'s real directory) — so the record-1 sector *content* is **not** the
  gate.
- **The real write blocker is DOWNSTREAM, at `SAVE`/`HSAVE`.** After a (poked) `RECORD 1`
  selects, `SAVE "x"CODE …` issues **no SD I/O at all**, **resets `last.record` to 0**, and
  ends with `ERRNR=&00` — i.e. it silently falls back to the default (floppy) device rather
  than writing to the record. A BASIC-level error code **`&0C` (12, a stock SAM-ROM error —
  B-DOS overrides only 81+)** appears around the `RECORD`/`SAVE` path; it is raised
  **downstream of the directory read**, not by B-DOS's `get.label` (whose failure is
  `rep81`=81). Its exact origin is a **model-fidelity gap in the SAVE/HSAVE write path**
  (candidates: the Trinity-detect `&DC` `&08/&09`→`'TR'` identity our ENC model may not
  serve; a post-init ready/status the write path validates; or the default-device routing).

**NEXT (i280b-b2q):** drive the write directly via the §8o-armed dispatch — `HRECORD`(156)
then `HSAVE`(132) (build the UIFA like `bdos_seam.asm bdos_fill_save_uifa`) — with the
mount var-set poked, and trace where the SAVE/HSAVE path diverges (no `CMD24`); then the
same with `HWSAD`(149) for the diff. The mount + select are no longer in the way; the
capture target is the SAVE-vs-HWSAD write step.

## 8s. The §8r write blocker, localized via the HWSAD hook path: device-select aborts on hk.a=0 (i280b-b2q)

Empirical, in emulation (regression guard `TestHWSADReachesWriteCore`,
`bdos_write_core_reach_test.go`). Driving the serve's own **`HWSAD`(149)** hook through
the §8o-armed real-ROM PTDOS dispatch, against a **genuine BASIC `RECORD 1`** select (the
faithful claim-select state — `last.record`=4809 persists, §8r fact 3), the write path:

- **Reaches** the hook dispatcher (`&8319`) → HWSAD handler (`&9E16`) → prelude (`&9E27`)
  → device-select (`&8662`) → the `&0103` ROM bridge — end-to-end, traceable.
- **Then DIVERGES** at device-select into the **`&8680` → `&9A8B` abort** (B-DOS's error
  reporter "rep": `&9A8B` calls `&8369`/`&82D1`, checks the error-trap SP `(&8104)`, and —
  `(&8104)`=0 here — builds + prints a BASIC error via the ROM char path `&025E`/`&00AC`/
  `&000D`, then returns to the editor idle loop). **No SD command is issued; the write core
  `&A8F4` and `CMD24` are never reached.** This is the §8r SAVE/HSAVE blocker reproduced
  through the **hook** path, not just BASIC `SAVE` — so it is not a BASIC default-device
  routing quirk; the divergence is inside the device-select gate itself.
- **Root cause = hk.a.** device-select is `cp 1 / jr z` (floppy) `/ cp 2 / jr nz &8680`
  (Trinity SD) — and **hk.a arrives as 0** (neither), so it takes the `&8680` abort. hk.a
  is read at `&9E3C ld a,(&81D9)`; `(&81D9)` is set by the dispatcher from the **alternate
  accumulator A'** (`&8321 exx` / `&8322 ex af,af'` / `&8323 ld (&81D9),a` — note the
  dispatcher reads the **whole alternate set**: `(&81DA)=HL'`, `(&81DC)=DE'`, `(&81DE)=BC'`
  too, so HWSAD's buffer/track/sector params are the ALT registers, not main). **But across
  the external `rst 8` entry the ROM path resets the alternate set before `&8319`**, so a
  caller's `A'` does NOT reach hk.a: the guard asserts `hk.a=0` for **both** a stub `A'=0`
  and `A'=2`. This reframes §8b's "force A=2" refutation: §8b set **main** A (the dispatcher
  never reads it — a proven no-op, as hardware showed); **A' is also not a usable lever
  across this dispatch path.**
- **The second gate** (for when hk.a does become 2): `&8677` checks the SD-claimed flag
  `(&80AF)`; `(&80AF)`=0 also diverts to the `&8680` abort. A genuine BASIC `RECORD 1`
  select does not leave `&80AF` set in the state this dispatch sees.
- **`&DC` bit-3 busy is already modelled** (`enc28j60.go` `StuckBusy`/`busyUntilT`/`isBusy`;
  `ctlStatus` ORs in `statusBusy`), so once the write core is reachable the busy-wait is
  immediately exercisable — the b2q "model the busy" half needs no new model work, only a
  path that reaches it.

**PROBE RESULT (the §8s open question answered; guard
`TestHWSADWriteCoreReachableWithGatesForced`).** Forcing BOTH device-select gates —
poking `hk.a`=2 (`&81D9`) and `&80AF`=1 at the HWSAD handler entry `&9E16`, before the
prelude reads them:

- **HWSAD reaches the write core.** It traverses device-select → the write core `&A8F4`
  and **issues `CMD24` to block 153** (record 1's region, base 152 + the track0/sector2
  offset), then **returns cleanly** (the stub `di;halt`, ~3.3k steps). So there is **no
  deeper model-fidelity gap past device-select**: the *only* blockers to the write are the
  two device-select gates. The b2q fix is therefore purely to make `hk.a`=2 + `&80AF`≠0
  hold faithfully when the serve invokes HWSAD — not to model any further hardware.
- **The `&DC` bit-3 busy-wait hang is reproducible.** With the same gates forced AND the
  `&DC` bit-3 BUSY flag wedged (`ENC28J60.StuckBusy`), the write path **spins forever**
  (hits the step cap, never halts) at **`&67CE` = `&A7CE`** — inside the `wait` routine
  `&A7CC` (the §9 `&DC` bit-3 busy-poll). This pins the suspected hardware hang precisely:
  it is the `&A7CC` `IN A,(&DC) / AND 8 / JR NZ` loop with no timeout, hanging when the
  PIC's BUSY bit never releases. (The default self-releasing model returns cleanly, which
  is why the hang never reproduced before — §8a.)

**NEXT (the FIX — i280b-b2i):** make the serve's HWSAD invocation satisfy the two
device-select gates (`hk.a`=2 + `&80AF`≠0) — either route HWSAD so `A'` survives to the
dispatcher (not the DOSCNT=0 external path that resets it), or explicitly claim the SD
device (the `&A0E4` setup `&8662` runs for A=2, which sets `&80AF`) before HWSAD — and/or
bound the `&A7CC` busy-wait with a timeout so a wedged PIC degrades instead of hanging.
Then diff against `HSAVE`(132). Every hypothesis still ends in a TAPO hardware retest (i271
markers).

## 8t. Reconcile §8s (emulation) with §8d–§8h (hardware): which blocker is REAL is still undecided — and the decisive shot needs a write-core marker (i280b-b2i)

The §8s emulation result and the hardware shots do **not** yet pin the same blocker, and
it matters because they point at **different fixes**. Stating the gap honestly so the next
step targets the right one (a §8s over-read would chase the device-select gate when the
real obstacle may be the busy-wait, or vice-versa):

- **What hardware shows (§8d/§8g/§8h):** `DATA_BLOCK → FLUSH_PRE → HWSAD_PRE`, then
  **silence** — no `HWSAD_POST`, curl times out. So the serve reaches the HWSAD `rst 8`
  and never returns. **Silence is consistent with BOTH** candidate hangs:
  (a) device-select aborts on `hk.a` (the §8s emulation path — `&8680→&9A8B`, which on
  hardware would divert to the editor/error and stop emitting markers = silence), OR
  (b) it reaches the write core and spins in the `&A7CC` `&DC`-bit-3 busy-wait (the §8a
  hypothesis, faithfully reproduced by `StuckBusy` in §8s) = silence. A passive marker
  shot **cannot tell them apart** — both end at `HWSAD_PRE` + silence.
- **What §8s emulation shows:** with `hk.a=0`, device-select aborts (never reaches the
  write core); with `hk.a=2` + `&80AF`≠0 **forced**, HWSAD reaches `&A8F4`/CMD24 cleanly,
  and a wedged `&DC` busy then hangs it at `&A7CC`. So in *emulation* the device-select
  gate is a real obstacle for `hk.a=0`.
- **Why this is UNDECIDED, not resolved:** §8b/§8c/§8h never cleanly achieved `hk.a=2` on
  hardware (§8c set *main* A — a no-op; §8h pinned A'=0). So we have no hardware data for
  the `hk.a=2` case, and we don't know whether the real dispatch even yields `hk.a=0` (the
  §8s `hk.a=0` came from the test's `DOSCNT=0` external-`rst 8` scaffold, whose ROM path
  resets `A'` — that may diverge from how the serve's own in-context `rst 8` dispatches).
  The emulator's device-select/`&9A8B` behavior under a synthetic stub (no real BASIC error
  context, `&8104`=0 → it error-prints rather than unwinds) may also diverge from the
  serve's real context. So **"emulation says device-select gate" is not yet a hardware
  fact.**

**A NETWORK MARKER INSIDE THE SD WRITE CORE IS UNSAFE — do NOT do it (the discovery-report
point 7 / one-PIC constraint).** A first-cut plan here was to detour-hook the write-core
entry `&A8F4` to emit a `DBG_WRITECORE` UDP marker. **That is wrong.** Per
`~/sam-archive/trinity-docs/DISCOVERY_REPORT.md` §3 point 7 (and the existing `dbg_marker`
header): the SD, ENC and EEPROM share **one** microcontroller, and `IN &DD/&DE/&DF` all
return the **same** last-clocked read-back latch. Emitting a network marker (an ENC TX via
`drv_write`) *inside* an SD transaction — CS asserted, between an SD command and its byte
read-back — clobbers that shared latch and the PIC's in-flight state, so the marker would
**cause or change the very hang it means to observe**. `dbg_marker` already forbids this
("never call while an SD chip-select is asserted"); a write-core marker breaks the rule.
The emulator already models the shared latch (`enc28j60.go`: "all three data ports alias
ONE shared read-back latch"), so this is a grounded hardware fact, not a guess.

**This re-points the investigation at the one-PIC interaction as the likely ROOT, not the
device-select gate.** The leading hypothesis (i280b-b2i) — `serve_rearm_enc`'s ENC ereset
between the claim-select and the data-phase write leaves the shared PIC's SD side wedged —
is exactly the one-PIC contention point 7 describes. `&DC` (status) carries BUSY (bit 3)
*and* ENCINT (bit 0) from the same PIC, so ENC activity (an ereset, or even a debug-marker
TX) can perturb the BUSY signal the `&A7CC` poll waits on. Corollary: the existing
`DBG_HWSAD_PRE` TX fires right before the SD write — close enough to the SD path that it,
too, may perturb the PIC; a clean fix test should keep network TX well clear of the write.

**REVISED PLAN (emulation-first, grounded; i280b-b2i):**
1. **Model the one-PIC ENC↔SD interaction faithfully** in `enc28j60.go`: an ENC ereset
   (and/or ENC TX) while/just-before an SD transaction leaves `&DC` BUSY (bit 3) asserted
   / the shared latch disturbed — so the real serve sequence (claim-select → `serve_rearm_enc`
   → HWSAD write) reproduces the `&A7CC` hang **without** the manual `StuckBusy` toggle.
   Ground every modelled effect in the discovery report + the one-PIC manual facts; do not
   invent behaviour.
2. **Implement the fix:** re-init/quiesce the SD side (the `&38/&04` ladder) after
   `serve_rearm_enc` and before the HWSAD write, and ensure no ENC TX (debug marker)
   interleaves the SD write. Verify in emulation the hang clears.
3. **Hardware confirm with a PRODUCTION (or marker-minimal) build** — markers, if any, only
   at points well clear of the SD transaction — so the test isn't confounded by the very
   TX-near-SD effect in point 7. Success = the push completes (record written, final ACK).

If a hardware *observation* of where it hangs is still wanted, use a NON-network channel
(border/screen for a human watcher, or a RAM breadcrumb a post-mortem can read) — never an
ENC TX inside the SD path. Do **not** commit a serve-code fix until the emulation reproduces
the hang via the modelled one-PIC interaction (CLAUDE.md §7 emulator-is-contract +
prime-directive understand-before-changing).

## 8u. Primary-source grounding for the hang — the one-PIC BUSY model (with exact citations)

Read from the Trinity manual OCR + Simon Owen's developer diary (cite the photo originals
in `~/sam-archive/trinity-docs/photos/` to confirm any UNVERIFIED OCR before relying on a
figure). These primary facts reframe the `&A7CC` hang:

- **`&A7CC` IS the manual's canonical `check_busy`.** The manual prints it verbatim:
  `check_busy: IN A,(&DC) / AND &08 / JR NZ,check_busy / RET`, "CALL after every OUT
  instruction." Source: `~/sam-archive/trinity-docs/text/IMG_20260617_162550.txt`. So the
  hang is this exact poll on `&DC` bit 3.
- **BUSY (`&DC` bit 3) is the WHOLE microcontroller's busy flag**, not an SD-specific one.
  "When you OUT a command or data to the Trinity, the microcontroller will take time to
  process … While busy, a bit on a Status Register will be set … Any data OUT'd while the
  microcontroller is busy will be ignored." It is set on EVERY OUT to ANY of `&DC`–`&DF`
  and cleared when the PIC finishes that one byte. Source: `IMG_20260617_162550.txt`,
  `IMG_20260617_162617.txt`. **So `check_busy` cannot tell SD-busy from ENC-busy — one PIC,
  one BUSY bit.**
- **`&DC` (status) is the ONLY port NOT routed through the microcontroller** — "can be read
  at any time", even while busy. Source: `IMG_20260617_162550.txt`. ⇒ a debug channel that
  *reads* `&DC` is always safe; an ENC-*TX* channel (`drv_write` over `&DE`) is not.
- **IN `&DD`/`&DE`/`&DF` share one read-back latch** (the DISCOVERY_REPORT §3 point 7):
  "all point to the same microcontroller port, and will return the last byte clocked in …
  from any peripheral." Interleaving OUTs to different peripherals before reading back loses
  data. Source: `~/sam-archive/trinity-docs/text/IMG_20260617_162617.txt` (summary in
  `~/sam-archive/trinity-docs/DISCOVERY_REPORT.md` §3.7).
- **ENC reset (`%00101000` OUT `&DC`) needs a 50 µs settle** ("wait 50us after this for the
  ENC28J60 to fully reset its registers"); SD init is `%00111000` (returns 0=absent/1=MMC/
  2=SD); ENC `/CS` pulse is `%00100011`. Source:
  `~/sam-archive/trinity-docs/text/IMG_20260617_162608.txt`, `IMG_20260617_162617.txt`.
- **Documented ENC TRANSMIT-HANG.** Simon Owen: "I was also stung by a documented ENC issue
  with the transmit logic getting stuck under certain conditions. A bug in my work-around
  meant I would still occasionally hang during transmits." The `ereset`/`epulse` in
  `src/netboot/encdrv.asm` is that work-around. Source:
  `~/sam-archive/trinity-docs/text/IMG_20260617_163210.txt`.

**Synthesised leading root cause (grounded, supersedes the §8s device-select-gate emphasis
as the HARDWARE story):** an ENC transmit that wedges (Simon's transmit-hang) — or any ENC
op that leaves the shared PIC mid-operation — leaves `&DC` BUSY **set**, and the next SD
op's `check_busy`/`&A7CC` then spins forever on a BUSY bit that reflects the **ENC/PIC**, not
the SD card. This unifies every prior thread: point 7 (one PIC, one BUSY), the §8s `&A7CC`
`StuckBusy` repro, the i280b-b2i hypothesis (`serve_rearm_enc`'s ereset disturbs the SD
side), and the manual's "OUT-while-busy is ignored" (a mis-timed SD command is dropped,
leaving the card mid-transaction → its read never completes). **The `DBG_HWSAD_PRE` marker
TX fires right before the SD write — it is itself a prime suspect for wedging the PIC.**

## 8v. The hang requires a PERMANENT PIC wedge — a finite ENC-reset settle is ridden out; the trigger is NOT source-pinned and needs a hardware measurement (i280b-b2i)

A primary-source read of the **B-DOS write-core disassembly** + the **encdrv `&DC` writers**
+ the **manual's busy semantics** (every claim cited below) refines §8u's "leading root
cause" and corrects the emulation-first plan's step 1. The decisive finding: **the SD write
core is self-protecting against any _finite_ busy, so a too-short ENC-reset settle cannot
hang it — only a _permanently wedged_ PIC can, and the primary sources do not pin a
deterministic trigger for that wedge in our serve sequence.**

**1. The write core check_busy's BEFORE its first OUT, and wait-then-OUTs every byte.**
The CMD24 sender `&A925` calls `&A81F` (`sd.cmd-with-address`), whose entry is
`&A81F DI / &A820 CALL &A7CC / &A823 LD A,&31 / &A825 OUT (&DC),&31`
(`~/sam-archive/bdos/analysis/bdos15t-beta6.annotated.dis:5844-5847`). So the **first** SD
action is a `check_busy` poll at `&A820`, *before* the first OUT (the `&31` SD-select). The
per-byte primitive `sd.out &A7C5` is `PUSH AF / CALL &A7CC / POP AF / OUT (&DC|&DF) / …`
(`:5787-5794`) — i.e. **wait-for-not-busy, _then_ OUT**. Consequence: the write core's
OUTs are NEVER issued while busy, so the manual's "OUT-while-busy is dropped" (§8u) cannot
desync the write core — it waits first. (The write-core entry `&A918` does one bare
`IN A,(&DC)` masked with `&04` — the WP/write bit, *not* `&08` busy — so it is a
write-protect check, not a busy gate; `:5995-6007`.)

**2. BUSY is time-based and self-clearing; reading `&DC` does NOT clear it.** Manual:
"the microcontroller marks itself as busy, then sends the byte over the SPI bus … and marks
itself as no longer busy" (`~/sam-archive/trinity-docs/text/IMG_20260617_162608.txt:52-55`);
"&DC … is the only I/O Port not linked via the microcontroller so can be read at any time"
(`IMG_20260617_162550.txt:41-42`). So `check_busy`/`&A7CC` (`IN A,(&DC) / AND 8 / RET Z / JR`,
no timeout — `dis:5791-5794`) can only _observe_ the PIC finishing; reading `&DC` has no
side effect. **Therefore a finite busy (the 50 µs ENC-reset settle, or any per-byte window)
is simply spun-out by `&A7CC` and the path proceeds** — the manual's "CALL check_busy after
every OUT" (`IMG_20260617_162550.txt:72-79`) is _designed_ to absorb exactly that transient.
The "ereset's 50 µs settle wasn't waited for" framing in §8u is thus **refuted as a hang
cause**: the write core rides it out.

**3. For `&A7CC` to spin forever the PIC's BUSY must be stuck _indefinitely_ — a true
wedge.** The only source-grounded cause of a permanent ENC/PIC wedge is the **ENC silicon
transmit-errata** Simon Owen documents: "a documented ENC issue with the transmit logic
getting stuck under certain conditions. A bug in my work-around meant I would still
occasionally hang during transmits" (`IMG_20260617_163200.txt:96-98` — note: the file is
`…163200.txt`, not the `…163210.txt` the §8u SOURCES list cites; correct that pointer). The
in-driver workaround for this is the **ECON1 TXRST set/clear** at the top of each TX attempt
(`src/netboot/encdrv.asm:231-237`), an ENC-internal errata fix — **not** the `&DC` `ereset`
(which is the power-on / link-open reset used by `drv_init`/`drv_exit`/`enc_rx_reestablish`).

**4. The encdrv `&DC` writers return while the PIC may still be busy — but this is benign.**
`eon`/`eoff`/`epulse`/`ereset` each `CALL wait_ready` _before_ their OUT and then RET with no
trailing poll (`encdrv.asm:456-479`); `ereset`'s post-reset wait is two blind `DJNZ $`
(~1 ms ≫ 50 µs), not a busy poll. So they leave the PIC mid-process on return, violating the
manual's "check_busy after every OUT" on their _last_ OUT. **But the next operation's leading
`wait_ready`/`check_busy` absorbs that finite residual** (our `wait_ready` is the bounded
i280b-b2c poll, `encdrv.asm:442-454`; B-DOS's `&A820` is the unbounded one) — so this is not
a bug that can wedge a well-formed caller. There is **no source-grounded software defect** in
the busy discipline to fix.

**What this means for the emulation-first plan (corrects §8t step 1).** "Model the one-PIC
interaction so the real serve reproduces the `&A7CC` hang _without_ manual `StuckBusy`" is
**not achievable from the primary sources**: a faithful, time-based busy model rides out every
finite window (point 2), and reproducing the hang would require a permanent wedge whose
**trigger the sources do not pin** (point 3) — modelling one would be _inventing_ behaviour,
which the prime directive and CLAUDE.md rule 7 (inadequate emulation manufactures false
confidence) forbid. The `StuckBusy` model (§8s, `TestHWSADWriteCoreReachableWithGatesForced`
case 2) is therefore **already the faithful model of the wedge as far as the sources allow** —
a wedged `&DC` bit-3 ⇒ `&A7CC` spins. The genuinely open question is purely **what triggers
the wedge in the real serve sequence**, and that is a **hardware datum**, not a modellable one.

**The decisive next step is hardware (Pete/TAPO-gated):** a **production / marker-minimal**
build push (no ENC TX anywhere near the SD write — the §8t prime suspect removed) while
**measuring `&DC` bit 3** across the per-block write via a NON-network channel (`&DC` reads
are always safe — the one port not via the PIC; or border/screen; or a RAM breadcrumb). Two
outcomes, each conclusive: (a) the push **completes** ⇒ the wedge was the debug-marker /
near-write ENC TX (errata), and the fix is "keep ENC TX clear of the SD write window" (the
markers are already debug-only and outside the SD CS, so a production build may already be
correct); (b) it **still hangs with `&DC` bit-3 stuck** ⇒ a deeper ENC/PIC contention or
TXRST-workaround bug, localizable from where bit-3 latches high. Until that datum exists the
fix cannot be chosen on grounds rather than guesswork — so i280b-b2i is gated on a tracked
hardware-measurement item (owner pete), not on further emulation. (Research: this section's
citations; the implementing-agent handover in `docs/plans/i280-bdos-write-trace.md`.)

## 8w. The §8v decisive shot RAN: a PRODUCTION (marker-free) serve ALSO hangs the WRQ write — the markers are exonerated; the wedge is real (i283)

The §8v decisive experiment was executed on real hardware (`tools/hardware-shot/run-shot.sh`
with the **production** `netboot_serve_boot.bin`, no `NETBOOT_DEBUG` markers; TAPO power-cycle
→ trinpush serve → `curl -T` a small `.mgt` WRQ to a highest-free record, 2026-06-29):

- **Result: `curl exit 28` (timeout).** The push did not complete in 35 s — the WRQ write
  **hangs with a marker-free production build**. This is §8v **outcome (b)**.
- **The §8v/§8t marker-suspect is EXONERATED.** A production build emits **no ENC TX near the
  SD write**, yet it still hangs. So `DBG_HWSAD_PRE` (and the debug markers generally) are
  **not** the cause; "keep ENC TX clear of the SD write window" is necessary hygiene but is
  **not sufficient** — the wedge happens without any debug TX. The remaining ENC activity
  before the per-block write in the WRQ path is `serve_rearm_enc`'s ereset + the handshake/ACK
  TX + the DATA-block RX; the wedge originates there, inside B-DOS's HWSAD core (consistent
  with the prior §8d debug trail `DATA_BLOCK→FLUSH_PRE→HWSAD_PRE→silence`).

**The MCU is now identified — Microchip PIC16F74** (read off the chip; `trinity-capabilities.md`),
which sharpens the wedge mechanism from "abstract ENC stall" to a concrete datasheet path: the
PIC16F74's **SSP** (its SPI master to ENC/SD/EEPROM) raises **SSPOV** (overflow) / **WCOL**
(write-collision) flags that "must be cleared in software" (DS30325B §9). If Colin's firmware's
per-byte wait-loop polls **BF** and does not handle an SSPOV/WCOL raised by a stalled ENC SPI op
(Owen's transmit-hang), it spins and **never clears `&DC` BUSY** → the SAM's `&A7CC` poll hangs.
The `&DC` BUSY flag is this PIC's firmware construct, so the firmware would settle it — but the
PIC16F74 firmware is **almost certainly code-protected** (read-back = all-zeros; un-protect =
chip-erase = wipe), so it can't be dumped. Net: the §8v "needs the PIC firmware **or** a
hardware measurement" is now "needs **Colin** (`q61`) **or** SSP-level hardware observation."

**NEXT (agent-actionable — hardware self-serve, no Pete gate):**
1. **Block-localize:** a DEBUG shot counting `DBG_DATA_BLOCK`/`DBG_HWSAD_PRE` markers — does it
   hang on block **1** (wedge from the claim/rearm/handshake setup) or a later block (wedge
   accumulates per ENC↔SD alternation)? Markers fire OUTSIDE the SD CS so this is safe (the §8d
   trail already used them); it adds the block index.
2. **`&DC` black-box:** instrument `bdos_write_sector` to read `&DC` bit-3 (the one safe port)
   immediately before the `rst 8` and stash it in a RAM breadcrumb / report it via a marker at
   that safe point — is BUSY already stuck *before* the B-DOS write, or only inside it?
3. **`q61` (Colin):** the definitive answer to the SSP/BUSY semantics; Pete's contact.
A logic-analyzer on the PIC↔peripheral SPI lines (Pete-side) would directly show the SSP stall.

## 8x. The MCU is a PIC16F74 + the ENC transmit-hang is a DOCUMENTED erratum + an SPI-bus probing guide (i283)

The microcontroller is identified — **Microchip PIC16F74** (40-pin, socketed; full board inventory + photos in `trinity-capabilities.md`). Two consequences for this investigation:

**(1) The §8w wedge is now grounded in a real, documented erratum — not speculation.** The ENC28J60 has a transmit-hang in **Rev. B7 Silicon Errata issue 12** (DS80349, https://ww1.microchip.com/downloads/en/DeviceDoc/80349b.pdf): after a transmit, **`TXRTS` is not cleared by the transmit logic**, so firmware that polls `TXRTS`-to-clear **spins forever**; the sanctioned fix is to wait on `TXIF`/`TXERIF` and reset via `ECON1.TXRST`. On the Trinity the PIC16F74 is the **single SPI master** shared by ENC+SD+EEPROM, so a firmware spin in the ENC transmit-completion poll blocks the *one* master → a queued SD op **never starts** → the SAM sees "SD write hangs" (the §8a/§8s/§8w symptom). The PIC firmware is almost certainly code-protected (can't dump — `trinity-capabilities.md`), so this is confirmed by **bus observation**, not a firmware read.

**(2) Oscilloscope / logic-analyzer probing guide (Pete has a scope).** SCK/MOSI/MISO are one shared 3-wire bus to all three peripherals; per-peripheral CS lines (driven from `&DC`) demultiplex. Ground at PIC pin 12 or 31.

| Ch | Signal | Probe point | Datasheet pin |
|----|--------|-------------|---------------|
| 0 (clk) | SCK | PIC pin **18** (RC3) or SD contact 5 | DS30325B |
| 1 | MOSI | PIC pin **24** (RC5/SDO) or SD contact 2 | DS30325B |
| 2 | MISO | PIC pin **23** (RC4/SDI) or SD contact 7 | DS30325B |
| 3 (trig) | SD-CS | SD slot contact **1** (DAT3) | SD SPI std |
| (4) | ENC-CS | ENC28J60 pin **9** | DS39662E |

Trigger SD-CS falling, single-shot. Signatures: **(a) SCK dead** = PIC stalled in firmware (the ENC-erratum signature — spinning on `TXRTS`); **(b) a CS stuck low** = died mid-transaction on that peripheral (ENC-CS stuck ⇒ the SD op can't start); **(c) SD CLK alive + MISO stuck high** = SD command dropped / card not responding (note: MISO held *low* during a write is legitimate busy-programming, not a hang); **(d)** decode the last bytes (sigrok/PulseView SPI decoder, mode 0) — ENC opcodes (WBM `0x7A`, bit-ops) before SCK dies ⇒ ENC-path wedge; an SD `CMD24` (`0x58`) with no card reply ⇒ SD side. Tool: a scope answers (a)/(b)/(c); a ~£10 FX2 USB analyzer + sigrok gives the decoded bytes (d). Sample ≥24 MHz (SPI ≤5 MHz = 20 MHz crystal /4).

## 8y. Pete's BASIC-SAVE-trace approach — status: the rig works + RECORD traces, but the emulated BASIC SAVE errors &0C before writing (no gold trace yet); the decisive unblock is a hardware BASIC SAVE

Pete's plan (capture the full IN/OUT trace of a minimal BASIC `SAVE`-to-record so the exact SD command + hook sequence is known) is implemented as **i280b-b2q**'s rig `bdos_save_capture_wip_test.go`. Status:
- ✅ It captures every Trinity-port IN/OUT and traces **`RECORD n`** cleanly: self-heal init ladder + `CMD17` block-152 (directory) read; the select persists (§8r). The select sequence is captured.
- ❌ The BASIC **`SAVE`-to-record fails in emulation**: no SD I/O, `last.record` reset, floppy fallback, stock **error &0C (12)** raised downstream of the directory read (§8r). So there is **no working-save (`CMD24`) trace via BASIC** — the gold sequence Pete wants does not yet exist, because the emulated SAVE never writes. (The only emulation path that reaches `CMD24` is driving the HWSAD hook directly with the device-select gates forced — §8s — not the BASIC orchestration.)
- **Suspected cause:** an emulator fidelity gap (Trinity-detect `&DC` `&08/&09`→'TR', a post-init ready/status, or default-device routing) makes the emulated SAVE reject the selected record before writing.

**Decisive unblock (two routes, the 2nd ties to §8x):**
1. **Emulation:** find why HSAVE raises &0C (trace the raise point in B-DOS's SAVE path) and make the emulated BASIC SAVE-to-record *succeed*, then capture the gold IN/OUT trace + diff our HWSAD path against it.
2. **Hardware (decisive):** run the minimal BASIC `SAVE`-to-record on the **real** SAM+Trinity and **scope-trace** the SPI bus (§8x). This answers the open question *does BASIC SAVE-to-record even work on hardware?* — **yes** ⇒ the gold SPI sequence is captured directly *and* the hang is specific to our serve's HWSAD invocation (copy what BASIC does); **no/hangs** ⇒ B-DOS record-save is broken board-wide (a bigger finding). Either outcome is conclusive, and it sidesteps the emulator &0C fidelity gap entirely.

## 8z. The emulated BASIC SAVE &0C is ROOT-CAUSED: SAVE never dispatches to B-DOS (DOSFLG=0, the rig loses the system-var page) — NOT an SD-port stub (i280b-b2s)

> **REFUTED by §8aa.** The "rig loses page 31 / SAVE never dispatches" conclusion below is wrong: it was a reboot-loop artifact of resuming at the broken `&01CB` point with a reset stack (which cold-reset the machine → the RAM test zeroed DOSFLG). With a faithful idle/resume (WTKY2 + `Continue`), DOSFLG stays `&1D` and BASIC SAVE *does* dispatch HSAVE. Read §8aa; the analysis below is kept for the record only.

Followed Pete's method (the trace is the diagnostic) on the `bdos_save_capture_wip_test.go` rig. The exact divergence:
- BASIC `SAVE`'s `RST 8` reaches the SAM ROM dispatcher `ERROR2 &37CE`; at **`&37EF LD A,(DOSFLG)` it reads `DOSFLG (&5BC2) = &00`**, so `&37F4 JR NZ,&380B` (→ PTDOS → B-DOS) is **not taken** — the ROM raises a stock error and **SAVE never reaches B-DOS** (no HSAVE `&9D54`, no device-select, no SD I/O). Same run, `RECORD 1` reads `DOSFLG=&1D` → dispatches correctly → full SD init ladder + `CMD17@152`, mounts `last.record=4809`.
- **Why DOSFLG flips &1D→&00:** `DOSFLG &5BC2` is the standard SAM "DOS resident" sysvar (B-DOS sets it once at boot `&807A`, never clears it). Per-page dumps: after `RECORD 1`, physical **page 31 holds `&5BC2`=&1D**; after `SAVE`, page 31 no longer holds &1D — **the rig's SAVE command loses/clobbers the system-variable page (page 31)** before its own `RST 8` reads DOSFLG.
- **It is NOT an SD-port stub.** The §8r suspects are all faithful already: the ENC `&08/&09→'TR'` identity probe, `&DC` bit1 present / bit2 WP / bit3 busy (`sdcard.go ctlStatus`), and the init-ladder+`CMD17` (RECORD reaches them). SAVE never gets near an SD port read. **The gap is harness paging fidelity** — the rig's `SAVE "x"CODE 32768,20` staging at `&8000` (section C, page-mapped) interacts with the ROM SAVE's repaging and drops page 31.
- **What B-DOS would do (grounded, disasm-cited):** `RECORD`'s handler `&9FC6` sets device# `&8132:=2` (via `rcd0 &9FF5`) and pokes the BASIC current-device sysvar `&5A07` so a plain SAVE defaults to D2 — **no port gating** (only RAM checks: `&80AF` avail + `last.record` range). All Trinity/SD port gating lives in HDINIT `&A1B1` at boot/`DEVICE`-time. So a working SAVE-to-record is RAM-state-driven; nothing was missing in the SD model.
- **No fix applied** (prime directive): couldn't yet determine if the page-31 loss is the rig staging SAVE wrongly (wrong LMPR/staging) vs a koron-go pager bug. The single load-bearing condition: **SAVE's `RST 8` must see `DOSFLG=&1D`**. **NEXT:** instrument the `OUT (&FA)/(&FB)` during the rig's SAVE to find what drops &1D from page 31; fix the rig's staging/paging (mirror real BASIC: stage CODE where SAVE doesn't repage over the sysvar page) so SAVE dispatches → capture the gold `HRECORD`+`HSAVE` trace → diff vs our `HWSAD`. **Alternative route:** drive `HSAVE`(132) directly via the §8o dispatch after a genuine RECORD select (the device state set up), sidestepping the BASIC-command paging entirely. Authority: SAM ROM v3.0 disasm (`ERROR2 &37CE`/`DOSFLG &5BC2`), `~/sam-archive/bdos/analysis/bdos15t-beta6.annotated.dis` (RECORD `&9FC6`/HSAVE `&9D54`).

## 8aa. §8z is REFUTED — the rig never ran any command; the DOSFLG loss was a reboot artifact of a broken resume point. A faithful rig (WTKY2 + Continue) shows BASIC SAVE dispatches HSAVE cleanly (i280b-b2s)

Followed §8z's "instrument the SAVE paging" plan (ROUTE A). The instrumentation overturned §8z's own root cause:

- **The DOSFLG `&1D→&00` write is the power-on RAM test, not SAVE's repaging.** Tracing the physical DOSFLG byte, it is zeroed by `LDIR` at **ROM1 `&EBC9`** — `MNINIT`/`RMPS` (SAM ROM v3.0 disasm `&EBAE`, `;CLEAR A PAGE`), the **cold-boot memory scan** that walks HMPR through all 32 pages clearing section C. Section C (HMPR=&00 → physical page 0) and the system-var page (secB at boot LMPR=&1F → physical page 0) **are the same physical page in this map**, so the RAM-test's section-C clear wipes DOSFLG. But that routine only runs at reset.
- **Why it ran: the rig RESET the machine on every "command".** The ring-buffer of PCs showed the resume going straight from `addrEditorIdle` to `&0000`: **`&01CB` is the ROM's `HLJPI` `JP (HL)` trampoline (disasm `&01C7`), NOT an editor idle loop**, and `RunBootFrom` resets `SP` to a synthetic stack each call. So "resuming a command" at `&01CB` with a clobbered stack jumped to `&0000` = cold reset → RAM test (zeroes DOSFLG) → B-DOS re-boot. **Every §8y/§8z RECORD/SAVE/DOSFLG/CMD17/CMD24 observation was that reboot loop, not command execution** (the §8z-era WIP rig even reported a phantom `CMD24 @PC=&01CB`). There is no page-31 paging-fidelity bug.
- **The fix is a faithful idle/resume.** The genuine editor idle is **WTKY2 `&04FA`** (the key-wait spin `CALL INPUTAD / RET C / JR Z,WTKY2`; disasm `&04F0`), combined with a new harness primitive **`Continue`** (`harness.go`) that resumes the SAME CPU in place — PC, SP, all registers, IFF preserved — instead of re-entering with a reset stack. With it, InjectKeys flows through the real editor → TOKMAIN → LINESCAN → the command interpreter `LINERUN`, exactly as a user typing.
- **Result (regression guard `TestBASICSaveDispatchesToHSAVE`, `bdos_basic_save_dispatch_test.go`):** DOSFLG stays `&1D` throughout (no reboot); `PRINT 1` executes end-to-end (reaches `LINERUN`); and **BASIC `SAVE "x"CODE addr,len` dispatches faithfully**: tokenise → `LINERUN` → the DOS dispatcher passes hook **code 132 (HSAVE)** to PTDOS → reaches the **B-DOS HSAVE handler `&9D54`** → device-select. So the SAVE *write dispatch* works; §8z's "SAVE never dispatches" is wrong.
- **The real remaining blocker (the gold-CMD24 gate) — faithful Trinity-record selection:**
  - With no record selected, HSAVE's device-select routes to the **floppy** path and raises **error 55 "Missing disc"** (`BOOT2 &D90E`, polling the FDC index hole `IN A,(&E0)`) — because the current device is not D2.
  - Selecting a record needs the BASIC keywords **`RECORD`/`DEVICE`**, which are **NOT base-ROM keywords**: they are B-DOS's **added-command dispatch-table extensions at `&8277`** (ANALYSIS.md §3; 16 tokens incl. RECORD &EF) and must be recognised by B-DOS's tokeniser/interpreter hook. In this boot snapshot they tokenise as letters → LINESCAN raises **error 29 "Nonsense in BASIC"** (`NONSENSE &0D29`), so they never execute. Whether B-DOS's BASIC keyword hook is meant to be installed at this editor-idle state (vs the autoboot path skipping full BASIC integration) is the next open question.
  - Hand-poking the device vars HRECORD sets (`&5A07`=2 current-device, `&8132`=2, `&8135`=&44, `&80AF`, `&780B`=2, the mount var-set; mirroring `bdos15a.src.txt hrcd2/rcd.da DEFW &5A07`) moves SAVE off the floppy path but it then raises **error 20 "Invalid device"** (`&3F13`; B-DOS `DEFB 20` `;invalid device`) — i.e. the hand-poked descriptor is not a self-consistent selected-record state. **Not landed** (prime directive: forcing a path by inventing state is exactly what to avoid); the faithful select must come from B-DOS itself.
- **Net:** ROUTE A's dispatch half is solved and faithful; the gold `CMD24` trace is gated specifically on **faithful record selection** (B-DOS BASIC-keyword recognition, or driving `HRECORD` via a dispatch that preserves the alt-set — the §8s/§8h hk.a contract). Authority: SAM ROM v3.0 disasm (`&EBAE` RAM test, `&01C7` HLJPI, `&04F0` WAITKEY/WTKY2, `&0D29` NONSENSE, `&D90E` Missing-disc, `&3F13` Invalid-device); ANALYSIS.md §3 (`&8277` added-command table); `bdos15a.src.txt` (HRECORD `rcd.da`→`&5A07`).

## 8ac. Pete's hardware constraint REFRAMES the hang: it is SD-WRITE-specific AND invocation-specific — NOT a general PIC wedge; replicate B-DOS's own write path (i280b-b2s)

Pete (2026-06-29) states the hardware-verified facts: on the real SAM+Trinity, **TFTP/network writes work, SD reads (CMD17) work, EEPROM reads AND writes work, and B-DOS's OWN SD writes work** (BASIC `SAVE` to a record, copying a file record→record). **Only OUR machine-code SD-write invocation hangs.** Consequences:

- **The §8x "ENC-erratum wedges the shared PIC" theory is REFUTED as the cause of this hang.** A wedged shared PIC (one SPI master for SD+ENC+EEPROM) would break SD reads, EEPROM r/w, and network too — but all of those work on hardware. So whatever hangs is **not** a general PIC/BUSY wedge. (The ENC errata may exist, but it is not what hangs our SD write; the §8w/§8s `StuckBusy` repro was a synthetic fault, not the real mechanism — SD reads use the same `&A7CC` poll and work.)
- **The hang is specific to (a) SD WRITES and (b) OUR invocation.** B-DOS's own write path (BASIC→`HSAVE`) writes to SD fine on hardware; our serve's raw `HWSAD`(149)-via-`rst 8` stub hangs. So the defect is in **how we invoke the write**, not the SD card, the bus, or the write core itself — exactly Pete's framing ("works from BASIC calling BDOS hooks, fails from machine code").
- **The convergent fix (Pete's idea):** trace the exact ROM+B-DOS routine/hook sequence a *working* BASIC record-write drives (`HSAVE`(132) via the ROM PTDOS dispatch → B-DOS device-select → the SD write-core), then **replicate that sequence** in our serve — call the same routines / establish the same device + `hk.a`/`&780B`/`&80AF` state — instead of the raw `HWSAD`(149)-direct that §8s/§8h showed cannot set `hk.a` across the external dispatch. This is now an **emulation** task (no hardware needed): §8aa's faithful rig (`Continue`/WTKY2) makes BASIC SAVE dispatch to `HSAVE` in the emulator; the remaining blocker is selecting a record in the rig (RECORD/DEVICE are B-DOS BASIC-keyword extensions not recognised at the boot-snapshot idle state — install B-DOS's keyword hook, or drive the record-select via the hook path with the device state B-DOS's `record:`/`hrcd2` routine sets). Then capture the working `HSAVE` IN/OUT+hook trace and port its setup into `bdos_seam.asm`.

## 8ab. Deferred idea (Pete, 2026-06-29) — on-hardware IN/OUT ring-buffer tracer for a hardware-vs-emulation diff

Patch B-DOS so each Trinity-port IN/OUT logs to a RAM ring buffer (record the op, then perform the real IN/OUT, then return), run the *same* save the rig runs, and TFTP the ring buffer back to the Pi at the end. This is an embedded software logic-analyzer: it yields the **real-hardware IN/OUT sequence** to diff directly against the emulation trace, finding remaining emulation gaps without a scope. Logging *before* each op means the buffer's last entry pinpoints the exact wedge instruction even on a hang.

Design wrinkles to resolve before building: **(1) size mismatch** — `IN/OUT` are 2 bytes (`DB nn`/`D3 nn`/`ED xx`), `CALL` is 3, so you can't splice in place; patch the *few* B-DOS Trinity primitives instead (`sd.out &A7C5`, `sd.in &A7F0`, `check_busy &A7CC`, the `&DC` selects) — wrap the routine, far fewer sites — or use a 1-byte `RST`→trampoline (free RST vectors are scarce; `RST 8` is the hook dispatcher). **(2) readback off a wedged SAM** — the TFTP-back uses the ENC, which shares the wedged PIC, so the hang case may be unreadable (the UDP-marker constraint); it is clean for a **working** save (no wedge). Sweet spot: capture the hardware gold trace of a *working* record save → diff vs the emulator. Status: **deferred** — evaluate after the i280b-b2s emulator-capture thread; track as an item if still valuable then.

## 9. Porting to fresh Z80

The fork's primitives map onto existing sam-aarch64 SPI code (the SD port reuses the same busy-poll / one-byte-lag / auto-null shapes the ENC and EEPROM drivers already implement). **Mirror these:**

| Trinity SD primitive (this doc) | sam-aarch64 analogue to mirror |
|---------------------------------|-------------------------------|
| `wait` &A7CC (busy-poll `&DC` bit 3) | `wait_ready` in `src/netboot/encdrv.asm` (same `IN A,(&DC) / AND %00001000 / JR NZ` — identical, ENC and SD share `&DC`) |
| `&31`/`&30` SD select/deselect | `eon`/`eoff` enable/disable in `src/netboot/encdrv.asm`, swapping the ENC `&21`/`&20` for SD `&31`/`&30` |
| `&3F` auto-null bulk read + `INI` | the ENC `&2F` auto-null bulk path (`rd_buf_mem`/`rd_buf_lp` loop) in `src/netboot/encdrv.asm` |
| `sd.out`/`sd.in` per-byte OUT→wait→dummy→IN | the per-byte `OUT (C),x / CALL wait_ready` pattern in `src/netboot/eeprom.asm` (`read_chunk`) |
| `sd.cmd-with-address` / CMD9 / CSD decode | written fresh on top of the above; mirror §4–§6 exactly |

The standard SD init + CSD-decode logic, and the per-brick task breakdown (i145a probe → i145b production read → i145c harness model → i145d E2E), live in **`docs/plans/i145-sd-csd-read.md`** — that plan cites *this* doc as its authority for the protocol details; **do not duplicate its brick list here.** The non-disruptive-`CMD9` caveat for the production path (the card is already initialised by B-DOS at boot, so prefer a bare CMD9 over a full re-init) is in that plan.

---

## Surprises / divergences from the "standard ladder" assumption

The i145 plan assumed a textbook SD-SPI ladder. The fork mostly matches it, with these wrinkles worth carrying into the port:

1. **A microcontroller pre-step (`&38`) precedes the standard ladder** — the Z80 does not drive CS/clock directly to wake the card; it issues the `&38` "SD init" command (returning MMC=1/SD=2), then polls `sd.in` B times until it returns **`&FF`** (the wake loop &A643 `inc a / jr z` — the *opposite* sense to a CMD0-response poll), *then* runs CMD0…CMD9. The textbook ladder has no equivalent.
2. **CMD1 MMC fallback is present** (&A6CB) — the ladder is not SD-only; it degrades to CMD1 for MMC, ≤5000 tries.
3. **CMD59 (CRC off) is sent** (&A705) before CMD9, which many minimal ladders skip.
4. **The address is self-modifying code, not a register argument** — the 32-bit sector/byte address is poked into the `LD HL,nn` immediates in `sd.cmd-with-address` (&A836/&A843). A register-passing port is fine, but the fork's shape is poke-then-call.
5. **Two SD-select modes, switched mid-command** — `&31` (manual, Z80 dummies) for command+arg, flipped to `&3F` (auto-null) immediately before the response/data phase. The auto-null mode is mandatory for the bulk loops to be pure `INI`/`OUTI`.
6. **All SD I/O runs under `DI`** (set in the command sender, cleared only in the deselect tail) — there is no interrupt/ESC window inside a transfer. The fork deleted the Atom `tst.esc` helper accordingly.
7. **The byte-vs-block `<<9` is a poked `JR` displacement** (&A18F), set once at init from the CMD58 CCS bit — not a runtime branch. Byte-addressed cards cap at 4 GB; block addressing is the >4 GB / 64 GB enabler.
