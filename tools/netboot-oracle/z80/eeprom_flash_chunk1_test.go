// eeprom_flash_chunk1_test.go — emulation verification of the bootblock flasher
// (item i135c): src/netboot/eeprom_flash_chunk1.asm writes the trinity-autoboot
// bootloader into Trinity EEPROM chunk 1 (the bootblock, device &2000) and
// verifies the write, reporting PASS/FAIL over the network (test_report "SATR").
//
// This proves the FLASHER itself — the new, never-run code path that drives the
// real eeprom.asm write_chunk against the faithful 25LC1024 write model
// (eeprom.go, i221) to land the bootloader bytes in chunk 1. It does NOT boot
// the patched image (the bootloader's record-3 auto-boot is verified separately
// by TestTrinityAutobootALHK, and booting it would need a record-3 SD image).
//
// TestFlashChunk1Pass: the write path lands bootloader.bin in chunk 1 ->
// status PASS, border green, EEPROMImage()[0x2000:0x2400] == bootloader.bin.
// TestFlashChunk1WriteFaultReportsFail: with the write path faulted, the
// read-back differs -> the payload reports FAIL, border red (negative control).
package z80_test

import (
	"bytes"
	"os"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

// requireFlashPrivate gates the flasher tests on SKIP_PRIVATE_TESTS. The flasher
// embeds the trinity-autoboot bootloader (Colin's boot block + our patches) — a
// private artifact kept out of the repo and CI (q55, 2026-06-26). So both the
// built payload (build/eeprom_flash_chunk1.bin) and the comparison bootloader.bin
// are absent in CI. Per the no-silent-skips policy (i253) the ONLY sanctioned
// skip is this explicit env gate; absent it, a missing artifact FAILs hard.
func requireFlashPrivate(t *testing.T) {
	t.Helper()
	if os.Getenv("SKIP_PRIVATE_TESTS") == "true" {
		t.Skipf("SKIP_PRIVATE_TESTS=true: trinity-autoboot bootloader is a private artifact (not in the repo/CI)")
	}
}

const (
	emFlashBin = "../../../build/eeprom_flash_chunk1.bin"
	emFlashMap = "../../../build/eeprom_flash_chunk1.map"

	// flashBootBin is the bootloader image the flasher embeds and writes; the
	// test compares the programmed chunk 1 against these exact bytes.
	flashBootBin = "../../../../trinity-autoboot/build/bootloader.bin"

	// chunk 1's device byte address: get_chunk maps n to (28 + n*4)<<8, so
	// chunk 1 = 32<<8 = 0x2000 (= the bootblock the ROM runs at boot).
	emFlashChunk1Dev = 0x2000
	emFlashChunk1End = emFlashChunk1Dev + 1024
)

func loadFlashBootloader(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(flashBootBin)
	if err != nil {
		t.Fatalf("bootloader not built — run `cd ~/git/trinity-autoboot && make`: %v", err)
	}
	if len(b) != 1024 {
		t.Fatalf("bootloader.bin is %d bytes, want 1024", len(b))
	}
	return b
}

// loadFlashChunk1 loads the flasher payload and an ENC28J60 whose flash EEPROM is
// all-0xFF (an unprogrammed device, distinct from the bootloader bytes so the
// write is observable).
func loadFlashChunk1(t *testing.T) (*z80h.Machine, *z80h.ENC28J60) {
	t.Helper()
	mac, err := z80h.Load(emFlashBin, emFlashMap)
	if err != nil {
		t.Fatalf("eeprom_flash_chunk1 not built (%s); run `make netboot-eeprom-flash-chunk1`: %v", emFlashBin, err)
	}
	enc := z80h.NewENC28J60()
	img := make([]byte, 131072)
	for i := range img {
		img[i] = 0xFF
	}
	enc.LoadEEPROMImage(img)
	mac.AttachIO(enc)
	return mac, enc
}

func TestFlashChunk1Pass(t *testing.T) {
	requireFlashPrivate(t)
	boot := loadFlashBootloader(t)
	mac, enc := loadFlashChunk1(t)

	res, err := mac.Call("eeprom_flash_main")
	if err != nil {
		t.Fatalf("run eeprom_flash_main: %v", err)
	}
	if !res.Halted {
		t.Fatalf("payload did not halt (PC=&%04X)", res.PC)
	}

	rep, ok := parseSATR(enc.TXFrames())
	if !ok {
		t.Fatalf("no SATR report frame was transmitted (%d frames)", len(enc.TXFrames()))
	}
	if rep.version != 1 {
		t.Errorf("report version = %d, want 1", rep.version)
	}
	if rep.testID != 2 {
		t.Errorf("report test_id = %d, want 2 (bootblock flash)", rep.testID)
	}
	if rep.status != 0 {
		t.Errorf("report status = %d, want 0 (PASS); detail = %v", rep.status, rep.detail)
	}
	if len(rep.detail) >= 2 {
		if rep.detail[0] != 1 {
			t.Errorf("report chunk = %d, want 1 (bootblock)", rep.detail[0])
		}
		if rep.detail[1] != 0 {
			t.Errorf("report fail-phase = %d, want 0 (no failure)", rep.detail[1])
		}
	}

	if b, written := enc.LastBorder(); !written || b != 4 {
		t.Errorf("border = %d (written=%v), want 4 (green = pass)", b, written)
	}

	// The decisive assertion: chunk 1 in the EEPROM now holds the bootloader,
	// byte-for-byte. This is what the real flash lands in the bootblock.
	img := enc.EEPROMImage()
	got := img[emFlashChunk1Dev:emFlashChunk1End]
	if !bytes.Equal(got, boot) {
		for i := range boot {
			if got[i] != boot[i] {
				t.Fatalf("chunk 1 device &%05X = &%02X, want &%02X (bootloader.bin) — write did not land", emFlashChunk1Dev+i, got[i], boot[i])
			}
		}
	}
}

func TestFlashChunk1WriteFaultReportsFail(t *testing.T) {
	requireFlashPrivate(t)
	mac, enc := loadFlashChunk1(t)
	enc.SetEEPROMWriteFault(true) // simulate a dead write path

	res, err := mac.Call("eeprom_flash_main")
	if err != nil {
		t.Fatalf("run eeprom_flash_main: %v", err)
	}
	if !res.Halted {
		t.Fatalf("payload did not halt (PC=&%04X)", res.PC)
	}

	rep, ok := parseSATR(enc.TXFrames())
	if !ok {
		t.Fatalf("no SATR report frame was transmitted (%d frames)", len(enc.TXFrames()))
	}
	if rep.status != 1 {
		t.Errorf("report status = %d, want 1 (FAIL) under a faulted write path", rep.status)
	}
	if len(rep.detail) >= 2 && rep.detail[1] != 1 {
		t.Errorf("report fail-phase = %d, want 1 (read-back verify)", rep.detail[1])
	}
	if b, written := enc.LastBorder(); !written || b != 2 {
		t.Errorf("border = %d (written=%v), want 2 (red = fail)", b, written)
	}
}
