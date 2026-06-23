package z80

// eeprom_internal_test.go — white-box tests of the 25LC1024 flash WRITE model
// (eeprom.go). These exercise the two datasheet-faithfulness properties the real
// driver (eeprom.asm) cannot reach, because write_chunk/write_index always issue a
// WREN and always page-align: (1) a WRITE is ignored unless the write-enable latch
// is set, and (2) the page-write address counter wraps within the 256-byte page.
// Driving the model directly is the only way to prove it is faithful to the part
// and NOT merely bent to the one access pattern the driver happens to use (i221).
//
// The driver-driven round-trip + backup/restore flow is the black-box companion in
// eeprom_write_test.go.

import "testing"

// runTxn drives one CS-framed SPI transaction: assert CS, clock each byte, deassert.
func (p *eeprom) runTxn(bytes ...byte) {
	p.csAssert()
	for _, b := range bytes {
		p.clock(b)
	}
	p.csDeassert()
}

// writeTxn builds a page-write transaction (cmd 0x02 + 24-bit MSB-first address +
// data) for the given flat address.
func writeTxn(addr int, data ...byte) []byte {
	tx := []byte{eepCmdWrite, byte(addr >> 16), byte(addr >> 8), byte(addr)}
	return append(tx, data...)
}

// readBack reads n bytes from the model at addr by driving a read transaction
// (cmd 0x03 + address + n dummy clocks), returning the latched bytes — the same
// path the driver's read_chunk uses.
func (p *eeprom) readBack(addr, n int) []byte {
	p.csAssert()
	p.clock(eepCmdRead)
	p.clock(byte(addr >> 16))
	p.clock(byte(addr >> 8))
	p.clock(byte(addr))
	out := make([]byte, n)
	for i := range out {
		p.clock(0) // dummy clock latches store[addr+i]
		out[i] = p.miso
	}
	p.csDeassert()
	return out
}

func TestEEPROMWriteIgnoredWithoutWREN(t *testing.T) {
	var p eeprom
	// A write with no preceding WREN must be ignored entirely (WEL clear).
	p.runTxn(writeTxn(0x300, 0xAA, 0xBB, 0xCC)...)
	if got := p.readBack(0x300, 3); got[0] != 0 || got[1] != 0 || got[2] != 0 {
		t.Fatalf("write without WREN programmed %v, want all zero (WEL gate failed)", got)
	}
}

func TestEEPROMWriteAfterWREN(t *testing.T) {
	var p eeprom
	p.runTxn(eepCmdWREN)                            // arm the latch
	p.runTxn(writeTxn(0x300, 0xAA, 0xBB, 0xCC)...) // now the write takes
	got := p.readBack(0x300, 3)
	if got[0] != 0xAA || got[1] != 0xBB || got[2] != 0xCC {
		t.Fatalf("write after WREN programmed %v, want [AA BB CC]", got)
	}
}

func TestEEPROMWriteEnableLatchClearsAfterEachWrite(t *testing.T) {
	var p eeprom
	p.runTxn(eepCmdWREN)
	p.runTxn(writeTxn(0x300, 0x11)...) // consumes the latch
	// A second write with no fresh WREN must be ignored: the WEL self-cleared.
	p.runTxn(writeTxn(0x301, 0x22)...)
	got := p.readBack(0x300, 2)
	if got[0] != 0x11 {
		t.Fatalf("first write = %#x, want 0x11", got[0])
	}
	if got[1] != 0 {
		t.Fatalf("second write (no fresh WREN) programmed %#x, want 0 (latch should self-clear)", got[1])
	}
}

func TestEEPROMWRDIClearsLatch(t *testing.T) {
	var p eeprom
	p.runTxn(eepCmdWREN) // arm
	p.runTxn(eepCmdWRDI) // then explicitly disarm
	p.runTxn(writeTxn(0x300, 0x55)...)
	if got := p.readBack(0x300, 1); got[0] != 0 {
		t.Fatalf("write after WRDI programmed %#x, want 0", got[0])
	}
}

func TestEEPROMPageWriteWrapsWithinPage(t *testing.T) {
	var p eeprom
	// 260 bytes written to a page-aligned address: the address counter wraps at the
	// 256-byte boundary, so bytes 256..259 overwrite offsets 0..3 of the SAME page
	// and the next page is never touched.
	data := make([]byte, 260)
	for i := range data {
		data[i] = byte(i)
	}
	p.runTxn(eepCmdWREN)
	p.runTxn(writeTxn(0x300, data...)...)

	page := p.readBack(0x300, 256)
	if page[0] != data[256] { // wrapped: data[256] overwrote offset 0
		t.Fatalf("page offset 0 = %#x, want %#x (wrapped data[256])", page[0], data[256])
	}
	if page[3] != data[259] { // wrapped: data[259] overwrote offset 3
		t.Fatalf("page offset 3 = %#x, want %#x (wrapped data[259])", page[3], data[259])
	}
	if page[4] != data[4] { // untouched by the wrap (written once)
		t.Fatalf("page offset 4 = %#x, want %#x", page[4], data[4])
	}
	if page[255] != data[255] {
		t.Fatalf("page offset 255 = %#x, want %#x", page[255], data[255])
	}
	// The following page must be pristine — the wrap stayed inside page 3.
	if next := p.readBack(0x400, 4); next[0] != 0 || next[1] != 0 || next[2] != 0 || next[3] != 0 {
		t.Fatalf("next page leaked %v, want all zero (page-wrap must not cross the boundary)", next)
	}
}

func TestEEPROMWriteAddressWrapsModuloDevice(t *testing.T) {
	var p eeprom
	// The 17-bit device address space wraps: a write addressed just past the top of
	// the device (0x20000 == device size) lands at offset 0 (A17 is don't-care).
	p.runTxn(eepCmdWREN)
	p.runTxn(writeTxn(eepDeviceSize, 0x7E)...)
	if got := p.readBack(0, 1); got[0] != 0x7E {
		t.Fatalf("write at device-size address programmed offset 0 = %#x, want 0x7E (modulo wrap)", got[0])
	}
}
