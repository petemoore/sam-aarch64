# sam-aarch64 Disk Image Catalogue

This catalogue covers every `.mgt` SAM disk image the build produces (or can produce). Sources: `ls build/*.mgt`, `grep -nE '\.mgt' Makefile`, `tools/build-disk/main.go`, `tools/run-release-gate.sh`. Counted: **27 images currently in `build/`**, plus **3 additional Makefile targets** not yet built locally (`netboot_http_smoke.mgt`, `font-proof.mgt`, `font-proof-8x8.mgt`).

All disk images are 819,200 bytes (SAM MGT format). All booted via **AUTO BASIC** (`CLEAR &7FFF: LOAD "<name>" CODE 32768: CALL 32768`, then `DI; HALT` when done) unless noted as CODE-auto (single `AUTO*` CODE file, entry = load address &8000 directly via B-DOS ALHK). Boot DOS baked into the floppy vessels is **B-DOS AL 1.5a** (`reference/bdos/al-bdos15a.bin` — the redistributable version), with SAMDOS 2 swappable via `-dos`. **Important — 1.5a vs 1.5t:** 1.5a is only what SimCoupé/CI boots from the *floppy* image. On **real Trinity hardware**, the autoboot bootblock loads **B-DOS 1.5t** (Colin's non-redistributable capture) into RAM to service the RST-8 storage hooks — so a *record*-booted disk runs against **1.5t**, not the baked 1.5a. Confirm Trinity behaviour against 1.5t (`bdos15t-beta6.annotated.dis`), never 1.5a. (See `memory/feedback_bdos_15t_not_15a`.)

---

## Category A — Assembler Test Disk (main + record variants)

### `build/test.mgt`
- **Makefile target:** `make disk`
- **Purpose:** Full assembler-test boot disk. The primary dev/CI disk: boots, runs all per-routine self-tests, then assembles the IN `.tbn` fixture and writes OUT; used by every `test-core`, `test-symbols`, `test-operands`, `test-paged` CI job.
- **Contents:**
  - `assembler.bin` — `src/assembler.asm` built with `-D BUILD_TESTS=1` (self-tests included)
  - `enctab.enc` — binary encoder table from `make enctab`
  - `IN` — the `.tbn` fixture (per-fixture; absent for bare-boot harness runs)
  - `test_mem` — `build/test_mem.bin` (off-axis page-13 test payload, from `src/test_mem_offaxis.asm`)
  - `p14` — `build/paged_call_test_payload.bin` (page-14 self-test payload, from `src/paged_call_test_payload.asm`)
  - `test_cluster` — `build/test_cluster.bin` (page-12 off-axis encoder cluster, from `src/test_offaxis_cluster.asm`)
  - `sd13` — `build/sysreg_data.bin` (page-13 sysreg lookup tables, from `src/sysreg_data.asm`)
  - `d15` — `build/disasm-test.bin` (page-15 disassembler with BUILD_TESTS=1, from `src/disasm.asm`)
  - `zx013` — `build/zx0-test.bin` (page-13 zx0 compressor+decoder with BUILD_TESTS=1, from `src/zx0_payload.asm`)
- **Boot mechanism:** AUTO BASIC `CLEAR &7FFF: LOAD "assembler" CODE 32768: CALL 32768`. Boot loader (`src/loader.asm`) HLOADs the payloads into their physical pages before handing off to `main_assemble`.
- **Entry point:** `&8000` (`src/assembler.asm` org)
- **How to test / verify:**
  - `make test-core` (core corpus), `make test-symbols`, `make test-operands`, `make test-paged` — each runs the round-trip (sam-aarch64 → `disk` → SimCoupé → samfile extract OUT → cmp vs GNU oracle).
  - The boot self-tests (routine-level: slots, symbols, local labels, expr_eval, PC-rel, trampoline, reader, disasm, zx0, …) run at boot and emit `OK`/`FAIL` via printer channel; a non-OK status causes the round-trip to fail.
  - Go harness `TestBootSelfTestsPass` (`tools/z80-test-harness-go/`) also boots this disk in ~30 ms.
- **ON A RECORD?** Record 186 holds `test_record.mgt` (the CODE-auto variant, see below). The BASIC-auto `test.mgt` is NOT stored as a Trinity record (a BASIC-auto disk can never boot from a record — B-DOS ALHK only runs an `AUTO*` CODE file directly).

---

### `build/test_record.mgt`
- **Makefile target:** `make disk-record` (PHONY — always repackages)
- **Purpose:** Boot-record-bootable RECORD vessel for the assembler test disk. Same payloads as `test.mgt` but the `assembler.bin` + AUTO BASIC pair are replaced by a single `AUTOasm` CODE file (exec = load address &8000). B-DOS ALHK runs the `AUTO*` CODE file directly when the record is booted via `boot-record.py` or the SAMBOOT auto-boot. Intended for the Trinity SD "assembler always available" workflow.
- **Contents:** Same as `test.mgt` except: replaces `auto` + `assembler` files with a single `AUTOasm` CODE file at &8000.
- **Boot mechanism:** B-DOS `ALHK` runs `AUTOasm` directly. Stored on the card via `make netboot-sd-push` + push workflow, booted via `boot-record.py <sam-ip> 186`.
- **Entry point:** `&8000`
- **How to test / verify:**
  - Go harness `TestBootRecordAssemblerVessel` (`tools/netboot-oracle/z80/`) — boots the exact artifact from the pushed context on captured B-DOS 1.5t; `SKIP_PRIVATE_TESTS`-gated in CI (needs Colin's ROM + EEPROM captures).
  - On hardware: store to record 186, boot it, look for the self-test OK status at the SAM screen / printer channel.
- **ON A RECORD?** YES — **Record 186** on the live card (stored 2026-07-02, named `test_record` or similar). Status: KEPT on card. Boot confirmed hardware (post-boot ping + discovery both silent = machine left trinload). On-screen self-test verdict deferred to Pete-presence.

---

## Category B — Release / Production Disk

### `build/release.mgt`
- **Makefile target:** produced inside `tools/run-release-gate.sh` — no standalone `make` target. Built by `build-disk -variant prod` (no BUILD_TESTS payloads).
- **Purpose:** The 3-way byte-match gate disk. Proves GNU binutils == Go toolchain == Z80 assembler on the vendored spectrum4 `release.s` (≈88 KB, `tests/release/release.s`). CI gate is `make ci-release` / `make ci-release-gate` (if wired) or called directly from the release gate script.
- **Contents:**
  - `assembler.bin` — `src/assembler.asm` built with no flags (assembler-prod, smallest binary, no self-tests)
  - `enctab.enc` — binary encoder table
  - `release.compact.tbn` — the compact `.tbn` of the spectrum4 release source (emitted by `sam-aarch64 -flatten`)
  - `sd13` — `build/sysreg_data.bin`
  - `d15` — `build/disasm.bin` (PROD disasm, no self-test)
  - `zx013` — `build/zx0.bin` (PROD zx0)
  - (no test-variant payloads: no `test_mem`, no `p14`, no `test_cluster`)
- **Boot mechanism:** AUTO BASIC `CLEAR &7FFF: LOAD "assembler" CODE 32768: CALL 32768`. Simulates the real end-user use: feed the big `.tbn`, wait ~20 s for the two-pass assembly, emit OUT.
- **Entry point:** `&8000`
- **How to test / verify:**
  - `tools/run-release-gate.sh` — 3-way byte-compare: `GNU (vendored tests/release/release.img)` vs `Go sam-aarch64` vs `Z80 SAM (SimCoupé)`. All three must match.
  - CI job `release` runs this gate.
  - `SIMCOUPE_TIMEOUT` lifted to 900 s for the large input.
- **ON A RECORD?** Not stored as a Trinity record in documented state.

---

## Category C — Assembler Encode-Self-Test Disk (enc-tests variant)

*Note: `build/assembler-enc-tests.bin` is built by `make assembler-enc-tests` but there is no `*.mgt` named `enc_tests.mgt` or similar in build/. The enc-tests variant disk is produced transiently during `make test-enc-tests` (which calls `tools/run-roundtrip.sh` with `ASSEMBLER_BIN=build/assembler-enc-tests.bin` against `tests/core/sources/inst_nop_ret.s`) and lands at `build/inst_nop_ret.mgt` — the same name the regular core corpus uses for that fixture. There is no permanently-named enc-tests-specific disk.*

*The enc-tests boot variant's unique self-test payloads are: `build/enc_fix_payload.bin` (page-11) and `build/overlay_suite.bin` (page-12). These ride the per-fixture disk named by the source file.*

---

## Category D — Netboot Bootable Disks

All netboot disks share the same build-disk format: DOS + AUTO BASIC + one CODE file (`-netboot-name`). AUTO BASIC: `CLEAR &7FFF: LOAD "<name>" CODE 32768: CALL 32768`. Entry: &8000. EEPROM config read at boot (`eeprom.asm find_index + read_chunk("Trinity Network ")`) yields the SAM's MAC, IP, gateway.

### `build/netboot_smoke.mgt`
- **Makefile target:** `make netboot-smoke-disk`
- **Purpose:** Trinity bring-up smoke test: boot on real SAM+Trinity, then `arping <sam-ip>` from LAN — SAM's MAC comes back. Proves ENC28J60 driver + ARP reply pipeline end-to-end.
- **Contents:** `netboot_smoke.bin` (`src/netboot/smoke_test.asm`); `smoke` CODE file at &8000.
- **Boot mechanism:** AUTO BASIC → `smoke` → `smoke_main` reads EEPROM config → `drv_init` → ARP-reply loop (`smoke_serve_once` forever).
- **How to test:** `arping <sam-ip>` from LAN — SAM MAC `02:54:52:49:4e:bc` replies. Go harness `TestNetbootBootSmoke` (`tools/netboot-oracle/z80/netboot_boot_test.go`) also gates the boot path.
- **ON A RECORD?** Not stored as a Trinity record in documented state.

---

### `build/netboot_server.mgt`
- **Makefile target:** `make netboot-server-disk`
- **Purpose:** Integrated ARP+DHCP+TFTP server boot disk (floppy vessel). Boot on SAM+Trinity, then point a Pi 400 at the SAM for network boot. The full `ARP + DHCP DISCOVER→OFFER→REQUEST→ACK + TFTP RRQ→DATA` session.
- **Contents:** `netboot_server.bin` (`src/netboot/netboot_server.asm`); `netboot` CODE file at &8000. Extends into section D (section-D overlay, up to &FFFF), so boot budget is 32 KB (&8000–&FFFF).
- **Boot mechanism:** AUTO BASIC → `netboot` → `netboot_main` → EEPROM config → `nb_fill_store` B-DOS store walk → `netboot_serve_once` dispatcher forever.
- **How to test:** Boot disk on SAM+Trinity, run `tools/hardware-shot/simulate-pi-client.py` from LAN; Go harness `TestNetbootServerFaithful` (`tools/netboot-oracle/z80/`) also boots the record vessel with captured ROM + B-DOS 1.5t.
- **ON A RECORD?** Record 187 holds `netboot_server_record.mgt` (CODE-auto variant, see below). This floppy vessel is NOT stored as a record.

---

### `build/netboot_server_record.mgt`
- **Makefile target:** `make netboot-server-record` (PHONY)
- **Purpose:** Integrated server as a boot_record-bootable RECORD vessel. Same binary as `netboot_server.mgt` but composed as a single `AUTOnbsrv` CODE-auto file + the Pi stand-in payload files in the same record directory (`config.txt`, `start4.elf`, `cmdline.txt`, `bcm2711-rpi-400.dtb` from `tools/netboot-oracle/testdata/pi-standins/`). Stand-in names >10 chars ride the NBMANIFEST name map (i346). Booted via `boot-record.py <sam-ip> 187`.
- **Contents:** `AUTOnbsrv` CODE-auto file at &8000 + 4 Pi stand-in files (some under mangled 10-char names + a manifest file).
- **Boot mechanism:** B-DOS ALHK → `AUTOnbsrv` (exec = &8000).
- **How to test:** `TestNetbootServerFaithful` (`tools/netboot-oracle/z80/`) boots this exact artifact on captured ROM + B-DOS 1.5t and replays the golden Pi session.
- **ON A RECORD?** YES — **Record 187** (stored + hardware-verified 2026-07-02). KEPT as reusable server record. First-ever DHCP served from real Trinity on this shot. TFTP serve confirmed (16.8 KB/s / 37 ms per 1024 B block).

---

### `build/netboot_serve.mgt`
- **Makefile target:** `make netboot-serve-disk`
- **Purpose:** Combined RRQ+WRQ "serve files" TFTP demo server (floppy vessel). Serves baked-in demo files (GET) and accepts disk image pushes (PUT, `trinity-sam-disks/` prefix). Config-aware: ships a small `cfg` CODE file that the AUTO BASIC overlays at `SERVE_CONFIG` address, so the WRQ record-placement strategy is baked at build time (`NETBOOT_STRATEGY=highest|lowest|explicit:N`). Default strategy: `highest`.
- **Contents:** `serve` CODE-auto binary (`netboot_serve_boot.bin`, `src/netboot/netboot_serve.asm`, `NETBOOT_REAL_LISTREAD=1`) + `cfg` CODE overlay at `SERVE_CONFIG` address.
- **Boot mechanism:** AUTO BASIC → `LOAD "serve" CODE 32768` → `LOAD "cfg" CODE <SERVE_CONFIG>` → `CALL 32768`. Entry: `serve_main` → EEPROM config → `provision_demo` → forever-loop `serve_serve_once`.
- **How to test:** Boot disk on SAM+Trinity, then from any LAN host: `tftp <sam-ip>` (GET serves demo files), `curl -T image.mgt tftp://<sam-ip>/trinity-sam-disks/image.mgt` (PUT writes a disk image to a free record).
- **ON A RECORD?** Record 145 holds `netboot_serve_record.mgt` (CODE-auto variant, see below). This floppy vessel is NOT stored as a record directly.

---

### `build/netboot_serve_record.mgt`
- **Makefile target:** `make netboot-serve-record` (PHONY)
- **Purpose:** Serve program as a boot_record-bootable RECORD vessel. Same binary as `netboot_serve.mgt` but composed as a single `AUTOserve` CODE-auto file with the strategy config baked into its bytes. Stored on card via `tools/trinload-push/trinpush-serve.py <sam-ip> --strategy highest`, booted via `boot-record.py`.
- **Contents:** `AUTOserve` CODE-auto file at &8000 (config bytes patched in by `trinpush-serve.py`).
- **Boot mechanism:** B-DOS ALHK → `AUTOserve` (exec = &8000) → `serve_main`.
- **How to test:** `TestBootRecordServeRecordVessel` (`tools/netboot-oracle/z80/`) boots this artifact from the pushed context on captured B-DOS 1.5t.
- **ON A RECORD?** YES — **Record 145** (`netboot_serve_re`, stored 2026-07-02 as the first sd-push run of the CODE-auto vessel). KEPT as reusable bootable serve record.

---

### `build/netboot_client.mgt`
- **Makefile target:** `make netboot-client-disk`
- **Purpose:** TFTP client boot disk. Boots on SAM+Trinity, fetches a file from a configured TFTP server, writes it to Trinity storage via B-DOS HSAVE hooks (Increment 3 test).
- **Contents:** `client` CODE file from `netboot_client_boot.bin` (`src/netboot/netboot_client.asm`, `NETBOOT_REAL_LISTREAD=1`). Extends into section D (SD CSD read overlay), 32 KB budget.
- **Boot mechanism:** AUTO BASIC → `client` → `client_main` → EEPROM config ��� ARP → TFTP RRQ → receive and HSAVE.
- **How to test:** Boot on SAM+Trinity with a TFTP server running the target file. See `docs/notes/netboot-trinity-testing.md` "Increment 3".
- **ON A RECORD?** Not stored as a Trinity record in documented state.

---

### `build/netboot_fetch_boot.mgt`
- **Makefile target:** `make netboot-fetch-boot-disk`
- **Purpose:** PXE-style fetch-and-boot disk (i182). Fetches a `.mgt` from a TFTP server, streams it into a scratch Trinity record, validates it, and `ALHK`-boots it. The bootstrapping vehicle for deploying a new assembler disk to the SAM.
- **Contents:** `fetchboot` CODE file from `netboot_fetch_boot.bin` (`src/netboot/netboot_client.asm`, `NETBOOT_REAL_LISTREAD=1 NETBOOT_FETCH_BOOT=1`). 32 KB budget.
- **Boot mechanism:** AUTO BASIC → `fetchboot` → `client_fetch_boot` → ARP → TFTP fetch → stream into free record → validate → `ALHK`.
- **How to test:** Boot on SAM+Trinity with a TFTP server serving the target `.mgt`. See `docs/notes/netboot-trinity-testing.md` "Increment 3" (i182 hardware run).
- **ON A RECORD?** Not stored as a Trinity record in documented state.

---

### `build/netboot_http.mgt`
- **Makefile target:** `make netboot-http-disk`
- **Purpose:** Full HTTP firmware-provisioning boot disk. Fetches all 6 Pi firmware files from a configured HTTP server (`HT_SERVER_IP_*`, default 192.168.0.1:80), SHA-256-verifies each against pinned manifest, streams via ZX0-compressed HSAVE into bounded Trinity records. Uses TLS if available (future; currently plain HTTP). Section-D overlay, 32 KB budget.
- **Contents:** `httpfetch` CODE file from `netboot_http_boot.bin` (`src/netboot/http_main.asm`, `NETBOOT_STREAM=1 NETBOOT_REAL_LISTREAD=1`, server IP baked at build time).
- **Boot mechanism:** AUTO BASIC → `httpfetch` → `http_main` → EEPROM config → DHCP → TCP → HTTP GET → SHA-256 verify → HSAVE.
- **How to test:** Boot on SAM+Trinity with HTTP server at configured IP (e.g. `python3 -m http.server` on LAN with Pi firmware blobs). See `docs/notes/netboot-trinity-testing.md` "HTTP fetch". CI: the Go harness `TestProvision*Z80` oracle tests exercise the provisioning loop.
- **ON A RECORD?** Not stored as a Trinity record in documented state.
- **Note:** The smoke variant `netboot_http_smoke.mgt` (`make netboot-http-smoke-disk`, `NETBOOT_HTTP_SMOKE=1`) exercises only 1 file (LICENCE.broadcom, 1594 B). It is NOT in current `build/`.

---

### `build/secd_probe.mgt`
- **Makefile target:** `make secd-loadability` (PHONY)
- **Purpose:** Section-D loadability probe. Empirical proof that `LOAD CODE 32768` deposits bytes past &BFFF into section-D RAM (not ROM1) at boot. Required foundation for SD CSD read (section-D overlay) and other section-D uses. Self-contained SimCoupé test: asserts "OK" or fails.
- **Contents:** `probe` CODE file from `build/secd_probe.bin` (`src/secd_probe.asm`, no DOS, uses `build-disk -netboot`).
- **Boot mechanism:** AUTO BASIC → `probe` → bakes sentinels into section D at &C000+, reads them back, prints "OK" (printer channel) iff readable, then `DI; HALT`.
- **Entry point:** &8000
- **How to test:** `make secd-loadability` — runs SimCoupé in an isolated HOME (no config pollution), asserts status file contains "OK". Not in CI directly (run before SD CSD work as a gate, i145b).
- **ON A RECORD?** Not stored as a Trinity record.

---

## Category E — Font-Proof / Editor Prototype Disks (not in current build/)

### `build/font-proof.mgt`
- **Makefile target:** `make font-proof`
- **Purpose:** 85×32 editor layout with 6px font. Renders a window of `tests/release/release.s` (lines 3837+) on a real SAM MODE 3 screen via the vendored five_pixel_font. Visual proof-of-concept for i76 P1b editor TUI. Capture via `tools/font-proof/run-capture.sh`.
- **Contents:** `fontproof.bin` (from `tools/font-proof/fontproof.asm`) + `font6.bin` + `text6.bin` under SAMDOS 2; called at &8000.
- **Boot mechanism:** SAMDOS 2 AUTO + CODE; `-call 32768`.
- **How to test:** Boot on real SAM (or SimCoupé with screen capture). Not a CI gate.
- **ON A RECORD?** Not stored as a Trinity record.

### `build/font-proof-8x8.mgt`
- **Makefile target:** `make font-proof` (same recipe, second output)
- **Purpose:** 64×24 layout with ROM 8×8 charset as reference comparison for font-proof (i76 P1b). Called at `&8003` (jumps into the 8×8 render path).
- **Contents:** Same `fontproof.bin` + `text8.bin` under SAMDOS 2; `-call 32771`.
- **How to test:** Boot on real SAM. Not a CI gate.
- **ON A RECORD?** Not stored as a Trinity record.

---

## Category F — Per-Fixture Assembler Test Corpus Disks

These disks are produced transiently by `tools/run-roundtrip.sh` (called by `make test-core`, `make test-symbols`, `make test-paged`, etc.) for each `.s` fixture in the test corpora. They exist in `build/` as residue of the last local test run. They are always rebuilt when the relevant test target runs; their names match the fixture source file's basename.

**All share the same disk structure:** assembler variant (test or prod, matching `ASSEMBLER_BIN`) + `enctab.enc` + fixture `.compact.tbn` as IN + full payload set (test: all pages; prod: no test-only pages) + DOS. AUTO BASIC entry at &8000. Expected result: `DI; HALT` after assembling the IN file; the assembled bytes are extracted as `OUT` and byte-compared vs GNU oracle.

### Core Corpus (`tests/core/sources/`) — assembled via `as + objcopy` (no linker, no relocations)

#### `build/inst_nop_ret.mgt`
- **Source:** `tests/core/sources/inst_nop_ret.s` — minimal `nop; ret` plus `mov/add/sub` (register-immediate)
- **Purpose:** Smallest meaningful encoding test; the canonical "does it boot at all" fixture. Also used by `make test-enc-tests` (one-fixture enc-tests variant smoke).

#### `build/inst_reg_imm.mgt`
- **Source:** `tests/core/sources/inst_reg_imm.s` — register + immediate ALU instructions.

#### `build/inst_movz_movn.mgt`
- **Source:** `tests/core/sources/inst_movz_movn.s` — `movz`/`movn` wide-immediate instructions.

#### `build/dir_data.mgt`
- **Source:** `tests/core/sources/dir_data.s` — `.byte`/`.long`/`.quad` data directives.

#### `build/dir_hword.mgt`
- **Source:** `tests/core/sources/dir_hword.s` — `.hword` (16-bit data) directives.

#### `build/dir_string.mgt`
- **Source:** `tests/core/sources/dir_string.s` — `.string`/`.ascii`/`.asciz` directives.

#### `build/expr_simple.mgt`
- **Source:** `tests/core/sources/expr_simple.s` — simple constant expressions in immediates.

#### `build/expr_extras.mgt`
- **Source:** `tests/core/sources/expr_extras.s` — more complex constant expressions.

#### `build/empty.mgt`
- **Source:** `tests/core/sources/empty.s` — empty source (zero-byte output). Special-cased: no `HSAVE` call; GNU oracle is `:` (produces empty file).

### Paged Corpus (`tests/paged/sources/`) — assembled via `as + ld -Ttext=0 + objcopy` (link-step resolves relocations)

#### `build/inst_long_emit.mgt`
- **Source:** `tests/paged/sources/inst_long_emit.s` — emits >16 KB of instructions, crossing a page boundary in the pool-run section-B output. Tests the paged-OUT machinery.

#### `build/inst_out_over32k.mgt`
- **Source:** `tests/paged/sources/inst_out_over32k.s` — emits >32 KB output, crossing the old two-page ceiling (i24). Tests multi-page paged-OUT.

### Symbols Corpus (`tests/symbols/sources/`) — assembled via `as + ld -Ttext=0 + objcopy`

#### `build/set_neg_highword.mgt`
- **Source:** `tests/symbols/sources/set_neg_highword.s` — negative absolute `.set`/`.equ` value consumed by `.quad`. Exercises i28: the high word must be sign-extended (0xFFFFFFFF), not zero.

---

## Category G — Diagnostic / Investigation One-Offs (NOT Makefile targets)

These three disks exist in `build/` as local artifacts from the i12b investigation (SimCoupé `-keyin` behaviour proof, June 21 2026). They have **no Makefile target** and are not reproduced by any `make` command. They should be treated as ephemeral — their source (likely manual `build-disk` invocations based on `src/netboot/key_read_test.asm`) is not captured in the tracked build system.

### `build/okstub.mgt`
- **Origin:** Manual build, i12b investigation (2026-06-21). Used to confirm CanType() and LMPR state when a program boots via the SAM startup screen.
- **Contents:** Diagnostic stub (likely based on `src/netboot/key_read_test.asm` or similar) that polls FLAGS/LASTK, reports status, halts.
- **Purpose:** Proved that `SIMCOUPE_KEYIN` does NOT reliably deliver keys to a running post-boot CODE program (see `tools/run-simcoupe.sh` DETERMINATION block for i12b).
- **Status:** Not reproducible from tracked build targets. Status file is empty (no printer output captured for the OK path).

### `build/dbgstub.mgt`
- **Origin:** Manual build, i12b investigation (2026-06-21).
- **Contents:** Variant diagnostic stub (debug build). Status file contains "RS" — a custom status code from the investigation.
- **Purpose:** Same investigation as `okstub.mgt`; tested a different code path.
- **Status:** Not reproducible from tracked build targets.

### `build/keyin-proof.mgt`
- **Origin:** Manual build, i12b investigation (2026-06-21). The canonical demonstration that keys do NOT arrive.
- **Contents:** Diagnostic stub booted with `SIMCOUPE_KEYIN='BOOT\nABCABC\n'` — confirmed LMPR=&1F (Section A=ROM0, Section B=page 0; CanType() satisfied) but FLAGS bit 5 (key-available) NEVER set and zero keys arrived.
- **Purpose:** Definitive proof for the i12b determination. Documented in `tools/run-simcoupe.sh`.
- **Status:** Not reproducible from tracked build targets.

---

## Record → Disk Mapping (documented live-card state as of 2026-07-02)

**IMPORTANT:** The table below reflects the DOCUMENTED state from the ROADMAP and item registry. The authoritative live-card state requires a `list-records` run (`make netboot-list-records` + push to SAM). Some records may have changed since last documented.

| Record # | Disk / Name | Source `.mgt` | Status |
|----------|-------------|---------------|--------|
| ~cjfixed | CJ's Elephant Antics (Pete's separate `cj.mgt`) | External project (`~/git/cjs-sam-remake/boot_m2b.mgt`) | Stored + booted 2026-07-02 (i295/i302); separate project |
| 145 | `netboot_serve_re` | `build/netboot_serve_record.mgt` | KEPT — CODE-auto serve vessel, hardware-proven 2026-07-02 |
| 186 | Assembler test disk | `build/test_record.mgt` | KEPT — CODE-auto assembler vessel, stored + boot-confirmed 2026-07-02; on-screen verdict deferred to Pete-presence |
| 187 | Integrated netboot server | `build/netboot_server_record.mgt` | KEPT — CODE-auto server vessel, hardware-proven 2026-07-02 (first DHCP from real Trinity) |

Records 188+ and any others: **run `list-records.py` to confirm current state** — the above is DOCUMENTATION STATE, not a live query.

---

## Source-Directory Map

| Directory | Produces | Ships in which disk(s) |
|-----------|----------|----------------------|
| `src/assembler.asm` (+ all of `src/`) | `assembler.bin` / `assembler-prod.bin` / `assembler-enc-tests.bin` | `test.mgt`, `test_record.mgt`, `release.mgt`, every per-fixture disk |
| `src/disasm.asm` | `disasm.bin` (prod), `disasm-test.bin` (test) | All assembler disks — prod variant in `release.mgt`/prod fixtures; test variant in `test.mgt`/test fixtures |
| `src/zx0_payload.asm` + `src/zx0_compress.asm` | `zx0.bin` (prod), `zx0-test.bin` (test) | All assembler disks (same split as disasm) |
| `src/sysreg_data.asm` | `sysreg_data.bin` | All assembler disks (both variants) |
| `src/test_mem_offaxis.asm` | `test_mem.bin` (page 13) | `test.mgt`, `test_record.mgt`, test-variant per-fixture disks |
| `src/test_offaxis_cluster.asm` | `test_cluster.bin` (page 12) | `test.mgt`, `test_record.mgt`, test-variant per-fixture disks |
| `src/paged_call_test_payload.asm` | `paged_call_test_payload.bin` (page 14) | `test.mgt`, `test_record.mgt`, test-variant per-fixture disks |
| `src/test_encode_inst_payload.asm` | `enc_fix_payload.bin` (page 11) | enc-tests-variant per-fixture disks only (`assembler-enc-tests.bin`) |
| `src/test_overlay_suite.asm` | `overlay_suite.bin` (page 12) | enc-tests-variant per-fixture disks only |
| `src/netboot/smoke_test.asm` | `netboot_smoke.bin` | `netboot_smoke.mgt` |
| `src/netboot/netboot_server.asm` | `netboot_server.bin` | `netboot_server.mgt`, `netboot_server_record.mgt` |
| `src/netboot/netboot_serve.asm` | `netboot_serve.bin` (hosttest), `netboot_serve_boot.bin` (real), `netboot_serve_boot_debug.bin` | `netboot_serve.mgt`, `netboot_serve_record.mgt` |
| `src/netboot/netboot_client.asm` | `netboot_client.bin` (hosttest), `netboot_client_boot.bin` (real), `netboot_fetch_boot.bin` | `netboot_client.mgt`, `netboot_fetch_boot.mgt` |
| `src/netboot/http_main.asm` | `netboot_http_boot.bin`, `netboot_http_smoke_boot.bin`, `netboot_http_boot_debug.bin` | `netboot_http.mgt` (full), `netboot_http_smoke.mgt` (smoke, not yet in build/) |
| `src/secd_probe.asm` | `secd_probe.bin` | `secd_probe.mgt` |
| `tools/font-proof/fontproof.asm` | `fontproof.bin` | `font-proof.mgt`, `font-proof-8x8.mgt` (not yet in build/) |
| `tests/core/sources/*.s` | per-fixture `.compact.tbn` | Per-fixture `*.mgt` (9 core fixtures) |
| `tests/paged/sources/inst_long_emit.s` | `.compact.tbn` | `inst_long_emit.mgt` |
| `tests/paged/sources/inst_out_over32k.s` | `.compact.tbn` | `inst_out_over32k.mgt` |
| `tests/symbols/sources/set_neg_highword.s` | `.compact.tbn` | `set_neg_highword.mgt` |

---

## Summary Counts

| Category | Count | Disks |
|----------|-------|-------|
| A — Assembler test/record vessels | 2 | `test.mgt`, `test_record.mgt` |
| B — Release / production disk | 1 | `release.mgt` |
| D — Netboot bootable disks | 9 | `netboot_smoke.mgt`, `netboot_server.mgt`, `netboot_server_record.mgt`, `netboot_serve.mgt`, `netboot_serve_record.mgt`, `netboot_client.mgt`, `netboot_fetch_boot.mgt`, `netboot_http.mgt`, `secd_probe.mgt` |
| E — Font-proof / editor prototype | 0 (not in build/) | (`font-proof.mgt`, `font-proof-8x8.mgt` — `make font-proof`) |
| F — Per-fixture corpus disks | 12 | 9 core + 2 paged + 1 symbols |
| G — Diagnostic one-offs (no Makefile target) | 3 | `okstub.mgt`, `dbgstub.mgt`, `keyin-proof.mgt` |
| **Total in build/** | **27** | |
| Additional Makefile targets not yet built | 3 | `netboot_http_smoke.mgt`, `font-proof.mgt`, `font-proof-8x8.mgt` |

---

## Unresolved / Flagged

1. **`okstub.mgt`, `dbgstub.mgt`, `keyin-proof.mgt`** — No Makefile target; source reconstruction uncertain. These are one-off diagnostic artifacts from the i12b investigation (June 21 2026). The probable source is `src/netboot/key_read_test.asm` (referenced in the i12b item), assembled and packaged manually with `build-disk -netboot`. They are NOT reproducible from `make`. They can be deleted from `build/` without losing anything tracked.

2. **Live Trinity card state** — The record-to-disk table above is documentation state from ROADMAP + registry. Run `make netboot-list-records` + `tools/trinload-push/list-records.py` against the live SAM (`192.168.2.75`) to confirm the current card layout. The ROADMAP notes record 145 and 187 as KEPT; 186 as KEPT; stale BASIC-auto record 186 was deleted before the CODE-auto vessel was stored.

3. **`release.mgt` Makefile target** — There is no standalone `make release-mgt` target. It is produced only inside `tools/run-release-gate.sh`. If a standalone target is wanted, it can be added as a simple `make` call to that script or by exposing the `build-disk -variant prod` invocation directly.

4. **enc-tests disk** — There is no permanently-named disk for the enc-tests variant. The variant is exercised only via `make test-enc-tests` which temporarily produces `build/inst_nop_ret.mgt` (overwriting the core variant). If a permanent enc-tests-specific disk name is needed, add a `make enc-tests-disk` target.
