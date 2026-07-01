// sd_real_card_base_test.go — i295 regression guard for THE create-record bug.
//
// THE BUG (empirically proven on Pete's real 64 GB card): sd_push wrote record bodies
// at csd_base + 1600*(n-1) with csd_base = 2438 (the un-clamped blocks/1600 = 77959 ->
// base 2438). But B-DOS 1.5t's HDINIT applies a 16-BIT-OVERFLOW CLAMP to the record
// count, so ITS base for this card is 2050 — reading the real card at 2050+1600*(n-1)
// found valid "BDOS"-stamped records (rec1@2050, rec3@5250, rec12@19650), while the
// 2438-based LBAs read all-zeros. sd_push therefore wrote to LBAs B-DOS never looks at
// (and, being 388 sectors high, spilled into the NEXT record) -> "81 Invalid record".
//
// THE MECHANISM (traced live on the real B-DOS 1.5t binary — TestBDOSRecordsMathBase64GB
// in sd_record_seek_trap_test.go and the isolated core here): HDINIT feeds its records
// math an EFFECTIVE dividend (bdosEffBlocks): blocks+1, then — when high16(blocks+1) >=
// 1600 (blocks >= ~104.86M) — a synthetic 0x064001C1 = 104,858,049 (&A452 substitution),
// which /1600 = exactly 65536. records1 saturates at 65536, so base ceilings at
// (65536+32)/32+1 = 2050. This is B-DOS's inherent 16-bit records limit.
//
// THE FIX: sd_csd.asm's csd_compute_eff mirrors bdosEffBlocks, so the Z80 csd_base ==
// B-DOS's base (2050) for the 64 GB card, and is unchanged for small cards. This test
// asserts B-DOS's base and the Go reference AGREE at 2050 (they used to both compute the
// WRONG 2438 because the isolated core was fed raw blocks, skipping the clamp).
package z80_test

import "testing"

func TestRealCardBaseBDOSvsSdPush(t *testing.T) {
	const realCSize = 0x01DBD3 // Pete's 64 GB card: blocks = (0x01DBD3+1)*1024 = 124,735,488
	const wantBase = 2050      // B-DOS's clamped base (empirically proven on the real card)

	blocks := refBlocksV2(realCSize)
	if blocks != 124735488 {
		t.Fatalf("refBlocksV2(0x%X) = %d, want 124735488", realCSize, blocks)
	}

	// --- 1. REAL B-DOS 1.5t core, fed the EFFECTIVE dividend B-DOS's HDINIT hands it
	// (bdosEffBlocks = the &A340 +1 and the &A452 16-bit clamp). This reproduces the
	// live-boot result (base=2050) — see TestBDOSRecordsMathBase64GB for the full-boot
	// trace that PROVES bdosEffBlocks is what B-DOS computes. ---
	mac := loadBdosInSectionB(t)
	eff := bdosEffBlocks(blocks)
	base, records, rres := runColinRecords(t, mac, eff)
	if !rres.ReachedStop {
		t.Fatalf("B-DOS records-math did not reach the store (PC=&%04X)", rres.PC)
	}
	_, fullRecords := refRecordsFull(blocks)
	t.Logf("REAL B-DOS 1.5t core on the 64GB CSD: blocks=%d -> eff(clamped)=%d  base(&80C2)=%d  BD_RECORDS(&80C4)=%d (full=%d)",
		blocks, eff, base, records, fullRecords)
	if base != wantBase {
		t.Fatalf("B-DOS base=%d, want %d (the empirically-proven clamped base)", base, wantBase)
	}

	// record n body base sector = base + 1600*(n-1):
	for _, n := range []int{1, 3, 12, 13} {
		t.Logf("  B-DOS record %d body base LBA = %d + 1600*(%d-1) = %d", n, base, n, (n-1)*1600+int(base))
	}

	// --- 2. The Go reference sd_push's csd_set_bd_records mirrors (refRecords, which now
	// applies the same clamp). It must AGREE with B-DOS. ---
	goBase, goRecords := refRecords(blocks)
	t.Logf("Go reference (what sd_push's csd_set_bd_records mirrors): base=%d records=%d", goBase, goRecords)

	if goBase != base {
		t.Fatalf("BASE MISMATCH: real B-DOS base=%d, Go reference/sd_push base=%d — sd_push would place record bodies at the WRONG LBA", base, goBase)
	}
	if goBase != wantBase {
		t.Fatalf("Go reference base=%d, want %d", goBase, wantBase)
	}
	t.Logf("base AGREES at %d: sd_push now writes record n at %d + 1600*(n-1) — exactly where B-DOS reads. "+
		"(record 13 body = %d, was %d with the old un-clamped 2438.)", base, base, 1600*12+base, 1600*12+2438)
}
