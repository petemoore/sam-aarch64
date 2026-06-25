// port_probe_test.go — emulation check of the hardware port-characterization
// probe (item i228 step A; src/netboot/port_probe_standalone.asm).
//
// The probe's real job is on hardware (read it off the wire to characterize the
// SAM's unmapped ports), but it is still a network payload that must run in
// emulation first (CLAUDE.md §7): this asserts it transmits a well-formed SATR
// report (test_id 2) whose port bytes are exactly the candidate list, so the
// sweep + report wiring is proven before the hardware push.
package z80_test

import (
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	portProbeBin = "../../../build/port_probe.bin"
	portProbeMap = "../../../build/port_probe.map"
)

// candidatePorts must match pp_ports in src/netboot/port_probe_standalone.asm.
var candidatePorts = []byte{
	0x00, 0x08, 0x10, 0x18, 0x20, 0x28, 0x30, 0x38,
	0x40, 0x50, 0x60, 0x70, 0x7F, 0x80, 0x90, 0xA0,
}

func TestPortProbeEmitsReport(t *testing.T) {
	mac, err := z80h.Load(portProbeBin, portProbeMap)
	if err != nil {
		t.Fatalf("port_probe not built (%s); run `make netboot-port-probe`: %v", portProbeBin, err)
	}
	enc := z80h.NewENC28J60()
	mac.AttachIO(enc)

	res, err := mac.Call("port_probe_main")
	if err != nil {
		t.Fatalf("run port_probe_main: %v", err)
	}
	if !res.Halted {
		t.Fatalf("payload did not halt (PC=&%04X)", res.PC)
	}

	rep, ok := parseSATR(enc.TXFrames())
	if !ok {
		t.Fatalf("no SATR report frame was transmitted")
	}
	if rep.testID != 2 {
		t.Errorf("report test_id = %d, want 2 (port probe)", rep.testID)
	}
	if len(rep.detail) != 2*len(candidatePorts) {
		t.Fatalf("report detail = %d bytes, want %d ([port,value] pairs)", len(rep.detail), 2*len(candidatePorts))
	}
	// The even bytes are the ports swept, in order; the odd bytes are whatever
	// the emulator returned (the hardware values are the point of the real run,
	// so they are not asserted here).
	for i, port := range candidatePorts {
		if got := rep.detail[2*i]; got != port {
			t.Errorf("detail pair %d: port = &%02X, want &%02X", i, got, port)
		}
	}
	t.Logf("port probe report (emulation values): %x", rep.detail)
}
