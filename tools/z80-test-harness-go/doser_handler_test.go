// doser_handler_test.go — the SAM-side DOSER (&5BC0) file-I/O error handler (i25b).
//
// The production assembler installs the DOSER handler at (&5BC0) at boot (before
// the first file-I/O hook), copied into LMPR-stable section B so it stays mapped
// even when an HLOAD trampoline has paged section C onto a payload. The ROM's
// PTDOS epilogue dispatches a file-I/O error to it with A = the SAMDOS error
// number; the handler resumes the caller on success (A==0) and converts a real
// error into the existing diagnosed FAIL banner, passing the error number through
// as the fail tag. These tests exercise both paths on the REAL assembler-prod.bin
// via the i25a harness DOSER model — the emulation gate before SimCoupé.
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestDoserHandlerFailsOnFileIOError: with the handler installed, a boot file-I/O
// error (a missing payload under StrictFileNotFound) is converted to the diagnosed
// FAIL banner instead of a silent no-op or a bare default halt. The tag is the
// SAMDOS error number 107 (0x6B = file-not-found), so the banner reads FAIL6b…,
// proving the handler ran AND passed the error number through to fail_with_tag.
func TestDoserHandlerFailsOnFileIOError(t *testing.T) {
	build := filepath.Join(repoRoot(t), "build")
	asm, err := os.ReadFile(filepath.Join(build, "assembler-prod.bin"))
	if err != nil {
		t.Fatalf("prerequisite missing: assembler-prod.bin (%v); run `make assembler-prod`", err)
	}
	enc, err := os.ReadFile(filepath.Join(build, "enctab.enc"))
	if err != nil {
		t.Fatalf("prerequisite missing: enctab.enc (%v); run `make enctab`", err)
	}
	tbn := buildAlignTbn(t, 4) // a trivial valid input; the boot fails before it is read

	// Serve the prod-boot disk with d15 OMITTED, under StrictFileNotFound: the boot's
	// d15 HLOAD reports file-not-found, the ROM dispatches DOSER, and the installed
	// handler turns it into the diagnosed FAIL.
	var partial []NamedFile
	for _, f := range prodBootDisk(t) {
		if f.Name != "d15" {
			partial = append(partial, f)
		}
	}

	res, _, _ := RunConfig(Config{
		AssemblerBin:       asm,
		EnctabData:         enc,
		InData:             tbn,
		Files:              partial,
		StrictFileNotFound: true,
		Timeout:            10 * time.Second,
	})
	t.Logf("Exit: %s  Printer: %q", res.ExitReason, res.PrinterCapture)

	if res.Passed {
		t.Fatalf("expected a diagnosed FAIL from the DOSER handler, but the run PASSED (handler not installed/reached?)")
	}
	if !strings.HasPrefix(res.PrinterCapture, "FAIL") {
		t.Fatalf("DOSER handler did not produce the diagnosed FAIL banner: printer=%q exit=%q",
			res.PrinterCapture, res.ExitReason)
	}
	if !strings.HasPrefix(res.PrinterCapture, "FAIL6b") {
		t.Errorf("FAIL tag = %q, want the file-not-found error number 6b (FAIL6b…) passed through by the handler",
			res.PrinterCapture)
	}
	if len(res.UnservedFiles) == 0 || res.UnservedFiles[0] != "d15" {
		t.Errorf("UnservedFiles = %v, want [d15]", res.UnservedFiles)
	}
}

// TestDoserHandlerResumesOnSuccess: with the COMPLETE prod-boot disk, every boot
// file op succeeds → DOSER fires with A==0 → the handler's `and a; ret z` resumes
// each time → the assembler boots and assembles cleanly. The handler is transparent
// on the success path (the success-resume half of the i25b contract).
func TestDoserHandlerResumesOnSuccess(t *testing.T) {
	build := filepath.Join(repoRoot(t), "build")
	asm, err := os.ReadFile(filepath.Join(build, "assembler-prod.bin"))
	if err != nil {
		t.Fatalf("prerequisite missing: assembler-prod.bin (%v); run `make assembler-prod`", err)
	}
	enc, _ := os.ReadFile(filepath.Join(build, "enctab.enc"))
	tbn := buildAlignTbn(t, 4)

	res := runProdComplete(t, asm, enc, tbn, 10*time.Second)
	if !res.Passed {
		t.Fatalf("complete disk should assemble cleanly with the DOSER handler installed: printer=%q exit=%q",
			res.PrinterCapture, res.ExitReason)
	}
}
