# i145 — SD CSD capacity read → BD_RECORDS (the hardware-in-the-loop bootstrap)

**Why this exists / the lesson that earned it.** On 2026-06-21, with trinload running on Pete's
real SAM, the WRQ server (i121) was pushed a `.mgt` and **rejected every disk with TFTP ERROR 3
("no free record")** in 0.046 s. Root cause: `bdos_find_free_record` needs `BD_RECORDS` (the card's
total record count), and on real hardware `BD_RECORDS` is **0** — nothing reads it from the card.
**Every emulation test injected `BD_RECORDS`**, so the full experiment (trinload → push server →
push a `.mgt` → store) was never faithfully emulated, and the gap shipped to hardware. This was a
*known* gap (this very item predicted it) but it was deferred behind a hardware gate and not caught
before the push. The fix is the emulation-first discipline applied properly: **use the hardware to
learn the real CSD, model that seam faithfully in the emulator, run the full experiment in
emulation, then return to hardware.** (See `memory/feedback_emulation_first`.)

**Hard constraint (Pete).** The card size is **inferred from the card's own CSD register — never
hardcoded.** Pete's card is 64 GB; other users have other sizes. The code queries the live
environment and decodes the capacity from the CSD.

## The Trinity SD-SPI facts (from the i145 research; cite these when implementing)

- **Ports:** `&DC` = microcontroller select/status (OUT selects a device; IN bit 3 = busy, bit 1 =
  card-present, bit 2 = write-protect). `&DF` = SD SPI data. (`&DD` = EEPROM, `&DE` = ENC.)
- **SD select bytes:** `&31` select, `&30` deselect, `&38` init, `&3F` select+auto-null, `&04` idle.
- **SPI mechanics:** full-duplex; **busy-poll `&DC` bit 3 before every byte**; after `OUT (&DF),cmd`
  do a dummy `OUT (&DF),0` then `IN (&DF)` to read the response; `&3F` auto-null mode avoids the
  per-byte dummy for bulk reads. (`docs/notes/trinity-capabilities.md:25-64`,
  `docs/notes/bdos-trinity-fork-analysis.md:21`.)
- **Reuse, don't reinvent the primitives:** `wait_ready` (`encdrv.asm:418-421`, poll `&DC` bit 3),
  the enable/disable pattern (`encdrv.asm:423-456`, swap the ENC `&21/&20` for SD `&31/&30`), and the
  per-byte OUT→wait→dummy→wait→IN pattern (`eeprom.asm:320-340`). **No raw SD-SPI code exists yet —
  it is written from scratch on top of these primitives.**
- **SD init + CSD read:** CMD0 (go-idle, arg 0, CRC `0x95`) → CMD8 (if-cond; may time out on old
  cards, OK) → ACMD41 (with HCS) until ready → CMD58 (OCR → CCS bit = SDHC/block-addr) → **CMD9
  (SEND_CSD)** → wait data token `0xFE` → read 16 CSD bytes (+2 CRC, ignored). Send fixed CRCs;
  cards don't enforce CRC on reads in SPI mode. Leave the card cleanly idle afterwards so B-DOS can
  re-init.
- **CSD capacity decode (version-aware — handles ANY size):** top 2 bits of CSD byte 0 =
  `CSD_STRUCTURE`. `00` = v1.0 (≤2 GB): `sectors = ((C_SIZE+1)·2^(C_SIZE_MULT+2)·2^READ_BL_LEN)/512`.
  `01` = v2.0 (SDHC/SDXC, e.g. 64 GB): `C_SIZE` = 22 bits in CSD bytes 7..9, `sectors =
  (C_SIZE+1)·1024`. Then **`BD_RECORDS = sectors / 1600`** (`bdos15a.src.txt:1744-1751`;
  `recordListSectors=(records+32)/32`, `base=recordListSectors+1`).
- **B-DOS does NOT expose capacity** via a stable hook/DVAR (the fork relocated its internals), so
  the raw CSD read is the version-agnostic source of truth (`docs/notes/bdos-version-landscape.md`,
  `docs/notes/bdos-trinity-fork-analysis.md`).

## The bricks (tracked as i145a–i145d; deps traced; prioritised to the top)

- **i145a — CSD-read hardware probe.** A trinload-pushable Z80 program (mirror `netboot_dumper.asm`:
  read a region → serve it over TFTP) that runs the SD init + CMD9, puts the 16 raw CSD bytes (and
  the computed `sectors`/`BD_RECORDS`) into a buffer, and serves them as `csd.bin` (and e.g.
  `records.bin`). **Run on hardware** (`tftp get csd.bin`) to learn the **real** CSD bytes + confirm
  the protocol/timing. This is the hardware half of the loop — needs the SAM (Pete left trinload
  running). Esc returns to trinload.
- **i145b — production CSD read → BD_RECORDS + wiring.** Fold i145a's proven SD-init/CSD-read/decode
  into a routine called at `serve_main` (and `client_main`) startup that sets `BD_RECORDS` from the
  card, replacing the injected value. Depends on **i145a**.
- **i145c — harness SD-SPI/CSD model.** Model the `&DC`/`&DF` SPI seam + the SD command state machine
  in the harness `CardModel` (mirror `enc28j60.go`), returning a **configurable** 16-byte CSD (the
  capacity parameterised from the *real* value i145a learned — not hardcoded). This makes i145b
  emulation-verifiable and **ends the inject-`BD_RECORDS` shortcut** that hid this gap. Depends on
  **i145a** (needs the real CSD to model faithfully).
- **i145d — full E2E emulation test.** The experiment that was never faithfully emulated: trinload →
  push the serve program → `serve_main` reads EEPROM + the modelled CSD → `BD_RECORDS` computed (NOT
  injected) → push a `.mgt` → validate (size==819,200) → stream to the highest free record → claim.
  Depends on **i145b** + **i145c**.

**Then** the real-hardware push (the i121 payoff): push the serve program → `curl -T disk.mgt
tftp://SAM/trinity-sam-disks/name` for each `.mgt` → `tftp.done`. Unblocked by i145b on hardware.

## Hardware-gated / open
- Real SD response timing + token retries, CRC handling, and leaving the card cleanly idle for B-DOS
  are only verifiable on real hardware (the probe i145a discovers these).
- The i145c model captures the *logic*; real SPI timing stays a thin hardware-final gate (like the
  HWSAD writes). Emulation-verified ≠ hardware-verified (CLAUDE.md §5).

## Run context (this session)
SAM @ `192.168.2.75` (MAC `02:54:52:49:4e:bc`), this host the Pi `192.168.2.44` on the same subnet.
trinload left running overnight by Pete (manual realisation of the i133 self-service-testing intent,
so i145 is un-gated from i133 — i133's autonomous version stays separate). Push a probe:
`tools/trinload-push/trinload-push.py 192.168.2.75 build/<probe>.bin 1 0x8000`; fetch:
`curl -s -o csd.bin tftp://192.168.2.75/csd.bin`. The fresh-trinload discovery only answers after a
reload if it was left in a stuck state (the `STALE` ARP entry is usable — a permanent ARP entry
needs root, unavailable to the agent).
