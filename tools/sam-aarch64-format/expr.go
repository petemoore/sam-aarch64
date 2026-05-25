package format

import (
	"encoding/binary"
	"fmt"
)

// ExprOp is one byte of expression bytecode (§5).
type ExprOp byte

const (
	OpPushImm8  ExprOp = 0x01
	OpPushImm16 ExprOp = 0x02
	OpPushImm32 ExprOp = 0x03
	OpPushImm64 ExprOp = 0x04
	OpPushSym   ExprOp = 0x05
	OpPushLocal ExprOp = 0x06
	OpPushPC    ExprOp = 0x07

	OpAdd ExprOp = 0x10
	OpSub ExprOp = 0x11
	OpMul ExprOp = 0x12
	OpDiv ExprOp = 0x13
	OpAnd ExprOp = 0x14
	OpOr  ExprOp = 0x15
	OpXor ExprOp = 0x16
	OpShl ExprOp = 0x17
	OpShr ExprOp = 0x18

	OpNeg ExprOp = 0x20
	OpNot ExprOp = 0x21

	OpRelLo12    ExprOp = 0x30
	OpRelHi12    ExprOp = 0x31
	OpRelAbsG0   ExprOp = 0x32
	OpRelAbsG0NC ExprOp = 0x33
	OpRelAbsG1   ExprOp = 0x34
	OpRelAbsG1NC ExprOp = 0x35
	OpRelAbsG2   ExprOp = 0x36
	OpRelAbsG2NC ExprOp = 0x37
	OpRelAbsG3   ExprOp = 0x38
)

func (o ExprOp) Name() string {
	switch o {
	case OpPushImm8:
		return "PUSH_IMM8"
	case OpPushImm16:
		return "PUSH_IMM16"
	case OpPushImm32:
		return "PUSH_IMM32"
	case OpPushImm64:
		return "PUSH_IMM64"
	case OpPushSym:
		return "PUSH_SYM"
	case OpPushLocal:
		return "PUSH_LOCAL"
	case OpPushPC:
		return "PUSH_PC"
	case OpAdd:
		return "ADD"
	case OpSub:
		return "SUB"
	case OpMul:
		return "MUL"
	case OpDiv:
		return "DIV"
	case OpAnd:
		return "AND"
	case OpOr:
		return "OR"
	case OpXor:
		return "XOR"
	case OpShl:
		return "SHL"
	case OpShr:
		return "SHR"
	case OpNeg:
		return "NEG"
	case OpNot:
		return "NOT"
	case OpRelLo12:
		return "REL_LO12"
	case OpRelHi12:
		return "REL_HI12"
	case OpRelAbsG0:
		return "REL_ABS_G0"
	case OpRelAbsG0NC:
		return "REL_ABS_G0_NC"
	case OpRelAbsG1:
		return "REL_ABS_G1"
	case OpRelAbsG1NC:
		return "REL_ABS_G1_NC"
	case OpRelAbsG2:
		return "REL_ABS_G2"
	case OpRelAbsG2NC:
		return "REL_ABS_G2_NC"
	case OpRelAbsG3:
		return "REL_ABS_G3"
	}
	return "UNKNOWN"
}

// ExprWriter builds an expression bytecode stream.
type ExprWriter struct {
	buf []byte
}

// Bytes returns the accumulated bytecode. The caller must not mutate
// the returned slice.
func (w *ExprWriter) Bytes() []byte { return w.buf }

// Reset clears the writer for reuse.
func (w *ExprWriter) Reset() { w.buf = w.buf[:0] }

// AppendRaw appends raw expression bytecode to the writer.
// Used by pseudo-instruction expanders that need to compose a new
// expression from an already-encoded sub-expression.
func (w *ExprWriter) AppendRaw(b []byte) {
	w.buf = append(w.buf, b...)
}

// WriteOp writes a 0-argument opcode (binary or unary operator).
func (w *ExprWriter) WriteOp(op ExprOp) {
	w.buf = append(w.buf, byte(op))
}

// WriteSym writes PUSH_SYM with a u16 LE symbol id.
func (w *ExprWriter) WriteSym(id uint16) {
	w.buf = append(w.buf, byte(OpPushSym), byte(id), byte(id>>8))
}

// WriteLocal writes PUSH_LOCAL with a digit (1–9) and a direction
// byte (0=f, 1=b).
func (w *ExprWriter) WriteLocal(digit, dir byte) {
	w.buf = append(w.buf, byte(OpPushLocal), digit, dir)
}

// WritePC writes the PUSH_PC opcode (the `.` operator).
func (w *ExprWriter) WritePC() { w.buf = append(w.buf, byte(OpPushPC)) }

// WriteImm writes a literal using the shortest PUSH_IMMn that fits.
func (w *ExprWriter) WriteImm(v int64) {
	switch {
	case v >= -128 && v <= 127:
		w.buf = append(w.buf, byte(OpPushImm8), byte(v))
	case v >= -32768 && v <= 32767:
		w.buf = append(w.buf, byte(OpPushImm16), byte(v), byte(v>>8))
	case v >= -(int64(1)<<31) && v <= (int64(1)<<31)-1:
		w.buf = append(w.buf, byte(OpPushImm32),
			byte(v), byte(v>>8), byte(v>>16), byte(v>>24))
	default:
		w.buf = append(w.buf, byte(OpPushImm64),
			byte(v), byte(v>>8), byte(v>>16), byte(v>>24),
			byte(v>>32), byte(v>>40), byte(v>>48), byte(v>>56))
	}
}

// ExprReader walks a bytecode stream one opcode at a time.
type ExprReader struct {
	buf []byte
	pos int
}

func NewExprReader(buf []byte) *ExprReader {
	return &ExprReader{buf: buf}
}

// Next returns the next opcode plus its inline operand (raw bytes,
// not parsed). For 0-argument opcodes the slice is nil.
func (r *ExprReader) Next() (ExprOp, []byte, error) {
	if r.pos >= len(r.buf) {
		return 0, nil, fmt.Errorf("expr: read past end")
	}
	op := ExprOp(r.buf[r.pos])
	r.pos++
	width := operandWidth(op)
	if width < 0 {
		return 0, nil, fmt.Errorf("expr: unknown opcode 0x%02x", byte(op))
	}
	if r.pos+width > len(r.buf) {
		return 0, nil, fmt.Errorf("expr: truncated operand for %s", op.Name())
	}
	operand := r.buf[r.pos : r.pos+width]
	r.pos += width
	return op, operand, nil
}

// AtEnd reports whether the reader has consumed the whole stream.
func (r *ExprReader) AtEnd() bool { return r.pos >= len(r.buf) }

// operandWidth returns the inline-operand size in bytes for an
// opcode, or -1 for unknown opcodes.
func operandWidth(op ExprOp) int {
	switch op {
	case OpPushImm8:
		return 1
	case OpPushImm16:
		return 2
	case OpPushImm32:
		return 4
	case OpPushImm64:
		return 8
	case OpPushSym:
		return 2
	case OpPushLocal:
		return 2
	case OpPushPC,
		OpAdd, OpSub, OpMul, OpDiv,
		OpAnd, OpOr, OpXor, OpShl, OpShr,
		OpNeg, OpNot,
		OpRelLo12, OpRelHi12,
		OpRelAbsG0, OpRelAbsG0NC,
		OpRelAbsG1, OpRelAbsG1NC,
		OpRelAbsG2, OpRelAbsG2NC,
		OpRelAbsG3:
		return 0
	}
	return -1
}

// EvalConst returns the value of a bytecode stream when every push
// operation is a literal. If any PUSH_SYM / PUSH_LOCAL / PUSH_PC or
// REL_* opcode is encountered, ok=false. Used by text2bin's
// constant-folder.
func EvalConst(buf []byte) (int64, bool) {
	r := NewExprReader(buf)
	stack := make([]int64, 0, 8)
	for !r.AtEnd() {
		op, operand, err := r.Next()
		if err != nil {
			return 0, false
		}
		switch op {
		case OpPushImm8:
			stack = append(stack, int64(int8(operand[0])))
		case OpPushImm16:
			stack = append(stack, int64(int16(binary.LittleEndian.Uint16(operand))))
		case OpPushImm32:
			stack = append(stack, int64(int32(binary.LittleEndian.Uint32(operand))))
		case OpPushImm64:
			stack = append(stack, int64(binary.LittleEndian.Uint64(operand)))
		case OpPushSym, OpPushLocal, OpPushPC,
			OpRelLo12, OpRelHi12,
			OpRelAbsG0, OpRelAbsG0NC,
			OpRelAbsG1, OpRelAbsG1NC,
			OpRelAbsG2, OpRelAbsG2NC,
			OpRelAbsG3:
			return 0, false
		case OpAdd, OpSub, OpMul, OpDiv,
			OpAnd, OpOr, OpXor, OpShl, OpShr:
			if len(stack) < 2 {
				return 0, false
			}
			b := stack[len(stack)-1]
			a := stack[len(stack)-2]
			stack = stack[:len(stack)-2]
			stack = append(stack, applyBinary(op, a, b))
		case OpNeg:
			if len(stack) < 1 {
				return 0, false
			}
			stack[len(stack)-1] = -stack[len(stack)-1]
		case OpNot:
			if len(stack) < 1 {
				return 0, false
			}
			stack[len(stack)-1] = ^stack[len(stack)-1]
		default:
			return 0, false
		}
	}
	if len(stack) != 1 {
		return 0, false
	}
	return stack[0], true
}

func applyBinary(op ExprOp, a, b int64) int64 {
	switch op {
	case OpAdd:
		return a + b
	case OpSub:
		return a - b
	case OpMul:
		return a * b
	case OpDiv:
		if b == 0 {
			return 0
		}
		return a / b
	case OpAnd:
		return a & b
	case OpOr:
		return a | b
	case OpXor:
		return a ^ b
	case OpShl:
		return a << uint64(b)
	case OpShr:
		return a >> uint64(b)
	}
	return 0
}
