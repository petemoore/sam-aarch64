# SAMDOS2 boot vs auto-RUN — what the docs say vs what samdos2 actually does

Date: 2026-05-10
Purpose: Settle, with citations from the docs corpus, *exactly* what
samdos2 does on the BTHK / ALHK auto-load follow-up after SAM ROM's
BOOT command, and why our handcrafted disk doesn't auto-RUN our AUTO
BASIC file even though samdos2 is now bundled at T4S1.

## TL;DR

**samdos2 alone does not auto-RUN an AUTO file.** Despite the SAM Tech
Manual's claim that hook 128 (INIT) "looks for an AUTO file on the
current disk and initialises the DVARS", samdos2's actual `init`
implementation only sets `CURCMD = 0x95` (the LOAD token) in BASIC's
"current command" sysvar at `&5b74` — and `RET`s. There is no AUTO
lookup, no file load, no execution. The `hauto` routine that *would*
load an AUTO file exists in the samdos source (`h.s:224`) but is
**never called** from anywhere.

Real bootable SAM disks (e.g. FRED 56) achieve auto-RUN by including
a custom T4S1 bootstrap file (FRED 56's is 8078 bytes), not by
relying on SAMDOS's built-in auto-load.

## What the SAM Tech Manual says (canonical aspirational spec)

`docs/sam/sam-coupe_tech-man_v3-0.txt` line 4524:

```
INIT     128   dec         Initialise and look for AUTO file
```

`docs/sam/sam-coupe_tech-man_v3-0.txt` line 4548 (HOOK CODE EXPLANATIONS):

```
INIT    This routine looks for an AUTO file on the current disk, and
        initialises the DVARS.
```

So per the canonical spec, hook 128 is supposed to look for an AUTO
file. (Note: the Tech Manual does not explicitly say it RUNs the file
— "looks for" leaves room for ambiguity.)

## What ROM BOOT actually does (the call site)

`docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt` D8CD–D8E4:

```
D8CD CD503A     BOOT:      CALL SYNTAX3
D8D0 CD331D                CALL GETBYTE
D8D3 A7                    AND A
D8D4 200F                  JR NZ,BOOTEX        ; BOOT 1 etc.
D8D6 3AC25B                LD A,(DOSFLG)
D8D9 A7                    AND A
D8DA 2803                  JR Z,BOOTNR         ; DOS not resident
D8DC CF                    RST 08H
D8DD 88                    DB ALHK             ; 136 — DO AUTO-LOAD
D8DE C9                    RET
D8DF
D8DF CDE5D8     BOOTNR:    CALL BOOTEX         ; load DOS via FDC
D8E2 CF                    RST 08H
D8E3 80                    DB BTHK             ; 128 — DO AUTO-LOAD, BUT NO ERROR IF NONE
D8E4 C9                    RET
```

So BOOT calls hook BTHK (or ALHK if DOS already resident) and immediately
returns. The auto-RUN, if it happens, must happen entirely inside the
DOS hook — there is no second-stage logic in ROM.

## What samdos2's INIT/INITX actually does (the implementation)

`~/git/samdos/src/h.s:215-218`:

```asm
init:          nop                ; entry from hook 128 (BTHK)
initx:         ld a,&95           ; LOAD token
               call nrwr
               defw &5b74         ; CURCMD (current BASIC command)
               ret
```

That's the entire body of both BTHK and ALHK handlers. Set CURCMD to
LOAD and return. Nothing else.

`hauto` is defined further down (line 224) and contains the actual
"find and load AUTO file" logic — but **a full grep of all of samdos
source (a.s, b.s, c.s, d.s, e.s, f.s, h.s, samdos.s) shows hauto and
autnam are never called or referenced anywhere except their own
definitions**. Dead code.

## Provenance: are we sure samdos2.reference.bin matches the source?

Yes. From `~/git/samdos/README.md`:

> The starting point was the source code SamDos2InCometFormatMasterv1.2.zip
> which contains five versions (comp1.s through comp5.s). [...]
> comp5.s is not the final "samdos2" as was publicly distributed.
>
> I disassembled the samdos2 binary using dZ80 and merged it into the
> last source release.

And `~/git/samdos/build.xml` enforces `obj/samdos2 == res/samdos2.reference.bin`
byte-for-byte via the `compare` target. So the source we're reading
*is* what the binary does.

## Why CURCMD=LOAD is not an auto-LOAD trigger

CURCMD is read by SAVE/LOAD/VERIFY shared code paths to know which
variant they're handling, not by the command dispatcher to decide what
to run next. From the ROM disasm `0DA2-0DD3`, CURCMD is set by the
dispatcher to the command code *currently being executed*, BEFORE
jumping to the handler. SAMDOS's init merely overwrites that value
mid-handler. After the handler returns, BASIC takes the next typed/program
statement via NEXTSTAT (`0DD4`) — there is no path that reads CURCMD
to decide to run a LOAD.

So setting CURCMD=LOAD inside the BTHK hook is essentially a state
flag for *something that never happens* in samdos2-only setups. It's
plausible this was originally meant to enable a follow-up LOAD by
some caller, but no such caller exists in the shipped samdos2.

## How FRED 56 does it (the working reference)

From `docs/notes/fred-disk-inspection.md`: FRED 56's T4S1 file is a
Code-typed boot blob `\x7f FRED56 \x7f`, 8078 bytes. The blob contains
the literal string `SAMDOS2` at file offset 0x133 — i.e. it does its
own SAMDOS lookup-by-name and load, then auto-RUNs `AUTOFRED..` (a
Type-16 SAM BASIC file with Start Line 10) via direct hook calls.

So the auto-RUN convention is **a per-disk custom bootstrap convention**,
not a SAMDOS feature.

## Implications for our build

Two viable paths to actually run our M0 stub:

1. **Custom T4S1 bootstrap (FRED-style).** Replace samdos2 at T4S1 with
   a small Z80 program that has the "BOOT" signature at offset 256, loads
   samdos2 (kept as a separate directory entry) into the appropriate page,
   and either (a) auto-RUNs our AUTO BASIC file via SAMDOS hooks, or (b)
   loads our stub directly via HGFLE/HLOAD and JPs to it. Bootable on
   real SAM hardware. Several hundred bytes of Z80.

2. **Bypass BASIC entirely.** Keep samdos2 at T4S1, but instead of
   relying on auto-RUN, write a small T4S1 bootstrap that *replaces*
   samdos2 at T4S1 and embeds the SAMDOS-loader inline (samdos2's own
   `dos:` routine in b.s is the template — ~30 lines of FDC code).
   After SAMDOS is loaded, the bootstrap directly LOADs our stub via
   the HGFLE hook and JPs to &6000. Equivalent functionally to (1) but
   self-contained.

Path 1 is the canonical convention; Path 2 is simpler if all we want
is to run a single Z80 program post-DOS-load.

## Sources cited

- `docs/sam/sam-coupe_tech-man_v3-0.txt` lines 4524, 4548 (hook 128 spec).
- `docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt` D8CD–D8E4 (BOOT command call site), 0DA2–0DD3 (command dispatcher and CURCMD usage).
- `~/git/samdos/src/h.s:215–224` (init/initx/hauto definitions; the latter is dead code).
- `~/git/samdos/src/b.s:497–540` (samhk hook table — both BTHK and ALHK route to init/initx).
- `~/git/samdos/README.md`, `~/git/samdos/build.xml` (provenance: source matches binary byte-for-byte).
- `docs/notes/fred-disk-inspection.md` (FRED 56 bootstrap convention).
