package z80_test

// assemble_disk_boot_faithful_test.go — the i365d-b2b emulation gate (the
// "assemble half" of the i365 demo, docs/specs/i365-demo-architecture.md): boot
// the assemble->disk BOOTABLE — build/assembler-demo.bin (DEMO_ASM) composed as a
// CODE-auto Trinity-record vessel ("AUTOasm") — on the faithful rig (Colin's real
// ROM + B-DOS 1.5t + the SPI SD model), have it HLOAD its payloads (enctab.enc,
// sd13, zx013, d15) AND its DOS 'IN' file (release-unstripped.tbn) from the boot
// record, run the two-pass assemble (main_assemble), HSAVE the result as
// 'RELEASEIMG', then reconstruct RELEASEIMG from the record's sectors and
// byte-compare to build/release-unstripped.img (the aarch64 image the Go authority
// byte-matches GNU as).
//
// What this proves over the prior gates: assembler_record_vessel_test.go boots the
// BUILD_TESTS AUTOasm from a record and proves the payload HLOADs work under ALHK
// exec, but ships NO 'IN' and stops at main_assemble — so load_in_file's real
// multi-page prefix HLOAD and the HSAVE-to-record were never exercised on the
// faithful rig under a record boot. The z80-test-harness-go
// TestReleaseDemoAssembleSave proves the full assemble+HSAVE end to end but through
// a MODELLED HGTHD/HLOAD, not real B-DOS. This test closes the gap: the whole
// assemble half — ALHK come-up, real-B-DOS load_in_file prefix load from the
// record, the full release assemble, real-B-DOS HSAVE onto the record — on the same
// B-DOS 1.5t the hardware runs, NOT the BDOSStore mock (i356).
//
// Gated on the proprietary captures; skips only under SKIP_PRIVATE_TESTS (i253),
// via bootToEditorIdleSDENC -> loadRealCaptures. Emulation-verified is not
// hardware-verified (CLAUDE.md §5) — the on-SAM run is a separate follow-up (i365e).

import (
	"bytes"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	assembleDiskBootRecordMGT = "../../../build/assemble_disk_boot_record.mgt"
	assemblerDemoMap          = "../../../build/assembler-demo.map"
	releaseUnstrippedImg      = "../../../build/release-unstripped.img"
)

// TestAssembleDiskBootFaithful boots the b2b vessel from a record and asserts the
// RELEASEIMG it HSAVEd to that record byte-matches build/release-unstripped.img.
func TestAssembleDiskBootFaithful(t *testing.T) {
	mac, _, sd, _ := bootToEditorIdleSDENC(t)

	mgt := mustReadFile(t, assembleDiskBootRecordMGT, "make netboot-assemble-disk-boot-record")
	want := mustReadFile(t, releaseUnstrippedImg, "make release-unstripped-tbn")

	// Resolve the demo assembler's landmarks BEFORE stageBootRecord loads
	// boot_record's map over the symbol table (the assembler_record_vessel ordering).
	if err := mac.LoadSymbols(assemblerDemoMap); err != nil {
		t.Fatalf("load demo assembler symbols: %v — rebuild with `make assembler-demo`", err)
	}
	asmSym := func(name string) uint16 {
		a, err := mac.Sym(name)
		if err != nil {
			t.Fatalf("demo assembler symbol %q absent from %s — rebuild with `make assembler-demo`", name, assemblerDemoMap)
		}
		return a
	}
	printStatus := asmSym("print_status_string") // reached right after save_out_file on success (and on the fail path)
	lastFailTag := asmSym("LAST_FAIL_TAG")

	const bootRecord = 2
	seedRecordFromMGT(sd, bootRecord, mgt, "asmdemo")
	seedRecordList(sd, map[int]string{1: "rec1", bootRecord: "asmdemo"})

	// --- boot the record: ALHK runs AUTOasm; it comes up under DEMO_ASM, HLOADs
	// enctab/sd13/zx013/d15 + the 'IN' prefix from the record through real B-DOS
	// hooks, assembles, HSAVEs RELEASEIMG onto the record, then prints "OK" (=>
	// print_status_string). Stop there — the DEMO ret would return into ALHK. ---
	const page = 1
	_, brMain := stageBootRecord(t, mac, bootRecord)
	armServeDispatch(mac, page)
	mac.Pager().HMPR = page
	res, err := mac.ContinueFrom(brMain, z80h.Entry{
		StepCap: 8_000_000_000, FrameIntPeriod: 60000,
		StopPC: printStatus,
	})
	if err != nil {
		t.Fatalf("boot_record -> assemble_disk_boot faulted: %v (PC=&%04X)", err, res.PC)
	}
	tag := mac.Pager().RAM[page][lastFailTag-0x8000]
	t.Logf("vessel outcome: reachedStop=%v halted=%v PC=&%04X steps=%d LAST_FAIL_TAG=&%02X release.img=%d bytes",
		res.ReachedStop, res.Halted, res.PC, res.Steps, tag, len(want))
	if !res.ReachedStop {
		t.Fatalf("assemble vessel did not reach print_status_string (&%04X): finalPC=&%04X halted=%v LAST_FAIL_TAG=&%02X — the come-up / IN load / assemble / HSAVE wedged or hit the step cap",
			printStatus, res.PC, res.Halted, tag)
	}
	if tag != 0 {
		t.Fatalf("LAST_FAIL_TAG = &%02X — a self-test / assemble step recorded a failure tag; the vessel took the fail path, not a clean assemble", tag)
	}

	// --- the physical proof: reconstruct RELEASEIMG from the record's sectors
	// (scan the directory for the CODE entry, follow the MGT forward-link chain as
	// HLOAD does) and byte-compare to the Go authority's release-unstripped.img. ---
	got := reconstructRecordFileByName(t, sd, bootRecord, "RELEASEIMG", len(want))
	if !bytes.Equal(got, want) {
		diff := 0
		for diff < len(want) && diff < len(got) && got[diff] == want[diff] {
			diff++
		}
		t.Fatalf("RELEASEIMG (%d bytes) does not byte-match release-unstripped.img (%d bytes): first mismatch at offset %d",
			len(got), len(want), diff)
	}

	// Data safety (i295): nothing written outside the target record's band.
	if outside := sd.WrittenSectorsOutsideRecord(faithCSDBase, bootRecord); len(outside) != 0 {
		t.Fatalf("record write escaped record %d's band to LBAs %v — a HSAVE landed outside the record", bootRecord, outside)
	}
	t.Logf("assemble->disk vessel: ALHK exec -> real-B-DOS IN-prefix load + assemble + HSAVE RELEASEIMG on the record, byte-matching GNU (%d bytes)", len(want))
}

// reconstructRecordFileByName reads a named CODE file back off a Trinity record by
// scanning the record's directory (linearSec 0..39, two 256-byte entries per
// sector) for the CODE entry with the given 10-char name, then following its MGT
// forward-link chain from the entry's first (track,sector) — hopping each sector's
// 2-byte link through the side-major mapping until the terminal 0,0 — exactly as
// real B-DOS HLOAD / the i365c serve do. Strips the 9-byte CODE header and returns
// the first n data bytes. Unlike reconstructRecordFile (which assumes the render
// sink's contiguous-from-linearSec-40 layout), this follows the real chain a
// B-DOS HSAVE produces, wherever the allocator placed it.
func reconstructRecordFileByName(t *testing.T, sd *z80h.SDCard, rec int, name string, n int) []byte {
	t.Helper()
	if len(name) > 10 {
		t.Fatalf("record file name %q longer than the 10-char B-DOS directory field", name)
	}
	padded := name + "          "[:10-len(name)]

	// Scan the 40 directory sectors (2 entries each) for the CODE entry.
	var track, sector byte
	found := false
	for lin := 0; lin < 40 && !found; lin++ {
		sec, ok := sd.RecordDataSector(faithCSDBase, rec, lin)
		if !ok {
			continue // an unwritten directory sector — keep scanning
		}
		for _, base := range []int{0, 256} {
			e := sec[base : base+256]
			if e[0] != 0x13 { // 0x13 = CODE
				continue
			}
			if string(e[1:11]) != padded {
				continue
			}
			track, sector = e[0x0D], e[0x0E]
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("no CODE directory entry named %q found in record %d — the HSAVE never wrote the file (or wrote it under a different name)", name, rec)
	}

	// Follow the forward-link chain, collecting 510-byte data units per sector.
	secCount := (n + rdpHdrLen + rdpPayload - 1) / rdpPayload // ceil((n+9)/510)
	body := make([]byte, 0, (secCount+1)*rdpPayload)
	for hop := 0; ; hop++ {
		if hop > secCount+2 {
			t.Fatalf("chain for %q did not terminate within %d sectors — a forward link is wrong or loops", name, secCount+2)
		}
		linear := mgtTSToRecordLinear(track, sector)
		sec, ok := sd.RecordDataSector(faithCSDBase, rec, linear)
		if !ok {
			t.Fatalf("record %d chain sector (linearSec %d, track %d sector %d) for %q was never written", rec, linear, track, sector, name)
		}
		body = append(body, sec[:rdpPayload]...)
		nt, ns := sec[rdpPayload], sec[rdpPayload+1]
		if nt == 0 && ns == 0 { // terminal link
			break
		}
		track, sector = nt, ns
	}
	if len(body) < rdpHdrLen+n {
		t.Fatalf("reconstructed %q body is %d bytes, need at least %d (9-byte header + %d data)", name, len(body), rdpHdrLen+n, n)
	}
	return body[rdpHdrLen : rdpHdrLen+n]
}
