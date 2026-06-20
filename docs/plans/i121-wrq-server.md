# i121 — WRQ (accept-in) support for the SAM netboot TFTP server

Pete's PRIMARY disk-ship use case: boot the SAM, `tftp put` a disk image from the
Mac, the SAM writes it to a free Trinity record, then boot+test — ending the
SD-card shuffle. A standard TFTP server handles both RRQ (serve-out, the shipped
i83/i95/i96 server) and WRQ (accept-in, this item).

**Status:** planned, not started. **Gated on q30** (the remote-push safety model —
see below) for the *write* brick (C3); the handshake (C1) + receive (C2) bricks are
not gated and can proceed first. The implementing agent should `registry split`
i121 into the bricks below as it starts execution.

## The pieces already in place (reuse, don't reinvent)
- **Serve-out server** (`src/netboot/netboot_serve.asm`, Go authority
  `tools/netboot-oracle/serve/serve.go`): a per-frame dispatcher routing UDP:69 →
  `handle_rrq`. The opcode is parsed (not dispatched on) in `parse_request`
  (`tftp_parse.asm:84-97`), which **already accepts WRQ** (`OP_WRQ equ 2`). The WRQ
  branch slots in at `netboot_serve.asm:277` (right after `parse_request` succeeds,
  before the RRQ path at `:280`). OACK build: `build_oack` (`tftp_build.asm:41`) +
  `build_oack_opts` (`netboot_serve.asm:648`).
- **Receive loop (i82)** `tftp_recv_data` (`tftp_client_loop.asm:61-194`): DATA(op3)
  → block-check → `ldir` into `STAGING` → `STAGE_OFFSET += len` → ACK; short block ⇒
  `XFER_DONE`. Fully decoupled from the RRQ-send side — reusable by pre-setting the
  peer endpoint + `ACKED=0`/`STAGE_OFFSET=0`. **Caveat:** its `SERVER_*` var names
  (meaning "the peer") collide with `netboot_serve.asm`'s `CLIENT_*`/`CONFIG_*` — the
  real integration cost (C2).
- **Write-out (i119, DONE #466):** `bdos_find_free_record` (`bdos_seam.asm:474`,
  first free by scan, needs `BD_RECORDS` from the hardware-gated CSD read i145;
  injected in emulation) → `bdos_select_record` (HRECORD) → `bdos_fill_save_uifa`
  (`bdos_name_to_uifa` drops dotted suffix, caps 10 chars) → `bdos_save_hook`
  (hardware-gated `rst 8` HSAVE). The interactive `bdos_pick_record` confirm gate
  (`bdos_picker.asm`) is the attended-client path — see the safety question.
- **Harness test pattern:** `tools/netboot-oracle/z80/netboot_serve_test.go`
  (`serveDemo` + `eqFrame` vs the Go authority; inject ARP/RRQ/ACK, assert TX) and
  the i119e E2E (`bdos_store_test.go` `TestClientE2EConfirm`: inject + assert
  `saves[0].Record/Name/Size`; decline ⇒ **zero** HSAVEs). HSAVE captured via
  `BDOSStore` (`bdos_store.go:280`); `CardModel.SetRecordEntry` + `WriteU16LE(BD_RECORDS,…)`.

## q30 — the remote-push safety model (Pete decides; gates C3)
i119 makes an interactive show-name + y/n confirm **mandatory** before any HSAVE
(`memory/trinity_storage_shared_resource`). A remote `tftp put` has **no operator at
the SAM keyboard** — `pick_read_yesno` would spin forever. Recommended (port, not
new design): **write-to-FREE-only** — `bdos_find_free_record` picks a free slot; if
none free, reply TFTP `ERROR(3, "no free record")` rather than overwrite anything.
This never touches a named record, satisfying the shared-resource invariant without
a keyboard. Alternative: a boot-time "remote pushes armed" pre-confirm. **Pete: is
write-to-free-only sufficient, or must remote pushes be operator-armed/confirmed?**

## Bricks (split when execution starts; mirror i119 B1–B5)
- **i121a (C1) — WRQ parse + handshake (ACK-0 / OACK).** Opcode dispatch at
  `netboot_serve.asm:277` → `handle_wrq` learns the client endpoint; bare WRQ →
  `build_ack0` (new 4-byte `00 04 00 00` builder in `tftp_build.asm`); optioned WRQ →
  OACK (reuse `build_oack`, echo `blksize`/`tsize`). Go authority: `serverloop.go`
  `StartWrite` (today rejects non-RRQ at `:80`) + `serve.go` `OnFrame` WRQ branch.
  Host test: inject WRQ → assert ACK-0/OACK byte-for-byte vs the Go authority.
  **Not gated; the clean first deliverable.**
- **i121b (C2) — receive-to-staging.** Wire `tftp_recv_data` into the WRQ transfer
  (DATA → ACK each → accumulate → short-block end); resolve the `SERVER_*`/`CLIENT_*`
  name collision. Host test: inject WRQ+DATA1..N, assert an ACK per block + `STAGING`
  holds the file.
- **i121c (C3) — auto-slot + write. GATED ON q30.** Final block → `bdos_find_free_record`
  → none ⇒ `ERROR(3)`; else select + `bdos_fill_save_uifa` (name from the WRQ
  filename) + `bdos_save_hook`, write-to-free-only safety. E2E mirrors
  `TestClientE2EConfirm` (assert HSAVE in the free record + the right bytes; all-full
  ⇒ `ERROR(3)` + zero HSAVEs).
- **i121d (C4) — combined RRQ+WRQ bootable program + disk/Makefile/CI wiring.**

## What stays hardware-gated
The `rst 8` HSAVE persist + the CSD-derived `BD_RECORDS` (i145) — same as i119.
Everything else (WRQ handshake, receive, auto-slot logic, the write *dispatch*) is
host-verified. The real Mac→SAM `tftp put` over the wire is the hardware final gate.

## Other ambiguities for Pete (recommended defaults; not blockers)
- **Home program:** extend `netboot_serve.asm` (i96, ARP+TFTP, matches stock `tftp`)
  vs `netboot_server.asm` (i95, +DHCP/PXE). Recommend `netboot_serve.asm`.
- **i114 (manifest):** OPEN, NOT a prerequisite — reuse i119's free-record detection.
