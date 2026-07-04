package z80_test

// demo_orchestrator_faithful_test.go — the i365d-b2c capstone emulation gate,
// PHASE A (render->assemble sequencing; docs/plans/i365d-b2c-orchestrator.md).
// Boot the DEMO_CHAIN render vessel (render_chain.bin) as a CODE-auto Trinity
// record on the faithful rig (Colin's real ROM + B-DOS 1.5t + the SPI SD model):
// ALHK runs AUTOrdb, which comes up, renders release.src to the record, then —
// instead of DI;HALTing — HLOADs the assembler overlay (asmdemo) over the &8000
// window via the section-B loader stub and enters it. The assembler assembles the
// same 'IN' .tbn prefix, HSAVEs RELEASEIMG through real B-DOS onto the record, and
// `ret`s onto the CHAIN_DONE (&7C40) DI;HALT barrier. We then reconstruct BOTH
// files from the record and byte-match: release.src == render.Emit, release.img ==
// the GNU-identical build/release-unstripped.img.
//
// This proves the one genuinely new capstone capability: a runtime overlay load
// (HLOAD a whole >free-RAM vessel over the exec window from outside it) sequencing
// two co-resident-incompatible phases that both persist to the record — on the
// same real B-DOS the hardware runs, not the BDOSStore mock (i356). The serve leg
// + NBMANIFEST long names + on-screen messages are Phase B; the on-SAM run is
// i365e (CLAUDE.md §5). Gated on the proprietary captures; skips only under
// SKIP_PRIVATE_TESTS (i253), via bootToEditorIdleSDENC -> loadRealCaptures.

import (
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
	render "github.com/petemoore/sam-aarch64/tools/sam-aarch64/render"
)

const (
	renderChainBin        = "../../../build/render_chain.bin"
	renderChainMap        = "../../../build/render_chain.map"
	demoOrchestratorMGT   = "../../../build/demo_orchestrator_record.mgt"
)

// TestDemoOrchestratorFaithful boots the b2c Phase-A demo record and asserts the
// render->assemble chain wrote both RELEASESRC and RELEASEIMG to the record.
func TestDemoOrchestratorFaithful(t *testing.T) {
	mac, _, sd, _ := bootToEditorIdleSDENC(t)

	mgt := mustReadFile(t, demoOrchestratorMGT, "make netboot-demo-orchestrator-record")
	tbn := mustReadFile(t, releaseUnstrippedTBN, "make release-unstripped-tbn")
	wantImg := mustReadFile(t, releaseUnstrippedImg, "make release-unstripped-tbn")

	// The byte-exact source target: what the Go renderer emits over the same tbn.
	wantSrc, err := render.Emit(tbn)
	if err != nil {
		t.Fatalf("render.Emit: %v", err)
	}

	// Resolve the render vessel's landmarks BEFORE stageBootRecord loads
	// boot_record's map over the symbol table (the b2a/largefile ordering).
	if err := mac.LoadSymbols(renderChainMap); err != nil {
		t.Fatalf("load vessel symbols: %v — rebuild with `make netboot-render-chain`", err)
	}
	vSym := func(name string) uint16 {
		a, err := mac.Sym(name)
		if err != nil {
			t.Fatalf("vessel symbol %q absent from %s", name, renderChainMap)
		}
		return a
	}
	// CHAIN_DONE is an `equ` (a bare RAM address the render pokes DI;HALT into),
	// so it is not emitted to the pyz80 mapfile — pin it to the source constant.
	const chainDone = 0x7C40 // src/netboot/render_disk_boot.asm CHAIN_DONE
	cfgRecOff := int(vSym("RDB_CFG_RECORD")) - 0x8000

	// Patch RDB_CFG_RECORD (LE16) in AUTOrdb so the render sink's raw-CMD24 LBA
	// math targets the record it booted from. The assembler's HSAVE needs no such
	// patch — it writes via B-DOS to the ambient boot-record selection (#813).
	const bootRecord = 2
	at, as := dirEntryFirstTS(t, mgt, "AUTOrdb")
	patchMGTPayloadByte(t, mgt, at, as, cfgRecOff, bootRecord)
	patchMGTPayloadByte(t, mgt, at, as, cfgRecOff+1, 0)

	seedRecordFromMGT(sd, bootRecord, mgt, "demo")
	seedRecordList(sd, map[int]string{1: "rec1", bootRecord: "demo"})

	// --- boot: ALHK runs AUTOrdb; render -> chain -> assemble; the assembler's
	// clean exit rets onto CHAIN_DONE. StopPC there (a low-RAM address neither
	// phase executes at, so it is unambiguous unlike a page-aliased vessel PC). ---
	const page = 1
	_, brMain := stageBootRecord(t, mac, bootRecord)
	armServeDispatch(mac, page)
	mac.Pager().HMPR = page
	// Step budget: render ~62.5M + chain HLOAD + assemble ~4.7M ~= 70M; cap at
	// 200M so a wedge FAILS FAST (~20s) instead of hanging to the go-test timeout.
	res, err := mac.ContinueFrom(brMain, z80h.Entry{
		StepCap:        200_000_000,
		FrameIntPeriod: 60000,
		StopPC:         chainDone,
	})
	if err != nil {
		t.Fatalf("boot_record -> demo chain faulted: %v (PC=&%04X)", err, res.PC)
	}
	t.Logf("chain outcome: PC=&%04X halted=%v steps=%d (CHAIN_DONE=&%04X) src=%d img=%d",
		res.PC, res.Halted, res.Steps, chainDone, len(wantSrc), len(wantImg))
	if res.PC != chainDone {
		t.Fatalf("chain did not reach CHAIN_DONE (&%04X): stopped at PC=&%04X halted=%v steps=%d — "+
			"the render, the overlay HLOAD, or the assemble wedged / hit a fail trap",
			chainDone, res.PC, res.Halted, res.Steps)
	}

	// --- the physical proof: reconstruct both files from the record's MGT chains
	// (as HLOAD/serve do) and byte-compare to the authorities. ---
	gotSrc := reconstructRecordFileByName(t, sd, bootRecord, "RELEASESRC", len(wantSrc))
	assertRenderDiskMatch(t, gotSrc, wantSrc)
	gotImg := reconstructRecordFileByName(t, sd, bootRecord, "RELEASEIMG", len(wantImg))
	assertRenderDiskMatch(t, gotImg, wantImg)

	// Data safety (i295): nothing written outside the target record's band.
	if outside := sd.WrittenSectorsOutsideRecord(faithCSDBase, bootRecord); len(outside) != 0 {
		t.Fatalf("record write escaped record %d's band to LBAs %v — the i295 guard did not hold", bootRecord, outside)
	}
}
