// bdos_seam_test.go — host-verification of the netboot storage seam: the
// UIFA/DIFA field arithmetic (src/netboot/bdos_seam.asm) that glues the i83
// TFTP server (serve by name) + the i82 TFTP client (write by name) to the
// B-DOS hooks. It runs the real Z80 routines under the flat-memory koron-go/z80
// harness (built with NETBOOT_HOSTTEST, so the RST 8 hook dispatch is excluded)
// and byte-compares the built UIFA + decoded size against the Go authority
// (tools/netboot-oracle/bdos).
//
// THE HONESTY LINE: only the field arithmetic is exercised here — pure memory
// build/decode, which is genuinely host-verifiable. The actual HGTHD/HLOAD/
// HSAVE/HRECORD RST 8 dispatch into B-DOS is NOT in this build and stays
// UNVERIFIED until real Trinity hardware (CLAUDE.md §5). Emulation-verified is
// not hardware-verified.
package z80_test

import (
	"bytes"
	"os"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/bdos"
	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	bdosBinPath = "../../../build/netboot_bdos_seam.bin"
	bdosMapPath = "../../../build/netboot_bdos_seam.map"
)

func loadBDOS(t *testing.T) *z80h.Machine {
	t.Helper()
	if _, err := os.Stat(bdosBinPath); err != nil {
		t.Skipf("bdos_seam binary not built (%s); run `make netboot-bdos-seam`", bdosBinPath)
	}
	mac, err := z80h.Load(bdosBinPath, bdosMapPath)
	if err != nil {
		t.Fatalf("load bdos_seam: %v", err)
	}
	return mac
}

// writeName writes a NUL-terminated filename into Z80 memory and points
// BD_NAME_PTR at it. It uses the BD_DIFA buffer's tail as scratch (the name
// routines don't touch BD_DIFA), returning the chosen scratch address.
func writeName(t *testing.T, mac *z80h.Machine, name string) {
	t.Helper()
	scratch := symAddr(t, mac, "BD_DIFA") // reuse the DIFA buffer as name scratch
	mac.Write(scratch, append([]byte(name), 0))
	mac.WriteU16LE(symAddr(t, mac, "BD_NAME_PTR"), scratch)
}

// TestBDOSNameToUIFA asserts the Z80 bdos_name_to_uifa builds the same 48-byte
// UIFA name block as the Go authority bdos.NameToUIFA, for several name shapes.
func TestBDOSNameToUIFA(t *testing.T) {
	for _, name := range []string{"config.txt", "kernel8", "verylongfilename", ""} {
		mac := loadBDOS(t)
		writeName(t, mac, name)
		if _, err := mac.Call("bdos_name_to_uifa"); err != nil {
			t.Fatalf("call bdos_name_to_uifa(%q): %v", name, err)
		}
		got := mac.Read(symAddr(t, mac, "BD_UIFA"), bdos.UIFALen)
		want := bdos.NameToUIFA(name)
		if !bytes.Equal(got, want[:]) {
			t.Errorf("UIFA for %q != Go authority\n  z80 %x\n  go  %x", name, got, want)
		}
	}
}

// TestBDOSDifaToSize asserts the Z80 bdos_difa_to_size decodes a DIFA's length
// fields to the same 32-bit byte length as the Go authority bdos.DifaToSize.
func TestBDOSDifaToSize(t *testing.T) {
	cases := []struct {
		pages     byte
		lengthMod uint16
		marker    bool
	}{
		{0, 0, false},
		{0, 300, true},
		{1, 0, false},
		{2, 300, true},
		{3, 16383, false},
		{255, 16383, true},
	}
	for _, c := range cases {
		mac := loadBDOS(t)
		var difa [bdos.UIFALen]byte
		difa[bdos.OffPages] = c.pages
		difa[bdos.OffLength] = byte(c.lengthMod)
		hi := byte(c.lengthMod >> 8)
		if c.marker {
			hi |= bdos.DifaLenMarker
		}
		difa[bdos.OffLength+1] = hi
		mac.Write(symAddr(t, mac, "BD_DIFA"), difa[:])

		if _, err := mac.Call("bdos_difa_to_size"); err != nil {
			t.Fatalf("call bdos_difa_to_size: %v", err)
		}
		got := mac.Read(symAddr(t, mac, "BD_SIZE"), 4)
		want := bdos.DifaToSize(difa)
		gotU := uint32(got[0]) | uint32(got[1])<<8 | uint32(got[2])<<16 | uint32(got[3])<<24
		if gotU != want {
			t.Errorf("size(pages=%d,mod=%d,marker=%v) z80=%d go=%d", c.pages, c.lengthMod, c.marker, gotU, want)
		}
	}
}

// TestBDOSFillSaveUIFA asserts the Z80 bdos_fill_save_uifa builds the same HSAVE
// UIFA as the Go authority bdos.SaveUIFA, for several sizes.
func TestBDOSFillSaveUIFA(t *testing.T) {
	const page = 5
	const addr = 0x8000
	for _, size := range []uint32{0, 511, 512, 16383, 16384, 16385, 40000, 65535} {
		mac := loadBDOS(t)
		writeName(t, mac, "recovery")
		mac.Write(symAddr(t, mac, "BD_SAVE_PAGE"), []byte{page})
		mac.WriteU16LE(symAddr(t, mac, "BD_SAVE_ADDR"), addr)
		mac.WriteU16LE(symAddr(t, mac, "BD_SAVE_SIZE"), uint16(size))

		if _, err := mac.Call("bdos_fill_save_uifa"); err != nil {
			t.Fatalf("call bdos_fill_save_uifa(size=%d): %v", size, err)
		}
		got := mac.Read(symAddr(t, mac, "BD_UIFA"), bdos.UIFALen)
		want, err := bdos.SaveUIFA("recovery", page, addr, size)
		if err != nil {
			t.Fatalf("Go SaveUIFA(%d): %v", size, err)
		}
		if !bytes.Equal(got, want[:]) {
			t.Errorf("save UIFA for size %d != Go authority\n  z80 %x\n  go  %x", size, got, want)
		}
	}
}
