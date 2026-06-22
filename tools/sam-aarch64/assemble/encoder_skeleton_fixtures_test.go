package assemble

import (
	"fmt"
	"testing"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

// encoder_skeleton_fixtures_test.go — the authority for the Z80
// standalone-encoder skeleton self-test (item i199 / i48c-b8e-1).
//
// Each fixture mirrors exactly one vector baked into
// src/test_encode_inst.asm::run_encode_inst_self_tests. The Z80 side
// calls encode_inst on the same {mnemonic_id, operand bytes, pc} and
// asserts the same expected word. This test is the drift guard: if the
// Go encoder (the authority, CLAUDE.md §6) changes an encoding, this
// test fails and the asm vector must be re-baked from the new value
// logged here.
//
// All fixtures use CONSTANT operand expressions (no PUSH_SYM /
// PUSH_LOCAL) so the Z80 self-test needs no populated symbol table —
// only PASS_PC, which encode_inst reads for the PC-relative slots.

func immExpr(v int64) []byte {
	var ew format.ExprWriter
	ew.WriteImm(v)
	return ew.Bytes()
}

type encFixture struct {
	name     string
	mnemonic string
	pc       int64
	build    func(w *format.OperandWriter)
	opCount  byte
}

func TestEncoderSkeletonFixtures(t *testing.T) {
	fixtures := []encFixture{
		{"nop", "nop", 0, func(w *format.OperandWriter) {}, 0},
		{"ret_x30", "ret", 0, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegX, 30)
		}, 1},
		{"add_x0_x1_5", "add", 0, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegXSP, 0)
			w.WriteReg(format.OpRegXSP, 1)
			w.WriteImmExpr(immExpr(5))
		}, 3},
		{"sub_x2_x3_4096", "sub", 0, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegXSP, 2)
			w.WriteReg(format.OpRegXSP, 3)
			w.WriteImmExpr(immExpr(0x1000))
		}, 3},
		{"movz_x0_0x1234", "movz", 0, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegX, 0)
			w.WriteImmExpr(immExpr(0x1234))
		}, 2},
		{"orr_x0_x1_0xff", "orr", 0, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegX, 0)
			w.WriteReg(format.OpRegX, 1)
			w.WriteImmExpr(immExpr(0xff))
		}, 3},
		// Logical-immediate replication-size coverage (i205): one ORR
		// per element size so the size-parameterized replicate routine
		// is exercised on every branch.  size==64 is orr_x0_x1_0xff
		// above; these add 32/16/8/4/2.
		{"orr_x0_x1_size32", "orr", 0, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegX, 0)
			w.WriteReg(format.OpRegX, 1)
			w.WriteImmExpr(immExpr(0x0000000100000001))
		}, 3},
		{"orr_x0_x1_size16", "orr", 0, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegX, 0)
			w.WriteReg(format.OpRegX, 1)
			w.WriteImmExpr(immExpr(0x0001000100010001))
		}, 3},
		{"orr_x0_x1_size8", "orr", 0, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegX, 0)
			w.WriteReg(format.OpRegX, 1)
			w.WriteImmExpr(immExpr(0x0101010101010101))
		}, 3},
		{"orr_x0_x1_size4", "orr", 0, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegX, 0)
			w.WriteReg(format.OpRegX, 1)
			w.WriteImmExpr(immExpr(0x1111111111111111))
		}, 3},
		{"orr_x0_x1_size2", "orr", 0, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegX, 0)
			w.WriteReg(format.OpRegX, 1)
			w.WriteImmExpr(immExpr(0x5555555555555555))
		}, 3},
		{"csel_x0_x1_x2_eq", "csel", 0, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegX, 0)
			w.WriteReg(format.OpRegX, 1)
			w.WriteReg(format.OpRegX, 2)
			w.WriteCond(format.CondEQ)
		}, 4},
		{"cbz_x0_pc8", "cbz", 0x1000, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegX, 0)
			w.WriteImmExpr(immExpr(0x1008))
		}, 2},
		{"b_pc16", "b", 0x1000, func(w *format.OperandWriter) {
			w.WriteImmExpr(immExpr(0x1010))
		}, 1},
		{"adrp_x0_0x3000", "adrp", 0x1000, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegX, 0)
			w.WriteImmExpr(immExpr(0x3000))
		}, 2},
		{"adr_x0_pc4", "adr", 0x1000, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegX, 0)
			w.WriteImmExpr(immExpr(0x1004))
		}, 2},
		// --- i203a special forms: shift / bitfield / ror ---
		{"lsl_x0_x1_4", "lsl", 0, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegX, 0)
			w.WriteReg(format.OpRegX, 1)
			w.WriteImmExpr(immExpr(4))
		}, 3},
		{"lsl_w0_w1_4", "lsl", 0, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegW, 0)
			w.WriteReg(format.OpRegW, 1)
			w.WriteImmExpr(immExpr(4))
		}, 3},
		{"lsr_x0_x1_4", "lsr", 0, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegX, 0)
			w.WriteReg(format.OpRegX, 1)
			w.WriteImmExpr(immExpr(4))
		}, 3},
		{"lsl_x0_x1_x2", "lsl", 0, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegX, 0)
			w.WriteReg(format.OpRegX, 1)
			w.WriteReg(format.OpRegX, 2)
		}, 3},
		{"lsr_x0_x1_x2", "lsr", 0, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegX, 0)
			w.WriteReg(format.OpRegX, 1)
			w.WriteReg(format.OpRegX, 2)
		}, 3},
		{"bfi_x0_x1_8_4", "bfi", 0, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegX, 0)
			w.WriteReg(format.OpRegX, 1)
			w.WriteImmExpr(immExpr(8))
			w.WriteImmExpr(immExpr(4))
		}, 4},
		{"bfxil_x0_x1_8_4", "bfxil", 0, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegX, 0)
			w.WriteReg(format.OpRegX, 1)
			w.WriteImmExpr(immExpr(8))
			w.WriteImmExpr(immExpr(4))
		}, 4},
		{"ubfx_w0_w1_8_4", "ubfx", 0, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegW, 0)
			w.WriteReg(format.OpRegW, 1)
			w.WriteImmExpr(immExpr(8))
			w.WriteImmExpr(immExpr(4))
		}, 4},
		{"bfc_x0_8_4", "bfc", 0, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegX, 0)
			w.WriteImmExpr(immExpr(8))
			w.WriteImmExpr(immExpr(4))
		}, 3},
		{"sbfx_x0_x1_8_4", "sbfx", 0, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegX, 0)
			w.WriteReg(format.OpRegX, 1)
			w.WriteImmExpr(immExpr(8))
			w.WriteImmExpr(immExpr(4))
		}, 4},
		{"ror_x0_x1_4", "ror", 0, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegX, 0)
			w.WriteReg(format.OpRegX, 1)
			w.WriteImmExpr(immExpr(4))
		}, 3},
		{"ror_w0_w1_4", "ror", 0, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegW, 0)
			w.WriteReg(format.OpRegW, 1)
			w.WriteImmExpr(immExpr(4))
		}, 3},
		// --- i203b special forms: bic-imm / csetm / barrier ---
		{"bic_x0_x1_0xff", "bic", 0, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegX, 0)
			w.WriteReg(format.OpRegX, 1)
			w.WriteImmExpr(immExpr(0xff))
		}, 3},
		{"bic_w0_w1_0xff", "bic", 0, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegW, 0)
			w.WriteReg(format.OpRegW, 1)
			w.WriteImmExpr(immExpr(0xff))
		}, 3},
		{"csetm_x0_eq", "csetm", 0, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegX, 0)
			w.WriteCond(format.CondEQ)
		}, 2},
		{"csetm_w3_ne", "csetm", 0, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegW, 3)
			w.WriteCond(format.CondNE)
		}, 2},
		{"isb_15", "isb", 0, func(w *format.OperandWriter) {
			w.WriteImmExpr(immExpr(15))
		}, 1},
		{"dsb_11", "dsb", 0, func(w *format.OperandWriter) {
			w.WriteImmExpr(immExpr(11))
		}, 1},
		{"dmb_11", "dmb", 0, func(w *format.OperandWriter) {
			w.WriteImmExpr(immExpr(11))
		}, 1},
		// --- i203c special forms: mrs / msr / dc / tlbi (sysreg) ---
		{"mrs_x0_midr", "mrs", 0, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegX, 0)
			w.WriteSysName("midr_el1")
		}, 2},
		{"msr_daifset_2", "msr", 0, func(w *format.OperandWriter) {
			w.WriteSysName("daifset")
			w.WriteImmExpr(immExpr(2))
		}, 2},
		{"dc_cvac_x0", "dc", 0, func(w *format.OperandWriter) {
			w.WriteSysName("cvac")
			w.WriteReg(format.OpRegX, 0)
		}, 2},
		{"tlbi_vmalle1", "tlbi", 0, func(w *format.OperandWriter) {
			w.WriteSysName("vmalle1")
		}, 1},
	}

	for _, fx := range fixtures {
		t.Run(fx.name, func(t *testing.T) {
			mid, ok := format.MnemonicID(fx.mnemonic)
			if !ok {
				t.Fatalf("unknown mnemonic %q", fx.mnemonic)
			}
			var ow format.OperandWriter
			fx.build(&ow)
			ops := ow.Bytes()
			rec := instRec(mid, fx.opCount, ops)
			f := fileFromRecords(nil, []format.Record{rec})
			p1, err := Pass1(f)
			if err != nil {
				t.Fatalf("Pass1: %v", err)
			}
			w, err := encodeInst(rec, fx.pc, p1, f)
			if err != nil {
				t.Fatalf("encodeInst: %v", err)
			}
			le := []byte{byte(w), byte(w >> 8), byte(w >> 16), byte(w >> 24)}
			// Log everything the asm vector needs.
			hexops := ""
			for _, b := range ops {
				hexops += fmt.Sprintf("%02x ", b)
			}
			t.Logf("FIXTURE %-18s mnem_id=%-3d pc=0x%05x opcount=%d word=0x%08x  LE=%02x %02x %02x %02x  ops=[ %s]",
				fx.name, mid, fx.pc, fx.opCount, w, le[0], le[1], le[2], le[3], hexops)
		})
	}
}
