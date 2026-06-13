# `tools/netboot-oracle` — Phase-3 netboot host harness

Go authority + host oracle for the SAM-side Z80 netboot subsystem (the i82 TFTP
client, i83 TFTP server, i86 DHCP responder). It builds and parses the DHCP and
TFTP packets the Z80 program must produce, and verifies that logic byte-for-byte
against **golden vectors** extracted from a real Raspberry Pi 400 netboot
capture. This is the only host-side check possible before i80 (SimCoupé
Trinity-net emulation): it validates the *protocol logic in isolation*, not the
Z80 execution, the ENC28J60 hardware, or an end-to-end Pi boot — those stay
gated on i80 / real Trinity hardware.

## Packages

- `frame` — the Ethernet/IPv4/UDP offset contract (plan §1.2) + `BuildUDPFrame`,
  the Go authority for the Z80 `build_udp_frame` fresh-frame primitive (plan §5.1,
  the one routine trinload lacks).
- `dhcp` — DHCP body parse + the OFFER/ACK builder (i86), incl. the fixed
  option-43 "Raspberry Pi Boot" blob.
- `tftp` — RRQ/OACK/DATA/ACK/ERROR build+parse (i82 client / i83 server) + the
  serve-by-name resolve rules (404 serial-subdir, ERROR(1)-on-miss-keep-serving).
- `pcap` — dependency-free libpcap/pcapng reader (no cgo/libpcap).
- `golden` — committed, masked golden vectors (`vectors_gen.go`, generated).

## Run

```
make ci-netboot-oracle          # from the repo root (CI gate)
cd tools/netboot-oracle && go test ./...
```

## Golden vectors

The real captures stay **out of the repo** (publication policy — they carry
Pete's LAN MACs/IPs); they live in `~/tftp-logs`. `golden/vectors_gen.go` holds
masked copies: real MACs/IPs/device-serials are rewritten to documentation
placeholders (server `192.0.2.1`, client `192.0.2.44`), while every protocol
field — notably the option-43 blob — is byte-identical. Regenerate with:

```
go run ./cmd/gen-golden          # reads ~/tftp-logs, rewrites golden/vectors_gen.go
```

Decode provenance: `docs/notes/pi-netboot-capture-analysis.md`. Plan:
`docs/plans/phase3-netboot-implementation-plan.md`.
