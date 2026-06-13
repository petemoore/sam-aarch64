# Phase 3 — end-to-end delivery design (the self-hosting boot/delivery loop)

**Item:** i84 · **Type:** design spec · **Status:** SPEC GATE — awaiting Pete's
approval; **no Phase-3 implementation begins until approved** (repo CLAUDE.md
"Development discipline" rule 1, the i7 precedent).

**Relationship to the prior sketch:** this extends and, on approval, supersedes
[`phase3-tftp-design.md`](phase3-tftp-design.md) (the 2026-05-27 ship-only
direction sketch). That sketch framed Phase 3 as a one-way *push* of `release.img`
from the SAM to a Pi already running `tftpd`; this document broadens it to the
full bidirectional self-hosting loop (fetch code *onto* the SAM; serve images
*to* a bare Pi for PXE boot) and folds the sketch's push model in as one outbound
option (§6.4). On approval, the sketch is deleted and its content lives here.

**Reads first:** [`../notes/tftp-protocol-research.md`](../notes/tftp-protocol-research.md)
(i82 — the TFTP RFC corpus + trinload analysis, the protocol authority for this
design) and [`../notes/trinity-capabilities.md`](../notes/trinity-capabilities.md)
(the ENC28J60 / EEPROM / SD hardware facts). This document does not restate
their protocol or hardware detail — it composes them into an architecture.

---

## 1. Goal

Close the loop so the SAM Coupé is a *self-hosting* aarch64 development machine:
code reaches the SAM and assembled images reach the Pi 400 **without physically
shuffling floppies**. Two opposite data flows:

- **Inbound (fetch):** pull disk and firmware images onto the SAM over the
  network and persist them to Trinity storage — the near-term bootstrap enabler
  (item **i82**). This is what removes the sneakernet from the daily loop.
- **Outbound (serve):** ship the freshly-assembled spectrum4 kernel (plus the Pi
  firmware blobs the Pi needs to reach it) to the Pi 400 for a network boot — the
  Phase-3 headline (item **i83**).

Both flows ride the same Quazar Trinity ENC28J60 Ethernet hardware and reuse the
same trinload-derived UDP/ARP/IP stack; they differ only in TFTP *role* (client
for fetch, server for serve).

## 2. The two loops at a glance

```
 INBOUND (i82 — TFTP client on the SAM)
   ┌────────────┐  RRQ/OACK/DATA   ┌──────────────┐  HRECORD/HSBYT  ┌────────────┐
   │ TFTP server│ ───────────────► │   SAM Coupé  │ ──────────────► │ Trinity SD │
   │ (Pete's Mac│ ◄─────────────── │ TFTP *client*│                 │ (B-DOS recs)│
   └────────────┘      ACK         └──────────────┘                 └────────────┘
                                          │ boot the fetched record
                                          ▼

 OUTBOUND (i83 — TFTP server on the SAM)
   ┌────────────┐  RRQ (netboot)   ┌──────────────┐
   │  Pi 400    │ ───────────────► │   SAM Coupé  │   serves: bootcode/start*.elf,
   │ boot ROM   │ ◄─────────────── │ TFTP *server*│   fixup*.dat, config.txt,
   │ (TFTP clnt)│   OACK/DATA       └──────────────┘   cmdline.txt, kernel image
   └────────────┘                         ▲
         │ executes spectrum4 kernel       │ images sourced from Trinity SD
         ▼                                  │ (fetched inbound, or self-provisioned)

 SELF-PROVISIONING (i70 — fill Trinity SD with Pi firmware once)
   online source ──HTTP/FTP on SAM (uIP)──► Trinity SD     (option A)
   online source ──host tool writes records─► Trinity SD    (option B)
```

## 3. The bootstrap chain (resolving the chicken-and-egg)

The inbound loop's value is "no physical disk needed", but *something* must put
the first TFTP client on the SAM and give it Trinity-storage access at power-on.
Two independent mechanisms, usable separately or together:

### 3.1 Colin Piggot's Trinity Boot ROM (the durable enabler)

The Trinity Boot ROM is a ROM-chip option that replaces the standard SAM ROM page
and auto-loads B-DOS from the Trinity EEPROM (chunk 1 holds a 1 KB B-DOS
bootblock) at power-on — giving Trinity SD access at boot with **no floppy in the
drive** ([`trinity-capabilities.md`](../notes/trinity-capabilities.md) §1). With
this ROM, a SAM that has our TFTP-client disk written to a Trinity SD record can
boot straight into it. This is the clean end state: power on → B-DOS from EEPROM →
autoboot the fetch client from an SD record.

**Dependency:** obtaining/configuring the Boot ROM is an *ask-Colin* item (Pete
makes the contact, never the agent — memory `project_sam_community_contacts`).
Its exact autoboot affordances need confirmation from Colin or the product
documentation (→ q11).

### 3.2 trinload as the interim bootstrap (no custom ROM needed)

trinload's whole purpose is to load code over the network into RAM and run it
([`tftp-protocol-research.md`](../notes/tftp-protocol-research.md) §6.1). So even
without the Boot ROM, a stock SAM with a Trinity card can boot a tiny trinload
disk, pull our TFTP client over the wire into RAM, and execute it — the client
then fetches the real images and writes them to SD. This makes the inbound loop
demonstrable on a plain machine before the Boot ROM is in hand, and is the natural
first integration target.

### 3.3 Layering

Both paths sit on the same foundation: trinload's ENC28J60 driver (`encdrv.asm`),
ARP responder, IPv4/UDP framing, and EEPROM MAC/IP config. The TFTP client/server
are new state machines layered on top — see i82 §6.8 for the reusable-routine
catalogue and §6.9 for the precise client delta.

## 4. Network topology and addressing

Direct cable, link-local, no switch/router/DNS/DHCP-on-the-LAN in the common case
(the old sketch's premise, retained). The SAM has one ENC28J60; it plays TFTP
*client* toward the Mac and TFTP *server* toward the Pi — never both
simultaneously in one session, so one MAC/IP suffices.

- **Addressing:** fixed IPs compiled in / read from the EEPROM "Trinity Network "
  chunk (MAC + IP — i82 §6.7; exact chunk layout pending a hardware read, i82
  §7.5 Q4). SAM = e.g. `192.168.1.1`, peer = `192.168.1.2`.
- **PHY:** ENC28J60 is 10BASE-T half-duplex; Auto-MDIX is unconfirmed for the
  Trinity board (`trinity-capabilities.md` §6), so assume a crossover cable or an
  Auto-MDIX peer until proven otherwise.
- **Why no DHCP for the inbound leg:** the client knows the server's address; it
  just sends an RRQ. DHCP only re-enters the picture for the *outbound* PXE leg,
  and even there it can be avoided (§6.3).

## 5. Inbound — the SAM TFTP client (i82)

The protocol mechanics, packet layouts, option semantics, Sorcerer's-Apprentice
fix, TID handling, and the trinload reuse map are all specified in the i82
research note; this section states only the *delivery* decisions.

- **Mode:** octet (binary). **RRQ option set:** `blksize=1428, tsize=0,
  timeout=2, windowsize=4`, with graceful fallback to RFC-1350 lock-step if the
  server sends no OACK (i82 §5.7, §7.1).
- **`tsize=0` is load-bearing:** the OACK returns the exact byte count, so the
  Z80 pre-computes the B-DOS record count (`⌈tsize / 819200⌉`, one 800 KB record
  per floppy-equivalent image) and allocates before the first DATA block lands
  (i82 §4.2, §7.3).
- **Where blocks land:** received DATA → a staging page in the top 32 KB (HMPR) →
  B-DOS records via the hook layer (`HRECORD` to select the record, then the
  sector-write hooks), the path verified portable SAMDOS↔B-DOS by i62 and
  static-verified for the Trinity SD fork by i71. No raw SPI driver is needed —
  the unchanged B-DOS hook surface reaches the SD write path
  ([`bdos-version-landscape.md`](../notes/bdos-version-landscape.md);
  `trinity-capabilities.md` §9 Q6).
- **Fetch targets:** (a) **disk images** — a full 800 KB record fetched and
  written, then booted (this is how new tools/dialects arrive on the SAM); (b)
  **Pi firmware blobs** — staged for the outbound leg / self-provisioning (§7).
- **Code budget:** ~150–200 lines of new Z80 for the client state machine on top
  of trinload (i82 §6.9). Lives in a high RAM page alongside the trinload stack.

This is the **near-term, fully-designable** half: every dependency except the
real-Trinity integration test is in hand.

## 6. Outbound — the SAM TFTP server (i83), the PXE shipper

The headline goal: boot a *bare* Pi 400 over the cable from images the SAM holds.
This is the harder protocol role (the SAM answers requests it does not initiate)
and carries the most external unknowns (the Pi's exact boot behaviour).

### 6.1 How a Pi 400 network-boots

The Pi 4/400 boot ROM can TFTP-netboot: it fetches the GPU firmware stage
(`bootcode`/`start4.elf`/`fixup4.dat`), `config.txt`/`cmdline.txt`, and finally
the kernel image, acting as the **TFTP client**. In the standard flow it first
uses DHCP to learn the TFTP server address and boot path; the Pi 4 bootloader
EEPROM also supports a **static netboot** configuration (`TFTP_IP` / `TFTP_PREFIX`
EEPROM settings) that hard-codes the server, removing the DHCP requirement. (These
are claims to verify against current Raspberry Pi bootloader docs and on real
hardware — → q11.)

### 6.2 What the SAM must therefore be

For the Pi to pull from the SAM, the SAM runs a **TFTP server**: it listens on UDP
69, parses incoming RRQs, optionally answers options with an OACK, and streams
DATA while honouring the Pi's ACK cadence. The server delta vs the i82 client:
build/parse the RRQ-handler and the DATA-send loop (instead of RRQ-builder +
DATA-receive loop); the ARP/IP/UDP framing and `ack_len`-style send primitive are
the same trinload reuse. The server must serve several files per boot and several
MB total (the GPU firmware dominates) — these come from Trinity SD records
(fetched inbound or self-provisioned), streamed out block by block.

### 6.3 Avoiding DHCP

If the Pi bootloader EEPROM static-netboot path (§6.1) works, the SAM needs
**only** a TFTP server — no DHCP/proxyDHCP. This is the strongly-preferred target:
DHCP on the Z80 is real extra scope. If static netboot proves unavailable, a
minimal proxyDHCP responder (answer DISCOVER with our TFTP address + filename, no
address leasing) is the fallback. The design assumes static-netboot until
hardware says otherwise (→ q11).

### 6.4 Two delivery models (the design fork → q12)

- **Model A — PXE pull (recommended, the true bare-Pi goal):** SAM = TFTP server;
  Pi = TFTP client via its boot ROM; SAM serves firmware + kernel from SD. Boots a
  bare Pi with no OS pre-installed. More SAM-side work (server role; firmware
  self-provisioning) but it is the actual "ship the kernel to bare metal" vision.
- **Model B — WRQ push (the old sketch's model, a simpler interim):** SAM = TFTP
  *client* doing a WRQ to a `tftpd` on a Pi **already running Linux**, then a
  Pi-side watcher reboots into the new image. Far less SAM-side work (reuses the
  i82 client; no server, no firmware serving) but it does not boot a bare Pi and
  needs a cooperating OS + reboot trigger on the Pi.

Recommendation: **Model A is the goal; Model B is a legitimate early milestone**
that proves the cable + assembled-kernel handoff while the server role is built.
Pete's call on whether to bother with B as a stepping stone (→ q12).

## 7. Firmware self-provisioning (i70)

Model A needs the Pi's firmware blobs (several MB) resident on Trinity SD. The
friction: B-DOS media is raw 800 KB records, **not FAT**, so a newcomer cannot
just drag firmware files onto the SD on a PC (`trinity-capabilities.md` §4, §7).
Two ways to populate it:

- **Option A — the SAM fetches firmware itself, once:** the SAM speaks HTTP or FTP
  over a real TCP stack, pulls the firmware from an online location, and writes it
  to SD via B-DOS hooks. Natural base: the **uIP** TCP/IP stack found on the
  corpus Trinity utility disk (ARP/IPv4/TCP — i61), since trinload's stack is
  UDP-only. Keeps the workflow self-hosted (no PC needed) but is the larger build.
- **Option B — a host-side tool writes B-DOS records to SD from a PC:** smaller,
  but reintroduces a PC dependency for the firmware step.

These are not mutually exclusive (B now, A later). Deferred-but-tracked under i70;
the choice is a future sub-decision, not a blocker for i82/i83 design.

## 8. Storage model

One home, already proven: **B-DOS records on Trinity SD**, reached through the
unchanged DOS hook surface.

- One record ≈ one 800 KB floppy image (80 trk × 10 sec × 512 B × 2 sides); a
  fetched disk image maps 1:1 to a record. Firmware/kernel blobs occupy
  record(s) sized to fit.
- Writes use the same `HSAVE`/`HRECORD`/sector hooks the assembler already uses
  for OUT (`samdos-file-io.md`), with `HRECORD` selecting the record — the i62
  dual-run proof (SAMDOS↔B-DOS) and the i71 static fork analysis cover this layer.
- No FAT, no custom SPI driver: the B-DOS hook route subsumes both (i58/i71).

## 9. Host-side iteration and the i80 dependency

SimCoupé does not emulate the Trinity network hardware, so today the net stack can
only run on real Trinity hardware — breaking the project's fast inner loop. **i80**
(emulate the ENC28J60 SPI path in SimCoupé) is the enabler that lets the TFTP
client/server be iterated host-side at the usual ~ms cadence, with real Trinity as
the integration gate. Until i80 exists:

- Design and unit-test the protocol *state machines* in isolation (Go model +
  the Z80 harness for the framing/parsing logic) where they don't need live
  hardware.
- Treat real-Trinity runs as the scarce integration gate — batch them.

i80 is therefore on the critical path for *efficient* Phase-3 work, even though
i82's logic can be written and reviewed without it. Pairs with the SD-emulation
interest already noted for SimCoupé.

## 10. Phasing

1. **3a — inbound client (i82).** Fully designable now. Build the RRQ client on
   trinload; land blocks to SD via B-DOS hooks. Bootstrap via trinload (§3.2).
   Gate: a real-Trinity fetch of a disk image, written to a record, booted.
2. **3b — outbound server (i83).** The PXE shipper (Model A). Depends on §6
   unknowns (q11/q12) being resolved with Pete + hardware.
3. **3c — self-provisioning (i70).** Fill SD with Pi firmware (Option B first,
   Option A later).
4. **Cross-cutting — i80** (SimCoupé Trinity-net emulation): start early; it makes
   3a/3b iterable host-side.

Each integration step is gated on real-Trinity confirmation — the one unknown no
amount of host design removes.

## 11. Questions for Pete (mirrored to the qN registry)

- **q11 — Pi netboot mechanics + Colin Boot ROM specifics.** Confirm: (a) the Pi
  400 boot ROM EEPROM *static netboot* path (`TFTP_IP`/`TFTP_PREFIX`) works so the
  SAM needs only a TFTP server and no DHCP (§6.3); (b) the exact firmware file set
  + option set the Pi's TFTP client requests; (c) the Trinity Boot ROM's autoboot
  affordances and how to obtain/configure it (ask-Colin). All hardware/contact
  gated.
- **q12 — outbound delivery model.** Model A (PXE pull, bare-Pi, the goal) as the
  target, with Model B (WRQ push to a Linux Pi) as an optional early milestone —
  or skip B and go straight to A? (§6.4).
- (Already settled, recorded for context:) the inbound RRQ option set (i82 §5.7);
  the target-machine RAM stance (q9 — both 256/512 KB, full release may exceed
  256 KB); the storage layer (i62/i71, §8).

## 12. Out of scope (Phase 3)

General internet access / routing / DNS; a full DHCP server (only static-netboot
or a minimal proxyDHCP if forced — §6.3); encryption/auth (the cable is the
boundary); wireless. These remain as the old sketch framed them.

## 13. Related

- [`../notes/tftp-protocol-research.md`](../notes/tftp-protocol-research.md) — i82
  protocol authority (RFC corpus + trinload reuse map + client delta).
- [`../notes/trinity-capabilities.md`](../notes/trinity-capabilities.md) — ENC28J60
  ports, throughput, SD/EEPROM, SPI mechanics.
- [`../notes/bdos-version-landscape.md`](../notes/bdos-version-landscape.md) /
  [`../notes/bdos-trinity-fork-analysis.md`](../notes/bdos-trinity-fork-analysis.md)
  — the B-DOS hook surface + Trinity SD sector layer.
- [`samdos-file-io.md`](samdos-file-io.md) — the DOS write hooks the inbound leg
  reuses.
- [`phase3-tftp-design.md`](phase3-tftp-design.md) — the prior sketch this
  extends/supersedes.
- Item registry: i82 (client), i83 (server), i80 (SimCoupé Trinity-net emulation),
  i70 (firmware self-provisioning), i58/i61/i62/i71 (Trinity + B-DOS research).
