package z80_test

import (
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

// §8r record-device mount/select diagnosis (i280b-b2r) — regression guards for the
// facts that re-aim Pete's §8q capture-and-diff plan. Var map (ANALYSIS.md §3,
// confirmed vs bdos15t-beta6.annotated.dis): last.record=&80C4 (mounted-record count
// sel.record range-checks against), base=&80C2, capacity=&80BD, record.no=&80C6,
// hd.wp=&80C8. last.record is in B-DOS's resident page (page 29 = section C at editor
// idle), so reads/writes page it into section C first.

// bootMountTestMachine boots Colin's real ROM v3.0 + B-DOS 1.5t with a Trinity SD card
// (csdV2(0x001D59): base=152, records=4809) whose record 1 is "BDOS"-stamped, returns
// the machine + the captured &DC-&DF command log and a CMD-frame reader.
func bootMountTestMachine(t *testing.T) (mac *z80h.Machine, log *[]capEv, dosPage uint8) {
	t.Helper()
	rom, eeprom := loadRealCaptures(t)
	mac = z80h.New()
	if err := mac.LoadROMImage(rom); err != nil {
		t.Fatalf("load ROM: %v", err)
	}
	mac.Pager().LMPR = bootLMPR
	mac.Pager().HMPR = bootHMPR
	enc := z80h.NewENC28J60()
	enc.LoadEEPROMImage(deviceLinearEEPROM(eeprom))
	sd := enc.AttachSD(csdV2(0x001D59))
	rec1 := make([]byte, 512)
	copy(rec1[210:220], []byte("TRINITY1  "))
	copy(rec1[232:236], []byte("BDOS")) // the i62 record-1 selection stamp
	sd.SeedSector(152, rec1)

	var lastPC uint16
	lg := &[]capEv{}
	mac.AttachIO(&capIO{inner: enc, lastPC: &lastPC, log: lg})
	res, err := mac.RunBootFrom(0x0000, z80h.Entry{
		StepCap: realBootStepCap, StopPC: addrEditorIdle, StopPCSkip: 16,
		Trace: func(pc uint16) { lastPC = pc },
	})
	if err != nil || !res.ReachedStop {
		t.Fatalf("boot failed: %v reached=%v PC=&%04X", err, res.ReachedStop, res.PC)
	}
	return mac, lg, mac.Pager().HMPR & 0x1F
}

// rdDOS16 reads a 16-bit B-DOS var, paging B-DOS's page into section C first.
func rdDOS16(mac *z80h.Machine, dosPage uint8, addr uint16) uint16 {
	saved := mac.Pager().HMPR
	mac.Pager().HMPR = dosPage
	b := mac.Read(addr, 2)
	mac.Pager().HMPR = saved
	return uint16(b[0]) | uint16(b[1])<<8
}

// runCmdLine injects a BASIC line + Enter and runs the editor until it drains, then
// returns (sawCMD0, the list of CMD17 block addresses) issued during the line.
func runCmdLine(mac *z80h.Machine, log *[]capEv, line string) (bool, []uint32) {
	from := len(*log)
	mac.InjectKeys(append([]byte(line), 0x0D))
	_, _ = mac.RunBootFrom(addrEditorIdle, z80h.Entry{
		StepCap: 30_000_000, FrameIntPeriod: 60000,
	})
	var dfOut []capEv
	for i := from; i < len(*log); i++ {
		if (*log)[i].write && (*log)[i].port == 0xDF {
			dfOut = append(dfOut, (*log)[i])
		}
	}
	var sawCMD0 bool
	var cmd17 []uint32
	for i := 0; i < len(dfOut); i++ {
		v := dfOut[i].val
		if v&0xC0 == 0x40 && i+4 < len(dfOut) {
			addr := uint32(dfOut[i+1].val)<<24 | uint32(dfOut[i+2].val)<<16 |
				uint32(dfOut[i+3].val)<<8 | uint32(dfOut[i+4].val)
			switch v & 0x3F {
			case 0:
				sawCMD0 = true
			case 17:
				cmd17 = append(cmd17, addr)
			}
			i += 4
		}
	}
	return sawCMD0, cmd17
}

func containsU32(s []uint32, v uint32) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// TestBDOSBootNoMountDeviceMounts asserts the two clean §8r mount facts:
//  1. The boot path leaves the SD record device UNMOUNTED — last.record (&80C4) = 0.
//  2. `DEVICE` re-runs HDINIT (&A1B1), which CMD17-reads block 152 (the record
//     directory) and computes last.record = 4809 directly from the card CSD (CMD9) —
//     NOT from any on-card boot sector / record-list. So the §8q premise that a full
//     card-level format is needed for the MOUNT is unnecessary; the count is
//     CSD-derived and `DEVICE` is the mount trigger.
func TestBDOSBootNoMountDeviceMounts(t *testing.T) {
	mac, log, dosPage := bootMountTestMachine(t)

	if got := rdDOS16(mac, dosPage, 0x80C4); got != 0 {
		t.Errorf("boot mounted unexpectedly: last.record=%d, want 0 (boot must not run HDINIT)", got)
	}
	_, cmd17 := runCmdLine(mac, log, "DEVICE")
	if got := rdDOS16(mac, dosPage, 0x80C4); got != 4809 {
		t.Errorf("DEVICE: last.record=%d, want 4809 (CSD-derived record count)", got)
	}
	if !containsU32(cmd17, 152) {
		t.Errorf("DEVICE: no CMD17 read of block 152 (the record directory); reads=%v", cmd17)
	}
	t.Logf("§8r facts 1+2 OK: boot leaves last.record=0; DEVICE mounts from CSD (last.record=4809, CMD17 block 152)")
}

// TestBDOSRecordSelectSelfHeals asserts §8r fact 3: with the full mount var-set poked
// in (range-checks satisfied), `RECORD 1` runs the faithful self-heal SD init ladder
// (CMD0..CMD16) + the CMD17 read of block 152 (the record directory) and the select
// PERSISTS (last.record stays 4809). So the record select itself reaches the card and
// holds — confirming the §8m read/write asymmetry (every record op re-inits the bus)
// and that our poked mount-state is sufficient for selection. The remaining write
// blocker is DOWNSTREAM, at the SAVE/HSAVE step (which resets last.record and issues
// no CMD24 — the i280b-b2q continuation), not the directory read or the record select.
// Fresh boot so a prior DEVICE does not poison the device. §8r.
func TestBDOSRecordSelectSelfHeals(t *testing.T) {
	mac, log, dosPage := bootMountTestMachine(t)

	// Poke the full SD mount var-set into B-DOS's page so the range-checks pass.
	var blocks uint32 = 7694336
	pk := func(a uint16, b []byte) {
		saved := mac.Pager().HMPR
		mac.Pager().HMPR = dosPage
		mac.Write(a, b)
		mac.Pager().HMPR = saved
	}
	pk16 := func(a uint16, v uint16) { pk(a, []byte{byte(v), byte(v >> 8)}) }
	pk(0x80BD, []byte{byte(blocks), byte(blocks >> 8), byte(blocks >> 16), byte(blocks >> 24)})
	pk16(0x80C2, 152)
	pk16(0x80C4, 4809)
	pk16(0x80C6, 1)
	pk(0x80C8, []byte{0x00})

	sawCMD0, cmd17 := runCmdLine(mac, log, "RECORD 1")
	if !sawCMD0 {
		t.Errorf("RECORD 1: no CMD0 — the self-heal init ladder did not run (expected faithful re-init)")
	}
	if !containsU32(cmd17, 152) {
		t.Errorf("RECORD 1: no CMD17 read of block 152 (the directory); reads=%v", cmd17)
	}
	if got := rdDOS16(mac, dosPage, 0x80C4); got != 4809 {
		t.Errorf("RECORD 1: select did not persist: last.record=%d, want 4809", got)
	}
	t.Logf("§8r fact 3 OK: RECORD 1 self-heals the bus (init ladder) + CMD17-reads block 152 and the select persists (last.record=4809); the write blocker is downstream at SAVE/HSAVE")
}
