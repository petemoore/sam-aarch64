// hkdf_test.go — the i88 host-verification of HKDF (src/netboot/hkdf.asm). It
// assembles the standalone module under the koron-go/z80 harness, drives
// hkdf_extract then hkdf_expand over the RFC 5869 Appendix-A vectors (plus the
// multi-block-OKM and zero-salt/empty-info boundaries), and asserts the PRK + OKM
// equal Go's crypto/hkdf.Extract / Expand byte-for-byte.
package z80_test

import (
	"bytes"
	"crypto/hkdf"
	"crypto/sha256"
	"os"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	hkdfBinPath = "../../../build/netboot_hkdf.bin"
	hkdfMapPath = "../../../build/netboot_hkdf.map"

	// Staging in low RAM, clear of the &8000+ code and the harness stack/trap.
	hkdfSaltStage = 0x4000
	hkdfIKMStage  = 0x4400
	hkdfInfoStage = 0x4800
	hkdfOutStage  = 0x5000
)

func loadHKDF(t *testing.T) *z80h.Machine {
	t.Helper()
	if _, err := os.Stat(hkdfBinPath); err != nil {
		t.Fatalf("hkdf binary not built (%s); run `make netboot-hkdf`", hkdfBinPath)
	}
	mac, err := z80h.Load(hkdfBinPath, hkdfMapPath)
	if err != nil {
		t.Fatalf("load hkdf: %v", err)
	}
	return mac
}

// seq builds n bytes [start, start+1, ...] (the RFC 5869 test-2 input shape).
func seq(start, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(start + i)
	}
	return b
}

// TestHKDF: hkdf_extract then hkdf_expand match Go crypto/hkdf across the RFC 5869
// vectors and the OKM-length boundaries (one block, exact multiples, partial,
// multi-block) and the zero-salt / empty-info case.
func TestHKDF(t *testing.T) {
	cases := []struct {
		name            string
		ikm, salt, info []byte
		length          int
	}{
		{"rfc5869-1", rep(0x0b, 22), seq(0x00, 13), seq(0xf0, 10), 42},
		{"rfc5869-2", seq(0x00, 80), seq(0x60, 80), seq(0xb0, 80), 82}, // 3 T-blocks
		{"rfc5869-3-zerosalt-emptyinfo", rep(0x0b, 22), nil, nil, 42},
		{"len-1", rep(0x22, 16), seq(0x01, 8), []byte("ctx"), 1},
		{"len-32-oneblock", rep(0x22, 16), seq(0x01, 8), []byte("ctx"), 32},
		{"len-33-justover", rep(0x22, 16), seq(0x01, 8), []byte("ctx"), 33},
		{"len-64-twoblocks", rep(0x22, 16), seq(0x01, 8), []byte("ctx"), 64},
	}

	mac := loadHKDF(t)
	saltPtr := mustSym(t, mac, "HKDF_SALT_PTR")
	saltLen := mustSym(t, mac, "HKDF_SALT_LEN")
	ikmPtr := mustSym(t, mac, "HKDF_IKM_PTR")
	ikmLen := mustSym(t, mac, "HKDF_IKM_LEN")
	infoPtr := mustSym(t, mac, "HKDF_INFO_PTR")
	infoLen := mustSym(t, mac, "HKDF_INFO_LEN")
	lCell := mustSym(t, mac, "HKDF_L")
	prkAddr := mustSym(t, mac, "HKDF_PRK")

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// --- extract: PRK = HMAC(salt, IKM) ---
			mac.Write(hkdfSaltStage, tc.salt)
			mac.Write(hkdfIKMStage, tc.ikm)
			mac.WriteU16LE(saltPtr, hkdfSaltStage)
			mac.WriteU16LE(saltLen, uint16(len(tc.salt)))
			mac.WriteU16LE(ikmPtr, hkdfIKMStage)
			mac.WriteU16LE(ikmLen, uint16(len(tc.ikm)))
			if _, err := mac.Call("hkdf_extract"); err != nil {
				t.Fatalf("hkdf_extract: %v", err)
			}
			gotPRK := mac.Read(prkAddr, 32)

			wantPRK, err := hkdf.Extract(sha256.New, tc.ikm, tc.salt)
			if err != nil {
				t.Fatalf("go hkdf.Extract: %v", err)
			}
			if !bytes.Equal(gotPRK, wantPRK) {
				t.Fatalf("PRK = %x, want %x", gotPRK, wantPRK)
			}

			// --- expand: OKM from the just-extracted PRK (left in HKDF_PRK) ---
			mac.Write(hkdfInfoStage, tc.info)
			mac.WriteU16LE(infoPtr, hkdfInfoStage)
			mac.WriteU16LE(infoLen, uint16(len(tc.info)))
			mac.WriteU16LE(lCell, uint16(tc.length))
			if _, err := mac.CallEntry("hkdf_expand", z80h.Entry{HL: hkdfOutStage}); err != nil {
				t.Fatalf("hkdf_expand: %v", err)
			}
			gotOKM := mac.Read(hkdfOutStage, tc.length)

			wantOKM, err := hkdf.Expand(sha256.New, wantPRK, string(tc.info), tc.length)
			if err != nil {
				t.Fatalf("go hkdf.Expand: %v", err)
			}
			if !bytes.Equal(gotOKM, wantOKM) {
				t.Errorf("OKM(%d) = %x, want %x", tc.length, gotOKM, wantOKM)
			}
		})
	}
}
