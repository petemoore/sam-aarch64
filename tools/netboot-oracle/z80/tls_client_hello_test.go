// tls_client_hello_test.go — the i88 host-verification of the TLS 1.3 ClientHello
// builder (src/netboot/tls_client_hello.asm), handshake brick 3. It assembles the
// standalone module under the koron-go/z80 harness, runs tls_build_client_hello,
// and asserts three independent properties of the emitted handshake message:
//
//	(a) it is byte-identical to a from-scratch Go reconstruction of the RFC 8446
//	    §4.1.2 framing (a golden-exactness anchor);
//	(b) Go crypto/tls (the stdlib TLS implementation) parses it into a
//	    tls.ClientHelloInfo with the expected ServerName / CipherSuites /
//	    SupportedVersions / SupportedCurves / SupportedSignatureSchemes — a
//	    genuinely independent structural check that also fails on any malformed
//	    message; and
//	(c) a hand-written TLV walk recovers the key_share entry with group x25519 and
//	    the supplied 32-byte public key.
package z80_test

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"os"
	"testing"
	"time"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	chBinPath = "../../../build/netboot_tls_client_hello.bin"
	chMapPath = "../../../build/netboot_tls_client_hello.map"
)

func loadCH(t *testing.T) *z80h.Machine {
	t.Helper()
	if _, err := os.Stat(chBinPath); err != nil {
		t.Fatalf("tls_client_hello binary not built (%s); run `make netboot-tls-client-hello`", chBinPath)
	}
	mac, err := z80h.Load(chBinPath, chMapPath)
	if err != nil {
		t.Fatalf("load tls_client_hello: %v", err)
	}
	return mac
}

// buildClientHelloZ80 runs tls_build_client_hello and returns the emitted message.
func buildClientHelloZ80(t *testing.T, random, sid, pub []byte, host string) []byte {
	t.Helper()
	mac := loadCH(t)
	mac.Write(mustSym(t, mac, "CH_RANDOM"), random)
	mac.Write(mustSym(t, mac, "CH_SESSION_ID"), sid)
	mac.Write(mustSym(t, mac, "CH_PUBKEY"), pub)
	mac.Write(mustSym(t, mac, "CH_HOSTNAME"), []byte(host))
	mac.WriteU16LE(mustSym(t, mac, "CH_HOSTNAME_LEN"), uint16(len(host)))
	if _, err := mac.CallEntry("tls_build_client_hello", z80h.Entry{}); err != nil {
		t.Fatalf("tls_build_client_hello: %v", err)
	}
	n := readU16LE(mac, mustSym(t, mac, "CH_MSG_LEN"))
	return mac.Read(mustSym(t, mac, "CH_MSG"), n)
}

// sigAlgs is the fixed signature_algorithms offer (8 schemes, 16 bytes).
var sigAlgs = []byte{
	0x04, 0x03, // ecdsa_secp256r1_sha256
	0x08, 0x04, // rsa_pss_rsae_sha256
	0x04, 0x01, // rsa_pkcs1_sha256
	0x05, 0x03, // ecdsa_secp384r1_sha384
	0x08, 0x05, // rsa_pss_rsae_sha384
	0x05, 0x01, // rsa_pkcs1_sha384
	0x08, 0x06, // rsa_pss_rsae_sha512
	0x06, 0x01, // rsa_pkcs1_sha512
}

// buildClientHelloGo reconstructs the §4.1.2 framing from scratch — the golden.
func buildClientHelloGo(random, sid, pub []byte, host string) []byte {
	be16 := func(n int) []byte { return []byte{byte(n >> 8), byte(n)} }
	hb := []byte(host)

	// server_name extension_data = ServerNameList = list_len || ServerName,
	// ServerName = name_type(0) || HostName(len16 || host).
	sni := append([]byte{0x00}, be16(len(hb))...)
	sni = append(sni, hb...)
	snl := append(be16(len(sni)), sni...)

	var ext []byte
	ext = append(ext, 0x00, 0x00)        // server_name
	ext = append(ext, be16(len(snl))...) // extension_data length
	ext = append(ext, snl...)
	ext = append(ext, 0x00, 0x2b, 0x00, 0x03, 0x02, 0x03, 0x04)       // supported_versions = TLS1.3
	ext = append(ext, 0x00, 0x0a, 0x00, 0x04, 0x00, 0x02, 0x00, 0x1d) // supported_groups = x25519
	ext = append(ext, 0x00, 0x0d, 0x00, 0x12, 0x00, 0x10)             // signature_algorithms header
	ext = append(ext, sigAlgs...)
	ext = append(ext, 0x00, 0x33, 0x00, 0x26, 0x00, 0x24, 0x00, 0x1d, 0x00, 0x20) // key_share prefix
	ext = append(ext, pub...)

	var body []byte
	body = append(body, 0x03, 0x03) // legacy_version
	body = append(body, random...)
	body = append(body, byte(len(sid)))
	body = append(body, sid...)
	body = append(body, 0x00, 0x02, 0x13, 0x03) // cipher_suites = TLS_CHACHA20_POLY1305_SHA256
	body = append(body, 0x01, 0x00)             // compression = null
	body = append(body, be16(len(ext))...)
	body = append(body, ext...)

	msg := []byte{0x01, byte(len(body) >> 16), byte(len(body) >> 8), byte(len(body))}
	return append(msg, body...)
}

// chFields is the result of a hand-written ClientHello TLV walk.
type chFields struct {
	random       []byte
	sessionID    []byte
	cipherSuites []byte
	exts         map[uint16][]byte
}

func walkClientHello(b []byte) (*chFields, error) {
	if len(b) < 4 || b[0] != 0x01 {
		return nil, fmt.Errorf("not a client_hello handshake message")
	}
	hlen := int(b[1])<<16 | int(b[2])<<8 | int(b[3])
	if 4+hlen != len(b) {
		return nil, fmt.Errorf("handshake length %d != body %d", hlen, len(b)-4)
	}
	p := b[4:]
	rd := func(n int) ([]byte, error) {
		if len(p) < n {
			return nil, fmt.Errorf("truncated (want %d, have %d)", n, len(p))
		}
		v := p[:n]
		p = p[n:]
		return v, nil
	}
	if _, err := rd(2); err != nil { // legacy_version
		return nil, err
	}
	f := &chFields{exts: map[uint16][]byte{}}
	var err error
	if f.random, err = rd(32); err != nil {
		return nil, err
	}
	sidLen, err := rd(1)
	if err != nil {
		return nil, err
	}
	if f.sessionID, err = rd(int(sidLen[0])); err != nil {
		return nil, err
	}
	csLen, err := rd(2)
	if err != nil {
		return nil, err
	}
	if f.cipherSuites, err = rd(int(csLen[0])<<8 | int(csLen[1])); err != nil {
		return nil, err
	}
	compLen, err := rd(1)
	if err != nil {
		return nil, err
	}
	if _, err = rd(int(compLen[0])); err != nil {
		return nil, err
	}
	extLen, err := rd(2)
	if err != nil {
		return nil, err
	}
	exts, err := rd(int(extLen[0])<<8 | int(extLen[1]))
	if err != nil {
		return nil, err
	}
	if len(p) != 0 {
		return nil, fmt.Errorf("%d trailing bytes after extensions", len(p))
	}
	for len(exts) > 0 {
		if len(exts) < 4 {
			return nil, fmt.Errorf("truncated extension header")
		}
		typ := uint16(exts[0])<<8 | uint16(exts[1])
		dlen := int(exts[2])<<8 | int(exts[3])
		if len(exts) < 4+dlen {
			return nil, fmt.Errorf("truncated extension %#04x", typ)
		}
		f.exts[typ] = exts[4 : 4+dlen]
		exts = exts[4+dlen:]
	}
	return f, nil
}

// keyShareX25519 pulls the x25519 key_exchange out of a key_share extension body.
func keyShareX25519(extData []byte) ([]byte, error) {
	if len(extData) < 2 {
		return nil, fmt.Errorf("key_share too short")
	}
	listLen := int(extData[0])<<8 | int(extData[1])
	list := extData[2:]
	if len(list) != listLen {
		return nil, fmt.Errorf("key_share client_shares length %d != %d", listLen, len(list))
	}
	for len(list) >= 4 {
		group := uint16(list[0])<<8 | uint16(list[1])
		klen := int(list[2])<<8 | int(list[3])
		if len(list) < 4+klen {
			return nil, fmt.Errorf("truncated KeyShareEntry")
		}
		if group == 0x001d {
			return list[4 : 4+klen], nil
		}
		list = list[4+klen:]
	}
	return nil, fmt.Errorf("no x25519 KeyShareEntry")
}

// parseCHViaTLS feeds the ClientHello (wrapped in an initial-handshake plaintext
// record) to a Go crypto/tls server and captures the parsed ClientHelloInfo,
// aborting the handshake from GetConfigForClient right after the parse. A nil
// return (or a malformed message) means crypto/tls rejected it.
func parseCHViaTLS(t *testing.T, ch []byte) *tls.ClientHelloInfo {
	t.Helper()
	cli, srv := net.Pipe()
	deadline := time.Now().Add(5 * time.Second)
	cli.SetDeadline(deadline)
	srv.SetDeadline(deadline)
	captured := make(chan *tls.ClientHelloInfo, 1)

	go func() {
		cfg := &tls.Config{
			GetConfigForClient: func(chi *tls.ClientHelloInfo) (*tls.Config, error) {
				c := *chi
				captured <- &c
				return nil, fmt.Errorf("captured; stop")
			},
		}
		s := tls.Server(srv, cfg)
		_ = s.Handshake()
		srv.Close()
	}()
	go io.Copy(io.Discard, cli) // drain any server alert so writes don't block
	rec := append([]byte{0x16, 0x03, 0x01, byte(len(ch) >> 8), byte(len(ch))}, ch...)
	go cli.Write(rec)

	select {
	case chi := <-captured:
		cli.Close()
		return chi
	case <-time.After(5 * time.Second):
		cli.Close()
		return nil
	}
}

func contains16(haystack []uint16, needle uint16) bool {
	for _, v := range haystack {
		if v == needle {
			return true
		}
	}
	return false
}

func chInputs() (random, sid, pub []byte) {
	random = make([]byte, 32)
	sid = make([]byte, 32)
	pub = make([]byte, 32)
	for i := 0; i < 32; i++ {
		random[i] = byte(i*7 + 3)
		sid[i] = byte(i*11 + 0x40)
		pub[i] = byte(i*5 + 0x80)
	}
	return
}

// TestTLSClientHelloGolden: the emitted message is byte-identical to the
// independent Go reconstruction, across host names of different lengths.
func TestTLSClientHelloGolden(t *testing.T) {
	random, sid, pub := chInputs()
	for _, host := range []string{"github.com", "raw.githubusercontent.com", "a", "objects.githubusercontent.com"} {
		got := buildClientHelloZ80(t, random, sid, pub, host)
		want := buildClientHelloGo(random, sid, pub, host)
		if !bytes.Equal(got, want) {
			t.Errorf("host=%q:\n  got  = %x\n  want = %x", host, got, want)
		}
	}
}

// TestTLSClientHelloParsedByCryptoTLS: crypto/tls parses the emitted message and
// reports the offer we built (an independent structural check).
func TestTLSClientHelloParsedByCryptoTLS(t *testing.T) {
	random, sid, pub := chInputs()
	const host = "github.com"
	ch := buildClientHelloZ80(t, random, sid, pub, host)

	chi := parseCHViaTLS(t, ch)
	if chi == nil {
		t.Fatal("crypto/tls did not parse the ClientHello (no ClientHelloInfo captured)")
	}
	if chi.ServerName != host {
		t.Errorf("ServerName = %q, want %q", chi.ServerName, host)
	}
	if !contains16(chi.CipherSuites, tls.TLS_CHACHA20_POLY1305_SHA256) {
		t.Errorf("CipherSuites = %#04x, want TLS_CHACHA20_POLY1305_SHA256", chi.CipherSuites)
	}
	if len(chi.CipherSuites) != 1 {
		t.Errorf("offered %d cipher suites, want exactly 1", len(chi.CipherSuites))
	}
	if !contains16(chi.SupportedVersions, tls.VersionTLS13) {
		t.Errorf("SupportedVersions = %#04x, want TLS 1.3", chi.SupportedVersions)
	}
	if !containsCurve(chi.SupportedCurves, tls.X25519) {
		t.Errorf("SupportedCurves = %v, want X25519", chi.SupportedCurves)
	}
	if !containsSig(chi.SignatureSchemes, tls.ECDSAWithP256AndSHA256) {
		t.Errorf("SignatureSchemes = %v, want ecdsa_secp256r1_sha256 present", chi.SignatureSchemes)
	}
	if !containsSig(chi.SignatureSchemes, tls.PSSWithSHA256) {
		t.Errorf("SignatureSchemes = %v, want rsa_pss_rsae_sha256 present", chi.SignatureSchemes)
	}
}

func containsCurve(s []tls.CurveID, v tls.CurveID) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func containsSig(s []tls.SignatureScheme, v tls.SignatureScheme) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// TestTLSClientHelloKeyShare: the key_share extension carries group x25519 and the
// supplied 32-byte public key, and the structural walk is self-consistent.
func TestTLSClientHelloKeyShare(t *testing.T) {
	random, sid, pub := chInputs()
	const host = "github.com"
	ch := buildClientHelloZ80(t, random, sid, pub, host)

	f, err := walkClientHello(ch)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if !bytes.Equal(f.random, random) {
		t.Errorf("random mismatch:\n got %x\nwant %x", f.random, random)
	}
	if !bytes.Equal(f.sessionID, sid) {
		t.Errorf("session_id mismatch:\n got %x\nwant %x", f.sessionID, sid)
	}
	if !bytes.Equal(f.cipherSuites, []byte{0x13, 0x03}) {
		t.Errorf("cipher_suites = %x, want 1303", f.cipherSuites)
	}
	ks, ok := f.exts[0x0033]
	if !ok {
		t.Fatal("no key_share extension")
	}
	got, err := keyShareX25519(ks)
	if err != nil {
		t.Fatalf("key_share: %v", err)
	}
	if !bytes.Equal(got, pub) {
		t.Errorf("key_share pubkey mismatch:\n got %x\nwant %x", got, pub)
	}
	// the four required extensions plus SNI are all present
	for _, typ := range []uint16{0x0000, 0x002b, 0x000a, 0x000d, 0x0033} {
		if _, ok := f.exts[typ]; !ok {
			t.Errorf("missing extension %#04x", typ)
		}
	}
}
