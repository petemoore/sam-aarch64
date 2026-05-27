package main

import (
	"bytes"
	"testing"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

func TestPass2_Nop(t *testing.T) {
	var rw format.RecordWriter
	nopID, _ := format.MnemonicID("nop")
	rw.WriteInst(nopID, 0, nil)
	var buf bytes.Buffer
	format.WriteFile(&buf, format.NewSymbolTable(), rw.Bytes())
	f, _ := format.ReadFile(buf.Bytes())

	res, _ := Pass1(f)
	out, err := Pass2(f, res)
	if err != nil {
		t.Fatal(err)
	}
	// NOP = 0xD503201F (little-endian: 1F 20 03 D5)
	want := []byte{0x1f, 0x20, 0x03, 0xd5}
	if !bytes.Equal(out, want) {
		t.Errorf("got % X, want % X", out, want)
	}
}

// TestMemInstSize_SignedExtendLoads exercises the size+scale entries
// added for LDRSB / LDRSH / LDRSW (ARM ARM C6.2.117 / C6.2.118 /
// C6.2.119). The values are the same as the LDR/STR family in their
// matching size class; this is a safety net against accidental table
// drift when ldrsb/ldrsh/ldrsw IDs shift.
func TestMemInstSize_SignedExtendLoads(t *testing.T) {
	tt := []struct {
		mnem  string
		size  uint32
		scale int64
	}{
		{"ldrsb", 0b00, 1},
		{"ldrsh", 0b01, 2},
		{"ldrsw", 0b10, 4},
	}
	for _, tc := range tt {
		id, ok := format.MnemonicID(tc.mnem)
		if !ok {
			t.Fatalf("%s not in mnemonic table", tc.mnem)
		}
		// Pin against Xt's expected register kind: ldrsb/ldrsh accept
		// both Xt and Wt — we test the X-form (matching the table
		// entries above); ldrsw is Xt-only.
		gotSize, gotScale := memInstSize(id, format.OpRegX)
		if gotSize != tc.size || gotScale != tc.scale {
			t.Errorf("memInstSize(%s) = (%#b, %d), want (%#b, %d)",
				tc.mnem, gotSize, gotScale, tc.size, tc.scale)
		}
	}
}

// TestMemInstOpc_SignedExtendLoads pins the opc values that distinguish
// signed-extend loads from regular LDR/STR. opc=10 selects "signed load
// to Xt" (sign-extend to 64-bit). opc=11 selects "signed load to Wt"
// (sign-extend to 32-bit). LDRSW only has the Xt form; selecting Wt
// must error.
func TestMemInstOpc_SignedExtendLoads(t *testing.T) {
	type tc struct {
		mnem  string
		rtK   format.OperandKind
		opc   uint32
		isErr bool
	}
	tests := []tc{
		{"ldrsb", format.OpRegX, 0b10, false},
		{"ldrsb", format.OpRegW, 0b11, false},
		{"ldrsh", format.OpRegX, 0b10, false},
		{"ldrsh", format.OpRegW, 0b11, false},
		{"ldrsw", format.OpRegX, 0b10, false},
		{"ldrsw", format.OpRegW, 0, true},
		// Regular loads/stores should ignore the rtKind argument and
		// return their canonical opc (01 for load, 00 for store).
		{"ldr", format.OpRegX, 0b01, false},
		{"str", format.OpRegX, 0b00, false},
		{"ldrh", format.OpRegW, 0b01, false},
	}
	for _, c := range tests {
		id, ok := format.MnemonicID(c.mnem)
		if !ok {
			t.Fatalf("%s not in mnemonic table", c.mnem)
		}
		got, err := memInstOpc(id, c.rtK)
		if c.isErr {
			if err == nil {
				t.Errorf("memInstOpc(%s, %v) expected error, got opc=%#b", c.mnem, c.rtK, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("memInstOpc(%s, %v) unexpected error: %v", c.mnem, c.rtK, err)
			continue
		}
		if got != c.opc {
			t.Errorf("memInstOpc(%s, %v) = %#b, want %#b", c.mnem, c.rtK, got, c.opc)
		}
	}
}

func TestPass2_DirectiveBytes(t *testing.T) {
	st := format.NewSymbolTable()
	var rw format.RecordWriter
	var ow format.OperandWriter
	for _, v := range []int64{1, 2, 3} {
		var ew format.ExprWriter
		ew.WriteImm(v)
		ow.WriteImmExpr(ew.Bytes())
	}
	id, _ := format.DirectiveID(".byte")
	rw.WriteDirective(id, 3, ow.Bytes())

	var buf bytes.Buffer
	format.WriteFile(&buf, st, rw.Bytes())
	f, _ := format.ReadFile(buf.Bytes())

	res, _ := Pass1(f)
	out, err := Pass2(f, res)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{1, 2, 3}
	if !bytes.Equal(out, want) {
		t.Errorf("got % X, want % X", out, want)
	}
}
