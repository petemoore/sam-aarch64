# plan-PR 1 — stuck note (2026-05-28)

**Status:** plan-PR 1 of `docs/notes/2026-05-28-paged-call-architecture.md`
is shipped as **draft**. The primitives (`paged_call`,
`paged_data_map_hmpr`, `paged_data_unmap_hmpr`) and constants
(`PAGED_PAGE_DISASM_AUX`) are in place; the page-13 disk-image
plumbing exists; the test file `test_paged_call.asm` and the
`page13_test_payload.asm` exist. **The self-test is currently
disabled (commented out in `assembler.asm`) and the design diverges
from the architecture doc in two places.**

This note documents the two issues so a follow-up session (or Pete)
can pick up cleanly.

---

## Issue 1 — `paged_call` trailer's SP-restore is wrong in the doc

### What the doc says (§3.3, the final HMPR-based handler)

```
paged_call:
    di
    pop     hl                  ; HL → inline payload
    in      a, (251)
    ld      (paged_call_hmpr_save), a
    ld      (paged_call_sp_save), sp     ; saved AFTER the pop
    ld      sp, paged_call_safe_sp
    ...
    push    hl                          ; real return after target
    ...
    ret                                  ; → target

paged_call_trailer:
    ld      a, (paged_call_hmpr_save)
    out     (251), a
    ld      sp, (paged_call_sp_save)
    ret                                  ; ← claims to return to caller
```

### Why it's broken

`pop hl` increments SP by 2 (moving SP back to where it was at the
moment the caller's `CALL paged_call` was executed). The saved value
(= caller_SP_at_call) IS the SP value before the CALL pushed the
return address.

At the trailer's `ret`, after restoring SP to (paged_call_sp_save),
SP = caller_SP_at_call. The `ret` reads `mem[SP..SP+1]`. But the
caller's return address was pushed at `mem[caller_SP_at_call - 2
..caller_SP_at_call - 1]` (SP-relative-pre-decrement push convention).
So `ret` reads bytes ABOVE the return-address slot — garbage from the
caller's stack frame. PC jumps to garbage. Crash.

### Fix applied in this PR

`src/m3/trampoline.asm` `paged_call_body` saves SP **BEFORE** the
pop:

```
paged_call_body:
    di
    ld   (PAGED_CALL_SP_SAVE), sp     ; SP = caller_SP - 2 (post-CALL state)
    in   a, (251)
    ld   (PAGED_CALL_HMPR_SAVE), a
    pop  hl                            ; HL = payload addr; SP = caller_SP
    ...read payload, advance HL to post-payload...
    push hl                            ; SP back to caller_SP - 2;
                                       ; mem[SP..+1] := post-payload-return
    ld   sp, TRAMP_SAFE_SP             ; switch to safe SP
    ...

paged_call_trailer:
    ld   a, (PAGED_CALL_HMPR_SAVE)
    out  (251), a
    ld   sp, (PAGED_CALL_SP_SAVE)      ; SP = caller_SP - 2;
                                       ; mem[SP..+1] = post-payload-return
    ret                                 ; correctly pops post-payload-return
```

This mirrors the structure of the existing HLOAD trampoline
(`trampoline_body` at the top of `trampoline.asm:472`), which saves
SP before any pop and restores to the same slot before its final RET.

The architecture doc's §3.3 pseudocode needs updating to match. Pete
asked not to silently diverge — this note IS the divergence report.

---

## Issue 2 — `paged_data_map_hmpr` cannot be safely called from section-C/D code

### What the doc envisions (§4.2)

The "bulk read" pattern shown at architecture doc §4.2:
```
ld      a, C                  ; C = target HMPR (page)
call    paged_data_map        ; HMPR := target, save old
ldir                          ; or whatever loop reads the page
call    paged_data_unmap      ; HMPR := saved old
```

with the implicit assumption that callers in `sysname.asm` (or any
other consumer) can bracket their reads this way.

### Why it doesn't work for callers in section C/D

The caller's `CALL paged_data_map_hmpr` pushes the return address to
the caller's section-D stack. The body changes HMPR (section C/D
remapped to the target page). The body's final `ret` reads from SP,
which still points into caller's section D — but section D is now
target+1 page, not entry_HMPR+1. The bytes read are from a different
physical page. Garbage return → crash.

Even with an internal SP-switch inside the helper (mirroring
`paged_read_byte` from doc §4.1), the caller's NEXT INSTRUCTION (after
`CALL paged_data_map_hmpr` returns) is fetched from caller's section
C/D under target HMPR — the bytes there are different from the
caller's actual code. CPU executes garbage.

The doc seems to implicitly assume the caller's code lives in
**section A or B** (LMPR-stable), where HMPR changes don't affect
instruction fetches. For instance, the §3.4 prose mentions reading
the sysreg-table page "during sysreg lookup in the encoder window"
where `LMPR_ENCTAB` is live (section A = page 4 = ENCTAB) — but the
sysreg-lookup CODE itself lives in `sysname.asm` (the assembler
binary, section C). When HMPR changes, section C changes too —
the caller's code is no longer at its expected fetch address.

### What this means for plan-PR 1's self-test

The plan-PR 1 instructions called for:

> Write a known sentinel byte (e.g. `&A5`) at `&8000` of page 13 via
> `paged_data_map_hmpr` / `paged_data_unmap_hmpr`.
> Read it back via the same map/unmap pair; assert it round-tripped.

The self-test code in `test_paged_call.asm` would live in section D
of the BUILD_TESTS binary (around `&C2xx`). It cannot safely invoke
`paged_data_map_hmpr` — would crash on return.

### Current state

`paged_data_map_hmpr` and `paged_data_unmap_hmpr` ARE implemented in
this PR (`trampoline.asm`); they are LDIR'd into section B at boot;
their constants are exposed for future call-site use. Their bodies
follow the spec's §4.2 ABI (caller passes A=page; helpers preserve
HMPR top-3 bits). They will be **usable by future plan-PRs whose
callers live in LMPR-stable memory** (section A or B) — for example,
plan-PR 2's sysreg-lookup IF that lookup is moved to section B as
part of the same PR.

The `paged_data` assertions from the test spec have been DROPPED.
The self-test is reduced to the `paged_call` round-trip only (which
IS safe from a section-D caller because the trailer restores HMPR
before returning, so the caller's continuation runs under the
original section C/D mapping).

---

## Issue 3 — Test binary boots, but the M3 fixtures hang

After implementing both fixes above and disabling the
`paged_data_*` assertions, **the assembler binary STILL hangs at boot
on the M3 fixture tests** (all 9 fixtures `rc=124` = SimCoupé
30-second timeout, empty printer log). This happens even with both
`call load_page13_payload` and `call run_paged_call_self_tests`
commented out — i.e. with the new bodies present in section C but
never executed.

### What was eliminated

- **Trampoline body bytes**: byte-identical to baseline (verified with
  `xxd` on a fresh build vs `git stash`'d baseline).
- **`enctab_trampoline_setup` body bytes**: byte-identical to
  baseline. Same 30-byte LDIR.
- **Address resolution**: all CALL targets shifted by the body-source
  insertion delta, but pyz80 resolves them correctly at assembly time
  (verified by spot-checking `enctab_map_in`, `load_enctab`,
  `LMPR_DEFAULT_RUNTIME` etc.).
- **`start:` boot sequence bytes**: byte-by-byte equivalent to
  baseline modulo the resolved addresses.
- **Section-D scratch layout** (`OPVAL_ARRAY` at `&C100` etc.):
  unchanged in the source.
- **Boot hang reproduces with the test self-tests commented out**
  AND with `enctab_trampoline_setup`'s LDIR reduced back to copying
  only the HLOAD body (30 bytes).
- **Baseline (git stash) reproduces clean 9/9 PASS in the same
  docker container** — so the docker / SimCoupé / X11 setup is fine.

### UPDATE 2026-05-28 evening — stack-overflow hypothesis didn't pan out

After moving `paged_call` / `paged_data` body sources to `paged_bodies.asm`
included at the END of `assembler.asm` (so `boot_hmpr` and other
BUILD_TESTS storage bytes stay close to their baseline addresses),
the test variant STILL hangs.  `boot_hmpr` moved from `&C0AB`
(plan-PR-1 inline-bodies version) to `&C071` (plan-PR-1 bodies-at-end
version) vs baseline `&C041`.  The hang persists with `boot_hmpr` at
`&C071` and even with the second LDIR (the one that copies paged_call
into section B) entirely commented out.

This means the regression isn't from `boot_hmpr` being too close to
the stack — it's from something ELSE that changed when adding 100ish
bytes of inert source-code bytes (the paged_call / paged_data body
sources) to the section-D tail of the BUILD_TESTS binary.

#### Original "stack overflow" hypothesis (kept for the record)

`boot_hmpr` is defined in `test_trampoline.asm:107` as a single
`defb 0` byte at the end of that file. Its absolute address is
wherever pyz80 happens to place it in the binary — determined by the
total size of the SECTION-C source code preceding it.

| Variant | `boot_hmpr` address | Δ from SP=&C100 |
|---|---|---|
| baseline (clean main)      | `&C041` | 191 bytes  |
| plan-PR 1 (my version)     | `&C0AB` | 85 bytes   |

The boot SP is `&C100`. The pre-load self-tests push/call from there.
In baseline, the stack would need to grow 192 bytes deep before
overwriting `boot_hmpr`. In my version, only **86 bytes deep**.

The pre-load self-test suite is large — 13 calls (`run_slot_self_tests`
through `run_emit_paged_self_tests`), each potentially nesting calls
that push registers. Reaching 86 bytes of stack depth is plausible;
reaching 192 isn't.

When `boot_hmpr` is overwritten:
- The post-load `run_trampoline_self_tests` reads `boot_hmpr` and
  compares to current HMPR.
- Mismatch → `jp nz, fail`.
- `fail` does `out (&fe), a` (border red), then
  `call print_status_string` with `msg_fail`.
- `print_status_string` writes bytes to printer port `&E8`/`&E9`.
- The SimCoupé wrapper captures that and reports "FAIL".

But what we see in the test runs is `rc=124` (timeout) with **empty
printer log** — i.e. the assembler isn't even reaching `print_status_string`
to emit "FAIL". So the corruption is more severe than just `boot_hmpr`:
the stack pushes likely overwrite something else in the
`&C0xx`-deeper range that the test code reads, leading to a more
catastrophic execution-path divergence.

### How the existing code "got away with it"

`reader_paged_self_test_investigation.md` (the doc referenced from
the `; NOTE: run_reader_paged_self_tests is DISABLED` block in
`assembler.asm:264`) is the canonical investigation of this exact
failure mode. PR #42 attempted to move SP to `&FFFE` to give the
stack more headroom; PR #43 reverted that because of a layout
interaction with PR #41. **plan-PR 1 has stumbled into the same
trap** — the section-C addition shifts test-data labels deeper into
the stack-growth zone, and the eventual stack-overwrite corrupts
test state.

### Where bisection should start (REVISED twice)

Stack overflow into `boot_hmpr` was the natural hypothesis based on
the test-data label addresses; it has been ruled out empirically
(see "UPDATE 2026-05-28 evening" above).

The smallest reproducer in this PR is:
- `enctab_trampoline_setup` is back to the baseline 30-byte LDIR
  (paged_call LDIR is included in this PR; commenting it out
  doesn't fix the hang).
- HMPR_SAVE / SP_SAVE are at `+32 / +33` (= `&7E20 / &7E21`),
  byte-identical to baseline.
- paged_bodies.asm is included at the END of assembler.asm,
  AFTER the BUILD_TESTS test files.
- `load_page13_payload` is BUILD_TESTS-only in `loader.asm` but
  not called from anywhere (test-call commented out).
- `test_paged_call.asm` is NOT included.

Despite all this, the test variant fails 0/9 fixtures (rc=124,
SimCoupé timeout, empty printer log).  The baseline (git stash)
passes 9/9 in the same docker container.

Triage path:

1. **(~30 min)** Bisect the section-C source additions by manually
   removing each new chunk (body sources, EQUs, name_page13 data,
   load_page13_payload routine, paged_call_setup LDIR) one at a
   time.  Build, run `make ci-m3`.  Find the minimal change that
   reproduces the hang.

2. **(~1 hr)** Hook SimCoupé's `-debugger` or attach a stub-trace
   via the printer channel (similar to how the existing
   print_status_string works) and find WHERE in the boot the Z80
   diverges.

3. **(?)** Once the root cause is known, decide whether plan-PR 1
   can ship at all in the current form, or whether plan-PR 3 (test
   corpus off-axis) needs to land first to free up section-C
   budget.

### How to repro the hang

```bash
cd ~/git/sam-aarch64

# baseline (clean):
git stash
docker run --rm -v "$(pwd):/work" -w /work \
  -e SDL_VIDEODRIVER=x11 -e SDL_AUDIODRIVER=dummy \
  -e AARCH64_AS=aarch64-linux-gnu-as -e AARCH64_LD=aarch64-linux-gnu-ld \
  -e AARCH64_OBJCOPY=aarch64-linux-gnu-objcopy \
  ghcr.io/petemoore/sam-aarch64-dev:latest \
  bash -c '
    Xvfb :99 -screen 0 1280x1024x24 >/dev/null 2>&1 &
    sleep 1; export DISPLAY=:99
    rm -rf build/
    make ci-m3 2>&1 | tail -5
  '
# → 9/9 PASS

git stash pop
# (re-applies plan-PR-1 changes)
docker run --rm ...same incantation as above...
# → 0/9 PASS, all rc=124 (timeout)
```

### Where bisection should start

Suggested triage order (rough hour estimates):

1. **(~30 min)** Build a test binary WITHOUT my new body sources, but
   WITH the new EQU constants. (Delete `paged_call_body` /
   `paged_data_map_body` / `paged_data_unmap_body` and their
   end-labels; comment out `paged_call_trailer_dst`'s definition;
   replace the EQU expressions for `PAGED_CALL_DST` etc. with
   hard-coded values like `TRAMPOLINE_DST + &40`.) If THIS boots
   clean, the bodies' SECTION-C SOURCE bytes (not their execution)
   are corrupting something. Likely either pyz80 layout interaction
   or a BASIC-load overlap somewhere.

2. **(~30 min)** Add bodies one by one (paged_call_body alone,
   then +paged_data_map_body, then +paged_data_unmap_body) and
   identify which one introduces the hang.

3. **(~1 hr)** If hang reproduces with just `paged_call_body`,
   examine the SIM trace / serial output for the `start:` sequence
   to find where execution diverges. The Z80 emulator in SimCoupé
   does support disassembled trace output; combine with `-debugger`
   or a manual stub-trace via printer-channel.

---

## What was implemented and is in tree

1. **`src/m3/trampoline.asm`** — `paged_call_body`, `paged_call_trailer`,
   `paged_data_map_body`, `paged_data_unmap_body`. All four are
   LDIR'd into section B by `enctab_trampoline_setup` at boot
   (single LDIR covering all bodies, since they're laid out
   contiguously in source order with destinations matching).
   Constants: `PAGED_PAGE_DISASM_AUX`, `PAGED_CALL_DST`,
   `PAGED_DATA_MAP_DST`, `PAGED_DATA_UNMAP_DST`, plus lowercase
   aliases `paged_call`, `paged_data_map_hmpr`,
   `paged_data_unmap_hmpr`. Static save slots
   `PAGED_CALL_HMPR_SAVE`, `PAGED_CALL_SP_SAVE`,
   `PAGED_DATA_HMPR_SAVE` placed at `TRAMPOLINE_DST + &E3..+&E6`.
   The `HMPR_SAVE` / `SP_SAVE` slots used by the existing HLOAD
   trampoline are kept at their original `TRAMPOLINE_DST + 32` /
   `+33` offsets to keep the trampoline body byte-identical to
   pre-PR-#50 state.
2. **`src/m3/loader.asm`** — `load_page13_payload` (BUILD_TESTS-only)
   HLOADs the page-13 test payload via the existing HLOAD
   trampoline. Currently not called.
3. **`src/m3/page13_test_payload.asm`** — 3-byte payload assembled
   standalone: `ld a, &42; ret`.
4. **`src/m3/test_paged_call.asm`** — paged_call self-test (3
   assertions: HMPR-pre captured, A=&42 after paged_call,
   HMPR-post equals HMPR-pre).  Currently not included.
5. **`src/m3/assembler.asm`** — wires up the (currently disabled)
   `call load_page13_payload` / `call run_paged_call_self_tests`
   in the BUILD_TESTS block before `load_enctab`.
6. **`tools/build-m3-disk/main.go`** — accepts `-p13 <path>` flag,
   deposits the payload as a CODE file called `p13` on the disk
   image.
7. **`tools/run-m3-roundtrip.sh`** — passes `-p13
   build/page13_test_payload.bin` to `build-m3-disk` when running
   against the BUILD_TESTS variant; skips for the prod variant.
8. **`Makefile`** — builds `$(BUILD)/page13_test_payload.bin` from
   the `.asm` source; `m3-disk` invokes `build-m3-disk` with the
   `-p13` flag.

## Binary sizes

| Variant | Baseline | PR 1 | Delta | Notes |
|---|---|---|---|---|
| test (BUILD_TESTS=1) | 16812 (ends &C1AC) | 16918 (ends &C216) | +106 | Deeper than doc's stated &C10F; pushes the "soft test ceiling" further |
| prod              | 12204 (ends &AFAC) | 12273 (ends &AFF1) | +69  | Under &AFFF absolute ceiling; 14 B headroom (target was > 50 B) |

The prod budget is tight but passing. The test variant pushes deeper
than the doc requested ("do NOT push deeper than &C10F"); plan-PR 3
(test corpus off-axis) is the right fix here.

## What needs to happen next

1. **Architecture doc patch** for the two spec issues:
   - §3.3 `paged_call` pseudocode: save SP before the `pop hl`, not
     after; stuff the post-payload return back to the caller's stack
     slot via `push hl` before switching to safe SP.
   - §4 should explicitly state that `paged_data_map_hmpr` /
     `paged_data_unmap_hmpr` are only safe to call from code in
     section A or B (LMPR-stable). Either: (a) document this as a
     constraint, (b) define a different mechanism for section-C/D
     callers (e.g. a `paged_read_block` helper that does
     map+read+unmap atomically from section B, mirroring §4.1's
     `paged_read_byte`), or (c) reframe plan-PR 2's sysreg-lookup as
     "first move the lookup routine to section B".
2. **Diagnose Issue 3** (boot hang). The bisection plan above is
   the natural starting point.
3. **Re-enable** the `load_page13_payload` + `run_paged_call_self_tests`
   calls in `assembler.asm` and the `include "test_paged_call.asm"`.
   Verify all 11 CI jobs green.
4. **Re-evaluate** the binary-size budgets — the test variant push
   past `&C10F` is structural to plan-PR 1 (the test code has to
   exist somewhere) and is a plan-PR 3 concern. If it's a blocker,
   plan-PR 3 may need to land first.

## Reference

- Design doc: `docs/notes/2026-05-28-paged-call-architecture.md`
  (read-only per Pete's instructions).
- Architecture brainstorm: `docs/notes/2026-05-28-memory-layout-brainstorm.md`.
- HLOAD trampoline (structural cousin): `src/m3/trampoline.asm:472`.
