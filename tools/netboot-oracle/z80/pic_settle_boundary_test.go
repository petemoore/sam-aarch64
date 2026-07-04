package z80_test

// pic_settle_boundary_test.go — model-level pins for the i327 identity-probe
// settle semantics, driving the ENC28J60 model directly (no Z80):
//
//   - the settle window anchors at the END of the SD transaction's traffic
//     (the manual's ~50µs applies "after a heavy controller operation"), not
//     at the opening &38 select;
//   - a window that had ALREADY elapsed before a run boundary (the backwards
//     T-state-cursor jump) does not survive it — carrying its absolute
//     deadline into the new, smaller timeline pinned every later faithful
//     run's chk_trinity probe stale (the i327 boot artifact);
//   - a window still OPEN at the boundary DOES survive it — that is the
//     i242 back-to-back catch (SD traffic at the end of one run, drv_init at
//     the start of the next; microseconds apart on hardware), pinned by
//     TestCSDProbeDrvInitMustPrecedeSD at the Z80 level and here at the
//     model level.

import (
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

// probeIdent drives chk_trinity's two-char identity read directly against the
// model, with generous inter-op gaps so the per-byte BUSY window (16 T-states)
// never drops an OUT. Returns the two identity chars.
func probeIdent(enc *z80h.ENC28J60, t0 uint64) (byte, byte) {
	enc.SetTState(t0)
	enc.Out(0xDC, 0x08) // select IDENT char 0
	enc.SetTState(t0 + 100)
	d := enc.In(0xDD)
	enc.SetTState(t0 + 200)
	enc.Out(0xDC, 0x09) // select IDENT char 1
	enc.SetTState(t0 + 300)
	e := enc.In(0xDD)
	return d, e
}

func TestPICSettleBoundary(t *testing.T) {
	newENC := func() *z80h.ENC28J60 {
		enc := z80h.NewENC28J60()
		enc.AttachSD(z80h.CSDForV2(0x001D59))
		return enc
	}

	// The i327 artifact shape: the &38 arms the window early in a long run,
	// the run continues far past the deadline with no probe, then a new run
	// begins (cursor jumps backwards). The window was spent; the probe in the
	// new run must read FRESH — real silicon settled long ago.
	t.Run("expired window does not survive a run boundary", func(t *testing.T) {
		enc := newENC()
		enc.SetTState(100)
		enc.Out(0xDC, 0x38) // SD init: arms the window (deadline 1300)
		enc.SetTState(50_000)
		enc.SetTState(10) // run boundary: window expired at 1300 << 50000
		if d, e := probeIdent(enc, 200); d != 'T' || e != 'R' {
			t.Errorf("identity probe after an EXPIRED window crossed a run boundary = %q%q, want \"TR\" (the i327 stale-pin artifact)", d, e)
		}
	})

	// The i242 shape: the window is still open when the boundary comes
	// (back-to-back runs = back-to-back routines on hardware). It must
	// survive, keeping the SD-before-ENC ordering bug catchable.
	t.Run("open window survives a run boundary", func(t *testing.T) {
		enc := newENC()
		enc.SetTState(100)
		enc.Out(0xDC, 0x38) // deadline 1300
		enc.SetTState(200)  // still open
		enc.SetTState(10)   // run boundary within the window
		if d, e := probeIdent(enc, 20); d == 'T' && e == 'R' {
			t.Error("identity probe inside a still-open window read fresh after the boundary — the i242 back-to-back catch is lost")
		}
	})

	// The re-base: a window still OPEN at the boundary must carry only its
	// REMAINING budget into the new (smaller) timeline, not its old absolute
	// deadline. Arm the window deep in a long run (a large absolute deadline)
	// with only a little budget left, then start a new run near t=0: without
	// re-basing, the old absolute deadline re-arms the window for the whole next
	// run, pinning every probe stale (the PR 820 §3 residual — the i327 artifact
	// in a narrower shape). With re-basing the window closes after the remaining
	// budget elapses, so a probe well past it reads fresh.
	t.Run("open window re-bases its remaining budget across the boundary", func(t *testing.T) {
		enc := newENC()
		enc.SetTState(100_000)
		enc.Out(0xDC, 0x38)    // arm deep in the run: absolute deadline 101_200
		enc.SetTState(100_100) // still open: 1_100 T-states of budget left
		enc.SetTState(50)      // run boundary: re-base to 50 + 1_100 = 1_150
		// Within the re-based budget the window still pins the probe stale — the
		// i242 back-to-back catch is preserved, not thrown away by the re-base.
		if d, e := probeIdent(enc, 100); d == 'T' && e == 'R' {
			t.Error("probe inside the re-based budget read fresh — the i242 back-to-back catch was lost")
		}
		// Past the re-based deadline (1_150) but far below the OLD absolute
		// deadline (101_200): fresh only if the deadline was re-based. Without
		// the clamp this reads stale — the whole-next-run stale pin.
		if d, e := probeIdent(enc, 2_000); d != 'T' || e != 'R' {
			t.Errorf("probe past the re-based deadline = %q%q, want \"TR\" — the old absolute deadline re-armed the window for the whole next run (PR 820 §3 residual)", d, e)
		}
	})

	// The end-anchor: SD traffic while the window is open refreshes the
	// deadline, so it measures quiet time after the transaction's LAST byte,
	// not after the opening select.
	t.Run("SD traffic refreshes the deadline to the last byte", func(t *testing.T) {
		enc := newENC()
		enc.SetTState(100)
		enc.Out(0xDC, 0x38) // deadline 1300
		enc.SetTState(1200)
		enc.Out(0xDD, 0xFF) // SD data byte at 1200 -> deadline 2400
		if d, e := probeIdent(enc, 1400); d == 'T' && e == 'R' {
			t.Error("probe 200T after the last SD byte read fresh — the window is still anchored at the opening &38, not the traffic end")
		}
		if d, e := probeIdent(enc, 3000); d != 'T' || e != 'R' {
			t.Errorf("probe past the refreshed deadline = %q%q, want \"TR\" (lazy in-run expiry)", d, e)
		}
	})
}
