package bdos

import (
	"fmt"
	"testing"
)

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
		name  string
		index int
		want  string
	}{
		{"abc", 5, "abc005"},            // short name, full prefix
		{"abcdefg", 0, "abcdefg000"},    // name exactly 7 chars (the prefix width)
		{"start.elf", 12, "start.e012"}, // 9 chars → truncated to 7
		{"start4.elf", 3, "start4.003"}, // 10 chars → truncated to 7
		{"start.elf", 0, "start.e000"},
	}
	for _, c := range cases {
		got := SpanRecordName(c.name, c.index)
		if got != c.want {
			t.Errorf("SpanRecordName(%q, %d) = %q, want %q", c.name, c.index, got, c.want)
		}
		if len(got) > NameLen {
			t.Errorf("SpanRecordName(%q, %d) = %q exceeds the %d-char B-DOS name field", c.name, c.index, got, NameLen)
		}
	}
	// The two spanning RPi firmware files must get distinct prefixes (the
	// documented unique-7-char-prefix constraint).
	if a, b := SpanRecordName("start.elf", 0), SpanRecordName("start4.elf", 0); a == b {
		t.Errorf("start.elf and start4.elf collide: both → %q", a)
	}
}

func TestSpanPlanNonSpanned(t *testing.T) {
	// size ≤ cap → a single record under the plain name (no suffix), so the
	// kernel and small files keep their natural TFTP name.
	recs := SpanPlan("kernel8.img", 21752, 100000)
	if len(recs) != 1 {
		t.Fatalf("non-spanned plan has %d records, want 1", len(recs))
	}
	want := SpanRecord{Name: "kernel8.img", Offset: 0, Length: 21752}
	if recs[0] != want {
		t.Errorf("non-spanned record = %+v, want %+v", recs[0], want)
	}
	// Exactly cap is still a single plain record (the boundary).
	if recs := SpanPlan("x", 1000, 1000); len(recs) != 1 || recs[0].Name != "x" {
		t.Errorf("size==cap plan = %+v, want one plain record", recs)
	}
}

func TestSpanPlanSpanned(t *testing.T) {
	recs := SpanPlan("start.elf", 2979296, 1000000)
	want := []SpanRecord{
		{"start.e000", 0, 1000000},
		{"start.e001", 1000000, 1000000},
		{"start.e002", 2000000, 979296},
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
	exact := SpanPlan("x", 2000000, 1000000)
	if len(exact) != 2 || exact[0].Length != 1000000 || exact[1].Length != 1000000 {
		t.Errorf("exact-multiple plan = %+v, want two full records", exact)
	}
}

// TestSpanPlanInvariants checks the structural guarantees across many sizes/caps:
// the records cover [0,size) contiguously with no gap or overlap, each ≤ cap, and
// the count matches SpanCount. These are the properties the serve-time reassembly
// relies on (read the records in order → exactly the original bytes).
func TestSpanPlanInvariants(t *testing.T) {
	check := func(size, recordCap int) {
		recs := SpanPlan("firmware.x", size, recordCap)
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

// TestSpanPlanNamesMatchSpanCount: a spanned object's record names are exactly
// SpanRecordName(name, 0..N-1), the names the server reconstructs from the size.
func TestSpanPlanNamesMatchSpanCount(t *testing.T) {
	const name, size, recordCap = "start4.elf", 2255072, 500000
	recs := SpanPlan(name, size, recordCap)
	n := SpanCount(size, recordCap)
	if len(recs) != n {
		t.Fatalf("plan has %d records, SpanCount says %d", len(recs), n)
	}
	for i := 0; i < n; i++ {
		if want := SpanRecordName(name, i); recs[i].Name != want {
			t.Errorf("record %d name = %q, want %q", i, recs[i].Name, want)
		}
	}
	if recs[0].Name != fmt.Sprintf("start4.%03d", 0) {
		t.Errorf("first spanned record name = %q, unexpected", recs[0].Name)
	}
}
