package tftp

import (
	"strings"
	"testing"
)

// A card with low claimed records (some leftover), and free records spread across
// the number range, exercises every strategy's ordering.
func newCard() Card {
	return Card{Records: []RecordState{
		{Record: 1, Cap: 100, Used: 100}, // full claimed — never a candidate
		{Record: 2, Cap: 100, Used: 60},  // claimed, 40 leftover
		{Record: 3, Cap: 100, Used: 0},   // free
		{Record: 5, Cap: 100, Used: 0},   // free
		{Record: 9, Cap: 100, Used: 0},   // free
	}}
}

func cfg(s Strategy) AllocConfig { return AllocConfig{Strategy: s} }

func TestAllocConfigFromHeader(t *testing.T) {
	m := parse(t, "!strategy first-free\n!pool-includes-self yes\nk local X\n")
	ac, err := m.AllocConfig()
	if err != nil {
		t.Fatal(err)
	}
	if ac.Strategy != StrategyFirstFree || !ac.PoolIncludesSelf {
		t.Errorf("config = %+v", ac)
	}

	// Defaults: absent strategy → highest-free, absent pool-includes-self → no.
	m = parse(t, "k local X\n")
	ac, _ = m.AllocConfig()
	if ac.Strategy != StrategyHighestFree || ac.PoolIncludesSelf {
		t.Errorf("default config = %+v, want highest-free / self-excluded", ac)
	}

	// fixed-list needs a pool.
	m = parse(t, "!strategy fixed-list\nk local X\n")
	if _, err := m.AllocConfig(); err == nil {
		t.Error("fixed-list without pool should error")
	}
	m = parse(t, "!strategy fixed-list\n!pool 10,11,12\nk local X\n")
	ac, err = m.AllocConfig()
	if err != nil {
		t.Fatal(err)
	}
	if ac.Strategy != StrategyFixedList || len(ac.FixedList) != 3 || ac.FixedList[0] != 10 {
		t.Errorf("fixed-list config = %+v", ac)
	}

	// Unknown strategy / bad bool surface as errors.
	if _, err := parse(t, "!strategy bogus\nk local X\n").AllocConfig(); err == nil {
		t.Error("unknown strategy should error")
	}
	if _, err := parse(t, "!pool-includes-self maybe\nk local X\n").AllocConfig(); err == nil {
		t.Error("bad pool-includes-self should error")
	}
}

// Resume invariant: a claimed record's leftover space is filled before any fresh
// record is opened, under every strategy.
func TestAllocateReusesClaimedFirst(t *testing.T) {
	for _, s := range []Strategy{StrategyHighestFree, StrategyFirstFree} {
		plan := newCard().Allocate(cfg(s), 30, -1) // fits inside record 2's 40 leftover
		if plan.Warning != "" {
			t.Fatalf("%v: unexpected warning %q", s, plan.Warning)
		}
		if len(plan.Parts) != 1 || plan.Parts[0].Record != 2 {
			t.Fatalf("%v: parts = %+v, want single record 2", s, plan.Parts)
		}
		if plan.Parts[0].StartInRecord != 60 {
			t.Errorf("%v: StartInRecord = %d, want 60 (after the used prefix)", s, plan.Parts[0].StartInRecord)
		}
	}
}

// highest-free spills from claimed leftover into the HIGHEST free record;
// first-free into the LOWEST.
func TestAllocateStrategyOrdering(t *testing.T) {
	// 40 (record 2 leftover) + 100 needed elsewhere = 140 bytes.
	hi := newCard().Allocate(cfg(StrategyHighestFree), 140, -1)
	if len(hi.Parts) != 2 || hi.Parts[0].Record != 2 || hi.Parts[1].Record != 9 {
		t.Errorf("highest-free parts = %+v, want records [2, 9]", hi.Parts)
	}

	lo := newCard().Allocate(cfg(StrategyFirstFree), 140, -1)
	if len(lo.Parts) != 2 || lo.Parts[0].Record != 2 || lo.Parts[1].Record != 3 {
		t.Errorf("first-free parts = %+v, want records [2, 3]", lo.Parts)
	}

	// The parts tile the blob contiguously and sum to its size.
	assertTiles(t, lo.Parts, 140)
	assertTiles(t, hi.Parts, 140)
}

func assertTiles(t *testing.T, parts []AllocPart, size int) {
	t.Helper()
	off, total := 0, 0
	for i, p := range parts {
		if p.Offset != off {
			t.Errorf("part %d offset = %d, want %d (contiguous)", i, p.Offset, off)
		}
		off += p.Length
		total += p.Length
	}
	if total != size {
		t.Errorf("parts sum to %d, want %d", total, size)
	}
}

// pool-includes-self excludes (or includes) the boot record.
func TestAllocateSelfExclusion(t *testing.T) {
	// Make record 3 the boot record; with self excluded it must not be used.
	plan := newCard().Allocate(cfg(StrategyFirstFree), 140, 3)
	for _, p := range plan.Parts {
		if p.Record == 3 {
			t.Fatalf("allocated to excluded self record 3: %+v", plan.Parts)
		}
	}
	// record 2 (40) + record 5 (100) covers 140.
	if len(plan.Parts) != 2 || plan.Parts[1].Record != 5 {
		t.Errorf("self-excluded parts = %+v, want [2, 5]", plan.Parts)
	}

	// With self included, record 3 is back in play (the lowest free).
	plan = newCard().Allocate(AllocConfig{Strategy: StrategyFirstFree, PoolIncludesSelf: true}, 140, 3)
	if plan.Parts[1].Record != 3 {
		t.Errorf("self-included second part = %d, want 3", plan.Parts[1].Record)
	}
}

// Fixed-list never steals an unlisted record: overflow warns and declines.
func TestAllocateFixedListNeverSteals(t *testing.T) {
	conf := AllocConfig{Strategy: StrategyFixedList, FixedList: []int{2, 5}}
	// pool capacity = 40 (rec2 leftover) + 100 (rec5) = 140; ask for 200.
	plan := newCard().Allocate(conf, 200, -1)
	if plan.Warning == "" {
		t.Fatal("expected an overflow warning")
	}
	if plan.Parts != nil {
		t.Errorf("overflow must decline (Parts nil), got %+v", plan.Parts)
	}
	if !strings.Contains(plan.Warning, "fixed-list") {
		t.Errorf("warning %q should name the fixed-list pool", plan.Warning)
	}

	// Records 3 and 9 are free but NOT in the list — they were never touched.
	if strings.Contains(plan.Warning, "stole") {
		t.Error("must not steal")
	}

	// Within capacity, the fixed list allocates fine (and only from listed records).
	plan = newCard().Allocate(conf, 120, -1)
	if plan.Warning != "" {
		t.Fatalf("unexpected warning %q", plan.Warning)
	}
	for _, p := range plan.Parts {
		if p.Record != 2 && p.Record != 5 {
			t.Errorf("allocated outside the fixed list: %+v", plan.Parts)
		}
	}
}

// first/highest-free warn-and-decline when the whole eligible card is full.
func TestAllocateCardFull(t *testing.T) {
	full := Card{Records: []RecordState{{Record: 1, Cap: 50, Used: 50}}}
	plan := full.Allocate(cfg(StrategyHighestFree), 10, -1)
	if plan.Warning == "" || plan.Parts != nil {
		t.Errorf("full card should warn+decline, got %+v / %q", plan.Parts, plan.Warning)
	}
	if !strings.Contains(plan.Warning, "card full") {
		t.Errorf("warning %q should say card full", plan.Warning)
	}
}

// A zero-byte blob still claims one record (mirrors bdos.SpanCount(0)==1).
func TestAllocateZeroByte(t *testing.T) {
	plan := newCard().Allocate(cfg(StrategyFirstFree), 0, -1)
	if plan.Warning != "" || len(plan.Parts) != 1 || plan.Parts[0].Length != 0 {
		t.Errorf("zero-byte alloc = %+v / %q", plan.Parts, plan.Warning)
	}
}
