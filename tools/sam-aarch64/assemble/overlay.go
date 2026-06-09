package assemble

import (
	"fmt"

	enc "github.com/petemoore/sam-aarch64/tools/aarch64enc"
	format "github.com/petemoore/sam-aarch64/tools/sam-aarch64-format"
)

// encodeInstOverlay turns one symbol/PC-bearing instruction at its true PC
// into a compact `.tbn` v2 overlay element: its assembled base word with the
// relocated field zeroed, plus a {slot, expression} patch. Pass 2 (and the
// Z80 decoder) re-fold the expression at the instruction's PC and OR the
// result into the base word — recovering the exact assembled word.
//
// The base word is the literal encoding at the true PC with the relocated
// field cleared (ZeroSlot); the non-field bits are PC-invariant. assertOverlay
// then confirms base|Fold == the literal encoder's word at the true PC — a
// deterministic byte-match guard at compaction time, per instruction.
func encodeInstOverlay(rec format.Record, pc int64, p1 *Pass1Result, f *format.File) (uint32, []format.InsnPatch, error) {
	slot, expr, isLitpool, litWidth, rt, err := overlayClassify(rec, pc, p1, f)
	if err != nil {
		return 0, nil, err
	}
	patch := format.InsnPatch{Slot: byte(slot), Expr: expr}

	if isLitpool {
		// `ldr Xt|Wt, =expr`: the base is the ldr-literal opcode + Rt with
		// imm19 zeroed. The pool slot's PC (and thus imm19) is resolved at
		// assemble time from the instruction's PC via the litpool table.
		base := uint32(0x18000000)
		if litWidth == 8 {
			base = 0x58000000
		}
		base |= uint32(rt)
		return base, []format.InsnPatch{patch}, nil
	}

	full, err := encodeInst(rec, pc, p1, f)
	if err != nil {
		return 0, nil, fmt.Errorf("overlay: encode @0x%x (mnem %d): %w", uint64(pc), rec.MnemonicID, err)
	}
	base := enc.ZeroSlot(full, slot)

	if err := assertOverlay(rec, base, slot, expr, pc, p1, f); err != nil {
		return 0, nil, err
	}
	return base, []format.InsnPatch{patch}, nil
}

// assertOverlay verifies that base | Fold(eval(expr,pc),pc,base) reproduces
// the literal encoder's word at the instruction's PC. Fold (aarch64enc) and
// the literal encoder (operandsToValues + slot encoders) are independent
// computations of the same field, so a match proves the overlay slot and
// fold-rule agree with the encoder — never a silently-wrong byte.
func assertOverlay(rec format.Record, base uint32, slot enc.FoldSlot, expr []byte, pc int64, p1 *Pass1Result, f *format.File) error {
	full, err := encodeInst(rec, pc, p1, f)
	if err != nil {
		return fmt.Errorf("overlay assert: encode @0x%x (mnem %d): %w", uint64(pc), rec.MnemonicID, err)
	}
	v, err := EvalUsage(expr, makeCtx(pc, p1, f), usage)
	if err != nil {
		return fmt.Errorf("overlay assert: eval @0x%x: %w", uint64(pc), err)
	}
	bits, err := enc.Fold(slot, v, pc, base)
	if err != nil {
		return fmt.Errorf("overlay assert: fold(slot %d) @0x%x (mnem %d): %w", slot, uint64(pc), rec.MnemonicID, err)
	}
	if base|bits != full {
		return fmt.Errorf("overlay assert: mnem %d slot %d @0x%x: base|fold=%#08x, encoder=%#08x",
			rec.MnemonicID, slot, uint64(pc), base|bits, full)
	}
	return nil
}

// overlayClassify mirrors encodeInst's dispatch (pass2.go) to determine,
// for a symbol/PC-bearing instruction, which overlay slot its relocated
// field uses and which operand expression drives it. rt is the destination
// register (only meaningful for the litpool case). It returns an error for
// any symbolic operand shape not covered by the overlay slot set — a loud
// gap, never a silent miss.
func overlayClassify(rec format.Record, pc int64, p1 *Pass1Result, f *format.File) (slot enc.FoldSlot, expr []byte, isLitpool bool, litWidth byte, rt byte, err error) {
	var ops []format.Operand
	var kinds []format.OperandKind
	or := format.NewOperandReader(rec.Operands)
	for !or.AtEnd() {
		o, e := or.Next()
		if e != nil {
			return 0, nil, false, 0, 0, e
		}
		ops = append(ops, o)
		kinds = append(kinds, o.Kind)
	}

	// Compound-operand forms (encodeInst dispatches these first).
	for _, k := range kinds {
		switch k {
		case format.OpMem:
			return classifyMem(rec, ops, pc, p1, f)
		case format.OpShiftedReg, format.OpExtendedReg:
			return 0, nil, false, 0, 0, fmt.Errorf("overlay: symbolic %v operand unsupported (mnem %d)", k, rec.MnemonicID)
		case format.OpLitPool:
			for _, o := range ops {
				if o.Kind == format.OpLitPool {
					return enc.FoldLitpool19, o.Expr, true, o.Width, ops[0].Reg, nil
				}
			}
		}
	}

	movID, _ := format.MnemonicID("mov")
	if rec.MnemonicID == movID && len(ops) == 2 &&
		(ops[0].Kind == format.OpRegX || ops[0].Kind == format.OpRegW) &&
		ops[1].Kind == format.OpImmExpr {
		return classifyMovImm(ops, pc, p1, f)
	}

	ldrID, _ := format.MnemonicID("ldr")
	if rec.MnemonicID == ldrID && len(ops) == 2 &&
		(ops[0].Kind == format.OpRegX || ops[0].Kind == format.OpRegW) &&
		ops[1].Kind == format.OpImmExpr {
		// ldr Xt, label — PC-relative literal load (imm19 = (label-pc)/4).
		return enc.FoldBranch19, ops[1].Expr, false, 0, 0, nil
	}

	if rec.MnemonicID == 22 || rec.MnemonicID == 23 { // tbz/tbnz
		if len(ops) < 3 {
			return 0, nil, false, 0, 0, fmt.Errorf("overlay: tbz/tbnz needs 3 operands")
		}
		return enc.FoldBranch14, ops[2].Expr, false, 0, 0, nil
	}

	// Special-form intercepts (lsl/lsr, bitfield, bic-imm, csetm, barriers,
	// ror-imm, mrs/msr/dc/tlbi): these carry constant or sys-name operands,
	// never a relocatable expression in the spectrum4 corpus. If one ever
	// does, fail loudly rather than mis-encode.
	switch rec.MnemonicID {
	case 17, 18, 49, 50, 51, 83, 84, 47, 52, 66, 67, 68, 70, 76, 77, 78, 79:
		return 0, nil, false, 0, 0, fmt.Errorf("overlay: mnem %d (special form) has no overlay slot for a symbolic operand", rec.MnemonicID)
	}

	// Generic form-table path: the relocated operand is the IMM_EXPR in a
	// relocatable slot whose expression references a symbol/local/PC/reloc,
	// or whose slot is PC-dependent (a constant PC-relative target — e.g.
	// `b 0x1000` — is structurally literal yet still needs a patch).
	form, ok, diag := enc.ValidateOperandKinds(rec.MnemonicID, kinds)
	if !ok {
		return 0, nil, false, 0, 0, fmt.Errorf("overlay: %s", diag)
	}
	for i, o := range ops {
		if o.Kind != format.OpImmExpr || i >= len(form.Slots) {
			continue
		}
		fs, ok := enc.FoldSlotForKind(form.Slots[i].SlotKind)
		if !ok {
			continue
		}
		if exprContextDep(o.Expr) || foldSlotPCDependent(fs) {
			return fs, o.Expr, false, 0, 0, nil
		}
	}
	return 0, nil, false, 0, 0, fmt.Errorf("overlay: no relocated operand found (mnem %d)", rec.MnemonicID)
}

// foldSlotPCDependent reports whether a slot's fold depends on the
// instruction's PC (so a constant target in that slot still needs a patch).
func foldSlotPCDependent(fs enc.FoldSlot) bool {
	switch fs {
	case enc.FoldBranch26, enc.FoldBranch19, enc.FoldBranch14,
		enc.FoldAdr, enc.FoldAdrp, enc.FoldLitpool19:
		return true
	}
	return false
}

// classifyMem maps a load/store with a symbolic memory offset to its slot.
func classifyMem(rec format.Record, ops []format.Operand, pc int64, p1 *Pass1Result, f *format.File) (enc.FoldSlot, []byte, bool, byte, byte, error) {
	// ldp/stp pair: scaled imm7 offset.
	if (rec.MnemonicID == 7 || rec.MnemonicID == 8) && len(ops) >= 3 && ops[2].Kind == format.OpMem {
		mem := ops[2]
		if len(mem.Expr) == 0 {
			return 0, nil, false, 0, 0, fmt.Errorf("overlay: pair with no offset expr")
		}
		return enc.FoldPairImm7, mem.Expr, false, 0, 0, nil
	}

	var mem format.Operand
	found := false
	for _, o := range ops {
		if o.Kind == format.OpMem {
			mem = o
			found = true
			break
		}
	}
	if !found || len(mem.Expr) == 0 {
		return 0, nil, false, 0, 0, fmt.Errorf("overlay: mem operand has no symbolic offset (mnem %d)", rec.MnemonicID)
	}

	// stur/ldur and the halfword/byte variants are always unscaled imm9.
	if isUnscaledMemMnemonic(rec.MnemonicID) {
		return enc.FoldMemImm9, mem.Expr, false, 0, 0, nil
	}

	switch mem.MemShape {
	case format.MemBaseOff:
		// Scaled unsigned imm12, unless the offset can't be a scaled imm12
		// (negative / non-multiple) and GNU's unscaled-imm9 rewrite applies.
		v, err := EvalUsage(mem.Expr, makeCtx(pc, p1, f), usage)
		if err != nil {
			return 0, nil, false, 0, 0, err
		}
		_, scale := memInstSize(rec.MnemonicID, ops[0].Kind)
		if v < 0 || v%scale != 0 || v/scale >= (1<<12) {
			if v >= -256 && v <= 255 {
				return enc.FoldMemImm9, mem.Expr, false, 0, 0, nil
			}
			return 0, nil, false, 0, 0, fmt.Errorf("overlay: mem offset %d not encodable (mnem %d)", v, rec.MnemonicID)
		}
		return enc.FoldMemImm12, mem.Expr, false, 0, 0, nil
	case format.MemBaseOffPre, format.MemBaseOffPost:
		return enc.FoldMemImm9, mem.Expr, false, 0, 0, nil
	}
	return 0, nil, false, 0, 0, fmt.Errorf("overlay: mem shape %v has no symbolic encoding (mnem %d)", mem.MemShape, rec.MnemonicID)
}

// classifyMovImm decides whether a symbol-bearing `mov Rd, #expr` lands in
// the movz imm16 field or the orr-immediate (logical) field, replicating
// tryEncodeMovImm's selection (pass2.go).
func classifyMovImm(ops []format.Operand, pc int64, p1 *Pass1Result, f *format.File) (enc.FoldSlot, []byte, bool, byte, byte, error) {
	imm, err := EvalUsage(ops[1].Expr, makeCtx(pc, p1, f), usage)
	if err != nil {
		return 0, nil, false, 0, 0, err
	}
	is64 := ops[0].Kind == format.OpRegX
	u := uint64(imm)
	if !is64 {
		u &= 0xffffffff
	}
	for shift := uint(0); shift < 64; shift += 16 {
		if !is64 && shift >= 32 {
			break
		}
		chunk := (u >> shift) & 0xffff
		if chunk<<shift == u {
			return enc.FoldMovzAuto, ops[1].Expr, false, 0, 0, nil
		}
	}
	slot := enc.OperandSlot{SlotKind: enc.LogicalImm, BitPosition: 10, BitWidth: 13}
	if _, lerr := enc.EncodeLogicalImmPub(slot, imm, is64); lerr == nil {
		return enc.FoldLogical, ops[1].Expr, false, 0, 0, nil
	}
	return 0, nil, false, 0, 0, fmt.Errorf("overlay: mov #%#x has no movz/orr overlay encoding (movn unsupported)", uint64(imm))
}

// exprContextDep reports whether an expression bytecode stream references a
// symbol, local label, the PC, or a relocation — i.e. it is the symbolic
// operand of an instruction. (A local mirror of the format package's
// internal predicate; a malformed stream is treated as context-dependent.)
func exprContextDep(buf []byte) bool {
	r := format.NewExprReader(buf)
	for !r.AtEnd() {
		op, _, err := r.Next()
		if err != nil {
			return true
		}
		switch op {
		case format.OpPushSym, format.OpPushLocal, format.OpPushPC,
			format.OpRelLo12, format.OpRelHi12,
			format.OpRelAbsG0, format.OpRelAbsG0NC,
			format.OpRelAbsG1, format.OpRelAbsG1NC,
			format.OpRelAbsG2, format.OpRelAbsG2NC,
			format.OpRelAbsG3:
			return true
		}
	}
	return false
}
