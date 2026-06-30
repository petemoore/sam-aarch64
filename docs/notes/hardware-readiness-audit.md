# Hardware-readiness audit

The authority an agent checks **before deploying any program to the real
SAM+Trinity**. The deploy-guard hook reads this doc: a program on the
**DO-NOT-DEPLOY** list must not be pushed to hardware until its gaps are closed.

This is a **living doc** — update the matrix and the verdict lists as fixes land,
not a dated snapshot.

## The six known hardware fixes

Real Trinity hardware exposes six failure classes that the flat host harness did
not (the emulation↔hardware gap; the comprehensive-emulation north star lives in
registry **i126** + memory `feedback_comprehensive_emulation`). Each fix is a discrete code change:

| # | Fix | What it prevents |
|---|-----|------------------|
| 1 | `&FF` flush before every SD command (leading-`&FF` Ncc sync) | The all-zeros CSD read class (i145g) — command framed without the read-lag sync responds wrong on silicon. |
| 2 | `drv_init` **before** any SD transaction | SD bus activity before ENC init leaves the ENC in an undefined state. |
| 3 | `enc_rx_reestablish` **after** the SD transaction | The SD transaction disturbs the ENC RX path; without a re-arm, serving dies after the first SD read. |
| 4 | Clean-exit `tr_terminate` (not a bare `di; halt`) | A bare `di; halt` strands the machine; trinload cannot recover the program. |
| 5 | Bounded SD busy-wait | The shared `wait_ready` (`encdrv.asm:427`) loops `JR wait_ready` forever; a stuck Trinity hangs the program with no timeout. |
| 6 | Colin's 4-step deselect tail | The 2-step `sdc_deselect` leaves SPI state the card mishandles on real hardware. |

Tracking: #1 = **i145g** (DONE + propagated). #2 = **i242 / i244**. #3 = **i242 / i245** (re-arm modelling) + **i249** (emulation catch, DONE). #4 = **i243b**. #5 = **i246** (program: bounded `sdc_wait_ready` in shared `sd_csd.asm`, DONE) + **i250** (emulation: stuck-BUSY in the DEFAULT SD test path, DONE; i241 first added the opt-in csd_probe form). #6 = **i247** (program: 4-step deselect tail in shared `sd_csd.asm`, DONE) + **i251** (emulation: gated by default, DONE).

## Program × fix matrix

`HAS` = fix present. `MISSING` = fix absent (a real-hardware risk). `N/A` = the
program performs no SD transaction, so the SD-path fixes do not apply.

| Program (entry) | #1 flush | #2 init-before-SD | #3 enc re-arm | #4 clean exit | #5 bounded wait | #6 4-step deselect |
|---|---|---|---|---|---|---|
| **csd_probe** (`probe_main`) | HAS | HAS | HAS (`csd_probe.asm:484`) | HAS | HAS | HAS |
| **serve_main** (`netboot_serve.asm`) | HAS | HAS (`drv_init` :1481 before `csd_set_bd_records` :1500, i242/i244) | HAS (`enc_rx_reestablish` :538, i245) | HAS (`sv_fail_*` → `tr_terminate` :1601, i243b) | HAS (`sd_csd.asm` → bounded `sdc_wait_ready`, i246) | HAS (`sd_csd.asm` → 4-step `sdc_deselect`, i247) |
| **client_main** (`netboot_client.asm`) | HAS | HAS | HAS (`enc_rx_reestablish` after SD write, i245) | N/A (disk-booted: terminal green-border `halt` is its by-design end state, not a trinload-recovery path) | HAS (`sd_csd.asm` → bounded `sdc_wait_ready`, i246) | HAS (`sd_csd.asm` → 4-step `sdc_deselect`, i247) |
| **dumper** | N/A | N/A | N/A | HAS (`tr_terminate`, i243b) | N/A | N/A |
| **http** | N/A | N/A | N/A | **MISSING** (#4) | N/A | N/A |
| **server** | N/A | N/A | N/A | **MISSING** (#4) | N/A | N/A |
| **smoke** | N/A | N/A | N/A | **MISSING** (#4) | N/A | N/A |
| **eeprom_roundtrip** | N/A | N/A | N/A | HAS | N/A | N/A |
| **eeprom_flash_chunk1** | N/A | N/A | N/A | HAS | N/A | N/A |
| **port_probe** | N/A | N/A | N/A | HAS | N/A | N/A |
| **mgt_screen_demo** | N/A | N/A | N/A | HAS | N/A | N/A |

**Key shared-module fact:** fixes **#5 and #6 live in the shared `sd_csd.asm`**.
One edit each fixes **both** serve_main and client_main — do not patch them
twice.

## Deployment mode

How each program reaches the SAM determines which fixes are load-bearing.

| Mode | Programs | Notes |
|---|---|---|
| **trinload-pushed** | csd_probe, serve_main, dumper, http, server, smoke, eeprom_roundtrip, port_probe, mgt_screen_demo | Pushed over the wire by trinload; clean exit (#4) returns control to trinload's `start`. |
| **disk-booted** | client_main | Booted from MGT disk; does an SD **write**. |
| **harness-only** | (host-test entry points, `*_HOSTTEST`-excluded mains) | Never reach hardware; the carve-outs are the i231/i126 debt, not a deploy target. |

## Gap list — ranked by hardware risk

Highest risk first (a program reaching hardware with this gap can hang or
silently fail):

1. **serve_main #2** — SD transaction (`csd_set_bd_records`) ran *before* `drv_init`. **DONE** (i242 / i244): the reorder landed; `drv_init` (:1481) now precedes `csd_set_bd_records` (:1500).
2. **serve_main #3** — no `enc_rx_reestablish` before `serve_serve_once`; serving died after the first SD read. **DONE** (i242 / i245 model + i249 emulation catch): `enc_rx_reestablish` is now called (:538) before serving resumes.
3. **client_main #3** — no ENC re-arm after the SD write. **DONE** (i245): `enc_rx_reestablish` present in `netboot_client.asm`.
4. **serve_main #5 / client_main #5** — unbounded `wait_ready`; a stuck Trinity hangs forever. **DONE** (i246): the shared `sd_csd.asm` now uses the bounded `sdc_wait_ready` (sticky `sdc_timed_out`), so one edit fixed both; the default emulation now catches a regression (i250).
5. **serve_main #6 / client_main #6** — 2-step `sdc_deselect`; real card mishandles SPI tail. **DONE** (i247): the shared `sd_csd.asm` now emits Colin's proven 4-step deselect tail; the default emulation catches a regression (i251).
6. **#4 clean exit** — bare `di; halt` instead of `tr_terminate`. **DONE for the SD-path + key trinload-pushed programs** (i243b): serve_main (`sv_fail_*` → `tr_terminate`) and dumper (`tr_terminate` ×3). client_main keeps a terminal green-border `halt` **by design** (it is disk-booted, so #4 is N/A — nothing pushed it to recover to). **Re-verify before deploying:** `netboot_server.asm` still shows a bare `halt` ×2 (residual, or a `*_HOSTTEST`-only main); `netboot_http`/`netboot_smoke` exit paths unconfirmed this pass — neither is on today's deploy path.

## HARDWARE-READY verdict

Safe to deploy to real SAM+Trinity today (no SD path, or all applicable fixes
present):

- **csd_probe** — the reference / gold standard (HAS all six).
- **serve_main** — **now HAS all six** (i242/i244/i245/i243b landed; verified in source 2026-06-26). The i194 disk-push deploy target.
- **client_main** — HAS all applicable fixes (#4 is N/A: disk-booted, terminal `halt` by design).
- **dumper** — no SD path; #4 (`tr_terminate`) present.
- **eeprom_roundtrip**
- **eeprom_flash_chunk1** — same EEPROM-write + network-report + `tr_terminate` paths as `eeprom_roundtrip` (i226-hardware-proven), no SD path; writes the trinity-autoboot bootloader into chunk 1 (the bootblock). Flashed + read-back-verified PASS on real hardware 2026-06-26 (i135c).
- **port_probe**
- **mgt_screen_demo**

## DO-NOT-DEPLOY verdict

Must NOT reach hardware until the cited gaps close:

- **server**, **http**, **smoke** — #4 (clean exit) unconfirmed: `server` still shows a bare `halt`; `http`/`smoke` exit paths not re-verified this pass. No SD risk, but confirm a `tr_terminate` exit (or a `*_HOSTTEST`-only main) before pushing. None is on the current deploy path.

## Emulation gaps (the load-bearing concern)

The deepest finding was: **the emulator did not fail when fixes #3, #5, or #6
were absent** (now all three are caught) — so an agent could "prove" a program in emulation and still ship a
hardware hang. Per Pete's directive these were the top of the priority queue (they
gate *all* trustworthy hardware work). Tied to the **i126** comprehensive-emulation
north star; #3, #5, and #6 are now all closed:

- **#3 caught (i249, DONE)** — the model perturbs the ENC RX path across an SD transaction (`rxDisarmed`), so a missing `enc_rx_reestablish` now fails a host test (the serve-dies-after-SD class).
- **#5 caught (i250, DONE)** — a stuck-BUSY condition is now part of the DEFAULT SD test path (`TestCSDToBDRecordsBoundedOnStuckBusy`), so an unbounded `wait_ready` fails by hanging to the step cap; no opt-in needed (i241 first added the opt-in csd_probe form).
- **#6 caught (i251, DONE)** — the shared `sd_csd.asm` deselect path is now gated by `TestCSDToBDRecordsDeselectTailProper`: a 2-step close fails it (the model tracks the proven 4-step order via `LastSDCloseProper`). The exact silicon misbehaviour of the short tail stays Genuinely-unspecified, so reads aren't fabricated to fail — the gate is the proper-order assertion.

(Fix #1's emulation gap — the missing-flush / one-byte-read-lag class — is
covered by **i245**, the one-byte-lag model.)

## Trinload wedge & uptime (i278)

Two related symptoms were observed pushing programs over trinload (2026-06-26):
(1) a wedged serve program (the i270 WRQ hang) left trinload non-responsive to a
re-push — discovery got no `!` reply; recovery needed a manual reset; (2)
trinload can appear "not responding even though running". i278 audited both in
emulation and against the trinload source.

**The wedge class is the #4 clean-exit gap.** trinload (`src/netboot/trinload.asm`,
vendored verbatim from simonowen/trinload @ a4b7af7) is **not interrupt-driven**:
its `read_loop` (`:88`) only runs when the CPU is *in* it. trinload pushes its own
`start` as the return address before `jp (hl)` into a pushed program (`try_exec`,
`:230`), so a program that RETs cleanly hands control back to `start`, which
re-runs `drv_init` and re-enters `read_loop` — trinload self-heals the ENC and
stays re-pushable on *every* clean return. A program that **never returns** (a
bare `di; halt`, an infinite loop, a hung serve loop) holds the CPU forever:
`read_loop` never runs again, so a later `?` discovery goes unanswered and the SAM
looks dead. trinload cannot pre-empt this (no timer/NMI listener), and it is
vendored verbatim so we do not patch it — **the fix is the pushed program's
clean exit (#4 `tr_terminate`), not trinload.** The wedge candidates are therefore
**exactly the #4-missing programs**: `netboot_server.asm` (bare `di; halt` ×2,
`:1096`/`:1101`), `http_main.asm` (bare `halt` ×2, `:538`/`:544`, bootable build),
`smoke_test.asm` (bare `halt` ×2, `:249`/`:254`) — the DO-NOT-DEPLOY list above.
`serve` and `dumper` return via `tr_terminate` and do **not** wedge.

**Modelled in the harness (i278).** `tools/netboot-oracle/z80/trinload_test.go`:
- `TestTrinloadWedgedByNonReturningProgram` — pushes `jr $` (a self-loop, the
  stand-in for any non-returning exit), then a second `?`; asserts exactly **one**
  `!` reply (pre-execute answered, post-execute unanswered = wedge) and that
  control never returns to `start`.
- `TestTrinloadRespondsToRediscoveryAfterCleanReturn` — the positive control:
  pushes `ret`, then a second `?`; asserts **two** `!` replies (trinload re-entered
  `read_loop` and is still re-pushable). This is the property a #4 clean-exit
  program must satisfy.

**Natural timeout: there is none in trinload.** `read_loop` is a pure
event-driven poll — no counter, timer, watchdog, or uptime degrade; the only
non-packet exit is Esc → `drv_exit` (`:91`). So trinload does not "time out" or
degrade after hours of its own accord. The "not responding even though running"
symptom is **not** a software timeout — it is either (a) a wedged pushed program
(above), or (b) the ENC28J60 RX path going deaf at the hardware level during a
long idle (PHY link, RX-buffer state) — *not* trinload code, and outside the flat
host harness. Because a clean program return re-runs `drv_init`, the only path to
trinload-internal ENC deafness is a program that wedges before returning.

**Recovery requirement (documented outcome).** A wedged SAM, or hardware-level ENC
deafness, is recovered only by **reset / power-cycle** (i266 / TAPO `tapo.sh`) or
Esc-exit-and-restart at the SAM. There is no trinload-side fix; the durable fix is
ensuring every trinload-pushed program has a #4 clean exit (`tr_terminate`).

See the registry (`build/registry ready`) for the live ids and ranks.
