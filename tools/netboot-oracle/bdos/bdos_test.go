package bdos

import (
	"bytes"
	"testing"
)

// TestNameToUIFA pins the UIFA name-block layout HGTHD/HSAVE read: type 19,
// the filename space-padded into the 10-byte name field, a spaced 4-byte ext,
// and 0xFF filler in bytes 15..47 (the fill_uifa convention).
func TestNameToUIFA(t *testing.T) {
	u := NameToUIFA("config.txt")
	if u[OffType] != TypeCode {
		t.Errorf("type = %d, want %d (CODE)", u[OffType], TypeCode)
	}
	// "config.txt" is exactly 10 chars -> fills the name field, no padding.
	if got := string(u[OffName : OffName+NameLen]); got != "config.txt" {
		t.Errorf("name field = %q, want %q", got, "config.txt")
	}
	if got := string(u[OffExt : OffExt+ExtLen]); got != "    " {
		t.Errorf("ext field = %q, want 4 spaces", got)
	}
	for i := OffExt + ExtLen; i < UIFALen; i++ {
		if u[i] != 0xFF {
			t.Errorf("filler byte %d = %#x, want 0xFF", i, u[i])
		}
	}

	// A short name is space-padded in the name field.
	s := NameToUIFA("kernel8")
	if got := string(s[OffName : OffName+NameLen]); got != "kernel8   " {
		t.Errorf("short name field = %q, want %q", got, "kernel8   ")
	}

	// A name longer than 10 chars is truncated to the field width.
	l := NameToUIFA("verylongfilename")
	if got := string(l[OffName : OffName+NameLen]); got != "verylongfi" {
		t.Errorf("long name field = %q, want truncated %q", got, "verylongfi")
	}
}

// TestDifaToSize pins the DIFA length decode: pages*16384 + lengthMod16K, with
// the +36 bit-7 marker masked off (HGTHD sets it; src/loader.asm clears it).
func TestDifaToSize(t *testing.T) {
	mk := func(pages byte, lengthMod uint16, marker bool) [UIFALen]byte {
		var d [UIFALen]byte
		d[OffPages] = pages
		d[OffLength] = byte(lengthMod)
		hi := byte(lengthMod >> 8)
		if marker {
			hi |= DifaLenMarker
		}
		d[OffLength+1] = hi
		return d
	}
	cases := []struct {
		pages     byte
		lengthMod uint16
		marker    bool
		want      uint32
	}{
		{0, 0, false, 0},
		{0, 300, true, 300},  // marker set must be masked off
		{1, 0, false, 16384}, // exactly one page
		{2, 300, true, 2*16384 + 300},
		{3, 16383, false, 3*16384 + 16383},
		{255, 16383, true, 255*16384 + 16383}, // max: 4193663
	}
	for _, c := range cases {
		got := DifaToSize(mk(c.pages, c.lengthMod, c.marker))
		if got != c.want {
			t.Errorf("DifaToSize(pages=%d, mod=%d, marker=%v) = %d, want %d", c.pages, c.lengthMod, c.marker, got, c.want)
		}
	}
}

// TestSaveUIFA pins the HSAVE UIFA: the name block + page + offset + the length
// encoding (pages = size>>14, lengthMod16K = size&0x3FFF), and the 64 KB ceiling.
func TestSaveUIFA(t *testing.T) {
	const page = 5
	const addr = 0x8000
	u, err := SaveUIFA("recovery", page, addr, 2*16384+300)
	if err != nil {
		t.Fatalf("SaveUIFA: %v", err)
	}
	if u[OffPage] != page {
		t.Errorf("UIFA[31] page = %d, want %d", u[OffPage], page)
	}
	if got := uint16(u[OffLoad]) | uint16(u[OffLoad+1])<<8; got != addr {
		t.Errorf("UIFA[32..33] load = %#x, want %#x", got, addr)
	}
	if u[OffPages] != 2 {
		t.Errorf("UIFA[34] pages = %d, want 2", u[OffPages])
	}
	if got := uint16(u[OffLength]) | uint16(u[OffLength+1])<<8; got != 300 {
		t.Errorf("UIFA[35..36] lengthMod = %d, want 300", got)
	}
	// The name block is the NameToUIFA block (same bytes 0..30).
	want := NameToUIFA("recovery")
	if !bytes.Equal(u[:OffPage], want[:OffPage]) {
		t.Errorf("name block differs from NameToUIFA")
	}

	// The 16-bit length ceiling (registry i24) is enforced.
	if _, err := SaveUIFA("big", 5, 0x8000, 0x10000); err == nil {
		t.Error("SaveUIFA accepted a size over the 64 KB ceiling")
	}
}

// TestSaveDifaRoundTrip pins the inverse relationship: a UIFA built by SaveUIFA
// decodes (via the DIFA length fields, which share the +34/+35..36 layout) back
// to the same size — the property the server-write / server-read seam relies on.
func TestSaveDifaRoundTrip(t *testing.T) {
	for _, size := range []uint32{0, 1, 511, 512, 16383, 16384, 16385, 40000, 65535} {
		u, err := SaveUIFA("f", 5, 0x8000, size)
		if err != nil {
			t.Fatalf("SaveUIFA(%d): %v", size, err)
		}
		// SaveUIFA writes lengthMod16K without the bit-7 marker; DifaToSize
		// masks it regardless, so the round-trip holds.
		if got := DifaToSize(u); got != size {
			t.Errorf("round-trip size %d -> %d", size, got)
		}
	}
}

// TestStore pins the flat-directory model the i83 server resolves against and
// the i82 client writes to, plus the wire format the Z80 resolve walks.
func TestStore(t *testing.T) {
	s := NewStore(Entry{"config.txt", 300}, Entry{"kernel8.img", 40000})
	if sz, ok := s.Lookup("config.txt"); !ok || sz != 300 {
		t.Errorf("Lookup config.txt = (%d,%v), want (300,true)", sz, ok)
	}
	if _, ok := s.Lookup("missing"); ok {
		t.Error("Lookup of a missing file returned ok")
	}

	// Add is the client write-out; a re-Add replaces the size.
	s.Add("recovery.elf", 12345)
	if sz, ok := s.Lookup("recovery.elf"); !ok || sz != 12345 {
		t.Errorf("after Add: Lookup = (%d,%v), want (12345,true)", sz, ok)
	}
	s.Add("recovery.elf", 67890)
	if sz, _ := s.Lookup("recovery.elf"); sz != 67890 {
		t.Errorf("re-Add did not replace size: got %d, want 67890", sz)
	}

	// Wire is the "name\0 + 4-byte LE size" record list the Z80 resolve walks,
	// terminated by an empty name.
	w := NewStore(Entry{"ab", 0x01020304}).Wire()
	want := []byte{'a', 'b', 0, 0x04, 0x03, 0x02, 0x01, 0}
	if !bytes.Equal(w, want) {
		t.Errorf("Wire() = %x, want %x", w, want)
	}
}
