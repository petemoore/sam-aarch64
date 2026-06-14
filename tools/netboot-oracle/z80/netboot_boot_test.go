// netboot_boot_test.go — i124: run the netboot BOOT WRAPPERS (smoke_main /
// client_main), not just the leaf routines, end-to-end under the harness.
//
// These entry points were previously carved out behind `ifndef NETBOOT_HOSTTEST`
// and only ever ran on real Trinity — the exact emulate-before-hardware bypass
// CLAUDE.md rule 7 forbids, and how the i82 client shipped to hardware with an
// init-path bug no test could catch. With the EEPROM modelled (eeprom.go) and the
// boot memory map applied (LoadBoot: ROM1 read-only at &C000), the real boot
// wrappers run here.
//
// smoke_main is the CONTROL: it reads the same "Trinity Network " EEPROM chunk and
// works on real hardware (i94 first-light), so a faithful emulation must reproduce
// its success (it answers an injected ARP for the SAM's IP, byte-for-byte the Go
// authority). client_main is the SUBJECT: it hangs on real hardware before any
// frame reaches the wire. Running it here shows its LOGIC is sound (it reaches
// client_first and emits the ARP request), which localises the hardware fault to
// the real-silicon layer the model idealises — see i124 for the diagnosis.
//
// Emulation-verified is not hardware-verified (CLAUDE.md §5).
package z80_test

import (
	"bytes"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/frame"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/internal/mask"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/smoke"
	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	smokeBootBin = "../../../build/netboot_smoke_boot.bin"
	smokeBootMap = "../../../build/netboot_smoke_boot.map"
	cliBootBin   = "../../../build/netboot_client_boot.bin"
	cliBootMap   = "../../../build/netboot_client_boot.map"

	// romBaseBoot is the first ROM1 address at boot (section D); the SAM cannot
	// write a loaded image above &BFFF until it pages RAM in.
	romBaseBoot = 0xC000

	// bootStepCap bounds a boot-wrapper run. The wrappers either halt (a fail/
	// success border) or loop forever (a serve loop / a livelock); the cap ends
	// the forever case so the test can inspect the wire + the spin PC.
	bootStepCap = 2_000_000
)

// cliBootServerIP is the fixed TFTP-server address the bootable client_main
// resolves via ARP (src/netboot/netboot_client.asm cl_server_ip).
var cliBootServerIP = frame.IPv4{192, 168, 0, 1}

// TestSmokeBootRunsFromEEPROM is the CONTROL: the real bootable smoke_main reads
// the SAM's MAC+IP from the emulated EEPROM, runs drv_init, and answers an ARP
// request for the SAM's IP with a reply byte-for-byte the Go authority's —
// proving the EEPROM model + boot path are faithful to the hardware-working smoke
// test (i94). If this passes, the emulation is trustworthy for the client subject.
func TestSmokeBootRunsFromEEPROM(t *testing.T) {
	mac, err := z80h.LoadBoot(smokeBootBin, smokeBootMap, romBaseBoot)
	if err != nil {
		t.Skipf("smoke boot binary not built (%v); run `make netboot-smoke-boot`", err)
	}
	enc := z80h.NewENC28J60()
	enc.ProgramTrinityNetwork(mask.ServerMAC, mask.ServerIP)
	mac.AttachIO(enc)

	// Queue an ARP request for the SAM's IP; smoke_main loops forever answering it.
	req := frame.BuildARPRequest(mask.ClientMAC, mask.ClientIP, mask.ServerIP)
	enc.InjectRX(req)

	res, err := mac.RunBoot("smoke_main", z80h.Entry{StepCap: bootStepCap})
	if err != nil {
		t.Fatalf("RunBoot smoke_main: %v", err)
	}
	border, _ := enc.LastBorder()
	t.Logf("smoke_main: halted=%v PC=&%04X steps=%d border=%d tx=%d",
		res.Halted, res.PC, res.Steps, border, len(enc.TXFrames()))

	// The proof the whole boot path ran is the ARP reply on the wire (it loops
	// forever, so it never halts).
	if len(enc.TXFrames()) == 0 {
		t.Fatalf("smoke_main transmitted nothing — the EEPROM read (the boot wrapper's first action) or drv_init failed in emulation (border=%d)", border)
	}
	got := enc.TXFrames()[0]
	want := smoke.NewResponder(mask.ServerMAC, mask.ServerIP).OnFrame(req)
	if want == nil {
		t.Fatal("Go responder ignored the ARP request (test bug)")
	}
	if !bytes.Equal(got, want) {
		t.Errorf("smoke_main reply != Go authority\n  z80 %x\n  go  %x", got, want)
	}
}

// TestClientBootReachesFirstTX runs the REAL bootable client_main end-to-end: it
// reads the SAM's MAC+IP from the emulated EEPROM, runs drv_init, and — the crux —
// client_first broadcasts the ARP request. On real hardware this transmits
// nothing (the board sits with a flickering border and the wire stays silent).
//
// This asserts client_main's LOGIC is sound in emulation: it reaches client_first
// and emits a well-formed broadcast ARP request for the server IP, sourced from
// the EEPROM-read identity, then spins in cl_fetch_loop awaiting a reply. Because
// the logic is sound here but fails on hardware, the fault is in the real-silicon
// layer this digital model idealises away — specifically the PHY link-up timing:
// client_main TXes proactively right after drv_init (which never waits for link),
// before the link is up, so the frame is dropped and — with no ARP retransmit in
// cl_fetch_loop — the client waits forever. smoke_main escapes this by being
// reactive. See i124 for the full diagnosis; the link-up model + fix are i124's
// follow-up. (Emulation-verified is not hardware-verified, CLAUDE.md §5.)
func TestClientBootReachesFirstTX(t *testing.T) {
	mac, err := z80h.LoadBoot(cliBootBin, cliBootMap, romBaseBoot)
	if err != nil {
		t.Skipf("client boot binary not built (%v); run `make netboot-client-boot`", err)
	}
	enc := z80h.NewENC28J60()
	enc.ProgramTrinityNetwork(mask.ServerMAC, mask.ServerIP)
	mac.AttachIO(enc)

	// No RX queued: after client_first's ARP, cl_fetch_loop spins for a reply that
	// never comes, so RunBoot returns Halted=false with PC inside the fetch loop.
	res, err := mac.RunBoot("client_main", z80h.Entry{StepCap: bootStepCap})
	if err != nil {
		t.Fatalf("RunBoot client_main: %v", err)
	}
	border, hadBorder := enc.LastBorder()
	t.Logf("client_main: halted=%v PC=&%04X steps=%d border=%d(written=%v) tx=%d",
		res.Halted, res.PC, res.Steps, border, hadBorder, len(enc.TXFrames()))

	if len(enc.TXFrames()) != 1 {
		t.Fatalf("client_main put %d frames on the wire, want exactly 1 (the ARP request). "+
			"halted=%v PC=&%04X — a fail border (2=cfg,1=init) or 0 frames means a bug EARLIER "+
			"than client_first (the EEPROM read / drv_init), not the suspected hardware link-up issue",
			len(enc.TXFrames()), res.Halted, res.PC)
	}
	arp, ok := frame.ParseARPRequest(enc.TXFrames()[0])
	if !ok {
		t.Fatalf("client_main's first frame is not a well-formed ARP request: %x", enc.TXFrames()[0])
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
	if arp.TargetIP != cliBootServerIP {
		t.Errorf("ARP asks for %v, want the server IP %v", arp.TargetIP, cliBootServerIP)
	}
	// It must still be running (a livelock awaiting the reply), not halted on a
	// fail border — exactly the hardware symptom (flickering border, silent wire).
	if res.Halted {
		t.Errorf("client_main halted (PC=&%04X border=%d); expected it to spin awaiting the ARP reply", res.PC, border)
	}
}

// TestClientBootRecoversFromLinkDownStart is the i131 recovery half: the REAL
// fixed client_main, booted with the PHY link modelled DOWN at start, still puts
// exactly its one ARP request on the wire — because drv_wait_link blocks until
// the link comes up before the proactive client_first send. With the emulator now
// dropping a transmit issued while the link is down (the silent wire, asserted in
// TestDrvWriteSilentWireWhileLinkDown), this is a genuine regression guard for the
// i127 fix: were drv_wait_link removed from client_main, client_first would clock
// the ARP out while the link is still down, the frame would be dropped, and this
// test would see 0 frames.
//
// The guard is self-calibrating (no magic op-count): a baseline run with the link
// up from the start records FirstTXOps — the exact op-count at which client_main
// puts the ARP on the wire, i.e. the moment an un-fixed client would transmit.
// The guarded run then models the link as still down at that op-count (threshold
// FirstTXOps + a small margin), so an un-fixed client would necessarily transmit
// into a down link and lose the frame; the fixed client's drv_wait_link spins past
// that point and the ARP still egresses, byte-identical to the baseline.
//
// Link-up *timing* stays hardware-gated (CLAUDE.md §5); this asserts the code's
// wait-then-send logic against the hardware-confirmed silent-wire model.
func TestClientBootRecoversFromLinkDownStart(t *testing.T) {
	run := func(linkUpAfterOps int) (z80h.CallResult, *z80h.ENC28J60) {
		mac, err := z80h.LoadBoot(cliBootBin, cliBootMap, romBaseBoot)
		if err != nil {
			t.Skipf("client boot binary not built (%v); run `make netboot-client-boot`", err)
		}
		enc := z80h.NewENC28J60()
		enc.ProgramTrinityNetwork(mask.ServerMAC, mask.ServerIP)
		enc.SetLinkUpAfterOps(linkUpAfterOps)
		mac.AttachIO(enc)
		// No RX queued: after the ARP egresses, cl_fetch_loop spins awaiting a
		// reply that never comes, so RunBoot stops at the step cap (not halted).
		res, err := mac.RunBoot("client_main", z80h.Entry{StepCap: bootStepCap})
		if err != nil {
			t.Fatalf("RunBoot client_main (linkUpAfterOps=%d): %v", linkUpAfterOps, err)
		}
		return res, enc
	}

	// Baseline: link up immediately. Exactly one ARP egresses; FirstTXOps records
	// the op-count at which it did — the point an un-fixed client would transmit.
	_, immENC := run(0)
	if n := len(immENC.TXFrames()); n != 1 {
		t.Fatalf("link up immediately: client_main put %d frames on the wire, want 1 (the ARP)", n)
	}
	unfixedTXOps := immENC.FirstTXOps()
	if unfixedTXOps <= 0 {
		t.Fatalf("baseline FirstTXOps = %d, want > 0 (no frame egressed?)", unfixedTXOps)
	}

	// Guarded run: model the link as still DOWN at unfixedTXOps (+ a margin to
	// cover the few ops client_first spends between drv_wait_link returning and the
	// actual send), coming up just after. An un-fixed client transmits at ~unfixed
	// TXOps < threshold → into a down link → dropped (0 frames). The fixed client
	// waits past the threshold, so its ARP still reaches the wire.
	const margin = 200
	threshold := unfixedTXOps + margin
	_, dlyENC := run(threshold)
	tx := dlyENC.TXFrames()
	if len(tx) != 1 {
		border, _ := dlyENC.LastBorder()
		t.Fatalf("link down at start (up after %d ops): client_main put %d frames on the wire, want exactly 1 — "+
			"0 means the ARP was dropped (drv_wait_link missing/broken: the i127 fix regressed); border=%d",
			threshold, len(tx), border)
	}
	if !bytes.Equal(tx[0], immENC.TXFrames()[0]) {
		t.Errorf("ARP after waiting for link != ARP with link up immediately\n  delayed %x\n  imm     %x", tx[0], immENC.TXFrames()[0])
	}
	// The ARP must have egressed only after the link came up — i.e. the fixed
	// client genuinely waited past the point an un-fixed one would have sent.
	if got := dlyENC.FirstTXOps(); got < threshold {
		t.Fatalf("guarded run egressed the ARP at op %d, before the link-up threshold %d — "+
			"client_first did not wait for link (the i127 fix is not in effect)", got, threshold)
	}
	t.Logf("client_main recovers: un-fixed TX would land at op %d; fixed client (link up after %d ops) sent its ARP at op %d — both exactly 1 ARP, byte-identical",
		unfixedTXOps, threshold, dlyENC.FirstTXOps())
}

// TestDrvWaitLinkWaitsThenSucceeds verifies the i127 fix's core: drv_wait_link
// polls PHSTAT2.LSTAT over the MII and blocks until the link reads up. It is a
// CODE-correctness check (does the poll loop wait, then return success), not a
// claim that link-up is record-13's hardware cause — that stays hardware-gated.
//
// With the link modelled up immediately, it returns at once; with a modelled
// delay, it must spin (more steps) and still return success once LSTAT reads up.
func TestDrvWaitLinkWaitsThenSucceeds(t *testing.T) {
	run := func(linkUpAfterOps int) z80h.CallResult {
		mac, err := z80h.Load(cliBootBin, cliBootMap)
		if err != nil {
			t.Skipf("client boot binary not built (%v); run `make netboot-client-boot`", err)
		}
		enc := z80h.NewENC28J60()
		enc.SetLinkUpAfterOps(linkUpAfterOps)
		mac.AttachIO(enc)
		res, err := mac.CallEntry("drv_wait_link", z80h.Entry{StepCap: bootStepCap})
		if err != nil {
			t.Fatalf("drv_wait_link (linkUpAfterOps=%d): %v", linkUpAfterOps, err)
		}
		return res
	}

	immediate := run(0)
	if immediate.BC != 1 {
		t.Fatalf("link up immediately: drv_wait_link returned BC=%d, want 1 (link up)", immediate.BC)
	}
	delayed := run(3000) // LSTAT reads down until ~3000 SPI ops have elapsed
	if delayed.BC != 1 {
		t.Fatalf("delayed link-up: drv_wait_link returned BC=%d, want 1 — the poll must wait then succeed", delayed.BC)
	}
	if delayed.Steps <= immediate.Steps {
		t.Fatalf("delayed run took %d steps, not more than the immediate %d — the poll did not actually wait for link",
			delayed.Steps, immediate.Steps)
	}
	t.Logf("drv_wait_link: immediate=%d steps, delayed(LSTAT up after 3000 ops)=%d steps (waited)",
		immediate.Steps, delayed.Steps)
}
