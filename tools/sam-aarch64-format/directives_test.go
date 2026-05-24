package format

import "testing"

func TestDirectiveTableLookup(t *testing.T) {
	id, ok := DirectiveID(".byte")
	if !ok {
		t.Fatalf("DirectiveID(\".byte\") not found")
	}
	if DirectiveName(id) != ".byte" {
		t.Errorf("round-trip failed: %d -> %q", id, DirectiveName(id))
	}
}

func TestDirectiveExpectedSet(t *testing.T) {
	want := []string{
		".text", ".data",
		".byte", ".short", ".word", ".quad",
		".ascii", ".asciz",
		".equ", ".set",
		".global",
		".balign",
		".org",
		".skip", ".space",
		".inst",
	}
	for _, n := range want {
		if _, ok := DirectiveID(n); !ok {
			t.Errorf("expected directive %q missing from table", n)
		}
	}
}
