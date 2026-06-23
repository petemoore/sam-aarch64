package assemble

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// These encodings are the canonical AArch64 forms (cross-checked against
// aarch64-linux-gnu-as in errata_xcheck_test.go when the toolchain is present).
const (
	// madd x0, x1, x2, x3  → 64-bit MAC, Ra=x3 (≠XZR): a 835769 candidate.
	maddX0 = uint32(0x9b020c20)
	// mul x0, x1, x2  (alias: madd x0,x1,x2,xzr) → Ra=XZR, NOT a mlxl.
	mulX0 = uint32(0x9b027c20)
	// smaddl x0, w1, w2, x3 → op31=001, 64-bit MAC.
	smaddlX0 = uint32(0x9b220c20)
	// umulh x0, x1, x2 → op31=110, excluded by the op31 filter.
	umulhX0 = uint32(0x9bc27c20)
	// ldr x4, [x5]  → unsigned-imm load, Rt=x4, Rn=x5.
	ldrX4 = uint32(0xf94000a4)
	// str x4, [x5]  → unsigned-imm store.
	strX4 = uint32(0xf90000a4)
	// add x0, x1, x2 → not a memory op, not a MAC.
	addX0 = uint32(0x8b020020)
	// adrp x0, #0  → ADRP, Rd=x0.
	adrpX0 = uint32(0x90000000)
	// adr x0, #0   → ADR, Rd=x0.
	adrX0 = uint32(0x10000000)
)

func TestMlxlP(t *testing.T) {
	cases := []struct {
		name string
		insn uint32
		want bool
	}{
		{"madd x0", maddX0, true},
		{"smaddl x0", smaddlX0, true},
		{"mul (Ra=XZR)", mulX0, false},
		{"umulh (op31 filtered)", umulhX0, false},
		{"add (not MAC)", addX0, false},
		{"ldr (not MAC)", ldrX4, false},
	}
	for _, c := range cases {
		if got := aarch64MlxlP(c.insn); got != c.want {
			t.Errorf("aarch64MlxlP(%s=0x%08x) = %v, want %v", c.name, c.insn, got, c.want)
		}
	}
}

func TestMemOp(t *testing.T) {
	if _, _, _, _, ok := aarch64MemOp(ldrX4); !ok {
		t.Errorf("ldr x4,[x5] not classified as a memory op")
	}
	if _, _, _, load, _ := aarch64MemOp(ldrX4); !load {
		t.Errorf("ldr x4,[x5] not classified as a load")
	}
	if _, _, _, load, _ := aarch64MemOp(strX4); load {
		t.Errorf("str x4,[x5] wrongly classified as a load")
	}
	if _, _, _, _, ok := aarch64MemOp(addX0); ok {
		t.Errorf("add x0 wrongly classified as a memory op")
	}
	if _, _, _, _, ok := aarch64MemOp(maddX0); ok {
		t.Errorf("madd x0 wrongly classified as a memory op")
	}
}

func TestErratumSequence835769(t *testing.T) {
	// ldr x4,[x5] then madd x0,x1,x2,x3: no register overlap → hazard.
	if !aarch64ErratumSequence(ldrX4, maddX0) {
		t.Errorf("ldr→madd (no dep) not flagged as a 835769 hazard")
	}
	// store then MAC: stores are never a RAW source → still a hazard.
	if !aarch64ErratumSequence(strX4, maddX0) {
		t.Errorf("str→madd not flagged as a 835769 hazard")
	}
	// RAW carve-out: load into x3, MAC reads x3 as Ra → safe.
	// madd x0,x1,x2,x4 reads x4 (=ldr's Rt) as the accumulator → safe.
	maddReadsX4 := uint32(0x9b021020) // madd x0,x1,x2,x4 (Ra=x4)
	if aarch64ErratumSequence(ldrX4, maddReadsX4) {
		t.Errorf("ldr x4→madd ...,x4 (RAW dep) wrongly flagged")
	}
	// Non-memory first instruction → never a hazard.
	if aarch64ErratumSequence(addX0, maddX0) {
		t.Errorf("add→madd wrongly flagged")
	}
	// MAC first, memory second → not the hazard order.
	if aarch64ErratumSequence(maddX0, ldrX4) {
		t.Errorf("madd→ldr wrongly flagged")
	}
}

func TestADRPPredicate(t *testing.T) {
	if !aarch64ADRP(adrpX0) {
		t.Errorf("adrp x0 not recognised as ADRP")
	}
	if aarch64ADRP(adrX0) {
		t.Errorf("adr x0 wrongly recognised as ADRP")
	}
	if aarch64ADRP(addX0) {
		t.Errorf("add x0 wrongly recognised as ADRP")
	}
}

func TestErratum843419SequenceP(t *testing.T) {
	// adrp x0; <mem op>; ldr x9,[x0] (unsigned-imm, base x0=ADRP Rd) → match.
	ldrFromX0 := uint32(0xf9400009) // ldr x9, [x0]
	if !aarch64Erratum843419SequenceP(adrpX0, ldrX4, ldrFromX0) {
		t.Errorf("adrp/ldr/ldr[x0] not flagged as an 843419 sequence")
	}
	// Final load uses a different base → no match.
	ldrFromX7 := uint32(0xf94000e9) // ldr x9, [x7]
	if aarch64Erratum843419SequenceP(adrpX0, ldrX4, ldrFromX7) {
		t.Errorf("adrp/ldr/ldr[x7] wrongly flagged (base ≠ ADRP Rd)")
	}
	// insn_2 a load-pair → excluded.
	ldpX1X2 := uint32(0xa9400c41) // ldp x1, x3, [x2]
	if aarch64Erratum843419SequenceP(adrpX0, ldpX1X2, ldrFromX0) {
		t.Errorf("load-pair as insn_2 wrongly allowed")
	}
}

func TestADRRewriteRoundTrip(t *testing.T) {
	// An ADRP at place computing target page P; the rewritten ADR must
	// encode the byte offset (P - place) and keep Rd.
	const place = int64(0x1000)
	// adrp x5, +0x2000 (decoded imm = 2 pages): immhi/immlo encode 2.
	// Build it via reencode of ADRP imm: imm field places 2.
	// decoded<<12 = 0x2000; with place&0xfff = 0 → ADR imm = 0x2000.
	insn := aarch64encodeADRPImm(aarch64ADRPOp, 2) | 5 // Rd = x5
	if !aarch64ADRP(insn) {
		t.Fatalf("constructed word 0x%08x is not an ADRP", insn)
	}
	imm := aarch64SignExtend(uint64(aarch64DecodeADRPImm(insn))<<12, 33) - int64(place&0xfff)
	if imm != 0x2000 {
		t.Fatalf("recomputed ADR imm = %d, want 0x2000", imm)
	}
	adr := aarch64ReencodeADRImm(aarch64ADROp, uint32(imm)) | aarch64RT(insn)
	if aarch64ADRP(adr) {
		t.Errorf("rewritten word 0x%08x still classifies as ADRP", adr)
	}
	if aarch64RD(adr) != 5 {
		t.Errorf("rewritten ADR Rd = %d, want 5", aarch64RD(adr))
	}
	if adr&0x9f000000 != aarch64ADROp {
		t.Errorf("rewritten word 0x%08x is not an ADR (op bits)", adr)
	}
}

// aarch64encodeADRPImm is a test helper: place a 21-bit immediate into an
// ADRP's immhi:immlo (the inverse of aarch64DecodeADRPImm).
func aarch64encodeADRPImm(insn, imm uint32) uint32 {
	immlo := imm & 0x3
	immhi := (imm >> 2) & mask(19)
	return (insn &^ ((mask(2) << 29) | (mask(19) << 5))) | (immlo << 29) | (immhi << 5)
}

// ---------------------------------------------------------------------------
// binutils cross-check: confirm our detection verdict matches GNU ld's own
// --fix-cortex-a53-* decision on identical inputs. Skipped when the GNU
// aarch64 toolchain is absent (a principled prerequisite-missing skip).
// ---------------------------------------------------------------------------

func toolchain(t *testing.T) (as, ld, objdump string) {
	t.Helper()
	find := func(names ...string) string {
		for _, n := range names {
			if p, err := exec.LookPath(n); err == nil {
				return p
			}
		}
		return ""
	}
	as = find("aarch64-linux-gnu-as", "aarch64-none-elf-as")
	ld = find("aarch64-linux-gnu-ld", "aarch64-none-elf-ld")
	objdump = find("aarch64-linux-gnu-objdump", "aarch64-none-elf-objdump")
	if as == "" || ld == "" || objdump == "" {
		t.Skip("aarch64 GNU toolchain (as/ld/objdump) not on PATH — skipping binutils cross-check")
	}
	return as, ld, objdump
}

func runTool(t *testing.T, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, stderr.String())
	}
}

func TestXCheck835769MatchesBinutils(t *testing.T) {
	as, ld, _ := toolchain(t)
	dir := t.TempDir()

	// A hazard (ldr→madd, no dep) and a safe case (RAW dep). ld inserts an
	// __erratum_835769_veneer symbol exactly when it considers a sequence a
	// hazard. Our predicate must agree on each.
	cases := []struct {
		name    string
		asm     string
		insn1   uint32 // ldr x4,[x5]
		insn2   uint32 // the madd
		ourFlag bool
	}{
		{"hazard", "ldr x4,[x5]\nmadd x0,x1,x2,x3\n", ldrX4, maddX0, true},
		{"raw-dep", "ldr x4,[x5]\nmadd x0,x1,x2,x4\n", ldrX4, 0x9b021020, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := filepath.Join(dir, c.name+".s")
			obj := filepath.Join(dir, c.name+".o")
			elf := filepath.Join(dir, c.name+".elf")
			if err := os.WriteFile(src, []byte(".text\n.global _start\n_start:\n"+c.asm+"ret\n"), 0644); err != nil {
				t.Fatal(err)
			}
			runTool(t, as, src, "-o", obj)
			runTool(t, ld, "--fix-cortex-a53-835769", obj, "-o", elf)

			b, err := os.ReadFile(elf)
			if err != nil {
				t.Fatal(err)
			}
			binutilsFixed := bytes.Contains(b, []byte("__erratum_835769_veneer"))
			if binutilsFixed != c.ourFlag {
				t.Errorf("binutils fixed = %v, our verdict = %v", binutilsFixed, c.ourFlag)
			}
			if got := aarch64ErratumSequence(c.insn1, c.insn2); got != c.ourFlag {
				t.Errorf("aarch64ErratumSequence(0x%08x,0x%08x) = %v, want %v", c.insn1, c.insn2, got, c.ourFlag)
			}
		})
	}
}

func TestXCheck843419MatchesBinutils(t *testing.T) {
	as, ld, objdump := toolchain(t)
	dir := t.TempDir()

	src := filepath.Join(dir, "haz843.s")
	obj := filepath.Join(dir, "haz843.o")
	scr := filepath.Join(dir, "haz843.ld")
	elf := filepath.Join(dir, "haz843.elf")
	asm := ".text\n.global _start\n_start:\n" +
		"adrp x0, sym\nldr x4,[x5]\nldr x9,[x0]\nret\n.data\nsym:\n.quad 0\n"
	// Place _start (the ADRP) at an address whose low 12 bits are 0xff8.
	script := "SECTIONS {\n  . = 0x10000 + 0xff8;\n  .text : { *(.text) }\n  .data : { *(.data) }\n}\n"
	if err := os.WriteFile(src, []byte(asm), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(scr, []byte(script), 0644); err != nil {
		t.Fatal(err)
	}
	runTool(t, as, src, "-o", obj)
	runTool(t, ld, "-T", scr, "--fix-cortex-a53-843419", obj, "-o", elf)

	// binutils' preferred 843419 fix is an in-place ADRP→ADR rewrite. Confirm
	// the instruction at 0x10ff8 became an ADR.
	out, err := exec.Command(objdump, "-d", elf).Output()
	if err != nil {
		t.Fatalf("objdump: %v", err)
	}
	var line string
	for _, l := range strings.Split(string(out), "\n") {
		if strings.Contains(l, "10ff8:") {
			line = l
			break
		}
	}
	if line == "" {
		t.Fatalf("no instruction at 0x10ff8 in:\n%s", out)
	}
	if !strings.Contains(line, "\tadr\t") {
		t.Errorf("binutils did not rewrite the ADRP to ADR: %q", strings.TrimSpace(line))
	}
	if !aarch64Erratum843419SequenceP(adrpX0, ldrX4, 0xf9400009) {
		t.Errorf("our 843419 predicate did not flag adrp/ldr/ldr[x0]")
	}
}
