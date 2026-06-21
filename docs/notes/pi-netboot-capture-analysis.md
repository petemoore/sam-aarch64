# Pi netboot capture analysis — the DHCP+TFTP oracle for the SAM server (i83/i86)

**Purpose:** distil real Raspberry Pi netboot packet captures into the exact
DHCP + TFTP behaviour the SAM-side netboot server (i83 TFTP + i86 DHCP) must
reproduce — ground truth, not guesswork. Composes into
[`../specs/phase3-delivery-design.md`](../specs/phase3-delivery-design.md) §6.

**Source captures:** `~/tftp-logs/` (Pete's Mac, Wireshark + `dnsmasq` logs;
kept out of the repo — they carry his LAN's MACs/IPs; only protocol-relevant
facts are reproduced here). The decisive files: `rpi400-boot-spectrum4.pcapng`
(a complete successful Pi 400 netboot of `spectrum4.img`), `dump.pcap`,
`dnsmasq.log` (1,549 DHCP cycles; the human-readable transaction log). Captured
with the hub + Wireshark technique Pete used for trinload (`9ff9099`).

**Reference server being mirrored:** `dnsmasq` providing **DHCP + TFTP** from one
process, bound to a USB NIC, server `192.168.50.1`, pool `192.168.50.10–.20`,
root `/private/tftpboot`. The SAM reproduces this in Z80.

**Client captured:** a Pi 400 (MAC OUI `dc:a6:32` = Pi 4/400/CM4/Pi 5 family).
No Pi 3 appears in these logs (→ a Pi-3 capture is tracked as **i89**); the Pi-3
file set is known from Pete's `tftproot` + the Raspberry Pi docs.

---

## 1. DHCP exchange — standard PXE-style DORA

The Pi boot ROM behaves as a **PXE client**. One full DISCOVER → OFFER →
REQUEST → ACK cycle, broadcast.

### What the Pi sends (DISCOVER/REQUEST)

| Option | Value (captured) |
|---|---|
| 53 message-type | 1 (DISCOVER) / 3 (REQUEST) |
| 60 vendor-class | **`PXEClient:Arch:00000:UNDI:002001`** |
| 93 client-arch | `0x0000` (the Pi reports the x86-BIOS arch code) |
| 55 param-request-list | **`[1, 3, 43, 60, 66, 67, 128, 129, 130, 131, 132, 133, 134, 135]`** |

### What the server must send (OFFER/ACK) — the i86 response template

| Option | Value the SAM must put in the reply |
|---|---|
| 53 message-type | 2 (OFFER) / 5 (ACK) |
| 54 server-identifier | the SAM's IP |
| 51 / 58 / 59 | lease-time / T1 / T2 (dnsmasq used 12h / 6h / 10h30m — any sane values) |
| 1 netmask | `255.255.255.0` |
| 28 broadcast | the subnet broadcast |
| 3 router | the SAM's IP (it is the only host) |
| **60 vendor-class** | **`PXEClient`** (9 bytes) — **echo it.** Every working capture carries it (dnsmasq emits it by default) and the PXE convention tags a netboot offer `PXEClient`; whether the Pi boot ROM *strictly* requires it (vs. the option-43 `Raspberry Pi Boot` string below, which is the confirmed requirement) is unverified — so echoing it is the safe, observed-correct behaviour, and it also stops the Pi grabbing a non-PXE DHCP server on a shared LAN |
| 97 client-machine-id | **echo** the client's 17-byte UUID back verbatim |
| **43 vendor-encap** | **the 32-byte PXE blob below — mandatory** |
| `siaddr` / next-server | the SAM's IP (this is how the Pi learns the TFTP server) |

**No option 66/67 is sent.** dnsmasq supplies the TFTP server via the BOOTP
`siaddr` (next-server) field, and the Pi does **not** need a bootfile name — its
boot ROM requests its own known filenames directly (§2). The SAM responder can
likewise omit 66/67 and just set `siaddr`.

### The exact option-43 blob (the magic the Pi 4 boot ROM requires)

```
06 01 03                                  ; PXE sub-opt 6 (DISCOVERY_CONTROL) = 3 (unicast to server)
0a 04 00 50 58 45                         ; PXE sub-opt 10 (MENU_PROMPT)  = timeout 0, "PXE"
09 14 00 00 11 "Raspberry Pi Boot"        ; PXE sub-opt 9 (BOOT_MENU): item 0x0000, len 0x11, "Raspberry Pi Boot"
ff                                        ; end
```

Full bytes (32): `06 01 03 0a 04 00 50 58 45 09 14 00 00 11 52 61 73 70 62 65
72 72 79 20 50 69 20 42 6f 6f 74 ff`. The literal string **`Raspberry Pi Boot`**
inside a PXE boot-menu structure is what the Pi 4 boot ROM looks for to accept
the offer as a valid netboot server. **The SAM sends this as a fixed constant.**

**i86 in one line:** assign an IP from a small pool, then emit the OFFER/ACK
template above (mostly constants + the fixed option-43 blob + the echoed client
UUID). It is a handful of fixed UDP/67→68 broadcast replies on top of trinload's
stack — no general DHCP server needed.

---

## 2. TFTP exchange — what the server must do

After DHCP, the Pi TFTP-requests files by name from the next-server.

### Negotiated options (captured RRQs)

```
RRQ "e0ff06da/start4.elf"  octet  tsize=0  blksize=1024
RRQ "armstub8-gic.bin"     octet  tsize=0  blksize=1468
```

- **Mode `octet`** (binary) always.
- **The Pi negotiates `tsize=0` and `blksize`** (1024 for the big firmware,
  1468 elsewhere). **No `windowsize`.** So the SAM **server (i83) must implement
  the OACK path**: answer `tsize=<actual file size>` and echo the accepted
  `blksize` (the server knows the size from the stored object). This is the
  opposite leg from the i82 *client* (which would *request* options); the server
  *answers* them. windowsize is a client-side concern, not needed to serve a Pi.

### Filename behaviour — serial-subdir, then root

The boot ROM first requests files under a **serial-number subdirectory**
(`e0ff06da/…`), then **falls back to the root** on not-found:

```
RRQ e0ff06da/start4.elf  -> ERROR(1) file not found
RRQ e0ff06da/start.elf   -> ERROR(1) file not found
RRQ config.txt           -> served      (root)
```

The SAM server can simply **serve from a single flat store and return ERROR(1)
for any serial-prefixed path** — the Pi retries at root automatically. (Optionally
it could honour a per-serial subdir later; not needed.)

### The probe sequence — the server MUST tolerate many misses

The boot ROM probes a long list of **optional** files and proceeds when they are
absent. Captured not-found probes include: `recovery.elf`, `recover4.elf`,
`recovery8.img`, `pieeprom.sig`, `dt-blob.bin`, `bootcfg.txt`,
`armstub8-gic.bin`, `bcm2711-rpi-4-b.dtb`, `usercfg.txt`, … **The server must
answer TFTP ERROR(1) for every miss and keep serving — it must not choke, hang,
or abort the session on a not-found.** This is the single most important server
robustness requirement.

### Files actually served for the Pi 400 boot (flat store contents)

Rough order across a successful boot: `config.txt` (first, and re-read several
times), `start4.elf`, `fixup4.dat`, `bcm2711-rpi-400.dtb`, `cmdline.txt`,
`armstub8-rpi4.bin`, `kernel8-rpi4.img`, and the kernel `spectrum4.img`. The
Pi-3 family (not in these logs) would instead pull `bootcode.bin`, `start.elf`,
`fixup.dat` — **distinct names, so one flat store serves both** (the
model-agnostic property, design §6.1).

**The exact file set is illustrative, not fixed.** This capture is from one
`spectrum4` build (it also shows an `armstub8-rpi4.bin` + several `.img` variants
from a related/earlier version); the final build may serve a slightly different set,
and the set **grows with features over time** (e.g. adding HDMI audio would need
extra `.dtb`/`.dtbo` overlay files we don't ship today). The server never encodes a
list — it serves whatever the store holds and ERROR(1)s the rest. **The mechanism
is the invariant; the specific filenames are not.**

---

## 3. Net implementation spec (for i83 + i86)

1. **i86 DHCP responder:** reply to DISCOVER/REQUEST broadcasts with the §1
   OFFER/ACK template — constants + the fixed 32-byte option-43 blob + echoed
   client UUID (opt 97) + `siaddr`=self + `PXEClient` (opt 60). Assign an address
   from a tiny pool (covers the shared-LAN multi-Pi case). Mandatory for Pi-3
   netboot too (no static path) and to satisfy the Pi-4 PXE check.
2. **i83 TFTP server:** octet; **OACK** with `tsize`=actual size + echoed
   `blksize` (handle 1024 and 1468); serve by name from the flat Trinity store;
   **ERROR(1) on every miss** and keep going; tolerate the serial-subdir prefix
   (404 it → the Pi falls back to root); stream large files (start4.elf is
   multi-MB) block by block.
3. **Store contents** = the union of files any target Pi requests (a provisioning
   choice, §7 / i70): the Pi-4 set + the Pi-3 set + `config.txt`/`cmdline.txt` +
   the kernel. Distinct names mean no collisions.

## 4. Pi 3 family — boot-ROM differences (i89)

The captures above are all Pi 4/400. No Pi 3 capture exists yet, so the facts
below are derived from the official Raspberry Pi network-boot documentation and
the long-standing `dnsmasq` Pi-3-netboot recipes (Sources) — not from a wire
capture. The wire-level confirmation (a real Pi 3 capture + Pi 3 golden vectors)
is the optional hardware tail, tracked as **i89b**. What is already settled:

- **DHCP — option-43 is still required.** The Pi 3 boot ROM is also a PXE-style
  client and accepts the OFFER only when the vendor-encapsulated option-43 carries
  the **"Raspberry Pi Boot"** PXE boot-menu string (the `dnsmasq`
  `pxe-service=0,"Raspberry Pi Boot"` recipe — some setups append three trailing
  spaces to the menu name). So the i86 responder's existing fixed option-43 blob is
  the right *mechanism* for both families; whether the Pi 3 boot ROM is byte-strict
  about the exact menu string (trailing spaces, sub-opt 10 prompt) is the one thing
  only a capture settles — hence i89b.
- **DHCP — Pi 3B option ordering.** On a **Pi 3B** specifically, option-66 (TFTP
  server) must appear **after** option-43 in the reply or the boot ROM uses the
  wrong TFTP server; the Pi 4 is order-insensitive. The Pi 3 also **ignores the
  standard DHCP bootfile name** and drives the transfer entirely from TFTP.
- **TFTP — the Pi 3 requests `bootcode.bin` first.** The Pi 3 boot ROM has no
  built-in stage that fetches `start.elf` directly; it pulls **`bootcode.bin`**
  (the second-stage loader, absent on Pi 4) first, then `start.elf` + `fixup.dat`
  + `config.txt`/`cmdline.txt` + the kernel. Same **serial-subdir-then-root** probe
  behaviour as the Pi 4 (§2).
- **Server impact: none.** The i83 server keys on the filename alone and the two
  families' firmware names are disjoint (`bootcode.bin`/`start.elf`/`fixup.dat` vs
  `start4.elf`/`fixup4.dat`), so **one flat store serves both** with no model
  awareness. This is now pinned by `TestServerServesBothPiFamilies` in
  `tools/netboot-oracle/oracle_test.go`, which resolves both file sets (sizes from
  the `http.RPiFirmware` manifest) through the same store.

## 5. Open follow-ups

- **i89b** — capture a **Pi 3** netboot on real hardware (hub + Wireshark) to
  confirm the §4 facts on the wire and generate Pi 3 golden vectors (the DHCP
  DISCOVER/OFFER/ACK + the first TFTP probe sequence), the same way the Pi 4
  vectors were produced. Pete-gated (needs a Pi 3 + the SAM + the capture rig).
  The decisive open detail is whether the Pi 3 boot ROM is byte-strict about the
  option-43 menu string.
- A fresh clean Pi-4 capture is **not** needed — the present one is complete.
- TFTP `blksize` values seen (1024 / 1468) should both be accepted; confirm no
  other sizes when the Pi-3 capture lands.

## Sources

- `~/tftp-logs/rpi400-boot-spectrum4.pcapng`, `dump.pcap`, `dnsmasq.log` (Pete's
  Mac captures, 2026; out of repo per the publication policy).
- Option/field decoding: RFC 2131/2132 (DHCP/BOOTP), RFC 4578 (PXE/BOOTP arch
  options), Intel PXE spec (option-43 sub-options), RFC 1350/2347/2348/2349
  (TFTP) — see [`tftp-protocol-research.md`](tftp-protocol-research.md).
- Pi 3 family (§4): Raspberry Pi *Network boot your Raspberry Pi* documentation
  (`raspberrypi.com/documentation/computers/remote-access.html`); the GSI
  "Network boot a bunch of Raspberry Pi 3" how-to; `dnsmasq-discuss` /
  raspberrypi/firmware issue threads on Pi 3B `pxe-service`/option-43/option-66
  ordering and the `bootcode.bin`-first request order.
- [`trinity-capabilities.md`](trinity-capabilities.md) — the ENC28J60 stack the
  responder/server sit on.
