# Testing the netboot programs on real Trinity hardware

**Purpose:** the hands-on guide for running the Phase-3 netboot programs on a
real SAM Coupé + Quazar Trinity, on Pete's home network. Each netboot increment
ships a **bootable disk** and a short section here: build the disk, what to put
where, what to do from the Pi / another machine, and what a pass looks like.

The host harness (`make ci-netboot-z80`) proves the **protocol logic** and the
**ENC28J60 wire I/O over the i80 emulation** byte-for-byte against the Go
authority. What it cannot prove — and what these on-hardware tests confirm — is
the real ENC28J60 silicon timing, the real B-DOS RST-8 hook dispatch, the EEPROM
config read, and an end-to-end Raspberry Pi netboot. **Those are Pete's tests on
real Trinity; this doc is how to run them.** Emulation-verified is not
hardware-verified (CLAUDE.md §5).

Background: [`trinity-capabilities.md`](trinity-capabilities.md) (the ENC28J60 /
EEPROM / SD hardware), [`pi-netboot-capture-analysis.md`](pi-netboot-capture-analysis.md)
(the DHCP+TFTP oracle), [`../specs/phase3-delivery-design.md`](../specs/phase3-delivery-design.md)
(the confirmed design).

---

## Prerequisites (all increments)

- A SAM Coupé with a Quazar Trinity interface fitted, Ethernet cable connected to
  your LAN (or a direct cable to the Pi for the point-to-point topology).
- The Trinity EEPROM holds a **"Trinity Network "** chunk with the SAM's MAC + IP
  (the same settings trinload uses). The netboot programs read their identity
  from it — `sam_mac = chunk+0` (6 bytes), `sam_ip = chunk+6` (4 bytes). If you
  have run trinload before, this is already set; otherwise write it with the
  Trinity config tool. Pick an IP on your LAN's subnet that nothing else uses.
- A second machine on the same LAN to observe from (your Mac/Pi). Handy tools:
  `arping`, `ping`, `tcpdump`/Wireshark.
- The dev toolchain to build the disk: `pyz80` + Go (the dev container has both).

**Prepare the FULL path — including data capture — before any hardware shot.**
A hardware run is expensive (it can wedge the SAM and cost power-cycles), so stage
everything end-to-end *first*:
- **The capture/recording mechanism must be proven working before the test starts**,
  not discovered missing mid-run. If the test reports data back over the wire (a
  served file, a UDP status, a TFTP transfer), **loopback-test the listener/receiver
  locally first** so you *know* it records. (Earned the hard way: a session started a
  run and only then found it had no working way to record the capture — too late.)
- **Don't ask whether trinload is running — test it.** Probe the SAM directly
  (`ping`/`arping 192.168.2.75`, or just attempt the transfer); a reachability check
  is faster and more reliable than a question.
- **Bound every wait that touches hardware.** A poll with no timeout can spin forever
  and hang the SAM (the SD busy-wait hang, i241). Verify the emulation exercises the
  timeout/abort path before trusting a routine on real silicon.
- **End a trinload-pushed program via `tr_terminate`, NEVER a raw `di; halt`.** A pushed
  program runs in trinload's RAM; `di; halt` **strands the SAM** (dead keyboard,
  power-cycle required) on every exit — including error exits — which makes the
  push→test→fix loop miserable. `tr_terminate` (`src/netboot/test_report.asm`, i228)
  probes the unmapped port `&007F` (floats high → `&FF` on real hardware; a distinct
  marker in the koron-go emulator) and does **`di; halt` under emulation** (so harness
  tests still stop) / **`RET` to trinload on hardware** (so the machine stays usable and
  re-pushable). Every exit path — success *and* error — ends with `jp tr_terminate`
  (set a diagnostic border first, and on an error path hold for an Esc so the operator
  can read it, then fall into `tr_terminate`). The **only** legitimate raw `di; halt` is
  SimCoupé's `-exitonhalt` in the *disk-booted* assembler self-tests
  (`src/assembler.asm`, `src/secd_probe.asm`) — those run under SimCoupé, not trinload.

Write the built `.mgt` to a real floppy (or load it in your usual way) and boot
it on the SAM. Every netboot disk **auto-runs on power-on** — a B-DOS boot, then
an AUTO BASIC that `CLEAR`s, `LOAD`s the program at &8000, and `CALL`s it.

---

## Increment 1 — the bring-up smoke test (i94)

**What it is.** The smallest possible "the Trinity Ethernet path is alive"
program. It boots, reads the SAM's MAC + IP from the EEPROM, initialises the
ENC28J60, then loops forever answering **ARP requests for the SAM's IP** — the
one observable network action that proves the whole wire path works end to end.

**Build the disk:**

```sh
make netboot-smoke-disk
# -> build/netboot_smoke.mgt   (B-DOS boot + AUTO + the smoke program at &8000)
```

**Run it:**

1. Boot `build/netboot_smoke.mgt` on the SAM + Trinity. On success the program is
   silently listening; on failure it sets a **border colour and halts** so you
   know which stage failed:
   - **red border** — no Trinity board detected, or no/blank "Trinity Network "
     EEPROM settings.
   - **blue border** — the ENC28J60 `drv_init` failed.
   - (A running smoke test flashes the driver's own tx/rx borders — green/black
     on receive, red/black on transmit — each time it answers an ARP.)

2. From another machine on the same LAN, ask "who has the SAM's IP?" and watch
   the SAM answer with its MAC:

   ```sh
   # Linux: send an ARP request directly (replace iface + IP)
   sudo arping -I eth0 192.168.x.y

   # macOS: prime the ARP cache with a ping, then read it back
   ping -c 3 192.168.x.y
   arp -n 192.168.x.y          # should now show the SAM's MAC
   ```

   `arping` printing replies — or `arp -n` showing the SAM's MAC for its IP — is
   the **pass**: the Trinity received a broadcast ARP request, the SAM matched its
   own IP, built an ARP reply, and `drv_write` put it on the wire. The Ethernet
   path comes up and talks.

3. (Optional) Watch the exchange on the wire:

   ```sh
   sudo tcpdump -i eth0 -n arp host 192.168.x.y
   ```

   You should see `ARP, Request who-has 192.168.x.y …` followed immediately by
   `ARP, Reply 192.168.x.y is-at <sam-mac>`.

**If it does not answer:** check the EEPROM IP matches the IP you are arping;
confirm the border is not red/blue (init failure); confirm the SAM and the
observer are on the same L2 segment (ARP does not cross routers). The reply uses
the SAM's real EEPROM MAC, so a wrong/blank EEPROM MAC is the usual culprit.

**What this confirms / does not.** A pass confirms: Trinity detected, ENC28J60
initialised with the real MAC, broadcast RX works, frame parse works, TX works —
the whole wire path. It does **not** exercise UDP, DHCP, TFTP, or B-DOS storage;
those are the next increments.

---

## Increment 2 — the integrated netboot server (i95: ARP + DHCP + TFTP)

**What it is.** The headline Phase-3 program: one main loop that reads a frame
and answers it as the **DHCP + TFTP server** a netbooting Pi needs (plus the
bring-up ARP from increment 1). It boots, reads the SAM's MAC + IP from the
EEPROM, sets a fixed DHCP pool on the SAM's own subnet, initialises the ENC28J60,
then loops forever serving:

- **ARP** who-has for the SAM's IP → an ARP reply (as increment 1);
- **DHCP** DISCOVER → OFFER, REQUEST → ACK — handing the Pi an address from the
  pool, with `siaddr` = the SAM (so the Pi learns the SAM is the TFTP server) and
  the fixed PXE option-43 "Raspberry Pi Boot" blob the Pi 4 boot ROM requires;
- **TFTP** RRQ → OACK (serve a stored file by name) / ERROR(1) (every miss, and
  keep serving — the boot ROM probes a long list of optional files), then the
  streamed DATA/ACK transfer to the short final block.

The dispatch is a byte-for-byte port of the Go authority
`tools/netboot-oracle/server/server.go::Server.OnFrame`; the full
DISCOVER→OFFER→REQUEST→ACK→ARP→RRQ→OACK→ACK→DATA session is host-verified over the
i80 emulation (`TestServerFullSession`).

**Build the disk:**

```sh
make netboot-server-disk
# -> build/netboot_server.mgt   (B-DOS boot + AUTO + the server at &8000)
```

**Run it:**

1. Boot `build/netboot_server.mgt` on the SAM + Trinity. As with the smoke test,
   a bring-up failure sets a **border colour and halts** (red = no Trinity / blank
   EEPROM settings; blue = `drv_init` failed). On success it is silently serving.

2. Point a Raspberry Pi (configured for network boot) at the SAM:
   - **Direct cable** (point-to-point): connect the Pi's Ethernet straight to the
     Trinity and power the Pi on. The SAM is the only DHCP server it can see.
   - **Shared LAN**: put the SAM/Trinity and the Pi on the same switch. Make sure
     no *other* DHCP server will answer the Pi's PXE DISCOVER first (the pool is
     tiny and the SAM does not arbitrate against another server) — for a clean
     test, an isolated switch or a direct cable is simplest.

   The store must already hold the files the Pi asks for (`start4.elf`,
   `fixup4.dat`, the kernel, the device tree, `config.txt`, …) under their flat
   root names; on real hardware those records are resolved through the B-DOS seam
   (i93). Until the store is provisioned (i70), expect the Pi to TFTP-probe and
   the SAM to ERROR(1) the missing files — which still confirms the DHCP + TFTP
   dispatch is alive.

3. Watch the exchange from another machine on the LAN:

   ```sh
   sudo tcpdump -i eth0 -n 'arp or port 67 or port 68 or port 69'
   ```

   A working session shows, in order: the Pi's `ARP who-has` answered by the SAM;
   `BOOTP/DHCP … Discover` → `… Offer`, `… Request` → `… ACK` from the SAM; then
   `TFTP … RRQ "start4.elf" octet …` → an OACK and a stream of DATA/ACK pairs (or
   `ERROR (1) File not found` for files not yet in the store).

**What a pass looks like.** With the store provisioned, the Pi reaches its kernel
(the firmware rainbow splash, then the boot). This is the headline Phase-3 result.
With the store empty/partial, a pass of the *server mechanism* is the clean
DHCP DORA + the TFTP RRQ/OACK (or RRQ/ERROR-keep-serving) exchange on the wire —
the Pi talking to the SAM as its boot server.

**What this confirms / does not (host-verified vs hardware-gated).** The host
harness already proves the ARP/DHCP/TFTP dispatch + the streamed transfer +
serve-by-name + ERROR(1)-on-miss byte-for-byte. This on-hardware run confirms what
the harness cannot: the real ENC28J60 silicon timing under back-to-back DATA/ACK,
the EEPROM config read, the **B-DOS RST-8 hook dispatch** that backs the real file
source (the harness uses an in-RAM source), and a real Pi accepting the SAM's
OFFER + booting. Emulation-verified is not hardware-verified (CLAUDE.md §5).

---

## Serve-files demo (i96) — a plain-TFTP server, no Pi needed

**What it is.** A focused demo that turns the SAM + Trinity into an ordinary TFTP
server. It serves a couple of small files **baked into the program** to any TFTP
client on the LAN — busybox/BSD `tftp`, `curl tftp://…`, a Windows `tftp` client.
**TFTP only: no DHCP, no Pi, no PXE.** That makes it the easiest netboot program to
prove end-to-end: you do not need a Raspberry Pi or any DHCP setup — just a machine
with a stock `tftp`/`curl` client and the SAM's IP. It is distinct from the Pi
netboot server (increment 2), which adds DHCP + the PXE option-43 blob a Pi
requires.

It boots, reads the SAM's MAC + IP from the EEPROM, provisions the demo files,
initialises the ENC28J60, then loops forever serving:

- **ARP** who-has for the SAM's IP → an ARP reply (so a plain TFTP client can
  resolve the SAM's MAC with no DHCP);
- **TFTP RRQ** → the file by name. A *bare* RRQ with no options (what a classic
  `tftp get` sends) is answered per RFC 2347 with **DATA block 1 directly** at the
  512-byte default — no OACK; an RRQ that *does* request options (e.g. `curl`'s
  `tsize`) is answered with an **OACK** then the streamed transfer. A request for a
  name that is not baked in gets **ERROR(1) File not found**, and the server keeps
  serving.

The baked-in files are `hello.txt` and `readme.txt` (a couple of lines each).

**Build the disk:**

```sh
make netboot-serve-disk
# -> build/netboot_serve.mgt   (B-DOS boot + AUTO + the combined RRQ+WRQ server at &8000)
```

The disk is the i121i **.mgt packaging vessel** — sibling of the i121d trinload
code block. Its AUTO BASIC loads the serve binary at `&8000`, then overlays a tiny
`SERVE_CONFIG` CODE file (`cfg`) at the `SERVE_CONFIG` address, so the disk carries
its WRQ record-placement **strategy** explicitly (the runtime image then matches a
host-patched trinload push exactly). The default is highest-free; build a disk with
a different strategy via `make netboot-serve-disk NETBOOT_STRATEGY=lowest` (or
`NETBOOT_STRATEGY=explicit:N`). The strategy governs which **free** Trinity record a
`tftp put` writes to — a named record is never overwritten (write-to-free-only, q30).

**Record vessel (i332):** the BASIC-auto disk above is the **floppy** vessel only —
stored as a Trinity record it does NOT boot (B-DOS's record boot runs the AUTO\*
file directly and never fires a BASIC RUN leg; the result is a silent livelock).
To store a **boot_record-bootable** serve record, build the CODE-auto shape:

```sh
make netboot-serve-record        # NETBOOT_STRATEGY=… works here too
# -> build/netboot_serve_record.mgt   (one auto-exec CODE file, config baked in)
```

then `tftp put build/netboot_serve_record.mgt trinity-sam-disks/<name>.mgt` (or
sd-push) and boot it with `tools/trinload-push/boot-record.py`. The emulation gate
for this exact artifact is `TestBootRecordServeRecordVessel`
(tools/netboot-oracle/z80).

**Run it:**

1. Boot `build/netboot_serve.mgt` on the SAM + Trinity. As with the smoke test, a
   bring-up failure sets a **border colour and halts** (red = no Trinity / blank
   EEPROM settings; blue = `drv_init` failed). On success it is silently serving.

2. From any machine on the same LAN, fetch a file by name:

   ```sh
   # classic tftp client (bare RRQ -> DATA, no options)
   tftp <sam-ip>
   tftp> binary
   tftp> get hello.txt
   tftp> get readme.txt
   tftp> quit
   cat hello.txt          # "Hello from a SAM Coupe over Trinity TFTP!"

   # or curl (sends a tsize option -> OACK path)
   curl -o hello.txt tftp://<sam-ip>/hello.txt
   curl tftp://<sam-ip>/readme.txt
   ```

   A request for a name that is not baked in returns "File not found" and the
   server keeps serving:

   ```sh
   tftp> get nope.txt     # Error code 1: File not found
   tftp> get hello.txt    # still works
   ```

3. (Optional) Watch the exchange:

   ```sh
   sudo tcpdump -i eth0 -n 'arp or port 69'
   ```

   You should see the client's `ARP who-has` answered by the SAM, then
   `TFTP … RRQ "hello.txt" octet …` followed by an OACK (curl) or a DATA stream
   (bare tftp) and the ACKs.

**What a pass looks like.** `hello.txt` / `readme.txt` arrive byte-for-byte
identical to the baked-in text. Both the bare-RRQ (`tftp`) and the optioned-RRQ
(`curl`) paths fetch correctly; a missing name returns File-not-found without
killing the server.

**What this confirms / does not (host-verified vs hardware-gated).** The host
harness already proves the ARP reply + the bare-RRQ→DATA path + the optioned-RRQ→
OACK path + ERROR(1)-on-miss byte-for-byte over the i80 emulation
(`netboot_serve_test.go`, `make ci-netboot-z80`). This on-hardware run confirms what
the harness cannot: the real ENC28J60 silicon timing, the EEPROM config read, and a
real stock TFTP/curl client interoperating with the SAM. Emulation-verified is not
hardware-verified (CLAUDE.md §5).

### Network debug step-markers (i271) — localize a hang off the wire

For diagnosing a hardware hang (the i270 WRQ-write bottleneck), build the serve with
the **network debug step-markers** compiled in:

```
make netboot-serve-boot-debug   # -> build/netboot_serve_boot_debug.bin
```

This is a **drop-in replacement** for `netboot_serve_boot.bin` (push it the same way
— it is the same boot image, byte-identical apart from the markers; the production
build is unaffected). At each WRQ / SD-write step it **broadcasts a 6-byte "SDBG" UDP
packet** to `255.255.255.255:9001` carrying the step code, so an agent reads how far
the SAM got *without a screen*, and a hang localizes to the **last marker seen**.

Watch the markers on any LAN machine:

```
sudo tcpdump -l -n -i eth0 'udp port 9001'   # each marker is a 48-byte broadcast
```

The payload is `'S','D','B','G'` + version(1) + **marker code(1)**. The codes
(`src/netboot/dbg_marker.asm`):

| code | step |
|------|------|
| `0x10` | `handle_wrq` entered |
| `0x11` | free record claimed + ENC re-armed |
| `0x12` | no free record → ERROR(3) |
| `0x13` | about to send the OACK / ACK-0 handshake |
| `0x20` | a DATA block accepted, about to sink/stage |
| `0x30` | final block: entering `wd_finalize` |
| `0x31` | record validated + claimed → final ACK |
| `0x32` | invalid image → ERROR(3) |
| `0x40` | `tftp.done` control → returning to trinload |

So for the i270 symptom (WRQ received, zero reply): seeing `0x10` but not `0x13`
means the hang is in the claim/SD-list-read phase; `0x10`+`0x13` then silence on the
DATA stream points at the SD write. The marker emission is host-verified in
`netboot_serve_dbg_test.go`; using it to localize a real hang stays a hardware run.

---

## Increment 3 — the TFTP client (i82): fetch a .mgt and save it to Trinity

**What it is.** The other direction: instead of *serving* files, the SAM is a
TFTP **client** that fetches a file (a `.mgt` disk image) from a TFTP server on the
LAN and **writes it to Trinity storage** via the B-DOS record hooks. It boots,
reads the SAM's MAC + IP from the EEPROM, broadcasts an ARP request to learn the
server's MAC, sends a TFTP read request for the configured filename, receives the
streamed DATA blocks (ACKing each, accumulating the bytes), and on the short final
block HSAVEs the assembled image to Trinity storage under that name. On success it
sets a **green border** and halts.

**Configure the target first.** The fetch target is fixed in the program (edit and
rebuild for your network): in `src/netboot/netboot_client.asm`, `cl_server_ip` is
the TFTP server's IP and `cl_filename` is the file to fetch (default
`recovery.mgt`). Set them to a server you control and a file it holds, then
`make netboot-client-disk`.

**Build the disk:**

```sh
make netboot-client-disk
# -> build/netboot_client.mgt   (B-DOS boot + AUTO + the client at &8000)
```

**Run it:**

1. On another machine on the same LAN, run a TFTP server that holds the `.mgt`
   image you want to fetch:

   ```sh
   # e.g. a one-off read-only tftpd serving the current directory
   sudo in.tftpd -L -s "$PWD" &
   ls recovery.mgt          # the file the SAM will ask for
   ```

2. Boot `build/netboot_client.mgt` on the SAM + Trinity. A bring-up failure sets a
   **border colour and halts** (red = no Trinity / blank EEPROM settings; blue =
   `drv_init` failed). On success it broadcasts the ARP, sends the RRQ, and pulls
   the file; a **green border** on completion means the image was HSAVEd to Trinity
   storage.

3. (Optional) Watch the exchange:

   ```sh
   sudo tcpdump -i eth0 -n 'arp or port 69'
   ```

   You should see the SAM's `ARP who-has <server-ip>` answered by the server, then
   `TFTP … RRQ "recovery.mgt" octet …` from the SAM followed by an OACK and a
   stream of DATA/ACK pairs, ending on a short final block.

**What a pass looks like.** The green border, and the fetched `.mgt` present on the
SAM's Trinity storage (browse it from B-DOS — `DIR` the record), byte-correct
versus the file the server served.

**What this confirms / does not (host-verified vs hardware-gated).** The host
harness already proves the client's **wire side** byte-for-byte over the i80
emulation (`netboot_client_test.go`, `make ci-netboot-z80`): the broadcast ARP
request, the RRQ after the ARP reply, the ACK cadence, and the **accumulated bytes
in the staging buffer** all match the Go authority `client.Client`. What it cannot
prove — and what this on-hardware run confirms — is the **B-DOS RST-8 HSAVE
write-out** (the harness has no ROM/SAMDOS/RST 8, so the bytes-to-storage step is
unverified until real Trinity; the i93 seam's field arithmetic *is* host-verified,
only the hook dispatch is not), the real ENC28J60 silicon timing, the EEPROM config
read, and the end-to-end fetch with a real TFTP server. Emulation-verified is not
hardware-verified (CLAUDE.md §5).

---

## HTTP fetch (i70): self-provision a firmware blob over HTTP

**What it is.** The firmware self-provisioning capstone: instead of fetching over
TFTP, the SAM is an **HTTP/1.0 client** that fetches a firmware blob from a plain
HTTP server and **writes it to Trinity storage** via the B-DOS record hooks. It
boots, reads the SAM's MAC + IP from the EEPROM, broadcasts an ARP request to learn
the server's MAC, opens a TCP connection (SYN → SYN-ACK → the handshake-completing
ACK that carries the `GET`), receives the streamed response (ACKing each segment,
accumulating the bytes), ends on the server's FIN (HTTP/1.0 closes after the body),
parses the response, copies the body past the `\r\n\r\n` header into a section-C
staging buffer, and HSAVEs it to Trinity storage under the configured name. On
success it sets a **green border** and halts.

**Configure the target first.** The fetch target is fixed in the program (edit and
rebuild for your network): in `src/netboot/netboot_http.asm`, `ht_server_ip` is the
HTTP server's IP and `ht_out_name` is the Trinity output filename (default
`firmware.bin`); the HTTP request path + Host header are `HTTP_PATH` / `HTTP_HOST`
in `src/netboot/http_get.asm` (defaults `/firmware/start4.elf` + `fw.local`). Set
them to a server you control and a blob it serves, then `make netboot-http-disk`.

**Build the disk:**

```sh
make netboot-http-disk
# -> build/netboot_http.mgt   (B-DOS boot + AUTO + the fetcher at &8000)
```

**Run it:**

1. On another machine on the same LAN, run a plain HTTP server that serves the blob
   at the configured path:

   ```sh
   # e.g. a one-off static server rooted at the dir holding ./firmware/start4.elf
   python3 -m http.server 80
   ```

2. Boot `build/netboot_http.mgt` on the SAM + Trinity. A bring-up failure sets a
   **border colour and halts** (red = no Trinity / blank EEPROM settings; blue =
   `drv_init` failed). On success it broadcasts the ARP, opens the connection, sends
   the `GET`, and pulls the body; a **green border** on completion means the blob was
   HSAVEd to Trinity storage.

3. (Optional) Watch the exchange:

   ```sh
   sudo tcpdump -i eth0 -n 'arp or port 80'
   ```

   You should see the SAM's `ARP who-has <server-ip>` answered by the server, then a
   TCP handshake, a `GET <path> HTTP/1.0` from the SAM, the response stream, and the
   FIN teardown.

**What a pass looks like.** The green border, and the fetched blob present on the
SAM's Trinity storage (browse it from B-DOS — `DIR` the record), byte-correct
versus the body the server served (the response body, past the HTTP header).

**What this confirms / does not (host-verified vs hardware-gated).** The host
harness already proves the fetcher's **wire side** byte-for-byte over the i80
emulation (`netboot_http_test.go`, `make ci-netboot-z80`): the broadcast ARP
request, the SYN, the handshake-completing ACK+`GET`, the response ACK cadence, the
FIN-ACK, and the **accumulated body in `CONN_DATA`** all match the Go authority
`http.Fetcher`. What it cannot prove — and what this on-hardware run confirms — is
the **B-DOS RST-8 HSAVE write-out** (the harness has no ROM/SAMDOS/RST 8, so the
bytes-to-storage step is unverified until real Trinity; the i93 seam's field
arithmetic *is* host-verified, only the hook dispatch is not), the **section-C source
paging** of the staging buffer, the real ENC28J60 silicon timing, the EEPROM config
read, and the end-to-end fetch against a real HTTP server. Emulation-verified is not
hardware-verified (CLAUDE.md §5).

---

## SAMBOOT ROM+EEPROM capture (i87a) — agent runs the i173 dumper

Capture the patched system ROM + the Trinity EEPROM off the real SAM, so the boot
chain can be analysed (i87b) and the EEPROM backed up before any flash (i135c).
The dumper is **pushed over trinload** (not booted from disk) and serves the dumps
over TFTP as 16 KB regions the host pulls and concatenates. It reads ROM/EEPROM
**read-only** — it never writes the card. The capture is **agent-driven**
(network-only, read-only). Charter: `docs/specs/samboot.md` §6 step 2.

> **CAPTURE COMPLETE (i87a).** `eeprom.bin` (131072 B, verified real — the "Trinity
> Network " chunk + the SAM's actual MAC `02:54:52:49:4e:bc`), `rom0.bin` (low 16 KB,
> **i87a-b1**) and now **`rom1.bin`** (high 16 KB, **i87a-b2**, 2026-06-21) are all
> captured and stashed in `~/sam-archive/samboot-capture/` — `rom.bin = rom0.bin +
> rom1.bin` (32768 B) is the full patched system ROM.
>
> The original i173 dumper crashed the SAM capturing rom1 (its `dumper_read_rom1`
> used scratch page `P-1 = page 0` + a `ldir`-clobbered register, killing trinload).
> The **i188** redesign reads ROM1 into STAGE's own free page (P+1), preserving the
> registers + entry LMPR — reproduced + fixed in emulation via the **i181** paged
> harness, then **hardware-confirmed** by this recapture: rom1 served cleanly
> (genuine Z80 ROM — most-common byte `0xCD`=CALL), and the SAM kept serving
> afterwards (eep0 re-pulled identical), proving no clobber. rom1 is now safe to
> pull from the current (`netboot_dumper.bin`) dumper.

> **Push recipes at a glance:** run `make trinpush-help` — it prints the canonical
> invocations (the executable pusher scripts and `tools/hardware-shot/run-shot.sh`)
> so the exact command never has to be looked up. Every push needs `DEPLOY_CHECKED=1`
> (the deploy-guard hook prints the hardware-readiness checklist without it).

Push the dumper with `tools/trinload-push/trinload-push.py` (the py3 pusher), then
pull the regions over TFTP. The full procedure:

1. **Build the pushable dumper:**
   ```sh
   make netboot-dumper               # -> build/netboot_dumper.bin (org &8000)
   ```
2. **Push it over trinload** (trinload must be RUNNING on the SAM — `192.168.2.75`):
   ```sh
   tools/trinload-push/trinload-push.py 192.168.2.75 build/netboot_dumper.bin 1 0x8000
   ```
   (py3; the upstream `~/git/trinload/test/trinload.py` is py2-only — see
   `tools/trinload-push/README.md`.) The dumper runs, reads its MAC/IP from the
   `"Trinity Network "` EEPROM chunk, inits the ENC28J60, and loops serving. Press
   **Esc** on the SAM to `RET` cleanly back to trinload (so it can be re-pushed).
3. **Pull the regions** from the host (the SAM serves plain TFTP on port 69; use a
   tftp client or `curl tftp://…` — the Pi has `curl` but not `tftp`):
   ```sh
   for f in rom0.bin rom1.bin eep0.bin eep1.bin eep2.bin eep3.bin \
            eep4.bin eep5.bin eep6.bin eep7.bin; do curl -s -o $f tftp://192.168.2.75/$f; done
   ```
4. **Concatenate + check sizes:**
   ```sh
   cat rom0.bin rom1.bin > rom.bin                       # expect 32768 bytes
   cat eep0.bin eep1.bin eep2.bin eep3.bin \
       eep4.bin eep5.bin eep6.bin eep7.bin > eeprom.bin  # expect 131072 bytes
   ```
5. **Stash the artifacts** under `~/sam-archive/` (non-redistributable, like the
   existing B-DOS analysis — Colin's proprietary work; never commit them).

**Read-this caveat.** The **EEPROM** read path is emulation-verified (`dumper_test.go`)
and now **hardware-confirmed** — `eeprom.bin` is captured and is the **mandatory
backup before i135c**. The **ROM-paging** read was **hardware-first** (the flat
harness has no real paging), and the 2026-06-21 run proved one of those
`VERIFY ON HARDWARE` assumptions wrong: `rom0.bin` reads cleanly, but `rom1.bin`
(assumption A3 — "P-1 is a free RAM page") crashes the SAM (see the run-result note
above). This is exactly the gap **i181** closes (a harness LMPR/HMPR paging model +
trinload residency, to reproduce the crash and verify the fix in emulation) before
the **i188** redesign re-enables a safe ROM capture.

---

## Later increments (placeholders — filled in as they land)

- **HTTPS provisioning (i88, stretch).** Fetch the Pi firmware blobs over TLS (e.g.
  direct from GitHub), so the store can be self-provisioned from an HTTPS-only
  source. Plain-HTTP provisioning (i70, above) already covers the Raspberry Pi apt
  archive, so this stays a genuine stretch.
