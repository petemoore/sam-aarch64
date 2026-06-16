package tls

// The TLS 1.3 key schedule (RFC 8446 §7.1) — the host authority for the Z80
// src/netboot/tls_keyschedule.asm leaf (brick 1). It is lifted from the inline
// oracle in z80/tls_keyschedule_test.go so the Z80 port and this authority share
// one reference; the package test pins it to the same RFC 8448 anchors.

import (
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/binary"
)

// ExpandLabel is HKDF-Expand-Label (RFC 8446 §7.1): the HkdfLabel encoding
// (length || "tls13 "+label || context) fed through HKDF-Expand. This is the
// authority for src/netboot/hkdf_expand_label.asm.
func ExpandLabel(secret []byte, label string, ctx []byte, length int) []byte {
	full := "tls13 " + label
	info := make([]byte, 0, 2+1+len(full)+1+len(ctx))
	info = binary.BigEndian.AppendUint16(info, uint16(length))
	info = append(info, byte(len(full)))
	info = append(info, full...)
	info = append(info, byte(len(ctx)))
	info = append(info, ctx...)
	okm, err := hkdf.Expand(sha256.New, secret, string(info), length)
	if err != nil {
		panic(err)
	}
	return okm
}

// Extract is HKDF-Extract(salt, IKM) = HMAC(salt, IKM).
func Extract(salt, ikm []byte) []byte {
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		panic(err)
	}
	return prk
}

// KeySchedule holds the full RFC 8446 §7.1 secret tree plus each traffic
// secret's record key/iv and Finished key. Field names mirror the spec labels
// (chs = "c hs traffic", sap = "s ap traffic", etc.).
type KeySchedule struct {
	Early, Derived, Handshake, Derived2, Master []byte
	CHS, SHS, CAP, SAP                          []byte
	CHSKey, CHSIV, CHSFin                       []byte
	SHSKey, SHSIV, SHSFin                       []byte
	CAPKey, CAPIV, CAPFin                       []byte
	SAPKey, SAPIV, SAPFin                       []byte
}

// trafficKeys derives the ChaCha20 key (32), record iv (12) and Finished key
// (32) for one traffic secret.
func trafficKeys(secret []byte) (key, iv, fin []byte) {
	return ExpandLabel(secret, "key", nil, 32),
		ExpandLabel(secret, "iv", nil, 12),
		ExpandLabel(secret, "finished", nil, 32)
}

// DeriveHandshake fills the Early/derived/Handshake secrets and the client/server
// handshake-traffic secrets + their key/iv/Finished from the ECDHE shared secret
// and hashHS = Transcript-Hash(ClientHello..ServerHello). Must be called before
// DeriveApplication (which consumes Derived2).
func (ks *KeySchedule) DeriveHandshake(ecdhe, hashHS []byte) {
	zero := make([]byte, 32)
	emptyHash := sha256.Sum256(nil)
	ks.Early = Extract(zero, zero)
	ks.Derived = ExpandLabel(ks.Early, "derived", emptyHash[:], 32)
	ks.Handshake = Extract(ks.Derived, ecdhe)
	ks.CHS = ExpandLabel(ks.Handshake, "c hs traffic", hashHS, 32)
	ks.SHS = ExpandLabel(ks.Handshake, "s hs traffic", hashHS, 32)
	ks.Derived2 = ExpandLabel(ks.Handshake, "derived", emptyHash[:], 32)
	ks.CHSKey, ks.CHSIV, ks.CHSFin = trafficKeys(ks.CHS)
	ks.SHSKey, ks.SHSIV, ks.SHSFin = trafficKeys(ks.SHS)
}

// DeriveApplication fills the Master secret and the client/server application-
// traffic secrets + their key/iv/Finished from hashAP =
// Transcript-Hash(ClientHello..server Finished). Requires a prior DeriveHandshake.
func (ks *KeySchedule) DeriveApplication(hashAP []byte) {
	zero := make([]byte, 32)
	ks.Master = Extract(ks.Derived2, zero)
	ks.CAP = ExpandLabel(ks.Master, "c ap traffic", hashAP, 32)
	ks.SAP = ExpandLabel(ks.Master, "s ap traffic", hashAP, 32)
	ks.CAPKey, ks.CAPIV, ks.CAPFin = trafficKeys(ks.CAP)
	ks.SAPKey, ks.SAPIV, ks.SAPFin = trafficKeys(ks.SAP)
}

// ComputeKeySchedule is the one-shot reference (the brick-1 oracle): the whole
// schedule from the ECDHE secret and both transcript hashes. The state machine
// instead derives handshake then application secrets in two phases (the app
// hash is only known after the server Finished), but the result is identical.
func ComputeKeySchedule(ecdhe, hashHS, hashAP []byte) KeySchedule {
	var ks KeySchedule
	ks.DeriveHandshake(ecdhe, hashHS)
	ks.DeriveApplication(hashAP)
	return ks
}
