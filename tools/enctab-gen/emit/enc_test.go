package emit

import (
	"bytes"
	"testing"

	enc "github.com/petemoore/sam-aarch64/tools/aarch64enc"
)

func TestRenderEnc_Header(t *testing.T) {
	var buf bytes.Buffer
	if err := RenderEnc(&buf, nil); err != nil {
		t.Fatal(err)
	}
	got := buf.Bytes()
	if string(got[:4]) != "ENC1" {
		t.Errorf("magic = %q", got[:4])
	}
	if got[4] != 1 || got[5] != 0 {
		t.Errorf("version bytes = % X, want 01 00", got[4:6])
	}
}

func TestRenderEnc_OneForm(t *testing.T) {
	forms := []enc.Form{
		{MnemonicID: 0, Pattern: 0xD503201F, Mask: 0xFFFFFFFF, Slots: nil},
	}
	var buf bytes.Buffer
	if err := RenderEnc(&buf, forms); err != nil {
		t.Fatal(err)
	}
	got := buf.Bytes()
	// Header: 4 magic + 2 ver + 2 flags = 8 bytes.
	// Form count u32 = 4 bytes.
	// Form: mnemonic u16 + opcount u8 + pattern u32 + mask u32 = 11 bytes.
	// Index count u32 = 4 bytes; one index entry = u16 + u32 + u16 = 8 bytes.
	// Total = 8 + 4 + 11 + 4 + 8 = 35 bytes.
	if len(got) != 35 {
		t.Errorf("expected 35 bytes, got %d", len(got))
	}
}
