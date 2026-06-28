// sd_record_seek_trap_test.go — i299: ISOLATE where B-DOS 1.5t actually reads a
// record's body, with NO guessing. Boots real B-DOS 1.5t against Pete's REAL 64 GB
// CSD, then runs RECORD 3 / 12 / 13 and traps the exact CMD17 block LBAs B-DOS issues.
// That is B-DOS *defining* the record->LBA mapping. We compare to our formula
// (base + 1600*(n-1)); any divergence (an offset, a different base, a different stride)
// is the create-record bug — and it tells us EXACTLY where sd_push must write + where
// the probe must read.
package z80_test

import (
	"fmt"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

func TestRecordSeekLBAReal64GB(t *testing.T) {
	const realCSize = 0x01DBD3 // Pete's 64 GB card (blocks=124,735,488)

	rom, eeprom := loadRealCaptures(t)
	mac := z80h.New()
	if err := mac.LoadROMImage(rom); err != nil {
		t.Fatalf("load ROM: %v", err)
	}
	mac.Pager().LMPR = bootLMPR
	mac.Pager().HMPR = bootHMPR
	enc := z80h.NewENC28J60()
	enc.LoadEEPROMImage(deviceLinearEEPROM(eeprom))
	sd := enc.AttachSD(csdV2(realCSize)) // the REAL 64 GB card geometry

	// Seed a record-1 selection stamp at the 64GB base (2438) so any boot-time default
	// record select finds a valid directory (mirrors bootMountTestMachine's 152 seed).
	rec1 := make([]byte, 512)
	copy(rec1[210:220], []byte("TRINITY1  "))
	copy(rec1[232:236], []byte("BDOS"))
	sd.SeedSector(2438, rec1)

	var lastPC uint16
	lg := &[]capEv{}
	mac.AttachIO(&capIO{inner: enc, lastPC: &lastPC, log: lg})
	res, err := mac.RunBootFrom(0x0000, z80h.Entry{
		StepCap: realBootStepCap, StopPC: addrEditorIdle, StopPCSkip: 16,
		Trace: func(pc uint16) { lastPC = pc },
	})
	if err != nil || !res.ReachedStop {
		t.Fatalf("boot with 64GB CSD did not reach editor idle: %v reached=%v PC=&%04X", err, res.ReachedStop, res.PC)
	}
	dosPage := mac.Pager().HMPR & 0x1F
	base := rdDOS16(mac, dosPage, 0x80C2)
	lastrec := rdDOS16(mac, dosPage, 0x80C4)
	t.Logf("64GB CSD: B-DOS base(&80C2)=%d  last.record(&80C4)=%d (16-bit)", base, lastrec)

	for _, n := range []int{1, 3, 12, 13} {
		_, cmd17 := runCmdLine(mac, lg, fmt.Sprintf("RECORD %d", n))
		want := int(base) + 1600*(n-1)
		t.Logf("RECORD %2d -> CMD17 read LBAs %v   (our formula base+1600*(n-1) = %d)", n, cmd17, want)
	}
	t.Log("If the trapped LBAs == our formula, the math is right and the probe's SD read interface is the suspect; if they differ, the divergence IS the bug.")
}
