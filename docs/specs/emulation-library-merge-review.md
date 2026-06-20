# Go SAM-emulation libraries: shared vs duplicated logic, merge recommendation

## Purpose

This is the **i208** review — the gating input to the **i190** merge decision (and the
artifact that answers **q41**). It compares the project's two Go SAM-Coupé emulation
cores, identifies what is duplicated, what is one-sided, and recommends whether to
collapse the shared part into one importable package. It is a recommendation for
Pete to act on, not an implementation. The interim standing rule it is meant to
retire is `feedback_mirror_emulator_fixes` (mirror every shared-emulation fix across
both cores until they share one library).

Both cores run on the **same** vendored CPU, `github.com/koron-go/z80 v0.10.2`
(pinned identically in `tools/z80-test-harness-go/go.mod` and
`tools/netboot-oracle/z80/go.mod`), so Z80 *instruction* emulation is already shared
and is out of scope. The duplication is in the **SAM-system model around the CPU**:
memory/paging, ROM-vs-RAM, and the device seams.

## The two cores at a glance

| Dimension | Assembler harness (`tools/z80-test-harness-go`) | Netboot/oracle harness (`tools/netboot-oracle/z80`) |
|---|---|---|
| Go unit | `package main` (a standalone command + a test suite), own module | importable `package z80`, own module (nested go.mod) |
| Stated purpose | Boot the BUILD_TESTS / prod assembler, page payloads into pages 12-15, run boot self-tests + decode oracle (`README.md`) | Run `src/netboot/*.asm` routines, byte-compare against golden vectors; model Trinity seams (`README.md`) |
| CPU | koron-go/z80 v0.10.2 | koron-go/z80 v0.10.2 |
| Memory/paging | **inline** in `harness.go`: `resolveRead`/`resolveWritePage`/`resolveReadPage` + `In`/`Out` | **extracted** `sampage` package (`sampage/sampage.go`), a port of the harness pager |
| ROM-vs-RAM at boot | ROM1 at &C000 via LMPR bit6 (`harness.go:400,424,464`); a 32 KB fake ROM whose only live byte is the RST-8 stub at &0008 (`installFakeROM`) | ROM1 at &C000 via LMPR bit6 (`sampage.go:85,108`); **plus** a `romActive/romBase` overlay (`harness.go:128-133`) and `LoadROMImage` for a **real** captured 32 KB ROM (`harness.go:381-387`) |
| Device seams | printer (&E8/&E9), synthetic RST-8 hook-dispatch port (&FD), SAMDOS HGTHD/HLOAD/HSAVE + DOSER error model | ENC28J60 (&DC/&DD/&DE, `enc28j60.go`), flash EEPROM (`eeprom.go`), SD card (&DF, `sdcard.go`), B-DOS RST-8 hooks (`bdos_store.go`), keyboard sysvar/matrix injection |
| Diagnostics | PC ring-trace, windowed trace, trigger-PC backtrace, read-coverage map (`coverage.go`), unserved-file hints | exact T-state accounting (`tstates.go`), `Entry.Trace` per-instruction hook, INI/IND block-input port correction |
| Test surface | 50 `Test*` funcs: assembler boot self-tests, zx0 port, disasm oracle, synthetic-parity, release paged | 317 `Test*` funcs: netboot protocol (arp/udp/tftp/dhcp/tcp), TLS/crypto leaves, bdos store, samboot real-boot, editmodel, asmparse |
| Entry-point shape | `Run`/`RunWithFiles`/`RunConfig` — boot the assembler binary to HALT and capture OUT bytes | `Call`/`CallEntry`/`RunBoot`/`RunFrom` — call a *named routine* to its RET and inspect memory/registers |

## Duplication findings (cited)

The duplication is narrow and precise: **the LMPR/HMPR pager + ROM write-protect, and
nothing else.**

1. **Section decode (read).** `Hardware.resolveRead` (`z80-test-harness-go/harness.go:385-407`)
   and `Mem.Get` (`netboot-oracle/z80/sampage/sampage.go:72-90`) are the same function:
   - Section A: ROM0 unless LMPR bit5 set, else RAM page `LMPR&0x1F`.
   - Section B: RAM page `(LMPR&0x1F)+1 mod 32`.
   - Section C: RAM page `HMPR&0x1F`.
   - Section D: ROM1 if LMPR bit6 set, else RAM page `(HMPR&0x1F)+1 mod 32`.
2. **Section decode (write) + ROM write-protect.** `resolveWritePage` (`harness.go:411-430`,
   ROM sections return -1 = drop) ≡ `Mem.Set` (`sampage.go:95-113`, ROM sections `return`).
3. **Paging-port I/O.** `Hardware.In`/`Out` for &FA/&FB (`harness.go:482-516`) ≡
   `Mem.PortIn`/`PortOut` (`sampage.go:117-141`), including the identical HMPR quirk
   "preserve bits 5-7 (CLUT), write only the low 5" (`harness.go:498-500` vs
   `sampage.go:137`).
4. **Constants.** `pageSize=16384`, `numPages=32`, `romSize=32768`, `portLMPR=0xFA`,
   `portHMPR=0xFB` are declared in both (`harness.go:99-122` vs `sampage.go:26-49`).

`sampage`'s own doc comment states it outright: it is *"the faithful pager ported from
the assembler emulator (tools/z80-test-harness-go/harness.go resolveRead/resolveWritePage),
extracted into a reusable package … the down-payment on i190"* (`sampage.go:7-16`). So
this is not accidental drift — it is a deliberate, documented copy awaiting the merge.

**No other duplication exists.** The device seams do not overlap: searching the netboot
core for the assembler core's devices returns nothing (no printer, no DOSER, no
HGTHD/HSAVE file-serving model in `netboot-oracle/z80`), and the assembler core has no
ENC28J60/EEPROM/SD/Trinity ports (its only non-paging, non-printer port case is &FE
border, `harness.go:513`). The two RST-8 mechanisms are *different models of the same
vector*, not copies — see one-sided features below.

## One-sided features (the q41 concern: "accuracy features exist on one side only")

These are the substantive divergences. Several are genuine accuracy features present on
only one side — exactly the risk Pete flagged.

**Netboot-only accuracy features:**

- **Real captured ROM image.** `LoadROMImage` (`harness.go:381-387`) loads the actual
  patched 32 KB Trinity system ROM (`~/sam-archive/samboot-capture/rom.bin`, the i190a
  work) into `sampage.Mem.ROM`, so a boot trace fetches the *real* reset/report-50 code.
  The assembler harness has only a synthetic fake ROM (0xFF fill + a 7-byte RST-8 stub,
  `installFakeROM`, `harness.go:358-373`) — it cannot run real ROM code.
- **Boot ROM overlay.** `romActive`/`romBase` + `poke` drop-on-ROM (`harness.go:128-133`,
  `LoadBoot` `harness.go:308-336`) model "&C000-&FFFF is ROM1 at boot until the program
  pages RAM there", so an over-size boot image's tail is dropped and a call into it
  reproduces the real-hardware crash. The assembler harness has the LMPR-bit6 ROM1 model
  in the pager but no equivalent fixed-overlay loader for boot images.
- **Exact T-state accounting** (`tstates.go`, `instrTStates`) — cycle-exact per-instruction
  costing, the i102 crypto-optimisation prerequisite. The assembler harness has no T-state
  model at all.
- **INI/IND block-input port correction** (`harness.go:174-222`) — fixes a koron-go v0.10.2
  spec quirk for block I/O. Per `feedback_mirror_emulator_fixes` this is *correctly* N/A
  to the assembler harness (it has no block-I/O device and emits no INI/IND), so it is a
  legitimately one-sided device fix, not a gap.
- **Device seams**: ENC28J60 (`enc28j60.go`), EEPROM (`eeprom.go`), SD (`sdcard.go`),
  keyboard injection (`InjectKeys`/`PressEsc`), B-DOS sector hooks (`bdos_store.go`). These
  are netboot-domain devices, not general SAM accuracy — appropriately one-sided.
- **Pluggable RST-vector hooks** (`Machine.rstHandlers`, `harness.go:246-256`) — a generic
  RST-target dispatch the B-DOS store plugs into.

**Assembler-harness-only accuracy/diagnostic features:**

- **Read-coverage map** (`coverage.go`, `EnableCoverage`, `resolveReadPage`,
  `harness.go:434-470`) — physical (page, offset) coverage for the i111 dead-code finder.
  No netboot equivalent.
- **SAMDOS DOSER file-I/O error model** (`doserDispatch`, `harness.go:1135-1161`) — models
  ROM PTDOS's `JP (DOSER)` post-hook error dispatch, the i25 prerequisite. The netboot
  core's B-DOS RST handler models hook *dispatch* but not the DOSER error vector.
- **HGTHD/HLOAD/HSAVE file-serving + geometry** (`harness.go:819-1006`) — the named-file
  registry, DIFA geometry population, and OUT-byte capture. Netboot-domain irrelevant.
- **Sysvar seeding** (`seedSysvars`, PRAMTP, `harness.go:337-343`).
- **Rich diagnostics**: windowed PC trace, trigger-PC backtrace + memory dump
  (`RunConfig` `TraceLo/TraceHi/TrigPC`, `harness.go:637-698`), unserved-file hints.

The one accuracy feature that is one-sided **and** belongs to the shared concern (memory
model) is the **real ROM image + boot overlay**: the netboot core can run authentic ROM
code through the *same* paging model, while the assembler core's paging model is identical
but is fed only a fake ROM. That asymmetry sits on top of the duplicated pager, which is
the precise shape i190 targets.

## Recommendation: **partial merge — extract only the pager (+ its ROM model) to one shared package; leave the two harnesses distinct.**

This matches the refined i190 principle already recorded in the registry
(`registry/items.yaml` i190: *"the goal is NOT to merge the two harnesses … There must be
ONE Go SAM-Coupé emulation core … that BOTH harnesses USE"*). The findings above support
it on the merits:

1. **Would one merged *core* benefit both? Yes — but only the pager.** The pager is
   duplicated verbatim and is the exact code whose divergence let the i87a/&C000 ROM-paging
   bug reach hardware (one harness had the model, the other didn't). Sharing it makes every
   memory-model accuracy fix land in both at once and retires the mirror-fixes rule for the
   memory model. There is real, ongoing risk in keeping two copies.
2. **Are they fundamentally different services otherwise? Yes.** Everything *except* the
   pager is domain-specific and non-overlapping: assembler file-I/O/DOSER/coverage vs
   netboot ENC/EEPROM/SD/crypto/T-states. Merging those would add coupling for zero
   accuracy gain and would slow each side's iteration. The 50-vs-317 test split and the
   different entry-point shapes (`Run` a whole binary to HALT vs `Call` a named routine to
   RET) confirm they are distinct tools that should stay distinct.

So: **do not merge the harnesses; do unify the one duplicated implementation.**

### Concrete shared-package shape

- **Promote `sampage` to a shared, importable location.** It currently lives at
  `tools/netboot-oracle/z80/sampage` inside the netboot-oracle/z80 module, so the
  assembler harness (a separate module) cannot import it. Move it to a neutral module both
  can require — e.g. `tools/sampage` (its own go.mod), added to `go.work` and `require`d by
  both `tools/z80-test-harness-go/go.mod` and `tools/netboot-oracle/z80/go.mod`. (The
  netboot module would change its import path from the nested package to the new one.)
- **Move the boot-ROM model into the shared package** so the *single* memory model carries
  it: `LoadROMImage`/`ROM` already live in `sampage.Mem`; fold the `romActive`/`romBase`
  overlay (currently in netboot `mem`, `harness.go:128-133`) into `sampage` too, so the
  assembler harness gets authentic-ROM capability for free if it ever needs it. This is the
  one accuracy asymmetry on the shared concern; putting it in the shared package erases it.
- **What MOVES to shared:** the LMPR/HMPR section decode (read + write + ROM write-protect),
  the &FA/&FB port I/O with the HMPR-CLUT quirk, the page/ROM constants, and the boot-ROM
  overlay. That is the whole of `sampage.go` plus ~6 lines from netboot `mem.poke`.
- **What STAYS separate:** *all* devices and diagnostics. Assembler side keeps its inline
  printer/&FD-hook/HGTHD-HLOAD-HSAVE/DOSER/coverage in `harness.go`. Netboot side keeps
  ENC28J60/EEPROM/SD/bdos_store/keyboard/T-states. Each harness keeps its own `Hardware`/
  `mem` struct that **embeds or wraps** the shared `sampage.Mem` and funnels every RAM/ROM
  access + paging port through it (the netboot core already does this — `mem.pager`,
  `peek`/`poke`, `harness.go:114-233`; the assembler core would be refactored to the same
  shape, replacing its inline `resolveRead`/`resolveWritePage`/`In`/`Out` paging cases with
  delegation to the shared `Mem`).

This is precisely the i190 acceptance criterion: *"the SAM CPU+memory+paging+ROM model
exists in exactly one place that both harnesses import; both existing test suites pass; the
harnesses may remain separate packages/modules."*

## Effort / risk

- **Effort: small–moderate.** `sampage` already exists and is already used by the netboot
  core, so the netboot side is mostly an import-path change. The assembler side is a
  mechanical refactor of four functions (`resolveRead`, `resolveWritePage`,
  `resolveReadPage`, the &FA/&FB cases of `In`/`Out`) to delegate to the shared `Mem`,
  preserving the existing `Hardware` struct's other fields and behaviour. The coverage
  hook (`resolveReadPage` → `cov.mark`) needs a thin wrapper around the shared decode but
  is otherwise unaffected.
- **Risk: low, with one watch-point.** Both test suites are the safety net (50 + 317
  `Test*`). The watch-point is the **assembler harness is `package main`**: importing a
  shared library is fine for a `main` package, but care is needed that the standalone
  binary (`main.go`) still builds and that no constant collides on rename. The HMPR-CLUT
  preservation and the section-D ROM1 toggle must be byte-identical after the move — they
  already are, so a straight extraction with the existing tests green is sufficient proof.
- **Module plumbing**: a new shared module under `go.work` plus a `require` in each
  consuming module; no behaviour change, just wiring.

### Does the merge retire `feedback_mirror_emulator_fixes`?

**Yes, for the memory model — which is the whole point of that rule.** The mirror-fixes
rule exists because the pager is duplicated; once the pager (and its ROM model) live in one
imported package, a pager fix lands in both harnesses automatically and the rule is moot.
Device fixes were never duplicated (ENC/EEPROM/SD/DOSER/coverage each exist on one side
only), so they were already outside the rule. After i190, the rule can be deleted.

## Decision needed from Pete

1. **Approve the partial merge** (extract the pager + boot-ROM model to one shared package;
   keep the two harnesses as distinct tools) — this is the i190 scope as already written.
   *Recommended.*
2. **Confirm the shared-package location/module name** (proposal: a new `tools/sampage`
   module, added to `go.work`, required by both harness modules). The alternative is to keep
   `sampage` where it is and have the assembler module require the netboot-oracle/z80 module
   — workable but couples the assembler harness to the netboot module's dependency graph, so
   a neutral module is cleaner.
3. **Confirm that the boot-ROM overlay (`romActive`/`romBase`) should move into the shared
   package** (vs staying netboot-local). Recommended to move it, so the single memory model
   carries the only one-sided *memory-model* accuracy feature.
4. On i190 completion, **delete the `feedback_mirror_emulator_fixes` memory entry** and its
   pointers (it is superseded by the shared import).
