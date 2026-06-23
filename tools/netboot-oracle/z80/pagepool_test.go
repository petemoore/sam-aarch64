// pagepool_test.go — host-verification of src/pagepool.asm (item i2a).
//
// Drives the Z80 page-allocator routines under the flat-memory koron-go/z80
// harness and asserts they track the Go authority (tools/sam-aarch64/pagepool)
// over random alloc/free op sequences — the same Go-authority-+-Z80-port pattern
// as editmodel_test.go.
//
// TestPagePoolInitReserveZ80: pp_init sizes the pool; pp_reserve removes a page;
//
//	pp_owner_of / pp_free_count read back the table.
//
// TestPagePoolRandomOpsZ80: a random walk of alloc/free ops compared after every
//
//	step against the pagepool.Pool oracle — page numbers, the full page_owner[]
//	table, and the free count must all match.
//
// TestPagePoolClaimAllFreeAllZ80: the spec §6 invariant — claim every FREE page,
//
//	confirm reserved pages never moved, free them all, confirm the count is
//	restored. This is the property the boot self-test (a later i2 brick) asserts.
package z80_test

import (
	"fmt"
	"math/rand"
	"testing"

	pagepool "github.com/petemoore/sam-aarch64/tools/sam-aarch64/pagepool"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	ppBinPath = "../../../build/pagepool.bin"
	ppMapPath = "../../../build/pagepool.map"
)

func loadPagePool(t *testing.T) *z80h.Machine {
	t.Helper()
	mac, err := z80h.Load(ppBinPath, ppMapPath)
	if err != nil {
		t.Skipf("pagepool binary not built (%s); run `make pagepool-z80`: %v", ppBinPath, err)
	}
	return mac
}

func ppSym(t *testing.T, mac *z80h.Machine, name string) uint16 {
	t.Helper()
	addr, err := mac.Sym(name)
	if err != nil {
		t.Fatalf("symbol %q: %v", name, err)
	}
	return addr
}

// ppFail is the sentinel A-value the Z80 routines return on failure (PP_FAIL).
const ppFail = 0xFF

// ppInit drives pp_init(A=nPages) on the Z80.
func ppInit(t *testing.T, mac *z80h.Machine, nPages int) {
	t.Helper()
	if _, err := mac.CallEntry("pp_init", z80h.Entry{A: uint8(nPages)}); err != nil {
		t.Fatalf("pp_init(%d): %v", nPages, err)
	}
}

// ppReserve drives pp_reserve(A=page); returns true on success.
func ppReserve(t *testing.T, mac *z80h.Machine, page int) bool {
	t.Helper()
	res, err := mac.CallEntry("pp_reserve", z80h.Entry{A: uint8(page)})
	if err != nil {
		t.Fatalf("pp_reserve(%d): %v", page, err)
	}
	return res.A != ppFail
}

// ppAlloc drives pp_alloc_page(A=owner); returns (page, ok).
func ppAlloc(t *testing.T, mac *z80h.Machine, owner pagepool.Owner) (int, bool) {
	t.Helper()
	res, err := mac.CallEntry("pp_alloc_page", z80h.Entry{A: uint8(owner)})
	if err != nil {
		t.Fatalf("pp_alloc_page(%d): %v", owner, err)
	}
	if res.A == ppFail {
		return 0, false
	}
	return int(res.A), true
}

// ppFree drives pp_free_page(A=page, B=expected owner); returns true on success.
// B is the high byte of the BC register the harness preloads.
func ppFree(t *testing.T, mac *z80h.Machine, page int, owner pagepool.Owner) bool {
	t.Helper()
	res, err := mac.CallEntry("pp_free_page", z80h.Entry{A: uint8(page), BC: uint16(owner) << 8})
	if err != nil {
		t.Fatalf("pp_free_page(%d): %v", page, err)
	}
	return res.A != ppFail
}

// ppOwnerOf drives pp_owner_of(A=page).
func ppOwnerOf(t *testing.T, mac *z80h.Machine, page int) pagepool.Owner {
	t.Helper()
	res, err := mac.CallEntry("pp_owner_of", z80h.Entry{A: uint8(page)})
	if err != nil {
		t.Fatalf("pp_owner_of(%d): %v", page, err)
	}
	return pagepool.Owner(res.A)
}

// ppFreeCount drives pp_free_count.
func ppFreeCount(t *testing.T, mac *z80h.Machine) int {
	t.Helper()
	res, err := mac.CallEntry("pp_free_count", z80h.Entry{})
	if err != nil {
		t.Fatalf("pp_free_count: %v", err)
	}
	return int(res.A)
}

// ppOwnerTable reads the whole page_owner[] table straight out of Z80 memory.
func ppOwnerTable(mac *z80h.Machine, ownerAddr uint16) []pagepool.Owner {
	raw := mac.Read(ownerAddr, pagepool.MaxPages)
	out := make([]pagepool.Owner, pagepool.MaxPages)
	for i, b := range raw {
		out[i] = pagepool.Owner(b)
	}
	return out
}

func TestPagePoolInitReserveZ80(t *testing.T) {
	mac := loadPagePool(t)
	ownerAddr := ppSym(t, mac, "PP_OWNER")

	var oracle pagepool.Pool
	oracle.Init(16)
	ppInit(t, mac, 16)

	if got, want := ppFreeCount(t, mac), oracle.FreeCount(); got != want {
		t.Errorf("free count after Init(16) = %d, want %d", got, want)
	}

	// Reserve the system page (1) and a screen page (8).
	for _, p := range []int{1, 8} {
		if !ppReserve(t, mac, p) {
			t.Fatalf("pp_reserve(%d) failed", p)
		}
		oracle.Reserve(p)
	}
	if got, want := ppFreeCount(t, mac), oracle.FreeCount(); got != want {
		t.Errorf("free count after reserves = %d, want %d", got, want)
	}

	// Out-of-range reserve fails.
	if ppReserve(t, mac, pagepool.MaxPages) {
		t.Errorf("pp_reserve(%d) should fail (out of range)", pagepool.MaxPages)
	}

	// The whole table matches the oracle.
	got := ppOwnerTable(mac, ownerAddr)
	for i := 0; i < pagepool.MaxPages; i++ {
		if got[i] != oracle.OwnerOf(i) {
			t.Errorf("page %d: Z80 owner %d, oracle %d", i, got[i], oracle.OwnerOf(i))
		}
	}
}

func TestPagePoolRandomOpsZ80(t *testing.T) {
	for _, seed := range []int64{1, 42, 2026} {
		seed := seed
		t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) {
			testPagePoolWithSeed(t, seed)
		})
	}
}

func testPagePoolWithSeed(t *testing.T, seed int64) {
	mac := loadPagePool(t)
	ownerAddr := ppSym(t, mac, "PP_OWNER")

	const nPages = 24
	var oracle pagepool.Pool
	oracle.Init(nPages)
	ppInit(t, mac, nPages)
	// Reserve a couple of system pages on both sides.
	for _, p := range []int{0, 1} {
		ppReserve(t, mac, p)
		oracle.Reserve(p)
	}

	rng := rand.New(rand.NewSource(seed))
	tags := []pagepool.Owner{pagepool.OwnerDoc, pagepool.OwnerIn, pagepool.OwnerOut, pagepool.OwnerScratch}
	// live tracks the tag each currently-allocated page was claimed under.
	live := map[int]pagepool.Owner{}

	for step := 0; step < 1500; step++ {
		if rng.Intn(2) == 0 {
			tag := tags[rng.Intn(len(tags))]
			zPage, zOK := ppAlloc(t, mac, tag)
			oPage, oOK := oracle.Alloc(tag)
			if zOK != oOK || (zOK && zPage != oPage) {
				t.Fatalf("step %d: Z80 alloc=(%d,%v) oracle=(%d,%v)", step, zPage, zOK, oPage, oOK)
			}
			if zOK {
				live[zPage] = tag
			}
		} else if len(live) > 0 {
			// Pick a random live page to free.
			pick := rng.Intn(len(live))
			i, page, tag := 0, -1, pagepool.Owner(0)
			for p, tg := range live {
				if i == pick {
					page, tag = p, tg
					break
				}
				i++
			}
			zOK := ppFree(t, mac, page, tag)
			oErr := oracle.Free(page, tag)
			if zOK != (oErr == nil) {
				t.Fatalf("step %d: Z80 free(%d,%d) ok=%v, oracle err=%v", step, page, tag, zOK, oErr)
			}
			delete(live, page)
		}

		// After every op the full table must agree with the oracle.
		got := ppOwnerTable(mac, ownerAddr)
		for i := 0; i < pagepool.MaxPages; i++ {
			if got[i] != oracle.OwnerOf(i) {
				t.Fatalf("step %d: page %d Z80 owner %d, oracle %d", step, i, got[i], oracle.OwnerOf(i))
			}
		}
		if got, want := ppFreeCount(t, mac), oracle.FreeCount(); got != want {
			t.Fatalf("step %d: Z80 free count %d, oracle %d", step, got, want)
		}
	}

	// A wrong-tag free is rejected and leaves the table unchanged.
	if page, ok := ppAlloc(t, mac, pagepool.OwnerDoc); ok {
		oracle.Alloc(pagepool.OwnerDoc)
		if ppFree(t, mac, page, pagepool.OwnerIn) {
			t.Errorf("wrong-tag free of page %d was accepted", page)
		}
		if ppOwnerOf(t, mac, page) != pagepool.OwnerDoc {
			t.Errorf("rejected free mutated the table at page %d", page)
		}
	}
}

func TestPagePoolClaimAllFreeAllZ80(t *testing.T) {
	mac := loadPagePool(t)

	const nPages = 16
	var oracle pagepool.Pool
	oracle.Init(nPages)
	ppInit(t, mac, nPages)
	reserved := []int{1, 5}
	for _, p := range reserved {
		ppReserve(t, mac, p)
		oracle.Reserve(p)
	}
	want := ppFreeCount(t, mac)
	if want != oracle.FreeCount() {
		t.Fatalf("free count %d != oracle %d", want, oracle.FreeCount())
	}

	// Claim every FREE page.
	claimed := []int{}
	for {
		page, ok := ppAlloc(t, mac, pagepool.OwnerScratch)
		if !ok {
			break
		}
		claimed = append(claimed, page)
	}
	if len(claimed) != want {
		t.Fatalf("claimed %d pages, want %d", len(claimed), want)
	}
	// Reserved pages never moved.
	for _, p := range reserved {
		if ppOwnerOf(t, mac, p) != pagepool.Reserved {
			t.Errorf("reserved page %d changed during claim-all (owner %d)", p, ppOwnerOf(t, mac, p))
		}
	}
	if ppFreeCount(t, mac) != 0 {
		t.Errorf("free count after claim-all = %d, want 0", ppFreeCount(t, mac))
	}

	// Free them all; the count is restored exactly.
	for _, page := range claimed {
		if !ppFree(t, mac, page, pagepool.OwnerScratch) {
			t.Fatalf("free(%d) rejected during free-all", page)
		}
	}
	if got := ppFreeCount(t, mac); got != want {
		t.Errorf("free count after free-all = %d, want %d", got, want)
	}
	for _, p := range reserved {
		if ppOwnerOf(t, mac, p) != pagepool.Reserved {
			t.Errorf("reserved page %d not Reserved after round-trip", p)
		}
	}
}
