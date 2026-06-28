# i88c — TLS 6b integration (wire tls_reasm → tcp_conn), plan + split

**Item:** i88c "i88 6b — hardware-gated integration". Parent umbrella i88 (LOWEST
priority TLS-crypto-only HTTPS stretch). Deps i133, i93b both DONE.

**Emulation-first, hardware last** (CLAUDE.md §5/§7). Go is the authority for the
framing (rule 6); the paging layout has **no Go authority** (a Z80 hardware
constraint) — a genuine design decision surfaced to Pete.

## What exists (host-verified)

- `src/netboot/tls_client.asm` (i88a) — the 6a handshake state machine. Consumes ONE
  record from `TC_RX`/`TC_RX_LEN` via `tls_client_on_record`. `TC_RX = 1056 B`.
- `src/netboot/tls_reasm.asm` (i88b) — `tls_reasm_feed` frames complete records from
  arbitrary chunks and emits each via `REASM_EMIT_PTR`. `REASM_BUF = 16645 B`
  (`REASM_MAX = 5 + 2^14 + 256`). Host-verified vs Go `tls/reassembler.go`.
- `src/netboot/tcp_conn.asm` — `storage_sink_flush` dispatches window bytes
  (HL=ptr, BC=len) to `CONN_SINK_FILTER` when `CONN_SINK_FILTER_MODE=1`. Precedent:
  `http_main.asm` wires `body_sink_write` as the filter (generic fn-pointer dispatch,
  already host-proven).

## Grounded facts (measured, not guessed)

Built `make netboot-tls-client` (`build/netboot_tls_client.map`):
- Composite spans `org &8000 → tls_client_end &F7A5` = **30629 B** (window &8000–&FFFF
  is 32768 B — already ~93% full with `TC_RX=1056`).
- `TC_RX` at `&EBD7`; growing it 1056 → 16645 (+15589) pushes `tls_client_end` to
  ~`&1348A` — **past &FFFF; cannot fit flat**. Adding `REASM_BUF` (16645) is a further
  +16645. **Full-size TLS records fundamentally require a paged buffer layout.**
- Therefore the bounded integration (below, b1) uses records ≤ `TC_RX=1056` (the
  existing capture's small test-cert flight), which IS flat-buildable and
  emulation-verifiable now; the full-size 16645 path is design-gated (b2).

## The wiring (the new code — mechanical glue, body_sink_write precedent)

Two small shims in a new composed `src/netboot/tls_main.asm`:

1. **`tls_sink_feed`** — the `CONN_SINK_FILTER` target. In: HL=window ptr, BC=len
   (the `storage_sink_flush` convention). Body: `jp tls_reasm_feed` (it already takes
   HL=chunk, BC=len). Records emit via `REASM_EMIT_PTR`.
2. **`tls_record_shim`** — set as `REASM_EMIT_PTR`. In: HL=record ptr (=REASM_BUF),
   BC=record len. Body: `ldir` BC bytes HL→`TC_RX`; `TC_RX_LEN = BC`; `call
   tls_client_on_record`; `ret`. (Records ≤1056 for b1; b2 grows TC_RX + pages.)

Init: `CONN_SINK_FILTER_MODE=1`, `CONN_SINK_FILTER=tls_sink_feed`,
`REASM_EMIT_PTR=tls_record_shim`, `tls_reasm_init`, then `tls_client_init` +
`tls_client_first` (as the existing tls_client test does).

## Split decision (atomic-items rule — i88c bundles independent deliverables)

`build/registry split --parent i88c`:

- **i88c-b1** (agent) — the wiring shims + a bounded emulation integration test.
  Compose `tls_main.asm` (tls_reasm + tls_client + tcp_conn dispatch + the two shims),
  place `REASM_BUF` in low memory so it fits flat. New `tools/netboot-oracle/z80/
  tls_integration_test.go`: replay the existing captured handshake flight (records
  ≤1056), chunked at mis-aligned TCP-segment sizes, THROUGH `storage_sink_flush` (the
  real CONN_SINK_FILTER dispatch) → `tls_sink_feed` → reasm → `tls_record_shim` →
  `tls_client_on_record`, and assert `TC_PHASE` reaches DONE / the derived secrets
  match the Go authority (same asserts as `TestTLSClientHandshakeReplay`, but driven
  through the sink + chunking rather than `feedRecord`). Land via PR. Deps: none new.
- **i88c-b2** (agent) — grow `TC_RX` 1056 → 16645 + finalise the **paged** buffer
  layout so a full-size (≤16645) record fits (REASM_BUF + TC_RX can't coexist flat).
  Design-gated → depends_on i88c-b1 + qNN (paging design).
- **i88c-b3** (agent) — the bootable TLS disk: `tls_main.asm` as `NETBOOT_STREAM`,
  `org &8000`, a `make netboot-tls-*` boot/disk target + build-disk wiring.
  depends_on i88c-b2.
- **i88c-b4** (agent, hardware) — the real RST-8 shot on Trinity. depends_on i88c-b3 +
  qNN. If the qNN answer makes the shot store via HSAVE, ALSO gate on i357 (the
  store-leg free-record wedge, itself q71-gated) — added at decision time (i344).

**qNN** (Pete) — i88c hardware-path design, no Go authority for either half:
(a) the paged buffer layout for full-size (16645 B) TLS records (both REASM_BUF and
TC_RX exceed a flat build); (b) the hardware-leg definition — is it a live-internet
TLS-1.3 handshake against a real CDN (needs an app-data GET/response/decrypt/store
driver that does NOT yet exist, beyond i88a's handshake-only 6a scope), or a
captured-flight replay proving the wiring on silicon? What does the shot fetch/store?

## Execution order this session

1. Split i88c; create qNN; gate b2/b4 on it. Mark **i88c-b1 IN_PROGRESS on its branch**.
2. Branch from origin/main; implement b1 (shims + `tls_main.asm` + integration test).
3. Prove b1 in emulation: `make netboot-tls-*` build + `go test` the new integration
   test (+ the existing TLS suite stays green). The Go harness is the local loop;
   the CI SimCoupé matrix is the gate (local SimCoupé disk-boot is broken, i352).
4. Open the b1 PR ready-for-review; CI green → §3 read-only review → merge.
5. b2/b3/b4 stay gated (qNN + i357). Surface qNN to Pete via SendMessage. Do NOT fire
   any hardware shot without Pete sign-off on qNN.
