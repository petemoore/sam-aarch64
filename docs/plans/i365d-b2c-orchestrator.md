# i365d-b2c — the capstone overlay orchestrator (plan)

Completes the i365 demo: ONE boot renders `release.tbn`→`release.src`, assembles
`release.tbn`→`release.img`, and serves both over TFTP. Design home:
`docs/specs/i365-demo-architecture.md`. Deps `i365d-b2a` (render→disk, #865) and
`i365d-b2b` (assemble→disk, #866) are DONE.

This is a large incremental item — one feature branch (`i365d-b2c`), merged once
when the faithful **end-to-end** gate is green (CLAUDE.md rule 5). Internally
phased; the branch may span sessions. `i365d-b2c` cannot be `split` (id-grammar
nesting limit), so it stays one item / one completing PR.

## Architecture — CHAIN of overlays (not a resident orchestrator)

The three phases are each an `org &8000`, >free-RAM vessel (render ~25 KB C+D,
assembler 13 KB C, server ~19 KB C+D) and cannot co-reside. They run **in
sequence, sharing only on-disk state** (the record's B-DOS selection + the
generated files). Chosen realization: a **chain**, no central orchestrator —
each overlay, at its end, HLOADs the next overlay over the `&8000` window and JPs
in. This sidesteps the self-relocation problem a resident orchestrator would need
(an `org &8000` controller cannot survive an HLOAD into its own window).

Boot AUTO file (ALHK-loaded) = the **render** vessel. Chain:

1. **render** (`render_disk_boot.asm`, `-D DEMO_CHAIN`): come-up (EEPROM/ENC/CSD)
   → HLOAD disasm→p31, reblock IN→p8..30, render `RELEASESRC` to the record via
   the raw-CMD24 sink. Then instead of `rdb_done: di;halt`, **chain to `asmdemo`**.
2. **assembler-demo** (`assembler.asm`, `-D DEMO_ASM -D DEMO_CHAIN`): assembles
   the IN prefix, HSAVEs `RELEASEIMG` through real B-DOS. Then instead of
   `ld sp,(saved_caller_sp);ret`, **chain to `nbsrv`**.
3. **server** (`netboot_server.asm`, unchanged): its own come-up, walks the record
   dir (finds RELEASESRC + RELEASEIMG + NBMANIFEST), serves both **disk-backed**
   under long names (`RELEASESRC→release.src`, `RELEASEIMG→release.img`) forever.

The server needs **no changes** — it already self-comes-up and streams large
disk-backed files (i365c). NBMANIFEST is composed onto the record by build-disk.

## The overlay-load mechanism (the one genuinely new capability)

A `<=32 KB` HLOAD to load-address `&8000` fills sections C+D (the two currently
mapped physical pages) with **no** per-16 KB HMPR-advance wrap — this is exactly
how ALHK's ROM1 continuation loads the 25 KB render vessel (proven by b2a). The
wrap that broke render's 23-page IN load only happens **past `&FFFF`** (>32 KB).
So a runtime HLOAD of any single overlay (≤~25 KB) to `&8000` is sound.

The HLOAD must run from **outside** the `&8000` window (it overwrites the caller).
Pattern (mirrors render's `rdb_tramp_src` LDIR technique):

- **Resident** overlay-tail code (still mapped, runs before the jump) arms the
  lookup: `bdos_name_to_uifa(next_name)` + `bdos_lookup_hook` (HGTHD → DIFA, page
  count), and restores the **boot LMPR** (ROM0 in section A, B-DOS sysvar page in
  section B) + `di` + `im 1` so the loaded overlay boots into a clean B-DOS state.
- Copy a tiny **straight-line** loader stub to low RAM (section B, e.g. `&6000`,
  clear of B-DOS sysvars ≤`&5FFF` and of render's trampoline slots `&7E00+`). The
  stub is position-independent in the `rdb_tramp_src` sense: no self-relative
  jumps, only absolute fixed-slot / `&8000` targets.
- `jp` the stub. The stub: sets HMPR to the window's section-C page, `rst 8
  / defb BD_HOOK_HLOAD` (loads DIFA's file to `&8000`), `di`, `jp &8000`.

Chain target names on the record (build-disk store names): render's AUTO name TBD;
assembler CODE = `asmdemo`; server CODE = `nbsrv`. disasm.bin is shared (render
p31, assembler d15/p15).

## Phasing (internal; still one item / branch / PR)

- **Phase A — render→assemble sequencing.** render chains to assembler; assembler
  (Phase-A build) ends at a `di;halt` completion barrier (not chaining to server
  yet). Gate: boot the composed record on the faithful rig → reconstruct BOTH
  `RELEASESRC` (==`render.Emit`) and `RELEASEIMG` (==GNU `release.img`) from the
  record's MGT chain. Proves the overlay-load + two-phase sequencing.
- **Phase B — serve + long names + messages + e2e.** assembler chains to the
  server; NBMANIFEST composed; on-screen RST&10 messages ("Generating source…",
  "Generating image…") between phases (screen-safe only post-come-up — verify on
  the rig; b2a note truth #5: RST&10 hangs *at come-up* under an ALHK record boot).
  Gate: boot → render → assemble → serve → TFTP-fetch both == expected.

## Build composition (build-disk)

One record via `build-disk` carrying: the render AUTO vessel (`-netboot-code-auto`),
plus extra CODE files `asmdemo`, `nbsrv`, `disasm`, `enctab`, `sd13`, `zx013`,
`IN=release-unstripped.tbn`, and (Phase B) the NBMANIFEST entries
`RELEASESRC→release.src`, `RELEASEIMG→release.img`. Reconcile the b2a
(`-netboot … -netboot-extra`) and b2b (`-variant prod -code-auto` positional)
composition styles — build-disk may need a mode that places multiple `&8000`
overlays + the prod payload set + a netboot manifest on one record.

## Files

- `src/netboot/render_disk_boot.asm` — add the `DEMO_CHAIN` tail (chain to asmdemo).
- `src/assembler.asm` / `src/main_loop.asm` — add the `DEMO_CHAIN` tail (chain to nbsrv).
- New shared chain-stub include (e.g. `src/netboot/overlay_chain.inc`) if the stub
  is identical across overlays.
- `Makefile` — `assembler-demo-chain.bin`, render `DEMO_CHAIN` build, the demo
  record target(s), and `netboot-z80-artifacts`.
- `tools/build-disk/main.go` — multi-overlay + manifest composition if needed.
- `tools/netboot-oracle/z80/demo_orchestrator_faithful_test.go` — Phase A + B gates.

## Emulation-first

Every path runs on the faithful rig (`tools/netboot-oracle/z80/`, real ROM +
B-DOS 1.5t + SPI SD) before hardware; `SKIP_PRIVATE_TESTS` gates the private
captures. The on-hardware shot is `i365e` (separate).

## Session-1 progress + the blocker (2026-07-05)

**Built + working:** the overlay-chain mechanism (`render_disk_boot.asm`
`-D DEMO_CHAIN`: `rdb_chain_next` + `rdb_chain_stub_src`), the demo record
composition (`Makefile` `netboot-render-chain` + `netboot-demo-orchestrator-record`
— render AUTOrdb + asmdemo + IN + disasm/d15 + enctab.enc/sd13/zx013), and the
Phase-A faithful gate (`demo_orchestrator_faithful_test.go`). Verified on the rig:
render **completes** (phase='5' verdict='P', RELEASESRC streamed), reaches
`rdb_chain_next`, restores the pristine boot LMPR, and invokes the `asmdemo`
lookup. b2a's gate still passes (the chain code is fully `DEMO_CHAIN`-gated).

**THE BLOCKER — B-DOS SD-driver coexistence.** `rdb_chain_next`'s `HGTHD(asmdemo)`
(`bdos_lookup_hook`, `rst 8`) **hangs in B-DOS's SD read** (ROM ~`&1DxE`, phase
marker stuck at 'C'), after the render's raw CMD17 (rdb_load_tbn reblock) + CMD24
(sink) ops. Ruled out by targeted experiments this session:
- **Not the directory clobber.** Writing RELEASESRC's dirent to an *empty*
  linearSec (5), leaving the directory 100% intact, still hangs. (But the clobber
  IS a latent shared-card data-safety bug — `render_disk_write_dirent` zeroes the
  whole linearSec-0 sector; fix it when unblocked: append into the first free dir
  slot, preserve existing entries + the `BDOS`@232 stamp.)
- **Not the LMPR section B.** Restoring the pristine boot LMPR captured at
  `rdb_main` entry (`rdb_bdos_lmpr`, ROM0 in A + sysvar page in B) — kept, it is
  correct — did not unblock.
- **Not the physical card / no static buffer overlaps sysvars.** `csd_set_bd_records`
  (our raw driver) reads the card fine at chain time *without hanging*; RDS buffers
  are at `&9888`/`&9A88` (section C), nothing maps into `&4B00–&5Cxx`.
- Come-up's `HGTHD(disasm)` works (before any raw CMD17/24); the chain's HGTHD is
  the **first raw-SD → B-DOS-SD transition in one boot** anywhere in the codebase.

**Deepened this session (all ruled out):**
- **The FIRST B-DOS SD op of any kind hangs** — added an HRECORD re-select
  (`bdos_select_record`) before the HGTHD; `rdb_phase` stays '5' (its post-HRECORD
  marker never runs), so **HRECORD itself wedges**, not just HGTHD. So it is NOT a
  directory-read / device-dispatch specific issue — B-DOS's *SD access* wedges.
- **The harness SD card model is clean after our ops.** `sdReset` (our `&04`
  all-deselect bracket, `sdcard.go:476`) clears `deSynced` AND `woken`; `woken`
  only bumps a stat counter (`sdcard.go:627`), gates nothing; `StuckBusy` is set
  only in unit tests, never by the model; the `&DC` bit-3 BUSY is time-based and
  clears as `tNow` advances. So the card model is NOT de-synced/busy/stuck.
- The hang is a ~72-byte ROM loop at ~`&1Dxx` (a B-DOS SD busy-poll or an FDC
  poll reached via ROM), spun billions of times — a *permanent* wedge, not a
  settle. B-DOS `HGTHD`/`HRECORD` read code (real 1.5t, running in the faithful
  rig) never satisfies its poll after the render's raw ops.
- The B-DOS investigation's headline (rule-8 authority): B-DOS's steady-state SD
  ops **rely on boot-time SPI-mode persistence and do NOT re-init** (only HDINIT
  `&38`s, at `&A623`); our raw READ path (`bd_cmd17_read_lba`, `csd_read`) re-runs
  `sdc_init_ladder` (the `&38` wake) every call — the WRITE core is already
  init-once (`bcw_reselect`, no `&38`). Full report in the commit / session log.

**Next step (fresh session):** trace WHY B-DOS's first SD op never satisfies its
poll here. Two concrete threads: (a) instrument the harness `&DC` IN handler
(`enc28j60.go`, the status-byte + `settleUntilT`/`sdInitBusyUntilT`/shared-latch
logic) to log what B-DOS's poll reads at chain time vs at the working come-up
HGTHD — the delta is the cause; (b) try the investigation's PREFERRED fix — make
the raw READ path **init-once** (re-select `&31`, NO `&38`) like the write core,
so the shared-PIC `&38` disturbance is never introduced, then re-test. If the
harness truly does not model the real SPI-mode-persistence dependence, that is a
harness faithfulness gap to close (rule 7) — model what makes B-DOS's poll wedge.
This is the crux of Phase A; assemble + serve are wired and waiting on it.

**Fallback if intractable:** assemble-FIRST ordering (assembler boots pure-B-DOS,
chains to render last) structurally avoids raw→B-DOS, but has a body-sector
collision (render's fixed `RDS_DATA_BASE=40` write overruns B-DOS's HSAVE'd
RELEASEIMG) that would then need solving — likely harder than the SD resync.

Current state: `TestDemoOrchestratorFaithful` is RED (fails fast at ~200M steps /
~20s, not a hang) on branch `i365d-b2c`. Not merged — the Phase A gate is the
merge bar (CLAUDE.md rule 5).
</content>
