# Robust resume-nudge delivery (i140)

How `monitor.sh` reliably delivers the task-done resume nudge into the Claude
Code TUI, and why the obvious "sleep longer" fix does not work.

## Symptom

On `TASK_DONE` the monitor stuffs `/context`, waits `CONTEXT_SETTLE`, then stuffs
the long `RESUME_NUDGE` and submits. The `/context` readout renders, but the
nudge never becomes a model turn — the agent sits idle until the ~1h
hang-timeout. This stalls the autonomous loop at every continue-vs-wind-down
checkpoint. i139 bumped `CONTEXT_SETTLE` 5→30s; the symptom persisted.

## Root cause (reproduced, not guessed)

The nudge is dropped because a **long string stuffed as one `screen -X stuff`
burst, right after a full-TUI redraw (`/context`), is swallowed wholesale** by
the TUI's paste ingestion. It is *not* a render-timing race.

Reproduced in an isolated throwaway `screen` session driving a real `claude`
process (never the live loop session), stuffing the verbatim 858-char
`RESUME_NUDGE`:

- **Old single-burst stuff after `/context`: 0/4 delivered.** The nudge text
  never even appeared in the input box; no turn started. Deterministic, every
  time — exactly Pete's symptom.
- A **short** marker in the same position always landed and submitted — so the
  trigger is the *long burst*, not the position or a modal.
- The same long string stuffed in **≤64-char chunks with a ~0.1s pause between
  chunks landed reliably** and submitted into a turn.
- A plain 858-char `X`-string stuffed as one burst when *not* immediately after
  `/context` landed fine — confirming the interaction is (long burst) × (just
  after the `/context` redraw).

Why the `CONTEXT_SETTLE` bump didn't help: the sleep is *before* the stuff, but
it is the **stuff burst itself** that is dropped, not a keystroke arriving
mid-render. No fixed sleep can win a problem timing isn't causing. `/context` is
a static, fixed-size readout (it does **not** grow taller at higher context
fill, and is not a keystroke-capturing modal in the default TUI), so "bigger
readout renders slower" was a false premise.

## Fix (chunk + verify-and-retry)

Two changes in `monitor.sh`:

1. **`stuff_raw()` chunks every payload** into `CHUNK_SIZE` (64) byte bursts with
   a `CHUNK_DELAY` (0.1s) pause between them. Short strings are a single chunk
   and behave exactly as before; long ones (the nudge, the startup prompt) now
   land byte-for-byte. This alone fixes the drop.

2. **`nudge_until_turn()` verifies delivery and retries.** After chunk-stuffing
   the nudge and sending a settled Enter, it polls the screen via
   `screen -X hardcopy` for the TUI's in-turn marker (`esc to interrupt`,
   `TURN_MARKER`). If no turn started within `NUDGE_VERIFY_WAIT` seconds it
   retries (a bare Enter first — the text is usually already sitting unsubmitted —
   then a full re-stuff as last resort), up to `NUDGE_TRIES` times. Success is
   *observed*, not assumed.

Verified end-to-end by running the actual modified `monitor.sh` against a fresh
isolated session with an isolated `ALOOP_SEMA_DIR`: touching `task-done` twice
produced `nudge delivered (attempt 1, 1s)` both times, and the agent took a real
turn each time (read the readout, began grepping the OPEN registries per the
nudge). The live loop session was never touched.

## Knobs (env overrides)

`ALOOP_CHUNK_SIZE` (64), `ALOOP_CHUNK_DELAY` (0.1), `ALOOP_NUDGE_TRIES` (4),
`ALOOP_NUDGE_VERIFY_WAIT` (8), `ALOOP_TURN_MARKER` (`esc to interrupt`).
`ALOOP_CONTEXT_SETTLE` stays as a small settle for the redraw but is no longer
load-bearing.
