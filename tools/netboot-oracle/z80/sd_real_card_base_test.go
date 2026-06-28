// sd_real_card_base_test.go — i299: run REAL B-DOS 1.5t's CSD-decode + records-math
// on Pete's ACTUAL 64 GB card CSD, and read the `base` (record-body start) it computes
// — then compare to what sd_push's csd_set_bd_records computes for the same CSD. If they
// differ, sd_push placed record bodies at the wrong LBA (the RECORD 13 "Invalid" cause).
//
// Pete's real card CSD (read live via the probe): 40 0e 00 32 5b 59 00 01 db d3 7f 80 0a 40 40 df
//   CSD_STRUCTURE=01 (v2), C_SIZE = (0x01<<16)|(0xdb<<8)|0xd3 = 0x01DBD3 = 121811
//   blocks = (C_SIZE+1)*1024 = 124,735,488 (~63 GB)
package z80_test

import "testing"

func TestRealCardBaseBDOSvsSdPush(t *testing.T) {
	const realCSize = 0x01DBD3 // Pete's 64 GB card

	// --- 1. REAL B-DOS 1.5t: decode the CSD + run the records-math, read base/records. ---
	mac := loadBdosInSectionB(t)
	csd := csdV2(realCSize)
	blocks, dres := runColinDecode(t, mac, csd, 3) // cardType 3 => v2/SDHC branch
	if !dres.Halted {
		t.Fatalf("B-DOS CSD decode did not RET cleanly (PC=&%04X)", dres.PC)
	}
	base, records, rres := runColinRecords(t, mac, blocks)
	if !rres.ReachedStop {
		t.Fatalf("B-DOS records-math did not reach the store (PC=&%04X)", rres.PC)
	}
	_, fullRecords := refRecordsFull(blocks)
	t.Logf("REAL B-DOS 1.5t on the 64GB CSD: blocks=%d  base(&80C2)=%d  BD_RECORDS(&80C4)=%d (16-bit; true=%d)",
		blocks, base, records, fullRecords)

	// record n body base sector (the seek poke = (n-1)*1600 + base):
	for _, n := range []int{1, 3, 12, 13} {
		t.Logf("  B-DOS record %d body base LBA = (%d-1)*1600 + %d = %d", n, n, base, (n-1)*1600+int(base))
	}

	// --- 2. The host CSD decode sd_push relies on (refBlocksV2/refRecords). ---
	goBlocks := refBlocksV2(realCSize)
	goBase, goRecords := refRecords(goBlocks)
	t.Logf("Go reference (what sd_push's csd_set_bd_records mirrors): blocks=%d base=%d records=%d", goBlocks, goBase, goRecords)

	// sd_push wrote cj (record 13) at csd_base + 1600*12. Report the mismatch, if any.
	t.Logf("sd_push wrote record 13 body at csd_base+1600*12 = %d ; B-DOS reads record 13 at %d", int(goBase)+1600*12, (13-1)*1600+int(base))

	if base != goBase {
		t.Errorf("BASE MISMATCH: real B-DOS computes base=%d, sd_push/refRecords computes %d — sd_push placed record bodies at the WRONG LBA", base, goBase)
	} else {
		t.Logf("base AGREES (%d): the computed base is identical; the RECORD 13 failure is NOT a base-computation mismatch — the real card's records must sit at a base B-DOS did NOT compute from this CSD (stored/older format), or the probe LBAs need re-checking against where B-DOS actually reads.", base)
	}
}
