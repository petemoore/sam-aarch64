package format

import (
	"encoding/binary"
	"fmt"
)

// OperandKind tags each operand with its on-disk shape (§4).
type OperandKind byte

const (
	OpRegX        OperandKind = 0x01
	OpRegW        OperandKind = 0x02
	OpRegXSP      OperandKind = 0x03
	OpRegWSP      OperandKind = 0x04
	OpImmExpr     OperandKind = 0x05
	OpShiftedReg  OperandKind = 0x06
	OpExtendedReg OperandKind = 0x07
	OpMem         OperandKind = 0x08
	OpString      OperandKind = 0x09
	OpCond        OperandKind = 0x0A
	OpSysName     OperandKind = 0x0B
	// OpLitPool is the `=expr` operand used by `ldr Xn, =expr` and
	// `ldr Wn, =expr`. The value is computed (in the case of a
	// constant) or resolved (in the case of a symbol) at link time
	// and stashed in the literal pool. Width is 8 bytes for X
	// destinations, 4 bytes for W destinations.
	OpLitPool OperandKind = 0x0C
)

func (k OperandKind) Name() string {
	switch k {
	case OpRegX:
		return "REG_X"
	case OpRegW:
		return "REG_W"
	case OpRegXSP:
		return "REG_X_SP"
	case OpRegWSP:
		return "REG_W_SP"
	case OpImmExpr:
		return "IMM_EXPR"
	case OpShiftedReg:
		return "SHIFTED_REG"
	case OpExtendedReg:
		return "EXTENDED_REG"
	case OpMem:
		return "MEM"
	case OpString:
		return "STRING"
	case OpCond:
		return "COND"
	case OpSysName:
		return "SYS_NAME"
	case OpLitPool:
		return "LIT_POOL"
	}
	return "UNKNOWN"
}

// MemShape sub-codes for the MEM operand (§4).
type MemShape byte

const (
	MemBase            MemShape = 0
	MemBaseOff         MemShape = 1
	MemBaseOffPre      MemShape = 2
	MemBaseOffPost     MemShape = 3
	MemBaseIdx         MemShape = 4
	MemBaseIdxShifted  MemShape = 5
	MemBaseIdxExtended MemShape = 6
)

// ShiftKind for SHIFTED_REG operands.
type ShiftKind byte

const (
	ShiftLSL ShiftKind = 0
	ShiftLSR ShiftKind = 1
	ShiftASR ShiftKind = 2
	ShiftROR ShiftKind = 3
)

func (s ShiftKind) Name() string {
	return [...]string{"lsl", "lsr", "asr", "ror"}[s]
}

// ExtendKind for EXTENDED_REG operands.
type ExtendKind byte

const (
	ExtUXTB ExtendKind = 0
	ExtUXTH ExtendKind = 1
	ExtUXTW ExtendKind = 2
	ExtUXTX ExtendKind = 3
	ExtSXTB ExtendKind = 4
	ExtSXTH ExtendKind = 5
	ExtSXTW ExtendKind = 6
	ExtSXTX ExtendKind = 7
)

func (e ExtendKind) Name() string {
	return [...]string{"uxtb", "uxth", "uxtw", "uxtx", "sxtb", "sxth", "sxtw", "sxtx"}[e]
}

// CondCode for COND operands. Values match the aarch64 encoding.
type CondCode byte

const (
	CondEQ CondCode = 0
	CondNE CondCode = 1
	CondCS CondCode = 2
	CondCC CondCode = 3
	CondMI CondCode = 4
	CondPL CondCode = 5
	CondVS CondCode = 6
	CondVC CondCode = 7
	CondHI CondCode = 8
	CondLS CondCode = 9
	CondGE CondCode = 10
	CondLT CondCode = 11
	CondGT CondCode = 12
	CondLE CondCode = 13
	CondAL CondCode = 14
	CondNV CondCode = 15
)

func (c CondCode) Name() string {
	return [...]string{
		"eq", "ne", "cs", "cc", "mi", "pl", "vs", "vc",
		"hi", "ls", "ge", "lt", "gt", "le", "al", "nv",
	}[c]
}

// OperandWriter accumulates encoded operand bytes.
type OperandWriter struct{ buf []byte }

func (w *OperandWriter) Bytes() []byte { return w.buf }

// WriteReg writes a register operand; kind must be one of OpRegX,
// OpRegW, OpRegXSP, OpRegWSP.
func (w *OperandWriter) WriteReg(kind OperandKind, reg byte) {
	w.buf = append(w.buf, byte(kind), reg)
}

// WriteImmExpr writes an IMM_EXPR operand carrying the given
// expression bytecode.
func (w *OperandWriter) WriteImmExpr(expr []byte) {
	w.buf = append(w.buf, byte(OpImmExpr))
	w.buf = appendU16(w.buf, uint16(len(expr)))
	w.buf = append(w.buf, expr...)
}

// WriteShiftedReg writes a SHIFTED_REG operand. width: 0=W, 1=X.
func (w *OperandWriter) WriteShiftedReg(width, reg byte, kind ShiftKind, amtExpr []byte) {
	w.buf = append(w.buf, byte(OpShiftedReg), width, reg, byte(kind))
	w.buf = appendU16(w.buf, uint16(len(amtExpr)))
	w.buf = append(w.buf, amtExpr...)
}

// WriteExtendedReg writes an EXTENDED_REG operand. amtExpr may be nil
// or empty (meaning no #N).
func (w *OperandWriter) WriteExtendedReg(width, reg byte, kind ExtendKind, amtExpr []byte) {
	w.buf = append(w.buf, byte(OpExtendedReg), width, reg, byte(kind))
	w.buf = appendU16(w.buf, uint16(len(amtExpr)))
	w.buf = append(w.buf, amtExpr...)
}

// WriteMemBase writes a MEM operand of shape [xn].
func (w *OperandWriter) WriteMemBase(base byte) {
	w.buf = append(w.buf, byte(OpMem), byte(MemBase), base)
}

// WriteMemBaseOff writes a MEM operand of shape [xn, #off], !, or post.
// shape must be MemBaseOff, MemBaseOffPre, or MemBaseOffPost.
func (w *OperandWriter) WriteMemBaseOff(shape MemShape, base byte, offExpr []byte) {
	w.buf = append(w.buf, byte(OpMem), byte(shape), base)
	w.buf = appendU16(w.buf, uint16(len(offExpr)))
	w.buf = append(w.buf, offExpr...)
}

// WriteMemBaseIdx writes a MEM operand of shape [xn, xm]. idxWidth: 0=W, 1=X.
func (w *OperandWriter) WriteMemBaseIdx(base, idx, idxWidth byte) {
	w.buf = append(w.buf, byte(OpMem), byte(MemBaseIdx), base, idx, idxWidth)
}

// WriteMemBaseIdxShifted writes a MEM operand of shape [xn, xm, lsl #N].
func (w *OperandWriter) WriteMemBaseIdxShifted(base, idx, idxWidth, shiftAmt byte) {
	w.buf = append(w.buf, byte(OpMem), byte(MemBaseIdxShifted),
		base, idx, idxWidth, shiftAmt)
}

// WriteMemBaseIdxExtended writes a MEM operand of shape
// [xn, wm/xm, extend #N].
func (w *OperandWriter) WriteMemBaseIdxExtended(base, idx, idxWidth byte, extend ExtendKind, shiftAmt byte) {
	w.buf = append(w.buf, byte(OpMem), byte(MemBaseIdxExtended),
		base, idx, idxWidth, byte(extend), shiftAmt)
}

func (w *OperandWriter) WriteString(s []byte) {
	w.buf = append(w.buf, byte(OpString))
	w.buf = appendU16(w.buf, uint16(len(s)))
	w.buf = append(w.buf, s...)
}

func (w *OperandWriter) WriteCond(c CondCode) {
	w.buf = append(w.buf, byte(OpCond), byte(c))
}

// WriteLitPool writes a LIT_POOL operand. width must be 4 (W
// destination, 4-byte pool entry) or 8 (X destination, 8-byte pool
// entry). expr is the value expression to place in the pool.
func (w *OperandWriter) WriteLitPool(width byte, expr []byte) {
	w.buf = append(w.buf, byte(OpLitPool), width)
	w.buf = appendU16(w.buf, uint16(len(expr)))
	w.buf = append(w.buf, expr...)
}

func (w *OperandWriter) WriteSysName(name string) {
	w.buf = append(w.buf, byte(OpSysName))
	w.buf = appendU16(w.buf, uint16(len(name)))
	w.buf = append(w.buf, name...)
}

func appendU16(buf []byte, v uint16) []byte {
	var tmp [2]byte
	binary.LittleEndian.PutUint16(tmp[:], v)
	return append(buf, tmp[:]...)
}

// Operand is a decoded operand record. Only the fields appropriate
// to Kind are populated; the rest are zero-valued.
type Operand struct {
	Kind OperandKind

	Reg      byte
	Width    byte
	Base     byte
	Idx      byte
	IdxWidth byte

	ShiftKind ShiftKind
	Extend    ExtendKind
	ShiftAmt  byte

	MemShape MemShape

	Expr    []byte
	AmtExpr []byte

	Str []byte

	Cond CondCode
}

// OperandReader walks an operand stream.
type OperandReader struct {
	buf []byte
	pos int
}

func NewOperandReader(buf []byte) *OperandReader {
	return &OperandReader{buf: buf}
}

func (r *OperandReader) AtEnd() bool { return r.pos >= len(r.buf) }

func (r *OperandReader) Next() (Operand, error) {
	if r.AtEnd() {
		return Operand{}, fmt.Errorf("operand: read past end")
	}
	kind := OperandKind(r.buf[r.pos])
	r.pos++
	var o Operand
	o.Kind = kind
	switch kind {
	case OpRegX, OpRegW, OpRegXSP, OpRegWSP:
		o.Reg = r.take(1)[0]
	case OpImmExpr:
		n := r.readLen()
		o.Expr = r.take(n)
	case OpShiftedReg:
		o.Width = r.take(1)[0]
		o.Reg = r.take(1)[0]
		o.ShiftKind = ShiftKind(r.take(1)[0])
		n := r.readLen()
		o.AmtExpr = r.take(n)
	case OpExtendedReg:
		o.Width = r.take(1)[0]
		o.Reg = r.take(1)[0]
		o.Extend = ExtendKind(r.take(1)[0])
		n := r.readLen()
		o.AmtExpr = r.take(n)
	case OpMem:
		o.MemShape = MemShape(r.take(1)[0])
		switch o.MemShape {
		case MemBase:
			o.Base = r.take(1)[0]
		case MemBaseOff, MemBaseOffPre, MemBaseOffPost:
			o.Base = r.take(1)[0]
			n := r.readLen()
			o.Expr = r.take(n)
		case MemBaseIdx:
			o.Base = r.take(1)[0]
			o.Idx = r.take(1)[0]
			o.IdxWidth = r.take(1)[0]
		case MemBaseIdxShifted:
			o.Base = r.take(1)[0]
			o.Idx = r.take(1)[0]
			o.IdxWidth = r.take(1)[0]
			o.ShiftAmt = r.take(1)[0]
		case MemBaseIdxExtended:
			o.Base = r.take(1)[0]
			o.Idx = r.take(1)[0]
			o.IdxWidth = r.take(1)[0]
			o.Extend = ExtendKind(r.take(1)[0])
			o.ShiftAmt = r.take(1)[0]
		default:
			return o, fmt.Errorf("operand: unknown MemShape %d", o.MemShape)
		}
	case OpString, OpSysName:
		n := r.readLen()
		o.Str = r.take(n)
	case OpCond:
		o.Cond = CondCode(r.take(1)[0])
	case OpLitPool:
		o.Width = r.take(1)[0]
		n := r.readLen()
		o.Expr = r.take(n)
	default:
		return o, fmt.Errorf("operand: unknown kind 0x%02x", byte(kind))
	}
	return o, nil
}

func (r *OperandReader) take(n int) []byte {
	if r.pos+n > len(r.buf) {
		s := r.buf[r.pos:]
		r.pos = len(r.buf)
		return s
	}
	s := r.buf[r.pos : r.pos+n]
	r.pos += n
	return s
}

func (r *OperandReader) readLen() int {
	if r.pos+2 > len(r.buf) {
		return 0
	}
	n := int(binary.LittleEndian.Uint16(r.buf[r.pos:]))
	r.pos += 2
	return n
}
