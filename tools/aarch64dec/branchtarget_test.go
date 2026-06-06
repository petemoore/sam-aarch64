package aarch64dec

import "testing"

func TestBranchTarget(t *testing.T) {
	tests := []struct {
		name   string
		pc     uint64
		word   uint32
		target uint64
		ok     bool
	}{
		// b 0x10 at pc=0: imm26=4 → byteOffset=16=0x10
		{"b 0x10 at pc=0", 0, 0x14000004, 0x10, true},
		// bl 0x8 at pc=0: imm26=2 → byteOffset=8
		{"bl 0x8 at pc=0", 0, 0x94000002, 0x8, true},
		// b.ne 0x10 at pc=0: BranchImm19 imm19=4 → byteOffset=16
		{"b.ne 0x10 at pc=0", 0, 0x54000081, 0x10, true},
		// cbz w0, 0x10 at pc=0: BranchImm19 imm19=4
		{"cbz w0 0x10 at pc=0", 0, 0x34000080, 0x10, true},
		// tbnz w10, #31, 0x310 at pc=0x314
		{"tbnz w10 #31 0x310 at pc=0x314", 0x314, 0x37ffffea, 0x310, true},
		// nop — not a branch
		{"nop", 0, 0xd503201f, 0, false},
		// ret — not a direct branch (register branch, no immediate target)
		{"ret", 0, 0xd65f03c0, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := BranchTarget(tt.pc, tt.word)
			if ok != tt.ok {
				t.Errorf("ok: got %v want %v", ok, tt.ok)
				return
			}
			if ok && got != tt.target {
				t.Errorf("target: got %#x want %#x", got, tt.target)
			}
		})
	}
}
