package z80_test

import (
	"os"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

// sd_push_faithful_test.go — i293 FAITHFUL verification against real B-DOS (private
// captures). It drives sd_push's OWN routines through Colin's forked ROM + B-DOS 1.5t
// + the SD model, so the RST 8 hooks dispatch to the REAL ROM handlers (NOT the
// AttachBDOS mock — no AttachBDOS here; the real ROM issues the real CMD9/CMD17/CMD24
// to sdcard.go). This proves the load-bearing pieces the flat-harness logic test
// (sd_push_test.go) cannot, AND honestly records the documented emulator boundary +
// open hardware blocker that bound how far the record-DIRECTED write can be verified
// in emulation today.
//
// WHAT THIS RIG PROVES (real B-DOS, this environment):
//   1. sd_push's csd_set_bd_records reads the real card CSD (CMD9) and computes
//      BD_RECORDS + csd_base — the record-count + LBA-base the picker/addressing need.
//   2. sd_push's bdos_find_free_record reads the on-card record LIST via the REAL
//      CMD17 single-block read and auto-picks the first free record (record 1 free on
//      an empty list; record 2 when record 1's list entry is named).
//   3. sd_push's HWSAD write path (bdos_write_record -> bdos_write_sector, A=2)
//      reaches the REAL B-DOS SD write core and issues a real CMD24 — the i280b-b2t
//      A=2 drive-select fix works for sd_push's invocation too (A=1 would hang in the
//      B-DOS floppy poll).
//
// THE DOCUMENTED BOUNDARY (why the record-DIRECTED write is not asserted-landed here):
//   The HWSAD write needs the ambient device armed as Trinity SD AND the mounted
//   record base set to the SELECTED record — all armed by a record-select with the
//   FULL DOS-call SP/paging context (registry i280b-b2n / docs/notes/
//   trinity-sd-z80-interface.md §8b: "post-boot the device is UNARMED; a record-select
//   before the write is mandatory", with the honest boundary that "an isolated
//   rst8/handler entry from the editor-idle snapshot wanders — the real DOS-call
//   context is not armed"). Driving sd_push's hooks one-at-a-time via ContinueFrom from
//   the editor-idle snapshot is exactly that isolated-entry path: the CMD24 fires but
//   lands at the DEFAULT record base (csd_base, record 1), NOT the bdos_select_record'd
//   free record — the HRECORD-via-rst8 select does not redirect B-DOS's write base from
//   this partial context. Faithfully verifying the record-DIRECTED landing therefore
//   needs the END-TO-END path (trinload push -> sd_push_main -> the receive loop) on
//   REAL Trinity hardware (the koron-go SD model also always clears busy, so the real
//   write-core busy-wait is unmodelled) — the open i280b/i270 + i283 hardware gate.
//
// So this test asserts (1)-(3) as hard facts and LOGS the record-base observation as
// the open-blocker characterization (the pattern bdos_write_core_reach_test.go uses):
// it documents reality rather than asserting a landing the emulator cannot faithfully
// produce. Shipping the record-directed claim as PASS would be exactly the kind of
// emulator-green-on-a-guess the prime directive + CLAUDE.md rule 8 forbid.
//
// GATED on the proprietary captures; skips under SKIP_PRIVATE_TESTS (the one sanctioned
// skip, i253). Emulation-verified is not hardware-verified (CLAUDE.md §5).

const (
	sdPushFaithBin = "../../../build/sd_push.bin"
	sdPushFaithMap = "../../../build/sd_push.map"

	// csdV2(0x001D59): base (first data sector) = 152, records = 4809 — the card shape
	// the gold faithful rigs use. csd_base feeds the record->LBA math.
	faithCSDBase = 152
)

// loadSDPushIntoPage copies build/sd_push.bin into physical RAM page `page` of the
// already-booted real-ROM machine and merges its symbol map, so sd_push's routines can
// be called by symbol while it lives in section C (HMPR = page).
//
// THE PAGED-POINTER CONTRACT (i280b-b2h §8k, TestHWSADPagedPointerContract): the real
// B-DOS HWSAD prelude repages section C to (H>>6)-1 from the high byte H of the source
// pointer hk.hl, then streams the 512 bytes from THERE. sd_push's BD_WRITE_BUF is at
// logical &909B (H=&90 -> (0x90>>6)-1 = 1), so the prelude reads absolute page 1 —
// meaning sd_push (and its BD_WRITE_BUF data) MUST live in page 1 for the write to read
// OUR bytes. This is the deployment contract: trinload must push sd_push to page 1 (the
// page the shipping serve uses).
func loadSDPushIntoPage(t *testing.T, mac *z80h.Machine, page uint8) {
	t.Helper()
	code, err := os.ReadFile(sdPushFaithBin)
	if err != nil {
		t.Fatalf("sd_push not built (%v); run `make netboot-sd-push`", err)
	}
	if err := mac.LoadSymbols(sdPushFaithMap); err != nil {
		t.Fatalf("load sd_push symbols: %v", err)
	}
	sH := mac.Pager().HMPR
	mac.Pager().HMPR = page
	mac.Write(0x8000, code)
	mac.Pager().HMPR = sH
}

func TestSDPushFaithfulRecordWrite(t *testing.T) {
	mac, lg, sd := bootToEditorIdleSD(t) // real ROM + B-DOS + SD model; records=4809, base=152

	const page = 1 // the page sd_push's BD_WRITE_BUF (H=&90) names for the HWSAD prelude
	loadSDPushIntoPage(t, mac, page)
	mac.Pager().HMPR = page

	sym := func(name string) uint16 {
		a, err := mac.Sym(name)
		if err != nil {
			t.Fatalf("sd_push symbol %q absent from %s", name, sdPushFaithMap)
		}
		return a
	}

	// --- FACT 1: sd_push reads the real card CSD -> BD_RECORDS + csd_base. ---
	if _, err := mac.ContinueFrom(sym("csd_set_bd_records"), z80h.Entry{StepCap: 4_000_000}); err != nil {
		t.Fatalf("csd_set_bd_records: %v", err)
	}
	records := leU16(mac.Read(sym("BD_RECORDS"), 2))
	base := leU16(mac.Read(sym("csd_base"), 2))
	t.Logf("FACT 1 — sd_push CSD read (real CMD9): BD_RECORDS=%d csd_base=%d", records, base)
	if records == 0 {
		t.Fatalf("BD_RECORDS == 0 after csd_set_bd_records — the CSD read failed; sd_push would decline every push")
	}
	if int(base) != faithCSDBase {
		t.Fatalf("csd_base = %d, want %d (the record->LBA anchor)", base, faithCSDBase)
	}

	// --- FACT 2: sd_push picks the first free record via the real CMD17 list read. ---
	// Name record 1's list entry so the pick advances to record 2 (proves the scan
	// reads real list bytes + the free test, not a fixed answer).
	listSec1 := make([]byte, 512)
	listSec1[0] = byte('A') // record 1's list entry: named => not free
	sd.SeedSector(1, listSec1) // card-absolute LBA 1 = list sector 1
	if _, err := mac.ContinueFrom(sym("bdos_find_free_record"), z80h.Entry{StepCap: 8_000_000}); err != nil {
		t.Fatalf("bdos_find_free_record: %v", err)
	}
	free := leU16(mac.Read(sym("BD_FREE_RECORD"), 2))
	t.Logf("FACT 2 — sd_push free-record pick (real CMD17 list read): BD_FREE_RECORD=%d", free)
	if free != 2 {
		t.Fatalf("BD_FREE_RECORD = %d, want 2 (record 1 named, record 2 the first free)", free)
	}

	// sd_push HRECORD-selects the free record (the real B-DOS HRECORD).
	if _, err := mac.ContinueFrom(sym("bdos_select_record"), z80h.Entry{
		HL: free, A: byte(free), StepCap: 4_000_000,
	}); err != nil {
		t.Fatalf("bdos_select_record(%d): %v", free, err)
	}

	// --- FACT 3: sd_push's HWSAD write reaches the real B-DOS SD write core + CMD24. ---
	// Stage a sector into BD_WRITE_BUF (page 1, the prelude's page) and drive the exact
	// primitive sp_data_block calls. Trace the real B-DOS write-core / CMD24 landmarks
	// (section-B aliases, B-DOS paged into section B during the hook).
	const (
		mcWriteCore = 0x68F4 // &A8F4 SD write core
		mcCMD24Send = 0x6925 // &A925 CMD24 sender
	)
	sec := make([]byte, 512)
	for i := range sec {
		sec[i] = byte((i*7 + 0x11) & 0xFF)
	}
	mac.Pager().HMPR = page
	mac.Write(sym("BD_WRITE_BUF"), sec)
	mac.WriteU16LE(sym("BD_WRITE_START"), 0) // linear sector 0
	mac.WriteU16LE(sym("BD_WRITE_COUNT"), 1)

	reachedCore := false
	from := len(*lg)
	res, err := mac.ContinueFrom(sym("bdos_write_record"), z80h.Entry{
		StepCap: 8_000_000,
		Trace: func(pc uint16) {
			if pc == mcWriteCore {
				reachedCore = true
			}
		},
	})
	if err != nil {
		t.Fatalf("bdos_write_record: %v", err)
	}
	frames, sawCMD24 := decodeFrames(*lg, from)
	t.Logf("FACT 3 — sd_push HWSAD write: reachedWriteCore=%v sawCMD24=%v finalPC=&%04X halted=%v SDframes=%d",
		reachedCore, sawCMD24, res.PC, res.Halted, len(frames))
	if !reachedCore {
		t.Errorf("sd_push's HWSAD write did NOT reach the real B-DOS SD write core &A8F4 — the A=2 drive-select (i280b-b2t) is not taking for sd_push's invocation")
	}
	if !sawCMD24 {
		t.Errorf("sd_push's HWSAD write issued no CMD24 — the write did not reach the SD command stage")
	}

	// --- THE DOCUMENTED BOUNDARY: where did the CMD24 land? ---
	// In this isolated-hook-entry rig the record-select does not arm the full DOS-call
	// context, so the write lands at the DEFAULT record base (record 1), not the picked
	// free record. We LOG this (the open-blocker characterization) rather than assert a
	// record-directed landing the emulator cannot faithfully produce from this context.
	landedFree := false
	if _, ok := sd.RecordDataSector(faithCSDBase, int(free), 0); ok {
		landedFree = true
	}
	_, landedRec1 := sd.RecordDataSector(faithCSDBase, 1, 0)
	t.Logf("BOUNDARY — CMD24 landing: at picked free record %d (LBA %d)? %v ; at default record 1 (LBA %d)? %v",
		free, faithCSDBase+1600*(int(free)-1), landedFree, faithCSDBase, landedRec1)
	if landedFree {
		// If a future fix arms the select via this path, the write lands at the free
		// record — flip this to an assertion then (with the data-safety range check).
		t.Logf("NOTE: the write LANDED at the picked free record — the record-directed select now works via the isolated-hook path; promote this to a hard assertion + data-safety range check and update the boundary note")
	} else {
		t.Logf("CONFIRMED OPEN BLOCKER (i280b/i270, §8b): sd_push's HWSAD CMD24 fires + reaches the write core, but lands at the DEFAULT record base (not the bdos_select_record'd free record) — the HRECORD-via-rst8 select does not redirect B-DOS's write base from the isolated editor-idle hook context. Record-DIRECTED + data-safe landing needs the end-to-end push on real Trinity hardware (i283), or B-DOS's full DOS-call-context mount. This is why sd_push must NOT be deployed to write a live shared card until the record-directed select is hardware-confirmed.")
	}
}

// leU16 decodes a 2-byte little-endian value.
func leU16(b []byte) uint16 { return uint16(b[0]) | uint16(b[1])<<8 }
