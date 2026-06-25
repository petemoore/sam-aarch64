// csd_probe_main_test.go — DIAGNOSTIC (investigation only, not a CI gate).
//
// The existing csd_probe_test.go exercises the probe PIECEWISE: it CallEntry's
// csd_read_into_stage (the SD read alone) and serve_serve_once (one serve
// iteration alone), each in isolation, against the v2/v1 CSD model. It never runs
// probe_main — the FULL trinload boot sequence the hardware actually executes:
//
//   probe_main: (1) find_index+read_chunk read the "Trinity Network " flash chunk
//   into CONFIG (the SAM's MAC/IP); (2) probe_provision sets up the csd.bin STORE;
//   (3) csd_read_into_stage runs the SD-SPI CSD read on &DC/&DF; (4) drv_init
//   re-inits the ENC28J60 with the SAM's MAC; (5) pm_serve_loop is the ARP+TFTP
//   serve loop (exits on Esc).
//
// On real hardware, pushing this probe HANGS the SAM (it stops answering ARP/ping
// after the push). The suspicion is the INTERLEAVED SD-then-ENC I/O on the shared
// Trinity microcontroller — a path no piecewise test covers. This test runs the
// whole probe_main end-to-end through the Go Trinity emulator (independent SD +
// ENC + EEPROM models) under a hard step cap, so a Z80 infinite loop surfaces as
// a TIMEOUT (RunBoot returns Halted=false at the spin PC) rather than wedging
// `go test`. It then reports PRECISELY where the run ends up.
//
// It uses the NON-HOSTTEST trinload build (build/csd_probe_trinload.bin), the only
// build that contains probe_main (the HOSTTEST build guards it out, which is WHY
// the piecewise test never ran it). The trinload image org's at &8000 and tops out
// at &B47D — entirely below &C000 — so a flat Load places it faithfully.
package z80_test

import (
	"bytes"
	"os"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/frame"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/tftp"
	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	csdProbeTrinloadBin = "../../../build/csd_probe_trinload.bin"
	csdProbeTrinloadMap = "../../../build/csd_probe_trinload.map"

	// The probe reads its own MAC/IP from the EEPROM "Trinity Network " chunk and
	// hard-codes CONFIG_SERVERTID = 40136 (probe_main). So the serve frames we
	// inject must be addressed to THIS identity, not csd_probe_test.go's.
	csdProbeMainTID = 40136
)

// The SAM identity probe_main reads from the flash chunk (ProgramTrinityNetwork
// writes sam_mac at chunk+0, sam_ip at chunk+6). The serve loop answers to these.
var (
	pmSAMMac    = [6]byte{0x02, 0x54, 0x52, 0x49, 0x4e, 0xbc} // mirrors the first-light SAM MAC shape
	pmSAMIp     = [4]byte{192, 168, 2, 75}
	pmClientMac = [6]byte{0x02, 0x00, 0x00, 0x00, 0x00, 0x44}
	pmClientIp  = [4]byte{192, 168, 2, 99}
)

const pmClientTID = 30574

func pmRRQ(name string) []byte {
	return frame.BuildUDPFrame(frame.UDP{
		DstMAC: frame.MAC(pmSAMMac), SrcMAC: frame.MAC(pmClientMac),
		SrcIP: frame.IPv4(pmClientIp), DstIP: frame.IPv4(pmSAMIp),
		SrcPort: pmClientTID, DstPort: 69,
		Payload: tftp.BuildRRQ(name, "octet", nil),
	})
}

func pmACK(block uint16) []byte {
	return frame.BuildUDPFrame(frame.UDP{
		DstMAC: frame.MAC(pmSAMMac), SrcMAC: frame.MAC(pmClientMac),
		SrcIP: frame.IPv4(pmClientIp), DstIP: frame.IPv4(pmSAMIp),
		SrcPort: pmClientTID, DstPort: csdProbeMainTID,
		Payload: tftp.BuildACK(block),
	})
}

func pmARP() []byte {
	return frame.BuildARPRequest(frame.MAC(pmClientMac), frame.IPv4(pmClientIp), frame.IPv4(pmSAMIp))
}

// loadCSDProbeMain loads the trinload (probe_main-bearing) build, skipping if it
// is not built. Flat Load (not LoadBoot): the whole image lives below &C000, and
// the probe pages nothing, so flat all-RAM is the faithful runtime model.
func loadCSDProbeMain(t *testing.T) *z80h.Machine {
	t.Helper()
	if _, err := os.Stat(csdProbeTrinloadBin); err != nil {
		t.Fatalf("csd_probe trinload binary not built (%s); run `make netboot-csd-probe-trinload`", csdProbeTrinloadBin)
	}
	mac, err := z80h.Load(csdProbeTrinloadBin, csdProbeTrinloadMap)
	if err != nil {
		t.Fatalf("load csd_probe_trinload: %v", err)
	}
	if _, err := mac.Sym("probe_main"); err != nil {
		t.Fatalf("probe_main symbol absent from %s — wrong build?", csdProbeTrinloadMap)
	}
	return mac
}

// pmInstrument records the first-visit order + hit counts of the probe_main
// milestones so the run's trajectory (how far it got, where it spun) is visible.
type pmInstrument struct {
	syms  map[uint16]string
	hits  map[uint16]int
	order []string
}

func newPMInstrument(t *testing.T, mac *z80h.Machine) *pmInstrument {
	t.Helper()
	names := []string{
		"probe_main", "find_index", "read_chunk", "probe_provision",
		"csd_read_into_stage", "csd_deselect", "drv_init",
		"pm_serve_loop", "pm_fail_cfg", "pm_fail_init", "serve_serve_once",
	}
	in := &pmInstrument{syms: map[uint16]string{}, hits: map[uint16]int{}}
	for _, n := range names {
		if a, err := mac.Sym(n); err == nil {
			in.syms[a] = n
		}
	}
	return in
}

func (in *pmInstrument) trace(pc uint16) {
	if name, ok := in.syms[pc]; ok {
		if in.hits[pc] == 0 {
			in.order = append(in.order, name)
		}
		in.hits[pc]++
	}
}

func (in *pmInstrument) count(name string) int {
	for a, n := range in.syms {
		if n == name {
			return in.hits[a]
		}
	}
	return 0
}

// TestCSDProbeMainEndToEnd runs the FULL probe_main boot sequence end-to-end
// against the configured SD card + ENC + EEPROM models, with a hard step cap so an
// infinite loop becomes a TIMEOUT (Halted=false at the spin PC), never a hang. It
// reports — via t.Logf — exactly how far probe_main got, then asserts the serve
// loop answered the ARP and streamed the CSD. A failure here REPRODUCES the
// hardware hang in emulation; a pass means the independent models do NOT reproduce
// it (pointing at a shared-microcontroller effect the models omit).
func TestCSDProbeMainEndToEnd(t *testing.T) {
	csd := z80h.CSDForV2(0x01E8FF) // 64GB SDHC, the card shape Pete's hardware run used

	mac := loadCSDProbeMain(t)
	enc := z80h.NewENC28J60()
	enc.ProgramTrinityNetwork(pmSAMMac, pmSAMIp) // so find_index/read_chunk succeed
	enc.AttachSD(csd)                            // so csd_read_into_stage drives a real CSD
	mac.AttachIO(enc)

	// Pre-queue the serve traffic. probe_main provisions + reads the CSD + inits the
	// ENC, then enters pm_serve_loop; the loop drains these in order:
	//   ARP request          -> ARP reply
	//   bare RRQ for csd.bin  -> DATA block 1 (the 16 CSD bytes, a short final block)
	//   ACK of block 1        -> transfer complete (no further TX)
	enc.InjectRX(pmARP())
	enc.InjectRX(pmRRQ("csd.bin"))
	enc.InjectRX(pmACK(1))

	in := newPMInstrument(t, mac)

	// A hard step cap: probe_main's serve loop runs forever (Esc-to-exit), so RunBoot
	// returns Halted=false at the cap once the queued frames are drained — that is
	// the expected non-hang outcome. A spin INSIDE the SD/ENC I/O would also surface
	// here as Halted=false, but with res.PC inside that routine (the diagnostic).
	res, err := mac.RunBoot("probe_main", z80h.Entry{
		StepCap: 8_000_000,
		Trace:   in.trace,
	})
	if err != nil {
		t.Fatalf("RunBoot probe_main faulted (undecodable instruction / bad symbol): %v", err)
	}

	border, borderWritten := enc.LastBorder()
	t.Logf("probe_main: halted=%v finalPC=&%04X steps=%d tstates=%d border=%d(written=%v) tx=%d",
		res.Halted, res.PC, res.Steps, res.TStates, border, borderWritten, len(enc.TXFrames()))
	t.Logf("milestone order: %v", in.order)
	t.Logf("hit counts: probe_main=%d find_index=%d read_chunk=%d probe_provision=%d csd_read=%d csd_deselect=%d drv_init=%d pm_serve_loop=%d serve_once=%d fail_cfg=%d fail_init=%d",
		in.count("probe_main"), in.count("find_index"), in.count("read_chunk"),
		in.count("probe_provision"), in.count("csd_read_into_stage"), in.count("csd_deselect"),
		in.count("drv_init"), in.count("pm_serve_loop"), in.count("serve_serve_once"),
		in.count("pm_fail_cfg"), in.count("pm_fail_init"))

	// --- Stage-by-stage diagnosis -------------------------------------------------

	// (A) The config read must have run and NOT hit pm_fail_cfg (border 2 = red).
	if in.count("read_chunk") == 0 {
		t.Errorf("read_chunk never ran — find_index did not match the 'Trinity Network ' chunk")
	}
	if in.count("pm_fail_cfg") > 0 {
		t.Errorf("probe_main hit pm_fail_cfg (border 2 / red) — the EEPROM config read failed")
	}

	// (B) The SD read must have RUN and COMPLETED (reached csd_deselect, its tail),
	// not spun inside the init ladder / token poll. If csd_read ran but csd_deselect
	// did not, the SD read spun — the prime hang suspect.
	if in.count("csd_read_into_stage") == 0 {
		t.Errorf("csd_read_into_stage never ran — probe_main spun before the SD read (PC=&%04X)", res.PC)
	} else if in.count("csd_deselect") == 0 {
		t.Errorf("csd_read_into_stage ran but never reached csd_deselect — the SD read SPUN "+
			"(final PC=&%04X). This is the interleaved-I/O hang suspect.", res.PC)
	}

	// (C) drv_init must have run and NOT hit pm_fail_init (border 1 = blue).
	if in.count("drv_init") == 0 {
		t.Errorf("drv_init never ran — probe_main spun between the SD read and the ENC init (PC=&%04X)", res.PC)
	}
	if in.count("pm_fail_init") > 0 {
		t.Errorf("probe_main hit pm_fail_init (border 1 / blue) — drv_init returned BC=0 (ENC init failed)")
	}

	// (D) The serve loop must have been reached.
	if in.count("pm_serve_loop") == 0 {
		t.Errorf("pm_serve_loop never reached — probe_main never entered the serve loop (final PC=&%04X)", res.PC)
	}

	// (E) If we halted, the probe hit a fail path (di;halt) — never expected here.
	if res.Halted {
		t.Fatalf("probe_main HALTED at PC=&%04X border=%d — a fail path (pm_fail_cfg=red/2 or "+
			"pm_fail_init=blue/1) ran; the run should spin in the serve loop, not halt", res.PC, border)
	}

	// --- Serve assertions: ARP answered + CSD streamed ----------------------------

	tx := enc.TXFrames()
	if len(tx) < 2 {
		t.Fatalf("probe_main transmitted %d frame(s), want >= 2 (ARP reply + the csd.bin DATA block). "+
			"Fewer means the serve loop did not run / the interleaved path stalled (final PC=&%04X, border=%d)",
			len(tx), res.PC, border)
	}

	// Frame 0: an ARP reply (opcode 2) sourced from the SAM's MAC.
	if !isARPReply(tx[0]) {
		t.Errorf("first TX frame is not an ARP reply: % x", tx[0])
	}

	// Find the csd.bin DATA frame among the TX and check it carries the 16 CSD bytes.
	var streamed []byte
	for _, f := range tx {
		u, ok := frame.ParseUDP(f)
		if !ok {
			continue
		}
		if _, payload, perr := tftp.ParseDATA(u.Payload); perr == nil {
			streamed = append(streamed, payload...)
		}
	}
	if len(streamed) != 16 {
		t.Fatalf("probe_main streamed %d CSD byte(s), want 16 (the csd.bin DATA payload). tx frames=%d",
			len(streamed), len(tx))
	}
	if !bytes.Equal(streamed, csd[:]) {
		t.Fatalf("served csd.bin = % 02x, configured CSD = % 02x", streamed, csd[:])
	}

	t.Logf("END-TO-END PASS: probe_main read config -> read CSD -> drv_init -> served ARP + the 16-byte CSD")
}

// isARPReply reports whether f is an Ethernet ARP reply (EtherType 0x0806, ARP
// opcode 2). Used to confirm the serve loop answered the injected ARP request.
func isARPReply(f []byte) bool {
	if len(f) < 22 {
		return false
	}
	if f[12] != 0x08 || f[13] != 0x06 {
		return false // not ARP
	}
	return f[20] == 0x00 && f[21] == 0x02 // ARP opcode = reply
}
