package format

import (
	"bytes"
	"reflect"
	"testing"
)

// TestHeaderTablesRoundTrip writes a populated label+local table through
// WriteFile and reads it back, asserting exact recovery — including
// multi-byte varint deltas (an offset gap > 127) and same-offset ties
// (which WriteFile must order deterministically by name_id / digit).
func TestHeaderTablesRoundTrip(t *testing.T) {
	st := NewSymbolTable()
	for _, n := range []string{"start", "loop", "exit", "tie_a", "tie_b"} {
		st.Intern(n)
	}

	// Deliberately out of offset order on input — WriteFile sorts copies.
	// Two labels share offset 0x300 (a tie, resolved by name_id asc).
	labels := []LabelRow{
		{NameID: 2, Offset: 0x300},          // exit (tie with tie_b below)
		{NameID: 0, Offset: 0},              // start
		{NameID: 1, Offset: 0x1000},         // loop — multi-byte varint delta
		{NameID: 4, Offset: 0x300},          // tie_b (same offset as exit)
		{NameID: 3, Offset: 0x1_0000_0000},  // tie_a — huge offset (5-byte varint delta)
	}
	// Local rows: same digit at multiple sites + a multi-byte delta.
	locals := []LocalRow{
		{Digit: 1, Offset: 0x10},
		{Digit: 2, Offset: 0x10}, // tie with digit 1 at 0x10 (digit asc)
		{Digit: 1, Offset: 0x2000},
		{Digit: 3, Offset: 0x4},
	}

	var rw RecordWriter
	rw.WriteDirective(0, 0, nil) // a .text no-op marker in the record stream

	var buf bytes.Buffer
	if err := WriteFile(&buf, st, labels, locals, rw.Bytes(), nil, nil); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	f, err := ReadFile(buf.Bytes())
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	// Expected order = sorted by offset asc, ties by name_id / digit asc.
	wantLabels := []LabelRow{
		{NameID: 0, Offset: 0},
		{NameID: 2, Offset: 0x300},
		{NameID: 4, Offset: 0x300},
		{NameID: 1, Offset: 0x1000},
		{NameID: 3, Offset: 0x1_0000_0000},
	}
	if !reflect.DeepEqual(f.Labels, wantLabels) {
		t.Errorf("labels:\n got %+v\nwant %+v", f.Labels, wantLabels)
	}

	wantLocals := []LocalRow{
		{Digit: 3, Offset: 0x4},
		{Digit: 1, Offset: 0x10},
		{Digit: 2, Offset: 0x10},
		{Digit: 1, Offset: 0x2000},
	}
	if !reflect.DeepEqual(f.Locals, wantLocals) {
		t.Errorf("locals:\n got %+v\nwant %+v", f.Locals, wantLocals)
	}

	// The record stream after the tables must survive intact.
	if len(f.Records) != 1 || f.Records[0].Kind != KindDirective {
		t.Errorf("records mismatch: got %+v", f.Records)
	}
}

// TestHeaderTablesEmpty confirms the symbolic-`.tbn` path (nil/nil) writes
// two empty tables and reads back nil/empty slices.
func TestHeaderTablesEmpty(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteFile(&buf, NewSymbolTable(), nil, nil, nil, nil, nil); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	f, err := ReadFile(buf.Bytes())
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(f.Labels) != 0 {
		t.Errorf("labels = %+v, want empty", f.Labels)
	}
	if len(f.Locals) != 0 {
		t.Errorf("locals = %+v, want empty", f.Locals)
	}
}
