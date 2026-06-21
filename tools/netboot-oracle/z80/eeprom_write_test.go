package z80_test

// eeprom_write_test.go — black-box host verification of the on-SAM EEPROM WRITE
// path (i221). It drives the REAL vendored write routines (write_chunk /
// write_index, src/netboot/eeprom.asm) against the 25LC1024 flash write model
// (eeprom.go) and confirms a subsequent REAL read (read_chunk) returns exactly the
// bytes written — a full write->read round-trip through the actual driver. It also
// rehearses the i135c backup/restore flow (snapshot, destructive overwrite, verify
// changed, restore, verify originals back) so the destructive hardware flash is no
// longer the first-ever execution of write_chunk.
//
// The write reuses the samboot_config host-test binary (it includes eeprom.asm, so
// its map carries write_chunk/write_index/read_chunk + the value/chunk/part data
// symbols). The wire push to real hardware stays gated (CLAUDE.md §5): this is
// emulation verification of the digital SPI write protocol, not the flash silicon.

import (
	"bytes"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

// chunkFlatAddr is the flat EEPROM byte address read_chunk/write_256 use for chunk
// number n: get_chunk maps n to (28 + n*4)<<8 (eeprom.asm). value 1 -> 0x2000.
func chunkFlatAddr(n int) int { return (28 + n*4) << 8 }

// indexFlatAddr is the flat address write_index/find_index use for chunk n's
// 64-byte index entry: get_index maps n to 64*(n-1).
func indexFlatAddr(n int) int { return 64 * (n - 1) }

// writeChunk loads the 1 KB pattern into the `chunk` buffer, sets `value`, and runs
// the real write_chunk routine against enc's flash model.
func writeChunk(t *testing.T, mac *z80h.Machine, value int, data []byte) {
	t.Helper()
	if len(data) != 1024 {
		t.Fatalf("chunk data is %d bytes, want 1024", len(data))
	}
	mac.Write(symAddr(t, mac, "value"), []byte{byte(value)})
	mac.Write(symAddr(t, mac, "chunk"), data)
	if _, err := mac.Call("write_chunk"); err != nil {
		t.Fatalf("call write_chunk: %v", err)
	}
}

// readChunk zeroes the `chunk` buffer, sets `value`, runs the real read_chunk, and
// returns the 1 KB it read back from the flash model.
func readChunk(t *testing.T, mac *z80h.Machine, value int) []byte {
	t.Helper()
	mac.Write(symAddr(t, mac, "chunk"), make([]byte, 1024))
	mac.Write(symAddr(t, mac, "value"), []byte{byte(value)})
	if _, err := mac.Call("read_chunk"); err != nil {
		t.Fatalf("call read_chunk: %v", err)
	}
	return mac.Read(symAddr(t, mac, "chunk"), 1024)
}

func pattern(n, seed int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*7 + seed)
	}
	return b
}

// TestEEPROMWriteChunkRoundTrip drives the real write_chunk then the real read_chunk
// and asserts the bytes survive the round-trip, and that the model's flat store
// holds them at the chunk's device address (the write landed where the read expects).
func TestEEPROMWriteChunkRoundTrip(t *testing.T) {
	mac := loadSambootCfg(t)
	enc := z80h.NewENC28J60()
	mac.AttachIO(enc)

	const value = 7
	want := pattern(1024, 3)
	writeChunk(t, mac, value, want)

	// (a) the model store holds the bytes at the chunk's flat address.
	img := enc.EEPROMImage()
	addr := chunkFlatAddr(value)
	if got := img[addr : addr+1024]; !bytes.Equal(got, want) {
		t.Fatalf("flash store at %#x mismatch after write_chunk", addr)
	}
	// (b) the real read_chunk reads them back identically.
	if got := readChunk(t, mac, value); !bytes.Equal(got, want) {
		t.Fatalf("read_chunk round-trip mismatch for value %d", value)
	}
}

// TestEEPROMWriteIndexRoundTrip drives the real write_index (the 64-byte header:
// part, total, name, description) and confirms the model store holds it at the
// index slot find_index would search.
func TestEEPROMWriteIndexRoundTrip(t *testing.T) {
	mac := loadSambootCfg(t)
	enc := z80h.NewENC28J60()
	mac.AttachIO(enc)

	const value = 4 // index addr 64*3 = 192 (page-aligned; within page 0)
	// The 64-byte header lives contiguously from `part`: part(1) total(1) name(16)
	// description(46). Set it as one block and write it.
	header := make([]byte, 64)
	header[0] = 1 // part
	header[1] = 1 // total
	copy(header[2:18], "TEST CHUNK NAME ") // 16 bytes
	for i := 18; i < 64; i++ {
		header[i] = byte(i) // description filler
	}
	mac.Write(symAddr(t, mac, "part"), header)
	mac.Write(symAddr(t, mac, "value"), []byte{byte(value)})
	if _, err := mac.Call("write_index"); err != nil {
		t.Fatalf("call write_index: %v", err)
	}

	img := enc.EEPROMImage()
	addr := indexFlatAddr(value)
	if got := img[addr : addr+64]; !bytes.Equal(got, header) {
		t.Fatalf("flash store at index addr %#x mismatch after write_index:\n got %v\nwant %v", addr, got[:18], header[:18])
	}
}

// TestEEPROMBackupRestoreFlow rehearses the i135c safety flow entirely in
// emulation: snapshot the device, run a destructive write_chunk, confirm the
// targeted chunk changed (and the rest did not), restore the snapshot, and confirm
// every original byte is back and the real read_chunk again returns the originals.
func TestEEPROMBackupRestoreFlow(t *testing.T) {
	mac := loadSambootCfg(t)
	enc := z80h.NewENC28J60()
	mac.AttachIO(enc)

	const value = 5
	addr := chunkFlatAddr(value)

	// A full 128 KB device pre-loaded with known "captured" contents.
	original := pattern(131072, 1)
	enc.LoadEEPROMImage(original)
	backup := enc.EEPROMImage() // the restore copy i135c keeps

	// The original chunk bytes, read through the real driver (the pre-flash read).
	origChunk := readChunk(t, mac, value)
	if !bytes.Equal(origChunk, original[addr:addr+1024]) {
		t.Fatalf("pre-write read_chunk disagrees with the loaded image at %#x", addr)
	}

	// Destructive overwrite.
	newData := pattern(1024, 99)
	writeChunk(t, mac, value, newData)

	after := enc.EEPROMImage()
	if bytes.Equal(after[addr:addr+1024], original[addr:addr+1024]) {
		t.Fatalf("write_chunk did not change the target chunk at %#x", addr)
	}
	if !bytes.Equal(after[addr:addr+1024], newData) {
		t.Fatalf("write_chunk wrote unexpected bytes at %#x", addr)
	}
	// Nothing outside the 1 KB chunk may have moved.
	if !bytes.Equal(after[:addr], backup[:addr]) || !bytes.Equal(after[addr+1024:], backup[addr+1024:]) {
		t.Fatalf("write_chunk touched bytes outside the target chunk")
	}

	// Restore from backup and confirm a byte-for-byte recovery.
	enc.LoadEEPROMImage(backup)
	if restored := enc.EEPROMImage(); !bytes.Equal(restored, original) {
		t.Fatalf("restore did not recover the original 128 KB image")
	}
	if got := readChunk(t, mac, value); !bytes.Equal(got, origChunk) {
		t.Fatalf("read_chunk after restore did not return the original chunk bytes")
	}
}
