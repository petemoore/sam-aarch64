# SAM Coupé BASIC SAVE format

The on-disk layout of a SAM BASIC file (file type `0x10`) and the
in-memory invariants the ROM expects after a clean `LOAD`.

This document supersedes the experimental theories in
`clear-investigation.md`, `clear-mechanism.md`,
`clear-actual-mechanism.md`, and `clear-remaining-diff.md` — the actual
M0-blocker turned out to be missing trailer bytes documented here.

## TL;DR

A SAM BASIC SAVE always emits **PROG section** + a **trailer** sized so that

```
SAVARS - NVARS == 604     (canonical, 94% of disks)
SAVARS - NVARS == 2156    (MasterDOS variant, ~5% of disks)
```

The split between vars-area (`NUMEND-NVARS`) and gap (`SAVARS-NUMEND`)
varies — the dominant canonical split is **92 + 512**, but only ~50% of
disks use exactly that. Both vars and gap can be larger as the user
defines variables and the runtime expands the gap.

The build-disk.sh emitter writes the canonical 92+512 split.

## Sections of a BASIC file body

```
PROG                                   ← file body byte 9 (after 9-byte body header)
  line: lineNumBE(2) + lineLenLE(2) + tokenised body + 0x0d
  ...
  0xff                                  ← end-of-program sentinel (1 byte)
NVARS                                  ← = PROG + program length
  numeric variables area               ← (NUMEND - NVARS) bytes
NUMEND
  gap                                  ← (SAVARS - NUMEND) bytes
SAVARS
  string/array variables               ← (typically empty for fresh saves)
EOF
```

The boundaries `NVARS`, `NUMEND`, `SAVARS` are recorded in the directory
entry as **3-byte page-form triplets** at offsets `0xDD`, `0xE0`, `0xE3`
respectively, each storing `(target - PROG)` in 8000H form.

ROM SAVE writes these triplets from sysvars at addresses
`5A88H/5A85H/5A82H` (NVARS/NUMEND/SAVARS) per
`docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt:870-876`.

## Why the trailer must exist

`LOAD` of a BASIC file copies the saved bytes into RAM and sets
in-memory NVARS/NUMEND/SAVARS sysvars from the dir-entry triplets.
`CLEAR` then walks PROG → NVARS → NUMEND → SAVARS, calling MCLS and
clearing variable storage. If the dir-entry triplets all point to PROG +
`program_length` (i.e. zero-sized vars and gap), CLEAR walks 0 bytes
through what the runtime believes is a 604-byte buffer and the
pointer arithmetic touches whatever lives at PROG + small offsets — in
our M0 disk that meant walking into the next file's body, hence the
"page-displaced screen → cold-boot splash" we chased for weeks.

Empirically verified: in 161 disks under `~/Downloads/`, **632 / 673
(93.9%)** of well-formed BASIC files have `(NUMEND-PROG) +
(SAVARS-NUMEND) == 604`. A second canonical variant (MasterDOS or
alternate ROM SAVE) has the sum at `2156` — 32 files. The pair
`(vars=92, gap=512)` exactly matches in 338 / 673 (50.2%) of files.

## What the 92 bytes hold

ROM `CLRSR` (`rom-disasm:13209-13230`, addr `396B-3996`) initialises
the post-PROG vars area at NEW / cold-boot:

```
397C CD1F1F     CALL ADDRNV         ; HL := address of NVARS area
397F 062E       LD B,46             ; loop count — 23 letter pointers × 2
3981 36FF       LD (HL),0FFH        ;     each initialised to 0xFFFF
       INC HL
       DJNZ CLNVP
3987 21E339     LD HL,PSVTAB
398A 0E1A       LD C,26             ; copy 26 bytes from PSVTAB
398C EDB0       LDIR
398E 21E939     LD HL,PSVT2
3991 0E14       LD C,20             ; then 20 more bytes from PSVT2
3993 EDB0       LDIR
3996 CDB639     CALL SETNE          ; NUMEND := NVARS + 92
```

Total = `46 + 26 + 20 = 92` bytes. The structure:

| Range (offset within vars area) | Source     | Content                                                                       |
|---------------------------------|------------|-------------------------------------------------------------------------------|
| 0..45 (46 bytes)                | filled here | letter-pointer table for `A`..`W` (23 letters × 2 bytes), all `0xFF`         |
| 46..51 (6 bytes)                | PSVTAB     | X-vars ptr `0x0019`, Y-vars ptr `0x0003`, Z-vars ptr `0xFFFF`                |
| 52..71 (20 bytes)               | PSVT2 (1st)| `os` var entry (= YOS, value 0) + `rg` var entry (= YRG, value 192)          |
| 72..91 (20 bytes)               | PSVT2 (2nd)| same `os` and `rg` entries again — ROM intentionally duplicates             |

PSVTAB and PSVT2 definitions are at `rom-disasm:13283-13297` (addr
`39E3-39FD`).

`os` and `rg` are SAM BASIC's persistent system numeric variables for
the Y-origin offset and Y-range — they exist in every BASIC instance
and survive NEW.

## What the 512-byte gap is

Allocated dynamically by the FIRST numeric-variable creation that runs
out of free space in the vars area. ROM `2B86-2B97` (`rom-disasm:10240-10255`):

```
2B86 7B         LD A,E
2B87 FE3C       CP 60               ; <60 free bytes left after the new var?
2B89 300F       JR NC,ANOK          ; if ≥60, no expansion needed
2B8B CD271F     CALL ADDRSAV        ; HL := SAVARS pointer
2B8E CDB91F     CALL DECPTR         ; back up
2B91 010002     LD BC,0200H         ; ← 512 bytes
2B94 CD1B1E     CALL MAKEROOM       ; insert 512 bytes before SAVARS
```

A fresh `NEW` state has NUMEND == SAVARS (no gap). The first call to
the variable-creation path triggers this 512-byte expansion. Subsequent
SAVEs include the gap.

Variants with `gap=2064` (giving `vars+gap=2156`) come from MasterDOS
or alternate ROMs whose MAKEROOM-equivalent uses a larger constant.

## Honest gap: why is `gap=512` near-universal even for empty programs?

The ROM mechanism above predicts: if no numeric variable has ever been
created, MAKEROOM never fires, so `gap=0`. Yet 94% of empirical disks
show `gap > 0`, with `gap=512` dominant. Something between `NEW` and
`SAVE` triggers exactly one MAKEROOM call in real-world BASIC use.

We did not trace this fully. Plausible candidates:

- Editing a line with numeric literals may transiently create
  parser-internal numeric state.
- SAVE itself may pre-allocate before serialising, to keep the on-disk
  layout consistent.
- Auto-RUN line-number storage (dir bytes 0xF3-0xF4) may invoke the
  numeric var path at SAVE time.

For our purposes the gap exists empirically — that's enough to know
how to emit a working file. The mechanism is left as an open
follow-up.

## Practical recipe for synthesising a SAM BASIC AUTO file

This recipe targets the common case the M0 boot path needs: a small
AUTO BASIC with no user-defined variables. For programs that actually
declare variables, the vars-area size grows accordingly; that's beyond
M0 scope.

1. **PROG section**: tokenised lines + `0xff` end-of-program sentinel.
2. **Trailer**: 92 bytes vars area + 512 bytes gap = 604 bytes. The
   bytes can be zero-filled; CLEAR re-initialises the vars area on
   AUTO-RUN. For byte-perfect canonical fidelity, write the
   CLRSR-init pattern (46 × `0xFF` + PSVTAB + 2 × PSVT2 = 92 bytes)
   followed by 512 zeros.
3. **Dir-entry triplets** at `0xDD/0xE0/0xE3`: page-form encoding of
   `(prog_length, prog_length+92, prog_length+604)` respectively.
4. **Body header LengthMod16K**: total body size including the trailer
   (= prog_length + 604).
5. **Sector chain**: enough sectors. For our AUTO: 9 header + 52 prog +
   92 vars + 512 gap = 665 bytes → 2 sectors at 510 usable bytes each.

### Why 92+512 specifically (and not, say, all-in-vars or all-in-gap)?

There is a `vars + gap = 604` invariant that's robust (94% of disks).
Within that, the split varies based on how many user variables are
defined at SAVE time. For `vars_count == 0` the dominant split is
`92 + 512` — that's what Pete's hand-saved reference has and what
Defender's AUTOBOOT has, both of which boot cleanly. A different split
that still sums to 604 would probably also work, but 92+512 is the
empirically-safe choice with the most reference data behind it.

We are NOT fully replicating ROM's dynamic MAKEROOM algorithm. We are
emitting a fixed canonical layout that matches observed clean SAVES.
For a future asssembler with real variables, the vars side of the
split would grow as variables are defined; the gap side would track
"available headroom" per the `<60 bytes free → +512` rule. That's
beyond M0 but documented here for when it matters.

`tools/build-disk.sh` implements this recipe for slot 1 (`auto`).

## Sysvar addresses (for cross-reference)

From `rom-disasm:870-876`:

| Sysvar  | Address |
|---------|---------|
| SAVARSP | `5A81H` |
| SAVARS  | `5A82H` |
| NUMENDP | `5A84H` |
| NUMEND  | `5A85H` |
| NVARSP  | `5A87H` |
| NVARS   | `5A88H` |
| PROG    | `5CD5H` (start of BASIC programs in section C, fixed) |

The "P" suffix is the page-byte that complements each address sysvar.

## References

- ROM v3.0 disassembly: `docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt`
- Tech Manual v3.0: `docs/sam/sam-coupe_tech-man_v3-0.txt`
- Empirical scan: 161 disks under `~/Downloads/`, scan script kept at
  `/tmp/mgt-validation/scan.py` during the verification session.
