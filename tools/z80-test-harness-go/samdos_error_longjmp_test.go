// samdos_error_longjmp_test.go — coverage for the SAMDOS file-I/O error model:
// the DOSER (&5BC0) post-hook dispatch (success AND error), the no-handler
// default halt, and injectable HGTHD/HSAVE failures.  This is the
// emulation-first prerequisite for i25 (the assembler-side DOSER error handler)
// — it lets that handler be verified in the harness instead of only on hardware.
//
// Most tests run a tiny self-contained Z80 program (no build artifacts), so
// they exercise the DOSER dispatch mechanics directly; one artifact-gated test
// boots the real prod assembler with sd13 omitted to show the "forgot
// -sysreg-data" mistake surfacing as a clean, cause-naming halt rather than a
// downstream &0038 garbage trap.
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Fixed addresses used by buildDoserProgram's scaffolding.
const (
	doserProgHandlerAddr = 0x8050 // DOSER handler entry (section C, page 2)
	doserProgNameAddr    = 0x8080 // 10-byte UIFA name lives here
	doserVecAddr         = 0x5BC0 // the real SAM DOSER sysvar address
)

// buildDoserProgram assembles a tiny Z80 program (loaded at &8000) that:
//   - sets SP = &C100,
//   - copies the SAMDOS UIFA name (space-padded to 10 chars) into &4B01..&4B0A
//     so HGTHD reads a deterministic name,
//   - optionally installs a DOSER handler: writes the handler address straight
//     into DOSER (&5BC0).  DOSER is a direct code vector (no indirection slot,
//     unlike the old (hksp) model),
//   - issues RST 8 / DEFB hookCode to drive the hook,
//   - if afterPrint != 0, prints afterPrint then HALTs — proving the hook
//     returned and the caller resumed (the success-dispatch resume path).
//
// The DOSER handler at doserProgHandlerAddr implements the alt-D convention:
//
//	AND A ; RET Z          ; success (A==0) → return to the caller's resume addr
//	LD A,'E' ; print ; HALT ; error (A!=0) → print 'E' and halt
//
// So a run whose printer capture is "E" proves the handler ran on an error;
// "K" (when afterPrint=='K') proves the success dispatch ran the handler AND
// correctly resumed the caller.
func buildDoserProgram(installHandler bool, hookCode byte, name string, afterPrint byte) []byte {
	p := make([]byte, doserProgNameAddr-0x8000+10)
	pc := 0
	emit := func(bs ...byte) { copy(p[pc:], bs); pc += len(bs) }
	lo := func(a int) byte { return byte(a & 0xFF) }
	hi := func(a int) byte { return byte(a >> 8) }

	emit(0x31, 0x00, 0xC1)                                   // LD SP, &C100
	emit(0x21, lo(doserProgNameAddr), hi(doserProgNameAddr)) // LD HL, name
	emit(0x11, 0x01, 0x4B)                                   // LD DE, &4B01
	emit(0x01, 0x0A, 0x00)                                   // LD BC, 10
	emit(0xED, 0xB0)                                         // LDIR (name → UIFA[1..10])
	if installHandler {
		emit(0x21, lo(doserProgHandlerAddr), hi(doserProgHandlerAddr)) // LD HL, handler
		emit(0x22, lo(doserVecAddr), hi(doserVecAddr))                 // LD (DOSER), HL
	}
	emit(0xCF, hookCode) // RST 8 ; DEFB hookCode
	if afterPrint != 0 {
		emit(0x3E, afterPrint, 0xD3, 0xE8, 0x3E, 0x01, 0xD3, 0xE9) // LD A,x;OUT(&E8);LD A,1;OUT(&E9)
	}
	emit(0x76) // HALT (caller-resume fallthrough)
	if pc > doserProgHandlerAddr-0x8000 {
		panic("doser program preamble overran the handler region")
	}
	// DOSER handler: AND A; RET Z (success → resume); else LD A,'E'; print; HALT.
	copy(p[doserProgHandlerAddr-0x8000:], []byte{
		0xA7,      // AND A
		0xC8,      // RET Z (success: A==0 → return to caller's resume addr)
		0x3E, 'E', // LD A,'E'
		0xD3, 0xE8, // OUT (&E8),A
		0x3E, 0x01, // LD A,1
		0xD3, 0xE9, // OUT (&E9),A
		0x76, // HALT
	})

	padded := name
	for len(padded) < 10 {
		padded += " "
	}
	copy(p[doserProgNameAddr-0x8000:], []byte(padded[:10]))
	return p
}

// TestSAMDOSDoserErrorDispatch: with a DOSER handler installed, a file-not-found
// (StrictFileNotFound) makes ROM PTDOS dispatch DOSER with A = file-not-found.
// The handler's `and a` finds A!=0, so it prints 'E' — proving the error
// dispatch reached the handler.
func TestSAMDOSDoserErrorDispatch(t *testing.T) {
	prog := buildDoserProgram(true, hookHGTHD, "MISSING", 0)
	res, _, _ := RunConfig(Config{
		AssemblerBin:       prog,
		DoserAddr:          doserVecAddr,
		StrictFileNotFound: true, // unknown file → faithful file-not-found
		Timeout:            2 * time.Second,
	})
	t.Logf("Exit: %s  Printer: %q", res.ExitReason, res.PrinterCapture)
	if res.PrinterCapture != "E" {
		t.Fatalf("DOSER error dispatch did not reach the handler: printer=%q exit=%q", res.PrinterCapture, res.ExitReason)
	}
	if len(res.UnservedFiles) != 1 || res.UnservedFiles[0] != "MISSING" {
		t.Errorf("UnservedFiles = %v; want [MISSING]", res.UnservedFiles)
	}
}

// TestSAMDOSDoserSuccessDispatchResumes: a *served* HGTHD succeeds → DOSER fires
// with A=0 → the handler's `and a; ret z` returns → the caller resumes and
// prints 'K'.  This is the key test that the success-path stack manipulation is
// correct: the handler ran AND control flowed back to the caller.
func TestSAMDOSDoserSuccessDispatchResumes(t *testing.T) {
	prog := buildDoserProgram(true, hookHGTHD, "FOUND", 'K')
	res, _, _ := RunConfig(Config{
		AssemblerBin: prog,
		DoserAddr:    doserVecAddr,
		Files:        []NamedFile{{Name: "FOUND", Content: []byte{1, 2, 3}, TargetPage: 9}},
		Timeout:      2 * time.Second,
	})
	t.Logf("Exit: %s  Printer: %q", res.ExitReason, res.PrinterCapture)
	if res.PrinterCapture != "K" {
		t.Fatalf("success DOSER dispatch did not run the handler AND resume the caller: printer=%q exit=%q", res.PrinterCapture, res.ExitReason)
	}
}

// TestSAMDOSDoserDefaultHalt: an unknown HGTHD file with no handler installed
// ((DOSER)=0) takes the default path — a clean halt naming the file at the true
// point of failure, never a downstream &0038 garbage trap.
func TestSAMDOSDoserDefaultHalt(t *testing.T) {
	prog := buildDoserProgram(false, hookHGTHD, "MISSING", 0)
	res, _, _ := RunConfig(Config{
		AssemblerBin:       prog,
		StrictFileNotFound: true, // unknown file → faithful file-not-found
		Timeout:            2 * time.Second,
	})
	t.Logf("Exit: %s", res.ExitReason)
	if res.Passed {
		t.Fatalf("expected the run to fail, got Passed; exit=%q", res.ExitReason)
	}
	if !strings.Contains(res.ExitReason, "file-I/O error") || !strings.Contains(res.ExitReason, "MISSING") {
		t.Errorf("ExitReason should name the file-I/O error and the missing file, got %q", res.ExitReason)
	}
	if strings.Contains(res.ExitReason, "TRAP") {
		t.Errorf("file-not-found surfaced as a downstream trap, not a clean default halt: %q", res.ExitReason)
	}
	if res.PrinterCapture != "" {
		t.Errorf("no handler installed, so nothing should print; got %q", res.PrinterCapture)
	}
}

// TestSAMDOSInjectedHGTHDFailure: Config.FailHGTHD forces a *served* file to
// report file-not-found, exercising the error path (and i25's handler) without
// physically omitting a payload.  The installed handler prints 'E'.
func TestSAMDOSInjectedHGTHDFailure(t *testing.T) {
	prog := buildDoserProgram(true, hookHGTHD, "FOUND", 0)
	res, _, _ := RunConfig(Config{
		AssemblerBin: prog,
		DoserAddr:    doserVecAddr,
		// "FOUND" IS served, but FailHGTHD forces a not-found error —
		// and it fires without StrictFileNotFound (injection always errors).
		Files:     []NamedFile{{Name: "FOUND", Content: []byte{1, 2, 3}, TargetPage: 9}},
		FailHGTHD: map[string]bool{"FOUND": true},
		Timeout:   2 * time.Second,
	})
	t.Logf("Exit: %s  Printer: %q", res.ExitReason, res.PrinterCapture)
	if res.PrinterCapture != "E" {
		t.Fatalf("injected HGTHD failure did not dispatch to the handler: printer=%q exit=%q", res.PrinterCapture, res.ExitReason)
	}
}

// TestSAMDOSInjectedHSAVEFailure: Config.FailHSAVE turns a save into a
// disk-full / name-conflict error that dispatches DOSER, exactly like a failed
// read (samdos-file-io.md "Critical caveat").
func TestSAMDOSInjectedHSAVEFailure(t *testing.T) {
	prog := buildDoserProgram(true, hookHSAVE, "OUT", 0)
	res, _, _ := RunConfig(Config{
		AssemblerBin: prog,
		DoserAddr:    doserVecAddr,
		FailHSAVE:    true,
		Timeout:      2 * time.Second,
	})
	t.Logf("Exit: %s  Printer: %q", res.ExitReason, res.PrinterCapture)
	if res.PrinterCapture != "E" {
		t.Fatalf("injected HSAVE failure did not dispatch to the handler: printer=%q exit=%q", res.PrinterCapture, res.ExitReason)
	}
}

// TestSAMDOSHSAVEFailureDefaultHalt: an HSAVE failure with no handler halts
// cleanly with a cause-naming message and captures no OUT.
func TestSAMDOSHSAVEFailureDefaultHalt(t *testing.T) {
	prog := buildDoserProgram(false, hookHSAVE, "OUT", 0)
	res, _, _ := RunConfig(Config{
		AssemblerBin: prog,
		FailHSAVE:    true,
		Timeout:      2 * time.Second,
	})
	t.Logf("Exit: %s", res.ExitReason)
	if res.Passed {
		t.Fatalf("expected HSAVE failure, got Passed; exit=%q", res.ExitReason)
	}
	if !strings.Contains(res.ExitReason, "HSAVE injected failure") {
		t.Errorf("ExitReason should name the HSAVE failure, got %q", res.ExitReason)
	}
	if res.OutBytes != nil {
		t.Errorf("a failed HSAVE must not capture OUT bytes, got %d bytes", len(res.OutBytes))
	}
	if res.PrinterCapture != "" {
		t.Errorf("no handler → nothing prints; got %q", res.PrinterCapture)
	}
}

// TestSAMDOSLegacySilentNoOpPreserved: the default (no StrictFileNotFound, no
// injected failure, no handler) keeps the legacy silent no-op for an unknown
// HGTHD file — the behaviour the decode-focused prod-boot tests rely on.  The
// file is still recorded in UnservedFiles for diagnostics, but the run is not
// halted: the caller resumes and prints 'K'.
func TestSAMDOSLegacySilentNoOpPreserved(t *testing.T) {
	prog := buildDoserProgram(false, hookHGTHD, "MISSING", 'K')
	res, _, _ := RunConfig(Config{
		AssemblerBin: prog,
		Timeout:      2 * time.Second,
	})
	t.Logf("Exit: %s  Printer: %q", res.ExitReason, res.PrinterCapture)
	if !strings.Contains(res.ExitReason, "HALT") {
		t.Errorf("expected the program to run through to its own HALT (silent no-op), got exit=%q", res.ExitReason)
	}
	if len(res.UnservedFiles) != 1 || res.UnservedFiles[0] != "MISSING" {
		t.Errorf("UnservedFiles = %v; want [MISSING] (diagnostic still recorded)", res.UnservedFiles)
	}
	if res.PrinterCapture != "K" {
		t.Errorf("silent no-op should resume the caller and print 'K'; got %q", res.PrinterCapture)
	}
}

// TestSAMDOSBootMissingSysregDataDiagnostic boots the real prod assembler with
// sd13 deliberately omitted — the "forgot -sysreg-data" mistake.  Under
// StrictFileNotFound the boot fails at the sd13 HGTHD with a clean,
// cause-naming halt (with the -sysreg-data remedy) instead of a downstream
// &0038 garbage trap.  No DOSER handler is installed, so this exercises the
// default-halt path on a real prod boot.  Artifact-gated: skips if the prod
// build is absent.
func TestSAMDOSBootMissingSysregDataDiagnostic(t *testing.T) {
	root := repoRoot(t)
	asmPath := filepath.Join(root, "build", "assembler-prod.bin")
	encPath := filepath.Join(root, "build", "enctab.enc")
	d15Path := filepath.Join(root, "build", "disasm.bin")
	for _, p := range []string{asmPath, encPath, d15Path} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("prerequisite missing: %s\n  run `make assembler-prod enctab disasm-payload`", p)
		}
	}
	asm, _ := os.ReadFile(asmPath)
	enc, _ := os.ReadFile(encPath)
	d15, _ := os.ReadFile(d15Path)

	// Serve d15 (loaded first, at line 320) but OMIT sd13 (loaded next, at
	// line 484).  zx013 is loaded after sd13, so the boot never reaches it.
	res, _, _ := RunConfig(Config{
		AssemblerBin:       asm,
		EnctabData:         enc,
		Files:              []NamedFile{{Name: "d15", Content: d15, TargetPage: 15}},
		StrictFileNotFound: true,
		Timeout:            10 * time.Second,
	})
	t.Logf("Exit: %s", res.ExitReason)
	if res.Passed {
		t.Fatalf("boot without sd13 unexpectedly passed; exit=%q", res.ExitReason)
	}
	if !strings.Contains(res.ExitReason, "sd13") {
		t.Errorf("ExitReason should name the missing sd13, got %q", res.ExitReason)
	}
	if !strings.Contains(res.ExitReason, "file-I/O error") {
		t.Errorf("ExitReason should be a clean file-I/O error, got %q", res.ExitReason)
	}
	if !strings.Contains(res.ExitReason, "-sysreg-data") {
		t.Errorf("ExitReason should carry the -sysreg-data remedy, got %q", res.ExitReason)
	}
	if strings.Contains(res.ExitReason, "TRAP") {
		t.Errorf("missing sd13 surfaced as a downstream trap, not the clean default halt: %q", res.ExitReason)
	}
	found := false
	for _, n := range res.UnservedFiles {
		if n == "sd13" {
			found = true
		}
	}
	if !found {
		t.Errorf("UnservedFiles should contain sd13, got %v", res.UnservedFiles)
	}
}
