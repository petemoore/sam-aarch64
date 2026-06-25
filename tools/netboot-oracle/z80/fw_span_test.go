// fw_span_test.go — the i99/q16 host-verification of the firmware-spanning
// primitives (src/netboot/fw_span.asm). It assembles the standalone module under
// the koron-go/z80 harness, drives the streaming chunk-length loop + the record
// naming, and asserts the per-record length sequence + the names match the Go
// authority bdos.SpanPlan / bdos.SpanRecordName byte-for-byte.
package z80_test

import (
	"bytes"
	"encoding/binary"
	"os"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/bdos"
	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	fwSpanBinPath = "../../../build/netboot_fw_span.bin"
	fwSpanMapPath = "../../../build/netboot_fw_span.map"
)

func loadFWSpan(t *testing.T) *z80h.Machine {
	t.Helper()
	if _, err := os.Stat(fwSpanBinPath); err != nil {
		t.Fatalf("fw_span binary not built (%s); run `make netboot-fw-span`", fwSpanBinPath)
	}
	mac, err := z80h.Load(fwSpanBinPath, fwSpanMapPath)
	if err != nil {
		t.Fatalf("load fw_span: %v", err)
	}
	return mac
}

func writeU32LE(t *testing.T, mac *z80h.Machine, name string, v uint32) {
	t.Helper()
	a, err := mac.Sym(name)
	if err != nil {
		t.Fatalf("%v", err)
	}
	mac.Write(a, []byte{byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24)})
}

func readU32LE(t *testing.T, mac *z80h.Machine, name string) uint32 {
	t.Helper()
	a, err := mac.Sym(name)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return binary.LittleEndian.Uint32(mac.Read(a, 4))
}

// TestFWSpanChunkLen: driving fw_span_chunk_len in the streaming loop (remaining =
// size; each pass len = min(cap, remaining); remaining -= len) reproduces exactly
// the per-record byte lengths bdos.SpanPlan assigns — across non-spanned, spanned,
// exact-multiple, and the real firmware sizes.
func TestFWSpanChunkLen(t *testing.T) {
	cases := []struct {
		size, cap int
	}{
		{1, 1000},        // non-spanned: one chunk = size
		{1000, 1000},     // exactly cap: one chunk
		{1001, 1000},     // one over: 1000 + 1
		{2000, 1000},     // exact multiple: 1000 + 1000
		{2001, 1000},     // 1000 + 1000 + 1
		{52476, 16384},   // bootcode.bin at a 16 KB cap
		{2979296, 65536}, // start.elf at a 64 KB cap (many records)
		{2255072, 500000},
		{7274, 4096},
	}
	// A fixed hash; only lengths are checked here (the hash does not affect lengths).
	var h [32]byte
	h[0], h[1], h[2] = 0xAB, 0xCD, 0xEF

	mac := loadFWSpan(t)
	for _, tc := range cases {
		// The Go authority's positive-length records (a 0-length record is a
		// caller-policy degenerate the streaming loop does not emit).
		var want []int
		for _, r := range bdos.SpanPlan(h, tc.size, tc.cap) {
			if r.Length > 0 {
				want = append(want, r.Length)
			}
		}

		var got []int
		remaining := uint32(tc.size)
		for i := 0; remaining > 0; i++ {
			if i > len(want)+1 {
				t.Fatalf("size=%d cap=%d: chunk loop did not terminate", tc.size, tc.cap)
			}
			writeU32LE(t, mac, "FW_SPAN_REMAINING", remaining)
			writeU32LE(t, mac, "FW_SPAN_CAP", uint32(tc.cap))
			if _, err := mac.Call("fw_span_chunk_len"); err != nil {
				t.Fatalf("fw_span_chunk_len: %v", err)
			}
			l := readU32LE(t, mac, "FW_SPAN_LEN")
			if l == 0 {
				t.Fatalf("size=%d cap=%d: chunk_len returned 0 with %d remaining", tc.size, tc.cap, remaining)
			}
			got = append(got, int(l))
			remaining -= l
		}

		if len(got) != len(want) {
			t.Errorf("size=%d cap=%d: %d chunks, want %d", tc.size, tc.cap, len(got), len(want))
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("size=%d cap=%d: chunk %d len = %d, want %d", tc.size, tc.cap, i, got[i], want[i])
			}
		}
	}
}

// TestFWSpanRecordName: fw_span_record_name(hashPtr, index) builds the same
// content-addressed <hash6><NNN> record name as bdos.SpanRecordName, across a
// variety of hash prefixes and the full range of record indices.
func TestFWSpanRecordName(t *testing.T) {
	// Representative 32-byte hashes covering varied nibble patterns.
	hashes := [][32]byte{
		func() [32]byte { var h [32]byte; h[0], h[1], h[2] = 0xAB, 0xCD, 0xEF; return h }(),
		func() [32]byte { var h [32]byte; h[0], h[1], h[2] = 0x00, 0x00, 0x00; return h }(),
		func() [32]byte { var h [32]byte; h[0], h[1], h[2] = 0xFF, 0xFF, 0xFF; return h }(),
		func() [32]byte { var h [32]byte; h[0], h[1], h[2] = 0x01, 0x23, 0x45; return h }(),
		func() [32]byte { var h [32]byte; h[0], h[1], h[2] = 0xDE, 0xAD, 0xBE; return h }(),
		func() [32]byte { var h [32]byte; h[0], h[1], h[2] = 0x0A, 0x0B, 0x0C; return h }(),
	}
	indices := []int{0, 1, 2, 9, 10, 45, 99, 100, 137, 999}

	mac := loadFWSpan(t)
	hashAddr, err := mac.Sym("FW_SPAN_HASH")
	if err != nil {
		t.Fatalf("%v", err)
	}
	for _, h := range hashes {
		// Write the full 32-byte hash into FW_SPAN_HASH.
		mac.Write(hashAddr, h[:])
		for _, idx := range indices {
			if _, err := mac.CallEntry("fw_span_record_name", z80h.Entry{HL: hashAddr, BC: uint16(idx)}); err != nil {
				t.Fatalf("fw_span_record_name(hash=[%02x%02x%02x...], %d): %v", h[0], h[1], h[2], idx, err)
			}
			raw := mac.Read(mustSym(t, mac, "FW_SPAN_NAME"), 16)
			n := bytes.IndexByte(raw, 0)
			if n < 0 {
				t.Fatalf("fw_span_record_name(hash=[%02x%02x%02x...], %d): result not NUL-terminated", h[0], h[1], h[2], idx)
			}
			got := string(raw[:n])
			want := bdos.SpanRecordName(h, idx)
			if got != want {
				t.Errorf("fw_span_record_name([%02x%02x%02x...], %d) = %q, want %q", h[0], h[1], h[2], idx, got, want)
			}
			if len(got) > bdos.NameLen {
				t.Errorf("fw_span_record_name([%02x%02x%02x...], %d) = %q exceeds the %d-char B-DOS name field", h[0], h[1], h[2], idx, got, bdos.NameLen)
			}
		}
	}
}

func mustSym(t *testing.T, mac *z80h.Machine, name string) uint16 {
	t.Helper()
	a, err := mac.Sym(name)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return a
}
