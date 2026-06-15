// x25519_field_test.go — the i88 host-verification of the Curve25519 field
// arithmetic (src/netboot/x25519.asm). It assembles the standalone module under
// the koron-go/z80 harness, drives each field op over edge-case operands, and
// asserts the result matches a math/big reference modulo p = 2^255-19:
// byte-for-byte for fe_freeze (the canonical form) and value-equal-mod-p for the
// almost-reduced ops (fe_add/fe_sub/fe_mul/fe_mul121665), plus the < 2^255 output
// invariant every op maintains. The Montgomery ladder builds on these in a
// follow-up; this verifies the arithmetic foundation independently.
package z80_test

import (
	"bytes"
	"math/big"
	"os"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	feBinPath = "../../../build/netboot_x25519.bin"
	feMapPath = "../../../build/netboot_x25519.map"
)

func loadFE(t *testing.T) *z80h.Machine {
	t.Helper()
	if _, err := os.Stat(feBinPath); err != nil {
		t.Skipf("x25519 binary not built (%s); run `make netboot-x25519-field`", feBinPath)
	}
	mac, err := z80h.Load(feBinPath, feMapPath)
	if err != nil {
		t.Fatalf("load x25519: %v", err)
	}
	return mac
}

// fePrime is p = 2^255 - 19.
func fePrime() *big.Int {
	return new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 255), big.NewInt(19))
}

// feToLE32 renders a non-negative big.Int as 32 little-endian bytes.
func feToLE32(x *big.Int) []byte {
	be := x.FillBytes(make([]byte, 32))
	out := make([]byte, 32)
	for i, v := range be {
		out[31-i] = v
	}
	return out
}

// feOp1 runs a unary entry (FE_A -> FE_OUT) and returns the 32-byte output.
func feOp1(t *testing.T, entry string, a *big.Int) []byte {
	t.Helper()
	mac := loadFE(t)
	mac.Write(mustSym(t, mac, "FE_A"), feToLE32(a))
	if _, err := mac.CallEntry(entry, z80h.Entry{}); err != nil {
		t.Fatalf("%s: %v", entry, err)
	}
	return mac.Read(mustSym(t, mac, "FE_OUT"), 32)
}

// feOp2 runs a binary entry (FE_A, FE_B -> FE_OUT) and returns the output.
func feOp2(t *testing.T, entry string, a, b *big.Int) []byte {
	t.Helper()
	mac := loadFE(t)
	mac.Write(mustSym(t, mac, "FE_A"), feToLE32(a))
	mac.Write(mustSym(t, mac, "FE_B"), feToLE32(b))
	if _, err := mac.CallEntry(entry, z80h.Entry{}); err != nil {
		t.Fatalf("%s: %v", entry, err)
	}
	return mac.Read(mustSym(t, mac, "FE_OUT"), 32)
}

// feElements are the operands the ops are exercised over: 0, small, p and its
// neighbours, the max < 2^255, and the curve/reduction constants — all < 2^255.
func feElements() []*big.Int {
	p := fePrime()
	max := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 255), big.NewInt(1)) // 2^255-1 = p+18
	return []*big.Int{
		big.NewInt(0),
		big.NewInt(1),
		big.NewInt(2),
		big.NewInt(19),
		big.NewInt(38),
		big.NewInt(121665),
		big.NewInt(121666),
		new(big.Int).Sub(p, big.NewInt(1)), // p-1
		new(big.Int).Set(p),                // p  (≡ 0)
		max,                                // 2^255-1
		new(big.Int).SetBytes([]byte{ // a "random" element < 2^255 (big-endian, top byte 0x7e)
			0x7e, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff,
			0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef, 0xfe, 0xdc, 0xba, 0x98, 0x76, 0x54, 0x32, 0x10,
		}),
	}
}

// assertReduced checks the < 2^255 output invariant (byte 31 bit 7 clear).
func assertReduced(t *testing.T, name string, out []byte) {
	t.Helper()
	if out[31]&0x80 != 0 {
		t.Errorf("%s: output not < 2^255 (byte31 = %#02x): %x", name, out[31], out)
	}
}

func TestFEFreeze(t *testing.T) {
	p := fePrime()
	for _, a := range feElements() {
		got := feOp1(t, "fe_freeze", a)
		want := feToLE32(new(big.Int).Mod(a, p)) // canonical < p
		if !bytes.Equal(got, want) {
			t.Errorf("fe_freeze(%x): got %x, want %x", feToLE32(a), got, want)
		}
		// Canonical result must itself be < p.
		if v := leToBig(got); v.Cmp(p) >= 0 {
			t.Errorf("fe_freeze(%x): result %x not < p", feToLE32(a), got)
		}
	}
}

func TestFEAdd(t *testing.T) {
	p := fePrime()
	els := feElements()
	for _, a := range els {
		for _, b := range els {
			got := feOp2(t, "fe_add", a, b)
			assertReduced(t, "fe_add", got)
			want := new(big.Int).Mod(new(big.Int).Add(a, b), p)
			if new(big.Int).Mod(leToBig(got), p).Cmp(want) != 0 {
				t.Errorf("fe_add(%x,%x): got %x (mod p %x), want %x",
					feToLE32(a), feToLE32(b), got, feToLE32(new(big.Int).Mod(leToBig(got), p)), feToLE32(want))
			}
		}
	}
}

func TestFESub(t *testing.T) {
	p := fePrime()
	els := feElements()
	for _, a := range els {
		for _, b := range els {
			got := feOp2(t, "fe_sub", a, b)
			assertReduced(t, "fe_sub", got)
			want := new(big.Int).Mod(new(big.Int).Sub(a, b), p)
			if new(big.Int).Mod(leToBig(got), p).Cmp(want) != 0 {
				t.Errorf("fe_sub(%x,%x): got %x (mod p %x), want %x",
					feToLE32(a), feToLE32(b), got, feToLE32(new(big.Int).Mod(leToBig(got), p)), feToLE32(want))
			}
		}
	}
}

func TestFEMul(t *testing.T) {
	p := fePrime()
	els := feElements()
	for _, a := range els {
		for _, b := range els {
			got := feOp2(t, "fe_mul", a, b)
			assertReduced(t, "fe_mul", got)
			want := new(big.Int).Mod(new(big.Int).Mul(a, b), p)
			if new(big.Int).Mod(leToBig(got), p).Cmp(want) != 0 {
				t.Errorf("fe_mul(%x,%x): got %x (mod p %x), want %x",
					feToLE32(a), feToLE32(b), got, feToLE32(new(big.Int).Mod(leToBig(got), p)), feToLE32(want))
			}
		}
	}
}

func TestFEMul121665(t *testing.T) {
	p := fePrime()
	c := big.NewInt(121665)
	for _, a := range feElements() {
		got := feOp1(t, "fe_mul121665", a)
		assertReduced(t, "fe_mul121665", got)
		want := new(big.Int).Mod(new(big.Int).Mul(a, c), p)
		if new(big.Int).Mod(leToBig(got), p).Cmp(want) != 0 {
			t.Errorf("fe_mul121665(%x): got %x (mod p %x), want %x",
				feToLE32(a), got, feToLE32(new(big.Int).Mod(leToBig(got), p)), feToLE32(want))
		}
	}
}
