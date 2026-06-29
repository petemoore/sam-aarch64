package z80_test

import (
	"fmt"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

// WIP capture rig (i280b-b2q, §8q): drive real BASIC `RECORD n` / `SAVE` / `FORMAT`
// at the prompt (InjectKeys + FrameIntPeriod) against Colin's real ROM + B-DOS, and
// decode the live &DC-&DF SD command frames — the harness for Pete's capture-and-diff
// plan (capture a WORKING record write, diff our HWSAD path against it). It currently
// only LOGS (the assertions await a valid Trinity card format — see §8q): a working
// SAVE needs the card-level record-list/directory at block 152, derivable from
// bdos15a hd.init + the FORMAT command. Once that lands, this captures the ground-truth
// write. Not yet a regression gate — it documents the empirical findings in §8q.

type capIO struct {
	inner  *z80h.ENC28J60
	lastPC *uint16
	log    *[]capEv
}
type capEv struct {
	pc    uint16
	write bool
	port  uint8
	val   uint8
}

func (l *capIO) In(port uint8) uint8 {
	v := l.inner.In(port)
	if port >= 0xDC && port <= 0xDF {
		*l.log = append(*l.log, capEv{*l.lastPC, false, port, v})
	}
	return v
}
func (l *capIO) Out(port uint8, value uint8) {
	if port >= 0xDC && port <= 0xDF {
		*l.log = append(*l.log, capEv{*l.lastPC, true, port, value})
	}
	l.inner.Out(port, value)
}
func (l *capIO) SetTState(t uint64) { l.inner.SetTState(t) }

func TestBDOSSaveCaptureWIP(t *testing.T) {
	rom, eeprom := loadRealCaptures(t)
	mac := z80h.New()
	if err := mac.LoadROMImage(rom); err != nil {
		t.Fatalf("load ROM: %v", err)
	}
	mac.Pager().LMPR = bootLMPR
	mac.Pager().HMPR = bootHMPR
	enc := z80h.NewENC28J60()
	enc.LoadEEPROMImage(deviceLinearEEPROM(eeprom))
	sd := enc.AttachSD(csdV2(0x001D59))
	// Make a higher-numbered record a valid B-DOS record (low records map to floppy
	// drives — Pete). base=152 (empirical), record n at base+(n-1)*1600. Stamp "BDOS"
	// at bytes 232-235 of its first sector (the i62 selection gate). Seed a few
	// candidate records so whichever the RECORD command reads is valid.
	stamp := func(block uint32) {
		s := make([]byte, 512)
		copy(s[232:236], []byte("BDOS"))
		sd.SeedSector(block, s)
	}
	const recBase = 152
	for _, n := range []uint32{1, 2, 3, 4, 5, 10, 300} {
		stamp(recBase + (n-1)*1600)
	}
	// Verify the seed is served by the model's backing store (block 152 = record 1 base).
	if got, ok := sd.CapturedSector(152); ok {
		t.Logf("seed check: block 152 bytes[232:236] = %q (ok=%v)", string(got[232:236]), ok)
	} else {
		t.Logf("seed check: block 152 NOT in backing store (ok=false) — SeedSector did not take")
	}
	var lastPC uint16
	var log []capEv
	mac.AttachIO(&capIO{inner: enc, lastPC: &lastPC, log: &log})

	res, err := mac.RunBootFrom(0x0000, z80h.Entry{
		StepCap: realBootStepCap, StopPC: addrEditorIdle, StopPCSkip: 16,
		Trace: func(pc uint16) { lastPC = pc },
	})
	if err != nil || !res.ReachedStop {
		t.Fatalf("boot failed: %v reached=%v PC=&%04X", err, res.ReachedStop, res.PC)
	}
	t.Logf("booted to editor idle: LMPR=&%02X HMPR=&%02X", mac.Pager().LMPR, mac.Pager().HMPR)

	// Type a direct command and run the editor until it drains the keys + returns to idle.
	typeLine := func(label, line string, capPorts bool) {
		logStart := len(log)
		mac.InjectKeys(append([]byte(line), 0x0D))
		hooks := []string{}
		seen := map[uint16]bool{}
		landmarks := map[uint16]string{
			0x0008: "RST8", 0x4319: "DISPATCH", 0x5D54: "HSAVE", 0x47CB: "open.file",
			0x4889: "HSVBK", 0x49C2: "HCFSM", 0x43ED: "wr.buff", 0x68F4: "SDwrite",
			0x6925: "CMD24", 0x4662: "devsel", 0x5E16: "HWSAD", 0x4406: "FDCpoll",
		}
		_, _ = mac.RunBootFrom(addrEditorIdle, z80h.Entry{
			StepCap:        30_000_000,
			FrameIntPeriod: 60000,
			Trace: func(pc uint16) {
				lastPC = pc
				if n, ok := landmarks[pc]; ok && !seen[pc] {
					seen[pc] = true
					hooks = append(hooks, n)
				}
			},
		})
		errnr := mac.Read(0x5C3A, 1)[0] // ERRNR: &FF = OK, else error number-1
		t.Logf("== %s (%q) ==", label, line)
		t.Logf("   landmarks hit: %v   pendingKeys=%d   ERRNR=&%02X   dev(&8132)=&%02X cls(&8135)=&%02X amb(&780B via pageB)",
			hooks, mac.PendingKeys(), errnr, mac.Read(0x8132, 1)[0], mac.Read(0x8135, 1)[0])
		if capPorts {
			t.Logf("   port events (%d):", len(log)-logStart)
			for i := logStart; i < len(log); i++ {
				e := log[i]
				dir := "IN "
				if e.write {
					dir = "OUT"
				}
				fmt.Printf("    [%3d] PC=&%04X %s (&%02X) = &%02X\n", i-logStart, e.pc, dir, e.port, e.val)
			}
		}
	}

	// Stage the CODE to be saved (20 bytes at 32768 = &8000... section C). Use a low addr
	// in a RAM page that exists; &8000 is section C. Stage via the pager RAM directly.
	for i := 0; i < 20; i++ {
		mac.Write(0x8000+uint16(i), []byte{byte(0xA0 + i)})
	}

	decodeCmds := func(label string, from int) {
		fmt.Printf("=== decoded SD command frames during %s ===\n", label)
		var dfOut []capEv
		for i := from; i < len(log); i++ {
			if log[i].write && log[i].port == 0xDF {
				dfOut = append(dfOut, log[i])
			}
		}
		for i := 0; i < len(dfOut); i++ {
			v := dfOut[i].val
			if v&0xC0 == 0x40 && i+4 < len(dfOut) {
				addr := uint32(dfOut[i+1].val)<<24 | uint32(dfOut[i+2].val)<<16 |
					uint32(dfOut[i+3].val)<<8 | uint32(dfOut[i+4].val)
				cmd := v & 0x3F
				tag := ""
				if cmd == 24 {
					tag = "  <<< CMD24 WRITE"
				}
				fmt.Printf("  CMD%-2d addr=%d (0x%X) @PC=&%04X%s\n", cmd, addr, addr, dfOut[i].pc, tag)
				i += 4
			}
		}
	}

	// Pete's hint: make B-DOS detect/init the seeded card (load its record structure
	// into memory) with DEVICE before selecting a record.
	dStart := len(log)
	typeLine("DEVICE", "DEVICE", false)
	decodeCmds("DEVICE", dStart)

	r1Start := len(log)
	typeLine("RECORD 1", "RECORD 1", false)
	decodeCmds("RECORD 1", r1Start)

	saveStart := len(log)
	typeLine("SAVE", `SAVE "test"CODE 32768,20`, false)
	decodeCmds("SAVE", saveStart)
}
