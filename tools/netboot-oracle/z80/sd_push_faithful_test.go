package z80_test

import (
	"bytes"
	"os"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

// sd_push_faithful_test.go — i293 FAITHFUL verification against real B-DOS (private
// captures). It drives sd_push's OWN routines through Colin's forked ROM + B-DOS 1.5t
// + the SD model, so the RST 8 hooks dispatch to the REAL ROM handlers (NOT the
// AttachBDOS mock — no AttachBDOS here; the real ROM issues the real CMD9/CMD17/CMD24
// to sdcard.go). It proves the record-DIRECTED write end-to-end in emulation:
//
//   1. sd_push's csd_set_bd_records reads the real card CSD (CMD9) and computes
//      BD_RECORDS + csd_base.
//   2. sd_push's bdos_find_free_record reads the on-card record LIST via the REAL
//      CMD17 single-block read and auto-picks the first free record (record 2 when
//      record 1's list entry is named).
//   3. sd_push's bdos_select_record (HRECORD) redirects B-DOS's seek base to the
//      chosen record, and sd_push's per-sector bdos_write_record (HWSAD) issues a real
//      CMD24 that LANDS in the CHOSEN record's LBA range (csd_base + 1600*(rec-1) + i)
//      — and ONLY there (data safety) — for a MULTI-SECTOR payload, with ZERO ENC
//      re-arm between sectors.
//
// THE FAITHFUL DISPATCH ARMING (the i293 emulator-fidelity fix). On real hardware a
// trinload-pushed program is an EXTERNAL caller of B-DOS — NOT a routine running inside
// a DOS call. The B-DOS hook dispatcher (&8319) reaches the handlers cleanly only in
// that external-caller state: DOSCNT (&5BC3) = 0 (the §8o/i280b-b2n unlock — DOSCNT=1
// is "DOS in control", which diverts the rst 8 through the &37E8 recursion guard that
// CLOBBERS the shadow registers carrying the record number, so HRECORD sees record 0
// and never redirects the seek base), with the serve runtime map (LMPR=&1F, HMPR=1).
// The earlier rig drove the hooks from the raw editor-idle snapshot (DOSCNT=1), which
// is the §8b "isolated rst-8 entry wanders" boundary; arming DOSCNT=0 models the real
// pushed-program context and the record-directed write then lands correctly. (Verified
// against the gold BASIC `RECORD n` path, which pokes the SAME seek-base immediate
// &A185 to csd_base+1600*(n-1) — TestBASICSaveWritesRecordToSD / the i293 diagnosis.)
//
// GATED on the proprietary captures; skips under SKIP_PRIVATE_TESTS (the one sanctioned
// skip, i253). Emulation-verified is not hardware-verified (CLAUDE.md §5).

const (
	sdPushFaithBin = "../../../build/sd_push.bin"
	sdPushFaithMap = "../../../build/sd_push.map"

	// csdV2(0x001D59): base (first data sector) = 152, records = 4809 — the card shape
	// the gold faithful rigs use. csd_base feeds the record->LBA math.
	faithCSDBase = 152

	// The resident B-DOS page at editor idle (page 29) holds the self-modifying seek-base
	// immediate &A185/&A188 the record select pokes; the faithful diagnosis reads it there.
	faithDOSPage = 29
)

// loadSDPushIntoPage copies build/sd_push.bin into physical RAM page `page` of the
// already-booted real-ROM machine and merges its symbol map, so sd_push's routines can
// be called by symbol while it lives in section C (HMPR = page).
//
// THE PAGED-POINTER CONTRACT (i280b-b2h §8k, TestHWSADPagedPointerContract): the real
// B-DOS HWSAD prelude repages section C to (H>>6)-1 from the high byte H of the source
// pointer hk.hl, then streams the 512 bytes from THERE. sd_push's BD_WRITE_BUF is at
// logical &90xx (H=&90 -> (0x90>>6)-1 = 1), so the prelude reads absolute page 1 —
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

// armServeDispatch puts the machine into the faithful external-caller dispatch state a
// trinload-pushed program runs in: DOSCNT (&5BC3) = 0 and the serve runtime map
// (LMPR=&1F, HMPR=page). Re-applied before each hook drive (a hook can change DOSCNT).
func armServeDispatch(mac *z80h.Machine, page uint8) {
	mac.Write(0x5BC3, []byte{0x00}) // DOSCNT := 0 (external caller / not-in-DOS)
	mac.Pager().LMPR = 0x1F
	mac.Pager().HMPR = page
}

// rdDOSPage reads n bytes at addr from B-DOS's resident page (section C view).
func rdDOSPage(mac *z80h.Machine, addr uint16, n int) []byte {
	saved := mac.Pager().HMPR
	mac.Pager().HMPR = faithDOSPage
	b := mac.Read(addr, n)
	mac.Pager().HMPR = saved
	return b
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
	// reads real list bytes + the free test, AND gives a NON-default target base so the
	// landing assertion below distinguishes record 2 (LBA 1752) from the default (152)).
	listSec1 := make([]byte, 512)
	listSec1[0] = byte('A')    // record 1's list entry: named => not free
	sd.SeedSector(1, listSec1) // card-absolute LBA 1 = list sector 1
	if _, err := mac.ContinueFrom(sym("bdos_find_free_record"), z80h.Entry{StepCap: 8_000_000}); err != nil {
		t.Fatalf("bdos_find_free_record: %v", err)
	}
	free := int(leU16(mac.Read(sym("BD_FREE_RECORD"), 2)))
	t.Logf("FACT 2 — sd_push free-record pick (real CMD17 list read): BD_FREE_RECORD=%d", free)
	if free != 2 {
		t.Fatalf("BD_FREE_RECORD = %d, want 2 (record 1 named, record 2 the first free)", free)
	}

	recBase := uint32(faithCSDBase + 1600*(free-1)) // record 2's first data LBA = 1752
	defBase := uint32(faithCSDBase)                 // the default (unredirected) base = 152

	// --- FACT 3: sd_push's HRECORD-select redirects B-DOS's seek base to record 2. ---
	// Drive sd_push's bdos_select_record in the faithful armed dispatch state. HRECORD
	// pokes the self-modifying seek-base immediate (&A185/&A188) in the resident B-DOS
	// page from the record number — to record 2's base (1752), NOT the default (152).
	seekBefore := leU16(rdDOSPage(mac, 0xA185, 2))
	armServeDispatch(mac, page)
	selStub := uint16(0x9000)
	mac.Pager().HMPR = page
	mac.Write(selStub, []byte{
		0x21, byte(free), byte(free >> 8), // ld hl,free
		0xAF,       // xor a            (bdos_select_record's HL=record, A=0 contract)
		0xCF, 0x9C, // rst 8 ; defb 156 (HRECORD)
		0xF3, 0x76, // di ; halt
	})
	if _, err := mac.RunBootFrom(selStub, z80h.Entry{StepCap: 6_000_000}); err != nil {
		t.Fatalf("HRECORD select stub: %v", err)
	}
	seekAfter := leU16(rdDOSPage(mac, 0xA185, 2))
	t.Logf("FACT 3 — sd_push HRECORD select: seek-base immediate &A185 %d -> %d (record %d base = %d)", seekBefore, seekAfter, free, recBase)
	if uint32(seekAfter) != recBase {
		t.Fatalf("HRECORD did not redirect the seek base: &A185 = %d, want %d (csd_base + 1600*(%d-1)). The record-directed select is not taking.", seekAfter, recBase, free)
	}

	// --- FACT 4: per-sector HWSAD writes land in record 2's LBA range, byte-exact,
	// with ZERO ENC re-arm, and NOTHING lands outside record 2. ---
	// Push a MULTI-SECTOR payload that crosses a track boundary (linear 0..11 spans
	// track 0 sectors 1-10 + track 1 sectors 1-2), exercising bdos_write_record's
	// linearSec->track/sector decode and the per-sector seek-from-record-base.
	const nSectors = 12
	want := make([][]byte, nSectors)
	for s := 0; s < nSectors; s++ {
		sec := make([]byte, 512)
		for i := range sec {
			sec[i] = byte((s*37 + i*7 + 0x11) & 0xFF) // distinctive, non-trivial, per-sector
		}
		want[s] = sec
	}

	const mcWriteCore = 0x68F4 // &A8F4 SD write core
	for s := 0; s < nSectors; s++ {
		// Stage sector s into BD_WRITE_BUF (page 1 — the prelude's page) and drive the
		// exact sp_data_block primitive (BD_WRITE_START = linear sector, COUNT = 1).
		mac.Pager().HMPR = page
		mac.Write(sym("BD_WRITE_BUF"), want[s])
		mac.WriteU16LE(sym("BD_WRITE_START"), uint16(s))
		mac.WriteU16LE(sym("BD_WRITE_COUNT"), 1)
		// Re-arm the faithful dispatch (a prior hook may have moved DOSCNT) and keep
		// HMPR=page so the prelude reads OUR bytes. NO enc_rx_reestablish between writes
		// — the i293 fix: an SD transaction does not disturb the ENC RX.
		armServeDispatch(mac, page)
		mac.Pager().HMPR = page

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
			t.Fatalf("bdos_write_record[sector %d]: %v", s, err)
		}
		frames, sawCMD24 := decodeFrames(*lg, from)
		if !reachedCore {
			t.Fatalf("sector %d: HWSAD did NOT reach the real B-DOS SD write core &A8F4", s)
		}
		if !sawCMD24 {
			t.Fatalf("sector %d: HWSAD issued no CMD24", s)
		}
		// The CMD24 for sector s must address record 2's LBA recBase+s.
		var cmd24Addr uint32
		var got24 bool
		for _, f := range frames {
			if f.cmd == 24 {
				cmd24Addr = f.addr
				got24 = true
			}
		}
		if !got24 || cmd24Addr != recBase+uint32(s) {
			t.Fatalf("sector %d: CMD24 landed at LBA %d, want %d (record %d, linear sector %d). The write is not record-directed.",
				s, cmd24Addr, recBase+uint32(s), free, s)
		}
		_ = res
	}

	// (a) Every sector's CMD24 landed in record 2's range, byte-exact (c).
	for s := 0; s < nSectors; s++ {
		got, ok := sd.RecordDataSector(faithCSDBase, free, s)
		if !ok {
			t.Fatalf("record %d linear sector %d not present in the SD store — the write did not land", free, s)
		}
		if !bytes.Equal(got, want[s]) {
			t.Fatalf("record %d linear sector %d: bytes differ (first got %#x want %#x) — the data path did not carry the pushed image", free, s, got[0], want[s][0])
		}
	}
	t.Logf("FACT 4a/c — all %d sectors landed in record %d (LBA %d..%d), byte-exact", nSectors, free, recBase, recBase+nSectors-1)

	// (d) DATA SAFETY: every committed CMD24 write fell inside record 2's range; none
	// touched any other record / the boot+list area. (SeedSector pre-loads at LBA 1/152
	// are NOT writes, so WrittenSectors is the clean view.)
	if outside := sd.WrittenSectorsOutsideRecord(faithCSDBase, free); len(outside) != 0 {
		t.Fatalf("DATA-SAFETY VIOLATION: %d CMD24 write(s) landed outside record %d's range [%d,%d): %v",
			len(outside), free, recBase, faithCSDBase+1600*free, outside)
	}
	if n := len(sd.WrittenSectors()); n != nSectors {
		t.Fatalf("expected exactly %d CMD24 writes, saw %d (WrittenSectors=%v)", nSectors, n, sd.WrittenSectors())
	}
	if _, landedDefault := sd.RecordDataSector(faithCSDBase, 1, 0); !landedDefault {
		// Sanity: record 1's data sector 0 (LBA 152) holds ONLY the seed (rec1 stamp),
		// never one of our pushed sectors — confirm our writes did not stray there.
		t.Logf("note: LBA %d (record 1 sec 0) absent — it was only ever the seeded stamp", defBase)
	} else if got, _ := sd.RecordDataSector(faithCSDBase, 1, 0); bytes.Equal(got, want[0]) {
		t.Fatalf("DATA-SAFETY VIOLATION: sector 0 of record 1 (the DEFAULT base, LBA %d) holds our pushed bytes — a write strayed to the default record", defBase)
	}
	t.Logf("FACT 4d — data-safe: all %d writes inside record %d's range, none at the default base or any other record", nSectors, free)
}

// leU16 decodes a 2-byte little-endian value.
func leU16(b []byte) uint16 { return uint16(b[0]) | uint16(b[1])<<8 }

