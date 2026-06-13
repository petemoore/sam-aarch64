# Plan — two bootable netboot demo disks (i96 serve-files server + i82 client)

Pete asked (2026-06-14, present) for two concrete bootable demo disks, prioritised
above the rest of the Phase-3 queue, each landed as one increment per PR,
foreground-CI, seen through to merge:

1. **A TFTP serve-files demo disk** (new item **i96**) — a bootable `.mgt` that
   serves a couple of small demo files baked into the binary to a **plain
   `tftp`/`curl` client** from any machine. TFTP only — **no DHCP, no Pi, no PXE
   option-43**. Distinct from the full Pi-netboot server (i95). Boot it → it
   serves; Pete `tftp get`s the files.
2. **The TFTP client demo disk** (item **i82**, the ROADMAP increment-3 client) —
   fetches `.mgt` disk images from a TFTP server and **saves them to Trinity
   storage via the B-DOS record hooks**.

Honesty line (CLAUDE.md §5): the real B-DOS record write + the on-hardware run
stay Pete's test — mark them, never claim them. Host-verify the protocol +
ENC28J60 wire I/O over the i80 emulation as far as possible.

This plan is grounded in the existing, fully host-verified netboot subsystem.
Every routine is a **port of a Go authority** (`tools/netboot-oracle/`, memory
`feedback_go_is_encoding_authority`) — port, do not reinvent. Both disks follow
the established bootable-program pattern of `src/netboot/smoke_test.asm` (i94) and
`src/netboot/netboot_server.asm` (i95): a `*_serve_once` host-verifiable
dispatcher + a `*_main` real-hardware boot wrapper behind `NETBOOT_HOSTTEST`,
reusing the host-verified packet primitives + the vendored `encdrv.asm` /
`eeprom.asm`.

---

## The load-bearing new behaviour for i96: bare-RRQ → DATA (RFC 2347)

The existing `tftp.ServerLoop.OnRRQ` **always sends an OACK on a hit**, because it
was built for the Pi 4 boot ROM, which *always* sends options (blksize/tsize/…).
A **plain `tftp`/`curl` client sends a bare RRQ with no options**. RFC 2347 is
explicit: the server may send an OACK **only if the client requested at least one
option**; for a bare RRQ the server must respond with **DATA block 1** directly
(default 512-byte blocks, RFC 1350). So the demo server (i96) must branch:

- RRQ **with** options that we accept → OACK (client ACKs block 0 → DATA 1) — the
  existing path, exercised by `curl tftp://…` which negotiates `tsize`.
- RRQ with **no** options → **DATA block 1 immediately** (no OACK) — what the
  classic BSD/macOS/busybox `tftp get` sends.

Both converge on the same `ServerXfer` DATA/ACK streaming after the first DATA.
This is the one genuinely new piece of protocol logic; it gets its own Go
authority routine + Z80 port + host test, byte-for-byte.

---

## PR A — i96 the serve-files demo server

### A1. Go authority: `tools/netboot-oracle/serve/serve.go`

A new package `serve` with a `Responder` that composes ARP + a bare-RRQ-capable
TFTP server over a baked-in store. It is the single frame-in/reply-out authority
the Z80 dispatcher ports, analogous to `server.Server` but **TFTP+ARP only**.

```
type Config struct { ServerMAC [6]byte; ServerIP [4]byte; ServerTID uint16 }

type Responder struct { ... arp *smoke.Responder; tftp *tftp.ServerLoop; ... }

func New(cfg Config, store tftp.Store, src func(name string) tftp.Source) *Responder

func (r *Responder) OnFrame(rx []byte) []byte:
  1. ARP request for our IP  -> smoke.Responder ARP reply
  2. ParseUDP; not UDP -> nil
  3. dst port 69 (RRQ): parse+resolve.
       hit + has-options   -> SetSource; OACK   (justOACKed=true; ACK0 -> FirstData)
       hit + no-options    -> SetSource; FirstData (DATA block 1) directly
       miss                -> ERROR(1) (keep serving)
  4. dst port == ServerTID (ACK): justOACKed ? FirstData : OnACK
  5. else nil
```

The no-option DATA-first path needs one small addition to `tftp.ServerLoop`: a
method that, on a hit, **starts the xfer and returns DATA block 1 without an
OACK**. Add `func (s *ServerLoop) OnRRQNoOpt(rrqFrame []byte) []byte` (parse +
resolve + learn client + `NewServerXfer` at 512 + return `wrap(FirstData)`), OR —
cleaner — add a `wantOACK bool` decision inside a shared helper. **Decision:** add
`ServerLoop.StartTransfer(rrqFrame []byte, sendOACK bool) []byte` that both the
existing `OnRRQ` (sendOACK=true) and the new no-option path (sendOACK=false) call,
keeping `OnRRQ` behaviour byte-identical (regression-guarded by the existing i83
+ i95 tests). Verify the existing tests stay green.

Tests `serve/serve_test.go`: bare-RRQ→DATA-512 full transfer; optioned-RRQ→OACK
(echo path); miss→ERROR(1)-keep-serving; ARP→reply; non-UDP ignored.

### A2. Z80 port: `src/netboot/netboot_serve.asm` (`serve_serve_once` + `serve_main`)

A *new* single-dispatch state machine (do **not** include the loop files —
`RXBUF`/`CONFIG_*`/`build_udp_frame`/`encdrv` collide), composing the
host-verified `build_udp_frame` / `build_arp_reply` / `tftp_build` / `tftp_parse`
+ `encdrv.asm` directly, exactly as `netboot_server.asm` does, minus DHCP. Ports
`serve.Responder.OnFrame` step-for-step, including the bare-RRQ→DATA-1 branch.

- `serve_serve_once` (host-verifiable): one `drv_read` → route ARP / RRQ /
  transfer-ACK → `drv_write`. Out BC = bytes sent (0 = silent).
- `serve_main` (real hardware, behind `if defined(NETBOOT_HOSTTEST)==0`): read
  MAC+IP from the EEPROM "Trinity Network " chunk, fill CONFIG, set the fixed
  ServerTID, init the ENC28J60, provision the baked-in demo store + source (see
  A3), then loop `serve_serve_once`. Border-colour-on-failure as smoke/server.

CONFIG block: `CONFIG_SERVERMAC/IP`, `CONFIG_SERVERTID`, the client endpoint +
transfer state mirrored from `netboot_server.asm`'s TFTP half, the flat `STORE`,
and `SRC_PTR`. The demo files are assembled into the binary (A3) so `serve_main`
points `STORE`/`SRC_PTR` at them — no B-DOS needed for the *demo* (that is what
makes it testable with a plain client, no Trinity storage provisioning).

### A3. Baked-in demo files

The demo serves files baked into the binary, so no B-DOS provisioning is needed.
Define them in the asm as two small files, e.g.:

```
demo_store:  defm "hello.txt": defb 0 : defw len_hello, 0   ; LE size
             defm "readme.txt": defb 0 : defw len_readme, 0
             defb 0                                          ; end sentinel
hello_bytes: defm "Hello from a SAM Coupe over Trinity TFTP!", 13, 10
len_hello:   equ ... ; computed
```

The store format is the one `tftp_parse.asm::resolve` already walks
(`name\0` + 4-byte LE size, NUL terminator). `SRC_PTR` must resolve **per name**:
the simplest correct model for the demo is a tiny name→source-pointer table the
`serve_main` consults when a hit resolves — OR, since the harness for i95 stubs a
single `SRC_PTR`, give the demo server a `serve_resolve_src` step that maps the
resolved name to the matching `*_bytes` label. **Decision:** a parallel
`demo_src_table` of `{name-hash-or-index → src ptr + size}`; on a resolved hit,
`serve_serve_once` looks the name up and sets `SRC_PTR` + `XFER_SIZE` before
arming the transfer. Keep it dead simple (≤3 files). The host test provides its
own store+source exactly like the i95 test, so this table is exercised only on the
`serve_main` boot path (host test drives `serve_serve_once` with injected
store/source — table not on that path) — i.e. the multi-file table is
**real-hardware-only glue**; mark it so. To keep multi-file serving host-verified,
the host test injects a 2-entry store + a name→src resolver and drives two RRQs.

### A4. Host verification: `tools/netboot-oracle/z80/netboot_serve_test.go`

Mirror `netboot_server_test.go`: load `netboot_serve.bin`, attach the emulated
ENC28J60, fill CONFIG + a 2-file store + sources, drv_init, then assert
frame-for-frame vs `serve.Responder.OnFrame`:
- ARP request → ARP reply.
- bare RRQ for file A → DATA block 1 (512) directly; ACK1→DATA2…→short final.
- optioned RRQ for file B → OACK; ACK0→DATA1…
- RRQ for a missing name → ERROR(1), keep serving.

### A5. Build wiring

`Makefile`: `netboot-serve` (host-test bin, `-D NETBOOT_HOSTTEST=1`),
`netboot-serve-boot` (bootable bin, no flag, includes eeprom.asm),
`netboot-serve-disk` (`build-disk -netboot … -netboot-name serve`). Add to
`netboot-z80-routines`. `ci-netboot-oracle` already runs `go test ./...` so
`serve/serve_test.go` is picked up.

### A6. Docs + registry

- `docs/notes/netboot-trinity-testing.md`: a new "**Serve-files demo (i96)**"
  section — build `make netboot-serve-disk`, boot it, then from any machine
  `tftp <sam-ip>` + `get hello.txt`, or `curl tftp://<sam-ip>/hello.txt`. Mark
  the real ENC silicon + the on-hardware run as Pete's test.
- `src/netboot/README.md`: add `netboot_serve.asm`.
- `docs/notes/item-registry.md`: new **i96** row.
- ROADMAP Current State + the Phase-3 milestone row: note i96 landed.

---

## PR B — i82 the TFTP client demo disk (increment 3)

### B1. Go authority: `tools/netboot-oracle/client/client.go`

A new `client` package with a `Client` dispatcher that composes the
already-host-verified `tftp.ClientFront` (ARP-for-server → RRQ) +
`tftp.ClientLoop` (DATA/ACK receive + SAS retransmit) into one fetch driver:

```
func (c *Client) Next(rx []byte) (tx []byte, done bool)  // the frame-in/out step
  - phase ARP:  rx is the ARP reply -> learn MAC; tx = RRQFrame
  - phase XFER: rx is a DATA frame  -> tx = ACK (or ERROR(5)); done on short final
  - the first tx (no rx) is the ARP request
```

Model it on how `server.Server.OnFrame` composes sub-responders; this is the
client dispatcher the ROADMAP NEXT ACTION says to "build first if missing". Tests
`client/client_test.go`: full ARP→RRQ→DATA×N→done; wrong-IP ARP ignored;
unknown-TID DATA → ERROR(5); SAS retransmit on timeout.

### B2. Z80 port: `src/netboot/netboot_client.asm` (`client_run_once` + `client_main`)

A *new* dispatcher composing the host-verified `build_udp_frame` /
`build_arp_request` / `tftp_client` (build_rrq/parse_oack) + the receive-side
logic from `tftp_client_loop.asm` (ported inline, since including the loop file
collides) + `encdrv.asm`. Phases: send ARP → recv ARP (learn MAC) → send RRQ →
recv DATA/ACK loop → on completion, **B-DOS write-out** via `bdos_seam.asm`
(`bdos_fill_save_uifa` + the `bdos_save_hook` HSAVE behind `NETBOOT_HOSTTEST`).

The accumulated file bytes go to a staging buffer (as `tftp_client_loop.asm` does
with `STAGING`); on the short final block, `client_main` calls the B-DOS seam to
HSAVE the staged image to Trinity storage under the requested name. The HSAVE
hook dispatch stays behind `NETBOOT_HOSTTEST` (not host-verifiable — no
ROM/SAMDOS in the harness); the **field arithmetic** (`bdos_fill_save_uifa`) IS
host-verified by the existing i93 `bdos_seam_test.go`.

### B3. Host verification: `tools/netboot-oracle/z80/netboot_client_test.go`

Drive `client_run_once` over the i80 emulation against `client.Client`:
ARP request emitted; inject ARP reply → RRQ emitted; inject DATA blocks → ACKs
emitted byte-for-byte; short final block → done + the staged bytes equal the
file. The B-DOS HSAVE is **not** exercised here (no hooks in the harness) — assert
only the wire side + the staged buffer contents; mark the HSAVE as Pete's test.

### B4. Build wiring

`Makefile`: `netboot-client` / `netboot-client-boot` / `netboot-client-disk`
(`-netboot-name client`). Add to `netboot-z80-routines`.

### B5. Docs + registry

- `docs/notes/netboot-trinity-testing.md`: fill in "**Increment 3 — the client
  (i82)**" — build the disk, run any TFTP server on another machine holding a
  `.mgt`, boot the client pointed at it, watch the fetch, then the file lands on
  Trinity storage (Pete's on-hardware test). Mark the B-DOS write + the
  on-hardware run as Pete's test.
- `src/netboot/README.md`: add `netboot_client.asm`.
- `docs/notes/item-registry.md`: update **i82** (mark the client boot disk done).
- ROADMAP Current State + the Phase-3 milestone row.

---

## Sequencing + discipline

- One increment per PR (A then B). Foreground-CI each; spawn the §3 pre-merge
  reviewer; record the verdict with `gh pr review --comment`; merge with
  `--merge --delete-branch`; fast-forward local main; only then start B.
- Verify locally before each push: `cd tools/netboot-oracle && go test ./...`
  (oracle), `make ci-netboot-z80` (the Z80 harness), and build each disk
  (`make netboot-serve-disk` / `netboot-client-disk`) to confirm it assembles.
- Never weaken a test to stay green; the B-DOS hook dispatch + on-hardware run are
  honestly marked Pete-gated, not faked.
- This plan is deleted by the PR that completes the second increment (PR B).
</content>
