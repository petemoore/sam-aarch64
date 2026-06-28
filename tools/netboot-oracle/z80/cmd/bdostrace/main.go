// Command bdostrace traces Colin Piggot's REAL B-DOS 1.5t (beta 6) Trinity SD
// record-write code paths in emulation, to derive the invocation contract our
// netboot serve (src/netboot/bdos_seam.asm) must satisfy. See the README and
// docs/plans/i280-bdos-write-trace.md for the full rationale.
//
// Why this tool exists (i280): on real hardware (2026-06-28) our TFTP-push serve
// hangs in the per-block SD write — bdos_write_sector -> rst 8 / defb 149 (the
// B-DOS HWSAD hook). B-DOS is resident (it loaded trinload), so the hook is
// serviced, yet our invocation hangs where B-DOS's own record writes succeed.
// The captured Trinity docs have NO SD-write example, so Colin's B-DOS is the
// only authority — and the way to learn the correct contract is to TRACE B-DOS's
// own working write in emulation. (CLAUDE.md feedback_port_diff_authority_first:
// the Z80/Colin code is the authority; our Go model is derived from it.)
//
// What the existing tests already prove (and therefore what this tool does NOT
// re-litigate): the &A623 SD-init ladder (sd_init_colin_test.go), the records
// math (csd_decode_colin_test.go), and the CMD24 write-core + CMD17 read-core
// sector ROUND-TRIP (sd_record_io_colin_test.go) all PASS in emulation. So the
// low-level SPI write succeeds. The UNcovered gap — and the focus here — is the
// HWSAD HOOK HANDLER path (&9E16): the page-setup + seek prelude that interprets
// the saved hook registers (hk.hl/hk.de/hk.a), drives the HMPR paging port, runs
// the seek (&A16B) to compute the LBA and poke the seek immediates, then falls
// into the write core. Our seam delegates ALL of that to HWSAD; no test exercises
// it. This tool drives that handler against the real SD-SPI model with full
// port/PC/register/memory instrumentation and reports where it diverges or hangs.
//
// The B-DOS 1.5t binary is Colin's PROPRIETARY code: referenced by its
// ~/sam-archive path (or $BDOS15T_BIN), never copied into the repo. When it is
// absent the tool errors out — there is nothing to trace without it.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

// ---------------------------------------------------------------------------
// Address map (real B-DOS addresses and their section-B aliases = real - &4000).
//
// The DOS page executes paged into SAM section B (&4000-&7FFF), so every internal
// CALL/data access uses a section-B alias: real X (&8000-&BFFF) appears at X-&4000.
// We load the whole DOS page INTO section B at the section-B org and run there, so
// the &5xxx/&6xxx alias CALLs resolve. (Same recipe as csd_decode_colin_test.go.)
// ---------------------------------------------------------------------------
const (
	bdosOrgReal = 0x8009                 // file offset 0 loads here on a real SAM
	secBBias    = 0x4000                 // section-B alias = real address - &4000
	bdosOrgB    = bdosOrgReal - secBBias // &4009 load origin in the section-B window

	// Routine entry points (real addresses; aliasB() converts to the run window).
	addrSDInit   = 0xA623 // SD init ladder (HDINIT body) — CMD0/8/41/58/9/16, sets &A18F shift
	addrHWSAD    = 0x9E16 // HWSAD hook handler (hook 149): page-setup + seek + write
	addrSeek     = 0xA16B // hd.seek-t: D=track,E=sector -> LBA, pokes &A836/&A843
	addrCmdSend  = 0xA81F // sd.cmd-with-address: B=opcode, immediates carry the LBA
	addrWriteCor = 0xA918 // hd.svb-t: WP check, CMD24, &FE token, 510x OUTI
	addrWriteTal = 0xA86B // write tail: last 2 OUTI, CRC, data-response, busy-wait
	addrDeselect = 0xA8D7 // SD deselect + EI

	// Self-modified seek immediates the CMD frame's 32-bit address is poked into
	// (trinity-sd-z80-interface.md §5/§7): &A835 `ld hl,nn` operand = &A836 (low) /
	// &A837 (high) = HIGH word; &A842 `ld hl,nn` operand = &A843/&A844 = LOW word.
	addrSeekHiImm = 0xA836 // HIGH word immediate
	addrSeekLoImm = 0xA843 // LOW  word immediate

	// Hook workspace (real addresses; the dispatcher at &8319 saves the caller's
	// registers here before jumping to the handler). HWSAD's handler reads these.
	addrHkA  = 0x81D9 // hk.a : saved A (drive)
	addrHkHL = 0x81DA // hk.hl: saved HL (source pointer, top bits select page)
	addrHkDE = 0x81DC // hk.de: saved DE (D=track, E=sector)
	addrHkBC = 0x81DE // hk.bc: saved BC

	addrHdWp = 0x80C8 // hd.wp write-protect DVAR (must be 0 to allow writes)

	// Device-dispatch state. xsad's dispatch (&83F7) does `call &8684` which reads
	// the ambient-device var &780B (`dec a; ret nz`): &780B==1 -> floppy (FDC ports
	// &E0/&E4, which hang with no controller), &780B!=1 -> the SD/Trinity path
	// (&A8F4). HWSAD's &8662 sets &780B from hk.a (A==2 -> Trinity store 2; A==1 ->
	// floppy store 1; A==0 -> floppy-port-setup, &780B unchanged). &780B is referenced
	// in the code as a bare &780B — the &7800 scratch page is mapped directly in the
	// run window (as csdBuf=&780F is), so it is ALREADY a window address (no aliasB).
	// Likewise the FDC-poke targets &45EA/&45A5 are section-B aliases used verbatim.
	winDevVar   = 0x780B // ambient-device var (2 = Trinity, 1 = floppy)
	winFdcPoke1 = 0x45EA // FDC port base self-modified by HWSAD's floppy setup (&8680)
	winFdcPoke2 = 0x45A5

	// (The HRECORD handler is at real &9FAB — hook 156, A=0 + record in hk.hl — and
	// is what i280b will drive before HWSAD to set the ambient device + record base.)

	// A 512-byte source buffer + a 6-byte launch prologue in the section-B window,
	// above the loaded code (ends ~real &AB2D = &6B2D) and clear of the harness
	// stack (RunFrom sets SP=&6FFE; nested CALLs grow just below &7000).
	sectorBufReal = 0xB400 // -> &7400 in the window (free RAM, clear of stack/code)
	launchReal    = 0xBE00 // -> &7E00 launch prologue
)

// aliasB converts a real B-DOS address to its section-B run-window address.
func aliasB(real uint16) uint16 { return real - secBBias }

// ---------------------------------------------------------------------------
// CSD construction (the ~3.7 GB SDHC card the existing Colin tests use, so the
// init ladder leaves the same byte-vs-block (&A18F) state they validate).
// ---------------------------------------------------------------------------
func csdV2(cSize uint32) [16]byte {
	var c [16]byte
	c[0] = 0x40 // CSD_STRUCTURE = 01 (v2.0/SDHC, block-addressed)
	c[7] = byte((cSize >> 16) & 0x3F)
	c[8] = byte((cSize >> 8) & 0xFF)
	c[9] = byte(cSize & 0xFF)
	return c
}

// ---------------------------------------------------------------------------
// Instrumentation.
// ---------------------------------------------------------------------------

// symbols maps notable real addresses to names, printed when execution enters one.
var symbols = map[uint16]string{
	addrSDInit:   "SD_INIT(&A623)",
	addrHWSAD:    "HWSAD_HANDLER(&9E16)",
	addrSeek:     "SEEK(&A16B)",
	addrCmdSend:  "CMD_SEND(&A81F)",
	addrWriteCor: "WRITE_CORE(&A918)",
	addrWriteTal: "WRITE_TAIL(&A86B)",
	addrDeselect: "DESELECT(&A8D7)",
}

// tracer holds the shared state the PC callback, the port-logging IODevice and
// the memory-access hook all write into. lastPC lets the port logger attribute a
// port access to the IN/OUT instruction performing it.
type tracer struct {
	mac    *z80h.Machine
	dev    *loggingIO
	lastPC uint16

	steps    uint64
	maxSteps uint64

	// recentPC is a small ring of the last few PCs, for hang reporting.
	recentPC [16]uint16
	rpi      int

	// portLog records every &DC-&DF access (capped); the hang loop shows here.
	portLog []portEvent
	portCap int

	// symVisits counts entries to each named symbol (so we see whether the path
	// reached the write core / where it stalled).
	symVisits map[uint16]int
	symOrder  []uint16 // first-seen order of symbols, for a readable summary

	// watch records reads/writes of the seek immediates and key DVARs.
	watch    []memEvent
	watchCap int

	// escape captures the first time execution leaves the section-B run window
	// (PC < &4000, i.e. into ROM/empty low memory the flat harness lacks): the PC
	// it jumped FROM (the last in-window instruction) and the target.
	escFromPC uint16
	escToPC   uint16
	escaped   bool
}

type portEvent struct {
	step  uint64
	pc    uint16
	write bool
	port  uint8
	val   uint8
}

type memEvent struct {
	step  uint64
	pc    uint16
	write bool
	addr  uint16
	val   uint8
}

func (tr *tracer) onPC(pc uint16) {
	tr.steps++
	// First escape below the run window (&4000) = a call/jp/ret into ROM the flat
	// harness does not model; capture the in-window PC we left from.
	if !tr.escaped && pc < 0x4000 && tr.lastPC >= 0x4000 {
		tr.escaped = true
		tr.escFromPC = tr.lastPC
		tr.escToPC = pc
	}
	tr.lastPC = pc
	tr.recentPC[tr.rpi] = pc
	tr.rpi = (tr.rpi + 1) % len(tr.recentPC)
	if name, ok := symbols[realFromWindow(pc)]; ok {
		if tr.symVisits[realFromWindow(pc)] == 0 {
			tr.symOrder = append(tr.symOrder, realFromWindow(pc))
			fmt.Printf("  [step %7d] ENTER %s (window &%04X)\n", tr.steps, name, pc)
		}
		tr.symVisits[realFromWindow(pc)]++
	}
}

// realFromWindow maps a section-B window PC back to its real B-DOS address (for
// symbol lookup). Code runs at window addresses &4000-&7FFF == real &8000-&BFFF.
func realFromWindow(pc uint16) uint16 {
	if pc >= 0x4000 && pc < 0x8000 {
		return pc + secBBias
	}
	return pc
}

// loggingIO wraps the real ENC+SD IODevice, logging &DC-&DF traffic with the PC
// of the accessing instruction, then delegating to the genuine model so behaviour
// is unchanged. It forwards SetTState so the SD/ENC timing model still works.
type loggingIO struct {
	inner *z80h.ENC28J60
	tr    *tracer
}

func (l *loggingIO) In(port uint8) uint8 {
	v := l.inner.In(port)
	if port >= 0xDC && port <= 0xDF {
		l.tr.recordPort(false, port, v)
	}
	return v
}

func (l *loggingIO) Out(port uint8, value uint8) {
	if port >= 0xDC && port <= 0xDF {
		l.tr.recordPort(true, port, value)
	}
	l.inner.Out(port, value)
}

func (l *loggingIO) SetTState(t uint64) { l.inner.SetTState(t) }

func (tr *tracer) recordPort(write bool, port, val uint8) {
	if len(tr.portLog) < tr.portCap {
		tr.portLog = append(tr.portLog, portEvent{tr.steps, tr.lastPC, write, port, val})
	}
}

func (tr *tracer) onMem(addr uint16, write bool, val uint8) {
	if !isWatched(addr) {
		return
	}
	if len(tr.watch) < tr.watchCap {
		tr.watch = append(tr.watch, memEvent{tr.steps, tr.lastPC, write, addr, val})
	}
}

// watchName labels a watched window address for the report.
func watchName(addr uint16) string {
	switch addr {
	case aliasB(addrSeekHiImm), aliasB(addrSeekHiImm + 1):
		return "; seek-imm HIGH word"
	case aliasB(addrSeekLoImm), aliasB(addrSeekLoImm + 1):
		return "; seek-imm LOW word"
	case aliasB(addrHdWp):
		return "; hd.wp"
	case aliasB(addrHkA):
		return "; hk.a"
	case aliasB(addrHkHL), aliasB(addrHkHL + 1):
		return "; hk.hl"
	case aliasB(addrHkDE), aliasB(addrHkDE + 1):
		return "; hk.de"
	case aliasB(addrHkBC), aliasB(addrHkBC + 1):
		return "; hk.bc"
	case winDevVar:
		return "; ambient-device (&780B: 2=Trinity,1=floppy)"
	case winFdcPoke1, winFdcPoke2:
		return "; FDC-port self-mod (floppy path)"
	}
	return ""
}

// isWatched reports whether a window address is one of the seek immediates or key
// DVARs we want to see read/written (their section-B aliases).
func isWatched(addr uint16) bool {
	switch addr {
	case aliasB(addrSeekHiImm), aliasB(addrSeekHiImm + 1),
		aliasB(addrSeekLoImm), aliasB(addrSeekLoImm + 1),
		aliasB(addrHdWp),
		aliasB(addrHkA), aliasB(addrHkHL), aliasB(addrHkHL + 1),
		aliasB(addrHkDE), aliasB(addrHkDE + 1),
		aliasB(addrHkBC), aliasB(addrHkBC + 1),
		winDevVar, winFdcPoke1, winFdcPoke2:
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// Setup.
// ---------------------------------------------------------------------------

func bdosBinPath() (string, error) {
	if p := os.Getenv("BDOS15T_BIN"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
		return "", fmt.Errorf("$BDOS15T_BIN=%q does not exist", p)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	p := filepath.Join(home, "sam-archive", "bdos", "analysis", "extracted", "bdos15t-beta6.bin")
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("B-DOS 1.5t binary absent at %s (set $BDOS15T_BIN); it is Colin's proprietary code, not in the repo", p)
	}
	return p, nil
}

func newTracer(maxSteps uint64) (*tracer, error) {
	bin, err := bdosBinPath()
	if err != nil {
		return nil, err
	}
	code, err := os.ReadFile(bin)
	if err != nil {
		return nil, fmt.Errorf("read B-DOS bin: %w", err)
	}
	mac := z80h.New()
	mac.Write(bdosOrgB, code)

	tr := &tracer{
		mac:       mac,
		maxSteps:  maxSteps,
		portCap:   4000,
		watchCap:  2000,
		symVisits: map[uint16]int{},
	}

	enc := z80h.NewENC28J60()
	enc.AttachSD(csdV2(0x001D59)) // ~3.7 GB SDHC, the existing-tests' card
	dev := &loggingIO{inner: enc, tr: tr}
	tr.dev = dev
	mac.AttachIO(dev)
	mac.SetAccessTrace(tr.onMem)
	return tr, nil
}

// run executes from a window address with the given entry registers, wiring the
// PC trace, and returns the result. It resets the per-run step/log state.
func (tr *tracer) run(fromReal uint16, in z80h.Entry) z80h.CallResult {
	tr.steps = 0
	tr.portLog = tr.portLog[:0]
	tr.watch = tr.watch[:0]
	tr.symVisits = map[uint16]int{}
	tr.symOrder = nil
	tr.escaped = false
	tr.escFromPC = 0
	tr.escToPC = 0
	in.Trace = tr.onPC
	if in.StepCap == 0 {
		in.StepCap = tr.maxSteps
	}
	res, err := tr.mac.RunFrom(aliasB(fromReal), in)
	if err != nil {
		// RunFrom reports a step-cap error with res zeroed; our own onPC counter
		// (tr.steps) and the recentPC ring still hold the truth, so surface them.
		fmt.Printf("  run error: %v\n", err)
		fmt.Printf("  *** HANG after %d steps; recent PCs (real): ", tr.steps)
		for i := 0; i < len(tr.recentPC); i++ {
			p := tr.recentPC[(tr.rpi+i)%len(tr.recentPC)]
			fmt.Printf("&%04X ", realFromWindow(p))
		}
		fmt.Println()
		if tr.escaped {
			fmt.Printf("  *** ESCAPED the run window: in-window real &%04X -> &%04X (a call/ret into SAM ROM / sysvars the flat section-B harness lacks)\n",
				realFromWindow(tr.escFromPC), tr.escToPC)
		}
	}
	return res
}

// ---------------------------------------------------------------------------
// Reporting.
// ---------------------------------------------------------------------------

func (tr *tracer) report(res z80h.CallResult) {
	fmt.Printf("\n  result: halted=%v reachedStop=%v steps=%d PC=&%04X (real &%04X) A=&%02X HL=&%04X DE=&%04X BC=&%04X\n",
		res.Halted, res.ReachedStop, res.Steps, res.PC, realFromWindow(res.PC), res.A, res.HL, res.DE, res.BC)

	if !res.Halted && res.Steps >= tr.maxSteps {
		fmt.Printf("  *** STEP CAP HIT — likely HANG at real &%04X ***\n", realFromWindow(res.PC))
		fmt.Printf("  recent PCs (window->real): ")
		for i := 0; i < len(tr.recentPC); i++ {
			p := tr.recentPC[(tr.rpi+i)%len(tr.recentPC)]
			fmt.Printf("&%04X ", realFromWindow(p))
		}
		fmt.Println()
	}

	// Symbol path (first-seen order, with visit counts).
	fmt.Printf("  path: ")
	for _, s := range tr.symOrder {
		fmt.Printf("%s x%d -> ", symbols[s], tr.symVisits[s])
	}
	fmt.Println("(end)")

	// Seek-immediate + DVAR accesses.
	if len(tr.watch) > 0 {
		fmt.Println("  watched memory (seek immediates / DVARs / device):")
		for _, m := range tr.watch {
			rw := "RD"
			if m.write {
				rw = "WR"
			}
			fmt.Printf("    [step %7d pc&%04X] %s &%04X = &%02X   %s\n",
				m.step, realFromWindow(m.pc), rw, m.addr, m.val, watchName(m.addr))
		}
	}

	// Port traffic summary + tail (the hang loop shows in the tail).
	fmt.Printf("  port events: %d logged\n", len(tr.portLog))
	tail := tr.portLog
	if len(tail) > 60 {
		tail = tail[len(tail)-60:]
		fmt.Println("  ...(last 60 port events)...")
	}
	for _, e := range tail {
		rw := "IN "
		if e.write {
			rw = "OUT"
		}
		fmt.Printf("    [step %7d pc&%04X] %s (&%02X) = &%02X%s\n",
			e.step, realFromWindow(e.pc), rw, e.port, e.val, decodeCtl(e.write, e.port, e.val))
	}
}

// decodeCtl annotates a &DC control byte (the SD select/mode) for readability.
func decodeCtl(write bool, port, val uint8) string {
	if port != 0xDC {
		return ""
	}
	if write {
		switch val {
		case 0x30:
			return "  ; SD deselect"
		case 0x31:
			return "  ; SD select (manual)"
		case 0x38:
			return "  ; uC SD-init"
		case 0x3F:
			return "  ; SD select (auto-null)"
		case 0x04:
			return "  ; all-deselect / auto-null off"
		}
		return ""
	}
	if val&0x08 != 0 {
		return "  ; BUSY"
	}
	return "  ; ready"
}

// ---------------------------------------------------------------------------
// Scenarios.
// ---------------------------------------------------------------------------

// scenarioInit runs B-DOS's real SD-init ladder so the card and the &A18F
// byte-vs-block shift / hd.wp workspace are set the way B-DOS expects before any
// write. It is the precondition for the write scenarios and a sanity check that
// the SD model answers the full CMD0/8/41/58/9/16 ladder.
func (tr *tracer) scenarioInit() z80h.CallResult {
	fmt.Println("== scenario: SD init ladder (&A623) ==")
	res := tr.run(addrSDInit, z80h.Entry{StepCap: 5_000_000})
	tr.report(res)
	return res
}

// scenarioWriteCore drives Colin's proven CMD24 write core + tail directly (the
// path the round-trip test validates), as the GOLD reference port byte-stream for
// a successful single-sector write. The LBA is poked into the seek immediates
// exactly as the seek path does.
func (tr *tracer) scenarioWriteCore(sector uint32) z80h.CallResult {
	fmt.Printf("== scenario: GOLD write core+tail (&A918/&A86B), sector=%d ==\n", sector)
	// Poke the 32-bit address into the seek immediates (LE operands).
	tr.mac.Write(aliasB(addrSeekHiImm), []byte{byte(sector >> 16), byte(sector >> 24)})
	tr.mac.Write(aliasB(addrSeekLoImm), []byte{byte(sector), byte(sector >> 8)})
	tr.mac.Write(aliasB(addrHdWp), []byte{0x00}) // not write-protected
	// A recognisable 512-byte pattern.
	pat := make([]byte, 512)
	for i := range pat {
		pat[i] = byte((i*7 + 0x5A) ^ (i >> 3))
	}
	tr.mac.Write(aliasB(sectorBufReal), pat)
	// Launch prologue: ld hl,buf; ld c,&df; call write-core; call write-tail; ret.
	core := aliasB(addrWriteCor)
	tail := aliasB(addrWriteTal)
	buf := aliasB(sectorBufReal)
	tr.mac.Write(aliasB(launchReal), []byte{
		0x21, byte(buf), byte(buf >> 8), // ld hl,buf
		0x0E, 0xDF, // ld c,&df
		0xCD, byte(core), byte(core >> 8), // call write-core
		0xCD, byte(tail), byte(tail >> 8), // call write-tail
		0xC9, // ret
	})
	res := tr.run(launchReal, z80h.Entry{StepCap: 3_000_000})
	tr.report(res)
	if sec, ok := tr.dev.inner.SD().CapturedSector(sector); ok {
		fmt.Printf("  model captured %d bytes at sector %d (write committed)\n", len(sec), sector)
	} else {
		fmt.Printf("  model captured NOTHING at sector %d (write did not commit)\n", sector)
	}
	return res
}

// scenarioHWSAD drives the HWSAD hook HANDLER (&9E16) the way the rst-8 dispatcher
// would, by pre-poking the hook workspace (hk.a/hk.hl/hk.de) as our seam's
// register contract implies (A=drive, D=track, E=sector, HL=source). This is the
// uncovered path: the handler does the page-setup + seek prelude (incl. the
// device-dispatch at &83F7 keyed on the ambient-device var &780B) before the write
// core. hkA is the A our seam passes (post-i270 = 0). presetDev, when >= 0, sets
// the ambient-device var &780B first — modelling the state a prior HRECORD-select
// leaves (Trinity = 2) so we can isolate the device-dispatch contract.
func (tr *tracer) scenarioHWSAD(track, sector uint8, srcReal uint16, hkA uint8, presetDev int) z80h.CallResult {
	fmt.Printf("== scenario: HWSAD handler (&9E16) track=%d sector=%d src=&%04X hk.a=%d presetDev=%d ==\n",
		track, sector, srcReal, hkA, presetDev)
	tr.mac.Write(aliasB(addrHdWp), []byte{0x00})
	if presetDev >= 0 {
		tr.mac.Write(winDevVar, []byte{byte(presetDev)})
	}
	// Populate the hook workspace as the dispatcher at &8319 would for our seam's
	// `A=hkA, D=track, E=sector, HL=source` contract.
	tr.mac.Write(aliasB(addrHkA), []byte{hkA})                                // hk.a
	tr.mac.Write(aliasB(addrHkHL), []byte{byte(srcReal), byte(srcReal >> 8)}) // hk.hl = source
	tr.mac.Write(aliasB(addrHkDE), []byte{sector, track})                     // hk.de = D=track,E=sector
	tr.mac.Write(aliasB(addrHkBC), []byte{0x00, 0x00})                        // hk.bc = 0
	// Seed a recognisable buffer at the source (in the window).
	pat := make([]byte, 512)
	for i := range pat {
		pat[i] = byte(0xA5 ^ (i & 0xFF))
	}
	tr.mac.Write(aliasB(sectorBufReal), pat)
	res := tr.run(addrHWSAD, z80h.Entry{StepCap: tr.maxSteps})
	tr.report(res)
	return res
}

// ---------------------------------------------------------------------------
// main.
// ---------------------------------------------------------------------------

func main() {
	scenario := flag.String("scenario", "all", "which trace to run: init | writecore | hwsad | all")
	maxSteps := flag.Uint64("maxsteps", 3_000_000, "instruction step cap (hang detector)")
	hkA := flag.Int("hka", 0, "hwsad scenario: the A (drive) value our seam passes (0=our seam post-i270; 2=Trinity device)")
	presetDev := flag.Int("dev", -1, "hwsad scenario: preset ambient-device var &780B before HWSAD (-1=leave as init left it; 2=Trinity as HRECORD leaves it)")
	flag.Parse()

	tr, err := newTracer(*maxSteps)
	if err != nil {
		fmt.Fprintln(os.Stderr, "bdostrace:", err)
		os.Exit(1)
	}

	switch *scenario {
	case "init":
		tr.scenarioInit()
	case "writecore":
		tr.scenarioInit()
		tr.scenarioWriteCore(5*1600 + 1234)
	case "hwsad":
		tr.scenarioInit()
		tr.scenarioHWSAD(2, 3, sectorBufReal, uint8(*hkA), *presetDev)
	case "all":
		tr.scenarioInit()
		fmt.Println()
		tr.scenarioWriteCore(5*1600 + 1234)
		fmt.Println()
		tr.scenarioHWSAD(2, 3, sectorBufReal, uint8(*hkA), *presetDev)
	default:
		fmt.Fprintf(os.Stderr, "unknown scenario %q\n", *scenario)
		os.Exit(2)
	}
}
