package tls

// Anchors for the key-schedule authority. These pin the same RFC 8448 /
// RFC 8446 §7.1 known-answers the Z80 brick-1 test uses, so this package and the
// Z80 port share one verified reference.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// TestExpandLabelAnchor pins ExpandLabel (and Extract) to RFC 8448: the TLS 1.3
// Early-Secret and its "derived" secret.
func TestExpandLabelAnchor(t *testing.T) {
	early := Extract(make([]byte, 32), make([]byte, 32)) // Extract(0, 0)
	if got := hex.EncodeToString(early); got != "33ad0a1c607ec03b09e6cd9893680ce210adf300aa1f2660e1b22e10f170f92a" {
		t.Fatalf("Early-Secret = %s, want the RFC 8448 value", got)
	}
	emptyHash := sha256.Sum256(nil)
	derived := ExpandLabel(early, "derived", emptyHash[:], 32)
	if got := hex.EncodeToString(derived); got != "6f2615a108c702c5678f54fc9dbab69716c076189c48250cebeac3576c3611ba" {
		t.Fatalf("derived = %s, want the RFC 8448 value", got)
	}
}

// TestKeyScheduleAnchor pins ComputeKeySchedule's input-independent secrets to
// RFC 8448, so any handshake cross-check built on it is meaningful.
func TestKeyScheduleAnchor(t *testing.T) {
	ks := ComputeKeySchedule(make([]byte, 32), make([]byte, 32), make([]byte, 32))
	if got := hex.EncodeToString(ks.Early); got != "33ad0a1c607ec03b09e6cd9893680ce210adf300aa1f2660e1b22e10f170f92a" {
		t.Errorf("Early = %s, want the RFC 8448 value", got)
	}
	if got := hex.EncodeToString(ks.Derived); got != "6f2615a108c702c5678f54fc9dbab69716c076189c48250cebeac3576c3611ba" {
		t.Errorf("derived = %s, want the RFC 8448 value", got)
	}
}

// TestKeyScheduleTwoPhase: deriving handshake then application secrets in two
// steps (as the state machine does) gives the same schedule as the one-shot
// ComputeKeySchedule reference.
func TestKeyScheduleTwoPhase(t *testing.T) {
	mk := func(seed byte) []byte {
		b := make([]byte, 32)
		for i := range b {
			b[i] = byte(i)*7 + seed
		}
		return b
	}
	ecdhe, hashHS, hashAP := mk(0xa0), mk(0xb0), mk(0xc0)

	want := ComputeKeySchedule(ecdhe, hashHS, hashAP)

	var got KeySchedule
	got.DeriveHandshake(ecdhe, hashHS)
	got.DeriveApplication(hashAP)

	for _, c := range []struct {
		name string
		a, b []byte
	}{
		{"CHS", got.CHS, want.CHS}, {"SHS", got.SHS, want.SHS},
		{"CAP", got.CAP, want.CAP}, {"SAP", got.SAP, want.SAP},
		{"CHSKey", got.CHSKey, want.CHSKey}, {"CHSIV", got.CHSIV, want.CHSIV}, {"CHSFin", got.CHSFin, want.CHSFin},
		{"SHSFin", got.SHSFin, want.SHSFin},
		{"CAPKey", got.CAPKey, want.CAPKey}, {"SAPKey", got.SAPKey, want.SAPKey},
		{"Master", got.Master, want.Master},
	} {
		if !bytes.Equal(c.a, c.b) {
			t.Errorf("%s differs between two-phase and one-shot", c.name)
		}
	}
}
