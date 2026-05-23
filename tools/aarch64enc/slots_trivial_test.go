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
