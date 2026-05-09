# CLEAR-in-AUTO-RUN: actual mechanism, given the corrected MNINIT trace

Date: 2026-05-10. Author: investigation agent (Opus 4.7), Pete's PRIME
DIRECTIVE (`feedback_correctness_over_workarounds.md`) in force.

This document supersedes the §5 mechanism in
`docs/notes/clear-remaining-diff.md`. That doc's §5 mechanism
("BASIC body too small → ELINE-NVARS-0x025D negative → RECL2BIG
garbage → XOINTERS sysvar carnage") was refuted by Pete's spot-check —
the agent missed a 20-byte LDIR at `rom:13226-13228` (`LD HL,PSVT2; LD
C,20; LDIR`), so SAVARS = NVARS+604 (not NVARS+584), ELINE-NVARS = 606
(not 586), AHL after `SUBAHLBC 0x025D` = +1 (positive), not -19.

ROM = `docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt`.
Tech Manual = `docs/sam/sam-coupe_tech-man_v3-0.txt`.
SAMDOS source = `~/git/samdos/src/<file>`.

---

## TL;DR (verdict)

**Cited root cause** (high confidence): CLEAR's RAMTOP-vs-WKEND
validation at `rom:13186-13192` fails for our specific combination of
(small body + low CLEAR target). After LDPRDT's MKRBIG-XOINTERS shifts
WKEND by +57 (the body size), `WKEND+180 = PROG+845 = linear 0x6022`,
which is **above** the CLEAR target `24575 = 0x5FFF`. The check `(WKEND+180)
- target` returns NC (no borrow), JR NC,RTERR (`rom:13190`) fires
"Invalid CLEAR address" error 48.

The interactive case has **no** MKRBIG-XOINTERS shift (no body load
ever happened), so WKEND stays at MNINIT-time PROG+608 = linear 0x5F6D,
WKEND+180 = 0x6021... wait, that's also > 0x5FFF.

**Actually** the interactive math also fails per this analysis. Either:

- (a) my understanding of WKEND-init is wrong (in which case the whole
  argument falls)
- (b) the empirical "interactive CLEAR works" claim needs re-verification
  on the specific value `CLEAR 24575` (Pete's claim was based on
  general "CLEAR n at OK prompt works" — maybe `CLEAR 24575` *also*
  errors interactively, just to error-not-crash).

I cannot conclusively determine the root cause of the
**visualization** difference (clean error 48 vs page-displaced screen)
from static analysis alone. **Runtime instrumentation needed.**

The body of this report goes through the math step-by-step with
citations, lays out the candidate mechanisms, and flags the gaps
honestly.

---

## 1. The corrected MNINIT and post-LDPRDT state

This section uses the prompt's corrections (which I independently
verified against ROM).

### 1.1 MNINIT-time state (SETMIN included)

CLRSR at `rom:13209-13234` walks:

- `ADDRNV` (rom:13215): A=NVARSP=0, HL=NVARS=PROG+1 (initial empty
  program — just the FF terminator at PROG)
- `LD B,46`; loop write FF, INC HL — fills NVARS..NVARS+45 with FF (46
  bytes for letter pointers A-W × 2 each... actually 23 letters × 2 =
  46 bytes)
- `LD HL,PSVTAB; LD C,26; LDIR` (rom:13223-13225): copies 26 bytes
  from PSVTAB to NVARS+46..NVARS+71. PSVTAB at `rom:13286-13288` is 6
  bytes (`19 00 03 00 FF FF`)... wait C=26 not 6. Let me recount.

PSVTAB layout per `rom:13286-13288`:
```
PSVTAB: DW 0019H          ;X VARS
        DW 0003H          ;Y VARS
        DW 0FFFFH         ;Z VARS
PSVT2:  DB 2; DW 8; DB "os"; DB 0,0,0,0,0    ;YOS variable
        DB 2; DW 0xFFFF; DB "rg"; DB 0,0,192,0,0  ;YRG variable
```

PSVTAB = 6 bytes (3 words). PSVT2 = 20 bytes (2 var entries × 10 bytes
each).

But CLRSR's LDIR copies 26 bytes from PSVTAB. PSVTAB only has 6 bytes
defined. The LDIR continues past the labeled bytes — i.e. PSVTAB+6
onwards reads bytes that are physically PSVT2.

So the LDIR-26-from-PSVTAB copies PSVTAB (6 bytes) + first 20 bytes of
PSVT2... but then there's another LDIR-20-from-PSVT2 right after at
`rom:13226-13228`:

```
13986 EB                 EX DE,HL
13987 21E339             LD HL,PSVTAB
13988 0E1A               LD C,26
13989 EDB0               LDIR              ;COPY 3 PTRS AND YOS/YRG
13990 21E939             LD HL,PSVT2
13991 0E14               LD C,20
13992 EDB0               LDIR              ;COPY YOS/YRG AGAIN
```

So:
- 26-byte LDIR from PSVTAB writes 6 bytes (the actual PSVTAB) + 20
  bytes (which are physically PSVT2's bytes since PSVT2 immediately
  follows PSVTAB).
- Then 20-byte LDIR from PSVT2 writes 20 bytes (PSVT2 again).

Net: 46 bytes written.

Hmm wait that's weird — the comment "COPY YOS/YRG AGAIN" suggests
intentional duplication. So the 26-from-PSVTAB writes PSVTAB (6 bytes
of letter pointers) + YOS/YRG (20 bytes), then the 20-from-PSVT2
overwrites those same YOS/YRG bytes... no, LDIR continues forward.

Let me re-read. After the first LDIR, DE has advanced by 26 bytes
(from initial DE = NVARS+46). Now DE = NVARS+72.

The second LDIR (20 bytes from PSVT2) writes to DE = NVARS+72..NVARS+91.

So total CLRSR writes: 46 bytes FF + 26 bytes PSVTAB+PSVT2_first + 20
bytes PSVT2 = 46 + 26 + 20 = 92 bytes, starting at NVARS=PROG+1 and
ending at PROG+93.

After the LDIRs: HL=DE = NVARS+92 = PROG+93.

Then `EX DE,HL` (rom:13229) → HL=NVARS+92 = PROG+93. Then `CALL SETNE`
(rom:13230): writes (NUMENDP, NUMEND) = page=0, addr=PROG+93. So
NUMEND = PROG+93.

Then `INC H; INC H` (rom:13231-13232): H += 2 = +0x200. HL = PROG+93 +
0x200 = PROG+605.

Then `CALL SETSAV` (rom:13233): SAVARS = PROG+605.

`LD (HL),0xFF` (rom:13234): byte at PROG+605 = 0xFF (terminator).

After CLRSR (within MNINIT): NVARS=PROG+1, NUMEND=PROG+93,
SAVARS=PROG+605. ✓ matches the prompt's correction.

Then MNINIT continues at `rom:24653-24655` (= ECFB-ED02 in ROM
addresses):

```
ECFB CD6B39   CALL CLRSR
ECFE 2A825A   LD HL,(SAVARS)    ;HL = PROG+605
ED01 23       INC HL            ;HL = PROG+606
ED02 22945A   LD (ELINE),HL     ;ELINE = PROG+606
```

So **ELINE = PROG+606** at MNINIT-time (matches prompt's correction).

Then SETMIN at `rom:24656` (`CALL SETMIN`):

```
1D71 DBFB     SETMIN: IN A,(URPORT); PUSH AF
1D74 CD351F           CALL ADDRELN     ;A=ELINEP=0, HL=ELINE in section C
1D77 CDB904           CALL SETKC2      ;set KCUR
1D7A 360D             LD (HL),0x0D     ;CR at ELINE = PROG+606
1D7C 23               INC HL
1D7D 36FF             LD (HL),0xFF     ;FF at PROG+607
1D7F 23               INC HL
1D80 22915A           LD (WORKSP),HL   ;WORKSP = PROG+608
1D83 32905A           LD (WORKSPP),A
1D86 F1               POP AF
1D87 D3FB             OUT (URPORT),A
                      ;falls through to SETWORK
1D89 2A915A   SETWORK: LD HL,(WORKSP)
1D8C 3A905A           LD A,(WORKSPP)
1D8F 228E5A           LD (WKEND),HL    ;WKEND = WORKSP = PROG+608
1D92 328D5A           LD (WKENDP),A
                      ;falls through to SETSTK
1D95 2ACC5B   SETSTK:  LD HL,(FPSBOT)
1D98 22655C           LD (STKEND),HL
1D9B C9               RET
```

So **MNINIT-time WKEND = PROG+608**. Cite: rom:6992-7012 (SETMIN
falls through SETWORK falls through SETSTK).

Summary at MNINIT-end (post `MODET 3`, `CLSHS2`, before user input):

| Sysvar | Value (sec-C-form) | Linear |
|--------|--------------------|--------|
| PROG   | 0x9CD5             | 0x5CD5 (= 5CB6+31, rom:24547) |
| NVARS  | 0x9CD6 (PROG+1)    | 0x5CD6 |
| NUMEND | 0x9D32 (PROG+93)   | 0x5D32 |
| SAVARS | 0x9F32 (PROG+605)  | 0x5F32 |
| ELINE  | 0x9F33 (PROG+606)  | 0x5F33 |
| WORKSP | 0x9F35 (PROG+608)  | 0x5F35 |
| WKEND  | 0x9F35 (PROG+608)  | 0x5F35 |
| RAMTOP | 0xBFFF             | 0xBFFF (rom:24534-24535, page-form) |
| RAMTOPP| 3                  | (rom:24521-24522) |
| LASTPAGE | 3                | (rom:24515-24521) |
| MODE   | 3                  | (rom:24657-24658) |

Cited from rom:24547 (PROG init), 24634-24655 (NVARS/NUMEND/SAVARS via
CLRSR; ELINE=SAVARS+1), 24656 (SETMIN, transitively SETWORK sets
WKEND/WORKSP), 24515-24535 (RAMTOP/LASTPAGE), 24657-24658 (MODE).

### 1.2 LDPRDT body load shifts WKEND, ELINE etc by +body_size

At LDPRDT (`rom:22591`), the LOAD-program path proceeds:

1. RDLLEN reads the body length from HDL+HDN+3 (= dir bytes 0xEF-0xF1).
   For our 57-byte body: CDE = page=0, mod=57. (rom:22591, RDLLEN at
   rom:7648-7659.)
2. NVARSP=0, NVARS+1=0 (rom:22624-22625) — NVARS reset to "below PROG"
   so XOINTERS skips it via the v2.1 H=0 check at rom:7261-7262.
3. RECL2BIG (rom:22629) — deletes any current program (no-op for empty
   PROG state at MNINIT-end).
4. ADDRPROG (rom:22632); JR LDCR3 → MKRBIG (rom:22649) opens 57 bytes
   at PROG.

MKRBIG (rom:7148) does:
- PUSH HL (LOCN=PROG)
- TSTRMBIG checks room (rom:14615-14620, calls TSTRMABC which calls
  AHLNORM/TESTROOM)
- 150-byte overhead check (rom:7150-7152)
- MKRM2 reformats CDE = (PAGES, MOD16K) (rom:7154-7159)
- POP HL; PUSH DE; PUSH HL (rom:7160-7162)
- `RST 30; DW XOINTERS` in MAKEROOM mode (CY' clear; cite §1.4 below)

XOINTERS (rom:23536-23613) walks 14 sysvars from SAVARSP for B=14 in
MAKEROOM mode: each sysvar whose location > LOCN gets +57 added.

The 14 sysvars walked (per `rom:23537 LD IY,SAVARSP` and PNLP's IY
+= 3 per iteration for B=14):

SAVARS, NUMEND, NVARS, DATADD, WKEND, WORKSP, ELINE, CHAD, KCUR,
NXTLINE, PROG, XPTR, DEST, PRPTR (cited at `rom:0294 5A81-5AA9`,
section "POINTERS THAT ARE ADJUSTED BY MAKEROOM, RECLAIM").

For our state at MKRBIG entry:
- SAVARS=PROG+605 > PROG → +57 → PROG+662
- NUMEND=PROG+93 > PROG → +57 → PROG+150
- NVARS-high = 0 (clobbered at rom:22625) → JR Z,NPSV taken (v2.1
  skip; rom:7260-7262). NOT adjusted.
- DATADD=? (set by RESTOREZ in MNINIT but not re-set after; could be
  PROG-1 = 0x9CD4, < PROG → not adjusted)
- WKEND=PROG+608 > PROG → +57 → **PROG+665**
- WORKSP=PROG+608 > PROG → +57 → PROG+665
- ELINE=PROG+606 > PROG → +57 → **PROG+663**
- CHAD/KCUR/NXTLINE/XPTR/DEST/PRPTR: undef or low, varies.
- PROG=PROG (= LOCN). Per "JR NC,NPSV ;DO NOT ADJUST SVAR IF IT IS <=
  LOCN" at rom:7282 — equal → NOT adjusted.

After MKRBIG: WKEND=PROG+665, ELINE=PROG+663, NVARS-still-clobbered.
Cite: rom:7257-7307 (PNLP/ASSV/PADJ), rom:23536-23613 (XOINTERS
wrapper).

5. LDDBLK (rom:22656) loads 57 body bytes into PROG..PROG+56.
6. LDPROG (rom:22679-22695) iterates B=3 reading 3 triplets from
   HDL+16/+19/+22 (= dir bytes 0xDD-0xE5):
   - NVARS-PROG = 57 → NVARS = PROG+57
   - NUMEND-PROG = 57 → NUMEND = PROG+57
   - SAVARS-PROG = 57 → SAVARS = PROG+57

   ELINE is **NOT** rewritten by LDPROG (cite: rom:22683-22695 only
   touches NVARS+1, NUMEND+1, SAVARS+1 via IY, not ELINE).

7. RESTOREZ (rom:22697) sets DATADD = PROG-1 (rom:12222 RESTOREZ → LD
   HL,0; JR RESTORE2; FNDLINE for line 0 in PROG; DEC HL).

8. DOCOMP (rom:22699) compiles labels/PROCs/FNs (no-op for our
   1-line program).

9. F2-byte check (rom:22701-22713): JP GOTO3 with HL=line 10.

GOTO3 sets NEWPPC=10, NSPPC=0, RETs.

State at AUTO-RUN entry (just before BASIC line 10 starts running):

| Sysvar | Value (sec-C-form) |
|--------|--------------------|
| PROG   | 0x9CD5             |
| NVARS  | 0x9D0E (PROG+57)   |
| NUMEND | 0x9D0E (PROG+57)   |
| SAVARS | 0x9D0E (PROG+57)   |
| ELINE  | 0x9F6C (PROG+663)  |
| WKEND  | 0x9F6E (PROG+665)  |
| WORKSP | 0x9F6E (PROG+665)  |
| RAMTOP | 0xBFFF             |
| LASTPAGE | 3 |

### 1.3 Verifying ELINE-NVARS at AUTO-RUN entry

ELINE - NVARS = (PROG+663) - (PROG+57) = **606**.

CLEAR's first computation (rom:13166-13174):
- HL=(ELINE)=0x9F6C, A=(ELINEP)=0
- ADDRNV (rom:13163) returns A=NVARSP=0, HL=NVARS=0x9D0E
- EX DE,HL; LD C,A → CDE = NVARS in page-form
- HL,A = ELINE
- SUBAHLCDE (rom:7565): AHL = ELINE - NVARS = 606 (page-form: page=0,
  offset=0x025E... but with bit 15 force, it's 0x825E)
- LD BC,0x025D
- SUBAHLBC (rom:7546): AHL = 606 - 605 = **+1** (NOT -19)

Comment at rom:13168: `;GET ELINE-NVARS IN AHL (AT LEAST 025DH)`.
Invariant satisfied with 1-byte margin. ✓

### 1.4 RECL2BIG with ABC=(0,0,1)

Per RECL2BIG (rom:7191-7196):

```
RES 7,B; RES 6,B   ; mask B (bits 7,6 = "page count high bits")
LD D,A             ; D = A = high byte of AHL = 0
OR B; OR C
RET Z              ; if A|B|C = 0, RET Z
```

For our state: A=0, B=0 (was 0 anyway), C=1. After mask: same. OR B|C
= 1, NZ. **RET Z NOT taken**.

Continue at rom:7198-7227:
- LD A,D (=0); LD D,B (=0); LD E,C (=1); LD C,A (=0). CDE = 0,0,1.
- PUSH BC=(0,junk); PUSH DE=(0,1); PUSH HL=NVARS; SCF; RST 30 XOINTERS.

XOINTERS reclaim mode (CY'=1 from SCF + RST 30 wrapper's EX AF,AF',
which ends up inverting again per rom:603 R1ONCLBC — net: CY' set when
caller SCF'd before RST 30 — cite rom:601-603, 23539).

XOINTERS walks 14 sysvars, adjusting each by -1 if location > LOCN
(=NVARS=PROG+57):

- SAVARS = PROG+57 = LOCN. SBC HL,DE (LOCN-svar) = 0, NC. ADD HL,DE =
  LOCN, NC. JR NC,NPSV taken — NOT adjusted.
- NUMEND = PROG+57 = LOCN. Same — NOT adjusted.
- NVARS = PROG+57 = LOCN. Same — NOT adjusted (also INC H/DEC H test:
  H=0x9D, NZ, not skipped via v2.1 path).
- DATADD = PROG-1 = 0x9CD4 < LOCN. PNT2: A=DATADDP=0; CP C=0; Z. EX
  DE,HL; SBC HL,DE (=LOCN-DATADD = +0x3A, NC). JR NC,NPSV taken — NOT
  adjusted.
- WKEND = PROG+665 > LOCN. SBC = LOCN-WKEND = -0x260 (CY=1). ADD
  restores HL with CY=1 (from 16-bit overflow). JR NC,NPSV not taken
  (CY=1 → C). PADJ: AHL = WKEND - 1 = **PROG+664**.
- WORKSP = PROG+665 > LOCN. Same — adjusted to PROG+664.
- ELINE = PROG+663 > LOCN. Adjusted to **PROG+662**.
- CHAD/KCUR/NXTLINE: depend on state; probably < LOCN.
- PROG = LOCN. Same offset, NC. NOT adjusted.
- XPTR/DEST/PRPTR: probably 0 (uninit) or skipped via v2.1 H=0 check.

Cite: rom:7257-7307 (PNLP/ASSV/PADJ), rom:23536-23613 (XOINTERS).

After XOINTERS: WKEND=PROG+664, WORKSP=PROG+664, ELINE=PROG+662.

Then XOINTERS sets PAGCOUNT/MODCOUNT for the FARLDIR move:
- AHL_makeroom = OLD_WKEND - LOCN = (PROG+665) - (PROG+57) = 608
- For reclaim: AHL -= adjust = 608 - 1 = **607**
- PAGCOUNT = 0 (high byte); MODCOUNT = 607+1 = 608 (per rom:23607-
  23610, INC HL is the "+1 for MODCOUNT may be 4000H now").

FARLDIR moves 608 bytes from src=NVARS+1=PROG+58 to dest=NVARS=PROG+57
(forward LDIR). This shifts the area PROG+58..PROG+665 down by 1 byte
to PROG+57..PROG+664. Cite: rom:7208-7227 (RECL2BIG post-XOINTERS),
rom:9998-10030 (FARLDIR).

### 1.5 CLRSR rebuild after CLEAR's RECL2BIG

CLRSR (`rom:13209-13234`) runs after RECL2BIG. It writes:

- 46 bytes FF starting at NVARS=PROG+57 → fills PROG+57..PROG+102
- 26-byte LDIR from PSVTAB → PROG+103..PROG+128
- 20-byte LDIR from PSVT2 → PROG+129..PROG+148
- SETNE → NUMEND = HL = PROG+149
- INC H, INC H → HL += 0x200 → PROG+661
- SETSAV → SAVARS = PROG+661
- LD (HL),0xFF → byte at PROG+661 = 0xFF

After CLRSR: NVARS=PROG+57, NUMEND=PROG+149, SAVARS=PROG+661.
ELINE=PROG+662 (unchanged from CLEAR-RECL2BIG-XOINTERS shift).
WKEND=PROG+664 (unchanged).

ELINE-SAVARS = 1 (ELINE = SAVARS+1 invariant maintained). ✓

### 1.6 DOCOMP, MCLS

DOCOMP (rom:13176, calling rom:12013) walks PROG looking for
labels/PROC/FN. For our 1-line program, no matches. Returns.

MCLS (rom:13177, calling rom:2080) clears the screen using:
- MODE (= 3 from MNINIT's MODET 3)
- CUSCRNP (= 0x7E from MNINIT/NEW2)
- LWBOT/LWTOP/LWLHS/LWRHS (= MAIT-block defaults; 0x5A3C-0x5A3F)
- M23PAPP/ATTRP (= MODET 3 defaults; 0x5A45/0x5A48)
- WINDTOP/WINDBOT/WINDRHS (set by CLSLOWER from LW*)

None of these sysvars are in the XOINTERS list (which is only the 14
ADJUST POINTERS at 0x5A81-0x5AAB per `rom:0867`). So MCLS sees the
correct MNINIT-defaults. **MCLS should run cleanly.**

### 1.7 The WKEND-vs-RAMTOP check (the predicted failure point)

At rom:13178-13192:

```
3940 2A8E5A    LD HL,(WKEND)    ; HL = WKEND = 0x9F6D (sec-C-form), linear 0x5F6D
3943 3A8D5A    LD A,(WKENDP)    ; A = 0
3946 01B400    LD BC,180
3949 CDCC1F    CALL ADDAHLBC    ; AHL = WKEND + 180 = PROG+844 = linear 0x6021
394C D1        POP DE
394D C1        POP BC           ; CDE = CLEAR target = 24575 in page-form
                                ; UNSTLEN at rom:14773 returns A=1, HL=0x9FFF for 24575
                                ; LD C,A; DEC C → C=0; SET 7,H → HL=0x9FFF
                                ; CDE = (C=0, DE=0x9FFF section-C-form, page-form)
394E CDE71F    CALL SUBAHLCDE   ; AHL = (WKEND+180) - target
                                ; = 0x6021 - 0x5FFF = +0x0022 (positive, NC)
3951 3006      JR NC,RTERR      ; ;JR IF RAMTOP WILL BE TOO CLOSE TO WKEND
                                ; NC → take JR → RTERR
```

For our AUTO-RUN state with body=57:

CLEAR's RECL2BIG-XOINTERS reduced WKEND from PROG+665 (after LDPRDT
MKRBIG) to PROG+664 (-1 from CLEAR's reclaim, per §1.4). So at the
WKEND-validation point (rom:13178):

- WKEND = PROG+664 = linear 0x5F6D (= 0x5CD5+664)
- WKEND+180 = 0x5F6D + 180 = 0x6021
- target = 24575 = 0x5FFF
- SUBAHLCDE: 0x6021 - 0x5FFF = +0x22 (positive, NC)
- JR NC,RTERR → **error 48 "Invalid CLEAR address"** fires.

**This predicts CLEAR fails with a clean error 48, not a memory
corruption crash.** Cite: rom:13178-13192.

### 1.8 Cross-check: interactive case math

For interactive `CLEAR 24575` (typed at OK prompt), no body load
happens, no MKRBIG-XOINTERS shift. WKEND = MNINIT-time PROG+608 (per
§1.1).

WKEND+180 = PROG+788 = linear 0x5CD5+788 = 0x5FE9.
target = 0x5FFF.
SUB: 0x5FE9 - 0x5FFF = -0x16 (negative, CY).
JR NC,RTERR — CY set → JR NC NOT taken.
Continue at rom:13186-13190:
- LD A,(LASTPAGE)=3
- CP C=0 (target page-1)
- JR NC,CLR4 — 3 ≥ 0 → NC → CLR4 taken.
CLR4 sets RAMTOP=24575, RAMTOPP=0. **CLEAR succeeds.**

So interactive CLEAR 24575 works because WKEND+180 is just below the
target (by 22 bytes). Our AUTO-RUN CLEAR 24575 fails because WKEND has
been shifted +57 by MKRBIG, making WKEND+180 just above the target.

**The body=57 size is the discriminator** — but via the
WKEND-RAMTOP check at rom:13186, NOT via the §5 ELINE-NVARS check.

---

## 2. The visualization gap

Per §1.7, CLEAR's WKEND-validation predicts error 48 "Invalid CLEAR
address". The error path is:

1. RTERR at rom:13191: `RST 08H; DB 48`.
2. ERROR2 at rom:0009 → reads error code 48.
3. CHAD/XPTR saved (rom:12906-12911).
4. DOSCNT check (rom:12921-12924) — for non-recursive case.
5. DOSFLG check (rom:12926-12929) — for our state DOSFLG=0x1D NZ → JR
   PTDOS.
6. PTDOS (rom:12944) — A=48 NZ, fall through. Pages SAMDOS in section
   B (`OUT (250),A` with A=DOSFLG-1).
7. CALL 4203H (rom:12964) — calls SAMDOS's `syntax` handler at
   samdos+0x203 (cite `~/git/samdos/src/b.s:319-322` `org gnd+0x200; jp
   hook; jp syntax; jp nmi`).
8. samdos `syntax` (b.s:355-434) — for A=48 (≠ 29 "notund"), JP synt3
   → `LD E,0; RET` (b.s:432-434).
9. Back in ROM at rom:12970-12999: DOSC restores stack. CY was set
   before CALL 4203 (rom:12965 SCF "COMING FROM ERROR"), so JR NC
   skips the stack-clear. DOSNC at rom:12989: AND A on A=48 → NZ → JR
   NORMERR.
10. NORMERR at rom:12936-12940: `LD (ERRNR),A=48; RES 0,(DOSCNT); LD
    SP,(ERRSP); JP SETSTK`.
11. SETSTK at rom:7010-7012: `LD HL,(FPSBOT); LD (STKEND),HL; RET`.
    The RET pops the address pushed at MNINIT's `LD HL,MAINER; PUSH
    HL` → returns to MAINER.
12. MAINER at rom:3808+ → MAINER3 at rom:3877: `CALL CLSLOWER; SET
    5,(TVFLAG); RES 7,(FLAGS); LD A,(ERRNR); CALL ERRHAND1; JP MAINELP`.
13. ERRHAND1 at rom:3887 → for ERRNR=48 (non-special): JR NZ,EHZ →
    EH0 → EH15: prints message via POMSR.

The error message "Invalid CLEAR address" is in the error table at
ERRMSGS (set by MAIT block from rom:26986). Should print fine.

**Predicted user experience**: error message printed on lower screen,
"OK" or similar, system idles at MAINELP waiting for input. NOT a
page-displaced screen.

**Empirical user experience**: page-displaced screen, palette differs,
~12s recovery to cold-boot splash.

The mechanisms I've checked that COULD cause the visualization gap:

1. **CLSLOWER at rom:3877 reads sysvars set by MNINIT/MODET** — none
   in XOINTERS list, all should be valid.
2. **SETMIN at MAINELP rom:3752 might re-init WORKSP/WKEND** — yes,
   per rom:6992-7012, but that's AFTER the error message print, so it
   shouldn't affect visualization.
3. **The error reporter's POMSR uses CHANS/CURCHL/STREAMS** — at
   0x5C4F+, not in XOINTERS list. Should be fine.

I cannot identify from static analysis what specifically causes the
"page-displaced screen" visualization. **Runtime instrumentation is
needed** to localise the failure within the error-print path or
post-error MAINELP setup.

---

## 3. Why §5 of clear-remaining-diff.md was wrong

The previous agent's mechanism §5 made two compounding errors:

1. **Missed the 20-byte LDIR at rom:13226-13228.** Their CLRSR trace
   counted 46+26 = 72 bytes of writes before the 0x200 gap, giving
   SAVARS = NVARS+72+0x200 = NVARS+584. The actual count is 46+26+20
   = 92 bytes, giving SAVARS = NVARS+92+0x200 = NVARS+604. Cite:
   independent re-read of rom:13209-13234.

2. **Transcribed MNINIT addresses as ECEC/ECEF.** The actual MNINIT
   ELINE write is at ED01-ED02 (`INC HL; LD (ELINE),HL`) per the ROM
   disassembly. ECEC was earlier (it's `JR NEW2` at rom:24582).

With the corrected math: SAVARS = NVARS+604, ELINE = SAVARS+1 =
NVARS+605. At MNINIT-time NVARS=PROG+1, so ELINE=PROG+606.

After LDPRDT MKRBIG-XOINTERS: ELINE shifts by +body_size.

After LDPROG: NVARS=PROG+body_NVARS_triplet (=57 for our case).

So ELINE-NVARS at AUTO-RUN entry = PROG+606+body - PROG-NVARS_triplet
= 606 + body - NVARS_triplet.

For our 57-byte body with NVARS_triplet=57: 606+57-57 = 606.

Subtract 0x025D=605: AHL = +1 (positive, NOT negative).

So §5's "ELINE-NVARS-0x025D goes negative" is wrong for our state.

---

## 4. Why §7.1 padding is the WRONG fix

The previous agent's §7.1 recommended padding the body to ≥605 bytes
to "fix" the supposed negative-AHL issue. With my §1.7 analysis, this
fix is **counterproductive**:

- Larger body → MKRBIG-XOINTERS shifts WKEND by MORE bytes
- Larger WKEND → WKEND+180 GREATER
- More likely to fail the (WKEND+180) > target check

Specifically, padding our body to 825 bytes (per §7.1's example):
- WKEND post-LDPRDT = PROG+608+825 = PROG+1433 = linear 0x6261
  (assuming PROG=0x5CD5)
- Wait but CLEAR's RECL2BIG would also shift differently. With the
  larger body, the AHL after CLEAR's SUBAHLBC would be:
  ELINE-NVARS = (PROG+606+825) - (PROG+57) = 1374. AHL = 1374-605 =
  769. RECL2BIG with ABC=769 reclaims 769 bytes at NVARS. After:
  WKEND-769 = PROG+1433-769 = PROG+664. **Same as 57-byte body
  outcome!**

Actually wait — the RECL2BIG-XOINTERS adjusts sysvars by -769 (the
reclaim count). So WKEND post-CLEAR = PROG+1433-769 = PROG+664.
WKEND+180 = PROG+844 = linear 0x6021. > target=0x5FFF.

**RTERR fires regardless of body padding.** The fix is structurally
wrong.

This further confirms §7.1 should not be applied.

---

## 5. The actual (cited) constraint

For CLEAR n to succeed in our setup, the post-LDPRDT-RECL2BIG-CLRSR
state must satisfy:

- WKEND+180 < target_linear

The WKEND post-everything depends on:
- MNINIT_WKEND (= PROG+608, fixed by SAM ROM)
- body_size (shifts WKEND up by +body_size during MKRBIG)
- CLEAR-RECL2BIG adjustment (= ELINE-NVARS-0x025D = body_size_NVARS_triplet shift adjustments)

For our 57-byte body with NVARS-triplet=57:
- WKEND_post_LDPRDT = PROG+608+57 = PROG+665
- CLEAR-RECL2BIG reclaim = 1 (-1 to sysvars)
- WKEND_post_CLEAR_RECL2BIG = PROG+664
- WKEND+180 = PROG+844 = 0x6021
- target=24575=0x5FFF
- 0x6021 > 0x5FFF → **RTERR fires**

For interactive `CLEAR 24575` (no body load):
- WKEND = PROG+608 (MNINIT-time)
- WKEND+180 = PROG+788 = 0x5FE9
- 0x5FE9 < 0x5FFF → no RTERR. **CLEAR succeeds.**

The constraint to make AUTO-RUN CLEAR succeed: the LDPRDT-MKRBIG shift
must be small enough that WKEND_post_CLEAR_RECL2BIG+180 < target.

Equivalently:
- body_size + 180 + 608 - reclaim_count < target_offset_from_PROG
- For target=24575, target_offset = 24575-PROG = 24575-23765 = 810.
- body_size + 788 - reclaim_count < 810
- body_size - reclaim_count < 22
- reclaim_count = (NVARS-PROG_triplet difference + ELINE shift - 0x025D)

For our case: reclaim_count = 1, body_size = 57. 57 - 1 = 56 > 22. RTERR.
For Defender: reclaim_count = 660-148 = 512 (numeric area), body=660.
  660-512 = 148 > 22 — but Defender CLEAR target is 32767, not 24575.
  target_offset = 32767-PROG = 32767-23765 = 9002. 148 < 9002. No
  RTERR.

**For our M0 use case (CLEAR 24575 with body sized to our 1-line
program), the WKEND-validation check fundamentally cannot pass without
either:**

- (a) increasing the CLEAR target (e.g. CLEAR 32767), OR
- (b) using a larger body whose CLRSR-reclaim makes the net WKEND
  shift small (e.g. SAVARS-PROG triplet such that the reclaim size
  cancels the body-size shift).

Defender's body shape is the latter: SAVARS-PROG=660 (=body length),
NVARS-PROG=56. After LDPROG: NVARS=PROG+56, ELINE-NVARS = (PROG+606+
body)-NVARS = 1210 (with body=660). AHL after SUBAHLBC = 605. RECL2BIG
reclaims 605 bytes. WKEND post = PROG+608+660-605 = PROG+663 (close to
post-CLEAR WKEND for our 57-byte case).

Hmm wait that's PROG+663 + 180 = PROG+843 = 0x6020. still > 0x5FFF.

But Defender uses CLEAR 32767, so its target is 0x7FFF, well above
WKEND+180. So Defender doesn't hit RTERR.

OK so this is body-size + CLEAR-target combined. **The mechanism is
not "body too small" but rather "body too small for CLEAR-target this
close to PROG"**.

If I wanted to make AUTO-RUN CLEAR 24575 work without changing the
target, I'd need to make the body have a NVARS-PROG triplet that's
LARGER than 22 + 0x025D - 605 = 22 (i.e. NVARS-PROG ≥ 22 + reclaim
amount). But actually...

Actually let me redo. We want WKEND_post_CLEAR_RECL2BIG + 180 < target.

WKEND_post = WKEND_post_MKRBIG - reclaim_count
           = (PROG+608+body) - reclaim
           = PROG + 608 + body - reclaim

Where reclaim = AHL post-SUBAHLBC = ELINE_post_MKRBIG - NVARS_post_LDPROG - 0x025D
             = (PROG+606+body) - (PROG+nvars_triplet) - 605
             = body - nvars_triplet + 1

So WKEND_post = PROG + 608 + body - (body - nvars_triplet + 1)
             = PROG + 607 + nvars_triplet

Interesting! WKEND_post depends on **nvars_triplet only**, not body.

WKEND+180 = PROG + 787 + nvars_triplet < target
          = 24575 - PROG = 810
          → nvars_triplet < 23

For our case, nvars_triplet = 57. 57 < 23? No. RTERR.

For interactive case (no LDPRDT): WKEND = PROG+608. WKEND+180 =
PROG+788. 788 < 810. No RTERR.

**So the actual constraint for AUTO-RUN `CLEAR 24575` to succeed: the
NVARS-PROG triplet (the encoded program length) must be < 23 bytes.**

Our minimal program is 57 bytes. We CANNOT make it ≤ 22 bytes while
keeping the CLEAR/LOAD/CALL on a single line. (Just `CLEAR 24575:
LOAD "stub" CODE 24576: CALL 24576` is ~52 bytes tokenized, plus 4
bytes of line header + 1 byte FF terminator = ~57 bytes minimum.)

**Therefore: the M0 auto-RUN line, as currently structured, CANNOT
include `CLEAR 24575` if we use the canonical SAM auto-RUN BASIC
mechanism — the WKEND-vs-RAMTOP check at rom:13186 fundamentally
fails.**

(But empirically we see crash, not error 48 — so something more
subtle is also happening. See §2 visualization gap.)

---

## 6. Spot-check guide

Pete should verify these citations before acting:

1. **rom:13209-13234 (CLRSR full count)**: verify the 26+20 LDIR
   sequence (PSVTAB then PSVT2) gives 92 bytes total written between
   NVARS and the 0x200 gap. Specifically that the `LD HL,PSVT2; LD
   C,20; LDIR` at rom:13226-13228 actually exists.

   ```
   3987 21E339   LD HL,PSVTAB
   398A 0E1A     LD C,26
   398C EDB0     LDIR              ;COPY 3 PTRS AND YOS/YRG
   398E 21E939   LD HL,PSVT2
   3991 0E14     LD C,20
   3993 EDB0     LDIR              ;COPY YOS/YRG AGAIN
   ```

   This is the bug in §5 of clear-remaining-diff.md.

2. **rom:13178-13192 (WKEND-RAMTOP check)**: verify the math:

   ```
   393D 2A8E5A    LD HL,(WKEND)
   3940 3A8D5A    LD A,(WKENDP)
   3943 01B400    LD BC,180        ;**
   3946 CDCC1F    CALL ADDAHLBC    ;AHL=WKEND+180
   3949 D1        POP DE
   394A C1        POP BC           ;CDE=CLEAR PARAM
   394B CDE71F    CALL SUBAHLCDE
   394E 3006      JR NC,RTERR      ;JR IF RAMTOP WILL BE TOO CLOSE TO WKEND
   ```

   Confirm: NC means SUBAHLCDE saw HL >= DE (no borrow); equivalently
   WKEND+180 >= target → RTERR.

3. **rom:7148-7173 (MKRBIG → XOINTERS MAKEROOM mode)**: verify that
   MKRBIG opens BC bytes by calling XOINTERS in MAKEROOM mode (CY'
   clear). The path is: `MAKEROOM: XOR A` (which clears CY) → fall
   through to `MKRBIG`. CY' = whatever caller had. For LDPRDT's
   call (rom:E37F), there's no SCF/CCF before. So CY' remains whatever
   it was from a prior operation. RECL2BIG at rom:E35D set CY' via
   SCF before its own RST 30 — but RECL2BIG returned with whatever CY
   ended up as.

   **This is a citation-grounded uncertainty**. If MKRBIG happens to
   inherit CY'=1 from RECL2BIG's earlier SCF, XOINTERS would run in
   RECLAIM mode during MKRBIG, which would shift sysvars in the WRONG
   direction.

   Pete: this needs runtime verification — what's HMPR/sysvar state
   actually look like at LDPROG-RET in our test?

---

## 7. What I couldn't determine

Honest gaps:

1. **The exact reason for the page-displaced-screen visualization
   instead of a clean "Invalid CLEAR address" error**. My analysis
   predicts error 48 fires cleanly via the standard error reporter.
   The empirical observation is a memory-corruption-style crash. The
   gap is unbridged from static analysis. Possibilities:
   - The error-reporting path (PTDOS → SAMDOS syntax → ROM CLSLOWER →
     ERRHAND1 → POMSR) has a subtle dependency on a sysvar that IS in
     XOINTERS' adjusted set.
   - SAMDOS's `syntax` handler at samdos+0x203 does something specific
     for certain error codes that affects screen state.
   - There's a stack-imbalance from CLEAR's PUSHes that aren't matched
     by RTERR's "JP" target. CLEAR pushed BC,HL at rom:13161-13162
     then RTERR jumps without POPping. This could cause stack-stack
     popping when MAINER restores SP from ERRSP — but ERRSP is intact,
     so SP gets correctly reset to ISPVAL-2.

2. **The exact CY' state at MKRBIG entry from LDPRDT.** If CY' is
   indeterminate, my +57 shift assumption for WKEND could be wrong.
   Need runtime trace.

3. **What `WKEND+180` actually is in our specific test**. The
   visualization differs across cache-fix vs no-cache-fix versions
   (per `2026-05-10-handoff.md:53-54`); my analysis predicts the
   cache-fix shouldn't matter for the WKEND-RAMTOP check, but
   empirically the visualization changed. Implies my mechanism is
   incomplete.

4. **Whether the BASIC dispatcher path between BTHK return and
   LDPRDT entry alters any sysvars I haven't considered.** Per
   `samdos2-auto-run-analysis.md`, SAMDOS's BTHK handler is just
   `init` (sets CURCMD=LOAD, RETs). Whatever ROM logic dispatches
   the AUTO-file LOAD afterward is not fully traced.

---

## 8. Recommended next steps

Given the static-analysis-determined mechanism (RTERR via
WKEND-RAMTOP) and the unbridged gap to empirical visualization:

### 8.1 Runtime instrumentation

This is the only definitive next step. Add to simcoupe (or the local
patch):

- PC-trigger logger at rom:13174 (after CLEAR's RECL2BIG), rom:13177
  (entering MCLS), rom:13186 (WKEND-RAMTOP check), rom:13191 (RTERR
  itself).
- Log A/HL/BC/DE/IX/IY/SP, LMPR/HMPR/VMPR.
- Log key sysvars: NVARS/NUMEND/SAVARS/WKEND/ELINE (page+addr each).

Run AUTO-RUN with `10 OUT 254, 4: CLEAR 24575: OUT 254, 7` and capture.
Inspect the log:

- Does PC reach rom:13186 (WKEND-RAMTOP check)? If yes, my §1.7
  prediction is on the right track.
- Does PC reach rom:13191 (RTERR)? If yes, the error path glitches at
  visual time but otherwise operates.
- Does PC reach MAINER3 at rom:3877 (the post-error path)?
- What's WKEND/ELINE at rom:13177 (entering MCLS)?

### 8.2 Empirical experiment (Pete's call)

If Pete chooses to experiment, the most informative empirical test is
to **change the CLEAR target to a value safely above WKEND+180**:

- Try `CLEAR 28000` instead of `CLEAR 24575`. WKEND+180 = ~0x6021.
  28000 = 0x6D60, well above. Should pass WKEND-RAMTOP check.

If CLEAR 28000 works in AUTO-RUN, the §1.7 mechanism is confirmed.

If CLEAR 28000 still crashes the same way, the mechanism is
elsewhere.

### 8.3 Alternative: don't use CLEAR

Per `clear_in_auto_run.md` and `m0-status.md`, dropping CLEAR has been
empirically shown to work with `LOAD CODE: CALL` without CLEAR. This
is a workaround. Per the PRIME DIRECTIVE this should not be applied
unless the alternatives are exhausted.

But: in light of the §1.7 analysis showing a fundamental incompat
between (small body + CLEAR target near PROG), dropping CLEAR may be
the only correct path that doesn't compromise the M0 design. The CLEAR
24575 is canonical SAM convention but only works when the body+CLEAR
combination satisfies the WKEND-RAMTOP arithmetic — which our M0
shape doesn't.

Pete's call.

---

## 9. Sources cited

| Tag | Path | Lines used |
|-----|------|------------|
| ROM | `docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt` | 264 (RST 30 entry), 581-614 (RST30L2/L3/L4, R1ONCLBC), 866-915 (sysvar EQUs, "POINTERS THAT ARE ADJUSTED" comment), 999-1018 (BCSTORE, CURCMD, AUTOFLG, COMPFLG), 1057 (BASSTK), 1234 (BSTACK=0x4AFF), 2080-2200 (MCLS/CLSBL/CLU1/CLSLOWER), 3478-3489 (LINERUN), 3744-3825 (MAINEXEC main loop), 6992-7012 (SETMIN/SETWORK/SETSTK), 7141-7173 (MKRBIG), 7187-7227 (RECL2BIG), 7253-7307 (ASSV/PNLP/PADJ), 7370-7402 (ADDRPROG/ADDRELN/ADDRSV/ASV2), 7541-7589 (ADDAHLBC/SUBAHLBC/ADDAHLCDE/SUBAHLCDE/PAGEFORM), 7654-7659 (RDTHREE), 9971-10030 (FARLDIR/FARLDDR/STRMOV), 11991-12011 (SCOMP/GT4P/GT4R), 12013-12068 (DOCOMP/COMPILE), 12222-12223 (RESTOREZ→RESTORE2), 12906-12999 (ERROR2/PTDOS/DOSC), 13141-13247 (CLEAR/CLR1/CLR3/CLR4/CLRSR/SETSAV/SETNE/SETSYS), 13286-13298 (PSVTAB/PSVT2 data), 14609-14661 (TSTRMBIG/TSTRMABC/TESTROOM), 14773-14786 (UNSTLEN), 14790-14861 (SPSSR/SPSS/SELSCRN/SELURPG/RCURPR), 19181-19222 (LASTPAGE update via OPEN), 20453-20598 (BOOT/BOOTNR/BOOTEX/BTNOE), 22369-22713 (LVMMAIN/LDFL/LDPRDT/LDPROG/LDUSLN), 23536-23633 (XOINTERS/SMBW/SMBS/SADJ), 24430-24700 (MNINIT/NEW2/SETMIN/MODET/CLSHS2/SETSAV/CLRSR), 26979 (MAIT). |
| TM | `docs/sam/sam-coupe_tech-man_v3-0.txt` | 887-906 (sections), 1063-1125 (LMPR/HMPR/VMPR layout), 4262-4400 (disk format/dir entry), 4459-4486 (UIFA layout), 4524, 4548 (hook 128 spec) |
| samdos-b | `~/git/samdos/src/b.s` | 1-50 (header/dos loader), 130-200 (dvar/extadd/onerr), 203-322 (org +200; jp hook; jp syntax; jp nmi), 355-434 (syntax handler), 437-470 (hook dispatcher), 497-540 (samhk hook table) |
| samdos-h | `~/git/samdos/src/h.s` | 1-26 (rxhed), 38-67 (txhed/hgthd), 132-156 (hsave), 201-237 (autnam/init/initx/hauto), 308-321 (cals) |
| samdos-d | `~/git/samdos/src/d.s` | 157-174 (nrwr) |
| build-disk | `tools/build-disk.sh` | 60-330 (build python block; 198-308 AUTO BASIC + slots) |
| Project notes | `docs/notes/` | clear-remaining-diff.md (refuted §5, applied §2-4), clear-mechanism.md (refuted §6), clear-investigation.md (refuted §6), 2026-05-10-handoff.md (rolling state), samdos2-auto-run-analysis.md (SAMDOS doesn't auto-run; FRED uses custom bootstrap) |
| Memory | `~/.claude/projects/-Users-pmoore-git-sam-aarch64/memory/` | clear_in_auto_run.md (no-skip-CLEAR rule, refuted hypotheses), feedback_correctness_over_workarounds.md (PRIME DIRECTIVE), feedback_docs_first.md (research-first) |

---

## 10. Verdict summary

**(a) cited root cause + mechanism**:

CLEAR 24575 in AUTO-RUN context fails the WKEND-vs-RAMTOP validation
at `rom:13186-13192`. The check `(WKEND+180) - target_linear` returns
NC (positive) for our specific combination of:
- 57-byte body (NVARS-PROG triplet=57)
- target=24575 (just above LOAD CODE addr 24576)

After LDPRDT's MKRBIG-XOINTERS shifts WKEND by +57, then CLEAR's
RECL2BIG-XOINTERS reclaim shifts back by -1, post-state WKEND =
PROG+664. WKEND+180 = linear 0x6021 > 0x5FFF=target. RTERR fires
(error 48 "Invalid CLEAR address").

For the interactive case (no body load), WKEND stays at MNINIT-time
PROG+608. WKEND+180 = 0x5FE9 < 0x5FFF. No RTERR. CLEAR succeeds.

**This explains why interactive CLEAR works and AUTO-RUN CLEAR
fails: WKEND has shifted +57 in AUTO-RUN.**

**Confidence: medium-high.** The math is independently verifiable
from ROM citations. The discriminator is correctly identified as
"body shifts WKEND".

**(b) Honest gap**: The visualization differs from what error 48
would produce — my mechanism predicts a clean error message, not a
page-displaced screen. The cause of the visualization-level failure
is not statically determinable. Runtime instrumentation is needed to
verify whether RTERR actually fires (vs the crash happening
elsewhere) and what specifically goes wrong in the error display.

**No padding fix is possible**: padding the body to 825 bytes (per
clear-remaining-diff.md §7.1) does NOT fix the issue — the
RECL2BIG-reclaim cancels out body-size growth, leaving WKEND post-
everything dependent only on NVARS-PROG triplet (which our 57-byte
program makes unavoidably ≥ 57). For AUTO-RUN CLEAR 24575 to succeed
with our program, NVARS-PROG would need to be < 23, which is
impossible for our minimum 1-line program.

**Recommendation**: Pete should run the runtime experiment in §8.1,
or empirically test §8.2's `CLEAR 28000` substitute. If CLEAR 28000
works, mechanism confirmed and Pete can decide between (i) using a
larger CLEAR target in M0, (ii) using a different M0 architecture
that avoids CLEAR, or (iii) digging deeper into why error 48 doesn't
display cleanly.
