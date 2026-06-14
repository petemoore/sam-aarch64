# `tools/netboot-oracle` — Phase-3 netboot host harness

Go authority + host oracle for the SAM-side Z80 netboot subsystem (i82 TFTP
client, i83 TFTP server, i86 DHCP responder). It builds and parses the DHCP and
TFTP packets the Z80 must produce, and verifies that logic byte-for-byte against
golden vectors from a real Raspberry Pi 400 netboot. This is the only host-side
check possible before i80 (SimCoupé Trinity-net emulation): it validates the
*protocol logic in isolation* — not the Z80 execution, the ENC28J60 hardware, or
an end-to-end Pi boot, which stay gated on i80 / real Trinity.

## Packages

- `frame` — the Ethernet/IPv4/UDP offset contract + `BuildUDPFrame`, the Go
  authority for the Z80 `build_udp_frame` fresh-frame primitive trinload lacks.
- `dhcp` — DHCP parse + the OFFER/ACK builder (i86), incl. the option-43 blob.
- `tftp` — RRQ/OACK/DATA/ACK/ERROR + serve-by-name resolve + the client/server
  transfer-loop state machines + the client originate front (the Go reference
  for the Z80 DATA/ACK loops + the ARP-for-server/RRQ-send front).
- `bdos` — the storage seam: the UIFA/DIFA field arithmetic gluing the server
  (serve by name) + client (write by name) to the B-DOS hooks, plus a flat-
  directory model. Models the field maths only — the RST 8 hook dispatch is
  NOT host-verifiable (no ROM in the harness) and stays gated on real Trinity.
- `pcap` — dependency-free libpcap/pcapng reader. `golden` — masked vectors.
- `z80/` — a nested Go module: a flat-memory koron-go/z80 harness that runs the
  SAM-side Z80 port (`src/netboot/*.asm`) and byte-compares its output against
  these golden vectors (`make ci-netboot-z80`, the `netboot-z80` CI job). Nested
  so this pure-Go module stays dependency-free; the Z80/pyz80 deps live there.

## Run

`make ci-netboot-oracle` (the CI gate), or `go test ./...` here.

## Golden vectors

The real captures stay out of the repo (`~/tftp-logs`, publication policy).
`golden/vectors_gen.go` holds masked copies — real MACs/IPs/serials rewritten to
documentation placeholders, every protocol field byte-identical. Regenerate with
`go run ./cmd/gen-golden`. Decode provenance + the plan:
`docs/notes/pi-netboot-capture-analysis.md`,
`docs/plans/phase3-netboot-implementation-plan.md`.
