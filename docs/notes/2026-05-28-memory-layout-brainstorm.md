# End-state memory-layout brainstorm — assembler + editor + TFTP + extras

**Status:** read-only design brainstorm. No code, no commits. Captured 2026-05-28 at Pete's request. Horizon: Phase 3 (TFTP) + Phase 2 (editor). Phase 4+ deliberately not addressed.

## 1. Cost model

Z80 sees 64 KB in four 16 KB sections A/B/C/D, backed by 32 physical pages. Paging is one `out (port), a` per swap. Two cost vectors:

1. **Bracket T-states** — the in/out/work/restore bracket dominates inner loops. High-zone OUT emit measures ~60 T-states/byte (`m6-status.md:39-43`), almost all of which is bracket overhead around a 4-T-state store.
2. **Code budget** — paging while running code needs a section-stable trampoline (`trampoline.asm:270,:297-305`). Each new bracketed call site eats into section C's code area. Production code today: 12 204 B used inside `&8000-&AFFF` (84 B headroom inside that block). **Effective ceiling is `&C000` = 16 384 B**, because `&B000-&BFFF` is reserved as additional code headroom (per `src/m3/assembler.asm:17`) — so ~4 180 B is actually available for code expansion. Editor + assembler need a structural plan if they're both to live in section C; the "84 B" framing made that look impossible when in fact it's tight-but-tractable. Bracket-site cost still applies the same way regardless of which kilobyte the call site lives in.

"Minimise swaps" for inner loops; "minimise bracket call sites" for code budget.

## 2. Component inventory

Estimates unless cited.

| # | Component | Size | Source / basis | Access pattern | In scope today? |
|---|---|---|---|---|---|
| 1 | Z80 assembler core | **12 204 B** | `m6_strand_a_complete.md:27` (measured) | Hot during pass 1/2 | yes |
| 2 | ENCTAB | **3 399 B** | `build/enctab.enc` measured | Per-instruction read | yes |
| 3 | OUT buffer | up to **32 KB** | `assembler.asm:36-41`, `m6-status.md:104-116` | Per-emit pass 2; HSAVE at end | yes |
| 4 | IN buffer (.tbn) | up to **96 KB** | `trampoline.asm:265`, `m6_strand_a_complete.md:26` | Per-record both passes | yes |
| 5 | Symbol/litpool/local-label scratch | **~7 KB** | `assembler.asm:24-31` | Per-record during assembly | yes |
| 6 | SAMDOS resident page | 1 page = 16 KB | `sam-paging.md:50-53, 572-585` | Per file I/O via PTDOS | yes |
| 7 | Z80 disassembler | **~2-3 KB** rough | Inverse-encoder driven by ENCTAB; ~ as complex as `form_lookup.asm` (367 lines) + small operand decoders | Per-instruction (disasm / F1 / sim) | M6 PR 4+ |
| 8 | Editor core | **~6-10 KB** rough | COMET (`comet.asm` 4903 lines, mixed) suggests editor alone ~4-6 KB; Tasword 2 ships ~14 KB and does more than ours needs | Per-keystroke | Phase 2 |
| 9 | F1/F2 explanation prose | **~10-25 KB** | ROADMAP §Editor vision: ~30-50 MRA regs; `SCTLR_EL1` alone = ~50 fields × ~40 B = ~2 KB. Instruction prose mostly schema | Panel-open | Phase 2 |
| 10 | Sysreg DB | **< 1 KB** | `sysregs.go` 39 entries × ~7 B = ~400 B | Per `msr`/`mrs` / per cursor | partially |
| 11 | Register simulator | **~2-4 KB** | aarch64 ALU subset (~76 mnemonics); 64-bit Z80 ALU < 100 B per op | F-key step | Phase 2 |
| 12 | Rewrite hints | **< 1 KB** | `2026-05-27-disassembly-canonicalisation-survey.md`: 0.37 % jarring — handful of patterns | Cursor-move check | Phase 2 |
| 13 | Cross-ref index | **~2-4 KB** | Parent pointers on SYMTAB; release.tbn peaks at 474 symbols (`2026-05-28-z80-table-sizing-census.md:25-26`) | Navigate keystroke | Phase 2 |
| 14 | Retro UI | **~2-4 KB code + 2-8 KB data** | SAA driver ~1-2 KB; mode-3 6×8 font = 1.5 KB; pattern data scales with track | 50 Hz IRQ | Phase 2 (optional) |
| 15 | Screen memory | 16-24 KB (mode-dependent) | `sam-paging.md:154-170` | Hardware DMA — CPU pays nothing | always |
| 16 | TFTP shipper | **~3-4 KB** | `~/git/trinload/trinload.bin` = 3 334 B measured; our one-way path likely smaller | One-shot at ship | Phase 3 |
| 17 | Trampoline + paging glue | **~500 B** | `trampoline.asm` (487 lines, mostly comments; compiled body ~30 B + helpers) | Per SAMDOS hook | yes |

Screen memory consumes physical-page slots but no CPU paging budget (VMPR is independent). Modes 3/4 wrap into the next physical page (`sam-paging.md:166-170`).

## 3. Proposed page-axis assignment

512 KB = 32 pages. Pages 0-3 = BASIC boot (`sam-paging.md:458-486`); one page = SAMDOS (dynamic via DOSFLG); 2 pages = screen in mode 3. The ~25 remaining are ours.

| Page(s) | Role | Mapped via | When |
|---|---|---|---|
| 0 | ROM0 / BASIC sys | A under LMPR_DEFAULT | passive |
| 1 | BASIC sys + permanent trampoline copy at `&7E00` | B under LMPR_DEFAULT | always — trampoline is LMPR-stable |
| 2 | **Editor document — low 16 KB** | C under LMPR_DEFAULT | always during editing |
| 3 | **Editor document — high 16 KB** | D under LMPR_DEFAULT | ditto |
| 4 | ENCTAB / form table (unchanged) | A under LMPR_ENCTAB | per-instruction during assembly |
| 5..6 | OUT buffer (unchanged) | B (low zone) / B-via-LMPR=`&25` (high zone) / C via HMPR at HSAVE | per-emit pass 2 |
| 7..12 | IN buffer (unchanged) | A under LMPR_IN_BASE + N | per-record during both passes |
| 13 | **Disassembler aux + sysreg DB + rewrite-hint table** (co-resident ≤ 3 KB) | A under LMPR_DISASM_AUX | per F1 / "R" / cursor-on-sysreg / disassembly |
| 14..15 | **Explanation prose** (instruction text + sysreg field prose) | C/D under HMPR_DISASM_TEXT | only while F1/F2 panel is open |
| 16..17 | **Editor scratch / undo history / line index** | C/D under HMPR_EDITOR_SCRATCH | per heavy operation (undo, full-buffer scan) |
| 18..21 | **Register simulator state + replay log** | C/D under HMPR_SIM | per simulator step |
| 22..27 | **Future: paged document, when source > 32 KB** | C/D under HMPR_DOC_PAGED | per cursor-move crossing a 16 KB boundary |
| 28 | **TFTP buffers** (UDP MTU staging + Trinity SPI scratch) | C under HMPR_TFTP | one-shot at ship |
| 29 | **Music pattern data + SAA driver scratch** | brief HMPR window in IRQ | 50 Hz |
| 30..31 | **Screen** (mode 3) | not in CPU's section map | hardware-side per-frame |
| (DOSFLG) | SAMDOS — ROM picks the page | B via PTDOS hook (`sam-paging.md:602-632`) | per file I/O |

Co-resident families: **F1 explain** = page 13 (disasm aux) + pages 14-15 (prose). **F2 sysreg** = same two brackets. **Simulator step** = page 4 (ENCTAB) + section-C code + section-D scratch.

**Crucial shape**: the document sits at section C/D under LMPR_DEFAULT, so editor keystrokes pay **zero** swaps. Today's IN buffer is right for the assembler's stream model and wrong for the editor's random-access model.

## 4. Latency-flow swap counts

"Bracket" = swap-in + swap-out = 2 `out (port), a`.

| Flow | Swaps | Why |
|---|---|---|
| Cursor up/down a line | **0** | Document in section C/D under LMPR_DEFAULT |
| Type a character | **0** | Same; status-line redraw is screen-side (VMPR — no CPU paging) |
| Page up/down | **0** for ≤ 32 KB docs; **2** if crossing into paged-out chunks | Hot path is no-swap |
| F1 explain instruction | **4** (2 brackets) | (a) LMPR_DISASM_AUX → page 13 for decode; (b) HMPR_DISASM_TEXT → pages 14-15 for prose. LMPR/HMPR independent (`sam-paging.md:24-30`) so they nest |
| F2 sysreg fields | **2** (or 1 swap if F1 already open) | Just HMPR_DISASM_TEXT; sysreg DB co-located on page 13 |
| Simulator step | **2** | LMPR_ENCTAB for slot decode; ALU emulator in section-C code; regs in section-D scratch |
| Rewrite hint surface / accept | **0** passive / **2** to accept | Passive check is a 4-byte hash against an in-section-C summary; accepting needs page 13 |
| Save → reassemble | per-record bracket × N + per-emit bracket for high-zone bytes | Existing M6 cost (~60 T-states/byte high-zone, `m6-status.md:39-43`). Layout doesn't worsen it |
| F-key toggle on cached panel | **0** | Editor keeps the most-recent HMPR target sticky |
| Ship to Pi | **~4 total** | HMPR → page 28 for UDP/TFTP staging; OUT read via existing HSAVE-style HMPR auto-page |
| Music IRQ tick (50 Hz) | **2 per tick** | HMPR → page 29, restored before RETI. ~30 T × 50 Hz = 1500 T-states/s = **< 0.05 % CPU** |

Worst-case combo: F1 on `msr SCTLR_EL1, x0`, then three simulator steps = 4 + 2×3 = **10 swaps** ≈ 250 T ≈ 60 μs at 4 MHz. Below perception.

Best case: scroll a 1000-line doc, 10 lines/keystroke = **zero swaps**.

## 5. Comparison vs. today

**Same**: ENCTAB at page 4; OUT at 5-6 (two-zone bracket); IN at 7-12 (per-record bracket); trampoline at `&7E00`; stack at top of section D (post-PR #30 / #42-revert open thread, `m6_strand_a_complete.md:42`).

**Changed (proposal, not built)**:

- Document reserved at section C/D under LMPR_DEFAULT. Today nothing lives there during assembly.
- Pages 13-29 get explicit roles. Today they're "00H unused" per the ALLOCT survey (`trampoline.asm:189-208`).
- New constants alongside `LMPR_ENCTAB` / `LMPR_OUT_HIGH` / `LMPR_IN_BASE`: `LMPR_DISASM_AUX = &2D` (page 13), `HMPR_DISASM_TEXT = &0E` (pages 14+15), etc. Same naming pattern.
- The 4 KB freed at `&B000-&BFFF` by M6 PRs 1+2 (`assembler.asm:18-22`) gets a designated owner — probably compact-`.tbn` dictionary + decode scratch.

Small in code (a few constants + two bracket helpers); large conceptually — every Phase 2/3 component gets its slot decided up front, not ad hoc.

## 6. Open questions / risks

1. **Editor + assembler share the section-C code window, or separate pages?** Assembler is 12 204 B; the section-C code area effectively extends to `&C000` (16 384 B — see §1) so there's ~4 180 B available before structural moves are required. The editor is roughly 6-10 KB rough estimate (§2), so co-residence is *not* trivially possible inside section C. Either an LMPR-flip between "section A = editor code" / "section A = ENCTAB" (extends trampoline pattern but complicates editor↔assembler calls) or quit-editor-to-assemble (simpler, less integrated). Separate design pass.
2. **Document: flat or paged?** Flat in section C/D for ≤ 32 KB; paged via per-record bracket (pages 22-27) once larger. Pain threshold matches expectation.
3. **Explanation prose at runtime.** Generated Mac-side like `enctab.enc`; HLOAD'd into pages 14-15 at boot via the same trampoline-HLOAD pattern. One combined "editor data" disk file.
4. **Music IRQ without paging?** Squeezing SAA driver + small pattern table into ~1 KB at `&7C00-&7DFF` (just below the trampoline) gives the IRQ zero swap cost — at the cost of ~1 KB section-B real estate that's free under LMPR_DEFAULT today.
5. **Mode-3 CLUT collision.** HMPR bits 5-6 = mode-3 CLUT high bits (`sam-paging.md:140-150`). All bracket helpers must use `TSURPG` to preserve those bits — same discipline as existing `enctab_map_in/out` — or the retro palette glitches on every panel toggle.
6. **SAMDOS in the middle of our allocation.** Lands in "the last free 16 K page" (`sam-paging.md:572-585`) — likely page 29, where we want music. Mitigation: at startup read DOSFLG (`&5BC2`) and shift high-page assignments around it. Pages 13-21 literal; 22-29 nominal.
7. **Page 13 over-subscription.** Disasm + alias + sysreg DB ~3 KB today, but compact-`.tbn` Level 3's per-project frequency dictionary (`compact-tbn-and-disassembler-design.md:84`) could exceed 10 KB. Mitigation: dictionary on its own page from the start.

## 7. Recommendation

**Short-term (next 1-2 PRs, within M6)**:

1. Re-apply the SP-fix PR #43 reverted (`m6_strand_a_complete.md:42`); unblocks the test variant.
2. Re-enable the reader-paged self-test once #1 lands (`m6-status.md:208-241`).
3. Adopt this note's page assignment as canonical — even pages 13-29 that aren't used yet. Writing it down stops casual "let's stick this on page N" decisions.

**Medium-term (M6 strand B)**:

4. Reserve page 13 for disasm aux + sysreg DB + rewrite-hint table when the disassembler lands. Build per-component bump arenas from the start (`m6_strand_a_complete.md:46` — "durable answer to the fixed-table-overflow class") — per-component, not global, so each table stays in a known page.
5. Reserve pages 14-15 for explanation prose. Don't build it until Phase 2 — the reservation prevents accidental reuse.
6. In the M6 PR 4+ design pass, decide the compact-`.tbn` dictionary's page location explicitly: co-resident with disasm aux (cheap reads, tight space) or separate page (more swaps, more room).

**Long-term (Phase 2 + Phase 3)**:

7. Editor's document at section C/D under LMPR_DEFAULT — *not* in the paged IN range. IN is right for the stream model, wrong for random-access. Safe because we exit via `di; halt` (no BASIC resume).
8. Bump-arena lands with the disasm-aux PR. Retrofitting later is more work.
9. TFTP shipper reuses HSAVE-style HMPR auto-paging on the OUT buffer. No new bracket needed.

### Headline

**Section C/D under LMPR_DEFAULT is the editor's domain; LMPR_ENCTAB is the assembler's; each Phase 2 panel gets its own LMPR_DISASM_AUX or HMPR_*_TEXT bracket.** We're not inventing new mechanics — we're extending the one we already understand from M6 PR 1 + PR 2.

## 8. Sources

- `docs/notes/sam-paging.md` — paging mechanics (LMPR/HMPR, REL PAGE FORM, BASIC pages, SAMDOS, TSURPG)
- `docs/ROADMAP.md` §Editor vision; `docs/specs/2026-05-09-vision.md`; `docs/specs/2026-05-09-phase1-assembler.md`
- `docs/specs/2026-05-27-phase3-tftp-direct-lan-design.md`; `docs/specs/2026-05-27-compact-tbn-and-disassembler-design.md`
- `docs/notes/m6-status.md`; `docs/notes/2026-05-28-z80-table-sizing-census.md`
- `src/m3/assembler.asm` (memory map block, lines 1-50); `src/m3/trampoline.asm` (page constants + ALLOCT survey)
- `tools/sam-aarch64-format/sysregs.go` (39 sysreg entries); `tools/aarch64enc/manual_forms.go`; `build/enctab.enc` (3 399 B measured)
- `~/git/trinload/trinload.bin` (3 334 B measured); `reference/comet-decoded/comet.asm` (4 903 lines, editor sizing reference)
- Memory entries: `m6_strand_a_complete.md`, `feedback_correctness_over_workarounds.md`, `feedback_sam_editor_keyboard_driven.md`, `future_roadmap.md`
