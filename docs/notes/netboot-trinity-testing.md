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

## Later increments (placeholders — filled in as they land)

- **Increment 2 — the netboot server (i86 DHCP + i83 TFTP + i93 store).** Boot the
  server disk, point a Pi on the same LAN at netboot, and the Pi pulls its boot
  files from the SAM. Pass = the Pi reaches the kernel. (The real B-DOS RST-8 hook
  dispatch and the real ENC28J60 streaming of multi-MB files are confirmed here.)
- **Increment 3 — the client (i82).** Boot the client disk, point it at a TFTP
  source, and it fetches a file into a B-DOS record. Pass = the file lands on the
  SAM's storage byte-correct.
- **HTTP / HTTPS provisioning (i70 / i88).** Fetch the Pi firmware blobs onto the
  SAM directly, so the store is self-provisioned.
