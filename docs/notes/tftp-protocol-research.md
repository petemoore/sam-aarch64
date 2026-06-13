# TFTP Protocol Research

**Purpose:** Ground the Phase-3 TFTP client design (i84) in the actual RFCs and in what the simonowen/trinload source already provides, rather than guessing.
This note covers RFC 1350 (core TFTP), RFC 2347 (option extension / OACK), RFC 2348 (blksize), RFC 2349 (tsize/timeout), and RFC 7440 (windowsize), plus a read of the trinload Z80 source to identify reusable layers and the precise delta for an RRQ client state machine.
Cross-reference `docs/notes/trinity-capabilities.md` for ENC28J60 hardware, port map, throughput estimates, and SPI mechanics — those facts are not restated here.

---

## 1. RFC 1350 — TFTP Protocol Revision 2

### 1.1 Opcodes

TFTP defines five packet types (RFC 1350 §3):

| Opcode | Name | Direction (client→server) |
|--------|------|--------------------------|
| 1 | RRQ — Read Request | → server |
| 2 | WRQ — Write Request | → server |
| 3 | DATA | server → client (for RRQ) |
| 4 | ACK | → server (for RRQ) |
| 5 | ERROR | either direction |

### 1.2 Packet Wire Formats

All fields are big-endian.
The ENC28J60 frame buffer stores packets in network byte order, so the Z80 must byte-swap 16-bit fields when reading them (or use per-byte access).

**RRQ / WRQ (RFC 1350 §5):**

| Offset | Field | Size | Value |
|--------|-------|------|-------|
| 0 | Opcode | 2 bytes | 1 = RRQ, 2 = WRQ |
| 2 | Filename | variable | null-terminated ASCII |
| 2+n | Mode | variable | null-terminated ASCII: `"octet"` |

For TFTP options (RFC 2347), additional null-terminated option-name / value pairs follow the mode, before the closing null: `filename NUL mode NUL opt1 NUL val1 NUL … optN NUL valN NUL`.

**DATA (RFC 1350 §5):**

| Offset | Field | Size | Value |
|--------|-------|------|-------|
| 0 | Opcode | 2 bytes | 3 |
| 2 | Block # | 2 bytes | 1-based, wraps after 65535 |
| 4 | Data | 0–512 bytes | file payload (default block size) |

**ACK (RFC 1350 §5):**

| Offset | Field | Size | Value |
|--------|-------|------|-------|
| 0 | Opcode | 2 bytes | 4 |
| 2 | Block # | 2 bytes | matches DATA block # being acknowledged |

**ERROR (RFC 1350 §5):**

| Offset | Field | Size | Value |
|--------|-------|------|-------|
| 0 | Opcode | 2 bytes | 5 |
| 2 | ErrorCode | 2 bytes | 0–7 (see §1.4) |
| 4 | ErrMsg | variable | null-terminated ASCII diagnostic |

### 1.3 Lock-Step ACK Protocol

The RFC 1350 transfer loop is strictly lock-step (RFC 1350 §3):

1. Client sends RRQ to server port 69.
2. Server allocates an ephemeral source port (its TID) and sends DATA block 1 from that port.
3. Client sends ACK 1 to the server's ephemeral port.
4. Server sends DATA block 2; client sends ACK 2; and so on.
5. A DATA packet shorter than 512 bytes (the default block size, or the negotiated blksize) signals end-of-transfer.
6. The client sends a final ACK for the last block and the transfer is complete.

### 1.4 Error Codes

RFC 1350 §5 defines eight error codes:

| Code | Meaning |
|------|---------|
| 0 | Not defined; see error message |
| 1 | File not found |
| 2 | Access violation |
| 3 | Disk full or allocation exceeded |
| 4 | Illegal TFTP operation |
| 5 | Unknown transfer ID |
| 6 | File already exists |
| 7 | No such user |

RFC 2347 (option extension) adds error code 8: option negotiation failed.

### 1.5 TID / UDP-Port Semantics

TID stands for Transfer Identifier (RFC 1350 §1).
Every endpoint randomly picks its own TID (a 16-bit UDP source port) at the start of a transfer.
The sequence for an RRQ is:

1. Client picks TID_c (a random source port) and sends RRQ to server UDP port 69.
2. Server picks TID_s (its own random ephemeral port) and sends DATA from TID_s to TID_c.
3. Every subsequent ACK from the client goes to TID_s, not to port 69.
4. If a DATA arrives from an unknown source port the client sends ERROR 5 ("Unknown TID") and ignores the packet (RFC 1350 §4).

For the SAM client, TID_c can be any port above 1024 (e.g. a fixed value like 49152 is fine on a direct cable with one server).

### 1.6 Block-Number Wraparound for Files > 32 MB

The block number is a 16-bit unsigned counter starting at 1 (RFC 1350 §5).
At the default 512-byte block size, 65535 blocks × 512 bytes = 33,552,384 bytes ≈ 32 MB.
Files larger than this cause the counter to wrap from 65535 to 0 and then to 1, creating ambiguity about whether a block is new or a retransmit.
RFC 1350 does not resolve this — it is the primary motivation for the blksize option (RFC 2348), which makes 32 MB reachable in fewer blocks (e.g. at blksize=1428, the ceiling rises to 65535 × 1428 ≈ 93 MB).
For Phase-3 use (kernel images are a few MB), wraparound is not a concern at any blksize.

### 1.7 Sorcerer's Apprentice Syndrome

RFC 1350 §6 documents a classic TFTP livelock:

1. Client sends RRQ; no ACK arrives in time (network loss or slow server).
2. Client retransmits RRQ.
3. Server now has two RRQ copies and sends two copies of DATA 1.
4. Client ACKs each copy separately; each ACK causes the server to send DATA 2 — so two copies of every subsequent block are sent for the rest of the transfer.

The fix: once a transfer is in progress (i.e. after the first DATA block is received), the client must only retransmit the last ACK on timeout — it must never re-send the original RRQ.
For a Z80 client with simple timeout/retry logic, the rule is: after the state machine transitions from "waiting for OACK or DATA 1" to "in-transfer", retransmit the last ACK only.

---

## 2. RFC 2347 — TFTP Option Extension

### 2.1 Mechanism

RFC 2347 lets a client request transfer options by appending null-terminated name/value pairs to the RRQ or WRQ, after the mode field (RFC 2347 §2):

```
  | opcode | filename NUL | mode NUL | opt1 NUL val1 NUL | … | optN NUL valN NUL |
```

The maximum RRQ packet size is 512 bytes including all options (RFC 2347 §2).

### 2.2 OACK Packet Format

The server responds to a recognized option set with an Option Acknowledgment (RFC 2347 §2):

| Offset | Field | Size | Value |
|--------|-------|------|-------|
| 0 | Opcode | 2 bytes | 6 |
| 2 | Negotiated pairs | variable | opt1 NUL accepted-val1 NUL … |

### 2.3 Negotiation Rules

- A server that does not understand options ignores them, sends no OACK, and replies with DATA (for RRQ) or ACK (for WRQ) — the client falls back to plain RFC 1350 (RFC 2347 §2).
- The server may accept a subset of options or accept an option with a different value (e.g. a smaller blksize than requested) — unacknowledged options are treated as if never sent (RFC 2347 §2).
- After receiving an OACK, the client acknowledges it with ACK block 0 before the transfer begins (RFC 2347 §2).

### 2.4 Interaction with the Lock-Step Loop

For RRQ the sequence with options becomes:
1. Client → RRQ with options (to port 69).
2. Server → OACK (from ephemeral TID_s).
3. Client → ACK block 0 (to TID_s).
4. Server → DATA block 1 (first real block).
5. Transfer proceeds as normal.

If the server sends DATA block 1 directly (no OACK), the client proceeds without option negotiation.

---

## 3. RFC 2348 — blksize Option

### 3.1 Valid Range

The `blksize` option value is the number of data octets per DATA packet, excluding the 4-byte TFTP header (RFC 2348 §2).
Valid range: 8–65464 bytes inclusive.
Values outside this range must be rejected by the server with ERROR 8.

### 3.2 MTU and Throughput

RFC 2348 §2 recommends 1428 bytes as the Ethernet-optimal value: 1500 byte Ethernet MTU − 20 byte IP header − 8 byte UDP header − 4 byte TFTP DATA header = 1468 bytes, then 40 bytes of headroom for some implementations = 1428 bytes.
At 1428 bytes versus 512 bytes the RFC reports a 2.8× throughput improvement from fewer round-trips.

On the Trinity's 10BASE-T link (half-duplex, ~10 Mbps wire speed), each round-trip costs approximately one RTT.
On a direct cable with no switch latency, RTT is dominated by the Z80 processing time per block (see `trinity-capabilities.md` §5 for the ~70–110 KB/s bulk read figure).
A larger blksize means fewer RRQ/OACK/ACK round-trips for the same file, which is the primary gain on the SAM.

### 3.3 Recommendation for SAM Client

`blksize=1428` fits in a single Ethernet frame without IP fragmentation and avoids the need for reassembly logic.
The ENC28J60's RX buffer is 6.5 KB (`rx_end EQU &19FF`, `encdrv.asm:6`), which fits one 1428-byte block comfortably.
The TX buffer is 1.5 KB (`tx_end EQU &1FFF`, `encdrv.asm:8`) — the outbound ACK is only 4 bytes of TFTP data, so TX size is not a constraint.

---

## 4. RFC 2349 — Timeout and Transfer-Size Options

### 4.1 timeout Option

The `timeout` option value is the retransmission timeout in seconds, as an ASCII decimal string in the range "1"–"255" (RFC 2349 §2).
The server echoes the accepted value in the OACK.
If not negotiated, TFTP implementations typically default to 5 seconds; the exact default is not specified in RFC 1350.

For a Z80 client connected over a direct cable, 2 seconds is a reasonable request — tight enough to recover from packet loss without wasting time, generous enough to survive Z80 processing overhead on the server side.

### 4.2 tsize Option

The `tsize` option specifies a file size in bytes as an ASCII decimal string (RFC 2349 §3).

**The key behaviour for an RRQ client (RFC 2349 §3):** if the client sends `tsize=0` in the RRQ, the server returns the actual file size in the OACK as `tsize=<N>`.
The client receives the exact byte count before the first DATA block.

This is directly relevant for i84: knowing the file size in advance lets the Z80 pre-compute how many 512-byte B-DOS sectors (or 800 KB records) are needed and pre-allocate them before the first DATA block arrives, rather than growing a buffer dynamically.
A file of N bytes requires ⌈N / 512⌉ sectors = ⌈N / (512 × 10 × 2 × 80)⌉ = ⌈N / 819200⌉ B-DOS records, where each record maps to one 800 KB floppy image.
For a WRQ (uploading a file to the server), the client sends `tsize=<actual size>` and the server echoes it back to confirm acceptance.

---

## 5. RFC 7440 — windowsize Option

### 5.1 Windowed Transfer Mechanism

The `windowsize` option extends TFTP from lock-step to a sliding-window protocol (RFC 7440 §3).
The negotiated window W means the server sends W consecutive DATA blocks before pausing for an ACK.
The ACK number in the window covers the highest consecutive block successfully received: the receiver sends ACK for the last block of the window, not for each block individually (RFC 7440 §3).

### 5.2 ACK Cadence

Without windowing (W=1): one ACK per DATA block — lock-step.
With W=4: the server sends DATA 1, DATA 2, DATA 3, DATA 4, then waits.
The client sends ACK 4 (if all four arrived), and the server continues with DATA 5–8.
If DATA 3 was lost, the client sends ACK 2 (the last consecutive block received); the server retransmits from DATA 3 onward (RFC 7440 §3).

### 5.3 Retransmission on Loss

On a sequence gap, the receiver sends an ACK for the last consecutive block received (RFC 7440 §3).
The sender must interpret this as a retransmit request starting from the next block after the ACK.
On timeout (no ACK within the timeout period), the sender retransmits from the last-ACKed block + 1 (RFC 7440 §3).

### 5.4 Why It Matters for a Slow Z80

At lock-step with 512-byte blocks, every 512 bytes costs one full RTT.
The Z80 processing overhead per block (receive packet, verify, send ACK, wait for next) is the bottleneck at ~70–110 KB/s (see `trinity-capabilities.md` §5).
Increasing blksize to 1428 cuts round-trips by ~2.8×.
Adding windowsize=4 means the server can burst 4 × 1428 = 5712 bytes before the Z80 must respond — it can pipeline server transmission against Z80 processing time.
RFC 7440 §4 reports 84% time reduction at W=16 on gigabit Ethernet; on a 10 Mbps half-duplex link the gain is smaller but the principle is the same: the Z80 ACK cadence is the bottleneck, and a window reduces ACK rate.

### 5.5 RX Buffer Constraint

With windowsize=W and blksize=B, the receiver must buffer W × B bytes before it can drain the first block.
At W=4, blksize=1428: 4 × 1428 = 5712 bytes.
The ENC28J60 RX ring is 6.5 KB (`rx_end EQU &19FF` = 6656 bytes, `encdrv.asm:6`), so W=4 at blksize=1428 just fits.
W=8 at blksize=1428 = 11424 bytes — exceeds the 6.5 KB RX ring; the ENC28J60 will start dropping frames.
The SAM client must not request a window that would overflow the ENC28J60 RX ring.

### 5.6 Recommendation for SAM Client

Request `windowsize=4` with `blksize=1428`.
This keeps the RX ring utilisation within the 6.5 KB hardware limit while delivering a worthwhile pipelining benefit.
If the server does not support windowsize, fall back gracefully to lock-step (the absence of an OACK or an OACK without a windowsize field means W=1 is in effect).

### 5.7 Full Recommended RRQ Option Set

The recommended option set to include in the RRQ:

```
blksize NUL 1428 NUL tsize NUL 0 NUL timeout NUL 2 NUL windowsize NUL 4 NUL
```

Rationale:
- `blksize=1428`: largest single-Ethernet-frame payload, 2.8× fewer round-trips vs 512.
- `tsize=0`: server returns file size in OACK — the Z80 can pre-allocate B-DOS records before the first DATA block.
- `timeout=2`: 2-second retransmit; generous enough for Z80 overhead, tight enough to recover fast.
- `windowsize=4`: server bursts 4 blocks before ACK; fits within the 6.5 KB ENC28J60 RX ring.

If the server ignores the OACK (RFC 2347 fallback), the client proceeds at 512-byte lock-step — functional, just slower.

---

## 6. trinload Source Analysis

### 6.1 What trinload IS (Not a TFTP Client)

`simonowen/trinload` (`~/git/trinload/`) is a **network code loader** for the SAM Coupé.
It does **not implement TFTP**.
It implements a custom binary protocol using three single-byte commands within a UDP datagram:
- `?` (ASCII 0x3F): discovery — server responds `!`
- `@` (ASCII 0x40): data block — carries page number, target address, and raw Z80 bytes to LDIR into memory
- `X` (ASCII 0x58): execute — jumps to an address with a given HMPR page set

The trinload protocol is proprietary and only works with a matching host-side sender tool.
A TFTP client state machine is an entirely new layer that sits on top of trinload's existing Ethernet/IPv4/UDP infrastructure.

### 6.2 UDP Send/Receive Paths

**Receive path** (`trinload.asm:61–71`): the `read_loop` calls `drv_read` (`encdrv.asm:99`) to poll for a packet into the `packet` buffer.
`drv_read` checks `EPKTCNT` (packet count register); if non-zero it reads the ENC28J60 hardware RX packet header (`rx_status`, 6 bytes) then the frame payload via `rd_buf_mem`.
`rd_buf_mem` (`encdrv.asm:343–372`) uses the RBM (Read Buffer Memory) opcode `&3A`, then enters the `rd_buf_lp` busy-poll + `INI` loop under auto-null mode.

**Transmit path** (`encdrv.asm:153`): `drv_write` sets TX buffer pointers, writes a single control-flag byte and the packet data via `wr_buf_mem`, then issues the transmit command (`ECON1 TXRTS`) and polls `EIR.TXIF`/`TXERIF` with up to 16 retries for the ENC28J60 R5 errata.
`wr_buf_mem` (`encdrv.asm:374–389`) uses the WBM opcode `&7A` and a busy-poll + `OUTI` loop.

### 6.3 ARP Responder

`trinload.asm:78–105` handles ARP.
On an incoming frame with EtherType `&0806` (ARP), it checks the ARP opcode (`packet+21`) for 1 = request, then validates the target IP against `sam_ip` (`packet+38` and `packet+40`).
If matched, it calls `return_eth` (swap MAC addresses in Ethernet header, `trinload.asm:275–283`) and `return_arp` (swap ARP sender/target MAC+IP fields, `trinload.asm:260–272`), then calls `drv_write` with the modified packet (`bc=42`).
The ARP responder is entirely self-contained and requires no changes for a TFTP client.

### 6.4 IPv4 Header and Checksum Handling

`trinload.asm:107–134` handles IPv4.
It filters fragmented packets (`packet+20` flags, bit 5 = MF; `trinload.asm:111–113`) and dispatches by IP protocol byte (`packet+23`): `&01` = ICMP, `&11` = UDP.

The IP checksum is computed by `checksum_ip` (`trinload.asm:306–318`), which calls `chksum_blk` (`trinload.asm:360–388`).
`chksum_blk` implements the RFC 1071 one's complement sum over 16-bit words in IX-pointed memory with BC holding the word count.
`checksum_ip` reads the IHL field from `packet+14` to compute the header word count, zeros the checksum field at `packet+24`–`packet+25`, runs `chksum_blk`, and writes the result back.

For a TFTP ACK packet (which is a new outgoing UDP datagram), `checksum_ip` must be called after filling in the IP header.
The IP header for the ACK is built by calling `return_ip` (swap src/dst IP from the incoming DATA, `trinload.asm:286–295`) and then `checksum_ip`.

### 6.5 UDP Header Handling

`trinload.asm:136–143` dispatches to UDP.
`try_udp` checks IP protocol `&11` and then the destination port at `packet+36` (big-endian 16-bit), comparing against the trinload listen port `&EDB0` (port 60848 in decimal).

`return_udp` (`trinload.asm:298–303`) swaps source and destination ports in the UDP header — the Z80 loads the incoming source/destination ports, then stores them back transposed.
For a TFTP client, this mechanism must be adapted: the client sends ACKs to TID_s (the server's ephemeral source port from the DATA packet, `packet+34`), not to a fixed port.
The `return_udp` swap already achieves exactly this: the incoming DATA's source port (`packet+34`) becomes the outgoing ACK's destination port (`packet+36`).

UDP checksum is set to zero (`checksum_udp`, `trinload.asm:352–356`) — this is legal for IPv4 UDP (RFC 768).

`set_udp_data_len` (`trinload.asm:232–246`) computes and fills the UDP length field (`packet+38`–`packet+39`) and the IP total-length field (`packet+16`–`packet+17`) from the data length in BC, and returns the full Ethernet frame length in BC for `drv_write`.

`ack_len` (`trinload.asm:215–228`) is a compound helper that calls `set_udp_data_len`, then `return_eth`, `return_ip`, `return_udp`, `checksum_ip`, `checksum_udp`, and finally `drv_write`.
It is the complete "send a UDP reply" primitive — a TFTP ACK sender can call `ack_len` directly after filling in the TFTP ACK payload.

### 6.6 Buffer Sizing

`packet defs 1518` (`trinload.asm:419`) — the frame buffer is sized for the maximum Ethernet frame (1518 bytes: 6 + 6 + 2 header + 1500 payload + 4 CRC).
A TFTP DATA packet with blksize=1428 fits: 14 (Ethernet) + 20 (IP) + 8 (UDP) + 4 (TFTP) + 1428 (data) = 1474 bytes, well under 1518.
At blksize=512 (the RFC 1350 default) each DATA packet is 14 + 20 + 8 + 4 + 512 = 558 bytes.

ENC28J60 buffer split (`encdrv.asm:5–8`):
- RX ring: `&0000`–`&19FF` = 6656 bytes (6.5 KB)
- TX buffer: `&1A00`–`&1FFF` = 1536 bytes (1.5 KB)

The TX buffer is sized for the maximum outgoing packet; the largest ACK a TFTP client sends is 42 bytes (14 Ethernet + 20 IP + 8 UDP + 4 TFTP ACK) — well inside 1.5 KB.

### 6.7 MAC / IP Configuration Source

`trinload.asm:31–44` loads the "Trinity Network " chunk from EEPROM using `find_index` / `read_chunk` (both in `eeprom.asm`).
After the call, `chunk` holds 1 KB of EEPROM data.
`sam_mac` and `sam_ip` are aliases into this buffer:

```
sam_mac:    equ chunk+0    ; trinload.asm:414 — 6-byte MAC address
sam_ip:     equ chunk+6    ; trinload.asm:415 — 4-byte IPv4 address
```

`drv_init` is called with `hl=chunk+0` (`trinload.asm:53`) so the ENC28J60 is initialised with the EEPROM MAC automatically.
The TFTP client inherits the same MAC/IP configuration by reading the same chunk — no new configuration mechanism is needed.

### 6.8 Reusable Routines for a TFTP Client

The following routines from `trinload.asm` + `encdrv.asm` are directly reusable without modification:

| Routine | File:line | Reuse as-is? | Notes |
|---------|-----------|-------------|-------|
| `drv_init` | `encdrv.asm:22` | **Yes** | Initialises ENC28J60, sets MAC, enables RX |
| `drv_read` | `encdrv.asm:99` | **Yes** | Polls for next Ethernet frame into HL buffer |
| `drv_write` | `encdrv.asm:153` | **Yes** | Transmits frame at HL with length BC |
| `drv_exit` | `encdrv.asm:256` | **Yes** | Resets ENC28J60, disables reception |
| `rd_buf_mem` | `encdrv.asm:343` | **Yes** | Bulk RX DMA via RBM + auto-null |
| `wr_buf_mem` | `encdrv.asm:374` | **Yes** | Bulk TX DMA via WBM |
| ARP responder (inlined) | `trinload.asm:78–105` | **Yes** | Handles ARP without modification |
| `return_eth` | `trinload.asm:275` | **Yes** | Swap Ethernet src/dst MACs for reply |
| `return_ip` | `trinload.asm:286` | **Yes** | Swap IPv4 src/dst IPs for reply |
| `return_udp` | `trinload.asm:298` | **Yes** | Swap UDP src/dst ports for reply |
| `checksum_ip` | `trinload.asm:306` | **Yes** | RFC 1071 IP header checksum |
| `chksum_blk` | `trinload.asm:360` | **Yes** | Core RFC 1071 word-sum primitive |
| `checksum_udp` | `trinload.asm:352` | **Yes** | Sets UDP checksum = 0 (valid for IPv4) |
| `set_udp_data_len` | `trinload.asm:232` | **Yes** | Fills UDP/IP length fields; returns frame length in BC |
| `ack_len` | `trinload.asm:215` | **Yes** | Compound: set length + swap addresses + checksum + transmit |
| `chk_trinity` | `encdrv.asm:457` | **Yes** | Probes for Trinity board at startup |
| `find_index` / `read_chunk` | `eeprom.asm` | **Yes** | Load MAC+IP from EEPROM "Trinity Network " chunk |
| `ip_to_eth_len` | `trinload.asm:250` | **Yes** | Compute Ethernet frame length from IP total-length field |

ICMP echo (`trinload.asm:115–134`) is also usable as-is (useful for debugging but optional for TFTP operation).

### 6.9 The Precise Delta: RRQ Client State Machine

Everything in §6.8 is infrastructure that a TFTP RRQ client inherits unchanged from trinload.
The **new code** is a TFTP state machine layer that replaces trinload's `try_data` / `try_exec` custom-protocol dispatch (`trinload.asm:155–211`) with TFTP opcode parsing and RRQ-driven control:

1. **Build and send the RRQ packet.** Fill UDP payload: opcode=1 (2 bytes, big-endian), filename (null-terminated), `"octet"` (null-terminated), then the option string `blksize NUL 1428 NUL tsize NUL 0 NUL timeout NUL 2 NUL windowsize NUL 4 NUL`. Set destination port = 69. Call `set_udp_data_len` and `drv_write`.

2. **Wait for the server's first response.** Call `drv_read` in a poll loop with a timeout counter. On receipt, parse the TFTP opcode from `packet+42` (bytes 42–43 = start of UDP payload for a standard Ethernet/IP/UDP frame).
   - Opcode 6 (OACK): parse option values, record negotiated blksize, tsize, windowsize. Send ACK block 0 via `ack_len`. Save server TID (src port at `packet+34`) for all future ACKs.
   - Opcode 3 (DATA): server ignored options; use defaults (blksize=512, windowsize=1). Process as first DATA block.
   - Opcode 5 (ERROR): print error code / message; abort.

3. **Inner transfer loop.** For each incoming DATA block:
   a. Validate source port matches saved TID_s; send ERROR 5 and discard if not.
   b. Read block number from `packet+44`–`packet+45` (big-endian 16-bit).
   c. Copy data payload (at `packet+46`, length = negotiated blksize, or shorter for the last block) to the write destination (a SAM RAM staging buffer or directly via a B-DOS HRECORD/HSBYT write).
   d. If windowsize > 1: only send ACK after W blocks, or when a short DATA is received, or when a gap is detected.
   e. Send ACK for the current block via `ack_len` (BC = 4 for the 4-byte TFTP ACK payload).
   f. If the DATA payload length < negotiated blksize, the transfer is complete — send the final ACK and exit.

4. **Timeout / retransmit.** On timeout without a DATA, retransmit the last ACK (never re-send the RRQ — Sorcerer's Apprentice fix, §1.7). Retry up to N times (e.g. 5), then abort.

5. **B-DOS write integration.** Each received DATA block (up to 1428 bytes) is written to the Trinity SD card via B-DOS hooks: HRECORD selects the target record; HSBYT / HOFLE write sectors. The `tsize` value received in the OACK tells the Z80 how many records to allocate before writing begins.

Approximate code volume: ~150–200 lines of Z80 assembly for the state machine (the RRQ builder, opcode dispatch, block loop, timeout/retry) on top of the trinload infrastructure.

---

## 7. Design Implications for i84

### 7.1 Protocol Choices

Use octet-mode TFTP (binary transfer) — the kernel image and B-DOS records are raw bytes, not ASCII.
Send the full option set from §5.7 in every RRQ.
If the OACK is absent or does not confirm windowsize/blksize, fall back gracefully to lock-step 512-byte.

### 7.2 Trinload Reuse

The trinload ARP responder, IPv4/UDP framing helpers, and `ack_len` are all reusable as-is.
The TFTP state machine is the only new code; it is a drop-in replacement for trinload's custom `@` / `X` dispatch (approximately `trinload.asm:155–211`).

### 7.3 Block Delivery to B-DOS Records

Received DATA blocks accumulate in a SAM RAM staging area (a page in the upper 32 KB, via HMPR paging).
When the staging area holds a full B-DOS sector (512 bytes), write it via HSBYT or equivalent.
The `tsize=0` OACK value gives the total byte count up front, so the Z80 can pre-allocate the exact number of B-DOS records (`⌈tsize / 819200⌉` records) before the first write.

### 7.4 Memory Layout

The TFTP client code can live in a SAM RAM page above &8000 (using HMPR paging, the same model trinload uses — see `trinload.asm:180–186` where it sets HMPR and LDIRs data to the top-32 KB page).
The `packet` buffer (1518 bytes) and any staging buffers need to be in a mapped page.
The trinload approach of a single flat `packet` buffer at a known address works well.

### 7.5 Open Questions / Unknowns

1. **LIKELY: Server-side TFTP options support.** `tftpd-hpa` (the standard Linux/macOS TFTP daemon) supports blksize and tsize but **not** windowsize (RFC 7440 is relatively recent).
   Check whether Pete's Mac TFTP server (`tftpd-hpa` or `in.tftpd`) advertises RFC 7440 support.
   If windowsize is not available, `blksize=1428` alone still gives the 2.8× improvement.

2. **UNKNOWN: Pi 400 TFTP client (PXE leg).** For i83 (the SAM serving the Pi via TFTP), the Pi's PXE firmware is the TFTP client.
   Its option set (blksize, windowsize) determines what the SAM TFTP server must handle.
   The Pi GPU firmware's `pxelinux` is known to use large blksize; the exact options need measurement.

3. **UNKNOWN: Exact TID_s port range.** `tftpd-hpa` picks an ephemeral port for TID_s.
   On Linux this is typically in the range 32768–60999.
   The Z80 client must record the TID_s from the first OACK or DATA packet and use it for all ACKs — the `return_udp` swap already does this automatically.

4. **LIKELY: EEPROM "Trinity Network " chunk format.** The chunk layout (`sam_mac equ chunk+0`, `sam_ip equ chunk+6`, `trinload.asm:414–415`) is observed from trinload source and confirmed by the `drv_init` call (`hl=chunk+0`).
   The exact byte layout of the chunk (MAC 6 bytes at +0, then 0 bytes at +6? or IP directly?) needs a hardware confirmation.
   `trinload.asm:46–50` checks `sam_mac+0` (MAC first byte) and `sam_ip+6` — the `+6` offset for the IP start is consistent with MAC bytes at offsets 0–5 and IP at 6–9.

5. **UNKNOWN: B-DOS HRECORD behaviour on Trinity SD without Atom Lite emulation.** The i62 proof verified HRECORD via B-DOS AL 1.5a under a floppy/Atom Lite setup.
   The Trinity 1.5t fork's sector-device swap (SD SPI) is known from the i71 analysis.
   End-to-end: TFTP client writes a block → B-DOS 1.5t HRECORD routes it to Trinity SD — this chain is unverified on real hardware and needs a real-Trinity test run.

---

## Sources

- RFC 1350: https://www.rfc-editor.org/rfc/rfc1350.txt
- RFC 2347: https://www.rfc-editor.org/rfc/rfc2347.txt
- RFC 2348: https://www.rfc-editor.org/rfc/rfc2348.txt
- RFC 2349: https://www.rfc-editor.org/rfc/rfc2349.txt
- RFC 7440: https://www.rfc-editor.org/rfc/rfc7440.txt
- `~/git/trinload/trinload.asm` — simonowen/trinload main source
- `~/git/trinload/encdrv.asm` — simonowen/trinload ENC28J60 driver
- `~/git/trinload/eeprom.asm` — simonowen/trinload EEPROM driver
- `docs/notes/trinity-capabilities.md` — Trinity hardware capabilities (ENC28J60 ports, throughput, SPI mechanics)
- `docs/specs/phase3-tftp-design.md` — Phase-3 TFTP design sketch (prior direction)
- `docs/notes/bdos-trinity-fork-analysis.md` — B-DOS 1.5t Trinity fork analysis (hook surface, SD sector-device layer)
- `docs/notes/bdos-version-landscape.md` — HRECORD / HSBYT / HOFLE hook compatibility
