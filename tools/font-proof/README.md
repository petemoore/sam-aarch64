# `tools/font-proof/` — real-SAM 6-px font readability proof

The i76 P1b probe (`docs/specs/editor-tui-prototype-design.md` §5): the SAM
itself renders a window of `tests/release/release.s` (code + long comments)
on a MODE 3 screen so 6-px-font readability is judged from an actual SAM
frame, not host arithmetic. Two screens, same source window: 85×32 with the
vendored 6×6 font (`tools/editor-prototype/fonts/`), and 64×24 with the SAM
ROM 8×8 charset as the known-readable reference.

## Files

- `fontproof.asm` — pyz80 probe (boot recipe as `tools/i62-bdos-experiment`):
  CALL 32768 renders the 6×6 screen, CALL 32771 the 8×8 reference.
- `main.go` — `fontproof font|text|disk`: font header → SAM row-padded
  binary; release.s window → flattened screen buffer; probe → bootable .mgt.
- `run-capture.sh` — boots a disk under SimCoupé and captures a PNG; dev
  container Xvfb route (`docs/notes/headless-simcoupe.md`) or the
  emulator-native `-pngonhalt` route for X-less hosts.
- `simcoupe-local-capture.patch` — local SimCoupé patch set for the
  `-pngonhalt` route (header explains each hunk; applies to the CI-pinned
  upstream SHA).

## Running

```bash
make font-proof     # build/font-proof.mgt (6x6) + build/font-proof-8x8.mgt
tools/font-proof/run-capture.sh build/font-proof.mgt build/mockups/font-proof-6x6.png
tools/font-proof/run-capture.sh build/font-proof-8x8.mgt build/mockups/font-proof-8x8-ref.png
```

This is a throwaway probe (spec §7), not a CI gate.
