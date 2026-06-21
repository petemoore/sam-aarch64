# i121d — Combined RRQ+WRQ serve: trinload push launcher with `--strategy`

**Goal:** Deliver the host PUSH LAUNCHER for the combined RRQ+WRQ serve program — a
`trinpush` wrapper that sets the WRQ disk-record placement strategy in the i121h
`SERVE_CONFIG` block, then pushes the program to the SAM via the existing trinload
`?`/`@`/`X` protocol for trinload to load+run. Plus the make/CI/doc wiring.

## What already exists (do NOT rebuild)
- The combined RRQ+WRQ program: `src/netboot/netboot_serve.asm` (`handle_wrq` :393,
  `wrq_claim_record`, `bdos_find_record_for_strategy`). Bootable build
  `netboot-serve-boot` → `build/netboot_serve_boot.bin` (org &8000, entry &8000 =
  `jp serve_main`; ends &C2EB; `SERVE_CONFIG` at &C2E7). This binary IS the
  trinload-pushable block (org &8000, self-contained) — no separate `_trinload`
  build is needed (unlike the dumper, which has no boot build).
- `SERVE_CONFIG` block (netboot_serve.asm:1551-1589): +0 magic `&5A`,
  +1 strategy (0=highest/1=lowest/2=explicit), +2..3 explicit record (LE).
  File offset = `addr(SERVE_CONFIG) - &8000`.
- The push protocol: `tools/trinload-push/trinload-push.py` (`?`→`!`, `@`-blocks,
  `X` exec). Verified against hardware for the dumper.
- The strategy→placement effect is already emulation-tested in Go
  (`netboot_serve_wrq_record_test.go` `patchStrategy` / `TestServeWRQRecordPushStrategy`).

## Deliverables
1. `tools/trinload-push/trinpush.py` — shared module: `parse_map`, `config_offset`,
   `patch_config`, `discover`, `push_data`, `execute`, `push_and_run`. Pure
   `patch_config(data, off, strategy, record)` so it is unit-testable.
2. Refactor `trinload-push.py` to use the module (CLI behaviour byte-identical).
3. `tools/trinload-push/trinpush-serve.py` — the i121d launcher:
   `<sam-ip> [--strategy highest|lowest|explicit:N] [--bin …] [--map …] [--page 1]`.
   Defaults to `build/netboot_serve_boot.bin` + `.map`. Patches `SERVE_CONFIG`
   (magic-checked) then pushes + execs at &8000.
4. `tools/trinload-push/test_trinpush.py` — unittest: pure `patch_config` (synthetic)
   + integration against the real built `netboot_serve_boot.bin`/`.map` (map parse,
   offset math, magic check, patched bytes).
5. Makefile: `netboot-serve-trinload` (phony alias → the boot binary, the pushable
   block) + `netboot-trinpush-test` (runs the unittest after building the boot binary).
6. CI: a step in the `netboot-z80` job running `make netboot-trinpush-test`.
7. README update in `tools/trinload-push/`.
8. Registry: i121d → DONE (+ assess i121c, whose code+tests already landed).

## Strategy encoding (mirror the Go authority + asm exactly)
- highest → 0, lowest → 1, explicit:N → 2 with record N as U16LE at +2..3.
- Patch only the strategy byte for highest/lowest; also the record word for explicit.
- Magic at +0 stays `&5A` (sanity-checked, never written).

Delete this plan in the completing PR.
