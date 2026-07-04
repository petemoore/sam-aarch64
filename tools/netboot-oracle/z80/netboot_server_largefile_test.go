// netboot_server_largefile_test.go — the i365c emulation gate: the integrated
// netboot server must serve a file LARGER than the RAM arena (NB_FILE_MAX =
// 1518) by STREAMING it from the boot record's SD sectors on demand, byte-exact
// against the file's payload.
//
// It boots the netboot_server_largefile_record vessel (`make
// netboot-server-largefile-record`) on the captured real ROM + B-DOS 1.5t: the
// record carries the AUTOnbsrv server binary + a distinctive 40000-byte ramp
// (multi-page, > 16 KB) as a plain CODE file + a small config.txt. The boot-time
// store walk indexes the ramp as disk-backed (NB_DISK_TABLE) and config.txt into
// the RAM arena; the boot infrastructure (bdos, AUTOnbsrv) stays out of both
// tables (the "plain" filter). A TFTP RRQ for the ramp at blksize 512 AND 1024
// (both re-block the 510-byte MGT data units across DATA-block boundaries) must
// reassemble to the ramp's payload byte-for-byte, matching the Go authority
// (tftp.ServerLoop); config.txt still serves from the arena (the regression);
// and the whole session issues ZERO SD writes (serve is read-only).
//
// This is the rule-7 gate in front of the i365c hardware serve: it proves the
// one path the flat harness cannot — the disk-backed descriptor + MGT-chain
// follow (track-major .mgt vs side-major record) + the 32-bit re-blocking
// send_next_data, all against the real record geometry.
//
// Gated on the proprietary captures; skips only under SKIP_PRIVATE_TESTS (i253).
// Emulation-verified is not hardware-verified (CLAUDE.md §5).
package z80_test

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/frame"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/internal/mask"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/tftp"
	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	largeFileRecordMGT = "../../../build/netboot_server_largefile_record.mgt"
	largeRampPath      = "../../../build/largefile_ramp.bin"
)

// mgtSectorOffset returns the byte offset of MGT (track_byte, sector) in a
// track-major .mgt image (80 cyl x 2 sides x 10 sectors x 512): the side rides
// bit 7 of the track byte, the cylinder bits 0-6, the sector is 1-based.
func mgtSectorOffset(trackByte, sector int) int {
	side := (trackByte & 0x80) >> 7
	cyl := trackByte & 0x7F
	return cyl*10240 + side*5120 + (sector-1)*512
}

// dirEntryFirstTS scans the .mgt directory (tracks 0-3 side 0, two 256-byte
// entries per sector) for a 10-char space-padded name and returns its body
// chain head [track_byte, sector] from directory-entry offset 0x0D..0x0E.
func dirEntryFirstTS(t *testing.T, mgt []byte, name string) (int, int) {
	t.Helper()
	field := name + "          "[:10-len(name)]
	for slot := 0; slot < 80; slot++ {
		d := slot / 2
		off := (d/10)*10240 + (d%10)*512 + (slot%2)*256
		e := mgt[off : off+256]
		if e[0] == 0 {
			continue
		}
		if string(e[1:11]) == field {
			return int(e[0x0D]), int(e[0x0E])
		}
	}
	t.Fatalf("directory entry %q not found in the vessel", name)
	return 0, 0
}

// patchMGTPayloadByte sets PAYLOAD byte `off` (0-based, past the 9-byte body
// header) of the file whose body chain starts at (track, sector), following the
// MGT [next-track, next-sector] links at +510/+511 of each sector.
func patchMGTPayloadByte(t *testing.T, mgt []byte, track, sector, off int, val byte) {
	t.Helper()
	bodyOff := off + 9
	base := 0
	for {
		soff := mgtSectorOffset(track, sector)
		if bodyOff < base+510 {
			mgt[soff+(bodyOff-base)] = val
			return
		}
		nt, ns := int(mgt[soff+510]), int(mgt[soff+511])
		if nt == 0 && ns == 0 {
			t.Fatalf("payload offset %d runs past the MGT chain end", off)
		}
		track, sector = nt, ns
		base += 510
	}
}

// parseDiskTable decodes NB_DISK_TABLE: name\0 + [track,sector] (2) + 4-byte LE
// size, terminated by a single leading NUL.
func parseDiskTable(t *testing.T, raw []byte) map[string]struct {
	track, sector byte
	size          int
} {
	t.Helper()
	out := map[string]struct {
		track, sector byte
		size          int
	}{}
	i := 0
	for i < len(raw) && raw[i] != 0 {
		j := bytes.IndexByte(raw[i:], 0)
		if j < 0 || i+j+7 > len(raw) {
			t.Fatalf("NB_DISK_TABLE truncated at offset %d: % X", i, raw[i:])
		}
		out[string(raw[i:i+j])] = struct {
			track, sector byte
			size          int
		}{raw[i+j+1], raw[i+j+2], int(binary.LittleEndian.Uint32(raw[i+j+3 : i+j+7]))}
		i += j + 7
	}
	return out
}

func TestNetbootServerLargeFile(t *testing.T) {
	mac, _, sd, enc := bootToEditorIdleSDENC(t)

	mgt := mustReadFile(t, largeFileRecordMGT, "make netboot-server-largefile-record")
	ramp := mustReadFile(t, largeRampPath, "make netboot-server-largefile-record")
	configTxt := mustReadFile(t, standinConfigPath, "restore tools/netboot-oracle/testdata/pi-standins")

	// Resolve the server's landmarks (and NB_BOOT_RECORD's offset) BEFORE
	// stageBootRecord loads the boot_record map over the symbol table.
	if err := mac.LoadSymbols(serverBootMap); err != nil {
		t.Fatalf("load server symbols: %v — rebuild with `make netboot-server`", err)
	}
	srvSym := func(name string) uint16 {
		a, err := mac.Sym(name)
		if err != nil {
			t.Fatalf("server symbol %q absent from %s — rebuild with `make netboot-server`", name, serverBootMap)
		}
		return a
	}
	serveLoop := srvSym("nb_serve_loop")
	cfgMAC := srvSym("CONFIG_SERVERMAC")
	cfgIP := srvSym("CONFIG_SERVERIP")
	storeAddr := srvSym("STORE")
	diskAddr := srvSym("NB_DISK_TABLE")
	bootRecOff := int(srvSym("NB_BOOT_RECORD")) - 0x8000

	// Patch NB_BOOT_RECORD in the AUTOnbsrv body so the booted server reads the
	// record it booted from (the real launcher patches this; the i365c test does
	// it directly). Patch the .mgt BEFORE seeding — seedRecordFromMGT copies it.
	const bootRecord = 2
	at, as := dirEntryFirstTS(t, mgt, "AUTOnbsrv")
	patchMGTPayloadByte(t, mgt, at, as, bootRecOff, bootRecord)

	seedRecordFromMGT(sd, bootRecord, mgt, "nbsrv")
	seedRecordList(sd, map[int]string{1: "rec1", 2: "nbsrv"})

	// --- boot the record to nb_serve_loop (the full real come-up). ---
	const page = 1
	_, brMain := stageBootRecord(t, mac, bootRecord)
	armServeDispatch(mac, page)
	mac.Pager().HMPR = page
	res, err := mac.ContinueFrom(brMain, z80h.Entry{
		StepCap: 400_000_000, FrameIntPeriod: 60000, StopPC: serveLoop,
	})
	if err != nil {
		t.Fatalf("boot_record -> netboot server faulted: %v (PC=&%04X)", err, res.PC)
	}
	if !res.ReachedStop {
		t.Fatalf("server did not reach nb_serve_loop (&%04X): finalPC=&%04X halted=%v — the come-up failed",
			serveLoop, res.PC, res.Halted)
	}

	// --- the walk indexed the ramp as disk-backed + config.txt into the arena;
	// the boot infrastructure (bdos, AUTOnbsrv) is in NEITHER table. ---
	gotStore := parseWalkStore(t, mac.Read(storeAddr, 1024))
	wantStore := map[string]int{"config.txt": len(configTxt), "ramp.bin": len(ramp)}
	if len(gotStore) != len(wantStore) {
		t.Fatalf("store walk indexed %v, want %v (the plain-file filter should exclude bdos + AUTOnbsrv)", gotStore, wantStore)
	}
	for name, size := range wantStore {
		if gotStore[name] != size {
			t.Fatalf("STORE %s = %d, want %d (all: %v)", name, gotStore[name], size, gotStore)
		}
	}
	gotDisk := parseDiskTable(t, mac.Read(diskAddr, 256))
	e, ok := gotDisk["ramp.bin"]
	if !ok {
		t.Fatalf("ramp.bin absent from NB_DISK_TABLE (%v)", gotDisk)
	}
	if e.size != len(ramp) {
		t.Fatalf("NB_DISK_TABLE ramp.bin size = %d, want %d", e.size, len(ramp))
	}
	rt, rs := dirEntryFirstTS(t, mgt, "ramp.bin")
	if int(e.track) != rt || int(e.sector) != rs {
		t.Fatalf("NB_DISK_TABLE ramp.bin chain head = (%d,%d), want (%d,%d)", e.track, e.sector, rt, rs)
	}
	if _, isDisk := gotDisk["config.txt"]; isDisk {
		t.Errorf("config.txt is disk-backed, want arena-backed (small file)")
	}
	t.Logf("walk: STORE=%v  NB_DISK_TABLE ramp.bin=(track %d,sector %d,%d bytes)", gotStore, e.track, e.sector, e.size)

	// --- the SAM identity the server adopted from the captured EEPROM. ---
	var samMAC [6]byte
	var samIP [4]byte
	copy(samMAC[:], mac.Read(cfgMAC, 6))
	copy(samIP[:], mac.Read(cfgIP, 4))

	// drive resumes the serve loop for exactly ONE serve_once (StopPC at the loop
	// top, skip the initial arrival) and returns the single frame the server
	// transmitted (nil = silence). Continue, NOT ContinueFrom (the faithful rig's
	// stack-safety note in netboot_server_faithful_test.go).
	drive := func(label string, req []byte) []byte {
		t.Helper()
		txBefore := len(enc.TXFrames())
		if req != nil {
			enc.InjectRX(req)
		}
		r, err := mac.Continue(z80h.Entry{
			StepCap: 40_000_000, FrameIntPeriod: 60000, StopPC: serveLoop, StopPCSkip: 1,
		})
		if err != nil {
			t.Fatalf("%s: serve loop faulted: %v (PC=&%04X)", label, err, r.PC)
		}
		if !r.ReachedStop {
			t.Fatalf("%s: serve loop did not return to nb_serve_loop (PC=&%04X halted=%v)", label, r.PC, r.Halted)
		}
		tx := enc.TXFrames()[txBefore:]
		if len(tx) == 0 {
			return nil
		}
		if len(tx) > 1 {
			t.Fatalf("%s: server transmitted %d frames, want at most 1", label, len(tx))
		}
		return tx[0]
	}
	eq := func(label string, got, want []byte) {
		t.Helper()
		if got == nil {
			t.Fatalf("%s: server sent nothing", label)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("%s != Go authority\n  z80 %x\n  go  %x", label, got, want)
		}
	}

	rrqFor := func(name string, tid uint16, blksize int) []byte {
		return frame.BuildUDPFrame(frame.UDP{
			DstMAC: frame.MAC(samMAC), SrcMAC: frame.MAC(mask.ClientMAC),
			SrcIP: frame.IPv4(mask.ClientIP), DstIP: frame.IPv4(samIP),
			SrcPort: tid, DstPort: 69,
			Payload: tftp.BuildRRQ(name, "octet", []tftp.Option{
				{Name: "tsize", Value: "0"},
				{Name: "blksize", Value: itoa(blksize)},
			}),
		})
	}
	ackFrom := func(tid, block uint16) []byte {
		return frame.BuildUDPFrame(frame.UDP{
			DstMAC: frame.MAC(samMAC), SrcMAC: frame.MAC(mask.ClientMAC),
			SrcIP: frame.IPv4(mask.ClientIP), DstIP: frame.IPv4(samIP),
			SrcPort: tid, DstPort: nbServerTID,
			Payload: tftp.BuildACK(block),
		})
	}

	// serveWhole drives RRQ -> OACK -> the full DATA/ACK cadence, asserting every
	// frame byte-matches the Go authority, and returns the reassembled payload.
	serveWhole := func(name string, want []byte, tid uint16, blksize int) []byte {
		ref := tftp.NewServerLoop(tftp.MapStore{name: uint64(len(want))}, samMAC, samIP, nbServerTID)
		ref.SetSource(tftp.ByteSource(want))
		rrq := rrqFor(name, tid, blksize)
		eq(name+" OACK", drive(name+" OACK", rrq), ref.OnRRQ(rrq))

		var got []byte
		d := drive(name+" DATA 1", ackFrom(tid, 0))
		eq(name+" DATA 1", d, ref.FirstData())
		_, p, _ := tftp.ParseDATA(udpPayload(t, d))
		got = append(got, p...)
		for blk := uint16(1); len(p) == blksize; blk++ {
			a := ackFrom(tid, blk)
			d = drive(name+" DATA", a)
			eq(name+" DATA", d, ref.OnACK(a))
			_, p, _ = tftp.ParseDATA(udpPayload(t, d))
			got = append(got, p...)
		}
		// The short final block (len < blksize) has been sent; ACK it -> the
		// transfer ends (silence). Its block number = full-blocks + 1.
		lastBlk := uint16(len(got)/blksize + 1)
		final := ackFrom(tid, lastBlk)
		if fin := drive(name+" final ACK", final); fin != nil {
			t.Errorf("%s: ACK of the final block should end the transfer, got %x", name, fin)
		}
		_ = ref.OnACK(final)
		return got
	}

	// --- the large file streams byte-exact at both blksizes. 512 and 1024 both
	// re-block the 510-byte MGT data units across DATA-block boundaries; 1024
	// spans up to 3 MGT sectors per block. ---
	for _, blksize := range []int{512, 1024} {
		got := serveWhole("ramp.bin", ramp, uint16(35000+blksize), blksize)
		if !bytes.Equal(got, ramp) {
			diff := 0
			for diff < len(got) && diff < len(ramp) && got[diff] == ramp[diff] {
				diff++
			}
			var g, w byte
			if diff < len(got) {
				g = got[diff]
			}
			if diff < len(ramp) {
				w = ramp[diff]
			}
			t.Fatalf("blksize %d: reassembled ramp (%d bytes) != payload (%d bytes); first diff at %#x got &%02X want &%02X",
				blksize, len(got), len(ramp), diff, g, w)
		}
		t.Logf("blksize %d: ramp.bin streamed from disk byte-exact (%d bytes)", blksize, len(got))
	}

	// --- regression: a small file on the SAME record STILL serves from the RAM
	// arena (XFER_DISK stays 0), byte-exact. ---
	gotC := serveWhole("config.txt", configTxt, 34000, 1024)
	if !bytes.Equal(gotC, configTxt) {
		t.Fatalf("config.txt arena serve (%d bytes) != fixture (%d bytes)", len(gotC), len(configTxt))
	}
	t.Logf("config.txt served from the arena byte-exact (%d bytes)", len(gotC))

	// --- serve is READ-ONLY: the SD model saw ZERO writes across the session. ---
	if w := sd.WrittenSectors(); len(w) != 0 {
		t.Fatalf("READ-ONLY VIOLATION: the serve issued %d SD write(s) at %v", len(w), w)
	}
}

// itoa is a tiny base-10 helper (avoids importing strconv for one call).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [6]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
