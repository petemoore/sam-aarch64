// tls_server_flight_test.go — the i88 host-verification of the TLS 1.3
// server-flight parser (src/netboot/tls_server_flight.asm), handshake brick 4. It
// assembles the standalone module under the koron-go/z80 harness and asserts:
//
//   - tls_parse_server_hello recovers the X25519 key_share from a hand-built
//     ServerHello (SH_OK=1, SH_SERVER_PUB == the server pub) and rejects (SH_OK=0)
//     a wrong cipher suite, a missing key_share, a non-1.3 supported_versions, and
//     the HelloRetryRequest sentinel random;
//   - tls_walk_server_flight over a hand-built EncryptedExtensions / Certificate /
//     CertificateVerify / Finished flight captures the Finished verify_data, sets
//     SF_OK=1 with all four flags, and that SF_HASH_BEFORE_FIN == Go
//     SHA-256(EE||Cert||CertVerify) while a post-walk transcript snapshot == Go
//     SHA-256(the whole flight) — proving the messages were folded in order with
//     the snapshot taken just before the Finished.
package z80_test

import (
	"bytes"
	"crypto/sha256"
	"os"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	sfBinPath = "../../../build/netboot_tls_server_flight.bin"
	sfMapPath = "../../../build/netboot_tls_server_flight.map"
)

func loadSF(t *testing.T) *z80h.Machine {
	t.Helper()
	if _, err := os.Stat(sfBinPath); err != nil {
		t.Skipf("tls_server_flight binary not built (%s); run `make netboot-tls-server-flight`", sfBinPath)
	}
	mac, err := z80h.Load(sfBinPath, sfMapPath)
	if err != nil {
		t.Fatalf("load tls_server_flight: %v", err)
	}
	return mac
}

func bytePattern(n, seed int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*seed + seed + 1)
	}
	return b
}

// hrrSentinel is SHA-256("HelloRetryRequest"), the ServerHello.random that signals
// a HelloRetryRequest (RFC 8446 §4.1.3).
var hrrSentinel = []byte{
	0xCF, 0x21, 0xAD, 0x74, 0xE5, 0x9A, 0x61, 0x11,
	0xBE, 0x1D, 0x8C, 0x02, 0x1E, 0x65, 0xB8, 0x91,
	0xC2, 0xA2, 0x11, 0x16, 0x7A, 0xBB, 0x8C, 0x5E,
	0x07, 0x9E, 0x09, 0xE2, 0xC8, 0xA8, 0x33, 0x9C,
}

// buildServerHello hand-encodes a ServerHello handshake message (RFC 8446 §4.1.3):
// version(0x0303) || random || session_id || cipher_suite || compression(0) ||
// extensions{ supported_versions=selected, [key_share x25519] }.
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

// parseServerHello runs tls_parse_server_hello and returns (ok, server_pub).
func parseServerHello(t *testing.T, sh []byte) (bool, []byte) {
	t.Helper()
	mac := loadSF(t)
	mac.Write(mustSym(t, mac, "SH_MSG"), sh)
	if _, err := mac.Call("tls_parse_server_hello"); err != nil {
		t.Fatalf("tls_parse_server_hello: %v", err)
	}
	ok := mac.Read(mustSym(t, mac, "SH_OK"), 1)[0] == 1
	pub := mac.Read(mustSym(t, mac, "SH_SERVER_PUB"), 32)
	return ok, pub
}

// TestTLSParseServerHelloValid: a well-formed ServerHello yields SH_OK=1 and the
// server's X25519 key_share.
func TestTLSParseServerHelloValid(t *testing.T) {
	random := bytePattern(32, 3)
	sid := bytePattern(32, 7)
	serverPub := bytePattern(32, 5)
	sh := buildServerHello(random, sid, serverPub, 0x1303, 0x0304, true)

	ok, pub := parseServerHello(t, sh)
	if !ok {
		t.Fatal("SH_OK=0 for a valid ServerHello")
	}
	if !bytes.Equal(pub, serverPub) {
		t.Errorf("SH_SERVER_PUB mismatch:\n got %x\nwant %x", pub, serverPub)
	}
}

// TestTLSParseServerHelloVariableSessionID: the session_id_echo length is honoured
// (a zero-length and a 32-byte echo both parse and locate the same key_share).
func TestTLSParseServerHelloVariableSessionID(t *testing.T) {
	random := bytePattern(32, 9)
	serverPub := bytePattern(32, 5)
	for _, sidLen := range []int{0, 1, 16, 32} {
		sid := bytePattern(sidLen, 13)
		sh := buildServerHello(random, sid, serverPub, 0x1303, 0x0304, true)
		ok, pub := parseServerHello(t, sh)
		if !ok || !bytes.Equal(pub, serverPub) {
			t.Errorf("sidLen=%d: ok=%v pub-match=%v", sidLen, ok, bytes.Equal(pub, serverPub))
		}
	}
}

// TestTLSParseServerHelloRejects: each malformed/unsupported ServerHello is rejected.
func TestTLSParseServerHelloRejects(t *testing.T) {
	random := bytePattern(32, 3)
	sid := bytePattern(32, 7)
	serverPub := bytePattern(32, 5)

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
		if ok, _ := parseServerHello(t, tc.sh); ok {
			t.Errorf("%s: SH_OK=1, want rejection", tc.name)
		}
	}
	// a handshake message that is not a ServerHello (type 2) is rejected
	notSH := append([]byte{0x01}, buildServerHello(random, sid, serverPub, 0x1303, 0x0304, true)[1:]...)
	if ok, _ := parseServerHello(t, notSH); ok {
		t.Error("non-ServerHello handshake type accepted")
	}
}

// TestTLSWalkServerFlight: the walk folds EE/Cert/CertVerify/Finished into the
// transcript in order, snapshots before the Finished, and captures the verify_data.
func TestTLSWalkServerFlight(t *testing.T) {
	ee := hsMsg(8, []byte{0x00, 0x00}) // EncryptedExtensions: empty extensions
	cert := hsMsg(11, bytePattern(500, 17))
	cv := hsMsg(15, bytePattern(256, 23))
	verifyData := bytePattern(32, 29)
	fin := hsMsg(20, verifyData)
	flight := bytes.Join([][]byte{ee, cert, cv, fin}, nil)
	beforeFin := bytes.Join([][]byte{ee, cert, cv}, nil)

	mac := loadSF(t)
	mac.Write(mustSym(t, mac, "SF_FLIGHT"), flight)
	mac.WriteU16LE(mustSym(t, mac, "SF_FLIGHT_LEN"), uint16(len(flight)))
	if _, err := mac.Call("tls_transcript_init"); err != nil {
		t.Fatalf("tls_transcript_init: %v", err)
	}
	if _, err := mac.Call("tls_walk_server_flight"); err != nil {
		t.Fatalf("tls_walk_server_flight: %v", err)
	}

	if mac.Read(mustSym(t, mac, "SF_OK"), 1)[0] != 1 {
		t.Fatal("SF_OK=0; expected all four flight messages seen")
	}
	for _, f := range []string{"sf_saw_ee", "sf_saw_cert", "sf_saw_cv", "sf_saw_fin"} {
		if mac.Read(mustSym(t, mac, f), 1)[0] != 1 {
			t.Errorf("%s not set", f)
		}
	}

	finLen := readU16LE(mac, mustSym(t, mac, "SF_FINISHED_LEN"))
	if finLen != len(verifyData) {
		t.Fatalf("SF_FINISHED_LEN = %d, want %d", finLen, len(verifyData))
	}
	if got := mac.Read(mustSym(t, mac, "SF_FINISHED"), finLen); !bytes.Equal(got, verifyData) {
		t.Errorf("captured Finished mismatch:\n got %x\nwant %x", got, verifyData)
	}

	// the snapshot taken right before the Finished == SHA-256(EE||Cert||CertVerify)
	wantBefore := sha256.Sum256(beforeFin)
	if got := mac.Read(mustSym(t, mac, "SF_HASH_BEFORE_FIN"), 32); !bytes.Equal(got, wantBefore[:]) {
		t.Errorf("SF_HASH_BEFORE_FIN mismatch:\n got %x\nwant %x", got, wantBefore[:])
	}

	// a snapshot of the full transcript after the walk == SHA-256(the whole flight)
	if _, err := mac.CallEntry("tls_transcript_snapshot", z80h.Entry{HL: mustSym(t, mac, "SF_SNAP")}); err != nil {
		t.Fatalf("tls_transcript_snapshot: %v", err)
	}
	wantAll := sha256.Sum256(flight)
	if got := mac.Read(mustSym(t, mac, "SF_SNAP"), 32); !bytes.Equal(got, wantAll[:]) {
		t.Errorf("post-walk transcript mismatch:\n got %x\nwant %x", got, wantAll[:])
	}
}

// TestTLSWalkServerFlightWithPrefix: when the transcript already holds a
// ClientHello..ServerHello prefix (as in the real handshake), the walk's snapshots
// include it — SF_HASH_BEFORE_FIN == SHA-256(prefix||EE||Cert||CertVerify).
func TestTLSWalkServerFlightWithPrefix(t *testing.T) {
	ch := hsMsg(1, bytePattern(120, 2)) // a stand-in ClientHello
	shp := hsMsg(2, bytePattern(90, 4)) // a stand-in ServerHello
	prefix := append(append([]byte{}, ch...), shp...)

	ee := hsMsg(8, []byte{0x00, 0x00})
	cert := hsMsg(11, bytePattern(300, 17))
	cv := hsMsg(15, bytePattern(180, 23))
	fin := hsMsg(20, bytePattern(32, 29))
	flight := bytes.Join([][]byte{ee, cert, cv, fin}, nil)

	mac := loadSF(t)
	mac.Write(mustSym(t, mac, "SF_FLIGHT"), flight)
	mac.WriteU16LE(mustSym(t, mac, "SF_FLIGHT_LEN"), uint16(len(flight)))
	if _, err := mac.Call("tls_transcript_init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	// feed the CH..SH prefix the way brick 6 will, via tls_transcript_update. The
	// prefix bytes live in free low RAM (the module loads at &8000); 0x4000 is
	// clear of the harness stack (&6FFE) and the loaded code.
	const prefixAddr = 0x4000
	mac.Write(prefixAddr, prefix)
	if _, err := mac.CallEntry("tls_transcript_update", z80h.Entry{HL: prefixAddr, BC: uint16(len(prefix))}); err != nil {
		t.Fatalf("prefix update: %v", err)
	}
	if _, err := mac.Call("tls_walk_server_flight"); err != nil {
		t.Fatalf("walk: %v", err)
	}

	wantBefore := sha256.Sum256(bytes.Join([][]byte{prefix, ee, cert, cv}, nil))
	if got := mac.Read(mustSym(t, mac, "SF_HASH_BEFORE_FIN"), 32); !bytes.Equal(got, wantBefore[:]) {
		t.Errorf("SF_HASH_BEFORE_FIN (with prefix) mismatch:\n got %x\nwant %x", got, wantBefore[:])
	}
}
