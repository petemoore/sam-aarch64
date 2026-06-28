package z80_test

import (
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

// trinity_sdinit_settle_test.go — the i289 host test for the BOUNDED post-&38
// settle window. It exercises the bare ENC model (no private ROM/B-DOS, no
// captures) so it runs in normal CI.
//
// BACKGROUND. The shared Trinity PIC, right after a heavy &38 SD-init, is briefly
// settling; an identity-select (&08..&0F, used by chk_trinity, which does NOT
// busy-poll) issued WITHIN that window is not honoured and the &DD read-back
// returns the prior (stale) latch — chk_trinity sees != 'TR' and reports the
// board missing. That catch (i242/i287) guards the real SD-before-ENC ordering
// bug Pete hit on hardware (2026-06-24). But the window is FINITE: real silicon
// settles in ~50µs and the latch never stays "unsettled" forever. The model
// formerly cleared sdInitSettling ONLY on an &28 ENC reset, so in a faithful full
// boot — where B-DOS's auto-load does the &38 with NO following &28 (~192k Z80
// instructions, 0 resets, before the next probe; i288) — the flag latched
// indefinitely and manufactured a FALSE stale read that does NOT happen on
// hardware. The fix (enc28j60.go sdInitSettleTStates) bounds the window in
// T-states.
//
// This test pins BOTH halves of the corrected contract:
//   (a) a &38 immediately followed by an identity probe with NO settle still reads
//       STALE  — the i242/i287 catch is preserved; and
//   (b) a &38 followed by advancing the T-state cursor PAST the settle budget,
//       then an identity probe, reads FRESH — the over-aggressive latch is gone.
//
// The deadline anchors at the END of the SD traffic (each SD data byte inside
// an open window refreshes it — i327): the manual's ~50µs settle follows the
// heavy operation, and the i242 hardware failure was a probe right after the
// CSD *traffic*. pic_settle_boundary_test.go pins the run-boundary semantics.

const (
	settleCtlPort  = 0xDC // portTrinityCtl
	settleEEPPort  = 0xDD // portTrinityEEP — also the identity-probe reply port
	settleSDPort   = 0xDF // portTrinitySD — SD SPI data
	settleProbeT   = 0x08 // selProbeT (&08 -> IDENT[0] = 'T')
	settleSDInit   = 0x38 // selSDInit (&38 SD-init wake)
	settleBudgetTS = 1200 // sdInitSettleTStates (must match enc28j60.go)
	// gapTS advances the cursor between successive port ops so the one-SPI-byte
	// BUSY window (busyByteTStates=16) has elapsed — otherwise a second OUT issued
	// while BUSY is silently dropped (manual:22). Real inter-OUT code gaps are far
	// larger than one SPI byte; this models that.
	gapTS = 64
)

// dirtyLatchWithSD clocks one SD byte at cursor t so the shared read-back latch
// holds a real (non-'T') SD response byte — exactly the latch state a faithful
// boot leaves after its post-&38 CSD read, before drv_init's chk_trinity probe.
// Returns the next free cursor.
func dirtyLatchWithSD(enc *z80h.ENC28J60, t uint64) uint64 {
	enc.SetTState(t)
	enc.Out(settleSDPort, 0xFF) // clock the SD -> latch <- SD MISO (not 'T')
	return t + gapTS
}

// probeIdentByte issues the identity-select &08 and reads the &DD reply latch,
// driving SetTState before each port op exactly as the harness does. It returns
// the byte chk_trinity would see for IDENT[0].
func probeIdentByte(enc *z80h.ENC28J60, t uint64) byte {
	enc.SetTState(t)
	enc.Out(settleCtlPort, settleProbeT) // select IDENT char 0
	enc.SetTState(t + gapTS)
	return enc.In(settleEEPPort) // read the shared latch
}

// TestSDInitSettleStaleWithinWindow is the i242/i287 catch, preserved: a &38
// SD-init immediately followed by an identity probe (no settle time elapsed)
// reads STALE — the probe is dropped and 'T' is NOT delivered.
func TestSDInitSettleStaleWithinWindow(t *testing.T) {
	enc := z80h.NewENC28J60()
	enc.AttachSD(z80h.CSDForV2(0x001D59))

	// Seed the latch with a known FRESH probe so 'T' is unambiguously present
	// before the SD-init disturbs the PIC. With a quiescent PIC the probe is
	// honoured and IDENT[0] = 'T'.
	if got := probeIdentByte(enc, 0); got != 'T' {
		t.Fatalf("pre-SD identity probe (quiescent PIC) = &%02X (%q), want 'T' — the probe path is broken before we even test settling", got, rune(got))
	}

	// Heavy SD-init at t=200: the PIC is now settling. settleUntilT = 200+budget.
	const sdInitT = 200
	enc.SetTState(sdInitT)
	enc.Out(settleCtlPort, settleSDInit)

	// A faithful boot then reads the CSD over SD, leaving a real SD byte (not 'T')
	// on the shared latch before drv_init's chk_trinity probe.
	next := dirtyLatchWithSD(enc, sdInitT+gapTS)

	// Probe WITHIN the window (cursor still < deadline): the probe is dropped, the
	// latch keeps its SD byte, and chk_trinity reads STALE (!= 'T').
	if got := probeIdentByte(enc, next); got == 'T' {
		t.Errorf("identity probe issued WITHIN the settle window returned 'T' (FRESH) — the i242/i287 back-to-back catch is broken; an SD-before-ENC ordering bug would no longer fail in emulation")
	}
}

// TestSDInitSettleFreshAfterWindow is the i289 fix: a &38 SD-init followed by
// advancing the T-state cursor PAST the settle budget, then an identity probe,
// reads FRESH ('T'). The latch no longer stays "unsettled" indefinitely waiting
// for an &28 that a faithful full boot never issues.
func TestSDInitSettleFreshAfterWindow(t *testing.T) {
	enc := z80h.NewENC28J60()
	enc.AttachSD(z80h.CSDForV2(0x001D59))

	// SD-init at t=200: settle window is [200, 200+budget).
	const sdInitT = 200
	enc.SetTState(sdInitT)
	enc.Out(settleCtlPort, settleSDInit)

	// Dirty the latch with a real SD byte (as a CSD read would), so a successful
	// FRESH probe is unambiguous: only an honoured probe can put 'T' back. The
	// SD byte also REFRESHES the settle deadline (i327): the manual's ~50µs is
	// measured after the transaction's LAST byte, not after the opening &38.
	lastByteT := uint64(sdInitT + gapTS)
	dirtyLatchWithSD(enc, lastByteT)

	// Advance well past the budget from the LAST SD byte — but VASTLY less than
	// the ~192k-instruction real-world gap that exposed the indefinite-latch bug
	// (i288). The PIC has settled; the probe must now be honoured.
	past := lastByteT + settleBudgetTS + 50
	if got := probeIdentByte(enc, past); got != 'T' {
		t.Errorf("identity probe issued PAST the settle window returned &%02X (%q), want 'T' (FRESH) — the over-aggressive indefinite latch is NOT fixed; a faithful full boot (&38 with no following &28) would falsely fail chk_trinity", got, rune(got))
	}
}

// TestSDInitSettleBoundaryStillStale guards the lower boundary: a probe at the
// LAST T-state inside the window (deadline-1) still reads STALE, so the budget is
// a genuine window, not an off-by-one no-op.
func TestSDInitSettleBoundaryStillStale(t *testing.T) {
	enc := z80h.NewENC28J60()
	enc.AttachSD(z80h.CSDForV2(0x001D59))

	const sdInitT = 200
	enc.SetTState(sdInitT)
	enc.Out(settleCtlPort, settleSDInit)
	lastByteT := uint64(sdInitT + gapTS)
	dirtyLatchWithSD(enc, lastByteT)

	// One T-state before the deadline (anchored at the LAST SD byte, i327):
	// still settling -> STALE.
	justInside := lastByteT + settleBudgetTS - 1
	if got := probeIdentByte(enc, justInside); got == 'T' {
		t.Errorf("identity probe one T-state before the settle deadline returned 'T' (FRESH) — the window closes too early; the i242/i287 catch is weakened")
	}
}
