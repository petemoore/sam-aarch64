# Plan — i370: SAM-runnable paged prep image + driver

Ephemeral (delete in the completing PR). Ports `asmprep.asm` to run in a
two-page LMPR window so prep runs paged on the SAM — the shared foundation
i371 (real-reader `.include` exercise) and i31b-b4 (chain wiring) build on.

Design source: `docs/specs/on-sam-preprocessor-design.md` §5/§6.4. Pattern to
mirror: `ASMPARSE_PAGED_BUFS` + `parse_paged_driver.asm` (the b8d brick-1 driver).

## Why paged, why two pages fit

Flat `asmprep.bin` is org &8000, 32 KB (sections C+D). i371's real reader
trampoline-HLOADs the include into section C, which would clobber prep. So prep
must move to an LMPR window (sections A/B), leaving C/D free. Measured layout
(`build/asmprep.map`): code+state `&8000..&90B8` (~4.4 KB); small buffers
`SET_TAB..PATH_ARENA` `&90B8..&AFBA` (~7.9 KB); big three `PREP_OUT` (8 K),
`PREP_SRC` (4 K), `PREP_FILES` (4 K) = **exactly 16 KB**. So:

- **Section A (page 8, &0000-&3FFF)** = the big three, `equ` `&0000/&2000/&3000`.
- **Section B (page 9, &4000-&7FFF)** = code (org &4000) + small buffers +
  `PREP_INCDIRS`/counts (~12.3 KB, fits 16 KB).

The i2-pool PP_PREP scaling for the full spectrum4 corpus is **b4's** job — the
oracle fixtures fit the window; the corpus does not.

## Step 1 — `-D ASMPREP_PAGED_BUFS` in `src/asmprep.asm`

Mirror `asmparse.asm:124-131` + `:4613-4637`.

- Org: after the `PREP_STANDALONE`/org guard, add
  `if defined(ASMPREP_PAGED_BUFS) \ org &4000 \ endif` (in the `else` of the
  STANDALONE guard).
- Buffers (in the data section): gate the big three so they become `equ` window
  addresses in the paged build, `defs` otherwise:
  ```
  if defined(ASMPREP_PAGED_BUFS)
  PREP_OUT:   equ &0000        ; page-8 window; 8 KB (ends &2000)
  PREP_SRC:   equ &2000        ; page-8 window; 4 KB (ends &3000)
  PREP_FILES: equ &3000        ; page-8 window; 4 KB (ends &4000)
  else
  PREP_OUT:   defs 8192
  PREP_SRC:   defs 4096
  ...
  PREP_FILES: defs 4096
  endif
  ```
  Keep `PREP_OUT_CAP`/`SET_TAB_MAX`/... equates unchanged. `PREP_PATH`,
  `PREP_INCDIRS`, `PREP_NFILES`, `PREP_NINCDIRS` stay `defs` (section B).
  NOTE: `PREP_FILES` is currently the last `defs`; splitting it out to an `equ`
  is fine (the `defs` order of the remaining buffers is preserved).
- Export a `.sym` (the driver imports `prep_run`, `PREP_ERR`, `PREP_OUT`,
  `PREP_SRC`, `PREP_FILES`, `PREP_NFILES`, `PREP_INCDIRS`, `PREP_NINCDIRS`,
  `PREP_PATH`, plus the output cursor — see driver).

## Step 2 — Makefile target `asmprep-paged-z80`

Mirror `asmparse-paged-z80` (Makefile:~1560s):
```
$(BUILD)/asmprep_paged.bin $(BUILD)/asmprep_paged.map $(BUILD)/asmprep_paged.sym: src/asmprep.asm $(asm_deps/src/asmprep.asm)
	@mkdir -p $(BUILD)
	pyz80 -D ASMPREP_PAGED_BUFS=1 --obj=$(BUILD)/asmprep_paged.bin \
	    --mapfile=$(BUILD)/asmprep_paged.map \
	    --exportfile=$(BUILD)/asmprep_paged.sym \
	    src/asmprep.asm
asmprep-paged-z80: $(BUILD)/asmprep_paged.bin ...
```

## Step 3 — `src/prep_paged_driver.asm`

Mirror `parse_paged_driver.asm` exactly. org &8000; `--importfile=asmprep_paged.sym`.
Entry `prep_paged_run`:
1. save boot LMPR (`in a,(&fa)`) + SP; `ld sp,&C0FE` (section D, HMPR-stable).
2. `ld a,&28; out (&fa),a` (LMPR=&28: A=page8, B=page9).
3. `ld bc,(src len — passed in BC by the test); call prep_run`.
   - Source pre-planted by the test into page 8 at `PREP_SRC` (&2000);
     `PREP_FILES`/`PREP_NFILES` pre-planted at &3000 for the memory reader.
   - `prep_run` sets `READER_VEC=prep_mem_reader` by default (no change).
4. snapshot into section-C cells: `PREP_ERR` byte, and output length =
   `BC` returned by `prep_run` (prep_run returns expanded byte count in BC —
   confirm against asmprep.asm:53-58 / the flat oracle contract).
5. restore LMPR + SP; `ret`.

## Step 4 — Makefile target `prep-paged-driver-z80`

Mirror `parse-paged-driver-z80`: depends on `$(BUILD)/asmprep_paged.sym`;
`pyz80 --importfile=$(BUILD)/asmprep_paged.sym ... src/prep_paged_driver.asm`.
Add both new targets to `netboot-z80-artifacts` (they load in the koron-go/z80
suite, so this is the correct aggregate).

## Step 5 — `tools/netboot-oracle/z80/asmprep_paged_test.go`

Mirror the b8i/b8j paged tests + reuse `asmprep_test.go`'s fixture harness
(`prepZ80Inc`/`prepGoInc`). For each oracle fixture:
- `z80h.Load(prep_paged_driver.bin, .map)` (+ import the paged sym addresses).
- Write source bytes into physical **page 8** at `PREP_SRC`'s offset (&2000);
  write the `PREP_FILES` memory table (+ `PREP_NFILES`, `PREP_INCDIRS`,
  `PREP_NINCDIRS`, `PREP_PATH`) into page 8 / page 9 at their sym offsets.
- `CallEntry("prep_paged_run", {BC: len})`.
- Read `PREP_ERR` + output length from the driver's section-C cells; read
  `PREP_OUT` bytes from physical page 8 (&0000).
- Byte-compare against host `frontend.Preprocess` (same fixtures as
  `TestAsmprepBrick3aInclude` / the brick-1/2 oracles). No `t.Skip` (i253):
  a missing `build/prep_paged_driver.bin` `t.Fatal`s.

Confirm the koron-go/z80 harness (`z80h`) models the LMPR=&28 page map so page-8
reads/writes hit physical page 8 (it does — that is how parse_paged_driver's
test reads `pager.RAM[8]`).

## Step 6 — verify + PR

`make asmprep-paged-z80 prep-paged-driver-z80 && cd tools/netboot-oracle/z80 &&
go test -run AsmprepPaged -count=1`. Then the full local gate, PR, CI, §3, merge.

## Downstream (not this PR)

- i371: bind `READER_VEC=prep_sam_reader` in this paged image, load an include
  from a real DOS disk, exercise under the SAM-fidelity harness + SimCoupé.
- i31b-b4: i2-pool PP_PREP buffer scaling + prep_run wired in front of
  b8d_chain_paged + the corpus gate.
