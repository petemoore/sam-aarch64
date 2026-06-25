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
		t.Fatalf("x25519 binary not built (%s); run `make netboot-x25519-field`", feBinPath)
	}
	mac, err := z80h.Load(feBinPath, feMapPath)
	if err != nil {
		t.Fatalf("load x25519: %v", err)
	}
	// mul8 uses the quarter-square table, which x25519 builds at its entry but the
	// field-op entries (fe_mul/fe_sqr/fe_invert/...) assume already built — so the
	// standalone tests build it here, once per freshly-loaded machine.
	if _, err := mac.Call("qsq_init"); err != nil {
		t.Fatalf("qsq_init: %v", err)
	}
	return mac
}

// TestMul8Exhaustive verifies the quarter-square byte multiply against the true
// product for ALL 256x256 operand pairs — a complete correctness proof of mul8
// (and therefore of qsq_table), independent of the higher-level field tests.
func TestMul8Exhaustive(t *testing.T) {
	mac := loadFE(t)
	for a := 0; a < 256; a++ {
		for e := 0; e < 256; e++ {
			res, err := mac.CallEntry("mul8", z80h.Entry{A: uint8(a), DE: uint16(e)})
			if err != nil {
				t.Fatalf("mul8(%d,%d): %v", a, e, err)
			}
			if want := uint16(a * e); res.HL != want {
				t.Fatalf("mul8(%d,%d) = %d, want %d", a, e, res.HL, want)
			}
		}
	}
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

// TestFEMulRandom drives fe_mul over many independent pseudo-random ASYMMETRIC
// operand pairs (a != b) versus a math/big reference. The fixed feElements set
// (11 values, 121 pairs) is sparse on the carry edge cases the Karatsuba 32x32
// multiply introduces — the half-operand sums/differences, the sign of the
// middle term (a_lo-a_hi)*(b_lo-b_hi), and the carry plumbing of the combine.
// Random asymmetric pairs hit both signs of the middle term and arbitrary
// half-boundary carries that a*a (TestFESqrMatchesMul) can't, exercising the
// new code path far more thoroughly. Reuses one machine: each op rewrites its
// own inputs/output, so state carries over safely.
func TestFEMulRandom(t *testing.T) {
	mac := loadFE(t)
	feA := mustSym(t, mac, "FE_A")
	feB := mustSym(t, mac, "FE_B")
	feOut := mustSym(t, mac, "FE_OUT")
	p := fePrime()
	seed := uint32(0xC0FFEE11)
	next := func() ([]byte, *big.Int) {
		b := make([]byte, 32)
		for i := range b {
			seed = seed*1664525 + 1013904223
			b[i] = byte(seed >> 16)
		}
		b[31] &= 0x7f // keep < 2^255
		return b, leToBig(b)
	}
	for n := 0; n < 1024; n++ {
		aLE, aV := next()
		bLE, bV := next()
		mac.Write(feA, aLE)
		mac.Write(feB, bLE)
		if _, err := mac.CallEntry("fe_mul", z80h.Entry{}); err != nil {
			t.Fatalf("fe_mul: %v", err)
		}
		got := mac.Read(feOut, 32)
		assertReduced(t, "fe_mul", got)
		want := new(big.Int).Mod(new(big.Int).Mul(aV, bV), p)
		if new(big.Int).Mod(leToBig(got), p).Cmp(want) != 0 {
			t.Fatalf("fe_mul(%x,%x): got %x (mod p %x), want %x",
				aLE, bLE, got, feToLE32(new(big.Int).Mod(leToBig(got), p)), feToLE32(want))
		}
	}
}

// TestFESqr: the dedicated symmetric squarer must equal a^2 mod p. fe_sqr is the
// dominant x25519 win (the ladder squares 4x per step + 254x in fe_invert), so it
// gets both a math/big reference pin here and a direct fe_sqr-vs-fe_mul(a,a)
// cross-check (TestFESqrMatchesMul) on a wider input set.
func TestFESqr(t *testing.T) {
	p := fePrime()
	for _, a := range feElements() {
		got := feOp1(t, "fe_sqr", a)
		assertReduced(t, "fe_sqr", got)
		want := new(big.Int).Mod(new(big.Int).Mul(a, a), p)
		if new(big.Int).Mod(leToBig(got), p).Cmp(want) != 0 {
			t.Errorf("fe_sqr(%x): got %x (mod p %x), want %x",
				feToLE32(a), got, feToLE32(new(big.Int).Mod(leToBig(got), p)), feToLE32(want))
		}
	}
}

// TestFESqrMatchesMul cross-checks fe_sqr(a) == fe_mul(a,a) for a spread of
// deterministic pseudo-random elements < 2^255 — exercising carry chains the
// 11-element feElements set doesn't reach. Reuses one machine: each op rewrites
// its inputs and FE_OUT, so state carries over safely.
func TestFESqrMatchesMul(t *testing.T) {
	mac := loadFE(t)
	feA := mustSym(t, mac, "FE_A")
	feB := mustSym(t, mac, "FE_B")
	feOut := mustSym(t, mac, "FE_OUT")
	// A simple LCG over 32 bytes gives reproducible operands without an import.
	seed := uint32(0x12345678)
	next := func() []byte {
		b := make([]byte, 32)
		for i := range b {
			seed = seed*1664525 + 1013904223
			b[i] = byte(seed >> 16)
		}
		b[31] &= 0x7f // keep < 2^255
		return b
	}
	for n := 0; n < 256; n++ {
		a := next()
		mac.Write(feA, a)
		if _, err := mac.CallEntry("fe_sqr", z80h.Entry{}); err != nil {
			t.Fatalf("fe_sqr: %v", err)
		}
		sq := append([]byte(nil), mac.Read(feOut, 32)...)
		mac.Write(feA, a)
		mac.Write(feB, a)
		if _, err := mac.CallEntry("fe_mul", z80h.Entry{}); err != nil {
			t.Fatalf("fe_mul: %v", err)
		}
		ml := mac.Read(feOut, 32)
		if !bytes.Equal(sq, ml) {
			t.Fatalf("fe_sqr(%x)=%x != fe_mul(a,a)=%x", a, sq, ml)
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

// feInvertStepCap bounds one fe_invert (a ~37M-byte-op Fermat exponentiation,
// far past the default runaway guard); generous so it never trips on real runs.
const feInvertStepCap = 200_000_000

// TestFEInvert: fe_invert computes the modular inverse (a^(p-2) mod p). The
// defining property a·a^-1 ≡ 1 (mod p) is checked for every non-zero element,
// plus equality with math/big's ModInverse; 0 (and p ≡ 0) invert to 0 (0^(p-2)).
func TestFEInvert(t *testing.T) {
	p := fePrime()
	// A focused subset (each inversion is seconds of emulation): include 0 and p
	// (both ≡ 0 → 0) and a spread of non-zero elements.
	els := []*big.Int{
		big.NewInt(0),
		new(big.Int).Set(p), // ≡ 0
		big.NewInt(1),
		big.NewInt(2),
		big.NewInt(121665),
		new(big.Int).Sub(p, big.NewInt(1)), // p-1
		new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 255), big.NewInt(1)), // 2^255-1
		new(big.Int).SetBytes([]byte{
			0x7e, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff,
			0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef, 0xfe, 0xdc, 0xba, 0x98, 0x76, 0x54, 0x32, 0x10,
		}),
	}
	for _, a := range els {
		mac := loadFE(t)
		mac.Write(mustSym(t, mac, "FE_A"), feToLE32(a))
		if _, err := mac.CallEntry("fe_invert", z80h.Entry{StepCap: feInvertStepCap}); err != nil {
			t.Fatalf("fe_invert(%x): %v", feToLE32(a), err)
		}
		got := mac.Read(mustSym(t, mac, "FE_OUT"), 32)
		assertReduced(t, "fe_invert", got)
		gotV := new(big.Int).Mod(leToBig(got), p)

		aModP := new(big.Int).Mod(a, p)
		if aModP.Sign() == 0 {
			if gotV.Sign() != 0 {
				t.Errorf("fe_invert(%x): got %x, want 0 (0^(p-2))", feToLE32(a), got)
			}
			continue
		}
		// Defining property: a · a^-1 ≡ 1 (mod p).
		prod := new(big.Int).Mod(new(big.Int).Mul(aModP, gotV), p)
		if prod.Cmp(big.NewInt(1)) != 0 {
			t.Errorf("fe_invert(%x): a·inv = %d, want 1", feToLE32(a), prod)
		}
		// And equality with math/big's inverse.
		want := new(big.Int).ModInverse(aModP, p)
		if gotV.Cmp(want) != 0 {
			t.Errorf("fe_invert(%x): got %x, want %x", feToLE32(a), feToLE32(gotV), feToLE32(want))
		}
	}
}
