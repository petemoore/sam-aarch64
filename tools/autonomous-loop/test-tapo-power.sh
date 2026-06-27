#!/usr/bin/env bash
#
# Unit test for the i281 TAPO power-management helpers in monitor.sh. These pure
# functions decide, from the state tapo.sh records + the keep-alive file, how long
# the SAM has been ON and which escalation thresholds are due — the logic that
# drives the monitor's "message the agent / auto-off backstop" behaviour, here
# exercised WITHOUT a real plug. Covers:
#   - tapo_state_is_on    : "on" vs "off"/missing/garbage (safe default = not on);
#   - tapo_on_epoch       : parse the epoch field, 0 on absent/non-numeric;
#   - tapo_effective_on   : max(power-on epoch, keep-alive mtime) — the keep-alive
#                           pushes the deadline forward;
#   - tapo_due_escalations: each threshold fires at most once, multiples at once.
#
# Run: tools/autonomous-loop/test-tapo-power.sh   (exits non-zero on failure)
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# Sourcing monitor.sh defines its functions and returns WITHOUT running the
# monitor loop (the `(return 0 …)` source-guard before its preflight).
# shellcheck source=monitor.sh
source "$here/monitor.sh"

tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
pass() { printf 'PASS: %s\n' "$1"; }
fail() { printf 'FAIL: %s\n' "$1" >&2; exit 1; }
eq() { [ "$1" = "$2" ] || fail "$3 (got '$1', want '$2')"; }

# --- tapo_state_is_on -------------------------------------------------------
sf="$tmp/state"
printf 'on 1782770000\n' >"$sf"
tapo_state_is_on "$sf" && pass "state_is_on: 'on' is on" || fail "state_is_on: 'on' should be on"
printf 'off 1782770000\n' >"$sf"
tapo_state_is_on "$sf" && fail "state_is_on: 'off' should NOT be on" || pass "state_is_on: 'off' is not on"
printf 'garbage\n' >"$sf"
tapo_state_is_on "$sf" && fail "state_is_on: garbage should NOT be on" || pass "state_is_on: garbage is not on"
tapo_state_is_on "$tmp/nonexistent" && fail "state_is_on: missing should NOT be on" || pass "state_is_on: missing is not on"

# --- tapo_on_epoch ----------------------------------------------------------
printf 'on 1782770123\n' >"$sf"
eq "$(tapo_on_epoch "$sf")" "1782770123" "on_epoch: parses the epoch"
printf 'on notanumber\n' >"$sf"
eq "$(tapo_on_epoch "$sf")" "0" "on_epoch: non-numeric -> 0"
eq "$(tapo_on_epoch "$tmp/nonexistent")" "0" "on_epoch: missing -> 0"

# --- tapo_effective_on ------------------------------------------------------
ka="$tmp/keepalive"
# No keep-alive file -> effective-on is just the power-on epoch.
eq "$(tapo_effective_on 1000000 "$tmp/nonexistent")" "1000000" "effective_on: no keep-alive -> on-epoch"
# Keep-alive NEWER than power-on -> effective-on jumps to the keep-alive mtime.
touch "$ka"; ka_mtime=$(stat -c %Y "$ka")
eq "$(tapo_effective_on 1000000 "$ka")" "$ka_mtime" "effective_on: fresh keep-alive wins over old on-epoch"
# Keep-alive OLDER than power-on -> power-on epoch wins (a power-cycle after a
# stale keep-alive must not be held down by it).
eq "$(tapo_effective_on $((ka_mtime + 10000)) "$ka")" "$((ka_mtime + 10000))" "effective_on: newer on-epoch wins over stale keep-alive"

# --- tapo_due_escalations ---------------------------------------------------
# Age 0 -> nothing due.
eq "$(tapo_due_escalations 0 "" 30 60 90 120 150)" "" "due: age 0 -> none"
# Age 35, none announced -> only 30 due.
eq "$(tapo_due_escalations 35 "" 30 60 90 120 150)" "30" "due: age 35 -> 30"
# Age 35 with 30 already announced -> nothing new.
eq "$(tapo_due_escalations 35 "30" 30 60 90 120 150)" "" "due: age 35, 30 announced -> none (once-only)"
# Age 100, none announced (e.g. monitor started late) -> 30 60 90 all due at once.
eq "$(tapo_due_escalations 100 "" 30 60 90 120 150)" "30 60 90" "due: age 100 -> 30 60 90 together"
# Age 100 with 30 60 announced -> only 90 new.
eq "$(tapo_due_escalations 100 "30 60" 30 60 90 120 150)" "90" "due: age 100, 30/60 announced -> 90"
# Age past the last threshold -> all remaining due.
eq "$(tapo_due_escalations 200 "30 60 90" 30 60 90 120 150)" "120 150" "due: age 200, 30/60/90 announced -> 120 150"

echo "ALL TAPO POWER TESTS PASSED"
