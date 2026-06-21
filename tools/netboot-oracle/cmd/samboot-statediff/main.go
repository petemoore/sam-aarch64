// Command samboot-statediff boots the stock SAM ROM v3.0 and Colin Piggot's
// forked ROM+EEPROM each to the SAME editor sync point and diffs full 64 KB RAM.
//
// WHY. The SAMBOOT faithful-inject design (docs/specs/samboot-opening-screen.md)
// has two open questions about whether our inject must reproduce stock-ROM
// teardown/editor state that Colin's fork handles differently:
//
//	Q1 — the MAINER3 editor flags: TVFLAG (&5C3C) bit 5, FLAGS (&5C3B) bit 7.
//	Q2 — Colin's two teardown pokes the stock ROM never did:
//	       NSPPC (&5C44)=&FF and TVDATA (&5BBE)=&10.
//
// Pete's experiment (the arbiter — measure, don't reason): boot the stock ROM and
// Colin's full fork each to the same sync point — the ROM editor's idle keyboard
// wait after an injected key 'x' — snapshot full RAM in both, and diff. The diff
// shows whether Colin's boot leaves sysvar/RAM state the stock ROM didn't. This
// command produces the diff; it does NOT decide Q1/Q2 (the orchestrator does).
//
// THE SYNC POINT (verified empirically, see the package comments below). Both
// ROMs run the IDENTICAL ROM editor keyboard-input code. Once each reaches the
// editor's keyboard-wait idle loop, its FLAGS-poll sits at &050E; on an available
// key it reads LASTK (&5C08) and clears FLAGS bit 5 (key consumed) at &0514. With
// a 'x' injected via the i138 LASTK/FLAGS stub, BOTH ROMs walk the exact same PC
// trail &050E &0510 &0511 &0514 ... — so &0514 (the key-consume) is a deterministic
// sync PC common to both. We snapshot all 64 KB of logical RAM at that PC.
//
// REACHING THE EDITOR.
//   - Colin's fork AUTO-BOOTS through its EEPROM bootblock + B-DOS init straight
//     to the editor FLAGS-poll idle (i257 EI;HALT-resume lets it run through
//     B-DOS init). No keypress needed to reach the editor.
//   - The stock v3.0 ROM has NO Trinity/EEPROM; it boots to its banner and WAITS
//     FOR A KEY (WTFK) at a hardware keyboard-matrix scan loop (&D5C0, IN A,(&F9)
//     / IN E,(&FE)) before entering BASIC/the editor. We model one held keypress
//     at the keyboard matrix to dismiss the WTFK, RELEASE it once the editor idle
//     is reached, then inject 'x' the same way — so the editor sees only the 'x'.
//     This is a genuine stock-vs-Colin difference (Colin auto-boots; stock waits),
//     handled faithfully, not papered over.
//
// FRAME INTERRUPT. Both runs use Entry.FrameIntPeriod to fire the SAM's 50 Hz
// maskable frame interrupt through the real CPU path (the genuine &0038 handler
// runs, populating LASTK/FLAGS/FRAMES) — identical for both runs, so it does not
// bias the diff. The minimal i257 EI;HALT-resume still applies between interrupts.
//
// CAVEAT (frame-int handler vs stale sysvars). Because both runs fire the same
// real frame interrupt, frame-driven sysvars (FRAMES &5C78 etc.) advance in both;
// any residual stale/zero sysvar is identical on both sides and so cancels in the
// diff — but the report flags suspicious all-zero sysvar bands so they cannot
// silently confound Q1/Q2.
//
// PROPRIETARY INPUTS (never committed): rom_stock_v30.bin, rom.bin, eeprom.bin
// under ~/sam-archive/samboot-capture (or $SAMBOOT_CAPTURE_DIR). Absence is a
// HARD FAILURE unless SKIP_PRIVATE_TESTS=true (the one sanctioned skip, i253).
//
// RUN: cd tools/netboot-oracle && go run ./cmd/samboot-statediff
package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

// Named sysvars the experiment calls out by name (Q1 + Q2).
const (
	addrFLAGS  = 0x5C3B // Q1: status flags, bit 7
	addrTVFLAG = 0x5C3C // Q1: TV flags, bit 5
	addrTVDATA = 0x5BBE // Q2: temporary colour/attribute (Colin pokes &10)
	addrNSPPC  = 0x5C44 // Q2: next-statement PPC (Colin pokes &FF)
)

// The sysvar band that decides Q1/Q2: the SAM second-sysvar area (&5800..) plus
// the Spectrum-style sysvars (&5C00..&5CFF). Enumerated byte-by-byte in the report.
const (
	sysvarLo = 0x5800
	sysvarHi = 0x5CFF
)

// Editor sync constants (verified empirically; see the file-level comment).
const (
	editorFlagsPoll  = 0x050E // editor idle FLAGS-poll PC (key-available check)
	editorKeyConsume = 0x0514 // the deterministic sync PC: LASTK read + FLAGS bit5 clear
)

const (
	frameIntPeriod = 20000     // instructions between 50 Hz frame interrupts
	bootStepCap    = 6_000_000 // generous; both runs reach the sync PC well within this
	bootLMPR       = 0x40      // reset paging: ROM0 at section A, ROM1 at section D
	bootHMPR       = 0x00
	romBytes       = 32768
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "samboot-statediff:", err)
		os.Exit(1)
	}
}

func run() error {
	dir := captureDir()

	stockSnap, err := snapshotStock(dir)
	if err != nil {
		return fmt.Errorf("stock v3.0 snapshot: %w", err)
	}
	colinSnap, err := snapshotColin(dir)
	if err != nil {
		return fmt.Errorf("Colin fork snapshot: %w", err)
	}

	report(stockSnap, colinSnap)

	// Pete's ultimate Q2 test: Colin with vs without his teardown pokes.
	colinNoPokes, err := snapshotColinNoPokes(dir)
	if err != nil {
		return fmt.Errorf("Colin-no-pokes snapshot: %w", err)
	}
	reportPokeTest(colinSnap, colinNoPokes)

	if err := saveAndFullDiff(stockSnap, colinSnap); err != nil {
		return fmt.Errorf("save snapshots / full diff: %w", err)
	}
	return nil
}

// snapshot captures the run outcome plus the 64 KB logical RAM image at the sync PC.
type snapshot struct {
	label     string
	reachedPC uint16
	reached   bool
	steps     uint64
	lmpr      byte // LMPR (&FA) at the snapshot — explains the &C000/&8000 region diffs
	hmpr      byte // HMPR (&FB) at the snapshot
	mem       [65536]byte
}

// snapshotColin boots Colin's fork (rom.bin + device-linear eeprom.bin) to the
// editor idle, injects 'x', and snapshots RAM at the key-consume sync PC.
func snapshotColin(dir string) (*snapshot, error) {
	rom, err := requireCapture(dir, "rom.bin")
	if err != nil {
		return nil, err
	}
	if rom == nil {
		return &snapshot{label: "colin", reached: false}, errSkipped
	}
	eep, err := requireCapture(dir, "eeprom.bin")
	if err != nil {
		return nil, err
	}
	mac := z80h.New()
	if err := mac.LoadROMImage(rom); err != nil {
		return nil, err
	}
	mac.Pager().LMPR = bootLMPR
	mac.Pager().HMPR = bootHMPR
	enc := z80h.NewENC28J60()
	enc.LoadEEPROMImage(deviceLinearEEPROM(eep))
	mac.AttachIO(enc)

	return runToSyncPC(mac, "colin", false)
}

// snapshotColinNoPokes is Pete's "ultimate" Q2 test (2026-06-25): boot Colin's fork
// with his two teardown pokes NOPped out, so we can compare WITH-pokes vs WITHOUT-
// pokes directly. In the bootblock teardown (chunk 1, run at &40A9), `LD (&5C44),A`
// = NSPPC=&FF and `LD (&5BBE),A` = TVDATA=&10. chunk-1 file offset F maps to device
// &2000+F, so the two stores sit at device &20B1 (NSPPC) and &20B6 (TVDATA) in the
// device-linear image. We replace ONLY the two store opcodes with NOPs (same length,
// no address shift; the LD-A immediates remain, harmless) and assert the bytes are
// exactly the expected pokes before NOPping — never guess at the site.
func snapshotColinNoPokes(dir string) (*snapshot, error) {
	rom, err := requireCapture(dir, "rom.bin")
	if err != nil {
		return nil, err
	}
	if rom == nil {
		return &snapshot{label: "colin-nopokes", reached: false}, errSkipped
	}
	eep, err := requireCapture(dir, "eeprom.bin")
	if err != nil {
		return nil, err
	}
	dev := deviceLinearEEPROM(eep)
	for _, p := range []struct {
		off  int
		want []byte
		name string
	}{
		{0x20B1, []byte{0x32, 0x44, 0x5C}, "NSPPC (LD (&5C44),A)"},
		{0x20B6, []byte{0x32, 0xBE, 0x5B}, "TVDATA (LD (&5BBE),A)"},
	} {
		if got := dev[p.off : p.off+len(p.want)]; !bytes.Equal(got, p.want) {
			return nil, fmt.Errorf("colin-nopokes: device &%04X = % X, want % X (%s poke site moved — refusing to NOP the wrong bytes)",
				p.off, got, p.want, p.name)
		}
		for i := range p.want {
			dev[p.off+i] = 0x00 // NOP
		}
	}
	mac := z80h.New()
	if err := mac.LoadROMImage(rom); err != nil {
		return nil, err
	}
	mac.Pager().LMPR = bootLMPR
	mac.Pager().HMPR = bootHMPR
	enc := z80h.NewENC28J60()
	enc.LoadEEPROMImage(dev)
	mac.AttachIO(enc)
	return runToSyncPC(mac, "colin-nopokes", false)
}

// reportPokeTest runs Pete's ultimate Q2 test: Colin WITH his teardown pokes vs the
// same boot with them NOPped out. Both share identical paging (same fork), so a
// full-64KB diff isolates EXACTLY the pokes' effect. Byte-identical ⇒ the pokes are
// redundant (the value is reached anyway) ⇒ the inject can leave them out.
func reportPokeTest(colin, noPokes *snapshot) {
	fmt.Println("--- Q2 ULTIMATE TEST: Colin WITH vs WITHOUT his NSPPC/TVDATA pokes ---")
	if !noPokes.reached {
		fmt.Println("  (colin-nopokes did not reach the sync PC — inconclusive)")
		fmt.Println()
		return
	}
	fmt.Printf("  sync PC: with=%s  without=%s  (same=%v)\n",
		hex4(colin.reachedPC), hex4(noPokes.reachedPC), colin.reachedPC == noPokes.reachedPC)
	for _, n := range []struct {
		name string
		addr uint16
	}{{"NSPPC", addrNSPPC}, {"TVDATA", addrTVDATA}} {
		w, wo := colin.mem[n.addr], noPokes.mem[n.addr]
		mark := "SAME"
		if w != wo {
			mark = "DIFFERS"
		}
		fmt.Printf("    %-7s %s : pokes-in=&%02X  pokes-out=&%02X   %s\n", n.name, hex4(n.addr), w, wo, mark)
	}
	total := 0
	var first []uint16
	for a := 0; a < 65536; a++ {
		if colin.mem[a] != noPokes.mem[a] {
			total++
			if len(first) < 24 {
				first = append(first, uint16(a))
			}
		}
	}
	if total == 0 {
		fmt.Println("  RESULT: BYTE-IDENTICAL across all 64 KB with vs without the pokes.")
		fmt.Println("          → the pokes are REDUNDANT; the inject can LEAVE THEM OUT.")
	} else {
		fmt.Printf("  RESULT: %d byte(s) differ (with vs without). First: ", total)
		for _, a := range first {
			fmt.Printf("%s ", hex4(a))
		}
		fmt.Println("\n          → the pokes change the end state; the inject must KEEP them.")
	}
	fmt.Println()
}

// snapshotStock boots the stock v3.0 ROM (no EEPROM device), dismisses its boot
// WTFK with a held matrix key, injects 'x', and snapshots RAM at the same sync PC.
func snapshotStock(dir string) (*snapshot, error) {
	rom, err := requireCapture(dir, "rom_stock_v30.bin")
	if err != nil {
		return nil, err
	}
	if rom == nil {
		return &snapshot{label: "stock", reached: false}, errSkipped
	}
	mac := z80h.New()
	if err := mac.LoadROMImage(rom); err != nil {
		return nil, err
	}
	mac.Pager().LMPR = bootLMPR
	mac.Pager().HMPR = bootHMPR
	// No EEPROM device: stock has no Trinity. IN on the Trinity ports floats 0xFF.

	return runToSyncPC(mac, "stock", true)
}

// runToSyncPC drives a prepared machine from reset to the editor key-consume sync
// PC after injecting 'x', and snapshots 64 KB. needWTFK=true (stock) holds a
// keyboard-matrix key to pass the boot WTFK, releasing it once the editor idle is
// reached so the editor sees only the injected 'x'.
func runToSyncPC(mac *z80h.Machine, label string, needWTFK bool) (*snapshot, error) {
	const (
		injectAfterPolls = 50 // FLAGS-poll iterations to settle the editor (matrix released) before injecting 'x'
		wtfkHoldFromStep = 1_200_000
	)
	snap := &snapshot{label: label}

	wtfkReleased := !needWTFK // Colin: nothing to release
	matrixDown := false
	injected := false
	pollsSinceReady := 0
	step := 0

	res, err := mac.RunBootFrom(0x0000, z80h.Entry{
		StepCap:        bootStepCap,
		FrameIntPeriod: frameIntPeriod,
		StopPC:         editorKeyConsume,
		StopPCSkip:     0, // stop at the FIRST key-consume after 'x' is injected
		Trace: func(pc uint16) {
			step++
			// Stock: hold a matrix key once deep in the boot WTFK so the banner
			// wait completes and the editor is entered. Released below.
			if needWTFK && !wtfkReleased && !injected && step > wtfkHoldFromStep {
				if !matrixDown {
					mac.SetKeyMatrix(0x00) // any key, held
					matrixDown = true
				}
			}
			if pc == editorFlagsPoll {
				// First time the editor idle FLAGS-poll runs: release the WTFK key.
				if needWTFK && !wtfkReleased {
					mac.SetKeyMatrix(0xFF) // release
					wtfkReleased = true
					pollsSinceReady = 0
				}
				if wtfkReleased {
					pollsSinceReady++
					// Inject 'x' once the editor is settled and the matrix released.
					if !injected && pollsSinceReady >= injectAfterPolls {
						mac.InjectKeys([]byte{'x'})
						injected = true
					}
				}
			}
		},
	})
	if err != nil {
		return nil, err
	}
	// Guard against stopping at &0514 BEFORE the inject (the editor never reaches
	// &0514 without an available key, so res.ReachedStop implies post-inject — but
	// assert injected to be certain the snapshot is at the intended sync point).
	if !injected {
		return nil, fmt.Errorf("%s: never injected 'x' (editor idle FLAGS-poll %s not reached; finalPC=&%04X steps=%d) — the boot did not reach the editor keyboard wait",
			label, hex4(editorFlagsPoll), res.PC, res.Steps)
	}
	if !res.ReachedStop {
		return nil, fmt.Errorf("%s: injected 'x' but did not reach the key-consume sync PC %s (finalPC=&%04X steps=%d) — the editor did not consume the injected key",
			label, hex4(editorKeyConsume), res.PC, res.Steps)
	}
	snap.reached = true
	snap.reachedPC = res.PC
	snap.steps = res.Steps
	snap.lmpr = mac.Pager().LMPR
	snap.hmpr = mac.Pager().HMPR
	// Snapshot all 64 KB of LOGICAL memory at the sync PC (post-paging, read via
	// the harness memory interface at the live paging state — consistent for both).
	for a := 0; a < 65536; a += 256 {
		copy(snap.mem[a:], mac.Read(uint16(a), 256))
	}
	return snap, nil
}

// saveAndFullDiff persists both raw 64 KB snapshots (so they can be inspected /
// diffed with any tool) and writes the COMPLETE byte-by-byte diff of all 64 KB —
// not just the sysvar band — to a file, so nothing is summarised away. The big
// &8000..&FFFF differences are largely paging / B-DOS-residency, which the captured
// LMPR/HMPR make explicit. Outputs land in the git-ignored build/ dir.
func saveAndFullDiff(stock, colin *snapshot) error {
	outDir := filepath.Join("..", "..", "build")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", outDir, err)
	}
	stockPath := filepath.Join(outDir, "samboot-statediff-stock.bin")
	colinPath := filepath.Join(outDir, "samboot-statediff-colin.bin")
	if err := os.WriteFile(stockPath, stock.mem[:], 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(colinPath, colin.mem[:], 0o644); err != nil {
		return err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "SAMBOOT full RAM diff (all 64 KB) — stock v3.0 vs Colin fork, at sync PC %s\n", hex4(editorKeyConsume))
	fmt.Fprintf(&b, "paging at snapshot: stock LMPR=&%02X HMPR=&%02X | colin LMPR=&%02X HMPR=&%02X\n",
		stock.lmpr, stock.hmpr, colin.lmpr, colin.hmpr)
	fmt.Fprintf(&b, "(&8000..&FFFF differences are largely paging / B-DOS-residency — see LMPR/HMPR)\n\n")
	total := 0
	regionTotals := map[string]int{}
	regionOf := func(a int) string {
		switch {
		case a <= 0x3FFF:
			return "0000-3FFF low/ROM-shadow"
		case a <= 0x57FF:
			return "4000-57FF screen/workspace"
		case a <= 0x5CFF:
			return "5800-5CFF sysvars"
		case a <= 0x7FFF:
			return "5D00-7FFF BASIC/editor work"
		case a <= 0xBFFF:
			return "8000-BFFF (Colin B-DOS resident)"
		default:
			return "C000-FFFF (ROM1/top RAM)"
		}
	}
	for a := 0; a < 65536; a++ {
		if stock.mem[a] != colin.mem[a] {
			fmt.Fprintf(&b, "%s : stock=&%02X  colin=&%02X%s\n", hex4(uint16(a)), stock.mem[a], colin.mem[a], sysvarName(uint16(a)))
			total++
			regionTotals[regionOf(a)]++
		}
	}
	fmt.Fprintf(&b, "\nTOTAL differing bytes: %d / 65536\n", total)
	diffPath := filepath.Join(outDir, "samboot-statediff-full.txt")
	if err := os.WriteFile(diffPath, []byte(b.String()), 0o644); err != nil {
		return err
	}

	absOf := func(p string) string {
		if a, e := filepath.Abs(p); e == nil {
			return a
		}
		return p
	}
	fmt.Println("--- Full snapshots + COMPLETE diff written --------------------")
	fmt.Printf("  paging at sync PC:  stock LMPR=&%02X HMPR=&%02X | colin LMPR=&%02X HMPR=&%02X\n",
		stock.lmpr, stock.hmpr, colin.lmpr, colin.hmpr)
	fmt.Printf("  stock snapshot (64 KB): %s\n", absOf(stockPath))
	fmt.Printf("  colin snapshot (64 KB): %s\n", absOf(colinPath))
	fmt.Printf("  full byte-by-byte diff: %s  (%d differing bytes total)\n", absOf(diffPath), total)
	fmt.Println("  per-region differing-byte totals:")
	for _, r := range []string{"0000-3FFF low/ROM-shadow", "4000-57FF screen/workspace", "5800-5CFF sysvars",
		"5D00-7FFF BASIC/editor work", "8000-BFFF (Colin B-DOS resident)", "C000-FFFF (ROM1/top RAM)"} {
		fmt.Printf("    %-34s %d\n", r, regionTotals[r])
	}
	fmt.Println("  inspect directly:  cmp -l <stock.bin> <colin.bin>  (or any hex differ)")
	return nil
}

// report emits the diff: named Q1/Q2 bytes, the full sysvar-band diff, and a
// region-level summary of the rest.
func report(stock, colin *snapshot) {
	fmt.Println("================================================================")
	fmt.Println("SAMBOOT stock-v3.0 vs Colin-fork RAM-diff (decides i258 Q1/Q2)")
	fmt.Println("================================================================")
	fmt.Println()
	fmt.Printf("sync point: both runs stop at the editor key-consume PC %s (LASTK read +\n", hex4(editorKeyConsume))
	fmt.Printf("            FLAGS bit5 clear) after an injected key 'x'.\n")
	fmt.Printf("  stock: reachedSyncPC=%v finalPC=%s steps=%d\n", stock.reached, hex4(stock.reachedPC), stock.steps)
	fmt.Printf("  colin: reachedSyncPC=%v finalPC=%s steps=%d\n", colin.reached, hex4(colin.reachedPC), colin.steps)
	samePC := stock.reached && colin.reached && stock.reachedPC == colin.reachedPC
	fmt.Printf("  BOTH reached the SAME sync PC: %v\n", samePC)
	fmt.Println()

	// Named Q1/Q2 bytes.
	fmt.Println("--- Named sysvars (Q1 + Q2) — stock vs Colin -------------------")
	named := []struct {
		name string
		addr uint16
		note string
	}{
		{"FLAGS", addrFLAGS, "Q1: bit7"},
		{"TVFLAG", addrTVFLAG, "Q1: bit5"},
		{"TVDATA", addrTVDATA, "Q2: Colin pokes &10"},
		{"NSPPC", addrNSPPC, "Q2: Colin pokes &FF"},
	}
	for _, n := range named {
		s := stock.mem[n.addr]
		c := colin.mem[n.addr]
		mark := "  "
		if s != c {
			mark = "**"
		}
		fmt.Printf("  %s %-7s %s = stock:&%02X  colin:&%02X   (%s)\n",
			mark, n.name, hex4(n.addr), s, c, n.note)
	}
	fmt.Println("  (** = differs)")
	fmt.Println()

	// Full sysvar-band diff.
	fmt.Printf("--- Sysvar-band byte diff %s..%s (the Q1/Q2 band) --------\n", hex4(sysvarLo), hex4(sysvarHi))
	type d struct {
		addr uint16
		s, c byte
	}
	var diffs []d
	for a := sysvarLo; a <= sysvarHi; a++ {
		if stock.mem[a] != colin.mem[a] {
			diffs = append(diffs, d{uint16(a), stock.mem[a], colin.mem[a]})
		}
	}
	if len(diffs) == 0 {
		fmt.Println("  (no differences in the sysvar band)")
	} else {
		fmt.Printf("  %d differing byte(s):\n", len(diffs))
		for _, x := range diffs {
			fmt.Printf("    %s : stock=&%02X  colin=&%02X%s\n", hex4(x.addr), x.s, x.c, sysvarName(x.addr))
		}
	}
	fmt.Println()

	// Suspicious all-zero sysvar flag (caveat guard).
	zStock := allZero(stock.mem[sysvarLo : sysvarHi+1])
	zColin := allZero(colin.mem[sysvarLo : sysvarHi+1])
	if zStock || zColin {
		fmt.Printf("  CAVEAT: sysvar band all-zero?  stock=%v colin=%v — if true the boot did not\n", zStock, zColin)
		fmt.Println("          populate sysvars and the diff is unreliable. Investigate before deciding.")
		fmt.Println()
	}

	// Region summary for the rest (not enumerated byte-by-byte).
	fmt.Println("--- Region summary (rest of RAM, NOT byte-enumerated) ----------")
	regions := []struct {
		name   string
		lo, hi int
	}{
		{"low RAM / ROM-shadow workspace &0000..&3FFF", 0x0000, 0x3FFF},
		{"screen + low workspace &4000..&57FF", 0x4000, 0x57FF},
		{"sysvar band &5800..&5CFF (enumerated above)", 0x5800, 0x5CFF},
		{"BASIC/editor work area &5D00..&7FFF", 0x5D00, 0x7FFF},
		{"&8000..&BFFF (Colin: B-DOS resident; stock: free)", 0x8000, 0xBFFF},
		{"&C000..&FFFF (ROM1 / top RAM)", 0xC000, 0xFFFF},
	}
	for _, r := range regions {
		n := 0
		for a := r.lo; a <= r.hi; a++ {
			if stock.mem[a] != colin.mem[a] {
				n++
			}
		}
		span := r.hi - r.lo + 1
		fmt.Printf("  %-52s %6d / %d bytes differ\n", r.name, n, span)
	}
	fmt.Println()
	fmt.Println("NOTE: &8000..&BFFF and screen/work areas differ by design (Colin has B-DOS")
	fmt.Println("resident + a different boot path); those are expected/ignorable. The SYSVAR")
	fmt.Println("BAND above is the payload that decides Q1/Q2. This tool measures; it does not")
	fmt.Println("decide — the orchestrator analyses the sysvar diff and settles Q1/Q2.")
}

func allZero(b []byte) bool {
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}

// sysvarName annotates a few well-known sysvar addresses inline in the diff.
func sysvarName(a uint16) string {
	known := map[uint16]string{
		addrTVDATA: "  <- TVDATA (Q2)",
		addrFLAGS:  "  <- FLAGS (Q1)",
		addrTVFLAG: "  <- TVFLAG (Q1)",
		addrNSPPC:  "  <- NSPPC (Q2)",
		0x5C78:     "  <- FRAMES lo (frame counter)",
		0x5C79:     "  <- FRAMES mid",
		0x5C7A:     "  <- FRAMES hi",
	}
	if s, ok := known[a]; ok {
		return s
	}
	return ""
}

func hex4(v uint16) string {
	const d = "0123456789ABCDEF"
	return "&" + string([]byte{d[(v>>12)&0xf], d[(v>>8)&0xf], d[(v>>4)&0xf], d[v&0xf]})
}

// captureDir resolves the proprietary-capture directory.
func captureDir() string {
	if d := os.Getenv("SAMBOOT_CAPTURE_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "sam-archive", "samboot-capture")
}

// errSkipped signals the SKIP_PRIVATE_TESTS sanctioned skip path.
var errSkipped = fmt.Errorf("skipped: SKIP_PRIVATE_TESTS=true and a proprietary capture is absent")

// requireCapture reads a proprietary capture. Absence is a HARD FAILURE unless
// SKIP_PRIVATE_TESTS=true (the one sanctioned skip, i253 / no-silent-skips). When
// skipping, it returns (nil, nil) and the caller emits the skip notice.
func requireCapture(dir, name string) ([]byte, error) {
	p := filepath.Join(dir, name)
	b, err := os.ReadFile(p)
	if err != nil {
		if os.Getenv("SKIP_PRIVATE_TESTS") == "true" {
			fmt.Fprintf(os.Stderr, "samboot-statediff: SKIP_PRIVATE_TESTS=true: proprietary capture %q absent — nothing to diff\n", name)
			os.Exit(0)
		}
		return nil, fmt.Errorf("proprietary capture %q absent under %s (set $SAMBOOT_CAPTURE_DIR, or SKIP_PRIVATE_TESTS=true): %w", name, dir, err)
	}
	if name != "eeprom.bin" && len(b) != romBytes {
		return nil, fmt.Errorf("%s is %d bytes, want %d (ROM0+ROM1)", name, len(b), romBytes)
	}
	return b, nil
}

// deviceLinearEEPROM un-rotates the CAPTURED eeprom.bin into a true device-linear
// image: the dumper reads by chunk number, chunk 1 lives at device &2000, so
// file offset F holds device byte (F+&2000) mod N. The Trinity SPI model addresses
// the device linearly, so the capture must be un-rotated before loading. (Mirrors
// the z80 test helper of the same name; kept local so this cmd has no test-pkg dep.)
func deviceLinearEEPROM(captured []byte) []byte {
	const dataBase = 0x2000
	n := len(captured)
	out := make([]byte, n)
	for d := 0; d < n; d++ {
		out[d] = captured[((d-dataBase)%n+n)%n]
	}
	return out
}
