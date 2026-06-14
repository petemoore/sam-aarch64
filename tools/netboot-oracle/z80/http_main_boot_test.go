// http_main_boot_test.go — i128: run the http_main BOOT WRAPPER end-to-end under
// the harness, verifying the proactive-TX gate added by i128.
//
// http_main (src/netboot/http_main.asm) is the SAM-side multi-file firmware-fetch
// orchestrator (the Z80 port of the Go Provisioner in tools/netboot-oracle/http/
// provision.go). Its bootable build (NETBOOT_STREAM, no NETBOOT_HOSTTEST) reads the
// SAM's MAC+IP from the Trinity EEPROM, initialises the ENC28J60, and then calls
// prov_first, which immediately broadcasts an ARP request — making http_main a
// PROACTIVE transmitter, exactly like the TFTP client.
//
// Before i128, http_main called drv_init then jumped straight to prov_first with
// no PHY link-up wait. On real hardware, drv_init does not wait for the 10BASE-T
// link-pulse detection to complete; a transmit issued before the link is up is
// silently dropped (the ENC28J60 clears TXRTS/sets TXIF but the bytes never
// egress). The TFTP client had this same bug, root-caused 2026-06-18 via the i124
// boot-path emulation; confirmed and fixed on hardware as i127 (PR #424). The
// i128 fix mirrors that fix into http_main: call drv_wait_link after drv_init and
// before prov_first, with a yellow-border (6) fail path on timeout.
//
// WHY FLAT Load NOT LoadBoot:
// http_main is a SECTION-D overlay program — it is loaded at &8000 and runs entirely
// within the &8000–&FFFF window that is RAM (not ROM1) in http_main's runtime
// context (the Trinity firmware loader has already paged RAM into section D before
// handing off). LoadBoot models the INITIAL boot-time memory map (ROM1 read-only
// at &C000), which is correct for the client/smoke boot wrappers (they run under
// the SAM's own bootloader which has ROM1 there), but would incorrectly drop the
// http_main binary's upper half at assembly time. The flat Load (all-RAM) faithfully
// models http_main's post-load runtime state. Section-D LOADABILITY on real hardware
// (how and when RAM is paged into section D) is a separate concern tracked by i70
// and i126; these tests assert only the program LOGIC (EEPROM → drv_init → link wait
// → prov_first ARP), not the loading mechanism.
//
// Emulation-verified is not hardware-verified (CLAUDE.md §5).
package z80_test

import (
	"bytes"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/frame"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/internal/mask"
	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	httpBootBin     = "../../../build/netboot_http_boot.bin"
	httpBootMap     = "../../../build/netboot_http_boot.map"
	httpBootStepCap = 2_000_000
)

// httpBootServerIP is the fixed firmware-fetch target http_main ARPs for
// (src/netboot/http_main.asm ht_server_ip). The SAM's own identity (the sender)
// is the EEPROM-read mask.ServerMAC / mask.ServerIP.
var httpBootServerIP = frame.IPv4{192, 168, 0, 1}

// TestHTTPMainBootReachesFirstTX runs the REAL bootable http_main end-to-end: it
// reads the SAM's MAC+IP from the emulated EEPROM, inits the ENC28J60, waits for
// the PHY link (the i128 fix), then prov_first broadcasts the ARP request for the
// firmware-fetch server. No RX is queued, so http_main spins in the provisioning
// loop awaiting the ARP reply — it must NOT halt on a fail border.
//
// This is the baseline / control: it asserts that the i128-fixed http_main reaches
// its first TX (prov_first's ARP) and that the ARP is well-formed, broadcast, and
// sourced from the EEPROM-read SAM identity. It also proves the flat Load model is
// correct: http_main's section-D binary runs under all-RAM to the ARP egress point.
func TestHTTPMainBootReachesFirstTX(t *testing.T) {
	mac, err := z80h.Load(httpBootBin, httpBootMap)
	if err != nil {
		t.Skipf("http boot binary not built (%v); run `make netboot-http-boot`", err)
	}
	enc := z80h.NewENC28J60()
	enc.ProgramTrinityNetwork(mask.ServerMAC, mask.ServerIP)
	mac.AttachIO(enc)

	// No RX queued: after prov_first's ARP egresses, the provisioning loop spins
	// awaiting a reply that never comes, so RunBoot returns Halted=false with PC
	// somewhere inside the fetch loop.
	res, err := mac.RunBoot("http_main", z80h.Entry{StepCap: httpBootStepCap})
	if err != nil {
		t.Fatalf("RunBoot http_main: %v", err)
	}
	border, hadBorder := enc.LastBorder()
	t.Logf("http_main: halted=%v PC=&%04X steps=%d border=%d(written=%v) tx=%d",
		res.Halted, res.PC, res.Steps, border, hadBorder, len(enc.TXFrames()))

	if len(enc.TXFrames()) != 1 {
		t.Fatalf("http_main put %d frames on the wire, want exactly 1 (the ARP request). "+
			"halted=%v PC=&%04X — a fail border (2=cfg,1=init,6=link) or 0 frames means a bug EARLIER "+
			"than prov_first (the EEPROM read / drv_init / drv_wait_link), not a normal spin",
			len(enc.TXFrames()), res.Halted, res.PC)
	}
	arp, ok := frame.ParseARPRequest(enc.TXFrames()[0])
	if !ok {
		t.Fatalf("http_main's first frame is not a well-formed ARP request: %x", enc.TXFrames()[0])
	}
	// Broadcast destination, and sourced from the EEPROM-read SAM identity.
	if !bytes.Equal(enc.TXFrames()[0][frame.OffDstMAC:frame.OffDstMAC+6], frame.BroadcastMAC[:]) {
		t.Errorf("ARP request dst = %x, want broadcast %x",
			enc.TXFrames()[0][frame.OffDstMAC:frame.OffDstMAC+6], frame.BroadcastMAC[:])
	}
	if arp.SenderMAC != mask.ServerMAC {
		t.Errorf("ARP sender MAC = %x, want the EEPROM MAC %x (the config read is wrong)", arp.SenderMAC, mask.ServerMAC)
	}
	if arp.SenderIP != mask.ServerIP {
		t.Errorf("ARP sender IP = %v, want the EEPROM IP %v (the config read is wrong)", arp.SenderIP, mask.ServerIP)
	}
	if arp.TargetIP != httpBootServerIP {
		t.Errorf("ARP asks for %v, want the firmware server IP %v", arp.TargetIP, httpBootServerIP)
	}
	// http_main must still be running (spinning awaiting the ARP reply), not halted
	// on a fail border. A halt here means the link wait timed out or init failed.
	if res.Halted {
		t.Errorf("http_main halted (PC=&%04X border=%d); expected it to spin awaiting the ARP reply", res.PC, border)
	}
}

// TestHTTPMainRecoversFromLinkDownStart is the i128 regression guard: the REAL
// fixed http_main, booted with the PHY link modelled DOWN at start, still puts
// exactly its one ARP request on the wire — because drv_wait_link blocks until the
// link comes up before the proactive prov_first send. With the emulator now dropping
// a transmit issued while the link is down (the silent wire, asserted in
// TestDrvWriteSilentWireWhileLinkDown), this is a genuine regression guard for the
// i128 fix: were drv_wait_link removed from http_main, prov_first would clock the
// ARP out while the link is still down, the frame would be dropped, and this test
// would see 0 frames.
//
// The guard is self-calibrating (no magic op-count): a baseline run with the link
// up from the start records FirstTXOps — the exact op-count at which http_main
// puts the ARP on the wire, i.e. the moment an un-fixed http_main would transmit.
// The guarded run then models the link as still down at that op-count (threshold
// FirstTXOps + a small margin), so an un-fixed http_main would necessarily transmit
// into a down link and lose the frame; the fixed http_main's drv_wait_link spins
// past that point and the ARP still egresses, byte-identical to the baseline.
//
// Link-up *timing* stays hardware-gated (CLAUDE.md §5); this asserts the code's
// wait-then-send logic against the hardware-confirmed silent-wire model (i131).
// Mirrors TestClientBootRecoversFromLinkDownStart from netboot_boot_test.go.
func TestHTTPMainRecoversFromLinkDownStart(t *testing.T) {
	run := func(linkUpAfterOps int) (z80h.CallResult, *z80h.ENC28J60) {
		mac, err := z80h.Load(httpBootBin, httpBootMap)
		if err != nil {
			t.Skipf("http boot binary not built (%v); run `make netboot-http-boot`", err)
		}
		enc := z80h.NewENC28J60()
		enc.ProgramTrinityNetwork(mask.ServerMAC, mask.ServerIP)
		enc.SetLinkUpAfterOps(linkUpAfterOps)
		mac.AttachIO(enc)
		// No RX queued: after the ARP egresses, the provisioning loop spins
		// awaiting a reply that never comes, so RunBoot stops at the step cap.
		res, err := mac.RunBoot("http_main", z80h.Entry{StepCap: httpBootStepCap})
		if err != nil {
			t.Fatalf("RunBoot http_main (linkUpAfterOps=%d): %v", linkUpAfterOps, err)
		}
		return res, enc
	}

	// Baseline: link up immediately. Exactly one ARP egresses; FirstTXOps records
	// the op-count at which it did — the point an un-fixed http_main would transmit.
	_, immENC := run(0)
	if n := len(immENC.TXFrames()); n != 1 {
		t.Fatalf("link up immediately: http_main put %d frames on the wire, want 1 (the ARP)", n)
	}
	unfixedTXOps := immENC.FirstTXOps()
	if unfixedTXOps <= 0 {
		t.Fatalf("baseline FirstTXOps = %d, want > 0 (no frame egressed?)", unfixedTXOps)
	}

	// Guarded run: model the link as still DOWN at unfixedTXOps (+ a margin to
	// cover the few ops http_main spends between drv_wait_link returning and the
	// actual send), coming up just after. An un-fixed http_main transmits at
	// ~unfixedTXOps < threshold → into a down link → dropped (0 frames). The fixed
	// http_main waits past the threshold, so its ARP still reaches the wire.
	const margin = 200
	threshold := unfixedTXOps + margin
	_, dlyENC := run(threshold)
	tx := dlyENC.TXFrames()
	if len(tx) != 1 {
		border, _ := dlyENC.LastBorder()
		t.Fatalf("link down at start (up after %d ops): http_main put %d frames on the wire, want exactly 1 — "+
			"0 means the ARP was dropped (drv_wait_link missing/broken: the i128 fix regressed); border=%d",
			threshold, len(tx), border)
	}
	if !bytes.Equal(tx[0], immENC.TXFrames()[0]) {
		t.Errorf("ARP after waiting for link != ARP with link up immediately\n  delayed %x\n  imm     %x", tx[0], immENC.TXFrames()[0])
	}
	// The ARP must have egressed only after the link came up — i.e. the fixed
	// http_main genuinely waited past the point an un-fixed one would have sent.
	if got := dlyENC.FirstTXOps(); got < threshold {
		t.Fatalf("guarded run egressed the ARP at op %d, before the link-up threshold %d — "+
			"prov_first did not wait for link (the i128 fix is not in effect)", got, threshold)
	}
	t.Logf("http_main recovers: un-fixed TX would land at op %d; fixed http_main (link up after %d ops) sent its ARP at op %d — both exactly 1 ARP, byte-identical",
		unfixedTXOps, threshold, dlyENC.FirstTXOps())
}
