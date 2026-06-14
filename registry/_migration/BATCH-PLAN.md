# Registry Migration — Dependency-Ordered Batch Plan

Generated 2026-06-19 as a read-only pre-pass for the i115f/i115d migration.

## PROGRESS (update as batches land)

- **Batches 1–7 DONE = ALL 101 closed items migrated.** Each independently reviewed; all PASS.
- **Batch 8 DONE** (open items i2–i27; i3 set kind:umbrella).
- **i48c SPLIT DONE** — umbrella + 13 brick leaves (b1–b4, b5a DONE; b5b–b8 OPEN).
- **Batch 9 remainder DONE** (i29–i65 open items + the i41 umbrella IN_PROGRESS).
- **All 5 OPEN questions migrated** (q13/q22/q23/q24/q25) + **i41 family completed**
  (i41d OPEN; i41e OPEN depends_on q24). Open questions went early so later dependent
  items can carry `--dep qN` and pass strict invariant-11.
- **STATE: 147 item records + 5 questions migrated; ~59 open-item rows remain** in
  `item-registry-open.md.old`. Every batch reviewed; all PASS.
- **REMAINING:** the ~59 open items (rest of batches 10–14 below) — several are
  UMBRELLAS whose children are already migrated (i100, i102, i102m → `--kind umbrella`,
  status DERIVED from children) — plus i7/i87/i111/i112/i119, the netboot cluster
  (i118/i120–i140), the BLOCKED:Pete items (i81a/i81c/i89/i117/i117a → owner:pete),
  i101 (--dep q23). Then **retire the 23 closed questions** (fold each decision into
  its item; do NOT migrate the closed question). Then the **CUTOVER**: strict
  `validate`, generate `.md` in place (`make registry` w/ `REGISTRY_OUTDIR=docs/notes`),
  retire `question-registry-closed.md` + rewire its refs, wire the `registry-sync` CI
  job + branch-protection check, i115e doc rewiring (CLAUDE.md/ROADMAP/SessionStart
  hook/autonomous-loop), dedupe the id-ledger, delete `_migration/` + the plan, §3
  review, single push → green → merge.

### Notes for the next session
- The 5 open questions are DONE — ignore them in the batch-12 list below. An item the
  old registry shows `BLOCKED:qN` → add `--dep qN`. An item gated on a bare Pete
  decision with no question → `--owner pete` (don't invent a qN).
- Tool hardened on-branch since the pre-pass: `add --kind/--pr/--role`; rune- (not
  byte-) length counting; trailing-newline trimmed before the 600-char bound. Run
  `make registry-gen` after checkout to rebuild `build/registry`.

### CORRECTION to the batch list below (next session, read this)

The batches-7/8 `i48c-bN` entries in the list below are **phantoms** — those bricks
are described *inside* the single `i48c` wall-of-text row (which is OPEN, in
`item-registry-open.md.old`); they are NOT separate `.old` rows. They get **minted
when `i48c` is split** during its batch (the i48c umbrella is in batch 9). So:
ignore the `i48c-bN` ids in batches 7–8; when migrating `i48c`, `add` the umbrella
then `add`/`split` one leaf per brick (b1–b4 + b5a DONE with their PRs; b5b–b8
OPEN), per the briefing's "Splitting" section. The real remaining `.old` open-item
rows are what drives batches 8–14.

The `--migrating` flag + the umbrella-status-derived rule + preserve-all-refs are
all in `MIGRATION-AGENT-BRIEFING.md`. Open items will have real `depends_on` edges
(old `BLOCKED:<x>` / blocked-on prose → an edge to the gating item/question); set
them with `--dep`. Closed questions are referenced by some items — fold the decision
into the item's desc, never `--ref` a q-id that will retire.

---

## 1. Row counts

| File | Rows |
|------|------|
| `item-registry-open.md` | 94 |
| `item-registry-closed.md` | 101 |
| `question-registry-open.md` | 5 |
| `question-registry-closed.md` | 23 |
| **Total** | **223** |

Note: i130 appears in the open file but its status cell reads `✅ DONE 2026-06-19` — it was closed but not yet moved. It should be treated as DONE at migration time, migrated to the closed view.

---

## 2. Dependency edges

Only `BLOCKED:<what>` tokens and explicit "blocked on / gated on" prose generate edges. The `refs`/pointer column is cross-linking, not dependency ordering.

### Explicit BLOCKED edges (open items file)

| Item | Depends on | Notes |
|------|-----------|-------|
| i41e | q24 | `BLOCKED:design+q24` |
| i81a | Pete (q?) | `BLOCKED:Pete` — no matching open qN; FLAG: no gating question found |
| i81c | Pete (q?) | `BLOCKED:Pete` — no matching open qN; FLAG: no gating question found |
| i87 | Pete/hardware (q?) | `BLOCKED:hardware` — no matching open qN |
| i89 | Pete/hardware (q?) | `BLOCKED:hardware` — no matching open qN |
| i100c | Pete (q?) | `BLOCKED:Pete` — no matching open qN |
| i101 | q23 | `BLOCKED:Pete-q23` |
| i117 | Pete (q?) | `BLOCKED:Pete` — no matching open qN |
| i117a | Pete (q?) | `BLOCKED:Pete` (via i117) |

### Prose-derived edges (blocking dependency in body)

| Item | Depends on | Source phrase |
|------|-----------|---------------|
| i7 | i74 | "the only remaining phase is D = i74 (gated on the `aarch64dec/sys.go` code→data refactor)" |
| i74 | (none — is the prerequisite) | "queue after i7 phase C" |
| i112 | i87 | "blocked on i87 (patched-ROM dump+diff) then hardware" |
| i133 | i132 | "needs i132 + RET-able+net-reporting program variants" |
| i41e | i41a | (sub-dependency; i41a is DONE so not blocking) |
| i115f | (none explicit, but runs before i115a-e rework) | Plan says "i115f runs FIRST" |
| i115a | i115f | rework needed to new schema that i115f defines |
| i115b | i115f | rework needed to new schema |
| i115c | i115a, i115b | Phase 3 builds on Phase 1+2 |
| i115d | i115c, i115f | Phase 4 (migration) requires CLI mutators + reshape |
| i115e | i115d | Phase 5 (rewiring) requires migration to be done |
| i115g | i115a, i115b, i115c, i115d, i115e | "later phase building on the core (i115a-e)" |
| i100c | i100 | sub-item of i100; i100 status shows i100a+i100b DONE, i100c remaining |
| i101 | q23, i100 | architecture gated on q23; i100 prereq |

### Dangling edges

- i81a, i81c, i87, i89, i100c, i117, i117a all have `BLOCKED:Pete` with no open qN as the explicit gate. Per the spec, "awaiting Pete / Pete's call" maps to a dependency on the relevant open question, OR a FLAG that a new qN would need minting. These are all hardware/community/product-call blockers; no new qN was explicitly noted as pending mint. These should become `depends_on: []` (Pete-gated items have no qN to reference) with the ownership set to `pete`.

- i41e depends on q24 — q24 IS in the open question registry. Valid edge.
- i101 depends on q23 — q23 IS in the open question registry. Valid edge.

### Cycle check

No cycles detected. The dependency graph is a DAG. Cross-references (refs column) can be cyclic (e.g. i119 ↔ i114) but those are not dependency edges.

---

## 3. Reshape / split list — wall-of-text / multi-deliverable rows

These rows need splitting into umbrella + leaves at migration. Confirmed by checking actual content.

### i48c — CONFIRMED, major split needed

The i48c row is a single ~5,856-character status cell tracking ~16 bricks across PRs #393–#435. This is the archetypal anti-pattern.

**Umbrella:** `i48c` — Z80 text→overlay encoder (editor input path)

**Leaves (by brick):** Each brick = one completing PR = one leaf. Bricks already landed (DONE, from the status cell text):
- `i48c-b1` — lexer: ident/reg-name/integer/string tokeniser
- `i48c-b1b` — lexer: line-end / comment / multi-char punctuation tokens
- `i48c-b1c` — lexer: error-recovery + the `active_raw` record
- `i48c-b1d` — lexer: label definitions (`.global` + local `1:` labels)
- `i48c-b2` — parser: top-level dispatch (mnemonic lines vs directives vs blank/comment)
- `i48c-b2a` — parser: register operands
- `i48c-b2b` — parser: immediate `#expr` operands
- `i48c-b2c` — parser: condition-code / shift / extend keywords
- `i48c-b3` — expression evaluator: `+ - | & ^ ~` / unary / parens subset
- `i48c-b3a2` — expression evaluator: shifts `<< >>`
- `i48c-b3a3` — expression evaluator: `*` (64-bit shift-add multiply)
- `i48c-b3a4` — expression evaluator: `/` (signed 64-bit division)
- `i48c-b3b` — symbol-intern + WriteSym (document symbol-table seam)
- `i48c-b3c` — PC (`.`) / local-ref / `:reloc:` primaries
- `i48c-b4` — memory operands (7 `[...]` shapes)
- `i48c-b5a` — movk/movz/movn special-form parse (DONE — PR #435)
- `i48c-b5b` — movl pseudo (movz+movk expansion)  → OPEN
- `i48c-b5c` — barriers dsb/dmb/isb → OPEN
- `i48c-b5d` — mrs/msr (sysreg/PSTATE name) → OPEN
- `i48c-b5e` — dc/tlbi (op-name + optional Xt) → OPEN
- `i48c-b5f` — `ldr =` literal-pool pseudo → OPEN
- `i48c-b6` — directives → OPEN
- `i48c-b7` — comments/blank-runs + i39c slot-packing → OPEN
- `i48c-b8` — integration/fixture round-trip (assembler-coupled, lands last) → OPEN

Note: the current status cell says B5a done (#435) and lists B5b–B8 as remaining. The earlier bricks (B1–B4c) don't have explicit DONE status in the current open-file row, but the plan says "B1–B7 are flat-memory-autonomous" and the closed registry should have the completed ones. **The i48c umbrella status should be checked against the closed registry for which bricks have already moved.** At migration: keep i48c as umbrella (OPEN/IN_PROGRESS), split DONE bricks to closed records (use existing ids if they appear in closed file, mint hyphenated ids for new leaves).

### i41 — CONFIRMED umbrella

The i41 row is an umbrella. Sub-items i41a, i41b, i41c are already DONE (in closed registry). i41d and i41e are open leaves. The i41 row itself should migrate as an umbrella with `kind: umbrella`, `status: OPEN` (since i41d and i41e are still open).

**Already split correctly in the registries** — just wire up properly:
- `i41` → umbrella, OPEN
- `i41a`, `i41b`, `i41c` → closed DONE leaves
- `i41d`, `i41e` → open leaves

### i115 — CONFIRMED umbrella

Already has explicit sub-items i115a–i115g. Migrate i115 as umbrella, sub-items as leaves with the ordering dependencies noted in section 2.

### i33 — NEEDS SPLIT

The i33 row (Trinity mass-storage → bigger-kernel architecture) is currently a single OPEN item with a long description covering: (a) using Trinity SD/flash to stage bigger assemblies than the floppy/RAM ceiling allows, and (b) the broader "storage-access half proven (netboot), assembler-scale deliverable unaddressed." The description acknowledges the storage-access half is proven. **Breakdown TBD** — the exact leaves aren't clear from the open file; flag for the migration agent to check the design doc and propose a split.

### i111 — NEEDS SPLIT

i111 ("Memory-read coverage / generic dead-code finder") says "designed (idea captured; phase-1 flat payloads first)". The "phase-1" implies multiple phases. **Breakdown TBD** — one brick for the flat-payload instrumentation, potentially another for the paged path.

### i102 — CONFIRMED umbrella

i102 is already marked as an umbrella with sub-items i102a–i102r (most in the closed registry). The open row tracks the umbrella status and remaining marginal passes. i102m is a separate open sub-item (sha256 rewrite umbrella), with i102n–i102r in the closed registry. Migrate i102 as umbrella (OPEN — diminishing returns), i102m as umbrella (OPEN), and all i102a–i102r leaves as DONE (already in closed).

### i100 — CONFIRMED umbrella

i100 is already effectively an umbrella: "host slice (i100a) + bootable Brick 7 (i100b) done; only i100c (picker UX) remains". i100a and i100b are in the closed registry; i100c is open (BLOCKED:Pete). Migrate i100 as umbrella, leaves i100a/i100b as closed DONE, i100c as open with `depends_on: []` (Pete's call, no gating qN).

### i39c — fine as-is

A single deferred leaf ("fold into i48c overlay-encoder work"). Migrate as a leaf with `depends_on: [i48c]`.

### i102m — CONFIRMED umbrella

Sub-items i102n–i102r are in the closed registry. Migrate i102m as umbrella with OPEN status (remaining marginal passes), leaves in closed.

---

## 4. Closed-question disposition (decisions that must be confirmed in items before retiring)

For each `qN` in `question-registry-closed.md`, the decision must already be curated into the relevant item(s). Closed questions do NOT migrate to the new model — they are verified and then deleted.

| qN | Decision | Primary item(s) | Notes |
|----|---------|-----------------|-------|
| q1 | Programmatic SAM-faithful renderer (not image generation) | i5, i76 | Folded into i76 workstream. Decision visible in i76a/b/c rows. |
| q2 | Next strand = compact-`.tbn` (i1) | i39, i1 | Decision executed; items closed. Verify in i39/i1 descriptions. |
| q3 | Native GitHub reviews, not plain comments | Governance (CLAUDE.md) | No item; folded into CLAUDE.md §3 "Reviewing PRs" rule. Confirm at migration that CLAUDE.md captures this. |
| q4 | Keep docs-based tracking, not GitHub Issues | i115 rationale | Captured in i115 description. |
| q5 | One integrated tool (`sam-aarch64`), not two CLIs | i48a | Folded into i48a description. |
| q6 | Value-dependent base words: compute at fold time (i48b) | i48b | Folded into i48b description. |
| q7 | Scope = no other GNU silent rewrites in corpus | i48a PR3 | Folded into i48a description (the q7 sweep). |
| q8 | Delete LLIST cluster | i52 PR 4 | Folded into i52 description. |
| q9 | Full-fidelity strategy: editor region off-pager + reload | i40, i51, i39b-2 | Folded into i39b-2/i40 descriptions. |
| q10 | Stay on SAMDOS 2 for now | No specific item | Note: decision may only be in git history. Confirm at migration. |
| q11 | Phase-3 mechanics + Colin boot-ROM specifics decided | i70, i83, i86, i87 | Decision captured in those items' prose. |
| q12 | Model A (PXE pull, SAM as server) | i83, i86, i95 | Captured in i83 description. |
| q14 | Autonomous-loop monitor proven live | i97 | i97 is DONE in closed. |
| q15 | Firmware .deb extraction strategy decided | i70, i88 | Decision captured in i70/i88 descriptions. |
| q16 | Incremental streaming write via B-DOS records | i99, i100 | Design captured in i99/i100 descriptions. |
| q17 | TLS 1.3 does NOT fit single bootable; crypto-only stretch | i88 | Captured in i88 description. |
| q18 | Build Brick 7 speculatively (provisional cap constant) | i100b | i100b is DONE in closed; q22 carries the hardware pin. |
| q19 | Real-hardware entropy: time-jitter seeding | i88 | Captured in i88 description. |
| q20 | Delete the retired per-mnemonic encoder library (i73 L9) | i73, i48c | Folded into i73 status; decision = delete. Verify in i73 description. |
| q21 | Rewrite SHA-256 (yes) | i102m | i102m is open (rewrite in progress). |
| q26 | Manifest design + 4 sub-choices decided | i114 | Decision captured in i114 description. |
| q27 | Free-record detection = on-card list read (not all-zeros scan) | i119 | Decision captured in i119 description and the design spec. |
| q28 | Migration = bounded-batch validated YAML (not markdown-first) | i115f | Folded into i115f description. |

**FLAG:** q3 (formal GitHub reviews) has no dedicated item — it's a CLAUDE.md policy, not a work item. The reviewer should confirm CLAUDE.md §3 captures the q3 decision before retiring it. If not, a sentence must be added to i115e (the CLAUDE.md rewiring item).

**FLAG:** q10 (stay on SAMDOS 2) has no clear item. Confirm the decision is captured somewhere (likely a docs/ARCHITECTURE.md note or a WONTFIX item). If not, document it in the migration.

---

## 5. Ordered batch plan

### Ordering rationale

Leaves-first up the DAG. Items with no `depends_on` edges go first. Items gated on open questions go after those questions. Umbrellas travel with their leaves. The i115 strand (the migration tooling itself) goes LAST in the open section since the cutover (i115d) is what enacts the migration.

### Summary of key dependency chains

```
i74 → i7               (i7 phase D gated on i74 code→data refactor)
i87 → i112             (i112 = SAM screen restore, gated on ROM dump)
q24 → i41e             (i41e = i48-IR payload, gated on serialize design)
q23 → i101             (i101 = capstone, gated on orchestration UX)
i132 → i133            (i133 = autonomous hardware test loop, gated on i132 DONE)
i115f → i115a, i115b   (rework to new schema)
i115a+b → i115c        (CLI mutators need skeleton+generator)
i115c → i115d          (migration needs CLI mutators)
i115d → i115e          (rewiring after cutover)
i115a-e → i115g        (priority queue builds on core)
```

### Complete batch list

**Note:** "closed" items (DONE/WONTFIX) all go into batches 1–7 since they have no blocking dependencies and constitute the bulk of the work. Open items with no gating deps go in batch 8–10. Gated open items go last.

---

#### Batch 1 — Closed items, early alphabet (no deps) [18 items]
`i1`, `i8`, `i9`, `i10`, `i11`, `i12a`, `i12c`, `i13`, `i14`, `i15`, `i17`, `i18`, `i26`, `i28`, `i34`, `i35`, `i36`, `i37`

Rationale: All DONE/WONTFIX. No dependencies on anything not already resolved. These are early-numbered items corresponding to M3–M7 era.

---

#### Batch 2 — Closed items, i38–i53 range [17 items]
`i38`, `i39`, `i39a`, `i39b`, `i39b-1`, `i39b-2`, `i40`, `i41a`, `i41b`, `i41c`, `i48`, `i48a`, `i48b`, `i48d`, `i49`, `i51`, `i52`

Rationale: All DONE. These are the M8 format/overlay items. i41a/b/c are sub-items of i41 (umbrella, open) — must migrate leaves before umbrella so the parent ref resolves. i39b splits into i39b-1 and i39b-2 (already have distinct ids). Include i53 here or batch 3.

---

#### Batch 3 — Closed items, i53–i79 range + i76 series [18 items]
`i53`, `i54`, `i57`, `i58`, `i59`, `i60`, `i61`, `i62`, `i63`, `i64`, `i67`, `i68`, `i71`, `i72`, `i75`, `i76`, `i76a`, `i76b`

Rationale: All DONE/WONTFIX. i76 series (host TUI prototype) — i76a/b/c/d are sub-items, all DONE in closed, so migrate leaves first then umbrella i76.

---

#### Batch 4 — Closed items, i76c–i99 range [17 items]
`i76c`, `i76d`, `i78`, `i79`, `i81`, `i81b`, `i82`, `i84`, `i85`, `i88a`, `i88b`, `i91`, `i94`, `i96`, `i97`, `i98`, `i99`

Rationale: All DONE. Includes i97 (autonomous-loop monitor, DONE), i82 (TFTP client, DONE), i84 (Phase-3 research, DONE). These are required before migrating their open dependents.

---

#### Batch 5 — Closed items, i100a–i102 series + sub-items [20 items]
`i100a`, `i100b`, `i102a`, `i102b`, `i102c`, `i102d`, `i102e`, `i102f`, `i102g`, `i102h`, `i102i`, `i102j`, `i102k`, `i102l`, `i102n`, `i102o`, `i102p`, `i102q`, `i102r`, `i105`

Rationale: All DONE. The i102 leaves (crypto optimization bricks) are numerous but all closed. i100a/b are DONE (closed). Must be in before i100 umbrella migrates in batch 9.

---

#### Batch 6 — Closed items, i107–i136 range [20 items]
`i107a`, `i107b`, `i108`, `i110`, `i125`, `i127`, `i128`, `i131`, `i132`, `i134`, `i136`

Then closed items from the high end:
`i57`, `i58`, `i59`, `i60`, `i61`, `i62`, `i63`, `i64` (if not already in batch 3 — check against actual closed ids)

Wait — let me reconcile. The closed ids extracted were:
`i1`, `i10`, `i100a`, `i100b`, `i102a-r` (18 items), `i105`, `i107a`, `i107b`, `i108`, `i110`, `i125`, `i127`, `i128`, `i11`, `i12a`, `i12c`, `i13`, `i131`, `i132`, `i134`, `i136`, `i14`, `i15`, `i17`, `i18`, `i26`, `i28`, `i34`, `i35`, `i36`, `i37`, `i38`, `i39`, `i39a`, `i39b`, `i39b-1`, `i39b-2`, `i40`, `i41a`, `i41b`, `i41c`, `i48`, `i48a`, `i48b`, `i48d`, `i49`, `i51`, `i52`, `i53`, `i54`, `i57`, `i58`, `i59`, `i60`, `i61`, `i62`, `i63`, `i64`, `i67`, `i68`, `i71`, `i72`, `i75`, `i76`, `i76a`, `i76b`, `i76c`, `i76d`, `i78`, `i79`, `i8`, `i81`, `i81b`, `i82`, `i84`, `i85`, `i88a`, `i88b`, `i9`, `i91`, `i94`, `i96`, `i97`, `i98`, `i99`

That's ~101 closed items total.

---

**REVISED BATCH PLAN** (corrected using the full sorted closed-id list):

---

#### Batch 1 — Closed items A: i1–i18 [18 items]
`i1`, `i8`, `i9`, `i10`, `i11`, `i12a`, `i12c`, `i13`, `i14`, `i15`, `i17`, `i18`, `i26`, `i28`, `i34`, `i35`, `i36`, `i37`

No dependencies between these. All DONE/WONTFIX. Proving batch (extra scrutiny on generated output).

---

#### Batch 2 — Closed items B: i38–i54 (M8 format/overlay strand) [17 items]
`i38`, `i39`, `i39a`, `i39b`, `i39b-1`, `i39b-2`, `i40`, `i41a`, `i41b`, `i41c`, `i48`, `i48a`, `i48b`, `i48d`, `i49`, `i51`, `i53`

i39b before i39, i39a before i39, i41a/b/c before i41 (umbrella). i51 depends on i40; i40 can go in same batch. i53 is WONTFIX.

Also add: `i54`

18 items total.

---

#### Batch 3 — Closed items C: i52 + i57–i79 range [17 items]
`i52`, `i57`, `i58`, `i59`, `i60`, `i61`, `i62`, `i63`, `i64`, `i67`, `i68`, `i71`, `i72`, `i75`, `i78`, `i79`

All DONE. i52 is the repo cleanup.

16 items.

---

#### Batch 4 — Closed items D: i76 series + i81/i82/i84/i85 [15 items]
`i76a`, `i76b`, `i76c`, `i76d`, `i76`, `i81`, `i81b`, `i82`, `i84`, `i85`, `i88a`, `i88b`

i76 leaves (i76a-d) must come before i76 umbrella. i82 is DONE (TFTP client). i84 is DONE (Phase-3 research).

12 items — pad with any small DONE items missed.

---

#### Batch 5 — Closed items E: i91–i110 range [18 items]
`i91`, `i94`, `i96`, `i97`, `i98`, `i99`, `i100a`, `i100b`, `i105`, `i107a`, `i107b`, `i108`, `i110`

i100a, i100b must be before i100 umbrella (batch 9).

13 items — merge with i125, i127, i128, i131, i132, i134, i136.

---

#### Batch 6 — Closed items F: i102 sub-items (the large set) [19 items]
`i102a`, `i102b`, `i102c`, `i102d`, `i102e`, `i102f`, `i102g`, `i102h`, `i102i`, `i102j`, `i102k`, `i102l`, `i102n`, `i102o`, `i102p`, `i102q`, `i102r`

17 items — the full i102 crypto-optimization brick set. Must be before i102 umbrella and i102m umbrella.

Add: `i125`, `i127`

19 items.

---

#### Batch 7 — Remaining closed items + closed i48c bricks [18 items]
`i128`, `i131`, `i132`, `i134`, `i136`

Plus any i48c bricks that have already landed in the closed registry (check: the open file says B5a done at #435 — any earlier bricks in the closed file?)

Given the i48c status cell lists B1–B5a as mostly complete, and PRs #393–#435 are referenced, there should be i48c sub-item leaves in the closed registry. The closed-registry extraction earlier showed no `i48c-*` ids — this means they have NOT been split yet into the closed registry. They will be created during migration. So batch 7 should focus on minting the DONE i48c leaf bricks.

Revised Batch 7 — Remaining closed + newly-minted i48c DONE leaves [18 items]:
`i128`, `i131`, `i132`, `i134`, `i136`
+ mint and migrate as DONE: `i48c-b1`, `i48c-b1b`, `i48c-b1c`, `i48c-b1d`, `i48c-b2`, `i48c-b2a`, `i48c-b2b`, `i48c-b2c`, `i48c-b3`, `i48c-b3a2`, `i48c-b3a3`, `i48c-b3a4`, `i48c-b3b`

18 items.

---

#### Batch 8 — Remaining DONE i48c leaves + open leaf items (no deps) [18 items]

Mint and migrate as DONE: `i48c-b3c`, `i48c-b4`, `i48c-b5a`

Open items with NO blocking dependencies (OPEN, ungated):
`i2`, `i3`, `i4`, `i5`, `i6`, `i12b`, `i16`, `i19`, `i20`, `i21`, `i22`, `i23`, `i24`, `i25`, `i27`

18 items.

---

#### Batch 9 — Open items, umbrellas + gated-but-uncomplicated [18 items]
`i29`, `i30`, `i31`, `i32`, `i33` (flag for split), `i39c`, `i41` (umbrella), `i42`, `i43`, `i44`, `i45`, `i46`, `i47`, `i48c` (umbrella, OPEN/IN_PROGRESS), `i50`, `i55`, `i56`, `i65`

Note: i39c has a soft dependency on i48c (folds into i48c work), but i48c umbrella is in this batch too so ordering within batch is fine (i48c before i39c).

18 items.

---

#### Batch 10 — Open items, mid-range [17 items]
`i66`, `i69`, `i70`, `i73`, `i74`, `i77`, `i80`, `i83`, `i86`, `i88`, `i90`, `i92`, `i93`, `i95`, `i100` (umbrella), `i100c`, `i102` (umbrella)

Note: i74 must come before i7 in this batch (i7 depends on i74). i100 umbrella after i100a/b (closed, batch 5). i100c after i100.

17 items.

---

#### Batch 11 — Open items, i7 + gated items [16 items]
`i7` (depends on i74, which is now in batch 10), `i102m` (umbrella; i102n-r in closed, batch 6), `i103`, `i104`, `i106`, `i109`, `i111` (flag for split), `i112` (depends on i87, also in this batch — put i87 first), `i87` (BLOCKED:hardware, depends_on: []), `i113`, `i114`, `i116`, `i118`, `i119`

Note: i87 has no true gating dependency beyond Pete's hardware action (no qN), so it migrates as OPEN with `depends_on: []` and `owner: pete`. i112 depends on i87, so i87 must precede i112 in this batch.

14 items — add `i120`, `i121`.

16 items.

---

#### Batch 12 — Open items, higher-numbered and hardware-gated [17 items]
`i122`, `i123`, `i124`, `i126`, `i129`, `i130` (DONE — correct file at migration), `i133` (depends on i132; i132 is DONE in closed batch 5, so ok), `i135`, `i137`, `i138`, `i139`, `i140`

12 items — add open questions:
`q13`, `q22`, `q23`, `q24`, `q25`

17 items.

---

#### Batch 13 — BLOCKED:Pete items + i117 cluster [10 items]
`i81a` (BLOCKED:Pete), `i81c` (BLOCKED:Pete), `i89` (BLOCKED:hardware), `i101` (gated on q23 — q23 migrated batch 12), `i117` (BLOCKED:Pete), `i117a` (BLOCKED:Pete, sub-item of i117)

Also: i41e (gated on q24 — q24 migrated batch 12), i41d (OPEN, no gates)

8 items — pad with remaining open items not yet placed.

---

#### Batch 14 — i115 strand (last, after all items migrated) [7 items]
`i115f`, `i115a`, `i115b`, `i115c`, `i115d`, `i115e`, `i115g`

Ordering within batch: i115f → i115a → i115b → i115c → i115d → i115e → i115g.

Rationale: The i115 strand IS the migration tooling. It migrates last because (a) its plan says "i115f runs FIRST before other i115 parts" (but first in the execution order of the WORK, not migration order), and (b) these items reference the migration plan and spec that are being used RIGHT NOW — they should migrate when everything else is settled, as the final bookkeeping.

---

## 6. Complete ordered batch list (ids only)

```
Batch  1 [18]: i1, i8, i9, i10, i11, i12a, i12c, i13, i14, i15, i17, i18, i26, i28, i34, i35, i36, i37
Batch  2 [18]: i38, i39, i39a, i39b, i39b-1, i39b-2, i40, i41a, i41b, i41c, i48, i48a, i48b, i48d, i49, i51, i53, i54
Batch  3 [16]: i52, i57, i58, i59, i60, i61, i62, i63, i64, i67, i68, i71, i72, i75, i78, i79
Batch  4 [12]: i76a, i76b, i76c, i76d, i76, i81, i81b, i82, i84, i85, i88a, i88b
Batch  5 [13]: i91, i94, i96, i97, i98, i99, i100a, i100b, i105, i107a, i107b, i108, i110
Batch  6 [19]: i102a, i102b, i102c, i102d, i102e, i102f, i102g, i102h, i102i, i102j, i102k, i102l, i102n, i102o, i102p, i102q, i102r, i125, i127
Batch  7 [18]: i128, i131, i132, i134, i136, i48c-b1, i48c-b1b, i48c-b1c, i48c-b1d, i48c-b2, i48c-b2a, i48c-b2b, i48c-b2c, i48c-b3, i48c-b3a2, i48c-b3a3, i48c-b3a4, i48c-b3b
Batch  8 [18]: i48c-b3c, i48c-b4, i48c-b5a, i2, i3, i4, i5, i6, i12b, i16, i19, i20, i21, i22, i23, i24, i25, i27
Batch  9 [18]: i29, i30, i31, i32, i33, i39c, i41, i42, i43, i44, i45, i46, i47, i48c, i50, i55, i56, i65
Batch 10 [17]: i66, i69, i70, i73, i74, i77, i80, i83, i86, i88, i90, i92, i93, i95, i100, i100c, i102
Batch 11 [16]: i7, i87, i102m, i103, i104, i106, i109, i111, i112, i113, i114, i116, i118, i119, i120, i121
Batch 12 [17]: i122, i123, i124, i126, i129, i130, i133, i135, i137, i138, i139, i140, q13, q22, q23, q24, q25
Batch 13 [ 8]: i81a, i81c, i89, i41d, i41e, i101, i117, i117a
Batch 14 [ 7]: i115f, i115a, i115b, i115c, i115d, i115e, i115g
```

**Total: 14 batches, ~215 rows** (the 223 total minus 23 closed questions that RETIRE rather than migrate — and plus ~13 newly-minted i48c DONE leaf ids).

---

## 7. Flags and anomalies

1. **i130 in wrong file**: `i130` is in `item-registry-open.md` but its status cell reads `✅ DONE 2026-06-19`. It should have been moved to the closed file. At migration time it should be added as a DONE leaf in `registry/items.yaml` (generating to the closed view) and deleted from the open `.old` file.

2. **i48c DONE bricks not yet split**: No `i48c-*` sub-ids appear in the closed registry. The split must be done at migration time. The migration agent needs to identify which bricks are DONE (PRs #393–#434, bricks B1–B5a) and which are OPEN (B5b–B8). The PR list in the status cell is the ground truth.

3. **i81a, i81c, i87, i89, i100c, i117, i117a — no gating qN**: These are `BLOCKED:Pete` or `BLOCKED:hardware` but have no corresponding open qN in the question registry. Per the new model they migrate as OPEN with `depends_on: []` and `owner: pete` (or equivalent). No new qN needs to be minted for them.

4. **i33, i111 — breakdown TBD**: These should be split but the exact leaf breakdown isn't clear from the open file alone. The migration agent should check `docs/specs/` and the item description carefully and propose a split to the reviewer, or migrate as a single leaf initially and note the split is deferred.

5. **q3, q10 no item**: q3 (formal GitHub reviews) and q10 (stay on SAMDOS 2) have no dedicated item. Confirm the decision is in CLAUDE.md / ARCHITECTURE.md before retiring at migration. If not found, add a sentence to the appropriate item (i115e for q3, ARCHITECTURE.md for q10).

6. **Batch 7 i48c DONE leaves — new id minting**: The i48c brick ids listed (`i48c-b1` etc.) are proposed. The actual ids may differ from the names used in the status cell prose — the migration agent must choose stable hyphenated ids (e.g. `i48c-b1`, `i48c-b1b`, etc.) and add them to the ledger. These are NEW ids being minted at migration time.

7. **Batch ordering caveat — i7 depends on i74**: i74 (OPEN, no gating deps) and i7 (OPEN, gated on i74) are in batches 10 and 11 respectively. This is correct: i74 migrates in batch 10, i7 with `depends_on: [i74]` migrates in batch 11.

8. **q28 is RESOLVED and folded into i115f**: q28 does not appear in the open question file — it was resolved and folded. Confirm at migration that i115f description captures the q28 decision (bounded-batch audited migration). It does — the i115f status cell says "q28 RESOLVED → it IS the bounded-batch migration."
