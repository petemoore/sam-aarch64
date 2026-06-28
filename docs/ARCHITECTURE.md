# Architecture

A synthesized overview of the whole system — the first document to read
after the root `README.md`. Each section summarizes one subsystem and links
the deep spec or reference that owns the detail; nothing here is normative
on its own. For current milestone state see `notes/m9-status.md`; for the
work backlog and open questions see `notes/item-registry-open.md` and
`notes/question-registry-open.md`.

## 1. What this is

A single Z80 program that runs on a SAM Coupé and assembles **aarch64**
(ARMv8-A 64-bit) machine code — making the SAM the development box for
Pete's bare-metal Raspberry Pi 400 kernel (`spectrum4`). The end-state
combines three subsystems on the SAM: a structured **editor** for aarch64
source, the **assembler** that turns that source into a flat
`kernel8.img`-style binary, and a **TFTP server** that netboots the result
straight onto a Pi over a Quazar Trinity ethernet interface. The
development loop closes without leaving the SAM.

The work is phased ([specs/vision.md](specs/vision.md)):

- **Phase 1 — standalone assembler** ([specs/phase1-assembler.md](specs/phase1-assembler.md)):
  source from disk → flat binary on disk. No editor, no network. This is
  the bulk of the repo today: the SAM-side Z80 assembler, its host-side Go
  toolchain, and the byte-match gates that prove correctness.
- **Phase 2 — visual editor**: a SAM-native structured editor producing the
  binary source format directly ([specs/editor-edit-model-design.md](specs/editor-edit-model-design.md)).
- **Phase 3 — TFTP server** over a direct SAM↔Pi ethernet link
  ([specs/phase3-tftp-design.md](specs/phase3-tftp-design.md)).

## 2. System shape

Two halves, one correctness contract: the **host Go toolchain** implements
everything first and serves as the authority; the **SAM-side Z80 program**
is a faithful port of the pieces that must run on the SAM. Both must
produce byte-identical output (§7).

### 2.1 SAM side — `src/`

`src/` holds the Z80 assembler that runs on the SAM (real hardware or
SimCoupé). [`src/README.md`](../src/README.md) is its index; the essentials:

- **One translation unit.** `assembler.asm` (`org &8000`, entry
  `jp start`) `include`s every other in-section file in a **load-bearing
  order** — code lands in section C in that sequence. The full ordered list
  (io → trampoline → loader → `slots/` per-operand encoders → expression
  evaluator → form lookup → encoder → record handlers → reader → two-pass
  driver → symbols → local labels → literal pool → print) lives in
  `src/README.md`.
- **Three build variants from the same source**, split by the
  `BUILD_TESTS` / `BUILD_TESTS_ENCODE` defines: the test variant (`make
  assembler` → `build/assembler.bin`, `-D BUILD_TESTS=1`) compiles in
  every boot-time self-test suite except the encode_inst family and
  exports `build/assembler.sym`; the encode self-test variant (`make
  assembler-enc-tests` → `build/assembler-enc-tests.bin`,
  `-D BUILD_TESTS_ENCODE=1`, i234) boots only the ENCTAB-coupled
  encode_inst family, time-multiplexing section-C test memory with the
  test variant across two boot runs; the production variant (`make
  assembler-prod` → `build/assembler-prod.bin`) omits all self-tests.
  All emit identical output bytes on every fixture; CI verifies the
  variants against each other (§7).
- **Off-axis payloads on physical pages 11–15.** Standalone binaries
  assembled separately and HLOAD'd at boot, reached by paging rather than
  `include`: the big self-test suites (`test_cluster.bin` → page 12,
  `test_mem.bin` → page 13, both `BUILD_TESTS` only), the encode_inst
  fixture data (`enc_fix_payload.bin` → page 11, `BUILD_TESTS_ENCODE`
  only), the production sysreg/dc/tlbi/pstate lookup tables
  (`sysreg_data.bin` → page 13, every build), the ZX0 compressor +
  decoder (`zx0.bin` → page 13 at `&8400`, every build), the `paged_call`
  self-test stub (page 14, `BUILD_TESTS` only), and the on-SAM
  disassembler (`disasm.bin` → page 15, every build). §5 covers the
  paging machinery.

### 2.2 Host side — `tools/`

[`tools/README.md`](../tools/README.md) is the full index. The production
core:

- **`tools/sam-aarch64/`** — the integrated host assembler, one binary
  built from three Go libraries: `frontend` (text → in-memory symbolic IR:
  lexer, parser, preprocessor, `-flatten`, strip passes), `assemble`
  (pass 1 / pass 2 / compaction to the `.tbn` overlay), and `render`
  (overlay → text). Modes: source → {binary, compact `.tbn`}
  (`--emit-tbn`), `.tbn` → binary, `--render` (`.tbn` → text), `-E`
  (preprocess only). The symbolic IR is **in-memory only — never
  serialized** ([specs/i48-syntactic-encoder-design.md](specs/i48-syntactic-encoder-design.md),
  Decision A).
- **`tools/sam-aarch64-format/`** — Go library (`package format`)
  implementing the `.tbn` container, record kinds, operand encoding,
  expression bytecode, and the directive/mnemonic tables (§6). Imported by
  `sam-aarch64` and `aarch64dec`.
- **`tools/aarch64enc/`** — the instruction **encoding authority** (§3, §4).
- **`tools/aarch64dec/`** — the instruction **decoding authority**: the Go
  disassembler the Z80 `src/disasm.asm` is ported from, oracle-gated
  against binutils `objdump` (§7).
- **`tools/tables-gen/`** — generates the Z80 data tables whose authority is
  Go source: the encoder form table `enctab.enc` (§4) and the sysreg/pstate/
  dc/tlbi tables `src/sysreg_tables.inc` (i7).
- **`tools/build-disk/`** — packs assembler + enctab + payloads + a
  `.tbn` into a bootable `.mgt` disk image (§7).

## 3. The authority model

The project's standing rule (see [`CLAUDE.md`](../CLAUDE.md), "If Go
already implements it, the Z80 side is a port, not a design"): **Go
implements first; the Z80 side mirrors it.**

- **Encoding truth lives in `tools/aarch64enc/`** — the form table plus the
  per-slot operand encoders and the overlay fold rules. When a Z80 encoding
  is wrong, the fix is to read the corresponding Go function and port it
  faithfully; the Go code already settles the design questions.
- **Decoding truth lives in `tools/aarch64dec/`**, which is itself held to
  GNU binutils: the `disasm` CI job asserts an exact match between
  `aarch64dec` and `objdump` on the vendored release. We match binutils'
  alias choices rather than inventing our own canonical forms.
- **Ultimate truth is GNU binutils**: every gate byte-compares our output
  against `as` (+ `ld -Ttext=0` where relocations need resolving) +
  `objcopy -O binary` (§7).
- Where the Z80 carries a copy of Go-side data (the sysreg/pstate/dc/tlbi
  tables in `src/sysreg_tables.inc`), it is **generated** from the Go
  authority by `tools/tables-gen` (`make tables`), not hand-mirrored, so the
  two cannot drift (i7). The CI job `sysreg-sync` runs both a fidelity guard
  (`make sysreg-sync-check` — the generated bytes match the Go maps) and a
  freshness guard (`make tables-sync-check` — the committed file matches a
  fresh regeneration).

For *execution* there is a parallel split (see `CLAUDE.md`, "Development
inner loop for Z80 changes"): **SimCoupé (run only inside the dev
container) is the sole CI gate** for SAM-side behaviour. The
`koron-go/z80` harness (`tools/z80-test-harness-go/`) exists to make
iteration fast, but it is a dev tool that is allowed to crash or mislead —
when it disagrees with SimCoupé, SimCoupé wins (§8).

## 4. Encoder tables

The aarch64 encoder is table-driven: a **form** is one
(mnemonic, operand-kind-tuple) pair with a 32-bit pattern, a fixed-bits
mask, and a list of operand slots (kind, bit position, width). The pipeline:

```
reference/arm-mra/  (vendored ARM Machine Readable Architecture XML)
        │
        ▼
tools/tables-gen          make enctab            make enctab-regen-source
        ├────────────────► build/enctab.enc      ├─► tools/aarch64enc/data.go
        │                  (binary form table,       (the same MRA projection,
        │                   loaded by the Z80)        as Go source)
        │
tools/aarch64enc/manual_forms.go   (hand-curated; never regenerated)
```

- **`data.go` is purely the MRA projection** and is rewritten verbatim by
  `make enctab-regen-source`. **`manual_forms.go` is hand-curated** and
  holds everything the MRA snapshot does not cover: mnemonics absent from
  the vendored XML, encoder choices where GNU `as` prefers a different
  alias than the MRA (e.g. `mov Xd, Xn` as ORR rather than ADD-imm-#0),
  register-form variants, and the bare 0-operand `ret`. Manual forms are
  consulted first in the linear scan, so they win ties.
- `make enctab` builds **`build/enctab.enc`** from both halves; the binary
  mirrors the Go runtime form table, so the Z80 and Go encoders share one
  table by construction.
- On the SAM, `enctab.enc` is HLOAD'd at boot into physical page 4 and read
  through section A under `LMPR_ENCTAB` (§5).
- The loader reads the file length from the SAMDOS DIFA header that HGTHD
  deposits at &4B50+34/35, so no build-time length constant is needed
  (i7 phase A eliminated `ENCTAB_LEN`).  Growing the form table is a
  no-op for the loader.

## 5. Memory + paging model

The right mental model for SAM memory is BASIC's, not the Z80's: the
machine is **one flat linear address space over all physical RAM** — 256
or 512 KB in 16 KB pages, addressed page:offset, the space SAM BASIC's
own POKE/PEEK address directly — and that flat space is the canonical
home of everything: code, buffers, tables, the document. The CPU's 64 KB
address space is not the memory; it is a **4-section window (A–D, 16 KB
each) that slides over the flat space**, LMPR selecting the page pair
under A/B and HMPR the pair under C/D. **Paging is the linker**: a
routine reaches code or data elsewhere in the flat space by mapping its
page into a section for the duration of the access — `paged_call`, the
reader bracket, and the emit bracket are this model's link-time fixups,
binding a window-visible address to a flat-space location at run time.

In hardware terms: **LMPR** is port `&FA`, **HMPR** is port `&FB`, and a
512 K machine has 32 physical 16 KB pages.
[`notes/sam-paging.md`](notes/sam-paging.md) is the hardware reference
(ports, REL PAGE FORM, ROM / DOS conventions).

The assembler's map — mirrored in
[`notes/memory-layout.md`](notes/memory-layout.md); the source of truth is
the header block of `src/assembler.asm`:

| Range | Section | Contents |
|-------|---------|----------|
| `&0000-&3FFF` | A | ROM0 by default; **or** ENCTAB (page 4) under `LMPR_ENCTAB`; **or** an IN page inside the reader bracket |
| `&4000-&7FFF` | B | Mostly unused; trampoline copy at `&7E00`. Under `LMPR_ENCTAB`, section B = page 5 = the OUT-low emit window |
| `&8000-&BFFF` | C | **Assembler code** — the buffers all live off-axis, so the whole 16 KB is code budget |
| `&C000-&FFFF` | D | Stack (`SP = &C100`) + scratch: OPVAL arrays, SYMTAB, litpool tables, `STAGING_BUF`, expression pool |

Everything bulky lives **off-axis** in physical pages that are paged into
a section only for the duration of an access:

- **Section-B paging helpers.** At startup the assembler copies two small
  helper bodies to `&7E00` in section B (`src/trampoline.asm`): the
  **HLOAD trampoline** and **`paged_call`**. Both exist because they
  remap sections C/D (HMPR) while running — so they must execute, with a
  safe stack, from a section that stays put.
- **ENCTAB** (page 4) — HLOAD'd at boot through the HLOAD trampoline
  (which swaps HMPR so the DOS load lands in page 4 via the section C
  window), then mapped into section A under `LMPR_ENCTAB` for encoder
  reads.
- **Paged IN** (a contiguous page-pool run, sized to the loaded `.tbn`
  prefix) — the source is HLOAD'd once at startup into a run allocated
  from the page pool; each record is staged into section D's
  `STAGING_BUF` through a per-record LMPR bracket into section A. Design
  rationale: [specs/paged-in-design.md](specs/paged-in-design.md).
- **Paged OUT** (a contiguous page-pool run, sized from the pass-1
  total) — every `emit_byte` writes through a per-byte LMPR bracket
  mapping the run's current page into section B; at end of pass 2, HSAVE
  reads the run through section C with the save's start-page (UIFA) set
  to the run's base page, auto-paging across `&C000`. Design rationale:
  [specs/paged-out-design.md](specs/paged-out-design.md).
- **`paged_call`** — the generic "call a routine in another physical
  page" helper (call shape: `call paged_call / defw addr / defb page`):
  it saves HMPR, maps the target page into sections C/D, switches to a
  safe section-B stack, `CALL`s the target, and restores HMPR
  bit-identically on return. The page-13 sysname matcher, the page-13
  ZX0 compressor/decoder, and the page-15 disassembler are invoked this
  way. Its static save slots are one deep — calls must not nest. (The
  big `BUILD_TESTS` off-axis suites use a direct LMPR-swap bracket
  instead.)
- **ZX0 payload** (page 13 at `&8400`, beside the sysname tables at
  `&8000`) — the greedy ZX0 compressor + turbo decoder for the editor's
  compressed-resident comment store, with its staging area on page 14
  (mapped at section D for free under `HMPR=13`). Placement, workspace,
  and watermark math:
  [specs/comment-storage-design.md](specs/comment-storage-design.md) §5.

The disk I/O underneath — the DOS hook layer (SAMDOS 2 / B-DOS): why HLOAD
needs the trampoline, why HSAVE does not, hook register-clobbering facts — is
[specs/samdos-file-io.md](specs/samdos-file-io.md).

**The `&C000` cliff**: both variants link at `org &8000` and must end
below `&C000`, where the stack page begins. Overrunning it produces a
silent boot-hang, so
[`tools/check-code-budget.sh`](../tools/check-code-budget.sh) turns
the cliff into a build failure with a number; it runs inline at the tail
of every assembler build and as `make check-budget` in CI.

## 6. The `.tbn` v2 source format

Source files on the SAM are not text: they are **`.tbn`** ("tokenised
binary") files — the hand-off format from the host assembler to the SAM,
and the format the Phase 2 editor will edit in place. The normative
encoding reference is
[specs/tbn-binary-format-reference.md](specs/tbn-binary-format-reference.md);
the design rationale is
[specs/compact-tbn-nextgen-design.md](specs/compact-tbn-nextgen-design.md).
The shape:

- There is exactly **one on-disk form: the compact instruction overlay**
  (`Version = 2`). The container is `"SA64"` magic, version, flags, then a
  `u32` **`editor_region_offset`** that splits the file into an
  **assembler-facing region** and a trailing **editor region** the SAM
  assembler never reads.
- The assembler-facing region opens with two **header position tables**
  (label table: `name_id` → offset-from-origin; local table: digit →
  offset; both delta-encoded), then a flat **record stream** of three
  kinds: `DIRECTIVE` (operands carried as self-describing encodings plus a
  stack-machine **expression bytecode** for anything symbolic),
  `LIT_DATA` (runs of constant data stored as raw assembled bytes), and
  `INSN_RUN` (runs of instructions — mode 0 is packed literal 32-bit
  words; mode 1 is base-word-plus-sparse-overlay: the relocated bitfield
  is zeroed and a patch carries a fold-slot id plus the expression that
  fills it at pass 2).
- The **editor region** holds the interned name table (front-coded),
  `.global` provenance flags, and the comment sidecar (comments anchored
  to output PC) — pure round-trip data for the renderer/editor.
- The Go package `tools/sam-aarch64-format/` is the code authority; the
  Z80 reader/decoder (`src/reader.asm`, `src/main_loop.asm`,
  `src/insn_run.asm`) mirrors the same constants.

**The invariant is binary-identity, not `.tbn`-identity**: the `.tbn` is a
working representation free to evolve (shrink, re-pack, relocate data),
and correctness is defined entirely by the **assembled output bytes** —
GNU, the Go path, and the Z80 path must agree byte-for-byte, and a `.tbn`
must round-trip through `--render` to source that reassembles to those
same bytes. The SAM loads only the assembler-facing prefix of a compact
`.tbn` (~38.6 KB for the full spectrum4 release, bounded by
`editor_region_offset`), so the 96 KB IN ceiling constrains the prefix,
not the file — the fully-commented release `.tbn` is ~363 KB on disk.

## 7. Build + test pipeline

[`Makefile`](../Makefile) drives everything;
[`.github/workflows/ci.yml`](../.github/workflows/ci.yml) maps the gates
to CI jobs.

**Build**: `make all` builds the three assembler variants (`assembler`,
`assembler-prod`, `assembler-enc-tests`), each tail-checked by the
code-budget script. `make disk` assembles the bootable test disk:
`tools/build-disk` packs `assembler.bin`, `enctab.enc`, and the off-axis
payloads (`-test-mem`, `-cluster`, `-paged-call`, `-sysreg-data`,
`-disasm`) into a `.mgt` image.

**The round-trip oracle** (the heart of the project): for each fixture
`.s`, host and SAM both assemble it, and the result is byte-compared
against GNU (`as` [+ `ld -Ttext=0` from the `symbols` corpus onward] +
`objcopy -O binary`). Each corpus dir (`tests/core`, `tests/symbols`,
`tests/operands`, `tests/paged`) carries a `run-roundtrip.sh` that sweeps
its `sources/*.s`, invoking `tools/run-roundtrip.sh <corpus> <fixture.s>`:
`sam-aarch64` (source → compact `.tbn`) → `build-disk` →
SimCoupé headless (`tools/run-simcoupe.sh`, `-exitonhalt`) → `samfile`
extracts the OUT file → byte-diff. The corpora are cumulative feature
tiers: `tests/core` (core emit), `tests/symbols` (symbols/two-pass),
`tests/operands` (compound operands + directives), `tests/paged` (paged
IN/OUT at scale); `tests/format` and `tests/spectrum4` are host-side
format/encoder corpora.

**The release gate**: `tools/run-release-gate.sh` is the headline
3-way byte-match — the vendored spectrum4 release (`tests/release/`,
21 752 bytes) must come out byte-identical from (1) GNU binutils (the
vendored `release.img`), (2) our Go toolchain, and (3) our Z80 toolchain
on SimCoupé. It is hermetic (both inputs vendored, refreshed via
`tools/revendor-release.sh`) and also runs `make check-budget`.

**Pure-Go gates** (no container): `ci-format` (format + host-assembler unit
tests, GNU-as cross-check), `ci-encoder` (encoder + `tables-gen` tests, plus
host-side round-trips over the `tests/format` and `tests/spectrum4`
corpora), `ci-disasm` (the `aarch64dec` vs `objdump`
oracle), `ci-disasm-roundtrip` (encode→decode→encode self-consistency,
including the overlay `--render` leg), `sysreg-sync-check` +
`tables-sync-check` (Go↔Z80 sysreg-table fidelity + generated-table
freshness), and `staticcheck` (dead-code gate, U1000, across the core Go
modules).

**CI job map** (`ci.yml`): `build-image` builds the dev container
(pyz80 + SimCoupé + Go) and pushes it to ghcr.io multi-arch; every
SimCoupé job pulls that image by sha tag and runs inside it. The jobs:

| Job | Runs |
|-----|------|
| `build-image` | dev image build + push |
| `format`, `encoder` | `make ci-format` / `ci-encoder` (host-only) |
| `disasm`, `disasm-roundtrip` | `make ci-disasm` / `ci-disasm-roundtrip` (host-only) |
| `sysreg-sync`, `staticcheck` | sync guard / dead-code gate (host-only) |
| `core`, `symbols`, `operands`, `paged` | fixture-corpus round-trips on SimCoupé (test variant) |
| `enc-tests` | one SimCoupé boot of the encode self-test variant to OK (i234) |
| `symbols-prod`, `operands-prod`, `paged-prod` | same corpora with the production variant (variant-divergence guard) |
| `release-gate` | the 3-way release byte-match + code budget |

Branch protection on `main` requires the status checks configured in the
repo settings (see `CLAUDE.md` §2); merges are merge commits only.

## 8. Dev inner loop

The standing rule (see `CLAUDE.md`, "Development inner loop for Z80
changes"): **don't use CI as the inner loop**. The pipeline is a test
pyramid — each layer is slower and more authoritative than the one above:

1. **Host checks (fastest)** — `go test ./...` across the Go modules, plus
   `pyz80` to confirm the Z80 still assembles. Pure host, no container.
2. **The Go Z80 harness** (`tools/z80-test-harness-go/`, see its
   [README](../tools/z80-test-harness-go/README.md)) — runs the real
   assembler binary under `koron-go/z80` at ~1 ms per fixture, emulating
   just enough of the DOS hooks (SAMDOS 2 / B-DOS: HGTHD/HLOAD/HSAVE, paged
   RAM) to run the full
   pipeline, including the **paged boot path**: `TestBootSelfTestsPass`
   boots the `BUILD_TESTS` assembler with all page-12–15 payloads and
   asserts every boot self-test passes in ~30 ms
   (`TestBootSelfTestsFailProbe` is the negative control), and
   `TestBootSelfTestsEncodePass` / `TestBootSelfTestsEncodeFailProbe` do
   the same for the encode self-test variant (i234). It also gives
   PC traces and register snapshots on failure. It is **not a gate**: it
   may crash or mislead without blocking anything, agents evolve it as
   normal work, and SimCoupé wins every disagreement. Gotcha: always pass
   `-sysreg-data` for the production assembler.
3. **SimCoupé in Docker** — the real-emulation confirmation before
   pushing. SimCoupé runs only inside the dev container
   (`tools/Dockerfile.dev`; see
   [`notes/headless-simcoupe.md`](notes/headless-simcoupe.md)), never on
   the host.
4. **CI** — the final pre-merge gate (§7), not a per-iteration tool: a CI
   round-trip costs minutes, the harness costs milliseconds.

The boot self-tests themselves are the bottom of a second pyramid, inside
the SAM binary: per-routine encoder/table assertions (`src/test_*.asm`)
run at boot before any fixture round-trip, so a regression names its
routine instead of surfacing as a byte-diff. The fixture corpora then
exercise combinations, and the release gate exercises everything at once.

## Going deeper

- SAM-side code: [`../src/README.md`](../src/README.md) ·
  [`notes/memory-layout.md`](notes/memory-layout.md) ·
  [`notes/sam-paging.md`](notes/sam-paging.md)
- Host toolchain: [`../tools/README.md`](../tools/README.md)
- Format: [specs/tbn-binary-format-reference.md](specs/tbn-binary-format-reference.md)
- SAM software catalogue (every key binary/disasm/investigation + how they relate):
  [specs/sam-software-catalogue.md](specs/sam-software-catalogue.md)
- Disk/file plumbing — the DOS hook layer (SAMDOS 2 / B-DOS):
  [specs/samdos-file-io.md](specs/samdos-file-io.md) ·
  [`notes/sam-disk-format.md`](notes/sam-disk-format.md) ·
  [`notes/sam-file-header.md`](notes/sam-file-header.md)
- Roadmap + tracking: [`ROADMAP.md`](ROADMAP.md) ·
  [`notes/m9-status.md`](notes/m9-status.md) ·
  [`notes/item-registry-open.md`](notes/item-registry-open.md) ·
  [`notes/question-registry-open.md`](notes/question-registry-open.md)
- Working agreements (authority model, inner loop, PR workflow):
  [`../CLAUDE.md`](../CLAUDE.md)
