// boot_record_faithful_test.go — the i328 hardware-bounce repro: boot_record's
// HRECORD+ALHK boot returned to trinload on the real SAM (both for a freshly
// stored record and for the i302-proven cjfixed record), so the tool — whose
// i316 verification drove the RST 8 dispatch against the flat BDOSStore MOCK —
// has a real-B-DOS dispatch gap. This file drives bdos_boot_record through
// Colin's REAL ROM + B-DOS (private captures; skips only under
// SKIP_PRIVATE_TESTS) in the two dispatch states, using the seek-base immediate
// &A185 in the resident B-DOS page as the HRECORD observable (the same FACT-3
// instrument as sd_push_faithful_test.go):
//
//   * UNARMED (DOSCNT as the editor idle leaves it) and ARMED (DOSCNT=0, the
//     external-caller state) BOTH dispatch correctly here: HRECORD redirects the
//     seek base to the requested record, and ALHK's AUTO search reads the
//     SELECTED record's directory. That ELIMINATES the &37E8 recursion-guard /
//     DOSCNT class and the selection-not-honoured class as the i328 cause — at
//     editor idle. The real-SAM bounce therefore lives in the TRINLOAD-idle
//     context, which no faithful rig reaches yet (the remaining i328 work).
package z80_test

import (
	"os"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

// loadBootRecordIntoPage mirrors loadSDPushIntoPage for boot_record.bin.
func loadBootRecordIntoPage(t *testing.T, mac *z80h.Machine, page uint8) {
	t.Helper()
	code := mustReadFile(t, bootRecordBin, "make netboot-boot-record")
	if err := mac.LoadSymbols(bootRecordMap); err != nil {
		t.Fatalf("load boot_record symbols: %v", err)
	}
	sH := mac.Pager().HMPR
	mac.Pager().HMPR = page
	mac.Write(0x8000, code)
	mac.Pager().HMPR = sH
}

// driveBootRecord runs bdos_boot_record for `record` from the given dispatch
// state and returns the &A185 seek-base immediate before/after, the CMD17 read
// addresses ALHK issued (WHICH record's directory the AUTO search actually
// scanned — the i328 observable), and the &37E8 recursion-guard hit count.
func driveBootRecord(t *testing.T, arm bool, record byte) (before, after uint16, reads []uint32, guardHits int) {
	t.Helper()
	mac, lg, _ := bootToEditorIdleSD(t)
	const page = 1
	loadBootRecordIntoPage(t, mac, page)
	mac.Pager().HMPR = page

	sym := func(name string) uint16 {
		a, err := mac.Sym(name)
		if err != nil {
			t.Fatalf("boot_record symbol %q absent from %s", name, bootRecordMap)
		}
		return a
	}
	mac.Write(sym("BD_BOOT_RECORD"), []byte{record})

	if arm {
		armServeDispatch(mac, page)
	}
	mac.Pager().HMPR = page

	before = leU16(rdDOSPage(mac, 0xA185, 2))
	from := len(*lg)
	res, err := mac.RunBootFrom(sym("bdos_boot_record"), z80h.Entry{
		StepCap: 20_000_000,
		Trace: func(pc uint16) {
			if pc == 0x37E8 {
				guardHits++
			}
		},
	})
	if err != nil {
		t.Fatalf("bdos_boot_record faulted: %v (PC=&%04X)", err, res.PC)
	}
	after = leU16(rdDOSPage(mac, 0xA185, 2))
	frames, _ := decodeFrames(*lg, from)
	for _, f := range frames {
		if f.cmd == 17 {
			reads = append(reads, f.addr)
		}
	}
	t.Logf("arm=%v record=%d: seek-base &A185 %d -> %d, &37E8 guard hits=%d, CMD17 reads=%v, finalPC=&%04X halted=%v steps=%d",
		arm, record, before, after, guardHits, reads, res.PC, res.Halted, res.Steps)
	return before, after, reads, guardHits
}

// TestBootRecordDispatchUnarmedVsArmed — the i328 discriminator. Record 2's base
// under the 4809-record CSD is 152+1600 = 1752.
func TestBootRecordDispatchUnarmedVsArmed(t *testing.T) {
	const record = 2
	const wantBase = faithCSDBase + 1600*(record-1) // 1752

	_, afterUnarmed, readsUnarmed, _ := driveBootRecord(t, false, record)
	_, afterArmed, readsArmed, _ := driveBootRecord(t, true, record)

	if afterArmed != wantBase {
		t.Errorf("ARMED dispatch: HRECORD did not redirect the seek base (&A185=%d, want %d) — the armed path itself is broken", afterArmed, wantBase)
	}
	// The i328 observable: WHICH record's directory did ALHK's AUTO search scan?
	// Reads in [1752, 1752+1600) = the SELECTED record 2 (selection honoured);
	// reads in [152, 1752) = record 1 / the pre-selection base (selection ignored
	// — the class that would re-boot trinload's own record on hardware).
	classify := func(name string, reads []uint32) {
		sel, unsel := 0, 0
		for _, a := range reads {
			switch {
			case a >= wantBase && a < wantBase+1600:
				sel++
			case a >= faithCSDBase && a < wantBase:
				unsel++
			}
		}
		t.Logf("%s: %d reads in the SELECTED record's band, %d in the unselected/default band", name, sel, unsel)
		if sel == 0 && unsel > 0 {
			t.Errorf("%s: ALHK's AUTO search read ONLY the unselected/default record's band %v — the HRECORD selection is not honoured by the auto-load (the i328 hardware bounce class)", name, reads)
		}
		if len(reads) == 0 {
			t.Errorf("%s: ALHK issued no CMD17 reads at all — the auto-load never reached the SD driver", name)
		}
	}
	classify("unarmed", readsUnarmed)
	classify("armed", readsArmed)
	_ = afterUnarmed
}

// mustReadFile reads a build artifact, failing (not skipping) when missing.
func mustReadFile(t *testing.T, path, buildHint string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s not built (%v); run `%s`", path, err, buildHint)
	}
	return b
}
