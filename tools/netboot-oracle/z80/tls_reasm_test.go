// tls_reasm_test.go — the i88 6b host-verification of the TLS-record reassembler
// (src/netboot/tls_reasm.asm). It assembles the standalone module under the
// koron-go/z80 harness, feeds tls_reasm_feed a sequence of byte chunks deliberately
// mis-aligned to record boundaries (header split, body split, several records
// coalesced, byte-at-a-time, empty chunks), and asserts the emitted records +
// per-record boundaries recorded by the reasm_record_leaf test double match the Go
// authority tls.RecordReassembler fed the identical chunks — the same "drive both
// with one input, compare byte-for-byte" pattern as the other netboot leaves.
package z80_test

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/tls"
	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	tlsReasmBinPath = "../../../build/netboot_tls_reasm.bin"
	tlsReasmMapPath = "../../../build/netboot_tls_reasm.map"
)

func loadTLSReasm(t *testing.T) *z80h.Machine {
	t.Helper()
	if _, err := os.Stat(tlsReasmBinPath); err != nil {
		t.Fatalf("tls_reasm binary not built (%s); run `make netboot-tls-reasm`", tlsReasmBinPath)
	}
	mac, err := z80h.Load(tlsReasmBinPath, tlsReasmMapPath)
	if err != nil {
		t.Fatalf("load tls_reasm: %v", err)
	}
	return mac
}

// runZ80Reasm feeds chunks through the Z80 tls_reasm_feed on a fresh machine and
// returns the emitted records (the concatenated REASM_OUT split by the per-record
// lengths the reasm_record_leaf double recorded into REASM_RECS).
func runZ80Reasm(t *testing.T, chunks [][]byte) [][]byte {
	t.Helper()
	mac := loadTLSReasm(t)
	in, err := mac.Sym("REASM_IN")
	if err != nil {
		t.Fatalf("%v", err)
	}
	for i, ch := range chunks {
		if len(ch) > 4096 {
			t.Fatalf("chunk %d is %d bytes, exceeds REASM_IN (4096)", i, len(ch))
		}
		if len(ch) > 0 {
			mac.Write(in, ch)
		}
		if _, err := mac.CallEntry("tls_reasm_feed", z80h.Entry{HL: in, BC: uint16(len(ch))}); err != nil {
			t.Fatalf("tls_reasm_feed(chunk %d): %v", i, err)
		}
	}

	out := mac.Read(mustSym(t, mac, "REASM_OUT"), int(readU16(t, mac, "REASM_OUT_LEN")))
	count := readU16(t, mac, "REASM_REC_COUNT")
	raw := mac.Read(mustSym(t, mac, "REASM_RECS"), int(count)*2)
	recs := make([][]byte, count)
	off := 0
	for i := 0; i < int(count); i++ {
		n := int(binary.LittleEndian.Uint16(raw[i*2:]))
		recs[i] = out[off : off+n]
		off += n
	}
	return recs
}

// goReasm runs the Go authority over the same chunks.
func goReasm(chunks [][]byte) [][]byte {
	var r tls.RecordReassembler
	var out [][]byte
	for _, c := range chunks {
		out = append(out, r.Feed(c)...)
	}
	return out
}

// tlsSynthRecord builds a synthetic TLS record: type, legacy version 0x0303,
// 2-byte big-endian length, then n ramp body bytes.
func tlsSynthRecord(typ byte, n int) []byte {
	r := []byte{typ, 0x03, 0x03, byte(n >> 8), byte(n)}
	for i := 0; i < n; i++ {
		r = append(r, byte(i))
	}
	return r
}

func chunkBySize(b []byte, size int) [][]byte {
	var out [][]byte
	for i := 0; i < len(b); i += size {
		j := i + size
		if j > len(b) {
			j = len(b)
		}
		out = append(out, b[i:j])
	}
	return out
}

// TestTLSReassembler: the Z80 tls_reasm_feed reframes the same records as the Go
// RecordReassembler — byte-for-byte, with identical per-record boundaries — across
// chunk sequences deliberately mis-aligned to record boundaries. Both must also
// reconstruct the original records exactly.
func TestTLSReassembler(t *testing.T) {
	records := [][]byte{
		tlsSynthRecord(0x16, 4),  // handshake, short
		tlsSynthRecord(0x14, 1),  // 1 body byte
		tlsSynthRecord(0x17, 10), // application_data
		tlsSynthRecord(0x16, 0),  // zero-length payload (header only)
	}
	var stream []byte
	for _, r := range records {
		stream = append(stream, r...)
	}
	// records[0] is 9 bytes (5+4); used for the boundary-exact case.

	cases := []struct {
		name   string
		chunks [][]byte
	}{
		// All four records coalesced into one chunk.
		{"coalesced_one_chunk", [][]byte{stream}},
		// Byte-at-a-time: splits every header and body.
		{"byte_at_a_time", chunkBySize(stream, 1)},
		// The 5-byte header of record 0 split across two chunks (3 + rest).
		{"header_split", [][]byte{stream[:3], stream[3:]}},
		// Misaligned fixed 2-byte chunks.
		{"two_byte_chunks", chunkBySize(stream, 2)},
		// A chunk ending exactly on record 0's boundary, then the rest.
		{"boundary_exact", [][]byte{stream[:9], stream[9:]}},
		// Record 0 whole + the first 2 bytes of record 1 in chunk 1 (partial tail
		// stays buffered), the remainder in chunk 2.
		{"trailing_partial", [][]byte{stream[:11], stream[11:]}},
		// Empty chunks interspersed are no-ops on both sides.
		{"empty_interspersed", [][]byte{{}, stream[:5], {}, stream[5:], {}}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := runZ80Reasm(t, tc.chunks)
			want := goReasm(tc.chunks)

			if len(got) != len(want) {
				t.Fatalf("emitted %d records, Go authority emitted %d", len(got), len(want))
			}
			for i := range want {
				if !bytes.Equal(got[i], want[i]) {
					t.Errorf("record %d: Z80 = %x, Go = %x", i, got[i], want[i])
				}
			}
			// Both must reconstruct the original record sequence exactly.
			if fmt.Sprint(recLens(got)) != fmt.Sprint(recLens(records)) {
				t.Errorf("record boundaries = %v, want %v", recLens(got), recLens(records))
			}
			for i := range records {
				if i < len(got) && !bytes.Equal(got[i], records[i]) {
					t.Errorf("record %d not reconstructed: got %x, want %x", i, got[i], records[i])
				}
			}
		})
	}
}

func recLens(recs [][]byte) []int {
	out := make([]int, len(recs))
	for i, r := range recs {
		out[i] = len(r)
	}
	return out
}
