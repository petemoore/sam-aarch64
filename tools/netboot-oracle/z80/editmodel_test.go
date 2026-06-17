// editmodel_test.go — host-verification of src/editmodel.asm Brick 1.
//
// Drives the Z80 block-list routines (em_reset, em_insert, em_line_count,
// em_block_count, em_line_at, em_goto) under the flat-memory koron-go/z80
// harness and asserts they produce the same logical sequence as a plain Go
// oracle slice driven through identical random inserts.
//
// The oracle is an []struct{id uint32; text []byte} maintained directly here
// — we deliberately do NOT import the editmodel Go package; the oracle is
// only the expected logical sequence, not a second implementation of the
// block-list algorithm. Each insert picks a random position and random text,
// and after all inserts we verify:
//   - em_line_count matches len(oracle)
//   - em_block_count > 1 (splits genuinely occurred)
//   - for every index i: em_line_at(i) returns oracle[i].id and oracle[i].text
//   - for every oracle entry: em_goto(id) returns found=1 and the correct index
//
// The test uses two fixed seeds so results are deterministic.
// With EM_BLOCK_CAP=256 and records of 12..44 bytes, 120 inserts produce
// several splits, proving the split + EM_LOC update path.
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
