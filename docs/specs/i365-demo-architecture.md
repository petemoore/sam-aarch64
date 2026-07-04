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
it when the demo ships.

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
- **`i365d-b1` — render sink → chunked write + MGT directory-entry writer.** The
  hard capability. Retarget `render_out_append` to `raw_record_sink` + `bd_record_write_hw`,
  write the named contiguous MGT file, gate on the faithful rig (HGTHD/HLOAD
  read-back).
- **`i365d-b2` — the CODE-auto driver + compose the demo `.mgt`.** Orchestrate
  render→save→assemble→save→serve with RST `&10` on-screen messages (pattern:
  `src/netboot/mgt_screen_demo_standalone.asm`), calling `DEMO_ASM`'s `start:`
  (saves caller SP, HSAVEs `RELEASEIMG`, `ret`s — keep the caller stack + return
  frame OUTSIDE the assembler's `&C100`-down boot stack and its `&8000` image, per
  the #859 review). Both render and assembler read ONE `release-unstripped.tbn`
  (DOS file `IN`; the assembler loads only the prefix, `src/main_loop.asm:1560`).
  Compose via build-disk `-code-auto` / `-netboot-code-auto` (`netboot-server-record`
  is the closest template). A record boot REQUIRES a CODE-auto vessel (i332).
  Hardware-confirm a record boot leaves its record selected for HSAVE; if not, the
  driver does a one-time `HRECORD` of the boot record first.
- **`i365e` — store on a record + boot the real SAM + fetch both over TFTP.**
  sd-push (first-free, data-safe) → boot-record.py → host fetch; `release.img`
  byte-matches GNU, `release.src` = the rendered listing.

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
