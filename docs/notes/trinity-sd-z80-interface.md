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

## 8a. SD-write / cj.mgt-push — settled truths and the open question (i292/i293)

> The original §8a–§8aj here was a long blow-by-blow of the cj.mgt SD-write
> investigation. Much of it stated unverified inferences as fact and carried
> claims Pete ordered "killed with fire" (chiefly "B-DOS hooks are flaky →
> reimplement" and "+232 is required to boot"). **It has been removed — git
> history preserves the full blow-by-blow.** What follows is the corrected,
> settled summary. Full evidence + the post-mortem:
> `docs/plans/trinity-sd-write-saga-cleanup.md` (§A settled truths, §B the
> false-claim purge list, §D the genuinely-open questions, §F the systemic
> controls); auto-loaded memory `feedback_trinity_sd_write_settled_truths`.

**The settled truths (do NOT re-derive without NEW contradicting primary-source evidence):**

1. **B-DOS's HWSAD/HRECORD/HRSAD hooks are sound — not flaky, no reimplementation
   needed.** Colin's B-DOS 1.5t writes records and boots them on real hardware. The
   "flaky hooks → reimplement our own SD driver" chain was a false conclusion
   defended across sessions.

2. **The per-block write hang was self-inflicted: our own per-block full ENC
   `ereset`.** `wd_send_ack` called `serve_rearm_enc → enc_rx_reestablish → CALL
   ereset` (a full ENC28J60 soft-reset + reconfigure) on **every** accepted DATA
   block, disturbing the shared-microcontroller SD state so B-DOS's *next* HWSAD
   found the SD unexpectedly reset and stalled. Colin's code isn't broken — we reset
   the controller out from under it between writes. **i293 removes the per-block
   ereset** (zero re-arm: the ENC RX engine is autonomous; the i249 "serving died
   after the first SD read" death was the *un-drained* shared-latch read, fixed by
   the drain rule below, not a genuine RX-engine disturbance).

3. **The hardware interleave / drain rule (solid, primary-source grounded).** The
   three data ports `&DD`/`&DE`/`&DF` alias **one** shared-microcontroller read-byte
   latch (`&DC` is the only port not routed through the microcontroller, so it is
   safe to read at any time). Therefore **an `OUT` to a Trinity peripheral must be
   followed by its `IN` before switching devices** (ENC / SD / EEPROM) — drain each
   device before alternating. B-DOS already respects this internally (the interleave
   detector found zero violations in B-DOS's own routines). Source: the Trinity
   manual's busy semantics (`~/sam-archive/trinity-docs/text/IMG_20260617_162550.txt`,
   `IMG_20260617_162608.txt`); `DISCOVERY_REPORT.md`.

4. **+232 is NOT a boot/select requirement.** Booting a record runs the disk's own
   boot sector and never inspects byte 232; record-push validation is **size-only
   (== 819200)**. The 4-byte `"BDOS"` stamp at offset 232 of a record's first
   directory entry is only B-DOS's catalog/format signature — read by
   `bdos_inspect_record` for the overwrite-safety gate (see §7 and
   `docs/specs/trinity-record-detection-design.md`), never a boot gate. FRED disks
   (no B-DOS) and cj.mgt boot fine without it. (Encoded by i285/#750 in
   `bdos_seam.asm`.)

5. **A=2 (Trinity drive-select) is real for the SECTOR hooks but not for HRECORD.**
   The raw sector hooks HWSAD(149)/HRSAD(160) device-select on `hk.a` = the caller's
   **main A** at the `rst 8` (the "hk.a = alternate A'" finding was a reboot-artifact,
   refuted). Our serve left main A = the sector number, so the first `.mgt` sector (1)
   selected the floppy → the un-timed FDC poll = a hang; the fix (PR #748) presents
   **A=2** before the `rst 8` for HWSAD/HRSAD (hardware-confirmed, i283). **HRECORD's
   contract is different: A=0 + record# in HL** — A=2 does NOT apply to it.

**The remaining solid facts** (the port map §1, the SD ladder/CMD sender §3–§5, the
record→sector LBA formula `LBA = csd_base + 1600·(record−1) + linearSec` and the
free-record-detection list-sector geometry §6–§7, and the porting map §9) are
unchanged and live in those sections.

**GENUINELY OPEN — do NOT re-frame these as the killed claims (`cleanup-plan` §D):**

- **D1 — the in-context HRECORD `rst 8` crash root cause.** On the old (own-CMD24)
  hardware path the on-screen RST-&10 trace read `WFabcde×13+GS`: the find does ~13
  successful SD reads and finds a free record, the claim sets it, HRECORD's `rst 8`
  is entered (`S`) and does not return (no `s`). HRECORD returns CLEAN in isolation
  AND under the serve's `LMPR &1F` paging — so *why it crashes in-context* is open.
  (Do NOT confuse this with the false "crash is in the find's FIRST SD read".)

- **D3 — the `.mgt`→record sector ordering: track-major vs side-major.** Whether the
  card addresses record sectors side-major (`linear = side·800 + 10·track + sector−1`)
  vs the track-major layout of a `.mgt` image is an **open investigation**, neither
  asserted as the fix nor debunked. The shipping code is track-major. Investigate
  with samdisk **3.8.12** (not the 4.0-alpha on this host). Tracked as **i294**.

- **D4 — how the real trinload record was created / its on-card layout.** samdisk vs
  a B-DOS FORMAT vs trinpush — unconfirmed; needed for faithful record-3 layout work
  (samdisk 3.8.12).

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
