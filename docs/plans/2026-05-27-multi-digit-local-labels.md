# Multi-digit local labels — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Allow the SAM-side Z80 assembler to handle local labels with two-digit values (`10`..`99`) in addition to single-digit (`1`..`9`). This unblocks `inst_ldr_litpool_local.s` (deferred from M5) and is a hard prerequisite for the spectrum4 release sources, which use locals up to `15:`.

**Architecture:** Replace the 9-per-digit fixed-array layout in `src/local_labels.asm` with a single shared sorted list of `(digit:u8, pc:u32)` tuples — 5 bytes per entry, capped at 200 total entries (1002 B, fits the existing 1 KB slot at `&CD60..&D15F`). The ABI is unchanged: callers still pass digit in `A` and PC via `local_label_pc_buf`. The Go-side tooling (`text2bin` lexer/parser, `refenc` pass1/pass2, `bin2text` emitter) already handles 1..99 — only the Z80 side needs to change.

**Tech Stack:** Z80 assembly in the pyz80 dialect; SAM Coupé under SimCoupé in the `ghcr.io/petemoore/sam-aarch64-dev:latest` container (Xvfb-driven); Go tooling on the host for golden-fixture generation.

---

## Why one shared list rather than a 99-entry table?

- A 99-entry per-digit table (mimicking the current 9-entry one) would need at minimum 99 × 6 bytes = ~600 B for indexing alone, and the per-digit cap would have to drop sharply (currently 24) to keep memory bounded — defeating the point.
- A single shared list (≤ 200 entries, 5 B each = 1000 B) gives more flexibility in how entries are distributed across digits, fits the existing slot, and yields simpler code (no per-digit pointer table, no awkward `*98` stride).
- Linear scan per lookup is O(entries-in-the-file), capped at 200. Real assembly files have ≪ 200 locals across all digits; spectrum4 release.s has < 100. Performance is fine.
- Definition order ≡ PC order, so the list stays sorted by PC automatically (same invariant the current implementation relies on).

## File structure

- **Modify**: `src/local_labels.asm` — full rewrite of the storage layout and the three lookup routines (`local_def_append`, `local_find_forward`, `local_find_backward`). `cmp_pc_at_hl_vs_ref` and `local_label_pc_buf` are preserved verbatim. ABI is unchanged.
- **Modify**: `src/test_local_labels.asm` — extend the self-test to cover two-digit digits (`10` and `99`), and to verify cross-digit isolation between a single-digit digit (`1`) and a two-digit one (`10`).
- **Modify**: `src/assembler.asm` — update the memory-map comment block (line ~58) so the LOCAL_LABEL_TABLE reservation matches the new shape (200 × 5 = 1000 B).
- **Modify**: `docs/specs/2026-05-24-m4-symbols-multipass-design.md` §2.3 — note the extended digit range and the new shared-list layout. Keep references to the old design as "v1; superseded by multi-digit work" so the design history is legible.
- **Create**: `tests/m5/sources/inst_ldr_litpool_local.s` — copy verbatim from `tests/m1/sources/inst_ldr_litpool_local.s`. Add to the M5 fixture corpus.
- **Modify**: `docs/notes/m5-status.md` — promote `inst_ldr_litpool_local.s` from the "deferred" list to the active corpus; bump the fixture count from 19 to 20.
- **Modify**: `docs/ROADMAP.md` — tick off the multi-digit-local-labels item in the deferred-work checklist; cite this PR.

No changes required in:

- `tools/text2bin/internal/translate/lexer.go` — `readNumberOrLocal` already handles two-digit (lines 245–283).
- `tools/text2bin/internal/translate/parser.go` — already accepts `t.Int >= 1 && t.Int <= 99` (line 55).
- `tools/refenc/pass1.go` — `LocalDefs map[byte][]int64` is keyed by digit byte; already supports 0..255 (line 29).
- `tools/refenc/pass2.go` — `LocalLabel` callback takes `digit byte`; already digit-byte-agnostic (line 153).
- `tools/bin2text/emit/emit.go` — formats with `%d` (line 35); already digit-byte-agnostic.
- `tools/sam-aarch64-format/{writer,reader,kinds}.go` — `KindLocalDef` payload is `digit byte`; format unchanged.

---

### Task 1: Rewrite `local_labels.asm` storage + append

**Files:**
- Modify: `src/local_labels.asm` (full file)

The new memory layout, replacing the 9-list layout at the top of the file:

```
LOCAL_LABEL_TABLE:      equ     &CD60   ; 2-byte count + 200 * 5-byte entries = 1002 bytes
LOCAL_LIST_MAX:         equ     200     ; total entries across ALL digits
LOCAL_ENTRY_SIZE:       equ     5       ; 1 byte digit + 4 bytes PC (LE)
LOCAL_MAX_DIGIT:        equ     99      ; digit range 1..99 inclusive
```

- [ ] **Step 1: Rewrite the doc header**

Replace the existing per-digit-list doc header (lines 1–58 of the current file) with one describing the new shared-list layout. The header MUST cite:

- `docs/specs/2026-05-24-m4-symbols-multipass-design.md` §2.3 (digit range update — see Task 5).
- This plan: `docs/plans/2026-05-27-multi-digit-local-labels.md`.
- The 1 KB slot reservation in `assembler.asm`.

Specifically include the on-disk layout block:

```
; Memory layout (1 KB slot at &CD60..&D15F):
;
;   offset 0..1   count   u16 LE     (entries used; 0..LOCAL_LIST_MAX)
;   offset 2..    entries — for i in 0..count:
;     base + 2 + i*5 + 0       = digit (u8, 1..99)
;     base + 2 + i*5 + 1..4    = pc    (u32 LE)
;
; LOCAL_LIST_MAX = 200 → 2 + 200*5 = 1002 bytes ≤ 1024-byte slot.
```

Keep the ABI summary intact (the three public routines and their inputs / outputs / clobbers).

- [ ] **Step 2: Replace `local_label_table_init`**

The new init only needs to zero the 2-byte count:

```
local_label_table_init:
                ld      hl, LOCAL_LABEL_TABLE
                ld      (hl), 0                     ; count LSB
                inc     hl
                ld      (hl), 0                     ; count MSB
                ret
```

The pointer table (`local_list_bases`) is removed entirely — there is no per-digit indexing anymore.

- [ ] **Step 3: Replace `local_def_append`**

```
local_def_append:
                ld      (local_saved_digit), a      ; A = digit, save

; Validate digit ∈ [1, 99].
                or      a
                jp      z, fail                     ; digit 0 invalid
                cp      LOCAL_MAX_DIGIT + 1
                jp      nc, fail                    ; digit > 99 invalid

; Load count (LSB into A; high byte known to be 0 since cap=200).
                ld      hl, LOCAL_LABEL_TABLE
                ld      a, (hl)                     ; A = count LSB
                inc     hl
                ld      e, (hl)
                or      e
                jp      nz, fail_overflow_local     ; high byte != 0 ⇒ corruption
                dec     hl                          ; HL = base
                ld      a, (hl)                     ; A = count LSB (reload)
                cp      LOCAL_LIST_MAX
                jp      nc, fail                    ; list full

; Compute entry address: base + 2 + count*5.
                ld      e, a                        ; E = count
                ld      d, 0
                ex      de, hl                      ; HL = count, DE = base
                ld      b, h
                ld      c, l                        ; BC = count
                add     hl, hl                      ; *2
                add     hl, hl                      ; *4
                add     hl, bc                      ; *5
                ld      bc, 2
                add     hl, bc                      ; + 2 (skip count field)
                add     hl, de                      ; + base → HL = &entries[count]

; Write digit byte.
                ld      a, (local_saved_digit)
                ld      (hl), a
                inc     hl

; Write 4-byte PC from local_label_pc_buf.
                ld      de, local_label_pc_buf
                ex      de, hl                      ; HL = src, DE = dest
                ld      bc, 4
                ldir

; Bump count (LSB only; high byte stays 0 because cap < 256).
                ld      hl, LOCAL_LABEL_TABLE
                inc     (hl)
                ret
```

Where `fail_overflow_local` is reusable as a local jump target inside the file, OR just `jp fail` directly — pick whichever matches the rest of `local_labels.asm`'s style. (The current file uses `jp fail` everywhere — match that.)

- [ ] **Step 4: Replace `local_find_forward`**

```
local_find_forward:
                ld      (local_saved_digit), a      ; A = target digit

; Validate digit ∈ [1, 99] (mirrors append).
                or      a
                jp      z, fail
                cp      LOCAL_MAX_DIGIT + 1
                jp      nc, fail

                ld      hl, LOCAL_LABEL_TABLE
                ld      a, (hl)                     ; count LSB
                inc     hl                          ; (skip high byte — known 0)
                inc     hl                          ; HL = &entries[0]

                or      a
                jr      z, lff_miss
                ld      b, a                        ; B = entries remaining

lff_loop:
                push    hl                          ; save base of this entry
                ld      a, (hl)                     ; A = entry digit
                ld      c, a
                ld      a, (local_saved_digit)
                cp      c
                jr      nz, lff_advance             ; digit mismatch — skip
                inc     hl                          ; HL → entry's pc field
                call    cmp_pc_at_hl_vs_ref         ; preserves HL, BC
                jr      c, lff_advance              ; cand < ref → skip
                or      a
                jr      nz, lff_hit                 ; cand > ref → HIT
                ; cand == ref → skip (need strictly greater)
lff_advance:
                pop     hl
                ld      a, LOCAL_ENTRY_SIZE
                add     a, l
                ld      l, a
                jr      nc, lff_no_carry
                inc     h
lff_no_carry:
                djnz    lff_loop

lff_miss:
                scf
                ret

lff_hit:
                ; HL → pc field of matching entry.  Copy 4 B to local_label_pc_buf.
                ld      de, local_label_pc_buf
                ld      bc, 4
                ldir
                pop     bc                          ; discard saved entry base
                or      a                           ; CF = 0
                ret
```

- [ ] **Step 5: Replace `local_find_backward`**

```
local_find_backward:
                ld      (local_saved_digit), a

                or      a
                jp      z, fail
                cp      LOCAL_MAX_DIGIT + 1
                jp      nc, fail

                ld      hl, LOCAL_LABEL_TABLE
                ld      a, (hl)                     ; count LSB
                inc     hl
                inc     hl                          ; HL = &entries[0]

                or      a
                jp      z, lfb_miss
                ld      b, a

                ld      de, 0
                ld      (local_pending_match), de   ; no candidate yet

lfb_loop:
                push    hl                          ; save entry base
                ld      a, (hl)
                ld      c, a
                ld      a, (local_saved_digit)
                cp      c
                jr      nz, lfb_advance             ; digit mismatch — skip
                inc     hl                          ; HL → pc field
                call    cmp_pc_at_hl_vs_ref         ; preserves HL, BC
                jr      c, lfb_remember             ; cand < ref → record + keep scanning
                or      a
                jr      nz, lfb_advance             ; cand > ref → not eligible; skip
                ; cand == ref → exact match; record and STOP (no later entry can beat ==)
                ld      (local_pending_match), hl
                pop     bc                          ; discard saved entry base
                jr      lfb_done

lfb_remember:
                ld      (local_pending_match), hl   ; HL → pc field
lfb_advance:
                pop     hl
                ld      a, LOCAL_ENTRY_SIZE
                add     a, l
                ld      l, a
                jr      nc, lfb_no_carry
                inc     h
lfb_no_carry:
                djnz    lfb_loop

lfb_done:
                ld      hl, (local_pending_match)
                ld      a, h
                or      l
                jr      z, lfb_miss
                ld      de, local_label_pc_buf
                ld      bc, 4
                ldir
                or      a
                ret

lfb_miss:
                scf
                ret
```

- [ ] **Step 6: Drop `local_get_list_base` and `local_list_bases`**

Both are now unreachable. Delete them along with the `local_pending_base` storage word (also unreachable — no per-digit base needs saving in the new design).

- [ ] **Step 7: Add new scratch storage at the bottom of the file**

```
; local_saved_digit — caller's A across append / find_forward / find_backward.
local_saved_digit:      defb    0
```

Keep:

```
local_label_pc_buf:     defb    0, 0, 0, 0
local_pending_match:    defw    0
```

Remove `local_pending_base` and `local_list_bases`.

- [ ] **Step 8: Build the assembler and verify it links**

Run inside the dev container (per `docs/notes/m5-status.md` §Hand-off recipe), or natively if the toolchain is local:

```
docker run --rm -v "$(pwd):/work" -w /work \
  ghcr.io/petemoore/sam-aarch64-dev:latest \
  bash -c 'make m3-asm m3-asm-prod'
```

Expected: both targets build. Record the new production-variant size and check headroom; budget should improve slightly (~60–80 B saved by removing the 18-byte pointer table + the per-digit indexing code).

- [ ] **Step 9: Commit**

```
g add src/local_labels.asm
g commit -m "local_labels: shared (digit,pc) list, digits 1..99"
```

Single coherent commit for the storage rewrite. Test updates land in the next task.

---

### Task 2: Update self-tests for multi-digit coverage

**Files:**
- Modify: `src/test_local_labels.asm`

The existing tests cover digit-1 list operations and digit-3-isolation. Extend to cover digits `10` and `99`, and cross-isolation between a single-digit and a two-digit digit.

All inline-literal helpers in `test_local_labels.asm` use **`defb b0, b1, b2, b3`** (4 bytes, LE). Both `set_local_pc_buf_imm` and `assert_local_pc_buf_eq_imm` consume exactly 4 inline bytes after the `call`. Match the existing style.

- [ ] **Step 1: Add multi-digit append cases**

After the existing digit-1 build (which appends PCs 4, 12, 24, 100), append to digit `10` at PC=50 and PC=200, then digit `99` at PC=1000:

```
; ---- Digit 10 list construction ----
                call    set_local_pc_buf_imm
                defb    &32, &00, &00, &00          ; pc=50
                ld      a, 10
                call    local_def_append

                call    set_local_pc_buf_imm
                defb    &C8, &00, &00, &00          ; pc=200
                ld      a, 10
                call    local_def_append

; ---- Digit 99 boundary ----
                call    set_local_pc_buf_imm
                defb    &E8, &03, &00, &00          ; pc=1000
                ld      a, 99
                call    local_def_append
```

- [ ] **Step 2: Assert digit-10 forward / backward lookups**

```
; ---- Digit 10 forward: ref_pc=0 → 50 ----
                call    set_local_pc_buf_imm
                defb    &00, &00, &00, &00
                ld      a, 10
                call    local_find_forward
                jp      c, fail
                call    assert_local_pc_buf_eq_imm
                defb    &32, &00, &00, &00          ; 50

; ---- Digit 10 forward: ref_pc=50 → 200 (strictly greater) ----
                call    set_local_pc_buf_imm
                defb    &32, &00, &00, &00          ; 50
                ld      a, 10
                call    local_find_forward
                jp      c, fail
                call    assert_local_pc_buf_eq_imm
                defb    &C8, &00, &00, &00          ; 200

; ---- Digit 10 backward: ref_pc=100 → 50 ----
                call    set_local_pc_buf_imm
                defb    &64, &00, &00, &00          ; 100
                ld      a, 10
                call    local_find_backward
                jp      c, fail
                call    assert_local_pc_buf_eq_imm
                defb    &32, &00, &00, &00          ; 50
```

- [ ] **Step 3: Assert digit-1 list is unperturbed by digit-10 / digit-99 appends**

Re-run one digit-1 lookup from the existing suite after the new appends:

```
; ---- Cross-digit isolation: digit-1 forward ref_pc=50 still → 100 ----
                call    set_local_pc_buf_imm
                defb    &32, &00, &00, &00          ; 50
                ld      a, 1
                call    local_find_forward
                jp      c, fail
                call    assert_local_pc_buf_eq_imm
                defb    &64, &00, &00, &00          ; 100
```

- [ ] **Step 4: Assert digit-99 forward → 1000 (verifies boundary digit works)**

```
                call    set_local_pc_buf_imm
                defb    &00, &00, &00, &00          ; ref=0
                ld      a, 99
                call    local_find_forward
                jp      c, fail
                call    assert_local_pc_buf_eq_imm
                defb    &E8, &03, &00, &00          ; 1000
```

- [ ] **Step 5: Run the boot-time tests**

```
docker run --rm -v "$(pwd):/work" -w /work \
  -e SDL_VIDEODRIVER=x11 -e SDL_AUDIODRIVER=dummy \
  -e AARCH64_AS=aarch64-linux-gnu-as -e AARCH64_LD=aarch64-linux-gnu-ld \
  -e AARCH64_OBJCOPY=aarch64-linux-gnu-objcopy \
  ghcr.io/petemoore/sam-aarch64-dev:latest \
  bash -c '
    Xvfb :99 -screen 0 1280x1024x24 >/dev/null 2>&1 &
    sleep 1; export DISPLAY=:99
    rm -rf build/
    make ci-m3 ci-m4 ci-m5
  '
```

Expected: `9/9`, `4/4`, `19/19`. The boot-time self-tests run during ci-m3's first fixture and must pass.

- [ ] **Step 6: Commit**

```
g add src/test_local_labels.asm
g commit -m "test_local_labels: cover digits 10 / 99 + cross-isolation"
```

---

### Task 3: Promote the `inst_ldr_litpool_local.s` fixture

**Files:**
- Create: `tests/m5/sources/inst_ldr_litpool_local.s`

- [ ] **Step 1: Copy the fixture from `tests/m1/sources/`**

```
cp tests/m1/sources/inst_ldr_litpool_local.s tests/m5/sources/
```

The current content:

```
.text
  ldr x2, =10f
  ldr x3, =1f
  ldr x4, =10f
1:
  .word 0xaabb
10:
  .word 0xccdd
```

This exercises BOTH digit `10` (forward) and digit `1` (forward), and verifies that the literal pool deduplicates the two `=10f` references.

- [ ] **Step 2: Run the M5 round-trip locally**

```
docker run --rm -v "$(pwd):/work" -w /work \
  -e SDL_VIDEODRIVER=x11 -e SDL_AUDIODRIVER=dummy \
  -e AARCH64_AS=aarch64-linux-gnu-as -e AARCH64_LD=aarch64-linux-gnu-ld \
  -e AARCH64_OBJCOPY=aarch64-linux-gnu-objcopy \
  ghcr.io/petemoore/sam-aarch64-dev:latest \
  bash -c '
    Xvfb :99 -screen 0 1280x1024x24 >/dev/null 2>&1 &
    sleep 1; export DISPLAY=:99
    make ci-m5 ci-m5-prod
  '
```

Expected: `20/20 M5 fixtures matched` for both jobs.

- [ ] **Step 3: Commit**

```
g add tests/m5/sources/inst_ldr_litpool_local.s
g commit -m "tests/m5: promote inst_ldr_litpool_local.s fixture"
```

---

### Task 4: Update `assembler.asm` memory-map comment

**Files:**
- Modify: `src/assembler.asm` (line ~58)

- [ ] **Step 1: Update the LOCAL_LABEL_TABLE reservation line**

Current (line ~61):

```
;   &CD60-&D0D1  LOCAL_LABEL_TABLE   (9 × 98 bytes = 882 bytes)
```

Replace with:

```
;   &CD60-&D147  LOCAL_LABEL_TABLE   (count + 200 × 5 bytes = 1002 bytes)
```

(End address: `&CD60 + 1001 = &D149` — pick the address that matches the rounded-up byte count actually used. Confirm by inspecting the `local_labels.asm` source.)

- [ ] **Step 2: Commit (bundled with the next task's spec update)**

Do not commit yet — fold into Task 5's commit.

---

### Task 5: Update the M4 design spec

**Files:**
- Modify: `docs/specs/2026-05-24-m4-symbols-multipass-design.md` §2.3

- [ ] **Step 1: Update the §2.3 digit range**

Find the section that describes local labels (search for "digit"). Update from "single decimal digit 1..9" to "decimal digit 1..99". Add a sub-paragraph:

> v2 (M6-era, multi-digit) — the on-SAM table was redesigned from a fixed
> 9-array layout to a single shared sorted `(digit, pc)` list. ABI
> unchanged; storage halved per-digit (entries are now 5 bytes rather
> than per-digit fixed slots). See `docs/plans/2026-05-27-multi-digit-local-labels.md`.

- [ ] **Step 2: Commit Tasks 4 + 5 together**

```
g add src/assembler.asm docs/specs/2026-05-24-m4-symbols-multipass-design.md
g commit -m "docs: multi-digit local labels — memory map + spec §2.3"
```

---

### Task 6: Update milestone status + roadmap

**Files:**
- Modify: `docs/notes/m5-status.md`
- Modify: `docs/ROADMAP.md`

- [ ] **Step 1: Promote the fixture in `m5-status.md`**

In the "Test status" table, bump the M5 line from `19/19` to `20/20` (both `ci-m5` and `ci-m5-prod`).

In the fixture corpus table, add a row for `inst_ldr_litpool_local.s` describing "OpLitPool + two-digit local labels (`10f`)".

In the "Deferred (covered-by-implication or non-trivial)" subsection, REMOVE the `inst_ldr_litpool_local.s` row (it's no longer deferred).

In the "What's NOT in M5 scope" section, REMOVE the "Multi-digit local labels" bullet.

- [ ] **Step 2: Tick off the ROADMAP deferred-work item**

In `docs/ROADMAP.md`'s "Deferred-work review checklist", find the multi-digit-local-labels item (it's not currently in the explicit checklist — there's a reference to it from `m5-status.md`). Add a new ticked entry:

```
- [x] ~~M6 prerequisite — multi-digit local labels~~ — **DONE 2026-05-27 in PR #XX**. `LOCAL_LABEL_TABLE` redesigned to a shared sorted `(digit, pc)` list supporting digits 1..99; required by `inst_ldr_litpool_local.s` and by spectrum4 release.s which uses locals up to `15`. See `docs/plans/2026-05-27-multi-digit-local-labels.md` and `docs/notes/m5-status.md`.
```

(Replace `#XX` with the actual PR number once the PR is opened.)

- [ ] **Step 3: Run the full CI matrix one more time as a final gate**

```
docker run --rm -v "$(pwd):/work" -w /work \
  -e SDL_VIDEODRIVER=x11 -e SDL_AUDIODRIVER=dummy \
  -e AARCH64_AS=aarch64-linux-gnu-as -e AARCH64_LD=aarch64-linux-gnu-ld \
  -e AARCH64_OBJCOPY=aarch64-linux-gnu-objcopy \
  ghcr.io/petemoore/sam-aarch64-dev:latest \
  bash -c '
    Xvfb :99 -screen 0 1280x1024x24 >/dev/null 2>&1 &
    sleep 1; export DISPLAY=:99
    rm -rf build/
    make ci-m3 ci-m3-prod ci-m4 ci-m4-prod ci-m5 ci-m5-prod
  '
```

Expected: `9/9 + 9/9 + 4/4 + 4/4 + 20/20 + 20/20`.

- [ ] **Step 4: Commit**

```
g add docs/notes/m5-status.md docs/ROADMAP.md
g commit -m "docs: tick off multi-digit local labels; bump M5 fixtures to 20"
```

---

### Task 7: Open the PR

- [ ] **Step 1: Push the worktree branch and open a draft PR**

```
g push -u origin <worktree-branch>
gh pr create --draft --title "multi-digit local labels (1..99) + promote inst_ldr_litpool_local fixture" \
  --body "$(cat <<'EOF'
Closes the M5 deferred fixture `inst_ldr_litpool_local.s` and clears one M6 prerequisite ahead of the paged-source work (spectrum4 release.s uses locals up to `15:`).

The on-SAM local-label table was a 9-element per-digit array. This PR replaces it with a single shared sorted `(digit, pc)` list, 5 bytes per entry, capped at 200 entries — fits the existing 1 KB slot at &CD60. The ABI is unchanged. The Go-side tooling (text2bin lexer, refenc, bin2text) already handled 1..99, so the diff is Z80-only plus the fixture promotion + docs.

20/20 M5 fixtures byte-match GNU; M3 + M4 regression checks unchanged. Self-tests cover digits 10 and 99 plus cross-digit isolation.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

- [ ] **Step 2: Update the ROADMAP PR-number placeholder**

Once the PR number is known, edit `docs/ROADMAP.md` to replace `#XX` with the real number, amend that commit (or add a fixup commit), and force-push if needed.

- [ ] **Step 3: Watch CI to green**

`gh pr checks --watch` until both `m5` and `m5-prod` jobs are green. Fix any CI failures autonomously — small corrections amend/squash into the relevant task commit and force-push.

- [ ] **Step 4: Mark ready / merge**

Per project-local PR workflow (`CLAUDE.md`): if CI is green AND the diff is mechanical (a Pete-glance is not load-bearing), merge directly via `gh pr merge --merge --delete-branch`. Otherwise leave open for review. (Corrected 2026-05-28: this plan originally said `--squash`; sam-aarch64 now uses merge commits — see the `feedback_merge_commits` memory entry.)
