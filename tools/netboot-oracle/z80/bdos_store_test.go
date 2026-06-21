// bdos_store_test.go — i134/i119e: run the netboot client's B-DOS write-out path
// in emulation. The RST 8 hook modelled (bdos_store.go + harness.go rstHandlers)
// lets the real hook bodies run host-side so the captured record + UIFA are asserted.
//
// i119e (B5) adds two end-to-end safety tests that replace the old
// TestClientBootWriteOutCaptured (which was a placeholder asserting the pre-i119 bug
// state, record==0). The new tests drive the full bootable client_main:
//
//   TestClientE2EConfirm  — picker finds free record 3, user types 'y':
//     green border (4), 1 save, record==3, name=="recovery" (dotted suffix dropped).
//
//   TestClientE2EDecline  — same setup, user types 'n':
//     cyan border (5 = cl_write_declined), 0 saves — the CONFIRMED gate blocked the
//     write. This is the SAFETY-INVARIANT test: no HSAVE without confirm.
//
// Emulation-verified is not hardware-verified (CLAUDE.md §5): this models the
// digital hook dispatch, not a real persist.
package z80_test

import (
	"strings"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/frame"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/internal/mask"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/tftp"
	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

// TestBDOSStoreCapturesRecordAndUIFA is the mechanism check: the real
// bdos_select_record (HRECORD) and bdos_save_hook (HSAVE) bodies — the RST 8
// dispatch the flat harness has no ROM for — run host-side via the modelled hook,
// and the store captures the selected record and the decoded save UIFA. It calls
// the routines directly (no transfer) so the capture is isolated from the client
// loop.
func TestBDOSStoreCapturesRecordAndUIFA(t *testing.T) {
	mac, err := z80h.Load(cliBootBin, cliBootMap)
	if err != nil {
		t.Skipf("client boot binary not built (%v); run `make netboot-client-boot`", err)
	}
	store := z80h.NewBDOSStore()
	mac.AttachBDOS(store)

	// Select record 7 (HRECORD): bdos_select_record reads A as the record number.
	if _, err := mac.CallEntry("bdos_select_record", z80h.Entry{A: 7}); err != nil {
		t.Fatalf("call bdos_select_record: %v", err)
	}
	if store.Selected() != 7 {
		t.Fatalf("after HRECORD, store.Selected()=%d, want 7 — the RST 8 HRECORD hook did not record the selected record", store.Selected())
	}

	// Stage a dot-free filename (<=10 chars, no extension separator) so it stores
	// verbatim in the UIFA name field (the extension-drop policy only triggers on '.').
	const unitName = "savetest"
	nameAddr := symAddr(t, mac, "RRQ_FILENAME")
	mac.Write(nameAddr, append([]byte(unitName), 0))
	mac.WriteU16LE(symAddr(t, mac, "BD_NAME_PTR"), nameAddr)
	mac.Write(symAddr(t, mac, "BD_SAVE_PAGE"), []byte{3})
	mac.WriteU16LE(symAddr(t, mac, "BD_SAVE_ADDR"), 0x8000)
	mac.WriteU16LE(symAddr(t, mac, "BD_SAVE_SIZE"), 5000)
	if _, err := mac.CallEntry("bdos_fill_save_uifa", z80h.Entry{}); err != nil {
		t.Fatalf("call bdos_fill_save_uifa: %v", err)
	}
	if _, err := mac.CallEntry("bdos_save_hook", z80h.Entry{}); err != nil {
		t.Fatalf("call bdos_save_hook: %v", err)
	}

	saves := store.Saves()
	if len(saves) != 1 {
		t.Fatalf("store captured %d HSAVEs, want 1", len(saves))
	}
	s := saves[0]
	if s.Record != 7 {
		t.Errorf("save tagged record %d, want 7 (the record selected when it ran)", s.Record)
	}
	if s.Name != unitName {
		t.Errorf("save name = %q, want %q", s.Name, unitName)
	}
	if s.Page != 3 {
		t.Errorf("save page = %d, want 3", s.Page)
	}
	if s.Addr != 0x8000 {
		t.Errorf("save addr = &%04X, want &8000", s.Addr)
	}
	if s.Size != 5000 {
		t.Errorf("save size = %d, want 5000 (pages*16384 + lengthMod16K)", s.Size)
	}
}

// bootClientE2ESetup builds a loaded boot machine with ENC, a BDOSStore backed by
// a CardModel, and pre-queued TFTP frames.  Card layout:
//
//	1: "LOADER"   (named)
//	2: "KERNEL"   (named)
//	3: free       (first free — auto-pick target)
//	4: "BOOT"     (named)
//	5: "SHADEBOBS" (named)
//
// BD_RECORDS is set to 5.  Returns mac, enc, store, and the file bytes served.
func bootClientE2ESetup(t *testing.T) (*z80h.Machine, *z80h.ENC28J60, *z80h.BDOSStore, []byte) {
	t.Helper()
	// Flat Load: client_boot's i145b CSD overlay lives in section-D RAM (see the
	// romBaseBoot note in netboot_boot_test.go); LoadBoot would drop it.
	mac, err := z80h.Load(cliBootBin, cliBootMap)
	if err != nil {
		t.Skipf("client boot binary not built (%v); run `make netboot-client-boot`", err)
	}
	enc := z80h.NewENC28J60()
	enc.ProgramTrinityNetwork(mask.ServerMAC, mask.ServerIP) // the SAM's own MAC+IP

	store := z80h.NewBDOSStore()
	card := z80h.NewCardModel()
	card.SetRecordEntry(1, makeEntry("LOADER"))
	card.SetRecordEntry(2, makeEntry("KERNEL"))
	// record 3: left free (all-zero entry — the auto-pick-free target)
	card.SetRecordEntry(4, makeEntry("BOOT"))
	card.SetRecordEntry(5, makeEntry("SHADEBOBS"))
	store.AttachCard(card)

	// Attach the SD model and seed list sector 1 from the card (all 5 records
	// are within records 1..32, so only one list sector is needed).
	// csdV2(7) → (7+1)*1024 = 8192 blocks → 8192/1600 = 5 records, matching
	// the BD_RECORDS = 5 layout: client_main calls csd_set_bd_records internally
	// which overwrites the injected value, so the CSD itself must encode 5 records.
	sd := enc.AttachSD(csdV2(7))
	sec1 := card.ListSector(1)
	sd.SeedSector(1, sec1[:])

	mac.AttachIO(enc)
	mac.AttachBDOS(store)
	if _, err := mac.Call("csd_set_bd_records"); err != nil {
		t.Fatalf("csd_set_bd_records: %v", err)
	}
	mac.WriteU16LE(symAddr(t, mac, "BD_RECORDS"), 5)

	// Pre-queue the frames client_main consumes: ARP reply -> DATA block 1 (short
	// final, so XFER_DONE fires after one block).  The emulated ENC delivers one
	// frame per EPKTCNT poll, so cl_fetch_loop plays them out across iterations.
	const bootSrvTID = 40000
	bootSrvMAC := frame.MAC{0x02, 0xAA, 0xBB, 0xCC, 0xDD, 0xEE}
	file := makeFile(200)
	enc.InjectRX(frame.BuildARPReply(bootSrvMAC, mask.ServerMAC, cliBootServerIP, frame.IPv4(mask.ServerIP)))
	enc.InjectRX(frame.BuildUDPFrame(frame.UDP{
		DstMAC: frame.MAC(mask.ServerMAC), SrcMAC: bootSrvMAC,
		SrcIP: cliBootServerIP, DstIP: frame.IPv4(mask.ServerIP),
		SrcPort: bootSrvTID, DstPort: cliOwnTID,
		Payload: tftp.BuildDATA(1, file),
	}))

	return mac, enc, store, file
}

// TestClientE2EConfirm is the i119e confirm path: the real bootable client_main,
// given a card with records 1+2 named and record 3 free, and the user pressing 'y',
// completes with a green border (4) and exactly one HSAVE tagged to record 3.
//
// This also verifies the gap-4 extension-drop fix (i119e Change 2): cl_filename is
// "recovery.mgt", so the stored UIFA name must be "recovery" (dotted suffix dropped,
// <=10 chars) rather than "recovery.m" (the old truncation-only behaviour).
func TestClientE2EConfirm(t *testing.T) {
	mac, enc, store, file := bootClientE2ESetup(t)
	// Mode 0 (auto-pick-free) is the default; client_main sets BD_PICK_MODE=0 before
	// calling bdos_pick_record.  Inject 'y' so the confirm gate passes.
	mac.InjectKeys([]byte{'y'})

	res, err := mac.RunBoot("client_main", z80h.Entry{StepCap: bootStepCap})
	if err != nil {
		t.Fatalf("RunBoot client_main: %v", err)
	}
	border, _ := enc.LastBorder()
	t.Logf("client_main (confirm): halted=%v PC=&%04X steps=%d border=%d tx=%d saves=%d",
		res.Halted, res.PC, res.Steps, border, len(enc.TXFrames()), len(store.Saves()))

	// Must reach the success path: green border (4) + halt.
	if !res.Halted || border != 4 {
		t.Fatalf("client_main did not reach write-out success: halted=%v border=%d (want halted, border 4=green); "+
			"5=write-declined 2=cfg 1=init 6=link mean something failed earlier", res.Halted, border)
	}

	saves := store.Saves()
	if len(saves) != 1 {
		t.Fatalf("client_main captured %d HSAVEs, want 1", len(saves))
	}
	s := saves[0]

	// i119 fix: client_main now picks via the picker; first free record is 3 (not 0).
	if s.Record != 3 {
		t.Errorf("write-out selected record %d, want 3 (first free record — the i119 fix)", s.Record)
	}

	// gap-4 extension-drop fix: "recovery.mgt" -> "recovery".
	const wantName = "recovery"
	if s.Name != wantName {
		t.Errorf("write-out name = %q, want %q (dotted suffix dropped by bdos_name_to_uifa, i119e)", s.Name, wantName)
	}

	if s.Size != uint32(len(file)) {
		t.Errorf("write-out size = %d, want %d (bytes received)", s.Size, len(file))
	}

	// Verify the show-name step ran: PICK_DISPLAY must contain "free" (record 3 is free).
	disp := readPickDisplay(t, mac)
	if !strings.Contains(disp, "free") {
		t.Errorf("PICK_DISPLAY %q does not contain \"free\" (show-name did not run)", disp)
	}
}

// TestClientE2EDecline is the i119e SAFETY-INVARIANT test: the real bootable
// client_main, same setup, user presses 'n' — the confirm gate must block the write
// entirely.  The result must be the cyan (border 5) cl_write_declined halt with zero
// HSAVEs captured.  No confirm => no HSAVE reaching Trinity storage.
func TestClientE2EDecline(t *testing.T) {
	mac, enc, store, _ := bootClientE2ESetup(t)
	// Inject 'n': the picker will find record 3, show its name, then the confirm gate
	// consumes 'n' => declined.  client_main must jump to cl_write_declined.
	mac.InjectKeys([]byte{'n'})

	res, err := mac.RunBoot("client_main", z80h.Entry{StepCap: bootStepCap})
	if err != nil {
		t.Fatalf("RunBoot client_main: %v", err)
	}
	border, _ := enc.LastBorder()
	t.Logf("client_main (decline): halted=%v PC=&%04X steps=%d border=%d tx=%d saves=%d",
		res.Halted, res.PC, res.Steps, border, len(enc.TXFrames()), len(store.Saves()))

	// Must reach cl_write_declined: cyan border (5) + halt (graceful, not a crash).
	if !res.Halted || border != 5 {
		t.Fatalf("client_main did not reach cl_write_declined: halted=%v border=%d "+
			"(want halted, border 5=cyan/write-declined); 4=green means write happened despite decline", res.Halted, border)
	}

	// SAFETY INVARIANT: zero HSAVEs — the CONFIRMED gate structurally blocked the write.
	saves := store.Saves()
	if len(saves) != 0 {
		t.Fatalf("client_main captured %d HSAVEs after user declined — SAFETY VIOLATION: "+
			"HSAVE must not reach Trinity storage without CONFIRMED=1", len(saves))
	}
}
