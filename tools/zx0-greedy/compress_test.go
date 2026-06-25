package zx0greedy

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// decompressC implements the ZX0 C decompressor logic (non-classic, forward mode)
// for testing purposes.  Mirrors dzx0.c decompress() with classic_mode=false.
func decompressC(compressed []byte) ([]byte, error) {
	bitMask := 0
	bitValue := 0
	backtrack := false
	lastByte := 0
	pos := 0
	lastOffset := 1

	readByte := func() int {
		if pos >= len(compressed) {
			return -1
		}
		lastByte = int(compressed[pos])
		pos++
		return lastByte
	}
	readBit := func() int {
		if backtrack {
			backtrack = false
			return lastByte & 1
		}
		bitMask >>= 1
		if bitMask == 0 {
			bitMask = 128
			bitValue = readByte()
			if bitValue < 0 {
				return 0
			}
		}
		if bitValue&bitMask != 0 {
			return 1
		}
		return 0
	}
	readGamma := func(inverted bool) int {
		value := 1
		for readBit() == 0 {
			bit := readBit()
			if inverted {
				bit ^= 1
			}
			value = (value << 1) | bit
		}
		return value
	}

	var out []byte
	for {
		// COPY_LITERALS
		length := readGamma(false)
		for i := 0; i < length; i++ {
			b := readByte()
			if b < 0 {
				return out, nil
			}
			out = append(out, byte(b))
		}
		if readBit() == 1 {
			goto newOffset
		}
		// COPY_FROM_LAST_OFFSET
		{
			length := readGamma(false)
			for i := 0; i < length; i++ {
				idx := len(out) - lastOffset
				if idx < 0 {
					out = append(out, 0)
				} else {
					out = append(out, out[idx])
				}
			}
			if readBit() == 0 {
				continue // back to COPY_LITERALS
			}
		}
	newOffset:
		msb := readGamma(true) // inverted for non-classic
		if msb == 256 {
			break // end marker
		}
		lsbByte := readByte()
		if lsbByte < 0 {
			break
		}
		lastOffset = msb*128 - (lsbByte >> 1)
		backtrack = true
		length2 := readGamma(false) + 1
		for i := 0; i < length2; i++ {
			idx := len(out) - lastOffset
			if idx < 0 {
				out = append(out, 0)
			} else {
				out = append(out, out[idx])
			}
		}
		if readBit() == 1 {
			goto newOffset
		}
	}
	return out, nil
}

// TestCompressRoundTrip compresses a variety of inputs and verifies that
// the Go-implemented C decompressor reconstructs the original.
func TestCompressRoundTrip(t *testing.T) {
	p := DefaultParams
	tests := []struct {
		name string
		src  []byte
	}{
		{"single literal", []byte("A")},
		{"short string", []byte("Hello, World!")},
		{"repeat bytes", bytes.Repeat([]byte("A"), 100)},
		{"repeat phrase", bytes.Repeat([]byte("hello "), 50)},
		{"mix", []byte("ABCDEFABCDEFABCDEFXYZ")},
		{"rep from offset 1", []byte("AAAAAAAAAAAAAAAAAAAAA")},
		{"two literal then rep then lit", []byte("ABABABABABABABABABAB")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			compressed := Compress(tc.src, p)
			got, err := decompressC(compressed)
			if err != nil {
				t.Fatalf("decompress error: %v", err)
			}
			if !bytes.Equal(got, tc.src) {
				t.Errorf("mismatch: got %q want %q", got, tc.src)
			}
		})
	}
}

// TestCompressCorpusBlock tests a real corpus block from the zx0-blocks dir,
// if available.
func TestCompressCorpusBlock(t *testing.T) {
	// Walk up to find repo root.
	dir, _ := os.Getwd()
	root := dir
	for {
		if _, err := os.Stat(filepath.Join(root, "Makefile")); err == nil {
			break
		}
		parent := filepath.Dir(root)
		if parent == root {
			t.Fatal("cannot find repo root (no Makefile found walking up) — broken test setup")
		}
		root = parent
	}

	rawPath := filepath.Join(root, "build", "zx0-blocks", "block_0002kb_0074.raw")
	raw, err := os.ReadFile(rawPath)
	if err != nil {
		t.Fatalf("corpus block not built at %s (run 'make zx0-blocks'): %v", rawPath, err)
	}

	p := DefaultParams
	compressed := Compress(raw, p)
	got, err := decompressC(compressed)
	if err != nil {
		t.Fatalf("decompress error: %v", err)
	}
	if !bytes.Equal(got, raw) {
		// Find first mismatch position.
		for i := range raw {
			if i >= len(got) || got[i] != raw[i] {
				t.Errorf("first mismatch at byte %d: got %02x want %02x", i, got[i], raw[i])
				break
			}
		}
		t.Errorf("total: got %d B want %d B", len(got), len(raw))
	} else {
		t.Logf("corpus block OK: %d raw → %d compressed", len(raw), len(compressed))
	}
}
