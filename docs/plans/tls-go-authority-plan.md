# Plan — i88 brick 6: the Go TLS-1.3 client handshake authority

**Status:** ready to execute (a fresh full-context session). **Scope:** the *host-side
Go authority only* — a `tools/netboot-oracle/tls/` package whose `Client` completes a
real TLS-1.3 1-RTT handshake against Go `crypto/tls` over `net.Pipe`. This is the
reference the later Z80 brick-6 state machine (`tls_client_*.asm`) will mirror
(CLAUDE.md §6: Go is the authority, the Z80 is a port). The Z80 capstone itself stays
gated on **q17** (the `&10000` footprint / paging call for Pete) and is out of scope here.

**Why a plan, not the code:** the handshake driver's verification is all-or-nothing
(crypto/tls completes the handshake or emits an opaque alert), so it wants a fresh
context budget for the finicky debug. This plan is the spec-gate artifact (development
discipline #1) so that execution is mechanical. Delete this plan in the PR that lands
the authority.

## Authority shape (mirror `http/provision.go`)

`Provisioner` is the template: an rx-driven state machine — `First() []byte`,
`OnFrame(rx []byte) (tx []byte, st Status)`, a `Phase` enum, composing already-verified
sub-bricks and adding only sequencing. The `tls.Client` mirrors this at the TLS-record
level (it does not re-implement TCP — the handshake bytes ride an in-test stream; the
Z80 port later threads them through `tcp_conn.asm`).

## Package layout — `tools/netboot-oracle/tls/`

Module: `github.com/petemoore/sam-aarch64/tools/netboot-oracle` (flat sub-packages, Go 1.26).

```
tls/
  keyschedule.go   — KeySchedule + ComputeKeySchedule(ecdhe, hashHS, hashAP)
  record.go        — Framing(iv, seq, typ, content) -> (nonce, aad, inner); Seal/Open
  clienthello.go   — BuildClientHello(random, sid, pub, host) []byte
  serverflight.go  — ParseServerHello(sh) (pub [32]byte, err); WalkServerFlight(...)
  client.go        — Client state machine (the new code; Phase enum; OnRecord)
  client_test.go   — full 1-RTT vs crypto/tls.Server + per-brick anchor tests
```

## Step 1 — extract the brick references into the package (low-risk, verifiable)

These already exist as **inline Go oracles in the z80 test files**; lift each into the
package as an exported function, then have the z80 test keep using its own copy OR import
the package (prefer import to avoid drift, but if that balloons the diff, leave the z80
copies and dedupe in a follow-up — note it as a `qN`/`iN` row, don't leave silent dupes).

| Extract | From | Into | Verify against |
|---|---|---|---|
| `goExpandLabel(secret, label, ctx, length)` | `z80/hkdf_expand_label_test.go:45` | `keyschedule.go` | already RFC-8446 §7.1 |
| `ksExtract`, `goKeyScheduleRef(ecdhe, hashHS, hashAP)` | `z80/tls_keyschedule_test.go:51,60` | `keyschedule.go` | RFC 8448 early/derived anchors (test lines 85–93) |
| `goFraming(iv, seq8, typ, content)` | `z80/tls_record_test.go:84` | `record.go` | RFC 8446 §5.2/§5.3 framing |
| `buildClientHelloGo(random, sid, pub, host)` | `z80/tls_client_hello_test.go:76` | `clienthello.go` | `crypto/tls` parse (the `parseCHViaTLS` pattern, test line 219) |
| `buildServerHello`, `ParseServerHello` | `z80/tls_server_flight_test.go:63,93` | `serverflight.go` | round-trip + the 5 rejection cases |

Each extracted function gets a package test pinning it to the SAME anchor its inline
oracle used. This step alone is independently mergeable if the state machine slips.

## Step 2 — the `Client` state machine (the new code)

```
Phase: Init -> SentCH -> GotSH -> GotFlight -> SentFin -> Done | Error
```

`OnRecord(rx []byte) (tx []byte, st Status, err error)` drives, per the spec
(`docs/specs/tls13-handshake.md` §state-machine, lines 150–156):

1. `First()` → ClientHello (BuildClientHello with our X25519 pub + 32-byte random);
   fold into transcript.
2. ServerHello in → `ParseServerHello` (extract server X25519 pub), compute ECDHE =
   `x25519(ourPriv, serverPub)` (use `golang.org/x/crypto/curve25519` or stdlib
   `crypto/ecdh`), fold SH into transcript, snapshot `hashHS`, run
   `ComputeKeySchedule` → handshake traffic keys/ivs/finished.
3. Decrypt the server flight (EncryptedExtensions/Certificate/CertificateVerify/Finished)
   with `Open` under server-handshake key/iv (seq from 0); fold each into transcript;
   snapshot before Finished; **verify** server Finished = HMAC(shsFin, hashBeforeFin).
   (No cert-chain validation — i88 scope reduction; the cert messages are folded but not
   checked.)
4. Snapshot `hashAP`; derive application secrets; compute + send client Finished
   (HMAC(chsFin, hashAfterServerFin)) sealed under client-handshake key/iv.
5. Switch to application keys → `Done`. (The HTTP GET over app keys is a later brick /
   the http_main composition; this authority's done-criterion is a completed handshake.)

## Step 3 — verification harness (`client_test.go`)

Drive against a real `crypto/tls.Server` over `net.Pipe` (extend the `parseCHViaTLS`
pattern, `z80/tls_client_hello_test.go:219`):

- `tls.Config{ Certificates: <self-signed>, CipherSuites: {tls.TLS_CHACHA20_POLY1305_SHA256},
  CurvePreferences: {tls.X25519}, MinVersion/MaxVersion: VersionTLS13,
  KeyLogWriter: <buf> }`. crypto/tls supports CHACHA20_POLY1305 + X25519 as server.
- Run our `Client` as the client side of the pipe; assert the server's `Handshake()`
  returns nil (it accepted our ClientHello, our Finished, everything).
- Cross-check derived secrets against the server's **KeyLogWriter** output
  (SSLKEYLOGFILE lines `CLIENT_HANDSHAKE_TRAFFIC_SECRET <clientRandom> <hex>`,
  `SERVER_HANDSHAKE_TRAFFIC_SECRET`, `CLIENT_TRAFFIC_SECRET_0`, `SERVER_TRAFFIC_SECRET_0`)
  — byte-for-byte equal to our `KeySchedule` secrets. This is the strongest proof:
  our independent derivation matches the stdlib's.
- Stage the debug: a wrong byte → crypto/tls alert. Verify incrementally — (a) server
  emits ServerHello (CH accepted), (b) keylog secrets match (key schedule + ECDHE right),
  (c) flight decrypts (record layer right), (d) handshake completes (Finished right).

## Wiring / hygiene

- Add the package to `make ci-netboot-oracle` (it runs `go test ./...` under
  `tools/netboot-oracle`, which picks up the new sub-package automatically — confirm).
- A `tls/README.md` (≤30 lines: what it is, that it's the Go authority for the Z80
  brick-6 TLS handshake, link to `docs/specs/tls13-handshake.md`).
- On completion: delete this plan; flip the i88 registry row (the Go authority landed;
  the Z80 capstone remains q17-gated); note in `m9-status` / ROADMAP.

## Risks / notes

- crypto/tls is strict: the ClientHello must be exactly acceptable (the `tls_client_hello`
  brick already round-trips through `crypto/tls`'s parser, so the bytes are known-good).
- The X25519 here is host Go (`crypto/ecdh`), not our Z80 `x25519.asm` — that's fine; the
  authority proves the *handshake orchestration*, and the Z80 primitives are already
  independently verified leaves.
- Keep the per-brick reference funcs (Step 1) pure and side-effect-free so the future Z80
  port can diff against them call-by-call.
