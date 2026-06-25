package render

import (
	"os"
	"testing"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

// TestEmitLinesEquivalentToEmitFile asserts the per-record line adapter
// reassembles to exactly the flat text EmitFile produces — the two share the
// emit helpers, so a divergence is a wiring bug in the span attribution.
func TestEmitLinesEquivalentToEmitFile(t *testing.T) {
	// A small synthetic in-memory IR exercising every record path the adapter
	// special-cases: directive, label, local, inline comment, instruction.
	f := &format.File{
		Names: []string{"_start", "loop"},
		Records: []format.Record{
			{Kind: format.KindComment, Placement: 0, Body: []byte("a header comment")},
			{Kind: format.KindLabelDef, SymbolID: 0},
			{Kind: format.KindInst, MnemonicID: mnemID(t, "nop"), OperandCount: 0},
			{Kind: format.KindLocalDef, Digit: 1},
			{Kind: format.KindInst, MnemonicID: mnemID(t, "nop"), OperandCount: 0},
			{Kind: format.KindComment, Placement: 1, Body: []byte("trailing note")},
		},
	}

	flat, err := EmitFile(f)
	if err != nil {
		t.Fatalf("EmitFile: %v", err)
	}
	lines, err := EmitLines(f)
	if err != nil {
		t.Fatalf("EmitLines: %v", err)
	}
	if got, want := JoinLines(lines), string(flat); got != want {
		t.Fatalf("EmitLines reassembled != EmitFile\n--- got ---\n%q\n--- want ---\n%q", got, want)
	}
	// Every line must carry a record index in range (or -1 for sidecar).
	for i, l := range lines {
		if l.RecordIdx < -1 || l.RecordIdx >= len(f.Records) {
			t.Errorf("line %d (%q) RecordIdx %d out of range", i, l.Text, l.RecordIdx)
		}
	}
}

// TestEmitLinesOnRealReleaseTbn round-trips the per-record adapter against the
// real release `.tbn` when it is present (the host build-target product). It is
// skipped when the file has not been built, since it is not a vendored fixture.
func TestEmitLinesOnRealReleaseTbn(t *testing.T) {
	const path = "../../../build/release-unstripped.tbn"
	buf, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("release tbn not built (%v); run `make release-unstripped-tbn` (a missing build artifact must fail, not skip — i253)", err)
	}
	f, err := format.ReadFile(buf)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	flat, err := EmitFile(f)
	if err != nil {
		t.Fatalf("EmitFile: %v", err)
	}
	lines, err := EmitLines(f)
	if err != nil {
		t.Fatalf("EmitLines: %v", err)
	}
	if got, want := JoinLines(lines), string(flat); got != want {
		t.Fatalf("EmitLines reassembled != EmitFile on real release (%d vs %d bytes)", len(got), len(want))
	}
	t.Logf("release: %d records → %d rendered lines", len(f.Records), len(lines))
}

func mnemID(t *testing.T, name string) uint16 {
	t.Helper()
	id, ok := format.MnemonicID(name)
	if !ok {
		t.Fatalf("no mnemonic id for %q", name)
	}
	return id
}
