# tools/hardware-shot

Self-serve Trinity hardware-shot tooling for the netboot disk-record push investigation
(i280b). Turns the previously session-scratch recipe into committed, reusable scripts.

- `listen-markers.py` — bind UDP `:9001` and decode the i271 debug step-markers the
  bootable serve broadcasts under `-D NETBOOT_DEBUG` (`SDBG` + version + code). The marker
  table mirrors `src/netboot/dbg_marker.asm`. A stall localizes to the last marker seen.
- `run-shot.sh` — one full shot: TAPO power-cycle → push a debug serve binary to the
  auto-booted trinload → disk-record WRQ push via `curl` → capture markers → power off.
- `simulate-pi-client.py` — drive a live SAM netboot server with the captured Pi 400
  exchange: DHCP DORA + a non-PXE negative probe, then TFTP fetches with byte
  verification and per-block timing (`--selftest` = the no-SAM capture-readiness check).

## Usage

```
make netboot-serve-boot-debug
DEPLOY_CHECKED=1 tools/hardware-shot/run-shot.sh
```

The SAM-deploy steps are gated by the deploy-guard hook (i252): pass `DEPLOY_CHECKED=1`
only after confirming the hardware-readiness checklist for the binary being pushed. The
TAPO plug enforces a 10 s minimum between power events; never power-cycle faster.

The canonical recipe + the marker semantics live in `docs/ROADMAP.md` (§8d) and
`docs/notes/trinity-sd-z80-interface.md` §8d; this directory is the tooling, not the doc.
The koron-go harness (`tools/netboot-oracle/z80/`) is the emulation gate — a hardware shot
is only for the faults that do not reproduce there (the shared ENC/SD-controller contention).
