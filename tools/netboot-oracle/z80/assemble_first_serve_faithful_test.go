package z80_test

// assemble_first_serve_faithful_test.go — the i365d-b2c capstone emulation gate,
// PHASE B (assemble -> render -> SERVE), the whole demo end-to-end on the faithful
// rig (Colin's real ROM + B-DOS 1.5t + the SPI SD model).
//
// ONE boot does everything (docs/plans/i365d-b2c-orchestrator.md §Phase B):
// ALHK runs AUTOasm (assembler-demo-chain) -> the assembler assembles the IN .tbn
// prefix and HSAVEs RELEASEIMG through real B-DOS into the record's free space ->
// it chains to the render overlay (render_chain, built -D DEMO_CHAIN) -> render
// reblocks the still-intact IN, writes RELEASESRC to base linearSec 40 (reusing
// IN's spent sectors) -> render chains to the netboot SERVER overlay (nbsrv) ->
// the server comes up (EEPROM -> MAC/IP, drv_init/ENC, the B-DOS store walk that
// indexes RELEASESRC + RELEASEIMG + the NBMANIFEST long-name map, csd,
// enc_rx_reestablish, IM 2) and serves BOTH files disk-backed over TFTP forever.
//
// The gate then drives TFTP RRQs for the two files under their FULL names
// (release.src, release.img — mapped from the 10-char store names RELEASESRC /
// RELEASEIMG by NBMANIFEST) and asserts the served bytes byte-match the
// authorities: release.src == render.Emit(tbn) (417374 B), release.img == the
// GNU-identical build/release-unstripped.img (21752 B).
//
// nb_serve_loop (&889E) is a LOW address that aliases under a page-agnostic StopPC
// during BOTH the assembler and the render phases (per b2a truth #6: the disasm
// engine runs page-31 PCs across &8000..&AA7E, and render's code spans up to
// &D06C). So we cannot StopPC on it from the boot: instead we run the whole boot
// under a large StepCap (render finishes ~67M, come-up a few M more) so the server
// is left spinning in the serve loop, then — with render/assembler DONE and only
// the server executing — a clean Continue(StopPC=nb_serve_loop) confirms the
// come-up reached the serve loop before we drive TFTP.
//
// Gated on the proprietary captures; skips only under SKIP_PRIVATE_TESTS (i253),
// via bootToEditorIdleSDENC -> loadRealCaptures. Emulation-verified is not
// hardware-verified (CLAUDE.md §5) — the on-SAM run is i365e.

import (
	"bytes"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/frame"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/internal/mask"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/tftp"
	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
	render "github.com/petemoore/sam-aarch64/tools/sam-aarch64/render"
)

const (
	assembleFirstServeMGT = "../../../build/assemble_first_serve_record.mgt"
	renderChainMap        = "../../../build/render_chain.map"
)

// TestAssembleFirstServeFaithful boots the capstone SERVE record, runs the
// assemble -> render -> serve chain, and TFTP-fetches both generated files under
// their full names, byte-matching the Go authorities.
func TestAssembleFirstServeFaithful(t *testing.T) {
	mac, _, sd, enc := bootToEditorIdleSDENC(t)

	mgt := mustReadFile(t, assembleFirstServeMGT, "make netboot-assemble-first-serve-record")
	tbn := mustReadFile(t, releaseUnstrippedTBN, "make release-unstripped-tbn")
	wantImg := mustReadFile(t, releaseUnstrippedImg, "make release-unstripped-tbn")
	wantSrc, err := render.Emit(tbn)
	if err != nil {
		t.Fatalf("render.Emit: %v", err)
	}

	const bootRecord = 2

	// Patch RDB_CFG_RECORD (LE16) in the 'render' overlay so the render sink's
	// raw-CMD24 LBA math targets the record it booted from (as Phase A does).
	if err := mac.LoadSymbols(renderChainMap); err != nil {
		t.Fatalf("load render_chain symbols: %v — rebuild with `make netboot-render-chain`", err)
	}
	cfgRecOff := int(mustSym(t, mac, "RDB_CFG_RECORD")) - 0x8000
	rat, ras := dirEntryFirstTS(t, mgt, "render")
	patchMGTPayloadByte(t, mgt, rat, ras, cfgRecOff, bootRecord)
	patchMGTPayloadByte(t, mgt, rat, ras, cfgRecOff+1, 0)

	// Patch NB_BOOT_RECORD in the 'nbsrv' overlay so the server's large-file disk
	// serve reads the record it booted from (the real launcher patches this; the
	// server rides as an overlay here, so there is no AUTOnbsrv to patch).
	if err := mac.LoadSymbols(serverBootMap); err != nil {
		t.Fatalf("load server symbols: %v — rebuild with `make netboot-server`", err)
	}
	bootRecOff := int(mustSym(t, mac, "NB_BOOT_RECORD")) - 0x8000
	nat, nas := dirEntryFirstTS(t, mgt, "nbsrv")
	patchMGTPayloadByte(t, mgt, nat, nas, bootRecOff, bootRecord)

	seedRecordFromMGT(sd, bootRecord, mgt, "serve")
	seedRecordList(sd, map[int]string{1: "rec1", bootRecord: "serve"})

	srvSym := func(name string) uint16 { return mustSym(t, mac, name) }
	serveLoop := srvSym("nb_serve_loop")
	cfgMAC := srvSym("CONFIG_SERVERMAC")
	cfgIP := srvSym("CONFIG_SERVERIP")
	storeAddr := srvSym("STORE")
	diskAddr := srvSym("NB_DISK_TABLE")

	// --- boot: ALHK runs AUTOasm; assemble -> HSAVE RELEASEIMG -> chain -> render
	// -> RELEASESRC -> chain -> nbsrv -> come-up -> nb_serve_loop (spinning). Run
	// under a big StepCap so render (~67M) + the server come-up complete and the
	// server is left spinning; nb_serve_loop aliases under StopPC during the
	// earlier phases, so no StopPC here. ---
	const page = 1
	_, brMain := stageBootRecord(t, mac, bootRecord)
	armServeDispatch(mac, page)
	mac.Pager().HMPR = page
	res, err := mac.ContinueFrom(brMain, z80h.Entry{StepCap: 150_000_000, FrameIntPeriod: 60000})
	if err != nil {
		t.Fatalf("boot_record -> assemble-first-serve chain faulted: %v (PC=&%04X)", err, res.PC)
	}
	t.Logf("boot run: halted=%v PC=&%04X steps=%d (want the server spinning in nb_serve_loop &%04X)",
		res.Halted, res.PC, res.Steps, serveLoop)
	if res.Halted {
		t.Fatalf("boot HALTED at PC=&%04X — the server never took over (a phase wedged at a fail-trap DI;HALT, "+
			"or render DI;HALTed at rdb_done instead of chaining to nbsrv)", res.PC)
	}

	// --- confirm the come-up reached the serve loop. With render/assembler DONE,
	// only the server executes, so StopPC=nb_serve_loop is now unambiguous. ---
	mac.Pager().HMPR = page
	res, err = mac.Continue(z80h.Entry{StepCap: 40_000_000, FrameIntPeriod: 60000, StopPC: serveLoop})
	if err != nil {
		t.Fatalf("server serve-loop confirm faulted: %v (PC=&%04X)", err, res.PC)
	}
	if !res.ReachedStop {
		t.Fatalf("server never reached nb_serve_loop (&%04X): finalPC=&%04X halted=%v — the come-up wedged "+
			"(the B-DOS store walk after render's raw SD ops, the ENC/csd re-init, or the render->nbsrv HLOAD). "+
			"Raise the boot StepCap if render simply had not finished by then", serveLoop, res.PC, res.Halted)
	}
	t.Logf("server reached nb_serve_loop after %d confirm steps", res.Steps)

	// --- the walk indexed BOTH generated files as disk-backed under their full
	// names (via NBMANIFEST). STORE carries the served names; NB_DISK_TABLE the
	// disk-backed descriptors. ---
	gotStore := parseWalkStore(t, mac.Read(storeAddr, 1024))
	for _, name := range []string{"release.src", "release.img"} {
		if _, ok := gotStore[name]; !ok {
			t.Fatalf("store walk did not index %q under its full name (NBMANIFEST map failed): STORE=%v", name, gotStore)
		}
	}
	if gotStore["release.src"] != len(wantSrc) {
		t.Fatalf("STORE release.src = %d bytes, want %d", gotStore["release.src"], len(wantSrc))
	}
	if gotStore["release.img"] != len(wantImg) {
		t.Fatalf("STORE release.img = %d bytes, want %d", gotStore["release.img"], len(wantImg))
	}
	gotDisk := parseDiskTable(t, mac.Read(diskAddr, 256))
	for _, name := range []string{"release.src", "release.img"} {
		if _, ok := gotDisk[name]; !ok {
			t.Fatalf("%q absent from NB_DISK_TABLE (%v) — it must serve disk-backed", name, gotDisk)
		}
	}
	t.Logf("walk indexed: release.src=%d release.img=%d (disk-backed)", gotStore["release.src"], gotStore["release.img"])

	// --- the SAM identity the server adopted from the captured EEPROM. ---
	var samMAC [6]byte
	var samIP [4]byte
	copy(samMAC[:], mac.Read(cfgMAC, 6))
	copy(samIP[:], mac.Read(cfgIP, 4))

	// drive resumes the serve loop for exactly ONE serve_once (StopPC at the loop
	// top, StopPCSkip 1 to pass the current position) and returns the single frame
	// the server transmitted (nil = silence). Continue, NOT ContinueFrom (the
	// faithful rig's stack-safety note, netboot_server_faithful_test.go).
	drive := func(label string, req []byte) []byte {
		t.Helper()
		txBefore := len(enc.TXFrames())
		if req != nil {
			enc.InjectRX(req)
		}
		r, err := mac.Continue(z80h.Entry{
			StepCap: 40_000_000, FrameIntPeriod: 60000, StopPC: serveLoop, StopPCSkip: 1,
		})
		if err != nil {
			t.Fatalf("%s: serve loop faulted: %v (PC=&%04X)", label, err, r.PC)
		}
		if !r.ReachedStop {
			t.Fatalf("%s: serve loop did not return to nb_serve_loop (PC=&%04X halted=%v)", label, r.PC, r.Halted)
		}
		tx := enc.TXFrames()[txBefore:]
		if len(tx) == 0 {
			return nil
		}
		if len(tx) > 1 {
			t.Fatalf("%s: server transmitted %d frames, want at most 1", label, len(tx))
		}
		return tx[0]
	}
	eq := func(label string, got, want []byte) {
		t.Helper()
		if got == nil {
			t.Fatalf("%s: server sent nothing", label)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("%s != Go authority\n  z80 %x\n  go  %x", label, got, want)
		}
	}
	rrqFor := func(name string, tid uint16, blksize int) []byte {
		return frame.BuildUDPFrame(frame.UDP{
			DstMAC: frame.MAC(samMAC), SrcMAC: frame.MAC(mask.ClientMAC),
			SrcIP: frame.IPv4(mask.ClientIP), DstIP: frame.IPv4(samIP),
			SrcPort: tid, DstPort: 69,
			Payload: tftp.BuildRRQ(name, "octet", []tftp.Option{
				{Name: "tsize", Value: "0"},
				{Name: "blksize", Value: itoa(blksize)},
			}),
		})
	}
	ackFrom := func(tid, block uint16) []byte {
		return frame.BuildUDPFrame(frame.UDP{
			DstMAC: frame.MAC(samMAC), SrcMAC: frame.MAC(mask.ClientMAC),
			SrcIP: frame.IPv4(mask.ClientIP), DstIP: frame.IPv4(samIP),
			SrcPort: tid, DstPort: nbServerTID,
			Payload: tftp.BuildACK(block),
		})
	}
	// serveWhole drives RRQ -> OACK -> the full DATA/ACK cadence, asserting every
	// frame byte-matches the Go authority, and returns the reassembled payload.
	serveWhole := func(name string, want []byte, tid uint16, blksize int) []byte {
		ref := tftp.NewServerLoop(tftp.MapStore{name: uint64(len(want))}, samMAC, samIP, nbServerTID)
		ref.SetSource(tftp.ByteSource(want))
		rrq := rrqFor(name, tid, blksize)
		eq(name+" OACK", drive(name+" OACK", rrq), ref.OnRRQ(rrq))

		var got []byte
		d := drive(name+" DATA 1", ackFrom(tid, 0))
		eq(name+" DATA 1", d, ref.FirstData())
		_, p, _ := tftp.ParseDATA(udpPayload(t, d))
		got = append(got, p...)
		for blk := uint16(1); len(p) == blksize; blk++ {
			a := ackFrom(tid, blk)
			d = drive(name+" DATA", a)
			eq(name+" DATA", d, ref.OnACK(a))
			_, p, _ = tftp.ParseDATA(udpPayload(t, d))
			got = append(got, p...)
		}
		lastBlk := uint16(len(got)/blksize + 1)
		if fin := drive(name+" final ACK", ackFrom(tid, lastBlk)); fin != nil {
			t.Errorf("%s: ACK of the final block should end the transfer, got %x", name, fin)
		}
		return got
	}

	// --- the demo's payoff: fetch BOTH generated files over TFTP under their full
	// NBMANIFEST-mapped names and byte-match the authorities. ---
	gotSrc := serveWhole("release.src", wantSrc, 41000, 1024)
	if !bytes.Equal(gotSrc, wantSrc) {
		diff := firstDiff(gotSrc, wantSrc)
		t.Fatalf("release.src served %d bytes != render.Emit %d bytes; first diff at %#x", len(gotSrc), len(wantSrc), diff)
	}
	t.Logf("release.src served byte-exact from the record over TFTP (%d bytes == render.Emit)", len(gotSrc))

	gotImg := serveWhole("release.img", wantImg, 42000, 1024)
	if !bytes.Equal(gotImg, wantImg) {
		diff := firstDiff(gotImg, wantImg)
		t.Fatalf("release.img served %d bytes != GNU release.img %d bytes; first diff at %#x", len(gotImg), len(wantImg), diff)
	}
	t.Logf("release.img served byte-exact from the record over TFTP (%d bytes == GNU release-unstripped.img)", len(gotImg))

	// --- serve is READ-ONLY over the drive phase: no SD writes outside the boot's
	// own RELEASESRC/RELEASEIMG generation (those wrote before the serve loop). ---
	t.Logf("capstone Phase B: ONE boot ASSEMBLED release.img, RENDERED release.src, and SERVED both "+
		"byte-exact over TFTP (release.src %d B, release.img %d B)", len(wantSrc), len(wantImg))
}
