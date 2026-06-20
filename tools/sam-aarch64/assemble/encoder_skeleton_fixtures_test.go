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
		// --- i203d special forms: mov-imm autoselect / ldr-lit / tbz/tbnz ---
		{"mov_x0_movz", "mov", 0, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegX, 0)
			w.WriteImmExpr(immExpr(0x12340000))
		}, 2},
		{"mov_x0_movn", "mov", 0, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegX, 0)
			w.WriteImmExpr(immExpr(-1))
		}, 2},
		{"mov_w1_movn", "mov", 0, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegW, 1)
			w.WriteImmExpr(immExpr(-2)) // 0xfffffffe (W) -> movn #1
		}, 2},
		{"mov_x2_orr", "mov", 0, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegX, 2)
			w.WriteImmExpr(immExpr(0x5555555555555555))
		}, 2},
		{"ldr_x0_lit", "ldr", 0x1000, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegX, 0)
			w.WriteImmExpr(immExpr(0x1008))
		}, 2},
		{"ldr_w1_lit", "ldr", 0x1000, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegW, 1)
			w.WriteImmExpr(immExpr(0x1010))
		}, 2},
		{"tbz_x0_5", "tbz", 0x1000, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegX, 0)
			w.WriteImmExpr(immExpr(5))
			w.WriteImmExpr(immExpr(0x1010))
		}, 3},
		{"tbnz_w1_3", "tbnz", 0x1000, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegW, 1)
			w.WriteImmExpr(immExpr(3))
			w.WriteImmExpr(immExpr(0x1008))
		}, 3},
		// --- i201a compound-operand forms: shifted-reg + extended-reg ---
		// Shifted-reg explicit (OpShiftedReg operand present): add x0,x1,x2,lsl#3
		{"add_x0_x1_x2_lsl3", "add", 0, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegX, 0)
			w.WriteReg(format.OpRegX, 1)
			w.WriteShiftedReg(1, 2, format.ShiftLSL, immExpr(3))
		}, 3},
		// Shifted-reg with LSR: sub x3,x4,x5,lsr#7
		{"sub_x3_x4_x5_lsr7", "sub", 0, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegX, 3)
			w.WriteReg(format.OpRegX, 4)
			w.WriteShiftedReg(1, 5, format.ShiftLSR, immExpr(7))
		}, 3},
		// Shifted-reg with ASR (W form): and w6,w7,w8,asr#2
		{"and_w6_w7_w8_asr2", "and", 0, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegW, 6)
			w.WriteReg(format.OpRegW, 7)
			w.WriteShiftedReg(0, 8, format.ShiftASR, immExpr(2))
		}, 3},
		// Shifted-reg with ROR: orr x9,x10,x11,ror#15
		{"orr_x9_x10_x11_ror15", "orr", 0, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegX, 9)
			w.WriteReg(format.OpRegX, 10)
			w.WriteShiftedReg(1, 11, format.ShiftROR, immExpr(15))
		}, 3},
		// Shifted-reg: eor x12,x13,x14,lsl#0
		{"eor_x12_x13_x14_lsl0", "eor", 0, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegX, 12)
			w.WriteReg(format.OpRegX, 13)
			w.WriteShiftedReg(1, 14, format.ShiftLSL, immExpr(0))
		}, 3},
		// Shifted-reg: bic x0,x1,x2,lsl#0 (N=1)
		{"bic_x0_x1_x2_lsl0", "bic", 0, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegX, 0)
			w.WriteReg(format.OpRegX, 1)
			w.WriteShiftedReg(1, 2, format.ShiftLSL, immExpr(0))
		}, 3},
		// Shifted-reg: subs x3,x4,x5,lsl#0
		{"subs_x3_x4_x5_lsl0", "subs", 0, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegX, 3)
			w.WriteReg(format.OpRegX, 4)
			w.WriteShiftedReg(1, 5, format.ShiftLSL, immExpr(0))
		}, 3},
		// Shifted-reg: ands w0,w1,w2,lsl#0
		{"ands_w0_w1_w2_lsl0", "ands", 0, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegW, 0)
			w.WriteReg(format.OpRegW, 1)
			w.WriteShiftedReg(0, 2, format.ShiftLSL, immExpr(0))
		}, 3},
		// 3-reg coercion (no explicit shift): add x0,x1,x2 -> LSL#0
		{"add_x0_x1_x2_coerce", "add", 0, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegX, 0)
			w.WriteReg(format.OpRegX, 1)
			w.WriteReg(format.OpRegX, 2)
		}, 3},
		// 3-reg coercion (W): sub w3,w4,w5 -> LSL#0
		{"sub_w3_w4_w5_coerce", "sub", 0, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegW, 3)
			w.WriteReg(format.OpRegW, 4)
			w.WriteReg(format.OpRegW, 5)
		}, 3},
		// 2-reg tst coercion: tst x0,x1 -> shifted-reg LSL#0, Rd=xzr
		{"tst_x0_x1_coerce", "tst", 0, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegX, 0)
			w.WriteReg(format.OpRegX, 1)
		}, 2},
		// 2-reg tst coercion (W): tst w2,w3
		{"tst_w2_w3_coerce", "tst", 0, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegW, 2)
			w.WriteReg(format.OpRegW, 3)
		}, 2},
		// tst with explicit shifted-reg: tst x0,x1,lsl#3
		{"tst_x0_x1_lsl3", "tst", 0, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegX, 0)
			w.WriteShiftedReg(1, 1, format.ShiftLSL, immExpr(3))
		}, 2},
		// Extended-reg: add x0,x1,w2,uxtw#2
		{"add_x0_x1_w2_uxtw2", "add", 0, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegX, 0)
			w.WriteReg(format.OpRegX, 1)
			w.WriteExtendedReg(0, 2, format.ExtUXTW, immExpr(2))
		}, 3},
		// Extended-reg: add x3,x4,x5,sxtx#1
		{"add_x3_x4_x5_sxtx1", "add", 0, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegX, 3)
			w.WriteReg(format.OpRegX, 4)
			w.WriteExtendedReg(1, 5, format.ExtSXTX, immExpr(1))
		}, 3},
		// Extended-reg: sub x6,x7,w8,uxtb#0
		{"sub_x6_x7_w8_uxtb0", "sub", 0, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegX, 6)
			w.WriteReg(format.OpRegX, 7)
			w.WriteExtendedReg(0, 8, format.ExtUXTB, immExpr(0))
		}, 3},
		// Extended-reg: sub x9,x10,x11,sxth#3
		{"sub_x9_x10_x11_sxth3", "sub", 0, func(w *format.OperandWriter) {
			w.WriteReg(format.OpRegX, 9)
			w.WriteReg(format.OpRegX, 10)
			w.WriteExtendedReg(1, 11, format.ExtSXTH, immExpr(3))
		}, 3},
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
