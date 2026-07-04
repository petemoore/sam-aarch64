// prep_reader_test.go — coverage for the real SAM `.include` reader
// (prep_sam_reader, src/prep_sam_reader.asm; i31b-b3b).
//
// The reader binds the on-SAM preprocessor's pluggable reader-vector contract
// (asmprep.asm: REQ_NAME_* in, READ_CONTENT_* + CF out) to the real SAM disk
// loader — UIFA + HGTHD + trampoline-HLOAD (the load_in_file pattern) — with a
// temporary DOSER (&5BC0) bracket that turns a file-not-found into a clean CF=0
// return instead of a BASIC longjmp. build/prep_reader_driver.bin is a
// standalone org-&8000 driver that requests the fixed name "INC1", calls
// prep_sam_reader, and reports on the printer channel:
//
//	hit  → "H" + READ_CONTENT_LEN (lo,hi); the bytes land in PREP_READER_PAGE=9.
//	miss → "M".
//
// This exercises the same HGTHD/HLOAD + DOSER model the boot self-tests use, so
// the reader is verified in the harness (SimCoupé is the gate) before it is
// wired into the paged prep image (i370) and the .include-from-disk exercise
// (i371).
package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// prepReaderPage is PREP_READER_PAGE in src/prep_reader_driver.asm — the
// physical page the reader HLOADs the include into.
const prepReaderPage = 9

// loadPrepReaderDriver reads the standalone reader driver binary. A missing
// artifact is a build-wiring bug, not a reason to skip (i253): TestMain runs
// `make harness-artifacts` (which builds prep-reader-z80) before any test, so
// absence here means the build genuinely failed — fail loudly.
func loadPrepReaderDriver(t *testing.T) []byte {
	t.Helper()
	root := repoRoot(t)
	path := filepath.Join(root, "build", "prep_reader_driver.bin")
	bin, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v (run `make prep-reader-z80`)", path, err)
	}
	if len(bin) == 0 {
		t.Fatalf("%s is empty", path)
	}
	return bin
}

// TestPrepSamReaderHit: the reader resolves "INC1" to a served DOS file, HGTHDs
// its geometry, and trampoline-HLOADs it into page 9. It must report a hit ("H")
// with READ_CONTENT_LEN == the file length, and the bytes on page 9 must equal
// the served content — proving name-map → HGTHD → HLOAD end-to-end.
func TestPrepSamReaderHit(t *testing.T) {
	driver := loadPrepReaderDriver(t)
	content := []byte(".set INCVAR 7\n")

	res, _, _, hw := RunConfigHW(Config{
		AssemblerBin: driver,
		Files:        []NamedFile{{Name: "MACROS", Content: content, TargetPage: prepReaderPage}},
		Timeout:      2 * time.Second,
	})

	if res.PrinterCapture != "H" {
		t.Fatalf("printer = %q, want %q (exit %q) — the reader did not report a hit (CF=1)",
			res.PrinterCapture, "H", res.ExitReason)
	}

	// The trampoline-HLOAD landed the file in physical page 9; verify the bytes
	// directly from the pager (independent of the current logical mapping). A
	// correct match proves name-map → HGTHD (geometry) → HLOAD end-to-end, and
	// that READ_CONTENT_LEN (= the HLOAD byte count) was the true file length.
	got := hw.readPageBytes(prepReaderPage, 0, len(content))
	if !bytes.Equal(got, content) {
		t.Errorf("page %d content = %q, want %q", prepReaderPage, got, content)
	}
}

// TestPrepSamReaderMiss: with no "INC1" served and StrictFileNotFound set, the
// HGTHD is a genuine file-not-found. The reader's DOSER bracket must convert
// that to CF=0 ("M") and let the driver reach its HALT cleanly — NOT longjmp to
// BASIC (which would surface as a pendingFault / non-"M" capture).
func TestPrepSamReaderMiss(t *testing.T) {
	driver := loadPrepReaderDriver(t)

	res, _, _, _ := RunConfigHW(Config{
		AssemblerBin:       driver,
		StrictFileNotFound: true,
		Timeout:            2 * time.Second,
	})

	if res.PrinterCapture != "M" {
		t.Fatalf("printer = %q, want %q (exit %q) — the DOSER not-found bracket did not return CF=0 cleanly",
			res.PrinterCapture, "M", res.ExitReason)
	}
	if len(res.UnservedFiles) != 1 || res.UnservedFiles[0] != "MACROS" {
		t.Errorf("UnservedFiles = %v, want [MACROS]", res.UnservedFiles)
	}
}
