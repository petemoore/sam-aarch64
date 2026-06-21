package z80

import "testing"

// TestRXFilterPacketFilter covers gap 8 (manual:95,96; datasheet §8.1): the ENC
// ERXFCON receive filter. With the POR default (UCEN+BCEN) a broadcast frame and a
// frame to our MAADR pass; a unicast frame to a DIFFERENT MAC is dropped. With
// ERXFCON==0 (sniffer) everything passes.
func TestRXFilterPacketFilter(t *testing.T) {
	e := NewENC28J60()
	// MAADR storage order is [4,5,2,3,0,1]; set our MAC = 02:54:52:49:4e:bc.
	mac := [6]byte{0x02, 0x54, 0x52, 0x49, 0x4e, 0xbc}
	e.regs[3][0x04] = mac[0]
	e.regs[3][0x05] = mac[1]
	e.regs[3][0x02] = mac[2]
	e.regs[3][0x03] = mac[3]
	e.regs[3][0x00] = mac[4]
	e.regs[3][0x01] = mac[5]

	bcast := append([]byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}, 0x08, 0x06)
	toUs := append(append([]byte{}, mac[:]...), 0x08, 0x00)
	toOther := []byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0x00, 0x08, 0x00}

	if !e.rxFilterPass(bcast) {
		t.Error("broadcast frame rejected by POR filter (BCEN); want accepted")
	}
	if !e.rxFilterPass(toUs) {
		t.Error("unicast-to-our-MAC frame rejected by POR filter (UCEN); want accepted")
	}
	if e.rxFilterPass(toOther) {
		t.Error("unicast-to-OTHER-MAC frame accepted by POR filter; want DROPPED")
	}

	// Sniffer mode: ERXFCON == 0 accepts everything.
	e.regs[1][regERXFCON] = 0
	if !e.rxFilterPass(toOther) {
		t.Error("sniffer mode (ERXFCON=0) dropped a frame; want accept-all (manual:96)")
	}
}

// TestDeselectTailObservable covers gap 10 (bdos:40): the proven SD close
// &30 -> dummy &DF -> &30 -> &04 is observed as a proper close; a bare &04 close is
// observed but flagged improper.
func TestDeselectTailObservable(t *testing.T) {
	e := NewENC28J60()
	var csd [16]byte
	csd[0] = 0x40
	e.AttachSD(csd)
	e.Out(0xDC, 0x31) // select SD
	e.In(0xDC)

	// The proven tail (poll between each, the canonical busy clear).
	e.Out(0xDC, 0x30) // 1st deselect
	e.In(0xDC)
	e.Out(0xDF, 0x2F) // dummy clock
	e.In(0xDC)
	e.Out(0xDC, 0x30) // 2nd deselect
	e.In(0xDC)
	e.Out(0xDC, 0x04) // close
	e.In(0xDC)
	if proper, observed := e.LastSDCloseProper(); !observed || !proper {
		t.Errorf("proven tail: observed=%v proper=%v, want both true", observed, proper)
	}

	// A bare &04 close after a single &30 is improper.
	e.Out(0xDC, 0x31)
	e.In(0xDC)
	e.Out(0xDC, 0x30)
	e.In(0xDC)
	e.Out(0xDC, 0x04) // close WITHOUT the dummy + 2nd &30
	e.In(0xDC)
	if proper, observed := e.LastSDCloseProper(); !observed || proper {
		t.Errorf("bare close: observed=%v proper=%v, want observed=true proper=false", observed, proper)
	}
}

// TestGlobalAutoNullMode covers gap a (manual:44): auto-null is ONE global mode
// with a target peripheral, mutually exclusive across peripherals, cleared only by
// &04. Selecting ENC auto-null (&2F) then SD auto-null (&3F) moves the target; &04
// clears it.
func TestGlobalAutoNullMode(t *testing.T) {
	e := NewENC28J60()
	var csd [16]byte
	csd[0] = 0x40
	e.AttachSD(csd)

	e.Out(0xDC, 0x2F) // ENC auto-null on
	e.In(0xDC)
	if !e.autoNullMode || e.autoNullTarget != periphENC {
		t.Errorf("&2F: autoNullMode=%v target=%v, want on/ENC", e.autoNullMode, e.autoNullTarget)
	}
	e.Out(0xDC, 0x3F) // SD auto-null on -> moves the single mode's target
	e.In(0xDC)
	if !e.autoNullMode || e.autoNullTarget != periphSD {
		t.Errorf("&3F: target=%v, want SD (mutually exclusive single mode)", e.autoNullTarget)
	}
	e.Out(0xDC, 0x04) // the ONLY clear
	e.In(0xDC)
	if e.autoNullMode {
		t.Errorf("&04 did not clear the global auto-null mode")
	}
}
