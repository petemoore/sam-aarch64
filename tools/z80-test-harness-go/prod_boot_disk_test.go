// prod_boot_disk_test.go — the complete production boot disk for prod-assembler
// harness tests (i184).
//
// The production assembler (assembler-prod.bin) unconditionally HLOADs three
// payloads at boot: "sd13" (the sysreg lookup data, page 13), "zx013" (the zx0
// compressor+decoder, page 13 at offset &0400) and "d15" (the disassembler,
// page 15) — src/loader.asm load_sysreg/load_zx0_payload/load_page15_payload,
// "PRODUCTION feature, both variants". Decode-focused tests used to boot with a
// minimal (often nil) file set and lean on the harness's legacy SILENT HLOAD
// no-op for those loads. That hid a class of bug: a genuinely-missing boot
// payload would also silently no-op rather than fail.
//
// The emulation-first ideal (one layer, no carve-outs — CLAUDE.md §7) is for
// every prod-boot test to serve the COMPLETE bootable disk and set
// Config.StrictFileNotFound, so an unserved boot payload FAILS LOUDLY (the
// faithful DOSER file-not-found dispatch, i183) instead of vanishing. This file
// is the shared helper that does both.
package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// prodBootDisk returns the complete set of payloads the production assembler
// HLOADs at boot, with the same target pages/offsets the real boot loader uses
// (and runBootSelfTests mirrors). Each payload is a build artifact; a missing
// one is a prerequisite failure (t.Fatal, never a silent skip — i253).
func prodBootDisk(t *testing.T) []NamedFile {
	t.Helper()
	build := filepath.Join(repoRoot(t), "build")
	read := func(name string) []byte {
		p := filepath.Join(build, name)
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("prerequisite missing: %s (%v)\n  run `make sysreg-data zx0-payload disasm-payload` from repo root", p, err)
		}
		return b
	}
	return []NamedFile{
		// sysreg lookup data on page 13 (offset 0).
		{Name: "sd13", Content: read("sysreg_data.bin"), TargetPage: 13},
		// zx0 compressor+decoder on page 13 at offset &0400 (beside sd13).
		{Name: "zx013", Content: read("zx0.bin"), TargetPage: 13, LoadOffset: 0x0400},
		// the production disassembler on page 15.
		{Name: "d15", Content: read("disasm.bin"), TargetPage: 15},
	}
}

// runProdComplete boots the production assembler over the COMPLETE prod-boot
// disk with StrictFileNotFound, so any boot payload the assembler HLOADs that is
// NOT served fails loudly instead of silently no-opping. Use this for every
// prod-assembler harness test in place of Run / RunWithFiles(..., nil, ...).
func runProdComplete(t *testing.T, assemblerBin, enctabData, inData []byte, timeout time.Duration) Result {
	t.Helper()
	res, _, _ := RunConfig(Config{
		AssemblerBin:       assemblerBin,
		EnctabData:         enctabData,
		InData:             inData,
		Files:              prodBootDisk(t),
		StrictFileNotFound: true,
		Timeout:            timeout,
	})
	return res
}
