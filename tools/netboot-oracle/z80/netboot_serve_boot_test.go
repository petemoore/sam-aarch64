// netboot_serve_boot_test.go — i124b: run the serve/server BOOT WRAPPERS
// (serve_main / netboot_main) end-to-end under the harness, closing the last
// netboot emulation-first carve-out.
//
// These wrappers were previously untested in emulation: they lived entirely
// behind `if defined(NETBOOT_HOSTTEST)==0` and ran only on real Trinity.
// Adding the mapfile outputs (Makefile) lets LoadBoot resolve their entry points
// and run them here — the same pattern used by smoke_main (TestSmokeBootRunsFromEEPROM)
// and client_main (TestClientBootReachesFirstTX) in netboot_boot_test.go.
//
// TestServeBootFromEEPROM — the serve-files demo server boot wrapper:
//
//	serve_main reads the "Trinity Network " EEPROM chunk (MAC+IP), fills CONFIG,
//	calls provision_demo (bakes hello.txt / readme.txt into STORE+SRC_TABLE), inits
//	the ENC28J60, then loops forever as a reactive server. The test injects an ARP
//	request and a bare TFTP RRQ for hello.txt; both replies must byte-match the Go
//	authorities (smoke.Responder for ARP, serve.Responder for the TFTP DATA frame).
//
// TestServerBootFromEEPROM — the integrated netboot server boot wrapper:
//
//	netboot_main reads the same EEPROM chunk, fills the full CONFIG block (MAC, IP,
//	DHCP pool, lease, TIDs), inits the ENC28J60, then loops forever. The test injects
//	an ARP request; the reply must byte-match smoke.NewResponder (the ARP sub-path is
//	exercised identically in TestSmokeBootRunsFromEEPROM and is the reactive minimum
//	that proves the boot wrapper ran through EEPROM read + drv_init successfully).
//
// Emulation-verified is not hardware-verified (CLAUDE.md §5). The NETBOOT_HOSTTEST
// guards are NOT deleted — they are correct build-variant switches; this PR adds
// emulation coverage, not a removal of the guards.
package z80_test

import (
	"bytes"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/frame"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/internal/mask"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/serve"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/smoke"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/tftp"
	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	serveBootBin  = "../../../build/netboot_serve_boot.bin"
	serveBootMap  = "../../../build/netboot_serve_boot.map"
	serverBootBin = "../../../build/netboot_server.bin"
	serverBootMap = "../../../build/netboot_server.map"

	// serveBootTID is the fixed transfer source TID that serve_main hard-codes
	// into CONFIG_SERVERTID (src/netboot/netboot_serve.asm serve_main; also the
	// value used by the HOSTTEST harness test in netboot_serve_test.go).
	serveBootTID = 40136

	// bootClientTID is an arbitrary client-side source port for boot test frames.
	bootClientTID = 30574
)

// serveBootHelloData is the exact content provision_demo bakes into hello.txt
// (src/netboot/netboot_serve.asm hello_data / hello_end). The Go authority must
// serve these same bytes so serve.Responder.OnFrame produces the identical frame.
var serveBootHelloData = []byte("Hello from a SAM Coupe over Trinity TFTP!\r\n")

// bootRRQ builds a bare TFTP RRQ frame (no options) sourced from mask.ClientMAC/IP
// to mask.ServerMAC/IP, targeting port 69 on the SAM.
func bootRRQ(name string) []byte {
	return frame.BuildUDPFrame(frame.UDP{
		DstMAC:  frame.MAC(mask.ServerMAC),
		SrcMAC:  frame.MAC(mask.ClientMAC),
		SrcIP:   frame.IPv4(mask.ClientIP),
		DstIP:   frame.IPv4(mask.ServerIP),
		SrcPort: bootClientTID,
		DstPort: 69,
		Payload: tftp.BuildRRQ(name, "octet", nil),
	})
}

// TestServeBootFromEEPROM runs the REAL bootable serve_main wrapper end-to-end:
// it reads the SAM's MAC+IP from the emulated EEPROM, provisions the baked-in
// demo files (hello.txt / readme.txt) into STORE+SRC_TABLE via provision_demo,
// inits the ENC28J60, then serves. Two injected frames exercise the two top-level
// reply paths: an ARP request -> an ARP reply, and a bare RRQ for hello.txt ->
// DATA block 1 directly (the no-options path defined in serve.Responder.OnFrame).
// Both replies must byte-match the corresponding Go authority frames, proving the
// boot wrapper correctly read the EEPROM identity and provisioned the demo store.
func TestServeBootFromEEPROM(t *testing.T) {
	// Flat Load (not LoadBoot): serve_boot ships the i145b CSD-read overlay, so its
	// tail runs above &C000 into section D — which is RAM at boot (LOAD CODE 32768
	// deposits the >&BFFF bytes; ROM1 off at run, proven by the section-D loadability
	// probe). LoadBoot's drop-the-tail-above-&C000 model would discard csd_set_bd_records
	// and crash serve_main's call into it. Flat all-RAM is the faithful runtime model
	// (the same reason http_main_boot_test uses flat Load).
	mac, err := z80h.Load(serveBootBin, serveBootMap)
	if err != nil {
		t.Fatalf("serve boot binary not built (%v); run `make netboot-serve-boot`", err)
	}
	enc := z80h.NewENC28J60()
	enc.ProgramTrinityNetwork(mask.ServerMAC, mask.ServerIP)
	mac.AttachIO(enc)

	// Build the Go authority with the same identity and the exact file content that
	// provision_demo assembles into the binary (hello_data in netboot_serve.asm).
	serveStore := tftp.MapStore{"hello.txt": uint64(len(serveBootHelloData))}
	serveCfg := serve.Config{
		ServerMAC: mask.ServerMAC,
		ServerIP:  mask.ServerIP,
		ServerTID: serveBootTID,
	}
	goAuth := serve.New(serveCfg, serveStore,
		func(_ string) tftp.Source { return tftp.ByteSource(serveBootHelloData) })

	// Pre-queue both frames: the serve_main loop processes them in order once
	// drv_init completes and the server enters its reactive loop.
	arpReq := frame.BuildARPRequest(frame.MAC(mask.ClientMAC), frame.IPv4(mask.ClientIP), frame.IPv4(mask.ServerIP))
	rrqReq := bootRRQ("hello.txt")
	enc.InjectRX(arpReq)
	enc.InjectRX(rrqReq)

	res, err := mac.RunBoot("serve_main", z80h.Entry{StepCap: bootStepCap})
	if err != nil {
		t.Fatalf("RunBoot serve_main: %v", err)
	}
	border, _ := enc.LastBorder()
	t.Logf("serve_main: halted=%v PC=&%04X steps=%d border=%d tx=%d",
		res.Halted, res.PC, res.Steps, border, len(enc.TXFrames()))

	// If halted the boot wrapper hit a fail path (sv_fail_cfg border=2 or
	// sv_fail_init border=1); the most likely cause is the EEPROM read failing
	// to find "Trinity Network " — a model bug, not an expected outcome.
	if res.Halted {
		t.Fatalf("serve_main halted at PC=&%04X border=%d — expected it to spin in the serve loop; "+
			"border=2 means EEPROM find_index/read_chunk failed (check ProgramTrinityNetwork), "+
			"border=1 means drv_init failed", res.PC, border)
	}

	tx := enc.TXFrames()
	if len(tx) < 2 {
		t.Fatalf("serve_main transmitted %d frame(s), want at least 2 (ARP reply + TFTP DATA 1); "+
			"border=%d — if border=2/1 serve_main halted early (not reached)", len(tx), border)
	}

	// Frame 0: the ARP reply — matches smoke.Responder (same authority as TestSmokeBootRunsFromEEPROM).
	wantARP := smoke.NewResponder(mask.ServerMAC, mask.ServerIP).OnFrame(arpReq)
	if wantARP == nil {
		t.Fatal("Go ARP responder ignored the ARP request (test bug)")
	}
	if !bytes.Equal(tx[0], wantARP) {
		t.Errorf("serve_main ARP reply != Go authority\n  z80 %x\n  go  %x", tx[0], wantARP)
	}

	// Frame 1: the TFTP DATA 1 reply for hello.txt (bare RRQ — no OACK, DATA directly).
	wantDATA := goAuth.OnFrame(rrqReq)
	if wantDATA == nil {
		t.Fatal("Go serve authority produced nil for the hello.txt RRQ (test bug — store not populated?)")
	}
	if !bytes.Equal(tx[1], wantDATA) {
		t.Errorf("serve_main TFTP DATA 1 (hello.txt) != Go authority\n  z80 %x\n  go  %x", tx[1], wantDATA)
	}
}

// TestServeBootComputesBDRecordsFromCSD is the i145b-b2 integration test: it runs
// the REAL bootable serve_main with an SD card whose CSD is configured, and asserts
// serve_main COMPUTED BD_RECORDS from that card (the un-gated csd_set_bd_records
// call shipped as a section-D overlay), not had it injected. Unlike
// csd_to_bd_records_test.go (which CALLs csd_set_bd_records by symbol against a
// standalone fixture), this drives the whole boot path — EEPROM read +
// provision_demo + csd_set_bd_records + drv_init — so it proves the un-gated read
// fits and runs in the real boot image. The card is ~3.7 GB; serve_main must store
// the same 16-bit record count the i145e-validated Go reference computes.
func TestServeBootComputesBDRecordsFromCSD(t *testing.T) {
	mac, err := z80h.Load(serveBootBin, serveBootMap)
	if err != nil {
		t.Fatalf("serve boot binary not built (%v); run `make netboot-serve-boot`", err)
	}
	enc := z80h.NewENC28J60()
	enc.ProgramTrinityNetwork(mask.ServerMAC, mask.ServerIP)
	const cSize = 0x001D59 // ~3.7 GB SDHC card (CSD v2.0)
	enc.AttachSD(csdV2(cSize))
	mac.AttachIO(enc)

	// Zero BD_RECORDS first so a non-zero result can only come from serve_main
	// computing it from the modelled card — never a stale/injected value.
	bdAddr := symAddr(t, mac, "BD_RECORDS")
	mac.WriteU16LE(bdAddr, 0)

	res, err := mac.RunBoot("serve_main", z80h.Entry{StepCap: bootStepCap})
	if err != nil {
		t.Fatalf("RunBoot serve_main: %v", err)
	}
	border, _ := enc.LastBorder()
	if res.Halted {
		t.Fatalf("serve_main halted at PC=&%04X border=%d — expected it to reach the serve loop "+
			"(border=2 EEPROM read failed, border=1 drv_init failed)", res.PC, border)
	}

	b := mac.Read(bdAddr, 2)
	got := uint16(b[0]) | uint16(b[1])<<8
	_, want := refRecords(refBlocksV2(cSize))
	if uint32(got) != want {
		t.Fatalf("serve_main computed BD_RECORDS=%d, want %d (from CSD C_SIZE=0x%X)", got, want, cSize)
	}
	if got == 0 {
		t.Fatalf("serve_main left BD_RECORDS=0 — the CSD read did not run in the boot path")
	}
	t.Logf("serve_main COMPUTED BD_RECORDS=%d from the modelled CSD (not injected)", got)
}

// TestServerBootFromEEPROM runs the REAL bootable netboot_main wrapper end-to-end:
// it reads the SAM's MAC+IP from the emulated EEPROM, fills the full CONFIG block
// (MAC, IP, DHCP pool, lease times, TID), inits the ENC28J60, then serves the
// integrated netboot session. The test injects a single ARP request and asserts
// the reply byte-matches smoke.NewResponder — the reactive minimum that proves the
// boot wrapper ran to completion through the EEPROM read and drv_init, the only
// code paths unique to the boot wrapper (the rest is shared with the HOSTTEST build,
// already proven in TestServerFullSession).
func TestServerBootFromEEPROM(t *testing.T) {
	mac, err := z80h.LoadBoot(serverBootBin, serverBootMap, romBaseBoot)
	if err != nil {
		t.Fatalf("server boot binary not built (%v); run `make netboot-server`", err)
	}
	enc := z80h.NewENC28J60()
	enc.ProgramTrinityNetwork(mask.ServerMAC, mask.ServerIP)
	mac.AttachIO(enc)

	arpReq := frame.BuildARPRequest(frame.MAC(mask.ClientMAC), frame.IPv4(mask.ClientIP), frame.IPv4(mask.ServerIP))
	enc.InjectRX(arpReq)

	res, err := mac.RunBoot("netboot_main", z80h.Entry{StepCap: bootStepCap})
	if err != nil {
		t.Fatalf("RunBoot netboot_main: %v", err)
	}
	border, _ := enc.LastBorder()
	t.Logf("netboot_main: halted=%v PC=&%04X steps=%d border=%d tx=%d",
		res.Halted, res.PC, res.Steps, border, len(enc.TXFrames()))

	// If halted: nb_fail_cfg (border=2, EEPROM read failed) or nb_fail_init (border=1,
	// drv_init failed). Neither is expected — the EEPROM is correctly programmed and
	// the ENC model accepts drv_init.
	if res.Halted {
		t.Fatalf("netboot_main halted at PC=&%04X border=%d — expected it to spin in the serve loop; "+
			"border=2 means EEPROM find_index/read_chunk failed, border=1 means drv_init failed", res.PC, border)
	}

	if len(enc.TXFrames()) == 0 {
		t.Fatalf("netboot_main transmitted nothing — the EEPROM read or drv_init failed silently (border=%d)", border)
	}

	// The first transmitted frame must be the ARP reply byte-matching the Go authority.
	wantARP := smoke.NewResponder(mask.ServerMAC, mask.ServerIP).OnFrame(arpReq)
	if wantARP == nil {
		t.Fatal("Go ARP responder ignored the ARP request (test bug)")
	}
	if !bytes.Equal(enc.TXFrames()[0], wantARP) {
		t.Errorf("netboot_main ARP reply != Go authority\n  z80 %x\n  go  %x", enc.TXFrames()[0], wantARP)
	}
}
