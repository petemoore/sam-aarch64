package tls

// TLS 1.3 record protection (RFC 8446 §5.2/§5.3) — the host authority for the
// Z80 src/netboot/tls_record.asm leaf (brick 2). Framing is lifted from the
// inline oracle in z80/tls_record_test.go; Seal/Open add the AEAD call so the
// state machine can encrypt/decrypt real records against crypto/tls.

import "encoding/binary"

// recordNonce builds the per-record nonce: iv XOR seq (the 64-bit record sequence
// number, big-endian, right-aligned in the 12-byte iv) — RFC 8446 §5.3.
func recordNonce(iv []byte, seq uint64) []byte {
	nonce := make([]byte, 12)
	copy(nonce, iv)
	var seq8 [8]byte
	binary.BigEndian.PutUint64(seq8[:], seq)
	for i := 0; i < 8; i++ {
		nonce[4+i] ^= seq8[i]
	}
	return nonce
}

// Framing reconstructs the §5.2/§5.3 inputs: the nonce, the 5-byte
// TLSCiphertext header used as AEAD AAD, and the TLSInnerPlaintext
// (content || real_content_type, no padding). The AAD length field covers the
// inner plaintext plus the 16-byte tag.
func Framing(iv []byte, seq uint64, typ byte, content []byte) (nonce, aad, inner []byte) {
	nonce = recordNonce(iv, seq)
	inner = append(append([]byte{}, content...), typ)
	L := len(inner) + 16
	aad = []byte{0x17, 0x03, 0x03, byte(L >> 8), byte(L)}
	return
}

// Seal produces a complete TLSCiphertext record (header || ciphertext || tag)
// protecting content of real content type typ under (key, iv) at record
// sequence number seq.
func Seal(key, iv []byte, seq uint64, typ byte, content []byte) []byte {
	nonce, aad, inner := Framing(iv, seq, typ, content)
	ct := aeadSeal(key, nonce, aad, inner)
	return append(append([]byte{}, aad...), ct...)
}

// Open authenticates and decrypts a TLSCiphertext record, returning the real
// content type and content. ok is false if the record is malformed or the tag
// does not verify. Trailing zero padding is stripped, then the content-type byte.
func Open(key, iv []byte, seq uint64, record []byte) (ok bool, typ byte, content []byte) {
	if len(record) < 5+16 {
		return false, 0, nil
	}
	aad := record[:5]
	ctAndTag := record[5:]
	nonce := recordNonce(iv, seq)
	pt, good := aeadOpen(key, nonce, aad, ctAndTag)
	if !good {
		return false, 0, nil
	}
	i := len(pt) - 1
	for i >= 0 && pt[i] == 0 { // strip TLSInnerPlaintext zero padding
		i--
	}
	if i < 0 {
		return false, 0, nil
	}
	return true, pt[i], pt[:i]
}
