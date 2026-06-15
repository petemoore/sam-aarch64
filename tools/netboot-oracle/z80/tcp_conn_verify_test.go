// tcp_conn_verify_test.go — the i100 host-verification of the streamed-body
// SHA-256 verify in src/netboot/tcp_conn.asm (q15 option c). It runs the same
// composed tcp_conn module as tcp_conn_stream_test.go with streaming enabled, but
// also turns on the hash: conn_verify_init resets the SHA-256 state before the
// body, storage_sink_flush feeds each flushed window through sha256_update as it
// streams (never buffering the whole body), and conn_verify_final writes the
// digest into CONN_HASH and compares it against CONN_PINNED_HASH.
//
// It asserts (1) CONN_HASH equals Go's crypto/sha256.Sum256(body) byte-for-byte —
// the digest computed incrementally across the bounded flush windows matches the
// authority — and (2) the verify flag: pinning the correct hash sets
// CONN_HASH_MATCH=1, a wrong hash sets it 0. The body spans several flush windows
// with a non-window-multiple length so the incremental update across windows
// (and the partial-final remainder flushed at the FIN) is exercised. The wire
// frames are cross-checked against the Go authority by the shared stream helpers,
// so enabling the hash does not change the wire.
package z80_test

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

// verifyInit resets the streamed-body hash on the machine (conn_verify_init).
// Must be called before any body segment arrives, alongside enableSink.
func verifyInit(t *testing.T, mac *z80h.Machine) {
	t.Helper()
	if _, err := mac.Call("conn_verify_init"); err != nil {
		t.Fatalf("conn_verify_init: %v", err)
	}
}

// pinHash fills CONN_PINNED_HASH with the 32-byte expected hash.
func pinHash(t *testing.T, mac *z80h.Machine, h [32]byte) {
	t.Helper()
	a, err := mac.Sym("CONN_PINNED_HASH")
	if err != nil {
		t.Fatalf("%v", err)
	}
	mac.Write(a, h[:])
}

// verifyFinal finishes the hash + compares against the pin (conn_verify_final),
// then returns CONN_HASH (the digest) and CONN_HASH_MATCH (0/1).
func verifyFinal(t *testing.T, mac *z80h.Machine) (hash [32]byte, match byte) {
	t.Helper()
	if _, err := mac.Call("conn_verify_final"); err != nil {
		t.Fatalf("conn_verify_final: %v", err)
	}
	ha, err := mac.Sym("CONN_HASH")
	if err != nil {
		t.Fatalf("%v", err)
	}
	copy(hash[:], mac.Read(ha, 32))
	ma, err := mac.Sym("CONN_HASH_MATCH")
	if err != nil {
		t.Fatalf("%v", err)
	}
	return hash, mac.Read(ma, 1)[0]
}

// streamBodyWithVerify boots a streaming connection with the hash enabled, feeds
// body across straddling segments, sends the FIN, and returns the loaded machine
// + the Go-side body digest. It enables the sink + conn_verify_init BEFORE the
// body, drives the handshake/body/FIN through the shared stream helpers (which
// cross-check every frame against the Go authority), and asserts the streamed
// bytes equal the body (so the digest is over the right bytes).
func streamBodyWithVerify(t *testing.T, window uint16, body []byte, segLen int) (*z80h.Machine, [32]byte) {
	t.Helper()
	mac := loadTCPConn(t)
	fillTCPConnConfig(t, mac)
	enc := z80h.NewENC28J60()
	initTCPConnDriver(t, mac, enc)

	enableSink(t, mac, window)
	verifyInit(t, mac)
	ref, goSink := goConnStream(int(window))
	ref.Connect()

	srvSeq := establishStream(t, mac, enc, ref)
	srvSeq = feedBodyZ80(t, mac, enc, ref, srvSeq, body, segLen)
	finStream(t, mac, enc, ref, srvSeq)

	// The streamed bytes must equal the body (the digest is computed over them).
	assertStream(t, mac, goSink, body, window)
	return mac, sha256.Sum256(body)
}

// TestTCPConnVerifyDigestMatchesCryptoSHA256: a multi-window body (>= 3 flush
// windows, a non-window-multiple length) streamed with the hash on yields a
// CONN_HASH equal to crypto/sha256.Sum256(body) byte-for-byte — the incremental
// update across the bounded windows (+ the partial-final remainder at the FIN)
// matches the authority.
func TestTCPConnVerifyDigestMatchesCryptoSHA256(t *testing.T) {
	const window = 16
	// 100 bytes, not a multiple of 16 -> 6 full windows of 16 + a 4-byte
	// remainder => 7 flushes (>= 3 windows, partial final), the incremental
	// carry across windows exercised hard.
	body := make([]byte, 100)
	for i := range body {
		body[i] = byte('a' + i%26)
	}

	mac, want := streamBodyWithVerify(t, window, body, 13) // segments straddle windows

	// Pin the correct hash, then finalise: the digest must match crypto/sha256
	// and the flag must be 1.
	pinHash(t, mac, want)
	got, match := verifyFinal(t, mac)
	if got != want {
		t.Fatalf("CONN_HASH != crypto/sha256(body)\n z80 %s\n  go %s",
			hex.EncodeToString(got[:]), hex.EncodeToString(want[:]))
	}
	if match != 1 {
		t.Errorf("CONN_HASH_MATCH = %d with the correct pin, want 1", match)
	}
}

// TestTCPConnVerifyFlagWrongPin: with a deliberately wrong pinned hash,
// conn_verify_final sets CONN_HASH_MATCH = 0 (but the digest in CONN_HASH still
// equals the real body hash — the pin does not corrupt the computed digest).
func TestTCPConnVerifyFlagWrongPin(t *testing.T) {
	const window = 24
	// 200 bytes, not a multiple of 24 -> 8 windows of 24 + an 8-byte remainder.
	body := make([]byte, 200)
	for i := range body {
		body[i] = byte('A' + (i*7)%26)
	}

	mac, real := streamBodyWithVerify(t, window, body, 11)

	wrong := real
	wrong[0] ^= 0xFF // corrupt one byte of the pin
	pinHash(t, mac, wrong)
	got, match := verifyFinal(t, mac)
	if got != real {
		t.Fatalf("CONN_HASH changed with a wrong pin\n z80 %s\n  go %s",
			hex.EncodeToString(got[:]), hex.EncodeToString(real[:]))
	}
	if match != 0 {
		t.Errorf("CONN_HASH_MATCH = %d with a wrong pin, want 0", match)
	}
}

// TestTCPConnVerifyFlagWrongPinLastByte: a wrong pin differing only in the LAST
// byte still sets CONN_HASH_MATCH = 0 (the full 32-byte compare, not a prefix).
func TestTCPConnVerifyFlagWrongPinLastByte(t *testing.T) {
	const window = 20
	body := make([]byte, 130) // 6 windows of 20 + a 10-byte remainder
	for i := range body {
		body[i] = byte(i)
	}

	mac, real := streamBodyWithVerify(t, window, body, 7)

	wrong := real
	wrong[31] ^= 0x01 // corrupt only the last byte
	pinHash(t, mac, wrong)
	got, match := verifyFinal(t, mac)
	if got != real {
		t.Fatalf("CONN_HASH != crypto/sha256(body)\n z80 %s\n  go %s",
			hex.EncodeToString(got[:]), hex.EncodeToString(real[:]))
	}
	if match != 0 {
		t.Errorf("CONN_HASH_MATCH = %d with a last-byte-wrong pin, want 0", match)
	}
}
