# SAM Coupé music-playback research — for Phase 2 editor retro-UI

**Status:** read-only research, uncommitted. Captured 2026-05-28 to inform the Phase 2 editor's chiptune background-music affordance (see `docs/ROADMAP.md` §"Editor vision → Retro UI affordances").

This note answers seven questions about how SAM Coupé music was authored and played back during the platform's commercial life (≈1989-1994 era), and where E-Tracker / Sound Machine sit in that picture. The audience is the future Phase 2 editor designer choosing between "static driver at page-29 plus tune data" and "host-included driver assembled into the editor binary".

## TL;DR

- **E-Tracker** (FRED Publishing, 1992; Wołoszyk + Siuda, localised by Adrian Parker): AY-style tracker driving SAA1099 directly. Ships a compiler producing a self-contained ~5 KB CODE file with two entry points (`CALL base` = start, `CALL base+6` = tick). Polled (`BASIC.PLAY`) and interrupt-driven (`INT-MUSIC`) integration examples ship on the disk.
- **The Sound Machine** (Revelation Software, 1991; Paul Angel; re-released by Persona 1997): waveform-based composition tool, 6 channels, up to 10 user-defined waveforms. Ships `SDRIVER` (1932 B) + 49352-B `.MSC` data files.
- Two distinct products, different authors, different formats — Sound Machine "for the beginner", E-Tracker "total control".
- **For the editor:** E-Tracker's compiled-tune model is the near-perfect fit — small driver, two CALL entries, documented IM2 wrapper.

## Question 1 — What are E-Tracker and Sound Machine?

| | **E-Tracker** | **The Sound Machine** |
|---|---|---|
| Authors | Maciej J. Wołoszyk + Andrzej Siuda (Polish ESI scene); English localisation by Adrian Parker | Paul Angel (UK) |
| Publisher | FRED Publishing, Dundee (1992) | Revelation Software (1991); re-released by Persona on Blitz 4B (1997) |
| Genealogy | "based on the ZX Spectrum port of the Amiga SoundTracker program" (worldofsam) | Original SAM design; no upstream lineage found |
| Niche | "total control and flexibility over the sound chip" (manual p.1) | "ideal for the beginner" (Coupe Scrapbook review, 82%) |
| Sound model | Pattern tracker: instruments + ornaments + envelopes; 32 patterns (v1.2), 256 (v2.3); 6 SAA channels | Waveform synth: up to 10 user-defined waveforms; two-waveform combinations per instrument; 6 channels |
| Tune-file size | ~3-10 KB compiled (95% compression on ~100 KB authoring source) | 49352 B per .MSC (≈3 pages) |

**Relationship:** Independent tools at different difficulty levels. E-Tracker manual p.1 positions explicitly against Sound Machine: *"Many of you may already own 'The Sound Machine', and whereas it is an excellent introduction to music, Etracker allows you total control and flexibility over the sound chip in your computer"*.

Sources: E-Tracker manual (downloaded from `worldofsam.org`), `worldofsam.org/products/e-tracker`, `spectrumcomputing.co.uk/entry/35000` (Paul Angel/Revelation/1991), `mono.org/~unc/Coupe/Utils/smachine.html`, disks under `~/sam-corpus/disks/`.

## Question 2 — How does host-program integration work?

**E-Tracker — "compiler + two CALLs" model.** The manual (p.19) describes this precisely:

> *"Once you have created your musical masterpiece, you will want to use it in conjunction with your own programs. To do this you need the compiler program on your Etracker disk. … You will be prompted for an address. This can be any value in the range 0 to 65535. Unless you understand how the memory is allocated in the Coupe, then we suggest that you ALWAYS use the address 16384."*

Once compiled+merged at an address `BASE` (default `&4000`, but FRED magazines almost universally chose `&8000`), the resulting file exposes two entry points:

- `BASE`     → initialise & start (also acts as stop if called when already playing)
- `BASE+6`   → tick one frame's worth of work

This is the integration pattern repeated identically in every FRED magazine issue from FRED 25 onwards. Concrete example from `FRED Magazine Issue 28 (1992)` BASIC file `-+ETUNES+-`: `LOAD filename$ CODE 32768`; `CALL 32768` (init+start); `DO WHILE INKEY$<>" ": PAUSE 1: CALL 32774: LOOP`; `SOUND 28,0` (silence — writes 0 to SAA register 28 = output mixer enable). `32768=&8000`, `32774=&8006`.

The compiled-tune file's first 6 bytes are the dispatch:
- `&8000: 21 B3 84` `LD HL,&84B3` — set HL to module-data pointer
- `&8003: C3 EF 83` `JP &83EF` — jump to init routine
- `&8006: 3E 01 3D 20 1F …` — tick entry: countdown literal at &8007 (default 6) self-modified to vary speed

(Disassembled from `~/sam-corpus/disks/FRED Magazine Issue 30 (1993).mgt:E1`.)

**E-Tracker — interrupt-driven integration.** The `INT-MUSIC` BASIC file (E-Tracker Program Disk) POKEs a 116-byte IM2 wrapper to `&4000`. Its structure, summarised from disassembly:

- `&4006` (START): `DI`; save `(&5B70)` (BASIC's interrupt-chain pointer) into a slot inside the STOP routine; install our handler at `&4034` into `(&5B70)`; switch LMPR to page 12 (where music lives); `CALL &8000` to init the player; restore LMPR to page 1; `EI; RET`.
- `&4020` (STOP): mirror of START but writes 0 into `(&5B70)`.
- `&4034` (IM2 handler): `BIT 3,C` (status port bit 3 = FRAME); if not frame, `JP &0049` (chain to ROM IM2); otherwise save **everything** — `BC/DE/IX/IY` + shadows `AF'/BC'/DE'/HL'` — call the tick trampoline, restore everything, then `JP &0049`.
- `&4059` (tick trampoline): self-modifies in current LMPR + SP; switches to a private music stack at `&C000`; pages in LMPR=12; `CALL &8006` (player tick); restores LMPR + SP.

This is the **canonical SAM "BASIC + background music" idiom**: hook `(&5B70)`, filter for FRAME on STATUS bit 3, full register save including shadows, private stack, LMPR-flip around the tick, chain to ROM `&0049` so other interrupt sources still work. The Tech Manual (p.72) calls `&5B70` merely "Reserved (2)" — the BASIC-interrupt-chain convention is observed-universal but not formally documented in the v3.0 manual.

**Sound Machine — driver-plus-data model.** SM disks ship `SDRIVER` (1932 B at &8000), `INITSDR` (78 B at &8000, mostly a SAMDOS-hook bootloader for the .MSC), and per-tune `.MSC` files (49352 B fixed, loaded at &14000 = page 5, spans 3 contiguous pages). No readme is present on the disk and worldofsam doesn't document SDRIVER's call convention. The fixed-size .MSC across all 9 sample tunes confirms it's a memory dump of three full 16 KB pages, not a compact serialisation. **SDRIVER's API is an open question** — would need disassembly to commit.

Sources: `E-Tracker Program Disk:INT-MUSIC` BASIC; E-Tracker Manual p.19-20; SM disks; Tech Manual §FRAMIV (2534), §interrupt vectors (1788), §STATUS port (1288).

## Question 3 — Memory footprint

**Player code:** ~2 KB stripped of tune data (FRED 25-32 era E1-E9 files are 2144-5029 B and start with byte-identical 64-byte preludes — so subtracting the per-tune size gives a ~2 KB player). By FRED 35 the player is reified as a 9364-byte `E-player` file separate from the tunes — the bigger figure probably bundles instrument banks. Sound Machine's `SDRIVER` is 1932 B.

**Tune data:** E-Tracker compiled tunes 2-5 KB (FRED 30 E1=2144, E2=2783, E3=3027, E4=2259, FRED 35 E1=5029; manual quotes ~5 KB for a 100 KB authoring source = 95% compression). Sound Machine .MSC tunes 49352 B fixed — waveform samples dominate.

**Driver state:** The INT-MUSIC IM2 wrapper is 116 B at &4000, plus a private music stack at &C000 (uses ~10 levels). Negligible.

Source: FRED 30, FRED 35, E-Tracker Manual p.19, Blitz 4B SM disk.

## Question 4 — Time-sharing model (interrupt vs poll)

**E-Tracker supports both, depending on which BASIC example the integrator copies.**

- The **BASIC.PLAY** model is **polled** from the host's main loop:
  ```
  40 CALL 32774: PAUSE 1: GOTO 40
  ```
  `PAUSE 1` synchronises with one frame interrupt (≈20 ms). This works fine when the host has nothing to do except wait for keystrokes.

- The **INT-MUSIC** model is **interrupt-driven** — once initialised, the music runs in the background from the SAM's frame interrupt (50 Hz) without further host intervention, via the IM2 wrapper at `&4000-&4073` described in Question 2. The manual on p.20 markets this as a feature for non-experts: *"for those who don't know the value of interrupts, the music will play without any intervention by you, once you have initiated it."*

The default tick rate is **once per `n` frames** where `n=6` is the manual's default (50/6 ≈ 8.3 rows/sec, suitable for ≈120 BPM 4/4 with 16ths per beat). The compile-time speed parameter accepts 1-15 in hex (`Command 3`, manual p.14).

**Sound Machine.** I did not find explicit documentation of the SM driver's tick model in the corpus or web archives. The Sam Coupe Scrapbook review (`http://www.mono.org/~unc/Coupe/Utils/smachine.html`) is consumer-focused and silent on internals. Given the driver fits in <2 KB and the .MSC fits in 3 pages, the most likely architecture is interrupt-driven by analogy with E-Tracker. **Open question.**

Source: E-Tracker Manual p.19-20; `~/sam-corpus/disks/E-Tracker Program Disk (19xx) (FRED Publishing).mgt:INT-MUSIC` (concrete IM2 wrapper); `https://www.worldofsam.org/forum/2020-11-06/1858` (Stefan Drissen forum post quoting the polled pattern); Tech Manual lines 2534-2545 (FRAMIV / LINIV).

## Question 5 — Register preservation and shared state

**E-Tracker via INT-MUSIC:** the IM2 wrapper saves AF/BC/DE/HL + IX/IY + all four shadows (AF'/BC'/DE'/HL') + LMPR (`IN A,(&FB)` into a self-modified literal) + SP (into another self-modified literal). The private stack is at &C000 (page 1's high 16 KB under BASIC's default LMPR). After the tick LMPR and SP are restored byte-identically. The host can use **any register** including shadows freely.

**Polled `CALL 32774`:** the player's tick prelude (`LD A,1 / DEC A`) clobbers AF without saving — the host is expected to save its own state around the CALL. BASIC does this automatically; assembly integrators must PUSH around the call.

**Implication for the Phase 2 editor:** under IM2, the editor's main-line code runs without register-discipline collision. Shared state to track:
1. **LMPR** — wrapper saves+restores it; editor must not assume LMPR stays put across an arbitrary instruction (the M6 trampoline pattern in `docs/notes/sam-paging.md` already understands this).
2. **`(&5B70)` IM2 chain pointer** — must not be overwritten without coordinating with the music driver.
3. **SAA1099 registers** — exclusive to the music driver.
4. **The private music stack page** — must be reserved (top of page 1 today; revisit in our layout).

## Question 6 — Examples in the wild

The corpus has ample E-Tracker examples but few Sound Machine integrators (SM was used more as a personal-use tool than embeddable middleware). Five disks worth mining when the music work begins:

1. **`~/sam-corpus/disks/E-Tracker Program Disk (19xx) (FRED Publishing).mgt`** — has `INT-MUSIC` BASIC (the canonical 116-byte IM2 wrapper as POKE table), instrument samples (`*.I`), authoring-source modules (`enolA_G .M`, `axeL_F .M` at 78626 B), and the compiled `MUSIC` file at &C000. Single best concrete reference for IM2 integration.

2. **`~/sam-corpus/disks/FRED Magazine Issue 30 (1993).mgt`** — polled integration BASIC (`ETUNES`) + 4 compiled tunes (`E1`-`E4`, 2-3 KB each) all loading at &8000. Smallest concrete polled-integration example. Player prelude is byte-identical across the four (verified).

3. **`~/sam-corpus/disks/FRED Magazine Issue 35 (1993).mgt`** — evolved integration: `E-player` (9364 B) is separate, tunes `E1`-`E9` (5029 B each) load at &C000 and switch dynamically. Closer to the architecture Phase 2 wants (static driver + swappable tune data).

4. **`~/sam-corpus/disks/E-Tracker Program Disk V1.2 (19xx) (FRED Publishing).mgt`** — has the compiler (`COMPILER` BASIC + `COMPILER.1/2/3`) plus the authoring tool (`ETracker.1`, 16177 B). The Mac-side build pipeline runs this compiler under SimCoupé.

5. **`~/sam-corpus/disks/Blitz Magazine Issue 4B - Sound Machine (1997) (Persona) _b1_.mgt`** — full SM reissue: `SDRIVER` (1932 B), `INITSDR` (78 B), 8 .MSC sample tunes. The comparison-point disk if we want to understand the design space E-Tracker won against.

Honourable mention: `E-Tunes Player (19xx) (Andrew Collier).mgt` — **despite the name, this is the ProTracker2 / Amiga MOD player, not E-Tracker** (67976 B multi-page CODE). The "E" prefix is unrelated. Source: `intensity.org.uk/samcoupe/ptcompiler.php`.

## Question 7 — SAA1099 programming model

**The chip** (`docs/saa1099/SAA1099_493200_DS.pdf`): 6 frequency generators (8 octaves × 256 tones, 31 Hz - 7.81 kHz); 2 noise generators (each: 3 fixed rates + 1 frequency-modulated by a tone generator); 6 noise/frequency mixers; 12 amplitude controllers (L+R × 6 channels × 4 bits); 2 envelope generators (8 shapes; control channels 2 and 5); master enable at register 28.

**SAM interface** (Tech Manual 1056-1061): address port = 511 dec = `&01FF` (high-byte bit 0 selects address vs data); data port = 255 dec = `&00FF`. Canonical write: `LD BC,&01FF; LD D,<reg#>; LD E,<value>; OUT (C),D; DEC B; OUT (C),E`.

E-Tracker uses this verbatim (verified at FRED 30 `E1:&8104`+) and writes a 25-byte SAA shadow block per tick (`&83D3-&83EC`) — classic AY-tracker design (build shadow register file across the frame, squirt to chip on tick). SAM BASIC's `SOUND a,d` does the same thing one register at a time.

**Does either tool drive the BEEPER instead?** No — both target the SAA exclusively. The SAM BEEPER is a single-channel square wave, useless beyond a PC-speaker tone. COMET, by contrast, uses BASIC's `SOUND` keyword for single-channel beep feedback (`reference/comet-decoded/comet.asm:4880` `soundtab`) — useful as a *minimal* alternative if Phase 2 wants type-clicks without a real music engine.

## Closing — recommendation skeleton

Three viable architectures, with swap-count costs traced back to the memory-layout brainstorm at `docs/notes/2026-05-28-memory-layout-brainstorm.md` §3 (which already reserves **page 29** for "Music pattern data + SAA driver scratch").

### Option A — static E-Tracker driver on page 29, tunes paged in on demand

Driver + tune both live on page 29; IM2 wrapper sits at a low fixed address in section A under LMPR_DEFAULT (mirror of INT-MUSIC's `&4000-&4073`); per-tick the wrapper switches LMPR to page 29, calls the player at `&8006`-equivalent, restores LMPR.

- **Tick swap count: 2** (LMPR in + out). Already costed in `2026-05-28-memory-layout-brainstorm.md:83` ("~30 T × 50 Hz = 1500 T-states/s = < 0.05 % CPU"). Lowest-cost option.
- **Sizing:** ~3 KB driver + ~5 KB tune ≈ 8 KB of page 29 used; ~8 KB headroom remains.
- **Build pipeline:** Mac-side, run E-Tracker `COMPILER` under SimCoupé, extract the resulting CODE file with `samfile cat`, package as an HLOAD payload alongside the editor.
- **Best for:** Editor users who want a single chosen ambient tune, swappable between sessions but not mid-edit.

### Option B — driver baked into the editor binary, tunes paged separately

Driver compiled into the editor's section-C code window (~3 KB of section-C budget); tune data on page 29. Tick from IM2 calls section-C code directly with no LMPR flip for the code, only for the data fetch.

- **Tick swap count: 2** (same as A — the data still has to be paged in). Tighter latency because LMPR is restored before *data read*, not after *code finishes*.
- **Sizing:** Driver eats ~3 KB of section-C code budget. Current headroom is 84 B (`m6_strand_a_complete.md:27`); needs M6 PR 4+ relief first.
- **Best for:** Editor that wants seamless mid-edit tune transitions.

### Option C — Sound Machine SDRIVER + .MSC blob

SDRIVER (~2 KB) at a fixed low address, .MSC blob (49 KB) across pages 28-29 — would conflict with TFTP buffers (page 28 per brainstorm).

- **Tick swap count: 2-4** (undetermined — SDRIVER's call convention not documented; would need disassembly).
- **Sizing:** ~2 KB driver + 48 KB tune is 3 physical pages.
- **Best for:** A "showpiece" mode where the music is the *demo* rather than the affordance. Probably overkill for ambient background.

### Lean (non-binding)

Option A. Cost model already factors it in; integration pattern documented (INT-MUSIC); tune format small (~5 KB); authoring path open (E-Tracker's compiler is on the program disk); failure mode contained (IM2 wrapper saves everything, so broken music can't corrupt the editor). Sound Machine and ProTracker2-on-SAM (Stefan Drissen, Andrew Collier) are wrong fits — much bigger, sample-fidelity focused, designed as destination programs not embeddable middleware.

When Phase 2 starts, the **first piece of code to write** is not the music driver — it's a smoke-test that uses the INT-MUSIC POKE table verbatim, loads one FRED-magazine compiled tune, and verifies it ticks under SimCoupé while a stub editor handles arrow keys. That settles the integration question with one day's work before any architectural commitment.

## Open questions I could not resolve

1. **Sound Machine SDRIVER call convention.** No documentation found; would need disassembly. Tractable but uncosted.
2. **E-Tracker v2.3 format vs v1.2 format compatibility.** worldofsam mentions v2.3 has "variable length pattern data" and 256 patterns vs v1.2's 32. The compiled-output format may differ; haven't verified. The FRED magazines all appear to use v1.2-era output.
3. **Andrew Collier's E-Tunes Player vs ProTracker2 Compiler.** I'm fairly confident Collier's "E-Tunes Player" disk in our corpus is the ProTracker2 player (based on file size + multi-page CODE layout + intensity.org.uk's mention that Collier wrote a third-party PT2 compiler), but I did not disassemble enough to confirm the format end-to-end. Cross-check on a real PT2 module file would settle it.
4. **The exact role of memory location `&5B70`.** Tech Manual calls it "Reserved (2)" but every interrupt-driven SAM program I looked at treats it as BASIC's interrupt-chain pointer. The convention is universal but undocumented in v3.0 of the Tech Manual. There may be a later supplement; not chased.
5. **Whether INT-MUSIC's `$0049` chain target is in ROM or in RAM.** The chain target `$0049` falls in low ROM. The exact ROM dispatcher there is the IM2 dispatcher entry post-vector-fetch, but I haven't traced it through the SAM ROM disassembly. Not blocking — the convention works empirically.

## Sources

External:
- E-Tracker Instruction Manual, FRED Publishing 1992 — `worldofsam.org/sites/default/files/dl-12/E-Tracker%20Manual.pdf` (read pp.1-21).
- `worldofsam.org/products/e-tracker`, `…/products/protracker-2`, `…/products/sam-mod-player`, `…/forum/2020-11-06/1858`.
- `spectrumcomputing.co.uk/entry/35000` (Sound Machine).
- `mono.org/~unc/Coupe/Utils/smachine.html`, `…/etracker.html` (Sam Coupe Scrapbook reviews).
- `intensity.org.uk/samcoupe/ptcompiler.php` (ProTracker2 / Andrew Collier context).
- `github.com/Deltafire/SCPlayer`, `github.com/stefandrissen/SAM-MOD-player` (modern reference players).

Disks (`~/sam-corpus/disks/`): `E-Tunes Player (19xx) (Andrew Collier).mgt` (actually PT2), `E-Tracker Program Disk (19xx) (FRED Publishing).mgt` (INT-MUSIC), `E-Tracker Program Disk V1.2 (19xx) (FRED Publishing).mgt` (COMPILER), `FRED Magazine Issue 25/28/30/35.mgt`, `Sound Machine (1991) (Paul Angel).mgt` (partly corrupted), `Blitz Magazine Issue 4B - Sound Machine (1997) (Persona) _b1_.mgt`.

Internal: `docs/ROADMAP.md` §Editor vision; `docs/specs/vision.md`; `docs/notes/2026-05-28-memory-layout-brainstorm.md` §3 (page-29 reservation) + §4 (swap-count line 83); `docs/notes/sam-paging.md`; `docs/sam/sam-coupe_tech-man_v3-0.txt` (lines 1056-1061 SAA ports, 1288 STATUS, 1333-1543 SAA programming, 1788 IM2 vector, 2534-2545 FRAMIV/LINIV, 3990 `&5B70` Reserved); `docs/saa1099/SAA1099_493200_DS.pdf`; `reference/comet-decoded/comet.asm:4495,4880` (BEEP-based feedback precedent).
