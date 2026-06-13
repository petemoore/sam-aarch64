// tls_keyschedule_test.go — the i88 host-verification of the TLS 1.3 key schedule
// (src/netboot/tls_keyschedule.asm), brick 1 of the handshake. It assembles the
// standalone module under the koron-go/z80 harness, runs tls_key_schedule, and
// asserts every derived secret / key / iv / finished equals a Go RFC 8446 §7.1
// reference (Go crypto/hkdf + the §7.1 HkdfLabel encoding via the package's
// goExpandLabel) across two input sets — byte-for-byte. The reference's Early and
// derived secrets are independently anchored to the RFC 8448 known-answer (here and
// in TestExpandLabelReferenceAnchor), and the Z80 Early/derived are asserted equal
// to those RFC values directly.
package z80_test

import (
	"bytes"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	ksBinPath = "../../../build/netboot_tls_keyschedule.bin"
	ksMapPath = "../../../build/netboot_tls_keyschedule.map"
)

func loadKS(t *testing.T) *z80h.Machine {
	t.Helper()
	if _, err := os.Stat(ksBinPath); err != nil {
		t.Skipf("tls_keyschedule binary not built (%s); run `make netboot-tls-keyschedule`", ksBinPath)
	}
	mac, err := z80h.Load(ksBinPath, ksMapPath)
	if err != nil {
		t.Fatalf("load tls_keyschedule: %v", err)
	}
	return mac
}

// goKeySchedule is the RFC 8446 §7.1 reference: the secret tree + each traffic
// secret's record key/iv/finished, built on Go crypto/hkdf + goExpandLabel.
type goKeySchedule struct {
	early, derived, handshake, derived2, master []byte
	chs, shs, cap, sap                          []byte
	chsKey, chsIV, chsFin                       []byte
	shsKey, shsIV, shsFin                       []byte
	capKey, capIV, capFin                       []byte
	sapKey, sapIV, sapFin                       []byte
}

func ksExtract(salt, ikm []byte) []byte {
	// HKDF-Extract(salt, IKM) = HMAC(salt, IKM); Go's Extract(h, secret=IKM, salt).
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		panic(err)
	}
	return prk
}

func goKeyScheduleRef(ecdhe, hashHS, hashAP []byte) goKeySchedule {
	zero := make([]byte, 32)
	emptyHash := sha256.Sum256(nil)
	var ks goKeySchedule
	ks.early = ksExtract(zero, zero)
	ks.derived = goExpandLabel(ks.early, "derived", emptyHash[:], 32)
	ks.handshake = ksExtract(ks.derived, ecdhe)
	ks.chs = goExpandLabel(ks.handshake, "c hs traffic", hashHS, 32)
	ks.shs = goExpandLabel(ks.handshake, "s hs traffic", hashHS, 32)
	ks.derived2 = goExpandLabel(ks.handshake, "derived", emptyHash[:], 32)
	ks.master = ksExtract(ks.derived2, zero)
	ks.cap = goExpandLabel(ks.master, "c ap traffic", hashAP, 32)
	ks.sap = goExpandLabel(ks.master, "s ap traffic", hashAP, 32)
	keys := func(s []byte) (k, iv, fin []byte) {
		return goExpandLabel(s, "key", nil, 32), goExpandLabel(s, "iv", nil, 12), goExpandLabel(s, "finished", nil, 32)
	}
	ks.chsKey, ks.chsIV, ks.chsFin = keys(ks.chs)
	ks.shsKey, ks.shsIV, ks.shsFin = keys(ks.shs)
	ks.capKey, ks.capIV, ks.capFin = keys(ks.cap)
	ks.sapKey, ks.sapIV, ks.sapFin = keys(ks.sap)
	return ks
}

// TestKeyScheduleRefAnchor pins the Go reference's input-independent secrets to
// RFC 8448, so the cross-checks below are meaningful.
func TestKeyScheduleRefAnchor(t *testing.T) {
	ks := goKeyScheduleRef(make([]byte, 32), make([]byte, 32), make([]byte, 32))
	if got := hex.EncodeToString(ks.early); got != "33ad0a1c607ec03b09e6cd9893680ce210adf300aa1f2660e1b22e10f170f92a" {
		t.Fatalf("Early-Secret = %s, want the RFC 8448 value", got)
	}
	if got := hex.EncodeToString(ks.derived); got != "6f2615a108c702c5678f54fc9dbab69716c076189c48250cebeac3576c3611ba" {
		t.Fatalf("derived = %s, want the RFC 8448 value", got)
	}
}

// TestTLSKeySchedule: the Z80 tls_key_schedule matches the Go RFC 8446 §7.1
// reference for every output, across two input sets (the RFC 8448 ECDHE with
// distinct transcript hashes, and an all-different deterministic set). The Z80
// Early/derived are also asserted equal to the RFC 8448 known-answer directly.
func TestTLSKeySchedule(t *testing.T) {
	type inputs struct {
		name                  string
		ecdhe, hashHS, hashAP []byte
	}
	mk := func(seed byte) []byte {
		b := make([]byte, 32)
		for i := range b {
			b[i] = byte(i)*7 + seed
		}
		return b
	}
	sets := []inputs{
		{"rfc8448-ecdhe", unhex(t, "8bd4054fb55b9d63fdfbacf9f04b9f0d35e6d63f537563efd46272900f89492d"), mk(0x11), mk(0x22)},
		{"deterministic", mk(0xa0), mk(0xb0), mk(0xc0)},
	}

	out := func(mac *z80h.Machine, sym string, n int) []byte {
		return mac.Read(mustSym(t, mac, sym), n)
	}

	for _, in := range sets {
		t.Run(in.name, func(t *testing.T) {
			mac := loadKS(t)
			mac.Write(mustSym(t, mac, "KS_ECDHE"), in.ecdhe)
			mac.Write(mustSym(t, mac, "KS_HASH_HS"), in.hashHS)
			mac.Write(mustSym(t, mac, "KS_HASH_AP"), in.hashAP)
			// The full schedule is ~25 HMAC-SHA256 ops (each several SHA-256
			// compressions); past the default runaway guard, so raise the cap.
			if _, err := mac.CallEntry("tls_key_schedule", z80h.Entry{StepCap: 60_000_000}); err != nil {
				t.Fatalf("tls_key_schedule: %v", err)
			}
			ref := goKeyScheduleRef(in.ecdhe, in.hashHS, in.hashAP)

			// Direct RFC 8448 anchor on the Z80 outputs (input-independent secrets).
			if got := hex.EncodeToString(out(mac, "KS_EARLY", 32)); got != "33ad0a1c607ec03b09e6cd9893680ce210adf300aa1f2660e1b22e10f170f92a" {
				t.Errorf("Z80 Early = %s, want RFC 8448 value", got)
			}
			if got := hex.EncodeToString(out(mac, "KS_DERIVED", 32)); got != "6f2615a108c702c5678f54fc9dbab69716c076189c48250cebeac3576c3611ba" {
				t.Errorf("Z80 derived = %s, want RFC 8448 value", got)
			}

			checks := []struct {
				sym  string
				n    int
				want []byte
			}{
				{"KS_EARLY", 32, ref.early}, {"KS_DERIVED", 32, ref.derived},
				{"KS_HANDSHAKE", 32, ref.handshake}, {"KS_DERIVED2", 32, ref.derived2},
				{"KS_MASTER", 32, ref.master},
				{"KS_CHS", 32, ref.chs}, {"KS_SHS", 32, ref.shs},
				{"KS_CAP", 32, ref.cap}, {"KS_SAP", 32, ref.sap},
				{"KS_CHS_KEY", 32, ref.chsKey}, {"KS_CHS_IV", 12, ref.chsIV}, {"KS_CHS_FIN", 32, ref.chsFin},
				{"KS_SHS_KEY", 32, ref.shsKey}, {"KS_SHS_IV", 12, ref.shsIV}, {"KS_SHS_FIN", 32, ref.shsFin},
				{"KS_CAP_KEY", 32, ref.capKey}, {"KS_CAP_IV", 12, ref.capIV}, {"KS_CAP_FIN", 32, ref.capFin},
				{"KS_SAP_KEY", 32, ref.sapKey}, {"KS_SAP_IV", 12, ref.sapIV}, {"KS_SAP_FIN", 32, ref.sapFin},
			}
			for _, c := range checks {
				if got := out(mac, c.sym, c.n); !bytes.Equal(got, c.want) {
					t.Errorf("%s = %x, want %x", c.sym, got, c.want)
				}
			}
		})
	}
}
