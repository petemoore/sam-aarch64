# i173 — SAMBOOT one-shot ROM+EEPROM dumper (build plan)

Ephemeral plan (deleted by the PR that completes i173). Controlling charter:
`docs/specs/samboot.md` §6 step 2. The dumper is pushed to the SAM via trinload,
reads the patched 32 KB system ROM + the 128 KB Trinity EEPROM, and serves both
over TFTP so the host `tftp get`s them. The captured dumps unblock i87b and are
the mandatory backup before any EEPROM flash (i135c).

## The load-bearing constraint (why regions, not one dump)

The proven serve loop serves **one contiguous ≤64 KB RAM buffer via a 16-bit
pointer**: `send_next_data` (`src/netboot/netboot_serve.asm:446-488`) computes
`DATA_PTR = SRC_PTR + XFER_OFFSET` with a 16-bit `add hl,de`; the high words of
`XFER_SIZE`/`XFER_OFFSET` are never consumed. No serve path pages a source bank
mid-transfer. So a served file must be a contiguous ≤64 KB RAM region. 32 KB ROM
+ 128 KB EEPROM cannot be staged at once.

**Therefore: serve 16 KB regions, one named TFTP file each.** Host concatenates.
- ROM → `rom0.bin` (ROM0 `&0000-&3FFF`) + `rom1.bin` (ROM1 `&C000-&FFFF`). `cat rom0.bin rom1.bin > rom.bin`.
- EEPROM → `eep0.bin`..`eep7.bin` (8 × 16 KB = 128 KB). `cat eep0.bin … eep7.bin > eeprom.bin`.

Each region is staged into one reused 16 KB scratch buffer just before its
transfer (the `dumper_refresh_region` hook, below), so the serve loop's
contiguous-buffer model is untouched.

## A. Memory & paging layout

- trinload pushes the program to **page P, offset `&8000`** (`@`/`X` packets set
  `set 7,d` forcing top-32K; final `X` = `out (HMPR),P; jp &8000`). trinload
  itself lives at `&6000-&72FC` (section B page, org `&6000`) — the dumper at
  section C does NOT overlap it; never repage section B so `RET` works.
- Dumper code + state live in **section C (`&8000-&BFFF`)** of page P — budget
  ≤16 KB (run `tools/netboot-boot-fit-check.sh`).
- Staging buffer `STAGE` = 16 KB, in **section D (`&C000-&FFFF`)** mapped to a
  **scratch RAM page S** (distinct from P and from trinload's page). Set LMPR
  bit6=0 (ROM1 off) so section D is writable RAM while staging/serving most
  regions. `SRC_PTR=&C000`, `XFER_SIZE=16384`.
- `rom1.bin` is special: ROM1 lives at section D, colliding with STAGE. Stage
  ROM1 via a **section-A scratch page** instead and have `dumper_refresh_region`
  **overwrite `SRC_PTR`** (and the page registers) to point at wherever that
  region was staged. (`resolve_src` already writes `SRC_PTR` at
  `netboot_serve.asm:578`; the refresh routine runs after it and may overwrite.)

## B. File sources (mirror `provision_demo`, `netboot_serve.asm:936-945`)

Provision a region STORE + SRC_TABLE from assembly-time templates:
- **STORE** entries (`name\0` + 4-byte LE size): `rom0.bin`,`rom1.bin`,`eep0.bin`…`eep7.bin`, each size 16384, then `0`.
- **SRC_TABLE** entries (`name\0` + 2-byte LE ptr + 4-byte LE size): all point at `STAGE`/16384 by default; the refresh hook overrides `SRC_PTR` per region as needed.

**The one serve-loop change:** in `rrq_hit` (`netboot_serve.asm:305`), after
`resolve_src` sets `SRC_PTR`/`XFER_SIZE` and before arming the transfer, add
`call dumper_refresh_region`. It inspects the resolved `PARSE_FILENAME` and fills
`STAGE` (or a region-specific buffer + overrides `SRC_PTR`) with the requested
region's bytes. By block-1 time the buffer holds the region; the loop streams it
normally. This keeps the streaming path proven and unchanged. Guard the hook so
the standalone serve build is unaffected (the dumper is its own image/program).

## C. ROM read (HARDWARE-FIRST — every paging assumption ships with a VERIFY-ON-HARDWARE comment)

ROM0 at section A (LMPR bit5=0), ROM1 at section D (LMPR bit6=1) — `docs/notes/sam-paging.md:99-122`.
- **rom0.bin:** `di`; save LMPR; set bit5=0 (ROM0 at A), bit6=0 (ROM1 off, protects STAGE at D); `ld hl,&0000 / ld de,&C000 / ld bc,&4000 / ldir`; restore LMPR; `ei`. Stack stays in section C (safe). VERIFY: reading `&0000-&3FFF` returns patched ROM low 16 KB.
- **rom1.bin:** ROM1 (section D) is the source and collides with STAGE — stage to a **section-A scratch page** instead: map scratch page at A (LMPR low5=scratch, bit5=1 RAM) with bit6=1 (ROM1 at D); `ld hl,&C000 / ld de,&0000 / ld bc,&4000 / ldir`; restore; set `SRC_PTR` to the section-A buffer and leave that page mapped at A for the transfer. VERIFY: `&C000-&FFFF` returns patched ROM high 16 KB.
- Keep `DI` only around each `ldir`; restore `EI`. Restore ALL touched page registers before `RET`.

Flag assumptions A1–A5 (ROM0/ROM1 mapping, ldir source reads ROM, stack safe,
ROM1 byte-addressable, ENC I/O independent of ROM mapping) as VERIFY-ON-HARDWARE.

## D. EEPROM read (EMULATION-VERIFIABLE — reuse `eeprom.asm` verbatim)

For region N (16 KB = 16 chunks), read chunks covering EEPROM bytes
`N*16384 .. N*16384+16383` (chunk K base = `8192+(K-1)*1024`; pick the mapping so
`cat eep0..eep7` reconstructs the raw 128 KB in chunk order). Per chunk:
`ld a,<chunk#> / ld (value),a / call read_chunk` then `ldir` the 1024-byte `chunk`
buffer to `STAGE + i*1024`. `read_chunk` reads by number via `get_chunk`
(`eeprom.asm:312-341,468-475`), exactly as the bootblock does.

## E. trinload integration

- Push to page P offset `&8000`; entry at `&8000`; init (read MAC/IP from the
  "Trinity Network " chunk via `find_index`+`read_chunk`, fill CONFIG, provision
  region STORE/SRC_TABLE, `drv_init`), then loop `serve_serve_once`.
- **Esc-to-exit:** poll the keyboard like trinload (`trinload.asm:89-92`); on Esc,
  restore all page registers and `RET` (trinload pushed `start` as the return
  address) so the dumper can be re-pushed for another capture.
- Must not clobber trinload (`&6000-&72FC`) or its page; never repage section B.

## F. Emulation test plan (`tools/netboot-oracle/z80/dumper_test.go`)

EMULATION-VERIFIABLE:
1. **EEPROM-read + serve, byte-exact.** Add `ENC28J60.ProgramChunk(n, data)` to
   `enc28j60.go`/`eeprom.go` (mirror `ProgramTrinityNetwork`). Program 16 known
   chunks; drive a bare RRQ for `eep0.bin` through `serve_serve_once`; assert the
   streamed DATA reconstructs the 16 KB programmed (identity round-trip — no Go
   authority needed). Mirror `netboot_serve_test.go` shape (`serveDemo`/`eqFrame`).
2. **Multi-block transfer completes** (16384 B = 32 full 512-B blocks → final ACK ends).
3. **Negative control:** an unprogrammed region serves zeros.

Build the host-test binary with `-D NETBOOT_HOSTTEST=1`, including `eeprom.asm`
(so the EEPROM refresh is testable) but guarding the ROM-paging refresh + the
forever-loop behind `NETBOOT_HOSTTEST==0` (or a `DUMPER_ROM` define), so the host
test exercises the EEPROM+serve path without the un-emulatable paging.

HARDWARE-FIRST: the ROM-page read. The flat harness has no patched-ROM contents
and does not act on LMPR/HMPR, so there is nothing to assert the ROM `ldir`
against (the captured bytes are the point — verified on hardware via i87a, diffed
in i87b). The harness LMPR/HMPR-paging model that would let us emulation-verify
the ROM-paging *sequence* against a stock ROM fixture is tracked separately (§7
follow-up item) and is NOT a blocker for i173.

## G. Build / wiring (mirror the `netboot-serve`/`netboot-trinload` blocks)

- New source `src/netboot/netboot_dumper.asm` (includes `build_udp_frame`,
  `build_arp_reply`, `tftp_build`, `tftp_parse`, `encdrv`, and always `eeprom.asm`).
- Makefile: `netboot-dumper` (host-test `.bin`/`.map`, `-D NETBOOT_HOSTTEST=1`) +
  `netboot-dumper-trinload` (trinload-pushable raw binary org `&8000`). Add both to
  `.PHONY` (line 76) and `netboot-z80-routines` (line 780). No `-disk` target — it
  is pushed via trinload, not booted (note this in the comment). Run
  `netboot-boot-fit-check.sh` on the binary.
- CI: `ci-netboot-z80` picks up `dumper_test.go` automatically (it `Skipf`s if the
  binary is absent, like every other netboot test).
- No Go authority needed (EEPROM round-trip is an identity check; serve cadence
  reuses existing authorities). Only the `ProgramChunk` test-support helper.

## H. Open questions → qN/iN
- (Hardware, i87a) Does the patched chip map ROM0/ROM1 as documented for stock?
- (§7 follow-up, tracked separately) Harness LMPR/HMPR paging model + stock-ROM
  fixture to emulation-verify the ROM-paging *sequence*. Not a blocker for i173.
- (Robustness) Re-push after Esc-RET works iff all page registers are restored.
