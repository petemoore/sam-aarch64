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
`ALOOP_HANG_TIMEOUT`, `ALOOP_CLEAR_SETTLE`.

## Status

Core built; **not yet live-tested** — see question-registry **q14** (confirm a
watcher-stuffed `/context` reaches the agent; tune `SUBMIT` — `\r` vs `\n` — and
the settle timing). Until tested, treat as unproven.
