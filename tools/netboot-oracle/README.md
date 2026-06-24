# `tools/netboot-oracle` — Phase-3 netboot host harness

Go authority + host oracle for the SAM-side Z80 netboot subsystem (i82 TFTP
client, i83 TFTP server, i86 DHCP responder). It builds and parses the DHCP and
TFTP packets the Z80 must produce, and verifies that logic byte-for-byte against
golden vectors from a real Raspberry Pi 400 netboot. This is the only host-side
check possible before i80 (SimCoupé Trinity-net emulation): it validates the
*protocol logic in isolation* — not the Z80 execution, the ENC28J60 hardware, or
an end-to-end Pi boot, which stay gated on i80 / real Trinity.

The wire golden vectors are Pi 4/400; the server is model-agnostic (it serves by
filename), so `TestServerServesBothPiFamilies` pins that one flat store serves the
Pi 3 firmware set (`bootcode.bin`/`start.elf`/`fixup.dat`) and the Pi 4 set alike.
The Pi 3 boot-ROM wire differences and the remaining capture work are in
`docs/notes/pi-netboot-capture-analysis.md` §4 (i89 / i89b).

## Packages

- `frame` — the Ethernet/IPv4/UDP offset contract + `BuildUDPFrame`, the Go
  authority for the Z80 `build_udp_frame` fresh-frame primitive trinload lacks,
  plus the ARP request/reply build + parse functions.
- `smoke` — the bring-up smoke-test responder (i94): answer an ARP request for
  the SAM's IP. The Go authority for the Z80 `smoke_test.asm` bring-up program.
- `server` — the integrated netboot server (i95): one `OnFrame` dispatcher that
  routes a received frame to ARP / DHCP / TFTP, composing `smoke`+`dhcp`+`tftp`.
  The Go authority for the Z80 integrated main loop (`netboot_server.asm`, to come).
- `tcp` — the TCP transport (i70): the one new transport the
  firmware-self-provisioning HTTP client rides on (the rest of the stack is
  UDP-only). `tcp.go` builds + parses the segment (the Go authority for the Z80
  `build_tcp_segment` primitive, incl. the mandatory pseudo-header checksum UDP
  did not need); `conn.go::Conn` is the client connection state machine (active
  open SYN→SYN-ACK→ACK, seq/ack tracking, data ACK cadence, FIN teardown) — the
  Go authority for the Z80 `tcp_conn.asm`.
- `http` — the HTTP/1.0 GET client (i70): `BuildRequest` (the `GET … HTTP/1.0`
  request bytes) + `ParseResponse` (status code + body offset) + a thin `Client`
  riding a `tcp.Conn` (the Go authority for the Z80 `http_get.asm`), and
  `Fetcher` — the integrated fetch phase machine (ARP → handshake → GET →
  accumulate → FIN), the HTTP analogue of the TFTP `client.Client` and the
  authority for the forthcoming Z80 `netboot_http.asm`.
- `dhcp` — DHCP parse + the OFFER/ACK builder (i86), incl. the option-43 blob.
- `tftp` — RRQ/OACK/DATA/ACK/ERROR + serve-by-name resolve + the client/server
  transfer-loop state machines + the client originate front (the Go reference
  for the Z80 DATA/ACK loops + the ARP-for-server/RRQ-send front). `manifest.go`
  is the serve-manifest authority (i114a): a line-based text format mapping full
  Pi-facing TFTP names/paths → local B-DOS files or remote record locators (+ span,
  size, optional SHA-256). `*Manifest` is a drop-in `Store` (so `Resolve` answers
  an RRQ straight off it), and `Entry.ServePlan` threads a remote blob through
  `bdos.SpanPlan` for the ordered read plan. `allocator.go` is the storage-allocation
  authority (i114b): the manifest-header policy (first-free / fixed-list / highest-free,
  default highest-free) chooses which records a new blob is written to over a modelled
  `Card`, reusing already-claimed leftover space first and **warning rather than ever
  stealing** an unlisted/excluded record on overflow. Design:
  `docs/specs/netboot-storage-manifest-design.md` §1-4.
- `bdos` — the storage seam: the UIFA/DIFA field arithmetic gluing the server
  (serve by name) + client (write by name) to the B-DOS hooks, plus a flat-
  directory model and the firmware-spanning convention (`span.go`: `SpanPlan`
  splits a large object into bounded, plain-`HSAVE`'d records reassembled in
  order at serve time — the i99/q16 authority the Z80 `fw_span.asm` mirrors).
  Models the field maths only — the RST 8 hook dispatch is NOT host-verifiable
  (no ROM in the harness) and stays gated on real Trinity.
- `samboot` — the SAMBOOT BIOS config (i176): the host authority for the
  editable default-boot-record setting (`Config.Encode`/`Decode`) the patched
  bootblock reads from the `"SAMBOOT Config  "` Trinity EEPROM chunk to decide
  whether to auto-boot a record at power-on. `cmd/samboot-config` is the host
  "BIOS setup" editor; the Z80 reader is `src/netboot/samboot_config.asm`
  (`z80/samboot_config_test.go` round-trips them). Format: `docs/specs/samboot.md` §4.
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
