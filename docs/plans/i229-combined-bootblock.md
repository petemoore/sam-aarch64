# i229 — combined patched-bootblock image (Stage 1 of the i135c no-brick plan)

Ephemeral plan. Deleted by the PR that completes i229. Stage 1 of the staged
i135c plan (Pete, 2026-06-23): **build + emulation-verify the combined patched
bootblock**, so it can be RAM-tested (i230) and then flashed (i135c) — never
rushing boot code.

## Load-bearing constraints (found during planning)

1. **Colin's bootblock is proprietary and must NOT enter the repo.**
   `docs/notes/samboot-bootblock-analysis.md` §7 cites it by *file offset* into
   `~/sam-archive/samboot-capture/eeprom.bin`, never copying bytes in. So the
   flashable image (Colin's ~350 B + our inject) **cannot be a committed binary**.
   Stage 1 is a **splice tool** (host Go or a script) that, at build/flash time,
   reads chunk 1 from the *local* `eeprom.bin`, patches our inject in, and writes
   the combined ≤1024-byte image to a local (git-ignored) path. The repo holds
   only our inject source + the tool.

2. **`samboot_stripes` is an unimplemented stub.** `src/netboot/samboot_inject.asm`
   lines 80-87: the real MODE-2 palette+banner redraw is an empty
   `if defined(NETBOOT_HOSTTEST)==0` block (only the call-probe counter runs). So
   **do NOT replace Colin's original screen-redraw with the inject's stripes** —
   that would blank the screen on the fall-through (no-auto-boot) path. Keep
   Colin's redraw; insert only the config-decision.

## The splice (low-risk)

Colin's bootblock (loaded at `&4000`): save paging → load B-DOS (chunks 2-13 →
`&8000`) → `CALL &805F` (B-DOS init) → screen redraw → `restore:` (restore paging,
`JP` BASIC). Hook site (analysis §7.2): after `CALL &805F` and before `restore:`,
with ~683 B free at EEPROM `&00015E-&000408` (→ `&415E` when loaded at `&4000`).

Insert, at the hook, a **config-decision** that runs AFTER Colin's redraw and
BEFORE `restore:`:

```
    call  samboot_read_config   ; i176: CY+HL=record (auto-boot) or NC (no)
    jr    nc, <fall through to restore:>
    ld    a, l
    ld    (BD_BOOT_RECORD), a
    jp    bdos_boot_record       ; i122a: HRECORD + ALHK, no return on hardware
```

placed in the free space (`&415E+`), with a `CALL`/`JP` spliced at the hook.
**Reuse Colin's resident EEPROM routines** (`read_chunk`/`find_index` already in
the bootblock at the §7.1 addresses) where the config read needs them, to save
space — confirm the calling contract matches `samboot_config.asm`'s expectations,
else include the minimal reader. Budget the spliced code against the 683 free
bytes (the decision itself is tiny; `samboot_read_config` + the config-chunk read
is the bulk — verify it fits or trim).

**Exact byte-level splice is determined with the local capture disassembly
in-hand** (z80dis on `eeprom.bin` chunk 1, per §7.1) — the addresses of the
redraw, `restore:`, and the resident `read_chunk` are read from the actual bytes,
not from memory.

## Agreed scope + workflow (Pete, 2026-06-23)

The RAM test (i230) exercises the patched bootblock's **new, in-memory parts**
bundled into ONE Go-emulation test that **boots from address 0 with the real
`rom.bin`** (the `samboot_real_boot` harness — so `PALTAB` is real, NOT seeded):
1. **draw the screen** — the rainbow stripes (verbatim ROM `&ED1B` port, already
   landed) building `LINICOLS` from the real `PALTAB`, + the MGT banner (RST 16);
   the Go harness has no screen render, so assert it *wrote* `LINICOLS`/screen RAM
   and did not crash (Pete: "even if we can't see the screen, it should write to
   the screen and not crash in emulation");
2. **fetch the SAMBOOT config** from the modelled Trinity EEPROM (`samboot_read_config`);
3. **boot the configured record** (RST 8 ALHK, captured via `AttachBDOS`);
4. a **second case where no record is configured** → it falls through to the
   BASIC exit (no ALHK).

SimCoupé is NOT used here (it doesn't model the Trinity read/boot); bundling the
screen into the Go test keeps the new parts together.

**Workflow (the i228 lesson):** build → Go-emulation test (above) → **trinload RAM
test on Pete's real SAM** (push the bundled payload; confirm screen + config-boot
+ no-boot) → **only then merge the PR** → then, as a separate step, the real
EEPROM flash (i135c). Do the hardware test BEFORE the PR lands, so a hardware
failure is fixed in the same PR rather than a follow-up.

## Boot-screen reproduction recipe (from the ROM, 2026-06-23 research)

The rainbow stripes are rendered LIVE by the ROM line-interrupt ISRs, not by the
`&ED1B` data write. Reproducing the MGT boot screen (in the bootblock AND a
viewable trinload demo) requires, in order — all ROM addresses cited in the
research / `docs/sam/...annotated-disassembly.txt`:

1. **Interrupt state live:** `IM 1`, `I=0`, `(ANYIV &5B70)=&0049` (all already set
   post-boot — do NOT clobber). The fragment's omission was step (e).
2. **CLUT/border baseline = index 0:** clear paper to CLUT index 0; set border to
   CLUT index 0 (`SETBORD &F13A` in the ROM1-reachable demo; inline `OUT (&FE),A`
   with bits 0-2,5=0 + bit3 in the bootblock where ROM1 is paged out). `LINEINT`
   writes ONLY CLUT reg 0 (`&F8`) per line; border = a CLUT-index lookup, so both
   track CLUT-0. Do NOT write `&FE` per scan line.
3. **Build LINICOLS:** the existing verbatim `&ED1B` port (`samboot_stripes`) —
   `PALTAB+1`→`LINICOLS`, 4-byte entries, step scan +11 to 166, `&FF` terminator.
4. **Print position:** `CALL CLSLOWER &06B5` (ROM0, always callable) — selects
   channel K + sets `SPOSNL` to the lower window. THIS fixes the top-left banner.
5. **Print the banner:** faithfully via `XOR A / CALL UTMSG &3DB0` + the é/space/
   size/"K" tail (`&0F7F` 3892-3915), reusing the ROM text at `&F5DD` — preferred
   over our private string.
6. **Arm + render:** `EI` (so FRAMINT arms STATPORT from LINICOLS[0] + LINEINT
   paints). THE missing step. Auto-boot path skips any wait; the no-boot/demo path
   does `WTFK`: `CALL READKEY &1CB1 / JR Z` (or a timed wait, per Pete).
7. **Teardown on keypress (mirror the ROM):** `LD A,&FF / LD (LINICOLS),A`
   (disarms the line-int next frame → stripes vanish) + `CALL CLSLOWER`.
8. **Return cleanly — NEVER `di;halt`:** demo → `EI` + paging-as-trinload-expects
   + `RET` (trinload's `try_exec` pushed `start`, so RET restarts trinload). The
   prior hang was the `di;halt` path / wrong paging, NOT the screen writes
   (trinload re-inits on restart). Bootblock → restore LMPR/HMPR + `JP ERRHAND2
   &102F` (the stock `restore:` exit).

Open hardware-confirm items (research §7): that the trinload-pushed demo sees a
live IM1+EI chain after `EI`; that the demo's screen mode leaves paper+border
pointing at CLUT-0; the bootblock section-D paging avoids ROM1 in all chosen
calls. ROM1 routines (FRAMINT/LINEINT/SETBORD/the data) are ISR/data, never
called directly — so the bootblock (ROM1 paged out) only needs ROM0 calls
(CLSLOWER/UTMSG/READKEY/RST 08/10) + inline border `OUT`.

## Verification (emulation-first, the reset chain)

Extend `tools/netboot-oracle/z80/samboot_real_boot_test.go` (i190a — boots the
real `rom.bin` + `eeprom.bin` through the reset→ROM→`&4000` chain via
`deviceLinearEEPROM` + `LoadEEPROMImage`):
- Build a test EEPROM image = the real capture, **with chunk 1 replaced by the
  combined patched bootblock**, **plus a provisioned `"SAMBOOT Config  "` chunk**
  (a free chunk) and a **bootable test record**.
- Assert: config mode=1 → the reset boot reaches `bdos_boot_record`/ALHK for the
  configured record (the boot is captured via `AttachBDOS`, as
  `samboot_inject_test.go` does); config absent/mode=0 → it falls through to the
  BASIC exit (no ALHK).
- This proves the spliced decision runs correctly **in the real reset chain**, not
  just as an isolated `samboot_inject` call (which `samboot_inject_test.go`
  already covers).

What stays hardware-only (for i230 RAM test + i135c flash): the stripes pixels,
the real ALHK record load, and the physical reset-handoff timing.

## Acceptance (i229)

- A splice tool produces a ≤1024-byte combined chunk-1 image from the local
  `eeprom.bin` + our inject, reproducibly (no proprietary bytes committed).
- The reset-chain test boots the configured record and falls through when
  unconfigured, green under `make ci-netboot-z80`.
- Then i230 (RAM-test on hardware) and only after that i135c (flash).

## Note on execution

This is the project's highest-stakes code (a bad flash bricks the boot EEPROM)
with subtle boot-ordering + proprietary-byte handling. It is to be built with
**fresh, focused attention and the local capture in-hand** — not rushed. This
plan captures the approach so that execution is precise.
