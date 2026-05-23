package translate

import (
	"strings"
	"testing"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

func TestIntegrationAllConstructs(t *testing.T) {
	src := strings.Join([]string{
		"// banner",
		".text",
		".global main",
		"main:",
		"    mov x0, #0",
		"    add x0, x0, #1 // inline",
		"    cmp x0, #10",
		"    b.lt 1f",
		"    ret",
		"1:",
		"    b 1b",
		".data",
		"msg:",
		"    .ascii \"hi\\n\"",
		"",
	}, "\n") + "\n"

	out, err := Translate([]byte(src), "x.s")
	if err != nil {
		t.Fatal(err)
	}
	f, err := format.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if f.Version != 1 {
		t.Errorf("version = %d", f.Version)
	}
	found := map[string]bool{}
	for _, n := range f.Names {
		found[n] = true
	}
	for _, n := range []string{"main", "msg"} {
		if !found[n] {
			t.Errorf("expected name %q missing from %v", n, f.Names)
		}
	}
	counts := map[format.RecordKind]int{}
	rr := format.NewRecordReader(f.Records)
	for !rr.AtEnd() {
		rec, err := rr.Next()
		if err != nil {
			t.Fatal(err)
		}
		counts[rec.Kind]++
	}
	if counts[format.KindLabelDef] < 2 {
		t.Errorf("LABEL_DEF count = %d, want ≥ 2", counts[format.KindLabelDef])
	}
	if counts[format.KindLocalDef] < 1 {
		t.Errorf("LOCAL_DEF count = %d, want ≥ 1", counts[format.KindLocalDef])
	}
	if counts[format.KindInst] < 6 {
		t.Errorf("INST count = %d, want ≥ 6", counts[format.KindInst])
	}
	if counts[format.KindDirective] < 3 {
		t.Errorf("DIRECTIVE count = %d, want ≥ 3", counts[format.KindDirective])
	}
	if counts[format.KindComment] < 1 {
		t.Errorf("COMMENT count = %d, want ≥ 1", counts[format.KindComment])
	}
}
