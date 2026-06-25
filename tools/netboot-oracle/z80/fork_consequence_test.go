// fork_consequence_test.go — ANALYSIS HARNESS. Proves the CONSEQUENCES of the
// stock-vs-fork ROM differences from ground principles (running the model), not
// by inference: (1) the exact stock return-to-BASIC path after the boot-screen
// key, by patching out the wait and tracing the teardown; (2) which non-boot diff
// addresses (&FC44 message pointer, &D902 disk constant, &FBFF command table) are
// actually read during cold boot. Skips when the private captures are absent.
package z80_test

import (
	"os"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

// TestStockTeardownReturnsToBASIC proves how the STOCK boot screen returns to
// BASIC after a keypress. The cold boot reaches WTFK (&0FA2: CALL READKEY / JR
// Z,WTFK) and, with no key modelled, would spin there forever. We patch the
// `JR Z,WTFK` (bytes &0FA5..&0FA6 = 28 FB) to NOP NOP so the no-key path falls
// straight through to the teardown — i.e. we test "what happens after the key" —
// and trace the exact instructions stock runs to get back to BASIC. This is the
// authoritative return-to-BASIC sequence the EEPROM bootblock must mirror.
func TestStockTeardownReturnsToBASIC(t *testing.T) {
	stockPath := requirePrivateCapture(t, "rom_stock_v30.bin")
	eepPath := requirePrivateCapture(t, "eeprom.bin")
	stock, _ := os.ReadFile(stockPath)
	eeprom, _ := os.ReadFile(eepPath)

	patched := append([]byte(nil), stock...)
	// &0FA5..&0FA6 are ROM0 -> file offset 0x0FA5. Replace JR Z,WTFK with NOP NOP.
	if patched[0x0FA5] != 0x28 || patched[0x0FA6] != 0xFB {
		t.Fatalf("expected JR Z at &0FA5 (28 FB), got %02X %02X", patched[0x0FA5], patched[0x0FA6])
	}
	patched[0x0FA5], patched[0x0FA6] = 0x00, 0x00

	mac := z80h.New()
	if err := mac.LoadROMImage(patched); err != nil {
		t.Fatalf("load: %v", err)
	}
	mac.Pager().LMPR = bootLMPR
	mac.Pager().HMPR = bootHMPR
	enc := z80h.NewENC28J60()
	enc.LoadEEPROMImage(deviceLinearEEPROM(eeprom))
	mac.AttachIO(enc)

	// Trace the teardown: from the first time we reach &0FA7 (CALL CLSLOWER), record
	// the PC path until it reaches the BASIC main loop region (ERRHAND2 &102F) and a
	// bit beyond, plus every write to LINICOLS (&5600).
	var path []uint16
	recording := false
	mac.SetAccessTrace(func(addr uint16, write bool, val uint8) {
		if write && addr == 0x5600 {
			t.Logf("  WRITE LINICOLS[0] (&5600) = &%02X  (the rainbow disarm)", val)
		}
	})
	mac.RunBootFrom(0x0000, z80h.Entry{
		StepCap: 3_000_000,
		Trace: func(pc uint16) {
			if pc == 0x0FA7 {
				recording = true
			}
			if recording && len(path) < 4000 {
				path = append(path, pc)
			}
		},
	})
	if len(path) == 0 {
		t.Fatal("never reached the teardown at &0FA7 — patch/boot did not work")
	}
	t.Logf("STOCK teardown -> BASIC path (from &0FA7):")
	for i, pc := range path {
		t.Logf("   [%d] &%04X", i, pc)
		if pc == 0x102F {
			t.Logf("   ^ reached ERRHAND2 (&102F) = exit to BASIC main loop")
		}
	}
}

// TestStockSetsNSPPCandTVDATA proves whether the STOCK boot writes NSPPC (&5C44)
// and TVDATA (&5BBE) — the two sysvars Colin's EEPROM bootblock sets before
// JP ERRHAND2 — and to what values. If stock sets them identically, the bootblock
// is replicating stock init (so our patch copies stock); if not, Colin added them.
// We trace the stock boot with the key-wait NOP'd out so it runs all the way to
// BASIC, recording every write to those two addresses.
func TestStockSetsNSPPCandTVDATA(t *testing.T) {
	stockPath := requirePrivateCapture(t, "rom_stock_v30.bin")
	eepPath := requirePrivateCapture(t, "eeprom.bin")
	stock, _ := os.ReadFile(stockPath)
	eeprom, _ := os.ReadFile(eepPath)
	patched := append([]byte(nil), stock...)
	patched[0x0FA5], patched[0x0FA6] = 0x00, 0x00 // NOP out JR Z,WTFK -> runs to BASIC

	mac := z80h.New()
	mac.LoadROMImage(patched)
	mac.Pager().LMPR = bootLMPR
	mac.Pager().HMPR = bootHMPR
	enc := z80h.NewENC28J60()
	enc.LoadEEPROMImage(deviceLinearEEPROM(eeprom))
	mac.AttachIO(enc)

	type wr struct {
		val  uint8
		seen bool
	}
	last := map[uint16]wr{0x5C44: {}, 0x5BBE: {}}
	n := map[uint16]int{}
	mac.SetAccessTrace(func(addr uint16, write bool, val uint8) {
		if !write {
			return
		}
		if _, ok := last[addr]; ok {
			last[addr] = wr{val: val, seen: true}
			n[addr]++
		}
	})
	mac.RunBootFrom(0x0000, z80h.Entry{StepCap: 3_000_000})
	for _, a := range []uint16{0x5C44, 0x5BBE} {
		name := "NSPPC"
		if a == 0x5BBE {
			name = "TVDATA"
		}
		w := last[a]
		if !w.seen {
			t.Logf("STOCK boot: &%04X %s never written during cold boot+teardown", a, name)
		} else {
			t.Logf("STOCK boot: &%04X %s written %d times; LAST value = &%02X", a, name, n[a], w.val)
		}
	}
	t.Log("(Colin's bootblock sets NSPPC=&FF and TVDATA=&10 before JP ERRHAND2)")
}

// TestNonBootDiffReachability proves whether the three NON-rainbow diff regions
// are read during cold boot, distinguishing "boot-relevant" from "later-behaviour"
// changes. &FC44 is the UMVAL message-table pointer (read at init -> UMSGS);
// &D902 is a disk index-hole constant; &FBFF is the BASIC command table.
func TestNonBootDiffReachability(t *testing.T) {
	forkPath := requirePrivateCapture(t, "rom.bin")
	stockPath := requirePrivateCapture(t, "rom_stock_v30.bin")
	eepPath := requirePrivateCapture(t, "eeprom.bin")
	eeprom, _ := os.ReadFile(eepPath)

	watch := map[uint16]string{
		0xFC44: "UMVAL message-table pointer (lo)",
		0xFC45: "UMVAL message-table pointer (hi)",
		0xD902: "disk index-hole constant",
		0xFBFF: "BASIC command table",
		0xFC07: "BASIC command table (mid)",
	}
	for _, which := range []string{"stock", "fork"} {
		path := stockPath
		if which == "fork" {
			path = forkPath
		}
		rom, _ := os.ReadFile(path)
		mac := z80h.New()
		mac.LoadROMImage(rom)
		mac.Pager().LMPR = bootLMPR
		mac.Pager().HMPR = bootHMPR
		enc := z80h.NewENC28J60()
		enc.LoadEEPROMImage(deviceLinearEEPROM(eeprom))
		mac.AttachIO(enc)
		hits := map[uint16]int{}
		mac.SetAccessTrace(func(addr uint16, write bool, val uint8) {
			if _, ok := watch[addr]; ok && !write {
				hits[addr]++
			}
		})
		mac.RunBootFrom(0x0000, z80h.Entry{StepCap: 3_000_000})
		t.Logf("[%s boot] reads of the watched non-boot-diff addresses:", which)
		for a, name := range watch {
			t.Logf("   &%04X %-36s read %d times during cold boot", a, name, hits[a])
		}
	}
}
