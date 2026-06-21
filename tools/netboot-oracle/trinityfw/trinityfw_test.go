// trinityfw_test.go — pure-Go sanity on the i213 firmware-stamp encoder/detector
// pair (no Z80). Pins the host-side authority the Z80 reader
// (src/netboot/trinity_identity_stamp.asm) is asserted against in
// tools/netboot-oracle/z80/trinity_identity_stamp_test.go.
package trinityfw

import "testing"

func TestEncodeLayout(t *testing.T) {
	enc := Encode(Version)
	if len(enc) != ChunkBytes {
		t.Fatalf("Encode len = %d, want %d", len(enc), ChunkBytes)
	}
	// magic at 0..3, version at 4, rest reserved zero.
	if enc[0] != Magic[0] || enc[1] != Magic[1] || enc[2] != Magic[2] || enc[3] != Magic[3] {
		t.Fatalf("magic = %x, want %x", enc[:4], Magic)
	}
	if enc[4] != Version {
		t.Fatalf("version byte = 0x%02x, want 0x%02x", enc[4], Version)
	}
	for i := 5; i < len(enc); i++ {
		if enc[i] != 0 {
			t.Fatalf("reserved byte %d = 0x%02x, want 0", i, enc[i])
		}
	}
}

func TestDetect(t *testing.T) {
	// Valid stamp -> ours, version passes through (even one this build predates).
	for _, v := range []uint8{1, 0x2A, 0xFF} {
		got, ours, err := Detect(Encode(v))
		if err != nil || !ours || got != v {
			t.Fatalf("Detect(Encode(%d)) = (%d,%v,%v), want (%d,true,nil)", v, got, ours, err, v)
		}
	}
	// Magic mismatch -> not ours, no error.
	bad := Encode(Version)
	bad[2] ^= 0xFF
	if _, ours, err := Detect(bad); ours || err != nil {
		t.Fatalf("Detect(bad magic) = ours=%v err=%v, want not-ours, nil", ours, err)
	}
	// Zero version (magic ok) -> not ours.
	if _, ours, _ := Detect(Encode(0)); ours {
		t.Fatalf("Detect(version 0) reported ours, want not ours")
	}
	// Too-short chunk -> error.
	if _, _, err := Detect([]byte{Magic[0]}); err == nil {
		t.Fatalf("Detect of a 1-byte chunk should error")
	}
}
