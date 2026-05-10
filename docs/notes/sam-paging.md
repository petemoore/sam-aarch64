# SAM Coupé memory paging — reference

Authoritative reference for the SAM Coupé's paging hardware, the ROM's
"REL PAGE FORM" addressing convention, the BASIC sysvars that record paging
state, and SAMDOS's interaction with all of it. Every claim is backed by a
source citation in the form `file:line`. The two principal sources are:

- `docs/sam/sam-coupe_tech-man_v3-0.txt` — Bruce Gordon's Technical Manual v3.0.
- `docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt` — annotated ROM v3.0
  disassembly. Note the disasm uses "URPORT/LRPORT" where the Tech Manual uses
  "HMPR/LMPR" (`docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt:27-29`);
  this document uses both pairs interchangeably.

For the file-format applications of REL PAGE FORM (the 9-byte file header,
EXEC addr encoding, etc.), see `docs/notes/sam-file-header.md`.

---

## TL;DR

- The Z80's 64K address space is split into four 16K **sections** A/B/C/D at
  `0x0000`, `0x4000`, `0x8000`, `0xC000`. There are 32 physical 16K **pages**
  (0..31) on a 512K machine, 16 (0..15) on a 256K machine.
- Three I/O ports drive paging:
  **LMPR** (port `0xFA` / 250) = section A page; section B = LMPR+1; LMPR also
  banks ROM0 (bit 5) and ROM1 (bit 6).
  **HMPR** (port `0xFB` / 251, called "URPORT" in the disasm) = section C page;
  section D = HMPR+1.
  **VMPR** (port `0xFC` / 252) = displayed screen page (independent of CPU
  paging).
- "REL PAGE FORM" is the (page-byte, 16-bit-offset) representation BASIC stores
  for any address ≥ 0x4000, including RAMTOP, file-header start/length/exec
  fields, and `CLEAR n` arguments. The page byte is the **physical page number**
  (0–31) where the byte lives, computed as `N DIV 16384`. The 16-bit offset is
  `(N MOD 16384) | 0x8000` — i.e. forced into section C's address range as a
  byte-pointer convention. ROM `UNSTLEN` does the encoding
  (`...rom-v3.0_annotated-disassembly.txt:14773-14786`).
- `PDPSR2` (`0x1279`) decodes a REL PAGE FORM (page, offset) to a Z80 address
  by paging the page in. For page < 4 it ASSUMES LMPR=0 / HMPR=1 (the default
  "BASIC layout") and fans the page byte out to one of the four sections; for
  page ≥ 4 it pages it in at section C (HMPR ← page-1) and offsets HL into
  0x8000+. **This is the source of the build-disk bug**: a saved page-0 EXEC
  addr decodes to section A, which holds ROM0, not user RAM, unless the caller
  has flipped LMPR bit 5 first.
- BASIC owns 4 physical pages (0,1,2,3) at boot. RAMTOP starts at `(page=4,
  offset=0xBFFF)` = 81919, which means "the byte just below page 4". User code
  living at logical addresses 0x4000–0xBFFF lives in physical page 1 or 2 and
  is reachable via the default LMPR=0 layout.
- SAMDOS resides in **one page** (recorded in DOSFLG, sysvar `&5BC2`) at logical
  `&8000–&BFFF` (section C) — paged in via HMPR ← DOSFLG. When the ROM dispatches
  a DOS hook, it actually pages the DOS at section B (LMPR ← DOSFLG-1) so it
  has a stack at &8000 and the DOS can use C/D for buffers; see "SAMDOS hook
  dispatch" below.
- Always save HMPR (and/or LMPR) around any paging op that crosses a public ROM
  call. The ROM's `R1OSR` / `POPOUT` (`...rom-v3.0_annotated-disassembly.txt:13908-13922`)
  and `R1OFFCLBC` (`...rom-v3.0_annotated-disassembly.txt:639-652`) macros are the
  canonical patterns.

---

## 1. Z80 address space → sections A/B/C/D

The Z80 sees 64K, divided into four 16K-aligned **sections**:

```
    0x0000      0x4000      0x8000      0xC000      0xFFFF
       SECTION    SECTION     SECTION     SECTION
          A          B           C           D
       (LMPR)    (LMPR+1)     (HMPR)     (HMPR+1)

         |---- BLOCK A.B ----|---- BLOCK C.D ----|
         |    (controlled    |    (controlled    |
         |     by LMPR)      |     by HMPR)      |
```

Tech Manual at `docs/sam/sam-coupe_tech-man_v3-0.txt:891-906`: "envisage
the 64K addressing range of the Z80 as 2 blocks 2 of sections of 16K… LMPR
manages the block A.B, and the HMPR manages the block C.D."

There are **32 physical 16K pages** on a 512K machine, **16 pages** on a 256K
machine; the basic-machine page-numbering range is 0..15
(`...tech-man_v3-0.txt:887-889`). PRAMTP (sysvar `&5CB4`) records the last
physical page actually present at boot: typically `0x0F` (256K) or `0x1F`
(512K) (`...rom-v3.0_annotated-disassembly.txt:24493`).

### LMPR and HMPR pair pages automatically

Writing page N to LMPR places page N in section A and **page N+1
automatically in section B** (`...tech-man_v3-0.txt:908-910`). The same
applies to HMPR / sections C+D (`...tech-man_v3-0.txt:912-914`). You can't
map non-adjacent pages to A and B.

---

## 2. The three page registers

### LMPR — Low Memory Page Register, port `0xFA` (250)

Bit-level layout (`...tech-man_v3-0.txt:1106-1125`):

| Bit | Name  | Function |
|-----|-------|----------|
| 0   | BCD 1 | Section A page bit 0 |
| 1   | BCD 2 | Section A page bit 1 |
| 2   | BCD 4 | Section A page bit 2 |
| 3   | BCD 8 | Section A page bit 3 |
| 4   | BCD 16 | Section A page bit 4 (bank — only used on >256K) |
| 5   | RAM0  | 1 = RAM (page = LMPR low 5 bits) in section A; 0 = ROM0 in section A |
| 6   | ROM1  | 1 = ROM1 in section D (overrides HMPR mapping for D); 0 = RAM (page = HMPR+1) |
| 7   | WPRAM | 1 = write-protect RAM in section A |

So the low 5 bits select the section A RAM page; section B is always
section-A-page+1. Bit 5 is the **ROM0 disable**: when bit 5 is 0 (the BASIC
default), section A is ROM0 regardless of what's in the low 5 bits, and the
RAM page selected by those bits is "shadowed" but only visible if bit 5 is
flipped. Bit 6 is the **ROM1 enable**: when set, section D shows ROM1,
overriding HMPR's section-D page.

The ROM-default LMPR after boot is `0x1F` (page 31 selected, ROM0 visible,
ROM1 off — shown by L13066 `LD A,1FH; OUT (250),A` when turning ROM1 off
inside `BUFMV` `...rom-v3.0_annotated-disassembly.txt:13063-13066`). When the
ROM wants ROM1 on it sets LMPR to `0x5F` (`...rom-v3.0_annotated-disassembly.txt:13062-13063`,
`LD A,5FH; OUT (250),A — ROM1 ON`). Bit 5 stays low for normal BASIC operation,
so the four BASIC pages 0,1,2,3 (see §5) are visible at A,B,C,D when LMPR=0
and HMPR=1.

### HMPR — High Memory Page Register, port `0xFB` (251)

Also called **URPORT** in the disasm (`...rom-v3.0_annotated-disassembly.txt:1280`,
`URPORT EQU 0FBH`).

Bit-level layout (`...tech-man_v3-0.txt:1081-1100`):

| Bit | Name   | Function |
|-----|--------|----------|
| 0   | BCD 1  | Section C page bit 0 |
| 1   | BCD 2  | Section C page bit 1 |
| 2   | BCD 3  | Section C page bit 2 (Tech Manual notation; in practice bit 2 = page bit 2) |
| 3   | BCD 4  | Section C page bit 3 |
| 4   | BCD 16 | Section C page bit 4 (>256K bank) |
| 5   | MD3S0  | Mode-3 CLUT address bit 4 |
| 6   | MD3S1  | Mode-3 CLUT address bit 5 |
| 7   | MCNTRL | 1 ⇒ external memory expansion drives sections C+D |

So **the low 5 bits select the section C page** (the Tech Manual's "BCD 3"
naming for bit 2 looks like a typo; bits 0–4 form a 5-bit page number that
matches LMPR's). Section D is always section-C-page+1. Bits 5–6 are reused
for the high two bits of the CLUT lookup address in screen mode 3 only;
**this means HMPR's value affects mode 3 colours**. Bit 7 forces external
expansion memory.

### VMPR — Video Memory Page Register, port `0xFC` (252)

Bit-level layout (`...tech-man_v3-0.txt:1063-1078`):

| Bit | Name   | Function |
|-----|--------|----------|
| 0–3 | BCD 1–8 | Video page bits 0–3 |
| 4   | BCD 16  | Video page bit 4 (bank) |
| 5   | MDE0    | Screen-mode bit 0 |
| 6   | MDE1    | Screen-mode bit 1 |
| 7   | TXMIDI / RXMIDI | MIDI OUT bit / MIDI IN bit (write/read) |

Low 5 bits select the **displayed** screen page **independently** of CPU
paging — VMPR doesn't change what the CPU sees at any address. Modes 3 and 4
use 24K and wrap into the next page (`...tech-man_v3-0.txt:947-955`): writing
page 12 to VMPR shows pages 12 and 13.

To find the screen base from BASIC the User Guide gives
`(IN 252 BAND 31) * 16384` (`...tech-man_v3-0.txt:998-1000`).

### Side-by-side semantics

| Register | Port (dec / hex) | Selects                       | Pair page             | Other functions in same byte |
|----------|------------------|-------------------------------|-----------------------|------------------------------|
| LMPR     | 250 / `0xFA`     | Section A page (0–31)         | Section B = LMPR+1    | Bit 5 ROM0 disable, bit 6 ROM1 enable, bit 7 WPRAM |
| HMPR     | 251 / `0xFB`     | Section C page (0–31)         | Section D = HMPR+1    | Bits 5–6 mode-3 CLUT high bits, bit 7 external mem |
| VMPR     | 252 / `0xFC`     | Displayed screen page (0–31)  | Wraps even→odd in 24K modes | Bits 5–6 screen mode, bit 7 MIDI |

---

## 3. ROM/SAMDOS save-restore patterns for paging registers

Any ROM routine that touches LMPR or HMPR must restore the entry value before
returning, otherwise it will corrupt the caller's mapping. The ROM has three
canonical helpers for this:

### `R1OSR` (`0x3C49`) and `POPOUT` (`0x3C42`)

`...rom-v3.0_annotated-disassembly.txt:13908-13922`:

```
3C42  POPOUT:    POP AF
      D3FA       OUT (250),A         ; restore LMPR
      F1   PPORT: POP AF
      D3FB       OUT (251),A         ; restore HMPR
      C9         RET
3C49  R1OSR:     POP IY
      DBFB       IN A,(251)
      F5         PUSH AF             ; save HMPR
      DBFA       IN A,(250)
      F5         PUSH AF             ; save LMPR
      E6BF       AND 0BFH
      D3FA       OUT (250),A         ; ROM1 OFF
      FDE9       JP (IY)
```

`R1OSR` is the entry pattern: it pops its own return addr into IY (so the
code uses `JP (IY)` to dispatch back), pushes HMPR then LMPR onto the stack,
turns ROM1 off, and `JP (IY)` to the calling code. The matching exit is
`POPOUT` which pops back LMPR then HMPR. This makes `R1OSR` / `POPOUT` a
bracket pair around any ROM routine that needs ROM1 off and arbitrary
LMPR/HMPR mucking. Used e.g. by `EPSUB` (`...rom-v3.0_annotated-disassembly.txt:13904`),
`STRCOMP` (`...rom-v3.0_annotated-disassembly.txt:14499`), and many others.

### `R1OFFCLBC` (`0x0207`)

`...rom-v3.0_annotated-disassembly.txt:639-652`:

```
0207  R1OFFCLBC: EX AF,AF'
0208  R1OF2:     IN A,(250)
      F5         PUSH AF             ; ORIG URPORT STATUS [sic — actually LMPR]
      E6BF       AND 0BFH            ; ROM1 BIT OFF
0207  R1OFON:    OUT (250),A         ; ROM1 OFF/ON
      08         EX AF,AF'
      CD1902     CALL LDBCJP         ; CALL BC
      08         EX AF,AF'
      F1         POP AF
      D3FA       OUT (250),A         ; restore LMPR
      08         EX AF,AF'
      C9         RET
```

(Note the comment in the disasm at L642 says "ORIG URPORT STATUS" but the
code is reading port 250 = LMPR; that comment is wrong — what's pushed is
LMPR.) `R1OFFCLBC` calls a routine via BC with ROM1 off and the original LMPR
restored on return. It does NOT save/restore HMPR. This is the helper used by
the LOAD CODE EXEC dispatch (`HDLDEX` at `0xE294`,
`...rom-v3.0_annotated-disassembly.txt:22481-22484`) — meaning the called code
runs with HMPR set by `PDPSR2` and is *not* restored automatically; the caller
of LOAD CODE has to either accept that HMPR has been clobbered or restore it
itself.

### `TSURPG` (`0x3FDF`) — the page-switch helper

`...rom-v3.0_annotated-disassembly.txt:14852-14861`:

```
3FDF  TSURPG:    PUSH HL
      67         LD H,A
      DBFB       IN A,(251)
      AC         XOR H
      E6E0       AND 0E0H            ; KEEP TOP 3 BITS FROM PORT
      AC         XOR H
      D3FB       OUT (251),A
      E1         POP HL
      C9         RET
```

`TSURPG` writes the low 5 bits of A to HMPR, **preserving the top 3 bits** of
the port (so mode-3 CLUT bits 5–6 and external-memory bit 7 are not
disturbed). Same routine is also reached as `SELURPG`. Always use this rather
than a raw `OUT (251),A` if there's any chance of being in screen mode 3 or
of needing the original CLUT bits.

`INCURPAGE`/`DECURPAGE` (`...rom-v3.0_annotated-disassembly.txt:14877-14886`)
build on TSURPG to bump HMPR by ±1 while adjusting HL out of the C/D range.

### Manual save-restore from within ROM/user code

The Tech Manual gives the canonical user-level pattern at lines 2685–2698:

```
IN A,(HMPR)
PUSH AF                 ; save current HMPR status
LD A,1
OUT (HMPR),A            ; page in PART2

IN A,(LMPR)
AND 0BFH
OUT (LMPR),A            ; ROM1 off
CALL PART2
IN A,(LMPR)
OR 40H
OUT (LMPR),A            ; ROM1 on again for RET to calculator
POP AF
OUT (HMPR),A            ; original HMPR status restored
RET
```

This pattern is robust: PUSH AF / change / call / POP AF / OUT.

---

## 4. REL PAGE FORM addressing

### What it is

REL PAGE FORM is the ROM's encoding of any address ≥ 16K as a (page-byte,
16-bit-offset) pair. It is used for:

- File-header start, length, and exec-address fields — see
  `docs/notes/sam-file-header.md` for layout details.
- RAMTOPP / RAMTOP sysvars (`&5CB1` / `&5CB2`) (`...rom-v3.0_annotated-disassembly.txt:1141-1142`).
- WKENDP / WKEND sysvars (`&5A8D` / `&5A8E`) (`...rom-v3.0_annotated-disassembly.txt:881-882`).
- `CLEAR n` parameter (after `UNSTLEN` decomposes the FP-stack value).
- Any other ROM internal that holds a "where in the 512K address space" pointer.

### Encoding: `UNSTLEN` (`0x3F8C`)

`...rom-v3.0_annotated-disassembly.txt:14769-14786`:

```
3F8C  UNSTLEN:   DB CALC          ; N
      DB STK16K        ; N, 16K
      DB MOD           ; N MOD 16K
      DB RCL3          ; N MOD 16K, INT(N/16K) (left by MOD)
      DB EXIT
      CD331D     CALL GETBYTE
      FE21       CP 21H
      D2391D     JP NC,IOORERR    ; PAGE MUST BE 00-20H (0=ROM, 1-20=RAM)
      F5         PUSH AF
      CD2E1D     CALL GETINT      ; TO HL AND BC
      F1         POP AF
      C9         RET              ; A=PAGE, HL=offset
```

- `N MOD 16384` → low 14 bits, returned in HL.
- `N DIV 16384` → page byte, returned in A.
- Page must be 0–32 inclusive (`CP 21H` rejects values ≥ 33).

The `UNSTLEN` block-comment header (line 14770) summarises:

> UNSTACK A 5-BYTE NUMBER TO AN ADDRESS IN A (PAGE) AND HL (8000-BFFF)
> (RES 7,H IF 5-BYTE IS A LENGTH, TO GIVE PAGE, +0000-3FFFH). GIVES IOOR ERROR
> IF NUMBER IS NEGATIVE OR >07FFFF

So:

- For **addresses**: caller does `SET 7, H` → HL is in `0x8000..0xBFFF`. The
  page byte is the **physical** page number where the byte lives. So address
  N decodes to `(page = N / 16384, offset = (N % 16384) | 0x8000)`.
- For **lengths**: HL is left as `0x0000..0x3FFF`; the high bit is *not* set.

### The 0x8000 marker on the offset

When REL PAGE FORM is being used to denote an *address* (not a length), the
offset is forced into the `0x8000..0xBFFF` range — that is, into the section
C address window. This is a presentation convention: it lets the ROM page in
the addressed page via HMPR (so it appears at section C) and use HL directly
as a byte pointer.

`PDPSUBR` (`0x126F`) does the encoding-with-marker for POKE/PEEK/CALL etc.:
`UNSTLEN` then `SET 7, H` (`...rom-v3.0_annotated-disassembly.txt:4495-4496`).

For SAVE encoding the EXEC addr (`HDRNMS` at `0xE104`,
`...rom-v3.0_annotated-disassembly.txt:22217-22226`):

```
CD8C3F     CALL UNSTLEN        ; A=page, HL=offset, in 0x0000-0x3FFF form
D1         POP DE
EB         EX DE,HL            ; HL=ptr to dest, ADE=addr
77         LD (HL),A           ; byte 0: page
23         INC HL
73         LD (HL),E           ; byte 1: offset LSB
23         INC HL
CBFA       SET 7,D             ; force 0x8000 bit on MSB
72         LD (HL),D           ; byte 2: offset MSB | 0x80
```

So a saved REL PAGE FORM 3-byte triple is: `<page>, <offset_LSB>, <offset_MSB | 0x80>`.

### Decoding: `PDPSR2` (`0x1279`)

`...rom-v3.0_annotated-disassembly.txt:4499-4527`:

```
1279  ;PDPSR2. USED BY LOAD CODE (EXEC)
1279  ;ENTRY: AHL=EXEC ADDR
1279  PDPSR2:    PUSH BC
      F5         PUSH AF                      ; KEEP STACK HAPPY
127B  PDPC:      CP 4
      300F       JR NC,PDPSUBR4               ; JR IF NOT 0000-FFFF (page >= 4)
127F             LD C,2                       ; PAGING WILL BE ROM0,BASE,BASE+1,BASE+2
      B9         CP C
      2809       JR Z,PDPSUBR3                ; ADDR IS OK IF PAGE IS 2 (HL stays in 0x8000+)
1284  3005       JR NC,PDPSUBR2               ; JR IF PAGE 3 - ADD 4000H TO ADDR
1286  CBBC       RES 7,H                      ; ADDR NOW 0000-3FFF
      A7         AND A
      2802       JR Z,PDPSUBR3                ; JR IF PAGE 0 - ADDR OK
                                              ; ELSE ADD 4000H FOR PAGE 1 ADDR
128B  PDPSUBR2:  SET 6,H                      ; ADD 4000H TO ADDR
128D  PDPSUBR3:  LD A,C
128E  PDPSUBR4:  DEC A
      CDDF3F     CALL TSURPG
      F1         POP AF                       ; ORIG URPORT
      C1         POP BC
      C9         RET
```

The "PAGING WILL BE ROM0, BASE, BASE+1, BASE+2" comment is the key. PDPSR2
fans out the 4 BASIC pages to the 4 sections like so:

| Encoded page (A) | After PDPSR2: HL                 | TSURPG arg | Where the data really is |
|------------------|----------------------------------|------------|--------------------------|
| 0                | `RES 7,H` → `0x0000..0x3FFF`     | A=1 (sets HMPR low5=1) | Section A (ROM0 unless caller flips LMPR bit 5) |
| 1                | `RES 7,H` then `SET 6,H` → `0x4000..0x7FFF` | A=1 | Section B (page LMPR+1) |
| 2                | unchanged: `0x8000..0xBFFF`       | A=1 | Section C (page = HMPR low5 = 1) |
| 3                | `SET 6,H` → `0xC000..0xFFFF`     | A=1 | Section D (page = HMPR low5 + 1 = 2) |
| ≥ 4              | unchanged: `0x8000..0xBFFF`       | A = page-1 | Section C (page = HMPR low5 = page-1), Section D = page |

For pages 0–3, **PDPSR2 always sets HMPR low5 = 1**. This works only if the
caller has set up LMPR low5 = 0 (so section A is page 0 / ROM0, section B is
page 1). With LMPR=0, HMPR=1 ⇒ map = (ROM0 or page 0, page 1, page 1, page 2).
That means encoded pages 0, 1, 2, 3 land in physical pages 0, 1, 1, 2
respectively.

**Asymmetry**: the page numbers PDPSR2 understands for the "low" range (0–3)
are physical-page numbers 0–3; pages 1 and 2 *both* end up via HMPR=1 in
section C, but with different HL adjustments to land them in different Z80
sections. The encoding, then, conflates "logical layout slot" with "physical
page" only for the BASIC-default case where the four BASIC pages happen to
be physical 0,1,2,3. **If LMPR has been altered (e.g. user code flipped bit 5
to access physical page 0 RAM in section A), PDPSR2's decoding silently
breaks**.

### Practical consequence: the build-disk EXEC bug

`tools/build-disk.sh:209-214` documents the bug Pete hit:

> If the dir entry has byte 0xf2 != 0xff the ROM (E281–E299) takes the
> HDLDEX path: PDPSR2 corrupts URPORT (HMPR) and JPs to R1OFFCLBC at
> the encoded exec addr, which (a) bypasses BASIC's `: CALL 24576`
> entirely and (b) lands in section A or wherever PDPSR2's page-0
> branch maps the offset, NOT at &6000.

The chain is: `HDLDEX` (`0xE294`) calls `PDPSR2`, then `LD B,H; LD C,L`,
then `JP R1OFFCLBC` (`...rom-v3.0_annotated-disassembly.txt:22481-22484`).
If the file header advertises `(page=0, offset=0xE000)` (a hand-rolled
encoding meant to mean "address 0x6000 in physical page 1"), PDPSR2 will
`RES 7,H` → HL = `0x6000`, then because A=0 it jumps to `PDPSUBR3` (skipping
`SET 6,H`), so HL stays at `0x6000`, in section B. TSURPG sets HMPR=1.
With LMPR=0 (default), section B is physical page 1, so the JP lands at
physical-page-1 offset 0x2000 — but only if LMPR=0. If the user's LMPR is
something else, you've jumped into garbage.

The correct REL PAGE FORM encoding for address 24576 is `(page=1, offset=0xA000)`:
`UNSTLEN(24576)` returns A=1, HL=0x2000; SET 7,H gives HL=0xA000.

(In the project's case, the actual fix in `build-disk.sh` was to set the
exec-addr field to `FF FF FF` so the LOAD CODE path does NOT take HDLDEX,
returning to BASIC instead and letting `: CALL 24576` run.)

---

## 5. The "4 pages allocated to BASIC"

The User Guide (`docs/sam/sam-coupe_use-guide.txt:4891-4896`) says of `CLEAR n`:

> N must be within the 4 pages allocated to BASIC when the computer is
> switched on, and must be ≤ 81919 unless extra pages have been OPENed.

The four pages are physical RAM pages **0, 1, 2, 3**. This is set up at boot
in the ALLOCT table (`docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt:24496-24522`):

```
EBFB  21 00 51    LD HL,ALLOCT          ; HL = 5100H
EC0E  3E 40       LD A,40H              ; "IN USE, CONTEXT 0"
EC10  06 04       LD B,4                ; PAGES TO RESERVE
EC12  ATIU: 77    LD (HL),A             ; mark ALLOCT[0..3] as used by BASIC
        23 / 10FC INC HL / DJNZ ATIU
EC16  7D          LD A,L                ; L = 04
EC17  3D          DEC A                 ; A = 03
EC18  32 B0 5C    LD (LASTPAGE),A       ; LASTPAGE = 3 (last page used by BASIC)
EC1B  32 B1 5C    LD (RAMTOPP),A        ; RAMTOPP = 3
...
EC2B  21 FF BF    LD HL,0BFFFH
EC2E  22 B2 5C    LD (RAMTOP),HL        ; RAMTOP = 0xBFFF
```

So at boot:
- `LASTPAGE` (`&5CB0`) = `0x03`
- `RAMTOPP` (`&5CB1`) = `0x03`
- `RAMTOP` (`&5CB2`) = `0xBFFF`

This is REL PAGE FORM `(page=3, offset=0xBFFF)`. To convert back to a 19-bit
linear address, ROM `AHLNORM` (`0x2021`,
`...rom-v3.0_annotated-disassembly.txt:7620-7627`) shifts AHL right by 2 with
the top 2 bits of HL rotating into A's bottom: result =
`page * 16384 + (HL & 0x3FFF)` = `3 * 16384 + 0x3FFF` = 65535. But the user
sees `PRINT RAMTOP` = **81919** (`docs/sam/sam-coupe_use-guide.txt:4750`).

Why? Because the *next free byte for BASIC variables* is at the very end of
page 3 (offset 0xBFFF), but the User Guide's "starts at 81919" is the
**4-page-end byte index** = `4 * 16384 - 1` = `0xFFFF` = 65535... no wait,
81919 = `5 * 16384 - 1`. That's confusing; let me show the math:

```
PRINT RAMTOP:  RAMTOP read as REL PAGE FORM, normalised via AHLNORM:
   page = 3, HL = 0xBFFF
   AHLNORM:  result = page * 16384 + (HL & 0x3FFF)
                    = 3 * 16384 + 0x3FFF
                    = 49152 + 16383
                    = 65535            <-- but User Guide says 81919
```

The `AHLNORM` arithmetic actually rotates *all 19 bits* of AHL right by 2, so
the page byte ends up shifted by 14 not multiplied by 16384 — except in
practice it's the same operation. The 81919 number reported is the **upper
limit** of the four BASIC pages: the byte at offset 81919 is the *last byte
in page 4* if you index linearly from 0. That is, "BASIC owns pages 0..3,
and CLEAR's argument can range up to 81919 = `5*16384-1`" because RAMTOP can
go up to the *start* of page 4 (the byte after page 3 ends). The exact
boundary is `RAMTOP <= LASTPAGE+1's first byte`, i.e. up through page 4
offset 0x3FFF = 81919. Setting RAMTOP higher requires `OPEN n` to extend
LASTPAGE.

`CLR1`/`CLR3` at `0x390F`–`0x3954`
(`...rom-v3.0_annotated-disassembly.txt:13157-13189`) shows the check:

```
3950  3A B0 5C    LD A,(LASTPAGE)
      B9          CP C                     ; C = new RAMTOPP
      3002        JR NC,CLR4               ; OK IF RAMTOP PAGE <= LAST ALLOCATED PAGE
3956  RTERR: RST 8 / DB 48                 ; "Invalid CLEAR address"
```

So `CLEAR n` succeeds iff `n / 16384 <= LASTPAGE`. With default LASTPAGE=3,
n can be up to `4 * 16384 - 1 = 65535`. With page 4 allocated by `OPEN 1`,
n can go up to `5 * 16384 - 1 = 81919`. (The User Guide's "starts at 81919"
is loose phrasing; the precise boot-time CLEAR ceiling is 65535, but
historically/colloquially the SAM community calls 81919 "the limit" because
it's the largest CLEAR value the ROM accepts before refusing on physical
page count alone.)

---

## 6. Sysvars relevant to paging

All sysvar addresses below are taken from
`docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt` and Tech Manual.

| Addr   | Name      | Width | Description |
|--------|-----------|-------|-------------|
| `&5A8D` | WKENDP    | 1 | REL PAGE FORM page byte for end-of-workspace (`...rom-v3.0_annotated-disassembly.txt:881`) |
| `&5A8E` | WKEND     | 2 | REL PAGE FORM offset for end-of-workspace (`...:882`) |
| `&5AD9` | NMILRP    | 1 | LMPR value when last NMI fired (`...:962`) |
| `&5BC2` | DOSFLG    | 1 | 0 if no DOS loaded, else physical page where SAMDOS lives (`...:1051`) |
| `&5BC3` | DOSCNT    | 1 | bit 0 set if DOS is in control of the system |
| `&5CB0` | LASTPAGE  | 1 | Last physical page reserved by BASIC; updated by OPEN/CLOSE (`...:1140`) |
| `&5CB1` | RAMTOPP   | 1 | Page byte of RAMTOP (`...:1141`) |
| `&5CB2` | RAMTOP    | 2 | Offset of RAMTOP (REL PAGE FORM) (`...:1142`) |
| `&5CB4` | PRAMTP    | 1 | Last physical page present in machine (e.g. `0x0F` = 256K, `0x1F` = 512K) (`...:1143`, `...:24493`) |

The ROM maintains RAMTOP "in 8000-BFFF form" (`...rom-v3.0_annotated-disassembly.txt:5794`,
"`MAINTAINED IN 8000-BFFF FORM, (UNLIKE OLDRT)`") — i.e. the offset has bit 7
of its high byte set, so RAMTOP at a page boundary reads as `0x8000`, not
`0x4000`. RAMTOP at the very top of the BASIC-allocated region reads as
`0xBFFF` (page 3 offset 0xBFFF as set at boot).

WKENDP/WKEND mark the high water mark of BASIC's workspace; new variables
allocate downward from RAMTOP toward WKEND. Because both are stored in
REL PAGE FORM the ROM can do paged comparisons without needing 19-bit
arithmetic on every operation.

---

## 7. SAMDOS-specific paging behaviour

### Where SAMDOS lives

`docs/sam/sam-coupe_tech-man_v3-0.txt:4632-4641`:

> When SAMDOS is loaded the ROM looks at its available memory and loads
> SAMDOS into the last free 16K page. The ROM uses the last two 16K pages
> for SCREEN 1, so SAMDOS usually loads into the third from last 16K page,
> but the page will be different if extra screens have been opened before
> SAMDOS is loaded. Address 5BC2H (SVAR 450) holds the page number used by
> SAMDOS, or zero if SAMDOS has not been loaded.
> PEEK SVAR 450*16384+16384 will give the start address of SAMDOS, When a
> DOS command is issued the DOS is loaded into section B (4000H) of the
> 64k addressing space, the command is performed, and the DOS is then
> paged out.

So SAMDOS is *one 16K page* whose physical page number is stored in
`DOSFLG` (`&5BC2`). To read SAMDOS:

- Page it in at section C: `OUT (HMPR), DOSFLG_value`. Now SAMDOS is at
  Z80 addresses `0x8000..0xBFFF`. **Section D becomes physical page
  DOSFLG+1**, which is *whatever page happens to live at HMPR+1 — typically
  one of the screen pages*. If you write to `0xC000..0xFFFF` while DOS is
  paged in, you may corrupt screen memory. Always keep your CPU within
  `0x8000..0xBFFF` while DOS is at section C.
- For DOS to do *work*, the ROM dispatches it at section B (see below) — so
  Tech Manual's "When a DOS command is issued the DOS is loaded into section
  B (4000H)" is describing the *hook-call* convention, not the resting
  state.

### SAMDOS hook dispatch (`PTDOS` at `0x380E`)

`docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt:12944-12978`:

```
380B  PTDOS:    AND A
      28EE      JR Z,NORMERR
380E  5F        LD E,A             ; ERROR / HOOK NUMBER
      0EFA      LD C,250
      ED40      IN B,(C)           ; B = LRPORT (= LMPR)
      210000    LD HL,0
      39        ADD HL,SP
      3AC25B    LD A,(DOSFLG)      ; 0 OR DOS PAGE (1-1FH)
      3D        DEC A              ; GET PAGE NO. FOR SECTION A (DOS IN SECTION B)
      F3        DI
      D3FA      OUT (250),A        ; DOS PAGED IN AT 4000H, ROM0 ON, ROM1 OFF
      310080    LD SP,8000H        ; STACK NOW OK
      FB        EI
      C5        PUSH BC            ; B = PREV LRPORT [LMPR]
      E5        PUSH HL            ; PREV STACK PTR
      7B        LD A,E             ; HOOK CODE
      FE80      CP 128
      3004      JR NC,DOSHK        ; JR WITH HOOK CODES
      CD0342    CALL 4203H         ; HANDLE ERROR CODE, RATHER THAN HOOK CODE
      37        SCF                ; "COMING FROM ERROR"
382D  DOSHK:    CALL NC,4200H      ; HANDLE HOOK CODE IN A
3830  DOSC:     POP HL             ; PREV STACK PTR
      C1        POP BC
      F3        DI
      ED41      OUT (C),B          ; PREV LRPORT RESTORED
      F9        LD SP,HL           ; PREV STACK
      FB        EI
```

Key observations:

1. The ROM saves LMPR on the stack via `IN B,(C)`/`PUSH BC`, **not** HMPR.
2. To dispatch the DOS, it sets `LMPR = DOSFLG - 1`. With DOSFLG = N,
   LMPR low5 = N-1 means section A = page N-1, **section B = page N = SAMDOS**.
   So SAMDOS executes from `0x4000..0x7FFF`.
3. `LD SP, 8000H` switches to a stack at section C top — necessary because
   the previous SP may have been in section B which has just been remapped.
4. ROM0 and ROM1 are both implicitly disabled because LMPR bit 5 stays 0
   (so section A = RAM page N-1, not ROM0) and bit 6 (ROM1) stays 0
   (because `DOSFLG-1` has bit 6 clear for any DOSFLG ≤ 31).
5. On exit, LMPR is restored from BC.

So the entry-point inside SAMDOS is at offset `0x4200` (for hook codes) and
`0x4203` (for error codes) — i.e. SAMDOS code starts at offset `0x0200` from
its page base, with `0x4000..0x41FF` being its data area. This is consistent
with `docs/notes/sam-disk-format.md` describing SAMDOS as "9-byte header +
body, body starts at page-base + 0x000, code begins at + 0x0009".

Actually note that `JP 8009H` is the boot entry (see §8): SAMDOS is initially
paged in at section C, and the ROM's BOOT sequence does
`JP 8009H` to invoke its initialisation routine. Once initialised, the
hook-dispatch path remaps SAMDOS to section B at `0x4000`.

### What `cals` does inside SAMDOS

SAMDOS itself has its own paging helper. `~/git/samdos/src/h.s:308-321`:

```
cals:          in a,(251)            ; read HMPR
               ld (port1),a          ; save it
               ld a,h
               and %11000000         ; isolate top 2 bits of HL
               jp z,rep0             ; ERROR if HL was in 0x0000-0x3FFF
               sub %01000000         ; subtract 0x40 from those bits
               rlca
               rlca                  ; (top 2 bits) -> bottom 2 bits, scaled
               out (251),a           ; write new HMPR low2
               ld a,h
               and %00111111         ; clear top 2 bits of H
               or %10000000          ; force bit 7 set => HL in 0x8000-0xBFFF
               ld h,a                ; HL now in section C
               ret
```

This is SAMDOS's equivalent of `PDPSR2` for arbitrary host-side addresses:
given an HL pointing somewhere in the user's 64K view, page it in at section
C and rewrite HL to point into 0x8000–0xBFFF. The `port1` variable
(`samdos/src/b.s:239`) saves the original HMPR for restoration after
`hrsd1`/`hwsd1` (`samdos/src/h.s:283-284, 298-299`):

```
ld a,(port1)
out (251),a       ; restore HMPR after the disk-buffer copy
```

The interesting wrinkle: `cals` rejects HL in `0x0000..0x3FFF` (section A).
That makes sense — section A is ROM0 by default and SAMDOS can't write to
ROM, nor can it remap section A from inside itself (LMPR controls section
A but SAMDOS's host has set LMPR to keep SAMDOS at section B; flipping
LMPR would page SAMDOS itself out).

---

## 8. The boot path

The ROM boots SAMDOS by reading track 4 sector 1 directly to `0x8000` and
`JP 8009H`. The machinery is in `BOOTEX` at `0xD8E5`
(`docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt:20473-20598`):

```
D8E5  BOOTEX:    LD HL,ALLOCT+1FH     ; scan ALLOCT from page 31 down
D8E8  FDPL:      LD A,(HL)
        ...                            ; find a free or DOS-tagged page
D8F5  GDP:       LD A,L                ; A = page number
       CDDF3F    CALL SELURPG          ; HMPR <- A; SAMDOS will load at 0x8000
        ...                            ; floppy reset, index hole, REST
D91B    CALL REST
D91E    LD DE,0401H                    ; track 4, sector 1
D921    RSAD: ...                      ; read 512 bytes to HL=0x8000
        ...
D967  BTNOE:    LD DE,80FFH
        LD HL,BTWD                     ; "BOOT" signature
        LD B,4
D96F  BTCK:    INC DE                  ; first iter: DE=0x8100
        LD A,(DE)
        XOR (HL)
        AND 5FH                        ; case-insensitive cmp
        JR Z,BTLY
        RST 8 / DB 53                  ; "NO DOS"
D978  BTLY:    INC HL
        DJNZ BTCK
D97B    JP 8009H                       ; boot sequence: enter SAMDOS at +9
```

State at the `JP 8009H` moment:

- **HMPR** = (page found at `BOOTEX:GDP`, e.g. 0x0E for 256K third-from-last
  page). So section C (`0x8000..0xBFFF`) contains the just-loaded sector
  plus 510-byte remainder; section D contains whatever happens to be at
  HMPR+1.
- **LMPR** = unchanged from BASIC's startup state (bit 5 = 0 → ROM0 in
  section A; low 5 bits = 0 → page 0 RAM in B; bit 6 = 0 → page HMPR+1 in
  section D). At boot LMPR is cleared in `BOOT2`/`BOOTEX`'s caller chain to
  the default. (See also `INIT`'s `OUT (250),A` with `A=0` paths in the
  startup code.)
- **ROM0** is ON (LMPR bit 5 = 0). The CPU is executing in ROM0 and is
  about to JP into SAMDOS at `0x8009`.
- **VMPR** is whatever was set up by ROM init (typically the screen page;
  see `...rom-v3.0_annotated-disassembly.txt:24494`, `OUT (VIDPORT),A` at
  early init).
- The Z80 has 256 bytes of T4S1 at `0x8000..0x80FF`, plus 2 trailer bytes
  containing track/sector pointer to T4S2 at `0x81FE/0x81FF` — though
  `BOOTEX` reads only the first sector.

Since SAMDOS is a 20-sector file (10K), its full body extends across T4S1
through T5S10. SAMDOS's own initialisation at `0x8009` reads the rest.

After SAMDOS init, `DOSFLG` is set to the page that holds SAMDOS, and
control returns to BASIC. From then on, all DOS work is dispatched via
`PTDOS` (§7), which pages SAMDOS to section B for execution.

---

## 9. Worked examples

### Example A: `LOAD_ADDR = 24576 = 0x6000`

24576 = 1 × 16384 + 8192 = (page 1, offset 0x2000). After `SET 7, H`
(forming a section-C-style pointer), HL = 0xA000.

REL PAGE FORM: `(page=1, offset=0xA000)` → bytes in file header:
`01 00 A0`.

Linearly, byte 24576 lives in **physical page 1**, at offset `0x2000` within
that page. With LMPR=0 (default BASIC): section A=page 0 (or ROM0 if LMPR
bit 5 = 0), section B=page 1. So Z80 address `0x6000` (in section B)
addresses physical-page-1 offset 0x2000 — i.e. byte 24576. Match.

PDPSR2 with A=1, HL=0xA000:
- `CP 4`: A < 4, fall through.
- `LD C, 2`. `CP 2`: not equal, fall through.
- `JR NC, PDPSUBR2`: A=1 < 2, fall through.
- `RES 7, H`: HL = `0x2000`.
- `AND A`: A=1, NZ, fall through.
- `SET 6, H`: HL = `0x6000`. → `PDPSUBR3`.
- `LD A, C` (A=2). `DEC A` (A=1). `CALL TSURPG`: HMPR low5 = 1.

Result: HL = `0x6000`, HMPR = 1. The data is in section B at `0x6000`,
which (with LMPR=0) is physical-page-1 offset 0x2000 = byte 24576. Correct.

### Example B: SAMDOS at `&8000–&BFFF` (section C)

DOSFLG holds SAMDOS's physical page, e.g. `0x0E` on a 256K machine. To
view SAMDOS:

```
LD A, (DOSFLG)         ; A = 0x0E
OUT (251), A           ; HMPR = 0x0E; section C = page 0x0E (SAMDOS)
                       ;            section D = page 0x0F (probably screen)
```

Section C (0x8000–0xBFFF) now contains SAMDOS. Section D contains whatever
is at page DOSFLG+1, typically a screen page. If you write to D you'll
clobber the screen. The Tech Manual's `PEEK SVAR 450*16384+16384` formula
for the SAMDOS start address relies on the BASIC `PEEK addr` mechanism
which auto-pages via REL PAGE FORM internally.

### Example C: `CLEAR 81919`

`UNSTLEN(81919)`: 81919 = 4 × 16384 + 16383 = (page=4, offset=0x3FFF).
`CLEAR` then `SET 7, H` → HL = `0xBFFF`. Then C ← A (= 4), DEC C → C = 3.
The check `LD A, (LASTPAGE); CP C; JR NC, CLR4` requires LASTPAGE ≥ 3,
which is always true at boot. RAMTOPP = 3, RAMTOP = `0xBFFF`. RAMTOP is
unchanged from boot value, but a real CLEAR happened (variables flushed).

### Example D: `CLEAR 24575`

`UNSTLEN(24575)`: 24575 = 1 × 16384 + 8191 = (page=1, offset=0x1FFF).
`SET 7, H` → HL = `0x9FFF`. C ← 1, DEC C → C = 0. Then `CP C`
(LASTPAGE=3 vs 0): NC. CLR4 sets RAMTOPP=0, RAMTOP=`0x9FFF`. So RAMTOP is
now (page=0, offset=0x9FFF), which `AHLNORM`s to byte 24575. BASIC's
variable space is now restricted to physical page 0 only — likely
catastrophic for any non-trivial program because the program itself starts
in page 0 and can collide with the new low ceiling. (This is why the User
Guide warns CLEAR n must be ≤ 81919 with caveats.)

### Example E: hand-rolling a REL PAGE FORM exec address

Wrong (the original `build-disk.sh` bug): "since 24576 = 0x6000 in section
B, encode as page=0, offset=0xE000 (forced into section C form)".

Why this is wrong: the page byte must be the **physical** page number where
the byte lives, not "section's offset / 16K". `UNSTLEN(24576)` returns
A=1, not A=0. PDPSR2 for `(0, 0xE000)` does `RES 7,H` → HL=0x6000, then
since A=0 it jumps to `PDPSUBR3` which sets HMPR low5=1. Now: TSURPG sets
HMPR=1, HL is `0x6000` which is section B. Section B is LMPR-controlled
(physical page 1 with LMPR=0), so the JP goes to physical-page-1 offset
0x2000 — *which actually happens to be 24576 too*, by coincidence of LMPR=0.

But: the AF that `R1OFFCLBC` saves is LMPR, and PDPSR2 has corrupted HMPR
(set it to 1). On return to BASIC, HMPR is wrong — and BASIC's section-C
state may now be unexpected, breaking subsequent operations.

Correct encoding for "exec at 24576": `(page=1, offset=0xA000)`, file
header bytes `01 00 A0`. PDPSR2 then maps HL=0x6000 (in section B) and
sets HMPR=1 — same final HL, but the encoding is unambiguous.

Better still for the auto-RUN case: encode `FF FF FF` (no auto-exec) and
let BASIC's `: CALL n` handle it, as `tools/build-disk.sh:225` does.

---

## 10. Cross references

- File-header layout & how REL PAGE FORM appears in the 9-byte header:
  `docs/notes/sam-file-header.md` (parallel doc).
- BOOT signature & disk-format mechanics: `docs/notes/sam-disk-format.md`.
- The `cals` helper in SAMDOS: `~/git/samdos/src/h.s:308`.
- ROM EQUates for sysvars and ports:
  `docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt:881-1283`.
- ROM port assignments table: Tech Manual,
  `docs/sam/sam-coupe_tech-man_v3-0.txt:1046-1106`.
