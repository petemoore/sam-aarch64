# On-SAM TLS 1.3 handshake — implementation spec (i88)

Status: **in progress.** The cipher-suite primitives below are all built and
host-verified; this doc is the plan for composing them into a working TLS 1.3
client. Bricks **1 (key schedule), 2 (record), 3 (ClientHello), 4 (server-flight
parser), 5 (transcript)** are landed (standalone, host-verified leaves); only the
capstone **brick 6 (state machine)** remains. **q17 (the `&10000` memory budget) is
RESOLVED** (Pete 2026-06-15: use paging, the agent owns the layout), so brick 6 is
unblocked on memory architecture; its remaining gate is the bootable/hardware integration
(the real `RST 8` path on Trinity, partly hardware-gated like `http_main`). The host-side
**Go authority that brick 6 mirrors has LANDED** (2026-06-16): `tools/netboot-oracle/tls/`
— a `Client` that completes a real 1-RTT handshake against Go `crypto/tls.Server` over
`net.Pipe`, cross-checking every derived secret against the server's `KeyLogWriter`. The
Z80 brick-6 state machine ports it (CLAUDE.md §6). i88 is the project's **lowest-priority** work (build only
when nothing else remains) — see `docs/notes/item-registry-open.md` i88 and
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
2. **Record protection** (`tls_record_seal`/`tls_record_open`) — **LANDED**
   (`src/netboot/tls_record.asm`, `tls_record_test.go`): nonce construction
   + the header-AAD AEAD wrapper. Verified by a seal→open round-trip + an
   independent Go reconstruction of the §5.2/§5.3 framing fed through the verified
   `aead_encrypt` (byte-exact record) + tamper/wrong-sequence rejection.
3. **ClientHello builder** (`tls_build_client_hello`) — **LANDED**
   (`src/netboot/tls_client_hello.asm`, `tls_client_hello_test.go`): emit the CH
   bytes for our fixed offer (X25519 key_share from a supplied ephemeral pub, the
   four required extensions + SNI). Pure byte assembly with back-patched container
   lengths. Verified three ways: byte-identical to an independent Go reconstruction
   of the §4.1.2 framing; parsed by Go `crypto/tls` into a `tls.ClientHelloInfo`
   (the stdlib TLS implementation as an independent structural oracle — it rejects
   a malformed message and reports the offer we built); and a hand-written TLV walk
   recovering the x25519 key_share = the supplied public key.
4. **Server-flight parser** (`tls_parse_server_hello` + the encrypted-flight walk)
   — **LANDED** (`src/netboot/tls_server_flight.asm`, `tls_server_flight_test.go`):
   `tls_parse_server_hello` extracts the server key_share from ServerHello and
   validates the negotiated suite/version (rejecting a wrong cipher, a missing
   key_share, a non-1.3 version, and the HRR sentinel random). `tls_walk_server
   _flight` walks EncryptedExtensions / Certificate / CertificateVerify / Finished
   after decryption, folding each into the transcript, snapshotting
   Hash(CH..CertificateVerify) just before the Finished, and capturing the server
   Finished verify_data. Verified vs Go-generated flights: the captured Finished +
   all four message flags, with `SF_HASH_BEFORE_FIN` = Go SHA-256(EE‖Cert‖CertVerify)
   and a post-walk snapshot = Go SHA-256(the whole flight), incl. a CH..SH-prefixed
   transcript variant.
5. **Transcript** (`tls_transcript_*`) — **LANDED** (`src/netboot/tls_transcript.asm`,
   `tls_transcript_test.go`): running SHA-256 over the handshake messages
   (a thin `sha256_update` accumulator + a "snapshot the digest now" via a
   save/final/restore of the 105-byte SHA-256 state, for the Derive-Secret
   contexts). Verified vs Go crypto/sha256 (empty + interleaved snapshots across
   the block boundary).
6. **The state machine** (`tls_client_*`): drive 1→5 over `tcp_conn.asm` — send CH,
   read+decrypt the server flight, derive keys, verify server Finished, send client
   Finished, switch to application keys, then run the HTTP GET through
   `tls_record_seal`/`open`. This is the integration capstone (q17 resolved — use
   paging; the remaining gate is the tcp_conn streaming + the bootable/hardware
   integration, partly hardware-gated like `http_main`). **Go authority landed**
   (2026-06-16): `tools/netboot-oracle/tls/`'s `Client` is the byte-for-byte
   reference — it completes a real handshake against `crypto/tls.Server` over
   `net.Pipe` with a `KeyLogWriter` secret cross-check; the Z80 port mirrors its
   `First()`/`OnRecord` sequencing.

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
- **Resolved (was q17, the crux): the SAM RAM / `&10000` memory budget** for putting
  the *whole* TLS stack into the bootable fetch. The primitives are standalone leaves
  *not* in the bootable image; a real TLS fetch must carry SHA-256 + HMAC + HKDF +
  Expand-Label + ChaCha20 + Poly1305 + X25519 + AEAD + the handshake code + their
  working buffers (X25519 alone needs ~1 KB of field temporaries; AEAD ~4 KB; the
  server flight — github's cert chain is several KB — must be buffered to decrypt),
  and `netboot_http_boot.bin` already ends near `&FA52`, so it will not all fit
  resident. **q17 RESOLVED (Pete 2026-06-15): use paging — the agent owns the layout.**
  The plan: **(c)** stream-and-discard the server flight (no cert-chain buffering, since
  cert validation is skipped) for RAM headroom, **+ (a)** page the TLS code + working
  buffers in for the one-time fetch (ENCTAB-COMET-trampoline style; a rare, slow paged
  path is fine). Shrinking the combined footprint to ease the paging is tracked as
  **i102** (the crypto T-state/size optimizations — all four leaves now optimized).
  Bricks 1–5 are host-verifiable standalone leaves regardless. **Server Finished** MAC:
  **verified** (it proves the peer derived the same handshake secret; cheap and correct).
- **Resolved (was q19): the real-hardware entropy source for the ephemeral X25519
  scalar.** A **build-time-injected per-build seed** (32 bytes of host `/dev/urandom`
  baked into a generated, gitignored asm block at `make` time) is the default. It is
  adequate because the i88 security model rests on the **SHA-256 content pin**, not on
  the transport — the firmware is public and hash-verified, so even a fixed scalar would
  be "secure" for that purpose, and a per-build *random, unpublished* scalar is not even
  breakable by a passive eavesdropper (ECDH hardness, unlike Konamiman's known `priv=1`).
  Caveats (all moot here): no forward secrecy; the seed is extractable from the disk
  artifact; a *distributed* release `.mgt` shares one seed (so confidentiality holds only
  for user-built images). Upgrade path for a generally-secure library: mix the seed (a
  per-device salt) with per-handshake runtime entropy (the Z80 `R` register, the 50 Hz
  frame timer, ENC28J60 packet-arrival jitter) through SHA-256. Details: closed q19.

## Prior art / novelty

A 2026-06-16 survey of native TLS on Z80 and adjacent 8-bit CPUs (web + GitHub):

- **Konamiman's TLSforZ80** (<https://github.com/Konamiman/TLSforZ80>, MIT, 2025) is the
  closest classic-Z80 prior art: a TLS 1.3 client, one suite `AES_128_GCM_SHA256`, with
  real AES-GCM / SHA-256 / HMAC / HKDF hand-written in Z80 asm — **but it stubs the key
  exchange** (`p256.asm` hardcodes the secp256r1 private key to `1`, so the shared secret
  is just the first half of the server's public key; the README states it is "as secure
  as not using TLS at all"). No certificate validation. It exercises the TLS 1.3 *protocol*
  without doing the elliptic-curve math.
- The only **real X25519 in the Z80 *family*** runs on the **eZ80** (TI-84+ CE), which has
  a hardware `MLT` multiply the classic Z80 lacks — so it is neither classic-Z80 prior art
  nor performance-transferable.
- The only **real native 8-bit TLS 1.3** found at all is on **6502** — JC-000's `c64-https`
  (Commodore 64), which independently chose **the same suite as us**
  (`TLS_CHACHA20_POLY1305_SHA256`) with real X25519 and even cert validation, at ~2–3 hours
  per handshake. Independent confirmation that ChaCha20-Poly1305 + X25519 is the natural
  no-hardware-multiply 8-bit choice, and that real EC is achievable but glacial.
- Everything else ("HTTPS on retro hardware") **offloads** TLS to a WiFi coprocessor
  (ESP8266/ESP32) or a PC proxy — the 8-bit CPU only ever sees plaintext. The SAM/Trinity
  world has no TLS of any kind.

**Novelty:** on this evidence, a classic-Z80 TLS 1.3 client with a *real* (non-stubbed,
non-offloaded) X25519 key exchange — i.e. this project's `tls_client.asm` once the state
machine lands — appears to be the **first TLS handshake and the first real elliptic-curve
key exchange executed on a classic Z80**. (Caveat: web/GitHub search, not exhaustive; a
private/demoscene/non-English release could exist.) This is the context that makes our
slow-but-real X25519 (i102: ~1.6 billion T-states/ladder, ~4–5 min at 6 MHz) a deliberate,
differentiated choice — everyone else stubbed or offloaded exactly that piece, and the
"slow handshake is fine, it's a one-shot fetch" premise is what makes it viable.
