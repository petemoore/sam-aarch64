// aead_test.go — the i88 host-verification of AEAD_CHACHA20_POLY1305
// (src/netboot/aead.asm), the TLS 1.3 record-protection construction. It
// assembles the standalone module under the koron-go/z80 harness and asserts:
//   - aead_keygen matches the RFC 8439 §2.6.2 poly1305_key_gen vector;
//   - aead_encrypt matches the RFC 8439 §2.8.2 worked example (ciphertext + tag),
//     byte-for-byte;
//   - aead_decrypt is the inverse of aead_encrypt (round-trip recovers the
//     plaintext with AEAD_OK=1) across message lengths incl. empty / non-block;
//   - a tampered tag or ciphertext byte sets AEAD_OK=0 (authentication rejects).
package z80_test

import (
	"bytes"
	"os"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	aeadBinPath = "../../../build/netboot_aead.bin"
	aeadMapPath = "../../../build/netboot_aead.map"
)

func loadAEAD(t *testing.T) *z80h.Machine {
	t.Helper()
	if _, err := os.Stat(aeadBinPath); err != nil {
		t.Fatalf("aead binary not built (%s); run `make netboot-aead`", aeadBinPath)
	}
	mac, err := z80h.Load(aeadBinPath, aeadMapPath)
	if err != nil {
		t.Fatalf("load aead: %v", err)
	}
	return mac
}

// aeadEncrypt runs aead_encrypt and returns (ciphertext, tag).
func aeadEncrypt(t *testing.T, key, nonce, aad, pt []byte) ([]byte, []byte) {
	t.Helper()
	mac := loadAEAD(t)
	mac.Write(mustSym(t, mac, "AEAD_KEY"), key)
	mac.Write(mustSym(t, mac, "AEAD_NONCE"), nonce)
	mac.Write(mustSym(t, mac, "AEAD_AAD"), aad)
	mac.WriteU16LE(mustSym(t, mac, "AEAD_AAD_LEN"), uint16(len(aad)))
	mac.Write(mustSym(t, mac, "AEAD_PT"), pt)
	mac.WriteU16LE(mustSym(t, mac, "AEAD_LEN"), uint16(len(pt)))
	if _, err := mac.CallEntry("aead_encrypt", z80h.Entry{}); err != nil {
		t.Fatalf("aead_encrypt: %v", err)
	}
	ct := mac.Read(mustSym(t, mac, "AEAD_CT"), len(pt))
	tag := mac.Read(mustSym(t, mac, "AEAD_TAG"), 16)
	return ct, tag
}

// aeadDecrypt runs aead_decrypt and returns (ok, plaintext).
func aeadDecrypt(t *testing.T, key, nonce, aad, ct, tag []byte) (bool, []byte) {
	t.Helper()
	mac := loadAEAD(t)
	mac.Write(mustSym(t, mac, "AEAD_KEY"), key)
	mac.Write(mustSym(t, mac, "AEAD_NONCE"), nonce)
	mac.Write(mustSym(t, mac, "AEAD_AAD"), aad)
	mac.WriteU16LE(mustSym(t, mac, "AEAD_AAD_LEN"), uint16(len(aad)))
	mac.Write(mustSym(t, mac, "AEAD_CT"), ct)
	mac.WriteU16LE(mustSym(t, mac, "AEAD_LEN"), uint16(len(ct)))
	mac.Write(mustSym(t, mac, "AEAD_TAG"), tag)
	if _, err := mac.CallEntry("aead_decrypt", z80h.Entry{}); err != nil {
		t.Fatalf("aead_decrypt: %v", err)
	}
	ok := mac.Read(mustSym(t, mac, "AEAD_OK"), 1)[0] == 1
	pt := mac.Read(mustSym(t, mac, "AEAD_PT"), len(ct))
	return ok, pt
}

// TestAEADKeyGen: aead_keygen == RFC 8439 §2.6.2 poly1305_key_gen.
func TestAEADKeyGen(t *testing.T) {
	key := unhex(t, "808182838485868788898a8b8c8d8e8f909192939495969798999a9b9c9d9e9f")
	nonce := unhex(t, "000000000001020304050607")
	want := unhex(t, "8ad5a08b905f81cc815040274ab29471a833b637e3fd0da508dbb8e2fdd1a646")

	mac := loadAEAD(t)
	mac.Write(mustSym(t, mac, "AEAD_KEY"), key)
	mac.Write(mustSym(t, mac, "AEAD_NONCE"), nonce)
	if _, err := mac.CallEntry("aead_keygen", z80h.Entry{}); err != nil {
		t.Fatalf("aead_keygen: %v", err)
	}
	got := mac.Read(mustSym(t, mac, "AEAD_OTK"), 32)
	if !bytes.Equal(got, want) {
		t.Errorf("otk = %x, want %x", got, want)
	}
}

// TestAEADEncryptRFC: aead_encrypt == RFC 8439 §2.8.2 worked example.
func TestAEADEncryptRFC(t *testing.T) {
	key := unhex(t, "808182838485868788898a8b8c8d8e8f909192939495969798999a9b9c9d9e9f")
	nonce := unhex(t, "070000004041424344454647")
	aad := unhex(t, "50515253c0c1c2c3c4c5c6c7")
	pt := []byte("Ladies and Gentlemen of the class of '99: If I could offer you only one tip for the future, sunscreen would be it.")
	wantCT := unhex(t, "d31a8d34648e60db7b86afbc53ef7ec2"+
		"a4aded51296e08fea9e2b5a736ee62d6"+
		"3dbea45e8ca9671282fafb69da92728b"+
		"1a71de0a9e060b2905d6a5b67ecd3b36"+
		"92ddbd7f2d778b8c9803aee328091b58"+
		"fab324e4fad675945585808b4831d7bc"+
		"3ff4def08e4b7a9de576d26586cec64b"+
		"6116")
	wantTag := unhex(t, "1ae10b594f09e26a7e902ecbd0600691")

	ct, tag := aeadEncrypt(t, key, nonce, aad, pt)
	if !bytes.Equal(ct, wantCT) {
		t.Errorf("ciphertext = %x,\n      want = %x", ct, wantCT)
	}
	if !bytes.Equal(tag, wantTag) {
		t.Errorf("tag = %x, want %x", tag, wantTag)
	}
}

// TestAEADRoundTrip: aead_decrypt(aead_encrypt(pt)) == pt with AEAD_OK=1, across
// lengths (empty, partial, exact-block, multi-block) and with / without AAD.
func TestAEADRoundTrip(t *testing.T) {
	key := unhex(t, "404142434445464748494a4b4c4d4e4f505152535455565758595a5b5c5d5e5f")
	nonce := unhex(t, "070000004041424344454647")
	for _, n := range []int{0, 1, 15, 16, 17, 63, 64, 65, 114, 200} {
		for _, aadLen := range []int{0, 12} {
			pt := make([]byte, n)
			for i := range pt {
				pt[i] = byte(i*53 + 11)
			}
			aad := make([]byte, aadLen)
			for i := range aad {
				aad[i] = byte(i*29 + 3)
			}
			ct, tag := aeadEncrypt(t, key, nonce, aad, pt)
			if n > 0 && bytes.Equal(ct, pt) {
				t.Errorf("n=%d: ciphertext equals plaintext (not encrypted)", n)
			}
			ok, back := aeadDecrypt(t, key, nonce, aad, ct, tag)
			if !ok {
				t.Errorf("n=%d aad=%d: decrypt rejected an authentic message", n, aadLen)
			}
			if !bytes.Equal(back, pt) {
				t.Errorf("n=%d aad=%d: round-trip mismatch\n got %x\nwant %x", n, aadLen, back, pt)
			}
		}
	}
}

// TestAEADTamper: a flipped tag or ciphertext byte must fail authentication
// (AEAD_OK=0). Built on the §2.8.2 vector.
func TestAEADTamper(t *testing.T) {
	key := unhex(t, "808182838485868788898a8b8c8d8e8f909192939495969798999a9b9c9d9e9f")
	nonce := unhex(t, "070000004041424344454647")
	aad := unhex(t, "50515253c0c1c2c3c4c5c6c7")
	pt := []byte("Ladies and Gentlemen of the class of '99: If I could offer you only one tip for the future, sunscreen would be it.")
	ct, tag := aeadEncrypt(t, key, nonce, aad, pt)

	// Sanity: the untampered message authenticates.
	if ok, _ := aeadDecrypt(t, key, nonce, aad, ct, tag); !ok {
		t.Fatal("baseline: authentic message rejected")
	}
	// Flip one tag byte.
	badTag := append([]byte(nil), tag...)
	badTag[0] ^= 0x01
	if ok, _ := aeadDecrypt(t, key, nonce, aad, ct, badTag); ok {
		t.Error("tampered tag accepted")
	}
	// Flip one ciphertext byte.
	badCT := append([]byte(nil), ct...)
	badCT[5] ^= 0x80
	if ok, _ := aeadDecrypt(t, key, nonce, aad, badCT, tag); ok {
		t.Error("tampered ciphertext accepted")
	}
	// Flip one AAD byte.
	badAAD := append([]byte(nil), aad...)
	badAAD[2] ^= 0x40
	if ok, _ := aeadDecrypt(t, key, nonce, badAAD, ct, tag); ok {
		t.Error("tampered AAD accepted")
	}
}
