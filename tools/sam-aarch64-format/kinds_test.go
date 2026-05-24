package format

import "testing"

func TestRecordKindValues(t *testing.T) {
	cases := []struct {
		k    RecordKind
		want byte
	}{
		{KindInst, 0x01},
		{KindLabelDef, 0x02},
		{KindLocalDef, 0x03},
		{KindDirective, 0x04},
		{KindComment, 0x05},
	}
	for _, c := range cases {
		if byte(c.k) != c.want {
			t.Errorf("%s = 0x%02x, want 0x%02x", c.k.Name(), byte(c.k), c.want)
		}
	}
}

func TestRecordKindNames(t *testing.T) {
	if KindInst.Name() != "INST" {
		t.Errorf("KindInst.Name() = %q, want %q", KindInst.Name(), "INST")
	}
	if KindComment.Name() != "COMMENT" {
		t.Errorf("KindComment.Name() = %q, want %q", KindComment.Name(), "COMMENT")
	}
}

func TestRecordKindIsKnown(t *testing.T) {
	if !KindInst.IsKnown() {
		t.Errorf("KindInst.IsKnown() = false, want true")
	}
	if RecordKind(0xFF).IsKnown() {
		t.Errorf("RecordKind(0xFF).IsKnown() = true, want false")
	}
}
