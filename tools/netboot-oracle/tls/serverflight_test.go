package tls

// Server-flight parsing authority tests, mirroring the Z80 brick-4 cases:
// ParseServerHello recovers the X25519 key_share and rejects a wrong cipher,
// a missing key_share, a non-1.3 version, and the HRR sentinel; WalkServerFlight
// splits EE/Cert/CertVerify/Finished and captures the verify_data.

import (
	"bytes"
	"testing"
)

func bytePattern(n, seed int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*seed + seed + 1)
	}
	return b
}

// buildServerHello hand-encodes a ServerHello (RFC 8446 §4.1.3) for the tests.
func buildServerHello(random, sid, serverPub []byte, cipher, version uint16, includeKS bool) []byte {
	be16 := func(n int) []byte { return []byte{byte(n >> 8), byte(n)} }
	var ext []byte
	ext = append(ext, 0x00, 0x2b, 0x00, 0x02, byte(version>>8), byte(version)) // supported_versions
	if includeKS {
		ext = append(ext, 0x00, 0x33)                  // key_share
		ext = append(ext, be16(2+2+len(serverPub))...) // extension_data length
		ext = append(ext, 0x00, 0x1d)                  // group = x25519
		ext = append(ext, be16(len(serverPub))...)     // key_exchange length
		ext = append(ext, serverPub...)
	}
	var body []byte
	body = append(body, 0x03, 0x03)
	body = append(body, random...)
	body = append(body, byte(len(sid)))
	body = append(body, sid...)
	body = append(body, byte(cipher>>8), byte(cipher))
	body = append(body, 0x00) // legacy_compression_method
	body = append(body, be16(len(ext))...)
	body = append(body, ext...)
	msg := []byte{0x02, byte(len(body) >> 16), byte(len(body) >> 8), byte(len(body))}
	return append(msg, body...)
}

// hsMsg wraps a body in a handshake header: type || uint24 length || body.
func hsMsg(typ byte, body []byte) []byte {
	h := []byte{typ, byte(len(body) >> 16), byte(len(body) >> 8), byte(len(body))}
	return append(h, body...)
}

func TestParseServerHelloValid(t *testing.T) {
	serverPub := bytePattern(32, 5)
	for _, sidLen := range []int{0, 1, 16, 32} {
		sh := buildServerHello(bytePattern(32, 3), bytePattern(sidLen, 13), serverPub, 0x1303, 0x0304, true)
		pub, ok := ParseServerHello(sh)
		if !ok {
			t.Fatalf("sidLen=%d: ok=false for a valid ServerHello", sidLen)
		}
		if !bytes.Equal(pub[:], serverPub) {
			t.Errorf("sidLen=%d: server pub mismatch", sidLen)
		}
	}
}

func TestParseServerHelloRejects(t *testing.T) {
	random, sid, serverPub := bytePattern(32, 3), bytePattern(32, 7), bytePattern(32, 5)
	cases := []struct {
		name string
		sh   []byte
	}{
		{"wrong cipher (AES-128-GCM)", buildServerHello(random, sid, serverPub, 0x1301, 0x0304, true)},
		{"no key_share", buildServerHello(random, sid, serverPub, 0x1303, 0x0304, false)},
		{"non-1.3 version (TLS 1.2)", buildServerHello(random, sid, serverPub, 0x1303, 0x0303, true)},
		{"HelloRetryRequest sentinel", buildServerHello(hrrSentinel, sid, serverPub, 0x1303, 0x0304, true)},
	}
	for _, tc := range cases {
		if _, ok := ParseServerHello(tc.sh); ok {
			t.Errorf("%s: ok=true, want rejection", tc.name)
		}
	}
	notSH := append([]byte{0x01}, buildServerHello(random, sid, serverPub, 0x1303, 0x0304, true)[1:]...)
	if _, ok := ParseServerHello(notSH); ok {
		t.Error("non-ServerHello handshake type accepted")
	}
}

func TestWalkServerFlight(t *testing.T) {
	ee := hsMsg(8, []byte{0x00, 0x00})
	cert := hsMsg(11, bytePattern(500, 17))
	cv := hsMsg(15, bytePattern(256, 23))
	verifyData := bytePattern(32, 29)
	fin := hsMsg(20, verifyData)
	flight := bytes.Join([][]byte{ee, cert, cv, fin}, nil)
	beforeFin := bytes.Join([][]byte{ee, cert, cv}, nil)

	// A partial buffer (everything but the last byte of the Finished) is not yet
	// complete.
	if _, complete, err := WalkServerFlight(flight[:len(flight)-1]); err != nil || complete {
		t.Fatalf("partial flight: complete=%v err=%v, want incomplete", complete, err)
	}

	sf, complete, err := WalkServerFlight(flight)
	if err != nil || !complete {
		t.Fatalf("WalkServerFlight: complete=%v err=%v", complete, err)
	}
	if !bytes.Equal(sf.BeforeFin, beforeFin) {
		t.Errorf("BeforeFin mismatch")
	}
	if !bytes.Equal(sf.Finished, fin) {
		t.Errorf("Finished message mismatch")
	}
	if !bytes.Equal(sf.VerifyData, verifyData) {
		t.Errorf("verify_data mismatch:\n got %x\nwant %x", sf.VerifyData, verifyData)
	}
}
