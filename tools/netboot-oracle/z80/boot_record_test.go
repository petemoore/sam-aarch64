// boot_record_test.go — i316: emulation verification of the trinload-pushable
// "boot Trinity SD record N" program (src/netboot/boot_record.asm).
//
// boot_record is a thin wrapper over bdos_boot_record (the i122a primitive,
// covered directly by bdos_boot_test.go): boot_record_main reads the host-chosen
// record number from its BOOT_CONFIG block (BOOT_CFG_RECORD, a host-patched byte),
// stores it in BD_BOOT_RECORD, and calls bdos_boot_record — which HRECORD-selects
// the record then fires ALHK (hook 136) to load+run its AUTO file. This test loads
// the REAL built binary, attaches the BDOSStore (which models the RST 8 HRECORD +
// ALHK dispatch — bdos_store.go), patches BOOT_CFG_RECORD to a chosen record, runs
// boot_record_main end-to-end, and asserts the store HRECORD-SELECTED that record
// and captured exactly one ALHK boot against it.
//
// It complements bdos_boot_test.go: that exercises the bdos_boot_record leaf via a
// direct memory poke of BD_BOOT_RECORD; this exercises the full pushable PROGRAM —
// the config-block read plumbing the record number into BD_BOOT_RECORD, then the
// boot — which is the actual i316 deliverable.
//
// THE HONESTY LINE (CLAUDE.md §5): the ALHK handler models the digital dispatch (a
// boot event against the selected record), not a real boot (which never returns on
// hardware — control boots into the loaded AUTO file). The actual on-hardware boot
// shot is a SEPARATE follow-up. Emulation-verified is not hardware-verified.
package z80_test

import (
	"os"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	bootRecordBin = "../../../build/boot_record.bin"
	bootRecordMap = "../../../build/boot_record.map"

	// bootCfgMagic mirrors BOOT_CFG_MAGIC_VAL in src/netboot/boot_record.asm — the
	// magic byte at BOOT_CONFIG+0 that the host launcher sanity-checks it found the
	// config block before patching BOOT_CFG_RECORD. Asserted here so a binary/layout
	// drift (the config block moving or losing its anchor) fails the test rather than
	// silently breaking the host patcher.
	bootCfgMagic = 0x5A
)

// loadBootRecord loads the built boot_record binary under the flat harness, failing
// (never skipping — i253) if it is not built.
func loadBootRecord(t *testing.T) *z80h.Machine {
	t.Helper()
	if _, err := os.Stat(bootRecordBin); err != nil {
		t.Fatalf("boot_record binary not built (%s); run `make netboot-boot-record`", bootRecordBin)
	}
	mac, err := z80h.Load(bootRecordBin, bootRecordMap)
	if err != nil {
		t.Fatalf("load boot_record: %v", err)
	}
	if _, err := mac.Sym("boot_record_main"); err != nil {
		t.Fatalf("boot_record_main symbol absent from %s — wrong build?", bootRecordMap)
	}
	return mac
}

// runBootRecord patches BOOT_CFG_RECORD to `record`, attaches a fresh BDOSStore, and
// runs boot_record_main end-to-end. It returns the store so the caller asserts the
// HRECORD selection + ALHK boot. It first checks the BOOT_CONFIG magic anchor.
func runBootRecord(t *testing.T, record byte) *z80h.BDOSStore {
	t.Helper()
	mac := loadBootRecord(t)

	// The BOOT_CONFIG magic anchor must be present (the host patcher's sanity byte).
	if got := mac.Read(symAddr(t, mac, "BOOT_CONFIG"), 1)[0]; got != bootCfgMagic {
		t.Fatalf("BOOT_CONFIG magic = %#x, want %#x (config-block layout drifted; the host patcher would fail its sanity check)", got, bootCfgMagic)
	}

	// Patch BOOT_CFG_RECORD exactly as the host launcher does (a single byte at the
	// config offset), so boot_record_main reads THIS record number, not the baked default.
	mac.Write(symAddr(t, mac, "BOOT_CFG_RECORD"), []byte{record})

	store := z80h.NewBDOSStore()
	mac.AttachBDOS(store)

	if _, err := mac.CallEntry("boot_record_main", z80h.Entry{}); err != nil {
		t.Fatalf("call boot_record_main (record %d): %v", record, err)
	}
	return store
}

// TestBootRecordSelectsAndBoots is the core i316 gate: a host names record 3 (via the
// patched BOOT_CFG_RECORD byte); boot_record_main must HRECORD-select record 3 and fire
// exactly one ALHK boot against it. This proves the config-read plumbing carries the
// host-chosen record number into BD_BOOT_RECORD and on into the boot primitive.
func TestBootRecordSelectsAndBoots(t *testing.T) {
	const record = 3
	store := runBootRecord(t, record)

	// The config read + HRECORD select ran: record 3 is the selected record.
	if store.Selected() != record {
		t.Errorf("store.Selected() = %d, want %d (BOOT_CFG_RECORD -> BD_BOOT_RECORD -> HRECORD)", store.Selected(), record)
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

// TestBootRecordFloppy verifies the floppy case: BOOT_CFG_RECORD = 0 selects record 0
// (the floppy) and ALHK boots it — confirming the program is not hard-wired to a non-
// zero record and honours the 0 = floppy convention of bdos_boot_record.
func TestBootRecordFloppy(t *testing.T) {
	store := runBootRecord(t, 0)

	if store.Selected() != 0 {
		t.Errorf("store.Selected() = %d, want 0 (floppy)", store.Selected())
	}
	boots := store.Boots()
	if len(boots) != 1 || boots[0] != 0 {
		t.Fatalf("store.Boots() = %v, want [0] (one ALHK fire against the floppy)", boots)
	}
}

// TestBootRecordDefaultIsRecordOne verifies the baked default: an UN-patched binary
// (BOOT_CFG_RECORD left at its assembled value) boots record 1. This pins the default
// the host launcher relies on when no record is specified, and guards the config
// block's default byte from silent drift.
func TestBootRecordDefaultIsRecordOne(t *testing.T) {
	mac := loadBootRecord(t)

	// Do NOT patch BOOT_CFG_RECORD — read what the binary ships with, then run.
	def := mac.Read(symAddr(t, mac, "BOOT_CFG_RECORD"), 1)[0]
	if def != 1 {
		t.Fatalf("baked BOOT_CFG_RECORD default = %d, want 1 (the un-patched default the host relies on)", def)
	}

	store := z80h.NewBDOSStore()
	mac.AttachBDOS(store)
	if _, err := mac.CallEntry("boot_record_main", z80h.Entry{}); err != nil {
		t.Fatalf("call boot_record_main (default): %v", err)
	}

	if store.Selected() != 1 {
		t.Errorf("store.Selected() = %d, want 1 (the baked default record)", store.Selected())
	}
	if boots := store.Boots(); len(boots) != 1 || boots[0] != 1 {
		t.Fatalf("store.Boots() = %v, want [1] (one ALHK fire against the default record)", boots)
	}
}
