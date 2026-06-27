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
