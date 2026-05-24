package format

import "testing"

func TestOperandKindValues(t *testing.T) {
	cases := []struct {
		k    OperandKind
		want byte
	}{
		{OpRegX, 0x01}, {OpRegW, 0x02}, {OpRegXSP, 0x03}, {OpRegWSP, 0x04},
		{OpImmExpr, 0x05}, {OpShiftedReg, 0x06}, {OpExtendedReg, 0x07},
		{OpMem, 0x08}, {OpString, 0x09}, {OpCond, 0x0A}, {OpSysName, 0x0B},
		{OpLitPool, 0x0C},
	}
	for _, c := range cases {
		if byte(c.k) != c.want {
			t.Errorf("%s = 0x%02x, want 0x%02x", c.k.Name(), byte(c.k), c.want)
		}
	}
}

func TestMemShapeValues(t *testing.T) {
	if MemBase != 0 || MemBaseOff != 1 || MemBaseOffPre != 2 || MemBaseOffPost != 3 ||
		MemBaseIdx != 4 || MemBaseIdxShifted != 5 || MemBaseIdxExtended != 6 {
		t.Errorf("MemShape constants do not match §4 sub-shapes")
	}
}

func TestShiftKindNames(t *testing.T) {
	if ShiftLSL.Name() != "lsl" || ShiftLSR.Name() != "lsr" ||
		ShiftASR.Name() != "asr" || ShiftROR.Name() != "ror" {
		t.Errorf("ShiftKind names do not match aarch64 syntax")
	}
}

func TestExtendKindNames(t *testing.T) {
	want := []string{"uxtb", "uxth", "uxtw", "uxtx", "sxtb", "sxth", "sxtw", "sxtx"}
	for i, n := range want {
		if ExtendKind(i).Name() != n {
			t.Errorf("ExtendKind(%d).Name() = %q, want %q", i, ExtendKind(i).Name(), n)
		}
	}
}

func TestCondCodeNames(t *testing.T) {
	if CondEQ.Name() != "eq" || CondAL.Name() != "al" || CondNV.Name() != "nv" {
		t.Errorf("cond-code names do not match aarch64 syntax")
	}
}
