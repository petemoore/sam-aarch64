// chacha20_test.go — the i88 host-verification of the ChaCha20 block function
// (src/netboot/chacha20.asm). It assembles the standalone module under the
// koron-go/z80 harness, runs chacha20_block for the RFC 8439 known-answer vectors
// (§2.3.2 + Appendix A.1 #1/#2 — distinct keys, counters, nonces), and asserts the
// 64-byte keystream block matches byte-for-byte.
package z80_test

import (
	"bytes"
	"encoding/hex"
	"os"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	ccBinPath  = "../../../build/netboot_chacha20.bin"
	ccMapPath  = "../../../build/netboot_chacha20.map"
	ccOutStage = 0x5000
)

func loadChaCha(t *testing.T) *z80h.Machine {
	t.Helper()
	if _, err := os.Stat(ccBinPath); err != nil {
		t.Skipf("chacha20 binary not built (%s); run `make netboot-chacha20`", ccBinPath)
	}
	mac, err := z80h.Load(ccBinPath, ccMapPath)
	if err != nil {
		t.Fatalf("load chacha20: %v", err)
	}
	return mac
}

func unhex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad hex %q: %v", s, err)
	}
	return b
}

// TestChaCha20Block: chacha20_block produces the RFC 8439 keystream blocks. The
// counter is a little-endian 32-bit word; the keys/nonces are byte strings.
func TestChaCha20Block(t *testing.T) {
	cases := []struct {
		name    string
		key     string // 64 hex chars
		counter uint32
		nonce   string // 24 hex chars
		want    string // 128 hex chars (64-byte keystream)
	}{
		{
			// RFC 8439 §2.3.2 worked example.
			name:    "rfc8439-2.3.2",
			key:     "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f",
			counter: 1,
			nonce:   "000000090000004a00000000",
			want: "10f1e7e4d13b5915500fdd1fa32071c4" +
				"c7d1f4c733c068030422aa9ac3d46c4e" +
				"d2826446079faa0914c2d705d98b02a2" +
				"b5129cd1de164eb9cbd083e8a2503c4e",
		},
		{
			// RFC 8439 Appendix A.1 Test Vector #1.
			name:    "rfc8439-A.1-1",
			key:     "0000000000000000000000000000000000000000000000000000000000000000",
			counter: 0,
			nonce:   "000000000000000000000000",
			want: "76b8e0ada0f13d90405d6ae55386bd28" +
				"bdd219b8a08ded1aa836efcc8b770dc7" +
				"da41597c5157488d7724e03fb8d84a37" +
				"6a43b8f41518a11cc387b669b2ee6586",
		},
		{
			// RFC 8439 Appendix A.1 Test Vector #2.
			name:    "rfc8439-A.1-2",
			key:     "0000000000000000000000000000000000000000000000000000000000000000",
			counter: 1,
			nonce:   "000000000000000000000000",
			want: "9f07e7be5551387a98ba977c732d080d" +
				"cb0f29a048e3656912c6533e32ee7aed" +
				"29b721769ce64e43d57133b074d839d5" +
				"31ed1f28510afb45ace10a1f4b794d6f",
		},
	}

	mac := loadChaCha(t)
	keyAddr := mustSym(t, mac, "CC_KEY")
	ctrAddr := mustSym(t, mac, "CC_COUNTER")
	nonceAddr := mustSym(t, mac, "CC_NONCE")

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mac.Write(keyAddr, unhex(t, tc.key))
			mac.Write(ctrAddr, []byte{byte(tc.counter), byte(tc.counter >> 8), byte(tc.counter >> 16), byte(tc.counter >> 24)})
			mac.Write(nonceAddr, unhex(t, tc.nonce))

			if _, err := mac.CallEntry("chacha20_block", z80h.Entry{HL: ccOutStage}); err != nil {
				t.Fatalf("chacha20_block: %v", err)
			}
			got := mac.Read(ccOutStage, 64)
			want := unhex(t, tc.want)
			if !bytes.Equal(got, want) {
				t.Errorf("keystream = %x,\n     want = %x", got, want)
			}
		})
	}
}
