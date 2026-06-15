# `tools/autonomous-loop/` — the autonomous-loop monitor (i97)

A small **deterministic** watcher (`monitor.sh`) that keeps ONE interactive
Claude Code session working across context limits — **losslessly** — by driving
it from outside via `screen -X stuff`. No LLM lives here: the agent runs the
proven `docs/ROADMAP.md` orchestrator and this script only relays signals and
resets context cleanly. Design + rationale: item-registry **i97**.

## Why

Claude Code auto-compacts context *lossily*, and an agent can't passively see its
own context level. This loop avoids both: the agent is *shown* `/context` on
demand and does a genuine `/clear` + re-prompt (re-grounded from ROADMAP) before
the limit, so nothing is summarised away. The session stays interactive and
`/remote-control` keeps working (same process throughout).

## Protocol (the startup prompt tells the agent to follow this)

1. Finish one work item (a merged PR) → `touch ~/.claude/autonomous-loop/task-done` → end the turn.
2. Monitor stuffs `/context`; the readout reaches the agent.
3. Agent: **≥20% free → next item** (loop); **<20% free → wind down** (write the
   ROADMAP handover) → `touch ~/.claude/autonomous-loop/wound-down`.
4. Monitor stuffs `/clear` + the startup prompt → clean reset. Repeat.

The agent never blocks on Pete: questions go in the `qN` registry and it keeps
working; it only addresses Pete directly when the whole non-Pete backlog drains.

## Run

```sh
ALOOP_SESSION=<your-screen-session> tools/autonomous-loop/monitor.sh
```

Run it in a **second** screen window; claude runs in window 0 (the `cl` window).
Tunable via env: `ALOOP_WINDOW`, `ALOOP_PROMPT`, `ALOOP_POLL`,
`ALOOP_HANG_TIMEOUT`, `ALOOP_CLEAR_SETTLE`, `ALOOP_SUBMIT_SETTLE`.

**Run exactly one monitor per session.** A single-instance lock
(`$SEMA_DIR/monitor.lock`) makes a second monitor refuse to start; if you
restart, the old one must be dead first (the lock auto-clears on a clean exit,
and a stale lock whose PID is gone is replaced). Two monitors driving one session
is what produced the duplicate-`/clear` glitch below.

## Status

Core built; **first live test 2026-06-15** (Pete), two failure modes found and
fixed — treat as **improving, not yet proven** (question-registry **q14**):

1. **A long stuffed line didn't submit.** `screen -X stuff "<line>\r"` sends the
   line and its Enter in one burst; the trailing `\r` can be absorbed into the
   paste, leaving the line unsubmitted. Fix: `submit()` sends a **separate,
   settled** Enter (`ALOOP_SUBMIT_SETTLE`, default 1s) as its own keystroke after
   each stuffed line. `\r` is confirmed as the submit key.
2. **Duplicate `/clear` on restart.** Restarting without killing the old monitor
   left **two** instances polling the same semaphores → each fired once → the
   agent saw `/clear` twice and the interleaved stuffs mangled the startup prompt.
   Fix: the single-instance lock above.

**Agent-side mitigation (load-bearing):** a control signal (`/clear`, `/context`)
that arrives **mid-turn** lands as a literal user message, not a command — the
agent must treat it as a **no-op** and keep working; the protocol only resets at
a clean turn boundary (the task-done / wound-down handshake). See the
`feedback_autonomous_loop` memory.
