package z80

// bdos_store.go — host model of the SAMDOS-2 / B-DOS file-I/O hook dispatch the
// netboot write-out drives (i134). The flat harness has no ROM, no SAMDOS bank,
// and no RST 8 vector, so bdos_seam.asm's hook bodies (bdos_select_record /
// bdos_save_hook, behind `ifndef NETBOOT_HOSTTEST`) could not run host-side and
// the client's persistence path was carved out of emulation and shipped straight
// to hardware — the exact emulate-before-hardware bypass CLAUDE.md rule 7 forbids
// and how the i82 client's init-path bug reached hardware uncaught.
//
// BDOSStore plugs the gap: attached via Machine.AttachBDOS, it registers an RST 8
// handler (harness.go's rstHandlers) that models the hooks the write-out and
// record-detection paths use:
//
//   HRECORD (&9C): bdos_select_record does `ld l,a; ld h,0; xor a; rst 8; defb &9C`
//     — A=0 = the select sub-function, HL = the record number. The store records
//     the selected record. (This is where the i119 bug lives: client_main selects
//     record 0 = the floppy, not a Trinity record n>=1 — now visible in emulation.)
//   HSAVE (132): bdos_save_hook copies the built UIFA to &4B00, sets IX there, and
//     `rst 8; defb 132`. The store captures + decodes the 48-byte UIFA at IX (the
//     filename, source page/offset, and byte length), tagged with the record that
//     was selected when the save ran.
//   HRSAD (160 / &A0): bdos_read_sector loads D=track, E=sector, HL=dest buffer,
//     and `rst 8; defb &A0`. The store fills the destination buffer with the 512
//     bytes for that (track, sector) from the attached CardModel (or all-zero if no
//     card is attached). This is the i119 brick-1 emulation foundation: the raw
//     sector-read primitive the free-record-detection algorithm will drive.
//
// THE HONESTY LINE (CLAUDE.md §5): this models the *digital dispatch* (which hook,
// with which UIFA / sector coordinates, against which selected record) so the
// write-out's and detection logic runs and is asserted host-side. It does NOT
// persist bytes to a real medium, model B-DOS error returns (e.g. "Invalid record"
// on an unstamped record), or replace the real RST 8 — that stays gated on real
// Trinity hardware. Emulation-verified is not hardware-verified.

import (
	"strings"

	"github.com/koron-go/z80"
)

// B-DOS hook codes + UIFA layout (mirror bdos_seam.asm BD_HOOK_* / BD_OFF_*).
const (
	rst8HookAddr = 0x0008 // the RST 8 vector the SAMDOS/B-DOS hooks dispatch through

	bdHookHRECORD  = 0x9C // record select (156)
	bdHookHGTHD    = 129  // get file header (lookup by name) — server-side, not modelled here
	bdHookHSAVE    = 132  // save whole file
	bdHookHWSAD    = 149  // write raw 512-byte sector at (D=track, E=sector) from HL (bdos15a.src.txt:528-531)
	bdHookHRSAD    = 160  // read raw 512-byte sector at (D=track, E=sector) into HL
	bdHookListRead = 161  // card-absolute list-sector read: E=listSector (1-based), HL=dest (512 bytes)

	bdUIFALen = 48 // the UIFA HSAVE reads at IX (bdos_save_hook stages it at &4B00)

	bdOffName   = 1 // 10-byte space-padded filename
	bdOffNameN  = 10
	bdOffPage   = 31 // HSAVE source page (low 5 bits)
	bdOffLoad   = 32 // HSAVE source offset (LE word)
	bdOffPages  = 34 // pages-count
	bdOffLength = 35 // length-mod-16K (LE word); bit 15 of the word is a marker

	// bdSectorSize is the number of bytes in one B-DOS / SAMDOS sector.
	bdSectorSize = 512
	// bdSectorsPerTrack is the number of sectors per track in a B-DOS flat store.
	// Sectors are 1-indexed within a track (1..10); tracks are 0-indexed (0..79).
	bdSectorsPerTrack = 10
	// bdBDOSStampOffset is the byte offset of the 4-byte "BDOS" stamp within the
	// first data sector (linear sector 0) of a B-DOS-formatted record.
	bdBDOSStampOffset = 232
	// bdRecordLabelOffset is the byte offset of the 10-byte disk label (volume
	// name) within a record's first data sector. B-DOS get.label reads the record's
	// name here (bdos15a.src.txt:2852, "LD BC,210 ;point to disk name"); bit 7 of
	// the first byte is the write-protect flag. This is the per-record (selected-
	// record) view; the card-level record list (list sectors 1..base-1, 16-byte
	// entries) is a separate, reachable view (see CardModel doc).
	bdRecordLabelOffset = 210
	// bdRecordLabelLen is the length of the disk-label name field (new.lab copies
	// 10 bytes: bdos15a.src.txt:2780-2782 "LD C,10 / LDIR").
	bdRecordLabelLen = 10
)

// CardModel is the harness model of a Trinity SD card: a central record list (in
// the boot area) and per-record sector data (at least sector 0, the first data
// sector). Used by the HRSAD and bdHookListRead handlers to serve reads.
//
// Linear sector index within a record = track*10 + (sector-1), matching the
// `conv.de` formula in the SAMDOS source (D=track 0-79, E=sector 1-10).
//
// REACHABILITY (corrected 2026-06-19 — Pete): the central RecordList IS reachable
// from a user program. The `RECORD` BASIC command lists every record's name by
// reading the list sectors off the card (bdos15a.src.txt:886-906, list.record;
// docs/specs/trinity-record-detection-design.md §2). The narrower true fact is
// only that there is no dedicated RST-8 hook named "list records". The harness
// models card-absolute list-sector reads via bdHookListRead (ListSector method):
// Z80 code loads E=listSector, HL=dest and issues RST 8 / DEFB BD_HOOK_LISTREAD,
// which models the result of a raw SD CMD17 single-block read at the card-absolute
// LBA — the hardware path (SPI ladder, ports &DC–&DF) is hardware-gated and not
// emulated at the SPI level (docs/specs/trinity-record-detection-design.md §8,
// open-verification-item-1). Per-record identity (the "BDOS" stamp at +232 and the
// disk label at +210) is still read via HRSAD after HRECORD-selecting the record —
// that remains the selected-record view, bdos_inspect_record.
type CardModel struct {
	// RecordList holds the 16-byte record-list entries, indexed by record number
	// minus 1 (entry 0 = record 1). Entries beyond len(RecordList) read as all-zero.
	// Each entry is the full 16-byte record name (space-padded, bit 7 of byte 0 =
	// write-protect; free ⇔ (byte0 & 0x7F) == 0 — bdos15a.src.txt:946-948 frec3x).
	// Served to Z80 code via ListSector / bdHookListRead.
	RecordList [][16]byte
	// Sectors holds the 512-byte sectors for each record (outer index = record
	// number minus 1, inner index = linear sector within the record:
	// D*10 + (E-1)). Sectors not present read as all-zero.
	Sectors []map[int][bdSectorSize]byte
}

// NewCardModel returns an empty card (no records, all sectors zero).
func NewCardModel() *CardModel { return &CardModel{} }

// SetRecordEntry sets the 16-byte record-list entry for record n (1-indexed).
// Grows the list as needed.
func (c *CardModel) SetRecordEntry(n int, entry [16]byte) {
	idx := n - 1
	for len(c.RecordList) <= idx {
		c.RecordList = append(c.RecordList, [16]byte{})
	}
	c.RecordList[idx] = entry
}

// SetSector sets the 512-byte content of a sector in record n (1-indexed), at
// linear sector index linearSector = track*10 + (sector-1).
func (c *CardModel) SetSector(n, linearSector int, data [bdSectorSize]byte) {
	idx := n - 1
	for len(c.Sectors) <= idx {
		c.Sectors = append(c.Sectors, nil)
	}
	if c.Sectors[idx] == nil {
		c.Sectors[idx] = map[int][bdSectorSize]byte{}
	}
	c.Sectors[idx][linearSector] = data
}

// RecordEntry returns the 16-byte list entry for record n (1-indexed), or an
// all-zero entry if n is out of range.
func (c *CardModel) RecordEntry(n int) [16]byte {
	idx := n - 1
	if idx < 0 || idx >= len(c.RecordList) {
		return [16]byte{}
	}
	return c.RecordList[idx]
}

// ListSector builds the 512-byte list sector for listSec (1-based). Each list
// sector holds 32 record-list entries of 16 bytes each (32*16 = 512). Entry e
// (0-indexed) within listSec maps to record number (listSec-1)*32 + e + 1. This
// models a card-absolute read of the list area (sectors 1..base-1 of the card,
// directly before record-1 data), the result a raw SD CMD17 at the corresponding
// LBA returns — docs/specs/trinity-record-detection-design.md §4.3.
func (c *CardModel) ListSector(listSec int) [bdSectorSize]byte {
	const entriesPerSector = 32
	const entrySize = 16
	var out [bdSectorSize]byte
	for e := 0; e < entriesPerSector; e++ {
		record := (listSec-1)*entriesPerSector + e + 1
		entry := c.RecordEntry(record)
		copy(out[e*entrySize:(e+1)*entrySize], entry[:])
	}
	return out
}

// Sector returns the 512-byte content of linearSector in record n (1-indexed),
// or all-zero if not set.
func (c *CardModel) Sector(n, linearSector int) [bdSectorSize]byte {
	idx := n - 1
	if idx < 0 || idx >= len(c.Sectors) || c.Sectors[idx] == nil {
		return [bdSectorSize]byte{}
	}
	return c.Sectors[idx][linearSector]
}

// SetRecordName sets the first 10 bytes of record n's 16-byte list-entry name
// (a test helper preserving bytes 10..15; the full entry name is 16 bytes,
// space-padded — docs/specs/trinity-record-detection-design.md §4.3). The name
// is space-padded to 10 bytes; longer names are truncated to 10.
func (c *CardModel) SetRecordName(n int, name string) {
	const nameLen = 10
	var entry [16]byte
	// preserve any existing bytes beyond the name field
	existing := c.RecordEntry(n)
	copy(entry[:], existing[:])
	for i := 0; i < nameLen; i++ {
		if i < len(name) {
			entry[i] = name[i]
		} else {
			entry[i] = ' '
		}
	}
	c.SetRecordEntry(n, entry)
}

// SetBDOSStamp writes the 4-byte "BDOS" stamp at offset 232 of record n's first
// data sector (linear sector 0), marking it as a B-DOS-formatted record.
func (c *CardModel) SetBDOSStamp(n int) {
	var sec [bdSectorSize]byte
	existing := c.Sector(n, 0)
	copy(sec[:], existing[:])
	copy(sec[bdBDOSStampOffset:], []byte("BDOS"))
	c.SetSector(n, 0, sec)
}

// SetRecordLabel writes the 10-byte disk label (volume name) at offset 210 of
// record n's first data sector — the per-record name bdos_inspect_record reads
// (the selected-record disk-label view, distinct from the card-level RecordList
// entry — see the type doc and trinity-record-detection-design.md §4.5). The name
// is space-padded to 10 bytes and truncated to 10 if longer. Bit 7
// of the first byte is the write-protect flag on real hardware; callers that need
// it set should OR it into the first character themselves.
func (c *CardModel) SetRecordLabel(n int, name string) {
	var sec [bdSectorSize]byte
	existing := c.Sector(n, 0)
	copy(sec[:], existing[:])
	for i := 0; i < bdRecordLabelLen; i++ {
		if i < len(name) {
			sec[bdRecordLabelOffset+i] = name[i]
		} else {
			sec[bdRecordLabelOffset+i] = ' '
		}
	}
	c.SetSector(n, 0, sec)
}

// BDOSSave is one captured HSAVE: the decoded UIFA plus the record selected when
// it ran. Size mirrors bdos_difa_to_size: pages*16384 + lengthMod16K (the +36
// marker bit cleared).
type BDOSSave struct {
	Record int             // the HRECORD-selected record at save time; -1 if none
	UIFA   [bdUIFALen]byte // the raw 48-byte UIFA as HSAVE read it at IX
	Name   string          // decoded filename (trailing spaces trimmed)
	Page   byte            // UIFA[31]
	Addr   uint16          // UIFA[32..33]
	Size   uint32          // pages*16384 + (lengthMod16K & 0x3FFF)
}

// SectorWrite is one captured HWSAD write: the 512 bytes written to a specific
// (record, linearSec) coordinate.
type SectorWrite struct {
	Record    int                // HRECORD-selected record at write time; -1 if none
	LinearSec int                // linear sector index (track*10 + (sector-1))
	Data      [bdSectorSize]byte // the 512 bytes written
}

// BDOSStore models the B-DOS record store for the netboot write-out. Construct
// with NewBDOSStore and attach with Machine.AttachBDOS.
type BDOSStore struct {
	selected     int           // last HRECORD selection; -1 = none selected yet
	saves        []BDOSSave    // captured HSAVEs, in order
	sectorWrites []SectorWrite // captured HWSAD writes, in order
	card         *CardModel    // nil = no card modelled; HRSAD returns all-zero
}

// NewBDOSStore returns a store with no record selected and no saves recorded.
func NewBDOSStore() *BDOSStore { return &BDOSStore{selected: -1} }

// Selected returns the most recently HRECORD-selected record (-1 if none).
func (s *BDOSStore) Selected() int { return s.selected }

// Saves returns the captured HSAVE operations, in order.
func (s *BDOSStore) Saves() []BDOSSave { return s.saves }

// SectorWrites returns the captured HWSAD sector-write operations, in order.
// Each entry carries the record that was selected when the write ran, the
// linear sector index (track*10 + (sector-1)), and the 512 bytes written.
func (s *BDOSStore) SectorWrites() []SectorWrite { return s.sectorWrites }

// AttachCard sets the card model the store uses for HRSAD reads. A nil card
// means HRSAD returns all-zero (no card present). Call before running Z80 code.
func (s *BDOSStore) AttachCard(c *CardModel) { s.card = c }

// AttachBDOS registers the store as the machine's RST 8 (B-DOS hook) handler, so
// the real bdos_select_record / bdos_save_hook / bdos_read_sector bodies run
// host-side.
func (mac *Machine) AttachBDOS(s *BDOSStore) {
	mac.setRSTHandler(rst8HookAddr, s.handle)
}

// handle is the RST 8 hook dispatcher. retAddr points at the inline hook-code
// byte (the `defb` after the `rst 8`); it returns retAddr+1 so the caller's `ret`
// resumes past it.
func (s *BDOSStore) handle(cpu *z80.CPU, mac *Machine, retAddr uint16) uint16 {
	hook := mac.m.ram[retAddr]
	switch hook {
	case bdHookHRECORD:
		// A=0 = select; HL = the record number (bdos_select_record set both).
		if cpu.AF.Hi == 0 {
			s.selected = int(cpu.HL.U16())
		}
	case bdHookHSAVE:
		// HSAVE reads the 48-byte UIFA at IX (bdos_save_hook set IX=&4B00).
		ix := cpu.IX
		var u [bdUIFALen]byte
		for i := 0; i < bdUIFALen; i++ {
			u[i] = mac.m.ram[ix+uint16(i)]
		}
		s.saves = append(s.saves, decodeBDOSSave(s.selected, u))
	case bdHookHGTHD:
		// Server-side lookup-by-name; the client write-out never issues it, so it
		// is not modelled (a no-op return). Add a DIFA deposit here if a server
		// write-out test ever needs it.
	case bdHookHWSAD:
		// HWSAD: write 512 bytes from the buffer at HL to (D=track, E=sector).
		// Register contract mirrors HRSAD (bdos15a.src.txt:528-537, shared rwsad
		// entry): D=track, E=sector, HL=source memory address. Linear sector index
		// = track*10 + (sector-1), matching the SAMDOS conv.de formula.
		track := int(cpu.DE.Hi)
		sector := int(cpu.DE.Lo)
		src := cpu.HL.U16()
		linearSec := track*bdSectorsPerTrack + (sector - 1)
		var data [bdSectorSize]byte
		for i := range data {
			data[i] = mac.m.ram[src+uint16(i)]
		}
		sw := SectorWrite{Record: s.selected, LinearSec: linearSec, Data: data}
		s.sectorWrites = append(s.sectorWrites, sw)
		// Also write into the CardModel so a subsequent HRSAD reads the data back.
		if s.card != nil {
			s.card.SetSector(s.selected, linearSec, data)
		}
	case bdHookHRSAD:
		// HRSAD: read 512 bytes at (D=track, E=sector) into the buffer at HL.
		// Linear sector index = track*10 + (sector-1), matching the SAMDOS conv.de
		// formula. The selected record determines which record's data to serve.
		track := int(cpu.DE.Hi)
		sector := int(cpu.DE.Lo)
		dest := cpu.HL.U16()
		linearSec := track*bdSectorsPerTrack + (sector - 1)
		var data [bdSectorSize]byte
		if s.card != nil {
			data = s.card.Sector(s.selected, linearSec)
		}
		for i, b := range data {
			mac.m.ram[dest+uint16(i)] = b
		}
	case bdHookListRead:
		// Card-absolute list-sector read: E = 1-based list-sector number, HL = dest.
		// Writes 512 bytes (32 × 16-byte record-list entries) to memory at HL.
		// This models the result of a raw SD CMD17 single-block read at the
		// card-absolute LBA of the list sector — NOT record-clamped, unlike HRSAD.
		// The hardware SPI ladder (ports &DC–&DF) is hardware-gated and not emulated
		// at the SPI level (docs/specs/trinity-record-detection-design.md §8).
		listSec := int(cpu.DE.Lo)
		dest := cpu.HL.U16()
		var data [bdSectorSize]byte
		if s.card != nil {
			data = s.card.ListSector(listSec)
		}
		for i, b := range data {
			mac.m.ram[dest+uint16(i)] = b
		}
	}
	return retAddr + 1 // skip the 1-byte inline hook code
}

// decodeBDOSSave decodes a captured UIFA the way bdos_difa_to_size / save_out_file
// encode it: name (space-trimmed), page, offset, and the 32-bit byte length.
func decodeBDOSSave(record int, u [bdUIFALen]byte) BDOSSave {
	name := strings.TrimRight(string(u[bdOffName:bdOffName+bdOffNameN]), " ")
	addr := uint16(u[bdOffLoad]) | uint16(u[bdOffLoad+1])<<8
	pages := uint32(u[bdOffPages])
	lenMod := (uint32(u[bdOffLength]) | uint32(u[bdOffLength+1])<<8) & 0x3FFF
	return BDOSSave{
		Record: record,
		UIFA:   u,
		Name:   name,
		Page:   u[bdOffPage],
		Addr:   addr,
		Size:   pages*16384 + lenMod,
	}
}
