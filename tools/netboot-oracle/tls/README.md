# `tools/netboot-oracle/tls` — the TLS 1.3 client handshake authority

The host-side Go reference the Z80 brick-6 handshake state machine
(`src/netboot/tls_client_*.asm`) mirrors (CLAUDE.md §6: Go is the authority, the
Z80 is a port). A `Client` completes a real 1-RTT TLS 1.3 handshake —
`TLS_CHACHA20_POLY1305_SHA256` + X25519, no cert-chain validation (i88 scope:
firmware is independently SHA-256-pinned) — composing the already-verified
leaves (key schedule, record protection, ClientHello, server-flight parser,
transcript) and adding only the rx-driven sequencing, exactly as
`http.Provisioner` does for the firmware fetch.

## Layout

- `keyschedule.go` — RFC 8446 §7.1 key schedule (`ExpandLabel`, `Extract`,
  `KeySchedule`, `ComputeKeySchedule`).
- `record.go` — §5.2/§5.3 record protection (`Framing`, `Seal`, `Open`).
- `aead.go` — ChaCha20-Poly1305 (RFC 8439), from scratch so the module stays
  pure-stdlib and the AEAD is independent of the copy `crypto/tls` uses.
- `clienthello.go` / `serverflight.go` — the ClientHello builder and
  ServerHello/flight parser.
- `client.go` — the `Client` state machine (`First`, `OnRecord`, `Phase`).

## Verification

`client_test.go` drives the `Client` through a full handshake against a real
`crypto/tls.Server` over `net.Pipe` and cross-checks every derived secret against
the server's `KeyLogWriter` — the strongest proof that our independent derivation
matches the stdlib's. The AEAD + key schedule + record layer also carry RFC
known-answer anchors. Run: `make ci-netboot-oracle`.

Design + the Z80 port plan: [`../../../docs/specs/tls13-handshake.md`](../../../docs/specs/tls13-handshake.md).
