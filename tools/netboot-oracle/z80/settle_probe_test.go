// settle_probe_test.go — emulation-first check of the i291b ENC28J60/shared-PIC
// settle-time probe (src/netboot/settle_probe.asm).
//
// The probe's real job is on hardware: pushed with a poked delay N, it reports
// over UDP whether a chk_trinity identity read issued N T-states after the &38
// SD-init reads STALE (the PIC still settling) or FRESH (settled), so a host
// bisection (i291b-b2) can sweep N on real silicon and converge on the real settle
// window. But it is a network payload that must run in emulation first
// (CLAUDE.md §7): this drives the BUILT BINARY through the ENC28J60 model across a
// sweep of settle_delay_count and asserts the reported status flips stale->fresh
// exactly once across the modelled settle boundary (sdInitSettleTStates) — proving
// the deployable probe reports stale<settle / fresh>=settle before any hardware push.
//
// pic_settle_boundary_test.go / trinity_sdinit_settle_test.go pin the MODEL's
// settle semantics by driving it directly (no Z80); this pins the deployable PROBE
// BINARY that will perform the real-hardware measurement.
package z80_test

import (
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	settleProbeBin = "../../../build/settle_probe.bin"
	settleProbeMap = "../../../build/settle_probe.map"

	settleStatusFresh = 0 // SATR status byte: settled (chk_trinity read 'TR')
	settleStatusStale = 1 // SATR status byte: still settling (identity select dropped)
	settleTestID      = 3 // TEST_ID_SETTLE in settle_probe.asm
)

// runSettleProbe loads a fresh probe binary + ENC model, pokes settle_delay_count
// = n, runs settle_probe_main to completion, and returns the decoded SATR report
// plus the run's total T-states. A fresh machine per call keeps each run's settle
// state and TX frames independent.
func runSettleProbe(t *testing.T, n uint16) (satrReport, uint64) {
	t.Helper()
	mac, err := z80h.Load(settleProbeBin, settleProbeMap)
	if err != nil {
		t.Fatalf("settle_probe not built (%s); run `make netboot-settle-probe`: %v", settleProbeBin, err)
	}
	enc := z80h.NewENC28J60()
	enc.AttachSD(z80h.CSDForV2(0x001D59)) // a card present, as on real Trinity hardware
	mac.AttachIO(enc)

	addr, err := mac.Sym("settle_delay_count")
	if err != nil {
		t.Fatalf("settle_delay_count symbol missing from the probe map: %v", err)
	}
	mac.WriteU16LE(addr, n)

	res, err := mac.Call("settle_probe_main")
	if err != nil {
		t.Fatalf("run settle_probe_main (N=%d): %v", n, err)
	}
	if !res.Halted {
		t.Fatalf("probe (N=%d) did not halt (PC=&%04X)", n, res.PC)
	}
	rep, ok := parseSATR(enc.TXFrames())
	if !ok {
		t.Fatalf("probe (N=%d) transmitted no SATR report frame", n)
	}
	if rep.testID != settleTestID {
		t.Fatalf("report test_id = %d, want %d (settle probe)", rep.testID, settleTestID)
	}
	if len(rep.detail) != 4 {
		t.Fatalf("report detail = %d bytes, want 4 ([N_lo,N_hi,readT,readR])", len(rep.detail))
	}
	// The probe echoes the N it used in detail[0..1]; confirm the poke landed.
	if gotN := uint16(rep.detail[0]) | uint16(rep.detail[1])<<8; gotN != n {
		t.Fatalf("probe echoed N=%d, want %d — the settle_delay_count poke did not land", gotN, n)
	}
	return rep, res.TStates
}

// TestSettleProbeStaleSignature: at the minimal delay (N=1) the probe reads STALE.
// The deciding signal is the FIRST identity select (&08 -> readT): issued inside
// the settle window it is dropped, so IN &DD returns the stale latch rather than
// 'T'. (chk_trinity's internal DJNZ delays between its two selects exceed the
// window, so readR may already read the settled 'R' — the classification hangs on
// the first select, not the pair.) A dropped first select is the i242/i287 catch:
// a probe right after the &38 must read stale.
func TestSettleProbeStaleSignature(t *testing.T) {
	rep, _ := runSettleProbe(t, 1)
	if rep.status != settleStatusStale {
		t.Fatalf("N=1 (minimal delay) status = %d, want STALE(%d) — a probe issued right after the &38 must read stale (the i242/i287 catch)", rep.status, settleStatusStale)
	}
	if rep.detail[2] == 'T' {
		t.Errorf("stale readT = 'T' — the first identity select was honoured within the window; the i242 catch is broken")
	}
}

// TestSettleProbeFreshReadsTR: well past the settle budget the identity select is
// honoured, so chk_trinity reads the real IDENT chars 'T','R' and classifies FRESH.
func TestSettleProbeFreshReadsTR(t *testing.T) {
	rep, _ := runSettleProbe(t, 500)
	if rep.status != settleStatusFresh {
		t.Fatalf("N=500 (large delay) status = %d, want FRESH(%d) — well past the settle budget the probe must read fresh", rep.status, settleStatusFresh)
	}
	if rep.detail[2] != 'T' || rep.detail[3] != 'R' {
		t.Errorf("fresh read pair = %q,%q, want 'T','R' (the honoured IDENT read)", rune(rep.detail[2]), rune(rep.detail[3]))
	}
}

// TestSettleProbeReportsSettleBoundary sweeps the poked delay N across the modelled
// settle window and asserts the probe's reported status transitions stale->fresh
// exactly once and never flaps back — the property a hardware bisection relies on
// to converge on the real settle. The boundary N is logged (with its run T-states)
// as a calibration anchor for the i291b-b2 hardware sweep.
func TestSettleProbeReportsSettleBoundary(t *testing.T) {
	const maxN = 200 // the modelled boundary sits well below this (~1200 T-states / ~26 T per iter)
	statuses := make([]byte, maxN+1)
	firstFresh := -1
	var firstFreshTS uint64
	var firstFreshRep satrReport
	for n := 1; n <= maxN; n++ {
		rep, ts := runSettleProbe(t, uint16(n))
		statuses[n] = rep.status
		if rep.status == settleStatusFresh && firstFresh < 0 {
			firstFresh, firstFreshTS, firstFreshRep = n, ts, rep
		}
	}

	// (a) the minimal delay reads STALE — the i242 catch, preserved by the probe.
	if statuses[1] != settleStatusStale {
		t.Errorf("N=1 reported status %d, want STALE(%d)", statuses[1], settleStatusStale)
	}
	// (b) the largest delay reads FRESH — the settle elapses and the probe sees it.
	if statuses[maxN] != settleStatusFresh {
		t.Errorf("N=%d reported status %d, want FRESH(%d)", maxN, statuses[maxN], settleStatusFresh)
	}
	// (c) monotonic: exactly one stale->fresh transition, stale strictly below it
	// and fresh strictly at/above it.
	if firstFresh < 0 {
		t.Fatalf("no N in 1..%d read FRESH — the probe never observes the window closing", maxN)
	}
	t.Logf("settle boundary: first FRESH at N=%d (run TStates=%d, read %q%q)",
		firstFresh, firstFreshTS, rune(firstFreshRep.detail[2]), rune(firstFreshRep.detail[3]))
	for n := 1; n < firstFresh; n++ {
		if statuses[n] != settleStatusStale {
			t.Errorf("non-monotonic: N=%d read FRESH before the boundary at N=%d", n, firstFresh)
		}
	}
	for n := firstFresh; n <= maxN; n++ {
		if statuses[n] != settleStatusFresh {
			t.Errorf("non-monotonic: N=%d read STALE after the boundary at N=%d — the probe's classification flaps", n, firstFresh)
		}
	}
}
