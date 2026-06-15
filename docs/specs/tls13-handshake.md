# On-SAM TLS 1.3 handshake — implementation spec (i88)

Status: **design / not yet implemented.** The cipher-suite primitives below are
all built and host-verified; this doc is the plan for composing them into a
working TLS 1.3 client. i88 is the project's **lowest-priority** work (build only
when nothing else remains) — see `docs/notes/item-registry.md` i88 and
`phase3-delivery-design.md` §7 for the rationale (the active firmware-fetch path is
plain HTTP via `cdn.githubraw.com` + a SHA-256 content pin; TLS is the durable
fallback for fetching directly from canonical GitHub).

## Scope

A **client-only TLS 1.3** handshake (RFC 8446) speaking exactly one cipher suite,
to one peer (github.com), reusing the existing TCP transport (`tcp_conn.asm`):

- Key exchange: **X25519** (the only supported_group offered).
- AEAD: **ChaCha20-Poly1305** (`TLS_CHACHA20_POLY1305_SHA256`).
- Hash/PRF: **SHA-256**.
- **No certificate-chain validation** (Pete's i88 scope reduction): the firmware
  content is independently SHA-256-pinned, so transport integrity is not relied on
  for content integrity. The server's Certificate / CertificateVerify are received
  and folded into the transcript but **not validated** against a CA store. (Pinning
  github's public key is an optional hardening — see q17.)
- 1-RTT only; no 0-RTT/PSK, no resumption, no client auth, no HelloRetryRequest
  handling beyond erroring out (github offers X25519, so HRR should not occur).

This is deliberately the **smallest conformant client** that github will complete a
handshake with and then serve an HTTPS GET over.

## The completed foundation (all host-verified, standalone leaves)

| Primitive | File | Role in the handshake |
|---|---|---|
| SHA-256 | `sha256.asm` | transcript hash; HKDF/HMAC hash |
| HMAC-SHA256 | `hmac_sha256.asm` | HKDF building block; Finished MAC |
| HKDF | `hkdf.asm` | Extract/Expand for the key schedule |
| HKDF-Expand-Label | `hkdf_expand_label.asm` | every secret/key/iv/finished derivation |
| ChaCha20 | `chacha20.asm` | the stream cipher (block + stream) |
| Poly1305 | `poly1305.asm` | the AEAD MAC |
| AEAD ChaCha20-Poly1305 | `aead.asm` | record protection (RFC 8439 §2.8) |
| X25519 | `x25519.asm` | the ECDHE shared secret |

Composition idiom (from `aead.asm`): a composite is built with its own `-D` flag
(not `NETBOOT_STANDALONE`), so the `include`d primitives' org guard stays inert and
the composite sets the org once — the primitives' own builds are unchanged. The
handshake module follows the same pattern.

## The 1-RTT flow

```
Client                                            Server (github)
------                                            ---------------
ClientHello  (key_share=X25519 pub, ciphers,  ->
              supported_versions=TLS1.3,
              supported_groups=x25519,
              signature_algorithms, SNI)
                                              <-  ServerHello (key_share=X25519 pub)
   -- derive handshake secrets from ECDHE --
                                              <-  {EncryptedExtensions}
                                              <-  {Certificate}          (not validated)
                                              <-  {CertificateVerify}    (not validated)
                                              <-  {Finished}             (verified, q17)
   {Finished}                                 ->
   -- derive application secrets --
   {GET / HTTP/1.1...}                        ->
                                              <-  {HTTP response}
```

`{…}` = records encrypted under the handshake (then application) traffic keys.

## Key schedule (RFC 8446 §7.1) → our primitives

All `Derive-Secret(S, label, msgs) = HKDF-Expand-Label(S, label,
Transcript-Hash(msgs), 32)` — a thin wrapper over the verified `expand_label`.
`Transcript-Hash` is the running SHA-256 over the handshake messages so far.

```
Early Secret      = HKDF-Extract(salt=0,                       IKM=0^32)         ; no PSK
                    (derived) = Derive-Secret(Early,  "derived", "")
Handshake Secret  = HKDF-Extract(salt=derived,                 IKM=ECDHE)        ; ECDHE = x25519(our priv, server pub)
  c hs traffic    = Derive-Secret(Handshake, "c hs traffic", CH..SH)
  s hs traffic    = Derive-Secret(Handshake, "s hs traffic", CH..SH)
                    (derived2) = Derive-Secret(Handshake, "derived", "")
Master Secret     = HKDF-Extract(salt=derived2,                IKM=0^32)
  c ap traffic    = Derive-Secret(Master, "c ap traffic", CH..server Finished)
  s ap traffic    = Derive-Secret(Master, "s ap traffic", CH..server Finished)
```

Per traffic secret: `key = Expand-Label(secret, "key", "", 32)` (ChaCha20 key,
32 B), `iv = Expand-Label(secret, "iv", "", 12)`, `finished_key =
Expand-Label(secret, "finished", "", 32)`.

## Record protection (RFC 8446 §5.2–5.3, §5.5)

- Per-record nonce = `iv XOR seq_number_be` (the 64-bit record sequence, per
  direction, right-aligned in the 12-byte IV, XORed). seq resets to 0 whenever the
  key changes (handshake→application).
- AEAD AAD = the 5-byte TLSCiphertext record header
  (`opaque_type=23 || legacy_version=0x0303 || length`).
- TLSInnerPlaintext = content || real_content_type || zeros(padding); we send no
  padding. Decrypt → strip the trailing content-type byte.
- This is `aead_encrypt`/`aead_decrypt` with the computed nonce + the header AAD.

## Decomposition into host-verifiable bricks (the implementation plan)

Each is a standalone, host-verified PR, mirroring the primitive cadence:

1. **Key schedule** (`tls_key_schedule`) — **LANDED** (`src/netboot/tls_keyschedule.asm`,
   `tls_keyschedule_test.go`): given the ECDHE shared secret + the two
   transcript hashes (CH..SH and CH..serverFin), produce all secrets + per-secret
   key/iv/finished. Verify against **RFC 8448 §3** (the worked TLS-1.3 handshake):
   the secrets, `iv`, and `finished` are hash-based and **cipher-independent**, so
   they match 8448 byte-for-byte even though 8448 uses AES-128-GCM; the 32-byte
   ChaCha20 `key` derivation is the same verified `expand_label` (len 32) — covered
   by the existing `hkdf_expand_label` tests. (Brick also exposes `tls_derive_secret`
   = `expand_label` with a transcript-hash context.)
2. **Record protection** (`tls_record_seal`/`tls_record_open`): nonce construction
   + the header-AAD AEAD wrapper. Verify against RFC 8448 record samples (the
   secrets there are reproducible) and a self round-trip.
3. **ClientHello builder** (`tls_build_client_hello`): emit the CH bytes for our
   fixed offer (X25519 key_share from a supplied ephemeral pub, the four required
   extensions + SNI). Verify the byte layout against a fixed golden vector + a Go
   `crypto/tls`-parser cross-check (the oracle parses our CH).
4. **Server-flight parser** (`tls_parse_server_hello` + the encrypted-flight walk):
   extract the server key_share from ServerHello; walk EncryptedExtensions /
   Certificate / CertificateVerify / Finished after decryption, feeding each into
   the transcript; capture the server Finished for verification. Verify against
   RFC 8448 / Go-generated flights.
5. **Transcript** (`tls_transcript_*`): running SHA-256 over the handshake messages
   (a thin `sha256_update` accumulator + a "snapshot the digest now" for the
   Derive-Secret contexts). Verify the snapshot hashes against 8448.
6. **The state machine** (`tls_client_*`): drive 1→5 over `tcp_conn.asm` — send CH,
   read+decrypt the server flight, derive keys, verify server Finished, send client
   Finished, switch to application keys, then run the HTTP GET through
   `tls_record_seal`/`open`. This is the integration capstone (see q17 — memory
   budget + the tcp_conn streaming integration; partly hardware-gated like
   `http_main`).

## Verification strategy

- RFC 8448 (the canonical TLS-1.3 worked example) anchors the schedule + records,
  even though its suite is AES-128-GCM: the SHA-256-based secrets/finished/iv are
  identical; only the AEAD `key` length differs (16 vs 32), and that path is the
  already-verified `expand_label`.
- Go's `crypto/tls` (stdlib, no external dep) is an available oracle for parsing
  our ClientHello and for generating/parsing flights in tests where a fixed RFC
  vector is awkward.
- The state-machine capstone is host-verified over the i80 ENC28J60 emulation +
  a scripted TLS peer where feasible; a real handshake to github is **hardware/
  network-gated** (like the rest of the netboot integration — emulation-verified ≠
  hardware-verified, CLAUDE.md §5).

## Design decisions + open questions

- **Settled (RFC-mechanical or Pete's prior scope):** the cipher suite (one
  suite), no cert-chain validation (content is SHA-256-pinned), 1-RTT only, SNI =
  the github host, X25519-only groups.
- **Open → q17** (the crux): the **SAM RAM / `&10000` memory budget** for putting
  the *whole* TLS stack into the bootable fetch. Today the primitives are standalone
  leaves *not* in the bootable image; a real TLS fetch must carry SHA-256 + HMAC +
  HKDF + Expand-Label + ChaCha20 + Poly1305 + X25519 + AEAD + the handshake code +
  their working buffers (X25519 alone needs ~1 KB of field temporaries; AEAD ~4 KB;
  the server flight — github's cert chain is several KB — must be buffered to
  decrypt). The bootable `netboot_http_boot.bin` already ends near `&FA52`. Whether
  the combined stack fits under `&10000`, or needs paging / a dedicated overlay
  loaded only for the one-time fetch, is an **architecture decision Pete should
  weigh in on** before the capstone (brick 6) is built. Bricks 1–5 are
  host-verifiable standalone leaves and do **not** depend on this — they proceed
  regardless; only the bootable integration is gated. A secondary, minor open point
  (agent will default to "verify"): whether to verify the **server Finished** MAC
  given cert-verification is skipped (it proves the peer derived the same handshake
  secret; cheap and correct to verify, so the default is yes).
