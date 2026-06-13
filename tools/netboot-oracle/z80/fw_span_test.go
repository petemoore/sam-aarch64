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
		t.Skipf("fw_span binary not built (%s); run `make netboot-fw-span`", fwSpanBinPath)
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
	mac := loadFWSpan(t)
	for _, tc := range cases {
		// The Go authority's positive-length records (a 0-length record is a
		// caller-policy degenerate the streaming loop does not emit).
		var want []int
		for _, r := range bdos.SpanPlan("firmware.x", tc.size, tc.cap) {
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

// TestFWSpanRecordName: fw_span_record_name(name, index) builds the same
// <prefix><NNN> record name as bdos.SpanRecordName, across short/exact/long names
// and the indices a real spanned firmware blob reaches.
func TestFWSpanRecordName(t *testing.T) {
	names := []string{"abc", "abcdefg", "start.elf", "start4.elf", "fixup.dat", "kernel8.img"}
	indices := []int{0, 1, 2, 9, 10, 45, 99, 100, 137, 999}

	mac := loadFWSpan(t)
	inAddr, err := mac.Sym("FW_SPAN_IN")
	if err != nil {
		t.Fatalf("%v", err)
	}
	for _, name := range names {
		for _, idx := range indices {
			// Write the logical name (NUL-terminated) into the input buffer.
			mac.Write(inAddr, append([]byte(name), 0))
			if _, err := mac.CallEntry("fw_span_record_name", z80h.Entry{HL: inAddr, BC: uint16(idx)}); err != nil {
				t.Fatalf("fw_span_record_name(%q,%d): %v", name, idx, err)
			}
			raw := mac.Read(mustSym(t, mac, "FW_SPAN_NAME"), 16)
			n := bytes.IndexByte(raw, 0)
			if n < 0 {
				t.Fatalf("fw_span_record_name(%q,%d): result not NUL-terminated", name, idx)
			}
			got := string(raw[:n])
			want := bdos.SpanRecordName(name, idx)
			if got != want {
				t.Errorf("fw_span_record_name(%q,%d) = %q, want %q", name, idx, got, want)
			}
			if len(got) > bdos.NameLen {
				t.Errorf("fw_span_record_name(%q,%d) = %q exceeds the %d-char B-DOS name field", name, idx, got, bdos.NameLen)
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
