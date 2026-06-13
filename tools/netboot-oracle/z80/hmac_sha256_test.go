// hmac_sha256_test.go — the i88 host-verification of HMAC-SHA256
// (src/netboot/hmac_sha256.asm). It assembles the standalone module under the
// koron-go/z80 harness, drives hmac_sha256 over the RFC 4231 vectors (plus the
// K0-branch boundaries — key <64, ==64, >64 hashed, and empty key/message), and
// asserts the 32-byte MAC equals Go's crypto/hmac + crypto/sha256 of the same
// (key, message) byte-for-byte.
package z80_test

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"os"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	hmacBinPath = "../../../build/netboot_hmac_sha256.bin"
	hmacMapPath = "../../../build/netboot_hmac_sha256.map"

	// Staging in low RAM, clear of the &8000+ code and the harness stack/trap.
	hmacKeyStage = 0x4000
	hmacMsgStage = 0x4800
	hmacOutStage = 0x5000
)

func loadHMAC(t *testing.T) *z80h.Machine {
	t.Helper()
	if _, err := os.Stat(hmacBinPath); err != nil {
		t.Skipf("hmac_sha256 binary not built (%s); run `make netboot-hmac-sha256`", hmacBinPath)
	}
	mac, err := z80h.Load(hmacBinPath, hmacMapPath)
	if err != nil {
		t.Fatalf("load hmac_sha256: %v", err)
	}
	return mac
}

// rep builds a byte slice of n copies of b (the RFC 4231 keys/data shapes).
func rep(b byte, n int) []byte { return bytes.Repeat([]byte{b}, n) }

// TestHMACSHA256: hmac_sha256 matches Go crypto/hmac across the RFC 4231 vectors
// and the K0-branch boundaries (key length <64 / ==64 / >64-hashed, empty key,
// empty message).
func TestHMACSHA256(t *testing.T) {
	cases := []struct {
		name     string
		key, msg []byte
	}{
		{"rfc4231-1", rep(0x0b, 20), []byte("Hi There")},
		{"rfc4231-2", []byte("Jefe"), []byte("what do ya want for nothing?")},
		{"rfc4231-3", rep(0xaa, 20), rep(0xdd, 50)},
		{"rfc4231-4", []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25}, rep(0xcd, 50)},
		{"key131-hashed", rep(0xaa, 131), []byte("Test Using Larger Than Block-Size Key - Hash Key First")},
		{"key64-boundary", rep(0x0b, 64), []byte("key is exactly one block")},
		{"key65-justover", rep(0x0b, 65), []byte("key one over a block, hashed")},
		{"empty-key", nil, []byte("message with an empty key")},
		{"empty-msg", []byte("k"), nil},
		{"empty-both", nil, nil},
	}

	mac := loadHMAC(t)
	keyPtr := mustSym(t, mac, "HMAC_KEY_PTR")
	keyLen := mustSym(t, mac, "HMAC_KEY_LEN")
	msgPtr := mustSym(t, mac, "HMAC_MSG_PTR")
	msgLen := mustSym(t, mac, "HMAC_MSG_LEN")

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mac.Write(hmacKeyStage, tc.key)
			mac.Write(hmacMsgStage, tc.msg)
			mac.WriteU16LE(keyPtr, hmacKeyStage)
			mac.WriteU16LE(keyLen, uint16(len(tc.key)))
			mac.WriteU16LE(msgPtr, hmacMsgStage)
			mac.WriteU16LE(msgLen, uint16(len(tc.msg)))

			if _, err := mac.CallEntry("hmac_sha256", z80h.Entry{HL: hmacOutStage}); err != nil {
				t.Fatalf("hmac_sha256: %v", err)
			}
			got := mac.Read(hmacOutStage, 32)

			h := hmac.New(sha256.New, tc.key)
			h.Write(tc.msg)
			want := h.Sum(nil)

			if !bytes.Equal(got, want) {
				t.Errorf("HMAC-SHA256 = %x, want %x", got, want)
			}
		})
	}
}
