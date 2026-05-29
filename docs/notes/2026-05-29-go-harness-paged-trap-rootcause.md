# Go test-harness "paged-path trap" — root cause (2026-05-29)

Date: 2026-05-29
Branch: `m7-harness-paged-trap`
Scope: `tools/z80-test-harness-go/` only. SimCoupé remains the sole CI gate; this is an inner-loop dev tool.

## Outcome (TL;DR)

**Root-caused and fixed.** The trap is **not** a 6-page paged-IN HLOAD fidelity gap. The multi-page IN load is faithful: with the correct inputs the harness runs the full 88 644-byte (6-page) spectrum4 release `.tbn` to completion and produces OUT **byte-identical** to the vendored `tests/m6/release/release.img` (21 752 B).

The trap was a **missing-input misconfiguration that failed silently**: the standalone runner was invoked without `-sysreg-data`, so physical page 13 (the sysreg lookup data the prod assembler *unconditionally* loads at boot) stayed empty. The first sysreg/`dc`/`tlbi`/pstate operand in the release source then dispatched into an empty page-13 matcher, which ran off the end of section C into the empty section D and wrapped to the `RST 38` vector at `&0038`.

The fix makes the harness **name the unserved file in the trap message** instead of leaving a cryptic `&0038` spin, and documents that the prod assembler requires `-sysreg-data`.

## The symptom

Running the prod assembler against the full release `.tbn` with no other inputs:

```
$ /tmp/z80-harness -assembler build/assembler-prod.bin -enctab build/enctab.enc -in /tmp/release.tbn
Exit:  TRAP: PC spinning at &0038 (jumped into 0xFF fake-ROM) after 1142441 steps
Regs:  PC=0038 SP=7E7E AF=0D08 BC=0000 DE=800D HL=7E66 IX=4B00 IY=0000 LMPR=1F HMPR=0D
```

`release.tbn` is 88 644 B = 6 IN pages (7..12). SimCoupé runs this exact input cleanly (the M6 byte-match), so the harness has a divergence.

## Trace evidence

Triggering on `PC=0x0038` and dumping the 200-PC ring buffer at the moment of the trap (the harness's own `-trig` facility) gives the path *into* the trap:

```
=== TRIGGER PC 0038 hit at step 1142378 ===
Regs at trigger: PC=0038 SP=7EFC ... HMPR=0D
Backtrace (oldest first):
  ... FFDA FFDB FFDC ... FFFD FFFE FFFF 0000 0038
```

Two facts pin it down:

- **`HMPR=0x0D` (= 13).** Section C maps physical page 13, section D maps page 14. This is the state the `paged_call` helper (`src/paged_bodies.asm`) leaves the machine in while it runs a page-13 target — i.e. the sysreg matcher dispatched from `src/sysname.asm` (`SYSREG_*_ENTRY` at `&8000` in page 13).
- **`SP=0x7EFC`** is in `TRAMP_SAFE_SP` territory (`&7F00 - 4`, section B), exactly where `paged_call` parks the stack during a paged target. So the trap is inside a `paged_call` into page 13, not in normal section-C/D code.

The backtrace `&FFDA → … → &FFFF → &0000 → &0038` is the CPU executing a slide of `0x00`/`0xFF` bytes through section D (page 14, which the harness leaves zero/`0xFF`), running off the top of the address space, wrapping to `&0000`, and immediately hitting the `RST 38h` opcode (`0xFF`) at `&0038`.

Why a slide? Page 13 in this run is all zeros (`0x00` = Z80 `NOP`). The matcher entry at `&8000` is therefore a 16 KB `NOP` slide that flows from section C (`&8000..&BFFF`, page 13) straight into section D (`&C000..&FFFF`, page 14) and off the end. There is no matcher code there because **`sysreg_data.bin` was never loaded** — `-sysreg-data` was omitted.

## Root cause

The prod assembler **unconditionally** HLOADs the sysreg lookup data into page 13 at boot:

- `src/assembler.asm:402` — `call load_page13_payload`, unconditional (outside the `BUILD_TESTS` block; the comment at `:393-401` is explicit that this is *the only* place page 13 is populated in a production build).
- `src/loader.asm:242-264` — `load_page13_payload` does `HGTHD` for SAMDOS file `"sd13"` then HLOADs it into `SYSREG_DATA_PAGE` (= 13) via the trampoline.
- The page-13 matcher is then invoked for the first sysreg/`dc`/`tlbi`/pstate operand via `paged_call` (`src/sysname.asm:376` ff., `src/trampoline.asm:358-367`).

The harness's HGTHD handler, when asked for a file it has neither in its registry nor recognises as a known pre-deposited legacy file, took the "Unknown file → leave `currentFile` nil → HLOAD is a no-op" path (`tools/z80-test-harness-go/harness.go`, the `else` branch of the HGTHD handler, ~line 645 pre-fix). That branch is *correct* for `enctab.enc` (pre-deposited into page 4) and the legacy `IN` path (pre-deposited into pages 7..). But for `"sd13"` it silently left page 13 empty.

On real SAM, `HGTHD` for a file that isn't on the disk does a SAMDOS "file not found" longjmp to a FAIL banner — a *loud* failure. The harness stub doesn't model that longjmp; it degraded to a silent no-op, so the missing payload only surfaced ~1.1 M steps later as the `&0038` garbage trap, which looked like a deep paging bug.

**The 6-page paged-IN HLOAD itself is faithful** — see verification below: supply `sd13` and the same 6-page input assembles to a byte-exact release image. So this finding *closes* the "trap at scale" open question from `docs/notes/2026-05-29-m6-bytematch-encoder-divergences.md` ("Go-harness fidelity follow-up"): there is no multi-page paging bug to fix.

## The fix (applied here)

Minimal and low-risk — the paging/HLOAD emulation is untouched (it's proven correct by the byte-match). The change is purely diagnostic:

1. `harness.go` — the HGTHD "Unknown file" branch now records any requested name that is neither registered nor the known pre-deposited `enctab.enc` (the legacy `IN` path is handled by an earlier branch and is genuinely pre-deposited). The list is deduplicated and surfaced as `Result.UnservedFiles`.
2. The `&0038` trap message (and the timeout message) now append a hint naming the unserved file(s), with a specific remedy for `"sd13"` ("supply build/sysreg_data.bin via -sysreg-data: the prod assembler always HLOADs \"sd13\" into page 13 at boot").
3. `main.go` prints `Unserved HGTHD files: …` on any run that had unserved files.
4. `README.md` documents that prod runs must pass `-sysreg-data`.

Post-fix the same bad invocation now self-explains:

```
Exit:  TRAP: PC spinning at &0038 (jumped into 0xFF fake-ROM) after 1142441 steps;
       HGTHD requested file(s) the harness could not serve: [sd13] (supply
       build/sysreg_data.bin via -sysreg-data: the prod assembler always HLOADs
       "sd13" into page 13 at boot); the matching HLOAD was a no-op, so that
       physical page is empty
```

## Verification

```
make text2bin m3-asm-prod enctab sysreg-data
build/text2bin -flatten -strip-comments -origin 0xfffffff000000000 \
    -o /tmp/release.tbn tests/m6/release/release.s          # 88 644 B = 6 pages

cd tools/z80-test-harness-go && go build -o /tmp/z80-harness .

# WITH sysreg-data: completes + byte-matches the vendored oracle.
/tmp/z80-harness -assembler ../../build/assembler-prod.bin -enctab ../../build/enctab.enc \
    -sysreg-data ../../build/sysreg_data.bin -in /tmp/release.tbn
#   Exit: HALT at PC=B7E1   Printer: "OK"
#   (OUT extracted and `cmp`'d vs tests/m6/release/release.img → byte-match, 21 752 B)

# WITHOUT sysreg-data: traps, now with the sd13 hint (shown above).
```

- `go build ./... && go vet ./... && go test ./...` in `tools/z80-test-harness-go/` — all green.
- New regression test `release_paged_test.go` (`TestReleasePagedInLoad`) asserts both halves: with `sd13` → OK + byte-match `release.img`; without → trap with `sd13` in `UnservedFiles`. It builds the `.tbn` from the vendored `tests/m6/release/release.s` so it is self-contained (no external spectrum4 checkout needed). Skips cleanly if build artefacts are absent.

The harness is not a CI gate, so none of this affects the SimCoupé matrix.

## Why this was easy to miss

`-sysreg-data` is only *needed* by sources that contain a sysreg/`dc`/`tlbi`/pstate operand. Every M3–M6 fixture in the corpus either supplies `sd13` (the sweep / test-variant tests do) or doesn't use those operands, so 36/36 fixtures passed without anyone noticing the standalone runner's silent-no-op trap on a sysreg-using source. The full release source is the first thing driven through the standalone runner that both (a) uses sysreg operands and (b) was run without `sd13`.

## Files

- Root cause: `src/assembler.asm:402`, `src/loader.asm:242-264`, `src/trampoline.asm:358-367`, `src/paged_bodies.asm` (the `paged_call`/HMPR=13 dispatch state).
- Fix: `tools/z80-test-harness-go/harness.go` (HGTHD unserved-file tracking + `unservedFileHint` + `Result.UnservedFiles`), `tools/z80-test-harness-go/main.go` (print unserved files), `tools/z80-test-harness-go/README.md`.
- Regression test: `tools/z80-test-harness-go/release_paged_test.go`.
