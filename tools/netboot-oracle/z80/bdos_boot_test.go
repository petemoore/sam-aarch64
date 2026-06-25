// bdos_boot_test.go — i122a: host-verification of the boot-a-record primitive
// (the first brick of i122 SAM fetch-and-boot).
//
// bdos_boot_record (client boot binary, RST 8) HRECORD-selects a record then
// fires ALHK (hook 136) to auto-load + run that record's AUTO file — the B-DOS
// BOOT "DOS resident" branch (KEYBOARD_BOOT_WORKAROUND.md §2) / HAUTO
// (bdos15a.src.txt:463). The test exercises the real RST 8 dispatch via
// AttachBDOS and asserts the store captured the boot against the selected
// record. It mirrors the bdos_write_test.go pattern for the HWSAD seam.
//
// THE HONESTY LINE (CLAUDE.md §5): the ALHK handler in bdos_store.go models the
// digital dispatch (a boot event against the selected record) — not a real boot
// (which never returns on hardware). Emulation-verified is not hardware-verified.
package z80_test

import (
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

// TestBDOSBootRecord verifies the real Z80 bdos_boot_record selects the record
// via HRECORD and fires ALHK, with the harness model capturing exactly one boot
// against that record.
func TestBDOSBootRecord(t *testing.T) {
	mac, err := z80h.Load(cliBootBin, cliBootMap)
	if err != nil {
		t.Fatalf("client boot binary not built (%v); run `make netboot-client-boot`", err)
	}
	store := z80h.NewBDOSStore()
	mac.AttachBDOS(store)

	const record = 3
	mac.Write(symAddr(t, mac, "BD_BOOT_RECORD"), []byte{record})

	if _, err := mac.CallEntry("bdos_boot_record", z80h.Entry{}); err != nil {
		t.Fatalf("call bdos_boot_record: %v", err)
	}

	// The select half ran: HRECORD recorded record 3.
	if store.Selected() != record {
		t.Errorf("store.Selected() = %d, want %d (HRECORD inside bdos_boot_record)", store.Selected(), record)
	}

	// The boot half ran: exactly one ALHK boot captured, tagged to record 3.
	boots := store.Boots()
	if len(boots) != 1 {
		t.Fatalf("store.Boots() = %d entries, want 1 (one ALHK fire)", len(boots))
	}
	if boots[0] != record {
		t.Errorf("boots[0] = %d, want %d (the record selected when ALHK fired)", boots[0], record)
	}
}

// TestBDOSBootRecordFloppy verifies the floppy case: BD_BOOT_RECORD = 0 selects
// record 0 (the floppy) and ALHK boots it. Confirms the primitive is not
// hard-wired to a non-zero record.
func TestBDOSBootRecordFloppy(t *testing.T) {
	mac, err := z80h.Load(cliBootBin, cliBootMap)
	if err != nil {
		t.Fatalf("client boot binary not built (%v); run `make netboot-client-boot`", err)
	}
	store := z80h.NewBDOSStore()
	mac.AttachBDOS(store)

	mac.Write(symAddr(t, mac, "BD_BOOT_RECORD"), []byte{0})
	if _, err := mac.CallEntry("bdos_boot_record", z80h.Entry{}); err != nil {
		t.Fatalf("call bdos_boot_record: %v", err)
	}

	if store.Selected() != 0 {
		t.Errorf("store.Selected() = %d, want 0 (floppy)", store.Selected())
	}
	boots := store.Boots()
	if len(boots) != 1 || boots[0] != 0 {
		t.Fatalf("store.Boots() = %v, want [0] (one ALHK fire against the floppy)", boots)
	}
}
