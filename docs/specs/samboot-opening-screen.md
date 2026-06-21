# SAMBOOT opening-screen — faithful-reproduction contract (i257)

**Status:** living design doc. The implementation contract for restoring the SAM
power-on opening screen (rainbow stripes + MGT banner) in the SAMBOOT patched
bootblock inject.

## The principle — zero degrees of freedom

Colin's forked system ROM **removes** the stock ROM's power-on opening screen
(it replaced the `&ED1B` RAINBOW SCREEN with a Trinity probe and the `&0F7F`
report-50 handler with an EEPROM fetch — see `colin-rom-fork-diff.md`). SAMBOOT
**adds it back**. The combined behaviour (Colin's forked ROM + Colin's EEPROM
bootblock + our inject) must be a strict **superset** of the original stock ROM
flow, plus exactly two additions: Colin's B-DOS load, and our auto-boot check.

Therefore every reproduced step is **copied from the stock ROM, byte-for-byte,
not approximated**. Where the stock ROM had a behaviour, we reproduce *that exact
behaviour* — the exact wait (any key, a timer, a specific key — whatever it was),
the exact teardown, the exact exit. The only place we have freedom is the
**auto-boot path**, which has no stock-ROM precedent (the stock ROM never
auto-booted); that one decision is recorded below and was made by Pete, not
guessed.

All addresses below are stock SAM ROM v3.0
(`docs/sam/sam-coupe_rom-v3.0_annotated-disassembly.txt`), cross-checked against
the patched-ROM capture (`~/sam-archive/samboot-capture/`, via
`colin-rom-fork-diff.md`). "Intact on Colin's ROM" = byte-identical patched-vs-stock
(verified), so the routine is safe to `CALL` from the bootblock.

## The exact stock-ROM opening-screen flow

At power-on the stock ROM ran, in order:

### 1. Build the rainbow stripes — `&ED1B` RAINBOW SCREEN
```
ED1B  LD DE,PALTAB+1 (&55D9)   ; palette table, skip entry 0
ED1E  LD HL,LINICOLS (&5600)   ; line-colour table, L=0
ED21  LD B,L / LD C,L          ; scan counter = 0
ED23  RBOWL: LD (HL),B / INC HL / LD (HL),C / INC HL   ; {scan_lo, 0,
       LD A,(DE) / INC DE / LD (HL),A / INC HL / LD (HL),A / INC HL  ;  colour, colour}
       LD A,B / ADD A,&0B / LD B,A / CP &A6 / JR C,RBOWL   ; step scan +11 until 166
ED35  LD (HL),&FF             ; terminator
```
Pure RAM writes into `LINICOLS`. The stripes are then painted **live** by the ROM
line-interrupt ISRs (FRAMINT re-arms STATPORT from `LINICOLS[0]` each frame;
LINEINT writes CLUT reg 0 per scan line). They render only with interrupts
enabled.

### 2. Route to the lower screen — `RST 08 / DB 50` → `MAINER3` (`&0F67`)
The RAINBOW SCREEN ended with `RST 08 / DB 50` (raise REPORT 50). The RST-08 error
machinery routes to `MAINER3`, which sets up the **lower** screen for the report:
```
0F67  MAINER3: CALL CLSLOWER (&06B5)   ; "SETS CHANNEL K ALSO" — clears the LOWER
                                       ;   screen and selects channel K (lower window
                                       ;   print position, SPOSNL). THIS is why the
                                       ;   banner lands in the lower screen: reports
                                       ;   always render there (Pete, 2026-06-25).
0F6A  LD HL,TVFLAG (&5C3C) / SET 5,(HL) ; "CLEAR LOWER SCREEN ON KEYSTROKE"
0F6F  DEC HL (&5C3B FLAGS) / RES 7,(HL) ; "NOT RUNNING"
0F75  CALL ERRHAND1 (&0F7B)            ; -> the report-50 banner at &0F7F
```
**The load-bearing setup is `CALL CLSLOWER`** (channel K / lower screen).
`CLSLOWER` (`&06B5`) is intact on Colin's ROM.

The two flags (`TVFLAG` bit 5 = "clear lower screen on keystroke", `FLAGS` bit 7 =
"not running") are BASIC **error-recovery editor** state, *not* part of the visible
opening screen. **OPEN QUESTION Q1 — decided by the RAM-diff experiment, NOT by
reasoning (Pete, 2026-06-25).** Whether to reproduce them turns on a fact we will
*measure*, not argue: does Colin's boot path (his ROM + EEPROM + B-DOS load) leave
the sysvar state the stock ROM's `MAINER3` path would have, or does it dirty/clean
it differently? See "## Open questions — the RAM-diff experiment" below. Do NOT
finalize this in the inject until the experiment answers it.

### 3. Print the banner — `&0F7F` report-50 handler
```
0F7F  XOR A / CALL UTMSG (&3DB0)   ; print message 0 (the MGT banner) from the
                                   ;   UMVAL table. UTMSG = LD DE,(UMSGS) then POMSG.
0F83  LD HL,BGFLG (&5A34) / LD A,&82 / LD (HL),A / RST &10   ; é via the foreign set
0F8A  LD (HL),0                     ;   (BGFLG != 0 selects the foreign glyph table)
0F8C  LD A," " / RST &10
0F8F  LD A,(PRAMTP &5CB4) / INC A / ... *16 ... / RST 30 / DW PRNUMB1 (&F5AB)  ; RAM size
0F9F  LD A,"K" / RST &10
```
Renders `"   MILES GORDON TECHNOLOGY PLC       © 1990  SAM Coupé <RAM>K"` (© = SAM
charset `&7F`; é via the foreign set). **Colin overwrote the UMVAL msg-0 text at
`&F5DD`** with his EEPROM reader, so `UTMSG` by number would print garbage. We
print our **own byte-exact copy** of the stock text via the ROM's own message
printer `POMSG` (`&3DB4` — UTMSG's "print msg A from list at DE" entry; intact on
Colin's ROM) pointed at our embedded list, keeping the verbatim é / RAM-size / "K"
tail. (Already implemented this way in `samboot_bootblock.asm`, i229.)

### 4. Wait for ANY key — `&0FA2` WTFK
```
0FA2  WTFK: CALL READKEY (&1CB1)
0FA5        JR Z,WTFK              ; READKEY returns Z when NO key is ready, so this
                                   ;   loops until ANY key is pressed (confirmed: the
                                   ;   stock comment is "WAIT FOR A KEYPRESS")
```
**Any key**, via `READKEY` (`&1CB1`, intact on Colin's ROM) — NOT a specific key.
(The i229 RAM-test *demo* used a trinload Esc-poll; that was a demo convenience and
must NOT appear in the bootblock.)

### 5. Teardown → BASIC — `&0FA7`
```
0FA7  CALL CLSLOWER (&06B5)        ; clear the lower screen
0FAA  LD A,&FF / LD (LINICOLS),A   ; disarm the rainbow (terminator at LINICOLS[0])
0FAF  JP ERRHAND2 (&102F)          ; exit to BASIC
```
Colin's bootblock tail at `&40A9` already does this teardown (CLSLOWER + disarm
LINICOLS + `JP &102F`), plus two extra sysvar pokes of his own (`&5C44`, `&5BBE`).
So on the no-auto-boot path we simply **fall into Colin's verbatim tail** — it *is*
the original teardown (a superset of it).

## Mapping onto the inject (the splice context)

The splice changes `&409E` (`CALL &805F`) to `CALL inject`; by then Colin's
bootblock has already loaded B-DOS chunks 2–13. `inject` runs at `&415E`. Faithful
structure:

```
inject:
        ; (1) stripes — verbatim &ED1B RAINBOW SCREEN (build LINICOLS). BEFORE &805F
        ;     so it runs in the screenless Go core (emulation-verified) and is up
        ;     while B-DOS initialises. Interrupts are already enabled (the bootblock
        ;     EI'd at &409D), so the stripes paint.
        <build LINICOLS, verbatim>
        call    &805F                 ; Colin's B-DOS init (his addition)
        ; (2) lower-screen setup — verbatim MAINER3 print-position step
        call    &06B5                 ; CLSLOWER — channel K / lower screen
        ; (3) banner — verbatim &0F7F, via POMSG + our byte-exact text + é/RAM/K
        <banner>
        ; --- our addition: the auto-boot check ---
        call    samboot_read_config    ; CY = auto-boot record in HL, NC = none
        jr      c, inject_autoboot
        ; (4) NO AUTO-BOOT: verbatim &0FA2 WTFK — wait for ANY key
inject_wtfk:
        call    &1CB1                  ; READKEY
        jr      z, inject_wtfk
        ; (5) teardown: RET into Colin's verbatim tail at &40A1->&40A9
        ;     (restore: paging, then CLSLOWER + disarm LINICOLS + JP &102F = original)
        ret
inject_autoboot:
        ; AUTO-BOOT (our addition; Pete 2026-06-25): show the FULL opening screen
        ; (stripes + banner, both — done above), then reproduce the teardown's
        ; stripe-cancel and boot the record. NO wait (unattended). The stripe-cancel
        ; mirrors "the stripes vanish as the user leaves the opening screen".
        call    &06B5                  ; CLSLOWER (= teardown &0FA7)
        ld      a, &ff
        ld      (&5600), a             ; disarm LINICOLS (= teardown &0FAA)
        ld      a, l
        ld      (BD_BOOT_RECORD), a
        jp      bdos_boot_record        ; i122a: HRECORD select + ALHK, no return
```

### The auto-boot path — the one degree of freedom (Pete decided) + OPEN QUESTION Q2
The stock ROM never auto-booted, so this path has no behaviour to copy. **Pete's
decision (2026-06-25): show the full opening screen (stripes AND banner), then
cancel the stripes and boot — no wait-for-key.** The cancel reproduces the stock
teardown's stripe-disarm (`&0FA7`–`&0FAA`: `CLSLOWER` + `LINICOLS[0]=&FF`).

**OPEN QUESTION Q2 — Colin's two extra teardown pokes — decided by the experiment.**
Colin's bootblock teardown (`&40A9`) does the stock teardown PLUS two pokes the
**stock ROM never did** (verified: stock `&0FA7` is only `CLSLOWER` + `LINICOLS=&FF`
+ `JP &102F`):
  - `LD (&5C44),&FF` — `&5C44` = **`NSPPC`** (next-statement PPC); `&FF` = the ROM
    sentinel for "no pending jump to a new line/statement" → reset the BASIC
    interpreter's pending-jump state.
  - `LD (&5BBE),&10` — `&5BBE` = **`TVDATA`** (temporary colour/attribute) = `&10`.

These are **Colin's additions**, almost certainly cleanup he needed *because* his
path loads B-DOS (the stock ROM didn't). **The nuance:** "follow the stock ROM" is
the default, but for these pokes it is NOT clear-cut — **our inject is in Colin's
situation, not the stock ROM's: we also load B-DOS.** So Colin's `NSPPC`/`TVDATA`
cleanup may be *necessary for us too*. On the no-auto-boot path we already inherit
it (we RET into Colin's verbatim tail). For the auto-boot path, whether to
reproduce it (and whether an auto CODE-block boot, not just BASIC, needs it) is
decided by the experiment below — NOT reasoned. Do NOT finalize until then.

## Open questions — the RAM-diff experiment (decides Q1 + Q2)

**Pete's experiment (2026-06-25), the arbiter for Q1 and Q2 — measure, don't reason.**
Boot the stock SAM ROM v3.0 and Colin's full fork (his ROM + his EEPROM bootblock +
B-DOS) each to the **same** sync point — the editor's `LASTK` (`&5C08`) read after
an injected key 'x' — snapshot full RAM at that PC in both, and **diff**. The diff
reveals whether Colin's boot leaves sysvar/RAM state the stock ROM didn't (e.g.
`NSPPC`, `TVDATA`, `TVFLAG`, `FLAGS`), giving evidence — not reasoning — for whether
our inject must reproduce the `MAINER3` flags (Q1) and the `NSPPC`/`TVDATA` pokes
(Q2). Pete's instinct is to follow the stock ROM, but Colin may have changed the
exit for a real reason (B-DOS side effects); the diff settles it.

**Enabler (DONE-ish):** the experiment requires booting Colin's fork *through* B-DOS
init to BASIC in emulation. The Go netboot core already models Trinity/SD/paging and
treats the screen as RAM; the one gap was that its run loop stopped at **every**
`HALT`, whereas a `HALT` is only terminal with interrupts disabled (`di; halt`). A
`HALT` with interrupts *enabled* (B-DOS init's frame-timed delay loops, e.g. `&AB17`)
must resume on the frame interrupt — exactly as real hardware does. With that fix
(the `IFF1`-guarded HALT, applied to **both** Go harnesses for sync), **Colin's full
fork now boots coherently to the ROM editor idle loop (`&0100`–`&05FF`) in the Go
core** — no SimCoupé/i126 needed. Tracked + landed via the harness-HALT item / PR.

**Experiment fidelity note:** start minimal (resume the EI-halt by advancing PC +
inject `LASTK` via the existing i138 stub, skipping the real `&0038` frame-int
handler) — since BOTH runs skip it identically, the diff still isolates the
Colin-vs-stock difference. Escalate to running the real handler only if the diff
shows handler-dependent artifacts.

## What is emulation-verified vs hardware (i230 / i135c)

- **Emulation-verified (Go netboot core):** the stripe LINICOLS table is built
  before `&805F` (asserted), and the spliced chunk-1 loads coherently to `&805F`.
  The decision logic (config → wait/boot dispatch) is host-tested at the
  `inject_decision`/`inject_wtfk` entry where `&805F` is skipped.
- **Hardware-only (i230 RAM test, Pete present; then i135c flash):** the banner +
  WTFK + teardown all use real ROM display/keyboard routines (`CLSLOWER`, `POMSG`,
  `READKEY`) that the screenless core cannot run, so they sit after `&805F` (which
  does not return in the Go core, by design — i232) and are confirmed on the real
  SAM: stripes render (✅ already confirmed via the i229 demo), banner lands in the
  lower screen, any-key continues, clean exit. Emulation-verified ≠ hardware-verified.

## The demo (`mgt_screen_demo_standalone.asm`)
For the i230 RAM test the demo must mirror this faithful flow: add `CALL CLSLOWER`
before the banner (so it lands in the lower screen, like the inject) and replace
the Esc-poll with the verbatim `READKEY` any-key WTFK. The Go harness records the
banner text via its RST-10 recorder and asserts the LINICOLS table; the banner
*position* and stripe *pixels* are hardware-confirmed.
