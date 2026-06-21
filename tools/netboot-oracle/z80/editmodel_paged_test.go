// editmodel_paged_test.go — host-verification of the edit-model's PAGED backend
// (src/editmodel.asm assembled with -D EM_PAGED=1; item i41d Brick 2).
//
// The paged backend has no flat EM_DATA arena: each block lives in its own i2
// page-pool page, reached by paging that page into section C via OUT (251)/HMPR.
// This test drives the SAME insert / delete+merge op sequence as the flat
// TestEditModelDeleteMergeZ80, but through the paged build under the one sampage
// harness — the memory model where OUT (251) pages section C for real
// (tools/sampage). A passing oracle comparison therefore proves every block
// round-trips correctly through real paging, including the split/merge
// cross-block copies that route through the resident EM_SCRATCH buffer (the
// paged backend cannot map two arbitrary pool pages at once).
//
// Pool setup: the code + resident structures occupy section A (RAM page 1 in the
// harness flat config) and the harness stack lives in section B (page 2), so the
// test reserves pages 0,1,2 and lets the pool hand out pages 3..31 for blocks.
package z80_test

import (
	"fmt"
	"math/rand"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	emPagedBinPath = "../../../build/editmodel-paged.bin"
	emPagedMapPath = "../../../build/editmodel-paged.map"

	// emPagedOrg is the low-memory org of the paged build (src/editmodel.asm
	// orgs &0000 under -D EM_PAGED so code + resident structures sit in
	// LMPR-mapped sections A+B, clear of the OUT (251) section-C/D window).
	emPagedOrg = 0x0000

	// emPagedTotalPages is the physical page count the test sizes the pool to (a
	// 512 KB SAM). emPagedReserved pages hold code/resident (page 1) + the
	// harness stack (page 2) + page 0 (kept reserved so a block's section-D
	// shadow never lands on the code page); blocks draw from pages 3..31.
	emPagedTotalPages = 32
)

var emPagedReservedPages = []uint8{0, 1, 2}

// loadPagedEditModel loads the paged build, sizes + reserves the page pool, and
// resets the document. After this the model is ready for em_insert/em_delete.
func loadPagedEditModel(t *testing.T) *z80h.Machine {
	t.Helper()
	mac, err := z80h.LoadAt(emPagedBinPath, emPagedMapPath, emPagedOrg)
	if err != nil {
		t.Skipf("paged editmodel not built (%s); run `make editmodel-paged-z80`: %v", emPagedBinPath, err)
	}

	// Size the pool, then reserve the resident pages so they are never handed
	// out as block pages.
	if _, err := mac.CallEntry("pp_init", z80h.Entry{A: emPagedTotalPages}); err != nil {
		t.Fatalf("pp_init: %v", err)
	}
	for _, p := range emPagedReservedPages {
		res, err := mac.CallEntry("pp_reserve", z80h.Entry{A: p})
		if err != nil {
			t.Fatalf("pp_reserve(%d): %v", p, err)
		}
		if res.A == 0xFF {
			t.Fatalf("pp_reserve(%d) returned PP_FAIL", p)
		}
	}
	if _, err := mac.Call("em_reset"); err != nil {
		t.Fatalf("em_reset: %v", err)
	}
	return mac
}

// TestEditModelPagedZ80 drives the paged backend through a build-up + interleaved
// insert/delete walk (forcing splits and merge-on-underflow, hence many paged
// cross-block copies) and checks every logical result against a Go oracle, the
// same way the flat Brick-1b test does — but every block access here is a real
// OUT (251) section-C paging operation.
func TestEditModelPagedZ80(t *testing.T) {
	for _, seed := range []int64{42, 137, 999} {
		seed := seed
		t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) {
			testEditModelPagedWithSeed(t, seed)
		})
	}
}

func testEditModelPagedWithSeed(t *testing.T, seed int64) {
	mac := loadPagedEditModel(t)

	symInText := emSym(t, mac, "EM_IN_TEXT")
	symInID := emSym(t, mac, "EM_IN_ID")
	symOutID := emSym(t, mac, "EM_OUT_ID")
	symOutText := emSym(t, mac, "EM_OUT_TEXT")

	freeAtStart := ppFreeCount(t, mac)
	wantFreeAtStart := emPagedTotalPages - len(emPagedReservedPages)
	if freeAtStart != wantFreeAtStart {
		t.Fatalf("pool free at start = %d, want %d (init/reserve wrong)", freeAtStart, wantFreeAtStart)
	}

	rng := rand.New(rand.NewSource(seed))
	oracle := make([]oracleLine, 0, 320)
	deletedIDs := make([]uint32, 0, 200)

	emInsert := func(at int) {
		textLen := 8 + rng.Intn(33)
		text := make([]byte, textLen)
		for i := range text {
			text[i] = byte(0x20 + rng.Intn(0x5f))
		}
		mac.Write(symInText, text)
		_, err := mac.CallEntry("em_insert", z80h.Entry{BC: uint16(at), A: uint8(textLen)})
		if err != nil {
			t.Fatalf("em_insert(at=%d): %v", at, err)
		}
		newID := emReadU24LE(mac.Read(symOutID, 3))
		if newID == 0 {
			t.Fatalf("em_insert returned id=0")
		}
		oracle = append(oracle, oracleLine{})
		copy(oracle[at+1:], oracle[at:])
		oracle[at] = oracleLine{id: newID, text: append([]byte(nil), text...)}
	}

	emDelete := func(at int) {
		if _, err := mac.CallEntry("em_delete", z80h.Entry{BC: uint16(at)}); err != nil {
			t.Fatalf("em_delete(at=%d): %v", at, err)
		}
		deletedIDs = append(deletedIDs, oracle[at].id)
		oracle = append(oracle[:at], oracle[at+1:]...)
	}

	// Build-up: 120 inserts force splits (each split claims a fresh pool page and
	// copies the second half through EM_SCRATCH into it).
	const buildCount = 120
	for step := 0; step < buildCount; step++ {
		emInsert(rng.Intn(len(oracle) + 1))
	}

	// Splits must have fired: more than one block, and the pool must have handed
	// out pages (proving the paged alloc path ran, not a silent flat fallback).
	bcRes, err := mac.Call("em_block_count")
	if err != nil {
		t.Fatalf("em_block_count: %v", err)
	}
	if bcRes.HL < 2 {
		t.Fatalf("em_block_count = %d after %d inserts, want >= 2 (splits must occur)", bcRes.HL, buildCount)
	}
	freeAfterBuild := ppFreeCount(t, mac)
	if freeAfterBuild >= freeAtStart {
		t.Fatalf("pool free = %d after build (was %d): no pages allocated — paged path did not run", freeAfterBuild, freeAtStart)
	}
	if int(bcRes.HL) != freeAtStart-freeAfterBuild {
		t.Errorf("block_count=%d but %d pages consumed: one page per block expected", bcRes.HL, freeAtStart-freeAfterBuild)
	}
	// The paging port (HMPR) must have moved off its flat-config default, i.e.
	// OUT (251) actually executed.
	if got := mac.Pager().HMPR & 0x1F; got < 3 {
		t.Errorf("HMPR section-C page = %d after build, want a block page (>=3): OUT (251) never mapped a block", got)
	}

	// Interleaved phase: ~200 ops force merge-on-underflow (each merge copies the
	// absorbed block through EM_SCRATCH and returns its page to the pool).
	const mixCount = 200
	for step := 0; step < mixCount; step++ {
		if len(oracle) == 0 || rng.Intn(2) == 0 {
			emInsert(rng.Intn(len(oracle) + 1))
		} else {
			emDelete(rng.Intn(len(oracle)))
		}
	}

	// --- line count ---
	lcRes, err := mac.Call("em_line_count")
	if err != nil {
		t.Fatalf("em_line_count: %v", err)
	}
	if int(lcRes.HL) != len(oracle) {
		t.Errorf("em_line_count = %d, want %d", lcRes.HL, len(oracle))
	}

	bcRes, err = mac.Call("em_block_count")
	if err != nil {
		t.Fatalf("em_block_count: %v", err)
	}
	t.Logf("seed=%d: %d lines, %d blocks, %d/%d pool pages free", seed, len(oracle), bcRes.HL, ppFreeCount(t, mac), wantFreeAtStart)

	// --- line-at for every index ---
	for i, want := range oracle {
		if _, err := mac.CallEntry("em_line_at", z80h.Entry{BC: uint16(i)}); err != nil {
			t.Fatalf("em_line_at(%d): %v", i, err)
		}
		gotID := emReadU24LE(mac.Read(symOutID, 3))
		if gotID != want.id {
			t.Errorf("em_line_at(%d): id = %d, want %d", i, gotID, want.id)
		}
		gotLen := int(mac.Read(symOutText, 1)[0])
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

	// --- goto for every live id ---
	for i, want := range oracle {
		idBuf := []byte{byte(want.id), byte(want.id >> 8), byte(want.id >> 16)}
		mac.Write(symInID, idBuf)
		res, err := mac.Call("em_goto")
		if err != nil {
			t.Fatalf("em_goto(id=%d): %v", want.id, err)
		}
		if res.A != 1 {
			t.Errorf("em_goto(id=%d, oracle[%d]): found=%d, want 1", want.id, i, res.A)
			continue
		}
		if int(res.HL) != i {
			t.Errorf("em_goto(id=%d): index=%d, want %d", want.id, res.HL, i)
		}
	}

	// --- goto for a sample of deleted ids returns not-found ---
	sampleSize := len(deletedIDs)
	if sampleSize > 20 {
		sampleSize = 20
	}
	for _, delID := range deletedIDs[len(deletedIDs)-sampleSize:] {
		idBuf := []byte{byte(delID), byte(delID >> 8), byte(delID >> 16)}
		mac.Write(symInID, idBuf)
		res, err := mac.Call("em_goto")
		if err != nil {
			t.Fatalf("em_goto(deleted id=%d): %v", delID, err)
		}
		if res.A != 0 {
			t.Errorf("em_goto(deleted id=%d): found=%d, want 0 (EM_LOC_ABSENT sentinel broken)", delID, res.A)
		}
	}
}
