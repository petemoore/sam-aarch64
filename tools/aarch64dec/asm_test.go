package aarch64dec

import (
	"strings"
	"testing"
)

func TestWriteAsm_labels(t *testing.T) {
	// nop at 0x0, b 0x8 at 0x4 (branch to ret), ret at 0x8.
	// b 0x8 at pc=0x4: offset=+1 instr (+4 bytes), imm26=1 → 0x14000001.
	data := []byte{
		0x1f, 0x20, 0x03, 0xd5, // nop  at 0x0
		0x01, 0x00, 0x00, 0x14, // b 0x8 at 0x4
		0xc0, 0x03, 0x5f, 0xd6, // ret  at 0x8
	}
	var buf strings.Builder
	if err := WriteAsm(&buf, 0, data); err != nil {
		t.Fatal(err)
	}
	want := "\t.text\n\tnop\n\tb\tL0\nL0:\n\tret\n"
	if got := buf.String(); got != want {
		t.Errorf("WriteAsm:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestWriteAsm_no_branches(t *testing.T) {
	// nop, ret — no branches, so no labels.
	data := []byte{
		0x1f, 0x20, 0x03, 0xd5, // nop
		0xc0, 0x03, 0x5f, 0xd6, // ret
	}
	var buf strings.Builder
	if err := WriteAsm(&buf, 0, data); err != nil {
		t.Fatal(err)
	}
	want := "\t.text\n\tnop\n\tret\n"
	if got := buf.String(); got != want {
		t.Errorf("WriteAsm:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestWriteAsm_declined_word(t *testing.T) {
	// A word aarch64dec declines renders as .inst 0xNNNNNNNN.
	// 0x885f7c00 = ldxr w0, [x0] — atomics are declined.
	data := []byte{0x00, 0x7c, 0x5f, 0x88}
	var buf strings.Builder
	if err := WriteAsm(&buf, 0, data); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); !strings.Contains(got, ".inst") {
		t.Errorf("expected .inst for declined word, got: %q", got)
	}
}
