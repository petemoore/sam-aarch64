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
# -> build/netboot_serve.mgt   (B-DOS boot + AUTO + the demo server at &8000)
```

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

## Later increments (placeholders — filled in as they land)

- **HTTP / HTTPS provisioning (i70 / i88).** Fetch the Pi firmware blobs onto the
  SAM directly, so the store is self-provisioned.
