# Plan — i31b-b3b: real SAM `.include` reader + SimCoupé exercise

Ephemeral execution plan (delete in the completing PR). Ports the `.include`
file-access vector of the on-SAM preprocessor from a memory-backed provider
(b3a, done) to the real SAM disk loader, and exercises it end-to-end.

Source of truth for the design: `docs/specs/on-sam-preprocessor-design.md` §6
brick 3 (spec APPROVED, q70 closed). This plan is the port-level detail.

## What b3a already delivered (the surface we bind to)

`src/asmprep.asm` has a **pluggable reader vector**, set at init (no conditional
assembly):

- Call site: `prep_build_and_try` (`asmprep.asm:2735`) sets the reader input
  (`REQ_NAME_PTR`, `REQ_NAME_LEN` — 16-bit, reader requires <256) then tail-jumps
  `prep_call_reader` = `ld hl,(READER_VEC); jp (hl)` (`asmprep.asm:2744`).
- **Reader ABI** (from `prep_mem_reader`, `asmprep.asm:2824`):
  - In: `REQ_NAME_PTR`/`REQ_NAME_LEN` = the resolved candidate name.
  - Out: **CF=1** with `READ_CONTENT_PTR`/`READ_CONTENT_LEN` set to the file
    bytes on a hit; **CF=0** on a miss (`or a; ret`, `asmprep.asm:2878`).
  - Returns via `ret` to the `handleInclude` search loop, which tries the next
    candidate on CF=0 and funnels to `prep_fail` (clean error) if all miss.
- Default init: `prep_run` sets `(READER_VEC)=prep_mem_reader` if zero
  (`asmprep.asm:136`). The SAM build points it at the real reader before
  `prep_run` — the exact hook this brick fills.

## The two hard problems (why this is not a one-shot port)

1. **The section-C collision.** The trampoline-HLOAD (`load_in_file` pattern,
   `main_loop.asm:1612`) requires `HL ∈ &8000..&BFFF` (section C) and lands the
   file in the physical page mapped there via HMPR (`trampoline.asm:8-31`).
   `asmprep.asm` runs org &8000 flat (32 KB, sections C+D) in the standalone
   oracle harness — so a real HLOAD would clobber prep's own section-C half.
   The spec's answer (§5, §6.4): the on-SAM prep runs in a **paged window**
   (b8d two-image pattern) with buffers **from the i2 pool (PP_PREP tag)**,
   leaving C/D free for the trampoline. That paged image is a substantial port
   the spec assigns to **b4** ("own paged image"), and b3b "likely shares the
   SAM-runnable asmprep harness with b4."

2. **Not-found must not longjmp to BASIC.** The reader contract is CF=0 on miss.
   But HGTHD on a missing file does `jp nz,rep26 → derr` which (with `(hksp)==0`,
   which the hook dispatcher forces — `samdos/src/b.s:451`; and confirmed off by
   `samdos-file-io.md:569`, i185) restores SP to `(entsp)` and pops into BASIC's
   error path — a crash for a `di/halt` top-level. The sanctioned recovery is the
   **DOSER (`&5BC0`) epilogue**: ROM PTDOS's `DOSC` does `LD HL,(DOSER); JP (HL)`
   after *every* hook with `A` = SAMDOS error number (0 = success)
   (`samdos-file-io.md:562`). The assembler already installs a persistent
   DOSER→`fail` handler (`assembler.asm:373`, body `:921`). For the reader we
   need a **temporary DOSER bracket** around the HGTHD that converts a not-found
   (A≠0) into "return to the reader with CF=0", restoring the prior vector after.
   No existing code brackets a single hook that way — this is new (small) ground.

## Decomposition (this brick splits into three tracked units)

- **b3b-b1 — the real reader routine** (this plan, mergeable now, unblocked).
  `prep_sam_reader`: name-map → UIFA+HGTHD+trampoline-HLOAD → CF=1 with content;
  not-found → CF=0 via a temporary DOSER bracket. Proven in the SAM-fidelity
  harness (`tools/z80-test-harness-go/`, which models HGTHD/HLOAD + DOSER) with a
  small driver — independent of the paged prep image.
- **new item — SAM-runnable paged prep image + driver harness** (the shared
  foundation the spec folds into b4). asmprep in a paged window with PP_PREP
  i2-pool buffers, running `prep_run` on the SAM under the SAM-fidelity harness +
  SimCoupé with the memory reader. Blocks b3b-b2 and b4.
- **b3b-b2 — end-to-end real-HLOAD exercise** (depends on b3b-b1 + the harness
  item). Bind `READER_VEC=prep_sam_reader` in the paged prep build, add an
  include file to the test disk (`build-disk` + Makefile), exercise `.include`
  from real DOS under SimCoupé.

## Open decisions raised to Pete (qN) — proceeding with faithful defaults

- **Name mapping** (POSIX include path → SAM 10-char catalogue name). Default:
  copy the include string verbatim into the UIFA 10-char name field (space-pad,
  4-space "ext" like `name_IN`), error if >10 bytes; no case fold. SAM names may
  contain `.`, so `.include "utils.s"` → SAM file `UTILS.S`. Revisit if Pete
  wants extension-splitting or case-folding.
- **HLOAD vs LBYT.** Spec says trampoline-HLOAD (load_in_file pattern); we follow
  it. (LBYT byte-streaming would sidestep the section-C collision but deviates
  from the approved mechanism — noted in the qN, not taken unilaterally.)

## b3b-b1 implementation steps (exact)

### Step 1 — `prep_sam_reader` routine (append to `src/asmprep.asm`)

Guarded so it only assembles into a SAM build (not the flat oracle), e.g. behind
`if defined(PREP_SAM_READER)`. ABI exactly matches `prep_mem_reader`.

1. **Name map** `REQ_NAME_*` → a 15-byte name block at a scratch `PREP_NAME_BLK`
   (type 19, 10 name, 4 ext): reject `REQ_NAME_LEN > 10` (CF=0 — treat as miss so
   the search loop / prep_fail path stays clean); else `ld a,19` type, copy
   `REQ_NAME_LEN` bytes, space-pad name to 10, 4 spaces ext.
2. **Install temporary DOSER bracket**: save `(&5BC0)` into `PREP_DOSER_SAVE`;
   set `(&5BC0)` = `prep_reader_doser` (a position-independent handler in a
   section-B-resident copy, mirroring `doser_handler_body`/`assembler.asm:921`).
   Save SP into `PREP_READER_SP`. The handler: `and a; ret z` (success → resume);
   else `ld sp,(PREP_READER_SP); scf ccf` (CF=0) `; jp prep_reader_notfound`.
3. `ld hl,PREP_NAME_BLK; call fill_uifa; rst 8; defb HOOK_HGTHD`. On not-found
   the DOSER bracket diverts to `prep_reader_notfound`.
4. Read geometry from DIFA (`&4B50+34..36`, mask `set 7,d`) as `load_in_file`
   does (`main_loop.asm:1636`). Include files are small (≤ one page for v1);
   assert `pages == 0` (tag/CF=0 otherwise for now) and take the length.
5. Alloc a target page (PP_PREP tag once the pool tag exists; for the b1 driver a
   fixed scratch page), re-issue `fill_uifa`+HGTHD, `di`, `call TRAMPOLINE_DST`
   with `HL=&8000`, `B=target page`, `C=0`, `DE=len`.
6. Restore `(&5BC0)` from `PREP_DOSER_SAVE`. Set `READ_CONTENT_PTR=&8000` (the
   section-C window with the target page mapped), `READ_CONTENT_LEN=len`, `scf`,
   `ret`. (Window/page hand-off to paged prep is finalised in b3b-b2.)

State bytes to add near the reader block: `PREP_NAME_BLK: defs 15`,
`PREP_DOSER_SAVE: defs 2`, `PREP_READER_SP: defs 2`.

### Step 2 — reader driver `src/prep_reader_driver.asm`

Small org-&8000 driver (pattern: the netboot routine drivers): `include sam_io.inc`,
`include trampoline.asm`, a minimal page-alloc (or fixed scratch page), the DOSER
handler body, and `src/asmprep.asm` with `-D PREP_SAM_READER=1` (reader compiled
in) `-D PREP_STANDALONE=1` off. Entry `prep_reader_probe`: set `REQ_NAME_PTR/LEN`
from a caller-planted name, `call prep_sam_reader`, snapshot CF + `READ_CONTENT_*`
to fixed cells the Go test reads.

### Step 3 — Makefile target `prep-reader-z80`

Mirror `asmprep-z80` (`Makefile:1583`): build `build/prep_reader_driver.bin` +
`.map`. Add to `netboot-z80-artifacts` (`Makefile:1729`) so the harness suite has it.

### Step 4 — SAM-fidelity harness test `tools/z80-test-harness-go/prep_reader_test.go`

Model: `boot_self_test_test.go` + `samdos_error_longjmp_test.go`.
- `TestPrepSamReaderHit`: `RunWithFiles` with a `NamedFile{Name:"INC1", Content:…}`
  registered; plant name "INC1" + call `prep_reader_probe`; assert CF=1,
  `READ_CONTENT_LEN`==len, and the bytes at the mapped window equal Content.
- `TestPrepSamReaderMiss`: `Config.StrictFileNotFound=true`, register no file (or
  a different name); assert CF=0 and no `pendingFault`/BASIC-crash — the DOSER
  bracket returned cleanly.
- No `t.Skip`: a missing `build/prep_reader_driver.bin` must `t.Fatal` (i253).

### Step 5 — verify + PR

`make prep-reader-z80 && go test ./tools/z80-test-harness-go/ -run PrepSamReader -count=1`.
Then the full local gate (`pyz80` builds + `go test ./...`), open the PR, watch CI
(SimCoupé matrix is the gate — the reader itself isn't SimCoupé-exercised until
b3b-b2, but the build must stay green), run the §3 pre-merge review, merge.

## Downstream (not this PR)

- New harness item: paged prep image (PP_PREP i2-pool buffers, b8d two-image),
  `prep_run` on SAM under SimCoupé with the memory reader. b4 + b3b-b2 depend on it.
- b3b-b2: bind `READER_VEC=prep_sam_reader`, disk include-file wiring, real-HLOAD
  `.include` under SimCoupé.
