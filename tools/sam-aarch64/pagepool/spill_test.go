package pagepool

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
)

// memBackend is an in-memory SpillBackend for tests: it models the pluggable
// store (Trinity record / floppy on the SAM) as a map. It records call counts
// and can be told to fail, so tests can exercise the error paths.
type memBackend struct {
	store    map[BlockID][]byte
	stores   int
	loads    int
	discards int
	failNext error // when non-nil, the next Store/Load/Discard returns it
}

func newMemBackend() *memBackend {
	return &memBackend{store: make(map[BlockID][]byte)}
}

func (b *memBackend) maybeFail() error {
	if b.failNext != nil {
		err := b.failNext
		b.failNext = nil
		return err
	}
	return nil
}

func (b *memBackend) Store(id BlockID, data []byte) error {
	if err := b.maybeFail(); err != nil {
		return err
	}
	buf := make([]byte, len(data))
	copy(buf, data)
	b.store[id] = buf
	b.stores++
	return nil
}

func (b *memBackend) Load(id BlockID) ([]byte, error) {
	if err := b.maybeFail(); err != nil {
		return nil, err
	}
	data, ok := b.store[id]
	if !ok {
		return nil, fmt.Errorf("memBackend: no stored block %d", id)
	}
	b.loads++
	buf := make([]byte, len(data))
	copy(buf, data)
	return buf, nil
}

func (b *memBackend) Discard(id BlockID) error {
	if err := b.maybeFail(); err != nil {
		return err
	}
	delete(b.store, id)
	b.discards++
	return nil
}

// newManager builds a Manager over a pool of nPages FREE pages (no reserved
// system pages, to keep the arithmetic of "how many DOC blocks fit" exact).
func newManager(t *testing.T, nPages int, backend SpillBackend) *Manager {
	t.Helper()
	var p Pool
	p.Init(nPages)
	return NewManager(&p, backend)
}

// TestNoSpillWhenPoolLarge proves the design property "a 512 KB machine touches
// disk zero times": when FREE always covers the DOC blocks, the backend is
// never called.
func TestNoSpillWhenPoolLarge(t *testing.T) {
	be := newMemBackend()
	m := newManager(t, 25, be) // ~512 KB worth of free pages

	ids := make([]BlockID, 0, 8)
	for i := 0; i < 8; i++ {
		id, err := m.NewBlock([]byte{byte(i)})
		if err != nil {
			t.Fatalf("NewBlock %d: %v", i, err)
		}
		ids = append(ids, id)
	}

	if m.Spills() != 0 {
		t.Errorf("Spills = %d, want 0 (pool was never short)", m.Spills())
	}
	if be.stores != 0 {
		t.Errorf("backend.stores = %d, want 0 (no disk I/O expected)", be.stores)
	}
	if got := m.ResidentDocCount(); got != 8 {
		t.Errorf("ResidentDocCount = %d, want 8", got)
	}
	// Every block still reads back its content without a reload.
	for i, id := range ids {
		data, err := m.BlockData(id)
		if err != nil {
			t.Fatalf("BlockData(%d): %v", id, err)
		}
		if !bytes.Equal(data, []byte{byte(i)}) {
			t.Errorf("block %d content = %v, want [%d]", id, data, i)
		}
	}
	if m.Reloads() != 0 {
		t.Errorf("Reloads = %d, want 0", m.Reloads())
	}
}

// TestLazySpillExactlyAsNeeded proves the lazy hinge of §4.5: DOC blocks spill
// ONLY when the pool is full and an allocation cannot otherwise be satisfied,
// and exactly one block spills per page of shortfall.
func TestLazySpillExactlyAsNeeded(t *testing.T) {
	be := newMemBackend()
	m := newManager(t, 4, be) // tight: only 4 pages

	// Fill the pool with 4 DOC blocks — no spill yet.
	ids := make([]BlockID, 0, 4)
	for i := 0; i < 4; i++ {
		id, err := m.NewBlock([]byte{byte(0xA0 + i)})
		if err != nil {
			t.Fatalf("NewBlock %d: %v", i, err)
		}
		ids = append(ids, id)
	}
	if m.Spills() != 0 {
		t.Fatalf("Spills = %d after filling exactly, want 0", m.Spills())
	}

	// The 5th DOC block forces exactly one spill (the coldest = ids[0], which
	// has not been touched since creation).
	id5, err := m.NewBlock([]byte{0xB0})
	if err != nil {
		t.Fatalf("NewBlock 5th: %v", err)
	}
	if m.Spills() != 1 {
		t.Errorf("Spills = %d after 5th block, want exactly 1", m.Spills())
	}
	if be.stores != 1 {
		t.Errorf("backend.stores = %d, want 1", be.stores)
	}
	if m.ResidentDocCount() != 4 || m.SpilledDocCount() != 1 {
		t.Errorf("resident=%d spilled=%d, want 4/1", m.ResidentDocCount(), m.SpilledDocCount())
	}

	// The coldest victim was ids[0]; reading it back faults it in (one reload),
	// spilling the now-coldest resident block to make room.
	data, err := m.BlockData(ids[0])
	if err != nil {
		t.Fatalf("BlockData(ids[0]): %v", err)
	}
	if !bytes.Equal(data, []byte{0xA0}) {
		t.Errorf("reloaded block content = %v, want [0xA0]", data)
	}
	if m.Reloads() != 1 {
		t.Errorf("Reloads = %d, want 1", m.Reloads())
	}
	if m.Spills() != 2 {
		t.Errorf("Spills = %d after reload-induced eviction, want 2", m.Spills())
	}
	// Pool invariant: never over-committed.
	if m.ResidentDocCount() != 4 {
		t.Errorf("ResidentDocCount = %d after reload, want 4", m.ResidentDocCount())
	}
	_ = id5
}

// TestReloadRoundTripsContent spills several blocks and verifies every one
// reads back byte-identical, regardless of resident/spilled order.
func TestReloadRoundTripsContent(t *testing.T) {
	be := newMemBackend()
	m := newManager(t, 3, be)

	const n = 9
	want := make(map[BlockID][]byte, n)
	ids := make([]BlockID, 0, n)
	for i := 0; i < n; i++ {
		payload := bytes.Repeat([]byte{byte(i)}, i+1) // distinct length + content
		id, err := m.NewBlock(payload)
		if err != nil {
			t.Fatalf("NewBlock %d: %v", i, err)
		}
		want[id] = payload
		ids = append(ids, id)
	}
	// With only 3 pages and 9 blocks, at least 6 must be spilled at some point.
	if m.Spills() == 0 {
		t.Fatalf("expected spills with 9 blocks in a 3-page pool, got 0")
	}

	// Read every block in a scrambled order; each must round-trip.
	order := []int{4, 0, 8, 2, 6, 1, 7, 3, 5}
	for _, i := range order {
		id := ids[i]
		data, err := m.BlockData(id)
		if err != nil {
			t.Fatalf("BlockData(block %d): %v", id, err)
		}
		if !bytes.Equal(data, want[id]) {
			t.Errorf("block %d content = %v, want %v", id, data, want[id])
		}
	}
	// Residency never exceeds the pool size.
	if m.ResidentDocCount() > 3 {
		t.Errorf("ResidentDocCount = %d, exceeds 3-page pool", m.ResidentDocCount())
	}
}

// TestDegradeToRefuseWithoutBackend is the i2 baseline: a nil backend must
// refuse-with-ErrPoolExhausted when the pool fills, never spill.
func TestDegradeToRefuseWithoutBackend(t *testing.T) {
	m := newManager(t, 3, nil) // Trinity-less machine: no spill backend

	for i := 0; i < 3; i++ {
		if _, err := m.NewBlock([]byte{byte(i)}); err != nil {
			t.Fatalf("NewBlock %d: %v", i, err)
		}
	}
	// The 4th must refuse — the honest ceiling, not a spill.
	_, err := m.NewBlock([]byte{0xFF})
	if !errors.Is(err, ErrPoolExhausted) {
		t.Fatalf("NewBlock past capacity: err = %v, want ErrPoolExhausted", err)
	}
	if m.Spills() != 0 {
		t.Errorf("Spills = %d with nil backend, want 0", m.Spills())
	}
	// A non-DOC allocation likewise refuses rather than spilling a DOC block.
	if _, err := m.Alloc(OwnerIn); !errors.Is(err, ErrPoolExhausted) {
		t.Errorf("Alloc(OwnerIn) when full: err = %v, want ErrPoolExhausted", err)
	}
}

// TestNonDocAllocSpillsDocBlock proves the cross-subsystem case of §4.5: an
// IN/OUT/scratch allocation, finding the pool full, evicts a cold DOC block to
// make room — the assembler's live budget coexists with the document, spilling
// only under pressure.
func TestNonDocAllocSpillsDocBlock(t *testing.T) {
	be := newMemBackend()
	m := newManager(t, 3, be)

	doc0, _ := m.NewBlock([]byte{0x10})
	doc1, _ := m.NewBlock([]byte{0x11})
	doc2, _ := m.NewBlock([]byte{0x12})
	if m.Spills() != 0 {
		t.Fatalf("unexpected early spill")
	}

	// Assembler grabs an IN page: pool is full → spill the coldest DOC (doc0).
	inPage, err := m.Alloc(OwnerIn)
	if err != nil {
		t.Fatalf("Alloc(OwnerIn): %v", err)
	}
	if m.Spills() != 1 {
		t.Errorf("Spills = %d after IN alloc, want 1", m.Spills())
	}
	if OwnerOf := m.pool.OwnerOf(inPage); OwnerOf != OwnerIn {
		t.Errorf("IN page owner = %d, want OwnerIn", OwnerOf)
	}

	// doc0 is the spilled one; doc1/doc2 still resident.
	if m.SpilledDocCount() != 1 {
		t.Errorf("SpilledDocCount = %d, want 1", m.SpilledDocCount())
	}

	// Assemble-end: free IN; reloading doc0 now succeeds without further spill
	// because the freed IN page is available.
	if err := m.Free(inPage, OwnerIn); err != nil {
		t.Fatalf("Free(inPage): %v", err)
	}
	data, err := m.BlockData(doc0)
	if err != nil {
		t.Fatalf("BlockData(doc0): %v", err)
	}
	if !bytes.Equal(data, []byte{0x10}) {
		t.Errorf("doc0 content = %v, want [0x10]", data)
	}
	if m.Spills() != 1 {
		t.Errorf("Spills = %d after reload into freed page, want still 1", m.Spills())
	}
	_ = doc1
	_ = doc2
}

// TestColdestVictimIsLRU proves the eviction victim is the least-recently-used
// resident block, not merely the lowest id: touching a block protects it from
// the next spill.
func TestColdestVictimIsLRU(t *testing.T) {
	be := newMemBackend()
	m := newManager(t, 3, be)

	a, _ := m.NewBlock([]byte{0xA})
	b, _ := m.NewBlock([]byte{0xB})
	c, _ := m.NewBlock([]byte{0xC})

	// Touch a (oldest by creation) so it becomes hottest; b is now coldest.
	if _, err := m.BlockData(a); err != nil {
		t.Fatalf("touch a: %v", err)
	}

	// A new block forces one spill; the victim must be b (coldest), not a.
	if _, err := m.NewBlock([]byte{0xD}); err != nil {
		t.Fatalf("NewBlock d: %v", err)
	}
	if m.Spills() != 1 {
		t.Fatalf("Spills = %d, want 1", m.Spills())
	}
	// a must still be resident (reading it must NOT cause a reload).
	beforeReloads := m.Reloads()
	if _, err := m.BlockData(a); err != nil {
		t.Fatalf("BlockData(a): %v", err)
	}
	if m.Reloads() != beforeReloads {
		t.Errorf("a was evicted (reload count rose) — LRU victim selection wrong")
	}
	// b must be the spilled one (reading it DOES reload).
	if _, err := m.BlockData(b); err != nil {
		t.Fatalf("BlockData(b): %v", err)
	}
	if m.Reloads() != beforeReloads+1 {
		t.Errorf("b was not the spill victim — expected a reload on access")
	}
	_ = c
}

// TestFreeBlockReleasesResidentAndSpilled checks both lifecycle ends: freeing a
// resident block returns its page; freeing a spilled block discards its backend
// copy. Both reclaim the slot.
func TestFreeBlockReleasesResidentAndSpilled(t *testing.T) {
	be := newMemBackend()
	m := newManager(t, 2, be)

	r, _ := m.NewBlock([]byte{1})
	s, _ := m.NewBlock([]byte{2})
	// Force s-or-r to spill by adding a third block.
	x, _ := m.NewBlock([]byte{3}) // spills the coldest (r)
	if m.SpilledDocCount() != 1 {
		t.Fatalf("want 1 spilled after 3rd block, got %d", m.SpilledDocCount())
	}

	freeBefore := m.pool.FreeCount()
	// Free the resident block x: a page returns to the pool.
	if err := m.FreeBlock(x); err != nil {
		t.Fatalf("FreeBlock(x resident): %v", err)
	}
	if m.pool.FreeCount() != freeBefore+1 {
		t.Errorf("FreeCount = %d after freeing resident, want %d", m.pool.FreeCount(), freeBefore+1)
	}

	// Free the spilled block r: backend copy discarded, no page change.
	discBefore := be.discards
	if err := m.FreeBlock(r); err != nil {
		t.Fatalf("FreeBlock(r spilled): %v", err)
	}
	if be.discards != discBefore+1 {
		t.Errorf("backend.discards = %d, want %d", be.discards, discBefore+1)
	}
	if _, err := m.BlockData(r); err == nil {
		t.Errorf("BlockData(freed block) succeeded, want error")
	}
	_ = s
}

// TestSpillStoreFailurePropagates checks that a backend Store error surfaces and
// leaves the table consistent (the victim stays resident, owning its page).
func TestSpillStoreFailurePropagates(t *testing.T) {
	be := newMemBackend()
	m := newManager(t, 2, be)

	v0, _ := m.NewBlock([]byte{1})
	_, _ = m.NewBlock([]byte{2})

	be.failNext = errors.New("disk write error")
	_, err := m.NewBlock([]byte{3}) // would need to spill the coldest (v0)
	if err == nil {
		t.Fatalf("NewBlock with failing backend: want error, got nil")
	}
	// v0 must remain resident and readable — the failed spill must not have
	// orphaned its page or lost its content.
	if m.SpilledDocCount() != 0 {
		t.Errorf("SpilledDocCount = %d after failed spill, want 0", m.SpilledDocCount())
	}
	data, err := m.BlockData(v0)
	if err != nil {
		t.Fatalf("BlockData(v0) after failed spill: %v", err)
	}
	if !bytes.Equal(data, []byte{1}) {
		t.Errorf("v0 content = %v, want [1]", data)
	}
}
