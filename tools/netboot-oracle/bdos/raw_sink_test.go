package bdos

import (
	"bytes"
	"testing"
)

// ref re-blocks the concatenation of chunks into SectorSize sectors the obvious
// way (independent of RawSink's incremental logic) so the tests check RawSink
// against a second implementation, not just against itself. finish=true appends a
// zero-padded final sector for a non-aligned tail.
func ref(chunks [][]byte, finish bool) []RawSectorWrite {
	var all []byte
	for _, c := range chunks {
		all = append(all, c...)
	}
	var out []RawSectorWrite
	for off := 0; off+SectorSize <= len(all); off += SectorSize {
		var w RawSectorWrite
		w.LinearSec = len(out)
		copy(w.Data[:], all[off:off+SectorSize])
		out = append(out, w)
	}
	if rem := len(all) % SectorSize; rem != 0 && finish {
		var w RawSectorWrite
		w.LinearSec = len(out)
		copy(w.Data[:], all[len(all)-rem:])
		out = append(out, w)
	}
	return out
}

func feed(chunks [][]byte, finish bool) *RawSink {
	s := NewRawSink()
	for _, c := range chunks {
		s.Write(c)
	}
	if finish {
		s.Finish()
	}
	return s
}

// seq returns n bytes whose values are a position-dependent pattern, so a
// misplaced or duplicated byte in the re-blocking shows up as a content mismatch.
func seq(start, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte((start + i) * 7)
	}
	return b
}

func TestRawSinkConstants(t *testing.T) {
	if SectorSize != 512 {
		t.Fatalf("SectorSize = %d, want 512", SectorSize)
	}
	if SectorsPerRecord != 1600 {
		t.Fatalf("SectorsPerRecord = %d, want 1600 (819200/512)", SectorsPerRecord)
	}
	if SectorsPerRecord*SectorSize != RecordSize {
		t.Fatalf("SectorsPerRecord*SectorSize = %d, want RecordSize %d", SectorsPerRecord*SectorSize, RecordSize)
	}
}

func TestRawSinkReblocking(t *testing.T) {
	cases := []struct {
		name   string
		chunks [][]byte
		finish bool
	}{
		{"empty stream", nil, true},
		{"one exact sector", [][]byte{seq(0, 512)}, true},
		{"one byte at a time, exactly one sector", oneByteEach(seq(0, 512)), true},
		{"sub-sector chunks completing a sector", [][]byte{seq(0, 200), seq(200, 200), seq(400, 112)}, true},
		{"a chunk straddling a boundary", [][]byte{seq(0, 500), seq(500, 100)}, true},
		{"one big chunk spanning many sectors", [][]byte{seq(0, 512*5)}, true},
		{"big chunk + non-aligned tail, finished", [][]byte{seq(0, 512*3+37)}, true},
		{"non-aligned tail, NOT finished (tail dropped)", [][]byte{seq(0, 512*2+9)}, false},
		{"empty chunks interspersed", [][]byte{seq(0, 100), {}, seq(100, 412), {}, seq(512, 10)}, true},
		{"irregular chunk sizes across several sectors", [][]byte{seq(0, 1), seq(1, 1023), seq(1024, 7), seq(1031, 600)}, true},
		{"full record (1600 sectors, sector-aligned)", [][]byte{seq(0, RecordSize)}, true},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			got := feed(c.chunks, c.finish).Writes()
			want := ref(c.chunks, c.finish)
			if len(got) != len(want) {
				t.Fatalf("emitted %d sectors, want %d", len(got), len(want))
			}
			for i := range want {
				if got[i].LinearSec != want[i].LinearSec {
					t.Errorf("sector[%d] linear = %d, want %d", i, got[i].LinearSec, want[i].LinearSec)
				}
				if !bytes.Equal(got[i].Data[:], want[i].Data[:]) {
					t.Errorf("sector[%d] data mismatch", i)
				}
			}
		})
	}
}

// TestRawSinkPending pins the buffered-byte count after a non-aligned stream — the
// state the Z80 RRS_FILL must match before Finish.
func TestRawSinkPending(t *testing.T) {
	s := feed([][]byte{seq(0, 512+37)}, false)
	if s.Pending() != 37 {
		t.Errorf("Pending() = %d, want 37", s.Pending())
	}
	if len(s.Writes()) != 1 {
		t.Errorf("emitted %d sectors before Finish, want 1", len(s.Writes()))
	}
	s.Finish()
	if s.Pending() != 0 {
		t.Errorf("Pending() after Finish = %d, want 0", s.Pending())
	}
	if len(s.Writes()) != 2 {
		t.Errorf("emitted %d sectors after Finish, want 2", len(s.Writes()))
	}
	// The flushed tail sector is zero-padded past byte 37.
	tail := s.Writes()[1].Data
	for i := 37; i < SectorSize; i++ {
		if tail[i] != 0 {
			t.Fatalf("tail sector byte %d = %#x, want 0 (zero-pad)", i, tail[i])
		}
	}
}

func oneByteEach(b []byte) [][]byte {
	out := make([][]byte, len(b))
	for i := range b {
		out[i] = []byte{b[i]}
	}
	return out
}
