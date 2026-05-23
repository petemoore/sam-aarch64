package aarch64enc

import "testing"

func TestEncodeXreg(t *testing.T) {
	cases := []struct {
		reg   byte
		slot  OperandSlot
		want  uint32
		ok    bool
	}{
		{5, OperandSlot{SlotKind: Xreg, BitPosition: 0, BitWidth: 5}, 5, true},
		{30, OperandSlot{SlotKind: Xreg, BitPosition: 0, BitWidth: 5}, 30, true},
		{31, OperandSlot{SlotKind: Xreg, BitPosition: 0, BitWidth: 5}, 31, true},
		{5, OperandSlot{SlotKind: Xreg, BitPosition: 5, BitWidth: 5}, 5 << 5, true},
		{32, OperandSlot{SlotKind: Xreg, BitPosition: 0, BitWidth: 5}, 0, false},
	}
	for _, c := range cases {
		got, err := encodeReg(c.slot, c.reg)
		if c.ok && err != nil {
			t.Errorf("encodeReg(%d) err = %v", c.reg, err)
			continue
		}
		if !c.ok && err == nil {
			t.Errorf("encodeReg(%d) succeeded, want error", c.reg)
			continue
		}
		if c.ok && got != c.want {
			t.Errorf("encodeReg(%d) = 0x%08x, want 0x%08x", c.reg, got, c.want)
		}
	}
}

func TestEncodeXregOrSpAcceptsThirtyOne(t *testing.T) {
	slot := OperandSlot{SlotKind: XregOrSp, BitPosition: 0, BitWidth: 5}
	got, err := encodeReg(slot, 31)
	if err != nil || got != 31 {
		t.Errorf("XregOrSp(31) = (0x%08x, %v), want (31, nil)", got, err)
	}
}

func TestEncodeImm5(t *testing.T) {
	slot := OperandSlot{SlotKind: Imm5, BitPosition: 10, BitWidth: 5}
	got, err := encodeImmN(slot, 17)
	if err != nil || got != 17<<10 {
		t.Errorf("Imm5(17) = (0x%08x, %v), want (0x%08x, nil)", got, err, 17<<10)
	}
	if _, err := encodeImmN(slot, 32); err == nil {
		t.Errorf("Imm5(32) should overflow")
	}
}

func TestEncodeImm6(t *testing.T) {
	slot := OperandSlot{SlotKind: Imm6, BitPosition: 16, BitWidth: 6}
	got, err := encodeImmN(slot, 63)
	if err != nil || got != 63<<16 {
		t.Errorf("Imm6(63) = (0x%08x, %v)", got, err)
	}
	if _, err := encodeImmN(slot, 64); err == nil {
		t.Errorf("Imm6(64) should overflow")
	}
}

func TestEncodeCondCode(t *testing.T) {
	slot := OperandSlot{SlotKind: CondCode, BitPosition: 0, BitWidth: 4}
	got, err := encodeCond(slot, 0xb)
	if err != nil || got != 0xb {
		t.Errorf("CondCode(LT) = (0x%08x, %v)", got, err)
	}
}
