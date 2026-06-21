// splice_test.go — i229 unit test for the chunk-1 splice, against a SYNTHETIC 1 KB
// bootblock (no proprietary capture needed): a CALL &805F at &9E, a final RET at
// &15D, zeros in the free space, and a recognisable `restore:`/screen-tail pattern
// at &A1 onward. It asserts the 3 patched bytes, the spliced inject region, and that
// every byte OUTSIDE those two regions is left byte-identical (the restore:/screen
// tail must survive untouched — the q50 invariant).
package samboot

import (
	"bytes"
	"testing"
)

// syntheticChunk1 builds a 1 KB bootblock stand-in with the real splice-site bytes
// and a marker tail, so the test exercises Splice without Colin's capture.
func syntheticChunk1() []byte {
	c := make([]byte, ChunkSize)
	// fill with a recognisable non-zero pattern up to the free space, so an
	// accidental overwrite of the tail is detectable (zeros would hide it).
	for i := 0; i < InjectFileOffset; i++ {
		c[i] = byte(0x40 + (i % 0x30))
	}
	// the splice site: CALL &805F (CD 5F 80) at &9E.
	c[SpliceOffset] = 0xCD
	c[SpliceOffset+1] = 0x5F
	c[SpliceOffset+2] = 0x80
	// restore: at &A1 — the verbatim tail that MUST survive the splice.
	copy(c[0xA1:], []byte{0x3E, 0x00, 0xD3, 0xFA, 0x3E, 0x00, 0xD3, 0xFB})
	// final RET at &15D.
	c[0x15D] = 0xC9
	// &15E..&3FF stay zero (the free space).
	return c
}

func TestSpliceSynthetic(t *testing.T) {
	orig := syntheticChunk1()
	inject := []byte{0xCD, 0x5F, 0x80, 0xCD, 0x6C, 0x41, 0xD0, 0x7D, 0x32, 0x53, 0xE4, 0xC3, 0x9E, 0x42}

	out, err := Splice(orig, inject)
	if err != nil {
		t.Fatalf("Splice: %v", err)
	}
	if len(out) != ChunkSize {
		t.Fatalf("Splice returned %d bytes, want %d", len(out), ChunkSize)
	}

	// 1. The 3 patched bytes: CALL inject (CD 5E 41 = CALL &415E).
	wantPatch := []byte{0xCD, byte(InjectLogical & 0xFF), byte(InjectLogical >> 8)}
	if got := out[SpliceOffset : SpliceOffset+3]; !bytes.Equal(got, wantPatch) {
		t.Errorf("splice site = % X, want % X (CALL inject @&415E)", got, wantPatch)
	}

	// 2. The inject blob landed verbatim at the free space.
	if got := out[InjectFileOffset : InjectFileOffset+len(inject)]; !bytes.Equal(got, inject) {
		t.Errorf("inject region = % X, want % X", got, inject)
	}

	// 3. Everything OUTSIDE the patch (3 bytes @ &9E) and the inject region
	//    (&15E..&15E+len) is byte-identical to the original — the restore:/screen
	//    tail and the whole bootblock body survive untouched.
	for i := 0; i < ChunkSize; i++ {
		inPatch := i >= SpliceOffset && i < SpliceOffset+3
		inInject := i >= InjectFileOffset && i < InjectFileOffset+len(inject)
		if inPatch || inInject {
			continue
		}
		if out[i] != orig[i] {
			t.Fatalf("byte &%04X changed: got %02X, want %02X (only the 3-byte CALL and the free space may change)", i, out[i], orig[i])
		}
	}

	// Specifically assert the restore: tail at &A1 is verbatim.
	if got := out[0xA1 : 0xA1+8]; !bytes.Equal(got, orig[0xA1:0xA1+8]) {
		t.Errorf("restore: tail @&A1 = % X, want % X (must be byte-identical)", got, orig[0xA1:0xA1+8])
	}
}

func TestSpliceRejectsWrongSize(t *testing.T) {
	if _, err := Splice(make([]byte, 512), []byte{0}); err == nil {
		t.Error("Splice accepted a 512-byte chunk1, want an error (must be exactly 1024)")
	}
}

func TestSpliceRejectsOversizeInject(t *testing.T) {
	if _, err := Splice(syntheticChunk1(), make([]byte, FreeSpace+1)); err == nil {
		t.Errorf("Splice accepted a %d-byte inject, want an error (max %d free bytes)", FreeSpace+1, FreeSpace)
	}
}

func TestSpliceRejectsWrongSite(t *testing.T) {
	c := syntheticChunk1()
	c[SpliceOffset+1] = 0x00 // corrupt the CALL target so it is no longer CALL &805F
	if _, err := Splice(c, []byte{0xC9}); err == nil {
		t.Error("Splice accepted a changed splice site, want an error (refuse to guess on a changed capture)")
	}
}
