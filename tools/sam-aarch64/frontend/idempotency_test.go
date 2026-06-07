package frontend

import (
	"reflect"
	"testing"

	emit "github.com/petemoore/sam-aarch64/tools/sam-aarch64/render"
)

// TestIdempotency confirms the text → IR → text round-trip is stable: parsing
// a source to the in-memory IR, rendering it back to canonical text, and
// re-parsing yields an identical IR (records + name table). Translate now
// returns the in-memory *format.File directly, so the comparison is over the
// decoded records rather than serialized bytes.
func TestIdempotency(t *testing.T) {
	sources := []string{
		"  nop\n",
		"main:\n  add x0, x1, #4\n",
		"  ldr x0, [x1, #8]\n",
		"  add x0, x1, x2, lsl #4\n",
		"  b.lt 1f\n1:\n",
		"  add x0, x1, :lo12:msg\n",
		".byte 1, 2, 3\n",
		".ascii \"hi\"\n",
		"  ldr x0, =0x30d0088a\n",
		"  ldr w2, =0xdeadbeef\n",
		"  ldr x1, =msg\nmsg:\n",
		"  ldr x2, =10f\n10:\n",
		".section bss_kernel, \"aw\", %nobits\n",
		".section text_tests, \"ax\"\n",
		".section .rodata\n",
		".arch armv8-a\n",
		".cpu cortex-a53\n",
	}
	for _, src := range sources {
		f1, err := Translate([]byte(src), "test.s")
		if err != nil {
			t.Errorf("first Translate of %q: %v", src, err)
			continue
		}
		canon, err := emit.EmitFile(f1)
		if err != nil {
			t.Errorf("Emit %q: %v", src, err)
			continue
		}
		f2, err := Translate(canon, "test.s")
		if err != nil {
			t.Errorf("second Translate %q: %v", src, err)
			continue
		}
		if !reflect.DeepEqual(f1.Records, f2.Records) {
			t.Errorf("idempotency failed for %q:\n recs1 = %+v\n recs2 = %+v\n canon = %q",
				src, f1.Records, f2.Records, string(canon))
		}
		if !reflect.DeepEqual(f1.Names, f2.Names) {
			t.Errorf("idempotency failed (names) for %q:\n names1 = %v\n names2 = %v",
				src, f1.Names, f2.Names)
		}
	}
}
