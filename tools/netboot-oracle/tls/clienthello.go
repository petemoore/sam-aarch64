package tls

// The TLS 1.3 ClientHello builder (RFC 8446 §4.1.2) for our fixed offer —
// the host authority for the Z80 src/netboot/tls_client_hello.asm leaf (brick
// 3). Lifted from the inline oracle in z80/tls_client_hello_test.go so the Z80
// port and this authority emit byte-identical messages.

// sigAlgs is our fixed signature_algorithms offer (8 schemes, 16 bytes). An
// ECDSA-P256 or RSA-PSS server certificate can sign with a scheme in this list.
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

// BuildClientHello emits the ClientHello handshake message for our fixed offer:
// the supplied 32-byte random and legacy_session_id, cipher suite
// TLS_CHACHA20_POLY1305_SHA256, and the four required extensions
// (supported_versions=TLS1.3, supported_groups=x25519, signature_algorithms,
// key_share=x25519 with pub) plus SNI=host.
func BuildClientHello(random, sid, pub []byte, host string) []byte {
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
