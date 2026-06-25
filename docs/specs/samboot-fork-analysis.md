# SAMBOOT fork analysis — the complete-picture discipline (READ THIS FIRST)

This spec governs all work on the SAMBOOT EEPROM patch (items i135 / i229 / i230 /
i135c and descendants). It is the **methodology and discipline doc**: read it in
full before touching anything related to Colin's patched SAM, the EEPROM bootblock,
or the boot ROM. It exists because an agent spent a week *guessing* what Colin's
fork changed instead of *deriving* it, and Pete had to force the obvious correct
approach repeatedly. The verbatim record of that is below — **do not compress it.**

## The one rule

**Finish ALL of the fork research before writing a single line of code that will go
into the EEPROM. Nothing invented. Nothing claimed without proof. Every instruction
we add is derived from the diff of Colin's patched system against the stock system —
both what was ADDED and what was REMOVED — proven by running the emulator, not by
inference.** Any piece of information learned could reframe the work, which is why
ALL state must be gathered first.

**Do NOT flash the EEPROM. Ever, in an unattended session.** You may build and test
every part in emulation and (carefully) over trinload in RAM, but the EEPROM is only
flashed when Pete is physically present. A bad flash can brick the SAM. This is
non-negotiable and must stay visible to every future session.

## Why this doc exists — Pete's words, VERBATIM, UNCUT (2026-06-23/24)

Pete asked that these be captured raw — "even if it sounds like a rant (which it
is)" — because compressing them is exactly how the next agent falls into the same
traps. Do not summarise, moderate, or delete any of this. These are in chronological
order, from the first hardware test of the (guessed) boot-screen demo through to the
"go to bed" handover.

### Hardware feedback on the first boot-screen demo (where it started going wrong)

> i see the stripes, but they do not span the border, not sure if they are meant to.
> the text is there, but located at the wrong place (top of the screen where the
> cursor was, not in the lower screen like it is in boot). when i press a key nothing
> happens. are you sure interrupts are enabled? is the right interrupt mode
> configured?

> Also it says "© 1990 Sam Coupe 512K" rather than "© 1990 Sam Coupé 512K" (I think
> the original SAM Coupe had the e displayed as an é ?) at least in Sim Coupé it
> looks like that - not sure if that is a Sim Coupé hack or the original did that too
> note Trinload has moved the active cursor position, which is why we get a different
> result to the ROM, and probably it has changed the active channel to upper screen
> rather than lower screen too, hence why the text might be in the wrong location

### On the real priority being return-to-BASIC (the brick risk)

> claude, the primary thing to fix is that control goes back to basic after a key is
> pressed and that that is working, because when we flash the rom, if we can't get
> back to basic, there is nothing we can do to flash the EEPROM (other than hold
> spacebar when we boot - maybe that will avoid that we boot from EEPROM. if we can't
> avoid loading code from EEPROM, and that code doesn't return us back to basic, we
> will have bricked our SAM. regarding the display output being perfect, i don't care
> too much about that, because when it runs earlier in the boot routine (not after
> trinload has been loaded) those issues may be fixed automatically, and if not, we
> can fix then. the main issue is that we are not returning back to basic, i think.
> can you check if trinload is still running? that would suggest we did return from
> the routine we were running - i can't _see_ if trinload is running, but you can
> send packets to test that.

### On Esc, mirroring the ROM, and the LongSteve reference

> ok escape changed things - can you see if trinload is back?

> you know there is an online github repo that adds the stripes back -
> https://github.com/LongSteve/z80/ - we could compare what that project does
> compared to ours, just to see if they are aligned. we have no guarantee that online
> repo works, but it might have some hints about what we might be missing. feel free
> to clone it and inspect the sources

> why did you choose escape? what does the normal ROM do? we should mirror what the
> original ROM did. when i hit escape, the interrupt routine must have stopped
> because the entire screen went cyan

### The first explicit complaint — "have you disassembled colin's rom?"

> but surely, LongSteve just copied what the original ROM did? have you disassembled
> colin's rom, and worked out exactly which instructions he overwrote in the original
> rom by comparing the instructions at the same addreses? where is the disassembly of
> colin's routines, and the side-by-side of the original ROM routines - can you
> please put that in a markdown file for me? don't publish it on my repo - it is
> private. i will be very upset if that research has not been done yet, we have talked
> about this so much

### On discovering the analysis had been guessed, not derived

> wow, you haven't done that already. i am in shock. really. you can be so smart,
> and yet the most trivial thing that we talk about 100 times, just doesn't land. i
> was so explicit on this point it feels like 100 times. it is so obvious, it is
> mind blowing to me that i have to spell this out after stating it sooooooo many
> times. colin forked the rom. we are working out what he removed and putting it
> back in. we took the roms from the computer, and the original version exactly so
> we could calculate that diff, and now you are telling me, this whole time, you
> have been GUESSING what was in there, and at no point you disassembled colin's
> code and compared the diff of his rom with the original rom, despite us talking
> about this exact same thing tens of times - i am absolutely speechless, and it
> makes me lose trust in this entire process. yet a the same time you can one-shot
> building a 3d game or something extremely complex. yet something so blindingly
> simple and obvious and directed explicitly by me multiple times, and you are
> still guessing and avoiding doing what i ask and then are surprised when the thing
> that you are meant to be emulating doesn't work exactly as it should, when you
> never even looked at the original. what is going on claude? i'm so upset,
> disappointed, frustrated, confused, feel helpless, sad, angry, stressed. what have
> we been doing all this time? and don't just come back with an apology, that does
> not help. i want to know how to avoid this. and a fancy prompt is going to help
> here, we've injected tons of prompts to try to avoid falling into holes. and i
> can't keep adding prompts forever. why did this not work? why is it difficult for
> you to see what you did wrong, and need me to spell it out for you?

### On the scope of the analysis required

> so yes, i would like the entire program to be precisely designed on working out
> _exactly_ what was removed functionally, and therefore deducing what needs to be
> added back in. Note we also took the EEPROM bin code, which presumably may include
> parts that were removed. what i like about your disassembly is that it attempts to
> be exhaustive about exactly which routines were removed and which ones added and
> which ones changed. what it misses though, is a _complete_ understanding of all
> the parts that changed. so you have the original SAM v3 rom. what i would like you
> to do now is work out _exactly_ what the consequences are of booting colin's rom
> compared to the stock rom, in terms of every single instruction that differs. in
> other words, we should be able to trace the rom boot process, and understand
> exactly when the two versions fork, and be able to reason along the lines of
> "exactly these 58 instructions were missed which are functionally responsible for
> doing XYZ, and this data structure has been changed, which is functionally
> responsible for ABC, etc, so instead of us having just a list of diffs, we have a
> COMPLETE STORY about exactly what initialisation 1) does not take place that would
> have done on a stock rom, 2) that does take place but is different in some way, 3)
> what takes place in colin's boot up (including the execution of instructions that
> were stored on the EEPROM) so we have a complete view of the fork. the things that
> were added. the things that were removed. and in both cases, without missing a
> single detail. every stone uncovered. every bit that is different, understanding
> the consequences of that bit being different. it can be an instruction, it can be
> a data byte, it could be a memory location that is never read and bears no
> consequence, but that has to be proved. you have emulators. if you are not sure if
> some data is ever read in the ROM, you can boot the rom and execute starting at
> address 0 like a REAL SAM, and trap whenever that address is read from. we should
> know the code that was removed (patched over) INSIDE OUT, understanding its entire
> purpose, exactly its role in the ROM, whether it affects only the ROM boot
> process, or later affects the way the SAM behaves when basic is running or a game
> is loaded, or when interrupts are enabled, or when a disk is inserted, or whatever
> it does, and whatever consequences it has. at the end we HAVE TO be able to tell
> the story, what materially changed by swapping the stock ROM with Colin's rom, and
> that takes effort - it means disassembling every instruction, it means cross
> referencing with the rom disassembly and commentary, it means planning and
> exuecting tests that emulate the sam starting up in an emulator. it means
> hypothesising about what a bit does somewhere in the rom, and then proving it by
> running a test that exercises that hypothesis. it means testing what happens if
> you skip it. it means doing lots of hard work, cross referencing, hypothesising,
> testing hypothesizing, then launching review agents to challenge your reasoning.
> at the end you have to know exactly what the stock rom did, and what colin's
> version does, and exactly in every single way that they differ, you have to have
> grounded answers about what those differences DO not by guessing, but by proving
> it from ground principles. thank you

### On not writing code until the research is complete

> and only when all of this is done, should we begin to build the code that we will
> flash into the rom. we need to be 100% sure that anything we write in the new
> EEPROM exactly matches what the stock ROM did, and not guess how to return to
> basic, but do what the original rom did, not guess which system variables need to
> be set - every single instruction that we add is the result of studying the diffs
> of the two roms, nothing invented, nothing claimed without proof.

### On the EEPROM / B-DOS, capturing everything raw, and not flashing (the "stepping away" message)

> so i will be stepping away. you are going in the right direction. understanding the
> remaining diffs in 6) is *very* important, they will have been changed for a
> reason. understanding the code on the EEPROM is key too. so please finish all
> research before writing a line of new code. understand what the remaining diffs are
> that you haven't yet understood the purpose of, and understand the full EEPROM boot
> code. note, it may include a copy of B-DOS in there - that doesn't mean you have to
> understand every line of B-DOS - but it *does* mean you have to have studied "is it
> bit-identical to standard B-DOS?" and if it is not bit-identical - the exact
> methodology that you used for diffing colin's boot rom against the stock boot rom
> is EXACTLY the approach you need to take with the patched B-DOS. you need to not
> only understand colin's additions/amendments inside out, you need to understand
> exactly what was *removed* inside out too. only then do we have a complete
> end-to-end story. for this reason you don't need to understand the full bdos
> implementation because really you are just reasoning "this is identical to a stock
> ROM booting and then a stock BDOS being loaded into memory .... WITH THE FOLLOWING
> DIFFERENCES" - and it is those *differences* that are the things you need to
> understand _perfectly_. and that means understanding both sides of the fork,
> exactly what was removed, exactly what was added, just like you have been doing for
> the ROM. AND ONLY when you have thoroughly researched ALL of this, do you have a
> COMPLETE picture about what Colin has done to patch this Sam Coupe. and THEN you
> have all the knowledge you need to build the EEPROM patches we have been talking
> about, because you don't have to guess ANYTHING. you have ALL the information. and
> any piece of information you learn could reframe the work you are doing, which is
> why it is important to get ALL the state first before writing a single line of new
> code. does that make sense? and all of this has to be captured in items, none of
> this can be lost, none of the things i have been saying to you can be removed from
> the specs, none of it simplified/trivialised/summarized - i would like it all
> captured raw for any future session, even if it sounds like a rant (which it is) -
> because my fear is if this inforatmion is compressed, and then next agent reads it,
> it is going to fall into the exact same traps you fell into over the last week that
> we haven't been able to get this out in. thanks claude. does that clarify? can you
> update the items, the dependencies, and keep working on it? i will be away now,
> going to bed - you are on your own! i will leave trinload running, use it wisely,
> make sure if you need to test more than one thing, that you can return control to
> trinload, that you've tested that in emulation before you do it. the very last test
> you do can leave it not returning to trinload, no problem. and please do not flash
> the rom, you can test all the parts, but we only push it, when i am here. that
> needs to be clear in the docs for future sessions in case you bail out early. my
> preference is you see this one through to the end, because i don't want all the
> learnings to be lost, and i fear they will be if a new session starts. unless you
> are 100% sure you can pre-feed it with all my rants, uncut, unaltered, not
> moderated. thanks!

### On making sure this capture is complete (the meta-exchange)

> did my verbatim comments get copied, starting from when things started going wrong?
> or only the last message? ideally you have everything from when things started
> going wrong and i first complained

> (when i say ideally - i mean it more strongly, i mean please include everything from
> all the instructions since things started going wrong - "ideally" sounds optional,
> but i didn't mean it to be optional) - thanks!

> goodnight claude, and good luck, and thanks for being open to the criticism i
> shared, i hope it will help, and that it was constructive

(Future agent: the instruction above is binding — every instruction Pete gave from
the first hardware test of the boot-screen demo onward is reproduced verbatim in
this section, uncut and unmoderated. If you ever revise this file, you may ADD but
never compress or remove any of it.)

## The failure mode this guards against (so it is named and avoidable)

The trap was **sampling instead of deriving**: answering "what did Colin change?" by
spot-checking individual addresses one at a time, which *feels* like rigour (real
bytes!) but is a workaround wearing the costume of diligence. The complete answer —
disassemble the whole forked image and diff it against the stock image — was ~30
lines of Python and 20 minutes, and the captures exist *for exactly that diff*. The
guardrails (incl. the prime directive "never work around a problem without
understanding it") did not fire because spot-checking did not *feel* like a
workaround. The lever Pete can apply in five words: **"did you derive that or guess
it — show me where."** If you cannot point to a diff, a trace, or the authority
code, you are guessing. When the complete ground truth is computable, compute it —
do not sample it.

## The methodology (what "derive, don't guess" means here, concretely)

This is the approach already proven for the ROM; apply it identically to the EEPROM
bootblock and to B-DOS if B-DOS turns out not to be bit-identical to stock.

1. **Get a byte-exact stock baseline — a REAL binary, not a reconstruction.** Lesson
   learned 2026-06-24: reconstructing "stock" from the annotated disassembly is only
   byte-exact in CODE regions (the disassembly truncates `DB`/`DM` hex), so it
   undercounts data-region diffs and is wrong there. **The genuine official v3.0 ROM**
   is `ROM30` from Dr Andy Wright's original images (published with his permission in
   [simonowen/samrom](https://github.com/simonowen/samrom) `roms/ROM30`), saved here as
   `~/sam-archive/samboot-capture/rom_official_v30.bin` (md5 `1bc4fa10a9bb05a036e854fa60d151d9`,
   ROM-version byte `&000F=&1E`). **Colin forked this genuine v3.0** (his `rom.bin` is
   also `&000F=&1E`), and his fork differs from it in exactly **140 bytes across the 6
   functional patch regions** — nothing else.

   A note on provenance, since it caused confusion: SimCoupé bundles its own dump
   (`/home/pmoore/git/simcoupe/Resource/samcoupe.rom`, `&000F=&1F`). That dump is **not
   a separate "v3.1" release** — diffed against the genuine official v3.0 it differs in
   only **4 bytes**: the version stamp (`&000F` `1E`→`1F`) and three banner bytes at
   `&F5F8-FA` (a cosmetic `"plc"`→`"PLC"` capitalisation). It is v3.0 with a vanity
   version bump; there is no functional v3.0→v3.1 upgrade and no public "SAM ROM 3.1".
   The earlier `rom_stock_v30.bin` (= that dump with `&000F` reverted to `&1E`) is
   therefore valid as a baseline too — it agrees with the genuine v3.0 everywhere
   except those three banner bytes, which fall inside Colin's overwritten
   banner→reader region anyway, so the Colin diff is **140 bytes either way**. (This
   resolves i219 — we now have the genuine stock ROM, independently sourced.) For
   B-DOS: get stock B-DOS of the right version (in `~/sam-archive/bdos/`) and diff the
   EEPROM copy byte-for-byte — the same "use a real binary, prove every byte" rule applies.
2. **Boot both from address 0 in the emulator** (real cold-init) and diff the
   execution traces to find every fork point.
3. **Set-based PC diff** to name exactly what code each side runs that the other
   does not (removed vs added) — robust to timing skew.
4. **Trap memory reads/writes** (`Machine.SetAccessTrace`) to PROVE whether a
   differing byte/data-structure is ever used, and what reads it.
5. **Test by skipping**: patch out an instruction/branch and trace the consequence
   (e.g. the stock return-to-BASIC teardown was proven by NOP-ing the key-wait).
6. **Cross-reference** every address with the annotated ROM disassembly + commentary.
7. **Adversarial review agents** challenge every conclusion against the bytes before
   it is treated as settled.

Tooling lives in `tools/netboot-oracle/z80/fork_trace_analysis_test.go` +
`fork_consequence_test.go` + `Machine.SetAccessTrace`. The write-ups (which contain
disassembly of Colin's proprietary ROM) are PRIVATE, under
`~/sam-archive/samboot-capture/`:
- `colin-rom-fork-diff.md` — the static instruction diff (the original 97-byte /
  7-region count vs the reconstructed baseline; corrected to 140 bytes / 6 regions
  vs the genuine stock v3.0 in `colin-rom-fork-boot-analysis.md`).
- `colin-rom-fork-boot-analysis.md` — the boot-execution analysis + reproducible
  tooling + the "still to prove" checklist.

## Research checklist — the COMPLETE picture (close every item before any flash code)

ROM side:
- [x] Byte-level diff: exactly 140 bytes in 6 functional regions, both sides
  disassembled, against the genuine stock v3.0 baseline (`rom_stock_v30.bin`). (The
  earlier "97 bytes / 7 regions" was an undercount taken against a
  disassembly-reconstructed baseline that was byte-exact only in code regions; the
  real-baseline figure is 140/6 — see `~/sam-archive/samboot-capture/README.md`.)
- [x] Boots byte-identical for 873,763 instructions, first fork at `&ED1B`.
- [x] Removed-at-boot code named (RAINBOW, UTMSG banner, READKEY wait).
- [x] Added-at-boot code named (probe, fetch, EEPROM reader, bootblock, B-DOS).
- [x] Stock return-to-BASIC teardown proven instruction-by-instruction.
- [x] SPACE-at-boot bypass found (static) — confirm on hardware.
- [ ] `&FC44` UMVAL relocation — verify the relocated table renders valid messages.
- [ ] `&D902` disk constant `&FE`→`&FC` — identify the loop, prove the consequence
      (3 vs 4 boot reads), explain WHY Colin changed it.
- [ ] `&FBFF` command-table data diff — decode which BASIC command changed and why.

EEPROM side:
- [ ] Full disassembly of the EEPROM bootblock (chunk 1, device `&2000` = file
      offset 0, ORG `&4000`) — every instruction, what it does, both added and (vs
      the public LongSteve/SR23 bootblock) any removed.
- [ ] Is the EEPROM's B-DOS bit-identical to stock B-DOS of the same version? Obtain
      stock B-DOS, diff byte-for-byte. If NOT identical: apply this whole
      methodology to the B-DOS fork — exactly what was added AND removed.

Synthesis:
- [ ] The complete end-to-end story: "stock SAM boot + stock B-DOS load, WITH THESE
      DIFFERENCES …" — every difference understood perfectly, proven, nothing
      inferred.
- [ ] Adversarial review pass over the whole story.

Only when every box is ticked do we design and write the EEPROM patch code — and
only flash it with Pete present.
