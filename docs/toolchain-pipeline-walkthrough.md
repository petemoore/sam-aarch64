# Toolchain pipeline — a byte-level worked walkthrough

This traces **one tiny aarch64 source file** through every representation and
every tool in the pipeline, with the **real bytes** at each step, the **exact
commands** that produce them, and the **real Z80 routines** that run on the SAM.
It exists to disambiguate overloaded words — "the source", "translate", "the
round-trip" — by showing concrete artifacts instead.

It is a companion to the normative format spec
([`docs/specs/tbn-binary-format-reference.md`](specs/tbn-binary-format-reference.md));
that doc defines the `.tbn` container exhaustively, this one walks a single
example through it.

> Every byte, command, and line number below was captured from a real run on
> this repo. If the tools change and this drifts, the tools win — re-capture it.

---

## 0. The shape of the pipeline

There are **four representations** of a program and a handful of tools that move
between them. Host tools are Go (`build/sam-aarch64`, `build/aarch64dec`); the
SAM side is the Z80 assembler in `src/` running on SimCoupé / real hardware.

```
                          ┌──────────────────── HOST (Go) ───────────────────┐
   demo.s  ──(1)──►  in-memory IR  ──(2)──►  demo.tbn        demo.bin
   (text)         (record structs,        (compact overlay,   (aarch64
                   never on disk)           on disk)            machine code)
                       │  ▲                     │   ▲                │  ▲
                  -flatten │                --emit-tbn │           -o │  │
                  (linker  │ -render            │      │              │  │ aarch64dec -asm
                   step)   │ (.tbn→text)        │      │              │  │ (binary→text)
                          (1)                  (2)    (render)       (3)│
                                                                       ▼
                                                          ┌──── SAM (Z80) ────┐
                                              demo.tbn ──► assembler ──► OUT bytes
                                                          (src/main_loop.asm…)
```

- **(1) text → IR**: `build/sam-aarch64` lexes/parses `demo.s` into a slice of
  `format.Record` structs **held in memory only** — never serialized
  (`tools/sam-aarch64/frontend/translate.go`; i48 decision A).
- **(2) IR → `.tbn`**: the compaction pass folds the IR into the **compact
  overlay** on disk (`--emit-tbn`). This is the one on-disk `.tbn` form and the
  only thing the SAM loads.
- **(3) IR → binary**: pass-1/pass-2 emit aarch64 machine code (`-o`).
- **`-flatten`**: an optional **linker step** (see §6) — *not* required for any
  of the above.
- **`-render`** / `aarch64dec -asm`: the two ways back to text (§5) — and they
  are **not** the same, which is the crux of the round-trip story (§7).

---

## 1. The example source (`demo.s`)

A deliberately tiny file that still contains: a comment, instructions, a label,
the implicit code section, a **named** section, and a data directive.

```asm
// demo.s — minimal walkthrough example
.text
start:
	mov	x0, #1		// set x0 = 1
	ret
.section bss_kernel
buf:
	.space	8
```

This is **122 bytes of ASCII**. When I say "the source" I mean exactly these
bytes. Two comments (`// demo.s …` standalone, `// set x0 = 1` trailing), two
instructions (`mov`, `ret`), two labels (`start`, `buf`), and two directives
that emit no code of their own (`.text`, `.section bss_kernel`) plus one that
reserves space (`.space 8`).

---

## 2. Representation: the assembled binary (`-o`)

```sh
build/sam-aarch64 -o demo.bin demo.s
```

Produces **16 bytes**:

```
offset  bytes              meaning
0       20 00 80 d2        mov x0, #1     (0xd2800020, little-endian)
4       c0 03 5f d6        ret            (0xd65f03c0, little-endian)
8       00 00 00 00 00 00 00 00   .space 8  (8 zero bytes)
```

Things to notice, because they drive everything later:

- `.text` and `.section bss_kernel` produced **zero bytes**. They are not in the
  binary *at all*. A directive that emits nothing leaves no trace in machine code.
- `.space 8` **did** emit 8 bytes (this is a non-flattened build — see §6 for
  what flatten does to it).
- The two comments and the two label *names* are nowhere in the binary either.

The binary is just code+data bytes. Everything structural — section membership,
names, comments — is gone. Keep that in mind for §7.

---

## 3. Representation: the `.tbn` (compact overlay)

```sh
build/sam-aarch64 --emit-tbn demo.tbn -o demo.bin demo.s
```

Produces **145 bytes**. Unlike the binary, the `.tbn` keeps the *structure*. Here
is the complete byte-by-byte decode (framing per the format spec §2/§3;
`[kind u8][length u16 LE][payload]` per record, `tools/sam-aarch64-format/reader.go:81`):

```
=== container header (12 bytes) ===
00: 53 41 36 34            magic "SA64"
04: 02 00                  version = 2
06: 01 00                  flags = 1  (bit0 FlagTaggedSidecar)
08: 43 00 00 00            editor_region_offset = 0x43 = 67   ← record stream ends here

=== label table (assembler-facing) ===
0c: 02 00                  count = 2
0e: 00 00  00              name_id=0 ("start"), offset_delta=0   →  start @ 0
11: 01 00  08              name_id=1 ("buf"),   offset_delta=8   →  buf   @ 8

=== local table ===
14: 00 00                  count = 0

=== record stream (bytes 22..66) ===
16: 04  02 00  | 00 00
                          DIRECTIVE len2: dir_id=0x00 (.text), operand_count=0
1b: 09  09 00  | 00  20 00 80 d2  c0 03 5f d6
                          INSN_RUN len9: mode0, then two 4-byte words —
                          20 00 80 d2 = mov x0,#1 ;  c0 03 5f d6 = ret
27: 04  0f 00  | 12 01  0b  0a 00  62 73 73 5f 6b 65 72 6e 65 6c
                          DIRECTIVE len15: dir_id=0x12 (.section, =18),
                          operand_count=1, operand = sysname len 10 "bss_kernel"
39: 04  07 00  | 0e 01  05 02 00 01 08
                          DIRECTIVE len7: dir_id=0x0e (.space, =14),
                          operand_count=1, operand = immediate expression for 8

=== editor region (byte 67+; the SAM never reads past offset 67) ===
43: 02 00                  name table count = 2
45: 00 05 "start"          name0: shared_prefix 0, suffix 5, "start"
4c: 00 03 "buf"            name1: shared_prefix 0, suffix 3, "buf"
51: 00 00                  global-flags count = 0
53: 02 00                  sidecar count = 2  (two comments)
55: 00 00  00 27 00 " demo.s — minimal walkthrough example"
                           row0: kind0 comment, anchor_delta 0, placement0
                                 (standalone), len 0x27=39, text (em-dash = e2 80 94)
81: 00 04  01 0b 00 " set x0 = 1"
                           row1: kind0 comment, anchor_delta 4 (PC after mov),
                                 placement1 (trailing), len 0x0b=11, text
                           (last byte at offset 144 → 145 bytes total)
```

The raw hex, for reference (`od -A d -t x1 demo.tbn`):

```
0   53 41 36 34 02 00 01 00 43 00 00 00 02 00 00 00
16  00 01 00 08 00 00 04 02 00 00 00 09 09 00 00 20
32  00 80 d2 c0 03 5f d6 04 0f 00 12 01 0b 0a 00 62
48  73 73 5f 6b 65 72 6e 65 6c 04 07 00 0e 01 05 02
64  00 01 08 02 00 00 05 73 74 61 72 74 00 03 62 75
80  66 00 00 02 00 00 00 00 27 00 20 64 65 6d 6f 2e
96  73 20 e2 80 94 20 6d 69 6e 69 6d 61 6c 20 77 61
112 6c 6b 74 68 72 6f 75 67 68 20 65 78 61 6d 70 6c
128 65 00 04 01 0b 00 20 73 65 74 20 78 30 20 3d 20 31
```

Key facts you can see directly in the bytes:

- The **machine-code words are embedded verbatim** in the `INSN_RUN` record
  (`20 00 80 d2 c0 03 5f d6` at offset 0x1f) — the SAM doesn't re-encode
  instructions, it copies these words out.
- `.section bss_kernel` **is a record** here (offset 0x27): `dir_id 0x12`, with
  the section name `"bss_kernel"` stored inline. It is **preserved**, unlike in
  the binary. This is the whole reason a `.tbn` can round-trip to source and a
  binary cannot.
- The label *names* ("start", "buf") and the comment *text* live in the **editor
  region** past `editor_region_offset` (67). The SAM assembler's record walk
  stops at offset 67 (`src/reader.asm` `reader_init`) and **never reads** the
  names or comments — it resolves labels by `name_id` → offset from the label
  table alone.

### 3.1 Comment stripping is editor-region-only

```sh
build/sam-aarch64 -strip-comments --emit-tbn demo.stripped.tbn -o demo.bin demo.s
```

Produces **85 bytes** — exactly 60 fewer. The first **67 bytes are byte-for-byte
identical** to the unstripped `.tbn`; only the editor region changes:

```
51: 00 00     global-flags count = 0
53: 00 00     sidecar count = 0      ← was 2; the two comment rows (60 bytes) are gone
              (file ends at offset 84 → 85 bytes)
```

So `-strip-comments` touches **only** the editor region. The **assembler-facing
region the SAM reads is identical** with or without comments — the SAM produces
the same bytes either way. (`-thin-comments=N` keeps one comment in N; same
mechanism.) This is why the release gate can flow the full comment corpus
without affecting the 96 KB on-SAM assembler budget.

---

## 4. SAM side: what the Z80 does with the `.tbn`

The SAM loads `demo.tbn` and runs the two-pass assembler in `src/`. For our
example the interesting question is what it does with `.text`, `.section`, and
`.space`. All citations are `src/main_loop.asm`:

- **Pass 1 (sizing).** `compute_directive_size` routes `.text`/`.data`/`.section`
  to `compute_dir_size_zero` — **size 0**, the PC is not advanced
  (`main_loop.asm:572-583`). `.space`/`.skip` are sized to their byte count.
- **Pass 2 (emit).** `main_handle_directive_pass2` routes `.text`
  (`main_loop.asm:513`), `.data` (`:515`) and `.section` (`:523-524`) to
  `walk_records` — a **pure no-op**, zero bytes emitted. `.space`/`.skip` go to
  `main_dir_skip_emit` (`:529-532`) which **does** emit the reserved bytes.

So on the SAM, exactly as on the host binary build, `.section bss_kernel` is a
zero-byte no-op and `.space 8` emits 8 bytes. The directive IDs are the shared
registry in `tools/sam-aarch64-format/directives.go` (`.text`=0, `.data`=1,
`.space`=14/0x0e, `.section`=18/0x12) mirrored by the `DIR_*` equs in the Z80.

---

## 5. Two ways back to text — and they differ

### 5.1 `.tbn → text` (the renderer / editor path)

```sh
build/sam-aarch64 -render demo.tbn
```

```asm
// demo.s — minimal walkthrough example
start:
	.text
	mov	x0, #0x1 // set x0 = 1
	ret
buf:
	.section bss_kernel
	.space 8
```

The renderer walks the `.tbn` **records** and the editor region, so it
**reproduces the directives and comments** — `.section bss_kernel` and `.space 8`
are back, because they were records in the `.tbn`. (It is a pretty-printer, not
a byte-exact source reproducer: note `#1`→`#0x1` and the label/`.text` order — it
reproduces *structure and meaning*, not the original whitespace.) Code:
`tools/sam-aarch64/render/emit.go:250-256` emits any `KindDirective` by name.

### 5.2 `binary → text` (the disassembler path)

```sh
build/aarch64dec -asm demo.bin
```

```asm
	.text
	mov	x0, #0x1
	ret
	udf	#0
	udf	#0
```

The disassembler decodes the **16 binary bytes**. The two `udf #0` are the 8
zero bytes from `.space` decoded as instructions (`0x00000000` = `udf #0`). And
crucially: **`.section bss_kernel` is gone** — it was never in the binary (§2).
The disassembler cannot invent a directive that emitted no bytes.

**This is the key contrast.** The same program, two paths back to text: the
`.tbn` path recovers `.section`; the binary path cannot. Not because one tool is
better — because the *input* to the binary path never contained it.

---

## 6. `-flatten`: the linker step (and what it consumes)

`-flatten` is **not** concatenation (that's the preprocessor, `-E`/`.include`)
and is **not** needed to make a `.tbn` (§3 made one without it). It is the
**linker**: it does the job GNU `ld` + a linker script does, *inside the IR*, so
the single-section SAM assembler can produce the same bytes a multi-section
linked build would.

```sh
build/sam-aarch64 -flatten -origin 0x1000 -o demo.flat.bin --emit-tbn demo.flat.tbn demo.s
```

The flattened binary is **8 bytes** (not 16):

```
0   20 00 80 d2 c0 03 5f d6        just mov + ret
```

And the flattened `.tbn` renders as:

```asm
// demo.s — minimal walkthrough example
	.org 0x1000
start:
	mov	x0, #0x1 // set x0 = 1
	ret
	.ltorg
	.equ buf, 0x2000
```

Compare to §3/§5.1. Flatten (`tools/sam-aarch64/frontend/flatten.go`):

- **bucketed records by section** using the `.text`/`.data`/`.section X`
  directives — so it *needs* those directives as input (they tell it which
  section each label is in);
- emitted `.org 0x1000` (the origin) and a trailing `.ltorg` (literal-pool flush);
- placed the `bss_kernel` section at its computed VMA and turned its label into
  `.equ buf, 0x2000` — an **absolute address** (0x2000 because the empty
  `bss_roms` section's 0x1000 alignment bumps it past 0x1000; see
  `SpectrumFourLayout`, overridable with `-layout`, item i56);
- **dropped** the `.space 8` bytes — a BSS / NOBITS section contributes no bytes
  to the image (mirroring `objcopy -O binary`);
- **consumed** (deleted) the `.section`/`.text` directives themselves.

So flatten is a **one-way lowering**: section structure and NOBITS bytes are
gone, replaced by absolute addresses. You can no more un-flatten back to the
multi-section source than you can recover `.o` files from a linked binary. That
is *by design* — it is linking.

This also answers "why not just delete `.section` at parse time, since it's a
no-op?" Because **flatten needs it**: it is the metadata that says which section
`buf` belongs to, so flatten can give `buf` the BSS VMA. Before flatten the
directive carries information; after flatten it has done its job and is dropped.
If you never flatten, it rides along as a harmless zero-byte no-op (§2, §4).

---

## 7. The round-trips — which is which, and what's lost where

### 7.1 The CI round-trip is **binary-level** (`tools/run-disasm-roundtrip.sh`)

The `disasm-roundtrip` CI gate proves `encode → decode → encode` is identity. For
each fixture it runs (real commands, `run-disasm-roundtrip.sh:83` encode, `:97`
decode, `:129` re-encode):

```sh
build/sam-aarch64 -o v1.bin        demo.s        # encode: source → bytes
build/aarch64dec  -asm v1.bin   >  disasm.s      # decode: bytes  → text
build/sam-aarch64 -o v2.bin        disasm.s      # encode: text   → bytes
cmp v1.bin v2.bin                                # assert v1 == v2
```

Run on our flattened binary it passes — `demo.flat.bin` (8 bytes) → disasm
(`mov/ret`) → reassemble → **identical 8 bytes** (verified). For the spectrum4
release the same gate runs with `-flatten -strip-comments` on the encode legs
(`run-disasm-roundtrip.sh:168,220,337`).

The thing to internalise: **this round-trip compares *bytes*, and it decodes
from *bytes*.** A `.section` directive is zero bytes (§2), so:

- it is **not in `v1.bin`**, so `aarch64dec` never emits it, so it is **not in
  `disasm.s`**, so it is **not in `v2.bin`** — and `v1.bin == v2.bin` anyway,
  because zero bytes either side is still zero bytes.

So the binary round-trip neither tests nor depends on `.section`. It is agnostic
to every zero-byte directive. (Run on the *non-flattened* `demo.bin`, the decode
is `mov/ret/udf/udf` and re-encoding those gives back the same 16 bytes — the
`.space` data survives as `udf` words because it *was* bytes; the `.section` text
does not survive, but it cost zero bytes so the byte-compare still holds.)

### 7.2 The SAM round-trip is **`.tbn` → bytes**, host-vs-SAM (`tools/run-roundtrip.sh`)

```sh
build/sam-aarch64 -o build/X.img --emit-tbn build/X.compact.tbn fixture.s   # host (run-roundtrip.sh:105)
#   → SimCoupé loads X.compact.tbn, the Z80 assembler emits OUT bytes
#   → assert SAM OUT == host X.img
```

No `-flatten`. The fixtures are non-flattened and **do** contain directives like
`.data` (`tests/core/sources/dir_data.s`) — so this gate exercises the SAM
assembler consuming `.text`/`.data`/`.section` records (as the zero-byte no-ops
of §4) and proves it emits the **same bytes as the host** from the same `.tbn`.

### 7.3 "We'd need *these* bytes back, but they were lost at step X"

Put concretely, using `.section bss_kernel`:

- To reproduce the *directive text* `.section bss_kernel`, a round-trip needs a
  representation that still contains it.
- The **binary** lost it at **§2** (it emitted zero bytes — it was never encoded).
  So no binary-based path (`aarch64dec`, §5.2) can recover it. The CI round-trip
  (§7.1) doesn't care, because it compares bytes and the directive is byte-free.
- The **`.tbn`** still has it (a record at offset 0x27, §3). So the `.tbn`-based
  path (`-render`, §5.1; the on-SAM editor) **does** recover it.

This is why there is **no host-vs-SAM parity gap**: both sides treat `.section`
identically (a zero-byte no-op — host §2, SAM §4); neither can recover directive
text from bytes alone; both recover it from the `.tbn`. The only thing untested
anywhere is a *named* `.section foo` surviving a **text→`.tbn`→text** render
round-trip — and that's untested symmetrically (the one file with named sections,
the release, is always flattened first, which is *meant* to consume them).

---

## 8. Cheat-sheet

| You have | You want | Command | Keeps directives? | Keeps comments? |
|---|---|---|---|---|
| `.s` text | binary | `sam-aarch64 -o x.bin x.s` | no (zero-byte ones vanish) | no |
| `.s` text | `.tbn` | `sam-aarch64 --emit-tbn x.tbn -o x.bin x.s` | **yes** (as records) | yes (editor region) |
| `.s` text | `.tbn`, no comments | add `-strip-comments` | yes | no (editor region only) |
| `.s` text | linked binary | `sam-aarch64 -flatten -origin … -o x.bin x.s` | consumed (→ `.org`/`.equ`) | n/a |
| `.tbn` | text | `sam-aarch64 -render x.tbn` | **yes** | yes |
| binary | text | `aarch64dec -asm x.bin` | **no** (not in the bytes) | no |
| `.tbn` | bytes (SAM) | SAM assembler (`src/main_loop.asm`) | consumes as no-ops | n/a (never read) |

**One-line summaries.**
- *Concatenation* = preprocessor (`-E`). *Linking/layout* = `-flatten`.
  *Serialization* = `--emit-tbn`. They are independent.
- The `.tbn` keeps structure (directives, names, comments); the **binary keeps
  only bytes**.
- `-flatten` is a one-way linker lowering; it *needs* `.section`/`.text`/`.data`
  as input and consumes them.
- The CI `disasm-roundtrip` is **byte-level** and blind to zero-byte directives;
  the SAM round-trip is `.tbn`→bytes host-vs-SAM. Neither implies a parity gap.

---

*Full format spec: [`docs/specs/tbn-binary-format-reference.md`](specs/tbn-binary-format-reference.md).
Host tool: [`tools/sam-aarch64/`](../tools/sam-aarch64/). SAM assembler: `src/main_loop.asm`,
`src/reader.asm`, `src/insn_run.asm`. Recreate every artifact above from `demo.s`
with the commands shown.*
