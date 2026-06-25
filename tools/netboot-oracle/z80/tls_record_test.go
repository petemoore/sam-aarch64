// tls_record_test.go — the i88 host-verification of TLS 1.3 record protection
// (src/netboot/tls_record.asm), handshake brick 2. It assembles the standalone
// module under the koron-go/z80 harness and asserts: (a) a seal->open round-trip
// recovers (content, type) with TR_OK=1 across content types / lengths / sequence
// numbers; (b) the sealed record's bytes equal an independent Go reconstruction of
// the RFC 8446 §5.2/§5.3 framing (nonce = iv XOR seq, AAD = the 5-byte header,
// inner = content||type) fed through the verified Z80 aead_encrypt; (c) a tampered
// record or a wrong sequence number fails authentication (TR_OK=0).
package z80_test

import (
	"bytes"
	"encoding/binary"
	"os"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	trBinPath = "../../../build/netboot_tls_record.bin"
	trMapPath = "../../../build/netboot_tls_record.map"
)

func loadTR(t *testing.T) *z80h.Machine {
	t.Helper()
	if _, err := os.Stat(trBinPath); err != nil {
		t.Fatalf("tls_record binary not built (%s); run `make netboot-tls-record`", trBinPath)
	}
	mac, err := z80h.Load(trBinPath, trMapPath)
	if err != nil {
		t.Fatalf("load tls_record: %v", err)
	}
	return mac
}

func readU16LE(mac *z80h.Machine, addr uint16) int {
	b := mac.Read(addr, 2)
	return int(b[0]) | int(b[1])<<8
}

func seqBytes(n uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, n)
	return b
}

// tlsRecordSeal runs tls_record_seal and returns the full TLSCiphertext record.
func tlsRecordSeal(t *testing.T, key, iv, seq8 []byte, typ byte, content []byte) []byte {
	t.Helper()
	mac := loadTR(t)
	mac.Write(mustSym(t, mac, "TR_KEY"), key)
	mac.Write(mustSym(t, mac, "TR_IV"), iv)
	mac.Write(mustSym(t, mac, "TR_SEQ"), seq8)
	mac.Write(mustSym(t, mac, "TR_TYPE"), []byte{typ})
	mac.Write(mustSym(t, mac, "TR_CONTENT"), content)
	mac.WriteU16LE(mustSym(t, mac, "TR_CONTENT_LEN"), uint16(len(content)))
	if _, err := mac.CallEntry("tls_record_seal", z80h.Entry{}); err != nil {
		t.Fatalf("tls_record_seal: %v", err)
	}
	n := readU16LE(mac, mustSym(t, mac, "TR_RECORD_LEN"))
	return mac.Read(mustSym(t, mac, "TR_RECORD"), n)
}

// tlsRecordOpen runs tls_record_open and returns (ok, type, content).
func tlsRecordOpen(t *testing.T, key, iv, seq8, record []byte) (bool, byte, []byte) {
	t.Helper()
	mac := loadTR(t)
	mac.Write(mustSym(t, mac, "TR_KEY"), key)
	mac.Write(mustSym(t, mac, "TR_IV"), iv)
	mac.Write(mustSym(t, mac, "TR_SEQ"), seq8)
	mac.Write(mustSym(t, mac, "TR_RECORD"), record)
	mac.WriteU16LE(mustSym(t, mac, "TR_RECORD_LEN"), uint16(len(record)))
	if _, err := mac.CallEntry("tls_record_open", z80h.Entry{}); err != nil {
		t.Fatalf("tls_record_open: %v", err)
	}
	ok := mac.Read(mustSym(t, mac, "TR_OK"), 1)[0] == 1
	typ := mac.Read(mustSym(t, mac, "TR_TYPE"), 1)[0]
	clen := readU16LE(mac, mustSym(t, mac, "TR_CONTENT_LEN"))
	content := mac.Read(mustSym(t, mac, "TR_CONTENT"), clen)
	return ok, typ, content
}

// goFraming reconstructs the RFC 8446 §5.2/§5.3 framing independently.
func goFraming(iv, seq8 []byte, typ byte, content []byte) (nonce, aad, inner []byte) {
	nonce = make([]byte, 12)
	copy(nonce, iv)
	for i := 0; i < 8; i++ {
		nonce[4+i] ^= seq8[i]
	}
	inner = append(append([]byte{}, content...), typ)
	L := len(inner) + 16
	aad = []byte{0x17, 0x03, 0x03, byte(L >> 8), byte(L)}
	return
}

func trKey() []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i*9 + 1)
	}
	return k
}
func trIV() []byte {
	v := make([]byte, 12)
	for i := range v {
		v[i] = byte(i*5 + 0xa0)
	}
	return v
}

// TestTLSRecordFraming: the sealed record == header || aead_encrypt(key, nonce,
// header, content||type), with nonce/header/inner reconstructed independently in
// Go and the AEAD computed by the verified Z80 aead_encrypt.
func TestTLSRecordFraming(t *testing.T) {
	key, iv := trKey(), trIV()
	for _, tc := range []struct {
		typ  byte
		seq  uint64
		clen int
	}{
		{22, 0, 16}, {23, 1, 100}, {22, 255, 1}, {23, 0x0102030405060708, 64}, {23, 5, 0},
	} {
		content := make([]byte, tc.clen)
		for i := range content {
			content[i] = byte(i*13 + 7)
		}
		seq8 := seqBytes(tc.seq)
		nonce, aad, inner := goFraming(iv, seq8, tc.typ, content)
		expCT, expTag := aeadEncrypt(t, key, nonce, aad, inner)
		want := append(append(append([]byte{}, aad...), expCT...), expTag...)
		got := tlsRecordSeal(t, key, iv, seq8, tc.typ, content)
		if !bytes.Equal(got, want) {
			t.Errorf("type=%d seq=%d clen=%d:\n  record = %x\n    want = %x", tc.typ, tc.seq, tc.clen, got, want)
		}
	}
}

// TestTLSRecordRoundTrip: seal then open recovers (content, type), TR_OK=1,
// across content types, lengths, and sequence numbers.
func TestTLSRecordRoundTrip(t *testing.T) {
	key, iv := trKey(), trIV()
	for _, typ := range []byte{22, 23, 21} {
		for _, n := range []int{0, 1, 5, 16, 17, 64, 255, 500} {
			for _, seq := range []uint64{0, 1, 42, 0xffffffffffffffff} {
				content := make([]byte, n)
				for i := range content {
					content[i] = byte(i*31 + int(typ))
				}
				seq8 := seqBytes(seq)
				rec := tlsRecordSeal(t, key, iv, seq8, typ, content)
				ok, gotType, gotContent := tlsRecordOpen(t, key, iv, seq8, rec)
				if !ok {
					t.Errorf("type=%d n=%d seq=%d: open rejected an authentic record", typ, n, seq)
					continue
				}
				if gotType != typ {
					t.Errorf("type=%d n=%d seq=%d: recovered type %d", typ, n, seq, gotType)
				}
				if !bytes.Equal(gotContent, content) {
					t.Errorf("type=%d n=%d seq=%d: content mismatch\n got %x\nwant %x", typ, n, seq, gotContent, content)
				}
			}
		}
	}
}

// TestTLSRecordTamper: a flipped record byte, or opening at the wrong sequence
// number (a different nonce), must fail authentication.
func TestTLSRecordTamper(t *testing.T) {
	key, iv := trKey(), trIV()
	content := []byte("a TLS 1.3 handshake message fragment")
	seq8 := seqBytes(7)
	rec := tlsRecordSeal(t, key, iv, seq8, 22, content)

	if ok, _, _ := tlsRecordOpen(t, key, iv, seq8, rec); !ok {
		t.Fatal("baseline: authentic record rejected")
	}
	// Flip a ciphertext byte (skip the 5-byte header).
	bad := append([]byte(nil), rec...)
	bad[8] ^= 0x40
	if ok, _, _ := tlsRecordOpen(t, key, iv, seq8, bad); ok {
		t.Error("tampered ciphertext accepted")
	}
	// Flip the tag (last byte).
	bad2 := append([]byte(nil), rec...)
	bad2[len(bad2)-1] ^= 0x01
	if ok, _, _ := tlsRecordOpen(t, key, iv, seq8, bad2); ok {
		t.Error("tampered tag accepted")
	}
	// Open at the wrong sequence number (nonce differs -> AEAD fails).
	if ok, _, _ := tlsRecordOpen(t, key, iv, seqBytes(8), rec); ok {
		t.Error("record opened at the wrong sequence number")
	}
}
