// sd_push_test.go — i293/i295 LOGIC test (CI-safe; SKIP_PRIVATE_TESTS-green).
//
// Runs the real sd_push binary (build/sd_push.bin) end-to-end under the flat-memory
// harness, exercising its OWN-LBA create-record flow deterministically, with NO
// proprietary captures:
//
//   - the "Trinity Network " EEPROM chunk is served by the ENC EEPROM model
//     (ProgramTrinityNetwork), so sd_push_main's find_index/read_chunk read the SAM
//     MAC/IP;
//   - the inserted card's CSD is served by the SD-SPI model (AttachSD), so
//     csd_set_bd_records reads it (real CMD9) and computes csd_base + csd_blocks;
//   - the free-record list read is the REAL CMD17 (NETBOOT_REAL_LISTREAD) against the
//     SD model — an empty store reads record 1's list entry as all-zero => FREE, so
//     bdos_find_free_record auto-picks record 1;
//   - bdos_claim_record's read-modify-write of the list sector, and every body-sector
//     write, are the program's OWN raw CMD24s (bd_record_write_hw) landing in the SD
//     model (sdcard.go) — NOT the AttachBDOS RST-8 mock. There is no HRECORD/HWSAD on
//     the write path any more (the i295 design reversal), so there is no B-DOS mock
//     here; the writes ARE the CMD24s the model captures.
//   - RST &10 (the dbg_char step markers) is modelled by AttachPrintRecorder (the flat
//     harness has no ROM print channel), so sd_push_main runs to the serve loop.
//
// What this proves: sd_push's wire loop (discovery reply, the @-block linearSec
// decode, per-block claim + own-CMD24 body write to the picked record, the sector-0
// BDOS-stamp mutation, the finalize count check) is correct in emulation, and every
// write is data-safe (only the claimed record's own body + its list entry are
// touched). What it does NOT prove (left to sd_push_faithful_test.go + hardware): that
// the same CMD24s land on real Trinity silicon. Emulation-verified is not hardware-
// verified (CLAUDE.md §5).
package z80_test

import (
	"bytes"
	"os"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/frame"
	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	sdPushBin = "../../../build/sd_push.bin"
	sdPushMap = "../../../build/sd_push.map"

	// CSDForV2(0x001D59): csd_base (first data sector) = 152, csd_blocks (capacity) =
	// 7,694,336, records = 4809 — the small ~3.7 GB card geometry the guard test uses.
	// The own-LBA math is LBA = csd_base + 1600*(record-1) + linearSec, so record 1's
	// body sectors are LBA 152.. .
	spCSDBase   = 152
	spCSDBlocks = 7694336
)

// The SAM identity sd_push_main reads from the flash chunk (ProgramTrinityNetwork
// writes sam_mac at chunk+0, sam_ip at chunk+6). All our frames address it.
var (
	spSAMMac   = [6]byte{0x02, 0x54, 0x52, 0x49, 0x4e, 0xbc} // the first-light SAM MAC shape
	spSAMIp    = [4]byte{192, 168, 2, 75}
	spHostMac  = [6]byte{0x02, 0x00, 0x00, 0x00, 0x00, 0x44}
	spHostIp   = [4]byte{192, 168, 2, 99}
	spHostTID  = uint16(40000)
	sdPushPort = uint16(0xEDB0)
)

// sdPushFrame builds a UDP frame to the SAM on sd_push's listen port, carrying the
// given payload (the first byte is the protocol byte: '?', '@', or 'F').
func sdPushFrame(payload []byte) []byte {
	return frame.BuildUDPFrame(frame.UDP{
		DstMAC: frame.MAC(spSAMMac), SrcMAC: frame.MAC(spHostMac),
		SrcIP: frame.IPv4(spHostIp), DstIP: frame.IPv4(spSAMIp),
		SrcPort: spHostTID, DstPort: sdPushPort,
		Payload: payload,
	})
}

// sdPushBlock builds an '@' data block: ['@'][linearSec LE16][data...].
func sdPushBlock(linearSec uint16, data []byte) []byte {
	p := make([]byte, 0, 3+len(data))
	p = append(p, '@', byte(linearSec), byte(linearSec>>8))
	p = append(p, data...)
	return sdPushFrame(p)
}

func loadSDPush(t *testing.T) *z80h.Machine {
	t.Helper()
	if _, err := os.Stat(sdPushBin); err != nil {
		t.Fatalf("sd_push binary not built (%s); run `make netboot-sd-push`", sdPushBin)
	}
	mac, err := z80h.Load(sdPushBin, sdPushMap)
	if err != nil {
		t.Fatalf("load sd_push: %v", err)
	}
	if _, err := mac.Sym("sd_push_main"); err != nil {
		t.Fatalf("sd_push_main symbol absent from %s — wrong build?", sdPushMap)
	}
	return mac
}

// setupSDPushMain builds a machine with sd_push loaded, the EEPROM net-config +
// SD-SPI card models attached, and the RST &10 print channel modelled — the state a
// trinload-pushed sd_push runs in, minus real hardware. The SD store starts EMPTY, so
// record 1's list entry reads all-zero (FREE) and bdos_find_free_record picks it. It
// returns the machine + the SD card so the test asserts the CMD24 writes.
func setupSDPushMain(t *testing.T, csd [16]byte) (*z80h.Machine, *z80h.ENC28J60, *z80h.SDCard) {
	t.Helper()
	mac := loadSDPush(t)
	enc := z80h.NewENC28J60()
	enc.ProgramTrinityNetwork(spSAMMac, spSAMIp) // so the EEPROM config read succeeds
	sd := enc.AttachSD(csd)                       // real CMD9/CMD17/CMD24 against sdcard.go
	mac.AttachIO(enc)
	mac.AttachPrintRecorder() // model RST &10 (dbg_char markers) — no ROM in the flat harness
	return mac, enc, sd
}

// TestSDPushLogic drives sd_push end-to-end with a small 3-sector .mgt stream and
// asserts the OWN-LBA create-record behaviour:
//  1. discovery is answered ('!');
//  2. the CSD read set csd_base=152 and the scan auto-picked the first free record
//     (record 1 on an empty list), with the data-safety guard NOT tripped;
//  3. exactly four raw CMD24s land — the catalogue-claim write to list sector 1, plus
//     three body-sector writes at record 1's absolute LBAs (152,153,154);
//  4. the body sectors are byte-exact, EXCEPT the first (linear 0), whose sector-0
//     B-DOS validity metadata ("BDOS"@232 + the "cj" label@210/@250) is mutated in;
//  5. the claim wrote record 1's 16-byte list entry ("cj" + spaces);
//  6. data safety: the ONLY write outside record 1's body band is the claim (list
//     sector 1) — nothing strayed into a neighbouring record or off the card;
//  7. finalize on a non-1600 count (3) replies 'E' (the size-only validation works).
func TestSDPushLogic(t *testing.T) {
	mac, enc, sd := setupSDPushMain(t, z80h.CSDForV2(0x001D59))

	// Three distinct 512-byte sectors at linear 0,1,2 (the start of the record body).
	sectors := make([][]byte, 3)
	for s := range sectors {
		sec := make([]byte, 512)
		for i := range sec {
			sec[i] = byte((s*37 + i*7 + 0x11) & 0xFF) // distinctive, non-trivial, per-sector
		}
		sectors[s] = sec
	}

	// Queue: discovery, the three data blocks, then a (deliberately premature) finalize
	// — the count is 3, not 1600, so finalize must reply 'E'. The run spins in the serve
	// loop after the queue drains (Esc-to-exit; the modelled keyboard reports no key), so
	// RunBoot returns when the serve loop finally RETs on the finalize.
	enc.InjectRX(sdPushFrame([]byte{'?'}))
	for s, sec := range sectors {
		enc.InjectRX(sdPushBlock(uint16(s), sec))
	}
	enc.InjectRX(sdPushFrame([]byte{'F'}))

	res, err := mac.RunBoot("sd_push_main", z80h.Entry{StepCap: 30_000_000})
	if err != nil {
		t.Fatalf("RunBoot sd_push_main faulted: %v", err)
	}
	writes := sd.WrittenSectors()
	t.Logf("sd_push: halted=%v finalPC=&%04X steps=%d tx=%d writes=%v",
		res.Halted, res.PC, res.Steps, len(enc.TXFrames()), writes)

	sym := func(name string) uint16 {
		a, err := mac.Sym(name)
		if err != nil {
			t.Fatalf("sd_push symbol %q absent from %s", name, sdPushMap)
		}
		return a
	}

	// (1) Discovery must have been answered with '!'.
	if got := countPayload(enc.TXFrames(), []byte{'!'}); got < 1 {
		t.Errorf("no '!' discovery reply among %d TX frames", len(enc.TXFrames()))
	}

	// (2) The CSD read set the record->LBA anchor, and the scan picked record 1.
	base := int(leU16(mac.Read(sym("csd_base"), 2)))
	free := int(leU16(mac.Read(sym("BD_FREE_RECORD"), 2)))
	guard := mac.Read(sym("bd_rec_guard_tripped"), 1)[0]
	if base != spCSDBase {
		t.Fatalf("csd_base = %d, want %d (the record->LBA anchor)", base, spCSDBase)
	}
	if free != 1 {
		t.Fatalf("BD_FREE_RECORD = %d, want 1 (the first free record on an empty list)", free)
	}
	if guard != 0 {
		t.Fatalf("data-safety guard tripped (bd_rec_guard_tripped=%d) — a write was refused as out-of-band", guard)
	}

	// (3) Exactly four CMD24 writes: the claim (list sector 1) + 3 body sectors.
	recLBA := func(linear int) uint32 { return uint32(base) + 1600*uint32(free-1) + uint32(linear) }
	const claimLBA = 1 // record 1's list entry lives in list sector 1 (card-absolute LBA 1)
	wantWrites := []uint32{claimLBA, recLBA(0), recLBA(1), recLBA(2)}
	if !equalU32(writes, wantWrites) {
		t.Fatalf("CMD24 writes landed at %v, want %v (claim @ list sector 1, then body 152/153/154)", writes, wantWrites)
	}

	// (3b) INIT-ONCE (i301): of those four writes, exactly ONE ran the full &38 init
	// ladder — the first (the claim). The card is inited once per push, then every
	// subsequent write re-selects (&31) only, mirroring B-DOS 1.5t hd.svb-t. A
	// regression back to per-sector init would make all four pay for an init.
	if n := sd.CMD24WritesAfterInit(); n != 1 {
		t.Fatalf("init-once broken: %d of the CMD24 writes ran the full init ladder, want exactly 1 (the card must init once per push, not per sector)", n)
	}

	// (4) Body sectors byte-exact, EXCEPT sector 0's mutated B-DOS metadata.
	// Linear 1 and 2 must be verbatim copies of the pushed payload.
	for _, linear := range []int{1, 2} {
		got, ok := sd.RecordDataSector(uint32(base), free, linear)
		if !ok {
			t.Fatalf("record %d linear sector %d absent from the SD store", free, linear)
		}
		if !bytes.Equal(got, sectors[linear]) {
			t.Errorf("record %d linear sector %d: bytes differ (first got %#x want %#x) — the block payload was not carried", free, linear, got[0], sectors[linear][0])
		}
	}
	// Linear 0 (LBA 152): the B-DOS validity stamp is mutated INTO the record's first
	// sector. Everything OUTSIDE the mutated windows [210,220)+[232,236)+[250,256) still
	// equals the pushed sector; the stamp is present so a real card reads the record as
	// a valid B-DOS disk (get.label reads body +232 for "BDOS").
	sec0, ok := sd.RecordDataSector(uint32(base), free, 0)
	if !ok {
		t.Fatalf("record %d linear sector 0 absent from the SD store", free)
	}
	if got := string(sec0[232:236]); got != "BDOS" {
		t.Errorf("sector 0 +232 = %q, want \"BDOS\" (the B-DOS validity stamp must be mutated in, verbatim not byte-swapped)", got)
	}
	if got := string(sec0[210:212]); got != "cj" {
		t.Errorf("sector 0 +210 = %q, want \"cj\" (the disk label from the claimed record name)", got)
	}
	// Bytes before the first mutated window are the pushed payload untouched.
	if !bytes.Equal(sec0[:210], sectors[0][:210]) {
		t.Errorf("sector 0 bytes [0,210) were modified — only the +210/+232/+250 metadata windows should change")
	}

	// (5) The claim wrote record 1's 16-byte list entry ("cj" + spaces).
	listSec, ok := sd.CapturedSector(claimLBA)
	if !ok {
		t.Fatalf("no list sector captured at LBA %d — the catalogue claim did not write", claimLBA)
	}
	if got := string(listSec[0:16]); got != "cj              " {
		t.Errorf("record 1 list entry = %q, want \"cj\" space-padded to 16 (bdos_claim_record did not register the record)", got)
	}

	// (6) DATA SAFETY: the ONLY write outside record 1's body band is the claim.
	outside := sd.WrittenSectorsOutsideRecord(uint32(base), free)
	if len(outside) != 1 || outside[0] != claimLBA {
		t.Fatalf("writes outside record %d's body band = %v, want exactly [%d] (only the list-sector claim; a body write must not stray into another record or off the card)", free, outside, claimLBA)
	}

	// (7) The premature finalize (count=3) must reply 'E' (error), not 'D'.
	if got := countPayload(enc.TXFrames(), []byte{'E'}); got < 1 {
		t.Errorf("premature finalize (3 sectors) did not reply 'E'; tx payloads=%v", txPayloads(enc.TXFrames()))
	}
	if got := countPayload(enc.TXFrames(), []byte{'D'}); got != 0 {
		t.Errorf("premature finalize replied 'D' (%d) — a 3-sector record must NOT validate as complete", got)
	}
}

// TestSDPushFinalizeComplete drives a finalize after exactly 1600 sectors and asserts
// the 'D' (done) reply — the size-only "a record is 1600 sectors" check. It streams a
// full 1600-sector record (each linearSec distinct) and confirms all 1600 body CMD24s
// land inside record 1's absolute LBA band, then finalize returns 'D'.
func TestSDPushFinalizeComplete(t *testing.T) {
	mac, enc, sd := setupSDPushMain(t, z80h.CSDForV2(0x001D59))

	sec := make([]byte, 512)
	for i := range sec {
		sec[i] = byte(i & 0xFF)
	}
	enc.InjectRX(sdPushFrame([]byte{'?'}))
	for s := 0; s < 1600; s++ {
		enc.InjectRX(sdPushBlock(uint16(s), sec))
	}
	enc.InjectRX(sdPushFrame([]byte{'F'}))

	res, err := mac.RunBoot("sd_push_main", z80h.Entry{StepCap: 400_000_000})
	if err != nil {
		t.Fatalf("RunBoot sd_push_main faulted: %v", err)
	}
	writes := sd.WrittenSectors()
	t.Logf("sd_push finalize-complete: finalPC=&%04X steps=%d writes=%d tx=%d",
		res.PC, res.Steps, len(writes), len(enc.TXFrames()))

	sym := func(name string) uint16 {
		a, err := mac.Sym(name)
		if err != nil {
			t.Fatalf("sd_push symbol %q absent from %s", name, sdPushMap)
		}
		return a
	}
	base := int(leU16(mac.Read(sym("csd_base"), 2)))
	free := int(leU16(mac.Read(sym("BD_FREE_RECORD"), 2)))
	if base != spCSDBase || free != 1 {
		t.Fatalf("csd_base=%d free=%d, want %d and 1 (record 1 on an empty list)", base, free, spCSDBase)
	}

	// All 1600 body sectors must have landed inside record 1's band. WrittenSectors
	// also holds the single catalogue-claim write (list sector 1), so the total is 1601,
	// with exactly one write outside the body band (the claim).
	if outside := sd.WrittenSectorsOutsideRecord(uint32(base), free); len(outside) != 1 || outside[0] != 1 {
		t.Fatalf("writes outside record %d's body band = %v, want exactly [1] (only the list-sector claim)", free, outside)
	}
	if got := len(writes); got != 1601 {
		t.Fatalf("CMD24 writes = %d, want 1601 (1600 body sectors + 1 catalogue claim) — the stream did not all write", got)
	}
	// Spot-check the band edges landed at the right absolute LBAs.
	for _, linear := range []int{0, 799, 1599} {
		if _, ok := sd.RecordDataSector(uint32(base), free, linear); !ok {
			t.Fatalf("record %d linear sector %d (LBA %d) not present — a body write is missing", free, linear, uint32(base)+1600*uint32(free-1)+uint32(linear))
		}
	}

	if got := countPayload(enc.TXFrames(), []byte{'D'}); got < 1 {
		t.Errorf("finalize after 1600 sectors did not reply 'D' (done); tx payloads=%v", txPayloads(enc.TXFrames()))
	}
	if got := countPayload(enc.TXFrames(), []byte{'E'}); got != 0 {
		t.Errorf("finalize after a complete 1600-sector record replied 'E' (%d) — should validate", got)
	}
}

// countPayload counts TX frames whose UDP payload exactly equals want.
func countPayload(frames [][]byte, want []byte) int {
	n := 0
	for _, f := range frames {
		if u, ok := frame.ParseUDP(f); ok && bytes.Equal(u.Payload, want) {
			n++
		}
	}
	return n
}

// txPayloads returns the UDP payloads of all TX frames (for diagnostics).
func txPayloads(frames [][]byte) [][]byte {
	var out [][]byte
	for _, f := range frames {
		if u, ok := frame.ParseUDP(f); ok {
			out = append(out, u.Payload)
		}
	}
	return out
}

// equalU32 reports whether two uint32 slices are element-wise equal.
func equalU32(a, b []uint32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
