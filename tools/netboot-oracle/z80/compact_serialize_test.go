// compact_serialize_test.go — host-verification of src/compact_serialize.asm
// (i48c-b8c: the Z80 compact v2 .tbn serializer) and of the compact_emit.asm
// INSN_RUN frame-packer boundary splits (the PR 827 review follow-up).
//
// Surface 1 — the serializer: the Z80 routine compact_serialize is the port of
// format.WriteFile + the table encoders it calls (writeLabelTable /
// writeLocalTable / appendNameTable / appendGlobalFlags / appendSidecar). Per
// fixture, every input is derived from the Go authority (frontend.Translate →
// assemble.Pass1 → assemble.Compact, plus the headerRows derivation below) and
// written into the flat harness in the b8b walk's buffer formats; the produced
// bytes must equal assemble.CompactTBNBytes byte-for-byte.
//
// Surface 2 — the frame packer: cemit_flush_inst (src/compact_emit.asm) is
// driven over PRE-CLASSIFIED element runs written straight into CEMIT_ELEMS
// (the packer needs no ENCTAB — only classification does), which is what makes
// the >253-element mode-0 split and the >1016-byte mode-1 payload split
// exercisable: the paged section-D payload's 256-byte element window cannot
// hold a 254-element run. The element lists and the expected frame bytes both
// come from assemble.Compact over real translated sources, so the Z80 packing
// must reproduce the authority's framing exactly.
package z80_test

import (
	"bytes"
	"encoding/binary"
	"math"
	"os"
	"sort"
	"strings"
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
	assemble "github.com/petemoore/sam-aarch64/tools/sam-aarch64/assemble"
	frontend "github.com/petemoore/sam-aarch64/tools/sam-aarch64/frontend"
)

const (
	serBin = "../../../build/test_compact_ser.bin"
	serMap = "../../../build/test_compact_ser.map"
)

// Fixed buffer addresses, mirroring the equ map in src/test_compact_ser.asm
// (equ constants do not appear in the pyz80 mapfile; the .asm header documents
// the identical layout — a drift here is a silent corruption, the same
// contract as the b8a/b8b table addresses).
const (
	serCemitElems    = 0x0800
	serCemitElemsEnd = 0x1E00
	serCemitOut      = 0x1E00
	serCemitOutEnd   = 0x2E00
	serLabelRowsBuf  = 0x3000
	serLocalRowsBuf  = 0x3300
	serGlobalsBuf    = 0x3600
	serNamesBuf      = 0x3700
	serSidecarBuf    = 0x3F00
	serRecsBuf       = 0x4700
	serOutBuf        = 0x5200
	serOutBufEnd     = 0x6E00
)

// Buffer capacities (bytes unless noted). A fixture that exceeds one is out of
// harness scope: it must be a REVIEWED serKnownOversize entry (then it just
// doesn't run), never a silent skip (i253).
const (
	serLabelRowsCap = 128 // rows (6 B each)
	serLocalRowsCap = 128 // rows (5 B each)
	serGlobalsCap   = 128 // ids (2 B each)
	serNamesCap     = serSidecarBuf - serNamesBuf
	serSidecarCap   = serRecsBuf - serSidecarBuf
	serRecsCap      = serOutBuf - serRecsBuf
	serOutCap       = serOutBufEnd - serOutBuf
)

// serKnownOversize is the reviewed exclude-list of corpus fixtures that exceed
// one of the caps above (name -> which cap, with the observed size). A NEW
// oversize fixture absent from the list is a t.Fatalf; a stale entry is a
// t.Errorf after the sweep. See i253 (no-silent-skips).
var serKnownOversize = map[string]string{
	"in_long_source.s": "sidecar ~16539 B exceeds the 2048-byte SERBUF_SIDECAR cap (comment-heavy paged fixture)",
}

// serKnownTranslateOOS is the reviewed exclude-list of corpus fixtures that
// frontend.Translate cannot handle in isolation. Empty today.
var serKnownTranslateOOS = map[string]string{}

var (
	serOversizeSeen     = map[string]bool{}
	serTranslateOOSSeen = map[string]bool{}
)

func loadCompactSer(t *testing.T) *z80h.Machine {
	t.Helper()
	if _, err := os.Stat(serBin); err != nil {
		t.Fatalf("compact-ser binary not built (%s); run `make compact-ser-z80`", serBin)
	}
	mac, err := z80h.Load(serBin, serMap)
	if err != nil {
		t.Fatalf("load compact-ser: %v", err)
	}
	return mac
}

// deriveHeaderRows mirrors assemble's headerRows (compact.go:196-222) in
// CAPTURE order (record order — the order the Z80 walk appends rows), which is
// multiset-equal to headerRows' map-iteration output; the serializer's sort
// puts both into the identical on-disk order. Label offsets use
// p1.Symbols[name] (headerRows' source); local offsets use the def-site record
// PC (the pcs pass1 appends to LocalDefs). NameID is the first-occurrence
// index in f.Names (the interned table has no duplicates, so this is the id
// the records reference).
func deriveHeaderRows(f *format.File, p1 *assemble.Pass1Result) ([]format.LabelRow, []format.LocalRow) {
	nameID := make(map[string]uint16, len(f.Names))
	for i, n := range f.Names {
		if _, seen := nameID[n]; !seen {
			nameID[n] = uint16(i)
		}
	}
	var labels []format.LabelRow
	var locals []format.LocalRow
	for i, rec := range f.Records {
		switch rec.Kind {
		case format.KindLabelDef:
			name := f.Names[rec.SymbolID]
			labels = append(labels, format.LabelRow{
				NameID: nameID[name],
				Offset: p1.Symbols[name] - p1.OriginVMA,
			})
		case format.KindLocalDef:
			locals = append(locals, format.LocalRow{
				Digit:  rec.Digit,
				Offset: p1.RecordPC[i] - p1.OriginVMA,
			})
		}
	}
	return labels, locals
}

// serLabelRowsWire encodes label rows in the b8b capture format the serializer
// consumes: [name_id:2 LE][offset:4 LE] per row (COMPACT_LABELROWS,
// src/test_compact_ir.asm). Offsets must already be screened to fit u32.
func serLabelRowsWire(rows []format.LabelRow) []byte {
	out := make([]byte, 0, len(rows)*6)
	for _, r := range rows {
		var b [6]byte
		binary.LittleEndian.PutUint16(b[0:], r.NameID)
		binary.LittleEndian.PutUint32(b[2:], uint32(r.Offset))
		out = append(out, b[:]...)
	}
	return out
}

// serLocalRowsWire encodes local rows: [digit:1][offset:4 LE] per row.
func serLocalRowsWire(rows []format.LocalRow) []byte {
	out := make([]byte, 0, len(rows)*5)
	for _, r := range rows {
		var b [5]byte
		b[0] = r.Digit
		binary.LittleEndian.PutUint32(b[1:], uint32(r.Offset))
		out = append(out, b[:]...)
	}
	return out
}

// serNamesWire encodes the interned name list: [count:2 LE] then per name
// [len:1][bytes]. A name longer than 255 bytes is out of wire range and fails
// hard (none exists in any fixture).
func serNamesWire(t *testing.T, names []string) []byte {
	t.Helper()
	out := make([]byte, 2)
	binary.LittleEndian.PutUint16(out, uint16(len(names)))
	for _, n := range names {
		if len(n) > 255 {
			t.Fatalf("name %q is %d bytes — exceeds the [len:1] name-list wire field", n, len(n))
		}
		out = append(out, byte(len(n)))
		out = append(out, n...)
	}
	return out
}

// serGlobalsWire encodes the globals id list in append (record) order:
// [id:2 LE] per entry — the COMPACT_GLOBALS capture format.
func serGlobalsWire(ids []uint16) []byte {
	out := make([]byte, 0, len(ids)*2)
	for _, id := range ids {
		var b [2]byte
		binary.LittleEndian.PutUint16(b[:], id)
		out = append(out, b[:]...)
	}
	return out
}

// serSidecarWire encodes sidecar rows in the COMPACT_SIDECAR capture format:
// [kind:1][anchor:4 LE] then kind 0 [placement:1][len:2 LE][body] / kind 1
// [run_len:4 LE], in source order (which is anchor-sorted — the serializer
// guards this).
func serSidecarWire(rows []format.SidecarRow) []byte {
	var out []byte
	for _, r := range rows {
		out = append(out, byte(r.Kind))
		var a [4]byte
		binary.LittleEndian.PutUint32(a[:], uint32(r.Anchor()))
		out = append(out, a[:]...)
		if r.Kind == format.SidecarBlank {
			var rl [4]byte
			binary.LittleEndian.PutUint32(rl[:], r.Blank.RunLen)
			out = append(out, rl[:]...)
		} else {
			out = append(out, r.Comment.Placement)
			var l [2]byte
			binary.LittleEndian.PutUint16(l[:], uint16(len(r.Comment.Body)))
			out = append(out, l[:]...)
			out = append(out, r.Comment.Body...)
		}
	}
	return out
}

// serSetCell writes a u16 parameter cell by mapfile symbol.
func serSetCell(t *testing.T, mac *z80h.Machine, sym string, v uint16) {
	t.Helper()
	addr, err := mac.Sym(sym)
	if err != nil {
		t.Fatal(err)
	}
	mac.WriteU16LE(addr, v)
}

// serReadCell reads a u16 cell by mapfile symbol.
func serReadCell(t *testing.T, mac *z80h.Machine, sym string) uint16 {
	t.Helper()
	addr, err := mac.Sym(sym)
	if err != nil {
		t.Fatal(err)
	}
	return binary.LittleEndian.Uint16(mac.Read(addr, 2))
}

// serCheckClean asserts a harness call returned cleanly (not via the fail
// trap), reporting the parked tag otherwise.
func serCheckClean(t *testing.T, mac *z80h.Machine, res z80h.CallResult, what string) {
	t.Helper()
	if !res.Halted {
		t.Fatalf("%s did not return cleanly (PC=&%04X)", what, res.PC)
	}
	failHalt, _ := mac.Sym("fail_halt")
	if res.PC == failHalt {
		tagAddr, _ := mac.Sym("ser_fail_tag")
		tag := mac.Read(tagAddr, 1)[0]
		t.Fatalf("%s hit the fail trap (tag &%02X)", what, tag)
	}
}

// serOversize is the single decision point for a fixture exceeding a harness
// cap: a reviewed serKnownOversize entry is recorded and the caller returns; an
// unknown oversize fixture fails hard (i253 — no silent skips). Returns true
// iff the caller should return.
func serOversize(t *testing.T, name, what string, got, capValue int, capName string) bool {
	t.Helper()
	if _, known := serKnownOversize[name]; known {
		serOversizeSeen[name] = true
		return true
	}
	t.Fatalf("%s: %s %d exceeds the %d %s cap and is NOT in serKnownOversize — review and add it (with the cap+size) or shrink the fixture", name, what, got, capValue, capName)
	return false // unreachable
}

// screenHeaderRowOffsets fails hard on any label/local offset outside u32
// range. A negative offset means a label/local before the origin-seeding
// `.org` — out of the u32 offset contract the b8a/b8b harness carries (there
// is deliberately no exclude-list: no fixture has this shape, and a new one
// must be reviewed, not skipped).
func screenHeaderRowOffsets(t *testing.T, name string, labels []format.LabelRow, locals []format.LocalRow) {
	t.Helper()
	for _, r := range labels {
		if r.Offset < 0 || r.Offset > math.MaxUint32 {
			t.Fatalf("%s: label row (name_id %d) offset %d outside the u32 harness contract — a label before the origin-seeding .org must be reviewed", name, r.NameID, r.Offset)
		}
	}
	for _, r := range locals {
		if r.Offset < 0 || r.Offset > math.MaxUint32 {
			t.Fatalf("%s: local row (digit %d) offset %d outside the u32 harness contract", name, r.Digit, r.Offset)
		}
	}
}

// checkSerializeFixture is the per-source assertion: derive every serializer
// input from the Go authority, run the Z80 compact_serialize over them, and
// byte-compare the produced .tbn against assemble.CompactTBNBytes.
func checkSerializeFixture(t *testing.T, name string, src []byte) {
	t.Helper()
	f, err := frontend.Translate(src, name)
	if err != nil {
		t.Fatalf("%s: Translate: %v", name, err)
	}
	p1, err := assemble.Pass1(f)
	if err != nil {
		t.Fatalf("%s: host Pass1: %v", name, err)
	}
	recs, sidecar, globals, err := assemble.Compact(f, p1)
	if err != nil {
		t.Fatalf("%s: host Compact: %v", name, err)
	}
	want, err := assemble.CompactTBNBytes(f, p1)
	if err != nil {
		t.Fatalf("%s: host CompactTBNBytes: %v", name, err)
	}

	labels, locals := deriveHeaderRows(f, p1)
	screenHeaderRowOffsets(t, name, labels, locals)

	// The name list, interned exactly as CompactTBNBytes interns it
	// (api.go:43-46): f.Names in ID order through a SymbolTable.
	st := format.NewSymbolTable()
	for _, n := range f.Names {
		st.Intern(n)
	}
	names := st.Names()

	// Cap screens (reviewed exclude-list, never a silent skip — i253).
	sidecarWire := serSidecarWire(sidecar)
	namesWire := serNamesWire(t, names)
	if n := len(labels); n > serLabelRowsCap {
		if serOversize(t, name, "label rows", n, serLabelRowsCap, "SERBUF_LABELROWS") {
			return
		}
	}
	if n := len(locals); n > serLocalRowsCap {
		if serOversize(t, name, "local rows", n, serLocalRowsCap, "SERBUF_LOCALROWS") {
			return
		}
	}
	if n := len(globals); n > serGlobalsCap {
		if serOversize(t, name, "global ids", n, serGlobalsCap, "SERBUF_GLOBALS") {
			return
		}
	}
	if n := len(namesWire); n > serNamesCap {
		if serOversize(t, name, "name-list bytes", n, serNamesCap, "SERBUF_NAMES") {
			return
		}
	}
	if n := len(sidecarWire); n > serSidecarCap {
		if serOversize(t, name, "sidecar bytes", n, serSidecarCap, "SERBUF_SIDECAR") {
			return
		}
	}
	if n := len(recs); n > serRecsCap {
		if serOversize(t, name, "record-stream bytes", n, serRecsCap, "SERBUF_RECS") {
			return
		}
	}
	if n := len(want); n > serOutCap {
		if serOversize(t, name, "output bytes", n, serOutCap, "SERBUF_OUT") {
			return
		}
	}

	mac := loadCompactSer(t)
	mac.Write(serLabelRowsBuf, serLabelRowsWire(labels))
	mac.Write(serLocalRowsBuf, serLocalRowsWire(locals))
	mac.Write(serGlobalsBuf, serGlobalsWire(globals))
	mac.Write(serNamesBuf, namesWire)
	mac.Write(serSidecarBuf, sidecarWire)
	mac.Write(serRecsBuf, recs)

	serSetCell(t, mac, "ser_labels_ptr", serLabelRowsBuf)
	serSetCell(t, mac, "ser_labels_count", uint16(len(labels)))
	serSetCell(t, mac, "ser_locals_ptr", serLocalRowsBuf)
	serSetCell(t, mac, "ser_locals_count", uint16(len(locals)))
	serSetCell(t, mac, "ser_globals_ptr", serGlobalsBuf)
	serSetCell(t, mac, "ser_globals_count", uint16(len(globals)))
	serSetCell(t, mac, "ser_names_ptr", serNamesBuf)
	serSetCell(t, mac, "ser_sidecar_ptr", serSidecarBuf)
	serSetCell(t, mac, "ser_sidecar_count", uint16(len(sidecar)))
	serSetCell(t, mac, "ser_recs_ptr", serRecsBuf)
	serSetCell(t, mac, "ser_recs_len", uint16(len(recs)))
	serSetCell(t, mac, "ser_out_base", serOutBuf)
	serSetCell(t, mac, "ser_out_end", serOutBufEnd)

	res, err := mac.CallEntry("compact_serialize", z80h.Entry{})
	if err != nil {
		t.Fatalf("%s: compact_serialize: %v", name, err)
	}
	serCheckClean(t, mac, res, name+": compact_serialize")

	outLen := int(serReadCell(t, mac, "ser_out_len"))
	got := mac.Read(serOutBuf, outLen)
	if !bytes.Equal(got, want) {
		i := 0
		for i < len(got) && i < len(want) && got[i] == want[i] {
			i++
		}
		t.Errorf("%s: serialized .tbn differs from CompactTBNBytes: %d B vs %d B, first divergence at offset %d (got % x..., want % x...)",
			name, len(got), len(want), i, tail(got, i, 8), tail(want, i, 8))
	}
}

// tail returns up to n bytes of b starting at i (for divergence reporting).
func tail(b []byte, i, n int) []byte {
	if i >= len(b) {
		return nil
	}
	end := i + n
	if end > len(b) {
		end = len(b)
	}
	return b[i:end]
}

// TestCompactSerializeTBNFixtures runs the serializer over hand fixtures
// exercising every table encoder (sorted ties, front-coded names incl. an
// empty suffix, globals reordering, mixed sidecars, multi-byte uvarints,
// non-zero origins) plus the corpus, asserting byte-for-byte equality with
// assemble.CompactTBNBytes.
func TestCompactSerializeTBNFixtures(t *testing.T) {
	hand := map[string]string{
		// Empty tables: no labels/locals/globals/comments, a single name-free
		// instruction — every table serialises as a bare zero count.
		"min": "  nop\n",
		// Two labels at ONE offset whose interned ids arrive in descending
		// order (z is interned first via the branch operand), forcing the
		// label-table tie sort to swap the captured rows.
		"labels_tie": "  b z\na:\nz:\n  ret\n",
		// Two local digits defined at the same offset in descending order —
		// the local-table tie sort must swap them.
		"locals_tie": "2:\n1:\n  ret\n",
		// Globals captured in descending id order (bb interned first via the
		// branch), so appendGlobalFlags' ascending sort must reorder.
		"globals_sort": "  b bb\n.global aa\n.global bb\naa:\nbb:\n  ret\n",
		// Front-coding: growing shared prefixes, a name that is a bare prefix
		// of its predecessor (empty suffix), and a prefix reset.
		"names_frontcode": "alpha_one:\n  nop\nalpha_two:\n  nop\nalp:\n  nop\nbeta:\n  ret\n",
		// Comments and blank runs interleaved, including a shared anchor
		// (source order is the tie-break the stable sort preserves).
		"sidecar_mix": "// head\n  mov x0, x1\n\n\n// tail comment\n  ret\n",
		// Offsets/anchors past 127 force multi-byte uvarint deltas.
		"uvarint_multi": ".skip 300\nfar:\n  nop\n// far comment\n  ret\n",
		// A 20000-byte offset needs a 3-byte uvarint delta.
		"uvarint_wide": ".skip 20000\nvfar:\n  ret\n",
		// Non-zero origin: offsets/anchors are origin-relative and the
		// editor_region_offset accounts for the pass-through .org record.
		"org_offset": ".org 0x1000\n  mov x0, x1\n// at 4\nlbl:\n  ret\n",
		// LitData + a litpool load + a label mid-data: a representative
		// record stream copied verbatim between the tables and the editor
		// region.
		"data_mix": ".word 1,2\nlbl:\n.byte 9\n  ldr x0, =0xdeadbeef\n  ret\n",
	}
	names := make([]string, 0, len(hand))
	for n := range hand {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		t.Run("hand/"+n, func(t *testing.T) {
			checkSerializeFixture(t, n, []byte(hand[n]))
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
					if _, known := serKnownTranslateOOS[e.Name()]; known {
						serTranslateOOSSeen[e.Name()] = true
						return
					}
					t.Fatalf("%s: Translate error and NOT in serKnownTranslateOOS — review and add it or fix Translate: %v", e.Name(), err)
				}
				checkSerializeFixture(t, e.Name(), src)
			})
		}
	}

	for name := range serKnownOversize {
		if !serOversizeSeen[name] {
			t.Errorf("serKnownOversize entry %q was never encountered — stale, prune it", name)
		}
	}
	for name := range serKnownTranslateOOS {
		if !serTranslateOOSSeen[name] {
			t.Errorf("serKnownTranslateOOS entry %q was never encountered — stale, prune it", name)
		}
	}
}

// parsePureInsnRun flattens a compact record stream that must consist of
// KindInsnRun records only (the boundary sources are pure instruction runs)
// into its ordered element list.
func parsePureInsnRun(t *testing.T, name string, recs []byte) []format.InsnElement {
	t.Helper()
	var elems []format.InsnElement
	rr := format.NewRecordReader(recs)
	for !rr.AtEnd() {
		rec, err := rr.Next()
		if err != nil {
			t.Fatalf("%s: record reader: %v", name, err)
		}
		if rec.Kind != format.KindInsnRun {
			t.Fatalf("%s: boundary fixture must compact to a pure instruction run, found %s", name, rec.Kind.Name())
		}
		elems = append(elems, rec.Elements...)
	}
	return elems
}

// cemitWireElements serialises elements into the CEMIT_ELEMS storage layout
// (src/compact_emit.asm header): [base:4 LE][patch_count:1] then per patch a
// packed [slot:4|expr_len:4] header (len nibble 15 = real-length-u8 escape)
// + expr bytes — the mode-1 wire layout (i39c).
func cemitWireElements(t *testing.T, elems []format.InsnElement) []byte {
	t.Helper()
	var out []byte
	for _, el := range elems {
		var w [4]byte
		binary.LittleEndian.PutUint32(w[:], el.BaseWord)
		out = append(out, w[:]...)
		if len(el.Patches) > 255 {
			t.Fatalf("element carries %d patches — exceeds the u8 wire field", len(el.Patches))
		}
		out = append(out, byte(len(el.Patches)))
		for _, p := range el.Patches {
			if len(p.Expr) > 255 {
				t.Fatalf("patch expr is %d bytes — exceeds the u8 wire field", len(p.Expr))
			}
			if p.Slot == 0 || p.Slot > 15 {
				t.Fatalf("patch slot %d — outside the 4-bit packed header field", p.Slot)
			}
			if len(p.Expr) < 15 {
				out = append(out, p.Slot<<4|byte(len(p.Expr)))
			} else {
				out = append(out, p.Slot<<4|0x0F, byte(len(p.Expr)))
			}
			out = append(out, p.Expr...)
		}
	}
	return out
}

// cemitWireSize is the on-wire size of one element (insnElementSize,
// compact.go).
func cemitWireSize(el format.InsnElement) int {
	sz := 5
	for _, p := range el.Patches {
		sz += 1 + len(p.Expr)
		if len(p.Expr) >= 15 {
			sz++
		}
	}
	return sz
}

// cemitFrame is one INSN_RUN frame's shape, for the structural guards that
// keep the boundary fixtures honest (a fixture that stops exercising its
// split branch must fail loudly, not fade to trivial coverage).
type cemitFrame struct {
	mode       byte
	elems      int
	payloadLen int
	hasLiteral bool // any patch-free element (mode-1 absorbed literal)
}

func cemitFrames(t *testing.T, recs []byte) []cemitFrame {
	t.Helper()
	var out []cemitFrame
	rr := format.NewRecordReader(recs)
	for !rr.AtEnd() {
		rec, err := rr.Next()
		if err != nil {
			t.Fatalf("frame parse: %v", err)
		}
		if rec.Kind != format.KindInsnRun {
			t.Fatalf("frame parse: unexpected %s", rec.Kind.Name())
		}
		fr := cemitFrame{mode: rec.Mode, elems: len(rec.Elements), payloadLen: len(rec.Raw)}
		for _, el := range rec.Elements {
			if len(el.Patches) == 0 {
				fr.hasLiteral = true
			}
		}
		out = append(out, fr)
	}
	return out
}

// runCemitFlush drives the flat frame packer: cemit_init over [outBase,
// outEnd), the wire elements written into CEMIT_ELEMS, then cemit_flush_inst.
// Returns the emitted bytes. A fail-trap halt fails the test unless wantTag is
// non-zero, in which case the trap must fire with exactly that tag (and nil is
// returned).
func runCemitFlush(t *testing.T, mac *z80h.Machine, elems []format.InsnElement, outBase, outEnd uint16, wantTag byte) []byte {
	t.Helper()
	wire := cemitWireElements(t, elems)
	if len(wire) > serCemitElemsEnd-serCemitElems {
		t.Fatalf("element run is %d B — exceeds the %d-byte flat CEMIT_ELEMS window", len(wire), serCemitElemsEnd-serCemitElems)
	}
	res, err := mac.CallEntry("cemit_init", z80h.Entry{HL: outBase, DE: outEnd})
	if err != nil {
		t.Fatalf("cemit_init: %v", err)
	}
	serCheckClean(t, mac, res, "cemit_init")
	mac.Write(serCemitElems, wire)
	serSetCell(t, mac, "cemit_elem_ptr", uint16(serCemitElems+len(wire)))
	serSetCell(t, mac, "cemit_elem_count", uint16(len(elems)))

	res, err = mac.CallEntry("cemit_flush_inst", z80h.Entry{})
	if err != nil {
		t.Fatalf("cemit_flush_inst: %v", err)
	}
	if wantTag != 0 {
		failHalt, _ := mac.Sym("fail_halt")
		if !res.Halted || res.PC != failHalt {
			t.Fatalf("cemit_flush_inst returned cleanly (PC=&%04X) — expected the fail trap with tag &%02X", res.PC, wantTag)
		}
		tagAddr, _ := mac.Sym("ser_fail_tag")
		if tag := mac.Read(tagAddr, 1)[0]; tag != wantTag {
			t.Fatalf("cemit_flush_inst fail tag = &%02X, want &%02X", tag, wantTag)
		}
		return nil
	}
	serCheckClean(t, mac, res, "cemit_flush_inst")
	outPtr := serReadCell(t, mac, "cemit_out_ptr")
	return mac.Read(outBase, int(outPtr-outBase))
}

// TestCompactEmitBoundarySplits gives the compact_emit split branches the
// runtime coverage the PR 827 review required: a >253-element patch-free run
// (the mode-0 253-word frame split, compact.go:311-321), a >1016-byte overlay
// run (the mode-1 payload split, compact.go:323-338), and a mixed run
// (absorbed short literal gaps + a litBreak-length gap splitting to mode-0).
// Elements and expected frame bytes both derive from assemble.Compact over
// real translated sources; structural guards pin each fixture to the shape
// that actually exercises its branch.
func TestCompactEmitBoundarySplits(t *testing.T) {
	cases := []struct {
		name  string
		src   string
		guard func(t *testing.T, frames []cemitFrame, elems []format.InsnElement)
	}{
		{
			name: "mode0_253_word_split",
			src:  strings.Repeat("  nop\n", 300),
			guard: func(t *testing.T, frames []cemitFrame, elems []format.InsnElement) {
				if len(frames) < 2 || frames[0].mode != 0 || frames[0].elems != 253 {
					t.Fatalf("fixture drifted: want >=2 mode-0 frames with the first split at 253 words, got %+v", frames)
				}
			},
		},
		{
			name: "mode1_payload_split",
			src:  strings.Repeat("  b 0x1000\n", 150),
			guard: func(t *testing.T, frames []cemitFrame, elems []format.InsnElement) {
				if len(frames) < 2 || frames[0].mode != 1 || frames[1].mode != 1 {
					t.Fatalf("fixture drifted: want >=2 mode-1 frames, got %+v", frames)
				}
				// The first frame must have split exactly because the next
				// element would not fit the 1016-byte payload cap.
				next := cemitWireSize(elems[frames[0].elems])
				if frames[0].payloadLen+next <= 1016 {
					t.Fatalf("fixture drifted: first frame payload %d + next element %d fits 1016 — the split branch was not forced", frames[0].payloadLen, next)
				}
			},
		},
		{
			name: "mode1_absorb_and_litbreak",
			src: strings.Repeat("  b 0x3000\n  nop\n", 80) +
				strings.Repeat("  nop\n", 5) +
				strings.Repeat("  b 0x3000\n  nop\n", 3) + "  ret\n",
			guard: func(t *testing.T, frames []cemitFrame, elems []format.InsnElement) {
				var mode0, mode1, absorbed int
				for _, fr := range frames {
					if fr.mode == 0 {
						mode0++
					} else {
						mode1++
						if fr.hasLiteral {
							absorbed++
						}
					}
				}
				if mode0 < 1 || mode1 < 2 || absorbed < 1 {
					t.Fatalf("fixture drifted: want >=1 mode-0, >=2 mode-1 (>=1 absorbing a literal), got %+v", frames)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, err := frontend.Translate([]byte(tc.src), tc.name)
			if err != nil {
				t.Fatalf("Translate: %v", err)
			}
			p1, err := assemble.Pass1(f)
			if err != nil {
				t.Fatalf("Pass1: %v", err)
			}
			want, _, _, err := assemble.Compact(f, p1)
			if err != nil {
				t.Fatalf("Compact: %v", err)
			}
			elems := parsePureInsnRun(t, tc.name, want)
			tc.guard(t, cemitFrames(t, want), elems)
			if len(want) > serCemitOutEnd-serCemitOut {
				t.Fatalf("expected frames are %d B — exceeds the %d-byte flat output window", len(want), serCemitOutEnd-serCemitOut)
			}

			mac := loadCompactSer(t)
			got := runCemitFlush(t, mac, elems, serCemitOut, serCemitOutEnd, 0)
			if !bytes.Equal(got, want) {
				i := 0
				for i < len(got) && i < len(want) && got[i] == want[i] {
					i++
				}
				t.Errorf("frame bytes differ from assemble.Compact: %d B vs %d B, first divergence at %d (got % x..., want % x...)",
					len(got), len(want), i, tail(got, i, 8), tail(want, i, 8))
			}
		})
	}
}

// TestCemitOutHdrWindowGuard proves the cemit_out_hdr room check is wrap-safe
// (the PR 827 review follow-up (b), compact_emit.asm): a flush whose frame
// cannot fit a window abutting top-of-memory must fail loudly with the &cd
// overflow tag — the pre-fix check computed cursor+3+payload, which wraps mod
// 65536 there and reads as a fit, silently corrupting low memory. An
// exact-fill window pins the need==room boundary as a success.
func TestCemitOutHdrWindowGuard(t *testing.T) {
	nop := format.InsnElement{BaseWord: 0xD503201F}

	t.Run("top_of_memory_overflow_fails", func(t *testing.T) {
		mac := loadCompactSer(t)
		elems := make([]format.InsnElement, 20)
		for i := range elems {
			elems[i] = nop
		}
		// Window [FFE0, FFFF): 31 bytes of room; the single mode-0 frame
		// needs 3+1+80 = 84. The unwrapped sum 0xFFE0+84 exceeds 0xFFFF, the
		// exact shape that used to wrap into a false fit.
		runCemitFlush(t, mac, elems, 0xFFE0, 0xFFFF, 0xCD)
	})

	t.Run("exact_fill_succeeds", func(t *testing.T) {
		mac := loadCompactSer(t)
		elems := []format.InsnElement{nop, nop}
		var w format.RecordWriter
		w.WriteInsnRun(0, elems)
		want := w.Bytes() // 3 header + 1 mode + 8 words = 12 bytes
		got := runCemitFlush(t, mac, elems, serCemitOut, serCemitOut+uint16(len(want)), 0)
		if !bytes.Equal(got, want) {
			t.Errorf("exact-fill frame = % x, want % x", got, want)
		}
	})
}

// TestSerializeSidecarOrderGuard proves the sidecar-anchor monotonicity guard
// (compact_serialize.asm:351-352) fires with tag &d1 on out-of-order input and
// passes on non-decreasing input — the PR 828 review follow-up (i48c-b8f).
// Mirrors the wantTag pattern of TestCemitOutHdrWindowGuard.
func TestSerializeSidecarOrderGuard(t *testing.T) {
	// Two SidecarBlank rows (kind 1: the shorter wire shape, just a RunLen
	// field after the kind+anchor header) at anchors 4 and 10. The subtests
	// swap their order to exercise both sides of the guard.
	rowLo := format.SidecarRow{Kind: format.SidecarBlank, Blank: format.BlankRun{Anchor: 4, RunLen: 1}}
	rowHi := format.SidecarRow{Kind: format.SidecarBlank, Blank: format.BlankRun{Anchor: 10, RunLen: 1}}

	// setupMinimal writes the parameter cells into mac for a minimal but
	// complete compact_serialize call: empty labels/locals/globals/recs, an
	// empty name table, the supplied sidecar rows, and a standard out window.
	setupMinimal := func(t *testing.T, mac *z80h.Machine, rows []format.SidecarRow) {
		t.Helper()
		mac.Write(serSidecarBuf, serSidecarWire(rows))
		mac.Write(serNamesBuf, serNamesWire(t, nil)) // [count:2 LE] = 0

		serSetCell(t, mac, "ser_labels_ptr", serLabelRowsBuf)
		serSetCell(t, mac, "ser_labels_count", 0)
		serSetCell(t, mac, "ser_locals_ptr", serLocalRowsBuf)
		serSetCell(t, mac, "ser_locals_count", 0)
		serSetCell(t, mac, "ser_globals_ptr", serGlobalsBuf)
		serSetCell(t, mac, "ser_globals_count", 0)
		serSetCell(t, mac, "ser_names_ptr", serNamesBuf)
		serSetCell(t, mac, "ser_sidecar_ptr", serSidecarBuf)
		serSetCell(t, mac, "ser_sidecar_count", uint16(len(rows)))
		serSetCell(t, mac, "ser_recs_ptr", serRecsBuf)
		serSetCell(t, mac, "ser_recs_len", 0)
		serSetCell(t, mac, "ser_out_base", serOutBuf)
		serSetCell(t, mac, "ser_out_end", serOutBufEnd)
	}

	t.Run("out_of_order_sidecar_fails", func(t *testing.T) {
		// Anchor 10 before anchor 4 (decreasing) must fire the &d1 guard.
		mac := loadCompactSer(t)
		setupMinimal(t, mac, []format.SidecarRow{rowHi, rowLo})
		res, err := mac.CallEntry("compact_serialize", z80h.Entry{})
		if err != nil {
			t.Fatalf("compact_serialize: %v", err)
		}
		failHalt, _ := mac.Sym("fail_halt")
		if !res.Halted || res.PC != failHalt {
			t.Fatalf("compact_serialize returned cleanly (PC=&%04X) — expected the fail trap with tag &D1", res.PC)
		}
		tagAddr, _ := mac.Sym("ser_fail_tag")
		if tag := mac.Read(tagAddr, 1)[0]; tag != 0xD1 {
			t.Fatalf("fail tag = &%02X, want &D1", tag)
		}
	})

	t.Run("in_order_sidecar_succeeds", func(t *testing.T) {
		// Anchor 4 before anchor 10 (non-decreasing) must produce a valid .tbn.
		mac := loadCompactSer(t)
		setupMinimal(t, mac, []format.SidecarRow{rowLo, rowHi})
		res, err := mac.CallEntry("compact_serialize", z80h.Entry{})
		if err != nil {
			t.Fatalf("compact_serialize: %v", err)
		}
		serCheckClean(t, mac, res, "compact_serialize")
		if outLen := serReadCell(t, mac, "ser_out_len"); outLen == 0 {
			t.Fatalf("compact_serialize wrote 0 bytes — expected a non-empty .tbn")
		}
	})
}
