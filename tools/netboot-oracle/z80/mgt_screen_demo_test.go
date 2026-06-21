// mgt_screen_demo_test.go — emulation verification of the MGT opening-screen RAM
// demo (src/netboot/mgt_screen_demo_standalone.asm, i229).
//
// The demo reproduces the SAM boot screen with NO ROM-routine calls (CLAUDE.md §7
// / i231: nothing hidden behind a build flag or needing a booted sysvar state): it
// baselines the border, builds the LINICOLS rainbow (verbatim &ED1B port), prints
// the MGT banner via RST &10, EI's to arm the line-interrupt stripes, holds until a
// key, disarms the rainbow, and RETs to trinload. So it runs verbatim in the flat
// harness. The Go harness has no display, so this asserts the demo built the exact
// LINICOLS table, printed the exact banner text, and returned cleanly — the
// rendered pixels are confirmed on Pete's real SAM (i230). PALTAB is NOT seeded: a
// real boot's ROM cold-init populates it (i232), so the test asserts the stripes
// copied WHATEVER PALTAB held (the loop's addressing/stepping/copy logic), not
// invented colours.
package z80_test

import (
	"bytes"
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
		t.Fatalf("mgt_screen_demo not built (%s); run `make netboot-mgt-screen-demo`: %v", mgtDemoBin, err)
	}

	// Do NOT seed PALTAB — that would be a synthetic, unfaithful state (i232: a
	// real boot's ROM cold-init populates it). Read whatever PALTAB actually holds
	// and assert the stripes copied IT.
	palette := mac.Read(paltab+1, 16)

	// Intercept RST &10 so the banner's print loop records its characters without a
	// real print channel.
	rec := mac.AttachPrintRecorder()

	// Press Esc so the hold-until-key loop (`in a,(&f9); bit 5`) falls through.
	mac.PressEsc(true)

	// Snapshot LINICOLS the first time the demo reaches the hold loop (mgt_wtfk):
	// the rainbow is fully built there, before the teardown disarms entry 0. The
	// post-run table has LINICOLS[0]=&FF (the teardown), which we assert separately.
	wtfk, err := mac.Sym("mgt_wtfk")
	if err != nil {
		t.Fatalf("symbol mgt_wtfk not in map: %v", err)
	}
	var built []byte
	res, err := mac.CallEntry("mgt_demo_main", z80h.Entry{
		Trace: func(pc uint16) {
			if pc == wtfk && built == nil {
				built = mac.Read(linicols, 16*4+1)
			}
		},
	})
	if err != nil {
		t.Fatalf("run mgt_demo_main: %v", err)
	}
	if !res.Halted {
		t.Fatalf("demo did not stop (PC=&%04X) — the hold loop never exited", res.PC)
	}
	if built == nil {
		t.Fatal("demo never reached the hold loop (mgt_wtfk) — the build/EI path did not run")
	}

	// The stripes build 16 four-byte LINICOLS entries {scan_lo, 0, colour, colour}
	// with scan stepping by &0B from 0 while < &A6, then an &FF terminator.
	got := built
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

	// The banner printed the authoritative MGT copyright text via RST &10.
	wantBanner := append([]byte("   MILES GORDON TECHNOLOGY plc      "), 0x7F)
	wantBanner = append(wantBanner, []byte(" 1990 SAM Coupe 512K")...)
	if got := rec.Chars(); !bytes.Equal(got, wantBanner) {
		t.Errorf("banner printed %q, want %q", got, wantBanner)
	}

	// On keypress the teardown disarms the rainbow: LINICOLS[0] = &FF, so the ROM
	// line-interrupt stops painting next frame and the screen returns to trinload's.
	if b := mac.Read(linicols, 1)[0]; b != 0xFF {
		t.Errorf("post-run LINICOLS[0] = &%02X, want &FF (teardown should disarm the rainbow)", b)
	}

	// The demo ends via tr_terminate, which in emulation takes the di;halt branch.
	if modeAddr, err := mac.Sym("TR_TERM_MODE"); err == nil {
		if b := mac.Read(modeAddr, 1)[0]; b != trModeEmu {
			t.Errorf("TR_TERM_MODE = &%02X, want &%02X (EMU)", b, trModeEmu)
		}
	}
}
