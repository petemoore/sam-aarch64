// spill_test.go — host-verification of src/spill.asm (item i215b).
//
// Drives the Z80 page-persistence (spill) manager under the flat-memory
// koron-go/z80 harness and asserts it reproduces the observable behaviour of the
// i215a Go authority (tools/sam-aarch64/pagepool/spill.go) — the same
// Go-authority-+-Z80-port pattern as pagepool_test.go / editmodel_test.go.
//
// Each test mirrors one scenario in spill_test.go (the Go authority's own test):
// the expected spill/reload counts, resident/spilled counts and payload bytes
// are exactly the values that test pins, so matching them here is a direct
// cross-check of the port against the authority.
package z80_test

import (
	"bytes"
	"testing"

	pagepool "github.com/petemoore/sam-aarch64/tools/sam-aarch64/pagepool"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	spBinPath = "../../../build/spill.bin"
	spMapPath = "../../../build/spill.map"
	// spScratch is a fixed flat-RAM staging address (clear of the binary's code
	// + data) where the harness writes a payload before sp_new_block copies it in.
	spScratch = 0xF000
	spFail    = 0xFF
)

func loadSpill(t *testing.T) *z80h.Machine {
	t.Helper()
	mac, err := z80h.Load(spBinPath, spMapPath)
	if err != nil {
		t.Fatalf("spill binary not built (%s); run `make spill-z80`: %v", spBinPath, err)
	}
	return mac
}

func spU16(mac *z80h.Machine, name string) int {
	addr, err := mac.Sym(name)
	if err != nil {
		panic(err)
	}
	raw := mac.Read(addr, 2)
	return int(raw[0]) | int(raw[1])<<8
}

func spInit(t *testing.T, mac *z80h.Machine, nPages int, backend bool) {
	t.Helper()
	if _, err := mac.CallEntry("sp_init", z80h.Entry{A: uint8(nPages)}); err != nil {
		t.Fatalf("sp_init(%d): %v", nPages, err)
	}
	b := uint8(0)
	if backend {
		b = 1
	}
	if _, err := mac.CallEntry("sp_set_backend", z80h.Entry{A: b}); err != nil {
		t.Fatalf("sp_set_backend(%v): %v", backend, err)
	}
}

// spNewBlock stages payload at spScratch and drives sp_new_block(HL=ptr, B=len);
// returns (id, ok).
func spNewBlock(t *testing.T, mac *z80h.Machine, payload []byte) (int, bool) {
	t.Helper()
	mac.Write(spScratch, payload)
	res, err := mac.CallEntry("sp_new_block", z80h.Entry{HL: spScratch, BC: uint16(len(payload)) << 8})
	if err != nil {
		t.Fatalf("sp_new_block(len=%d): %v", len(payload), err)
	}
	if res.A == spFail {
		return 0, false
	}
	return int(res.HL), true
}

// spBlockData drives sp_block_data(HL=id); returns (payload, ok). On success the
// routine returns DE=&payload and B=length, which we read out of Z80 memory.
func spBlockData(t *testing.T, mac *z80h.Machine, id int) ([]byte, bool) {
	t.Helper()
	res, err := mac.CallEntry("sp_block_data", z80h.Entry{HL: uint16(id)})
	if err != nil {
		t.Fatalf("sp_block_data(%d): %v", id, err)
	}
	if res.A == spFail {
		return nil, false
	}
	length := int(res.BC >> 8)
	return mac.Read(res.DE, length), true
}

func spFreeBlock(t *testing.T, mac *z80h.Machine, id int) bool {
	t.Helper()
	res, err := mac.CallEntry("sp_free_block", z80h.Entry{HL: uint16(id)})
	if err != nil {
		t.Fatalf("sp_free_block(%d): %v", id, err)
	}
	return res.A != spFail
}

func spAlloc(t *testing.T, mac *z80h.Machine, owner pagepool.Owner) (int, bool) {
	t.Helper()
	res, err := mac.CallEntry("sp_alloc", z80h.Entry{A: uint8(owner)})
	if err != nil {
		t.Fatalf("sp_alloc(%d): %v", owner, err)
	}
	if res.A == spFail {
		return 0, false
	}
	return int(res.A), true
}

func spFree(t *testing.T, mac *z80h.Machine, page int, owner pagepool.Owner) bool {
	t.Helper()
	res, err := mac.CallEntry("sp_free", z80h.Entry{A: uint8(page), BC: uint16(owner) << 8})
	if err != nil {
		t.Fatalf("sp_free(%d,%d): %v", page, owner, err)
	}
	return res.A != spFail
}

func spArmFail(t *testing.T, mac *z80h.Machine) {
	t.Helper()
	if _, err := mac.CallEntry("sp_arm_fail", z80h.Entry{}); err != nil {
		t.Fatalf("sp_arm_fail: %v", err)
	}
}

func spResident(t *testing.T, mac *z80h.Machine) int {
	t.Helper()
	res, err := mac.CallEntry("sp_resident_doc_count", z80h.Entry{})
	if err != nil {
		t.Fatalf("sp_resident_doc_count: %v", err)
	}
	return int(res.A)
}

func spSpilledCount(t *testing.T, mac *z80h.Machine) int {
	t.Helper()
	res, err := mac.CallEntry("sp_spilled_doc_count", z80h.Entry{})
	if err != nil {
		t.Fatalf("sp_spilled_doc_count: %v", err)
	}
	return int(res.A)
}

func spOwnerOf(t *testing.T, mac *z80h.Machine, page int) pagepool.Owner {
	t.Helper()
	res, err := mac.CallEntry("pp_owner_of", z80h.Entry{A: uint8(page)})
	if err != nil {
		t.Fatalf("pp_owner_of(%d): %v", page, err)
	}
	return pagepool.Owner(res.A)
}

func spSpills(mac *z80h.Machine) int  { return spU16(mac, "SP_SPILLS") }
func spReloads(mac *z80h.Machine) int { return spU16(mac, "SP_RELOADS") }
func spStores(mac *z80h.Machine) int  { return spU16(mac, "SP_BE_STORES") }

// TestSpillNoSpillWhenPoolLargeZ80 mirrors TestNoSpillWhenPoolLarge: a roomy pool
// touches the backend zero times and every block reads back without a reload.
func TestSpillNoSpillWhenPoolLargeZ80(t *testing.T) {
	mac := loadSpill(t)
	spInit(t, mac, 25, true)

	ids := make([]int, 0, 8)
	for i := 0; i < 8; i++ {
		id, ok := spNewBlock(t, mac, []byte{byte(i)})
		if !ok {
			t.Fatalf("NewBlock %d refused in a 25-page pool", i)
		}
		ids = append(ids, id)
	}
	if got := spSpills(mac); got != 0 {
		t.Errorf("Spills = %d, want 0 (pool never short)", got)
	}
	if got := spStores(mac); got != 0 {
		t.Errorf("backend stores = %d, want 0", got)
	}
	if got := spResident(t, mac); got != 8 {
		t.Errorf("ResidentDocCount = %d, want 8", got)
	}
	for i, id := range ids {
		data, ok := spBlockData(t, mac, id)
		if !ok {
			t.Fatalf("BlockData(%d) refused", id)
		}
		if !bytes.Equal(data, []byte{byte(i)}) {
			t.Errorf("block %d content = %v, want [%d]", id, data, i)
		}
	}
	if got := spReloads(mac); got != 0 {
		t.Errorf("Reloads = %d, want 0", got)
	}
}

// TestSpillLazyExactlyAsNeededZ80 mirrors TestLazySpillExactlyAsNeeded: exactly
// one spill per page of shortfall, and a reload re-evicts the now-coldest block.
func TestSpillLazyExactlyAsNeededZ80(t *testing.T) {
	mac := loadSpill(t)
	spInit(t, mac, 4, true)

	ids := make([]int, 0, 4)
	for i := 0; i < 4; i++ {
		id, ok := spNewBlock(t, mac, []byte{byte(0xA0 + i)})
		if !ok {
			t.Fatalf("NewBlock %d refused", i)
		}
		ids = append(ids, id)
	}
	if got := spSpills(mac); got != 0 {
		t.Fatalf("Spills = %d after filling exactly, want 0", got)
	}

	if _, ok := spNewBlock(t, mac, []byte{0xB0}); !ok {
		t.Fatalf("5th NewBlock refused")
	}
	if got := spSpills(mac); got != 1 {
		t.Errorf("Spills = %d after 5th block, want 1", got)
	}
	if got := spStores(mac); got != 1 {
		t.Errorf("backend stores = %d, want 1", got)
	}
	if r, s := spResident(t, mac), spSpilledCount(t, mac); r != 4 || s != 1 {
		t.Errorf("resident=%d spilled=%d, want 4/1", r, s)
	}

	// ids[0] was the coldest victim; reading it faults it in (one reload) and
	// spills the now-coldest resident block.
	data, ok := spBlockData(t, mac, ids[0])
	if !ok {
		t.Fatalf("BlockData(ids[0]) refused")
	}
	if !bytes.Equal(data, []byte{0xA0}) {
		t.Errorf("reloaded content = %v, want [0xA0]", data)
	}
	if got := spReloads(mac); got != 1 {
		t.Errorf("Reloads = %d, want 1", got)
	}
	if got := spSpills(mac); got != 2 {
		t.Errorf("Spills = %d after reload-induced eviction, want 2", got)
	}
	if got := spResident(t, mac); got != 4 {
		t.Errorf("ResidentDocCount = %d after reload, want 4", got)
	}
}

// TestSpillReloadRoundTripsContentZ80 mirrors TestReloadRoundTripsContent: 9
// blocks in a 3-page pool all read back byte-identical in a scrambled order.
func TestSpillReloadRoundTripsContentZ80(t *testing.T) {
	mac := loadSpill(t)
	spInit(t, mac, 3, true)

	const n = 9
	want := make(map[int][]byte, n)
	ids := make([]int, 0, n)
	for i := 0; i < n; i++ {
		payload := bytes.Repeat([]byte{byte(i)}, i+1) // distinct length + content
		id, ok := spNewBlock(t, mac, payload)
		if !ok {
			t.Fatalf("NewBlock %d refused", i)
		}
		want[id] = payload
		ids = append(ids, id)
	}
	if spSpills(mac) == 0 {
		t.Fatalf("expected spills with 9 blocks in a 3-page pool, got 0")
	}

	for _, i := range []int{4, 0, 8, 2, 6, 1, 7, 3, 5} {
		id := ids[i]
		data, ok := spBlockData(t, mac, id)
		if !ok {
			t.Fatalf("BlockData(block %d) refused", id)
		}
		if !bytes.Equal(data, want[id]) {
			t.Errorf("block %d content = %v, want %v", id, data, want[id])
		}
	}
	if got := spResident(t, mac); got > 3 {
		t.Errorf("ResidentDocCount = %d, exceeds 3-page pool", got)
	}
}

// TestSpillDegradeToRefuseWithoutBackendZ80 mirrors TestDegradeToRefuseWithout
// Backend: the i2 baseline refuses when full rather than spilling.
func TestSpillDegradeToRefuseWithoutBackendZ80(t *testing.T) {
	mac := loadSpill(t)
	spInit(t, mac, 3, false) // no backend

	for i := 0; i < 3; i++ {
		if _, ok := spNewBlock(t, mac, []byte{byte(i)}); !ok {
			t.Fatalf("NewBlock %d refused below capacity", i)
		}
	}
	if _, ok := spNewBlock(t, mac, []byte{0xFF}); ok {
		t.Fatalf("NewBlock past capacity succeeded; want refuse")
	}
	if got := spSpills(mac); got != 0 {
		t.Errorf("Spills = %d with nil backend, want 0", got)
	}
	if _, ok := spAlloc(t, mac, pagepool.OwnerIn); ok {
		t.Errorf("Alloc(OwnerIn) when full succeeded; want refuse")
	}
}

// TestSpillNonDocAllocSpillsDocBlockZ80 mirrors TestNonDocAllocSpillsDocBlock: a
// non-DOC alloc evicts a cold DOC block, and freeing it lets the reload reuse the
// page with no further spill.
func TestSpillNonDocAllocSpillsDocBlockZ80(t *testing.T) {
	mac := loadSpill(t)
	spInit(t, mac, 3, true)

	doc0, _ := spNewBlock(t, mac, []byte{0x10})
	spNewBlock(t, mac, []byte{0x11})
	spNewBlock(t, mac, []byte{0x12})
	if spSpills(mac) != 0 {
		t.Fatalf("unexpected early spill")
	}

	inPage, ok := spAlloc(t, mac, pagepool.OwnerIn)
	if !ok {
		t.Fatalf("Alloc(OwnerIn) refused")
	}
	if got := spSpills(mac); got != 1 {
		t.Errorf("Spills = %d after IN alloc, want 1", got)
	}
	if got := spOwnerOf(t, mac, inPage); got != pagepool.OwnerIn {
		t.Errorf("IN page owner = %d, want OwnerIn", got)
	}
	if got := spSpilledCount(t, mac); got != 1 {
		t.Errorf("SpilledDocCount = %d, want 1", got)
	}

	if !spFree(t, mac, inPage, pagepool.OwnerIn) {
		t.Fatalf("Free(inPage) rejected")
	}
	data, ok := spBlockData(t, mac, doc0)
	if !ok {
		t.Fatalf("BlockData(doc0) refused")
	}
	if !bytes.Equal(data, []byte{0x10}) {
		t.Errorf("doc0 content = %v, want [0x10]", data)
	}
	if got := spSpills(mac); got != 1 {
		t.Errorf("Spills = %d after reload into freed page, want still 1", got)
	}
}

// TestSpillColdestVictimIsLRUZ80 mirrors TestColdestVictimIsLRU: touching a block
// protects it; the victim is the least-recently-used, not the lowest id.
func TestSpillColdestVictimIsLRUZ80(t *testing.T) {
	mac := loadSpill(t)
	spInit(t, mac, 3, true)

	a, _ := spNewBlock(t, mac, []byte{0xA})
	b, _ := spNewBlock(t, mac, []byte{0xB})
	spNewBlock(t, mac, []byte{0xC})

	// Touch a (oldest by creation) so it becomes hottest; b is now coldest.
	if _, ok := spBlockData(t, mac, a); !ok {
		t.Fatalf("touch a refused")
	}
	if _, ok := spNewBlock(t, mac, []byte{0xD}); !ok {
		t.Fatalf("NewBlock d refused")
	}
	if got := spSpills(mac); got != 1 {
		t.Fatalf("Spills = %d, want 1", got)
	}

	// a must still be resident (reading it must NOT reload).
	before := spReloads(mac)
	if _, ok := spBlockData(t, mac, a); !ok {
		t.Fatalf("BlockData(a) refused")
	}
	if spReloads(mac) != before {
		t.Errorf("a was evicted (reload count rose) — LRU victim selection wrong")
	}
	// b must be the spilled one (reading it DOES reload).
	if _, ok := spBlockData(t, mac, b); !ok {
		t.Fatalf("BlockData(b) refused")
	}
	if spReloads(mac) != before+1 {
		t.Errorf("b was not the spill victim — expected a reload on access")
	}
}

// TestSpillFreeBlockReleasesResidentAndSpilledZ80 mirrors TestFreeBlockReleases
// ResidentAndSpilled: freeing a resident block returns its page; freeing a
// spilled block discards its backend copy. Both reclaim the slot.
func TestSpillFreeBlockReleasesResidentAndSpilledZ80(t *testing.T) {
	mac := loadSpill(t)
	spInit(t, mac, 2, true)

	r, _ := spNewBlock(t, mac, []byte{1})
	spNewBlock(t, mac, []byte{2})
	x, _ := spNewBlock(t, mac, []byte{3}) // spills the coldest (r)
	if got := spSpilledCount(t, mac); got != 1 {
		t.Fatalf("want 1 spilled after 3rd block, got %d", got)
	}

	freeBefore := freeCount(t, mac)
	if !spFreeBlock(t, mac, x) { // x is resident
		t.Fatalf("FreeBlock(x resident) rejected")
	}
	if got := freeCount(t, mac); got != freeBefore+1 {
		t.Errorf("FreeCount = %d after freeing resident, want %d", got, freeBefore+1)
	}

	discBefore := spU16(mac, "SP_BE_DISCARDS")
	if !spFreeBlock(t, mac, r) { // r is spilled
		t.Fatalf("FreeBlock(r spilled) rejected")
	}
	if got := spU16(mac, "SP_BE_DISCARDS"); got != discBefore+1 {
		t.Errorf("backend discards = %d, want %d", got, discBefore+1)
	}
	if _, ok := spBlockData(t, mac, r); ok {
		t.Errorf("BlockData(freed block) succeeded, want refuse")
	}
}

// freeCount reads the live pool FREE count via pp_free_count.
func freeCount(t *testing.T, mac *z80h.Machine) int {
	t.Helper()
	res, err := mac.CallEntry("pp_free_count", z80h.Entry{})
	if err != nil {
		t.Fatalf("pp_free_count: %v", err)
	}
	return int(res.A)
}

// TestSpillStoreFailurePropagatesZ80 mirrors TestSpillStoreFailurePropagates: a
// failed Store leaves the victim resident, owning its page and content.
func TestSpillStoreFailurePropagatesZ80(t *testing.T) {
	mac := loadSpill(t)
	spInit(t, mac, 2, true)

	v0, _ := spNewBlock(t, mac, []byte{1})
	spNewBlock(t, mac, []byte{2})

	spArmFail(t, mac)
	if _, ok := spNewBlock(t, mac, []byte{3}); ok { // would spill the coldest (v0)
		t.Fatalf("NewBlock with failing backend succeeded; want refuse")
	}
	if got := spSpilledCount(t, mac); got != 0 {
		t.Errorf("SpilledDocCount = %d after failed spill, want 0", got)
	}
	data, ok := spBlockData(t, mac, v0)
	if !ok {
		t.Fatalf("BlockData(v0) after failed spill refused")
	}
	if !bytes.Equal(data, []byte{1}) {
		t.Errorf("v0 content = %v, want [1]", data)
	}
}
