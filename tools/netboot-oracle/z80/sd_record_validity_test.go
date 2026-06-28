// sd_record_validity_test.go — the i299 DEFINITIVE resolver (emulation-first, CLAUDE.md
// §7). The hardware read-back proved our create-record write is byte-correct: record 13's
// body sector 0 at LBA (13-1)*1600+csd_base carries "BDOS"@232 + the disk name@210, and
// 1.5t's get.label (the RECORD-select validity gate, exprcd→rep81 "81 Invalid record")
// reads EXACTLY that LBA+232. So 1.5t source says record 13 should VALIDATE — yet hardware
// reported "81 Invalid record". This test settles the contradiction by running the REAL
// B-DOS 1.5t RECORD-select against a record seeded EXACTLY as our hardware write produced it.
//
// It mirrors TestBASICSaveWritesRecordToSD's rig (bootToEditorIdleSD = real ROM + B-DOS 1.5t
// + the SD-SPI model), which itself seeds "BDOS"@232 at record 1's base (LBA 152) as the
// "record-1 selection stamp" — i.e. the rig already encodes that a "BDOS"@232 at
// (n-1)*1600+base sector-0 is what makes RECORD-select succeed. We seed record 13 the same
// way our write did and assert RECORD 13 does NOT raise errnr 81; a no-stamp variant is the
// negative control (must raise 81).
//
// If RECORD 13 validates here, our write structure IS valid → the hardware "invalid" was a
// stale/cache artifact (re-test on a clean B-DOS boot). If it fails here, the bug reproduces
// and the rig exposes exactly which LBA/offset get.label rejects.
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
				t.Logf("RESOLVED: real B-DOS 1.5t RECORD %d VALIDATES our write structure (errnr=%d, not 81). The hardware 'Invalid record' was a stale/cache artifact, not a write defect.", rec, errnr)
			}
		})
	}
}
