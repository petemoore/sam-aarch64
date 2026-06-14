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
