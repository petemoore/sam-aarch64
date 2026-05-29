# SAMDOS file-save idiom: research findings

**Status**: design note. No code yet. Captures research output from
2026-05-27 surveying COMET's source, the SAMDOS source, and the
existing READ-IN trampoline implementation, in preparation for the
M6 trampoline-extension PR that will let the assembler emit OUT
files that don't fit in section C's 2 KB window.

## Findings worth noting (read first)

1. **There is no HSAVE trampoline.** Unlike HLOAD, **HSAVE manages
   HMPR by itself**.  At `samdos/src/h.s:136-143` HSAVE reads the
   current HMPR, ORs UIFA byte 31's low 5 bits in, and writes that
   to port 251.  At `h.s:154-155` it restores the original HMPR
   before returning.  Inside the save loop (`samdos/src/c.s:354-369`,
   inside `ctas`) HSAVE auto-increments HMPR every time it crosses
   `&C000`, so multi-page outputs flow across physical pages without
   help from the caller.  The HLOAD-style "save HMPR, set HMPR, RST
   8, restore HMPR" trampoline is **unnecessary for HSAVE**: the
   bytes inside that trampoline are redundant with what HSAVE
   already does internally.

2. **The READ-IN trampoline's HOWTO comment block was over-engineered
   on this point.**  Lines 131-136 of `src/trampoline.asm`
   describe "**HSAVE (hook 132) trampoline.** Mirror
   `trampoline_hload` but with `defb 132` instead of `defb
   HOOK_HLOAD`."  That mirror is not needed.  The HSAVE-side change
   is simply to populate UIFA byte 31 with the **OUT-buffer's
   physical page** (rather than the current HMPR's low 5 bits, as
   `save_out_file` does today at `src/main_loop.asm:2082-2084`)
   and call `RST 8 / DEFB 132`.  No trampoline at the call site.

3. **COMET does NOT have a save-side counterpart.**  COMET hands its
   "O Save objectcode" menu key back to BASIC (`comet.asm:131-133
   JP &0013` to BASIC's KEYS handler, then BASIC executes `SAVE
   "name" CODE …`).  The only RST 8 hook codes COMET issues are 129
   (HGTHD) and 130 (HLOAD) — see `grep -n 'DEFB +13[0-9]'` returning
   only lines 1278 (`DEFB 130`) and 202 + 1330 (`DEFB 129`).
   Likewise `ltran.asm` and `sctran.asm` (the LERM/SC source
   converters that ship with COMET) do all their work in HMPR-paged
   RAM only and never call any save-side hook.  So the search of
   COMET found that the SAM-era idiom for saving large outputs from
   assemblers is to **let BASIC do the SAVE**, not to call HSAVE
   directly.  We follow a different path because we cannot return to
   BASIC: `assembler.asm`'s top-level loop ends in `di / halt`, and
   AUTO has been consumed.

4. **The "open + byte-stream" hooks (`HOFLE`/`SBYT`/`CFSM`) are
   broken in canonical SAMDOS 2** — see `docs/notes/sam-stub-audit.md`
   §"TL;DR — concrete bug list" (bugs #1, #2, #3).  `HSAVE` is the
   only working save-side path.  The pre-built snippet below uses
   HSAVE for that reason.

5. **The runtime READ/WRITE question for the OUT buffer (how to add
   bytes to the OUT page while the assembler runs from section C)
   IS the harder question.**  It's an LMPR-swap problem, identical
   in shape to ENCTAB's runtime-read pattern.  This note covers
   only the *flush trampoline* — how to call HSAVE on a paged OUT
   buffer.  The runtime-access design belongs in the impl spec.

## Background

The SAM-side Z80 assembler currently lives in section C (`&8000-&AFFF`)
with a 2 KB output buffer at `OUT_BUF = &B800` (`src/assembler.asm:33`).
Pass 2 emits bytes into `OUT_BUF`, then `save_out_file`
(`src/main_loop.asm:2079-2093`) populates UIFA byte 31 from the
current HMPR's low 5 bits — i.e. the same physical page where the
assembler is running — and calls `RST 8 / DEFB 132` (HSAVE).

This works for M3/M4/M5 fixtures whose OUT fits in 2 KB.  But the
real spectrum4 outputs are ~21.7 KB (release.bin), which neither
fits in OUT_BUF nor can be emitted into the only 4 KB of slack we
have in section C above the assembler code.  M6 needs to:

- allocate the OUT buffer in a **physical page that the assembler
  is not running from** (page 5 is a good candidate — free per the
  Tech Manual page allocation table, contiguous to page 6 if we
  ever need a second OUT page);
- write to that page during pass-2 emit via an LMPR swap (mirror of
  the ENCTAB read pattern in `src/trampoline.asm:96-117`);
- at end of pass 2, call HSAVE with UIFA byte 31 set to the OUT
  page so SAMDOS reads through section C from the right physical
  page.

This note documents the third step (the HSAVE call) only.  The
LMPR-swap "where the OUT buffer lives during emit" decision is part
of the impl PR's design.

## The HSAVE pattern

Citations: `samdos/src/h.s:132-156` (HSAVE body); `c.s:721-723`
(svblk entry); `c.s:354-369` (ctas's HMPR auto-increment at &C000
boundary).

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

Two facts make this call-site simple:

- **HSAVE itself saves and restores HMPR.**  Lines 136-137 save the
  entry HMPR to `port3` (a SAMDOS bank variable, unaffected by HMPR
  changes); lines 154-155 restore it.  The caller does not need to
  bracket the call.
- **HSAVE auto-pages across `&C000`.**  `svblk → sblok → ctas` at
  `c.s:354-369` watches `(svhl)` (the current source pointer, kept
  in `dchan`'s scratch); whenever H hits `&C0` it resets bit 14 of
  H (rewinds source pointer to `&8000`) and does `IN A,(251); INC A;
  OUT (251),A` — i.e. HMPR := HMPR+1.  Combined with UIFA byte 34's
  page-count, HSAVE can save any contiguous run of pages without
  the caller's involvement.

## Why it works (paging analysis)

When the caller does `RST 8 / DEFB 132`:

1. ROM `ERROR2` (`docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt:12906-12918`)
   reads the DEFB byte from caller's section C via `LD A,(DE)` — the
   caller's HMPR is still in effect at this point.

2. ROM `PTDOS` (`...rom-v3.0:12944-12978`) saves caller's LMPR,
   pages SAMDOS bank into section B (LMPR low 5 bits = DOSFLG),
   switches SP to `&8000`, dispatches via SAMDOS hook.  HMPR is
   **not touched** by PTDOS.

3. SAMDOS hook entry (`samdos/src/b.s:439-470`) saves caller's IX
   to `(svhdr)`, captures other register state, and jumps to
   `hsave`.

4. HSAVE reads `(svhdr)` to find caller's UIFA — but reads it via
   `cmr / defw nrread`, which pages the BASIC sys page into section
   B for the read and then reverts (`docs/notes/sam-stub-audit.md`
   §"Cross-bank reads and writes — `cmr; defw nrread/nrrite`").
   So caller's UIFA at `&4B00` is read regardless of the caller's
   physical layout (provided `&4B00` is in the BASIC sys page, which
   our assembler enforces by convention).

5. HSAVE OUTs the new HMPR derived from UIFA byte 31.  Section C
   now maps to the OUT-buffer's physical page.  The caller's code
   page (where the running assembler lives) is paged out of section
   C — but the CPU's PC is in SAMDOS (section B), so this is fine.
   The caller's stack at the &C100 / section-D area is *also* paged
   out — but SAMDOS has already switched SP to `&8000`, so the
   caller's stack frame isn't touched during the hook.

6. HSAVE reads source bytes from `HL = (hd0d1)` (= UIFA[32..33])
   through section C.  Each page-crossing auto-bumps HMPR.

7. On return, HSAVE restores port3 (= caller's entry HMPR).  PTDOS
   then restores LMPR.  Section C is back to caller's code page,
   section B is back to caller's LMPR-set value, the stack is back
   to `&C100`-area.  `RET` pops PC = address-after-DEFB-132, which
   is in section C — and section C now maps to the right page —
   so execution resumes correctly.

**No HMPR trampoline needed.**  No section-B helper code.  No
static save slot.  The call is just `RST 8 / DEFB 132` after UIFA
setup.

## Why this differs from HLOAD

HLOAD has no equivalent of HSAVE's UIFA-byte-31 → HMPR mechanism.
HLOAD reads the destination address from registers `(hkhl)` /
`(hkbc)` / `(hkde)` (captured at hook entry from caller's HL/BC/DE
— see `samdos/src/b.s:443-446` and `h.s:74-90 dschd`).  It does
NOT touch HMPR.  So if the caller wants HLOAD to write to a
physical page other than the one currently mapped in section C,
the caller must arrange that mapping itself.

That's why the HLOAD trampoline at `src/trampoline.asm:308-331`
exists: to do the HMPR change around the RST 8 from a location
(section B) that's unaffected by the HMPR change.

HSAVE took a different design choice: the source page is encoded
in the UIFA, not in registers, and HSAVE does the HMPR dance
itself.  Lucky for us — we save a trampoline body, a stack-save
slot, and the entire section-B copy machinery for the save path.

## Hook survey

The full list of save-side hooks (`samdos/src/b.s:497-538`):

| Hook | What it does | Trampoline needed? | Verdict |
|------|--------------|--------------------|---------|
| 132 HSAVE | Whole-file save. Reads UIFA[31] → HMPR. Calls `gtixd → ofsm → svhd → svblk → cfsm`. Restores HMPR. (`h.s:132-156`) | No — HSAVE does it internally. | **This is the one to use.** |
| 147 HOFLE | Open new file for byte-streaming. (`h.s:242-246`) | N/A — broken in SAMDOS 2 (audit Bug #1). | Skip. |
| 148 SBYT  | Save next byte through `(IX+rptl..rpth)`. (`c.s:533-551`) | N/A — broken without working HOFLE (audit Bug #2). | Skip. |
| 150 HSVBK | `jp svblk` directly. (`h.s:249`) Reuses `dchan` state set by a prior HSAVE / HOFLE call. Auto-pages HMPR like HSAVE's body does. | No (same logic as HSAVE). | Could be useful for incremental multi-block save with reused FCB. Not needed for M6's "single HSAVE at end of pass 2" model. |
| 149 HWSAD | Write 512-byte sector at (track, sector) from arbitrary HL via `cals`. (`h.s:289+`) | Possibly — but writes raw sectors, not files. | Wrong abstraction. |
| 152 CFSM  | Close file / flush directory. (`c.s:1306-1343`) | N/A — broken externally without HSAVE-set FCB (audit Bug #3). | Skip. |

Only HSAVE is viable.  HSVBK is a curiosity for future use if we
ever want to assemble multiple files in one assembler invocation.

## SAMDOS register clobbering — quick recap

For completeness, the same register-clobber facts that the READ-IN
note relied on for HLOAD also apply to HSAVE.  Audit reference:
`docs/notes/sam-stub-audit.md` §"Stack and paging".  Concretely
the SAMDOS hook epilogue (`rfhk` at `b.s:475-479`) and PTDOS's
restore (`rom:12970-12975`):

| Register | Preserved across HSAVE? | Source |
|----------|------------------------|--------|
| AF | No — HSAVE leaves CY meaningful on name-conflict return | `h.s:147 jr c, hsave1` |
| BC | No — `b.s:443-446` saves caller BC to `hkbc` but does not restore | observed |
| DE | No — same, saved to `hkde` not restored | observed |
| HL | No — saved to `hkhl` not restored | observed |
| IX | **NOT preserved** — `b.s:440` saves caller IX to `svhdr`, but `rfhk` (`b.s:475-479`) does not restore it.  After HSAVE returns, IX = `dchan` (set by `gtixd` inside HSAVE).  See `src/trampoline.asm:65-71`. | `b.s:440`, `b.s:475-479`; `c.s:1513` (gtixd sets `IX = dchan`) |
| IY | Preserved (not touched by dispatcher; we don't depend on this and the READ-IN trampoline doc warns against it).  Callers that care should save/restore IY around the RST 8. | observed |
| SP | Preserved (PTDOS saves/restores) | `rom:12950-12951, 12970-12975` |
| LMPR | Preserved (PTDOS saves/restores via `IN B,(C); OUT (C),B`) | `rom:12949, 12973` |
| HMPR | Preserved (HSAVE saves to `port3`, restores) | `h.s:136-137, 154-155` |

So the M6 caller can issue HSAVE without bracketing it with
register-save sequences, as long as it doesn't need any of A/BC/DE/HL/IX
after the call.  IY, SP, LMPR, HMPR all survive.  IX is clobbered to
`dchan` — same as for HLOAD and the other file-IO hooks.

**Critical caveat from the audit (still applies):** HSAVE longjmps
on error via `derr → derr1` (`d.s:430-460`), which restores SP to
`(entsp)` and pops into BASIC's error path.  For our `di / halt`
top-level loop this means a HSAVE error effectively crashes the
assembler.  The READ-IN side is no better in this regard; the M6
impl spec may want to install an `(hksp)` handler before HSAVE for
graceful error reporting, but that's an orthogonal question and
out of scope here.

## Pre-built code snippet

Drop this into `src/main_loop.asm` (or `src/m6/save.asm` if we
spin out a new module) when the M6 impl PR lands.  It replaces the
existing `save_out_file` at `main_loop.asm:2079-2093`.

```asm
; -----------------------------------------------------------------------
; save_out_file_paged — HSAVE the paged OUT buffer.
;
; Caller contract: the OUT-buffer's contents have been emitted into
; physical page OUT_PAGE.  Bytes 0..OUT_LEN-1 of that page hold the
; emit stream.  OUT_LEN may exceed 16 KB (multi-page case): if so,
; pages OUT_PAGE, OUT_PAGE+1, ... must be contiguous in physical
; memory (i.e. we don't allow OUT to be discontiguous across
; physical pages — the impl PR's page allocator must enforce this).
;
; HSAVE's UIFA layout for code-type files (Tech Manual
; `docs/sam/sam-coupe_tech-man_v3-0.txt:4459-4496`, cross-checked
; against `samdos/src/h.s:132-156`):
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
; Input (assumed established by the impl PR's page allocator):
;   OUT_PAGE  - physical page where the OUT buffer's first byte lives
;   OUT_LEN   - total byte count, may exceed 16384
;
; Output: file "OUT" written to current drive.  Errors longjmp via
; `(hksp)` (BASIC error handler by default — assembler halts).
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


; Globals consumed (declared elsewhere by the impl PR):
;   OUT_PAGE   defb 0           ; impl PR: filled with the allocated page
;                                 (e.g. 5, picked from Tech Manual's
;                                 "free pages 4..12" range; ENCTAB
;                                 already uses page 4 per
;                                 src/trampoline.asm:210).
;   OUT_LEN    defw 0           ; impl PR: 16-bit length is the M6
;                                 ceiling; >64 KB would need a wider
;                                 counter, but spectrum4 release.bin
;                                 is ~22 KB so 16-bit suffices.
;
; Constants reused from the existing code base:
;   name_OUT       - main_loop.asm:2103-2105
;   fill_uifa      - sam_io.inc (already populates UIFA[0..14])
;   HOOK_HSAVE     - defined alongside HOOK_HLOAD (= 132 per
;                    samdos/src/b.s:501)
;   UIFA           - section B at &4B00 (sam_io.inc convention)
```

### Length-encoding fragment (verified against HSAVE source)

HSAVE pulls `pges1` from `uifa+34` (masked with `&1F`) at
`h.s:352-354 / hconr` and `hd0b1` from `uifa+35-36` (with bit 7 of
the high byte cleared) at `h.s:356-359`.  The 16-bit value
`bytes = OUT_LEN` decomposes as:

- pages = `bytes >> 14`        → UIFA[34]   (top 2 bits of OUT_LEN's high byte)
- remainder = `bytes & 0x3FFF` → UIFA[35-36] (LE, low 14 bits)

The snippet above encodes that with two `RLCA` instructions on the
length-high byte to shift its top 2 bits into bits 0-1, masking
with `3` to get the pages count.  Then the same high byte is
masked with `&3F` to leave the 14-bit length-mod-16K in HL for the
`ld (UIFA + 35), hl` store.  This avoids a 16-bit division.

### Why we OUT a page derived from `OUT_PAGE`, not from current HMPR

The existing `save_out_file` at `main_loop.asm:2082-2084` does:

```asm
in      a, (251)
and     &1f
ld      (UIFA + 31), a
```

This works in M3/M4/M5 because the OUT buffer lives in the SAME
physical page as the assembler (it's at `&B800` in section C, which
maps to the assembler's running page).  For M6 the OUT buffer is
in a DIFFERENT page — page 5 (or whatever the allocator picks) —
so we need UIFA[31] = `OUT_PAGE`, not the current HMPR.

### Where the OUT data must actually be

UIFA bytes 31-33 together describe `<physical page, section-C offset>`
of the first source byte.  The snippet hardcodes offset `&8000`
because that's the start of section C.  This means the OUT buffer
must begin at offset 0 within its physical page — i.e. the OUT
data fills `<OUT_PAGE>:&0000..&3FFF`, then continues into
`<OUT_PAGE+1>:&0000..` once HSAVE auto-pages.

If for some reason the OUT data does NOT start at the page
boundary (e.g. the page is shared with something else), set UIFA
bytes 32-33 to `&8000 + offset_into_page` and shrink OUT_LEN
accordingly.

## Open questions deferred to the impl PR

These belong in a separate impl-design note, not here:

1. **Where does the OUT buffer live during pass 2?**  Page 5 is the
   obvious choice (free per Tech Manual, contiguous to page 6 if
   the OUT exceeds 16 KB).  But the assembler needs to **write** to
   that page during emit — via LMPR swap (mirror of ENCTAB's
   read pattern) or some other mechanism.  This is the "runtime
   read/write via section A" question the trampoline HOWTO comment
   raises (`src/trampoline.asm:138-158`).

2. **Three-buffer paging.**  Pass 2 reads IN, reads ENCTAB, writes
   OUT.  Only one page can be LMPR-mapped into section A at a time.
   The impl PR must pick a paging strategy — interleaved (swap LMPR
   per access), phased (swap once per phase), or
   scratch-window-based (cache a slice of one buffer in section C
   to amortise swaps).  This is the harder M6 design question.

3. **Error path.**  HSAVE longjmps on error.  Today the assembler
   crashes.  The impl PR may want to install `(hksp)` for graceful
   handling.  Out of scope for this note.

4. **`OUT_PAGE` allocation policy.**  Static (page 5 always)?
   Driven by an extension of the existing ENCTAB_PAGE constant?
   This is bookkeeping, not architecture.

5. **Multi-output.**  spectrum4 builds multiple `.bin` outputs.  If
   one assembler invocation must emit them all, HSVBK (hook 150)
   becomes interesting — see Hook survey table above.  But the
   current plan is one assembler invocation per output, so HSAVE
   alone suffices.

## Related notes

- `docs/specs/2026-05-27-samdos-load-idiom.md` — the READ-IN
  counterpart.  Same shape; its trampoline is more elaborate than
  ours because HLOAD doesn't manage HMPR for the caller.
- `docs/notes/sam-stub-audit.md` — SAMDOS hook semantics audit.
  HSAVE's calling convention and the broken HOFLE/SBYT/CFSM path
  documented in §"TL;DR — concrete bug list".
- `src/trampoline.asm` — the READ-IN trampoline + LMPR-swap
  machinery.  The "HOW TO EXTEND THIS PATTERN FOR IN AND OUT
  BUFFERS" comment block (lines 119-181) anticipated this note;
  with the present findings, the IN-side answer for that block is
  "extend the existing trampoline" but the OUT-side answer is
  simpler: "no trampoline, just call HSAVE with UIFA[31] set
  correctly."
- `src/main_loop.asm:2079-2093` — the current single-page
  `save_out_file` to be replaced.
