# Phase 3 — end-to-end delivery design (the self-hosting boot/delivery loop)

**Item:** i84 · **Type:** design spec · **Status:** **design direction CONFIRMED
by Pete (2026-06-14)** — the two open decisions are resolved (q12 = Model A only;
q11 = drop the Colin dependency, DHCP is in scope, bootstrap via Pete's patched
SAM ROM). The remaining gate is the **final go-ahead to begin implementation**;
the only open work is *research*, not a Pete decision (the exact Pi netboot file
list + DHCP handshake conventions, settled during the i83/i86 build).

**Relationship to the prior sketch:** this supersedes
[`phase3-tftp-design.md`](phase3-tftp-design.md) (the 2026-05-27 ship-only
direction sketch, which framed Phase 3 as a one-way *push* of `release.img` to a
Pi already running `tftpd` — now rejected as Model B, §6.5). On the implementation
go-ahead the sketch is deleted.

**Reads first:** [`../notes/tftp-protocol-research.md`](../notes/tftp-protocol-research.md)
(i82 — the TFTP RFC corpus + trinload analysis, the protocol authority) and
[`../notes/trinity-capabilities.md`](../notes/trinity-capabilities.md) (the
ENC28J60 / EEPROM / SD hardware facts). This document composes them into an
architecture; it does not restate their detail.

---

## 1. Goal

Close the loop so the SAM Coupé is a *self-hosting* aarch64 development machine:
the daily development cycle never leaves the SAM, and no host-side tools are
required. Two opposite data flows over the one Quazar Trinity ENC28J60:

- **Outbound (serve) — the headline (i83).** The SAM is a **TFTP server** that
  network-boots a *bare* Raspberry Pi: it serves the Pi firmware blobs the Pi's
  boot ROM requests, plus the assembled `spectrum4.img` kernel. This is what lets
  "edit → assemble → boot on the Pi" happen entirely from the SAM.
- **Inbound (fetch) — the bootstrap enabler (i82).** The SAM is also a **TFTP
  client**, used to pull images *onto* the SAM and avoid the SD-card dance.
  trinload was already almost a TFTP client; making it a *real* one keeps it
  broadly compatible with standard boot mechanisms.

Both ride the same trinload-derived ENC28J60 / ARP / IPv4 / UDP stack and differ
only in TFTP role. Phase 3 also needs two supporting pieces the old sketch missed:
a **minimal DHCP responder** (§6.3 — mandatory for Pi-3-class netboot and for
serving several machines on a LAN) and an on-SAM **HTTP fetch** for one-time
firmware self-provisioning (§7 — keeps provisioning host-tool-free).

## 2. The loops at a glance

```
 OUTBOUND  (i83 — the SAM is the netboot server: DHCP + TFTP)
   ┌──────────────┐  DHCP DISCOVER/REQUEST   ┌──────────────────────────┐
   │  Raspberry    │ ───────────────────────► │        SAM Coupé          │
   │  Pi (3/4/400/ │ ◄─────────────────────── │  DHCP responder (i86) +   │
   │  …) boot ROM  │   DHCP OFFER/ACK          │  TFTP *server* (i83)      │
   │  = TFTP client│                           │                           │
   │               │  RRQ "start4.elf" …       │  serves whatever file is  │
   │               │ ───────────────────────► │  requested, by name, from │
   │               │ ◄─────────────────────── │  a flat Trinity store     │
   │               │   OACK/DATA               │  (firmware + spectrum4.img)│
   └──────────────┘                           └──────────────────────────┘
         │ executes the kernel                            ▲
         ▼                                                │ store populated by ↓

 INBOUND  (i82 — the SAM is a TFTP client)        SELF-PROVISIONING (i70)
   ┌────────────┐  RRQ/OACK/DATA  ┌────────────┐    SAM ──HTTP over uIP──► internet
   │ TFTP server│ ──────────────► │ SAM Coupé  │    (fetch Pi firmware once → store)
   │ (e.g. Mac) │ ◄────────────── │ TFTP client│
   └────────────┘     ACK         └────────────┘

 Topologies (§4): direct SAM↔Pi cable, OR SAM + several Pis on a shared LAN.
```

## 3. Bootstrap — how the SAM reaches Trinity storage + runs the server at boot

The "no physical disk" property comes from **Pete's patched SAM ROM**: his SAM has
a modified system ROM installed that adds Trinity-interface support, so the Trinity
(and its SD storage, via B-DOS) is usable the instant the machine powers on — no
B-DOS floppy needed. The server disk lives in Trinity storage and autoboots.

- This is **not a Colin dependency.** We hold the materials to understand and
  reproduce the mechanism: the `trinity.mgt` disk (`~/sam-corpus/disks/trinity.mgt`,
  decoded `~/sam-corpus/outputs/trinity.txt`) carries code for all the Trinity
  features; `reference/bdos/` has the B-DOS 1.5a source; and the 1.5t Trinity-fork
  delta is analysed in `~/sam-archive/bdos/analysis/`. The one artifact still to
  capture is Pete's actual patched ROM — when his SAM is next online he will dump
  it to disk, and we reverse the patch then (**i87**).
- **Interim path without the patched ROM:** trinload's purpose is to load code over
  the network into RAM and run it (i82 §6.1). So a plain SAM + Trinity can boot a
  tiny trinload disk, pull the server/client code over the wire, and run it — the
  inbound loop is demonstrable before any ROM work.
- **Layering:** everything sits on trinload's ENC28J60 driver, ARP responder, and
  IPv4/UDP framing (i82 §6.8 catalogues the reusable routines).

## 4. Network topology and addressing — support both

The direct SAM↔Pi cable was the original wish, but a shared LAN is explicitly
**also** in scope, because it lets one SAM netboot **several** machines (Pete runs
both a Pi 400 and a Pi 3). The design must not hard-code a single peer.

- **Direct cable:** SAM ↔ one Pi, link-local. ENC28J60 is 10BASE-T half-duplex;
  Auto-MDIX is unconfirmed for the Trinity board (`trinity-capabilities.md` §6), so
  assume a crossover cable or an Auto-MDIX peer.
- **Shared LAN:** SAM + N Pis through a switch/hub. The SAM's DHCP responder hands
  out addresses from a small pool (mirroring Pete's `dnsmasq` range
  `192.168.50.10–.20`, server `192.168.50.1`); each Pi then TFTPs from the SAM.
  A hub here doubles as the packet-capture point (§9).
- The SAM holds one MAC/IP (the EEPROM "Trinity Network " chunk, i82 §6.7) and
  plays server toward the Pi(s); when used as a *client* (§5) it talks to an
  external server — never both roles in the same session.

**Reference architecture (the thing the SAM replaces):** Pete's Mac runs
`dnsmasq` providing **DHCP + TFTP from one process** bound to a USB NIC (`en7`),
serving `/private/tftpboot` (both Pi-3 and Pi-4 firmware sets + `spectrum4.img`).
The SAM server reproduces this `dnsmasq` behaviour in Z80.

## 5. Inbound — the SAM TFTP client (i82)

Protocol mechanics, packet layouts, options, the Sorcerer's-Apprentice fix, TID
handling, and the trinload reuse-map are specified in the i82 research note; only
the delivery decisions live here.

- Octet mode; RRQ option set `blksize=1428, tsize=0, timeout=2, windowsize=4`, with
  graceful fallback to RFC-1350 lock-step if the server sends no OACK (i82 §5.7).
- `tsize=0` returns the byte count in the OACK, so the Z80 pre-allocates the right
  number of B-DOS records before the first DATA block (i82 §4.2, §7.3).
- Received DATA → a staging page (HMPR) → B-DOS records via the unchanged hook
  surface (i62 verified SAMDOS↔B-DOS; i71 static-verified the Trinity SD fork). No
  raw SPI driver needed.
- **Also the interim firmware bridge:** until the on-SAM HTTP fetch (§7) exists, the
  client can pull the Pi firmware from any standard TFTP source (e.g. Pete's Mac)
  into the Trinity store, so the outbound server is not blocked on §7.

## 6. Outbound — the SAM netboot server (i83 + i86), Model A

The headline: boot a *bare* Pi over the network from images the SAM holds. This is
the harder role (the SAM answers requests it did not initiate) and is two
cooperating servers: a TFTP server and a small DHCP responder.

### 6.1 The Pi is the client; the SAM serves files by name

A Raspberry Pi boot ROM that network-boots **is the TFTP client**: it DHCPs to find
the server, then TFTP-requests the specific files it wants, *by name*. **The server
needs no model awareness** — it just serves whatever filename is requested if that
file exists in the store. The firmware filenames are already distinct across
families (Pi 3: `bootcode.bin`, `start.elf`, `fixup.dat`; Pi 4/400:
`start4.elf`, `fixup4.dat`, `bcm2711-rpi-400.dtb`; plus `config.txt` and the
kernel `spectrum4.img`), so **one flat store serves every model** — exactly how
Pete's `dnsmasq` works. There is no per-machine configuration on the SAM.

This means "support all TFTP-bootable aarch64 Pis" costs essentially nothing
extra in the server: the same serve-by-name loop covers Pi 3 / 3+ / 4 / 400 / CM /
Pi 5. The *only* model-dependent variable is **which files are present in the
store** — a provisioning choice (each project decides which firmware revision and
which files to ship, §7), not server logic. (Pi 3 and Pi 400 are the two Pete
owns, so they are the first-light targets; the rest follow for free.)

### 6.2 The TFTP server state machine

The delta from the i82 client: parse incoming RRQs (filename + mode + options),
answer options with an OACK where supported, and run the DATA-send / ACK-wait loop
(instead of building RRQs and receiving DATA). The ARP/IP/UDP framing and the
`ack_len`-style send primitive are the same trinload reuse. It serves several files
per boot and several MB total (the GPU firmware dominates), streamed block-by-block
from the Trinity store.

**Confirmed from the captures** ([`../notes/pi-netboot-capture-analysis.md`](../notes/pi-netboot-capture-analysis.md),
a real Pi 400 netboot): the server must implement the **OACK** path — the Pi boot
ROM negotiates `octet` + `tsize=0` (the server answers the file's real size) +
`blksize` (1024 and 1468 both seen); it does *not* use windowsize (that is a
client-side concern). It must serve **by name** from the flat store, return **TFTP
ERROR(1) for every miss and keep going** (the boot ROM probes a long list of
optional files — `recovery.elf`, `pieeprom.sig`, `dt-blob.bin`, … — and proceeds on
not-found; choking on a miss breaks the boot), and tolerate the Pi's
**serial-number subdirectory prefix** (`<serial>/start4.elf`) by 404-ing it so the
Pi falls back to root.

### 6.3 DHCP is required — the SAM must speak it (i86)

This is the key correction to the prior sketch. **Pi-3-class netboot has no static
path** — the boot ROM always broadcasts DHCP to learn its address, the TFTP server,
and the bootfile. Pete's own working setup confirms it: `dnsmasq` does DHCP *and*
TFTP. So the SAM must provide a **minimal DHCP/bootp responder** (item **i86**):
answer `DISCOVER`/`REQUEST` with an `OFFER`/`ACK` carrying an address from the pool,
the subnet, and the next-server (`siaddr` = itself), plus the Raspberry-Pi netboot
DHCP conventions — now **confirmed from a real capture**
([`../notes/pi-netboot-capture-analysis.md`](../notes/pi-netboot-capture-analysis.md)):
echo vendor-class **`PXEClient`** (opt 60 — present in every working capture, so echo it; strict necessity vs. the option-43 string is unverified),
send the fixed 32-byte **option-43 PXE blob** containing the literal `Raspberry Pi
Boot`, echo the client UUID (opt 97), and set `siaddr` to the TFTP server (no opt 67
bootfile needed — the Pi requests its own filenames). A uniform Pi convention,
**not** per-model config. It is incremental on trinload's
UDP stack (DHCP is two UDP broadcasts on ports 67/68), but it does mean the server
is "DHCP + TFTP", not TFTP-only. The Pi 4/400 static-EEPROM netboot path
(`TFTP_IP`/`CLIENT_IP`/…) is therefore *not* relied on — building DHCP covers every
model with one code path and matches Pete's proven flow.

### 6.4 Per-Pi netboot research (execution detail, not a Pete decision)

To populate the store and satisfy the handshake, the build needs the exact file
list each target Pi requests and the DHCP conventions — now **settled** from a real
Pi 400 capture, distilled into the implementation spec
[`../notes/pi-netboot-capture-analysis.md`](../notes/pi-netboot-capture-analysis.md)
(the exact DHCP option-43 blob, the OACK `tsize`/`blksize` options, the served-file
list, and the ERROR(1)-tolerant probe sequence). A **Pi 3** capture (item **i89**)
will confirm the older family the same way; its file set is already known from
Pete's `tftproot`. Model-general; no SAM-side per-model code.

### 6.5 Model B is rejected

Model B (SAM = TFTP *client* doing a WRQ to a `tftpd` on a Pi already running
Linux, plus a Pi-side reboot trigger) is **not pursued**: it needs a cooperating OS
and a host-side daemon on the Pi, which violates the self-hosting / no-host-tools
goal and does not boot a bare Pi. Recorded for context only.

## 7. Firmware self-provisioning (i70) — getting the Pi firmware into the store

Model A needs the Pi firmware blobs resident in the Trinity store. To keep this
host-tool-free, **the SAM fetches them itself over the network**, source-agnostic: a
plain **HTTP or TFTP** fetch from any reachable server covers the interim (e.g.
Pete's Mac, which already serves the firmware). It is a one-time / rare operation
(firmware is stable per chosen revision), **not on the daily-loop critical path**,
and lands *after* the client+server+DHCP core; the TFTP client (§5) bridges it
meanwhile. The host-side-tool alternative (a PC writing B-DOS records) is rejected —
it reintroduces a host dependency. uIP (i61) provides the TCP/IP the HTTP client
sits on.

**The canonical source is GitHub (HTTPS-only) → item i88 (a stretch goal).** The
purest form — and the one that lowers the **barrier to entry for *other* SAM+Trinity
contributors** (fetch firmware on-SAM, no SD-card dance) — is the SAM fetching
directly from the Raspberry Pi firmware repo on GitHub. That needs **TLS on the
Z80** (**i88**), the single hardest component in the project: feasible but large.
The tractable route is to **pin GitHub's certificate/public key** (we control both
ends, so no CA store is needed) and use the Z80-friendliest TLS 1.3 suite —
**X25519 + ChaCha20-Poly1305 + SHA-256** (ARX-only; no AES S-boxes, no GHASH). The
handshake is slow (seconds) but firmware-fetch is rare, so that is fine. i88 is a
**deliberate stretch**: the daily loop never needs it, the interim plain-HTTP/TFTP
path covers provisioning, and the next agent should also check whether a plain-HTTP
RPi-firmware mirror exists (which would make on-SAM HTTPS optional).

**Finding (2026-06-15): a plain-HTTP source exists — on-SAM TLS (i88) is OPTIONAL,
not necessary.** Verified end-to-end (a real firmware `.deb` downloaded + unpacked
over port 80, no TLS): the **Raspberry Pi apt archive** `http://archive.raspberrypi.org/debian/`
serves the *complete* Pi 3 + Pi 4/400 netboot firmware set over **plain HTTP** — the
GPU firmware/bootloader blobs (`bootcode.bin`, `start*.elf`, `fixup*.dat`) ship in
`raspberrypi-bootloader_*.deb`, the kernels + DTBs in the sibling
`raspberrypi-kernel_*.deb`, both under
`…/pool/main/r/raspberrypi-firmware/`, and apt's own default source line for this
archive is `http://` (no redirect to HTTPS). So the i70 HTTP/1.x client already
built (no TLS) **suffices** to self-provision firmware; only GitHub itself is
HTTPS-locked (301→https, empty body on :80), which is what would have forced i88.
The trade is **format work, not crypto**: the blobs sit inside a `.deb` (an `ar`
archive wrapping an **xz**-compressed `tar`), so the SAM must parse ar + xz-inflate +
untar `data.tar.xz` to extract `./boot/*`. That is far smaller than a TLS stack, and
sidesteppable by pointing the client at any HTTP server hosting the **already-extracted**
boot files (a one-time host/community step, host-tool-free for the end user). apt's
integrity is GPG-signed (verifiable **without** transport security), so dropping TLS
loses nothing apt itself relies on. **Consequence:** i88 drops from "needed for the
purest form" to a genuine stretch; the open choice is the extraction strategy —
on-SAM `.deb`/xz handling vs a pre-extracted HTTP mirror vs host-side staging
(question **q15**). Sources: live archive fetch + GitHub :80 redirect check (2026-06-15);
the apt archive pool dir; the netboot file-set references in the i70/i84 research.

## 8. Storage model

A **flat file store on Trinity SD**, reached through the unchanged B-DOS hook
surface (`HSAVE`/`HRECORD`/sector hooks, `samdos-file-io.md`; i62 dual-run proof,
i71 fork analysis). Files are addressed by name (the TFTP filename ↔ a stored
object); the kernel and each firmware blob are objects in the store. One 800 KB
B-DOS record ≈ one floppy image; large objects span record(s). No FAT, no custom
SPI driver — the B-DOS route subsumes both (i58/i71). The store's *contents* are a
provisioning choice that **varies** by project, firmware revision, and features
(e.g. HDMI audio → extra `.dtbo` overlays) — the server is indifferent, serving
whatever is present and `ERROR(1)`-ing the rest, so the file set is never hard-coded
anywhere.

**Persisting a multi-MB firmware (q16 RESOLVED — Pete, 2026-06-15).** A real Pi
firmware blob (`start4.elf` ≈ 3 MB) exceeds both the SAM's RAM and one ~800 KB
B-DOS record, so it is persisted as a **sequence of bounded files spanning multiple
records/disks**, each written with a plain **`HSAVE`** (the only *verified* save
hook — the byte-stream append hooks `HOFLE`/`HSBYT` are broken for external `RST 8`
callers per `sam-stub-audit.md`, so this **avoids appending to a growing file**
entirely), reassembled in order at serve time (the TFTP server streams DATA blocks
across the spanning files). This pairs with the i99 download-streaming: the HTTP
fetch can't hold a multi-MB body in RAM, so it streams windows to storage as they
arrive, and each window/record-sized chunk is one bounded `HSAVE`. **A single B-DOS
file is *not* limited to 64 KB** — the UIFA encodes size as page-count(+34) × 16384
+ length-mod-16K(+35..36), a **32-bit** value (`bdos_difa_to_size`), so a file may be
up to ~4 MB (255 × 16 KB pages); the binding limits are RAM (the HSAVE source) and
the ~800 KB per-record capacity, not a 64 KB file ceiling (the 64 KB is the 16-bit
*addressable window*, distinct from i24's assembler `OUT_LEN`). The open detail is a
spanning-file naming/ordering convention + the serve-time reassembly — host-verifiable
in the i80 emulation; the real `RST 8` `HSAVE`-per-record stays the hardware gate.

## 9. Host-side iteration (i80) + the empirical oracle

SimCoupé does not emulate the Trinity network hardware, so today the stack only
runs on real Trinity hardware. **i80** (emulate the ENC28J60 SPI path in SimCoupé,
bridged to a host/virtual network) is the enabler for a fast host inner loop; its
scope now spans DHCP + TFTP + HTTP, so it must carry enough of a virtual network to
exercise a netbooting-Pi handshake. Until i80 exists, design/unit-test the protocol
state machines in isolation and treat real-Trinity runs as the scarce integration
gate.

**The implementation oracle is a real packet capture.** Put a hub between the SAM
and the Pi, run Wireshark, and mirror the captured DHCP+TFTP exchange byte-for-byte.
This is a proven technique here: Pete documented trinload's ethernet frames exactly
this way (trinload commit `9ff9099`, `test/README.md`). Pete's Mac `dnsmasq` is the
reference server to diff our responses against. Pete **provided real captures**
(`~/tftp-logs`: `rpi400-boot-spectrum4.pcapng` + `dnsmasq.log`), now distilled into
[`../notes/pi-netboot-capture-analysis.md`](../notes/pi-netboot-capture-analysis.md)
— so §6.3/§6.4 (the Pi's filenames, OACK options, and the exact DHCP option-43 blob)
are already settled from ground truth. A **Pi 3** capture (i89) will extend this to
the older family.

## 10. Phasing

1. **3a — inbound client (i82).** Fully designable now; bootstrap via trinload or
   the patched ROM. Gate: a real-Trinity fetch of an image to a record, booted.
2. **3b — outbound server (i83) + DHCP responder (i86).** Model A. The daily-loop
   headline; the per-Pi handshake is **already settled** by the captured oracle
   ([`../notes/pi-netboot-capture-analysis.md`](../notes/pi-netboot-capture-analysis.md)
   — OACK `tsize`/`blksize`, serve-by-name + ERROR(1)-tolerant, the option-43 blob).
3. **3c — firmware self-provisioning (i70).** After the core; bridged by the client
   meanwhile. **HTTPS-direct-from-GitHub (i88) is a later stretch.**
4. **Cross-cutting — i80** (SimCoupé Trinity-net emulation) for host iteration, and
   **i87** (dump + reverse Pete's patched SAM ROM) when his SAM is next online.

Each integration step is gated on real-Trinity confirmation — the one unknown no
host design removes.

## 11. Resolved decisions / residual research

- **q12 → resolved:** Model A only (PXE pull, bare Pi). Model B rejected (§6.5).
- **q11 → resolved:** no Colin dependency (materials in hand + Pete's patched ROM,
  §3); DHCP is in scope and wanted (§6.3); bootstrap via the patched ROM (dump =
  i87). The residual research — the Pi file list + DHCP conventions — is now
  **settled** from real captures ([`../notes/pi-netboot-capture-analysis.md`](../notes/pi-netboot-capture-analysis.md), §9).
- (For context: the inbound RRQ option set is settled, i82 §5.7; the storage layer
  is settled, §8; the target-RAM stance is q9.)

## 12. Out of scope (Phase 3)

General internet *routing*/DNS beyond the single HTTP firmware-fetch (§7); serving
non-Pi / non-aarch64 targets; encryption/auth (the cable/LAN is the boundary);
wireless. DHCP and a single HTTP-fetch are now **in** scope (they were out in the
old sketch).

## 13. Related

- [`../notes/pi-netboot-capture-analysis.md`](../notes/pi-netboot-capture-analysis.md)
  — **the DHCP+TFTP server oracle** (real Pi 400 capture distilled into the i83/i86
  implementation spec: option-43 blob, OACK options, probe/ERROR behaviour).
- [`../notes/tftp-protocol-research.md`](../notes/tftp-protocol-research.md) — i82
  protocol authority (RFC corpus + trinload reuse map + client delta).
- [`../notes/trinity-capabilities.md`](../notes/trinity-capabilities.md) — ENC28J60
  ports, throughput, SD/EEPROM, SPI mechanics.
- [`../notes/bdos-version-landscape.md`](../notes/bdos-version-landscape.md) /
  [`../notes/bdos-trinity-fork-analysis.md`](../notes/bdos-trinity-fork-analysis.md)
  — the B-DOS hook surface + Trinity SD sector layer.
- [`samdos-file-io.md`](samdos-file-io.md) — the DOS hooks the store reuses.
- `~/git/trinload` (incl. `test/README.md`, commit `9ff9099`, captured frames),
  `~/sam-corpus/disks/trinity.mgt` (+ uIP), `~/sam-archive/bdos/analysis/`.
- Item registry: i82 (client), i83 (server), i86 (DHCP responder), i70 (firmware
  self-provisioning), i88 (HTTPS-from-GitHub stretch), i80 (SimCoupé Trinity-net
  emulation), i87 (patched-ROM dump), i89 (Pi 3 capture), i58/i61/i62/i71 (Trinity +
  B-DOS research).
- [`phase3-tftp-design.md`](phase3-tftp-design.md) — the prior sketch this
  supersedes.
