# i121 — WRQ (accept-in) support for the SAM netboot TFTP server

Pete's PRIMARY disk-ship use case: boot the SAM, `tftp put` a disk image from the
Mac, the SAM writes it to a free Trinity record, then boot+test — ending the
SD-card shuffle. A standard TFTP server handles both RRQ (serve-out, the shipped
i83/i95/i96 server) and WRQ (accept-in, this item).

**Status:** in progress. i121a (handshake) is **DONE** (#490); the bricks are split in the
registry (i121a–i121e). q30 (the remote-push safety model) is **RESOLVED** (2026-06-20):
write-to-free-only + an auto-pick disk config (prompts if absent), so the write brick is no
longer gated. i121b/c/e/d are the remaining work, host-verified in the Go harness before
any hardware test.

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

## q30 — the remote-push safety model — RESOLVED 2026-06-20 (write-to-free-only + auto-pick config)
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
- **i121e (C5) — graceful termination / return-to-trinload.** Without it the serve loop
  monopolizes the SAM and nothing can run after a push batch. Mechanism in the section below.
  Host test: inject a normal RRQ/WRQ then the sentinel WRQ; assert the serve loop RETs (StopPC
  at the loop-exit / trinload return), mirroring `trinload_test.go` + the dumper Esc pattern.

## The escape hatch — returning control to trinload (i121e, Pete 2026-06-21)

trinload bootstraps our program by pushing its `start` as the return address and `jp`-ing in
(`trinload.asm:230-231,238`), so a plain **`RET` returns to trinload** — proven in emulation
(`trinload_test.go:75-118`) and already used by the ROM dumper's Esc-to-exit
(`netboot_dumper.asm:318-329`). The serve program lacks an exit: `sv_serve_loop`
(`netboot_serve.asm:935-937`) is an infinite `call serve_serve_once; jr sv_serve_loop`. i121e
adds two exits, both RET-to-trinload:

- **Sentinel push (unattended):** a `tftp put` of the reserved name **`tftp.done`** (empty file)
  is detected in `handle_wrq` (`netboot_serve.asm:367`) after the client endpoint is learned and
  *before* the ACK-0/OACK branch; on match it sends no reply and sets `XFER_STOP_REQUESTED`.
  `sv_serve_loop` polls the flag after each frame and `ret nz`. Reserved name ⇒ never stored
  (manifest design, decision 6).
- **Keyboard Esc (attended):** the same non-blocking poll the dumper uses
  (`netboot_dumper.asm:319-326`: `ld a,&f7; in a,(&f9); bit 5,a; ret z`) in `sv_serve_loop`, for
  manual recovery if a session must be ended by hand.

The serve loop does no paging of its own (unlike the dumper, which needed the i188 LMPR
save/restore), so the exit is a clean RET with no page/register restoration. Emulation-first is
the safety net: a hardware crash is unrecoverable and equally strands the machine, so every
brick is green in the Go harness before it touches hardware.

## What stays hardware-gated
The `rst 8` HSAVE persist + the CSD-derived `BD_RECORDS` (i145) — same as i119.
Everything else (WRQ handshake, receive, auto-slot logic, the write *dispatch*) is
host-verified. The real Mac→SAM `tftp put` over the wire is the hardware final gate.

## Other ambiguities for Pete (recommended defaults; not blockers)
- **Home program:** extend `netboot_serve.asm` (i96, ARP+TFTP, matches stock `tftp`)
  vs `netboot_server.asm` (i95, +DHCP/PXE). Recommend `netboot_serve.asm`.
- **i114 (manifest):** OPEN, NOT a prerequisite — reuse i119's free-record detection.
