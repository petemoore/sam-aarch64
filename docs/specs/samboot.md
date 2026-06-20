# SAMBOOT — configurable boot + autonomous hardware-test loop for the SAM Coupé

**This is the controlling overview for the SAMBOOT endeavour.** It is the single
home for *the whole story*: what we are building, why, the boot mechanism it
depends on, and the index into every deep doc and tracked item. It is a
**narrative + index** — it states the facts that live nowhere else and *links*
(never restates) the authorities for everything else (single-source-of-truth,
repo CLAUDE.md).

Controlling item: **i133** (the end-to-end loop). The investigate-first crux is
**i135** (auto-run a chosen record at power-on). The whole strand is indexed in
§7.

---

## 1. The goal, in one picture

Today, every hardware experiment on Pete's SAM means pulling the SD card to the
Mac and back — real card wear, and a human in the loop every time. SAMBOOT closes
that loop over the network:

```
  ┌──────────────────────────────────────────────────────────────────────┐
  │  Tapo smart plug  ── power-cycle ──►  SAM Coupé (Colin-patched ROM)     │
  │        ▲                                      │                          │
  │        │                              reset → EEPROM bootblock           │
  │        │                                      │                          │
  │        │                          draw MGT stripes  (always)             │
  │        │                                      │                          │
  │        │                 read SAMBOOT BIOS config: which record to boot? │
  │        │                                      │                          │
  │        │                     ┌────────────────┴───────────────┐         │
  │        │            a configured record                  none configured │
  │        │            (our TFTP-server disk,                     │         │
  │        │             a game, any disk)                  normal BASIC     │
  │        │                     │                          (wait for user)  │
  │  agent ◄── result over wire ─┤  (server disk:) receive image → save to   │
  │        ── push image ───────►┘  Trinity → boot it → report back over wire│
  └──────────────────────────────────────────────────────────────────────┘
```

For our test loop the configured boot record is the TFTP-server disk: the agent
power-cycles, waits for the SAM to come up serving, pushes a disk image, the SAM
saves + boots it, the booted program reports its result back over the wire, the
agent reads it — then power-cycles for the next experiment. **No human, no SD
shuffle.** But the boot mechanism is fully general: the configured record can be
*any* disk (a game, a demo) — SAMBOOT is "auto-boot the record you chose, with no
keypress," and the TFTP server is just the record *we* choose.

## 2. The one hard unknown (the crux)

A stock SAM ROM waits for user input at power-on; there is normally no way to make
it boot a chosen disk unattended. SAMBOOT is only possible because Pete's SAM runs
**Colin Piggot's patched system ROM**, which at reset auto-loads code from the
Trinity interface's writable EEPROM. **The entire endeavour hinges on whether we
can inject a "boot this record, no keypress" hook into that boot path.** If we
cannot, hardware testing will always need a human to pick and boot a record — so
we **investigate and prove that first, before building any of the custom loop**
(§6).

## 3. The boot chain — three distinct layers (the canonical naming)

> **This section is the single home for these names.** Other docs/items that
> touch the boot path point *here* rather than coining their own terms — they
> have historically been conflated, and that conflation is a real hazard.

The SAM reaches a running DOS through **three separate artifacts** — two of them
distinct *patches of distinct systems*:

1. **Patched SAM *system ROM* (a physical chip).** Colin supplies a modified 32 KB
   system-ROM chip that is swapped into the SAM in place of the stock ROM. Its
   only job vs stock is: at reset, fetch and run the bootblock from the Trinity
   EEPROM (instead of waiting for the keyboard). *This is a hardware chip; we do
   not rewrite it.* Colin has said it is a very small patch.
2. **Trinity EEPROM *bootblock*.** ~1 KB of Z80 code held in **EEPROM chunk 1**,
   loaded and run by the patched ROM. It pages the Trinity EEPROM in (ports
   `&DC`/`&DD`), copies the B-DOS image out of the EEPROM into RAM at `&8000`, and
   launches it. A public reproduction of this code exists:
   [`LongSteve/z80` `boot.asm`](https://github.com/LongSteve/z80/blob/main/boot.asm)
   (derived from Colin's SAM Revival issue 23 source) — and it already includes
   the MGT-stripes redraw *and* an unfinished "auto-load arbitrary file" TODO,
   which is exactly our boot-a-record injection hook.
3. **Colin's forked *B-DOS* 1.5t.** His Trinity-supporting DOS, held in **EEPROM
   chunks 2–13**, loaded by the bootblock. A *separate patch of a separate
   system* from the ROM chip — its hook table is byte-identical to stock B-DOS
   1.5a (so SAMDOS hooks like `ALHK`/136 carry over unchanged).

**The injection target is the *writable EEPROM* (the bootblock in chunk 1, and/or
the B-DOS image in chunks 2–13) — never the physical ROM chip.** The EEPROM is
flashable in software via the Trinity `write_chunk` routine
(`src/netboot/eeprom.asm`); the chip is not. This is what makes SAMBOOT tractable.

## 4. The injection + the SAM "BIOS"

The plan is to patch the **EEPROM bootblock** so that, after loading B-DOS and
**always redrawing the MGT opening stripes** (the screen the patch otherwise
tramples — folding in what was tracked separately as **i112**), it:

1. **Reads a SAMBOOT boot config from the EEPROM** — a small, *editable* config
   chunk, structured exactly like the existing "Trinity Network " config chunk the
   bootblock already reads. This is effectively a **BIOS for the SAM**: a
   persisted, user-configurable boot sequence.
2. **Boots the configured record, or nothing:**
   - a **record number is configured** → auto-boot that Trinity record with no
     keypress. The record can be *any* disk — our TFTP-server disk, or a game like
     Lemmings; the mechanism does not care what the record contains;
   - **no record configured** → fall through to normal B-DOS/BASIC, waiting for
     the user (the stripes are already on screen).

So the BIOS is a generic "default boot record" setting — the same idea as a PC
BIOS boot-order. For our autonomous test loop we simply set it to the
TFTP-server disk; for everyday use Pete could set it to anything, or leave it
unset for a normal boot.

The push-safety concern (q30) is handled one layer up, *inside* the server
program we choose to boot (its own accept-in config), not by the BIOS — the BIOS
only chooses *which record boots*.

A config editor (host-side and/or on-SAM) lets the default boot record be set —
the "BIOS setup" equivalent.

## 5. What is already built (the transport)

Most of the network transport the loop needs already exists and is host-verified;
several legs are hardware-confirmed (first-light 2026-06-18, SAM @ `192.168.2.75`).
See `src/netboot/README.md` for the file-by-file map and verification status.

| Loop leg | Status | Code |
|---|---|---|
| Bring-up smoke (ARP on real Trinity) | **hardware-confirmed** (rec 10) | `smoke_test.asm` (i94) |
| TFTP **server** (RRQ serve-out) | **hardware-confirmed** (rec 11) | `netboot_serve.asm` / `netboot_server.asm` (i96/i95) |
| TFTP **client** (RRQ fetch → Trinity) | host-verified | `netboot_client.asm` (i82) |
| TFTP **WRQ** (write / push-in disk image) | **not built** | **i121** |
| Fetch-and-boot a record | umbrella, boot primitive done (i122a, #480) | `bdos_seam.asm`, **i122** |
| Persist to Trinity storage (HSAVE) | host-verified; HSAVE dispatch hardware-gated | `bdos_seam.asm` (i119) |
| trinload push→run→return (RAM, dev iteration) | **hardware-confirmed** (rec 3/128) | `trinload.asm` (i129/i132) |
| Read EEPROM config chunk | hardware-confirmed (reads "Trinity Network ") | `eeprom.asm` (Colin's lib, vendored) |

The dumper (§6 step 2) reuses the **proven TFTP server** to ship the captured
ROM/EEPROM off the SAM — the host just `tftp get`s them; no new transport, and no
WRQ needed.

## 6. The investigate-first chain (atomic steps)

Each step is one landable deliverable; the hardware steps are gated on Pete being
physically present (`owner: pete`), the rest are agent work.

1. **Understand the bootblock from public source** *(agent, no hardware)* — read
   `LongSteve/z80` `boot.asm` + the forked-B-DOS chunk map; document the load
   sequence and pin down the auto-load-file (boot-a-record) injection hook + the
   stripes redraw. *(i135a)*
2. **Build the one-shot dumper** *(agent)* — a small program that reads the
   patched system ROM (32 KB, via SAM ROM paging) and the EEPROM chunks, and
   **serves them via the proven TFTP server** so the host pulls `rom.bin` +
   `eeprom.bin`. Pushed onto the SAM via trinload (already proven). This is the
   first thing to build; it captures the artifacts that unblock all the offline
   analysis, and the EEPROM capture is the **mandatory backup** before any flash.
3. **Run the dumper on hardware** *(pete)* — capture `rom.bin` + `eeprom.bin` from
   the real SAM. *(i87a)*
4. **Document the boot chain** *(agent)* — diff `rom.bin` vs the stock SAM ROM
   3.0, confirm the EEPROM bootblock matches the public reproduction, and write up
   exactly how the chip → bootblock → B-DOS handoff works + where the hook
   attaches. *(i87b)*
5. **Design + emulation-prototype the injection** *(agent, emulation-first)* —
   model the EEPROM chunks in the harness and prototype the patched bootblock:
   always redraw the stripes, read the BIOS config, boot the configured record (or
   fall through). Includes the BIOS config schema + the stripes redraw. *(i135d,
   folds i112)*
6. **Flash the patched EEPROM** *(pete)* — write the prototyped image via
   `write_chunk`, with the step-3 backup as the restore path, power-cycle, and
   confirm unattended auto-boot of the configured record. **This step answers the
   crux.** *(i135c)*

Then the loop is assembled from the already-built transport (§5) + the
remaining host-side pieces (Tapo power-cycle control, the result-reporting
channel, the experiment orchestrator) under the controlling item **i133**.

## 7. Item index (the live registry is the source of truth)

Browse `build/registry view --id iNN`; this is a map, not a status mirror.

- **i133** — the controlling item: the full autonomous loop (depends on i135 +
  i121 + i122 + the host-side pieces).
- **i135** — auto-boot a chosen record at power-on (the crux); the §6 chain
  (understand → design+prototype → flash) hangs here. i135 is a **prerequisite
  of** i133.
- **i87** — capture + document the patched ROM/EEPROM (split: hardware capture +
  desk analysis).
- **i112** — restore the trampled opening stripes; **folded into the injection
  patch** (§4 / §6 step 5).
- **i121** — TFTP WRQ (push-in disk image); the one unbuilt transport leg.
- **i122** — fetch-and-boot a record (umbrella; boot primitive i122a done).
- **i119** — persist a disk image to Trinity storage (the save half).
- **i173** the one-shot dumper · **i176** the SAMBOOT BIOS config + editor ·
  **i177** the open-source **trinboot** release (a Python-over-trinload patcher /
  bootable `.mgt` that gives *any* Trinity owner the configurable default-boot
  record + restored stripes — depends on SAMBOOT, never the reverse) · plus the
  host-side result-channel / Tapo / orchestrator items (see the registry).

## 8. Risks, constraints, provenance

- **The crux may fail.** If no boot-a-record hook can be injected into the EEPROM
  boot path, hardware testing stays human-gated. We prove §6 step 6 before
  committing to the custom loop.
- **No need to involve Colin.** The mechanism is publicly documented
  (`LongSteve/z80` + SAM Revival 23) and Colin has confirmed the ROM patch is very
  small; we work from the public source rather than asking him.
- **Redistributability.** The patched system ROM and forked B-DOS 1.5t are Colin
  Piggot's proprietary work. Captured dumps (`rom.bin`, `eeprom.bin`) are **local
  analysis artifacts — never committed to the repo** (kept under `~/sam-archive`
  like the existing B-DOS analysis). The public `LongSteve/z80` source is **cited
  by URL, not vendored.**
- **Emulation-first.** Every code path — the dumper, the injection prototype, the
  bootloader — runs in the koron-z80 harness before hardware (CLAUDE.md §7).
  "Emulation-verified" is **not** "hardware-verified" (CLAUDE.md §5); the hardware
  steps (§6 steps 3, 6) are the real gate.

## 9. Cross-references (the authorities — linked, not restated)

- Delivery architecture (server/client/HTTP/capstone): `docs/specs/phase3-delivery-design.md`
- Trinity hardware (ports, EEPROM, SD, SPI): `docs/notes/trinity-capabilities.md`
- Forked B-DOS layer + hook portability: `docs/notes/bdos-trinity-fork-analysis.md`, `docs/notes/bdos-version-landscape.md`
- Safe free-record selection: `docs/specs/trinity-record-detection-design.md`
- Serve-time name→record + storage classes: `docs/specs/netboot-storage-manifest-design.md`
- Hands-on hardware run guide: `docs/notes/netboot-trinity-testing.md`
- DHCP+TFTP wire oracle: `docs/notes/pi-netboot-capture-analysis.md`
- Z80 file-by-file map + authority model: `src/netboot/README.md`
- The trinload push vehicle + EEPROM library: `src/netboot/trinload.asm`, `src/netboot/eeprom.asm`
- Public bootblock reproduction: [`LongSteve/z80` `boot.asm`](https://github.com/LongSteve/z80/blob/main/boot.asm)
- Deep boot-chain evidence (external, non-redistributable): `~/sam-archive/trinity-docs/KEYBOARD_BOOT_WORKAROUND.md`, `~/sam-archive/trinity-docs/DISCOVERY_REPORT.md`, `~/sam-archive/bdos/analysis/`
