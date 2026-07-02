# SAMDOS file I/O: the HLOAD and HSAVE idioms

**Status**: living reference. Captures the research findings (from surveys of
COMET's source, the SAM ROM disassembly, the SAMDOS source, and the wider
sam-corpus) that underpin every file read and write the SAM-side assembler
performs. The READ side needs a paging trampoline; the WRITE side does not —
the two halves below explain both patterns, why they work, and the register /
error-path facts any caller must respect.

**This is the project's DOS hook-layer reference.** The hooks documented here
(HGTHD / HLOAD / HSAVE / HOFLE / HSBYT …) are the **SAMDOS 2 hook interface**,
which **B-DOS implements compatibly** — and B-DOS is the project's **boot DOS**:
as of i75 (the q10 resolution) `tools/build-disk` packs B-DOS AL 1.5a into
every shipped/CI disk by default, and it supplies the Trinity/Atom mass-storage
records that Phase 2/3 need. SAMDOS 2 is retained as a compatibility build
(`tools/build-disk -dos reference/samdos/samdos2.bin …`). Hook portability
SAMDOS 2 ↔ B-DOS AL is **verified**: i62 booted one probe binary under both
SAMDOS 2 + floppy and B-DOS AL 1.5a + emulated Atom Lite and round-tripped
HSAVE / HGTHD / HLOAD through both backends, and i71 verified statically that
the Trinity fork (B-DOS 1.5t) leaves all 39 hook vectors and their handler
bodies untouched — only the sector-device layer is swapped. The hook table
(`docs/notes/bdos-version-landscape.md` §"SAMDOS compatibility") confirms every
hook the idioms below use is present and SAMDOS-compatible in B-DOS, with the
empirical proof in §"Empirical verification (i62)" and the static fork analysis
in `docs/notes/bdos-trinity-fork-analysis.md`. On top of the SAMDOS-compatible
surface B-DOS adds the record layer — HRECORD (&9C, the one backend-conditional
step: RECORD 0 = floppy, RECORD n = an 800 KB mass-storage slice) and friends
(HVEBK &9D, HLBYT &9F, …). The line-level source citations below remain to the
**SAMDOS 2 source** (`samdos/src/*.s`), which stays authoritative for the
shared hook semantics; the B-DOS additions sit above that surface.

Source-citation conventions: `samdos/src/*.s` line references are to the
SAMDOS 2 source (cloned at `~/git/samdos` from https://github.com/stefandrissen/samdos — the reconstructed, commented SAMDOS 2 source; the assembled binary is vendored at
`reference/samdos/samdos2.bin`); `comet.asm` references are to
`reference/comet-decoded/comet.asm`; ROM references are to
`docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt` and Tech Manual
references to `docs/sam/sam-coupe_tech-man_v3-0.txt` (both in the local docs
corpus).

## Background — the section-C window

SAMDOS's whole-file hooks read and write memory **through section C
(`&8000-&BFFF`)**. The Tech Manual specifies that HLOAD requires
`HL ∈ &8000-&BFFF`. Which *physical* page that window shows is governed by
HMPR — so "load a file into physical page N" and "save a file from physical
page N" are both, at bottom, HMPR-management problems:

- **HLOAD (hook 130) does not touch HMPR.** If the destination is any page
  other than the one currently mapped at section C, the *caller* must arrange
  the mapping — from code that survives the mapping change. That is the READ
  trampoline.
- **HSAVE (hook 132) manages HMPR itself.** It derives the source page from
  UIFA byte 31, sets HMPR internally, auto-increments across `&C000`, and
  restores HMPR before returning. The WRITE side needs **no trampoline** —
  just a correctly-populated UIFA.

## READ — the HLOAD trampoline pattern

### The COMET pattern

COMET — the SAM Coupé–era assembler that is the closest analogue to this
project — solves the "load into arbitrary memory" problem with a small
trampoline in section A. Source: `reference/comet-decoded/comet.asm:1265-1284`.

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

- The trampoline lives in section A (COMET copies it to `&4F00`), which is
  mapped via LMPR — independent of HMPR. Changing HMPR while running from
  section A doesn't yank the instruction stream out from under us.
- HL is `&8000` (section C, satisfies the Tech Manual constraint).
- After the HMPR change, section C physically maps to the page where the data
  should land.
- HLOAD writes through `&8000+` into the destination page.
- HMPR is restored on return so the rest of the program sees its original
  section-C mapping.

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

COMET deliberately pages section C to overlap its own LMPR-page. The
source-file ends up "above" COMET's code in physical RAM. HMPR is restored
after HLOAD returns.

### The BASIC `LOAD ... CODE addr` pattern (the canonical one)

The same idiom, baked into ROM. From
`docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt:22407-22512`, the chain
`CDSCVE → STSPEC2/3 → LDVDBLK → DOSLD` does:

1. `LD A, (LDCO) / ADD A, C` — derive absolute destination page from caller's
   address.
2. `CALL TSURPG` (ROM `&3FDF`) — `TSURPG` is "START SWITCHED IN AT HL" — sets
   HMPR to the bottom 5 bits of A, preserves the top 3.
3. `CALL RDLLEN` — fills C / DE with pages-count and length-mod-16K.
4. `LD IX, HDR` — set UIFA pointer.
5. `RST 08H / DB 0x82` — HLOAD.

Every disk that uses `LOAD ... CODE someaddress` exercises this. Sampled
corpus disks:

- **SC Monitor Pro 1.2**: `auto` BASIC line 10 — `INPUT "Ram page start...";
  LET m=(r+1)*16384: LOAD "moncode" CODE m` loads a 32 KB file into a
  user-chosen high-RAM page. The ROM decomposes `m → page/offset` and HMPRs
  before HLOAD.
- **Secretary Word Processor**: `LOAD "E_Manual" CODE` with a fixed
  start=147456 (physical page 9 offset 0). Same pattern.

Conclusion: the trampoline-around-HLOAD pattern is the established SAM idiom
for "load into arbitrary RAM". Every real SAM program uses it (directly via
the ROM, or via its own copy of the trampoline).

### Load-side hook survey (no hook avoids the trampoline)

Full survey of `samdos/src/b.s:497-538`:

| Hook | What it does | Helps? |
|------|--------------|--------|
| 130 HLOAD | `dschd → ldblk`. HL must be `&8000-&BFFF`. | This is the one to use. |
| 131 HVERY | Same setup as HLOAD but compares. Auto-HMPR-increment at `&C000` boundary (`h.s:115-129`). | No — verify only. |
| 149 HWSAD | Write 512-byte sector from arbitrary HL via `cals` page-translation. | No — raw sector. |
| 150 HSVBK | `jp svblk`. Save block. | No — save side. |
| 160 HRSAD | Read 512-byte sector at (track, sector) into arbitrary HL via `cals`. | No — raw sector. |
| 161 HLDBK | `jp ldblk` directly. Skips `dschd` so re-uses prior HLOAD/HGTHD state. Same HL constraint. | No — same constraint. |

`cals` (`samdos/src/h.s:308-321`) is the only routine that translates an
arbitrary HL to (HMPR-page, `&8000`-window-offset). It is used ONLY by HRSAD
and HWSAD, both of which work in raw 512-byte sectors at (track, sector)
coordinates. **No file-aware hook uses `cals`.**

The Tech Manual list (`docs/sam/sam-coupe_tech-man_v3-0.txt:4515-4536`) is
complete — no undocumented file-IO hooks.

### The trampoline contract

The caller sets up:

- `HL = &8000` (or `&8000 + offset` if loading mid-section-C)
- `B = target physical page` (HMPR value to set)
- `IX = UIFA at &4B00`
- `C = pages count` (from HGTHD's DIFA at +34)
- `DE = length mod 16K` (from DIFA at +35, with bit 7 of D cleared)

then `CALL`s the trampoline, which restores HMPR on return. Optionally the
SAMDOS error vector `&5BC0` can be patched to point into the trampoline so
HMPR is restored even on HLOAD error (COMET does this).

This handles loads of any size into any 16K-aligned page. For loads > 16K,
HLOAD's internal multi-page path (`samdos/src/c.s:682-699`, in
`ldblk`/`ccnt`) auto-increments through the source file; combined with the
trampoline's HMPR control, this populates arbitrary contiguous regions.

### Pre-built trampoline reference

A minimal verbatim Z80 trampoline implementing the COMET pattern:

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

(The trailing `DI` mirrors COMET's; SAMDOS's RST 8 dispatch does `EI` so DI
here restores the no-interrupts invariant typical of batch programs.)

### The production implementation (src/trampoline.asm) hardens two points

The assembler's in-tree trampoline (`src/trampoline.asm`, `trampoline_body`,
copied to `TRAMPOLINE_DST = &7E00` in section B at startup) follows the COMET
shape but replaces two pieces that are unsafe under this project's memory
layout — the full rationale lives in the `src/trampoline.asm` header comments:

1. **Static HMPR save instead of `push af`/`pop af`.** The snippet's push/pop
   pair brackets the HMPR change, which only works if SP points into
   LMPR-stable memory. With the stack in section D (`SP = &C100`, and section
   D = HMPR+1), the push would write to one physical page and the pop read
   from another. The production trampoline saves the HMPR byte to a static
   section-B address (`HMPR_SAVE`, next to the trampoline body), which is
   LMPR-stable across the HMPR change.
2. **SP switch around the RST 8.** With caller SP still in section D, the
   RST 8 return-address push lands inside the load-target's adjacent page —
   and a multi-page HLOAD's auto-paging spill can overwrite it (a ≥ ~16.6 KB
   file reaches the pushed return address; PTDOS's eventual RET then pops
   garbage and hangs). The trampoline switches SP to a section-B-stable
   scratch stack (`TRAMP_SAFE_SP`) for the RST 8 and restores it after HMPR is
   restored — the same workaround COMET applies via `LD SP, (sproom)` at its
   call site (`comet.asm:1189`). With the switch in place, paged-IN files of
   ≥ 32 KB load cleanly.

### Where the READ pattern is load-bearing

Every off-axis HLOAD in `src/loader.asm` routes through the trampoline:
ENCTAB into physical page 4, the IN `.tbn` buffer into pages 7..12, the
disassembler payload into page 15, the sysreg lookup data into page 13, and
the `BUILD_TESTS` payloads into pages 12-14. The runtime-*read* counterpart
(mapping a loaded page into section A via an LMPR swap for encoder/reader
access) is separate machinery, also in `src/trampoline.asm`
(`enctab_map_in` / `enctab_map_out` and the reader bracket).

## WRITE — HSAVE needs no trampoline

### Findings worth noting (read first)

1. **There is no HSAVE trampoline.** Unlike HLOAD, **HSAVE manages HMPR by
   itself**. At `samdos/src/h.s:136-143` HSAVE reads the current HMPR, ORs
   UIFA byte 31's low 5 bits in, and writes that to port 251. At
   `h.s:154-155` it restores the original HMPR before returning. Inside the
   save loop (`samdos/src/c.s:354-369`, inside `ctas`) HSAVE auto-increments
   HMPR every time it crosses `&C000`, so multi-page outputs flow across
   physical pages without help from the caller. The HLOAD-style "save HMPR,
   set HMPR, RST 8, restore HMPR" trampoline is **unnecessary for HSAVE**:
   the bytes inside that trampoline are redundant with what HSAVE already
   does internally. The save-side call is simply: populate UIFA byte 31 with
   the OUT-buffer's **physical page** and issue `RST 8 / DEFB 132`.
2. **COMET does NOT have a save-side counterpart.** COMET hands its "O Save
   objectcode" menu key back to BASIC (`comet.asm:131-133 JP &0013` to
   BASIC's KEYS handler, then BASIC executes `SAVE "name" CODE …`). The only
   RST 8 hook codes COMET issues are 129 (HGTHD) and 130 (HLOAD) — see
   `grep -n 'DEFB +13[0-9]'` returning only lines 1278 (`DEFB 130`) and
   202 + 1330 (`DEFB 129`). Likewise `ltran.asm` and `sctran.asm` (the
   LERM/SC source converters that ship with COMET) do all their work in
   HMPR-paged RAM only and never call any save-side hook. The SAM-era idiom
   for saving large outputs from assemblers is to **let BASIC do the SAVE**,
   not to call HSAVE directly. This project follows a different path because
   it cannot return to BASIC: `assembler.asm`'s top-level loop ends in
   `di / halt`, and AUTO has been consumed.
3. **The "open + byte-stream" hooks (`HOFLE`/`SBYT`/`CFSM`) are broken in
   canonical SAMDOS 2** — see `docs/notes/sam-stub-audit.md` §"TL;DR —
   concrete bug list" (bugs #1, #2, #3). `HSAVE` is the only working
   save-side path. The pre-built snippet below uses HSAVE for that reason.
4. **The runtime WRITE question for the OUT buffer (how the assembler adds
   bytes to the OUT page while running from section C) is the harder
   question** — an LMPR-swap problem, identical in shape to ENCTAB's
   runtime-read pattern. This document covers only the *flush* — how to call
   HSAVE on a paged OUT buffer. The runtime emit path is designed in
   `docs/specs/paged-out-design.md` (OUT in physical pages
   5-6, written via section B).

### The HSAVE pattern

Citations: `samdos/src/h.s:132-156` (HSAVE body); `c.s:721-723` (svblk
entry); `c.s:354-369` (ctas's HMPR auto-increment at the `&C000` boundary).

HSAVE is structured as:

```asm
hsave:         call setf3        ; mark "save in progress" flag
               call rxhed        ; copy 48-byte UIFA from caller's IX
                                 ; (via cmr/nrread; reads through
                                 ; the BASIC sys page paged into B)
               call ckdrv        ; resolve drive

               in   a, (251)     ; A = current HMPR
               ld   (port3), a   ; save it
               and  %11100000    ; B = current HMPR top 3 bits
               ld   b, a
               ld   a, (uifa+31) ; A = UIFA byte 31 (start page)
               and  %00011111    ; low 5 bits
               or   b            ; OR with original top 3 bits
               out  (251), a     ; HMPR := <orig top 3 | UIFA[31] low 5>

               call gtixd        ; IX := dchan (does NOT depend on caller IX)
               call ofsm         ; allocate sector map, error if disk full
               jr   c, hsave1    ; CY = name conflict, restore HMPR + return
               call svhd         ; write the 9-byte file header
               ld   hl, (hd0d1)  ; HL = UIFA bytes 32-33 (section-C source)
               ld   de, (hd0b1)  ; DE = UIFA bytes 35-36 (length mod 16K)
               call svblk        ; the bulk save (auto-pages HMPR across &C000)
               call cfsm         ; close: flush buffer, update directory

hsave1:        ld   a, (port3)   ; restore HMPR
               out  (251), a
               ret
```

Two facts make the call-site simple:

- **HSAVE itself saves and restores HMPR.** Lines 136-137 save the entry HMPR
  to `port3` (a SAMDOS bank variable, unaffected by HMPR changes); lines
  154-155 restore it. The caller does not need to bracket the call.
- **HSAVE auto-pages across `&C000`.** `svblk → sblok → ctas` at
  `c.s:354-369` watches `(svhl)` (the current source pointer, kept in
  `dchan`'s scratch); whenever H hits `&C0` it resets bit 14 of H (rewinds
  the source pointer to `&8000`) and does `IN A,(251); INC A; OUT (251),A` —
  i.e. HMPR := HMPR+1. Combined with UIFA byte 34's page-count, HSAVE can
  save any contiguous run of pages without the caller's involvement.

### Why it works (paging analysis)

When the caller does `RST 8 / DEFB 132`:

1. ROM `ERROR2`
   (`docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt:12906-12918`)
   reads the DEFB byte from the caller's section C via `LD A,(DE)` — the
   caller's HMPR is still in effect at this point.
2. ROM `PTDOS` (`...rom-v3.0:12944-12978`) saves the caller's LMPR, pages the
   SAMDOS bank into section B (LMPR low 5 bits = DOSFLG), switches SP to
   `&8000`, dispatches via the SAMDOS hook. HMPR is **not touched** by PTDOS.
3. SAMDOS hook entry (`samdos/src/b.s:439-470`) saves the caller's IX to
   `(svhdr)`, captures other register state, and jumps to `hsave`.
4. HSAVE reads `(svhdr)` to find the caller's UIFA — but reads it via
   `cmr / defw nrread`, which pages the BASIC sys page into section B for the
   read and then reverts (`docs/notes/sam-stub-audit.md` §"Cross-bank reads
   and writes — `cmr; defw nrread/nrrite`"). So the caller's UIFA at `&4B00`
   is read regardless of the caller's physical layout (provided `&4B00` is in
   the BASIC sys page, which the assembler enforces by convention).
5. HSAVE OUTs the new HMPR derived from UIFA byte 31. Section C now maps to
   the OUT-buffer's physical page. The caller's code page (where the running
   assembler lives) is paged out of section C — but the CPU's PC is in SAMDOS
   (section B), so this is fine. The caller's stack in the `&C100` /
   section-D area is *also* paged out — but SAMDOS has already switched SP to
   `&8000`, so the caller's stack frame isn't touched during the hook.
6. HSAVE reads source bytes from `HL = (hd0d1)` (= UIFA[32..33]) through
   section C. Each page-crossing auto-bumps HMPR.
7. On return, HSAVE restores port3 (= caller's entry HMPR). PTDOS then
   restores LMPR. Section C is back to the caller's code page, section B is
   back to the caller's LMPR-set value, the stack is back to the
   `&C100`-area. `RET` pops PC = address-after-DEFB-132, which is in section
   C — and section C now maps to the right page — so execution resumes
   correctly.

**No HMPR trampoline needed.** No section-B helper code. No static save slot.
The call is just `RST 8 / DEFB 132` after UIFA setup.

### Why this differs from HLOAD

HLOAD has no equivalent of HSAVE's UIFA-byte-31 → HMPR mechanism. HLOAD reads
the destination address from registers `(hkhl)` / `(hkbc)` / `(hkde)`
(captured at hook entry from the caller's HL/BC/DE — see
`samdos/src/b.s:443-446` and `h.s:74-90 dschd`). It does NOT touch HMPR. So
if the caller wants HLOAD to write to a physical page other than the one
currently mapped in section C, the caller must arrange that mapping itself.

That's why the HLOAD trampoline exists: to do the HMPR change around the
RST 8 from a location (section B) that's unaffected by the HMPR change.

HSAVE took a different design choice: the source page is encoded in the UIFA,
not in registers, and HSAVE does the HMPR dance itself. Lucky for us — that
saves a trampoline body, a stack-save slot, and the entire section-B copy
machinery for the save path.

### Save-side hook survey

The full list of save-side hooks (`samdos/src/b.s:497-538`):

| Hook | What it does | Trampoline needed? | Verdict |
|------|--------------|--------------------|---------|
| 132 HSAVE | Whole-file save. Reads UIFA[31] → HMPR. Calls `gtixd → ofsm → svhd → svblk → cfsm`. Restores HMPR. (`h.s:132-156`) | No — HSAVE does it internally. | **This is the one to use.** |
| 147 HOFLE | Open new file for byte-streaming. (`h.s:242-246`) | N/A — broken in SAMDOS 2 (audit Bug #1). | Skip. |
| 148 SBYT  | Save next byte through `(IX+rptl..rpth)`. (`c.s:533-551`) | N/A — broken without working HOFLE (audit Bug #2). | Skip. |
| 150 HSVBK | `jp svblk` directly. (`h.s:249`) Reuses `dchan` state set by a prior HSAVE / HOFLE call. Auto-pages HMPR like HSAVE's body does. | No (same logic as HSAVE). | Could be useful for incremental multi-block save with a reused FCB — interesting if one assembler invocation must ever emit multiple files. Not needed for the "single HSAVE at end of pass 2" model. |
| 149 HWSAD | Write 512-byte sector at (track, sector) from arbitrary HL via `cals`. (`h.s:289+`) | Possibly — but writes raw sectors, not files. | Wrong abstraction. |
| 152 CFSM  | Close file / flush directory. (`c.s:1306-1343`) | N/A — broken externally without an HSAVE-set FCB (audit Bug #3). | Skip. |

Only HSAVE is viable.

### Pre-built code snippet (the call-site shape)

This is the save call-site, implemented in-tree as `save_out_file`
(`src/main_loop.asm`). The UIFA byte-layout comment is the durable part —
cross-checked against `samdos/src/h.s:132-156` and the Tech Manual
(`docs/sam/sam-coupe_tech-man_v3-0.txt:4459-4496`):

```asm
; -----------------------------------------------------------------------
; save_out_file_paged — HSAVE the paged OUT buffer.
;
; Caller contract: the OUT-buffer's contents have been emitted into
; physical page OUT_PAGE.  Bytes 0..OUT_LEN-1 of that page hold the
; emit stream.  OUT_LEN may exceed 16 KB (multi-page case): if so,
; pages OUT_PAGE, OUT_PAGE+1, ... must be contiguous in physical
; memory (the page allocator must enforce this — OUT may not be
; discontiguous across physical pages).
;
; HSAVE's UIFA layout for code-type files:
;
;   byte 0       : type = 19 (CODE)
;   bytes 1-10   : 10-char name, space-padded
;   bytes 11-14  : 4-char ext, space-padded (typically all spaces)
;   bytes 15-30  : filler; HSAVE doesn't read these (any value OK)
;   byte 31      : start page (HSAVE low-5-bits → HMPR low 5 bits)
;   bytes 32-33  : start offset (section-C address; HSAVE sets HL
;                  from this then reads source through &8000-&BFFF)
;   byte 34      : pages count (HSAVE ANDs with &1F for paging loop)
;   bytes 35-36  : length-mod-16K (HSAVE clears bit 7 of byte 36)
;   bytes 37-47  : exec page/offset + comment; HSAVE doesn't read
;
; No trampoline.  No section-B helper.  HSAVE does its own HMPR
; save/restore (`h.s:136-137, 154-155`) and auto-pages across &C000
; (`c.s:354-369` inside ctas, gated by flag-6 which svblk sets via
; setf6 at `c.s:721`).
;
; Input:
;   OUT_PAGE  - physical page where the OUT buffer's first byte lives
;   OUT_LEN   - total byte count, may exceed 16384
;
; Output: file "OUT" written to current drive.  Errors longjmp to BASIC's
; error path (assembler halts); install a DOSER (&5BC0) handler to catch
; them gracefully (registry item i25 — NOT (hksp); see "Critical caveat").
;
; Clobbers: A, BC, DE, HL, IX (IX = dchan on exit).  IY/SP/LMPR/HMPR
; preserved.
; -----------------------------------------------------------------------
save_out_file_paged:
                ld      hl, name_OUT
                call    fill_uifa            ; type + name + ext at UIFA[0..14]

                ld      a, (OUT_PAGE)        ; physical page of OUT data
                and     &1f                  ; HSAVE masks anyway, but be explicit
                ld      (UIFA + 31), a

                ld      hl, &8000            ; source offset in section-C form
                ld      (UIFA + 32), hl      ; HSAVE reads HL = (hd0d1) = UIFA[32-33]
                                             ; from this — bytes 32-33 are LE.

                ld      hl, (OUT_LEN)        ; total bytes
                ld      a, h                 ; A = pages-count component
                rlca                         ; shift the top 2 bits of length
                rlca                         ; (length high byte) into bits 0-1
                and     3                    ;   so A = pages (length / 16384)
                ld      (UIFA + 34), a       ; bytes/16384

                ld      a, h                 ; length-mod-16K = LE length with
                and     &3f                  ; top 2 bits of H cleared (i.e.
                ld      h, a                 ; H AND 0x3F gives 14-bit remainder)
                ld      (UIFA + 35), hl      ; bytes 35-36 = length mod 16384

                rst     8
                defb    HOOK_HSAVE           ; 132 — longjmps on error
                ret
```

In the in-tree implementation (`save_out_file`, `src/main_loop.asm`):
`OUT_PAGE` is the runtime `OUT_RUN_BASE` — the first page of the
pool-allocated OUT run (`docs/specs/paged-out-design.md`) — and the
pages/remainder split is computed across the 24-bit `OUT_LEN` (a multi-page
run can exceed the snippet's two-`RLCA`-plus-`AND 3` reach). `name_OUT` /
`fill_uifa` / `UIFA` (`&4B00`) live in `src/main_loop.asm` / `sam_io.inc`,
and `HOOK_HSAVE = 132` is defined alongside `HOOK_HLOAD` (per
`samdos/src/b.s:501`).

#### Length encoding (verified against the HSAVE source)

HSAVE pulls `pges1` from `uifa+34` (masked with `&1F`) at `h.s:352-354 /
hconr` and `hd0b1` from `uifa+35-36` (with bit 7 of the high byte cleared) at
`h.s:356-359`. The 16-bit value `bytes = OUT_LEN` decomposes as:

- pages = `bytes >> 14`        → UIFA[34]   (top 2 bits of OUT_LEN's high byte)
- remainder = `bytes & 0x3FFF` → UIFA[35-36] (LE, low 14 bits)

The snippet encodes that with two `RLCA` instructions on the length-high byte
to shift its top 2 bits into bits 0-1, masking with `3` to get the pages
count. Then the same high byte is masked with `&3F` to leave the 14-bit
length-mod-16K in HL for the `ld (UIFA + 35), hl` store. This avoids a 16-bit
division. (A 16-bit `OUT_LEN` caps the output at 64 KB; a larger output would
need a wider counter — tracked as registry item i24.)

#### Why UIFA[31] is the OUT page, not the current HMPR

A caller whose output buffer lives in the SAME physical page as its own code
(e.g. a small in-section-C buffer) can fill UIFA[31] from the current HMPR's
low 5 bits. With the OUT buffer in a DIFFERENT physical page, UIFA[31] must
be the OUT-buffer's page — that single byte is the entire "paged save"
mechanism.

#### Where the OUT data must actually be

UIFA bytes 31-33 together describe `<physical page, section-C offset>` of the
first source byte. The snippet hardcodes offset `&8000` because that's the
start of section C — meaning the OUT data must begin at offset 0 within its
physical page (the data fills `<OUT_PAGE>:&0000..&3FFF`, then continues into
`<OUT_PAGE+1>:&0000..` once HSAVE auto-pages). If the data does NOT start at
the page boundary (e.g. the page is shared with something else), set UIFA
bytes 32-33 to `&8000 + offset_into_page` and shrink OUT_LEN accordingly.

## SAMDOS register clobbering across the file-IO hooks

These facts apply to HSAVE and HLOAD alike (and the other file-IO hooks —
the dispatcher and epilogue are shared). Audit reference:
`docs/notes/sam-stub-audit.md` §"Stack and paging". Concretely the SAMDOS
hook epilogue (`rfhk` at `b.s:475-479`) and PTDOS's restore
(`rom:12970-12975`):

| Register | Preserved across the hook? | Source |
|----------|----------------------------|--------|
| AF | No — HSAVE leaves CY meaningful on name-conflict return | `h.s:147 jr c, hsave1` |
| BC | No — `b.s:443-446` saves caller BC to `hkbc` but does not restore | observed |
| DE | No — same, saved to `hkde` not restored | observed |
| HL | No — saved to `hkhl` not restored | observed |
| IX | **NOT preserved** — `b.s:440` saves caller IX to `svhdr`, but `rfhk` (`b.s:475-479`) does not restore it. After the hook returns, IX = `dchan` (set by `gtixd` inside the hook body). | `b.s:440`, `b.s:475-479`; `c.s:1513` (gtixd sets `IX = dchan`) |
| IY | Preserved (not touched by the dispatcher) — but don't depend on undocumented behaviour; callers that care should save/restore IY around the RST 8. | observed |
| SP | Preserved (PTDOS saves/restores) | `rom:12950-12951, 12970-12975` |
| LMPR | Preserved (PTDOS saves/restores via `IN B,(C); OUT (C),B`) | `rom:12949, 12973` |
| HMPR | Preserved by HSAVE (saves to `port3`, restores). NOT managed by HLOAD — the caller's trampoline owns it. | `h.s:136-137, 154-155` |

So a caller can issue HSAVE without bracketing it with register-save
sequences, as long as it doesn't need any of A/BC/DE/HL/IX after the call.
IY, SP, LMPR, HMPR all survive. IX is clobbered to `dchan` — same for HLOAD
and the other file-IO hooks.

**Critical caveat (from the audit):** HSAVE and HLOAD longjmp on error via
`derr → derr1` (`d.s:430-460`), which restores SP to `(entsp)` and pops into
BASIC's error path. For a `di / halt` top-level loop this means a file-IO
error effectively crashes the program.

To catch this gracefully, install a handler in the **`DOSER` (`&5BC0`)** error
vector — the same one COMET patches (the `&5BC0` mention above; COMET
`comet.asm:1265-1382` `prepare`/`dier`/`sproom`). `DOSER` is a BASIC system
variable (`VAR2+&1C0`), always mapped in section B, so the application can
write it from its own address space with **no paging dance**. ROM PTDOS's
post-hook return path `DOSC`
(`docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt:12977-12980, 13003`)
does `LD HL,(DOSER); INC H; DEC H; JR NZ,DHLJ` → `JP (HL)` after **every** DOS
hook (success or error), with `A` = the error number (0 on success). So a
`DOSER` handler fires on the error longjmp and can convert it into a clean,
diagnosed FAIL. Tracked as registry item **i25**.

> **Do NOT use `(hksp)` for this** — it is the wrong vector. The hook
> dispatcher zeros `(hksp)` on **every** hook entry, *before* the hook body
> runs (`samdos/src/b.s:450-451`), so any value an application writes to
> `(hksp)` is gone by the time `derr` reads it — `derr` always sees 0 for an
> app-initiated error and falls through to the default BASIC-error path.
> `(hksp)` is purely SAMDOS-internal: it is set only by the NMI snapshot-save
> path (`d.s:606`, torn down at `d.s:744-745`) as an internal retry vehicle,
> not an application handler. (See registry item i185.)

## Where the patterns live in this codebase

- `src/trampoline.asm` — the HLOAD trampoline (`trampoline_body` +
  `TRAMPOLINE_DST` copy) and the LMPR-swap runtime-read machinery; its header
  comments carry the deep paging rationale.
- `src/loader.asm` — every boot-time HLOAD call site (ENCTAB, IN, disasm,
  sysreg data, test payloads), all routed through the trampoline.
- `src/main_loop.asm` — `save_out_file`, the HSAVE call site for the paged
  OUT buffer.
- `docs/specs/paged-in-design.md` /
  `docs/specs/paged-out-design.md` — the runtime read/emit
  paths these file-IO calls bracket.
- `docs/notes/sam-stub-audit.md` — SAMDOS hook semantics audit (the broken
  HOFLE/SBYT/CFSM path; cross-bank read/write helpers; stack and paging
  facts).
- `docs/notes/sam-paging.md` — the LMPR/HMPR paging primer.
