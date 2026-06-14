// bdos_store_test.go — i134: run the netboot client's B-DOS write-out path in
// emulation. Until now bdos_select_record / bdos_save_hook (the HRECORD record
// select + the HSAVE) were behind `ifndef NETBOOT_HOSTTEST` and never ran under
// any harness — the write-out shipped to hardware uncaught (CLAUDE.md rule 7).
// With the RST 8 hook modelled (bdos_store.go + harness.go rstHandlers) the real
// hook bodies run host-side and the captured record + UIFA are asserted.
//
// This is the prerequisite for emulation-testing i119 (the record-selection fix):
// the end-to-end test below drives the real bootable client_main to its write-out
// and shows it selects record 0 = the floppy — the i119 bug, now visible in
// emulation rather than only on hardware. Emulation-verified is not hardware-
// verified (CLAUDE.md §5): this models the digital hook dispatch, not a real
// persist.
package z80_test

import (
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

	// Stage a filename (<=10 chars, the SAM UIFA name field, so it round-trips
	// without truncation) and the save parameters, build the UIFA, then HSAVE it.
	const unitName = "save.bin"
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

// TestClientBootWriteOutCaptured is the i134 end-to-end: the REAL bootable
// client_main, fed a complete (minimal) TFTP transfer, runs through to its B-DOS
// write-out — which now executes in emulation and is captured. It proves the
// whole boot client (EEPROM read, drv_init, link-up wait, ARP, fetch loop, AND
// the previously-unemulated HRECORD+HSAVE write-out) runs host-side, and it
// surfaces the i119 record-selection bug: client_main selects record 0 (=floppy),
// not a Trinity record n>=1.
func TestClientBootWriteOutCaptured(t *testing.T) {
	mac, err := z80h.LoadBoot(cliBootBin, cliBootMap, romBaseBoot)
	if err != nil {
		t.Skipf("client boot binary not built (%v); run `make netboot-client-boot`", err)
	}
	enc := z80h.NewENC28J60()
	enc.ProgramTrinityNetwork(mask.ServerMAC, mask.ServerIP) // the SAM's own MAC+IP
	store := z80h.NewBDOSStore()
	mac.AttachIO(enc)
	mac.AttachBDOS(store)

	// The TFTP server client_main resolves (cl_server_ip = 192.168.0.1) and a small
	// file it serves as a single short final DATA block (no OACK → 512 default; a
	// sub-512 block 1 is the final block → XFER_DONE after one block).
	const bootSrvTID = 40000
	bootSrvMAC := frame.MAC{0x02, 0xAA, 0xBB, 0xCC, 0xDD, 0xEE}
	file := makeFile(200)

	// Pre-queue the frames the client consumes in order: the ARP reply (→ RRQ),
	// then DATA block 1 (→ ACK 1 + done). The emulated ENC delivers one per
	// EPKTCNT poll, so client_main's fetch loop plays them out across iterations.
	enc.InjectRX(frame.BuildARPReply(bootSrvMAC, mask.ServerMAC, cliBootServerIP, frame.IPv4(mask.ServerIP)))
	enc.InjectRX(frame.BuildUDPFrame(frame.UDP{
		DstMAC: frame.MAC(mask.ServerMAC), SrcMAC: bootSrvMAC,
		SrcIP: cliBootServerIP, DstIP: frame.IPv4(mask.ServerIP),
		SrcPort: bootSrvTID, DstPort: cliOwnTID,
		Payload: tftp.BuildDATA(1, file),
	}))

	res, err := mac.RunBoot("client_main", z80h.Entry{StepCap: bootStepCap})
	if err != nil {
		t.Fatalf("RunBoot client_main: %v", err)
	}
	border, _ := enc.LastBorder()
	t.Logf("client_main: halted=%v PC=&%04X steps=%d border=%d tx=%d selected=%d saves=%d",
		res.Halted, res.PC, res.Steps, border, len(enc.TXFrames()), store.Selected(), len(store.Saves()))

	// It must reach the success path: green border (4) + halt, having written out.
	if !res.Halted || border != 4 {
		t.Fatalf("client_main did not reach the success write-out: halted=%v border=%d (want halted, border 4=green); "+
			"2=cfg 1=init 6=link mean it failed earlier", res.Halted, border)
	}
	saves := store.Saves()
	if len(saves) != 1 {
		t.Fatalf("client_main's write-out captured %d HSAVEs, want 1", len(saves))
	}
	s := saves[0]
	// The SAM UIFA name field is 10 bytes, so the 12-char cl_filename is stored
	// truncated — faithful SAM DOS behaviour, surfaced here for i119 (the RRQ
	// fetch name and the on-SAM stored name differ when the name exceeds 10 chars).
	wantName := cliFilename
	if len(wantName) > 10 {
		wantName = wantName[:10]
	}
	if s.Name != wantName {
		t.Errorf("write-out filename = %q, want %q (the 10-char SAM name field)", s.Name, wantName)
	}
	if s.Size != uint32(len(file)) {
		t.Errorf("write-out size = %d, want %d (the bytes received)", s.Size, len(file))
	}
	// The i119 bug, now visible in emulation: client_main does `xor a;
	// call bdos_select_record`, selecting record 0 = the floppy, not a Trinity
	// record n>=1. i119 changes this; when it does, flip this assertion to n>=1.
	if s.Record != 0 {
		t.Errorf("write-out selected record %d; the current (pre-i119) client_main selects 0 (=floppy). "+
			"If this changed, the i119 fix landed — update this assertion to the new record", s.Record)
	}
}
