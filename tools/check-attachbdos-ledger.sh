#!/usr/bin/env bash
# check-attachbdos-ledger.sh — the i311 AttachBDOS mock-retirement ratchet guard.
#
# Policy (CLAUDE.md rule 8 / feedback_trinity_sd_write_settled_truths): the
# AttachBDOS Go mock (tools/netboot-oracle/z80/bdos_store.go) substitutes for
# real B-DOS and has hidden real write-path failures (i274 axis A). B-DOS
# behaviour must be exercised against the faithful rig (real Z80 B-DOS in
# emulation), never this mock. The fix is SYSTEMIC: the per-file `.AttachBDOS(`
# call-site counts are pinned in tools/attachbdos-ledger.txt (the ratchet
# ledger) and may only shrink toward zero (migration is i311b, gated on i254).
#
# This guard counts the live `.AttachBDOS(` call sites per _test.go file and
# EXACT-matches them against the ledger, failing on any divergence in either
# direction:
#   - more than the ledger permits (or an un-listed file) → a NEW mock use was
#     added; don't (use the faithful rig instead).
#   - fewer than the ledger lists (or a stale entry) → a use was migrated but the
#     ledger wasn't ratcheted down; update the ledger.
#
# It counts CALL sites (`.AttachBDOS(`), so the method definition in bdos_store.go
# (`func (mac *Machine) AttachBDOS(`) is not counted — it is the mock itself,
# deleted by i311b once the ledger reaches zero.
set -uo pipefail

cd "$(dirname "$0")/.."

ledger="tools/attachbdos-ledger.txt"
if [ ! -f "$ledger" ]; then
  echo "ERROR: ratchet ledger $ledger not found." >&2
  exit 1
fi

# Live per-file counts of `.AttachBDOS(` call sites in _test.go files under the
# netboot-oracle module (the only place the mock is used).
declare -A live
while IFS=: read -r path _rest; do
  [ -n "$path" ] || continue
  live["$path"]=$(( ${live["$path"]:-0} + 1 ))
done < <(grep -rnE '\.AttachBDOS\(' --include='*_test.go' tools/netboot-oracle 2>/dev/null)

# Ledger per-file counts (skip blanks and #-comments).
declare -A allowed
while read -r count path _rest; do
  [ -z "${count:-}" ] && continue
  case "$count" in \#*) continue;; esac
  if ! [[ "$count" =~ ^[0-9]+$ ]]; then
    echo "ERROR: malformed ledger line in $ledger: '$count $path'" >&2
    exit 1
  fi
  allowed["$path"]=$count
done < "$ledger"

fail=0
emit() { echo "  $*" >&2; }

# Every live file must be listed with an EXACTLY matching count.
for path in "${!live[@]}"; do
  l=${live["$path"]}
  a=${allowed["$path"]:-0}
  if [ "$a" -eq 0 ]; then
    fail=1
    emit "NEW mock use: $path has $l .AttachBDOS( call site(s), ledger lists none."
  elif [ "$l" -gt "$a" ]; then
    fail=1
    emit "mock use ADDED: $path has $l, ledger permits $a (the ratchet only goes DOWN)."
  elif [ "$l" -lt "$a" ]; then
    fail=1
    emit "mock use MIGRATED (good) but ledger stale: $path has $l, ledger lists $a — ratchet the ledger down to $l."
  fi
done

# Every ledger entry must correspond to a live file (no stale entries).
for path in "${!allowed[@]}"; do
  if [ "${live["$path"]:-0}" -eq 0 ]; then
    fail=1
    emit "stale ledger entry: $path lists ${allowed["$path"]} but has no .AttachBDOS( call sites — remove the line."
  fi
done

live_total=0
for path in "${!live[@]}"; do live_total=$(( live_total + ${live["$path"]} )); done

if [ "$fail" -ne 0 ]; then
  {
    echo "ERROR: AttachBDOS mock-use ledger out of sync (i311 mock-retirement guard)."
    echo "  Background: the AttachBDOS mock substitutes for real B-DOS and has hidden"
    echo "  real write-path failures (CLAUDE.md rule 8). Do NOT add a mock-based B-DOS"
    echo "  test — use the faithful rig (real Z80 B-DOS). Migrating one away is welcome:"
    echo "  ratchet $ledger down to match (migration is i311b, gated on i254)."
  } >&2
  exit 1
fi

echo "check-attachbdos-ledger: OK — $live_total AttachBDOS mock use(s) across ${#live[@]} file(s), exactly matching the ratchet ledger."
