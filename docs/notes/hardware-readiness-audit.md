# Hardware-readiness audit

The authority an agent checks **before deploying any program to the real
SAM+Trinity**. The deploy-guard hook reads this doc: a program on the
**DO-NOT-DEPLOY** list must not be pushed to hardware until its gaps are closed.

This is a **living doc** — update the matrix and the verdict lists as fixes land,
not a dated snapshot.

## The six known hardware fixes

Real Trinity hardware exposes six failure classes that the flat host harness did
not (the emulation↔hardware gap; see `docs/notes/comprehensive-emulation` north
star, registry **i126**). Each fix is a discrete code change:

| # | Fix | What it prevents |
|---|-----|------------------|
| 1 | `&FF` flush before every SD command (leading-`&FF` Ncc sync) | The all-zeros CSD read class (i145g) — command framed without the read-lag sync responds wrong on silicon. |
| 2 | `drv_init` **before** any SD transaction | SD bus activity before ENC init leaves the ENC in an undefined state. |
| 3 | `enc_rx_reestablish` **after** the SD transaction | The SD transaction disturbs the ENC RX path; without a re-arm, serving dies after the first SD read. |
| 4 | Clean-exit `tr_terminate` (not a bare `di; halt`) | A bare `di; halt` strands the machine; trinload cannot recover the program. |
| 5 | Bounded SD busy-wait | The shared `wait_ready` (`encdrv.asm:427`) loops `JR wait_ready` forever; a stuck Trinity hangs the program with no timeout. |
| 6 | Colin's 4-step deselect tail | The 2-step `sdc_deselect` leaves SPI state the card mishandles on real hardware. |

Tracking: #1 = **i145g** (DONE + propagated). #2 = **i242 / i244**. #3 = **i242 / i245** (re-arm modelling) + the new program-fix item below. #4 = **i243b**. #5 = **i241** (stuck-BUSY emulation, opt-in). #6 = Colin's 4-step deselect.

## Program × fix matrix

`HAS` = fix present. `MISSING` = fix absent (a real-hardware risk). `N/A` = the
program performs no SD transaction, so the SD-path fixes do not apply.

| Program (entry) | #1 flush | #2 init-before-SD | #3 enc re-arm | #4 clean exit | #5 bounded wait | #6 4-step deselect |
|---|---|---|---|---|---|---|
| **csd_probe** (`probe_main`) | HAS | HAS | HAS (`csd_probe.asm:484`) | HAS | HAS | HAS |
| **serve_main** (`netboot_serve.asm`) | HAS | **MISSING** (`csd_set_bd_records` ~:1336 before `drv_init` ~:1339) | **MISSING** (no re-arm before `serve_serve_once`) | **MISSING** (bare `di; halt`, `sv_fail_cfg`/`sv_fail_init`) | **MISSING** (`sd_csd.asm` → unbounded `wait_ready`) | **MISSING** (2-step `sdc_deselect` `sd_csd.asm:246`) |
| **client_main** (`netboot_client.asm`) | HAS | HAS | **MISSING** (no re-arm after SD write) | **MISSING** (bare `di; halt`) | **MISSING** (`sd_csd.asm` → unbounded `wait_ready`) | **MISSING** (2-step `sdc_deselect` `sd_csd.asm:246`) |
| **dumper** | N/A | N/A | N/A | **MISSING** (#4) | N/A | N/A |
| **http** | N/A | N/A | N/A | **MISSING** (#4) | N/A | N/A |
| **server** | N/A | N/A | N/A | **MISSING** (#4) | N/A | N/A |
| **smoke** | N/A | N/A | N/A | **MISSING** (#4) | N/A | N/A |
| **eeprom_roundtrip** | N/A | N/A | N/A | HAS | N/A | N/A |
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

1. **serve_main #2** — SD transaction (`csd_set_bd_records`) runs *before* `drv_init`. Tracked: **i242 / i244** (reorder running).
2. **serve_main #3** — no `enc_rx_reestablish` before `serve_serve_once`; serving dies after the first SD read. Tracked: **i242 / i245** (model) + new program-fix item.
3. **client_main #3** — no ENC re-arm after the SD write. **Was untracked → new item.**
4. **serve_main #5 / client_main #5** — unbounded `wait_ready`; a stuck Trinity hangs forever. **Was untracked → new shared-module item.**
5. **serve_main #6 / client_main #6** — 2-step `sdc_deselect`; real card mishandles SPI tail. **Was untracked → new shared-module item.**
6. **serve_main #4 / client_main #4 / dumper / http / server / smoke #4** — bare `di; halt` instead of `tr_terminate`. Tracked: **i243b**.

## HARDWARE-READY verdict

Safe to deploy to real SAM+Trinity today (no SD path, or all applicable fixes
present):

- **csd_probe** — the reference / gold standard (HAS all six).
- **eeprom_roundtrip**
- **port_probe**
- **mgt_screen_demo**

## DO-NOT-DEPLOY verdict

Must NOT reach hardware until the cited gaps close:

- **serve_main** — missing #2, #3, #4, #5, #6.
- **client_main** — missing #3, #4, #5, #6.
- **dumper**, **http**, **server**, **smoke** — missing #4 (clean exit) only; otherwise no SD risk, but still gated on i243b.

## Emulation gaps (the load-bearing concern)

The deepest finding: **the emulator does not currently fail when fixes #3, #5,
or #6 are absent** — so an agent can "prove" a program in emulation and still
ship a hardware hang. Per Pete's directive these are the top of the priority
queue (they gate *all* trustworthy hardware work). Tracked as top-priority
emulation-gap items, tied to the **i126** comprehensive-emulation north star:

- **#3 not caught** — the emulator does not model the SD transaction disturbing the ENC RX path, so a missing `enc_rx_reestablish` passes (the serve-dies-after-SD class).
- **#5 not caught** — the modelled SD BUSY clears in ~1 poll, so an unbounded `wait_ready` never hangs; only the **opt-in** stuck-BUSY mode (i241) exercises it.
- **#6 not caught** — the model accepts the 2-step deselect; the 4-step tail requirement is unmodelled.

(Fix #1's emulation gap — the missing-flush / one-byte-read-lag class — is
covered by **i245**, the one-byte-lag model.)

See the registry (`build/registry ready`) for the live ids and ranks.
