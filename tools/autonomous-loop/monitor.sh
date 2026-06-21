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
#      the turn; the agent then reads the (already-rendered) readout and takes ONE
#      of three exits (the nudge spells them out): continue to the next item
#      (>=50% free, workable tip), CONTEXT/BUDGET wind-down (`touch $WOUND_DOWN`),
#      or BACKLOG-DRAINED hold (`touch $QUIESCENT`). The nudge is NOT the startup
#      prompt -- the live agent still holds full context and must not re-ground.
#   3. monitor sees WOUND_DOWN -> stuffs `/clear` + the startup prompt: a
#      genuine clean reset, re-grounded from ROADMAP (never lossy compaction)
#   4. monitor sees QUIESCENT -> HOLDS: stops nudging (no /clear, no restart)
#      until the session transcript grows (Pete writes back / any new turn),
#      then auto-expires the hold and resumes normal polling. This stops the
#      drained-loop waste: without it, a true drain winds down -> /clear+restart
#      -> re-confirms the drain -> restarts again, forever (item i103).
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
RESUME_NUDGE="${ALOOP_RESUME:-Autonomous-loop checkpoint: you just finished a work item; the /context readout above is yours. Decide ONCE, mechanically -- do not re-litigate (re-deciding burns the context this protects). Pull the next item from 'build/registry ready' (run from the repo root) -- the priority-queue tip is authoritative; do NOT judgment-pick a lower item or grep the markdown views to improvise. If the tip turns out to be blocked by something not yet tracked as a depends_on edge, that is a missing-edge bug: add the edge ('build/registry dep add --id <tip> --on <blocker>', creating the blocker item first if it does not exist) -- the queue auto-reshuffles -- then pull 'ready' again, repeating until you get a genuinely workable tip. Then choose ONE exit by REASON. (1) WORK THE TIP NOW if you have a genuinely workable non-Pete tip AND /context shows at least 50% free -- you still hold full session context, so do not re-read ROADMAP from scratch. (2) CONTEXT/BUDGET WIND-DOWN if a workable non-Pete tip exists but /context shows under 50% free, or the tip is bigger than your remaining budget: write your ROADMAP Current-State handover, then 'touch ~/.claude/autonomous-loop/wound-down' -- the monitor /clears and restarts you FRESH to keep working (a large/multi-session item is still workable: take this exit to get a fresh-context session for it -- it is NOT a drain). (3) BACKLOG-DRAINED HOLD if ZERO workable non-Pete items remain -- every ready item is Pete/hardware-gated or deferred/speculative-pending-a-decision-you-cannot-make, and any genuine missing-edge is now tracked: write your handover, then 'touch ~/.claude/autonomous-loop/quiescent' -- the monitor HOLDS (stops nudging) until the transcript grows (Pete writes back), so it will not wastefully restart you just to re-confirm the drain. If both (3) and low context apply, prefer quiescent. Note: 'build/registry ready' auto-includes owner:pete items when the presence marker (~/.claude/autonomous-loop/pete-present) exists -- no flag needed.}"
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
# Idle-gating (i179). The agent touches task-done then ENDS its turn, but ending
# is NOT instant -- it may keep emitting (e.g. a wind-down summary). Delivering
# /context + the nudge while that prior turn is still running makes turn_running
# ambiguous: nudge_until_turn's old "if turn_running: return 0" treated the still-
# running PRIOR turn as nudge-success and never sent the nudge -> the agent
# stalled (Pete, 2026-06-20). wait_for_idle() gates delivery on SUSTAINED idle
# first; IDLE_CONFIRM consecutive idle reads (not one) avoid mistaking a brief gap
# between an agent's tool calls for end-of-turn. This is distinct from (and
# additive to) the i140 long-burst paste-drop fix.
IDLE_POLL="${ALOOP_IDLE_POLL:-2}"           # seconds between idle checks
IDLE_CONFIRM="${ALOOP_IDLE_CONFIRM:-3}"     # consecutive idle reads required to declare the prior turn ended
IDLE_MAX="${ALOOP_IDLE_MAX:-300}"           # cap on waiting for idle before proceeding anyway
LOGFILE="${ALOOP_LOG:-$SEMA_DIR/monitor.log}" # persistent trace: every stuff/submit/turn-check/branch. `tail -f` it to watch the loop live.

TASK_DONE="$SEMA_DIR/task-done"
WOUND_DOWN="$SEMA_DIR/wound-down"
QUIESCENT="$SEMA_DIR/quiescent"             # backlog-drained hold (i103): agent touches it when ZERO workable non-Pete items remain; monitor stops nudging until the transcript grows (Pete writes back)
PETE_PRESENT="$SEMA_DIR/pete-present"       # presence semaphore (i240): while it exists Pete is driving interactively -- suppress ONLY the time-based hang nudge (file-driven task-done/wound-down/quiescent stay live); announce arrival/departure on the transition edges
LOCK="$SEMA_DIR/monitor.lock"               # single-instance guard (see preflight)

# i240 announcement lines, stuffed once on the presence transition edges.
PETE_ARRIVAL_MSG="${ALOOP_PETE_ARRIVAL:-Hi Claude, Pete here -- I am back. (Autonomous-loop nudges are suppressed while I am present; carry on, I will steer.)}"
PETE_DEPARTURE_MSG="${ALOOP_PETE_DEPARTURE:-Pete has left (his presence flag was removed). Please continue working autonomously per docs/ROADMAP.md and the autonomous-loop protocol.}"
# PROJECTS_DIR holds the Claude Code session transcripts (one .jsonl per
# session, appended to live). The quiescence watcher (i103) measures the active
# transcript's size to detect "the transcript grew" = a new turn = a sign of
# life (Pete wrote back), which auto-expires the hold. Override the whole path
# with ALOOP_TRANSCRIPT to pin one file.
PROJECTS_DIR="${ALOOP_PROJECTS_DIR:-$HOME/.claude/projects}"

# --- i147: structural stop-after-merge -------------------------------------
# The context protection (touch task-done -> /context checkpoint -> fresh turn)
# relies on the agent CHOOSING to stop after each merged PR -- the easiest step to
# skip mid-flow (2026-06-20 a session never stopped and accumulated ~10 merged PRs
# into one polluted context). This makes the checkpoint STRUCTURAL: the monitor
# watches the main branch for newly-landed PR merge commits and, if one appears
# with no checkpoint pending, synthesizes task-done itself -- so the agent gets
# the /context checkpoint even when it forgot to stop. The watch is read-only (a
# throttled `git fetch` of the watched ref + rev-parse); it never touches the
# working tree, HEAD, or local branches, so it is safe alongside the agent's own
# git work in the same checkout. Only 2-parent merge commits count -- single-parent
# direct doc-only pushes (which this project lands straight on main) are ignored.
MERGE_WATCH="${ALOOP_MERGE_WATCH:-1}"          # 1 = enforce stop-after-merge; 0 = off
REPO="${ALOOP_REPO:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." 2>/dev/null && pwd)}"
MERGE_REMOTE="${ALOOP_MERGE_REMOTE:-origin}"
MERGE_BRANCH="${ALOOP_MERGE_BRANCH:-main}"
MERGE_REF="${ALOOP_MERGE_REF:-$MERGE_REMOTE/$MERGE_BRANCH}"   # the ref new_merges_since reads
MERGE_CHECK_INTERVAL="${ALOOP_MERGE_CHECK_INTERVAL:-120}"    # seconds between merge-watch fetches

# log() timestamps to stdout AND appends to $LOGFILE so the trace survives the
# session -- the live stall that motivated i179 left no inspectable record. Watch
# with `tail -f "$LOGFILE"`. (Pete asked for exactly this visibility, 2026-06-20.)
log() {
  local line; line="$(date '+%Y-%m-%d %H:%M:%S')  $*"
  printf '%s\n' "$line"
  printf '%s\n' "$line" >>"$LOGFILE" 2>/dev/null || true
}
# preview() renders a stuffed payload for the trace: newlines as literal \n, capped.
preview() { local s="${1//$'\n'/\\n}"; printf '%s' "${s:0:70}"; }

# transcript_size() echoes the byte size of the live session transcript -- the
# Claude Code .jsonl the session appends to on every turn. The quiescence
# watcher (i103) uses it to detect "the transcript grew" (Pete wrote back / any
# new turn ran), which auto-expires the backlog-drained hold so the loop is
# never permanently deaf. Honours ALOOP_TRANSCRIPT if set; otherwise picks the
# most-recently-modified *.jsonl under PROJECTS_DIR (the active session is the
# one being written). Echoes 0 when none can be found -- the caller treats a
# 0 baseline as "cannot watch" and declines to hold (deaf-loop insurance).
transcript_size() {
  local f=""
  if [ -n "${ALOOP_TRANSCRIPT:-}" ] && [ -f "$ALOOP_TRANSCRIPT" ]; then
    f="$ALOOP_TRANSCRIPT"
  else
    f="$(find "$PROJECTS_DIR" -maxdepth 2 -name '*.jsonl' -printf '%T@ %p\n' 2>/dev/null | sort -rn | head -1 | cut -d' ' -f2-)"
  fi
  if [ -n "$f" ] && [ -f "$f" ]; then
    wc -c <"$f" 2>/dev/null | tr -d ' '
  else
    echo 0
  fi
}

# new_merges_since() echoes the MERGE-commit SHAs (2+ parents) that landed on
# $ref after $since -- i.e. PRs merged since we last looked. Single-parent commits
# (direct doc-only pushes, which this project lands straight on main) are NOT merge
# commits and are correctly ignored, so only a real PR landing triggers the
# structural checkpoint (i147). Empty output = no new PR merge. Pure and
# side-effect-free (it does no fetch), so it is unit-testable against a local ref
# -- see test-merge-detection.sh.
new_merges_since() {
  local repo="$1" since="$2" ref="$3"
  git -C "$repo" rev-list --merges "${since}..${ref}" 2>/dev/null
}

# fetch_main_sha() quietly updates the watched remote ref and echoes its current
# SHA (empty on failure, e.g. offline -- the caller then makes no decision). The
# fetch only updates refs/remotes/<remote>/<branch>; it never touches the working
# tree, HEAD, or local branches, so it is safe to run while the agent works in the
# same checkout. Plain `git` (not the `g` faketime wrapper) -- fetch/rev-parse
# create no commits, so timestamps are irrelevant here.
fetch_main_sha() {
  git -C "$REPO" fetch -q "$MERGE_REMOTE" "$MERGE_BRANCH" 2>/dev/null
  git -C "$REPO" rev-parse "$MERGE_REF" 2>/dev/null
}

# stuff_raw() sends a string to the session in CHUNK_SIZE-byte bursts, pausing
# CHUNK_DELAY between them. A long string stuffed as ONE `screen -X stuff` burst
# is dropped wholesale by the Claude Code TUI right after a full-screen redraw
# (e.g. /context) -- the i140 root cause, reproduced 0/4 delivered with the real
# 858-char resume nudge. Chunking lets every byte land (reproduced reliably).
# Short strings (a single chunk) behave exactly as a plain stuff did before.
stuff_raw() {
  local s="$1" i
  log "→ stuff ${#s}B in $(( (${#s}+CHUNK_SIZE-1)/CHUNK_SIZE )) chunk(s): \"$(preview "$s")\""
  for (( i=0; i<${#s}; i+=CHUNK_SIZE )); do
    screen -S "$SESSION" -p "$WINDOW" -X stuff "${s:i:CHUNK_SIZE}"
    sleep "$CHUNK_DELAY"
  done
}
# stuff() sends a line and a trailing Enter (chunked). The trailing \r can still
# be absorbed into a long paste, so a turn is not guaranteed -- callers that need
# the line to actually submit follow up with submit() (an explicit settled Enter)
# or, for the load-bearing nudge, nudge_until_turn() (verify-and-retry).
stuff() { stuff_raw "$1"; log "→ Enter (trailing, in-burst)"; screen -S "$SESSION" -p "$WINDOW" -X stuff "$SUBMIT"; }
# submit() sends a standalone Enter, AFTER a short settle, as its own keystroke.
# A long line stuffed in one burst can have its trailing \r absorbed into the
# paste (the TUI is still ingesting the text), so the line sits unsubmitted in
# the input -- the failure Pete hit 2026-06-15. Sending a separate, settled Enter
# reliably confirms it. (If the line already submitted, this Enter lands on an
# empty input line, a harmless no-op.) Tune ALOOP_SUBMIT_SETTLE if 1s is tight.
submit() { sleep "$SUBMIT_SETTLE"; log "→ submit (settled Enter)"; screen -S "$SESSION" -p "$WINDOW" -X stuff "$SUBMIT"; }

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

# wait_for_idle() blocks until NO turn has been running for IDLE_CONFIRM
# consecutive checks (sustained idle), or IDLE_MAX seconds elapse. This is the
# i179 fix: the agent touches task-done then ENDS its turn, but ending is not
# instant -- it may keep emitting (a summary), and 'esc to interrupt' can briefly
# clear between an agent's tool calls -- so we require SUSTAINED idle, not one
# reading. Gating delivery on real idle makes every later turn_running transition
# unambiguously OUR doing (the nudge), not the tail of the prior turn. Arg is a
# label for the trace. Always returns 0 (proceeds even on timeout, logging it).
wait_for_idle() {
  local what="${1:-session}" waited=0 idle=0
  log "wait_for_idle($what): need ${IDLE_CONFIRM}×idle @ ${IDLE_POLL}s (max ${IDLE_MAX}s)"
  while (( waited < IDLE_MAX )); do
    if turn_running; then
      (( idle > 0 )) && log "wait_for_idle($what): turn re-detected at ${waited}s, resetting"
      idle=0
    else
      idle=$(( idle + 1 ))
    fi
    if (( idle >= IDLE_CONFIRM )); then
      log "wait_for_idle($what): idle confirmed after ${waited}s"
      return 0
    fi
    sleep "$IDLE_POLL"; waited=$(( waited + IDLE_POLL ))
  done
  log "wait_for_idle($what): WARNING -- turn still detected after ${IDLE_MAX}s; proceeding anyway"
  return 0
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
  # Callers MUST gate with wait_for_idle first (i179): at entry no turn is running,
  # so the only turn that can appear after we send is the nudge's. The first
  # attempt therefore ALWAYS sends -- never short-circuit on turn_running before
  # sending (the old bug: a still-running prior turn was read as nudge-success and
  # the nudge was never sent). The turn_running short-circuit is for RE-attempts
  # only, where a prior Enter may already have submitted the text.
  log "nudge_until_turn: delivering resume nudge (${#msg}B), up to $NUDGE_TRIES tries"
  for (( t=1; t<=NUDGE_TRIES; t++ )); do
    if (( t > 1 )) && turn_running; then log "nudge: a prior Enter already started the turn"; return 0; fi
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

# When this file is SOURCED (e.g. by test-merge-detection.sh) expose the functions
# above without running the monitor; executing it directly falls through to the
# preflight + loop below. `(return 0 …)` succeeds only in a sourced context.
(return 0 2>/dev/null) && return 0

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

rm -f "$TASK_DONE" "$WOUND_DOWN" "$QUIESCENT" # start from a clean slate
log "monitor up: session='$SESSION' window=$WINDOW poll=${POLL}s hang=${HANG_TIMEOUT}s (pid $$)"
log "semaphores under: $SEMA_DIR"

# --- loop ------------------------------------------------------------------
last_signal=$SECONDS
quiescent_mark=""                           # transcript size when quiescence began; empty = not holding (i103)
# i240: seed the presence edge-detector from the CURRENT state so a monitor
# (re)started while Pete is already present does NOT spuriously announce arrival.
pete_was=""; [ -e "$PETE_PRESENT" ] && pete_was="1"
log "i240: pete-present at startup: ${pete_was:-no}"
# i147: seed the structural-merge-checkpoint watch from the CURRENT main tip so a
# merge that already landed before the monitor started does NOT trigger a spurious
# checkpoint.
last_main_sha=""; last_merge_check=$SECONDS
if [ "$MERGE_WATCH" = "1" ]; then
  last_main_sha="$(fetch_main_sha)"
  log "i147: merge-watch on; REPO=$REPO ref=$MERGE_REF seed=${last_main_sha:-<none>} interval=${MERGE_CHECK_INTERVAL}s"
else
  log "i147: merge-watch off (ALOOP_MERGE_WATCH=0)"
fi
while true; do
  # i240 -- Pete-presence gate. While the pete-present semaphore exists, Pete is
  # driving the session interactively. Suppress ONLY the time-based HANG-timeout
  # nudge (the "if you are idle, resume" line that fires on a timer regardless of
  # what Pete is doing -- THAT is what garbles his typing; gated in the HANG branch
  # below). The FILE-DRIVEN transitions stay live even while Pete is present, because
  # they are agent-initiated, not unsolicited interrupts: TASK_DONE (the agent wrote
  # task-done -> /context + checkpoint), WOUND_DOWN (the agent wrote wound-down ->
  # /clear + restart), QUIESCENT (the agent declared the backlog drained). So Pete can
  # still trigger a wind-down without first marking himself away (Pete, 2026-06-25).
  # On each presence transition edge, stuff a one-shot arrival/departure line.
  pete_now=""; [ -e "$PETE_PRESENT" ] && pete_now="1"
  if [ "$pete_now" != "$pete_was" ]; then
    if [ -n "$pete_now" ]; then
      log "i240: pete-present appeared -> announce arrival; hang-nudge suppressed (file-driven transitions stay live)"
      stuff "$PETE_ARRIVAL_MSG"; submit
    else
      log "i240: pete-present removed -> announce departure; hang-nudge resumes"
      stuff "$PETE_DEPARTURE_MSG"; submit
      last_signal=$SECONDS   # fresh hang-timeout baseline so departure doesn't instantly hang-nudge
    fi
    pete_was="$pete_now"
  fi
  # i147 -- structural stop-after-merge. Throttled, read-only watch of the main
  # branch: when a PR merge commit lands with no checkpoint already pending (and
  # Pete is away -- when he is present he is steering, so we don't force a
  # checkpoint), synthesize task-done so the agent gets the /context checkpoint
  # even if it forgot to stop. The TASK_DONE branch below then waits for the
  # current turn to end before delivering, so the agent finishes whatever it is
  # mid-flow on; the merge is marked accounted for there (and here), so a single
  # merge cannot double-fire. Direct doc-only pushes (single-parent) never trigger.
  if [ "$MERGE_WATCH" = "1" ] && (( SECONDS - last_merge_check >= MERGE_CHECK_INTERVAL )); then
    last_merge_check=$SECONDS
    cur_main_sha="$(fetch_main_sha)"
    if [ -n "$cur_main_sha" ]; then
      if [ -z "$last_main_sha" ]; then
        last_main_sha="$cur_main_sha"                     # first successful read: just seed, never fire
      elif [ "$cur_main_sha" != "$last_main_sha" ]; then
        merges="$(new_merges_since "$REPO" "$last_main_sha" "$MERGE_REF")"
        last_main_sha="$cur_main_sha"
        if [ -n "$merges" ] && [ -z "$pete_now" ] \
           && [ ! -e "$TASK_DONE" ] && [ ! -e "$WOUND_DOWN" ] && [ ! -e "$QUIESCENT" ]; then
          log "i147: PR merge landed on $MERGE_REF with no checkpoint pending -> synthesizing task-done (structural stop-after-merge): $(printf '%s' "$merges" | tr '\n' ' ')"
          touch "$TASK_DONE"
        fi
      fi
    fi
  fi
  # If $QUIESCENT vanished by any path (auto-expire below, or an external rm),
  # forget the stale baseline so a future hold re-marks from scratch.
  [ -e "$QUIESCENT" ] || quiescent_mark=""
  if [ -e "$QUIESCENT" ]; then
    # Backlog-drained hold (i103). The agent confirmed ZERO workable non-Pete
    # items remain and touched $QUIESCENT (instead of $WOUND_DOWN), so a /clear
    # + restart would only re-confirm the drain -- a wasteful loop. Hold here,
    # suppressing the hang-timeout nudge, until the transcript grows (Pete writes
    # back / any new turn), then auto-expire.
    if [ -z "$quiescent_mark" ]; then
      # First sight: let the wind-down turn that wrote $QUIESCENT fully settle so
      # its own writes are already in the transcript, THEN snapshot the size as
      # the baseline. Marking it now (not from the file's mtime, and only after
      # idle) is what stops the wind-down turn's own growth from counting as
      # "new activity" and spuriously re-waking (the i103 design note).
      wait_for_idle "pre-quiescent"
      quiescent_mark="$(transcript_size)"
      if [ "$quiescent_mark" = "0" ]; then
        # No transcript to watch -> we cannot detect a wake, and holding would
        # risk a permanently-deaf loop. Decline to hold: drop the semaphore and
        # fall back to normal behaviour (the hang-timeout still backstops).
        log "QUIESCENT: WARNING -- no session transcript found to watch; NOT holding (deaf-loop insurance). Removing $QUIESCENT."
        rm -f "$QUIESCENT"
        quiescent_mark=""
        last_signal=$SECONDS
      else
        log "QUIESCENT: backlog drained; holding (transcript mark=${quiescent_mark}B). Nudging suppressed until the transcript grows (Pete writes back)."
      fi
    else
      now_size="$(transcript_size)"
      if [ "$now_size" -gt "$quiescent_mark" ] 2>/dev/null; then
        log "QUIESCENT: transcript grew ${quiescent_mark}B -> ${now_size}B (sign of life) -> exiting hold, resuming normal polling"
        rm -f "$QUIESCENT"
        quiescent_mark=""
        last_signal=$SECONDS    # the live turn (Pete's message) handles itself; don't nudge over it
      fi
      # else: still drained and idle -- do nothing (this is the whole point).
    fi
  elif [ -e "$WOUND_DOWN" ]; then
    log "WOUND_DOWN detected"
    wait_for_idle "pre-clear"   # same race as TASK_DONE: never /clear over a still-running turn
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
    # i147: a wind-down clears the slate -- refresh the merge-watch baseline so any
    # merge that landed before the restart can't trigger a checkpoint afterwards.
    if [ "$MERGE_WATCH" = "1" ]; then merge_synced="$(fetch_main_sha)"; [ -n "$merge_synced" ] && last_main_sha="$merge_synced"; fi
    last_signal=$SECONDS
  elif [ -e "$TASK_DONE" ]; then
    log "TASK_DONE detected"
    # i179: the agent touches task-done then ENDS its turn, but ending is not
    # instant (it may keep emitting, e.g. a summary). Deliver only once that prior
    # turn has genuinely ended -- otherwise turn_running is ambiguous and the nudge
    # is skipped/falsely-confirmed, and the agent stalls (Pete, 2026-06-20).
    wait_for_idle "pre-context"
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
    # i147: this checkpoint accounts for whatever merge prompted it (the agent's own
    # task-done, or a merge-watch synthesis) -- refresh the watch baseline so the
    # same merge can't synthesize a SECOND task-done on the next merge-check.
    if [ "$MERGE_WATCH" = "1" ]; then merge_synced="$(fetch_main_sha)"; [ -n "$merge_synced" ] && last_main_sha="$merge_synced"; fi
    last_signal=$SECONDS
  elif [ -z "$pete_now" ] && [ $((SECONDS - last_signal)) -ge "$HANG_TIMEOUT" ]; then
    # i240: the time-based hang nudge fires ONLY when Pete is away. While he is
    # present this is suppressed (it is the unsolicited interrupt that garbles his
    # typing); the file-driven TASK_DONE/WOUND_DOWN/QUIESCENT branches above still run.
    log "no signal for ${HANG_TIMEOUT}s -> nudge (in case the session went idle)"
    stuff "If you are idle and waiting on nothing, resume autonomously per docs/ROADMAP.md and the autonomous-loop protocol (tools/autonomous-loop/README.md). If you are mid-task, ignore this."
    submit
    last_signal=$SECONDS
  fi
  sleep "$POLL"
done
