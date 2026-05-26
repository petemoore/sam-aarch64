package translate

import (
	"bytes"
	"testing"

	emit "github.com/petemoore/sam-aarch64/tools/bin2text/emit"
)

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
		bin1, err := Translate([]byte(src), "test.s")
		if err != nil {
			t.Errorf("first Translate of %q: %v", src, err)
			continue
		}
		canon, err := emit.Emit(bin1)
		if err != nil {
			t.Errorf("Emit %q: %v", src, err)
			continue
		}
		bin2, err := Translate(canon, "test.s")
		if err != nil {
			t.Errorf("second Translate %q: %v", src, err)
			continue
		}
		if !bytes.Equal(bin1, bin2) {
			t.Errorf("idempotency failed for %q:\n bin1 = % X\n bin2 = % X\n canon = %q",
				src, bin1, bin2, string(canon))
		}
	}
}
