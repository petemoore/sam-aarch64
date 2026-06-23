// Package pagepool is the host-side correctness authority for the on-SAM IDE
// page allocator (item i2a). It models the page_owner[] ownership table and the
// alloc_page/free_page discipline of docs/specs/ide-memory-model-design.md §4.1.
//
// This is a reference prototype of the allocation *policy* (first-FREE handout,
// tag-checked frees, reserved pages never handed out); the exact SAM byte
// layout and Z80 register interface are the Z80 port's concern (src/pagepool.asm).
// The koron-go/z80 harness drives the Z80 routines against this same policy
// (tools/netboot-oracle/z80/pagepool_test.go).
//
// Design authority: docs/specs/ide-memory-model-design.md §4.1.
package pagepool

import "fmt"

// Owner is the per-page ownership tag stored in one page_owner[] byte. The
// numeric values match the PP_* equates in src/pagepool.asm so the Go authority
// and the Z80 port agree byte-for-byte on the table contents.
type Owner uint8

const (
	// Reserved is a page the IDE never hands out: absent (beyond the machine's
	// physical page count), or held by ROM/BASIC/DOS/screen/resident-code. It is
	// also the init value for pages above the physical page count.
	Reserved Owner = 0
	// Free is an allocatable page currently held by no subsystem.
	Free Owner = 1
	// Owner tags (>= firstOwnerTag): a page handed out to a subsystem. free_page
	// only accepts a page whose current tag is one of these.
	OwnerDoc     Owner = 2
	OwnerIn      Owner = 3
	OwnerOut     Owner = 4
	OwnerScratch Owner = 5
	OwnerEnctab  Owner = 6
	OwnerDisasm  Owner = 7
	OwnerPayload Owner = 8

	// firstOwnerTag is the lowest value that denotes a handed-out page. Anything
	// >= this is "owned"; Reserved/Free are below it.
	firstOwnerTag Owner = 2

	// MaxPages is the page_owner[] table size — 32 covers a 512 KB SAM (pages
	// 0..31). It matches PP_MAX_PAGES in src/pagepool.asm.
	MaxPages = 32
)

// Pool is the page allocator state: one ownership byte per physical page plus
// the count of pages actually present on the machine.
type Pool struct {
	owner  [MaxPages]Owner
	nPages int
}

// Init sizes the pool for a machine with nPages physical pages: pages [0,nPages)
// become Free, pages [nPages,MaxPages) become Reserved (absent). This mirrors
// pp_init — boot-time pool sizing (reading PRAMTP, NEWing BASIC, reserving the
// system pages) is a later brick that layers pp_reserve on top of this base.
func (p *Pool) Init(nPages int) {
	if nPages < 0 || nPages > MaxPages {
		panic(fmt.Sprintf("pagepool: nPages %d out of range [0,%d]", nPages, MaxPages))
	}
	p.nPages = nPages
	for i := range p.owner {
		if i < nPages {
			p.owner[i] = Free
		} else {
			p.owner[i] = Reserved
		}
	}
}

// Reserve marks a page Reserved so it is never handed out (the system pages, the
// resident-code page, the screen pages). Mirrors pp_reserve. Returns an error if
// the page is out of range.
func (p *Pool) Reserve(page int) error {
	if page < 0 || page >= MaxPages {
		return fmt.Errorf("pagepool: reserve page %d out of range", page)
	}
	p.owner[page] = Reserved
	return nil
}

// Alloc hands out the lowest-numbered Free page, tags it owner, and returns its
// page number. The first-FREE policy is deterministic so the Z80 port and this
// oracle return identical page numbers for an identical op sequence. ok is false
// when no Free page remains (pool exhaustion). Mirrors pp_alloc.
func (p *Pool) Alloc(owner Owner) (page int, ok bool) {
	if owner < firstOwnerTag {
		panic(fmt.Sprintf("pagepool: Alloc with non-owner tag %d", owner))
	}
	for i := 0; i < p.nPages; i++ {
		if p.owner[i] == Free {
			p.owner[i] = owner
			return i, true
		}
	}
	return 0, false
}

// Free returns a page to the Free pool. It asserts the page currently carries
// the expected owner tag — a tag mismatch (double-free, freeing a Reserved page,
// or freeing under the wrong owner) is a page-ownership bug and returns an error
// without changing the table. Mirrors pp_free.
func (p *Pool) Free(page int, expected Owner) error {
	if page < 0 || page >= MaxPages {
		return fmt.Errorf("pagepool: free page %d out of range", page)
	}
	if p.owner[page] != expected {
		return fmt.Errorf("pagepool: free page %d: owner is %d, expected %d", page, p.owner[page], expected)
	}
	p.owner[page] = Free
	return nil
}

// AllocRun hands out the lowest-indexed CONTIGUOUS run of n Free pages, tags all
// n with owner, and returns the run's first page number. The first-fit policy is
// deterministic so the Z80 port (pp_alloc_run) and this oracle return identical
// page numbers for an identical op sequence. ok is false when no run of n
// consecutive Free pages exists (including n > nPages). Mirrors pp_alloc_run.
func (p *Pool) AllocRun(n int, owner Owner) (page int, ok bool) {
	if n <= 0 {
		panic(fmt.Sprintf("pagepool: AllocRun with n %d <= 0", n))
	}
	if owner < firstOwnerTag {
		panic(fmt.Sprintf("pagepool: AllocRun with non-owner tag %d", owner))
	}
	// Scan start indices [0, nPages-n]; the last valid start is nPages-n.
	for start := 0; start+n <= p.nPages; start++ {
		allFree := true
		for i := start; i < start+n; i++ {
			if p.owner[i] != Free {
				allFree = false
				break
			}
		}
		if allFree {
			for i := start; i < start+n; i++ {
				p.owner[i] = owner
			}
			return start, true
		}
	}
	return 0, false
}

// FreeRun returns n consecutive pages [page,page+n) to the Free pool. It asserts
// EVERY one of the n pages currently carries the expected owner tag and validates
// all n BEFORE mutating any, so a partial mismatch leaves the table untouched
// (all-or-nothing). A range error or any tag mismatch returns an error with the
// table unchanged. Mirrors pp_free_run.
func (p *Pool) FreeRun(page, n int, expected Owner) error {
	if n <= 0 {
		return fmt.Errorf("pagepool: FreeRun with n %d <= 0", n)
	}
	if page < 0 || page+n > MaxPages {
		return fmt.Errorf("pagepool: free run [%d,%d) out of range", page, page+n)
	}
	for i := page; i < page+n; i++ {
		if p.owner[i] != expected {
			return fmt.Errorf("pagepool: free run page %d: owner is %d, expected %d", i, p.owner[i], expected)
		}
	}
	for i := page; i < page+n; i++ {
		p.owner[i] = Free
	}
	return nil
}

// OwnerOf returns the current ownership tag of a page. Mirrors pp_owner_of.
func (p *Pool) OwnerOf(page int) Owner {
	if page < 0 || page >= MaxPages {
		return Reserved
	}
	return p.owner[page]
}

// FreeCount returns the number of currently-Free pages — the document budget the
// IDE surfaces to the user. Mirrors pp_free_count.
func (p *Pool) FreeCount() int {
	n := 0
	for i := 0; i < p.nPages; i++ {
		if p.owner[i] == Free {
			n++
		}
	}
	return n
}
