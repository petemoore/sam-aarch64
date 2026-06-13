# `src/netboot/` — the SAM-side Z80 netboot subsystem (Phase 3)

A standalone SAM Coupé Z80 program (separate from the `src/` assembler) that turns a SAM + Quazar Trinity into an aarch64 **netboot server**: it answers a Raspberry Pi's DHCP and TFTP requests so the Pi boots over the network from images the SAM holds. It also contains the TFTP **client** used to fetch images onto the SAM. It shares no memory map, paging trampoline, or build target with the assembler.

It sits on the trinload UDP/IP/Ethernet stack (simonowen/trinload, Simon Owen; BSD-style licence) and adds the DHCP responder (i86), TFTP server (i83) and TFTP client (i82) state machines.

## Authority and verification

The host Go module `tools/netboot-oracle/` is the **authority**: its `frame`/`dhcp`/`tftp` packages build and parse the same packets, verified byte-for-byte against masked golden vectors from a real Pi 400 capture. Each routine here is a faithful port of the matching Go function (memory `feedback_go_is_encoding_authority`) — port, do not reinvent.

The **protocol logic is host-verifiable**: `tools/netboot-oracle/z80/` assembles each routine with pyz80, runs it under a flat-memory koron-go/z80 harness, and asserts its emitted packet equals the golden vector (`make ci-netboot-z80`, the `netboot-z80` CI job). The **ENC28J60 wire I/O and an end-to-end Pi boot are NOT host-verifiable** — they are gated on i80 (SimCoupé Trinity-net emulation) or real Trinity hardware, and live on an unmerged branch until one of those can exercise them.

## Files

- `build_udp_frame.asm` — originate a fresh UDP/IPv4/Ethernet frame (plan §5.1), the primitive trinload lacks; the port of `frame/frame.go::BuildUDPFrame`.
- `dhcp_reply.asm` — build the DHCP OFFER/ACK body the responder (i86) emits (the option template + the fixed option-43 "Raspberry Pi Boot" blob + the echoed client UUID); the port of `dhcp/dhcp.go::BuildReply`.
- `tftp_build.asm` — the i83 TFTP server's reply-packet builders: OACK, DATA, ERROR; the port of `tftp/tftp.go::BuildOACK`/`BuildDATA`/`BuildError`.
- `tftp_parse.asm` — the i83 TFTP server's request side: parse an incoming RRQ (`parse_request`) and resolve its filename against the flat store (`resolve`: 404 serial-subdir, OACK a hit, ERROR(1) every miss); the port of `tftp/tftp.go::ParseRequest` and `tftp/server.go::Resolve`.

Design + sequencing: `docs/plans/phase3-netboot-implementation-plan.md`; wire-level oracle: `docs/notes/pi-netboot-capture-analysis.md`.
