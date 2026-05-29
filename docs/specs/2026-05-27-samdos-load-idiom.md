# SAMDOS file-load idiom: research findings

**Status**: design note. No code yet. Captures research output from 2026-05-27 surveying COMET's source, the SAM ROM disassembly, and the wider sam-corpus.

## Background

The M3 loader at `src/loader.asm` loads `enctab.enc` via `HGTHD` (129) + `HLOAD` (130). The Tech Manual specifies that HLOAD requires `HL ∈ &8000-&BFFF` (section C). The earlier loader violated this by loading to `&C100` (section D), getting away with it only because hand-crafted disk layout dodged the SAMDOS auto-wrap-fix in `ctas` (`samdos/src/c.s:347-369`).

PR #17 fixed the immediate problem by moving `ENCTAB_BUF` to `&9000` (option (a) — small enctab fits comfortably in section C alongside the assembler code). This document captures the *future-proof* pattern for when loads exceed section C's capacity (paged source, symbol table, etc.).

## The COMET pattern (option (b))

COMET — the SAM Coupé–era assembler that's the closest analogue to what we're building — solves the "load into arbitrary memory" problem with a small trampoline in section A. Source: `reference/comet-decoded/comet.asm:1265-1284`.

The trampoline (16 bytes):

```asm
loaddata:
    IN   A, (251)        ; read HMPR
    PUSH AF              ; save it
    LD   A, B            ; B = destination page
    OUT  (251), A        ; HMPR = destination page
    RST  8
    DEFB 130             ; HLOAD
    EX   AF, AF'
    POP  AF
    OUT  (251), A        ; restore HMPR
    EX   AF, AF'
    DI
    RET
```

Why it works:

- The trampoline lives in section A (COMET copies it to `&4F00`), which is mapped via LMPR — independent of HMPR. Changing HMPR while running from section A doesn't yank the instruction stream out from under us.
- HL is `&8000` (section C, satisfies the Tech Manual constraint).
- After HMPR change, section C now physically maps to the page where we want the data to land.
- HLOAD writes through `&8000+` into the destination page.
- HMPR is restored on return so the rest of the program sees its original section-C mapping.

COMET's call site (`comet.asm:1191-1200`):

```asm
CALL prepare              ; copy loaddata to &4F00 + patch dier
IN   A, (252)             ; read LMPR
AND  31                   ; B = page number where COMET itself lives
LD   B, A
LD   HL, 32768            ; HL = &8000
LD   A, (linebuff+34)     ; pages from HGTHD-loaded DIFA
LD   C, A
LD   DE, (linebuff+35)    ; length-mod-16K
RES  7, D
CALL 20224                ; the copied trampoline at &4F00
```

COMET deliberately pages section C to overlap its own LMPR-page. The source-file ends up "above" COMET's code in physical RAM. HMPR is restored after HLOAD returns.

## The BASIC `LOAD ... CODE addr` pattern (the canonical one)

The same idiom, baked into ROM. From `docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt:22407-22512`, the chain `CDSCVE → STSPEC2/3 → LDVDBLK → DOSLD` does:

1. `LD A, (LDCO) / ADD A, C` — derive absolute destination page from caller's address.
2. `CALL TSURPG` (ROM `&3FDF`) — `TSURPG` is "START SWITCHED IN AT HL" — sets HMPR to the bottom 5 bits of A, preserves the top 3.
3. `CALL RDLLEN` — fills C / DE with pages-count and length-mod-16K.
4. `LD IX, HDR` — set UIFA pointer.
5. `RST 08H / DB 0x82` — HLOAD.

Every disk that uses `LOAD ... CODE someaddress` exercises this. Sampled corpus disks:

- **SC Monitor Pro 1.2**: `auto` BASIC line 10 — `INPUT "Ram page start..."; LET m=(r+1)*16384; LOAD "moncode" CODE m` loads a 32 KB file into a user-chosen high-RAM page. The ROM decomposes `m → page/offset` and HMPRs before HLOAD.
- **Secretary Word Processor**: `LOAD "E_Manual" CODE` with a fixed start=147456 (physical page 9 offset 0). Same pattern.

Conclusion: the trampoline-around-HLOAD pattern is the established SAM idiom for "load into arbitrary RAM". Every real SAM program uses it (directly via the ROM, or via its own copy of the trampoline).

## Hooks we have NOT been using (none help)

Full survey of `samdos/src/b.s:497-538`:

| Hook | What it does | Helps us? |
|------|--------------|-----------|
| 130 HLOAD | `dschd → ldblk`. HL must be `&8000-&BFFF`. | This is what we use. |
| 131 HVERY | Same setup as HLOAD but compares. Auto-HMPR-increment at `&C000` boundary (`h.s:115-129`). | No — verify only. |
| 149 HWSAD | Write 512-byte sector from arbitrary HL via `cals` page-translation. | No — raw sector. |
| 150 HSVBK | `jp svblk`. Save block. | No — save side. |
| 160 HRSAD | Read 512-byte sector at (track, sector) into arbitrary HL via `cals`. | No — raw sector. |
| 161 HLDBK | `jp ldblk` directly. Skips `dschd` so re-uses prior HLOAD/HGTHD state. Same HL constraint. | No — same constraint. |

`cals` (`samdos/src/h.s:308-321`) is the only routine that translates an arbitrary HL to (HMPR-page, `&8000`-window-offset). It's used ONLY by HRSAD and HWSAD, both of which work in raw 512-byte sectors at (track, sector) coordinates. **No file-aware hook uses `cals`.**

The Tech Manual list (`docs/sam/sam-coupe_tech-man_v3-0.txt:4515-4536`) is complete — no undocumented file-IO hooks.

## Recommendation

**Adopt the COMET trampoline pattern when load-into-arbitrary-RAM becomes load-bearing.** Implementation outline:

1. Static 16-byte trampoline embedded in our binary (or copied to section A at startup, matching COMET).
2. Caller sets:
   - `HL = &8000` (or `&8000 + offset` if loading mid-section-C)
   - `B = target physical page` (HMPR value to set)
   - `IX = UIFA at &4B00`
   - `C = pages count` (from HGTHD's DIFA at +34)
   - `DE = length mod 16K` (from DIFA at +35, with bit 7 of D cleared)
3. `CALL trampoline`. Restores HMPR on return.
4. Optionally patch SAMDOS error vector `&5BC0` to point into the trampoline so HMPR is restored even on HLOAD error (COMET does this).

This handles loads of any size into any 16K-aligned page. For loads > 16K, HLOAD's internal multi-page path (`samdos/src/c.s:682-699`, in `ldblk`/`ccnt`) auto-increments through the source file; combined with the trampoline's HMPR control, this can populate arbitrary contiguous regions.

## When we'll need this

Not now. PR #17's ENCTAB_BUF-in-section-C handles the current 1 KB enctab case fine. The trampoline pattern is the right answer when:

- **The paged source format** for the on-SAM editor (could exceed 16 KB for debug/tests builds).
- **The symbol table** during M4 multi-pass assembly (~50 KB).
- **Output buffer** if we ever assemble large enough to need streaming-with-buffer (unlikely for release; possible for debug/tests).

## Pre-built trampoline reference

For future use, here's a verbatim Z80 trampoline that should drop into our `loader.asm` once we need it:

```asm
; trampoline_hload — HMPR-aware HLOAD wrapper.
; Live in section A or B (NOT section C/D — HMPR changes during the body
; would yank the instruction stream out from under us).
;
; Input:
;   HL = &8000..&BFFF (HLOAD's section-C window)
;   B  = target physical page (5-bit, OR'd with top 3 bits of current HMPR)
;   IX = UIFA pointer
;   C  = pages count (from DIFA+34)
;   DE = length mod 16K (from DIFA+35, with bit 7 of D cleared)
;
; Output: HMPR restored to its entry value; HL/BC/DE/AF clobbered.
trampoline_hload:
                in      a, (251)       ; A = current HMPR
                push    af             ; save
                ld      a, b           ; A = target page
                out     (251), a       ; HMPR = target page
                rst     8
                defb    130            ; HLOAD
                ex      af, af'        ; save HLOAD's AF (CY = error flag)
                pop     af
                out     (251), a       ; restore HMPR
                ex      af, af'        ; restore HLOAD's AF
                di
                ret
```

(The trailing `DI` mirrors COMET's; SAMDOS's RST 8 dispatch does `EI` so DI here restores the no-interrupts invariant typical of batch programs.)

## Related notes

- The audit at `docs/notes/sam-stub-audit.md` covers SAMDOS hook semantics but didn't survey real-world load patterns. This note fills that gap.
- PR #17 (currently draft) implements option (a) for the immediate ENCTAB case. Future PRs will introduce the trampoline as M3/M4 needs grow.
