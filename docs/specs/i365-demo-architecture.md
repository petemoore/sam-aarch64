# i365 demo — architecture

The i365 demo (umbrella `i365`) proves the assembler CORE end-to-end on real
hardware, network-verifiable: a Trinity-record disk holding only `release.tbn`
plus the programs boots and (1) **renders** `release.tbn` → `release.src`
(human-readable source, the editor's display path) and saves it, (2) **assembles**
`release.tbn` → `release.img` (aarch64 binary) and saves it, (3) **serves** both
over TFTP so a host fetch confirms the run. The interactive editor is the only
missing piece.

This doc is the design home for the leaves `i365c`/`i365d` point at. It is a
living design doc — fold its durable parts into `docs/ARCHITECTURE.md` and delete
it when the demo ships (after `i365e` + `i368`).

## SHIPPED (i365d-b2c, 2026-07-05) — ASSEMBLE-FIRST, not render-first

The demo is implemented and faithful-gated end-to-end: ONE boot assembles
`release.img`, renders `release.src`, and serves both byte-exact over TFTP
(`assemble_first_serve_faithful_test.go`). The **"Driver architecture" section
below (render-first orchestrator) was SUPERSEDED** — render-first is
architecturally impossible for b2c: the 1600-sector record can't hold IN (729
sec) + RELEASESRC (820) + RELEASEIMG (43) + the files unless **RELEASESRC reuses
IN's sectors** (base linearSec 40), but IN is read by BOTH the render and the
assembler, so they must be **ordered so both read IN before render overwrites it**
→ assemble-first. The shipped chain is `assembler(AUTO) → render(overlay) →
server(overlay)`, each HLOAD'd over the `&8000` window from a section-B loader
stub (the one genuinely new capability). Key overlay-load lesson: a >16 KB
(2-page) HLOAD to `&8000` ADVANCES HMPR per 16 KB (data lands in C+D fine, but
HMPR is left at the 2nd page — the stub must save/restore the boot HMPR); and the
loader stub must not leave SP at `&FFFE` (it collides with `rdb_load_tbn`'s stack).
FIX 2 (the free-slot MGT dir writer that preserves existing entries + the BDOS
stamp, in `render_disk_sink.asm`) is a real shared-card data-safety fix. The
on-screen RST&10 progress messages are deferred to hardware (`i368`, needs a real
screen — RST&10 hangs at come-up under an ALHK record boot). Remaining: `i365e`
(store + boot the real SAM + fetch both) and `i368` (the messages).

## The two capability walls (why the demo is bigger than "wire it up")

`i365a` (#858, the `.tbn`→source renderer, byte-exact vs Go `render.Emit` over the
full 371 KB corpus) and `i365b` (#859, the callable `DEMO_ASM` assembler that
HSAVEs `RELEASEIMG` and `ret`s) are DONE. What remains is **not** glue — it is two
substantial new capabilities the original rows underestimated, plus the driver
and the hardware shot. Measured sizes ground the problem:

- `release.src` = **417,374 bytes** (Go `render.Emit` over `build/release-unstripped.tbn`; the byte-exact target the Z80 render is gated against).
- `release.img` = **21,752 bytes** (`build/release-unstripped.img`).
- A Trinity record = **819,200 bytes** (1600 × 512). Both files + the 40-sector directory ≈ 899 of 1600 sectors (~56%) — room is not the problem.
- Free RAM during a render ≈ 5 × 16 KB ≈ 80 KB ≪ 408 KB output — **whole-file buffering is impossible**.

### Wall 1 — writing a >free-RAM file to disk (i365d render sink)

The renderer streams source text one byte at a time out of `render_out_append`
(`src/tbn_render.asm:901`, currently `out (RENDER_SINK_PORT),a`, port `&E8`, the
harness capture port). That sink must be retargeted to an on-disk **chunked**
write, because:

- **HSAVE can't do it.** `save_out_file` (`src/main_loop.asm:1835`) writes a whole
  memory region as one file, but caps at **64 KB/call** (UIFA[34] masked `&1F`,
  UIFA[35-36] 14-bit) and needs the whole region resident. 408 KB fails both.
  There is **no append / seek / write-at-offset** — HOPEN/HCLOS/HEOF/HPTR are
  `ret` stubs in SAMDOS.
- **The B-DOS streaming trio is broken for external callers, even in 1.5t.**
  HOFLE(147)/SBYT(148)/CFSM(152) never call `gtixd`/`res.buf`, so `ofsm`'s
  IX-relative writes land inside SAMDOS's own paged-in code (`docs/notes/sam-stub-audit.md:242-301`).
  Only HSAVE calls `gtixd` first, which is why HSAVE alone is safe. B-DOS 1.5t is
  byte-identical here under relocation — it does **not** fix this.

**The mechanism that works: raw-sector chunked streaming into one named MGT file.**

- Reuse `src/netboot/raw_record_sink.asm` verbatim as the accumulator: it re-blocks
  a byte stream into 512-byte sectors, flushes each full sector immediately
  ("RAM never holds more than one sector"), zero-pads the final partial sector,
  and keeps a 32-bit total in `RRS_TOTAL`. This is almost exactly the render
  sink's body already written.
- Flush each sector via `bd_record_write_hw` (raw CMD24, `src/netboot/sd_csd.asm:606`,
  with the i295 data-safety band guard keeping every write inside the record) at
  linear sectors placed **after** the directory (tracks 0-3) and after
  `release.img`'s sectors.
- LBA math is `bd_record_lba_compute` (`sd_csd.asm:504-567`): `LBA = csd_base + 1600*(record-1) + linearSec`.
- The directory-region format + `"BDOS"@232` stamp is `store_format_record`
  (`src/netboot/http_main.asm:735`).

**The one genuinely new piece: an MGT directory-entry writer for a named CODE
file.** The streamed body lands in raw sectors; to be a real B-DOS file that
`HGTHD`/`HLOAD` and the server's directory walk find, we must write a directory
entry in the format `nb_walk_entry` decodes (`src/netboot/netboot_server.asm:1344-1428`):
`+0` type (19=CODE), `+1..10` name, `+0xEF` full-16K-page count, `+0xF0..F1`
lengthMod16K — plus the 195-byte MGT sector-address bitmap, start track/sector,
the 9-byte CODE header, and the per-sector forward links (510 data + 2-byte
next-track/sector). This is a bounded port of B-DOS's `ofsm`/`svhd`/`cfsm`
*effect* (CLAUDE.md rule 8 — B-DOS is the authority; derive from the 1.5t
annotated disassembly and from `store_format_record`, which already reproduces
FORMAT's record-level effect via raw CMD24). Length comes from `RRS_TOTAL` at
end-of-stream; a contiguous allocator from track 4 (after `release.img`) avoids
replicating B-DOS's BAM scan.

### Wall 2 — serving a >arena file over TFTP (i365c)

The netboot server (`src/netboot/netboot_server.asm`) serves only **≤ 1518-byte**
files: `nb_fill_store` stages small CODE files into a RAM arena (`FILE_ARENA`) and
**skips every file ≥ 16 KB** (`NB_FILE_MAX=1518`, `nb_walk_entry:1409-1428`);
`send_next_data:812` reads each TFTP block from `SRC_PTR+offset`, a RAM pointer.
Both demo outputs are oversize, so today's server would skip them. The i95b-b1
"serve is hardware-proven" claim holds only for the small pi-standins.

**Large-file serve = stream from the record's sectors on demand.** The read
primitive already exists and is hardware-proven: `bd_record_read_hw`
(`src/netboot/sd_csd.asm:397`, i362, gated `NETBOOT_WANT_RECORD_READ`) does a real
CMD17 read of one record-body sector by linear sector, sharing `bd_record_lba_compute`
with the write path. Large-file serve is:

1. `nb_walk_entry`: for large plain-CODE files, instead of staging bytes, record a
   **disk-backed descriptor** `{name, start track/sector within the record, size}`.
2. `resolve_src` / `NB_SRC_TABLE`: flag disk-backed vs arena-backed entries.
3. `send_next_data`: for disk-backed files, follow the MGT sector chain from
   `XFER_OFFSET` (each sector = 510 data + 2-byte next-track/sector link), reading
   sectors via `bd_record_read_hw`, re-blocking the 510-byte data units into
   512-byte TFTP DATA blocks. Needs **full 32-bit** `XFER_OFFSET`/`XFER_SIZE`
   arithmetic — `send_next_data` is 16-bit today, fine for ≤64 KB but wrong for
   `release.src`.

**Coupling to Wall 1:** if the render sink writes `release.src` as a **contiguous**
run of sectors and records its start sector + length in the directory entry, the
serve side can read it **linearly** (no chain-following). This is exactly why
`i365c` and `i365d` are cohesive — the write layout the driver produces determines
the read layout the server consumes. The serve side should still handle the MGT
chain generally (HSAVE'd `release.img` is B-DOS-allocated), but the demo's own
`release.src` write should be contiguous to keep both sides simple.

## Emulation-first (CLAUDE.md rule 7) and the mock trap (i356)

Every path is exercised in the faithful harness `tools/netboot-oracle/z80/`
(real captured ROM + **B-DOS 1.5t** + the SPI SD model) before hardware. The
primitives are already proven there: `bd_record_write_hw` raw CMD24 + i295 guard
(`sd_record_write_guard_test.go`, runs in public CI), the store leaf reaching real
B-DOS CMD24 (`http_main_faithful_store_test.go`, `sd_push_faithful_test.go`), whole-file
hook read-back (`hook_roundtrip_faithful_test.go`, real 1.5t).

**Trap:** the `BDOSStore` mock (`bdos_store.go`) captures `SectorWrites` but does
**not** enforce B-DOS's `"BDOS"@232` validity gate — a mock-only test of the
directory-entry writer gives false confidence (the i356 lesson). The dir-entry
writer and the large-file serve must be validated on the **faithful** rig: assert
a subsequent `HGTHD("RELEASESRC")` returns the right length and `HLOAD`/`nb_fill_store`
reads the bytes back. This host has `~/sam-archive`, so `SKIP_PRIVATE_TESTS`
faithful tests run locally; public CI runs the mock + raw-CMD24-vs-SPI tests.

## Recommended slicing

- **`i365c` — large-file serve-from-disk.** Server-only, independently gateable
  (seed a record with a large file from an `.mgt` fixture via `seedRecordFromMGT`,
  boot the server, assert byte-exact TFTP serve under the long name). Read
  primitive (`bd_record_read_hw`) exists; new work is the disk-backed descriptor +
  MGT-chain/32-bit `send_next_data` + the NBMANIFEST long-name mapping (build-disk
  `buildManifest` / `-netboot-extra`, already built; `netboot_server_faithful_test.go`
  is the template).
- **`i365d-b1` (#864, DONE) — render sink → chunked write + MGT directory-entry writer.**
  The hard capability. `render_disk_sink.asm` re-blocks a byte stream into 510+2 MGT
  sectors written contiguously from linearSec 40 via `bd_record_write_hw` (raw CMD24 +
  i295 band guard) + a directory-entry writer; `render_out_append` dispatches through
  `RENDER_SINK_VEC` (default = the `&E8` harness port). Faithful-gated HGTHD/HLOAD
  read-back across every finish path + the 417374 B `release.src` size.
- **`i365d-b2` — the CODE-auto driver + compose the demo `.mgt` (umbrella).** See
  **Driver architecture** below: render, DEMO_ASM, and the server cannot be
  co-resident, so the demo runs them **sequentially**, split into leaves:
  - **`i365d-b2a` — render→disk bootable.** Load `release-unstripped.tbn` (DOS `IN`)
    from the boot record into the render IN pages 8..30, point `RENDER_SINK_VEC` at
    `render_disk_sink_byte`, `render_run` → `release.src` on the record. Gate:
    HGTHD/HLOAD `RELEASESRC` == `render.Emit`.
  - **`i365d-b2b` — assemble→disk bootable (DONE).** Compose `assembler-demo.bin`
    (DEMO_ASM) as the `AUTOasm` CODE-auto record vessel (`build-disk -code-auto
    -variant prod`), reading `release-unstripped.tbn` as DOS `IN`, with the prod
    HLOAD-by-name payloads (`enctab.enc`, `sd13`, `d15`, `zx013`) on the record.
    Unlike b2a, `release.img` is <64 KB, so DEMO_ASM's `save_out_file` HSAVEs it
    **directly through real B-DOS** — no raw-sector sink, no ENC/CSD come-up, no IM2
    takeover. And unlike render's 23-page IN, the assembler's `load_in_file` reads
    only a ≤6-page (≤96 KB) prefix via one trampoline HLOAD that auto-pages across
    those pages (`main_loop.asm:1560`; the `.tbn` editor/render tail never enters
    RAM) — so no MGT re-block either. The record is booted directly (no thin driver
    needed): the faithful gate stops at `print_status_string` (right after
    `save_out_file`), before the DEMO `ret` returns into ALHK. Gate
    (`assemble_disk_boot_faithful_test.go`, FAITHFUL rig): boot → AUTOasm → real-B-DOS
    IN-prefix load + assemble + HSAVE `RELEASEIMG` on the record, reconstructed from
    the record's MGT chain == `build/release-unstripped.img` (byte-matches GNU). This
    proves the multi-page prefix HLOAD works under an ALHK record boot on real B-DOS
    1.5t — the open question the b2a note flagged.
  - **`i365d-b2c` — overlay orchestrator + compose the demo `.mgt` (depends on b2a+b2b).**
    Sequences the phases with RST `&10` messages (`mgt_screen_demo_standalone` idiom),
    then serves both under long names via NBMANIFEST (`RELEASESRC→release.src`,
    `RELEASEIMG→release.img`). Compose via build-disk `-netboot-code-auto`
    (`netboot-server-record` template). Record stays HRECORD-selected through ALHK exec
    (`netboot_server_faithful_test.go:19`; else a one-time driver `HRECORD`).
- **`i365e` — store on a record + boot the real SAM + fetch both over TFTP.**
  sd-push (first-free, data-safe) → boot-record.py → host fetch; `release.img`
  byte-matches GNU, `release.src` = the rendered listing.

## Driver architecture (i365d-b2) — sequential overlays, not a fused binary

The three demo subsystems are each a near-/over-a-full `&8000`-window `org &8000`
image and **cannot be co-resident**: `assembler-demo.bin` (13.1 KB, `&8000-&B341`
section C + `&C100`-down boot stack + section-D scratch + off-axis pages 4-15),
`tbn_render_driver.bin` (21.3 KB, `&8000-&D31E` = section C **and** D + off-axis
pages 7-31), `netboot_server.bin` (19.2 KB, section C+D + IM2 `&FD00`, streams file
bodies from disk so no off-axis data pages). All three claim section C at `&8000`;
render+server each need C+D; render's off-axis 7-31 collides with the assembler's
4-15. So a single fused CODE-auto image is impossible.

The demo therefore runs the phases **in sequence, sharing only on-disk state** (the
record's B-DOS selection + the generated files): render streams `release.src` to the
record; DEMO_ASM HSAVEs `RELEASEIMG` to the record; the server serves both by reading
their sectors. Each phase reloads from disk what it needs — nothing survives resident
across phases except the on-disk files. The orchestrator (b2c) keeps its own code +
stack + return frame in a page **no** subsystem's exec window occupies (the #859
constraint generalized) and pages/CALLs each subsystem into the window in turn. The
one shared input is a single `release-unstripped.tbn` (`IN`): render reads it whole,
the assembler only the prefix (`main_loop.asm:1560`).

| Phase | Exec window (`&8000-&FFFF`) | Off-axis pages | Free off-axis |
|---|---|---|---|
| Render (b2a) | 2 physical pages (C+D) | 7, 8-30, 31 | 4, 5, 6 |
| Assemble (b2b) | section C code + section-D scratch/stack | 4, 5-12, 13, 14, 15 | 16-31 |
| Serve (b2c) | 2 physical pages (C+D) + IM2 `&FD00` | none (streams from SD) | 4-31 |

Precedents: the assembler record vessel (`-code-auto`, one `AUTOasm` file HLOADing
sibling payloads like `d15` and running them via `paged_call`) is the overlay model;
`render_disk_probe.asm` shows composing render-sink + B-DOS + SD in one `&8000` image;
`netboot-server-record` is the serve-vessel template. A >16 KB two-page CODE-auto
vessel is proven (the 20391 B `AUTOasm` boots green; the 18 KB `netboot_serve_record`
booted on real hardware).

## Key file map

| Piece | Location |
|---|---|
| Render sink to retarget | `src/tbn_render.asm:901` (`render_out_append`), port `&E8` |
| Chunked-write accumulator (reuse) | `src/netboot/raw_record_sink.asm` |
| Raw CMD24 write + LBA + band guard | `src/netboot/sd_csd.asm:504-567,606,674+` |
| Raw CMD17 record-sector read | `src/netboot/sd_csd.asm:397` (`bd_record_read_hw`) |
| Dir-region format + `"BDOS"@232` model | `src/netboot/http_main.asm:735` (`store_format_record`) |
| MGT dir-entry format the server reads | `src/netboot/netboot_server.asm:1344-1428` (`nb_walk_entry`) |
| Serve data path (16-bit today) | `src/netboot/netboot_server.asm:812` (`send_next_data`) |
| NBMANIFEST long-name map | `src/netboot/netboot_server.asm:1703`; `tools/build-disk/main.go:785` (`buildManifest`) |
| DEMO_ASM callable assembler | `src/assembler.asm:302,695`; `src/main_loop.asm:1878` |
| On-screen RST &10 messages | `src/netboot/mgt_screen_demo_standalone.asm` |
| Record-vessel test template | `tools/netboot-oracle/z80/netboot_server_faithful_test.go` |
| release-unstripped.tbn (DOS `IN`) | `Makefile:2202` (`release-unstripped-tbn`) |
</content>
</invoke>
