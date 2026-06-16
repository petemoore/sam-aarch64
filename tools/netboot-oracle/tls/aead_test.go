package tls

// RFC 8439 known-answer tests for the from-scratch ChaCha20-Poly1305 AEAD. Each
// primitive is pinned to its RFC vector so a regression localizes to the layer
// that broke, rather than only surfacing as an opaque handshake failure.

import (
	"bytes"
	"encoding/hex"
	"testing"
)

func unhexT(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad hex %q: %v", s, err)
	}
	return b
}

// TestChaChaBlockKAT pins chachaBlock to RFC 8439 §2.3.2.
func TestChaChaBlockKAT(t *testing.T) {
	key := unhexT(t, "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f")
	nonce := unhexT(t, "000000090000004a00000000")
	want := unhexT(t, ""+
		"10f1e7e4d13b5915500fdd1fa32071c4"+
		"c7d1f4c733c068030422aa9ac3d46c4e"+
		"d2826446079faa0914c2d705d98b02a2"+
		"b5129cd1de164eb9cbd083e8a2503c4e")
	got := chachaBlock(key, 1, nonce)
	if !bytes.Equal(got, want) {
		t.Errorf("chachaBlock:\n got %x\nwant %x", got, want)
	}
}

// TestPoly1305KAT pins poly1305 to RFC 8439 §2.5.2 (the one-time key r||s).
func TestPoly1305KAT(t *testing.T) {
	key := unhexT(t, "85d6be7857556d337f4452fe42d506a80103808afb0db2fd4abff6af4149f51b")
	msg := []byte("Cryptographic Forum Research Group")
	want := unhexT(t, "a8061dc1305136c6c22b8baf0c0127a9")
	got := poly1305(msg, key)
	if !bytes.Equal(got, want) {
		t.Errorf("poly1305:\n got %x\nwant %x", got, want)
	}
}

// TestAEADKAT pins the AEAD to RFC 8439 §2.8.2: the tag matches and the
// ciphertext round-trips; a single tampered byte fails authentication.
func TestAEADKAT(t *testing.T) {
	key := unhexT(t, "808182838485868788898a8b8c8d8e8f909192939495969798999a9b9c9d9e9f")
	nonce := unhexT(t, "070000004041424344454647")
	aad := unhexT(t, "50515253c0c1c2c3c4c5c6c7")
	pt := []byte("Ladies and Gentlemen of the class of '99: If I could offer you only one tip for the future, sunscreen would be it.")
	wantTag := unhexT(t, "1ae10b594f09e26a7e902ecbd0600691")

	out := aeadSeal(key, nonce, aad, pt)
	gotTag := out[len(out)-16:]
	if !bytes.Equal(gotTag, wantTag) {
		t.Errorf("AEAD tag:\n got %x\nwant %x", gotTag, wantTag)
	}

	rt, ok := aeadOpen(key, nonce, aad, out)
	if !ok {
		t.Fatal("aeadOpen rejected an authentic ciphertext")
	}
	if !bytes.Equal(rt, pt) {
		t.Errorf("round-trip mismatch:\n got %x\nwant %x", rt, pt)
	}

	bad := append([]byte(nil), out...)
	bad[0] ^= 0x80
	if _, ok := aeadOpen(key, nonce, aad, bad); ok {
		t.Error("aeadOpen accepted a tampered ciphertext")
	}
	bad2 := append([]byte(nil), out...)
	bad2[len(bad2)-1] ^= 0x01
	if _, ok := aeadOpen(key, nonce, aad, bad2); ok {
		t.Error("aeadOpen accepted a tampered tag")
	}
}
