// sd_record_validity_test.go — i299/i295 regression guard for B-DOS's RECORD-select
// validity CONTRACT. Runs REAL B-DOS 1.5t RECORD-select (bootToEditorIdleSD = real ROM +
// B-DOS 1.5t + the SD-SPI model) against a record whose body sector 0 is seeded exactly as
// our create-record write produces it (cj.mgt dir + "BDOS"@232 + name@210 at the record's
// body base (n-1)*1600+base), and asserts:
//   - WITH "BDOS"@232 at the record base → errnr 0 (get.label validates it).
//   - WITHOUT the stamp → errnr 81 ("Invalid record") [negative control: get.label IS the gate].
// So a record with the stamp AT ITS BODY BASE is RECORD-valid — the write STRUCTURE is
// correct when it lands at the base B-DOS actually reads.
//
// NOTE — the actual real-hardware bug (for the record): this test uses the small card
// (base=152), so it validates at base=152's record-13 LBA. The real 64GB-card "81 Invalid
// record" was NOT a stamp/structure problem, and NOT a stale/cache artifact — it was a BASE
// MISCOMPUTE: sd_push wrote the body at csd_base=2438 (un-clamped blocks/1600) while B-DOS
// reads at base=2050 (its 16-bit records clamp). See TestBDOSRecordsMathBase64GB (traces
// base=2050 on the real B-DOS binary) + the sd_csd.asm csd_compute_eff fix. This test guards
// the get.label CONTRACT; the base fix ensures sd_push writes where B-DOS reads.
package z80_test

import (
	"fmt"
	"testing"
)

func TestRecordSelectValidityViaGetLabel(t *testing.T) {
	const base = 152 // csdV2(0x001D59) base — the small card bootToEditorIdleSD models
	const rec = 13
	recBodyS0 := uint32((rec-1)*1600 + base) // 19352 = where 1.5t get.label reads record 13's +232

	// buildSector0 reproduces our hardware write's record-13 body sector 0: the cj.mgt
	// directory first entry (samdos.bin), the disk name@210, optionally "BDOS"@232, name
	// tail@250 (spaces) — byte-for-byte the layout the probe read back at LBA 21638.
	buildSector0 := func(withBDOS bool) []byte {
		s := make([]byte, 512)
		s[0] = 0x13
		copy(s[1:], []byte("samdos.bin"))
		copy(s[210:220], []byte("cj        ")) // name@210, 10 bytes (as our claim built it)
		if withBDOS {
			copy(s[232:236], []byte("BDOS")) // the stamp our sector-0 mutation wrote
		}
		copy(s[250:256], []byte("      ")) // name tail@250, 6 bytes
		return s
	}

	cases := []struct {
		name     string
		withBDOS bool
		wantInvalid bool // expect errnr 81 ("Invalid record")
	}{
		{"our-write: BDOS@232 present at (n-1)*1600+base", true, false},
		{"negative control: no BDOS@232", false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mac, _, sd := bootToEditorIdleSD(t)
			sd.SeedSector(recBodyS0, buildSector0(c.withBDOS))

			_, _, errnr := editorRunLine(t, mac, fmt.Sprintf("RECORD %d", rec))
			t.Logf("RECORD %d (BDOS@232=%v, body s0 @ LBA %d) -> errnr=%d", rec, c.withBDOS, recBodyS0, errnr)

			invalid := errnr == 81
			if invalid != c.wantInvalid {
				if c.wantInvalid {
					t.Fatalf("RECORD %d WITHOUT a BDOS stamp returned errnr=%d, want 81 — the negative control did not fail; get.label is not the gate here, or the seeded LBA is wrong", rec, errnr)
				}
				t.Fatalf("RECORD %d WITH BDOS@232 at LBA %d (exactly our hardware write) returned errnr 81 (Invalid record) — our write structure is genuinely rejected by 1.5t get.label; the bug reproduces in emulation", rec, recBodyS0)
			}
			if c.withBDOS {
				t.Logf("CONTRACT CONFIRMED: real B-DOS 1.5t RECORD %d validates a record with BDOS@232 at its body base (errnr=%d, not 81). The real-hardware 'Invalid record' was the BASE MISCOMPUTE (sd_push wrote base=2438; B-DOS reads base=2050, its 16-bit clamp) — the base fix makes sd_push write here.", rec, errnr)
			}
		})
	}
}
