// samdos_error_longjmp_test.go — coverage for the i183 SAMDOS file-I/O error
// model: the (hksp) longjmp, the no-handler default halt, and injectable
// HGTHD/HSAVE failures.  This is the emulation-first prerequisite for i25 (the
// assembler-side (hksp) error handler) — it lets that handler be verified in
// the harness instead of only on hardware.
//
// Most tests run a tiny self-contained Z80 program (no build artifacts), so
// they exercise the longjmp mechanics directly; one artifact-gated test boots
// the real prod assembler with sd13 omitted to show the "forgot -sysreg-data"
// mistake surfacing as a clean, cause-naming halt rather than a downstream
// &0038 garbage trap.
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Fixed addresses used by buildHkspProgram's installed-handler scaffolding.
const (
	hkspProgHandlerAddr = 0x8050 // handler entry (section C, page 2)
	hkspProgNameAddr    = 0x8080 // 10-byte UIFA name lives here
	hkspProgSlotAddr    = 0xFE00 // [slot] = handler entry — the longjmp's stack word
	hkspProgVecAddr     = 0xFE10 // (hksp) vector; its 2-byte value points at the slot
)

// buildHkspProgram assembles a tiny Z80 program (loaded at &8000) that:
//   - copies the SAMDOS UIFA name "MISSING   " into &4B01..&4B0A (so HGTHD
//     reads a deterministic name),
//   - optionally installs an (hksp) error handler: (hksp) at hkspProgVecAddr
//     holds hkspProgSlotAddr, and [hkspProgSlotAddr] holds hkspProgHandlerAddr,
//     so derr's modelled `ld sp,(hksp); ret` lands at the handler,
//   - issues RST 8 / DEFB hookCode to drive the hook.
//
// The handler emits 'E' on the printer channel then HALTs, so a run whose
// printer capture is "E" proves control reached the handler via the longjmp.
// Without the handler the program falls through to a trailing HALT.
func buildHkspProgram(installHandler bool, hookCode byte) []byte {
	p := make([]byte, hkspProgNameAddr-0x8000+10)
	pc := 0
	emit := func(bs ...byte) { copy(p[pc:], bs); pc += len(bs) }
	lo := func(a int) byte { return byte(a & 0xFF) }
	hi := func(a int) byte { return byte(a >> 8) }

	emit(0x31, 0x00, 0xC1)                                 // LD SP, &C100
	emit(0x21, lo(hkspProgNameAddr), hi(hkspProgNameAddr)) // LD HL, name
	emit(0x11, 0x01, 0x4B)                                 // LD DE, &4B01
	emit(0x01, 0x0A, 0x00)                                 // LD BC, 10
	emit(0xED, 0xB0)                                       // LDIR (name → UIFA[1..10])
	if installHandler {
		emit(0x21, lo(hkspProgHandlerAddr), hi(hkspProgHandlerAddr)) // LD HL, handler
		emit(0x22, lo(hkspProgSlotAddr), hi(hkspProgSlotAddr))       // LD (slot), HL
		emit(0x21, lo(hkspProgSlotAddr), hi(hkspProgSlotAddr))       // LD HL, slot
		emit(0x22, lo(hkspProgVecAddr), hi(hkspProgVecAddr))         // LD ((hksp)), HL
	}
	emit(0xCF, hookCode, 0x76) // RST 8 ; DEFB hookCode ; HALT (no-op fallback)
	if pc > hkspProgHandlerAddr-0x8000 {
		panic("hksp program preamble overran the handler region")
	}
	// Handler: LD A,'E'; OUT (&E8),A; LD A,1; OUT (&E9),A; HALT.
	copy(p[hkspProgHandlerAddr-0x8000:], []byte{0x3E, 'E', 0xD3, 0xE8, 0x3E, 0x01, 0xD3, 0xE9, 0x76})
	copy(p[hkspProgNameAddr-0x8000:], []byte("MISSING   "))
	return p
}

// TestSAMDOSFileNotFoundDefaultHalt: an unknown HGTHD file with no handler
// installed ((hksp)=0) takes derr1's default path — a clean halt naming the
// file at the true point of failure, never a downstream &0038 garbage trap.
func TestSAMDOSFileNotFoundDefaultHalt(t *testing.T) {
	prog := buildHkspProgram(false, hookHGTHD)
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
		t.Errorf("file-not-found surfaced as a downstream trap, not a clean longjmp halt: %q", res.ExitReason)
	}
	if len(res.UnservedFiles) != 1 || res.UnservedFiles[0] != "MISSING" {
		t.Errorf("UnservedFiles = %v; want [MISSING]", res.UnservedFiles)
	}
	if res.PrinterCapture != "" {
		t.Errorf("no handler installed, so nothing should print; got %q", res.PrinterCapture)
	}
}

// TestSAMDOSFileNotFoundLongjmp: with an (hksp) handler installed, a
// file-not-found longjmps into it (derr's `ld sp,(hksp); ret`).  The handler
// prints 'E', proving control reached it via the modelled longjmp.
func TestSAMDOSFileNotFoundLongjmp(t *testing.T) {
	prog := buildHkspProgram(true, hookHGTHD)
	res, _, _ := RunConfig(Config{
		AssemblerBin:       prog,
		HkspAddr:           hkspProgVecAddr,
		StrictFileNotFound: true,
		Timeout:            2 * time.Second,
	})
	t.Logf("Exit: %s  Printer: %q", res.ExitReason, res.PrinterCapture)
	if res.PrinterCapture != "E" {
		t.Fatalf("(hksp) longjmp did not reach the handler: printer=%q exit=%q", res.PrinterCapture, res.ExitReason)
	}
}

// TestSAMDOSInjectedHGTHDFailure: Config.FailHGTHD forces a *served* file to
// report file-not-found, exercising the error path (and i25's handler) without
// physically omitting a payload.  The installed handler runs.
func TestSAMDOSInjectedHGTHDFailure(t *testing.T) {
	prog := buildHkspProgram(true, hookHGTHD)
	res, _, _ := RunConfig(Config{
		AssemblerBin: prog,
		HkspAddr:     hkspProgVecAddr,
		// "MISSING" IS served, but FailHGTHD forces a not-found error —
		// and it fires without StrictFileNotFound (injection always errors).
		Files:     []NamedFile{{Name: "MISSING", Content: []byte{1, 2, 3}, TargetPage: 9}},
		FailHGTHD: map[string]bool{"MISSING": true},
		Timeout:   2 * time.Second,
	})
	t.Logf("Exit: %s  Printer: %q", res.ExitReason, res.PrinterCapture)
	if res.PrinterCapture != "E" {
		t.Fatalf("injected HGTHD failure did not longjmp to the handler: printer=%q exit=%q", res.PrinterCapture, res.ExitReason)
	}
}

// TestSAMDOSInjectedHSAVEFailure: Config.FailHSAVE turns a save into a
// disk-full / name-conflict error that longjmps via (hksp), exactly like a
// failed read (samdos-file-io.md "Critical caveat").
func TestSAMDOSInjectedHSAVEFailure(t *testing.T) {
	prog := buildHkspProgram(true, hookHSAVE)
	res, _, _ := RunConfig(Config{
		AssemblerBin: prog,
		HkspAddr:     hkspProgVecAddr,
		FailHSAVE:    true,
		Timeout:      2 * time.Second,
	})
	t.Logf("Exit: %s  Printer: %q", res.ExitReason, res.PrinterCapture)
	if res.PrinterCapture != "E" {
		t.Fatalf("injected HSAVE failure did not longjmp to the handler: printer=%q exit=%q", res.PrinterCapture, res.ExitReason)
	}
}

// TestSAMDOSHSAVEFailureDefaultHalt: an HSAVE failure with no handler halts
// cleanly with a cause-naming message and captures no OUT.
func TestSAMDOSHSAVEFailureDefaultHalt(t *testing.T) {
	prog := buildHkspProgram(false, hookHSAVE)
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
// injected failure) keeps the legacy silent no-op an unknown HGTHD file — the
// behaviour the decode-focused prod-boot tests rely on.  The file is still
// recorded in UnservedFiles for diagnostics, but the run is not halted.
func TestSAMDOSLegacySilentNoOpPreserved(t *testing.T) {
	prog := buildHkspProgram(false, hookHGTHD)
	res, _, _ := RunConfig(Config{
		AssemblerBin: prog,
		Timeout:      2 * time.Second,
	})
	t.Logf("Exit: %s", res.ExitReason)
	if !strings.Contains(res.ExitReason, "HALT") {
		t.Errorf("expected the program to run through to its own HALT (silent no-op), got exit=%q", res.ExitReason)
	}
	if len(res.UnservedFiles) != 1 || res.UnservedFiles[0] != "MISSING" {
		t.Errorf("UnservedFiles = %v; want [MISSING] (diagnostic still recorded)", res.UnservedFiles)
	}
	if res.PrinterCapture != "" {
		t.Errorf("silent no-op should print nothing; got %q", res.PrinterCapture)
	}
}

// TestSAMDOSBootMissingSysregDataDiagnostic boots the real prod assembler with
// sd13 deliberately omitted — the "forgot -sysreg-data" mistake.  Under
// StrictFileNotFound the boot fails at the sd13 HGTHD with a clean,
// cause-naming halt (with the -sysreg-data remedy) instead of a downstream
// &0038 garbage trap.  Artifact-gated: skips if the prod build is absent.
func TestSAMDOSBootMissingSysregDataDiagnostic(t *testing.T) {
	root := repoRoot(t)
	asmPath := filepath.Join(root, "build", "assembler-prod.bin")
	encPath := filepath.Join(root, "build", "enctab.enc")
	d15Path := filepath.Join(root, "build", "disasm.bin")
	for _, p := range []string{asmPath, encPath, d15Path} {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("prerequisite missing: %s\n  run `make assembler-prod enctab disasm-payload`", p)
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
		t.Errorf("missing sd13 surfaced as a downstream trap, not the clean longjmp halt: %q", res.ExitReason)
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
