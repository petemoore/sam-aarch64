# Plan — i88 brick 6: the Z80 TLS-1.3 client handshake state machine

**Status:** ready to execute. **Scope:** port the host-side Go authority
(`tools/netboot-oracle/tls/`, landed PR #325) to Z80 — CLAUDE.md §6: Go is the
authority, the Z80 is a mechanical port. The Go `tls.Client` is both the
reference *and* the host-test oracle.

**Decomposition (mirror the Go split).** The Go authority already separates the
TLS **record-level** state machine (`Client.OnRecord`, takes/returns TLS records,
no TCP) from the transport. Port it the same way, in two independent units:

- **6a — the record-level state machine** (`src/netboot/tls_client.asm`): the
  faithful port of `Client.First`/`OnRecord`. Composes the landed bricks 1-5 +
  `x25519.asm`. Takes/returns **TLS records** (not TCP frames). **Fully
  host-verifiable** against the Go authority, with checkable intermediate
  secrets in memory (NOT all-or-nothing). **This is the primary deliverable.**
- **6b — the TLS-over-TCP integration + bootable** (`tls_client_main.asm` /
  paging): wire 6a's records onto `tcp_conn.asm`'s byte stream, add the
  ARP→SYN→records framing + the paging layout for the bootable. Partly
  **hardware-gated** like `http_main` brick 7 (q18): the real `RST 8` path / a
  live github handshake validate only on Trinity. Lower priority; do 6a first.

Delete this plan in the PR that lands 6a (note 6b's remaining hardware gate in
the i88 row). The whole effort lives on a feature branch per CLAUDE.md §5 until
6a's host test is green.

---

## Part 0 — prerequisite: dedupe the one shared double-include (sha256), via a `-D` flag

**Why:** bricks 1-5 each `include` their full dependency chain as standalone
leaves. In the 6a include set (the five bricks + `x25519`), exactly **one** leaf
is reachable via two paths and so is emitted twice — `sha256.asm`, via
`tls_keyschedule`→`hkdf_expand_label`→`hkdf`→`hmac_sha256`→`sha256` **and** via
`tls_server_flight`→`tls_transcript`→`sha256`. Tracing the rest: `hkdf_expand_label`/
`hkdf`/`hmac_sha256` come only through key-schedule; `tls_transcript` only through
server-flight; `aead`/`chacha20`/`poly1305` only through `tls_record`; `x25519`/
`tls_client_hello` pull nothing. So **sha256 is the sole collision.** A second
emission redefines every `sha256` label → an assembly error. (The org guard
`if defined(NETBOOT_STANDALONE)` only guards the `org`, not the body.)

**Do NOT use a generic include-once guard.** pyz80 (verified by reading
`pyz80/pyz80.py` + a reproducer, 2026-06-16) runs exactly **two passes** and
**never resets `symboltable` between them** (`for p in 1,2:`, no clear). So the
classic guard

```asm
                if defined(INCLUDED_SHA256)     ; <-- DOES NOT WORK in pyz80
                else
INCLUDED_SHA256: equ 1
; ... body ...
                endif
```

defines `INCLUDED_SHA256` in pass 1, which **persists into pass 2**, so the
`if defined(...)` is true on pass 2 and the `else` body is **skipped on pass 2**.
A self-contained standalone leaf survives this (pass-1 memory persists, its
labels are set once), which is why a naive guard *looks* fine when built alone —
but any file with labels **after** an include of a guarded body phase-errors
(`Symbol X: expected <hi> but calculated 32768, has this symbol been used
twice?` — the included body emitted on pass 1, skipped on pass 2). This was
observed on `tls_keyschedule`/`tls_record`/`tls_server_flight`. Guards are a dead
end here.

**Fix (verified): suppress the redundant include with a `-D` build flag** — a
command-line define is set before pass 1 and is consistent across **both** passes,
so no phase error. The composite `tls_client.asm` is built with
`-D NETBOOT_TLS_CLIENT=1`; have the **second** sha256 path skip its own include
under that flag, so sha256 arrives exactly once (via the key-schedule chain, which
6a includes first). One edit, in `tls_transcript.asm` — wrap its
`include "sha256.asm"`:

```asm
                if defined(NETBOOT_TLS_CLIENT)
                else                            ; standalone / server-flight build:
                include "sha256.asm"            ; sha256 comes from here
                endif                           ; in the 6a composite it comes via
                                                ; the key-schedule chain (included first)
```

Standalone `tls_transcript` / `tls_server_flight` builds (no `NETBOOT_TLS_CLIENT`)
include sha256 as before — byte-identical. This change is **folded into the 6a
PR**, not a separate PR (it is only meaningful *together with* 6a's composite, so
they review as one unit). Suppressing `tls_transcript`'s include makes the
key-schedule chain the **sole** sha256 emitter — the invariant is "emitted exactly
once," which the `-D` flag guarantees independent of include order (pyz80's two
passes resolve forward references either way). (If 6b later composes another
sha256 source, e.g. `tcp_conn` under `NETBOOT_HOSTTEST`, apply the same one-line
flag-suppression there.)

**Verification:** `tls_client.asm` assembles with sha256 present once; every
standalone brick/leaf build stays byte-identical (run the `make ci-netboot-z80`
brick targets + `tls_*_test.go`). A reproducer for the pyz80 two-pass behaviour
lived in `/tmp` during this analysis; the conclusion above is what matters.

---

## Part 1 — 6a: `src/netboot/tls_client.asm` (the record-level state machine)

### Composition

```asm
                if defined(NETBOOT_TLS_CLIENT)
                org     &8000
                endif

                include "tls_keyschedule.asm"   ; brick 1 (+ expand_label→hkdf→hmac→SHA256 — first sha256)
                include "tls_record.asm"         ; brick 2 (+ aead→chacha20+poly1305)
                include "tls_client_hello.asm"   ; brick 3
                include "tls_server_flight.asm"  ; brick 4 (+ tls_transcript; its sha256 suppressed, Part 0)
                include "x25519.asm"             ; ECDHE + client pubkey
; sha256 is emitted exactly once — via the key-schedule chain — because
; tls_transcript's own sha256 include is flag-suppressed in this composite
; (Part 0). pyz80 is 2-pass, so forward refs resolve regardless of order;
; keeping tls_keyschedule first is just for readability, not correctness.
```

Build with `-D NETBOOT_TLS_CLIENT=1` (so each leaf's `NETBOOT_STANDALONE` org
guard stays inert and 6a sets the org once — the aead/tls_record idiom).

### State + buffers (the data block the host test reads/writes)

Reuse the bricks' existing named buffers (`KS_ECDHE`/`KS_HASH_HS`/`KS_HASH_AP`/
`KS_CHS..KS_SAP*`, `TR_*`, `CH_RANDOM`/`CH_SESSION_ID`/`CH_PUBKEY`/`CH_HOSTNAME`/
`CH_MSG`, `SH_MSG`/`SH_OK`/`SH_SERVER_PUB`, `SF_FLIGHT`/`SF_OK`/`SF_FINISHED`/
`SF_HASH_BEFORE_FIN`, the `tls_transcript` SHA state). Add 6a's own cells:

```
TC_CLIENT_PRIV  defs 32   ; injected X25519 scalar (host test); real HW: see q19
TC_PHASE        defs 1     ; 0=INIT 1=SENT_CH 2=GOT_SH 3=DONE 4=ERROR (mirror Go Phase)
TC_SERVER_SEQ   defs 8     ; server handshake-record seq (BE), reset 0 at key change
TC_FLIGHT_OFF   defs 2     ; bytes of SF_FLIGHT already folded into the transcript
TC_RX           defs 1056  ; one inbound TLS record (header+payload), caller-filled
TC_RX_LEN       defs 2
TC_TX           defs 1056  ; one outbound TLS record (the CH record / client Finished)
TC_TX_LEN       defs 2
TC_STATUS       defs 1     ; 0=CONTINUE 1=DONE (mirror Go Status)
```

`SF_FLIGHT` (8192 B) doubles as the decrypted-flight accumulator: append each
decrypted record's content at `SF_FLIGHT + SF_FLIGHT_LEN`, then run
`tls_walk_server_flight` over `[0, SF_FLIGHT_LEN)`.

### Entry points (faithful port of the Go authority — cite `client.go`)

**`tls_client_init`** — mirror `NewClient` + the pubkey step. Inputs:
`TC_CLIENT_PRIV` (scalar), `CH_RANDOM`, `CH_SESSION_ID`, `CH_HOSTNAME`/`_LEN`
(caller-filled). `x25519` uses **fixed buffers** (not register args, per
`x25519.asm:808`): `X25519_K` (scalar in), `X25519_U` (u in), `X25519_OUT`
(result out). So: copy `TC_CLIENT_PRIV`→`X25519_K`, the RFC 7748 base point
(`09` then 31 × `00`)→`X25519_U`, `call x25519`, copy `X25519_OUT`→`CH_PUBKEY`.
Then `tls_transcript_init`; `TC_PHASE=INIT`; `TC_SERVER_SEQ=0`; `SF_FLIGHT_LEN=0`;
`TC_FLIGHT_OFF=0`.

**`tls_client_first`** — mirror `Client.First`. Call `tls_build_client_hello`
(brick 3) → `CH_MSG`/`CH_MSG_LEN`; `tls_transcript_update(HL=CH_MSG,
BC=CH_MSG_LEN)`; emit the plaintext record into `TC_TX`: `0x16 0x03 0x01
len16 || CH_MSG`, set `TC_TX_LEN`; `TC_PHASE=SENT_CH`. Returns the CH record in
`TC_TX`.

**`tls_client_on_record`** — mirror `Client.OnRecord`. Dispatch on `TC_RX[0]`:
- `0x14` ChangeCipherSpec → ignore: `TC_TX_LEN=0`, `TC_STATUS=CONTINUE`, ret.
- `0x15` Alert → `TC_PHASE=ERROR`, ret (host test treats as failure).
- `0x16` plaintext handshake (ServerHello) → `tls_client_on_server_hello`.
- `0x17` application_data (encrypted flight) → `tls_client_on_encrypted`.

**`tls_client_on_server_hello`** (port `onServerHello`): require `TC_PHASE=SENT_CH`.
Copy `TC_RX[5:]`→`SH_MSG`; `tls_parse_server_hello`; if `SH_OK=0`→ERROR.
`tls_transcript_update(SH_MSG, len)`; `tls_transcript_snapshot(HL=KS_HASH_HS)`
(this is hashHS). Compute the ECDHE: copy `TC_CLIENT_PRIV`→`X25519_K`,
`SH_SERVER_PUB`→`X25519_U`, `call x25519`, copy `X25519_OUT`→`KS_ECDHE`. Call
`tls_key_schedule` — wait: brick 1 needs BOTH hashHS and hashAP. **Split the
derivation** (the Go authority's `DeriveHandshake`/`DeriveApplication`): brick 1
as landed derives everything from both hashes at once. For 6a, either (a) add a
`tls_key_schedule_handshake` entry to brick 1 that stops after the handshake
secrets (Early→Handshake→c/s hs traffic + their key/iv/fin + derived2), and a
`tls_key_schedule_application` that finishes (Master + c/s ap traffic) from
`KS_HASH_AP`; or (b) call the full `tls_key_schedule` twice — once now with a
zero `KS_HASH_AP` (handshake secrets are independent of hashAP, so they are
correct; ignore the app outputs), once after the server Finished with the real
`KS_HASH_AP`. **Prefer (a)** — it is the faithful two-phase port and avoids a
throwaway pass; it is a small, mechanical split of brick 1 (the secret tree is
already laid out in order). `TC_PHASE=GOT_SH`; `TC_TX_LEN=0`; CONTINUE.

**`tls_client_on_encrypted`** (port `onEncrypted`): require `TC_PHASE=GOT_SH`.
`tls_record_open` with `TR_KEY=KS_SHS_KEY`, `TR_IV=KS_SHS_IV`, `TR_SEQ=
TC_SERVER_SEQ`, `TR_RECORD=TC_RX`, `TR_RECORD_LEN=TC_RX_LEN`. If `TR_OK=0`→ERROR.
Increment `TC_SERVER_SEQ` (BE +1). Require `TR_TYPE=0x16`. Append `TR_CONTENT`
(`TR_CONTENT_LEN` bytes) to `SF_FLIGHT` at `SF_FLIGHT_LEN`; advance
`SF_FLIGHT_LEN`. Run `tls_walk_server_flight`. If `SF_OK=0` (Finished not yet
present) → `TC_TX_LEN=0`, CONTINUE (need more records). Else:
- Fold `BeforeFin` (EE‖Cert‖CertVerify = `SF_FLIGHT[0 : finished_offset]`) into
  the transcript: `tls_transcript_update`. (`tls_walk_server_flight` already
  computed `SF_HASH_BEFORE_FIN`; assert the fold reproduces it, or reuse
  `SF_HASH_BEFORE_FIN` directly as hashBeforeFin.)
- Verify server Finished: `hmac_sha256(KS_SHS_FIN, SF_HASH_BEFORE_FIN)` ==
  `SF_FINISHED` (32 B). Mismatch → ERROR. (HMAC entry from `hmac_sha256.asm`.)
- Fold the server Finished message into the transcript;
  `tls_transcript_snapshot(HL=KS_HASH_AP)` (hashAP).
- `tls_key_schedule_application` (or the second brick-1 pass) → app secrets.
- Client Finished: `clientVerify = hmac_sha256(KS_CHS_FIN, KS_HASH_AP)` (32 B);
  build `finMsg = 20 00 00 20 || clientVerify`; fold into the transcript;
  `tls_record_seal` with `TR_KEY=KS_CHS_KEY`, `TR_IV=KS_CHS_IV`, `TR_SEQ=0`,
  `TR_TYPE=0x16`, `TR_CONTENT=finMsg` → `TR_RECORD`; copy to `TC_TX`/`TC_TX_LEN`.
- `TC_PHASE=DONE`; `TC_STATUS=DONE`.

Transcript snapshot points (must match the Go authority exactly): **hashHS** after
folding CH+SH; **hashBeforeFin** after EE/Cert/CertVerify; **hashAP** after the
server Finished. Server-record seq increments per `0x17` record only (CCS not
counted). Client Finished record uses client seq 0.

### Makefile

```make
$(BUILD)/netboot_tls_client.bin $(BUILD)/netboot_tls_client.map: \
        src/netboot/tls_client.asm $(wildcard src/netboot/tls_*.asm) \
        src/netboot/x25519.asm src/netboot/aead.asm src/netboot/sha256.asm ...
	@mkdir -p $(BUILD)
	pyz80 -D NETBOOT_TLS_CLIENT=1 --obj=$(BUILD)/netboot_tls_client.bin \
	    --mapfile=$(BUILD)/netboot_tls_client.map src/netboot/tls_client.asm
```
Add a `.PHONY: netboot-tls-client` target and fold it into `ci-netboot-z80`.

---

## Part 2 — Go authority additions for the deterministic oracle

The current `tls.Client` (`NewClient`) randomizes the key/random/sid, so a run is
not reproducible. For the Z80 host test, add a deterministic path + a capture:

1. **Deterministic constructor** in `tools/netboot-oracle/tls/client.go`:
   `NewClientDeterministic(host string, priv []byte, random, sid [32]byte)
   (*Client, error)` — set `c.priv` from `ecdh.X25519().NewPrivateKey(priv)`,
   `c.random=random`, `c.sid=sid`. (Keep `NewClient` as the random wrapper.)
   Also export the scalar for the Z80: `func (c *Client) PrivateScalar() []byte`
   returning `c.priv.Bytes()` (the raw 32-byte scalar the Z80 loads into
   `TC_CLIENT_PRIV`).
2. **Wire capture** in a new exported test helper (or in the z80 test directly,
   which imports the parent `tls` package via the existing replace directive):
   drive `NewClientDeterministic` against `crypto/tls.Server` over `net.Pipe`
   (reuse `TestHandshakeAgainstCryptoTLS`'s harness), recording, in order: the
   ClientHello record the client sent, each inbound record (ServerHello, CCS, the
   `0x17` flight records), and the client Finished record. Return a struct
   `{Priv, Random, Sid [32]byte; Inbound [][]byte; CHRecord, FinRecord []byte;
   CHS,SHS,CAP,SAP []byte}`. (`CHS..SAP` from `c.Schedule()` for the
   intermediate-secret asserts.)

This capture is deterministic *for the Z80 test*: the server's randomness is
absorbed into the captured `Inbound` bytes; the Z80 must reproduce the captured
client outputs from the captured inputs + the captured scalar.

---

## Part 3 — `tls_client_test.go` (host verification, capture-then-replay)

In `tools/netboot-oracle/z80/`:

1. Capture a handshake via Part 2 (`cap`).
2. `mac := Load("netboot_tls_client.bin", ...)`. Write `cap.Priv`→`TC_CLIENT_PRIV`,
   `cap.Random`→`CH_RANDOM`, `cap.Sid`→`CH_SESSION_ID`, the host bytes→`CH_HOSTNAME`
   /`CH_HOSTNAME_LEN`.
3. `CallEntry("tls_client_init", Entry{StepCap: 80_000_000})` (x25519 pubkey is a
   ~37M-byte-op ladder — raise the cap as `x25519_test.go` does). Assert
   `Read(CH_PUBKEY,32)` == the pubkey in `cap.CHRecord`'s key_share.
4. `CallEntry("tls_client_first", …)`; assert `TC_TX[:TC_TX_LEN]` == `cap.CHRecord`.
5. For each `rec` in `cap.Inbound`: `Write(TC_RX, rec)`, `WriteU16LE(TC_RX_LEN,
   len(rec))`, `CallEntry("tls_client_on_record", Entry{StepCap: 80_000_000})`.
   After the ServerHello record, assert the intermediate secrets in memory:
   `Read(KS_CHS,32)`==`cap.CHS`, `KS_SHS`==`cap.SHS` (proves ECDHE + key schedule).
   When `TC_STATUS=DONE`, assert `TC_TX[:TC_TX_LEN]` == `cap.FinRecord` and
   `Read(KS_CAP,32)`==`cap.CAP`, `KS_SAP`==`cap.SAP`.

Because each derived secret is asserted in memory, 6a is **not** all-or-nothing:
a wrong byte localizes to the ECDHE, a specific traffic secret, the record layer,
or the Finished MAC. The two x25519 ladders make the test ~10-15 s (acceptable;
`netboot-z80` already runs multi-second x25519 tests). A negative control (flip a
byte of one inbound record → expect `TC_PHASE=ERROR`) guards the AEAD-auth path.

---

## Part 4 — 6b: TLS-over-TCP integration + paging (the bootable, partly HW-gated)

This is the analogue of `http_main` brick 7 (q18) — do it after 6a is green; its
real-network payoff is hardware-gated (CLAUDE.md §5).

- **Record reassembly over TCP.** `tcp_conn`'s `CONN_DATA` is a byte stream;
  TLS records (5-byte header + payload) must be reassembled from it (a record may
  span TCP segments; a segment may carry several records). Add a small reassembly
  shim that, as `tcp_conn_recv` accumulates bytes, emits complete TLS records to
  `tls_client_on_record` and writes the returned `TC_TX` back through the TCP
  send path (`CONN_TX_PAYLOAD`/the segment builder). Phase machine:
  `ARP → SYN → (TLS records) → app-data GET → DONE`, structured like
  `http_main`'s `prov_first`/`prov_onframe` (rx-driven; one frame in, one frame
  out + status).
- **Paging layout (q17 resolved — agent owns it).** The composite 6a binary
  (bricks 1-5 + x25519 + tcp_conn + encdrv) will not fit the resident netboot
  image (`netboot_http_boot.bin` already ends near `&FA52`, one 16K section-C
  page). Plan: stage the TLS code + working buffers on **dedicated RAM pages**
  (pages 4..N, free after BASIC's 0-3; see `docs/notes/sam-paging.md` §5) and
  page them into section C (HMPR, port `&FB`) for the one-time, slow handshake —
  the ENCTAB-COMET-trampoline idiom (a rare slow paged path is fine, q17). The
  resident boot stub keeps the TCP/ENC driver + a trampoline that pages in the
  TLS payload, runs the handshake, then pages back to stream the firmware GET.
  Decide the exact page assignment when wiring `tls_client_main.asm` + the
  `build-disk` payload (mirror the `netboot-http-disk` target). The handshake's
  ~4 KB AEAD scratch + the multi-KB server flight buffer (`SF_FLIGHT`) live in
  the paged region; **stream-and-discard** the cert chain (no validation, i88
  scope) so the flight buffer need not hold the whole chain.
- **Verification.** Host-verify 6b's wire side over the i80 ENC28J60 emulation
  against a scripted TLS peer derived from the Go authority's recorded records
  (extend Part 2's capture to TCP-framed bytes). The real github handshake +
  the `RST 8`/paging on Trinity stay hardware-gated (Pete's test).

---

## Risks / notes

- **Brick-1 two-phase split** (Part 1) is the one non-mechanical change to a
  landed brick; keep `tls_keyschedule_test.go` green (the full `tls_key_schedule`
  entry stays, plus the two new partial entries). The handshake secrets are
  independent of hashAP, so the split is a clean cut in the existing secret tree.
- **x25519 cost**: two ladders per handshake (~12 s in the harness). Fine for a
  host test; on hardware the one-time fetch tolerates a slow handshake (i88
  premise). Do not add x25519 to any fast inner-loop test.
- **The Go authority is the oracle**: keep Part 2's capture pure/deterministic so
  the Z80 test diffs call-by-call. If `crypto/tls`'s flight framing varies
  run-to-run (record coalescing), the capture absorbs it — the Z80 replays the
  exact captured records, so the test stays stable.
- **q19 (new): the real-hardware entropy source** for the ephemeral X25519
  scalar. The host test injects a fixed scalar; real hardware needs 32 random
  bytes. Because the firmware content is SHA-256-pinned, transport forward
  secrecy is not security-critical here, so a weak source (frame-arrival jitter /
  a timer) is likely acceptable — but it is a genuine design point for 6b. Raise
  as `q19` in the question registry in the 6a PR; it does not block 6a.
