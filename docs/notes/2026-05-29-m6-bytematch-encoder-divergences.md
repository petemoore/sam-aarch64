# M6 release byte-match — SAM-side encoder divergences (2026-05-29)

Status: **FULL BYTE-MATCH ACHIEVED (2026-05-29).** All encoder
divergences are closed: the SAM-side assembler's OUT now byte-matches
the GNU oracle on the spectrum4 release image (`cmp build/release.gnu.img
build/release.sam.bin` → exit 0, no differences). This note is the
authoritative tracking artifact for the encoder bugs the byte-match
surfaced; every class below is now done.

## TL;DR

Driving the full 88 KB `release-stripped.tbn` through the SAM-side
assembler **on SimCoupé** (the authoritative gate) revealed two
separate things:

1. **The "trap at scale" open question is answered — NEGATIVE.** The
   88 KB source flows end-to-end through SimCoupé cleanly: OK banner,
   21 752-byte OUT, 21 s wall-clock. The Go-harness trap (PC → `&0038`)
   reported in the 2026-05-28 handover is therefore a **Go-harness
   fidelity gap, NOT a real SAM paged-IN bug**. (Tracked as its own
   work item — see "Go-harness fidelity follow-up" below.)

2. **The byte-match is NOT yet achieved — it surfaced ~7 distinct
   SAM-side encoder bug classes** that the M3–M6 fixture corpus never
   exercised. The assembler completes without error but silently emits
   wrong bytes for several immediate / address / condition encodings.
   **118 of 5 438 instruction words differ** (358 bytes); everything
   else is byte-identical to GNU.

This is the byte-match doing exactly its job: catching real encoder
bugs that 36/36 green fixtures missed.

## Method / oracle chain (how we know it's a SAM bug, not the oracle)

- **Fresh GNU oracle == committed `release.img`** (`scripts/build-spectrum4-release.sh`
  rebuilds `as + ld -T spectrum4.ld + objcopy` from current HEAD;
  sha `a93f0a48…`). Oracle is current, not stale.
- **refenc (Mac-side reference encoder) on the *identical* stripped
  `.tbn` == GNU oracle** (byte-identical). So the `.tbn`,
  `text2bin -strip-comments`, and the Go encoder authority are all
  correct.
- **SAM-side OUT on the same stripped `.tbn` differs** at 118 words.
  Since refenc and the SAM assembler consume byte-identical input and
  refenc matches GNU, **the divergence is purely the Z80 encoder.**

Reproduce:
```bash
make release-stripped-tbn                      # build/release-stripped.tbn (88 644 B)
make m3-asm-prod enctab sysreg-data build-m3-disk
./build/build-m3-disk -sysreg-data build/sysreg_data.bin \
    build/assembler-prod.bin build/enctab.enc \
    build/release-stripped.tbn build/release-stripped.mgt
# SimCoupé in the dev container (timeout must exceed 30 s — use the new SIMCOUPE_TIMEOUT):
docker run --rm -v "$(pwd):/work" -w /work -e SDL_VIDEODRIVER=x11 \
    -e SDL_AUDIODRIVER=dummy -e SIMCOUPE_TIMEOUT=900 \
    ghcr.io/petemoore/sam-aarch64-dev:latest bash -c \
    'Xvfb :99 -screen 0 1280x1024x24 >/dev/null 2>&1 & sleep 1; export DISPLAY=:99; \
     tools/run-simcoupe.sh build/release-stripped.mgt build/release-stripped.status.log'
samfile cat -i build/release-stripped.mgt -f OUT > build/release-stripped.sam.bin
bash scripts/build-spectrum4-release.sh        # builds build/release.gnu.img
cmp -l build/release.gnu.img build/release-stripped.sam.bin   # 358 differing bytes
```

For fast per-bug iteration, refenc is the oracle on any `.tbn`, and the
Go harness (`tools/z80-test-harness-go/`) runs small fixtures in ~1 ms
(it only *traps* on the full-88 KB paged-IN load, not on small inputs).

## AUTHORITY FOR ALL FIXES — port from the Go implementation, do not reinvent

**Binding rule (Pete, 2026-05-29):** the Go encoder
(`tools/aarch64enc/` + `tools/sam-aarch64-format/` + `tools/refenc/`) is
the faithful, binutils-equivalent oracle. It was written *first*,
specifically to inform the Z80 port. **Where a Z80 encoding is wrong,
the fix is to read the corresponding Go function and port it faithfully
to Z80 — the Go code says exactly what to do. Nothing is to be
re-derived from the ARM ARM or re-invented.** Any agent (sub-agent or
otherwise) that touches these encodings MUST cite the Go function it is
porting in the commit message.

Per-class Go authority (read these before fixing):

| Class | Go authority |
|---|---|
| 2 MOV wide-imm decomposition | `tools/aarch64enc/encode.go:41-66` (`hw := (Imm>>16)&3`; the `hw==0 && (Imm<0 \|\| Imm>0xffff)` chunk-search loop sets `hw=shift/16`, `imm16=chunk`); `slots_imm.go:24-34` (`encodeImm16Shifted`, hw at BitPosition+16) |
| 3,4 MOV→MOVN / MOV→bitmask alias selection | `tools/refenc/pass2.go` MOV-alias resolution (which of MOVZ/MOVN/ORR-bitmask a bare `mov Rd,#imm` becomes); `tools/aarch64enc/manual_forms.go` mov/movn/movk forms |
| 3,7 bitmask (N:immr:imms) immediate | `tools/aarch64enc/slots_logical.go`; `tools/refenc/pass2.go:468` (inverted-mask / 32-bit-mask handling) |
| 5 ADRP page-delta | `tools/aarch64enc/slots_adrp.go` (full algorithm incl. origin/page handling) |
| 6 csetm condition invert | `tools/refenc/pass2.go:308,1420-1424` (`csinv Rd,xzr,xzr,!cond`; condition bit XOR 1); `manual_forms.go:416-417` |
| 1 64-bit address data | `tools/refenc/pass2.go` + `tools/sam-aarch64-format/` data-directive / symbol-value width (how the full 64-bit origin-relative symbol value is emitted for `.quad`) |

## Bug inventory (the work items)

Counts are of differing 4-byte instruction words. Examples give the
output offset, the GNU (correct) text, and the SAM (wrong) text.

| # | Class | Count | Fix status |
|---|---|---|---|
| 1 | 64-bit address data — high word truncated to 0 | 55 | ✅ done (2026-05-29) |
| 2 | MOV (wide immediate) — wrong 16-bit chunk / `hw` shift | 30 | ✅ done (2026-05-29) |
| 3 | MOV (bitmask immediate) alias unhandled | 10 | ✅ done (2026-05-29) |
| 4 | MOV (inverted wide immediate) → MOVN; emits invalid encoding | 7 | ✅ done (2026-05-29) |
| 5 | ADRP page-delta sign/high-bit wrong under large origin | 13 | ✅ done (2026-05-29) |
| 6 | `csetm` condition not inverted | 2 | ✅ done (csetm commit) |
| 7 | Logical (bitmask) immediate value wrong (`bic`-imm) | 1 | ✅ done (2026-05-29) |
| — | stray `mov x9, RAM_DISK_SIZE` (`.set` constant + ORIGIN_HIGH) | 1 | ✅ done (2026-05-29) |

**Release-diff progress:** 358 (initial) → 356 (after csetm, Class 6) →
235 (after Classes 2+3+4) → 16 (after Class 1) → 3 (after Class 5, ADRP) →
2 (after the `.set`-absolute stray fix) → **0 (FULL BYTE-MATCH)** after
Class 7 (`bic`-immediate).

### Class 5 — ADRP (done)

Under origin `0xfffffff0_00000000` an adrp page-delta whose low 32 bits
have bit 31 set (e.g. `0xff841000`) is POSITIVE in 33-bit space (bit 32
clear); the 32-bit-only encoder did an arithmetic `>>12` that mis-signed
it.  Fix: compute the page-difference in full 64-bit width
(target = BCDE-low ++ ORIGIN_HIGH-high, pc = PASS_PC ++ ORIGIN_HIGH —
the origins cancel), mask to 33 bits, sign-extend from bit 32, then the
`>>12`/pack tail.  Ports `tools/refenc/pass2.go:363-369` +
`tools/aarch64enc/slots_adrp.go:11-24`.  The boot self-test assertion in
`test_slots.asm` for target `0xFFFFF000` was stale (it assumed
32-bit-signed operand semantics → `0x60FFFFE0`); corrected to the
faithful `0x607FFFE0` (matches refenc `adrp x0,0xFFFFF000`).  ORIGIN_HIGH
is now zeroed at cold boot so the pre-`main_assemble` self-tests see a
defined value.  Fixture: `tests/m6/sources/inst_adrp_highorigin.s`.

### Stray `mov x9, RAM_DISK_SIZE` (done)

The Class-1 fix re-applied ORIGIN_HIGH to EVERY symbol at eval time —
correct for labels, wrong for `.set`/`.equ` ABSOLUTE constants.
`mov x9, RAM_DISK_SIZE` (`.set RAM_DISK_SIZE,0x10000000`) saw
`0xfffffff0_10000000`, failing the MOV single-chunk decomposition and
emitting `mov x9,#0` (`d2800009` vs GNU `d2a20009`).  Fix: a 64-byte
per-id `SYMTAB_ABS_BITMAP`; `main_dir_equ_pass1` marks a symbol absolute
when its evaluated high word != ORIGIN_HIGH; `eval_push_sym` zero-fills
the high word for absolute symbols and re-applies ORIGIN_HIGH only for
origin-relative ones.  Faithful to Go (Symbols[name] holds the full
value, eval adds no origin: `pass1.go:154/296`, `pass2.go:150`).
Fixture: `tests/m6/sources/inst_mov_setconst.s`.

### Class 7 — `bic` immediate (done)

`bic Rd,Rn,#imm` = `and Rd,Rn,#~imm`.  The generic form-table path fed
the AND-imm encoding the RAW immediate, so `bic w7,w7,#1` emitted
`and w7,w7,#0x1` (`120000e7`) vs GNU `and w7,w7,#0xfffffffe`
(`121f78e7`).  Fix (csetm-style in-place mutate + fall-through): negate
operand 2 (`~imm`) for the (Rd,Rn,#imm) shape of bic, then let the
generic encoder produce the AND.  Ports `tools/refenc/pass2.go:304`
(case 47) + `encodeBicImm` (`pass2.go:1397`).  Fixture:
`tests/m6/sources/inst_bic_imm.s`.

Class-1 sample offsets `0x169c` / `0x38c4` byte-match (confirmed gone).
The Class-2/3/4 fix is a single MOV-alias
auto-selection intercept (`encode_mov_imm_word` in `src/m3/intercepts.asm`),
a faithful port of `tools/refenc/pass2.go:438-502` (`tryEncodeMovImm`):
MOVZ chunk-search → MOVN chunk-search → ORR-bitmask, in that priority
order (the same order GNU as uses).  All targeted offsets (0x3c, 0x78,
0x170, 0x390, 0x648, 0x794, 0x844, 0xa20, 0xc6c, 0x4c20, 0x488c, 0x4898 …)
now byte-match.  Fixtures: `tests/m6/sources/inst_mov_{wide_imm,movn,bitmask}.s`.

Note on Class 3: several values the bug inventory below listed under
Class 3 (e.g. `mov x0, #0xfffffffffffffffd`, `mov w7, #0xfffffffe`) are
in fact resolved by GNU as as **MOVN**, not ORR-bitmask — verified with
`aarch64-none-elf-as`.  The MOV-alias selection order handles them
correctly regardless of which sub-class they fall in.  Class 7
(`and w7, w7, #0xfffffffe`) is a separate operand-evaluation issue (the
bitmask *slot* encoder `encode_logical_imm` is a correct port and is
reused here for the MOV-bitmask path), left open for follow-up.

### Class 1 — 64-bit address data, high word truncated (55 sites)

`.quad`-style 64-bit address constants pointing into the
`0xfffffff0_xxxxxxxx` range. SAM emits the **low** word correctly but
`0x00000000` for the **high** word; GNU emits `0xfffffff0`.

```
@0x169c  low-word@0x1698 GNU=80b800fe SAM=80b800fe (match)   high GNU=f0ffffff SAM=00000000
@0x38c4  low-word@0x38c0 GNU=54440000 SAM=54440000 (match)   high GNU=f0ffffff SAM=00000000
```
Confirmed: low halves match, only the high halves differ.

**Root cause (confirmed 2026-05-29):** `PASS_PC` (`assembler.asm:99`) is a
4-byte counter — it tracks only the *low* 32 bits of the VMA. The link
origin enters via the leading `.org 0xfffffff0_00000000` that
`text2bin -flatten -origin` emits, but `main_dir_org_set_pc`
(`main_loop.asm`) copied only `expr_result[0..3]` into PASS_PC, dropping
the high word `0xfffffff0`. Symbol values are PASS_PC snapshots
(`symbols.asm` 8-byte entry: id u16 + address u32 + next_off u16 — only
32 address bits). So `eval_push_sym` zero-extended the 32-bit address to a
64-bit eval value, and `.quad`'s 8-byte emit (`main_dir_quad_emit`,
faithful) wrote high = 0. The data-emit path was correct; the truncation
was upstream in PASS_PC / symbol-value width.

The Go reference never truncates: `pc` starts at `OriginVMA`
(`refenc/pass2.go:18`), every label value is `res.Symbols[name] = pc`
(`pass1.go:154`), `makeCtx` exposes Symbol/PC/LocalLabel as that full
64-bit value (`pass2.go:148-152`), and `.quad` emits all 8 bytes via
`evalImmsAsBytes` (`pass2.go:1775`). Constants (`.quad 0x93`) are literal
values that never carry the origin — exactly mirrored.

**Fix (this work item):** added a 4-byte `ORIGIN_HIGH` scratch
(`assembler.asm`, `&C960`, RAM only — no binary-size cost). `.org`'s
set-pc tail now stashes `expr_result[4..7]` into it (LDIR); `pass_pc_reset`
clears it (so the `-Ttext=0` fixture corpus, origin 0, is unchanged). The
three origin-relative eval pushes — `eval_push_sym`, `eval_push_pc`,
`eval_push_local` (`expr_eval.asm`) — now write `ORIGIN_HIGH` into the
eval-stack slot's high 4 bytes (via the shared `eval_store_origin_high`
helper) instead of zero-filling, materialising the full 64-bit
origin-relative value. Faithful port of the Go origin-carrying `pc`
semantics. Verified: `tests/m6/sources/inst_quad_addr.s` SAM-vs-refenc at
`-origin 0xfffffff000000000` byte-matches; release diff 235 → 16 with the
55 Class-1 sites gone.

### Class 2 — MOV (wide immediate), wrong chunk/shift (30 sites)

`mov Xd/Wd, #imm` where the non-zero 16-bit chunk is not in the low
position. SAM drops it.

```
@0x3c    GNU mov x0, #0x80000000        SAM mov x0, #0x0           (0x8000<<16)
@0x170   GNU mov x2, #0x600000000       SAM mov x2, #0x0           (0x6<<32)
@0x648   GNU mov w0, #0x100000          SAM mov w0, #0x0           (0x10<<16)
@0xa20   GNU mov w1, #0x10000           SAM movz w1, #0x0, lsl #16 (0x1<<16; shift kept, imm lost)
@0xc6c   GNU mov x9, #0x100000000       SAM mov x9, #0x0           (0x1<<32)
```
Inconsistent failure (sometimes `hw` kept with imm=0, sometimes both
lost) → the MOV→MOVZ immediate decomposition (find the single non-zero
16-bit chunk, set `imm16`=chunk, `hw`=chunk index) is computing the
chunk/shift wrongly. Explicit `movz Xd,#imm,lsl#n` fixtures pass; the
**MOV-alias decomposition** path is the bug.

### Class 3 — MOV (bitmask immediate) alias unhandled (10 sites)

Immediates that GNU encodes as `MOV (bitmask immediate)` (i.e. `ORR
Xd, XZR, #bimm`) — repeating bit patterns.

```
@0x390   GNU mov x15, #0xcccccccccccccccc   SAM mov x15, #0xcccc
@0x488c  GNU mov x4,  #0xff00ff00ff00ff00   SAM mov x4,  #0xff00
@0x4898  GNU mov x5,  #0xff00ff00ff00ff      SAM mov x5,  #0xff000000000000
@0x4c20  GNU mov x0,  #0xfffffffffffffffd   SAM mov x0,  #0xfffd000000000000
```
SAM falls back to a (wrong) wide-immediate encoding instead of
recognising the bitmask-immediate alias. Likely shares the bitmask
encoder with Class 7.

### Class 4 — MOV (inverted wide immediate) → MOVN (7 sites)

`mov Wd, #imm` where GNU picks MOVN (e.g. all-ones / small-negative).
SAM emits an **invalid** encoding (`hw=11` on a 32-bit op →
`.inst 0x52ffff..; undefined`).

```
@0x78    GNU mov w1, #0xffffffff (= movn w1,#0)   SAM .inst 0x52ffffe1 ; undefined
@0x844   GNU mov w0, #0xfffffffd                  SAM .inst 0x52ffffa0 ; undefined
@0x794   GNU mov w8, #0x8fcfffff                  SAM .inst 0x52ffffe8 ; undefined
```
The MOV→MOVN path (when the value can't be a single MOVZ but its
inverse can) is missing/broken; worse, the result is not a legal
instruction. **Functional-correctness risk, not just byte-match.**

### Class 5 — ADRP page-delta wrong under large origin (13 sites) — ✅ DONE (see TL;DR Class 5 above)

```
@0x58    GNU adrp x2, 0xff841000      SAM adrp x2, 0xffffffffff841000
@0x6c8   GNU adrp x1, 0xfe104000      SAM adrp x1, 0xfffffffffe104000
@0x74c   GNU adrp x10,0xfd500000      SAM adrp x10,0xfffffffffd500000
```
The `immhi` sign bit is set in SAM but not GNU — the 21-bit page-offset
is being computed/sign-extended wrong under origin
`0xfffffff000000000`. The M4 `pcrel`/`adrp` fixtures use `-Ttext=0`
(small origin) so never exercised the high-origin path.

### Class 6 — `csetm` condition not inverted (2 sites)

```
@0x4984  GNU csetm w7,  ne     SAM csetm w7,  eq
@0x498c  GNU csetm w24, ne     SAM csetm w24, eq
```
`csetm Wd,cond` = `csinv Wd,WZR,WZR,invert(cond)`. SAM is not inverting
the condition (or inverting the wrong field). Small, self-contained.

### Class 7 — logical (bitmask) immediate value wrong (1 site) — ✅ DONE

```
@0x51a0  GNU and w7, w7, #0xfffffffe    SAM and w7, w7, #0x1
```
Root cause was NOT the bitmask slot encoder (it is a correct port) but
the `bic`-immediate alias: the source is `bic w7, w7, #1`, which is
`and w7, w7, #~1` = `and w7, w7, #0xfffffffe`.  The generic form-table
path encoded the AND with the RAW immediate (no negate).  Fixed by
negating operand 2 for the bic-immediate shape — see the "Class 7" entry
in the TL;DR above.

## Fix plan (ordering)

Use TDD per bug: minimal fixture → reproduce divergence vs refenc/GNU →
fix Z80 → verify byte-match (harness fast, SimCoupé authoritative) →
re-run full release byte-match to watch the diff count fall. Commit per
class. Watch the prod/test code budgets (`&C000` ceiling; see
`memory/feedback_test_variant_fragility.md`).

Suggested order (self-contained & high-confidence first):
1. **Class 6 (csetm)** — tiny, isolated condition inversion.
2. **Class 2 (MOV wide-imm decomposition)** — 30 sites, one routine.
3. **Class 4 (MOV→MOVN)** — fixes invalid encodings (correctness, not
   just bytes); pairs with Class 2 (shared MOV-alias selection).
4. **Class 3 + 7 (bitmask immediate)** — shared encoder; do together.
5. **Class 1 (64-bit data high word)** — 55 sites; needs symbol-value
   width / origin investigation.
6. **Class 5 (ADRP high origin)** — page-delta arithmetic under 64-bit
   origin.

Each class needs a permanent fixture added to `tests/m6/sources/` (or
m5) so the corpus covers it going forward — these gaps existed because
no fixture used these forms. **A focused fixture per class is the
durable regression guard; the full release byte-match (PR-5 CI gate) is
the backstop.**

## Go-harness fidelity follow-up (separate work item)

The Go harness trapped (PC → `&0038`) on the full 88 KB paged-IN load
where SimCoupé succeeds. Root-cause why the harness's HLOAD / paging
stub diverges from SimCoupé at ≥6-page IN loads, and fix it so the
harness can run the full release input (it would make the iteration
above much faster). **Not on the critical path** (SimCoupé is the gate
and works), but worth understanding. Tracked in `m6-status.md` /
`m7-status.md`. Pete's call: ideally M6, acceptable M7.

## Final whole-branch review — follow-ups (2026-05-29)

A final review of the cumulative branch diff (each Z80 port read against
its cited Go function, both variants rebuilt, encodings cross-checked via
refenc) found **no CRITICAL issues** — the ports are faithful, the shared
eval-path changes (ORIGIN_HIGH / SYMTAB_ABS_BITMAP) interact correctly
with the MOV and ADRP fixes, the soft-fail reject mechanism is
stack-balanced, and the budget reclaim lost no coverage. Report-only
follow-ups (none block the milestone):

1. **CI cannot yet catch a regression in the origin-dependent logic.**
   The three new fixtures (`inst_quad_addr.s`, `inst_adrp_highorigin.s`,
   `inst_mov_setconst.s`) only manifest their bug at the release origin
   `0xfffffff0…`; the m6 SimCoupé roundtrip links at `-Ttext=0`, where
   they pass trivially. The only real guard is the **full release
   byte-match, which is not wired into CI** (`tools/run-m6-release-stripped.sh`
   is a manual driver). Until **PR-5** (the `m6-release` gate) lands, the
   ORIGIN_HIGH / abs-bitmap / 33-bit-ADRP logic can silently regress with
   CI green. → Raises PR-5 priority; tracked in `m6-status.md`.
2. **Latent (non-release) edge:** an absolute `.set X, 0xfffffff0_NNNNNNNN`
   whose high word coincidentally equals ORIGIN_HIGH is misclassified
   origin-relative (the Z80 stores only the low 32 bits + reconstructs
   ORIGIN_HIGH; Go stores the full value). Harmless for release (its
   `.set` constants are ≤32-bit), but a divergence-from-Go on inputs the
   release doesn't exercise. → Fold into the M7 "Z80↔Go encoding/operator
   parity audit" (`m7-status.md`).
3. **Latent fragility (pre-existing, not new):** `run_slot_self_tests`
   computes the ADRP test as `target − PASS_PC` before `pass_pc_reset`,
   relying on cold-boot RAM being 0 at PASS_PC. The old 32-bit test had
   the identical dependency. An explicit `pass_pc_reset` before the
   page-12 self-test cluster would harden it. → M7 housekeeping.
4. **MINOR (fixed):** the `assembler.asm` section-D memory-map comment
   said `&F000-&FFFF free`; now records the 27-byte MOV/logical-imm
   scratch reservation. (Feeds the M7 canonical-memory-layout doc.)
5. **MINOR:** `build-spectrum4-release.sh` toolchain fallback only probes
   `$AS`; if `AARCH64_AS` is set but LD/OBJCOPY are not, the others keep
   their none-elf defaults. Matches the documented override contract;
   low impact.

## References
- `scripts/build-spectrum4-release.sh` — oracle builder (refenc vs GNU).
- `tools/refenc/` — Mac-side reference encoder (the per-`.tbn` oracle).
- `src/m3/encoder.asm`, `src/m3/main_loop.asm` — Z80 encoder + directives.
- `docs/notes/m6-status.md` — milestone status (this work is the headline closer).
- `memory/feedback_test_variant_fragility.md` — budget cliff to respect while editing.
