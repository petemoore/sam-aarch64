# Paged-call architecture — design + plan (2026-05-28)

**Status:** read-only design note. No code, no commits. Produced after
the FAIL00 / 8-sysreg post-mortem surfaced structural fragility in
how the assembler binary places off-axis tables. Horizon: M6 strand B
+ Phase 2 prerequisites. The brainstorm doc
(`docs/notes/2026-05-28-memory-layout-brainstorm.md`) sketches WHERE
pages go; this note pins down HOW we call into and read from them.

---

## 0. Why this note exists

`src/m3/sysname.asm:716` holds the (currently 12-entry) `sysreg_table`
inside the assembler binary. Adding the 8 missing `spectrum4` sysregs
(`hcr_el2`, `mair_el1`, `scr_el3`, `spsr_el3`, `tcr_el1`, `ttbr0_el1`,
`ttbr1_el1`, `vbar_el1`) — ~111 B of static data — pushed the
**`BUILD_TESTS` variant** of the binary from `&C10F` to `&C21B`. The
prod variant has ~4 180 B headroom in section C
(`docs/notes/2026-05-28-memory-layout-brainstorm.md:10`); the test
variant has none — its tail spills past `OPVAL_ARRAY` at `&C100`
(`src/m3/assembler.asm:72`) and corrupts pass-1/pass-2 scratch while
running. That's a fragility, not a fix.

The brainstorm doc proposes "even pages 13–29 that aren't used yet …
write it down so casual 'let's stick this on page N' decisions stop"
(`memory-layout-brainstorm.md:117`). This note operationalises that:
it defines the **single mechanism** for placing code in an off-axis
page and calling it from section C with minimal per-site overhead, and
the **simpler parallel mechanism** for placing data in an off-axis
page and reading it from section A.

Once those exist, the 8-sysreg fix is "move `sysreg_table` to page
13", not "find +111 B of section-C code budget".

---

## 1. Existing-patterns survey

### 1.1 SAM ROM v3.0 — RST vectors

`docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt:173-196` and
the actual byte layout (lines 211-294) inventory the SAM's RST
vectors. Critical ones for our purposes:

| Vector | Address | What it does | Inline payload? |
|---|---|---|---|
| RST 0  | `&0000` | `DI; JP MINITH` — power-on reset (`...:211-212`) | none — single jump |
| RST 8  | `&0008` | `NOP; EXX; JP ERROR2`. SAMDOS hijacks this for hook dispatch (`...:219-223`) | **1 byte** error/hook number — see below |
| RST 10 | `&0010` | `JP RST102`. Print A (`...:230-232`) | none |
| RST 18 | `&0018` | `JP GETCHAR1`. Get char from CHAD (`...:241-245`) | none |
| RST 20 | `&0020` | `JP NEXTCHAR1`. Advance CHAD then read (`...:249-254`) | none |
| RST 28 | `&0028` | `JP FPCP2`. **Floating-point calculator with inline operation byte string** (`...:256`) | **N bytes** — calculator program |
| **RST 30** | `&0030` | `JP RST30L2`. **"CALL or JP to upper ROM" — 1-byte RST + 2-byte inline address.** (`...:264, 581-614`) | **2 bytes** — target ROM1 address |
| RST 38 | `&0038` | Maskable interrupt handler (`...:273-294`) | none |

**RST 30H is the SAM-ROM-blessed paged-call primitive.** Annotation
at line 192-194:

> 0030 (RST 30H) - CALL or JP to the upper ROM. The address to go to
> follows the RST 30H instruction. 8000 is subtracted from it to give
> a JP - otherwise, you get a CALL.

The handler at `RST30L2` (`&01CF`,
`...rom-v3.0_annotated-disassembly.txt:581-614`):

```
01CF E3       RST30L2:  EX (SP),HL          ; HL = caller's return address (= inline payload)
01D0 F5                 PUSH AF
01D1 7C                 LD A,H
01D2 FE40               CP 40H
01D4 301C               JR NC,RST30L4       ; user-mode caller → indirect via RST30V
01D6 ED43475B           LD (BCSTORE),BC
01DA 4E                 LD C,(HL)           ; C = lo(target)
01DB 23                 INC HL
01DC 46                 LD B,(HL)           ; B = hi(target)
01DD 23                 INC HL              ; HL = caller's return after the 2-byte word
01DE CB78               BIT 7,B
01E0 2006               JR NZ,RST30L3       ; bit 7 set → 'CALL ROM1'
01E2 CBF8               SET 7,B             ; bit 7 was clear → 'JP ROM1' (throw return)
01E4 F1                 POP AF
01E5 E1                 POP HL              ; junk one return address
01E6 1802               JR R1ONCLBC
01E8 F1     RST30L3:    POP AF
01E9 E3                 EX (SP),HL          ; restore caller's HL, push adjusted return
01EA 08     R1ONCLBC:   EX AF,AF'
01EB DBFA               IN A,(250)          ; save LMPR
01ED F5                 PUSH AF
01EE F640               OR 40H              ; ROM1 ON (bit 6)
01F0 181B               JR R1OFON           ; OUT (250),A / CALL LDBCJP / restore
```

**Shape**: 1-byte RST + 2-byte inline target → handler stores caller
BC, reads the 2-byte target from the inline payload, advances the
return address past the payload, flips LMPR bit 6 (ROM1 on), CALLs
the target, and on return restores LMPR. **Exactly Pete's call-site
shape, except**:

1. It only switches ROM1 on/off (LMPR bit 6), not arbitrary RAM
   pages. No support for "page X into section A".
2. The "1-byte vs 3-byte total payload" choice is encoded in the
   target's top bit (`CALL` = bit 7 set; `JP` = bit 7 clear, "throw
   away one return"), not in a separate inline byte.
3. No HMPR / section-C handling.

The ROM uses it ~80 times (every `RST 30H` in `...:439-` and
following) — well-trodden, well-understood. The pattern itself is
sound; the implementation is just specialised to ROM0↔ROM1.

### 1.2 SAM ROM v3.0 — paged-data routines

`FARLDIR` (`&2A5E`,
`...rom-v3.0_annotated-disassembly.txt:9998-10030`) and `FARLDDR`
(`&2A4E`, `...:9978-9991`) implement "LDIR/LDDR between 19-bit far
addresses": move (PAGCOUNT × 16 K + MODCOUNT) bytes from `AHL` to
`CDE`, paging unchanged on exit. The implementation pages source via
HMPR, destination via TEMPB2/TEMPW1, copies via a 256-byte buffer at
`BUFF256` (`&4C00`). This is the SAM's "paged memcpy" — used by
string concatenation, `FARLDIR` (line 460 of the disassembly), and
similar bulk ops.

For our case (per-instruction sysreg-name lookup, per-record .tbn
read) **FARLDIR is too heavyweight**: it brackets with `R1OSR` (saves
both ports), buffers through a 256-byte staging area, and clobbers
DE/HL/AF/AF'/BC. It's the right shape for "copy this 10 KB block
across pages once" (already what the M6 PR 2 reader-payload copy
loop does, modulo our own LDIR variant at
`src/m3/reader.asm:218-237`), not "read this 1 byte from page 13".

### 1.3 SAM ROM — register-save bracket helpers

`R1OSR` / `POPOUT` at `&3C49` / `&3C42`
(`docs/notes/sam-paging.md:188-214` quoting
`...rom-v3.0_annotated-disassembly.txt:13908-13922`) saves both LMPR
and HMPR onto the stack on entry, runs the body via `JP (IY)`, and
restores via `POP AF; OUT (250),A; POP AF; OUT (251),A; RET`.
`R1OFFCLBC` at `&0207` (`...:639-652`) saves LMPR only, calls a
routine via BC, restores LMPR on return. Both routines run ROM1 off
during the body. Neither pages arbitrary RAM into A or B; both
preserve sections C+D (HMPR) only by side effect (POPOUT) or not at
all (R1OFFCLBC). Useful idioms; not directly reusable for the
data-on-page-13 case.

### 1.4 SAMDOS hook dispatch — `RST 8 / DEFB hook_no`

`~/git/samdos/src/b.s:497-501` (cited in `m6_strand_a_complete.md:55`
already):

```
samhk:        defw init      ; 128
              defw hgthd     ; 129
              defw hload     ; 130
              defw ...       ; 131
              defw hsave     ; 132
              ...
```

Call site: `rst 8; defb 130` (HLOAD), with file params in UIFA. The
ROM's RST 8 handler (`&0008`, `...:219-223`) dispatches via PTDOS
(`...:12944-12978`, quoted in `sam-paging.md:601-632`). PTDOS pages
SAMDOS into section B by setting LMPR = DOSFLG-1, switches SP to
`&8000` (section C top — outside the page that just moved), pushes
the saved LMPR + saved SP, jumps to the hook handler, and on return
restores LMPR and SP via `OUT (C),B; LD SP,HL`. This is the SAM's
production "page in a specific RAM page, call a routine there, and
restore" mechanism — exactly the abstraction we want, but specialised
to SAMDOS. PTDOS's table-of-hook-numbers indirection (`samhk[i]`
selects which routine in the paged-in code runs) is the part our
mechanism diverges from: we don't want a 256-entry dispatch table per
target page; we want each call site to name its own target address
inline.

### 1.5 ZX Spectrum 128K — `RST 28H` ROM-switching primitive

**This is the closest precedent to Pete's proposed shape.**

`/tmp/spec128_rom0.asm:740-757` (downloaded from
`https://github.com/ZXSpectrumVault/rom-disassemblies/blob/master/Spectrum%20128K/Spectrum128_ROM0.asm`):

```
; -------------------------------
; RST $28 - Call Routine in ROM 1
; -------------------------------
; RST 28 calls a routine in ROM 1 (or alternatively a routine in RAM while
; ROM 1 is paged in). Call as follows: RST 28 / DEFW address.

L0028:  EX   (SP),HL      ; Get the address after the RST $28 into HL,
                          ; saving HL on the stack.
        PUSH AF           ; Save the AF registers.
        LD   A,(HL)       ; Fetch the first address byte.
        INC  HL           ; Point HL to the byte after
        INC  HL           ; the required address.
        LD   (RETADDR),HL ; $5B5A. Store this in RETADDR.
        DEC  HL           ; (There is no RST $30)
        LD   H,(HL)       ; Fetch the second address byte.
        LD   L,A          ; HL=Subroutine to call.
        POP  AF           ; Restore AF.
        JP   L005C        ; Jump ahead to continue.
```

Continuation at `L005C` (`/tmp/spec128_rom0.asm:807-818`):

```
; Continuation from routine at $0028 (ROM 0).
L005C:  LD   (TARGET),HL  ; $5B58. Save the address in ROM 0 to call.
        LD   HL,YOUNGER   ; $5B14. HL='Return to ROM 0' routine held in RAM.
        EX   (SP),HL      ; Stack HL.
        PUSH HL           ; Save previous stack address.
        LD   HL,(TARGET)  ; $5B58. HL=Retrieve address to call.
        EX   (SP),HL      ; Stack HL.
        JP   SWAP         ; $5B00. Switch to other ROM (ROM 1) and return to address to call.
```

Mechanism walkthrough:

1. `RST 28` pushes the return address (= pointer to the inline 2-byte
   address that follows the `RST 28` instruction).
2. Handler reads the 2-byte target off the stack via `EX (SP),HL`,
   advances HL past it, and stores the adjusted return in
   `RETADDR (&5B5A)`.
3. Pushes `YOUNGER` (`&5B14`) — a "switch back and RET to RETADDR"
   stub kept in RAM at the old ZX Printer buffer — as the new return
   address.
4. `JP SWAP` — toggles the ROM-select bit in port `&7FFD` and
   "returns" (via the stacked target) to the ROM1 routine. ROM1 runs;
   when it RETs, control lands in `YOUNGER` (in RAM), which toggles
   ROM-select back and RETs to RETADDR — i.e. past the inline payload
   in the caller.

**Why the SWAP / YOUNGER routines live in RAM** (lines 822-866):
during the `OUT (C),A` that toggles ROM-select, the CPU's next
instruction fetch is from the same address — and the byte there is in
the *other* ROM. If SWAP were in ROM0, the instruction after `OUT`
would come from ROM1. Putting SWAP in RAM (the old ZX Printer buffer
at `&5B00-&5B6F`, copied there at boot) keeps the fetch stable across
the bank switch. **This is the direct analogue of our trampoline
living in section B at `&7E00` rather than in section C**
(`src/m3/trampoline.asm:18-44`); the structural problem is identical.

Per-call cost: 4 bytes (`RST 28` + 2-byte target + the standard
machine-code RET in ROM1 that pairs with YOUNGER). Each ROM1 routine
just RETs normally — no special handling — because YOUNGER is on the
stack as the synthetic return.

Notable: the 128K *also* has `SWAP` paths invoked from `RST 38`
(maskable interrupt — `/tmp/spec128_rom0.asm:771-781`) for the same
reason: the interrupt vector lives in ROM0 but the maskable interrupt
handler is in ROM1. The 128K's interrupt handler swaps to ROM1, runs
the handler, swaps back. **Implication for our design**: if we adopt
a similar mechanism and want music IRQ to fire while assembly is
paged into LMPR_ENCTAB, the interrupt vector at `&0038` is ROM0 and
will be executed *as garbage* under LMPR_ENCTAB. We currently DI
across the entire assemble window (`trampoline.asm:441` and similar);
the editor / music story has to address this, but it's out of scope
here.

### 1.6 COMET — `getset` / `restore` bracket helpers

`reference/comet-decoded/comet.asm:3161-3178`:

```
getset:        DI
               POP  HL              ; HL = caller's return address
               LD   (sproom),SP     ; save caller's SP
               LD   SP,chartabel    ; switch to fixed scratch stack
               LD   B,A             ; B = requested page number
               IN   A,(250)
               PUSH AF              ; save current LMPR onto scratch stack
               LD   A,B
               OR   32              ; set RAM0 bit (bit 5)
               OUT  (250),A         ; LMPR := target page | &20 — section A = target page
               LD   A,B
jumphl:        JP   (HL)            ; "return" to caller with target page mapped

restore:       POP  HL              ; HL = caller's return address
               POP  AF              ; AF = saved LMPR (from earlier PUSH AF)
               LD   SP,(sproom)     ; restore caller's SP
               OUT  (250),A         ; LMPR restored
               EI
               JP   (HL)
```

Caller pattern: `LD A,page / CALL getset / ...code that reads from
section A under LMPR=page|&20... / CALL restore`.

**Cost per call site**: `LD A,n` (2 B) + `CALL getset` (3 B) + body
(arbitrary) + `CALL restore` (3 B) = 8 B + body. **Significant**:
this is *not* an inline-payload pattern — it's a paired bracket. The
body executes in the caller's context (the caller resumes via
`JP (HL)`); only the LMPR and SP are switched.

Compared to Pete's RST + inline shape (4 B per call site, no
matching "restore" because the handler itself returns and the bracket
is internal): COMET's pattern is 4 B more per site, **but** it
trivially handles multi-statement bodies (any code between
`CALL getset` and `CALL restore` runs with section A mapped). Pete's
shape needs the target to be a single routine; multi-statement work
in the paged page becomes a separate function. Both patterns work;
they make different trade-offs about who composes what.

`sproom` (`comet.asm:4876`) is COMET's two-byte SP-save slot,
analogous to our `SP_SAVE` at `TRAMPOLINE_DST + 33`
(`src/m3/trampoline.asm:297`).

Other COMET sites that use the same pattern: `comet.asm:2242, 2700,
2705, 2732, 2746, 3022, 3051, 3590, 3685, 3805, 3928, 4112, 4141,
4354` — i.e. roughly 15 paged-call sites across the assembler. Most
match getset/restore symmetrically; a handful do `getset` once and
keep section A mapped across a longer stretch.

### 1.7 trinload — page-aware bulk writes only

`~/git/trinload/trinload.asm:181-188` writes a UDP-packet payload
into a target page via `out (HMPR), A; ... call ROM_LDIR`. This is a
single-page program plus paged data destinations; it doesn't need a
paged-call mechanism at all. Sanity-check finding: not every
multi-page SAM program needs the abstraction this note proposes —
trinload gets away without it because it's effectively one-shot.

### 1.8 SAM corpus spot-check

Skipped under the read-only-research constraint — the four precedents
above (SAM ROM RST 30H, 128K RST 28H, COMET getset, SAMDOS RST 8) are
sufficient to recommend a design without dispatching another agent.
Pete's brainstorm spec accepts a 1-2-game spot-check is "mostly a
sanity check"; the four sources above ARE the SAM community standard
practice for paged calls and they converge on the same shape.

### 1.9 What our codebase already does

`src/m3/trampoline.asm` lines 362-396 implement a one-shot, hard-coded
trampoline that brackets a SAMDOS HLOAD hook. It's *not* a generic
paged-call mechanism; it's an HLOAD-specific bracket that lives in
section B and is invoked via plain `CALL TRAMPOLINE_DST`. The body
saves HMPR, sets HMPR = target, RSTs the hook, restores HMPR. To
extend it to "arbitrary paged routine" would mean: parameterising the
RST/HOOK by a register, removing the hook-specific stack-switch (or
keeping it conditionally), and adding LMPR support. Practically, it
becomes a different routine; the existing one stays as the HLOAD
trampoline.

`src/m3/reader.asm:218-237`, `src/m3/encoder.asm:439-498`,
`src/m3/main_loop.asm:218-265` implement **per-byte LMPR bracketing
inline** at the call sites — 8 B of bracket per record read, 9 B of
bracket per high-zone emit, etc. This is the current state of the
art: explicit `in a,(250); ld (save),a; ld a,target; out (250),a;
...work...; ld a,(save); out (250),a`. **It works but explodes code
size as we add call sites.** The 8-sysreg fix would have added one
more such bracket pair per call to `sysreg_lookup` — i.e. a fresh
explosion every milestone.

---

## 2. Mechanism choice + reasoning

### 2.1 Three options on the table

**(a) Reuse a SAM ROM routine.** *Rejected.* RST 30H toggles
LMPR bit 6 only (ROM1); it doesn't page arbitrary RAM. FARLDIR is
heavyweight and copy-oriented, not call-oriented. R1OSR/POPOUT
brackets the call but doesn't *do* the paging — the caller still has
to issue OUTs. None of these solves the per-site overhead problem.

**(b) Install our own RST handler.** Specifically RST 38H or one of
RST 10/18/20 — see §2.3 for the vector choice. The handler is **3 B
inline payload after the RST**: `addr_lo`, `addr_hi`, `lmpr_value`.
Per site: 4 B vs `CALL nnnn`'s 3 B = **+1 B per site**. Matches
Pete's brainstorm exactly.

**(c) Section-B trampoline helper called via plain `CALL`.** Call
site: `CALL paged_call / DEFW addr / DEFB lmpr` = 6 B per site (+3 B
vs CALL nnnn). Doesn't take over any RST vector or section A; lives
entirely in section B alongside the existing `enctab_trampoline_setup`
machinery. Trade 2 B per site for not needing the RST-vector
infrastructure work.

### 2.2 Recommendation: **(c) section-B `paged_call` helper.** Defer
(b) as a future optimisation if call-site density ever crosses ~200
sites.

**Reasoning** (steelmanning before deciding):

(b) is tempting because it matches Pete's spec exactly and saves 2 B
per site. With ~30 anticipated call sites in M6 strand B + Phase 2
(disasm aux, sysreg lookup, F1 prose access, simulator step), 2 B ×
30 = 60 B saved. That's roughly one sysreg entry. Not transformative.

(b) requires us to take over an RST vector at boot. Our boot already
starts at `&8000` (`src/m3/assembler.asm:122`) after BASIC's
`CALL 32768`, with ROM0 still in section A. To install a handler at
e.g. `&0038`, we'd have to either:
- Page out ROM0 (LMPR bit 5 = 1) for the lifetime of the program,
  and put our RST handler at the correct address in section A. Cost:
  ROM0 routines (RST 8 for SAMDOS, RST 10/18/20/28/30 for whatever
  BASIC functions we still want) all stop working unless we
  reimplement them. **The HLOAD trampoline depends on RST 8 still
  working** (`trampoline.asm:377`); breaking RST 8 breaks file I/O.
- Or, keep ROM0 paged in, find some unused RST vector, and rely on
  *that one alone* being free for us. RST 38 is interrupt — usable
  *if* interrupts stay DI'd, which they currently do
  (`trampoline.asm:441`, `assembler.asm:170`). But the editor / music
  story wants interrupts on; we'd then collide.

(c) avoids all of this. The helper lives in section B at e.g.
`TRAMPOLINE_DST + 64` (= `&7E40`, clear of the HLOAD trampoline body
at `&7E00..&7E20`, HMPR_SAVE/SP_SAVE at `+32..+34`, and far below the
TRAMP_SAFE_SP slot at `&7F00`). Call sites use plain `CALL` — no
fiddly RST vector negotiation, no risk of breaking SAMDOS hooks, no
need to manage ROM0 paging just to install our entry point. The
+2 B per site cost is real but bounded; the structural simplicity is
permanent.

**Crucial property both options share** and (b)/(c) preserve: the
handler lives in section B (LMPR+1 page = page 1 in BASIC-default
mapping; "always BASIC sys page, untouched by our HMPR changes",
`trampoline.asm:36-44`). The handler instruction stream stays stable
across the LMPR change it performs, the same way the HLOAD trampoline
does. This is the key structural insight from §1.5's "SWAP lives in
RAM, not in either ROM" — exactly mirrored.

**Pete might disagree** and prefer (b) for the +1 B saving and
because his intuition was "RST n". I'd push back: the section-B
helper is *the same trampoline pattern we already use and trust*,
extended generically. Pulling in an RST vector is novel mechanism on
top of the existing trampoline; novel mechanism is the thing we want
less of, not more, when the existing one works.

If Pete still wants (b): the answer is to land (c) FIRST (because the
2 B/site bookkeeping is cheap enough that we don't pay materially
during M6 strand B / Phase 2), and re-evaluate after enough call
sites exist to put a real number on the saving. Today the call-site
inventory is ~5 (sysreg lookup × 3 paths, sysname pstate lookup, dc
lookup, tlbi lookup). 5 sites × 2 B = 10 B. Negligible.

### 2.3 If we ever do (b) anyway: which RST vector?

For the file record, if a future milestone wants the +1 B saving:

- **RST 8 — taken** (SAMDOS hook). Untouchable.
- **RST 10 — `JP RST102` (PRINT A)**
  (`...rom-v3.0_annotated-disassembly.txt:230-232`). We never use it
  (we're a batch program with `di; halt` exit). Available, but only
  if ROM0 stays in section A — at which point the `RST 10` instruction
  in our code at e.g. `&8123` is fetched from section C, dispatches
  to ROM0's `&0010`, which jumps to `RST102` in ROM0. We'd need to
  install our handler... no, wait — we can't, because the bytes at
  `&0010` are ROM0. **RST vectors are only redefinable if ROM0 is
  paged out of section A** — same problem as RST 38.
- **RST 18, 20, 28, 30, 38** — same constraint. All require ROM0
  paged out, breaking RST 8.

Conclusion: **option (b) requires fundamentally restructuring the
boot path** (e.g., copy SAMDOS PTDOS into our own page-1 trampoline,
then page ROM0 out permanently, then install our RSTs in low memory).
That's a separate, big project. Not in scope for the "8 sysregs
land somewhere" goal. Section-B trampoline (option c) ships now.

### 2.4 Memory-layout impact

Option (c) places ~40-60 B of new code at the existing trampoline
home in section B (`TRAMPOLINE_DST = &7E00`, currently consumes
~30 B body + 3 B static save). The trampoline page is page 1 (BASIC
sys page) under the default LMPR; the body is LMPR-stable across the
HMPR changes the body itself performs. The new helper's body is
LMPR-stable across the *LMPR* changes it performs because the body
lives in section B and the helper changes LMPR's low 5 bits — i.e.
section A's page — *not* section B's page. (Specifically: section B
= LMPR low5 + 1. If our helper sets LMPR low5 = N, section B is
LMPR-modified to be page N+1 — but the helper code at e.g. `&7E40`
is still being fetched from page 1 because **the LMPR change just
happened in the previous instruction**, and the next fetch comes
from wherever LMPR-low5+1 says... wait, this needs a deeper look —
see §3.2 for the correctness argument.)

No new pages consumed by the mechanism itself. The 8-sysreg case
consumes one new page (page 13 in the brainstorm; alternatively a
"data-tables" page anywhere unused).

---

## 3. Handler semantics — exact spec

This section pins down the section-B `paged_call` helper rigorously.

### 3.1 Call site

```
    call    paged_call
    defw    target_addr     ; 2 bytes — addr in section A under target page
    defb    target_lmpr     ; 1 byte  — LMPR value while target executes
                            ;   (= &20 | low5; RAM0 bit + low 5 = page #)
    ; execution resumes here after target returns
```

Total: 6 bytes per call site. The target routine is a normal Z80
routine ending in `RET`.

### 3.2 The section-B-fetch invariant — why LMPR is the wrong port

A first cut had the handler do `out (250), a` to map the target page
into section A, then continue executing in section B. **This is
broken**: writing LMPR changes both section A's page (low 5 bits)
AND section B's page (low 5 bits + 1, per `sam-paging.md:88-91`).
After the `OUT`, the next instruction fetch from `&7E41` (or
wherever the handler lives) reads from the NEW section-B page =
target_low5+1 — i.e. some other page entirely, containing garbage.

There's no clever workaround: a trailer in section B can't survive
the LMPR change either (same problem on the return path), and a
trailer in section A would have to be byte-identical at the same
offset in every target page (absurd). COMET handles this by making
`getset` and `restore` two separately-called helpers with
caller-managed bracketing (§1.6) — but each `CALL` itself executes
during a window where section A is the target page, so COMET's
restore helper *must* live in section B because section A is busy.
COMET's getset/restore work because COMET's call sites use explicit
brackets and accept the multi-instruction bracket cost. The
single-shot "1 RST + inline" shape is incompatible with LMPR
swapping unless the handler is in a page that's stable across the
swap — which on the SAM means **section B (LMPR+1)**, but only if
the port being changed is *not* LMPR.

The fix is structural: change **HMPR** instead. HMPR controls
sections C+D; section B is LMPR-managed and stays stable across HMPR
changes. This is exactly why the existing HLOAD trampoline at
`trampoline.asm:362-396` is correct: it changes HMPR while running
from section B. We generalise that.

### 3.3 The final spec — paged_call via HMPR, in section B

The HLOAD trampoline at `trampoline.asm:362-396` already does this
correctly for ONE specific target (RST 8 / DEFB 130). The post-OUT
section-B fetch problem doesn't bite it because:

1. The `out (251), a` changes HMPR (section C+D), NOT LMPR. Section
   B's page is unaffected.
2. The trampoline body fetches its post-OUT instructions from
   section B at `TRAMPOLINE_DST + N`, which is page 1 (BASIC sys
   page) — and stays page 1 across the HMPR change.

**The HLOAD trampoline works because it changes the OTHER port from
the one that controls section B.**

For our paged-call mechanism to work with the same structural
guarantee, **it must change HMPR (not LMPR)** to swap the target
page in. Then the target runs from **section C** (`&8000-&BFFF`),
not section A. The trampoline body in section B is stable across
HMPR changes.

Revised call-site shape:

```
    call    paged_call
    defw    target_addr_in_C    ; target's address in section C (&8000-&BFFF)
    defb    target_hmpr         ; HMPR value while target runs
    ; resume here after target's RET
```

Handler (this time correct):

```
paged_call:           ; lives in section B at TRAMPOLINE_DST + N
    di
    pop     hl                  ; HL → inline payload
    in      a, (251)            ; A = current HMPR
    ld      (paged_call_hmpr_save), a
    ld      (paged_call_sp_save), sp
    ld      sp, paged_call_safe_sp

    ld      e, (hl)
    inc     hl
    ld      d, (hl)
    inc     hl
    ld      a, (hl)             ; A = target HMPR
    inc     hl                  ; HL = post-payload return

    push    hl                  ; real return after the target
    ld      hl, paged_call_trailer
    push    hl                  ; trailer pops first (target's RET lands here)
    push    de                  ; target — final RET jumps to it

    out     (251), a            ; HMPR := target → section C = target page
    ret                         ; → DE = target_addr_in_C

paged_call_trailer:           ; in section B, fetched stably across HMPR change
    ld      a, (paged_call_hmpr_save)
    out     (251), a            ; restore caller's HMPR
    ld      sp, (paged_call_sp_save)
    ; NB: don't EI — caller chose DI state; we restore it via the
    ; existing convention (M3 runs DI throughout). If editor / Phase 2
    ; wants EI, that's a caller-side bracket.
    ret                         ; → caller's post-payload return
```

**This handler is structurally identical to the HLOAD trampoline at
`trampoline.asm:362-396`**, generalised: any (target_addr, target_hmpr)
instead of (RST 8, fixed HOOK_HLOAD byte). Per-site cost = 6 bytes (=
`CALL paged_call` + DEFW + DEFB) vs `CALL nnnn`'s 3 bytes = **+3 B
per site**.

Note: 3 B not 1 B, because we're not taking the RST shortcut. Pete's
+1 B framing assumed an RST-based mechanism; section-B trampoline
loses that 2 B but gains the structural correctness.

### 3.4 Properties

**Register preservation**: handler clobbers A, HL, DE; preserves BC,
IX, IY (not touched). Per-call ABI: same as a plain `CALL` to a
routine that itself clobbers A/HL/DE (most assembler routines
already do). Callers of the existing HLOAD trampoline already pass
arguments in B, C, DE, IX (`trampoline.asm:346-352`); paged_call's
caller would pass target-args in BC/IX/IY only.

**Stack consumption**: 6 bytes (3 PUSHs at handler entry: post-payload
return, trailer, target). The scratch stack at `paged_call_safe_sp`
needs ~64 B headroom for the target to use during its body. Reuse
the existing `TRAMP_SAFE_SP = &7F00` (`trampoline.asm:305`) — already
sized for HLOAD's much deeper PTDOS dispatch.

**Interrupt safety**: handler DIs on entry; trailer leaves DI. If
the caller had EI set, they re-enable it after `CALL paged_call`
returns (matches today's HLOAD-trampoline contract,
`trampoline.asm:392-394`). The 50 Hz music IRQ design has to wrap
its own bracket; outside scope.

**Re-entrance**: target runs in section C; if the target itself
contains a `CALL paged_call`, the inner call's `paged_call_hmpr_save`
overwrites the outer's. **Not re-entrant.** Mitigation: target
routines must not paged-call. For sysreg-table lookup this is
trivially fine — the table is leaf-only. For deeper Phase 2 use, a
small stack of saves (push HMPR before each call instead of static)
is a future change. Document the constraint in the helper's header
comment.

**Interaction with SAMDOS RST 8**: SAMDOS hooks RST 8 → PTDOS → page
SAMDOS into section B (`sam-paging.md:601-632`). PTDOS *saves LMPR*
(not HMPR) before its swap. Our paged_call changes HMPR. The two
ports are orthogonal — calling `paged_call` and then later doing a
SAMDOS hook (or vice versa) does not interfere, provided the inner
call's bracket fully completes before the outer's body resumes.
**This is the same guarantee the HLOAD trampoline already provides**.

**Interaction with ENCTAB / OUT / IN trampolines**: the existing
LMPR=LMPR_ENCTAB window controls section A (page 4 = ENCTAB). Our
paged_call changes HMPR. Independent. If we want to read the
sysreg-table page WHILE LMPR_ENCTAB is live (i.e. during sysreg
lookup in the encoder window), it works: section C gets the sysreg
page; section A still has ENCTAB; section B still has page 5 (OUT
low zone). All three buffers concurrently visible. **This is the
killer property** — paged-data via HMPR composes cleanly with the
existing LMPR-based ENCTAB/OUT/IN mapping.

---

## 4. Paged-data — generalisation

The sysreg-table case is **read-only static data**, not a callable
routine. The full `paged_call` mechanism is overkill for "load one
byte from page 13".

### 4.1 The simpler primitive

For paged-data access, define two helpers:

```
paged_read_byte:   ; A := (target_page : target_offset)
    ; in:  C = target_page (low 5 + RAM0 if page > 0... actually
    ;          C = target HMPR value, e.g. &2D for page 13)
    ;      HL = target offset in section-C form (&8000..&BFFF)
    ; out: A = byte
    ; clobbers: A only — HL/BC/DE preserved
    di
    in      a, (251)
    push    af                  ; (a) save HMPR; section B = page 1 stable
    ld      a, c
    out     (251), a
    ld      a, (hl)             ; read via section C
    pop     bc                  ; (b) recover original HMPR into B
    push    af                  ; preserve A across out
    ld      a, b
    out     (251), a
    pop     af                  ; A = byte
    ret
```

Wait — `push af` and `pop af` cross the HMPR change; they hit section
D (= HMPR+1). For SP in section D (= &C100), the push goes to one
page, the pop reads from another. **Same SP-collision problem the
HLOAD trampoline already solved.**

Fix: use the same SP-switch into section-B-stable scratch:

```
paged_read_byte:
    ; in:  C = target HMPR value
    ;      HL = target offset in section-C form (&8000..&BFFF)
    ; out: A = byte
    di
    ld      (paged_data_sp_save), sp
    ld      sp, paged_data_safe_sp
    in      a, (251)
    push    af                  ; section B SP — stable across HMPR change
    ld      a, c
    out     (251), a
    ld      a, (hl)             ; A = byte
    pop     bc                  ; B = original HMPR; clobbers B
    ld      c, a                ; preserve byte in C
    ld      a, b
    out     (251), a
    ld      a, c
    ld      sp, (paged_data_sp_save)
    ret
```

10 instructions, ~25 T-states overhead per byte read. For sysreg
lookup (~6 bytes per match attempt), that's 150 T-states per attempt
× say 12 attempts = ~2 ms per sysreg-bearing instruction. Negligible.

### 4.2 Smarter bulk reads — recommended

For 5-byte sysreg-table entry copies (the common access pattern,
`sysname.asm:716`), bracket once around the whole copy loop:

```
    ld      a, C                  ; C = target HMPR
    call    paged_data_map        ; HMPR := target, save old
    ldir                          ; or whatever loop reads the page
    call    paged_data_unmap      ; HMPR := saved old
```

This is exactly the COMET `getset` / `restore` pattern from §1.6,
specialised to HMPR. Per-site cost ~10 B for the bracket but you
only pay it once per *block read*, not once per byte. The 5-byte
sysreg copy: 10 B bracket + 5 B `ldir`-style copy = 15 B per use vs
~50 B for per-byte bracketing. **This is the right shape for paged
data.**

### 4.3 The generalisation — paged_call is paged_map + call + paged_unmap

The single-shot `paged_call` helper (§3.3) is literally just
`paged_data_map` + a call to the target via the trailer + an inline
`paged_data_unmap`. **Both should be implemented in one PR**, with
the data-bracket helpers being the primary primitives and
`paged_call` being a thin convenience wrapper.

For PR sizing this means PR 1 = "paged_data_map / paged_data_unmap +
the section-B paged_call wrapper + ONE test", which is a coherent
unit.

### 4.4 ENCTAB-style LMPR paged data (existing)

The existing ENCTAB pattern (`trampoline.asm:440-476`) is the LMPR
analogue of the HMPR-based `paged_data_map`/`paged_data_unmap`. We
should generalise the naming:

- `lmpr_swap_in(page) / lmpr_swap_out` — already exists as
  `enctab_map_in / enctab_map_out`. Rename if used for non-ENCTAB
  data; keep specialised names for ENCTAB if not.
- `hmpr_swap_in(page) / hmpr_swap_out` — new helpers per §4.2.

The brainstorm doc's page-axis allocation
(`memory-layout-brainstorm.md:43-62`) already separates "LMPR pages"
(ENCTAB, OUT-low-zone) from "HMPR pages" (TFTP buffers, music, F1
prose, sim state). The two-helper family makes this concrete.

---

## 5. Memory-layout impact

The brainstorm doc's page allocation
(`memory-layout-brainstorm.md:43-62`) was written assuming each page
gets either an LMPR-bracket or an HMPR-bracket entry point. This
note's recommended mechanism (section-B trampoline for paged_call;
HMPR-based paged_data helpers) doesn't change those assignments — but
it pins down which port each page is accessed via:

| Page(s) | Role | Access port | Constraint added |
|---|---|---|---|
| 4 | ENCTAB | LMPR (existing) | unchanged |
| 5..6 | OUT buffer | LMPR (existing) | unchanged |
| 7..12 | IN buffer | LMPR (existing) | unchanged |
| 13 | **Disasm aux + sysreg DB + rewrite hints** | **HMPR** (new) | first HMPR-paged-data consumer; helpers must land before this page is used |
| 14..15 | Explanation prose | HMPR (existing convention) | parallels page 13 |
| 16..17 | Editor scratch | HMPR | parallels |
| 18..21 | Simulator state | HMPR | parallels |
| 22..27 | Future paged document | HMPR | parallels |
| 28 | TFTP buffers | HMPR | parallels |
| 29 | Music pattern data | HMPR (inside IRQ) | parallels; IRQ wraps its own bracket |

**Section-A vs section-B vs section-C residence of the helper bodies**:

- Section A: holds ROM0 normally; LMPR-paged pages 4 (ENCTAB) or
  7..12 (IN current page) during their windows. **No helpers live
  here**.
- Section B: holds page 1 (BASIC sys page) under LMPR_DEFAULT, OUT-low-
  zone (page 5) under LMPR_ENCTAB. Helpers at `TRAMPOLINE_DST &7E00`
  area; new helpers join the family. **All paged-call /
  paged-data helpers live here** — section B is the LMPR-stable
  "kernel page" for our system.
- Section C: holds the assembler code (`&8000-&AFFF`) under default
  HMPR. Under paged_call, holds the target page during the call.
  Caller code lives here; target page code lives here briefly.

**Constraint added**: section B (page 1 under LMPR_DEFAULT, page 5
under LMPR_ENCTAB) must accommodate the trampoline + paged_call +
paged_data helpers. Today the trampoline alone is ~30 B. Adding
~100 B of new helpers brings us to ~130 B at `&7E00..&7EFF`. Section
B above `&7F00` is reserved as `TRAMP_SAFE_SP` scratch
(`trampoline.asm:305`). Plenty of room.

**No new page consumed by the mechanism itself.** Page 13 is
consumed by the first *user* of the mechanism (sysreg-table relocate).

**Brainstorm doc update needed?** Yes, minor: add a note that the
"new constants alongside LMPR_ENCTAB / LMPR_OUT_HIGH / LMPR_IN_BASE"
mentioned at `memory-layout-brainstorm.md:97` are now
`HMPR_DATA_TABLES = &0D` (page 13 in HMPR low-5 form), etc., and
that the access mechanism is `paged_data_map_hmpr(A)` /
`paged_data_unmap_hmpr` per §4.2 of this note.

---

## 6. Implementation plan

Each PR is a coherent, testable, mergeable unit. PRs land in order;
each depends on the previous.

### PR 1 — paged_call + paged_data helpers (medium)

**Scope:**
- Add `paged_call` body to `src/m3/trampoline.asm`, alongside the
  existing `enctab_trampoline_setup` etc.
- Add `paged_data_map_hmpr` / `paged_data_unmap_hmpr` (rename
  candidates: `hmpr_swap_in` / `hmpr_swap_out`) in the same file.
- Reserve scratch slots `paged_call_hmpr_save`, `paged_call_sp_save`,
  `paged_data_hmpr_save`, `paged_data_sp_save` in section B at
  `TRAMPOLINE_DST + 64..+72` (room for 8 B of scratch state without
  encroaching on the existing HMPR_SAVE / SP_SAVE at +32/+33).
- Add one boot self-test exercising `paged_call`:
  - At startup, write a known byte (e.g. `&5A`) into page 13 at
    section-C offset `&8000`.
  - Define a trivial 4-byte routine in page 13 that does
    `ld a, &A5; ret`.
  - Call it via `paged_call / DEFW &8004 / DEFB &2D` (page 13 HMPR
    value = `&20 | 13` = `&2D`).
  - Assert A == `&A5` on return; assert the byte at section-C `&8000`
    is restored to the caller's expected value (HMPR was put back).
- Add a parallel boot self-test for `paged_data_map_hmpr`:
  - Write `&5A` into page 13 offset `&8000`.
  - `ld c, &2D; ld hl, &8000; call paged_read_byte` (or the
    bulk-bracket equivalent).
  - Assert A == `&5A`.

**Risk callouts:**
- The section-B fetch-stability argument (§3.2, §3.3) was wrong
  twice during this note's drafting. The implementation MUST be
  reviewed against the HLOAD trampoline structure to confirm the
  fetch-from-section-B invariant holds across the OUT.
- The verification subagent discipline from
  `m6_strand_a_complete.md:107-110` MUST be applied: a dedicated
  review pass against the spec + existing trampoline pattern before
  impl dispatch.
- Port number cross-check (251 for HMPR, 250 for LMPR — easy to
  transpose, has bitten before per `m6_strand_a_complete.md:93-95`).

**Estimate:** ~150 lines of new assembly (helper body + tests +
documentation), medium PR.

### PR 2 — port `sysreg_table` to paged data; add 8 missing entries (small-medium)

**Scope:**
- Move `sysreg_table`, `pstate_table`, `dc_table`, `tlbi_table`,
  `sysname_table` from `src/m3/sysname.asm:716-793` to a new file
  `src/m3/sysreg_data.asm` placed at the head of page 13's content.
- Add a new build step: assemble `sysreg_data.asm` standalone (org
  `&8000`), produce a flat binary, HLOAD-target it to page 13 at
  startup. (Or: extend `enctab.enc`'s pattern — generate the page-13
  payload Mac-side as a separate `.dat` file, load via the same
  HLOAD trampoline mechanism extended to target page 13.)
- Replace the inline `ld hl, sysreg_table` reads in
  `sysname.asm` with `ld c, HMPR_DATA_TABLES; ld hl, &8000; call
  paged_data_map_hmpr / ldir / call paged_data_unmap_hmpr` per
  §4.2.
- Add the 8 missing sysreg entries (`hcr_el2`, `mair_el1`, `scr_el3`,
  `spsr_el3`, `tcr_el1`, `ttbr0_el1`, `tcr_el1`, `ttbr1_el1`,
  `vbar_el1`) — comes "for free" because we're no longer constrained
  by the test-variant `&C200` ceiling.
- Verify the test-variant binary tail returns to `&C10F` (or wherever
  it was pre-FAIL00) once the tables are gone.

**Risk callouts:**
- The first real consumer of paged_data — if PR 1's invariants are
  wrong, PR 2 surfaces it.
- Boot-time sequencing: page-13 load happens BEFORE
  `enctab_map_in` switches LMPR (the page-13 load uses HMPR; ENCTAB
  uses LMPR; they're independent). But the build-time mechanism
  needs to produce page 13's contents, which means a Mac-side step
  similar to `enctab.enc` generation. Probably extends
  `tools/refenc/` or adds a parallel `tools/sysreg-page-gen/`.
- The sysreg table is currently referenced by name (`ld hl,
  sysreg_table`); after the move, callers in `sysname.asm` need the
  page-13-offset-form address `&8000`. Cross-file refs become
  off-axis-page-aware; needs care to keep the section-A LMPR
  refs separate from section-C HMPR refs.

**Estimate:** ~300 lines (data file + build glue + sysname.asm
refactor + tests), small-medium PR.

### PR 3 — port M5 self-test fixture corpus off-axis (small)

**Scope:**
- The M3/M4/M5 boot self-test corpus
  (`src/m3/test_*.asm` files) consumes section-C code budget in the
  BUILD_TESTS variant. Move it to a paged page (page 14? — TBD per
  brainstorm doc); invoke the tests via paged_call entries that map
  the test page in and run.
- Net effect on prod variant: zero (these tests are
  `BUILD_TESTS`-only). Net effect on test variant: reclaims the
  spillover-past-`&C000` that's been the source of the test-variant
  fragility throughout M6.

**Risk callouts:**
- The test corpus assumes section-C scratch addresses
  (`assembler.asm:72` `OPVAL_ARRAY` at `&C100`, etc.); if a test
  paged_call body itself reads those, it works because section D
  under HMPR_TESTS is whatever HMPR was set to + 1 — *not* the
  caller's section D. The test page needs to be HMPR-paired such
  that section D (= test_page + 1) is itself a valid scratch
  destination or is unused. Probably easiest: test page is paired
  with a dedicated "test scratch" page, e.g. HMPR low5 = 14 puts
  section C = page 14 (tests) and section D = page 15 (test
  scratch).

**Estimate:** ~200 lines, small PR.

### PR 4 — codegen sysreg / mnemonic tables from Go-side authority (medium)

**Scope:**
- The current `sysreg_table` is hand-maintained in
  `src/m3/sysname.asm`. The authoritative list lives in
  `tools/sam-aarch64-format/sysregs.go` (39 entries per
  `memory-layout-brainstorm.md:29`). Hand-sync drifts.
- Add a Mac-side build step that generates `sysreg_data.asm` (or
  the page-13 binary directly) from `sysregs.go`, similar to how
  `build/enctab.enc` is generated.
- Apply the same pattern to other static tables: form table,
  mnemonic table, intercept tables. Each becomes a generated payload
  loaded into its designated page at boot.

**Risk callouts:**
- Largest of the four PRs by lines-changed but lowest risk because
  every change is data-only. Tests catch wrong data immediately.
- Depends on PR 2's page-13 loading infrastructure being settled.

**Estimate:** ~400 lines (codegen tool + build glue + N small
asm refactors), medium PR.

---

## 7. Open questions + risks

**Unresolved by this note:**

1. **The section-B fetch-stability argument needs one more careful
   read** before PR 1 implementation. §3.2 and §3.3 talked me out of
   two designs; the final HMPR-based design is structurally
   equivalent to the existing HLOAD trampoline and inherits its
   correctness — but the chain of reasoning above is error-prone
   enough that a verification-subagent pass on the PR 1 spec is
   mandatory before impl dispatch. (Standard discipline per
   `m6_strand_a_complete.md:107-110`.)

2. **Re-entrance of paged_call.** §3.4 documents that the static-save
   scratch precludes re-entrance. The brainstorm doc's worst-case
   "F1 on `msr SCTLR_EL1, x0`, then three simulator steps = 4 + 2×3
   = 10 swaps" (`memory-layout-brainstorm.md:81-85`) is sequential
   bracket-on-bracket-off, not nested — so non-re-entrant is fine
   for Phase 2 as currently planned. If the editor ever wants to
   invoke a paged routine from inside another paged routine, this
   needs revisiting. Document the constraint loudly in the helper's
   header.

3. **Spectrum 128K precedent specifically for "1-byte RST + 3 inline
   bytes (addr_lo, addr_hi, page_num)"**: the 128K's RST 28H uses
   1-byte RST + 2 inline bytes (no page byte because there are only
   2 ROMs and the toggle is implicit). Pete's "1-byte RST + 3 inline"
   intuition is *more general* than the 128K — and would work *if*
   we could install our own RST handler. We can't (§2.3). So Pete's
   exact shape is a hypothetical that solves a problem we don't
   strictly need to solve; the section-B trampoline at +3 B/site is
   the deliverable.

4. **ROM routine register-clobber for our case**: paged_call and
   paged_data_map clobber A, HL, DE; preserve BC, IX, IY. Callers
   that need to pass arguments to the target should use BC + IX + IY.
   This is a soft constraint; the alternative (preserve everything)
   adds ~10 T-states per call but no real cost. If a caller needs to
   preserve A across paged_call, they push it. Documented in the
   helper header; the existing M3 calling conventions can be left
   alone.

5. **What might surprise us from the SAM ROM?** Probably nothing
   directly — we're using HMPR-only, which is mechanically the same
   as the existing HLOAD trampoline. The one lurking gotcha: HMPR
   bits 5-6 are the mode-3 CLUT high bits
   (`sam-paging.md:140-150`). All `OUT (251), A` instructions in the
   helper MUST mask through `TSURPG`-equivalent logic, or the retro
   palette glitches every time we touch a paged data page. Brainstorm
   doc §6.5 (`memory-layout-brainstorm.md:108`) already flagged this.
   **PR 1's helper must preserve HMPR bits 5-7 (CLUT + external-mem)
   across the swap** — same discipline as the existing
   `trampoline_body` does via `in a, (251)` capture + restore.

6. **Music IRQ interaction**: if we ever EI during a paged_call
   window, the IRQ's section-C fetch sees the target page, not ROM0
   / interrupt vector. The IRQ handler at `&0038` is in ROM0 / our
   section A; LMPR controls section A; we're touching HMPR. So
   actually IRQ at `&0038` works correctly under our paged_call
   window — section A is unchanged. The IRQ handler might itself
   want to read screen data via HMPR (mode 3 CLUT bits etc.), so its
   handler needs to bracket its own HMPR access. Standard
   discipline; not a new problem this design introduces.

7. **The 7-day-old m6_strand_a_complete memory says page 4 is
   ENCTAB, pages 5-6 are OUT, pages 7-12 are IN, and the assembler
   binary ends at `&C10F` (test variant pre-FAIL00).** Verify against
   current main before PR 1 — sections of the brainstorm doc and the
   note above cite `assembler.asm` line numbers and offsets that
   were correct as-of `m6_strand_a_complete.md`'s last update (18
   days ago per the staleness warning). Specifically `OPVAL_ARRAY`
   at `&C100`, the `&E100..&FFFF` "free" region, and the trampoline
   layout at `&7E00..&7F00` should all be re-confirmed.

---

## 8. Headline

**Section-B `paged_call` helper, HMPR-based, structurally identical
to the existing HLOAD trampoline at `src/m3/trampoline.asm:362-396`.
+3 B per call site (6 B vs `CALL nnnn`'s 3 B). One PR lands the
mechanism and a test; the next ports `sysreg_table` to page 13 and
the 8 missing sysregs land for free; subsequent PRs port the test
fixture corpus off-axis and codegen the data tables Mac-side.**

The +3 B (not Pete's intuited +1 B) is a deliberate trade: it avoids
having to install a custom RST vector, which would require either
breaking SAMDOS's RST 8 or restructuring the boot path. The HLOAD
trampoline already proves the section-B+HMPR pattern works; we
generalise it instead of inventing new mechanism.

## 9. Sources

- `docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt:173-196`
  (RST vector inventory), `:264, 581-614` (RST 30H handler),
  `:639-652` (R1OFFCLBC), `:13908-13922` (R1OSR / POPOUT),
  `:9978-10030` (FARLDIR / FARLDDR), `:12944-12978` (PTDOS / SAMDOS
  hook dispatch), `:14852-14861` (TSURPG)
- `/tmp/spec128_rom0.asm:740-757` (RST 28H handler ROM 0 → ROM 1),
  `:807-818` (RST 28 continuation L005C), `:865-957` (SWAP /
  YOUNGER / ONERR in RAM at `&5B00`), from
  https://github.com/ZXSpectrumVault/rom-disassemblies/blob/master/Spectrum%20128K/Spectrum128_ROM0.asm
- `reference/comet-decoded/comet.asm:3161-3178` (getset / restore /
  jumphl), `:4876` (sproom), `:539, 1189, 1235, 1325, 3163` (SP-save
  pattern), `:2242, 2700, 2705, 2732, 2746, 3022, 3051, 3590, 3685,
  3805, 3928, 4112, 4141, 4354` (call-site survey)
- `~/git/samdos/src/b.s:497-501` (samhk hook table)
- `~/git/trinload/trinload.asm:181-188, 198-212` (page-aware writes)
- `docs/notes/sam-paging.md` — entire (LMPR/HMPR semantics, REL PAGE
  FORM, PTDOS dispatch, R1OSR/POPOUT/TSURPG); `:88-91` (section B
  = LMPR+1); `:140-150` (HMPR mode-3 CLUT bits)
- `docs/notes/2026-05-28-memory-layout-brainstorm.md` — entire;
  particularly `:10` (real code ceiling), `:43-62` (page-axis),
  `:97` (constants pattern), `:108` (CLUT preservation)
- `src/m3/assembler.asm:1-200` (current memory map);
  `src/m3/trampoline.asm` — entire (HLOAD trampoline body and
  SP-switch reasoning at `:362-425`); `src/m3/encoder.asm:439-498`
  (emit_byte LMPR bracket); `src/m3/reader.asm:218-237`
  (reader_next_kind LMPR bracket); `src/m3/main_loop.asm:218-265`
  (in_map_current / in_normalise_hl); `src/m3/sysname.asm:716-793`
  (sysreg / pstate / dc / tlbi tables)
- `docs/notes/2026-05-28-reader-paged-self-test-investigation.md`
  (SP-collision rationale, also cited by HLOAD trampoline)
- `docs/notes/2026-05-28-hload-16k-limit-investigation.md` (the
  SP-switch design that paged_call inherits)
- `docs/specs/2026-05-27-samdos-load-idiom.md` (the HLOAD-trampoline
  specification, structurally the closest precedent in our own code)
- Memory: `m6_strand_a_complete.md` (FAIL00 root cause + the
  "8 sysregs missing" diagnosis at `:40`),
  `feedback_correctness_over_workarounds.md` (the discipline that
  rejected option (a) on "no fence-sit" grounds)
