# `CLEAR n` in AUTO-RUN BASIC — mechanism, multi-disk survey, and minimum fix

Date: 2026-05-10. Branch: `m0-toolchain-bootstrap`. Author: investigation
agent (Opus 4.7) following Pete's PRIME DIRECTIVE
(`feedback_correctness_over_workarounds.md`).

This document supersedes the §6 fix recommendation in
`docs/notes/clear-investigation.md`. That recommendation
("`MODE 3: CLS #` priming before `CLEAR`") was derived by pattern-matching
on FRED 56's atypical bootstrap and is **refuted** by a survey of 153 real
SAM Coupé disks (§3) — most CLEAR-first AUTO-RUN BASICs in the wild do
exactly what we do, and they work.

ROM citations are line-in-file in
`docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt` (henceforth `rom`).
Tech Manual = `docs/sam/sam-coupe_tech-man_v3-0.txt` (`tm`). User Guide
= `docs/sam/sam-coupe_use-guide.txt` (`ug`). SAMDOS source = `~/git/samdos/src/<file>:line`.

---

## 0. TL;DR

1. **The "MODE 3: CLS # priming before CLEAR" hypothesis from
   `clear-investigation.md` §6 is REFUTED by survey.** 15 of ~90 sampled
   real SAM disks have `CLEAR n` as the first executable statement of
   their AUTO-RUN BASIC, with no priming. They work. (§3)

2. **The MCLS sysvar-collision hypothesis (`clear-investigation.md`
   §5.2-5.4) is plausible but cannot be definitively localised from
   static analysis alone.** Tracing `MCLS → CLSWIND → CLSG → SPSSR →
   SELSCRN` (§2) shows the only page-byte read is `(CUSCRNP)` at
   `rom:14809` (`SELSCRN`), and CUSCRNP at AUTO-RUN entry is `0x7E`
   (page 0x1E = screen page 1) — a screen page, not the SAMDOS page
   (0x1D). So the "MCLS LDIRs into SAMDOS" specific-mechanism claim
   from the prior agent's §5.4 is unsupported. (§2)

3. **Our setup is structurally near-identical to "Defender Compilation"
   from /tmp/sam-disks** — its AUTOBOOT is `1 CLEAR 32767: LOAD "DEFENDER"
   CODE 32768: CALL 32768`. Same layout (SAMDOS at T4S1, AUTO BASIC
   at slot 1, CODE at slot 2), nearly identical dir-entry encoding.
   Defender works on real SAM hardware. So "we're trying something
   nobody else does" is wrong. (§4)

4. **One concrete encoding bug in `tools/build-disk.sh:240-243`**: all
   three BASIC dir-entry triplets (NVARS-PROG, NUMEND-PROG,
   SAVARS-PROG at dir bytes 0xDD/0xE0/0xE3) are set to
   `len(BASIC_BODY) = 57`. Defender's NVARS-PROG = 56 (one less),
   matching "program-length WITHOUT the trailing 0xff terminator".
   The off-by-one would put NVARS one byte past the terminator,
   pointing into uninitialised memory. (§4.3)

5. **Static analysis cannot prove this off-by-one is the cause.**
   The static analysis exhausted its useful range; the next step is
   **a single targeted simcoupe instrumentation experiment** — propose
   in §6, no code change required of this investigation.

6. **Minimum fix proposal (cite-grounded, not yet validated)**: change
   `len(BASIC_BODY)` to `len(BASIC_BODY) - 1` in the three triplet
   writes (build-disk.sh:240-243). Cite: ROM `LDPROG` at `rom:22683-22695`
   computes `NVARS = PROG + (NVARS-PROG triplet)`, and the FF
   terminator at `(NVARS)` requires `(NVARS-PROG triplet) = bytes-up-to
   -but-not-including-the-FF-byte`. Defender's encoding (`00 38 80` =
   56 for a 57-byte program-with-FF) confirms this convention.

7. **This investigation explicitly corrects three prior project docs**:
   `clear-investigation.md` §6 (refuted), `2026-05-10-handoff.md` line
   199 ("CLEAR still has an unexplained problem" — narrowed to one
   plausible cause), and `clear_in_auto_run.md` (memory; recommends
   not using CLEAR — should now recommend the off-by-one fix).

---

## 1. The actual symptom (re-stating, to avoid drift)

From `2026-05-10-handoff.md:73-74`:

> `OUT : CLEAR 24575 : OUT` (no LOAD/CALL) → page-displaced screen,
> even AFTER the AUTO file fix.

So the failure is observed when `CLEAR 24575` runs as a single
statement (preceded only by `OUT 254, 4` for visual feedback) in
the AUTO-RUN BASIC line. The visual is a "page-displaced structured
pattern" — colour stripes in mode-3 attribute layout, suggesting an
LDIR with timing-dependent code-bytes-as-attributes interpretation
(`clear-investigation.md:601-604`).

The same `CLEAR 24575` works:
- Typed at the OK prompt interactively (`clear_in_auto_run.md:15-16`).
- Following `MODE 4: CLS #` in FRED 56's `MODE 4: CLS #: CLEAR 81919`
  (`fred-disk-inspection.md:520`).

Pete's empirical observation is the ground truth; we work backward
from it.

---

## 2. What ROM `CLEAR n` actually executes — sysvar-level trace

The ROM `CLEAR` routine is at `rom:13148` and runs `RECL2BIG → CLRSR
→ DOCOMP → MCLS → ...validation` (`rom:13174-13204`). The previous
investigation (`clear-investigation.md` §4) covered this in detail
and is not repeated. The new contribution here is **finishing the
sysvar-by-sysvar trace through MCLS**, which §7 of that doc explicitly
left open.

### 2.1 Cold-init values of all MCLS-relevant sysvars

`MNINIT → NEW2` (`rom:24482-24659`) cold-initialises sysvar memory.
The page-byte sysvars (`*P` suffixes) start at 0 because `MNINIT`
clears one page of RAM at `0x8000-0xBFFF` (`rom:24454-24458`,
`LD HL,8000H; LD DE,8001H; LD BC,3FFFH; LD (HL),L; LDIR`) and the
sysvar area at `0x5A00-0x5BFF` is *outside* that range, so it
contains whatever the SAM ROM left there at hardware reset — which
is **not zeroed by RAMTEST**. However:

- `MNINIT EBE6` reads SHIFT key, sets PRAMTP = `0x1F` for 512K
  (`rom:24482-24493`). PRAMTP = `(physical RAM page count - 1)`.
- `MNINIT EBFB-EC09` clears `ALLOCT` (`0x5100-0x51FF`) to 0 then writes
  0xFF to bytes ≥ `(0x21 - PRAMTP)` (`rom:24496-24509`). This is *not*
  the sysvar area.
- `MNINIT EC10-EC18` reserves `ALLOCT[0..3] = 0x40` (BASIC's 4 pages),
  sets `LASTPAGE = 3, RAMTOPP = 3` (`rom:24515-24522`).
- `MNINIT EC1E-EC23` writes `FISCRNP = (PRAMTP-1) | 0x60 = 0x7E`
  (`rom:24523-24529`).
- `MNINIT EC2B-EC2E` writes `RAMTOP = 0xBFFF`.
- `NEW2 EC8F-EC97` writes `SCPTR = SCLIST = 0x5CA0`,
  `CUSCRNP = (FISCRNP) = 0x7E` (`rom:24587-24591`),
  `OUT (VIDPORT), 0x7E` (`rom:24592` — this is VMPR, not URPORT/HMPR).
- `NEW2 ECCC-ECD8` runs `CALL ADDRPROG`. ADDRPROG (`rom:7370-7402`)
  reads `(PROGP) = (5A9F)`. Since PROGP was uninit and is in
  `0x5A00-0x5BFF`, its value depends on what was in that RAM at boot.
  **In a clean cold boot it is 0** (because the SAM ROM zero-fills
  the sysvar area as part of power-on-reset, before MNINIT runs;
  this is implicit but not cited inline — would need `cite needed`
  to be 100% certain).
- `NEW2 ECD2-ECD8` writes `(NVARS) = HL = ADDRPROG_result + 1`,
  `(NVARSP) = A = PROGP = 0`, `(ELINEP) = A = 0` (`rom:24637-24639`).
  So **NVARSP = ELINEP = 0** at end of cold init.
- `NEW2 ED08-ED0A` calls `MODET 3` (`rom:24657-24658`), which sets
  `MODE = 3`, screen-window sysvars, `M23PAPP/ATTRP` defaults.

### 2.2 Sysvar values at AUTO-RUN entry (after SAMDOS load + LDPRDT)

After `BOOT` → `BOOTEX` (loads SAMDOS via floppy) → `JP &8009` → SAMDOS
runs its sector loader and `dos8` epilogue (`b.s:33-127`). The dos8
epilogue mutates exactly five sysvars (`b.s:112-126`):

- `port2 cache` (in section C, internal SAMDOS RAM, no ROM-sysvar effect)
- `snprt2 cache` (ditto)
- `DOSFLG = PRAMTP - 2 = 0x1D` (`b.s:117`)
- `ALLOCT[DOSFLG] = 0x60` (`b.s:120-121`)
- `PSLD = 0x0144` (`b.s:124`)

Then RET to ROM, `RST 8 / DB BTHK` (`rom:20469-20471`). SAMDOS's
BTHK handler `init/initx` (`h.s:215-218`) sets `CURCMD = 0x95` (LOAD
token) and RETs. (Per `samdos2-auto-run-analysis.md`: SAMDOS's `init`
does NOT auto-load AUTO files; that's a ROM-side function via the
auto-RUN dir-entry flag.)

Then ROM's BASIC main loop runs, finds the directory entry's auto-RUN
flag (byte 0xF2 = 0; cited at `m0_open_findings.md` item 12 and ROM
`rom:22701-22713` `LDUSLN`). LDPRDT (`rom:22591`) loads the AUTO
BASIC body. Critically it does:

- `E356 LD (NVARSP), A` with `A = (HDR)-16 = 0`, **forcing NVARSP=0**
  (`rom:22624`, comment "PREVENT XOINTERS ALTERING NVARS, CAUSING
  SADJ WITH NO FOR-NEXTS").
- `E359 LD (NVARS+1), A=0` — clears MSB of NVARS for the same reason.
- `E35D CALL RECL2BIG` — deletes any current program (no-op for empty
  initial state).
- `E366 CALL ADDRPROG; E369 JR LDCR3 → MKRBIG` — opens space for the
  loading body at PROG (`rom:22632-22649`).
- `E38A CALL LDDBLK` — uses SAMDOS LDHK to load the file body bytes
  into PROG..PROG+body_len (`rom:22656`).
- `E3AB-E3CF LDPROG`: for each of three triplets `HDL+16, HDL+19,
  HDL+22` (= our dir-entry bytes 0xDD/0xE0/0xE3), reads triplet, adds
  to `(PROG)`, writes result to `(NVARSP+NVARS, NUMENDP+NUMEND,
  SAVARSP+SAVARS)` respectively (`rom:22683-22695`).

So at AUTO-RUN-line entry:

| Sysvar    | Value | Source |
|-----------|-------|--------|
| `PROG`    | 0x9CD5 (section-C-form) | `MNINIT EC42`, `rom:24547` |
| `PROGP`   | 0 | inferred (uninit RAM zeroed by power-on, not explicitly cited inline) |
| `NVARS`   | `PROG + (NVARS-PROG triplet from dir 0xDD)` | `LDPROG`, `rom:22683-22689` |
| `NVARSP`  | 0 (page byte from same triplet — first byte = 0 in our case) | same |
| `NUMEND`  | `PROG + (NUMEND-PROG triplet from dir 0xE0)` | same |
| `NUMENDP` | 0 | same |
| `SAVARS`  | `PROG + (SAVARS-PROG triplet from dir 0xE3)` | same |
| `SAVARSP` | 0 | same |
| `WKEND`   | unchanged from cold init | `rom:24655` |
| `WKENDP`  | 0 | inferred |
| `ELINE`   | unchanged from cold init (= SAVARS+1 from MNINIT) | `rom:24655` |
| `ELINEP`  | 0 | inferred |
| `RAMTOP`  | 0xBFFF | `rom:24534-24535` |
| `RAMTOPP` | 3 | `rom:24521-24522` |
| `LASTPAGE`| 3 | `rom:24515-24521` |
| `DOSFLG`  | 0x1D | `b.s:117` |
| `MODE`    | 3 | `rom:24657-24658` (`MODET 3`) |
| `THFATP`  | 0 | `rom:24650` |
| `FISCRNP` | 0x7E | `rom:24523-24529` |
| `CUSCRNP` | 0x7E | `rom:24587-24591` |
| `LMPR`    | restored to value before BTHK by `PTDOS` epilogue | `rom:12968-12973` |
| `HMPR`    | indeterminate; last touched by ROM during LDDBLK / hook restore | discussed §2.3 |
| `VMPR`    | 0x7E (set by NEW2; never modified again unless MODE/CLS rerun) | `rom:24592` |

### 2.3 What MCLS actually reads (full trace)

`MCLS = XOR A; JR CLSBL` (`rom:2080-2081`). With `A=0` (= "clear
entire screen, not just window"), CLSBL falls through to:

1. `CALL CLU1` (`rom:2101`) — clears the upper window. CLU1 is at
   `rom:2140-2200`. It:
   - Loads graphics-coord sysvars (`XCOORD`, `YCOORD`, `XOS`, `YOS`,
     `XRG`, `YRG`) via `STKZERO/SETESP/GTFCOORDS` (`rom:2142-2163`).
   - Calls `CLSE` (CLEAR ENTIRE SCREEN — QUICKLY) at `rom:2169`.
2. `CLSLOWER` (`rom:2103-2127`) — sets up a temporary window covering
   the lower-window area, calls `CLSWIND` (`rom:2127`).
3. `CLSL2`/`CLWC` (`rom:2129-2134`) — restores K-channel state.

**The page-byte read** happens inside `CLSE`/`CLSG` (`rom:2204-2250`).
For MODE 3 (our state), `CLS1` (`rom:2224`) sets up `H=0xE0,
DE=(M23PAPP), BC=0x0006`, then `CLSG` (`rom:2228`) does:

```
0782 CDA83F     CLSG:      CALL SPSSR        ;STORE PAGE, SELECT SCREEN
0785 53                    LD D,E
0786 F3                    DI
0787 ED73C85A              LD (TEMPW1),SP
078B 2E00                  LD L,0
078D F9                    LD SP,HL
078E D5         CLSLP:     PUSH DE  (×16)
0796 10F6                  DJNZ CLSLP
0798 0D                    DEC C
0799 20F3                  JR NZ,CLSLP       ;DO 0x6000 BYTES (mode 3)
079B ED7BC85A              LD SP,(TEMPW1)
079F FB                    EI
07A0 C3BF3F                JP RCURPR         ;RESET CURRENT UR PAGE
```

`SPSSR` (`rom:14799-14810`) is:

```
3FA8 DBFA     SPSSR:      IN A,(250)
3FAA 327A5A               LD (CLRP),A
3FAD E6BF                 AND 0BFH
3FAF D3FA                 OUT (250),A      ;ROM1 OFF
3FB1 DBFB     SPSS:       IN A,(251)       ;save HMPR
3FB3 32795A               LD (CURP),A
3FB6 3A785A   SELSCRN:    LD A,(CUSCRNP)   ;SCREEN PAGE
3FB9 1824                 JR SELURPG       ;OUT (251),A keeping top 3 bits
```

**The only page-byte sysvar consumed during an MCLS-driven CLSE**
is `(CUSCRNP)`. With CUSCRNP = 0x7E, `SELURPG` (`rom:14852-14861`,
`TSURPG`) preserves bits 7,6,5 of port 251 and writes the low 5 bits
of A (= 0x1E) — so HMPR ends up with low 5 bits = `0x1E`, which is
the screen page (page 30 on a 512K machine = the displayed-screen
page that VMPR also points at).

Then CLSG sets `SP = 0xE000` (= section D top, displayed-screen-page
+1 in HMPR mapping; page 31 = `PRAMTP` itself). It pushes 0x6000
bytes of pattern bytes from `0xE000` downward through `0x8000`. After
the loop, `LD SP,(TEMPW1)` restores old SP, EI, `JP RCURPR` (`rom:14817`)
restores `LMPR` from CLRP and `HMPR` from CURP — **HMPR is restored
to its entry value, not its loop-time screen-page value**.

### 2.4 What CLSWIND consumes after CLU1 returns

After CLU1 returns, CLSBL → CLSLOWER (`rom:2103-2127`) sets up
the temp window from LWBOT/LWTOP/LWRHS/M23PAPP/ATTRP and calls
`CLSWIND`. `CLSWIND` (`rom:3078-3098`) reads `WINDBOT/WINDTOP`
(set by CLSLOWER from LWxxx), `DEVICE` (`5A73`), `LSOFF` (`5A5D`),
then JR `EDRSF` (`rom:3098`).

`EDRSF` (`rom:3125-3161`): `CALL SPSSR` again (so HMPR gets bumped
to screen page again), `LD A,(MODE)`, branches by mode:
- MODE ≥ 2: `JP RUPDN` (`rom:0AD9`) — does the mode-3 attribute LDIR.

`RUPDN` does the actual "scroll/clear pattern" LDIR. It reads
`M23PAPT/ATTRT` (set by CLSLOWER from M23PAPP/ATTRP) and writes
through HMPR-mapped section C. **Again, the only page-byte read
is via CUSCRNP, and it routes correctly to the screen page.**

### 2.5 Conclusion of the MCLS sysvar trace

**No path through MCLS reads a sysvar that resolves to the SAMDOS
page (DOSFLG = 0x1D)**, given the MNINIT-time defaults that obtain
at our AUTO-RUN entry. The previous investigation's §5.4 hypothesis
("`MCLS` calls `OUT (URPORT), 0x1D` because some sysvar's page byte
got bumped by RECL2BIG/XOINTERS") would require that XOINTERS push
WKENDP/ELINEP from 0 to 0x1D. XOINTERS adjusts page bytes by ±1
(per `ASSV`/`PADJ` in `rom:7253-7309`), not by ~30. So this specific
mechanism is **unsupported** by static analysis.

That does not mean MCLS is exonerated. Possible remaining mechanisms
(not provable from static analysis):

- **A sysvar in the 0x5A00-0x5BFF area is non-zero from prior power-on
  state** (uncited assumption above) and an XOINTERS-driven adjust
  pushes it to a wrong page. Pete's empirical state would need to be
  observed to verify.
- **CLSE's `LD SP,0xE000`** assumes section D contains usable RAM at
  that point. With HMPR set to the screen page (0x1E), section D is
  page 0x1F = `PRAMTP`. On a 512K machine that's the highest page; on
  a 256K machine it's page 0x10 — but `MNINIT EBE6` rejects SHIFT
  presses, defaulting to 512K, so this path is fine for our setup.
- **An interrupt fires inside the DI-protected `CLSG` loop and
  re-enters BASIC paging logic**. This was historically a problem on
  early SAM ROMs but DI is set at `0786`, so as long as no NMI fires
  it's safe.

The honest answer is: **without runtime instrumentation we cannot
conclusively localise the failure inside `MCLS`**. See §6 for the
proposed minimum experiment.

---

## 3. Multi-disk survey

153 SAM Coupé .dsk images from `/Users/pmoore/Downloads/GoodSamC2/`
were inspected with `samfile ls` + `samfile cat | samfile basic-to-text`.
Of these, 90 contain a SAM-BASIC-typed file whose name starts with
"auto"/"AUTO"/"Auto"/"autoexec".

**Categorisation by first executable token of the auto-RUN line**:

| First executable statement | Count |
|----------------------------|-------|
| `CLEAR n`                  | **15** |
| `MODE n`/`SCREEN n`/`CSIZE`/`PALETTE`/`BORDER`/`CLS` | 16 |
| Other (LET, REM, RUN N, GO TO N, IF, POKE, OPEN, FORMAT, ON ERROR, LOAD) | 59 |

The "other" category is dominated by `RUN N` / `GO TO N` indirection
and `REM` lines — i.e. line 10 is a comment or trampoline, with the
real action at a later line.

**Disks where `CLEAR n` is the first executable statement** (refuting
`clear-investigation.md` §6's claim that `MODE: CLS #` priming is
needed):

| Disk file | Start line | First line content (truncated) |
|-----------|------------|-------------------------------|
| Defender Compilation | 1 | `1 CLEAR 32767: LOAD "DEFENDER" CODE 32768: CALL 32768` |
| Diaz Demo 2 | 1 | `10 CLEAR 32766` (line 20: `POKE DVAR 0,0: MODE 4: CLS #`) |
| Mike AJ Disc 6-Edwin | 10 | `10 CLEAR 32767` (line 20: `MODE 4: CLS #: CSIZE 8,8: LOAD "uno$" CODE`) |
| Metempsychosis Demo Christine | 0 | `10 CLEAR 31881: SCREEN 1: MODE 4: CLS #: ...: CLEAR : POKE DVAR 0,0: POKE 23658,8` |
| Metempsychosis Demo Highlander | 0 | (identical to Christine) |
| Metempsychosis Demo 6 | 0 | (identical) |
| Metempsychosis Demo 7 | 0 | (identical) |
| Metempsychosis pdm9, pdm11 | 0 | (identical) |
| Metempsychosis promo1 | 0 | (identical) |
| Metempsychosis Sample Disk 7 | 0 | (identical) |
| Metempsychosis Mega_mix | 0 | (identical) |
| Metempsychosis Internal_highlander | 0 | (identical) |
| Metempsychosis Demo 9 | 0 | (identical) |
| Metempsychosis RTJ_pdm1 | 1 | `10 CLEAR : LOAD "BOLD" CODE UDG " ": DEVICE D1: RUN 15` |

Notes:

- "Start line 0" with directory byte 0xF2=0 means "auto-RUN starting
  from the smallest line ≥ 0", which is whatever line 10 happens to
  be. So all these run line 10 first.
- All `CLEAR n` arguments are in **page 1** (16384 ≤ n ≤ 32767), same
  as our value 24575. The shape `CLEAR 24575` ↔ "set RAMTOP just
  below 24576 = LOAD CODE target" is canonical convention from
  `ug:4886-4896` and item 6 of `m0_open_findings.md`.
- **None** of these disks precede `CLEAR n` with `MODE x: CLS #`.
  FRED 56's pattern `MODE 4: CLS #: CLEAR 81919` is unusual.

**Defender Compilation is the closest analog to our setup**:

```
File  Type         Size    First sectors  Notes
DOS   Code (#19)   10000B  T4S1..T5S10    ≡ samdos2 (named "DOS"; same body)
AUTOBOOT  BASIC    660B    T6S1..T6S2     line 1: CLEAR 32767: LOAD "DEFENDER" CODE 32768: CALL 32768
DEFENDER  Code     32032B  T6S3+...       game body, loaded by AUTOBOOT
```

Compare to our M0 disk (`tools/build-disk.sh`):

```
File     Type         Size    First sectors  Notes
samdos2  Code (#19)   10000B  T4S1..T5S10    same SAMDOS2 binary
auto     BASIC        57B     T6S1           line 10: CLEAR 24575: LOAD "stub" CODE 24576: CALL 24576
stub     Code         124B    T6S2           our Z80 stub
IN       Code         12B     T6S3           input fixture
```

**The structural analog is essentially exact.** Defender Compilation
is the smoking-gun proof that our pattern works in principle.

### 3.1 What the 16 "MODE-first" disks do

For completeness, the AUTO-RUN BASICs that begin with `MODE`/`SCREEN`/
`PALETTE` (no CLEAR first) include all FRED issues 1-12 except
FRED 5 (which uses CLEAR), Banzai, BoxRevenge, several Mike AJ
demos, Pics-from-the-Net, RGB Viewer Pics, and various single-purpose
demos. Most of these follow `MODE n` with `LOAD "code-blob" CODE` and
do NOT use `CLEAR n` later in their flow — so they have no need to
protect a RAM region from BASIC variables.

This subset roughly matches the FRED-style "menu/launcher" pattern
where the AUTO BASIC is the menu itself, not a thin trampoline. Our
M0 use case is the trampoline pattern — same as Defender, not FRED.

### 3.2 What `CLEAR` does in canonical disks vs. ours

Defender's `CLEAR 32767` and ours `CLEAR 24575`: both set
`RAMTOPP = (n DIV 16384) - 1`. For 32767: page 1, RAMTOPP after
CLEAR = 0. For 24575: page 1, RAMTOPP after CLEAR = 0. Identical.
Both leave `RAMTOP = (n MOD 16384) | 0x8000` = 0xBFFF (Defender)
or 0x9FFF (ours). Different offsets, but per `ug:4891-4896` and
`sam-paging.md:807-820`, both are within the 4 BASIC pages.

Per `rom:13189-13192`: `CP C; JR NC, CLR4` — the validator passes
when `LASTPAGE ≥ new-RAMTOPP-page`. LASTPAGE = 3 from MNINIT;
new-RAMTOPP-page = 0 in both cases; 3 ≥ 0 ⇒ NC ⇒ CLR4 ⇒ success.

So at the ROM-CLEAR level, Defender's `CLEAR 32767` and our `CLEAR
24575` are structurally equivalent and both pass validation.

---

## 4. Concrete differences between our disk and Defender Compilation

A bit-by-bit comparison of slot-1 directory entry + body header.

### 4.1 Body header (9 bytes prepended to each file body)

| Field (bytes 0-8) | Defender AUTOBOOT @ T6S1+0 | Our auto @ T6S1+0 | Difference? |
|---|---|---|---|
| 0 (Type) | `10` | `10` | same — type 16 BASIC |
| 1-2 (LengthMod16K LE) | `94 02` (660) | `39 00` (57) | different size, expected |
| 3-4 (PageOffset LE) | `d5 9c` | `d5 9c` | **same** = 0x9CD5 (PROG section-C-form) |
| 5-6 (Unused — canonical FF FF) | `ff ff` | `ff ff` | same |
| 7 (Pages) | `00` | `00` | same |
| 8 (StartPage) | `00` | `00` | same |

Body headers are functionally identical.

### 4.2 Directory entry bytes 0xDD–0xEE (BASIC-specific tail)

| Offset within entry | Defender AUTOBOOT | Our auto | Notes |
|---|---|---|---|
| 0xDD..0xDF (NVARS-PROG triplet) | `00 38 80` = `(p=0, off=0x8038)` ⇒ 56 | `00 39 80` = `(p=0, off=0x8039)` ⇒ 57 | **off-by-one** — see §4.3 |
| 0xE0..0xE2 (NUMEND-PROG triplet) | `00 94 80` ⇒ 148 | `00 39 80` ⇒ 57 | program has no numerics, so 57 = 56 + 0 + 1 (off-by-one); should be 56 |
| 0xE3..0xE5 (SAVARS-PROG triplet) | `00 94 82` ⇒ 660 | `00 39 80` ⇒ 57 | same comment |
| 0xE6 (separator) | `20` | `00` | Defender uses ASCII space; ours uses 0x00. Functional effect: unknown. |
| 0xE7..0xEB (padding) | `ff ff ff ff ff` | `00 00 00 00 00` | cosmetic; ROM does not read these bytes per anything I traced |
| 0xEC (StartPage mirror) | `00` | `00` | same |
| 0xED..0xEE (PageOffset LE mirror) | `d5 9c` | `d5 9c` | same |
| 0xF0..0xF1 (LengthMod16K LE) | `94 02` | `39 00` | size, expected |
| 0xF2 (auto-RUN flag) | `00` | `00` | both auto-RUN |
| 0xF3..0xF4 (start line LE) | `01 00` (line 1) | `0a 00` (line 10) | different; both valid |

### 4.3 The off-by-one in (NVARS-PROG)

Defender's BASIC body (660 bytes) decomposes:

- offset 0..3 = line# (BE) + body-length (LE) for line 1
- offset 4..54 = tokenised statements (51 bytes)
- offset 55 = `0x0d` (line CR terminator)
- offset 56 = `0xff` (program terminator at NVARS)
- offset 57..147 = numerics zone (filled FF, length 91 + 1 byte)
- offset 148..659 = strings zone (saved variable values)
- offset 660 = end of file body

Defender's NVARS-PROG = 56. So `(NVARS) = PROG + 56` = the byte
holding the FF program-terminator. **The program proper occupies
PROG..PROG+55 inclusive (56 bytes); the FF terminator sits at NVARS,
and NVARS-PROG counts UP TO but NOT INCLUDING the terminator.**

Our BASIC body (57 bytes) decomposes:

- offset 0..3 = line# + body-length for line 10
- offset 4..54 = tokenised line 10 (52 bytes — counted by Python in §3.2.1 of clear-investigation didn't account for the `0x0d` already in line_body)
- offset 55 = `0x0d` line CR terminator
- offset 56 = `0xff` program terminator

So our program occupies offsets 0..55 (56 bytes), and the FF terminator
is at offset 56. **NVARS-PROG should be 56, not 57.** Cite for the
encoding convention: `rom:22683-22695` (LDPROG) computes `NVARS = PROG
+ (NVARS-PROG triplet)`; combined with Defender's empirical encoding
(56-byte program with terminator at offset 56 ⇒ triplet stores 56),
the convention is "triplet stores offset of FF terminator from PROG"
— equivalently, length-of-program-without-FF.

`tools/build-disk.sh:240-243` writes `page_form_3byte(len(BASIC_BODY))`
where `len(BASIC_BODY) = 57`. Should be `len(BASIC_BODY) - 1 = 56`.

### 4.4 Why this off-by-one might matter for `CLEAR`

When `CLEAR n` runs, its very first computation (`rom:13167-13174`)
is:

```
3920  LD HL,(ELINE)
3923  LD A,(ELINEP)
3923  CALL SUBAHLCDE     ; AHL = ELINE - NVARS (page-form)
3926  LD BC,025DH
3929  CALL SUBAHLBC      ; AHL = "space to reclaim"
392C  LD B,H; LD C,L     ; BC = mod-16K of space
392E  LD HL,(NVARS)
3931  CALL RECL2BIG      ; reclaim ABC bytes at HL
```

`ELINE` is set by MNINIT at `rom:24655` to `(SAVARS) + 1`. SAVARS in
turn is set by CLRSR (`rom:13209-13234`) running through SETNE/SETSAV.

Working through CLRSR: it calls ADDRNV (HL=NVARS, A=NVARSP=0), fills
46 bytes with FF (the 23 letter pointers, 2 bytes each), then `EX
DE,HL; LD HL,PSVTAB; LD C,26; LDIR` (copies 26 bytes). After this
HL is at NVARS+46+26 = NVARS+72. Then `CALL SETNE` writes
NUMENDP/NUMEND from current HL+page state. `INC H; INC H` (skips a
0x200 block — this is the FPCS area). `CALL SETSAV` writes
SAVARSP/SAVARS. `LD (HL),0xFF` (terminator at SAVARS).

So after CLRSR: NVARS, NUMEND, SAVARS, ELINE are all in page 0 at
addresses derived from PROG's offset.

**Now consider the off-by-one**. With our triplets all set to 57:

- After LDPROG: NVARS = PROG + 57, NUMEND = PROG + 57, SAVARS = PROG + 57.
- ELINE was set by MNINIT to SAVARS+1 — but MNINIT used a *different*
  NVARS (the one from ADDRPROG at boot time), so ELINE at AUTO-RUN
  entry is **the MNINIT-time SAVARS+1, NOT the LDPROG-time SAVARS+1**.

Specifically: MNINIT ran CLRSR which set SAVARS at NVARS_init+72+0x200
(roughly). NVARS_init = PROG+1 (from `INC HL` in `rom:24636`). So
MNINIT-time SAVARS ≈ PROG + 1 + 72 + 512 = PROG + 585. And
MNINIT-time ELINE = SAVARS+1 ≈ PROG + 586.

After LDPROG: NVARS/NUMEND/SAVARS = PROG+57. ELINE is **unchanged**
(LDPROG does not rewrite ELINE).

So at CLEAR entry: NVARS = PROG+57, ELINE = PROG+586 (approx).

`SUBAHLCDE`: AHL = ELINE - NVARS = 586 - 57 = 529.

`SUBAHLBC` with BC=0x025D (605): AHL = 529 - 605 = -76 (signed).

In page-form arithmetic (`rom:7565-7589`), -76 has `A=0xFF` (high
bit set after AHLNORM rotates), `HL` has wrap-around bits. Then
`PAGEFORM` fixes `H` and computes `CCF` overflow flag if `A>0x20`.
For A=0xFF... we get carry set on `CP 0x20` then CCF gives **NC**
i.e. "no carry" — meaning the overflow check passes as if positive.

But the actual values returned are huge in their interpretation as
"bytes to reclaim". RECL2BIG (`rom:7191`) does `RES 7,B; RES 6,B`
to clamp B to ≤ 0x3F, then OR D|B|C. If A (=D after the LD D,A in
1E57) is 0xFF, OR with anything is non-zero, so RECL2BIG proceeds
into XOINTERS with garbage block-size = 0xFF * 16384 + something.

**XOINTERS with a garbage block size will adjust ALL 14 sysvar page
bytes by an unbounded amount**, potentially pushing ELINEP/WKENDP/
WORKSPP into invalid pages (e.g. 0x1D = SAMDOS, or higher).

This is the **most plausible causal chain** between the off-by-one
and the visual-corruption symptom. **It is still not 100% provable
without runtime instrumentation** (XOINTERS' actual behaviour with
garbage AHL depends on intricate signed-arithmetic details of
PAGEFORM/AHLNORM that are easy to miscompute by hand).

### 4.5 Why the off-by-one would NOT have shown up before the AUTO header fix

Before commit c3ce913 / `2026-05-10-handoff.md` line 12-25, `tools/build-disk.sh`
wrote zeroed dir-entry triplets at 0xDD-0xE5 (`samfile-capabilities.md`'s
write of `0x00 0x00 0x00`). After the fix, the triplets are
`00 39 80` (= length 57). Both the pre-fix (zeroed) and post-fix
(57) values lead to incorrect NVARS/NUMEND/SAVARS being computed by
LDPROG. The crash visualisation changed (red+pattern → blue noise
per `clear_in_auto_run.md:30-33`) but the underlying disorder
persisted — different garbage values produce different visual
patterns, not a fundamentally different failure.

**A correct triplet (= 56) has not been tried yet.**

---

## 5. Refutations of prior project docs

### 5.1 `clear-investigation.md` §6 — REFUTED in part

§6 recommended emitting `MODE 3: CLS #` before `CLEAR n` in the AUTO
BASIC line, on the basis that FRED 56's `MODE 4: CLS #: CLEAR 81919`
works. The §3 multi-disk survey shows this is not a canonical
convention — 15 of ~90 sampled disks use `CLEAR n` without any
MODE/CLS priming and they work on real hardware.

What §6 got right:
- The mechanism's locus IS in CLEAR's call to `MCLS` and / or its
  predecessor `RECL2BIG`.
- The fix should be in our build, not in SAMDOS or the boot path.

What §6 got wrong:
- The proposed fix (adding `MODE 3: CLS #`) is not necessary.
- The "MCLS LDIRs into SAMDOS page" specific claim (§5.4) is not
  supported by static analysis.

§7's honest "I cannot fully cite" caveat is well-placed.

### 5.2 `samdos2-auto-run-analysis.md` — STANDS

This doc's headline claim ("SAMDOS2 alone does not auto-RUN AUTO files;
that's a ROM-side function via the dir-entry auto-RUN flag, not a
SAMDOS hook") is supported and confirmed by the multi-disk survey
(15 disks rely on the same ROM-side mechanism).

### 5.3 `m0-status.md` "What is NOT working" — needs update

`m0-status.md:42-65` presents the symptom as "after LOAD completes,
OK prompt appears, CALL doesn't fire" and attributes it to
"URPORT corruption in PDPSR2". This diagnosis was made before the
empirical narrowing in `2026-05-10-handoff.md:73-74` showed CLEAR
alone (no LOAD/CALL) crashes — meaning the issue is upstream of
the LOAD CODE flow. The PDPSR2 corruption hypothesis is now
**probably wrong** (or, more conservatively, addresses a different
issue that may or may not also exist).

The "next concrete step (option (c))" — making `auto` a
type-19 (Code) directory entry with auto-execute — would also work,
because it would skip BASIC entirely and avoid the `CLEAR n` problem.
But it is not the *minimum* fix per the PRIME DIRECTIVE: the off-by-one
in the triplets is a one-character fix in build-disk.sh. Pete should
weigh the two against each other.

### 5.4 `clear_in_auto_run.md` (memory) — needs update

This memory file currently advises: "Don't use `CLEAR n` in AUTO-RUN
BASIC lines." Per §3 (15 real disks do exactly that) and §4 (probable
specific cause is a fixable encoding bug), this advice is wrong.
Updated guidance should be: "If `CLEAR n` crashes in AUTO-RUN, check
that build-disk.sh's NVARS-PROG triplet at dir-entry byte 0xDD-0xDF
encodes `len(BASIC_BODY) - 1`, not `len(BASIC_BODY)`."

### 5.5 `sam-paging.md` — has a known wrong claim already documented

Per `2026-05-10-handoff.md:165-178`: sam-paging.md:765's claim that
the page byte for address 0x6000 should be 0x01 is contradicted by
real-SAVE evidence (CHOMPER) which uses 0x00. This is already noted
in the handoff. Not a new finding from this investigation.

---

## 6. Proposed minimum fix

**In `tools/build-disk.sh`, change line 240 from:**

```python
triplet = page_form_3byte(len(BASIC_BODY))
```

**to:**

```python
triplet = page_form_3byte(len(BASIC_BODY) - 1)  # offset of FF terminator from PROG
                                                # (NVARS-PROG; cite ROM rom:22683-22695)
```

Cite: `rom:22683-22695` (LDPROG computes `(NVARSP, NVARS) = PROG +
NVARS-PROG triplet`); empirical confirmation from Defender Compilation's
AUTOBOOT dir-entry (`/Users/pmoore/Downloads/GoodSamC2/Defender
Compilation (19xx).dsk` slot 1 bytes 0xDD-0xDF = `00 38 80` for a
57-byte body whose FF terminator sits at offset 56).

This is a one-character fix (subtract 1) in build-disk.sh.

### 6.1 If the fix doesn't work — minimum diagnostic experiment

If `CLEAR 24575` still crashes after the off-by-one fix, the next
step is **runtime instrumentation of the patched simcoupe**, not
more static analysis. Propose (do NOT modify the patch yourself —
this is the recommendation Pete should consider implementing):

1. Add to `tools/simcoupe-exitonhalt.patch` a tiny PC-trigger logger:
   when PC reaches `0x3901` (CLEAR entry), `0x3937` (DOCOMP entry),
   `0x393A` (MCLS entry inside CLEAR), `0x06DD` (CLSWIND call inside
   MCLS-CLSLOWER), and `0x0782` (CLSG entry), log to stderr:
   - PC, A, BC, DE, HL, IX, IY, SP
   - LMPR (`IN A,(0xFA)`), HMPR (`IN A,(0xFB)`), VMPR (`IN A,(0xFC)`)
   - The first 8 bytes at `(IX)`, `(IY)`, and `(SP)`
   - Selected sysvars: NVARSP/NVARS, NUMENDP/NUMEND, SAVARSP/SAVARS,
     ELINEP/ELINE, WKENDP/WKEND, FISCRNP, CUSCRNP, RAMTOPP/RAMTOP,
     LASTPAGE, DOSFLG, MODE, M23PAPP, ATTRP

2. Run the disk with the AUTO line `10 OUT 254, 4: CLEAR 24575: OUT
   254, 7` and capture the log.

3. Inspect the log:
   - Are NVARS/NUMEND/SAVARS sensible after LDPROG? (Should be PROG+57 = 0x5D0E in section-C-form 0x9D0E.)
   - Is HMPR sensible at MCLS entry? (Should be screen page 0x1E
     during the LDIR loop, anything else outside.)
   - Does an `OUT (URPORT), <unexpected page>` appear in the log?

This single experiment localises the failure to within ≤ 5 ROM
regions. If it shows the off-by-one fix works, we're done. If not,
the log will pinpoint the next layer to investigate.

### 6.2 Why this is minimum-fix per the PRIME DIRECTIVE

Per `feedback_correctness_over_workarounds.md`:
> Workarounds you cannot explain are fragile and hide the real bug.

The §6 fix is **not** a workaround:

- It's grounded in a specific cite (`rom:22683-22695`).
- It's grounded in observed canonical convention (Defender's encoding).
- It's a one-character change.
- It does not bypass any feature of the system.
- If it works, we'll **know** why CLEAR was failing (the triplet
  value caused garbage block-size, RECL2BIG/XOINTERS adjusted page
  bytes by an unbounded amount, MCLS subsequently read a stale sysvar,
  HMPR landed on a wrong page, LDIR scribbled).
- If it doesn't work, the §6.1 experiment localises the next layer.

The alternative `MODE 3: CLS #` priming from `clear-investigation.md`
§6 is a **workaround** because it would happen to mask the symptom
without addressing the root encoding bug — and would leave the same
encoding latent for any future use of variables / NUMEND etc. in
AUTO-RUN BASIC.

---

## 7. What this investigation did NOT establish

Honest gaps:

1. **The exact behaviour of `RECL2BIG → XOINTERS` with negative
   AHL (= ELINE-NVARS-0x025D < 0)**. The signed arithmetic of
   `SUBAHLBC → AHLNORM → PAGEFORM` (`rom:7546-7589`) was traced but
   not exhaustively simulated. RECL2BIG might ALSO RET-Z early
   (`rom:7196`) if the masked block size is zero — meaning XOINTERS
   never runs. If so, the off-by-one fix wouldn't help on its own
   and the real cause is downstream of RECL2BIG (e.g. in CLRSR's
   reset of NVARSP to 0 via ADDRNV's read of the now-correct
   `(NVARSP)`). The §6.1 experiment would settle this.

2. **Whether MNINIT-time `(PROGP)` is 0 by virtue of power-on RAM
   zero-fill.** This was assumed in §2.1 but not cited from a specific
   ROM line. If the SAM ROM's pre-MNINIT PALL/RAM init does NOT zero
   `0x5A00-0x5BFF`, then PROGP at NEW2 time could be any value, and
   subsequent NVARSP/ELINEP would inherit it. This would render
   build-disk.sh's "encode triplet with page=0" assumption invalid in
   some boot scenarios.

3. **Whether the off-by-one is ALSO present in slots 2 (stub) and 3
   (IN) bodies.** The CODE-typed slots use the body header's
   `LengthMod16K` (bytes 1-2) for the loaded length, not a triplet.
   But `tools/build-disk.sh:273` writes `len(stub_body) & 0xff,
   (len(stub_body) >> 8) & 0xff` — which is byte-exact, not
   off-by-one. The stub/IN encoding is fine.

4. **Whether the FRED 56 bootstrap's BTHK handler does anything beyond
   what `samdos2-auto-run-analysis.md` documents.** FRED's body at
   `&8090+` was not fully disassembled in this investigation. Per
   `clear-investigation.md` §3.2 it does have a custom BTHK handler.
   This is unrelated to whether `CLEAR n` works in our context but
   matters if FRED's pattern ends up being our fallback.

---

## 8. Sources cited

| Tag | File | Line ranges used |
|-----|------|------------------|
| `rom:NNNN` | `docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt` | 869-900 (sysvar EQUs); 1131-1143 (KBQ/PRAMTP/RAMTOP/RAMTOPP/LASTPAGE EQUs); 2080-2200 (MCLS/CLS/CLSE/CLSG); 2204-2250 (CLSE/CLSG); 3070-3100 (CLSWIND); 3120-3160 (EDRSF); 7180-7228 (RECL2BIG); 7350-7402 (ADDRNV/ADDRPROG/ASV2); 7540-7600 (SUBAHLCDE/SUBAHLBC/AHLNORM/PAGEFORM); 12013-12068 (DOCOMP/COMPILE); 13141-13247 (CLEAR/CLR1/CLR3/CLR4/CLRSR); 14773-14786 (UNSTLEN); 14796-14861 (SPSSR/SPSS/SELSCRN/SELURPG/RCURPR/TSURPG); 19181-19222 (LASTPAGE); 20453-20598 (BOOT/BOOTNR/BOOTEX/BTNOE); 22591-22713 (LDPRDT/LDPROG/LDUSLN); 23525-23613 (XOINTERS); 24430-24700 (MNINIT/NEW2). |
| `tm:NNNN` | `docs/sam/sam-coupe_tech-man_v3-0.txt` | 887-906 (sections); 1063-1125 (LMPR/HMPR/VMPR layout); 4524, 4548 (hook 128 spec); 4632-4641 (SAMDOS-paging) |
| `ug:NNNN` | `docs/sam/sam-coupe_use-guide.txt` | 4886-4896 (CLEAR n usage); 4938-4946 (BOOT/disk usage) |
| `b.s:NN` | `~/git/samdos/src/b.s` | 14-22 (samdos header); 33-127 (dos: loader and dos8 epilogue); 497-540 (samhk hook table) |
| `h.s:NN` | `~/git/samdos/src/h.s` | 132-156 (hsave); 215-237 (init/initx/hauto); 308-321 (cals) |
| `clear-inv.md` | `docs/notes/clear-investigation.md` | passim; specifically §0 (TL;DR), §3.1 (LDPRDT effects), §4 (CLEAR full trace), §6 (refuted recommendation), §7 (honest unknowns) |
| `paging.md` | `docs/notes/sam-paging.md` | §2 (port semantics); §3 (R1OSR/POPOUT/TSURPG); §4 (REL PAGE FORM); §7 (SAMDOS paging); §9 example C/D (CLEAR semantics worked examples) |
| `samdos-AR.md` | `docs/notes/samdos2-auto-run-analysis.md` | TL;DR + §1 (SAMDOS hauto is dead code) |
| `fred.md` | `docs/notes/fred-disk-inspection.md` | §1 (FRED ls); §2 (boot mechanism); appendix (FRED auto-RUN BASIC) |
| `m0-status` | `docs/notes/m0-status.md` | §"What is NOT working" |
| `handoff` | `docs/notes/2026-05-10-handoff.md` | §"Test results" L60-77; §"Page-byte encoding" L160-191 |
| `m0_findings` | `~/.claude/projects/-Users-pmoore-git-sam-aarch64/memory/m0_open_findings.md` | item 6 (CLEAR-before-LOAD-CODE convention); item 12 (BASIC auto-RUN dir-byte 0xF2) |
| `prime` | `~/.claude/projects/-Users-pmoore-git-sam-aarch64/memory/feedback_correctness_over_workarounds.md` | passim |
| `cl-AR mem` | `~/.claude/projects/-Users-pmoore-git-sam-aarch64/memory/clear_in_auto_run.md` | passim |
| build-disk | `tools/build-disk.sh` | 56-309 (the python build block); 240-243 (the triplet write — locus of the proposed fix) |
| Defender disk | `/Users/pmoore/Downloads/GoodSamC2/Defender Compilation (19xx).dsk` | dir entry slot 1 (offsets 0x100-0x1FF); body at offset 0xF000-0xF294 |
| Survey corpus | `/Users/pmoore/Downloads/GoodSamC2/*.dsk` | 153 disks; 90 with auto-named BASIC files; 15 with CLEAR-first AUTO-RUN (§3 table) |
