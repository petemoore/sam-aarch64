#!/usr/bin/env bash
# check-trinity-authority.sh — the i273 Trinity-hardware authority guard.
#
# Enforces the rule (CLAUDE.md "Development discipline" rule 8;
# feedback_port_diff_authority_first): the working Z80/Colin SAM code is the
# AUTHORITY for real Trinity hardware behaviour (EEPROM / SD / ENC28J60 Ethernet /
# the B-DOS storage seam); the Go emulation models are DERIVED FROM and verified
# against it, never the reverse. The recurring rot (i270, i145g, i249/250/251) is a
# Go model being treated as the authority, so emulator-green ships a hardware bug.
#
# The teeth (mirroring check-hosttest-carveouts.sh): a provenance ledger
# (tools/trinity-authority-ledger.txt) maps each Trinity-hardware Go MODEL file to
# its in-repo SAM/Colin authority source. This guard requires:
#   1. every ledger model file exists, and carries the in-source provenance marker
#      (so a reader of the model sees it disclaim being the authority);
#   2. every ledger authority path exists in the repo (the derivation is real);
#   3. the set of files carrying the provenance marker EXACTLY equals the ledger's
#      model set — so a new Trinity-hardware model cannot be marked without a
#      ledger entry, and a stale ledger entry (marker removed) is caught.
#
# Adding a Trinity-hardware model is therefore a deliberate, reviewed act: mark the
# file and name the SAM/Colin authority it is derived from. Run from the repo root.
set -uo pipefail
cd "$(dirname "$0")/.."

ledger="tools/trinity-authority-ledger.txt"
marker="Trinity-HW provenance (i273)"
search_root="tools/netboot-oracle"

if [ ! -f "$ledger" ]; then
    echo "check-trinity-authority: ledger not found: $ledger" >&2
    exit 1
fi

fail=0
emit() { echo "  $*" >&2; }

# --- the ledger's model set + per-entry checks ---------------------------------
declare -A in_ledger=()
while read -r gofile authority _note; do
    case "$gofile" in ''|\#*) continue;; esac
    if [ -z "${authority:-}" ]; then
        emit "MALFORMED ledger line (no authority path): $gofile"
        fail=1
        continue
    fi
    in_ledger["$gofile"]=1
    if [ ! -f "$gofile" ]; then
        emit "MISSING model file named by the ledger: $gofile"
        fail=1
    elif ! grep -qF "$marker" "$gofile"; then
        emit "model file lacks the provenance marker \"$marker\": $gofile"
        fail=1
    fi
    if [ ! -e "$authority" ]; then
        emit "MISSING authority path for $gofile: $authority (the DERIVED-FROM source must exist in-repo)"
        fail=1
    fi
done < "$ledger"

# --- every marker-carrying file must be in the ledger --------------------------
while IFS= read -r marked; do
    [ -z "$marked" ] && continue
    if [ -z "${in_ledger[$marked]:-}" ]; then
        emit "file carries the provenance marker but is NOT in the ledger: $marked"
        emit "  -> add a '$marked <authority>' line to $ledger naming its SAM/Colin authority"
        fail=1
    fi
done < <(grep -rlF "$marker" "$search_root" --include='*.go' 2>/dev/null | sort)

if [ "$fail" -ne 0 ]; then
    echo "" >&2
    echo "check-trinity-authority: FAILED — the Trinity-hardware authority ledger is out of sync." >&2
    echo "The Z80/Colin SAM code is the authority for Trinity hardware; the Go model is" >&2
    echo "derived from it (CLAUDE.md rule 8). Fix the ledger / marker / authority path above." >&2
    exit 1
fi

count=${#in_ledger[@]}
echo "check-trinity-authority: OK — $count Trinity-hardware Go model(s) each derived from a present SAM/Colin authority."
