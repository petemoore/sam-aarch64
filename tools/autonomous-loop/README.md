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
3. Agent: follow the resume nudge — it carries the decision rule (the
   authoritative wording is `RESUME_NUDGE` in `monitor.sh`). After picking the
   `ready` tip it takes ONE of three exits: **(1) keep working** (workable
   non-Pete tip, ≥50% free); **(2) context/budget wind-down** — write the
   ROADMAP handover → `touch ~/.claude/autonomous-loop/wound-down` (monitor
   `/clear`s + restarts fresh to keep working, incl. for a large item that wants
   a fresh-context session); **(3) backlog-drained hold** — when *zero* workable
   non-Pete items remain → write the handover → `touch ~/.claude/autonomous-loop/quiescent`.
4. Monitor reacts: on `wound-down`, `/clear` + the startup prompt → clean reset,
   repeat. On `quiescent` (item **i103**), **hold**: stop nudging — no `/clear`,
   no restart — until the session transcript grows (Pete writes back / any new
   turn), then auto-expire the hold and resume normal polling.

The agent never blocks on Pete: questions go in `registry/questions.yaml` (via
`build/registry add --space questions …` then `make registry`) and it keeps
working; it only addresses Pete directly when the whole non-Pete backlog drains —
and then it goes **quiescent** (exit 3) rather than restart-looping. Without the
quiescent hold, a true drain would wind down → `/clear` + restart → re-confirm
the drain → restart again, forever, waking an idle session and burning tokens;
the hold is keyed to transcript growth so it **auto-expires on the first sign of
life** (no manual restart, no permanently-deaf loop). If no transcript can be
found to watch, the monitor declines to hold (deaf-loop insurance) and the
hang-timeout still backstops.
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

`ready` **excludes `owner:pete` (needs-Pete-present) items by default**, so the
tip is always agent-actionable — you never have to skip a hardware/Pete item. When
Pete is around, `ready` auto-includes and prioritises his items when the
**presence marker** `~/.claude/autonomous-loop/pete-present` exists — just
`touch` it at session start and `rm` it when he leaves; no flag needed. The
marker persists across sessions and context resets, so autonomous runs pick it up
without any per-session setup. `--pete-present` and `--pete-away` still work as
before: an explicit flag always overrides the marker (`--pete-away` wins over the
marker; `--pete-present` wins when there is no marker).

The same `pete-present` marker also **gates the monitor itself (i240)**: while it
exists, the monitor **suppresses every nudge/restart** (the
`/context`+resume, the `/clear`+restart, and the hang-timeout nudge) — Pete is
driving the session interactively and the loop must not interrupt him. On each
presence transition the monitor stuffs a one-shot line into the session: an
**arrival** line when the marker appears (`ALOOP_PETE_ARRIVAL`) and a
**departure** line when it is removed (`ALOOP_PETE_DEPARTURE`), so the in-session
agent knows the mode changed. Semaphores are **not consumed** while suppressed, so
a `task-done`/`wound-down` left pending during a present period is processed
normally the moment Pete leaves — no work signal is lost. So `touch
~/.claude/autonomous-loop/pete-present` both surfaces Pete's items in `ready` and
silences the loop; `rm` it to hand control back to autonomous mode.

## Run

```sh
ALOOP_SESSION=<your-screen-session> tools/autonomous-loop/monitor.sh
```

Run it in a **second** screen window; claude runs in window 0 (the `cl` window).
Tunable via env: `ALOOP_WINDOW`, `ALOOP_PROMPT`, `ALOOP_POLL`,
`ALOOP_HANG_TIMEOUT`, `ALOOP_CLEAR_SETTLE`, `ALOOP_CONTEXT_SETTLE`,
`ALOOP_SUBMIT_SETTLE`, `ALOOP_CHUNK_SIZE`, `ALOOP_CHUNK_DELAY`,
`ALOOP_NUDGE_TRIES`, `ALOOP_NUDGE_VERIFY_WAIT`, `ALOOP_TURN_MARKER`,
`ALOOP_IDLE_POLL`, `ALOOP_IDLE_CONFIRM`, `ALOOP_IDLE_MAX`, `ALOOP_LOG`,
`ALOOP_PETE_ARRIVAL`, `ALOOP_PETE_DEPARTURE` (the i240 presence-edge lines)
(see `monitor-nudge-delivery.md`), and — for the i103 quiescent hold —
`ALOOP_PROJECTS_DIR` / `ALOOP_TRANSCRIPT` (where the watcher reads transcript
growth; defaults to the newest `*.jsonl` under `~/.claude/projects`).

**Watch it live:** the monitor traces every action — each stuffed payload, each
submit, every idle-wait and branch decision — to `$ALOOP_LOG` (default
`~/.claude/autonomous-loop/monitor.log`). `tail -f` it to see exactly what the
monitor sent and decided (added in i179, after a dropped nudge left no
inspectable record of what happened).

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
5. **The resume nudge was skipped when the monitor raced the agent's prior turn
   (i179).** The agent touches `task-done` then keeps emitting (e.g. a wind-down
   summary) *before* its turn ends; the monitor entered the task-done branch while
   that turn was still running, and `nudge_until_turn`'s opening
   `if turn_running: return 0` mistook the **still-finishing prior turn** for a
   nudge-induced one — returning "success" without ever sending the nudge.
   `/context` (stuffed first) still rendered, so it looked like #4, but the nudge
   was never attempted. Fix: `wait_for_idle()` gates delivery on **sustained idle**
   (the prior turn must be provably done first) and the first nudge attempt always
   sends. Distinct from and additive to #4; persistent `$ALOOP_LOG` tracing was
   added so the next such issue is visible, not guessed. Details:
   `monitor-nudge-delivery.md`.

**Agent-side mitigation (load-bearing):** a control signal (`/clear`, `/context`)
that arrives **mid-turn** lands as a literal user message, not a command — the
agent must treat it as a **no-op** and keep working; the protocol only resets at
a clean turn boundary (the task-done / wound-down handshake). See the
`feedback_autonomous_loop` memory.
