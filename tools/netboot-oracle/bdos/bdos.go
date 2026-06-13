// Package bdos is the Go authority for the SAM-side netboot storage seam: the
// UIFA/DIFA field arithmetic that glues the i83 TFTP server (serve a file by
// name) and the i82 TFTP client (write a received file by name) to the B-DOS /
// SAMDOS-2 file-I/O hooks (HGTHD/HLOAD/HSAVE, the HRECORD-selected flat store).
//
// What this package models — and the load-bearing honesty line:
//
//   - The UIFA name-block layout HGTHD/HSAVE read (type + 10-char space-padded
//     name + 4-char ext), and
//   - the DIFA length encoding HGTHD deposits (pages-count at +34, length-mod-16K
//     at +35..36 with bit 7 of the high byte set as a marker), decoded back to a
//     32-bit byte length — the file size the OACK's `tsize` reports, and
//   - the HSAVE UIFA length encoding (the inverse: total length -> pages-count +
//     length-mod-16K, plus the source page/offset).
//
// This arithmetic is pure (memory layout + integer maths); it is the
// host-verifiable half of the storage seam, the byte-for-byte porting spec for
// the Z80 `bdos_name_to_uifa` / `bdos_difa_to_size` / `bdos_fill_save_uifa`
// routines. The field conventions are taken verbatim from the assembler's own
// real hook call sites (src/loader.asm load_payload_generic, src/io.asm
// fill_uifa, src/main_loop.asm save_out_file) and docs/specs/samdos-file-io.md.
//
// What this package does NOT model, and what stays UNVERIFIED until real
// hardware: the actual RST 8 hook dispatch into B-DOS (HGTHD's directory walk,
// HLOAD's sector reads, HSAVE's sector writes, HRECORD's record selection). The
// flat-memory koron-go/z80 harness has no ROM / no SAMDOS bank / no RST 8
// dispatch, so the hook bodies cannot be exercised host-side. The i62 dual-run
// proof (bdos-version-landscape.md) verified those hooks round-trip on real
// SAMDOS-2 + B-DOS-AL backends; an end-to-end netboot fetch on real Trinity is
// the final integration gate (CLAUDE.md §5). Emulation-verified ≠ hardware-
// verified.
package bdos

import "errors"

// UIFA / DIFA field layout (docs/specs/samdos-file-io.md; src/sam_io.inc).
const (
	UIFALen     = 48                // a UIFA / DIFA is 48 bytes
	OffType     = 0                 // file type (19 = CODE, 16 = BASIC)
	OffName     = 1                 // 10 bytes, space-padded
	OffExt      = 11                // 4 bytes, space-padded (unused for CODE)
	OffPage     = 31                // HSAVE: source physical page (low 5 bits -> HMPR)
	OffLoad     = 32                // HSAVE: source offset in section C (LE word)
	OffPages    = 34                // pages-count (number of whole 16 KB pages)
	OffLength   = 35                // length-mod-16K (LE word; HGTHD sets bit 7 of the high byte)
	NameLen     = 10                // the on-disk filename field width
	ExtLen      = 4                 // the on-disk extension field width
	NameExtSpan = OffPage - OffName // 30: name(10)+ext(4)+filler(16) up to byte 31
)

// TypeCode is the SAM file type for a machine-code file (the type the netboot
// images use). docs/notes/sam-file-header.md.
const TypeCode = 19

// PageSize is the SAM 16 KB physical-page size; B-DOS file lengths are encoded
// as (pages * PageSize) + lengthMod16K.
const PageSize = 16384

// DifaLenMarker is bit 7 of the DIFA length high byte (byte +36): HGTHD sets it
// as a "length present" marker (samdos/src/h.s; src/loader.asm clears it before
// using the length). A decoder must mask it off.
const DifaLenMarker = 0x80

// NameToUIFA builds the 48-byte UIFA name block HGTHD / HSAVE read for a CODE
// file: type at +0, the filename space-padded into the 10-byte name field, the
// 4-byte ext field spaced, and bytes 15..47 filled with 0xFF (the fill_uifa
// convention, src/io.asm). The filename is truncated to 10 chars (the on-disk
// field width) — longer names are not representable, matching the SAM directory.
//
// This is the Go authority for the Z80 `bdos_name_to_uifa`.
func NameToUIFA(name string) [UIFALen]byte {
	var u [UIFALen]byte
	u[OffType] = TypeCode
	// Name field: copy up to 10 chars, space-pad the rest.
	for i := 0; i < NameLen; i++ {
		if i < len(name) {
			u[OffName+i] = name[i]
		} else {
			u[OffName+i] = ' '
		}
	}
	// Ext field: 4 spaces (CODE files carry no ext).
	for i := 0; i < ExtLen; i++ {
		u[OffExt+i] = ' '
	}
	// Bytes 15..47: 0xFF filler (fill_uifa).
	for i := OffExt + ExtLen; i < UIFALen; i++ {
		u[i] = 0xFF
	}
	return u
}

// DifaToSize decodes a 48-byte DIFA (as HGTHD deposits it) into the file's total
// byte length: pages-count (byte +34) * 16384 + length-mod-16K (bytes +35..36
// LE, with the +36 bit-7 marker masked off). This is the size the OACK `tsize`
// reports and the byte count the server streams.
//
// The Go authority for the Z80 `bdos_difa_to_size`. Mirrors src/loader.asm's
// DIFA read (`ld hl,(&4B50+35) / and &7F` then `ld a,(&4B50+34)`).
func DifaToSize(difa [UIFALen]byte) uint32 {
	pages := uint32(difa[OffPages])
	lengthMod := uint32(difa[OffLength]) | (uint32(difa[OffLength+1]&^DifaLenMarker) << 8)
	return pages*PageSize + lengthMod
}

// SaveUIFA builds the UIFA HSAVE reads to write a CODE file of `size` bytes
// living at section-C offset `loadAddr` in physical page `page`: the name block,
// plus UIFA[31] = page (low 5 bits), UIFA[32..33] = loadAddr (LE), UIFA[34] =
// pages-count (size >> 14), UIFA[35..36] = length-mod-16K (size & 0x3FFF, LE).
//
// The Go authority for the Z80 `bdos_fill_save_uifa`; mirrors
// src/main_loop.asm save_out_file's length encoding (RLCA/RLCA -> pages, AND
// &3F -> remainder). Returns an error if size exceeds the 16-bit UIFA length
// encoding's 64 KB ceiling (registry i24 — a wider counter is future work).
func SaveUIFA(name string, page uint8, loadAddr uint16, size uint32) ([UIFALen]byte, error) {
	u := NameToUIFA(name)
	if size > 0xFFFF {
		return u, errors.New("bdos: file size exceeds the 16-bit UIFA length ceiling (64 KB; registry i24)")
	}
	u[OffPage] = page & 0x1F
	u[OffLoad] = byte(loadAddr)
	u[OffLoad+1] = byte(loadAddr >> 8)
	u[OffPages] = byte(size >> 14) // whole 16 KB pages
	rem := uint16(size & 0x3FFF)   // low 14 bits
	u[OffLength] = byte(rem)
	u[OffLength+1] = byte(rem >> 8)
	return u, nil
}

// --- Flat-store directory model -------------------------------------------

// Entry is one file in a B-DOS record's flat directory: a name and its byte
// length. (The on-disk entry carries far more — the sector map, the body-header
// cache — but the netboot resolve seam only needs name -> length; the rest is
// HGTHD's business on real hardware.)
type Entry struct {
	Name string
	Size uint32
}

// Store models a single B-DOS record's flat directory (the storage the i83
// server resolves names against and the i82 client adds received files to). It
// is the host-side stand-in for the directory HGTHD would walk after HRECORD
// selects the record — analogous to how enc28j60.go stands in for the Trinity
// Ethernet hardware. Lookup is a linear name walk (HGTHD has no index), exactly
// like the Z80 resolve_store_loop.
type Store struct {
	entries []Entry
}

// NewStore builds a store from a set of files. Names are matched exactly
// (case-sensitive here; the real cknam is case-insensitive — the netboot
// filenames the Pi requests are already lowercase, so exact match is faithful
// to the captured traffic).
func NewStore(files ...Entry) *Store {
	s := &Store{}
	s.entries = append(s.entries, files...)
	return s
}

// Lookup returns a file's byte length and true if a file of that exact name is
// in the store, or (0, false) on a miss — the resolve the i83 server does
// (ERROR(1) on a miss, OACK with the size on a hit). Mirrors the Z80
// resolve_store_loop's linear walk.
func (s *Store) Lookup(name string) (uint32, bool) {
	for _, e := range s.entries {
		if e.Name == name {
			return e.Size, true
		}
	}
	return 0, false
}

// Add inserts (or replaces) a file by name — the i82 client's write-out of a
// received file into the record's directory (the in-memory model of the HSAVE
// that lands the bytes on real hardware).
func (s *Store) Add(name string, size uint32) {
	for i := range s.entries {
		if s.entries[i].Name == name {
			s.entries[i].Size = size
			return
		}
	}
	s.entries = append(s.entries, Entry{Name: name, Size: size})
}

// Wire serialises the store into the flat "name\0 + 4-byte LE size" records the
// Z80 resolve_store_loop walks, terminated by an empty name (a lone NUL). This
// is the bridge between the Go model and the Z80 STORE buffer the harness fills
// — the host test builds the same directory on both sides and asserts the Z80
// resolve agrees with Store.Lookup.
func (s *Store) Wire() []byte {
	var b []byte
	for _, e := range s.entries {
		b = append(b, []byte(e.Name)...)
		b = append(b, 0)
		b = append(b, byte(e.Size), byte(e.Size>>8), byte(e.Size>>16), byte(e.Size>>24))
	}
	b = append(b, 0) // empty name = end of store
	return b
}
