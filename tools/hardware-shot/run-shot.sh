#!/bin/bash
# run-shot.sh — one self-serve Trinity hardware shot for the disk-record WRQ push.
#
# Encodes the proven recipe (docs/ROADMAP.md §8d): power-cycle the SAM via the TAPO
# plug, push a NETBOOT_DEBUG serve binary to the auto-booted trinload, drive a
# disk-record WRQ push with curl, capture the UDP:9001 step-markers, then power off.
# The markers localize how far the push got (the LAST marker before it stalls); see
# listen-markers.py for the DBG_* decode and src/netboot/dbg_marker.asm for the source.
#
# The SAM-deploy steps (trinpush-serve, the tftp:// curl) are gated by the deploy-guard
# hook (i252): run this script with DEPLOY_CHECKED=1 in the environment, after confirming
# the hardware-readiness checklist for the binary being pushed.
#
# Usage:
#   DEPLOY_CHECKED=1 tools/hardware-shot/run-shot.sh [BIN] [MAP] [PAYLOAD] [IP]
# Defaults: the debug serve binary, a 1.5 KB synthetic .mgt, the SAM at 192.168.2.75.
set -uo pipefail
cd "$(dirname "$0")/../.."

BIN="${1:-build/netboot_serve_boot_debug.bin}"
MAP="${2:-build/netboot_serve_boot_debug.map}"
PAYLOAD="${3:-}"
IP="${4:-192.168.2.75}"
TAPO="${TAPO:-$HOME/bin/tapo.sh}"

if [[ "${DEPLOY_CHECKED:-}" != "1" ]]; then
    echo "run-shot.sh: set DEPLOY_CHECKED=1 (after confirming the hardware-readiness checklist)." >&2
    exit 2
fi
for f in "$BIN" "$MAP"; do
    [[ -f "$f" ]] || { echo "run-shot.sh: missing $f — build it first (make netboot-serve-boot-debug)." >&2; exit 1; }
done

# A throwaway disk-record payload if none supplied (enough blocks to exercise WRQ ->
# claim -> rearm -> handshake -> DATA; finalize rejects a non-800K image, after the path
# under test). Named with the trinity-sam-disks/ prefix => the validated disk-record class.
if [[ -z "$PAYLOAD" ]]; then
    PAYLOAD="$(mktemp --suffix=.mgt)"
    python3 -c "open('$PAYLOAD','wb').write(bytes((i*17+3)&0xff for i in range(1536)))"
fi

cleanup() { "$TAPO" off || true; }
trap cleanup EXIT

echo "== power cycle: off, then on =="
"$TAPO" off; "$TAPO" on

echo "== wait for trinload (ping $IP) =="
start=$(date +%s)
until ping -c1 -W2 "$IP" >/dev/null 2>&1; do
    (( $(date +%s) - start > 150 )) && { echo "TIMEOUT: $IP not reachable"; exit 1; }
    sleep 3
done
echo "reachable after $(( $(date +%s) - start ))s"
sleep 5   # let trinload finish coming up before the push

echo "== push the serve binary ($BIN) =="
python3 tools/trinload-push/trinpush-serve.py "$IP" --strategy highest --bin "$BIN" --map "$MAP" || {
    echo "push failed"; exit 1; }

echo "== listen for markers + disk-record WRQ push =="
python3 tools/hardware-shot/listen-markers.py --seconds 12 &
LPID=$!
sleep 1
curl -s --max-time 35 -T "$PAYLOAD" "tftp://$IP/trinity-sam-disks/$(basename "$PAYLOAD")"
echo "curl exit: $?"
wait $LPID

echo "== done (powering off via trap) =="
