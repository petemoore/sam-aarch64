// x25519_test.go — the i88 host-verification of the X25519 scalar multiplication
// (the Montgomery ladder + clamp/decode + final inversion in src/netboot/x25519.asm).
// It runs x25519(scalar, u) under the koron-go/z80 harness and asserts the result
// equals (a) the RFC 7748 §5.2 known-answer vectors and (b) Go's crypto/ecdh
// X25519 over the base point for several scalars — byte-for-byte. The field
// arithmetic + inversion are verified separately (x25519_field_test.go); this
// covers their composition into the full key exchange.
package z80_test

import (
	"bytes"
	"crypto/ecdh"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

// x25519StepCap bounds one scalar multiplication (~255 ladder steps over the
// multi-precision field ops + the inversion, a few hundred million byte-ops);
// generous so it never trips on a correct run.
const x25519StepCap = 600_000_000

// x25519Z80 runs x25519(scalar, u) on a fresh machine and returns the 32-byte
// output u-coordinate. (loadFE loads the same netboot_x25519 binary.)
func x25519Z80(t *testing.T, scalar, u []byte) []byte {
	t.Helper()
	mac := loadFE(t)
	mac.Write(mustSym(t, mac, "X25519_K"), scalar)
	mac.Write(mustSym(t, mac, "X25519_U"), u)
	if _, err := mac.CallEntry("x25519", z80h.Entry{StepCap: x25519StepCap}); err != nil {
		t.Fatalf("x25519: %v", err)
	}
	return mac.Read(mustSym(t, mac, "X25519_OUT"), 32)
}

// TestX25519RFC: the RFC 7748 §5.2 known-answer vectors (two arbitrary
// scalar/u pairs + the base-point self-multiply that opens the iterative test).
func TestX25519RFC(t *testing.T) {
	cases := []struct {
		name, scalar, u, want string
	}{
		{
			"rfc7748-1",
			"a546e36bf0527c9d3b16154b82465edd62144c0ac1fc5a18506a2244ba449ac4",
			"e6db6867583030db3594c1a424b15f7c726624ec26b3353b10a903a6d0ab1c4c",
			"c3da55379de9c6908e94ea4df28d084f32eccf03491c71f754b4075577a28552",
		},
		{
			"rfc7748-2",
			"4b66e9d4d1b4673c5ad22691957d6af5c11b6421e0ea01d42ca4169e7918ba0d",
			"e5210f12786811d3f4b7959d0538ae2c31dbe7106fc03c3efc4cd549c715a493",
			"95cbde9476e8907d7aade45cb4b873f88b595a68799fa152e6f8f7647aac7957",
		},
		{
			// RFC 7748 §5.2 iterative test, k = u = base point (0x09…), one iteration.
			"basepoint-9",
			"0900000000000000000000000000000000000000000000000000000000000000",
			"0900000000000000000000000000000000000000000000000000000000000000",
			"422c8e7a6227d7bca1350b3e2bb7279f7897b87bb6854b783c60e80311ae3079",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := x25519Z80(t, unhex(t, tc.scalar), unhex(t, tc.u))
			if !bytes.Equal(got, unhex(t, tc.want)) {
				t.Errorf("x25519 = %x,\n   want = %x", got, unhex(t, tc.want))
			}
		})
	}
}

// TestX25519VsGo: x25519(scalar, basepoint) matches Go crypto/ecdh's public-key
// derivation (= scalar·G) for several scalars — an independent oracle for the
// scalar clamping + ladder over the standard base point.
func TestX25519VsGo(t *testing.T) {
	base := make([]byte, 32)
	base[0] = 9
	curve := ecdh.X25519()
	for _, seed := range []int{1, 2} {
		scalar := make([]byte, 32)
		for j := range scalar {
			scalar[j] = byte(j*53 + seed*101 + 7)
		}
		priv, err := curve.NewPrivateKey(scalar)
		if err != nil {
			t.Fatalf("seed %d: NewPrivateKey: %v", seed, err)
		}
		want := priv.PublicKey().Bytes()
		got := x25519Z80(t, scalar, base)
		if !bytes.Equal(got, want) {
			t.Errorf("seed %d: x25519(scalar, base) = %x,\n                    want = %x", seed, got, want)
		}
	}
}
