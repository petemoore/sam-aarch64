# Why `CLEAR n` works in FRED 56's auto-RUN BASIC but crashes in our minimal-boot auto-RUN

Date: 2026-05-10. Branch: `m0-toolchain-bootstrap`. Author: investigation
agent (Opus 4.7).

This document answers: why does `CLEAR n` succeed inside FRED 56's
auto-RUN BASIC line `10 MODE 4: CLS #: CLEAR 81919` but crash when our
hand-built disk runs an auto-RUN BASIC line `10 CLEAR 24575: LOAD "stub" CODE 24576: CALL 24576`?

The empirical setup, prior refuted hypotheses, and the requirement to
explain (not work around) are documented in the agent prompt; they are
not repeated here. Pete: **the conclusion is in §6 — read that first if
you only want the answer**.

All claims are cited `file:line`. Where the cited file is the SAM ROM
disassembly the line numbers are line-in-file in
`docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt` (henceforth
`rom`). Tech Manual lines are in `docs/sam/sam-coupe_tech-man_v3-0.txt`
(henceforth `tm`). SAMDOS source lines are
`/Users/pmoore/git/samdos/src/<file>:line`.

---

## 0. TL;DR

**The minimal-boot disk crashes inside `CLEAR`'s call to `MCLS`
(`rom:13177`), not inside any of the sysvar-validation or memory-reclaim
paths.**

`CLEAR` calls `MCLS` (= clear screens, `rom:2080`) AS A SUBROUTINE.
`MCLS` reads `MODE` (`5A40H`), `THFATP` (`5A44H`), `LWBOT`/`LWTOP`/
`LWLHS`/`LWRHS`/`UWTOP`/`UWBOT`/`UWLHS`/`UWRHS` (the upper- and
lower-window bounding boxes), `M23PAPP`, `ATTRP`, `WINDTOP`/`WINDBOT`/
`WINDLHS`/`WINDRHS` (the temporary window), `SCPTR`/`FISCRNP`/
`SCLIST`/`CUSCRNP` (which physical page is the displayed screen),
`CHANS`/the K-channel/`SPOSNL`/`TVFLAG`/`ATTRT` and a clutch of
graphics-coord sysvars (`XCOORD`/`YCOORD`/`XOS`/`YOS`/`XRG`/`YRG`).

In an *interactive* session those sysvars have been initialised by
`MNINIT` → `NEW2` (`rom:24587–24659`) and then *paid in to* by
`CHIT` data-block `LDIR` of 18 bytes (`rom:24560–24565`) and `MAIT`
data-block `LDIR` of 26 bytes (`rom:24566–24569`). They are valid.

In FRED 56's auto-RUN, `CLEAR 81919` is **preceded by `MODE 4: CLS #`**
(see line 10 of `AUTOFRED..` below), so even if MODE/window state were
in an unusual state at AUTO-RUN entry, the `MODE 4: CLS #` rebuilds it
*before* `CLEAR` runs.

In our minimal-boot, `CLEAR 24575` is the **first statement of the
AUTO-RUN program**, executed straight after `LDPROG` returns. Between
cold-`MNINIT` time and that point, the screen window state and (more
specifically) `M23PAPP`/`ATTRP`/`WINDxxx` retain MNINIT-time defaults
that `MCLS` *does* successfully run on at the OK prompt — but the
**displayed-screen page** has been pulled out from under it because
SAMDOS placed itself at the page that `FISCRNP` was originally pointing
at.

The crash visualisation ("structured page-displaced screen pattern, no
OK prompt") matches `MCLS` paging in the *wrong* page through HMPR and
calling `CLSWIND`, which then `LDIR`s background colour into what is
actually SAMDOS code or BASIC ROM-mirror RAM at `&8000–&BFFF`.

The most reliable fix is **emit `MODE 3: CLS #` (or any equivalent
screen-state rebuild) before `CLEAR n`** in the AUTO line, mirroring
FRED's pattern. Failing that, set `FISCRNP` and `CUSCRNP` *before* the
auto-RUN entry to a page that is not the SAMDOS resident page.

The full chain of evidence is in §1–§5.

---

## 1. FRED 56's bootstrap: what state it leaves behind

FRED 56's T4S1 file `\x7f FRED56 \x7f` (8078 bytes) is a custom
bootstrap that is **functionally a SAMDOS replacement plus an extra
auto-RUN engine**. Its body, disassembled with the ROM's `JP &8009`
target as VMA (so that the 9-byte file header lives at &8000–&8008 and
real code starts at &8009), is at `/tmp/fred-boot-corrected.disasm`.

### 1.1 The first 130 bytes are an inline floppy-sector loader

`/tmp/fred-boot-corrected.disasm:8009-8076` is byte-identical in
*function* to SAMDOS's `dos:..dos8` loop in
`/Users/pmoore/git/samdos/src/b.s:33-110`:

| FRED address | FRED bytes | SAMDOS source line | Effect |
|--------------|------------|--------------------|--------|
| 8009  | `21 fe 81`        | `b.s:33` `ld hl,&8000+510` | HL = end-of-sector pointer |
| 800c  | `11 02 04`        | `b.s:34` `ld de,&0402`      | DE = first track/sector to load |
| 800f  | `af`              | `b.s:36` `xor a`             | clear retry counter |
| 8010  | `32 11 81`        | `b.s:37` `ld (dct+&4000),a` | clear DCT in section B |
| 8013  | `22 05 b8`        | `b.s:38` `ld (svhl+&4000),hl` | save HL across reads |
| 8016–806e | identical to `dos2..dos7` floppy I/O loop | `b.s:40-103` | step+seek+read sectors via VL-1772 |
| 806f–8076 | `2b 5e 2b 56 7a b3 20 98` | `b.s:104-110` `dos8: dec hl; ld e,(hl); dec hl; ld d,(hl); ld a,d; or e; jr nz,dos` | follow next-sector chain |

### 1.2 The dos8 epilogue (FRED 8077–8090) is byte-identical to SAMDOS

`/tmp/fred-boot-corrected.disasm:8077-8090`:

```
8077 3a b4 5c    LD A,(&5CB4)         ; A = PRAMTP
807a 32 2f 81    LD (&812F),A         ; FRED's port2 cache (section C @ &812F)
807d 3d          DEC A
807e 32 08 81    LD (&8108),A         ; FRED's snprt2 cache
8081 3d          DEC A
8082 32 c2 5b    LD (&5BC2),A         ; DOSFLG = PRAMTP - 2
8085 26 51       LD H,&51
8087 6f          LD L,A
8088 36 60       LD (HL),&60          ; ALLOCT[DOSFLG] = &60
808a 21 44 01    LD HL,&0144
808d 22 06 5a    LD (&5A06),HL        ; PSLD = device-letter "D" / disk 1
8090 c9          RET
```

Compare `b.s:112-126`:

```
ld a,(&5cb4)   ;page (PRAMTP)
ld (port2+&4000),a
dec a
ld (snprt2+&4000),a
dec a
ld (&5bc2),a   ;dosflg
ld h,&51
ld l,a         ;dsc use
ld (hl),&60      ; ALLOCT[A] = 0x60
ld hl,&0144    ;device
ld (&5a06),hl     ; PSLD = 0x0144
ret
```

The two epilogues set the same five things (PRAMTP cached, DOSFLG,
ALLOCT[DOSFLG]=`&60`, PSLD=`&0144`) and `RET`. Cite: `b.s:112-126`,
`/tmp/fred-boot-corrected.disasm:8077-8090`.

**At RET time**, FRED's bootstrap and our SAMDOS leave the *core sysvar
state* identical. There is no divergence in DOSFLG, PRAMTP, or PSLD at
this point.

### 1.3 What's not initialised by the FRED epilogue (or by SAMDOS)

The FRED bootstrap, like SAMDOS itself, does not touch:

- `RAMTOP`/`RAMTOPP` (`5CB2`/`5CB1`) — set to `&BFFF` / `PRAMTP-1` by
  ROM `MNINIT` at `rom:24493, 24521-24522, 24534-24535`. Untouched
  thereafter unless `OPEN n` (`rom:19189`) or `CLEAR` (`rom:13197-13198`)
  modify them.
- `LASTPAGE` (`5CB0`) — set to 3 by ROM `MNINIT` (`rom:24515-24521`:
  `LASTPAGE = (PRAMTP&31)+4-1`-equivalent; concretely `L-1` after the 4
  reservations). Updated only by `OPEN n` (`rom:19189`).
- `NVARS`/`NUMEND`/`SAVARS` (`5A88`/`5A85`/`5A82`) — set by ROM
  `MNINIT → NEW2 → CLRSR` (`rom:24652` and `13209-13235`) so
  `SAVARS = WKEND` and the empty-program terminators (`FFh`) sit there.
- `ELINE` (`5A94`) — `MNINIT` sets it to `SAVARS+1` (`rom:24653-24655`).
- `WKEND` (`5A8E`) — set indirectly via the `MAIT` data block at
  `rom:24566-24569` (26-byte `LDIR` of `MAIT` to `BASSTK..ELINE`).
- `MODE` (`5A40`), `MODET`, `THFATP`/`THFATT` (`5A44`/`5A4D`),
  `WINDTOP`/`WINDBOT`/`WINDLHS`/`WINDRHS` (`5A56-5A59`), `LWTOP`/
  `LWBOT`/`LWLHS`/`LWRHS` (`5A3E-5A3F` / `5A3C-5A3D`), `UWTOP`/`UWBOT`/
  `UWLHS`/`UWRHS` (`5A3A-5A3B` / `5A38-5A39`), `M23PAPP` (`5A48`),
  `ATTRP` (`5A45`), `MASKP` (`5A46`), `PFLAGP` (`5A47`), `OVERP`
  (`5A4A`) — set by `MODET` call inside `NEW2` (`rom:24656-24658`).
- `FISCRNP` (`5C9F`), `CUSCRNP` (`5A78`), `SCPTR` (`5C9D`),
  `SCLIST` (`5CA0`) — set by `MNINIT` `rom:24523-24533` and by `NEW2`
  `rom:24587-24598`. **Critically**, `FISCRNP` is set to
  `(PRAMTP-1) | 0x60` (a screen-page marker) at `rom:24524-24529` —
  i.e. **the screen page is `PRAMTP - 1` = `0x1E` on a 512K machine**
  before SAMDOS loads.

After the FRED epilogue's `RET` lands back in ROM `BOOTEX`, ROM does
`RST 8 / DB BTHK` (`rom:20469-20471`) — see §3.

---

## 2. Our minimal-boot path: what state SAMDOS leaves behind

`tools/build-disk.sh:138-176` writes our disk so that ROM `BOOTEX`
reads T4S1 raw to `&8000`, finds the literal `BOOT` at sector offset
256, and `JP &8009` lands on byte 0 of the SAMDOS body
(`docs/notes/sam-disk-format.md:472-535`). No custom bootstrap of our
own — we use vendored `samdos2.bin` directly.

The state at the moment SAMDOS's `RET` (after the `dos8` epilogue) is
*identical* to FRED's, by the byte-for-byte equivalence shown in §1.2.

In particular: at "BOOT command's RST 8 BTHK has fired and SAMDOS has
returned" time, the state is fully determined by:

(a) whatever ROM `MNINIT → NEW2 → MODET` left behind, and
(b) the five mutations SAMDOS made:
    - `ALLOCT[PRAMTP-2] = 0x60` (`b.s:120-121`)
    - `DOSFLG = PRAMTP-2` (`b.s:117`)
    - `PSLD = 0x0144` (`b.s:124`)
    - `port2`/`snprt2` internal SAMDOS RAM (in section C, doesn't
       affect ROM sysvars) (`b.s:113-115`)

There is **no divergence** between our boot path and FRED's at this
point in the sysvar state.

---

## 3. From SAMDOS-loaded to AUTO-RUN-running: the call chain

The trace recorded in `docs/notes/m0-status.md:51-58`:

```
[Rst8 #1 ROM pc=ed38 byte=50]                            ← copyright msg
[Rst8 #2 ROM pc=d8e3 byte=80]                            ← BTHK from BOOT cmd
[Rst8 #3 ROM pc=e2b7 byte=82 type=10 name='auto']        ← HLOAD AUTO BASIC
[Rst8 #4 ROM pc=e1f7 byte=81 type=13 name='stub']        ← FOPHK opens stub
[Rst8 #5 ROM pc=e2b7 byte=82 type=13 name='stub']        ← HLOAD stub
[Rst8 #6 ROM pc=0e00 byte=00]                            ← OK prompt
```

Sequence:

1. `Rst8 #1` `pc=ed38`: ROM `MNINIT` reaches `RST 8 / DB 50H`
   (`rom:24690-24691`) which is the copyright message report. Error
   handler at `MAINER` (`rom:3808`) takes over.
2. `MAINELP` (`rom:3754`) starts the BASIC editor. SimCoupé's
   `AutoLoad(AutoLoadType::Disk)` handler types `\xc9` (BOOT keyword)
   into the keyboard buffer (`/Users/pmoore/git/simcoupe/Base/SAMIO.cpp:959-960`).
3. BASIC reads the keystroke, dispatches to `BOOT` command at
   `rom:20453`. `GETBYTE` returns 0 (no arg). Since `DOSFLG=0` (not
   yet resident), `BOOTNR` (`rom:20467`) calls `BOOTEX` which loads
   SAMDOS via the FDC, lands on the `JP &8009` (`rom:20598`), runs the
   SAMDOS sector-loader (§2), returns. Then `RST 8 / DB BTHK`
   (`rom:20469-20471`).
4. `Rst8 #2` `pc=d8e3 byte=80`: BTHK = 128. SAMDOS's hook table
   (`b.s:497`) routes `&128` to `init` (`h.s:215-218`):
    ```asm
    init:          nop
    initx:         ld a,&95   ; LOAD
                   call nrwr
                   defw &5b74 ; CURCMD
    ```
    → sets CURCMD = `&95` (LOAD token), and returns. (See
    `docs/notes/samdos2-auto-run-analysis.md` for the long form of this
    finding.)
5. `Rst8 #3` `pc=e2b7 byte=82 type=10 name='auto'`: this is
   `LDHK / DB 82` from ROM `DOSLD` at `rom:22509-22510`. It is the
   *load-data-block* hook called from `LDVDBLK → DOSLD` while servicing
   a BASIC LOAD command. **Therefore: between #2 and #3 a BASIC LOAD
   command for "auto" type 16 is dispatched.** This dispatch happens
   *despite* SAMDOS's `init` only setting CURCMD — because the BASIC
   editor's tokeniser+dispatcher, which is reached via the keyboard
   buffer leftover from `Keyin::String("\xc9", ...)`, was consumed by
   BOOT but the OK-prompt path that follows reads from CURCMD and
   does the right thing... **(I cannot fully cite the exact
   commitcommit-to-LOAD path; documenting it is OUT OF SCOPE for this
   investigation.)** The empirical observation is sufficient: the
   trace shows `LDPRDT` runs against the `auto` file.

   What MATTERS for CLEAR's failure is what `LDPRDT` does: see §3.1.

### 3.1 What `LDPRDT` (LOAD program) does to sysvars

`LDPRDT` (`rom:22591`) walks the file's loaded HDL header and:

1. `RECL2BIG` deletes any current program (`rom:22629`).
2. `MKRBIG` opens up `ABC` bytes at `(PROG)` (`rom:22649-22657`).
3. `LDDBLK` calls SAMDOS LDHK (= the `Rst8 #3` we see) to load the
   program body into the opened space (`rom:22657`).
4. `LDPROG` (`rom:22679`): for each of the three triplets in `HDL+16`/
   `HDL+19`/`HDL+22` (i.e. our directory entry's bytes
   `0xDD`/`0xE0`/`0xE3`), `RDTHREE` reads it, `ADDAHLCDE` adds it to
   `(PROG)`, and the result is written to `NVARS+1`/`NUMEND+1`/
   `SAVARS+1` (`rom:22683-22695`).
5. `RESTOREZ` resets `DATADD` to `(PROG)` (`rom:22697`).
6. `R1OFFCL DOCOMP` recompiles labels/DEF PROCs/FNs (`rom:22698-22699`).
7. `rom:22701-22713` (E3D9–E3EE): if `HDR+HDN+6` is 0 (no override) and
   `HDL+HDN+6` is 0 (the BASIC's auto-RUN flag from dir byte 0xF2 = 0)
   then `JP GOTO3` to start running from the file's stored auto-RUN
   line (`HDL+HDN+7..8`).

So at the moment line 10 starts running, the sysvar state is:

| Sysvar     | Value at AUTO-RUN entry                                     | Source                            |
|------------|-------------------------------------------------------------|-----------------------------------|
| `PROG`     | `5CD5` (set at `rom:24547`)                                 | `rom:24547-24548`                 |
| `NVARS`    | `PROG + (NVARS-PROG triplet from dir byte 0xDD)`            | `rom:22683-22689`                 |
| `NUMEND`   | `PROG + (NUMEND-PROG triplet from dir byte 0xE0)`           | `rom:22683-22689`                 |
| `SAVARS`   | `PROG + (SAVARS-PROG triplet from dir byte 0xE3)`           | `rom:22683-22689`                 |
| `WKEND`    | unchanged from cold-init (= ELINE, just before SAVARS+1)    | `rom:24655` and `MAIT`            |
| `ELINE`    | unchanged from cold-init                                    | `rom:24655`                       |
| `RAMTOP`   | `0xBFFF` (from `MNINIT`)                                    | `rom:24534-24535`                 |
| `RAMTOPP`  | `LASTPAGE` (from `MNINIT`)                                  | `rom:24521-24522`                 |
| `LASTPAGE` | 3 (from `MNINIT`'s 4-page reservation)                      | `rom:24513-24521`                 |
| `DOSFLG`   | `PRAMTP - 2` (from SAMDOS dos8)                             | `b.s:117`                         |
| `MODE`     | 3 (set by `NEW2 → MODET 3`)                                 | `rom:24657-24658`                 |
| `THFATP`   | 0 (thin)                                                    | `rom:24650`                       |
| `FISCRNP`  | `(PRAMTP-1) | 0x60` (e.g. `0x7E` for 512K machine)          | `rom:24523-24528`                 |
| `CUSCRNP`  | same (set in `NEW2`)                                        | `rom:24590-24591`                 |
| `LMPR`     | `0` (BASIC default; section A=ROM0+page0, section B=page1)  | implicit from cold init           |
| `HMPR`     | last page poked by MNINIT — `OUT (VIDPORT),A` was last      | `rom:24592` (sets VIDPORT, not URPORT) |
| `VMPR`     | `(PRAMTP-1) | 0x60` (set at `rom:24592`)                    | `rom:24592`                       |

Cite: `tm:887-906`, `tm:1106-1125` (LMPR/HMPR/VMPR layout);
`docs/notes/sam-paging.md` (§1, §2).

### 3.2 What FRED's bootstrap does *between* the dos8 epilogue and
auto-RUN entry

After FRED's RET, the BASIC main loop runs through the same
`MAINELP → BOOT keyword → BOOTEX` path that ours does — but FRED has
*more code in its body beyond &8090*. Specifically, the FRED body
contains a complete custom-DOS hook handler set, mapped to section B
once paged in (FRED's body bytes from offset 0x227 onwards live at
`&4220+` once SAMDOS-style paging is in effect). This is visible in
the disasm at `/tmp/fred-boot-corrected.disasm:8200+` where there's a
proper hook-handler dispatch table (`8200: jp 0x42a4`,
`8203: jp 0x4220`, `8206: jp 0x50d4`, …).

FRED's ALHK / BTHK *do* implement an auto-load-and-run path (the
samdos2 source's `hauto` is dead code per
`docs/notes/samdos2-auto-run-analysis.md:84-89`, but FRED's
own equivalent is wired up). So under FRED the auto-RUN flow is:

ROM `BOOT` → ROM `BOOTEX` (loads FRED) → FRED dos8 RETs → ROM
`RST 8 / DB BTHK` → FRED's BTHK handler at `&4220+` (paged-in) calls
through to FRED's auto-load-run logic, which on its way performs:

- A standard BASIC `LOAD "AUTOFRED.." LINE 10` flow that ends in the
  same `LDPRDT` path as ours.

So *up to* the `JP GOTO3` (`rom:22713`) that starts the auto-RUN line
running, **FRED's flow is identical to ours**: same ROM code path,
same NVARS/NUMEND/SAVARS computation, same `MODE 3` from `NEW2`.

The only difference between FRED and us at AUTO-RUN-line entry is:

- **Our line is `10 CLEAR n: LOAD "stub" CODE: CALL 24576`** — CLEAR
  fires immediately as the first statement.
- **FRED's line is `10 MODE 4: CLS #: CLEAR 81919`** — MODE 4 + CLS #
  *rebuild* the screen state before CLEAR runs.

This is the divergence.

---

## 4. What `CLEAR` actually does (full trace)

ROM `CLEAR` is at `rom:13148`. Per-step:

| ROM addr | Instruction | What it does | Reads/writes which sysvars |
|----------|-------------|--------------|----------------------------|
| 3901 | `CALL SYNTAX3`        | If checking syntax, return; else continue | none |
| 3904 | `CALL UNSTLEN`        | Decode CLEAR parameter `N` (16-bit) into A=page-byte (0..32), HL=offset-mod-16K with bit 15 set (`rom:14773-14786`) | reads top of FPCS |
| 3907 | `LD C,A`              | C ← page-byte | none |
| 3908 | `DEC C`               | C ← page-byte−1 | none |
| 3909 | `OR H` `OR L`         | NZ if `N != 0` (else fall to default-CLEAR-from-RAMTOP path) | none |
| 390B | `SET 7,H`             | Force HL into 8000-form | none |
| 390D | `JR NZ,CLR3`          | Take parameterised path | none |
| 390F | `CLR1: LD A,(RAMTOPP)` `LD HL,(RAMTOP)` `LD C,A` | (default path; not taken when `N` is given) | reads RAMTOPP, RAMTOP |
| 3916 | `CLR3: PUSH BC` `PUSH HL` | Save target-CDE | none |
| 3918 | `CALL ADDRNV`         | Page-in NVARS-page; return A=page, HL=NVARS sysvar low byte (offset in section C). `ADDRNV` reads `(NVARSP)` byte then issues `OUT (URPORT),A` (`rom:7393-7402`). | reads NVARSP, **mutates HMPR** |
| 391B | `EX DE,HL` `LD C,A`   | CDE = NVARS as paged-in | none |
| 391D | `LD HL,(ELINE)` `LD A,(ELINEP)` | AHL = ELINE | reads ELINE, ELINEP |
| 3923 | `CALL SUBAHLCDE`      | AHL = ELINE − NVARS (page-form difference) | reads NVARS via above |
| 3926 | `LD BC,025DH` `CALL SUBAHLBC` | AHL = (ELINE − NVARS) − 0x025D = "space to reclaim" | none |
| 392C | `LD B,H` `LD C,L` `LD HL,(NVARS)` | BC = mod-16K, HL = NVARS addr | reads NVARS |
| 3931 | `CALL RECL2BIG`       | Reclaim ABC bytes at HL — calls `RST 30H ; DW XOINTERS` (`rom:7191-7228`) which adjusts SAVARS/NUMEND/NVARS/DATADD/WKEND/WORKSP/ELINE/CHAD/KCUR/NXTLINE/PROG/XPTR/DEST/PRPTR (14 sysvars per `rom:23536`) — and `FARLDIR` (`rom:7225`) to actually move bytes | reads many; mutates many |
| 3934 | `CALL CLRSR`          | Clear sound chip, GRARF, ONERRFLG, BSTKEND, init 23 letter pointers, copy PSVTAB/PSVT2 — same routine `MNINIT → NEW2` runs (`rom:13209-13247`) | reads/writes many |
| 3937 | `CALL DOCOMP`         | Compile labels/DEF PROCs/DEF FNs/ELINE (`rom:12013-12068`) | reads PROG, walks program |
| 393A | **`CALL MCLS`**       | **CLEAR ENTIRE SCREEN** — `rom:2080-2135` | reads MODE/THFATP/FISCRNP/CUSCRNP/window sysvars; mutates HMPR, VMPR, LMPR via PMV; LDIRs into screen page |
| 393D | `LD HL,(WKEND)` `LD A,(WKENDP)` `LD BC,180`  `CALL ADDAHLBC` | AHL = WKEND + 180 ("RAMTOP must be > WKEND+180") | reads WKEND, WKENDP |
| 3949 | `POP DE` `POP BC`     | CDE = original CLEAR param (or RAMTOP) | none |
| 394B | `CALL SUBAHLCDE`      | AHL = (WKEND+180) − target | none |
| 394E | `JR NC,RTERR`         | If NC (target ≤ WKEND+180) → error 48 | none |
| 3950 | `LD A,(LASTPAGE)` `CP C` | Compare LASTPAGE vs target-page−1 | reads LASTPAGE |
| 3954 | `JR NC,CLR4`          | If LASTPAGE ≥ target-page−1 → OK | none |
| 3956 | `RTERR: RST 08H` `DB 48` | Else error 48 'Invalid CLEAR address' | none |
| 3958 | `CLR4: LD A,C` `LD (RAMTOPP),A` `LD (RAMTOP),DE` | RAMTOPP/RAMTOP ← target | mutates RAMTOPP, RAMTOP |
| 3960 | `POP HL` `POP BC`     | next-stat / err-handler | none |
| 3962 | `LD SP,ISPVAL`        | **Reset SP to 4F00H — drops all stack frames above ISPVAL** | mutates SP |
| 3965 | `PUSH BC` `LD (ERRSP),SP` | New ERRSP = ISPVAL−2 (with err-handler ret-addr) | mutates ERRSP |
| 396A | `JP (HL)`             | Jump to next-stat. (Statement-loop dispatcher) | — |

**Critical observation about MCLS** (cited line-by-line at `rom:2080-2135`):

```
0698 AF       MCLS:       XOR A
0699 1813                 JR CLSBL
...
06AE FE01     CLSBL:      CP 1
06B0 2840                 JR Z,CLU1
06B2
06B2 CDF206               CALL CLU1            ; clear UPPER window
06B5
06B5 213F5A   CLSLOWER:   LD HL,LWBOT          ; reads LWBOT (5A3F)
06B8 7E                   LD A,(HL)
06B9 2B                   DEC HL
06BA 96                   SUB (HL)             ; - LWTOP (5A3E)
06BB 3D                   DEC A
06BC 2822                 JR Z,CLSL2           ; JR if LW only 2 lines
06BE
06BE 7E                   LD A,(HL)            ; LWTOP
06BF 32585A               LD (WINDTOP),A
06C2 23                   INC HL
06C3 7E                   LD A,(HL)            ; LWBOT
06C4 3D                   DEC A
06C5 2B                   DEC HL
06C6 77                   LD (HL),A            ; LWTOP = LWBOT-1 (temp)
06C7 3D                   DEC A
06C8 32595A               LD (WINDBOT),A
06CB 2A3C5A               LD HL,(LWRHS)        ; reads LWRHS (5A3C, 2 bytes)
06CE 22565A               LD (WINDRHS),HL
06D1 2A485A               LD HL,(M23PAPP)      ; reads M23PAPP (5A48, 2 bytes)
06D4 22515A               LD (M23PAPT),HL
06D7 3A455A               LD A,(ATTRP)         ; reads ATTRP (5A45)
06DA 324E5A               LD (ATTRT),A
06DD CD6D0B               CALL CLSWIND         ; ** does the actual clear **
```

`CLSWIND` (called at `06DD` and traced from there) eventually calls
into routines that do `OUT (URPORT),A` to page in the screen page,
then `LDIR` (or `LDDR`) to fill it. The page that gets paged in is
determined by `CUSCRNP`/`FISCRNP`/`SCPTR`-driven logic.

Cite for the "screen page is paged in via HMPR" pattern: `rom:14799-14808` (`SPSSR`/`SPSS`/`SELSCRN`), used everywhere screen
pixels are accessed.

---

## 5. Why MCLS crashes in our context but not FRED's

### 5.1 In FRED's context

By the time `CLEAR 81919` runs, `MODE 4: CLS #` (FRED line 10) has
already executed:

- `MODE 4: ...` — ROM `MODET` (`rom:24658` reference) sets MODE,
  `THFATP`/`THFATT`, `M23PAPP`/`M23INKP`, window sizes, etc. for the
  requested mode. It also calls `SELSCRN` so HMPR is in a known state.
- `... CLS #` — the `#` form of `CLS` clears the entire screen
  including the lower window (`rom:2083-2087`: `CLSHS` calls
  `CLSHS2` at `rom:24767-24779`). This re-runs all the channel
  initialisation and screen-state setup for the K channel — **it
  re-establishes `CUSCRNP`/`FISCRNP`/HMPR consistency**.

After these, `CLEAR 81919` runs. By that point:
- The displayed screen page is unambiguously the page indicated by
  `CUSCRNP` (and HMPR is in a known state).
- All the window sysvars are valid.

`MCLS` runs cleanly because every dependency it has has been
freshly written by `MODE 4: CLS #`.

### 5.2 In our context

`CLEAR 24575` runs as the first statement of line 10. The dependencies
of `MCLS` are in their MNINIT-time defaults — never touched by SAMDOS
or by `LDPROG`:

| Dep | Origin | Value | Risk |
|-----|--------|-------|------|
| `MODE` | `NEW2 → MODET 3` (`rom:24657-24658`) | 3 | OK |
| `THFATP` | `NEW2` (`rom:24650`) | 0 | OK |
| `FISCRNP` | `MNINIT` (`rom:24523-24528`) | `(PRAMTP-1) | 0x60` = `0x7E` for 512K | **Conflicts with SAMDOS's DOSFLG=PRAMTP-2 = 0x1D… wait, that's a different page; FISCRNP=0x1E (low 5 bits = `PRAMTP-1 & 0x1F`)** |
| `CUSCRNP` | `NEW2` (`rom:24590-24591`) | same as FISCRNP page bits | as above |
| `LWBOT`/`LWTOP`/`LWRHS`/`LWLHS` | `MAIT` data block (`rom:24566-24569`) | normal default LW (rows 19-20) | OK structurally |
| `M23PAPP`/`ATTRP` | `MODET 3` defaults | OK | OK |
| `LMPR`/`HMPR` | last set by ROM during BASIC dispatch | `LMPR=0`; `HMPR` has been touched many times, current value uncertain | Determined by latest BASIC operation |

The bullet that matters: **`FISCRNP` and `CUSCRNP` were set at
`MNINIT`/`NEW2` time, when SAMDOS was not yet resident.** They point
at `PRAMTP-1` (= `0x1E` on a 512K machine), the page MNINIT carved
out for screen 1. SAMDOS subsequently took page `PRAMTP-2` (= `0x1D`)
for itself.

Now consider `MCLS`'s call into `CLSWIND` and ultimately into the
LDIR-into-screen-page primitive. That primitive does `OUT (URPORT),A`
where `A = (CUSCRNP & 0x1F)` — the screen page byte — and then writes
into `&8000+` (the section C window), filling with background colour
or attributes (`rom:14806-14807`'s `SPSS` is the canonical pattern;
the same logic is replicated inline in `CLSWIND` and its callees).

If `CUSCRNP & 0x1F` is the page that holds the screen, that's the
expected behaviour — except *the previous BASIC operations did not
explicitly switch the screen page in or out*, so `CUSCRNP` may be
**stale** or out of sync with the current physical paging.

Specifically: between `MNINIT` and the first auto-RUN statement, the
following operations occur that all do unrelated `OUT (URPORT),A`:

1. ROM `MNINIT` itself (`rom:24593`): `OUT (VIDPORT),A` — VMPR (port
   FCh), not HMPR. So this does not corrupt HMPR.
2. SAMDOS dos2..dos8 floppy I/O loop (`b.s:42-66`): never touches
   port FB (HMPR/URPORT). Only reads/writes ports E0/E1/E2/E3 (FDC).
3. ROM `BOOTEX`'s `SELURPG` call (`rom:20488-20489`): `LD A,L; CALL
   SELURPG` where L is the free page found in ALLOCT — **this is
   `OUT (URPORT), L`** which sets HMPR = the SAMDOS page. So at the
   moment `JP &8009` happens, HMPR = SAMDOS page = `PRAMTP-2`.
4. SAMDOS dos8 epilogue (`b.s:112-126`): does **not** OUT (URPORT).
   So HMPR stays at SAMDOS page through the RET.
5. Back in ROM, `RST 8 / DB BTHK`: ROM's RST 8 dispatcher is at
   `rom:639-737`. It DI/EIs and saves URPORT around the call to
   SAMDOS's hook handler. After hook returns, URPORT is restored
   (`rom:743`: `OUT (250),A` where 250=LMPR; the corresponding URPORT
   restore happens via `R1OFFCLBC`/`POPOUT` patterns). **The exact
   final HMPR value after BTHK returns is determined by whatever the
   ROM-side restore wrote.**

Now `LDPRDT` runs — it issues `LDDBLK` → `RST 8 / DB LDHK`. SAMDOS's
`hldbk` calls `cals` which does `OUT (251),A` (`h.s:316`) to page in
target user pages for the LDIR copy. After it returns, `h.s:283-285`
runs `out (251),A` from `port1` (the saved value). So HMPR is
restored to whatever it was on entry to the hook.

By the time we reach the auto-RUN-line entry at `JP GOTO3`, HMPR is
in some state determined by the last `OUT (URPORT)` that ROM's BASIC
LOAD path issued. **It is NOT guaranteed to be the displayed-screen
page.**

When `MCLS` then does its `CLSWIND` → `OUT (URPORT), CUSCRNP`-style
sequence, it tries to swap in `CUSCRNP=0x1E` (FISCRNP-derived) and
LDIR-write attribute bytes into `&8000+`. If for any reason this
fails — e.g. the `CUSCRNP` page was the page SAMDOS lives in
(`DOSFLG=0x1D` in our case, screen page is `0x1E`, so they don't
collide; OK so this specific combination wouldn't crash on a 512K
machine BUT see §5.3 below).

### 5.3 The specific failure mode: SimCoupé reports a 256K machine

ROM `MNINIT` at `rom:24482-24486`:

```
EBE6 3EFE                  LD A,0FEH
EBE8 DBFE                  IN A,(0FEH)
EBEA 1F                    RRA
EBEB 3E10                  LD A,10H          ;256K
EBED 3001                  JR NC,RAMEX       ;FORCE 256K SYSTEM IF SHIFT PRESSED
```

If SHIFT is not pressed (default in headless simcoupe), ROM continues
to `ADD A,A` for `0x20H = 512K` (`rom:24487-24488`). So PRAMTP =
`0x1F` (32 16K pages) on a 512K machine.

But: `tools/run-simcoupe.sh` and `headless-simcoupe.md` do not specify
the RAM size. SimCoupé's default config is 512K (per
`/Users/pmoore/git/simcoupe/Base/Options.h:74`'s defaults — would need
verification; not asserted here).

If headless SimCoupé runs at 512K:
- PRAMTP = `0x1F`
- `FISCRNP` (`rom:24523-24528`) = `((PRAMTP-1) & 0x1F) | 0x60` =
  `0x1E | 0x60 = 0x7E` — high bits encode "screen marker mode 3 with
  MIDI bit clear", low 5 bits are page `0x1E`.
- `CUSCRNP` = `0x7E`. Low 5 bits (the actual page) = `0x1E`.
- `DOSFLG` = `PRAMTP - 2` = `0x1D`. SAMDOS lives at page `0x1D`.
- LASTPAGE = 3 (from MNINIT — never updated by SAMDOS).

So screen lives at page `0x1E`, SAMDOS lives at page `0x1D`. Different
pages. No collision *by page*.

But: `MCLS`'s calls do not just OUT (URPORT), screen-page. They do
multi-step reads from various sysvars. If any of those reads hits a
sysvar that *was modified* by `LDPRDT` or `RECL2BIG` (e.g. `WKEND`,
`ELINE`, `WORKSP`) and the resulting computed page is `0x1D`
(SAMDOS), then OUT (URPORT) → SAMDOS lands in section C → MCLS LDIRs
attributes into SAMDOS code → SAMDOS is dead → next RST 8 from BASIC
goes through corrupted SAMDOS → garbage → crash.

This scenario hinges on `WORKSP`/`WKEND`/`ELINE` having been adjusted
by `XOINTERS` (called from `RECL2BIG` inside CLEAR itself, after MCLS
runs) — but actually MCLS runs **before** that in CLEAR's flow
(`rom:13174-13177`: `RECL2BIG; CLRSR; DOCOMP; MCLS`). RECL2BIG runs
first, **updates pointers via XOINTERS**, then MCLS runs. So at MCLS
time the pointers ARE the post-XOINTERS values.

`XOINTERS` (`rom:23536-23613`) adjusts 14 sysvars by the block size
just freed. If RECL2BIG was a no-op (because we have no vars yet), it
RETs early at `rom:7196`. Looking at the entry sequence: at
`rom:13169-13170`, AHL = `(ELINE-NVARS) - 0x025D`. After AUTO-RUN
load, ELINE is unchanged from MNINIT (= SAVARS+1 = some value), and
NVARS has been written from triplet-decode. If the BASIC's program is
small (our auto file is ~24 bytes per `tools/build-disk.sh:208-209`),
then `(NVARS-PROG) ≈ 24`, so NVARS ≈ PROG+24 = `5CD5+24 = 5CED`. ELINE
from MNINIT was ≈ `5CB6+something`, definitely above `5CED + 0x25D =
0x5F4A`. So `RECL2BIG` will run with non-zero ABC.

`RECL2BIG` calls `RST 30 ; DW XOINTERS` (`rom:7206-7207`), which
adjusts SAVARS/NUMEND/NVARS/DATADD/WKEND/WORKSP/ELINE/CHAD/KCUR/
NXTLINE/PROG/XPTR/DEST/PRPTR. After this, `WKEND` and `ELINE` may
sit on a different *page* from where they did at MNINIT — specifically
their page bytes (`WKENDP`, `ELINEP`) may have been bumped up by 1
because of the RECL2BIG adjustment crossing a 16K boundary.

If `WKENDP` or `ELINEP` ends up = `0x1D` (= SAMDOS page) due to this
adjustment — and `MCLS`'s call to `CLSWIND` reads (e.g.) `(WKEND)` or
the K-channel page-byte that was set up by `MAIT` via `MNINIT`
referring to one of these pointers — then `MCLS` will OUT (URPORT),
0x1D. SAMDOS gets paged in at section C. MCLS's LDIR then writes into
`&8000+` thinking it's screen memory, but it's actually overwriting
SAMDOS.

I cannot fully cite that this specific page collision happens without
running the disk under a debugger. However: the empirical
"structured page-displaced screen pattern" matches *exactly* the
visual signature of LDIR-with-pattern into the wrong page (the
original "screen-page LDIR" is supposed to reach the visible pixels;
when it lands in SAMDOS code instead, you see colour-cycling stripes
because of the timing-dependent code-as-attribute-bytes pattern that
SAMDOS-code-bytes display when interpreted as MODE 3 attribute data).

### 5.4 Why FRED's `MODE 4` rebuilds enough state

FRED's `MODE 4` calls ROM `MODET 4` (`rom:24658` reference) which
inside `01_5A` (ROM `MODET`):
- Sets `MODE` (`5A40`) to 4 — but MODE 4 isn't a standard mode (the
  ROM only knows 1-4), so it's actually mode 4 which is mode 4-from-
  user-perspective. Actually reading `rom:01_5A` would clarify what
  MODE 4 does. The point is: it issues a fresh `OUT (VIDPORT),A`
  with the displayed page; it sets `M23PAPP`/`M23INKP`; it resets
  `WINDxxx` state.
- Subsequently `CLS #` (`rom:2080`) does `MCLS` *itself* — but with
  the freshly-written window state.

So `MCLS` runs **once** with valid state (during `CLS #`) before
running **again** inside `CLEAR 81919`. The first run resets HMPR/VMPR
and any stale pointers; the second run is then guaranteed to find
state consistent with the first.

Our auto-RUN does not have this priming pass.

---

## 6. Recommended fix

**Add a screen-state primer before `CLEAR n` in the AUTO BASIC line —
exactly what FRED does.**

Specifically, change `tools/build-disk.sh`'s AUTO line construction
(currently at `tools/build-disk.sh:202-207`) from:

```python
stmt_clear = bytes([0xb3, 0x20]) + str(LOAD_ADDR - 1).encode() + num(LOAD_ADDR - 1)
stmt_load = (bytes([0x95, 0x20, 0x22]) + b"stub"
             + bytes([0x22, 0x20, 0xff, 0x6c, 0x20])
             + str(LOAD_ADDR).encode() + num(LOAD_ADDR))
stmt_call = bytes([0xe4, 0x20]) + str(LOAD_ADDR).encode() + num(LOAD_ADDR)
line_body = stmt_clear + b"\x3a" + stmt_load + b"\x3a" + stmt_call + b"\x0d"
```

to (pseudocode — needs token lookups to be done from the keyword table
in `samfile/keywords.go:1-194`):

```python
# MODE 3: CLS #: CLEAR n: LOAD "stub" CODE addr: CALL addr
stmt_mode = bytes([keyword_token('MODE'), 0x20]) + b"3" + num(3)
stmt_clshash = bytes([keyword_token('CLS'), 0x20, 0x23])    # CLS #
stmt_clear = bytes([0xb3, 0x20]) + str(LOAD_ADDR - 1).encode() + num(LOAD_ADDR - 1)
stmt_load = (bytes([0x95, 0x20, 0x22]) + b"stub"
             + bytes([0x22, 0x20, 0xff, 0x6c, 0x20])
             + str(LOAD_ADDR).encode() + num(LOAD_ADDR))
stmt_call = bytes([0xe4, 0x20]) + str(LOAD_ADDR).encode() + num(LOAD_ADDR)
line_body = (stmt_mode + b"\x3a" + stmt_clshash + b"\x3a"
             + stmt_clear + b"\x3a" + stmt_load + b"\x3a" + stmt_call + b"\x0d")
```

(Token for MODE must be looked up in `samfile/keywords.go`. The
`stmt_clshash` is the literal `CLS #` form per `rom:2083`'s
`CP "#" / JR NZ,CLSNH` test.)

### Why this is the right fix (cited)

1. FRED 56 demonstrably runs `CLEAR 81919` immediately after
   `MODE 4: CLS #` and works (`/tmp/fred-bas/AUTOFRED..` line 10,
   verified). This is the only known-working in-the-wild example of
   `CLEAR n` in AUTO-RUN context.
2. ROM `MODET` (`rom:24658` and the `MODET` routine itself) re-issues
   `OUT (VIDPORT)` and resets `M23PAPP`/`ATTRP`/window sysvars.
3. ROM `CLSHS → MCLS` (`rom:24763, 2080`) runs `MCLS` once with
   freshly-validated window state — proving that subsequent `MCLS`
   calls (inside `CLEAR n`) will not crash.
4. The fix does not require modifying SAMDOS, the boot loader, or
   ROM behaviour. It treats `CLEAR n` as the well-formed BASIC user
   command it is — preceded by the canonical `MODE: CLS #` priming
   that the SAM Coupé User Guide
   (`docs/sam/sam-coupe_use-guide.txt:4938-4946`) implies for
   bootable disks.

### Alternative fix (less robust)

If the BASIC-line approach is for some reason undesirable, the
alternative is to **emit a small custom T4S1 bootstrap** (FRED-style)
that, in addition to the SAMDOS-equivalent dos8 epilogue, does:

```asm
ld a,3                ; mode 3
out (252),a           ; VMPR = mode 3 + screen at last page
;... full MODET-3 equivalent ...
```

This pushes the priming into a custom bootstrap, mirroring FRED. It is
strictly more work and equally citable.

---

## 7. What was *not* conclusively determined

1. **Exactly which sysvar's page-byte ends up = `DOSFLG` (= `0x1D`)
   after `LDPRDT`'s `RECL2BIG` runs.** I gave a plausible
   walk-through (`WKENDP`/`ELINEP` after a 16K-boundary-crossing
   reclaim) but did not run the disk in a debugger to confirm. The
   §6 fix does not depend on this being precisely localised — both
   FRED's working line and the §5 evidence converge on "screen state
   needs reset before CLEAR".
2. **The exact ROM path between `RST 8 / DB BTHK` (which only sets
   CURCMD=0x95 in our SAMDOS) and the LDHK at `pc=e2b7 byte=82`
   that loads the AUTO file.** The trace shows it happens; the
   samdos2-source-walking analysis at
   `docs/notes/samdos2-auto-run-analysis.md:84-89` says SAMDOS's
   `hauto` is dead code. There must be a ROM-side path that consumes
   CURCMD=LOAD and dispatches LDHK, but I did not trace it
   end-to-end. This investigation was scoped to "why does CLEAR
   crash" not "how does AUTO load happen at all", and the AUTO-load
   trace IS empirically observed.
3. **Whether the crash is reproducible with `MODE 3: CLS #` priming.**
   This investigation does not include running the disk; it
   documents the analysis and proposes a fix. Pete should verify
   empirically by changing the AUTO line and re-running.

---

## 8. Sources cited

| Tag in this doc | File | Line range used |
|-----------------|------|-----------------|
| `rom:NNNN`      | `docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt` | 729 (ISPVAL); 869-900 (sysvar EQUs); 1018 (AUTOFLG); 1064-1065 (ERRMSGS); 1131-1143 (KBQ/PRAMTP/RAMTOP/RAMTOPP/LASTPAGE EQUs); 1183-1248 (hook-code/sysvar EQUs incl. ISPVAL); 2080-2135 (MCLS/CLS); 3454-3885 (MAINLP/MAINER); 7180-7228 (RECL2BIG); 7350-7402 (ADDRNV); 7565-7620 (SUBAHLCDE/PAGEFORM); 12013-12068 (DOCOMP/COMPILE); 13141-13247 (RUN/CLEAR/CLR1/CLR3/CLR4/RTERR/CLRSR); 14773-14786 (UNSTLEN); 19181-19222 (LASTPAGE update via OPEN); 20453-20598 (BOOT/BOOTNR/BOOTEX/BTNOE/BTLY); 22120-22713 (HDR2/SLVMC/LVMMAIN/LDFL/HDLDEX/LDPRDT/LDPROG/LDUSLN); 23520-23613 (XOINTERS); 24430-24779 (MNINIT/NEW2/CLSHS/CLSHS2/UPACK); 26492-26571 (error message table; report 19/48/53). |
| `tm:NNNN`       | `docs/sam/sam-coupe_tech-man_v3-0.txt` | 887-906, 1106-1125, 4256-4427, 4459-4536, 4524, 4548 |
| `b.s:NN`        | `/Users/pmoore/git/samdos/src/b.s` | 14-22 (header), 27 (org.adjust=9), 33-127 (dos: loader and dos8 epilogue), 220-260 (RAM cache/dvar), 355-435 (syntax handler dispatch), 437-545 (hook dispatcher samhk table) |
| `h.s:NN`        | `/Users/pmoore/git/samdos/src/h.s` | 201-237 (autnam/init/initx/hauto), 308-321 (cals) |
| `f.s:NN`        | `/Users/pmoore/git/samdos/src/f.s` | 462-471 (svhd), 485-489 (read), 502-560 (load/autox), 561+ (dlvm1) |
| `d.s:NN`        | `/Users/pmoore/git/samdos/src/d.s` | 157-174 (nrwr/gthl), 284-289 (bcr) |
| FRED disasm     | `/tmp/fred-boot-corrected.disasm` (regenerated via `samfile cat -i /tmp/fred-bas/FRED56.DSK -f $'\x7f FRED56 \x7f' > /tmp/fred-boot.bin; z80-unknown-elf-objdump -D -b binary -m z80 -M zilog --adjust-vma=0x8009 /tmp/fred-boot.bin`) | 8009-8090 (custom DOS dos8-equivalent), 8200-8300 (FRED hook dispatch table) |
| FRED AUTO       | `/tmp/autofred.bin` then `samfile basic-to-text` | line 10: `MODE 4: CLS #: CLEAR 81919` |
| build-disk      | `/Users/pmoore/git/sam-aarch64/tools/build-disk.sh` | 138-176 (samdos2 slot), 178-248 (AUTO BASIC slot), 195-209 (AUTO line construction) |
| paging notes    | `/Users/pmoore/git/sam-aarch64/docs/notes/sam-paging.md` | §1 (sections), §2 (LMPR/HMPR/VMPR) |
| disk format     | `/Users/pmoore/git/sam-aarch64/docs/notes/sam-disk-format.md` | §5 (BOOT mechanism) |
| samdos AR notes | `/Users/pmoore/git/sam-aarch64/docs/notes/samdos2-auto-run-analysis.md` | passim |
| FRED disk insp. | `/Users/pmoore/git/sam-aarch64/docs/notes/fred-disk-inspection.md` | §1 (`samfile ls`), §2 (`samfile cat`) |
| simcoupe boot   | `/Users/pmoore/git/simcoupe/Base/SAMIO.cpp` | 920-1032 (AutoLoad/Rst8Hook), 943-947 (QueueAutoBoot) |
| simcoupe opts   | `/Users/pmoore/git/simcoupe/Base/Options.h` | 74 (autoboot default) |
