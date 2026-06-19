#!/usr/bin/env bash
#
# Autonomous-loop monitor (item i97).
#
# Keeps ONE interactive Claude Code session working across context limits,
# losslessly, by driving it from OUTSIDE via `screen -X stuff`. This is
# deterministic plumbing -- there is no LLM here. The agent runs the proven
# `docs/ROADMAP.md` orchestrator; this script only relays signals and resets
# context cleanly. See README.md (and item-registry i97) for the design.
#
# Protocol (the agent, told via the startup prompt, cooperates):
#   1. agent finishes one work item -> `touch $TASK_DONE` -> ends its turn
#   2. monitor sees TASK_DONE -> stuffs `/context` THEN a short resume nudge.
#      The nudge is load-bearing: `/context` is a *local* Claude Code command --
#      it renders the readout but does NOT invoke the model, so it cannot, by
#      itself, wake the agent for another turn. A real text line is what creates
#      the turn; the agent then reads the (already-rendered) readout and either
#      continues to the next item (>=50% free) or winds down (writes its ROADMAP
#      handover) and `touch $WOUND_DOWN`. The nudge is NOT the startup prompt --
#      the live agent still holds full context and must not re-ground from scratch.
#   3. monitor sees WOUND_DOWN -> stuffs `/clear` + the startup prompt: a
#      genuine clean reset, re-grounded from ROADMAP (never lossy compaction)
#
set -uo pipefail

# --- configuration (override via env) --------------------------------------
SESSION="${ALOOP_SESSION:-}"               # screen session name (see `screen -ls`); REQUIRED
WINDOW="${ALOOP_WINDOW:-0}"                 # window claude runs in (Pete: 0, the `cl` window)
SEMA_DIR="${ALOOP_SEMA_DIR:-$HOME/.claude/autonomous-loop}"
STARTUP_PROMPT="${ALOOP_PROMPT:-Continue per docs/ROADMAP.md. AUTONOMOUS-LOOP RUN: after you complete each work item (a merged PR), run 'touch ~/.claude/autonomous-loop/task-done' and end your turn -- the monitor will then show you /context and a task-done checkpoint nudge that carries the continue-vs-wind-down decision rule -- follow it (the rule lives there, at the decision point, not here). Never block on Pete while unblocked work remains -- put any question in the qN registry and keep working. Full protocol: tools/autonomous-loop/README.md.}"
# RESUME_NUDGE wakes the agent for its next turn AFTER the task-done /context
# readout. It is deliberately short and is NOT the startup prompt: the live agent
# keeps its full session context, so it needs only a real text line (any prompt)
# to take a turn -- re-grounding from ROADMAP here would be wasteful and is only
# for the post-/clear path. Override via env.
RESUME_NUDGE="${ALOOP_RESUME:-Autonomous-loop checkpoint: you just finished a work item; the /context readout above is yours. Decide ONCE, mechanically -- do not re-litigate (re-deciding burns the context this protects). If /context shows under 50% free: write your ROADMAP Current-State handover, then run 'touch ~/.claude/autonomous-loop/wound-down'. Otherwise grep the OPEN registries (docs/notes/item-registry-open.md, question-registry-open.md) for genuinely-unblocked work -- do NOT judge drained from the Current-State curated menu, it has under-counted real OPEN items before. Wind down EVEN above 50% free if the only work left is fresh-session/large-multi-session, BLOCKED:Pete-gated, or bigger than your remaining budget; otherwise pick the nearest unblocked item by ROADMAP order and continue now -- you still hold full session context, so do not re-read ROADMAP from scratch.}"
POLL="${ALOOP_POLL:-10}"                    # seconds between polls
HANG_TIMEOUT="${ALOOP_HANG_TIMEOUT:-1800}"  # seconds with no signal -> nudge an idle session
CLEAR_SETTLE="${ALOOP_CLEAR_SETTLE:-30}"    # seconds for /clear to fully reset the TUI before re-prompting
CONTEXT_SETTLE="${ALOOP_CONTEXT_SETTLE:-5}" # seconds for /context to finish its redraw before stuffing the resume nudge. /context is a full-TUI redraw; a few seconds lets it settle. This is NOT the load-bearing fix for the dropped nudge (that is chunked stuff + verify-and-retry below) -- no fixed sleep reliably wins, because the real cause was the long nudge being stuffed as ONE burst and dropped wholesale, not a render race. See monitor-nudge-delivery.md.
SUBMIT=$'\r'                                # Enter key -- \r confirmed to submit in the Claude Code TUI (live test 2026-06-15)
SUBMIT_SETTLE="${ALOOP_SUBMIT_SETTLE:-1}"   # seconds to let a stuffed line settle before the confirming Enter
CHUNK_SIZE="${ALOOP_CHUNK_SIZE:-64}"        # max chars per `screen -X stuff` burst. A long line stuffed in ONE burst right after a full-TUI redraw (/context) is dropped wholesale by the TUI's paste ingestion -- the i140 root cause (reproduced 0/4 delivered). Chunking into small bursts with a brief inter-chunk pause lets every byte land (reproduced: chunked lands the full line reliably). See monitor-nudge-delivery.md.
CHUNK_DELAY="${ALOOP_CHUNK_DELAY:-0.1}"     # seconds between stuffed chunks (sleep accepts fractional seconds)
NUDGE_TRIES="${ALOOP_NUDGE_TRIES:-4}"       # how many times to (re)send the resume nudge until a turn actually starts
NUDGE_VERIFY_WAIT="${ALOOP_NUDGE_VERIFY_WAIT:-8}" # seconds to poll the screen for a started turn after each nudge attempt
TURN_MARKER="${ALOOP_TURN_MARKER:-esc to interrupt}" # text the Claude Code TUI shows while a model turn is running; our proof the nudge became a turn

TASK_DONE="$SEMA_DIR/task-done"
WOUND_DOWN="$SEMA_DIR/wound-down"
LOCK="$SEMA_DIR/monitor.lock"               # single-instance guard (see preflight)

log()   { printf '%s  %s\n' "$(date '+%Y-%m-%d %H:%M:%S')" "$*"; }

# stuff_raw() sends a string to the session in CHUNK_SIZE-byte bursts, pausing
# CHUNK_DELAY between them. A long string stuffed as ONE `screen -X stuff` burst
# is dropped wholesale by the Claude Code TUI right after a full-screen redraw
# (e.g. /context) -- the i140 root cause, reproduced 0/4 delivered with the real
# 858-char resume nudge. Chunking lets every byte land (reproduced reliably).
# Short strings (a single chunk) behave exactly as a plain stuff did before.
stuff_raw() {
  local s="$1" i
  for (( i=0; i<${#s}; i+=CHUNK_SIZE )); do
    screen -S "$SESSION" -p "$WINDOW" -X stuff "${s:i:CHUNK_SIZE}"
    sleep "$CHUNK_DELAY"
  done
}
# stuff() sends a line and a trailing Enter (chunked). The trailing \r can still
# be absorbed into a long paste, so a turn is not guaranteed -- callers that need
# the line to actually submit follow up with submit() (an explicit settled Enter)
# or, for the load-bearing nudge, nudge_until_turn() (verify-and-retry).
stuff() { stuff_raw "$1"; screen -S "$SESSION" -p "$WINDOW" -X stuff "$SUBMIT"; }
# submit() sends a standalone Enter, AFTER a short settle, as its own keystroke.
# A long line stuffed in one burst can have its trailing \r absorbed into the
# paste (the TUI is still ingesting the text), so the line sits unsubmitted in
# the input -- the failure Pete hit 2026-06-15. Sending a separate, settled Enter
# reliably confirms it. (If the line already submitted, this Enter lands on an
# empty input line, a harmless no-op.) Tune ALOOP_SUBMIT_SETTLE if 1s is tight.
submit() { sleep "$SUBMIT_SETTLE"; screen -S "$SESSION" -p "$WINDOW" -X stuff "$SUBMIT"; }

# turn_running() returns 0 iff a model turn is currently in flight, detected by
# the TURN_MARKER text the TUI shows ("esc to interrupt"). This is the proof a
# stuffed nudge actually became a turn -- not just landed in the input box.
turn_running() {
  local hc; hc="$(mktemp)"
  screen -S "$SESSION" -X hardcopy "$hc" 2>/dev/null
  local found=1
  grep -qF "$TURN_MARKER" "$hc" && found=0
  rm -f "$hc"
  return $found
}

# nudge_until_turn() delivers the load-bearing resume nudge and VERIFIES it
# became a turn, retrying with backoff if not. This is the robust replacement for
# "stuff once + hope": a blind sleep cannot fix the i140 drop because the cause is
# burst-size, not a render race. Each attempt: chunk-stuff the nudge, send a
# settled Enter, then poll the screen up to NUDGE_VERIFY_WAIT seconds for a
# started turn. If a turn started, done. If the text landed but did not submit, a
# bare Enter on the next attempt submits it (re-stuffing an already-present line
# just appends, but the verify loop stops the moment a turn starts, so at most one
# extra Enter is in play). Returns 0 on success, 1 if all tries are exhausted.
nudge_until_turn() {
  local msg="$1" t p
  for (( t=1; t<=NUDGE_TRIES; t++ )); do
    if turn_running; then return 0; fi   # already running (e.g. a prior Enter took)
    if (( t == 1 )); then
      stuff "$msg"                        # first attempt: full nudge + Enter
    else
      # Re-attempt: the nudge text is likely already sitting unsubmitted in the
      # input (landed but the Enter was absorbed). A standalone settled Enter
      # submits it without duplicating the text.
      log "nudge: no turn after attempt $((t-1)); re-sending Enter"
      screen -S "$SESSION" -p "$WINDOW" -X stuff "$SUBMIT"
    fi
    submit                               # belt-and-braces settled Enter
    for (( p=1; p<=NUDGE_VERIFY_WAIT; p++ )); do
      sleep 1
      if turn_running; then
        log "nudge delivered (attempt $t, ${p}s)"
        return 0
      fi
    done
  done
  # Last resort: re-stuff the whole nudge once more in case the input was empty
  # (the text never landed at all on any attempt).
  log "nudge: still no turn after $NUDGE_TRIES tries; re-stuffing nudge from scratch"
  stuff "$msg"; submit
  for (( p=1; p<=NUDGE_VERIFY_WAIT; p++ )); do
    sleep 1
    if turn_running; then log "nudge delivered (final re-stuff, ${p}s)"; return 0; fi
  done
  log "WARNING: resume nudge could not be confirmed as a turn -- the hang-timeout will retry"
  return 1
}

# --- preflight -------------------------------------------------------------
[ -n "$SESSION" ] || { echo "ERROR: set ALOOP_SESSION to your screen session (see 'screen -ls')." >&2; exit 1; }
command -v screen >/dev/null || { echo "ERROR: 'screen' not found." >&2; exit 1; }
mkdir -p "$SEMA_DIR"

# Single-instance guard. TWO monitors driving the same session is the likely
# cause of the duplicate-/clear glitch (Pete, 2026-06-15): restarting without
# killing the old one leaves both polling $SEMA_DIR, so each fires once on the
# same semaphore -> the agent sees /clear twice and the interleaved stuffs mangle
# the startup prompt. Refuse to start if a live monitor holds the lock; replace a
# stale lock (whose PID is gone) and continue.
if [ -e "$LOCK" ] && kill -0 "$(cat "$LOCK" 2>/dev/null)" 2>/dev/null; then
  echo "ERROR: a monitor is already running (PID $(cat "$LOCK")). Kill it first, or remove $LOCK if it is stale." >&2
  exit 1
fi
echo $$ > "$LOCK"
trap 'rm -f "$LOCK"' EXIT

rm -f "$TASK_DONE" "$WOUND_DOWN"            # start from a clean slate
log "monitor up: session='$SESSION' window=$WINDOW poll=${POLL}s hang=${HANG_TIMEOUT}s (pid $$)"
log "semaphores under: $SEMA_DIR"

# --- loop ------------------------------------------------------------------
last_signal=$SECONDS
while true; do
  if [ -e "$WOUND_DOWN" ]; then
    log "WOUND_DOWN -> flush input, /clear, settle ${CLEAR_SETTLE}s, restart"
    # Submit any half-typed line first so /clear starts on a clean input line
    # and can't get merged into a partial human message. (Pete, 2026-06-15.)
    submit
    stuff "/clear"
    submit
    sleep "$CLEAR_SETTLE"
    stuff "$STARTUP_PROMPT"
    submit
    rm -f "$WOUND_DOWN" "$TASK_DONE"
    last_signal=$SECONDS
  elif [ -e "$TASK_DONE" ]; then
    log "TASK_DONE -> /context + resume nudge (agent decides: continue or wind down)"
    stuff "/context"
    submit
    # /context is a LOCAL command -- it renders but does not wake the model.
    # The resume nudge below is the real prompt that actually creates the next
    # turn; without it the agent sits idle until the hang-timeout nudge (~1h),
    # which is exactly the stall this fixes. (Pete's 2026-06-15 live-run log.)
    #
    # Let /context finish its full-TUI redraw before nudging. The load-bearing
    # delivery is nudge_until_turn(): it CHUNK-stuffs the nudge (so the long line
    # is not dropped wholesale after the redraw -- the i140 root cause) and then
    # VERIFIES a turn actually started, retrying if not. A blind sleep alone can
    # never fix this -- the cause is burst-size, not a render race (the old code
    # dropped the real nudge 0/4 even with a 30s settle). See monitor-nudge-delivery.md.
    sleep "$CONTEXT_SETTLE"
    nudge_until_turn "$RESUME_NUDGE"
    rm -f "$TASK_DONE"
    last_signal=$SECONDS
  elif [ $((SECONDS - last_signal)) -ge "$HANG_TIMEOUT" ]; then
    log "no signal for ${HANG_TIMEOUT}s -> nudge (in case the session went idle)"
    stuff "If you are idle and waiting on nothing, resume autonomously per docs/ROADMAP.md and the autonomous-loop protocol (tools/autonomous-loop/README.md). If you are mid-task, ignore this."
    submit
    last_signal=$SECONDS
  fi
  sleep "$POLL"
done
