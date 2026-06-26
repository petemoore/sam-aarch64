// record_len_guard_test.go — proves the Z80 record-length guards fire.
//
// Two i17-review hardening residuals (i73):
//
//   - L4: reader_next_kind's STAGING_BUF bounds check.  STAGING_BUF is
//     exactly 1024 bytes, so a record whose payload is exactly 1024 bytes
//     fills it and is LEGAL; only a payload > 1024 overflows.  The old
//     high-byte-only check (`cp size>>8 / jr c`) rejected the exactly-full
//     1024-byte payload one byte short of the buffer's true capacity.  The
//     fixed check rejects only len > 1024.
//
//   - L5: main_handle_lit_insts' zero-length guard.  A LIT_DATA / INSN_RUN
//     record always carries at least its 1-byte tag/mode byte, so a
//     payload_len of 0 is malformed.  Without the guard the handler's
//     unconditional `dec bc` underflows BC to &FFFF and the pass-2 memcpy /
//     PASS_PC advance runs for 65535 bytes, smashing OUT.  The guard rejects
//     a zero-length record cleanly.
//
// Both cases need a .tbn the Go front-end would never emit, so the records
// are hand-assembled and wrapped with format.WriteFile (which lays down the
// magic/version/header tables the reader expects).
package main

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

// wrapRecords serialises a raw record stream into a complete .tbn (empty
// symbol/label/local/editor tables — the records are all the assembler sees).
func wrapRecords(t *testing.T, records []byte) []byte {
	t.Helper()
	st := format.NewSymbolTable()
	var buf bytes.Buffer
	if err := format.WriteFile(&buf, st, nil, nil, records, nil, nil); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return buf.Bytes()
}

// litDataRecord hand-builds a LIT_DATA record header [kind][len u16 LE]
// followed by payloadLen payload bytes (a 1-byte directive tag then data).
// payloadLen may be 0 to forge the malformed zero-length record for L5.
func litDataRecord(payloadLen int) []byte {
	var hdr [3]byte
	hdr[0] = byte(format.KindLitData)
	binary.LittleEndian.PutUint16(hdr[1:], uint16(payloadLen))
	return append(hdr[:], make([]byte, payloadLen)...)
}

// TestRecordPayloadExactlyFull (L4): a record whose payload is exactly 1024
// bytes (= STAGING_BUF capacity) must assemble cleanly; 1025 must fail.
func TestRecordPayloadExactlyFull(t *testing.T) {
	build := filepath.Join(repoRoot(t), "build")
	asm, err := os.ReadFile(filepath.Join(build, "assembler-prod.bin"))
	if err != nil {
		t.Fatalf("read assembler: %v", err)
	}
	enc, err := os.ReadFile(filepath.Join(build, "enctab.enc"))
	if err != nil {
		t.Fatalf("read enctab: %v", err)
	}

	cases := []struct {
		name       string
		payloadLen int
		wantPass   bool
	}{
		{"payload 1023 (under full)", 1023, true},
		{"payload 1024 (exactly full — L4)", 1024, true},
		{"payload 1025 (overflow)", 1025, false},
	}
	for _, c := range cases {
		tbn := wrapRecords(t, litDataRecord(c.payloadLen))
		res := runProdComplete(t, asm, enc, tbn, 10*time.Second)
		if c.wantPass && !res.Passed {
			t.Errorf("%s: expected clean assembly, got printer=%q exit=%s",
				c.name, res.PrinterCapture, res.ExitReason)
		}
		if !c.wantPass && res.Passed {
			t.Errorf("%s: expected STAGING_BUF overflow rejection, but assembly PASSED",
				c.name)
		}
		if (c.wantPass && res.Passed) || (!c.wantPass && !res.Passed) {
			t.Logf("%s: %v as expected", c.name, res.Passed)
		}
	}
}

// TestZeroLengthRecordRejected (L5): a zero-length LIT_DATA record must be
// rejected, not underflow BC into a 65535-byte runaway copy.
func TestZeroLengthRecordRejected(t *testing.T) {
	build := filepath.Join(repoRoot(t), "build")
	asm, err := os.ReadFile(filepath.Join(build, "assembler-prod.bin"))
	if err != nil {
		t.Fatalf("read assembler: %v", err)
	}
	enc, err := os.ReadFile(filepath.Join(build, "enctab.enc"))
	if err != nil {
		t.Fatalf("read enctab: %v", err)
	}

	tbn := wrapRecords(t, litDataRecord(0))
	res := runProdComplete(t, asm, enc, tbn, 10*time.Second)
	if res.Passed {
		t.Errorf("zero-length LIT_DATA record: expected rejection, but assembly PASSED (BC underflow!)")
	} else {
		t.Logf("zero-length record rejected as expected (printer=%q)", res.PrinterCapture)
	}
}
