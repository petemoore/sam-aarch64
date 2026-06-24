# `.tbn` header integrity check (CRC) — design spec

**Status**: design spec for registry item **i42** ("CRC / signature in the
`.tbn` file header (integrity check)"), a deferred M1 known gap. **Spec only —
nothing here is implemented.** It carries exactly one open decision for Pete
(the version-compatibility strategy, §7); the rest is decision-free.

**Authority**: the Go package `tools/sam-aarch64-format/` is the source of truth
for the format; this spec cites it inline. The normative format description is
`docs/specs/tbn-binary-format-reference.md` — once an integrity field ships, the
header section there is updated in the same PR.

---

## 1. Problem

The `.tbn` header (magic `SA64`, version, flags, editor-region offset) has **no
integrity field**. A truncated, bit-rotted, or partially-written file is only
caught indirectly — by a bad magic/version, an out-of-range editor offset, or a
record-stream truncation error — and silent mid-file corruption (a flipped bit
in an instruction word or expression byte) is **not caught at all**: it flows
through to assembled output. i42 asks for a header integrity check so a reader
can reject a corrupt file up front.

This is an **integrity** check (detect accidental corruption), not an
**authenticity** check (prove who produced the file). That distinction drives
the algorithm choice in §5.

---

## 2. Current header layout (byte-by-byte, as it stands today)

The fixed header is **12 bytes**, all multi-byte fields **little-endian**. From
`tools/sam-aarch64-format/writer.go:114-148` (write) and
`reader.go:192-208` (read):

| Offset | Size | Field | Value / meaning |
|-------:|-----:|-------|-----------------|
| 0 | 4 | `magic` | ASCII `S A 6 4` (`format.go:7`, `Magic = [4]byte{'S','A','6','4'}`) |
| 4 | 2 | `version` u16 LE | currently `2` (`format.go:13`, `Version uint16 = 2`); reader rejects any other (`reader.go:201-203`) |
| 6 | 2 | `flags` u16 LE | currently `FlagTaggedSidecar = 1<<0` (`format.go:18,27`) |
| 8 | 4 | `editor_region_offset` u32 LE | byte offset from file start where the editor region begins; the assembler-facing record stream ends here (`reader.go:205`, `writer.go:118-121`) |
| 12 | … | — | header position tables (label table · local table), then the record stream, then (at `editor_region_offset`) the editor region |

The Go header-length constant is `const headerLen = 4 + 2 + 2 + 4` (= 12),
defined identically in `reader.go:193` and `writer.go:120`. The flags word
currently carries one defined bit (`FlagTaggedSidecar`); bits `1<<1 .. 1<<15`
are unused and read as zero.

The Z80 side mirrors this exactly: `src/reader.asm:66-151` validates `SA64`
(lines 71-87), validates version `== 2` (lines 89-97), **skips** the flags u16
(lines 99-101, "Skip flags u16 (whatever it is)"), then decodes
`editor_region_offset` (lines 103-146) and jumps to the header tables at the
fixed offset `12` (lines 148-151).

---

## 3. Header consumers (everywhere the header is parsed/validated)

Every site below would be touched (or at minimum re-reviewed) by an integrity
field, because adding a field at the front shifts the offset of everything after
it (see §6 — the design avoids that shift on purpose).

**Go — write:**
- `tools/sam-aarch64-format/writer.go:114-148` — `WriteFile`, the sole header
  writer. Emits magic · version · flags · editor_region_offset, then tables ·
  records · editor region.

**Go — read / validate:**
- `tools/sam-aarch64-format/reader.go:192-265` — `ReadFile`, the sole header
  parser. Checks magic (197-199), version (200-203), reads flags (204), reads +
  range-checks editor_region_offset (205-208).
- `tools/sam-aarch64/render/emit.go:50` — `--render` path, calls `ReadFile`.
- `tools/comment-bench/main.go:62` — calls `ReadFile`.
- `tools/z80-test-harness-go/compact_tbn_test.go:79` — independently reads
  `editor_region_offset` from `in[8:12]` (does **not** go through `ReadFile`);
  any header-layout change must keep this in sync.

**Go — tests asserting header bytes:**
- `tools/sam-aarch64-format/file_writer_test.go:23-37` — asserts magic at `[0:4]`
  and editor_region_offset at `[8:12]`.
- `tools/sam-aarch64-format/file_reader_test.go` — `TestReadFileWrongMagic`
  (constructs a literal 12-byte header), `TestReadFileWrongVersion`,
  round-trip + sidecar-flag tests.
- `tools/sam-aarch64-format/format_test.go:5-12` — Magic/Version constants.

**Z80 — read / validate:**
- `src/reader.asm:66-151` — `reader_init`: magic + version validation, flags
  skip, editor_region_offset decode. **The flags word is skipped, not read** —
  so a flag-bit-driven design (§7 option b) needs this site to start reading it.
- `src/main_loop.asm:1556-1599` — a second copy of the editor_region_offset
  decode (mirrors `reader.asm`).

`tools/sam-aarch64-format/editor_region.go` operates entirely past
`editor_region_offset` and **does not touch the file-level header** — confirmed,
not a consumer for this change.

---

## 4. What the integrity check must cover

The check must detect accidental corruption anywhere it matters. Two candidate
coverage spans:

- **(A) Whole file minus the CRC field itself** — magic, version, flags,
  editor_region_offset, header tables, record stream, **and** the editor region.
- **(B) Assembler-facing region only** — header fields + tables + record stream,
  i.e. bytes `0 .. editor_region_offset`, excluding the editor region.

**Recommend (A), whole-file.** Rationale: the editor region (name strings,
`.global` provenance, comment sidecar) is real user source content that the
editor round-trips; corruption there silently damages the rendered source even
though the assembled binary is fine. A whole-file CRC is also simpler to reason
about (one span, "everything except the 4 CRC bytes") and is what a reader can
verify with a single linear pass. The Z80 cost is the same shape either way (a
table-free or 256-byte-table CRC-32 loop over a byte span); covering the editor
region adds only loop iterations, not complexity.

The CRC field's **own** bytes are excluded from the computed CRC (the standard
"CRC over everything but the CRC slot" convention), so the writer can compute in
one pass and patch the field, and the reader zeroes/skips that slot when
recomputing.

---

## 5. Algorithm: CRC-32

**Recommend CRC-32 (IEEE 802.3 / zlib polynomial `0xEDB88320` reflected).**

- i42 says "CRC **or** signature." The requirement is an **integrity** check
  against accidental corruption (truncation, bit-rot, half-written files), not
  an **authenticity** check against a malicious actor. CRC-32 is the
  proportionate, standard choice for integrity.
- A cryptographic signature (Ed25519, HMAC, …) solves a problem i42 does not
  pose: it proves *who* wrote the file, which requires **key management**
  (generation, distribution, storage, rotation, a trust root on the SAM side)
  — all out of scope, and none of it buys corruption detection that CRC-32
  doesn't already give. If authenticity is ever wanted it is a separate item
  with its own design; this spec deliberately does not pre-empt it (the flags
  word in §7 leaves room for a future "signed" bit).
- CRC-32 is cheap on Z80: a 256-entry lookup table (1 KB) gives a tight
  `xor`/`shift`/table-index inner loop, or a table-free bitwise variant trades
  the 1 KB for ~8× the loop work. The Go side uses `hash/crc32` (stdlib,
  `crc32.ChecksumIEEE`) — zero new dependency. The Go value is the **oracle**;
  the Z80 implementation is a faithful port verified against it (per the project's
  "Go is the encoding authority — port, don't reinvent" rule).

**Endianness/placement:** CRC-32 stored as **u32 little-endian**, consistent
with every other multi-byte header field (`editor_region_offset` and the u16s
are all LE).

---

## 6. Concrete field placement

Place the CRC as a **u32 LE field appended to the fixed header**, growing the
header from 12 to **16 bytes**:

| Offset | Size | Field | Notes |
|-------:|-----:|-------|-------|
| 0 | 4 | `magic` | unchanged |
| 4 | 2 | `version` u16 LE | see §7 for whether this bumps |
| 6 | 2 | `flags` u16 LE | see §7 for the gating-flag option |
| 8 | 4 | `editor_region_offset` u32 LE | unchanged |
| **12** | **4** | **`crc32` u32 LE** | **new** — CRC-32 over the whole file with these 4 bytes treated as zero |
| 16 | … | — | header tables · record stream · editor region |

Why **offset 12 (end of header)** rather than wedged between existing fields:
- It is the **only** addition that doesn't reshuffle the existing four fields,
  so the magic/version/flags/editor_offset offsets every consumer in §3 already
  hard-codes stay valid.
- The trade-off is that the header tables move from offset 12 → 16. That shift
  is mechanical and lives in **two** constants: `headerLen` in `reader.go:193` /
  `writer.go:120` (Go) and the `ld hl, 12` literal at `src/reader.asm:150` (Z80).
  Both become `16`. The `editor_region_offset` value is computed from `headerLen`
  on the write side (`writer.go:121`) and decoded (not assumed) on the read side,
  so it stays correct automatically.

CRC computation convention: writer assembles the full header (with the `crc32`
slot = `0x00000000`) + tables + records + editor region, computes
`crc32.ChecksumIEEE` over the entire buffer **with the 4 CRC bytes still zero**,
then writes the result LE into offset 12. Reader, to verify, reads the stored
CRC, recomputes over the file with offset 12-15 forced to zero, and compares.

---

## 7. OPEN DECISION FOR PETE — version-compatibility strategy

This is **the** decision this spec defers. Adding the field is mechanical; how
existing `.tbn` files and existing tools interoperate is a policy call.

> **Context that bounds the stakes:** the pre-v2 on-disk format was never
> released, and v2 `.tbn` files are **build artifacts regenerated from `.s`
> sources** (the `build/*.compact.tbn` fixtures are produced by `sam-aarch64`,
> not hand-authored or archived). There is no corpus of irreplaceable old
> `.tbn` files in the wild to preserve. That makes a hard version bump cheaper
> here than it would be for a format with archived user data.

**Option (a) — bump to version 3, CRC mandatory.**
New tools write v3 with a required CRC; the reader rejects v2 (or keeps a
back-compat path that accepts v2 without a CRC). Cleanest end state (every v3
file is integrity-checked, no conditional logic), but old files are unreadable
by new tools unless a back-compat branch is kept, and the Z80 `version == 2`
check (`reader.asm:91`) must accept `3` (and gate the CRC read on it).

**Option (b) — keep version 2, add a `FlagHasCRC = 1<<1` flag bit (RECOMMENDED).**
The CRC field at offset 12 is present **only when** `flags & FlagHasCRC` is set;
files without the bit keep the 12-byte header and parse exactly as today. This
is purely **additive**: old files stay valid, new files set the bit and carry
the field, and each reader's behaviour is "if the bit is set, the header is 16
bytes and bytes 12-15 are the CRC; verify it." Least disruptive. The cost is a
**variable-length header** (12 or 16 bytes) — every offset past the header
becomes `12 + (hasCRC ? 4 : 0)`, which the Z80 `reader.asm` must compute rather
than hard-code, and `reader.asm` must **start reading the flags word it
currently skips** (lines 99-101).

**Option (c) — keep version 2, write the CRC always, verification opt-in.**
The field is always present (header is unconditionally 16 bytes); writers always
fill it; readers may verify or ignore. Simpler than (b) on the read path (header
is a fixed 16 bytes, no branch), but it **silently breaks** any existing v2
reader that assumes a 12-byte header — including the current `src/reader.asm`
and `compact_tbn_test.go:79` — because the tables now start at 16, not 12. In
effect this is a wire-incompatible change wearing the same version number, which
is the worst of both worlds; **not recommended**.

**Recommendation: option (b).** It is the only choice that keeps every existing
file and every existing reader valid while letting new files opt into integrity
checking, and it reserves the flags word (which already exists and is already
skipped on the Z80 side) for exactly this kind of additive capability. Reserve
`FlagHasCRC = 1<<1`; leave `1<<2 .. 1<<15` free (a future `FlagSigned` could
live at `1<<2` without disturbing this design). **If Pete prefers the clean end
state over strict back-compat — reasonable given there are no archived `.tbn`
files to preserve — option (a) with a hard v3 bump and no v2 back-compat path is
the next-best and yields simpler readers (fixed 16-byte header).**

The remaining work items in §8 are written against option (b); switching to (a)
or (c) only changes the gating logic, not the field layout, algorithm, or
coverage span.

---

## 8. Implementation work items (for the eventual build, not now)

Scoped against the recommended option (b). Each is independently reviewable.

1. **Format constants** (`format.go`) — add `FlagHasCRC uint16 = 1 << 1`; decide
   whether the default `Flags` constant sets it (yes, so freshly written files
   are integrity-checked by default).
2. **Go writer** (`writer.go:114-148`) — when `FlagHasCRC` is set: reserve the
   4-byte slot at offset 12 (zeroed), recompute `editor_region_offset` against
   the 16-byte header, assemble the full buffer, `crc32.ChecksumIEEE` over it
   with the slot zero, patch the LE CRC into offset 12.
3. **Go reader / verify** (`reader.go:192-208`) — make `headerLen` conditional on
   `flags & FlagHasCRC` (16 vs 12); when set, read the stored CRC, recompute over
   the file with bytes 12-15 zeroed, return an error on mismatch
   (`"file: crc mismatch (have %08x, want %08x)"`). Keep the existing range
   checks.
4. **Out-of-band Go reader** (`compact_tbn_test.go:79`) — update the
   hard-coded `in[8:12]` editor-offset read to account for the conditional
   header length, or route it through `ReadFile`.
5. **Z80 reader** (`src/reader.asm:66-151`) — start reading the flags word
   (currently skipped at 99-101); if `FlagHasCRC` set, treat the header as 16
   bytes (the `ld hl, 12` at line 150 becomes conditional 12/16) and optionally
   verify the CRC. **Decision sub-point:** does the SAM verify the CRC at load,
   or only skip past it? Verifying costs a full-file CRC-32 pass on the Z80 at
   load time; given the SAM's speed, the recommendation is **skip-but-tolerate**
   on the SAM (read past the field, don't verify) and let the **host** be the
   integrity gate — but flag this for Pete alongside §7 since it is the same
   cost/benefit shape. Mirror any layout change into
   `src/main_loop.asm:1556-1599`.
6. **Go unit tests** (`file_writer_test.go`, `file_reader_test.go`,
   `format_test.go`) — assert the CRC slot is written, a round-trip verifies, a
   corrupted byte is rejected, and a legacy 12-byte (no-`FlagHasCRC`) file still
   parses. Update the literal headers in `TestReadFileWrongMagic` /
   `TestReadFileWrongVersion` if the default flags now set `FlagHasCRC`.
7. **Fixture regeneration** — see §9.
8. **Reference doc** (`docs/specs/tbn-binary-format-reference.md`) — update the
   header section to document the CRC field and the `FlagHasCRC` bit.

---

## 9. Round-trip test + fixture impact

The headline gate is **`disasm-roundtrip`** (`Makefile` target
`ci-disasm-roundtrip` → `tools/run-disasm-roundtrip.sh`), the
assemble→disassemble→reassemble byte-match: it asserts that re-rendering a
`.tbn` to source and re-assembling produces a **byte-identical** `.tbn`. The
per-suite `tests/*/run-roundtrip.sh` scripts and the
`compact_tbn_test.go` harness assembly are the supporting gates.

Impact:

- **The byte-match invariant is preserved by construction.** With option (b), if
  the writer deterministically sets `FlagHasCRC` and computes the CRC over a
  deterministic buffer, the same source produces the same `.tbn` bytes including
  the CRC — so assemble→render→reassemble still byte-matches. The CRC is a pure
  function of the rest of the file, so it never breaks round-trip determinism.
- **Fixtures regenerate, not hand-edit.** The `build/*.compact.tbn` fixtures and
  the per-suite generated `.tbn` outputs are produced by `sam-aarch64`; once the
  writer emits the CRC field they regenerate with the new 16-byte header and new
  trailing CRC. No fixture is hand-authored, so this is a `make`-driven
  regeneration, committed in the implementing PR. (If any test stores a
  **golden** expected `.tbn` byte-for-byte, that golden is regenerated in the
  same PR; the §3 test list flags `file_writer_test.go` / `file_reader_test.go`
  as the literal-header sites to update.)
- **CI implication:** the implementing PR must regenerate every committed `.tbn`
  artifact in lock-step with the writer change in a single commit, or the
  round-trip gate fails on a header-length / CRC mismatch between a stale fixture
  and the new reader. This is the one cross-cutting wiring risk and the pre-merge
  review should check for it explicitly.

---

## Open decision for Pete

**Version-compatibility strategy for the new CRC-32 field (§7):**

- **(a)** bump to **version 3**, CRC mandatory — cleanest end state, old files
  need a back-compat path or become unreadable;
- **(b) — RECOMMENDED** — keep **version 2**, gate the field on a new
  `FlagHasCRC = 1<<1` flag bit — purely additive, old files and old readers stay
  valid, header becomes variable-length (12 or 16 bytes);
- **(c)** keep version 2 but always write the field with opt-in verification —
  **not recommended** (wire-incompatible under the same version number; silently
  breaks existing 12-byte-header readers).

A secondary sub-decision rides along (§8 item 5): **does the SAM-side reader
verify the CRC at load, or skip-but-tolerate it and leave the host as the
integrity gate?** Recommendation: host verifies, SAM skips (avoids a full-file
CRC pass on the Z80 at load).

Everything else in this spec (CRC-32, LE u32 at offset 12, whole-file coverage
excluding the CRC slot, the work-item breakdown) is decision-free and follows
from the recommendation above.
