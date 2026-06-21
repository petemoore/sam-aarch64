// eeprom_roundtrip_test.go — emulation verification of the non-destructive
// Trinity EEPROM write round-trip test payload (item i225) and the reusable
// test_report-over-network primitive it is the first client of.
//
// The payload (src/netboot/eeprom_roundtrip_standalone.asm) reads a free scratch
// chunk, writes a position-sensitive pattern, reads it back and verifies, then
// restores the original bytes and re-verifies — exercising the REAL eeprom.asm
// write path (write_chunk -> write_256, the path that has never run on hardware)
// against the faithful 25LC1024 write model (eeprom.go, i221). It reports the
// outcome by transmitting a "SATR" UDP packet via the real ENC driver AND
// painting the border, so the SAME binary reports identically here and on real
// hardware. These tests read the result straight off the transmitted frame —
// exactly how an agent reads a hardware run off a UDP listener.
//
// TestEEPROMRoundTripPass: the write path works -> status PASS, border green, the
// scratch chunk is restored to its original bytes.
// TestEEPROMRoundTripWriteFaultReportsFail: with the EEPROM write path faulted
// (SetEEPROMWriteFault), the read-back differs -> the payload reports FAIL, border
// red — the negative control proving the test can actually fail.
package z80_test

import (
	"bytes"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	emRTBin = "../../../build/eeprom_roundtrip.bin"
	emRTMap = "../../../build/eeprom_roundtrip.map"

	// emRTScratchDev is the device byte address of scratch chunk 20:
	// get_chunk maps n to (28 + n*4)<<8, so chunk 20 = 108<<8 = 0x6C00.
	emRTScratchDev = 0x6C00
	emRTScratchEnd = emRTScratchDev + 1024
	emRTMarker     = 0xAB // a recognizable original byte in the scratch chunk
)

// satrReport is a decoded "SATR" test-report record (test_report.asm).
type satrReport struct {
	version byte
	testID  uint16
	status  byte
	detail  []byte
}

// parseSATR finds the first transmitted frame carrying a "SATR" report and
// decodes it. It scans for the magic rather than assuming a fixed offset, so it
// is robust to the exact Ethernet/IP/UDP header size.
func parseSATR(frames [][]byte) (satrReport, bool) {
	for _, f := range frames {
		i := bytes.Index(f, []byte("SATR"))
		if i < 0 {
			continue
		}
		p := f[i:]
		if len(p) < 9 {
			continue
		}
		dlen := int(p[8])
		if len(p) < 9+dlen {
			continue
		}
		return satrReport{
			version: p[4],
			testID:  uint16(p[5]) | uint16(p[6])<<8,
			status:  p[7],
			detail:  append([]byte(nil), p[9:9+dlen]...),
		}, true
	}
	return satrReport{}, false
}

// loadEEPROMRoundtrip loads the payload and an ENC28J60 whose flash EEPROM is
// seeded with a marker byte in the scratch chunk (the rest 0xFF, an unprogrammed
// device), so the round-trip's backup/restore has real original content to
// preserve.
func loadEEPROMRoundtrip(t *testing.T) (*z80h.Machine, *z80h.ENC28J60) {
	t.Helper()
	mac, err := z80h.Load(emRTBin, emRTMap)
	if err != nil {
		t.Skipf("eeprom_roundtrip not built (%s); run `make netboot-eeprom-roundtrip`: %v", emRTBin, err)
	}
	enc := z80h.NewENC28J60()
	img := make([]byte, 131072)
	for i := range img {
		img[i] = 0xFF
	}
	for i := emRTScratchDev; i < emRTScratchEnd; i++ {
		img[i] = emRTMarker
	}
	enc.LoadEEPROMImage(img)
	mac.AttachIO(enc)
	return mac, enc
}

func TestEEPROMRoundTripPass(t *testing.T) {
	mac, enc := loadEEPROMRoundtrip(t)

	res, err := mac.Call("eeprom_roundtrip_main")
	if err != nil {
		t.Fatalf("run eeprom_roundtrip_main: %v", err)
	}
	if !res.Halted {
		t.Fatalf("payload did not halt (PC=&%04X)", res.PC)
	}

	rep, ok := parseSATR(enc.TXFrames())
	if !ok {
		t.Fatalf("no SATR report frame was transmitted (%d frames)", len(enc.TXFrames()))
	}
	if rep.version != 1 {
		t.Errorf("report version = %d, want 1", rep.version)
	}
	if rep.testID != 1 {
		t.Errorf("report test_id = %d, want 1 (EEPROM round-trip)", rep.testID)
	}
	if rep.status != 0 {
		t.Errorf("report status = %d, want 0 (PASS); detail = %v", rep.status, rep.detail)
	}
	if len(rep.detail) >= 2 {
		if rep.detail[0] != 20 {
			t.Errorf("report scratch chunk = %d, want 20", rep.detail[0])
		}
		if rep.detail[1] != 0 {
			t.Errorf("report fail-phase = %d, want 0 (no failure)", rep.detail[1])
		}
	}

	if b, written := enc.LastBorder(); !written || b != 4 {
		t.Errorf("border = %d (written=%v), want 4 (green = pass)", b, written)
	}

	// The scratch chunk must be restored to its original marker bytes.
	img := enc.EEPROMImage()
	for i := emRTScratchDev; i < emRTScratchEnd; i++ {
		if img[i] != emRTMarker {
			t.Errorf("scratch byte device &%05X = &%02X, want &%02X — restore failed", i, img[i], emRTMarker)
			break
		}
	}
}

func TestEEPROMRoundTripWriteFaultReportsFail(t *testing.T) {
	mac, enc := loadEEPROMRoundtrip(t)
	enc.SetEEPROMWriteFault(true) // simulate a dead write path

	res, err := mac.Call("eeprom_roundtrip_main")
	if err != nil {
		t.Fatalf("run eeprom_roundtrip_main: %v", err)
	}
	if !res.Halted {
		t.Fatalf("payload did not halt (PC=&%04X)", res.PC)
	}

	rep, ok := parseSATR(enc.TXFrames())
	if !ok {
		t.Fatalf("no SATR report frame was transmitted")
	}
	if rep.status == 0 {
		t.Errorf("report status = 0 (PASS) but the write path was faulted — the test must report FAIL")
	}
	if len(rep.detail) >= 2 && rep.detail[1] != 1 {
		t.Errorf("report fail-phase = %d, want 1 (read-back after the test write)", rep.detail[1])
	}
	if b, _ := enc.LastBorder(); b != 2 {
		t.Errorf("border = %d, want 2 (red = fail)", b)
	}

	// With writes dropped, the scratch chunk must be untouched (still the marker).
	img := enc.EEPROMImage()
	if img[emRTScratchDev] != emRTMarker {
		t.Errorf("scratch byte device &%05X = &%02X, want &%02X — a write stuck despite the fault", emRTScratchDev, img[emRTScratchDev], emRTMarker)
	}
}
