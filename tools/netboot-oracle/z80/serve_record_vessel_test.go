// serve_record_vessel_test.go — the i332 emulation gate: the CODE-auto record
// vessel (build/netboot_serve_record.mgt, `make netboot-serve-record`) must be
// bootable by boot_record from the pushed context on the captured B-DOS 1.5t.
//
// The BASIC-auto floppy vessel (netboot_serve.mgt) is NOT record-bootable:
// B-DOS's record boot (ALHK + the ROM1 load-continuation) runs the record's
// AUTO* file directly and never fires a BASIC RUN leg, so a BASIC-auto record
// NOP-slides into junk (the i319a-b2 silent livelock on real hardware). The
// record vessel therefore ships the serve binary as ONE auto-executing CODE
// file: exec = load &8000, config baked into the file bytes. The exec
// environment B-DOS hands over is the flat load window — HMPR = start page
// (section C/D = the file's two pages), ROM1 off, SP in low RAM — so the >16K
// binary runs exactly as the BASIC vessel's CALL 32768 would run it.
//
// Gated on the proprietary captures; skips only under SKIP_PRIVATE_TESTS (i253).
// Emulation-verified is not hardware-verified (CLAUDE.md §5) — the real-SAM
// shot is i319a-b2.
package z80_test

import (
	"bytes"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/frame"
	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const serveRecordMGT = "../../../build/netboot_serve_record.mgt"

func TestBootRecordServeRecordVessel(t *testing.T) {
	mac, lg, sd, enc := bootToEditorIdleSDENC(t)

	mgt := mustReadFile(t, serveRecordMGT, "make netboot-serve-record")
	seedRecordFromMGT(sd, 2, mgt, "serve")
	seedRecordList(sd, map[int]string{1: "rec1", 2: "serve"})

	if err := mac.LoadSymbols(serveBootMap); err != nil {
		t.Fatalf("load serve symbols: %v", err)
	}
	serveSym := func(name string) uint16 {
		a, err := mac.Sym(name)
		if err != nil {
			t.Fatalf("serve symbol %q absent from %s — rebuild with `make netboot-serve-boot`", name, serveBootMap)
		}
		return a
	}
	serveLoop := serveSym("sv_serve_loop")
	cfgIP := serveSym("CONFIG_SERVERIP")
	cfgMAC := serveSym("CONFIG_SERVERMAC")

	// Boot the record from the armed pushed-program state (the exact context
	// the real launcher's push creates; see TestBootRecordFromTrinloadIdle).
	enc.SettlePIC()
	const page = 1
	_, brMain := stageBootRecord(t, mac, 2)
	armServeDispatch(mac, page)
	mac.Pager().HMPR = page

	from := len(*lg)
	res, err := mac.ContinueFrom(brMain, z80h.Entry{
		StepCap: 200_000_000, FrameIntPeriod: 60000,
		StopPC: serveLoop,
	})
	if err != nil {
		t.Fatalf("boot_record -> serve record faulted: %v (PC=&%04X)", err, res.PC)
	}
	dir, body, out := classifyReads(cmd17Since(lg, from), 2)
	t.Logf("boot: finalPC=&%04X reachedStop=%v steps=%d record-2 reads dir=%d body=%d outside=%d",
		res.PC, res.ReachedStop, res.Steps, dir, body, out)
	if !res.ReachedStop {
		t.Fatalf("serve did not reach sv_serve_loop (&%04X): finalPC=&%04X halted=%v — the record vessel did not boot",
			serveLoop, res.PC, res.Halted)
	}
	if body == 0 {
		t.Error("no record-2 BODY sectors read — the serve binary cannot have been loaded from the card")
	}

	// The load must be COMPLETE through the file's final sector. The tail of
	// the >16K image (its second page) holds the csd_* routines and then
	// SERVE_CONFIG; compare the static code span [csd_blocks_to_records,
	// csd_blocks) — symbol-derived, past the 16K fold, and before the
	// runtime-mutated CSD variables and the strategy-dependent config bytes —
	// against the source binary. A short load (a lost final sector) leaves
	// this window unwritten and cannot pass.
	serveBin := mustReadFile(t, serveBootBin, "make netboot-serve-boot")
	tailLo := serveSym("csd_blocks_to_records")
	tailHi := serveSym("csd_blocks")
	if tailLo < 0xC000 || tailHi <= tailLo {
		t.Fatalf("tail window [&%04X,&%04X) no longer suits the completeness check — pick fresh symbols past the 16K fold", tailLo, tailHi)
	}
	loaded := mac.Pager().RAM[page+1][tailLo-0xC000 : tailHi-0xC000]
	want := serveBin[tailLo-0x8000 : tailHi-0x8000]
	if !bytes.Equal(loaded, want) {
		diff := 0
		for diff < len(want) && loaded[diff] == want[diff] {
			diff++
		}
		t.Errorf("loaded image tail differs from netboot_serve_boot.bin at &%04X (got &%02X want &%02X) — the record load did not deliver the file's final sectors",
			int(tailLo)+diff, loaded[diff], want[diff])
	}

	// Serve's adopted identity, read back from its CONFIG block (filled from
	// the captured EEPROM settings chunk during serve_main init). Both symbols
	// live below &C000, i.e. in the file's first page (section C = `page`).
	p1 := mac.Pager().RAM[page]
	var serveIP frame.IPv4
	var serveMAC frame.MAC
	copy(serveIP[:], p1[cfgIP-0x8000:])
	copy(serveMAC[:], p1[cfgMAC-0x8000:])
	t.Logf("serve identity: MAC=% X IP=%d.%d.%d.%d", serveMAC[:], serveIP[0], serveIP[1], serveIP[2], serveIP[3])

	// Liveness: an ARP request for serve's IP must be answered with an ARP
	// reply carrying its MAC — the same first-contact probe a LAN host makes
	// before `tftp <sam-ip>`.
	hostMAC := frame.MAC{0x02, 0x00, 0x00, 0x00, 0x00, 0x99}
	hostIP := frame.IPv4{192, 168, 2, 1}
	txBefore := len(enc.TXFrames())
	enc.InjectRX(frame.BuildARPRequest(hostMAC, hostIP, serveIP))
	if _, err := mac.ContinueFrom(res.PC, z80h.Entry{StepCap: 8_000_000, FrameIntPeriod: 60000}); err != nil {
		t.Fatalf("serve loop faulted after ARP inject: %v", err)
	}
	replies := 0
	for _, f := range enc.TXFrames()[txBefore:] {
		if mac, ip, ok := frame.ParseARPReply(f); ok {
			replies++
			if ip != serveIP {
				t.Errorf("ARP reply sender IP = %v, want %v", ip, serveIP)
			}
			if mac != serveMAC {
				t.Errorf("ARP reply sender MAC = % X, want % X", mac[:], serveMAC[:])
			}
		}
	}
	if replies != 1 {
		t.Fatalf("%d ARP replies to the probe, want 1 — the booted serve is not live on the wire", replies)
	}
	t.Logf("serve answered ARP — the record vessel boots and serves")
}
