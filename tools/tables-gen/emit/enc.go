package emit

import (
	"encoding/binary"
	"io"
	"sort"

	enc "github.com/petemoore/sam-aarch64/tools/aarch64enc"
)

// RenderEnc writes the binary .enc file per spec §2.
func RenderEnc(w io.Writer, forms []enc.Form) error {
	// Header.
	if _, err := w.Write([]byte("ENC1")); err != nil {
		return err
	}
	hdr := make([]byte, 4)
	binary.LittleEndian.PutUint16(hdr[0:2], 1) // version
	binary.LittleEndian.PutUint16(hdr[2:4], 0) // flags
	if _, err := w.Write(hdr); err != nil {
		return err
	}

	sorted := make([]enc.Form, len(forms))
	copy(sorted, forms)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].MnemonicID < sorted[j].MnemonicID
	})

	// Form table.
	cnt := make([]byte, 4)
	binary.LittleEndian.PutUint32(cnt, uint32(len(sorted)))
	if _, err := w.Write(cnt); err != nil {
		return err
	}
	for _, f := range sorted {
		head := make([]byte, 11)
		binary.LittleEndian.PutUint16(head[0:2], f.MnemonicID)
		head[2] = byte(len(f.Slots))
		binary.LittleEndian.PutUint32(head[3:7], f.Pattern)
		binary.LittleEndian.PutUint32(head[7:11], f.Mask)
		if _, err := w.Write(head); err != nil {
			return err
		}
		for _, s := range f.Slots {
			row := []byte{
				byte(s.SlotKind),
				byte(s.ExpectedKind),
				s.BitPosition,
				s.BitWidth,
			}
			if _, err := w.Write(row); err != nil {
				return err
			}
		}
	}

	// Mnemonic index.
	type idxEntry struct {
		MnemonicID  uint16
		FirstFormID uint32
		FormCount   uint16
	}
	var entries []idxEntry
	i := 0
	for i < len(sorted) {
		j := i
		for j < len(sorted) && sorted[j].MnemonicID == sorted[i].MnemonicID {
			j++
		}
		entries = append(entries, idxEntry{
			MnemonicID:  sorted[i].MnemonicID,
			FirstFormID: uint32(i),
			FormCount:   uint16(j - i),
		})
		i = j
	}
	binary.LittleEndian.PutUint32(cnt, uint32(len(entries)))
	if _, err := w.Write(cnt); err != nil {
		return err
	}
	for _, e := range entries {
		row := make([]byte, 8)
		binary.LittleEndian.PutUint16(row[0:2], e.MnemonicID)
		binary.LittleEndian.PutUint32(row[2:6], e.FirstFormID)
		binary.LittleEndian.PutUint16(row[6:8], e.FormCount)
		if _, err := w.Write(row); err != nil {
			return err
		}
	}
	return nil
}
