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
#   2. monitor sees TASK_DONE -> stuffs `/context` -> the readout reaches the
#      agent, which continues to the next item if >=20% free, else winds down
#      (writes its ROADMAP handover) and `touch $WOUND_DOWN`
#   3. monitor sees WOUND_DOWN -> stuffs `/clear` + the startup prompt: a
#      genuine clean reset, re-grounded from ROADMAP (never lossy compaction)
#
set -uo pipefail

# --- configuration (override via env) --------------------------------------
SESSION="${ALOOP_SESSION:-}"               # screen session name (see `screen -ls`); REQUIRED
WINDOW="${ALOOP_WINDOW:-0}"                 # window claude runs in (Pete: 0, the `cl` window)
SEMA_DIR="${ALOOP_SEMA_DIR:-$HOME/.claude/autonomous-loop}"
STARTUP_PROMPT="${ALOOP_PROMPT:-Continue per docs/ROADMAP.md. AUTONOMOUS-LOOP RUN: after you complete each work item (a merged PR), run 'touch ~/.claude/autonomous-loop/task-done' and end your turn -- the monitor will then show you /context; if it reports under 20% free context, write your ROADMAP Current-State handover and then run 'touch ~/.claude/autonomous-loop/wound-down', otherwise pick the next item and continue. Never block on Pete while unblocked work remains -- put any question in the qN registry and keep working. Full protocol: tools/autonomous-loop/README.md.}"
POLL="${ALOOP_POLL:-10}"                    # seconds between polls
HANG_TIMEOUT="${ALOOP_HANG_TIMEOUT:-1800}"  # seconds with no signal -> nudge an idle session
CLEAR_SETTLE="${ALOOP_CLEAR_SETTLE:-2}"     # seconds for /clear to settle before re-prompting
SUBMIT=$'\r'                                # Enter key; if the TUI ignores it, try $'\n'

TASK_DONE="$SEMA_DIR/task-done"
WOUND_DOWN="$SEMA_DIR/wound-down"

log()   { printf '%s  %s\n' "$(date '+%Y-%m-%d %H:%M:%S')" "$*"; }
stuff() { screen -S "$SESSION" -p "$WINDOW" -X stuff "$1$SUBMIT"; }

# --- preflight -------------------------------------------------------------
[ -n "$SESSION" ] || { echo "ERROR: set ALOOP_SESSION to your screen session (see 'screen -ls')." >&2; exit 1; }
command -v screen >/dev/null || { echo "ERROR: 'screen' not found." >&2; exit 1; }
mkdir -p "$SEMA_DIR"
rm -f "$TASK_DONE" "$WOUND_DOWN"            # start from a clean slate
log "monitor up: session='$SESSION' window=$WINDOW poll=${POLL}s hang=${HANG_TIMEOUT}s"
log "semaphores under: $SEMA_DIR"

# --- loop ------------------------------------------------------------------
last_signal=$SECONDS
while true; do
  if [ -e "$WOUND_DOWN" ]; then
    log "WOUND_DOWN -> /clear + restart"
    stuff "/clear"
    sleep "$CLEAR_SETTLE"
    stuff "$STARTUP_PROMPT"
    rm -f "$WOUND_DOWN" "$TASK_DONE"
    last_signal=$SECONDS
  elif [ -e "$TASK_DONE" ]; then
    log "TASK_DONE -> /context (agent decides: continue or wind down)"
    stuff "/context"
    rm -f "$TASK_DONE"
    last_signal=$SECONDS
  elif [ $((SECONDS - last_signal)) -ge "$HANG_TIMEOUT" ]; then
    log "no signal for ${HANG_TIMEOUT}s -> nudge (in case the session went idle)"
    stuff "If you are idle and waiting on nothing, resume autonomously per docs/ROADMAP.md and the autonomous-loop protocol (tools/autonomous-loop/README.md). If you are mid-task, ignore this."
    last_signal=$SECONDS
  fi
  sleep "$POLL"
done
