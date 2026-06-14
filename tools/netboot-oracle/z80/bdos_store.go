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
// handler (harness.go's rstHandlers) that models the two hooks the write-out uses:
//
//   HRECORD (&9C): bdos_select_record does `ld l,a; ld h,0; xor a; rst 8; defb &9C`
//     — A=0 = the select sub-function, HL = the record number. The store records
//     the selected record. (This is where the i119 bug lives: client_main selects
//     record 0 = the floppy, not a Trinity record n>=1 — now visible in emulation.)
//   HSAVE (132): bdos_save_hook copies the built UIFA to &4B00, sets IX there, and
//     `rst 8; defb 132`. The store captures + decodes the 48-byte UIFA at IX (the
//     filename, source page/offset, and byte length), tagged with the record that
//     was selected when the save ran.
//
// THE HONESTY LINE (CLAUDE.md §5): this models the *digital dispatch* (which hook,
// with which UIFA, against which selected record) so the write-out's logic runs
// and is asserted host-side. It does NOT persist bytes to a real medium, model
// B-DOS error returns (e.g. "Invalid record" on an unstamped record), or replace
// the real RST 8 — that stays gated on real Trinity hardware. Emulation-verified
// is not hardware-verified.

import (
	"strings"

	"github.com/koron-go/z80"
)

// B-DOS hook codes + UIFA layout (mirror bdos_seam.asm BD_HOOK_* / BD_OFF_*).
const (
	rst8HookAddr = 0x0008 // the RST 8 vector the SAMDOS/B-DOS hooks dispatch through

	bdHookHRECORD = 0x9C // record select (156)
	bdHookHGTHD   = 129  // get file header (lookup by name) — server-side, not modelled here
	bdHookHSAVE   = 132  // save whole file

	bdUIFALen = 48 // the UIFA HSAVE reads at IX (bdos_save_hook stages it at &4B00)

	bdOffName   = 1 // 10-byte space-padded filename
	bdOffNameN  = 10
	bdOffPage   = 31 // HSAVE source page (low 5 bits)
	bdOffLoad   = 32 // HSAVE source offset (LE word)
	bdOffPages  = 34 // pages-count
	bdOffLength = 35 // length-mod-16K (LE word); bit 15 of the word is a marker
)

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

// BDOSStore models the B-DOS record store for the netboot write-out. Construct
// with NewBDOSStore and attach with Machine.AttachBDOS.
type BDOSStore struct {
	selected int        // last HRECORD selection; -1 = none selected yet
	saves    []BDOSSave // captured HSAVEs, in order
}

// NewBDOSStore returns a store with no record selected and no saves recorded.
func NewBDOSStore() *BDOSStore { return &BDOSStore{selected: -1} }

// Selected returns the most recently HRECORD-selected record (-1 if none).
func (s *BDOSStore) Selected() int { return s.selected }

// Saves returns the captured HSAVE operations, in order.
func (s *BDOSStore) Saves() []BDOSSave { return s.saves }

// AttachBDOS registers the store as the machine's RST 8 (B-DOS hook) handler, so
// the real bdos_select_record / bdos_save_hook bodies run host-side.
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
