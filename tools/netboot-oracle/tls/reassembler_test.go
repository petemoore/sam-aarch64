package tls

import (
	"bytes"
	"testing"
)

// synthRecord builds a synthetic TLS record: 1-byte type, 2-byte legacy version
// (0x0303), 2-byte big-endian length, then n body bytes (a deterministic ramp).
func synthRecord(typ byte, n int) []byte {
	r := []byte{typ, 0x03, 0x03, byte(n >> 8), byte(n)}
	for i := 0; i < n; i++ {
		r = append(r, byte(i))
	}
	return r
}

// fixedChunks splits b into consecutive chunks of at most `size` bytes.
func fixedChunks(b []byte, size int) [][]byte {
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

func feedAll(r *RecordReassembler, chunks [][]byte) [][]byte {
	var out [][]byte
	for _, c := range chunks {
		out = append(out, r.Feed(c)...)
	}
	return out
}

// TestRecordReassembler verifies the authority reconstructs the original record
// sequence regardless of how the byte stream is split into chunks — header split
// across chunks, body split, several records coalesced, byte-at-a-time — and that
// a trailing partial record stays buffered (not emitted) until completed.
func TestRecordReassembler(t *testing.T) {
	records := [][]byte{
		synthRecord(0x16, 4),  // handshake, short
		synthRecord(0x14, 1),  // change_cipher_spec-ish, 1 body byte
		synthRecord(0x17, 10), // application_data
		synthRecord(0x16, 0),  // zero-length payload (header only)
		synthRecord(0x17, 300), // spans many small chunks
	}
	var stream []byte
	for _, r := range records {
		stream = append(stream, r...)
	}

	// Every chunk size from 1 (byte-at-a-time, splits every header) up through the
	// whole stream (all records coalesced into one chunk) must reframe identically.
	for size := 1; size <= len(stream)+1; size++ {
		var r RecordReassembler
		got := feedAll(&r, fixedChunks(stream, size))
		if len(got) != len(records) {
			t.Fatalf("chunk size %d: got %d records, want %d", size, len(got), len(records))
		}
		for i := range records {
			if !bytes.Equal(got[i], records[i]) {
				t.Errorf("chunk size %d: record %d = %x, want %x", size, i, got[i], records[i])
			}
		}
	}

	// A trailing partial record must NOT be emitted until completed.
	var r RecordReassembler
	full := synthRecord(0x17, 8)
	if out := r.Feed(full[:6]); len(out) != 0 { // header + 1 body byte only
		t.Fatalf("partial record emitted early: %d records", len(out))
	}
	out := r.Feed(full[6:])
	if len(out) != 1 || !bytes.Equal(out[0], full) {
		t.Fatalf("completing the record: got %d records (%x), want 1 (%x)", len(out), out, full)
	}
}
