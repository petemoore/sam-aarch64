package tls

// The TLS 1.3 server-flight parsing authority — the host reference for the Z80
// src/netboot/tls_server_flight.asm leaf (brick 4): ParseServerHello extracts
// the server key_share and validates the negotiated suite/version/HRR; the
// flight walk splits the decrypted EncryptedExtensions / Certificate /
// CertificateVerify / Finished and captures the Finished verify_data. Mirrors
// the inline oracle in z80/tls_server_flight_test.go.

import "bytes"

// hrrSentinel is SHA-256("HelloRetryRequest"), the ServerHello.random value that
// signals a HelloRetryRequest (RFC 8446 §4.1.3). We do not handle HRR.
var hrrSentinel = []byte{
	0xCF, 0x21, 0xAD, 0x74, 0xE5, 0x9A, 0x61, 0x11,
	0xBE, 0x1D, 0x8C, 0x02, 0x1E, 0x65, 0xB8, 0x91,
	0xC2, 0xA2, 0x11, 0x16, 0x7A, 0xBB, 0x8C, 0x5E,
	0x07, 0x9E, 0x09, 0xE2, 0xC8, 0xA8, 0x33, 0x9C,
}

// ParseServerHello validates a ServerHello handshake message against our fixed
// offer and returns the server's 32-byte X25519 key_share. ok is false for a
// wrong handshake type, a non-1.3 negotiated version, a cipher other than
// TLS_CHACHA20_POLY1305_SHA256, a missing/short x25519 key_share, or the HRR
// sentinel random.
func ParseServerHello(sh []byte) (pub [32]byte, ok bool) {
	if len(sh) < 4 || sh[0] != 0x02 {
		return pub, false
	}
	hlen := int(sh[1])<<16 | int(sh[2])<<8 | int(sh[3])
	body := sh[4:]
	if hlen != len(body) {
		return pub, false
	}
	p := body
	rd := func(n int) ([]byte, bool) {
		if len(p) < n {
			return nil, false
		}
		v := p[:n]
		p = p[n:]
		return v, true
	}
	if _, okv := rd(2); !okv { // legacy_version
		return pub, false
	}
	random, okv := rd(32)
	if !okv {
		return pub, false
	}
	if bytes.Equal(random, hrrSentinel) {
		return pub, false
	}
	sidLen, okv := rd(1)
	if !okv {
		return pub, false
	}
	if _, okv = rd(int(sidLen[0])); !okv {
		return pub, false
	}
	cs, okv := rd(2)
	if !okv {
		return pub, false
	}
	if cs[0] != 0x13 || cs[1] != 0x03 { // TLS_CHACHA20_POLY1305_SHA256
		return pub, false
	}
	if _, okv = rd(1); !okv { // legacy_compression_method
		return pub, false
	}
	extLenB, okv := rd(2)
	if !okv {
		return pub, false
	}
	exts, okv := rd(int(extLenB[0])<<8 | int(extLenB[1]))
	if !okv || len(p) != 0 {
		return pub, false
	}

	var sawVersion, sawKeyShare bool
	for len(exts) >= 4 {
		typ := int(exts[0])<<8 | int(exts[1])
		dlen := int(exts[2])<<8 | int(exts[3])
		if len(exts) < 4+dlen {
			return pub, false
		}
		data := exts[4 : 4+dlen]
		switch typ {
		case 0x002b: // supported_versions: selected_version (2 bytes)
			if len(data) != 2 || data[0] != 0x03 || data[1] != 0x04 {
				return pub, false
			}
			sawVersion = true
		case 0x0033: // key_share: group(2) || key_exchange_len(2) || key_exchange
			if len(data) < 4 {
				return pub, false
			}
			group := int(data[0])<<8 | int(data[1])
			klen := int(data[2])<<8 | int(data[3])
			if group != 0x001d || klen != 32 || len(data) < 4+klen {
				return pub, false
			}
			copy(pub[:], data[4:4+32])
			sawKeyShare = true
		}
		exts = exts[4+dlen:]
	}
	if !sawVersion || !sawKeyShare {
		return pub, false
	}
	return pub, true
}

// ServerFlight is the parsed encrypted server flight: the handshake messages
// before the Finished (EncryptedExtensions || Certificate || CertificateVerify),
// the full Finished message, and its verify_data. BeforeFin is exactly the bytes
// folded into the transcript before snapshotting for the server-Finished MAC.
type ServerFlight struct {
	BeforeFin  []byte
	Finished   []byte
	VerifyData []byte
}

// WalkServerFlight parses the decrypted server flight (a concatenation of
// handshake messages). complete is false (err nil) when buf does not yet contain
// a full Finished message — the caller should decrypt more records and retry.
// err is non-nil on a malformed message. On success it splits everything before
// the Finished into BeforeFin and returns the Finished + its verify_data.
func WalkServerFlight(buf []byte) (sf ServerFlight, complete bool, err error) {
	off := 0
	for {
		if off+4 > len(buf) {
			return ServerFlight{}, false, nil // need more bytes for a header
		}
		typ := buf[off]
		mlen := int(buf[off+1])<<16 | int(buf[off+2])<<8 | int(buf[off+3])
		end := off + 4 + mlen
		if end > len(buf) {
			return ServerFlight{}, false, nil // body not fully arrived
		}
		if typ == 20 { // Finished
			sf.BeforeFin = buf[:off]
			sf.Finished = buf[off:end]
			sf.VerifyData = buf[off+4 : end]
			return sf, true, nil
		}
		off = end
	}
}
