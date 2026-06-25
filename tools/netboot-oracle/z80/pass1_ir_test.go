// pass1_ir_test.go — host-verification of src/test_pass1_ir.asm (i48c-b8a: the
// Z80 Pass1-over-IR walk).
//
// The Z80 routine pass1_ir_walk walks the in-memory IR record stream (the same
// records src/asmparse.asm parse_run emits) and computes, for a fixture: each
// LABEL_DEF's name->PC, each LOCAL_DEF's digit->PC list, each `.equ`/`.set`
// value, and the literal-pool slot placement. This test drives it under the
// flat koron-go/z80 harness and asserts the resolved tables byte-match the host
// authority assemble.Pass1 over the same records.
//
// Authority: tools/sam-aarch64/frontend.Translate produces the *format.File IR
// (LABEL_DEF/LOCAL_DEF carried as records, header tables empty); assemble.Pass1
// over that File is the expected. The Z80 input is the SAME records serialised
// into parse_run's wire layout (serializeIR below mirrors the framing the
// asmparse_test.go reader documents), so both sides see byte-identical input.
package z80_test

import (
	"encoding/binary"
	"os"
	"sort"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
	assemble "github.com/petemoore/sam-aarch64/tools/sam-aarch64/assemble"
	frontend "github.com/petemoore/sam-aarch64/tools/sam-aarch64/frontend"
)

const (
	p1Bin = "../../../build/test_pass1_ir.bin"
	p1Map = "../../../build/test_pass1_ir.map"
)

// Fixed table addresses. These are `equ` constants in src/test_pass1_ir.asm
// (mirroring src/assembler.asm), so they do not appear in the pyz80 mapfile
// (which carries labels + data symbols only). They are stable by the same
// contract the integrated assembler relies on; a drift here vs. the .asm is a
// silent corruption, so the .asm header documents the identical map.
const (
	addrLITPOOLTable    = 0xD200
	addrLITPOOLCount    = 0xD3C0
	addrLocalLabelTable = 0xE280
)

// pass1IRBufSize matches the PASS1_IR_BUF reservation in src/test_pass1_ir.asm.
const pass1IRBufSize = 10752

// pass1KnownOversize is the reviewed exclude-list of corpus fixtures whose
// serialised IR is genuinely larger than the SAM's real on-chip PASS1_IR_BUF
// (pass1IRBufSize) — a true on-hardware limit, NOT a pass1 bug or a test gap.
// The flat harness cannot process them because the SAM itself cannot. Each
// entry is name -> the reason (which cap, with the observed size). A NEW corpus
// fixture that exceeds the cap but is absent here is a t.Fatalf (it must be
// reviewed and added, never silently skipped); a stale entry (never hit) is a
// t.Errorf at end of the corpus sweep. See i253 (no-silent-skips).
var pass1KnownOversize = map[string]string{
	"in_long_source.s": "IR ~16484 B exceeds the 10752-byte PASS1_IR_BUF (comment-heavy paged fixture)",
}

// pass1KnownTranslateOOS is the reviewed exclude-list of corpus fixtures that
// frontend.Translate cannot handle in isolation (include search paths, etc.) —
// a fixture-scope issue, not a pass1 gap. Empty today: every corpus fixture
// Translates standalone. A NEW fixture that errors but is absent here is a
// t.Fatalf; a stale entry is a t.Errorf.
var pass1KnownTranslateOOS = map[string]string{}

// pass1OversizeSeen / pass1TranslateOOSSeen record which exclude-list entries
// the corpus sweep actually hit, so a stale (never-encountered) entry can be
// flagged after the sweep.
var (
	pass1OversizeSeen     = map[string]bool{}
	pass1TranslateOOSSeen = map[string]bool{}
)

func loadPass1IR(t *testing.T) *z80h.Machine {
	t.Helper()
	if _, err := os.Stat(p1Bin); err != nil {
		t.Fatalf("pass1-ir binary not built (%s); run `make pass1-ir-z80`", p1Bin)
	}
	mac, err := z80h.Load(p1Bin, p1Map)
	if err != nil {
		t.Fatalf("load pass1-ir: %v", err)
	}
	return mac
}

// serializeIR turns a File's records into the parse_run wire stream that
// pass1_ir_walk consumes. Framing mirrors src/asmparse.asm (and the
// asmparse_test.go reader):
//
//	INST:      [REC_KIND_INST][mnem_id:2 LE][op_count:1][ops_len:2 LE][ops]
//	LABEL_DEF: [REC_KIND_LABEL_DEF][len:2 LE = 2][sym_id:2 LE]
//	LOCAL_DEF: [REC_KIND_LOCAL_DEF][len:2 LE = 1][digit:1]
//	DIRECTIVE: [REC_KIND_DIRECTIVE][len:2 LE][dir_id:1][op_count:1][operands]
//	COMMENT:   [REC_KIND_COMMENT][len:2 LE][placement:1][body]
//	BLANK_RUN: [REC_KIND_BLANK_RUN][len:2 LE][run_len:4 LE]
//
// Only the record kinds Translate produces for the assembler-facing prefix are
// emitted; the pass1 walk skips COMMENT/BLANK_RUN (no PC effect).
func serializeIR(t *testing.T, f *format.File) []byte {
	t.Helper()
	var out []byte
	u16 := func(v uint16) []byte { b := make([]byte, 2); binary.LittleEndian.PutUint16(b, v); return b }
	for _, rec := range f.Records {
		switch rec.Kind {
		case format.KindInst:
			out = append(out, byte(format.KindInst))
			out = append(out, u16(rec.MnemonicID)...)
			out = append(out, rec.OperandCount)
			out = append(out, u16(uint16(len(rec.Operands)))...)
			out = append(out, rec.Operands...)
		case format.KindLabelDef:
			out = append(out, byte(format.KindLabelDef))
			out = append(out, u16(2)...)
			out = append(out, u16(rec.SymbolID)...)
		case format.KindLocalDef:
			out = append(out, byte(format.KindLocalDef))
			out = append(out, u16(1)...)
			out = append(out, rec.Digit)
		case format.KindDirective:
			payload := []byte{rec.DirectiveID, rec.OperandCount}
			payload = append(payload, rec.Operands...)
			out = append(out, byte(format.KindDirective))
			out = append(out, u16(uint16(len(payload)))...)
			out = append(out, payload...)
		case format.KindComment:
			payload := append([]byte{rec.Placement}, rec.Body...)
			out = append(out, byte(format.KindComment))
			out = append(out, u16(uint16(len(payload)))...)
			out = append(out, payload...)
		case format.KindBlankRun:
			b := make([]byte, 4)
			binary.LittleEndian.PutUint32(b, rec.RunLen)
			out = append(out, byte(format.KindBlankRun))
			out = append(out, u16(4)...)
			out = append(out, b...)
		default:
			t.Fatalf("serializeIR: unsupported record kind %s in fixture", rec.Kind.Name())
		}
	}
	return out
}

// runPass1IR loads the IR buffer into the harness, runs pass1_ir_walk, and
// returns the machine for table read-back. A non-clean halt (the fail trap)
// fails the test with the parked tag.
func runPass1IR(t *testing.T, mac *z80h.Machine, ir []byte) {
	t.Helper()
	bufAddr, err := mac.Sym("PASS1_IR_BUF")
	if err != nil {
		t.Fatal(err)
	}
	lenAddr, err := mac.Sym("PASS1_IR_LEN")
	if err != nil {
		t.Fatal(err)
	}
	if len(ir) > pass1IRBufSize {
		// checkFixture pre-screens oversize fixtures against pass1KnownOversize
		// before reaching here, so any IR that arrives oversize is a programming
		// error in the caller, not a fixture-scope skip.
		t.Fatalf("runPass1IR reached with oversize IR (%d > %d) — checkFixture should have screened it", len(ir), pass1IRBufSize)
	}
	mac.Write(bufAddr, ir)
	mac.WriteU16LE(lenAddr, uint16(len(ir)))

	res, err := mac.CallEntry("pass1_ir_walk", z80h.Entry{})
	if err != nil {
		t.Fatalf("pass1_ir_walk: %v", err)
	}
	if !res.Halted {
		t.Fatalf("pass1_ir_walk did not return cleanly (PC=&%04X)", res.PC)
	}
	// A fail/fail_with_tag trap halts at fail_halt, not the harness RET-trap.
	failHalt, _ := mac.Sym("fail_halt")
	if res.PC == failHalt {
		tagAddr, _ := mac.Sym("p1ir_fail_tag")
		tag := mac.Read(tagAddr, 1)[0]
		t.Fatalf("pass1_ir_walk hit the fail trap (tag &%02X)", tag)
	}
}

// lookupZ80Symbol pre-seeds symbol_value_buf with a sentinel, runs
// symbol_lookup, and reports hit/value by whether the buffer changed. This is
// the reliable hit/miss signal without F-register access.
func lookupZ80Symbol(t *testing.T, mac *z80h.Machine, id uint16) (int64, bool) {
	t.Helper()
	valAddr, _ := mac.Sym("symbol_value_buf")
	sentinel := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	mac.Write(valAddr, sentinel)
	if _, err := mac.CallEntry("symbol_lookup", z80h.Entry{HL: id}); err != nil {
		t.Fatalf("symbol_lookup(%d): %v", id, err)
	}
	b := mac.Read(valAddr, 4)
	if b[0] == 0xDE && b[1] == 0xAD && b[2] == 0xBE && b[3] == 0xEF {
		return 0, false // buffer untouched => miss
	}
	return int64(int32(binary.LittleEndian.Uint32(b))), true
}

// readZ80Locals reads the LOCAL_LABEL_TABLE: [count:2 LE][digit:1, pc:4 LE]*.
// Returns digit -> ordered list of PCs (definition order, as appended).
func readZ80Locals(t *testing.T, mac *z80h.Machine) map[byte][]int64 {
	t.Helper()
	base := uint16(addrLocalLabelTable)
	count := int(binary.LittleEndian.Uint16(mac.Read(base, 2)))
	out := map[byte][]int64{}
	addr := base + 2
	for i := 0; i < count; i++ {
		e := mac.Read(addr, 5)
		digit := e[0]
		pc := int64(int32(binary.LittleEndian.Uint32(e[1:])))
		out[digit] = append(out[digit], pc)
		addr += 5
	}
	return out
}

// z80PoolEntry mirrors the LITPOOL_TABLE entry fields the host PoolEntry needs:
// width and the final entry_pc (set at flush). expr_ptr points into the
// section-D copy, so the bytes are read from there for the dedup-key compare.
type z80PoolEntry struct {
	width   byte
	entryPC int64
	expr    []byte
}

// readZ80Pool reads LITPOOL_TABLE (LITPOOL_COUNT entries, 14-byte stride):
// +0 width, +1 expr_ptr u16, +3 expr_len u16, +9 entry_pc u32 LE.
func readZ80Pool(t *testing.T, mac *z80h.Machine) []z80PoolEntry {
	t.Helper()
	base := uint16(addrLITPOOLTable)
	count := int(mac.Read(uint16(addrLITPOOLCount), 1)[0])
	var out []z80PoolEntry
	for i := 0; i < count; i++ {
		e := mac.Read(base+uint16(i*14), 14)
		exprPtr := binary.LittleEndian.Uint16(e[1:])
		exprLen := int(binary.LittleEndian.Uint16(e[3:]))
		entryPC := int64(int32(binary.LittleEndian.Uint32(e[9:])))
		out = append(out, z80PoolEntry{
			width:   e[0],
			entryPC: entryPC,
			expr:    mac.Read(exprPtr, exprLen),
		})
	}
	return out
}

// nameToID inverts file.Names to map a symbol name back to its interned id.
func nameToID(f *format.File) map[string]uint16 {
	m := map[string]uint16{}
	for id, name := range f.Names {
		m[name] = uint16(id)
	}
	return m
}

// checkFixture is the per-source assertion: Translate -> host Pass1 (expected)
// and serializeIR -> Z80 pass1_ir_walk (got), compared on Symbols, LocalDefs,
// and PoolEntries.
func checkFixture(t *testing.T, name string, src []byte) {
	t.Helper()
	f, err := frontend.Translate(src, name)
	if err != nil {
		t.Fatalf("%s: Translate: %v", name, err)
	}
	want, err := assemble.Pass1(f)
	if err != nil {
		t.Fatalf("%s: host Pass1: %v", name, err)
	}

	ir := serializeIR(t, f)

	// Oversize screen: an IR larger than the SAM's real PASS1_IR_BUF cannot be
	// processed on hardware — a genuine limit, not a pass1 gap. Such a fixture
	// must be a REVIEWED entry in pass1KnownOversize (then we just don't run it);
	// a NEW oversize fixture absent from the list fails hard so it gets reviewed
	// (no silent skip — i253).
	if len(ir) > pass1IRBufSize {
		if _, known := pass1KnownOversize[name]; known {
			pass1OversizeSeen[name] = true
			return
		}
		t.Fatalf("%s: IR %d bytes exceeds the %d-byte PASS1_IR_BUF and is NOT in pass1KnownOversize — review and add it (with the cap+size) or shrink the fixture", name, len(ir), pass1IRBufSize)
	}

	// A fresh machine per fixture so the tables start empty.
	freshMac := loadPass1IR(t)
	runPass1IR(t, freshMac, ir)

	ids := nameToID(f)

	// Symbols: every name the host resolved must resolve to the same low-32 bits
	// on the Z80 (SYMTAB stores 32 bits; the fixture corpus is origin 0, so the
	// low word is the full value).
	for symName, wantVal := range want.Symbols {
		id, ok := ids[symName]
		if !ok {
			t.Errorf("%s: symbol %q has no interned id in file.Names", name, symName)
			continue
		}
		gotVal, hit := lookupZ80Symbol(t, freshMac, id)
		if !hit {
			t.Errorf("%s: symbol %q (id %d) not found in Z80 SYMTAB (want %d)", name, symName, id, wantVal)
			continue
		}
		if int32(gotVal) != int32(wantVal) {
			t.Errorf("%s: symbol %q = %d, host Pass1 = %d (low32)", name, symName, int32(gotVal), int32(wantVal))
		}
	}

	// LocalDefs: digit -> PC list, compared in definition order.
	gotLocals := readZ80Locals(t, freshMac)
	for digit, wantPCs := range want.LocalDefs {
		gotPCs := gotLocals[digit]
		if len(gotPCs) != len(wantPCs) {
			t.Errorf("%s: local digit %d: %d defs, host Pass1 %d (%v vs %v)",
				name, digit, len(gotPCs), len(wantPCs), gotPCs, wantPCs)
			continue
		}
		for i := range wantPCs {
			if int32(gotPCs[i]) != int32(wantPCs[i]) {
				t.Errorf("%s: local digit %d def[%d] = %d, host Pass1 = %d",
					name, digit, i, int32(gotPCs[i]), int32(wantPCs[i]))
			}
		}
	}
	for digit := range gotLocals {
		if _, ok := want.LocalDefs[digit]; !ok {
			t.Errorf("%s: Z80 has local digit %d the host Pass1 does not", name, digit)
		}
	}

	// PoolEntries: width + final placed PC (entry_pc) + dedup-key expr, in emit
	// order. The host PoolEntries[i].PC is the placed PC; the Z80 entry_pc is the
	// same after the end-of-input flush.
	gotPool := readZ80Pool(t, freshMac)
	if len(gotPool) != len(want.PoolEntries) {
		t.Errorf("%s: %d pool entries, host Pass1 %d", name, len(gotPool), len(want.PoolEntries))
	} else {
		for i := range want.PoolEntries {
			w := want.PoolEntries[i]
			g := gotPool[i]
			if g.width != w.Width {
				t.Errorf("%s: pool[%d] width = %d, host %d", name, i, g.width, w.Width)
			}
			if int32(g.entryPC) != int32(w.PC) {
				t.Errorf("%s: pool[%d] PC = %d, host %d", name, i, int32(g.entryPC), int32(w.PC))
			}
			if !bytesEqual(g.expr, w.Expr) {
				t.Errorf("%s: pool[%d] expr = %x, host %x", name, i, g.expr, w.Expr)
			}
		}
	}
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestPass1IRCoreFixtures runs the pass1-over-IR walk over the simple core
// fixtures (and a couple of hand sources exercising labels/locals/.equ/litpool)
// and asserts the Z80 tables match host assemble.Pass1.
func TestPass1IRCoreFixtures(t *testing.T) {
	// Hand sources covering the four record kinds with PC effects.
	hand := map[string]string{
		"labels": "start:\n  mov x0, x1\n  add x1, x2, x3\nloop:\n  b loop\n",
		"locals": "1:\n  mov x0, x1\n  b 1b\n2:\n  add x0, x0, x1\n  b 2b\n",
		"equ":    ".equ FOO, 0x10\n.set BAR, FOO+4\n  mov x0, x1\n",
		"data":   ".byte 1,2,3\n.word 0xdeadbeef\nlbl:\n  mov x0, x1\n",
		"mixed":  "a:\n  mov x0, x1\n1:\n  add x1, x1, #1\n.equ N, 8\n  b 1b\nb:\n  ret\n",
		// litpool: a single 8-byte slot flushed implicitly at end-of-input.
		"lit_one": "  ldr x0, =0xdeadbeef\n  ret\n",
		// litpool: a 4-byte and an 8-byte slot, explicitly flushed by .ltorg
		// (exercises width partitioning + alignment padding + the flush PC).
		"lit_ltorg": "  ldr w0, =0x1234\n  ldr x1, =0xdeadbeef\n  .ltorg\n  ret\n",
		// litpool with a symbolic slot expr (=label) — non-constant bytecode.
		"lit_sym": "  ldr x0, =label\nlabel:\n  ret\n",
		// litpool dedup: two ldr of the same value share one slot.
		"lit_dedup": "  ldr x0, =0xaaaa\n  ldr x1, =0xaaaa\n  ret\n",
	}
	names := make([]string, 0, len(hand))
	for n := range hand {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		t.Run("hand/"+n, func(t *testing.T) {
			checkFixture(t, n, []byte(hand[n]))
		})
	}

	// Corpus fixture sources, across the assembler test tiers. A fixture
	// frontend.Translate can't handle in isolation (include search paths, etc.)
	// is a fixture-scope issue, not a pass1 bug — but it must be a REVIEWED entry
	// in pass1KnownTranslateOOS, never a silent skip (i253). Oversize-against-the
	// -SAM-buffer fixtures are screened inside checkFixture against
	// pass1KnownOversize.
	for _, dir := range []string{"core", "format", "operands", "symbols", "paged"} {
		srcDir := "../../../tests/" + dir + "/sources"
		entries, err := os.ReadDir(srcDir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || len(e.Name()) < 2 || e.Name()[len(e.Name())-2:] != ".s" {
				continue
			}
			path := srcDir + "/" + e.Name()
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			t.Run(dir+"/"+e.Name(), func(t *testing.T) {
				if _, err := frontend.Translate(src, e.Name()); err != nil {
					if _, known := pass1KnownTranslateOOS[e.Name()]; known {
						pass1TranslateOOSSeen[e.Name()] = true
						return
					}
					t.Fatalf("%s: Translate error and NOT in pass1KnownTranslateOOS — review and add it or fix Translate: %v", e.Name(), err)
				}
				checkFixture(t, e.Name(), src)
			})
		}
	}

	// Stale-entry guard: every exclude-list entry must have been encountered by
	// the corpus sweep, else the list has drifted (the fixture was renamed,
	// removed, or shrank below the cap) and should be pruned.
	for name := range pass1KnownOversize {
		if !pass1OversizeSeen[name] {
			t.Errorf("pass1KnownOversize entry %q was never encountered — stale, prune it", name)
		}
	}
	for name := range pass1KnownTranslateOOS {
		if !pass1TranslateOOSSeen[name] {
			t.Errorf("pass1KnownTranslateOOS entry %q was never encountered — stale, prune it", name)
		}
	}
}
