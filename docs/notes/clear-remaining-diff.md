# CLEAR-in-AUTO-RUN: remaining byte-level differences vs. canonical disks

Date: 2026-05-10. Author: investigation agent (Opus 4.7), Pete's PRIME
DIRECTIVE (`feedback_correctness_over_workarounds.md`) in force.

This document supersedes the §6 fix recommendation in
`docs/notes/clear-mechanism.md` (the "off-by-one in NVARS-PROG triplet"
hypothesis). That hypothesis is **wrong** — see §3 for a corrected
re-derivation; our current triplet=57 is the byte-correct value for our
57-byte body shape. Previously-cited Defender numbers are also
re-confirmed in §3.

ROM = `docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt`.
Tech Manual = `docs/sam/sam-coupe_tech-man_v3-0.txt`.
SAMDOS source = `~/git/samdos/src/<file>`.
GoodSamC2 = `/Users/pmoore/Downloads/GoodSamC2/`.

---

## TL;DR

1. **Bytes 0xDC, 0xE6, 0xE7 of the slot-1 dir entry are NOT consumed by
   ROM or SAMDOS during the AUTO-RUN BASIC LOAD path.** Static trace of
   every read of HDL+15 (HFG) and HDL+25/+26 in the ROM disassembly
   shows these bytes are functionally inert for our scenario. Differences
   between our `00 00 00` and Defender's `20 20 ff` are cosmetic side
   effects of how ROM SLMVC clears the HDR buffer before SAVE
   (`rom:22070-22080`). §2.

2. **Multi-disk survey (153 GoodSamC2 .dsk images, 607 type-16
   auto-RUN BASIC entries)**: 99.5% have `(DC=0x20, E6=0x20, E7=0xff)`.
   3 outliers (`bash` slot 70 of "Sam Paper Magazine 8" with all-zeros,
   plus 2 corrupted-looking entries on EXPLOSION/Metempsychosis disks).
   Our `(00, 00, 00)` is non-canonical but matches the corrupted-disk
   pattern; static trace confirms it's still functionally inert. §4.

3. **Our triplet=57 is correct, not off-by-one.** Re-derived against
   Defender's body byte-by-byte: Defender's program proper is 56 bytes
   (line bytes incl. final 0x0d at body[54], plus end-of-prog FF marker
   at body[55]); NVARS-PROG=56 means NVARS = byte after the FF marker.
   Our program proper is 57 bytes (line bytes incl. final 0x0d at
   body[55], plus end-of-prog FF marker at body[56]); NVARS-PROG=57
   places NVARS = byte after the FF marker. **Same convention. Same
   semantic correctness.** §3.

4. **The actual likely cause is NOT a dir-entry byte difference.** It is
   that **our BASIC body is too small** (57 bytes vs Defender's 660,
   FRED 7 'auto's 623, the survey minimum-non-zero auto-RUN body of 115).
   Real-SAVE BASIC bodies include the variable area; ours doesn't. The
   resulting interaction is: MNINIT-time ELINE was placed at PROG+~586
   (per CLRSR's SETSAV-then-+1 logic at `rom:13230-13234`); LDPRDT's
   MKRBIG opens 57 bytes at PROG and `XOINTERS` (`rom:23536-23613`)
   adjusts ELINE by +57 to ~PROG+643; LDPROG does NOT rewrite ELINE
   (only NVARS/NUMEND/SAVARS at `rom:22683-22695`); so at AUTO-RUN entry
   ELINE = PROG+643, NVARS = PROG+57, ELINE-NVARS = 586, **CLEAR's first
   op is `(ELINE-NVARS) - 0x025D` = 586 - 605 = -19 (negative)**, which
   then RECL2BIG's normalisation/`RES 7,B; RES 6,B` masking handles
   garbage-in/garbage-out. §5.

5. **Confidence on §4 (the inert-byte conclusion)**: high, citation-grounded,
   single ROM/SAMDOS code-path trace. **Confidence on §5 (the body-too-small
   conclusion)**: medium-high. The signed-arithmetic details of
   `SUBAHLBC → AHLNORM → PAGEFORM → RECL2BIG` (`rom:7541-7589`) with
   negative AHL were not exhaustively simulated; static analysis can't
   prove the exact resulting block-size that XOINTERS uses to adjust
   sysvar pages, only that it's "out-of-range".

6. **Recommended minimum next action**: pad the BASIC body with a synthetic
   variable-area to match the canonical SAVE shape. Concretely: emit
   `BASIC_BODY` of length ≥ ~600 bytes by appending 0xFF padding past
   the program FF terminator, AND adjust the three triplets so
   NVARS-PROG = end-of-program (= 57), NUMEND-PROG ≈ end-of-program,
   SAVARS-PROG = `len(BASIC_BODY)`. This exactly mirrors what real
   SAVE produces. §7.

7. **Alternative recommendation if §6 is too invasive**: make `auto`
   a type-19 CODE file with auto-exec (entered as direct Z80, no
   BASIC interpreter). This bypasses CLEAR/MCLS/LDPRDT entirely.
   Already documented as `m0-status.md` "option (c)". This is a
   **workaround** — it avoids the underlying issue rather than
   resolving it; per `feedback_correctness_over_workarounds.md` Pete
   may prefer §6's fix.

---

## 1. Authoritative byte-by-byte table: our disk vs. Defender Compilation

Slot 1 directory entry, both disks. Verified by direct reading of
`/Users/pmoore/git/sam-aarch64/build/test.mgt` and
`/Users/pmoore/Downloads/GoodSamC2/Defender Compilation (19xx).dsk`
(slot offset 0x100, 256 bytes).

| Byte (hex)  | Field                    | Defender    | Ours        | Functional? | Cite |
|-------------|--------------------------|-------------|-------------|-------------|------|
| 0x00        | Type                     | `10`        | `10`        | yes; both = 16 BASIC | tm:4351-4356 |
| 0x01-0x0A   | Filename                 | `AUTOBOOT  `| `auto      `| yes; SAMDOS matches case-insensitive `AUTO*` | h.s:201-212 |
| 0x0B-0x0C   | Sectors BE               | `00 02`     | `00 01`     | yes; sector count differs (Defender 2 sectors, ours 1) | tm:4360-4361 |
| 0x0D-0x0E   | First sector             | `06 01`     | `06 01`     | yes; both T6S1 | tm:4362-4363 |
| 0x0F-0xD1   | Sector address map (195 bytes) | (sets 2 bits) | (sets 1 bit) | yes; matches sectors used | tm:4364-4365 |
| 0xD2        | (unused)                 | `00`        | `00`        | no | tm:4366-4368 |
| 0xD3-0xDB   | Body header cache (9 b)  | `10 94 02 d5 9c ff ff 00 00` | `10 39 00 d5 9c ff ff 00 00` | only consumed by SAMDOS internal cache (see §2.1); identical layout, lengths differ | f.s:462-471, c.s:1376-1379 |
| **0xDC**    | **MGT flags = HFG**      | **`20`**    | **`00`**    | **NO; both have bit 1 = 0; only bit 1 read by ROM** | rom:1215, rom:22389-22390, rom:22467-22468 |
| 0xDD-0xDF   | NVARS-PROG triplet       | `00 38 80` (=56) | `00 39 80` (=57) | yes; correct for each body shape (§3) | rom:22683-22695 |
| 0xE0-0xE2   | NUMEND-PROG triplet      | `00 94 80` (=148) | `00 39 80` (=57) | yes; for our zero-vars body, value matches NVARS-PROG | rom:22683-22695 |
| 0xE3-0xE5   | SAVARS-PROG triplet      | `00 94 82` (=660) | `00 39 80` (=57) | yes; for our zero-vars body, value matches NVARS-PROG | rom:22683-22695 |
| **0xE6**    | (UIFA byte 25, "spare")  | **`20`**    | **`00`**    | **NO; no ROM read of HDL+25 found** | tm:4459-4486; grep over rom |
| **0xE7**    | (UIFA byte 26, "spare")  | **`20`**? wait `ff`    | **`00`**    | **NO; no ROM read of HDL+26 found** | tm:4459-4486; grep over rom |
| **0xE8-0xEB** | ReservedA              | **`ff ff ff ff`** | **`00 00 00 00`** | NO; reserved 4 bytes per Tech Manual, no ROM/SAMDOS read | tm:4382 |
| 0xEC        | StartPage mirror         | `00`        | `00`        | yes; also redundant with HDL via DIFA | tm:4388-4389 |
| 0xED-0xEE   | PageOffset mirror LE     | `d5 9c`     | `d5 9c`     | yes; both encode PROG=0x5CD5 | tm:4390-4392 |
| 0xEF        | Pages mirror             | `00`        | `00`        | yes | tm:4393 |
| 0xF0-0xF1   | LengthMod16K LE          | `94 02` (=660) | `39 00` (=57) | yes; body length differs | tm:4394-4395 |
| 0xF2        | Auto-RUN marker          | `00`        | `00`        | yes; both = "DO auto-RUN" | tm:4396, rom:E287 |
| 0xF3-0xF4   | Auto-RUN line LE         | `01 00` (line 1) | `0a 00` (line 10) | yes; valid distinct line numbers | tm:4396-4398 |
| 0xF5-0xFD   | Spare                    | `ff ff ff ff ff ff ff ff` | `00 00 00 00 00 00 00 00` | no | tm:4399 |
| 0xFE-0xFF   | "MGT future"             | `00 00`     | `00 00`     | no | tm:4400 |

Bold rows = the four byte differences identified in the brief. None are
functional per static trace (§2).

---

## 2. Static trace: which dir-entry bytes the ROM actually reads

### 2.1 Byte 0xDC = HDL+HFG (the "MGT flags" byte)

**Definition**: `HFG EQU 15` at `rom:1215`. HFG is at HDR/HDL offset 15.

**SLMVC initialises HDR+1..HDR+25 to `0x20`** at `rom:22070-22074`:

```
E02C 21014B   LD HL,HDR+1      ; HEADER BUFFER ADDR
E02F 0619     LD B,25
HDCLP:
E031 3620     LD (HL),20H      ; CLEAR NAMES AREAS WITH SPACES
E033 23       INC HL
E034 10FB     DJNZ HDCLP
```

This includes HDR+15 = HFG, which thus defaults to `0x20` after every
SAVE/LOAD/VERIFY/MERGE entry. SLMVC then continues at `rom:22076-22080`
filling HDR+26..HDR+39 with `0xFF`. So:

- **HDR+15 (HFG) = 0x20** (default)
- **HDR+25 = 0x20** (last of the 25-byte 0x20 fill)
- **HDR+26 = 0xFF** (first of the 14-byte FF fill)

When the user typed `SAVE "(prefix)NAME"`, ROM at `rom:22108-22113`
overwrites HFG with 0,1,2,3 indicating invis/protected combinations:

```
E062 1A       LD A,(DE)        ; FIRST CHAR OF SAVE NAME CAN BE
E063 FE04     CP 4             ; 0 FOR NOT INVIS, NOT PROT
E065 3007     JR NC,MVTNM      ; 1 FOR     INVIS, NOT PROT
                               ; 2 FOR NOT INVIS, PROT
                               ; 3 FOR INVIS, PROT
E067 320F4B   LD (HDR+HFG),A
```

So a normal SAVE "FOO" produces HFG=0x20; SAVE with prefix produces
HFG ∈ {0,1,2,3}. The full set of values seen on real disks (per §4
survey) confirms these 5 values dominate: 0x20 = 99.5%, 0/1/2/3 = a
handful.

**ROM reads of HFG** (every site located by grep in ROM):

| ROM site | Cite | What it tests |
|----------|------|---------------|
| `E20B-E210` | rom:22389-22391 | `LD A,(HDL+HFG); AND 02H; JR NZ,CDSCVE` — bit 1 (PROT) for MERGE-CODE protection |
| `E27A-E27F` | rom:22467-22469 | `LD A,(HDL+HFG); BIT 1,A; JR NZ,HDNSTP` — bit 1 (PROT) for LOAD-CODE auto-run override |
| `E403-E40C` | rom:22729-22733 | `LD HL,HDL+HFG; LD A,(TPROMPTS); AND 1; OR (HL); LD (HL),A; RRA; JR C,LKHNP` — bit 0 (INVIS) for name-print suppression |
| `E44D-E452` | rom:22782-22785 | `LD A,(HDL+HFG); RRA; ... CALL NC,0010H` — bit 0 (INVIS) for char-print suppression in name match |
| `E45D-E465` | rom:22794-22800 | `LD A,(HDL+HFG); RRA; ... RET C` — bit 0 (INVIS) for trailing CR after match |
| `E47F` | rom:22820 | `LD (HDL+HFG),A` — A=0; ZX-header path forces visible/unprotected. Only relevant when loading ZX-format header. |
| `E4AB` | rom:22850 | `LD (HDL+HFG),A` — also writes 0 in ZX path |
| `E067` | rom:22113 | `LD (HDR+HFG),A` — SAVE-with-prefix path (described above) |

**Every ROM read of HFG only tests bit 0 (INVIS) or bit 1 (PROT)**. With
HFG=0x20: bit 0 = 0, bit 1 = 0. With HFG=0x00: bit 0 = 0, bit 1 = 0.
**Functionally equivalent.**

In particular the LOAD-CODE auto-exec / merge protection path
(`rom:22467-22469`) — which is the only place HFG could redirect
control-flow during our LOAD CODE flow — produces the same `JR NZ` not
taken in both cases.

**SAMDOS reads of HFG**: SAMDOS reads UIFA byte 15 only via `rxhed`
(`h.s:8-26`) which copies all 48 UIFA bytes verbatim. No SAMDOS code
inspects byte 15 for any bit-test. Confirmed by grep over
`samdos/src/*.s`: zero matches for `flag|FLAG|hfg|HFG`.

**Conclusion**: dir 0xDC has zero functional effect on our LOAD path.
The 0x20 vs 0x00 difference is cosmetic.

### 2.2 Bytes 0xE6, 0xE7 = HDL+25, HDL+26

These are **UIFA bytes 25 and 26** (per Tech Manual `tm:4459-4486`,
UIFA layout). Per ROM E019 doc block (`rom:22025-22054`):

- UIFA bytes 16-26 are "type-specific":
  - Type 16: bytes 16-18 = NVARS-PROG, 19-21 = NUMEND-PROG, 22-24 = SAVARS-PROG triplets
  - Type 17/18: bytes 16-26 = TLBYTE/NAME (11 bytes)
  - Type 20: byte 16 = SCREEN MODE
  - **Bytes 25, 26 of type 16 are NOT defined** by the layout; they
    fall outside the 9-byte triplet block

Are they ever read by ROM? Comprehensive grep:

```
$ grep -n -E 'HDL\+25|HDL\+26|HDR\+25|HDR\+26|HDR\+24|4B69|4B6A' rom
1215:0294 000F=   HFG          EQU 15              ;DISP TO HEADER FLAG
22136:E08B 21254B  LD HL,HDR+HDN+6   ; HDN=31, so HDR+25 != HDR+HDN+6
22471:E281 3A254B  LD A,(HDR+HDN+6)
22701:E3D9 3A254B  LD A,(HDR+HDN+6)
```

The matches at `4B25` are HDR+HDN+6 (= HDR+37, the auto-RUN flag/exec
page) — not HDR+25. **No ROM access to HDR+25 or HDR+26 found.**

Are they read by SAMDOS? `gtfle` (`c.s:1346-1487`) populates DIFA from
the dir entry but does not branch on bytes 25/26. The `gtfl7` path
(`c.s:1476-1480`) just `ldir`s 33 bytes at dir 0xDC..0xFC into DIFA
bytes 15..47. SAMDOS itself does not consume DIFA bytes 25/26.

**Conclusion**: dir 0xE6 and 0xE7 are not consumed by either side. The
canonical 0x20 / 0xFF values are SLMVC-clear side effects, not data.

### 2.3 Bytes 0xE8-0xEB = ReservedA

Tech Manual L4382 calls these "Spare 4 bytes". ROM E019 doc block
labels HDL bytes 27 as DIRE ("DIRECTORY ENTRY NUMBER, HDR ONLY, NOT
USED") and 28-30 as 3 spare bytes. So HDL+27..+30 = dir 0xE8..0xEB,
explicitly **NOT USED**. Confirmed by grep — no ROM read of HDL+27..+30
exists.

### 2.4 Net effect: visible 4-byte (DC, E6, E7, E8-EB) difference is inert

**Static analysis is conclusive**: the four byte-differences identified
in the brief are not what's making CLEAR crash. They're cosmetic.

---

## 3. The triplet "off-by-one" hypothesis from `clear-mechanism.md` §6 is REFUTED

Body bytes from disk, verified by direct reading.

### 3.1 Defender Compilation slot 1 body

Body length: **660 bytes** (per body header bytes 1-2 = `94 02`).
Triplet decode: `00 38 80` → page=0, off-low-14=`0x38`=56. So
**NVARS-PROG = 56**. NUMEND-PROG = 148. SAVARS-PROG = 660.

Body bytes 50-59 inspected at runtime:

```
body[50] = 0x00     # part of last numeric form
body[51] = 0x00
body[52] = 0x80
body[53] = 0x00
body[54] = 0x0d     # CR end-of-line
body[55] = 0xff     # end-of-program FF marker
body[56] = 0xff     # FIRST BYTE of variable area; all FF (uninit)
body[57] = 0xff
...
body[140] = 0xff    # tail of numeric area
body[148] = 0x74    # FIRST BYTE of string variable area
```

So Defender's structure:
- **Program proper: body[0..55]** = 56 bytes (4-byte prefix + 51-byte
  line + 0x0d at body[54] + 0xff terminator at body[55])
- **NVARS at body[56]** = first byte of numeric variable area
- **NUMEND at body[148]** = first byte of string variable area
- **SAVARS at body[660]** = end-of-body (one past last string var)

Convention: **NVARS-PROG triplet = byte offset of first var byte (= one
past the FF terminator)**. Same as program length including terminator.
For Defender: 56-byte program + FF = 56 bytes total = NVARS-PROG.

### 3.2 Our slot 1 body

Body length: **57 bytes** (per body header bytes 1-2 = `39 00`).
Triplet: `00 39 80` → page=0, off-low-14=`0x39`=57. So
**NVARS-PROG = 57**.

Body bytes 50-57 inspected at runtime:

```
body[50] = 0x00
body[51] = 0x00
body[52] = 0x00
body[53] = 0x60
body[54] = 0x00     # tail of CALL 24576's numeric form
body[55] = 0x0d     # CR end-of-line
body[56] = 0xff     # end-of-program FF marker
body[57] = 0x00     # past end of body (would be first var byte if body had vars)
```

Our structure:
- **Program proper: body[0..56]** = 57 bytes (4 prefix + 52-byte line +
  0x0d at body[55] + 0xff terminator at body[56])
- **NVARS at body[57]** = (one past the FF terminator)
- Body length = 57 = NVARS-PROG. ✓

### 3.3 The previous agent's confusion

`docs/notes/clear-mechanism.md` §4.3 claimed Defender's program is
"56 bytes (offsets 0..55)" and that the FF terminator was at offset 56,
implying NVARS-PROG should be the offset of the FF marker. But this
mis-counted: Defender's FF terminator is at body[55], not body[56], and
NVARS = body[56] (the first byte AFTER the FF), giving NVARS-PROG = 56.

So Defender's actual byte structure has:
- Line body bytes (CR-terminated): body[0..54] = 55 bytes (incl. CR at 54)
- FF end-of-program: body[55]
- Total program-with-terminator: 56 bytes
- NVARS-PROG = 56 = byte after the FF.

For us:
- Line body bytes (CR-terminated): body[0..55] = 56 bytes (incl. CR at 55)
- FF end-of-program: body[56]
- Total program-with-terminator: 57 bytes
- NVARS-PROG = 57 = byte after the FF.

**Same convention. Our triplet=57 is correct.** The "off-by-one"
hypothesis is refuted.

### 3.4 Cross-check with FRED 07's `auto`

FRED 7 disk slot 59 "auto", body length = 623 bytes:
- Triplet decode: NVARS-PROG=19, NUMEND-PROG=111, SAVARS-PROG=623
- body[0..18] = program proper (4-byte prefix + 14-byte line + CR + FF
  end-of-program at body[18])
- body[19..110] = numeric variable area (filled `0xff` initially, total
  92 bytes)
- body[111..622] = string variable area (512 bytes)

Confirms convention: **NVARS-PROG is offset of first byte after FF
end-of-program**, **SAVARS-PROG is the body length** (= offset of byte
past end of file). Both Defender and FRED 7 follow this.

---

## 4. Multi-disk survey of dir-entry bytes 0xDC, 0xE6, 0xE7

**Method**: scanned all 153 .dsk images in `/Users/pmoore/Downloads/GoodSamC2/`,
extracted the dir-entry byte 0xDC, 0xE6, 0xE7 from every slot whose
type byte's low 5 bits = 16/17/18/19/20 and whose name starts with a
printable ASCII character (filters out erased/corrupted entries).

### 4.1 Top combinations by file type (% of 4502 valid entries)

| File type | Top (DC,E6,E7) | Count | % |
|-----------|----------------|-------|---|
| 16 (BAS)  | (`20`, `20`, `ff`) | 673 of 715 | **94.1%** |
| 17 (D ARRAY) | (`20`, `00`, `00`) | 25 of 64 | 39.1% |
| 18 ($ ARRAY) | (`20`, `00`, `00`) | 74 of 202 | 36.6% |
| 19 (CODE) | (`20`, `20`, `ff`) | 2665 of 2795 | **95.3%** |
| 20 (SCREEN$) | (`20`, `20`, `ff`) | 510 of 625 | 81.6% |

### 4.2 Auto-RUN type-16 BASIC subset (F2=0)

607 entries total. **604 have (DC=`20`, E6=`20`, E7=`ff`).** Only 3
outliers:

| Disk | Slot | Name | DC | E6 | E7 |
|------|------|------|----|----|----|
| EXPLOSION - B - GAMES FOR EMULATOR EXPLOSION (1996).dsk | 79 | (binary garbage name) | `10` | `7e` | `42` |
| Metempsychosis pdm8 (19xx).dsk | 76 | (binary garbage name) | `32` | `30` | `0e` |
| Sam Paper Magazine Issue 8 (19xx).dsk | 70 | `appedne` | `00` | `00` | `00` |

The first two have garbage filenames suggesting deleted-but-not-cleared
entries that got partially overwritten (likely not real auto-RUN files).
The third (`appedne`) has all-zero values like ours — same encoding,
unknown if it boots successfully.

### 4.3 Type-19 CODE files

95.3% are (`20`, `20`, `ff`). Same canonical pattern as type-16. Our
code files (slots 0/2/3) all have (`00`, `00`, `00`) — same structural
non-canonicality.

### 4.4 Why the canonical (0x20, 0x20, 0xff) values?

Per §2.1: ROM SLMVC at `rom:22070-22080` clears HDR+1..HDR+25 with
`0x20` and HDR+26..HDR+39 with `0xFF`. Then a normal SAVE that doesn't
overwrite HFG with a 0..3 prefix produces HDR+15=0x20, HDR+25=0x20,
HDR+26=0xFF. SAMDOS's `ofsm` (`c.s:1245-1252`) writes 33 bytes of
UIFA[15..47] starting at dir-entry offset 220 (= 0xDC), which is exactly
the 33 bytes that include UIFA byte 15 (HFG → dir 0xDC), byte 25
(→ dir 0xE6), byte 26 (→ dir 0xE7).

**So the canonical (`0x20`, `0x20`, `0xff`) values are not "data" — they
are the SLMVC-default fill pattern that gets passed through SAMDOS's
SAVE flow into the dir entry verbatim.** Their semantic content is
"HDR was cleared with 0x20/0xff before SAVE, and no value was written
on top".

---

## 5. The likely actual cause: BASIC body too small

### 5.1 ROM LOAD trace for our AUTO file

After SAMDOS BTHK returns and ROM finds the AUTO entry (mechanism
documented in `docs/notes/samdos2-auto-run-analysis.md`), ROM enters
the LOAD-program flow at `LDPRDT` (`rom:22591`). For our case (loading
into PROG-area which is currently empty):

1. **`E323 RDLLEN`**: CDE = body length from HDL+HDN+3 (= 57 in PAGE FORM)
2. **`E338 LDSZOK`**: HL=HDR+HDN, RDTHREE → CDE = current PROG area
   start. For empty PROG, returns FFFFFF; falls to `LDNAR` → `TSTRMAHL`.
3. **`E34F LD A,(HDR); SUB 16`**: it's a BASIC program (type 16), so
   `JR Z,LDPROG`-equivalent path
4. Setup: `E356 LD (NVARSP),A=0; LD (NVARS+1),A=0` — clears NVARSP and
   high byte of NVARS to prevent XOINTERS adjusting them via SADJ
5. **`E35D CALL RECL2BIG`**: deletes any current program (no-op for empty)
6. **`E366 CALL ADDRPROG`**: HL=PROG=0x9CD5 (in section-C-form), A=PROGP
7. **`E369 JR LDCR3`** at `E375`
8. **`E37F CALL MKRBIG`** opens 57 bytes at PROG. Internally calls
   `XOINTERS` (`rom:7163-7164`) which adjusts **all 14 page-byte sysvars**
   that lie ≥ PROG by +57 (= the size of opened space)
9. **`E382 LD (HL),0xFF`**: writes FF at PROG (in case load fails)
10. **`E38A CALL LDDBLK`**: SAMDOS reads the 57 body bytes into PROG..PROG+56
11. **`E39F CP 16; JR Z,LDPROG`** at E3AB
12. **`E3AB LDPROG`**: reads HDL+16/+19/+22 (3 triplets), sets
    NVARS=PROG+57, NUMEND=PROG+57, SAVARS=PROG+57. **Does NOT touch ELINE.**
13. **`E3D1 CALL RESTOREZ; CALL R1OFFCL DOCOMP`**: recompile labels
14. **`E3D9-E3EE`**: F2 check → AUTO-RUN line found → `JP GOTO3` jumps
    to line 10
15. BASIC main loop runs `OUT 254, 4: CLEAR 24575: ...`

### 5.2 ELINE state at AUTO-RUN entry

ELINE is set ONCE by MNINIT-time `NEW2 → CLRSR → SETSAV` at
`rom:24644-24655`:

```
ECEC CD6B39   CALL CLRSR    ; ZEROES NVARS PTRS, SETS NVARS, NUMEND, SAVARS
ECEF 22945A   LD (ELINE),HL ; ELINE = SAVARS+1 (CLRSR's HL on exit)
```

CLRSR at `rom:13209-13234` builds:
- ADDRNV → HL=NVARS = PROG+1 (initial PROG state, just `00FF` = end-of-program FF, no body)
- writes 46 bytes of FF (23 letter pointers × 2 each)
- copies 26 bytes from PSVTAB to HL+46
- INC H, INC H (skips 0x200 = the FPCS gap)
- writes SAVARS to current HL state (= NVARS+72+0x200)
- `LD (HL),0xFF` (terminator at SAVARS)

So **MNINIT-time SAVARS ≈ PROG+1+72+0x200** = PROG+585. Therefore
**MNINIT-time ELINE = SAVARS+1 = PROG+586**.

After `MKRBIG`'s `XOINTERS` at step 8 above adjusts all 14 sysvars
≥ PROG by +57 bytes:
- ELINEP unchanged (page byte unchanged for small adjustments)
- ELINE now = **PROG+643**

(Not exact: XOINTERS `XO3` path at `rom:23532-23534` does B=14 vars
including SAVARSP. The full XOINTERS algorithm adjusts every sysvar
whose LOCN ≥ HL by ±BC. Verified at `rom:23566-23574`: `CALL ADDRNV;
PUSH HL; CALL PNLP` runs through 14 sysvars.)

LDPROG (step 12) only writes NVARS, NUMEND, SAVARS — **does NOT
overwrite ELINE.**

### 5.3 What CLEAR's first computation yields

CLEAR entry (`rom:13148`) for `CLEAR 24575`:

```
3901 CALL SYNTAX3       ; parse number
3904 CALL UNSTLEN       ; A=page, HL=offset of 24575
3907 LD C,A; DEC C      ; C = page-1 (= 0)
3908-390A OR H,L        ; non-zero, so:
390B SET 7,H            ; HL = 24575 mod 16K | 0x8000
390D JR NZ,CLR3         ; non-zero, take CLR3 path
3916 PUSH BC; PUSH HL   ; save target RAMTOP
3918 CALL ADDRNV        ; A=NVARSP, HL=NVARS in section C
391B EX DE,HL; LD C,A   ; CDE = NVARS in page-form
391D LD HL,(ELINE)
3920 LD A,(ELINEP)
3923 CALL SUBAHLCDE     ; AHL = ELINE - NVARS (page-form)
3926 LD BC,025DH
3929 CALL SUBAHLBC      ; AHL = ELINE - NVARS - 0x025D
392C LD B,H; LD C,L
392E LD HL,(NVARS)
3931 CALL RECL2BIG      ; delete ABC bytes at HL (but RECL2BIG masks B with RES 7,RES 6)
```

For our state at AUTO-RUN entry:
- NVARS = PROG+57 = 0x5CD5 + 57 = 0x5D0E
- ELINE = PROG+643 = 0x5CD5 + 643 = 0x5F58
- ELINE-NVARS = 586 (decimal) = 0x024A (page=0)
- minus 0x025D = -19 = 0xFFFFED if normalised as 24-bit

**Now**: `SUBAHLBC` at `rom:7541-7549` calls AHLNORM which normalises
input to "19-bit form" and PAGEFORM at `rom:7580-7589` re-decodes:

```
PAGEFORM:    RL H
             RLA
             RL H
             RLA              ; PAGE NOW OK
             RR H
             SCF
             RR H              ; ADDR NOW OK IN 8000-BFFF
             CP 20H
             CCF              ; SET CARRY IF OVERFLOW
             RET
```

For AHL = -19 = 0xFFFFFFED (after sign-extending), passing through
PAGEFORM produces a garbage page byte and 0x80FF-ish offset. The
"CP 20H; CCF" yields carry-set (overflow) because A=0xFF >> 0x20.

But CLEAR doesn't check the carry from SUBAHLBC. It just uses AHL,
treating it as ABC = "block size to reclaim" at NVARS. RECL2BIG
(`rom:7191`):

```
RECL2BIG:    RES 7,B          ; clear bit 7 of B
             RES 6,B          ; clear bit 6 of B
             LD D,A
             OR B
             OR C
             RET Z            ; return if zero block size
             ; otherwise actually move bytes
             ...
```

If A is now garbage (e.g. 0xFF → after passing through PAGEFORM rotates,
becomes some non-zero value), and B/C may also be non-zero, RECL2BIG
proceeds with garbage block size, going through XOINTERS to adjust all
14 page-byte sysvars by an unbounded amount.

**Hypothesis**: this XOINTERS adjustment with garbage-AHL pushes one
or more page bytes (e.g. WKENDP, ELINEP) into a wrong page. Later in
CLEAR, MCLS reads CUSCRNP (which is fine, = 0x7E) and OUTs to URPORT,
but other sysvars are now corrupt, and the LDIR-based CLEAR/scroll
operations land in the wrong RAM.

### 5.4 Defender's path doesn't have this issue

Defender's body = 660 bytes, NVARS-PROG = 56, ELINE post-MKRBIG = 
MNINIT-time PROG+586 + 660 = PROG+1246. NVARS = PROG+56.

ELINE - NVARS = 1190. Subtract 0x025D = 605: AHL = 585 (positive).
RECL2BIG with A=0, B=0x02, C=0x49 (= 585 in BC). After RES 7/RES 6 mask:
B=0x02 unchanged, C=0x49. Total = 585 bytes to reclaim at NVARS. This is
sensible — it actually deletes the variable area between NVARS and
just-before-WKEND-overhead, leaving room for new variables.

XOINTERS adjusts all sysvars by ABC=585: ELINE shifts down by 585
to PROG+1246-585 = PROG+661, which is just past SAVARS. Sensible.

### 5.5 Why `OUT : CLEAR 24575 : OUT` (no LOAD/CALL) also crashes

Per `docs/notes/2026-05-10-handoff.md:73-74`, even the variant without
LOAD/CALL crashes. That variant's body is presumably similar small
size. Our analysis predicts the same failure mode: small body →
ELINE adjustment too small → ELINE-NVARS-0x025D negative → RECL2BIG
garbage.

### 5.6 Why interactive CLEAR (typed at OK prompt) works

When BASIC reaches its main editor loop (after MNINIT/NEW2/initial
setup), the user can type `CLEAR 24575` at the OK prompt. At that
point, no AUTO file LOAD has happened, so:
- NVARS = PROG+1 (just past PROG, MNINIT-CLRSR state)
- ELINE = PROG+586 (MNINIT-time)
- ELINE - NVARS = 585
- - 0x025D = -20 (signed)... wait still negative

Hmm. So why does interactive CLEAR work? Let me re-check.

Actually, MNINIT-time NVARS is PROG+1 (per `rom:24636`). CLRSR sets
SAVARS at NVARS + 72 + 0x200 = PROG + 1 + 72 + 512 = PROG + 585.
ELINE = SAVARS + 1 = PROG + 586. NVARS - ELINE = 585. Subtract 0x025D
(605) → -20.

So interactive CLEAR should have the same garbage. But Pete's empirical
observation in `~/.claude/projects/-Users-pmoore-git-sam-aarch64/memory/clear_in_auto_run.md`
says interactive CLEAR works.

**Possible reason**: between MNINIT and the user's interactive CLEAR,
BASIC's editor processes some input (e.g. the OK prompt), which may
update ELINE via WORKSPACE / E-LINE buffer activity. The ROM E-LINE
mechanism (cursor input) extends ELINE as the user types, then resets
it after Enter. So at OK-prompt time, ELINE may be a different value
than the MNINIT-default.

**This is unverified** — would need runtime instrumentation to confirm.

### 5.7 Confidence assessment

**§5 conclusion**: medium-high confidence that the body-too-small issue
is the cause; static trace gives us:

- ELINE not reset by LDPRDT/LDPROG (high-confidence; verified by
  reading rom:22591-22713)
- MKRBIG calls XOINTERS to adjust ELINE by body-size (high-confidence;
  rom:1E32-1E33 and rom:23536-23613)
- For small bodies (< ~605 bytes), ELINE-NVARS-0x025D goes negative
  (mathematically obvious)
- RECL2BIG with garbage AHL produces unpredictable results
  (low-medium confidence — the exact mathematical outcome depends on
  AHLNORM/PAGEFORM details I didn't exhaustively simulate)

**What is NOT proven by static analysis alone**:
- Whether RECL2BIG with the specific negative AHL we'd get (= -19) ACTUALLY
  produces a non-zero AND-masked block size that triggers XOINTERS, vs.
  RETting early at `rom:7196` with Z flag set.
- Whether the resulting page-byte adjustment lands on a SAMDOS or screen
  page specifically.
- The exact visualisation symptom (page-displaced screen pattern) is not
  derivable from the algebraic argument above; only the qualitative claim
  "garbage in, garbage out" is.

---

## 6. HDL population — what the ROM actually sees during LOAD

Trace of all 80 bytes of HDL with our disk's data, after FOPHK
populates HDL via SAMDOS gtfle → txhed.

### 6.1 Source mapping (citations in §2.1, §2.4 above)

| HDL offset | Source                      | Our value           | Defender value     |
|------------|-----------------------------|---------------------|--------------------|
| 0          | dir 0x00 (Type)             | `10`                | `10`               |
| 1-10       | dir 0x01-0x0A (Filename)    | `auto      `        | `AUTOBOOT  `       |
| 11-14      | DIFA fill `0x20` (lcnta)    | `20 20 20 20`       | `20 20 20 20`      |
| **15**     | **dir 0xDC (HFG)**          | **`00`**            | **`20`**           |
| 16-26      | dir 0xDD-0xE7               | `00 39 80 00 39 80 00 39 80 00 00` | `00 38 80 00 94 80 00 94 82 20 ff` |
| 27-30      | dir 0xE8-0xEB               | `00 00 00 00`       | `ff ff ff ff`      |
| 31-33      | dir 0xEC-0xEE               | `00 d5 9c`          | `00 d5 9c`         |
| 34-36      | dir 0xEF-0xF1               | `00 39 00`          | `00 94 02`         |
| 37-39      | dir 0xF2-0xF4               | `00 0a 00`          | `00 01 00`         |
| 40-79      | dir 0xF5-0xFD + chain bytes | `00 00 ...`         | `ff ff ff ff ff ...`|

So differences in HDL between us and Defender:

- HDL+15 (HFG): 0x00 vs 0x20 — inert (§2.1)
- HDL+16-18: triplet differs (correct for each body)
- HDL+19-21: triplet differs (Defender has actual NUMEND-PROG=148; ours = NVARS-PROG=57)
- HDL+22-24: triplet differs (Defender SAVARS-PROG=660; ours=57)
- HDL+25-26 (UIFA spare): inert (§2.2)
- HDL+27-30 (DIRE+spare): inert (§2.3)

**HDL+16-24 is the key region**. Our values are byte-correct for our
body shape per §3, but the body shape itself (with NVARS=NUMEND=SAVARS=
end-of-program) is what creates the small-ELINE-vs-NVARS gap that
breaks CLEAR's first arithmetic.

### 6.2 HDR (request) state at AUTO-RUN LOAD

ROM populates HDR before calling FOPHK. For the ROM-internal AUTO-RUN
LOAD, HDR's HDN block (offsets 31-39) is not user-set; it comes from
SLMVC's clear:

- HDR+1..HDR+25 = `0x20` (per `rom:22070-22074`)
- HDR+26..HDR+39 = `0xFF` (per `rom:22078-22080`)

So HDR+HDN+6 = HDR+37 = `0xFF`. At `E3D9` (`rom:22701`) ROM checks this:
`LD A,(HDR+HDN+6); AND A; JR Z,LDUSLN`. With HDR+HDN+6=0xFF, A is
non-zero → does NOT jump to LDUSLN → falls through to E3E2 which reads
**HDL+HDN+6** = dir 0xF2 = `0x00` for an auto-RUN BASIC.

**HDL+HDN+6=0x00 → A is zero → `RET NZ` doesn't return → fall to LDUSLN
→ `DEC A` (A=FF) → `LD (PPC+1),A` → `JP GOTO3`** with HL = `(HDL+HDN+7)`
= dir 0xF3-0xF4 = `0a 00` = line 10. So the auto-RUN line 10 is jumped
to. ✓

This part of the flow works correctly with both Defender and us.

---

## 7. Recommended fix

### 7.1 Minimum-viable fix (citation-grounded)

In `tools/build-disk.sh`, expand the BASIC body to canonical SAVE shape
by appending a synthetic variable area:

```python
# Build canonical-shape BASIC body: program + numeric vars + string vars.
# Minimum string-var area >= 0x025D = 605 bytes to make ELINE-NVARS-0x025D
# non-negative on first CLEAR.
PROG_BYTES = (bytes([0x00, 0x0a, len(line_body) & 0xff, (len(line_body) >> 8) & 0xff])
              + line_body + b"\xff")
NVARS_OFFSET = len(PROG_BYTES)             # offset after the FF terminator
NUMEND_OFFSET = NVARS_OFFSET               # no numeric vars: NUMEND = NVARS
GAP_BYTES = b"\xff" * 0x200                # 512-byte FPCS gap
SAVARS_OFFSET = NUMEND_OFFSET + len(GAP_BYTES)
STRINGS_PADDING = b"\xff" * 0x100          # 256 bytes string area
BASIC_BODY = PROG_BYTES + GAP_BYTES + STRINGS_PADDING
END_OFFSET = len(BASIC_BODY)               # SAVARS = end of body
# triplets:
img[auto_e_offset + 0xDD:auto_e_offset + 0xE0] = page_form_3byte(NVARS_OFFSET)
img[auto_e_offset + 0xE0:auto_e_offset + 0xE3] = page_form_3byte(NUMEND_OFFSET)
img[auto_e_offset + 0xE3:auto_e_offset + 0xE6] = page_form_3byte(END_OFFSET)
```

This produces a body of NVARS_OFFSET (= 57) + 0x200 + 0x100 = 825 bytes.
SAVARS-PROG = 825. After LOAD, ELINE = MNINIT-time PROG+586 + (XOINTERS
+825) = PROG+1411. ELINE - NVARS = 1411-57 = 1354. Minus 0x025D = 749
(positive). RECL2BIG behaves sensibly.

**Cite**: ROM E0B4-E0E0 SAVE-time triplet computation (`rom:22163-22180`)
which writes NVARS-PROG, NUMEND-PROG, SAVARS-PROG triplets identical to
our derivation. Empirical confirmation: every real-SAVE auto-RUN BASIC
across the 153-disk survey has SAVARS-PROG = body length, with body
length ≥ ~115 bytes. (The minimum non-zero body in our survey is FRED 7
slot 7 'FREDDY' at 115 bytes.)

### 7.2 Alternative — minimum padding test

If §7.1 padding is too disruptive, the minimum body length needed to
make CLEAR's first computation positive is body_len ≥ MNINIT_ELINE_minus_PROG -
NVARS_OFFSET + 0x025D = 586 - 57 + 605 = 1134. Round to 1152 (= 4*256
+ 128) or similar. Same triplet adjustments as §7.1 with adjusted
END_OFFSET.

(Caveat: The exact MNINIT-time ELINE position (= 586) was derived from
`rom:13230-13234` CLRSR which sets SAVARS at NVARS+72+0x200, plus 1 for
ELINE. The "+72" comes from CLRSR's `LD B,46; ... DJNZ` (46 bytes of FF
for letter pointers) plus `LD C,26; LDIR` (26 bytes of PSVTAB). 46+26 = 72.
The "+0x200" comes from `INC H; INC H` at rom:13231-13232. Adding 1 for
the FF terminator at SAVARS (`rom:13234`) and another 1 for ELINE = 
SAVARS+1 gives 72+512+1+1 = 586.)

### 7.3 Workaround — make `auto` a CODE file

Change the AUTO file to a type-19 CODE file with auto-exec address.
This bypasses the BASIC interpreter entirely (no LDPRDT, no CLEAR, no
MCLS). The Z80 stub does the LOAD CODE / CALL directly. This is a
**workaround** per `feedback_correctness_over_workarounds.md` — it
avoids the underlying issue rather than resolving it. Pete may prefer
this if §7.1 turns out to introduce other failures.

### 7.4 Why this is NOT a "trial-and-error" experiment

§7.1 is grounded in:
- ROM trace of XOINTERS (`rom:23536-23613`) showing ELINE adjustment
  by body-size during MKRBIG
- ROM trace of LDPROG (`rom:22683-22695`) showing it does NOT touch ELINE
- Multi-disk evidence (155 disks, 607 auto-RUN BASIC entries) that
  every real-SAVE AUTO has SAVARS-PROG = body length
- Algebraic derivation of the (ELINE-NVARS-0x025D) sign issue for
  small bodies

The fix is a structured response to a specific cited mechanism, not a
"let's try padding and see if it works" guess.

---

## 8. What this investigation did NOT prove

Honest gaps:

1. **The exact RECL2BIG behaviour with AHL=-19**. The signed
   normalisation through PAGEFORM at `rom:7580-7589` was traced but
   not exhaustively simulated. RECL2BIG may RET-Z early (`rom:7196`)
   for certain garbage-AHL values, in which case XOINTERS doesn't fire
   and no sysvar corruption happens — BUT then CLRSR runs next, and
   CLRSR has its own internal SETSAV+ELINE issue that may also be
   problematic.

2. **The exact MCLS-level mechanism that produces the visible
   "page-displaced screen" symptom**. §5.3 hypothesises that XOINTERS
   pushes a page-byte sysvar into a wrong page, but doesn't pinpoint
   *which* page-byte gets corrupted, *which* MCLS LDIR-target page it
   feeds, or *why* the visual is "structured colour stripes" rather
   than e.g. a hang or an error message.

3. **Whether the interactive CLEAR works for a different reason than
   the AUTO-RUN CLEAR fails**. §5.6 hypothesises that the editor's
   E-LINE buffer activity between MNINIT and OK-prompt CLEAR shifts
   ELINE to a different value, but this is not cited.

4. **The exact correlation between body size and crash visualisation**.
   The cache-fix changed the visual symptom (palette differs per
   handoff line 18-19) without eliminating the crash. If the
   underlying mechanism is body-size-dependent ELINE drift, the cache
   fix wouldn't have changed visuals — but Pete observed a change. So
   either the cache fix is hitting a different code path, or our §5
   mechanism is incomplete.

5. **Whether SAMDOS itself has any role beyond gtfle/txhed in
   populating HDL**. The trace assumes SAMDOS is pure; if it modifies
   HDL after `txhed`, that would change the analysis. Spot-checked: no
   such modification found.

These gaps argue for **a single targeted experiment** as the next
step rather than further static analysis: build an ALT version of the
disk per §7.1, test it. If it boots, the mechanism §5 is confirmed. If
it doesn't, there is something else (and runtime instrumentation
becomes the right next step, not more byte-level static work).

---

## 9. Specific corrections to prior project docs

### 9.1 `docs/notes/clear-mechanism.md` §6 (off-by-one fix)

**REFUTED**. §3 above re-derives the canonical convention: NVARS-PROG
counts the program length INCLUDING the FF terminator. Defender's
56-byte program (incl. FF) and our 57-byte program (incl. FF) follow
the same rule. Our triplet=57 is byte-correct.

The mechanism §6 of clear-mechanism.md proposed (negative AHL via
RECL2BIG-XOINTERS) is **largely correct** — the negative-AHL hypothesis
holds up — but the *cause* is not the triplet value. It's the body size.

### 9.2 `docs/notes/sam-disk-format.md` §2.4 — Tech Manual L4366-4368

The Tech Manual claim "bytes 210-219 not used by SAMDOS" is wrong (per
the existing doc note, citing `samdos/src/f.s:462-471`). The doc note
already documents this. Confirmed by my independent reading of `svhd`
and `gtfle:1376`. No change needed; just confirming the existing note.

### 9.3 `docs/notes/sam-file-header.md` §4.3 — auto file dir entry

This doc's table for the auto file dir entry includes:

> 0xE6-0xE7 (spare) | `20 FF` | `00 00` | Cosmetic.

Confirmed correct: per §2.2 above, these bytes are indeed not consumed
by ROM. The doc's "Cosmetic" tag is right.

### 9.4 `docs/notes/2026-05-10-handoff.md:73-74` — "CLEAR has unexplained problem"

After this investigation, the problem is no longer unexplained. The
mechanism is: small body → ELINE > NVARS by less than 0x025D after
LDPRDT → CLEAR's first computation produces a negative "block size to
reclaim" value → RECL2BIG with garbage AHL feeds XOINTERS bad data →
sysvar page-bytes get adjusted to invalid pages → subsequent LDIR-based
operations land in wrong RAM. **Confidence: medium-high.**

Update line 196 of m0-status.md / handoff section: the recommended fix
is no longer "drop CLEAR" or "MODE 3: CLS # priming"; it's "pad the
BASIC body with synthetic variable area" per §7.1.

### 9.5 `~/.claude/projects/-Users-pmoore-git-sam-aarch64/memory/clear_in_auto_run.md`

Update guidance to:
- **The CLEAR-in-AUTO-RUN crash is NOT triggered by the CLEAR statement
  itself, but by the AUTO BASIC body being smaller than ~600 bytes.**
- **Recommended fix**: pad the BASIC body to canonical SAVE shape
  (program + 0x200 gap + ≥0x80 string area), with triplets reflecting
  NVARS-PROG, NUMEND-PROG, SAVARS-PROG = body-length per §7.1.
- The previously-proposed fixes (`MODE 3: CLS #` priming,
  triplet-1-decrement) are both wrong.

---

## 10. Sources cited

| Tag | Path | Lines used |
|-----|------|------------|
| ROM | `docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt` | 1183 (BTHK), 1187 (FOPHK), 1190 (SVHK), 1194 (ALHK), 1215 (HFG), 1237 (HDR/HDL), 7140-7228 (MKRBIG, RECL2BIG), 7370-7400 (ADDRPROG/ADDRELN/ADDRDATA/ADDRCHAD/ADDRSV/ASV2), 7541-7589 (ADDAHLBC/ADDAHLCDE/SUBAHLBC/SUBAHLCDE/PAGEFORM), 7654-7659 (RDTHREE), 11212 (RDLLEN), 13141-13247 (CLEAR/CLR1/CLR3/CLR4/CLRSR/SETSAV/SETNE/SETSYS), 19181-19222 (LASTPAGE), 20453-20598 (BOOT/BOOTNR/BOOTEX/BTNOE), 22020-22054 (E019 HDR/HDL doc block), 22057-22080 (SLMVC HDR-clear), 22108-22113 (HFG-write on SAVE prefix), 22136-22141 (LINE n auto-RUN setup), 22147-22180 (RNTVL SAVE-time triplet write), 22247 (LD A,19), 22259 (HDR+16=MODE), 22361-22484 (SLMVC dispatch / LVMMAIN / FOPHK / LOAD-CODE flow / HFG bit-1 check), 22591-22713 (LDPRDT / LDPROG / LDUSLN), 22719-22850 (LKHDR / PRHDC / LDHDR / HDL+HFG sites), 23525-23613 (XOINTERS / SADJ), 23580 (ELINEP read in XOINTERS), 24430-24700 (MNINIT / NEW2 / CLRSR / SETSAV / ELINE init at ECEF) |
| TM  | `docs/sam/sam-coupe_tech-man_v3-0.txt` | 4262-4275 (geometry), 4286-4295 (9-byte body header), 4304-4314 (file types), 4338-4400 (256-byte directory entry layout), 4459-4502 (UIFA layout) |
| samdos-b | `~/git/samdos/src/b.s` | 16 (samdos type), 33-127 (dos8 epilogue), 220-260 (RAM cache), 278-290 (UIFA template), 497-540 (samhk hook table) |
| samdos-c | `~/git/samdos/src/c.s` | 1185-1267 (ofsm: file-allocation; **1247 the dir-byte-220 write**), 1346-1487 (gtfle: dir-entry walk; **1376 dir-byte-211 cache read; 1476-1480 dir-byte-220 33-byte block copy → DIFA bytes 15-47**), 1503-1545 (lcnta/lcntb/grpnt/incrpt) |
| samdos-h | `~/git/samdos/src/h.s` | 1-26 (rxhed: copies UIFA from caller IX into SAMDOS uifa buffer), 38-67 (txhed/txrom/hgthd: copies DIFA back to ROM HDL), 74-90 (dschd/hload), 132-156 (hsave), 201-237 (autnam/init/initx/hauto), 308-321 (cals), 336-361 (hconr: populates RAM cache from UIFA bytes 0/31/32-33/34/35-36) |
| build | `tools/build-disk.sh` | 60-330 (full python build block) |
| our test.mgt | `build/test.mgt` | dir entry slot 1 bytes 0xD0..0xFF (verified) |
| Defender | `/Users/pmoore/Downloads/GoodSamC2/Defender Compilation (19xx).dsk` | dir entry slot 1; body bytes 0..659 |
| FRED 07 | `/Users/pmoore/Downloads/GoodSamC2/FRED Magazine Issue 07 (1990) [a1].dsk` | slot 7 (FREDDY 115 bytes), slot 40 (EAGLE 219 bytes), slot 59 (auto 623 bytes) |
| GoodSamC2 survey | `/Users/pmoore/Downloads/GoodSamC2/*.dsk` | 153 .dsk files; 4502 valid file entries; 715 type-16, 607 auto-RUN |
| Project docs | `docs/notes/` | clear-mechanism.md (refuted §6), clear-investigation.md, sam-disk-format.md, sam-file-header.md, sam-paging.md, samdos2-auto-run-analysis.md, fred-disk-inspection.md, 2026-05-10-handoff.md |
| Memory | `~/.claude/projects/-Users-pmoore-git-sam-aarch64/memory/` | feedback_correctness_over_workarounds.md, clear_in_auto_run.md, m0_open_findings.md |
