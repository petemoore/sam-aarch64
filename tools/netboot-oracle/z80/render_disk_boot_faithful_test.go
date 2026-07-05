package z80_test

// render_disk_boot_faithful_test.go — the i365d-b2a emulation gate (Wall 1
// capstone, docs/specs/i365-demo-architecture.md): boot the render->disk
// BOOTABLE (render_disk_boot.asm) as a CODE-auto Trinity-record vessel on the
// faithful rig (Colin's real ROM + B-DOS 1.5t + the SPI SD model), have it HLOAD
// release-unstripped.tbn (DOS 'IN') from the boot record into the render IN pages
// 8..30 and disasm.bin into page 31, render release.tbn -> release.src streamed to
// the record via render_disk_sink (i365d-b1), then reconstruct release.src from
// the record's contiguous sectors and byte-compare to the Go authority
// render.Emit (~417 KB). This is the whole demo Wall-1 path end to end — the boot
// come-up, the multi-page HLOAD from disk (the new piece over i365d-b1's probe),
// the full 23-page render, and the > free-RAM chunked disk write — proven on the
// same real B-DOS the hardware runs, NOT the BDOSStore mock (i356).
//
// Gated on the proprietary captures; skips only under SKIP_PRIVATE_TESTS (i253),
// via bootToEditorIdleSDENC -> loadRealCaptures. Emulation-verified is not
// hardware-verified (CLAUDE.md §5) — the on-SAM run is a separate follow-up.

import (
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
	render "github.com/petemoore/sam-aarch64/tools/sam-aarch64/render"
)

const (
	renderDiskBootBin       = "../../../build/render_disk_boot.bin"
	renderDiskBootMap       = "../../../build/render_disk_boot.map"
	renderDiskBootRecordMGT = "../../../build/render_disk_boot_record.mgt"
	releaseUnstrippedTBN    = "../../../build/release-unstripped.tbn"
)

// TestRenderDiskBootFaithful boots the b2a vessel from a record and asserts the
// rendered release.src it streamed to that record byte-matches render.Emit over
// the same release-unstripped.tbn.
func TestRenderDiskBootFaithful(t *testing.T) {
	mac, _, sd, _ := bootToEditorIdleSDENC(t)

	mgt := mustReadFile(t, renderDiskBootRecordMGT, "make netboot-render-disk-boot-record")
	tbn := mustReadFile(t, releaseUnstrippedTBN, "make release-unstripped-tbn")

	// The byte-exact target: the source text the Go renderer emits over the tbn.
	want, err := render.Emit(tbn)
	if err != nil {
		t.Fatalf("render.Emit: %v", err)
	}
	n := len(want)

	// Resolve the vessel's landmarks BEFORE stageBootRecord loads boot_record's
	// map over the symbol table (the netboot_server_largefile_test ordering).
	if err := mac.LoadSymbols(renderDiskBootMap); err != nil {
		t.Fatalf("load vessel symbols: %v — rebuild with `make netboot-render-disk-boot`", err)
	}
	vSym := func(name string) uint16 {
		a, err := mac.Sym(name)
		if err != nil {
			t.Fatalf("vessel symbol %q absent from %s", name, renderDiskBootMap)
		}
		return a
	}
	phaseAddr := vSym("rdb_phase")
	verdictAddr := vSym("rdb_verdict")
	cfgRecOff := int(vSym("RDB_CFG_RECORD")) - 0x8000

	// Patch RDB_CFG_RECORD (LE16) in the AUTOrdb body so the booted vessel writes
	// to the record it booted from (bd_record_write_hw's LBA math). The real
	// launcher patches this; the test does it directly. Patch the .mgt BEFORE
	// seeding — seedRecordFromMGT copies it.
	const bootRecord = 2
	at, as := dirEntryFirstTS(t, mgt, "AUTOrdb")
	patchMGTPayloadByte(t, mgt, at, as, cfgRecOff, bootRecord)
	patchMGTPayloadByte(t, mgt, at, as, cfgRecOff+1, 0)

	seedRecordFromMGT(sd, bootRecord, mgt, "rdboot")
	seedRecordList(sd, map[int]string{1: "rec1", bootRecord: "rdboot"})

	// --- boot the record: ALHK runs AUTOrdb; it comes up, HLOADs disasm +
	// re-blocks the 'IN' tbn into pages 8..30, renders release.src to the record,
	// then DI;HALTs at rdb_done. Run to the halt (no StopPC — a page-agnostic
	// StopPC aliases the disasm engine's page-31 PCs). ---
	const page = 1
	_, brMain := stageBootRecord(t, mac, bootRecord)
	armServeDispatch(mac, page)
	mac.Pager().HMPR = page
	res, err := mac.ContinueFrom(brMain, z80h.Entry{StepCap: 900_000_000, FrameIntPeriod: 60000})
	if err != nil {
		t.Fatalf("boot_record -> render_disk_boot faulted: %v (PC=&%04X)", err, res.PC)
	}
	mac.Pager().HMPR = page
	phase := mac.Read(phaseAddr, 1)[0]
	verdict := mac.Read(verdictAddr, 1)[0]
	t.Logf("vessel outcome: halted=%v PC=&%04X phase=%q verdict=%q steps=%d release.src=%d bytes",
		res.Halted, res.PC, phase, verdict, res.Steps, n)
	if !res.Halted {
		t.Fatalf("vessel did not HALT at rdb_done: PC=&%04X steps=%d phase=%q — the come-up/render wedged or hit the step cap",
			res.PC, res.Steps, phase)
	}
	if phase != '5' {
		t.Fatalf("phase = %q, want '5' — halted at a fail trap not rdb_done (D=disasm, I=tbn re-blocked, R=rendered, F=finished; c/e/L=come-up/load fail)", phase)
	}
	if verdict != 'P' {
		t.Fatalf("verdict = %q, want 'P' — RDS_ERR set: a bd_record_write_hw / i295-guard failure during the render stream", verdict)
	}

	// --- the physical proof: reconstruct release.src from the record's sectors
	// (following the MGT forward-link chain, as HLOAD/serve do) and byte-compare
	// to the Go authority. HLOAD cannot hold 417 KB resident, so this is the
	// > free-RAM proof (as in TestRenderDiskWriteReleaseSizeFaithful). ---
	got := reconstructRecordFileByName(t, sd, bootRecord, "RELEASESRC", n)
	assertRenderDiskMatch(t, got, want)
	assertDirEntryByName(t, sd, bootRecord, "RELEASESRC", n)

	// Data safety (i295): nothing written outside the target record's band.
	if outside := sd.WrittenSectorsOutsideRecord(faithCSDBase, bootRecord); len(outside) != 0 {
		t.Fatalf("record write escaped record %d's band to LBAs %v — the i295 guard did not hold", bootRecord, outside)
	}
}
