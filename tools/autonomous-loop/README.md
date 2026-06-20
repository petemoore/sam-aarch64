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
2. Monitor stuffs `/context` **then a short resume nudge**. The nudge is required:
   `/context` is a *local* command — it renders the readout but does not invoke
   the model, so it can't wake the agent on its own; the nudge (a real text line,
   *not* the startup prompt) creates the next turn so the agent reads the readout
   and acts.
3. Agent: follow the resume nudge — it carries the continue-vs-wind-down
   decision rule (the authoritative wording is `RESUME_NUDGE` in `monitor.sh`;
   on wind-down, write the ROADMAP handover → `touch ~/.claude/autonomous-loop/wound-down`).
4. Monitor stuffs `/clear` + the startup prompt → clean reset. Repeat.

The agent never blocks on Pete: questions go in `registry/questions.yaml` (via
`build/registry add --space questions …` then `make registry`) and it keeps
working; it only addresses Pete directly when the whole non-Pete backlog drains.
Work is pulled from `build/registry ready` (returns the top unblocked open item
from the priority queue in `registry/priority.yaml`; generated view:
`docs/notes/backlog.md`). The YAML is the source of truth — never hand-edit
the generated `docs/notes/*-registry-*.md` files.

**Pull the tip; never judgment-pick.** The `ready` tip is authoritative — do not
skip it for a lower item, and do not grep the markdown views to improvise an
ordering. If the tip turns out to be blocked by something *not yet tracked* as a
`depends_on` edge, that is a **missing-edge bug** (the model has no `BLOCKED`
status — all blocking is an edge). Fix it in place: ensure the blocker is a
tracked item (create it with `build/registry add …` if absent — `--owner pete`
if it is hardware/Pete-gated), then `build/registry dep add --id <tip> --on
<blocker>`. The CLI auto-repairs the priority order (topological repair), so
re-running `build/registry ready` now surfaces a genuinely workable tip. Repeat
until you get one. Run the CLI from the repo root so it operates on the live
registry, not the bundled test fixtures.

## Run

```sh
ALOOP_SESSION=<your-screen-session> tools/autonomous-loop/monitor.sh
```

Run it in a **second** screen window; claude runs in window 0 (the `cl` window).
Tunable via env: `ALOOP_WINDOW`, `ALOOP_PROMPT`, `ALOOP_POLL`,
`ALOOP_HANG_TIMEOUT`, `ALOOP_CLEAR_SETTLE`, `ALOOP_CONTEXT_SETTLE`,
`ALOOP_SUBMIT_SETTLE`, `ALOOP_CHUNK_SIZE`, `ALOOP_CHUNK_DELAY`,
`ALOOP_NUDGE_TRIES`, `ALOOP_NUDGE_VERIFY_WAIT`, `ALOOP_TURN_MARKER`
(see `monitor-nudge-delivery.md`).

**Run exactly one monitor per session.** A single-instance lock
(`$SEMA_DIR/monitor.lock`) makes a second monitor refuse to start; if you
restart, the old one must be dead first (the lock auto-clears on a clean exit,
and a stale lock whose PID is gone is replaced). Two monitors driving one session
is what produced the duplicate-`/clear` glitch below.

## Status

Core built; **first live test 2026-06-15** (Pete), three failure modes found and
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
3. **`/context` alone never resumed the agent (the ~1-item/hour stall).** The
   task-done branch stuffed only `/context` — a *local* command that renders the
   readout but does **not** invoke the model — so the agent sat idle after every
   item until the hourly hang-timeout nudge (a real prompt) kicked it. The 8-hour
   live log showed it unmistakably: every `TASK_DONE -> /context` was followed by
   ~60 min of silence, then a `nudge`, then the next item. Fix: the task-done
   branch now stuffs `/context` **then a short resume nudge** (`ALOOP_RESUME`) —
   a real text line that creates the next turn immediately. The nudge is *not*
   the startup prompt: the live agent keeps full context and must not re-ground.
4. **The long resume nudge was dropped wholesale after `/context` (i136/i140).**
   The nudge never became a turn, so the agent sat idle until the ~1h
   hang-timeout. The real cause is **not** a render race a longer sleep can win:
   a long string stuffed as ONE `screen -X stuff` burst right after `/context`'s
   full-TUI redraw is swallowed by the TUI's paste ingestion (reproduced 0/4
   delivered with the real 858-char nudge). Fix: `stuff` now **chunks** every
   payload into small bursts, and the task-done branch uses `nudge_until_turn`,
   which **verifies a turn actually started** (polls the screen for the
   `esc to interrupt` marker) and **retries** if not. Mechanism + reproduction:
   `monitor-nudge-delivery.md`.

**Agent-side mitigation (load-bearing):** a control signal (`/clear`, `/context`)
that arrives **mid-turn** lands as a literal user message, not a command — the
agent must treat it as a **no-op** and keep working; the protocol only resets at
a clean turn boundary (the task-done / wound-down handshake). See the
`feedback_autonomous_loop` memory.
