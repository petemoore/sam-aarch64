// sd_push_test.go — i293 LOGIC test (CI-safe; SKIP_PRIVATE_TESTS-green).
//
// Runs the real sd_push binary (build/sd_push.bin) end-to-end under the flat-memory
// harness, exercising its receive -> free-record-pick -> per-sector HWSAD-dispatch
// -> ACK logic deterministically, with NO proprietary captures:
//
//   - the "Trinity Network " EEPROM chunk is served by the ENC EEPROM model
//     (ProgramTrinityNetwork), so sd_push_main reads the SAM MAC/IP;
//   - the inserted card's CSD is served by the SD model (AttachSD), so
//     csd_set_bd_records computes BD_RECORDS + csd_base;
//   - the free-record list read is the REAL CMD17 (NETBOOT_REAL_LISTREAD) against the
//     SD model — an empty store reads record 1's list entry as all-zero => FREE, so
//     bdos_find_free_record auto-picks record 1;
//   - the HRECORD select + HWSAD writes are RST 8 hooks the flat harness intercepts
//     via AttachBDOS (the SAME pattern bdos_write_test.go uses for the real Z80
//     bdos_write_sector). This exercises the DISPATCH; the FAITHFUL real-ROM CMD24
//     surface is sd_push_faithful_test.go.
//
// What this proves: sd_push's wire loop (discovery reply, the @-block linearSec
// decode, the per-sector HWSAD dispatch to the picked record, the finalize count
// check) is correct in emulation. What it does NOT prove (left to the faithful rig +
// hardware): that the real ROM HWSAD handler issues a CMD24 that lands. Emulation-
// verified is not hardware-verified (CLAUDE.md §5).
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
)

// The SAM identity sd_push_main reads from the flash chunk (ProgramTrinityNetwork
// writes sam_mac at chunk+0, sam_ip at chunk+6). All our frames address it.
var (
	spSAMMac    = [6]byte{0x02, 0x54, 0x52, 0x49, 0x4e, 0xbc} // the first-light SAM MAC shape
	spSAMIp     = [4]byte{192, 168, 2, 75}
	spHostMac   = [6]byte{0x02, 0x00, 0x00, 0x00, 0x00, 0x44}
	spHostIp    = [4]byte{192, 168, 2, 99}
	spHostTID   = uint16(40000)
	sdPushPort  = uint16(0xEDB0)
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

// TestSDPushLogic drives sd_push end-to-end under the flat harness with a small
// 3-sector .mgt stream and asserts: (1) discovery is answered ('!'); (2) each '@'
// block is HWSAD-written to the auto-picked free record at the right linear sector
// with the right bytes; (3) finalize on a non-1600 count replies 'E' (the size-only
// validation works), and on exactly 1600 replies 'D'.
func TestSDPushLogic(t *testing.T) {
	mac := loadSDPush(t)
	enc := z80h.NewENC28J60()
	enc.ProgramTrinityNetwork(spSAMMac, spSAMIp) // so the EEPROM config read succeeds
	enc.AttachSD(z80h.CSDForV2(0x001D59))        // 4809 records, base 152 — so BD_RECORDS>=1 + csd_base set
	mac.AttachIO(enc)
	store := z80h.NewBDOSStore()
	card := z80h.NewCardModel()
	store.AttachCard(card)
	mac.AttachBDOS(store) // intercept the HRECORD select + HWSAD writes

	// Three distinct 512-byte sectors at linear 0,1,2 (the start of record 1).
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
	// loop after the queue drains (Esc-to-exit), so RunBoot returns at the step cap.
	enc.InjectRX(sdPushFrame([]byte{'?'}))
	for s, sec := range sectors {
		enc.InjectRX(sdPushBlock(uint16(s), sec))
	}
	enc.InjectRX(sdPushFrame([]byte{'F'}))

	res, err := mac.RunBoot("sd_push_main", z80h.Entry{StepCap: 12_000_000})
	if err != nil {
		t.Fatalf("RunBoot sd_push_main faulted: %v", err)
	}
	t.Logf("sd_push: halted=%v finalPC=&%04X steps=%d tx=%d selected=%d writes=%d",
		res.Halted, res.PC, res.Steps, len(enc.TXFrames()), store.Selected(), len(store.SectorWrites()))

	// (1) The auto-picked free record must be record 1 (empty list => record 1 free).
	if store.Selected() != 1 {
		t.Errorf("HRECORD selected record %d, want 1 (the first free record on an empty list)", store.Selected())
	}

	// (2) Discovery must have been answered with '!'.
	if got := countPayload(enc.TXFrames(), []byte{'!'}); got < 1 {
		t.Errorf("no '!' discovery reply among %d TX frames", len(enc.TXFrames()))
	}

	// (3) Exactly three HWSAD writes, to record 1, at linear sectors 0,1,2, byte-exact.
	writes := store.SectorWrites()
	if len(writes) != 3 {
		t.Fatalf("SectorWrites() = %d, want 3 — the @-blocks did not all HWSAD-dispatch", len(writes))
	}
	for s, w := range writes {
		if w.Record != 1 {
			t.Errorf("write %d: record = %d, want 1 (the picked free record)", s, w.Record)
		}
		if w.LinearSec != s {
			t.Errorf("write %d: linearSec = %d, want %d (track-major linearSec decode)", s, w.LinearSec, s)
		}
		if !bytes.Equal(w.Data[:], sectors[s]) {
			t.Errorf("write %d: data mismatch (first byte got %#x want %#x) — the block payload was not carried", s, w.Data[0], sectors[s][0])
		}
	}

	// (4) The premature finalize (count=3) must reply 'E' (error), not 'D'.
	if got := countPayload(enc.TXFrames(), []byte{'E'}); got < 1 {
		t.Errorf("premature finalize (3 sectors) did not reply 'E'; tx payloads=%v", txPayloads(enc.TXFrames()))
	}
	if got := countPayload(enc.TXFrames(), []byte{'D'}); got != 0 {
		t.Errorf("premature finalize replied 'D' (%d) — a 3-sector record must NOT validate as complete", got)
	}
}

// TestSDPushFinalizeComplete drives a finalize after exactly 1600 sectors and
// asserts the 'D' (done) reply — the size-only "a record is 1600 sectors" check.
// To keep the run bounded it pushes the same single sector 1600 times (each a
// distinct linearSec), which is enough to exercise the count gate without a full
// 819200-byte payload.
func TestSDPushFinalizeComplete(t *testing.T) {
	mac := loadSDPush(t)
	enc := z80h.NewENC28J60()
	enc.ProgramTrinityNetwork(spSAMMac, spSAMIp)
	enc.AttachSD(z80h.CSDForV2(0x001D59))
	mac.AttachIO(enc)
	store := z80h.NewBDOSStore()
	card := z80h.NewCardModel()
	store.AttachCard(card)
	mac.AttachBDOS(store)

	sec := make([]byte, 512)
	for i := range sec {
		sec[i] = byte(i & 0xFF)
	}
	enc.InjectRX(sdPushFrame([]byte{'?'}))
	for s := 0; s < 1600; s++ {
		enc.InjectRX(sdPushBlock(uint16(s), sec))
	}
	enc.InjectRX(sdPushFrame([]byte{'F'}))

	res, err := mac.RunBoot("sd_push_main", z80h.Entry{StepCap: 200_000_000})
	if err != nil {
		t.Fatalf("RunBoot sd_push_main faulted: %v", err)
	}
	t.Logf("sd_push finalize-complete: finalPC=&%04X steps=%d writes=%d tx=%d",
		res.PC, res.Steps, len(store.SectorWrites()), len(enc.TXFrames()))

	if got := len(store.SectorWrites()); got != 1600 {
		t.Fatalf("HWSAD writes = %d, want 1600 (a full record) — the stream did not all dispatch", got)
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
