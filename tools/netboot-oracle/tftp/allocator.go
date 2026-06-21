package tftp

// Storage-allocation strategy (i114b). Where new served blobs get written is an
// explicit, user-chosen policy saved in the manifest header (design §4), with a
// reasonable default (highest-free, design §6 decision 4). This gives the user
// control instead of the system silently colonising records.
//
// This file is the Go authority for that allocation over a modelled card. The
// header policy is parsed out of the generic Manifest.Header (populated by the
// i114a parser) and applied to a Card (the set of records with their used/free
// bytes) to produce an ordered write plan. It is pure logic, host-verifiable; the
// real RST 8 HRECORD/HSAVE per record stays the live-Trinity gate (CLAUDE.md §5).
//
// The load-bearing invariant (design §4): overflow is an EXPLICIT WARNING, never a
// silent record-steal. A fixed-list pool that can't hold the blob warns and
// declines rather than grabbing an unlisted record; first/highest-free decline
// (warn) only when the whole card is full. Resuming a session reuses
// already-claimed leftover space before opening a fresh record, so
// reboot-then-add-a-file packs into existing records.

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Strategy is a storage-allocation policy (design §4).
type Strategy int

const (
	// StrategyHighestFree packs already-claimed leftover space, then prefers the
	// highest-numbered free records — keeping the user's low, memorable slots for
	// their own disks. The default (design §6 decision 4).
	StrategyHighestFree Strategy = iota
	// StrategyFirstFree packs already-claimed leftover space, then the lowest free record.
	StrategyFirstFree
	// StrategyFixedList allocates only from a hand-maintained record list; overflow
	// warns and declines, never steals an unlisted record.
	StrategyFixedList
)

func (s Strategy) String() string {
	switch s {
	case StrategyHighestFree:
		return "highest-free"
	case StrategyFirstFree:
		return "first-free"
	case StrategyFixedList:
		return "fixed-list"
	default:
		return "?"
	}
}

// Header directive keys (design §4 header section).
const (
	hdrStrategy = "strategy"
	hdrPoolSelf = "pool-includes-self"
	hdrPool     = "pool" // the fixed list (StrategyFixedList)
)

// AllocConfig is the resolved allocation policy. SelfRecord is supplied at
// allocation time (the boot record number is only known at runtime); whether it
// is eligible comes from the header (PoolIncludesSelf, design §4 invariant).
type AllocConfig struct {
	Strategy         Strategy
	PoolIncludesSelf bool
	FixedList        []int // StrategyFixedList: the eligible record numbers
}

// ParseStrategy maps a header value to a Strategy. An empty value defaults to
// highest-free (design §6 decision 4); an unknown value is an error rather than a
// silent fallback — the policy must be unambiguous.
func ParseStrategy(s string) (Strategy, error) {
	switch s {
	case "", "highest-free":
		return StrategyHighestFree, nil
	case "first-free":
		return StrategyFirstFree, nil
	case "fixed-list":
		return StrategyFixedList, nil
	default:
		return 0, fmt.Errorf("unknown storage strategy %q (want highest-free, first-free, or fixed-list)", s)
	}
}

// AllocConfig reads the allocation policy out of the manifest header. Defaults:
// strategy highest-free, pool-includes-self no (keep the boot record out of the
// pool unless declared). A fixed-list strategy requires a non-empty pool list.
func (m *Manifest) AllocConfig() (AllocConfig, error) {
	strat, err := ParseStrategy(m.Header[hdrStrategy])
	if err != nil {
		return AllocConfig{}, err
	}
	self, err := parseBoolHeader(m.Header[hdrPoolSelf], false)
	if err != nil {
		return AllocConfig{}, fmt.Errorf("%s: %w", hdrPoolSelf, err)
	}
	cfg := AllocConfig{Strategy: strat, PoolIncludesSelf: self}
	if raw := m.Header[hdrPool]; raw != "" {
		list, err := parseRecordList(raw)
		if err != nil {
			return AllocConfig{}, fmt.Errorf("%s: %w", hdrPool, err)
		}
		cfg.FixedList = list
	}
	if strat == StrategyFixedList && len(cfg.FixedList) == 0 {
		return AllocConfig{}, fmt.Errorf("strategy fixed-list needs a non-empty %q header", hdrPool)
	}
	return cfg, nil
}

func parseBoolHeader(v string, def bool) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "":
		return def, nil
	case "yes", "true", "1":
		return true, nil
	case "no", "false", "0":
		return false, nil
	default:
		return false, fmt.Errorf("want yes/no, got %q", v)
	}
}

func parseRecordList(raw string) ([]int, error) {
	parts := strings.Split(raw, ",")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("bad record number %q", p)
		}
		out = append(out, n)
	}
	return out, nil
}

// RecordState is one storage record for allocation: its number, capacity, and how
// many bytes are already used. Used == 0 is a free (unclaimed) record; 0 < Used <
// Cap is a claimed record with leftover space the allocator reuses first.
type RecordState struct {
	Record int
	Cap    int
	Used   int
}

// Free returns the unused bytes in the record (never negative).
func (r RecordState) Free() int {
	if r.Used >= r.Cap {
		return 0
	}
	return r.Cap - r.Used
}

func (r RecordState) claimed() bool { return r.Used > 0 }

// Card is the modelled set of storage records the allocator chooses from.
type Card struct {
	Records []RecordState
}

// AllocPart is one record assigned to a blob: the [Offset, Offset+Length) byte
// range of the blob it holds, written at byte StartInRecord within the record
// (the record's prior Used offset — non-zero when reusing a claimed record's
// leftover space).
type AllocPart struct {
	Record        int
	Offset        int // byte offset within the blob
	Length        int
	StartInRecord int // byte offset within the record where this part is written
}

// AllocPlan is the result of allocating a blob. Warning is non-empty exactly when
// the pool could not hold the blob without violating the never-steal invariant; in
// that case Parts is nil (the allocation is declined, not partially committed).
type AllocPlan struct {
	Parts   []AllocPart
	Warning string
}

// Allocate plans where a size-byte blob is written on the card under cfg.
// Already-claimed records' leftover space is reused first (resume packs into
// existing records); then free records are opened in strategy order (lowest for
// first-free, highest for highest-free, in-list for fixed-list). The boot record
// (selfRecord) is excluded unless cfg.PoolIncludesSelf. If the eligible pool can't
// hold the blob, Allocate returns a warning and declines (Parts nil) rather than
// stealing an unlisted/excluded record.
func (c Card) Allocate(cfg AllocConfig, size, selfRecord int) AllocPlan {
	candidates := c.orderedCandidates(cfg, selfRecord)

	parts := make([]AllocPart, 0, len(candidates))
	remaining, offset := size, 0
	// A zero-byte blob still occupies one record (mirrors bdos.SpanCount(0)==1).
	if size == 0 {
		for _, r := range candidates {
			if r.Free() > 0 {
				return AllocPlan{Parts: []AllocPart{{Record: r.Record, Offset: 0, Length: 0, StartInRecord: r.Used}}}
			}
		}
		return AllocPlan{Warning: overflowWarning(cfg, 0)}
	}
	for _, r := range candidates {
		if remaining == 0 {
			break
		}
		free := r.Free()
		if free <= 0 {
			continue
		}
		take := free
		if take > remaining {
			take = remaining
		}
		parts = append(parts, AllocPart{Record: r.Record, Offset: offset, Length: take, StartInRecord: r.Used})
		offset += take
		remaining -= take
	}
	if remaining > 0 {
		return AllocPlan{Warning: overflowWarning(cfg, remaining)}
	}
	return AllocPlan{Parts: parts}
}

func overflowWarning(cfg AllocConfig, short int) string {
	if cfg.Strategy == StrategyFixedList {
		return fmt.Sprintf("fixed-list pool full: short by %d bytes — add a record to the pool list (never stealing an unlisted record)", short)
	}
	return fmt.Sprintf("card full: short by %d bytes — no free record available", short)
}

// orderedCandidates returns the eligible records in allocation order: claimed
// records with leftover space first (ascending by number — deterministic resume
// reuse), then free records in strategy order.
func (c Card) orderedCandidates(cfg AllocConfig, selfRecord int) []RecordState {
	eligible := func(r RecordState) bool {
		if r.Record == selfRecord && !cfg.PoolIncludesSelf {
			return false
		}
		if cfg.Strategy == StrategyFixedList {
			return containsInt(cfg.FixedList, r.Record)
		}
		return true
	}

	var claimed, free []RecordState
	for _, r := range c.Records {
		if !eligible(r) {
			continue
		}
		if r.claimed() && r.Free() > 0 {
			claimed = append(claimed, r)
		} else if !r.claimed() {
			free = append(free, r)
		}
	}
	sort.Slice(claimed, func(i, j int) bool { return claimed[i].Record < claimed[j].Record })
	switch cfg.Strategy {
	case StrategyHighestFree:
		sort.Slice(free, func(i, j int) bool { return free[i].Record > free[j].Record })
	default: // first-free and fixed-list fill from the lowest free record
		sort.Slice(free, func(i, j int) bool { return free[i].Record < free[j].Record })
	}
	return append(claimed, free...)
}

func containsInt(xs []int, x int) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
