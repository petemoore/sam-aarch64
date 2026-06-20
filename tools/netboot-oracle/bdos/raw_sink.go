package bdos

// raw_sink.go — the Go authority for the i122b streaming raw-record write (the
// DiskRecord storage-class persist path, docs/specs/netboot-storage-manifest-
// design.md §6.5). The Z80 src/netboot/raw_record_sink.asm mirrors this byte for
// byte; the real RST 8 HWSAD per sector stays the hardware gate (CLAUDE.md §5).
// Emulation-verified ≠ hardware-verified.
//
// Why a streaming sector sink (not buffer-then-save): a Trinity disk record is
// exactly RecordSize (819,200) bytes — far larger than the bounded RAM window the
// i99 streaming receive holds at once. So the fetched image cannot be buffered
// whole and saved with one HSAVE (the fw_span flat-file path). Instead the body
// bytes are accumulated into SectorSize (512-byte) sectors as they arrive and
// each full sector is written immediately via HWSAD (i114c bdos_write_record), at
// consecutive linear sectors 0, 1, 2, … of the target record. RAM never holds
// more than one sector of the image.
//
// The body arrives in arbitrary-sized chunks (the bodySink forwards each i99
// flush window whole, and a window is neither sector-sized nor sector-aligned),
// so the sink must re-block an arbitrary byte stream into fixed 512-byte sectors:
// a chunk may complete several sectors, complete none, or straddle a boundary.
// RawSink is that re-blocking accumulator; the Z80 raw_record_sink_leaf is its
// port, and raw_record_sink_test.go feeds both the identical chunk sequence and
// asserts the emitted sector writes match.

// SectorSize is the byte length of one B-DOS / SAMDOS sector — the granularity of
// the HWSAD raw-sector write. Identical to bdSectorSize in z80/bdos_store.go.
const SectorSize = 512

// SectorsPerRecord is the number of SectorSize sectors in one Trinity record
// (RecordSize / SectorSize = 819200 / 512 = 1600). A full disk image is exactly
// this many sectors, so it streams sector-aligned with no partial tail; the
// partial-tail handling in Finish exists for generality and for short test
// images, not for the 819,200-byte production case.
const SectorsPerRecord = RecordSize / SectorSize

// RawSectorWrite is one sector emitted by RawSink: the linear sector index within
// the target record (0-based: 0, 1, 2, …) and the 512 bytes written there. It
// mirrors z80.SectorWrite (minus the Record field, which the selected record
// supplies at HWSAD time, not the sink).
type RawSectorWrite struct {
	LinearSec int
	Data      [SectorSize]byte
}

// RawSink re-blocks an arbitrary byte stream into fixed SectorSize sectors,
// emitting one RawSectorWrite per full sector at consecutive linear sectors
// starting at 0. Construct with NewRawSink; feed body bytes with Write (any chunk
// sizes); call Finish once at end-of-stream to flush a final partial sector
// (zero-padded). Writes returns the emitted sector writes in order.
type RawSink struct {
	buf    []byte // pending bytes of the sector being filled (len 0..SectorSize-1 between Writes)
	linear int    // next linear sector index to emit
	writes []RawSectorWrite
}

// NewRawSink returns an empty sink positioned at linear sector 0.
func NewRawSink() *RawSink { return &RawSink{} }

// Write accumulates p into the current sector, flushing each full SectorSize
// boundary as it is reached. p may be any length (including 0) and need not be
// sector-aligned; bytes that do not complete a sector remain buffered for the
// next Write or for Finish.
func (s *RawSink) Write(p []byte) {
	for len(p) > 0 {
		avail := SectorSize - len(s.buf)
		n := avail
		if len(p) < n {
			n = len(p)
		}
		s.buf = append(s.buf, p[:n]...)
		p = p[n:]
		if len(s.buf) == SectorSize {
			s.flush()
		}
	}
}

// Finish flushes a final partial sector if any bytes remain buffered, zero-padding
// it to SectorSize. It is a no-op when the stream ended on a sector boundary (the
// exact-size disk-record case). Call exactly once after the last Write.
func (s *RawSink) Finish() {
	if len(s.buf) == 0 {
		return
	}
	for len(s.buf) < SectorSize {
		s.buf = append(s.buf, 0)
	}
	s.flush()
}

// flush emits the full buffer as the next linear sector and resets the buffer.
func (s *RawSink) flush() {
	var w RawSectorWrite
	w.LinearSec = s.linear
	copy(w.Data[:], s.buf)
	s.writes = append(s.writes, w)
	s.linear++
	s.buf = s.buf[:0]
}

// Writes returns the sector writes emitted so far, in order.
func (s *RawSink) Writes() []RawSectorWrite { return s.writes }

// Pending returns the number of bytes buffered for the sector not yet flushed
// (0 between completed sectors). Exposed so a test can assert the Z80 sink's
// RRS_FILL state matches.
func (s *RawSink) Pending() int { return len(s.buf) }
