#!/usr/bin/env bash
# check-hosttest-carveouts.sh — the i231 emulation-first ratchet guard.
#
# Policy (CLAUDE.md §7 / feedback_emulation_first): an opening assembler
# directive of the shape
#     if defined(NETBOOT_HOSTTEST)==0          (alone or ANDed with other gates)
# excludes its block from the HOST/emulation build, so that code ships to real
# hardware UN-emulated. That is the recurring anti-pattern this guard exists to
# stop. The fix is SYSTEMIC: the per-file carve-out counts are pinned in
# tools/hosttest-carveout-allowlist.txt (the ratchet ledger) and may only shrink.
#
# This guard counts the live carve-outs per file and EXACT-matches them against
# the ledger, failing on any divergence in either direction:
#   - more than the ledger permits (or an un-listed file) → a NEW carve-out was
#     added; don't (model it in the harness / run it under SimCoupé instead).
#   - fewer than the ledger lists (or a stale zero-actual entry) → a carve-out
#     was eliminated but the ledger wasn't ratcheted down; update the ledger.
#
# It deliberately does NOT count `if defined(NETBOOT_HOSTTEST)` (a host-test-only
# recording double — never reaches hardware) nor `... | defined(NETBOOT_STREAM)`
# (present in both the host harness and the real bootable — the correct pattern).
set -uo pipefail

cd "$(dirname "$0")/.."

allowlist="tools/hosttest-carveout-allowlist.txt"
if [ ! -f "$allowlist" ]; then
  echo "ERROR: ratchet ledger $allowlist not found." >&2
  exit 1
fi

# The anti-pattern: an opening `if` whose condition contains defined(NETBOOT_HOSTTEST)==0.
# Allow arbitrary surrounding whitespace and ANDed gates (e.g. *(defined(DUMPER)==0)).
pattern='^[[:space:]]*if([[:space:]]|\().*defined\(NETBOOT_HOSTTEST\)[[:space:]]*==[[:space:]]*0'

# Live per-file counts, keyed by path.
declare -A live
while IFS= read -r path; do
  [ -n "$path" ] && live["$path"]=$(( ${live["$path"]:-0} + 1 ))
done < <(grep -rnE "$pattern" src/ 2>/dev/null | sed -E 's/:[0-9]+:.*//')

# Ledger per-file counts (skip blanks and #-comments).
declare -A allowed
while read -r count path _rest; do
  [ -z "${count:-}" ] && continue
  case "$count" in \#*) continue;; esac
  if ! [[ "$count" =~ ^[0-9]+$ ]]; then
    echo "ERROR: malformed ledger line in $allowlist: '$count $path'" >&2
    exit 1
  fi
  allowed["$path"]=$count
done < "$allowlist"

fail=0
emit() { echo "  $*" >&2; }

# Every live file must be listed with an EXACTLY matching count.
for path in "${!live[@]}"; do
  l=${live["$path"]}
  a=${allowed["$path"]:-0}
  if [ "$a" -eq 0 ]; then
    fail=1
    emit "NEW carve-out: $path has $l NETBOOT_HOSTTEST==0 block(s), ledger lists none."
  elif [ "$l" -gt "$a" ]; then
    fail=1
    emit "carve-out ADDED: $path has $l, ledger permits $a (the ratchet only goes DOWN)."
  elif [ "$l" -lt "$a" ]; then
    fail=1
    emit "carve-out ELIMINATED (good) but ledger stale: $path has $l, ledger lists $a — ratchet the ledger down to $l."
  fi
done

# Every ledger entry must correspond to a live file (no stale zero-actual lines).
for path in "${!allowed[@]}"; do
  if [ "${live["$path"]:-0}" -eq 0 ]; then
    fail=1
    emit "stale ledger entry: $path lists ${allowed["$path"]} but has no carve-outs — remove the line."
  fi
done

live_total=0
for path in "${!live[@]}"; do live_total=$(( live_total + ${live["$path"]} )); done

if [ "$fail" -ne 0 ]; then
  {
    echo "ERROR: NETBOOT_HOSTTEST==0 carve-out ledger out of sync (i231 emulation-first guard)."
    echo "  Background: a NETBOOT_HOSTTEST==0 block ships its code to hardware un-emulated"
    echo "  (CLAUDE.md §7). Do NOT add one — make the harness model it, or run it under"
    echo "  SimCoupé. Eliminating one is welcome: ratchet $allowlist down to match."
  } >&2
  exit 1
fi

echo "check-hosttest-carveouts: OK — $live_total carve-out(s) across ${#live[@]} file(s), exactly matching the ratchet ledger."
