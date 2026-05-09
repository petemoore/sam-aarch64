# M0 stub audit — `src/stub.asm` and `src/sam_io.inc` against canonical SAMDOS

**Status**: investigation report. Citations are `file:line` against
`~/git/samdos/src/{a..h}.s`,
`docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt`,
`docs/sam/sam-coupe_tech-man_v3-0.txt`, and existing
`docs/notes/sam-{file-io,paging,file-header}.md`.

This audit was triggered by the symptom: the patched SimCoupé times out
at 30 s when running the stub end-to-end. The stub never reaches its
`OUT (&DEAD), &C0` exit. `docs/notes/sam-file-io.md` is the project's
own pre-Task-7 spike — this audit confirms the calling convention it
documents but identifies a hard real bug in canonical SAMDOS 2's
`HOFLE` / `SBYT` / `CFSM` paths that breaks the stub.

---

## TL;DR — concrete bug list, severity-ranked

| # | Severity | Where | Bug |
|---|---|---|---|
| 1 | **REAL BUG** | stub flow: `create_output` → `hofle` → `ofsm` | `ofsm` at `c.s:1231-1260` writes 256+ bytes through `(IX+ffsa..)` with `IX=caller's UIFA pointer`. With our `IX=&4B00` those writes land **inside SAMDOS code at bank-1 offset `&4B13..&4C12`**, corrupting the running SAMDOS image. The corruption either hangs SAMDOS or longjmps it to BASIC silently. This is the proximate cause of the 30 s timeout. |
| 2 | **REAL BUG** | stub flow: `write_byte` → `sbyt` | `sbyt` at `c.s:533-551` reads `(IX+bufl=15)`/`(IX+bufh=16)`/`(IX+rptl=13)`/`(IX+rpth=14)` to find the in-buffer write pointer. With `IX=&4B00` (or whatever `IX` was last left as by hofle/fill_uifa) those four bytes are not the buffer pointer SAMDOS needs. The `ld (hl), a` write at `c.s:547` then either writes garbage somewhere (best case) or hits ROM and silently no-ops (`&FFxx`). |
| 3 | **REAL BUG** | stub flow: `close_output` → `cfsm` | `cfsm` at `c.s:1306-1343` likewise relies on `IX = dchan`-shaped FCB. With `IX=&4B00` it (a) flushes garbage bytes to a "current sector" pointed at by garbage `(IX+bufl..)`, then (b) at `cfm3:1334-1338` copies 256 bytes from `(IX+ffsa..)` into the directory entry. Result: directory entry filled with whatever was at `&4B13..&4C12` in SAMDOS bank — i.e. SAMDOS code bytes — not a valid sector address map. |
| 4 | **NON-CANONICAL** | `sam_io.inc:62-71` `fill_uifa` | Pads UIFA bytes 15–47 with `&FF`. Tech Manual `docs/sam/sam-coupe_tech-man_v3-0.txt:4459-4496` and SAMDOS source `b.s:278-290` agree the layout is `type / name(10) / ext(4) / filler(16,&FF) / startpage / pageoffset(2) / pages / lenmod16k(2) / execpage / execofs(2) / spare(8,&00)`. For our type-19 (code) **streaming-write** use, fields 31–47 are not consulted by `hofle`/`sbyt` — but bytes 40–47 should be `&00` not `&FF` to match a vanilla SAMDOS-saved UIFA. Cosmetic for our use, important if a load hook later parses the saved file. |
| 5 | **QUESTIONABLE** | `stub.asm:43` open_input then `stub.asm:50-52` open_output without intermediate read | Per `docs/notes/sam-file-io.md:155-163` the comment says "abandoning the read is safe". The SAMDOS source confirms there is **no separate FCB for IN vs OUT** — `dchan` is single-channel (`a.s:133`). The act of `hofle` (after `hgfle`) overwrites `dchan`'s state. **However, this is moot** given bug #1: hofle never proceeds correctly anyway. If bug #1 is fixed, the IN-then-OUT-without-read pattern still works because it relies on `gtfle`'s post-state being entirely replaced by `ofsm`'s setup. |
| 6 | **OK** | `sam_io.inc:81-84` `open_input` | `hgfle` at `h.s:252-257` is the **only one of our four hooks that sets `IX=dchan` internally** (via `gtfle:1348` → `fdhr:984 ld ix, dchan`). So `hgfle` works correctly when called externally with `IX = caller's UIFA`. |

The fix for bugs #1–#3 is the same: **the streaming byte-stream API
(HOFLE/SBYT/CFSM) is broken in canonical SAMDOS 2 when called externally
via RST 8** because none of those routines call `gtixd` at entry to
re-point `IX` at `dchan`. The only way to use SAMDOS 2 streaming writes
is to either (a) switch to `HSAVE` (hook 132) which writes the whole
file in one go and goes through `gtixd` first, or (b) work around by
publishing a custom `(hksp)` error handler and accept that streaming is
unusable.

---

## Calling convention — what the canonical sources agree on

All citations below are confirmed against multiple sources.

### The dispatch path

SAM ROM `RST 8 / DEFB <code>` enters `&0008` (`docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt:219-223`).
For code ≥ 128 with DOSFLG ≠ 0, ROM `PTDOS` at `&380B` takes over
(`...rom-v3.0_annotated-disassembly.txt:12944-12978`):

1. Read prev `LMPR` into `B` (`12948-12949`).
2. `LD HL, 0; ADD HL, SP` — capture caller SP (`12950-12951`).
3. `LD A, (DOSFLG); DEC A; OUT (250), A` — page SAMDOS bank into
   section A (so section B = DOSFLG, ROM0 in section A; bit 5 of
   `DOSFLG-1` is 0) (`12952-12955`).
4. `LD SP, &8000` — switch to SAMDOS-internal stack (`12956`).
5. `PUSH BC; PUSH HL` — save prev LMPR + SP onto new stack (`12958-12959`).
6. For hook codes: `CALL &4200` (`12967`). `&4200` is SAMDOS's
   `jp hook` entry per `b.s:319-322`.

**Critical**: ROM does NOT touch HMPR (`...rom-v3.0_annotated-disassembly.txt:12944-12978`,
no `OUT (251)`). HMPR is preserved across the hook. So sections C/D
keep whatever the caller had. Section B is replaced with SAMDOS bank.

The SAMDOS dispatcher itself (`b.s:439-470`) saves caller's `IX` to
`(svhdr)` (`b.s:440`) then jumps via the `samhk` table to the hook
routine. **It does NOT reset `IX`.** So the hook routine inherits
caller's `IX`.

### Cross-bank reads and writes — `cmr; defw nrread/nrrite`

`cmr` is `&0103` (`a.s:28`), which is `JP JSVIN` per
`...rom-v3.0_annotated-disassembly.txt:441` and the JSVIN definition
at `...rom-v3.0_annotated-disassembly.txt:715-746`. JSVIN does:

1. Save current `LMPR` (port 250) to `B` (`723-724`).
2. `LD A, &1F; OUT (250), A` — comment: "SYS PAGE IN AT 4000H,
   ROM0 ON, ROM1 OFF" (`725-727`). Pages **BASIC's system page into
   section B**.
3. Switch SP (`728-730`).
4. Dispatch to the address following the `defw` (`731-737`).
5. `JSVIN2` (`739-746`) — restore SP and LMPR.

So `call cmr; defw nrread` reads `(HL)` while **the BASIC sys page is
paged into section B**. `nrread = ld a,(hl); ret` (`...rom-v3.0_annotated-disassembly.txt:370-371`).

The application's UIFA at `&4B00` therefore needs to be **in the BASIC
sys page** for SAMDOS's hook reads to see it. Per
`docs/notes/sam-paging.md:541-560` and `docs/notes/sam-file-header.md:28-32`,
&4B00 is also the SAM ROM's `HDR` 80-byte tape/disk-header request
buffer — i.e. it's a designated region of the BASIC sys page.

(The SAM ROM declares `HDR EQU 4B00H` at
`docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt:1236`, length
80 bytes, and uses `&4B00..&4B4F` as the load-request header buffer.)

### UIFA layout — Tech Manual vs SAMDOS source

Tech Manual `docs/sam/sam-coupe_tech-man_v3-0.txt:4459-4496` and
SAMDOS internal `uifa` slot (`b.s:278-290`) agree on the 48-byte layout:

| Offset | Bytes | Field            | Notes |
|--------|-------|------------------|-------|
| 0      | 1     | type             | 19 = code, 16 = SAM BASIC, etc. |
| 1-10   | 10    | name             | space-padded |
| 11-14  | 4     | ext              | space-padded; ignored on disk |
| 15     | 1     | flags            | "MGT only"; SAMDOS leaves `&FF` |
| 16-26  | 11    | type-info        | type-specific (code: unused; basic: lengths) |
| 27-30  | 4     | reserved         | `&FF` |
| 31     | 1     | start page       | for code: page mod 32 |
| 32-33  | 2     | page offset LE   | for code: target addr in 8000-BFFF form |
| 34     | 1     | num pages        | high byte of length |
| 35-36  | 2     | length mod 16K LE| low 14 bits of length |
| 37     | 1     | exec page        | optional |
| 38-39  | 2     | exec offset LE   | optional |
| 40-47  | 8     | comment / spare  | `&00` per default `b.s:289-290` |

Our `fill_uifa` (`sam_io.inc:62-71`) writes type+name(10)+ext(4) = 15
bytes from caller, then pads bytes 15–47 with `&FF`. Bytes 40–47
should be `&00` per the SAMDOS default (`b.s:290`), but neither
`hofle` nor `sbyt` nor `cfsm` reads bytes 37–47 of the UIFA — they
only consult bytes 0–36 via `hconr` (`h.s:336-361`):

- `(uifa+0)` → `nstr1` and `hd001` (`h.s:342-344`)
- `(uifa+31)` → `page1` (`h.s:346-347`)
- `(uifa+32-33)` → `hd0d1` (`h.s:349-350`)
- `(uifa+34)` → `pges1`, masked with `&1F` (`h.s:352-354`)
- `(uifa+35-36)` → `hd0b1` and back to `(uifa+35)` with bit 7 cleared (`h.s:356-359`)

Our UIFA leaves 31, 32-33, 34, 35-36 all as `&FF`. After `(uifa+34) AND &1F`
we get `pges1=&1F`. After `RES 7,(uifa+35-msb)` we get `hd0b1=0x7FFF`.
For type-19 streaming write these are written into the on-disk file
header at `c.s:721-cfsm` flush time, but they don't affect whether the
hook returns. They DO mean the resulting file's 9-byte header is wrong
(claims length 0x7FFF in last page, ~32K pages used), but that's a
data-integrity concern, not a control-flow blocker. **Severity: cosmetic
for M0**, since M0 only cares whether `OUT` is written; correctness of
length fields is checked downstream by file extract / diff. (See `docs/notes/sam-file-header.md`
for the format details.)

### Error handling — the longjmp gotcha

Most hook errors do not return. The dispatcher saves entry SP at
`(entsp)` (`b.s:439`). On error the hook eventually calls `derr`
(`d.s:430-460`):

```
derr:  call bcr
       ld hl, (hksp)        ; user-installed handler vector
       ld a, h
       or l
       jr z, derr1          ; default-handler path if (hksp)=0
       ld sp, hl            ; jump to user handler
       ret
derr1: ... ld sp, (entsp); ret  ; restore caller SP and pop into BASIC error path
```

The dispatcher zeros `(hksp)` on each entry (`b.s:450-451`), so by
default every error longjmps to BASIC's error handler. `(hksp)` is
declared at `b.s:160`. The application can install its own error
handler by setting `(hksp)` *between* hook calls (the dispatcher's
zero happens *before* the hook routine runs, but `(hksp)` set by the
hook routine itself, e.g. by writing to an absolute address, won't
help — the dispatcher already cleared it). For the M0 stub, no
custom handler → all errors → BASIC error → `EXIT=124 timeout`.

`(hksp)` is in SAMDOS bank, and `b.s:160` says it lives at offset 60
from gnd+&100. The application can't write to `&410A`-area from its
own address space without paging SAMDOS in. **Custom `(hksp)` is
infeasible for the M0 stub without a paging dance.**

---

## Per-hook analysis

### Hook 158 — `HGFLE` (open existing file for reading)

**Source**: `h.s:252-257`.

```
hgfle: call rxhed       ; copy 48 bytes (svhdr) → uifa via cmr/nrread
       call gtfle       ; gtfle calls fdhr which sets ix = dchan
       ld de, (svde)    ; first track/sector of file's data block
       call rsad        ; read that sector into dram buffer
       call ldhd        ; consume 9-byte file header via lbyt × 9
       ret
```

- **Calling convention**: `IX = caller's UIFA pointer (= &4B00 by our
  convention)`. `(svhdr)` is then set by the dispatcher (`b.s:440`)
  to that value, so `rxhed` reads UIFA across banks via `cmr;
  defw nrread`.
- **Internal IX management**: `gtfle` (`c.s:1348-1487`) calls `fdhr`
  (`c.s:984`) which does `ld ix, dchan`. After `gtfle` returns,
  `IX = dchan = &7800` in section B (= SAMDOS bank). `lbyt` calls
  inside `ldhd` (`f.s:494-497`) operate correctly on dchan.
- **Return state**: registers preserved per the convention; `IX` left
  at `dchan` in SAMDOS bank. Buffer at `dram` (`a.s:147 dram = fsa+256`)
  contains the first data sector; in-buffer pointer in `dchan`
  (`(ix+rptl)`/`(ix+rpth)`) sits past the 9-byte header (so the next
  `lbyt` returns the first user-payload byte).
- **Errors**: `gtfle:1357` → `rep26` ("File not found") on `fdhr` returning
  not-found. Longjmp via `derr`.
- **Our usage** (`stub.asm:42-43`): correct — we set `IX=&4B00` via
  `fill_uifa` and call `RST 8 / DEFB 158`.
- **Divergence**: none material. **OK.**

### Hook 147 — `HOFLE` (open new file for streaming write)

**Source**: `h.s:242-246`.

```
hofle: call rxhed       ; copy caller's IX → uifa via cmr/nrread (OK)
       call ofsm        ; "open file sector map" — see analysis below
       ret c            ; CY = name conflict (asks user) or disk full
       call svhd        ; sbyt × 9 — write the 9-byte file header
       ret
```

- **Calling convention per Tech Manual** (`docs/sam/sam-coupe_tech-man_v3-0.txt:4576-4578`):
  "ix must point to the UIFA. The routine will create a sector address
  map, and save the header to the disk and reset pointer RPT."
- **Actual SAMDOS source**: after `rxhed` returns, `IX` is still
  caller's UIFA pointer. `ofsm` then runs.
- **`ofsm` IX trace** (`c.s:1185-1260`):
  1. `:1185 push ix` — saves caller's `IX` (e.g. `&4B00`).
  2. `:1187-1190` zero 195 bytes at `sam` workspace (no IX use).
  3. `:1193 call fdhr` — fdhr does `ld ix, dchan` (`c.s:984`); after
     fdhr, `IX = dchan = &7800`.
  4. `:1194 jr nz, ofm4` — file-not-found path (the success case for new
     output). At ofm4, `IX` is still `dchan`.
  5. **`:1231 ofm4: pop ix` — restores caller's UIFA pointer.** `IX` is
     now back to caller's `&4B00`.
  6. `:1234-1237` `ld b,0; ofm5: ld (ix+ffsa), 0; inc ix; djnz ofm5` —
     **256 byte writes starting at `(caller_ix+ffsa=19)`, with `IX`
     incrementing each iteration**. With `caller_ix=&4B00` this writes
     to addresses `&4B13..&4C12` in **the bank currently in section B**.
  7. `:1238 pop ix` — back to caller_ix.
  8. `:1240-1244` ofm6 copies 11 bytes from `nstr1` to `(caller_ix+19..29)`.
  9. `:1246-1252` add 220 to `IX`, then ofm6 copies 33 bytes from
     `(uifa+15)` to `(caller_ix+239..271)`.
  10. `:1252 pop ix` — caller_ix.
  11. `:1254 call fnfs` — uses `(IX+fsam=34)`, `(IX+cntl=31)`,
      `(IX+cnth=30)` (`c.s:925-948`). Writes at `caller_ix+30..34`.
  12. `:1255 call svnsr` — `(IX+nsrh=18)`, `(IX+nsrl=17)` (`c.s:1556-1558`).
  13. `:1256-1257` `ld (ix+ftrk=32), d; ld (ix+fsct=33), e`.
  14. `:1258 call clrrpt` — `(IX+rptl=13)=0; (IX+rpth=14)=0` (`c.s:1519-1521`).

**Critical**: every memory access at step 6 onward goes to **whatever
bank is in section B at the time**. During the hook, that's the SAMDOS
bank (`PTDOS:12953-12955` set `LMPR=DOSFLG-1` so section B = SAMDOS
page). `caller_ix=&4B00` then maps to **SAMDOS bank offset `&B00`,
which is inside SAMDOS code** (verified by `hexdump` of
`reference/samdos/samdos2.bin` at offset `&B00`, which contains real
Z80 instructions).

**Therefore**: invoking `HOFLE` via `RST 8` with `IX = a UIFA address
in section B` (i.e. `&4B00`) overwrites SAMDOS's own running code with
zeros at offsets `&B13..&C12` and with filename / fsa data at
`&B0D..&B0E`, `&B11..&B12`, `&BD3..`, `&D7C..`, etc. The SAMDOS image
is corrupted in place.

**Why this hasn't been noticed in 35 years**: the canonical user-facing
high-level API is `HSAVE` (hook 132, `h.s:132-156`), which **does**
call `gtixd` (`h.s:145`) before `ofsm`. `gtixd` (`c.s:1513-1521`)
sets `IX = dchan` and `(buf) = dram` before hooks reference them.
Most existing applications either use `HSAVE` for whole-file writes
or use the higher-level ROM hooks like `HLOAD` (130) / `HGTHD` (129)
that don't go through `ofsm`. The byte-streaming pair `HOFLE`+`SBYT`+`CFSM`
appears to have been intended only for **internal use by SAMDOS's own
implementation of `HSAVE` / COPY / other commands**, never for
external `RST 8` callers — despite the Tech Manual at
`docs/sam/sam-coupe_tech-man_v3-0.txt:4576` documenting it as
externally-callable. This is a real gap between Tech Manual and
implementation.

- **Our usage** (`stub.asm:50-53`): triggers the bug above.
- **CY return**: only set when name already exists and user declines
  to overwrite (`ofsm:1219` `scf; ret`, after `ofm2:1198-1220` prompt
  via `cyes`). Disk-full goes through `rep24` longjmp
  (`fnfs → fns5:929 → rep24`) — **not** CY return. Our `jp c, fail`
  catch in `stub.asm:53` handles only the name-conflict case; disk-full
  longjmps to BASIC.
- **Severity**: **REAL BUG** — proximate cause of the 30 s timeout.

### Hook 148 — `SBYT` (save byte to currently open file)

**Source**: `c.s:533-551`.

```
sbyt:  push bc
       push de
       push hl
       push af
       call tfbf       ; check buffer-full; tfbf reads (ix+bufl..bufh+rptl..rpth)
       jr nz, sbt1     ; if not full, just write
sbt2:  call fnfs       ; allocate next sector
       ld (hl), d      ; link prev sector → next
       inc hl
       ld (hl), e
       ex de, hl
       call swpnsr     ; uses (ix+nsrl..nsrh)
       call wsad       ; flush prev sector to disk
sbt1:  pop af
       ld (hl), a      ; write the byte to (HL = current buffer pos)
       pop hl
       pop de
       pop bc
       jp incrpt       ; increment (ix+rptl..rpth)
```

- **Calling convention per Tech Manual** (`docs/sam/sam-coupe_tech-man_v3-0.txt:4580-4582`):
  "Save the byte in the Accumulator to the RAM pointed to by the
  pointer RPT. If the sector is full the data will be stored in
  the next sector pointed to by the sector address map."
- **What "RPT" means**: per Tech Manual `:4546-4547` "When the hook
  code explanation refers to 'RPT', it refers to the pointer used
  internally by SAMDOS." So RPT is internal — the caller is not
  expected to populate it directly. The expectation is that the
  preceding `HOFLE` set up RPT/buf inside SAMDOS's FCB. **Which it
  doesn't — see Bug #1.**
- **Actual register dependency**: `sbyt` reads `(IX+bufl=15)`,
  `(IX+bufh=16)`, `(IX+rptl=13)`, `(IX+rpth=14)` via `tfbf` →
  `grpnt` (`c.s:1531-1537`). Writes `(IX+rptl)+1` via `incrpt`
  (`c.s:1542-1545`). Calls `fnfs` (uses more IX-relative offsets)
  on buffer-full.
- **Required IX**: `dchan = &7800`. With `IX=dchan`, `(ix+bufl..)` is
  `dram` pointer (set by prev `gtixd`), which is the actual DOS RAM
  buffer at `&7913` (`a.s:147 dram = fsa+256 = dchan+19+256 = &7913`).
- **What happens with `IX=&4B00` left over from `fill_uifa`**: `tfbf`
  reads buffer pointer from `&4B0F..&4B10`, which after `ofsm` ran
  contains arbitrary SAMDOS-bank bytes. The `ld (hl), a` at `:547`
  writes to that garbage address. Even if the address happens to be
  writable RAM, no actual disk write occurs because `wsad` is never
  called (the buffer never reports "full" because the count is also
  garbage). So our 4 NOP bytes are written to nowhere persistent and
  never reach a sector.
- **Severity**: **REAL BUG** — same root cause as Bug #1.

### Hook 152 — `CFSM` (close file / flush directory entry)

**Source**: `c.s:1306-1343`.

```
cfsm:  call grpnt          ; uses (ix+bufl..rpth)
       ld a, c
       and a
       jr nz, cfm1
       ld a, b
cfsm1: cp 2
cfsm2: jr z, cfm2
cfm1:  ld (hl), 0          ; pad rest of sector with zero
       call incrpt
       jr cfsm
cfm2:  call gtnsr          ; (ix+nsrl..nsrh)
       call wsad           ; flush sector to disk
       call decsam
       push ix
       ld a, &40           ; flag: looking for THIS file's dir entry
       call fdhr           ; fdhr sets ix = dchan
       jp nz, rep25
       call point
       ld (svix), ix
       pop ix              ; restore the *caller's* IX
       push ix
       ld b, 0
cfm3:  ld a, (ix+ffsa)     ; READ from caller_ix+19..274 (256 bytes)
       ld (hl), a          ; write into the dir-entry sector
       inc ix
       inc hl
       djnz cfm3
       ld ix, (svix)       ; restore dchan (saved at cfm2)
       call wsad           ; flush dir-entry sector
       pop ix
       ret
```

- **Calling convention per Tech Manual** (`docs/sam/sam-coupe_tech-man_v3-0.txt:4599-4601`):
  "Close file sector map. This routine empties the RAM and copies
  the header area on to the directory, closes the file, then
  updates the directory."
- **Required IX**: `dchan`. `cfm3` reads 256 bytes from `(IX+ffsa..)`
  which is the FSA region populated by `ofsm` (assuming `IX=dchan`
  during ofsm).
- **What happens with `IX=&4B00`**: `cfm3` reads SAMDOS code bytes
  from `&4B13..&4C12` and writes them into the directory entry.
  Result: the dir entry is filled with SAMDOS code rather than a
  valid sector address map. The file is unreadable on next mount,
  and even worse, the sector address map's bits-set pattern claims
  ownership of unrelated random sectors → next FORMAT or new file
  can collide.
- **Severity**: **REAL BUG** — even if Bug #1 were absent, `cfsm`
  would still write garbage into the directory.

### Hook 159 — `LBYT` (load byte from currently open file) — **not used by stub**

**Source**: `c.s:557-570`.

Same `IX`-dependency as `SBYT`. Works correctly when called after
`HGFLE` (which sets `IX=dchan` via `gtfle`/`fdhr`). **Not exercised
by our stub** since we skip the read entirely (`stub.asm:46-47`),
so this hook isn't a current blocker.

### Hook 166 — `HERAZ` (erase file by name) — **not used by stub**

**Source**: `h.s:262-267`.

```
heraz: call rxhed       ; uses cmr/nrread, OK
       call ckdrv       ; sets dstr1 / drive
       call findc       ; presumably sets ix = dchan (via fdhr)
       jp nz, rep26     ; "File not found" longjmp
       ld (hl), 0       ; zero the dir-entry type byte to mark erased
       jp wsad          ; write the dir sector back
```

`findc` calls `fdhr` (line not shown, in `c.s` neighbourhood) which
sets `IX=dchan` per the standard pattern. So `HERAZ` works correctly
externally for files that exist; it longjmps with "File not found"
if the file is absent. The doc note in `stub.asm:38-40` is correct
that you can't probe-with-recover, but the comment is moot since we
don't call HERAZ.

---

## Stack and paging (audit-grade verification)

### Stack
The stub starts with `DI` at `stub.asm:35`. ROM `PTDOS` then pushes
twice and `LD SP, &8000` (`12956`). The hook runs on a fresh stack
ending at `&8000`. SAMDOS does its work, returns; ROM pops back into
caller's SP. **The stub's stack at &6000-area is preserved correctly
by the ROM layer** (`PTDOS` saves and restores SP via `PUSH HL` /
`POP HL` at `12959`/`12970-12974`).

So the post-hook `OUT (&DEAD), &C0; HALT` sequence at `stub.asm:71-74`
**would** execute correctly **if** the hooks returned cleanly. They
don't (Bug #1).

### Paging
- **Section A** during hook: ROM0 (LMPR bit 5 = 0, low 5 = DOSFLG-1).
- **Section B** during hook: SAMDOS bank (LMPR low 5 + 1 = DOSFLG).
- **Section C** during hook: HMPR-controlled, **preserved** across
  the hook (ROM does not touch port 251).
- **Section D** during hook: HMPR+1, also preserved.

So the stub at `&6000` (section B) is paged out during the hook.
This is fine for execution — the stub's only role at hook time is to
have already written the UIFA at `&4B00`. SAMDOS's `cmr`/`nrread`
pages the BASIC sys page in at section B to read the UIFA. Our stub
runs in BASIC's page 1 (LMPR=0 default), and `&4B00` is in that page
at offset `&B00`, which is the BASIC sys page region housing the ROM's
`HDR` 80-byte header buffer. **Reads work.** The same UIFA-content is
read by `rxhed` correctly.

The bug is not in the cross-bank read path — it's purely in `ofsm` /
`sbyt` / `cfsm` doing **direct** memory access via IX-relative `LD`
instructions, which go to the bank currently in section B (= SAMDOS
itself), not to the user's bank.

### `DI` / `EI`
ROM `PTDOS` does `DI` (`12954`) / `EI` (`12957`) inside the dispatch
window. Our `DI` at `stub.asm:35` is fine but redundant for hook
calls; it's needed so the post-hook `OUT (&DEAD)` is uninterrupted.
**No issue.**

---

## Concrete fix list

### Fix #1 — switch to `HSAVE` for the M0 stub

**Severity**: blocks M0.

`HSAVE` (hook 132) is the canonical write-whole-file API and **does**
work externally because it calls `gtixd` at `h.s:145` before `ofsm` /
`svhd` / `svblk` / `cfsm`. Pre-fill UIFA bytes 31–36 (start page,
load addr, num pages, length-mod-16K) with values matching our
4-byte payload, point HL/DE at the payload, and call `RST 8 / DEFB 132`.

Code change shape (`stub.asm`, replacing the create_output → emit
loop → close_output sequence):

```asm
                ; --- emit OUT via HSAVE (single-shot whole-file write) ---
                ld      hl, name_OUT
                call    fill_uifa_for_hsave  ; new helper; see below
                rst     8
                defb    132                  ; HSAVE
                ; HSAVE longjmps on error; on success just falls through
```

`fill_uifa_for_hsave` populates byte 0 (type=19) + name + ext + bytes
31–36:

| Byte | Value | Reason |
|------|-------|--------|
| 31 | 0x01 | start page = 1 (page bytes match our payload "page-form", in REL PAGE form: `(page=1, offset=0xA000)` per `docs/notes/sam-paging.md:760-783`); `hsave:140-143` ANDs with `&1F` and writes to HMPR. |
| 32-33 | LE 0xA000 | page offset (8000-BFFF form) — `hd0d1`. |
| 34 | 0x00 | num 16K pages — for a 4-byte file, length < 16K, so 0. `hsave` ANDs with `&1F`. |
| 35-36 | LE 0x0004 | length-mod-16K = 4 bytes. |

This puts UIFA bytes 0–36 in canonical form and fields 37–47 can stay
`&FF` for our purposes (HSAVE doesn't read them).

For the data: HSAVE expects HL=ptr-to-data, BC=byte-count? Actually
re-checking `h.s:148-152`:

```
hsave: ... call svhd        ; write 9-byte header
       ld hl, (hd0d1)         ; HL = page offset (saved by rxhed/hconr from UIFA bytes 32-33)
       ld de, (hd0b1)         ; DE = length-mod-16K (saved from UIFA bytes 35-36)
       call svblk             ; write `length` bytes from `HL`
       call cfsm
```

So HSAVE pulls source addr from UIFA bytes 32-33 and length from
UIFA bytes 35-36. **No direct register I/O — everything via UIFA.**

That means our payload at `payload:` in `stub.asm:85` (currently at
some absolute address determined by pyz80) needs to be at an address
that matches the UIFA bytes 32-33 we set. With `org &6000`, payload
ends up at around `&6000 + jp_table + sam_io.inc_size + ...`. We
either:
(a) Hard-code payload at a known address with explicit `org` (e.g.
`&6FF0`), then set UIFA bytes 32-33 = `(payload_addr & 0x3FFF) | 0x8000`.
(b) Fix the UIFA bytes 32-33 at compile time using pyz80's symbol
resolution — `defw payload | 0x8000`.

Option (b) is cleaner. The pyz80 expression `(payload & 0x3FFF) | 0x8000`
produces the section-C-form address.

The page byte at UIFA byte 31 is the **physical page**. Our stub runs
with LMPR=0 default; payload at `&6FF0` is in section B = page 1, so
byte 31 = 1.

### Fix #2 — bytes 40–47 should be `&00`, not `&FF`

**Severity**: cosmetic. `fill_uifa` line `sam_io.inc:65-69` pads
bytes 15–47 with `&FF`. Per `b.s:289-290` SAMDOS's internal default
has bytes 40–47 as `&00`. Change `fill_uifa` to pad 15–39 with `&FF`
and 40–47 with `&00`, OR change the on-disk default in `b.s` (we don't
control SAMDOS source). For our use case `&FF` is harmless because
neither hook reads bytes 40–47.

**Recommendation**: leave as-is (`&FF` for the whole filler) — saves
one block of code in `fill_uifa`. Only change if we discover a hook
that does read bytes 40–47.

### Fix #3 — UIFA byte ordering note

`sam_io.inc:43-44` documents `UIFA_PAGES: equ 34` and `UIFA_LENGTH: equ 35`.
Tech Manual says byte 34 is "NUMBER OF PAGES IN LENGTH" and bytes 35-36
are "MODULO 0 TO 16383 LENGTH" (`docs/sam/sam-coupe_tech-man_v3-0.txt:4487-4493`).
Our names match. **No fix needed.**

### Fix #4 — `close_input` is a no-op, document explicitly

`sam_io.inc:105 close_input: ret` is correct and documented. Keep it
but add a `; SAMDOS 2; MasterDOS exposes hook 135` comment to clarify
the pattern for future MasterDOS support. **Optional.**

### Fix #5 — verify the post-hook `OUT (&DEAD)` sequence

If we adopt Fix #1 (use HSAVE), the post-hook code at `stub.asm:71-74`
should work as-is. **No fix needed.** But we should test in isolation
by writing a stub that does only `DI; LD BC, &DEAD; LD A, &C0;
OUT (C), A; HALT` (skip all SAMDOS hooks) and confirm SimCoupé
exits cleanly with this minimal program. That eliminates all
SAMDOS-related variables for the magic-port test.

---

## Open questions

1. **Why does the SAMDOS Tech Manual document `HOFLE` / `SBYT` / `CFSM`
   as externally-callable when the source clearly only sets up `IX=dchan`
   via `HSAVE`?** Possibilities:
   (a) Documentation bug, never fixed in v3.0.
   (b) Earlier SAMDOS 1.x had a working external streaming API that
       was lost in the SAMDOS 2 rewrite.
   (c) The intended convention was for callers to pre-populate `dchan`
       themselves before `HOFLE`, but no source documents this.
   (d) MasterDOS fixed the bug; SAMDOS 2 didn't, but the docs were
       shared and not updated.
   Resolving this would require looking at MasterDOS source (not
   available locally) or at SAMDOS 1.x source. **Uncertain — needs
   verification.** Practically irrelevant to M0; we route around it
   via Fix #1 (use HSAVE).

2. **Is there a way to use the streaming API by pre-paging SAMDOS into
   section C and reaching dchan at `&8000+&3800 = &B800`?** The
   application could `OUT (251), DOSFLG` to put SAMDOS in section C,
   then `IX = &B800` would point at dchan. But:
   - `rxhed` reads `(svhdr) = caller_ix = &B800` via `cmr; nrread`.
     `cmr` pages sys page into section B, not section C, so &B800 is
     no longer SAMDOS. Reads would fail (or read garbage from sys page
     offset &7800, which is outside the sys page anyway).
   This path is also broken. **No clean external streaming path exists
   in SAMDOS 2.**

3. **Could the M0 stub install a custom `(hksp)` handler to catch
   longjmps and convert errors into clean exits?** `(hksp)` lives at
   `gnd+&62 = &4062` in section B (when SAMDOS is paged in). The
   stub would need to page SAMDOS in itself, write `(hksp)`, page out,
   then call hooks. The `LD (HL), value` sequence is straightforward;
   the paging dance is messy. **Possible but out of scope for M0.**

4. **Why does the existing `docs/notes/sam-file-io.md` claim the
   streaming API works?** That doc was written as a Task 6 spike based
   on the Tech Manual + COMET (which uses HGTHD/HLOAD only,
   `comet.asm:194-203, 1273-1284`). The spike did NOT verify against
   the SAMDOS source's `ofsm` / `sbyt` / `cfsm` IX dependency. The
   conclusion in `sam-file-io.md` that the streaming API "works for
   M0" is **incorrect** as written; this audit supersedes it on the
   point of HOFLE/SBYT/CFSM. The `sam-file-io.md:155-163` claim that
   "abandoning the read is safe" is correct in isolation; the broader
   API claim is wrong.

5. **Is COMET's confirmation actually relevant?** COMET uses HGTHD
   (129) + HLOAD (130) + HSAVE (132 — the high-level whole-file
   `HSAVE` IS the working path) but **NOT** HOFLE (147). So COMET
   working tells us nothing about HOFLE's correctness. The
   spike's appeal to COMET as a "cross-check" was misplaced for the
   streaming-byte API — only valid for the open-with-header API.

---

## References

- `~/git/samdos/src/h.s` — hook entry points: `hgfle:252`, `hofle:242`,
  `heraz:262`, `hsave:132`, `hgthd:59`, `hload:70`.
- `~/git/samdos/src/c.s` — internal helpers: `sbyt:533`, `lbyt:557`,
  `cfsm:1306`, `ofsm:1185`, `gtfle:1348`, `fdhr:984`, `gtixd:1513`,
  `clrrpt:1519`, `grpnt:1531-1537`, `tfbf:520-529`, `fnfs:895-951`.
- `~/git/samdos/src/b.s` — dispatcher: `hook:439-470`, `samhk:497-538`,
  `rfhk:475-479`, `(svhdr):215`, `(hksp):160`, internal `uifa:278-290`.
- `~/git/samdos/src/a.s` — equ table: `dchan:133`, `dram:147`,
  `bufl..fsam:111-124`, `nrread:23`, `nrrite:24`, `cmr:28`.
- `~/git/samdos/src/d.s` — error handling: `derr:430-460`,
  `rep0..rep31:294-388`.
- `~/git/samdos/src/f.s` — `svhd:462-471`, `ldhd:494-497`.
- `docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt` —
  `PTDOS:12944-12978`, `JSVIN:715-746`, `NRREAD:370-371`,
  `HDR EQU:1236`, `HDL EQU:1237`.
- `docs/sam/sam-coupe_tech-man_v3-0.txt` — UIFA layout `:4459-4496`,
  hook list `:4515-4541`, hook descriptions `:4544-4627`.
- `docs/notes/sam-file-io.md` — pre-Task-7 spike (superseded for the
  streaming API by this audit).
- `docs/notes/sam-paging.md` — paging mechanics, especially
  `:541-560` (sysvars) and `:600-654` (SAMDOS hook dispatch paging).
- `docs/notes/sam-file-header.md` — file-header layouts cross-reference.
- `reference/comet-decoded/comet.asm:194-203, 1273-1284` — COMET's
  HGTHD/HLOAD usage (does NOT exercise HOFLE/SBYT/CFSM).
- `reference/samdos/samdos2.bin` — vendored binary, hex-inspected at
  offset `&B00` to confirm SAMDOS code (not scratch RAM) lives there.
