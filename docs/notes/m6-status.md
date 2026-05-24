# M6 — current status (read me first)

Entry point for any session picking up where M6 left off.

**M6 IN PROGRESS — PR 2 of N landed.** PR 1 paged OUT; PR 2 pages IN.
With both, source files > 2 KB AND outputs > 2 KB are now possible
on the SAM-side assembler.  Subsequent PRs cover the compact `.tbn`
format, the on-SAM disassembler, and multi-digit local labels.

## M6 scope (full milestone)

Per `docs/ROADMAP.md` M6 row + the standing deferred items.  M6 is
the milestone where real spectrum4 sources flow through the SAM-side
toolchain — `release.s` is ~20 KB source emitting ~22 KB output.

| Strand | Status | Spec | PR |
|---|---|---|---|
| Paged OUT buffer (sections-B emit + HSAVE auto-paging) | ✅ done | `docs/specs/2026-05-27-m6-paged-out-design.md` (+ `docs/specs/2026-05-27-samdos-save-idiom.md`) | landed (#36) — M6 PR 1 |
| Paged IN buffer (source > 2 KB; trampoline-HLOAD into pages 7..10) | ✅ done | `docs/specs/2026-05-27-m6-paged-in-design.md` | this PR (M6 PR 2) |
| Multi-digit local labels (`10f` / `10b` / …) | 📋 plan ready | `docs/plans/2026-05-27-multi-digit-local-labels.md` | open draft PR #35 (independent of this PR) |
| Compact `.tbn` format | 📋 designed | `docs/specs/2026-05-27-compact-tbn-and-disassembler-design.md` | M6 PR 3+ |
| On-SAM disassembler | 📋 designed | `docs/specs/2026-05-27-compact-tbn-and-disassembler-design.md` | M6 PR 4+ |
| spectrum4 release.bin byte-match on SAM | 📋 ultimate goal | — | M6 PR N |

## PR 1 — paged OUT buffer (this PR)

### What landed

Per `docs/specs/2026-05-27-m6-paged-out-design.md` and
`docs/specs/2026-05-27-samdos-save-idiom.md`:

1. **Runtime emit path.**  `emit_byte` now writes the OUT buffer
   through section B (`&4000-&7FFF`) rather than section C.  Two
   zones:
   - **Low zone (bytes 0..16383):** section B already maps to
     physical page 5 for free during the encoder window
     (LMPR_ENCTAB = `&24`; section B = LMPR + 1 = page 5).  Writes
     land with no LMPR change — zero overhead vs the pre-M6
     emit.
   - **High zone (bytes 16384..32767):** per-byte LMPR bracket
     (`out (250), &25` → write → restore the LMPR snapshot
     captured via `in a, (250)`).  ~60 T-states / byte extra.
   - Zone transition at OUT_PC = `&8000`: flip `OUT_ZONE`, wrap
     `OUT_PC` to `&4000`.  A second wrap is `jp fail` (32 KB cap).
2. **Save path.**  `save_out_file` now populates UIFA[31] with
   `OUT_BASE_PAGE` (= 5), UIFA[32-33] with `&8000`, UIFA[34] with
   `OUT_LEN >> 14`, UIFA[35-36] with `OUT_LEN & 0x3FFF`.  HSAVE
   manages its own HMPR (saves at entry, auto-pages across `&C000`
   inside its save loop, restores at exit) — no caller-side
   trampoline needed.  Per
   `docs/specs/2026-05-27-samdos-save-idiom.md`.
3. **Constants.**  `OUT_BASE_PAGE = 5`, `LMPR_OUT_HIGH = &25` added
   to `src/m3/trampoline.asm` alongside `ENCTAB_PAGE` / `LMPR_ENCTAB`.
4. **Old section-C OUT buffer removed.**  `OUT_BUF` / `OUT_BUF_END`
   constants dropped from `assembler.asm`; `OUT_BASE` storage word
   dropped from `main_loop.asm`.  The freed 2 KB at `&B800-&BFFF`
   is reserved for future use.
5. **Boot self-test.**  `test_emit_paged.asm` exercises both zones
   (low-zone write-back via section B, forced zone cross, high-zone
   write-back via LMPR=&25).  Included in the test variant only.
6. **Fixture.**  `tests/m6/sources/inst_long_emit.s` emits 16424
   bytes (> 16 KB) via 4 ALU instructions + `.skip 16384` + 6 more
   ALU instructions.  The `.skip` straddles the OUT_ZONE boundary
   so the trailing instructions land in the high zone.
7. **CI.**  New `m6` + `m6-prod` GitHub Actions jobs mirror the M5
   pattern.

### Deviations from the plan

| What | Why |
|---|---|
| Port 250 (LMPR), not port 251 (HMPR) | The plan + design spec both used `in/out (251)` for the LMPR snapshot.  Port 251 is HMPR; LMPR is port 250 (verified against every existing site in the codebase — trampoline.asm:309, test_trampoline.asm:43/52/82/86, enctab_map_in/out).  Writing to port 251 would map page 5 into section C, evicting the running assembler.  Caught by the boot self-test hanging on the first M3 fixture (rc=124).  Fix: swap port 251 → 250 throughout the emit_byte high-zone block and the test's read-back bracket. |
| Fixture uses `.skip 16384`, not 4300 × `add x0, x0, x0` | The plan's "4300 instructions" generation produces a .tbn of ~51 KB which overflows the 2 KB IN_BUF (paged-IN is M6 PR 2, not this PR).  Using `.skip 16384` instead keeps the .tbn at 1151 bytes while still emitting > 16 KB of OUT. |
| Boot self-test brackets itself in `enctab_map_in` / `enctab_map_out` | The plan's test code doesn't bracket.  Without the bracket, LMPR is LMPR_DEFAULT_RUNTIME and section B maps to whatever BASIC's page is — not page 5 — so the test would write/read random memory rather than the OUT buffer.  Bracketing matches the LMPR state the encoder runs in (= LMPR_ENCTAB during pass 2). |

### Test status (all green)

| Layer | Status | Notes |
|---|---|---|
| `make m3-asm` (test) | ✅ PASS | 16176 B in disk slot |
| `make m3-asm-prod` | ✅ PASS | 11906 B / 12288 B (382 B headroom) |
| `make ci-m3` | ✅ PASS | 9/9 M3 fixtures (regression check) |
| `make ci-m3-prod` | ✅ PASS | 9/9 M3 fixtures (production variant) |
| `make ci-m4` | ✅ PASS | 4/4 M4 fixtures |
| `make ci-m4-prod` | ✅ PASS | 4/4 M4 fixtures |
| `make ci-m5` | ✅ PASS | 19/19 M5 fixtures |
| `make ci-m5-prod` | ✅ PASS | 19/19 M5 fixtures |
| `make ci-m6` | ✅ PASS | 1/1 M6 fixtures (>16 KB output crosses OUT_ZONE) |
| `make ci-m6-prod` | ✅ PASS | 1/1 M6 fixtures (production variant) |
| Boot self-tests | ✅ PASS | Slots + symbols + local-label + M4 expr_eval + PC-rel + M5 directives + ror-imm + ShiftedReg + ExtendedReg + Mem + SysName + LitPool + paged-emit |

### Code budget

| variant | size | budget | headroom |
|---|---|---|---|
| `m3-asm` (test) | 16176 B | 20391 B (40-sector disk slot) | 4215 B |
| `m3-asm-prod` (no self-tests) | 11906 B | 12288 B (`&8000-&AFFF`) | **382 B** |

Net cost of M6 PR 1: 38 B in production (11868 → 11906).  Test
variant gained 211 B (15965 → 16176, including the new
test_emit_paged.asm).  Both well within budget.

### Memory layout (during assembly)

```
&8000-&AFFF  assembler code (12 KB; production 11906 B post-M6 PR 1)
&B000-&B7FF  IN .tbn buffer (2 KB) — still single-page; M6 PR 2 makes it paged
&B800-&BFFF  reserved (freed by M6 PR 1; available for future use)
&C000-&C0FF  stack (SP = &C100)
&C100-&FFFF  scratch (OPVAL arrays, SYMTAB, OPMEM_OFF, LITPOOL, etc.)

Physical page 4 (off-axis): ENCTAB body — paged into section A on demand
Physical pages 5..6 (off-axis): OUT buffer
  - page 5 reached for free via section B under LMPR_ENCTAB (low zone)
  - page 6 reached via per-byte LMPR=LMPR_OUT_HIGH bracket (high zone)
  - HSAVE reads via section C with UIFA[31] = OUT_BASE_PAGE
```

## PR 2 — paged IN buffer (this PR)

### What landed

Per `docs/specs/2026-05-27-m6-paged-in-design.md`:

1. **Storage.**  IN .tbn loaded via trampoline-HLOAD into physical
   pages 7..10 (4 contiguous pages = 64 KB ceiling per design;
   see "Caveat" below for the load-path's effective range).
2. **Cursor.**  Flat 16-bit `IN_POS` replaced with 24-bit (page,
   offset) pair: `IN_POS_PAGE` holds a full LMPR byte (RAM0 bit +
   low 5 bits = the IN page mapped into section A), `IN_POS_OFFSET`
   the section-A offset in `[&0000, &4000)`.  IN_END similarly
   split.
3. **Reader runtime.**  `reader_next_kind` brackets each record
   fetch with an LMPR=`&27`-derived window mapping the current IN
   page into section A; copies the record's `[kind][len][payload]`
   into `STAGING_BUF` (= `&D500`, 1 KB) via an LDI loop with inline
   `H >= &40` page-cross test (re-normalises HL + bumps LMPR mid-
   loop); restores `LMPR_ENCTAB` before returning so the encoder
   window is live on entry to the record handler.
4. **Cursor helpers.**  Three primitives in main_loop.asm:
   `in_map_current` (LMPR := IN_POS_PAGE), `in_persist_hl`
   (snapshot HL + LMPR to cursor), `in_normalise_hl` (COMET-style
   adjustpo; while H >= &40, subtract &40 from H and bump LMPR's
   low 5 bits).
5. **Pass 1 → pass 2 rewind.**  `reset_reader_to_in_buf` sets
   cursor to `(LMPR_IN_BASE, 0)` and re-walks the header via
   `reader_init`.  No disk re-read between passes.
6. **Litpool cross-pass fix.**  Pass 1's `expr_ptr` would have
   pointed into the per-record STAGING_BUF (overwritten on the
   next `reader_next_kind` call).  `litpool_register` now copies
   the expr bytecode into `LITPOOL_EXPR_BUF` (= `&D900`, 2 KB
   bump-allocator in section D) at registration time.  Pass 2's
   flush reads from the page-stable copy.
7. **Boot self-test.**  `test_reader_paged.asm` exercises (a) the
   page-cross helper (`in_normalise_hl` with HL=`&7FFE` → bumped
   LMPR, HL=`&3FFE`); (b) a synthetic record fetch (stamp a
   15-byte ".tbn" blob into page 7 via an LMPR-bracket LDIR, then
   reset_reader_to_in_buf + reader_next_kind, assert payload bytes
   match in STAGING_BUF); (c) post-read LMPR check (= `LMPR_ENCTAB`
   on return).
8. **Fixture.**  `tests/m6/sources/in_long_source.s` is a 44-line
   fixture: 20 long-comment records (800 B payload each) plus one
   shorter comment that pushes the .tbn just past the 16 KB
   section-A page boundary.  Total .tbn ~16.5 KB.

### Deviations from the plan

| What | Why |
|---|---|
| `in_normalise_hl` called after LDI loop (not just inside) | Without this, a record whose last payload byte lands at section-A offset `&3FFF` leaves the cursor at `(old_page, &4000)` — same logical position as `(next_page, &0000)` but byte-wise different.  `reader_at_end` then compares page-wise and falsely reports "not at end" → infinite loop.  Found via the long-source fixture.  Fix: call `in_normalise_hl` before `in_persist_hl`, ensuring canonical cursor form. |
| Fixture sized at ~16.5 KB rather than the plan's "4000 add instructions" | Two reasons: (1) the 4000-add fixture produces ~28 KB of .tbn — within the 64 KB ceiling but per-record encode cost overruns the SimCoupé 30s timeout in the dev-container path.  (2) Empirically the trampoline-HLOAD's section-D spillover near `&C100` becomes sensitive past ~16.6 KB — see Caveat below.  The 16.5 KB fixture still exercises the reader's intra-record page-cross + multi-page HLOAD path. |

### Caveat — trampoline-HLOAD effective range

The trampoline calling convention sets `HMPR = IN_BASE_PAGE` during
the HLOAD call.  HMPR=7 makes section C = page 7 and section D =
page 8.  During HLOAD, SAMDOS's internal `ctas` writes can spill
into section D before catching up at the &C000 boundary; with the
stack at SP=&C100 (page 8 offset &0100), HLOAD writes >~256 bytes
past the page boundary into the stack region can corrupt RST 8
return-state.  Empirically, .tbn files up to ~16626 bytes load
cleanly; > 16640 bytes hangs the assembler.

This puts the **effective IN ceiling at ~16.5 KB**, not the 64 KB
the design intended.  Lifting this requires either moving the
stack to an HMPR-stable location or changing the multi-page HLOAD
strategy.  Tracked as a follow-up; the current PR's fixture stays
inside the safe range while still exercising both the load-time
multi-page HLOAD path AND the reader's intra-record page-cross.

### Test status (all green)

| Layer | Status | Notes |
|---|---|---|
| `make m3-asm-prod` | ✅ PASS | 12058 B / 12288 B (230 B headroom) |
| `make ci-m3` | ✅ PASS | 9/9 |
| `make ci-m3-prod` | ✅ PASS | 9/9 |
| `make ci-m4` | ✅ PASS | 4/4 |
| `make ci-m4-prod` | ✅ PASS | 4/4 |
| `make ci-m5` | ✅ PASS | 19/19 |
| `make ci-m5-prod` | ✅ PASS | 19/19 |
| `make ci-m6` | ✅ PASS | 2/2 (`inst_long_emit` + `in_long_source`) |
| `make ci-m6-prod` | ✅ PASS | 2/2 |
| Boot self-tests | ✅ PASS | + paged-reader self-tests (page-cross + synthetic record fetch + post-read LMPR check) |

### Code budget

| variant | size | budget | headroom |
|---|---|---|---|
| `m3-asm-prod` (no self-tests) | 12058 B | 12288 B (`&8000-&AFFF`) | **230 B** |

Net cost of M6 PR 2: 152 B in production (11906 → 12058).  Comfortably
under the 12200 B watch threshold the verification subagent set;
plenty of room for M6 PR 3+ work.

## What's NOT in M6 PR 2 (still pending)

- **Paged IN > 16.5 KB** — caveat above.  Requires either an
  HMPR-stable stack relocation or a multi-page load strategy that
  doesn't write into section D.  Tracked for follow-up.
- **Compact `.tbn` format** — separate strand; needed for real
  spectrum4 sources to fit in SAM RAM.
- **On-SAM disassembler** — follows compact `.tbn`.
- **Multi-digit local labels** (`10f` / `10b`) — independent draft
  PR #35.
- **64 KB output limit** — 16-bit `OUT_LEN`; debug builds (~274 KB)
  need M7+.
- **`(hksp)` error handler** — HSAVE / HGTHD / HLOAD longjmp on
  error; the assembler crashes.  Same behaviour as M3..M5.  Out
  of scope.

## Hand-off recipe (verify locally)

```bash
# Inside the sam-aarch64 dev container or with toolchain locally:
make ci-m3 ci-m4 ci-m5 ci-m6
# expect:
#   9/9 M3 fixtures matched
#   4/4 M4 fixtures matched
#   19/19 M5 fixtures matched
#   1/1 M6 fixtures matched

# Production-variant gate (the standing CI matrix):
make ci-m3-prod ci-m4-prod ci-m5-prod ci-m6-prod
# expect identical pass counts
```

Or via Docker (mirrors GH Actions):

```bash
docker run --rm -v "$(pwd):/work" -w /work \
  -e SDL_VIDEODRIVER=x11 -e SDL_AUDIODRIVER=dummy \
  -e AARCH64_AS=aarch64-linux-gnu-as -e AARCH64_LD=aarch64-linux-gnu-ld \
  -e AARCH64_OBJCOPY=aarch64-linux-gnu-objcopy \
  ghcr.io/petemoore/sam-aarch64-dev:latest \
  bash -c '
    Xvfb :99 -screen 0 1280x1024x24 >/dev/null 2>&1 &
    sleep 1; export DISPLAY=:99
    rm -rf build/
    make ci-m3 ci-m4 ci-m5 ci-m6 ci-m3-prod ci-m4-prod ci-m5-prod ci-m6-prod
  '
```

## Authoritative references

- M6 PR 1 design spec: `docs/specs/2026-05-27-m6-paged-out-design.md`
- HSAVE call-site design note: `docs/specs/2026-05-27-samdos-save-idiom.md`
- M6 PR 1 plan: `docs/plans/2026-05-27-m6-paged-out.md`
- M5 status (prior milestone): `docs/notes/m5-status.md`
- M4 status (older): `docs/notes/m4-status.md`
- M3 status (older): `docs/notes/m3-status.md`
- Compact `.tbn` + disassembler design (later M6 PRs):
  `docs/specs/2026-05-27-compact-tbn-and-disassembler-design.md`
- ROADMAP: `docs/ROADMAP.md`
