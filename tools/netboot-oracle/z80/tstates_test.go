// tstates_test.go — the authoritative oracle for the T-state counter
// (tstates.go). Each anchor asserts the measured cycles of a hand-assembled
// instruction EXACTLY equal its canonical Zilog "Z80 CPU User Manual" T-state
// figure. These anchor numbers are the oracle: if the counter disagrees, the
// counter (or its table) is wrong, never the anchor.
package z80_test

import (
	"testing"

	z80h "github.com/petemoore/sam-aarch64/tools/netboot-oracle/z80"
)

// progOrg is where anchor programs are staged. It matches the harness load org
// so RunFrom executes from familiar territory, clear of the stack/trap.
const progOrg = 0x8000

// retT is the canonical T-state cost of RET (10), the terminator every anchor
// program ends with. measureBody returns the program's total cycles minus this
// trailing RET, isolating the body under test.
const retT = 10

// measureBody stages body bytes followed by a RET at progOrg, runs it, and
// returns the total T-states LESS the trailing RET (10) — i.e. the cycles of
// exactly the body instructions. in supplies entry registers (for ops that read
// HL/BC/DE).
func measureBody(t *testing.T, body []byte, in z80h.Entry) uint64 {
	t.Helper()
	mac := z80h.New()
	prog := append(append([]byte{}, body...), 0xC9) // body ; RET
	mac.Write(progOrg, prog)
	res, err := mac.RunFrom(progOrg, in)
	if err != nil {
		t.Fatalf("RunFrom(% X): %v", body, err)
	}
	if res.TStates < retT {
		t.Fatalf("total %d T < trailing RET %d", res.TStates, retT)
	}
	return res.TStates - retT
}

// TestTStateAnchors asserts each opcode's measured cycles equal its canonical
// value. The non-conditional anchors are a single body instruction.
func TestTStateAnchors(t *testing.T) {
	// jpTarget is progOrg+3 (the byte just after a 3-byte JP nn at progOrg), so
	// the jump lands on the trailing RET. lo/hi avoid a compile-time constant
	// byte-overflow.
	jpTarget := uint16(progOrg + 3)
	jpLo, jpHi := byte(jpTarget), byte(jpTarget>>8)
	cases := []struct {
		name string
		body []byte
		in   z80h.Entry
		want uint64
	}{
		{"NOP", []byte{0x00}, z80h.Entry{}, 4},
		{"LD B,C", []byte{0x41}, z80h.Entry{}, 4},
		{"LD A,n", []byte{0x3E, 0x55}, z80h.Entry{}, 7},
		// HL=progOrg so LD A,(HL) reads inside the staged program (any byte; the
		// value is irrelevant to the cycle count).
		{"LD A,(HL)", []byte{0x7E}, z80h.Entry{HL: progOrg}, 7},
		{"LD (HL),n", []byte{0x36, 0xAB}, z80h.Entry{HL: 0x9000}, 10},
		{"LD BC,nn", []byte{0x01, 0x34, 0x12}, z80h.Entry{}, 10},
		{"LD (nn),HL", []byte{0x22, 0x00, 0x90}, z80h.Entry{}, 16},
		{"INC BC", []byte{0x03}, z80h.Entry{}, 6},
		{"INC (HL)", []byte{0x34}, z80h.Entry{HL: 0x9000}, 11},
		{"ADD A,B", []byte{0x80}, z80h.Entry{}, 4},
		{"ADD A,n", []byte{0xC6, 0x01}, z80h.Entry{}, 7},
		{"ADD HL,BC", []byte{0x09}, z80h.Entry{}, 11},
		{"JP nn", []byte{0xC3, jpLo, jpHi}, z80h.Entry{}, 10},
		{"JR e", []byte{0x18, 0x00}, z80h.Entry{}, 12},
		{"EX DE,HL", []byte{0xEB}, z80h.Entry{}, 4},
		{"CB RLC B", []byte{0xCB, 0x00}, z80h.Entry{}, 8},
		{"CB RLC (HL)", []byte{0xCB, 0x06}, z80h.Entry{HL: 0x9000}, 15},
		{"CB BIT 0,B", []byte{0xCB, 0x40}, z80h.Entry{}, 8},
		{"CB BIT 0,(HL)", []byte{0xCB, 0x46}, z80h.Entry{HL: 0x9000}, 12},
		{"ED SBC HL,BC", []byte{0xED, 0x42}, z80h.Entry{}, 15},
		{"ED NEG", []byte{0xED, 0x44}, z80h.Entry{}, 8},
		{"DD LD IX,nn", []byte{0xDD, 0x21, 0x34, 0x12}, z80h.Entry{}, 14},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := measureBody(t, tc.body, tc.in)
			if got != tc.want {
				t.Errorf("%s: measured %d T, canonical %d", tc.name, got, tc.want)
			}
		})
	}

	// PUSH BC = 11. PUSH leaves SP below the trap return address, so balance it
	// with two INC SP (0x33, 6 each) to restore SP before the trailing RET; then
	// subtract their known cost. Body = PUSH BC(11) + INC SP(6) + INC SP(6).
	t.Run("PUSH BC", func(t *testing.T) {
		got := measureBody(t, []byte{0xC5, 0x33, 0x33}, z80h.Entry{}) - 6 - 6
		if got != 11 {
			t.Errorf("PUSH BC: %d, want 11", got)
		}
	})

	// POP BC = 10. POP needs a word on the stack, so PUSH BC (11) first; the pair
	// is SP-balanced, leaving the trap address for the trailing RET. Subtract the
	// PUSH. Body = PUSH BC(11) + POP BC(10).
	t.Run("POP BC", func(t *testing.T) {
		got := measureBody(t, []byte{0xC5, 0xC1}, z80h.Entry{}) - 11
		if got != 10 {
			t.Errorf("POP BC: %d, want 10", got)
		}
	})

	// DD JP (IX) = 8. Load IX with the address of the trailing RET (LD IX,nn=14),
	// then JP (IX) lands on it and terminates. Subtract the LD IX,nn. The RET sits
	// at progOrg + 6 (after LD IX,nn = 4 bytes and JP (IX) = 2 bytes).
	t.Run("DD JP (IX)", func(t *testing.T) {
		retAddr := uint16(progOrg + 6)
		body := []byte{0xDD, 0x21, byte(retAddr), byte(retAddr >> 8), 0xDD, 0xE9}
		got := measureBody(t, body, z80h.Entry{}) - 14
		if got != 8 {
			t.Errorf("DD JP (IX): %d, want 8", got)
		}
	})
}

// TestTStateAnchorsConditional covers branch-dependent timing. The body sets the
// deciding flag/counter, then the conditional op runs; the body's known cost is
// subtracted so the conditional op's own cycles are isolated and matched to its
// canonical taken / not-taken figure.
func TestTStateAnchorsConditional(t *testing.T) {
	// Flag-prep instructions and their known costs (subtracted out):
	//   SCF (0x37) = 4 sets carry; OR A (0xB7) = 4 clears carry & sets Z by A;
	//   XOR A (0xAF) = 4 sets Z (A=0); a CP (0x05 DEC B etc.) — we use simple
	//   single-byte 4-cycle ops so the subtraction is unambiguous.
	const prep4 = 4

	// JR Z,e taken: XOR A sets Z; JR Z,+0 taken = 12.
	t.Run("JR Z taken", func(t *testing.T) {
		got := measureBody(t, []byte{0xAF, 0x28, 0x00}, z80h.Entry{}) - prep4
		if got != 12 {
			t.Errorf("JR Z taken: %d, want 12", got)
		}
	})
	// JR Z,e not-taken: OR A with A!=0 clears Z; JR Z not-taken = 7. Load A first
	// with LD A,1 (7) then OR A (4); subtract both.
	t.Run("JR Z not-taken", func(t *testing.T) {
		got := measureBody(t, []byte{0x3E, 0x01, 0xB7, 0x28, 0x00}, z80h.Entry{}) - 7 - prep4
		if got != 7 {
			t.Errorf("JR Z not-taken: %d, want 7", got)
		}
	})

	// DJNZ taken: B=2 on entry (B-1=1 != 0) so it branches = 13.
	t.Run("DJNZ taken", func(t *testing.T) {
		got := measureBody(t, []byte{0x10, 0x00}, z80h.Entry{BC: 0x0200})
		if got != 13 {
			t.Errorf("DJNZ taken: %d, want 13", got)
		}
	})
	// DJNZ not-taken: B=1 on entry (B-1=0) so it falls through = 8.
	t.Run("DJNZ not-taken", func(t *testing.T) {
		got := measureBody(t, []byte{0x10, 0x00}, z80h.Entry{BC: 0x0100})
		if got != 8 {
			t.Errorf("DJNZ not-taken: %d, want 8", got)
		}
	})

	// CALL nn = 17 (always). It calls into a RET so the program terminates: body
	// = CALL <ret-slot>; the called RET returns to the body's own RET, which
	// terminates. The body executes CALL(17) + the inner RET(10); measureBody
	// strips one trailing RET (the body's own), so the measured body cost is
	// CALL(17) + innerRET(10) = 27. Assert that, deriving CALL=17.
	t.Run("CALL nn", func(t *testing.T) {
		// Layout at progOrg: CD lo hi (call) , C9 (inner RET) , then measureBody
		// appends the terminating RET. The CALL targets the inner RET at +3.
		target := uint16(progOrg + 3)
		body := []byte{0xCD, byte(target), byte(target >> 8), 0xC9}
		got := measureBody(t, body, z80h.Entry{})
		const innerRET = 10
		if got-innerRET != 17 {
			t.Errorf("CALL nn: %d (minus innerRET %d = %d), want 17", got, innerRET, got-innerRET)
		}
	})

	// RET Z taken: XOR A sets Z, then RET Z returns to the trap = 11. The body is
	// XOR A (4) ; RET Z. Because RET Z (taken) returns straight to the HALT trap,
	// measureBody's appended RET never executes — so the measured total has NO
	// trailing RET to strip. Measure the whole program directly here.
	t.Run("RET Z taken", func(t *testing.T) {
		mac := z80h.New()
		mac.Write(progOrg, []byte{0xAF, 0xC8, 0xC9}) // XOR A ; RET Z ; RET
		res, err := mac.RunFrom(progOrg, z80h.Entry{})
		if err != nil {
			t.Fatalf("RunFrom: %v", err)
		}
		// XOR A (4) + RET Z taken (11). The final RET is never reached.
		if res.TStates != 4+11 {
			t.Errorf("RET Z taken total = %d, want %d (XOR A 4 + RET Z 11)", res.TStates, 4+11)
		}
	})
	// RET Z not-taken = 5: OR A on A!=0 clears Z; RET Z falls through to the
	// trailing RET (10) which returns to the trap. Total = LD A,1(7) + OR A(4) +
	// RET Z not-taken(5) + RET(10).
	t.Run("RET Z not-taken", func(t *testing.T) {
		mac := z80h.New()
		mac.Write(progOrg, []byte{0x3E, 0x01, 0xB7, 0xC8, 0xC9}) // LD A,1 ; OR A ; RET Z ; RET
		res, err := mac.RunFrom(progOrg, z80h.Entry{})
		if err != nil {
			t.Fatalf("RunFrom: %v", err)
		}
		if res.TStates != 7+4+5+10 {
			t.Errorf("RET Z not-taken total = %d, want %d", res.TStates, 7+4+5+10)
		}
	})
}

// TestTStateLDIR exercises the repeating block op: LDIR copying n bytes costs
// 21 per repeating iteration and 16 on the final one, so n bytes = 21*(n-1)+16.
func TestTStateLDIR(t *testing.T) {
	for _, n := range []uint16{1, 2, 5} {
		mac := z80h.New()
		// LDIR ; RET, copying n bytes from DE? No — LDIR copies (HL)->(DE). Set
		// HL=src, DE=dst, BC=n. Source/dest are scratch RAM clear of the program.
		mac.Write(progOrg, []byte{0xED, 0xB0, 0xC9}) // LDIR ; RET
		res, err := mac.RunFrom(progOrg, z80h.Entry{HL: 0xA000, DE: 0xB000, BC: n})
		if err != nil {
			t.Fatalf("n=%d LDIR: %v", n, err)
		}
		// Strip the trailing RET (10).
		body := res.TStates - retT
		var want uint64
		if n == 1 {
			want = 16 // single iteration is the final one
		} else {
			want = 21*uint64(n-1) + 16
		}
		if body != want {
			t.Errorf("LDIR n=%d: body %d T, want %d", n, body, want)
		}
	}
}

// TestTStateLDIRFinalAnchor pins the two LDIR figures the prompt lists explicitly:
// the repeating value (21) and the final value (16).
func TestTStateLDIRFinalAnchor(t *testing.T) {
	// n=1: the only iteration is the final one => body = 16.
	mac := z80h.New()
	mac.Write(progOrg, []byte{0xED, 0xB0, 0xC9})
	res, _ := mac.RunFrom(progOrg, z80h.Entry{HL: 0xA000, DE: 0xB000, BC: 1})
	if got := res.TStates - retT; got != 16 {
		t.Errorf("LDIR final iteration = %d, want 16", got)
	}
	// n=2: first iteration repeats (21), second is final (16) => body = 37.
	mac2 := z80h.New()
	mac2.Write(progOrg, []byte{0xED, 0xB0, 0xC9})
	res2, _ := mac2.RunFrom(progOrg, z80h.Entry{HL: 0xA000, DE: 0xB000, BC: 2})
	if got := res2.TStates - retT; got != 21+16 {
		t.Errorf("LDIR 2-byte = %d, want %d (21 repeating + 16 final)", got, 21+16)
	}
}

// TestTStateComposite asserts two multi-instruction programs match a by-hand sum
// of the anchor values — proving the accumulation is exact across a sequence.
func TestTStateComposite(t *testing.T) {
	// Program A: LD BC,nn (10) ; INC BC (6) ; ADD HL,BC (11) ; LD A,n (7) ;
	//            ADD A,B (4) ; NOP (4) = 42.
	progA := []byte{
		0x01, 0x34, 0x12, // LD BC,&1234
		0x03,       // INC BC
		0x09,       // ADD HL,BC
		0x3E, 0x07, // LD A,7
		0x80, // ADD A,B
		0x00, // NOP
	}
	if got := measureBody(t, progA, z80h.Entry{}); got != 10+6+11+7+4+4 {
		t.Errorf("composite A = %d, want %d", got, 10+6+11+7+4+4)
	}

	// Program B mixes a CB op and a prefixed op:
	//   LD A,n (7) ; CB RLC B (8) ; DD LD IX,nn (14) ; PUSH BC (11) ;
	//   POP BC (10) ; EX DE,HL (4) = 54.
	progB := []byte{
		0x3E, 0x01, // LD A,1
		0xCB, 0x00, // RLC B
		0xDD, 0x21, 0x00, 0x90, // LD IX,&9000
		0xC5, // PUSH BC
		0xC1, // POP BC
		0xEB, // EX DE,HL
	}
	if got := measureBody(t, progB, z80h.Entry{}); got != 7+8+14+11+10+4 {
		t.Errorf("composite B = %d, want %d", got, 7+8+14+11+10+4)
	}
}
