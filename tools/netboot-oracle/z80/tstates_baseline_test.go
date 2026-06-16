// tstates_baseline_test.go — recorded T-state baselines for the netboot crypto
// leaves over fixed payloads. These are the starting point for the i102
// optimization: each test drives a primitive once over a deterministic input,
// logs the measured cycles (a future PR drives them down), and asserts only that
// the run cost > 0 cycles AND completed with no error. A clean run is the proof
// the T-state table (tstates.go) is COMPLETE for every opcode that primitive
// executes — an unknown opcode would surface as an error here, not a silent
// guess. If one does, add that opcode's canonical timing to the table (cite it),
// never relax this assertion.
package z80_test

import (
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

// requireTStates fails if a run errored (an unknown opcode breaks completeness)
// or burned zero cycles (a no-op measurement, meaningless as a baseline).
func requireTStates(t *testing.T, res z80h.CallResult, err error, name string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: run failed (table incomplete or routine error): %v", name, err)
	}
	if res.TStates == 0 {
		t.Fatalf("%s: measured 0 T-states (nothing ran)", name)
	}
	t.Logf("i102 baseline: %s = %d T-states (%d steps)", name, res.TStates, res.Steps)
}

// TestBaselineSHA256 hashes a fixed 256-byte message in one update and logs the
// init+update+final cycle total.
func TestBaselineSHA256(t *testing.T) {
	mac := loadSHA256Machine(t)
	msg := make([]byte, 256)
	for i := range msg {
		msg[i] = byte(i*7 + 3)
	}
	var total z80h.CallResult
	res, err := mac.Call("sha256_init")
	if err != nil {
		t.Fatalf("sha256_init: %v", err)
	}
	total.TStates += res.TStates
	total.Steps += res.Steps
	mac.Write(shaInputStage, msg)
	res, err = mac.CallEntry("sha256_update", z80h.Entry{HL: shaInputStage, BC: uint16(len(msg))})
	if err != nil {
		t.Fatalf("sha256_update: %v", err)
	}
	total.TStates += res.TStates
	total.Steps += res.Steps
	res, err = mac.CallEntry("sha256_final", z80h.Entry{HL: shaOutputStage})
	requireTStates(t, total, err, "sha256(256B, init+update+final)")
	total.TStates += res.TStates
	total.Steps += res.Steps
	t.Logf("i102 baseline: sha256(256B total) = %d T-states (%d steps)", total.TStates, total.Steps)
}

// TestBaselineHMACSHA256 MACs a fixed message under a fixed key.
func TestBaselineHMACSHA256(t *testing.T) {
	mac := loadHMAC(t)
	key := rep(0x0b, 32)
	msg := []byte("The quick brown fox jumps over the lazy dog, twice over for length.")
	mac.Write(hmacKeyStage, key)
	mac.Write(hmacMsgStage, msg)
	mac.WriteU16LE(mustSym(t, mac, "HMAC_KEY_PTR"), hmacKeyStage)
	mac.WriteU16LE(mustSym(t, mac, "HMAC_KEY_LEN"), uint16(len(key)))
	mac.WriteU16LE(mustSym(t, mac, "HMAC_MSG_PTR"), hmacMsgStage)
	mac.WriteU16LE(mustSym(t, mac, "HMAC_MSG_LEN"), uint16(len(msg)))
	res, err := mac.CallEntry("hmac_sha256", z80h.Entry{HL: hmacOutStage})
	requireTStates(t, res, err, "hmac_sha256(32B key, 66B msg)")
}

// TestBaselineHKDF runs extract then expand for a fixed 42-byte OKM.
func TestBaselineHKDF(t *testing.T) {
	mac := loadHKDF(t)
	ikm := rep(0x0b, 22)
	salt := seq(0x00, 13)
	info := seq(0xf0, 10)
	mac.Write(hkdfSaltStage, salt)
	mac.Write(hkdfIKMStage, ikm)
	mac.WriteU16LE(mustSym(t, mac, "HKDF_SALT_PTR"), hkdfSaltStage)
	mac.WriteU16LE(mustSym(t, mac, "HKDF_SALT_LEN"), uint16(len(salt)))
	mac.WriteU16LE(mustSym(t, mac, "HKDF_IKM_PTR"), hkdfIKMStage)
	mac.WriteU16LE(mustSym(t, mac, "HKDF_IKM_LEN"), uint16(len(ikm)))
	var total z80h.CallResult
	res, err := mac.Call("hkdf_extract")
	if err != nil {
		t.Fatalf("hkdf_extract: %v", err)
	}
	total.TStates += res.TStates
	total.Steps += res.Steps
	mac.Write(hkdfInfoStage, info)
	mac.WriteU16LE(mustSym(t, mac, "HKDF_INFO_PTR"), hkdfInfoStage)
	mac.WriteU16LE(mustSym(t, mac, "HKDF_INFO_LEN"), uint16(len(info)))
	mac.WriteU16LE(mustSym(t, mac, "HKDF_L"), 42)
	res, err = mac.CallEntry("hkdf_expand", z80h.Entry{HL: hkdfOutStage})
	requireTStates(t, total, err, "hkdf(extract+expand, 42B OKM)")
	total.TStates += res.TStates
	total.Steps += res.Steps
	t.Logf("i102 baseline: hkdf(extract+expand, 42B OKM) = %d T-states (%d steps)", total.TStates, total.Steps)
}

// TestBaselineHKDFExpandLabel runs the TLS 1.3 HKDF-Expand-Label "derived" shape.
func TestBaselineHKDFExpandLabel(t *testing.T) {
	mac := loadEL(t)
	secret := rep(0x5a, 32)
	label := "derived"
	ctx := rep(0xab, 32)
	mac.Write(elSecretStage, secret)
	mac.Write(elLabelStage, []byte(label))
	mac.Write(elCtxStage, ctx)
	mac.WriteU16LE(mustSym(t, mac, "EL_SECRET_PTR"), elSecretStage)
	mac.WriteU16LE(mustSym(t, mac, "EL_LABEL_PTR"), elLabelStage)
	mac.WriteU16LE(mustSym(t, mac, "EL_LABEL_LEN"), uint16(len(label)))
	mac.WriteU16LE(mustSym(t, mac, "EL_CTX_PTR"), elCtxStage)
	mac.WriteU16LE(mustSym(t, mac, "EL_CTX_LEN"), uint16(len(ctx)))
	mac.WriteU16LE(mustSym(t, mac, "EL_LENGTH"), 32)
	res, err := mac.CallEntry("expand_label", z80h.Entry{HL: elOutStage})
	requireTStates(t, res, err, "expand_label(derived, 32B)")
}

// TestBaselineChaCha20Block runs one ChaCha20 block (the RFC 8439 §2.3.2 inputs).
func TestBaselineChaCha20Block(t *testing.T) {
	mac := loadChaCha(t)
	mac.Write(mustSym(t, mac, "CC_KEY"), unhex(t, "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"))
	mac.Write(mustSym(t, mac, "CC_COUNTER"), []byte{1, 0, 0, 0})
	mac.Write(mustSym(t, mac, "CC_NONCE"), unhex(t, "000000090000004a00000000"))
	res, err := mac.CallEntry("chacha20_block", z80h.Entry{HL: ccOutStage})
	requireTStates(t, res, err, "chacha20_block")
}

// TestBaselineChaCha20Encrypt runs the RFC 8439 §2.4.2 114-byte stream cipher.
func TestBaselineChaCha20Encrypt(t *testing.T) {
	mac := loadChaCha(t)
	key := unhex(t, "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f")
	nonce := unhex(t, "000000000000004a00000000")
	plain := []byte("Ladies and Gentlemen of the class of '99: If I could offer you only one tip for the future, sunscreen would be it.")
	got := ccEncryptResult(t, mac, key, 1, nonce, plain)
	requireTStates(t, got, nil, "chacha20_encrypt(114B)")
}

// ccEncryptResult drives chacha20_encrypt and returns the CallResult (for the
// baseline's cycle reading; ccEncrypt above returns only the bytes).
func ccEncryptResult(t *testing.T, mac *z80h.Machine, key []byte, counter uint32, nonce, msg []byte) z80h.CallResult {
	t.Helper()
	mac.Write(mustSym(t, mac, "CC_KEY"), key)
	mac.Write(mustSym(t, mac, "CC_COUNTER"), []byte{byte(counter), byte(counter >> 8), byte(counter >> 16), byte(counter >> 24)})
	mac.Write(mustSym(t, mac, "CC_NONCE"), nonce)
	mac.Write(ccMsgStage, msg)
	mac.WriteU16LE(mustSym(t, mac, "CC_MSG_PTR"), ccMsgStage)
	mac.WriteU16LE(mustSym(t, mac, "CC_MSG_LEN"), uint16(len(msg)))
	res, err := mac.CallEntry("chacha20_encrypt", z80h.Entry{HL: ccEncOut})
	if err != nil {
		t.Fatalf("chacha20_encrypt: %v", err)
	}
	return res
}

// TestBaselinePoly1305 MACs the RFC 8439 §2.5.2 fixed message.
func TestBaselinePoly1305(t *testing.T) {
	mac := loadPoly(t)
	key := unhex(t, "85d6be7857556d337f4452fe42d506a80103808afb0db2fd4abff6af4149f51b")
	msg := []byte("Cryptographic Forum Research Group")
	mac.Write(mustSym(t, mac, "POLY_KEY"), key)
	mac.Write(polyMsgStage, msg)
	mac.WriteU16LE(mustSym(t, mac, "POLY_MSG_PTR"), polyMsgStage)
	mac.WriteU16LE(mustSym(t, mac, "POLY_MSG_LEN"), uint16(len(msg)))
	res, err := mac.CallEntry("poly1305_mac", z80h.Entry{HL: polyOutStage})
	requireTStates(t, res, err, "poly1305_mac(34B)")
}

// TestBaselineAEADEncrypt runs the RFC 8439 §2.8.2 AEAD encryption (114B + AAD).
func TestBaselineAEADEncrypt(t *testing.T) {
	mac := loadAEAD(t)
	key := unhex(t, "808182838485868788898a8b8c8d8e8f909192939495969798999a9b9c9d9e9f")
	nonce := unhex(t, "070000004041424344454647")
	aad := unhex(t, "50515253c0c1c2c3c4c5c6c7")
	pt := []byte("Ladies and Gentlemen of the class of '99: If I could offer you only one tip for the future, sunscreen would be it.")
	mac.Write(mustSym(t, mac, "AEAD_KEY"), key)
	mac.Write(mustSym(t, mac, "AEAD_NONCE"), nonce)
	mac.Write(mustSym(t, mac, "AEAD_AAD"), aad)
	mac.WriteU16LE(mustSym(t, mac, "AEAD_AAD_LEN"), uint16(len(aad)))
	mac.Write(mustSym(t, mac, "AEAD_PT"), pt)
	mac.WriteU16LE(mustSym(t, mac, "AEAD_LEN"), uint16(len(pt)))
	res, err := mac.CallEntry("aead_encrypt", z80h.Entry{})
	requireTStates(t, res, err, "aead_encrypt(114B+12B AAD)")
}

// TestBaselineX25519 runs one X25519 scalar multiplication over the base point —
// the heaviest crypto leaf (the Montgomery ladder + field inversion), the prime
// target for the i102 optimization. It needs the high step cap the field tests
// use.
func TestBaselineX25519(t *testing.T) {
	mac := loadFE(t)
	scalar := unhex(t, "a546e36bf0527c9d3b16154b82465edd62144c0ac1fc5a18506a2244ba449ac4")
	u := unhex(t, "e6db6867583030db3594c1a424b15f7c726624ec26b3353b10a903a6d0ab1c4c")
	mac.Write(mustSym(t, mac, "X25519_K"), scalar)
	mac.Write(mustSym(t, mac, "X25519_U"), u)
	res, err := mac.CallEntry("x25519", z80h.Entry{StepCap: x25519StepCap})
	requireTStates(t, res, err, "x25519(scalar mult)")
}
