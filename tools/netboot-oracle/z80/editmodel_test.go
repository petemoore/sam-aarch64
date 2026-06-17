// editmodel_test.go — host-verification of src/editmodel.asm Brick 1a+1b.
//
// Drives the Z80 block-list routines under the flat-memory koron-go/z80
// harness and asserts they produce the same logical sequence as a plain Go
// oracle slice driven through identical random ops.
//
// TestEditModelBlockListZ80: Brick 1a (insert-only). 120 inserts, two seeds.
//
//	Verifies em_insert, em_line_count, em_block_count, em_line_at, em_goto.
//
// TestEditModelDeleteMergeZ80: Brick 1b (delete + merge). Build-up phase of
//
//	120 inserts, then an interleaved phase of ~200 ops (~50% insert / ~50%
//	delete). Verifies em_delete, merge-on-underflow, descriptor reuse, and
//	the EM_LOC_ABSENT sentinel for em_goto on deleted ids.
package z80_test

import (
	"encoding/binary"
	"fmt"
	"math/rand"
	"os"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	emBinPath = "../../../build/editmodel.bin"
	emMapPath = "../../../build/editmodel.map"
)

func loadEditModel(t *testing.T) *z80h.Machine {
	t.Helper()
	if _, err := os.Stat(emBinPath); err != nil {
		t.Skipf("editmodel binary not built (%s); run `make editmodel-z80`", emBinPath)
	}
	mac, err := z80h.Load(emBinPath, emMapPath)
	if err != nil {
		t.Fatalf("load editmodel: %v", err)
	}
	return mac
}

func emSym(t *testing.T, mac *z80h.Machine, name string) uint16 {
	t.Helper()
	addr, err := mac.Sym(name)
	if err != nil {
		t.Fatalf("symbol %q: %v", name, err)
	}
	return addr
}

// oracleLine is one line in the oracle slice.
type oracleLine struct {
	id   uint32
	text []byte
}

// emReadU24LE reads a 3-byte little-endian u24 from buf.
func emReadU24LE(buf []byte) uint32 {
	return uint32(buf[0]) | uint32(buf[1])<<8 | uint32(buf[2])<<16
}

// TestEditModelBlockListZ80 verifies the Z80 block-list against a Go oracle
// for two fixed seeds.
func TestEditModelBlockListZ80(t *testing.T) {
	for _, seed := range []int64{42, 137} {
		seed := seed
		t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) {
			testEditModelWithSeed(t, seed)
		})
	}
}

func testEditModelWithSeed(t *testing.T, seed int64) {
	mac := loadEditModel(t)

	symInText := emSym(t, mac, "EM_IN_TEXT")
	symInID := emSym(t, mac, "EM_IN_ID")
	symOutID := emSym(t, mac, "EM_OUT_ID")
	symOutText := emSym(t, mac, "EM_OUT_TEXT")

	// Reset the document.
	if _, err := mac.Call("em_reset"); err != nil {
		t.Fatalf("em_reset: %v", err)
	}

	rng := rand.New(rand.NewSource(seed))
	const numInserts = 120

	// oracle is the expected logical sequence of lines.
	oracle := make([]oracleLine, 0, numInserts)

	for step := 0; step < numInserts; step++ {
		count := len(oracle)
		at := rng.Intn(count + 1)

		// Random text length 8..40 bytes.
		textLen := 8 + rng.Intn(33)
		text := make([]byte, textLen)
		for i := range text {
			text[i] = byte(0x20 + rng.Intn(0x5f)) // printable ASCII
		}

		// Write text into EM_IN_TEXT.
		mac.Write(symInText, text)

		// Call em_insert with BC=at, A=textLen.
		res, err := mac.CallEntry("em_insert", z80h.Entry{
			BC: uint16(at),
			A:  uint8(textLen),
		})
		if err != nil {
			t.Fatalf("step %d: em_insert: %v", step, err)
		}
		_ = res

		// Read the new id from EM_OUT_ID (3 bytes LE).
		idBytes := mac.Read(symOutID, 3)
		newID := emReadU24LE(idBytes)
		if newID == 0 {
			t.Fatalf("step %d: em_insert returned id=0", step)
		}

		// Insert into oracle at position at.
		oracleText := make([]byte, len(text))
		copy(oracleText, text)
		oracle = append(oracle, oracleLine{})
		copy(oracle[at+1:], oracle[at:])
		oracle[at] = oracleLine{id: newID, text: oracleText}
	}

	// --- em_line_count ---
	lcRes, err := mac.Call("em_line_count")
	if err != nil {
		t.Fatalf("em_line_count: %v", err)
	}
	if int(lcRes.HL) != len(oracle) {
		t.Errorf("em_line_count = %d, want %d", lcRes.HL, len(oracle))
	}

	// --- em_block_count ---
	bcRes, err := mac.Call("em_block_count")
	if err != nil {
		t.Fatalf("em_block_count: %v", err)
	}
	if bcRes.HL <= 1 {
		t.Errorf("em_block_count = %d, want > 1 (splits must have occurred)", bcRes.HL)
	}
	t.Logf("seed=%d: %d lines, %d blocks after %d inserts", seed, len(oracle), bcRes.HL, numInserts)

	// --- em_line_at for every index ---
	for i, want := range oracle {
		res, err := mac.CallEntry("em_line_at", z80h.Entry{BC: uint16(i)})
		if err != nil {
			t.Fatalf("em_line_at(%d): %v", i, err)
		}
		_ = res

		// Read id from EM_OUT_ID.
		gotIDBytes := mac.Read(symOutID, 3)
		gotID := emReadU24LE(gotIDBytes)
		if gotID != want.id {
			t.Errorf("em_line_at(%d): id = %d, want %d", i, gotID, want.id)
		}

		// Read [len][text] from EM_OUT_TEXT.
		outTextHeader := mac.Read(symOutText, 1)
		gotLen := int(outTextHeader[0])
		if gotLen != len(want.text) {
			t.Errorf("em_line_at(%d): text len = %d, want %d", i, gotLen, len(want.text))
			continue
		}
		if gotLen > 0 {
			gotText := mac.Read(symOutText+1, gotLen)
			for j := 0; j < gotLen; j++ {
				if gotText[j] != want.text[j] {
					t.Errorf("em_line_at(%d): text[%d] = %02x, want %02x", i, j, gotText[j], want.text[j])
					break
				}
			}
		}
	}

	// --- em_goto for every oracle entry ---
	for i, want := range oracle {
		// Write id to EM_IN_ID (3 bytes LE).
		var idBuf [3]byte
		idBuf[0] = byte(want.id)
		idBuf[1] = byte(want.id >> 8)
		idBuf[2] = byte(want.id >> 16)
		mac.Write(symInID, idBuf[:])

		res, err := mac.Call("em_goto")
		if err != nil {
			t.Fatalf("em_goto(id=%d, oracle[%d]): %v", want.id, i, err)
		}
		if res.A != 1 {
			t.Errorf("em_goto(id=%d, oracle[%d]): found=%d, want 1", want.id, i, res.A)
			continue
		}
		if int(res.HL) != i {
			t.Errorf("em_goto(id=%d): index=%d, want %d", want.id, res.HL, i)
		}
	}

	// --- em_goto for a non-existent id returns not-found ---
	var badID [3]byte
	binary.LittleEndian.PutUint16(badID[:2], 0xFFFF)
	badID[2] = 0x0F
	mac.Write(symInID, badID[:])
	res, err := mac.Call("em_goto")
	if err != nil {
		t.Fatalf("em_goto(bad id): %v", err)
	}
	if res.A != 0 {
		t.Errorf("em_goto(nonexistent id): found=%d, want 0", res.A)
	}
}

// TestEditModelDeleteMergeZ80 verifies em_delete (Brick 1b): deletion,
// merge-on-underflow, descriptor reuse, and the EM_LOC_ABSENT sentinel.
//
// Structure: build-up phase (120 inserts, forced splits) then interleaved
// phase (~200 ops, ~50/50 insert/delete). The oracle is a plain slice of
// {id, text} pairs; deleted ids are tracked separately to verify that
// em_goto returns not-found for them after deletion.
func TestEditModelDeleteMergeZ80(t *testing.T) {
	for _, seed := range []int64{42, 137, 999} {
		seed := seed
		t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) {
			testEditModelDeleteMergeWithSeed(t, seed)
		})
	}
}

func testEditModelDeleteMergeWithSeed(t *testing.T, seed int64) {
	mac := loadEditModel(t)

	symInText := emSym(t, mac, "EM_IN_TEXT")
	symInID := emSym(t, mac, "EM_IN_ID")
	symOutID := emSym(t, mac, "EM_OUT_ID")
	symOutText := emSym(t, mac, "EM_OUT_TEXT")

	if _, err := mac.Call("em_reset"); err != nil {
		t.Fatalf("em_reset: %v", err)
	}

	rng := rand.New(rand.NewSource(seed))

	// oracle is the live logical sequence; deletedIDs tracks recently deleted ids.
	oracle := make([]oracleLine, 0, 320)
	deletedIDs := make([]uint32, 0, 200)

	// emInsert performs one insert into the Z80 model and the oracle.
	emInsert := func(at int) {
		textLen := 8 + rng.Intn(33)
		text := make([]byte, textLen)
		for i := range text {
			text[i] = byte(0x20 + rng.Intn(0x5f))
		}
		mac.Write(symInText, text)
		_, err := mac.CallEntry("em_insert", z80h.Entry{
			BC: uint16(at),
			A:  uint8(textLen),
		})
		if err != nil {
			t.Fatalf("em_insert(at=%d): %v", at, err)
		}
		idBytes := mac.Read(symOutID, 3)
		newID := emReadU24LE(idBytes)
		if newID == 0 {
			t.Fatalf("em_insert returned id=0")
		}
		// Insert into oracle.
		oracleText := make([]byte, len(text))
		copy(oracleText, text)
		oracle = append(oracle, oracleLine{})
		copy(oracle[at+1:], oracle[at:])
		oracle[at] = oracleLine{id: newID, text: oracleText}
	}

	// emDelete performs one delete from the Z80 model and the oracle.
	emDelete := func(at int) {
		_, err := mac.CallEntry("em_delete", z80h.Entry{BC: uint16(at)})
		if err != nil {
			t.Fatalf("em_delete(at=%d): %v", at, err)
		}
		deletedIDs = append(deletedIDs, oracle[at].id)
		oracle = append(oracle[:at], oracle[at+1:]...)
	}

	// Build-up phase: 120 inserts to force splits.
	const buildCount = 120
	for step := 0; step < buildCount; step++ {
		at := rng.Intn(len(oracle) + 1)
		emInsert(at)
	}

	// Interleaved phase: ~200 ops, ~50% insert / ~50% delete.
	const mixCount = 200
	for step := 0; step < mixCount; step++ {
		if len(oracle) == 0 || rng.Intn(2) == 0 {
			at := rng.Intn(len(oracle) + 1)
			emInsert(at)
		} else {
			at := rng.Intn(len(oracle))
			emDelete(at)
		}
	}

	// --- Verify em_line_count ---
	lcRes, err := mac.Call("em_line_count")
	if err != nil {
		t.Fatalf("em_line_count: %v", err)
	}
	if int(lcRes.HL) != len(oracle) {
		t.Errorf("em_line_count = %d, want %d", lcRes.HL, len(oracle))
	}

	// --- Verify em_block_count (merges should keep it bounded) ---
	bcRes, err := mac.Call("em_block_count")
	if err != nil {
		t.Fatalf("em_block_count: %v", err)
	}
	t.Logf("seed=%d: %d lines, %d blocks after build+mix", seed, len(oracle), bcRes.HL)

	// --- Verify em_line_at for every index ---
	for i, want := range oracle {
		res, err := mac.CallEntry("em_line_at", z80h.Entry{BC: uint16(i)})
		if err != nil {
			t.Fatalf("em_line_at(%d): %v", i, err)
		}
		_ = res

		gotIDBytes := mac.Read(symOutID, 3)
		gotID := emReadU24LE(gotIDBytes)
		if gotID != want.id {
			t.Errorf("em_line_at(%d): id = %d, want %d", i, gotID, want.id)
		}

		outTextHeader := mac.Read(symOutText, 1)
		gotLen := int(outTextHeader[0])
		if gotLen != len(want.text) {
			t.Errorf("em_line_at(%d): text len = %d, want %d", i, gotLen, len(want.text))
			continue
		}
		if gotLen > 0 {
			gotText := mac.Read(symOutText+1, gotLen)
			for j := 0; j < gotLen; j++ {
				if gotText[j] != want.text[j] {
					t.Errorf("em_line_at(%d): text[%d] = %02x, want %02x", i, j, gotText[j], want.text[j])
					break
				}
			}
		}
	}

	// --- Verify em_goto for every live oracle entry ---
	for i, want := range oracle {
		var idBuf [3]byte
		idBuf[0] = byte(want.id)
		idBuf[1] = byte(want.id >> 8)
		idBuf[2] = byte(want.id >> 16)
		mac.Write(symInID, idBuf[:])
		res, err := mac.Call("em_goto")
		if err != nil {
			t.Fatalf("em_goto(id=%d, oracle[%d]): %v", want.id, i, err)
		}
		if res.A != 1 {
			t.Errorf("em_goto(id=%d, oracle[%d]): found=%d, want 1", want.id, i, res.A)
			continue
		}
		if int(res.HL) != i {
			t.Errorf("em_goto(id=%d): index=%d, want %d", want.id, res.HL, i)
		}
	}

	// --- Verify em_goto returns not-found for a sample of deleted ids ---
	// The EM_LOC_ABSENT (&FF) sentinel must cause em_goto to return A=0.
	sampleSize := len(deletedIDs)
	if sampleSize > 20 {
		sampleSize = 20
	}
	// Take the last sampleSize deleted ids (most recently deleted, most likely
	// to have triggered a merge).
	start := len(deletedIDs) - sampleSize
	for _, delID := range deletedIDs[start:] {
		var idBuf [3]byte
		idBuf[0] = byte(delID)
		idBuf[1] = byte(delID >> 8)
		idBuf[2] = byte(delID >> 16)
		mac.Write(symInID, idBuf[:])
		res, err := mac.Call("em_goto")
		if err != nil {
			t.Fatalf("em_goto(deleted id=%d): %v", delID, err)
		}
		if res.A != 0 {
			t.Errorf("em_goto(deleted id=%d): found=%d, want 0 (EM_LOC_ABSENT sentinel broken)", delID, res.A)
		}
	}
}
