# Phase 3: TFTP bootstrap over direct SAM↔Pi LAN

**Status**: design direction sketch, 2026-05-27. Pete's architectural call captured before context drift. No code yet — Phase 3 ordering is after M3 + M4 + on-SAM editor (Phase 2).

## The shape of Phase 3

Once the SAM can edit and assemble spectrum4 source on-disc, the final piece is shipping the assembled `.img` to a Raspberry Pi 400 to actually boot it. The TFTP layer.

## Network topology

**Direct Ethernet cable between SAM (Quazar Trinity interface) and Pi 400. No switch, no home network, no internet.**

```
┌─────────────┐                 ┌─────────────────┐
│  SAM Coupé  │   crossover     │  Raspberry Pi   │
│  + Trinity  │ ◄────────────►  │      400        │
└─────────────┘   or Auto-MDIX  └─────────────────┘
```

Why this shape:

- **No TCP/IP stack on SAM**. SAMDOS doesn't have one; the spectrum4 kernel doesn't have one. Bringing up a general-purpose IP stack with DHCP, ARP, routing, etc. would be enormous scope creep for a one-way file shipment.
- **No internet expectation**. The Pi 400 in this setup is a target machine for our kernel, not a general-purpose network host. The SAM has no use for the wider internet during this workflow.
- **Both endpoints are link-local**. Fixed IPs (e.g. SAM = `192.168.1.1/24`, Pi = `192.168.1.2/24`), no DHCP, no DNS, no routing tables. The Pi is reachable at exactly one address.
- **Maximum simplicity in the SAM-side protocol stack**. Just enough Ethernet framing + UDP + TFTP to ship a single file from SAM to Pi (and maybe receive ACKs). Probably ~500 lines of Z80 total.

## Protocol layers, bottom-up

1. **Ethernet framing** — Trinity hardware accepts/produces raw Ethernet frames via the on-board controller (probably an ENC28J60 or similar SPI-attached chip; the port addresses are in `[[trinity-hardware]]` memory entry). Source MAC = SAM's, destination MAC = Pi's (known from one-time configuration, or learned via ARP if we want that little luxury).
2. **ARP** — optional. With fixed peer-MAC known in advance, can be skipped entirely. Including a minimal ARP responder (~20 lines) means the Pi's standard network stack can discover us without manual configuration.
3. **IPv4** — only the absolute minimum: source/dest IP, no fragmentation, no options. Header checksum is the one nontrivial bit.
4. **UDP** — trivial header, checksum optional (and often disabled on IPv4-over-LAN).
5. **TFTP (RFC 1350)** — only the WRQ (write request) + DATA + ACK opcodes. We're shipping; no need for RRQ or OACK. ~50 lines of Z80 protocol state machine.

## Reference implementation

`reference/trinload/` (or `~/git/trinload`, the [simonowen/trinload](https://github.com/simonowen/trinload) source) is the canonical SAM-side TFTP client implementation. Worth reading end-to-end before designing ours — it solves exactly this problem, has been working since the Trinity hardware existed, and tells us:

- How Trinity's SPI register set is wired and what the init dance looks like.
- The conventional buffer sizing for a 512-byte-payload TFTP transfer.
- The error-recovery shape: timeout/retry on missed ACK, how many retries before giving up.
- The MAC-address convention (does trinload hardcode it, prompt the user, ARP for it?).

We may be able to literally lift trinload's protocol layer with light modifications — its main loop becomes our "ship release.img" pathway.

## Pi-side configuration

The Pi 400 runs Linux on bare-metal-ish (or a minimal U-Boot pre-stage), and just needs:

- A TFTP server (`tftpd-hpa` or similar) bound to the link-local IP.
- The `tftpboot` directory it serves accepts our `release.img` upload.
- The Pi's bootloader configured to TFTP-load that image on next reboot.

This is all standard Pi/Linux config; no special-snowflake software on the Pi side. The SAM is the only host doing unusual things.

## Why this beats "real" networking

- **No subnet, gateway, DNS, DHCP configuration**. Both sides have fixed IPs literally compiled in. SAM-side becomes a `defb` table.
- **No security model needed**. There's no other machine on the wire.
- **Cable in, cable out**. User experience: plug a single Ethernet cable, both lights come on, press "ship" in our editor, image lands on the Pi.
- **Trinity hardware was designed for exactly this kind of point-to-point work**. We're not fighting the hardware's affordances.

## What this design does NOT cover (out of scope for Phase 3)

- Two-way file transfer (Pi → SAM). One-shot ship-and-boot is the primary workflow.
- Multiple Pi targets. If a future need surfaces, manual reconfig of the destination IP is fine.
- Wireless / 802.11. Cable only.
- Internet access from SAM. Not even on the table.
- Encryption / authentication. The cable is the security boundary.

## Open questions for future-Pete

1. **MAC address handling**: hardcode the Pi's MAC, ARP for it, or prompt the user once? trinload's choice is the natural default.
2. **Reboot trigger**: does the Pi auto-reload after TFTP upload, or does the SAM also need to send an "execute" signal? Probably a Pi-side cron / watchdog timer; SAM doesn't care.
3. **Editor integration**: a single key in the on-SAM editor that says "ship current build to Pi", with the image path baked into the project config.

## Ordering relative to other Phase 3 work

This is the *only* Phase 3 work item that's been concretely sketched so far. If/when more Phase 3 items surface (e.g. on-SAM Pi serial console, on-Pi disk-image inspection helpers), they go in adjacent design notes and the ROADMAP table grows.

## Related

- `[[trinity-hardware]]` — Trinity port addresses and SPI access details.
- `[[future-roadmap]]` — broader sam-as-daily-driver vision.
- `reference/trinload/` source — the canonical SAM-side TFTP implementation.
