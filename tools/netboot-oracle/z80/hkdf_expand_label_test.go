// hkdf_expand_label_test.go — the i88 host-verification of TLS 1.3
// HKDF-Expand-Label (src/netboot/hkdf_expand_label.asm). It assembles the
// standalone module under the koron-go/z80 harness, runs expand_label over the
// TLS key-schedule label shapes, and asserts the output equals a Go reference of
// RFC 8446 §7.1 — itself anchored to the independent RFC 8448 known-answer (the
// TLS 1.3 Early-Secret "derived" secret).
package z80_test

import (
	"bytes"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"os"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	elBinPath = "../../../build/netboot_hkdf_expand_label.bin"
	elMapPath = "../../../build/netboot_hkdf_expand_label.map"

	elSecretStage = 0x4000
	elLabelStage  = 0x4400
	elCtxStage    = 0x4800
	elOutStage    = 0x5000
)

func loadEL(t *testing.T) *z80h.Machine {
	t.Helper()
	if _, err := os.Stat(elBinPath); err != nil {
		t.Skipf("hkdf_expand_label binary not built (%s); run `make netboot-hkdf-expand-label`", elBinPath)
	}
	mac, err := z80h.Load(elBinPath, elMapPath)
	if err != nil {
		t.Fatalf("load hkdf_expand_label: %v", err)
	}
	return mac
}

// goExpandLabel is the RFC 8446 §7.1 reference (HkdfLabel encoding + HKDF-Expand),
// the authority the Z80 expand_label is compared against.
func goExpandLabel(secret []byte, label string, ctx []byte, length int) []byte {
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

// TestExpandLabelReferenceAnchor pins the Go reference to RFC 8448: TLS 1.3's
// Early-Secret and its "derived" secret. If this fails, the reference is wrong and
// the cross-checks below would be meaningless.
func TestExpandLabelReferenceAnchor(t *testing.T) {
	early, err := hkdf.Extract(sha256.New, make([]byte, 32), nil) // Extract(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(early); got != "33ad0a1c607ec03b09e6cd9893680ce210adf300aa1f2660e1b22e10f170f92a" {
		t.Fatalf("Early-Secret = %s, want the RFC 8448 value", got)
	}
	emptyHash := sha256.Sum256(nil)
	derived := goExpandLabel(early, "derived", emptyHash[:], 32)
	if got := hex.EncodeToString(derived); got != "6f2615a108c702c5678f54fc9dbab69716c076189c48250cebeac3576c3611ba" {
		t.Fatalf("derived = %s, want the RFC 8448 value", got)
	}
}

// TestHKDFExpandLabel: the Z80 expand_label matches the Go RFC 8446 §7.1 reference
// across the TLS key-schedule labels (derived / key / iv / finished / traffic),
// with empty and 32-byte (hash) contexts and the real output lengths.
func TestHKDFExpandLabel(t *testing.T) {
	early, _ := hkdf.Extract(sha256.New, make([]byte, 32), nil)
	emptyHash := sha256.Sum256(nil)
	hctx := sha256.Sum256([]byte("a transcript"))
	secret := bytes.Repeat([]byte{0x5a}, 32)

	cases := []struct {
		name   string
		secret []byte
		label  string
		ctx    []byte
		length int
	}{
		{"rfc8448-derived", early, "derived", emptyHash[:], 32},
		{"key-16", secret, "key", nil, 16},
		{"key-32", secret, "key", nil, 32},
		{"iv-12", secret, "iv", nil, 12},
		{"finished-32", secret, "finished", nil, 32},
		{"c-hs-traffic", early, "c hs traffic", hctx[:], 32},
		{"len-40-multiblock", secret, "exp master", emptyHash[:], 40},
	}

	mac := loadEL(t)
	secPtr := mustSym(t, mac, "EL_SECRET_PTR")
	labPtr := mustSym(t, mac, "EL_LABEL_PTR")
	labLen := mustSym(t, mac, "EL_LABEL_LEN")
	ctxPtr := mustSym(t, mac, "EL_CTX_PTR")
	ctxLen := mustSym(t, mac, "EL_CTX_LEN")
	lenCell := mustSym(t, mac, "EL_LENGTH")

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mac.Write(elSecretStage, tc.secret)
			mac.Write(elLabelStage, []byte(tc.label))
			mac.Write(elCtxStage, tc.ctx)
			mac.WriteU16LE(secPtr, elSecretStage)
			mac.WriteU16LE(labPtr, elLabelStage)
			mac.WriteU16LE(labLen, uint16(len(tc.label)))
			mac.WriteU16LE(ctxPtr, elCtxStage)
			mac.WriteU16LE(ctxLen, uint16(len(tc.ctx)))
			mac.WriteU16LE(lenCell, uint16(tc.length))

			if _, err := mac.CallEntry("expand_label", z80h.Entry{HL: elOutStage}); err != nil {
				t.Fatalf("expand_label: %v", err)
			}
			got := mac.Read(elOutStage, tc.length)
			want := goExpandLabel(tc.secret, tc.label, tc.ctx, tc.length)
			if !bytes.Equal(got, want) {
				t.Errorf("expand_label(%q, %d) = %x, want %x", tc.label, tc.length, got, want)
			}
		})
	}
}
