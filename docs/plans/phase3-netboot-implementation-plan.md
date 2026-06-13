# Phase-3 netboot — implementation plan (i86 DHCP · i83 TFTP server · i82 TFTP client)

**Status:** execution-ready, **gated on Pete's go-ahead** to begin Phase-3
implementation. This is a *plan*, not landed code. The confirmed design
([`../specs/phase3-delivery-design.md`](../specs/phase3-delivery-design.md)) and
the capture oracle
([`../notes/pi-netboot-capture-analysis.md`](../notes/pi-netboot-capture-analysis.md))
make the three components mechanically specifiable; the trinload reuse-map
([`../notes/tftp-protocol-research.md`](../notes/tftp-protocol-research.md) §6)
makes them small. None of it is end-to-end verifiable on the host without **i80**
(SimCoupé Trinity-net emulation) or real Trinity hardware — so the only thing
testable on the host before i80 is the **unit-level packet builder/parser against
the captures** (§4 below).

**Scope note — this is a standalone program.** The netboot server/client is a
**separate Z80 program**, NOT part of the assembler binary in `src/`. It is built
from trinload's stack + new TFTP/DHCP state machines, booted from Trinity storage
(or net-loaded by trinload in the interim, design §3). It shares no memory map,
no paging trampoline, and no build target with the assembler. Give it its own
build directory (proposed `src/netboot/`, see §7) and its own host-side oracle
harness (proposed `tools/netboot-oracle/`, see §4).

**Reads first (the authorities this plan composes, does not restate):**

- Design: [`../specs/phase3-delivery-design.md`](../specs/phase3-delivery-design.md) — the confirmed architecture (Model A, serve-by-name, both topologies, §10 phasing).
- Oracle: [`../notes/pi-netboot-capture-analysis.md`](../notes/pi-netboot-capture-analysis.md) — the exact DHCP option-43 blob, OACK options, serve-by-name + ERROR(1)-tolerant probe behaviour, from a real Pi 400 capture.
- Protocol + reuse-map: [`../notes/tftp-protocol-research.md`](../notes/tftp-protocol-research.md) — the TFTP RFC corpus and the trinload routine inventory (§6.8).
- Hardware: [`../notes/trinity-capabilities.md`](../notes/trinity-capabilities.md) — ENC28J60 ports, throughput, SD/EEPROM, SPI.
- B-DOS store: [`../notes/bdos-version-landscape.md`](../notes/bdos-version-landscape.md) + [`../specs/samdos-file-io.md`](../specs/samdos-file-io.md) — the HRECORD/HSAVE hooks the flat store reuses.

---

## 0. The one architectural fact that shapes everything

trinload **has no fresh-packet origination path.** Every send in trinload is a
*reply-by-address-swap*: the incoming frame stays in the `packet` buffer and
`ack_len` (`trinload.asm:215`) chains `set_udp_data_len → return_eth → return_ip →
return_udp → checksum_ip → checksum_udp → drv_write`, swapping src/dst MACs, IPs,
and ports in place (research note §6.5, §6.8). There is no routine that builds an
Ethernet/IP/UDP frame from scratch.

This cleanly splits the three components by how much new framing they need:

| Component | Role | Send pattern | New framing code |
|---|---|---|---|
| **i86 DHCP responder** | server | reply to a received DISCOVER/REQUEST broadcast | **small** — mostly the `ack_len` swap chain; but the *destination* of a DHCP reply is a broadcast (or the offered IP), not a pure swap, and the L2/L3 broadcast + `siaddr`/`yiaddr`/options must be written into the buffer |
| **i83 TFTP server** | server | reply to a received RRQ, then DATA/ACK loop | **small–medium** — the OACK/DATA replies *are* reply-by-swap (the Pi's RRQ source becomes the reply destination); the new work is the TFTP opcode/option state machine + streaming from the store |
| **i82 TFTP client** | client | **originate** an RRQ to a server's port 69 | **medium** — genuinely new: ARP to learn the server MAC, then build a fresh frame (src = self, dst = server) with no incoming packet to swap from |

The implication for sequencing: the **servers reuse more of trinload and need
less new framing**, but the **client is the cleaner first build** because it is
the §10 phase 3a bootstrap *and* because its "originate a fresh frame" primitive
(`build_udp_frame`, §5.1) is then reusable by the DHCP responder's broadcast
reply. Build the fresh-frame primitive once, in the client, then lean on it.

---

## 1. Shared foundation — the trinload stack as a vendored library

All three components sit on the identical trinload infrastructure. The first
implementation step is to establish that shared base so the three state machines
are pure additions.

### 1.1 What is reused unmodified (research note §6.8)

These routines are taken from `~/git/trinload` **as-is** — they are the L1/L2/L3
stack and need no change:

| Routine | trinload file:line | Purpose |
|---|---|---|
| `drv_init` | `encdrv.asm:22` | init ENC28J60, set MAC from EEPROM, enable RX |
| `drv_read` | `encdrv.asm:99` | poll next frame into the `packet` buffer; BC = length |
| `drv_write` | `encdrv.asm:153` | transmit `packet` (HL), length BC; R5-errata 16-retry loop |
| `drv_exit` | `encdrv.asm:256` | reset ENC, disable RX |
| `rd_buf_mem` / `wr_buf_mem` | `encdrv.asm:343` / `:374` | bulk ENC buffer DMA (RBM/WBM) |
| `chk_trinity` | `encdrv.asm:457` | probe for the Trinity board at startup |
| ARP responder | `trinload.asm:78–105` | answer ARP-who-has for `sam_ip` |
| `return_eth` / `return_ip` / `return_udp` | `trinload.asm:275` / `:286` / `:298` | swap L2/L3/L4 src↔dst for a reply |
| `checksum_ip` / `chksum_blk` | `trinload.asm:306` / `:360` | RFC 1071 IP-header checksum + core word-sum |
| `checksum_udp` | `trinload.asm:353` | zero the UDP checksum (legal for IPv4, RFC 768) |
| `set_udp_data_len` / `ip_to_eth_len` | `trinload.asm:232` / `:250` | fill UDP/IP length fields; derive frame length |
| `ack_len` | `trinload.asm:215` | the compound "send a UDP reply by swap" primitive |
| `find_index` / `read_chunk` | `eeprom.asm:134` / `:285` | load the "Trinity Network " chunk (MAC+IP) from EEPROM |

`sam_mac equ chunk+0`, `sam_ip equ chunk+6` (`trinload.asm:414–415`); the `packet`
buffer is `defs 1518` (`trinload.asm:419`); ENC RX ring `&0000–&19FF` (6.5 KB),
TX `&1A00–&1FFF` (1.5 KB) (`encdrv.asm:5–8`).

### 1.2 Packet-buffer offsets (the parse/emit map, research note §6.9)

For a standard Ethernet/IPv4/UDP frame in the `packet` buffer:

| Field | Offset | Notes |
|---|---|---|
| dst MAC | `packet+0` | |
| src MAC | `packet+6` | |
| EtherType | `packet+12` | `&0800` IPv4, `&0806` ARP (big-endian) |
| IP flags | `packet+20` | bit 5 = MF (fragment) — drop fragmented |
| IP protocol | `packet+23` | `&01` ICMP, `&11` UDP |
| IP src | `packet+26` | 4 bytes |
| IP dst | `packet+30` | 4 bytes |
| UDP src port | `packet+34` | big-endian — this is the peer's TID for the reply |
| UDP dst port | `packet+36` | big-endian |
| UDP length | `packet+38` | |
| UDP checksum | `packet+40` | |
| UDP payload | `packet+42` | TFTP opcode / DHCP op start here |

These offsets are the contract for both the Z80 code and the host oracle (§4) —
the host parser/builder must agree byte-for-byte.

### 1.3 The interim bootstrap (design §3)

Before Pete's patched ROM (i87) exists, trinload net-loads the program: a plain
SAM + Trinity boots a tiny trinload disk, the host sends `@`/`X` packets to LDIR
the netboot binary into the top-32 KB page and run it (the `try_data`/`try_exec`
HMPR+LDIR pattern, `trinload.asm:166–211`). So the program is testable on real
hardware *without* the ROM patch — the ROM patch just removes the trinload step.

---

## 2. i82 — the TFTP client (§10 phase 3a, build first)

**Behaviour (the authority is the i82 research note §6.9; delivery design §5):**
octet-mode RRQ client that pulls an image from a standard TFTP server into a
B-DOS record. RRQ option set `blksize=1428, tsize=0, timeout=2, windowsize=4`
with graceful RFC-1350 lock-step fallback if no OACK (research note §5.7, §2.3).
`tsize=0` returns the byte count in the OACK so the Z80 pre-allocates
`⌈tsize/819200⌉` B-DOS records before the first DATA (research note §4.2, §7.3).

**Why first:** it is the §10 phase 3a bootstrap enabler, it is the smallest state
machine (~150–200 new Z80 lines, research note §6.9), and it forces us to build
the **fresh-frame primitive** (§5.1) that the DHCP responder then reuses.

**New code (replaces trinload's `try_data`/`try_exec` dispatch, `trinload.asm:155–211`):**

1. **ARP-for-server.** The client originates traffic, so it must learn the
   server's MAC. Send an ARP request for the server IP (build a fresh ARP frame —
   see §5.1, the same fresh-frame need), cache the reply's sender MAC. (On a
   direct cable the server answers; on a shared LAN the switch forwards it.)
2. **Build + send the RRQ** (research note §6.9 step 1). Fresh UDP frame: dst =
   server MAC/IP/port 69, src = self; payload = `opcode=1`, filename NUL, `octet`
   NUL, then the option string. Uses `build_udp_frame` (§5.1) + `set_udp_data_len`
   + `drv_write`.
3. **Await first response** (step 2). Parse opcode at `packet+42`:
   OACK(6) → record negotiated blksize/tsize/windowsize, ACK block 0 (reply-by-swap
   via `ack_len`), save the server TID from `packet+34`; DATA(3) → server ignored
   options, use defaults; ERROR(5) → abort with the message.
4. **Transfer loop** (step 3). Validate source port == saved TID (else ERROR 5 +
   discard); read block# at `packet+44`; copy payload at `packet+46` to the
   staging page; ACK via `ack_len`; a short DATA ends the transfer.
5. **Timeout/retransmit** (step 4). On timeout retransmit the **last ACK only**,
   never the RRQ (Sorcerer's-Apprentice fix, research note §1.7); retry N times
   then abort.
6. **B-DOS write integration** (step 5). Each DATA block → staging page (HMPR) →
   B-DOS records via the unchanged HSAVE/HRECORD hook surface (`samdos-file-io.md`;
   i62 dual-run proof). `tsize` from the OACK sizes the pre-allocation.

**Decisions already settled (do not re-open):** option set (research note §5.7);
TID_c can be a fixed port > 1024 (research note §1.5); blksize=1428 fits one
Ethernet frame, no reassembly (research note §3.3); the staging page lives above
`&8000` via HMPR (research note §7.4). windowsize=4 fits the 6.5 KB RX ring;
never request a window that overflows it (research note §5.5).

**Open hardware unknowns (carry from research note §7.5 — flag, do not block):**
the exact EEPROM "Trinity Network " chunk layout (§7.5 #4 — the `sam_ip` offset is
internally inconsistent in trinload's own sanity check; **confirm with a hardware
read** before the client relies on it); B-DOS HRECORD on real Trinity SD without
Atom-Lite emulation (§7.5 #5 — unverified, needs a real-Trinity run); server-side
windowsize support (§7.5 #1 — `tftpd-hpa` lacks RFC 7440; fall back to blksize
alone). These are integration-gate items, not design gaps.

---

## 3. i83 + i86 — the netboot server (§10 phase 3b)

The headline. Two cooperating servers that reply to a netbooting Pi. Both are
**reply-driven** (the Pi initiates), so they reuse the trinload swap chain more
than the client does. The oracle
([`../notes/pi-netboot-capture-analysis.md`](../notes/pi-netboot-capture-analysis.md))
is ground truth for every wire-level decision below.

### 3.1 i86 — the DHCP responder (oracle §1)

Answer the Pi boot ROM's broadcast DISCOVER/REQUEST with an OFFER/ACK. The Pi is
a **PXE client** doing a standard DORA cycle (oracle §1).

**Dispatch:** extend the trinload UDP dispatch (`trinload.asm:136–143`) to also
match **UDP dst port 67** (DHCP server port; client is 68). DHCP rides UDP exactly
like trinload's own protocol, so this is one more port branch.

**The OFFER/ACK the SAM must emit (oracle §1, the response template):**

| Field / option | Value |
|---|---|
| op / `yiaddr` | BOOTREPLY; `yiaddr` = an address from the pool (mirror dnsmasq `192.168.50.10–.20`) |
| `siaddr` (next-server) | the SAM's IP — **this is how the Pi learns the TFTP server** |
| opt 53 message-type | 2 (OFFER) / 5 (ACK) |
| opt 54 server-id | the SAM's IP |
| opt 51/58/59 | lease / T1 / T2 (any sane values — dnsmasq used 12h/6h/10h30m) |
| opt 1 netmask | `255.255.255.0` |
| opt 28 broadcast | the subnet broadcast |
| opt 3 router | the SAM's IP |
| opt 60 vendor-class | **`PXEClient`** (9 bytes) — echo it (oracle §1: present in every working capture) |
| opt 97 client-machine-id | **echo** the client's 17-byte UUID verbatim |
| **opt 43 vendor-encap** | **the fixed 32-byte PXE blob** (below) — mandatory |
| **no opt 66/67** | dnsmasq omits the bootfile; the Pi requests its own filenames |

**The exact option-43 blob (oracle §1 — a fixed constant the SAM sends verbatim):**

```
06 01 03 0a 04 00 50 58 45 09 14 00 00 11
52 61 73 70 62 65 72 72 79 20 50 69 20 42 6f 6f 74 ff
```

(= PXE sub-opt 6 DISCOVERY_CONTROL=3, sub-opt 10 MENU_PROMPT timeout-0 "PXE",
sub-opt 9 BOOT_MENU item 0 len 0x11 `Raspberry Pi Boot`, end. The literal string
`Raspberry Pi Boot` inside a PXE boot-menu structure is what the Pi 4 boot ROM
requires to accept the offer.)

**Reply framing:** the OFFER/ACK is an L2 broadcast (or unicast to `yiaddr`),
sent from UDP 67 to UDP 68. It is *not* a pure src/dst swap (the destination is
broadcast and `yiaddr`/`siaddr`/the options are freshly written), so it uses the
fresh-frame primitive (§5.1) with a broadcast dst MAC `ff:ff:ff:ff:ff:ff`, or
copies the request frame and overwrites the BOOTP/DHCP body + flips the swap. The
implementer picks the cheaper of the two once the buffer offsets are mapped; both
are mechanically settled.

**Address pool:** a tiny fixed pool (covers the shared-LAN multi-Pi case, design
§4). Track leased addresses in a small table keyed by client MAC so a REQUEST
gets the same address it was OFFERed. For the direct-cable single-Pi case a pool
of one suffices; build the small table anyway (the LAN case is in scope).

**i86 in one line (oracle §1):** assign from the pool, then emit the OFFER/ACK
template (constants + the fixed option-43 blob + the echoed client UUID +
`siaddr`=self + `PXEClient`). A handful of fixed UDP-67→68 broadcast replies on
trinload's stack — no general DHCP server.

### 3.2 i83 — the TFTP server (oracle §2)

After DHCP, the Pi TFTP-requests files by name from the next-server. The delta
from the i82 client: **parse** incoming RRQs and **answer** options with an OACK,
then run the **DATA-send / ACK-wait** loop (the opposite leg from the client).

**Dispatch:** match UDP dst port 69 (the well-known TFTP server port). The Pi's
RRQ source port is its TID — every reply (OACK, DATA, ERROR) goes back to it via
the trinload swap (`return_udp` already does exactly this, research note §6.5).

**The state machine (oracle §2 + §3):**

1. **Parse the RRQ** at `packet+42`: opcode 1, then filename NUL, `octet` NUL,
   then option pairs (`tsize` NUL `0` NUL, `blksize` NUL `<n>` NUL — 1024 and 1468
   both seen, oracle §2).
2. **Resolve the filename** against the flat Trinity store (§3.3). Two behaviours
   the oracle makes mandatory:
   - **ERROR(1) on every miss and keep serving** — the boot ROM probes a long
     list of optional files (`recovery.elf`, `pieeprom.sig`, `dt-blob.bin`,
     `armstub8-gic.bin`, …) and proceeds on not-found (oracle §2, §3 step 2). *The
     single most important robustness requirement: the server must not choke,
     hang, or abort the session on a miss.*
   - **404 the serial-subdir prefix** (`<serial>/start4.elf`) so the Pi falls back
     to root (oracle §2 "serial-subdir, then root"). Serve only from the flat root.
3. **On a hit, send the OACK** (oracle §2 "negotiated options"): `tsize`=the file's
   real size (known from the stored object), echo the accepted `blksize`. **No
   windowsize** — that is a client concern; the Pi does not request it.
4. **DATA/ACK loop:** stream the file block-by-block at the negotiated blksize from
   the store; wait for each ACK; a final short block ends the transfer. start4.elf
   is multi-MB so this streams from the store, never buffering the whole file.
5. **ERROR/timeout:** retransmit the last DATA on ACK timeout; abort cleanly on a
   client ERROR. Never let one file's failure kill the boot session.

**Reply framing:** OACK/DATA/ERROR are all replies to the Pi's RRQ — the swap
chain (`return_eth`/`return_ip`/`return_udp` + `ack_len`-style send) fits directly
(research note §7.2). The only new send-side work vs the client is filling the
TFTP payload (OACK option string / DATA block / ERROR message).

### 3.3 The flat store (design §8, oracle §3)

A **flat file store on Trinity SD** via the unchanged B-DOS hook surface
(`HSAVE`/`HRECORD`/sector hooks, `samdos-file-io.md`; i62 dual-run proof, i71 fork
analysis). Files are addressed **by name** (TFTP filename ↔ stored object); no FAT,
no custom SPI driver. Store contents = the union of files any target Pi requests
(a **provisioning** choice, §4 / i70) — distinct filenames across Pi families mean
one flat store serves every model (design §6.1). **The server never encodes a file
list** — it serves whatever the store holds and ERROR(1)s the rest (oracle §3:
"the mechanism is the invariant; the specific filenames are not").

The store needs a name→object lookup the Z80 can walk cheaply (a directory of
`{name, record#, size}` tuples). Designing that index is part of i83; the B-DOS
record layout is settled (`bdos-version-landscape.md`).

### 3.4 Pi 3 (i89, future, non-blocking)

The captured oracle is a Pi 400 (Pi 4 family). The Pi-3 file set
(`bootcode.bin`/`start.elf`/`fixup.dat`) is already known from Pete's `tftproot`,
and the serve-by-name mechanism is model-agnostic, so **no Pi-3-specific code is
planned.** When Pete captures a Pi 3 netboot (i89) it confirms the older family's
DHCP option-43 behaviour (it may not present the full PXE dance) — a validation
of the model-agnostic claim, not new server work. Build to the oracle; widen if
the Pi-3 capture surprises us.

## 4. i70 — firmware self-provisioning (§10 phase 3c, after the core)

Model A needs the Pi firmware blobs in the store. To stay host-tool-free the SAM
fetches them itself (design §7): interim = a plain **HTTP or TFTP** fetch from any
reachable server (the i82 client already does TFTP, so it bridges provisioning
immediately — no new code needed for the interim path). The canonical source is
GitHub (HTTPS-only) → **i88**, a deliberate stretch (TLS 1.3 on Z80 with cert
pinning + X25519/ChaCha20-Poly1305/SHA-256). i70/i88 land **after** the
client+server+DHCP core and are off the daily-loop critical path; this plan
sequences them last and does not detail i88 (its own design when reached). The
next agent on i70 should also check for a plain-HTTP RPi-firmware mirror, which
would make on-SAM HTTPS optional (design §7).

---

## 5. The new framing primitive (built once, in the client)

### 5.1 `build_udp_frame` — originate a fresh UDP/IPv4/Ethernet frame

trinload cannot do this (§0). The client needs it for the RRQ + ARP; the DHCP
responder reuses it for the broadcast OFFER/ACK. Specify it once:

- **Inputs:** dst MAC (6), dst IP (4), dst UDP port (2), src UDP port (2), a
  pointer to the UDP payload + its length.
- **Behaviour:** write the Ethernet header (dst MAC, `sam_mac` as src,
  EtherType `&0800`); the IPv4 header (version/IHL, total length, TTL, proto `&11`,
  `sam_ip` as src, the dst IP, then `checksum_ip`); the UDP header (src/dst ports,
  length, checksum 0 via `checksum_udp`); then the payload. Returns the frame
  length in BC for `drv_write`.
- **Reuse:** the IP/UDP length + checksum work is exactly `set_udp_data_len` +
  `checksum_ip` + `checksum_udp` — only the *address* fields differ from the swap
  path (they are set, not swapped). So `build_udp_frame` is `set_udp_data_len` and
  the two checksums with the address-fill inlined ahead of them. Small.
- **ARP variant:** the client's ARP-for-server needs a fresh ARP frame (EtherType
  `&0806`, opcode 1, sender = self, target IP = server, target MAC = zero,
  broadcast dst MAC). A trimmed sibling of the above; or hand-assemble the 42-byte
  ARP request from a template + `sam_mac`/`sam_ip`.

This is the only genuinely-new L2/L3 code in the whole plan; everything else is
trinload reuse + protocol state machines.

---

## 6. Test vectors — the captures are golden, the host oracle is the only pre-i80 check

**The `~/tftp-logs` captures are the golden vectors** (oracle note source list):
`rpi400-boot-spectrum4.pcapng` (a complete successful Pi 400 netboot) +
`dnsmasq.log`. The verification idea (design §9, oracle §"reference server"):
**replay the Pi's captured packets at our packet builder/parser and assert our
responses match what dnsmasq produced.** dnsmasq is the reference server to diff
against. This is the same hub+Wireshark technique Pete used for trinload
(`9ff9099`, `test/README.md`).

### 6.1 What is testable on the host *before* i80 (the only thing)

A host-side **oracle harness** (proposed `tools/netboot-oracle/`, a Go module so
it sits with the project's other Go scaffolding) that:

1. Reads the captured `.pcapng`/`dnsmasq.log` (kept out of the repo per the
   publication policy — they carry Pete's LAN MACs/IPs; the harness reads them
   from `~/tftp-logs`, like the i62 rig reads off-repo disks).
2. **DHCP (i86):** feed each captured DISCOVER/REQUEST to a host port of the i86
   response builder; assert the emitted OFFER/ACK matches dnsmasq's reply on every
   field that matters — opt 53/54/51/1/28/3/60/97, **the exact 32-byte option-43
   blob**, `siaddr`, and `yiaddr` from the pool. (Pete's real MACs/IPs are masked
   in any committed fixture; the *structure* is asserted.)
3. **TFTP server (i83):** feed each captured RRQ to a host port of the i83 parser;
   assert: the filename + options are parsed correctly; a hit produces the right
   OACK (`tsize`=size, echoed `blksize`); **every miss produces ERROR(1) and the
   server stays alive**; a serial-subdir path 404s. Replay the captured DATA/ACK
   cadence and assert block numbering + the short-final-block termination.
4. **TFTP client (i82):** the inverse — assert the RRQ builder emits the exact
   `blksize=1428,tsize=0,timeout=2,windowsize=4` option string; that an OACK is
   parsed into the right negotiated values; that the Sorcerer's-Apprentice rule
   holds (a simulated timeout retransmits the last ACK, never the RRQ).

This harness validates the **packet builder/parser logic in isolation**, byte-for-
byte against ground truth. It is a host unit test, on the §1.2 offset contract. It
**does not** prove the Z80 runs, that the ENC28J60 transmits, that B-DOS writes the
SD, or that a real Pi boots — none of those are host-testable without i80.

### 6.2 What needs i80 or hardware (explicitly NOT pre-i80 testable)

- The Z80 state machines actually executing (needs SimCoupé + i80's virtual net,
  or real Trinity).
- ENC28J60 TX/RX on real silicon (the R5-errata retry loop, `encdrv.asm:185`).
- B-DOS HRECORD → real Trinity SD (research note §7.5 #5 — unverified end-to-end).
- The EEPROM chunk layout for `sam_ip` (research note §7.5 #4 — needs a hardware
  read; trinload's own sanity check is internally inconsistent).
- A real Pi completing a netboot from the SAM (the integration gate, design §10:
  "each integration step is gated on real-Trinity confirmation").

So: **build the host oracle harness first** (it is the regression net for the
packet logic), develop the Z80 against it, and treat i80 / real-Trinity as the
scarce integration gate. If i80 lands, the same captured vectors drive an
end-to-end SimCoupé test; until then they drive the unit harness.

---

## 7. Build + layout

- **New program dir** `src/netboot/` (proposed): vendor the needed trinload
  sources (or reference the `~/git/trinload` clone via the build), add
  `tftp_client.asm` (i82), `tftp_server.asm` (i83), `dhcp_responder.asm` (i86),
  `udp_frame.asm` (§5.1 `build_udp_frame` + the ARP-request builder), `store.asm`
  (the §3.3 name→record index over B-DOS). A top-level `netboot.asm` wires init
  (`drv_init` + EEPROM config) → role select → the read/dispatch loop. Carries its
  own ≤30-line README (the per-directory README rule).
- **Build target** in the Makefile to assemble the standalone binary with pyz80,
  plus a trinload-loadable form for the interim bootstrap (§1.3).
- **Host oracle** `tools/netboot-oracle/` (Go module, §6) with its own README.
- **Provenance:** the vendored trinload code carries its upstream attribution
  (simonowen/trinload) in the dir README, like `reference/bdos/`.

This program is entirely separate from the assembler build — it shares no symbols,
no paging trampoline, no test wiring with `src/assembler.asm`. The §3 pre-merge
review's test-wiring/loader checklist (CLAUDE.md §3 items 1–2) applies to its own
build, not the assembler's.

---

## 8. Sequencing (mirrors design §10 phasing)

1. **Foundation** — vendor the trinload stack into `src/netboot/`; stand up the
   `tools/netboot-oracle/` harness reading `~/tftp-logs`; lock the §1.2 offset
   contract. *Host-testable.*
2. **3a — i82 client** + the §5.1 `build_udp_frame`/ARP primitive. Host-oracle:
   RRQ option string + OACK parse + SAS retransmit rule. *Gate (hardware): a
   real-Trinity fetch of an image to a B-DOS record, booted.*
3. **3b — i86 DHCP responder.** Host-oracle: OFFER/ACK fields + the exact
   option-43 blob + UUID echo + pool assignment vs the captured dnsmasq replies.
4. **3b — i83 TFTP server** + the §3.3 store index. Host-oracle: RRQ parse, OACK,
   **ERROR(1)-on-miss-keep-serving**, serial-subdir 404, DATA/ACK cadence vs the
   capture. *Gate (hardware): a real Pi netboots `spectrum4.img` from the SAM.*
5. **3c — i70 provisioning** (interim via the i82 client; i88 HTTPS a later
   stretch).
6. **Cross-cutting — i80** (SimCoupé Trinity-net emulation, the host inner loop)
   and **i87** (dump + reverse Pete's patched ROM) land when available; neither
   blocks the design/unit work but both are needed before the integration gates
   pass without real hardware.

Each numbered hardware gate is the one unknown no host design removes (design §10).

---

## 9. What this plan does NOT decide (carry-forwards, all already tracked)

- **i80** (SimCoupé Trinity-net emulation) — the enabler for a fast host inner
  loop; upstream scope, Pete's contact. Until it lands, §6.1 is the ceiling.
- **i87** (patched-ROM dump) — removes the interim trinload bootstrap step; Pete
  dumps the ROM when his SAM is next online.
- **i88** (HTTPS-from-GitHub) — the firmware-provisioning stretch; its own design
  when reached.
- **i89** (Pi 3 capture) — confirms the older family; non-blocking (§3.4).
- The hardware unknowns in research note §7.5 (EEPROM chunk layout, B-DOS-on-SD,
  server windowsize) — integration-gate confirmations, not design gaps.

This plan is **execution-ready and gated on Pete's go-ahead** to start Phase-3
implementation. On go-ahead, the first step is §8.1 (foundation + host oracle),
and `phase3-tftp-design.md` is deleted (it is superseded by the confirmed design,
design doc top-matter).

---

## Sources

- [`../specs/phase3-delivery-design.md`](../specs/phase3-delivery-design.md) — the confirmed Phase-3 architecture.
- [`../notes/pi-netboot-capture-analysis.md`](../notes/pi-netboot-capture-analysis.md) — the DHCP+TFTP capture oracle (the wire-level authority).
- [`../notes/tftp-protocol-research.md`](../notes/tftp-protocol-research.md) — the TFTP RFC corpus + the trinload reuse-map (§6.8) + the RRQ-client delta (§6.9).
- [`../notes/trinity-capabilities.md`](../notes/trinity-capabilities.md) — ENC28J60 / EEPROM / SD hardware facts.
- [`../notes/bdos-version-landscape.md`](../notes/bdos-version-landscape.md), [`../specs/samdos-file-io.md`](../specs/samdos-file-io.md) — the B-DOS hook surface the flat store reuses.
- `~/git/trinload` (`trinload.asm`, `encdrv.asm`, `eeprom.asm`) — the stack this program extends.
- Item registry: i82 (client), i83 (server), i86 (DHCP), i70 (provisioning), i88 (HTTPS stretch), i80 (SimCoupé net emulation), i87 (patched-ROM dump), i89 (Pi 3 capture).
