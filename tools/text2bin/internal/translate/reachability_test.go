package translate

import (
	"bytes"
	"testing"

	emit "github.com/petemoore/sam-aarch64/tools/bin2text/emit"
	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

func handCraftedFiles() [][]byte {
	var out [][]byte

	// File 0: every register operand kind.
	{
		st := format.NewSymbolTable()
		var ow format.OperandWriter
		ow.WriteReg(format.OpRegX, 0)
		ow.WriteReg(format.OpRegW, 1)
		ow.WriteReg(format.OpRegXSP, 31)
		ow.WriteReg(format.OpRegWSP, 31)
		var rw format.RecordWriter
		id, _ := format.MnemonicID("mov")
		rw.WriteInst(id, 4, ow.Bytes())
		var buf bytes.Buffer
		format.WriteFile(&buf, st, rw.Bytes())
		out = append(out, buf.Bytes())
	}

	// File 1: every cond code via csel.
	{
		st := format.NewSymbolTable()
		var rw format.RecordWriter
		id, _ := format.MnemonicID("csel")
		for c := byte(0); c < 16; c++ {
			var ow format.OperandWriter
			ow.WriteReg(format.OpRegX, 0)
			ow.WriteReg(format.OpRegX, 1)
			ow.WriteReg(format.OpRegX, 2)
			ow.WriteCond(format.CondCode(c))
			rw.WriteInst(id, 4, ow.Bytes())
		}
		var buf bytes.Buffer
		format.WriteFile(&buf, st, rw.Bytes())
		out = append(out, buf.Bytes())
	}

	// File 2: all seven memory shapes.
	{
		st := format.NewSymbolTable()
		var rw format.RecordWriter
		id, _ := format.MnemonicID("ldr")
		shapes := []func(*format.OperandWriter){
			func(ow *format.OperandWriter) { ow.WriteMemBase(1) },
			func(ow *format.OperandWriter) {
				var ew format.ExprWriter
				ew.WriteImm(8)
				ow.WriteMemBaseOff(format.MemBaseOff, 1, ew.Bytes())
			},
			func(ow *format.OperandWriter) {
				var ew format.ExprWriter
				ew.WriteImm(8)
				ow.WriteMemBaseOff(format.MemBaseOffPre, 1, ew.Bytes())
			},
			func(ow *format.OperandWriter) {
				var ew format.ExprWriter
				ew.WriteImm(8)
				ow.WriteMemBaseOff(format.MemBaseOffPost, 1, ew.Bytes())
			},
			func(ow *format.OperandWriter) { ow.WriteMemBaseIdx(1, 2, 1) },
			func(ow *format.OperandWriter) { ow.WriteMemBaseIdxShifted(1, 2, 1, 3) },
			func(ow *format.OperandWriter) {
				ow.WriteMemBaseIdxExtended(1, 2, 0, format.ExtUXTW, 2)
			},
		}
		for _, build := range shapes {
			var ow format.OperandWriter
			ow.WriteReg(format.OpRegX, 0)
			build(&ow)
			rw.WriteInst(id, 2, ow.Bytes())
		}
		var buf bytes.Buffer
		format.WriteFile(&buf, st, rw.Bytes())
		out = append(out, buf.Bytes())
	}

	// File 3: shifted register, all four shift kinds.
	{
		st := format.NewSymbolTable()
		var rw format.RecordWriter
		id, _ := format.MnemonicID("add")
		for _, sk := range []format.ShiftKind{
			format.ShiftLSL, format.ShiftLSR, format.ShiftASR, format.ShiftROR,
		} {
			var ow format.OperandWriter
			ow.WriteReg(format.OpRegX, 0)
			ow.WriteReg(format.OpRegX, 1)
			var ew format.ExprWriter
			ew.WriteImm(4)
			ow.WriteShiftedReg(1, 2, sk, ew.Bytes())
			rw.WriteInst(id, 3, ow.Bytes())
		}
		var buf bytes.Buffer
		format.WriteFile(&buf, st, rw.Bytes())
		out = append(out, buf.Bytes())
	}

	// File 4: extended register, all eight extend kinds.
	// uxtb/uxth/uxtw/sxtb/sxth/sxtw expect W register (width=0);
	// uxtx/sxtx expect X register (width=1).
	{
		st := format.NewSymbolTable()
		var rw format.RecordWriter
		id, _ := format.MnemonicID("add")
		for e := byte(0); e < 8; e++ {
			var ow format.OperandWriter
			ow.WriteReg(format.OpRegX, 0)
			ow.WriteReg(format.OpRegX, 1)
			width := byte(0) // W default
			if e == 3 || e == 7 { // UXTX or SXTX
				width = 1
			}
			ow.WriteExtendedReg(width, 2, format.ExtendKind(e), nil)
			rw.WriteInst(id, 3, ow.Bytes())
		}
		var buf bytes.Buffer
		format.WriteFile(&buf, st, rw.Bytes())
		out = append(out, buf.Bytes())
	}

	// File 5: symbol references + relocation operators.
	{
		st := format.NewSymbolTable()
		msgID := st.Intern("msg")
		var rw format.RecordWriter

		// Bare symbol ref via `b target`.
		{
			var ew format.ExprWriter
			ew.WriteSym(msgID)
			var ow format.OperandWriter
			ow.WriteImmExpr(ew.Bytes())
			bid, _ := format.MnemonicID("b")
			rw.WriteInst(bid, 1, ow.Bytes())
		}
		// :lo12: relocation.
		{
			var ew format.ExprWriter
			ew.WriteSym(msgID)
			ew.WriteOp(format.OpRelLo12)
			var ow format.OperandWriter
			ow.WriteReg(format.OpRegX, 0)
			ow.WriteReg(format.OpRegX, 1)
			ow.WriteImmExpr(ew.Bytes())
			aid, _ := format.MnemonicID("add")
			rw.WriteInst(aid, 3, ow.Bytes())
		}

		var buf bytes.Buffer
		format.WriteFile(&buf, st, rw.Bytes())
		out = append(out, buf.Bytes())
	}

	// File 6: labels + local labels + comments interleaved.
	{
		st := format.NewSymbolTable()
		mainID := st.Intern("main")
		var rw format.RecordWriter
		rw.WriteComment(0, []byte(" banner"))
		rw.WriteLabelDef(mainID)
		rw.WriteLocalDef(1)
		nopID, _ := format.MnemonicID("nop")
		rw.WriteInst(nopID, 0, nil)
		rw.WriteComment(1, []byte(" trailing"))
		retID, _ := format.MnemonicID("ret")
		rw.WriteInst(retID, 0, nil)
		var buf bytes.Buffer
		format.WriteFile(&buf, st, rw.Bytes())
		out = append(out, buf.Bytes())
	}

	// File 7: directives — every one in the table that takes operands.
	{
		st := format.NewSymbolTable()
		var rw format.RecordWriter

		// .byte 1, 2, 3
		{
			var ow format.OperandWriter
			for _, v := range []int64{1, 2, 3} {
				var ew format.ExprWriter
				ew.WriteImm(v)
				ow.WriteImmExpr(ew.Bytes())
			}
			id, _ := format.DirectiveID(".byte")
			rw.WriteDirective(id, 3, ow.Bytes())
		}
		// .ascii "hi"
		{
			var ow format.OperandWriter
			ow.WriteString([]byte("hi"))
			id, _ := format.DirectiveID(".ascii")
			rw.WriteDirective(id, 1, ow.Bytes())
		}

		var buf bytes.Buffer
		format.WriteFile(&buf, st, rw.Bytes())
		out = append(out, buf.Bytes())
	}

	return out
}

func TestReachabilityRoundtrip(t *testing.T) {
	for i, bin := range handCraftedFiles() {
		canon, err := emit.Emit(bin)
		if err != nil {
			t.Errorf("file %d: emit %v", i, err)
			continue
		}
		bin2, err := Translate(canon, "synth.s")
		if err != nil {
			t.Errorf("file %d: translate %v", i, err)
			continue
		}
		if !bytes.Equal(bin, bin2) {
			t.Errorf("file %d not round-trippable:\n bin  = % X\n bin2 = % X\n canon = %s",
				i, bin, bin2, string(canon))
		}
	}
}
