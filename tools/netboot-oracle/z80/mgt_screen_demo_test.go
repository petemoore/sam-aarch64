// mgt_screen_demo_test.go — emulation verification of the MGT opening-screen RAM
// demo (src/netboot/mgt_screen_demo_standalone.asm, i229). The stripes routine is
// a verbatim port of the stock SAM ROM RAINBOW SCREEN code (&ED1B); it is pure
// RAM writes, so it is fully verified here with NO screen model and NO build
// carve-out (CLAUDE.md §7): the harness seeds PALTAB, runs the demo, and asserts
// the exact LINICOLS line-colour table it produced. SimCoupé / real hardware
// render the actual pixels. The demo then RETs to trinload (tr_terminate).
package z80_test

import (
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	mgtDemoBin = "../../../build/mgt_screen_demo.bin"
	mgtDemoMap = "../../../build/mgt_screen_demo.map"

	paltab   = 0x55D8 // ROM palette table (stripes read from +1)
	linicols = 0x5600 // line-colour table (stripes write here)
)

func TestMGTScreenDemoStripes(t *testing.T) {
	mac, err := z80h.Load(mgtDemoBin, mgtDemoMap)
	if err != nil {
		t.Skipf("mgt_screen_demo not built (%s); run `make netboot-mgt-screen-demo`: %v", mgtDemoBin, err)
	}

	// Seed PALTAB+1 with a recognizable palette so we can prove the stripes
	// copied the right colours (on hardware the ROM cold-init populates it).
	palette := make([]byte, 16)
	for i := range palette {
		palette[i] = byte(0x70 + i) // distinct, non-zero
	}
	mac.Write(paltab+1, palette)

	res, err := mac.Call("mgt_demo_main")
	if err != nil {
		t.Fatalf("run mgt_demo_main: %v", err)
	}
	if !res.Halted {
		t.Fatalf("demo did not stop (PC=&%04X)", res.PC)
	}

	// The stripes build 16 four-byte LINICOLS entries {scan_lo, 0, colour, colour}
	// with scan stepping by &0B from 0 while < &A6, then an &FF terminator.
	got := mac.Read(linicols, 16*4+1)
	scan := 0
	for i := 0; i < 16; i++ {
		base := i * 4
		if got[base] != byte(scan) {
			t.Errorf("LINICOLS[%d].scan_lo = &%02X, want &%02X", i, got[base], byte(scan))
		}
		if got[base+1] != 0 {
			t.Errorf("LINICOLS[%d].scan_hi = &%02X, want 0", i, got[base+1])
		}
		if got[base+2] != palette[i] || got[base+3] != palette[i] {
			t.Errorf("LINICOLS[%d] colour = &%02X/&%02X, want &%02X (both)", i, got[base+2], got[base+3], palette[i])
		}
		scan += 0x0B
	}
	if got[16*4] != 0xFF {
		t.Errorf("LINICOLS terminator = &%02X, want &FF", got[16*4])
	}

	// The demo ends via tr_terminate, which in emulation takes the di;halt branch.
	if modeAddr, err := mac.Sym("TR_TERM_MODE"); err == nil {
		if b := mac.Read(modeAddr, 1)[0]; b != trModeEmu {
			t.Errorf("TR_TERM_MODE = &%02X, want &%02X (EMU)", b, trModeEmu)
		}
	}
}
