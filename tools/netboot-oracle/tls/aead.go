package tls

// ChaCha20-Poly1305 AEAD (RFC 8439), implemented from scratch so the netboot
// authority stays a pure-stdlib module with no external dependency and its AEAD
// is genuinely independent of the copy crypto/tls uses internally. Correctness
// is pinned by the RFC 8439 known-answer vectors in aead_test.go; it is also
// transitively exercised by the full handshake against crypto/tls.Server.
//
// This is the host reference for the Z80 src/netboot/aead.asm leaf (already
// independently verified); the two implement the same RFC 8439 construction.

import (
	"crypto/subtle"
	"encoding/binary"
	"math/big"
)

func rotl32(x uint32, n uint) uint32 { return x<<n | x>>(32-n) }

// chachaQR is the ChaCha quarter-round on four state words (RFC 8439 §2.1).
func chachaQR(s *[16]uint32, a, b, c, d int) {
	s[a] += s[b]
	s[d] ^= s[a]
	s[d] = rotl32(s[d], 16)
	s[c] += s[d]
	s[b] ^= s[c]
	s[b] = rotl32(s[b], 12)
	s[a] += s[b]
	s[d] ^= s[a]
	s[d] = rotl32(s[d], 8)
	s[c] += s[d]
	s[b] ^= s[c]
	s[b] = rotl32(s[b], 7)
}

// chachaBlock produces the 64-byte ChaCha20 keystream block for (key, counter,
// nonce) — RFC 8439 §2.3. key is 32 bytes, nonce 12 bytes.
func chachaBlock(key []byte, counter uint32, nonce []byte) []byte {
	var s [16]uint32
	s[0], s[1], s[2], s[3] = 0x61707865, 0x3320646e, 0x79622d32, 0x6b206574
	for i := 0; i < 8; i++ {
		s[4+i] = binary.LittleEndian.Uint32(key[i*4:])
	}
	s[12] = counter
	s[13] = binary.LittleEndian.Uint32(nonce[0:])
	s[14] = binary.LittleEndian.Uint32(nonce[4:])
	s[15] = binary.LittleEndian.Uint32(nonce[8:])

	w := s
	for i := 0; i < 10; i++ {
		chachaQR(&w, 0, 4, 8, 12)
		chachaQR(&w, 1, 5, 9, 13)
		chachaQR(&w, 2, 6, 10, 14)
		chachaQR(&w, 3, 7, 11, 15)
		chachaQR(&w, 0, 5, 10, 15)
		chachaQR(&w, 1, 6, 11, 12)
		chachaQR(&w, 2, 7, 8, 13)
		chachaQR(&w, 3, 4, 9, 14)
	}
	out := make([]byte, 64)
	for i := 0; i < 16; i++ {
		binary.LittleEndian.PutUint32(out[i*4:], w[i]+s[i])
	}
	return out
}

// chacha20 encrypts/decrypts in by XORing the ChaCha20 keystream starting at the
// given block counter (RFC 8439 §2.4).
func chacha20(key []byte, counter uint32, nonce, in []byte) []byte {
	out := make([]byte, len(in))
	for off := 0; off < len(in); off += 64 {
		ks := chachaBlock(key, counter, nonce)
		counter++
		n := len(in) - off
		if n > 64 {
			n = 64
		}
		for i := 0; i < n; i++ {
			out[off+i] = in[off+i] ^ ks[i]
		}
	}
	return out
}

var poly1305P = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 130), big.NewInt(5)) // 2^130 - 5

func leToBig(b []byte) *big.Int {
	r := make([]byte, len(b))
	for i := range b {
		r[i] = b[len(b)-1-i]
	}
	return new(big.Int).SetBytes(r)
}

// bigToLE returns the low n bytes of x, little-endian (implicitly x mod 2^(8n)).
func bigToLE(x *big.Int, n int) []byte {
	be := x.Bytes()
	out := make([]byte, n)
	for i := 0; i < len(be) && i < n; i++ {
		out[i] = be[len(be)-1-i]
	}
	return out
}

// poly1305 computes the 16-byte one-time MAC of msg under the 32-byte key
// (r||s), per RFC 8439 §2.5. Uses math/big for the 2^130-5 arithmetic — clarity
// over speed, which is right for an authority.
func poly1305(msg, key []byte) []byte {
	r := make([]byte, 16)
	copy(r, key[0:16])
	r[3] &= 15
	r[7] &= 15
	r[11] &= 15
	r[15] &= 15
	r[4] &= 252
	r[8] &= 252
	r[12] &= 252
	rBig := leToBig(r)
	sBig := leToBig(key[16:32])

	acc := big.NewInt(0)
	for off := 0; off < len(msg); off += 16 {
		n := len(msg) - off
		if n > 16 {
			n = 16
		}
		block := make([]byte, n+1)
		copy(block, msg[off:off+n])
		block[n] = 1 // the high "1" bit beyond the block
		acc.Add(acc, leToBig(block))
		acc.Mul(acc, rBig)
		acc.Mod(acc, poly1305P)
	}
	acc.Add(acc, sBig)
	return bigToLE(acc, 16)
}

func pad16(n int) int {
	if n%16 == 0 {
		return 0
	}
	return 16 - (n % 16)
}

// macData is the Poly1305 input for the AEAD: aad || pad16 || ct || pad16 ||
// le64(len aad) || le64(len ct) — RFC 8439 §2.8.
func macData(aad, ct []byte) []byte {
	m := make([]byte, 0, len(aad)+pad16(len(aad))+len(ct)+pad16(len(ct))+16)
	m = append(m, aad...)
	m = append(m, make([]byte, pad16(len(aad)))...)
	m = append(m, ct...)
	m = append(m, make([]byte, pad16(len(ct)))...)
	var lens [16]byte
	binary.LittleEndian.PutUint64(lens[0:], uint64(len(aad)))
	binary.LittleEndian.PutUint64(lens[8:], uint64(len(ct)))
	return append(m, lens[:]...)
}

// aeadSeal returns ciphertext||tag for the AEAD_CHACHA20_POLY1305 construction
// (RFC 8439 §2.8). nonce is 12 bytes.
func aeadSeal(key, nonce, aad, plaintext []byte) []byte {
	otk := chachaBlock(key, 0, nonce)[:32] // poly1305_key_gen (counter 0)
	ct := chacha20(key, 1, nonce, plaintext)
	tag := poly1305(macData(aad, ct), otk)
	return append(ct, tag...)
}

// aeadOpen authenticates and decrypts ciphertext||tag, returning (plaintext,
// true) on success or (nil, false) if the tag does not verify.
func aeadOpen(key, nonce, aad, ctAndTag []byte) ([]byte, bool) {
	if len(ctAndTag) < 16 {
		return nil, false
	}
	ct := ctAndTag[:len(ctAndTag)-16]
	tag := ctAndTag[len(ctAndTag)-16:]
	otk := chachaBlock(key, 0, nonce)[:32]
	want := poly1305(macData(aad, ct), otk)
	if subtle.ConstantTimeCompare(want, tag) != 1 {
		return nil, false
	}
	return chacha20(key, 1, nonce, ct), true
}
