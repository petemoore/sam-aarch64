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
//
// TestEditModelUndoRedoZ80: Brick 3 (bounded ring-journal undo/redo). A random
//
//	walk of insert/delete/undo/redo ops compared after every step against a Go
//	reference that mirrors the bounded-journal semantics (drop-oldest cap,
//	id-stable undo-of-delete). Verifies em_undo, em_redo, em_can_undo,
//	em_can_redo.
//
// TestEditModelUndoDropOldestZ80: Brick 3 deterministic drop-oldest check —
//
//	EM_MAX_UNDO+5 inserts, then undo until em_can_undo is false; exactly
//	EM_MAX_UNDO undos succeed and the oldest 5 lines survive.
package z80_test

import (
	"bytes"
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
		t.Fatalf("editmodel binary not built (%s); run `make editmodel-z80`", emBinPath)
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

// ---------------------------------------------------------------------------
// Brick 3 — bounded ring-journal undo/redo.
// ---------------------------------------------------------------------------

// emMaxUndo MUST match EM_MAX_UNDO in src/editmodel.asm. The Z80 ring is sized
// for the flat-memory harness; this constant lets the reference apply the same
// drop-oldest cap so the two agree bit-for-bit on undo availability.
const emMaxUndo = 16

// refEntry mirrors editmodel.go's editEntry: an insert or delete with the
// document index, record id, and a copy of the line text.
type refEntry struct {
	op   int // 0 = insert, 1 = delete
	at   int
	id   uint32
	text []byte
}

// refDoc is an independent Go reference for the Z80 edit model: a flat slice of
// lines plus undo/redo stacks implementing the exact editmodel.go semantics
// (doInsert/doDelete/IndexOf/pushUndo/Undo/Redo) at the Z80's EM_MAX_UNDO cap.
type refDoc struct {
	lines []oracleLine
	undo  []refEntry
	redo  []refEntry
}

func (d *refDoc) doInsert(at int, id uint32, text []byte) {
	t := append([]byte(nil), text...)
	d.lines = append(d.lines, oracleLine{})
	copy(d.lines[at+1:], d.lines[at:])
	d.lines[at] = oracleLine{id: id, text: t}
}

func (d *refDoc) doDelete(at int) (uint32, []byte) {
	id := d.lines[at].id
	text := append([]byte(nil), d.lines[at].text...)
	d.lines = append(d.lines[:at], d.lines[at+1:]...)
	return id, text
}

func (d *refDoc) indexOf(id uint32) (int, bool) {
	for i := range d.lines {
		if d.lines[i].id == id {
			return i, true
		}
	}
	return 0, false
}

// pushUndo appends to the undo stack with drop-oldest at emMaxUndo (design §7.2).
func (d *refDoc) pushUndo(e refEntry) {
	d.undo = append(d.undo, e)
	if len(d.undo) > emMaxUndo {
		d.undo = d.undo[1:]
	}
}

// insert is the public insert: structural insert + journal + clear redo. The id
// is taken from the Z80 (em_insert allocates it), so both models share ids.
func (d *refDoc) insert(at int, id uint32, text []byte) {
	d.doInsert(at, id, text)
	d.pushUndo(refEntry{op: 0, at: at, id: id, text: append([]byte(nil), text...)})
	d.redo = d.redo[:0]
}

func (d *refDoc) delete(at int) {
	id, text := d.doDelete(at)
	d.pushUndo(refEntry{op: 1, at: at, id: id, text: text})
	d.redo = d.redo[:0]
}

func (d *refDoc) undoOp() bool {
	if len(d.undo) == 0 {
		return false
	}
	e := d.undo[len(d.undo)-1]
	d.undo = d.undo[:len(d.undo)-1]
	if e.op == 0 {
		idx, ok := d.indexOf(e.id)
		if !ok {
			panic("ref undoOp insert: id not live")
		}
		d.doDelete(idx)
	} else {
		d.doInsert(e.at, e.id, e.text)
	}
	d.redo = append(d.redo, e)
	return true
}

func (d *refDoc) redoOp() bool {
	if len(d.redo) == 0 {
		return false
	}
	e := d.redo[len(d.redo)-1]
	d.redo = d.redo[:len(d.redo)-1]
	if e.op == 0 {
		d.doInsert(e.at, e.id, e.text)
	} else {
		idx, ok := d.indexOf(e.id)
		if !ok {
			panic("ref redoOp delete: id not live")
		}
		d.doDelete(idx)
	}
	d.pushUndo(e)
	return true
}

func (d *refDoc) canUndo() bool { return len(d.undo) > 0 }
func (d *refDoc) canRedo() bool { return len(d.redo) > 0 }

// emRandText builds n bytes of printable ASCII. n must stay <= EM_UNDO_TEXT_MAX.
func emRandText(rng *rand.Rand, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(0x20 + rng.Intn(0x5f))
	}
	return b
}

// emCompareDoc asserts the Z80 model's full line sequence equals the reference.
func emCompareDoc(t *testing.T, mac *z80h.Machine, ref *refDoc, symOutID, symOutText uint16, step int, label string) {
	t.Helper()
	lcRes, err := mac.Call("em_line_count")
	if err != nil {
		t.Fatalf("step %d (%s): em_line_count: %v", step, label, err)
	}
	if int(lcRes.HL) != len(ref.lines) {
		t.Fatalf("step %d (%s): line_count = %d, want %d", step, label, lcRes.HL, len(ref.lines))
	}
	for i, want := range ref.lines {
		if _, err := mac.CallEntry("em_line_at", z80h.Entry{BC: uint16(i)}); err != nil {
			t.Fatalf("step %d (%s): em_line_at(%d): %v", step, label, i, err)
		}
		gotID := emReadU24LE(mac.Read(symOutID, 3))
		if gotID != want.id {
			t.Fatalf("step %d (%s): line_at(%d) id = %d, want %d", step, label, i, gotID, want.id)
		}
		gotLen := int(mac.Read(symOutText, 1)[0])
		if gotLen != len(want.text) {
			t.Fatalf("step %d (%s): line_at(%d) len = %d, want %d", step, label, i, gotLen, len(want.text))
		}
		if gotLen > 0 {
			if got := mac.Read(symOutText+1, gotLen); !bytes.Equal(got, want.text) {
				t.Fatalf("step %d (%s): line_at(%d) text mismatch", step, label, i)
			}
		}
	}
}

// emCompareFlags asserts em_can_undo / em_can_redo match the reference.
func emCompareFlags(t *testing.T, mac *z80h.Machine, ref *refDoc, step int, label string) {
	t.Helper()
	cu, err := mac.Call("em_can_undo")
	if err != nil {
		t.Fatalf("step %d (%s): em_can_undo: %v", step, label, err)
	}
	if (cu.A == 1) != ref.canUndo() {
		t.Fatalf("step %d (%s): can_undo = %d, want %v", step, label, cu.A, ref.canUndo())
	}
	cr, err := mac.Call("em_can_redo")
	if err != nil {
		t.Fatalf("step %d (%s): em_can_redo: %v", step, label, err)
	}
	if (cr.A == 1) != ref.canRedo() {
		t.Fatalf("step %d (%s): can_redo = %d, want %v", step, label, cr.A, ref.canRedo())
	}
}

// TestEditModelUndoRedoZ80 drives a random walk of insert/delete/undo/redo and
// compares the Z80 model against the Go reference after every step.
func TestEditModelUndoRedoZ80(t *testing.T) {
	for _, seed := range []int64{42, 137, 999} {
		seed := seed
		t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) {
			testEditModelUndoRedoWithSeed(t, seed)
		})
	}
}

func testEditModelUndoRedoWithSeed(t *testing.T, seed int64) {
	mac := loadEditModel(t)

	symInText := emSym(t, mac, "EM_IN_TEXT")
	symOutID := emSym(t, mac, "EM_OUT_ID")
	symOutText := emSym(t, mac, "EM_OUT_TEXT")

	if _, err := mac.Call("em_reset"); err != nil {
		t.Fatalf("em_reset: %v", err)
	}

	rng := rand.New(rand.NewSource(seed))
	ref := &refDoc{}

	doInsert := func(at int) {
		textLen := 8 + rng.Intn(33) // 8..40, <= EM_UNDO_TEXT_MAX
		text := emRandText(rng, textLen)
		mac.Write(symInText, text)
		if _, err := mac.CallEntry("em_insert", z80h.Entry{BC: uint16(at), A: uint8(textLen)}); err != nil {
			t.Fatalf("em_insert(at=%d): %v", at, err)
		}
		newID := emReadU24LE(mac.Read(symOutID, 3))
		if newID == 0 {
			t.Fatalf("em_insert returned id=0")
		}
		ref.insert(at, newID, text)
	}
	doDelete := func(at int) {
		if _, err := mac.CallEntry("em_delete", z80h.Entry{BC: uint16(at)}); err != nil {
			t.Fatalf("em_delete(at=%d): %v", at, err)
		}
		ref.delete(at)
	}
	doUndo := func(step int) {
		res, err := mac.Call("em_undo")
		if err != nil {
			t.Fatalf("em_undo: %v", err)
		}
		if got, want := res.A == 1, ref.undoOp(); got != want {
			t.Fatalf("step %d: em_undo performed=%v, want %v", step, got, want)
		}
	}
	doRedo := func(step int) {
		res, err := mac.Call("em_redo")
		if err != nil {
			t.Fatalf("em_redo: %v", err)
		}
		if got, want := res.A == 1, ref.redoOp(); got != want {
			t.Fatalf("step %d: em_redo performed=%v, want %v", step, got, want)
		}
	}

	const steps = 300
	for step := 0; step < steps; step++ {
		r := rng.Intn(100)
		switch {
		case len(ref.lines) == 0:
			doInsert(0)
		case r < 45:
			doInsert(rng.Intn(len(ref.lines) + 1))
		case r < 62:
			doDelete(rng.Intn(len(ref.lines)))
		case r < 85:
			doUndo(step)
		default:
			doRedo(step)
		}
		emCompareDoc(t, mac, ref, symOutID, symOutText, step, "walk")
		emCompareFlags(t, mac, ref, step, "walk")
	}

	t.Logf("seed=%d: %d lines after %d-step undo/redo walk", seed, len(ref.lines), steps)
}

// TestEditModelUndoDropOldestZ80 deterministically exercises the drop-oldest
// cap: after EM_MAX_UNDO+5 end-inserts, only the most recent EM_MAX_UNDO are
// undoable, so exactly EM_MAX_UNDO undos succeed and the oldest 5 lines survive.
func TestEditModelUndoDropOldestZ80(t *testing.T) {
	mac := loadEditModel(t)

	symInText := emSym(t, mac, "EM_IN_TEXT")
	symOutID := emSym(t, mac, "EM_OUT_ID")

	if _, err := mac.Call("em_reset"); err != nil {
		t.Fatalf("em_reset: %v", err)
	}

	rng := rand.New(rand.NewSource(7))
	const n = emMaxUndo + 5
	ids := make([]uint32, n)
	for i := 0; i < n; i++ {
		textLen := 8 + rng.Intn(20)
		text := emRandText(rng, textLen)
		mac.Write(symInText, text)
		if _, err := mac.CallEntry("em_insert", z80h.Entry{BC: uint16(i), A: uint8(textLen)}); err != nil {
			t.Fatalf("em_insert #%d: %v", i, err)
		}
		ids[i] = emReadU24LE(mac.Read(symOutID, 3))
	}

	lc, err := mac.Call("em_line_count")
	if err != nil {
		t.Fatalf("em_line_count: %v", err)
	}
	if int(lc.HL) != n {
		t.Fatalf("after %d inserts: line_count = %d, want %d", n, lc.HL, n)
	}

	// Undo until em_can_undo reports empty, counting the successful undos.
	undos := 0
	for {
		cu, err := mac.Call("em_can_undo")
		if err != nil {
			t.Fatalf("em_can_undo: %v", err)
		}
		if cu.A == 0 {
			break
		}
		res, err := mac.Call("em_undo")
		if err != nil {
			t.Fatalf("em_undo: %v", err)
		}
		if res.A != 1 {
			t.Fatalf("em_undo returned 0 while em_can_undo was 1")
		}
		undos++
		if undos > n+5 {
			t.Fatalf("undo did not terminate (cap broken)")
		}
	}
	if undos != emMaxUndo {
		t.Fatalf("drop-oldest: %d undos succeeded, want %d", undos, emMaxUndo)
	}

	// The oldest (n - emMaxUndo) lines were never undoable and must survive in order.
	remain := n - emMaxUndo
	lc2, err := mac.Call("em_line_count")
	if err != nil {
		t.Fatalf("em_line_count: %v", err)
	}
	if int(lc2.HL) != remain {
		t.Fatalf("after undo: line_count = %d, want %d", lc2.HL, remain)
	}
	for i := 0; i < remain; i++ {
		if _, err := mac.CallEntry("em_line_at", z80h.Entry{BC: uint16(i)}); err != nil {
			t.Fatalf("em_line_at(%d): %v", i, err)
		}
		gotID := emReadU24LE(mac.Read(symOutID, 3))
		if gotID != ids[i] {
			t.Fatalf("after undo: line %d id = %d, want %d (oldest survivors)", i, gotID, ids[i])
		}
	}

	// em_can_redo must now report the undone edits are redoable.
	cr, err := mac.Call("em_can_redo")
	if err != nil {
		t.Fatalf("em_can_redo: %v", err)
	}
	if cr.A != 1 {
		t.Fatalf("after %d undos: can_redo = %d, want 1", undos, cr.A)
	}
}

// ---------------------------------------------------------------------------
// Brick 4a — EMDL exact serialize/load.
// ---------------------------------------------------------------------------

// emExpectedEMDL builds the EMDL v1 byte stream for an ordered line sequence,
// mirroring Serialize in tools/sam-aarch64/editmodel/serialize.go:
//
//	"EMDL" | ver:1 | linecount:4 LE | per line { id:3 LE | textlen:2 LE | text }
func emExpectedEMDL(oracle []oracleLine) []byte {
	var buf bytes.Buffer
	buf.WriteString("EMDL")
	buf.WriteByte(0x01)
	var u32 [4]byte
	binary.LittleEndian.PutUint32(u32[:], uint32(len(oracle)))
	buf.Write(u32[:])
	for _, l := range oracle {
		buf.WriteByte(byte(l.id))
		buf.WriteByte(byte(l.id >> 8))
		buf.WriteByte(byte(l.id >> 16))
		var u16 [2]byte
		binary.LittleEndian.PutUint16(u16[:], uint16(len(l.text)))
		buf.Write(u16[:])
		buf.Write(l.text)
	}
	return buf.Bytes()
}

// TestEditModelSerializeZ80 verifies em_serialize / em_load (Brick 4a): the Z80
// EMDL bytes match the format spec, the stream round-trips losslessly (same id +
// text sequence, same reserialized bytes), nextID is restored to maxID+1, and a
// corrupt header fails loud without disturbing the live document.
func TestEditModelSerializeZ80(t *testing.T) {
	for _, seed := range []int64{42, 137, 999} {
		seed := seed
		t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) {
			testEditModelSerializeWithSeed(t, seed)
		})
	}
}

func testEditModelSerializeWithSeed(t *testing.T, seed int64) {
	mac := loadEditModel(t)

	symInText := emSym(t, mac, "EM_IN_TEXT")
	symInID := emSym(t, mac, "EM_IN_ID")
	symOutID := emSym(t, mac, "EM_OUT_ID")
	symOutText := emSym(t, mac, "EM_OUT_TEXT")
	symSerBuf := emSym(t, mac, "EM_SER_BUF")

	if _, err := mac.Call("em_reset"); err != nil {
		t.Fatalf("em_reset: %v", err)
	}

	rng := rand.New(rand.NewSource(seed))
	// Bound the document so the serialized stream fits EM_SER_BUF (3072): 50
	// lines * (5 + <=40) + 9 header <= ~2259 bytes.
	const numLines = 50
	oracle := make([]oracleLine, 0, numLines)
	for step := 0; step < numLines; step++ {
		at := rng.Intn(len(oracle) + 1)
		textLen := 8 + rng.Intn(33)
		text := emRandText(rng, textLen)
		mac.Write(symInText, text)
		if _, err := mac.CallEntry("em_insert", z80h.Entry{BC: uint16(at), A: uint8(textLen)}); err != nil {
			t.Fatalf("em_insert(at=%d): %v", at, err)
		}
		newID := emReadU24LE(mac.Read(symOutID, 3))
		if newID == 0 {
			t.Fatalf("em_insert returned id=0")
		}
		oracle = append(oracle, oracleLine{})
		copy(oracle[at+1:], oracle[at:])
		oracle[at] = oracleLine{id: newID, text: text}
	}

	// Splits must have occurred, so the serialize block-walk spans >1 block.
	if bc, err := mac.Call("em_block_count"); err != nil {
		t.Fatalf("em_block_count: %v", err)
	} else if bc.HL <= 1 {
		t.Fatalf("em_block_count = %d, want > 1 (need multi-block coverage)", bc.HL)
	}

	// --- serialize and compare against the EMDL spec bytes ---
	serRes, err := mac.Call("em_serialize")
	if err != nil {
		t.Fatalf("em_serialize: %v", err)
	}
	serLen := int(serRes.HL)
	got := mac.Read(symSerBuf, serLen)
	want := emExpectedEMDL(oracle)
	if !bytes.Equal(got, want) {
		t.Fatalf("em_serialize: %d bytes mismatch vs EMDL spec (want %d bytes)", len(got), len(want))
	}

	// --- reset, load the same bytes, verify the document is reconstructed ---
	if _, err := mac.Call("em_reset"); err != nil {
		t.Fatalf("em_reset: %v", err)
	}
	mac.Write(symSerBuf, want) // bytes persist across em_reset, but be explicit
	ldRes, err := mac.Call("em_load")
	if err != nil {
		t.Fatalf("em_load: %v", err)
	}
	if ldRes.A != 1 {
		t.Fatalf("em_load returned A=%d, want 1 (valid stream)", ldRes.A)
	}

	lcRes, err := mac.Call("em_line_count")
	if err != nil {
		t.Fatalf("em_line_count: %v", err)
	}
	if int(lcRes.HL) != len(oracle) {
		t.Fatalf("after load: line_count = %d, want %d", lcRes.HL, len(oracle))
	}
	for i, wantLine := range oracle {
		if _, err := mac.CallEntry("em_line_at", z80h.Entry{BC: uint16(i)}); err != nil {
			t.Fatalf("em_line_at(%d): %v", i, err)
		}
		gotID := emReadU24LE(mac.Read(symOutID, 3))
		if gotID != wantLine.id {
			t.Fatalf("after load: line_at(%d) id = %d, want %d", i, gotID, wantLine.id)
		}
		gotLen := int(mac.Read(symOutText, 1)[0])
		if gotLen != len(wantLine.text) {
			t.Fatalf("after load: line_at(%d) len = %d, want %d", i, gotLen, len(wantLine.text))
		}
		if gotLen > 0 {
			if g := mac.Read(symOutText+1, gotLen); !bytes.Equal(g, wantLine.text) {
				t.Fatalf("after load: line_at(%d) text mismatch", i)
			}
		}
	}
	// goto-by-id must resolve every loaded line.
	for i, wantLine := range oracle {
		var idBuf [3]byte
		idBuf[0] = byte(wantLine.id)
		idBuf[1] = byte(wantLine.id >> 8)
		idBuf[2] = byte(wantLine.id >> 16)
		mac.Write(symInID, idBuf[:])
		g, err := mac.Call("em_goto")
		if err != nil {
			t.Fatalf("em_goto(id=%d): %v", wantLine.id, err)
		}
		if g.A != 1 || int(g.HL) != i {
			t.Fatalf("after load: em_goto(id=%d) found=%d index=%d, want 1/%d", wantLine.id, g.A, g.HL, i)
		}
	}

	// --- reserialize: must be byte-identical (partition-independent format) ---
	ser2Res, err := mac.Call("em_serialize")
	if err != nil {
		t.Fatalf("em_serialize #2: %v", err)
	}
	got2 := mac.Read(symSerBuf, int(ser2Res.HL))
	if !bytes.Equal(got2, want) {
		t.Fatalf("reserialize after load is not byte-stable")
	}

	// --- nextID restored to maxID+1: the next insert gets a fresh, unused id ---
	var maxID uint32
	for _, l := range oracle {
		if l.id > maxID {
			maxID = l.id
		}
	}
	probe := emRandText(rng, 10)
	mac.Write(symInText, probe)
	if _, err := mac.CallEntry("em_insert", z80h.Entry{BC: uint16(len(oracle)), A: 10}); err != nil {
		t.Fatalf("probe em_insert: %v", err)
	}
	probeID := emReadU24LE(mac.Read(symOutID, 3))
	if probeID != maxID+1 {
		t.Fatalf("after load: next allocated id = %d, want maxID+1 = %d", probeID, maxID+1)
	}

	// --- corrupt header fails loud without disturbing the live document ---
	// Re-serialize the current doc, corrupt the magic, and confirm em_load
	// returns A=0 and leaves the document intact (validation precedes reset).
	if _, err := mac.Call("em_serialize"); err != nil {
		t.Fatalf("em_serialize #3: %v", err)
	}
	liveCount, err := mac.Call("em_line_count")
	if err != nil {
		t.Fatalf("em_line_count: %v", err)
	}
	mac.Write(symSerBuf, []byte{'X'}) // clobber the 'E'
	badRes, err := mac.Call("em_load")
	if err != nil {
		t.Fatalf("em_load(bad magic): %v", err)
	}
	if badRes.A != 0 {
		t.Fatalf("em_load(bad magic) returned A=%d, want 0 (fail-loud)", badRes.A)
	}
	afterBad, err := mac.Call("em_line_count")
	if err != nil {
		t.Fatalf("em_line_count after bad load: %v", err)
	}
	if afterBad.HL != liveCount.HL {
		t.Fatalf("em_load(bad magic) disturbed the document: line_count %d -> %d", liveCount.HL, afterBad.HL)
	}

	t.Logf("seed=%d: serialize/load round-trip ok, %d lines, %d serialized bytes", seed, len(oracle), serLen)
}
