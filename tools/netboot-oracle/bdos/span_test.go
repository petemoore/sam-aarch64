package bdos

import (
	"testing"
)

// hash constructs a [32]byte with the first 3 bytes set to b0,b1,b2 and the
// rest zeroed. Used in tests where only the hash prefix (the naming input)
// matters.
func hash3(b0, b1, b2 byte) [32]byte {
	var h [32]byte
	h[0], h[1], h[2] = b0, b1, b2
	return h
}

func TestSpanCount(t *testing.T) {
	const cap = 1000
	cases := []struct {
		size, want int
	}{
		{0, 1},       // empty object: one empty record
		{1, 1},       // tiny
		{cap - 1, 1}, // just under one record
		{cap, 1},     // exactly one record
		{cap + 1, 2}, // one byte over → two records
		{2 * cap, 2}, // exactly two records
		{2*cap + 1, 3},
		{2979296, 2980}, // start.elf at cap=1000
	}
	for _, c := range cases {
		if got := SpanCount(c.size, cap); got != c.want {
			t.Errorf("SpanCount(%d, %d) = %d, want %d", c.size, cap, got, c.want)
		}
	}
	if got := SpanCount(100, 0); got != 0 {
		t.Errorf("SpanCount with cap 0 = %d, want 0 (invalid)", got)
	}
}

func TestSpanRecordName(t *testing.T) {
	cases := []struct {
		b0, b1, b2 byte
		index      int
		want       string
	}{
		{0xAB, 0xCD, 0xEF, 0, "abcdef000"},   // worked example: index 0
		{0xAB, 0xCD, 0xEF, 1, "abcdef001"},   // index 1
		{0xAB, 0xCD, 0xEF, 2, "abcdef002"},   // index 2
		{0xAB, 0xCD, 0xEF, 999, "abcdef999"}, // max index
		{0x00, 0x00, 0x00, 0, "000000000"},   // all-zero prefix
		{0xFF, 0xFF, 0xFF, 0, "ffffff000"},   // all-FF prefix
		{0x01, 0x23, 0x45, 42, "012345042"},  // mixed nibbles
	}
	for _, c := range cases {
		h := hash3(c.b0, c.b1, c.b2)
		got := SpanRecordName(h, c.index)
		if got != c.want {
			t.Errorf("SpanRecordName([%02x%02x%02x...], %d) = %q, want %q", c.b0, c.b1, c.b2, c.index, got, c.want)
		}
		if len(got) > NameLen {
			t.Errorf("SpanRecordName([%02x%02x%02x...], %d) = %q exceeds the %d-char B-DOS name field", c.b0, c.b1, c.b2, c.index, got, NameLen)
		}
		// All valid names are exactly 9 chars (6 hex + 3 decimal).
		if len(got) != 9 {
			t.Errorf("SpanRecordName([%02x%02x%02x...], %d) = %q has length %d, want 9", c.b0, c.b1, c.b2, c.index, got, len(got))
		}
	}

	// Identical hashes produce identical name prefixes (dedup property).
	h1 := hash3(0x12, 0x34, 0x56)
	if a, b := SpanRecordName(h1, 0), SpanRecordName(h1, 0); a != b {
		t.Errorf("identical hash produced different names: %q vs %q", a, b)
	}

	// Different hashes produce different name prefixes.
	h2 := hash3(0x78, 0x9A, 0xBC)
	if a, b := SpanRecordName(h1, 0), SpanRecordName(h2, 0); a == b {
		t.Errorf("different hashes collide: both → %q", a)
	}
}

func TestSpanPlanNonSpanned(t *testing.T) {
	// size ≤ cap → a single record; content-addressed as <hash6>000.
	h := hash3(0xAB, 0xCD, 0xEF)
	recs := SpanPlan(h, 21752, 100000)
	if len(recs) != 1 {
		t.Fatalf("non-spanned plan has %d records, want 1", len(recs))
	}
	want := SpanRecord{Name: "abcdef000", Offset: 0, Length: 21752}
	if recs[0] != want {
		t.Errorf("non-spanned record = %+v, want %+v", recs[0], want)
	}
	// Exactly cap is still a single record.
	h2 := hash3(0x01, 0x02, 0x03)
	if recs := SpanPlan(h2, 1000, 1000); len(recs) != 1 || recs[0].Name != "010203000" {
		t.Errorf("size==cap plan = %+v, want one hash-named record", recs)
	}
}

func TestSpanPlanSpanned(t *testing.T) {
	h := hash3(0xAB, 0xCD, 0xEF)
	recs := SpanPlan(h, 2979296, 1000000)
	want := []SpanRecord{
		{"abcdef000", 0, 1000000},
		{"abcdef001", 1000000, 1000000},
		{"abcdef002", 2000000, 979296},
	}
	if len(recs) != len(want) {
		t.Fatalf("spanned plan has %d records, want %d", len(recs), len(want))
	}
	for i := range want {
		if recs[i] != want[i] {
			t.Errorf("record %d = %+v, want %+v", i, recs[i], want[i])
		}
	}

	// Exact multiple: two full records, no partial tail.
	h2 := hash3(0x00, 0x11, 0x22)
	exact := SpanPlan(h2, 2000000, 1000000)
	if len(exact) != 2 || exact[0].Length != 1000000 || exact[1].Length != 1000000 {
		t.Errorf("exact-multiple plan = %+v, want two full records", exact)
	}
}

// TestSpanPlanInvariants checks the structural guarantees across many sizes/caps:
// the records cover [0,size) contiguously with no gap or overlap, each ≤ cap, and
// the count matches SpanCount. These are the properties the serve-time reassembly
// relies on (read the records in order → exactly the original bytes).
func TestSpanPlanInvariants(t *testing.T) {
	h := hash3(0x12, 0x34, 0x56)
	check := func(size, recordCap int) {
		recs := SpanPlan(h, size, recordCap)
		if got, want := len(recs), SpanCount(size, recordCap); got != want {
			t.Errorf("SpanPlan(%d,%d): %d records, SpanCount says %d", size, recordCap, got, want)
			return
		}
		off, total := 0, 0
		for i, r := range recs {
			if r.Offset != off {
				t.Errorf("SpanPlan(%d,%d) record %d offset = %d, want %d (gap/overlap)", size, recordCap, i, r.Offset, off)
			}
			if r.Length > recordCap || r.Length < 0 {
				t.Errorf("SpanPlan(%d,%d) record %d length %d out of range (cap %d)", size, recordCap, i, r.Length, recordCap)
			}
			off += r.Length
			total += r.Length
		}
		if total != size {
			t.Errorf("SpanPlan(%d,%d) lengths sum to %d, want %d", size, recordCap, total, size)
		}
	}
	// Small caps × small sizes: exhaustive boundary coverage (a tiny cap with a
	// multi-MB size would build a multi-million-record slice for no extra signal).
	smallSizes := []int{0, 1, 2, 6, 7, 8, 99, 100, 101, 999, 1000, 1001, 2000, 2001}
	for _, recordCap := range []int{1, 7, 100, 1000} {
		for _, size := range smallSizes {
			check(size, recordCap)
		}
	}
	// The real firmware sizes against realistic per-record caps.
	for _, recordCap := range []int{16384, 65536, 500000, 1000000} {
		for _, size := range []int{1594, 52476, 2979296, 2255072, 7274, 5413} {
			check(size, recordCap)
		}
	}
}

// TestSpanPlanNamesMatchSpanCount: every record name equals SpanRecordName(hash, i),
// and StoredRecordNames returns the same sequence.
func TestSpanPlanNamesMatchSpanCount(t *testing.T) {
	h := hash3(0x01, 0x23, 0x45)
	const size, recordCap = 2255072, 500000
	recs := SpanPlan(h, size, recordCap)
	n := SpanCount(size, recordCap)
	if len(recs) != n {
		t.Fatalf("plan has %d records, SpanCount says %d", len(recs), n)
	}
	for i := 0; i < n; i++ {
		if want := SpanRecordName(h, i); recs[i].Name != want {
			t.Errorf("record %d name = %q, want %q", i, recs[i].Name, want)
		}
	}
	// StoredRecordNames returns the same names in the same order.
	stored := StoredRecordNames(h, n)
	if len(stored) != n {
		t.Fatalf("StoredRecordNames returned %d names, want %d", len(stored), n)
	}
	for i := 0; i < n; i++ {
		if stored[i] != recs[i].Name {
			t.Errorf("StoredRecordNames[%d] = %q, SpanPlan[%d].Name = %q", i, stored[i], i, recs[i].Name)
		}
	}
}

// TestStoredRecordNamesDedup: two blobs with the same hash produce identical
// record names (dedup falls out), while different hashes produce different names.
func TestStoredRecordNamesDedup(t *testing.T) {
	h1 := hash3(0xAB, 0xCD, 0xEF)
	h2 := hash3(0xAB, 0xCD, 0xEF) // same bytes
	h3 := hash3(0x11, 0x22, 0x33) // different

	n1 := StoredRecordNames(h1, 3)
	n2 := StoredRecordNames(h2, 3)
	n3 := StoredRecordNames(h3, 3)

	for i := range n1 {
		if n1[i] != n2[i] {
			t.Errorf("identical hashes produced different name at index %d: %q vs %q", i, n1[i], n2[i])
		}
		if n1[i] == n3[i] {
			t.Errorf("different hashes produced same name at index %d: %q", i, n1[i])
		}
	}
}
