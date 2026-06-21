// compact_ir_test.go — host-verification of src/test_compact_ir.asm (i48c-b8b:
// the Z80 compact-core walk, the NON-ENCODER body of assemble.Compact).
//
// The Z80 routine compact_ir_walk runs the b8a pass1 walk (RecordPC captured)
// then walks the same IR record stream a second time emitting the compact-core
// outputs that need no instruction encoder: COMMENT/BLANK_RUN → editor sidecar
// rows, `.global` → name_id list, constant-data directives → KindLitData runs
// (split at 1016 bytes), LABEL_DEF/LOCAL_DEF dropped, and INST records reduced
// to skeleton KindInsnRun elements (placeholder base word, no patches). This
// test drives it under the flat koron-go/z80 harness and asserts the
// non-encoder outputs match the host authority assemble.Compact.
//
// Authority: frontend.Translate → assemble.Pass1 → assemble.Compact. The Z80
// input is the SAME records serializeIR (pass1_ir_test.go) produces, so both
// sides see byte-identical input. Compact's INST elements carry real base words
// + overlay patches (i48c-b8e's territory); this test compares only the
// non-encoder fields — sidecar rows, globals, KindLitData records, and the
// instruction-element COUNT / record-stream structure — never INST base/patches.
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
	cBin = "../../../build/test_compact_ir.bin"
	cMap = "../../../build/test_compact_ir.map"
)

// Fixed compact-core RAM addresses. These are `equ` constants in
// src/test_compact_ir.asm (in the &F000-&FFFF free block, kept out of the code
// image), so they do not appear in the pyz80 mapfile. They are stable by the
// same contract the b8a table addresses rely on; a drift here vs. the .asm is a
// silent corruption, so the .asm RAM map documents the identical addresses.
const (
	addrCompactSidecar      = 0xF200
	addrCompactGlobals      = 0xF500
	addrCompactRecs         = 0xFA40
	addrCompactSidecarCount = 0xFF80
	addrCompactGlobalsCount = 0xFF82
	addrCompactRecsLen      = 0xFF84
)

// Compact-core buffer capacities (mirroring the *_CAP equates in
// src/test_compact_ir.asm and the COMPACT_REC_PC cap in test_pass1_ir.asm). A
// fixture whose Z80 output would exceed any of these is out of harness scope and
// skipped (like the IR-buffer size skip in pass1_ir_test.go) — not a compact-core
// gap. The buffers live in the 4 KB &F000-&FFFF free block, so they are sized for
// the common corpus + the hand fixtures, not the worst case.
const (
	compactRecPCRecords = 128
	compactSidecarCap   = 768
	compactGlobalsCap   = 128
	compactDataRunCap   = 1216
	compactRecsCap      = 1344
)

// z80SidecarBytes is the on-wire size of the Z80 COMPACT_SIDECAR buffer for these
// rows (row = [kind u8][anchor u32]; comment tail = [placement u8][len u16][body];
// blank tail = [run_len u32]).
func z80SidecarBytes(rows []format.SidecarRow) int {
	n := 0
	for _, r := range rows {
		n += 1 + 4
		if r.Kind == format.SidecarBlank {
			n += 4
		} else {
			n += 1 + 2 + len(r.Comment.Body)
		}
	}
	return n
}

// z80DataRunBytes is the largest single open constant-data run the Z80
// COMPACT_DATA_RUN must hold before a flush. It is computed from the host's
// compacted record stream: consecutive KindLitData records of the SAME directive
// id form one logical run (the split into ≤1016-byte records is exactly what the
// Z80 flush does), so the peak run = max sum of LitData bytes over a maximal
// same-dir LitData stretch. Any record kind other than that same-dir LitData run
// breaks the stretch.
func z80DataRunBytes(t *testing.T, stream []byte) int {
	t.Helper()
	peak, run := 0, 0
	haveDir := false
	curDir := byte(0)
	i := 0
	for i < len(stream) {
		kind := format.RecordKind(stream[i])
		plen := int(binary.LittleEndian.Uint16(stream[i+1:]))
		payload := stream[i+3 : i+3+plen]
		i += 3 + plen
		if kind == format.KindLitData {
			dir := payload[0]
			if haveDir && dir != curDir {
				if run > peak {
					peak = run
				}
				run = 0
			}
			haveDir = true
			curDir = dir
			run += len(payload) - 1
			continue
		}
		if run > peak {
			peak = run
		}
		run = 0
		haveDir = false
	}
	if run > peak {
		peak = run
	}
	return peak
}

// z80RecsBytes is the size the Z80 compacted record stream occupies. It equals
// the host stream's non-InsnRun records (LitData / Directive copied verbatim) plus
// the Z80's mode-0 InsnRun framing for each coalesced instruction run: a run of n
// elements becomes ceil(n/253) frames, each [kind][len:2][mode] + 4*n_frame bytes.
func z80RecsBytes(t *testing.T, stream []byte) int {
	t.Helper()
	total := 0
	i := 0
	insnRun := 0
	flushInsn := func() {
		if insnRun == 0 {
			return
		}
		for insnRun > 0 {
			n := insnRun
			if n > 253 {
				n = 253
			}
			total += 3 + 1 + 4*n // header + mode + words
			insnRun -= n
		}
	}
	for i < len(stream) {
		kind := format.RecordKind(stream[i])
		plen := int(binary.LittleEndian.Uint16(stream[i+1:]))
		payload := stream[i+3 : i+3+plen]
		i += 3 + plen
		if kind == format.KindInsnRun {
			insnRun += countInsnElements(t, payload)
			continue
		}
		flushInsn()
		total += 3 + plen
	}
	flushInsn()
	return total
}

func loadCompactIR(t *testing.T) *z80h.Machine {
	t.Helper()
	if _, err := os.Stat(cBin); err != nil {
		t.Fatalf("compact-ir binary not built (%s); run `make compact-ir-z80`", cBin)
	}
	mac, err := z80h.Load(cBin, cMap)
	if err != nil {
		t.Fatalf("load compact-ir: %v", err)
	}
	return mac
}

// runCompactIR loads the IR buffer, runs compact_ir_walk, and returns the
// machine for output read-back. A non-clean halt (the fail trap) fails the test.
func runCompactIR(t *testing.T, mac *z80h.Machine, ir []byte) {
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
		t.Skipf("IR %d bytes exceeds the %d-byte PASS1_IR_BUF (out of harness scope)", len(ir), pass1IRBufSize)
	}
	mac.Write(bufAddr, ir)
	mac.WriteU16LE(lenAddr, uint16(len(ir)))

	res, err := mac.CallEntry("compact_ir_walk", z80h.Entry{})
	if err != nil {
		t.Fatalf("compact_ir_walk: %v", err)
	}
	if !res.Halted {
		t.Fatalf("compact_ir_walk did not return cleanly (PC=&%04X)", res.PC)
	}
	failHalt, _ := mac.Sym("fail_halt")
	if res.PC == failHalt {
		tagAddr, _ := mac.Sym("p1ir_fail_tag")
		tag := mac.Read(tagAddr, 1)[0]
		t.Fatalf("compact_ir_walk hit the fail trap (tag &%02X)", tag)
	}
}

// z80Sidecar reads COMPACT_SIDECAR: [count u16] then per row in source order
// [kind u8][anchor u32 LE] and a kind tail (comment: [placement u8][len u16][body];
// blank: [run_len u32 LE]). Returns []format.SidecarRow in stored (source) order.
func z80Sidecar(t *testing.T, mac *z80h.Machine) []format.SidecarRow {
	t.Helper()
	count := int(binary.LittleEndian.Uint16(mac.Read(uint16(addrCompactSidecarCount), 2)))
	addr := uint16(addrCompactSidecar)
	rows := make([]format.SidecarRow, 0, count)
	for i := 0; i < count; i++ {
		kind := mac.Read(addr, 1)[0]
		addr++
		anchor := int64(int32(binary.LittleEndian.Uint32(mac.Read(addr, 4))))
		addr += 4
		switch format.SidecarKind(kind) {
		case format.SidecarBlank:
			runLen := binary.LittleEndian.Uint32(mac.Read(addr, 4))
			addr += 4
			rows = append(rows, format.SidecarRow{Kind: format.SidecarBlank, Blank: format.BlankRun{Anchor: anchor, RunLen: runLen}})
		case format.SidecarComment:
			placement := mac.Read(addr, 1)[0]
			addr++
			blen := int(binary.LittleEndian.Uint16(mac.Read(addr, 2)))
			addr += 2
			body := mac.Read(addr, blen)
			addr += uint16(blen)
			rows = append(rows, format.SidecarRow{Kind: format.SidecarComment, Comment: format.CommentRow{Anchor: anchor, Placement: placement, Body: body}})
		default:
			t.Fatalf("z80 sidecar row %d: unknown kind %d", i, kind)
		}
	}
	return rows
}

// z80Globals reads COMPACT_GLOBALS: [count u16] then name_id u16 LE entries in
// append order.
func z80Globals(t *testing.T, mac *z80h.Machine) []uint16 {
	t.Helper()
	count := int(binary.LittleEndian.Uint16(mac.Read(uint16(addrCompactGlobalsCount), 2)))
	out := make([]uint16, count)
	addr := uint16(addrCompactGlobals)
	for i := 0; i < count; i++ {
		out[i] = binary.LittleEndian.Uint16(mac.Read(addr, 2))
		addr += 2
	}
	return out
}

// z80Recs reads the compacted record stream (compact_recs_len bytes at
// COMPACT_RECS).
func z80Recs(t *testing.T, mac *z80h.Machine) []byte {
	t.Helper()
	n := int(binary.LittleEndian.Uint16(mac.Read(uint16(addrCompactRecsLen), 2)))
	return mac.Read(uint16(addrCompactRecs), n)
}

// litDataRec is one KindLitData record (dir_id + raw bytes) parsed from a record
// stream — the non-encoder constant-data output compared exactly between sides.
type litDataRec struct {
	dirID byte
	bytes []byte
}

// compactView is the non-encoder projection of a compact record stream: the
// ordered sequence of "blocks" (an InsnRun run coalesced to its element count, a
// LitData record, or a pass-through Directive). INST base words / patches are NOT
// captured — i48c-b8e's territory.
type compactView struct {
	blocks []viewBlock
}

type viewBlock struct {
	kind     string // "insn", "litdata", "directive"
	insnN    int    // element count for "insn"
	lit      litDataRec
	dirBytes []byte // verbatim DIRECTIVE record bytes for "directive"
}

// parseCompactView walks a compact record stream, coalescing consecutive
// KindInsnRun records into one "insn" block (summing element counts across mode-0
// and mode-1 frames) so the host's frame-packing choice doesn't affect the
// comparison. KindLitData and KindDirective become their own blocks.
func parseCompactView(t *testing.T, stream []byte) compactView {
	t.Helper()
	var v compactView
	i := 0
	for i < len(stream) {
		kind := format.RecordKind(stream[i])
		plen := int(binary.LittleEndian.Uint16(stream[i+1:]))
		payload := stream[i+3 : i+3+plen]
		i += 3 + plen
		switch kind {
		case format.KindInsnRun:
			n := countInsnElements(t, payload)
			if len(v.blocks) > 0 && v.blocks[len(v.blocks)-1].kind == "insn" {
				v.blocks[len(v.blocks)-1].insnN += n
			} else {
				v.blocks = append(v.blocks, viewBlock{kind: "insn", insnN: n})
			}
		case format.KindLitData:
			v.blocks = append(v.blocks, viewBlock{kind: "litdata", lit: litDataRec{dirID: payload[0], bytes: append([]byte(nil), payload[1:]...)}})
		case format.KindDirective:
			v.blocks = append(v.blocks, viewBlock{kind: "directive", dirBytes: append([]byte(nil), payload...)})
		default:
			t.Fatalf("parseCompactView: unexpected record kind %s", kind.Name())
		}
	}
	return v
}

// countInsnElements counts the elements in one KindInsnRun payload
// [mode u8] then, mode 0: [word u32]*; mode 1: [word u32][patch_count u8]
// [slot u8, expr_len u8, expr]*.
func countInsnElements(t *testing.T, payload []byte) int {
	t.Helper()
	mode := payload[0]
	p := 1
	n := 0
	switch mode {
	case 0:
		for p < len(payload) {
			p += 4
			n++
		}
	case 1:
		for p < len(payload) {
			p += 4 // base word
			pc := int(payload[p])
			p++
			for k := 0; k < pc; k++ {
				p++ // slot
				elen := int(payload[p])
				p++
				p += elen
			}
			n++
		}
	default:
		t.Fatalf("countInsnElements: unknown mode %d", mode)
	}
	return n
}

// checkCompactFixture is the per-source assertion: Translate → host Pass1 →
// host Compact (expected), and serializeIR → Z80 compact_ir_walk (got),
// compared on sidecar rows, globals, and the non-encoder record-stream view.
func checkCompactFixture(t *testing.T, name string, src []byte) {
	t.Helper()
	f, err := frontend.Translate(src, name)
	if err != nil {
		t.Fatalf("%s: Translate: %v", name, err)
	}
	p1, err := assemble.Pass1(f)
	if err != nil {
		t.Fatalf("%s: host Pass1: %v", name, err)
	}
	// A non-zero OriginVMA IS in scope: the Z80 compact-core captures OriginVMA's
	// low word (ORIGIN_LOW, set by the seeding `.org`) and computes the sidecar
	// anchor as RecordPC - OriginVMA, exactly as host Compact (compact.go:109/129).
	wantRecs, wantSidecar, wantGlobals, err := assemble.Compact(f, p1)
	if err != nil {
		t.Fatalf("%s: host Compact: %v", name, err)
	}
	if len(f.Records) > compactRecPCRecords {
		t.Skipf("%s: %d records exceeds the %d-record COMPACT_REC_PC cap (out of harness scope)", name, len(f.Records), compactRecPCRecords)
	}
	if n := z80SidecarBytes(wantSidecar); n > compactSidecarCap {
		t.Skipf("%s: sidecar %d bytes exceeds the %d-byte COMPACT_SIDECAR cap (comment-heavy fixture, out of harness scope)", name, n, compactSidecarCap)
	}
	if n := len(wantGlobals) * 2; n > compactGlobalsCap {
		t.Skipf("%s: globals %d bytes exceeds the %d-byte COMPACT_GLOBALS cap (out of harness scope)", name, n, compactGlobalsCap)
	}
	if n := z80DataRunBytes(t, wantRecs); n > compactDataRunCap {
		t.Skipf("%s: data run %d bytes exceeds the %d-byte COMPACT_DATA_RUN cap (out of harness scope)", name, n, compactDataRunCap)
	}
	if n := z80RecsBytes(t, wantRecs); n > compactRecsCap {
		t.Skipf("%s: record stream %d bytes exceeds the %d-byte COMPACT_RECS cap (out of harness scope)", name, n, compactRecsCap)
	}

	ir := serializeIR(t, f)
	mac := loadCompactIR(t)
	runCompactIR(t, mac, ir)

	// 1) Sidecar rows — exact, in source order (host returns sidecarOut in source
	// order; WriteFile sorts later, so order here is the source order both sides
	// preserve).
	gotSidecar := z80Sidecar(t, mac)
	compareSidecar(t, name, gotSidecar, wantSidecar)

	// 2) Globals — exact, in append (record) order.
	gotGlobals := z80Globals(t, mac)
	if !uint16SlicesEqual(gotGlobals, wantGlobals) {
		t.Errorf("%s: globals = %v, host Compact = %v", name, gotGlobals, wantGlobals)
	}

	// 3) Record stream — the non-encoder view: block sequence + LitData bytes +
	// pass-through directive bytes + instruction element counts (NOT INST base
	// words / patches).
	gotView := parseCompactView(t, z80Recs(t, mac))
	wantView := parseCompactView(t, wantRecs)
	compareView(t, name, gotView, wantView)
}

func compareSidecar(t *testing.T, name string, got, want []format.SidecarRow) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s: %d sidecar rows, host Compact %d", name, len(got), len(want))
		return
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.Kind != w.Kind {
			t.Errorf("%s: sidecar[%d] kind = %d, host %d", name, i, g.Kind, w.Kind)
			continue
		}
		switch w.Kind {
		case format.SidecarBlank:
			if g.Blank.Anchor != w.Blank.Anchor || g.Blank.RunLen != w.Blank.RunLen {
				t.Errorf("%s: sidecar[%d] blank = {anchor %d, run %d}, host {anchor %d, run %d}",
					name, i, g.Blank.Anchor, g.Blank.RunLen, w.Blank.Anchor, w.Blank.RunLen)
			}
		case format.SidecarComment:
			if g.Comment.Anchor != w.Comment.Anchor || g.Comment.Placement != w.Comment.Placement || !bytesEqual(g.Comment.Body, w.Comment.Body) {
				t.Errorf("%s: sidecar[%d] comment = {anchor %d, place %d, body %q}, host {anchor %d, place %d, body %q}",
					name, i, g.Comment.Anchor, g.Comment.Placement, g.Comment.Body,
					w.Comment.Anchor, w.Comment.Placement, w.Comment.Body)
			}
		}
	}
}

func compareView(t *testing.T, name string, got, want compactView) {
	t.Helper()
	if len(got.blocks) != len(want.blocks) {
		t.Errorf("%s: %d record blocks, host Compact %d (got %s, want %s)",
			name, len(got.blocks), len(want.blocks), blockKinds(got), blockKinds(want))
		return
	}
	for i := range want.blocks {
		g, w := got.blocks[i], want.blocks[i]
		if g.kind != w.kind {
			t.Errorf("%s: block[%d] kind = %s, host %s", name, i, g.kind, w.kind)
			continue
		}
		switch w.kind {
		case "insn":
			if g.insnN != w.insnN {
				t.Errorf("%s: block[%d] insn elements = %d, host %d", name, i, g.insnN, w.insnN)
			}
		case "litdata":
			if g.lit.dirID != w.lit.dirID || !bytesEqual(g.lit.bytes, w.lit.bytes) {
				t.Errorf("%s: block[%d] litdata = {dir %d, %x}, host {dir %d, %x}",
					name, i, g.lit.dirID, g.lit.bytes, w.lit.dirID, w.lit.bytes)
			}
		case "directive":
			if !bytesEqual(g.dirBytes, w.dirBytes) {
				t.Errorf("%s: block[%d] directive = %x, host %x", name, i, g.dirBytes, w.dirBytes)
			}
		}
	}
}

func blockKinds(v compactView) string {
	s := "["
	for i, b := range v.blocks {
		if i > 0 {
			s += " "
		}
		s += b.kind
	}
	return s + "]"
}

func uint16SlicesEqual(a, b []uint16) bool {
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

// TestCompactIRCoreFixtures runs the compact-core walk over hand sources
// exercising comments, blank runs, .global, data directives (incl. the 1016-byte
// split), and a mix, plus the corpus, asserting the Z80 non-encoder outputs
// match assemble.Compact.
func TestCompactIRCoreFixtures(t *testing.T) {
	hand := map[string]string{
		// A trailing comment + a standalone comment around instructions.
		"comments": "  mov x0, x1 // trailing\n// standalone\n  ret\n",
		// Blank runs interleaved with comments (source order at a shared anchor).
		"blanks": "  mov x0, x1\n\n\n// after blanks\n  ret\n",
		// .global with two clean symbol refs → name_id list, no record.
		"global": ".global foo\n.global bar\nfoo:\n  ret\nbar:\n  ret\n",
		// Constant data directives of each width → LitData runs.
		"data_byte": ".byte 1,2,3,4,5\n  ret\n",
		"data_word": ".word 0x11223344, 0xdeadbeef\n  ret\n",
		"data_quad": ".quad 0x1122334455667788\n  ret\n",
		// Mixed same/different directive runs (a .word run then a .byte run must
		// flush between them on the dir-id change).
		"data_mixed": ".word 1,2\n.word 3\n.byte 9\n.byte 8,7\n  ret\n",
		// A data run split by a label (flushData at the label boundary).
		"data_label_split": ".word 1,2\nlbl:\n.word 3,4\n  ret\n",
		// Labels + locals are dropped from the record stream.
		"labels_dropped": "a:\n1:\n  mov x0, x1\n  b 1b\nb:\n  ret\n",
		// .global whose operand is NOT a clean symbol ref stays a directive
		// (here .global of an expression isn't producible from text, so this is
		// covered structurally by the corpus; a plain instruction run only here).
		"insn_run": "  mov x0, x1\n  add x1, x2, x3\n  sub x4, x5, x6\n  ret\n",
		// The 1016-byte LitData split: 255 .word operands = 1020 bytes → two
		// records (1016 + 4) on whole-element (4-byte) boundaries.
		"data_split": wordsFixture(255),
		// Non-zero origin: a leading `.org 0x1000` seeds OriginVMA, so each
		// record's RecordPC is its VMA and the sidecar anchor is RecordPC -
		// OriginVMA (= the byte offset from origin). The trailing comment anchors
		// at offset 4 (one instruction past origin), proving the subtraction.
		"org_small": ".org 0x1000\n  mov x0, x1\n// at offset 4\n  ret\n",
		// Kernel-VMA origin (the spectrum4 link origin's shape): OriginVMA's low
		// word is 0, so RecordPC.low - OriginVMA.low still yields the offset (the
		// high words cancel). The comment again anchors at offset 4.
		"org_high": ".org 0xfffffff000000000\n  mov x0, x1\n// kernel-vma offset 4\n  ret\n",
		// `. =` alias for `.org`, with a comment BEFORE the origin-setting line —
		// its RecordPC (0) is below OriginVMA, so the anchor clamps to 0.
		"org_dot_eq": "// before origin\n. = 0x8000\n  mov x0, x1\n  ret\n",
	}
	names := make([]string, 0, len(hand))
	for n := range hand {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		t.Run("hand/"+n, func(t *testing.T) {
			checkCompactFixture(t, n, []byte(hand[n]))
		})
	}

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
					t.Skipf("%s: Translate error (out of compact-ir fixture scope): %v", e.Name(), err)
				}
				checkCompactFixture(t, e.Name(), src)
			})
		}
	}
}

// wordsFixture builds a `.word` directive with n constant operands followed by a
// `ret`, producing n*4 data bytes — used to exercise the 1016-byte LitData split.
func wordsFixture(n int) string {
	s := ".word "
	for i := 0; i < n; i++ {
		if i > 0 {
			s += ","
		}
		s += "0x10000000"
	}
	return s + "\n  ret\n"
}
