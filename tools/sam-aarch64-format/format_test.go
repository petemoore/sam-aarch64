package format

import "testing"

func TestMagicAndVersion(t *testing.T) {
	if string(Magic[:]) != "SA64" {
		t.Errorf("Magic = %q, want \"SA64\"", string(Magic[:]))
	}
	if Version != 3 {
		t.Errorf("Version = %d, want 3", Version)
	}
}
