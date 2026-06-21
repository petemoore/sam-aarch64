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
opening screen. **Q1 RESOLVED by the RAM-diff experiment (2026-06-25): do NOT
reproduce them.** Measured, not argued: at the editor-idle sync point, the stock
ROM (which *ran* `MAINER3`) and Colin's fork (which *skipped* it) reach **identical**
`TVFLAG=&01` / `FLAGS=&00`. So the `MAINER3` flag-setting does not persist to matter
by the editor idle — reproducing it in our inject changes nothing. The inject omits
it. See "## Open questions — the RAM-diff experiment" below for the data.

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

These are **Colin's additions**. **Q2 RESOLVED by the RAM-diff experiment
(2026-06-25): reproduce Colin's full teardown — including `NSPPC=&FF` + `TVDATA=&10`
— on the auto-boot path** (then `jp bdos_boot_record` instead of Colin's `JP &102F`).
This *reverses* the earlier guess that they were Colin-specific BASIC-prep we could
skip: the experiment shows **both** the stock ROM and Colin's fork reach `NSPPC=&FF`
*and* `TVDATA=&10` at the post-boot editor idle — so these are the **normal**
post-boot values, not Colin cruft. The faithful inject therefore reproduces Colin's
exact teardown on the auto-boot path; the no-auto-boot path already inherits it (it
RETs into Colin's verbatim tail). **One honest caveat:** the experiment exercised
the BASIC/editor path, not an actual record-boot, so "reproduce" is the faithful +
safe choice (mirror Colin exactly), not a proof that the booted record strictly
requires the pokes.

## The RAM-diff experiment (RESOLVED Q1 + Q2)

**Pete's experiment (2026-06-25), the arbiter — measure, don't reason.** Boot the
stock SAM ROM v3.0 and Colin's full fork (his ROM + his EEPROM bootblock + B-DOS)
each to the **same** sync point — the ROM editor's keyboard-wait read at `&0514`
after an injected key 'x' — snapshot full RAM in both, and **diff**. Tool:
`tools/netboot-oracle/cmd/samboot-statediff` (uses the captures; the real 50 Hz
frame-int handler runs, so `LASTK`/`FLAGS`/`FRAMES` are genuinely populated).

**RESULT — both runs reach the same sync PC `&0514`; the four Q1/Q2 sysvars are
IDENTICAL:**

| sysvar | addr | stock v3.0 | Colin fork |
|---|---|---|---|
| `FLAGS` (Q1 bit 7) | `&5C3B` | `&00` | `&00` |
| `TVFLAG` (Q1 bit 5) | `&5C3C` | `&01` | `&01` |
| `TVDATA` (Q2) | `&5BBE` | `&10` | `&10` |
| `NSPPC` (Q2) | `&5C44` | `&FF` | `&FF` |

So the stock ROM's normal boot **already** reaches `NSPPC=&FF` / `TVDATA=&10` — the
exact values Colin's "extra" teardown pokes set — and the Q1 flags converge too.
→ **Q1: don't reproduce the `MAINER3` flags** (they don't persist to matter).
→ **Q2: reproduce Colin's full teardown incl. the pokes on the auto-boot path**
(they're the *normal* post-boot state, not Colin cruft). (55 other sysvar-band bytes
differ — keyboard-repeat/stream buffers + `ERRNR &5C3A`=`&50`-vs-`&00` from stock's
report-50 entry — none Q1/Q2-relevant.)

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
