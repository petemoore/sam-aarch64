# i39b — name-table front-coding + editor sidecars (M8 Phase 2) — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: use superpowers:executing-plans (or
> subagent-driven-development) to execute this plan. Steps use checkbox (`- [ ]`) syntax.
> **Authority:** `docs/specs/2026-06-08-compact-tbn-nextgen-design.md` §3.5/§3.6/§3.7
> (the ideas), §4 "Format B" (the target), §5 "Phased path → Phase 2". Predecessor:
> i39a (the v2 overlay + header label table, MERGED #131) and i48a (symbolic IR is
> in-memory-only; the on-disk `.tbn` is overlay-only, MERGED #141/#142/#144).

**Goal (design Format B, Phase 2):** shrink the compact `.tbn` from ~45.2 KB toward
~38.6 KB *file* and — the headline — ~34.5 KB **assembler-resident** (−32% vs the i1
51 KB), by (1) **front-coding the name table** and (2) moving the data the SAM
assembler provably never touches (name *strings*, comment text, `.global` flags) out
of the assembler's paged-in set into an **editor/disassembler-only sidecar**.

## The invariant SHIFTS from i39a — read this first

i39a's guard was "**compact `.tbn` bytes byte-identical**." i39b **deliberately changes
the `.tbn` bytes** (the name table shrinks). So the guard becomes:

1. **The assembled binary stays byte-identical** — GNU == Go == Z80/SAM on the
   vendored release (21752 B). This is the m6-release 3-way gate; it does NOT compare
   `.tbn` bytes, only the assembled OUT. ✅ still the hard gate.
2. **Round-trip fidelity holds** — `disasm-roundtrip` (assemble → emit compact `.tbn`
   → render → re-assemble → byte-identical OUT) over the M3–M6 corpus + full release,
   and **every name string is recovered exactly** from the front-coded table.
3. **The compact `.tbn` gets SMALLER** (this is the win; assert a size *ceiling* that
   ratchets down, not a fixed size).
4. **`make check-budget`** stays under `&C000`; the assembler-resident footprint drops.

Do NOT assert `.tbn` byte-identity anywhere for i39b — find and relax any such check to
"assembled binary identical + round-trip holds" (grep the gates/harness for `cmp.*tbn`).

## What the SAM assembler needs vs doesn't (the load-bearing fact)

Since i39a PR(d) the Z80 reader seeds the symbol table from the **header label/local
tables** (`name_id → offset`), and the evaluator resolves `PUSH_SYM id` → value by
**id**, never by string. **The SAM assembler never decodes a name string.** Today
`reader.asm:reader_init_skip_names` merely *walks past* the `[len u16][bytes]` name
table to reach the header tables + records. That walk is the only assembler-side name
contact, and it's what i39b changes/eliminates.

---

## Recommended sub-phasing (split i39b into two independently-shippable PRs)

The design bundles both under "Phase 2," but they have different risk profiles. Ship
front-coding first (lower risk, file-size win), then the page-group split (the resident
win, bigger layout change). Suggest registering them as **i39b-1** and **i39b-2** in the
item registry (Pete's call on id granularity — see handover note).

### i39b-1 — name-table front-coding (the −2.8 KB file win)

Encoding: replace each name-table entry `[len u16][bytes]` with
`[shared_prefix_len uvarint][suffix_len uvarint][suffix bytes]`, where
`shared_prefix_len` is the count of leading bytes shared with the **previous** name
(encounter order — no sort, to keep ids stable and avoid re-indexing every `PUSH_SYM`).
Decode is a prefix-copy from the previous name. (Sort-then-frontcode saves a touch more
but would renumber ids; encounter-order front-coding is the safe choice given ids are
wired through every overlay patch + the header tables.)

- [ ] **Go writer** (`tools/sam-aarch64-format/writer.go` `WriteFile`): front-code the
      name list (track previous name; emit shared-prefix-len + suffix). Keep the
      `[count u16]` header. Consider a leading `[name_table_byte_len u32]` so a skipper
      can jump the whole table in O(1) (helps the Z80 skip and the future page split).
- [ ] **Go reader** (`reader.go` `ReadFile`): reconstruct full names by prefix-copy from
      the prior name; `File.Names` stays a `[]string` of full names (callers unchanged).
- [ ] **Round-trip + golden tests** (`tools/sam-aarch64-format/*_test.go`): a
      front-code/decode round-trip over a prefix-sharing name set (`spectrum4_*`, `__*`,
      `handle_*`); a golden-byte test of a small front-coded table.
- [ ] **Z80 skip** (`src/reader.asm` `reader_init_skip_names`): either parse the
      front-coded entries to skip, or (cleaner) read the new `[name_table_byte_len]`
      and advance the cursor by it in one step (page-renormalising). The assembler still
      decodes nothing — it only needs the correct end-of-name-table cursor.
- [ ] **Render/editor** (`tools/sam-aarch64/render/emit.go` via `ReadFile`): unaffected
      if `ReadFile` returns full names — confirm `render.Emit` still gets full spellings.
- [ ] **Gates:** relax any `.tbn` byte-identity assertion (see "invariant" above); keep
      the assembled-binary byte-match + `disasm-roundtrip`. Verify the harness
      (`TestCompactTbnAssembly`/`TestReleasePagedInLoad`) still byte-matches release.img
      (it feeds the new `.tbn` through the Z80 skip).
- [ ] **Verify locally:** GNU == Go(source) == Go(compact `.tbn`) (21752 B); compact
      `.tbn` shrinks from 45,189 B toward ~42–43 KB; all Go suites + harness + the
      non-SimCoupé `make ci-*` gates + `make staticcheck` green. Then CI (SimCoupé).

### i39b-2 — editor sidecars + page-group split (the resident win)

Move the data the assembler never maps into a separate region/page-group:

- name *strings* (the front-coded table) — assembler skips/never maps it.
- comment text (`COMMENT` records) → an editor-only sidecar.
- `.global` provenance → a ~1-bit/symbol flag in a sidecar (§3.5), not a `DIRECTIVE`
  record in the assembler stream.

The assembler's `reader_init` then **doesn't map the name/comment page at all**
(`reader_init_skip_names` is *deleted*, per §3.7) — the resident-budget win. The
editor/disassembler pages the sidecar in on demand to render `foo:` / `bl foo` /
comments. This is a `.tbn` layout change (a page-group/section index in the header) +
Z80 paging work + render/editor changes. Higher risk than i39b-1; scope it after i39b-1
lands and the front-coded table is proven.

- [ ] Design the header **section/page-group index** (where each group starts; which the
      assembler maps). Capture the layout in `tbn-binary-format-reference.md` (i48d's doc).
- [ ] Move COMMENT + `.global` into the sidecar; assembler stream drops them.
- [ ] Z80: delete `reader_init_skip_names`; assembler maps only the record/header pages.
- [ ] Render/editor: page in the sidecar for round-trip.
- [ ] Same verification set as i39b-1, plus a measured assembler-resident-budget drop.

---

## Risks / watch-items

- **id stability.** Front-coding MUST stay encounter-order (no sort) — ids are wired
  through every `PUSH_SYM`, the header label/local tables, and `headerRows`. A sort
  would renumber everything. (If a sorted variant is ever wanted, it needs an id
  remap pass + re-verification.)
- **The shifting invariant.** The most likely footgun is a leftover `.tbn`-byte-identity
  assertion in a gate/harness that now fails because the `.tbn` legitimately shrank.
  Grep first; convert to assembled-binary-identity + round-trip.
- **Z80 page boundaries.** The name-table skip already renormalises HL across page
  boundaries (`in_normalise_hl`); the front-coded skip / `name_table_byte_len` jump must
  preserve that (the §"page-boundary cursor" fix from i39a PR(d) is the reference).
- **Harness coverage.** Ensure the harness feeds the *new* front-coded `.tbn` through the
  Z80 skip (it should, since it builds the `.tbn` via `sam-aarch64`); add a name-heavy
  fixture if the release alone doesn't stress prefix sharing.

## Tracking obligations (handover contract rule 1)

- On i39b-1 merge: update `docs/notes/m8-status.md` (i39b row), `docs/notes/item-registry.md`,
  and the ROADMAP Current State. If split into i39b-1/i39b-2, register both ids.
- Record the compact `.tbn` size before/after (the ratcheting ceiling) so the win is auditable.
