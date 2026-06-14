package tls

// RecordReassembler frames complete TLS records out of an arbitrary byte stream.
// TCP segments do not align to TLS record boundaries, so a caller feeds it the
// payload chunks as they arrive and it returns each complete record
// ([5-byte header || payload]) as soon as enough bytes have been seen, buffering
// any partial-record tail for the next chunk. The record length is the 16-bit
// big-endian field at header bytes 3..4 (length = hdr[3]<<8 | hdr[4]), exactly as
// capture.go::readRecord frames a record off a blocking conn — but readRecord
// relies on a net.Conn's already-ordered stream, whereas this reassembles from
// segments that may split a record (even its header) or coalesce several.
//
// This is the host authority for the Z80 reassembler src/netboot/tls_reasm.asm:
// both are driven with the same chunk sequences and their emitted records must
// match byte-for-byte (tools/netboot-oracle/z80/tls_reasm_test.go).
type RecordReassembler struct {
	buf []byte // bytes of the in-progress record so far (< one complete record)
}

// Feed accumulates chunk and returns, in order, every record it now completes.
// Partial-record bytes remain buffered for the next Feed. It consumes the chunk
// incrementally — taking only the bytes the in-progress record still needs —
// so the buffer never holds more than one complete record at a time.
func (r *RecordReassembler) Feed(chunk []byte) [][]byte {
	var out [][]byte
	for len(chunk) > 0 {
		// Bytes the in-progress record still needs: first to complete its
		// 5-byte header, then (once the length field is known) its payload.
		var need int
		if len(r.buf) < 5 {
			need = 5 - len(r.buf)
		} else {
			need = (5 + (int(r.buf[3])<<8 | int(r.buf[4]))) - len(r.buf)
		}
		take := need
		if take > len(chunk) {
			take = len(chunk)
		}
		r.buf = append(r.buf, chunk[:take]...)
		chunk = chunk[take:]
		if len(r.buf) >= 5 {
			total := 5 + (int(r.buf[3])<<8 | int(r.buf[4]))
			if len(r.buf) == total {
				out = append(out, append([]byte(nil), r.buf...))
				r.buf = r.buf[:0]
			}
		}
	}
	return out
}
