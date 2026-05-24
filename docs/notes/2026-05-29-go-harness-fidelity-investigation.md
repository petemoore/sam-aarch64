# Go Z80 test-harness fidelity investigation

Date: 2026-05-29
Author: investigation agent (branch `investigate/go-harness-fidelity`)
Scope: research + experiment, no critical-path code changed. Throwaway sweep code lives in `tools/z80-test-harness-go/sweep_test.go` (gated behind `SWEEP=1`).

This note answers four questions Pete raised about the future of `tools/z80-test-harness-go/`: (Q1) should it load the real SAM ROM + real SAMDOS2 instead of stubbing the hooks; (Q2) how does the full M3-M6 fixture corpus fare through the harness today; (Q3) what is the evidence of harness value so far and how do we track usage durably; (Q4) where this work should be scheduled.

Every hardware/behaviour claim below is anchored to a file:line in the ROM disassembly, the SAMDOS2 source, or the assembler sources. The sweep numbers are measured, not asserted.

## Executive summary

- **Q1 — real-ROM + real-SAMDOS2 is NOT worth doing, and would NOT cheaply deliver the benefit Pete wants.** The assembler only ever touches three SAMDOS hooks (HGTHD 129, HLOAD 130, HSAVE 132) plus paging ports 250/251. But those hooks reach disk through SAMDOS's WD1772 floppy-controller driver, which polls a real FDC status register, does timed track-stepping, and DRQ-handshakes block transfers (`samdos/src/b.s:40-110`, `c.s:104-191`). Executing the real ROM+SAMDOS therefore drags in: a cycle/handshake-accurate FDC model, a real `.mgt` disk image to read from, the ROM's PTDOS dispatcher with its DOSFLG-driven dynamic paging and interrupt enable (`rom-v3.0:12944-12978`), and the sysvar layout SAMDOS depends on. That is a SimCoupé re-implementation, not "a memory image + a few ports". And it would *not* model the encroachment Pete cares about, because SAMDOS is not resident in the address window the assembler runs in — it is paged in only for the duration of a hook call (`sam-paging.md:586-598`, Tech Manual `:4632-4641`).
- **The RAM-encroachment benefit has a much cheaper realisation.** The thing that can actually clobber the assembler is its *own* scratch/stack growth past its budgeted region, or an OUT/IN/scratch page collision — both are static memory-map facts, checkable with a link-map assertion (build-time) and a harness write-watchpoint on the physical pages the assembler must not touch (run-time). Neither needs the real ROM.
- **Q2 — full-corpus sweep: 36/36 fixtures byte-match GNU, in BOTH the prod and BUILD_TESTS variants, with ZERO divergence.** That includes the >16 KB paged-OUT fixture (`inst_long_emit`, 16424 B) and the multi-page IN fixture (`in_long_source`). Total wall-clock for all 72 runs incl. text2bin + GNU-oracle subprocess spawns: **1.58 s**. The post-#59 harness is high-fidelity across the entire committed corpus today; there is no known fidelity gap on the corpus.
- **Q3 — usage so far is 1-of-2 recent agents**, exactly as Pete believed: PR #54 built it, PR #59 (commit `114e0ca`) is its first adversarial use and the use that *extended* it (named-file registry, register snapshots, windowed trace, BUILD_TESTS support); PR #60 (`m6-closure-pr2-sysreg-off-axis`) contains no harness reference — SimCoupé throughout. Recommended tracking: a one-line **harness-used: yes/no/n-a** field in the PR-completion checklist (the M6-closure plan already has per-PR checklists), backed by an append-only `tools/z80-test-harness-go/USAGE.md` ledger that an agent jots a dated line into when it uses the tool. Cheap, honest, durable, no code.
- **Q4 — schedule the *cheap* encroachment guards (link-map assert + write-watchpoint) as small inner-loop tasks in M7, where the plan already says the harness "is expected to mature significantly." Do NOT schedule real-ROM/SAMDOS execution at all** unless a concrete bug appears that only real-ROM fidelity can catch — and the corpus sweep says no such bug exists today. The divergence-sweep harness itself (`sweep_test.go`) is worth promoting to a `make harness-sweep` convenience target in M7 as a fast pre-SimCoupé smoke check.

---

## Q1 — Real-ROM + real-SAMDOS2 vs stubs

### What the harness stubs today

The harness (`harness.go`) loads only the prod/test assembler binary, `enctab.enc`, and the `.tbn` into physical RAM pages, installs a 7-byte RST-8 intercept stub at `&0008`, and services the three hooks the assembler issues:

- **HGTHD (129)** — sets the IN/named-file geometry in the `&4B50` header copy and selects the "current file" (`harness.go:591-641`).
- **HLOAD (130)** — copies the current file's bytes into the section-C physical page, spanning consecutive pages like SAMDOS's auto-paging (`harness.go:643-669`).
- **HSAVE (132)** — reads the OUT bytes out of physical pages 5-6 via `UIFA[31..36]` (`harness.go:671-688`).

Everything else (FDC, disk, ASIC, line interrupt, keyboard, screen, the rest of the ROM) is absent. The fake ROM is `0xFF` everywhere except the stub (`harness.go:265-280`).

### What the assembler actually invokes (grounded)

The assembler touches a *small* SAMDOS API surface — only HGTHD/HLOAD/HSAVE and paging ports 250/251:

- `src/loader.asm:124` — `rst 8 / defb HOOK_HGTHD`
- `src/loader.asm` HLOAD via the section-B trampoline; HSAVE trampoline in `src/trampoline.asm:131-167`
- paging: `out (250/251), a` brackets in `src/encoder.asm:421-422`, `trampoline.asm`

So at the *hook-name* level Pete's intuition is right: it's only three hooks. The problem is what those three hooks *do underneath*.

### What the real ROM + SAMDOS2 touch underneath those three hooks

**1. The ROM PTDOS dispatcher is not a no-op.** RST 8 in the SAM ROM is the *error* vector; SAMDOS installs its hook chain and the dispatcher PTDOS at `&380B` does real paging + stack + interrupt work on every hook call (`docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt:12944-12978`, reproduced in `docs/notes/sam-paging.md:600-628`):

```
PTDOS:  LD E,A ; hook number      IN B,(C)        ; B = LMPR (port 250)
        LD HL,0 / ADD HL,SP       LD A,(DOSFLG)   ; &5BC2 = page where SAMDOS lives
        DEC A / DI / OUT (250),A  ; pages SAMDOS into section B
        LD SP,8000H / EI          ; new stack, interrupts ON
        ... CALL 4200H ...        ; dispatch hook in SAMDOS (now at &4000)
```

To run this for real the harness would need: the real ROM image, a correct `DOSFLG` (`&5BC2`) and the surrounding sysvars, SAMDOS2 resident in *its* page, and interrupts left enabled (the assembler runs `di`, but PTDOS does `EI` — `sam-paging.md` records that PTDOS's `EI` is exactly why the SimCoupé exit stub needs a trailing `di`, see `memory/simcoupe_crtp_dispatch_per_platform.md`).

**2. SAMDOS reaches the disk through a real WD1772 FDC driver.** HGTHD → `ckdrv` + `gtixd` + `gtfle`; HLOAD → `dschd` → `gtixd`+`rsad`+`ldhd` then `ldblk`; HSAVE → `gtixd`+`ofsm`+`svhd`+`svblk`+`cfsm` (`samdos/src/h.s:59-90, 132-154`). The bottom of every one of those is FDC port I/O:

- Port equates: `comm=224 (&E0)`, `trck=225`, `sect=226`, `dtrq=227` (`samdos/src/b.s:7-10`).
- The sector loader polls the controller's BUSY bit, does step-in/step-out track seeking with `djnz` timing delays, issues the read-sector command, then DRQ-handshakes the data stream with `ini` and a `bit 1` (DRQ) / `bit 0` (BUSY) poll loop, with a 10-try CRC-error retry path (`samdos/src/b.s:40-110`).
- The catalogue read and block load/save loops do the same DRQ-gated `in a,(c)` / `ini` / `outi` streaming (`samdos/src/c.s:104-191, 622-653, 810-845`).

This is a *protocol*, not a memory-mapped register: status flags, command/track/sector registers, a data-request handshake whose timing the driver assumes, and motor/seek delays. Faithfully executing SAMDOS means emulating that controller and backing it with a real `.mgt` image. koron-go/z80 models the CPU (it even has IM0/IM1/IM2/NMI and RETI/RETN, `go doc` confirms), but it has **no device model** — the FDC, the disk geometry, and the DRQ timing would all be new code.

### Is it "inexpensive"? No — it is a SimCoupé re-implementation

Surface required to make real-ROM+SAMDOS execution work, grounded in the code paths above:

| Surface | Needed because | Effort signal |
|---|---|---|
| Real SAM ROM 3.0 image + RST-8/PTDOS path | hook dispatch is in PTDOS, not a stub | ROM is large; PTDOS interacts with sysvars |
| SAMDOS2 resident image + DOSFLG/sysvar layout | PTDOS pages SAMDOS by `&5BC2` and dispatches at `&4200` | must reproduce boot-time residency `sam-paging.md:586` |
| WD1772 FDC model (status/cmd/track/sector/DRQ, motor + seek timing) | every hook bottoms out in the FDC driver `b.s:40-110`, `c.s:104-191` | this is the big one — handshake + timing accurate |
| Real `.mgt` disk image as the FDC backing store | `gtixd`/`gtfle`/`ldblk`/`svblk` read/write sectors | already have `build-m3-disk`; would need to feed FDC |
| Interrupt handling live during hooks | PTDOS does `EI`; line interrupt at `&0038` | koron-go supports IMx but ISR + ASIC line-int source absent |
| Boot/init path (if booting the disk for real) | ROM reset does screen/ASIC init before AUTO-RUN | `rom-v3.0:0000 DI / JP MINITH` and onward |

That is the bulk of what SimCoupé already does. We would be rebuilding the emulator we already use as the CI gate — and SimCoupé is the **fidelity reference by project rule**. Re-deriving it in Go buys nothing over just running SimCoupé, while costing a large, bug-prone device-emulation effort.

### Would it deliver the RAM-encroachment benefit? No — and there's a cheaper way

Pete's stated benefit is catching the assembler encroaching into "SAMDOS program space." Two facts make real-ROM execution the wrong tool for this:

1. **SAMDOS is not resident in the assembler's window.** It lives in one 16 KB page recorded in `DOSFLG` (`&5BC2`), and is paged into section B *only for the duration of a hook call*, then paged out (`sam-paging.md:586-598`; Tech Manual `:4632-4641`). The assembler runs in section C (`&8000-&BFFF`, physical page 2) with scratch/stack in section D (`&C000-&FFFF`). There is no steady-state overlap to "encroach" on; the only collision window is *inside* a hook, where SAMDOS owns the machine anyway and uses its own stack at `&8000`.
2. **The realistic encroachment bugs are static memory-map facts.** The assembler's budget is tight and explicit (`src/assembler.asm:21-22, 74-113`): stack at `&C100` growing down, scratch arrays at `&C100-&E100`, OUT on physical pages 5-6, IN on 7-12, test_mem on page 13, p14 on page 14. The bugs that actually bite are (a) scratch/stack growth past a budgeted boundary, (b) two physical-page roles colliding, (c) code-size overrun (the production budget is 12265/12288 B today — measured from `build/assembler-prod.bin`). All three are detectable *without* executing SAMDOS:

   - **Build-time link-map assertion.** pyz80 emits symbol addresses; a tiny checker can assert `scratch_end < stack_floor`, `code_end < &C000`, and that the physical-page role table has no duplicates. This is the cheapest, most direct guard and it runs in the existing build.
   - **Run-time write-watchpoint in the harness.** The harness already has the hook for this — `Hardware.watchSPLo/watchSPHi` + `stackWrites` exist but are currently unused (`harness.go:189-194, 214-218`). Wiring `Set()` to record any write to a forbidden physical page (e.g. the SAMDOS-resident page, or page roles the assembler must never write) gives a precise "who wrote where" trace at ~1 ms, far more actionable than a real-ROM crash.

### Q1 recommendation

**Keep the stubs. Do not execute the real ROM/SAMDOS.** Instead, get the *specific* encroachment benefit cheaply:

- Add a build-time link-map assertion (scratch/stack/code-size + physical-page-role uniqueness).
- Activate the harness's already-scaffolded write-watchpoint to flag writes into forbidden physical pages.

This is a **hybrid leaning hard toward "keep stubs + add cheap static/dynamic guards"**, and it delivers Pete's encroachment benefit at a tiny fraction of the cost of a partial SimCoupé re-implementation. If a future bug ever proves to need true SAMDOS-resident-page modelling, the *minimal* escalation is to load the SAMDOS2 image into its DOSFLG page as a passive memory image purely so the write-watchpoint can flag overlap — **without executing it**. Full real-ROM execution should remain off the table absent a concrete bug only it can catch.

---

## Q2 — Full-corpus divergence sweep

### Method

`tools/z80-test-harness-go/sweep_test.go` (gated behind `SWEEP=1`) enumerates every fixture under `tests/m{3,4,5,6}/sources/*.s` — the same sources the SimCoupé `ci-m{3,4,5,6}{,-prod}` matrix runs — and for each:

1. `build/text2bin -o X.tbn X.s` to produce the SAM-side input.
2. GNU oracle: `aarch64-none-elf-as` + `ld -Ttext=0` + `objcopy -O binary` (identical to `tools/run-m6-roundtrip.sh:128-135`).
3. Runs the **post-#59 harness** with both `assembler-prod.bin` and `assembler.bin` (BUILD_TESTS variant, with `test_mem` on page 13 and `p14` on page 14 registered as named files).
4. Compares harness OUT bytes against the GNU oracle.

Run on the host (macOS, aarch64-none-elf toolchain present). Prereqs built with `make text2bin enctab m3-asm-prod m3-asm test-mem-offaxis paged-call-payload`.

### Result: zero divergence

```
PROD variant: 36 fixtures, 36 harness-OK, 36 byte-match-GNU
TEST variant: 36 fixtures, 36 harness-OK, 36 byte-match-GNU
```

All 72 runs (36 fixtures × 2 variants) passed and byte-matched GNU. Total wall-clock **1.58 s** including all the text2bin and GNU-oracle subprocess spawns; the harness Z80 step itself is ~1 ms/run as advertised.

| Milestone | Fixtures | Prod match | Test match | Notable |
|---|---|---|---|---|
| m3 | 9 | 9/9 | 9/9 | directives, expr, movz/movn, reg-imm |
| m4 | 5 | 5/5 | 5/5 | pcrel, bcond, ccmp, csel, local labels |
| m5 | 20 | 20/20 | 20/20 | shifted/extended, all mem shapes, litpool/ltorg, mrs/msr, dc/tlbi |
| m6 | 2 | 2/2 | 2/2 | `inst_long_emit` (16424 B, paged-OUT >16 KB); `in_long_source` (multi-page IN) |
| **Total** | **36** | **36/36** | **36/36** | — |

### Interpretation

- **No fidelity gap on the committed corpus.** Every fixture that SimCoupé byte-matches, the post-#59 harness also byte-matches, prod and test. The harness can help debug any fixture in the corpus.
- **The hard cases work.** `inst_long_emit` exercises the paged-OUT machinery (HSAVE auto-paging across `&C000` into page 6); `in_long_source` exercises multi-page IN. Both byte-match — so the harness's HSAVE multi-page capture (`readPageBytes`, `harness.go:444-454`) and the named-file multi-page HLOAD (`harness.go:653-668`) are faithful, not just present.
- **The categorisation Pete asked for** — "harness fidelity gap vs genuine signal" — comes out empty on both axes for the corpus: 0 fidelity gaps, 0 genuine divergences. The one historical fidelity gap (the #59 trigger-PC backtrace that "looked like a Z80 bug") was a *BUILD_TESTS-boot* gap (empty pages 13/14, IN not restored after the reader self-test clobbers it), which #59's named-file registry + IN auto-re-deposit closed (`commit 114e0ca` message; `harness.go:534-544`). That gap is not in the fixture corpus — it was in the boot self-test path, which is exactly where the harness's value showed up.

Caveat: this sweep validates the **OUT byte stream and the OK banner**, the same contract SimCoupé's round-trip checks. It does not validate timing, interrupt behaviour, or anything off that contract — but neither does the CI gate, so the comparison is apples-to-apples.

---

## Q3 — Usage tracking & evidence of value so far

### Evidence to date (confirmed from git)

- **PR #54** (`694c2e3`, "tools: land Go Z80 test harness for dev-side iteration") — built the harness (Spike A of the bake-off).
- **PR #59** (`114e0ca`, "z80-harness: BUILD_TESTS variant support + fault diagnostics") — its own commit message calls itself **"First adversarial use of the Go harness"**. It is also the use that *extended* the tool: named-file registry, register snapshot at exit, `&0038` garbage-trap detector, windowed PC/register trace, trigger-PC backtrace, and `TestVariantBootSelfTests`. The trigger-PC backtrace revealed that an apparent "Z80 bug" was actually a harness fidelity gap (empty pages 13/14 + IN not restored) — i.e. the tool's diagnostics paid for themselves on first contact.
- **PR #60** (`m6-closure-pr2-sysreg-off-axis`, commits `bd20ef6`/`c88cb60`/`d88e5bd`) — `git log -p ... | grep -i harness` returns nothing; SimCoupé throughout.

So **1 of the 2 recent feature agents used it** (the third PR, #54, *is* the harness). Pete's recollection is exactly correct.

### Why value is hard to see, and what to track

The honest read: one strong data point of value (the #59 adversarial use found a real root cause fast) and one explicit non-use (#60). Two samples is too few to conclude the tool earns its keep — which is precisely why a durable, low-friction usage signal matters.

Options weighed:

| Mechanism | Pro | Con |
|---|---|---|
| Counter in the harness | automatic | counts CI/test invocations too; dishonest signal; needs a writable state file |
| Auto-appended run ledger | automatic, no discipline | noisy (every `go test` bumps it); hard to attribute to a task; writes during CI |
| Memory-entry convention | already a project habit | memory is for durable preferences, not a running tally; clutters the index |
| **PR-checklist field + hand-jotted USAGE.md ledger** | honest, attributable, zero code, matches existing per-PR checklists | relies on agent discipline |

### Q3 recommendation

Adopt the **PR-checklist field + ledger** pair, because it's honest (a counter that ticks on CI runs would lie) and attributable (we want to know *which task* used it and *whether it helped*):

1. Add one line to the per-PR completion checklist the M6-closure/M7 plans already use:
   `harness-used: yes | no | n-a  (if yes: did it help? one sentence)`
2. Back it with an append-only `tools/z80-test-harness-go/USAGE.md` where an agent that uses the tool jots one dated line: date, PR/branch, what it was used for, whether it helped or misled. The #59 entry would read like: *"2026-05-28 PR#59 — reproduced BUILD_TESTS boot trap; trigger-PC backtrace exposed empty pages 13/14 as a harness gap, not a Z80 bug. Helped."*

Cheap, durable, no automation to maintain, and it produces the honest longitudinal record needed to decide later whether the tool keeps earning its place.

---

## Q4 — Where to schedule

The M6-closure plan (`docs/plans/2026-05-29-m6-closure-release-bytematch.md`) already states the harness "is expected to mature significantly through M7" and lists the harness as the inner-loop tool with agents empowered to extend it mid-task. That is the right home for the cheap follow-ups; it is the wrong home for a SimCoupé re-implementation (which should not be scheduled at all).

Recommended placement:

- **M7, small inner-loop tasks (do):**
  - Build-time link-map assertion (scratch/stack/code-size budgets + physical-page-role uniqueness). This is the cheap realisation of Pete's encroachment benefit and protects the tight 12265/12288 B production budget.
  - Activate the harness write-watchpoint (`watchSPLo/Hi` + `stackWrites` already scaffolded) to flag writes into forbidden physical pages at run time.
  - Promote `sweep_test.go` into a `make harness-sweep` convenience target — a ~2 s full-corpus smoke check to run before the SimCoupé gate. (Keep it non-gating, per the dev-tool rule.)
  - Add the USAGE.md ledger + PR-checklist field (Q3).
- **M7 naturally (already in the sketch):** the harness will get exercised hard by the on-SAM disassembler / shared-data-structure work; let real usage drive further diagnostics, per the "agents own the harness" rule.
- **Do NOT schedule:** full real-ROM + real-SAMDOS2 execution. Park it behind a trigger: *only* if a future bug appears that byte-stream + watchpoint guards demonstrably cannot catch and that reproduces only under real SAMDOS residency. The corpus sweep shows no such bug exists today.

---

## Appendix — reproduction

```
# from repo root
make text2bin enctab m3-asm-prod m3-asm test-mem-offaxis paged-call-payload
cd tools/z80-test-harness-go
SWEEP=1 go test -run TestCorpusSweep -v -timeout 300s ./...
```

`sweep_test.go` is throwaway investigation scaffolding — it is committed on this branch for reproducibility but is not intended as a CI gate. The harness `.go` files on this branch are the post-#59 versions (copied from commit `114e0ca`) so the sweep reflects the latest tooling; that copy is incidental to the investigation and should not be taken as landing #59 a second time.

## Sources

- `tools/z80-test-harness-go/harness.go` — hook stubs, named-file registry, watchpoint scaffold, paged capture.
- `samdos/src/b.s:7-10` (FDC port equates), `:40-110` (sector-read FDC handshake + retry).
- `samdos/src/c.s:104-191, 622-653, 810-845` (catalogue/block DRQ-gated streaming), `:354-369` (ctas auto-paging).
- `samdos/src/h.s:59-90, 132-154` (HGTHD/HLOAD/HSAVE hook bodies → disk).
- `docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt:12944-12978` (PTDOS dispatcher); `docs/notes/sam-paging.md:586-628` (SAMDOS residency + PTDOS reproduction); Tech Manual `:4632-4641` (SAMDOS one-page residency).
- `src/loader.asm:41-75, 113-130` (hook usage + clobber facts), `src/assembler.asm:7-113` (memory map / budgets), `src/trampoline.asm:131-167` (HSAVE trampoline).
- git: PR #54 `694c2e3`, PR #59 `114e0ca`, PR #60 `bd20ef6`/`c88cb60`/`d88e5bd`.
