// bdos_picker_test.go — i119d (B4): host-verification of the Trinity SD record-
// selection picker (src/netboot/bdos_picker.asm / bdos_pick_record).
//
// SAFETY CONTRACT (design §1; docs/specs/trinity-record-detection-design.md):
// The Trinity SD is a SHARED user resource. Before returning CONFIRMED=1 the
// picker MUST:
//   1. Show the chosen record's name in PICK_DISPLAY ("Record N: <name>"), AND
//   2. Read a 'y'/'Y' confirm key from the user.
// A path that sets BD_PICK_CONFIRMED=1 without both steps is a CRITICAL bug.
//
// Tests use the boot binary (cliBootBin / cliBootMap) via z80h.Load, consistent
// with the B3 tests (bdos_list_test.go): the picker routines call
// bdos_find_free_record / bdos_record_entry which are in the NETBOOT_HOSTTEST==0
// block, so they're only in the boot binary.  The harness intercepts RST 8
// (BD_HOOK_LISTREAD) exactly as in the B3 tests, serving list bytes from a
// CardModel.
//
// Six tests cover the safety matrix:
//
//   TestPickAutoConfirm      — mode 0 (auto-pick-free): picks first free record,
//     shows its name, user types 'y' → CONFIRMED=1, PICK_DISPLAY contains "free".
//
//   TestPickAutoDecline      — same card, mode 0, user types 'n' → CONFIRMED=0.
//     Safety check: CONFIRMED stays 0 even though a free record was found.
//
//   TestPickByNumber         — mode 1 (manual-by-number): inject "5\ry" → picks
//     record 5 (named "SHADEBOBS"), PICK_DISPLAY contains "SHADEBOBS", CONFIRMED=1.
//
//   TestPickByNumberDecline  — mode 1, inject "5\rn" → CONFIRMED=0.
//
//   TestPickByName           — mode 2 (manual-by-name):
//     sub-case A: inject "BOOT\ry"  → finds record 4 ("BOOT"), CONFIRMED=1.
//     sub-case B: inject "NOPE\r"   → not found, BD_PICK_RECORD=0, CONFIRMED=0.
//
//   TestPickCancel           — mode 1, inject "0\r" (out-of-range) → RECORD=0,
//     CONFIRMED=0, PICK_DISPLAY contains "cancelled".
//
// THE HONESTY LINE: all selection / show-name / confirm DECISION logic is host-
// verified here.  picker_render (the MODE-2 screen blit) is hardware-gated and
// is a stub ret in this build — a follow-up item registers it (i143).
// Emulation-verified is not hardware-verified (CLAUDE.md §5).
package z80_test

import (
	"bytes"
	"strings"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

// pickMachineSetup loads the boot binary, attaches an ENC28J60 + SD card with
// the record list seeded, sets BD_RECORDS, and returns the machine.
//
// Card layout:
//
//	1: "LOADER"     (named)
//	2: "KERNEL"     (named)
//	3: free         (first free — auto-pick target)
//	4: "BOOT"       (named; by-name target)
//	5: "SHADEBOBS"  (named; by-number target)
//
// BD_RECORDS is set to 5. All 5 records are in list sector 1.
func pickMachineSetup(t *testing.T) *z80h.Machine {
	t.Helper()
	mac, err := z80h.Load(cliBootBin, cliBootMap)
	if err != nil {
		t.Skipf("client boot binary not built (%v); run `make netboot-client-boot`", err)
	}
	card := z80h.NewCardModel()
	card.SetRecordEntry(1, makeEntry("LOADER"))
	card.SetRecordEntry(2, makeEntry("KERNEL"))
	// record 3: left free (all-zero)
	card.SetRecordEntry(4, makeEntry("BOOT"))
	card.SetRecordEntry(5, makeEntry("SHADEBOBS"))

	// Attach ENC + SD; seed list sector 1 (all 5 records are within records 1..32).
	enc := z80h.NewENC28J60()
	sd := enc.AttachSD(csdV2(1))
	mac.AttachIO(enc)
	sec1 := card.ListSector(1)
	sd.SeedSector(1, sec1[:])
	if _, err := mac.Call("csd_set_bd_records"); err != nil {
		t.Fatalf("csd_set_bd_records: %v", err)
	}
	mac.WriteU16LE(symAddr(t, mac, "BD_RECORDS"), 5)
	return mac
}

// readPickDisplay returns PICK_DISPLAY as a NUL-terminated Go string.
func readPickDisplay(t *testing.T, mac *z80h.Machine) string {
	t.Helper()
	raw := mac.Read(symAddr(t, mac, "PICK_DISPLAY"), 256)
	end := bytes.IndexByte(raw, 0)
	if end < 0 {
		end = 256
	}
	return string(raw[:end])
}

// readPickRecord returns BD_PICK_RECORD as a uint16 (LE).
func readPickRecord(t *testing.T, mac *z80h.Machine) uint16 {
	b := mac.Read(symAddr(t, mac, "BD_PICK_RECORD"), 2)
	return uint16(b[0]) | uint16(b[1])<<8
}

// readPickConfirmed returns BD_PICK_CONFIRMED as a byte.
func readPickConfirmed(t *testing.T, mac *z80h.Machine) byte {
	return mac.Read(symAddr(t, mac, "BD_PICK_CONFIRMED"), 1)[0]
}

// setPickMode writes BD_PICK_MODE.
func setPickMode(t *testing.T, mac *z80h.Machine, mode byte) {
	t.Helper()
	mac.Write(symAddr(t, mac, "BD_PICK_MODE"), []byte{mode})
}

// callPicker calls bdos_pick_record and returns the error.
func callPicker(t *testing.T, mac *z80h.Machine) {
	t.Helper()
	if _, err := mac.CallEntry("bdos_pick_record", z80h.Entry{}); err != nil {
		t.Fatalf("bdos_pick_record: %v", err)
	}
}

// TestPickAutoConfirm verifies mode 0 (auto-pick-free):
// - finds the first free record (record 3)
// - shows its name in PICK_DISPLAY (must contain "free")
// - reads 'y' from the inject queue
// - sets BD_PICK_CONFIRMED = 1
// Safety gate: CONFIRMED is only 1 because both show-name AND confirm key ran.
func TestPickAutoConfirm(t *testing.T) {
	mac := pickMachineSetup(t)
	setPickMode(t, mac, 0) // BD_PICK_MODE_AUTO
	mac.InjectKeys([]byte{'y'})
	callPicker(t, mac)

	rec := readPickRecord(t, mac)
	if rec != 3 {
		t.Errorf("BD_PICK_RECORD = %d, want 3 (first free)", rec)
	}
	conf := readPickConfirmed(t, mac)
	if conf != 1 {
		t.Errorf("BD_PICK_CONFIRMED = %d, want 1 (confirmed)", conf)
	}
	disp := readPickDisplay(t, mac)
	if !strings.Contains(disp, "free") {
		t.Errorf("PICK_DISPLAY %q does not contain \"free\" (show-name not called or broken)", disp)
	}
}

// TestPickAutoDecline verifies mode 0, user types 'n':
// - record 3 is found and shown
// - 'n' is consumed
// - BD_PICK_CONFIRMED = 0 (safety gate: CONFIRMED NOT set even though record was found)
func TestPickAutoDecline(t *testing.T) {
	mac := pickMachineSetup(t)
	setPickMode(t, mac, 0) // BD_PICK_MODE_AUTO
	mac.InjectKeys([]byte{'n'})
	callPicker(t, mac)

	rec := readPickRecord(t, mac)
	if rec != 3 {
		t.Errorf("BD_PICK_RECORD = %d, want 3 (still set — declined, not cancelled)", rec)
	}
	conf := readPickConfirmed(t, mac)
	if conf != 0 {
		t.Errorf("BD_PICK_CONFIRMED = %d, want 0 (declined)", conf)
	}
}

// TestPickByNumber verifies mode 1 (manual-by-number):
// inject "5\ry" → record 5 ("SHADEBOBS") picked and confirmed.
// PICK_DISPLAY must contain "SHADEBOBS" to prove show-name ran.
func TestPickByNumber(t *testing.T) {
	mac := pickMachineSetup(t)
	setPickMode(t, mac, 1) // BD_PICK_MODE_NUM
	mac.InjectKeys([]byte("5\ry"))
	callPicker(t, mac)

	rec := readPickRecord(t, mac)
	if rec != 5 {
		t.Errorf("BD_PICK_RECORD = %d, want 5", rec)
	}
	conf := readPickConfirmed(t, mac)
	if conf != 1 {
		t.Errorf("BD_PICK_CONFIRMED = %d, want 1", conf)
	}
	disp := readPickDisplay(t, mac)
	if !strings.Contains(disp, "SHADEBOBS") {
		t.Errorf("PICK_DISPLAY %q does not contain \"SHADEBOBS\" (show-name broken)", disp)
	}
}

// TestPickByNumberDecline verifies mode 1, inject "5\rn":
// record 5 is selected and shown; 'n' is the confirm key; CONFIRMED=0.
// Safety gate check: the name IS shown ('SHADEBOBS' in display) but CONFIRMED=0.
func TestPickByNumberDecline(t *testing.T) {
	mac := pickMachineSetup(t)
	setPickMode(t, mac, 1) // BD_PICK_MODE_NUM
	mac.InjectKeys([]byte("5\rn"))
	callPicker(t, mac)

	rec := readPickRecord(t, mac)
	if rec != 5 {
		t.Errorf("BD_PICK_RECORD = %d, want 5", rec)
	}
	conf := readPickConfirmed(t, mac)
	if conf != 0 {
		t.Errorf("BD_PICK_CONFIRMED = %d, want 0 (declined)", conf)
	}
	disp := readPickDisplay(t, mac)
	if !strings.Contains(disp, "SHADEBOBS") {
		t.Errorf("PICK_DISPLAY %q does not contain \"SHADEBOBS\" (show-name must run even when declined)", disp)
	}
}

// TestPickByName verifies mode 2 (manual-by-name):
//
//   sub-case A: inject "BOOT\ry" → finds record 4 ("BOOT"), confirms it.
//   sub-case B: inject "NOPE\r"  → name not found; RECORD=0, CONFIRMED=0.
//
// For sub-case B no 'y'/'n' is injected — the gate is never reached because
// the record lookup returns 0 (cancelled path).
func TestPickByName(t *testing.T) {
	t.Run("found-and-confirmed", func(t *testing.T) {
		mac := pickMachineSetup(t)
		setPickMode(t, mac, 2) // BD_PICK_MODE_NAME
		mac.InjectKeys([]byte("BOOT\ry"))
		callPicker(t, mac)

		rec := readPickRecord(t, mac)
		if rec != 4 {
			t.Errorf("BD_PICK_RECORD = %d, want 4 (\"BOOT\")", rec)
		}
		conf := readPickConfirmed(t, mac)
		if conf != 1 {
			t.Errorf("BD_PICK_CONFIRMED = %d, want 1", conf)
		}
		disp := readPickDisplay(t, mac)
		if !strings.Contains(disp, "BOOT") {
			t.Errorf("PICK_DISPLAY %q does not contain \"BOOT\" (show-name broken)", disp)
		}
	})

	t.Run("not-found", func(t *testing.T) {
		mac := pickMachineSetup(t)
		setPickMode(t, mac, 2) // BD_PICK_MODE_NAME
		// Inject "NOPE\r": name not on card; picker returns 0 (not-found / cancelled)
		// without reaching the confirm gate, so no 'y'/'n' needed.
		mac.InjectKeys([]byte("NOPE\r"))
		callPicker(t, mac)

		rec := readPickRecord(t, mac)
		if rec != 0 {
			t.Errorf("BD_PICK_RECORD = %d, want 0 (not found)", rec)
		}
		conf := readPickConfirmed(t, mac)
		if conf != 0 {
			t.Errorf("BD_PICK_CONFIRMED = %d, want 0 (not found → no confirm gate)", conf)
		}
	})
}

// TestPickCancel verifies mode 1 with an out-of-range number:
// inject "0\r" → record 0 is invalid; picker returns RECORD=0, CONFIRMED=0,
// PICK_DISPLAY contains "cancelled".
func TestPickCancel(t *testing.T) {
	mac := pickMachineSetup(t)
	setPickMode(t, mac, 1) // BD_PICK_MODE_NUM
	// "0\r": atoi_dec("0") = 0, which is invalid (records are 1-based); cancelled.
	mac.InjectKeys([]byte("0\r"))
	callPicker(t, mac)

	rec := readPickRecord(t, mac)
	if rec != 0 {
		t.Errorf("BD_PICK_RECORD = %d, want 0 (cancelled)", rec)
	}
	conf := readPickConfirmed(t, mac)
	if conf != 0 {
		t.Errorf("BD_PICK_CONFIRMED = %d, want 0 (cancelled)", conf)
	}
	disp := readPickDisplay(t, mac)
	if !strings.Contains(disp, "cancelled") {
		t.Errorf("PICK_DISPLAY %q does not contain \"cancelled\"", disp)
	}
}
