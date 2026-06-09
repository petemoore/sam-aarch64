# Question index — the project-wide `qN` registry

This is the **canonical, milestone-neutral registry** of every **open question for
Pete** (decisions an agent cannot settle itself, or chose to defer rather than
guess). It is the sibling of the item registry (`docs/notes/item-registry.md`): the
item registry tracks *work* (`iN`), this tracks *unresolved decisions* (`qN`).

**Governance is identical to the item registry — see `docs/notes/item-registry.md`
for the shared registry discipline** (stable ids locked once assigned, never
renumbered, never archived, marked-resolved rather than deleted). In one line for
questions: `qN` ids; the table is the **single sure-fire list of what's still open**;
add the moment a question arises (not just in chat — chat questions get lost in
simultaneous-edit races); flip ⏳→✅ with the resolution + where it was decided the
instant it's answered; land on `main` via PR so Pete sees it where he reads.

This doc is **never archived**: when a milestone status doc is superseded, its
questions live on here, so an open question is never stranded in an archived
`m{N}-status` doc. (Created 2026-06-08 by extracting the `qN` list out of
`m7-status.md` — mirroring the i*N* item-registry extraction in PR #128 — because
M7 was winding down while open questions like q1 were still live. See the
`feedback_capture_open_questions` memory.)

| id | question | status | pointer |
|----|----------|--------|---------|
| **q1** | **i5 graphics tooling** — for editor UI mockups at SAM-accurate resolution: (a) a programmatic SAM-faithful renderer I write (PNG / SAM `SCREEN$` at exact MODE 3 / MODE 4 geometry + the real palette), (b) a dedicated retro/pixel-art tool you drive by hand, or (c) a generic AI image generator? I have no native image gen and a generic generator wouldn't respect the hard SAM constraints, so (a) is the honest best for *faithful* mockups, (b) for hand-authored final art. | ⏳ **OPEN — Pete researching tools the agent can drive**; parked on his side, no agent action meanwhile | item registry i5 |
| **q5** | **i48 host packaging** — once the symbolic `.tbn` is gone: two CLIs over a shared front-end library, or one integrated tool? | ✅ RESOLVED (Pete 2026-06-09) — **one integrated tool.** The real decision is the library factoring (text→overlay encoder / overlay→bytes assembler / overlay→text renderer as shared, dual-target, Go-authoritative libs — those are what port to Z80, not the CLI shape); on top, one host tool **mirrors the SAM's single integrated assembler** (the cleanest port reference), staged modes (`--emit-tbn` / assemble-from-`.tbn`) as flags, production = a single pass-1. The two-binary split was an M1–M6 artifact; it stopped being free once the SAM must do text→overlay itself. | `docs/specs/2026-06-08-i48-single-format-syntactic-encoder-design.md` §3, §8; item registry i48a |
| **q6** | **i48 editor model for value-dependent base words** — at edit time a referenced symbol may be unresolved (e.g. `mov Rd, fwd_label` before the label), so the editor can't always pick the final base word: keep the element tentative/symbolic until assemble, or default-then-validate? Interacts with **i41** (edit-model). | ✅ RESOLVED (Pete 2026-06-08, via the i41 design decision #3 + i48b) — the editor holds the **i48 symbolic IR, not base words**, so there is no edit-time base word to pick; the value-dependent bits are computed in the **fold at serialize/assemble** (i48b), where the symbol pass exists. "Keep symbolic until assemble" — the edit-time concern dissolves. | `docs/specs/2026-06-08-editor-edit-model-design.md` §7.3; i48 spec §4; item registry i41, i48b |
| **q7** | **i48 strictness scope** — we forego GNU's silent `ldr→ldur` and `add lsl #12` auto-rewrite (→ syntactic / error). Any other "generous" GNU rewrites in the corpus to treat the same way? | ✅ RESOLVED (i48a PR3 sweep, 2026-06-09) — **no additional value-dependent silent form-rewrites beyond `ldr→ldur` and `add #big→lsl#12`.** Swept the M1–M6 sources + the flattened `release.s` (90 files, ~14k lines) by category, with `aarch64-none-elf-as`/objdump as the GNU oracle and `build/sam-aarch64`'s overlay path (text→overlay→bytes, exercising the strict `overlayClassify`) as ours. Distinguish value-dependent **field** computation (allowed — done at assemble, where the value is in hand; opcode unchanged) from value-dependent **form/opcode** rewrites (the forbidden kind). Findings below; the whole 21752-byte release round-trips through the overlay path **byte-identical to GNU**, and the per-instruction `assertOverlay` guard (`assemble/overlay.go`) would fail loudly on any silent divergence. See full evidence in the i48a-pr3 report. | i48 spec §4, §8; item registry i48a |
| **q8** | **LLIST tool-cluster disposition** (`tools/llist-*` + `llist-*.sh` + the `llist-normalise` binary under i34) — archive / delete / keep? Superseded by the EDIT/EDKY detokeniser spike. | ⏳ OPEN — punted (Pete 2026-05-29); leave the cluster in place until decided | item registry i34; `tools/README.md` |
| **q2** | Next major strand after M7 → **compact-`.tbn` (i1)**. | ✅ RESOLVED (Pete 2026-06-08) — "let's compact .tbn"; shipped as i1 (PRs #121/#122/#124) | item registry i1 |
| **q3** | Formal GitHub reviews for pre-merge review agents? | ✅ RESOLVED (Pete 2026-06-08) — **YES**; record each pre-merge review with `gh pr review <n> --comment` (not `--approve`); codified in project `CLAUDE.md` §3 | project `CLAUDE.md` §3 |
| **q4** | Work tracking → GitHub Issues, or keep the docs? | ✅ RESOLVED (Pete 2026-06-08) — keep the `iN`/`qN` doc registries, **no GitHub Issues** for now; fully reversible | this file; item registry |

*Pre-`qN` resolved questions (historical, kept for the record):* `src/m3`→flat-`src/`
rename — resolved (PR #82); design-strand handling (agent drives design solo, review
later) — resolved (Pete 2026-05-29). These predate the `qN` convention and are done;
no active id.

## q7 sweep findings (i48a PR3, 2026-06-09)

The full per-category enumeration behind the q7 resolution above. The corpus swept is `tests/m{1,3,4,5,6}/sources/*.s` plus the flattened `tests/m6/release/release.s` (90 source files). The test: every file was assembled through `build/sam-aarch64`'s overlay path (`-o … --emit-tbn …` then re-assemble the `.tbn`), which routes each symbol/PC-bearing instruction through the strict `overlayClassify`; GNU `aarch64-none-elf-as` + objdump was the oracle. The release re-assembled from its overlay `.tbn` byte-identically to the vendored GNU `release.img` (21752 B), and every M1–M6 fixture round-tripped clean.

The load-bearing distinction is **value-dependent field encoding (allowed)** vs **value-dependent form/opcode rewrite (the forbidden kind)**. The former happens at assemble time where the value is in hand and the opcode is unchanged (just the immediate's bits); the latter is GNU silently choosing a *different instruction form* than the mnemonic the user typed — that is what Decision B turns into an error for symbolic operands. The two known cases (`ldr→ldur`, `add #big→lsl#12`) are the only members of the forbidden class the corpus contains.

Categories checked:

- **`mov Rd, Rn` (register form)** — purely syntactic (alias keyed by mnemonic+operand kinds in `aarch64enc/manual_forms.go`: ORR Rd,XZR,Rm or ADD-#0 for SP). 74 corpus uses. Not value-dependent; not q7.
- **`mov Rd, #imm` (movz / movn / orr-bitmask)** — value-dependent *opcode*. Exercised (`tests/m6/sources/inst_mov_movn.s`, `inst_mov_bitmask.s`, 272 mov-imm total). **Already handled correctly** per spec §4(c): `movz` is the syntactic default, with `movn`/`orr` fallback at *assemble* time (value in hand); symbolic `mov` overlay supports movz-auto + logical and errors otherwise. Byte-matches GNU. This is the narrow, sanctioned try-then-fallback — not a forbidden silent rewrite.
- **`cmp` / `cmn` / `tst` / `mvn` / `neg` / `negs` / `ccmp` / `ccmn` aliases** — the mnemonic the user types fixes the form (all keyed by mnemonic ID in `manual_forms.go` → SUBS/ADDS/ANDS/ORN with zr baked). 200+ uses. Syntactic; not q7.
- **`ldr`/`str` scaled→unscaled (`ldur`/`stur`)** — value-dependent for the offset. 5 *constant* negative-offset cases (`tests/m5/sources/inst_mem_str_promote.s`, `[x0,#-1]!`) — these promote at parse (value in hand), byte-match GNU, and stay allowed (spec §4b "constant operands keep their parse-time choice"). **Zero symbolic mem offsets** in the corpus (`grep '\[…, :lo12:'` → 0): the strict symbolic-mem→error path (`FoldMemImm12`, verified to error on a constructed unfitting `:lo12:` offset) costs nothing. This is one of the two known forbidden-class members; already handled.
- **`add`/`sub` large-immediate `lsl #12` split** — value-dependent. 8 *constant* cases (`add x1,x1,#0x1000`, `#0x200000`, `#0x140000`) — all 4096-multiples, promoted at parse by `encodeImm12Shifted`, byte-match GNU. 2 explicit `lsl #12` uses (kept syntactic). **Zero symbolic add-imms that overflow 12 bits** (the 27 `:lo12:` add-imms all fit by construction). A constructed `.set BIG,0x200000 / add x1,x1,#BIG` (symbolic eval path) correctly **errors** (`FoldAddSubImm12: … lsl #12 must be explicit`) where GNU silently splits — the second known forbidden-class member; already handled.
- **logical-immediate canonicalization (`and`/`orr`/`eor`/`ands`/`bic` `#imm`)** — value-dependent *field* (N:immr:imms), but the **opcode is unchanged**. 99 uses; byte-match GNU. Allowed (encode-time field computation, not a form-rewrite); not q7.
- **`ldr Rd, =val` literal-pool synthesis** — 69 uses. The task brief flagged GNU's "`=small`→`mov`" optimization, but `aarch64-none-elf-as` at the default arch level does **not** do it: every `ldr =val` (down to `=0`/`=1`) stays a real pool load + `.word`/`.quad` slot. Our tool also keeps the pool form (`FoldLitpool19`); byte-matches GNU. No divergence; not q7.
- **`add`/`cmp` negative-immediate auto-swap (`add #-N`→`sub`, `cmp #-N`→`cmn`)** — value-dependent form, but **zero** occurrences in the corpus. Not exercised; nothing to handle.
- **`b`/`bl`/`adr`/`adrp` + `:lo12:`** — range-checked at fold time (a value out of range is an error, never a silent form change). 168+ uses; byte-match GNU. Syntactic form selection; not q7.

Conclusion: the corpus exercises several value-dependent *field* computations and the two known value-dependent *form-rewrites*, all already handled. **No third member of the forbidden class exists in the M1–M6 + release corpus**, so q7's strictness scope is exactly `ldr→ldur` + `add #big→lsl#12`. No code change is implied by this sweep.
