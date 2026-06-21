// poly1305_test.go — the i88 host-verification of Poly1305 (src/netboot/poly1305.asm).
// It assembles the standalone module under the koron-go/z80 harness, runs
// poly1305_mac, and asserts the 16-byte tag equals (a) the RFC 8439 §2.5.2
// known-answer vector and (b) an independent math/big reference implementation of
// Poly1305 (clamp r, the (a+n)*r mod 2^130-5 loop, +s, low 16 bytes) across many
// message lengths — empty, partial, block-boundary, and multi-block — byte-for-byte.
package z80_test

import (
	"bytes"
	"math/big"
	"os"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	polyBinPath = "../../../build/netboot_poly1305.bin"
	polyMapPath = "../../../build/netboot_poly1305.map"

	// Staging in low RAM, clear of the &8000+ code and the harness stack/trap.
	polyMsgStage = 0x4000
	polyOutStage = 0x5000
)

func loadPoly(t *testing.T) *z80h.Machine {
	t.Helper()
	if _, err := os.Stat(polyBinPath); err != nil {
		t.Fatalf("poly1305 binary not built (%s); run `make netboot-poly1305`", polyBinPath)
	}
	mac, err := z80h.Load(polyBinPath, polyMapPath)
	if err != nil {
		t.Fatalf("load poly1305: %v", err)
	}
	return mac
}

// TestPoly1305Mul8Exhaustive verifies poly1305's quarter-square byte multiply
// against the true product for ALL 256x256 operand pairs — a complete, copy-
// independent correctness proof of this leaf's mul8 and its qsq_table.
func TestPoly1305Mul8Exhaustive(t *testing.T) {
	mac := loadPoly(t)
	if _, err := mac.Call("qsq_init"); err != nil {
		t.Fatalf("qsq_init: %v", err)
	}
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

// polyZ80 drives poly1305_mac on a fresh machine and returns the 16-byte tag.
func polyZ80(t *testing.T, key, msg []byte) []byte {
	t.Helper()
	mac := loadPoly(t)
	mac.Write(mustSym(t, mac, "POLY_KEY"), key)
	mac.Write(polyMsgStage, msg)
	mac.WriteU16LE(mustSym(t, mac, "POLY_MSG_PTR"), polyMsgStage)
	mac.WriteU16LE(mustSym(t, mac, "POLY_MSG_LEN"), uint16(len(msg)))
	if _, err := mac.CallEntry("poly1305_mac", z80h.Entry{HL: polyOutStage}); err != nil {
		t.Fatalf("poly1305_mac: %v", err)
	}
	return mac.Read(polyOutStage, 16)
}

// leToBig interprets a little-endian byte slice as a big integer.
func leToBig(b []byte) *big.Int {
	r := make([]byte, len(b))
	for i, v := range b {
		r[len(b)-1-i] = v
	}
	return new(big.Int).SetBytes(r)
}

// poly1305Ref is the independent reference (RFC 8439 §2.5): the Go authority the
// Z80 port is checked against.
func poly1305Ref(key, msg []byte) []byte {
	p := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 130), big.NewInt(5)) // 2^130 - 5
	r := make([]byte, 16)
	copy(r, key[:16])
	r[3] &= 15
	r[7] &= 15
	r[11] &= 15
	r[15] &= 15
	r[4] &= 252
	r[8] &= 252
	r[12] &= 252
	rn := leToBig(r)
	sn := leToBig(key[16:32])
	acc := big.NewInt(0)
	for i := 0; i < len(msg); i += 16 {
		end := i + 16
		if end > len(msg) {
			end = len(msg)
		}
		blk := make([]byte, end-i+1)
		copy(blk, msg[i:end])
		blk[end-i] = 1
		acc.Add(acc, leToBig(blk))
		acc.Mul(acc, rn)
		acc.Mod(acc, p)
	}
	acc.Add(acc, sn)
	acc.Mod(acc, new(big.Int).Lsh(big.NewInt(1), 128)) // low 128 bits
	be := acc.FillBytes(make([]byte, 16))              // big-endian
	out := make([]byte, 16)
	for i, v := range be {
		out[15-i] = v
	}
	return out
}

// TestPoly1305RFC: the RFC 8439 §2.5.2 worked example, and a self-check that the
// math/big reference reproduces it (so the reference itself is anchored to the RFC
// before it is used to fuzz the Z80 port).
func TestPoly1305RFC(t *testing.T) {
	key := unhex(t, "85d6be7857556d337f4452fe42d506a8"+
		"0103808afb0db2fd4abff6af4149f51b")
	msg := []byte("Cryptographic Forum Research Group")
	wantTag := unhex(t, "a8061dc1305136c6c22b8baf0c0127a9")

	if got := poly1305Ref(key, msg); !bytes.Equal(got, wantTag) {
		t.Fatalf("reference disagrees with RFC 8439 §2.5.2: got %x, want %x", got, wantTag)
	}
	if got := polyZ80(t, key, msg); !bytes.Equal(got, wantTag) {
		t.Errorf("Z80 tag = %x, want %x", got, wantTag)
	}
}

// TestPoly1305VsRef: poly1305_mac matches the math/big reference across message
// lengths that exercise the empty case, partial blocks, exact block boundaries,
// and multi-block messages (where the reduction folds repeatedly).
func TestPoly1305VsRef(t *testing.T) {
	// A deterministic but non-trivial key (r has bits the clamp must clear).
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i*37 + 11)
	}
	for _, n := range []int{0, 1, 2, 15, 16, 17, 31, 32, 33, 48, 63, 64, 65, 113, 114, 128, 200, 256} {
		msg := make([]byte, n)
		for i := range msg {
			msg[i] = byte(i*131 + 7)
		}
		want := poly1305Ref(key, msg)
		got := polyZ80(t, key, msg)
		if !bytes.Equal(got, want) {
			t.Errorf("len=%d: tag = %x, want %x", n, got, want)
		}
	}
}

// TestPoly1305KeyVariety: vary the key (including all-zero r, which makes every
// product zero, and all-0xFF, which maximises the clamp + reduction) over a fixed
// message, to exercise the freeze/canonicalisation path.
func TestPoly1305KeyVariety(t *testing.T) {
	msg := []byte("The quick brown fox jumps over the lazy dog. 0123456789!")
	keys := [][]byte{
		bytes.Repeat([]byte{0x00}, 32),
		bytes.Repeat([]byte{0xff}, 32),
		bytes.Repeat([]byte{0x01}, 32),
		append(bytes.Repeat([]byte{0xff}, 16), bytes.Repeat([]byte{0x00}, 16)...), // max r, zero s
		append(bytes.Repeat([]byte{0x00}, 16), bytes.Repeat([]byte{0xff}, 16)...), // zero r, max s
	}
	for i, key := range keys {
		want := poly1305Ref(key, msg)
		got := polyZ80(t, key, msg)
		if !bytes.Equal(got, want) {
			t.Errorf("key#%d: tag = %x, want %x", i, got, want)
		}
	}
}
