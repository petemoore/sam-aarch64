// netboot_server_faithful_test.go — the i95b-b1 emulation gate: the integrated
// netboot server's RECORD vessel (build/netboot_server_record.mgt, `make
// netboot-server-record`) must boot from a Trinity record on the captured real
// ROM + B-DOS 1.5t and serve the golden Pi 400 session byte-for-byte against
// the Go authorities.
//
// This is the rule-7 gate in front of the i95b-b1 hardware shot, and it proves
// the ONE path the flat harness cannot: the whole bootable come-up — boot_record
// (HRECORD + ALHK) -> the ROM1 load-continuation runs the AUTOnbsrv CODE file ->
// netboot_main reads the SAM identity from the captured EEPROM -> drv_init ->
// the B-DOS STORE WALK (nb_fill_store) reads the booted record's directory via
// real RST-8 HRSAD, HGTHDs + HLOADs each small stand-in file through the real
// 1.5t hooks into FILE_ARENA, and the NBMANIFEST rewrite (nb_apply_manifest,
// i346) maps mangled 10-char store names back to their full TFTP names — then
// the serve loop answers the captured golden frames (DISCOVER->OFFER,
// REQUEST->ACK, ARP, RRQ->OACK->DATA, miss->ERROR(1), non-PXE silence)
// byte-for-byte vs the Go sub-responders.
//
// The walk needs no record number: B-DOS's HRECORD selection persists from
// boot_record's select through the ALHK boot into the running program (the same
// ambient selection the assembler record vessel's post-boot HLOADs use, #813).
//
// Staged assertions so a wedge localises: boot -> walk contents -> arena bytes
// -> each protocol exchange in session order.
//
// Gated on the proprietary captures; skips only under SKIP_PRIVATE_TESTS (i253).
// Emulation-verified is not hardware-verified (CLAUDE.md §5) — the real-SAM
// shot is the i95b-b1 gate itself.
package z80_test

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/dhcp"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/frame"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/golden"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/internal/mask"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/smoke"
	"github.com/petemoore/sam-aarch64/tools/netboot-oracle/tftp"
	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

const (
	serverRecordMGT   = "../../../build/netboot_server_record.mgt"
	standinConfigPath = "../testdata/pi-standins/config.txt"
	standinStart4Path = "../testdata/pi-standins/start4.elf"

	// The i346 long-named stand-ins: both exceed the 10-char B-DOS directory
	// field, so build-disk stores them mangled ("cmdline.tx", "bcm2711-rp")
	// plus an NBMANIFEST map, and the server's walk rewrites its tables to
	// serve them under these full TFTP names.
	standinCmdlinePath = "../testdata/pi-standins/cmdline.txt"
	standinDtbPath     = "../testdata/pi-standins/bcm2711-rpi-400.dtb"
)

// readdressToSAM rebuilds a captured golden unicast frame so it targets the
// SAM identity the server adopted from the real EEPROM (the golden vectors are
// masked to mask.ServerMAC/IP, which the ENC's unicast RX filter would drop).
// The UDP payload — the captured client bytes — is preserved verbatim.
func readdressToSAM(t *testing.T, goldenFrame []byte, samMAC [6]byte, samIP [4]byte) []byte {
	t.Helper()
	u, ok := frame.ParseUDP(goldenFrame)
	if !ok {
		t.Fatal("golden frame did not parse as UDP (test bug)")
	}
	u.DstMAC = frame.MAC(samMAC)
	u.DstIP = frame.IPv4(samIP)
	return frame.BuildUDPFrame(u)
}

// parseWalkStore decodes the Z80 flat STORE the walk filled: repeated
// name\0 + 4-byte LE size entries, terminated by a single leading NUL.
func parseWalkStore(t *testing.T, raw []byte) map[string]int {
	t.Helper()
	out := map[string]int{}
	i := 0
	for i < len(raw) && raw[i] != 0 {
		j := bytes.IndexByte(raw[i:], 0)
		if j < 0 || i+j+5 > len(raw) {
			t.Fatalf("STORE truncated at offset %d: % X", i, raw[i:])
		}
		name := string(raw[i : i+j])
		size := binary.LittleEndian.Uint32(raw[i+j+1 : i+j+5])
		out[name] = int(size)
		i += j + 5
	}
	return out
}

// parseWalkSrcTable decodes NB_SRC_TABLE: repeated name\0 + 2-byte LE arena
// pointer + 4-byte LE size entries, terminated by a single leading NUL.
func parseWalkSrcTable(t *testing.T, raw []byte) map[string]struct {
	ptr  uint16
	size int
} {
	t.Helper()
	out := map[string]struct {
		ptr  uint16
		size int
	}{}
	i := 0
	for i < len(raw) && raw[i] != 0 {
		j := bytes.IndexByte(raw[i:], 0)
		if j < 0 || i+j+7 > len(raw) {
			t.Fatalf("NB_SRC_TABLE truncated at offset %d: % X", i, raw[i:])
		}
		name := string(raw[i : i+j])
		ptr := binary.LittleEndian.Uint16(raw[i+j+1 : i+j+3])
		size := binary.LittleEndian.Uint32(raw[i+j+3 : i+j+7])
		out[name] = struct {
			ptr  uint16
			size int
		}{ptr, int(size)}
		i += j + 7
	}
	return out
}

func TestNetbootServerFaithful(t *testing.T) {
	mac, lg, sd, enc := bootToEditorIdleSDENC(t)

	mgt := mustReadFile(t, serverRecordMGT, "make netboot-server-record")
	configTxt := mustReadFile(t, standinConfigPath, "restore tools/netboot-oracle/testdata/pi-standins")
	start4Elf := mustReadFile(t, standinStart4Path, "restore tools/netboot-oracle/testdata/pi-standins")
	cmdlineTxt := mustReadFile(t, standinCmdlinePath, "restore tools/netboot-oracle/testdata/pi-standins")
	dtbBin := mustReadFile(t, standinDtbPath, "restore tools/netboot-oracle/testdata/pi-standins")
	seedRecordFromMGT(sd, 2, mgt, "nbsrv")
	seedRecordList(sd, map[int]string{1: "rec1", 2: "nbsrv"})

	// Resolve the server's landmarks BEFORE stageBootRecord loads the
	// boot_record map over the symbol table.
	if err := mac.LoadSymbols(serverBootMap); err != nil {
		t.Fatalf("load server symbols: %v — rebuild with `make netboot-server`", err)
	}
	srvSym := func(name string) uint16 {
		a, err := mac.Sym(name)
		if err != nil {
			t.Fatalf("server symbol %q absent from %s — rebuild with `make netboot-server`", name, serverBootMap)
		}
		return a
	}
	serveLoop := srvSym("nb_serve_loop")
	cfgMAC := srvSym("CONFIG_SERVERMAC")
	cfgIP := srvSym("CONFIG_SERVERIP")
	storeAddr := srvSym("STORE")
	tblAddr := srvSym("NB_SRC_TABLE")

	// --- Stage 1: boot the record from the armed pushed-program state (the
	// exact context the real launcher's push creates; see
	// TestBootRecordFromTrinloadIdle). StepCap sizing: the ALHK body load is
	// ~34 sectors through the ROM1 continuation, then the walk reads 40
	// directory sectors and HLOADs the two stand-ins through real B-DOS hooks
	// + the bit-banged SPI model.
	const page = 1
	_, brMain := stageBootRecord(t, mac, 2)
	armServeDispatch(mac, page)
	mac.Pager().HMPR = page

	from := len(*lg)
	res, err := mac.ContinueFrom(brMain, z80h.Entry{
		StepCap: 400_000_000, FrameIntPeriod: 60000,
		StopPC: serveLoop,
	})
	if err != nil {
		t.Fatalf("boot_record -> netboot server faulted: %v (PC=&%04X)", err, res.PC)
	}
	dir, body, out := classifyReads(cmd17Since(lg, from), 2)
	t.Logf("stage 1 boot: finalPC=&%04X reachedStop=%v steps=%d record-2 reads dir=%d body=%d outside=%d",
		res.PC, res.ReachedStop, res.Steps, dir, body, out)
	if !res.ReachedStop {
		t.Fatalf("server did not reach nb_serve_loop (&%04X): finalPC=&%04X halted=%v — the come-up failed (EEPROM read, drv_init, or the store walk)",
			serveLoop, res.PC, res.Halted)
	}
	if body == 0 {
		t.Error("no record-2 BODY sectors read — the vessel cannot have been loaded from the card")
	}

	// --- Stage 2: the walk filled STORE + NB_SRC_TABLE from the record's
	// directory — the small stand-ins indexed with their exact sizes, the
	// oversize files (the AUTOnbsrv binary itself, the boot DOS) excluded.
	// The long-named stand-ins appear under their FULL TFTP names (the i346
	// manifest rewrite), and the NBMANIFEST entry itself is dropped — the
	// exact-count check below asserts both.
	wantStore := map[string]int{
		"config.txt":          len(configTxt),
		"start4.elf":          len(start4Elf),
		"cmdline.txt":         len(cmdlineTxt),
		"bcm2711-rpi-400.dtb": len(dtbBin),
	}
	gotStore := parseWalkStore(t, mac.Read(storeAddr, 1024))
	if len(gotStore) != len(wantStore) {
		t.Fatalf("stage 2: store walk indexed %v, want %v", gotStore, wantStore)
	}
	for name, size := range wantStore {
		if gotStore[name] != size {
			t.Fatalf("stage 2: store walk indexed %s at %d bytes, want %d (all: %v)", name, gotStore[name], size, gotStore)
		}
	}
	// The staged arena bytes must be the files, byte-for-byte from the record
	// through the real HGTHD+HLOAD — the manifest-mapped entries too (their
	// rewritten NB_SRC_TABLE rows must keep the right arena ptr + size).
	tbl := parseWalkSrcTable(t, mac.Read(tblAddr, 512)) // NB_SRC_TABLE_LEN
	for name, want := range map[string][]byte{
		"config.txt":          configTxt,
		"start4.elf":          start4Elf,
		"cmdline.txt":         cmdlineTxt,
		"bcm2711-rpi-400.dtb": dtbBin,
	} {
		e, ok := tbl[name]
		if !ok {
			t.Fatalf("stage 2: %s absent from NB_SRC_TABLE (%v)", name, tbl)
		}
		if e.size != len(want) {
			t.Fatalf("stage 2: NB_SRC_TABLE size for %s = %d, want %d", name, e.size, len(want))
		}
		if got := mac.Read(e.ptr, e.size); !bytes.Equal(got, want) {
			diff := 0
			for diff < len(want) && got[diff] == want[diff] {
				diff++
			}
			t.Fatalf("stage 2: arena bytes for %s differ at offset %#x (got &%02X want &%02X) — HLOAD did not deliver the file",
				name, diff, got[diff], want[diff])
		}
	}
	t.Logf("stage 2 walk: STORE=%v (arena bytes verified)", gotStore)

	// --- The Go authorities, built from the identity the server adopted from
	// the captured EEPROM + netboot_main's fixed pool derivation (subnet /24,
	// broadcast .255, pool .100 x8, lease 7200/3600/6300, TID 40136). The
	// byte-for-byte compares below therefore also verify the derivation.
	var samMAC [6]byte
	var samIP [4]byte
	copy(samMAC[:], mac.Read(cfgMAC, 6))
	copy(samIP[:], mac.Read(cfgIP, 4))
	t.Logf("server identity (from the captured EEPROM): MAC=% X IP=%d.%d.%d.%d", samMAC[:], samIP[0], samIP[1], samIP[2], samIP[3])
	subnet := [4]byte{255, 255, 255, 0}
	broadcast := [4]byte{samIP[0], samIP[1], samIP[2], 255}
	poolBase := [4]byte{samIP[0], samIP[1], samIP[2], 100}
	refDHCP := dhcp.NewResponder(samMAC, samIP, subnet, broadcast, poolBase, 8, 7200, 3600, 6300)
	refARP := smoke.NewResponder(samMAC, samIP)
	goStore := tftp.MapStore{
		"config.txt":          uint64(len(configTxt)),
		"start4.elf":          uint64(len(start4Elf)),
		"cmdline.txt":         uint64(len(cmdlineTxt)),
		"bcm2711-rpi-400.dtb": uint64(len(dtbBin)),
	}
	refTFTP := tftp.NewServerLoop(goStore, samMAC, samIP, nbServerTID)

	// drive injects req (nil = idle), resumes the serve loop, and returns the
	// single frame the server transmitted (nil for silence). Continue, NOT
	// ContinueFrom: a drive's StepCap can end mid-interrupt-handler, and
	// ContinueFrom pushes the halt-trap return address onto the LIVE stack —
	// inserting a word under a half-unwound handler frame, so its pops/RET
	// come off one slot wrong and control lands on the halt trap (the wedge
	// this test's first draft chased into the server). Continue resumes with
	// the machine state untouched.
	drive := func(label string, req []byte) []byte {
		t.Helper()
		txBefore := len(enc.TXFrames())
		if req != nil {
			enc.InjectRX(req)
		}
		r, err := mac.Continue(z80h.Entry{StepCap: 8_000_000, FrameIntPeriod: 60000})
		if err != nil {
			t.Fatalf("%s: serve loop faulted: %v (PC=&%04X)", label, err, r.PC)
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
			t.Errorf("%s != Go authority\n  z80 %x\n  go  %x", label, got, want)
		}
	}

	// --- Stage 3: golden DHCP DISCOVER -> OFFER, byte-for-byte (broadcast —
	// no readdressing needed).
	eq("OFFER", drive("OFFER", golden.DHCPDiscover), refDHCP.OnRequest(golden.DHCPDiscover))
	t.Logf("stage 3: DISCOVER -> OFFER matches the Go authority")

	// --- Stage 4: a non-PXE DISCOVER/REQUEST is ignored — the stage-1
	// vendor-class filter, now under the real ROM. (The Go responder ignores
	// these too, so no reference call: the pools stay in lockstep.)
	for _, tc := range nonPXEDHCPVariants(t) {
		if r := drive(tc.name, tc.req); r != nil {
			t.Errorf("stage 4 %s: server replied (%d bytes), want silence", tc.name, len(r))
		}
	}
	t.Logf("stage 4: non-PXE DHCP ignored")

	// --- Stage 5: golden DHCP REQUEST -> ACK (same lease as the OFFER).
	eq("DHCP ACK", drive("DHCP ACK", golden.DHCPRequest), refDHCP.OnRequest(golden.DHCPRequest))
	t.Logf("stage 5: REQUEST -> ACK matches the Go authority")

	// --- Stage 6: ARP who-has the SAM's IP -> reply.
	arpReq := frame.BuildARPRequest(frame.MAC(mask.ClientMAC), frame.IPv4(mask.ClientIP), frame.IPv4(samIP))
	eq("ARP reply", drive("ARP", arpReq), refARP.OnFrame(arpReq))
	t.Logf("stage 6: ARP answered")

	// --- Stage 7: the captured golden RRQ for config.txt (tsize 0 + blksize
	// 1024), readdressed to the SAM identity -> OACK, transfer armed.
	rrqConfig := readdressToSAM(t, golden.TFTPRrqRoot1024, samMAC, samIP)
	u, _ := frame.ParseUDP(rrqConfig)
	configTID := u.SrcPort
	refTFTP.SetSource(tftp.ByteSource(configTxt))
	eq("OACK", drive("OACK", rrqConfig), refTFTP.OnRRQ(rrqConfig))
	t.Logf("stage 7: RRQ config.txt -> OACK matches the Go authority")

	ackFrom := func(tid uint16, block uint16) []byte {
		return frame.BuildUDPFrame(frame.UDP{
			DstMAC: frame.MAC(samMAC), SrcMAC: frame.MAC(mask.ClientMAC),
			SrcIP: frame.IPv4(mask.ClientIP), DstIP: frame.IPv4(samIP),
			SrcPort: tid, DstPort: nbServerTID,
			Payload: tftp.BuildACK(block),
		})
	}

	// --- Stage 8: ACK 0 -> DATA 1 (a full 1024-byte block).
	eq("DATA 1", drive("DATA 1", ackFrom(configTID, 0)), refTFTP.FirstData())

	// --- Stage 9: ACK 1 -> DATA 2 (the short final block = the file's tail).
	d2 := drive("DATA 2", ackFrom(configTID, 1))
	eq("DATA 2", d2, refTFTP.OnACK(ackFrom(configTID, 1)))
	if _, p2, _ := tftp.ParseDATA(udpPayload(t, d2)); !bytes.Equal(p2, configTxt[1024:]) {
		t.Errorf("stage 9: final block payload (%d bytes) != config.txt tail (%d bytes)", len(p2), len(configTxt)-1024)
	}

	// --- Stage 10: the ACK of the short final block ends the transfer.
	if fin := drive("final ACK", ackFrom(configTID, 2)); fin != nil {
		t.Errorf("stage 10: ACK of the final block should end the transfer, got %x", fin)
	}
	_ = refTFTP.OnACK(ackFrom(configTID, 2)) // keep the reference in lockstep
	t.Logf("stages 8-10: config.txt streamed (1024 + %d) and completed", len(configTxt)-1024)

	// --- Stage 11: a second transfer — RRQ start4.elf (single short block)
	// from a fresh client TID.
	const start4TID = 30575
	rrqStart4 := frame.BuildUDPFrame(frame.UDP{
		DstMAC: frame.MAC(samMAC), SrcMAC: frame.MAC(mask.ClientMAC),
		SrcIP: frame.IPv4(mask.ClientIP), DstIP: frame.IPv4(samIP),
		SrcPort: start4TID, DstPort: 69,
		Payload: tftp.BuildRRQ("start4.elf", "octet", []tftp.Option{{Name: "tsize", Value: "0"}, {Name: "blksize", Value: "1024"}}),
	})
	refTFTP.SetSource(tftp.ByteSource(start4Elf))
	eq("OACK (start4.elf)", drive("OACK start4", rrqStart4), refTFTP.OnRRQ(rrqStart4))
	d1 := drive("DATA 1 start4", ackFrom(start4TID, 0))
	eq("DATA 1 (start4.elf)", d1, refTFTP.FirstData())
	if _, p1, _ := tftp.ParseDATA(udpPayload(t, d1)); !bytes.Equal(p1, start4Elf) {
		t.Errorf("stage 11: start4.elf payload (%d bytes) != fixture (%d bytes)", len(p1), len(start4Elf))
	}
	if fin := drive("final ACK start4", ackFrom(start4TID, 1)); fin != nil {
		t.Errorf("stage 11: ACK of start4.elf's single block should end the transfer, got %x", fin)
	}
	_ = refTFTP.OnACK(ackFrom(start4TID, 1))
	t.Logf("stage 11: start4.elf served in one short block")

	// rrqFor builds an optioned RRQ (tsize 0 + blksize 1024, the captured
	// shape) for name from a fresh client TID.
	rrqFor := func(name string, tid uint16) []byte {
		return frame.BuildUDPFrame(frame.UDP{
			DstMAC: frame.MAC(samMAC), SrcMAC: frame.MAC(mask.ClientMAC),
			SrcIP: frame.IPv4(mask.ClientIP), DstIP: frame.IPv4(samIP),
			SrcPort: tid, DstPort: 69,
			Payload: tftp.BuildRRQ(name, "octet", []tftp.Option{{Name: "tsize", Value: "0"}, {Name: "blksize", Value: "1024"}}),
		})
	}

	// --- Stage 11b (i346): cmdline.txt — a manifest-mapped name (stored as
	// "cmdline.tx" on the record) served under its full 11-char TFTP name,
	// one short block, byte-verified.
	const cmdlineTID = 30576
	refTFTP.SetSource(tftp.ByteSource(cmdlineTxt))
	eq("OACK (cmdline.txt)", drive("OACK cmdline", rrqFor("cmdline.txt", cmdlineTID)), refTFTP.OnRRQ(rrqFor("cmdline.txt", cmdlineTID)))
	dc := drive("DATA 1 cmdline", ackFrom(cmdlineTID, 0))
	eq("DATA 1 (cmdline.txt)", dc, refTFTP.FirstData())
	if _, p, _ := tftp.ParseDATA(udpPayload(t, dc)); !bytes.Equal(p, cmdlineTxt) {
		t.Errorf("stage 11b: cmdline.txt payload (%d bytes) != fixture (%d bytes)", len(p), len(cmdlineTxt))
	}
	if fin := drive("final ACK cmdline", ackFrom(cmdlineTID, 1)); fin != nil {
		t.Errorf("stage 11b: ACK of cmdline.txt's single block should end the transfer, got %x", fin)
	}
	_ = refTFTP.OnACK(ackFrom(cmdlineTID, 1))
	t.Logf("stage 11b: cmdline.txt served under its full manifest-mapped name")

	// --- Stage 11c (i346): bcm2711-rpi-400.dtb — the 19-char mapped name
	// (stored as "bcm2711-rp"), streamed as a full 1024-byte block plus the
	// short tail, both byte-verified.
	const dtbTID = 30577
	refTFTP.SetSource(tftp.ByteSource(dtbBin))
	eq("OACK (bcm2711-rpi-400.dtb)", drive("OACK dtb", rrqFor("bcm2711-rpi-400.dtb", dtbTID)), refTFTP.OnRRQ(rrqFor("bcm2711-rpi-400.dtb", dtbTID)))
	dd1 := drive("DATA 1 dtb", ackFrom(dtbTID, 0))
	eq("DATA 1 (bcm2711-rpi-400.dtb)", dd1, refTFTP.FirstData())
	if _, p, _ := tftp.ParseDATA(udpPayload(t, dd1)); !bytes.Equal(p, dtbBin[:1024]) {
		t.Errorf("stage 11c: dtb block 1 (%d bytes) != fixture head", len(p))
	}
	dd2 := drive("DATA 2 dtb", ackFrom(dtbTID, 1))
	eq("DATA 2 (bcm2711-rpi-400.dtb)", dd2, refTFTP.OnACK(ackFrom(dtbTID, 1)))
	if _, p, _ := tftp.ParseDATA(udpPayload(t, dd2)); !bytes.Equal(p, dtbBin[1024:]) {
		t.Errorf("stage 11c: dtb final block (%d bytes) != fixture tail (%d bytes)", len(p), len(dtbBin)-1024)
	}
	if fin := drive("final ACK dtb", ackFrom(dtbTID, 2)); fin != nil {
		t.Errorf("stage 11c: ACK of the dtb's final block should end the transfer, got %x", fin)
	}
	_ = refTFTP.OnACK(ackFrom(dtbTID, 2))
	t.Logf("stage 11c: bcm2711-rpi-400.dtb streamed (1024 + %d) under its mapped name", len(dtbBin)-1024)

	// --- Stage 11d (i346): the rewrite replaced (not aliased) the mangled
	// names, and dropped the manifest from the store — the on-record store
	// name and NBMANIFEST itself both ERROR(1), byte-matching the authority
	// (whose store never held either).
	for i, name := range []string{"cmdline.tx", "NBMANIFEST"} {
		tid := uint16(30580 + i)
		reply := drive("miss "+name, rrqFor(name, tid))
		eq("ERROR(1) for "+name, reply, refTFTP.OnRRQ(rrqFor(name, tid)))
		if code, _, err := tftp.ParseError(udpPayload(t, reply)); err != nil || code != tftp.ErrFileNotFound {
			t.Errorf("stage 11d: RRQ %s -> code %d (err %v), want 1 (file not found)", name, code, err)
		}
	}
	t.Logf("stage 11d: the mangled store name and NBMANIFEST are not servable")

	// --- Stage 12: a miss — the captured golden RRQ for recovery.elf (not in
	// the record) -> ERROR(1, File not found), and the server keeps serving.
	rrqMiss := readdressToSAM(t, golden.TFTPRrqRoot1468, samMAC, samIP)
	miss := drive("miss", rrqMiss)
	eq("ERROR(1)", miss, refTFTP.OnRRQ(rrqMiss))
	if code, _, err := tftp.ParseError(udpPayload(t, miss)); err != nil || code != tftp.ErrFileNotFound {
		t.Errorf("stage 12: miss reply code = %d (err %v), want 1 (file not found)", code, err)
	}
	// Keep-serving: an ARP probe is still answered after the miss.
	eq("ARP after miss", drive("ARP after miss", arpReq), refARP.OnFrame(arpReq))
	t.Logf("stage 12: miss -> ERROR(1), server still serving — the record vessel boots and serves the golden session")
}
