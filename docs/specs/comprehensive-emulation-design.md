# Comprehensive emulation — one faithful SAM layer used by every test (i126)

**Status:** decision spec — awaiting Pete's call on the strategic open decisions (q58)
before the multi-increment implementation begins. Direction is blessed
(`feedback_comprehensive_emulation`, Pete 2026-06-23); the *approach, fork strategy,
and timing* are the open calls this doc frames.

## The problem (why i126 exists)

We emulate the SAM with **two partial layers**, each modelling half the machine, so a
change can pass one and fail the other — or pass both and fail on silicon:

| Layer | Trinity (ENC/EEPROM/SD/B-DOS) | Screen / line-interrupts / sound / exact timing | Boots from address 0 (real ROM) |
|---|---|---|---|
| `tools/z80-test-harness-go/` (assembler harness) | ✗ | ✗ | ✗ (fake ROM + RST-8 stub) |
| `tools/netboot-oracle/z80/` (netboot harness) | ✓ (i235: one shared PIC) | ✗ | ✓ (`samboot_real_boot_test.go`) |
| **SimCoupé** (the sole CI gate, `tools/Dockerfile.dev`) | ✗ | ✓ | ✓ |

The concrete failure this causes (Pete, 2026-06-23): a routine that is a *verbatim ROM
copy* of the rainbow-stripe drawer **passed** the screenless Go harness and went
**flat-yellow on real hardware** — the stripes are rendered by the live line-interrupt
handlers, which no Go harness models. The symmetric gap is the reverse: SimCoupé has the
screen but **no Trinity devices**, so it cannot run any netboot/storage path end to end.
The recurring cost is hardware-debug detours for bugs a faithful emulator catches at
build time (i82 `client_main`; the yellow screen).

**North star (Pete):** ONE comprehensive emulation = fork SimCoupé (the most complete
SAM — real ROM, boot from 0, screen, timing, sound, line-interrupts) and **port the
Trinity device models into it**, so a single layer captures Trinity + network + storage
+ screen + timing + sound *and their interactions*, boots from address 0, and is used by
**every** test. Then cross-subsystem surprises fail in emulation, not first on silicon,
and the `NETBOOT_HOSTTEST` carve-outs (i231) are deleted. Stop the "SimCoupé port is too
much work" / "merging the emulators is overkill" shortcuts.

## What is already decided (do NOT re-design)

- **Shared pager (i190, done).** `tools/sampage/` is the shared SAM pager (LMPR/HMPR +
  ROM write-protect); `netboot-oracle/z80` already imports it. (The assembler harness has
  not yet migrated to it — out of scope here.) Ref: `docs/specs/emulation-library-merge-review.md`.
- **Trinity fidelity = one shared PIC (i235, done, PR #672).** `enc28j60.go` models the
  Trinity microcontroller as one shared device (MUX select, BUSY gate, auto-null,
  shared read-back latch, ENCINT, RX filter, 27-byte network record), not three
  independent chips. Ref: `docs/specs/trinity-emulation-fidelity.md`. The C++ port mirrors
  **this** model.
- **SimCoupé stays the CI gate** (`docs/ARCHITECTURE.md` §8). i126 makes it *more
  complete*; it does not replace it.
- **Boot-from-address-0 fidelity principle (i232).** Tests should boot from reset with
  real ROM, not hand-seeded state. A faithful SimCoupé already does this natively.

## The Trinity device code to port (the concrete payload)

From `tools/netboot-oracle/z80/` — ~2,700 lines of Go state machines → C++, validated
against the same vectors the Go fidelity battery uses:

| Model | File | LOC | Hooks (SAM I/O ports) |
|---|---|---|---|
| ENC28J60 + shared PIC | `enc28j60.go` | 1,345 | Trinity ports `&DC–&DF` (SPI/MUX) |
| 25LC1024 EEPROM | `eeprom.go` | 218 | shared SPI bus |
| SD/MMC card | `sdcard.go` | 621 | shared SPI bus |
| B-DOS storage | `bdos_store.go` | ~510 | RST-8 hooks / record map |

SimCoupé source is **not vendored** — it is git-cloned from `github.com/simonowen/simcoupe`
at a pinned SHA and CMake-built inside `tools/Dockerfile.dev`. The port adds Trinity port
dispatch (`&DC–&DF`) to SimCoupé's I/O handler and wires the three SPI devices + B-DOS
seam as new peripherals behind it.

## The strategic open decisions (q58 — Pete's call)

These are genuinely fundamental and gate the whole effort; the agent should not pick them
unilaterally:

1. **Fork strategy.** A **private maintained fork** (e.g. `~/git/simcoupe-trinity`,
   rebased on upstream), an **in-repo vendored copy + patch series**, or **upstreaming a
   Trinity peripheral to `simonowen/simcoupe`** so it lives in mainline (Pete knows Simon
   Owen personally — `project_sam_community_contacts`)? This determines maintenance cost,
   how CI builds the emulator, and whether the work is public.
2. **Timing / sequencing.** Start now, or after the current bootblock / netboot-autonomy
   work? Today the project treats i126 as a **future** north-star: i232 is "FUTURE … must
   not block current work," and i241–i254 are "gated on the i126 comprehensive-emulation
   north star." A 500–1000 hr effort wants an explicit go and a slot in the roadmap.
3. **Retire the flat Go harness, or keep it?** Recommendation: **keep** it for ~1 ms
   dev-iteration speed (it is not a gate), but make the *unified SimCoupé+Trinity* layer
   the authority every CI test runs through. SimCoupé wins every disagreement (unchanged).

The agent's recommended default, if Pete wants one: **upstream-first** (offer Simon a
Trinity peripheral; fall back to a maintained private fork if upstreaming isn't wanted),
**deferred** until the netboot-autonomy line (i133/i194/i264) is past its current push,
and **keep** the Go harness for speed. But this is Pete's to set.

## Proposed decomposition (sketch — finalised into registry items once q58 is answered)

Ordered, individually-buildable increments, each landing under SimCoupé's existing CI
gate, each gated so the next can't regress it:

1. **Fork scaffold + CI.** Stand up the chosen fork form; CI builds it; the full existing
   assembler round-trip + release-gate still pass through it unchanged (proves the base is
   sound before any Trinity code).
2. **Trinity port dispatch + EEPROM.** Add `&DC–&DF` SPI/MUX dispatch and port
   `eeprom.go` (smallest device); first end-to-end test: SimCoupé boots from 0 and reads
   the Trinity network record from EEPROM (the i96 "Trinity Network" read, now under the
   screen-bearing emulator).
3. **ENC28J60 + shared PIC.** Port `enc28j60.go` (the heavy lift); reproduce the ARP /
   TFTP serve paths end to end under SimCoupé, including the PHY link-up timing that the
   flat harness needed `NETBOOT_HOSTTEST` stubs for.
4. **SD/MMC + B-DOS seam.** Port `sdcard.go` + `bdos_store.go`; CSD read (i145), record
   list, raw-record store/serve under the unified layer.
5. **Migrate tests + delete carve-outs (i231).** Move the netboot/boot-wrapper tests onto
   the unified layer; delete the `NETBOOT_HOSTTEST==0` carve-outs (17 files) as each path
   gains a real screen-bearing, Trinity-bearing home; add the CI ratchet that fails on a
   reintroduced carve-out.
6. **Boot-from-0 default (i232).** Adopt the reset-then-run helper as the default across
   the migrated tests.

i231 and i232 fold into increments 5 and 6; i124 (DONE) is the precursor. i241–i254 (the
emulation-gap corrections currently gated on this north star) are revisited as each device
lands under the unified layer.

## Why this is a spec, not yet a plan

Per the development discipline (spec gate), a non-trivial design gets Pete's approval
before implementation. The strategic decisions (q58) change the shape of increment 1
materially (a private fork, a vendored patch series, and an upstream PR are different
first commits). Once q58 is answered, this sketch is turned into a registry umbrella
(`i126` → ordered child increments) and increment 1 becomes the workable tip.
