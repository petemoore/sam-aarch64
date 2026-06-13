package tls

// Record-layer authority tests: Seal then Open round-trips (content, type)
// across content types / lengths / sequence numbers, and tampering or a wrong
// sequence number fails authentication. The AEAD itself is pinned by aead_test.go.

import (
	"bytes"
	"testing"
)

func trKey() []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i*9 + 1)
	}
	return k
}

func trIV() []byte {
	v := make([]byte, 12)
	for i := range v {
		v[i] = byte(i*5 + 0xa0)
	}
	return v
}

func TestRecordRoundTrip(t *testing.T) {
	key, iv := trKey(), trIV()
	for _, typ := range []byte{22, 23, 21} {
		for _, n := range []int{0, 1, 5, 16, 17, 64, 255, 500} {
			for _, seq := range []uint64{0, 1, 42, 0xffffffffffffffff} {
				content := make([]byte, n)
				for i := range content {
					content[i] = byte(i*31 + int(typ))
				}
				rec := Seal(key, iv, seq, typ, content)
				ok, gotType, gotContent := Open(key, iv, seq, rec)
				if !ok {
					t.Errorf("type=%d n=%d seq=%d: Open rejected an authentic record", typ, n, seq)
					continue
				}
				if gotType != typ {
					t.Errorf("type=%d n=%d seq=%d: recovered type %d", typ, n, seq, gotType)
				}
				if !bytes.Equal(gotContent, content) {
					t.Errorf("type=%d n=%d seq=%d: content mismatch", typ, n, seq)
				}
			}
		}
	}
}

func TestRecordTamper(t *testing.T) {
	key, iv := trKey(), trIV()
	content := []byte("a TLS 1.3 handshake message fragment")
	rec := Seal(key, iv, 7, 22, content)

	if ok, _, _ := Open(key, iv, 7, rec); !ok {
		t.Fatal("baseline: authentic record rejected")
	}
	bad := append([]byte(nil), rec...)
	bad[8] ^= 0x40 // a ciphertext byte (past the 5-byte header)
	if ok, _, _ := Open(key, iv, 7, bad); ok {
		t.Error("tampered ciphertext accepted")
	}
	bad2 := append([]byte(nil), rec...)
	bad2[len(bad2)-1] ^= 0x01 // the tag
	if ok, _, _ := Open(key, iv, 7, bad2); ok {
		t.Error("tampered tag accepted")
	}
	if ok, _, _ := Open(key, iv, 8, rec); ok { // wrong sequence -> wrong nonce
		t.Error("record opened at the wrong sequence number")
	}
}
