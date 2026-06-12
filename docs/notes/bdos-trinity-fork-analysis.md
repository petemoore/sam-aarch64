# B-DOS 1.5t (Trinity fork): structure, device layer, and the hook surface

**Purpose:** Document how the Trinity fork of B-DOS (1.5t) differs from its public ancestor 1.5a, with one load-bearing question answered precisely: are the SAMDOS-compatible hooks this project's assembler uses (HGTHD/HLOAD/HSAVE/HOFLE/HSBYT, plus the rest of the hook surface) touched by the fork? This grounds the B-DOS reorientation (i72) and the boot-disk question (q10). (Research: i71, 2026-06-12. Companions: `bdos-version-landscape.md`, `trinity-capabilities.md`, `../specs/samdos-file-io.md`.)

**Sources and method.** The analysis compares the freeware B-DOS 1.5a release — binary plus Edwin Blink's complete Z80 source from `Bdos15a.zip`, the last public B-DOS source — against the B-DOS 1.5t beta 6 binary from private reference materials. Both binaries were disassembled and diffed at instruction level with relocation effects normalised out (16-bit operands re-checked against a piecewise shift map, so only real retargets survive), and the fork's structure was annotated routine-by-routine against the 1.5a source as label authority. All findings below are static (binary-diff) results; no Trinity hardware was executed (no emulator covers the Trinity ports). 1.5a routine names below are Edwin Blink's own labels from the freeware source.

## Lineage

B-DOS 1.5a (Edwin Blink, 1997/1998) is the last version with public source and the fork point for both descendant lines (see `bdos-version-landscape.md`). The Trinity fork, 1.5t, is by Colin Piggot and Chris Pile, 2008–2014; beta 6 (January 2014) is current, distributed with Trinity hardware and on the SAM Revival issue 25 coverdisk (issue 21 carried the SD-slot programming article). The fork self-describes as a patch: its boot banner keeps Edwin Blink's 1.5a version line verbatim and adds a Trinity patch line naming Piggot/Pile and the beta. Sizes: 1.5a is 10191 bytes; 1.5t beta 6 is 11044 bytes (+853).

Notably, the fork still reports DOS version 5 in DVAR 7 — identical to 1.5a and AL 1.5a. The documented DVAR-7 detection idiom (the one the i62 probe uses) therefore works unchanged under 1.5t, and conversely cannot distinguish 1.5t from the Atom builds; fingerprinting the fork requires the banner text or a Trinity hardware probe.

## What changed: the device layer swapped, everything above it kept

Structurally, 1.5t is the 1.5a program with its sector-device layer replaced underneath an unchanged command/hook surface. In 1.5a, every sector operation funnels through six dispatch sites in the common sector-I/O code — each tests the ambient device (floppy vs mass storage) and branches to one of six Atom routines: `hd.sbuf` (save sector buffer), `hd.lbuf` (load sector buffer), `hd.vbuf` (verify), `hd.lds` (load directory sector), `hd.ldbk` (load 510-byte block), `hd.svbk` (save 510-byte block). The fork keeps the six sites byte-for-byte in shape and retargets them at six SD equivalents, one-to-one; a handful of direct call sites in mass-storage-only paths (record-list access, FORMAT, record-label check) swap the same way. The floppy branch (WD FDC, ports `&E0`–`&E7`) is untouched, as is everything upstream: file headers, UIFA/DIFA handling, directory management, error reporting, date stamping.

What the fork deletes is exactly the Atom hardware layer: the ATA register-banging driver (`hd.seek`'s cylinder/head/sector arithmetic, `set.chs`'s five task-file writes, `hd.ready`/`hd.busy`/`wait.drq` status polling, master/slave selection, soft reset — 645 bytes reduced to a 92-byte seek), the ATA IDENTIFY boot panel, the Atom power-management command (DEVICE ON/OFF/STOP is removed from the added-BASIC-command table; an SD card has no spin-down), a boot-time Atom settle sequence, and the ESC-abort helper used in Atom busy-waits (SD transfers run with interrupts disabled, so there is no abort window). "Master"/"Slave" in DIR headers becomes "Trinity".

What it adds (+~1.4 KB gross) is a complete SD/MMC driver in the SPI style: the standard SD init ladder (CMD0, CMD8, CMD55+ACMD41 with HCS, CMD1 MMC fallback, CMD58 OCR read for the card type, CMD59 CRC-off, CMD9 CSD read and capacity parse, CMD16 blocklen 512), single-block read/write/verify via CMD17/CMD24, 32-bit sector arithmetic with a multiply/divide pair, a Trinity presence probe, card-capacity reporting ("BDOS records available = n"), hardware write-protect sensing, and a hot-swap path: RESTORE DEVICE now re-detects the interface and re-initialises the card, so cards can be changed without rebooting. SDHC/SDXC block addressing is selected per card at init (byte-addressed MMC/SDv1 cards are usable to 4 GB; block addressing is what carries beta 6 to its advertised 64 GB).

Port usage matches and extends what `trinity-capabilities.md` documents: the microcontroller select/status port `&DC` (busy = bit 3, polled before every SPI byte) with SD select values `&30`/`&31`/`&38`/`&3F` — the fork is the canonical consumer of these values, confirming the earlier empirical recovery — plus `&04` as the idle/deselect state bracketing every transaction, and SD data on port `&DF`. The `&3F` select-with-auto-null value is used for every bulk response/data phase, so the inner transfer loops are pure `INI`/`OUTI` with busy-polling and no per-byte dummy writes. Two port facts are new: the Trinity presence probe writes microcontroller commands `&08` then `&09` to `&DC` and reads the replies — expected `'T'`,`'R'` — from port `&DD` (the microcontroller answers on the EEPROM data port, not the SD port); and `IN (&DC)` carries card-present on bit 1 and the write-protect switch sense (inverted) on bit 2.

## The hook surface: untouched (verified)

This is the load-bearing result. Three checks, all on the final binaries:

1. The hook dispatch code (reached via the fixed `&8203` entry vector) is identical to 1.5a modulo relocation: same code-range check (hooks 128–166), same table lookup, same register save/restore through the `hk.*` boot variables.
2. The 39-entry hook vector table maps 1:1 between the two binaries: zero hooks added, zero removed, zero retargeted. Every entry's delta is the relocation shift of the region its handler sits in (a few bytes; HDINIT moved further because the init region around it was rewritten — same handler). The "unhandled hook" ignore entry occupies the same slots in both. HGTHD (129), HLOAD (130), HVERY (131), HSAVE (132), HVMSAD (134), HDINIT (135), HOFLE (147), HSBYT (148), HWSAD (149), HSVBK (150), HCFSM (152), HRECORD (156), HVEBK (157), HLBYT (159), HRSAD (160), HLDBK (161), HERAZ (166): all present, same slots, same handlers.
3. The handler bodies for all of these fall in regions the instruction-level diff classifies as relocation-only — no semantic edits. HRECORD keeps its register contract exactly (A=0 + record number in HL selects the record, ambient device becomes D2); only the record-to-media arithmetic far below it changed (CHS decomposition replaced by a flat 32-bit sector computation). The record model itself — 800 KB records of 1600 × 512-byte sectors, a record list + boot area at the start of the medium, record placement by the same base formula 1.5a uses (the formulas the i62 experiment verified against AL 1.5a and SimCoupé) — is identical, and the fork performs the same `BDOS` ID check at byte 232 of a record's first directory entry, rejecting unstamped records with the same 'Invalid record' error. Directory layout, date stamping, and the directory-management code generally are relocation-only, so the 1.5a filler-byte behaviour carries over too.

**Precision about what is verified vs inferred:** "untouched" is established by static binary diff — bytes, not execution. Confidence in the static claim is high: it is a mechanical 1:1 table comparison plus a relocation-classified diff over the handler code, done twice independently (annotation pass, then re-verification). What remains unexecuted is the runtime leg on real Trinity hardware (no emulator handles ports `&DC`–`&DF`); the residual risk is the generic gap between "same code" and "observed behaviour", not any observed difference. For planning purposes: code written against the SAMDOS hook layer per `../specs/samdos-file-io.md`, proven on SAMDOS 2 and B-DOS AL 1.5a in i62, has no identified reason to behave differently under 1.5t — the bytes it would execute above the device layer are the same bytes.

## Boot behaviour without Trinity hardware

The fork's boot path stays floppy-first: the boot sector pulls the DOS off the floppy via the FDC exactly as 1.5a, and mass-storage init happens only inside HDINIT, gated on the Trinity presence probe. If the probe fails ("No Trinity Ethernet Interface attached") or no card is present ("No flash card detected"), the DOS continues as a plain floppy DOS — functionally 1.5a with the Atom layer absent. This is a static observation, but the failure path is short and unconditional, so a 1.5t boot floppy is expected to behave on a Trinity-less SAM (or in SimCoupé, which leaves the Trinity ports unhandled) like 1.5a does without an Atom. Relevant to q10: the no-mass-storage tier degrades gracefully on this fork too.

## Implications

**For q10 (boot-disk DOS migration):** the fork analysis removes the main unknown on the 1.5t side — the hook surface our assembler targets is bit-identical 1.5a code, so a B-DOS-targeted boot disk works identically under AL 1.5a (CI tier, i62-verified) and 1.5t (Trinity tier, statically verified). DVAR-7 detection behaves identically under both. The open q10 sub-question that remains is the AL 1.5a no-hardware boot behaviour (i72's assessment); 1.5t's own no-hardware path is graceful per the above.

**For i70 / Phase-3 (multi-MB firmware blobs on SD):** B-DOS records are the right container and the hook layer is sufficient — HRECORD to select a record, then ordinary HSAVE/HOFLE+HSBYT writes, exactly the i62-proven call shapes. Two sizing facts matter: a record stores ~780 KB of file data (800 KB minus the directory tracks), so a multi-MB firmware set spans a handful of records and needs a record-spanning convention on our side; and the transfer loops are busy-polled SPI in the 20–80 KB/s estimated band (`trinity-capabilities.md` §5; the fork's inner loops have the same per-byte structure the estimate was derived from), so a one-off multi-MB provisioning write is minutes, not seconds — fine for the fetch-once model proposed in i70. Writes are gated by the card's physical write-protect switch (sensed in hardware by the fork) — a provisioning flow should surface that error case.

**For i72 (documentation reorientation):** `../specs/samdos-file-io.md`'s B-DOS compatibility preamble can state the 1.5t leg as statically verified rather than likely, with the runtime caveat above.

## Corrections / updates for the companion docs

For `bdos-version-landscape.md`:

1. §Recommendations: "the Trinity 1.5t leg remains LIKELY" → upgrade to verified-static: the hook dispatch, vector table, and handler code are 1.5a's bytes under relocation; only the sector-device layer changed. Runtime confirmation on hardware remains outstanding, but the claim no longer rests on lineage inference.
2. Open question 1 (what beta 6's "improved compatibility" covers): partially answered — beta 6 implements SDHC/SDXC block addressing (the >4 GB / 64 GB enabler) and card hot-swap via RESTORE DEVICE; per-beta attribution is impossible from beta 6 alone, so keep the question open but narrowed.
3. Open question 2 (filler-byte-32): can be closed — directory-management code is relocation-only in the diff, so 1.5a's behaviour carries over.
4. §SAMDOS compatibility / detection: add that 1.5t still reports DVAR 7 = 5, so DVAR-7 detection treats it as 1.5a-family (good for portability, useless for fingerprinting the fork).
5. Hardware matrix: the 64 GB figure applies to block-addressed SDHC/SDXC; byte-addressed MMC/SDv1 cards cap at 4 GB under the fork.
6. The fork's banner confirms the authorship/date line ("Colin Piggot/Chris Pile 2008/2014") verbatim.

For `trinity-capabilities.md`:

1. §2: the SD select values `&30`/`&31`/`&38`/`&3F` are now confirmed by B-DOS 1.5t itself (their canonical consumer), upgrading the "recovered from period utility software" provenance; `&04` additionally serves as the SD-idle/all-deselect state around every transaction.
2. §2 (new facts): microcontroller identity probe = commands `&08`, `&09` written to `&DC`, replies read from port `&DD`, expected `'T','R'`; `IN (&DC)` bit 1 = card present; bit 2 = write-protect sense, inverted.
3. §3: "auto-null for SD port — LIKELY" → verified: the fork's bulk read and write loops run under the `&3F` select with no per-byte dummy writes.
4. §5: the SD throughput estimate's loop model matches the fork's actual inner loops (busy-poll + `INI`/`OUTI` per byte), so the 20–80 KB/s working band stands, with the auto-null caveat resolved in its favour.
5. Open question 3 (who handles SD command latency): answered — the Z80 polls everything (R1 response, data token, write-busy, each with bounded retry loops), with the microcontroller's `&38` SD-init command run once before the SPI-level init ladder; the microcontroller's role per byte is the busy flag on `&DC` bit 3.
6. §4: the record model is confirmed from the implementation side — same 1600-sector stride, same record-list/base formulas as the public 1.5a source, same record ID stamp; sub-8 GB Trinity media and Atom-era media remain interchangeable at the format level (as SimCoupé's Atom Lite path assumes).

## Verification status

| Claim | Status |
|---|---|
| Hook dispatch + 39-entry vector table + handlers identical to 1.5a (mod relocation) | VERIFIED (static, twice independently) |
| Device layer swapped at six dispatch sites + direct mass-storage call sites | VERIFIED (static) |
| Record model / formulas / BDOS ID stamp unchanged | VERIFIED (static; formulas also execution-verified on AL 1.5a in i62) |
| DVAR 7 still = 5 | VERIFIED (static) |
| SD select values / probe / status bits as listed | VERIFIED (static) |
| Graceful floppy-only boot without Trinity | EXPECTED (short unconditional failure path; not executed) |
| Hook behaviour at runtime on real Trinity hardware | NOT EXECUTED (no emulator; needs hardware) |
| Beta 4/5 vs beta 6 feature attribution | UNKNOWN (only beta 6 analysed) |
