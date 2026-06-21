// spill.go is the host-side correctness authority for the on-SAM IDE
// page-persistence (spill) feature — item i215a. It layers a lazy-spill policy
// on top of the i2a page allocator (pagepool.go): when the FREE pool is
// exhausted, instead of refusing an allocation outright (the i2 baseline), the
// Manager evicts the COLDEST resident editor-document block to a pluggable
// backend, frees its page, and satisfies the request. A spilled block reloads
// on demand (which may itself spill another cold block). With no backend
// configured the Manager degrades to the i2 baseline — refuse-with-message.
//
// Only DOC blocks are spill victims: IN/OUT/scratch pages are the assembler's
// live working set and are never evicted (design §4.5). The block payload is
// opaque []byte here — on the SAM it is the i40 .tbn serialization of one i41
// document block; modelling it as opaque bytes keeps this authority decoupled
// from the editor's block format while faithfully exercising the spill/reload
// lifecycle.
//
// Design authority: docs/specs/ide-memory-model-design.md §4.5 (lazy spill),
// §5 (pool-exhaustion policy), §7 (q36 decision: spill is downstream of i2,
// pluggable backend, must degrade to refuse on a Trinity-less machine).
package pagepool

import (
	"errors"
	"fmt"
)

// BlockID is the stable identifier of an editor-document block. On the SAM it
// is the i41 block handle; here it just keys the resident/spilled registry and
// the backing store.
type BlockID uint32

// SpillBackend is the pluggable persistence store a spilled DOC block is
// serialized to. On the SAM the concrete backend is a Trinity SD record when
// the machine has Trinity, else a physical floppy/tape (item i215c); the
// assembler must run fully with NO backend (a floppy/tape-only machine that
// declines to spill), in which case the Manager is constructed with a nil
// backend and degrades to refuse-with-message.
//
// Store/Load/Discard operate on the opaque serialized block payload. Load must
// return the bytes a prior Store wrote for the same id; Discard releases the
// backing store for an id that is reloaded (or whose block is freed while
// spilled).
type SpillBackend interface {
	Store(id BlockID, data []byte) error
	Load(id BlockID) ([]byte, error)
	Discard(id BlockID) error
}

// ErrPoolExhausted is returned by Manager allocations when no FREE page is
// available and the shortfall cannot be covered by spilling — either because no
// backend is configured (the i2 baseline on a Trinity-less machine) or because
// no resident DOC block remains to evict. It is the honest ceiling: the caller
// surfaces it as the design §5 "document full — N KB max on this machine"
// refuse-with-message.
var ErrPoolExhausted = errors.New("pagepool: pool exhausted and no DOC block available to spill")

// docBlock tracks one editor-document block's residency. When resident it owns
// a physical page (page >= 0) and holds the live payload; when spilled the
// payload lives in the backend (page == -1, data == nil).
type docBlock struct {
	id       BlockID
	page     int    // physical page when resident; -1 when spilled
	data     []byte // live payload when resident; nil when spilled
	lastUsed uint64 // LRU sequence: higher == hotter; the lowest resident is "coldest"
	spilled  bool
}

// Manager is the spill-aware allocator. It owns a Pool and the registry of DOC
// blocks, mediating every allocation so that an IN/OUT/scratch request can
// trigger the eviction of a cold DOC block (and vice versa). A nil backend
// makes it a strict pass-through to the i2 baseline (no spill; refuse on
// exhaustion).
type Manager struct {
	pool    *Pool
	backend SpillBackend // nil => degrade-to-refuse (i2 baseline)

	blocks map[BlockID]*docBlock
	seq    uint64  // monotonic LRU clock
	nextID BlockID // next auto-allocated DOC block id

	spills  int // count of DOC-block evictions (observability for tests)
	reloads int // count of on-demand reloads
}

// NewManager builds a spill manager over pool. A nil backend yields the i2
// baseline: allocations that the FREE pool cannot satisfy refuse with
// ErrPoolExhausted rather than spilling.
func NewManager(pool *Pool, backend SpillBackend) *Manager {
	return &Manager{
		pool:    pool,
		backend: backend,
		blocks:  make(map[BlockID]*docBlock),
	}
}

// tick advances and returns the LRU clock so each touch orders strictly after
// every prior one (no ties), making coldest-block selection deterministic.
func (m *Manager) tick() uint64 {
	m.seq++
	return m.seq
}

// ensureFreePage guarantees at least one FREE page exists, spilling the coldest
// resident DOC block if necessary. It returns ErrPoolExhausted when the pool is
// full and the shortfall cannot be covered (no backend, or no resident DOC
// block to evict). This is the lazy hinge of design §4.5: it performs disk I/O
// ONLY when FREE is empty, and exactly one eviction per shortfall.
//
// protect, when >= 0 as a BlockID's worth, names a block that must not be
// chosen as the victim (the block currently being reloaded); pass noProtect to
// protect nothing.
func (m *Manager) ensureFreePage(protect BlockID, protecting bool) error {
	if m.pool.FreeCount() > 0 {
		return nil // FREE non-empty: hand out a page, no disk I/O (the common path)
	}
	if m.backend == nil {
		return ErrPoolExhausted // i2 baseline: no spill backend, refuse
	}
	victim := m.coldestResidentExcept(protect, protecting)
	if victim == nil {
		return ErrPoolExhausted // nothing left to evict
	}
	if err := m.backend.Store(victim.id, victim.data); err != nil {
		return fmt.Errorf("pagepool: spill block %d: %w", victim.id, err)
	}
	if err := m.pool.Free(victim.page, OwnerDoc); err != nil {
		return fmt.Errorf("pagepool: free spilled page %d: %w", victim.page, err)
	}
	victim.spilled = true
	victim.page = -1
	victim.data = nil
	m.spills++
	return nil
}

// coldestResidentExcept returns the resident DOC block with the lowest lastUsed
// (the least-recently-touched) — the spill victim — optionally excluding one
// protected block from the candidate set. The strictly-monotonic LRU clock
// guarantees a unique minimum, so the choice (and the whole spill sequence) is
// deterministic for a given op order. The exclusion lets a reload avoid
// evicting the very block it is faulting in, and gives a single choke point for
// any future protected-set extension.
func (m *Manager) coldestResidentExcept(protect BlockID, protecting bool) *docBlock {
	var coldest *docBlock
	for _, b := range m.blocks {
		if b.spilled {
			continue
		}
		if protecting && b.id == protect {
			continue
		}
		if coldest == nil || b.lastUsed < coldest.lastUsed {
			coldest = b
		}
	}
	return coldest
}

const noProtect BlockID = 0

// NewBlock creates a resident DOC block holding data and returns its id. It
// allocates one page from the pool, spilling a cold block first if the pool is
// full. Returns ErrPoolExhausted if the page cannot be obtained even after a
// spill attempt (no backend / nothing to evict). The payload is copied so the
// caller may reuse its buffer.
func (m *Manager) NewBlock(data []byte) (BlockID, error) {
	if err := m.ensureFreePage(noProtect, false); err != nil {
		return 0, err
	}
	page, ok := m.pool.Alloc(OwnerDoc)
	if !ok {
		// ensureFreePage guaranteed a FREE page; a miss here is a logic bug.
		return 0, ErrPoolExhausted
	}
	id := m.nextID
	m.nextID++
	buf := make([]byte, len(data))
	copy(buf, data)
	m.blocks[id] = &docBlock{
		id:       id,
		page:     page,
		data:     buf,
		lastUsed: m.tick(),
		spilled:  false,
	}
	return id, nil
}

// reload faults a spilled block back into RAM: it secures a FREE page (spilling
// another cold block if needed), reads the payload from the backend, marks the
// block resident, and discards the now-stale backend copy. It is the
// "reload ONLY the DOC blocks that were spilled" half of design §4.5.
func (m *Manager) reload(b *docBlock) error {
	if err := m.ensureFreePage(b.id, true); err != nil {
		return err
	}
	page, ok := m.pool.Alloc(OwnerDoc)
	if !ok {
		return ErrPoolExhausted
	}
	data, err := m.backend.Load(b.id)
	if err != nil {
		// Undo the page grab so the table is unchanged on failure.
		_ = m.pool.Free(page, OwnerDoc)
		return fmt.Errorf("pagepool: reload block %d: %w", b.id, err)
	}
	if err := m.backend.Discard(b.id); err != nil {
		_ = m.pool.Free(page, OwnerDoc)
		return fmt.Errorf("pagepool: discard reloaded block %d: %w", b.id, err)
	}
	b.page = page
	b.data = data
	b.spilled = false
	m.reloads++
	return nil
}

// BlockData returns the payload of a DOC block, reloading it from the backend
// first if it was spilled, and marks it as most-recently-used. A reload may
// spill a different cold block to make room. Returns an error if the id is
// unknown or a reload fails.
func (m *Manager) BlockData(id BlockID) ([]byte, error) {
	b, ok := m.blocks[id]
	if !ok {
		return nil, fmt.Errorf("pagepool: unknown block %d", id)
	}
	if b.spilled {
		if err := m.reload(b); err != nil {
			return nil, err
		}
	}
	b.lastUsed = m.tick()
	return b.data, nil
}

// FreeBlock releases a DOC block: its page returns to the pool if resident, or
// its backend copy is discarded if spilled. The id becomes unknown afterwards.
func (m *Manager) FreeBlock(id BlockID) error {
	b, ok := m.blocks[id]
	if !ok {
		return fmt.Errorf("pagepool: unknown block %d", id)
	}
	if b.spilled {
		if m.backend != nil {
			if err := m.backend.Discard(id); err != nil {
				return fmt.Errorf("pagepool: discard freed block %d: %w", id, err)
			}
		}
	} else {
		if err := m.pool.Free(b.page, OwnerDoc); err != nil {
			return err
		}
	}
	delete(m.blocks, id)
	return nil
}

// Alloc hands out a non-DOC page (IN/OUT/scratch/etc.) for the assembler's live
// working set, spilling a cold DOC block first if the pool is full. The owner
// must not be OwnerDoc — DOC pages are created via NewBlock so the manager can
// track them as spill victims. Returns ErrPoolExhausted if no page can be
// secured even after a spill attempt.
func (m *Manager) Alloc(owner Owner) (page int, err error) {
	if owner == OwnerDoc {
		return 0, fmt.Errorf("pagepool: use NewBlock for OwnerDoc allocations")
	}
	if owner < firstOwnerTag {
		return 0, fmt.Errorf("pagepool: Alloc with non-owner tag %d", owner)
	}
	if err := m.ensureFreePage(noProtect, false); err != nil {
		return 0, err
	}
	p, ok := m.pool.Alloc(owner)
	if !ok {
		return 0, ErrPoolExhausted
	}
	return p, nil
}

// Free returns a non-DOC page allocated via Alloc to the pool (the assembler
// releasing IN/OUT/scratch at assemble-end). DOC blocks are released with
// FreeBlock instead.
func (m *Manager) Free(page int, owner Owner) error {
	if owner == OwnerDoc {
		return fmt.Errorf("pagepool: use FreeBlock for OwnerDoc pages")
	}
	return m.pool.Free(page, owner)
}

// Spills reports how many DOC-block evictions have occurred — 0 means the pool
// never ran short and the run touched the backend zero times (the design's
// "a 512 KB machine touches disk zero times" property is observable as Spills
// == 0).
func (m *Manager) Spills() int { return m.spills }

// Reloads reports how many spilled blocks have been faulted back into RAM.
func (m *Manager) Reloads() int { return m.reloads }

// ResidentDocCount reports how many DOC blocks currently hold a physical page.
func (m *Manager) ResidentDocCount() int {
	n := 0
	for _, b := range m.blocks {
		if !b.spilled {
			n++
		}
	}
	return n
}

// SpilledDocCount reports how many DOC blocks are currently spilled to the
// backend.
func (m *Manager) SpilledDocCount() int {
	n := 0
	for _, b := range m.blocks {
		if b.spilled {
			n++
		}
	}
	return n
}
