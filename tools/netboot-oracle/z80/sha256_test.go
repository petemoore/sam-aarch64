package z80_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/rand"
	"os"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

// Built artefact: the Makefile target netboot-sha256.
const (
	sha256BinPath = "../../../build/netboot_sha256.bin"
	sha256MapPath = "../../../build/netboot_sha256.map"
)

// Scratch RAM regions the SHA-256 driver routines read/write. They sit clear of
// the loaded code AND of the harness stack/trap (&6FFE/&7000). The standalone
// image is org &8000; with the 8x-unrolled round block it grows to ≈ &9062 (the
// K table ends there), so the message stage must sit ABOVE that, not at the old
// &9000 (which then landed inside the K table and corrupted the high rounds).
// &A000 leaves a ~4 KB margin over the code and ≈24 KB of headroom for the
// longest chunk the streaming tests stage (the 999-byte long-vector chunk).
const (
	shaInputStage  = 0xA000 // where the test stages the message bytes
	shaOutputStage = 0x5000 // where sha256_final writes the 32-byte digest
)

func loadSHA256Machine(t *testing.T) *z80h.Machine {
	t.Helper()
	if _, err := os.Stat(sha256BinPath); err != nil {
		t.Fatalf("sha256 routine binary not built (%s); run `make netboot-sha256`", sha256BinPath)
	}
	mac, err := z80h.Load(sha256BinPath, sha256MapPath)
	if err != nil {
		t.Fatalf("load routine: %v", err)
	}
	return mac
}

// z80Sum computes the SHA-256 of input on the Z80, feeding it in the given
// chunk sizes (cycled if shorter than the message) via repeated sha256_update
// calls — exactly the streaming API the firmware download path uses. A nil/empty
// chunks slice means "feed the whole message in one update".
func z80Sum(t *testing.T, mac *z80h.Machine, input []byte, chunks []int) [32]byte {
	t.Helper()

	if _, err := mac.Call("sha256_init"); err != nil {
		t.Fatalf("sha256_init: %v", err)
	}

	off := 0
	ci := 0
	for off < len(input) {
		n := len(input) - off
		if len(chunks) > 0 {
			n = chunks[ci%len(chunks)]
			ci++
			if n > len(input)-off {
				n = len(input) - off
			}
			if n == 0 {
				// A zero-length chunk is a legal update; exercise it but make
				// progress by also doing nothing this iteration.
				if _, err := mac.CallEntry("sha256_update", z80h.Entry{HL: shaInputStage, BC: 0}); err != nil {
					t.Fatalf("sha256_update(len=0): %v", err)
				}
				continue
			}
		}
		// Stage this chunk's bytes at a FIXED address and update over them. Each
		// sha256_update only reads its own chunk, so we restage at the same place
		// every call — the cumulative offset would overflow the 16-bit address
		// space on the 1 MB vector.
		mac.Write(shaInputStage, input[off:off+n])
		if _, err := mac.CallEntry("sha256_update", z80h.Entry{HL: shaInputStage, BC: uint16(n)}); err != nil {
			t.Fatalf("sha256_update(off=%d,len=%d): %v", off, n, err)
		}
		off += n
	}
	// Empty message: still issue one zero-length update to exercise that path.
	if len(input) == 0 {
		if _, err := mac.CallEntry("sha256_update", z80h.Entry{HL: shaInputStage, BC: 0}); err != nil {
			t.Fatalf("sha256_update(empty): %v", err)
		}
	}

	if _, err := mac.CallEntry("sha256_final", z80h.Entry{HL: shaOutputStage}); err != nil {
		t.Fatalf("sha256_final: %v", err)
	}
	var out [32]byte
	copy(out[:], mac.Read(shaOutputStage, 32))
	return out
}

// shaCases are the FIPS 180-4 / NIST vectors plus the padding-boundary cases.
func shaCases() []struct {
	name  string
	input []byte
} {
	mkrand := func(n int, seed int64) []byte {
		b := make([]byte, n)
		rng := rand.New(rand.NewSource(seed))
		rng.Read(b)
		return b
	}
	return []struct {
		name  string
		input []byte
	}{
		{"empty", []byte{}},
		{"abc", []byte("abc")},
		// The 56-byte two-block FIPS vector (crosses into a 2nd padding block:
		// 56 + 0x80 = 57 > 56, so the length pushes into a second block).
		{"two-block-56", []byte("abcdbcdecdefdefgefghfghighijhijkijkljklmklmnlmnomnopnopq")},
		{"len-64-exact-block", mkrand(64, 1)},
		{"len-65", mkrand(65, 2)},
		{"len-55", mkrand(55, 5)}, // last byte before the 1-block/2-block boundary
		{"len-56", mkrand(56, 6)}, // first length needing a 2nd padding block
		{"len-119", mkrand(119, 3)},
		{"len-120", mkrand(120, 4)},
	}
}

// TestSHA256WholeMessage hashes each vector as a single sha256_update and
// asserts the Z80 digest equals Go's crypto/sha256.Sum256 byte-for-byte.
func TestSHA256WholeMessage(t *testing.T) {
	mac := loadSHA256Machine(t)
	for _, tc := range shaCases() {
		t.Run(tc.name, func(t *testing.T) {
			want := sha256.Sum256(tc.input)
			got := z80Sum(t, mac, tc.input, nil)
			if got != want {
				t.Errorf("digest mismatch\n z80 %s\n  go %s",
					hex.EncodeToString(got[:]), hex.EncodeToString(want[:]))
			}
		})
	}
}

// TestSHA256Chunked is the most important test: it proves the streaming API by
// feeding each message in awkward chunk sizes (1/13/64/100 …) so the partial
// block is carried across many sha256_update calls. The digest must still match.
func TestSHA256Chunked(t *testing.T) {
	mac := loadSHA256Machine(t)
	chunkPatterns := [][]int{
		{1},              // one byte at a time
		{13},             // a size coprime with 64
		{64},             // exact-block chunks
		{100},            // larger than a block
		{1, 13, 64, 100}, // a mix, as the prompt specifies
		{0, 1, 0, 7, 0},  // interleave zero-length updates
		{3, 61},          // 3 then 61 = exactly fills the first block
	}
	for _, tc := range shaCases() {
		want := sha256.Sum256(tc.input)
		for pi, pat := range chunkPatterns {
			t.Run(fmt.Sprintf("%s/pat%d", tc.name, pi), func(t *testing.T) {
				got := z80Sum(t, mac, tc.input, pat)
				if got != want {
					t.Errorf("digest mismatch (chunks %v)\n z80 %s\n  go %s",
						pat, hex.EncodeToString(got[:]), hex.EncodeToString(want[:]))
				}
			})
		}
	}
}

// TestSHA256KnownVectors pins the two canonical FIPS digests explicitly so a
// regression is obvious even without re-deriving them from crypto/sha256.
func TestSHA256KnownVectors(t *testing.T) {
	mac := loadSHA256Machine(t)
	cases := []struct {
		name  string
		input []byte
		want  string
	}{
		{"empty", []byte{}, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
		{"abc", []byte("abc"), "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"},
		{"two-block-56", []byte("abcdbcdecdefdefgefghfghighijhijkijkljklmklmnlmnomnopnopq"),
			"248d6a61d20638b8e5c026930c3e6039a33ce45964ff2167f6ecedd419db06c1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wantBytes, _ := hex.DecodeString(tc.want)
			got := z80Sum(t, mac, tc.input, nil)
			if !bytes.Equal(got[:], wantBytes) {
				t.Errorf("digest mismatch\n z80  %s\n want %s", hex.EncodeToString(got[:]), tc.want)
			}
		})
	}
}

// TestSHA256LongVector feeds the NIST long message (1,000,000 bytes of 'a') in
// chunks — the canonical streaming stress vector. Its digest is the published
// cdc76e5c… value. This is what proves the bit-length counter and the
// many-blocks carry are right.
func TestSHA256LongVector(t *testing.T) {
	mac := loadSHA256Machine(t)
	const n = 1_000_000
	input := bytes.Repeat([]byte("a"), n)
	want := sha256.Sum256(input)
	const published = "cdc76e5c9914fb9281a1c7e284d73e67f1809a48a497200e046d39ccc7112cd0"
	if hex.EncodeToString(want[:]) != published {
		t.Fatalf("crypto/sha256 of 1e6 'a' = %s, expected the published %s",
			hex.EncodeToString(want[:]), published)
	}
	// Feed it in awkwardly-sized chunks to exercise the streaming carry hard.
	got := z80Sum(t, mac, input, []int{1, 13, 64, 100, 999})
	if got != want {
		t.Errorf("long-vector digest mismatch\n z80 %s\n  go %s",
			hex.EncodeToString(got[:]), hex.EncodeToString(want[:]))
	}
}

// TestSHA256Reinit asserts sha256_init fully resets state: hashing "abc" after a
// previous full hash of a different message must give the plain "abc" digest
// (final must not have left H in a state a re-init fails to clear).
func TestSHA256Reinit(t *testing.T) {
	mac := loadSHA256Machine(t)
	// First hash a long-ish different message.
	_ = z80Sum(t, mac, bytes.Repeat([]byte("x"), 200), []int{7})
	// Now hash "abc" fresh; it must equal the canonical "abc" digest.
	got := z80Sum(t, mac, []byte("abc"), nil)
	want := sha256.Sum256([]byte("abc"))
	if got != want {
		t.Errorf("re-init did not fully reset\n z80 %s\n  go %s",
			hex.EncodeToString(got[:]), hex.EncodeToString(want[:]))
	}
}
