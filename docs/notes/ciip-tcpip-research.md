# "CIIP" / Z80 TCP/IP stack — adoption research (i161)

**Question.** Should sam-aarch64 adopt or adapt an existing Z80 TCP/IP stack
("CIIP") for the SAM Coupé + Quazar Trinity (ENC28J60) netboot path, instead of
continuing to hand-roll the minimal handlers in `src/netboot/*.asm`?

**Recommendation: don't bother — keep hand-rolling.** "CIIP" is a mis-remembering
of **CPC/IP**, Mark Rison's Z80 TCP/IP stack for the Amstrad CPC — the stack
Simon Owen looked at for the SAM. It is real, it is Z80 asm, and it is feature-rich
(TCP/UDP/ICMP/DNS/TFTP/HTTP/telnet/finger/ping). But it is **the wrong link layer**
(SLIP/PPP over a serial UART — no Ethernet, no ARP, so nothing for our ENC28J60 to
drive) and it is **proprietarily licensed** ("you may not distribute it without my
permission"), which rules out vendoring it the way trinload/encdrv are vendored.
The genuinely relevant, permissively-licensed prior art is Adam Dunkels' **uIP** —
whose single-packet-buffer TCP architecture our hand-rolled `tcp_conn.asm` already
mirrors (ported from the project's own Go authority and hardware-proven). Adopting
either would be net-negative work. Detail below.

## 1. Identifying "CIIP" — it is CPC/IP (Mark Rison)

**No stack is published under the literal name "CIIP."** The i161 description's
hints — *a Z80 TCP/IP stack with TFTP/HTTP/DNS servers+clients that Simon Owen
reportedly looked at* — point unambiguously to **CPC/IP**:

> Simon Owen, DevBlog "SAM/IP" (2007-12-11): *"The SAM port of uIP seems to be on
> hold at the moment, so I've been looking at other IP stacks to use until it's
> ready. The most appealing is Mark Rison's CPC/IP, not least because it's written
> in Z80 and should work without extensive changes."*
> <https://simonowen.com/blog/2007/12/11/samip/>

So "CIIP" ≈ "CPC/IP" (a letter-transposition of the same acronym). The "Simon
Owen ported it" memory is half-right: Simon *considered* CPC/IP as a stop-gap
while his **uIP** SAM port was stalled — he did not (per the blog) ship a CPC/IP
SAM port. CPC/IP itself is by **Mark Rison**, not Simon Owen.

**Local search.** `~/sam-archive` contains no CPC/IP / CIIP source (only
incidental "ciip"-substring hits in trinity-docs and session blog snapshots — not
the stack). So there is no local copy to adapt; the canonical sources are online.

### CPC/IP — the facts

- **Author / canonical home.** Mark Rison; <http://www.nenie.org/cpcip/>. A
  GitHub mirror of the sources exists: **pulkomandy/cpcip**
  (<https://github.com/pulkomandy/cpcip>).
- **Version / status.** v0.20, 2001-06-03 — **abandonware since 2001** (no changes
  in ~25 years). <https://github.com/pulkomandy/cpcip/blob/master/index.html>
- **Protocols.** PPP, SLIP, IP, ICMP, UDP, TCP, DNS (client+server), **TFTP
  (server)**, **HTTP/0.9 (server)**, ping, finger, telnet (clients). A genuinely
  full suite — more than we have.
- **Language / toolchain.** **Z80 assembly**, assembled with **ZMAC** (not pyz80).
- **Size.** ~14 KB excluding serial/filing-system/IP buffers, including all the
  clients/servers.

### Why CPC/IP does **not** fit (and is not adoptable)

- **(a) Licence — BLOCKER (proprietary).** Verbatim from the source:
  > *"CPC/IP is Copyright (c) 1999-2001 Mark RISON. You may not distribute it
  > without my permission (this includes modified/extended versions). Try emailing
  > me for permission -- you should be pleasantly surprised!"*
  > <https://github.com/pulkomandy/cpcip/blob/master/index.html>

  This is **incompatible** with the project's vendoring attitude. The project
  vendors simonowen/trinload + Quazar's `encdrv.asm`/`eeprom.asm` *verbatim* under
  permissive ("do what you like") terms with a provenance header
  (`src/netboot/trinload.asm`, `src/netboot/README.md`). CPC/IP's "no
  distribution without permission" forbids exactly that — committing a copy (even
  modified) into a public repo would need Mark Rison's written consent. (He hints
  he'd grant it, but it is **not** a standing licence; this is Pete's call to
  pursue, not the agent's — cf. the SAM-community-contacts norm.)

- **(b) Link layer — WRONG (no Ethernet, no ARP).** CPC/IP frames IP over **SLIP
  or PPP on a serial UART** (it requires a Z80-DART-compatible serial chip at
  `&fadc/fadd` + a CTC/8253 clock at `&fbdc…`). It has **no Ethernet driver and no
  ARP** — because a point-to-point serial link needs neither. Our path is
  **Ethernet over the ENC28J60** via Trinity. The entire link/driver/ARP layer of
  CPC/IP is unusable for us, and that is the layer we'd most want to *not* rewrite.
  We already have the ENC28J60 + ARP solved (Quazar `encdrv.asm`, host-emulated by
  `tools/netboot-oracle/z80/enc28j60.go`; `build_arp_*.asm`).

- **(c) Portability cost.** Adapting CPC/IP would mean: get a bespoke distribution
  licence, rip out its serial/SLIP/PPP link layer, splice in our Ethernet+ARP, and
  re-verify its TCP/UDP against our Go oracle + on real Trinity — i.e. keep only
  the IP/TCP core and re-home it on a foreign hardware model. That is comparable
  to (or worse than) extending what we already have, with a licensing dependency on
  top.

## 2. The permissively-licensed alternative — uIP

The stack worth knowing about for *licence* reasons is **uIP** (Adam Dunkels), the
one Simon Owen was actually porting before he eyed CPC/IP.

- **Licence — GOOD (BSD-style).** Permissive, commercial-use OK — fits the
  project's vendoring pattern. <https://en.wikipedia.org/wiki/UIP_(software)>,
  <https://github.com/adamdunkels/uip>
- **Protocols.** IP, ICMP, UDP, RFC-compliant TCP, + ARP for Ethernet links.
  Written in **C**.
- **Architecture — same as ours.** uIP's defining constraint is that it **uses one
  packet buffer** and keeps **a single outstanding (unacked) TCP segment** — it
  retransmits by calling the app to regenerate data, and does **not** do
  sliding-window / multiple in-flight segments.
  <https://en.wikipedia.org/wiki/UIP_(software)>. That is **exactly** the model of
  our hand-rolled `tcp_conn.asm`: a single-segment, in-order client state machine
  (SYN → SYN/ACK → ACK → in-order data + ACK → FIN), where "a bare ACK or an
  out-of-order/duplicate segment is ignored." So uIP offers **no more capable TCP**
  than we have.
- **Portability — POOR for us.** uIP is **C**, not Z80 asm, and our discipline is
  byte-exact Z80 ported from a Go authority (`tools/netboot-oracle/`). Adopting uIP
  means either dragging a C toolchain (z88dk/SDCC) + a second unverified codegen
  path into the boot image (against CLAUDE.md §7 emulation-first/byte-exact), or
  hand-porting uIP's C to Z80 — i.e. the work we already did, from a less familiar
  source than our own Go.

### Other things seen (none a fit)

- **Manawyrm/RC2014-Ethernet-Firmware** — a Z80 **uIP 1.0** port, but **GPL-3.0**
  (copyleft → incompatible with permissive vendoring), **z88dk C**, and targets
  **RTL8019/NE2000**, not the ENC28J60.
  <https://github.com/Manawyrm/RC2014-Ethernet-Firmware>
- **Spectranet** (spectrumero/spectranet) — the "Spectrum TCP/IP" name, but its
  NIC is the **WIZnet W5100**, which does **TCP/IP in silicon**; the ROM is a
  socket library around hardware sockets, so there is **no software TCP state
  machine to port** to a raw ENC28J60.
  <https://github.com/spectrumero/spectranet>,
  <https://sinclair.wiki.zxnet.co.uk/wiki/Spectranet>
- **icplan.de "TCP/IP Stack mit Z80"** (Jens Dietrich) — the nearest *phonetic*
  "CIIP" namesake, but a SLIP-over-RS232 hobby stack with **no NIC, no ARP, no
  licence**, dead since 2002. Not it. <https://icplan.de/seite1/>
- **jayacotton/inettools-z80** (RC2014 inet *tools*, not a stack core),
  **ZSock** (z88dk serial-era), **KCNet** (offloads TCP to a WIZnet/AVR bridge) —
  all wrong link layer or wrong model.
  <https://github.com/jayacotton/inettools-z80>,
  <https://www.rst38.org.uk/zsock/>,
  <http://kc85.info/index.php/kcnet-uk/z80-tcp-ip.html>

## 3. Comparison to the current hand-rolled approach

The hand-rolled stack is **well past the break-even point** for adoption:

- ARP (`build_arp_request.asm`/`build_arp_reply.asm`), ICMP, **UDP**
  (`build_udp_frame.asm`), **TFTP** client+server, **DHCP** responder, and a
  **TCP** client state machine (`tcp_conn.asm` + `build_tcp_segment.asm`) already
  exist and are **host-verified** against the project's Go oracle
  (`tools/netboot-oracle/`) under the i80 ENC28J60 emulation; the smoke + TFTP-serve
  paths are **hardware-verified on real Trinity** (memory:
  `trinity_hardware_first_light`).
- Our TCP is **architecturally equivalent to uIP** (single outstanding segment,
  in-order only, app-driven retransmit) — so no candidate offers a *more capable*
  TCP we'd gain by switching.
- Every candidate is the **wrong language** (C, needing a second codegen path the
  byte-exact discipline doesn't want), the **wrong NIC** (RTL8019), the **wrong
  link layer** (SLIP/PPP serial — CPC/IP, icplan, ZSock), the **wrong hardware
  model** (W5100 silicon TCP — Spectranet), or the **wrong licence** (proprietary
  CPC/IP; GPL-3.0 RC2014). The one permissive, relevant artefact — uIP — embodies
  the design we already have and would have to be re-ported to Z80 anyway.

If a future need arises for **real sliding-window TCP** (multiple in-flight
segments, e.g. for throughput on large firmware fetches), **no off-the-shelf Z80
stack supplies it** — uIP and its ports are single-segment by design, and CPC/IP
is serial-link single-stream — so that would be fresh design work in *our own*
`tcp_conn.asm` against an extended Go authority, regardless of adoption.

### Bottom line

**Don't adopt. Keep hand-rolling.** CPC/IP (the real "CIIP") is proprietary and
serial-only — neither vendorable nor link-compatible. uIP is the only
licence-compatible relevant prior art, and our `tcp_conn.asm` already embodies its
single-buffer design — ported, host-verified, and hardware-proven — from the
project's own Go oracle. The only thing worth lifting from either is *conceptual*
(the app-regenerates-data retransmit model we already follow), not code. Should
windowing ever be needed, that is fresh design in our stack, not a port of
someone else's. (If Pete ever wants CPC/IP's TCP core as a reference, contacting
Mark Rison for a distribution licence is *his* call to make — community-contact
norm — but the link-layer mismatch means even a granted licence buys little.)

## Sources

- Simon Owen DevBlog "SAM/IP" (2007-12-11) — uIP on hold; CPC/IP "written in Z80", preferred stop-gap — <https://simonowen.com/blog/2007/12/11/samip/>
- CPC/IP (Mark Rison) canonical home — <http://www.nenie.org/cpcip/>
- CPC/IP source mirror (license, protocols, serial-hardware reqs, ~14K size, v0.20/2001) — <https://github.com/pulkomandy/cpcip/blob/master/index.html>, repo <https://github.com/pulkomandy/cpcip>
- uIP (Adam Dunkels) historical sources — <https://github.com/adamdunkels/uip>
- uIP — Wikipedia (BSD-style licence; one packet buffer / single outstanding segment; TCP/UDP/ICMP) — <https://en.wikipedia.org/wiki/UIP_(software)>
- uIP design doc 0.6 (Dunkels, 2002) — <https://www.dunkels.com/adam/download/uip-doc-0.6.pdf>
- RC2014-Ethernet-Firmware (Manawyrm) — uIP 1.0 port, GPL-3.0, z88dk, RTL8019/NE2000 — <https://github.com/Manawyrm/RC2014-Ethernet-Firmware>
- Spectranet (W5100 hardware TCP) — <https://github.com/spectrumero/spectranet>, <https://sinclair.wiki.zxnet.co.uk/wiki/Spectranet>
- icplan.de "TCP/IP Stack für eine Z80 CPU" (SLIP, no NIC, no licence; nearest "CIIP" namesake) — <https://icplan.de/seite1/>
- jayacotton/inettools-z80 — <https://github.com/jayacotton/inettools-z80>; ZSock — <https://www.rst38.org.uk/zsock/>; KCNet Z80 TCP/IP — <http://kc85.info/index.php/kcnet-uk/z80-tcp-ip.html>
- Project vendoring precedent (trinload/encdrv/eeprom, permissive + provenance header) — `src/netboot/trinload.asm`, `src/netboot/README.md`
- Current hand-rolled TCP client state machine — `src/netboot/tcp_conn.asm`, `src/netboot/build_tcp_segment.asm`
