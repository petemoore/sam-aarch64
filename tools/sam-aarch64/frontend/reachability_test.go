package frontend

import (
	"reflect"
	"testing"

	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
	emit "github.com/petemoore/sam-aarch64/tools/sam-aarch64/render"
)

// handCraftedFiles returns in-memory symbolic Files covering every operand /
// record shape the renderer + parser must round-trip. Each is built directly
// as the front-end's in-memory IR (records are never serialized).
func handCraftedFiles() []*format.File {
	var out []*format.File

	// File 0: every register operand kind.
	{
		st := format.NewSymbolTable()
		var ow format.OperandWriter
		ow.WriteReg(format.OpRegX, 0)
		ow.WriteReg(format.OpRegW, 1)
		ow.WriteReg(format.OpRegXSP, 31)
		ow.WriteReg(format.OpRegWSP, 31)
		id, _ := format.MnemonicID("mov")
		out = append(out, fileFromRecords(st.Names(), []format.Record{instRec(id, 4, ow.Bytes())}))
	}

	// File 1: every cond code via csel.
	{
		st := format.NewSymbolTable()
		var recs []format.Record
		id, _ := format.MnemonicID("csel")
		for c := byte(0); c < 16; c++ {
			var ow format.OperandWriter
			ow.WriteReg(format.OpRegX, 0)
			ow.WriteReg(format.OpRegX, 1)
			ow.WriteReg(format.OpRegX, 2)
			ow.WriteCond(format.CondCode(c))
			recs = append(recs, instRec(id, 4, ow.Bytes()))
		}
		out = append(out, fileFromRecords(st.Names(), recs))
	}

	// File 2: all seven memory shapes.
	{
		st := format.NewSymbolTable()
		var recs []format.Record
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
			recs = append(recs, instRec(id, 2, ow.Bytes()))
		}
		out = append(out, fileFromRecords(st.Names(), recs))
	}

	// File 3: shifted register, all four shift kinds.
	{
		st := format.NewSymbolTable()
		var recs []format.Record
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
			recs = append(recs, instRec(id, 3, ow.Bytes()))
		}
		out = append(out, fileFromRecords(st.Names(), recs))
	}

	// File 4: extended register, all eight extend kinds.
	// uxtb/uxth/uxtw/sxtb/sxth/sxtw expect W register (width=0);
	// uxtx/sxtx expect X register (width=1).
	{
		st := format.NewSymbolTable()
		var recs []format.Record
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
			recs = append(recs, instRec(id, 3, ow.Bytes()))
		}
		out = append(out, fileFromRecords(st.Names(), recs))
	}

	// File 5: symbol references + relocation operators.
	{
		st := format.NewSymbolTable()
		msgID := st.Intern("msg")
		var recs []format.Record

		// Bare symbol ref via `b target`.
		{
			var ew format.ExprWriter
			ew.WriteSym(msgID)
			var ow format.OperandWriter
			ow.WriteImmExpr(ew.Bytes())
			bid, _ := format.MnemonicID("b")
			recs = append(recs, instRec(bid, 1, ow.Bytes()))
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
			recs = append(recs, instRec(aid, 3, ow.Bytes()))
		}

		out = append(out, fileFromRecords(st.Names(), recs))
	}

	// File 6: labels + local labels + comments interleaved.
	{
		st := format.NewSymbolTable()
		mainID := st.Intern("main")
		nopID, _ := format.MnemonicID("nop")
		retID, _ := format.MnemonicID("ret")
		out = append(out, fileFromRecords(st.Names(), []format.Record{
			commentRec(0, []byte(" banner")),
			labelRec(mainID),
			localRec(1),
			instRec(nopID, 0, nil),
			commentRec(1, []byte(" trailing")),
			instRec(retID, 0, nil),
		}))
	}

	// File 7: directives — every one in the table that takes operands.
	{
		st := format.NewSymbolTable()
		var recs []format.Record

		// .byte 1, 2, 3
		{
			var ow format.OperandWriter
			for _, v := range []int64{1, 2, 3} {
				var ew format.ExprWriter
				ew.WriteImm(v)
				ow.WriteImmExpr(ew.Bytes())
			}
			id, _ := format.DirectiveID(".byte")
			recs = append(recs, dirRec(id, 3, ow.Bytes()))
		}
		// .ascii "hi"
		{
			var ow format.OperandWriter
			ow.WriteString([]byte("hi"))
			id, _ := format.DirectiveID(".ascii")
			recs = append(recs, dirRec(id, 1, ow.Bytes()))
		}

		out = append(out, fileFromRecords(st.Names(), recs))
	}

	return out
}

// TestReachabilityRoundtrip renders each hand-crafted File to text and
// re-parses it, asserting the in-memory IR (records + names) round-trips.
func TestReachabilityRoundtrip(t *testing.T) {
	for i, f := range handCraftedFiles() {
		canon, err := emit.EmitFile(f)
		if err != nil {
			t.Errorf("file %d: emit %v", i, err)
			continue
		}
		f2, err := Translate(canon, "synth.s")
		if err != nil {
			t.Errorf("file %d: translate %v", i, err)
			continue
		}
		if !reflect.DeepEqual(f.Records, f2.Records) {
			t.Errorf("file %d not round-trippable:\n recs  = %+v\n recs2 = %+v\n canon = %s",
				i, f.Records, f2.Records, string(canon))
		}
		if !reflect.DeepEqual(f.Names, f2.Names) {
			t.Errorf("file %d names not round-trippable:\n names  = %v\n names2 = %v",
				i, f.Names, f2.Names)
		}
	}
}
