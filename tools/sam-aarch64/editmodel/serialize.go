package editmodel

// serialize.go implements the cleanly-separable serialize/load seam (design
// decision 4). This is a deliberately simple, self-describing binary format
// — the separable boundary that sub-item i41c swaps for the v2 .tbn encoding
// via the i48 encoder. Changing the format requires only this file and
// Load/Serialize callers; the block-list algorithm in editmodel.go is
// unaffected.

import (
	"encoding/binary"
	"fmt"
	"io"
)

// magic is the 4-byte file magic for the editmodel serialization format.
const magic = "EMDL"

// version is the format version byte written by Serialize.
const version = 0x01

// Serialize writes the document to w in the editmodel v1 format:
//
//	4 bytes: magic "EMDL"
//	1 byte:  version 0x01
//	4 bytes: little-endian uint32 line count
//	per line (in document order):
//	  3 bytes: little-endian u24 RecordID
//	  2 bytes: little-endian uint16 text length
//	  N bytes: text
//
// The format is independent of the block partition — block boundaries are not
// recorded. Serialize→Load→Serialize is byte-stable as long as the logical
// (id, text) sequence is preserved.
func (d *Document) Serialize(w io.Writer) error {
	if _, err := io.WriteString(w, magic); err != nil {
		return err
	}
	if _, err := w.Write([]byte{version}); err != nil {
		return err
	}
	lc := d.LineCount()
	var hdr [4]byte
	binary.LittleEndian.PutUint32(hdr[:], uint32(lc))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}

	for _, b := range d.blocks {
		for _, l := range b.lines {
			if len(l.text) > 65535 {
				return fmt.Errorf("editmodel: line id %d text length %d exceeds uint16 max (65535)", l.id, len(l.text))
			}
			// 3-byte little-endian u24 id.
			var idBuf [3]byte
			idBuf[0] = byte(l.id)
			idBuf[1] = byte(l.id >> 8)
			idBuf[2] = byte(l.id >> 16)
			if _, err := w.Write(idBuf[:]); err != nil {
				return err
			}
			// 2-byte little-endian uint16 text length.
			var lenBuf [2]byte
			binary.LittleEndian.PutUint16(lenBuf[:], uint16(len(l.text)))
			if _, err := w.Write(lenBuf[:]); err != nil {
				return err
			}
			if _, err := w.Write(l.text); err != nil {
				return err
			}
		}
	}
	return nil
}

// Load parses an editmodel v1 stream from r and reconstructs a Document.
// Lines are re-blocked deterministically: fill a block until adding the next
// line would exceed blockCapacityBytes with ≥1 line already present, then
// start a new block. This ensures Load→Serialize is byte-stable for any
// document produced by Serialize.
func Load(r io.Reader) (*Document, error) {
	// Magic.
	var magicBuf [4]byte
	if _, err := io.ReadFull(r, magicBuf[:]); err != nil {
		return nil, fmt.Errorf("editmodel: reading magic: %w", err)
	}
	if string(magicBuf[:]) != magic {
		return nil, fmt.Errorf("editmodel: bad magic %q (want %q)", string(magicBuf[:]), magic)
	}

	// Version.
	var verBuf [1]byte
	if _, err := io.ReadFull(r, verBuf[:]); err != nil {
		return nil, fmt.Errorf("editmodel: reading version: %w", err)
	}
	if verBuf[0] != version {
		return nil, fmt.Errorf("editmodel: unsupported version 0x%02X (want 0x%02X)", verBuf[0], version)
	}

	// Line count.
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, fmt.Errorf("editmodel: reading line count: %w", err)
	}
	lineCount := int(binary.LittleEndian.Uint32(hdr[:]))

	d := &Document{
		nextID: 1,
		loc:    make(map[RecordID]*block),
	}

	var maxID RecordID
	var curBlock *block

	for i := 0; i < lineCount; i++ {
		// 3-byte u24 id.
		var idBuf [3]byte
		if _, err := io.ReadFull(r, idBuf[:]); err != nil {
			return nil, fmt.Errorf("editmodel: reading id for line %d: %w", i, err)
		}
		id := RecordID(uint32(idBuf[0]) | uint32(idBuf[1])<<8 | uint32(idBuf[2])<<16)
		if id == 0 {
			return nil, fmt.Errorf("editmodel: line %d has reserved id 0", i)
		}
		if id > maxRecordID {
			return nil, fmt.Errorf("editmodel: line %d id 0x%X exceeds maxRecordID", i, id)
		}

		// 2-byte text length.
		var lenBuf [2]byte
		if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
			return nil, fmt.Errorf("editmodel: reading text length for line %d: %w", i, err)
		}
		textLen := int(binary.LittleEndian.Uint16(lenBuf[:]))

		// Text bytes.
		text := make([]byte, textLen)
		if textLen > 0 {
			if _, err := io.ReadFull(r, text); err != nil {
				return nil, fmt.Errorf("editmodel: reading text for line %d: %w", i, err)
			}
		}

		l := line{id: id, text: text}
		lineBytes := len(text) + lineOverheadBytes

		// Determine whether this line fits in the current block or needs a
		// new one. A block with ≥1 line that would overflow on adding this
		// line triggers a new block.
		needNewBlock := curBlock == nil ||
			(len(curBlock.lines) >= 1 && curBlock.bytes()+lineBytes > blockCapacityBytes)

		if needNewBlock {
			curBlock = &block{}
			d.blocks = append(d.blocks, curBlock)
		}

		curBlock.lines = append(curBlock.lines, l)
		d.loc[id] = curBlock

		if id > maxID {
			maxID = id
		}
	}

	if maxID > 0 {
		d.nextID = maxID + 1
	}
	return d, nil
}
