// Package serve is the Go authority for the serve-files TFTP demo server (i96) —
// the combined frame-in/reply-out router the Z80 src/netboot/netboot_serve.asm
// ports. It is a focused, plain-TFTP demo: a SAM + Quazar Trinity that serves a
// few small files baked into the binary to an ordinary TFTP client (busybox/BSD
// `tftp`, `curl tftp://…`) on the LAN — TFTP only, no DHCP and no Pi PXE blob.
// That makes it testable from any machine with a stock `tftp`/`curl` client, with
// no Raspberry Pi, DHCP server, or option-43 negotiation involved.
//
// It composes two already-host-verified responders:
//
//	OnFrame(rxFrame) -> reply, or nil to stay silent:
//	  ARP request for our IP            -> an ARP reply        (smoke.Responder)
//	  UDP dst 69 (TFTP RRQ)             -> serve the file by name (tftp.ServerLoop)
//	  UDP dst 69 (TFTP WRQ) (i121a)     -> learn client endpoint, reply ACK-0 or OACK
//	  UDP dst = our transfer TID (ACK)  -> the next DATA       (tftp.ServerLoop)
//	  anything else                     -> nil (keep serving)
//
// The ARP responder is what lets a plain TFTP client resolve the SAM's MAC before
// its RRQ (there is no DHCP here to do it). The one behaviour beyond the i95 Pi
// server: an RRQ with **no options** is answered per RFC 2347 with DATA block 1
// directly (no OACK), which is what a classic `tftp get` sends; an RRQ that does
// request options (e.g. `curl`'s tsize) is answered with an OACK as before.
//
// WRQ handling (i121a — handshake only): a bare WRQ (no options) is answered with
// ACK-0 (`00 04 00 00`); an optioned WRQ is answered with an OACK echoing the
// accepted blksize and the client's tsize. DATA reception (i121b) is not included
// here; this is the handshake brick only.
//
// This mirrors how the Z80 demo loop dispatches one drv_read frame: it is the
// byte-for-byte porting spec for netboot_serve.asm.
//
// Verification: host-verifiable end-to-end over the i80 emulation (the Z80
// netboot_serve.asm runs the real driver against the emulated Trinity and the
// served frames are asserted byte-for-byte against this authority). NOT
// host-verifiable: the real ENC28J60 silicon and an end-to-end run on real
// hardware — gated on real Trinity (CLAUDE.md §5). Emulation-verified is not
// hardware-verified.
package serve

import (
	"strconv"
	"strings"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/bdos"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/frame"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/smoke"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/tftp"
)

// PlacementStrategy selects WHICH free Trinity record a WRQ disk-record push lands
// in. It mirrors the Z80 serve config block's strategy byte (SERVE_CFG_STRATEGY in
// src/netboot/netboot_serve.asm) and the dispatch in bdos_find_record_for_strategy
// (src/netboot/bdos_seam.asm): the program reads its strategy from a host-patchable
// config block, so a packaging vessel can set it without recompiling.
type PlacementStrategy uint8

const (
	// StrategyHighestFree places at the HIGHEST free record (the baked default —
	// manifest design §4 s3 / decision 4: keeps the user's low, memorable record
	// slots for their own disks; TFTP storage grows down from the top). Z80 strategy
	// byte 0 (SERVE_STRAT_HIGHEST).
	StrategyHighestFree PlacementStrategy = 0
	// StrategyLowestFree places at the LOWEST free record (the pre-i121h behaviour;
	// bdos_find_free_record). Z80 strategy byte 1 (SERVE_STRAT_LOWEST).
	StrategyLowestFree PlacementStrategy = 1
	// StrategyExplicit places at a named record if it is free, else nowhere (->
	// ERROR(3)). Z80 strategy byte 2 (SERVE_STRAT_EXPLICIT); the record is the config
	// block's SERVE_CFG_RECORD.
	StrategyExplicit PlacementStrategy = 2
)

// Config is the SAM's fixed identity for the demo server (no DHCP pool — there is
// no DHCP here).
type Config struct {
	ServerMAC [6]byte
	ServerIP  [4]byte
	ServerTID uint16 // the SAM's ephemeral source port for TFTP transfers

	// DiskRecordPush selects the i121f WRQ "disk-record push" behaviour: a WRQ is
	// streamed into a claimed Trinity record (the body re-blocked into 512-byte
	// sectors via bdos.RawSink), validated as a Trinity disk record on the final
	// block (size == RecordSize AND the BDOS stamp@232), and answered with the
	// final ACK on success or ERROR(3) on a bad image. With it false the WRQ path
	// is the i121a/i121b flat receive-to-staging (no record claim, always ACK) —
	// the Z80 HOSTTEST wire build mirrors this. The Z80 picks the same behaviour
	// from WRQ_SINK_MODE (set by a successful free-record claim in handle_wrq).
	DiskRecordPush bool

	// Strategy is the WRQ disk-record PLACEMENT strategy (i121h). The zero value
	// (StrategyHighestFree) is the default, matching the un-patched Z80 config block.
	// For StrategyExplicit, ExplicitRecord names the record.
	Strategy PlacementStrategy
	// ExplicitRecord is the 1-based record StrategyExplicit places into (the Z80
	// SERVE_CFG_RECORD). Ignored for the other strategies.
	ExplicitRecord int
}

// Responder is the serve-files demo server. It owns the ARP + TFTP sub-responders
// and the flat store, and routes each received frame.
type Responder struct {
	cfg   Config
	store tftp.Store
	src   func(name string) tftp.Source // the file source for a resolved name

	arp  *smoke.Responder
	tftp *tftp.ServerLoop

	// justFirst is set after an OACK reply: the next ACK (of block 0) triggers
	// FirstData (block 1) rather than the OnACK advance. It is NOT set on the
	// bare-RRQ path, where DATA block 1 has already been sent and the next ACK
	// (of block 1) advances normally.
	justFirst bool

	// wrqClient holds the WRQ client endpoint, populated on a WRQ and used when
	// wrapping the ACK-0 / OACK (i121a) and the per-block ACKs (i121b).
	wrqClient wrqEndpoint

	// wrqRecv is the active receive-to-staging state for an accepted WRQ (i121b):
	// the lock-step DATA/ACK loop that accumulates the pushed file into staging.
	// nil when no WRQ transfer is in progress.
	wrqRecv *wrqReceiver

	// freeRecordAvailable models the i121f free-record claim (the Z80
	// bdos_find_free_record result): when DiskRecordPush is set and this is false,
	// a WRQ is rejected with ERROR(3, "no free record") and no receiver is armed
	// (the shared-resource invariant — never touch a named record). Default true;
	// a test sets it false via SetFreeRecordAvailable to drive the all-full case.
	freeRecordAvailable bool

	// freeRecords models the sequence of records bdos_find_free_record would
	// return across successive pushes (i121g). Each successful push CLAIMS the head
	// (writes its record-list name entry), so the next push sees the next free
	// record advance. Empty ⇒ no free record (the all-full case). When nil and
	// freeRecordAvailable is true, a default single free record is assumed (the
	// pre-i121g behaviour for tests that don't exercise multiple pushes).
	freeRecords []int

	// claims records the records claimed by completed valid pushes, in order, with
	// the name written into each (i121g). Mirrors the Z80 bdos_claim_record list
	// writes (BDOSStore.ListWrites). The two-push test asserts the claimed records
	// differ.
	claims []Claim

	// flatStores records the flat-file archive stores completed by FlatFile-class
	// WRQ pushes (i121c): the default storage class, HSAVE'd into a free record via
	// the i119 bdos_save_hook seam rather than streamed+validated as an 819,200-byte
	// disk record. Mirrors the Z80 BDOSStore.Saves the harness captures.
	flatStores []FlatStore
}

// Claim is one completed disk-record claim: the record marked used and the name
// written into its central record-list entry. Mirrors the Z80 ListWrite the
// harness captures from bdos_claim_record (z80/bdos_store.go).
type Claim struct {
	Record int    // the 1-based record claimed
	Name   string // the record name written (filename-derived, ≤10 chars)
}

// FlatStore is one completed flat-file archive store (i121c): the storage name
// (the WRQ filename after bdos.Classify strips any class prefix) and the bytes
// HSAVE'd into the claimed record. Mirrors the Z80 BDOSStore.BDOSSave the harness
// captures from bdos_save_hook (name + the saved file bytes).
type FlatStore struct {
	Record int    // the 1-based record HSAVE'd into
	Name   string // the storage name (Classify internal name)
	Data   []byte // the stored file bytes
}

// wrqEndpoint is the client-side identity learned from a WRQ frame (i121a).
type wrqEndpoint struct {
	mac [6]byte
	ip  [4]byte
	tid uint16
}

// wrqReceiver is the receive-to-staging state for an accepted WRQ (i121b). It
// mirrors tftp.ClientXfer's receive side (block-check, accumulate, ACK,
// short-final-block end) but on the server's WRQ side: the peer is the WRQ
// client, and the per-block ACKs go back to it. The first DATA the server
// expects is block 1 (after the ACK-0 / OACK handshake), so acked starts at 0.
type wrqReceiver struct {
	blksize int    // negotiated block size (512 bare WRQ, else the accepted blksize)
	acked   uint16 // the highest block number ACKed so far
	done    bool   // a short (< blksize) block has ended the transfer
	data    []byte // accumulated staged bytes (flat receive-to-staging path)

	// Disk-record push state (i121f), used only when DiskRecordPush is set. The
	// pushed body is streamed into sink (re-blocked into 512-byte sectors, the Z80
	// raw_record_sink authority); on the short final block the result is validated
	// (bdos.ValidateDiskRecord over sink.total + sector-0 of the written bytes) and
	// valid records the outcome the WRQPushOutcome accessor returns.
	push     bool
	sink     *bdos.RawSink
	valid    bool   // set on the final block: the streamed image validated
	filename string // the WRQ filename (the claim name is derived from it, i121g)

	// Flat-file archive push state (i121c), set when the WRQ filename has no
	// "trinity-sam-disks/" prefix (bdos.Classify -> FlatFile, the default class).
	// The body accumulates into data (the flat receive-to-staging buffer) and on the
	// short final block is HSAVE'd into the claimed record (finalizeFlat) instead of
	// validated as an 819,200-byte disk record. flat and push are mutually exclusive.
	flat     bool
	flatName string // the storage name (Classify internal name, prefix stripped)
}

// New builds a serve-files demo server over a flat store. src(name) yields the
// file Source for a resolved filename (on the SAM the file is assembled into the
// binary; in the harness a ByteSource).
func New(cfg Config, store tftp.Store, src func(name string) tftp.Source) *Responder {
	return &Responder{
		cfg:                 cfg,
		store:               store,
		src:                 src,
		arp:                 smoke.NewResponder(cfg.ServerMAC, cfg.ServerIP),
		tftp:                tftp.NewServerLoop(store, cfg.ServerMAC, cfg.ServerIP, cfg.ServerTID),
		freeRecordAvailable: true,
	}
}

// SetFreeRecordAvailable models the i121f free-record claim (the Z80
// bdos_find_free_record result) for the disk-record push path: false makes the
// next WRQ reject with ERROR(3, "no free record") and arm nothing. Only meaningful
// when Config.DiskRecordPush is set. Default (from New) is true.
func (r *Responder) SetFreeRecordAvailable(v bool) { r.freeRecordAvailable = v }

// SetFreeRecords seeds the sequence of records bdos_find_free_record returns
// across successive pushes (i121g): each valid push claims the head, so the next
// push advances to the next entry. An empty/nil slice ⇒ no free record (the
// all-full ERROR(3) case). Setting a non-empty slice also marks records as
// available. Only meaningful when Config.DiskRecordPush is set.
func (r *Responder) SetFreeRecords(records []int) {
	r.freeRecords = append([]int(nil), records...)
	r.freeRecordAvailable = len(records) > 0
}

// Claims returns the records claimed by completed valid pushes, in order, each
// with the name written into its record-list entry (i121g). The two-push test
// asserts successive claims target different records.
func (r *Responder) Claims() []Claim { return r.claims }

// FlatStores returns the flat-file archive stores completed by FlatFile-class WRQ
// pushes (i121c), in order — each the record claimed, the storage name, and the
// HSAVE'd bytes. The Z80 test asserts these against BDOSStore.Saves.
func (r *Responder) FlatStores() []FlatStore { return r.flatStores }

// nextFreeRecord returns the record the next push would claim (the LOWEST free
// record — the head of the free-record sequence), or 0 when none is free. When
// freeRecords is nil but freeRecordAvailable is true, it falls back to a single
// default free record (the pre-i121g single-push behaviour). This is the
// lowest-first base that nextRecordForStrategy specialises per placement strategy.
func (r *Responder) nextFreeRecord() int {
	if len(r.freeRecords) > 0 {
		return r.freeRecords[0]
	}
	if r.freeRecords == nil && r.freeRecordAvailable {
		return defaultFreeRecord
	}
	return 0
}

// nextRecordForStrategy returns the record the next push places into per the
// configured placement strategy (Config.Strategy), or 0 when none is available.
// Mirrors the Z80 bdos_find_record_for_strategy (src/netboot/bdos_seam.asm):
//   - StrategyHighestFree (default): the HIGHEST free record.
//   - StrategyLowestFree: the LOWEST free record (nextFreeRecord).
//   - StrategyExplicit: Config.ExplicitRecord if it is free, else 0.
//
// "Free" is the modelled freeRecords set (the records bdos_find_*_record would
// return); the nil-but-available fallback keeps the single-default-record path for
// tests that seed only freeRecordAvailable.
func (r *Responder) nextRecordForStrategy() int {
	switch r.cfg.Strategy {
	case StrategyLowestFree:
		return r.nextFreeRecord()
	case StrategyExplicit:
		if r.recordIsFree(r.cfg.ExplicitRecord) {
			return r.cfg.ExplicitRecord
		}
		return 0
	default: // StrategyHighestFree (and any unrecognised value)
		return r.highestFreeRecord()
	}
}

// highestFreeRecord returns the highest record in the modelled free set, or 0 if
// none. Mirrors the Z80 bdos_find_highest_free_record (downward scan).
func (r *Responder) highestFreeRecord() int {
	highest := 0
	for _, n := range r.freeRecords {
		if n > highest {
			highest = n
		}
	}
	if highest == 0 && r.freeRecords == nil && r.freeRecordAvailable {
		return defaultFreeRecord
	}
	return highest
}

// recordIsFree reports whether record n is in the modelled free set (or the
// single-default fallback when only freeRecordAvailable is seeded). Used by the
// explicit-record strategy. n <= 0 is never free (records are 1-based).
func (r *Responder) recordIsFree(n int) bool {
	if n <= 0 {
		return false
	}
	for _, f := range r.freeRecords {
		if f == n {
			return true
		}
	}
	if r.freeRecords == nil && r.freeRecordAvailable {
		return n == defaultFreeRecord
	}
	return false
}

// removeFreeRecord drops record n from the modelled free set (a claim marks it
// used). The Z80 side achieves this via bdos_claim_record writing the record-list
// name entry; here it advances the model so the next push won't re-pick n.
func (r *Responder) removeFreeRecord(n int) {
	out := r.freeRecords[:0]
	for _, f := range r.freeRecords {
		if f != n {
			out = append(out, f)
		}
	}
	r.freeRecords = out
}

// defaultFreeRecord is the record a single-push test claims when it seeds only
// freeRecordAvailable (not an explicit freeRecords sequence).
const defaultFreeRecord = 1

// WRQPushOutcome returns the result of the active/last disk-record push: the
// 512-byte sector writes the body re-blocked into (via bdos.RawSink), the total
// streamed byte count, whether the transfer completed (a short final block), and
// whether the streamed image validated as a Trinity disk record. The Z80 test
// compares its captured SectorWrites() + BD_REC_VALID against these.
func (r *Responder) WRQPushOutcome() (writes []bdos.RawSectorWrite, total int, done, valid bool) {
	if r.wrqRecv == nil || !r.wrqRecv.push {
		return nil, 0, false, false
	}
	rc := r.wrqRecv
	return rc.sink.Writes(), rc.sink.Total(), rc.done, rc.valid
}

// OnFrame routes one received Ethernet frame and returns the reply frame to
// transmit, or nil to stay silent. ARP first (cheapest), then the TFTP RRQ /
// transfer-ACK paths.
func (r *Responder) OnFrame(rx []byte) []byte {
	// 1. ARP request for our IP -> an ARP reply (so a plain client can resolve
	//    the SAM's MAC without DHCP).
	if reply := r.arp.OnFrame(rx); reply != nil {
		return reply
	}

	u, ok := frame.ParseUDP(rx)
	if !ok {
		return nil
	}

	// 2. TFTP request on port 69 (RRQ or WRQ). Parse opcode first to dispatch.
	if u.DstPort == 69 {
		r.justFirst = false
		req, err := tftp.ParseRequest(u.Payload)
		if err != nil {
			return nil
		}

		// WRQ (i121a): learn the client endpoint and reply ACK-0 (bare WRQ)
		// or OACK (optioned WRQ). DATA reception is a later brick (i121b).
		if req.Opcode == tftp.OpWRQ {
			return r.startWrite(u, req)
		}

		// RRQ: on a hit install the transfer source, then either OACK (options
		// requested) or DATA block 1 directly (a bare RRQ, RFC 2347).
		hit := false
		hasOpts := false
		if req.Opcode == tftp.OpRRQ {
			if action, _ := tftp.Resolve(r.store, req.Filename); action == tftp.ActionOACK {
				r.tftp.SetSource(r.src(req.Filename))
				hit = true
				hasOpts = len(req.Options) > 0
			}
		}
		if hit && hasOpts {
			r.justFirst = true
			return r.tftp.StartTransfer(rx, true)
		}
		// A hit with no options -> DATA block 1 directly; a miss -> ERROR(1).
		return r.tftp.StartTransfer(rx, false)
	}

	// 3. A frame on our transfer TID (UDP dst = our transfer TID). During a WRQ
	//    receive it is the client's DATA (accumulate + ACK); during an RRQ serve
	//    it is the client's ACK (advance / FirstData).
	if u.DstPort == r.cfg.ServerTID {
		if r.wrqRecv != nil && tftp.Opcode(u.Payload) == tftp.OpDATA {
			return r.recvData(u)
		}
		if r.justFirst {
			r.justFirst = false
			return r.tftp.FirstData()
		}
		return r.tftp.OnACK(rx)
	}

	return nil
}

// recvData processes one received DATA frame of an accepted WRQ transfer: it
// validates the source TID is the WRQ client, runs the block-check / accumulate
// / short-final-block logic (mirroring tftp.ClientXfer.OnData), and returns the
// per-block ACK wrapped back to the client. A future/out-of-window block draws
// no reply (nil). On the short final block the receiver is left in place with
// done set (the staged bytes are available); the i121c write brick consumes it.
func (r *Responder) recvData(u frame.UDP) []byte {
	if u.SrcPort != r.wrqClient.tid {
		return nil // a stray sender on our transfer TID: ignore
	}
	block, data, err := tftp.ParseDATA(u.Payload)
	if err != nil {
		return nil
	}
	rcv := r.wrqRecv
	switch {
	case block == rcv.acked+1:
		// The next expected block: route the payload (the flat staging accumulate,
		// or the disk-record streaming sink), ACK it, end on a short block.
		if rcv.push {
			rcv.sink.Write(data)
		} else {
			rcv.data = append(rcv.data, data...)
		}
		rcv.acked = block
		if len(data) < rcv.blksize {
			rcv.done = true
			if rcv.push {
				return r.finalizePush(rcv, block)
			}
			if rcv.flat {
				return r.finalizeFlat(rcv, block)
			}
		}
		return r.wrapToWRQClient(tftp.BuildACK(block))
	case block <= rcv.acked:
		// A duplicate (the client retransmitted): re-ACK it, don't re-stage.
		return r.wrapToWRQClient(tftp.BuildACK(block))
	default:
		// A future block we haven't reached: ignore (no gap-filling).
		return nil
	}
}

// finalizePush commits a disk-record push on its short final block: flush the
// sink's final partial sector, validate the streamed image as a Trinity disk
// record (size == RecordSize from sink.Total() AND the BDOS stamp@232 of the
// written sector 0), and reply with the final ACK on a valid image or
// ERROR(3, "invalid disk record") on a bad one. Port of netboot_serve.asm
// wd_finalize; the validation is bdos.ValidateDiskRecord (the same authority the
// Z80 bdos_validate_disk_record ports).
func (r *Responder) finalizePush(rcv *wrqReceiver, block uint16) []byte {
	rcv.sink.Finish()

	var sector0 []byte
	if w := rcv.sink.Writes(); len(w) > 0 {
		sector0 = w[0].Data[:]
	} else {
		sector0 = make([]byte, bdos.SectorSize) // no sectors written: a zero first sector
	}
	rcv.valid = bdos.ValidateDiskRecord(rcv.sink.Total(), sector0) == nil
	if rcv.valid {
		// CLAIM the record (i121g): mark the just-written record used by recording
		// its record-list name entry, and drop it from the free set so the next push
		// lands on the next record per the strategy. The record is the one the
		// strategy picked (highest-free by default, i121h). Mirrors the Z80
		// bdos_claim_record (which writes the list entry); the claim is recorded ONLY
		// on the valid path (an invalid push leaves the record free for reuse).
		claimed := r.nextRecordForStrategy()
		r.claims = append(r.claims, Claim{Record: claimed, Name: ClaimRecordName(rcv.filename)})
		r.removeFreeRecord(claimed)
		return r.wrapToWRQClient(tftp.BuildACK(block))
	}
	return r.wrapToWRQClient(tftp.BuildError(3, "invalid disk record"))
}

// finalizeFlat commits a FlatFile-class push (i121c) on its short final block: the
// staged body is HSAVE'd into the claimed record as a plain file (no 819,200-byte
// disk-record validation — a flat file is any size), the record is claimed so the
// next push lands elsewhere, and the final ACK is returned. Port of netboot_serve.asm
// wd_finalize_flat (which calls bdos_fill_save_uifa + bdos_save_hook + bdos_claim_record),
// itself the server mirror of netboot_client.asm client_main's flat-file write-out.
// A flat file is never rejected on content: the explicit "trinity-sam-disks/" prefix is
// the only thing that opts a push into the validated disk-record class (design §6.5).
func (r *Responder) finalizeFlat(rcv *wrqReceiver, block uint16) []byte {
	claimed := r.nextRecordForStrategy()
	r.flatStores = append(r.flatStores, FlatStore{
		Record: claimed,
		Name:   rcv.flatName,
		Data:   append([]byte(nil), rcv.data...),
	})
	// Claim the record's central-list name entry so the next push lands on the next
	// free record (the i121g claim, shared with the disk-record path). The name is
	// derived from the WRQ filename, hardened identically (ClaimRecordName).
	r.claims = append(r.claims, Claim{Record: claimed, Name: ClaimRecordName(rcv.filename)})
	r.removeFreeRecord(claimed)
	return r.wrapToWRQClient(tftp.BuildACK(block))
}

// recordNameLen is the full central-list record-name field width: the whole
// 16-byte list entry IS the record name (trinity-record-detection-design.md §4.3),
// so pushed disk images stay legible via the `RECORD` command. (Distinct from
// bdos.NameLen = 10, the narrower B-DOS file-name field.)
const recordNameLen = 16

// recordNameDefault is the safe fallback when sanitisation leaves no characters,
// so a claimed entry is never accidentally free (firstByte&0x7F == 0) or illegal.
const recordNameDefault = "pushed"

// ClaimRecordName derives the central-list name written for a claimed record from
// the WRQ filename. The Go authority for the Z80 bdos_build_claim_entry; both must
// agree byte-for-byte. THE WRQ FILENAME IS ATTACKER-CONTROLLABLE and the entry is
// written into a shared user resource (the Trinity SD card's record list), so the
// derivation is HARDENED:
//
//  1. Strip a leading "trinity-sam-disks/" prefix (the disk-record namespace).
//  2. Take the FINAL path segment (the leaf after the last '/'), collapsing any
//     directory traversal — "../../../etc/passwd" -> "passwd" — and ensuring '/'
//     is never stored.
//  3. Drop a dotted suffix ("disk.mgt" -> "disk").
//  4. Sanitise every byte to the legal printable record-name charset 0x20..0x7E;
//     anything outside it (high-bit, control 0x00..0x1F, DEL 0x7F) becomes '_'.
//     B-DOS prints names AND 127 per byte and renders masked bytes <0x21 as a
//     space-replacement (bdos15a.src.txt:4098-4115); 0x20..0x7E renders cleanly
//     and is bit-7-clear, so the first char can never set the write-protect bit.
//  5. Truncate to recordNameLen (16) — a hard bound; no input can overrun the slot.
//  6. If sanitisation produced nothing, substitute recordNameDefault so the entry
//     is a valid NAMED entry, never free or illegal.
func ClaimRecordName(filename string) string {
	_, name := bdos.Classify(filename)
	// Final path segment: collapse any directory part (and stray '/').
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	// Drop a dotted suffix.
	if i := strings.IndexByte(name, '.'); i >= 0 {
		name = name[:i]
	}
	// Sanitise every byte to the legal printable charset 0x20..0x7E.
	b := []byte(name)
	for i, c := range b {
		if c < 0x20 || c > 0x7E {
			b[i] = '_'
		}
	}
	name = string(b)
	// Truncate to the 16-char record-name field.
	if len(name) > recordNameLen {
		name = name[:recordNameLen]
	}
	// Empty-name guard: never write a free/illegal entry.
	if name == "" {
		name = recordNameDefault
	}
	return name
}

// WRQStaged returns the bytes accumulated by the active/last WRQ receive (the
// reference the host test compares the Z80 STAGING buffer against), and whether
// the transfer has completed (a short final block arrived).
func (r *Responder) WRQStaged() (data []byte, done bool) {
	if r.wrqRecv == nil {
		return nil, false
	}
	return r.wrqRecv.data, r.wrqRecv.done
}

// startWrite handles a WRQ (i121a handshake only). It learns the client endpoint
// from the received frame, then replies ACK-0 for a bare WRQ, or an OACK
// echoing the accepted blksize (and tsize the client declared) for an optioned
// WRQ (RFC 2347). DATA reception is deferred to i121b.
//
// Port of netboot_serve.asm handle_wrq (Z80 side).
func (r *Responder) startWrite(u frame.UDP, req *tftp.Request) []byte {
	// Learn the client endpoint from the request frame. HL/IP/TID learned here
	// are used only for the handshake reply; the wire-receive loop (i121b) will
	// use them to validate DATA source ports.
	copy(r.wrqClient.mac[:], u.SrcMAC[:])
	copy(r.wrqClient.ip[:], u.SrcIP[:])
	r.wrqClient.tid = u.SrcPort

	// Both storage classes (DiskRecord push, i121f; FlatFile HSAVE, i121c) claim a
	// free record per the placement strategy before the handshake. None available (no
	// free record, or the explicit record is already named) -> ERROR(3, "no free
	// record"), arm nothing (never touch a named record — the shared-resource
	// invariant). The class is picked from the filename in newWRQReceiver
	// (bdos.Classify, the "trinity-sam-disks/" prefix). Port of netboot_serve.asm
	// wrq_claim_record (which calls bdos_find_record_for_strategy).
	if r.cfg.DiskRecordPush && r.nextRecordForStrategy() == 0 {
		r.wrqRecv = nil
		return r.wrapToWRQClient(tftp.BuildError(3, "no free record"))
	}

	// Bare WRQ (no options) -> ACK-0 (`00 04 00 00`), RFC 1350. Arm the receiver
	// at the 512-byte default; the client's DATA block 1 follows the ACK-0.
	if len(req.Options) == 0 {
		r.wrqRecv = r.newWRQReceiver(512, req.Filename)
		return r.wrapToWRQClient(tftp.BuildACK(0))
	}

	// Optioned WRQ -> OACK echoing the accepted blksize; mirror blksize clamping
	// from AcceptedBlksize (same logic as the RRQ OACK path). If the client also
	// sent tsize, echo it back unchanged (the server learns the incoming size from
	// the client's declaration — it doesn't add its own).
	blksize := uint64(512)
	if v, ok := req.Option("blksize"); ok {
		if n, err := strconv.ParseUint(v, 10, 64); err == nil {
			blksize = tftp.AcceptedBlksize(n)
		}
	}
	// Arm the receiver at the negotiated blksize; the client's DATA block 1
	// follows its ACK of the OACK.
	r.wrqRecv = r.newWRQReceiver(int(blksize), req.Filename)
	var oackOpts []tftp.Option
	oackOpts = append(oackOpts, tftp.Option{Name: "blksize", Value: strconv.FormatUint(blksize, 10)})
	if ts, ok := req.Option("tsize"); ok {
		oackOpts = append(oackOpts, tftp.Option{Name: "tsize", Value: ts})
	}
	return r.wrapToWRQClient(tftp.BuildOACK(oackOpts))
}

// newWRQReceiver arms a WRQ receiver at blksize. When the server writes pushes to
// records (DiskRecordPush), the WRQ filename's storage class (bdos.Classify, the
// "trinity-sam-disks/" prefix discriminator) picks the path:
//   - DiskRecord ("trinity-sam-disks/X") -> a bdos.RawSink (body re-blocked into the
//     record's sectors, the Z80 raw_record_sink authority), validated on the final
//     block as an 819,200-byte disk record (finalizePush).
//   - FlatFile (any other name, the default class) -> flat receive-to-staging into
//     data, HSAVE'd into the claimed record on the final block (finalizeFlat, i121c).
// When DiskRecordPush is off this is the pure i121a/i121b flat receive-to-staging
// (no claim, no store) — the wire test path.
func (r *Responder) newWRQReceiver(blksize int, filename string) *wrqReceiver {
	rc := &wrqReceiver{blksize: blksize, filename: filename}
	if r.cfg.DiskRecordPush {
		class, internal := bdos.Classify(filename)
		if class == bdos.DiskRecord {
			rc.push = true
			rc.sink = bdos.NewRawSink()
		} else {
			rc.flat = true
			rc.flatName = internal
		}
	}
	return rc
}

// wrapToWRQClient wraps a TFTP payload as a UDP datagram back to the WRQ client
// (from the SAM's IP + transfer TID to the client's IP + TID).
func (r *Responder) wrapToWRQClient(payload []byte) []byte {
	return frame.BuildUDPFrame(frame.UDP{
		DstMAC:  r.wrqClient.mac,
		SrcMAC:  r.cfg.ServerMAC,
		SrcIP:   r.cfg.ServerIP,
		DstIP:   r.wrqClient.ip,
		SrcPort: r.cfg.ServerTID,
		DstPort: r.wrqClient.tid,
		Payload: payload,
	})
}
