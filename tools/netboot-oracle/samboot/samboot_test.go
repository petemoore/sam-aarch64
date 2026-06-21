package samboot

import "testing"

// TestChunkName pins the BIOS config chunk name to exactly 16 bytes — the
// eeprom.asm find_index key length. A name that is not 16 bytes would miss on
// hardware (the Z80 reader's samboot_cfg_name is the same 16 bytes).
func TestChunkName(t *testing.T) {
	if len(ChunkName) != 16 {
		t.Fatalf("ChunkName %q is %d bytes, want 16", ChunkName, len(ChunkName))
	}
}

// TestEncodeLayout asserts the fixed byte layout (version, mode, LE record, then
// reserved zeros) the Z80 reader src/netboot/samboot_config.asm parses.
func TestEncodeLayout(t *testing.T) {
	boot := Boot(0x0102).Encode()
	if len(boot) != ChunkBytes {
		t.Fatalf("Encode len = %d, want %d", len(boot), ChunkBytes)
	}
	if boot[0] != Version {
		t.Errorf("version byte = 0x%02x, want 0x%02x", boot[0], Version)
	}
	if boot[1] != ModeBoot {
		t.Errorf("mode byte = 0x%02x, want ModeBoot 0x%02x", boot[1], ModeBoot)
	}
	if boot[2] != 0x02 || boot[3] != 0x01 {
		t.Errorf("record bytes = %02x %02x, want 02 01 (LE 0x0102)", boot[2], boot[3])
	}
	for i := 4; i < len(boot); i++ {
		if boot[i] != 0 {
			t.Fatalf("reserved byte %d = 0x%02x, want 0", i, boot[i])
		}
	}

	none := None().Encode()
	if none[0] != Version || none[1] != ModeNone || none[2] != 0 || none[3] != 0 {
		t.Errorf("none layout = %02x %02x %02x %02x, want version,ModeNone,0,0", none[0], none[1], none[2], none[3])
	}
}

// TestRoundTrip confirms every config survives Encode -> Decode unchanged.
func TestRoundTrip(t *testing.T) {
	for _, c := range []Config{None(), Boot(0), Boot(1), Boot(7), Boot(0xFFFF)} {
		got, err := Decode(c.Encode())
		if err != nil {
			t.Fatalf("Decode(Encode(%v)): %v", c, err)
		}
		if got != c {
			t.Errorf("round-trip %v -> %v", c, got)
		}
	}
}

// TestDecodeUnrecognised confirms an unknown version or a non-boot mode decodes to
// "no auto-boot" (matching samboot_read_config), never a spurious boot.
func TestDecodeUnrecognised(t *testing.T) {
	bad := Boot(9).Encode()
	bad[0] = 0xFF // unrecognised version
	if got, _ := Decode(bad); got.AutoBoot {
		t.Errorf("bad-version chunk decoded to auto-boot %v, want no auto-boot", got)
	}

	mode := Boot(9).Encode()
	mode[1] = 0x42 // unknown mode (not ModeBoot)
	if got, _ := Decode(mode); got.AutoBoot {
		t.Errorf("unknown-mode chunk decoded to auto-boot %v, want no auto-boot", got)
	}
}

// TestDecodeShort confirms a chunk too short to hold the header is an error, not a
// silent misread.
func TestDecodeShort(t *testing.T) {
	if _, err := Decode([]byte{Version, ModeBoot}); err == nil {
		t.Errorf("Decode of a 2-byte chunk should error (needs the record bytes)")
	}
}
