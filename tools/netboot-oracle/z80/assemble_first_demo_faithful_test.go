package z80_test

// assemble_first_demo_faithful_test.go — the i365d-b2c capstone emulation gate,
// PHASE A, ASSEMBLE-FIRST ordering (docs/plans/i365d-b2c-orchestrator.md §FIX 4).
// Boot the assemble-first capstone record on the faithful rig (Colin's real ROM +
// B-DOS 1.5t + the SPI SD model): ALHK runs AUTOasm (assembler-demo-chain, built
// DEMO_ASM+DEMO_CHAIN), which comes up, HLOADs its payloads + the 'IN' .tbn prefix
// from the record, assembles, HSAVEs RELEASEIMG through real B-DOS into the
// record's free space — then, instead of returning, HGTHDs the 'render' CODE
// overlay, LDIRs a section-B loader stub and JPs it. The stub HLOADs render over
// the &8000 window and enters it; render comes up, reblocks the STILL-INTACT IN,
// renders release.src, streams RELEASESRC to base linearSec 40 (reusing IN's spent
// sectors), then DI;HALTs at rdb_done. We reconstruct BOTH files from the record
// and byte-match: release.img == the GNU-identical build/release-unstripped.img,
// release.src == render.Emit.
//
// Why assemble-first (not render-first): render's RELEASESRC write is contiguous
// from linearSec 40 for ~820 sectors, overwriting IN and the assembler overlay on
// the record; and the 1600-sector record cannot hold RELEASESRC + RELEASEIMG
// WITHOUT reusing IN's sectors. So IN must be read by BOTH phases before render
// overwrites it → order = assemble-FIRST, and there is NO B-DOS op after render
// (its IN-reblock DOS-page clobber is harmless). This proves the one genuinely new
// capstone capability — a runtime overlay load sequencing two co-resident-
// incompatible phases that both persist to the record — on the same real B-DOS the
// hardware runs, not the BDOSStore mock (i356). The serve leg + NBMANIFEST long
// names + on-screen messages are Phase B; the on-SAM run is i365e (CLAUDE.md §5).
// Gated on the proprietary captures; skips only under SKIP_PRIVATE_TESTS (i253),
// via bootToEditorIdleSDENC -> loadRealCaptures.

import (
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
	render "github.com/petemoore/sam-aarch64/tools/sam-aarch64/render"
)

const assembleFirstDemoMGT = "../../../build/assemble_first_demo_record.mgt"

// TestAssembleFirstDemoFaithful boots the assemble-first capstone record and
// asserts the assemble->render chain wrote both RELEASEIMG and RELEASESRC.
func TestAssembleFirstDemoFaithful(t *testing.T) {
	mac, _, sd, _ := bootToEditorIdleSDENC(t)

	mgt := mustReadFile(t, assembleFirstDemoMGT, "make netboot-assemble-first-demo-record")
	tbn := mustReadFile(t, releaseUnstrippedTBN, "make release-unstripped-tbn")
	wantImg := mustReadFile(t, releaseUnstrippedImg, "make release-unstripped-tbn")

	// The byte-exact source target: what the Go renderer emits over the same tbn.
	wantSrc, err := render.Emit(tbn)
	if err != nil {
		t.Fatalf("render.Emit: %v", err)
	}

	// Resolve the render overlay's landmarks BEFORE stageBootRecord loads
	// boot_record's map over the symbol table (the b2a/largefile ordering). The
	// render vessel is the b2a build (render_disk_boot.bin / .map) — it ships as
	// the 'render' overlay here, HLOADed by the assembler's chain tail.
	if err := mac.LoadSymbols(renderDiskBootMap); err != nil {
		t.Fatalf("load render overlay symbols: %v — rebuild with `make netboot-render-disk-boot`", err)
	}
	vSym := func(name string) uint16 {
		a, err := mac.Sym(name)
		if err != nil {
			t.Fatalf("render overlay symbol %q absent from %s", name, renderDiskBootMap)
		}
		return a
	}
	phaseAddr := vSym("rdb_phase")
	verdictAddr := vSym("rdb_verdict")
	cfgRecOff := int(vSym("RDB_CFG_RECORD")) - 0x8000

	// Patch RDB_CFG_RECORD (LE16) in the 'render' OVERLAY (not the AUTO file) so
	// the render sink's raw-CMD24 LBA math targets the record it booted from. The
	// assembler's HSAVE needs no such patch — it writes via B-DOS to the ambient
	// boot-record selection (#813). Patch the .mgt BEFORE seeding (seed copies it).
	const bootRecord = 2
	at, as := dirEntryFirstTS(t, mgt, "render")
	patchMGTPayloadByte(t, mgt, at, as, cfgRecOff, bootRecord)
	patchMGTPayloadByte(t, mgt, at, as, cfgRecOff+1, 0)

	seedRecordFromMGT(sd, bootRecord, mgt, "demo")
	seedRecordList(sd, map[int]string{1: "rec1", bootRecord: "demo"})

	// --- boot: ALHK runs AUTOasm; assemble -> HSAVE RELEASEIMG -> chain -> render
	// -> RELEASESRC -> render DI;HALTs at rdb_done. Run to the halt (no StopPC — a
	// page-agnostic StopPC aliases the disasm engine's page-31 PCs, per b2a). The
	// assemble (~4.67M) + chain HLOAD + render (~62.5M) ~= 70M; cap at 200M so a
	// wedge FAILS FAST (~20s) instead of hanging to the go-test timeout. ---
	const page = 1
	_, brMain := stageBootRecord(t, mac, bootRecord)
	armServeDispatch(mac, page)
	mac.Pager().HMPR = page
	res, err := mac.ContinueFrom(brMain, z80h.Entry{StepCap: 200_000_000, FrameIntPeriod: 60000})
	if err != nil {
		t.Fatalf("boot_record -> assemble-first chain faulted: %v (PC=&%04X)", err, res.PC)
	}
	mac.Pager().HMPR = page
	phase := mac.Read(phaseAddr, 1)[0]
	verdict := mac.Read(verdictAddr, 1)[0]
	t.Logf("chain outcome: halted=%v PC=&%04X phase=%q verdict=%q steps=%d src=%d img=%d",
		res.Halted, res.PC, phase, verdict, res.Steps, len(wantSrc), len(wantImg))
	if !res.Halted {
		t.Fatalf("chain did not HALT at render's rdb_done: PC=&%04X steps=%d phase=%q — the assemble, "+
			"the overlay HLOAD, or the render wedged or hit the step cap", res.PC, res.Steps, phase)
	}
	if phase != '5' {
		t.Fatalf("phase = %q, want '5' — halted at a fail trap not rdb_done (D=disasm, I=tbn re-blocked, "+
			"R=rendered, F=finished; c/e/L=come-up/load fail). A non-render phase means the assembler failed "+
			"before the chain, or the overlay never loaded", phase)
	}
	if verdict != 'P' {
		t.Fatalf("verdict = %q, want 'P' — RDS_ERR set: a bd_record_write_hw / i295-guard failure during the render stream", verdict)
	}

	// --- the physical proof: reconstruct both files from the record's MGT chains
	// (scan the directory for the CODE entry, follow the forward-link chain, as
	// HLOAD/serve do) and byte-compare to the authorities. ---
	gotImg := reconstructRecordFileByName(t, sd, bootRecord, "RELEASEIMG", len(wantImg))
	assertRenderDiskMatch(t, gotImg, wantImg)
	gotSrc := reconstructRecordFileByName(t, sd, bootRecord, "RELEASESRC", len(wantSrc))
	assertRenderDiskMatch(t, gotSrc, wantSrc)

	// Data safety (i295): nothing written outside the target record's band.
	if outside := sd.WrittenSectorsOutsideRecord(faithCSDBase, bootRecord); len(outside) != 0 {
		t.Fatalf("record write escaped record %d's band to LBAs %v — a HSAVE/CMD24 landed outside the record", bootRecord, outside)
	}
	t.Logf("assemble-first capstone: ALHK exec -> real-B-DOS assemble + HSAVE RELEASEIMG (%d B) -> overlay chain "+
		"-> render RELEASESRC (%d B), both reconstruct + byte-match from the record", len(wantImg), len(wantSrc))
}
