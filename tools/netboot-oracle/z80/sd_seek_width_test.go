// sd_seek_width_test.go — i295 DATA-SAFETY analysis of B-DOS 1.5t's record->LBA seek
// arithmetic `base + 1600*(N-1)`, established from the ACTUAL register/store widths of
// the real B-DOS binary (not inference). The question (coordinator, i295): can B-DOS's
// seek ever WRAP below base — landing a record body write in the record-LIST region
// (sectors 1..base-1) or sector 0 — i.e. is B-DOS 1.5t itself data-unsafe at some SD
// card size?
//
// The seek is computed at &A100..&A112:
//
//	a0fd: ld bc,0x0640            ; 1600
//	a100: call &A113              ; mult16-32: BC:HL = 1600 * (N-1)     (32-bit product)
//	a103: ld de,(&80C2)           ; DE = base                          (16-bit load)
//	a107: add hl,de               ; low16 += base
//	a108: jr nc,a10b / a10a: inc bc ; carry into high16                (=> 32-bit add)
//	a10b: ld (&A185),hl           ; poke seek immediate LOW  16 bits
//	a10e: ld (&A188),bc           ; poke seek immediate HIGH 16 bits   (=> 32-bit seek base)
//
// WIDTHS (deterministic):
//  1. mult16-32 (&A113): TRUE 32-bit — TestSeekMult16x32IsGenuine32Bit below runs the
//     real routine and shows 1600*M is exact for M up to 65535 (incl. M=10486, past
//     2^24 where a 24-bit intermediate would overflow, and M=65535). No 24-bit trap.
//  2. base (&80C2): a 16-bit store/load — BUT records1 is CLAMPED to <=65536 by the
//     &A452 mechanism (see TestBDOSRecordsMathBase64GB), so base = (records1+32)/32+1
//     <= 2050 for ANY card. base can never approach 2^16, so it never wraps.
//  3. the +base add and the &A185/&A188 poke: 32-bit throughout.
//  4. N (the record number) is bounded by last.record (&80C4), a 16-bit value, so
//     N-1 <= 65534.
//
// CONCLUSION (stated deterministically with the numbers): the maximum seek base B-DOS
// 1.5t can ever compute is base + 1600*(N-1) <= 2050 + 1600*65534 = 2050 + 104,854,400
// = 104,856,450 (0x063FFB82), which is < 2^27 and far below the 32-bit seek's 2^32.
// Because base is clamped <=2050 and N is 16-bit-bounded, the 32-bit seek NEVER wraps
// below base for ANY SD card size. B-DOS 1.5t's record->LBA seek is data-SAFE by
// construction — the same 16-bit records clamp that fixes our base also bounds the
// seek. (This is an analysis of B-DOS itself; OUR sd_push write path adds its own
// defensive band guard regardless — see TestRecordWriteRefusesOutOfBand.)
package z80_test

import (
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

// TestSeekMult16x32IsGenuine32Bit runs B-DOS 1.5t's REAL seek multiply (&A113) on a
// spread of multipliers up to the 16-bit maximum and asserts the product is the exact
// 32-bit 1600*M — proving there is no narrower (e.g. 24-bit) intermediate that would
// overflow and wrap the seek. Entry per the &A100 caller: HL = M, BC = 1600; the
// routine returns the product in BC:HL (high16 in BC, low16 in HL).
func TestSeekMult16x32IsGenuine32Bit(t *testing.T) {
	mac := loadBdosInSectionB(t)
	const entry = uint16(0xA113 - 0x4000) // &6113 (section-B alias)

	// Multipliers chosen to straddle the byte/word/24-bit boundaries: M=10486 makes
	// 1600*M = 16,777,600 > 2^24 (0x01000180) — a 24-bit product would drop the top
	// bit; M=65534/65535 are the max record-number multipliers for the 64 GB card.
	for _, m := range []uint32{0, 1, 100, 1023, 10485, 10486, 40000, 65534, 65535} {
		res, err := mac.RunFrom(entry, z80h.Entry{HL: uint16(m), BC: 1600})
		if err != nil {
			t.Fatalf("M=%d: %v", m, err)
		}
		if !res.Halted {
			t.Fatalf("M=%d: mult did not RET cleanly (PC=&%04X)", m, res.PC)
		}
		got := uint32(res.BC)<<16 | uint32(res.HL)
		want := 1600 * m
		if got != want {
			t.Fatalf("1600*%d: &A113 returned %d (0x%08X), want %d — the multiply is NARROWER than 32-bit and OVERFLOWS (the seek could wrap)", m, got, got, want)
		}
		t.Logf("1600*%-6d = %d (0x%08X)  [&A113 exact 32-bit]", m, got, got)
	}

	// The seek-safety conclusion, with the numbers, as a guarded invariant: max seek =
	// max base (2050, clamped) + 1600 * max (N-1) (65534) must stay well under 2^32 and
	// strictly above base (no wrap).
	const maxBase = 2050
	const maxNm1 = 65534
	maxSeek := uint64(maxBase) + 1600*uint64(maxNm1)
	if maxSeek >= 1<<32 {
		t.Fatalf("max seek %d >= 2^32 — B-DOS's 32-bit seek could overflow", maxSeek)
	}
	if maxSeek < maxBase {
		t.Fatalf("max seek %d < base %d — wrap possible", maxSeek, maxBase)
	}
	t.Logf("SEEK-SAFETY: max seek = %d (base<=2050 clamped, N<=65535 16-bit) < 2^27 << 2^32, > base "+
		"=> B-DOS 1.5t's seek never wraps below base for any card size (data-safe by construction).", maxSeek)
}
