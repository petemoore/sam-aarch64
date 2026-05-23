package format

import "testing"

func TestMnemonicTableLookup(t *testing.T) {
	id, ok := MnemonicID("add")
	if !ok {
		t.Fatalf("MnemonicID(\"add\") not found")
	}
	if MnemonicName(id) != "add" {
		t.Errorf("round-trip failed: %d -> %q", id, MnemonicName(id))
	}
}

func TestMnemonicTableUnknown(t *testing.T) {
	if _, ok := MnemonicID("not_a_real_mnemonic"); ok {
		t.Errorf("MnemonicID returned ok for nonsense input")
	}
	if MnemonicName(0xFFFF) != "" {
		t.Errorf("MnemonicName(0xFFFF) should return empty string")
	}
}

func TestMnemonicIDsStable(t *testing.T) {
	id, _ := MnemonicID("nop")
	if id != 0 {
		t.Errorf("MnemonicID(\"nop\") = %d, want 0", id)
	}
	id, _ = MnemonicID("add")
	if id != 1 {
		t.Errorf("MnemonicID(\"add\") = %d, want 1", id)
	}
}
