package assemble_test

import (
	"encoding/binary"
	"testing"

	assemble "github.com/petemoore/sam-aarch64/tools/sam-aarch64/assemble"
	frontend "github.com/petemoore/sam-aarch64/tools/sam-aarch64/frontend"
)

// assembleErrata assembles src (the symbolic record IR lives only in memory)
// and applies the given Cortex-A53 workarounds, returning the binary.
func assembleErrata(t *testing.T, src string, opts assemble.ErrataOptions) []byte {
	t.Helper()
	f, err := frontend.TranslateWithOptions([]byte(src), "test.s", frontend.PreprocessOptions{})
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	p1, err := assemble.Pass1(f)
	if err != nil {
		t.Fatalf("pass1: %v", err)
	}
	out, err := assemble.Pass2WithErrata(f, p1, opts)
	if err != nil {
		t.Fatalf("pass2+errata: %v", err)
	}
	return out
}

func word(b []byte, off int) uint32 { return binary.LittleEndian.Uint32(b[off:]) }

const nopWord = uint32(0xd503201f)

func TestErrata835769EndToEnd(t *testing.T) {
	// A memory op immediately followed by a 64-bit MAC with no shared
	// register: a hazard. The fix inserts a NOP between them.
	const haz = "ldr x4, [x5]\nmadd x0, x1, x2, x3\n"

	off := assembleErrata(t, haz, assemble.ErrataOptions{})
	if len(off) != 8 {
		t.Fatalf("unfixed length = %d, want 8", len(off))
	}
	on := assembleErrata(t, haz, assemble.ErrataOptions{Fix835769: true})
	if len(on) != 12 {
		t.Fatalf("fixed length = %d, want 12 (a NOP inserted)", len(on))
	}
	if word(on, 0) != word(off, 0) {
		t.Errorf("first instruction changed: 0x%08x → 0x%08x", word(off, 0), word(on, 0))
	}
	if word(on, 4) != nopWord {
		t.Errorf("word[1] = 0x%08x, want NOP 0x%08x", word(on, 4), nopWord)
	}
	if word(on, 8) != word(off, 4) {
		t.Errorf("MAC moved/changed: 0x%08x → 0x%08x", word(off, 4), word(on, 8))
	}
}

func TestErrata835769RawDepNotFixed(t *testing.T) {
	// madd reads x4 — the load's destination — as its accumulator: a true
	// RAW dependency, which serialises in the pipeline. No fix.
	const safe = "ldr x4, [x5]\nmadd x0, x1, x2, x4\n"
	on := assembleErrata(t, safe, assemble.ErrataOptions{Fix835769: true})
	if len(on) != 8 {
		t.Fatalf("RAW-dependency case wrongly fixed: length = %d, want 8", len(on))
	}
}

func TestErrata835769SeparatedNotFixed(t *testing.T) {
	// A non-memory instruction between the load and the MAC breaks the
	// adjacency: no fix.
	const sep = "ldr x4, [x5]\nadd x6, x7, x8\nmadd x0, x1, x2, x3\n"
	on := assembleErrata(t, sep, assemble.ErrataOptions{Fix835769: true})
	if len(on) != 12 {
		t.Fatalf("non-adjacent case wrongly fixed: length = %d, want 12", len(on))
	}
}

func TestErrata843419EndToEnd(t *testing.T) {
	// .org places the ADRP at page offset 0xff8 (an affected slot); the
	// following ld/st sequence completes the erratum signature, so the
	// ADRP is rewritten to an ADR in place (no length change).
	const src = ".org 0xff8\nadrp x0, lbl\nldr x4, [x5]\nldr x9, [x0]\nlbl:\n"

	off := assembleErrata(t, src, assemble.ErrataOptions{})
	if w := word(off, 0); w&0x9f000000 != 0x90000000 {
		t.Fatalf("unfixed word[0] = 0x%08x, expected an ADRP", w)
	}
	on := assembleErrata(t, src, assemble.ErrataOptions{Fix843419: true})
	if len(on) != len(off) {
		t.Fatalf("843419 fix changed image length %d → %d (should be in place)", len(off), len(on))
	}
	w := word(on, 0)
	if w&0x9f000000 != 0x10000000 {
		t.Errorf("fixed word[0] = 0x%08x, expected an ADR (op bits 0x10000000)", w)
	}
	if w&0x1f != 0 {
		t.Errorf("rewritten ADR Rd = %d, want 0 (x0)", w&0x1f)
	}
}

func TestErrata843419WrongOffsetNotFixed(t *testing.T) {
	// The same sequence, but the ADRP sits at a non-affected page offset
	// (0xff0): no fix.
	const src = ".org 0xff0\nadrp x0, lbl\nldr x4, [x5]\nldr x9, [x0]\nlbl:\n"
	on := assembleErrata(t, src, assemble.ErrataOptions{Fix843419: true})
	if w := word(on, 0); w&0x9f000000 != 0x90000000 {
		t.Errorf("ADRP at safe offset wrongly rewritten: word[0] = 0x%08x", w)
	}
}
