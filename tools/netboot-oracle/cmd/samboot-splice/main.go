// Command samboot-splice produces the combined patched-bootblock chunk-1 image
// (i229): a copy of Colin Piggot's captured Trinity bootblock (chunk 1, the first
// 1 KB of the EEPROM) with the 3-byte SAMBOOT splice applied (CALL &805F @&409E ->
// CALL inject) and the assembled `inject` blob (build/samboot_bootblock.bin) laid
// into the bootblock free space. This is the exact chunk-1 the i230 RAM test loads
// and i135c flashes.
//
// The captured EEPROM is Colin's PROPRIETARY image and is NEVER committed: this tool
// reads it from the local capture dir and writes the combined image to a git-ignored
// path (build/samboot_bootblock_chunk1.bin). The splice LOGIC is the samboot package
// (samboot.Splice), shared with the in-Go reset-chain test.
//
// Capture-gated convenience: if the proprietary eeprom.bin is absent (a fork / CI),
// this exits 0 with a log rather than failing — the no-silent-skip rule (i253)
// governs the reset-chain TEST, not this build helper. The test FAILS hard on an
// absent capture (unless SKIP_PRIVATE_TESTS=true); this helper just no-ops.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/samboot"
)

// defaultEEPROM resolves the captured EEPROM path: $SAMBOOT_CAPTURE_DIR/eeprom.bin
// or ~/sam-archive/samboot-capture/eeprom.bin (the CAPTURE-NOTES.txt home, mirroring
// the netboot-oracle test helper). Returns "" if the home cannot be resolved.
func defaultEEPROM() string {
	if dir := os.Getenv("SAMBOOT_CAPTURE_DIR"); dir != "" {
		return filepath.Join(dir, "eeprom.bin")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "sam-archive", "samboot-capture", "eeprom.bin")
}

func main() {
	eepromPath := flag.String("eeprom", defaultEEPROM(), "captured Trinity EEPROM image (proprietary; never committed)")
	injectPath := flag.String("inject", "build/samboot_bootblock.bin", "assembled inject blob (org &415E)")
	outPath := flag.String("out", "build/samboot_bootblock_chunk1.bin", "combined chunk-1 output (git-ignored; contains capture bytes)")
	flag.Parse()

	if *eepromPath == "" {
		fmt.Fprintln(os.Stderr, "samboot-splice: cannot resolve the capture home; set -eeprom or $SAMBOOT_CAPTURE_DIR")
		os.Exit(2)
	}

	// Capture-gated: a missing proprietary EEPROM is a no-op here (the test gates it).
	if _, err := os.Stat(*eepromPath); err != nil {
		fmt.Printf("samboot-splice: proprietary capture %s absent — skipping the splice build (the reset-chain test gates this, not this helper)\n", *eepromPath)
		return
	}

	eeprom, err := os.ReadFile(*eepromPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "samboot-splice: read eeprom: %v\n", err)
		os.Exit(1)
	}
	if len(eeprom) < samboot.ChunkSize {
		fmt.Fprintf(os.Stderr, "samboot-splice: eeprom is %d bytes, need at least %d (chunk 1)\n", len(eeprom), samboot.ChunkSize)
		os.Exit(1)
	}

	inject, err := os.ReadFile(*injectPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "samboot-splice: read inject %s: %v (run `make netboot-samboot-inject` first)\n", *injectPath, err)
		os.Exit(1)
	}

	// chunk 1 = the first 1 KB of the EEPROM (file offset 0 = device &2000 = the
	// bootblock; see samboot_real_boot_test.go's deviceLinearEEPROM note).
	combined, err := samboot.Splice(eeprom[:samboot.ChunkSize], inject)
	if err != nil {
		fmt.Fprintf(os.Stderr, "samboot-splice: %v\n", err)
		os.Exit(1)
	}

	if err := os.MkdirAll(filepath.Dir(*outPath), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "samboot-splice: mkdir: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*outPath, combined, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "samboot-splice: write %s: %v\n", *outPath, err)
		os.Exit(1)
	}
	fmt.Printf("samboot-splice: wrote %s (%d bytes; inject %d B into %d free; CALL &805F @&409E -> CALL &415E)\n",
		*outPath, len(combined), len(inject), samboot.FreeSpace)
}
