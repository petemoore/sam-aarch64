// Command bdostrace-paged is the PAGED-BOOT counterpart of cmd/bdostrace. It
// boots Colin Piggot's REAL captured patched ROM + Trinity EEPROM to a B-DOS-
// resident editor idle (the genuine paged environment: ROM0 at &0000, B-DOS in
// its real &8000 page, sysvars at &5xxx), then drives the real B-DOS Trinity SD
// record-write machinery to capture the "gold entry contract" a successful HWSAD
// write needs — the input the i280b fix to src/netboot/bdos_seam.asm consumes.
//
// Why a SEPARATE tool from cmd/bdostrace (i280b vs i280a). The flat section-B
// bdostrace can run the §8 SD-write CORE (which is self-contained), but it cannot
// run the HWSAD/HRSAD hook ENTRY PRELUDE: the prelude calls SAM-ROM/B-DOS bridge
// helpers (&0005 jp(hl), &0033, &0103) and reads sysvars the flat 64 KB model
// lacks, so the flat run escapes the window at real &9BF1 -> call &0103 (see
// trinity-sd-z80-interface.md §8a). Faithfully tracing the prelude needs the real
// paged boot — which is exactly what newRealBootMachine (samboot_real_boot_test.go)
// provides. Those helpers live in package z80_test (a _test.go file) and cannot be
// imported from a cmd/ main, so the small amount of boot setup is replicated here.
//
// What this tool establishes (the gold contract). The B-DOS Trinity SD device
// dispatch is gated on the ambient-device var &780B, set by HRECORD-select. A
// successful record write requires the device class+number HRECORD arms; with the
// wrong ambient device the SAME dispatch falls into an un-timed FDC poll = the
// hardware hang. The read (HRSAD) and write (HWSAD) paths share that gate, so the
// hypothesised wr.buff-vs-rd.buff asymmetry is REFUTED — see the report + §8b.
//
// The ROM/EEPROM are Colin's PROPRIETARY captures: referenced by their
// ~/sam-archive path (or $SAMBOOT_CAPTURE_DIR), never copied into the repo. Absent
// -> the tool errors out (it is a cmd tool, not a test; no SKIP_PRIVATE_TESTS).
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

// ---------------------------------------------------------------------------
// Capture loading (replicated from samboot_real_boot_test.go — that helper is in
// package z80_test and cannot be imported here).
// ---------------------------------------------------------------------------

const (
	realROMBytes  = 32768
	realEEPROMMin = 0x2400

	addrEditorIdle  = 0x01CB // ROM editor keyboard-wait idle loop (= "reached BASIC")
	bootLMPR        = 0x40   // reset: ROM0 at section A, ROM1 at section D
	bootHMPR        = 0x00
	realBootStepCap = 5_000_000
)

func capturePath(name string) (string, error) {
	if dir := os.Getenv("SAMBOOT_CAPTURE_DIR"); dir != "" {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
		return "", fmt.Errorf("%s absent under $SAMBOOT_CAPTURE_DIR=%s", name, dir)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	p := filepath.Join(home, "sam-archive", "samboot-capture", name)
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("proprietary capture %s absent at %s (set $SAMBOOT_CAPTURE_DIR); it is Colin's non-redistributable artifact, not in the repo", name, p)
	}
	return p, nil
}

func loadCaptures() (rom, eeprom []byte, err error) {
	romPath, err := capturePath("rom.bin")
	if err != nil {
		return nil, nil, err
	}
	eepPath, err := capturePath("eeprom.bin")
	if err != nil {
		return nil, nil, err
	}
	if rom, err = os.ReadFile(romPath); err != nil {
		return nil, nil, err
	}
	if eeprom, err = os.ReadFile(eepPath); err != nil {
		return nil, nil, err
	}
	if len(rom) != realROMBytes {
		return nil, nil, fmt.Errorf("rom.bin is %d bytes, want %d", len(rom), realROMBytes)
	}
	if len(eeprom) < realEEPROMMin {
		return nil, nil, fmt.Errorf("eeprom.bin is %d bytes, want >= %d", len(eeprom), realEEPROMMin)
	}
	return rom, eeprom, nil
}

// deviceLinearEEPROM un-rotates the captured eeprom.bin into device-linear order
// (the dumper reads by chunk number; chunk 1 lives at device &2000). Copied from
// samboot_real_boot_test.go — device[D] = captured[(D-&2000) mod N].
func deviceLinearEEPROM(captured []byte) []byte {
	const dataBase = 0x2000
	n := len(captured)
	out := make([]byte, n)
	for d := 0; d < n; d++ {
		out[d] = captured[((d-dataBase)%n+n)%n]
	}
	return out
}

// csdV2 builds the ~3.7 GB SDHC CSD the existing Colin tests use, so the boot's
// HDINIT leaves the same byte-vs-block (&A18F) state they validate. Copied from
// cmd/bdostrace.
func csdV2(cSize uint32) [16]byte {
	var c [16]byte
	c[0] = 0x40 // CSD_STRUCTURE = 01 (v2.0/SDHC, block-addressed)
	c[7] = byte((cSize >> 16) & 0x3F)
	c[8] = byte((cSize >> 8) & 0xFF)
	c[9] = byte(cSize & 0xFF)
	return c
}

// ---------------------------------------------------------------------------
// Real B-DOS addresses (from the 1.5t annotated disassembly,
// ~/sam-archive/bdos/analysis/bdos15t-beta6.annotated.dis — proprietary, cited by
// address only). All addresses are REAL (where B-DOS sits at section C, &8000-
// &BFFF, post-boot). The disassembly's internal CALL targets are section-B ALIASES
// (real - &4000), because B-DOS is *designed* to run paged into section B at &4000
// (the &0033 ROM bridge pages it there before each hook dispatches). So the handler
// at hook-table slot 149 reads "&5E16" but its real resident address is &9E16.
const (
	winDevVar = 0x780B // ambient-device var: 2=Trinity SD, 1=floppy (dis &8673/&8684)
	winDevCls = 0x8135 // device-class byte: &44 = mass-storage; else &865C error (dis &8657)
	winDevNum = 0x8132 // device number: 1=floppy,2=Trinity -> &780B (dis &865F/&8662)

	addrDispatch = 0x8319 // hook dispatcher: saves SP/IX/A'/HL'/DE'/BC', indexes &839F
	addrHookTbl  = 0x839F // hook table base; slot = (code-128)*2, handler stored as alias
	addrHWSAD    = 0x9E16 // HWSAD(149) handler: ld hl,wr.buff(&83ED) ; shared rwsad prelude
	addrHRSAD    = 0x9E1B // HRSAD(160) handler: ld hl,rd.buff(&844F) ; shares &9E1E onward
	addrWrBuff   = 0x83ED // wr.buff device trampoline (&0005 jp(hl) target for writes)
	addrRdBuff   = 0x844F // rd.buff device trampoline (for reads)
	addrWDev     = 0x83F7 // write device-dispatch: jp nz,&A8F4 (SD) else FDC poll &8406
	addrRDev     = 0x8459 // read  device-dispatch: jp nz,&A954 (SD) else FDC poll &8460
	addrSDSave   = 0xA8F4 // SD save-buffer path (the §8 CMD24 write core funnel)
	addrSDLoad   = 0xA954 // SD load-buffer path (CMD17 read)
	addrFDCWPoll = 0x8406 // write FDC poll: in a,(&E0) un-timed = the HANG shape (floppy)
	addrFDCRPoll = 0x8460 // read  FDC poll
	addrDevSel   = 0x8662 // device-select: keyed on A (=hk.a/drive), sets &780B
	addrRead780B = 0x8684 // reads &780B: dec a; ret nz -> Z if floppy, NZ if SD
	addrErr5F31  = 0x9F31 // B-DOS error report ("device not present") via &865C jp nz

	// Hook workspace (real; the dispatcher saves the caller's alt-set regs here).
	hkA  = 0x81D9 // saved A' = drive
	hkHL = 0x81DA // saved HL' = source pointer (top 2 bits select the page)
	hkDE = 0x81DC // saved DE' = D:track, E:sector
	hkBC = 0x81DE // saved BC'
)

// symbols names the landmark addresses for the PC trace.
var symbols = map[uint16]string{
	addrDispatch: "DISPATCH(&8319)", addrHWSAD: "HWSAD(&9E16)", addrHRSAD: "HRSAD(&9E1B)",
	addrWrBuff: "wr.buff(&83ED)", addrRdBuff: "rd.buff(&844F)",
	addrWDev: "wdev(&83F7)", addrRDev: "rdev(&8459)",
	addrSDSave: "SD_SAVE(&A8F4)", addrSDLoad: "SD_LOAD(&A954)",
	addrFDCWPoll: "FDC_WPOLL(&8406)", addrFDCRPoll: "FDC_RPOLL(&8460)",
	addrDevSel: "devselect(&8662)", addrRead780B: "read780B(&8684)",
	addrErr5F31: "BDOS_ERROR(&9F31)",
}

// ---------------------------------------------------------------------------
// loggingIO: wraps the real ENC+SD device, logging &DC-&DF SPI traffic with the
// accessing PC (mirrors cmd/bdostrace). It must implement SetTState so the SD
// timing model still clocks.
// ---------------------------------------------------------------------------
type loggingIO struct {
	inner   *z80h.ENC28J60
	lastPC  *uint16
	portLog *[]portEvent
	cap     int
}

type portEvent struct {
	pc    uint16
	write bool
	port  uint8
	val   uint8
}

func (l *loggingIO) In(port uint8) uint8 {
	v := l.inner.In(port)
	if port >= 0xDC && port <= 0xDF && len(*l.portLog) < l.cap {
		*l.portLog = append(*l.portLog, portEvent{*l.lastPC, false, port, v})
	}
	return v
}

func (l *loggingIO) Out(port uint8, value uint8) {
	if port >= 0xDC && port <= 0xDF && len(*l.portLog) < l.cap {
		*l.portLog = append(*l.portLog, portEvent{*l.lastPC, true, port, value})
	}
	l.inner.Out(port, value)
}

func (l *loggingIO) SetTState(t uint64) { l.inner.SetTState(t) }

// ---------------------------------------------------------------------------
// machine: the booted, B-DOS-resident environment + the instrumentation state.
// ---------------------------------------------------------------------------
type machine struct {
	mac     *z80h.Machine
	enc     *z80h.ENC28J60
	lastPC  uint16
	portLog []portEvent

	dosPage uint8 // the physical page B-DOS occupies (section C at idle)
}

// boot builds the emulator, attaches the SD card, and runs the real boot to the
// editor idle loop (B-DOS resident). Mirrors newRealBootMachine + the
// TestRealBootBootsToBASIC recipe.
func boot() (*machine, error) {
	rom, eeprom, err := loadCaptures()
	if err != nil {
		return nil, err
	}
	mac := z80h.New()
	if err := mac.LoadROMImage(rom); err != nil {
		return nil, fmt.Errorf("load ROM: %w", err)
	}
	mac.Pager().LMPR = bootLMPR
	mac.Pager().HMPR = bootHMPR

	enc := z80h.NewENC28J60()
	enc.LoadEEPROMImage(deviceLinearEEPROM(eeprom))
	enc.AttachSD(csdV2(0x001D59)) // ~3.7 GB SDHC — mounts during boot's HDINIT
	m := &machine{mac: mac, enc: enc}

	dev := &loggingIO{inner: enc, lastPC: &m.lastPC, portLog: &m.portLog, cap: 8000}
	mac.AttachIO(dev)

	res, err := mac.RunBootFrom(0x0000, z80h.Entry{
		StepCap:    realBootStepCap,
		StopPC:     addrEditorIdle,
		StopPCSkip: 16,
		Trace:      func(pc uint16) { m.lastPC = pc },
	})
	if err != nil {
		return nil, fmt.Errorf("boot faulted: %w", err)
	}
	if !res.ReachedStop {
		return nil, fmt.Errorf("boot did not reach the editor idle loop &%04X (finalPC=&%04X) — B-DOS not resident", addrEditorIdle, res.PC)
	}
	m.dosPage = mac.Pager().HMPR & 0x1F
	fmt.Printf("booted to B-DOS-resident editor idle: PC=&%04X LMPR=&%02X HMPR=&%02X (B-DOS page %d)\n",
		res.PC, mac.Pager().LMPR, mac.Pager().HMPR, m.dosPage)
	fmt.Printf("post-boot device state: &780B=&%02X (ambient dev) &8132=&%02X (dev#) &8135=&%02X (dev class)\n",
		m.rd(winDevVar), m.rd(winDevNum), m.rd(winDevCls))
	return m, nil
}

func (m *machine) rd(a uint16) uint8 { return m.mac.Read(a, 1)[0] }

// ---------------------------------------------------------------------------
// Experiment 1: faithfulness probe — does a raw `rst 8 / defb N` from arbitrary
// post-boot code reach the real B-DOS hook handler?  (It does not, and the trace
// shows precisely why — the SAM DOS-call indirection is not armed for an external
// caller in the editor-idle snapshot.)
// ---------------------------------------------------------------------------
func (m *machine) experimentRST8() {
	fmt.Println("\n== experiment 1: rst 8 / defb 149 (HWSAD) from a scratch RAM stub ==")
	// Stub in section-B RAM (page after section A): ld a,0 / ld de,track:sec /
	// ld hl,src(>=&4000) / rst 8 / defb 149 / di / halt.
	stub := uint16(0x4100)
	src := uint16(0x4400)
	prog := []byte{
		0x3E, 0x00, // ld a,0 (drive 0 = our seam post-i270)
		0x11, 0x02, 0x00, // ld de,&0002 (D=track0 E=sector2)
		0x21, byte(src), byte(src >> 8), // ld hl,src
		0xCF, 149, // rst 8 ; defb 149 (HWSAD)
		0xF3, 0x76, // di ; halt
	}
	m.mac.Write(stub, prog)

	reached := map[uint16]int{}
	var ring [24]uint16
	ri := 0
	r, err := m.mac.RunBootFrom(stub, z80h.Entry{
		StepCap: 4_000_000,
		Trace: func(pc uint16) {
			m.lastPC = pc
			ring[ri] = pc
			ri = (ri + 1) % len(ring)
			if pc == addrDispatch || pc == addrHWSAD {
				reached[pc]++
			}
		},
	})
	if err != nil {
		fmt.Printf("  run faulted: %v\n", err)
		return
	}
	dosVec := uint16(m.rd(0x5AEE)) | uint16(m.rd(0x5AEF))<<8
	fmt.Printf("  result: halted=%v steps=%d finalPC=&%04X\n", r.Halted, r.Steps, r.PC)
	fmt.Printf("  reached dispatcher &8319: %d times; reached HWSAD &9E16: %d times\n",
		reached[addrDispatch], reached[addrHWSAD])
	fmt.Printf("  SAM DOS-hook vector (&5AEE) = &%04X, &5BC3(DOS-present)=&%02X\n", dosVec, m.rd(0x5BC3))
	fmt.Println("  finding: the ROM RST8 handler (&37CE) reads the inline hook code, then")
	fmt.Println("    dispatches DOS via `ld sp,(&5C3D); jp &1D95` (the relocated DOS-call")
	fmt.Println("    stack chain). In the editor-idle snapshot that chain is not armed for an")
	fmt.Println("    EXTERNAL caller, so the hook returns to the editor without reaching &8319.")
	fmt.Printf("    recent PCs: %s\n", fmtPCs(ring[:], ri))
}

// ---------------------------------------------------------------------------
// Experiment 2: drive the device-select (&8662) directly for each drive value to
// capture the &780B-vs-FDC discriminator — the gold contract's core. This isolates
// the exact dispatch that decides SD-path vs FDC-hang.
// ---------------------------------------------------------------------------
func (m *machine) experimentDeviceSelect() {
	fmt.Println("\n== experiment 2: device-select (&8662) -> ambient device (&780B) ==")
	fmt.Println("  The handler does `ld a,(hk.a); call &8662`. &8662 is keyed on that A:")
	fmt.Println("    cp 1 -> &780B=1 (floppy) ; cp 2 -> &780B=2 (Trinity) ; else (A=0) leave &780B.")
	fmt.Println("  Driving &8662 in isolation confirms the A==1 floppy store directly; the A==0")
	fmt.Println("  and A==2 sub-paths (&8680 floppy-port setup / &4677+&60E4 SD setup) make")
	fmt.Println("  further B-DOS sub-calls that need the fuller record-select context to RET")
	fmt.Println("  cleanly, so they are read from the disassembly, not the isolated call:")
	for _, a := range []uint8{0, 1, 2} {
		const prior = 1 // a known prior &780B (floppy) so we see whether A=0 leaves it
		m.pageDOSIntoSectionB()
		m.mac.Write(winDevVar, []byte{prior})
		m.mac.Write(winDevCls, []byte{0x44}) // arm device class so &865C does not error
		r := m.callAliasB(0x8662, a)
		dev := m.rd(winDevVar)
		fmt.Printf("  A=%d (prior &780B=%d) -> &780B=&%02X (ran %d steps, halted=%v)\n",
			a, prior, dev, r.Steps, r.Halted)
	}
	fmt.Println("  From the 1.5t disasm (&8662-&8695): A==2 -> &780B=2 (SD); A==1 -> &780B=1")
	fmt.Println("  (floppy/FDC hang); A==0 -> &8680 floppy-port setup, &780B UNCHANGED.")
}

// pageDOSIntoSectionB maps the B-DOS page into section B (&4000-&7FFF) so a routine
// whose internal CALLs use section-B aliases (real-&4000) resolves, while keeping
// section A = ROM0 (for the &0005/&0033/&0103 ROM bridges). This is the run-context
// the &0033 ROM bridge establishes before each hook handler.
func (m *machine) pageDOSIntoSectionB() {
	// secB page = (LMPR&0x1F)+1; want it = dosPage; keep bit5=0 (ROM0 at A).
	m.mac.Pager().LMPR = (m.dosPage - 1) & 0x1F
}

// callAliasB calls a real B-DOS address at its section-B alias (real-&4000) with
// A preloaded, trapping on the RET. Returns when it halts or caps.
func (m *machine) callAliasB(real uint16, a uint8) z80h.CallResult {
	alias := real - 0x4000
	r, err := m.mac.RunFrom(alias, z80h.Entry{A: a, StepCap: 2_000_000})
	if err != nil {
		// A cap inside a routine is reported as an error by RunFrom; surface and move on.
		fmt.Printf("    (callAliasB &%04X capped/faulted: %v)\n", real, err)
	}
	return r
}

// ---------------------------------------------------------------------------
// Experiment 3: drive the HWSAD and HRSAD handlers (at their section-B aliases,
// B-DOS paged into section B) for the same flat register shape, with the ambient
// device preset each way, and show that read and write take the SAME gate — the
// refutation of the wr.buff-vs-rd.buff asymmetry hypothesis.
// ---------------------------------------------------------------------------
func (m *machine) experimentHandlerSymmetry() {
	fmt.Println("\n== experiment 3: HWSAD vs HRSAD — same &780B gate (asymmetry hypothesis) ==")
	type run struct {
		label   string
		handler uint16
		hkADrv  uint8
		dev     int // preset &780B (-1 = leave)
	}
	runs := []run{
		{"HWSAD a=2 (Trinity drive)", addrHWSAD, 2, -1},
		{"HWSAD a=0 dev=2 (record-selected SD)", addrHWSAD, 0, 2},
		{"HWSAD a=0 dev=1 (stale floppy)", addrHWSAD, 0, 1},
		{"HRSAD a=2 (Trinity drive)", addrHRSAD, 2, -1},
		{"HRSAD a=0 dev=2 (record-selected SD)", addrHRSAD, 0, 2},
		{"HRSAD a=0 dev=1 (stale floppy)", addrHRSAD, 0, 1},
	}
	for _, rn := range runs {
		m.pageDOSIntoSectionB()
		// Pre-poke the hook workspace (section-B aliases) + arm the device class so
		// the handler reaches the device-select rather than the &865C "no device"
		// error. hk.hl must be >= &4000 (top 2 bits select the page).
		srcPtr := uint16(0x6000) // section B/C source pointer (page in top bits)
		m.mac.Write(hkA-0x4000, []byte{rn.hkADrv})
		m.mac.Write(hkHL-0x4000, []byte{byte(srcPtr), byte(srcPtr >> 8)})
		m.mac.Write(hkDE-0x4000, []byte{0x02, 0x00}) // E=sector2, D=track0
		m.mac.Write(hkBC-0x4000, []byte{0, 0})
		m.mac.Write(winDevCls, []byte{0x44})
		if rn.dev >= 0 {
			m.mac.Write(winDevVar, []byte{byte(rn.dev)})
		}
		visits := map[uint16]int{}
		var order []uint16
		base := len(m.portLog)
		var escTo, escFrom uint16
		last := uint16(0)
		r, err := m.mac.RunBootFrom(rn.handler-0x4000, z80h.Entry{
			StepCap: 3_000_000,
			Trace: func(pc uint16) {
				m.lastPC = pc
				real := pc
				if pc < 0x8000 && pc >= 0x4000 {
					real = pc + 0x4000
				}
				if escTo == 0 && pc < 0x4000 && last >= 0x4000 &&
					pc != 0x0005 && pc != 0x0033 && pc != 0x0103 && pc != 0x0038 {
					escTo, escFrom = pc, last
				}
				last = pc
				if _, ok := symbols[real]; ok {
					if visits[real] == 0 {
						order = append(order, real)
					}
					visits[real]++
				}
			},
		})
		if err != nil {
			fmt.Printf("  [%s] faulted: %v\n", rn.label, err)
			continue
		}
		var names []string
		took := "(neither SD nor FDC reached)"
		for _, a := range order {
			names = append(names, symbols[a])
			switch a {
			case addrSDSave, addrSDLoad:
				took = "SD PATH"
			case addrFDCWPoll, addrFDCRPoll:
				took = "FDC POLL (hang shape)"
			case addrErr5F31:
				took = "B-DOS ERROR (device-class check)"
			}
		}
		fmt.Printf("  [%-38s] halted=%v steps=%d &780B=&%02X dispatch=%s\n",
			rn.label, r.Halted, r.Steps, m.rd(winDevVar), took)
		fmt.Printf("      path: %v\n", names)
		if escTo != 0 {
			fmt.Printf("      (escaped run window at &%04X from &%04X)\n", escTo, escFrom)
		}
		_ = base
	}
	fmt.Println("  Observed: both HWSAD and HRSAD enter the shared prelude and reach the SAME")
	fmt.Println("  device-select &8662 before the page-setup's `out (&fb)` repages section C")
	fmt.Println("  and the run escapes into a ROM bridge (the editor-idle snapshot lacks the")
	fmt.Println("  full DOS-call paging context). The gate itself is byte-identical in the two")
	fmt.Println("  trampolines (disasm: wr.buff &83ED `call &8684; jp nz,&A8F4`; rd.buff &844F")
	fmt.Println("  `call &8684; jp nz,&A954` — same &8684 read of &780B) => NO wr/rd asymmetry.")
}

func fmtPCs(ring []uint16, ri int) string {
	out := ""
	for i := 0; i < len(ring); i++ {
		out += fmt.Sprintf("&%04X ", ring[(ri+i)%len(ring)])
	}
	return out
}

func main() {
	flag.Parse()
	m, err := boot()
	if err != nil {
		fmt.Fprintln(os.Stderr, "bdostrace-paged:", err)
		os.Exit(1)
	}
	m.experimentRST8()
	m.experimentDeviceSelect()
	m.experimentHandlerSymmetry()

	fmt.Println("\n== GOLD CONTRACT (see docs/notes/trinity-sd-z80-interface.md §8b) ==")
	fmt.Println("  A successful HWSAD write needs the ambient device armed as Trinity SD:")
	fmt.Println("    &8135 (device class) == &44   (else &865C -> B-DOS 'no device' error)")
	fmt.Println("    &8132 (device number) == 2    (Trinity), which &8662 turns into")
	fmt.Println("    &780B (ambient device) == 2   (so &83F7/&8459 take jp nz -> SD path)")
	fmt.Println("  All three are armed by HRECORD-select (&9F11/&9F15 set &8132+&8135).")
	fmt.Println("  hk.a (the drive in A) only re-runs &8662: a=2 sets &780B=2, a=1 sets")
	fmt.Println("  &780B=1 (FDC hang), a=0 LEAVES &780B as the prior select left it.")
	fmt.Println("  Read (HRSAD) and write (HWSAD) share this gate -> the discriminator is")
	fmt.Println("  the AMBIENT DEVICE (&780B/&8132/&8135), NOT a wr.buff-vs-rd.buff asymmetry.")
}
