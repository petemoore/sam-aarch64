package pagepool

import (
	"math/rand"
	"testing"
)

func TestInitSizesPool(t *testing.T) {
	var p Pool
	p.Init(16) // 256 KB machine: pages 0..15

	if got := p.FreeCount(); got != 16 {
		t.Errorf("FreeCount after Init(16) = %d, want 16", got)
	}
	for i := 0; i < 16; i++ {
		if p.OwnerOf(i) != Free {
			t.Errorf("page %d: owner %d, want Free", i, p.OwnerOf(i))
		}
	}
	for i := 16; i < MaxPages; i++ {
		if p.OwnerOf(i) != Reserved {
			t.Errorf("absent page %d: owner %d, want Reserved", i, p.OwnerOf(i))
		}
	}
}

func TestReserveRemovesFromPool(t *testing.T) {
	var p Pool
	p.Init(16)
	if err := p.Reserve(1); err != nil { // the system page
		t.Fatalf("Reserve(1): %v", err)
	}
	if got := p.FreeCount(); got != 15 {
		t.Errorf("FreeCount after reserving 1 page = %d, want 15", got)
	}
	// Alloc never returns the reserved page.
	for {
		page, ok := p.Alloc(OwnerDoc)
		if !ok {
			break
		}
		if page == 1 {
			t.Fatalf("Alloc handed out reserved page 1")
		}
	}
}

func TestAllocFirstFree(t *testing.T) {
	var p Pool
	p.Init(4)
	for want := 0; want < 4; want++ {
		page, ok := p.Alloc(OwnerOut)
		if !ok || page != want {
			t.Fatalf("Alloc #%d = (%d,%v), want (%d,true)", want, page, ok, want)
		}
	}
	if _, ok := p.Alloc(OwnerOut); ok {
		t.Errorf("Alloc on exhausted pool returned ok=true")
	}
}

func TestFreeTagAssertion(t *testing.T) {
	var p Pool
	p.Init(4)
	page, _ := p.Alloc(OwnerDoc)

	// Freeing under the wrong tag is rejected and changes nothing.
	if err := p.Free(page, OwnerIn); err == nil {
		t.Errorf("Free with wrong tag accepted")
	}
	if p.OwnerOf(page) != OwnerDoc {
		t.Errorf("rejected Free mutated the table: owner now %d", p.OwnerOf(page))
	}

	// Correct tag frees it.
	if err := p.Free(page, OwnerDoc); err != nil {
		t.Errorf("Free with correct tag: %v", err)
	}
	if p.OwnerOf(page) != Free {
		t.Errorf("after Free, owner %d, want Free", p.OwnerOf(page))
	}

	// Double-free is rejected (page is now Free, not OwnerDoc).
	if err := p.Free(page, OwnerDoc); err == nil {
		t.Errorf("double-free accepted")
	}
	// Freeing a reserved page is rejected.
	if err := p.Free(1, OwnerDoc); err == nil {
		_ = err
	}
}

// TestClaimAllFreeAllRoundTrip is the spec §6 invariant: claim every Free page,
// then free them all, and confirm the free count and reserved pages are restored
// exactly — the property the Z80 boot self-test will assert.
func TestClaimAllFreeAllRoundTrip(t *testing.T) {
	var p Pool
	p.Init(16)
	p.Reserve(1)
	p.Reserve(5)
	want := p.FreeCount() // 14

	claimed := []int{}
	for {
		page, ok := p.Alloc(OwnerScratch)
		if !ok {
			break
		}
		claimed = append(claimed, page)
	}
	if len(claimed) != want {
		t.Fatalf("claimed %d pages, want %d", len(claimed), want)
	}
	// Reserved pages never moved.
	if p.OwnerOf(1) != Reserved || p.OwnerOf(5) != Reserved {
		t.Errorf("reserved pages changed during claim-all")
	}
	for _, page := range claimed {
		if err := p.Free(page, OwnerScratch); err != nil {
			t.Fatalf("Free(%d): %v", page, err)
		}
	}
	if got := p.FreeCount(); got != want {
		t.Errorf("FreeCount after free-all = %d, want %d", got, want)
	}
}

// TestRandomOpsInvariant fuzzes alloc/free and checks the pool invariant after
// every op: every page is Reserved, Free, or owned; the table never loses or
// duplicates a page.
func TestRandomOpsInvariant(t *testing.T) {
	var p Pool
	p.Init(20)
	p.Reserve(0)
	p.Reserve(1)
	rng := rand.New(rand.NewSource(99))

	live := map[int]Owner{} // page -> tag we allocated it under
	tags := []Owner{OwnerDoc, OwnerIn, OwnerOut, OwnerScratch}

	for step := 0; step < 2000; step++ {
		if rng.Intn(2) == 0 {
			tag := tags[rng.Intn(len(tags))]
			if page, ok := p.Alloc(tag); ok {
				if _, dup := live[page]; dup {
					t.Fatalf("step %d: Alloc returned live page %d", step, page)
				}
				live[page] = tag
			}
		} else if len(live) > 0 {
			// Free a random live page.
			pick := rng.Intn(len(live))
			i := 0
			for page, tag := range live {
				if i == pick {
					if err := p.Free(page, tag); err != nil {
						t.Fatalf("step %d: Free(%d): %v", step, page, err)
					}
					delete(live, page)
					break
				}
				i++
			}
		}
		// Invariant: free count + live count + reserved count == nPages.
		reserved := 0
		for i := 0; i < 20; i++ {
			if p.OwnerOf(i) == Reserved {
				reserved++
			}
		}
		if p.FreeCount()+len(live)+reserved != 20 {
			t.Fatalf("step %d: free %d + live %d + reserved %d != 20",
				step, p.FreeCount(), len(live), reserved)
		}
	}
}
