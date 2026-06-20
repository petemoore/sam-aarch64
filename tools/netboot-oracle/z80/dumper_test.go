// dumper_test.go — the i173 host-verification of the SAMBOOT ROM+EEPROM dumper's
// EEPROM-read + serve path. It loads the dumper host-test binary
// (src/netboot/netboot_dumper.asm, built -D NETBOOT_HOSTTEST=1), programs known
// 1 KB chunks into the emulated Trinity flash (ProgramChunk), drives a bare RRQ
// for a region file (eep0.bin) through the real serve_serve_once dispatcher with
// the dumper_refresh_region hook armed, and asserts the streamed DATA blocks
// reconstruct the programmed 16 KB byte-for-byte.
//
// This is an IDENTITY round-trip — the captured bytes are the deliverable, so
// there is no Go authority to compare against; the assertion is that what the
// EEPROM was programmed with is exactly what the dumper streams. It proves the
// emulation-verifiable half of i173: the eeprom.asm read loop (dumper_read_
// eeprom_region) fills STAGE correctly and the serve loop streams it intact over
// a full multi-block transfer.
//
// NOT exercised here (HARDWARE-FIRST, guarded out of the host-test build): the
// ROM-paging read (rom0.bin/rom1.bin). The flat harness has no patched-ROM
// contents and does not act on LMPR/HMPR, so there is nothing to assert the ROM
// ldir against — those bytes are captured + diffed on real hardware (i87a/i87b).
// Emulation-verified is not hardware-verified (CLAUDE.md §5).
package z80_test

import (
	"bytes"
	"os"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/frame"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/tftp"
	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	dumperBinPath = "../../../build/netboot_dumper.bin"
	dumperMapPath = "../../../build/netboot_dumper.map"

	dumperRegionBytes = 16384 // one served region = 16 KB
	dumperChunkBytes  = 1024
	dumperRegionChunk = dumperRegionBytes / dumperChunkBytes // 16 chunks per region
)

func loadDumper(t *testing.T) *z80h.Machine {
	t.Helper()
	if _, err := os.Stat(dumperBinPath); err != nil {
		t.Skipf("netboot_dumper binary not built (%s); run `make netboot-dumper`", dumperBinPath)
	}
	mac, err := z80h.Load(dumperBinPath, dumperMapPath)
	if err != nil {
		t.Fatalf("load netboot_dumper: %v", err)
	}
	return mac
}

// fillDumperConfig writes CONFIG_* and copies the dumper's own region STORE +
// SRC_TABLE templates (assembled into the host-test image) into the live tables
// resolve / resolve_src walk — exactly what dumper_provision does on hardware,
// driven here from the test since dumper_provision is excluded from the host-test
// build.
func fillDumperConfig(t *testing.T, mac *z80h.Machine) {
	t.Helper()
	be16 := func(v uint16) []byte { return []byte{byte(v >> 8), byte(v & 0xff)} }
	mac.Write(symAddr(t, mac, "CONFIG_SERVERMAC"), dumperServerMAC[:])
	mac.Write(symAddr(t, mac, "CONFIG_SERVERIP"), dumperServerIP[:])
	mac.Write(symAddr(t, mac, "CONFIG_SERVERTID"), be16(dumperServerTID))

	// Copy the assembled STORE/SRC_TABLE templates into the live tables. The
	// store template ends exactly where the src template begins (they are laid
	// out contiguously), so dump_src_tmpl is the store template's end marker —
	// pyz80's map collapses dump_store_tmpl_end onto dump_src_tmpl (same address),
	// so we use the surviving name.
	copyTemplate(t, mac, "dump_store_tmpl", "dump_src_tmpl", "STORE")
	copyTemplate(t, mac, "dump_src_tmpl", "dump_src_tmpl_end", "SRC_TABLE")
}

// copyTemplate copies [startSym, endSym) of the loaded image to dstSym.
func copyTemplate(t *testing.T, mac *z80h.Machine, startSym, endSym, dstSym string) {
	t.Helper()
	start := symAddr(t, mac, startSym)
	end := symAddr(t, mac, endSym)
	if end <= start {
		t.Fatalf("template %s..%s has non-positive length", startSym, endSym)
	}
	mac.Write(symAddr(t, mac, dstSym), mac.Read(start, int(end-start)))
}

func initDumperDriver(t *testing.T, mac *z80h.Machine, enc *z80h.ENC28J60) {
	t.Helper()
	mac.AttachIO(enc)
	macAddr := symAddr(t, mac, "CONFIG_SERVERMAC")
	mac.Write(macAddr, dumperServerMAC[:])
	res, err := mac.CallEntry("drv_init", z80h.Entry{HL: macAddr})
	if err != nil {
		t.Fatalf("call drv_init: %v", err)
	}
	if res.BC != 1 {
		t.Fatalf("drv_init returned BC=%d, want 1", res.BC)
	}
}

var (
	dumperServerMAC = [6]byte{0x02, 0x00, 0x00, 0x00, 0x00, 0x01}
	dumperServerIP  = [4]byte{192, 0, 2, 1}
	dumperClientMAC = [6]byte{0x02, 0x00, 0x00, 0x00, 0x00, 0x44}
	dumperClientIP  = [4]byte{192, 0, 2, 44}
)

const (
	dumperServerTID = 40136
	dumperClientTID = 30574
)

// dumperRRQ builds the client's bare RRQ (no options) for a region file.
func dumperRRQ(name string) []byte {
	return frame.BuildUDPFrame(frame.UDP{
		DstMAC: dumperServerMAC, SrcMAC: dumperClientMAC,
		SrcIP: dumperClientIP, DstIP: dumperServerIP,
		SrcPort: dumperClientTID, DstPort: 69,
		Payload: tftp.BuildRRQ(name, "octet", nil),
	})
}

// dumperAck builds the client's ACK frame (client TID -> server TID).
func dumperAck(block uint16) []byte {
	return frame.BuildUDPFrame(frame.UDP{
		DstMAC: dumperServerMAC, SrcMAC: dumperClientMAC,
		SrcIP: dumperClientIP, DstIP: dumperServerIP,
		SrcPort: dumperClientTID, DstPort: dumperServerTID,
		Payload: tftp.BuildACK(block),
	})
}

// dumperServe injects req (if non-nil) and runs one serve_serve_once, returning
// the single frame the driver transmitted (or nil if it sent nothing). Mirrors
// serveDemo (netboot_serve_test.go).
func dumperServe(t *testing.T, mac *z80h.Machine, enc *z80h.ENC28J60, req []byte) []byte {
	t.Helper()
	before := len(enc.TXFrames())
	if req != nil {
		enc.InjectRX(req)
	}
	res, err := mac.Call("serve_serve_once")
	if err != nil {
		t.Fatalf("call serve_serve_once: %v", err)
	}
	tx := enc.TXFrames()
	if res.BC == 0 {
		if len(tx) != before {
			t.Fatalf("serve_serve_once returned BC=0 but transmitted a frame")
		}
		return nil
	}
	if len(tx) != before+1 {
		t.Fatalf("serve_serve_once transmitted %d frames, want 1", len(tx)-before)
	}
	out := tx[len(tx)-1]
	if int(res.BC) != len(out) {
		t.Fatalf("serve_serve_once returned BC=%d but the wire frame is %d bytes", res.BC, len(out))
	}
	return out
}

// streamRegion drives a full bare-RRQ transfer of one region file and returns the
// concatenated DATA payloads (the reconstructed region bytes). It also returns the
// number of DATA blocks the transfer took, so a caller can assert the multi-block
// cadence completes.
func streamRegion(t *testing.T, mac *z80h.Machine, enc *z80h.ENC28J60, name string) (data []byte, blocks int) {
	t.Helper()
	// Bare RRQ -> DATA block 1 directly (RFC 2347, no OACK).
	out := dumperServe(t, mac, enc, dumperRRQ(name))
	for {
		if out == nil {
			t.Fatalf("%s: transfer ended early after %d blocks", name, blocks)
		}
		_, payload, perr := tftp.ParseDATA(udpPayload(t, out))
		if perr != nil {
			t.Fatalf("%s: block %d is not a DATA packet: %v", name, blocks+1, perr)
		}
		data = append(data, payload...)
		blocks++
		short := len(payload) < 512 // a short (or zero) block is the final one
		// ACK this block; the server sends the next (or finishes after the short
		// final block's ACK).
		out = dumperServe(t, mac, enc, dumperAck(uint16(blocks)))
		if short {
			if out != nil {
				t.Fatalf("%s: ACK of the short final block should end the transfer, got %x", name, out)
			}
			break
		}
	}
	return data, blocks
}

// programRegionChunks programs the 16 chunks backing region n (chunk values
// n*16+1 .. n*16+16) with the given 16 KB of bytes and returns those bytes.
func programRegionChunks(enc *z80h.ENC28J60, region int, want []byte) {
	for i := 0; i < dumperRegionChunk; i++ {
		value := region*dumperRegionChunk + i + 1 // chunk values are 1-based
		enc.ProgramChunk(value, want[i*dumperChunkBytes:(i+1)*dumperChunkBytes])
	}
}

// distinctRegionBytes builds 16 KB of bytes whose value depends on both the chunk
// index and the byte offset, so a mis-ordered or partial read is caught (not just
// a constant fill).
func distinctRegionBytes() []byte {
	b := make([]byte, dumperRegionBytes)
	for i := range b {
		chunk := i / dumperChunkBytes
		b[i] = byte((chunk*131 + i*7 + 3) & 0xff)
	}
	return b
}

// TestDumperEEPROMRegionRoundTrip is the headline i173 host check: a region's
// 16 known EEPROM chunks, served over a full bare-RRQ TFTP transfer, reconstruct
// byte-for-byte. It exercises the real eeprom.asm read loop (dumper_read_eeprom_
// region) + the dumper_refresh_region hook + the serve loop, end to end.
func TestDumperEEPROMRegionRoundTrip(t *testing.T) {
	mac := loadDumper(t)
	fillDumperConfig(t, mac)
	enc := z80h.NewENC28J60()
	initDumperDriver(t, mac, enc)

	want := distinctRegionBytes()
	programRegionChunks(enc, 0, want) // region 0 -> eep0.bin (chunk values 1..16)

	got, blocks := streamRegion(t, mac, enc, "eep0.bin")

	if len(got) != dumperRegionBytes {
		t.Fatalf("streamed %d bytes, want %d (region size)", len(got), dumperRegionBytes)
	}
	if !bytes.Equal(got, want) {
		// Localise the first mismatch for a useful failure.
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("eep0.bin byte %d (chunk %d, offset %d) = 0x%02x, want 0x%02x",
					i, i/dumperChunkBytes, i%dumperChunkBytes, got[i], want[i])
			}
		}
	}
	// 16 KB at the 512-byte default blksize = 32 full blocks; the dumper must
	// terminate with a 33rd zero-length block (RFC 1350 exact-multiple rule).
	const fullBlocks = dumperRegionBytes / 512
	if blocks != fullBlocks+1 {
		t.Errorf("eep0.bin transfer took %d DATA blocks, want %d (32 full + 1 zero-length terminator)",
			blocks, fullBlocks+1)
	}
}

// TestDumperEEPROMUnprogrammedZeros is the negative control: an EEPROM region
// whose chunks were never programmed streams back all zeros (unprogrammed flash
// cells read 0 in the emulation), confirming the read path is not fabricating
// data and the round-trip would catch a stuck-nonzero read.
func TestDumperEEPROMUnprogrammedZeros(t *testing.T) {
	mac := loadDumper(t)
	fillDumperConfig(t, mac)
	enc := z80h.NewENC28J60()
	initDumperDriver(t, mac, enc)

	// Program nothing for region 1 (eep1.bin, chunk values 17..32).
	got, _ := streamRegion(t, mac, enc, "eep1.bin")
	if len(got) != dumperRegionBytes {
		t.Fatalf("streamed %d bytes, want %d", len(got), dumperRegionBytes)
	}
	for i, b := range got {
		if b != 0 {
			t.Fatalf("unprogrammed eep1.bin byte %d = 0x%02x, want 0x00", i, b)
		}
	}
}

// TestDumperEEPROMSecondRegionOffset confirms the region->chunk-value mapping is
// correct for a region other than 0: region 2 (eep2.bin) reads chunk values
// 33..48, so programming those (and NOT region 0's) yields region 2's bytes —
// catching an off-by-one or a region-index-ignored read.
func TestDumperEEPROMSecondRegionOffset(t *testing.T) {
	mac := loadDumper(t)
	fillDumperConfig(t, mac)
	enc := z80h.NewENC28J60()
	initDumperDriver(t, mac, enc)

	want := distinctRegionBytes()
	programRegionChunks(enc, 2, want) // region 2 -> eep2.bin (chunk values 33..48)

	got, _ := streamRegion(t, mac, enc, "eep2.bin")
	if !bytes.Equal(got, want) {
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("eep2.bin byte %d = 0x%02x, want 0x%02x (region->value mapping off?)",
					i, got[i], want[i])
			}
		}
	}
	// And eep0.bin (region 0, values 1..16, never programmed) must read zeros —
	// proving region 2's data did not bleed into region 0.
	got0, _ := streamRegion(t, mac, enc, "eep0.bin")
	for i, b := range got0 {
		if b != 0 {
			t.Fatalf("eep0.bin byte %d = 0x%02x after programming region 2, want 0x00", i, b)
		}
	}
}
