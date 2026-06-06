# Item index — the project-wide `iN` registry

This is the **canonical, milestone-neutral registry** of every tracked `iN`
item. The id space is **project-wide, not per-milestone** — an id minted under
one milestone keeps its meaning forever. This doc is **never archived**: when a
milestone's status doc (`docs/notes/m{N}-status.md`) is superseded and moved to
`docs/notes/archive/`, the items it owned live on here, so nothing is lost.

Milestone status docs and the ROADMAP **reference this file** as the registry
home; they do not duplicate the table. At milestone close, every item belonging
to that milestone must be marked ✅ done / ❌ wontfix here, or re-pointed to the
active milestone's strands — no item may be left living only in an archived
`m{N}-status` doc (see the ROADMAP handover contract).

**Convention (Pete, 2026-06-08):** every tracked item has a stable **`iN`** id
(`i` = item). This table is the **registry** — the authoritative id↔item map.
Rules: (1) once an id appears in a PR title, branch, or commit it is **locked** —
never renumber it; (2) sub-items take letter suffixes (`i12a`/`i12b`/`i12c`);
(3) a new item gets the next free integer; (4) reference items by id in
conversation, PRs, and these docs. The per-milestone scope/deferred-backlog
tables carry the same ids. (The ids below were assigned in the 2026-06-08
planning session; `i13` is locked to "gitignore" to match shipped PR #107, so the
"replace `cls`" item — tentatively i13 in conversation — is registered as `i16`.)

| id | item | status | pointer |
|----|------|--------|---------|
| **i1** | Compact-`.tbn` format change (hybrid bytes/symbolic; `KindLitInsts` + `KindLitData`) | ✅ PR1 #121 + PR2 #122 + PR3 #124 all merged — **88,644 → 51,117 B (−42.3%)**, 6 IN pages → 4, byte-identical to GNU. Next-gen redesign explored as **i39**. | `docs/specs/2026-05-27-compact-tbn-and-disassembler-design.md`; `docs/specs/2026-06-08-tbn-binary-format-reference.md` (§7) |
| **i2** | On-SAM IDE memory model (edit buffer + IN/OUT paging; "claim all free RAM, grow on demand") | 🧭 reframed; deferred to editor work | scope row "IN/OUT paged-buffer ceiling" |
| **i3** | Editor groundwork (Phase 2) — full vision | 🧭 | ROADMAP "Editor vision" |
| **i4** | Basic read-only listing/scroll viewer (centre-locked cursor; up/down only) | 🧭 new 2026-06-08 | — (Pete's idea; precursor to i3) |
| **i5** | UI visual prototyping via image generation (MODE 3 64×24 vs MODE 4 32×24 mockups) | 🧭 new 2026-06-08 | — (Pete's idea) |
| **i6** | SAM screen-mode decision (MODE 3 vs 4, or user preference) | 🧭 | ROADMAP "Editor vision"; scope row |
| **i7** | Codegen sysreg/mnemonic/form tables from Go authority | 📋 | scope row |
| **i8** | Sysreg-table de-dup into shared `src/sysreg_tables.inc` | ✅ DONE (PR #108) | `src/sysreg_tables.inc` |
| **i9** | Parity robustness seeds (sysname full-Go-parity + untested-form empirical sweep) | ✅ DONE (PR #114) — but it *pinned* two found gaps as skipped tests; fixed under **i36/i37** | `docs/notes/2026-06-08-z80-go-disasm-parity-i9.md` |
| **i10** | Go-vs-Z80 capability parity report | ✅ DONE (PR #109) | `docs/notes/2026-06-08-go-vs-z80-disasm-capability-parity.md` |
| **i11** | Full ARMv8.0-A A64 ISA footprint research | ✅ DONE (PR #110) | `docs/notes/2026-06-08-armv8-a64-isa-footprint-research.md` |
| **i12a** | SimCoupé v1.2.16 bump (pin SHA to upstream, drop vendored `-exitonhalt` patch) | ✅ DONE (PR #112) | `tools/Dockerfile.dev` |
| **i12b** | Editor-testing input injection (`-keyin` vs `FLAGS`/`LASTK` memory injection) | 🧭 new 2026-06-08 | `tools/run-simcoupe.sh`; for automated editor tests |
| **i12c** | Rebase/upstream macOS+Linux paste support | ✅ RESOLVED/MOOT — upstreamed (`87f2a69`); arrives free with i12a | `~/.claude/.../memory/project_simcoupe_sdl_paste_branch.md` |
| **i13** | gitignore in-tree Go build binaries | ✅ DONE (PR #107) | `.gitignore` |
| **i14** | Non-canonical logical-immediate decoder tests (`decodeBitMasks` `immr≥esize` reject) | ✅ DONE (PR #113) | deferred-backlog row |
| **i15** | `adds` 3-register form (Z80) | 🧭 | deferred-backlog row |
| **i16** | Replace `cls` test instruction with a real spectrum4 one | 🧭 (was tentatively "i13" in conversation) | `docs/ROADMAP.md` deferred checklist |
| **i17** | Deep reviews of `main_loop.asm` + `litpool.asm` + `SYMTAB_*` equ sentinels | ⏳ | repo-audit §6 |
| **i18** | Wider naming review (`tests/m{N}`, `ci-m{N}`, `build-m3-disk`, milestone fixtures) | ⏳ | scope row "Directory naming" |
| **i19** | Subagent worktree-isolation leak — harden | 🧭 | scope row |
| **i20** | Linker-layout coupling (`spectrum4.ld` hardcoded in flatten) | 🧭 | scope row |
| **i21** | Go-harness fidelity follow-ups (watchpoint, `make harness-sweep`, USAGE.md) | ⏳ | scope row |
| **i22** | Per-fail-site diagnostic strings | 🧭 | deferred-backlog row |
| **i23** | Paged-IN >16.5 KB HLOAD-ceiling lift | 🧭 | deferred-backlog row |
| **i24** | 64 KB output / 16-bit `OUT_LEN` limit | 🧭 | deferred-backlog row |
| **i25** | `(hksp)` HSAVE/HLOAD error handler | 🧭 | deferred-backlog row |
| **i26** | text2bin operand-kind validation (Task 21) | 🧭 | deferred-backlog row |
| **i27** | Cortex-A53 errata workarounds (`--fix-cortex-a53-*`) | 🧭 | deferred-backlog row |
| **i28** | Absolute `.set` high-word edge vs Go | 🧭 | deferred-backlog row |
| **i29** | Harden slot self-test `PASS_PC` dependency | 🧭 | deferred-backlog row |
| **i30** | LDIR-fan-out for cross-page-shared blocks (smaller binary + faster boot) | 🧭 deferred/possible | `docs/plans/2026-06-07-strand-b-pr4-z80-disassembler-port.md` "Future enhancements" |
| **i31** | On-SAM preprocessor (`.if` build-constraints + macros) | 🧭 beyond-M7 | "Beyond-M7 / future ideas" |
| **i32** | Multiple source files + staged/partial loading | 🧭 beyond-M7 | "Beyond-M7 / future ideas" |
| **i33** | Trinity SD/flash storage → bigger-kernel architecture | 🧭 beyond-M7 | `memory/trinity_hardware.md` |
| **i34** | Untrack accidentally-committed Go binaries | ⏳ partial — `enctab-gen` untracked (PR #111); `llist-normalise` pending LLIST disposition (open-Q5) | `.gitignore`; open-question 5 |
| **i35** | `sdiv` missing across the whole stack (no mnemonic/Form → `.inst`; unencodable) | ✅ DONE (PR #115) | deferred-backlog row; mirrored `udiv` ID 72 |
| **i36** | Z80 disasm: ccmp/ccmn decode (was → `.inst`) — added decode + encoder (ccmn ID 100); binutils-aligned | ✅ DONE (PR #119) | `docs/notes/2026-06-08-z80-go-disasm-parity-i9.md` |
| **i37** | base csinv/csneg (Rn≠Rm): Go `aarch64dec` now decodes them (IDs 101/102), encoder coverage added; aliases intact | ✅ DONE (PR #119) | same analysis doc. The i9 sweep skip is removed; families now certified |
| **i38** | Audit for other skipped/excluded tests + PRs that left gaps rather than fixing them | ✅ DONE (PR #118) — 14 skip sites, 13 legitimate, only the i9 gap (now fixed by i36/i37); no other papered-over gaps | `docs/notes/2026-06-08-skipped-tests-and-gaps-audit.md` |
| **i39** | Next-gen maximally-efficient compact `.tbn` encoding (the design) — instruction overlay (assembled word + zeroed bits + sparse expression overlay, unifying literal/symbolic INST into one run), header label/offset table, one `.tbn` **v2** with a contiguous evictable editor region, name front-coding, bitfield-packing polish. **→ now milestone M8.** | 📋 **design agreed — Format B** (Pete 2026-06-08; all 5 open Qs resolved, §7). Implementation split into i39a/b/c. | `docs/notes/m8-status.md`; `docs/specs/2026-06-08-compact-tbn-nextgen-design.md` §7 |
| **i39a** | M8 Phase 1 — instruction overlay (unify literal/symbolic INST into one run) + header label/offset table; the v2 format flip | 📋 plan written (PR breakdown a–d on one branch; biggest risk = slot/fold-rule byte-match fidelity) | `docs/plans/2026-06-08-i39-phase1-instruction-overlay-plan.md` |
| **i39b** | M8 Phase 2 — name-table front-coding + comment/`.global`/base-hint editor sidecars (evictable region) | 🧭 designed | design §3.6/§3.7, §5 |
| **i39c** | M8 Phase 3 — bitfield-packing polish on the overlay slot bytes | 🧭 designed (low priority) | design §3.1 |
| **i40** | Assembler-side editor-region eviction (write editor region/`.tbn` to disk before assembling, reuse RAM as OUT/scratch, reload to restore the editor view). Lets "everything in one file" coexist with minimal resident RAM. **→ M8 (editor phase).** | 🧭 future — Pete 2026-06-08 | `docs/notes/m8-status.md`; design §7 (decision 1) |
| **i41** | Editor edit-buffer data structure — insertion performance for large source. A naive contiguous edit buffer (mutating the `.tbn` in place) requires shifting all bytes after an insert; at ~400 KB on the SAM Z80 that is ~1.5–2+ s (before paging overhead), exceeding the ~1 s/edit bound. **Resolution is architectural:** the `.tbn` is the *serialized* storage/assembly form (offset/PC-based, contiguous); the editor holds the source in a separate insertion-friendly in-memory document model (options: gap buffer / piece table / record linked-list) and serializes to `.tbn` on save/assemble — edits never shift the `.tbn`, and offsets/PCs are recomputed in one O(n) pass on serialize, not per keystroke. Comments/symbols are anchored by record-reference (or a stable record-id), NOT by byte-offset, so an insert touches only local nodes (no global reindex). Capture now; full edit-model design at editor time (Phase 2). Does NOT change i39 (i39 is the storage/assembly format). Cross-links: i39, i40, i3/i4 (editor). **Design explored (opus, 2026-06-08):** recommends a **paged block-list** (block = page, intra-block gap buffer, record-id side-tables), MVP gap-buffer first — worst-case edit ~30–80 ms at 400 KB, every op 0–2 page swaps; the edit buffer holds *source-shaped* records (overlay packing is serialize-time). 5 open Qs for Pete. | 📋 design explored — recommendation + 5 open Qs awaiting Pete (Pete 2026-06-08) | `docs/specs/2026-06-08-editor-edit-model-design.md` (design); `docs/notes/m8-status.md`; ROADMAP "Editor vision"; cross-links i39, i40, i3, i4 |
| **i42** | CRC / signature in the `.tbn` file header (integrity check) | 🧭 (deferred since M1; the v1 header has magic/version/flags but no integrity field) | `docs/notes/m1-status.md` "Known gaps" (CRC / signature in the file header) |
| **i43** | Editor: register simulator with user-chosen seeds (step instructions; inject/randomise source regs; show dest regs/flags/memory) + replay-on-edit | 🧭 Phase-2 editor sub-feature | `docs/ROADMAP.md` "Editor vision"; sub-feature of i3 |
| **i44** | Editor: inline instruction-explanation panel (prose semantics, flags set, regs read/written, bit-field layout) | 🧭 Phase-2 editor sub-feature | `docs/ROADMAP.md` "Editor vision"; sub-feature of i3 |
| **i45** | Editor: "did you mean a simpler instruction?" result-equivalent rewrite hint | 🧭 Phase-2 editor sub-feature | `docs/ROADMAP.md` "Editor vision"; sub-feature of i3 |
| **i46** | Editor: system-register documentation panel surfaced from the cursor line (embed ARM MRA subset + editorial overlay) | 🧭 Phase-2 editor sub-feature | `docs/ROADMAP.md` "Editor vision"; sub-feature of i3 |
| **i47** | Editor: "why is this instruction here?" inverse-flow navigation + retro UI affordances (chiptune, period fonts, animations) | 🧭 Phase-2 editor sub-features | `docs/ROADMAP.md` "Editor vision"; sub-features of i3 |
