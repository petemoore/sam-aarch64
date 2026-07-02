// assembler_record_vessel_test.go — the i319b-b2 emulation gate: the assembler
// record vessel (build/test_record.mgt, `make disk-record`) must be bootable by
// boot_record from the pushed context on the captured B-DOS 1.5t.
//
// This is the rule-7 gate in front of the i319b-b3 hardware shot, and it
// answers the one question the flat harness (z80-test-harness-go, which MODELS
// the RST-8 hooks and skips BASIC/DOS entirely) cannot: after B-DOS's record
// boot hands the AUTOasm CODE file control — ALHK + the ROM1 load-continuation,
// exec &8000, HMPR = start page, ROM1 off — do the assembler's own subsequent
// RST-8 HGTHD/HLOAD-by-name calls work against the booted record the way they
// do under BASIC's CALL 32768 on a floppy? The assembler's start: captures the
// boot LMPR/HMPR rather than assuming BASIC's, but the DOS workspace mapping
// under the ALHK exec context is B-DOS's business, provable only here.
//
// What reaching main_assemble proves: EVERY BUILD_TESTS boot self-test passed —
// each failure path is `jp fail`/`jp fail_with_tag` (src/assembler.asm), which
// prints FAIL to the printer channel, records LAST_FAIL_TAG, and never
// proceeds. So StopPC = main_assemble ⇔ the whole boot ladder (mem, sysreg,
// paged-call, emit-paged, disasm-on-15, zx0-on-13, trampoline, reader-paged,
// encode_inst) ran green against payloads HLOADed from the RECORD through the
// real hooks. The assemble loop itself is out of scope: the vessel ships no
// "IN" file (same as the floppy test.mgt), so the test stops at the
// main_assemble boundary.
//
// Gated on the proprietary captures; skips only under SKIP_PRIVATE_TESTS
// (i253). Emulation-verified is not hardware-verified (CLAUDE.md §5) — the
// real-SAM shot is i319b-b3.
package z80_test

import (
	"bytes"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	assemblerRecordMGT = "../../../build/test_record.mgt"
	assemblerMap       = "../../../build/assembler.map"
	enctabBin          = "../../../build/enctab.enc"
	zx0TestBin         = "../../../build/zx0-test.bin"
)

func TestBootRecordAssemblerRecordVessel(t *testing.T) {
	mac, lg, sd, _ := bootToEditorIdleSDENC(t)

	mgt := mustReadFile(t, assemblerRecordMGT, "make disk-record")
	seedRecordFromMGT(sd, 2, mgt, "asm")
	seedRecordList(sd, map[int]string{1: "rec1", 2: "asm"})

	// Resolve the assembler's landmarks BEFORE stageBootRecord loads the
	// boot_record map over the symbol table.
	if err := mac.LoadSymbols(assemblerMap); err != nil {
		t.Fatalf("load assembler symbols: %v — rebuild with `make assembler` (the recipe emits build/assembler.map)", err)
	}
	asmSym := func(name string) uint16 {
		a, err := mac.Sym(name)
		if err != nil {
			t.Fatalf("assembler symbol %q absent from %s — rebuild with `make assembler`", name, assemblerMap)
		}
		return a
	}
	mainAssemble := asmSym("main_assemble")
	lastFailTag := asmSym("LAST_FAIL_TAG")

	// Boot the record from the armed pushed-program state (the exact context
	// the real launcher's push creates; see TestBootRecordFromTrinloadIdle).
	// The PIC settle window the boot's SD traffic armed expired within the
	// boot run itself; the model expires it across the run boundary (i327).
	const page = 1
	_, brMain := stageBootRecord(t, mac, 2)
	armServeDispatch(mac, page)
	mac.Pager().HMPR = page

	// StepCap sizing: the AUTOasm load is 40 body sectors through the ROM1
	// continuation, then the boot HLOADs ~70 more sectors of sibling payloads
	// (enctab/sd13/zx013/d15 + the BUILD_TESTS payloads) through real B-DOS
	// hooks + the bit-banged SPI model, then the full self-test ladder runs.
	from := len(*lg)
	res, err := mac.ContinueFrom(brMain, z80h.Entry{
		StepCap: 800_000_000, FrameIntPeriod: 60000,
		StopPC: mainAssemble,
	})
	if err != nil {
		t.Fatalf("boot_record -> assembler record faulted: %v (PC=&%04X)", err, res.PC)
	}
	dir, body, out := classifyReads(cmd17Since(lg, from), 2)
	t.Logf("boot: finalPC=&%04X reachedStop=%v steps=%d record-2 reads dir=%d body=%d outside=%d",
		res.PC, res.ReachedStop, res.Steps, dir, body, out)
	if !res.ReachedStop {
		// The section-C image may have been replaced by the time of a wedge,
		// but LAST_FAIL_TAG (section C RAM in the vessel's first page) names
		// a self-test failure if one fired before the stop.
		tag := mac.Pager().RAM[page][lastFailTag-0x8000]
		t.Fatalf("assembler did not reach main_assemble (&%04X): finalPC=&%04X halted=%v LAST_FAIL_TAG=&%02X — the record vessel did not boot green",
			mainAssemble, res.PC, res.Halted, tag)
	}
	if body == 0 {
		t.Error("no record-2 BODY sectors read — the vessel cannot have been loaded from the card")
	}
	if tag := mac.Pager().RAM[page][lastFailTag-0x8000]; tag != 0 {
		t.Errorf("LAST_FAIL_TAG = &%02X after reaching main_assemble — a self-test recorded a failure tag yet control proceeded (fail path broken?)", tag)
	}

	// The boot's HLOAD-by-name calls must have landed the sibling payloads in
	// their absolute physical pages, byte-for-byte from the record: enctab in
	// page 4 and the zx0 payload at page 13 offset &0400. sd13 (page 13
	// offset 0) is deliberately NOT byte-compared: the payload CONTAINS live
	// scratch (match_nfields at file offset &69 — "page-local scratch,
	// writable", src/sysreg_data.asm), so its post-boot bytes legitimately
	// differ; its delivery is proven in anger by the sysreg/sysname
	// self-tests passing, which walk its tables via do_match.
	for _, c := range []struct {
		name string
		path string
		page uint8
		off  int
	}{
		{"enctab.enc", enctabBin, 4, 0},
		{"zx013", zx0TestBin, 13, 0x0400},
	} {
		want := mustReadFile(t, c.path, "make disk-record")
		got := mac.Pager().RAM[c.page][c.off : c.off+len(want)]
		if !bytes.Equal(got, want) {
			diff := 0
			for diff < len(want) && got[diff] == want[diff] {
				diff++
			}
			t.Errorf("%s: page %d bytes differ from %s at offset %#x (got &%02X want &%02X) — the boot's HLOAD from the record did not deliver it",
				c.name, c.page, c.path, c.off+diff, got[diff], want[diff])
		}
	}
	t.Logf("assembler record vessel boots green: ALHK exec -> real-hook HLOADs from the record -> all boot self-tests passed (main_assemble reached)")
}
